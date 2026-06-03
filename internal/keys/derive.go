package keys

import (
	"context"
	"errors"

	"golang.org/x/crypto/argon2"
)

const (
	argon2Time    = 4
	argon2MemoryK = 256 * 1024
	argon2Threads = 4
	argon2KeyLen  = 64

	minPassphraseLen = 12
	saltLen          = 16
)

// ErrPassphraseTooShort is returned when the passphrase is fewer than 12 bytes.
var ErrPassphraseTooShort = errors.New("hush/keys: passphrase too short")

// ErrSaltMissing is returned when the salt is not exactly 16 bytes.
var ErrSaltMissing = errors.New("hush/keys: salt missing or wrong length")

// DeriveMasterSeed derives the 64-byte hush master seed from a passphrase and a
// 16-byte salt using Argon2id with locked parameters (time=4, memory=256 MiB,
// threads=4, keyLen=64).
//
// ctx is inspected once at entry; a non-nil ctx.Err() returns immediately without
// invoking Argon2id. Cancellation arriving after entry does not abort the derivation.
func DeriveMasterSeed(ctx context.Context, passphrase, salt []byte) ([]byte, error) {
	return deriveMasterSeed(ctx, passphrase, salt, argon2Time, argon2MemoryK, argon2Threads)
}

// deriveMasterSeed is the parameterised core shared by DeriveMasterSeed and the
// fuzz harness. Production callers go through DeriveMasterSeed, which pins the
// locked Argon2id cost (time=4, memory=256 MiB, threads=4). The fuzz target
// supplies deliberately cheap parameters so it can hammer the input-validation
// and determinism logic at thousands of execs/sec — fuzzing the full-strength
// KDF oversubscribes CI workers and trips Go's fuzz-coordinator deadline. The
// locked parameters themselves are pinned by the KAT in derive_test.go.
func deriveMasterSeed(ctx context.Context, passphrase, salt []byte, time, memoryK uint32, threads uint8) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(passphrase) < minPassphraseLen {
		return nil, ErrPassphraseTooShort
	}
	if len(salt) != saltLen {
		return nil, ErrSaltMissing
	}
	return argon2.IDKey(passphrase, salt, time, memoryK, threads, argon2KeyLen), nil
}
