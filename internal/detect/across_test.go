package detect_test

import (
	"math"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/quat"
)

// spin integrates a per-sample angular velocity in degrees per second into an
// orientation track.
func spin(rate float64, count int, omega func(i int) [3]float64) []quat.Q {
	track := make([]quat.Q, count)
	current := quat.Q{1, 0, 0, 0}
	track[0] = current
	dt := 1.0 / rate
	for i := 1; i < count; i++ {
		w := omega(i)
		half := dt * math.Pi / 180.0 / 2.0
		delta := quat.Q{1, w[0] * half, w[1] * half, w[2] * half}
		product, err := quat.Multiply(delta, current)
		if err != nil {
			panic(err)
		}
		current = product
		track[i] = current
	}
	return track
}

// TestAcrossAxisSeparatesRateChangeFromWobble is the discrimination the metric
// exists for, and the reason the plain residual magnitude was not enough.
//
// Turning faster or slower than the local trend about the axis you are already
// turning about is flying. The axis itself moving is the artifact. Both produce
// the same residual magnitude; only the split tells them apart. Measured on one
// aggressive manoeuvre in the real clip, a 366 °/s exit rotation reads 29%
// across-axis where the wobble 400 ms later reads 92%.
func TestAcrossAxisSeparatesRateChangeFromWobble(t *testing.T) {
	const rate, count = 200.0, 4000

	// Fast yaw whose rate ripples about the same axis.
	alongTrack := spin(rate, count, func(i int) [3]float64 {
		return [3]float64{0, 0, 300 + 60*math.Sin(float64(i)*0.7)}
	})
	// The same fast yaw, rippling across the axis instead.
	acrossTrack := spin(rate, count, func(i int) [3]float64 {
		return [3]float64{60 * math.Sin(float64(i)*0.7), 0, 300}
	})

	share := func(track []quat.Q) float64 {
		result, err := detect.Run(points(track, rate), detect.Defaults())
		if err != nil {
			t.Fatal(err)
		}
		if len(result.ResidualAcross) != len(result.Residual) {
			t.Fatal("the across-axis series was not produced")
		}
		var total, across float64
		// Skip the filter's edges, where the low-pass has no full window.
		for i := 400; i < len(result.Residual)-400; i++ {
			total += result.Residual[i]
			across += result.ResidualAcross[i]
		}
		if total == 0 {
			t.Fatal("no residual at all")
		}
		return across / total
	}

	alongShare, acrossShare := share(alongTrack), share(acrossTrack)
	t.Logf("rate ripple along the axis: %.0f%% across; wobble across it: %.0f%% across",
		alongShare*100, acrossShare*100)

	if alongShare > 0.35 {
		t.Errorf("a rate ripple about the rotation axis reads %.0f%% across-axis; "+
			"it is variation in how fast the aircraft turned, not the axis moving",
			alongShare*100)
	}
	if acrossShare < 0.65 {
		t.Errorf("a wobble across the rotation axis reads only %.0f%% across-axis",
			acrossShare*100)
	}
	if acrossShare <= alongShare*1.5 {
		t.Errorf("the two are not separated: %.2f against %.2f", acrossShare, alongShare)
	}
}

func TestAlongAxisManeuverProducesNoActionableEvents(t *testing.T) {
	const rate, count = 200.0, 4000
	alongTrack := spin(rate, count, func(i int) [3]float64 {
		return [3]float64{0, 0, 300 + 60*math.Sin(float64(i)*0.7)}
	})
	result, err := detect.Run(points(alongTrack, rate), detect.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range result.Events {
		if event.Action != detect.ActionNone {
			t.Errorf("along-axis rate ripple produced an actionable %s event (action=%s) at %.3f-%.3f (severity %.1f, note=%q)",
				event.Class, event.Action, event.StartSeconds, event.EndSeconds, event.Severity, event.Note)
		}
	}
}
