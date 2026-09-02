package advise_test

import (
	"strings"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/advise"
	"github.com/steamvogue/djgyrofix/internal/detect"
)

// base is a clip that would be reported as clean: a quiet noise floor, no
// events, and a detection floor of 60 °/s so the noisy level lands at 30.
func base() advise.Input {
	return advise.Input{
		File:             "clip.MP4",
		DurationSeconds:  30,
		RollingBaseline:  true,
		ShortClipSeconds: 15,
		Profile:          "balanced",
		Sensitivity:      1,
		MinSeverity:      5,
		MaxAffected:      0.15,
		Noise:            detect.NoiseProfile{P10: 0.5, P50: 0.6, P90: 0.7, NoisyDPS: 30},
	}
}

func smooth(start, end, severity float64) detect.Event {
	return detect.Event{
		StartSeconds: start, EndSeconds: end, Severity: severity,
		Class: detect.ClassJitter, Action: detect.ActionSmooth,
	}
}

func TestVerdicts(t *testing.T) {
	tests := []struct {
		name  string
		input func(*advise.Input)
		want  advise.Verdict
	}{
		{"quiet clip with nothing found", func(*advise.Input) {}, advise.VerdictClean},
		{"a handful of events", func(in *advise.Input) {
			in.Events = []detect.Event{smooth(1, 2, 9)}
			in.AffectedSeconds, in.AffectedFraction = 1, 1.0/30
		}, advise.VerdictPatch},
		{"over the affected limit", func(in *advise.Input) {
			in.Events = []detect.Event{smooth(1, 8, 9)}
			in.AffectedSeconds, in.AffectedFraction = 7, 7.0/30
		}, advise.VerdictReview},
		{"noise floor high across the clip", func(in *advise.Input) {
			in.Noise = detect.NoiseProfile{P10: 100, P50: 116, P90: 122, NoisyDPS: 30, NoisyFraction: 1, NoisySeconds: 30}
		}, advise.VerdictUpstream},
		{"noise floor high over a third of it", func(in *advise.Input) {
			in.Noise = detect.NoiseProfile{P10: 0.5, P50: 0.6, P90: 114, NoisyDPS: 30, NoisyFraction: 0.33, NoisySeconds: 10}
		}, advise.VerdictUpstream},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base()
			test.input(&input)
			got := advise.Evaluate(input)
			if got.Verdict != test.want {
				t.Errorf("verdict = %q, want %q (headline %q)", got.Verdict, test.want, got.Headline)
			}
			if got.Headline == "" {
				t.Error("every verdict must carry a headline")
			}
		})
	}
}

// TestUpstreamOutranksAQuietEventList is the whole reason this package exists.
// On badly mounted footage the rolling threshold climbs with the noise it
// should be catching, so detection reports a short, reassuring event list. The
// verdict has to override that reassurance rather than agree with it.
func TestUpstreamOutranksAQuietEventList(t *testing.T) {
	input := base()
	input.Events = []detect.Event{smooth(14.98, 15.03, 6.8)}
	input.AffectedSeconds, input.AffectedFraction = 0.05, 0.05/30
	input.Noise = detect.NoiseProfile{P10: 110, P50: 116, P90: 122, NoisyDPS: 30, NoisyFraction: 1, NoisySeconds: 30}

	got := advise.Evaluate(input)
	if got.Verdict != advise.VerdictUpstream {
		t.Fatalf("verdict = %q, want upstream", got.Verdict)
	}
	if got.NextCommand != "" {
		t.Errorf("upstream advice must not hand out a fix command, got %q", got.NextCommand)
	}
	if !strings.Contains(strings.Join(got.Reasons, " "), "not a clean bill of health") {
		t.Errorf("the reasons must say the quiet event list is a symptom:\n%v", got.Reasons)
	}
}

// TestLocalisedRoughnessIsNotDescribedAsUniform guards the wording: a clip that
// is clean for two thirds of its length has a quiet median, and telling that
// pilot their whole clip is noisy would contradict the number printed above it.
func TestLocalisedRoughnessIsNotDescribedAsUniform(t *testing.T) {
	input := base()
	input.Noise = detect.NoiseProfile{P10: 0.5, P50: 0.6, P90: 114, NoisyDPS: 30, NoisyFraction: 0.33, NoisySeconds: 9.9}

	reasons := strings.Join(advise.Evaluate(input).Reasons, " ")
	if !strings.Contains(reasons, "while the rest of it sits quietly") {
		t.Errorf("localised roughness must name the quiet remainder:\n%s", reasons)
	}
}

func TestNearMissesSuggestALooserProfile(t *testing.T) {
	input := base()
	input.NearMiss = 4

	suggestions := advise.Evaluate(input).Suggestions
	if !hasFlag(suggestions, "--profile aggressive") {
		t.Errorf("4 near misses should suggest a looser profile, got %v", suggestions)
	}
}

func TestBreadthNearTheLimitSuggestsAStricterProfile(t *testing.T) {
	input := base()
	input.Events = []detect.Event{smooth(1, 5, 9)}
	input.AffectedSeconds, input.AffectedFraction = 4, 0.14

	suggestions := advise.Evaluate(input).Suggestions
	if !hasFlag(suggestions, "--profile conservative") {
		t.Errorf("14%% affected against a 15%% limit should suggest stepping down, got %v", suggestions)
	}
}

// TestSuggestionsStayInScope keeps the block short enough to read. A bridge
// note on a clip whose verdict is "your airframe is the problem" is noise.
func TestSuggestionsStayInScope(t *testing.T) {
	input := base()
	input.Events = []detect.Event{{
		StartSeconds: 25, EndSeconds: 25.005, Severity: 9,
		Class: detect.ClassDropout, Action: detect.ActionBridge,
	}}
	input.Noise = detect.NoiseProfile{P10: 110, P50: 116, P90: 122, NoisyDPS: 30, NoisyFraction: 1, NoisySeconds: 30}

	if hasFlag(advise.Evaluate(input).Suggestions, "--no-bridge") {
		t.Error("an upstream verdict must not offer bridge tuning")
	}

	input.Noise = base().Noise
	input.AffectedSeconds, input.AffectedFraction = 0.005, 0.005/30
	if !hasFlag(advise.Evaluate(input).Suggestions, "--no-bridge") {
		t.Error("a patchable clip with a dropout should offer --no-bridge")
	}
}

func TestUpstreamVerdictDoesNotOfferDetectorTuning(t *testing.T) {
	input := base()
	input.Events = []detect.Event{smooth(1, 2, 9)}
	input.NearMiss = 12
	input.ResidualRegions = 8
	input.Scored = true
	input.ImprovementPercent = 10
	input.Noise = detect.NoiseProfile{
		P10: 100, P50: 116, P90: 122, NoisyDPS: 30,
		NoisyFraction: 1, NoisySeconds: 30,
	}

	got := advise.Evaluate(input)
	for _, suggestion := range got.Suggestions {
		if suggestion.Flags != advise.NoFlag {
			t.Errorf("upstream diagnosis contradicted itself with %s: %+v", suggestion.Flags, got)
		}
	}
}

func TestShortClipSaysTheBaselineWindowIsInert(t *testing.T) {
	input := base()
	input.RollingBaseline = false
	input.DurationSeconds = 8

	why := ""
	for _, suggestion := range advise.Evaluate(input).Suggestions {
		if strings.Contains(suggestion.Why, "--baseline-window") {
			why = suggestion.Why
		}
	}
	if why == "" {
		t.Error("a globally-thresholded clip should say --baseline-window has no effect")
	}
}

func hasFlag(suggestions []advise.Suggestion, flags string) bool {
	for _, suggestion := range suggestions {
		if suggestion.Flags == flags {
			return true
		}
	}
	return false
}

// TestResidualRegionsAloneDoNotAskForMoreCorrection pins the gate added after
// measuring the bounded correction across one to eight passes: the count of
// regions still tripping the detector falls indefinitely without the in-region
// residual moving with it, so it cannot on its own justify correcting harder.
func TestResidualRegionsAloneDoNotAskForMoreCorrection(t *testing.T) {
	tests := []struct {
		name        string
		improvement float64
		want        bool
	}{
		{"bounded ceiling reached", 84.7, false},
		{"just inside the weak-correction gate", 39.9, true},
		{"correction genuinely missed", 12.0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base()
			input.Events = []detect.Event{smooth(1, 2, 9)}
			input.AffectedSeconds, input.AffectedFraction = 1, 1.0/30
			input.ResidualRegions = 43
			input.Scored = true
			input.ImprovementPercent = test.improvement
			input.ClipImprovementPercent = 5

			got := false
			for _, suggestion := range advise.Evaluate(input).Suggestions {
				if suggestion.Flags == "--sensitivity 1.3" {
					got = true
					if strings.Contains(suggestion.Why, "edge") {
						t.Errorf("the edge claim came back; the residual is spread through the region: %s", suggestion.Why)
					}
				}
			}
			if got != test.want {
				t.Errorf("suggested --sensitivity 1.3 = %v at %.1f%% in-region, want %v",
					got, test.improvement, test.want)
			}
		})
	}
}

// TestUnscoredResidualRegionsStaySilent covers the manual path, where nothing
// scored the correction. Without the in-region figure there is no way to tell a
// ceiling from a miss, and guessing would fire on every such run.
func TestUnscoredResidualRegionsStaySilent(t *testing.T) {
	input := base()
	input.Events = []detect.Event{smooth(1, 2, 9)}
	input.AffectedSeconds, input.AffectedFraction = 1, 1.0/30
	input.ResidualRegions = 43

	for _, suggestion := range advise.Evaluate(input).Suggestions {
		if suggestion.Flags == "--sensitivity 1.3" {
			t.Errorf("an unscored run guessed at under-correction: %+v", suggestion)
		}
	}
}
