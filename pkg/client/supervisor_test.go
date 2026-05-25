package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/pkg/client"
)

// fakeSocket binds a Unix listener at a short temp path, accepts a
// single connection, reads the verb line, and writes reply. Returns
// the socket path. Cleanup registers with t.
func fakeSocket(t *testing.T, reply []byte) string {
	t.Helper()
	return fakeSocketOpts(t, reply, false)
}

// fakeSocketHang accepts a connection but never writes a reply,
// forcing the client to hit its context deadline.
func fakeSocketHang(t *testing.T) string {
	t.Helper()
	return fakeSocketOpts(t, nil, true)
}

func fakeSocketOpts(t *testing.T, reply []byte, hang bool) string {
	t.Helper()
	// Use /tmp (short path) to stay under Linux/macOS UDS path limits.
	dir, err := os.MkdirTemp("/tmp", "h23p-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")

	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, aerr := listener.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 64)
				_, _ = c.Read(buf)
				if hang {
					time.Sleep(5 * time.Second)
					return
				}
				_, _ = c.Write(reply)
			}(conn)
		}
	}()
	return path
}

// statusFixture builds a canonical status JSON payload covering every
// nullable field.
func statusFixture(t *testing.T) []byte {
	t.Helper()
	pid := 4242
	lastFail := "2026-04-15T12:00:00Z"
	doc := map[string]any{
		"supervisor":          "ex",
		"state":               "running",
		"session_expires_at":  "2026-04-15T13:12:00Z",
		"session_jti":         "abc-uuid",
		"restart_count":       uint64(2),
		"refresh_window_next": "2026-04-15T16:00:00Z",
		"scope_healthy":       []string{"ANTHROPIC_API_KEY"},
		"scope_stale":         []string{"OPENAI_API_KEY"},
		"last_auth_failure":   &lastFail,
		"child_pid":           &pid,
		"child_uptime":        "8h12m0s",
		"discord_connected":   true,
	}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	return append(b, '\n')
}

// =============================================================
// Snapshot — typed parse path
// =============================================================

func TestSnapshot_Typed(t *testing.T) {
	path := fakeSocket(t, statusFixture(t))
	sup := client.NewSupervisorStatus(path)

	got, err := sup.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ex", got.Supervisor)
	assert.Equal(t, client.State("running"), got.State)
	assert.Equal(t, "abc-uuid", got.SessionJTI)
	assert.Equal(t, uint64(2), got.RestartCount)
	assert.Equal(t, time.Date(2026, 4, 15, 13, 12, 0, 0, time.UTC), got.SessionExpiresAt)
	assert.Equal(t, time.Date(2026, 4, 15, 16, 0, 0, 0, time.UTC), got.RefreshWindowNext)
	assert.Equal(t, time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC), got.LastAuthFailure)
	assert.Equal(t, []string{"ANTHROPIC_API_KEY"}, got.ScopeHealthy)
	assert.Equal(t, []string{"OPENAI_API_KEY"}, got.ScopeStale)
	assert.Equal(t, 4242, got.ChildPID)
	assert.Equal(t, 8*time.Hour+12*time.Minute, got.ChildUptime)
	assert.True(t, got.DiscordConnected)
}

func TestSnapshot_NullFieldsRenderAsZero(t *testing.T) {
	// Minimal payload — no child, no last failure, no scopes.
	body := []byte(`{
		"supervisor":"ex",
		"state":"awaiting-approval",
		"session_expires_at":"0001-01-01T00:00:00Z",
		"session_jti":"",
		"restart_count":0,
		"refresh_window_next":"0001-01-01T00:00:00Z",
		"scope_healthy":[],
		"scope_stale":[],
		"last_auth_failure":null,
		"child_pid":null,
		"child_uptime":"0s",
		"discord_connected":false
	}` + "\n")
	path := fakeSocket(t, body)
	sup := client.NewSupervisorStatus(path)

	got, err := sup.Snapshot(context.Background())
	require.NoError(t, err)
	assert.True(t, got.SessionExpiresAt.IsZero())
	assert.True(t, got.RefreshWindowNext.IsZero())
	assert.True(t, got.LastAuthFailure.IsZero())
	assert.Equal(t, 0, got.ChildPID)
	assert.Equal(t, time.Duration(0), got.ChildUptime)
}

// =============================================================
// SnapshotRaw — bytes pass-through path
// =============================================================

func TestSnapshotRaw_PreservesBytes(t *testing.T) {
	body := statusFixture(t)
	path := fakeSocket(t, body)
	sup := client.NewSupervisorStatus(path)

	got, err := sup.SnapshotRaw(context.Background())
	require.NoError(t, err)
	// Exactly one trailing newline.
	require.NotEmpty(t, got)
	assert.Equal(t, byte('\n'), got[len(got)-1])
	assert.NotEqual(t, byte('\n'), got[len(got)-2])
	// Parses cleanly as JSON — i.e. we didn't mangle the bytes.
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(got))), &raw))
	assert.Equal(t, "ex", raw["supervisor"])
}

func TestSnapshotRaw_NormalizesTrailingNewlines(t *testing.T) {
	// Server happens to emit two trailing newlines — SDK normalises to one.
	body := append(statusFixture(t), '\n')
	path := fakeSocket(t, body)
	sup := client.NewSupervisorStatus(path)

	got, err := sup.SnapshotRaw(context.Background())
	require.NoError(t, err)
	// Exactly one trailing newline; no duplicates.
	assert.Equal(t, byte('\n'), got[len(got)-1])
	assert.NotEqual(t, byte('\n'), got[len(got)-2])
}

// =============================================================
// Snapshot — error paths
// =============================================================

func TestSnapshot_SocketMissing(t *testing.T) {
	sup := client.NewSupervisorStatus("/tmp/hush-pkg-client-nope.sock")
	_, err := sup.Snapshot(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrSocketUnavailable), "got %v", err)
}

func TestSnapshot_MalformedJSON(t *testing.T) {
	path := fakeSocket(t, []byte("not json at all\n"))
	sup := client.NewSupervisorStatus(path)
	_, err := sup.Snapshot(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse), "got %v", err)
}

func TestSnapshot_BadUptimeField(t *testing.T) {
	body := []byte(`{"supervisor":"ex","state":"running","session_expires_at":"0001-01-01T00:00:00Z","refresh_window_next":"0001-01-01T00:00:00Z","scope_healthy":[],"scope_stale":[],"last_auth_failure":null,"child_pid":null,"child_uptime":"not-a-duration","discord_connected":false}` + "\n")
	path := fakeSocket(t, body)
	sup := client.NewSupervisorStatus(path)
	_, err := sup.Snapshot(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse), "got %v", err)
}

func TestSnapshot_ContextDeadline(t *testing.T) {
	path := fakeSocketHang(t)
	sup := client.NewSupervisorStatus(path)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := sup.Snapshot(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrSocketUnavailable), "got %v", err)
}

// =============================================================
// Refresh
// =============================================================

func TestRefresh_OK(t *testing.T) {
	path := fakeSocket(t, []byte(`{"ok":true}`+"\n"))
	sup := client.NewSupervisorStatus(path)
	require.NoError(t, sup.Refresh(context.Background()))
}

func TestRefresh_Denied(t *testing.T) {
	path := fakeSocket(t, []byte(`{"ok":false,"error":"vault unreachable"}`+"\n"))
	sup := client.NewSupervisorStatus(path)
	err := sup.Refresh(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrRefreshDenied), "got %v", err)
	assert.Contains(t, err.Error(), "vault unreachable")
}

func TestRefresh_Malformed(t *testing.T) {
	path := fakeSocket(t, []byte("nope\n"))
	sup := client.NewSupervisorStatus(path)
	err := sup.Refresh(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse), "got %v", err)
}

func TestRefresh_SocketMissing(t *testing.T) {
	sup := client.NewSupervisorStatus("/tmp/hush-pkg-client-nope-refresh.sock")
	err := sup.Refresh(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrSocketUnavailable), "got %v", err)
}

// =============================================================
// Reload — T-306 Phase 6 SDK coverage
// =============================================================

// fakeSocketCapturing accepts a single connection, captures the first
// up-to-512-byte request, and writes the supplied reply. The captured
// bytes are returned via the *[]byte argument once the handler has
// finished. Used by Reload tests that need to assert the wire format
// the SDK actually sends.
func fakeSocketCapturing(t *testing.T, reply []byte, captured *[]byte) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "h23r-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")

	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, aerr := listener.Accept()
		if aerr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 512)
		n, _ := conn.Read(buf)
		*captured = append((*captured)[:0], buf[:n]...)
		_, _ = conn.Write(reply)
	}()
	return path
}

func TestReload_HappyPath(t *testing.T) {
	body := []byte(`{"ok":true,"result":"ok","old_pid":4242,"new_pid":4243,"readiness_ms":150,"strategy":"http-proxy","config_path":"/etc/hush/sup.toml"}` + "\n")
	var captured []byte
	path := fakeSocketCapturing(t, body, &captured)
	sup := client.NewSupervisorStatus(path)

	res, err := sup.Reload(context.Background(), "/etc/hush/sup.toml")
	require.NoError(t, err)
	assert.Equal(t, 4242, res.OldPID)
	assert.Equal(t, 4243, res.NewPID)
	assert.Equal(t, 150*time.Millisecond, res.ReadinessDuration)
	assert.Equal(t, "http-proxy", res.Strategy)

	// Wait for the goroutine to record the captured request.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(captured) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	require.NotEmpty(t, captured, "server did not capture a request")
	assert.True(t, strings.HasPrefix(string(captured), "reload "), "wire frame must start with reload verb: %q", captured)
	assert.True(t, strings.HasSuffix(string(captured), "\n"), "wire frame must end with newline")
	// JSON body carries the operator config path.
	jsonPart := strings.TrimSuffix(strings.TrimPrefix(string(captured), "reload "), "\n")
	var sent map[string]string
	require.NoError(t, json.Unmarshal([]byte(jsonPart), &sent))
	assert.Equal(t, "/etc/hush/sup.toml", sent["config_path"])
}

func TestReload_ConfigInvalid(t *testing.T) {
	body := []byte(`{"ok":false,"result":"config-invalid","error":"swap requires [child.handoff] mode = http-proxy"}` + "\n")
	path := fakeSocket(t, body)
	sup := client.NewSupervisorStatus(path)

	_, err := sup.Reload(context.Background(), "/etc/hush/sup.toml")
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrReloadConfigInvalid), "got %v", err)
	assert.Contains(t, err.Error(), "swap requires")
}

func TestReload_ReadinessFailed(t *testing.T) {
	body := []byte(`{"ok":false,"result":"readiness-failed","error":"probe timeout"}` + "\n")
	path := fakeSocket(t, body)
	sup := client.NewSupervisorStatus(path)

	_, err := sup.Reload(context.Background(), "/etc/hush/sup.toml")
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrReloadReadinessFailed), "got %v", err)
	assert.Contains(t, err.Error(), "probe timeout")
}

func TestReload_SwapInFlight(t *testing.T) {
	body := []byte(`{"ok":false,"result":"swap-in-flight","error":"already in flight"}` + "\n")
	path := fakeSocket(t, body)
	sup := client.NewSupervisorStatus(path)

	_, err := sup.Reload(context.Background(), "/etc/hush/sup.toml")
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrReloadInFlight), "got %v", err)
}

func TestReload_UnknownResultMapsToErrReloadFailed(t *testing.T) {
	body := []byte(`{"ok":false,"result":"error","error":"backend port allocate failed"}` + "\n")
	path := fakeSocket(t, body)
	sup := client.NewSupervisorStatus(path)

	_, err := sup.Reload(context.Background(), "/etc/hush/sup.toml")
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrReloadFailed), "got %v", err)
	assert.Contains(t, err.Error(), "backend port allocate failed")
	// Must NOT match any of the specific sentinels.
	assert.False(t, errors.Is(err, client.ErrReloadConfigInvalid))
	assert.False(t, errors.Is(err, client.ErrReloadReadinessFailed))
	assert.False(t, errors.Is(err, client.ErrReloadInFlight))
}

func TestReload_SocketMissingMapsToUnreachable(t *testing.T) {
	sup := client.NewSupervisorStatus("/tmp/hush-pkg-client-nope-reload.sock")
	_, err := sup.Reload(context.Background(), "/etc/hush/sup.toml")
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrSocketUnavailable), "got %v", err)
}

func TestReload_MalformedResponseIsInvalid(t *testing.T) {
	path := fakeSocket(t, []byte("not json\n"))
	sup := client.NewSupervisorStatus(path)
	_, err := sup.Reload(context.Background(), "/etc/hush/sup.toml")
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse), "got %v", err)
}

func TestReload_EmptyConfigPathStillSendsValidJSON(t *testing.T) {
	body := []byte(`{"ok":true,"result":"ok","old_pid":1,"new_pid":2,"strategy":"http-proxy"}` + "\n")
	var captured []byte
	path := fakeSocketCapturing(t, body, &captured)
	sup := client.NewSupervisorStatus(path)

	_, err := sup.Reload(context.Background(), "")
	require.NoError(t, err)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(captured) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	require.NotEmpty(t, captured)
	jsonPart := strings.TrimSuffix(strings.TrimPrefix(string(captured), "reload "), "\n")
	var sent map[string]string
	require.NoError(t, json.Unmarshal([]byte(jsonPart), &sent))
	assert.Empty(t, sent["config_path"])
}

// =============================================================
// Misc
// =============================================================

func TestSocketPath_Accessor(t *testing.T) {
	sup := client.NewSupervisorStatus("/some/path")
	assert.Equal(t, "/some/path", sup.SocketPath())
}

func TestClose_NoOp(t *testing.T) {
	sup := client.NewSupervisorStatus("/some/path")
	assert.NoError(t, sup.Close())
}
