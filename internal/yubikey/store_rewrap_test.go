package yubikey_test

import (
	"bytes"
	"context"
	"testing"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/mrz1836/go-tumbler/securebytes"
	"github.com/mrz1836/go-tumbler/transport"
	"github.com/mrz1836/hush/internal/yubikey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func constFn(v string) func() ([]byte, error) {
	return func() ([]byte, error) { return []byte(v), nil }
}

func TestRewrapPassphrase_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newStore()
	oldPass, newPass := []byte("old-passphrase-value"), []byte("new-passphrase-value")

	res, err := store.Enroll(ctx, tumbler.PolicyPasswordAndYubiKey,
		yubikey.EnrollOptions{Passphrase: oldPass, WithRecovery: true})
	require.NoError(t, err)

	before, err := tumbler.ParseEnvelope(res.Envelope)
	require.NoError(t, err)

	newEnv, err := store.RewrapPassphrase(ctx, res.Envelope, constFn("old-passphrase-value"), newPass)
	require.NoError(t, err)

	// Policy and slot count are preserved (add-before-remove keeps recovery).
	after, err := tumbler.ParseEnvelope(newEnv)
	require.NoError(t, err)
	assert.Equal(t, tumbler.PolicyPasswordAndYubiKey, after.EffectivePolicy())
	assert.Equal(t, before.SlotCount(), after.SlotCount())

	// The NEW passphrase recovers the SAME master seed — the DEK never rotated.
	got, err := store.Unlock(ctx, newEnv, constFn("new-passphrase-value"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(res.MasterSeed, got), "seed must be unchanged")

	// The OLD passphrase no longer opens the re-wrapped envelope.
	_, err = store.Unlock(ctx, newEnv, constFn("old-passphrase-value"))
	assert.ErrorIs(t, err, tumbler.ErrAuthFailed)
}

func TestRewrapPassphrase_RecoveryStillOpens(t *testing.T) {
	ctx := context.Background()
	store := newStore()

	res, err := store.Enroll(ctx, tumbler.PolicyPasswordAndYubiKey,
		yubikey.EnrollOptions{Passphrase: []byte("first-pass"), WithRecovery: true})
	require.NoError(t, err)
	require.NotEmpty(t, res.RecoveryCode)

	newEnv, err := store.RewrapPassphrase(ctx, res.Envelope, constFn("first-pass"), []byte("second-pass"))
	require.NoError(t, err)

	// The untouched recovery slot still recovers the same seed.
	seed, err := store.UnlockWithRecovery(ctx, newEnv, res.RecoveryCode)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(res.MasterSeed, seed))
}

func TestRewrapPassphrase_YubiKeyOnly_NoPassphraseFactor(t *testing.T) {
	ctx := context.Background()
	store := newStore()

	res, err := store.Enroll(ctx, tumbler.PolicyYubiKeyOnly, yubikey.EnrollOptions{})
	require.NoError(t, err)

	_, err = store.RewrapPassphrase(ctx, res.Envelope, nil, []byte("new-pass"))
	assert.ErrorIs(t, err, yubikey.ErrNoPassphraseFactor)
}

func TestRewrapPassphrase_WrongOldPassphrase_AuthFailed(t *testing.T) {
	ctx := context.Background()
	store := newStore()

	res, err := store.Enroll(ctx, tumbler.PolicyPasswordAndYubiKey,
		yubikey.EnrollOptions{Passphrase: []byte("the-right-one")})
	require.NoError(t, err)

	_, err = store.RewrapPassphrase(ctx, res.Envelope, constFn("the-wrong-one"), []byte("new-pass"))
	assert.ErrorIs(t, err, tumbler.ErrAuthFailed)
}

func TestRewrapPassphrase_MultiPrimary_Unsupported(t *testing.T) {
	ctx := context.Background()
	fake := transport.NewFakeTransport(2, []byte("device-hmac-secret-20"))
	store := yubikey.NewStore(fake, 2)

	// A malformed/unsupported envelope with TWO password-and-yubikey slots is
	// rejected before any YubiKey interaction.
	envelope := buildTwoPrimaryEnvelope(t, fake)
	_, err := store.RewrapPassphrase(ctx, envelope, constFn("pass-a"), []byte("new-pass"))
	assert.ErrorIs(t, err, yubikey.ErrUnsupportedPolicy)
}

// buildTwoPrimaryEnvelope constructs a 2FA envelope carrying two
// password-and-yubikey slots — a shape RewrapPassphrase must refuse.
func buildTwoPrimaryEnvelope(t *testing.T, fake *transport.FakeTransport) []byte {
	t.Helper()
	ctx := context.Background()
	dek, err := tumbler.GenerateDEK(64)
	require.NoError(t, err)
	defer func() { _ = dek.Destroy() }()

	mk := func(pass string) tumbler.Method {
		pw, pErr := securebytes.New([]byte(pass))
		require.NoError(t, pErr)
		return tumbler.NewYubiKeyMethod(fake, tumbler.YubiKeyConfig{
			Slot: 2,
			KDF:  tumbler.NewArgon2idKDF(4, 256*1024, 4),
		}, pw)
	}

	env, err := tumbler.NewEnvelope(ctx, dek, tumbler.PolicyPasswordAndYubiKey, mk("pass-a"))
	require.NoError(t, err)
	require.NoError(t, env.AddSlot(ctx, dek, mk("pass-b")))
	blob, err := env.Marshal()
	require.NoError(t, err)
	return blob
}
