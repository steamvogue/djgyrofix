package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"github.com/steamvogue/djgyrofix/internal/advise"
	"github.com/steamvogue/djgyrofix/internal/correct"
	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/djiproto"
	"github.com/steamvogue/djgyrofix/internal/patch"
	"github.com/steamvogue/djgyrofix/internal/pipeline"
	"github.com/steamvogue/djgyrofix/internal/quat"
	"github.com/steamvogue/djgyrofix/internal/report"
)

// analysis is one file's detection or manual-range result plus the writes it
// implies. Nothing here has touched the video yet — every patch is computed in
// full before the file is opened for writing (plan §7.3 step 1).
type analysis struct {
	report  report.Report
	writes  []patch.Write
	spans   []patch.SampleSpan
	params  map[string]any
	events  []detect.Event
	samples int
}

// analyze runs the whole read-and-compute half of the pipeline for one file.
func analyze(path string, opts *options, intervals []interval) (*analysis, error) {
	override, err := opts.variantOverride()
	if err != nil {
		return nil, err
	}
	source, err := pipeline.Open(path, override)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Opened read-only; a close failure has nothing the caller can act on.
	defer func() { _ = source.Close() }()

	result := &analysis{
		report: report.Report{
			File:            path,
			Variant:         string(source.Variant),
			VariantDetected: string(source.VariantDetected),
			VariantOverride: override != "",
			Camera:          cameraIdentity(source),
			DurationSeconds: source.DurationSeconds(),
			SampleCount:     source.Track.SampleCount(),
			Timescale:       source.Track.Timescale,
			VideoFPS:        pipeline.VideoFPS(source.Tracks),
			Events:          []detect.Event{},
		},
		samples: source.Track.SampleCount(),
	}
	result.spans = make([]patch.SampleSpan, source.Track.SampleCount())
	for index := range result.spans {
		result.spans[index] = patch.SampleSpan{
			Offset: source.Track.SampleOffsets[index],
			Size:   source.Track.SampleSizes[index],
		}
	}

	if len(intervals) > 0 {
		if err := analyzeRanges(source, opts, intervals, result); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return result, nil
	}
	if err := analyzeAuto(source, opts, result); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return result, nil
}

// analyzeAuto is the automatic path: detect, then correct by weight envelope.
func analyzeAuto(source *pipeline.Source, opts *options, result *analysis) error {
	params, err := opts.detectParams()
	if err != nil {
		return err
	}
	points, err := source.ReadAll()
	if err != nil {
		return err
	}
	result.report.QuaternionCount = len(points)
	if len(points) == 0 {
		return fmt.Errorf("the DJI metadata track holds no readable quaternions for variant %s", source.Variant)
	}

	detected, err := detect.Run(points, params)
	if err != nil {
		return err
	}
	if opts.auto {
		// Autopilot re-detects, never re-reads. Detection is the cheap half of
		// the pass; the correction and the file read are not, so the profile is
		// settled from detection alone and only the winner is corrected.
		detected, params, err = autopilot(points, params, opts, result)
		if err != nil {
			return err
		}
	}
	result.report.SampleRate = detected.SampleRate
	result.report.BaselineDPS = detected.BaselineDPS
	result.report.ThresholdDPS = detected.ThresholdDPS
	result.report.RollingBaseline = detected.Rolling
	result.report.Events = detected.Events
	result.report.AffectedSeconds = detected.AffectedSeconds
	result.report.AffectedFraction = detected.AffectedFraction
	result.report.Noise = detected.Noise
	result.report.NearMissEvents = detected.NearMiss
	result.report.DuplicateShare = detected.DuplicateShare
	result.events = detected.Events
	result.params = paramsMap(params, opts)

	if !detected.Rolling && detected.QuaternionCount > 0 {
		// A rolling window needs enough clip to slide over. Below the cutoff
		// the median of a five-second window is the median of the whole file,
		// so the reference's global baseline is used and said out loud.
		result.report.Warnings = append(result.report.Warnings,
			fmt.Sprintf("clip is shorter than %.0fs; thresholds came from a single global baseline, not a rolling one",
				params.ShortClipSeconds))
	}

	times := pipeline.Times(points)
	values := pipeline.Values(points)
	repairStats := &correct.RepairStats{}
	corrected, correctionPasses, postCorrection, discovered, correctionScopes, err := autoCorrect(points, detected, params, correct.EnvelopeOptions{
		Strength:    opts.strength,
		SmoothingMS: opts.smoothingMS,
		RepairRuns:  opts.repair == "runs",
		Repair:      correct.DefaultRepairOptions(),
		Stats:       repairStats,
	}, opts.maxAffected)
	if err != nil {
		return err
	}
	if len(discovered) > 0 {
		result.report.Events = append(result.report.Events, discovered...)
		sort.SliceStable(result.report.Events, func(a, b int) bool {
			return result.report.Events[a].StartSeconds < result.report.Events[b].StartSeconds
		})
		result.events = result.report.Events
		result.report.AffectedSeconds = eventUnionSeconds(actionableEvents(result.report.Events))
		if result.report.DurationSeconds > 0 {
			result.report.AffectedFraction = result.report.AffectedSeconds / result.report.DurationSeconds
		}
	}
	if opts.repair == "runs" {
		result.report.Repair = repairStats
	}
	result.params["repair"] = opts.repair
	result.params["correction_passes"] = correctionPasses
	result.params["events_discovered_during_correction"] = len(discovered)
	scopePadding := math.Max(params.GapSeconds, params.EnvelopeBlurSeconds)
	remaining, outside := residualEventCounts(postCorrection, correctionScopes, scopePadding)
	if remaining > 0 {
		// Deliberately phrased as a ceiling rather than a defect count. Adding
		// passes drives this number down indefinitely without moving the
		// residual it stands for, so a reader who treats it as work outstanding
		// will correct footage that is already as good as the bounded passes
		// can make it.
		result.report.Warnings = append(result.report.Warnings,
			fmt.Sprintf("%d of %d corrected region(s) still trip the detector after %d bounded pass(es); "+
				"that is where the bounded correction settles, not a count of defects left in the video",
				remaining, len(actionableEvents(correctionScopes)), correctionPasses))
	}
	if outside > 0 {
		result.report.Warnings = append(result.report.Warnings,
			fmt.Sprintf("post-correction scan found %d actionable event(s) outside the bounded correction scopes; left untouched", outside))
	}
	writes, changedQuats, changedSamples, err := buildWrites(points, corrected)
	if err != nil {
		return err
	}
	result.writes = writes
	result.report.Writes = len(writes)
	result.report.BytesWritten = len(writes) * 4
	result.report.QuaternionsChanged = changedQuats
	result.report.SamplesChanged = changedSamples

	before, after, clipBefore, clipAfter := scoreEvents(times, values, corrected, result.report.Events)
	result.report.ScoreBefore = before
	result.report.ScoreAfter = after
	result.report.ClipScoreBefore = clipBefore
	result.report.ClipScoreAfter = clipAfter

	result.report.Advice = adviseReport(&result.report, params, opts, remaining)
	return nil
}

// adviseReport turns a finished analysis into the diagnosis block. It runs last
// because two of its inputs — the predicted improvement and the count of
// regions still detectable — only exist once correction has been planned.
func adviseReport(rep *report.Report, params detect.Params, opts *options, residualRegions int) *advise.Advice {
	advice := advise.Evaluate(advise.Input{
		File:                   rep.File,
		DurationSeconds:        rep.DurationSeconds,
		Events:                 rep.Events,
		AffectedSeconds:        rep.AffectedSeconds,
		AffectedFraction:       rep.AffectedFraction,
		Noise:                  rep.Noise,
		NearMiss:               rep.NearMissEvents,
		RollingBaseline:        rep.RollingBaseline,
		ShortClipSeconds:       params.ShortClipSeconds,
		Profile:                params.Profile,
		Sensitivity:            params.Sensitivity,
		MinSeverity:            params.MinSeverity,
		MaxAffected:            opts.maxAffected,
		ImprovementPercent:     rep.ImprovementPercent(),
		ClipImprovementPercent: rep.ClipImprovementPercent(),
		Scored:                 rep.ScoreBefore > 0,
		ResidualRegions:        residualRegions,
	})
	return &advice
}

// autopilot settles on a profile before any correction is planned.
//
// It steps at most once in each direction and never revisits a profile, so the
// loop is bounded at three detection passes over the same in-memory points. The
// full trail — every profile tried and the measurement that moved it on — is
// recorded on the report, because a run that silently chose different
// parameters than the flags asked for would be worse than no autopilot at all.
func autopilot(points []pipeline.Point, params detect.Params, opts *options, result *analysis) (*detect.Result, detect.Params, error) {
	record := &report.AutoRecord{Profile: params.Profile}
	tried := map[string]bool{params.Profile: true}
	detected, err := detect.Run(points, params)
	if err != nil {
		return nil, params, err
	}

	for attempt := 0; attempt < maxAutopilotSteps; attempt++ {
		record.Attempts = append(record.Attempts, params.Profile)
		decision := advise.Step(autopilotInput(detected, params, opts), tried)
		record.Steps = append(record.Steps, decision.Reason)
		if decision.Refuse {
			record.Refused = true
			break
		}
		if decision.Profile == "" || tried[decision.Profile] {
			break
		}
		next, err := detect.ProfileParams(decision.Profile)
		if err != nil {
			return nil, params, err
		}
		// Explicit flags outrank the preset, exactly as they do on a hand-run
		// pass: autopilot chooses a profile, not a whole parameter set.
		next, err = opts.applyOverrides(next)
		if err != nil {
			return nil, params, err
		}
		rerun, err := detect.Run(points, next)
		if err != nil {
			return nil, params, err
		}
		tried[decision.Profile] = true
		params, detected = next, rerun
		record.Profile = params.Profile
	}

	result.report.Auto = record
	return detected, params, nil
}

// maxAutopilotSteps bounds the profile search. Three profiles exist and a step
// never revisits one, so this can never be the binding limit; it is here so a
// future profile list cannot turn the loop into a search.
const maxAutopilotSteps = 3

func autopilotInput(detected *detect.Result, params detect.Params, opts *options) advise.Input {
	return advise.Input{
		Events:           detected.Events,
		AffectedSeconds:  detected.AffectedSeconds,
		AffectedFraction: detected.AffectedFraction,
		Noise:            detected.Noise,
		NearMiss:         detected.NearMiss,
		Profile:          params.Profile,
		MinSeverity:      params.MinSeverity,
		MaxAffected:      opts.maxAffected,
	}
}

const maxAutoCorrectionPasses = 3

// autoCorrect rescans after each pass. Residual events near an existing scope
// remain authorized; newly exposed smoothing events may extend the scope only
// while its time union stays below --max-affected. Newly detected dropouts are
// never added here.
func autoCorrect(points []pipeline.Point, initial *detect.Result, params detect.Params, options correct.EnvelopeOptions, maxAffected float64) ([]quat.Q, int, *detect.Result, []detect.Event, []detect.Event, error) {
	times := pipeline.Times(points)
	working := pipeline.Values(points)
	current := initial
	lastScan := initial
	passes := 0
	padding := math.Max(params.GapSeconds, params.EnvelopeBlurSeconds)
	scopes := actionableEvents(initial.Events)
	discovered := []detect.Event{}
	limitSeconds := initial.DurationSeconds * maxAffected
	if used := eventUnionSeconds(scopes); limitSeconds < used {
		limitSeconds = used
	}

	for passes < maxAutoCorrectionPasses && hasActionableEvents(current.Events) {
		next, err := correct.Envelope(times, working, current, options)
		if err != nil {
			return nil, passes, current, discovered, scopes, err
		}
		working = quantizeQuaternions(next)
		passes++

		rescannedPoints := make([]pipeline.Point, len(points))
		copy(rescannedPoints, points)
		for index := range rescannedPoints {
			rescannedPoints[index].Values = working[index]
		}
		rescanned, err := detect.Run(rescannedPoints, params)
		if err != nil {
			return nil, passes, current, discovered, scopes, err
		}
		lastScan = rescanned
		selected := make([]detect.Event, 0, len(rescanned.Events))
		for _, event := range rescanned.Events {
			if event.Action != detect.ActionSmooth {
				continue
			}
			if eventMatchesScope(event, scopes, padding) {
				selected = append(selected, event)
				continue
			}
			candidateScopes := append(append([]detect.Event(nil), scopes...), event)
			if eventUnionSeconds(candidateScopes) > limitSeconds {
				continue
			}
			event.Note = fmt.Sprintf("newly exposed after correction pass %d", passes)
			scopes = append(scopes, event)
			discovered = append(discovered, event)
			selected = append(selected, event)
		}
		if len(selected) == 0 {
			return working, passes, rescanned, discovered, scopes, nil
		}
		copyResult := *rescanned
		copyResult.Events = selected
		current = &copyResult
	}
	return working, passes, lastScan, discovered, scopes, nil
}

func quantizeQuaternions(values []quat.Q) []quat.Q {
	output := make([]quat.Q, len(values))
	for index, value := range values {
		for component := range value {
			output[index][component] = float64(float32(value[component]))
		}
	}
	return output
}

func hasActionableEvents(events []detect.Event) bool {
	for _, event := range events {
		if event.Action != detect.ActionNone {
			return true
		}
	}
	return false
}

func actionableEvents(events []detect.Event) []detect.Event {
	actionable := make([]detect.Event, 0, len(events))
	for _, event := range events {
		if event.Action != detect.ActionNone {
			actionable = append(actionable, event)
		}
	}
	return actionable
}

func eventMatchesScope(event detect.Event, scopes []detect.Event, paddingSeconds float64) bool {
	for _, scope := range scopes {
		if eventsOverlap(event, scope, paddingSeconds) {
			return true
		}
	}
	return false
}

func eventUnionSeconds(events []detect.Event) float64 {
	if len(events) == 0 {
		return 0
	}
	ranges := append([]detect.Event(nil), events...)
	sort.SliceStable(ranges, func(a, b int) bool {
		return ranges[a].StartSeconds < ranges[b].StartSeconds
	})
	total := 0.0
	start, end := ranges[0].StartSeconds, ranges[0].EndSeconds
	for _, event := range ranges[1:] {
		if event.StartSeconds <= end {
			end = math.Max(end, event.EndSeconds)
			continue
		}
		total += math.Max(0, end-start)
		start, end = event.StartSeconds, event.EndSeconds
	}
	return total + math.Max(0, end-start)
}

func residualEventCounts(result *detect.Result, original []detect.Event, paddingSeconds float64) (inside, outside int) {
	if result == nil {
		return 0, 0
	}
	for _, event := range result.Events {
		if event.Action == detect.ActionNone {
			continue
		}
		matched := false
		for _, scope := range original {
			if scope.Action != detect.ActionNone && eventsOverlap(event, scope, paddingSeconds) {
				matched = true
				break
			}
		}
		if matched {
			inside++
		} else {
			outside++
		}
	}
	return inside, outside
}

func eventsOverlap(a, b detect.Event, paddingSeconds float64) bool {
	return a.StartSeconds <= b.EndSeconds+paddingSeconds &&
		a.EndSeconds >= b.StartSeconds-paddingSeconds
}

// analyzeRanges is the manual path, and the golden-parity path.
//
// It reproduces the reference exactly: one window per interval with the same
// context padding, the same fixed 180 ms default window, and the same
// accumulation of already-patched samples so a later interval's context sees
// the earlier interval's output.
func analyzeRanges(source *pipeline.Source, opts *options, intervals []interval, result *analysis) error {
	// Manual ranges are the legacy path, and deliberately so: it is the one
	// held to byte-for-byte parity with the Python reference, which has no
	// notion of run-repair. Run-repair is the default everywhere else, so a
	// caller who names a mode here gets the blur regardless. Say that out loud
	// rather than leave it to be inferred from a journal.
	if opts.repairSet && opts.repair != "blur" {
		result.report.Warnings = append(result.report.Warnings,
			"--ranges always corrects by blur; --repair chooses a mode for detected events only")
	}

	smoothingMS := opts.smoothingMS
	if smoothingMS <= 0 {
		smoothingMS = correct.DefaultSmoothingMS
	}
	duration := source.DurationSeconds()
	if intervals[len(intervals)-1].end > duration {
		return fmt.Errorf("range end %.3fs is past the track duration %.3fs",
			intervals[len(intervals)-1].end, duration)
	}

	applied := map[int64][4]byte{}
	var allWrites []patch.Write
	changedSamples := map[int]struct{}{}
	changedQuats := 0
	weightedBefore, weightedAfter := 0.0, 0.0
	sampleRate := 0.0

	contextSeconds := math.Max(0.75, smoothingMS/1000.0*4.0)
	for _, current := range intervals {
		first, last := source.Track.SampleRange(
			math.Max(0, current.start-contextSeconds),
			math.Min(duration, current.end+contextSeconds))
		points, err := source.ReadRange(first, last)
		if err != nil {
			return err
		}
		if len(points) == 0 {
			return fmt.Errorf("no attitude data in %.3f-%.3f", current.start, current.end)
		}
		// Earlier intervals may already have rewritten samples that fall in
		// this interval's context window; read their patched values back.
		for index := range points {
			for component, offset := range points[index].Offsets {
				if offset < 0 {
					continue
				}
				if value, ok := applied[offset]; ok {
					points[index].Values[component] =
						float64(math.Float32frombits(binary.LittleEndian.Uint32(value[:])))
				}
			}
		}

		selected := 0
		for _, point := range points {
			if current.start < point.Time && point.Time < current.end {
				selected++
			}
		}
		if selected == 0 {
			return fmt.Errorf("no correctable attitude data in %.3f-%.3f", current.start, current.end)
		}

		times := pipeline.Times(points)
		values := pipeline.Values(points)
		if rate, _, ok := pipeline.SampleRate(times); ok && sampleRate == 0 {
			sampleRate = rate
		}
		smoothed, err := correct.Legacy(times, values, correct.LegacyParams{
			StartSeconds: current.start,
			EndSeconds:   current.end,
			SmoothingMS:  smoothingMS,
			Strength:     opts.strength,
		})
		if err != nil {
			return err
		}
		before, err := correct.AngularAccelerationScore(times, values, current.start, current.end)
		if err != nil {
			return err
		}
		after, err := correct.AngularAccelerationScore(times, smoothed, current.start, current.end)
		if err != nil {
			return err
		}
		weightedBefore += before * float64(selected)
		weightedAfter += after * float64(selected)
		changedQuats += selected

		// Restrict the output to the selected window, so buildWrites cannot
		// emit anything for the context padding.
		output := append([]quat.Q(nil), values...)
		for index, point := range points {
			if current.start < point.Time && point.Time < current.end {
				output[index] = smoothed[index]
			}
		}
		writes, _, _, err := buildWrites(points, output)
		if err != nil {
			return err
		}
		for _, write := range writes {
			value, err := patch.DecodeBytes(write.New)
			if err != nil {
				return err
			}
			applied[write.Offset] = value
		}
		for _, point := range points {
			if current.start < point.Time && point.Time < current.end {
				changedSamples[point.SampleIndex] = struct{}{}
			}
		}
		allWrites = append(allWrites, writes...)

		result.report.Events = append(result.report.Events, detect.Event{
			StartSeconds:  current.start,
			EndSeconds:    current.end,
			PeakSeconds:   (current.start + current.end) / 2,
			Class:         detect.ClassJitter,
			Action:        detect.ActionSmooth,
			Severity:      10,
			SeverityLabel: "manual",
			DominantAxes:  []string{},
			SpikeCount:    1,
			SmoothingMS:   smoothingMS,
			Note:          "manual range from --ranges",
		})
		result.report.AffectedSeconds += current.end - current.start
	}

	// A later interval can rewrite an offset an earlier one already changed.
	// Collapse to one write per offset, keeping the first "old" and the last
	// "new", so revert restores the true original bytes.
	result.writes = collapseWrites(allWrites)
	result.report.Writes = len(result.writes)
	result.report.BytesWritten = len(result.writes) * 4
	result.report.QuaternionsChanged = changedQuats
	result.report.SamplesChanged = len(changedSamples)
	if changedQuats > 0 {
		result.report.ScoreBefore = weightedBefore / float64(changedQuats)
		result.report.ScoreAfter = weightedAfter / float64(changedQuats)
	}
	if duration > 0 {
		result.report.AffectedFraction = result.report.AffectedSeconds / duration
	}
	result.report.RollingBaseline = false
	result.events = result.report.Events
	result.params = map[string]any{
		"mode":         "ranges",
		"ranges":       opts.ranges,
		"repair":       "blur",
		"smoothing_ms": smoothingMS,
		"strength":     opts.strength,
	}
	result.report.SampleRate = sampleRate
	// QuaternionCount is deliberately left at zero here. Manual-range mode only
	// reads the requested windows, and the whole-track figure would cost a
	// second full pass over the metadata track purely to fill a header field.
	// The text writer omits the count when it is zero rather than printing a
	// window total that reads like a track total.
	return nil
}

// buildWrites diffs corrected quaternions against the originals and emits one
// four-byte write per changed component.
//
// A component whose float32 representation is unchanged produces no write at
// all, which is what makes "a clean clip writes nothing" true rather than
// aspirational.
func buildWrites(points []pipeline.Point, corrected []quat.Q) ([]patch.Write, int, int, error) {
	if len(points) != len(corrected) {
		return nil, 0, 0, fmt.Errorf("internal: %d points against %d corrected quaternions", len(points), len(corrected))
	}
	var writes []patch.Write
	changedQuaternions := 0
	changedSamples := map[int]struct{}{}
	for index, point := range points {
		quaternionChanged := false
		for component := 0; component < 4; component++ {
			value := corrected[index][component]
			if point.Offsets[component] < 0 {
				// proto3 omitted this component because it was zero, so there
				// is no slot to write into. Skipping silently would leave the
				// orientation partly updated and corrupt it, so this is fatal.
				if math.Abs(value) > djiproto.DefaultTolerance {
					return nil, 0, 0, fmt.Errorf(
						"sample %d at %.3fs: a zero-valued quaternion component is omitted in the source protobuf; "+
							"it cannot be changed without resizing the MP4 sample",
						point.SampleIndex, point.Time)
				}
				continue
			}
			oldBits := math.Float32bits(float32(point.Values[component]))
			newBits := math.Float32bits(float32(value))
			if oldBits == newBits {
				continue
			}
			var oldBytes, newBytes [4]byte
			binary.LittleEndian.PutUint32(oldBytes[:], oldBits)
			binary.LittleEndian.PutUint32(newBytes[:], newBits)
			writes = append(writes, patch.Write{
				Offset: point.Offsets[component],
				Old:    patch.EncodeBytes(oldBytes),
				New:    patch.EncodeBytes(newBytes),
			})
			quaternionChanged = true
		}
		if quaternionChanged {
			changedQuaternions++
			changedSamples[point.SampleIndex] = struct{}{}
		}
	}
	patch.Sort(writes)
	return writes, changedQuaternions, len(changedSamples), nil
}

// collapseWrites reduces repeated writes to the same offset to a single entry
// spanning the true original bytes and the final value.
func collapseWrites(writes []patch.Write) []patch.Write {
	if len(writes) == 0 {
		return nil
	}
	first := map[int64]string{}
	final := map[int64]string{}
	var order []int64
	for _, write := range writes {
		if _, seen := first[write.Offset]; !seen {
			first[write.Offset] = write.Old
			order = append(order, write.Offset)
		}
		final[write.Offset] = write.New
	}
	collapsed := make([]patch.Write, 0, len(order))
	for _, offset := range order {
		if first[offset] == final[offset] {
			continue
		}
		collapsed = append(collapsed, patch.Write{Offset: offset, Old: first[offset], New: final[offset]})
	}
	patch.Sort(collapsed)
	return collapsed
}

// scoreEvents measures the residual reduction two ways: over the corrected
// regions, and over the whole clip.
//
// The in-region figure was the only one reported at first, on the reasoning
// that a clip-wide average dilutes a real improvement to nothing. That reading
// was backwards. It cannot fall where detection never looked, so it is at its
// most flattering exactly when detection has under-covered — which is the one
// case a pilot needs to be told about.
//
// Both are measured on distinct orientations rather than on stored samples.
// The metric is the reference's median angular acceleration, and on a stream
// where DJI has written every attitude twice it is overwhelmingly a measure of
// that: on the real clip it read 2469.5 as stored against 6.8 with the repeats
// dropped, so 99.7% of it was the duplication. Smoothing removes the stair-step
// whether or not it removes any jitter, which is how a run could report a 91.6%
// reduction and look identical in Gyroflow.
//
// The mask comes from the before-series and is applied to both, so the two are
// compared on one time grid. correct.AngularAccelerationScore keeps the
// reference's exact behaviour for golden parity and is not affected.
func scoreEvents(times []float64, before, after []quat.Q, events []detect.Event) (float64, float64, float64, float64) {
	times, before, after = distinctOrientations(times, before, after)

	beforeScorer, err := correct.PrepareAngularAcceleration(times, before)
	if err != nil {
		return 0, 0, 0, 0
	}
	afterScorer, err := correct.PrepareAngularAcceleration(times, after)
	if err != nil {
		return 0, 0, 0, 0
	}
	// The clip-wide pair is the honest headline. The in-region figure cannot
	// fall where detection never looked, so it reads best exactly when
	// under-detection has left the footage still shaking.
	clipBefore, clipAfter := 0.0, 0.0
	if len(times) > 1 {
		clipBefore = beforeScorer.Score(times[0], times[len(times)-1])
		clipAfter = afterScorer.Score(times[0], times[len(times)-1])
	}
	weightedBefore, weightedAfter, total := 0.0, 0.0, 0.0
	for _, event := range events {
		if event.Action == detect.ActionNone {
			continue
		}
		weight := float64(event.LastPoint - event.FirstPoint + 1)
		if weight <= 0 {
			continue
		}
		scoreBefore := beforeScorer.Score(event.StartSeconds, event.EndSeconds)
		scoreAfter := afterScorer.Score(event.StartSeconds, event.EndSeconds)
		weightedBefore += scoreBefore * weight
		weightedAfter += scoreAfter * weight
		total += weight
	}
	if total == 0 {
		return 0, 0, clipBefore, clipAfter
	}
	return weightedBefore / total, weightedAfter / total, clipBefore, clipAfter
}

// distinctOrientations drops the samples DJI wrote twice, keeping one index
// mask derived from the uncorrected series and applying it to both. Correction
// smooths the repeats apart, so a mask taken per-series would compare a
// half-rate before against a full-rate after and read the difference in
// sampling as a difference in quality.
func distinctOrientations(times []float64, before, after []quat.Q) ([]float64, []quat.Q, []quat.Q) {
	keptTimes := make([]float64, 0, len(times))
	keptBefore := make([]quat.Q, 0, len(before))
	keptAfter := make([]quat.Q, 0, len(after))
	for index := range before {
		if index > 0 && before[index] == before[index-1] {
			continue
		}
		keptTimes = append(keptTimes, times[index])
		keptBefore = append(keptBefore, before[index])
		keptAfter = append(keptAfter, after[index])
	}
	return keptTimes, keptBefore, keptAfter
}

func paramsMap(params detect.Params, opts *options) map[string]any {
	return map[string]any{
		"mode":               "auto",
		"profile":            params.Profile,
		"style":              params.Style,
		"sensitivity":        params.Sensitivity,
		"mad_k":              params.MADK,
		"rel_k":              params.RelK,
		"baseline_window":    params.BaselineWindow.String(),
		"floor_dps":          params.FloorDPS,
		"min_severity":       params.MinSeverity,
		"imu_full_scale":     params.IMUFullScale,
		"bridge_max_samples": params.BridgeMaxSamples,
		"no_bridge":          params.NoBridge,
		"strength":           opts.strength,
		"smoothing_ms":       opts.smoothingMS,
	}
}

// cameraIdentity copies what the stream says about its camera into the report,
// with the serial removed. A scan report is meant to be shareable, and the
// serial identifies the owner's individual unit rather than the model and
// firmware that a comparison between reports actually turns on.
func cameraIdentity(source *pipeline.Source) *djiproto.Identity {
	identity := source.Identity
	identity.Serial = ""
	if identity.Model == "" && identity.Schema == "" && identity.Firmware == "" {
		return nil
	}
	return &identity
}
