package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// vaultFixture captures stdout/stderr buffers, the in-memory log
// handler, the test vault, and the vaultDeps for one rekey invocation.
// Mirrors secretFixture in spirit but is rekey-specific.
type vaultFixture struct {
	t          *testing.T
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
	stdoutS    *Stream
	stderrS    *Stream
	stdoutFile *os.File
	logBuf     *bytes.Buffer
	deps       *vaultDeps
	vaultPath  string
	vaultKey   *securebytes.SecureBytes
	stdinFile  *os.File
	tempDir    string
}

// newVaultFixture returns a fixture wired to a real on-disk vault
// containing the supplied entries. Tests override individual deps
// fields before invoking runVaultRekey.
func newVaultFixture(t *testing.T, entries []testutil.VaultEntry) *vaultFixture {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	stdoutS := newStream(stdout, false, true)
	stderrS := newStream(stderr, false, true)
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	stdin := dummyTTY(t)

	if entries == nil {
		entries = []testutil.VaultEntry{}
	}
	vaultPath, vaultKey, _ := testutil.NewTestVaultDetailed(t, entries)
	stateDir := filepath.Dir(vaultPath)

	canonical := filepath.Join(stateDir, secretsVaultFilename)
	require.NoError(t, os.Rename(vaultPath, canonical))
	vaultPath = canonical

	frozenSeed := testutil.NewTestKeys(t)

	deps := &vaultDeps{
		loadSecrets:      vault.LoadSecrets,
		saveVault:        vault.SaveWithSalt,
		promptPassphrase: scriptedSecretReader(t, []string{"correctbatterystaple", "newbatterystaple1", "newbatterystaple1"}),
		isStdinTTY:       func(_ *os.File) bool { return true },
		isStdoutTTY:      func(_ *os.File) bool { return true },
		deriveMasterSeed: func(_ context.Context, _, _ []byte) ([]byte, error) {
			out := make([]byte, len(frozenSeed))
			copy(out, frozenSeed)
			return out, nil
		},
		readVaultSalt: readVaultSalt,
		kill:          func(_ int, _ syscall.Signal) error { return nil },
		readPIDFile:   func(_ string) ([]byte, error) { return nil, fsErrNotExist() },
		stateDirRoot:  stateDir,
		logger:        logger,
		nowFn:         time.Now,
	}

	return &vaultFixture{
		t:          t,
		stdout:     stdout,
		stderr:     stderr,
		stdoutS:    stdoutS,
		stderrS:    stderrS,
		stdoutFile: nil,
		logBuf:     logBuf,
		deps:       deps,
		vaultPath:  vaultPath,
		vaultKey:   vaultKey,
		stdinFile:  stdin,
		tempDir:    stateDir,
	}
}

// ---------- Foundational surface tests ----------

func TestVault_RootMounts(t *testing.T) {
	t.Parallel()
	cmd := newVaultCmd()
	require.Equal(t, "vault", cmd.Use)
	require.Nil(t, cmd.RunE)
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Use] = true
	}
	require.True(t, subs["rekey"], "rekey subcommand must be registered")
}

func TestVault_ProductionDeps_NotNil(t *testing.T) {
	t.Parallel()
	d := productionVaultDeps()
	require.NotNil(t, d.loadSecrets)
	require.NotNil(t, d.saveVault)
	require.NotNil(t, d.promptPassphrase)
	require.NotNil(t, d.isStdinTTY)
	require.NotNil(t, d.isStdoutTTY)
	require.NotNil(t, d.deriveMasterSeed)
	require.NotNil(t, d.readVaultSalt)
	require.NotNil(t, d.kill)
	require.NotNil(t, d.readPIDFile)
	require.NotNil(t, d.logger)
	require.NotNil(t, d.nowFn)
}

// ---------- TTY refusal ----------

func TestVaultRekey_RefusesPipedStdin(t *testing.T) {
	t.Parallel()
	fx := newVaultFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	fx.deps.isStdinTTY = func(_ *os.File) bool { return false }
	fx.deps.isStdoutTTY = func(_ *os.File) bool { return true }

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.True(t, errors.Is(err, errNoTTY))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Equal(t, vaultMsgNoTTY+"\n", fx.stderr.String())

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes), "vault must not change on TTY refusal")
	require.Contains(t, fx.logBuf.String(), "vault_rekeyed")
	require.Contains(t, fx.logBuf.String(), "outcome=tty_refused")
	require.Contains(t, fx.logBuf.String(), "verb=rekey")
}

func TestVaultRekey_RefusesRedirectedStdout(t *testing.T) {
	t.Parallel()
	fx := newVaultFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	fx.deps.isStdinTTY = func(_ *os.File) bool { return true }
	fx.deps.isStdoutTTY = func(_ *os.File) bool { return false }

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.True(t, errors.Is(err, errNoTTY))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Equal(t, vaultMsgNoTTY+"\n", fx.stderr.String())

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes))
	require.Contains(t, fx.logBuf.String(), "outcome=tty_refused")
}

// ---------- Passphrase authentication ----------

func TestVaultRekey_OldPassphraseAuthFailure(t *testing.T) {
	t.Parallel()
	fx := newVaultFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	// Force LoadSecrets to surface ErrAuthFailed regardless of inputs.
	fx.deps.loadSecrets = func(_ context.Context, _ string, _ *securebytes.SecureBytes) ([]vault.Secret, error) {
		return nil, fmt.Errorf("decrypt: %w", vault.ErrAuthFailed)
	}
	// Only the current passphrase prompt fires before the load fails.
	fx.deps.promptPassphrase = scriptedSecretReader(t, []string{"wrong-passphrase"})

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.True(t, errors.Is(err, vault.ErrAuthFailed))
	require.Equal(t, ExitAuth, mapErr(err))

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes), "vault must not change on auth failure")
	require.Contains(t, fx.logBuf.String(), "vault_rekeyed")
	require.Contains(t, fx.logBuf.String(), "outcome=passphrase_failed")
}

// ---------- New passphrase too short ----------

func TestVaultRekey_NewPassphraseTooShort(t *testing.T) {
	t.Parallel()
	fx := newVaultFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	// current passphrase + short new passphrase (the confirm prompt
	// must NOT fire because validation rejects the short new pass
	// before the second prompt).
	fx.deps.promptPassphrase = scriptedSecretReader(t, []string{"correctbatterystaple", "short"})

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.True(t, errors.Is(err, errPassphraseTooShort))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Equal(t, vaultMsgPassphraseTooShort+"\n", fx.stderr.String())

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes))
	require.Contains(t, fx.logBuf.String(), "vault_rekeyed")
	require.Contains(t, fx.logBuf.String(), "outcome=passphrase_too_short")
}

// ---------- New passphrase confirmation mismatch ----------

func TestVaultRekey_NewPassphraseMismatch(t *testing.T) {
	t.Parallel()
	fx := newVaultFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	fx.deps.promptPassphrase = scriptedSecretReader(t, []string{
		"correctbatterystaple", // current
		"newbatterystaple1",    // new
		"differentpassphrase1", // confirm — mismatch
	})

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.True(t, errors.Is(err, errPassphraseMismatch))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Equal(t, vaultMsgPassphraseMismatch+"\n", fx.stderr.String())

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes))
	require.Contains(t, fx.logBuf.String(), "vault_rekeyed")
	require.Contains(t, fx.logBuf.String(), "outcome=new_passphrase_mismatch")
}

// ---------- New passphrase equals old ----------

func TestVaultRekey_NewPassphraseEqualsOld(t *testing.T) {
	t.Parallel()
	fx := newVaultFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	fx.deps.promptPassphrase = scriptedSecretReader(t, []string{
		"correctbatterystaple", // current
		"correctbatterystaple", // new (same)
		"correctbatterystaple", // confirm
	})

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.True(t, errors.Is(err, errPassphraseUnchanged))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Equal(t, vaultMsgPassphraseUnchanged+"\n", fx.stderr.String())

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes))
	require.Contains(t, fx.logBuf.String(), "vault_rekeyed")
	require.Contains(t, fx.logBuf.String(), "outcome=new_passphrase_unchanged")
}

// ---------- Phase 2 stops before any write ----------

// TestVaultRekey_Phase2StopsBeforeRewrite proves the validation path
// reaches the planned "not yet implemented" sentinel without rewriting
// the vault. Phase 3 replaces this test with the real snapshot +
// rewrite assertions.
func TestVaultRekey_Phase2StopsBeforeRewrite(t *testing.T) {
	t.Parallel()
	fx := newVaultFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.Error(t, err)
	require.True(t, errors.Is(err, errVaultRekeyNotImplemented))

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes), "Phase 2 must not rewrite the vault")
}

// ---------- Constant-time compare helper ----------

func TestConstantTimeSecureEqual(t *testing.T) {
	t.Parallel()
	require.Equal(t, 1, constantTimeSecureEqual([]byte("abc"), []byte("abc")))
	require.Equal(t, 0, constantTimeSecureEqual([]byte("abc"), []byte("abd")))
	require.Equal(t, 0, constantTimeSecureEqual([]byte("abc"), []byte("abcd")))
	require.Equal(t, 0, constantTimeSecureEqual(nil, []byte("x")))
	require.Equal(t, 1, constantTimeSecureEqual(nil, nil))
}
