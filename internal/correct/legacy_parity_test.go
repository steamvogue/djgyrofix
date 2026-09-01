package correct_test

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/steamvogue/djgyrofix/internal/correct"
	"github.com/steamvogue/djgyrofix/internal/quat"
)

// goldenCase is one fixture produced by testdata/golden/gen_smoothing.py against
// the DJIGyroFix v0.92 Python reference.
type goldenCase struct {
	Name        string     `json:"name"`
	Start       float64    `json:"start"`
	End         float64    `json:"end"`
	SmoothingMS float64    `json:"smoothing_ms"`
	Strength    float64    `json:"strength"`
	Times       []string   `json:"times"`
	Input       []string   `json:"input"`
	InputExact  [][]string `json:"input_exact"`
	Output      []string   `json:"output"`
	ScoreBefore string     `json:"score_before"`
	ScoreAfter  string     `json:"score_after"`
}

func decodeFloat64(t *testing.T, value string) float64 {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 8 {
		t.Fatalf("bad float64 hex %q: %v", value, err)
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(raw))
}

func encodeQuat32(q quat.Q) string {
	raw := make([]byte, 16)
	for index, value := range q {
		binary.LittleEndian.PutUint32(raw[index*4:], math.Float32bits(float32(value)))
	}
	return hex.EncodeToString(raw)
}

// TestLegacyMatchesPythonReference is the golden-parity gate of plan §9.1.
// Every quaternion the Go port writes must land on the same four bytes the
// reference writes. A mismatch is a bug, not a rounding difference.
func TestLegacyMatchesPythonReference(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/golden/smoothing.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures struct {
		Cases []goldenCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if len(fixtures.Cases) == 0 {
		t.Fatal("no golden cases")
	}

	for _, fixture := range fixtures.Cases {
		t.Run(fixture.Name, func(t *testing.T) {
			times := make([]float64, len(fixture.Times))
			for index, value := range fixture.Times {
				times[index] = decodeFloat64(t, value)
			}
			// The reference feeds smooth_quaternions the exact float64 values
			// it decoded, so the fixture carries them at full precision.
			input := make([]quat.Q, len(fixture.InputExact))
			for index, components := range fixture.InputExact {
				for component, value := range components {
					input[index][component] = decodeFloat64(t, value)
				}
			}

			output, err := correct.Legacy(times, input, correct.LegacyParams{
				StartSeconds: fixture.Start,
				EndSeconds:   fixture.End,
				SmoothingMS:  fixture.SmoothingMS,
				Strength:     fixture.Strength,
			})
			if err != nil {
				t.Fatalf("Legacy: %v", err)
			}
			if len(output) != len(fixture.Output) {
				t.Fatalf("got %d quaternions, want %d", len(output), len(fixture.Output))
			}
			mismatches := 0
			for index, want := range fixture.Output {
				got := encodeQuat32(output[index])
				if got != want {
					mismatches++
					if mismatches <= 5 {
						t.Errorf("sample %d (t=%g): got %s, want %s", index, times[index], got, want)
					}
				}
			}
			if mismatches > 0 {
				t.Errorf("%d of %d quaternions differ from the Python reference", mismatches, len(output))
			}

			before, err := correct.AngularAccelerationScore(times, input, fixture.Start, fixture.End)
			if err != nil {
				t.Fatalf("score before: %v", err)
			}
			after, err := correct.AngularAccelerationScore(times, output, fixture.Start, fixture.End)
			if err != nil {
				t.Fatalf("score after: %v", err)
			}
			// The score is not held to bit parity, and cannot be. It runs
			// 2*acos(cosine) with cosine riding against 1.0, where one ULP of
			// difference between Go's and glibc's acos moves the result by a
			// percent. Nothing is written from this value — it only feeds the
			// reported improvement percentage — so agreement to 1e-6 relative
			// is the honest bar. The written quaternions above are exact.
			assertClose(t, "score_before", before, decodeFloat64(t, fixture.ScoreBefore))
			assertClose(t, "score_after", after, decodeFloat64(t, fixture.ScoreAfter))
		})
	}
}

func assertClose(t *testing.T, label string, got, want float64) {
	t.Helper()
	if got == want {
		return
	}
	// Below this magnitude both numbers are angular acceleration noise floor
	// and their ratio carries no information.
	if math.Abs(want) < 1e-6 && math.Abs(got) < 1e-6 {
		return
	}
	if math.Abs(got-want)/math.Abs(want) > 1e-6 {
		t.Errorf("%s: got %v, want %v", label, got, want)
	}
}
