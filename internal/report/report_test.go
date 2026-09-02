package report_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/advise"
	"github.com/steamvogue/djgyrofix/internal/correct"
	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/report"
)

func sample() report.Report {
	return report.Report{
		File:            "DJI_0042.MP4",
		Variant:         "wm169",
		VariantDetected: "wm169",
		DurationSeconds: 1042,
		SampleCount:     36012,
		QuaternionCount: 144048,
		SampleRate:      199.8,
		VideoFPS:        29.97,
		BaselineDPS:     18.4,
		ThresholdDPS:    71.2,
		RollingBaseline: true,
		Events: []detect.Event{
			{
				StartSeconds: 72.480, EndSeconds: 72.560, PeakSeconds: 72.5,
				Class: detect.ClassDropout, Action: detect.ActionBridge,
				Severity: 9.1, SeverityLabel: "high", DominantAxes: []string{"Y"}, SpikeCount: 1,
			},
			{
				StartSeconds: 224.120, EndSeconds: 224.910, PeakSeconds: 224.4,
				Class: detect.ClassJitter, Action: detect.ActionSmooth,
				Severity: 6.8, SeverityLabel: "medium", DominantAxes: []string{"X", "Z"},
				SpikeCount: 9, SmoothingMS: 200,
			},
			{
				StartSeconds: 481.220, EndSeconds: 481.300, PeakSeconds: 481.25,
				Class: detect.ClassMotion, Action: detect.ActionNone,
				Severity: 7.4, SeverityLabel: "medium", DominantAxes: []string{"X"}, SpikeCount: 1,
				Note: "residual tracks intentional motion",
			},
		},
		AffectedSeconds:  0.95,
		AffectedFraction: 0.00091,
	}
}

func render(t *testing.T, reports []report.Report, format string) string {
	t.Helper()
	var buffer bytes.Buffer
	if err := report.Write(&buffer, reports, format); err != nil {
		t.Fatalf("format %s: %v", format, err)
	}
	return buffer.String()
}

func TestTimestampFormatting(t *testing.T) {
	cases := map[float64]string{
		0:        "00:00:00.000",
		72.48:    "00:01:12.480",
		3661.5:   "01:01:01.500",
		-5:       "00:00:00.000",
		59.9996:  "00:01:00.000", // the carry must reach the minutes field
		1042.125: "00:17:22.125",
	}
	for seconds, want := range cases {
		if got := report.Timestamp(seconds); got != want {
			t.Errorf("Timestamp(%g) = %q, want %q", seconds, got, want)
		}
	}
}

func TestTimecodeFormatting(t *testing.T) {
	cases := []struct {
		seconds, fps float64
		want         string
	}{
		{0, 30, "00:00:00:00"},
		{1, 30, "00:00:01:00"},
		{1.5, 30, "00:00:01:15"},
		{3661, 25, "01:01:01:00"},
		{5, 0, "00:00:05:00"}, // a missing frame rate falls back to 30
	}
	for _, test := range cases {
		if got := report.Timecode(test.seconds, test.fps); got != test.want {
			t.Errorf("Timecode(%g, %g) = %q, want %q", test.seconds, test.fps, got, test.want)
		}
	}
}

func TestTextReportNamesEveryEvent(t *testing.T) {
	output := render(t, []report.Report{sample()}, "text")
	for _, want := range []string{
		"DJI_0042.MP4", "wm169", "rolling",
		"00:01:12.480", "dropout", "bridge",
		"00:03:44.120", "jitter", "smooth",
		"motion", "none", "residual tracks intentional motion",
		"3 events",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("text report is missing %q\n%s", want, output)
		}
	}
}

func TestTextReportSaysWhatADryRunWouldDo(t *testing.T) {
	value := sample()
	value.DryRun = true
	value.Writes = 1284
	value.BytesWritten = 5136
	value.QuaternionsChanged = 321
	value.SamplesChanged = 80
	output := render(t, []report.Report{value}, "text")
	if !strings.Contains(output, "dry run") || !strings.Contains(output, "fix --apply DJI_0042.MP4") {
		t.Errorf("a dry run did not say so:\n%s", output)
	}
}

func TestJSONReportIsMachineReadable(t *testing.T) {
	output := render(t, []report.Report{sample()}, "json")
	var decoded report.Report
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("single-file JSON is not valid: %v\n%s", err, output)
	}
	if len(decoded.Events) != 3 || decoded.Events[0].Class != detect.ClassDropout {
		t.Errorf("events did not survive the round trip: %+v", decoded.Events)
	}

	// Several files must come back as an array, not concatenated objects.
	output = render(t, []report.Report{sample(), sample()}, "json")
	var many []report.Report
	if err := json.Unmarshal([]byte(output), &many); err != nil {
		t.Fatalf("multi-file JSON is not valid: %v\n%s", err, output)
	}
	if len(many) != 2 {
		t.Errorf("got %d reports, want 2", len(many))
	}
}

func TestCSVReportHasOneRowPerEvent(t *testing.T) {
	output := render(t, []report.Report{sample()}, "csv")
	rows, err := csv.NewReader(strings.NewReader(output)).ReadAll()
	if err != nil {
		t.Fatalf("CSV is not parseable: %v\n%s", err, output)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows including the header, want 4", len(rows))
	}
	if rows[0][0] != "file" || rows[1][6] != string(detect.ClassDropout) {
		t.Errorf("unexpected CSV shape: %v", rows[:2])
	}
}

func TestEDLReportIsWellFormed(t *testing.T) {
	output := render(t, []report.Report{sample()}, "edl")
	// 72.480 s at 29.97 fps is frame 2172, which non-drop timecode shows as
	// 72 whole seconds plus 12 frames.
	for _, want := range []string{"TITLE:", "FCM: NON-DROP FRAME", "001  AX", "* FROM CLIP NAME:", "00:01:12:12"} {
		if !strings.Contains(output, want) {
			t.Errorf("EDL is missing %q\n%s", want, output)
		}
	}
}

func TestUnknownFormatIsRejected(t *testing.T) {
	var buffer bytes.Buffer
	if err := report.Write(&buffer, []report.Report{sample()}, "yaml"); err == nil {
		t.Error("an unknown format was accepted")
	}
}

func TestImprovementPercentIsClamped(t *testing.T) {
	value := sample()
	value.ScoreBefore, value.ScoreAfter = 100, 30
	if got := value.ImprovementPercent(); got < 69.9 || got > 70.1 {
		t.Errorf("ImprovementPercent = %g, want 70", got)
	}
	// A correction that made the metric worse must report zero, not a negative.
	value.ScoreAfter = 200
	if got := value.ImprovementPercent(); got != 0 {
		t.Errorf("ImprovementPercent = %g for a worse score, want 0", got)
	}
	value.ScoreBefore = 0
	if got := value.ImprovementPercent(); got != 0 {
		t.Errorf("ImprovementPercent = %g with no baseline, want 0", got)
	}
}

// TestRangesRoundTripsAScanReport is what makes --format text or json
// actionable: the reported events can be handed straight back as --ranges.
func TestRangesRoundTripsAScanReport(t *testing.T) {
	got := report.Ranges(sample().Events)
	if got != "72.480-72.560,224.120-224.910" {
		t.Errorf("Ranges = %q", got)
	}
	if report.Ranges(nil) != "" {
		t.Error("Ranges of nothing should be empty")
	}
}

func TestNoEventsIsSaidPlainly(t *testing.T) {
	value := sample()
	value.Events = nil
	value.AffectedSeconds = 0
	output := render(t, []report.Report{value}, "text")
	if !strings.Contains(output, "no events") {
		t.Errorf("a clean report did not say so:\n%s", output)
	}
}

func TestAppliedReportExplainsTheResultAndGatesARetryOnVisibleTwitching(t *testing.T) {
	value := sample()
	value.Operation = "fix"
	value.Applied = true
	value.JournalPath = "DJI_0042.MP4.gyrofix.json"
	value.ScoreBefore, value.ScoreAfter = 100, 15.3
	value.ClipScoreBefore, value.ClipScoreAfter = 100, 95
	value.AffectedFraction = 0.04
	value.Repair = &correct.RepairStats{
		RunsReplaced: 2372, SamplesReplaced: 21843, RunsTooLong: 10, RunsRealMotion: 44,
	}
	value.Warnings = []string{"42 original correction region(s) remain detectable after 3 bounded pass(es)"}
	value.Advice = &advise.Advice{
		Verdict:       advise.VerdictPatch,
		Headline:      "81 correctable events over 19.82 s (3.99% of the clip) — this is what djgyrofix is for",
		PreviewFile:   "DJI_0042.MP4",
		RevertCommand: "djgyrofix revert DJI_0042.MP4",
		Suggestions: []advise.Suggestion{{
			Flags:   "--sensitivity 1.3",
			Why:     "42 corrected regions remain above the detector; 1.3 lowers the threshold and gives their residual edges more correction weight",
			When:    "the stabilized video still twitches at those times",
			Command: "djgyrofix fix --apply --sensitivity 1.3 DJI_0042.MP4",
		}},
	}

	output := render(t, []report.Report{value}, "text")
	for _, want := range []string{
		"interpolated 2372 short artifact runs",
		"motion-like runs",
		"bounded",
		"smoothing instead",
		"84.7% inside the",
		"corrected regions",
		"96.0% of footage outside",
		"correction regions",
		"Preview DJI_0042.MP4 in Gyroflow",
		"If stabilization is smooth, stop",
		"a residual warning alone does not mean",
		"the repair failed",
		"Only if the stabilized video still twitches",
		"djgyrofix revert DJI_0042.MP4",
		"djgyrofix fix --apply --sensitivity 1.3 DJI_0042.MP4",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("guided report is missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "warning:") > strings.Index(output, "next:") {
		t.Errorf("warning must be visible before the action plan:\n%s", output)
	}
	if strings.Contains(output, "try --sensitivity 1.3") {
		t.Errorf("report fell back to an unscoped floating flag:\n%s", output)
	}
}

func TestRunRepairExplainsWhenSmoothingDidAllTheWork(t *testing.T) {
	value := sample()
	value.Events = []detect.Event{{Class: detect.ClassImpact, Action: detect.ActionSmooth}}
	value.Repair = &correct.RepairStats{}
	output := render(t, []report.Report{value}, "text")
	for _, want := range []string{
		"no short artifact runs qualified for interpolation",
		"bounded smoothing handled the 1 detected event instead",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("fallback report is missing %q:\n%s", want, output)
		}
	}

	value.Events = nil
	output = render(t, []report.Report{value}, "text")
	if strings.Contains(output, "run-repair:") {
		t.Errorf("a clean run printed a meaningless zero-work repair line:\n%s", output)
	}
}

func TestOutputCopyRetrySaysForceOnlyReplacesTheCopy(t *testing.T) {
	value := sample()
	value.Applied = true
	value.OutputPath = "fixed.MP4"
	value.Advice = &advise.Advice{
		Verdict:     advise.VerdictPatch,
		PreviewFile: "fixed.MP4",
		Suggestions: []advise.Suggestion{{
			Flags:   "--sensitivity 1.3",
			Why:     "one region remains above the detector",
			When:    "the stabilized video still twitches",
			Command: "djgyrofix fix --apply --sensitivity 1.3 --out fixed.MP4 --force DJI_0042.MP4",
		}},
	}
	output := render(t, []report.Report{value}, "text")
	for _, want := range []string{"untouched source", "--force replaces", "only that derived copy", "--out fixed.MP4"} {
		if !strings.Contains(output, want) {
			t.Errorf("copy retry is missing %q:\n%s", want, output)
		}
	}
}
