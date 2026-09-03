package detect_test

import (
	"testing"

	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/quat"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

// duplicated repeats every sample of a track, reproducing how DJI stores its
// fused attitude: 1978 Hz of samples carrying 989 Hz of information.
func duplicated(track []quat.Q) []quat.Q {
	out := make([]quat.Q, 0, len(track)*2)
	for _, value := range track {
		out = append(out, value, value)
	}
	return out
}

// TestOversamplingDoesNotBecomeNoise is the property the whole detector rests
// on. Differencing consecutive stored samples across a duplicated stream makes
// every other velocity exactly zero and doubles the rest — a square wave at
// Nyquist whose amplitude scales with rotation rate, which is largest exactly
// where the detector is looking. On the real clip it accounted for three
// quarters of the residual and lifted the apparent noise floor from 3.4 °/s to
// 37.9 °/s.
func TestOversamplingDoesNotBecomeNoise(t *testing.T) {
	track := synth.Attitude(synth.AttitudeOptions{
		Defect: synth.DefectNone, Rate: testRate, Seconds: 60, Seed: 20260901,
	})

	plain, err := detect.Run(points(track, testRate), detect.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	// The duplicated stream carries the same information at twice the rate.
	doubled, err := detect.Run(points(duplicated(track), testRate*2), detect.Defaults())
	if err != nil {
		t.Fatal(err)
	}

	if doubled.DuplicateShare < 0.4 {
		t.Fatalf("duplicate share %.4f, want about a half", doubled.DuplicateShare)
	}
	// The noise floor must describe the aircraft, not how often the numbers
	// were written down.
	if doubled.Noise.P50 > plain.Noise.P50*2+0.5 {
		t.Errorf("oversampling raised the noise floor from %.2f to %.2f °/s",
			plain.Noise.P50, doubled.Noise.P50)
	}
	if doubled.BaselineDPS > plain.BaselineDPS*2+0.5 {
		t.Errorf("oversampling raised the baseline from %.2f to %.2f °/s",
			plain.BaselineDPS, doubled.BaselineDPS)
	}
}

// TestAFrozenRunIsNotMistakenForOversampling keeps the two apart. A telemetry
// dropout that repeats one orientation is locally the same shape as DJI's
// duplication — a short run of identical values — and only the whole-stream
// share separates them. Collapsing a dropout would erase the very signature
// that makes it reconstructable.
func TestAFrozenRunIsNotMistakenForOversampling(t *testing.T) {
	result := run(t, synth.DefectDropout, nil, nil)
	if result.DuplicateShare >= 0.4 {
		t.Fatalf("a clip with one frozen dropout reports %.4f duplicate share", result.DuplicateShare)
	}
	found := false
	for _, event := range result.Events {
		if event.Class == detect.ClassDropout {
			found = true
		}
	}
	if !found {
		t.Errorf("the dropout stopped being detected: %+v", result.Events)
	}
}

// TestDuplicateShareIsMeasuredNotAssumed guards the gate itself.
func TestDuplicateShareIsMeasuredNotAssumed(t *testing.T) {
	clean := run(t, synth.DefectNone, nil, nil)
	if clean.DuplicateShare > 0.01 {
		t.Errorf("a clean generated track reports %.4f duplicate share", clean.DuplicateShare)
	}
}

// padded fills slots at a fractional ratio, the way DJI does at a frame rate
// whose slot count is not a whole multiple of the IMU rate. At 1.6 the pattern
// is irregular — some values written once, some twice — rather than the clean
// alternation of the 60 fps case.
func padded(track []quat.Q, ratio float64) []quat.Q {
	out := make([]quat.Q, 0, int(float64(len(track))*ratio)+1)
	for index := 0; index < int(float64(len(track))*ratio); index++ {
		source := int(float64(index) / ratio)
		if source >= len(track) {
			break
		}
		out = append(out, track[source])
	}
	return out
}

// TestFractionalPaddingIsStillOversampling covers the frame rates between the
// two that have been measured. Every clip seen packs about 33.3 quaternion slots
// into a video frame and fills them from a ~1000 Hz IMU, so the repeat factor is
// 33.3 x fps / 1000: exactly one at 30 fps and exactly two at 60, which is why
// the measured clips sit at 0.00 and 0.50 duplicate share with nothing between.
//
// 48 and 50 fps fall between, padding by 1.6 and 1.66 for a duplicate share near
// 0.37. Those are ordinary shooting modes, and the 0.4 threshold set from the
// two observed rates alone would have left them uncollapsed.
//
// Collapsing them is an improvement rather than a cure, and the bounds below say
// so. A whole-number ratio produces runs of one length and the floor comes back
// to where an unpadded stream sits. A fractional one alternates between run
// lengths, so the interval between distinct orientations keeps changing and some
// of that survives as residual. Measured against a 0.56 °/s unpadded floor:
//
//	ratio   uncollapsed   collapsed
//	1.60       4.20          2.29
//	1.66       4.41          2.20
//	3.33       8.33          under 1.62
//
// The gap between the last two columns is what this test defends. Closing the
// remaining gap at fractional ratios would mean resampling to the distinct
// orientations rather than holding velocity across the repeats, which is a
// larger change than anything here has evidence to justify — no clip at 48 or
// 50 fps has been seen.
func TestFractionalPaddingIsStillOversampling(t *testing.T) {
	track := synth.Attitude(synth.AttitudeOptions{
		Defect: synth.DefectNone, Rate: testRate, Seconds: 60, Seed: 20260901,
	})
	plain, err := detect.Run(points(track, testRate), detect.Defaults())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		ratio float64
		// tolerance is the multiple of the unpadded floor this ratio must stay
		// under. Uncollapsed, every one of these lands above 7x.
		tolerance float64
	}{
		{ratio: 1.6, tolerance: 5},
		{ratio: 1.66, tolerance: 5},
		{ratio: 3.33, tolerance: 3},
	}
	for _, test := range tests {
		stream := padded(track, test.ratio)
		result, err := detect.Run(points(stream, testRate*test.ratio), detect.Defaults())
		if err != nil {
			t.Fatalf("ratio %.2f: %v", test.ratio, err)
		}
		want := 1 - 1/test.ratio
		if result.DuplicateShare < want-0.05 || result.DuplicateShare > want+0.05 {
			t.Errorf("ratio %.2f: duplicate share %.4f, want about %.2f",
				test.ratio, result.DuplicateShare, want)
		}
		if limit := plain.Noise.P50 * test.tolerance; result.Noise.P50 > limit {
			t.Errorf("ratio %.2f: floor %.2f °/s against an unpadded %.2f, over the %.0fx bound %.2f — "+
				"padding is being read as noise again",
				test.ratio, result.Noise.P50, plain.Noise.P50, test.tolerance, limit)
		}
	}
}
