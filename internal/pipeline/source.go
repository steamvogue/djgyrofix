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
