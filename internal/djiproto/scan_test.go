package djiproto_test

import (
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/steamvogue/djgyrofix/internal/djiproto"
	"github.com/steamvogue/djgyrofix/internal/quat"
)

func varint(value uint64) []byte {
	var out []byte
	for value >= 0x80 {
		out = append(out, byte(value&0x7F)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}

func message(fieldNumber int, payload []byte) []byte {
	out := varint(uint64(fieldNumber)<<3 | 2)
	out = append(out, varint(uint64(len(payload)))...)
	return append(out, payload...)
}

func quaternionFields(values [4]float32) []byte {
	var out []byte
	for index, value := range values {
		out = append(out, varint(uint64(index+1)<<3|5)...)
		raw := make([]byte, 4)
		binary.LittleEndian.PutUint32(raw, math.Float32bits(value))
		out = append(out, raw...)
	}
	return out
}

// wm169Sample nests a quaternion under the wm169 field path 3.3.2.3.
func wm169Sample(values [4]float32) []byte {
	return message(3, message(3, message(2, message(3, quaternionFields(values)))))
}

func TestPatchKeepsMessageSize(t *testing.T) {
	data := wm169Sample([4]float32{0.5, -0.5, -0.5, 0.5})
	before := len(data)

	refs, err := djiproto.Quaternions(data, djiproto.VariantWM169)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("found %d quaternions, want 1", len(refs))
	}

	replacement := quat.Q{0.6, -0.4, -0.4, math.Sqrt(0.48)}
	if err := djiproto.WriteQuaternion(data, refs[0], replacement, djiproto.DefaultTolerance); err != nil {
		t.Fatal(err)
	}
	if len(data) != before {
		t.Fatalf("patch resized the sample: %d bytes, was %d", len(data), before)
	}

	updated, err := djiproto.Quaternions(data, djiproto.VariantWM169)
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range replacement {
		if math.Abs(updated[0].Values[index]-want) > 1e-6 {
			t.Errorf("component %d = %g, want %g", index, updated[0].Values[index], want)
		}
	}
}

// TestOmittedComponentIsAHardError covers the proto3 case from plan §12: a
// component that was zero has no byte slot, and silently skipping it would
// leave the orientation half-updated and corrupt.
func TestOmittedComponentIsAHardError(t *testing.T) {
	// Only components 1 and 2 are present, as proto3 would emit for (w, x, 0, 0).
	var body []byte
	body = append(body, varint(1<<3|5)...)
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, math.Float32bits(0.7071068))
	body = append(body, raw...)
	body = append(body, varint(2<<3|5)...)
	binary.LittleEndian.PutUint32(raw, math.Float32bits(0.7071068))
	body = append(body, raw...)

	data := message(3, message(3, message(2, message(3, body))))
	refs, err := djiproto.Quaternions(data, djiproto.VariantWM169)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("found %d quaternions, want 1", len(refs))
	}
	if refs[0].Offsets[2] != -1 || refs[0].Offsets[3] != -1 {
		t.Fatalf("expected components 3 and 4 to have no slot, got offsets %v", refs[0].Offsets)
	}

	if err := djiproto.WriteQuaternion(data, refs[0], quat.Q{0.7, 0.7, 0.1, 0}, djiproto.DefaultTolerance); err == nil {
		t.Error("writing a non-zero value into an omitted component was accepted")
	}
	// Writing zero back into an omitted slot is a no-op, not an error.
	if err := djiproto.WriteQuaternion(data, refs[0], quat.Q{0.7071068, 0.7071068, 0, 0}, djiproto.DefaultTolerance); err != nil {
		t.Errorf("writing zeros into omitted components failed: %v", err)
	}
}

func TestNonUnitQuaternionsAreIgnored(t *testing.T) {
	// The scanner walks blind field paths, so the near-unit-norm check is the
	// only thing separating a real quaternion from four unrelated float32s.
	for _, values := range [][4]float32{
		{10, 0, 0, 0},
		{0.01, 0.01, 0, 0},
		{float32(math.NaN()), 0, 0, 1},
	} {
		data := wm169Sample(values)
		refs, err := djiproto.Quaternions(data, djiproto.VariantWM169)
		if err != nil {
			t.Fatal(err)
		}
		if len(refs) != 0 {
			t.Errorf("%v was accepted as a quaternion", values)
		}
	}
}

func TestVariantPathsAreDistinct(t *testing.T) {
	// A wm169 sample must not be readable as wa530: the paths differ and a
	// wrong guess should find nothing rather than garbage.
	data := wm169Sample([4]float32{0.5, -0.5, -0.5, 0.5})
	refs, err := djiproto.Quaternions(data, djiproto.VariantWA530)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("wa530 path found %d quaternions in a wm169 sample", len(refs))
	}
	if _, err := djiproto.Quaternions(data, djiproto.Variant("nope")); err == nil {
		t.Error("an unknown variant was accepted")
	}
}

func TestDetectVariantSniffsTheModelString(t *testing.T) {
	cases := map[string]struct {
		want       djiproto.Variant
		recognised bool
	}{
		"oq101": {djiproto.VariantOQ101, true},
		"wa530": {djiproto.VariantWA530, true},
		"wm169": {djiproto.VariantWM169, true},
		// wm169 is also the fallback, so an unrecognised stream lands on the
		// same variant while reporting that it was not recognised.
		"other": {djiproto.VariantWM169, false},
	}
	for marker, want := range cases {
		samples := [][]byte{[]byte("junk" + marker + "junk")}
		got, recognised := djiproto.DetectVariant(samples)
		if got != want.want || recognised != want.recognised {
			t.Errorf("DetectVariant(%q) = %q/%v, want %q/%v",
				marker, got, recognised, want.want, want.recognised)
		}
	}
	// Only the first kilobyte of the first five samples is probed.
	padded := append(make([]byte, 2000), []byte("oq101")...)
	if got, recognised := djiproto.DetectVariant([][]byte{padded}); got != djiproto.VariantWM169 || recognised {
		t.Errorf("DetectVariant looked past the first kilobyte, got %q/%v", got, recognised)
	}
}

// TestSchemaNameOutranksTheSubstringProbe pins the signal order. DJI names its
// own protobuf definition in the stream, which is a statement rather than a
// guess, and no real clip measured here carries the substrings the older probe
// looks for — all three would otherwise have reached the default by luck.
func TestSchemaNameOutranksTheSubstringProbe(t *testing.T) {
	// A sample whose schema says one thing and whose bytes contain the marker
	// for another. The schema wins.
	sample := schemaSample(t, "dvtm_O4P.proto", "wa530 appears in the payload")
	got, recognised := djiproto.DetectVariant([][]byte{sample})
	if got != djiproto.VariantWM169 || !recognised {
		t.Errorf("schema name lost to the substring probe: %q/%v", got, recognised)
	}

	unknown := schemaSample(t, "dvtm_future9.proto", "nothing familiar here")
	if got, recognised := djiproto.DetectVariant([][]byte{unknown}); recognised {
		t.Errorf("an unknown schema was reported as recognised: %q", got)
	}
}

// schemaSample builds a sample carrying a schema name at 1.1.1 plus a trailer
// in an unrelated field, without depending on the synth fixture builder. The
// trailer goes inside field 15 rather than after the message, because a real
// sample is well-formed protobuf from end to end and a scanner that has to
// tolerate trailing garbage would be testing something DJI never produces.
func schemaSample(t *testing.T, schema, trailer string) []byte {
	t.Helper()
	inner := append([]byte{0x0A, byte(len(schema))}, schema...)
	middle := append([]byte{0x0A, byte(len(inner))}, inner...)
	outer := append([]byte{0x0A, byte(len(middle))}, middle...)
	marker := append([]byte{0x7A, byte(len(trailer))}, trailer...)
	return append(outer, marker...)
}

func TestReadIdentity(t *testing.T) {
	sample := schemaSample(t, "dvtm_O4P.proto", "")
	identity := djiproto.ReadIdentity(sample)
	if identity.Schema != "dvtm_O4P.proto" {
		t.Errorf("schema = %q", identity.Schema)
	}
	// Absent fields stay empty rather than being guessed at.
	if identity.Model != "" || identity.Firmware != "" || identity.Serial != "" {
		t.Errorf("absent fields were invented: %+v", identity)
	}
	if identity := djiproto.ReadIdentity([]byte{0xFF, 0xFF}); identity.Schema != "" {
		t.Errorf("malformed input produced an identity: %+v", identity)
	}
}

func TestMalformedInputIsRejectedNotPanicked(t *testing.T) {
	cases := map[string][]byte{
		"truncated varint":       {0xFF, 0xFF, 0xFF},
		"length past end":        {0x1A, 0x7F, 0x01},
		"field number zero":      {0x00, 0x01},
		"unsupported wire type":  {0x1C},
		"truncated fixed32":      {0x0D, 0x01, 0x02},
		"group start wire type3": {0x1B},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := djiproto.Fields(data, 0, len(data)); err == nil {
				t.Errorf("accepted malformed input %x", data)
			}
		})
	}
	if _, err := djiproto.Fields([]byte{0x08, 0x01}, 0, 99); err == nil {
		t.Error("accepted an end offset past the buffer")
	}
	if _, err := djiproto.Fields([]byte{0x08, 0x01}, 5, 1); err == nil {
		t.Error("accepted an inverted range")
	}
}

// FuzzQuaternions exercises the scanner against arbitrary bytes. It walks
// attacker-controllable length fields, so it must reject rather than panic or
// allocate wildly.
func FuzzQuaternions(f *testing.F) {
	f.Add(wm169Sample([4]float32{0.5, -0.5, -0.5, 0.5}))
	f.Add(message(3, message(3, message(4, message(3, quaternionFields([4]float32{1, 0, 0, 0}))))))
	f.Add([]byte{0x1A, 0xFF, 0xFF, 0xFF, 0x7F})
	f.Add([]byte{})
	f.Add([]byte{0x08, 0x96, 0x01})

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, variant := range djiproto.Variants {
			refs, err := djiproto.Quaternions(data, variant)
			if err != nil {
				continue
			}
			if len(refs) == 0 {
				continue
			}
			// One buffer copy per variant, not per quaternion: a crafted input
			// can hold thousands of them and copying inside the loop makes the
			// harness quadratic, which starves the fuzzer rather than the code
			// under test.
			copyOf := append([]byte(nil), data...)
			for _, ref := range refs {
				for _, offset := range ref.Offsets {
					if offset < -1 || offset+4 > len(data) {
						t.Fatalf("offset %d escapes a %d-byte buffer", offset, len(data))
					}
				}
				// Anything the scanner accepts must be safe to write back
				// without resizing the buffer.
				if err := djiproto.WriteQuaternion(copyOf, ref, ref.Values, djiproto.DefaultTolerance); err != nil {
					t.Fatalf("could not write back an accepted quaternion: %v", err)
				}
			}
			if len(copyOf) != len(data) {
				t.Fatalf("write resized the buffer to %d from %d", len(copyOf), len(data))
			}
		}
	})
}

// TestScannerCostStaysLinear guards against a crafted sample turning the nested
// path walk quadratic. Field paths are up to five levels deep and every level
// can match many messages, so this is worth pinning down.
func TestScannerCostStaysLinear(t *testing.T) {
	inner := quaternionFields([4]float32{0.5, -0.5, -0.5, 0.5})
	var level3 []byte
	for index := 0; index < 400; index++ {
		level3 = append(level3, message(3, inner)...)
	}
	var level2 []byte
	for index := 0; index < 20; index++ {
		level2 = append(level2, message(2, level3)...)
	}
	data := message(3, message(3, level2))

	start := time.Now()
	refs, err := djiproto.Quaternions(data, djiproto.VariantWM169)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 8000 {
		t.Fatalf("found %d quaternions, want 8000", len(refs))
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("scanning %d bytes took %v, which is not linear behaviour", len(data), elapsed)
	}
}
