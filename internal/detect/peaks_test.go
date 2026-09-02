package detect

import "testing"

func TestLocalPeakCountBoundaryConditions(t *testing.T) {
	cases := []struct {
		name       string
		values     []float64
		thresholds []float64
		expected   int
	}{
		{
			name:       "empty series",
			values:     []float64{},
			thresholds: []float64{},
			expected:   1, // fallback default
		},
		{
			name:       "single value supra threshold",
			values:     []float64{100.0},
			thresholds: []float64{20.0},
			expected:   1,
		},
		{
			name:       "two values with leading peak",
			values:     []float64{100.0, 30.0},
			thresholds: []float64{20.0, 20.0},
			expected:   1,
		},
		{
			name:       "two values with trailing peak",
			values:     []float64{30.0, 100.0},
			thresholds: []float64{20.0, 20.0},
			expected:   1,
		},
		{
			name:       "leading peak and interior peak separated by at least 3 bins",
			values:     []float64{120.0, 30.0, 10.0, 20.0, 110.0, 25.0},
			thresholds: []float64{15.0, 15.0, 15.0, 15.0, 15.0, 15.0},
			expected:   2, // index 0 and index 4
		},
		{
			name:       "interior peak and trailing peak separated by at least 3 bins",
			values:     []float64{25.0, 110.0, 20.0, 10.0, 30.0, 120.0},
			thresholds: []float64{15.0, 15.0, 15.0, 15.0, 15.0, 15.0},
			expected:   2, // index 1 and index 5
		},
		{
			name:       "peaks closer than 3 bins merge to 1",
			values:     []float64{100.0, 90.0, 105.0, 20.0},
			thresholds: []float64{15.0, 15.0, 15.0, 15.0},
			expected:   1, // index 0 and 2 are within distance 2, so only 1 selected
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actual := localPeakCount(tc.values, tc.thresholds)
			if actual != tc.expected {
				t.Errorf("got %d peaks, expected %d", actual, tc.expected)
			}
		})
	}
}
