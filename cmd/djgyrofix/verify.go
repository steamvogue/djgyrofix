package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/steamvogue/djgyrofix/internal/patch"
)

// runVerify checks patched files against their journals.
func runVerify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ExitOnError)
	flags.Usage = commandUsage(flags,
		"check a patched file",
		"djgyrofix verify <file...>",
		`Checks a patched file against its journal: the size is unchanged, every
patched range holds the value the journal recorded, and reversing the writes in
memory reproduces the pre-patch metadata digest.

A file with some ranges patched and some not is an interrupted write, and the
journal still holds every original byte — "revert --force" repairs it.`,
		nil,
		[][2]string{
			{"djgyrofix verify DJI_0042.MP4", "check one patched clip"},
			{"djgyrofix verify DJI_*.MP4", "check a whole folder"},
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
		if err := verifyOne(path); err != nil {
			failures = append(failures, err)
		}
	}
	return summarize(failures)
}

func verifyOne(path string) error {
	journalPath := patch.JournalPath(path)
	journal, err := patch.LoadJournal(journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s: no journal at %s", path, journalPath)
		}
		return err
	}
	spans, err := metadataSpans(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	verdict, err := patch.Verify(path, journal, spans)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	fmt.Printf("%s\n", path)
	fmt.Printf("  journal:   %s (%s, %s)\n", journalPath, journal.Tool, journal.Created)
	fmt.Printf("  size:      %s (%d bytes)\n", passFail(verdict.SizeOK), journal.Source.Size)
	fmt.Printf("  patches:   %d of %d ranges hold the expected bytes\n",
		verdict.BytesMatched, verdict.BytesMatched+verdict.BytesMismatched)
	fmt.Printf("  digest:    %s\n", passFail(verdict.DigestOK))
	if !verdict.DigestOK {
		fmt.Printf("    expected %s\n    actual   %s\n", verdict.DigestExpected, verdict.DigestActual)
	}
	if verdict.OK() {
		fmt.Printf("  verdict:   intact\n")
		return nil
	}

	// A file with some ranges patched and some not is the signature of an
	// interrupted write. The journal still holds every original byte, so this
	// is repairable rather than lost.
	if verdict.BytesMismatched > 0 && verdict.BytesMatched > 0 {
		fmt.Printf("  verdict:   partially patched — run `djgyrofix revert --force %s` to restore\n", path)
	} else {
		fmt.Printf("  verdict:   does not match the journal (%s)\n", describeMismatch(verdict))
	}
	return fmt.Errorf("%s failed verification", path)
}

func passFail(ok bool) string {
	if ok {
		return "ok"
	}
	return "MISMATCH"
}
