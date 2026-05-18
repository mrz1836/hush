package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

const (
	testGoodPassphrase     = "correctbatterystaple"
	testListenAddrInput    = "100.96.10.4:7743"
	testOwnerIDInput       = "123456789012345678"
	testApplicationIDIn    = "345678901234567890"
	testBotTokenInput      = "discord-bot-token-XYZ"
	testInitBinaryPath     = "/usr/local/bin/hush-test"
	testRandSeedBytes      = "random-bytes-for-tests-123456789012"
	testShortPassphrase    = "shortpass"
	testMismatchPassphrase = "differentpassphrase1"
)

// initFixture captures captures stdout, stderr, the FakeKeychain,
// and the initDeps used by an init invocation. Tests assemble one
// per scenario.
type initFixture struct {
	t         *testing.T
	stdout    *bytes.Buffer
	stderr    *bytes.Buffer
	stdoutS   *Stream
	stderrS   *Stream
	keychain  *keychain.FakeKeychain
	deps      *initDeps
	stdinFile *os.File
	tempDir   string
}

func newInitFixture(t *testing.T) *initFixture {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	stdoutS := newStream(stdout, false, true)
	stderrS := newStream(stderr, false, true)
	tmpDir := t.TempDir()
	// vault.Save enforces parent-mode 0700 (SDD-03); t.TempDir
	// returns 0755 by default.
	require.NoError(t, os.Chmod(tmpDir, 0o700))
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)

	stdin := dummyTTY(t)
	deps := &initDeps{
		keychain:         kc,
		binaryPath:       func() (string, error) { return testInitBinaryPath, nil },
		randReader:       newDeterministicReader(),
		stateDirRoot:     tmpDir,
		nowFn:            time.Now,
		platformACL:      func() bool { return true },
		isTTY:            func(_ *os.File) bool { return true },
		deriveMasterSeed: fastDeriveMasterSeed,
		promptSecret: scriptedSecretReader(t, []string{
			testGoodPassphrase, testGoodPassphrase, testBotTokenInput,
		}),
		promptLine: scriptedLineReader(t, []string{
			testListenAddrInput, testOwnerIDInput, testApplicationIDIn,
		}),
	}
	return &initFixture{
		t:         t,
		stdout:    stdout,
		stderr:    stderr,
		stdoutS:   stdoutS,
		stderrS:   stderrS,
		keychain:  kc,
		deps:      deps,
		stdinFile: stdin,
		tempDir:   tmpDir,
	}
}

func dummyTTY(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// scriptedSecretReader returns a promptSecret seam that yields the
// supplied passphrases in order. Each invocation pops the head.
func scriptedSecretReader(t *testing.T, secrets []string) func(*os.File, io.Writer, string) (*securebytes.SecureBytes, error) {
	t.Helper()
	idx := 0
	return func(_ *os.File, _ io.Writer, _ string) (*securebytes.SecureBytes, error) {
		if idx >= len(secrets) {
			return nil, fmt.Errorf("scripted secret reader exhausted at index %d", idx)
		}
		v := secrets[idx]
		idx++
		return securebytes.New([]byte(v))
	}
}

// scriptedLineReader returns a promptLine seam that yields the
// supplied non-secret strings in order.
func scriptedLineReader(t *testing.T, lines []string) func(*os.File, io.Writer, string) (string, error) {
	t.Helper()
	idx := 0
	return func(_ *os.File, _ io.Writer, _ string) (string, error) {
		if idx >= len(lines) {
			return "", fmt.Errorf("scripted line reader exhausted at index %d", idx)
		}
		v := lines[idx]
		idx++
		return v, nil
	}
}

// fastDeriveMasterSeed is a HMAC-SHA-256-based stand-in for
// keys.DeriveMasterSeed. It's deterministic on (passphrase, salt),
// fast (no Argon2id), and produces a 64-byte seed that
// keys.DeriveClientKey / keys.DeriveVaultEncKey accept. It enforces
// the same minimum-passphrase-length and salt-length contracts.
func fastDeriveMasterSeed(ctx context.Context, passphrase, salt []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(passphrase) < 12 {
		return nil, errors.New("passphrase too short")
	}
	if len(salt) != 16 {
		return nil, errors.New("salt wrong length")
	}
	first := sha256.Sum256(append(append([]byte("hush/init-test/0\x00"), passphrase...), salt...))
	second := sha256.Sum256(append(append([]byte("hush/init-test/1\x00"), passphrase...), salt...))
	out := make([]byte, 0, 64)
	out = append(out, first[:]...)
	out = append(out, second[:]...)
	return out, nil
}

// deterministicReader yields predictable bytes for tests that need
// to read from io.Reader; cycles a 32-byte pattern.
type deterministicReader struct {
	pattern []byte
	pos     int
}

func newDeterministicReader() *deterministicReader {
	return &deterministicReader{pattern: []byte(testRandSeedBytes)}
}

func (d *deterministicReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = d.pattern[d.pos%len(d.pattern)]
		d.pos++
	}
	return len(p), nil
}

// ---- Server-mode tests ----------------------------------------------------

func TestInitServer_RefusesShortPassphrase(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testShortPassphrase})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errPassphraseTooShort))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Contains(t, fx.stderr.String(), initMsgPassphraseTooShort)

	// No artifacts created.
	_, statErr := os.Stat(filepath.Join(fx.tempDir, "secrets.vault"))
	require.True(t, errors.Is(statErr, os.ErrNotExist))
	_, statErr = os.Stat(filepath.Join(fx.tempDir, "config.toml"))
	require.True(t, errors.Is(statErr, os.ErrNotExist))
	_, kcErr := fx.keychain.Retrieve(context.Background(), kcServiceVaultPassphrase, kcAccountServer)
	require.True(t, errors.Is(kcErr, keychain.ErrKeychainItemNotFound))
}

func TestInitServer_RejectsConfirmationMismatch(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{
		testGoodPassphrase, testMismatchPassphrase,
	})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errPassphraseMismatch))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Contains(t, fx.stderr.String(), initMsgPassphraseMismatch)

	_, statErr := os.Stat(filepath.Join(fx.tempDir, "secrets.vault"))
	require.True(t, errors.Is(statErr, os.ErrNotExist))
}

func TestInitServer_RejectsNonTTYStdin(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.isTTY = func(_ *os.File) bool { return false }

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errNoTTY))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Contains(t, fx.stderr.String(), initMsgNoTTY)
}

func TestInitServer_CreatesVaultWith0600(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	vaultPath := filepath.Join(fx.tempDir, "secrets.vault")
	info, err := os.Stat(vaultPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.Greater(t, info.Size(), int64(0))

	// Vault starts with the SDD-03 magic header bytes "HUSH".
	header := make([]byte, 4)
	f, err := os.Open(vaultPath)
	require.NoError(t, err)
	defer f.Close()
	_, err = io.ReadFull(f, header)
	require.NoError(t, err)
	require.Equal(t, []byte{0x48, 0x55, 0x53, 0x48}, header)
}

func TestInitServer_CreatesConfigWithAllDefaults(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	configPath := filepath.Join(fx.tempDir, "config.toml")
	info, err := os.Stat(configPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	body, err := os.ReadFile(configPath)
	require.NoError(t, err)
	got := string(body)

	// Every documented schema field must appear.
	wantFields := []string{
		"listen_addr", "path_prefix", "state_dir", "audit_log",
		"discord_owner_id", "client_registry",
		"bot_token_keychain_item", "application_id",
		"argon_time", "argon_memory_mb", "argon_threads",
		"jwt_default_ttl", "max_interactive_ttl", "max_supervisor_ttl",
		"default_max_uses", "nonce_ttl", "clock_skew",
		"claim_approval_timeout",
		"require_tailscale", "allowed_cidrs", "health_bind",
		"require_file_mode_checks", "require_keychain_acl",
		"require_ntp_sync", "max_clock_drift",
	}
	for _, k := range wantFields {
		require.Contains(t, got, k+" =", "missing field %q", k)
	}

	// Locked default values for the schema-default fields.
	require.Contains(t, got, fmt.Sprintf("argon_time = %d", config.DefaultArgonTime))
	require.Contains(t, got, fmt.Sprintf("argon_memory_mb = %d", config.DefaultArgonMemoryMB))
	require.Contains(t, got, fmt.Sprintf("argon_threads = %d", config.DefaultArgonThreads))
	require.Contains(t, got, "default_max_uses = 50")
	require.Contains(t, got, "bot_token_keychain_item = 'hush-discord'")
	require.Contains(t, got, "require_tailscale = true")
	require.Contains(t, got, "allowed_cidrs = ['100.64.0.0/10']")
}

func TestInitServer_StoresVaultPassphraseInKeychain(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	got, err := fx.keychain.Retrieve(context.Background(), kcServiceVaultPassphrase, kcAccountServer)
	require.NoError(t, err)
	defer got.Destroy()

	require.NoError(t, got.Use(func(b []byte) {
		require.Equal(t, testGoodPassphrase, string(b))
	}))
	require.Equal(t, testInitBinaryPath, fx.keychain.RecordedACL(kcServiceVaultPassphrase, kcAccountServer))
}

func TestInitServer_StoresBotTokenInKeychain(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	got, err := fx.keychain.Retrieve(context.Background(), "hush-discord", kcAccountServer)
	require.NoError(t, err)
	defer got.Destroy()

	require.NoError(t, got.Use(func(b []byte) {
		require.Equal(t, testBotTokenInput, string(b))
	}))
	require.Equal(t, testInitBinaryPath, fx.keychain.RecordedACL("hush-discord", kcAccountServer))
}

func TestInitServer_RefusesPreExistingVault(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	vaultPath := filepath.Join(fx.tempDir, "secrets.vault")
	originalBytes := []byte("preexisting-vault-bytes")
	require.NoError(t, os.WriteFile(vaultPath, originalBytes, 0o600))
	statBefore, _ := os.Stat(vaultPath)

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errVaultExists))
	require.Equal(t, ExitErr, mapErr(err))

	got, err := os.ReadFile(vaultPath)
	require.NoError(t, err)
	require.Equal(t, originalBytes, got)
	statAfter, _ := os.Stat(vaultPath)
	require.Equal(t, statBefore.ModTime(), statAfter.ModTime())
}

func TestInitServer_RefusesPreExistingConfig(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	configPath := filepath.Join(fx.tempDir, "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("preexisting"), 0o600))

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errConfigExists))
	require.Equal(t, ExitErr, mapErr(err))
	require.Contains(t, fx.stderr.String(), "config already exists")

	_, statErr := os.Stat(filepath.Join(fx.tempDir, "secrets.vault"))
	require.True(t, errors.Is(statErr, os.ErrNotExist), "vault must not be written when config exists")
}

func TestInitServer_RefusesPreExistingKeychainItem(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	prep, err := securebytes.New([]byte("preexisting-passphrase"))
	require.NoError(t, err)
	require.NoError(t, fx.keychain.Store(context.Background(), kcServiceVaultPassphrase, kcAccountServer, prep, "/abs/other"))
	require.NoError(t, prep.Destroy())

	err = runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errKeychainItemExists))
	require.Equal(t, ExitErr, mapErr(err))
	require.Contains(t, fx.stderr.String(), "keychain item already exists for service=hush-vault-passphrase account=hush-server")
}

func TestInitServer_PathPrefixGenerated12CharsURLSafe(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(fx.tempDir, "config.toml"))
	require.NoError(t, err)
	re := regexp.MustCompile(`(?m)^path_prefix = ['"]([A-Za-z0-9_-]{12})['"]`)
	matches := re.FindStringSubmatch(string(body))
	require.NotNil(t, matches, "expected path_prefix line in:\n%s", string(body))
	require.Len(t, matches[1], 12)
}

func TestInitServer_RoundTripsConfigViaLoadServer(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	configPath := filepath.Join(fx.tempDir, "config.toml")
	loaded, err := config.LoadServer(context.Background(), configPath)
	require.NoError(t, err)
	require.Equal(t, fx.tempDir, loaded.Server.StateDir)
	require.Equal(t, "hush-discord", loaded.Discord.BotTokenKeychainItem)
}

func TestInitServer_RefusesPlatformWithoutACL(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.platformACL = func() bool { return false }

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errPlatformACLUnsupported))
	require.Contains(t, fx.stderr.String(), "has no per-binary keychain ACL")

	_, statErr := os.Stat(filepath.Join(fx.tempDir, "secrets.vault"))
	require.True(t, errors.Is(statErr, os.ErrNotExist))
}

func TestInitServer_AtomicWriteConfigToml(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	configPath := filepath.Join(fx.tempDir, "config.toml")
	tmpPath := configPath + ".tmp"

	// Pre-create the .tmp path so OPEN+EXCL fails — simulating a
	// crashed prior run.
	require.NoError(t, os.WriteFile(tmpPath, []byte("leftover"), 0o600))

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.Error(t, err)
	_, statErr := os.Stat(configPath)
	require.True(t, errors.Is(statErr, os.ErrNotExist))
}

// ---- Client-mode tests ----------------------------------------------------

// runClientWithFlags wraps the cobra path so flag parsing exercises
// real cobra behavior.
func runClientWithFlags(ctx context.Context, fx *initFixture, machineIndex string) error {
	cmd := newInitClientCmd()
	cmd.SetArgs([]string{})
	if machineIndex != "" {
		require.NoError(fx.t, cmd.Flags().Set("machine-index", machineIndex))
	}
	return runInitClient(ctx, fx.stdoutS, fx.stderrS, fx.stdinFile, cmd, fx.deps)
}

func TestInitClient_RequiresMachineIndex(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})

	err := runClientWithFlags(context.Background(), fx, "")
	require.True(t, errors.Is(err, errMissingFlag))
	require.Equal(t, ExitInputErr, mapErr(err))
	require.Contains(t, fx.stderr.String(), initMsgMissingMachineIndex)

	// No keychain item created.
	_, kcErr := fx.keychain.Retrieve(context.Background(), kcServiceClient, "machine-0")
	require.True(t, errors.Is(kcErr, keychain.ErrKeychainItemNotFound))
}

func TestInitClient_RejectsNegativeMachineIndex(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)

	err := runClientWithFlags(context.Background(), fx, "-1")
	require.True(t, errors.Is(err, errMissingFlag))
	require.Contains(t, fx.stderr.String(), initMsgMachineIndexInvalid)
}

func TestInitClient_RejectsOversizedMachineIndex(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	overMax := strconv.FormatUint(uint64(^uint32(0))+1, 10)

	err := runClientWithFlags(context.Background(), fx, overMax)
	require.True(t, errors.Is(err, errMissingFlag))
	require.Contains(t, fx.stderr.String(), initMsgMachineIndexInvalid)
}

func TestInitClient_StoresInKeychainViaFake(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})

	err := runClientWithFlags(context.Background(), fx, "3")
	require.NoError(t, err)

	got, err := fx.keychain.Retrieve(context.Background(), kcServiceClient, "machine-3")
	require.NoError(t, err)
	defer got.Destroy()
	require.Equal(t, 32, got.Len())
	require.Equal(t, testInitBinaryPath, fx.keychain.RecordedACL(kcServiceClient, "machine-3"))
}

func TestInitClient_NonInteractiveRegistersClient(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.clientNonInteractive = true
	fx.deps.clientPassphrase = testGoodPassphrase
	registry := filepath.Join(fx.tempDir, "clients.json")
	fx.deps.clientRegistry = registry
	keyFile := filepath.Join(fx.tempDir, "client-machine-1.key")
	fx.deps.clientKeyFile = keyFile

	err := runClientWithFlags(context.Background(), fx, "1")
	require.NoError(t, err)

	got, err := fx.keychain.Retrieve(context.Background(), kcServiceClient, "machine-1")
	require.NoError(t, err)
	defer got.Destroy()
	require.Equal(t, 32, got.Len())

	raw, err := os.ReadFile(registry)
	require.NoError(t, err)
	var entries []clientRegistryJSONEntry
	require.NoError(t, json.Unmarshal(raw, &entries))
	require.Len(t, entries, 1)
	require.Regexp(t, `^[0-9a-f]{16}$`, entries[0].Fingerprint)
	require.Regexp(t, `^0[23][0-9a-f]{64}$`, entries[0].PublicKey)

	keyRaw, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	require.Regexp(t, `^[0-9a-f]{64}\n$`, string(keyRaw))
}

func TestInitClient_PrintsFingerprintOneLine(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})

	err := runClientWithFlags(context.Background(), fx, "0")
	require.NoError(t, err)

	out := fx.stdout.String()
	got := strings.TrimRight(out, "\n")
	require.Regexp(t, `^SHA256:[A-Za-z0-9+/]{43}$`, got)
	require.Equal(t, 50, len(got))
	require.Equal(t, "\n", out[len(got):])
	// Newline appears exactly once.
	require.Equal(t, 1, strings.Count(out, "\n"))
}

func TestInitClient_DeterministicAcrossRuns(t *testing.T) {
	t.Parallel()
	runOnce := func() string {
		fx := newInitFixture(t)
		fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})
		// Force the same salt across runs by using a fresh deterministic
		// reader seeded the same way.
		fx.deps.randReader = newDeterministicReader()
		err := runClientWithFlags(context.Background(), fx, "0")
		require.NoError(t, err)
		return fx.stdout.String()
	}
	first := runOnce()
	second := runOnce()
	require.Equal(t, first, second)
}

func TestInitClient_DistinctInputsProduceDistinctFingerprints(t *testing.T) {
	t.Parallel()
	fingerprintFor := func(passphrase, idx string) string {
		fx := newInitFixture(t)
		fx.deps.promptSecret = scriptedSecretReader(t, []string{passphrase, passphrase})
		fx.deps.randReader = newDeterministicReader()
		err := runClientWithFlags(context.Background(), fx, idx)
		require.NoError(t, err)
		return strings.TrimRight(fx.stdout.String(), "\n")
	}
	a0 := fingerprintFor("alpha-passphrase-1", "0")
	a1 := fingerprintFor("alpha-passphrase-1", "1")
	b0 := fingerprintFor("beta-passphrase-22", "0")
	require.NotEqual(t, a0, a1)
	require.NotEqual(t, a0, b0)
	require.NotEqual(t, a1, b0)
}

func TestInitClient_RefusesPreExistingKeychainItem(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	prep, err := securebytes.New([]byte("preexisting-machine-key"))
	require.NoError(t, err)
	require.NoError(t, fx.keychain.Store(context.Background(), kcServiceClient, "machine-3", prep, "/abs/other"))
	require.NoError(t, prep.Destroy())

	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})

	err = runClientWithFlags(context.Background(), fx, "3")
	require.True(t, errors.Is(err, errKeychainItemExists))
	require.Contains(t, fx.stderr.String(), "keychain item already exists for service=hush-client account=machine-3")
}

func TestInitClient_ConflictsWithServerMode(t *testing.T) {
	t.Parallel()
	// Mutual exclusivity is structural: the cobra command tree
	// separates the two subcommands; "hush init server client" is
	// rejected by cobra as an unknown command rather than by an
	// in-process flag combination check (research §6).
	root := newInitCmd()
	root.SetArgs([]string{"server", "client"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.Execute()
	require.Error(t, err)
}

func TestInitClient_RejectsConfirmationMismatch(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{
		testGoodPassphrase, testMismatchPassphrase,
	})

	err := runClientWithFlags(context.Background(), fx, "0")
	require.True(t, errors.Is(err, errPassphraseMismatch))
	require.Contains(t, fx.stderr.String(), initMsgPassphraseMismatch)
}

func TestInitClient_NoStderrOnSuccess(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})

	err := runClientWithFlags(context.Background(), fx, "0")
	require.NoError(t, err)

	// Stderr is empty (the scripted prompts never write echoes).
	require.Empty(t, fx.stderr.String())
	// Stdout is the fingerprint line only.
	require.Regexp(t, `^SHA256:[A-Za-z0-9+/]{43}\n$`, fx.stdout.String())
}

// ---- Sentinel-leak / passphrase-isolation tests --------------------------

func TestInitServer_NeverReadsPassphraseFromEnv(t *testing.T) {
	t.Setenv("HUSH_PASSPHRASE", testutil.SentinelSecret(15))
	t.Setenv("PASSPHRASE", testutil.SentinelSecret(15))
	fx := newInitFixture(t)

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	got, err := fx.keychain.Retrieve(context.Background(), kcServiceVaultPassphrase, kcAccountServer)
	require.NoError(t, err)
	defer got.Destroy()

	require.NoError(t, got.Use(func(b []byte) {
		require.Equal(t, testGoodPassphrase, string(b))
		require.NotContains(t, string(b), testutil.SentinelSecret(15))
	}))
}

func TestInitServer_NeverLeaksPassphraseToOutput(t *testing.T) {
	t.Parallel()
	sentinel := testutil.SentinelSecret(15) + "_pass"
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{
		sentinel, sentinel, "discord-bot-token-XYZ",
	})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	testutil.AssertSentinelAbsent(t, sentinel, fx.stdout.String())
	testutil.AssertSentinelAbsent(t, sentinel, fx.stderr.String())

	body, err := os.ReadFile(filepath.Join(fx.tempDir, "config.toml"))
	require.NoError(t, err)
	testutil.AssertSentinelAbsent(t, sentinel, string(body))
}

func TestInitServer_NeverLeaksBotTokenToOutput(t *testing.T) {
	t.Parallel()
	sentinelBot := testutil.SentinelSecret(15) + "_bot"
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{
		testGoodPassphrase, testGoodPassphrase, sentinelBot,
	})

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.NoError(t, err)

	testutil.AssertSentinelAbsent(t, sentinelBot, fx.stdout.String())
	testutil.AssertSentinelAbsent(t, sentinelBot, fx.stderr.String())

	body, err := os.ReadFile(filepath.Join(fx.tempDir, "config.toml"))
	require.NoError(t, err)
	testutil.AssertSentinelAbsent(t, sentinelBot, string(body))
}

func TestInitClient_NeverLeaksDerivedKeyToOutput(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})

	err := runClientWithFlags(context.Background(), fx, "0")
	require.NoError(t, err)

	got, err := fx.keychain.Retrieve(context.Background(), kcServiceClient, "machine-0")
	require.NoError(t, err)
	defer got.Destroy()

	require.NoError(t, got.Use(func(stored []byte) {
		// No 8-byte subsequence of the stored private key should
		// appear in stdout.
		out := fx.stdout.String()
		for offset := 0; offset+8 <= len(stored); offset++ {
			window := stored[offset : offset+8]
			require.False(t,
				bytes.Contains([]byte(out), window),
				"private-key window %x leaked at offset %d", window, offset)
		}
	}))
}

func TestInit_LintNoOsGetenv(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("init.go")
	require.NoError(t, err)
	require.NotContains(t, string(body), "os.Getenv",
		"init.go must not call os.Getenv (FR-001)")
}

func TestInit_NoPassphraseFlag(t *testing.T) {
	t.Parallel()
	for _, cmd := range []*cobra.Command{newInitServerCmd(), newInitClientCmd()} {
		cmd.Flags().VisitAll(func(p *pflag.Flag) {
			n := strings.ToLower(p.Name)
			require.NotContains(t, n, "pass", "flag %q smells like a secret", p.Name)
			require.NotContains(t, n, "secret", "flag %q smells like a secret", p.Name)
			// "key" substring would catch --keychain or similar; the
			// actual contract is "no flag whose value is a passphrase
			// or signing key", so we check explicit key-class names.
			require.NotEqual(t, "key", n, "flag %q smells like a secret", p.Name)
			require.NotEqual(t, "private-key", n, "flag %q smells like a secret", p.Name)
		})
	}
}

func TestInit_NeverGeneratesPassphrase(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("init.go")
	require.NoError(t, err)
	s := string(body)
	require.NotContains(t, s, "GeneratePassphrase")
	require.NotContains(t, s, "passphrase.Generate")
	require.NotRegexp(t, `Generate.*Pass`, s)
}

// ---- Helper / lower-level coverage -----------------------------------------

func TestSSHStyleFingerprint_StableLength(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase})

	err := runClientWithFlags(context.Background(), fx, "5")
	require.NoError(t, err)

	got := strings.TrimRight(fx.stdout.String(), "\n")
	require.Equal(t, 50, len(got))
	require.True(t, strings.HasPrefix(got, "SHA256:"))
}

func TestParseMachineIndex_AcceptsRange(t *testing.T) {
	t.Parallel()
	good := []string{"0", "1", "42", "4294967295"}
	for _, in := range good {
		_, err := parseMachineIndex(in)
		require.NoError(t, err, "input %q", in)
	}
	bad := []string{"-1", "abc", "4294967296", ""}
	for _, in := range bad {
		_, err := parseMachineIndex(in)
		require.Error(t, err, "input %q should fail", in)
	}
}
