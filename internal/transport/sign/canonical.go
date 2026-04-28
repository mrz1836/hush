package sign

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"strconv"
)

// RawMessage is a named byte slice whose contents are inserted verbatim into
// the canonical output (FR-022 escape hatch). A nil or zero-length RawMessage
// emits the JSON null literal.
type RawMessage []byte

//nolint:gochecknoglobals // sentinel-class type token, set-once at package load
var rawMessageType = reflect.TypeOf(RawMessage(nil))

// CanonicalJSON encodes v as deterministic JSON with map keys sorted
// lexicographically at every depth. User-defined MarshalJSON hooks are NOT
// invoked. Returns (nil, ErrCanonicalUnsupported) for non-finite floats,
// non-string-keyed maps, and unsupported Go kinds. Never returns partial output.
func CanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeValue(&buf, reflect.ValueOf(v)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

//nolint:gocyclo,cyclop // recursive reflect-walker: branching is inherent to the kind-switch design
func encodeValue(buf *bytes.Buffer, v reflect.Value) error {
	// Handle the zero Value (reflect.ValueOf(nil)) — emit null.
	if !v.IsValid() {
		buf.WriteString("null")
		return nil
	}

	// Dereference pointers and interfaces.
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			buf.WriteString("null")
			return nil
		}
		v = v.Elem()
	}

	// RawMessage: verbatim insertion (must check before the Slice branch).
	if v.Type() == rawMessageType {
		return encodeRawMessage(buf, v)
	}

	//nolint:exhaustive // default branch handles all unsupported kinds uniformly
	switch v.Kind() {
	case reflect.Bool:
		encodeBool(buf, v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		buf.WriteString(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		buf.WriteString(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		return encodeFloat(buf, v.Float())
	case reflect.String:
		encodeString(buf, v.String())
	case reflect.Slice:
		if v.IsNil() {
			buf.WriteString("null")
			return nil
		}
		return encodeArray(buf, v)
	case reflect.Array:
		return encodeArray(buf, v)
	case reflect.Map:
		return encodeMap(buf, v)
	case reflect.Struct:
		return encodeStruct(buf, v)
	default:
		// Func, Chan, UnsafePointer, Complex64, Complex128, etc.
		return ErrCanonicalUnsupported
	}

	return nil
}

func encodeBool(buf *bytes.Buffer, b bool) {
	if b {
		buf.WriteString("true")
	} else {
		buf.WriteString("false")
	}
}

func encodeFloat(buf *bytes.Buffer, f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return ErrCanonicalUnsupported
	}
	buf.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	return nil
}

// encodeString emits a JSON-quoted string. json.Marshal on a plain string value
// always succeeds — the error path cannot be triggered and is intentionally absent.
func encodeString(buf *bytes.Buffer, s string) {
	// Passing s (not a user-defined type) prevents MarshalJSON dispatch.
	quoted, _ := json.Marshal(s) //nolint:errchkjson // json.Marshal on string always succeeds
	buf.Write(quoted)
}

func encodeRawMessage(buf *bytes.Buffer, v reflect.Value) error {
	b := v.Bytes()
	if len(b) == 0 {
		buf.WriteString("null")
		return nil
	}
	buf.Write(b)
	return nil
}

func encodeArray(buf *bytes.Buffer, v reflect.Value) error {
	buf.WriteByte('[')
	n := v.Len()
	for i := range n {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := encodeValue(buf, v.Index(i)); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

func encodeMap(buf *bytes.Buffer, v reflect.Value) error {
	if v.IsNil() {
		buf.WriteString("null")
		return nil
	}
	if v.Type().Key().Kind() != reflect.String {
		return ErrCanonicalUnsupported
	}

	keys := make([]string, 0, v.Len())
	for _, k := range v.MapKeys() {
		keys = append(keys, k.String())
	}
	sort.Strings(keys)

	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodeString(buf, k)
		buf.WriteByte(':')
		if err := encodeValue(buf, v.MapIndex(reflect.ValueOf(k))); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// structField holds a resolved (name, index) pair for a struct field.
type structField struct {
	name  string
	index int
}

func encodeStruct(buf *bytes.Buffer, v reflect.Value) error {
	t := v.Type()

	fields := collectStructFields(t)

	// Sort by resolved field name (alphabetical).
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].name < fields[j].name
	})

	buf.WriteByte('{')
	first := true
	for _, sf := range fields {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		encodeString(buf, sf.name)
		buf.WriteByte(':')
		if err := encodeValue(buf, v.Field(sf.index)); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

func collectStructFields(t reflect.Type) []structField {
	fields := make([]structField, 0, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := resolveFieldName(f)
		fields = append(fields, structField{name: name, index: i})
	}
	return fields
}

func resolveFieldName(f reflect.StructField) string {
	tag, ok := f.Tag.Lookup("json")
	if !ok || tag == "" || tag == "-" {
		return f.Name
	}
	// Take only the first comma-separated token as the field name.
	for j, c := range tag {
		if c == ',' {
			tag = tag[:j]
			break
		}
	}
	if tag == "" {
		return f.Name
	}
	return tag
}
