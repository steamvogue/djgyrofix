// Package djiproto scans DJI timed-metadata samples for orientation
// quaternions while remembering exactly where each component's four bytes live.
//
// This is deliberately not a protobuf library. Decoding into structs and
// re-serializing loses byte offsets and can change varint lengths, which would
// resize the MP4 sample and invalidate every offset in stsz/stco. The scanner
// walks the wire format in place and hands back byte offsets to overwrite.
package djiproto

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/steamvogue/djgyrofix/internal/quat"
)

// Error reports a malformed protobuf structure.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errorf(format string, args ...any) error {
	return &Error{msg: fmt.Sprintf(format, args...)}
}

// Wire types this scanner understands.
const (
	wireVarint = iota
	wireFixed64
	wireLengthDelimited
	_ // start group, unsupported
	_ // end group, unsupported
	wireFixed32
)

// Field is one protobuf field located inside a sample buffer.
type Field struct {
	Number       int
	WireType     int
	KeyStart     int
	ValueStart   int
	ValueEnd     int
	PayloadStart int
	PayloadEnd   int
}

// Ref is one quaternion found in a sample, with the byte offset of each
// component. An offset is -1 when proto3 omitted that component because it was
// zero — there is then no slot to write into.
type Ref struct {
	Values  quat.Q
	Offsets [4]int
}

// Variant names the DJI metadata layout. Each one nests the quaternion under a
// different field path.
type Variant string

// Known metadata variants.
const (
	VariantWM169 Variant = "wm169"
	VariantWA530 Variant = "wa530"
	VariantOQ101 Variant = "oq101"
)

// Variants lists every supported variant, in sniffing order.
var Variants = []Variant{VariantWM169, VariantWA530, VariantOQ101}

// quaternionPaths maps a variant to the nested field numbers leading to the
// message that holds the four fixed32 components. The DJI schema is not public;
// these paths were found empirically and are validated by the near-unit-norm
// check in Quaternions.
var quaternionPaths = map[Variant][]int{
	VariantWM169: {3, 3, 2, 3},
	VariantWA530: {3, 3, 4, 3},
	VariantOQ101: {3, 3, 2, 1, 3},
}

// Path returns the field path used to locate quaternions for a variant.
func (v Variant) Path() ([]int, bool) {
	path, ok := quaternionPaths[v]
	return path, ok
}

// Varint decodes the value of a wire-type-0 field located by Fields. It is
// exported for callers that read scalar fields the quaternion path walks past,
// such as the metadata track's own timestamp and sample counter.
func Varint(data []byte, position, end int) (uint64, error) {
	value, _, err := readVarint(data, position, end)
	return value, err
}

func readVarint(data []byte, position, end int) (uint64, int, error) {
	var value uint64
	shift := uint(0)
	for position < end && shift < 70 {
		current := data[position]
		position++
		value |= uint64(current&0x7F) << shift
		if current&0x80 == 0 {
			return value, position, nil
		}
		shift += 7
	}
	return 0, 0, errorf("invalid protobuf varint")
}

// Fields returns every top-level field between start and end.
func Fields(data []byte, start, end int) ([]Field, error) {
	if start < 0 || end < start || end > len(data) {
		return nil, errorf("invalid protobuf message boundary")
	}
	var fields []Field
	err := walkFields(data, start, end, func(field Field) bool {
		fields = append(fields, field)
		return true
	})
	return fields, err
}

// walkFields calls visit for each field in order, stopping early if visit
// returns false. Nothing is allocated per field, which matters when this runs
// over tens of millions of samples.
func walkFields(data []byte, start, end int, visit func(Field) bool) error {
	if start < 0 || end < start || end > len(data) {
		return errorf("invalid protobuf message boundary")
	}
	position := start
	for position < end {
		keyStart := position
		key, next, err := readVarint(data, position, end)
		if err != nil {
			return err
		}
		position = next
		number := int(key >> 3)
		wireType := int(key & 7)
		if number == 0 {
			return errorf("invalid protobuf field number 0")
		}
		valueStart := position
		var payloadStart, payloadEnd int
		switch wireType {
		case wireVarint:
			if _, position, err = readVarint(data, position, end); err != nil {
				return err
			}
			payloadStart, payloadEnd = valueStart, position
		case wireFixed64:
			position += 8
			payloadStart, payloadEnd = valueStart, position
		case wireLengthDelimited:
			length, next, err := readVarint(data, position, end)
			if err != nil {
				return err
			}
			payloadStart = next
			if length > uint64(end-payloadStart) {
				return errorf("protobuf field exceeds sample boundary")
			}
			payloadEnd = payloadStart + int(length)
			position = payloadEnd
		case wireFixed32:
			position += 4
			payloadStart, payloadEnd = valueStart, position
		default:
			return errorf("unsupported protobuf wire type %d", wireType)
		}
		if position > end {
			return errorf("protobuf field exceeds sample boundary")
		}
		if !visit(Field{
			Number:       number,
			WireType:     wireType,
			KeyStart:     keyStart,
			ValueStart:   valueStart,
			ValueEnd:     position,
			PayloadStart: payloadStart,
			PayloadEnd:   payloadEnd,
		}) {
			return nil
		}
	}
	return nil
}

type span struct{ start, end int }

// messagesAtPath descends the given field numbers, collecting the payload span
// of every length-delimited field matching each step.
func messagesAtPath(data []byte, start, end int, path []int) ([]span, error) {
	spans := []span{{start, end}}
	for _, fieldNumber := range path {
		var next []span
		for _, current := range spans {
			err := walkFields(data, current.start, current.end, func(field Field) bool {
				if field.Number == fieldNumber && field.WireType == wireLengthDelimited {
					next = append(next, span{field.PayloadStart, field.PayloadEnd})
				}
				return true
			})
			if err != nil {
				return nil, err
			}
		}
		spans = next
		if len(spans) == 0 {
			break
		}
	}
	return spans, nil
}

// schemaVariants maps DJI's own protobuf definition name to the layout that
// reads it. The stream names its schema in field 1.1.1, which is the closest
// thing to a declared format version it carries.
//
// Only what has been seen is listed. An unmatched schema is reported as
// unrecognised rather than mapped by resemblance: "dvtm_O4P.proto" and
// "dvtm_O4.proto" happen to share a layout, and guessing that any future
// dvtm_* does too is the kind of assumption that produces silent nonsense.
var schemaVariants = map[string]Variant{
	"dvtm_o4.proto":    VariantWM169,
	"dvtm_o4p.proto":   VariantWM169,
	"dvtm_ow001.proto": VariantWM169,
}

// DetectVariant sniffs the layout from the first few samples, and reports
// whether it actually recognised the stream or fell back to the default.
//
// The schema name is tried first because it is the camera's own statement of
// its format. The substring probe beneath it is the original heuristic, carried
// over from the Python reference; it has never matched on any real clip
// measured here — all three name their schema instead and would otherwise have
// reached the default by luck. That is why the second return value exists:
// falling through has to be visible rather than silent.
func DetectVariant(firstSamples [][]byte) (Variant, bool) {
	var probe strings.Builder
	for index, sample := range firstSamples {
		if index >= 5 {
			break
		}
		if identity := ReadIdentity(sample); identity.Schema != "" {
			if variant, ok := schemaVariants[strings.ToLower(identity.Schema)]; ok {
				return variant, true
			}
		}
		if len(sample) > 1024 {
			sample = sample[:1024]
		}
		probe.Write(sample)
	}
	lower := strings.ToLower(probe.String())
	if strings.Contains(lower, "oq101") {
		return VariantOQ101, true
	}
	if strings.Contains(lower, "wa530") {
		return VariantWA530, true
	}
	if strings.Contains(lower, "wm169") {
		return VariantWM169, true
	}
	return VariantWM169, false
}

// Quaternions returns every quaternion in a sample, in wire order, together
// with the byte offsets of its components. A candidate is accepted only when
// all four components are finite and the raw norm is near unity — the only
// validation available without the schema.
func Quaternions(data []byte, variant Variant) ([]Ref, error) {
	path, ok := quaternionPaths[variant]
	if !ok {
		return nil, errorf("unknown DJI metadata variant: %s", variant)
	}
	spans, err := messagesAtPath(data, 0, len(data), path)
	if err != nil {
		return nil, err
	}
	refs := make([]Ref, 0, len(spans))
	for _, current := range spans {
		ref := Ref{Offsets: [4]int{-1, -1, -1, -1}}
		err := walkFields(data, current.start, current.end, func(field Field) bool {
			if field.Number >= 1 && field.Number <= 4 && field.WireType == wireFixed32 {
				index := field.Number - 1
				bits := binary.LittleEndian.Uint32(data[field.PayloadStart : field.PayloadStart+4])
				ref.Values[index] = float64(math.Float32frombits(bits))
				ref.Offsets[index] = field.PayloadStart
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		norm := ref.Values.Norm()
		if norm >= 0.5 && norm <= 1.5 && ref.Values.IsFinite() {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// WriteQuaternion overwrites the four component slots in place. Each write is
// exactly four bytes at a scanned offset; no varint is ever re-emitted, so the
// sample size cannot change (invariant I3).
//
// When proto3 omitted a zero component there is no slot. Writing a non-zero
// value there would require resizing the sample, so it is a hard error rather
// than a silent skip — skipping would leave the orientation partly updated and
// corrupt it.
func WriteQuaternion(data []byte, ref Ref, values quat.Q, tolerance float64) error {
	if !values.IsFinite() {
		return errorf("quaternion must contain four finite components")
	}
	for index, value := range values {
		offset := ref.Offsets[index]
		if offset < 0 {
			if math.Abs(value) > tolerance {
				return errorf("a zero-valued quaternion component is omitted in the source protobuf; " +
					"it cannot be changed without resizing the MP4 sample")
			}
			continue
		}
		binary.LittleEndian.PutUint32(data[offset:offset+4], math.Float32bits(float32(value)))
	}
	return nil
}

// DefaultTolerance is the magnitude below which a missing component slot is
// considered unchanged.
const DefaultTolerance = 1e-7

// Identity is what a metadata sample says about the camera that wrote it.
//
// None of this is needed to patch a file. It is here because the open question
// about the O4 stabilization fault is which units and which firmware are
// affected, and nobody can correlate reports without these fields — public
// reports carry footage but not the trace or the camera behind it.
//
// Field numbers were read off three cameras and are not guaranteed beyond them,
// so every field is optional and an absent one stays empty rather than guessing.
type Identity struct {
	// Schema is the name of DJI's own protobuf definition, e.g.
	// "dvtm_O4P.proto". It is the closest thing to a declared format version in
	// the stream and a far better layout signal than sniffing a model string.
	Schema string `json:"schema,omitempty"`
	// Model is the camera as it names itself, e.g. "DJI O4P".
	Model string `json:"model,omitempty"`
	// Firmware varies per unit where the two version strings beside it track the
	// metadata format instead, which is the reading behind the name. Treat it as
	// a correlation key rather than an authoritative version.
	Firmware string `json:"firmware,omitempty"`
	// Serial identifies the individual unit. It is read but never printed
	// unless asked for: it is personal to the owner of the camera.
	Serial string `json:"serial,omitempty"`
	// Aircraft is the airframe model where one is recorded, e.g. "DJI FC8885".
	Aircraft string `json:"aircraft,omitempty"`
	// FormatVersions are the remaining version strings, kept because they are
	// what would distinguish a schema change within one model.
	FormatVersions []string `json:"format_versions,omitempty"`
}

// identityPaths maps a field path to the Identity member it fills.
var identityPaths = []struct {
	path   []int
	assign func(*Identity, string)
}{
	{[]int{1, 1, 1}, func(id *Identity, value string) { id.Schema = value }},
	{[]int{1, 1, 10}, func(id *Identity, value string) { id.Model = value }},
	{[]int{1, 1, 6}, func(id *Identity, value string) { id.Firmware = value }},
	{[]int{1, 1, 5}, func(id *Identity, value string) { id.Serial = value }},
	{[]int{2, 2, 1, 4}, func(id *Identity, value string) { id.Aircraft = value }},
	{[]int{3, 2, 1, 4}, func(id *Identity, value string) {
		if id.Aircraft == "" {
			id.Aircraft = value
		}
	}},
	{[]int{1, 1, 2}, func(id *Identity, value string) { id.FormatVersions = append(id.FormatVersions, value) }},
	{[]int{1, 1, 3}, func(id *Identity, value string) { id.FormatVersions = append(id.FormatVersions, value) }},
}

// ReadIdentity extracts what a sample says about its camera. Fields it cannot
// find or that do not hold plain text are left empty.
func ReadIdentity(sample []byte) Identity {
	var identity Identity
	for _, entry := range identityPaths {
		if value, ok := textAt(sample, entry.path); ok {
			entry.assign(&identity, value)
		}
	}
	return identity
}

// textAt walks a field path and returns the payload when it is printable text.
func textAt(data []byte, path []int) (string, bool) {
	start, end := 0, len(data)
	for index, number := range path {
		fields, err := Fields(data, start, end)
		if err != nil {
			return "", false
		}
		found := false
		for _, field := range fields {
			if field.Number != number || field.WireType != 2 {
				continue
			}
			start, end, found = field.PayloadStart, field.PayloadEnd, true
			break
		}
		if !found {
			return "", false
		}
		if index == len(path)-1 {
			return printableText(data[start:end])
		}
	}
	return "", false
}

func printableText(payload []byte) (string, bool) {
	if len(payload) == 0 || len(payload) > 64 {
		return "", false
	}
	for _, character := range payload {
		if character < 0x20 || character > 0x7e {
			return "", false
		}
	}
	return strings.TrimSpace(string(payload)), true
}
