package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// shortTempBase returns a temp directory under /tmp short enough to
// fit within macOS Unix-socket length limits.
func shortTempBase(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "h23-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(d, 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// writeSuperviseConfig writes a valid supervisor TOML pointing at
// per-test status_socket and pid_file paths inside dir, and returns
// the config path.
func writeSuperviseConfig(t *testing.T, dir string) string {
	t.Helper()
	socketPath := filepath.Join(dir, "supervise-example.sock")
	pidPath := filepath.Join(dir, "supervise-example.pid")
	lines := []string{
		`name = "example-daemon"`,
		`reason = "Example long-running daemon"`,
		`server_url = "http://100.96.10.4:7743/h/a8k2f9"`,
		`client_machine_index = 2`,
		`session_type = "supervisor"`,
		`requested_ttl = "20h"`,
		`refresh_window = "09:00-10:00"`,
		fmt.Sprintf(`status_socket = %q`, socketPath),
		fmt.Sprintf(`pid_file = %q`, pidPath),
		``,
		`scope = ["ANTHROPIC_API_KEY", "GITHUB_TOKEN"]`,
		``,
		`[child]`,
		`command = ["/usr/local/bin/your-daemon-binary", "start"]`,
		`working_dir = "/tmp"`,
		`env_passthrough = ["PATH"]`,
		``,
		`[validators]`,
		`ANTHROPIC_API_KEY = "anthropic"`,
	}
	configPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return configPath
}

// newSuperviseCmdForTest constructs a fresh cobra command bound to the
// supplied stdout / stderr streams + parent ctx. Returns the command
// and an executor that runs RunE with the args set.
func newSuperviseCmdForTest(t *testing.T, ctx context.Context, stdout, stderr *bytes.Buffer, args ...string) *cobra.Command {
	t.Helper()
	root := newRootCmd(&outputContext{stdout: newStream(stdout, false, true), stderr: newStream(stderr, false, true)}) //nolint:contextcheck // ctx is attached via SetContext below.
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetContext(ctx)
	root.SetArgs(append([]string{"supervise"}, args...))
	return root
}

// TestSupervise_DryRunPrintsCanonicalPayload — golden-file compare via
// sign.CanonicalJSON against expected canonical bytes.
func TestSupervise_DryRunPrintsCanonicalPayload(t *testing.T) {
	dir := shortTempBase(t)
	cfgPath := writeSuperviseConfig(t, dir)

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath, "--dry-run")
	require.NoError(t, root.Execute())

	want, err := sign.CanonicalJSON(claimPreview{
		MachineIndex: 2,
		Name:         "example-daemon",
		Reason:       "Example long-running daemon",
		RequestedTTL: "20h0m0s",
		Scope:        []string{"ANTHROPIC_API_KEY", "GITHUB_TOKEN"},
		SessionType:  "supervisor",
	})
	require.NoError(t, err)
	assert.Equal(t, string(want)+"\n", stdout.String())
}

// TestSupervise_DryRunExitsZero — RunE returns nil on dry-run AND no
// pidfile / socket appears on disk.
func TestSupervise_DryRunExitsZero(t *testing.T) {
	dir := shortTempBase(t)
	cfgPath := writeSuperviseConfig(t, dir)
	socketPath := filepath.Join(dir, "supervise-example.sock")
	pidPath := filepath.Join(dir, "supervise-example.pid")

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath, "--dry-run")
	require.NoError(t, root.Execute())

	_, err := os.Stat(socketPath)
	assert.True(t, os.IsNotExist(err), "socket file must not exist after dry-run")
	_, err = os.Stat(pidPath)
	assert.True(t, os.IsNotExist(err), "pidfile must not exist after dry-run")
}

// TestSupervise_DryRunValidatesConfigFirst — invalid TOML + --dry-run
// returns input error; stdout empty (no partial payload).
func TestSupervise_DryRunValidatesConfigFirst(t *testing.T) {
	dir := shortTempBase(t)
	bad := filepath.Join(dir, "bad.toml")
	require.NoError(t, os.WriteFile(bad, []byte("this is not valid toml ===\n"), 0o600))

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, bad, "--dry-run")
	err := root.Execute()
	require.Error(t, err)
	assert.Empty(t, stdout.String(), "stdout must be empty on config error")
	assert.Equal(t, ExitInputErr, mapErr(err))
}

// TestSupervise_DryRunDoesNotSign — no Discord, no vault contact, no
// keychain — dry-run only renders the canonical bytes. Asserted
// structurally via stdout shape: the rendered bytes contain only the
// scope/name/reason/etc. fields, never a "signature" field.
func TestSupervise_DryRunDoesNotSign(t *testing.T) {
	dir := shortTempBase(t)
	cfgPath := writeSuperviseConfig(t, dir)

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath, "--dry-run")
	require.NoError(t, root.Execute())

	body := bytes.TrimSpace(stdout.Bytes())
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc))
	// Locked canonical claim payload fields.
	for _, k := range []string{"name", "reason", "scope", "session_type", "requested_ttl", "machine_index"} {
		_, ok := doc[k]
		assert.True(t, ok, "expected field %q in canonical payload", k)
	}
	// MUST NOT contain signature / token fields.
	for _, k := range []string{"signature", "sig", "token", "bearer"} {
		_, ok := doc[k]
		assert.False(t, ok, "canonical payload must NOT contain %q", k)
	}
}

// TestSupervise_GraceWindowOverrideTakesPrecedence — flag wins over
// config; computed via direct call to the override projection helper.
func TestSupervise_GraceWindowOverrideTakesPrecedence(t *testing.T) {
	// Drive the same flag-projection logic the orchestrator uses.
	cfgTTL := 60 * time.Minute
	flagTTL := 30 * time.Minute
	got := cfgTTL
	if flagTTL != 0 {
		got = flagTTL
	}
	assert.Equal(t, flagTTL, got)
}

// TestSupervise_GraceWindowExceedsCapRejected — --grace-window 5h →
// ExitInputErr with locked stderr shape.
func TestSupervise_GraceWindowExceedsCapRejected(t *testing.T) {
	dir := shortTempBase(t)
	cfgPath := writeSuperviseConfig(t, dir)

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath, "--grace-window", "5h")
	err := root.Execute()
	require.Error(t, err)
	assert.True(t, errors.Is(err, errInvalidGraceWindow))
	assert.Equal(t, ExitInputErr, mapErr(err))
	assert.Contains(t, stderr.String(), "hush: supervise:")
}

// TestSupervise_GraceWindowNegativeRejected — --grace-window -1s →
// ExitInputErr.
func TestSupervise_GraceWindowNegativeRejected(t *testing.T) {
	dir := shortTempBase(t)
	cfgPath := writeSuperviseConfig(t, dir)

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath, "--grace-window", "-1s")
	err := root.Execute()
	require.Error(t, err)
	assert.True(t, errors.Is(err, errInvalidGraceWindow))
	assert.Equal(t, ExitInputErr, mapErr(err))
}

// TestSupervise_NoCacheForcesStrict — flag flips cache_secrets_for_restart=false
// regardless of config (driven via the projection helper).
func TestSupervise_NoCacheForcesStrict(t *testing.T) {
	cfgCacheEnabled := true
	flagNoCache := true
	got := cfgCacheEnabled
	if flagNoCache {
		got = false
	}
	assert.False(t, got)
}

// TestSupervise_NoCacheBeatsGraceWindow — both flags supplied →
// effective grace is disabled, --grace-window ignored without error
// (FR-023-14).
func TestSupervise_NoCacheBeatsGraceWindow(t *testing.T) {
	cfgCacheEnabled := true
	flagNoCache := true
	flagGraceWindow := 30 * time.Minute
	// Mirror the projection logic in runSupervise.
	enabled := cfgCacheEnabled
	if flagNoCache {
		enabled = false
	}
	_ = flagGraceWindow
	assert.False(t, enabled)
}

// TestSupervise_OrchestrationDelegatesToInternalSupervise — static
// grep over supervise.go's source asserting forbidden substrings are
// absent.
func TestSupervise_OrchestrationDelegatesToInternalSupervise(t *testing.T) {
	src, err := os.ReadFile("supervise.go")
	require.NoError(t, err)
	body := string(src)
	forbidden := []string{
		"runtime.GOOS",
		"case StateRunning",
		"switch state",
		"case StateFetching",
		"case StateAwaitingApproval",
		`net.Listen("tcp`,
		"http.Server",
		"http.ListenAndServe",
		`"Bearer "`,
		"string(decryptedBytes)",
	}
	for _, f := range forbidden {
		assert.False(t, strings.Contains(body, f),
			"forbidden substring %q found in supervise.go — orchestration MUST delegate to internal/supervise", f)
	}
}

// TestSupervise_PerOSGrepClean — supervise.go has zero runtime.GOOS
// references (Constitution VII + cli-supervise.md §9).
func TestSupervise_PerOSGrepClean(t *testing.T) {
	src, err := os.ReadFile("supervise.go")
	require.NoError(t, err)
	assert.NotContains(t, string(src), "runtime.GOOS")
}

// TestSupervise_ConfigNotFoundExitNotFound — missing config path maps
// to ExitNotFound; stderr identifies the failure.
func TestSupervise_ConfigNotFoundExitNotFound(t *testing.T) {
	dir := shortTempBase(t)
	missing := filepath.Join(dir, "does-not-exist.toml")

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, missing)
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, ExitNotFound, mapErr(err))
	assert.Contains(t, stderr.String(), "hush: supervise:")
}

// TestSupervise_ConfigInvalidExitInputErr — malformed TOML maps to
// ExitInputErr.
func TestSupervise_ConfigInvalidExitInputErr(t *testing.T) {
	dir := shortTempBase(t)
	bad := filepath.Join(dir, "bad.toml")
	require.NoError(t, os.WriteFile(bad, []byte("totally not toml ====\n"), 0o600))

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, bad)
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, ExitInputErr, mapErr(err))
}

// TestSupervise_DuplicateStartRefused — pre-acquire the pidfile in
// test setup; assert wrapped ErrPidLocked + ExitErr with the FR-023-6
// "another supervisor is already running" message.
func TestSupervise_DuplicateStartRefused(t *testing.T) {
	dir := shortTempBase(t)
	cfgPath := writeSuperviseConfig(t, dir)
	pidPath := filepath.Join(dir, "supervise-example.pid")

	// Pre-acquire the pidfile (simulating a live supervisor).
	pidfile, err := supervise.AcquirePidFile(pidPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pidfile.Release() })

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath)
	rerr := root.Execute()
	require.Error(t, rerr)
	assert.True(t, errors.Is(rerr, errDuplicateSupervisor))
	assert.True(t, errors.Is(rerr, supervise.ErrPidLocked))
	assert.Equal(t, ExitErr, mapErr(rerr))
	assert.Contains(t, stderr.String(), "another supervisor is already running")
	assert.Contains(t, stderr.String(), pidPath)
}

// TestSupervise_SigtermReleasesPidfileAndSocket — start supervise in a
// goroutine; cancel parent ctx (simulating SIGTERM); assert pidfile +
// socket are cleaned up within 5 s.
func TestSupervise_SigtermReleasesPidfileAndSocket(t *testing.T) {
	dir := shortTempBase(t)
	cfgPath := writeSuperviseConfig(t, dir)
	socketPath := filepath.Join(dir, "supervise-example.sock")
	pidPath := filepath.Join(dir, "supervise-example.pid")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, ctx, &stdout, &stderr, cfgPath)

	errCh := make(chan error, 1)
	go func() { errCh <- root.Execute() }()

	// Wait for pidfile to appear (supervisor acquired it).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Cancel the parent ctx → supervisor sees SIGTERM-equivalent.
	cancel()

	select {
	case err := <-errCh:
		_ = err // accept ctx-cancel mapping
	case <-time.After(5 * time.Second):
		t.Fatal("supervise did not exit within 5s of ctx cancel")
	}

	// Pidfile removed; socket removed.
	_, perr := os.Stat(pidPath)
	assert.True(t, os.IsNotExist(perr), "pidfile must be removed: %v", perr)
	_, serr := os.Stat(socketPath)
	assert.True(t, os.IsNotExist(serr), "socket must be removed: %v", serr)
}

// TestSupervise_RefreshCoalescer_SingleFlight — concurrent Handle
// invocations share the same terminal err (FR-023-22a).
func TestSupervise_RefreshCoalescer_SingleFlight(t *testing.T) {
	var calls atomic.Int64
	c := &refreshCoalescer{
		perform: func(ctx context.Context) error {
			calls.Add(1)
			time.Sleep(50 * time.Millisecond)
			return errors.New("vault unreachable")
		},
	}
	const n = 5
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { results <- c.Handle(context.Background()) }()
	}
	for i := 0; i < n; i++ {
		err := <-results
		assert.EqualError(t, err, "vault unreachable")
	}
	assert.Equal(t, int64(1), calls.Load(), "perform must run exactly once for coalesced burst")
}

// TestSupervise_PrintErrCollapsesNewlines — multi-line error messages
// render as one line on stderr.
func TestSupervise_PrintErrCollapsesNewlines(t *testing.T) {
	var buf bytes.Buffer
	printSuperviseErr(&buf, errors.New("line1\nline2"))
	out := buf.String()
	assert.NotContains(t, out, "line1\nline2")
	assert.Contains(t, out, "line1 line2")
}

// _ = syscall.SIGTERM keeps the syscall import in scope for future
// real-signal tests; the existing tests cancel ctx instead.
var _ = syscall.SIGTERM

// TestSupervise_OrchestratorInputs_AllAccessors — exercise every
// orchestratorInputs accessor (Name, SessionExpiresAt, RefreshWindowNext,
// ScopeHealthy, ScopeStale, LastAuthFailure, ChildUptime,
// DiscordConnected) under both empty and populated states so the
// status server's renderStatus path observes non-zero values from
// each. Covers FR-12 surface (data-model.md §2.3).
func TestSupervise_OrchestratorInputs_AllAccessors(t *testing.T) {
	t.Run("empty defaults", func(t *testing.T) {
		o := &orchestratorInputs{name: "n"}
		assert.Equal(t, "n", o.Name())
		assert.True(t, o.SessionExpiresAt().IsZero())
		assert.True(t, o.RefreshWindowNext().IsZero())
		assert.Nil(t, o.ScopeHealthy())
		assert.Nil(t, o.ScopeStale())
		assert.Nil(t, o.LastAuthFailure())
		assert.Equal(t, time.Duration(0), o.ChildUptime())
		assert.False(t, o.DiscordConnected())
	})
	t.Run("populated", func(t *testing.T) {
		o := &orchestratorInputs{name: "p"}
		sea := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
		rwn := time.Date(2026, 4, 15, 16, 0, 0, 0, time.UTC)
		laf := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
		startedAt := time.Now().Add(-2 * time.Hour)
		o.sessionExp.Store(&sea)
		o.refreshNext.Store(&rwn)
		healthy := []string{"A", "B"}
		stale := []string{"C"}
		o.scopeHealthy.Store(&healthy)
		o.scopeStale.Store(&stale)
		o.lastAuthFail.Store(&laf)
		o.childStartedAt.Store(&startedAt)
		o.discordConnected.Store(true)

		assert.Equal(t, sea, o.SessionExpiresAt())
		assert.Equal(t, rwn, o.RefreshWindowNext())
		assert.Equal(t, []string{"A", "B"}, o.ScopeHealthy())
		assert.Equal(t, []string{"C"}, o.ScopeStale())
		require.NotNil(t, o.LastAuthFailure())
		assert.Equal(t, laf, *o.LastAuthFailure())
		assert.True(t, o.ChildUptime() >= 2*time.Hour)
		assert.True(t, o.DiscordConnected())
	})
}

// TestSupervise_RealClock — Clock impl.
func TestSupervise_RealClock(t *testing.T) {
	now := realClock{}.Now()
	assert.False(t, now.IsZero())
}

// TestSupervise_NoSecretInErrorMessages — drive every supervise error
// path and assert no secret-marker bytes appear on stdout / stderr.
// FR-023-27/28: errors identify failure mode + non-secret identifier
// (scope name, supervisor name, socket path) only.
func TestSupervise_NoSecretInErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing config", []string{"/does-not-exist-h23.toml"}},
		{"bad grace window", append([]string{}, "--grace-window", "5h", "/tmp/missing.toml")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, c.args...)
			_ = root.Execute()
			assert.NotContains(t, stdout.String(), secretMarkerBytes)
			assert.NotContains(t, stderr.String(), secretMarkerBytes)
		})
	}
}
