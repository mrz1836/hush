package cli

import (
	"context"
	"crypto/rand"
	"fmt"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/mrz1836/hush/internal/vault/securebytes"
	"github.com/mrz1836/hush/internal/yubikey"
)

// seedEnroller is the yubikey.Store capability the migration needs; an
// interface so tests inject a FakeTransport-backed store.
type seedEnroller interface {
	Enroll(ctx context.Context, policy tumbler.Policy, opts yubikey.EnrollOptions) (*yubikey.EnrollResult, error)
}

// migrateVaultToYubiKey re-encrypts the vault at vaultPath under a NEW random
// master seed wrapped by a YubiKey keyslots envelope, stamps the vault with
// format version 0x02, and writes the keyslots sidecar under stateDir.
//
// secrets are the current vault contents (already loaded via the old
// passphrase-derived key by the caller). On success the vault can no longer be
// opened by the passphrase alone — recovery requires the envelope (YubiKey,
// plus passphrase for 2FA) or the returned recovery code.
//
// The caller is responsible for snapshotting the vault first (rollback) and
// for destroying the loaded secrets afterward.
func migrateVaultToYubiKey(
	ctx context.Context,
	vaultPath, stateDir string,
	secrets []vault.Secret,
	store seedEnroller,
	policy tumbler.Policy,
	opts yubikey.EnrollOptions,
) (recoveryCode string, err error) {
	// 1. Generate the new random master seed + envelope (this triggers touch).
	res, err := store.Enroll(ctx, policy, opts)
	if err != nil {
		return "", fmt.Errorf("enroll: %w", err)
	}
	defer zeroBytes(res.MasterSeed)

	// 2. Derive the new vault encryption key from the new master seed.
	rawKey, err := keys.DeriveVaultEncKey(res.MasterSeed)
	if err != nil {
		return "", fmt.Errorf("derive vault key: %w", err)
	}
	defer zeroBytes(rawKey)
	vaultEncKey, err := securebytes.New(rawKey)
	if err != nil {
		return "", fmt.Errorf("wrap vault key: %w", err)
	}
	defer func() { _ = vaultEncKey.Destroy() }()

	// 3. Re-encrypt the vault under the new key with a fresh salt and v2.
	salt := make([]byte, 16)
	if _, rerr := rand.Read(salt); rerr != nil {
		return "", fmt.Errorf("rand salt: %w", rerr)
	}
	if serr := vault.SaveWithSaltAndVersion(ctx, vaultPath, vaultEncKey, salt, vault.Version2, secrets); serr != nil {
		return "", fmt.Errorf("re-encrypt vault: %w", serr)
	}

	// 4. Persist the keyslots sidecar. If this fails after the vault was
	// re-encrypted, the caller must restore the snapshot.
	if kerr := keyslots.Save(stateDir, res.Envelope, policy.String()); kerr != nil {
		return "", fmt.Errorf("save keyslots: %w", kerr)
	}
	return res.RecoveryCode, nil
}
