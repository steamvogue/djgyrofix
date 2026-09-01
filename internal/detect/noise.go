package detect

import (
	"math"
	"sort"
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
// It is bracketed by two measurements: the p90 of the real repairable clip
// (66.7 °/s), which must fall below it, and the p10 of a synthetic clip
// resonating end to end (113.3 °/s), which must fall above it. Ninety leaves
// roughly a third of margin either way.
//
// That is one real data point on each side, and the upper one is generated
// rather than observed. Expect this number to move as more real footage is
// seen; it is the single value most likely to be wrong in this package, which
// is why it is here on its own rather than inlined. See Known limits in the
// README.
const noisyFloorDPS = 90.0

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
