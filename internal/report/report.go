// Package report renders scan and fix results as text, JSON, EDL or CSV.
//
// The machine-readable formats exist so a human can stay in the loop over a
// batch: review the events in an NLE timeline or a spreadsheet, then feed the
// approved ranges back through --ranges.
package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/steamvogue/djgyrofix/internal/detect"
)

// Report is everything one file's scan or fix produced.
type Report struct {
	File            string `json:"file"`
	Variant         string `json:"variant"`
	VariantDetected string `json:"variant_detected"`
	VariantOverride bool   `json:"variant_override"`

	DurationSeconds float64 `json:"duration_seconds"`
	SampleCount     int     `json:"sample_count"`
	QuaternionCount int     `json:"quaternion_count"`
	SampleRate      float64 `json:"sample_rate_hz"`
	Timescale       uint32  `json:"timescale"`
	VideoFPS        float64 `json:"video_fps,omitempty"`

	BaselineDPS      float64        `json:"baseline_dps"`
	ThresholdDPS     float64        `json:"threshold_dps"`
	RollingBaseline  bool           `json:"rolling_baseline"`
	Events           []detect.Event `json:"events"`
	AffectedSeconds  float64        `json:"affected_seconds"`
	AffectedFraction float64        `json:"affected_fraction"`

	// Fix results. Applied is false for a scan or a dry run.
	Applied            bool    `json:"applied"`
	DryRun             bool    `json:"dry_run"`
	Writes             int     `json:"writes"`
	BytesWritten       int     `json:"bytes_written"`
	QuaternionsChanged int     `json:"quaternions_changed"`
	SamplesChanged     int     `json:"samples_changed"`
	OutputPath         string  `json:"output_path,omitempty"`
	JournalPath        string  `json:"journal_path,omitempty"`
	BackupPath         string  `json:"backup_path,omitempty"`
	ScoreBefore        float64 `json:"score_before,omitempty"`
	ScoreAfter         float64 `json:"score_after,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

// ImprovementPercent is the reduction in high-frequency residual, on the
// reference's angular-acceleration metric.
func (r Report) ImprovementPercent() float64 {
	if r.ScoreBefore <= 0 {
		return 0
	}
	return math.Max(0, (1.0-r.ScoreAfter/r.ScoreBefore)*100.0)
}

// Timestamp renders seconds as HH:MM:SS.mmm.
//
// Rounding happens once, to whole milliseconds, before the value is split.
// Formatting the seconds field with %.3f instead would let 59.9996 print as
// "00:00:60.000", because the carry never reaches the minutes.
func Timestamp(seconds float64) string {
	if seconds < 0 || math.IsNaN(seconds) {
		seconds = 0
	}
	total := int64(math.Round(seconds * 1000))
	milliseconds := total % 1000
	whole := total / 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", whole/3600, whole%3600/60, whole%60, milliseconds)
}

// Timecode renders seconds as SMPTE non-drop HH:MM:SS:FF.
//
// Non-drop is what an NLE expects from an EDL with no drop-frame flag, and it
// deliberately drifts from wall clock on fractional rates: the frame index
// comes from the true rate (29.97), while the displayed timecode divides by the
// nominal one (30). At the end of an hour of 29.97 footage the timecode reads
// about 3.6 seconds behind real time, which is the defined behaviour and what
// the NLE will reverse on import.
func Timecode(seconds, fps float64) string {
	if fps <= 0 {
		fps = 30
	}
	if seconds < 0 || math.IsNaN(seconds) {
		seconds = 0
	}
	nominal := int(math.Round(fps))
	if nominal < 1 {
		nominal = 1
	}
	total := int(math.Round(seconds * fps))
	frames := total % nominal
	whole := total / nominal
	return fmt.Sprintf("%02d:%02d:%02d:%02d", whole/3600, whole%3600/60, whole%60, frames)
}

// Write renders the report in the named format.
func Write(out io.Writer, reports []Report, format string) error {
	switch format {
	case "", "text":
		return writeText(out, reports)
	case "json":
		return writeJSON(out, reports)
	case "edl":
		return writeEDL(out, reports)
	case "csv":
		return writeCSV(out, reports)
	default:
		return fmt.Errorf("unknown format %q (want text, json, edl or csv)", format)
	}
}

// errWriter accumulates the first write error so the text and EDL renderers can
// stay readable.
//
// These formats are hundreds of Fprintf calls, and checking each one inline
// would drown the layout. Dropping the errors instead is not an option: the
// machine-readable output is meant to be redirected to a file, and a full disk
// or a closed pipe has to surface as a non-zero exit rather than a truncated
// report that looks complete.
type errWriter struct {
	out io.Writer
	err error
}

func (w *errWriter) printf(format string, args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.out, format, args...)
}

func (w *errWriter) println(args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintln(w.out, args...)
}

func writeJSON(out io.Writer, reports []Report) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if len(reports) == 1 {
		return encoder.Encode(reports[0])
	}
	return encoder.Encode(reports)
}

func writeText(out io.Writer, reports []Report) error {
	writer := &errWriter{out: out}
	for index, report := range reports {
		if index > 0 {
			writer.println()
		}
		writeTextOne(writer, report)
	}
	return writer.err
}

func writeTextOne(w *errWriter, report Report) {
	variant := report.Variant
	if report.VariantOverride {
		variant += " (forced; sniffed " + report.VariantDetected + ")"
	}
	w.printf("%s  %s  %.0f s  %d samples", report.File, variant,
		report.DurationSeconds, report.SampleCount)
	if report.QuaternionCount > 0 && report.SampleRate > 0 {
		w.printf("  %d quaternions @ %.1f Hz", report.QuaternionCount, report.SampleRate)
	}
	w.println()

	scope := "rolling"
	if !report.RollingBaseline {
		scope = "global"
	}
	w.printf("baseline %.1f °/s   threshold %.1f °/s (%s)\n",
		report.BaselineDPS, report.ThresholdDPS, scope)

	if len(report.Events) == 0 {
		w.println("\nno events")
	} else {
		w.println()
		w.printf("  %-2s %-12s %-12s %-7s %-8s %-4s %-6s %-6s %s\n",
			"#", "start", "end", "dur", "type", "sev", "axes", "peaks", "action")
		for index, event := range report.Events {
			axes := strings.Join(event.DominantAxes, "/")
			if axes == "" {
				axes = "-"
			}
			w.printf("  %-2d %-12s %-12s %-7s %-8s %-4.1f %-6s %-6d %s\n",
				index+1,
				Timestamp(event.StartSeconds),
				Timestamp(event.EndSeconds),
				fmt.Sprintf("%.3fs", event.DurationSeconds()),
				event.Class,
				event.Severity,
				axes,
				event.SpikeCount,
				event.Action)
			if event.Note != "" {
				w.printf("     note: %s\n", event.Note)
			}
		}
		w.printf("\n%d event%s, %.2f s affected (%.2f%% of clip)\n",
			len(report.Events), plural(len(report.Events)),
			report.AffectedSeconds, report.AffectedFraction*100)
	}

	if report.Applied {
		w.printf("\npatched %d quaternions in %d samples, %d bytes\n",
			report.QuaternionsChanged, report.SamplesChanged, report.BytesWritten)
		if report.OutputPath != "" {
			w.printf("output:  %s\n", report.OutputPath)
		}
		if report.JournalPath != "" {
			w.printf("journal: %s\n", report.JournalPath)
		}
		if report.BackupPath != "" {
			w.printf("backup:  %s\n", report.BackupPath)
		}
		if report.ScoreBefore > 0 {
			w.printf("high-frequency residual reduced %.1f%%\n", report.ImprovementPercent())
		}
	} else if report.DryRun && report.Writes > 0 {
		// Reached from `scan` as well as from a `fix` dry run, so the
		// instruction names the command rather than saying "re-run": scan has
		// no --apply flag of its own.
		w.printf("\ndry run: would patch %d quaternions in %d samples (%d bytes)\n",
			report.QuaternionsChanged, report.SamplesChanged, report.BytesWritten)
		w.printf("run `djgyrofix fix --apply %s` to write\n", report.File)
	}

	for _, warning := range report.Warnings {
		w.printf("warning: %s\n", warning)
	}
}

// writeEDL emits a CMX 3600 edit decision list, one event per edit, so the
// flagged ranges can be dropped onto an NLE timeline for review.
func writeEDL(out io.Writer, reports []Report) error {
	w := &errWriter{out: out}
	for _, report := range reports {
		fps := report.VideoFPS
		if fps <= 0 {
			fps = 30
		}
		w.printf("TITLE: djgyrofix %s\n", report.File)
		w.println("FCM: NON-DROP FRAME")
		w.printf("* SOURCE FPS %.3f\n", fps)
		for index, event := range report.Events {
			start := Timecode(event.StartSeconds, fps)
			end := Timecode(event.EndSeconds, fps)
			w.printf("%03d  AX       V     C        %s %s %s %s\n",
				index+1, start, end, start, end)
			w.printf("* FROM CLIP NAME: %s\n", report.File)
			w.printf("* COMMENT: %s severity %.1f action %s\n",
				event.Class, event.Severity, event.Action)
		}
		w.println()
	}
	return w.err
}

func writeCSV(out io.Writer, reports []Report) error {
	writer := csv.NewWriter(out)
	if err := writer.Write([]string{
		"file", "index", "start", "end", "duration", "peak", "type", "action",
		"severity", "severity_label", "axes", "peaks", "peak_dps", "baseline_dps",
		"threshold_dps", "smoothing_ms", "note",
	}); err != nil {
		return err
	}
	for _, report := range reports {
		for index, event := range report.Events {
			if err := writer.Write([]string{
				report.File,
				strconv.Itoa(index + 1),
				formatFloat(event.StartSeconds),
				formatFloat(event.EndSeconds),
				formatFloat(event.DurationSeconds()),
				formatFloat(event.PeakSeconds),
				string(event.Class),
				string(event.Action),
				formatFloat(event.Severity),
				event.SeverityLabel,
				strings.Join(event.DominantAxes, "/"),
				strconv.Itoa(event.SpikeCount),
				formatFloat(event.PeakDPS),
				formatFloat(event.BaselineDPS),
				formatFloat(event.ThresholdDPS),
				formatFloat(event.SmoothingMS),
				event.Note,
			}); err != nil {
				return err
			}
		}
	}
	writer.Flush()
	return writer.Error()
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 3, 64)
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

// Ranges renders the events as a --ranges argument, so a scan report can be
// reviewed, edited and fed straight back in.
func Ranges(events []detect.Event) string {
	var parts []string
	for _, event := range events {
		if event.Action == detect.ActionNone {
			continue
		}
		parts = append(parts, fmt.Sprintf("%.3f-%.3f", event.StartSeconds, event.EndSeconds))
	}
	return strings.Join(parts, ",")
}
