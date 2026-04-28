package ecies

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// roundTrip is the shared shape used by RoundTrip_1B / 1KB / 1MB.
func roundTrip(t *testing.T, plaintext []byte) {
	t.Helper()
	priv := generateFreshKey(t)

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(envelope), minEnvelopeSize)

	sb, err := Decrypt(t.Context(), priv, envelope)
	require.NoError(t, err)
	require.NotNil(t, sb)
	t.Cleanup(func() { _ = sb.Destroy() })

	require.NoError(t, sb.Use(func(b []byte) {
		require.True(t, bytes.Equal(plaintext, b))
	}))
}

func TestECIES_RoundTrip_1B(t *testing.T) {
	roundTrip(t, []byte{0x42})
}

func TestECIES_RoundTrip_1KB(t *testing.T) {
	plaintext := make([]byte, 1024)
	_, err := rand.Read(plaintext)
	require.NoError(t, err)
	roundTrip(t, plaintext)
}

func TestECIES_RoundTrip_1MB(t *testing.T) {
	plaintext := make([]byte, 1<<20)
	_, err := rand.Read(plaintext)
	require.NoError(t, err)
	roundTrip(t, plaintext)
}

func TestECIES_EncryptIsRandomised(t *testing.T) {
	priv := generateFreshKey(t)
	plaintext := []byte("hush-randomisation-marker")

	a, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
	require.NoError(t, err)
	b, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
	require.NoError(t, err)
	require.False(t, bytes.Equal(a, b), "two Encrypt calls on the same input must produce different envelopes")
}

func TestECIES_EnvelopeMeetsMinSize(t *testing.T) {
	priv := generateFreshKey(t)
	for _, size := range []int{1, 2, 15, 16, 17, 64, 1024} {
		plaintext := make([]byte, size)
		_, err := rand.Read(plaintext)
		require.NoError(t, err)
		envelope, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(envelope), minEnvelopeSize)
	}
}

func TestECIES_NoPlaintextSubstringInEnvelope(t *testing.T) {
	priv := generateFreshKey(t)
	plaintext := []byte("PLAINTEXT_MARKER_IN_ENVELOPE_TEST")

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
	require.NoError(t, err)
	require.False(t, bytes.Contains(envelope, plaintext),
		"envelope must not contain the plaintext as a substring")
}

func TestECIES_DecryptWrongKey_Fails(t *testing.T) {
	keyA := generateFreshKey(t)
	keyB := generateFreshKey(t)
	plaintext := []byte("wrong-key-test")

	envelope, err := Encrypt(t.Context(), &keyA.PublicKey, plaintext)
	require.NoError(t, err)

	sb, err := Decrypt(t.Context(), keyB, envelope)
	require.ErrorIs(t, err, ErrECIESDecryptFailed)
	require.Nil(t, sb)
}

func TestECIES_DecryptMangledEnvelope_Fails(t *testing.T) {
	priv := generateFreshKey(t)
	plaintext := make([]byte, 64)
	_, err := rand.Read(plaintext)
	require.NoError(t, err)

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
	require.NoError(t, err)

	cases := []struct {
		name     string
		position int
	}{
		{"flip-byte-in-magic", 0},
		{"flip-byte-in-pubkey", 10},
		{"flip-byte-in-ciphertext", 40},
		{"flip-byte-in-mac", len(envelope) - 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mangled := mangleByte(envelope, tc.position)
			sb, err := Decrypt(t.Context(), priv, mangled)
			require.ErrorIs(t, err, ErrECIESDecryptFailed)
			require.Nil(t, sb)
		})
	}
}

func TestECIES_DecryptTruncatedEnvelope_Fails(t *testing.T) {
	priv := generateFreshKey(t)
	plaintext := make([]byte, 100)
	_, err := rand.Read(plaintext)
	require.NoError(t, err)

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
	require.NoError(t, err)
	require.Greater(t, len(envelope), minEnvelopeSize)

	truncated := truncateEnvelope(envelope, len(envelope)-1)
	sb, err := Decrypt(t.Context(), priv, truncated)
	require.ErrorIs(t, err, ErrECIESDecryptFailed)
	require.Nil(t, sb)
}

func TestECIES_DecryptAppendedByte_Fails(t *testing.T) {
	priv := generateFreshKey(t)
	plaintext := []byte("appended-byte-test")

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
	require.NoError(t, err)

	appended := appendByte(envelope)
	sb, err := Decrypt(t.Context(), priv, appended)
	require.ErrorIs(t, err, ErrECIESDecryptFailed)
	require.Nil(t, sb)
}

func TestECIES_DecryptEmptyEnvelope_TooShort(t *testing.T) {
	priv := generateFreshKey(t)
	cases := []int{0, 1, 84}
	for _, length := range cases {
		t.Run("length=...", func(t *testing.T) {
			synthetic := make([]byte, length)
			sb, err := Decrypt(t.Context(), priv, synthetic)
			require.ErrorIs(t, err, ErrECIESEnvelopeTooShort)
			require.NotErrorIs(t, err, ErrECIESDecryptFailed,
				"too-short rejection must be sentinel-distinct from decrypt-failed")
			require.Nil(t, sb)
		})
	}
}

func TestECIES_DecryptReturnsSecureBytes(t *testing.T) {
	priv := generateFreshKey(t)
	plaintext := []byte("ownership-transfer-test")

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
	require.NoError(t, err)

	sb, err := Decrypt(t.Context(), priv, envelope)
	require.NoError(t, err)
	require.NotNil(t, sb)

	require.NoError(t, sb.Use(func(b []byte) {
		require.True(t, bytes.Equal(plaintext, b))
	}))

	require.NoError(t, sb.Destroy())

	useAfterDestroy := sb.Use(func(_ []byte) {})
	require.ErrorIs(t, useAfterDestroy, securebytes.ErrDestroyed)
}

func TestECIES_EncryptZeroesInternalBuffersOnSuccess(t *testing.T) {
	priv := generateFreshKey(t)
	original := []byte("encrypt-zero-success-test")
	originalCopy := bytes.Clone(original)

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, original)
	require.NoError(t, err)
	require.NotEmpty(t, envelope)

	require.True(t, bytes.Equal(original, originalCopy),
		"caller's plaintext slice must NOT be mutated by Encrypt")
}

func TestECIES_EncryptZeroesInternalBuffersOnError(t *testing.T) {
	priv := generateFreshKey(t)
	original := []byte("encrypt-zero-error-test")
	originalCopy := bytes.Clone(original)

	// Empty plaintext path.
	envelope, err := Encrypt(t.Context(), &priv.PublicKey, nil)
	require.ErrorIs(t, err, ErrECIESEmptyPlaintext)
	require.Nil(t, envelope)

	// Nil pubkey path.
	envelope, err = Encrypt(t.Context(), nil, original)
	require.ErrorIs(t, err, ErrECIESInvalidRecipientKey)
	require.Nil(t, envelope)
	require.True(t, bytes.Equal(original, originalCopy))

	// Wrong-curve pubkey path.
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	envelope, err = Encrypt(t.Context(), &p256.PublicKey, original)
	require.ErrorIs(t, err, ErrECIESInvalidRecipientKey)
	require.Nil(t, envelope)
	require.True(t, bytes.Equal(original, originalCopy))
}

func TestECIES_EncryptDoesNotMutateCallerSlice(t *testing.T) {
	priv := generateFreshKey(t)
	original := []byte("caller-slice-immutability-test")
	originalCopy := bytes.Clone(original)

	_, err := Encrypt(t.Context(), &priv.PublicKey, original)
	require.NoError(t, err)
	require.True(t, bytes.Equal(originalCopy, original))
}

func TestECIES_EncryptRejectsEmpty(t *testing.T) {
	priv := generateFreshKey(t)
	envelope, err := Encrypt(t.Context(), &priv.PublicKey, []byte{})
	require.ErrorIs(t, err, ErrECIESEmptyPlaintext)
	require.NotErrorIs(t, err, ErrECIESInvalidRecipientKey)
	require.NotErrorIs(t, err, ErrECIESDecryptFailed)
	require.Nil(t, envelope)
}

func TestECIES_EncryptRejectsNilPub(t *testing.T) {
	envelope, err := Encrypt(t.Context(), nil, []byte("plain"))
	require.ErrorIs(t, err, ErrECIESInvalidRecipientKey)
	require.Nil(t, envelope)
}

func TestECIES_EncryptRejectsWrongCurvePub(t *testing.T) {
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	envelope, err := Encrypt(t.Context(), &p256.PublicKey, []byte("plain"))
	require.ErrorIs(t, err, ErrECIESInvalidRecipientKey)
	require.NotErrorIs(t, err, ErrECIESDecryptFailed)
	require.Nil(t, envelope)
}

func TestECIES_EncryptRespectsCancelledContext(t *testing.T) {
	priv := generateFreshKey(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	envelope, err := Encrypt(ctx, &priv.PublicKey, []byte("ctx-test"))
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, envelope)
}

func TestECIES_DecryptRespectsCancelledContext(t *testing.T) {
	priv := generateFreshKey(t)
	envelope, err := Encrypt(t.Context(), &priv.PublicKey, []byte("ctx-test"))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	sb, err := Decrypt(ctx, priv, envelope)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, sb)
}

func TestECIES_DecryptRespectsDeadlineContext(t *testing.T) {
	priv := generateFreshKey(t)
	envelope, err := Encrypt(t.Context(), &priv.PublicKey, []byte("deadline-test"))
	require.NoError(t, err)

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	sb, err := Decrypt(ctx, priv, envelope)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, sb)
}

func TestECIES_NoLeakOnError(t *testing.T) {
	sentinel := testutil.SentinelSecret(9)
	priv := generateFreshKey(t)
	plaintext := []byte("prefix-" + sentinel + "-suffix")

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
	require.NoError(t, err)

	cases := []struct {
		name   string
		mangle func([]byte) []byte
	}{
		{"flip-byte-in-magic", func(e []byte) []byte { return mangleByte(e, 0) }},
		{"flip-byte-in-pubkey", func(e []byte) []byte { return mangleByte(e, 10) }},
		{"flip-byte-in-ciphertext", func(e []byte) []byte { return mangleByte(e, 40) }},
		{"flip-byte-in-mac", func(e []byte) []byte { return mangleByte(e, len(e)-1) }},
		{"truncate-to-min-minus-1", func(e []byte) []byte { return truncateEnvelope(e, minEnvelopeSize-1) }},
		{"truncate-to-zero", func(_ []byte) []byte { return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mangled := tc.mangle(envelope)
			sb, err := Decrypt(t.Context(), priv, mangled)
			require.Error(t, err)
			require.Nil(t, sb)

			testutil.AssertSentinelAbsent(t, sentinel, err.Error())
			for cur := err; cur != nil; cur = errors.Unwrap(cur) {
				testutil.AssertSentinelAbsent(t, sentinel, cur.Error())
			}
		})
	}
}

//nolint:gocognit // goroutine fan-out + assert chain: complexity is the race-detector test pattern
func TestECIES_ConcurrentRoundTrip(t *testing.T) {
	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			priv := generateFreshKey(t)
			plaintext := make([]byte, 256)
			if _, err := rand.Read(plaintext); !assert.NoError(t, err) {
				return
			}

			envelope, err := Encrypt(t.Context(), &priv.PublicKey, plaintext)
			if !assert.NoError(t, err) {
				return
			}

			sb, err := Decrypt(t.Context(), priv, envelope)
			if !assert.NoError(t, err) || !assert.NotNil(t, sb) {
				return
			}
			defer func() { _ = sb.Destroy() }()

			assert.NoError(t, sb.Use(func(b []byte) {
				assert.True(t, bytes.Equal(plaintext, b))
			}))
		}()
	}
	wg.Wait()
}
