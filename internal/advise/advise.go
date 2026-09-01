// Package advise turns a detection result into a verdict a pilot can act on.
//
// Every number the tool prints is already actionable to whoever wrote the
// detector, and to nobody else. A scan that reports "baseline 116.2 °/s,
// threshold 348.9 °/s, 3 events" is telling you that the airframe is resonating
// badly enough to hide its own artifacts, but only if you already knew what a
// clean baseline looks like. This package encodes that reading.
//
// It deliberately holds no opinion the detector does not support. Every reason
// it prints traces to a measured quantity, and the parameter suggestions name
// the exact flag to add rather than describing a direction to go in.
package advise

import (
	"fmt"
	"math"

	"github.com/steamvogue/djgyrofix/internal/detect"
)

// Verdict is the headline answer: is this tool the right one for this clip?
type Verdict string

// Verdicts, in the order they are tested.
const (
	// VerdictUpstream means the noise floor itself is the defect. Patching
	// metadata cannot help, and the quiet event list is a symptom rather than
	// a reassurance.
	VerdictUpstream Verdict = "upstream"
	// VerdictReview means detection flagged more than --max-affected, so fix
	// will refuse until a human decides.
	VerdictReview Verdict = "review"
	// VerdictPatch means a bounded set of artifacts was found: the case this
	// tool exists for.
	VerdictPatch Verdict = "patch"
	// VerdictClean means the metadata carries no correctable artifact.
	VerdictClean Verdict = "clean"
)

// NoFlag is the Suggestion.Flags value for advice that is not a parameter
// change at all — mounting, tuning, or a Gyroflow setting. The report renders
// those as notes rather than as something to paste onto a command line.
const NoFlag = "(no flag)"

// Suggestion is one concrete parameter change, with the measurement behind it.
type Suggestion struct {
	Flags string `json:"flags"`
	Why   string `json:"why"`
}

// Advice is the rendered verdict.
type Advice struct {
	Verdict  Verdict  `json:"verdict"`
	Headline string   `json:"headline"`
	Reasons  []string `json:"reasons,omitempty"`
	// Prediction is what correction is expected to achieve. It is separate from
	// Reasons because a report of a patch that has already been applied prints
	// the measured figure instead, and printing both would read as two
	// different results for the same run.
	Prediction  string       `json:"prediction,omitempty"`
	Suggestions []Suggestion `json:"suggestions,omitempty"`
	NextCommand string       `json:"next_command,omitempty"`
}

// Input is everything Evaluate reads. It is a plain struct rather than the
// report type so that report can depend on advise and not the reverse.
type Input struct {
	File             string
	DurationSeconds  float64
	Events           []detect.Event
	AffectedSeconds  float64
	AffectedFraction float64
	Noise            detect.NoiseProfile
	NearMiss         int
	RollingBaseline  bool
	ShortClipSeconds float64

	Profile     string
	Sensitivity float64
	MinSeverity float64
	MaxAffected float64

	// ImprovementPercent is the predicted residual reduction inside the
	// corrected regions. Valid only when Scored is set.
	ImprovementPercent float64
	Scored             bool
	// ResidualRegions is how many original correction regions were still
	// detectable after the bounded passes.
	ResidualRegions int
}

// Thresholds for the verdicts. They are named so the reasons can quote them.
const (
	// noisyShareUpstream is the share of a clip that has to sit at or above the
	// noisy level before the noise floor, rather than the metadata, is the
	// story. A quarter of a clip is already more than smoothing could rescue.
	noisyShareUpstream = 0.25
	// noisyShareMention is where a rough stretch is worth naming even though
	// the rest of the clip is patchable.
	noisyShareMention = 0.05
	// nearLimitShare is how close to --max-affected counts as close enough to
	// warn about, since the next clip off the same battery may well cross it.
	nearLimitShare = 0.8
	// nearMissTrigger is how many events must score just under --min-severity
	// before a looser profile is worth suggesting.
	nearMissTrigger = 3
	// weakCorrection is the predicted reduction below which the correction is
	// not really reaching the artifact.
	weakCorrection = 40.0
)

// Evaluate reads the detection result and returns the verdict.
func Evaluate(in Input) Advice {
	actionable := actionableCount(in.Events)

	var advice Advice
	switch {
	case in.Noise.NoisyFraction >= noisyShareUpstream:
		advice = upstream(in)
	case in.AffectedFraction > in.MaxAffected:
		advice = review(in, actionable)
	case actionable > 0:
		advice = patch(in, actionable)
	default:
		advice = clean(in)
	}

	advice.Suggestions = append(advice.Suggestions, tuning(in, actionable, advice.Verdict)...)
	return advice
}

// upstream is the verdict the README promised and the rolling threshold never
// delivered: on badly mounted footage the local threshold climbs with the noise
// it should be catching, so the event list goes quiet exactly where the footage
// is worst.
func upstream(in Input) Advice {
	// Two different clips reach this verdict and they need different words. One
	// is rough end to end; the other is clean for most of its length with one
	// bad stretch, and telling that pilot their footage is uniformly noisy when
	// the median says otherwise would cost the whole diagnosis its credibility.
	wholeClip := in.Noise.P50 >= in.Noise.NoisyDPS

	advice := Advice{Verdict: VerdictUpstream}
	if wholeClip {
		advice.Headline = "the recorded metadata is not what is wrong here — the noise floor is"
		advice.Reasons = []string{
			fmt.Sprintf("the local residual floor is at or above %.1f °/s across %.0f%% of the clip (%s)",
				in.Noise.NoisyDPS, in.Noise.NoisyFraction*100, seconds(in.Noise.NoisySeconds)),
			fmt.Sprintf("typical floor %.1f °/s, p90 %.1f °/s — clean footage runs a small fraction of that",
				in.Noise.P50, in.Noise.P90),
		}
	} else {
		advice.Headline = fmt.Sprintf(
			"%.0f%% of this clip is an airframe problem rather than a metadata one",
			in.Noise.NoisyFraction*100)
		advice.Reasons = []string{
			fmt.Sprintf("%s of the clip has a noise floor at or above %.1f °/s, peaking at %.1f °/s, "+
				"while the rest of it sits quietly at %.1f °/s",
				seconds(in.Noise.NoisySeconds), in.Noise.NoisyDPS, in.Noise.P90, in.Noise.P50),
		}
	}
	advice.Reasons = append(advice.Reasons,
		"the rolling threshold rises with that floor, so detection goes quiet over the "+
			"roughest stretches; a short event list here is not a clean bill of health")
	if actionableCount(in.Events) > 0 {
		advice.Reasons = append(advice.Reasons,
			"the events listed above are real, but they are not what is making the rough part shake")
	}
	advice.Suggestions = []Suggestion{{
		Flags: NoFlag,
		Why: "soft-mount the air unit, check the tune and the props before patching metadata — " +
			"no software can turn a resonating IMU into a quiet one",
	}}
	return advice
}

// review is the over-budget case: fix will refuse, and a human has to choose
// between a stricter profile and accepting the breadth.
func review(in Input, actionable int) Advice {
	return Advice{
		Verdict: VerdictReview,
		Headline: fmt.Sprintf("detection flagged %.1f%% of the clip, over the --max-affected limit of %.0f%% — fix will refuse",
			in.AffectedFraction*100, in.MaxAffected*100),
		Reasons: []string{
			fmt.Sprintf("%d actionable event%s covering %s", actionable, plural(actionable), seconds(in.AffectedSeconds)),
			"smoothing this much of a clip degrades stabilization everywhere it touches, " +
				"so the limit is a deliberate stop rather than a tuning nuisance",
		},
		Suggestions: []Suggestion{
			{Flags: "--profile conservative", Why: "fewer, higher-confidence events"},
			{Flags: fmt.Sprintf("--max-affected %.2f", clampUp(in.AffectedFraction)),
				Why: "accept the breadth, having looked at the event list first"},
		},
	}
}

// patch is the case the tool exists for.
func patch(in Input, actionable int) Advice {
	advice := Advice{
		Verdict: VerdictPatch,
		Headline: fmt.Sprintf("%d correctable event%s over %s (%.2f%% of the clip) — this is what djgyrofix is for",
			actionable, plural(actionable), seconds(in.AffectedSeconds), in.AffectedFraction*100),
		Reasons: []string{
			fmt.Sprintf("noise floor %.1f °/s typical, %.1f °/s p90 — quiet enough that these events stand out as artifacts",
				in.Noise.P50, in.Noise.P90),
		},
		NextCommand: fmt.Sprintf("djgyrofix fix --apply %s", in.File),
	}
	if in.Scored {
		advice.Prediction = fmt.Sprintf(
			"predicted residual reduction %.1f%%, measured inside the corrected regions only",
			in.ImprovementPercent)
	}
	return advice
}

// clean is the answer that saves an evening: nothing here to patch.
func clean(in Input) Advice {
	return Advice{
		Verdict:  VerdictClean,
		Headline: "no correctable artifacts in the metadata",
		Reasons: []string{
			fmt.Sprintf("noise floor %.1f °/s typical, %.1f °/s p90, against a %.1f °/s detection floor",
				in.Noise.P50, in.Noise.P90, in.Noise.NoisyDPS*2),
		},
		Suggestions: []Suggestion{{
			Flags: NoFlag,
			Why: "if the footage still shakes after stabilization, the metadata is not the cause — " +
				"try Gyroflow's Complementary integration method or its low-pass filter",
		}},
	}
}

// tuning are the parameter steers that apply regardless of verdict.
func tuning(in Input, actionable int, verdict Verdict) []Suggestion {
	var suggestions []Suggestion

	if in.NearMiss >= nearMissTrigger && in.AffectedFraction < in.MaxAffected/2 {
		suggestions = append(suggestions, Suggestion{
			Flags: "--profile aggressive",
			Why: fmt.Sprintf("%d event%s scored just under --min-severity %.1f and were dropped",
				in.NearMiss, plural(in.NearMiss), in.MinSeverity),
		})
	}
	if in.AffectedFraction > 0 && in.AffectedFraction <= in.MaxAffected &&
		in.AffectedFraction >= nearLimitShare*in.MaxAffected {
		suggestions = append(suggestions, Suggestion{
			Flags: "--profile conservative",
			Why: fmt.Sprintf("%.1f%% affected is within a whisker of the %.0f%% refusal limit",
				in.AffectedFraction*100, in.MaxAffected*100),
		})
	}
	if in.Scored && actionable > 0 && in.ImprovementPercent < weakCorrection {
		suggestions = append(suggestions, Suggestion{
			Flags: "--smoothing-ms 200",
			Why: fmt.Sprintf("the correction only removes %.1f%% of the residual it was aimed at, "+
				"which usually means the derived window is too short for this artifact",
				in.ImprovementPercent),
		})
	}
	if in.ResidualRegions > 0 {
		suggestions = append(suggestions, Suggestion{
			Flags: "--sensitivity 1.3",
			Why: fmt.Sprintf("%d corrected region%s still trips detection after the bounded passes",
				in.ResidualRegions, plural(in.ResidualRegions)),
		})
	}
	if !in.RollingBaseline && in.DurationSeconds > 0 {
		suggestions = append(suggestions, Suggestion{
			Flags: NoFlag,
			Why: fmt.Sprintf("under %.0fs the thresholds come from one global baseline, "+
				"so --baseline-window has no effect on this clip", in.ShortClipSeconds),
		})
	}
	if verdict == VerdictPatch && in.Noise.NoisyFraction >= noisyShareMention {
		suggestions = append(suggestions, Suggestion{
			Flags: NoFlag,
			Why: fmt.Sprintf("%.0f%% of the clip (%s) sits above the %.1f °/s noise level; "+
				"detection is less sensitive over that stretch",
				in.Noise.NoisyFraction*100, seconds(in.Noise.NoisySeconds), in.Noise.NoisyDPS),
		})
	}
	if verdict == VerdictPatch && hasDropout(in.Events) {
		suggestions = append(suggestions, Suggestion{
			Flags: "--no-bridge",
			Why:   "if you would rather no orientation were reconstructed at all",
		})
	}
	return suggestions
}

func actionableCount(events []detect.Event) int {
	count := 0
	for _, event := range events {
		if event.Action != detect.ActionNone {
			count++
		}
	}
	return count
}

func hasDropout(events []detect.Event) bool {
	for _, event := range events {
		if event.Class == detect.ClassDropout && event.Action == detect.ActionBridge {
			return true
		}
	}
	return false
}

// clampUp rounds a fraction up to the next 5% so the suggested --max-affected
// clears the measured one rather than sitting exactly on it.
func clampUp(fraction float64) float64 {
	const step = 0.05
	return math.Ceil((fraction+1e-9)/step) * step
}

func seconds(value float64) string {
	if value < 1 {
		return fmt.Sprintf("%.0f ms", value*1000)
	}
	return fmt.Sprintf("%.2f s", value)
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
