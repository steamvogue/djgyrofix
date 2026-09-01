package quat_test

import (
	"math"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/quat"
)

func TestPyRoundBreaksTiesToEven(t *testing.T) {
	// Go's math.Round rounds half away from zero. Python's builtin round goes
	// to even, and filter radii are computed with it — a mismatch changes the
	// box-blur width at exact .5 boundaries and breaks golden parity.
	cases := []struct {
		input float64
		want  int
	}{
		{0.5, 0}, {1.5, 2}, {2.5, 2}, {3.5, 4}, {-0.5, 0}, {-1.5, -2},
		{2.4, 2}, {2.6, 3}, {0, 0}, {36.0, 36},
	}
	for _, test := range cases {
		if got := quat.PyRound(test.input); got != test.want {
			t.Errorf("PyRound(%g) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestMedianMatchesPythonStatistics(t *testing.T) {
	cases := []struct {
		input []float64
		want  float64
	}{
		{[]float64{1}, 1},
		{[]float64{3, 1, 2}, 2},
		{[]float64{4, 1, 3, 2}, 2.5},
		{[]float64{}, 0},
	}
	for _, test := range cases {
		if got := quat.Median(test.input); got != test.want {
			t.Errorf("Median(%v) = %g, want %g", test.input, got, test.want)
		}
	}
	// The input must not be reordered under the caller.
	input := []float64{3, 1, 2}
	quat.Median(input)
	if input[0] != 3 {
		t.Errorf("Median sorted its argument in place: %v", input)
	}
}

func TestNormalizeRejectsDegenerate(t *testing.T) {
	for _, value := range []quat.Q{
		{0, 0, 0, 0},
		{math.NaN(), 0, 0, 0},
		{math.Inf(1), 0, 0, 0},
		{1e-20, 0, 0, 0},
	} {
		if _, err := quat.Normalize(value); err == nil {
			t.Errorf("Normalize(%v) accepted a degenerate quaternion", value)
		}
	}
}

func TestMultiplyByInverseIsIdentity(t *testing.T) {
	value := quat.Q{0.5, -0.5, 0.5, 0.5}
	inverse, err := quat.Inverse(value)
	if err != nil {
		t.Fatal(err)
	}
	product, err := quat.Multiply(value, inverse)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(product[0]-1) > 1e-12 {
		t.Errorf("q * q⁻¹ = %v, want identity", product)
	}
}

func TestSlerpEndpointsAndMidpoint(t *testing.T) {
	a := quat.Q{1, 0, 0, 0}
	// 90 degrees about Y.
	b := quat.Q{math.Sqrt2 / 2, 0, math.Sqrt2 / 2, 0}
	for _, test := range []struct {
		amount float64
		want   quat.Q
	}{
		{0, a},
		{1, b},
		{0.5, quat.Q{math.Cos(math.Pi / 8), 0, math.Sin(math.Pi / 8), 0}},
	} {
		got, err := quat.Slerp(a, b, test.amount)
		if err != nil {
			t.Fatal(err)
		}
		for index := range got {
			if math.Abs(got[index]-test.want[index]) > 1e-12 {
				t.Errorf("Slerp(a, b, %g) = %v, want %v", test.amount, got, test.want)
				break
			}
		}
	}
}

func TestSlerpTakesTheShortArc(t *testing.T) {
	a := quat.Q{1, 0, 0, 0}
	b := quat.Q{-0.999, 0, -0.0447, 0}
	got, err := quat.Slerp(a, b, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	// Going the long way round would land near the antipode, with w far from 1.
	if got[0] < 0.99 {
		t.Errorf("Slerp took the long arc: %v", got)
	}
}

func TestUnwrapRemovesDoubleCoverFlips(t *testing.T) {
	input := []quat.Q{{1, 0, 0, 0}, {-0.999, 0, -0.0447, 0}, {0.998, 0, 0.0632, 0}}
	unwrapped := quat.Unwrap(input)
	for index := 1; index < len(unwrapped); index++ {
		if quat.Dot(unwrapped[index-1], unwrapped[index]) < 0 {
			t.Errorf("Unwrap left a sign flip at index %d: %v", index, unwrapped)
		}
	}
}

func TestBoxBlurAveragesOverTheWindow(t *testing.T) {
	// A constant series must survive blurring exactly.
	input := make([]quat.Q, 20)
	for index := range input {
		input[index] = quat.Q{1, 0, 0, 0}
	}
	blurred, err := quat.BoxBlur(input, 3)
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range blurred {
		if math.Abs(value[0]-1) > 1e-15 {
			t.Fatalf("BoxBlur changed a constant series at %d: %v", index, value)
		}
	}
	// An alternating series must be pulled toward its mean.
	for index := range input {
		angle := 0.4 * float64(index%2)
		input[index] = quat.Q{math.Cos(angle / 2), 0, math.Sin(angle / 2), 0}
	}
	blurred, err = quat.BoxBlur(input, 4)
	if err != nil {
		t.Fatal(err)
	}
	spread := 0.0
	for index := 1; index < len(blurred)-1; index++ {
		spread = math.Max(spread, math.Abs(blurred[index][3-1]-blurred[index-1][3-1]))
	}
	if spread > 0.05 {
		t.Errorf("BoxBlur left an alternating series unsmoothed, spread %g", spread)
	}
}

func TestSmoothstepIsClampedAndMonotonic(t *testing.T) {
	if got := quat.Smoothstep(-1); got != 0 {
		t.Errorf("Smoothstep(-1) = %g, want 0", got)
	}
	if got := quat.Smoothstep(2); got != 1 {
		t.Errorf("Smoothstep(2) = %g, want 1", got)
	}
	previous := -1.0
	for step := 0; step <= 100; step++ {
		value := quat.Smoothstep(float64(step) / 100)
		if value < previous {
			t.Fatalf("Smoothstep is not monotonic at %d", step)
		}
		previous = value
	}
}

func TestDegreesMatchesCPython(t *testing.T) {
	// CPython multiplies by 180.0/pi with pi already rounded to a double.
	if got := quat.Degrees(1.0); got != 57.29577951308232 {
		t.Errorf("Degrees(1) = %.17g, want 57.29577951308232", got)
	}
}
