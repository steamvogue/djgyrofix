// Command mkfixture writes a synthetic DJI-style MP4 for manual testing.
//
// It is a development aid, not part of the shipped tool: the same generator
// backs the Go tests, and this exposes it so a human can run the CLI against a
// file with artifacts injected at known times.
package main

import (
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/steamvogue/djgyrofix/internal/djiproto"
	"github.com/steamvogue/djgyrofix/internal/quat"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

func main() {
	out := flag.String("o", "fixture.MP4", "output path")
	seconds := flag.Float64("seconds", 30, "clip length")
	rate := flag.Float64("rate", 200, "quaternion rate in Hz")
	kind := flag.String("kind", "mixed", "clean | jitter | impact | dropout | whippan | mixed")
	variant := flag.String("variant", "wm169", "wm169 | wa530 | oq101")
	perSample := flag.Int("per-sample", 4, "quaternions per metadata sample")
	rough := flag.Float64("rough-until", 0, "add broadband shake from t=0 until this time")
	seed := flag.Int64("seed", 20260901, "random seed")
	flag.Parse()

	sampleRate := *rate / float64(*perSample)
	sampleCount := int(*seconds * sampleRate)
	timescale := uint32(1000)
	deltaTicks := uint32(math.Round(float64(timescale) / sampleRate))
	if deltaTicks == 0 {
		fmt.Fprintln(os.Stderr, "mkfixture: sample rate is too high for a 1000 Hz timescale")
		os.Exit(2)
	}

	track := synth.Attitude(synth.AttitudeOptions{
		Defect:     synth.Defect(*kind),
		Rate:       *rate,
		Seconds:    *seconds,
		Seed:       *seed,
		RoughUntil: *rough,
	})
	built, err := synth.Build(synth.Options{
		Variant:          djiproto.Variant(*variant),
		Timescale:        timescale,
		SampleCount:      sampleCount,
		QuatsPerSample:   *perSample,
		SampleDeltaTicks: deltaTicks,
		SamplesPerChunk:  64,
		WithVideoTrack:   true,
		Quaternion: func(sampleIndex, subIndex int) quat.Q {
			index := sampleIndex**perSample + subIndex
			if index >= len(track) {
				index = len(track) - 1
			}
			return track[index]
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkfixture:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, built.Bytes, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "mkfixture:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d bytes, %d samples x %d quaternions @ %.1f Hz, kind=%s variant=%s\n",
		*out, len(built.Bytes), sampleCount, *perSample, *rate, *kind, *variant)
}
