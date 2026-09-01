package main

import (
	"testing"

	"github.com/steamvogue/djgyrofix/internal/correct"
	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/pipeline"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

func TestAutoCorrectConvergesInsideOriginalScope(t *testing.T) {
	const rate = 200.0
	track := synth.Attitude(synth.AttitudeOptions{
		Defect: synth.DefectVectorJitter, Rate: rate, Seconds: 30, Seed: 20260901,
	})
	points := make([]pipeline.Point, len(track))
	for index, value := range track {
		points[index] = pipeline.Point{Time: float64(index) / rate, Values: value}
	}
	params := detect.Defaults()
	initial, err := detect.Run(points, params)
	if err != nil {
		t.Fatal(err)
	}
	_, passes, final, _, scopes, err := autoCorrect(points, initial, params,
		correct.EnvelopeOptions{Strength: 1}, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	if passes < 1 || passes > maxAutoCorrectionPasses {
		t.Fatalf("correction used %d passes", passes)
	}
	inside, outside := residualEventCounts(final, scopes,
		params.GapSeconds)
	if inside != 0 {
		t.Errorf("%d event(s) remain inside the original correction scope after %d passes", inside, passes)
	}
	if outside != 0 {
		t.Errorf("correction introduced %d actionable event(s) outside its original scope", outside)
	}
}
