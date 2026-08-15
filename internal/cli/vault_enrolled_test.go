package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/mrz1836/go-tumbler/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/mrz1836/hush/internal/yubikey"
)

// vaultRekeyNewPass is the new passphrase the fixture's scripted reader yields
// (matching newVaultFixture's promptPassphrase script).
const vaultRekeyNewPass = "newbatterystaple1"

// newEnrolledVaultFixture migrates the v1 fixture vault to an enveloped (v2)
// vault via a deterministic FakeTransport, then wires deps.newRewrapper to a
// same-secret fake store and makes the legacy KDF a hard failure.
func newEnrolledVaultFixture(t *testing.T, entries []testutil.VaultEntry, policy tumbler.Policy) *vaultFixture {
	t.Helper()
	fx := newVaultFixture(t, entries)
	ctx := context.Background()

	loaded, err := vault.LoadSecrets(ctx, fx.vaultPath, fx.vaultKey)
	require.NoError(t, err)

	store := yubikey.NewStore(transport.NewFakeTransport(2, []byte(enrolledFixtureFakeSecret)), 2)
	opts := yubikey.EnrollOptions{WithRecovery: true}
	if policy == tumbler.PolicyPasswordAndYubiKey {
		opts.Passphrase = []byte(vaultRekeyTestCurrentPass)
	}
	_, err = migrateVaultToYubiKey(ctx, fx.vaultPath, fx.tempDir, loaded, store, policy, opts)
	require.NoError(t, err)
	for _, s := range loaded {
		if s.Value != nil {
			_ = s.Value.Destroy()
		}
	}
	require.True(t, keyslots.Exists(fx.tempDir))

	fx.deps.newRewrapper = func(onTouch func()) (passphraseRewrapper, error) {
		s := yubikey.NewStore(transport.NewFakeTransport(2, []byte(enrolledFixtureFakeSecret)), 2)
		if onTouch != nil {
			s.SetTouchPrompt(onTouch)
		}
		return s, nil
	}
	fx.deps.deriveMasterSeed = func(_ context.Context, _, _ []byte) ([]byte, error) {
		t.Fatal("enveloped rekey must not derive the vault key from the passphrase")
		return nil, nil
	}
	return fx
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: test reads files under its own temp dir
	require.NoError(t, err)
	return b
}

func keyslotsBackupExists(t *testing.T, stateDir string) bool {
	t.Helper()
	matches, err := filepath.Glob(keyslots.Path(stateDir) + ".bak-*")
	require.NoError(t, err)
	return len(matches) > 0
}

// unlockEnvelopeSeed recovers the master seed from the on-disk envelope using
// the given passphrase and a same-secret fake store.
func unlockEnvelopeSeed(t *testing.T, stateDir, pass string) []byte {
	t.Helper()
	envelope, _, err := keyslots.Load(stateDir)
	require.NoError(t, err)
	store := yubikey.NewStore(transport.NewFakeTransport(2, []byte(enrolledFixtureFakeSecret)), 2)
	seed, err := store.Unlock(context.Background(), envelope, func() ([]byte, error) {
		return []byte(pass), nil
	})
	require.NoError(t, err)
	return seed
}

func TestVaultRekey_Enveloped_RewrapsInPlace(t *testing.T) {
	fx := newEnrolledVaultFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}}, tumbler.PolicyPasswordAndYubiKey)

	vaultBefore := readFileBytes(t, fx.vaultPath)
	keyslotsBefore := readFileBytes(t, keyslots.Path(fx.tempDir))
	seedBefore := unlockEnvelopeSeed(t, fx.tempDir, vaultRekeyTestCurrentPass)

	err := runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.stdoutFile, fx.deps)
	require.NoError(t, err)

	// The vault body is byte-identical — only the passphrase factor changed.
	assert.Equal(t, vaultBefore, readFileBytes(t, fx.vaultPath), "secrets.vault must be untouched")
	// The sidecar changed and a snapshot was written.
	assert.NotEqual(t, keyslotsBefore, readFileBytes(t, keyslots.Path(fx.tempDir)), "keyslots.json must change")
	assert.True(t, keyslotsBackupExists(t, fx.tempDir), "a keyslots.json.bak-* snapshot must exist")

	// The NEW passphrase opens the re-wrapped envelope and yields the SAME seed.
	seedAfter := unlockEnvelopeSeed(t, fx.tempDir, vaultRekeyNewPass)
	assert.Equal(t, seedBefore, seedAfter, "master seed must be unchanged")

	// Audit shape: enveloped success, no restart.
	logs := fx.logBuf.String()
	assert.Contains(t, logs, "outcome=success")
	assert.Contains(t, logs, "restart_required=false")
	assert.Contains(t, logs, "rekey_mode=enveloped")

	// Operator copy: no v1 restart line; the enveloped success line instead.
	assert.NotContains(t, fx.stderr.String(), "restart it to pick up")
	assert.Contains(t, fx.stdout.String(), "no restart needed")
}

func TestVaultRekey_Enveloped_YubiKeyOnly_Refused(t *testing.T) {
	fx := newEnrolledVaultFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}}, tumbler.PolicyYubiKeyOnly)

	keyslotsBefore := readFileBytes(t, keyslots.Path(fx.tempDir))

	err := runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.stdoutFile, fx.deps)
	require.NoError(t, err, "refusal is not an error; nothing is mutated")

	assert.Contains(t, fx.stderr.String(), "yubikey-only")
	assert.Equal(t, keyslotsBefore, readFileBytes(t, keyslots.Path(fx.tempDir)), "sidecar must be unchanged")
	assert.False(t, keyslotsBackupExists(t, fx.tempDir), "no snapshot on refusal")
	assert.Contains(t, fx.logBuf.String(), "outcome=no_passphrase_factor")
}

func TestVaultRekey_Enveloped_SaveFailure_LeavesSnapshot(t *testing.T) {
	fx := newEnrolledVaultFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}}, tumbler.PolicyPasswordAndYubiKey)

	keyslotsBefore := readFileBytes(t, keyslots.Path(fx.tempDir))

	// Loosen the state dir so keyslots.Save's checkDirMode (0700) fails AFTER the
	// snapshot is written — the snapshot creation itself only needs owner write.
	require.NoError(t, os.Chmod(fx.tempDir, 0o755))
	t.Cleanup(func() { _ = os.Chmod(fx.tempDir, 0o700) })

	err := runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.stdoutFile, fx.deps)
	require.Error(t, err)

	// The sidecar is untouched (Save aborted before writing) and the snapshot
	// remains as the rollback artifact.
	assert.Equal(t, keyslotsBefore, readFileBytes(t, keyslots.Path(fx.tempDir)))
	assert.True(t, keyslotsBackupExists(t, fx.tempDir), "snapshot must survive a Save failure")
}
