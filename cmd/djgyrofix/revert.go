package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/steamvogue/djgyrofix/internal/mp4"
	"github.com/steamvogue/djgyrofix/internal/patch"
)

// runRevert restores original bytes from the sidecar journal.
func runRevert(args []string) error {
	flags := flag.NewFlagSet("revert", flag.ExitOnError)
	keep := flags.Bool("keep-journal", false, "leave the journal in place after reverting")
	force := flags.Bool("force", false, "revert even when the file no longer matches the journal")
	flags.Usage = commandUsage(flags,
		"undo a patch",
		"djgyrofix revert [flags] <file...>",
		`Restores the original bytes recorded in the sidecar journal, then removes the
journal. The file is checked against the journal first and the revert is
refused if it no longer matches, unless --force is given.`,
		[]flagGroup{{"Flags", revertFlagNames}},
		[][2]string{
			{"djgyrofix revert DJI_0042.MP4", "restore and drop the journal"},
			{"djgyrofix revert --keep-journal DJI_0042.MP4", "restore but keep the record"},
			{"djgyrofix revert --force DJI_0042.MP4", "repair a half-written patch"},
		})
	if err := flags.Parse(args); err != nil {
		return err
	}
	paths := flags.Args()
	if len(paths) == 0 {
		flags.Usage()
		return fmt.Errorf("no input files")
	}

	var failures []error
	for _, path := range paths {
		if err := revertOne(path, *keep, *force); err != nil {
			failures = append(failures, err)
		}
	}
	return summarize(failures)
}

func revertOne(path string, keep, force bool) error {
	journalPath := patch.JournalPath(path)
	journal, err := patch.LoadJournal(journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s: no journal at %s — nothing to revert", path, journalPath)
		}
		return err
	}

	spans, spanErr := metadataSpans(path)
	if spanErr == nil {
		verdict, err := patch.Verify(path, journal, spans)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !verdict.OK() && !force {
			return fmt.Errorf(
				"%s does not match its journal (%s); the file changed underneath it. "+
					"Inspect with `djgyrofix verify`, or pass --force to revert anyway",
				path, describeMismatch(verdict))
		}
	}

	written, err := patch.Revert(path, journal)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	restored := "restored"
	if spanErr == nil && journal.MetadataDigest != "" {
		switch digest, err := metadataDigestOf(path, spans); {
		case err != nil:
			restored = "restored, but the metadata digest could not be recomputed"
		case digest == journal.MetadataDigest:
			restored = "restored, matches original digest"
		default:
			restored = "restored, but the metadata digest does not match the journal"
		}
	}
	fmt.Printf("%s: %s — %d bytes (%d writes)\n", path, restored, written*4, written)

	if !keep {
		if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// metadataSpans locates the djmd sample byte ranges, which the digest covers.
func metadataSpans(path string) ([]patch.SampleSpan, error) {
	tracks, err := mp4.ParseFile(path)
	if err != nil {
		return nil, err
	}
	track, err := mp4.FindDJIMetadataTrack(tracks)
	if err != nil {
		return nil, err
	}
	spans := make([]patch.SampleSpan, track.SampleCount())
	for index := range spans {
		spans[index] = patch.SampleSpan{Offset: track.SampleOffsets[index], Size: track.SampleSizes[index]}
	}
	return spans, nil
}

// metadataDigestOf hashes the djmd sample bytes of a file as they stand.
func metadataDigestOf(path string, spans []patch.SampleSpan) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return patch.MetadataDigest(file, spans, nil)
}

func describeMismatch(verdict patch.VerifyReport) string {
	switch {
	case !verdict.SizeOK:
		return "file size differs from the journal"
	case verdict.BytesMismatched > 0:
		return fmt.Sprintf("%d patched ranges hold unexpected bytes, first at offset %d",
			verdict.BytesMismatched, verdict.FirstMismatch)
	default:
		return "metadata digest differs"
	}
}
