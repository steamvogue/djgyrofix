// Package correct turns detected artifacts into replacement quaternions.
//
// Two paths live here. Legacy is a literal port of the reference's
// smooth_quaternions and exists so `fix --ranges` stays byte-identical to the
// Python tool; it is the golden-parity gate that guards every later change.
// Envelope is the new continuous-weight path used by automatic detection.
package correct

import (
	"math"
	"sort"

	"github.com/steamvogue/djgyrofix/internal/quat"
)

// Error reports invalid smoothing input, where the reference raises ValueError.
type Error string

func (e Error) Error() string { return string(e) }

// LegacyParams mirrors the reference's smoothing knobs.
type LegacyParams struct {
	StartSeconds float64
	EndSeconds   float64
	SmoothingMS  float64
	Strength     float64
}

// DefaultSmoothingMS is the reference's fixed smoothing window.
const DefaultSmoothingMS = 180.0

// Legacy smooths only the requested interval and restores each source
// quaternion's sign, reproducing gyrofix.smoothing.smooth_quaternions exactly.
//
// Three moving-average passes approximate a Gaussian low-pass. Filtering runs
// on components after sign unwrapping; every result is renormalized and blended
// back toward the source near the interval boundaries so the correction does
// not step at the edges.
func Legacy(times []float64, quaternions []quat.Q, params LegacyParams) ([]quat.Q, error) {
	if len(times) != len(quaternions) {
		return nil, Error("time and quaternion arrays have different lengths")
	}
	if len(quaternions) == 0 {
		return []quat.Q{}, nil
	}
	if params.EndSeconds <= params.StartSeconds {
		return nil, Error("end time must be after start time")
	}
	if math.IsNaN(params.SmoothingMS) || math.IsInf(params.SmoothingMS, 0) || params.SmoothingMS <= 0 {
		return nil, Error("smoothing duration must be positive")
	}
	if math.IsNaN(params.Strength) || math.IsInf(params.Strength, 0) {
		return nil, Error("smoothing strength must be finite")
	}
	for _, time := range times {
		if math.IsNaN(time) || math.IsInf(time, 0) {
			return nil, Error("sample times must be finite")
		}
	}

	source := make([]quat.Q, len(quaternions))
	for index, value := range quaternions {
		unit, err := quat.Normalize(value)
		if err != nil {
			return nil, err
		}
		source[index] = unit
	}
	continuous := quat.Unwrap(source)

	var intervals []float64
	for index := 0; index+1 < len(times); index++ {
		delta := times[index+1] - times[index]
		if times[index+1] > times[index] && !math.IsNaN(delta) && !math.IsInf(delta, 0) {
			intervals = append(intervals, delta)
		}
	}
	if len(intervals) == 0 {
		// No usable timing: hand back the raw input untouched, as the
		// reference does — not the normalized copy.
		return append([]quat.Q(nil), quaternions...), nil
	}
	head := intervals
	if len(head) > 5000 {
		head = head[:5000]
	}
	sampleInterval := quat.Median(head)
	sigmaSamples := quat.PyRound((params.SmoothingMS / 1000.0) / sampleInterval)
	if sigmaSamples < 1 {
		sigmaSamples = 1
	}

	filtered := continuous
	for pass := 0; pass < 3; pass++ {
		var err error
		if filtered, err = quat.BoxBlur(filtered, sigmaSamples); err != nil {
			return nil, err
		}
	}

	duration := params.EndSeconds - params.StartSeconds
	edgeSeconds := math.Min(0.20, duration*0.15)
	strength := math.Min(1.0, math.Max(0.0, params.Strength))
	startIndex := nearestIndex(times, params.StartSeconds)
	endIndex := nearestIndex(times, params.EndSeconds)

	startCorrection, err := correctionAt(continuous[startIndex], filtered[startIndex])
	if err != nil {
		return nil, err
	}
	endCorrection, err := correctionAt(continuous[endIndex], filtered[endIndex])
	if err != nil {
		return nil, err
	}

	output := make([]quat.Q, 0, len(times))
	for index, time := range times {
		original := source[index]
		if time <= params.StartSeconds || time >= params.EndSeconds {
			output = append(output, original)
			continue
		}
		value := filtered[index]
		if edgeSeconds > 0 && time < params.StartSeconds+edgeSeconds {
			correction, err := quat.Slerp(startCorrection, quat.Identity,
				quat.Smoothstep((time-params.StartSeconds)/edgeSeconds))
			if err != nil {
				return nil, err
			}
			if value, err = quat.Multiply(correction, value); err != nil {
				return nil, err
			}
		}
		if edgeSeconds > 0 && time > params.EndSeconds-edgeSeconds {
			correction, err := quat.Slerp(quat.Identity, endCorrection,
				quat.Smoothstep((time-(params.EndSeconds-edgeSeconds))/edgeSeconds))
			if err != nil {
				return nil, err
			}
			if value, err = quat.Multiply(correction, value); err != nil {
				return nil, err
			}
		}
		if strength < 1.0 {
			if value, err = quat.Slerp(continuous[index], value, strength); err != nil {
				return nil, err
			}
		}
		if quat.Dot(value, original) < 0.0 {
			value = value.Neg()
		}
		output = append(output, value)
	}
	return output, nil
}

// correctionAt is the rotation carrying the filtered orientation back onto the
// original one, sign-normalized so it interpolates toward identity the short way.
func correctionAt(original, filtered quat.Q) (quat.Q, error) {
	inverse, err := quat.Inverse(filtered)
	if err != nil {
		return quat.Q{}, err
	}
	correction, err := quat.Multiply(original, inverse)
	if err != nil {
		return quat.Q{}, err
	}
	if correction[0] < 0.0 {
		correction = correction.Neg()
	}
	return correction, nil
}

// nearestIndex returns the first index whose time is closest to target,
// matching Python's min(range(n), key=...) tie-breaking.
func nearestIndex(times []float64, target float64) int {
	best := 0
	bestDistance := math.Abs(times[0] - target)
	for index := 1; index < len(times); index++ {
		distance := math.Abs(times[index] - target)
		if distance < bestDistance {
			best, bestDistance = index, distance
		}
	}
	return best
}

// AngularAccelerationScore is the median absolute angular acceleration inside a
// window, in degrees per second squared expressed in radians per second — the
// reference's before/after quality metric, ported unchanged so the reported
// improvement percentage matches.
// AccelerationScorer normalizes and differentiates one quaternion track once.
// Individual event scores then inspect only the accelerations in that event,
// rather than traversing the full track again.
type AccelerationScorer struct {
	leftTimes  []float64
	rightTimes []float64
	values     []float64
}

// PrepareAngularAcceleration builds a reusable event scorer in O(samples).
func PrepareAngularAcceleration(times []float64, quaternions []quat.Q) (*AccelerationScorer, error) {
	if len(times) != len(quaternions) {
		return nil, Error("time and quaternion arrays have different lengths")
	}
	normalized := make([]quat.Q, len(quaternions))
	for index, value := range quaternions {
		unit, err := quat.Normalize(value)
		if err != nil {
			return nil, err
		}
		normalized[index] = unit
	}
	type velocity struct{ time, value float64 }
	var velocities []velocity
	for index := 1; index < len(times); index++ {
		interval := times[index] - times[index-1]
		if interval <= 0 {
			continue
		}
		cosine := math.Min(1.0, math.Max(-1.0, math.Abs(quat.Dot(normalized[index-1], normalized[index]))))
		angle := 2.0 * math.Acos(cosine)
		velocities = append(velocities, velocity{times[index], angle / interval})
	}
	scorer := &AccelerationScorer{}
	for index := 0; index+1 < len(velocities); index++ {
		interval := velocities[index+1].time - velocities[index].time
		if interval > 0 {
			scorer.leftTimes = append(scorer.leftTimes, velocities[index].time)
			scorer.rightTimes = append(scorer.rightTimes, velocities[index+1].time)
			scorer.values = append(scorer.values,
				math.Abs(velocities[index+1].value-velocities[index].value)/interval)
		}
	}
	return scorer, nil
}

// Score returns the median absolute angular acceleration in [start, end].
func (s *AccelerationScorer) Score(start, end float64) float64 {
	if s == nil || len(s.values) == 0 || end < start {
		return 0
	}
	first := sort.Search(len(s.leftTimes), func(index int) bool {
		return s.leftTimes[index] >= start
	})
	values := make([]float64, 0)
	for index := first; index < len(s.values) && s.rightTimes[index] <= end; index++ {
		values = append(values, s.values[index])
	}
	if len(values) == 0 {
		return 0
	}
	return quat.Median(values)
}

// AngularAccelerationScore is the compatibility wrapper for callers scoring a
// single range. Multi-event callers should prepare one scorer and reuse it.
func AngularAccelerationScore(times []float64, quaternions []quat.Q, start, end float64) (float64, error) {
	scorer, err := PrepareAngularAcceleration(times, quaternions)
	if err != nil {
		return 0, err
	}
	return scorer.Score(start, end), nil
}
