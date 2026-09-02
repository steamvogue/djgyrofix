package detect

import (
	"testing"

	"github.com/steamvogue/djgyrofix/internal/quat"
)

func regularTimes(count int, interval float64) []float64 {
	times := make([]float64, count)
	for index := range times {
		times[index] = float64(index) * interval
	}
	return times
}

func unitQuaternions(count int) []quat.Q {
	values := make([]quat.Q, count)
	for index := range values {
		values[index] = quat.Q{1, 0, 0, 0}
	}
	return values
}

func TestTransmissionGapDoesNotCondemnTheFollowingSample(t *testing.T) {
	const count, gapIndex = 40, 20
	const interval = 0.005
	times := regularTimes(count, interval)
	// Missing packets move this and every later timestamp forward. The first
	// sample after the gap is still valid orientation data and must not be
	// selected for interpolation merely because no packets arrived before it.
	for index := gapIndex; index < len(times); index++ {
		times[index] += 10 * interval
	}

	implausible := plausibilityGate(
		times,
		unitQuaternions(count),
		make([]vec3, count),
		interval,
		Defaults(),
	)
	for index, marked := range implausible {
		if marked {
			t.Fatalf("valid sample %d was marked implausible across a transmission gap", index)
		}
	}
	if events := dropoutEvents(times, implausible, Defaults()); len(events) != 0 {
		t.Fatalf("transmission gap became a bridge event: %+v", events)
	}
}

func TestNonMonotonicTimestampsRemainImplausible(t *testing.T) {
	const count, badIndex = 40, 20
	const interval = 0.005
	for _, test := range []struct {
		name string
		time float64
	}{
		{name: "duplicate", time: float64(badIndex-1) * interval},
		{name: "regression", time: float64(badIndex-2) * interval},
	} {
		t.Run(test.name, func(t *testing.T) {
			times := regularTimes(count, interval)
			times[badIndex] = test.time
			implausible := plausibilityGate(
				times,
				unitQuaternions(count),
				make([]vec3, count),
				interval,
				Defaults(),
			)
			if !implausible[badIndex] {
				t.Fatalf("sample %d with a %s timestamp passed the plausibility gate", badIndex, test.name)
			}
		})
	}
}
