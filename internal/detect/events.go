package detect

import (
	"math"
	"sort"

	"github.com/steamvogue/djgyrofix/internal/quat"
)

// plausibilityGate marks quaternions that the camera could not physically have
// produced. This is the line between telemetry corruption — safe to
// reconstruct — and real violent motion, which must never be reconstructed:
// bridging a genuine impact fabricates an orientation the camera never had, and
// Gyroflow will then mis-correct with full confidence.
//
// Three evidence paths may condemn a sample:
//
//  1. Paired opposing rate excursions that return to the prior trajectory.
//  2. Raw quaternion norm far from unity, before normalization.
//  3. A timestamp discontinuity or a duplicate decode time.
//
// The paired excursion may exceed full scale or form a large under-range
// reversal. A lone over-rate step is never bridgeable evidence by itself.
func plausibilityGate(times []float64, raw []quat.Q, velocities []vec3, interval float64, params Params) []bool {
	implausible := make([]bool, len(raw))
	speeds := make([]float64, len(velocities))
	for index, velocity := range velocities {
		speeds[index] = velocity.norm()
	}

	for index := range raw {
		if math.Abs(raw[index].Norm()-1.0) > params.NormTolerance {
			implausible[index] = true
			continue
		}
		if index > 0 {
			step := times[index] - times[index-1]
			if step <= 0 {
				implausible[index] = true
			} else if step > 3.0*interval && index+1 < len(times) && times[index+1] <= times[index] {
				// Non-monotonic timestamp jump anomaly, not a standard telemetry transmission gap.
				implausible[index] = true
			}
		}
	}

	// The rate test needs care about which sample it condemns. Velocity at
	// index i measures the step from i-1 to i, so a corrupt run [a..b] shows up
	// as one impossible rate at a (good into bad) and another at b+1 (bad back
	// into good) — with normal-looking rates in between, because consecutive
	// corrupt samples often carry the same wrong value and imply no motion at
	// all. Marking only the over-rate indices would therefore condemn the first
	// good sample after the run and miss the corrupt ones inside it.
	//
	// So the two impulses are paired: everything from the entry index up to,
	// but not including, the return index is the corrupt run.
	var over []int
	for index := range speeds {
		if speeds[index] > params.IMUFullScale {
			over = append(over, index)
		}
	}
	maxRun := params.BridgeMaxSamples + 1
	if maxRun < 2 {
		maxRun = 2
	}
	for position := 0; position < len(over); position++ {
		entry := over[position]
		if position+1 < len(over) && over[position+1]-entry <= maxRun {
			returnIndex := over[position+1]
			if !verifiedReturn(times, raw, velocities, speeds, entry, returnIndex, params) {
				continue
			}
			for index := entry; index < returnIndex; index++ {
				implausible[index] = true
			}
			position++
		}
	}

	// A one-sample glitch can also stay under full scale while still reversing
	// immediately — a large excursion followed by a large excursion the other
	// way, with the track back where it started.
	reversalFloor := params.IMUFullScale * 0.5 / params.Sensitivity
	for index := 1; index+1 < len(velocities); index++ {
		if speeds[index] < reversalFloor || speeds[index+1] < reversalFloor {
			continue
		}
		if !verifiedReturn(times, raw, velocities, speeds, index, index+1, params) {
			continue
		}
		implausible[index] = true
	}
	return implausible
}

// verifiedReturn distinguishes a corrupt plateau from rapid real motion. Two
// nearby over-rate transitions are only bridgeable when the second transition
// reverses the first, lands back near the pre-entry attitude, and the track has
// settled afterwards. An isolated over-rate step is reportable as a transient,
// but is never enough evidence to reconstruct samples.
func verifiedReturn(times []float64, raw []quat.Q, velocities []vec3, speeds []float64, entry, returnIndex int, params Params) bool {
	if entry <= 0 || returnIndex <= entry || returnIndex >= len(raw) {
		return false
	}
	if dot3(velocities[entry], velocities[returnIndex]) >= 0 {
		return false
	}

	before, err := quat.Normalize(raw[entry-1])
	if err != nil {
		return false
	}
	after, err := quat.Normalize(raw[returnIndex])
	if err != nil {
		return false
	}
	cosine := math.Min(1, math.Max(-1, math.Abs(quat.Dot(before, after))))
	returnDegrees := quat.Degrees(2 * math.Acos(cosine))
	elapsed := times[returnIndex] - times[entry-1]
	if elapsed <= 0 {
		return false
	}
	localSpeed := 0.0
	if entry > 1 {
		localSpeed = math.Max(localSpeed, speeds[entry-1])
	}
	if returnIndex+1 < len(speeds) {
		localSpeed = math.Max(localSpeed, speeds[returnIndex+1])
	}
	returnTolerance := math.Max(1.0, 0.5+3.0*localSpeed*elapsed)
	if returnDegrees > returnTolerance {
		return false
	}

	// Corrupt multi-sample runs normally form a nearly stationary wrong
	// plateau. Sustained large motion between or immediately after the two
	// transitions is a burst, not a safe interpolation target.
	quietLimit := math.Max(params.MotionDPS, params.IMUFullScale*0.25)
	for index := entry + 1; index < returnIndex; index++ {
		if speeds[index] > quietLimit {
			return false
		}
	}
	settleLimit := params.IMUFullScale * 0.5 / params.Sensitivity
	if returnIndex+1 < len(speeds) && speeds[returnIndex+1] > settleLimit {
		return false
	}
	return true
}

// dropoutEvents turns runs of implausible samples into events. A run longer
// than the bridge limit is still reported, but with no action: interpolating
// across it would invent more orientation than it recovers.
func dropoutEvents(times []float64, implausible []bool, params Params) []Event {
	var events []Event
	for index := 0; index < len(implausible); {
		if !implausible[index] {
			index++
			continue
		}
		first := index
		for index < len(implausible) && implausible[index] {
			index++
		}
		last := index - 1

		event := Event{
			StartSeconds: times[first],
			EndSeconds:   times[last],
			PeakSeconds:  times[(first+last)/2],
			Class:        ClassDropout,
			Action:       ActionBridge,
			Severity:     9.0,
			FirstPoint:   first,
			LastPoint:    last,
			DominantAxes: []string{},
			SpikeCount:   1,
		}
		if event.EndSeconds <= event.StartSeconds {
			// A single sample has zero duration; give it the bin width so it
			// is visible in reports and in the affected-duration total.
			event.EndSeconds = event.StartSeconds + params.BinSeconds
		}
		if event.EndSeconds > times[len(times)-1] {
			event.EndSeconds = times[len(times)-1]
		}
		length := last - first + 1
		switch {
		case params.NoBridge:
			event.Action = ActionNone
			event.Note = "reconstruction disabled by --no-bridge"
		case length > params.BridgeMaxSamples:
			event.Action = ActionNone
			event.Note = "run of " + itoa(length) + " samples exceeds --bridge-max-samples"
		case first == 0 || last == len(implausible)-1:
			event.Action = ActionNone
			event.Note = "no valid data on both sides to interpolate between"
		}
		event.SeverityLabel = severityLabel(event.Severity)
		events = append(events, event)
	}
	return events
}

// binEvents groups supra-threshold bins into events and classifies them.
func binEvents(bins binned, baselines, thresholds []float64, times []float64, params Params, implausible []bool) []Event {
	active := make([]bool, len(bins.metrics))
	for index, metric := range bins.metrics {
		active[index] = metric >= thresholds[index]
	}

	maxGapBins := quat.PyRound(params.GapSeconds / bins.width)
	if maxGapBins < 1 {
		maxGapBins = 1
	}
	paddingBins := quat.PyRound(params.PadSeconds / bins.width)
	if paddingBins < 1 {
		paddingBins = 1
	}

	var events []Event
	for index := 0; index < len(active); {
		if !active[index] {
			index++
			continue
		}
		first := index
		lastActive := index
		gap := 0
		index++
		for index < len(active) {
			if active[index] {
				lastActive = index
				gap = 0
			} else {
				gap++
				if gap > maxGapBins {
					break
				}
			}
			index++
		}
		if event, ok := classify(bins, baselines, thresholds, times, first, lastActive, paddingBins, params, implausible); ok {
			events = append(events, event)
		}
	}
	return events
}

func classify(bins binned, baselines, thresholds, times []float64, rawFirst, rawLast, paddingBins int, params Params, implausible []bool) (Event, bool) {
	activeCount := 0
	peakIndex := rawFirst
	for index := rawFirst; index <= rawLast; index++ {
		if bins.metrics[index] >= thresholds[index] {
			activeCount++
		}
		if bins.metrics[index] > bins.metrics[peakIndex] {
			peakIndex = index
		}
	}
	peakRatio := bins.metrics[peakIndex] / math.Max(thresholds[peakIndex], 1e-9)
	// A lone marginal bin is noise. Keep a single bin only when it is well
	// clear of the threshold.
	if activeCount < 2 && peakRatio < 1.7 {
		return Event{}, false
	}

	first := rawFirst - paddingBins
	if first < 0 {
		first = 0
	}
	last := rawLast + paddingBins
	if last > len(bins.metrics)-1 {
		last = len(bins.metrics) - 1
	}
	startSeconds := math.Max(times[0], bins.times[first]-bins.width/2.0)
	endSeconds := math.Min(times[len(times)-1], bins.times[last]+bins.width/2.0)

	firstPoint, lastPoint := pointSpan(bins, first, last)
	if firstPoint < 0 || lastPoint < 0 {
		return Event{}, false
	}

	var energies vec3
	motionSum := 0.0
	residualSum := 0.0
	acrossSum := 0.0
	for index := rawFirst; index <= rawLast; index++ {
		for axis := 0; axis < 3; axis++ {
			energies[axis] += bins.axisEnergy[index][axis]
		}
		motionSum += bins.motion[index] * bins.motion[index]
		residualSum += bins.metrics[index] * bins.metrics[index]
		if index < len(bins.acrossEnergy) {
			acrossSum += bins.acrossEnergy[index] * bins.acrossEnergy[index]
		}
	}
	binCount := float64(rawLast - rawFirst + 1)
	motionRMS := math.Sqrt(motionSum / binCount)
	residualRMS := math.Sqrt(residualSum / binCount)
	acrossRMS := math.Sqrt(acrossSum / binCount)

	event := Event{
		StartSeconds:  startSeconds,
		EndSeconds:    endSeconds,
		PeakSeconds:   bins.times[peakIndex],
		PeakDPS:       bins.metrics[peakIndex],
		BaselineDPS:   baselines[peakIndex],
		ThresholdDPS:  thresholds[peakIndex],
		BaselineRatio: bins.metrics[peakIndex] / math.Max(baselines[peakIndex], 1e-9),
		DominantAxes:  dominantAxes(energies),
		SpikeCount:    localPeakCount(bins.metrics[rawFirst:rawLast+1], thresholds[rawFirst:rawLast+1]),
		FirstPoint:    firstPoint,
		LastPoint:     lastPoint,
		Severity:      math.Min(10.0, 4.0+3.0*math.Log2(math.Max(1.0, peakRatio))),
	}
	event.SeverityLabel = severityLabel(event.Severity)

	// Intentional input: the low-passed rotation is fast and the residual is a
	// small fraction of it. The residual signal already cancels smooth fast
	// rotation, so this is the second line of defence — it catches a whip-pan
	// whose float32 quantization noise scales up with the rotation rate.
	if motionRMS > params.MotionDPS && residualRMS < params.MotionRatio*motionRMS {
		event.Class = ClassMotion
		event.Action = ActionNone
		event.Note = "residual tracks intentional motion"
		return event, true
	}
	// When the aircraft is turning and the residual is predominantly along the
	// local rotation axis, it represents acceleration or deceleration of the
	// turn (control input) rather than the rotation axis wobbling (airframe vibration).
	if motionRMS > params.MotionDPS*0.5 && residualRMS > 0 && len(bins.acrossEnergy) > 0 {
		acrossRatio := acrossRMS / residualRMS
		if acrossRatio < 0.35 {
			event.Class = ClassMotion
			event.Action = ActionNone
			event.Note = "residual is along rotation axis (control input)"
			return event, true
		}
	}

	duration := endSeconds - startSeconds
	if duration < 0.10 && event.SpikeCount <= 1 {
		event.Class = ClassImpact
		event.Action = ActionSmooth
		event.SmoothingMS = impactSmoothingMS(duration)
	} else {
		event.Class = ClassJitter
		event.Action = ActionSmooth
		event.SmoothingMS = jitterSmoothingMS(duration)
	}
	if params.Profile != "" && hasImplausible(implausible, firstPoint, lastPoint) {
		event.Note = "contains samples that failed the plausibility gate"
	}
	return event, true
}

// impactSmoothingMS and jitterSmoothingMS derive the blur window per event.
// One global 180 ms is wrong for both classes: it over-smooths a 40 ms spike
// and under-smooths a two-second jitter run.
func impactSmoothingMS(duration float64) float64 {
	return math.Min(100.0, math.Max(60.0, duration*1000.0*1.5))
}

func jitterSmoothingMS(duration float64) float64 {
	return math.Min(400.0, math.Max(120.0, duration*1000.0*0.5))
}

func pointSpan(bins binned, first, last int) (int, int) {
	firstPoint := -1
	for index := first; index <= last; index++ {
		if bins.firstPoint[index] >= 0 {
			firstPoint = bins.firstPoint[index]
			break
		}
	}
	lastPoint := -1
	for index := last; index >= first; index-- {
		if bins.lastPoint[index] >= 0 {
			lastPoint = bins.lastPoint[index]
			break
		}
	}
	return firstPoint, lastPoint
}

func hasImplausible(implausible []bool, first, last int) bool {
	for index := first; index <= last && index < len(implausible); index++ {
		if implausible[index] {
			return true
		}
	}
	return false
}

func dominantAxes(energies vec3) []string {
	names := [3]string{"X", "Y", "Z"}
	maximum := math.Max(energies[0], math.Max(energies[1], energies[2]))
	if maximum <= 0 {
		return []string{}
	}
	order := []int{0, 1, 2}
	sort.SliceStable(order, func(a, b int) bool { return energies[order[a]] > energies[order[b]] })
	axes := []string{}
	for _, axis := range order {
		if energies[axis] >= maximum*0.35 {
			axes = append(axes, names[axis])
		}
	}
	return axes
}

// localPeakCount counts distinct supra-threshold maxima at least three bins
// apart, which separates a single impact from sustained jitter.
func localPeakCount(values []float64, thresholds []float64) int {
	var candidates []int
	if len(values) > 1 && values[0] >= thresholds[0] && values[0] > values[1] {
		candidates = append(candidates, 0)
	}
	for index := 1; index+1 < len(values); index++ {
		if values[index] >= thresholds[index] &&
			values[index] >= values[index-1] &&
			values[index] > values[index+1] {
			candidates = append(candidates, index)
		}
	}
	last := len(values) - 1
	if len(values) > 1 && values[last] >= thresholds[last] && values[last] >= values[last-1] {
		candidates = append(candidates, last)
	}
	sort.SliceStable(candidates, func(a, b int) bool { return values[candidates[a]] > values[candidates[b]] })
	var selected []int
	for _, candidate := range candidates {
		separated := true
		for _, existing := range selected {
			if abs(candidate-existing) < 3 {
				separated = false
				break
			}
		}
		if separated {
			selected = append(selected, candidate)
		}
	}
	if len(selected) < 1 {
		return 1
	}
	return len(selected)
}

// dropOverlaps removes threshold events that a bridged dropout already
// explains. A corrupt sample spikes the residual as violently as it breaks the
// orientation, so the same artifact otherwise appears twice — once to be
// bridged and once to be smoothed.
//
// Only short events are dropped. A long jitter run that happens to contain a
// dropout is a real artifact in its own right, and both corrections apply: the
// bridge repairs the bad samples and the envelope smooths the rest, with the
// envelope skipping the bridged samples so the two cannot fight.
const dropoutShadowSeconds = 0.15

func dropOverlaps(events []Event) []Event {
	kept := make([]Event, 0, len(events))
	for _, event := range events {
		if event.Class == ClassDropout {
			kept = append(kept, event)
			continue
		}
		covered := false
		for _, other := range events {
			if other.Class != ClassDropout || other.Action != ActionBridge {
				continue
			}
			overlaps := other.FirstPoint <= event.LastPoint && other.LastPoint >= event.FirstPoint
			if overlaps && event.DurationSeconds() < dropoutShadowSeconds {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, event)
		}
	}
	return kept
}

func severityLabel(score float64) string {
	switch {
	case score < 6.0:
		return "low"
	case score < 8.2:
		return "medium"
	default:
		return "high"
	}
}

// weightEnvelope builds a confidence floor across each confirmed event and a
// smooth shoulder outside it. The detection threshold answers whether an event
// exists; it must not also make correction vanish exactly where the event first
// crosses that threshold.
func weightEnvelope(bins binned, thresholds []float64, times []float64, events []Event, params Params, interval float64) []float64 {
	weights := make([]float64, len(times))
	binWeights := make([]float64, len(bins.metrics))
	for index, metric := range bins.metrics {
		threshold := thresholds[index]
		if threshold <= 0 {
			continue
		}
		excess := (metric - threshold) / (params.EnvelopeK * threshold)
		binWeights[index] = quat.Smoothstep(math.Min(1.0, math.Max(0.0, excess)))
	}

	shoulderPoints := quat.PyRound(params.EnvelopeBlurSeconds / interval)
	if shoulderPoints < 1 {
		shoulderPoints = 1
	}
	for _, event := range events {
		if event.Action != ActionSmooth {
			continue
		}
		first := max(0, event.FirstPoint)
		last := min(len(times)-1, event.LastPoint)
		if last < first {
			continue
		}
		// Confirmed low-severity events still receive half correction. Confidence
		// rises with severity, while a high-excess bin may lift the entire event
		// to full correction. One constant core weight matters: changing blend
		// strength every 10 ms can itself create angular-rate steps.
		floor := math.Min(1, math.Max(0.5, 0.5+0.1*(event.Severity-5)))
		coreWeight := floor
		for index := first; index <= last; index++ {
			binIndex := binIndexAt(bins, times[index])
			if binIndex >= 0 && binIndex < len(binWeights) {
				coreWeight = math.Max(coreWeight, binWeights[binIndex])
			}
		}
		for index := first; index <= last; index++ {
			weights[index] = math.Max(weights[index], coreWeight)
		}
		for offset := 1; offset <= shoulderPoints; offset++ {
			amount := quat.Smoothstep(1 - float64(offset)/float64(shoulderPoints+1))
			weight := coreWeight * amount
			if index := first - offset; index >= 0 && weight > weights[index] {
				weights[index] = weight
			}
			if index := last + offset; index < len(weights) && weight > weights[index] {
				weights[index] = weight
			}
		}
	}
	for index := range weights {
		weights[index] = math.Min(1, math.Max(0, weights[index]))
	}
	return weights
}

func binIndexAt(bins binned, seconds float64) int {
	index := int((seconds - bins.startSeconds) / bins.width)
	if index < 0 {
		return 0
	}
	if index >= len(bins.metrics) {
		return len(bins.metrics) - 1
	}
	return index
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func itoa(value int) string {
	// strconv would do, but detect stays free of formatting imports so the
	// numeric core has nothing to accidentally depend on.

	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
