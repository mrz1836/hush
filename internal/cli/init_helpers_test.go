package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

func mustSecureForTest(t *testing.T, b []byte) *securebytes.SecureBytes {
	t.Helper()
	sb, err := securebytes.New(b)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Destroy() })
	return sb
}

func openPTY(t *testing.T) (master, slave *os.File, err error) {
	t.Helper()
	master, slave, err = pty.Open()
	if err != nil {
		return nil, nil, err
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})
	return master, slave, nil
}

func TestReadLineFromTTY_FromPipe(t *testing.T) {
	t.Parallel()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	go func() {
		defer w.Close()
		_, _ = w.WriteString("operator-input\n")
	}()

	prompt := &strings.Builder{}
	got, err := readLineFromTTY(r, prompt, "Enter: ")
	require.NoError(t, err)
	require.Equal(t, "operator-input", got)
	require.Equal(t, "Enter: ", prompt.String())
}

func TestReadLineFromTTY_EmptyOnEOF(t *testing.T) {
	t.Parallel()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	w.Close()

	got, err := readLineFromTTY(r, io.Discard, "x")
	require.NoError(t, err)
	require.Equal(t, "", got)
}

func TestReadPassphraseTTY_NonTTYErrors(t *testing.T) {
	t.Parallel()
	r, _, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()

	_, err = readPassphraseTTY(r, io.Discard, "x")
	require.True(t, errors.Is(err, errNoTTY))
}

func TestReadPassphraseTTY_NilFile(t *testing.T) {
	t.Parallel()
	_, err := readPassphraseTTY(nil, io.Discard, "x")
	require.True(t, errors.Is(err, errNoTTY))
}

func TestWriteConfigTOMLAtomic_FreshWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	path := filepath.Join(dir, "config.toml")

	doc := buildServerDecodedFromDefaults(serverInputs{
		listenAddr:    "100.96.10.4:7743",
		pathPrefix:    "abcdefghijkl",
		ownerID:       "1",
		applicationID: "2",
		stateDir:      dir,
	})

	require.NoError(t, writeConfigTOMLAtomic(path, doc))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(body), "listen_addr")
}

func TestWriteConfigTOMLAtomic_TmpExistsRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	path := filepath.Join(dir, "config.toml")
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte("leftover"), 0o600))

	doc := buildServerDecodedFromDefaults(serverInputs{stateDir: dir})
	err := writeConfigTOMLAtomic(path, doc)
	require.Error(t, err)
}

func TestEnsureStateDir_CreatesAbsentDir(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o700))
	dir := filepath.Join(parent, "nested-state-dir")

	require.NoError(t, ensureStateDir(dir))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestEnsureStateDir_NonDirectoryFails(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o700))
	notDir := filepath.Join(parent, "notdir")
	require.NoError(t, os.WriteFile(notDir, []byte("x"), 0o600))

	err := ensureStateDir(notDir)
	require.Error(t, err)
}

func TestEnsureStateDir_AlreadyExists(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o700))
	require.NoError(t, ensureStateDir(parent))
}

func TestResolveStateDir_OverrideWins(t *testing.T) {
	t.Parallel()
	got, err := resolveStateDir("/tmp/test-override-x")
	require.NoError(t, err)
	require.Equal(t, "/tmp/test-override-x", got)
}

func TestResolveStateDir_DefaultExpands(t *testing.T) {
	t.Parallel()
	got, err := resolveStateDir("")
	require.NoError(t, err)
	require.NotContains(t, got, "~")
}

func TestGuardKeychainAbsent_PassesWhenAbsent(t *testing.T) {
	t.Parallel()
	stderr := newStream(io.Discard, false, true)
	fx := newInitFixture(t)

	require.NoError(t, guardKeychainAbsent(context.Background(), fx.keychain, "svc", "acct", stderr))
}

func TestNewInitCmd_HasSubcommands(t *testing.T) {
	t.Parallel()
	cmd := newInitCmd()
	require.Equal(t, "init", cmd.Use)
	require.NotNil(t, cmd, "init parent built")
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Use] = true
	}
	require.True(t, subs["server"])
	require.True(t, subs["client"])
}

func TestNewInitClientCmd_FlagDeclared(t *testing.T) {
	t.Parallel()
	cmd := newInitClientCmd()
	f := cmd.Flags().Lookup("machine-index")
	require.NotNil(t, f)
}

func TestSubtleEqual_KnownPairs(t *testing.T) {
	t.Parallel()
	require.True(t, subtleEqual([]byte("abc"), []byte("abc")))
	require.False(t, subtleEqual([]byte("abc"), []byte("abd")))
	require.False(t, subtleEqual([]byte("abc"), []byte("abcd")))
}

func TestProductionInitDeps_ReturnsLiveDeps(t *testing.T) {
	t.Parallel()
	deps, err := productionInitDeps()
	require.NoError(t, err)
	require.NotNil(t, deps)
	require.NotNil(t, deps.keychain)
	require.NotNil(t, deps.binaryPath)
	require.NotNil(t, deps.platformACL)
}

// brokenReader returns the supplied error on every Read.
type brokenReader struct{ err error }

func (b brokenReader) Read([]byte) (int, error) { return 0, b.err }

func TestRunInitServer_BinaryPathError(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.binaryPath = func() (string, error) { return "", errors.New("binary path missing") }

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
	require.Contains(t, err.Error(), "binary path")
}

func TestRunInitServer_RandReaderError(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.randReader = brokenReader{err: errors.New("entropy starved")}

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
	require.Contains(t, err.Error(), "salt")
}

func TestRunInitServer_PromptLineError(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptLine = func(_ *os.File, _ io.Writer, _ string) (string, error) {
		return "", errors.New("prompt line broken")
	}

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestRunInitServer_FirstSecretPromptError(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = func(_ *os.File, _ io.Writer, _ string) (*securebytes.SecureBytes, error) {
		return nil, errors.New("first prompt broken")
	}

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestRunInitServer_ConfirmPromptError(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	idx := 0
	fx.deps.promptSecret = func(_ *os.File, _ io.Writer, _ string) (*securebytes.SecureBytes, error) {
		idx++
		if idx == 1 {
			return securebytes.New([]byte(testGoodPassphrase))
		}
		return nil, errors.New("confirm broken")
	}

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestRunInitServer_OwnerIDLineError(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	idx := 0
	fx.deps.promptLine = func(_ *os.File, _ io.Writer, _ string) (string, error) {
		idx++
		if idx == 1 {
			return testListenAddrInput, nil
		}
		return "", errors.New("owner-id prompt broken")
	}

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestRunInitServer_AppIDLineError(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	idx := 0
	fx.deps.promptLine = func(_ *os.File, _ io.Writer, _ string) (string, error) {
		idx++
		if idx == 1 {
			return testListenAddrInput, nil
		}
		if idx == 2 {
			return testOwnerIDInput, nil
		}
		return "", errors.New("app-id prompt broken")
	}

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestRunInitServer_BotTokenPromptError(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	idx := 0
	fx.deps.promptSecret = func(_ *os.File, _ io.Writer, _ string) (*securebytes.SecureBytes, error) {
		idx++
		switch idx {
		case 1, 2:
			return securebytes.New([]byte(testGoodPassphrase))
		default:
			return nil, errors.New("bot-token prompt broken")
		}
	}

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestRunInitServer_PromptLineEmptyExhaustsAttempts(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptLine = func(_ *os.File, _ io.Writer, _ string) (string, error) {
		return "", nil
	}

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errMissingFlag))
	require.Contains(t, fx.stderr.String(), "is required")
}

func TestRunInitClient_BinaryPathError(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})
	fx.deps.binaryPath = func() (string, error) { return "", errors.New("binary path") }

	err := runClientWithFlags(context.Background(), fx, "0")
	require.Error(t, err)
	require.Contains(t, err.Error(), "binary path")
}

func TestRunInitClient_RandReaderError(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})
	fx.deps.randReader = brokenReader{err: errors.New("entropy")}

	err := runClientWithFlags(context.Background(), fx, "0")
	require.Error(t, err)
}

func TestRunInitClient_PlatformRefusesEarly(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.platformACL = func() bool { return false }

	err := runClientWithFlags(context.Background(), fx, "0")
	require.True(t, errors.Is(err, errPlatformACLUnsupported))
	require.Contains(t, fx.stderr.String(), "has no per-binary keychain ACL")
}

func TestRunInitClient_NonTTYStdin(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.isTTY = func(_ *os.File) bool { return false }

	err := runClientWithFlags(context.Background(), fx, "0")
	require.True(t, errors.Is(err, errNoTTY))
}

func TestRunInitClient_PassphraseTooShort(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{"short"})

	err := runClientWithFlags(context.Background(), fx, "0")
	require.True(t, errors.Is(err, errPassphraseTooShort))
}

func TestWriteConfigTOMLAtomic_OverwriteRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	path := filepath.Join(dir, "config.toml")

	// Write once then attempt to write again — second tmp-write
	// must fail (O_EXCL on the .tmp).
	doc := buildServerDecodedFromDefaults(serverInputs{stateDir: dir})
	require.NoError(t, writeConfigTOMLAtomic(path, doc))

	// Pre-populate the .tmp again to simulate a race.
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte("conflict"), 0o600))
	require.Error(t, writeConfigTOMLAtomic(path, doc))
}

func TestSerializeECPrivKey_Returns32Bytes(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})

	require.NoError(t, runClientWithFlags(context.Background(), fx, "1"))
	got, err := fx.keychain.Retrieve(context.Background(), kcServiceClient, "machine-1")
	require.NoError(t, err)
	defer got.Destroy()
	require.Equal(t, 32, got.Len())
}

func TestGeneratePathPrefix_ReadError(t *testing.T) {
	t.Parallel()
	_, err := generatePathPrefix(brokenReader{err: errors.New("entropy")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "path_prefix")
}

func TestNewInitCmd_PrintsHelpForBareInit(t *testing.T) {
	t.Parallel()
	root := newInitCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{})
	// Bare `init` has no Run; cobra exits cleanly without error
	// but emits help. Just ensure Execute returns without panic.
	_ = root.Execute()
}

func TestReadPassphraseTTY_PromptWriteFails(t *testing.T) {
	t.Parallel()
	r, _, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	_, err = readPassphraseTTY(r, io.Discard, "label: ")
	require.True(t, errors.Is(err, errNoTTY))
}

// TestReadPassphraseTTY_ViaPTY exercises the term.ReadPassword path
// against a PTY pair to bring readPassphraseTTY out of 0% coverage.
func TestReadPassphraseTTY_ViaPTY(t *testing.T) {
	t.Parallel()
	master, slave, err := openPTY(t)
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	go func() { _, _ = master.WriteString("via-pty-passphrase\n") }()

	prompt := &strings.Builder{}
	got, err := readPassphraseTTY(slave, prompt, "Pass: ")
	require.NoError(t, err)
	defer got.Destroy()

	require.NoError(t, got.Use(func(b []byte) {
		require.Equal(t, "via-pty-passphrase", string(b))
	}))
	require.Contains(t, prompt.String(), "Pass: ")
}

func TestWriteConfigTOMLAtomic_RenameFailsWhenTargetIsDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	target := filepath.Join(dir, "config.toml")
	require.NoError(t, os.MkdirAll(target, 0o700))

	doc := buildServerDecodedFromDefaults(serverInputs{stateDir: dir})
	err := writeConfigTOMLAtomic(target, doc)
	require.Error(t, err)
}

func TestNewInitCmd_RegistersUnderRoot(t *testing.T) {
	t.Parallel()
	out := &outputContext{
		stdout: newStream(io.Discard, false, true),
		stderr: newStream(io.Discard, false, true),
	}
	root := newRootCmd(out)
	var found bool
	for _, c := range root.Commands() {
		if c.Use == "init" {
			found = true
		}
	}
	require.True(t, found, "init command not registered on root")
}

// TestServerInputs_FillsAllFields ensures buildServerDecodedFromDefaults
// returns a document with every field populated.
func TestServerInputs_FillsAllFields(t *testing.T) {
	t.Parallel()
	got := buildServerDecodedFromDefaults(serverInputs{
		listenAddr:    "100.96.10.4:7743",
		pathPrefix:    "abcdef123456",
		ownerID:       "1",
		applicationID: "2",
		stateDir:      "/tmp",
	})
	require.Equal(t, "100.96.10.4:7743", got.Server.ListenAddr)
	require.Equal(t, "abcdef123456", got.Server.PathPrefix)
	require.Equal(t, "/tmp", got.Server.StateDir)
	require.Equal(t, "/tmp/audit.jsonl", got.Server.AuditLog)
	require.Equal(t, "/tmp/clients.json", got.Server.ClientRegistry)
	require.Equal(t, "1", got.Server.DiscordOwnerID)
	require.Equal(t, "hush-discord", got.Discord.BotTokenKeychainItem)
	require.Equal(t, "2", got.Discord.ApplicationID)
	require.True(t, got.Network.RequireTailscale)
	require.True(t, got.Security.RequireFileModeChecks)
	require.True(t, got.Security.RequireKeychainACL)
	require.True(t, got.Security.RequireNTPSync)
}

// TestServerInputs_DefaultsStateDirWhenEmpty exercises the
// stateDir == "" fallback.
func TestServerInputs_DefaultsStateDirWhenEmpty(t *testing.T) {
	t.Parallel()
	got := buildServerDecodedFromDefaults(serverInputs{})
	require.NotEmpty(t, got.Server.StateDir)
}

// TestSecureBytesEqual_DifferentLength returns (false, nil) when
// lengths differ — a length mismatch is a legitimate "values differ"
// answer, not a compare failure.
func TestSecureBytesEqual_DifferentLength(t *testing.T) {
	t.Parallel()
	a := mustSecureForTest(t, []byte("abcd"))
	b := mustSecureForTest(t, []byte("abcdef"))
	equal, err := secureBytesEqual(a, b)
	require.NoError(t, err)
	require.False(t, equal)
}

func TestSecureBytesEqual_SameContent(t *testing.T) {
	t.Parallel()
	a := mustSecureForTest(t, []byte("abcdef"))
	b := mustSecureForTest(t, []byte("abcdef"))
	equal, err := secureBytesEqual(a, b)
	require.NoError(t, err)
	require.True(t, equal)
}

// TestSecureBytesEqual_AfterDestroyReturnsError verifies that a Use
// failure (here: destroyed input) surfaces as an error rather than
// being collapsed into "values differ". The latter would mislead the
// operator into retyping their passphrase when the real fault is
// internal.
func TestSecureBytesEqual_AfterDestroyReturnsError(t *testing.T) {
	t.Parallel()
	a, _ := securebytes.New([]byte("abcdef"))
	b := mustSecureForTest(t, []byte("abcdef"))
	require.NoError(t, a.Destroy())
	equal, err := secureBytesEqual(a, b)
	require.Error(t, err)
	require.False(t, equal)
}

func TestGuardFileAbsent_ReturnsExistsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "thing")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	stderr := &strings.Builder{}
	s := newStream(stderr, false, true)
	got := guardFileAbsent(path, errVaultExists, "msg %s", s)
	require.True(t, errors.Is(got, errVaultExists))
}

func TestGuardFileAbsent_ReturnsNilOnAbsent(t *testing.T) {
	t.Parallel()
	stderr := newStream(io.Discard, false, true)
	got := guardFileAbsent("/no/such/path/at/all", errVaultExists, "x", stderr)
	require.NoError(t, got)
}

func TestRunInitServer_VaultExistsRefuses(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(fx.tempDir, "secrets.vault"), []byte("x"), 0o600))

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errVaultExists))
}

func TestRunInitServer_HappyPathBotTokenInKeychainHasACL(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	require.NoError(t, runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps))

	require.Equal(t, testInitBinaryPath, fx.keychain.RecordedACL("hush-discord", kcAccountServer))
	require.Equal(t, testInitBinaryPath, fx.keychain.RecordedACL(kcServiceVaultPassphrase, kcAccountServer))
	require.Contains(t, fx.stderr.String(), initMsgServerComplete)
}

func TestNewInitServerCmd_HasRunE(t *testing.T) {
	t.Parallel()
	cmd := newInitServerCmd()
	require.NotNil(t, cmd.RunE)
	require.Equal(t, "server", cmd.Use)
}

func TestNewInitClientCmd_HasRunE(t *testing.T) {
	t.Parallel()
	cmd := newInitClientCmd()
	require.NotNil(t, cmd.RunE)
	require.Equal(t, "client", cmd.Use)
}

// brokenWriter returns the supplied error on every Write.
type brokenWriter struct{ err error }

func (b brokenWriter) Write([]byte) (int, error) { return 0, b.err }

func TestReadPassphraseTTY_PromptWriterErrorAfterTTYCheck(t *testing.T) {
	t.Parallel()
	master, slave, err := openPTY(t)
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	go func() { _, _ = master.WriteString("ok\n") }()

	_, err = readPassphraseTTY(slave, brokenWriter{err: io.ErrShortWrite}, "Pass: ")
	require.Error(t, err)
}

func TestReadLineFromTTY_PromptWriterErrors(t *testing.T) {
	t.Parallel()
	r, _, _ := os.Pipe()
	defer r.Close()
	_, err := readLineFromTTY(r, brokenWriter{err: io.ErrShortWrite}, "x")
	require.Error(t, err)
}

func TestRunInitServer_CancelledContextDuringDerive(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runInitServer(ctx, fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
}

func TestRunInitClient_CancelledContextDuringDerive(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runClientWithFlags(ctx, fx, "0")
	require.Error(t, err)
}

func TestDefaultIsTTY_NilReturnsFalse(t *testing.T) {
	t.Parallel()
	require.False(t, defaultIsTTY(nil))
}

func TestNewInitClientCmd_ExecuteHitsRunE(t *testing.T) {
	t.Parallel()
	// Drives newInitClientCmd's RunE through cobra so the closure
	// statements are exercised. Stdin is /dev/null (non-tty) so
	// the actual keychain Store is never reached.
	cmd := newInitClientCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--machine-index", "0"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	require.Error(t, err) // expected: errNoTTY since real stdin in test is not a TTY
}

func TestNewInitServerCmd_ExecuteHitsRunE(t *testing.T) {
	t.Parallel()
	cmd := newInitServerCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	require.Error(t, err) // expected: errNoTTY
}

func TestSerializeECPrivKey_NonEmptyResult(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})
	require.NoError(t, runClientWithFlags(context.Background(), fx, "9"))
	got, err := fx.keychain.Retrieve(context.Background(), kcServiceClient, "machine-9")
	require.NoError(t, err)
	defer got.Destroy()
	require.NoError(t, got.Use(func(b []byte) {
		require.Len(t, b, 32)
		// At least one non-zero byte.
		nonzero := false
		for _, x := range b {
			if x != 0 {
				nonzero = true
				break
			}
		}
		require.True(t, nonzero)
	}))
}
