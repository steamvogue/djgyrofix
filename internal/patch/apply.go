package patch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

// ErrSizeChanged reports a violation of invariant I1.
var ErrSizeChanged = errors.New("file size changed during patching")

// Sort orders writes by file offset, which keeps the write pass sequential and
// makes the digest override walk linear.
func Sort(writes []Write) {
	sort.Slice(writes, func(a, b int) bool { return writes[a].Offset < writes[b].Offset })
}

// Overrides converts journal writes into digest overrides. Selecting old
// reproduces the pre-patch bytes; selecting new reproduces the post-patch ones.
func Overrides(writes []Write, useOld bool) ([]Override, error) {
	overrides := make([]Override, 0, len(writes))
	for _, write := range writes {
		text := write.New
		if useOld {
			text = write.Old
		}
		value, err := DecodeBytes(text)
		if err != nil {
			return nil, err
		}
		overrides = append(overrides, Override{Offset: write.Offset, Value: value})
	}
	sort.Slice(overrides, func(a, b int) bool { return overrides[a].Offset < overrides[b].Offset })
	return overrides, nil
}

// Apply overwrites the recorded byte ranges in place and verifies that the file
// size is unchanged.
//
// Every write is exactly four bytes at an offset the protobuf scanner found, so
// nothing outside a djmd sample payload can be touched (invariants I2 and I3).
// The size check afterwards is the last line of defence for I1.
func Apply(path string, writes []Write, expectedSize int64) (int, error) {
	if len(writes) == 0 {
		return 0, nil
	}
	ordered := append([]Write(nil), writes...)
	Sort(ordered)

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if expectedSize > 0 && info.Size() != expectedSize {
		return 0, fmt.Errorf("%w: %s is %d bytes, journal expects %d", ErrSizeChanged, path, info.Size(), expectedSize)
	}

	written := 0
	for _, write := range ordered {
		value, err := DecodeBytes(write.New)
		if err != nil {
			return written, err
		}
		if write.Offset < 0 || write.Offset+4 > info.Size() {
			return written, fmt.Errorf("write at %d is outside %s", write.Offset, path)
		}
		if _, err := file.WriteAt(value[:], write.Offset); err != nil {
			return written, err
		}
		written++
	}
	if err := file.Sync(); err != nil {
		return written, err
	}
	after, err := file.Stat()
	if err != nil {
		return written, err
	}
	if after.Size() != info.Size() {
		return written, fmt.Errorf("%w: %s is now %d bytes, was %d", ErrSizeChanged, path, after.Size(), info.Size())
	}
	return written, nil
}

// Revert restores the original bytes recorded in a journal.
func Revert(path string, journal *Journal) (int, error) {
	inverse := make([]Write, len(journal.Writes))
	for index, write := range journal.Writes {
		inverse[index] = Write{Offset: write.Offset, Old: write.New, New: write.Old}
	}
	return Apply(path, inverse, journal.Source.Size)
}

// VerifyReport is the outcome of checking a patched file against its journal.
type VerifyReport struct {
	SizeOK          bool
	BytesMatched    int
	BytesMismatched int
	// FirstMismatch is the offset of the first byte range that does not hold
	// the value the journal says it should.
	FirstMismatch  int64
	DigestOK       bool
	DigestExpected string
	DigestActual   string
}

// OK reports whether the file is exactly what the journal describes.
func (r VerifyReport) OK() bool {
	return r.SizeOK && r.BytesMismatched == 0 && r.DigestOK
}

// Verify checks a patched file against its journal: the size is unchanged,
// every patched offset holds the new value, and reversing the writes in memory
// reproduces the pre-patch metadata digest.
//
// The digest check is what catches a partially applied patch — an interrupted
// write leaves some offsets new and some old, and the byte check alone would
// only say which, not whether the rest of the track is intact.
func Verify(path string, journal *Journal, spans []SampleSpan) (VerifyReport, error) {
	report := VerifyReport{FirstMismatch: -1, DigestExpected: journal.MetadataDigest}
	file, err := os.Open(path)
	if err != nil {
		return report, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return report, err
	}
	report.SizeOK = info.Size() == journal.Source.Size

	buffer := make([]byte, 4)
	for _, write := range journal.Writes {
		expected, err := DecodeBytes(write.New)
		if err != nil {
			return report, err
		}
		if _, err := file.ReadAt(buffer, write.Offset); err != nil {
			return report, fmt.Errorf("reading %d: %w", write.Offset, err)
		}
		if bytes.Equal(buffer, expected[:]) {
			report.BytesMatched++
			continue
		}
		report.BytesMismatched++
		if report.FirstMismatch < 0 {
			report.FirstMismatch = write.Offset
		}
	}

	if journal.MetadataDigest != "" && len(spans) > 0 {
		overrides, err := Overrides(journal.Writes, true)
		if err != nil {
			return report, err
		}
		report.DigestActual, err = MetadataDigest(file, spans, overrides)
		if err != nil {
			return report, err
		}
		report.DigestOK = report.DigestActual == journal.MetadataDigest
	} else {
		report.DigestOK = true
	}
	return report, nil
}

// CopyFile duplicates a file, preferring a filesystem-level clone where the
// platform offers one. On a copy-on-write filesystem this costs no space and no
// time, which is what makes a full backup a reasonable option at all.
func CopyFile(source, destination string) error {
	if err := cloneFile(source, destination); err == nil {
		return nil
	} else if !errors.Is(err, errCloneUnsupported) {
		return err
	}
	return copyFileFallback(source, destination)
}

func copyFileFallback(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	// Cleanup after a failed copy is best-effort: the copy error is what the
	// caller needs, and a removal that also fails must not mask it.
	fail := func(err error) error {
		output.Close()
		_ = os.Remove(destination)
		return err
	}
	// io.Copy between two *os.File uses copy_file_range on Linux, which the
	// kernel turns into a reflink on btrfs and XFS.
	if _, err := io.Copy(output, input); err != nil {
		return fail(err)
	}
	if err := output.Sync(); err != nil {
		return fail(err)
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}

var errCloneUnsupported = errors.New("filesystem clone not supported on this platform")
