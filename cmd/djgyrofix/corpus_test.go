package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

// Detection has no ground truth on real footage. The pinned tables in
// golden_test.go record what the detector decides; nothing there can say whether
// deciding it was right. DESIGN §13.4 wants that answered against a labelled
// corpus, weighting false positives above misses, because a correction applied
// to intentional motion invents an orientation the aircraft never held while a
// miss only leaves footage as it already was.
//
// This is the scoring half of that, built before the footage so a clip can be
// labelled and scored the day it lands. Labels come from two places: real clips
// carry a CSV a human filled in while watching, and synthetic clips derive
// theirs from the constants synth injected the artifacts at, which is ground
// truth by construction and lets the scorer be exercised today.

type labelVerdict string

const (
	// labelArtifact is a region the reviewer judged a genuine defect. Detection
	// finding it is a hit; missing it is a miss.
	labelArtifact labelVerdict = "artifact"
	// labelMotion is a region the reviewer judged intentional flight. Detection
	// acting on it is a false positive, the error that matters most here.
	labelMotion labelVerdict = "motion"
	// labelUnsure is reviewed but undecided. Counted, never scored — folding a
	// coin flip into precision would make the number look more certain than the
	// review behind it.
	labelUnsure labelVerdict = "unsure"
)

// labelTolerance absorbs the difference between a time read off a video
// scrubber and a detector boundary derived from the sample grid.
const labelTolerance = 0.1

type label struct {
	start, end float64
	verdict    labelVerdict
	note       string
}

type corpusScore struct {
	hit, missed, falsePositive, unsure, unlabelled int
}

// precision and recall are computed over labelled ground only. An actionable
// event that overlaps no label is reported as unlabelled rather than counted
// against precision: the reviewer labelled what they reviewed, and treating
// everything else as correct or incorrect would both be inventions.
func (s corpusScore) precision() float64 {
	if s.hit+s.falsePositive == 0 {
		return math.NaN()
	}
	return float64(s.hit) / float64(s.hit+s.falsePositive)
}

func (s corpusScore) recall() float64 {
	if s.hit+s.missed == 0 {
		return math.NaN()
	}
	return float64(s.hit) / float64(s.hit+s.missed)
}

func scoreAgainstLabels(events []detect.Event, labels []label) corpusScore {
	var score corpusScore
	acted := actionableEvents(events)
	matched := make([]bool, len(acted))
	for _, item := range labels {
		touched := false
		for index, event := range acted {
			if event.EndSeconds+labelTolerance < item.start || item.end+labelTolerance < event.StartSeconds {
				continue
			}
			touched = true
			matched[index] = true
		}
		switch item.verdict {
		case labelArtifact:
			if touched {
				score.hit++
			} else {
				score.missed++
			}
		case labelMotion:
			if touched {
				score.falsePositive++
			}
		case labelUnsure:
			score.unsure++
		}
	}
	for _, seen := range matched {
		if !seen {
			score.unlabelled++
		}
	}
	return score
}

// parseLabels reads a reviewer's CSV. Blank lines and lines opening with # are
// skipped, so a file can carry who reviewed it and how without a sidecar.
func parseLabels(reader io.Reader) ([]label, error) {
	source := csv.NewReader(reader)
	source.FieldsPerRecord = -1
	source.TrimLeadingSpace = true
	source.Comment = '#'
	rows, err := source.ReadAll()
	if err != nil {
		return nil, err
	}
	var labels []label
	for number, row := range rows {
		if len(row) == 0 || strings.TrimSpace(row[0]) == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(row[0]), "start") {
			continue // header
		}
		if len(row) < 3 {
			return nil, fmt.Errorf("line %d: want start,end,verdict[,note], got %d field(s)", number+1, len(row))
		}
		start, err := strconv.ParseFloat(strings.TrimSpace(row[0]), 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: start: %w", number+1, err)
		}
		end, err := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: end: %w", number+1, err)
		}
		if end <= start {
			return nil, fmt.Errorf("line %d: end %g must be after start %g", number+1, end, start)
		}
		verdict := labelVerdict(strings.ToLower(strings.TrimSpace(row[2])))
		switch verdict {
		case labelArtifact, labelMotion, labelUnsure:
		default:
			return nil, fmt.Errorf("line %d: unknown verdict %q (want artifact, motion or unsure)", number+1, row[2])
		}
		item := label{start: start, end: end, verdict: verdict}
		if len(row) > 3 {
			item.note = strings.TrimSpace(row[3])
		}
		labels = append(labels, item)
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("no labelled ranges")
	}
	sort.Slice(labels, func(a, b int) bool { return labels[a].start < labels[b].start })
	return labels, nil
}

// syntheticLabels is ground truth by construction: synth injected each artifact
// at these times, so the whip-pan is the one region detection must never act on.
func syntheticLabels() []label {
	return []label{
		{start: synth.JitterStart, end: synth.JitterEnd, verdict: labelArtifact, note: "injected jitter burst"},
		{start: synth.ImpactAt, end: synth.ImpactAt + synth.ImpactLength, verdict: labelArtifact, note: "injected impact"},
		{start: synth.WhipPanStart, end: synth.WhipPanEnd, verdict: labelMotion, note: "injected whip-pan, intentional"},
		{start: synth.DropoutAt, end: synth.DropoutAt + 0.02, verdict: labelArtifact, note: "injected dropout"},
	}
}

// TestSyntheticCorpusScores exercises the scorer against labels that cannot be
// wrong, and pins the invariant of DESIGN §9.4 in the terms §13.4 will use: the
// whip-pan must never be acted on, at any profile.
func TestSyntheticCorpusScores(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	labels := syntheticLabels()
	t.Logf("%-14s %5s %7s %6s %7s %10s %8s", "profile", "hit", "missed", "false", "unlab.", "precision", "recall")
	for _, profile := range []string{"conservative", "balanced", "aggressive"} {
		opts := defaultOptions()
		opts.profile = profile
		if err := opts.validateCommon(); err != nil {
			t.Fatal(err)
		}
		result, err := analyze(path, opts, nil)
		if err != nil {
			t.Fatalf("%s: %v", profile, err)
		}
		score := scoreAgainstLabels(result.report.Events, labels)
		t.Logf("%-14s %5d %7d %6d %7d %9.2f %8.2f", profile,
			score.hit, score.missed, score.falsePositive, score.unlabelled,
			score.precision(), score.recall())
		if score.falsePositive > 0 {
			t.Errorf("%s acted on the injected whip-pan: correcting intentional motion "+
				"invents an orientation the aircraft never held", profile)
		}
	}
}

// TestCorpusLabelFiles validates every committed label file and scores the ones
// whose clip is on this machine. Validation runs everywhere, including CI with
// no footage, so a malformed contribution fails before anyone tries to use it.
func TestCorpusLabelFiles(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "corpus", "*.labels.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("no labelled clips yet; see testdata/corpus/README.md")
	}
	for _, path := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".labels.csv"), func(t *testing.T) {
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			labels, err := parseLabels(file)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			artifacts, motions := 0, 0
			for _, item := range labels {
				switch item.verdict {
				case labelArtifact:
					artifacts++
				case labelMotion:
					motions++
				}
			}
			t.Logf("%d labelled range(s): %d artifact, %d motion", len(labels), artifacts, motions)

			clip := filepath.Join("..", "..", "testdata",
				strings.TrimSuffix(filepath.Base(path), ".labels.csv")+".MP4")
			if _, err := os.Stat(clip); err != nil {
				t.Skipf("labels validated; %s is not on this machine", filepath.Base(clip))
			}
			if testing.Short() {
				t.Skip("labels validated; skipping the clip read in short mode")
			}
			t.Logf("%-14s %5s %7s %6s %7s %10s %8s", "profile", "hit", "missed", "false", "unlab.", "precision", "recall")
			for _, profile := range []string{"conservative", "balanced", "aggressive"} {
				opts := defaultOptions()
				opts.profile = profile
				if err := opts.validateCommon(); err != nil {
					t.Fatal(err)
				}
				result, err := analyze(clip, opts, nil)
				if err != nil {
					t.Fatalf("%s: %v", profile, err)
				}
				score := scoreAgainstLabels(result.report.Events, labels)
				t.Logf("%-14s %5d %7d %6d %7d %9.2f %8.2f", profile,
					score.hit, score.missed, score.falsePositive, score.unlabelled,
					score.precision(), score.recall())
			}
		})
	}
}

func TestParseLabels(t *testing.T) {
	good := `# reviewed by someone, in Gyroflow, 2026-09-03
start,end,verdict,note
8.0,9.2,artifact,vibration burst

20.0,20.9,motion,deliberate whip
25.0,25.02,unsure,too brief to judge
`
	labels, err := parseLabels(strings.NewReader(good))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(labels) != 3 {
		t.Fatalf("got %d labels, want 3: %+v", len(labels), labels)
	}
	if labels[0].verdict != labelArtifact || labels[0].note != "vibration burst" {
		t.Errorf("first label: %+v", labels[0])
	}

	for name, text := range map[string]string{
		"unknown verdict": "1.0,2.0,probably\n",
		"reversed range":  "2.0,1.0,artifact\n",
		"missing field":   "1.0,2.0\n",
		"unparseable":     "one,2.0,artifact\n",
		"empty":           "# nothing here\n",
	} {
		if _, err := parseLabels(strings.NewReader(text)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestScoreAgainstLabels(t *testing.T) {
	events := []detect.Event{
		{StartSeconds: 8.0, EndSeconds: 9.2, Action: detect.ActionSmooth},
		{StartSeconds: 20.1, EndSeconds: 20.5, Action: detect.ActionSmooth},
		{StartSeconds: 40.0, EndSeconds: 40.2, Action: detect.ActionSmooth},
		{StartSeconds: 50.0, EndSeconds: 50.2, Action: detect.ActionNone},
	}
	labels := []label{
		{start: 8.0, end: 9.2, verdict: labelArtifact},
		{start: 20.0, end: 20.9, verdict: labelMotion},
		{start: 30.0, end: 30.5, verdict: labelArtifact},
		{start: 50.0, end: 50.2, verdict: labelUnsure},
	}
	got := scoreAgainstLabels(events, labels)
	want := corpusScore{hit: 1, missed: 1, falsePositive: 1, unsure: 1, unlabelled: 1}
	if got != want {
		t.Errorf("score = %+v, want %+v", got, want)
	}
	if got.precision() != 0.5 || got.recall() != 0.5 {
		t.Errorf("precision %.2f recall %.2f, want 0.50 / 0.50", got.precision(), got.recall())
	}
}
