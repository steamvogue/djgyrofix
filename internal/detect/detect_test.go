package detect_test

import (
	"math"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/detect"
	"github.com/steamvogue/djgyrofix/internal/pipeline"
	"github.com/steamvogue/djgyrofix/internal/quat"
	"github.com/steamvogue/djgyrofix/internal/synth"
)

const testRate = 200.0

// points turns a generated attitude track into the point series detection eats.
func points(track []quat.Q, rate float64) []pipeline.Point {
	out := make([]pipeline.Point, len(track))
	for index, value := range track {
		out[index] = pipeline.Point{
			Time:        float64(index) / rate,
			SampleIndex: index / 4,
			Values:      value,
			Offsets:     [4]int64{int64(index) * 16, int64(index)*16 + 4, int64(index)*16 + 8, int64(index)*16 + 12},
		}
	}
	return out
}

func run(t *testing.T, defect synth.Defect, mutate func(*synth.AttitudeOptions), tune func(*detect.Params)) *detect.Result {
	t.Helper()
	options := synth.AttitudeOptions{Defect: defect, Rate: testRate, Seconds: 30, Seed: 20260901}
	if mutate != nil {
		mutate(&options)
	}
	params := detect.Defaults()
	if tune != nil {
		tune(&params)
	}
	result, err := detect.Run(points(synth.Attitude(options), options.Rate), params)
	if err != nil {
		t.Fatalf("detect.Run: %v", err)
	}
	return result
}

func eventsCovering(result *detect.Result, seconds float64) []detect.Event {
	var covering []detect.Event
	for _, event := range result.Events {
		if event.StartSeconds <= seconds && seconds <= event.EndSeconds {
			covering = append(covering, event)
		}
	}
	return covering
}

// TestWhipPanProducesNoEvents is the most important test here. A whip-pan is
// fast rotation with no short-timescale deviation, and flagging it would mean
// smoothing footage that was fine. False positives on intentional motion are
// the main risk of automatic detection.
func TestWhipPanProducesNoEvents(t *testing.T) {
	result := run(t, synth.DefectWhipPan, nil, nil)
	for _, event := range result.Events {
		if event.Action != detect.ActionNone {
			t.Errorf("whip-pan produced an actionable %s event at %.3f-%.3f (severity %.1f)",
				event.Class, event.StartSeconds, event.EndSeconds, event.Severity)
		}
	}
	if len(eventsCovering(result, (synth.WhipPanStart+synth.WhipPanEnd)/2)) > 0 {
		for _, event := range eventsCovering(result, (synth.WhipPanStart+synth.WhipPanEnd)/2) {
			if event.Class != detect.ClassMotion {
				t.Errorf("whip-pan classified as %s, want motion or nothing", event.Class)
			}
		}
	}
}

func TestCleanClipProducesNoEvents(t *testing.T) {
	result := run(t, synth.DefectNone, nil, nil)
	for _, event := range result.Events {
		if event.Action != detect.ActionNone {
			t.Errorf("clean clip produced an actionable %s event at %.3f-%.3f",
				event.Class, event.StartSeconds, event.EndSeconds)
		}
	}
	if result.AffectedSeconds != 0 {
		t.Errorf("clean clip reported %.3fs affected", result.AffectedSeconds)
	}
}

func TestJitterIsFoundAndClassified(t *testing.T) {
	result := run(t, synth.DefectJitter, nil, nil)
	middle := (synth.JitterStart + synth.JitterEnd) / 2
	covering := eventsCovering(result, middle)
	if len(covering) == 0 {
		t.Fatalf("no event covers the injected jitter at %.2fs; got %+v", middle, result.Events)
	}
	event := covering[0]
	if event.Class != detect.ClassJitter {
		t.Errorf("classified as %s, want jitter", event.Class)
	}
	if event.Action != detect.ActionSmooth {
		t.Errorf("action is %s, want smooth", event.Action)
	}
	if event.SpikeCount < 2 {
		t.Errorf("jitter reported %d peaks, expected several", event.SpikeCount)
	}
	// The derived window must be a jitter-sized one, not the impact range.
	if event.SmoothingMS < 120 || event.SmoothingMS > 400 {
		t.Errorf("derived smoothing window %.0f ms is outside the jitter range", event.SmoothingMS)
	}
}

func TestVectorChangeJitterIsFoundWithoutFlaggingTheCleanChange(t *testing.T) {
	clean := run(t, synth.DefectVectorChange, nil, nil)
	for _, event := range clean.Events {
		inFixture := event.EndSeconds >= synth.JitterStart-0.8 &&
			event.StartSeconds <= synth.JitterStart+0.8
		if inFixture && event.Action != detect.ActionNone {
			t.Errorf("clean vector change produced actionable %s event at %.3f-%.3f",
				event.Class, event.StartSeconds, event.EndSeconds)
		}
	}

	jitter := run(t, synth.DefectVectorJitter, nil, nil)
	covering := eventsCovering(jitter, synth.JitterStart+0.25)
	found := false
	for _, event := range covering {
		if event.Action == detect.ActionSmooth {
			found = true
		}
	}
	if !found {
		t.Fatalf("no smoothing event covers low-frequency vector jitter; events: %+v", jitter.Events)
	}
}

func TestOverRateMotionWithoutReturnIsNotABridgeableDropout(t *testing.T) {
	const count = 400
	track := make([]quat.Q, count)
	for index := range track {
		track[index] = quat.Q{1, 0, 0, 0}
	}
	rotation := func(degrees float64) quat.Q {
		half := degrees * math.Pi / 180 / 2
		return quat.Q{math.Cos(half), math.Sin(half), 0, 0}
	}
	// Two consecutive 3000 degree/second steps are over the configured sensor
	// rate, but continue in the same direction. They are rapid motion, not the
	// entry and return edges of a corrupt plateau.
	track[200] = rotation(15)
	for index := 201; index < len(track); index++ {
		track[index] = rotation(30)
	}
	result, err := detect.Run(points(track, testRate), detect.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if result.Implausible[200] || result.Implausible[201] {
		t.Fatalf("continuing over-rate motion was marked implausible: [%v %v]",
			result.Implausible[200], result.Implausible[201])
	}
	for _, event := range result.Events {
		if event.Class == detect.ClassDropout && event.Action == detect.ActionBridge {
			t.Errorf("continuing over-rate motion became a bridge at %.3f-%.3f",
				event.StartSeconds, event.EndSeconds)
		}
	}
}

func TestUnequalFastReversalWithoutReturnIsNotABridgeableDropout(t *testing.T) {
	const count = 400
	track := make([]quat.Q, count)
	for index := range track {
		track[index] = quat.Q{1, 0, 0, 0}
	}
	rotation := func(degrees float64) quat.Q {
		half := degrees * math.Pi / 180 / 2
		return quat.Q{math.Cos(half), math.Sin(half), 0, 0}
	}
	// The track moves out at 1600 degree/second and back at 1200, then settles
	// two degrees away. Opposing vectors alone do not prove corrupt data.
	track[200] = rotation(8)
	for index := 201; index < len(track); index++ {
		track[index] = rotation(2)
	}
	result, err := detect.Run(points(track, testRate), detect.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if result.Implausible[200] {
		t.Fatal("unequal fast reversal was marked implausible without returning to its entry attitude")
	}
}

func TestEventBoundsStayWithinTrack(t *testing.T) {
	result := run(t, synth.DefectJitter, func(options *synth.AttitudeOptions) {
		options.JitterAt = 29.0
	}, nil)
	lastTime := float64(result.QuaternionCount-1) / testRate
	if len(result.Events) == 0 {
		t.Fatal("tail fixture produced no events")
	}
	for _, event := range result.Events {
		if event.StartSeconds < 0 || event.EndSeconds > lastTime {
			t.Errorf("event %.6f-%.6f escapes track 0-%.6f",
				event.StartSeconds, event.EndSeconds, lastTime)
		}
	}
}

func TestImpactIsFoundAndGetsAShortWindow(t *testing.T) {
	result := run(t, synth.DefectImpact, nil, nil)
	covering := eventsCovering(result, synth.ImpactAt+synth.ImpactLength/2)
	if len(covering) == 0 {
		t.Fatalf("no event covers the injected impact at %.2fs; got %+v", synth.ImpactAt, result.Events)
	}
	event := covering[0]
	if event.Action != detect.ActionSmooth {
		t.Errorf("action is %s, want smooth", event.Action)
	}
	if event.SmoothingMS > 400 {
		t.Errorf("derived smoothing window %.0f ms is too wide for a transient", event.SmoothingMS)
	}
}

func TestDropoutIsFoundAndBridged(t *testing.T) {
	result := run(t, synth.DefectDropout, nil, nil)
	covering := eventsCovering(result, synth.DropoutAt)
	if len(covering) == 0 {
		t.Fatalf("no event covers the injected dropout at %.2fs; got %+v", synth.DropoutAt, result.Events)
	}
	var dropout *detect.Event
	for index := range covering {
		if covering[index].Class == detect.ClassDropout {
			dropout = &covering[index]
		}
	}
	if dropout == nil {
		t.Fatalf("the dropout was not classified as one: %+v", covering)
	}
	if dropout.Action != detect.ActionBridge {
		t.Errorf("action is %s, want bridge", dropout.Action)
	}

	// The corrupt samples themselves, and only those, must fail the gate.
	first := int(synth.DropoutAt * testRate)
	for offset := 0; offset < synth.DropoutSamples; offset++ {
		if !result.Implausible[first+offset] {
			t.Errorf("corrupt sample %d passed the plausibility gate", first+offset)
		}
	}
	if result.Implausible[first-1] || result.Implausible[first+synth.DropoutSamples] {
		t.Error("the plausibility gate condemned a good sample next to the dropout")
	}
}

func TestBridgingIsRefusedForLongRuns(t *testing.T) {
	result := run(t, synth.DefectDropout, nil, func(params *detect.Params) {
		params.BridgeMaxSamples = 1
	})
	covering := eventsCovering(result, synth.DropoutAt)
	for _, event := range covering {
		if event.Class == detect.ClassDropout && event.Action == detect.ActionBridge {
			t.Errorf("a %d-sample run was bridged with --bridge-max-samples 1", synth.DropoutSamples)
		}
	}
}

func TestNoBridgeDisablesReconstruction(t *testing.T) {
	result := run(t, synth.DefectDropout, nil, func(params *detect.Params) {
		params.NoBridge = true
	})
	for _, event := range result.Events {
		if event.Action == detect.ActionBridge {
			t.Error("a bridge survived --no-bridge")
		}
	}
}

// roughClip builds a clip that is rough for the first roughUntil seconds and
// calm afterwards, with a jitter burst injected in the calm tail.
func roughClip(t *testing.T, roughUntil, jitterAt float64, forceGlobal bool) *detect.Result {
	t.Helper()
	track := synth.Attitude(synth.AttitudeOptions{
		Defect: synth.DefectJitter, Rate: testRate, Seconds: 60, Seed: 20260901,
		RoughUntil: roughUntil, RoughDPS: 120, JitterAt: jitterAt,
	})
	params := detect.Defaults()
	if forceGlobal {
		// Force the reference's single global baseline, for comparison.
		params.ShortClipSeconds = 1e9
	}
	result, err := detect.Run(points(track, testRate), params)
	if err != nil {
		t.Fatalf("detect.Run: %v", err)
	}
	return result
}

// TestRollingBaselineDoesNotBlanketFlagARoughSegment is the main reason the
// global baseline was replaced.
//
// With a third of the clip genuinely rough, a single global threshold sits at
// the quiet floor and flags the whole rough stretch — a third of the footage,
// which is both wrong and exactly the blanket smoothing --max-affected exists
// to refuse. A window that slides raises the threshold where the footage is
// actually rough and leaves it alone.
func TestRollingBaselineDoesNotBlanketFlagARoughSegment(t *testing.T) {
	rolling := roughClip(t, 20, 57, false)
	global := roughClip(t, 20, 57, true)
	if !rolling.Rolling {
		t.Fatal("expected a rolling baseline on a 60s clip")
	}
	if rolling.AffectedFraction >= global.AffectedFraction/2 {
		t.Errorf("rolling flagged %.1f%% against the global baseline's %.1f%%; expected far less",
			rolling.AffectedFraction*100, global.AffectedFraction*100)
	}
	if rolling.AffectedFraction > 0.10 {
		t.Errorf("rolling still flagged %.1f%% of a mostly-fine clip", rolling.AffectedFraction*100)
	}
}

// TestRollingBaselineStillSeesTheCalmTailOfAMostlyRoughClip covers the other
// direction: the reference's baseline is the median of the quietest 55% of
// bins, so it resists a rough segment far better than a plain median would —
// it only breaks once the rough part crowds the calm part out of that quietest
// slice, at roughly 72% coverage. Past that it goes blind, and the rolling
// window does not.
func TestRollingBaselineStillSeesTheCalmTailOfAMostlyRoughClip(t *testing.T) {
	const jitterAt = 57.0
	rolling := roughClip(t, 50, jitterAt, false)
	global := roughClip(t, 50, jitterAt, true)
	middle := jitterAt + (synth.JitterEnd-synth.JitterStart)/2

	if len(eventsCovering(rolling, middle)) == 0 {
		t.Errorf("the rolling baseline missed the jitter at %.1fs; events: %+v", middle, rolling.Events)
	}
	if len(eventsCovering(global, middle)) != 0 {
		t.Skip("the global baseline also found it on this seed; the comparison is not meaningful here")
	}
}

func TestShortClipsFallBackToTheGlobalBaseline(t *testing.T) {
	result := run(t, synth.DefectNone, func(options *synth.AttitudeOptions) {
		options.Seconds = 10
	}, nil)
	if result.Rolling {
		t.Error("a 10s clip used a rolling baseline; the window cannot slide over it")
	}
}

func TestWeightEnvelopeIsZeroOutsideSmoothingEvents(t *testing.T) {
	result := run(t, synth.DefectMixed, nil, nil)
	inside := func(index int) bool {
		seconds := float64(index) / testRate
		for _, event := range result.Events {
			if event.Action != detect.ActionSmooth {
				continue
			}
			// Allow for the envelope's blurred shoulders.
			if seconds >= event.StartSeconds-0.2 && seconds <= event.EndSeconds+0.2 {
				return true
			}
		}
		return false
	}
	for index, weight := range result.Weights {
		if weight > 0 && !inside(index) {
			t.Fatalf("weight %.4f at %.3fs is outside every smoothing event", weight, float64(index)/testRate)
		}
		if weight < 0 || weight > 1 {
			t.Fatalf("weight %.4f at index %d is outside [0, 1]", weight, index)
		}
	}
}

func TestConfirmedEventHasMeaningfulCorrectionWeight(t *testing.T) {
	result := run(t, synth.DefectVectorJitter, nil, nil)
	positive, total := 0, 0.0
	for _, event := range result.Events {
		if event.Action != detect.ActionSmooth {
			continue
		}
		for index := event.FirstPoint; index <= event.LastPoint; index++ {
			if result.Weights[index] > 0 {
				positive++
				total += result.Weights[index]
			}
		}
	}
	if positive == 0 {
		t.Fatal("confirmed vector-jitter event has no correction weight")
	}
	if average := total / float64(positive); average < 0.5 {
		t.Errorf("confirmed event average correction weight is %.3f, want at least 0.5", average)
	}
}

func TestConfirmedEventCoreUsesOneStableWeight(t *testing.T) {
	result := run(t, synth.DefectVectorJitter, nil, nil)
	for _, event := range result.Events {
		if event.Action != detect.ActionSmooth {
			continue
		}
		minimum, maximum := 1.0, 0.0
		for index := event.FirstPoint; index <= event.LastPoint; index++ {
			minimum = math.Min(minimum, result.Weights[index])
			maximum = math.Max(maximum, result.Weights[index])
		}
		if maximum-minimum > 1e-12 {
			t.Errorf("event core weight varies from %.6f to %.6f", minimum, maximum)
		}
		return
	}
	t.Fatal("vector-jitter fixture produced no smoothing event")
}

func TestSensitivityMovesTheThreshold(t *testing.T) {
	quiet := run(t, synth.DefectJitter, nil, func(params *detect.Params) { params.Sensitivity = 0.5 })
	loud := run(t, synth.DefectJitter, nil, func(params *detect.Params) { params.Sensitivity = 2.0 })
	if !(loud.ThresholdDPS < quiet.ThresholdDPS) {
		t.Errorf("higher sensitivity gave threshold %.2f, not below %.2f", loud.ThresholdDPS, quiet.ThresholdDPS)
	}
}

func TestProfilesOrderBySensitivity(t *testing.T) {
	thresholds := map[string]float64{}
	for _, name := range []string{"conservative", "balanced", "aggressive"} {
		params, err := detect.ProfileParams(name)
		if err != nil {
			t.Fatal(err)
		}
		result, err := detect.Run(points(synth.Attitude(synth.AttitudeOptions{
			Defect: synth.DefectJitter, Rate: testRate, Seconds: 30, Seed: 1,
		}), testRate), params)
		if err != nil {
			t.Fatal(err)
		}
		thresholds[name] = result.ThresholdDPS
	}
	if !(thresholds["aggressive"] <= thresholds["balanced"] && thresholds["balanced"] <= thresholds["conservative"]) {
		t.Errorf("profiles are not ordered by sensitivity: %v", thresholds)
	}
	if _, err := detect.ProfileParams("reckless"); err == nil {
		t.Error("an unknown profile was accepted")
	}
}

func TestParamsValidation(t *testing.T) {
	base := detect.Defaults()
	if err := base.Validate(); err != nil {
		t.Fatalf("defaults are invalid: %v", err)
	}
	for name, mutate := range map[string]func(*detect.Params){
		"sensitivity too low":  func(p *detect.Params) { p.Sensitivity = 0.01 },
		"sensitivity too high": func(p *detect.Params) { p.Sensitivity = 10 },
		"negative mad-k":       func(p *detect.Params) { p.MADK = -1 },
		"zero window":          func(p *detect.Params) { p.BaselineWindow = 0 },
		"negative floor":       func(p *detect.Params) { p.FloorDPS = -1 },
		"severity out of band": func(p *detect.Params) { p.MinSeverity = 99 },
		"zero full scale":      func(p *detect.Params) { p.IMUFullScale = 0 },
		"negative bridge":      func(p *detect.Params) { p.BridgeMaxSamples = -2 },
		"zero bin width":       func(p *detect.Params) { p.BinSeconds = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			params := base
			mutate(&params)
			if err := params.Validate(); err == nil {
				t.Error("accepted invalid parameters")
			}
		})
	}
}

func TestTooFewPointsIsNotAnError(t *testing.T) {
	result, err := detect.Run(points(synth.Attitude(synth.AttitudeOptions{
		Rate: testRate, Seconds: 0.05,
	}), testRate), detect.Defaults())
	if err != nil {
		t.Fatalf("a very short track should report nothing, not fail: %v", err)
	}
	if len(result.Events) != 0 {
		t.Errorf("found %d events in a 10-sample track", len(result.Events))
	}
}

func TestSeverityIsMonotonicInPeakRatio(t *testing.T) {
	// Severity is 4 + 3*log2(peak/threshold), capped at 10. Two clips with the
	// same threshold but different peaks must order correctly.
	weak := run(t, synth.DefectJitter, func(options *synth.AttitudeOptions) {
		options.NoiseDPS = 0.35
	}, func(params *detect.Params) { params.FloorDPS = 400 })
	strong := run(t, synth.DefectJitter, func(options *synth.AttitudeOptions) {
		options.NoiseDPS = 0.35
	}, func(params *detect.Params) { params.FloorDPS = 60 })
	weakest, strongest := 0.0, 0.0
	for _, event := range weak.Events {
		weakest = math.Max(weakest, event.Severity)
	}
	for _, event := range strong.Events {
		strongest = math.Max(strongest, event.Severity)
	}
	if strongest < weakest {
		t.Errorf("a lower threshold gave lower severity: %.2f vs %.2f", strongest, weakest)
	}
}
