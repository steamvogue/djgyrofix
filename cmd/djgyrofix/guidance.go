package main

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/steamvogue/djgyrofix/internal/advise"
	"github.com/steamvogue/djgyrofix/internal/report"
)

// finalizeReport adds the command context that analysis deliberately does not
// know. Detection decides which knob is justified; the CLI knows whether the
// user scanned, dry-ran, patched in place or wrote an --out copy, and can turn
// that knob into a safe command sequence without dropping their current flags.
func finalizeReport(rep *report.Report, opts *options, operation string) {
	rep.Operation = operation
	if rep.Advice == nil {
		return
	}

	diagnosis := rep.Advice
	diagnosis.PreviewFile = rep.File
	if opts.out != "" {
		diagnosis.PreviewFile = opts.out
	} else if rep.OutputPath != "" {
		diagnosis.PreviewFile = rep.OutputPath
	}

	apply := diagnosis.Verdict == advise.VerdictPatch
	if apply && !rep.Applied {
		diagnosis.NextCommand = fixCommand(opts, rep.File, "", true, false)
	} else if rep.Applied {
		diagnosis.NextCommand = ""
	}

	// An in-place retry must start by restoring the original. A copy made with
	// --out can instead be regenerated from its untouched source; --force then
	// means only "replace that derived copy".
	canRevert := opts.out == "" && opts.backup != "none"
	if rep.Applied {
		canRevert = opts.out == "" && rep.JournalPath != ""
	}
	if apply && canRevert {
		diagnosis.RevertCommand = "djgyrofix revert "
		if strings.HasPrefix(rep.File, "-") {
			diagnosis.RevertCommand += "-- "
		}
		diagnosis.RevertCommand += commandArg(rep.File)
	}

	for index := range diagnosis.Suggestions {
		suggestion := &diagnosis.Suggestions[index]
		if suggestion.Flags == advise.NoFlag {
			continue
		}
		overwriteCopy := apply && opts.out != ""
		suggestion.Command = fixCommand(opts, rep.File, suggestion.Flags, apply, overwriteCopy)
		if apply && opts.out == "" && !canRevert {
			// Re-running this against the now-patched source would compound the
			// correction. With no journal, the only safe retry starts elsewhere.
			suggestion.Command = ""
		}
	}
}

// fixCommand preserves every setting that changes detection or correction,
// then replaces the one flag a suggestion is explicitly about. Report-only
// flags such as --format and --jobs are intentionally omitted from the
// single-file command shown to the user.
func fixCommand(opts *options, path, suggestion string, apply, overwriteCopy bool) string {
	profile := opts.profile
	style := opts.style
	sensitivity := opts.sensitivity
	smoothingMS := opts.smoothingMS
	maxAffected := opts.maxAffected
	noBridge := opts.noBridge
	auto := opts.auto

	parts := strings.Fields(suggestion)
	if len(parts) > 0 {
		switch parts[0] {
		case "--profile":
			if len(parts) == 2 {
				profile = parts[1]
				auto = false
			}
		case "--sensitivity":
			if len(parts) == 2 {
				if value, err := strconv.ParseFloat(parts[1], 64); err == nil {
					sensitivity = value
				}
			}
		case "--smoothing-ms":
			if len(parts) == 2 {
				if value, err := strconv.ParseFloat(parts[1], 64); err == nil {
					smoothingMS = value
				}
			}
		case "--max-affected":
			if len(parts) == 2 {
				if value, err := strconv.ParseFloat(parts[1], 64); err == nil {
					maxAffected = value
				}
			}
		case "--no-bridge":
			noBridge = true
		}
	}

	args := []string{"djgyrofix", "fix"}
	if apply {
		args = append(args, "--apply")
	}
	if profile != "" && profile != "balanced" {
		args = append(args, "--profile", profile)
	}
	if style != "" && style != "normal" {
		args = append(args, "--style", style)
	}
	if sensitivity != 0 && sensitivity != 1 {
		args = append(args, "--sensitivity", numberArg(sensitivity))
	}
	if opts.madK > 0 {
		args = append(args, "--mad-k", numberArg(opts.madK))
	}
	if opts.baselineWindow > 0 {
		args = append(args, "--baseline-window", opts.baselineWindow.String())
	}
	if opts.floorDPS > 0 {
		args = append(args, "--floor-dps", numberArg(opts.floorDPS))
	}
	if opts.minSeverity.set {
		args = append(args, "--min-severity", numberArg(opts.minSeverity.value))
	}
	if opts.imuFullScale > 0 {
		args = append(args, "--imu-full-scale", numberArg(opts.imuFullScale))
	}
	if opts.variant != "" && opts.variant != "auto" {
		args = append(args, "--variant", opts.variant)
	}
	if auto {
		args = append(args, "--auto")
	}
	if opts.repair != "" && opts.repair != "runs" {
		args = append(args, "--repair", opts.repair)
	}
	if opts.strength != 1 {
		args = append(args, "--strength", numberArg(opts.strength))
	}
	if smoothingMS > 0 {
		args = append(args, "--smoothing-ms", numberArg(smoothingMS))
	}
	if opts.bridgeMaxSamples.set {
		args = append(args, "--bridge-max-samples", strconv.Itoa(opts.bridgeMaxSamples.value))
	}
	if noBridge {
		args = append(args, "--no-bridge")
	}
	if opts.ranges != "" {
		args = append(args, "--ranges", commandArg(opts.ranges))
	}
	if opts.backup != "" && opts.backup != "journal" {
		args = append(args, "--backup", opts.backup)
	}
	if opts.out != "" {
		args = append(args, "--out", commandArg(opts.out))
	}
	if maxAffected != 0.15 {
		args = append(args, "--max-affected", numberArg(maxAffected))
	}
	if opts.force || overwriteCopy {
		args = append(args, "--force")
	}
	if strings.HasPrefix(path, "-") {
		args = append(args, "--")
	}
	args = append(args, commandArg(path))
	return strings.Join(args, " ")
}

func numberArg(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// commandArg leaves ordinary paths readable and quotes anything a shell could
// split or interpret. Double quotes work in the common Windows and POSIX
// shells used to run the released binaries; embedded shell metacharacters are
// escaped for the latter.
func commandArg(value string) string {
	if value != "" {
		safe := true
		for _, character := range value {
			if !unicode.IsLetter(character) && !unicode.IsDigit(character) && !strings.ContainsRune("_./:-\\", character) {
				safe = false
				break
			}
		}
		if safe {
			return value
		}
	}
	replacer := strings.NewReplacer("\"", "\\\"", "$", "\\$", "`", "\\`")
	return "\"" + replacer.Replace(value) + "\""
}
