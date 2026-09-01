// Package patch applies size-preserving byte writes to a video and records
// enough to undo them exactly.
//
// The patch is small relative to the video — a few hundred thousand four-byte
// writes against a file that may be tens of gigabytes — so duplicating the file
// to enable undo is the wrong trade. A sidecar journal recording (offset, old
// bytes, new bytes) restores bit-exact original state in about a second.
//
// Measured on a real 6.5 GB clip with 98 detected events: 335,761 writes, a
// 28 MB journal, 11 s to patch and 1.3 s to revert. That is 0.4% of the file
// size, against 100% for a full copy.
package patch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/steamvogue/djgyrofix/internal/detect"
)

// JournalVersion is the on-disk format version.
const JournalVersion = 1

// JournalSuffix is appended to the video path to name its journal.
const JournalSuffix = ".gyrofix.json"

// Write is a single four-byte overwrite and the bytes it replaced.
type Write struct {
	Offset int64  `json:"off"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

// SourceInfo identifies the file the journal belongs to.
type SourceInfo struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	MTime string `json:"mtime"`
}

// TrackInfo records what was patched.
type TrackInfo struct {
	Variant   string `json:"variant"`
	Timescale uint32 `json:"timescale"`
	Samples   int    `json:"samples"`
}

// Journal is the sidecar undo record.
type Journal struct {
	Version int    `json:"version"`
	Tool    string `json:"tool"`
	Created string `json:"created"`

	Source SourceInfo `json:"source"`
	Track  TrackInfo  `json:"track"`

	// MetadataDigest covers the djmd sample bytes only, before patching. It
	// catches a file that changed underneath the journal.
	MetadataDigest string `json:"metadata_digest"`

	Params map[string]any `json:"params"`
	Events []detect.Event `json:"events"`
	Writes []Write        `json:"writes"`
}

// JournalPath is the sidecar path for a video.
func JournalPath(videoPath string) string { return videoPath + JournalSuffix }

// EncodeBytes renders four bytes as lowercase hex.
func EncodeBytes(value [4]byte) string { return hex.EncodeToString(value[:]) }

// DecodeBytes parses the hex form written by EncodeBytes.
func DecodeBytes(value string) ([4]byte, error) {
	var out [4]byte
	raw, err := hex.DecodeString(value)
	if err != nil {
		return out, fmt.Errorf("invalid hex %q: %w", value, err)
	}
	if len(raw) != 4 {
		return out, fmt.Errorf("expected 4 bytes, got %d in %q", len(raw), value)
	}
	copy(out[:], raw)
	return out, nil
}

// LoadJournal reads and validates a sidecar journal.
func LoadJournal(path string) (*Journal, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var journal Journal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if journal.Version != JournalVersion {
		return nil, fmt.Errorf("%s: journal version %d is not supported by this build (expected %d)",
			path, journal.Version, JournalVersion)
	}
	return &journal, nil
}

// SaveJournal writes the journal durably before the video is touched: to a
// temp file in the same directory, fsynced, then renamed into place. If the
// process dies mid-patch, the journal already on disk is what makes the file
// repairable.
func SaveJournal(path string, journal *Journal) error {
	raw, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.partial")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	// A no-op once the rename below succeeds, and best-effort if it did not:
	// a leftover temp file is untidy, but failing the save over it would be
	// worse than the mess.
	defer func() { _ = os.Remove(tempName) }()
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	return syncDir(directory)
}

// syncDir flushes the directory entry so the rename itself survives a crash.
// Not all platforms permit opening a directory; failing that is not fatal.
func syncDir(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return nil
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return nil
	}
	return nil
}

// SampleSpan is one metadata sample's location, used to digest the track.
type SampleSpan struct {
	Offset int64
	Size   int64
}

// Override replaces four bytes at an absolute file offset before hashing.
type Override struct {
	Offset int64
	Value  [4]byte
}

// MetadataDigest hashes the metadata sample bytes in sample order. Only the
// djmd payloads are covered, so an unrelated container rewrite does not
// invalidate a journal, while any change to the data being patched does.
//
// Overrides must be sorted by offset. They substitute bytes before hashing,
// which is how a patched file is checked against its pre-patch digest. The
// sorted requirement keeps this linear: a journal can hold tens of thousands of
// writes and a track tens of thousands of samples, and pairing them naively
// would be quadratic.
func MetadataDigest(reader io.ReaderAt, spans []SampleSpan, overrides []Override) (string, error) {
	hasher := sha256.New()
	var buffer []byte
	cursor := 0
	for _, span := range spans {
		if int64(cap(buffer)) < span.Size {
			buffer = make([]byte, span.Size)
		}
		chunk := buffer[:span.Size]
		if _, err := io.ReadFull(io.NewSectionReader(reader, span.Offset, span.Size), chunk); err != nil {
			return "", fmt.Errorf("digest: %w", err)
		}
		for cursor < len(overrides) && overrides[cursor].Offset < span.Offset {
			cursor++
		}
		for position := cursor; position < len(overrides) && overrides[position].Offset+4 <= span.Offset+span.Size; position++ {
			copy(chunk[overrides[position].Offset-span.Offset:], overrides[position].Value[:])
		}
		hasher.Write(chunk)
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

// NewJournal builds a journal header for a source file.
func NewJournal(tool string, info os.FileInfo, name string) *Journal {
	return &Journal{
		Version: JournalVersion,
		Tool:    tool,
		Created: time.Now().UTC().Format(time.RFC3339),
		Source: SourceInfo{
			Name:  name,
			Size:  info.Size(),
			MTime: info.ModTime().UTC().Format(time.RFC3339Nano),
		},
		Params: map[string]any{},
		Events: []detect.Event{},
		Writes: []Write{},
	}
}
