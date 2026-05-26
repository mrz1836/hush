package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/keys"
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
		randReader:    rand.Reader,
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
	require.NotNil(t, d.randReader)
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

// ---------- Phase 3: snapshot + rewrite ----------

// newVaultRekeyRoundTripFixture builds a fresh on-disk vault sealed
// with the operator's "current" passphrase and wires deriveMasterSeed
// to a deterministic but PASSPHRASE-AWARE function so the round-trip
// can distinguish "old passphrase" from "new passphrase". The
// testutil.NewTestKeys cached seed used by other fixtures is identical
// across passphrases — that is fine for input-validation tests, but
// would make the AC-7 "old passphrase no longer decrypts" assertion
// vacuous.
func newVaultRekeyRoundTripFixture(t *testing.T, entries []testutil.VaultEntry, currentPass string) *vaultFixture {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	stdoutS := newStream(stdout, false, true)
	stderrS := newStream(stderr, false, true)
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	stdin := dummyTTY(t)

	// Passphrase-aware deterministic master seed for tests:
	// SHA-256(passphrase || salt). Real production uses Argon2id; the
	// CLI path's only requirement is that two distinct passphrases
	// produce two distinct seeds.
	deriveSeed := func(_ context.Context, passphrase, salt []byte) ([]byte, error) {
		h := sha256.New()
		h.Write(passphrase)
		h.Write(salt)
		return h.Sum(nil), nil
	}

	stateDir := t.TempDir()
	require.NoError(t, os.Chmod(stateDir, 0o700))
	vaultPath := filepath.Join(stateDir, secretsVaultFilename)

	// Seed the on-disk vault under the current passphrase + a fresh
	// salt so the test exercises the real read-salt → derive-key →
	// load-secrets path.
	initialSalt := mustReadFullForTest(t, rand.Reader, 16)
	initialSeed, err := deriveSeed(context.Background(), []byte(currentPass), initialSalt)
	require.NoError(t, err)
	initialRawKey, err := keys.DeriveVaultEncKey(initialSeed)
	require.NoError(t, err)
	initialKey, err := securebytes.New(initialRawKey)
	require.NoError(t, err)
	t.Cleanup(func() { _ = initialKey.Destroy() })

	seedSecrets := make([]vault.Secret, 0, len(entries))
	valueSBs := make([]*securebytes.SecureBytes, 0, len(entries))
	for _, e := range entries {
		sb, sbErr := securebytes.New([]byte(e.Value))
		require.NoError(t, sbErr)
		valueSBs = append(valueSBs, sb)
		seedSecrets = append(seedSecrets, vault.Secret{Name: e.Name, Description: e.Description, Value: sb})
	}
	t.Cleanup(func() {
		for _, sb := range valueSBs {
			_ = sb.Destroy()
		}
	})
	require.NoError(t, vault.SaveWithSalt(context.Background(), vaultPath, initialKey, initialSalt, seedSecrets))

	deps := &vaultDeps{
		loadSecrets:      vault.LoadSecrets,
		saveVault:        vault.SaveWithSalt,
		promptPassphrase: scriptedSecretReader(t, []string{currentPass, "newbatterystaple1", "newbatterystaple1"}),
		isStdinTTY:       func(_ *os.File) bool { return true },
		isStdoutTTY:      func(_ *os.File) bool { return true },
		deriveMasterSeed: deriveSeed,
		readVaultSalt:    readVaultSalt,
		kill:             func(_ int, _ syscall.Signal) error { return nil },
		readPIDFile:      func(_ string) ([]byte, error) { return nil, fsErrNotExist() },
		randReader:       rand.Reader,
		stateDirRoot:     stateDir,
		logger:           logger,
		nowFn:            time.Now,
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
		vaultKey:   initialKey,
		stdinFile:  stdin,
		tempDir:    stateDir,
	}
}

func mustReadFullForTest(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	require.NoError(t, err)
	return buf
}

// findVaultSnapshot returns the absolute path of the unique
// `secrets.vault.bak-*` snapshot file under stateDir. Fails the test
// if zero or more than one snapshot is present.
func findVaultSnapshot(t *testing.T, stateDir string) string {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	require.NoError(t, err)
	matches := make([]string, 0, 1)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasPrefix(name, secretsVaultFilename+".bak-") {
			matches = append(matches, filepath.Join(stateDir, name))
		}
	}
	sort.Strings(matches)
	require.Lenf(t, matches, 1, "expected exactly one snapshot, got %v", matches)
	return matches[0]
}

// TestVaultRekey_RoundTrip_SnapshotsAndRewrites is the AC-6/AC-7
// round-trip: snapshot is created with 0600 perms byte-identical to
// the pre-rekey file; new vault decrypts under the new passphrase with
// the original secret set preserved; old passphrase fails; salt
// changed.
func TestVaultRekey_RoundTrip_SnapshotsAndRewrites(t *testing.T) {
	t.Parallel()
	const currentPass = "correctbatterystaple"
	entries := []testutil.VaultEntry{
		{Name: "FOO", Description: "alpha", Value: "valueA"},
		{Name: "BAR", Description: "bravo", Value: "valueB"},
	}
	fx := newVaultRekeyRoundTripFixture(t, entries, currentPass)

	preBytes, err := os.ReadFile(fx.vaultPath)
	require.NoError(t, err)
	preSalt, err := readVaultSalt(fx.vaultPath)
	require.NoError(t, err)

	err = runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.True(t, errors.Is(err, errVaultRekeyPostWriteNotImplemented),
		"Phase 3 must complete the rewrite and stop with the Phase 4 stub; got %v", err)

	// Snapshot exists, 0600, byte-identical to the pre-rekey vault.
	snapPath := findVaultSnapshot(t, fx.tempDir)
	info, err := os.Stat(snapPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "snapshot must be 0600")
	snapBytes, err := os.ReadFile(snapPath) //nolint:gosec // test-controlled snapshot path
	require.NoError(t, err)
	require.True(t, bytes.Equal(preBytes, snapBytes),
		"snapshot must be byte-identical to the pre-rekey vault file")

	// On-disk vault salt changed.
	postSalt, err := readVaultSalt(fx.vaultPath)
	require.NoError(t, err)
	require.False(t, bytes.Equal(preSalt, postSalt), "salt must change after rekey")

	// New passphrase decrypts; secrets preserved exactly.
	newSeed, err := fx.deps.deriveMasterSeed(context.Background(), []byte("newbatterystaple1"), postSalt)
	require.NoError(t, err)
	newRawKey, err := keys.DeriveVaultEncKey(newSeed)
	require.NoError(t, err)
	newKey, err := securebytes.New(newRawKey)
	require.NoError(t, err)
	defer func() { _ = newKey.Destroy() }()
	loaded, err := vault.LoadSecrets(context.Background(), fx.vaultPath, newKey)
	require.NoError(t, err)
	defer func() {
		for i := len(loaded) - 1; i >= 0; i-- {
			if loaded[i].Value != nil {
				_ = loaded[i].Value.Destroy()
			}
		}
	}()
	require.Len(t, loaded, len(entries))
	loadedByName := map[string]vault.Secret{}
	for _, s := range loaded {
		loadedByName[s.Name] = s
	}
	for _, want := range entries {
		got, ok := loadedByName[want.Name]
		require.Truef(t, ok, "secret %q missing after rekey", want.Name)
		require.Equal(t, want.Description, got.Description)
		var raw []byte
		require.NoError(t, got.Value.Use(func(b []byte) {
			raw = append([]byte(nil), b...)
		}))
		require.Equal(t, want.Value, string(raw))
	}

	// Old passphrase no longer decrypts the rewritten vault.
	oldSeed, err := fx.deps.deriveMasterSeed(context.Background(), []byte(currentPass), postSalt)
	require.NoError(t, err)
	oldRawKey, err := keys.DeriveVaultEncKey(oldSeed)
	require.NoError(t, err)
	oldKey, err := securebytes.New(oldRawKey)
	require.NoError(t, err)
	defer func() { _ = oldKey.Destroy() }()
	_, err = vault.LoadSecrets(context.Background(), fx.vaultPath, oldKey)
	require.True(t, errors.Is(err, vault.ErrAuthFailed),
		"old passphrase must fail to decrypt the rewritten vault; got %v", err)
}

// TestVaultRekey_SnapshotFailureAbortsBeforeRewrite proves the
// snapshot-before-rewrite ordering: a snapshot create failure surfaces
// to the caller and the on-disk vault is left untouched.
func TestVaultRekey_SnapshotFailureAbortsBeforeRewrite(t *testing.T) {
	t.Parallel()
	const currentPass = "correctbatterystaple"
	fx := newVaultRekeyRoundTripFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}}, currentPass)

	// Pre-create the exact snapshot path the rekey would use so the
	// O_EXCL open in snapshotVaultFile fails. Use a fixed nowFn so the
	// path is deterministic for the test.
	frozen := time.Date(2026, 5, 26, 1, 23, 45, 0, time.UTC)
	fx.deps.nowFn = func() time.Time { return frozen }
	collidingPath := fx.vaultPath + ".bak-" + frozen.Format(time.RFC3339)
	require.NoError(t, os.WriteFile(collidingPath, []byte("preexisting"), 0o600))

	preBytes, err := os.ReadFile(fx.vaultPath)
	require.NoError(t, err)

	err = runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.Error(t, err)
	require.False(t, errors.Is(err, errVaultRekeyPostWriteNotImplemented),
		"snapshot failure must abort before the rewrite stub is reached")

	postBytes, err := os.ReadFile(fx.vaultPath)
	require.NoError(t, err)
	require.True(t, bytes.Equal(preBytes, postBytes), "vault must not change when the snapshot fails")
}

// TestVaultRekey_SaltMintFailureAbortsBeforeRewrite proves a random
// source failure during salt minting is fatal and surfaces to the
// caller; the vault file is not touched (the snapshot already exists
// — that is the operator's recovery artefact).
func TestVaultRekey_SaltMintFailureAbortsBeforeRewrite(t *testing.T) {
	t.Parallel()
	const currentPass = "correctbatterystaple"
	fx := newVaultRekeyRoundTripFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}}, currentPass)

	failingReader := iotest.ErrReader(errors.New("synthetic rand failure"))
	fx.deps.randReader = failingReader

	preBytes, err := os.ReadFile(fx.vaultPath)
	require.NoError(t, err)

	err = runVaultRekey(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.Error(t, err)
	require.False(t, errors.Is(err, errVaultRekeyPostWriteNotImplemented))

	postBytes, err := os.ReadFile(fx.vaultPath)
	require.NoError(t, err)
	require.True(t, bytes.Equal(preBytes, postBytes), "vault must not change when salt mint fails")
}

func TestMintFreshVaultSalt(t *testing.T) {
	t.Parallel()
	deps := &vaultDeps{randReader: rand.Reader}
	salt, err := mintFreshVaultSalt(deps)
	require.NoError(t, err)
	require.Len(t, salt, vaultSaltLen)
	salt2, err := mintFreshVaultSalt(deps)
	require.NoError(t, err)
	require.False(t, bytes.Equal(salt, salt2), "two crypto/rand draws must differ in 16 bytes")
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
