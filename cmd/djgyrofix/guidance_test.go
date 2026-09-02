package main

import (
	"strings"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/advise"
	"github.com/steamvogue/djgyrofix/internal/report"
)

func patchReport(file string) report.Report {
	return report.Report{
		File: file,
		Advice: &advise.Advice{
			Verdict: advise.VerdictPatch,
			Suggestions: []advise.Suggestion{{
				Flags: "--sensitivity 1.3",
				Why:   "residual regions remain detectable",
				When:  "the stabilized video still twitches at those times",
			}},
		},
	}
}

func TestFinalizeReportPreservesSettingsAndReplacesTheSuggestedFlag(t *testing.T) {
	rep := patchReport("source clip.MP4")
	opts := defaultOptions()
	opts.profile = "aggressive"
	opts.style = "freestyle"
	opts.sensitivity = 0.8
	opts.floorDPS = 20
	opts.variant = "wm169"
	opts.repair = "blur"
	opts.strength = 0.5

	finalizeReport(&rep, opts, "scan")

	if rep.Operation != "scan" {
		t.Errorf("operation = %q, want scan", rep.Operation)
	}
	wantNext := `djgyrofix fix --apply --profile aggressive --style freestyle --sensitivity 0.8 --floor-dps 20 --variant wm169 --repair blur --strength 0.5 "source clip.MP4"`
	if rep.Advice.NextCommand != wantNext {
		t.Errorf("next command:\n got %s\nwant %s", rep.Advice.NextCommand, wantNext)
	}
	retry := rep.Advice.Suggestions[0].Command
	if !strings.Contains(retry, "--sensitivity 1.3") || strings.Contains(retry, "--sensitivity 0.8") {
		t.Errorf("suggestion did not replace the current sensitivity: %s", retry)
	}
	if rep.Advice.RevertCommand != `djgyrofix revert "source clip.MP4"` {
		t.Errorf("revert command = %q", rep.Advice.RevertCommand)
	}
}

func TestZeroStrengthIsPreservedInTheApplyCommand(t *testing.T) {
	opts := defaultOptions()
	opts.strength = 0
	command := fixCommand(opts, "clip.MP4", "", true, false)
	if !strings.Contains(command, "--strength 0") {
		t.Errorf("zero-strength dry run would turn into a full-strength apply: %s", command)
	}
}

func TestSuggestedCommandsTerminateFlagsBeforeADashFilename(t *testing.T) {
	rep := patchReport("-clip.MP4")
	finalizeReport(&rep, defaultOptions(), "scan")
	if !strings.HasSuffix(rep.Advice.NextCommand, "-- -clip.MP4") {
		t.Errorf("apply command would parse the filename as a flag: %s", rep.Advice.NextCommand)
	}
	if rep.Advice.RevertCommand != "djgyrofix revert -- -clip.MP4" {
		t.Errorf("revert command would parse the filename as a flag: %s", rep.Advice.RevertCommand)
	}
}

func TestOutputCopyRetryOverwritesOnlyTheDerivedCopy(t *testing.T) {
	rep := patchReport("source clip.MP4")
	rep.Applied = true
	opts := defaultOptions()
	opts.out = "fixed copy.MP4"

	finalizeReport(&rep, opts, "fix")

	if rep.Advice.PreviewFile != "fixed copy.MP4" {
		t.Errorf("preview file = %q", rep.Advice.PreviewFile)
	}
	if rep.Advice.RevertCommand != "" {
		t.Errorf("an --out retry must not revert the source: %s", rep.Advice.RevertCommand)
	}
	if rep.Advice.NextCommand != "" {
		t.Errorf("an applied report must not suggest applying the same patch again: %s", rep.Advice.NextCommand)
	}
	retry := rep.Advice.Suggestions[0].Command
	for _, want := range []string{`--sensitivity 1.3`, `--out "fixed copy.MP4"`, `--force`, `"source clip.MP4"`} {
		if !strings.Contains(retry, want) {
			t.Errorf("copy retry is missing %q: %s", want, retry)
		}
	}
}

func TestInPlaceRetryWithoutAJournalDoesNotSuggestCompounding(t *testing.T) {
	rep := patchReport("clip.MP4")
	rep.Applied = true
	opts := defaultOptions()
	opts.backup = "none"

	finalizeReport(&rep, opts, "fix")

	if rep.Advice.RevertCommand != "" || rep.Advice.Suggestions[0].Command != "" {
		t.Errorf("unsafe retry was offered without a journal: %+v", rep.Advice)
	}
}
