package keys

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// FuzzDeriveMaster covers G10 + AC-9 + SC-002.
// Run with: go test -fuzz=FuzzDeriveMaster -fuzztime=60s ./internal/keys/
func FuzzDeriveMaster(f *testing.F) {
	f.Add([]byte("correct-horse-battery"), []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	f.Add([]byte("short"), []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	f.Add([]byte("correct-horse-battery"), []byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{}, []byte{})

	f.Fuzz(func(t *testing.T, passphrase, salt []byte) {
		fuzzAssertDeriveMaster(t, passphrase, salt)
	})
}

// fuzzAssertDeriveMaster runs the fuzz assertion body; extracted to reduce
// cognitive complexity of FuzzDeriveMaster below the project threshold.
func fuzzAssertDeriveMaster(t *testing.T, passphrase, salt []byte) {
	t.Helper()

	ctx := context.Background()
	seed1, err := DeriveMasterSeed(ctx, passphrase, salt)
	if err != nil {
		knownErr := errors.Is(err, ErrPassphraseTooShort) ||
			errors.Is(err, ErrSaltMissing) ||
			errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded)
		if !knownErr {
			t.Errorf("unexpected error type: %v", err)
		}
		return
	}

	if len(seed1) != argon2KeyLen {
		t.Errorf("seed length %d != %d", len(seed1), argon2KeyLen)
		return
	}

	seed2, err := DeriveMasterSeed(ctx, passphrase, salt)
	if err != nil {
		t.Errorf("re-derivation failed: %v", err)
		return
	}

	if !bytes.Equal(seed1, seed2) {
		t.Error("non-deterministic derivation: same inputs produced different seeds")
	}
}
