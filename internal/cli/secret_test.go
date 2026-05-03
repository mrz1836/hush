package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// secretSentinel is the leak-scan sentinel — appears as an entry value
// in many of the fixtures below. Tests assert this string never appears
// in stdout, stderr, audit log records, or error strings.
//
//nolint:gochecknoglobals // immutable test sentinel; identical pattern to existing test_sentinels_test.go
var secretSentinel = testutil.SentinelSecret(17)

// secretFixture captures stdout/stderr buffers, the in-memory log
// handler, the test vault, and the secretDeps for one verb invocation.
type secretFixture struct {
	t          *testing.T
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
	stdoutS    *Stream
	stderrS    *Stream
	stdoutFile *os.File // for stdout-TTY detection seam (real file when isStdoutTTY stub is overridden)
	logBuf     *bytes.Buffer
	deps       *secretDeps
	vaultPath  string
	vaultKey   *securebytes.SecureBytes
	stdinFile  *os.File
	tempDir    string
}

// newSecretFixture returns a fixture with an empty vault. Tests
// override individual deps fields and seed entries via the seedEntries
// helper before invoking a runSecret* function.
func newSecretFixture(t *testing.T, entries []testutil.VaultEntry) *secretFixture {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	stdoutS := newStream(stdout, false, true)
	stderrS := newStream(stderr, false, true)
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	stdin := dummyTTY(t)

	var vaultPath string
	var vaultKey *securebytes.SecureBytes
	if entries == nil {
		vaultPath, vaultKey, _ = testutil.NewTestVaultDetailed(t, []testutil.VaultEntry{})
	} else {
		vaultPath, vaultKey, _ = testutil.NewTestVaultDetailed(t, entries)
	}
	stateDir := filepath.Dir(vaultPath)

	// Rename "test.vault" → "secrets.vault" so resolveVaultPath finds
	// the file under the deps.stateDirRoot seam.
	canonical := filepath.Join(stateDir, secretsVaultFilename)
	require.NoError(t, os.Rename(vaultPath, canonical))
	vaultPath = canonical

	// Capture the seed that testutil.NewTestVaultDetailed used to
	// derive the vault key. The deriveMasterSeed seam returns this
	// seed verbatim so the runSecret* code's DeriveVaultEncKey call
	// produces the same key the on-disk vault was sealed with.
	frozenSeed := testutil.NewTestKeys(t)

	deps := &secretDeps{
		loadSecrets:      vault.LoadSecrets,
		saveVault:        vault.Save,
		promptPassphrase: scriptedSecretReader(t, []string{"correctbatterystaple"}),
		promptSecret:     scriptedSecretReader(t, []string{"value-typed", "value-typed"}),
		promptLine:       scriptedLineReader(t, []string{"description"}),
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

	return &secretFixture{
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

// fsErrNotExist returns os.ErrNotExist via a wrapper so the file is
// linkable without a top-level import of io/fs.
func fsErrNotExist() error { return os.ErrNotExist }

// secureBytesContent returns the content of a SecureBytes as a byte
// slice — for test assertions only.
func secureBytesContent(t *testing.T, sb *securebytes.SecureBytes) []byte {
	t.Helper()
	var out []byte
	require.NoError(t, sb.Use(func(b []byte) {
		out = make([]byte, len(b))
		copy(out, b)
	}))
	return out
}

// readVaultEntries decrypts the on-disk vault at path using key and
// returns the names + descriptions only (values are destroyed
// immediately).
func readVaultEntries(t *testing.T, path string, key *securebytes.SecureBytes) []testutil.VaultEntry {
	t.Helper()
	secrets, err := vault.LoadSecrets(context.Background(), path, key)
	require.NoError(t, err)
	out := make([]testutil.VaultEntry, 0, len(secrets))
	for _, s := range secrets {
		val := ""
		if s.Value != nil {
			val = string(secureBytesContent(t, s.Value))
			_ = s.Value.Destroy()
		}
		out = append(out, testutil.VaultEntry{Name: s.Name, Description: s.Description, Value: val})
	}
	return out
}

// ----------------------- Foundational tests -----------------------

func TestSecret_HelpDoesNotMentionValueFlags(t *testing.T) {
	t.Parallel()
	verbs := []string{"add", "remove", "list", "rotate"}
	banned := []string{"--value", "--secret", "--password", "--description", "--force", "--yes", "--no-confirm"}
	for _, v := range verbs {
		root := newSecretCmd()
		buf := &bytes.Buffer{}
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{v, "--help"})
		_ = root.Execute()
		out := buf.String()
		for _, b := range banned {
			require.NotContainsf(t, out, b, "verb %s help mentions banned flag %s", v, b)
		}
	}
}

func TestSecret_RootMounts(t *testing.T) {
	t.Parallel()
	cmd := newSecretCmd()
	require.Equal(t, "secret", cmd.Use)
	require.Nil(t, cmd.RunE)
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		// take only the first space-delimited token (Use is "add NAME")
		name := c.Use
		if idx := strings.Index(name, " "); idx >= 0 {
			name = name[:idx]
		}
		subs[name] = true
	}
	require.True(t, subs["add"])
	require.True(t, subs["remove"])
	require.True(t, subs["list"])
	require.True(t, subs["rotate"])
	require.Len(t, subs, 4)
}

func TestSecret_RegistersUnderRoot(t *testing.T) {
	t.Parallel()
	out := &outputContext{
		stdout: newStream(io.Discard, false, true),
		stderr: newStream(io.Discard, false, true),
	}
	root := newRootCmd(out)
	var found bool
	for _, c := range root.Commands() {
		if c.Use == "secret" {
			found = true
		}
	}
	require.True(t, found)
}

func TestSecret_NoSecretFlagsDeclared(t *testing.T) {
	t.Parallel()
	cmds := []*cobra.Command{
		newSecretAddCmd(),
		newSecretRemoveCmd(),
		newSecretListCmd(),
		newSecretRotateCmd(),
	}
	for _, c := range cmds {
		c.Flags().VisitAll(func(p *pflag.Flag) {
			n := strings.ToLower(p.Name)
			require.NotContains(t, n, "value")
			require.NotContains(t, n, "secret")
			require.NotContains(t, n, "password")
		})
	}
}

func TestSecret_ValidateSecretName(t *testing.T) {
	t.Parallel()
	good := []string{"FOO", "F", "FOO_BAR", "_FOO", "ANTHROPIC_API_KEY", "A1B2"}
	for _, n := range good {
		require.NoErrorf(t, validateSecretName(n), "name=%q", n)
	}
	bad := []string{"", "foo", "1FOO", "FOO-BAR", "FOO BAR", strings.Repeat("A", 65), "FOO!"}
	for _, n := range bad {
		err := validateSecretName(n)
		require.Errorf(t, err, "name=%q", n)
		require.True(t, errors.Is(err, errInvalidSecretName))
	}
}

// ----------------------- US1 Add tests -----------------------

func TestSecret_AddRefusesPipedStdin(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.isStdinTTY = func(_ *os.File) bool { return false }

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"NAME"})
	require.True(t, errors.Is(err, errNoTTY))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Equal(t, secretMsgNoTTY+"\n", fx.stderr.String())

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes))
	require.Contains(t, fx.logBuf.String(), "secret_tty_refused")
	require.Contains(t, fx.logBuf.String(), "verb=add")
}

func TestSecret_AddRefusesValueFlag(t *testing.T) {
	t.Parallel()
	root := newSecretCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"add", "--value", "foo", "NAME"})
	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown flag")
}

func TestSecret_AddRefusesSecretFlag(t *testing.T) {
	t.Parallel()
	root := newSecretCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"add", "--secret", "foo", "NAME"})
	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown flag")
}

func TestSecret_AddRefusesPasswordFlag(t *testing.T) {
	t.Parallel()
	root := newSecretCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"add", "--password", "foo", "NAME"})
	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown flag")
}

func TestSecret_AddInvalidName(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	loadCalled := false
	fx.deps.loadSecrets = func(_ context.Context, _ string, _ *securebytes.SecureBytes) ([]vault.Secret, error) {
		loadCalled = true
		return nil, errSyntheticTest
	}

	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"foo"})
	require.True(t, errors.Is(err, errInvalidSecretName))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Equal(t, secretMsgInvalidName+"\n", fx.stderr.String())
	require.False(t, loadCalled)
}

func TestSecret_AddTTYHappyPath(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{"new-secret-value", "new-secret-value"})
	fx.deps.promptLine = scriptedLineReader(t, []string{"my description"})

	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"ANTHROPIC_API_KEY"})
	require.NoError(t, err)

	entries := readVaultEntries(t, fx.vaultPath, fx.vaultKey)
	require.Len(t, entries, 1)
	require.Equal(t, "ANTHROPIC_API_KEY", entries[0].Name)
	require.Equal(t, "my description", entries[0].Description)
	require.Equal(t, "new-secret-value", entries[0].Value)

	info, err := os.Stat(fx.vaultPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.Contains(t, fx.logBuf.String(), "secret_added")
	require.Contains(t, fx.logBuf.String(), "name=ANTHROPIC_API_KEY")
}

func TestSecret_AddConfirmationMismatch(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{"secret123", "secret124"})

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.True(t, errors.Is(err, errSecretValueMismatch))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Equal(t, secretMsgValueMismatch+"\n", fx.stderr.String())

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes))
	require.Contains(t, fx.logBuf.String(), "secret_confirmation_mismatch")
	require.Contains(t, fx.logBuf.String(), "outcome=value_mismatch")
}

func TestSecret_AddDuplicateRefuses(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{
		{Name: "EXISTING_KEY", Description: "preexisting", Value: secretSentinel},
	})
	fx.deps.promptSecret = scriptedSecretReader(t, []string{"new-value", "new-value"})
	fx.deps.promptLine = scriptedLineReader(t, []string{"new desc"})

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"EXISTING_KEY"})
	require.True(t, errors.Is(err, errSecretExists))
	require.Equal(t, ExitErr, mapErr(err))

	want := fmt.Sprintf(secretMsgExistsFmt+"\n", "EXISTING_KEY")
	require.Equal(t, want, fx.stderr.String())
	testutil.AssertSentinelAbsent(t, secretSentinel, fx.stderr.String())

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes))
}

func TestSecret_AddPassphraseFailureSurfacesAuthCode(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.loadSecrets = func(_ context.Context, _ string, _ *securebytes.SecureBytes) ([]vault.Secret, error) {
		return nil, fmt.Errorf("decrypt: %w", vault.ErrAuthFailed)
	}

	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.True(t, errors.Is(err, vault.ErrAuthFailed))
	require.Equal(t, ExitAuth, mapErr(err))
	require.Contains(t, fx.logBuf.String(), "secret_passphrase_failed")
}

// ----------------------- US2 List tests -----------------------

func TestSecret_ListRefusesPipedStdin(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.isStdinTTY = func(_ *os.File) bool { return false }

	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.True(t, errors.Is(err, errNoTTY))
	require.Equal(t, secretMsgNoTTY+"\n", fx.stderr.String())
}

func TestSecret_ListNoValues(t *testing.T) {
	t.Parallel()
	for _, isTTY := range []bool{true, false} {
		t.Run(fmt.Sprintf("isTTY=%v", isTTY), func(t *testing.T) {
			t.Parallel()
			fx := newSecretFixture(t, []testutil.VaultEntry{
				{Name: "ALPHA", Description: "alpha desc", Value: "a-value"},
				{Name: "BRAVO", Description: "", Value: secretSentinel},
				{Name: "CHARLIE", Description: "charlie desc", Value: "c-value"},
			})
			fx.deps.isStdoutTTY = func(_ *os.File) bool { return isTTY }

			err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
			require.NoError(t, err)

			testutil.AssertSentinelAbsent(t, secretSentinel, fx.stdout.String())
			testutil.AssertSentinelAbsent(t, secretSentinel, fx.stderr.String())
			testutil.AssertSentinelAbsent(t, secretSentinel, fx.logBuf.String())
		})
	}
}

func TestSecret_ListJSONOutput(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{
		{Name: "FOO", Description: "thing one", Value: "v1"},
		{Name: "GITHUB_TOKEN", Description: "", Value: "v2"},
	})
	fx.deps.isStdoutTTY = func(_ *os.File) bool { return false }

	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.NoError(t, err)

	want := `[{"name":"FOO","description":"thing one"},{"name":"GITHUB_TOKEN","description":""}]` + "\n"
	require.Equal(t, want, fx.stdout.String())

	// Confirm the JSON has exactly two keys per object.
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(fx.stdout.String())), &got))
	for _, obj := range got {
		require.Len(t, obj, 2)
		_, hasName := obj["name"]
		_, hasDesc := obj["description"]
		require.True(t, hasName)
		require.True(t, hasDesc)
	}
}

func TestSecret_ListTTYOutput(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{
		{Name: "FOO", Description: "thing one", Value: "v1"},
		{Name: "GITHUB_TOKEN", Description: "", Value: "v2"},
	})
	fx.deps.isStdoutTTY = func(_ *os.File) bool { return true }

	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.NoError(t, err)

	want := "FOO — thing one\nGITHUB_TOKEN\n"
	require.Equal(t, want, fx.stdout.String())
}

func TestSecret_ListSortedAscending(t *testing.T) {
	t.Parallel()
	for _, isTTY := range []bool{true, false} {
		t.Run(fmt.Sprintf("isTTY=%v", isTTY), func(t *testing.T) {
			t.Parallel()
			fx := newSecretFixture(t, []testutil.VaultEntry{
				{Name: "ZULU", Description: "z", Value: "z"},
				{Name: "ALPHA", Description: "a", Value: "a"},
				{Name: "MIKE", Description: "m", Value: "m"},
			})
			fx.deps.isStdoutTTY = func(_ *os.File) bool { return isTTY }

			err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
			require.NoError(t, err)

			out := fx.stdout.String()
			alphaIdx := strings.Index(out, "ALPHA")
			mikeIdx := strings.Index(out, "MIKE")
			zuluIdx := strings.Index(out, "ZULU")
			require.True(t, alphaIdx < mikeIdx && mikeIdx < zuluIdx, "expected ALPHA<MIKE<ZULU in %q", out)
		})
	}
}

func TestSecret_ListEmptyVault(t *testing.T) {
	t.Parallel()
	t.Run("TTY", func(t *testing.T) {
		t.Parallel()
		fx := newSecretFixture(t, nil)
		fx.deps.isStdoutTTY = func(_ *os.File) bool { return true }

		err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
		require.NoError(t, err)
		require.Empty(t, fx.stdout.String())
		require.Equal(t, secretMsgEmptyVault+"\n", fx.stderr.String())
	})
	t.Run("Pipe", func(t *testing.T) {
		t.Parallel()
		fx := newSecretFixture(t, nil)
		fx.deps.isStdoutTTY = func(_ *os.File) bool { return false }

		err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
		require.NoError(t, err)
		require.Equal(t, "[]\n", fx.stdout.String())
		require.Empty(t, fx.stderr.String())
	})
}

func TestSecret_ListAuditNotEmittedOnSuccess(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	fx.deps.isStdoutTTY = func(_ *os.File) bool { return false }

	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.NoError(t, err)
	require.NotContains(t, fx.logBuf.String(), "vault_listed")
	require.NotContains(t, fx.logBuf.String(), "secret_listed")
}

// ----------------------- US3 Rotate tests -----------------------

func TestSecret_RotateRefusesPipedStdin(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.isStdinTTY = func(_ *os.File) bool { return false }

	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errNoTTY))
}

func TestSecret_RotateAtomic(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{
		{Name: "ALPHA", Description: "a", Value: "v1"},
		{Name: "BRAVO", Description: "", Value: "v2"},
		{Name: "CHARLIE", Description: "c", Value: "v3"},
	})
	preBytes, _ := os.ReadFile(fx.vaultPath)

	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.False(t, bytes.Equal(preBytes, postBytes), "ciphertext should differ after rotate")

	got := readVaultEntries(t, fx.vaultPath, fx.vaultKey)
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
	require.Equal(t, []testutil.VaultEntry{
		{Name: "ALPHA", Description: "a", Value: "v1"},
		{Name: "BRAVO", Description: "", Value: "v2"},
		{Name: "CHARLIE", Description: "c", Value: "v3"},
	}, got)

	info, err := os.Stat(fx.vaultPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.Contains(t, fx.logBuf.String(), "vault_rotated")
	require.Contains(t, fx.logBuf.String(), "outcome=success")
}

func TestSecret_RotateSendsSIGHUP(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})

	pidPath := filepath.Join(fx.tempDir, pidFilename)
	require.NoError(t, os.WriteFile(pidPath, []byte("4242\n"), 0o600))

	var killCalls []struct {
		pid int
		sig syscall.Signal
	}
	fx.deps.kill = func(pid int, sig syscall.Signal) error {
		killCalls = append(killCalls, struct {
			pid int
			sig syscall.Signal
		}{pid, sig})
		return nil
	}
	fx.deps.readPIDFile = os.ReadFile

	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	// Probe (kill 0) + SIGHUP delivery → 2 invocations.
	require.Len(t, killCalls, 2)
	require.Equal(t, 4242, killCalls[0].pid)
	require.Equal(t, syscall.Signal(0), killCalls[0].sig)
	require.Equal(t, 4242, killCalls[1].pid)
	require.Equal(t, syscall.SIGHUP, killCalls[1].sig)

	require.Contains(t, fx.stderr.String(), "signalled running server (pid=4242)")
	require.Contains(t, fx.logBuf.String(), "signalled=true")
}

func TestSecret_RotateMissingPIDTolerant(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	preBytes, _ := os.ReadFile(fx.vaultPath)

	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)
	require.Equal(t, secretMsgPidAbsent+"\n", fx.stderr.String())

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.False(t, bytes.Equal(preBytes, postBytes))
	require.Contains(t, fx.logBuf.String(), "signalled=false")
}

func TestSecret_RotateStalePIDTolerant(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})

	pidPath := filepath.Join(fx.tempDir, pidFilename)
	require.NoError(t, os.WriteFile(pidPath, []byte("999999\n"), 0o600))

	fx.deps.readPIDFile = os.ReadFile
	fx.deps.kill = func(_ int, _ syscall.Signal) error { return syscall.ESRCH }

	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)
	require.Equal(t, secretMsgPidStale+"\n", fx.stderr.String())
}

func TestSecret_RotateUnreadablePIDTolerant(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})

	pidPath := filepath.Join(fx.tempDir, pidFilename)
	require.NoError(t, os.WriteFile(pidPath, []byte("not-a-number\n"), 0o600))

	fx.deps.readPIDFile = os.ReadFile

	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)
	require.Equal(t, secretMsgPidUnreadable+"\n", fx.stderr.String())
}

func TestSecret_RotateNotOurUserTolerant(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})

	pidPath := filepath.Join(fx.tempDir, pidFilename)
	require.NoError(t, os.WriteFile(pidPath, []byte("1\n"), 0o600))

	fx.deps.readPIDFile = os.ReadFile
	fx.deps.kill = func(_ int, _ syscall.Signal) error { return syscall.EPERM }

	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)
	require.Equal(t, secretMsgPidNotOurUser+"\n", fx.stderr.String())
}

// ----------------------- US4 Remove tests -----------------------

func TestSecret_RemoveRefusesPipedStdin(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.isStdinTTY = func(_ *os.File) bool { return false }

	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.True(t, errors.Is(err, errNoTTY))
}

func TestSecret_RemoveAtomic(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{
		{Name: "FOO", Description: "f", Value: "v1"},
		{Name: "BAR", Description: "b", Value: "v2"},
	})
	fx.deps.promptLine = scriptedLineReader(t, []string{"FOO"})

	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.NoError(t, err)

	got := readVaultEntries(t, fx.vaultPath, fx.vaultKey)
	require.Len(t, got, 1)
	require.Equal(t, "BAR", got[0].Name)

	info, err := os.Stat(fx.vaultPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// No stale .tmp lingers.
	tmp := fx.vaultPath + ".tmp"
	_, statErr := os.Stat(tmp)
	require.True(t, errors.Is(statErr, os.ErrNotExist))

	require.Contains(t, fx.logBuf.String(), "secret_removed")
	require.Contains(t, fx.logBuf.String(), "name=FOO")
}

func TestSecret_RemoveAbsent(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	promptCalled := false
	fx.deps.promptLine = func(_ *os.File, _ io.Writer, _ string) (string, error) {
		promptCalled = true
		return "", errSyntheticTest
	}

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"NOPE"})
	require.True(t, errors.Is(err, vault.ErrSecretNotFound))
	require.Equal(t, ExitNotFound, mapErr(err))
	require.False(t, promptCalled, "confirmation prompt must NOT fire before not-found check")

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes))
}

func TestSecret_RemoveTokenMismatch(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Description: "f", Value: "v"}})
	fx.deps.promptLine = scriptedLineReader(t, []string{"foo"})

	preBytes, _ := os.ReadFile(fx.vaultPath)
	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.True(t, errors.Is(err, errConfirmationMismatch))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Equal(t, secretMsgRemoveTokenMismatch+"\n", fx.stderr.String())

	postBytes, _ := os.ReadFile(fx.vaultPath)
	require.True(t, bytes.Equal(preBytes, postBytes))
	require.Contains(t, fx.logBuf.String(), "secret_confirmation_mismatch")
	require.Contains(t, fx.logBuf.String(), "verb=remove")
}

// ----------------------- Cross-cutting -----------------------

func TestSecret_AuditLogOmitsSecretBytes(t *testing.T) {
	t.Parallel()

	// add path
	fx := newSecretFixture(t, nil)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{secretSentinel + "_add", secretSentinel + "_add"})
	fx.deps.promptLine = scriptedLineReader(t, []string{"a description"})
	require.NoError(t, runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"}))
	testutil.AssertSentinelAbsent(t, secretSentinel+"_add", fx.logBuf.String())
	testutil.AssertSentinelAbsent(t, secretSentinel+"_add", fx.stdout.String())
	testutil.AssertSentinelAbsent(t, secretSentinel+"_add", fx.stderr.String())

	// remove path
	fx2 := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: secretSentinel + "_remove"}})
	fx2.deps.promptLine = scriptedLineReader(t, []string{"FOO"})
	require.NoError(t, runSecretRemove(context.Background(), fx2.stderrS, fx2.stdinFile, fx2.deps, []string{"FOO"}))
	testutil.AssertSentinelAbsent(t, secretSentinel+"_remove", fx2.logBuf.String())
	testutil.AssertSentinelAbsent(t, secretSentinel+"_remove", fx2.stdout.String())
	testutil.AssertSentinelAbsent(t, secretSentinel+"_remove", fx2.stderr.String())

	// list path
	fx3 := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: secretSentinel + "_list"}})
	fx3.deps.isStdoutTTY = func(_ *os.File) bool { return false }
	require.NoError(t, runSecretList(context.Background(), fx3.stdoutS, fx3.stderrS, fx3.stdinFile, nil, fx3.deps))
	testutil.AssertSentinelAbsent(t, secretSentinel+"_list", fx3.logBuf.String())
	testutil.AssertSentinelAbsent(t, secretSentinel+"_list", fx3.stdout.String())
	testutil.AssertSentinelAbsent(t, secretSentinel+"_list", fx3.stderr.String())

	// rotate path
	fx4 := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: secretSentinel + "_rotate"}})
	require.NoError(t, runSecretRotate(context.Background(), fx4.stderrS, fx4.stdinFile, fx4.deps))
	testutil.AssertSentinelAbsent(t, secretSentinel+"_rotate", fx4.logBuf.String())
	testutil.AssertSentinelAbsent(t, secretSentinel+"_rotate", fx4.stdout.String())
	testutil.AssertSentinelAbsent(t, secretSentinel+"_rotate", fx4.stderr.String())
}

func TestSecret_ErrorsDoNotLeakSecretBytes(t *testing.T) {
	t.Parallel()
	const sentinel = "SECRET_SHOULD_NEVER_APPEAR_17_LEAK"

	// TTY refusal
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: sentinel}})
	fx.deps.isStdinTTY = func(_ *os.File) bool { return false }
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"BAR"})
	require.Error(t, err)
	testutil.AssertSentinelAbsent(t, sentinel, fx.stderr.String())
	testutil.AssertSentinelAbsent(t, sentinel, err.Error())

	// invalid name
	fx = newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: sentinel}})
	err = runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"foo"})
	require.Error(t, err)
	testutil.AssertSentinelAbsent(t, sentinel, fx.stderr.String())
	testutil.AssertSentinelAbsent(t, sentinel, err.Error())

	// add confirmation mismatch
	fx = newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: sentinel}})
	fx.deps.promptSecret = scriptedSecretReader(t, []string{"a", "b"})
	err = runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"BAR"})
	require.Error(t, err)
	testutil.AssertSentinelAbsent(t, sentinel, fx.stderr.String())
	testutil.AssertSentinelAbsent(t, sentinel, err.Error())

	// duplicate add
	fx = newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Description: "d", Value: sentinel}})
	fx.deps.promptSecret = scriptedSecretReader(t, []string{"x", "x"})
	fx.deps.promptLine = scriptedLineReader(t, []string{"new"})
	err = runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
	testutil.AssertSentinelAbsent(t, sentinel, fx.stderr.String())
	testutil.AssertSentinelAbsent(t, sentinel, err.Error())

	// remove not found
	fx = newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: sentinel}})
	err = runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"NOPE"})
	require.Error(t, err)
	testutil.AssertSentinelAbsent(t, sentinel, fx.stderr.String())
	testutil.AssertSentinelAbsent(t, sentinel, err.Error())

	// remove confirmation mismatch
	fx = newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: sentinel}})
	fx.deps.promptLine = scriptedLineReader(t, []string{"foo"})
	err = runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
	testutil.AssertSentinelAbsent(t, sentinel, fx.stderr.String())
	testutil.AssertSentinelAbsent(t, sentinel, err.Error())

	// rotate stale PID
	fx = newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: sentinel}})
	require.NoError(t, os.WriteFile(filepath.Join(fx.tempDir, pidFilename), []byte("999999\n"), 0o600))
	fx.deps.readPIDFile = os.ReadFile
	fx.deps.kill = func(_ int, _ syscall.Signal) error { return syscall.ESRCH }
	err = runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)
	testutil.AssertSentinelAbsent(t, sentinel, fx.stderr.String())
}

func TestSecret_FileModeAfterAdd(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{"v", "v"})
	fx.deps.promptLine = scriptedLineReader(t, []string{"d"})

	require.NoError(t, runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"}))

	info, err := os.Stat(fx.vaultPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestSecret_FileModeAfterRotate(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	require.NoError(t, runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps))

	info, err := os.Stat(fx.vaultPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// ----------------------- Helper coverage -----------------------

func TestSecret_ProductionDeps_NotNil(t *testing.T) {
	t.Parallel()
	d := productionSecretDeps()
	require.NotNil(t, d.loadSecrets)
	require.NotNil(t, d.saveVault)
	require.NotNil(t, d.promptPassphrase)
	require.NotNil(t, d.promptSecret)
	require.NotNil(t, d.promptLine)
	require.NotNil(t, d.isStdinTTY)
	require.NotNil(t, d.isStdoutTTY)
	require.NotNil(t, d.deriveMasterSeed)
	require.NotNil(t, d.readVaultSalt)
	require.NotNil(t, d.kill)
	require.NotNil(t, d.readPIDFile)
	require.NotNil(t, d.logger)
	require.NotNil(t, d.nowFn)
}

func TestSecret_ProbePIDFile_AllBranches(t *testing.T) {
	t.Parallel()

	// pidAbsent
	deps := &secretDeps{
		readPIDFile: func(_ string) ([]byte, error) { return nil, os.ErrNotExist },
		kill:        func(_ int, _ syscall.Signal) error { return nil },
	}
	got, _ := probePIDFile(deps, "/no/such/path")
	require.Equal(t, pidAbsent, got)

	// pidUnreadable (read error)
	deps = &secretDeps{
		readPIDFile: func(_ string) ([]byte, error) { return nil, errSyntheticTest },
		kill:        func(_ int, _ syscall.Signal) error { return nil },
	}
	got, _ = probePIDFile(deps, "/anything")
	require.Equal(t, pidUnreadable, got)

	// pidUnreadable (parse error)
	deps = &secretDeps{
		readPIDFile: func(_ string) ([]byte, error) { return []byte("xx\n"), nil },
		kill:        func(_ int, _ syscall.Signal) error { return nil },
	}
	got, _ = probePIDFile(deps, "/anything")
	require.Equal(t, pidUnreadable, got)

	// pidUnreadable (zero PID)
	deps = &secretDeps{
		readPIDFile: func(_ string) ([]byte, error) { return []byte("0\n"), nil },
		kill:        func(_ int, _ syscall.Signal) error { return nil },
	}
	got, _ = probePIDFile(deps, "/anything")
	require.Equal(t, pidUnreadable, got)

	// pidStale (ESRCH)
	deps = &secretDeps{
		readPIDFile: func(_ string) ([]byte, error) { return []byte("4242"), nil },
		kill:        func(_ int, _ syscall.Signal) error { return syscall.ESRCH },
	}
	got, gotPid := probePIDFile(deps, "/anything")
	require.Equal(t, pidStale, got)
	require.Equal(t, 4242, gotPid)

	// pidNotOurUser (EPERM)
	deps = &secretDeps{
		readPIDFile: func(_ string) ([]byte, error) { return []byte("4242"), nil },
		kill:        func(_ int, _ syscall.Signal) error { return syscall.EPERM },
	}
	got, _ = probePIDFile(deps, "/anything")
	require.Equal(t, pidNotOurUser, got)

	// pidStale (other error)
	deps = &secretDeps{
		readPIDFile: func(_ string) ([]byte, error) { return []byte("4242"), nil },
		kill:        func(_ int, _ syscall.Signal) error { return errSyntheticTest },
	}
	got, _ = probePIDFile(deps, "/anything")
	require.Equal(t, pidStale, got)

	// pidPresent
	deps = &secretDeps{
		readPIDFile: func(_ string) ([]byte, error) { return []byte("4242"), nil },
		kill:        func(_ int, _ syscall.Signal) error { return nil },
	}
	got, gotPid = probePIDFile(deps, "/anything")
	require.Equal(t, pidPresent, got)
	require.Equal(t, 4242, gotPid)
}

// ----------------------- Error-path coverage -----------------------

func TestSecret_AddPromptPassphraseError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.promptPassphrase = func(_ *os.File, _ io.Writer, _ string) (*securebytes.SecureBytes, error) {
		return nil, errSyntheticTest
	}
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_AddReadVaultSaltError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.readVaultSalt = func(_ string) ([]byte, error) { return nil, errSyntheticTest }
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_AddDeriveSeedError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.deriveMasterSeed = func(_ context.Context, _, _ []byte) ([]byte, error) {
		return nil, errSyntheticTest
	}
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_AddPromptValueError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	idx := 0
	fx.deps.promptSecret = func(_ *os.File, _ io.Writer, _ string) (*securebytes.SecureBytes, error) {
		idx++
		return nil, errSyntheticTest
	}
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_AddPromptConfirmError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	idx := 0
	fx.deps.promptSecret = func(_ *os.File, _ io.Writer, _ string) (*securebytes.SecureBytes, error) {
		idx++
		if idx == 1 {
			return securebytes.New([]byte("v"))
		}
		return nil, errSyntheticTest
	}
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_AddPromptDescriptionError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{"v", "v"})
	fx.deps.promptLine = func(_ *os.File, _ io.Writer, _ string) (string, error) {
		return "", errSyntheticTest
	}
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_AddSaveFails(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{"v", "v"})
	fx.deps.promptLine = scriptedLineReader(t, []string{""})
	fx.deps.saveVault = func(_ context.Context, _ string, _ *securebytes.SecureBytes, _ []vault.Secret) error {
		return errSyntheticTest
	}
	err := runSecretAdd(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_RemoveInvalidName(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"foo"})
	require.True(t, errors.Is(err, errInvalidSecretName))
}

func TestSecret_RemovePromptPassphraseError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.promptPassphrase = func(_ *os.File, _ io.Writer, _ string) (*securebytes.SecureBytes, error) {
		return nil, errSyntheticTest
	}
	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_RemoveDeriveSeedError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.deriveMasterSeed = func(_ context.Context, _, _ []byte) ([]byte, error) {
		return nil, errSyntheticTest
	}
	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_RemoveReadSaltError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.readVaultSalt = func(_ string) ([]byte, error) { return nil, errSyntheticTest }
	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_RemovePromptLineError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	fx.deps.promptLine = func(_ *os.File, _ io.Writer, _ string) (string, error) {
		return "", errSyntheticTest
	}
	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_RemoveAuthFailed(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.loadSecrets = func(_ context.Context, _ string, _ *securebytes.SecureBytes) ([]vault.Secret, error) {
		return nil, fmt.Errorf("decrypt: %w", vault.ErrAuthFailed)
	}
	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.True(t, errors.Is(err, vault.ErrAuthFailed))
}

func TestSecret_RemoveSaveFails(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	fx.deps.promptLine = scriptedLineReader(t, []string{"FOO"})
	fx.deps.saveVault = func(_ context.Context, _ string, _ *securebytes.SecureBytes, _ []vault.Secret) error {
		return errSyntheticTest
	}
	err := runSecretRemove(context.Background(), fx.stderrS, fx.stdinFile, fx.deps, []string{"FOO"})
	require.Error(t, err)
}

func TestSecret_ListAuthFailed(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.loadSecrets = func(_ context.Context, _ string, _ *securebytes.SecureBytes) ([]vault.Secret, error) {
		return nil, fmt.Errorf("decrypt: %w", vault.ErrAuthFailed)
	}
	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.True(t, errors.Is(err, vault.ErrAuthFailed))
}

func TestSecret_ListPromptError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.promptPassphrase = func(_ *os.File, _ io.Writer, _ string) (*securebytes.SecureBytes, error) {
		return nil, errSyntheticTest
	}
	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.Error(t, err)
}

func TestSecret_ListReadSaltError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.readVaultSalt = func(_ string) ([]byte, error) { return nil, errSyntheticTest }
	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.Error(t, err)
}

func TestSecret_ListDeriveSeedError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.deriveMasterSeed = func(_ context.Context, _, _ []byte) ([]byte, error) {
		return nil, errSyntheticTest
	}
	err := runSecretList(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, nil, fx.deps)
	require.Error(t, err)
}

func TestSecret_RotatePromptError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.promptPassphrase = func(_ *os.File, _ io.Writer, _ string) (*securebytes.SecureBytes, error) {
		return nil, errSyntheticTest
	}
	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestSecret_RotateReadSaltError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.readVaultSalt = func(_ string) ([]byte, error) { return nil, errSyntheticTest }
	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestSecret_RotateDeriveSeedError(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.deriveMasterSeed = func(_ context.Context, _, _ []byte) ([]byte, error) {
		return nil, errSyntheticTest
	}
	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestSecret_RotateAuthFailed(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, nil)
	fx.deps.loadSecrets = func(_ context.Context, _ string, _ *securebytes.SecureBytes) ([]vault.Secret, error) {
		return nil, fmt.Errorf("decrypt: %w", vault.ErrAuthFailed)
	}
	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, vault.ErrAuthFailed))
}

func TestSecret_RotateSaveFails(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	fx.deps.saveVault = func(_ context.Context, _ string, _ *securebytes.SecureBytes, _ []vault.Secret) error {
		return errSyntheticTest
	}
	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestSecret_RotateKillFailsAfterPidPresent(t *testing.T) {
	t.Parallel()
	fx := newSecretFixture(t, []testutil.VaultEntry{{Name: "FOO", Value: "v"}})
	pidPath := filepath.Join(fx.tempDir, pidFilename)
	require.NoError(t, os.WriteFile(pidPath, []byte("4242\n"), 0o600))

	fx.deps.readPIDFile = os.ReadFile
	calls := 0
	fx.deps.kill = func(_ int, sig syscall.Signal) error {
		calls++
		// kill 0 → success (pidPresent), then SIGHUP delivery → error
		if sig == syscall.SIGHUP {
			return errSyntheticTest
		}
		return nil
	}

	err := runSecretRotate(context.Background(), fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)
	require.Equal(t, secretMsgPidStale+"\n", fx.stderr.String())
	require.Equal(t, 2, calls)
}

func TestSecret_AddListSaveSecretsHasZeroLen(t *testing.T) {
	t.Parallel()
	// Confirms the `len(secrets) == 0` boundary in destroySecrets is
	// safe (nothing to destroy).
	destroySecrets(nil)
	destroySecrets([]vault.Secret{})
}

func TestSecret_CobraRunE_Add_HitsRunE(t *testing.T) {
	t.Parallel()
	cmd := newSecretAddCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"FOO"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	// Real production path: stdin is not a TTY in this test process,
	// so we get errNoTTY (or earlier).
	require.Error(t, err)
}

func TestSecret_CobraRunE_Remove_HitsRunE(t *testing.T) {
	t.Parallel()
	cmd := newSecretRemoveCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"FOO"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	require.Error(t, err)
}

func TestSecret_CobraRunE_List_HitsRunE(t *testing.T) {
	t.Parallel()
	cmd := newSecretListCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	require.Error(t, err)
}

func TestSecret_CobraRunE_Rotate_HitsRunE(t *testing.T) {
	t.Parallel()
	cmd := newSecretRotateCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	require.Error(t, err)
}

func TestSecret_NameLengthBoundary(t *testing.T) {
	t.Parallel()
	// 64-char name must pass; 65-char must fail.
	require.NoError(t, validateSecretName(strings.Repeat("A", 64)))
	require.Error(t, validateSecretName(strings.Repeat("A", 65)))
}

func TestSecret_DestroySecrets_LIFO(t *testing.T) {
	t.Parallel()
	a, _ := securebytes.New([]byte("aaa"))
	b, _ := securebytes.New([]byte("bbb"))
	secrets := []vault.Secret{{Name: "A", Value: a}, {Name: "B", Value: b}}
	destroySecrets(secrets)
	// idempotent on already-destroyed
	destroySecrets(secrets)
}

// ---------------- Lint checks ----------------

func TestSecret_LintNoOsGetenv(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("secret.go")
	require.NoError(t, err)
	require.NotContains(t, string(body), "os.Getenv",
		"secret.go must not call os.Getenv (FR-001 + research R10)")
}
