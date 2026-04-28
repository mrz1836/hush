package ecies

import (
	"context"
	"errors"
	"testing"
)

// FuzzECIESDecrypt is Constitution VIII fuzz target #3: ECIES decrypt input
// handling. Asserts no panic and every error is one of the four typed values
// (ErrECIESDecryptFailed, ErrECIESEnvelopeTooShort, context.Canceled,
// context.DeadlineExceeded). Seed corpus is loaded from
// testdata/fuzz/FuzzECIESDecrypt/.
func FuzzECIESDecrypt(f *testing.F) {
	priv := generateFuzzKey(f)

	// Explicit Add for the smallest seeds; the corpus directory is loaded
	// automatically by the Go fuzz harness.
	f.Add([]byte(""))
	f.Add([]byte{0x00})

	f.Fuzz(func(t *testing.T, envelope []byte) {
		sb, err := Decrypt(t.Context(), priv, envelope)
		if err == nil {
			if sb != nil {
				_ = sb.Destroy()
			}
			return
		}
		if !errors.Is(err, ErrECIESDecryptFailed) &&
			!errors.Is(err, ErrECIESEnvelopeTooShort) &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Decrypt returned non-sentinel error type %T: %v", err, err)
		}
	})
}
