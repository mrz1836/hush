package token

import (
	"net/netip"
	"slices"
	"testing"
)

func TestNewClientIP_Canonicalizes(t *testing.T) {
	cases := []struct {
		name string
		in   string // input string fed through netip.ParseAddr
		want string // expected canonical form
	}{
		{"ipv4", "100.64.0.1", "100.64.0.1"},
		{"ipv6 loopback short", "::1", "::1"},
		{"ipv6 loopback long collapses", "0000:0000:0000:0000:0000:0000:0000:0001", "::1"},
		{"ipv6 documentation prefix", "2001:0db8:0000:0000:0000:0000:0000:0001", "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, err := netip.ParseAddr(tc.in)
			if err != nil {
				t.Fatalf("netip.ParseAddr: %v", err)
			}
			got := NewClientIP(addr)
			if string(got) != tc.want {
				t.Errorf("NewClientIP(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewClientIP_ZeroAddrYieldsEmpty(t *testing.T) {
	var zero netip.Addr
	if got := NewClientIP(zero); got != "" {
		t.Errorf("NewClientIP(zero) = %q, want empty", got)
	}
}

//nolint:gocognit // table-driven test with happy + reject branches; complexity is structural
func TestParseClientIP(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    ClientIP
		wantErr bool
	}{
		{"ipv4 happy", "100.64.0.1", "100.64.0.1", false},
		{"ipv6 long canonicalizes", "0000:0000:0000:0000:0000:0000:0000:0001", "::1", false},
		{"empty rejected", "", "", true},
		{"garbage rejected", "not-an-ip", "", true},
		{"trailing garbage rejected", "1.2.3.4-extra", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseClientIP(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseClientIP(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseClientIP(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseClientIP(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewClientIP_FormDiffersFromInputButCompareEqual(t *testing.T) {
	// The point of the named type: equivalent IPv6 forms collapse to the
	// same canonical string and thus compare equal via ==.
	a, _ := ParseClientIP("::1")
	b, _ := ParseClientIP("0000:0000:0000:0000:0000:0000:0000:0001")
	if a != b {
		t.Errorf("ClientIP(::1) %q != ClientIP(long) %q — canonicalization broken", a, b)
	}
}

func TestNewScope(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want Scope
	}{
		{"nil yields nil", nil, nil},
		{"empty yields nil", []string{}, nil},
		{"single", []string{"A"}, Scope{"A"}},
		{"already sorted", []string{"A", "B", "C"}, Scope{"A", "B", "C"}},
		{"unsorted gets sorted", []string{"C", "A", "B"}, Scope{"A", "B", "C"}},
		{"duplicates removed", []string{"A", "B", "A", "C", "B"}, Scope{"A", "B", "C"}},
		{"empty strings dropped", []string{"A", "", "B", ""}, Scope{"A", "B"}},
		{"only empties yields nil", []string{"", ""}, nil},
		{"mixed case preserved (case-sensitive)", []string{"a", "A"}, Scope{"A", "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewScope(tc.in)
			if !slices.Equal([]string(got), []string(tc.want)) {
				t.Errorf("NewScope(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewScope_DoesNotMutateInput(t *testing.T) {
	in := []string{"C", "A", "B", "A"}
	orig := append([]string(nil), in...)
	_ = NewScope(in)
	if !slices.Equal(in, orig) {
		t.Errorf("NewScope mutated input: before=%v after=%v", orig, in)
	}
}

//nolint:gocognit // fuzz harness with multiple property assertions; complexity is structural
func FuzzNewScope_Idempotent(f *testing.F) {
	// Seed corpus covers the documented edge cases plus a few stress shapes.
	f.Add("A,B,C")
	f.Add("C,B,A")
	f.Add("A,A,A")
	f.Add(",,")
	f.Add("")
	f.Add("OPENAI_API_KEY,GITHUB_TOKEN,ANTHROPIC_API_KEY")

	f.Fuzz(func(t *testing.T, raw string) {
		// Cap input size to keep fuzz bounded.
		const maxRaw = 4096
		if len(raw) > maxRaw {
			return
		}
		in := splitCommas(raw)
		first := NewScope(in)
		second := NewScope([]string(first))
		if !slices.Equal([]string(first), []string(second)) {
			t.Fatalf("NewScope not idempotent: first=%v second=%v", first, second)
		}
		// Property: output is sorted.
		if !slices.IsSorted([]string(first)) {
			t.Fatalf("NewScope output not sorted: %v", first)
		}
		// Property: no duplicates.
		for i := 1; i < len(first); i++ {
			if first[i] == first[i-1] {
				t.Fatalf("NewScope output contains duplicate at index %d: %v", i, first)
			}
		}
		// Property: no empties.
		for i, s := range first {
			if s == "" {
				t.Fatalf("NewScope output contains empty string at index %d: %v", i, first)
			}
		}
	})
}

// splitCommas is a tiny helper for the fuzz seed: comma-delimited inputs
// are easier for the fuzz harness to mutate than []string. Trailing/leading
// commas yield empty elements (intentional — those exercise the empty-drop
// path inside NewScope).
func splitCommas(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := range len(s) {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
