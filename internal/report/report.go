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

	"github.com/steamvogue/djgyrofix/internal/advise"
	"github.com/steamvogue/djgyrofix/internal/correct"
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

	BaselineDPS      float64             `json:"baseline_dps"`
	ThresholdDPS     float64             `json:"threshold_dps"`
	RollingBaseline  bool                `json:"rolling_baseline"`
	Events           []detect.Event      `json:"events"`
	AffectedSeconds  float64             `json:"affected_seconds"`
	AffectedFraction float64             `json:"affected_fraction"`
	Noise            detect.NoiseProfile `json:"noise"`
	NearMissEvents   int                 `json:"near_miss_events"`
	// DuplicateShare is the fraction of stored quaternions identical to their
	// predecessor. DJI oversamples its fused attitude, so real footage sits at
	// 0.5 and the effective information rate is half the stored one.
	DuplicateShare float64 `json:"duplicate_share"`

	// Advice is the verdict rendered from everything above it. Always present
	// on an automatic scan or fix; nil on the manual --ranges path, where the
	// user has already decided what to correct.
	Advice *advise.Advice `json:"advice,omitempty"`
	// Auto records what autopilot chose and why, when --auto was passed.
	Auto *AutoRecord `json:"auto,omitempty"`

	// Fix results. Applied is false for a scan or a dry run.
	Applied            bool   `json:"applied"`
	DryRun             bool   `json:"dry_run"`
	Writes             int    `json:"writes"`
	BytesWritten       int    `json:"bytes_written"`
	QuaternionsChanged int    `json:"quaternions_changed"`
	SamplesChanged     int    `json:"samples_changed"`
	OutputPath         string `json:"output_path,omitempty"`
	JournalPath        string `json:"journal_path,omitempty"`
	BackupPath         string `json:"backup_path,omitempty"`
	// Repair is what run-repair did, when --repair runs was used.
	Repair      *correct.RepairStats `json:"repair,omitempty"`
	ScoreBefore float64              `json:"score_before,omitempty"`
	ScoreAfter  float64              `json:"score_after,omitempty"`
	// ClipScoreBefore and ClipScoreAfter measure the same metric over the whole
	// clip rather than over the corrected regions.
	ClipScoreBefore float64 `json:"clip_score_before,omitempty"`
	ClipScoreAfter  float64 `json:"clip_score_after,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

// AutoRecord is the audit trail of an autopilot run: every profile it tried,
// in order, and the measurement that moved it on.
type AutoRecord struct {
	Profile  string   `json:"profile"`
	Refused  bool     `json:"refused"`
	Steps    []string `json:"steps"`
	Attempts []string `json:"attempts"`
}

// ImprovementPercent is the reduction in transient residual inside the
// corrected regions, on the reference's angular-acceleration metric.
//
// Read it with ClipImprovementPercent, never alone. This figure cannot fall
// where detection never looked, so it reads best precisely when under-detection
// has left the footage still shaking: one real clip reported 91.6% here while a
// two-second burst it had covered 12% of remained plainly visible.
func (r Report) ImprovementPercent() float64 {
	if r.ScoreBefore <= 0 {
		return 0
	}
	return math.Max(0, (1.0-r.ScoreAfter/r.ScoreBefore)*100.0)
}

// ClipImprovementPercent is the same reduction measured over the whole clip. It
// is the honest headline, because it falls when correction misses something.
func (r Report) ClipImprovementPercent() float64 {
	if r.ClipScoreBefore <= 0 {
		return 0
	}
	return math.Max(0, (1.0-r.ClipScoreAfter/r.ClipScoreBefore)*100.0)
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
	if report.DuplicateShare >= 0.4 && report.SampleRate > 0 {
		w.printf("stored rate is %.0f%% duplicate pairs — %.1f Hz of information\n",
			report.DuplicateShare*100, report.SampleRate*(1-report.DuplicateShare))
	}

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

	if report.Repair != nil {
		w.println()
		writeWrapped(w, fmt.Sprintf(
			"run-repair: replaced %d runs (%d quaternions); %d too long to interpolate, %d were real motion",
			report.Repair.RunsReplaced, report.Repair.SamplesReplaced,
			report.Repair.RunsTooLong, report.Repair.RunsRealMotion), "  ")
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
			w.printf("transient residual reduced %.1f%% clip-wide, %.1f%% inside the corrected regions\n",
				report.ClipImprovementPercent(), report.ImprovementPercent())
		}
	} else if report.DryRun && report.Writes > 0 {
		// Reached from `scan` as well as from a `fix` dry run, so the
		// instruction names the command rather than saying "re-run": scan has
		// no --apply flag of its own.
		w.printf("\ndry run: would patch %d quaternions in %d samples (%d bytes)\n",
			report.QuaternionsChanged, report.SamplesChanged, report.BytesWritten)
		if report.Advice == nil {
			// The manual --ranges path has no diagnosis to carry the next
			// command, so it keeps the instruction of its own.
			w.printf("run `djgyrofix fix --apply %s` to write\n", report.File)
		}
	}

	writeAdvice(w, report)

	for _, warning := range report.Warnings {
		w.printf("warning: %s\n", warning)
	}
}

// writeAdvice renders the diagnosis block: the verdict, the measurements behind
// it, the flags worth trying next, and the command to run.
//
// This is the part of the report a pilot can act on without knowing what a
// residual is, so it goes last, where the eye lands.
func writeAdvice(w *errWriter, report Report) {
	if report.Advice == nil {
		return
	}
	advice := report.Advice

	if report.Auto != nil {
		headline := "autopilot: " + report.Auto.Profile + " profile"
		if report.Auto.Refused {
			headline += " — refused"
		}
		w.println()
		writeWrapped(w, headline, "  ")
		for _, step := range report.Auto.Steps {
			writeWrapped(w, "  "+step, "    ")
		}
	}

	w.println()
	writeWrapped(w, "diagnosis: "+advice.Headline, "  ")
	for _, reason := range advice.Reasons {
		writeWrapped(w, "  "+reason, "  ")
	}
	// A patch that has already run has printed its measured reduction above,
	// and it needs no invitation to run the command it just ran.
	if advice.Prediction != "" && !report.Applied {
		writeWrapped(w, "  "+advice.Prediction, "  ")
	}
	for _, suggestion := range advice.Suggestions {
		if suggestion.Flags == advise.NoFlag {
			writeWrapped(w, "  note: "+suggestion.Why, "        ")
			continue
		}
		writeWrapped(w, "  try "+suggestion.Flags+" — "+suggestion.Why, "      ")
	}
	if advice.NextCommand != "" && !report.Applied {
		w.printf("  next: %s\n", advice.NextCommand)
	}
}

// writeWrapped emits one logical line, folded at adviceWidth with the given
// continuation indent. The whole line is wrapped, prefix included, because
// wrapping only the tail leaves the first line as long as the prefix made it.
//
// A line's own leading indent is held back from the wrapper, which works in
// words and would otherwise swallow it.
func writeWrapped(w *errWriter, line, indent string) {
	body := strings.TrimLeft(line, " ")
	lead := line[:len(line)-len(body)]
	w.printf("%s%s\n", lead, wrapIndent(body, adviceWidth-len(lead), indent))
}

// adviceWidth is where the prose wraps. Eighty columns is the width every
// terminal has, and the alternative — querying the real one — would make the
// output depend on the window it happened to be run in, which is no way to
// paste a report into a bug report.
const adviceWidth = 78

// wrapIndent breaks text at adviceWidth, continuing lines with the given
// indent. Words longer than the width are left intact rather than split.
func wrapIndent(text string, width int, indent string) string {
	words := joinUnits(strings.Fields(text))
	if len(words) == 0 {
		return ""
	}
	var lines []string
	current := words[0]
	for _, word := range words[1:] {
		limit := width
		if len(lines) > 0 {
			limit -= len([]rune(indent))
		}
		if len([]rune(current))+1+len([]rune(word)) > limit {
			lines = append(lines, current)
			current = word
			continue
		}
		current += " " + word
	}
	lines = append(lines, current)

	// A greedy fill can leave a stub on the last line — one word, or a lone
	// "(30.00 s)" — which reads as a rendering fault rather than as a wrap.
	// Pulling a word down from the line above costs a little raggedness and
	// removes the widow.
	if len(lines) > 1 {
		last := lines[len(lines)-1]
		previous := lines[len(lines)-2]
		if len([]rune(last)) <= widowWidth {
			if cut := strings.LastIndex(previous, " "); cut > 0 {
				lines[len(lines)-2] = previous[:cut]
				lines[len(lines)-1] = previous[cut+1:] + " " + last
			}
		}
	}
	return strings.Join(lines, "\n"+indent)
}

// widowWidth is how short a trailing line has to be before it is worth
// reflowing the line above to avoid it.
const widowWidth = 12

// joinUnits reattaches a bare unit to the number in front of it, so a wrap
// cannot leave "113.9" at the end of one line and "°/s" at the start of the
// next. Splitting a quantity from its unit is the one line break that makes a
// measurement harder to read than no wrapping at all.
func joinUnits(words []string) []string {
	units := map[string]bool{"°/s": true, "ms": true, "s": true, "Hz": true, "%": true}
	joined := make([]string, 0, len(words))
	for _, word := range words {
		if len(joined) > 0 && units[strings.TrimRight(word, ",.;:")] && endsWithDigit(joined[len(joined)-1]) {
			joined[len(joined)-1] += " " + word
			continue
		}
		joined = append(joined, word)
	}
	return joined
}

func endsWithDigit(word string) bool {
	if word == "" {
		return false
	}
	last := word[len(word)-1]
	return last >= '0' && last <= '9'
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
