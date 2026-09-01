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

const rate = 200.0

func series(defect synth.Defect) ([]float64, []quat.Q) {
	track := synth.Attitude(synth.AttitudeOptions{
		Defect: defect, Rate: rate, Seconds: 30, Seed: 20260901,
	})
	times := make([]float64, len(track))
	values := make([]quat.Q, len(track))
	for index, value := range track {
		times[index] = float64(index) / rate
		// Store through float32, as the file does, so what the corrector sees
		// is what the patcher would read back.
		for component := range value {
			values[index][component] = float64(float32(value[component]))
		}
	}
	return times, values
}

func detected(t *testing.T, times []float64, values []quat.Q) *detect.Result {
	t.Helper()
	points := make([]pipeline.Point, len(times))
	for index := range times {
		points[index] = pipeline.Point{Time: times[index], SampleIndex: index / 4, Values: values[index]}
	}
	result, err := detect.Run(points, detect.Defaults())
	if err != nil {
		t.Fatalf("detect.Run: %v", err)
	}
	return result
}

func TestEnvelopeLeavesUntouchedPointsBitIdentical(t *testing.T) {
	times, values := series(synth.DefectMixed)
	result := detected(t, times, values)
	output, err := correct.Envelope(times, values, result, correct.EnvelopeOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}

	// A point may only change where the correction weight is non-zero, or where
	// it was reconstructed as part of a dropout. Everywhere else it must come
	// back as the exact same float64 and produce no write at all — returning a
	// renormalized copy would change the last bit and dirty samples nothing was
	// wrong with.
	//
	// The weight, not the event bounds, is the right frontier here: the
	// envelope is blurred, so its taper deliberately reaches a little past
	// each event.
	correctable := func(index int) bool {
		return result.Weights[index] > 0 || result.Implausible[index]
	}
	changedOutside := 0
	for index := range values {
		if output[index] != values[index] && !correctable(index) {
			changedOutside++
			if changedOutside <= 3 {
				t.Errorf("point %d at %.3fs changed with weight %g and plausible data",
					index, times[index], result.Weights[index])
			}
		}
	}
	if changedOutside > 0 {
		t.Errorf("%d points with zero correction weight were modified", changedOutside)
	}

	changedInside := 0
	for index := range values {
		if output[index] != values[index] {
			changedInside++
		}
	}
	if changedInside == 0 {
		t.Fatal("nothing was corrected, so the test proves nothing")
	}
	t.Logf("%d of %d points corrected", changedInside, len(values))
}

func TestEnvelopeStrengthZeroIsIdentity(t *testing.T) {
	times, values := series(synth.DefectMixed)
	result := detected(t, times, values)
	output, err := correct.Envelope(times, values, result, correct.EnvelopeOptions{Strength: 0})
	if err != nil {
		t.Fatal(err)
	}
	for index := range values {
		if output[index] != values[index] {
			t.Fatalf("strength 0 modified point %d", index)
		}
	}
}

// TestBridgeReplacesCorruptSamplesWithAnInterpolation checks the reconstruction
// actually lands between the good neighbours, rather than merely being
// different from the corrupt value.
func TestBridgeReplacesCorruptSamplesWithAnInterpolation(t *testing.T) {
	times, values := series(synth.DefectDropout)
	result := detected(t, times, values)
	output, err := correct.Envelope(times, values, result, correct.EnvelopeOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}

	first := int(synth.DropoutAt * rate)
	last := first + synth.DropoutSamples - 1
	before, err := quat.Normalize(values[first-1])
	if err != nil {
		t.Fatal(err)
	}
	after, err := quat.Normalize(values[last+1])
	if err != nil {
		t.Fatal(err)
	}
	arc := math.Acos(math.Min(1, math.Abs(quat.Dot(before, after))))

	for index := first; index <= last; index++ {
		if output[index] == values[index] {
			t.Fatalf("corrupt sample %d was left alone", index)
		}
		reconstructed, err := quat.Normalize(output[index])
		if err != nil {
			t.Fatal(err)
		}
		// The reconstruction must sit on the short arc between the neighbours:
		// its angle to each endpoint cannot exceed the endpoints' separation.
		toBefore := math.Acos(math.Min(1, math.Abs(quat.Dot(reconstructed, before))))
		toAfter := math.Acos(math.Min(1, math.Abs(quat.Dot(reconstructed, after))))
		if toBefore > arc+1e-9 || toAfter > arc+1e-9 {
			t.Errorf("sample %d landed off the arc: %.6f and %.6f rad from the endpoints, which are %.6f apart",
				index, toBefore, toAfter, arc)
		}
		if math.Abs(reconstructed.Norm()-1) > 1e-9 {
			t.Errorf("sample %d is not a unit quaternion: norm %.12f", index, reconstructed.Norm())
		}
	}
}

func TestEnvelopeReducesHighFrequencyContent(t *testing.T) {
	times, values := series(synth.DefectJitter)
	result := detected(t, times, values)
	output, err := correct.Envelope(times, values, result, correct.EnvelopeOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}
	before, err := correct.AngularAccelerationScore(times, values, synth.JitterStart, synth.JitterEnd)
	if err != nil {
		t.Fatal(err)
	}
	after, err := correct.AngularAccelerationScore(times, output, synth.JitterStart, synth.JitterEnd)
	if err != nil {
		t.Fatal(err)
	}
	if after >= before {
		t.Errorf("angular acceleration went from %g to %g; the correction did not help", before, after)
	}
}

// TestNoDiscontinuityAtEventEdges is what replaces the reference's edge
// correction. Because w(t) tapers to zero at every event boundary, the patched
// track must not step there.
func TestNoDiscontinuityAtEventEdges(t *testing.T) {
	times, values := series(synth.DefectJitter)
	result := detected(t, times, values)
	output, err := correct.Envelope(times, values, result, correct.EnvelopeOptions{Strength: 1})
	if err != nil {
		t.Fatal(err)
	}

	// Express the step as the angular rate it implies, in degrees per second.
	// A ratio against the source step is the wrong measure: where the source is
	// nearly still, any absolute change looks like a large multiple of nothing.
	rateAt := func(track []quat.Q, index int) float64 {
		a, errA := quat.Normalize(track[index-1])
		b, errB := quat.Normalize(track[index])
		if errA != nil || errB != nil {
			return 0
		}
		radians := math.Acos(math.Min(1, math.Abs(quat.Dot(a, b))))
		return 2 * radians * 180 / math.Pi * rate
	}
	// The detection floor is 60 °/s and the synthetic sensor noise is well
	// under 1 °/s. An edge artifact worth calling a discontinuity would be a
	// large fraction of the floor; anything under a few °/s is invisible both
	// to Gyroflow and to the next scan.
	const toleranceDPS = 5.0

	checked := 0
	for _, event := range result.Events {
		if event.Action != detect.ActionSmooth {
			continue
		}
		for _, edge := range []int{event.FirstPoint, event.LastPoint} {
			if edge <= 0 || edge >= len(output)-1 {
				continue
			}
			checked++
			source, patched := rateAt(values, edge), rateAt(output, edge)
			if excess := patched - source; excess > toleranceDPS {
				t.Errorf("event edge %d at %.3fs steps by %.2f °/s more than the source (%.2f vs %.2f)",
					edge, times[edge], excess, patched, source)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no smoothing event edges were checked")
	}
}

func TestEnvelopeRejectsMismatchedInput(t *testing.T) {
	times, values := series(synth.DefectNone)
	result := detected(t, times, values)
	if _, err := correct.Envelope(times[:10], values, result, correct.EnvelopeOptions{Strength: 1}); err == nil {
		t.Error("mismatched time and quaternion lengths were accepted")
	}
}

func TestLegacyRejectsBadParameters(t *testing.T) {
	times := []float64{0, 1}
	values := []quat.Q{{1, 0, 0, 0}, {1, 0, 0, 0}}
	cases := map[string]correct.LegacyParams{
		"end before start":  {StartSeconds: 1, EndSeconds: 0, SmoothingMS: 180, Strength: 1},
		"infinite smooth":   {StartSeconds: 0, EndSeconds: 1, SmoothingMS: math.Inf(1), Strength: 1},
		"zero smooth":       {StartSeconds: 0, EndSeconds: 1, SmoothingMS: 0, Strength: 1},
		"nan strength":      {StartSeconds: 0, EndSeconds: 1, SmoothingMS: 180, Strength: math.NaN()},
		"negative smooth":   {StartSeconds: 0, EndSeconds: 1, SmoothingMS: -5, Strength: 1},
		"equal start & end": {StartSeconds: 1, EndSeconds: 1, SmoothingMS: 180, Strength: 1},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := correct.Legacy(times, values, params); err == nil {
				t.Error("accepted invalid parameters")
			}
		})
	}
	if _, err := correct.Legacy([]float64{0}, values, correct.LegacyParams{
		StartSeconds: 0, EndSeconds: 1, SmoothingMS: 180, Strength: 1,
	}); err == nil {
		t.Error("accepted mismatched lengths")
	}
	if _, err := correct.Legacy([]float64{0, math.NaN()}, values, correct.LegacyParams{
		StartSeconds: 0, EndSeconds: 1, SmoothingMS: 180, Strength: 1,
	}); err == nil {
		t.Error("accepted a non-finite sample time")
	}
	if got, err := correct.Legacy(nil, nil, correct.LegacyParams{
		StartSeconds: 0, EndSeconds: 1, SmoothingMS: 180, Strength: 1,
	}); err != nil || len(got) != 0 {
		t.Errorf("an empty series should give an empty result, got %v, %v", got, err)
	}
}
