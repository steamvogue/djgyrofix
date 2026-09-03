package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/steamvogue/djgyrofix/internal/djiproto"
	"github.com/steamvogue/djgyrofix/internal/pipeline"
)

// runInfo dumps track and variant details.
//
// It prints both the sniffed variant and the field path used to find
// quaternions, because sniffing is a heuristic — it greps the first samples for
// a model string — and you will eventually hit a camera it guesses wrong on.
// Seeing the guess and the resulting quaternion count is how you find out.
func runInfo(args []string) error {
	flags := flag.NewFlagSet("info", flag.ExitOnError)
	variantFlag := flags.String("variant", "auto", "metadata layout: wm169 | wa530 | oq101 | auto")
	all := flags.Bool("all-variants", false, "report what every variant path would find")
	flags.Usage = commandUsage(flags,
		"inspect a file",
		"djgyrofix info [flags] <file...>",
		`Lists every track, marks the DJI metadata track with an asterisk, and prints
the metadata variant along with the protobuf field path used to find
quaternions.

Variant sniffing is a heuristic — it greps the first samples for a model string
— so this is where you find out it guessed wrong. --all-variants reports what
each other path would have found.`,
		[]flagGroup{{"Flags", infoFlagNames}},
		[][2]string{
			{"djgyrofix info DJI_0042.MP4", "tracks, variant and sample rate"},
			{"djgyrofix info --all-variants clip.MP4", "diagnose a wrong variant guess"},
			{"djgyrofix info --variant oq101 clip.MP4", "force a layout and see what it finds"},
		})
	if err := flags.Parse(args); err != nil {
		return err
	}
	paths := flags.Args()
	if len(paths) == 0 {
		flags.Usage()
		return fmt.Errorf("no input files")
	}

	opts := &options{variant: *variantFlag}
	override, err := opts.variantOverride()
	if err != nil {
		return err
	}

	var failures []error
	for index, path := range paths {
		if index > 0 {
			fmt.Println()
		}
		if err := infoOne(path, override, *all); err != nil {
			failures = append(failures, err)
		}
	}
	return summarize(failures)
}

func infoOne(path string, override djiproto.Variant, allVariants bool) error {
	source, err := pipeline.Open(path, override)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	// Opened read-only; a close failure has nothing the caller can act on.
	defer func() { _ = source.Close() }()

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", path)
	fmt.Printf("  size:      %d bytes\n", info.Size())
	fmt.Printf("  tracks:    %d\n", len(source.Tracks))
	for _, track := range source.Tracks {
		marker := "  "
		if track == source.Track {
			marker = "* "
		}
		name := track.HandlerName
		if name == "" {
			name = "-"
		}
		fmt.Printf("  %sid=%d handler=%s entry=%s name=%q samples=%d timescale=%d duration=%.3fs\n",
			marker, track.ID, track.HandlerType, orDash(track.SampleEntry), name,
			track.SampleCount(), track.Timescale, track.DurationSeconds())
	}

	path4, _ := source.Variant.Path()
	fmt.Printf("\n  variant:   %s", source.Variant)
	if source.Variant != source.VariantDetected {
		fmt.Printf("  (forced; sniffing said %s)", source.VariantDetected)
	} else {
		fmt.Printf("  (sniffed)")
	}
	fmt.Printf("\n  field path: %s\n", joinInts(path4, "."))

	points, err := source.ReadAll()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	fmt.Printf("  quaternions: %d in %d samples", len(points), source.Track.SampleCount())
	if source.Track.SampleCount() > 0 {
		fmt.Printf(" (%.2f per sample)", float64(len(points))/float64(source.Track.SampleCount()))
	}
	fmt.Println()
	if rate, interval, ok := pipeline.SampleRate(pipeline.Times(points)); ok {
		fmt.Printf("  rate:      %.2f Hz (median interval %.3f ms)\n", rate, interval*1000)
	}
	if fps := pipeline.VideoFPS(source.Tracks); fps > 0 {
		fmt.Printf("  video:     %.3f fps\n", fps)
	}

	// DJI's own clock, printed beside the container's. On contributed footage
	// this is the first place a dropped metadata sample or an odd clock would
	// show, and neither is visible anywhere else.
	clock, err := source.ReadMetadataClock()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if clock.Present {
		fmt.Printf("\n  dji clock: %.4f Hz over %.3f s (container says %.4f Hz, %+.4f%%)\n",
			clock.Rate, clock.SpanSeconds, clock.ContainerRate, clock.DriftPercent())
		fmt.Printf("  counter:   %d to %d", clock.FirstSequence, clock.LastSequence)
		switch {
		case clock.SequenceGaps == 0:
			fmt.Printf(", unbroken\n")
		case clock.FirstGapSample >= source.Track.SampleCount()-2:
			fmt.Printf(", %d step(s) other than one, in the trailing samples only\n", clock.SequenceGaps)
		default:
			fmt.Printf(", %d step(s) other than one, first at sample %d — metadata samples are missing\n",
				clock.SequenceGaps, clock.FirstGapSample)
		}
	}

	if allVariants {
		fmt.Printf("\n  what each variant path finds in the first 200 samples:\n")
		limit := 200
		if limit > source.Track.SampleCount() {
			limit = source.Track.SampleCount()
		}
		for _, candidate := range djiproto.Variants {
			probe := *source
			probe.Variant = candidate
			found, err := probe.ReadRange(0, limit)
			marker := " "
			if candidate == source.Variant {
				marker = "*"
			}
			if err != nil {
				fmt.Printf("  %s %-6s error: %v\n", marker, candidate, err)
				continue
			}
			fmt.Printf("  %s %-6s %d quaternions\n", marker, candidate, len(found))
		}
	}
	return nil
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func joinInts(values []int, separator string) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = fmt.Sprint(value)
	}
	return strings.Join(parts, separator)
}
