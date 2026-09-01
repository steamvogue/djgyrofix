package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/patch"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

// captureStdout runs body with stdout redirected and returns what it printed.
func captureStdout(t *testing.T, body func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = writer

	done := make(chan string, 1)
	go func() {
		var buffer bytes.Buffer
		_, _ = io.Copy(&buffer, reader)
		done <- buffer.String()
	}()

	runErr := body()
	os.Stdout = saved
	writer.Close()
	output := <-done
	reader.Close()
	return output, runErr
}

func TestScanCommandReportsWithoutWriting(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	before := readFile(t, path)

	output, err := captureStdout(t, func() error { return runScan([]string{path}) })
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, want := range []string{"wm169", "jitter", "dropout"} {
		if !strings.Contains(output, want) {
			t.Errorf("scan output is missing %q\n%s", want, output)
		}
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("scan modified the video")
	}
	if _, err := os.Stat(patch.JournalPath(path)); !os.IsNotExist(err) {
		t.Error("scan wrote a journal")
	}
}

func TestScanCommandFormats(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	for _, format := range []string{"json", "csv", "edl", "text"} {
		t.Run(format, func(t *testing.T) {
			output, err := captureStdout(t, func() error {
				return runScan([]string{"--format", format, path})
			})
			if err != nil {
				t.Fatalf("scan --format %s: %v", format, err)
			}
			if strings.TrimSpace(output) == "" {
				t.Errorf("scan --format %s produced nothing", format)
			}
		})
	}
	if _, err := captureStdout(t, func() error {
		return runScan([]string{"--format", "yaml", path})
	}); err == nil {
		t.Error("an unknown --format was accepted")
	}
}

func TestScanCommandNeedsAFile(t *testing.T) {
	if _, err := captureStdout(t, func() error { return runScan(nil) }); err == nil {
		t.Error("scan with no arguments was accepted")
	}
}

func TestScanReportsFailuresWithoutStopping(t *testing.T) {
	good := writeFixture(t, synth.DefectMixed)
	bad := filepath.Join(filepath.Dir(good), "broken.MP4")
	if err := os.WriteFile(bad, []byte("not an MP4 at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := captureStdout(t, func() error { return runScan([]string{good, bad}) })
	if err == nil {
		t.Error("a batch with a broken file reported success")
	}
	// The good file must still have been reported.
	if !strings.Contains(output, filepath.Base(good)) {
		t.Errorf("the readable file was not reported\n%s", output)
	}
}

func TestInfoCommandPrintsTheVariantAndItsFieldPath(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	output, err := captureStdout(t, func() error { return runInfo([]string{"--all-variants", path}) })
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	for _, want := range []string{"variant:", "wm169", "field path: 3.3.2.3", "quaternions:", "rate:", "djmd"} {
		if !strings.Contains(output, want) {
			t.Errorf("info output is missing %q\n%s", want, output)
		}
	}
	// --all-variants must show what the other paths would find, which is how a
	// wrong sniff gets diagnosed.
	if !strings.Contains(output, "wa530") || !strings.Contains(output, "oq101") {
		t.Errorf("--all-variants did not report the other paths\n%s", output)
	}
}

func TestFixCommandRejectsContradictoryFlags(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	cases := map[string][]string{
		"apply and dry-run together": {"--apply", "--dry-run", path},
		"unknown backup mode":        {"--apply", "--backup", "maybe", path},
		"backup none without force":  {"--apply", "--backup", "none", path},
		"out with several inputs":    {"--apply", "--out", "x.MP4", path, path},
		"strength out of range":      {"--strength", "3", path},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := captureStdout(t, func() error { return runFix(args) }); err == nil {
				t.Error("accepted contradictory flags")
			}
		})
	}
}

func TestFixCommandAppliesAndRevertRestores(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	before := readFile(t, path)

	if _, err := captureStdout(t, func() error { return runFix([]string{"--apply", path}) }); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if bytes.Equal(readFile(t, path), before) {
		t.Fatal("fix --apply changed nothing")
	}
	if _, err := captureStdout(t, func() error { return runVerify([]string{path}) }); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := captureStdout(t, func() error { return runRevert([]string{path}) }); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("revert did not restore the original bytes")
	}
}

func TestRevertAndVerifyNeedAJournal(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	if _, err := captureStdout(t, func() error { return runRevert([]string{path}) }); err == nil {
		t.Error("revert accepted a file with no journal")
	}
	if _, err := captureStdout(t, func() error { return runVerify([]string{path}) }); err == nil {
		t.Error("verify accepted a file with no journal")
	}
	for _, run := range map[string]func([]string) error{"revert": runRevert, "verify": runVerify} {
		if _, err := captureStdout(t, func() error { return run(nil) }); err == nil {
			t.Error("a command with no arguments was accepted")
		}
	}
}

func TestRevertKeepJournal(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	if _, err := captureStdout(t, func() error { return runFix([]string{"--apply", path}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return runRevert([]string{"--keep-journal", path}) }); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if _, err := os.Stat(patch.JournalPath(path)); err != nil {
		t.Error("--keep-journal removed the journal anyway")
	}
}

// TestRangesModeSkipsDetection covers the manual escape hatch, which is also
// the golden-parity path: no detection runs and only the named window changes.
func TestRangesModeSkipsDetection(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	opts := defaultOptions()
	opts.ranges = "8.0-9.5"
	intervals, err := parseRanges(opts.ranges)
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyze(path, opts, intervals)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(result.report.Events) != 1 {
		t.Fatalf("got %d events, want exactly the one manual range", len(result.report.Events))
	}
	if result.report.Events[0].Note != "manual range from --ranges" {
		t.Errorf("the event does not identify itself as manual: %+v", result.report.Events[0])
	}
	if result.report.Writes == 0 {
		t.Error("the manual range produced no writes")
	}
	if result.report.RollingBaseline {
		t.Error("manual mode reported a rolling baseline it never computed")
	}
	if result.report.SampleRate <= 0 {
		t.Error("manual mode did not report a sample rate")
	}
	// Manual mode reads only the requested windows, so it reports no
	// whole-track quaternion count rather than paying for a second full pass.
	if result.report.QuaternionCount != 0 {
		t.Errorf("manual mode reported a quaternion count of %d", result.report.QuaternionCount)
	}
}

func TestRangesModeRejectsAWindowPastTheEnd(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	opts := defaultOptions()
	intervals, err := parseRanges("100-200")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := analyze(path, opts, intervals); err == nil {
		t.Error("a range past the end of the track was accepted")
	}
}

// TestOverlappingRangesCollapseToOneWritePerOffset makes revert correct when
// two ranges touch the same sample: the journal must hold the true original
// bytes, not the intermediate value the first range wrote.
func TestOverlappingRangesCollapseToOneWritePerOffset(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	before := readFile(t, path)

	opts := defaultOptions()
	opts.apply = true
	// Two windows close enough that their context padding overlaps.
	intervals, err := parseRanges("8.0-8.6,8.7-9.3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixOne(path, opts, intervals); err != nil {
		t.Fatalf("fix: %v", err)
	}
	journal, err := patch.LoadJournal(patch.JournalPath(path))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, write := range journal.Writes {
		if seen[write.Offset] {
			t.Fatalf("offset %d appears twice in the journal", write.Offset)
		}
		seen[write.Offset] = true
		original, err := patch.DecodeBytes(write.Old)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original[:], before[write.Offset:write.Offset+4]) {
			t.Fatalf("journal records %s as the original at %d, but the file had %x",
				write.Old, write.Offset, before[write.Offset:write.Offset+4])
		}
	}
	if err := revertOne(path, false, false); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("overlapping ranges did not round-trip")
	}
}

func TestJobsProcessesABatchInOrder(t *testing.T) {
	first := writeFixture(t, synth.DefectJitter)
	second := filepath.Join(filepath.Dir(first), "second.MP4")
	if err := os.WriteFile(second, readFile(t, first), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := captureStdout(t, func() error {
		return runScan([]string{"--jobs", "4", "--format", "csv", first, second})
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	firstAt := strings.Index(output, first)
	secondAt := strings.Index(output, second)
	if firstAt < 0 || secondAt < 0 {
		t.Fatalf("both files should appear in the report\n%s", output)
	}
	if firstAt > secondAt {
		t.Error("parallel scanning reordered the report; output must follow argument order")
	}
}
