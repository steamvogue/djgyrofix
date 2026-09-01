package advise_test

import (
	"strings"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/advise"
	"github.com/steamvogue/djgyrofix/internal/detect"
)

func tried(names ...string) map[string]bool {
	set := map[string]bool{}
	for _, name := range names {
		set[name] = true
	}
	return set
}

func TestAutopilotKeepsAProfileThatFits(t *testing.T) {
	input := base()
	input.Events = []detect.Event{smooth(1, 2, 9)}
	input.AffectedSeconds, input.AffectedFraction = 1, 1.0/30

	decision := advise.Step(input, tried("balanced"))
	if decision.Profile != "" || decision.Refuse {
		t.Errorf("a fitting profile should stand, got %+v", decision)
	}
	if decision.Reason == "" {
		t.Error("every decision must state its reason")
	}
}

func TestAutopilotStepsDownWhenOverTheLimit(t *testing.T) {
	input := base()
	input.Events = []detect.Event{smooth(1, 8, 9)}
	input.AffectedSeconds, input.AffectedFraction = 7, 7.0/30

	decision := advise.Step(input, tried("balanced"))
	if decision.Profile != "conservative" {
		t.Errorf("profile = %q, want conservative", decision.Profile)
	}
}

// TestAutopilotStepsOneRungAtATime is what stops a start on conservative from
// landing on aggressive: balanced is the profile most footage wants, and
// skipping it would be a worse answer than either neighbour.
func TestAutopilotStepsOneRungAtATime(t *testing.T) {
	input := base()
	input.Profile = "conservative"
	input.MinSeverity = 6.5
	input.NearMiss = 5

	decision := advise.Step(input, tried("conservative"))
	if decision.Profile != "balanced" {
		t.Errorf("profile = %q, want balanced — a step is one rung", decision.Profile)
	}

	input.Profile = "aggressive"
	input.Events = []detect.Event{smooth(1, 8, 9)}
	input.AffectedSeconds, input.AffectedFraction = 7, 7.0/30
	if decision := advise.Step(input, tried("aggressive")); decision.Profile != "balanced" {
		t.Errorf("stepping down from aggressive = %q, want balanced", decision.Profile)
	}
}

func TestAutopilotRefusesUpstreamFootage(t *testing.T) {
	input := base()
	input.Noise = detect.NoiseProfile{P10: 110, P50: 116, P90: 122, NoisyDPS: 30, NoisyFraction: 1, NoisySeconds: 30}

	decision := advise.Step(input, tried("balanced"))
	if !decision.Refuse {
		t.Fatalf("an airframe problem must be refused, got %+v", decision)
	}
	if decision.Profile != "" {
		t.Errorf("a refusal must not also name a profile to try, got %q", decision.Profile)
	}
	if !strings.Contains(decision.Reason, "no profile can patch") {
		t.Errorf("reason should say no profile helps, got %q", decision.Reason)
	}
}

// TestAutopilotRefusesRatherThanCycling pins termination: once every profile in
// the direction of travel has been tried, the answer is a refusal, not another
// step. Without this the loop could bounce between two profiles that each look
// better from where the other stands.
func TestAutopilotRefusesRatherThanCycling(t *testing.T) {
	input := base()
	input.Profile = "conservative"
	input.Events = []detect.Event{smooth(1, 8, 9)}
	input.AffectedSeconds, input.AffectedFraction = 7, 7.0/30

	decision := advise.Step(input, tried("conservative", "balanced", "aggressive"))
	if !decision.Refuse {
		t.Fatalf("with every profile tried the answer must be a refusal, got %+v", decision)
	}
}

func TestAutopilotStopsOnACleanClip(t *testing.T) {
	decision := advise.Step(base(), tried("balanced"))
	if decision.Profile != "" || decision.Refuse {
		t.Errorf("a clean clip needs no step and no refusal, got %+v", decision)
	}
	if !strings.Contains(decision.Reason, "nothing to patch") {
		t.Errorf("reason = %q", decision.Reason)
	}
}

// TestAutopilotDoesNotChaseNearMissesOnACleanClip keeps the step-up rule from
// firing on ordinary quiet footage: one or two events just under the cut is
// normal, and stepping up there would patch noise on every clean clip.
func TestAutopilotDoesNotChaseNearMissesOnACleanClip(t *testing.T) {
	input := base()
	input.NearMiss = 2

	if decision := advise.Step(input, tried("balanced")); decision.Profile != "" {
		t.Errorf("2 near misses should not move the profile, got %q", decision.Profile)
	}
}
