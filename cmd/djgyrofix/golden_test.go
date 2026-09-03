package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/advise"
	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/report"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

// updateDetection rewrites the pinned tables instead of comparing against them.
// Regenerating is the normal response to an intended change: run with -update,
// then read the diff. The diff is the review.
var updateDetection = flag.Bool("update", false, "rewrite the detection golden tables")

// The detection goldens pin what pass 1 decides and what pass 2 plans in
// response, so that a change to either arrives as a reviewable diff instead of
// as a number nobody thought to check.
//
// Golden parity does not cover this ground. testdata/golden/parity.sh drives
// --ranges, which skips detection entirely: it guards the numeric core against
// the Python reference byte for byte, and says nothing about which events are
// found or how they are classified. Detection can be badly wrong with parity
// green.
//
// What a diff here means depends on the fixture. The synthetic cases carry
// ground truth by construction — synth injects an artifact of known kind at a
// known time — so `whippan` finding anything actionable is a false positive on
// intentional motion, which is the failure mode that matters most: a
// correction there invents an orientation the aircraft never held. The corpus
// cases carry no ground truth at all. Nobody has labelled which 140 ms of real
// footage is a wobble and which is a control input, so those tables pin
// behaviour, not correctness. A diff in them is a question to go and answer at
// the timestamps that moved, never a verdict on its own.

type detectionCase struct {
	name string
	// defect builds a synthetic clip. Empty when the case reads real footage.
	defect synth.Defect
	// corpus names a file under testdata/. Real captures are multi-gigabyte and
	// gitignored, so those cases skip wherever the file is absent — which is
	// everywhere but the machine that recorded them, CI included. Their tables
	// are still committed: a table that only one machine can regenerate is
	// still worth reading on any of them.
	corpus string
	tune   func(*options)
}

var detectionCases = []detectionCase{
	// One case per injected defect. Between them these cover every class the
	// classifier can return and both actions it can take.
	{name: "synth-clean", defect: synth.DefectNone},
	{name: "synth-jitter", defect: synth.DefectJitter},
	{name: "synth-impact", defect: synth.DefectImpact},
	{name: "synth-dropout", defect: synth.DefectDropout},
	{name: "synth-whippan", defect: synth.DefectWhipPan},
	{name: "synth-vector-change", defect: synth.DefectVectorChange},
	{name: "synth-vector-jitter", defect: synth.DefectVectorJitter},
	{name: "synth-mixed", defect: synth.DefectMixed},

	// The same clip through the tuning surface. A change that only moves the
	// presets, or only bites once the floor is lowered, is invisible in the
	// default-profile tables above.
	{
		name:   "synth-mixed-aggressive",
		defect: synth.DefectMixed,
		tune:   func(o *options) { o.profile = "aggressive" },
	},
	{
		name:   "synth-mixed-conservative",
		defect: synth.DefectMixed,
		tune:   func(o *options) { o.profile = "conservative" },
	},
	{
		name:   "synth-mixed-floor20",
		defect: synth.DefectMixed,
		tune:   func(o *options) { o.floorDPS = 20 },
	},
	// Correction planning, not detection: the same events, corrected the other
	// way. This is what catches a run-selection change that leaves the event
	// table untouched.
	{
		name:   "synth-mixed-blur",
		defect: synth.DefectMixed,
		tune:   func(o *options) { o.repair = "blur" },
	},

	{name: "corpus-raw", corpus: "DJI_20260705_RAW.MP4"},
	{
		name:   "corpus-raw-floor20",
		corpus: "DJI_20260705_RAW.MP4",
		tune:   func(o *options) { o.floorDPS = 20 },
	},
	{name: "corpus-villa2", corpus: "DJI_20260830_VILLA2.MP4"},
	// An Osmo, which reaches the same wm169 path under a "CAM meta" handler
	// rather than "DJI meta", and stores 989 Hz of unduplicated attitude where
	// the air units store 1978 Hz of which half is padding. It is also the only
	// clip here that comes back upstream, so it pins that verdict against a real
	// measurement rather than a synthetic noise floor.
	{name: "corpus-osmo", corpus: "OSM_20260808192827_0003_D.MP4"},
}

func TestDetectionGolden(t *testing.T) {
	for _, testCase := range detectionCases {
		t.Run(testCase.name, func(t *testing.T) {
			path := testCase.fixture(t)
			opts := defaultOptions()
			if testCase.tune != nil {
				testCase.tune(opts)
			}
			if err := opts.validateCommon(); err != nil {
				t.Fatalf("options: %v", err)
			}
			result, err := analyze(path, opts, nil)
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			compareGolden(t, testCase.name, renderDetection(result.report))
		})
	}
}

// fixture returns the clip to analyse, skipping the case when real footage is
// not on this machine.
func (c detectionCase) fixture(t *testing.T) string {
	t.Helper()
	if c.corpus == "" {
		return writeFixture(t, c.defect)
	}
	path := filepath.Join("..", "..", "testdata", c.corpus)
	info, err := os.Stat(path)
	if err != nil {
		t.Skipf("corpus clip %s is not on this machine", c.corpus)
	}
	// Reading several gigabytes to reach the metadata track is the whole cost
	// of these cases, and it is I/O the short suite has no reason to pay.
	if testing.Short() {
		t.Skipf("skipping %.1f GB corpus clip in short mode", float64(info.Size())/(1<<30))
	}
	return path
}

func compareGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", "detection", name+".txt")
	if *updateDetection {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no pinned table (run go test ./cmd/djgyrofix -run TestDetectionGolden -update): %v", err)
	}
	if string(want) == got {
		return
	}
	t.Errorf("detection changed for %s.\n"+
		"Read the diff: if it is the change you meant, re-pin it with\n"+
		"  go test ./cmd/djgyrofix -run TestDetectionGolden -update\n\n%s",
		name, firstDifference(string(want), got))
}

// firstDifference reports the first line that moved, with a little context.
// A whole-table dump is unreadable for the corpus cases, which run to a hundred
// events; the line that changed is what the reviewer needs.
func firstDifference(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for index := 0; index < len(wantLines) || index < len(gotLines); index++ {
		wantLine, gotLine := lineAt(wantLines, index), lineAt(gotLines, index)
		if wantLine == gotLine {
			continue
		}
		var out strings.Builder
		fmt.Fprintf(&out, "first change at line %d", index+1)
		if delta := len(gotLines) - len(wantLines); delta != 0 {
			fmt.Fprintf(&out, " (%+d lines overall)", delta)
		}
		fmt.Fprintf(&out, "\n")
		for context := max(0, index-2); context < index; context++ {
			fmt.Fprintf(&out, "  %s\n", lineAt(wantLines, context))
		}
		fmt.Fprintf(&out, "- %s\n+ %s\n", wantLine, gotLine)
		return out.String()
	}
	return "tables differ only in trailing content"
}

func lineAt(lines []string, index int) string {
	if index < 0 || index >= len(lines) {
		return "(end of table)"
	}
	return lines[index]
}

// renderDetection lays one report out as a fixed-width table.
//
// Every number is printed at the precision the text report already rounds to.
// That is deliberate: the numeric core is held to bit-identical results across
// platforms, but detection runs percentiles and medians over the whole clip on
// top of it, and pinning raw float64s would turn any last-digit difference into
// a CI failure that says nothing about behaviour. The file path is left out
// entirely — it is a temporary directory on the synthetic cases.
func renderDetection(rep report.Report) string {
	var out strings.Builder

	fmt.Fprintf(&out, "clip       %.3f s  %d samples  %d quaternions @ %.1f Hz  duplicates %.1f%%\n",
		rep.DurationSeconds, rep.SampleCount, rep.QuaternionCount, rep.SampleRate, rep.DuplicateShare*100)
	baseline := "global"
	if rep.RollingBaseline {
		baseline = "rolling"
	}
	fmt.Fprintf(&out, "baseline   %.1f dps  threshold %.1f dps (%s)\n",
		rep.BaselineDPS, rep.ThresholdDPS, baseline)
	fmt.Fprintf(&out, "noise      p10 %.1f  p50 %.1f  p90 %.1f dps  noisy %.1f%% (%.2f s) at >= %.1f dps\n",
		rep.Noise.P10, rep.Noise.P50, rep.Noise.P90,
		rep.Noise.NoisyFraction*100, rep.Noise.NoisySeconds, rep.Noise.NoisyDPS)
	fmt.Fprintf(&out, "events     %d found  %d actionable  %d near-miss  %.3f s affected (%.2f%%)\n",
		len(rep.Events), actionable(rep.Events), rep.NearMissEvents,
		rep.AffectedSeconds, rep.AffectedFraction*100)

	if len(rep.Events) > 0 {
		out.WriteString("\n")
		fmt.Fprintf(&out, "  %3s %10s %10s  %-8s %-7s %5s %9s %7s  %-9s %5s %9s\n",
			"#", "start", "end", "class", "action", "sev", "peak_dps", "ratio", "axes", "peaks", "window_ms")
		for index, event := range rep.Events {
			fmt.Fprintf(&out, "  %3d %10.3f %10.3f  %-8s %-7s %5.1f %9.1f %7.1f  %-9s %5d %9.1f\n",
				index+1, event.StartSeconds, event.EndSeconds, event.Class, event.Action,
				event.Severity, event.PeakDPS, event.BaselineRatio,
				strings.Join(event.DominantAxes, "/"), event.SpikeCount, event.SmoothingMS)
			if event.Note != "" {
				fmt.Fprintf(&out, "      note: %s\n", event.Note)
			}
		}
	}

	out.WriteString("\n")
	if rep.Repair != nil {
		fmt.Fprintf(&out, "run-repair %d runs interpolated (%d quaternions)  %d too long  %d motion-like\n",
			rep.Repair.RunsReplaced, rep.Repair.SamplesReplaced,
			rep.Repair.RunsTooLong, rep.Repair.RunsRealMotion)
	}
	fmt.Fprintf(&out, "writes     %d quaternions in %d samples\n",
		rep.QuaternionsChanged, rep.SamplesChanged)
	if rep.ScoreBefore > 0 {
		fmt.Fprintf(&out, "residual   %.1f%% reduced in region  %.1f%% clip-wide\n",
			rep.ImprovementPercent(), rep.ClipImprovementPercent())
	}
	if rep.Advice != nil {
		fmt.Fprintf(&out, "verdict    %s\n", rep.Advice.Verdict)
		for _, suggestion := range rep.Advice.Suggestions {
			// The flags are the decision; the prose behind them is presentation,
			// covered by advise's own tests. Pinning the wording here would churn
			// every table on a copy edit and teach the reviewer to re-pin without
			// reading, which is the one habit this harness cannot afford.
			flags := suggestion.Flags
			if flags == advise.NoFlag {
				flags = "(note)"
			}
			fmt.Fprintf(&out, "suggests   %s\n", flags)
		}
	}
	for _, warning := range rep.Warnings {
		fmt.Fprintf(&out, "warning    %s\n", warning)
	}
	return out.String()
}

func actionable(events []detect.Event) int {
	count := 0
	for _, event := range events {
		if event.Action != detect.ActionNone {
			count++
		}
	}
	return count
}
