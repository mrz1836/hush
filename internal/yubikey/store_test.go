package yubikey_test

import (
	"bytes"
	"context"
	"testing"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/mrz1836/go-tumbler/transport"
	"github.com/mrz1836/hush/internal/yubikey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStore() *yubikey.Store {
	fake := transport.NewFakeTransport(2, []byte("device-hmac-secret-20"))
	return yubikey.NewStore(fake, 2)
}

func TestEnroll_GeneratesRandom64ByteSeed(t *testing.T) {
	ctx := context.Background()
	store := newStore()

	res, err := store.Enroll(ctx, tumbler.PolicyYubiKeyOnly, yubikey.EnrollOptions{})
	require.NoError(t, err)
	require.Len(t, res.MasterSeed, 64, "hush master seed is 64 bytes")
	require.NotEmpty(t, res.Envelope)

	// A second enrollment yields a different random seed.
	res2, err := store.Enroll(ctx, tumbler.PolicyYubiKeyOnly, yubikey.EnrollOptions{})
	require.NoError(t, err)
	assert.False(t, bytes.Equal(res.MasterSeed, res2.MasterSeed))
}

func TestEnrollUnlock_TwoFactor(t *testing.T) {
	ctx := context.Background()
	store := newStore()
	pass := []byte("correct-horse-battery-staple")

	res, err := store.Enroll(ctx, tumbler.PolicyPasswordAndYubiKey, yubikey.EnrollOptions{Passphrase: pass})
	require.NoError(t, err)

	pol, err := store.EffectivePolicy(res.Envelope)
	require.NoError(t, err)
	assert.Equal(t, tumbler.PolicyPasswordAndYubiKey, pol)

	// Correct passphrase recovers the SAME random master seed.
	got, err := store.Unlock(ctx, res.Envelope, func() ([]byte, error) {
		return []byte("correct-horse-battery-staple"), nil
	})
	require.NoError(t, err)
	assert.True(t, bytes.Equal(res.MasterSeed, got), "recovered seed must equal the enrolled seed")

	// Wrong passphrase fails.
	_, err = store.Unlock(ctx, res.Envelope, func() ([]byte, error) {
		return []byte("wrong"), nil
	})
	assert.ErrorIs(t, err, tumbler.ErrAuthFailed)
}

func TestEnrollUnlock_YubiKeyOnly(t *testing.T) {
	ctx := context.Background()
	store := newStore()

	res, err := store.Enroll(ctx, tumbler.PolicyYubiKeyOnly, yubikey.EnrollOptions{})
	require.NoError(t, err)

	got, err := store.Unlock(ctx, res.Envelope, nil)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(res.MasterSeed, got))
}

func TestEnroll_TwoFactorRequiresPassphrase(t *testing.T) {
	ctx := context.Background()
	store := newStore()
	_, err := store.Enroll(ctx, tumbler.PolicyPasswordAndYubiKey, yubikey.EnrollOptions{})
	assert.ErrorIs(t, err, yubikey.ErrPassphraseRequired)
}

func TestRecoveryCode(t *testing.T) {
	ctx := context.Background()
	store := newStore()

	res, err := store.Enroll(ctx, tumbler.PolicyPasswordAndYubiKey, yubikey.EnrollOptions{
		Passphrase:   []byte("passphrase-value"),
		WithRecovery: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.RecoveryCode)

	got, err := store.UnlockWithRecovery(ctx, res.Envelope, res.RecoveryCode)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(res.MasterSeed, got))
}
