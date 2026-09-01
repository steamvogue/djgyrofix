package advise

import "fmt"

// Autopilot decides, from a first detection pass, whether a different profile
// would serve the clip better — and whether patching should happen at all.
//
// It is deliberately rule-based rather than an optimizer. Nothing here searches
// for the parameter set that minimises some residual score, because that search
// has an obvious degenerate answer: smooth everything, and every residual
// metric improves. The rules only ever step one profile in a direction the
// measurement already justifies, and each step names the measurement that
// caused it, so an autopilot run stays as auditable as a hand-tuned one.
//
// Profiles are ordered conservative < balanced < aggressive. A run makes at
// most one step in each direction and never revisits a profile it has tried,
// which bounds the work at three detection passes and one correction pass.

// ladder is the profiles in order of how much they detect. A step moves one
// rung, never two: jumping from conservative straight to aggressive because
// conservative found nothing would skip the profile that is right for most
// footage, and it is the one the user is most likely to have wanted.
var ladder = []string{"conservative", "balanced", "aggressive"}

// stricter and looser return the adjacent untried profile in each direction, or
// an empty string when there is none left to try.
func stricter(profile string, tried map[string]bool) string { return neighbour(profile, tried, -1) }
func looser(profile string, tried map[string]bool) string   { return neighbour(profile, tried, +1) }

func neighbour(profile string, tried map[string]bool, direction int) string {
	position := -1
	for index, name := range ladder {
		if name == profile {
			position = index
			break
		}
	}
	if position < 0 {
		return ""
	}
	for step := position + direction; step >= 0 && step < len(ladder); step += direction {
		if !tried[ladder[step]] {
			return ladder[step]
		}
	}
	return ""
}

// Decision is one autopilot outcome.
type Decision struct {
	// Profile is the profile to try next. Empty means the current one stands.
	Profile string `json:"profile,omitempty"`
	// Refuse means the clip should not be patched at all.
	Refuse bool `json:"refuse,omitempty"`
	// Reason is the measurement that drove the decision, for the report.
	Reason string `json:"reason"`
}

// Step returns the next autopilot move for a completed detection pass.
//
// tried holds the profiles already evaluated in this run, so a step never
// bounces between two profiles that each look better from the other.
func Step(in Input, tried map[string]bool) Decision {
	switch {
	case in.Noise.NoisyFraction >= noisyShareUpstream:
		return Decision{
			Refuse: true,
			Reason: fmt.Sprintf(
				"the noise floor is at or above %.1f °/s across %.0f%% of the clip; "+
					"no profile can patch an airframe problem",
				in.Noise.NoisyDPS, in.Noise.NoisyFraction*100),
		}

	case in.AffectedFraction > in.MaxAffected:
		if next := stricter(in.Profile, tried); next != "" {
			return Decision{
				Profile: next,
				Reason: fmt.Sprintf("%.1f%% affected is over the %.0f%% limit; stepping down to %s",
					in.AffectedFraction*100, in.MaxAffected*100, next),
			}
		}
		return Decision{
			Refuse: true,
			Reason: fmt.Sprintf(
				"still %.1f%% affected on the %s profile, over the %.0f%% limit — "+
					"this needs a human, not a preset",
				in.AffectedFraction*100, in.Profile, in.MaxAffected*100),
		}

	case actionableCount(in.Events) == 0 && in.NearMiss >= nearMissTrigger && looser(in.Profile, tried) != "":
		next := looser(in.Profile, tried)
		return Decision{
			Profile: next,
			Reason: fmt.Sprintf("nothing kept, but %d event%s scored just under --min-severity %.1f; stepping up to %s",
				in.NearMiss, plural(in.NearMiss), in.MinSeverity, next),
		}

	case actionableCount(in.Events) == 0:
		return Decision{Reason: "no correctable artifacts; nothing to patch"}

	default:
		return Decision{Reason: fmt.Sprintf("%s profile fits: %d event%s over %s (%.2f%% of the clip)",
			in.Profile, actionableCount(in.Events), plural(actionableCount(in.Events)),
			seconds(in.AffectedSeconds), in.AffectedFraction*100)}
	}
}
