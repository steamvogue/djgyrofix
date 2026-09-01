package correct

import (
	"fmt"
	"math"

	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/quat"
)

// EnvelopeOptions controls pass 2.
type EnvelopeOptions struct {
	// Strength is a global multiplier on the correction weight, 0 to 1.
	Strength float64
	// SmoothingMS overrides the per-event window derivation when non-zero.
	SmoothingMS float64
	// RepairRuns replaces short out-of-trend runs by interpolation before any
	// blurring, instead of blurring across them.
	RepairRuns bool
	// Repair configures that pass.
	Repair RepairOptions
	// Stats receives what run-repair did, when it runs.
	Stats *RepairStats
}

// Envelope applies the corrections pass 1 asked for and returns the full point
// series, with untouched points carrying their original values byte for byte.
//
// That last part matters: returning a normalized copy of an untouched
// quaternion would change its float32 representation in the last bit and
// generate a write for a sample nothing was wrong with. A clean clip must
// produce zero writes.
func Envelope(times []float64, values []quat.Q, result *detect.Result, options EnvelopeOptions) ([]quat.Q, error) {
	if len(times) != len(values) {
		return nil, Error("time and quaternion arrays have different lengths")
	}
	strength := math.Min(1.0, math.Max(0.0, options.Strength))
	output := append([]quat.Q(nil), values...)
	if len(values) == 0 || strength == 0 {
		return output, nil
	}

	normalized := make([]quat.Q, len(values))
	for index, value := range values {
		unit, err := quat.Normalize(value)
		if err != nil {
			return nil, err
		}
		normalized[index] = unit
	}
	working := append([]quat.Q(nil), normalized...)

	// Two events whose weight shoulders overlap must not both correct the same
	// point; the second pass would smooth an already-smoothed value with a
	// different window. Events are in time order, so the first one wins.
	corrected := make([]bool, len(values))

	// Reconstruct every confirmed corrupt run before filtering anything. A
	// smoothing event may begin before a contained dropout, so relying on event
	// order would otherwise filter corrupt input and later bridge from stale
	// neighbours.
	for _, event := range result.Events {
		if event.Action != detect.ActionBridge {
			continue
		}
		if err := bridge(times, working, output, event, result.Implausible); err != nil {
			return nil, err
		}
	}
	// Run-repair goes between the bridges and the blur. A run it replaces is
	// no longer over threshold, so the blur that follows sees a trajectory that
	// has already been restored and leaves the surrounding motion alone.
	repaired := make([]bool, len(result.Events))
	if options.RepairRuns {
		stats, handled, err := RepairRuns(times, working, output, result, options.Repair)
		if err != nil {
			return nil, err
		}
		repaired = handled
		if options.Stats != nil {
			options.Stats.RunsReplaced += stats.RunsReplaced
			options.Stats.SamplesReplaced += stats.SamplesReplaced
			options.Stats.RunsTooLong += stats.RunsTooLong
			options.Stats.RunsRealMotion += stats.RunsRealMotion
		}
	}

	for eventIndex, event := range result.Events {
		if event.Action != detect.ActionSmooth {
			continue
		}
		if repaired[eventIndex] {
			continue
		}
		window := options.SmoothingMS
		if window <= 0 {
			window = event.SmoothingMS
		}
		if err := smoothEvent(times, working, output, result, event, window, strength, corrected); err != nil {
			return nil, err
		}
	}
	return output, nil
}

// bridge reconstructs a short run of corrupt samples by interpolating between
// the last good orientation before it and the first good one after, weighted by
// timestamp.
//
// Blurring a dropout would destroy the surrounding motion dynamics; a slerp
// preserves the real motion exactly and removes only the glitch. This runs only
// on samples that failed the plausibility gate, which is the hard precondition
// from plan §5.3 — bridging genuine violent motion would fabricate orientation.
func bridge(times []float64, working, output []quat.Q, event detect.Event, implausible []bool) error {
	before := event.FirstPoint - 1
	for before >= 0 && before < len(implausible) && implausible[before] {
		before--
	}
	after := event.LastPoint + 1
	for after < len(implausible) && implausible[after] {
		after++
	}
	if before < 0 || after >= len(working) {
		return nil
	}
	span := times[after] - times[before]
	if span <= 0 {
		return nil
	}
	for index := event.FirstPoint; index <= event.LastPoint && index < len(output); index++ {
		amount := (times[index] - times[before]) / span
		value, err := quat.Slerp(working[before], working[after], amount)
		if err != nil {
			return fmt.Errorf("bridge at %.3fs: %w", times[index], err)
		}
		// Align to the preceding good sample rather than to the corrupt
		// original: the original's sign carries no information here, and
		// matching it could introduce a double-cover flip mid-run.
		if quat.Dot(value, working[before]) < 0.0 {
			value = value.Neg()
		}
		working[index] = value
		output[index] = value
	}
	return nil
}

// smoothEvent low-passes one event's span and blends toward it by the weight
// envelope.
//
// There is no edge correction here and none is needed. The reference blends
// its filtered track back toward the source over a fixed 200 ms shoulder at
// each end of a hand-picked range, because the filter output steps at the
// boundary. Here w(t) already falls to zero continuously at both edges by
// construction, so the correction tapers itself.
func smoothEvent(times []float64, working, output []quat.Q, result *detect.Result, event detect.Event, smoothingMS, strength float64, corrected []bool) error {
	if smoothingMS <= 0 {
		return nil
	}
	interval := result.SampleInterval
	if interval <= 0 {
		return nil
	}
	// A centered three-pass box filter reaches three radii either side; give it
	// that much real data so the span edges are not averaged against nothing.
	contextSeconds := math.Max(0.75, smoothingMS/1000.0*4.0)
	contextPoints := int(contextSeconds/interval) + 1
	first := event.FirstPoint - contextPoints
	if first < 0 {
		first = 0
	}
	last := event.LastPoint + contextPoints
	if last > len(working)-1 {
		last = len(working) - 1
	}
	if last <= first {
		return nil
	}

	radius := quat.PyRound((smoothingMS / 1000.0) / interval)
	if radius < 1 {
		radius = 1
	}
	continuous := quat.Unwrap(working[first : last+1])
	filtered := continuous
	for pass := 0; pass < 3; pass++ {
		var err error
		if filtered, err = quat.BoxBlur(filtered, radius); err != nil {
			return err
		}
	}

	// Apply across the weight's whole support, not just the event bounds. The
	// envelope is blurred, so its shoulders reach outside the event — and
	// clipping the correction to the event bounds would drop the taper on the
	// floor and step back to the source exactly where w is still non-zero.
	// That is the discontinuity the envelope exists to prevent, so cutting it
	// off here would reintroduce it.
	applyFirst, applyLast := event.FirstPoint, event.LastPoint
	for applyFirst > first && result.Weights[applyFirst-1] > 0 {
		applyFirst--
	}
	for applyLast < last && result.Weights[applyLast+1] > 0 {
		applyLast++
	}

	for index := applyFirst; index <= applyLast && index < len(output); index++ {
		if corrected[index] {
			continue
		}
		weight := result.Weights[index] * strength
		if weight <= 0 {
			continue
		}
		if weight > 1 {
			weight = 1
		}
		local := index - first
		value, err := quat.Slerp(continuous[local], filtered[local], weight)
		if err != nil {
			return fmt.Errorf("smooth at %.3fs: %w", times[index], err)
		}
		// Restore the source quaternion's sign, as the reference does, so the
		// patched track keeps the same double-cover convention as the file.
		if quat.Dot(value, working[index]) < 0.0 {
			value = value.Neg()
		}
		output[index] = value
		corrected[index] = true
	}
	return nil
}
