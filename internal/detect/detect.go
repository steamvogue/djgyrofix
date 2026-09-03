package detect

import (
	"math"
	"sort"

	"github.com/steamvogue/djgyrofix/internal/pipeline"
	"github.com/steamvogue/djgyrofix/internal/quat"
)

// Event is one detected artifact.
type Event struct {
	StartSeconds float64 `json:"start"`
	EndSeconds   float64 `json:"end"`
	PeakSeconds  float64 `json:"peak"`
	Class        Class   `json:"type"`
	Action       Action  `json:"action"`

	Severity      float64 `json:"severity"`
	SeverityLabel string  `json:"severity_label"`
	PeakDPS       float64 `json:"peak_dps"`
	BaselineDPS   float64 `json:"baseline_dps"`
	ThresholdDPS  float64 `json:"threshold_dps"`
	BaselineRatio float64 `json:"baseline_ratio"`

	DominantAxes []string `json:"axes"`
	SpikeCount   int      `json:"peaks"`
	// SmoothingMS is the blur window derived for this event, zero for a bridge.
	SmoothingMS float64 `json:"smoothing_ms,omitempty"`
	// FirstPoint and LastPoint bound the affected quaternions, inclusive.
	FirstPoint int `json:"first_point"`
	LastPoint  int `json:"last_point"`
	// Note explains a refusal, when there is one.
	Note string `json:"note,omitempty"`
}

// DurationSeconds is the event length.
func (e Event) DurationSeconds() float64 { return e.EndSeconds - e.StartSeconds }

// Result is everything pass 1 produced.
type Result struct {
	QuaternionCount  int     `json:"quaternion_count"`
	SampleRate       float64 `json:"sample_rate"`
	SampleInterval   float64 `json:"-"`
	DurationSeconds  float64 `json:"duration_seconds"`
	BaselineDPS      float64 `json:"baseline_dps"`
	ThresholdDPS     float64 `json:"threshold_dps"`
	Rolling          bool    `json:"rolling_baseline"`
	Events           []Event `json:"events"`
	AffectedSeconds  float64 `json:"affected_seconds"`
	AffectedFraction float64 `json:"affected_fraction"`

	// Noise describes the residual floor across the clip, which the rolling
	// threshold otherwise hides.
	Noise NoiseProfile `json:"noise"`
	// Kinetics describes how hard the aircraft was turning, which decides
	// whether stabilization has a chance regardless of how clean the record is.
	Kinetics Kinetics `json:"kinetics"`
	// NearMiss counts events that were dropped only because they scored just
	// under MinSeverity. A cluster of them means a stricter profile than the
	// footage needs.
	NearMiss int `json:"near_miss_events"`
	// DuplicateShare is the fraction of stored quaternions identical to their
	// predecessor. DJI oversamples, so on real footage this sits at 0.5.
	DuplicateShare float64 `json:"duplicate_share"`

	// Weights is the per-quaternion correction envelope w(t) from plan §6.2.
	// It is already masked to actionable smoothing events and blurred, so a
	// caller can slerp toward the filtered track by w without any edge
	// handling of its own.
	Weights []float64 `json:"-"`
	// Implausible marks quaternions that failed the physical plausibility
	// gate; only these are eligible for reconstruction.
	Implausible []bool `json:"-"`
	// Residual is the per-sample deviation of angular velocity from its own
	// low-passed copy, in degrees per second. Detection works on this binned
	// into 10 ms energy; run-repair needs it per sample, because the artifact
	// lives in runs far shorter than a bin.
	Residual []float64 `json:"-"`
	// Trend is the per-sample low-passed angular speed in degrees per second,
	// which is what a replaced run has to stay consistent with.
	Trend []float64 `json:"-"`
	// ResidualAcross is the part of the residual perpendicular to the local
	// rotation axis — the axis wobbling rather than the rate along it varying.
	//
	// Measured across one aggressive manoeuvre on the real clip, this is what
	// tells the two apart. Through a 366 °/s exit rotation the residual is 29%
	// across-axis: the aircraft turned faster than the local trend about the
	// same axis, which is flying. Through the wobble 400 ms later it is 92%
	// across-axis. The plain magnitude sees one number for both.
	ResidualAcross []float64 `json:"-"`
	// PointThreshold is the detection threshold interpolated onto each sample.
	PointThreshold []float64 `json:"-"`
}

type vec3 [3]float64

func (v vec3) norm() float64 {
	total := float64(v[0] * v[0])
	total += float64(v[1] * v[1])
	total += float64(v[2] * v[2])
	return math.Sqrt(total)
}

func dot3(a, b vec3) float64 {
	total := float64(a[0] * b[0])
	total += float64(a[1] * b[1])
	total += float64(a[2] * b[2])
	return total
}

// Run executes pass 1 over the whole point series.
func Run(points []pipeline.Point, params Params) (*Result, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	result := &Result{Events: []Event{}, Weights: make([]float64, len(points))}
	if len(points) < 20 {
		return result, nil
	}

	times := pipeline.Times(points)
	rawValues := pipeline.Values(points)
	rate, interval, ok := pipeline.SampleRate(times)
	if !ok {
		return result, nil
	}
	result.QuaternionCount = len(points)
	result.SampleRate = rate
	result.SampleInterval = interval
	result.DurationSeconds = times[len(times)-1] - times[0]

	// Sign-unwrap before differencing, so the double cover does not read as a
	// 180-degree jump every time the source flips sign.
	normalized := make([]quat.Q, len(rawValues))
	for index, value := range rawValues {
		unit, err := quat.Normalize(value)
		if err != nil {
			return nil, err
		}
		normalized[index] = unit
	}
	continuous := quat.Unwrap(normalized)

	result.DuplicateShare = duplicatePairShare(rawValues)
	collapse := result.DuplicateShare >= structuralDuplicateShare
	velocities, err := angularVelocities(times, continuous, interval, collapse)
	if err != nil {
		return nil, err
	}
	result.Kinetics = kinetics(velocities, interval, params.VideoFPS)

	lowpassRadius := quat.PyRound(params.LowpassSeconds / interval)
	if lowpassRadius < 2 {
		lowpassRadius = 2
	}
	lowpass := boxBlurVec3(velocities, lowpassRadius)
	residuals := make([]vec3, len(velocities))
	for index := range velocities {
		for axis := 0; axis < 3; axis++ {
			residuals[index][axis] = velocities[index][axis] - lowpass[index][axis]
		}
	}

	result.Residual, result.Trend, result.ResidualAcross = pointSeries(residuals, lowpass)
	result.Implausible = plausibilityGate(times, rawValues, velocities, interval, params)

	bins := binResiduals(times, residuals, lowpass, result.ResidualAcross, params.BinSeconds)
	if len(bins.metrics) == 0 {
		return result, nil
	}
	baselines, thresholds, rolling := thresholdCurve(bins, params, result.DurationSeconds)
	result.Rolling = rolling
	result.BaselineDPS = quat.Median(baselines)
	result.ThresholdDPS = quat.Median(thresholds)
	result.Noise = noiseProfile(baselines, params, bins.width)

	events := dropoutEvents(times, result.Implausible, params)
	events = append(events, binEvents(bins, baselines, thresholds, times, params, result.Implausible)...)
	sort.SliceStable(events, func(a, b int) bool { return events[a].StartSeconds < events[b].StartSeconds })
	events = dropOverlaps(events)

	kept := events[:0]
	for _, event := range events {
		if event.Class != ClassDropout && event.Severity < params.MinSeverity {
			if event.Severity >= params.MinSeverity-nearMissSeverity {
				result.NearMiss++
			}
			continue
		}
		kept = append(kept, event)
	}
	result.Events = append([]Event{}, kept...)

	for _, event := range result.Events {
		if event.Action != ActionNone {
			result.AffectedSeconds += event.DurationSeconds()
		}
	}
	if result.DurationSeconds > 0 {
		result.AffectedFraction = result.AffectedSeconds / result.DurationSeconds
	}
	result.Weights = weightEnvelope(bins, thresholds, times, result.Events, params, interval)
	result.PointThreshold = pointThresholds(bins, thresholds, times)
	return result, nil
}

// pointSeries flattens the per-axis residual and low-passed velocity into the
// per-sample magnitudes run-repair works from, and splits the residual across
// the local rotation axis.
//
// The split is the whole point. A residual pointing along the axis the aircraft
// is already turning about means it turned faster or slower than the local
// trend, which is what flying looks like. A residual perpendicular to that axis
// means the axis moved, which is what the artifact looks like. Taking the plain
// magnitude of the residual throws that distinction away, and it is the
// distinction between correcting a wobble and cutting into a real manoeuvre.
func pointSeries(residuals, lowpass []vec3) ([]float64, []float64, []float64) {
	residual := make([]float64, len(residuals))
	trend := make([]float64, len(lowpass))
	across := make([]float64, len(residuals))
	for index := range residuals {
		residual[index] = residuals[index].norm()
		speed := lowpass[index].norm()
		trend[index] = speed
		if speed < 1e-9 {
			// With no local rotation there is no axis to be across, so every
			// deviation counts.
			across[index] = residual[index]
			continue
		}
		unit := vec3{lowpass[index][0] / speed, lowpass[index][1] / speed, lowpass[index][2] / speed}
		projection := dot3(residuals[index], unit)
		var perpendicular vec3
		for axis := 0; axis < 3; axis++ {
			perpendicular[axis] = residuals[index][axis] - projection*unit[axis]
		}
		across[index] = perpendicular.norm()
	}
	return residual, trend, across
}

// pointThresholds maps the per-bin threshold curve onto every sample, so a run
// is judged against the same bar its enclosing event was.
func pointThresholds(bins binned, thresholds []float64, times []float64) []float64 {
	out := make([]float64, len(times))
	if len(thresholds) == 0 {
		return out
	}
	for index, time := range times {
		bin := int((time - bins.startSeconds) / bins.width)
		if bin < 0 {
			bin = 0
		}
		if bin >= len(thresholds) {
			bin = len(thresholds) - 1
		}
		out[index] = thresholds[bin]
	}
	return out
}

// angularVelocities converts orientations into an angular velocity vector in
// degrees per second, ported from the reference's _rotation_velocity.
//
// The difference is taken against the last *distinct* orientation rather than
// the immediately preceding sample, because DJI stores each fused attitude
// twice. Both real O4 clips measured here are exactly 50.00% duplicate pairs —
// 496,759 runs of length two in one, 227,756 in the other, and five runs of any
// other length between them — so the stream presents 1978 Hz while carrying
// 989 Hz of information.
//
// Differencing consecutive stored samples turns that into a square wave at
// Nyquist: every other velocity is exactly zero and the rest are double the
// true rate. Its amplitude scales with how fast the aircraft is rotating, so it
// is largest exactly where the detector is looking. On the fast clip it
// accounted for three quarters of the whole-clip residual (118.4 °/s RMS as
// stored, 31.6 °/s differenced properly) and it inflated the apparent noise
// floor from 3.2 °/s to 37.9 °/s, dragging every threshold up with it. Real
// artifacts then hid inside phantom noise several times their own size.
//
// The collapse is decided per file rather than applied blindly, because a short
// run of identical orientations is also what a frozen telemetry dropout looks
// like, and locally the two are the same shape. Only the whole-stream statistic
// tells them apart: structural oversampling covers half the file at a fixed
// parity, a dropout covers two samples in thousands. See duplicatePairShare.
func angularVelocities(times []float64, quaternions []quat.Q, interval float64, collapse bool) ([]vec3, error) {
	velocities := make([]vec3, len(quaternions))
	previous := 0
	for index := 1; index < len(quaternions); index++ {
		if collapse && quaternions[index] == quaternions[previous] {
			// No new information here. Hold the velocity of the step this
			// sample belongs to, so the series stays the same length as the
			// stored one and every downstream index still lines up.
			velocities[index] = velocities[index-1]
			continue
		}
		step := times[index] - times[previous]
		if step < interval {
			step = interval
		}
		inverse, err := quat.Inverse(quaternions[previous])
		if err != nil {
			return nil, err
		}
		delta, err := quat.Multiply(quaternions[index], inverse)
		if err != nil {
			return nil, err
		}
		previous = index
		if delta[0] < 0.0 {
			delta = delta.Neg()
		}
		vectorNorm := vec3{delta[1], delta[2], delta[3]}.norm()
		if vectorNorm < 1e-12 || step <= 0.0 {
			continue
		}
		angle := 2.0 * math.Atan2(vectorNorm, math.Max(0.0, delta[0]))
		scale := quat.Degrees(angle) / (vectorNorm * step)
		velocities[index] = vec3{delta[1] * scale, delta[2] * scale, delta[3] * scale}
	}
	return velocities, nil
}

func boxBlurVec3(values []vec3, radius int) []vec3 {
	if radius <= 0 {
		return append([]vec3(nil), values...)
	}
	var prefixes [3][]float64
	for axis := range prefixes {
		prefixes[axis] = make([]float64, len(values)+1)
	}
	for index, value := range values {
		for axis := 0; axis < 3; axis++ {
			prefixes[axis][index+1] = prefixes[axis][index] + value[axis]
		}
	}
	output := make([]vec3, len(values))
	for index := range values {
		first := index - radius
		if first < 0 {
			first = 0
		}
		last := index + radius + 1
		if last > len(values) {
			last = len(values)
		}
		count := float64(last - first)
		for axis := 0; axis < 3; axis++ {
			output[index][axis] = (prefixes[axis][last] - prefixes[axis][first]) / count
		}
	}
	return output
}

// binned holds residual energy aggregated into fixed-width time bins.
type binned struct {
	startSeconds float64
	width        float64
	times        []float64
	metrics      []float64
	axisEnergy   []vec3
	acrossEnergy []float64
	// motion is the RMS low-passed speed per bin, used to tell intentional
	// fast motion from an artifact.
	motion []float64
	// firstPoint and lastPoint map each bin back to the point series.
	firstPoint []int
	lastPoint  []int
}

func binResiduals(times []float64, residuals, lowpass []vec3, across []float64, binSeconds float64) binned {
	start := times[0]
	span := times[len(times)-1] - start
	count := int(math.Ceil(span / binSeconds))
	if count < 1 {
		count = 1
	}
	bins := binned{
		startSeconds: start,
		width:        binSeconds,
		times:        make([]float64, count),
		metrics:      make([]float64, count),
		axisEnergy:   make([]vec3, count),
		acrossEnergy: make([]float64, count),
		motion:       make([]float64, count),
		firstPoint:   make([]int, count),
		lastPoint:    make([]int, count),
	}
	sums := make([]float64, count)
	acrossSums := make([]float64, count)
	motionSums := make([]float64, count)
	counts := make([]int, count)
	for index := range bins.firstPoint {
		bins.firstPoint[index] = -1
		bins.lastPoint[index] = -1
	}
	for index, time := range times {
		binIndex := int((time - start) / binSeconds)
		if binIndex < 0 {
			binIndex = 0
		}
		if binIndex >= count {
			binIndex = count - 1
		}
		sums[binIndex] += dot3(residuals[index], residuals[index])
		motionSums[binIndex] += dot3(lowpass[index], lowpass[index])
		if index < len(across) {
			acrossSums[binIndex] += across[index] * across[index]
		}
		counts[binIndex]++
		for axis := 0; axis < 3; axis++ {
			bins.axisEnergy[binIndex][axis] += residuals[index][axis] * residuals[index][axis]
		}
		if bins.firstPoint[binIndex] < 0 {
			bins.firstPoint[binIndex] = index
		}
		bins.lastPoint[binIndex] = index
	}
	for index := 0; index < count; index++ {
		bins.times[index] = start + (float64(index)+0.5)*binSeconds
		if counts[index] > 0 {
			bins.metrics[index] = math.Sqrt(sums[index] / float64(counts[index]))
			bins.acrossEnergy[index] = math.Sqrt(acrossSums[index] / float64(counts[index]))
			bins.motion[index] = math.Sqrt(motionSums[index] / float64(counts[index]))
		}
	}
	return bins
}

// thresholdCurve computes a per-bin detection threshold.
//
// The reference computes one global baseline over the analysis window, which is
// right for a hand-picked three-second range and wrong for a whole-file scan.
// Two things go wrong, and they are not the same thing:
//
// The common one is over-detection. A global threshold is set by the quiet
// parts of the clip, so a genuinely rough stretch sits far above it end to end
// and is flagged wholesale. Measured on a synthetic 60-second clip that is
// rough for its first 20 seconds, the global baseline flags 35% of the footage
// where a rolling one flags 2%. Blanket-smoothing a third of a clip degrades
// stabilization everywhere and fixes nothing.
//
// The rarer one is blindness. The reference's baseline is the median of the
// quietest 55% of bins, which is the 27.5th percentile overall — considerably
// more robust to one bad segment than the plain median it is often described
// as. It only fails once the rough part crowds the calm part out of that
// slice, past roughly 72% coverage, and then it stops seeing anything at all.
//
// A sliding Hampel window fixes both, at the cost of a threshold that is no
// longer a single reportable number.
//
// The 1.4826 factor is what makes "5 sigma" actually mean five sigma; most
// informal descriptions of Hampel omit it and silently change sensitivity.
func thresholdCurve(bins binned, params Params, durationSeconds float64) ([]float64, []float64, bool) {
	count := len(bins.metrics)
	baselines := make([]float64, count)
	thresholds := make([]float64, count)
	floor := params.FloorDPS / params.Sensitivity

	if durationSeconds < params.ShortClipSeconds {
		baseline, sigma := globalBaseline(bins.metrics)
		threshold := combineThreshold(baseline, sigma, floor, params)
		for index := range baselines {
			baselines[index] = baseline
			thresholds[index] = threshold
		}
		return baselines, thresholds, false
	}

	// A rolling window has to be short enough to actually roll. The window is a
	// half-width, so it spans twice this either side of each bin; letting it
	// reach much past half the clip turns the "rolling" baseline back into the
	// global one this replaced, and localized roughness stops registering as
	// anything but the clip's own level. Capping the span at half the clip
	// keeps a long window useful on long footage — where it belongs — without
	// flattening a thirty-second one.
	window := params.BaselineWindow.Seconds()
	if longest := durationSeconds / 4; window > longest {
		window = longest
	}
	halfWidth := int(math.Round(window / bins.width))
	if halfWidth < 1 {
		halfWidth = 1
	}
	// Evaluating a full median at every bin would be O(bins x window); a
	// twelve-minute clip is 72000 bins against a 1000-bin window. The curve is
	// smooth by construction, so it is evaluated on a stride and interpolated.
	stride := halfWidth / 20
	if stride < 1 {
		stride = 1
	}
	var knotIndices []int
	var knotBaselines, knotSigmas []float64
	scratch := make([]float64, 0, 2*halfWidth+1)
	for index := 0; index < count; index += stride {
		first := index - halfWidth
		if first < 0 {
			first = 0
		}
		last := index + halfWidth + 1
		if last > count {
			last = count
		}
		scratch = scratch[:0]
		scratch = append(scratch, bins.metrics[first:last]...)
		baseline := quat.Median(scratch)
		for position := range scratch {
			scratch[position] = math.Abs(scratch[position] - baseline)
		}
		knotIndices = append(knotIndices, index)
		knotBaselines = append(knotBaselines, baseline)
		knotSigmas = append(knotSigmas, 1.4826*quat.Median(scratch))
	}
	if knotIndices[len(knotIndices)-1] != count-1 {
		knotIndices = append(knotIndices, count-1)
		knotBaselines = append(knotBaselines, knotBaselines[len(knotBaselines)-1])
		knotSigmas = append(knotSigmas, knotSigmas[len(knotSigmas)-1])
	}
	for index := range baselines {
		baseline := interpolate(knotIndices, knotBaselines, index)
		sigma := interpolate(knotIndices, knotSigmas, index)
		baselines[index] = baseline
		thresholds[index] = combineThreshold(baseline, sigma, floor, params)
	}
	return baselines, thresholds, true
}

// globalBaseline reproduces the reference's quietest-55% estimate, used on
// clips too short for a rolling window to mean anything.
func globalBaseline(metrics []float64) (float64, float64) {
	nonzero := make([]float64, 0, len(metrics))
	for _, metric := range metrics {
		if metric > 0 {
			nonzero = append(nonzero, metric)
		}
	}
	if len(nonzero) == 0 {
		return 0, 0
	}
	sort.Float64s(nonzero)
	quietCount := quat.PyRound(float64(len(nonzero)) * 0.55)
	if quietCount < 5 {
		quietCount = 5
	}
	if quietCount > len(nonzero) {
		quietCount = len(nonzero)
	}
	quiet := nonzero[:quietCount]
	baseline := quat.Median(quiet)
	deviations := make([]float64, len(quiet))
	for index, metric := range quiet {
		deviations[index] = math.Abs(metric - baseline)
	}
	return baseline, 1.4826 * quat.Median(deviations)
}

func combineThreshold(baseline, sigma, floor float64, params Params) float64 {
	fromSigma := baseline + params.MADK*sigma/params.Sensitivity
	fromRelative := baseline * params.RelK / params.Sensitivity
	return math.Max(floor, math.Max(fromSigma, fromRelative))
}

func interpolate(knots []int, values []float64, index int) float64 {
	position := sort.SearchInts(knots, index)
	if position >= len(knots) {
		return values[len(values)-1]
	}
	if knots[position] == index || position == 0 {
		return values[position]
	}
	left, right := knots[position-1], knots[position]
	if right == left {
		return values[position]
	}
	fraction := float64(index-left) / float64(right-left)
	return values[position-1] + fraction*(values[position]-values[position-1])
}
