package synth

import (
	"math"
	"math/rand"

	"github.com/steamvogue/djgyrofix/internal/quat"
)

// Defect names a synthetic artifact to inject into a generated attitude track.
type Defect string

// Injectable defects. Whippan is the important one: it is fast rotation that
// detection must never flag, and false positives on intentional motion are the
// main risk of automating any of this.
const (
	DefectNone         Defect = "clean"
	DefectJitter       Defect = "jitter"
	DefectVectorChange Defect = "vector-change"
	DefectVectorJitter Defect = "vector-jitter"
	DefectImpact       Defect = "impact"
	DefectDropout      Defect = "dropout"
	DefectWhipPan      Defect = "whippan"
	DefectMixed        Defect = "mixed"
)

// AttitudeOptions configures a generated attitude series.
type AttitudeOptions struct {
	Defect Defect
	// Rate is the quaternion rate in Hz.
	Rate float64
	// Seconds is the clip length.
	Seconds float64
	// Seed makes the sensor noise reproducible.
	Seed int64
	// NoiseDPS is the standard deviation of the per-sample sensor noise.
	NoiseDPS float64
	// RoughUntil applies extra broadband shake from t=0 to this time. When it
	// covers more than about half the clip, a globally-computed baseline is
	// pulled up by it and can no longer see anything in the calm remainder.
	RoughUntil float64
	// RoughDPS is the standard deviation of that shake.
	RoughDPS float64
	// JitterAt moves the injected jitter burst, so it can be placed in the
	// calm part of a mostly-rough clip. Zero means the default window.
	JitterAt float64
	// DropoutTime moves the corrupt run. Zero means DropoutAt.
	DropoutTime float64
}

// Windows are the time spans each defect occupies, in seconds.
const (
	JitterStart  = 8.0
	JitterEnd    = 9.2
	ImpactAt     = 15.0
	ImpactLength = 0.06
	WhipPanStart = 20.0
	WhipPanEnd   = 20.9
	DropoutAt    = 25.0
	// DropoutSamples is how many consecutive quaternions are corrupted.
	DropoutSamples = 2
)

// Attitude integrates a plausible drone attitude track with the requested
// defect injected at a known time, so a detector can be scored against ground
// truth rather than against eyeballs.
func Attitude(options AttitudeOptions) []quat.Q {
	if options.Rate <= 0 {
		options.Rate = 200
	}
	if options.Seconds <= 0 {
		options.Seconds = 30
	}
	if options.NoiseDPS == 0 {
		options.NoiseDPS = 0.35
	}
	if options.RoughDPS == 0 {
		options.RoughDPS = 90
	}
	jitterStart, jitterEnd := JitterStart, JitterEnd
	if options.JitterAt > 0 {
		jitterStart = options.JitterAt
		jitterEnd = options.JitterAt + (JitterEnd - JitterStart)
	}
	count := int(options.Seconds * options.Rate)
	rng := rand.New(rand.NewSource(options.Seed))
	track := make([]quat.Q, count)
	current := quat.Q{1, 0, 0, 0}

	wants := func(defect Defect) bool {
		if options.Defect == defect {
			return true
		}
		if options.Defect != DefectMixed {
			return false
		}
		return defect == DefectJitter || defect == DefectImpact ||
			defect == DefectDropout || defect == DefectWhipPan
	}

	for index := 0; index < count; index++ {
		seconds := float64(index) / options.Rate
		// A gentle drift everywhere, in degrees per second, so the track is
		// never artificially still.
		omega := [3]float64{
			6 * math.Sin(0.7*seconds),
			4 * math.Cos(0.4*seconds),
			3 * math.Sin(0.23*seconds),
		}
		if wants(DefectJitter) && seconds > jitterStart && seconds < jitterEnd {
			omega[0] += 260 * math.Sin(2*math.Pi*41*seconds)
			omega[2] += 190 * math.Sin(2*math.Pi*37*seconds+1.1)
		}
		// A deliberate change of rotation axis is legitimate motion. The
		// vector-jitter variant adds a damped, low-frequency attitude response
		// after the same change: it is fast enough to be objectionable, but is
		// not broadband sensor noise.
		if wants(DefectVectorChange) || wants(DefectVectorJitter) {
			const changeAt = JitterStart
			const transition = 0.12
			const moveStart = changeAt - 0.7
			const moveEnd = changeAt + 0.7
			if seconds >= moveStart && seconds < moveEnd {
				gain := 1.0
				if seconds < moveStart+transition {
					amount := (seconds - moveStart) / transition
					gain = 0.5 - 0.5*math.Cos(math.Pi*amount)
				} else if seconds > moveEnd-transition {
					amount := (moveEnd - seconds) / transition
					gain = 0.5 - 0.5*math.Cos(math.Pi*amount)
				}
				axisAmount := math.Min(1, math.Max(0, (seconds-changeAt)/transition))
				axisAmount = 0.5 - 0.5*math.Cos(math.Pi*axisAmount)
				omega[0] += 240 * gain * (1 - axisAmount)
				omega[2] += 240 * gain * axisAmount
			}
			if wants(DefectVectorJitter) {
				delta := seconds - changeAt
				if delta >= 0 && delta < 0.8 {
					ring := math.Exp(-delta/0.34) * math.Sin(2*math.Pi*6*delta)
					omega[0] += 210 * ring
					omega[2] -= 165 * ring
				}
			}
		}
		if wants(DefectImpact) {
			// A real impact is a sharp transient that rings down. A constant
			// rate held for the same duration is smooth motion, and detection
			// is right to leave that alone.
			if delta := seconds - ImpactAt; delta >= 0 && delta < ImpactLength {
				omega[1] += 850 * math.Exp(-delta/0.012) * math.Cos(2*math.Pi*70*delta)
			}
		}
		if wants(DefectWhipPan) && seconds > WhipPanStart && seconds < WhipPanEnd {
			// Fast, but perfectly smooth: a half-sine rate profile with no
			// short-timescale oscillation at all.
			omega[2] += 420 * math.Sin(math.Pi*(seconds-WhipPanStart)/(WhipPanEnd-WhipPanStart))
		}
		if options.RoughUntil > 0 && seconds < options.RoughUntil {
			omega[0] += rng.NormFloat64() * options.RoughDPS
			omega[1] += rng.NormFloat64() * options.RoughDPS
		}
		for axis := range omega {
			omega[axis] += rng.NormFloat64() * options.NoiseDPS
		}
		current = integrate(current, omega, 1.0/options.Rate)
		track[index] = current
	}

	if wants(DefectDropout) {
		// A short run of physically impossible orientation with valid data on
		// both sides — telemetry corruption, not motion.
		dropoutAt := options.DropoutTime
		if dropoutAt <= 0 {
			dropoutAt = DropoutAt
		}
		at := int(dropoutAt * options.Rate)
		for offset := 0; offset < DropoutSamples && at+offset < count; offset++ {
			track[at+offset] = quat.Q{0.51, 0.62, -0.44, 0.39}
		}
	}
	return track
}

func integrate(current quat.Q, omegaDegrees [3]float64, dt float64) quat.Q {
	half := dt * math.Pi / 180.0 / 2.0
	delta := quat.Q{1, omegaDegrees[0] * half, omegaDegrees[1] * half, omegaDegrees[2] * half}
	product, err := quat.Multiply(delta, current)
	if err != nil {
		return current
	}
	return product
}
