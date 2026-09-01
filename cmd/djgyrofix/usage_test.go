package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// commands returns each subcommand's configured flag set, by running the same
// registration the real entry points do.
func commands() map[string]*flag.FlagSet {
	sets := map[string]*flag.FlagSet{}

	scan := flag.NewFlagSet("scan", flag.ContinueOnError)
	scanOpts := &options{}
	scanOpts.registerDetection(scan)
	scanOpts.registerCorrection(scan)
	scanOpts.registerIO(scan, false)
	sets["scan"] = scan

	fix := flag.NewFlagSet("fix", flag.ContinueOnError)
	fixOpts := &options{}
	fixOpts.registerDetection(fix)
	fixOpts.registerCorrection(fix)
	fixOpts.registerIO(fix, true)
	sets["fix"] = fix

	return sets
}

// TestEveryFlagAppearsInExactlyOneGroup keeps the static help groups honest.
// They are lists of names, so a flag added to registerCorrection without a
// matching entry would silently fall into the catch-all "Other" block, and a
// name removed from registration would leave a dead entry behind.
func TestEveryFlagAppearsInExactlyOneGroup(t *testing.T) {
	grouped := map[string]int{}
	for _, names := range [][]string{detectionFlagNames, correctionFlagNames, safetyFlagNames, outputFlagNames} {
		for _, name := range names {
			grouped[name]++
		}
	}
	for name, count := range grouped {
		if count > 1 {
			t.Errorf("flag %q appears in %d groups", name, count)
		}
	}

	fix := commands()["fix"]
	registered := map[string]bool{}
	fix.VisitAll(func(entry *flag.Flag) { registered[entry.Name] = true })

	for name := range registered {
		if grouped[name] == 0 {
			t.Errorf("flag -%s is registered on `fix` but is in no help group", name)
		}
	}
	for name := range grouped {
		if !registered[name] {
			t.Errorf("help group names -%s, which `fix` does not register", name)
		}
	}
}

// TestScanGroupsCoverItsOwnFlags checks the subset scan registers, which omits
// the safety group entirely because scan never writes.
func TestScanGroupsCoverItsOwnFlags(t *testing.T) {
	scan := commands()["scan"]
	covered := map[string]bool{}
	for _, names := range [][]string{detectionFlagNames, correctionFlagNames, outputFlagNames} {
		for _, name := range names {
			covered[name] = true
		}
	}
	scan.VisitAll(func(entry *flag.Flag) {
		if !covered[entry.Name] {
			t.Errorf("flag -%s is registered on `scan` but is in none of its help groups", entry.Name)
		}
	})
	for _, name := range safetyFlagNames {
		if scan.Lookup(name) != nil && name != "force" {
			t.Errorf("scan registers the write-side flag -%s; scan must never write", name)
		}
	}
}

func TestHelpListsEveryFlagAndAnExample(t *testing.T) {
	for name, flags := range commands() {
		t.Run(name, func(t *testing.T) {
			var buffer bytes.Buffer
			flags.SetOutput(&buffer)
			groups := []flagGroup{
				{"Detection", detectionFlagNames},
				{"Correction", correctionFlagNames},
				{"Safety", safetyFlagNames},
				{"Output", outputFlagNames},
			}
			commandUsage(flags, "summary", "usage line", "prose", groups,
				[][2]string{{"djgyrofix " + name + " clip.MP4", "an example"}})()
			output := buffer.String()

			flags.VisitAll(func(entry *flag.Flag) {
				if !strings.Contains(output, "  -"+entry.Name) {
					t.Errorf("help omits -%s\n%s", entry.Name, output)
				}
			})
			for _, want := range []string{"usage line", "prose", "Examples:", "an example"} {
				if !strings.Contains(output, want) {
					t.Errorf("help omits %q\n%s", want, output)
				}
			}
		})
	}
}

// TestHelpSuppressesZeroDefaults pins the borrowed flag-package rule: printing
// "(default false)" or "(default 0)" on every flag is noise, and printing a
// sentinel default on an optional flag is a lie.
func TestHelpSuppressesZeroDefaults(t *testing.T) {
	flags := commands()["fix"]
	var buffer bytes.Buffer
	flags.SetOutput(&buffer)
	commandUsage(flags, "s", "u", "", []flagGroup{
		{"Detection", detectionFlagNames},
		{"Correction", correctionFlagNames},
		{"Safety", safetyFlagNames},
		{"Output", outputFlagNames},
	}, nil)()
	output := buffer.String()

	for _, unwanted := range []string{"(default false)", "(default 0)", "(default 0s)", "(default -1)", `(default "")`} {
		if strings.Contains(output, unwanted) {
			t.Errorf("help prints the noise default %s\n%s", unwanted, output)
		}
	}
	// Meaningful defaults must still show.
	for _, want := range []string{`(default "balanced")`, "(default 1)", `(default "journal")`, "(default 0.15)"} {
		if !strings.Contains(output, want) {
			t.Errorf("help omits the real default %s\n%s", want, output)
		}
	}
}

// TestOrphanFlagsStillAppear guards the catch-all: a flag missing from every
// group must still be documented, or the help would understate what the
// command accepts.
func TestOrphanFlagsStillAppear(t *testing.T) {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.Bool("grouped", false, "in a group")
	flags.Bool("forgotten", false, "in no group")
	var buffer bytes.Buffer
	flags.SetOutput(&buffer)
	commandUsage(flags, "s", "u", "", []flagGroup{{"Group", []string{"grouped"}}}, nil)()
	output := buffer.String()
	if !strings.Contains(output, "Other:") || !strings.Contains(output, "-forgotten") {
		t.Errorf("an ungrouped flag was dropped from the help\n%s", output)
	}
}
