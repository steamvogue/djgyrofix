package mp4_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/mp4"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

func TestFinalizeMapsSamplesAndTimestamps(t *testing.T) {
	track := &mp4.Track{
		ID:           7,
		Timescale:    100,
		Duration:     30,
		SampleSizes:  []int64{2, 3, 4},
		ChunkOffsets: []int64{100, 200},
		Stsc:         []mp4.StscEntry{{1, 2, 1}, {2, 1, 1}},
		Stts:         []mp4.SttsEntry{{3, 10}},
	}
	if err := track.Finalize(); err != nil {
		t.Fatal(err)
	}
	wantOffsets := []int64{100, 102, 200}
	for index, want := range wantOffsets {
		if track.SampleOffsets[index] != want {
			t.Errorf("sample %d offset = %d, want %d", index, track.SampleOffsets[index], want)
		}
	}
	wantDTS := []int64{0, 10, 20}
	for index, want := range wantDTS {
		if track.SampleDTS[index] != want {
			t.Errorf("sample %d dts = %d, want %d", index, track.SampleDTS[index], want)
		}
	}
}

func TestFinalizeRejectsBrokenTables(t *testing.T) {
	cases := map[string]*mp4.Track{
		"stsc not starting at chunk 1": {
			ID: 7, Timescale: 100, SampleSizes: []int64{4},
			ChunkOffsets: []int64{100}, Stsc: []mp4.StscEntry{{2, 1, 1}}, Stts: []mp4.SttsEntry{{1, 10}},
		},
		"non-monotonic stsc": {
			ID: 7, Timescale: 100, SampleSizes: []int64{4, 4},
			ChunkOffsets: []int64{100, 200}, Stsc: []mp4.StscEntry{{1, 1, 1}, {1, 1, 1}}, Stts: []mp4.SttsEntry{{2, 10}},
		},
		"zero samples per chunk": {
			ID: 7, Timescale: 100, SampleSizes: []int64{4},
			ChunkOffsets: []int64{100}, Stsc: []mp4.StscEntry{{1, 0, 1}}, Stts: []mp4.SttsEntry{{1, 10}},
		},
		"zero timescale": {
			ID: 7, SampleSizes: []int64{4},
			ChunkOffsets: []int64{100}, Stsc: []mp4.StscEntry{{1, 1, 1}}, Stts: []mp4.SttsEntry{{1, 10}},
		},
		"stts short of samples": {
			ID: 7, Timescale: 100, SampleSizes: []int64{4, 4, 4},
			ChunkOffsets: []int64{100}, Stsc: []mp4.StscEntry{{1, 3, 1}}, Stts: []mp4.SttsEntry{{2, 10}},
		},
		"too few chunks for the samples": {
			ID: 7, Timescale: 100, SampleSizes: []int64{4, 4, 4},
			ChunkOffsets: []int64{100}, Stsc: []mp4.StscEntry{{1, 1, 1}}, Stts: []mp4.SttsEntry{{3, 10}},
		},
	}
	for name, track := range cases {
		t.Run(name, func(t *testing.T) {
			if err := track.Finalize(); err == nil {
				t.Error("accepted a broken sample table")
			}
		})
	}
}

func TestBoxIteratorRejectsBoxBeyondParent(t *testing.T) {
	data := make([]byte, 8)
	binary.BigEndian.PutUint32(data, 128)
	copy(data[4:], "free")
	data = append(data, []byte("payload")...)
	if _, err := mp4.Boxes(bytes.NewReader(data), 0, int64(len(data))); err == nil {
		t.Error("accepted a box claiming to run past its parent")
	}
}

func TestBoxIteratorHandlesExtendedAndZeroSizes(t *testing.T) {
	// size==1 means a 64-bit size follows the type.
	extended := make([]byte, 16)
	binary.BigEndian.PutUint32(extended, 1)
	copy(extended[4:], "mdat")
	binary.BigEndian.PutUint64(extended[8:], 24)
	extended = append(extended, make([]byte, 8)...)
	boxes, err := mp4.Boxes(bytes.NewReader(extended), 0, int64(len(extended)))
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 1 || boxes[0].Size != 24 || boxes[0].HeaderSize != 16 {
		t.Fatalf("extended header parsed as %+v", boxes)
	}

	// size==0 means the box runs to the end of its parent.
	zero := make([]byte, 8)
	copy(zero[4:], "mdat")
	zero = append(zero, make([]byte, 12)...)
	boxes, err = mp4.Boxes(bytes.NewReader(zero), 0, int64(len(zero)))
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 1 || boxes[0].Size != 20 {
		t.Fatalf("zero-size box parsed as %+v", boxes)
	}
}

func TestSampleRangeOverReadsOneSampleEachSide(t *testing.T) {
	// The reference deliberately widens by one sample on each side so a
	// centered filter never starts mid-glitch. That behaviour is load-bearing
	// for golden parity, so it is pinned here.
	track := &mp4.Track{
		Timescale: 1000,
		SampleDTS: []int64{0, 100, 200, 300, 400, 500},
	}
	first, last := track.SampleRange(0.2, 0.3)
	if first != 1 || last != 5 {
		t.Errorf("SampleRange(0.2, 0.3) = [%d, %d), want [1, 5)", first, last)
	}
	first, last = track.SampleRange(0, 0)
	if first != 0 || last != 2 {
		t.Errorf("SampleRange(0, 0) = [%d, %d), want [0, 2)", first, last)
	}

	empty := &mp4.Track{}
	if first, last := empty.SampleRange(0, 1); first != 0 || last != 0 {
		t.Errorf("empty track range = [%d, %d), want [0, 0)", first, last)
	}
}

func TestFragmentedFilesAreRejected(t *testing.T) {
	built, err := synth.Build(synth.Options{SampleCount: 8})
	if err != nil {
		t.Fatal(err)
	}
	// A moof box at the top level means the samples this walker indexed are
	// not the whole story; guessing would put writes at the wrong offsets.
	moof := make([]byte, 8)
	binary.BigEndian.PutUint32(moof, 8)
	copy(moof[4:], "moof")
	fragmented := append(append([]byte(nil), built.Bytes...), moof...)
	if _, err := mp4.ParseTracks(bytes.NewReader(fragmented), int64(len(fragmented))); err == nil {
		t.Error("a fragmented file was accepted")
	}
}

func TestMissingMoovIsRejected(t *testing.T) {
	data := make([]byte, 8)
	binary.BigEndian.PutUint32(data, 8)
	copy(data[4:], "ftyp")
	if _, err := mp4.ParseTracks(bytes.NewReader(data), int64(len(data))); err == nil {
		t.Error("a file with no moov was accepted")
	}
}

func TestFindDJIMetadataTrackPrefersTheLargestMatch(t *testing.T) {
	small := &mp4.Track{ID: 1, SampleEntry: "djmd", SampleSizes: make([]int64, 3)}
	large := &mp4.Track{ID: 2, HandlerName: "DJI Meta", SampleSizes: make([]int64, 300)}
	video := &mp4.Track{ID: 3, SampleEntry: "avc1", SampleSizes: make([]int64, 9000)}
	got, err := mp4.FindDJIMetadataTrack([]*mp4.Track{small, large, video})
	if err != nil {
		t.Fatal(err)
	}
	if got != large {
		t.Errorf("selected track %d, want 2", got.ID)
	}
	if _, err := mp4.FindDJIMetadataTrack([]*mp4.Track{video}); err == nil {
		t.Error("a file with no metadata track was accepted")
	}
}

// FuzzParseTracks walks attacker-controllable length fields throughout, so it
// must reject malformed input rather than panic or allocate without bound.
func FuzzParseTracks(f *testing.F) {
	for _, options := range []synth.Options{
		{SampleCount: 4, QuatsPerSample: 2},
		{SampleCount: 6, SamplesPerChunk: 2, Use64BitOffsets: true, WithVideoTrack: true},
	} {
		if built, err := synth.Build(options); err == nil {
			f.Add(built.Bytes)
		}
	}
	f.Add([]byte("ftypisom"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		tracks, err := mp4.ParseTracks(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		for _, track := range tracks {
			if len(track.SampleOffsets) != len(track.SampleSizes) {
				t.Fatalf("track %d: %d offsets for %d sizes", track.ID, len(track.SampleOffsets), len(track.SampleSizes))
			}
			if len(track.SampleDTS) != len(track.SampleSizes) {
				t.Fatalf("track %d: %d timestamps for %d samples", track.ID, len(track.SampleDTS), len(track.SampleSizes))
			}
			for index, offset := range track.SampleOffsets {
				if offset < 0 || offset+track.SampleSizes[index] > int64(len(data)) {
					t.Fatalf("track %d sample %d spans [%d, %d) outside a %d-byte file",
						track.ID, index, offset, offset+track.SampleSizes[index], len(data))
				}
			}
		}
	})
}
