// Package detect finds high-frequency attitude artifacts in a quaternion track
// and decides which of them are safe to correct.
//
// The discriminator is the residual: angular velocity minus its own low-passed
// copy. A whip-pan or a flip is smooth fast rotation and cancels out entirely,
// so only high-frequency content survives. That is what makes automatic
// detection viable at all — a plain velocity threshold would flag every
// intentional fast move in the clip.
package detect

import (
	"fmt"
	"time"
)

// Class names the kind of artifact an event is.
type Class string

// Event classes. Only Dropout is ever reconstructed; Motion is never touched.
const (
	// ClassDropout is a short run of physically impossible samples with valid
	// data either side — telemetry corruption, safe to bridge.
	ClassDropout Class = "dropout"
	// ClassImpact is a brief single-peaked spike that is physically plausible.
	ClassImpact Class = "impact"
	// ClassJitter is sustained multi-peaked high-frequency content.
	ClassJitter Class = "jitter"
	// ClassMotion is high residual that tracks intentional input. Left alone.
	ClassMotion Class = "motion"
)

// Action names what correction an event will receive.
type Action string

// Corrective actions.
const (
	ActionBridge Action = "bridge"
	ActionSmooth Action = "smooth"
	ActionNone   Action = "none"
)

// Params controls detection. Zero values are not meaningful; start from
// ProfileParams and override.
type Params struct {
	// Profile is the preset these values came from.
	Profile string `json:"profile"`
	// Sensitivity scales every threshold. Above 1.0 detects more.
	Sensitivity float64 `json:"sensitivity"`
	// MADK is the Hampel sigma multiplier.
	MADK float64 `json:"mad_k"`
	// RelK is the multiple of the local baseline a bin must also exceed.
	RelK float64 `json:"rel_k"`
	// BaselineWindow is the rolling baseline half-width.
	BaselineWindow time.Duration `json:"baseline_window"`
	// FloorDPS is the absolute residual floor in degrees per second, so still
	// footage with sensor noise cannot trip detection.
	FloorDPS float64 `json:"floor_dps"`
	// MinSeverity drops events scoring below it, on a 0-10 scale.
	MinSeverity float64 `json:"min_severity"`
	// IMUFullScale is the rate above which a sample is physically impossible.
	IMUFullScale float64 `json:"imu_full_scale"`
	// NormTolerance is how far a raw quaternion norm may sit from unity before
	// the sample is considered corrupt.
	NormTolerance float64 `json:"norm_tolerance"`
	// BridgeMaxSamples is the longest run of bad samples that may be
	// reconstructed by interpolation.
	BridgeMaxSamples int `json:"bridge_max_samples"`
	// NoBridge disables reconstruction entirely.
	NoBridge bool `json:"no_bridge"`
	// MotionRatio is the residual-to-low-passed-speed ratio below which a
	// supra-threshold event is intentional motion rather than an artifact.
	MotionRatio float64 `json:"motion_ratio"`
	// MotionDPS is the low-passed speed above which the motion test applies.
	MotionDPS float64 `json:"motion_dps"`

	// BinSeconds is the residual energy bin width.
	BinSeconds float64 `json:"bin_seconds"`
	// LowpassSeconds is the velocity low-pass radius.
	LowpassSeconds float64 `json:"lowpass_seconds"`
	// GapSeconds is how long a sub-threshold gap may be without splitting an event.
	GapSeconds float64 `json:"gap_seconds"`
	// PadSeconds widens each event on both sides.
	PadSeconds float64 `json:"pad_seconds"`
	// ShortClipSeconds is the duration below which the rolling baseline is
	// abandoned for a single global one.
	ShortClipSeconds float64 `json:"short_clip_seconds"`
	// EnvelopeK sets how far above threshold the correction weight reaches 1.
	EnvelopeK float64 `json:"envelope_k"`
	// EnvelopeBlurSeconds softens the shoulders of the weight envelope.
	EnvelopeBlurSeconds float64 `json:"envelope_blur_seconds"`
}

// Defaults returns the balanced profile.
func Defaults() Params {
	return Params{
		Profile:             "balanced",
		Sensitivity:         1.0,
		MADK:                5.0,
		RelK:                3.0,
		BaselineWindow:      5 * time.Second,
		FloorDPS:            60,
		MinSeverity:         5.0,
		IMUFullScale:        2000,
		NormTolerance:       0.02,
		BridgeMaxSamples:    3,
		MotionRatio:         0.25,
		MotionDPS:           120,
		BinSeconds:          0.010,
		LowpassSeconds:      0.012,
		GapSeconds:          0.20,
		PadSeconds:          0.02,
		ShortClipSeconds:    15.0,
		EnvelopeK:           1.0,
		EnvelopeBlurSeconds: 0.100,
	}
}

// ProfileParams returns the named preset.
//
// The presets move sensitivity, the Hampel multiplier and the severity cut
// together. False positives cost more than misses here — smoothing intentional
// motion degrades footage that was fine — so conservative is the one to reach
// for when in doubt.
func ProfileParams(name string) (Params, error) {
	params := Defaults()
	switch name {
	case "conservative":
		params.Profile = "conservative"
		params.MADK = 7.0
		params.RelK = 4.0
		params.FloorDPS = 90
		params.MinSeverity = 6.5
		params.MotionRatio = 0.35
		params.BridgeMaxSamples = 2
	case "balanced", "":
		params.Profile = "balanced"
	case "aggressive":
		params.Profile = "aggressive"
		params.MADK = 3.5
		params.RelK = 2.2
		params.FloorDPS = 40
		params.MinSeverity = 3.5
		params.MotionRatio = 0.18
		params.BridgeMaxSamples = 4
	default:
		return Params{}, fmt.Errorf("unknown profile %q (want conservative, balanced or aggressive)", name)
	}
	return params, nil
}

// Validate checks the parameters are usable.
func (p Params) Validate() error {
	if p.Sensitivity < 0.1 || p.Sensitivity > 3.0 {
		return fmt.Errorf("sensitivity %g is outside the supported range 0.1-3.0", p.Sensitivity)
	}
	if p.MADK <= 0 {
		return fmt.Errorf("mad-k must be positive, got %g", p.MADK)
	}
	if p.BaselineWindow <= 0 {
		return fmt.Errorf("baseline-window must be positive, got %s", p.BaselineWindow)
	}
	if p.FloorDPS < 0 {
		return fmt.Errorf("floor-dps must not be negative, got %g", p.FloorDPS)
	}
	if p.MinSeverity < 0 || p.MinSeverity > 10 {
		return fmt.Errorf("min-severity %g is outside the range 0-10", p.MinSeverity)
	}
	if p.IMUFullScale <= 0 {
		return fmt.Errorf("imu-full-scale must be positive, got %g", p.IMUFullScale)
	}
	if p.BridgeMaxSamples < 0 {
		return fmt.Errorf("bridge-max-samples must not be negative, got %d", p.BridgeMaxSamples)
	}
	if p.BinSeconds <= 0 || p.LowpassSeconds <= 0 {
		return fmt.Errorf("bin and low-pass widths must be positive")
	}
	return nil
}
