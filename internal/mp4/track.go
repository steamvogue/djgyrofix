package mp4

import "sort"

// StscEntry maps a run of chunks to a samples-per-chunk count.
type StscEntry struct {
	FirstChunk       uint32
	SamplesPerChunk  uint32
	DescriptionIndex uint32
}

// SttsEntry is a run of samples sharing one decode-time delta.
type SttsEntry struct {
	Count uint32
	Delta uint32
}

// Track holds the sample tables of one MP4 track plus the reconstructed
// per-sample offsets and decode timestamps.
type Track struct {
	ID            uint32
	HandlerType   string
	HandlerName   string
	SampleEntry   string
	Timescale     uint32
	Duration      uint64
	SampleSizes   []int64
	ChunkOffsets  []int64
	Stsc          []StscEntry
	Stts          []SttsEntry
	SampleOffsets []int64
	SampleDTS     []int64
}

// DurationSeconds is the track duration in seconds, or zero when the timescale
// is unset.
func (t *Track) DurationSeconds() float64 {
	if t.Timescale == 0 {
		return 0
	}
	return float64(t.Duration) / float64(t.Timescale)
}

// SampleCount is the number of samples in the track.
func (t *Track) SampleCount() int { return len(t.SampleSizes) }

// SampleTime returns the decode timestamp of a sample in seconds.
func (t *Track) SampleTime(index int) float64 {
	return float64(t.SampleDTS[index]) / float64(t.Timescale)
}

// NextSampleTime returns the decode timestamp of the following sample, or the
// track duration for the last one. The span between the two is what the
// reference subdivides to give each quaternion within a sample a time.
func (t *Track) NextSampleTime(index int) float64 {
	if index+1 < len(t.SampleDTS) {
		return t.SampleTime(index + 1)
	}
	return t.DurationSeconds()
}

// Finalize expands the chunk and timing tables into flat per-sample offset and
// DTS arrays, validating the tables on the way.
func (t *Track) Finalize() error {
	if len(t.SampleSizes) == 0 {
		return errorf("track %d: sample sizes are missing", t.ID)
	}
	if len(t.ChunkOffsets) == 0 || len(t.Stsc) == 0 {
		return errorf("track %d: chunk table is missing", t.ID)
	}
	if len(t.Stts) == 0 {
		return errorf("track %d: timing table is missing", t.ID)
	}
	if t.Timescale == 0 {
		return errorf("track %d: invalid timescale %d", t.ID, t.Timescale)
	}
	if t.Stsc[0].FirstChunk != 1 {
		return errorf("track %d: first stsc entry must start at chunk 1", t.ID)
	}
	previousChunk := uint32(0)
	for _, entry := range t.Stsc {
		if entry.FirstChunk <= previousChunk || entry.SamplesPerChunk == 0 || entry.DescriptionIndex == 0 {
			return errorf("track %d: invalid stsc table", t.ID)
		}
		previousChunk = entry.FirstChunk
	}

	offsets := make([]int64, 0, len(t.SampleSizes))
	sampleIndex := 0
	stscIndex := 0
	for position, chunkOffset := range t.ChunkOffsets {
		chunkIndex := uint32(position + 1)
		if sampleIndex >= len(t.SampleSizes) {
			break
		}
		for stscIndex+1 < len(t.Stsc) && t.Stsc[stscIndex+1].FirstChunk <= chunkIndex {
			stscIndex++
		}
		offset := chunkOffset
		for count := uint32(0); count < t.Stsc[stscIndex].SamplesPerChunk; count++ {
			if sampleIndex >= len(t.SampleSizes) {
				break
			}
			offsets = append(offsets, offset)
			offset += t.SampleSizes[sampleIndex]
			sampleIndex++
		}
	}
	if len(offsets) != len(t.SampleSizes) {
		return errorf("track %d: mapped %d of %d samples", t.ID, len(offsets), len(t.SampleSizes))
	}
	t.SampleOffsets = offsets

	dts := make([]int64, 0, len(t.SampleSizes))
	value := int64(0)
	for _, entry := range t.Stts {
		if entry.Count == 0 {
			return errorf("track %d: invalid stts table", t.ID)
		}
		remaining := len(t.SampleSizes) - len(dts)
		take := int(entry.Count)
		if take > remaining {
			take = remaining
		}
		for index := 0; index < take; index++ {
			dts = append(dts, value+int64(index)*int64(entry.Delta))
		}
		value += int64(entry.Count) * int64(entry.Delta)
		if len(dts) == len(t.SampleSizes) {
			break
		}
	}
	if len(dts) < len(t.SampleSizes) {
		return errorf("track %d: timing table has %d entries for %d samples", t.ID, len(dts), len(t.SampleSizes))
	}
	t.SampleDTS = dts[:len(t.SampleSizes)]
	return nil
}

// SampleRange returns the half-open sample index range covering the given time
// window. It deliberately over-reads one sample on each side, matching the
// reference, so a filter never starts mid-glitch.
func (t *Track) SampleRange(startSeconds, endSeconds float64) (int, int) {
	if len(t.SampleDTS) == 0 || t.Timescale == 0 {
		return 0, 0
	}
	startTick := int64(startSeconds * float64(t.Timescale))
	if startTick < 0 {
		startTick = 0
	}
	endTick := int64(endSeconds * float64(t.Timescale))
	if endTick < startTick {
		endTick = startTick
	}
	first := bisectLeft(t.SampleDTS, startTick) - 1
	if first < 0 {
		first = 0
	}
	last := bisectRight(t.SampleDTS, endTick) + 1
	if last > len(t.SampleDTS) {
		last = len(t.SampleDTS)
	}
	if last < first {
		last = first
	}
	return first, last
}

func bisectLeft(values []int64, target int64) int {
	return sort.Search(len(values), func(index int) bool { return values[index] >= target })
}

func bisectRight(values []int64, target int64) int {
	return sort.Search(len(values), func(index int) bool { return values[index] > target })
}
