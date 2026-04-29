package config

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// injectStateDir replaces __STATE_DIR__ in src with dir and writes to a temp file.
func injectStateDir(t *testing.T, src, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(src) //nolint:gosec // G304: src is a testdata fixture path controlled by the test, not user input
	require.NoError(t, err)
	content := strings.ReplaceAll(string(raw), "__STATE_DIR__", dir)
	dst := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(dst, []byte(content), 0o600)) //nolint:gosec // G703: dst is in t.TempDir(), safe for test use
	return dst
}

// loadWithStateDir loads src fixture with a fresh state dir injected.
func loadWithStateDir(t *testing.T, fixture string) (*Server, error) {
	t.Helper()
	dir := t.TempDir()
	cfg := injectStateDir(t, fixture, dir)
	return LoadServer(context.Background(), cfg)
}

// ---- Positive (happy-path) tests -------------------------------------------

func TestServer_FullMinimalConfig(t *testing.T) {
	t.Parallel()
	s, err := loadWithStateDir(t, "testdata/valid/minimal-valid.toml")
	require.NoError(t, err)
	require.NotNil(t, s)

	// Every documented default must be present.
	assert.Equal(t, DefaultArgonTime, s.Crypto.ArgonTime)
	assert.Equal(t, DefaultArgonMemoryMB, s.Crypto.ArgonMemoryMB)
	assert.Equal(t, DefaultArgonThreads, s.Crypto.ArgonThreads)
	assert.Equal(t, DefaultJWTTTL, s.Crypto.JWTDefaultTTL)
	assert.Equal(t, DefaultMaxInteractiveTTL, s.Crypto.MaxInteractiveTTL)
	assert.Equal(t, DefaultMaxSupervisorTTL, s.Crypto.MaxSupervisorTTL)
	assert.Equal(t, DefaultMaxUses, s.Crypto.DefaultMaxUses)
	assert.Equal(t, DefaultNonceTTL, s.Crypto.NonceTTL)
	assert.Equal(t, DefaultClockSkew, s.Crypto.ClockSkew)

	assert.True(t, s.Network.RequireTailscale)
	assert.Equal(t, DefaultAllowedCIDRs, s.Network.AllowedCIDRs)

	assert.True(t, s.Security.RequireFileModeChecks)
	assert.True(t, s.Security.RequireKeychainACL)
	assert.True(t, s.Security.RequireNTPSync)
	assert.Equal(t, DefaultMaxClockDrift, s.Security.MaxClockDrift)

	// Keychain item name populated (not a token value).
	assert.Equal(t, "hush-discord", s.Discord.BotTokenKeychainItem)
}

func TestServer_FullMaximalConfig(t *testing.T) {
	t.Parallel()
	s, err := loadWithStateDir(t, "testdata/valid/full-maximal.toml")
	require.NoError(t, err)
	require.NotNil(t, s)

	// Every non-default value must be preserved unchanged.
	assert.Equal(t, uint32(8), s.Crypto.ArgonTime)
	assert.Equal(t, uint32(512), s.Crypto.ArgonMemoryMB)
	assert.Equal(t, uint8(8), s.Crypto.ArgonThreads)
	assert.Equal(t, 4*time.Hour, s.Crypto.JWTDefaultTTL)
	assert.Equal(t, 10*time.Hour, s.Crypto.MaxInteractiveTTL)
	assert.Equal(t, 16*time.Hour, s.Crypto.MaxSupervisorTTL)
	assert.Equal(t, 100, s.Crypto.DefaultMaxUses)
	assert.Equal(t, 120*time.Second, s.Crypto.NonceTTL)
	assert.Equal(t, 45*time.Second, s.Crypto.ClockSkew)

	assert.False(t, s.Security.RequireKeychainACL)
	assert.False(t, s.Security.RequireNTPSync)
	assert.Equal(t, 30*time.Second, s.Security.MaxClockDrift)

	assert.Equal(t, "hush-prod-discord", s.Discord.BotTokenKeychainItem)
	assert.Equal(t, "234567890123456789", s.Server.DiscordAuditChannelID)

	// health_bind explicitly set — different from listen_addr
	wantHB := netip.MustParseAddrPort("100.127.255.1:8081")
	assert.Equal(t, wantHB, s.Network.HealthBind)
}

func TestLoadServer_AppliesEveryDocumentedDefault(t *testing.T) {
	t.Parallel()

	// Build a config that omits every optional field; load it and check each
	// default is populated.
	s, err := loadWithStateDir(t, "testdata/valid/minimal-valid.toml")
	require.NoError(t, err)

	tests := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"ArgonTime", s.Crypto.ArgonTime, DefaultArgonTime},
		{"ArgonMemoryMB", s.Crypto.ArgonMemoryMB, DefaultArgonMemoryMB},
		{"ArgonThreads", s.Crypto.ArgonThreads, DefaultArgonThreads},
		{"JWTDefaultTTL", s.Crypto.JWTDefaultTTL, DefaultJWTTTL},
		{"MaxInteractiveTTL", s.Crypto.MaxInteractiveTTL, DefaultMaxInteractiveTTL},
		{"MaxSupervisorTTL", s.Crypto.MaxSupervisorTTL, DefaultMaxSupervisorTTL},
		{"DefaultMaxUses", s.Crypto.DefaultMaxUses, DefaultMaxUses},
		{"NonceTTL", s.Crypto.NonceTTL, DefaultNonceTTL},
		{"ClockSkew", s.Crypto.ClockSkew, DefaultClockSkew},
		{"RequireTailscale", s.Network.RequireTailscale, DefaultRequireTailscale},
		{"RequireFileModeChecks", s.Security.RequireFileModeChecks, DefaultRequireFileModeChecks},
		{"RequireKeychainACL", s.Security.RequireKeychainACL, DefaultRequireKeychainACL},
		{"RequireNTPSync", s.Security.RequireNTPSync, DefaultRequireNTPSync},
		{"MaxClockDrift", s.Security.MaxClockDrift, DefaultMaxClockDrift},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.got)
		})
	}
}

func TestLoadServer_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/valid/minimal-valid.toml", dir)

	s1, err := LoadServer(context.Background(), cfg)
	require.NoError(t, err)
	s2, err := LoadServer(context.Background(), cfg)
	require.NoError(t, err)

	// Compare exported fields only (reflect.DeepEqual includes unexported fields).
	assert.Equal(t, s1.Server, s2.Server)
	assert.Equal(t, s1.Discord, s2.Discord)
	assert.Equal(t, s1.Crypto, s2.Crypto)
	assert.Equal(t, s1.Network, s2.Network)
	assert.Equal(t, s1.Security, s2.Security)
}

func TestLoadServer_ContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/valid/minimal-valid.toml", dir)
	s, err := LoadServer(ctx, cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestServer_ExpandsTildePathsCorrectly(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	// Write a minimal config that uses ~ for state_dir. The state_dir must
	// exist, so we use t.TempDir() but express it via its real path.
	dir := t.TempDir()
	// If TempDir is under HOME, we can use a ~ path; otherwise just use absolute.
	var rawStateDir string
	if strings.HasPrefix(dir, home+"/") {
		rawStateDir = "~/" + strings.TrimPrefix(dir, home+"/")
	} else {
		rawStateDir = dir
	}

	content := "[server]\n" +
		"listen_addr = \"100.96.10.4:7743\"\n" +
		"path_prefix = \"a8k2f9\"\n" +
		"state_dir = \"" + rawStateDir + "\"\n" +
		"audit_log = \"" + dir + "/audit.jsonl\"\n" +
		"discord_owner_id = \"123456789012345678\"\n" +
		"\n[discord]\n" +
		"bot_token_keychain_item = \"hush-discord\"\n" +
		"application_id = \"345678901234567890\"\n"

	cfg := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0o600))

	s, err := LoadServer(context.Background(), cfg)
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(s.Server.StateDir), "StateDir must be absolute")
	assert.Equal(t, dir, s.Server.StateDir)
}

func TestServer_DoesNotExpandEnvVars(t *testing.T) {
	t.Parallel()
	// $HOME in state_dir is NOT expanded — treated as a literal directory name.
	// The directory won't exist, so we expect ErrStateDirNotFound.
	content := "[server]\n" +
		"listen_addr = \"100.96.10.4:7743\"\n" +
		"path_prefix = \"a8k2f9\"\n" +
		"state_dir = \"$HOME/should-not-exist\"\n" +
		"discord_owner_id = \"123456789012345678\"\n" +
		"\n[discord]\n" +
		"bot_token_keychain_item = \"hush-discord\"\n" +
		"application_id = \"345678901234567890\"\n"

	cfg := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0o600))

	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrStateDirNotFound)
}

// ---- Decode-phase rejection tests ------------------------------------------

func TestServer_RejectsUnknownField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/unknown-field.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrUnknownField)
}

func TestServer_RejectsWrongType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/wrong-type.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrTOMLDecode)
}

func TestServer_RejectsBadDuration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/bad-duration.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrInvalidDuration)
}

// ---- Path-safety tests (state_dir) -----------------------------------------

func TestServer_RejectsMissingStateDir(t *testing.T) {
	t.Parallel()
	// Use a directory path that does not exist.
	dir := t.TempDir()
	nonexistent := filepath.Join(dir, "does-not-exist")
	cfg := injectStateDir(t, "testdata/valid/minimal-valid.toml", nonexistent)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrStateDirNotFound)
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestServer_RejectsStateDirNotADirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a regular file and use it as state_dir.
	regularFile := filepath.Join(dir, "notadir")
	require.NoError(t, os.WriteFile(regularFile, []byte("x"), 0o600))
	cfg := injectStateDir(t, "testdata/valid/minimal-valid.toml", regularFile)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrStateDirUnsafe)
}

func TestServer_LoaderDoesNotCreateStateDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	nonexistent := filepath.Join(dir, "should-not-be-created")
	cfg := injectStateDir(t, "testdata/valid/minimal-valid.toml", nonexistent)
	_, _ = LoadServer(context.Background(), cfg)
	_, statErr := os.Stat(nonexistent)
	assert.ErrorIs(t, statErr, fs.ErrNotExist, "loader must not create state_dir")
}

// ---- US6: no secrets in struct / no env reads ------------------------------

func TestServer_SchemaHasNoSecretFields(t *testing.T) { //nolint:gocognit // reflection-based structural test; complexity is necessary
	t.Parallel()
	// Reflect over all exported string fields of Server and its sub-sections.
	// Assert no field name contains Token/Passphrase/Secret/Password/Key
	// UNLESS the name ends with "KeychainItem" (the safe-pointer marker).
	forbidden := []string{"Token", "Passphrase", "Secret", "Password"}
	safeMarker := "KeychainItem"

	var checkStruct func(t *testing.T, rv reflect.Value, path string)
	checkStruct = func(t *testing.T, rv reflect.Value, path string) {
		t.Helper()
		rt := rv.Type()
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			if !field.IsExported() {
				continue
			}
			fullPath := path + "." + field.Name
			if field.Type.Kind() == reflect.Struct {
				checkStruct(t, rv.Field(i), fullPath)
				continue
			}
			if field.Type.Kind() == reflect.String {
				for _, bad := range forbidden {
					if strings.Contains(field.Name, bad) && !strings.HasSuffix(field.Name, safeMarker) {
						t.Errorf("field %s contains secret-like name %q without %q suffix", fullPath, bad, safeMarker)
					}
				}
			}
		}
	}

	var s Server
	checkStruct(t, reflect.ValueOf(s), "Server")
}

func TestLoadServer_DoesNotReadSecretsFromEnv(t *testing.T) { //nolint:gocognit // reflection-based env-leak audit; complexity is necessary
	// Cannot use t.Parallel — t.Setenv requires a non-parallel test.
	t.Setenv("HUSH_DISCORD_TOKEN", "leaked-token-value-12345")
	t.Setenv("HUSH_VAULT_PASSPHRASE", "leaked-passphrase-67890")
	t.Setenv("HUSH_BOT_TOKEN", "another-leak-value-99999")

	leaked := []string{
		"leaked-token-value-12345",
		"leaked-passphrase-67890",
		"another-leak-value-99999",
	}

	s, err := loadWithStateDir(t, "testdata/valid/minimal-valid.toml")
	require.NoError(t, err)

	var checkStruct func(t *testing.T, rv reflect.Value)
	checkStruct = func(t *testing.T, rv reflect.Value) {
		t.Helper()
		rt := rv.Type()
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			if !field.IsExported() {
				continue
			}
			fv := rv.Field(i)
			if field.Type.Kind() == reflect.Struct {
				checkStruct(t, fv)
				continue
			}
			if field.Type.Kind() == reflect.String {
				val := fv.String()
				for _, leak := range leaked {
					assert.NotEqual(t, leak, val, "field %s must not contain leaked env value", field.Name)
				}
			}
		}
	}
	checkStruct(t, reflect.ValueOf(*s))
}

func TestServer_DiscordBotTokenIsKeychainItemName(t *testing.T) {
	t.Parallel()
	s, err := loadWithStateDir(t, "testdata/valid/minimal-valid.toml")
	require.NoError(t, err)
	// The value must match what the TOML fixture specifies — a Keychain item name.
	assert.Equal(t, "hush-discord", s.Discord.BotTokenKeychainItem)
	// It must NOT look like an actual Discord bot token (those are long opaque strings).
	assert.Less(t, len(s.Discord.BotTokenKeychainItem), 50,
		"BotTokenKeychainItem should be a short item name, not a long token")
}

func TestPackageHasNoEnvVarReadsForSecretFields(t *testing.T) { //nolint:gocognit // source-scan test; complexity is inherent to the scan loop
	t.Parallel()
	// Walk all *.go source files in the package and assert zero os.Getenv /
	// os.LookupEnv calls. The only permitted env-reading call is os.UserHomeDir.
	dir := "."
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	forbidden := []string{"os.Getenv(", "os.LookupEnv("}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue // test files may call Getenv via t.Setenv
		}
		path := filepath.Join(dir, entry.Name())
		f, err := os.Open(path) //nolint:gosec // path is a known-safe testdata directory entry
		require.NoError(t, err)
		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			for _, bad := range forbidden {
				if strings.Contains(line, bad) {
					t.Errorf("%s:%d: forbidden call %q found", path, lineNum, bad)
				}
			}
		}
		require.NoError(t, f.Close())
	}
}

// ---- Regression: every invalid fixture returns a sentinel error -------------

func TestLoadServer_AllErrorsAreSentinels(t *testing.T) {
	t.Parallel()

	allSentinels := []error{
		ErrTOMLDecode, ErrUnknownField, ErrMissingRequiredField, ErrInvalidDuration,
		ErrTailscaleBindRequired, ErrListenLoopback, ErrListenUnspecified,
		ErrListenPublic, ErrListenMalformed, ErrTailscaleRequired,
		ErrPathPrefixInvalid, ErrAuditLogEscape,
		ErrStateDirNotFound, ErrStateDirUnsafe,
		ErrArgonMemoryTooLow, ErrArgonTimeTooLow, ErrArgonThreadsTooLow,
		ErrArgonMemoryTooHigh, ErrArgonTimeTooHigh, ErrArgonThreadsTooHigh,
		ErrSupervisorTTLOutOfRange,
		ErrConfigFileMode,
	}

	isKnown := func(err error) bool {
		for _, s := range allSentinels {
			if errors.Is(err, s) {
				return true
			}
		}
		return false
	}

	// Additionally check fs.ErrNotExist (for state-dir-missing fixtures).
	isKnownOrFSErr := func(err error) bool {
		return isKnown(err) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, context.Canceled)
	}

	entries, err := os.ReadDir("testdata/invalid")
	require.NoError(t, err)

	dir := t.TempDir()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()
			fixture := filepath.Join("testdata/invalid", entry.Name())
			cfg := injectStateDir(t, fixture, dir)
			s, err := LoadServer(context.Background(), cfg)
			assert.Nil(t, s, "must return nil *Server on error")
			require.Error(t, err)
			assert.True(t, isKnownOrFSErr(err), "error %v must be a known sentinel", err)
		})
	}
}

// TestLoadServer_FileNotFound checks that a missing config file returns a
// non-nil error (not a sentinel, but an fs-level error).
func TestLoadServer_FileNotFound(t *testing.T) {
	t.Parallel()
	s, err := LoadServer(context.Background(), "/nonexistent/path/config.toml")
	assert.Nil(t, s)
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

// ---- health_bind inheritance ------------------------------------------------

func TestServer_HealthBindInheritsListenAddr(t *testing.T) {
	t.Parallel()
	s, err := loadWithStateDir(t, "testdata/valid/minimal-valid.toml")
	require.NoError(t, err)
	// When health_bind is absent, it inherits listen_addr.
	assert.Equal(t, s.Server.ListenAddr, s.Network.HealthBind)
}

// ---- Config file permissions (Q5) ------------------------------------------

// configWithMode writes the minimal-valid fixture with stateDir injected at
// the given mode and returns the path. Used by the file-mode test set.
func configWithMode(t *testing.T, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile("testdata/valid/minimal-valid.toml")
	require.NoError(t, err)
	content := strings.ReplaceAll(string(raw), "__STATE_DIR__", dir)
	dst := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(dst, []byte(content), mode)) //nolint:gosec // explicit mode under test
	require.NoError(t, os.Chmod(dst, mode))                      // overcome umask if needed
	return dst
}

func TestLoadServer_RejectsLooseConfigPerms(t *testing.T) {
	t.Parallel()
	for _, mode := range []os.FileMode{0o644, 0o660, 0o640, 0o666, 0o604} {
		t.Run(fmt.Sprintf("%#o", mode), func(t *testing.T) {
			t.Parallel()
			cfg := configWithMode(t, mode)
			s, err := LoadServer(context.Background(), cfg)
			require.Error(t, err)
			require.Nil(t, s)
			require.ErrorIs(t, err, ErrConfigFileMode,
				"mode %#o should be rejected with ErrConfigFileMode", mode)
		})
	}
}

func TestLoadServer_AcceptsConfigAt0600(t *testing.T) {
	t.Parallel()
	cfg := configWithMode(t, 0o600)
	s, err := LoadServer(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, s)
}

// ---- Path-safety (audit_log) -----------------------------------------------

func TestServer_RejectsAuditLogOutsideStateDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/audit-log-escape.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrAuditLogEscape)
}
