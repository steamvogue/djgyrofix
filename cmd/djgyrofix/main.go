// Command djgyrofix detects and corrects transient attitude deviations in
// the timed-metadata track of DJI MP4 and MOV files.
//
// The correction is a byte-level in-place patch: the same four-byte float slots
// are overwritten with filtered values. Sample sizes never change, so stsz,
// stco and co64 stay valid and the container is untouched. Nothing is
// re-encoded and nothing is remuxed — a remux would rewrite moov and invalidate
// every sample offset, and -c copy typically drops the private djmd track
// altogether.
//
// Copyright (C) 2026 djgyrofix contributors.
// Derived from kim2160/DJIGyroFix v0.92 (GPL-3.0).
//
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. See the LICENSE file for the full text.
package main

import (
	"fmt"
	"os"
)

// usage is a function rather than a constant because the version it names is
// resolved at run time from the build stamp — see version.go.
func usage() string {
	return `djgyrofix ` + Version + ` — fix DJI gyro metadata artifacts in place

usage: djgyrofix <command> [flags] <file...>

Detects and corrects transient attitude deviations in the timed-metadata track
of DJI MP4 and MOV files, so Gyroflow stops over-correcting on them.
The patch is byte-level and size-preserving: nothing is re-encoded or remuxed.

Commands:
  scan     analyze and report; never writes to the video
  fix      analyze and patch in place (or to --out); dry run without --apply
  revert   restore original bytes from the sidecar journal
  verify   check a patched file against its journal
  info     dump track, variant and sample-rate details

Typical use:
  djgyrofix scan DJI_0042.MP4                   see what is wrong
  djgyrofix fix --apply DJI_0042.MP4            patch it, journal alongside
  djgyrofix revert DJI_0042.MP4                 undo, byte for byte

Run "djgyrofix <command> -h" for the flags and examples of a command.
Report bugs at https://github.com/steamvogue/djgyrofix/issues
`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage())
		os.Exit(2)
	}
	command, args := os.Args[1], os.Args[2:]
	var err error
	switch command {
	case "scan":
		err = runScan(args)
	case "fix":
		err = runFix(args)
	case "revert":
		err = runRevert(args)
	case "verify":
		err = runVerify(args)
	case "info":
		err = runInfo(args)
	case "version", "--version", "-version":
		fmt.Println(ToolName)
		return
	case "help", "-h", "--help":
		fmt.Print(usage())
		return
	default:
		fmt.Fprintf(os.Stderr, "djgyrofix: unknown command %q\n\n%s", command, usage())
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "djgyrofix: %v\n", err)
		os.Exit(1)
	}
}
