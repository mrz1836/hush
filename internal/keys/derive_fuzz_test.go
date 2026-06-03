package keys

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// FuzzDeriveMaster fuzzes master-seed derivation.
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

// Cheap Argon2id parameters for fuzzing. The fuzz target validates the
// input-handling logic, the 64-byte output length, and the determinism
// property — none of which depend on the KDF cost. Production parameters
// (time=4, memory=256 MiB, threads=4) are deliberately slow, and running
// them on every fuzz exec oversubscribes CI workers until a worker misses
// Go's fuzz-coordinator deadline ("context deadline exceeded"). The locked
// production parameters are pinned separately by the KAT in derive_test.go.
const (
	fuzzArgon2Time    = 1
	fuzzArgon2MemoryK = 8
	fuzzArgon2Threads = 1
)

// fuzzAssertDeriveMaster runs the fuzz assertion body; extracted to reduce
// cognitive complexity of FuzzDeriveMaster below the project threshold.
func fuzzAssertDeriveMaster(t *testing.T, passphrase, salt []byte) {
	t.Helper()

	ctx := context.Background()
	seed1, err := deriveMasterSeed(ctx, passphrase, salt, fuzzArgon2Time, fuzzArgon2MemoryK, fuzzArgon2Threads)
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

	seed2, err := deriveMasterSeed(ctx, passphrase, salt, fuzzArgon2Time, fuzzArgon2MemoryK, fuzzArgon2Threads)
	if err != nil {
		t.Errorf("re-derivation failed: %v", err)
		return
	}

	if !bytes.Equal(seed1, seed2) {
		t.Error("non-deterministic derivation: same inputs produced different seeds")
	}
}
