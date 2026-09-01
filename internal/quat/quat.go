package quat

import "math"

// Q is a quaternion stored as (w, x, y, z) — invariant I4.
type Q [4]float64

// Identity is the no-rotation quaternion.
var Identity = Q{1, 0, 0, 0}

// Neg returns -q, which represents the same rotation with the opposite sign.
func (q Q) Neg() Q { return Q{-q[0], -q[1], -q[2], -q[3]} }

// IsFinite reports whether every component is finite.
func (q Q) IsFinite() bool {
	for _, value := range q {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

// Norm returns the Euclidean length of q.
func (q Q) Norm() float64 { return math.Sqrt(Dot(q, q)) }

// Dot accumulates the four products left to right, matching Python's
// sum(x*y for x, y in zip(a, b)).
func Dot(a, b Q) float64 {
	total := float64(a[0] * b[0])
	total += float64(a[1] * b[1])
	total += float64(a[2] * b[2])
	total += float64(a[3] * b[3])
	return total
}

// Normalize scales q to unit length. It reports an error for a non-finite or
// degenerate quaternion, where the reference raises ValueError.
func Normalize(q Q) (Q, error) {
	norm := math.Sqrt(Dot(q, q))
	if math.IsNaN(norm) || math.IsInf(norm, 0) || norm < 1e-12 {
		return Q{}, Error("invalid zero-length quaternion")
	}
	return Q{q[0] / norm, q[1] / norm, q[2] / norm, q[3] / norm}, nil
}

// Multiply returns the normalized Hamilton product a ⊗ b. The reference
// normalizes on every multiply; that is reproduced here rather than optimized
// away, because it is observable in the output bytes.
func Multiply(a, b Q) (Q, error) {
	w := float64(a[0]*b[0]) - float64(a[1]*b[1])
	w -= float64(a[2] * b[2])
	w -= float64(a[3] * b[3])

	x := float64(a[0]*b[1]) + float64(a[1]*b[0])
	x += float64(a[2] * b[3])
	x -= float64(a[3] * b[2])

	y := float64(a[0]*b[2]) - float64(a[1]*b[3])
	y += float64(a[2] * b[0])
	y += float64(a[3] * b[1])

	z := float64(a[0]*b[3]) + float64(a[1]*b[2])
	z -= float64(a[2] * b[1])
	z += float64(a[3] * b[0])

	return Normalize(Q{w, x, y, z})
}

// Inverse returns the conjugate of the normalized q, which is its inverse for
// unit quaternions.
func Inverse(q Q) (Q, error) {
	unit, err := Normalize(q)
	if err != nil {
		return Q{}, err
	}
	return Q{unit[0], -unit[1], -unit[2], -unit[3]}, nil
}

// Slerp interpolates along the shortest arc from a to b by amount, falling back
// to normalized linear interpolation when the two are nearly parallel and the
// sine of the angle would lose precision.
func Slerp(a, b Q, amount float64) (Q, error) {
	qa, err := Normalize(a)
	if err != nil {
		return Q{}, err
	}
	qb, err := Normalize(b)
	if err != nil {
		return Q{}, err
	}
	cosine := Dot(qa, qb)
	if cosine < 0.0 {
		qb = qb.Neg()
		cosine = -cosine
	}
	cosine = math.Min(1.0, math.Max(-1.0, cosine))
	amount = math.Min(1.0, math.Max(0.0, amount))
	if cosine > 0.9995 {
		var lerp Q
		for index := range lerp {
			lerp[index] = qa[index] + float64(amount*(qb[index]-qa[index]))
		}
		return Normalize(lerp)
	}
	angle := math.Acos(cosine)
	denominator := math.Sin(angle)
	left := math.Sin((1.0-amount)*angle) / denominator
	right := math.Sin(amount*angle) / denominator
	var result Q
	for index := range result {
		result[index] = float64(left*qa[index]) + float64(right*qb[index])
	}
	return Normalize(result)
}

// Unwrap flips the sign of each quaternion that points away from its
// predecessor, so component-wise filtering does not average across the
// double-cover discontinuity.
func Unwrap(quaternions []Q) []Q {
	continuous := make([]Q, 0, len(quaternions))
	for _, value := range quaternions {
		if len(continuous) > 0 && Dot(continuous[len(continuous)-1], value) < 0.0 {
			value = value.Neg()
		}
		continuous = append(continuous, value)
	}
	return continuous
}

// BoxBlur averages each quaternion over a centered window of the given radius
// using prefix sums, then renormalizes. Three passes approximate a Gaussian.
func BoxBlur(quaternions []Q, radius int) ([]Q, error) {
	if radius <= 0 || len(quaternions) < 2 {
		result := make([]Q, len(quaternions))
		for index, value := range quaternions {
			unit, err := Normalize(value)
			if err != nil {
				return nil, err
			}
			result[index] = unit
		}
		return result, nil
	}
	var prefixes [4][]float64
	for component := range prefixes {
		prefixes[component] = make([]float64, len(quaternions)+1)
	}
	for index, quaternion := range quaternions {
		for component := 0; component < 4; component++ {
			prefixes[component][index+1] = prefixes[component][index] + quaternion[component]
		}
	}
	result := make([]Q, len(quaternions))
	for index := range quaternions {
		first := index - radius
		if first < 0 {
			first = 0
		}
		last := index + radius + 1
		if last > len(quaternions) {
			last = len(quaternions)
		}
		count := float64(last - first)
		var average Q
		for component := 0; component < 4; component++ {
			average[component] = (prefixes[component][last] - prefixes[component][first]) / count
		}
		unit, err := Normalize(average)
		if err != nil {
			return nil, err
		}
		result[index] = unit
	}
	return result, nil
}
