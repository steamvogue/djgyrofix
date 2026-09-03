package pipeline_test

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/djiproto"
	"github.com/steamvogue/djgyrofix/internal/pipeline"
	"github.com/steamvogue/djgyrofix/internal/quat"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

func fixture(t *testing.T, options synth.Options) (string, *synth.File) {
	t.Helper()
	built, err := synth.Build(options)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "clip.MP4")
	if err := os.WriteFile(path, built.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, built
}

func TestReadAllRecoversEveryQuaternionAtItsRealOffset(t *testing.T) {
	// Several chunks, so the contiguous-run read path has boundaries to get
	// wrong, and a decoy video track so selection has to choose.
	path, built := fixture(t, synth.Options{
		SampleCount: 40, QuatsPerSample: 3, SamplesPerChunk: 6, WithVideoTrack: true,
	})
	source, err := pipeline.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	points, err := source.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 40*3 {
		t.Fatalf("read %d quaternions, want 120", len(points))
	}
	for index, point := range points {
		sampleIndex, subIndex := index/3, index%3
		if point.SampleIndex != sampleIndex {
			t.Fatalf("point %d claims sample %d, want %d", index, point.SampleIndex, sampleIndex)
		}
		for component, offset := range point.Offsets {
			want := int64(built.QuatOffsets[sampleIndex][subIndex][component])
			if offset != want {
				t.Fatalf("point %d component %d at offset %d, want %d", index, component, offset, want)
			}
		}
	}
}

// TestSubSampleTimesAreEvenlySpaced pins the reference's interpolation: each
// quaternion within a sample gets sampleTime + span*subIndex/count. There is no
// known per-quaternion timestamp in the DJI schema, so this linear split is the
// best available and everything downstream depends on it.
func TestSubSampleTimesAreEvenlySpaced(t *testing.T) {
	const perSample = 4
	path, _ := fixture(t, synth.Options{
		SampleCount: 10, QuatsPerSample: perSample, Timescale: 1000, SampleDeltaTicks: 20,
	})
	source, err := pipeline.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	points, err := source.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// 20 ticks at a 1000 Hz timescale is 20 ms per sample, so four quaternions
	// land 5 ms apart.
	for index, point := range points {
		want := float64(index) * 0.005
		if math.Abs(point.Time-want) > 1e-9 {
			t.Fatalf("point %d at %.6fs, want %.6fs", index, point.Time, want)
		}
	}
}

func TestReadWindowOverReadsOneSampleEachSide(t *testing.T) {
	path, _ := fixture(t, synth.Options{
		SampleCount: 50, QuatsPerSample: 2, Timescale: 1000, SampleDeltaTicks: 20,
	})
	source, err := pipeline.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	// Samples run at 20 ms each, so [0.20, 0.30) is samples 10 through 14.
	// The reference deliberately widens by one on each side.
	points, err := source.ReadWindow(0.20, 0.30)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) == 0 {
		t.Fatal("the window read nothing")
	}
	if points[0].SampleIndex >= 10 {
		t.Errorf("window started at sample %d, expected one before 10", points[0].SampleIndex)
	}
	last := points[len(points)-1].SampleIndex
	if last <= 15 {
		t.Errorf("window ended at sample %d, expected one past 15", last)
	}
}

func TestVariantOverrideAndDetection(t *testing.T) {
	path, _ := fixture(t, synth.Options{Variant: djiproto.VariantOQ101, SampleCount: 12})
	source, err := pipeline.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if source.VariantDetected != djiproto.VariantOQ101 || source.Variant != djiproto.VariantOQ101 {
		t.Errorf("sniffed %q and using %q, want oq101", source.VariantDetected, source.Variant)
	}
	source.Close()

	// An override must be honoured while the sniffed value is still reported,
	// because that is how a user finds out the guess was wrong.
	source, err = pipeline.Open(path, djiproto.VariantWM169)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.Variant != djiproto.VariantWM169 || source.VariantDetected != djiproto.VariantOQ101 {
		t.Errorf("override gave variant %q, detected %q", source.Variant, source.VariantDetected)
	}

	if _, err := pipeline.Open(path, djiproto.Variant("mavic4")); err == nil {
		t.Error("an unknown variant override was accepted")
	}
}

func TestOpenRejectsNonVideo(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "notes.txt")
	if err := os.WriteFile(path, []byte("not an MP4"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Open(path, ""); err == nil {
		t.Error("a non-MP4 file was accepted")
	}
	if _, err := pipeline.Open(directory, ""); err == nil {
		t.Error("a directory was accepted")
	}
	if _, err := pipeline.Open(filepath.Join(directory, "missing.MP4"), ""); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestSampleRateFromTimes(t *testing.T) {
	times := make([]float64, 100)
	for index := range times {
		times[index] = float64(index) / 199.8
	}
	rate, interval, ok := pipeline.SampleRate(times)
	if !ok {
		t.Fatal("SampleRate reported failure on a clean series")
	}
	if math.Abs(rate-199.8) > 1e-6 {
		t.Errorf("rate = %g, want 199.8", rate)
	}
	if math.Abs(interval-1/199.8) > 1e-12 {
		t.Errorf("interval = %g", interval)
	}
	// A stalled or single-sample series has no rate to report.
	if _, _, ok := pipeline.SampleRate([]float64{1}); ok {
		t.Error("SampleRate accepted a single sample")
	}
	if _, _, ok := pipeline.SampleRate([]float64{1, 1, 1}); ok {
		t.Error("SampleRate accepted a series with no forward progress")
	}
}

func TestTimesAndValuesProject(t *testing.T) {
	points := []pipeline.Point{
		{Time: 1, Values: quat.Q{1, 0, 0, 0}},
		{Time: 2, Values: quat.Q{0, 1, 0, 0}},
	}
	times := pipeline.Times(points)
	values := pipeline.Values(points)
	if len(times) != 2 || times[1] != 2 {
		t.Errorf("Times = %v", times)
	}
	if len(values) != 2 || values[1] != (quat.Q{0, 1, 0, 0}) {
		t.Errorf("Values = %v", values)
	}
}

func TestVideoFPSFindsTheVideoTrack(t *testing.T) {
	path, _ := fixture(t, synth.Options{SampleCount: 20, WithVideoTrack: true})
	source, err := pipeline.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if fps := pipeline.VideoFPS(source.Tracks); math.Abs(fps-30) > 1 {
		t.Errorf("VideoFPS = %g, want about 30", fps)
	}

	pathNoVideo, _ := fixture(t, synth.Options{SampleCount: 20})
	metaOnly, err := pipeline.Open(pathNoVideo, "")
	if err != nil {
		t.Fatal(err)
	}
	defer metaOnly.Close()
	if fps := pipeline.VideoFPS(metaOnly.Tracks); fps != 0 {
		t.Errorf("VideoFPS = %g with no video track, want 0", fps)
	}
}

func TestReadRangeClampsOutOfBoundsRequests(t *testing.T) {
	path, _ := fixture(t, synth.Options{SampleCount: 10, QuatsPerSample: 2})
	source, err := pipeline.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	all, err := source.ReadRange(-50, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 20 {
		t.Errorf("clamped read returned %d quaternions, want 20", len(all))
	}
	if empty, err := source.ReadRange(5, 5); err != nil || empty != nil {
		t.Errorf("an empty range returned %v, %v", empty, err)
	}
}

// TestMetadataClockReadsDJIsOwnTiming covers the two scalar fields that sit
// beside the quaternions. The interesting case is the second one: a counter
// that steps by more than one means metadata samples went missing, which is the
// only direct evidence of that available — everywhere else it has to be
// inferred from timestamps.
func TestMetadataClockReadsDJIsOwnTiming(t *testing.T) {
	const samples, deltaMicros = 60, 16683
	tests := []struct {
		name         string
		clock        func(int) (uint64, uint64)
		wantGaps     int
		wantFirstGap int
	}{
		{
			name: "unbroken",
			clock: func(index int) (uint64, uint64) {
				return uint64(1_000_000 + index*deltaMicros), uint64(500 + index)
			},
			wantGaps: 0, wantFirstGap: -1,
		},
		{
			name: "a dropped sample in the body",
			clock: func(index int) (uint64, uint64) {
				sequence := uint64(500 + index)
				if index >= 30 {
					sequence += 7
				}
				return uint64(1_000_000 + index*deltaMicros), sequence
			},
			wantGaps: 1, wantFirstGap: 30,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, _ := fixture(t, synth.Options{
				SampleCount: samples, QuatsPerSample: 4, Timescale: 1000,
				SampleDeltaTicks: 17, WithVideoTrack: true, SampleClock: test.clock,
			})
			source, err := pipeline.Open(path, "")
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()

			clock, err := source.ReadMetadataClock()
			if err != nil {
				t.Fatal(err)
			}
			if !clock.Present || clock.Samples != samples {
				t.Fatalf("clock = %+v, want present with %d samples", clock, samples)
			}
			if clock.SequenceGaps != test.wantGaps || clock.FirstGapSample != test.wantFirstGap {
				t.Errorf("gaps %d at %d, want %d at %d",
					clock.SequenceGaps, clock.FirstGapSample, test.wantGaps, test.wantFirstGap)
			}
			if want := 1e6 / float64(deltaMicros); math.Abs(clock.Rate-want) > 0.01 {
				t.Errorf("embedded rate %.4f Hz, want %.4f", clock.Rate, want)
			}
			// The fixture's decode times run at 17 ms against the embedded
			// clock's 16.683, so the drift is real and signed.
			if clock.DriftPercent() <= 0 {
				t.Errorf("drift %.4f%%, want the embedded clock to lead", clock.DriftPercent())
			}
		})
	}
}

// TestMetadataClockIsAbsentRatherThanGuessed covers a layout that does not carry
// the fields. Reporting nothing is the point: the field numbers were confirmed
// on wm169 only, and a reader that invented a rate from whatever varint it found
// would be worse than one that stays quiet.
func TestMetadataClockIsAbsentRatherThanGuessed(t *testing.T) {
	path, _ := fixture(t, synth.Options{SampleCount: 20, QuatsPerSample: 4})
	source, err := pipeline.Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	clock, err := source.ReadMetadataClock()
	if err != nil {
		t.Fatal(err)
	}
	if clock.Present || clock.Rate != 0 || clock.DriftPercent() != 0 {
		t.Errorf("a fixture without the fields reported a clock: %+v", clock)
	}
}
