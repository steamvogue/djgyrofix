package detect_test

import (
	"testing"

	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

// TestNoiseProfileSeparatesCleanFromRough is the measurement the diagnosis
// rests on. The reported baseline is a median over the whole clip, so it cannot
// tell a clean clip from one that is clean for most of its length and unusable
// for the rest — the percentile spread and the noisy share can.
func TestNoiseProfileSeparatesCleanFromRough(t *testing.T) {
	clean := run(t, synth.DefectNone, nil, nil)
	if clean.Noise.NoisyFraction != 0 {
		t.Errorf("clean clip has %.1f%% of its length above the noisy level, want none",
			clean.Noise.NoisyFraction*100)
	}
	if clean.Noise.P90 >= clean.Noise.NoisyDPS {
		t.Errorf("clean p90 %.2f °/s should sit well under the %.2f °/s noisy level",
			clean.Noise.P90, clean.Noise.NoisyDPS)
	}

	rough := run(t, synth.DefectMixed, func(options *synth.AttitudeOptions) {
		options.RoughUntil = options.Seconds
	}, nil)
	if rough.Noise.NoisyFraction < 0.9 {
		t.Errorf("a clip rough end to end reports only %.1f%% noisy, want nearly all of it",
			rough.Noise.NoisyFraction*100)
	}
}

// TestNoiseProfileSeesRoughnessTheMedianHides is the case that motivated the
// whole diagnosis: one third of the clip resonating leaves the reported median
// baseline indistinguishable from clean footage.
func TestNoiseProfileSeesRoughnessTheMedianHides(t *testing.T) {
	clean := run(t, synth.DefectMixed, nil, nil)
	partial := run(t, synth.DefectMixed, func(options *synth.AttitudeOptions) {
		options.RoughUntil = options.Seconds / 3
	}, nil)

	if got, want := partial.BaselineDPS, clean.BaselineDPS*2; got > want {
		t.Logf("median baseline %.2f °/s against %.2f °/s on the clean clip", got, clean.BaselineDPS)
	}
	if partial.Noise.NoisyFraction < 0.2 {
		t.Errorf("a third of the clip is rough but only %.1f%% reads as noisy",
			partial.Noise.NoisyFraction*100)
	}
	if partial.Noise.P90 <= partial.Noise.P50*10 {
		t.Errorf("p90 %.2f °/s should tower over the p50 %.2f °/s on a partly rough clip",
			partial.Noise.P90, partial.Noise.P50)
	}
}

// TestNoisyLevelTracksTheDetectionFloor keeps the test self-consistent rather
// than absolute: what counts as a noisy clip has to move when the user moves
// the floor detection itself works from.
func TestNoisyLevelTracksTheDetectionFloor(t *testing.T) {
	base := run(t, synth.DefectNone, nil, nil)
	raised := run(t, synth.DefectNone, nil, func(params *detect.Params) {
		params.FloorDPS *= 4
	})
	if raised.Noise.NoisyDPS <= base.Noise.NoisyDPS {
		t.Errorf("noisy level %.1f °/s did not follow a raised floor from %.1f °/s",
			raised.Noise.NoisyDPS, base.Noise.NoisyDPS)
	}

	sensitive := run(t, synth.DefectNone, nil, func(params *detect.Params) {
		params.Sensitivity = 2
	})
	if sensitive.Noise.NoisyDPS >= base.Noise.NoisyDPS {
		t.Errorf("noisy level %.1f °/s did not follow a raised sensitivity from %.1f °/s",
			sensitive.Noise.NoisyDPS, base.Noise.NoisyDPS)
	}
}

// TestNearMissCountsOnlyTheEventsJustUnderTheCut feeds the step-up rule, so a
// severity far below the cut must not inflate it.
func TestNearMissCountsOnlyTheEventsJustUnderTheCut(t *testing.T) {
	strict := run(t, synth.DefectMixed, nil, func(params *detect.Params) {
		params.MinSeverity = 10
	})
	loose := run(t, synth.DefectMixed, nil, func(params *detect.Params) {
		params.MinSeverity = 0
	})
	if loose.NearMiss != 0 {
		t.Errorf("nothing can be a near miss when the cut is zero, got %d", loose.NearMiss)
	}
	if strict.NearMiss > len(loose.Events) {
		t.Errorf("near misses %d exceed the %d events that exist at all",
			strict.NearMiss, len(loose.Events))
	}
}
