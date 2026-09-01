package correct_test

import (
	"math"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/correct"
	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/pipeline"
	"github.com/steamvogue/djgyrofix/internal/quat"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

// repairSetup runs detection over a generated track and returns everything the
// correction pass needs.
func repairSetup(t *testing.T, defect synth.Defect) ([]float64, []quat.Q, *detect.Result) {
	t.Helper()
	const rate = 200.0
	track := synth.Attitude(synth.AttitudeOptions{
		Defect: defect, Rate: rate, Seconds: 30, Seed: 20260901,
	})
	points := make([]pipeline.Point, len(track))
	for index, value := range track {
		points[index] = pipeline.Point{
			Time: float64(index) / rate, SampleIndex: index / 4, Values: value,
		}
	}
	result, err := detect.Run(points, detect.Defaults())
	if err != nil {
		t.Fatalf("detect.Run: %v", err)
	}
	return pipeline.Times(points), pipeline.Values(points), result
}

// spikedTrack is a clean rotation with brief excursions injected: the shape
// this pass is for. Each excursion departs the trajectory for a few samples and
// returns to it, which is what the real artifact does — median run 4.04 ms on
// the measured clip, against detected events spanning hundreds of milliseconds.
func spikedTrack(t *testing.T) ([]float64, []quat.Q, *detect.Result) {
	t.Helper()
	const rate, seconds = 200.0, 30.0
	track := synth.Attitude(synth.AttitudeOptions{
		Defect: synth.DefectNone, Rate: rate, Seconds: seconds, Seed: 20260901,
	})
	// A rotation about X that ramps up and back down inside the run, so the
	// orientation leaves the arc and returns to it. A constant offset would be
	// a step rather than an excursion — it departs and stays departed, which is
	// what a real movement looks like and what departsAndReturns must refuse.
	shape := []float64{0.35, 0.85, 1.0, 0.85, 0.35}
	const halfAngle = 0.05
	for _, at := range []float64{5, 10, 15, 20, 25} {
		start := int(at * rate)
		for offset, scale := range shape {
			if start+offset >= len(track) {
				break
			}
			angle := halfAngle * scale
			kick := quat.Q{math.Cos(angle), math.Sin(angle), 0, 0}
			product, err := quat.Multiply(kick, track[start+offset])
			if err != nil {
				t.Fatal(err)
			}
			track[start+offset] = product
		}
	}
	points := make([]pipeline.Point, len(track))
	for index, value := range track {
		points[index] = pipeline.Point{
			Time: float64(index) / rate, SampleIndex: index / 4, Values: value,
		}
	}
	result, err := detect.Run(points, detect.Defaults())
	if err != nil {
		t.Fatalf("detect.Run: %v", err)
	}
	return pipeline.Times(points), pipeline.Values(points), result
}

// TestRunRepairTouchesLessThanTheBlur is the property the whole pass exists
// for. Blurring an event smooths every sample in it; replacing the runs inside
// it touches only the samples that were out of trend, leaving the genuine
// motion between them exactly as recorded.
func TestRunRepairTouchesLessThanTheBlur(t *testing.T) {
	times, values, result := spikedTrack(t)
	if len(result.Events) == 0 {
		t.Fatal("the spiked fixture produced no events")
	}

	blurred, err := correct.Envelope(times, values, result, correct.EnvelopeOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := correct.Envelope(times, values, result, correct.EnvelopeOptions{
		Strength: 1, RepairRuns: true, Repair: correct.DefaultRepairOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}

	changed := func(out []quat.Q) int {
		count := 0
		for index := range values {
			if out[index] != values[index] {
				count++
			}
		}
		return count
	}
	blurCount, repairCount := changed(blurred), changed(repaired)
	if repairCount == 0 {
		t.Fatal("run-repair changed nothing on a fixture built from short excursions")
	}
	if repairCount >= blurCount {
		t.Errorf("run-repair touched %d samples, the blur %d — it is meant to be the surgical one",
			repairCount, blurCount)
	}
	t.Logf("blur touched %d samples, run-repair %d", blurCount, repairCount)
}

// TestSustainedJitterFallsBackToTheBlur is the other half of the contract. A
// burst longer than the cap cannot be interpolated without inventing the
// orientation across it, so the blur has to stay in charge of those.
func TestSustainedJitterFallsBackToTheBlur(t *testing.T) {
	times, values, result := repairSetup(t, synth.DefectJitter)
	if len(result.Events) == 0 {
		t.Skip("no events in the fixture")
	}
	working := append([]quat.Q(nil), values...)
	output := append([]quat.Q(nil), values...)
	stats, handled, err := correct.RepairRuns(times, working, output, result, correct.DefaultRepairOptions())
	if err != nil {
		t.Fatal(err)
	}
	if stats.RunsTooLong == 0 {
		t.Errorf("a 1.24 s burst produced no run over the %0.0f ms cap",
			correct.DefaultRepairOptions().MaxRunSeconds*1000)
	}
	// Short runs inside the burst may still be replaced, but the event as a
	// whole must stay with the blur: something in it was too long to
	// interpolate, and leaving that uncorrected would be worse than smoothing.
	for index, done := range handled {
		if done && result.Events[index].Action != "" {
			t.Errorf("event %d was marked fully handled despite %d runs over the cap",
				index, stats.RunsTooLong)
		}
	}
}

// TestRunRepairKeepsTheEndpoints checks that a replacement is an interpolation
// between real neighbours, not a new trajectory: the samples bracketing a
// replaced run are never themselves modified.
func TestRunRepairKeepsTheEndpoints(t *testing.T) {
	times, values, result := repairSetup(t, synth.DefectJitter)
	repaired, err := correct.Envelope(times, values, result, correct.EnvelopeOptions{
		Strength: 1, RepairRuns: true, Repair: correct.DefaultRepairOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < len(values)-1; index++ {
		if repaired[index] == values[index] {
			continue
		}
		// A changed sample must sit inside a run, so at least one neighbour is
		// changed too, or it is a lone sample bracketed by originals.
		before := repaired[index-1] == values[index-1]
		after := repaired[index+1] == values[index+1]
		if before && after {
			// A single replaced sample between two untouched ones is exactly
			// the intended shape: its value must lie between them.
			mid, err := quat.Slerp(values[index-1], values[index+1], 0.5)
			if err != nil {
				t.Fatal(err)
			}
			if angle(t, repaired[index], mid) > 5.0 {
				t.Errorf("sample %d was replaced with something %0.2f° off the midpoint of its neighbours",
					index, angle(t, repaired[index], mid))
			}
		}
	}
}

// TestRunRepairRefusesToInventLongSpans keeps the cap meaningful. Interpolating
// a long run fabricates orientation the aircraft never reported, which is the
// one failure this correction can make that a blur cannot.
func TestRunRepairRefusesToInventLongSpans(t *testing.T) {
	times, values, result := repairSetup(t, synth.DefectJitter)
	options := correct.DefaultRepairOptions()
	options.MaxRunSeconds = 0 // nothing may be replaced

	working := append([]quat.Q(nil), values...)
	output := append([]quat.Q(nil), values...)
	stats, handled, err := correct.RepairRuns(times, working, output, result, options)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RunsReplaced != 0 {
		t.Errorf("replaced %d runs with a zero-length cap", stats.RunsReplaced)
	}
	for index, done := range handled {
		if done {
			t.Errorf("event %d reported as handled when nothing was replaced", index)
		}
	}
}

// TestRunRepairLeavesACleanClipAlone is the same invariant the blur holds: a
// clip with nothing wrong must produce no writes at all.
func TestRunRepairLeavesACleanClipAlone(t *testing.T) {
	times, values, result := repairSetup(t, synth.DefectNone)
	repaired, err := correct.Envelope(times, values, result, correct.EnvelopeOptions{
		Strength: 1, RepairRuns: true, Repair: correct.DefaultRepairOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := range values {
		if repaired[index] != values[index] {
			t.Fatalf("a clean clip was modified at sample %d", index)
		}
	}
}

// TestRunRepairStillBridgesDropouts checks the two reconstruction paths do not
// collide: a dropout is gated on physical implausibility and must stay with
// the bridge, which has the stricter evidence behind it.
func TestRunRepairStillBridgesDropouts(t *testing.T) {
	times, values, result := repairSetup(t, synth.DefectDropout)
	repaired, err := correct.Envelope(times, values, result, correct.EnvelopeOptions{
		Strength: 1, RepairRuns: true, Repair: correct.DefaultRepairOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := 0
	for index := range values {
		if repaired[index] != values[index] {
			changed++
		}
	}
	if changed == 0 {
		t.Error("the dropout was left uncorrected with run-repair enabled")
	}
}

func angle(t *testing.T, a, b quat.Q) float64 {
	t.Helper()
	inverse, err := quat.Inverse(a)
	if err != nil {
		t.Fatal(err)
	}
	delta, err := quat.Multiply(b, inverse)
	if err != nil {
		t.Fatal(err)
	}
	if delta[0] < 0 {
		delta = delta.Neg()
	}
	vector := math.Sqrt(delta[1]*delta[1] + delta[2]*delta[2] + delta[3]*delta[3])
	return quat.Degrees(2.0 * math.Atan2(vector, math.Max(0, delta[0])))
}
