package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/djiproto"
	"github.com/steamvogue/djgyrofix/internal/patch"
	"github.com/steamvogue/djgyrofix/internal/quat"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

// writeFixture builds a synthetic clip on disk and returns its path.
func writeFixture(t *testing.T, defect synth.Defect, options ...func(*synth.Options)) string {
	t.Helper()
	const rate, perSample, seconds = 200.0, 4, 30.0
	track := synth.Attitude(synth.AttitudeOptions{
		Defect: defect, Rate: rate, Seconds: seconds, Seed: 20260901,
	})
	build := synth.Options{
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
	}
	for _, option := range options {
		option(&build)
	}
	built, err := synth.Build(build)
	if err != nil {
		t.Fatalf("synth.Build: %v", err)
	}
	path := filepath.Join(t.TempDir(), "DJI_0042.MP4")
	if err := os.WriteFile(path, built.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func defaultOptions() *options {
	return &options{
		profile:     "balanced",
		sensitivity: 1.0,
		strength:    1.0,
		backup:      "journal",
		maxAffected: 0.15,
		variant:     "auto",
		jobs:        1,
		format:      "text",
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestFixThenRevertIsBitIdentical is the round-trip property of plan §9.3.
func TestFixThenRevertIsBitIdentical(t *testing.T) {
	for _, defect := range []synth.Defect{synth.DefectJitter, synth.DefectImpact, synth.DefectDropout, synth.DefectMixed} {
		t.Run(string(defect), func(t *testing.T) {
			path := writeFixture(t, defect)
			before := readFile(t, path)

			opts := defaultOptions()
			opts.apply = true
			result, err := fixOne(path, opts, nil)
			if err != nil {
				t.Fatalf("fix: %v", err)
			}
			if !result.Applied {
				t.Fatalf("nothing was applied for defect %s", defect)
			}
			after := readFile(t, path)
			if len(after) != len(before) {
				t.Fatalf("invariant I1 violated: %d bytes, was %d", len(after), len(before))
			}
			if bytes.Equal(after, before) {
				t.Fatal("the patch changed nothing")
			}

			journal, err := patch.LoadJournal(patch.JournalPath(path))
			if err != nil {
				t.Fatal(err)
			}
			if err := revertOne(path, false, false); err != nil {
				t.Fatalf("revert: %v", err)
			}
			restored := readFile(t, path)
			if !bytes.Equal(restored, before) {
				t.Error("invariant I6 violated: revert did not restore the original bytes")
			}
			if _, err := os.Stat(patch.JournalPath(path)); !os.IsNotExist(err) {
				t.Error("revert left the journal behind")
			}
			if len(journal.Writes) == 0 {
				t.Error("the journal recorded no writes")
			}
		})
	}
}

// TestOnlyMetadataSampleBytesAreTouched is invariant I2: every changed byte
// must lie inside a djmd sample payload and be one of the four-byte ranges the
// journal recorded. Nothing in moov, nothing in a video chunk.
func TestOnlyMetadataSampleBytesAreTouched(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	before := readFile(t, path)

	spans, err := metadataSpans(path)
	if err != nil {
		t.Fatal(err)
	}

	opts := defaultOptions()
	opts.apply = true
	if _, err := fixOne(path, opts, nil); err != nil {
		t.Fatalf("fix: %v", err)
	}
	after := readFile(t, path)

	journal, err := patch.LoadJournal(patch.JournalPath(path))
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[int64]bool{}
	for _, write := range journal.Writes {
		if len(write.New) != 8 || len(write.Old) != 8 {
			t.Fatalf("invariant I3 violated: a write is not four bytes: %+v", write)
		}
		for offset := write.Offset; offset < write.Offset+4; offset++ {
			recorded[offset] = true
		}
	}

	inMetadata := func(offset int64) bool {
		for _, span := range spans {
			if offset >= span.Offset && offset < span.Offset+span.Size {
				return true
			}
		}
		return false
	}
	changed := 0
	for offset := range before {
		if before[offset] == after[offset] {
			continue
		}
		changed++
		position := int64(offset)
		if !recorded[position] {
			t.Fatalf("byte %d changed but is not in the journal", position)
		}
		if !inMetadata(position) {
			t.Fatalf("invariant I2 violated: byte %d is outside every djmd sample", position)
		}
	}
	if changed == 0 {
		t.Fatal("nothing changed, so the invariant is vacuous")
	}
	t.Logf("%d bytes changed across %d recorded writes", changed, len(journal.Writes))
}

// TestCleanClipWritesNothing is the no-op safety property: a file with nothing
// wrong must not be modified and must not gain a journal, which would otherwise
// make it look patched to the idempotency guard.
func TestCleanClipWritesNothing(t *testing.T) {
	path := writeFixture(t, synth.DefectNone)
	before := readFile(t, path)

	opts := defaultOptions()
	opts.apply = true
	result, err := fixOne(path, opts, nil)
	if err != nil {
		t.Fatalf("fix: %v", err)
	}
	if result.Writes != 0 {
		t.Errorf("a clean clip produced %d writes", result.Writes)
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("a clean clip was modified")
	}
	if _, err := os.Stat(patch.JournalPath(path)); !os.IsNotExist(err) {
		t.Error("a clean clip gained a journal")
	}
}

func TestWhipPanIsNotPatched(t *testing.T) {
	path := writeFixture(t, synth.DefectWhipPan)
	before := readFile(t, path)

	opts := defaultOptions()
	opts.apply = true
	if _, err := fixOne(path, opts, nil); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("a whip-pan was smoothed; intentional motion must be left alone")
	}
}

func TestDryRunIsTheDefaultAndWritesNothing(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	before := readFile(t, path)

	opts := defaultOptions()
	result, err := fixOne(path, opts, nil)
	if err != nil {
		t.Fatalf("fix: %v", err)
	}
	if !result.DryRun || result.Applied {
		t.Error("fix without --apply reported itself as applied")
	}
	if result.Writes == 0 {
		t.Error("the dry run planned no writes, so it proves nothing")
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("a dry run modified the file")
	}
	if _, err := os.Stat(patch.JournalPath(path)); !os.IsNotExist(err) {
		t.Error("a dry run wrote a journal")
	}
}

func TestIdempotencyGuardRefusesASecondPatch(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	opts := defaultOptions()
	opts.apply = true
	if _, err := fixOne(path, opts, nil); err != nil {
		t.Fatalf("first fix: %v", err)
	}
	patched := readFile(t, path)

	if _, err := fixOne(path, opts, nil); err == nil {
		t.Fatal("a second patch was accepted without --force")
	}
	if !bytes.Equal(readFile(t, path), patched) {
		t.Error("the refused second patch still modified the file")
	}

	// --force reverts first, then re-applies, so the result must match the
	// first patch exactly rather than compounding on top of it.
	opts.force = true
	if _, err := fixOne(path, opts, nil); err != nil {
		t.Fatalf("forced re-fix: %v", err)
	}
	if !bytes.Equal(readFile(t, path), patched) {
		t.Error("--force compounded the correction instead of reverting first")
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	opts := defaultOptions()
	opts.apply = true
	if _, err := fixOne(path, opts, nil); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if err := verifyOne(path); err != nil {
		t.Fatalf("a freshly patched file failed verification: %v", err)
	}

	journal, err := patch.LoadJournal(patch.JournalPath(path))
	if err != nil {
		t.Fatal(err)
	}
	// Undo one write behind the journal's back: the signature of an
	// interrupted patch.
	original, err := patch.DecodeBytes(journal.Writes[0].Old)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteAt(original[:], journal.Writes[0].Offset); err != nil {
		t.Fatal(err)
	}
	handle.Close()

	if err := verifyOne(path); err == nil {
		t.Error("verify accepted a half-reverted file")
	}
	// Revert must refuse without --force, then succeed with it.
	if err := revertOne(path, true, false); err == nil {
		t.Error("revert accepted a file that no longer matches its journal")
	}
	if err := revertOne(path, false, true); err != nil {
		t.Errorf("forced revert failed: %v", err)
	}
}

func TestMaxAffectedRefusesBlanketSmoothing(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	opts := defaultOptions()
	opts.apply = true
	opts.maxAffected = 0.001
	if _, err := fixOne(path, opts, nil); err == nil {
		t.Fatal("--max-affected did not refuse a clip over the limit")
	}
	if _, err := os.Stat(patch.JournalPath(path)); !os.IsNotExist(err) {
		t.Error("the refused run still wrote a journal")
	}

	opts.force = true
	result, err := fixOne(path, opts, nil)
	if err != nil {
		t.Fatalf("--force did not override --max-affected: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("the override was applied without a warning")
	}
}

func TestOutLeavesTheOriginalUntouched(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	before := readFile(t, path)
	out := filepath.Join(filepath.Dir(path), "patched.MP4")

	opts := defaultOptions()
	opts.apply = true
	opts.out = out
	result, err := fixOne(path, opts, nil)
	if err != nil {
		t.Fatalf("fix: %v", err)
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("--out modified the source file")
	}
	patched := readFile(t, out)
	if len(patched) != len(before) {
		t.Errorf("invariant I1 violated in the copy: %d bytes, want %d", len(patched), len(before))
	}
	if bytes.Equal(patched, before) {
		t.Error("the copy was not patched")
	}
	if result.JournalPath != patch.JournalPath(out) {
		t.Errorf("journal went to %s, want %s", result.JournalPath, patch.JournalPath(out))
	}
}

func TestBackupCopyKeepsAnOriginal(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	before := readFile(t, path)

	opts := defaultOptions()
	opts.apply = true
	opts.backup = "copy"
	result, err := fixOne(path, opts, nil)
	if err != nil {
		t.Fatalf("fix: %v", err)
	}
	if result.BackupPath == "" {
		t.Fatal("no backup was recorded")
	}
	if !bytes.Equal(readFile(t, result.BackupPath), before) {
		t.Error("the backup does not match the original")
	}
}

func TestVariantOverrideIsHonouredAndReported(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	opts := defaultOptions()
	opts.variant = "wa530"
	// The fixture is wm169, so the wa530 path finds nothing at all — which is
	// exactly what a wrong --variant should look like: an empty read, not
	// silently patched garbage.
	if _, err := analyze(path, opts, nil); err == nil {
		t.Error("forcing the wrong variant was accepted")
	}

	opts.variant = "wm169"
	result, err := analyze(path, opts, nil)
	if err != nil {
		t.Fatalf("forcing the right variant failed: %v", err)
	}
	if !result.report.VariantOverride || result.report.VariantDetected != string(djiproto.VariantWM169) {
		t.Errorf("override not reported: %+v", result.report)
	}
}

func TestJournalRoundTripsThroughJSON(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	opts := defaultOptions()
	opts.apply = true
	if _, err := fixOne(path, opts, nil); err != nil {
		t.Fatalf("fix: %v", err)
	}
	raw := readFile(t, patch.JournalPath(path))

	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("the journal is not valid JSON: %v", err)
	}
	for _, key := range []string{"version", "tool", "created", "source", "track", "metadata_digest", "params", "events", "writes"} {
		if _, ok := generic[key]; !ok {
			t.Errorf("the journal is missing %q", key)
		}
	}
	journal, err := patch.LoadJournal(patch.JournalPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if journal.Track.Variant != string(djiproto.VariantWM169) {
		t.Errorf("journal variant = %q", journal.Track.Variant)
	}
	if journal.Source.Size != int64(len(readFile(t, path))) {
		t.Errorf("journal records size %d, file is %d", journal.Source.Size, len(readFile(t, path)))
	}
}

// TestJournalVersionIsChecked guards against a future format silently
// mis-reverting a file written by an older build.
func TestJournalVersionIsChecked(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "future.gyrofix.json")
	if err := os.WriteFile(path, []byte(`{"version":999,"writes":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := patch.LoadJournal(path); err == nil {
		t.Error("a journal from an unsupported version was accepted")
	}
}

func TestStrengthZeroChangesNothing(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed)
	before := readFile(t, path)
	opts := defaultOptions()
	opts.apply = true
	opts.strength = 0
	if _, err := fixOne(path, opts, nil); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("--strength 0 still modified the file")
	}
}

func TestCo64AndChunkedFilesRoundTrip(t *testing.T) {
	path := writeFixture(t, synth.DefectMixed, func(build *synth.Options) {
		build.Use64BitOffsets = true
		build.SamplesPerChunk = 7
	})
	before := readFile(t, path)
	opts := defaultOptions()
	opts.apply = true
	if _, err := fixOne(path, opts, nil); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if len(readFile(t, path)) != len(before) {
		t.Error("invariant I1 violated on a co64 file")
	}
	if err := revertOne(path, false, false); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Error("a co64 file did not round-trip")
	}
}
