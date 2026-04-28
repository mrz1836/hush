package sign

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"testing"
)

// FuzzVerifyRequest is Constitution VIII fuzz target #4: request signature
// payload parsing. Asserts no panic and every error is a typed sentinel.
func FuzzVerifyRequest(f *testing.F) {
	pub := generateFuzzKey(f).Public().(*ecdsa.PublicKey) //nolint:forcetypeassert // ecdsa.GenerateKey always returns *ecdsa.PublicKey

	// Seed corpus: all four files in testdata/fuzz/FuzzVerifyRequest/ are
	// loaded automatically by the Go fuzz harness. Explicit Add entries here
	// for clarity.
	f.Add([]byte(""), []byte(""))
	f.Add([]byte("replay-defense-test"), []byte("badsig"))
	f.Add([]byte("x"), []byte{0x30, 0x44, 0x02, 0x20, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08})
	f.Add([]byte{0xff, 0xfe, 0xfd}, []byte{0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, payload, sig []byte) {
		err := Verify(context.Background(), pub, payload, sig)
		if err == nil {
			return
		}
		if !errors.Is(err, ErrSignatureInvalid) && !errors.Is(err, context.Canceled) {
			t.Errorf("Verify returned non-sentinel error type %T: %v", err, err)
		}
	})
}
