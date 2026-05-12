package supervise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubStatusInputs implements StatusInputs with field-backed values.
type stubStatusInputs struct {
	name              string
	sessionExpiresAt  time.Time
	refreshWindowNext time.Time
	scopeHealthy      []string
	scopeStale        []string
	lastAuthFailure   *time.Time
	childUptime       time.Duration
	discordConnected  bool
}

func (s *stubStatusInputs) Name() string                 { return s.name }
func (s *stubStatusInputs) SessionExpiresAt() time.Time  { return s.sessionExpiresAt }
func (s *stubStatusInputs) RefreshWindowNext() time.Time { return s.refreshWindowNext }
func (s *stubStatusInputs) ScopeHealthy() []string       { return s.scopeHealthy }
func (s *stubStatusInputs) ScopeStale() []string         { return s.scopeStale }
func (s *stubStatusInputs) LastAuthFailure() *time.Time  { return s.lastAuthFailure }
func (s *stubStatusInputs) ChildUptime() time.Duration   { return s.childUptime }
func (s *stubStatusInputs) DiscordConnected() bool       { return s.discordConnected }

// shortTempDir returns a 0o700 directory under a short-path root with a
// short prefix — required because macOS Unix-socket paths are limited to
// ~104 bytes and t.TempDir() returns long /var/folders/... paths that
// exceed the cap when combined with our test names. We start from the
// per-OS convention via defaultRuntimeDir() (research.md R-2) when its
// length is short enough, falling back to /tmp. Cleanup is registered
// with t.
func shortTempDir(t *testing.T) string {
	t.Helper()
	root := defaultRuntimeDir()
	// Heuristic: macOS UserCacheDir is long (~/Library/Caches/hush);
	// drop back to /tmp when the runtime dir would already eat half the
	// 104-byte cap.
	if len(root) > 30 {
		root = "/tmp"
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir runtime root: %v", err)
	}
	d, err := os.MkdirTemp(root, "h22-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatalf("chmod 0700: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// tempSocketPath returns a temp socket path under a 0o700 parent.
func tempSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortTempDir(t), "s.sock")
}

// silentLogger returns a *slog.Logger discarding records — keeps test
// output clean while exercising the same code paths.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// startServer spawns Run(ctx) in a goroutine, returning a func that
// cancels the ctx and waits for Run to return. Polls via net.Dial to
// confirm the socket is actually accepting connections (a regular-file
// inode at the path returns ENOTSOCK on dial, which is the
// distinguishing signal we want).
func startServer(t *testing.T, srv *StatusServer) func() error {
	t.Helper()
	ctx, cancelFn := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var d net.Dialer
		c, err := d.DialContext(ctx, "unix", srv.socketPath)
		if err == nil {
			_ = c.Close()
			break
		}
		select {
		case err := <-errCh:
			t.Fatalf("Run returned before listener was ready: %v", err)
		default:
		}
		time.Sleep(2 * time.Millisecond)
	}
	return func() error {
		cancelFn()
		select {
		case err := <-errCh:
			return err
		case <-time.After(2 * time.Second):
			return errors.New("Run did not return within 2s of ctx cancel")
		}
	}
}

// dialAndDrive sends "status\n" and reads the full response.
func dialAndDrive(t *testing.T, path string) []byte {
	t.Helper()
	var d net.Dialer
	conn, err := d.DialContext(context.Background(), "unix", path)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	if _, werr := conn.Write([]byte("status\n")); werr != nil {
		t.Fatalf("write request: %v", werr)
	}
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	body, err := io.ReadAll(conn)
	require.NoError(t, err)
	return body
}

// ============================================================
// US3 — Status JSON shape + redaction + pre-attach defaults
// ============================================================

// TestSocket_StatusJSONShape — every FR-12 key present with the
// documented Go type (FR-022-12, SC-022-5).
func TestSocket_StatusJSONShape(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())
	srv.attach(&stubStatusInputs{
		name:              "openclaw",
		sessionExpiresAt:  time.Date(2026, 4, 15, 13, 12, 0, 0, time.UTC),
		refreshWindowNext: time.Date(2026, 4, 15, 16, 0, 0, 0, time.UTC),
		scopeHealthy:      []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		scopeStale:        []string{},
		lastAuthFailure:   nil,
		childUptime:       8*time.Hour + 12*time.Minute,
		discordConnected:  true,
	})

	stop := startServer(t, srv)
	body := dialAndDrive(t, path)
	require.NoError(t, stop())

	body = bytes.TrimSpace(body)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &doc))

	for _, key := range []string{
		"supervisor", "session_expires_at", "refresh_window_next",
		"scope_healthy", "scope_stale", "last_auth_failure",
		"child_pid", "child_uptime", "discord_connected", "state",
	} {
		_, ok := doc[key]
		assert.True(t, ok, "FR-12 key %q must be present", key)
	}

	// Type assertions on each field.
	var sup string
	require.NoError(t, json.Unmarshal(doc["supervisor"], &sup))
	assert.Equal(t, "openclaw", sup)

	var sea string
	require.NoError(t, json.Unmarshal(doc["session_expires_at"], &sea))
	assert.Equal(t, "2026-04-15T13:12:00Z", sea)

	var sh []string
	require.NoError(t, json.Unmarshal(doc["scope_healthy"], &sh))
	assert.Equal(t, []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}, sh)

	var ss []string
	require.NoError(t, json.Unmarshal(doc["scope_stale"], &ss))
	assert.Equal(t, []string{}, ss)

	// last_auth_failure is null
	assert.Equal(t, "null", string(doc["last_auth_failure"]))

	// child_pid is null (no Store -> snap.ChildPID = 0)
	assert.Equal(t, "null", string(doc["child_pid"]))

	var cu string
	require.NoError(t, json.Unmarshal(doc["child_uptime"], &cu))
	assert.Equal(t, "8h12m0s", cu)

	var dc bool
	require.NoError(t, json.Unmarshal(doc["discord_connected"], &dc))
	assert.True(t, dc)

	var state string
	require.NoError(t, json.Unmarshal(doc["state"], &state))
}

// TestSocket_StatusJSONFromSnapshot — encoder emits byte-equal expected
// FR-12 JSON given a constructed Snapshot + stub StatusInputs
// (FR-022-12, FR-022-16). Exercises renderStatus directly.
func TestSocket_StatusJSONFromSnapshot(t *testing.T) {
	srv := NewStatusServer("/dev/null/ignored", nil, silentLogger())
	srv.attach(&stubStatusInputs{
		name:              "openclaw",
		sessionExpiresAt:  time.Date(2026, 4, 15, 13, 12, 0, 0, time.UTC),
		refreshWindowNext: time.Date(2026, 4, 15, 16, 0, 0, 0, time.UTC),
		scopeHealthy:      []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		scopeStale:        []string{},
		childUptime:       8*time.Hour + 12*time.Minute,
		discordConnected:  true,
	})

	snap := Snapshot{State: StateRunning, ChildPID: 51234}
	body, err := srv.renderStatus(snap)
	require.NoError(t, err)

	expected := `{"supervisor":"openclaw","session_expires_at":"2026-04-15T13:12:00Z","refresh_window_next":"2026-04-15T16:00:00Z","scope_healthy":["ANTHROPIC_API_KEY","OPENAI_API_KEY"],"scope_stale":[],"last_auth_failure":null,"child_pid":51234,"child_uptime":"8h12m0s","discord_connected":true,"state":"running"}`
	assert.Equal(t, expected, string(body))
}

// TestSocket_TokenInResponseRedacted — Snapshot.Token marker bytes never
// appear in the rendered JSON; no "token" field at all (FR-022-13,
// SC-022-6, Constitution X).
func TestSocket_TokenInResponseRedacted(t *testing.T) {
	const marker = "MARKER_d3adb33f"
	store := newTestStoreWithToken(t, []byte(marker))

	path := tempSocketPath(t)
	srv := NewStatusServer(path, store, silentLogger())
	srv.attach(&stubStatusInputs{name: "test"})

	stop := startServer(t, srv)
	body := dialAndDrive(t, path)
	require.NoError(t, stop())

	assert.False(t, bytes.Contains(body, []byte(marker)), "marker bytes leaked in response")
	assert.False(t, bytes.Contains(body, []byte(`"token"`)), `"token" field present`)
	assert.False(t, bytes.Contains(body, []byte("MARKER_d3adb")), "marker prefix leaked")
}

// TestSocket_PreAttachDefaultsRenderShapeConformant — without attach,
// the server emits the FR-12 default-shape document (contracts/api.md §2.3).
func TestSocket_PreAttachDefaultsRenderShapeConformant(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())

	stop := startServer(t, srv)
	body := dialAndDrive(t, path)
	require.NoError(t, stop())

	body = bytes.TrimSpace(body)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &doc))

	assert.Equal(t, `""`, string(doc["supervisor"]))
	assert.Equal(t, `"0001-01-01T00:00:00Z"`, string(doc["session_expires_at"]))
	assert.Equal(t, `"0001-01-01T00:00:00Z"`, string(doc["refresh_window_next"]))
	assert.Equal(t, `[]`, string(doc["scope_healthy"]))
	assert.Equal(t, `[]`, string(doc["scope_stale"]))
	assert.Equal(t, `null`, string(doc["last_auth_failure"]))
	assert.Equal(t, `null`, string(doc["child_pid"]))
	assert.Equal(t, `"0s"`, string(doc["child_uptime"]))
	assert.Equal(t, `false`, string(doc["discord_connected"]))
	assert.Equal(t, `""`, string(doc["state"]))
}

// ============================================================
// US4 — Lifecycle: graceful shutdown, force-close, single-shot, no leak
// ============================================================

// TestSocket_GracefulShutdownOnCtx — ctx cancel returns nil error from
// Run within sub-second bound (FR-022-14, SC-022-7, Clarification 3).
func TestSocket_GracefulShutdownOnCtx(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())

	stop := startServer(t, srv)
	start := time.Now()
	err := stop()
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 1*time.Second)
}

// TestSocket_ConnectionForceClosedOnCtxCancel — mid-handler ctx cancel
// force-closes the conn within sub-second; Run returns within sub-second
// (FR-022-14, Clarification 3).
func TestSocket_ConnectionForceClosedOnCtxCancel(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())

	ctx, cancelFn := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()

	// Wait for listener.
	deadline := time.Now().Add(2 * time.Second)
	var d net.Dialer
	var conn net.Conn
	for time.Now().Before(deadline) {
		c, derr := d.DialContext(ctx, "unix", path)
		if derr == nil {
			conn = c
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	require.NotNil(t, conn, "dial did not succeed within deadline")
	defer func() { _ = conn.Close() }()

	// Don't write a request: handler is reading; cancel ctx mid-read.
	start := time.Now()
	cancelFn()

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 64)
	_, readErr := conn.Read(buf)
	// Read returns either io.EOF (clean close) or an error containing
	// "use of closed" / "connection reset" depending on platform.
	if readErr == nil {
		t.Fatalf("expected read error after ctx cancel, got nil")
	}
	assert.Less(t, time.Since(start), 2*time.Second)

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after ctx cancel")
	}
}

// TestSocket_RebindAfterStop — fresh StatusServer rebinds the same path
// post-Run-stop without EADDRINUSE (SC-022-7).
func TestSocket_RebindAfterStop(t *testing.T) {
	path := tempSocketPath(t)

	srv1 := NewStatusServer(path, nil, silentLogger())
	stop1 := startServer(t, srv1)
	require.NoError(t, stop1())

	srv2 := NewStatusServer(path, nil, silentLogger())
	stop2 := startServer(t, srv2)

	// Verify second server actually services a request.
	body := dialAndDrive(t, path)
	require.NotEmpty(t, body)
	require.NoError(t, stop2())
}

// TestSocket_RunSecondCallReturnsErrAlreadyRunning — second Run on the
// same instance returns errors.Is(err, ErrAlreadyRunning) (FR-022-14a).
func TestSocket_RunSecondCallReturnsErrAlreadyRunning(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())

	stop := startServer(t, srv)

	err := srv.Run(context.Background())
	assert.True(t, errors.Is(err, ErrAlreadyRunning), "second Run should return ErrAlreadyRunning, got %v", err)

	require.NoError(t, stop())
}

// TestSocket_NoGoroutineLeak — start/stop cycles return runtime.NumGoroutine
// to baseline within tolerance (FR-022-17, SC-022-8).
func TestSocket_NoGoroutineLeak(t *testing.T) {
	// Warm-up cycle so any one-time-init goroutines have spawned before
	// the baseline read.
	{
		srv := NewStatusServer(tempSocketPath(t), nil, silentLogger())
		stop := startServer(t, srv)
		_ = stop()
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		srv := NewStatusServer(tempSocketPath(t), nil, silentLogger())
		stop := startServer(t, srv)
		require.NoError(t, stop())
	}
	runtime.GC()
	time.Sleep(20 * time.Millisecond)

	now := runtime.NumGoroutine()
	assert.LessOrEqual(t, now, baseline+2, "goroutine leak: baseline=%d now=%d", baseline, now)
}

// TestSocket_PreviousSocketCleanedUp — stale socket inode at the path
// is unlinked pre-bind (FR-022-11).
func TestSocket_PreviousSocketCleanedUp(t *testing.T) {
	path := tempSocketPath(t)

	require.NoError(t, os.WriteFile(path, []byte("stale"), 0o600))

	srv := NewStatusServer(path, nil, silentLogger())
	stop := startServer(t, srv)

	// If Run failed, the listener file would not be a socket. Probe by
	// dialing.
	var d net.Dialer
	conn, err := d.DialContext(context.Background(), "unix", path)
	require.NoError(t, err)
	_ = conn.Close()

	require.NoError(t, stop())
}

// ============================================================
// US5 — Filesystem permissions are the only authorization
// ============================================================

// TestSocket_Mode0600 — post-Run, socket inode mode is exactly 0o600
// (FR-022-9, SC-022-4).
func TestSocket_Mode0600(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())

	stop := startServer(t, srv)
	defer func() { _ = stop() }()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestSocket_ParentMode0700 — when parent dir is missing, Run creates
// it at 0o700 (FR-022-10, SC-022-4).
func TestSocket_ParentMode0700(t *testing.T) {
	root := shortTempDir(t)
	parent := filepath.Join(root, "n", "s")
	path := filepath.Join(parent, "s.sock")

	srv := NewStatusServer(path, nil, silentLogger())
	stop := startServer(t, srv)
	defer func() { _ = stop() }()

	info, err := os.Stat(parent)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// TestSocket_ParentLooseRefuses — pre-existing 0o755 parent → Run
// returns ErrSocketPermsLoose; no listener bound (FR-022-10).
func TestSocket_ParentLooseRefuses(t *testing.T) {
	root := shortTempDir(t)
	parent := filepath.Join(root, "loose")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	path := filepath.Join(parent, "s.sock")

	srv := NewStatusServer(path, nil, silentLogger())
	err := srv.Run(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSocketPermsLoose), "got %v, want errors.Is(err, ErrSocketPermsLoose)", err)

	_, statErr := os.Stat(path)
	assert.Error(t, statErr, "no socket should be created on perms-refusal")
}

// TestNewStatusServer_NilLoggerPanics — the constructor is documented
// to panic on a nil logger (Constitution IX startup-wiring exemption).
func TestNewStatusServer_NilLoggerPanics(t *testing.T) {
	assert.Panics(t, func() {
		_ = NewStatusServer("/tmp/ignored.sock", nil, nil)
	})
}

// TestSocket_RenderStatusLastAuthFailureNonNil — when StatusInputs
// reports a non-nil LastAuthFailure, the JSON renders an RFC3339 string
// rather than null.
func TestSocket_RenderStatusLastAuthFailureNonNil(t *testing.T) {
	srv := NewStatusServer("/dev/null/ignored", nil, silentLogger())
	failureAt := time.Date(2026, 4, 15, 13, 12, 0, 0, time.UTC)
	srv.attach(&stubStatusInputs{
		name:             "x",
		lastAuthFailure:  &failureAt,
		scopeHealthy:     []string{"A"},
		scopeStale:       []string{"B"},
		discordConnected: false,
	})
	body, err := srv.renderStatus(Snapshot{})
	require.NoError(t, err)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &doc))
	assert.Equal(t, `"2026-04-15T13:12:00Z"`, string(doc["last_auth_failure"]))
}

// TestSocket_EnsureParentNotDirectory — when the configured parent path
// exists but is a regular file, ensureParentMode0700 surfaces the
// errParentNotDir sentinel (programmer-error class).
func TestSocket_EnsureParentNotDirectory(t *testing.T) {
	root := t.TempDir()
	notADir := filepath.Join(root, "notadir")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))
	err := ensureParentMode0700(notADir)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errParentNotDir))
}

// TestSocket_RunFailsWhenStaleNonEmptyDirAtPath — when a non-empty
// directory occupies the configured socket path, os.Remove fails (not
// ErrNotExist) and Run surfaces the wrapped error before binding (covers
// the cleanup error path).
func TestSocket_RunFailsWhenStaleNonEmptyDirAtPath(t *testing.T) {
	parent := shortTempDir(t)
	path := filepath.Join(parent, "s.sock")
	require.NoError(t, os.Mkdir(path, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "x"), []byte("x"), 0o600))

	srv := NewStatusServer(path, nil, silentLogger())
	err := srv.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supervise: status socket cleanup")
}

// TestSocket_RunFailsListenOnImpossiblePath — a socket path with a
// non-existent unwriteable component triggers the Listen error branch.
// We use a path whose parent we explicitly mark read-only so MkdirAll
// can't create the leaf and Listen can't bind.
func TestSocket_RunFailsListenOnImpossiblePath(t *testing.T) {
	// Construct a socket path well over the 104-byte cap so Listen fails
	// with EINVAL before any other branch.
	parent := shortTempDir(t)
	// 120-byte filename forces EINVAL on bind for most Unix kernels.
	tooLong := strings.Repeat("a", 120)
	path := filepath.Join(parent, tooLong+".sock")

	srv := NewStatusServer(path, nil, silentLogger())
	err := srv.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supervise: status socket listen")
}

// TestSocket_HandlerRecoversFromInputsPanic — the per-connection
// handler's top-frame recover catches a panic from inputs and the
// server keeps running for the next request.
func TestSocket_HandlerRecoversFromInputsPanic(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())
	srv.attach(panickyInputs{})

	stop := startServer(t, srv)
	t.Cleanup(func() { _ = stop() })

	// Drive a request — handler will panic mid-render; conn closes
	// without response. Server must remain accepting.
	var d net.Dialer
	conn, err := d.DialContext(context.Background(), "unix", path)
	require.NoError(t, err)
	_, _ = conn.Write([]byte("status\n"))
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = io.ReadAll(conn)
	_ = conn.Close()

	// Second dial should still succeed (server didn't die).
	conn2, err := d.DialContext(context.Background(), "unix", path)
	require.NoError(t, err)
	_ = conn2.Close()
}

// panickyInputs is a StatusInputs whose Name() panics — used to
// exercise the handler's recover.
type panickyInputs struct{}

func (panickyInputs) Name() string                 { panic("boom") }
func (panickyInputs) SessionExpiresAt() time.Time  { return time.Time{} }
func (panickyInputs) RefreshWindowNext() time.Time { return time.Time{} }
func (panickyInputs) ScopeHealthy() []string       { return nil }
func (panickyInputs) ScopeStale() []string         { return nil }
func (panickyInputs) LastAuthFailure() *time.Time  { return nil }
func (panickyInputs) ChildUptime() time.Duration   { return 0 }
func (panickyInputs) DiscordConnected() bool       { return false }

// TestSocket_RunFailsParentMkdirAll — when the parent's parent is read-
// only, ensureParentMode0700 cannot MkdirAll the leaf and Run surfaces
// the wrapped error.
func TestSocket_RunFailsParentMkdirAll(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory ACLs; cannot exercise EACCES path")
	}
	root := shortTempDir(t)
	gp := filepath.Join(root, "gp")
	require.NoError(t, os.Mkdir(gp, 0o500))
	t.Cleanup(func() { _ = os.Chmod(gp, 0o700) })

	path := filepath.Join(gp, "missing", "s.sock")
	srv := NewStatusServer(path, nil, silentLogger())
	err := srv.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supervise: parent mkdir")
}

// TestSocket_EnsureParentStatError — when the grandparent directory has
// mode 0o000, os.Stat on a missing child returns EACCES (not ErrNotExist).
// ensureParentMode0700 surfaces the wrapped stat error.
func TestSocket_EnsureParentStatError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory ACLs; cannot exercise EACCES path")
	}
	root := shortTempDir(t)
	gp := filepath.Join(root, "gp")
	require.NoError(t, os.Mkdir(gp, 0o000))
	t.Cleanup(func() { _ = os.Chmod(gp, 0o700) })

	target := filepath.Join(gp, "missing")
	err := ensureParentMode0700(target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supervise: parent stat")
}

// TestSocket_DefaultRuntimeDirFallback — covers the platform-shim's
// fallback path when UserCacheDir / XDG_RUNTIME_DIR is unavailable.
func TestSocket_DefaultRuntimeDirFallback(t *testing.T) {
	// Drop HOME (darwin UserCacheDir basis) and XDG_RUNTIME_DIR (linux);
	// either way, defaultRuntimeDir must fall back to os.TempDir().
	t.Setenv("HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	got := defaultRuntimeDir()
	assert.Equal(t, os.TempDir(), got)
}

// TestSocket_StoreSnapshotPath — when a real Store is wired in, the
// server takes the Snapshot via store.Snapshot() per FR-022-16. Verifies
// the snapshotForResponse non-nil branch.
func TestSocket_StoreSnapshotPath(t *testing.T) {
	store := newTestStoreWithToken(t, []byte("payload"))
	path := tempSocketPath(t)
	srv := NewStatusServer(path, store, silentLogger())

	stop := startServer(t, srv)
	body := dialAndDrive(t, path)
	require.NoError(t, stop())

	body = bytes.TrimSpace(body)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &doc))
	assert.Equal(t, `"fetching"`, string(doc["state"]))
}

// TestSocket_NoTCPListenerOrHTTPServer — static byte-grep over the
// chunk's source files asserts absence of TCP / HTTP / bearer auth
// (SC-022-9, Constitution V).
func TestSocket_NoTCPListenerOrHTTPServer(t *testing.T) {
	files := []string{
		"pidfile.go",
		"socket.go",
		"socket_darwin.go",
		"socket_linux.go",
	}
	forbidden := []string{
		`net.listen("tcp"`,
		`http.server`,
		`http.listenandserve`,
		`bearer`,
		`authorization`,
	}
	for _, fname := range files {
		body, err := os.ReadFile(fname) //nolint:gosec // test reads its own source files
		require.NoError(t, err, "read %s", fname)
		lc := strings.ToLower(string(body))
		for _, fb := range forbidden {
			assert.False(t, strings.Contains(lc, fb), "forbidden token %q found in %s", fb, fname)
		}
	}
}

// ============================================================
// SDD-23 — verb dispatch (status | refresh) + handler wiring
// ============================================================

// dialAndDriveVerb sends the supplied verb (no trailing newline; one
// is added) and reads the full response.
func dialAndDriveVerb(t *testing.T, path, verb string) []byte {
	t.Helper()
	var d net.Dialer
	conn, err := d.DialContext(context.Background(), "unix", path)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	if _, werr := conn.Write([]byte(verb + "\n")); werr != nil {
		t.Fatalf("write verb: %v", werr)
	}
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	body, err := io.ReadAll(conn)
	require.NoError(t, err)
	return body
}

// TestSocket_VerbStatusReturnsStatusDocument — explicit "status\n"
// produces the existing FR-12 status JSON document.
func TestSocket_VerbStatusReturnsStatusDocument(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())
	srv.AttachStatusInputs(&stubStatusInputs{name: "x"})

	stop := startServer(t, srv)
	body := dialAndDriveVerb(t, path, "status")
	require.NoError(t, stop())

	body = bytes.TrimSpace(body)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &doc))
	assert.Equal(t, `"x"`, string(doc["supervisor"]))
}

// TestSocket_VerbRefreshInvokesHandler — handler returns nil, response
// is {"ok":true}\n.
func TestSocket_VerbRefreshInvokesHandler(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())
	srv.AttachRefreshHandler(func(_ context.Context) error { return nil })

	stop := startServer(t, srv)
	body := dialAndDriveVerb(t, path, "refresh")
	require.NoError(t, stop())

	assert.Equal(t, "{\"ok\":true}\n", string(body))
}

// TestSocket_VerbRefreshErrorIsSerialised — handler returns a single-
// line error, response carries the message verbatim.
func TestSocket_VerbRefreshErrorIsSerialised(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())
	srv.AttachRefreshHandler(func(_ context.Context) error {
		return errors.New("vault unreachable")
	})

	stop := startServer(t, srv)
	body := dialAndDriveVerb(t, path, "refresh")
	require.NoError(t, stop())

	assert.Equal(t, "{\"ok\":false,\"error\":\"vault unreachable\"}\n", string(body))
}

// TestSocket_VerbRefreshErrorMultilineSerialisedAsOneLine — multi-line
// error messages collapse newlines into spaces.
func TestSocket_VerbRefreshErrorMultilineSerialisedAsOneLine(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())
	srv.AttachRefreshHandler(func(_ context.Context) error {
		return errors.New("line1\nline2")
	})

	stop := startServer(t, srv)
	body := dialAndDriveVerb(t, path, "refresh")
	require.NoError(t, stop())

	assert.NotContains(t, string(body), "\nline2")
	assert.Contains(t, string(body), "line1 line2")
}

// TestSocket_VerbRefreshHandlerUnwiredReturnsStableError — no handler
// attached, refresh path returns a stable error response without
// panicking.
func TestSocket_VerbRefreshHandlerUnwiredReturnsStableError(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())

	stop := startServer(t, srv)
	body := dialAndDriveVerb(t, path, "refresh")
	require.NoError(t, stop())

	assert.Equal(t, "{\"ok\":false,\"error\":\"refresh handler not wired\"}\n", string(body))
}

// TestSocket_VerbUnrecognisedFallsBackToStatus — unknown verb still
// produces a status doc (backward-compat with SDD-22 §2.5 advisory
// payload).
func TestSocket_VerbUnrecognisedFallsBackToStatus(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())
	srv.AttachStatusInputs(&stubStatusInputs{name: "y"})

	stop := startServer(t, srv)
	body := dialAndDriveVerb(t, path, "garbage")
	require.NoError(t, stop())

	body = bytes.TrimSpace(body)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &doc))
	assert.Equal(t, `"y"`, string(doc["supervisor"]))
}

// TestSocket_VerbStatusEmptyPayloadReturnsStatusDocument — empty
// request (just "\n") returns the status doc.
func TestSocket_VerbStatusEmptyPayloadReturnsStatusDocument(t *testing.T) {
	path := tempSocketPath(t)
	srv := NewStatusServer(path, nil, silentLogger())
	srv.AttachStatusInputs(&stubStatusInputs{name: "z"})

	stop := startServer(t, srv)
	body := dialAndDriveVerb(t, path, "")
	require.NoError(t, stop())

	body = bytes.TrimSpace(body)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &doc))
	assert.Equal(t, `"z"`, string(doc["supervisor"]))
}

// TestSocket_AttachRefreshHandlerCalledTwicePanics — single-shot
// contract documented in socket-protocol.md §3.1.
func TestSocket_AttachRefreshHandlerCalledTwicePanics(t *testing.T) {
	srv := NewStatusServer("/dev/null/ignored", nil, silentLogger())
	srv.AttachRefreshHandler(func(_ context.Context) error { return nil })
	assert.Panics(t, func() {
		srv.AttachRefreshHandler(func(_ context.Context) error { return nil })
	})
}

// ============================================================
// SDD-23 — path-derivation helpers (per-OS)
// ============================================================

// TestSocketPathForSupervisor_DerivesPlatformPath — assert per-name
// path is produced under the platform runtime root.
func TestSocketPathForSupervisor_DerivesPlatformPath(t *testing.T) {
	for _, name := range []string{"alpha", "AlphaBeta_1", "x-y-z"} {
		got := SocketPathForSupervisor(name)
		assert.True(t, filepath.IsAbs(got), "path must be absolute, got %q", got)
		assert.True(t, strings.HasSuffix(got, name+".sock"), "expected suffix %q in %q", name+".sock", got)
	}
}

// TestSocketPathForSupervisor_InvalidNamePanics — slug validation per
// socket-protocol.md §4.1.
func TestSocketPathForSupervisor_InvalidNamePanics(t *testing.T) {
	assert.Panics(t, func() {
		_ = SocketPathForSupervisor("../etc")
	})
	assert.Panics(t, func() {
		_ = SocketPathForSupervisor("name with spaces")
	})
	assert.Panics(t, func() {
		_ = SocketPathForSupervisor("")
	})
}

// TestEnumerateSupervisorSockets_ListsMatchingFiles — populate the
// platform runtime dir with one matching + one non-matching file;
// assert only the matcher is returned. Drives the helper through its
// public API; the parent dir is the path returned by
// SocketPathForSupervisor.
func TestEnumerateSupervisorSockets_ListsMatchingFiles(t *testing.T) {
	root := shortTempDir(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_RUNTIME_DIR", root)

	wantPath := SocketPathForSupervisor("matched")
	require.NoError(t, os.MkdirAll(filepath.Dir(wantPath), 0o700))
	require.NoError(t, os.WriteFile(wantPath, []byte("x"), 0o600))
	noiseDir := filepath.Dir(wantPath)
	require.NoError(t, os.WriteFile(filepath.Join(noiseDir, "not-a-supervisor.sock"), []byte("x"), 0o600))

	got, err := EnumerateSupervisorSockets()
	require.NoError(t, err)
	assert.Contains(t, got, wantPath)
	for _, p := range got {
		assert.True(t, strings.HasSuffix(p, ".sock"))
		assert.NotEqual(t, "not-a-supervisor.sock", filepath.Base(p))
	}
}

// TestEnumerateSupervisorSockets_EmptyDirReturnsEmptySlice — runtime
// dir exists, but no matching files.
func TestEnumerateSupervisorSockets_EmptyDirReturnsEmptySlice(t *testing.T) {
	root := shortTempDir(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_RUNTIME_DIR", root)
	// Make sure the runtime dir exists for both schemes.
	require.NoError(t, os.MkdirAll(filepath.Dir(SocketPathForSupervisor("probe")), 0o700))

	got, err := EnumerateSupervisorSockets()
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestEnumerateSupervisorSockets_MissingDirReturnsEmptySlice — runtime
// dir does not exist.
func TestEnumerateSupervisorSockets_MissingDirReturnsEmptySlice(t *testing.T) {
	root := filepath.Join(shortTempDir(t), "does-not-exist")
	t.Setenv("HOME", root)
	t.Setenv("XDG_RUNTIME_DIR", root)
	got, err := EnumerateSupervisorSockets()
	require.NoError(t, err)
	assert.Empty(t, got)
}

// ============================================================
// Fuzz target — Constitution VIII §Mandatory fuzz targets, item 6
// ============================================================

// FuzzStatusJSON_Encode fuzzes the renderStatus encoder. Goal: panic-
// free, output unmarshals into a map[string]json.RawMessage with all 10
// FR-12 keys present (research.md R-10).
func FuzzStatusJSON_Encode(f *testing.F) {
	// Seed corpus from documented examples.
	f.Add("openclaw", "2026-04-15T13:12:00Z", "2026-04-15T16:00:00Z", "ANTHROPIC,OPENAI", "", int64(8*time.Hour+12*time.Minute), true, 51234)
	f.Add("", "0001-01-01T00:00:00Z", "0001-01-01T00:00:00Z", "", "", int64(0), false, 0)
	f.Add("supervisor", "2030-01-01T00:00:00Z", "2030-01-02T00:00:00Z", "A,B,C", "X,Y", int64(1*time.Hour), false, 1)

	f.Fuzz(func(t *testing.T, name, sea, rwn, healthy, stale string, uptimeNS int64, discord bool, pid int) {
		srv := NewStatusServer("/dev/null/ignored", nil, silentLogger())
		seaT, _ := time.Parse(time.RFC3339, sea)
		rwnT, _ := time.Parse(time.RFC3339, rwn)
		srv.attach(&stubStatusInputs{
			name:              name,
			sessionExpiresAt:  seaT,
			refreshWindowNext: rwnT,
			scopeHealthy:      splitNonEmpty(healthy),
			scopeStale:        splitNonEmpty(stale),
			childUptime:       time.Duration(uptimeNS),
			discordConnected:  discord,
		})
		snap := Snapshot{ChildPID: pid}
		body, err := srv.renderStatus(snap)
		if err != nil {
			t.Fatalf("renderStatus error: %v", err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("unmarshal: %v: %s", err, body)
		}
		for _, k := range []string{
			"supervisor", "session_expires_at", "refresh_window_next",
			"scope_healthy", "scope_stale", "last_auth_failure",
			"child_pid", "child_uptime", "discord_connected", "state",
		} {
			if _, ok := doc[k]; !ok {
				t.Fatalf("missing FR-12 key %q in %s", k, body)
			}
		}
	})
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, p := range bytes.Split([]byte(s), []byte{','}) {
		if len(p) == 0 {
			continue
		}
		out = append(out, string(p))
	}
	return out
}
