package cli

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/mrz1836/go-tumbler/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/mrz1836/hush/internal/vault/securebytes"
	"github.com/mrz1836/hush/internal/yubikey"
)

// deriveVaultKey builds the vault encryption key from a passphrase+salt via
// the legacy path, returning an owned SecureBytes.
func deriveVaultKeyForTest(t *testing.T, ctx context.Context, passphrase, salt []byte) *securebytes.SecureBytes {
	t.Helper()
	seed, err := keys.DeriveMasterSeed(ctx, passphrase, salt)
	require.NoError(t, err)
	defer func() {
		for i := range seed {
			seed[i] = 0
		}
	}()
	raw, err := keys.DeriveVaultEncKey(seed)
	require.NoError(t, err)
	key, err := securebytes.New(raw)
	require.NoError(t, err)
	return key
}

func TestMigrateVaultToYubiKey_EndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := stateDir0700(t)
	vaultPath := filepath.Join(dir, "secrets.vault")

	passphrase := []byte("original-passphrase-strong")
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)

	// 1. Create a legacy v1 (password-only) vault with one secret.
	val, err := securebytes.New([]byte("s3cr3t-value"))
	require.NoError(t, err)
	oldKey := deriveVaultKeyForTest(t, ctx, passphrase, salt)
	require.NoError(t, vault.SaveWithSalt(ctx, vaultPath, oldKey, salt, []vault.Secret{
		{Name: "api", Description: "prod key", Value: val},
	}))
	_ = oldKey.Destroy()

	data, err := os.ReadFile(vaultPath)
	require.NoError(t, err)
	require.Equal(t, vault.Version1, data[4], "fixture must be v1")

	// 2. Load the current secrets with the old key (migration load step).
	loadKey := deriveVaultKeyForTest(t, ctx, passphrase, salt)
	loaded, err := vault.LoadSecrets(ctx, vaultPath, loadKey)
	require.NoError(t, err)
	_ = loadKey.Destroy()

	// 3. Migrate to password-and-yubikey with a recovery code.
	fake := transport.NewFakeTransport(2, []byte("device-hmac-secret-20"))
	store := yubikey.NewStore(fake, 2)
	recoveryCode, err := migrateVaultToYubiKey(ctx, vaultPath, dir, loaded, store,
		tumbler.PolicyPasswordAndYubiKey, yubikey.EnrollOptions{Passphrase: passphrase, WithRecovery: true})
	require.NoError(t, err)
	require.NotEmpty(t, recoveryCode)
	for _, s := range loaded {
		_ = s.Value.Destroy()
	}

	// 4. The vault is now v2 and a keyslots sidecar exists.
	data, err = os.ReadFile(vaultPath)
	require.NoError(t, err)
	assert.Equal(t, vault.Version2, data[4], "migrated vault must be v2")
	assert.True(t, keyslots.Exists(dir))

	// 5. Unlock via the envelope (touch + passphrase), derive the new key,
	//    and read back the identical secret.
	passSB, err := securebytes.New(append([]byte(nil), passphrase...))
	require.NoError(t, err)
	defer func() { _ = passSB.Destroy() }()

	newSeed, err := unlockMasterSeed(ctx, dir, passSB, salt,
		func() (masterSeedUnlocker, error) { return yubikey.NewStore(fake, 2), nil })
	require.NoError(t, err)
	newRaw, err := keys.DeriveVaultEncKey(newSeed)
	require.NoError(t, err)
	newKey, err := securebytes.New(newRaw)
	require.NoError(t, err)
	defer func() { _ = newKey.Destroy() }()

	store2, err := vault.Load(ctx, vaultPath, newKey)
	require.NoError(t, err)
	got, err := store2.Get("api")
	require.NoError(t, err)
	require.NoError(t, got.Use(func(b []byte) {
		assert.Equal(t, []byte("s3cr3t-value"), b)
	}))

	// 6. The old passphrase-derived key must NOT open the v2 vault — proving
	//    the passphrase alone can no longer bypass the YubiKey factor.
	staleKey := deriveVaultKeyForTest(t, ctx, passphrase, salt)
	defer func() { _ = staleKey.Destroy() }()
	_, err = vault.Load(ctx, vaultPath, staleKey)
	assert.ErrorIs(t, err, vault.ErrAuthFailed)
}

func TestMigrateVaultToYubiKey_RecoveryUnlock(t *testing.T) {
	ctx := context.Background()
	dir := stateDir0700(t)
	vaultPath := filepath.Join(dir, "secrets.vault")

	passphrase := []byte("another-passphrase-value")
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)

	val, err := securebytes.New([]byte("recover-me"))
	require.NoError(t, err)
	oldKey := deriveVaultKeyForTest(t, ctx, passphrase, salt)
	require.NoError(t, vault.SaveWithSalt(ctx, vaultPath, oldKey, salt, []vault.Secret{
		{Name: "db", Description: "", Value: val},
	}))
	_ = oldKey.Destroy()

	loadKey := deriveVaultKeyForTest(t, ctx, passphrase, salt)
	loaded, err := vault.LoadSecrets(ctx, vaultPath, loadKey)
	require.NoError(t, err)
	_ = loadKey.Destroy()

	fake := transport.NewFakeTransport(2, []byte("secret"))
	store := yubikey.NewStore(fake, 2)
	rc, err := migrateVaultToYubiKey(ctx, vaultPath, dir, loaded, store,
		tumbler.PolicyYubiKeyOnly, yubikey.EnrollOptions{WithRecovery: true})
	require.NoError(t, err)
	for _, s := range loaded {
		_ = s.Value.Destroy()
	}

	// Recover the master seed with the recovery code and open the vault.
	envelope, _, err := keyslots.Load(dir)
	require.NoError(t, err)
	newSeed, err := store.UnlockWithRecovery(ctx, envelope, rc)
	require.NoError(t, err)
	newRaw, err := keys.DeriveVaultEncKey(newSeed)
	require.NoError(t, err)
	newKey, err := securebytes.New(newRaw)
	require.NoError(t, err)
	defer func() { _ = newKey.Destroy() }()

	store2, err := vault.Load(ctx, vaultPath, newKey)
	require.NoError(t, err)
	assert.Contains(t, store2.Names(), "db")
}
