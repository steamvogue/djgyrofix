// Package synth builds synthetic DJI-style MP4 files in memory.
//
// Real DJI footage is multi-gigabyte and cannot be committed, but every
// invariant this tool claims — size preservation, offset discovery, round-trip
// revert, parser robustness — needs a file with a real djmd track to assert
// against. These files are structurally identical to the parts of a DJI MP4
// that djgyrofix touches.
package synth

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/steamvogue/djgyrofix/internal/djiproto"
	"github.com/steamvogue/djgyrofix/internal/quat"
)

// Options configures a synthetic file.
type Options struct {
	Variant djiproto.Variant
	// Timescale is the metadata track timescale in ticks per second.
	Timescale uint32
	// SampleCount is the number of djmd samples.
	SampleCount int
	// QuatsPerSample is how many quaternions each sample carries.
	QuatsPerSample int
	// SampleDeltaTicks is the decode-time delta between samples.
	SampleDeltaTicks uint32
	// SamplesPerChunk controls chunk packing; zero means a single chunk.
	SamplesPerChunk uint32
	// Use64BitOffsets emits co64 instead of stco.
	Use64BitOffsets bool
	// WithVideoTrack adds a decoy track so track selection is exercised.
	WithVideoTrack bool
	// Quaternion supplies the orientation for each quaternion slot. The
	// returned value is stored as float32, as DJI does.
	Quaternion func(sampleIndex, subIndex int) quat.Q
	// OmitZeroComponents drops exactly-zero components the way proto3 does,
	// which leaves no byte slot to patch.
	OmitZeroComponents bool
}

// File is a built synthetic MP4 plus the facts a test needs to check against.
type File struct {
	Bytes         []byte
	SampleOffsets []int64
	SampleSizes   []int64
	// QuatOffsets[i][j] holds the four component byte offsets of quaternion j
	// in sample i, with -1 for an omitted component.
	QuatOffsets [][][4]int
	Timescale   uint32
	Duration    uint64
}

// Build assembles the file.
func Build(options Options) (*File, error) {
	if options.Timescale == 0 {
		options.Timescale = 1000
	}
	if options.SampleCount <= 0 {
		options.SampleCount = 200
	}
	if options.QuatsPerSample <= 0 {
		options.QuatsPerSample = 4
	}
	if options.SampleDeltaTicks == 0 {
		options.SampleDeltaTicks = 20
	}
	if options.SamplesPerChunk == 0 {
		options.SamplesPerChunk = uint32(options.SampleCount)
	}
	if options.Variant == "" {
		options.Variant = djiproto.VariantWM169
	}
	if options.Quaternion == nil {
		options.Quaternion = func(sampleIndex, subIndex int) quat.Q {
			angle := 0.01 * float64(sampleIndex*options.QuatsPerSample+subIndex)
			return quat.Q{math.Cos(angle / 2), 0, math.Sin(angle / 2), 0}
		}
	}
	path, ok := options.Variant.Path()
	if !ok {
		return nil, fmt.Errorf("synth: unknown variant %q", options.Variant)
	}

	samples := make([][]byte, options.SampleCount)
	quatOffsets := make([][][4]int, options.SampleCount)
	sizes := make([]int64, options.SampleCount)
	for index := range samples {
		payload, offsets := buildSample(options, path, index)
		samples[index] = payload
		quatOffsets[index] = offsets
		sizes[index] = int64(len(payload))
	}

	// The chunk table names absolute file offsets, so build moov once with
	// placeholders to learn its length, then rebuild it with real offsets.
	chunkCount := (options.SampleCount + int(options.SamplesPerChunk) - 1) / int(options.SamplesPerChunk)
	ftyp := box("ftyp", concat([]byte("isom"), be32(0x200), []byte("isomiso2mp41")))
	moov := buildMoov(options, sizes, make([]int64, chunkCount))
	mdatStart := int64(len(ftyp) + len(moov) + 8)

	chunkOffsets := make([]int64, chunkCount)
	sampleOffsets := make([]int64, options.SampleCount)
	offset := mdatStart
	for index := range samples {
		if index%int(options.SamplesPerChunk) == 0 {
			chunkOffsets[index/int(options.SamplesPerChunk)] = offset
		}
		sampleOffsets[index] = offset
		offset += int64(len(samples[index]))
	}
	moov = buildMoov(options, sizes, chunkOffsets)
	if int64(len(ftyp)+len(moov)+8) != mdatStart {
		return nil, fmt.Errorf("synth: moov size changed between passes")
	}

	file := concat(ftyp, moov, box("mdat", concat(samples...)))

	// Shift the recorded quaternion offsets from sample-relative to absolute.
	for sampleIndex, offsets := range quatOffsets {
		for quatIndex := range offsets {
			for component := range offsets[quatIndex] {
				if offsets[quatIndex][component] >= 0 {
					quatOffsets[sampleIndex][quatIndex][component] += int(sampleOffsets[sampleIndex])
				}
			}
		}
	}
	return &File{
		Bytes:         file,
		SampleOffsets: sampleOffsets,
		SampleSizes:   sizes,
		QuatOffsets:   quatOffsets,
		Timescale:     options.Timescale,
		Duration:      uint64(options.SampleCount) * uint64(options.SampleDeltaTicks),
	}, nil
}

// buildSample nests the quaternion messages under the variant's field path and
// records where each component's four bytes ended up.
func buildSample(options Options, path []int, sampleIndex int) ([]byte, [][4]int) {
	offsets := make([][4]int, options.QuatsPerSample)
	var inner []byte
	for subIndex := 0; subIndex < options.QuatsPerSample; subIndex++ {
		body, componentOffsets := quaternionMessage(options, options.Quaternion(sampleIndex, subIndex))
		key := varint(uint64(path[len(path)-1])<<3 | 2)
		length := varint(uint64(len(body)))
		base := len(inner) + len(key) + len(length)
		for component := range componentOffsets {
			if componentOffsets[component] >= 0 {
				offsets[subIndex][component] = base + componentOffsets[component]
			} else {
				offsets[subIndex][component] = -1
			}
		}
		inner = concat(inner, key, length, body)
	}

	// A variant marker so DetectVariant has something to sniff. Field 15 is
	// outside the quaternion path, so it cannot confuse the scanner.
	marker := lengthDelimited(15, []byte(options.Variant))
	payload := inner
	shift := 0
	for step := len(path) - 2; step >= 0; step-- {
		key := varint(uint64(path[step])<<3 | 2)
		length := varint(uint64(len(payload)))
		shift += len(key) + len(length)
		payload = concat(key, length, payload)
	}
	shift += len(marker)
	for subIndex := range offsets {
		for component := range offsets[subIndex] {
			if offsets[subIndex][component] >= 0 {
				offsets[subIndex][component] += shift
			}
		}
	}
	return concat(marker, payload), offsets
}

func quaternionMessage(options Options, value quat.Q) ([]byte, [4]int) {
	var body []byte
	offsets := [4]int{-1, -1, -1, -1}
	for component := 0; component < 4; component++ {
		stored := float32(value[component])
		if options.OmitZeroComponents && stored == 0 {
			continue
		}
		body = append(body, varint(uint64(component+1)<<3|5)...)
		offsets[component] = len(body)
		raw := make([]byte, 4)
		binary.LittleEndian.PutUint32(raw, math.Float32bits(stored))
		body = append(body, raw...)
	}
	return body, offsets
}

func buildMoov(options Options, sizes []int64, chunkOffsets []int64) []byte {
	duration := uint64(len(sizes)) * uint64(options.SampleDeltaTicks)
	stsz := concat(be32(0), be32(0), be32(uint32(len(sizes))))
	for _, size := range sizes {
		stsz = append(stsz, be32(uint32(size))...)
	}

	var chunkBox []byte
	if options.Use64BitOffsets {
		payload := concat(be32(0), be32(uint32(len(chunkOffsets))))
		for _, offset := range chunkOffsets {
			payload = append(payload, be64(uint64(offset))...)
		}
		chunkBox = box("co64", payload)
	} else {
		payload := concat(be32(0), be32(uint32(len(chunkOffsets))))
		for _, offset := range chunkOffsets {
			payload = append(payload, be32(uint32(offset))...)
		}
		chunkBox = box("stco", payload)
	}

	stbl := box("stbl", concat(
		box("stsd", concat(be32(0), be32(1), box("djmd", nil))),
		box("stts", concat(be32(0), be32(1), be32(uint32(len(sizes))), be32(options.SampleDeltaTicks))),
		box("stsc", concat(be32(0), be32(1), be32(1), be32(options.SamplesPerChunk), be32(1))),
		box("stsz", stsz),
		chunkBox,
	))
	minf := box("minf", concat(box("nmhd", be32(0)), box("dinf", nil), stbl))
	mdhd := box("mdhd", concat(be32(0), be32(0), be32(0), be32(options.Timescale), be32(uint32(duration)), be32(0x55C40000)))
	hdlr := box("hdlr", concat(be32(0), be32(0), []byte("meta"), be32(0), be32(0), be32(0), []byte("DJI meta\x00")))
	tkhd := box("tkhd", concat(be32(0), be32(0), be32(0), be32(1), be32(0), be32(uint32(duration)), make([]byte, 60)))
	trak := box("trak", concat(tkhd, box("mdia", concat(mdhd, hdlr, minf))))

	var children []byte
	if options.WithVideoTrack {
		children = append(children, decoyVideoTrack(options.Timescale, duration)...)
	}
	children = append(children, trak...)
	mvhd := box("mvhd", concat(be32(0), be32(0), be32(0), be32(options.Timescale), be32(uint32(duration)), make([]byte, 80)))
	return box("moov", concat(mvhd, children))
}

// decoyVideoTrack is a well-formed track with no djmd sample entry, so track
// selection has something wrong to reject. It carries a plausible 30 fps sample
// count so the EDL writer's frame-rate estimate has something real to work on.
func decoyVideoTrack(timescale uint32, duration uint64) []byte {
	frames := uint32(duration * 30 / uint64(timescale))
	if frames == 0 {
		frames = 1
	}
	frameDelta := uint32(duration) / frames
	if frameDelta == 0 {
		frameDelta = 1
	}
	stbl := box("stbl", concat(
		box("stsd", concat(be32(0), be32(1), box("avc1", make([]byte, 8)))),
		box("stts", concat(be32(0), be32(1), be32(frames), be32(frameDelta))),
		box("stsc", concat(be32(0), be32(1), be32(1), be32(frames), be32(1))),
		box("stsz", concat(be32(0), be32(4), be32(frames))),
		box("stco", concat(be32(0), be32(1), be32(0))),
	))
	minf := box("minf", concat(box("vmhd", make([]byte, 12)), stbl))
	mdhd := box("mdhd", concat(be32(0), be32(0), be32(0), be32(timescale), be32(uint32(duration)), be32(0x55C40000)))
	hdlr := box("hdlr", concat(be32(0), be32(0), []byte("vide"), be32(0), be32(0), be32(0), []byte("VideoHandler\x00")))
	tkhd := box("tkhd", concat(be32(0), be32(0), be32(0), be32(2), be32(0), be32(uint32(duration)), make([]byte, 60)))
	return box("trak", concat(tkhd, box("mdia", concat(mdhd, hdlr, minf))))
}

func box(name string, payload []byte) []byte {
	out := make([]byte, 0, 8+len(payload))
	out = append(out, be32(uint32(8+len(payload)))...)
	out = append(out, name...)
	return append(out, payload...)
}

func lengthDelimited(fieldNumber int, payload []byte) []byte {
	return concat(varint(uint64(fieldNumber)<<3|2), varint(uint64(len(payload))), payload)
}

func varint(value uint64) []byte {
	var out []byte
	for value >= 0x80 {
		out = append(out, byte(value&0x7F)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}

func be32(value uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, value)
	return out
}

func be64(value uint64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, value)
	return out
}

func concat(parts ...[]byte) []byte {
	total := 0
	for _, part := range parts {
		total += len(part)
	}
	out := make([]byte, 0, total)
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}
