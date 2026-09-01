// Package quat implements the quaternion math used by djgyrofix.
//
// Component order is (w, x, y, z) throughout, matching DJI's on-wire layout and
// the Python reference implementation — not the (x, y, z, w) convention used by
// most math libraries (invariant I4). All arithmetic is float64; only the final
// store to the MP4 is float32 (invariant I5).
//
// Every expression of the form a*b+c is written with an explicit float64()
// conversion around the product. Go permits fusing that into a single FMA
// instruction (and does so on arm64), which rounds once instead of twice and
// silently diverges from CPython. The conversion is the spec-sanctioned barrier.
package quat

import (
	"math"
	"sort"
)

// Error is a quaternion-domain error, mirroring the ValueError raised by the
// Python reference.
type Error string

func (e Error) Error() string { return string(e) }

// PyRound rounds half to even, as Python's builtin round() does. Go's
// math.Round rounds half away from zero, which changes filter radii at exact
// .5 boundaries and breaks golden parity.
func PyRound(value float64) int {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	floor := math.Floor(value)
	diff := value - floor
	switch {
	case diff > 0.5:
		return int(floor) + 1
	case diff < 0.5:
		return int(floor)
	}
	if math.Mod(floor, 2) == 0 {
		return int(floor)
	}
	return int(floor) + 1
}

// Median reproduces Python's statistics.median: the middle element for an odd
// count, the mean of the two middle elements for an even count. The input is
// copied before sorting.
func Median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	half := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[half]
	}
	return (sorted[half-1] + sorted[half]) / 2.0
}

// Smoothstep is the cubic 3t²−2t³ ease over a value clamped to [0, 1].
func Smoothstep(value float64) float64 {
	value = math.Min(1.0, math.Max(0.0, value))
	return float64(value*value) * (3.0 - float64(2.0*value))
}

// radToDeg is CPython's math.degrees scale factor: 180.0 divided by pi already
// rounded to a double. Writing 180.0/math.Pi with Go's untyped constant would
// do the division in arbitrary precision and can land one ULP away.
const radToDeg = 180.0 / float64(math.Pi)

// Degrees converts radians to degrees exactly as Python's math.degrees does.
func Degrees(radians float64) float64 { return radians * radToDeg }
