// Package mp4 walks the ISO base media file format far enough to rebuild the
// per-sample byte offsets and decode timestamps of a timed-metadata track.
//
// Nothing here ever writes. The box walk exists only so the patcher knows which
// byte ranges belong to djmd sample payloads (invariant I2).
package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Error reports a malformed or unsupported MP4 structure.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errorf(format string, args ...any) error {
	return &Error{msg: fmt.Sprintf(format, args...)}
}

// Box is one ISO-BMFF box located in the file.
type Box struct {
	Type       string
	Start      int64
	Size       int64
	HeaderSize int64
}

// DataStart is the first byte of the box payload.
func (b Box) DataStart() int64 { return b.Start + b.HeaderSize }

// End is the first byte past the box.
func (b Box) End() int64 { return b.Start + b.Size }

// PayloadSize is the box size excluding its header.
func (b Box) PayloadSize() int64 { return b.Size - b.HeaderSize }

func readExact(reader io.ReaderAt, offset int64, size int64) ([]byte, error) {
	if size < 0 {
		return nil, errorf("negative read length %d at %d", size, offset)
	}
	if size > maxTableBytes {
		return nil, errorf("refusing to read %d bytes at %d: table exceeds %d byte cap", size, offset, maxTableBytes)
	}
	buffer := make([]byte, size)
	if _, err := io.ReadFull(io.NewSectionReader(reader, offset, size), buffer); err != nil {
		return nil, errorf("unexpected end of file at %d: %v", offset, err)
	}
	return buffer, nil
}

// maxTableBytes caps any single sample-table allocation. Box sizes are
// attacker-controllable; without this a crafted stsz header would ask for a
// multi-gigabyte allocation before any bounds check could reject it.
const maxTableBytes = 512 << 20

// Boxes lists the boxes between start and end, validating that each one fits
// inside its parent.
func Boxes(reader io.ReaderAt, start, end int64) ([]Box, error) {
	var boxes []Box
	position := start
	for position+8 <= end {
		header := make([]byte, 8)
		if _, err := reader.ReadAt(header, position); err != nil {
			// A short read at the tail is a truncated box, which the reference
			// treats as the end of the list rather than an error. A real I/O
			// failure is not the same thing and must not be reported as a
			// successfully parsed file — that would hand back a short box list
			// and let the caller patch against incomplete offsets.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return nil, errorf("reading box header at %d: %v", position, err)
		}
		size := int64(binary.BigEndian.Uint32(header[0:4]))
		boxType := decodeType(header[4:8])
		headerSize := int64(8)
		switch size {
		case 1:
			large := make([]byte, 8)
			if _, err := reader.ReadAt(large, position+8); err != nil {
				return nil, errorf("truncated extended box header at %d", position)
			}
			size = int64(binary.BigEndian.Uint64(large))
			headerSize = 16
		case 0:
			size = end - position
		}
		if size < headerSize || position+size > end || size < 0 {
			return nil, errorf("invalid MP4 box %q at %d: size=%d", boxType, position, size)
		}
		boxes = append(boxes, Box{Type: boxType, Start: position, Size: size, HeaderSize: headerSize})
		position += size
	}
	return boxes, nil
}

func findChild(reader io.ReaderAt, parent Box, typeName string) (Box, bool, error) {
	children, err := Boxes(reader, parent.DataStart(), parent.End())
	if err != nil {
		return Box{}, false, err
	}
	for _, child := range children {
		if child.Type == typeName {
			return child, true, nil
		}
	}
	return Box{}, false, nil
}

// decodeType renders a four-character code the way latin-1 decoding does in the
// reference, so non-ASCII box types stay printable instead of failing.
func decodeType(raw []byte) string {
	runes := make([]rune, len(raw))
	for index, value := range raw {
		runes[index] = rune(value)
	}
	return string(runes)
}
