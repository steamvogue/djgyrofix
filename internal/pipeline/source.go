// Package pipeline reads the DJI metadata track once and turns it into a flat
// series of timestamped quaternions with absolute file offsets.
//
// Both passes work from this one read. A 20-minute clip is tens of MB of
// quaternions; streaming it through a circular buffer would be a self-imposed
// constraint, and the centered-window filters need lookahead anyway.
package pipeline

import (
	"fmt"
	"io"
	"os"

	"github.com/steamvogue/djgyrofix/internal/djiproto"
	"github.com/steamvogue/djgyrofix/internal/mp4"
	"github.com/steamvogue/djgyrofix/internal/quat"
)

// Point is one orientation quaternion at one instant, with the byte offsets of
// its four components in the file. An offset is -1 when proto3 omitted that
// component, leaving no slot to patch.
type Point struct {
	Time        float64
	SampleIndex int
	Values      quat.Q
	Offsets     [4]int64
}

// Source is an opened video with its DJI metadata track located.
type Source struct {
	Path    string
	Size    int64
	Tracks  []*mp4.Track
	Track   *mp4.Track
	Variant djiproto.Variant
	// VariantDetected is what sniffing guessed, even when overridden.
	VariantDetected djiproto.Variant
	file            *os.File
}

// Open parses the container and locates the metadata track. Pass an empty
// override to accept the sniffed variant.
func Open(path string, override djiproto.Variant) (*Source, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	tracks, err := mp4.ParseTracks(file, info.Size())
	if err != nil {
		file.Close()
		return nil, err
	}
	track, err := mp4.FindDJIMetadataTrack(tracks)
	if err != nil {
		file.Close()
		return nil, err
	}
	source := &Source{
		Path:   path,
		Size:   info.Size(),
		Tracks: tracks,
		Track:  track,
		file:   file,
	}
	probe := make([][]byte, 0, 5)
	for index := 0; index < 5 && index < track.SampleCount(); index++ {
		sample, err := source.readSample(index)
		if err != nil {
			file.Close()
			return nil, err
		}
		probe = append(probe, sample)
	}
	source.VariantDetected = djiproto.DetectVariant(probe)
	source.Variant = source.VariantDetected
	if override != "" {
		if _, ok := override.Path(); !ok {
			file.Close()
			return nil, fmt.Errorf("unknown DJI metadata variant: %s", override)
		}
		source.Variant = override
	}
	return source, nil
}

// Close releases the file handle.
func (s *Source) Close() error { return s.file.Close() }

// DurationSeconds is the metadata track duration.
func (s *Source) DurationSeconds() float64 { return s.Track.DurationSeconds() }

func (s *Source) readSample(index int) ([]byte, error) {
	size := s.Track.SampleSizes[index]
	buffer := make([]byte, size)
	if _, err := io.ReadFull(io.NewSectionReader(s.file, s.Track.SampleOffsets[index], size), buffer); err != nil {
		return nil, fmt.Errorf("could not read DJI metadata sample %d: %w", index, err)
	}
	return buffer, nil
}

// ReadRange decodes samples in [first, last) into points.
//
// Samples belonging to one chunk are contiguous on disk, so consecutive ranges
// are read in a single syscall rather than one per sample; a whole-file scan of
// a long flight is tens of thousands of samples and the difference is real.
func (s *Source) ReadRange(first, last int) ([]Point, error) {
	if first < 0 {
		first = 0
	}
	if last > s.Track.SampleCount() {
		last = s.Track.SampleCount()
	}
	if last <= first {
		return nil, nil
	}
	points := make([]Point, 0, (last-first)*4)
	for index := first; index < last; {
		runEnd, runBytes, err := s.readRun(index, last)
		if err != nil {
			return nil, err
		}
		base := s.Track.SampleOffsets[index]
		for sampleIndex := index; sampleIndex < runEnd; sampleIndex++ {
			start := s.Track.SampleOffsets[sampleIndex] - base
			sample := runBytes[start : start+s.Track.SampleSizes[sampleIndex]]
			refs, err := djiproto.Quaternions(sample, s.Variant)
			if err != nil {
				return nil, fmt.Errorf("sample %d: %w", sampleIndex, err)
			}
			sampleTime := s.Track.SampleTime(sampleIndex)
			span := s.Track.NextSampleTime(sampleIndex) - sampleTime
			divisor := float64(len(refs))
			if divisor < 1 {
				divisor = 1
			}
			for subIndex, ref := range refs {
				point := Point{
					Time:        sampleTime + span*float64(subIndex)/divisor,
					SampleIndex: sampleIndex,
					Values:      ref.Values,
				}
				for component, offset := range ref.Offsets {
					if offset < 0 {
						point.Offsets[component] = -1
					} else {
						point.Offsets[component] = s.Track.SampleOffsets[sampleIndex] + int64(offset)
					}
				}
				points = append(points, point)
			}
		}
		index = runEnd
	}
	return points, nil
}

// readRun reads the longest span of contiguous samples starting at first.
func (s *Source) readRun(first, limit int) (int, []byte, error) {
	start := s.Track.SampleOffsets[first]
	end := start + s.Track.SampleSizes[first]
	runEnd := first + 1
	const maxRunBytes = 32 << 20
	for runEnd < limit && s.Track.SampleOffsets[runEnd] == end && end-start < maxRunBytes {
		end += s.Track.SampleSizes[runEnd]
		runEnd++
	}
	buffer := make([]byte, end-start)
	if _, err := io.ReadFull(io.NewSectionReader(s.file, start, end-start), buffer); err != nil {
		return 0, nil, fmt.Errorf("could not read DJI metadata samples %d-%d: %w", first, runEnd-1, err)
	}
	return runEnd, buffer, nil
}

// ReadAll decodes the entire metadata track.
func (s *Source) ReadAll() ([]Point, error) { return s.ReadRange(0, s.Track.SampleCount()) }

// ReadWindow decodes the samples covering a time window, over-reading one
// sample on each side exactly as the reference does so a centered filter never
// starts mid-glitch.
func (s *Source) ReadWindow(startSeconds, endSeconds float64) ([]Point, error) {
	first, last := s.Track.SampleRange(startSeconds, endSeconds)
	return s.ReadRange(first, last)
}

// Times projects the point times, which the filters take as a plain slice.
func Times(points []Point) []float64 {
	times := make([]float64, len(points))
	for index, point := range points {
		times[index] = point.Time
	}
	return times
}

// Values projects the point quaternions.
func Values(points []Point) []quat.Q {
	values := make([]quat.Q, len(points))
	for index, point := range points {
		values[index] = point.Values
	}
	return values
}

// SampleRate estimates the quaternion rate from the median inter-sample
// interval, which drives every filter radius downstream.
func SampleRate(times []float64) (rate float64, interval float64, ok bool) {
	intervals := make([]float64, 0, len(times))
	for index := 0; index+1 < len(times); index++ {
		delta := times[index+1] - times[index]
		if delta > 0 {
			intervals = append(intervals, delta)
		}
	}
	if len(intervals) == 0 {
		return 0, 0, false
	}
	interval = quat.Median(intervals)
	if interval <= 0 {
		return 0, 0, false
	}
	return 1.0 / interval, interval, true
}

// VideoFPS estimates the video frame rate, which the EDL writer needs to turn
// event times into SMPTE timecode. Zero means no video track was found.
func VideoFPS(tracks []*mp4.Track) float64 {
	for _, track := range tracks {
		if track.HandlerType != "vide" {
			continue
		}
		duration := track.DurationSeconds()
		if duration > 0 && track.SampleCount() > 0 {
			return float64(track.SampleCount()) / duration
		}
	}
	return 0
}

// MetadataClock is DJI's own timing for the metadata track, read from the two
// scalar fields that sit beside the quaternions in their container message.
//
// The container carries a microsecond timestamp and a sample counter; the
// quaternion messages under it carry only their four components, with no
// per-quaternion timing of any kind. That was established by walking every
// quaternion message in two O4 clips — 993,523 and 455,517 of them — and
// finding the same four fixed32 fields and nothing else in each. Sub-sample
// times are therefore interpolated across the sample because no better source
// exists, not because interpolating was assumed to be good enough.
//
// The counter makes a dropped metadata sample identifiable rather than inferred:
// it steps by one per sample through the body of both clips. The timestamp gives
// DJI's own idea of the rate, which is worth having next to the container's,
// because on one measured clip the two disagree by 0.1%.
//
// Field numbers were confirmed on wm169 only. Present is false wherever the
// fields are missing, which is how a layout that numbers them differently
// reports rather than inventing a reading.
type MetadataClock struct {
	Present bool
	// Samples is how many samples carried both fields.
	Samples int
	// SpanSeconds is the embedded clock's own elapsed time across the track.
	SpanSeconds float64
	// Rate is samples per second by the embedded clock.
	Rate float64
	// ContainerRate is samples per second by the track's decode times.
	ContainerRate float64
	// FirstSequence and LastSequence bound the counter.
	FirstSequence, LastSequence uint64
	// SequenceGaps counts steps other than exactly one. Both measured clips
	// show two, in their final two samples, so a trailing pair is normal and a
	// gap in the body is not.
	SequenceGaps int
	// FirstGapSample is where the first such step happened, or -1.
	FirstGapSample int
}

// DriftPercent is how much faster the embedded clock runs than the container.
func (c MetadataClock) DriftPercent() float64 {
	if c.ContainerRate <= 0 || c.Rate <= 0 {
		return 0
	}
	return 100 * (c.Rate/c.ContainerRate - 1)
}

// ReadMetadataClock walks every sample for the container's timestamp and
// counter. It is a second full pass over the track and is meant for `info`, not
// for the analysis path.
func (s *Source) ReadMetadataClock() (MetadataClock, error) {
	clock := MetadataClock{FirstGapSample: -1}
	count := s.Track.SampleCount()
	if count < 2 {
		return clock, nil
	}
	path, ok := s.Variant.Path()
	if !ok || len(path) < 2 {
		return clock, nil
	}
	var firstStamp, lastStamp, previous uint64
	for index := 0; index < count; {
		runEnd, runBytes, err := s.readRun(index, count)
		if err != nil {
			return clock, err
		}
		base := s.Track.SampleOffsets[index]
		for sampleIndex := index; sampleIndex < runEnd; sampleIndex++ {
			start := s.Track.SampleOffsets[sampleIndex] - base
			sample := runBytes[start : start+s.Track.SampleSizes[sampleIndex]]
			stamp, sequence, found := containerScalars(sample, path[:len(path)-1])
			if !found {
				continue
			}
			if clock.Samples == 0 {
				firstStamp, clock.FirstSequence = stamp, sequence
			} else if sequence != previous+1 {
				clock.SequenceGaps++
				if clock.FirstGapSample < 0 {
					clock.FirstGapSample = sampleIndex
				}
			}
			previous, lastStamp, clock.LastSequence = sequence, stamp, sequence
			clock.Samples++
		}
		index = runEnd
	}
	if clock.Samples < 2 || lastStamp <= firstStamp {
		return MetadataClock{FirstGapSample: -1}, nil
	}
	clock.Present = true
	clock.SpanSeconds = float64(lastStamp-firstStamp) / 1e6
	clock.Rate = float64(clock.Samples-1) / clock.SpanSeconds
	if span := s.Track.SampleTime(count-1) - s.Track.SampleTime(0); span > 0 {
		clock.ContainerRate = float64(count-1) / span
	}
	return clock, nil
}

// containerScalars reads the timestamp and counter beside the quaternions.
func containerScalars(sample []byte, path []int) (stamp, sequence uint64, found bool) {
	start, end := 0, len(sample)
	for _, number := range path {
		fields, err := djiproto.Fields(sample, start, end)
		if err != nil {
			return 0, 0, false
		}
		descended := false
		for _, field := range fields {
			if field.Number == number && field.WireType == 2 {
				start, end, descended = field.PayloadStart, field.PayloadEnd, true
				break
			}
		}
		if !descended {
			return 0, 0, false
		}
	}
	fields, err := djiproto.Fields(sample, start, end)
	if err != nil {
		return 0, 0, false
	}
	var haveStamp, haveSequence bool
	for _, field := range fields {
		if field.WireType != 0 {
			continue
		}
		value, err := djiproto.Varint(sample, field.ValueStart, field.ValueEnd)
		if err != nil {
			return 0, 0, false
		}
		switch field.Number {
		case 1:
			stamp, haveStamp = value, true
		case 2:
			sequence, haveSequence = value, true
		}
	}
	return stamp, sequence, haveStamp && haveSequence
}

// DuplicatePairShare is the fraction of quaternions identical to the one before
// them. DJI fills a fixed number of slots per video frame from an IMU running
// near 1000 Hz, so this is a function of frame rate rather than a property of
// the format: none at 30 fps, a half at 60. Detection uses its own copy to
// decide whether to collapse the repeats; this one is for reporting.
func DuplicatePairShare(values []quat.Q) float64 {
	if len(values) < 2 {
		return 0
	}
	identical := 0
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			identical++
		}
	}
	return float64(identical) / float64(len(values)-1)
}
