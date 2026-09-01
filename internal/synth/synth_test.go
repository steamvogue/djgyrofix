package synth_test

import (
	"bytes"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/djiproto"
	"github.com/steamvogue/djgyrofix/internal/mp4"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

// TestSyntheticFileParsesBackToWhatWasBuilt closes the loop on the test
// fixtures themselves: if the builder and the parser ever disagree, every test
// built on top of them is meaningless.
func TestSyntheticFileParsesBackToWhatWasBuilt(t *testing.T) {
	for _, variant := range djiproto.Variants {
		for _, wide := range []bool{false, true} {
			name := string(variant)
			if wide {
				name += "_co64"
			}
			t.Run(name, func(t *testing.T) {
				built, err := synth.Build(synth.Options{
					Variant:         variant,
					SampleCount:     37,
					QuatsPerSample:  5,
					SamplesPerChunk: 4,
					Use64BitOffsets: wide,
					WithVideoTrack:  true,
				})
				if err != nil {
					t.Fatalf("build: %v", err)
				}
				reader := bytes.NewReader(built.Bytes)
				tracks, err := mp4.ParseTracks(reader, int64(len(built.Bytes)))
				if err != nil {
					t.Fatalf("parse: %v", err)
				}
				track, err := mp4.FindDJIMetadataTrack(tracks)
				if err != nil {
					t.Fatalf("find track: %v", err)
				}
				if track.SampleEntry != "djmd" {
					t.Errorf("sample entry = %q, want djmd", track.SampleEntry)
				}
				if got, want := track.SampleCount(), len(built.SampleOffsets); got != want {
					t.Fatalf("sample count = %d, want %d", got, want)
				}
				for index := range built.SampleOffsets {
					if track.SampleOffsets[index] != built.SampleOffsets[index] {
						t.Fatalf("sample %d offset = %d, want %d", index, track.SampleOffsets[index], built.SampleOffsets[index])
					}
					if track.SampleSizes[index] != built.SampleSizes[index] {
						t.Fatalf("sample %d size = %d, want %d", index, track.SampleSizes[index], built.SampleSizes[index])
					}
				}

				// Variant sniffing must recover what was written.
				var first [][]byte
				for index := 0; index < 5; index++ {
					start := built.SampleOffsets[index]
					first = append(first, built.Bytes[start:start+built.SampleSizes[index]])
				}
				if got := djiproto.DetectVariant(first); got != variant {
					t.Errorf("DetectVariant = %q, want %q", got, variant)
				}

				// Every quaternion offset the scanner reports must be the one
				// the builder wrote — this is what invariant I3 rests on.
				for index := range built.SampleOffsets {
					start := built.SampleOffsets[index]
					sample := built.Bytes[start : start+built.SampleSizes[index]]
					refs, err := djiproto.Quaternions(sample, variant)
					if err != nil {
						t.Fatalf("sample %d: %v", index, err)
					}
					if len(refs) != len(built.QuatOffsets[index]) {
						t.Fatalf("sample %d: found %d quaternions, want %d", index, len(refs), len(built.QuatOffsets[index]))
					}
					for quatIndex, ref := range refs {
						for component := range ref.Offsets {
							want := built.QuatOffsets[index][quatIndex][component] - int(start)
							if ref.Offsets[component] != want {
								t.Fatalf("sample %d quat %d component %d: offset %d, want %d",
									index, quatIndex, component, ref.Offsets[component], want)
							}
						}
					}
				}
			})
		}
	}
}
