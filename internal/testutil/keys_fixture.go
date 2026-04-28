package testutil

import (
	"context"
	"sync"
	"testing"

	"github.com/mrz1836/hush/internal/keys"
)

// testPassphrase is the hardcoded test passphrase — 32 bytes, satisfies SDD-01 minPassphraseLen.
// NEVER use outside of tests.
var testPassphrase = []byte("hush-test-seed-NEVER-USE-IN-PROD") //nolint:gochecknoglobals // immutable test constant

// testSalt is a fixed 16-byte salt for deterministic key derivation in tests.
// NEVER use outside of tests.
var testSalt = []byte{ //nolint:gochecknoglobals // immutable test constant
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
	0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10,
}

// seedOnce guards the single initialisation of cachedSeed.
//
// observable only through NewTestKeys. Argon2id costs ~1.5 s per call; memoisation avoids
// multiplying that cost across the full test suite.
//
//nolint:gochecknoglobals // sync.Once + deterministic test seed: set once, never mutated,
var (
	seedOnce   sync.Once
	cachedSeed []byte
)

// NewTestKeys returns a deterministic 64-byte master seed derived from the hardcoded test
// passphrase and salt. Two calls in any process return byte-identical slices.
// The returned slice is a defensive copy — mutating it cannot poison subsequent calls.
func NewTestKeys(t *testing.T) (masterSeed []byte) {
	t.Helper()
	seedOnce.Do(func() {
		var err error
		cachedSeed, err = keys.DeriveMasterSeed(context.Background(), testPassphrase, testSalt)
		if err != nil {
			t.Errorf("hush/testutil: NewTestKeys: DeriveMasterSeed: %v", err)
		}
	})
	out := make([]byte, len(cachedSeed))
	copy(out, cachedSeed)
	return out
}
