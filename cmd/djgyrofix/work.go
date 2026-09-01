package main

import (
	"encoding/binary"
	"fmt"
	"math"

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
	result.report.SampleRate = detected.SampleRate
	result.report.BaselineDPS = detected.BaselineDPS
	result.report.ThresholdDPS = detected.ThresholdDPS
	result.report.RollingBaseline = detected.Rolling
	result.report.Events = detected.Events
	result.report.AffectedSeconds = detected.AffectedSeconds
	result.report.AffectedFraction = detected.AffectedFraction
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
	corrected, err := correct.Envelope(times, values, detected, correct.EnvelopeOptions{
		Strength:    opts.strength,
		SmoothingMS: opts.smoothingMS,
	})
	if err != nil {
		return err
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

	before, after := scoreEvents(times, values, corrected, detected.Events)
	result.report.ScoreBefore = before
	result.report.ScoreAfter = after
	return nil
}

// analyzeRanges is the manual path, and the golden-parity path.
//
// It reproduces the reference exactly: one window per interval with the same
// context padding, the same fixed 180 ms default window, and the same
// accumulation of already-patched samples so a later interval's context sees
// the earlier interval's output.
func analyzeRanges(source *pipeline.Source, opts *options, intervals []interval, result *analysis) error {
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

// scoreEvents measures the residual reduction over the corrected regions only.
// Averaging over the whole clip would dilute a real improvement to nothing.
func scoreEvents(times []float64, before, after []quat.Q, events []detect.Event) (float64, float64) {
	weightedBefore, weightedAfter, total := 0.0, 0.0, 0.0
	for _, event := range events {
		if event.Action == detect.ActionNone {
			continue
		}
		weight := float64(event.LastPoint - event.FirstPoint + 1)
		if weight <= 0 {
			continue
		}
		scoreBefore, err := correct.AngularAccelerationScore(times, before, event.StartSeconds, event.EndSeconds)
		if err != nil {
			continue
		}
		scoreAfter, err := correct.AngularAccelerationScore(times, after, event.StartSeconds, event.EndSeconds)
		if err != nil {
			continue
		}
		weightedBefore += scoreBefore * weight
		weightedAfter += scoreAfter * weight
		total += weight
	}
	if total == 0 {
		return 0, 0
	}
	return weightedBefore / total, weightedAfter / total
}

func paramsMap(params detect.Params, opts *options) map[string]any {
	return map[string]any{
		"mode":               "auto",
		"profile":            params.Profile,
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
