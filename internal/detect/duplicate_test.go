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
