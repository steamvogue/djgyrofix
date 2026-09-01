package main

import (
	"flag"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/djiproto"
)

// options is the full flag surface shared by scan and fix. Each command
// registers only the subset it honours.
type options struct {
	// Detection
	profile        string
	sensitivity    float64
	madK           float64
	baselineWindow time.Duration
	floorDPS       float64
	minSeverity    optionalFloat
	imuFullScale   float64

	// Correction
	strength         float64
	smoothingMS      float64
	bridgeMaxSamples optionalInt
	noBridge         bool
	ranges           string

	// Safety and I/O
	dryRun      bool
	apply       bool
	backup      string
	out         string
	force       bool
	maxAffected float64
	variant     string
	jobs        int
	format      string
}

func (o *options) registerDetection(flags *flag.FlagSet) {
	flags.StringVar(&o.profile, "profile", "balanced", "detection preset: conservative | balanced | aggressive")
	flags.Float64Var(&o.sensitivity, "sensitivity", 1.0, "scale all thresholds, 0.1-3.0 (higher detects more)")
	flags.Float64Var(&o.madK, "mad-k", 0, "Hampel sigma multiplier (default from profile)")
	flags.DurationVar(&o.baselineWindow, "baseline-window", 0, "rolling baseline half-width (default 5s)")
	flags.Float64Var(&o.floorDPS, "floor-dps", 0, "absolute residual floor in °/s (default from profile)")
	flags.Var(&o.minSeverity, "min-severity", "ignore events scoring below this, 0-10 (default from profile)")
	flags.Float64Var(&o.imuFullScale, "imu-full-scale", 0, "plausibility gate in °/s (default 2000)")
}

func (o *options) registerCorrection(flags *flag.FlagSet) {
	flags.Float64Var(&o.strength, "strength", 1.0, "global multiplier on the correction weight, 0-1")
	flags.Float64Var(&o.smoothingMS, "smoothing-ms", 0, "override the per-event window derivation (default: auto)")
	flags.Var(&o.bridgeMaxSamples, "bridge-max-samples", "longest dropout run to SLERP-bridge (default 3)")
	flags.BoolVar(&o.noBridge, "no-bridge", false, "disable reconstruction entirely")
	flags.StringVar(&o.ranges, "ranges", "", `manual override, e.g. "12.5-14.0,61-62.25" (skips detection)`)
}

func (o *options) registerIO(flags *flag.FlagSet, forFix bool) {
	// Scan performs the same bounded correction analysis as fix so its dry-run
	// report predicts the eventual writes. Fix exposes the limit as a flag;
	// scan still needs the same default expansion budget internally.
	o.maxAffected = 0.15
	if forFix {
		flags.BoolVar(&o.dryRun, "dry-run", false, "analyze and report without writing (the default)")
		flags.BoolVar(&o.apply, "apply", false, "actually write")
		flags.StringVar(&o.backup, "backup", "journal", "undo strategy: journal | copy | none")
		flags.StringVar(&o.out, "out", "", "write a patched copy here instead of patching in place")
		flags.BoolVar(&o.force, "force", false, "override idempotency and safety guards")
		flags.Float64Var(&o.maxAffected, "max-affected", o.maxAffected, "refuse if flagged duration exceeds this fraction of the clip")
	}
	flags.StringVar(&o.variant, "variant", "auto", "metadata layout: wm169 | wa530 | oq101 | auto")
	flags.IntVar(&o.jobs, "jobs", runtime.NumCPU(), "files to process in parallel")
	flags.StringVar(&o.format, "format", "text", "output format: text | json | edl | csv")
}

// detectParams folds the profile preset and any explicit overrides together.
func (o *options) detectParams() (detect.Params, error) {
	params, err := detect.ProfileParams(o.profile)
	if err != nil {
		return params, err
	}
	params.Sensitivity = o.sensitivity
	if o.madK > 0 {
		params.MADK = o.madK
	}
	if o.baselineWindow > 0 {
		params.BaselineWindow = o.baselineWindow
	}
	if o.floorDPS > 0 {
		params.FloorDPS = o.floorDPS
	}
	if o.minSeverity.set {
		params.MinSeverity = o.minSeverity.value
	}
	if o.imuFullScale > 0 {
		params.IMUFullScale = o.imuFullScale
	}
	if o.bridgeMaxSamples.set {
		params.BridgeMaxSamples = o.bridgeMaxSamples.value
	}
	params.NoBridge = o.noBridge
	return params, params.Validate()
}

func (o *options) variantOverride() (djiproto.Variant, error) {
	if o.variant == "" || o.variant == "auto" {
		return "", nil
	}
	variant := djiproto.Variant(o.variant)
	if _, ok := variant.Path(); !ok {
		return "", fmt.Errorf("unknown variant %q (want wm169, wa530, oq101 or auto)", o.variant)
	}
	return variant, nil
}

func (o *options) validateCommon() error {
	if o.strength < 0 || o.strength > 1 || math.IsNaN(o.strength) {
		return fmt.Errorf("strength %g is outside the range 0-1", o.strength)
	}
	if o.smoothingMS < 0 || math.IsInf(o.smoothingMS, 0) || math.IsNaN(o.smoothingMS) {
		return fmt.Errorf("smoothing-ms must be a finite non-negative value, got %g", o.smoothingMS)
	}
	if o.jobs < 1 {
		return fmt.Errorf("jobs must be at least 1, got %d", o.jobs)
	}
	switch o.format {
	case "", "text", "json", "edl", "csv":
	default:
		return fmt.Errorf("unknown format %q (want text, json, edl or csv)", o.format)
	}
	return nil
}

// optionalFloat and optionalInt are flags that remember whether they were set,
// so a profile preset can stand in when they were not.
//
// The alternative — a sentinel like -1 meaning "unset" — leaks into the help
// text, because the flag package prints the actual default alongside the usage
// string and the reader sees "(default 3) (default -1)". Returning an empty
// String() makes the flag package treat the value as zero and print no default
// at all, leaving the usage string to say where the real one comes from. It
// also keeps zero itself a usable value: --min-severity 0 means zero, not unset.
type optionalFloat struct {
	value float64
	set   bool
}

func (o *optionalFloat) String() string { return "" }

func (o *optionalFloat) Set(text string) error {
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return err
	}
	o.value, o.set = value, true
	return nil
}

type optionalInt struct {
	value int
	set   bool
}

func (o *optionalInt) String() string { return "" }

func (o *optionalInt) Set(text string) error {
	value, err := strconv.Atoi(text)
	if err != nil {
		return err
	}
	o.value, o.set = value, true
	return nil
}

// interval is one closed time range to correct.
type interval struct{ start, end float64 }

// parseRanges reads the --ranges argument. Ranges are merged, so overlapping
// user input cannot silently widen or double-process a region.
func parseRanges(text string) ([]interval, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var intervals []interval
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		halves := strings.Split(part, "-")
		if len(halves) != 2 {
			return nil, fmt.Errorf("range %q must be START-END", part)
		}
		start, err := parseTime(halves[0])
		if err != nil {
			return nil, fmt.Errorf("range %q: %w", part, err)
		}
		end, err := parseTime(halves[1])
		if err != nil {
			return nil, fmt.Errorf("range %q: %w", part, err)
		}
		if end <= start {
			return nil, fmt.Errorf("range %q: end must be after start", part)
		}
		intervals = append(intervals, interval{start, end})
	}
	if len(intervals) == 0 {
		return nil, fmt.Errorf("no usable ranges in %q", text)
	}
	return mergeIntervals(intervals), nil
}

func mergeIntervals(intervals []interval) []interval {
	sorted := append([]interval(nil), intervals...)
	for index := 1; index < len(sorted); index++ {
		for position := index; position > 0 && sorted[position].start < sorted[position-1].start; position-- {
			sorted[position], sorted[position-1] = sorted[position-1], sorted[position]
		}
	}
	merged := sorted[:0]
	for _, current := range sorted {
		if len(merged) > 0 && current.start <= merged[len(merged)-1].end {
			if current.end > merged[len(merged)-1].end {
				merged[len(merged)-1].end = current.end
			}
			continue
		}
		merged = append(merged, current)
	}
	return append([]interval(nil), merged...)
}

// parseTime accepts seconds ("22.5") or clock time ("00:00:22.500", "1:02.5"),
// ported from the reference so --ranges values behave identically.
func parseTime(value string) (float64, error) {
	text := strings.ReplaceAll(strings.TrimSpace(value), ",", ".")
	if text == "" {
		return 0, fmt.Errorf("a time is required")
	}
	parts := strings.Split(text, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("invalid time format %q", value)
	}
	numbers := make([]float64, len(parts))
	for index, part := range parts {
		number, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid time format %q", value)
		}
		if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			return 0, fmt.Errorf("times must be finite and non-negative, got %q", value)
		}
		numbers[index] = number
	}
	for _, number := range numbers[1:] {
		if number >= 60 {
			return 0, fmt.Errorf("minutes and seconds must be below 60 in %q", value)
		}
	}
	seconds := 0.0
	for _, number := range numbers {
		seconds = seconds*60 + number
	}
	return seconds, nil
}
