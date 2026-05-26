package supervise

import (
	"bytes"
	"testing"
)

// TestZeroBytes covers V5: the small helper used in doClaimRequest and
// refill.fetchOne to scrub a response body after parse/decrypt. The
// helper does NOT close the residual-risk #12 unzeroable-string gap on
// json.Unmarshal'd fields — it just drops one of the two unscrubbed
// heap copies. Verifying the helper itself is enough; the deferred call
// site in doClaimRequest is one line and visually auditable.
func TestZeroBytes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"nil", nil, nil},
		{"empty", []byte{}, []byte{}},
		{"single_byte", []byte{0xff}, []byte{0x00}},
		{"multi_byte", []byte("eyJhbGciOiJFUzI1NksifQ.placeholder.sig"), make([]byte, len("eyJhbGciOiJFUzI1NksifQ.placeholder.sig"))},
		{"already_zero", make([]byte, 16), make([]byte, 16)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			zeroBytes(tc.in)
			if !bytes.Equal(tc.in, tc.want) {
				t.Errorf("after zeroBytes: got %x, want %x", tc.in, tc.want)
			}
		})
	}
}

// TestZeroBytes_NoPanicOnNil documents that zeroBytes must be safe to
// call on a nil slice — the deferred zeroBytes in doClaimRequest fires
// even on early-return paths where io.ReadAll never populated the slice.
func TestZeroBytes_NoPanicOnNil(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("zeroBytes(nil) panicked: %v", r)
		}
	}()
	zeroBytes(nil)
}
