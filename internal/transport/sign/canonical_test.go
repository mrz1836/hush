package sign

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

// TestCanonical_SortsAtAllDepths verifies 10 known shapes produce byte-identical
// output regardless of map iteration order.
func TestCanonical_SortsAtAllDepths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		input    any
		expected string
	}{
		{
			name:     "flat map top-level sort",
			input:    map[string]any{"b": 1, "a": 2},
			expected: `{"a":2,"b":1}`,
		},
		{
			name:     "nested map inner sort",
			input:    map[string]any{"outer": map[string]any{"y": 2, "x": 1}},
			expected: `{"outer":{"x":1,"y":2}}`,
		},
		{
			name:     "three-level nested map deep sort",
			input:    map[string]any{"a": map[string]any{"b": map[string]any{"d": 4, "c": 3}}},
			expected: `{"a":{"b":{"c":3,"d":4}}}`,
		},
		{
			name: "struct with json tags alphabetised by tag",
			input: struct {
				B int `json:"b"`
				A int `json:"a"`
			}{B: 1, A: 2},
			expected: `{"a":2,"b":1}`,
		},
		{
			name:     "struct without json tags alphabetised by field name",
			input:    struct{ B, A int }{B: 1, A: 2},
			expected: `{"A":2,"B":1}`,
		},
		{
			name: "mixed struct + map field",
			input: struct {
				Z map[string]any
				A int
			}{Z: map[string]any{"q": 9}, A: 1},
			expected: `{"A":1,"Z":{"q":9}}`,
		},
		{
			name:     "embedded RawMessage verbatim",
			input:    map[string]any{"a": RawMessage([]byte("[1,2,3]"))},
			expected: `{"a":[1,2,3]}`,
		},
		{
			name:     "slice with mixed elements order preserved",
			input:    []any{1, "two", true, nil, 3.14},
			expected: `[1,"two",true,null,3.14]`,
		},
		{
			name:     "boolean null integer primitive emission",
			input:    map[string]any{"truthy": true, "falsy": false, "absent": nil, "n": 42},
			expected: `{"absent":null,"falsy":false,"n":42,"truthy":true}`,
		},
		{
			name:     "unicode-edge string UTF-8 correctness",
			input:    map[string]any{"key": "emoji-✓-Latin-1-é"},
			expected: `{"key":"emoji-✓-Latin-1-é"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := CanonicalJSON(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(got, []byte(tc.expected)) {
				t.Errorf("got  %s\nwant %s", got, tc.expected)
			}
		})
	}
}

// TestCanonical_RejectsNaN asserts NaN returns ErrCanonicalUnsupported and no bytes.
func TestCanonical_RejectsNaN(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(math.NaN())
	if !errors.Is(err, ErrCanonicalUnsupported) {
		t.Errorf("expected ErrCanonicalUnsupported, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bytes, got %q", got)
	}
}

// TestCanonical_RejectsInf asserts +Inf and -Inf return ErrCanonicalUnsupported.
func TestCanonical_RejectsInf(t *testing.T) {
	t.Parallel()
	for _, f := range []float64{math.Inf(1), math.Inf(-1)} {
		got, err := CanonicalJSON(f)
		if !errors.Is(err, ErrCanonicalUnsupported) {
			t.Errorf("Inf(%v): expected ErrCanonicalUnsupported, got %v", f, err)
		}
		if got != nil {
			t.Errorf("Inf(%v): expected nil bytes, got %q", f, got)
		}
	}
}

// TestCanonical_StructAndMap exercises both chunk-contract gotcha cases.
func TestCanonical_StructAndMap(t *testing.T) {
	t.Parallel()
	t.Run("struct declaration-order vs alphabetical", func(t *testing.T) {
		t.Parallel()
		type T struct {
			B int
			A int
		}
		got, err := CanonicalJSON(T{B: 1, A: 2})
		if err != nil {
			t.Fatal(err)
		}
		want := `{"A":2,"B":1}`
		if string(got) != want {
			t.Errorf("got %s, want %s (stdlib would emit declaration order)", got, want)
		}
	})
	t.Run("nested map keys sorted at inner depth", func(t *testing.T) {
		t.Parallel()
		input := map[string]any{"outer": map[string]any{"y": 2, "x": 1}}
		got, err := CanonicalJSON(input)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"outer":{"x":1,"y":2}}`
		if string(got) != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
}

// TestCanonical_RejectsFunc, TestCanonical_RejectsChan, etc.
func TestCanonical_RejectsFunc(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(func() {})
	if !errors.Is(err, ErrCanonicalUnsupported) {
		t.Errorf("expected ErrCanonicalUnsupported, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bytes, got %q", got)
	}
}

func TestCanonical_RejectsChan(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(make(chan int))
	if !errors.Is(err, ErrCanonicalUnsupported) {
		t.Errorf("expected ErrCanonicalUnsupported, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bytes, got %q", got)
	}
}

func TestCanonical_RejectsComplex(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(complex(1, 2))
	if !errors.Is(err, ErrCanonicalUnsupported) {
		t.Errorf("expected ErrCanonicalUnsupported, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bytes, got %q", got)
	}
}

func TestCanonical_RejectsNonStringMap(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(map[int]string{1: "a"})
	if !errors.Is(err, ErrCanonicalUnsupported) {
		t.Errorf("expected ErrCanonicalUnsupported, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bytes, got %q", got)
	}
}

// TestCanonical_IgnoresMarshalJSON verifies the encoder does NOT invoke MarshalJSON.
func TestCanonical_IgnoresMarshalJSON(t *testing.T) {
	t.Parallel()
	type withMarshal struct {
		A int
	}
	got, err := CanonicalJSON(withMarshal{A: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"A":1}` {
		t.Errorf("got %s, want {\"A\":1}", got)
	}
}

// TestCanonical_IgnoresMarshalJSON_RealHook verifies with a type that actually
// has a MarshalJSON method — the canonical encoder must ignore it.
type marshalHijack struct {
	Real int
}

func (marshalHijack) MarshalJSON() ([]byte, error) {
	return []byte(`"hijacked"`), nil
}

func TestCanonical_IgnoresMarshalJSONHook(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(marshalHijack{Real: 42})
	if err != nil {
		t.Fatal(err)
	}
	// Must emit {"Real":42}, NOT "hijacked"
	want := `{"Real":42}`
	if string(got) != want {
		t.Errorf("got %s, want %s — encoder must not honor MarshalJSON hook", got, want)
	}
}

// TestCanonical_EmbedsRawMessageVerbatim verifies RawMessage is inserted verbatim.
func TestCanonical_EmbedsRawMessageVerbatim(t *testing.T) {
	t.Parallel()
	t.Run("non-empty RawMessage", testRawMessageNonEmpty)
	t.Run("nil RawMessage emits null", testRawMessageNil)
	t.Run("zero-length RawMessage emits null", testRawMessageZeroLength)
}

func testRawMessageNonEmpty(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(RawMessage([]byte("[1,2,3]")))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "[1,2,3]" {
		t.Errorf("got %s, want [1,2,3]", got)
	}
}

func testRawMessageNil(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(RawMessage(nil))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "null" {
		t.Errorf("got %s, want null", got)
	}
}

func testRawMessageZeroLength(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(RawMessage([]byte{}))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "null" {
		t.Errorf("got %s, want null", got)
	}
}

// TestCanonical_Nil verifies nil input produces "null".
func TestCanonical_Nil(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "null" {
		t.Errorf("got %s, want null", got)
	}
}

// TestCanonical_UintTypes covers the reflect.Uint* switch branch.
func TestCanonical_UintTypes(t *testing.T) {
	t.Parallel()
	type uints struct {
		U  uint
		U8 uint8
		U6 uint16
		U3 uint32
		U4 uint64
	}
	got, err := CanonicalJSON(uints{U: 1, U8: 2, U6: 3, U3: 4, U4: 5})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"U":1,"U3":4,"U4":5,"U6":3,"U8":2}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestCanonical_NilSlice covers the nil-slice → "null" branch.
func TestCanonical_NilSlice(t *testing.T) {
	t.Parallel()
	var s []int
	got, err := CanonicalJSON(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "null" {
		t.Errorf("got %s, want null", got)
	}
}

// TestCanonical_Array covers the reflect.Array case.
func TestCanonical_Array(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON([3]int{10, 20, 30})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "[10,20,30]" {
		t.Errorf("got %s, want [10,20,30]", got)
	}
}

// TestCanonical_NilMap covers the nil-map → "null" branch.
func TestCanonical_NilMap(t *testing.T) {
	t.Parallel()
	var m map[string]any
	got, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "null" {
		t.Errorf("got %s, want null", got)
	}
}

// TestCanonical_ArrayWithInvalidElement covers encodeArray error propagation.
func TestCanonical_ArrayWithInvalidElement(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON([]any{1, make(chan int), 3})
	if !errors.Is(err, ErrCanonicalUnsupported) {
		t.Errorf("expected ErrCanonicalUnsupported, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bytes on error, got %q", got)
	}
}

// TestCanonical_MapWithInvalidValue covers encodeMap value error propagation.
func TestCanonical_MapWithInvalidValue(t *testing.T) {
	t.Parallel()
	got, err := CanonicalJSON(map[string]any{"ok": 1, "bad": make(chan int)})
	if !errors.Is(err, ErrCanonicalUnsupported) {
		t.Errorf("expected ErrCanonicalUnsupported, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bytes on error, got %q", got)
	}
}

// TestCanonical_StructWithInvalidField covers encodeStruct field error propagation.
func TestCanonical_StructWithInvalidField(t *testing.T) {
	t.Parallel()
	type badStruct struct {
		OK  int
		Bad chan int
	}
	got, err := CanonicalJSON(badStruct{OK: 1, Bad: make(chan int)})
	if !errors.Is(err, ErrCanonicalUnsupported) {
		t.Errorf("expected ErrCanonicalUnsupported, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bytes on error, got %q", got)
	}
}

// TestCanonical_StructUnexportedField verifies unexported fields are skipped.
func TestCanonical_StructUnexportedField(t *testing.T) {
	t.Parallel()
	type mixedStruct struct {
		Exported int
		hidden   int //nolint:unused // test fixture: unexported field must be skipped by encoder
	}
	got, err := CanonicalJSON(mixedStruct{Exported: 7})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"Exported":7}` {
		t.Errorf("got %s, want {\"Exported\":7}", got)
	}
}

// TestCanonical_JSONTagWithOptions covers the tag trimming path (json:"name,omitempty").
func TestCanonical_JSONTagWithOptions(t *testing.T) {
	t.Parallel()
	type tagged struct {
		Field int `json:"field,omitempty"`
	}
	got, err := CanonicalJSON(tagged{Field: 5})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"field":5}` {
		t.Errorf("got %s, want {\"field\":5}", got)
	}
}

// TestCanonical_JSONTagOptionsOnly covers json:",omitempty" (tag becomes "").
func TestCanonical_JSONTagOptionsOnly(t *testing.T) {
	t.Parallel()
	type optOnly struct {
		Name int `json:",omitempty"`
	}
	got, err := CanonicalJSON(optOnly{Name: 3})
	if err != nil {
		t.Fatal(err)
	}
	// tag is empty after trim → falls back to field name "Name"
	if string(got) != `{"Name":3}` {
		t.Errorf("got %s, want {\"Name\":3}", got)
	}
}

// TestCanonical_JSONTagDash covers json:"-" — the canonical encoder does NOT exclude
// the field; it falls back to the Go field name (options-ignored policy).
func TestCanonical_JSONTagDash(t *testing.T) {
	t.Parallel()
	type dashStruct struct {
		Visible int
		Hidden  int `json:"-"`
	}
	got, err := CanonicalJSON(dashStruct{Visible: 1, Hidden: 2})
	if err != nil {
		t.Fatal(err)
	}
	// "-" tag → resolveFieldName returns Go field name "Hidden"; field IS included.
	if string(got) != `{"Hidden":2,"Visible":1}` {
		t.Errorf("got %s, want {\"Hidden\":2,\"Visible\":1}", got)
	}
}

// TestCanonical_Determinism verifies two calls on the same value produce identical bytes.
func TestCanonical_Determinism(t *testing.T) {
	t.Parallel()
	input := map[string]any{"z": 3, "a": 1, "m": map[string]any{"d": 4, "b": 2}}
	a, errA := CanonicalJSON(input)
	b, errB := CanonicalJSON(input)
	if errA != nil || errB != nil {
		t.Fatalf("errors: %v / %v", errA, errB)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("not deterministic: %s vs %s", a, b)
	}
}
