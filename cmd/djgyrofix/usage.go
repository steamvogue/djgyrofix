package main

import (
	"flag"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

// The flag package prints every flag in one alphabetical list. For `fix` that
// is twenty entries with detection, correction and safety knobs interleaved,
// which tells a reader nothing about which ones are dangerous. These groups
// restore that structure.
//
// The lists are static, so they can drift from what is actually registered.
// TestEveryFlagAppearsInExactlyOneGroup fails if they do.
var (
	detectionFlagNames  = []string{"profile", "style", "sensitivity", "mad-k", "baseline-window", "floor-dps", "min-severity", "imu-full-scale", "auto"}
	correctionFlagNames = []string{"strength", "smoothing-ms", "bridge-max-samples", "no-bridge", "ranges"}
	safetyFlagNames     = []string{"dry-run", "apply", "backup", "out", "force", "max-affected"}
	outputFlagNames     = []string{"variant", "jobs", "format"}
	revertFlagNames     = []string{"keep-journal", "force"}
	infoFlagNames       = []string{"variant", "all-variants"}
)

// flagGroup is one titled block of flags in a help listing.
type flagGroup struct {
	title string
	names []string
}

// commandUsage renders a command's help: a one-line summary, the usage line, a
// paragraph of prose, grouped flags, then worked examples.
func commandUsage(flags *flag.FlagSet, summary, usageLine, prose string, groups []flagGroup, examples [][2]string) func() {
	return func() {
		// Rendered into a builder and flushed once, so help never appears
		// half-written and the individual writes cannot fail.
		out := &strings.Builder{}
		write(out, "djgyrofix %s — %s\n\n", flags.Name(), summary)
		write(out, "usage: %s\n", usageLine)
		if prose != "" {
			write(out, "\n%s\n", strings.TrimSpace(prose))
		}

		printed := map[string]bool{}
		for _, group := range groups {
			var block []*flag.Flag
			for _, name := range group.names {
				if found := flags.Lookup(name); found != nil && !printed[name] {
					block = append(block, found)
					printed[name] = true
				}
			}
			if len(block) == 0 {
				continue
			}
			write(out, "\n%s:\n", group.title)
			for _, entry := range block {
				printFlag(out, entry)
			}
		}

		// Anything a group forgot still has to appear, or the help would lie
		// about what the command accepts.
		var orphans []*flag.Flag
		flags.VisitAll(func(entry *flag.Flag) {
			if !printed[entry.Name] {
				orphans = append(orphans, entry)
			}
		})
		if len(orphans) > 0 {
			sort.Slice(orphans, func(a, b int) bool { return orphans[a].Name < orphans[b].Name })
			write(out, "\nOther:\n")
			for _, entry := range orphans {
				printFlag(out, entry)
			}
		}

		if len(examples) > 0 {
			write(out, "\nExamples:\n")
			width := 0
			for _, example := range examples {
				if len(example[0]) > width {
					width = len(example[0])
				}
			}
			for _, example := range examples {
				write(out, "  %-*s  %s\n", width, example[0], example[1])
			}
		}
		// The one real write. Help going nowhere is not something flag.Usage
		// can report or the caller can act on.
		_, _ = io.WriteString(flags.Output(), out.String())
	}
}

// write appends formatted text to a strings.Builder, whose Write is documented
// never to fail.
func write(out *strings.Builder, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}

// printFlag reproduces the flag package's own two-line layout, including its
// rule for when a default is worth printing, so the grouped help still looks
// native rather than reinvented.
func printFlag(out *strings.Builder, entry *flag.Flag) {
	name, usage := flag.UnquoteUsage(entry)
	line := "  -" + entry.Name
	if name != "" {
		line += " " + name
	}
	write(out, "%s\n", line)
	write(out, "    \t%s", strings.ReplaceAll(usage, "\n", "\n    \t"))
	if !isZeroValue(entry) {
		// String defaults are quoted, as the flag package quotes them, so an
		// empty or space-bearing default is visible rather than invisible.
		if getter, ok := entry.Value.(flag.Getter); ok {
			if _, isString := getter.Get().(string); isString {
				write(out, " (default %q)\n", entry.DefValue)
				return
			}
		}
		write(out, " (default %v)", entry.DefValue)
	}
	write(out, "\n")
}

// isZeroValue reports whether a flag's default is its type's zero value, which
// the flag package treats as not worth printing. This is its own logic, lifted
// so grouped help and ungrouped help agree.
//
// It is also what keeps the optional flags quiet: optionalFloat and optionalInt
// return an empty String() precisely so no misleading sentinel default appears,
// and their usage text names the real source instead.
func isZeroValue(entry *flag.Flag) bool {
	typ := reflect.TypeOf(entry.Value)
	var zero reflect.Value
	if typ.Kind() == reflect.Pointer {
		zero = reflect.New(typ.Elem())
	} else {
		zero = reflect.Zero(typ)
	}
	value, ok := zero.Interface().(flag.Value)
	if !ok {
		return entry.DefValue == ""
	}
	return entry.DefValue == value.String()
}
