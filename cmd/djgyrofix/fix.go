package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/steamvogue/djgyrofix/internal/patch"
	"github.com/steamvogue/djgyrofix/internal/report"
)

// runFix analyzes and patches. Without --apply it computes everything and
// writes nothing, which is the default on purpose: this tool edits originals in
// place, and a dry run is the cheapest way to find out that detection has
// flagged something it should not have.
func runFix(args []string) error {
	flags := flag.NewFlagSet("fix", flag.ExitOnError)
	opts := &options{}
	opts.registerDetection(flags)
	opts.registerCorrection(flags)
	opts.registerIO(flags, true)
	flags.Usage = commandUsage(flags,
		"analyze and patch",
		"djgyrofix fix [flags] <file...>",
		`Patches the DJI metadata track in place. The whole patch is computed before
the file is opened for writing, and nothing outside a djmd sample payload is
ever touched.

Without --apply this is a dry run: it reports exactly what it would change and
writes nothing. That is the default because this command edits originals.

Every applied patch writes a sidecar journal next to the video, so
"djgyrofix revert" restores the original bytes exactly.`,
		[]flagGroup{
			{"Detection", detectionFlagNames},
			{"Correction", correctionFlagNames},
			{"Safety", safetyFlagNames},
			{"Output", outputFlagNames},
		},
		[][2]string{
			{"djgyrofix fix DJI_0042.MP4", "dry run — report, write nothing"},
			{"djgyrofix fix --apply DJI_0042.MP4", "patch in place, journal alongside"},
			{"djgyrofix fix --auto --apply DJI_0042.MP4", "let it pick the profile, or refuse the clip"},
			{"djgyrofix fix --apply --backup copy DJI_0042.MP4", "also keep a full .orig copy"},
			{"djgyrofix fix --apply --out fixed.MP4 DJI_0042.MP4", "leave the original untouched"},
			{"djgyrofix fix --apply --ranges 12.5-14.0 clip.MP4", "skip detection, smooth one window"},
			{"djgyrofix fix --apply --jobs 8 DJI_*.MP4", "batch a folder in parallel"},
		})
	if err := flags.Parse(args); err != nil {
		return err
	}
	opts.markExplicit(flags)
	if err := opts.validateCommon(); err != nil {
		return err
	}
	if opts.apply && opts.dryRun {
		return errors.New("--apply and --dry-run contradict each other")
	}
	switch opts.backup {
	case "journal", "copy", "none":
	default:
		return fmt.Errorf("unknown backup mode %q (want journal, copy or none)", opts.backup)
	}
	if opts.backup == "none" && opts.apply && !opts.force && opts.out == "" {
		return errors.New("--backup none leaves no way to undo an in-place patch; add --force to accept that")
	}
	paths := flags.Args()
	if len(paths) == 0 {
		flags.Usage()
		return fmt.Errorf("no input files")
	}
	if opts.out != "" && len(paths) > 1 {
		return errors.New("--out names a single file, so it cannot be combined with multiple inputs")
	}
	intervals, err := parseRanges(opts.ranges)
	if err != nil {
		return err
	}

	reports, failures := forEachFile(paths, opts.jobs, func(path string) (report.Report, error) {
		return fixOne(path, opts, intervals)
	})
	if err := report.Write(os.Stdout, reports, opts.format); err != nil {
		return err
	}
	return summarize(failures)
}

func fixOne(path string, opts *options, intervals []interval) (report.Report, error) {
	journalPath := patch.JournalPath(path)
	if opts.out != "" {
		journalPath = patch.JournalPath(opts.out)
	}

	// Idempotency guard: a file that already carries a journal has been
	// patched. Smoothing already-smoothed data compounds the correction and
	// the second journal's "old" bytes would no longer be the true original.
	if existing, err := patch.LoadJournal(journalPath); err == nil {
		if !opts.force {
			return report.Report{}, fmt.Errorf(
				"%s is already patched (journal %s); revert it first, or pass --force to revert and re-apply",
				path, journalPath)
		}
		if opts.apply && opts.out == "" {
			if _, err := patch.Revert(path, existing); err != nil {
				return report.Report{}, fmt.Errorf("%s: reverting the existing patch before re-applying: %w", path, err)
			}
			if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
				return report.Report{}, err
			}
		}
	} else if !os.IsNotExist(err) && !errors.Is(err, os.ErrNotExist) {
		// A journal that exists but will not parse is a stop sign, not noise.
		if _, statErr := os.Stat(journalPath); statErr == nil {
			return report.Report{}, fmt.Errorf("%s: %w", journalPath, err)
		}
	}

	result, err := analyze(path, opts, intervals)
	if err != nil {
		return report.Report{}, err
	}
	result.report.DryRun = !opts.apply

	// Autopilot refusing is not the same as detection over-flagging: it means
	// no profile in the set can help this clip, so there is nothing to raise a
	// limit for. It still yields to --force, because the events it found are
	// real even when they are not the cause of the shake.
	if result.report.Auto != nil && result.report.Auto.Refused {
		reason := "autopilot refused"
		if steps := result.report.Auto.Steps; len(steps) > 0 {
			reason = steps[len(steps)-1]
		}
		if !opts.force {
			return report.Report{}, fmt.Errorf("%s: %s. Pass --force to patch anyway, "+
				"or drop --auto and choose a profile yourself", path, reason)
		}
		result.report.Warnings = append(result.report.Warnings, reason+" (accepted via --force)")
	}

	if intervals == nil && result.report.AffectedFraction > opts.maxAffected {
		message := fmt.Sprintf(
			"detection flagged %.1f%% of the clip, over the --max-affected limit of %.1f%%",
			result.report.AffectedFraction*100, opts.maxAffected*100)
		if !opts.force {
			return report.Report{}, fmt.Errorf(
				"%s: %s. That is the signature of a bad baseline or genuinely rough footage — "+
					"blanket smoothing there degrades stabilization everywhere. "+
					"Raise --max-affected, lower --sensitivity, or pass --force", path, message)
		}
		result.report.Warnings = append(result.report.Warnings, message+" (accepted via --force)")
	}

	if !opts.apply {
		finalizeReport(&result.report, opts, "fix")
		return result.report, nil
	}
	if len(result.writes) == 0 {
		// Nothing to undo means nothing to record. Writing a journal here
		// would make the file look patched to the idempotency guard.
		finalizeReport(&result.report, opts, "fix")
		return result.report, nil
	}
	if err := applyPatch(path, journalPath, opts, result); err != nil {
		return report.Report{}, fmt.Errorf("%s: %w", path, err)
	}
	result.report.Applied = true
	finalizeReport(&result.report, opts, "fix")
	return result.report, nil
}

// applyPatch performs the write half, in the order that keeps a half-finished
// run recoverable: copy first if asked, journal to disk and fsynced second,
// video third.
func applyPatch(path, journalPath string, opts *options, result *analysis) error {
	target := path
	if opts.out != "" {
		if _, err := os.Stat(opts.out); err == nil && !opts.force {
			return fmt.Errorf("output %s already exists; pass --force to overwrite", opts.out)
		} else if err == nil {
			if err := os.Remove(opts.out); err != nil {
				return err
			}
		}
		if err := patch.CopyFile(path, opts.out); err != nil {
			return fmt.Errorf("copying to %s: %w", opts.out, err)
		}
		target = opts.out
		result.report.OutputPath = opts.out
	} else if opts.backup == "copy" {
		backupPath := path + ".orig"
		if _, err := os.Stat(backupPath); err == nil {
			if !opts.force {
				return fmt.Errorf("backup %s already exists; pass --force to overwrite", backupPath)
			}
			if err := os.Remove(backupPath); err != nil {
				return err
			}
		}
		if err := patch.CopyFile(path, backupPath); err != nil {
			return fmt.Errorf("writing backup %s: %w", backupPath, err)
		}
		result.report.BackupPath = backupPath
	}

	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	handle, err := os.Open(target)
	if err != nil {
		return err
	}
	digest, err := patch.MetadataDigest(handle, result.spans, nil)
	handle.Close()
	if err != nil {
		return err
	}

	if opts.backup != "none" {
		journal := patch.NewJournal(ToolName, info, path)
		journal.Track = patch.TrackInfo{
			Variant:   result.report.Variant,
			Timescale: result.report.Timescale,
			Samples:   result.samples,
		}
		journal.MetadataDigest = digest
		journal.Params = result.params
		journal.Events = result.events
		journal.Writes = result.writes
		if err := patch.SaveJournal(journalPath, journal); err != nil {
			return fmt.Errorf("writing journal %s: %w", journalPath, err)
		}
		result.report.JournalPath = journalPath
	}

	written, err := patch.Apply(target, result.writes, info.Size())
	if err != nil {
		return err
	}
	if written != len(result.writes) {
		return fmt.Errorf("wrote %d of %d planned patches", written, len(result.writes))
	}
	after, err := os.Stat(target)
	if err != nil {
		return err
	}
	if after.Size() != info.Size() {
		return fmt.Errorf("%w: %d bytes, expected %d", patch.ErrSizeChanged, after.Size(), info.Size())
	}
	return nil
}
