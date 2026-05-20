package keys

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// katPassphrase is the fixed passphrase used for all known-answer vectors.
const katPassphrase = "correct-horse-battery"

// katSeedHex is the expected Argon2id output for katPassphrase + testKATSalt().
// Frozen by the first successful test run as a cross-architecture regression guard.
const katSeedHex = "c6733b5d2ede0b8bef9466786ee55a14c33db735bcd9e73e70d0ecf32e5d6cddabe64423b9535a16a814b0eb5a3174b22f78206c1388f5c91297677639207c91"

// testKATSalt returns the fixed 16-byte salt used for all known-answer vectors.
func testKATSalt() []byte {
	return []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
}

func TestDeriveMasterSeed_Deterministic(t *testing.T) {
	ctx := context.Background()

	t.Run("same inputs produce byte-identical seeds", func(t *testing.T) {
		pass := []byte("my-long-passphrase-here!")
		salt := make([]byte, 16)
		for i := range salt {
			salt[i] = byte(i + 10)
		}
		seed1, err := DeriveMasterSeed(ctx, pass, salt)
		require.NoError(t, err)
		seed2, err := DeriveMasterSeed(ctx, pass, salt)
		require.NoError(t, err)
		assert.Equal(t, seed1, seed2, "two derivations must be byte-identical")
		assert.Len(t, seed1, 64)
	})

	t.Run("different passphrases produce different seeds", func(t *testing.T) {
		salt := make([]byte, 16)
		s1, err := DeriveMasterSeed(ctx, []byte("passphrase-one-here"), salt)
		require.NoError(t, err)
		s2, err := DeriveMasterSeed(ctx, []byte("passphrase-two-here"), salt)
		require.NoError(t, err)
		assert.NotEqual(t, s1, s2)
	})

	t.Run("KAT: fixed inputs pin fixed output", func(t *testing.T) {
		got, err := DeriveMasterSeed(ctx, []byte(katPassphrase), testKATSalt())
		require.NoError(t, err)
		require.Len(t, got, 64)
		gotHex := hex.EncodeToString(got)
		t.Logf("KAT seed hex: %s", gotHex)
		assert.Equal(t, katSeedHex, gotHex, "KAT: output must be frozen across architectures")
	})

	t.Run("happy path context: background ctx does not abort", func(t *testing.T) {
		seed, err := DeriveMasterSeed(context.Background(), []byte(katPassphrase), testKATSalt())
		require.NoError(t, err)
		assert.Len(t, seed, 64)
	})
}

func TestDeriveMasterSeed_RejectsShortPassphrase(t *testing.T) {
	validSalt := make([]byte, 16)
	ctx := context.Background()

	cases := []struct {
		name       string
		passphrase []byte
		wantErr    bool
	}{
		{name: "len=0", passphrase: []byte{}, wantErr: true},
		{name: "len=1", passphrase: []byte("a"), wantErr: true},
		{name: "len=11", passphrase: []byte("11characters")[:11], wantErr: true},
		{name: "len=12 boundary passes", passphrase: []byte("12_chars_ok!"), wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			seed, err := DeriveMasterSeed(ctx, tc.passphrase, validSalt)
			elapsed := time.Since(start)

			if tc.wantErr {
				require.ErrorIs(t, err, ErrPassphraseTooShort)
				assert.Nil(t, seed)
				assert.Less(t, elapsed, 100*time.Millisecond, "validation must not invoke Argon2id")
			} else {
				require.NoError(t, err)
				assert.Len(t, seed, 64)
			}
		})
	}
}

func TestDeriveMasterSeed_RejectsBadSalt(t *testing.T) {
	validPass := []byte("valid-passphrase!!")
	ctx := context.Background()

	cases := []struct {
		name    string
		salt    []byte
		wantErr bool
	}{
		{name: "len=0", salt: []byte{}, wantErr: true},
		{name: "len=8", salt: make([]byte, 8), wantErr: true},
		{name: "len=15", salt: make([]byte, 15), wantErr: true},
		{name: "len=16 boundary passes", salt: make([]byte, 16), wantErr: false},
		{name: "len=17", salt: make([]byte, 17), wantErr: true},
		{name: "len=24", salt: make([]byte, 24), wantErr: true},
		{name: "len=32", salt: make([]byte, 32), wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			seed, err := DeriveMasterSeed(ctx, validPass, tc.salt)
			elapsed := time.Since(start)

			if tc.wantErr {
				require.ErrorIs(t, err, ErrSaltMissing)
				assert.Nil(t, seed)
				assert.Less(t, elapsed, 100*time.Millisecond, "validation must not invoke Argon2id")
			} else {
				require.NoError(t, err)
				assert.Len(t, seed, 64)
			}
		})
	}
}

func TestDeriveMasterSeed_RespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before any call

	start := time.Now()
	seed, err := DeriveMasterSeed(ctx, []byte(katPassphrase), testKATSalt())
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.Canceled, "pre-canceled ctx must surface context.Canceled")
	assert.Nil(t, seed)
	assert.Less(t, elapsed, 100*time.Millisecond, "must return before Argon2id")
}
