package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeConfig writes body to a temp file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "supervise.toml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

// minimalBody returns the bytes of testdata/valid_minimal.toml.
func minimalBody(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/valid_minimal.toml")
	require.NoError(t, err)
	return string(b)
}

// removeLine returns body with any line whose key starts with prefix removed.
func removeLine(body, prefix string) string {
	out := make([]string, 0, 32)
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, prefix) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ---- US1: happy path + defaults --------------------------------------------

func TestSuperviseConfig_FullMinimal(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	require.NotNil(t, s)

	assert.Equal(t, "example-daemon", s.Name)
	assert.Equal(t, DefaultRequestedTTL, s.RequestedTTL)
	assert.Equal(t, DefaultRefreshNudgeBefore, s.RefreshNudgeBefore)
	assert.Equal(t, DefaultBootRetryTimeout, s.BootRetryTimeout)
	assert.Equal(t, DefaultCacheSecretsForRestart, s.CacheSecretsForRestart)
	assert.Equal(t, DefaultLogLevel, s.LogLevel)
	assert.Equal(t, DefaultRestartOnCleanExit, s.Child.RestartOnCleanExit)
	assert.Equal(t, DefaultRestartOnExit78, s.Child.RestartOnExit78)
	assert.Equal(t, DefaultWatchdogEnabled, s.Watchdog.Enabled)
	assert.Equal(t, DefaultWatchdogMaxAlertsPerHour, s.Watchdog.MaxAlertsPerHour)
	assert.NotNil(t, s.Watchdog.Patterns)
	assert.Empty(t, s.Watchdog.Patterns)
	assert.Nil(t, s.Reseal)
}

func TestSuperviseConfig_FullMaximal(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/valid_maximal.toml")
	require.NoError(t, err)
	p := writeConfig(t, string(b))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	require.NotNil(t, s)

	assert.Equal(t, DefaultRequestedTTL, s.RequestedTTL)
	assert.Equal(t, DefaultRefreshWindow, s.RefreshWindow)
	assert.Equal(t, DefaultRefreshNudgeBefore, s.RefreshNudgeBefore)
	assert.Equal(t, DefaultBootRetryTimeout, s.BootRetryTimeout)
	assert.Equal(t, DefaultLogLevel, s.LogLevel)
	assert.Equal(t, DefaultRestartOnCleanExit, s.Child.RestartOnCleanExit)
	assert.Equal(t, DefaultRestartOnExit78, s.Child.RestartOnExit78)
	assert.Len(t, s.Validators, 3)
	assert.Nil(t, s.Reseal)
}

func TestLoad_Idempotent(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s1, err := Load(context.Background(), p)
	require.NoError(t, err)
	s2, err := Load(context.Background(), p)
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(s1, s2))
}

func TestSuperviseConfig_DefaultRequestedTTL(t *testing.T) {
	t.Parallel()
	body := removeLine(minimalBody(t), "requested_ttl")
	p := writeConfig(t, body)
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultRequestedTTL, s.RequestedTTL)
	assert.Equal(t, 20*time.Hour, DefaultRequestedTTL)
}

func TestSuperviseConfig_ClientKeyFileExpands(t *testing.T) {
	t.Parallel()
	body := strings.Replace(minimalBody(t), `client_machine_index = 2`, "client_machine_index = 2\nclient_key_file = \"~/hush-smoke-client.key\"", 1)
	p := writeConfig(t, body)
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	require.NotEmpty(t, s.ClientKeyFile)
	assert.True(t, filepath.IsAbs(s.ClientKeyFile))
	assert.Contains(t, s.ClientKeyFile, "hush-smoke-client.key")
}

func TestSuperviseConfig_DefaultRefreshWindow(t *testing.T) {
	t.Parallel()
	body := removeLine(minimalBody(t), "refresh_window")
	p := writeConfig(t, body)
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultRefreshWindow, s.RefreshWindow)
	assert.Equal(t, "09:00-10:00", DefaultRefreshWindow)
}

func TestSuperviseConfig_DefaultRefreshNudgeBefore(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultRefreshNudgeBefore, s.RefreshNudgeBefore)
	assert.Equal(t, 30*time.Minute, DefaultRefreshNudgeBefore)
}

func TestSuperviseConfig_DefaultBootRetryTimeout(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultBootRetryTimeout, s.BootRetryTimeout)
	assert.Equal(t, 10*time.Minute, DefaultBootRetryTimeout)
}

func TestSuperviseConfig_DefaultCacheSecretsForRestart(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultCacheSecretsForRestart, s.CacheSecretsForRestart)
	assert.False(t, DefaultCacheSecretsForRestart)
}

func TestSuperviseConfig_DefaultGraceWindow(t *testing.T) {
	t.Parallel()
	body := "cache_secrets_for_restart = true\n" + minimalBody(t)
	p := writeConfig(t, body)
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultGraceWindow, s.CacheGraceTTL)
	assert.Equal(t, 60*time.Minute, DefaultGraceWindow)
}

func TestSuperviseConfig_DefaultLogLevel(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultLogLevel, s.LogLevel)
	assert.Equal(t, "info", DefaultLogLevel)
}

func TestSuperviseConfig_DefaultRestartOnCleanExit(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultRestartOnCleanExit, s.Child.RestartOnCleanExit)
	assert.True(t, DefaultRestartOnCleanExit)
}

func TestSuperviseConfig_DefaultRestartOnExit78(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultRestartOnExit78, s.Child.RestartOnExit78)
	assert.False(t, DefaultRestartOnExit78)
}

func TestSuperviseConfig_DefaultWatchdogEnabled(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultWatchdogEnabled, s.Watchdog.Enabled)
	assert.True(t, DefaultWatchdogEnabled)
}

func TestSuperviseConfig_DefaultWatchdogMaxAlertsPerHour(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, DefaultWatchdogMaxAlertsPerHour, s.Watchdog.MaxAlertsPerHour)
	assert.Equal(t, 6, DefaultWatchdogMaxAlertsPerHour)
}

func TestSuperviseConfig_DefaultWatchdogPatterns(t *testing.T) {
	t.Parallel()
	p := writeConfig(t, minimalBody(t))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.NotNil(t, s.Watchdog.Patterns)
	assert.Empty(t, s.Watchdog.Patterns)
	assert.Equal(t, []string{}, DefaultWatchdogPatterns)
}

func TestSuperviseConfig_DefaultDMRateLimit(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 5*time.Minute, DefaultDMRateLimit)
}

func TestSuperviseConfig_MaxGraceWindowConstant(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 4*time.Hour, MaxGraceWindow)
}

func TestSuperviseConfig_MaxRequestedTTLConstant(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 24*time.Hour, MaxRequestedTTL)
}

func TestSuperviseConfig_MaxStandingLeaseTTLConstant(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 30*24*time.Hour, MaxStandingLeaseTTL)
	assert.Greater(t, MaxStandingLeaseTTL, MaxRequestedTTL,
		"the standing-lease ceiling must exceed the ordinary 24h requested_ttl ceiling")
}

func TestSuperviseConfig_ResealMinSessionFloorConstant(t *testing.T) {
	t.Parallel()
	assert.Equal(t, time.Hour, ResealMinSessionFloor)
}

func TestSuperviseConfig_ResealSectionAbsent_IsUnset(t *testing.T) {
	t.Parallel()
	body := minimalBody(t)
	require.NotContains(t, body, "[reseal]")
	p := writeConfig(t, body)
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Nil(t, s.Reseal)
}

func TestSuperviseConfig_WatchdogSectionAbsent_AppliesAllDefaults(t *testing.T) {
	t.Parallel()
	body := minimalBody(t) // minimal already omits [watchdog]
	require.NotContains(t, body, "[watchdog]")
	p := writeConfig(t, body)
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.True(t, s.Watchdog.Enabled)
	assert.NotNil(t, s.Watchdog.Patterns)
	assert.Empty(t, s.Watchdog.Patterns)
	assert.Equal(t, 6, s.Watchdog.MaxAlertsPerHour)
}

func TestSuperviseConfig_PathFieldsAreExpandedAndAbsolute(t *testing.T) {
	// Not Parallel: overrides package-level userHomeDir.
	home := t.TempDir()
	prevHomeFn := userHomeDir
	t.Cleanup(func() { userHomeDir = prevHomeFn })
	userHomeDir = func() (string, error) { return home, nil }

	body := strings.NewReplacer(
		`status_socket = "/tmp/hush/supervise-example-daemon.sock"`,
		`status_socket = "~/sockets/example.sock"`,
		`pid_file = "/tmp/hush/supervise-example-daemon.pid"`,
		`pid_file = "~/run/example.pid"`,
	).Replace(minimalBody(t))
	p := writeConfig(t, body)
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(s.StatusSocket))
	assert.True(t, filepath.IsAbs(s.PIDFile))
	assert.Equal(t, filepath.Join(home, "sockets/example.sock"), s.StatusSocket)
	assert.Equal(t, filepath.Join(home, "run/example.pid"), s.PIDFile)
}

// ---- US2: unknown / mismatch / missing -------------------------------------

func TestSuperviseConfig_RejectsUnknownField(t *testing.T) {
	t.Parallel()
	base := minimalBody(t)
	cases := []struct {
		name string
		body string
	}{
		// Root: prepend before any table.
		{"root", "bogus_root_field = 42\n" + base},
		// Child: inject inside the existing [child] table.
		{"child", strings.Replace(base, "[child]\n", "[child]\nbogus_field = \"x\"\n", 1)},
		// Discord: append a new [discord] table at the end (it's absent in minimal).
		{"discord", base + "\n[discord]\nunknown = \"x\"\n"},
		// Watchdog: append a new [watchdog] table at the end.
		{"watchdog", base + "\n[watchdog]\nbogus = 1\n"},
		// Reseal: append a new [reseal] table at the end.
		{"reseal", base + "\n[reseal]\nbogus = \"x\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := writeConfig(t, tc.body)
			_, err := Load(context.Background(), p)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrUnknownField), "expected ErrUnknownField, got %v", err)
		})
	}
}

func TestSuperviseConfig_RejectsTypeMismatch(t *testing.T) {
	t.Parallel()
	body := strings.Replace(minimalBody(t), `requested_ttl = "20h"`, `requested_ttl = 42`, 1)
	p := writeConfig(t, body)
	_, err := Load(context.Background(), p)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTOMLDecode), "expected ErrTOMLDecode, got %v", err)
}

// ---- US3: validator allow-list (mostly in validate_test.go) ----------------

func TestSuperviseConfig_LoadedConfigContainsOnlyAllowListedValidators(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/valid_maximal.toml")
	require.NoError(t, err)
	p := writeConfig(t, string(b))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	for _, v := range s.Validators {
		_, ok := validatorAllowList[string(v)]
		assert.True(t, ok, "validator %q is not in allow-list", v)
	}
}

// ---- US8: no secrets / no env / no init / no goroutines ---------------------

func TestLoad_DoesNotReadSecretsFromEnv(t *testing.T) {
	t.Setenv("HUSH_DISCORD_TOKEN", "should-be-ignored")
	t.Setenv("HUSH_VAULT_PASSPHRASE", "should-be-ignored")
	t.Setenv("HUSH_REASON", "env-injected-reason")
	t.Setenv("HUSH_REFRESH_WINDOW", "00:00-23:59")

	b, err := os.ReadFile("testdata/valid_maximal.toml")
	require.NoError(t, err)
	p := writeConfig(t, string(b))
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, "Example long-running daemon", s.Reason)
	assert.Equal(t, "09:00-10:00", s.RefreshWindow)
}

func TestSchema_HasNoSecretFields(t *testing.T) {
	t.Parallel()
	// Guard names — words that would indicate a credential VALUE on the
	// struct. "Secret" alone is not in the list because the
	// CacheSecretsForRestart flag is a non-secret behaviour toggle whose
	// domain word is "secrets".
	guard := []string{"Token", "Password", "Passphrase", "Credential", "ApiKey", "APIKey"}
	st := reflect.TypeOf(Supervisor{})
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		assert.NotEqual(t, reflect.SliceOf(reflect.TypeOf(byte(0))), f.Type, "field %s is []byte", f.Name)
		for _, w := range guard {
			assert.False(t, strings.Contains(f.Name, w), "field %s contains secret-shaped name %q", f.Name, w)
		}
	}
	for _, sub := range []reflect.Type{reflect.TypeOf(Child{}), reflect.TypeOf(DiscordRouting{}), reflect.TypeOf(Watchdog{}), reflect.TypeOf(ResealSchedule{}), reflect.TypeOf(ChildReadiness{}), reflect.TypeOf(ChildShutdown{}), reflect.TypeOf(ChildHandoff{})} {
		for i := 0; i < sub.NumField(); i++ {
			f := sub.Field(i)
			for _, w := range guard {
				assert.False(t, strings.Contains(f.Name, w), "field %s.%s contains secret-shaped name %q", sub.Name(), f.Name, w)
			}
		}
	}
}

func TestPackage_NoInit(t *testing.T) {
	t.Parallel()
	// Grep-style guard: the package must have no init() function. We scan the
	// package's .go sources (excluding _test.go) for the literal "func init(".
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		assert.False(t, strings.Contains(string(data), "func init("), "%s defines an init() function", name)
	}
}

func TestLoad_NoGoroutineLeak(t *testing.T) {
	// Not Parallel: NumGoroutine fluctuates with sibling parallel tests.
	// Run serially and call Load many times; the goroutine count after the
	// burst must not grow unboundedly.
	before := runtime.NumGoroutine()
	for i := 0; i < 32; i++ {
		p := writeConfig(t, minimalBody(t))
		_, err := Load(context.Background(), p)
		require.NoError(t, err)
	}
	after := runtime.NumGoroutine()
	assert.LessOrEqual(t, after, before+2, "Load must not spawn goroutines: before=%d after=%d", before, after)
}

func TestSuperviseConfig_HomeExpansionIsTheOnlyEnvCall(t *testing.T) {
	// Not Parallel: overrides package-level userHomeDir.
	home := t.TempDir()
	prevHomeFn := userHomeDir
	t.Cleanup(func() { userHomeDir = prevHomeFn })
	userHomeDir = func() (string, error) { return home, nil }

	body := strings.NewReplacer(
		`status_socket = "/tmp/hush/supervise-example-daemon.sock"`,
		`status_socket = "~/sockets/x.sock"`,
		`pid_file = "/tmp/hush/supervise-example-daemon.pid"`,
		`pid_file = "~/run/x.pid"`,
	).Replace(minimalBody(t))
	p := writeConfig(t, body)
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(s.StatusSocket, home))
	assert.True(t, strings.HasPrefix(s.PIDFile, home))
}

// ---- ctx cancellation -------------------------------------------------------

func TestLoad_HonoursContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := writeConfig(t, minimalBody(t))
	_, err := Load(ctx, p)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestLoad_OpenError(t *testing.T) {
	t.Parallel()
	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "does-not-exist.toml"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}
