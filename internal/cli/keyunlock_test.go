package cli

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

type fakeUnlocker struct {
	seed             []byte
	err              error
	gotEnvelope      []byte
	calledPassphrase bool
}

func (f *fakeUnlocker) Unlock(_ context.Context, envelope []byte, passphraseFn func() ([]byte, error)) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.gotEnvelope = envelope
	if passphraseFn != nil {
		pf, err := passphraseFn()
		if err != nil {
			return nil, err
		}
		if len(pf) > 0 {
			f.calledPassphrase = true
		}
	}
	return append([]byte(nil), f.seed...), nil
}

func stateDir0700(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	return dir
}

func mkPassphrase(t *testing.T, s string) *securebytes.SecureBytes {
	t.Helper()
	sb, err := securebytes.New([]byte(s))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Destroy() })
	return sb
}

// TestUnlockMasterSeed_LegacyPath uses the real Argon2id derivation (locked
// params) so it is intentionally a bit slow; it proves the password-only path
// is untouched.
func TestUnlockMasterSeed_LegacyPath(t *testing.T) {
	dir := stateDir0700(t)
	pass := mkPassphrase(t, "correct horse battery staple")
	salt := make([]byte, 16)

	seed, err := unlockMasterSeed(context.Background(), dir, pass, salt,
		func() (masterSeedUnlocker, error) {
			t.Fatal("unlocker factory must not be called without an envelope")
			return nil, errSyntheticTest // unreachable after t.Fatal; satisfies nilnil
		})
	require.NoError(t, err)
	assert.Len(t, seed, 64)
}

func TestUnlockMasterSeed_EnvelopedPath(t *testing.T) {
	dir := stateDir0700(t)
	require.NoError(t, keyslots.Save(dir, []byte("marshaled-envelope"), "password-and-yubikey"))

	pass := mkPassphrase(t, "the-passphrase")
	fake := &fakeUnlocker{seed: make([]byte, 64)}
	for i := range fake.seed {
		fake.seed[i] = byte(i)
	}

	seed, err := unlockMasterSeed(context.Background(), dir, pass, make([]byte, 16),
		func() (masterSeedUnlocker, error) { return fake, nil })
	require.NoError(t, err)
	assert.Equal(t, fake.seed, seed)
	assert.Equal(t, []byte("marshaled-envelope"), fake.gotEnvelope)
	assert.True(t, fake.calledPassphrase, "2FA store should receive the passphrase")
}

func TestUnlockMasterSeed_FactoryError(t *testing.T) {
	dir := stateDir0700(t)
	require.NoError(t, keyslots.Save(dir, []byte("env"), "yubikey-only"))
	pass := mkPassphrase(t, "passphrase")

	_, err := unlockMasterSeed(context.Background(), dir, pass, make([]byte, 16),
		func() (masterSeedUnlocker, error) { return nil, errors.New("ykman missing") })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "YubiKey")
}

func TestUnlockMasterSeed_UnlockError(t *testing.T) {
	dir := stateDir0700(t)
	require.NoError(t, keyslots.Save(dir, []byte("env"), "yubikey-only"))
	pass := mkPassphrase(t, "passphrase")

	sentinel := errors.New("touch timeout")
	_, err := unlockMasterSeed(context.Background(), dir, pass, make([]byte, 16),
		func() (masterSeedUnlocker, error) { return &fakeUnlocker{err: sentinel}, nil })
	assert.ErrorIs(t, err, sentinel)
}
