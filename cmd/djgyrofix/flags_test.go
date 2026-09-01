package main

import (
	"math"
	"testing"
)

func TestParseTimeAcceptsSecondsAndClockValues(t *testing.T) {
	cases := map[string]float64{
		"22.5":         22.5,
		"00:00:22.500": 22.5,
		"1:02.5":       62.5,
		"1:00:00":      3600,
		"0":            0,
		"22,5":         22.5, // comma decimal separator, as the reference allows
		" 12.5 ":       12.5,
	}
	for input, want := range cases {
		got, err := parseTime(input)
		if err != nil {
			t.Errorf("parseTime(%q): %v", input, err)
			continue
		}
		if math.Abs(got-want) > 1e-12 {
			t.Errorf("parseTime(%q) = %g, want %g", input, got, want)
		}
	}
}

func TestParseTimeRejectsNonsense(t *testing.T) {
	// A clock component of 60 or more is a typo, not a time. Accepting it would
	// silently shift the range the user meant.
	for _, input := range []string{"", "nan", "inf", "1:60", "1:-1", "-3", "a:b", "1:2:3:4", "1..2"} {
		if got, err := parseTime(input); err == nil {
			t.Errorf("parseTime(%q) = %g, want an error", input, got)
		}
	}
}

func TestParseRangesMergesAndValidates(t *testing.T) {
	got, err := parseRanges("12.5-14.0,61-62.25")
	if err != nil {
		t.Fatal(err)
	}
	want := []interval{{12.5, 14.0}, {61, 62.25}}
	if len(got) != len(want) {
		t.Fatalf("got %d ranges, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("range %d = %v, want %v", index, got[index], want[index])
		}
	}

	// Overlapping input must merge, so a region cannot be processed twice.
	merged, err := parseRanges("1-3,2-5,10-11")
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 2 || merged[0] != (interval{1, 5}) || merged[1] != (interval{10, 11}) {
		t.Errorf("overlapping ranges merged to %v", merged)
	}

	// Unsorted input must sort.
	sorted, err := parseRanges("9-10,1-2")
	if err != nil {
		t.Fatal(err)
	}
	if sorted[0] != (interval{1, 2}) {
		t.Errorf("unsorted ranges were not ordered: %v", sorted)
	}

	if got, err := parseRanges(""); err != nil || got != nil {
		t.Errorf("an empty --ranges should mean automatic detection, got %v, %v", got, err)
	}
}

func TestParseRangesRejectsBadInput(t *testing.T) {
	for _, input := range []string{"14.0-12.5", "5-5", "12.5", "1-2-3", ",", "abc-def"} {
		if got, err := parseRanges(input); err == nil {
			t.Errorf("parseRanges(%q) = %v, want an error", input, got)
		}
	}
}

func TestClockTimestampsRoundTripThroughRanges(t *testing.T) {
	got, err := parseRanges("00:01:12.480-00:01:13.000")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || math.Abs(got[0].start-72.480) > 1e-9 || math.Abs(got[0].end-73.0) > 1e-9 {
		t.Errorf("clock range parsed as %v", got)
	}
}

func TestDetectParamsFoldProfileAndOverrides(t *testing.T) {
	opts := defaultOptions()
	opts.profile = "conservative"
	params, err := opts.detectParams()
	if err != nil {
		t.Fatal(err)
	}
	if params.Profile != "conservative" || params.MADK != 7.0 {
		t.Errorf("conservative preset not applied: %+v", params)
	}

	// An explicit flag must win over the preset.
	opts.madK = 2.5
	opts.floorDPS = 10
	opts.minSeverity = optionalFloat{value: 1, set: true}
	params, err = opts.detectParams()
	if err != nil {
		t.Fatal(err)
	}
	if params.MADK != 2.5 || params.FloorDPS != 10 || params.MinSeverity != 1 {
		t.Errorf("explicit overrides were ignored: %+v", params)
	}

	// min-severity 0 must stay distinguishable from "not set".
	opts.minSeverity = optionalFloat{value: 0, set: true}
	params, err = opts.detectParams()
	if err != nil {
		t.Fatal(err)
	}
	if params.MinSeverity != 0 {
		t.Errorf("--min-severity 0 was treated as unset, got %g", params.MinSeverity)
	}

	opts.profile = "nonsense"
	if _, err := opts.detectParams(); err == nil {
		t.Error("an unknown profile was accepted")
	}
}

func TestValidateCommonRejectsBadFlags(t *testing.T) {
	for name, mutate := range map[string]func(*options){
		"negative strength": func(o *options) { o.strength = -0.1 },
		"strength above 1":  func(o *options) { o.strength = 1.5 },
		"nan strength":      func(o *options) { o.strength = math.NaN() },
		"negative smooth":   func(o *options) { o.smoothingMS = -5 },
		"zero jobs":         func(o *options) { o.jobs = 0 },
		"unknown format":    func(o *options) { o.format = "yaml" },
	} {
		t.Run(name, func(t *testing.T) {
			opts := defaultOptions()
			mutate(opts)
			if err := opts.validateCommon(); err == nil {
				t.Error("accepted invalid flags")
			}
		})
	}
}

func TestVariantOverrideParsing(t *testing.T) {
	opts := defaultOptions()
	for _, name := range []string{"auto", ""} {
		opts.variant = name
		if got, err := opts.variantOverride(); err != nil || got != "" {
			t.Errorf("variant %q gave (%q, %v), want no override", name, got, err)
		}
	}
	for _, name := range []string{"wm169", "wa530", "oq101"} {
		opts.variant = name
		if got, err := opts.variantOverride(); err != nil || string(got) != name {
			t.Errorf("variant %q gave (%q, %v)", name, got, err)
		}
	}
	opts.variant = "mavic"
	if _, err := opts.variantOverride(); err == nil {
		t.Error("an unknown variant was accepted")
	}
}
