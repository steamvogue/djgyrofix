package patch_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/patch"
)

func tempFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.MP4")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestApplyIsSizePreservingAndExact(t *testing.T) {
	original := []byte("0123456789abcdefghij")
	path := tempFile(t, original)
	writes := []patch.Write{
		{Offset: 4, Old: "34353637", New: "ffffffff"},
		{Offset: 12, Old: "63646566", New: "00112233"},
	}
	written, err := patch.Apply(path, writes, int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	if written != 2 {
		t.Errorf("wrote %d ranges, want 2", written)
	}
	patched := read(t, path)
	if len(patched) != len(original) {
		t.Fatalf("invariant I1 violated: %d bytes, was %d", len(patched), len(original))
	}
	want := append([]byte(nil), original...)
	copy(want[4:], []byte{0xff, 0xff, 0xff, 0xff})
	copy(want[12:], []byte{0x00, 0x11, 0x22, 0x33})
	if !bytes.Equal(patched, want) {
		t.Errorf("patched = %q, want %q", patched, want)
	}
}

func TestApplyRefusesWritesOutsideTheFile(t *testing.T) {
	path := tempFile(t, []byte("0123456789"))
	for name, write := range map[string]patch.Write{
		"past the end": {Offset: 8, Old: "38390000", New: "ffffffff"},
		"negative":     {Offset: -4, Old: "00000000", New: "ffffffff"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := patch.Apply(path, []patch.Write{write}, 10); err == nil {
				t.Error("accepted a write outside the file")
			}
		})
	}
	if read(t, path)[0] != '0' {
		t.Error("a refused write still modified the file")
	}
}

func TestApplyRefusesAResizedFile(t *testing.T) {
	path := tempFile(t, []byte("0123456789"))
	if _, err := patch.Apply(path, []patch.Write{{Offset: 0, Old: "30313233", New: "ffffffff"}}, 99); err == nil {
		t.Error("patched a file whose size does not match the journal")
	}
}

func TestApplyRejectsMalformedHex(t *testing.T) {
	path := tempFile(t, []byte("0123456789"))
	for name, value := range map[string]string{
		"not hex":   "zzzzzzzz",
		"too short": "ffff",
		"too long":  "ffffffffff",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := patch.Apply(path, []patch.Write{{Offset: 0, Old: "30313233", New: value}}, 10); err == nil {
				t.Error("accepted a malformed write value")
			}
		})
	}
}

func TestRevertInvertsApply(t *testing.T) {
	original := []byte("0123456789abcdefghij")
	path := tempFile(t, original)
	journal := &patch.Journal{
		Version: patch.JournalVersion,
		Source:  patch.SourceInfo{Size: int64(len(original))},
		Writes: []patch.Write{
			{Offset: 4, Old: "34353637", New: "ffffffff"},
			{Offset: 12, Old: "63646566", New: "00112233"},
		},
	}
	if _, err := patch.Apply(path, journal.Writes, journal.Source.Size); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(read(t, path), original) {
		t.Fatal("apply changed nothing")
	}
	if _, err := patch.Revert(path, journal); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read(t, path), original) {
		t.Error("revert did not restore the original bytes")
	}
}

func TestMetadataDigestCoversOnlyTheNamedSpans(t *testing.T) {
	content := []byte("AAAABBBBCCCCDDDD")
	spans := []patch.SampleSpan{{Offset: 4, Size: 4}, {Offset: 12, Size: 4}}
	base, err := patch.MetadataDigest(bytes.NewReader(content), spans, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A change outside the spans must not move the digest: an unrelated
	// container rewrite should not invalidate a journal.
	outside := append([]byte(nil), content...)
	copy(outside[0:], "ZZZZ")
	got, err := patch.MetadataDigest(bytes.NewReader(outside), spans, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != base {
		t.Error("the digest changed for a byte outside every metadata sample")
	}

	// A change inside must move it.
	inside := append([]byte(nil), content...)
	copy(inside[4:], "ZZZZ")
	got, err = patch.MetadataDigest(bytes.NewReader(inside), spans, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got == base {
		t.Error("the digest did not change for a byte inside a metadata sample")
	}
}

func TestMetadataDigestOverridesReproduceThePrePatchHash(t *testing.T) {
	original := []byte("AAAABBBBCCCCDDDD")
	spans := []patch.SampleSpan{{Offset: 0, Size: 16}}
	before, err := patch.MetadataDigest(bytes.NewReader(original), spans, nil)
	if err != nil {
		t.Fatal(err)
	}

	patched := append([]byte(nil), original...)
	copy(patched[4:], "ZZZZ")
	writes := []patch.Write{{Offset: 4, Old: "42424242", New: "5a5a5a5a"}}
	overrides, err := patch.Overrides(writes, true)
	if err != nil {
		t.Fatal(err)
	}
	// Reversing the writes in memory must reproduce the original digest. This
	// is how verify proves the untouched remainder of the track is intact.
	got, err := patch.MetadataDigest(bytes.NewReader(patched), spans, overrides)
	if err != nil {
		t.Fatal(err)
	}
	if got != before {
		t.Errorf("reversed digest %s, want %s", got, before)
	}
}

func TestOverridesAreSortedByOffset(t *testing.T) {
	overrides, err := patch.Overrides([]patch.Write{
		{Offset: 40, Old: "00000000", New: "11111111"},
		{Offset: 8, Old: "22222222", New: "33333333"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if overrides[0].Offset != 8 || overrides[1].Offset != 40 {
		t.Errorf("overrides not sorted: %+v", overrides)
	}
}

func TestJournalSaveAndLoadRoundTrip(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "clip.MP4"+patch.JournalSuffix)
	journal := &patch.Journal{
		Version:        patch.JournalVersion,
		Tool:           "djgyrofix test",
		Created:        "2026-09-01T00:00:00Z",
		Source:         patch.SourceInfo{Name: "clip.MP4", Size: 1234},
		Track:          patch.TrackInfo{Variant: "wm169", Timescale: 1000, Samples: 42},
		MetadataDigest: "sha256:abc",
		Params:         map[string]any{"profile": "balanced"},
		Writes:         []patch.Write{{Offset: 8, Old: "00000000", New: "11111111"}},
	}
	if err := patch.SaveJournal(path, journal); err != nil {
		t.Fatal(err)
	}
	// No temp files may survive; a stray .partial would be mistaken for a
	// journal by nothing, but it would litter the user's footage directory.
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("SaveJournal left %d files behind: %v", len(entries), entries)
	}

	loaded, err := patch.LoadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Track.Variant != "wm169" || len(loaded.Writes) != 1 || loaded.Writes[0].Offset != 8 {
		t.Errorf("journal did not round-trip: %+v", loaded)
	}
}

func TestVerifyReportsAPartialPatch(t *testing.T) {
	original := []byte("AAAABBBBCCCCDDDD")
	path := tempFile(t, original)
	spans := []patch.SampleSpan{{Offset: 0, Size: 16}}
	digest, err := patch.MetadataDigest(bytes.NewReader(original), spans, nil)
	if err != nil {
		t.Fatal(err)
	}
	journal := &patch.Journal{
		Version:        patch.JournalVersion,
		Source:         patch.SourceInfo{Size: int64(len(original))},
		MetadataDigest: digest,
		Writes: []patch.Write{
			{Offset: 4, Old: "42424242", New: "5a5a5a5a"},
			{Offset: 8, Old: "43434343", New: "59595959"},
		},
	}
	if _, err := patch.Apply(path, journal.Writes, journal.Source.Size); err != nil {
		t.Fatal(err)
	}
	report, err := patch.Verify(path, journal, spans)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK() {
		t.Fatalf("a fully patched file failed verification: %+v", report)
	}

	// Undo one of the two writes, as an interrupted run would leave it.
	handle, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteAt([]byte("BBBB"), 4); err != nil {
		t.Fatal(err)
	}
	handle.Close()

	report, err = patch.Verify(path, journal, spans)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK() {
		t.Error("verify passed a half-patched file")
	}
	if report.BytesMatched != 1 || report.BytesMismatched != 1 {
		t.Errorf("expected one matched and one mismatched range, got %+v", report)
	}
	if report.FirstMismatch != 4 {
		t.Errorf("first mismatch reported at %d, want 4", report.FirstMismatch)
	}
	// The digest still reverses cleanly, because the remainder of the track is
	// untouched — that is what tells the user this is repairable.
	if !report.DigestOK {
		t.Error("the digest check failed on a file whose other bytes are intact")
	}
}

func TestVerifyDetectsAResizedFile(t *testing.T) {
	path := tempFile(t, []byte("AAAABBBB"))
	journal := &patch.Journal{Version: patch.JournalVersion, Source: patch.SourceInfo{Size: 999}}
	report, err := patch.Verify(path, journal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.SizeOK || report.OK() {
		t.Error("verify accepted a file of the wrong size")
	}
}

func TestCopyFileDuplicatesExactly(t *testing.T) {
	content := bytes.Repeat([]byte("djgyrofix"), 5000)
	source := tempFile(t, content)
	destination := filepath.Join(filepath.Dir(source), "copy.MP4")
	if err := patch.CopyFile(source, destination); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read(t, destination), content) {
		t.Error("the copy does not match the source")
	}
	// Copying onto an existing path must fail rather than clobber it.
	if err := patch.CopyFile(source, destination); err == nil {
		t.Error("CopyFile overwrote an existing file")
	}
}

func TestSortOrdersWritesByOffset(t *testing.T) {
	writes := []patch.Write{{Offset: 100}, {Offset: 4}, {Offset: 40}}
	patch.Sort(writes)
	for index := 1; index < len(writes); index++ {
		if writes[index-1].Offset > writes[index].Offset {
			t.Fatalf("writes not sorted: %+v", writes)
		}
	}
}

func TestEncodeAndDecodeBytes(t *testing.T) {
	value := [4]byte{0x00, 0x11, 0xab, 0xff}
	encoded := patch.EncodeBytes(value)
	if encoded != "0011abff" {
		t.Errorf("EncodeBytes = %q", encoded)
	}
	decoded, err := patch.DecodeBytes(encoded)
	if err != nil || decoded != value {
		t.Errorf("DecodeBytes(%q) = %v, %v", encoded, decoded, err)
	}
}
