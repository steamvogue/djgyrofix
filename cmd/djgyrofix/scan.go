package main

import (
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/steamvogue/djgyrofix/internal/report"
)

// runScan analyzes files and reports. It never opens a video for writing.
func runScan(args []string) error {
	flags := flag.NewFlagSet("scan", flag.ExitOnError)
	opts := &options{}
	opts.registerDetection(flags)
	opts.registerCorrection(flags)
	opts.registerIO(flags, false)
	flags.Usage = commandUsage(flags,
		"analyze and report",
		"djgyrofix scan [flags] <file...>",
		`Reports what is wrong with a clip and what a fix would change. The video is
opened read-only and is never written to, so this is always safe to run.

The report also states what a patch would touch, which makes it the right first
step on footage you care about.`,
		[]flagGroup{
			{"Detection", detectionFlagNames},
			{"Correction", correctionFlagNames},
			{"Output", outputFlagNames},
		},
		[][2]string{
			{"djgyrofix scan DJI_0042.MP4", "report events in one clip"},
			{"djgyrofix scan --auto DJI_0042.MP4", "let it pick the profile and say why"},
			{"djgyrofix scan --profile conservative DJI_*.MP4", "fewer, higher-confidence events"},
			{"djgyrofix scan --format json DJI_0042.MP4", "machine-readable report"},
			{"djgyrofix scan --format edl DJI_0042.MP4 > events.edl", "review the ranges in an NLE"},
		})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := opts.validateCommon(); err != nil {
		return err
	}
	paths := flags.Args()
	if len(paths) == 0 {
		flags.Usage()
		return fmt.Errorf("no input files")
	}
	intervals, err := parseRanges(opts.ranges)
	if err != nil {
		return err
	}

	reports, failures := forEachFile(paths, opts.jobs, func(path string) (report.Report, error) {
		result, err := analyze(path, opts, intervals)
		if err != nil {
			return report.Report{}, err
		}
		result.report.DryRun = true
		return result.report, nil
	})
	if err := report.Write(os.Stdout, reports, opts.format); err != nil {
		return err
	}
	return summarize(failures)
}

// forEachFile analyzes files in parallel but returns reports in argument order,
// so batch output is deterministic regardless of --jobs.
func forEachFile(paths []string, jobs int, work func(string) (report.Report, error)) ([]report.Report, []error) {
	if jobs > len(paths) {
		jobs = len(paths)
	}
	if jobs < 1 {
		jobs = 1
	}
	reports := make([]report.Report, len(paths))
	errs := make([]error, len(paths))

	var group sync.WaitGroup
	queue := make(chan int)
	for worker := 0; worker < jobs; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range queue {
				reports[index], errs[index] = work(paths[index])
			}
		}()
	}
	for index := range paths {
		queue <- index
	}
	close(queue)
	group.Wait()

	var ordered []report.Report
	var failures []error
	for index := range paths {
		if errs[index] != nil {
			failures = append(failures, errs[index])
			continue
		}
		ordered = append(ordered, reports[index])
	}
	return ordered, failures
}

func summarize(failures []error) error {
	for _, failure := range failures {
		fmt.Fprintf(os.Stderr, "djgyrofix: %v\n", failure)
	}
	if len(failures) == 1 {
		return fmt.Errorf("1 file failed")
	}
	if len(failures) > 1 {
		return fmt.Errorf("%d files failed", len(failures))
	}
	return nil
}
