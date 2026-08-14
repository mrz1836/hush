package cli

import (
	"context"
	"os"
	"testing"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/mrz1836/go-tumbler/transport"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/mrz1836/hush/internal/vault/securebytes"
	"github.com/mrz1836/hush/internal/yubikey"
	"github.com/stretchr/testify/require"
)

// Deterministic inputs shared by the enrolled-vault fixtures. The FakeTransport
// secret makes the challenge-response reproducible; the passphrase matches the
// fixture's scripted promptPassphrase so a 2FA envelope unlocks.
const (
	enrolledFixtureFakeSecret = "device-hmac-secret-20"
	enrolledFixturePassphrase = "correctbatterystaple"
)

// newEnrolledSecretFixture builds a real enveloped (v2) vault on top of
// newSecretFixture: it migrates the v1 fixture vault to a keyslots envelope via
// a deterministic FakeTransport, then rewires deps so (a) the enveloped branch
// unlocks through a same-secret fake store and (b) reaching the legacy Argon2id
// KDF is a hard test failure.
func newEnrolledSecretFixture(t *testing.T, entries []testutil.VaultEntry, policy tumbler.Policy) *secretFixture {
	t.Helper()
	fx := newSecretFixture(t, entries)
	ctx := context.Background()

	// Load the current v1 secrets with the fixture's v1 key, then migrate.
	loaded, err := vault.LoadSecrets(ctx, fx.vaultPath, fx.vaultKey)
	require.NoError(t, err)

	store := yubikey.NewStore(transport.NewFakeTransport(2, []byte(enrolledFixtureFakeSecret)), 2)
	opts := yubikey.EnrollOptions{WithRecovery: true}
	if policy == tumbler.PolicyPasswordAndYubiKey {
		opts.Passphrase = []byte(enrolledFixturePassphrase)
	}
	_, err = migrateVaultToYubiKey(ctx, fx.vaultPath, fx.tempDir, loaded, store, policy, opts)
	require.NoError(t, err)
	for _, s := range loaded {
		if s.Value != nil {
			_ = s.Value.Destroy()
		}
	}
	require.True(t, keyslots.Exists(fx.tempDir), "migration must write the keyslots sidecar")
	require.Equal(t, vault.Version2, vaultVersionByte(t, fx.vaultPath), "migrated vault must be v2")

	// Enveloped branch: unlock via a fresh store on the SAME fake secret.
	fx.deps.newUnlocker = func(onTouch func()) (masterSeedUnlocker, error) {
		s := yubikey.NewStore(transport.NewFakeTransport(2, []byte(enrolledFixtureFakeSecret)), 2)
		if onTouch != nil {
			s.SetTouchPrompt(onTouch)
		}
		return s, nil
	}
	// Prove the enveloped branch never falls back to the passphrase KDF.
	fx.deps.deriveMasterSeed = func(_ context.Context, _, _ []byte) ([]byte, error) {
		t.Fatal("enrolled vault must never hit the legacy Argon2id KDF")
		return nil, nil
	}
	return fx
}

// vaultVersionByte returns the on-disk format-version byte (header offset 4).
func vaultVersionByte(t *testing.T, path string) byte {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads a vault it just wrote under a temp dir
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(data), 5)
	return data[4]
}

// openEnrolledVault recovers the master seed through the envelope (fake store +
// passphrase) and loads the vault, so tests can assert contents without driving
// another scripted verb.
func openEnrolledVault(t *testing.T, fx *secretFixture) []vault.Secret {
	t.Helper()
	ctx := context.Background()
	salt, err := readVaultSalt(fx.vaultPath)
	require.NoError(t, err)

	pass, err := securebytes.New([]byte(enrolledFixturePassphrase))
	require.NoError(t, err)
	defer func() { _ = pass.Destroy() }()

	seed, err := unlockMasterSeed(ctx, fx.tempDir, pass, salt, func() (masterSeedUnlocker, error) {
		return yubikey.NewStore(transport.NewFakeTransport(2, []byte(enrolledFixtureFakeSecret)), 2), nil
	})
	require.NoError(t, err)
	defer zeroBytes(seed)

	raw, err := keys.DeriveVaultEncKey(seed)
	require.NoError(t, err)
	key, err := securebytes.New(raw)
	require.NoError(t, err)
	defer func() { _ = key.Destroy() }()

	secrets, err := vault.LoadSecrets(ctx, fx.vaultPath, key)
	require.NoError(t, err)
	return secrets
}

// secretNames returns the sorted-order-independent set of names.
func secretNames(secrets []vault.Secret) []string {
	names := make([]string, 0, len(secrets))
	for _, s := range secrets {
		names = append(names, s.Name)
	}
	return names
}

func destroyAll(secrets []vault.Secret) {
	for _, s := range secrets {
		if s.Value != nil {
			_ = s.Value.Destroy()
		}
	}
}

func TestSecret_Enrolled_Add_RoundTripStaysV2(t *testing.T) {
	fx := newEnrolledSecretFixture(t, nil, tumbler.PolicyPasswordAndYubiKey)

	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"API_KEY"})
	require.NoError(t, err)
	require.Equal(t, vault.Version2, vaultVersionByte(t, fx.vaultPath), "add must not downgrade the vault")

	secrets := openEnrolledVault(t, fx)
	defer destroyAll(secrets)
	require.Contains(t, secretNames(secrets), "API_KEY")
}

func TestSecret_Enrolled_List_ShowsEntriesNoLeak(t *testing.T) {
	fx := newEnrolledSecretFixture(t, []testutil.VaultEntry{
		{Name: "ALPHA", Value: secretSentinel, Description: "first"},
		{Name: "BETA", Value: secretSentinel},
	}, tumbler.PolicyPasswordAndYubiKey)

	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.stdoutFile, fx.deps)
	require.NoError(t, err)

	out := fx.stdout.String()
	require.Contains(t, out, "ALPHA")
	require.Contains(t, out, "BETA")
	// The value must never appear on any stream or in the audit log.
	require.NotContains(t, out, secretSentinel)
	require.NotContains(t, fx.stderr.String(), secretSentinel)
	require.NotContains(t, fx.logBuf.String(), secretSentinel)
}

func TestSecret_Enrolled_Remove_StaysV2(t *testing.T) {
	fx := newEnrolledSecretFixture(t, []testutil.VaultEntry{
		{Name: "KEEP", Value: "k"},
		{Name: "DROP", Value: "d"},
	}, tumbler.PolicyPasswordAndYubiKey)
	// remove requires the typed-name confirmation to equal the target.
	fx.deps.promptLine = scriptedLineReader(t, []string{"DROP"})

	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"DROP"})
	require.NoError(t, err)
	require.Equal(t, vault.Version2, vaultVersionByte(t, fx.vaultPath), "remove must not downgrade the vault")

	secrets := openEnrolledVault(t, fx)
	defer destroyAll(secrets)
	names := secretNames(secrets)
	require.Contains(t, names, "KEEP")
	require.NotContains(t, names, "DROP")
}

func TestSecret_Enrolled_Rotate_StaysV2(t *testing.T) {
	fx := newEnrolledSecretFixture(t, []testutil.VaultEntry{
		{Name: "ONE", Value: "1"},
		{Name: "TWO", Value: "2"},
	}, tumbler.PolicyPasswordAndYubiKey)

	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)
	require.Equal(t, vault.Version2, vaultVersionByte(t, fx.vaultPath), "rotate must not downgrade the vault")

	secrets := openEnrolledVault(t, fx)
	defer destroyAll(secrets)
	require.ElementsMatch(t, []string{"ONE", "TWO"}, secretNames(secrets))
}

func TestSecret_Enrolled_WrongPassphrase_AuthFailed(t *testing.T) {
	fx := newEnrolledSecretFixture(t, []testutil.VaultEntry{{Name: "X", Value: "v"}}, tumbler.PolicyPasswordAndYubiKey)
	fx.deps.promptPassphrase = scriptedSecretReader(t, []string{"the-wrong-passphrase"})

	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.stdoutFile, fx.deps)
	require.Error(t, err)
	require.ErrorIs(t, err, tumbler.ErrAuthFailed)
}

func TestSecret_Enrolled_YkmanMissing_Surfaced(t *testing.T) {
	fx := newEnrolledSecretFixture(t, []testutil.VaultEntry{{Name: "X", Value: "v"}}, tumbler.PolicyPasswordAndYubiKey)
	fx.deps.newUnlocker = func(_ func()) (masterSeedUnlocker, error) {
		return nil, errSyntheticTest
	}

	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.stdoutFile, fx.deps)
	require.Error(t, err)
	require.ErrorIs(t, err, errSyntheticTest)
}

// countingUnlocker wraps a real unlocker and records whether the passphrase
// callback is invoked — used to prove yubikey-only never asks for a passphrase.
type countingUnlocker struct {
	inner             masterSeedUnlocker
	passphraseFnCalls int
}

func (c *countingUnlocker) Unlock(ctx context.Context, envelope []byte, passphraseFn func() ([]byte, error)) ([]byte, error) {
	return c.inner.Unlock(ctx, envelope, func() ([]byte, error) {
		c.passphraseFnCalls++
		return passphraseFn()
	})
}

func TestSecret_Enrolled_YubiKeyOnly_PassphraseFnNeverCalled(t *testing.T) {
	fx := newEnrolledSecretFixture(t, []testutil.VaultEntry{{Name: "Y", Value: "v"}}, tumbler.PolicyYubiKeyOnly)

	counter := &countingUnlocker{}
	fx.deps.newUnlocker = func(_ func()) (masterSeedUnlocker, error) {
		counter.inner = yubikey.NewStore(transport.NewFakeTransport(2, []byte(enrolledFixtureFakeSecret)), 2)
		return counter, nil
	}
	// A wrong passphrase must not matter for yubikey-only.
	fx.deps.promptPassphrase = scriptedSecretReader(t, []string{"irrelevant-passphrase"})

	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.stdoutFile, fx.deps)
	require.NoError(t, err)
	require.Equal(t, 0, counter.passphraseFnCalls, "yubikey-only must never invoke the passphrase callback")
	require.Contains(t, fx.stdout.String(), "Y")
}
