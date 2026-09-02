package correct

import (
	"fmt"
	"math"

	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/quat"
)

// Run-repair replaces the samples that do not belong instead of blurring
// everything around them.
//
// The envelope pass low-passes a whole detected event. That is the wrong shape
// for this artifact. Measured on the real clip, the per-sample residual crosses
// four times its median in 3,145 runs whose median length is 4.04 ms and whose
// p90 is 25.28 ms — inside detected events that routinely span hundreds of
// milliseconds. Blurring the event smooths 300 ms of genuine motion to remove
// 4 ms of overshoot, which is why the corrected footage went soft while the
// spike survived.
//
// Replacing a run interpolates along the arc between the last good orientation
// before it and the first good one after, exactly as a dropout bridge does, and
// leaves every sample outside the run byte-identical.
//
// The danger is the mirror image of the benefit, and it is the reason this is
// gated rather than unconditional: these runs cluster on sharp movements, where
// the true trajectory has the most curvature and an interpolation is most
// likely to invent an orientation the aircraft never held. A run is therefore
// only replaced when its endpoints are still consistent with the surrounding
// motion — when the deviation departs and returns. A run whose endpoints have
// themselves moved somewhere new is real motion, and is left to the blur.

// RepairOptions controls run-repair.
type RepairOptions struct {
	// MaxRunSeconds is the longest run that may be replaced. Beyond it the
	// interpolation would be fabricating more orientation than it removes.
	MaxRunSeconds float64
	// ExcessK is how far over the local threshold a sample must sit to count as
	// part of a run.
	ExcessK float64
	// TrendTolerance is how far the endpoint-to-endpoint rotation may differ
	// from what the local trend predicts, as a fraction of that prediction,
	// before the run is treated as real motion.
	TrendTolerance float64
	// TrendFloorDegrees is an absolute allowance added to that tolerance, so a
	// nearly-stationary run is not rejected for a fraction of a degree.
	TrendFloorDegrees float64
	// AlongAxis selects runs by the plain residual magnitude instead of by the
	// part of it perpendicular to the local rotation axis. It exists so the two
	// can be compared; the perpendicular measure is the better one.
	//
	// A residual pointing along the axis already being turned about means the
	// aircraft turned faster or slower than the local trend, which is flying.
	// Perpendicular means the axis itself moved, which is the artifact. Through
	// one 366 °/s exit rotation on the real clip the residual is 29% across
	// axis; through the wobble 400 ms later it is 92%. Selecting runs by the
	// plain magnitude cuts into the first to reach the second.
	AlongAxis bool
}

// DefaultRepairOptions are the measured starting points.
//
// MaxRunSeconds sits just past the p90 run length on the real clip: it covers
// the bulk of the population while refusing the 151 runs over 50 ms, the
// longest of which is 320 ms and could not be interpolated without inventing a
// third of a second of attitude.
func DefaultRepairOptions() RepairOptions {
	return RepairOptions{
		MaxRunSeconds:     0.030,
		ExcessK:           1.0,
		TrendTolerance:    0.5,
		TrendFloorDegrees: 2.0,
	}
}

// RepairStats reports what run-repair did, for the report.
type RepairStats struct {
	// RunsReplaced is how many runs were interpolated.
	RunsReplaced int `json:"runs_replaced"`
	// SamplesReplaced is how many quaternions those runs covered.
	SamplesReplaced int `json:"samples_replaced"`
	// RunsTooLong were over MaxRunSeconds and left to the blur.
	RunsTooLong int `json:"runs_too_long"`
	// RunsRealMotion failed the departs-and-returns test.
	RunsRealMotion int `json:"runs_real_motion"`
}

// RepairRuns replaces short out-of-trend runs inside smoothing events.
//
// It runs before the envelope and writes into the same working series, so a run
// this pass has already fixed is what any later blur sees.
// It returns, per event, whether every supra-threshold run inside it was
// replaced. Those events need no blur: replacing the runs has already restored
// the trajectory, and blurring on top would smooth the genuine motion the
// repair was careful to leave alone — which is the whole reason for this pass.
func RepairRuns(
	times []float64,
	working, output []quat.Q,
	result *detect.Result,
	options RepairOptions,
) (RepairStats, []bool, error) {
	var stats RepairStats
	handled := make([]bool, len(result.Events))
	if len(result.Residual) != len(times) || len(result.PointThreshold) != len(times) {
		return stats, handled, nil
	}
	for eventIndex, event := range result.Events {
		if event.Action != detect.ActionSmooth {
			continue
		}
		runs, unhandled := 0, 0
		first := max(0, event.FirstPoint)
		last := min(len(times)-1, event.LastPoint)
		for index := first; index <= last; index++ {
			if !overThreshold(result, index, options.ExcessK, options.AlongAxis) {
				continue
			}
			runEnd := index
			for runEnd+1 <= last && overThreshold(result, runEnd+1, options.ExcessK, options.AlongAxis) {
				runEnd++
			}
			runs++
			replaced, err := repairRun(times, working, output, result, index, runEnd, options, &stats)
			if err != nil {
				return stats, handled, err
			}
			if replaced {
				stats.RunsReplaced++
				stats.SamplesReplaced += runEnd - index + 1
			} else {
				unhandled++
			}
			index = runEnd
		}
		// An event with nothing over the per-sample bar was detected on binned
		// energy alone. There is no run to replace, so the blur is still the
		// only tool for it.
		handled[eventIndex] = runs > 0 && unhandled == 0
	}
	return stats, handled, nil
}

func overThreshold(result *detect.Result, index int, k float64, alongAxis bool) bool {
	measure := result.Residual
	if !alongAxis && len(result.ResidualAcross) == len(result.Residual) {
		measure = result.ResidualAcross
	}
	return measure[index] > result.PointThreshold[index]*k
}

// repairRun decides on one run and, if it passes, interpolates it.
func repairRun(
	times []float64,
	working, output []quat.Q,
	result *detect.Result,
	first, last int,
	options RepairOptions,
	stats *RepairStats,
) (bool, error) {
	before := first - 1
	after := last + 1
	if before < 0 || after >= len(working) {
		return false, nil
	}
	span := times[after] - times[before]
	if span <= 0 {
		return false, nil
	}
	if times[last]-times[first] > options.MaxRunSeconds {
		stats.RunsTooLong++
		return false, nil
	}
	// A dropout is already reconstructed by the bridge pass, from a stricter
	// gate. Leave it alone.
	if len(result.Implausible) == len(working) {
		for index := first; index <= last; index++ {
			if result.Implausible[index] {
				return false, nil
			}
		}
	}
	if !departsAndReturns(times, working, result, before, after, options) {
		stats.RunsRealMotion++
		return false, nil
	}

	for index := first; index <= last; index++ {
		amount := (times[index] - times[before]) / span
		value, err := quat.Slerp(working[before], working[after], amount)
		if err != nil {
			return false, fmt.Errorf("repairing the run at %.3fs: %w", times[index], err)
		}
		// Align to the preceding good sample, not to the original: the
		// original's sign carries no information once it is being replaced,
		// and matching it could introduce a double-cover flip mid-run.
		if quat.Dot(value, working[before]) < 0.0 {
			value = value.Neg()
		}
		working[index] = value
		output[index] = value
	}
	return true, nil
}

// departsAndReturns is the guard that keeps this from fabricating orientation.
//
// The artifact is an excursion: the attitude leaves the trajectory and comes
// back to it. If the orientations bracketing a run are still where the
// surrounding motion says they should be, whatever happened between them was an
// excursion and replacing it restores the trajectory. If the brackets have
// themselves moved somewhere the trend does not predict, the aircraft really
// went there, and interpolating would erase a movement that happened.
func departsAndReturns(
	times []float64,
	working []quat.Q,
	result *detect.Result,
	before, after int,
	options RepairOptions,
) bool {
	span := times[after] - times[before]
	if span <= 0 {
		return false
	}
	// What the low-passed motion either side of the run predicts it should
	// have rotated through, in degrees.
	trend := 0.0
	if len(result.Trend) == len(working) {
		trend = (result.Trend[before] + result.Trend[after]) / 2.0
	}
	predicted := trend * span

	actual, err := angleBetween(working[before], working[after])
	if err != nil {
		return false
	}
	allowance := math.Abs(predicted)*options.TrendTolerance + options.TrendFloorDegrees
	return math.Abs(actual-predicted) <= allowance
}

// angleBetween is the rotation in degrees between two orientations.
func angleBetween(a, b quat.Q) (float64, error) {
	inverse, err := quat.Inverse(a)
	if err != nil {
		return 0, err
	}
	delta, err := quat.Multiply(b, inverse)
	if err != nil {
		return 0, err
	}
	if delta[0] < 0.0 {
		delta = delta.Neg()
	}
	vector := math.Sqrt(float64(delta[1]*delta[1]) + float64(delta[2]*delta[2]) + float64(delta[3]*delta[3]))
	return quat.Degrees(2.0 * math.Atan2(vector, math.Max(0.0, delta[0]))), nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
