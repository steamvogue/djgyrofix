package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/advise"
	"github.com/steamvogue/djgyrofix/internal/patch"
	"github.com/steamvogue/djgyrofix/internal/quat"
	"github.com/steamvogue/djgyrofix/internal/report"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

// writeRoughFixture builds a clip with broadband shake over the first
// roughFraction of its length — footage whose problem is the airframe, not the
// recorded metadata.
func writeRoughFixture(t *testing.T, roughFraction float64) string {
	t.Helper()
	const rate, perSample, seconds = 200.0, 4, 30.0
	track := synth.Attitude(synth.AttitudeOptions{
		Defect: synth.DefectMixed, Rate: rate, Seconds: seconds, Seed: 20260901,
		RoughUntil: seconds * roughFraction,
	})
	built, err := synth.Build(synth.Options{
		Timescale:        1000,
		SampleCount:      int(seconds * rate / perSample),
		QuatsPerSample:   perSample,
		SampleDeltaTicks: uint32(1000 * perSample / rate),
		SamplesPerChunk:  64,
		WithVideoTrack:   true,
		Quaternion: func(sampleIndex, subIndex int) quat.Q {
			index := sampleIndex*perSample + subIndex
			if index >= len(track) {
				index = len(track) - 1
			}
			return track[index]
		},
	})
	if err != nil {
		t.Fatalf("synth.Build: %v", err)
	}
	path := filepath.Join(t.TempDir(), "DJI_ROUGH.MP4")
	if err := os.WriteFile(path, built.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEveryAutomaticScanCarriesAVerdict(t *testing.T) {
	for _, defect := range []synth.Defect{synth.DefectNone, synth.DefectMixed, synth.DefectWhipPan} {
		t.Run(string(defect), func(t *testing.T) {
			result, err := analyze(writeFixture(t, defect), defaultOptions(), nil)
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			if result.report.Advice == nil {
				t.Fatal("an automatic scan produced no diagnosis")
			}
			if result.report.Advice.Headline == "" {
				t.Error("the diagnosis has no headline")
			}
		})
	}
}

// TestManualRangesCarryNoVerdict keeps the advisor out of the path where the
// user has already decided what to correct. There is nothing to diagnose when
// detection did not run.
func TestManualRangesCarryNoVerdict(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	result, err := analyze(path, defaultOptions(), []interval{{start: 8, end: 9}})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if result.report.Advice != nil {
		t.Errorf("the manual --ranges path produced a diagnosis: %+v", result.report.Advice)
	}
}

// TestRoughFootageIsDiagnosedNotReassured is the behaviour the rolling
// threshold made necessary. On a resonating airframe the threshold climbs with
// the noise, so the event list stays short and a per-event report reads as
// good news. The verdict has to say otherwise.
func TestRoughFootageIsDiagnosedNotReassured(t *testing.T) {
	result, err := analyze(writeRoughFixture(t, 1.0), defaultOptions(), nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if got := result.report.Advice.Verdict; got != advise.VerdictUpstream {
		t.Fatalf("verdict = %q, want upstream (events found: %d, noisy share %.2f)",
			got, len(result.report.Events), result.report.Noise.NoisyFraction)
	}
	if result.report.Advice.NextCommand != "" {
		t.Error("footage no patch can help must not come with a patch command")
	}
}

// TestPartlyRoughFootageIsStillCaught is the case a median baseline cannot see:
// two thirds of this clip is clean, so the reported baseline looks fine.
func TestPartlyRoughFootageIsStillCaught(t *testing.T) {
	result, err := analyze(writeRoughFixture(t, 1.0/3.0), defaultOptions(), nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	// The fixture is rough for a third of its length. The measured share comes
	// out lower because the rolling baseline tapers across the boundary, so
	// this asserts the verdict and a clear margin over the threshold rather
	// than a number that would sit on the knife edge.
	if result.report.Noise.NoisyFraction < 0.22 {
		t.Fatalf("noisy share %.2f, want the rough third to register with margin",
			result.report.Noise.NoisyFraction)
	}
	if got := result.report.Advice.Verdict; got != advise.VerdictUpstream {
		t.Errorf("verdict = %q, want upstream", got)
	}
}

func TestAutopilotRefusesToPatchUpstreamFootage(t *testing.T) {
	path := writeRoughFixture(t, 1.0)
	before := readFile(t, path)

	opts := defaultOptions()
	opts.auto = true
	opts.apply = true
	if _, err := fixOne(path, opts, nil); err == nil {
		t.Fatal("autopilot patched footage no profile can help")
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("the refused run modified the file")
	}
	if _, err := os.Stat(patch.JournalPath(path)); !os.IsNotExist(err) {
		t.Error("the refused run wrote a journal")
	}

	// The refusal is advice, not a lock: the events it found are real, and a
	// pilot who has read the diagnosis may still want them smoothed.
	opts.force = true
	result, err := fixOne(path, opts, nil)
	if err != nil {
		t.Fatalf("--force did not override the autopilot refusal: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("the override was applied without a warning")
	}
}

// TestAutopilotRecordsWhatItChose is the audit requirement: a run that quietly
// used different parameters than the flags asked for would be worse than no
// autopilot at all.
func TestAutopilotRecordsWhatItChose(t *testing.T) {
	opts := defaultOptions()
	opts.auto = true
	result, err := analyze(writeFixture(t, synth.DefectMixed), opts, nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if result.report.Auto == nil {
		t.Fatal("an autopilot run left no record")
	}
	if result.report.Auto.Profile == "" || len(result.report.Auto.Steps) == 0 {
		t.Errorf("the record names no profile or no reason: %+v", result.report.Auto)
	}
	if result.report.Auto.Refused {
		t.Error("a patchable clip was refused")
	}
}

// TestAutopilotStepsDownRatherThanOverflowing checks the loop end to end: with
// a budget the balanced profile cannot meet, autopilot has to try the stricter
// profile before giving up.
func TestAutopilotStepsDownRatherThanOverflowing(t *testing.T) {
	opts := defaultOptions()
	opts.auto = true
	opts.maxAffected = 0.001
	result, err := analyze(writeFixture(t, synth.DefectMixed), opts, nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	trail := strings.Join(result.report.Auto.Attempts, ",")
	if !strings.Contains(trail, "conservative") {
		t.Errorf("autopilot never tried the stricter profile, trail: %s", trail)
	}
	if !result.report.Auto.Refused {
		t.Error("a budget no profile can meet should end in a refusal")
	}
}

// TestExplicitFlagsSurviveAutopilot pins the precedence: autopilot chooses a
// profile, not a whole parameter set, so a flag the user typed still wins.
func TestExplicitFlagsSurviveAutopilot(t *testing.T) {
	opts := defaultOptions()
	opts.auto = true
	opts.maxAffected = 0.001
	opts.floorDPS = 123
	result, err := analyze(writeFixture(t, synth.DefectMixed), opts, nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if got := result.params["floor_dps"]; got != 123.0 {
		t.Errorf("floor_dps = %v after autopilot stepped profile, want the flag's 123", got)
	}
}

// adviceLineLimit is the widest a folded prose line may be: the wrap width
// plus the deepest continuation indent the block uses.
const adviceLineLimit = 86

func TestTextReportRendersTheDiagnosis(t *testing.T) {
	opts := defaultOptions()
	result, err := analyze(writeFixture(t, synth.DefectMixed), opts, nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	finalizeReport(&result.report, opts, "scan")
	var buffer bytes.Buffer
	if err := report.Write(&buffer, []report.Report{result.report}, "text"); err != nil {
		t.Fatal(err)
	}
	output := buffer.String()
	for _, want := range []string{"diagnosis:", "next:", "Apply the planned correction:", "djgyrofix fix --apply", "Preview"} {
		if !strings.Contains(output, want) {
			t.Errorf("the text report omits %q\n%s", want, output)
		}
	}
	// The prose has to fold; a path or a command to paste must not, so lines
	// carrying the file name are exempt from the width check.
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, result.report.File) {
			continue
		}
		if len([]rune(line)) > adviceLineLimit {
			t.Errorf("a report line is %d columns wide:\n%s", len([]rune(line)), line)
		}
	}
}
