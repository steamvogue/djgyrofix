// Package detect finds short-timescale attitude deviations in a quaternion
// track and decides which of them are safe to correct.
//
// The discriminator is the residual: angular velocity minus its own low-passed
// copy. This exposes the brief excursions that frame vibration leaves in the
// track, as well as broadband noise. Smooth fast rotation mostly cancels, which
// keeps a plain velocity threshold from flagging every intentional fast move.
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
	// ClassJitter is sustained multi-peaked transient deviation.
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
	// Style is the flight style the baseline window came from, empty when
	// --baseline-window was set explicitly.
	Style string `json:"style,omitempty"`
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
	// VideoFPS is the frame rate of the video this attitude track belongs to.
	// It is not used for detection at all — it is here so the report can say how
	// much of the recorded motion is faster than the frames can represent, which
	// is a property of the pair rather than of the track alone. Zero means
	// unknown and the figure is omitted.
	VideoFPS float64 `json:"video_fps,omitempty"`
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
		Style:               string(StyleNormal),
		Sensitivity:         1.0,
		MADK:                5.0,
		RelK:                3.0,
		BaselineWindow:      styleWindows[StyleNormal],
		FloorDPS:            60,
		MinSeverity:         5.0,
		IMUFullScale:        2000,
		NormTolerance:       0.02,
		BridgeMaxSamples:    3,
		MotionRatio:         0.25,
		MotionDPS:           120,
		BinSeconds:          0.010,
		LowpassSeconds:      0.060,
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

// Style names how the aircraft was being flown, which sets the rolling baseline
// window and nothing else.
//
// The window has to be long enough that a burst of ringing cannot dominate its
// own neighbourhood. The threshold is baseline + k·sigma over a sliding window,
// so a two-second burst inside a ten-second window contributes a fifth of the
// samples the sigma is computed from — it raises the very bar meant to catch
// it, and hides. Measured on the real clip in docs/FINDINGS.md, a burst at
// 79-81 s sat at 165-235 °/s against a threshold of 605 °/s, and only 12% of it
// was flagged. At a 20 s window that rose to 46%, and correction took the
// window's residual from 238.6 °/s RMS to 14.3 °/s.
//
// Smoother flying needs less of this, not more: long cruising passes give a
// stable local baseline that a short window tracks well. Violent flying needs
// the long window, because that is where the sustained ringing lives.
type Style string

// Flight styles, ordered by how violent the flying is.
const (
	// StyleCinematic is smooth cruising and slow pans.
	StyleCinematic Style = "cinematic"
	// StyleNormal is ordinary mixed flying, and the default.
	StyleNormal Style = "normal"
	// StyleFreestyle is flips, rolls and hard direction changes.
	StyleFreestyle Style = "freestyle"
)

// styleWindows maps each style to its rolling baseline half-width.
var styleWindows = map[Style]time.Duration{
	StyleCinematic: 5 * time.Second,
	StyleNormal:    12 * time.Second,
	StyleFreestyle: 20 * time.Second,
}

// Styles lists every style, smoothest first, for help text and validation.
var Styles = []Style{StyleCinematic, StyleNormal, StyleFreestyle}

// StyleWindow returns the baseline window for a named style.
func StyleWindow(name string) (time.Duration, error) {
	if window, ok := styleWindows[Style(name)]; ok {
		return window, nil
	}
	return 0, fmt.Errorf("unknown style %q (want cinematic, normal or freestyle)", name)
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
