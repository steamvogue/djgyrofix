package detect_test

import (
	"testing"

	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

// runWithParams runs detection over a generated track with an explicit
// parameter set, for tests that start from a named profile.
func runWithParams(t *testing.T, defect synth.Defect, params detect.Params) *detect.Result {
	t.Helper()
	return run(t, defect, nil, func(target *detect.Params) { *target = params })
}

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

// TestNoisyLevelIsIndependentOfDetectionSettings pins the second half of the
// calibration fix. How much an airframe resonates is a physical property of the
// aircraft; it cannot change because the pilot asked for a wider or narrower
// search. A level derived from --floor-dps did exactly that, and it put the
// level below real repairable footage on the aggressive profile.
func TestNoisyLevelIsIndependentOfDetectionSettings(t *testing.T) {
	base := run(t, synth.DefectNone, nil, nil)

	raised := run(t, synth.DefectNone, nil, func(params *detect.Params) {
		params.FloorDPS *= 4
	})
	if raised.Noise.NoisyDPS != base.Noise.NoisyDPS {
		t.Errorf("noisy level moved from %.1f to %.1f °/s when --floor-dps changed",
			base.Noise.NoisyDPS, raised.Noise.NoisyDPS)
	}

	sensitive := run(t, synth.DefectNone, nil, func(params *detect.Params) {
		params.Sensitivity = 2
	})
	if sensitive.Noise.NoisyDPS != base.Noise.NoisyDPS {
		t.Errorf("noisy level moved from %.1f to %.1f °/s when --sensitivity changed",
			base.Noise.NoisyDPS, sensitive.Noise.NoisyDPS)
	}

	// The effective floor is still reported, because the clean verdict quotes it.
	if raised.Noise.FloorDPS <= base.Noise.FloorDPS {
		t.Errorf("reported floor %.1f °/s did not follow --floor-dps from %.1f °/s",
			raised.Noise.FloorDPS, base.Noise.FloorDPS)
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

// TestKnownRepairableFootageIsNotCalledNoisy pins the calibration against the
// one real clip there is evidence for. The 8m17s clip in docs/FINDINGS.md
// measured 39.2 °/s typical and 66.7 °/s at p90, and it is repairable: 6.17% of
// it flagged, a 91.6% residual reduction, a rescan that came back empty.
//
// The first calibration put the noisy level at half the detection floor, which
// separated the synthetic fixtures — 0.6 °/s clean against 116 °/s rough, a gap
// so wide that any threshold looks right — and called that real clip an
// airframe problem. That is the worst error the diagnosis can make: it tells a
// pilot to go re-mount a camera over footage the tool would have fixed.
func TestKnownRepairableFootageIsNotCalledNoisy(t *testing.T) {
	// Measured on the real clips with the duplicated samples collapsed, in °/s:
	// the worst p90 of the two, from the clip that does have real artifacts.
	// The pre-duplication figures were 39.2 and 66.7, and were mostly the
	// sampling artifact rather than the airframe.
	const realTypical, realP90 = 3.4, 4.3

	for _, profile := range []string{"conservative", "balanced", "aggressive"} {
		params, err := detect.ProfileParams(profile)
		if err != nil {
			t.Fatalf("ProfileParams(%q): %v", profile, err)
		}
		result := runWithParams(t, synth.DefectNone, params)
		if realP90 >= result.Noise.NoisyDPS {
			t.Errorf("%s: noisy level %.1f °/s sits at or below the %.1f °/s p90 of "+
				"footage known to be repairable — it would be diagnosed as an airframe problem",
				profile, result.Noise.NoisyDPS, realP90)
		}
		if realTypical >= result.Noise.NoisyDPS {
			t.Errorf("%s: noisy level %.1f °/s sits at or below the %.1f °/s typical floor "+
				"of repairable footage", profile, result.Noise.NoisyDPS, realTypical)
		}
	}
}

// TestUniformlyRoughFootageStillReadsAsNoisy is the other side of the same
// calibration: raising the level to clear real footage must not raise it past
// a clip that genuinely is resonating end to end.
func TestUniformlyRoughFootageStillReadsAsNoisy(t *testing.T) {
	rough := run(t, synth.DefectMixed, func(options *synth.AttitudeOptions) {
		options.RoughUntil = options.Seconds
	}, nil)
	if rough.Noise.NoisyFraction < 0.9 {
		t.Errorf("a clip rough end to end reads as only %.1f%% noisy at a %.1f °/s level "+
			"(p50 %.1f °/s) — the calibration has been loosened too far",
			rough.Noise.NoisyFraction*100, rough.Noise.NoisyDPS, rough.Noise.P50)
	}
}
