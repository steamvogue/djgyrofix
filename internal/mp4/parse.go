package mp4

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
)

// ParseTracks reads the moov box and returns every track with its sample tables
// expanded. A fragmented file is rejected outright: its samples live in moof
// boxes that this walker does not index, and guessing would put writes at the
// wrong offsets.
func ParseTracks(reader io.ReaderAt, size int64) ([]*Track, error) {
	top, err := Boxes(reader, 0, size)
	if err != nil {
		return nil, err
	}
	var moov *Box
	for index := range top {
		switch top[index].Type {
		case "moov":
			if moov == nil {
				moov = &top[index]
			}
		case "moof":
			return nil, errorf("fragmented MP4 (moof box at %d) is not supported", top[index].Start)
		}
	}
	if moov == nil {
		return nil, errorf("MP4 moov box was not found")
	}
	if _, found, err := findChild(reader, *moov, "mvex"); err != nil {
		return nil, err
	} else if found {
		return nil, errorf("fragmented MP4 (mvex box in moov) is not supported")
	}

	children, err := Boxes(reader, moov.DataStart(), moov.End())
	if err != nil {
		return nil, err
	}
	var tracks []*Track
	for _, child := range children {
		if child.Type != "trak" {
			continue
		}
		track, err := parseTrack(reader, child)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, track)
	}
	if len(tracks) == 0 {
		return nil, errorf("MP4 track was not found")
	}
	for _, track := range tracks {
		for index, offset := range track.SampleOffsets {
			sampleSize := track.SampleSizes[index]
			if offset < 0 || sampleSize < 0 || offset+sampleSize > size {
				return nil, errorf("track %d: sample points outside the file boundary", track.ID)
			}
		}
	}
	return tracks, nil
}

// ParseFile opens path and parses its tracks.
func ParseFile(path string) ([]*Track, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return ParseTracks(file, info.Size())
}

func parseTrack(reader io.ReaderAt, trak Box) (*Track, error) {
	track := &Track{}
	tkhd, hasTkhd, err := findChild(reader, trak, "tkhd")
	if err != nil {
		return nil, err
	}
	mdia, hasMdia, err := findChild(reader, trak, "mdia")
	if err != nil {
		return nil, err
	}
	if !hasTkhd || !hasMdia {
		return nil, errorf("track is missing tkhd or mdia")
	}
	if track.ID, err = parseTkhd(reader, tkhd); err != nil {
		return nil, err
	}

	mdhd, hasMdhd, err := findChild(reader, mdia, "mdhd")
	if err != nil {
		return nil, err
	}
	hdlr, hasHdlr, err := findChild(reader, mdia, "hdlr")
	if err != nil {
		return nil, err
	}
	minf, hasMinf, err := findChild(reader, mdia, "minf")
	if err != nil {
		return nil, err
	}
	if !hasMdhd || !hasHdlr || !hasMinf {
		return nil, errorf("track %d: incomplete mdia box", track.ID)
	}
	if track.Timescale, track.Duration, err = parseMdhd(reader, mdhd); err != nil {
		return nil, err
	}
	if track.HandlerType, track.HandlerName, err = parseHdlr(reader, hdlr); err != nil {
		return nil, err
	}

	stbl, hasStbl, err := findChild(reader, minf, "stbl")
	if err != nil {
		return nil, err
	}
	if !hasStbl {
		return nil, errorf("track %d: missing stbl", track.ID)
	}
	tables, err := Boxes(reader, stbl.DataStart(), stbl.End())
	if err != nil {
		return nil, err
	}
	children := map[string]Box{}
	for _, table := range tables {
		if _, seen := children[table.Type]; !seen {
			children[table.Type] = table
		}
	}
	if box, ok := children["stsd"]; ok {
		if track.SampleEntry, err = parseStsd(reader, box); err != nil {
			return nil, err
		}
	}
	if box, ok := children["stts"]; ok {
		if track.Stts, err = parseStts(reader, box); err != nil {
			return nil, err
		}
	}
	if box, ok := children["stsc"]; ok {
		if track.Stsc, err = parseStsc(reader, box); err != nil {
			return nil, err
		}
	}
	if box, ok := children["stsz"]; ok {
		if track.SampleSizes, err = parseStsz(reader, box); err != nil {
			return nil, err
		}
	}
	chunkBox, hasChunks := children["co64"]
	if !hasChunks {
		chunkBox, hasChunks = children["stco"]
	}
	if hasChunks {
		if track.ChunkOffsets, err = parseChunkOffsets(reader, chunkBox); err != nil {
			return nil, err
		}
	}
	if err := track.Finalize(); err != nil {
		return nil, err
	}
	return track, nil
}

func parseTkhd(reader io.ReaderAt, box Box) (uint32, error) {
	data, err := readExact(reader, box.DataStart(), min64(box.PayloadSize(), 32))
	if err != nil {
		return 0, err
	}
	if len(data) < 1 {
		return 0, errorf("invalid tkhd box")
	}
	offset := 12
	required := 16
	if data[0] == 1 {
		offset, required = 20, 24
	}
	if len(data) < required {
		return 0, errorf("invalid tkhd box")
	}
	return binary.BigEndian.Uint32(data[offset:]), nil
}

func parseMdhd(reader io.ReaderAt, box Box) (uint32, uint64, error) {
	data, err := readExact(reader, box.DataStart(), min64(box.PayloadSize(), 40))
	if err != nil {
		return 0, 0, err
	}
	if len(data) < 1 {
		return 0, 0, errorf("invalid mdhd box")
	}
	if data[0] == 1 {
		if len(data) < 32 {
			return 0, 0, errorf("invalid mdhd box")
		}
		return binary.BigEndian.Uint32(data[20:]), binary.BigEndian.Uint64(data[24:]), nil
	}
	if len(data) < 20 {
		return 0, 0, errorf("invalid mdhd box")
	}
	return binary.BigEndian.Uint32(data[12:]), uint64(binary.BigEndian.Uint32(data[16:])), nil
}

func parseHdlr(reader io.ReaderAt, box Box) (string, string, error) {
	data, err := readExact(reader, box.DataStart(), box.PayloadSize())
	if err != nil {
		return "", "", err
	}
	if len(data) < 12 {
		return "", "", errorf("invalid hdlr box")
	}
	handler := decodeType(data[8:12])
	name := ""
	if len(data) > 24 {
		name = strings.TrimRight(string(data[24:]), "\x00")
	}
	return handler, name, nil
}

func parseStsd(reader io.ReaderAt, box Box) (string, error) {
	data, err := readExact(reader, box.DataStart(), min64(box.PayloadSize(), 24))
	if err != nil {
		return "", err
	}
	if len(data) < 16 || binary.BigEndian.Uint32(data[4:]) == 0 {
		return "", nil
	}
	return decodeType(data[12:16]), nil
}

func parseStts(reader io.ReaderAt, box Box) ([]SttsEntry, error) {
	data, err := readExact(reader, box.DataStart(), box.PayloadSize())
	if err != nil {
		return nil, err
	}
	if len(data) < 8 {
		return nil, errorf("invalid stts box")
	}
	count := int(binary.BigEndian.Uint32(data[4:]))
	if count < 0 || len(data) < 8+count*8 {
		return nil, errorf("invalid stts box")
	}
	entries := make([]SttsEntry, count)
	for index := range entries {
		base := 8 + index*8
		entries[index] = SttsEntry{
			Count: binary.BigEndian.Uint32(data[base:]),
			Delta: binary.BigEndian.Uint32(data[base+4:]),
		}
	}
	return entries, nil
}

func parseStsc(reader io.ReaderAt, box Box) ([]StscEntry, error) {
	data, err := readExact(reader, box.DataStart(), box.PayloadSize())
	if err != nil {
		return nil, err
	}
	if len(data) < 8 {
		return nil, errorf("invalid stsc box")
	}
	count := int(binary.BigEndian.Uint32(data[4:]))
	if count < 0 || len(data) < 8+count*12 {
		return nil, errorf("invalid stsc box")
	}
	entries := make([]StscEntry, count)
	for index := range entries {
		base := 8 + index*12
		entries[index] = StscEntry{
			FirstChunk:       binary.BigEndian.Uint32(data[base:]),
			SamplesPerChunk:  binary.BigEndian.Uint32(data[base+4:]),
			DescriptionIndex: binary.BigEndian.Uint32(data[base+8:]),
		}
	}
	return entries, nil
}

func parseStsz(reader io.ReaderAt, box Box) ([]int64, error) {
	data, err := readExact(reader, box.DataStart(), box.PayloadSize())
	if err != nil {
		return nil, err
	}
	if len(data) < 12 {
		return nil, errorf("invalid stsz box")
	}
	fixedSize := binary.BigEndian.Uint32(data[4:])
	count := int(binary.BigEndian.Uint32(data[8:]))
	if count < 0 {
		return nil, errorf("invalid stsz sample table")
	}
	if fixedSize != 0 {
		if int64(count) > maxTableBytes/4 {
			return nil, errorf("invalid stsz sample table: %d samples", count)
		}
		sizes := make([]int64, count)
		for index := range sizes {
			sizes[index] = int64(fixedSize)
		}
		return sizes, nil
	}
	if len(data) < 12+count*4 {
		return nil, errorf("invalid stsz sample table")
	}
	sizes := make([]int64, count)
	for index := range sizes {
		sizes[index] = int64(binary.BigEndian.Uint32(data[12+index*4:]))
	}
	return sizes, nil
}

func parseChunkOffsets(reader io.ReaderAt, box Box) ([]int64, error) {
	data, err := readExact(reader, box.DataStart(), box.PayloadSize())
	if err != nil {
		return nil, err
	}
	if len(data) < 8 {
		return nil, errorf("invalid %s box", box.Type)
	}
	count := int(binary.BigEndian.Uint32(data[4:]))
	width := 4
	if box.Type == "co64" {
		width = 8
	}
	if count < 0 || len(data) < 8+count*width {
		return nil, errorf("invalid %s box", box.Type)
	}
	offsets := make([]int64, count)
	for index := range offsets {
		base := 8 + index*width
		if width == 8 {
			offsets[index] = int64(binary.BigEndian.Uint64(data[base:]))
		} else {
			offsets[index] = int64(binary.BigEndian.Uint32(data[base:]))
		}
	}
	return offsets, nil
}

// FindDJIMetadataTrack picks the DJI timed-metadata track: a djmd sample entry,
// or a handler name naming DJI or camera metadata. When several match, the one
// with the most samples wins.
func FindDJIMetadataTrack(tracks []*Track) (*Track, error) {
	var best *Track
	for _, track := range tracks {
		name := strings.ToLower(track.HandlerName)
		if track.SampleEntry != "djmd" && !strings.Contains(name, "dji meta") && !strings.Contains(name, "cam meta") {
			continue
		}
		if best == nil || len(track.SampleSizes) > len(best.SampleSizes) {
			best = track
		}
	}
	if best == nil {
		return nil, errorf("no DJI gyro metadata (djmd) track found")
	}
	return best, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
