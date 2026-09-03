package detect

import (
	"math"
	"sort"

	"github.com/steamvogue/djgyrofix/internal/quat"
)

// NoiseProfile summarises the residual noise floor across a whole clip.
//
// The rolling Hampel threshold is what makes whole-file detection viable, and
// it has one consequence that a single reported baseline number hides: the
// threshold rises with the local noise floor, so a stretch of badly mounted,
// resonating footage raises its own bar and goes quiet. A near-empty event list
// over such a stretch is not a clean bill of health, and nothing in a per-event
// report says so. These statistics are what lets the report say it out loud.
type NoiseProfile struct {
	// P10, P50 and P90 are percentiles of the per-bin baseline curve. A wide
	// spread means the clip is clean in places and rough in others; the
	// reported median alone cannot distinguish that from uniformly clean.
	P10 float64 `json:"p10_dps"`
	P50 float64 `json:"p50_dps"`
	P90 float64 `json:"p90_dps"`
	// NoisyFraction is the share of the clip whose local noise floor sits at or
	// above NoisyDPS.
	NoisyFraction float64 `json:"noisy_fraction"`
	// NoisySeconds is that share as a duration.
	NoisySeconds float64 `json:"noisy_seconds"`
	// NoisyDPS is the level the fractions were measured against. It scales with
	// --floor-dps and --sensitivity instead of hardcoding an absolute number.
	NoisyDPS float64 `json:"noisy_dps"`
	// FloorDPS is the effective detection floor NoisyDPS was derived from.
	FloorDPS float64 `json:"floor_dps"`
}

// noiseProfile summarises the baseline curve produced by thresholdCurve.
//
// The comparison level is absolute and does not move with --floor-dps or
// --profile, which was the first attempt and was wrong twice over.
//
// Wrong in principle: how much a frame resonates is a property of the airframe.
// It has nothing to do with how aggressively the pilot asked to detect events,
// so a level derived from the detection floor makes the same footage change
// diagnosis when only the search did.
//
// Wrong in practice: half the floor separated the synthetic fixtures easily —
// the clean one idles at 0.6 °/s and the rough one at 116 °/s, a gap wide
// enough that any threshold between them looks correct — but real FPV footage
// carries a far higher residual floor than a generated clean track. The 8m17s
// clip in docs/FINDINGS.md sits at 39.2 °/s typical and 66.7 °/s at p90, and it
// is demonstrably repairable: 6.17% flagged, a 91.6% residual reduction, a
// rescan that comes back empty. Both the original level and a floor-scaled
// replacement called that clip an airframe problem, which is the worst error
// this package can make — it sends a pilot to re-mount a camera over footage
// the tool would have fixed.
func noiseProfile(baselines []float64, params Params, binSeconds float64) NoiseProfile {
	profile := NoiseProfile{NoisyDPS: noisyFloorDPS, FloorDPS: effectiveFloor(params)}
	if len(baselines) == 0 {
		return profile
	}
	sorted := append([]float64(nil), baselines...)
	sort.Float64s(sorted)
	profile.P10 = percentile(sorted, 0.10)
	profile.P50 = percentile(sorted, 0.50)
	profile.P90 = percentile(sorted, 0.90)

	noisy := 0
	for _, baseline := range baselines {
		if baseline >= profile.NoisyDPS {
			noisy++
		}
	}
	profile.NoisyFraction = float64(noisy) / float64(len(baselines))
	profile.NoisySeconds = float64(noisy) * binSeconds
	return profile
}

// noisyFloorDPS is where a residual floor stops being background and starts
// being the defect, in degrees per second.
//
// The anchors below it are two real O4 clips measured with DJI's duplicated
// samples collapsed: a clip its owner considers entirely normal reads 2.0 °/s
// typical and 2.8 °/s at p90, and the clip with real transient artifacts reads
// 3.4 °/s and 4.3 °/s — its problem is bursts, not floor. The anchor above it
// is a synthetic clip resonating end to end, whose floor never drops below
// 113 °/s. Forty-five sits about ten times over the highest real reading and
// about two and a half times under the synthetic one.
//
// An earlier value of 90 was derived before the duplication was understood,
// from an apparent floor of 39 °/s that was roughly 92% sampling artifact. The
// upper anchor is still generated rather than observed: no measurement of a
// genuinely badly-mounted airframe exists here yet, so the margin below is
// deliberately the generous one. Expect this to move. See Known limits.
const noisyFloorDPS = 45.0

// effectiveFloor is the absolute floor thresholdCurve actually applies, after
// --sensitivity has scaled it.
func effectiveFloor(params Params) float64 {
	if params.Sensitivity <= 0 {
		return params.FloorDPS
	}
	return params.FloorDPS / params.Sensitivity
}

// percentile reads a fraction out of an already-sorted slice by linear
// interpolation between neighbours.
func percentile(sorted []float64, fraction float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	position := fraction * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := lower + 1
	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	weight := position - float64(lower)
	return sorted[lower] + weight*(sorted[upper]-sorted[lower])
}

// nearMissSeverity is how far under MinSeverity an event may score and still
// count as a near miss. One and a half points is roughly the gap between two
// adjacent profiles' severity cuts, so a cluster inside it is exactly the case
// where the next profile down would have kept them.
const nearMissSeverity = 1.5

// structuralDuplicateShare is the fraction of identical consecutive pairs above
// which a stream is treated as oversampled rather than as carrying occasional
// repeats.
//
// The padding is a consequence of frame rate, not a fixed property of DJI's
// format. Every clip measured packs about 33.3 quaternion slots into each video
// frame and fills them from an IMU running near 1000 Hz, so the repeat factor is
// 33.3 x fps / 1000: one at 30 fps and two at 60, which is exactly what the
// three measured clips show — 0.00 duplicate share on a 29.97 fps Osmo and
// 0.5000 on both 59.94 fps air units.
//
// That makes the intermediate rates the ones this threshold has to survive. A
// 48 or 50 fps clip pads by a fractional 1.6 to 1.66, giving a duplicate share
// near 0.37 to 0.40 — under the 0.4 this constant used to hold, which would have
// left the stream uncollapsed and differenced into a square wave at Nyquist.
// There is no such clip here to test against; the value below is set from the
// model rather than from a measurement, and is deliberately far from both ends
// of it. A genuine frozen dropout is two samples in a million, six orders of
// magnitude below this; the smallest real padding is 0.37, more than twice it.
const structuralDuplicateShare = StructuralDuplicateShare

// StructuralDuplicateShare is exported so the report can describe the same
// stream the detector collapsed. Two copies of this number drift apart the
// moment one of them is tuned, which is exactly what happened when the value
// moved from 0.4 and the report kept describing the old boundary.
const StructuralDuplicateShare = 0.15

// duplicatePairShare is the fraction of quaternions identical to the one before
// them.
func duplicatePairShare(values []quat.Q) float64 {
	if len(values) < 2 {
		return 0
	}
	identical := 0
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			identical++
		}
	}
	return float64(identical) / float64(len(values)-1)
}

// Kinetics describes how hard the aircraft was actually turning. It is entirely
// separate from the noise profile: that measures how faithful the *record* of
// the motion is, this measures the motion itself.
//
// It exists because a clip can have an immaculate metadata track and still
// stabilize badly. Rolling-shutter skew is rate times readout time, and readout
// is a property of the camera that no amount of attitude correction touches; at
// several hundred degrees per second the frame is distorted internally before
// stabilization sees it, and motion blur has smeared it as well. A verdict that
// looks only at residual will call such a clip patchable, because by its own
// measure it is — and then patching will not help.
type Kinetics struct {
	// P50DPS, P90DPS and P99DPS are percentiles of the absolute rotation rate.
	P50DPS float64 `json:"p50_dps"`
	P90DPS float64 `json:"p90_dps"`
	P99DPS float64 `json:"p99_dps"`
	// FastFraction is the share of the clip above FastDPS.
	FastFraction float64 `json:"fast_fraction"`
	// FastDPS is the rate that fraction was measured against.
	FastDPS float64 `json:"fast_dps"`
	// AboveFrameNyquistDPS is the rms rotation faster than half the video frame
	// rate. A frame sequence cannot represent motion above that: it lands in the
	// footage as blur inside each frame rather than as movement between them.
	// Counter-rotating for it does not remove it — the value sampled at each
	// frame time has essentially random phase — so it is added back as jitter on
	// top of frames that were merely soft. It is the figure to set a stabilizer's
	// low-pass from, and no event-based correction reaches it, because it is
	// spread across the whole clip rather than gathered into events.
	AboveFrameNyquistDPS float64 `json:"above_frame_nyquist_dps,omitempty"`
	// FrameNyquistHz is half the video frame rate, zero when it is unknown.
	FrameNyquistHz float64 `json:"frame_nyquist_hz,omitempty"`
	// SkewP99Degrees is the within-frame rolling-shutter skew the p99 rate
	// implies at NominalReadoutSeconds. It is an order-of-magnitude figure, not
	// a measurement: real readout is camera-specific and Gyroflow ships a
	// separate tool to measure it.
	SkewP99Degrees float64 `json:"skew_p99_degrees"`
}

// NominalReadoutSeconds is the readout time the skew estimate assumes. Fifteen
// milliseconds is a common figure for a 4K sensor line-scan and is used only to
// turn a rotation rate into a number a reader can picture.
const NominalReadoutSeconds = 0.015

// FastDPS is the rate above which rolling-shutter skew reaches roughly five
// degrees within a single frame at the nominal readout. Past this the frame is
// being distorted internally faster than any attitude correction can matter.
const FastDPS = 5.0 / NominalReadoutSeconds

// kinetics summarises the absolute rotation rate.
func kinetics(velocities []vec3, interval, videoFPS float64) Kinetics {
	if len(velocities) == 0 {
		return Kinetics{FastDPS: FastDPS}
	}
	magnitudes := make([]float64, len(velocities))
	fast := 0
	for index, velocity := range velocities {
		magnitude := math.Sqrt(velocity[0]*velocity[0] + velocity[1]*velocity[1] + velocity[2]*velocity[2])
		magnitudes[index] = magnitude
		if magnitude > FastDPS {
			fast++
		}
	}
	sort.Float64s(magnitudes)
	at := func(fraction float64) float64 {
		index := int(float64(len(magnitudes)-1) * fraction)
		return magnitudes[index]
	}
	result := Kinetics{
		P50DPS:       at(0.50),
		P90DPS:       at(0.90),
		P99DPS:       at(0.99),
		FastFraction: float64(fast) / float64(len(magnitudes)),
		FastDPS:      FastDPS,
	}
	result.SkewP99Degrees = result.P99DPS * NominalReadoutSeconds
	if videoFPS > 0 && interval > 0 {
		result.FrameNyquistHz = videoFPS / 2
		result.AboveFrameNyquistDPS = aboveFrameNyquist(velocities, 1/interval, result.FrameNyquistHz)
	}
	return result
}

// aboveFrameNyquist is the rms of the per-axis rotation rate left after a
// zero-phase low-pass at the cutoff. Per-axis rather than by magnitude, so a
// sign change is not folded into itself and counted as motion.
func aboveFrameNyquist(velocities []vec3, sampleRate, cutoffHz float64) float64 {
	if len(velocities) < 8 || cutoffHz <= 0 || sampleRate <= 0 {
		return 0
	}
	radius := int(sampleRate / (2 * cutoffHz))
	if radius < 1 {
		radius = 1
	}
	total := 0.0
	for axis := 0; axis < 3; axis++ {
		series := make([]float64, len(velocities))
		for index, velocity := range velocities {
			series[index] = velocity[axis]
		}
		smooth := zeroPhaseAverage(series, radius)
		for index := range series {
			residual := series[index] - smooth[index]
			total += residual * residual
		}
	}
	return math.Sqrt(total / float64(len(velocities)*3))
}

func zeroPhaseAverage(series []float64, radius int) []float64 {
	out := runningAverage(series, radius)
	reverseFloats(out)
	out = runningAverage(out, radius)
	reverseFloats(out)
	return out
}

func runningAverage(series []float64, radius int) []float64 {
	out := make([]float64, len(series))
	window := 0.0
	for index := range series {
		window += series[index]
		if index > 2*radius {
			window -= series[index-2*radius-1]
		}
		out[index] = window / math.Min(float64(index+1), float64(2*radius+1))
	}
	return out
}

func reverseFloats(series []float64) {
	for left, right := 0, len(series)-1; left < right; left, right = left+1, right-1 {
		series[left], series[right] = series[right], series[left]
	}
}
