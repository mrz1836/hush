package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
)

// shortTempBaseClient returns a temp dir short enough for macOS Unix
// sockets. Duplicates shortTempBase to keep the test file independent.
func shortTempBaseClient(t *testing.T) string {
	t.Helper()
	return testutil.ShortTempDir(t, "h23c-")
}

// fakeStatusServer accepts one connection, reads the verb line, and
// writes the supplied reply bytes followed by close. Cleanup is
// registered with t.
func fakeStatusServer(t *testing.T, reply []byte, opts ...fakeOpt) string {
	t.Helper()
	cfg := &fakeCfg{}
	for _, o := range opts {
		o(cfg)
	}
	dir := shortTempBaseClient(t)
	path := filepath.Join(dir, "fake.sock")
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
				if cfg.hangAfterAccept {
					time.Sleep(5 * time.Second)
					return
				}
				_, _ = c.Write(reply)
			}(conn)
		}
	}()
	return path
}

type fakeCfg struct {
	hangAfterAccept bool
}

type fakeOpt func(*fakeCfg)

func withHangAfterAccept() fakeOpt {
	return func(c *fakeCfg) { c.hangAfterAccept = true }
}

// statusFixture returns a canned status JSON document for fake-server
// responses.
func statusFixture(t *testing.T) []byte {
	t.Helper()
	doc := statusDoc{
		Supervisor:        "ex",
		State:             "running",
		SessionExpiresAt:  "2026-04-15T13:12:00Z",
		RefreshWindowNext: "2026-04-15T16:00:00Z",
		ScopeHealthy:      []string{"ANTHROPIC_API_KEY"},
		ScopeStale:        []string{},
		ChildUptime:       "8h12m0s",
		DiscordConnected:  true,
	}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	return append(b, '\n')
}

// runClientCmd executes a fresh client subcommand instance with the
// supplied args. Returns stdout, stderr, and the RunE error.
func runClientCmd(t *testing.T, ctx context.Context, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	streamCtx := &outputContext{stdout: newStream(&stdout, false, true), stderr: newStream(&stderr, false, true)}
	root := newRootCmd(streamCtx) //nolint:contextcheck // ctx attached via SetContext below.
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetContext(ctx)
	root.SetArgs(append([]string{"client"}, args...))
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

// withTerminalFn temporarily replaces isTerminalFn for the duration of
// the test.
func withTerminalFn(t *testing.T, fn func(uintptr) bool) {
	t.Helper()
	old := isTerminalFn
	isTerminalFn = fn
	t.Cleanup(func() { isTerminalFn = old })
}

// withTimeouts temporarily shrinks the status / refresh timeouts.
func withTimeouts(t *testing.T, status, refresh time.Duration) {
	t.Helper()
	oldS, oldR := clientStatusTimeout, clientRefreshTimeout
	clientStatusTimeout = status
	clientRefreshTimeout = refresh
	t.Cleanup(func() {
		clientStatusTimeout = oldS
		clientRefreshTimeout = oldR
	})
}

// ============================================================
// resolveSocketPath
// ============================================================

func TestClientStatus_InvalidSocketPathExitInputErr(t *testing.T) {
	_, _, err := runClientCmd(t, context.Background(), "status", "--socket", "relative/path")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSocketAmbiguous))
	assert.Equal(t, ExitInputErr, mapErr(err))
}

// ============================================================
// status — TTY / JSON / auto-detect
// ============================================================

func TestClientStatus_TTYHumanSummary(t *testing.T) {
	path := fakeStatusServer(t, statusFixture(t))
	withTerminalFn(t, func(uintptr) bool { return true })
	// Force a *os.File stdout sink via temp file; otherwise the
	// stream is a *bytes.Buffer and JSON path is taken.
	stdoutFile, err := os.CreateTemp("/tmp", "h23-stdout-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(stdoutFile.Name()) })

	var stderr bytes.Buffer
	root := newRootCmd(&outputContext{stdout: newStream(stdoutFile, true, true), stderr: newStream(&stderr, false, true)}) //nolint:contextcheck // ctx attached via SetContext below.
	root.SetOut(stdoutFile)
	root.SetErr(&stderr)
	root.SetContext(context.Background())
	root.SetArgs([]string{"client", "status", "--socket", path})
	require.NoError(t, root.Execute())
	require.NoError(t, stdoutFile.Sync())
	require.NoError(t, stdoutFile.Close())

	body, err := os.ReadFile(stdoutFile.Name())
	require.NoError(t, err)
	out := string(body)
	for _, label := range []string{
		"Supervisor:", "State:", "Child PID:", "Child up:",
		"Session expires:", "Next refresh:",
		"Healthy scopes:", "Stale scopes:", "Discord:", "Last auth fail:",
	} {
		assert.Contains(t, out, label)
	}
	assert.NotContains(t, out, "{")
	assert.NotContains(t, out, "}")
}

func TestClientStatus_PipeJSON(t *testing.T) {
	path := fakeStatusServer(t, statusFixture(t))
	withTerminalFn(t, func(uintptr) bool { return false })

	stdout, _, err := runClientCmd(t, context.Background(), "status", "--socket", path)
	require.NoError(t, err)
	// Body is the raw socket bytes + single trailing newline.
	assert.True(t, strings.HasSuffix(stdout, "\n"))
	var doc statusDoc
	require.NoError(t, json.Unmarshal(bytes.TrimSpace([]byte(stdout)), &doc))
	assert.Equal(t, "ex", doc.Supervisor)
}

func TestClientStatus_JsonFlagOverridesTTY(t *testing.T) {
	path := fakeStatusServer(t, statusFixture(t))
	withTerminalFn(t, func(uintptr) bool { return true })

	stdout, _, err := runClientCmd(t, context.Background(), "status", "--socket", path, "--json")
	require.NoError(t, err)
	var doc statusDoc
	require.NoError(t, json.Unmarshal(bytes.TrimSpace([]byte(stdout)), &doc))
	assert.Equal(t, "ex", doc.Supervisor)
}

func TestClientStatus_SocketUnreachableExitErr(t *testing.T) {
	_, stderr, err := runClientCmd(t, context.Background(), "status", "--socket", "/tmp/does-not-exist-h23.sock")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSocketUnreachable))
	assert.Equal(t, ExitErr, mapErr(err))
	assert.Contains(t, stderr, "hush: client status:")
	// no secret bytes leak — error message identifies path only
	assert.Contains(t, stderr, "/tmp/does-not-exist-h23.sock")
}

func TestClientStatus_TimeoutExitErr(t *testing.T) {
	withTimeouts(t, 200*time.Millisecond, 200*time.Millisecond)
	path := fakeStatusServer(t, nil, withHangAfterAccept())
	_, _, err := runClientCmd(t, context.Background(), "status", "--socket", path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSocketUnreachable))
	assert.Equal(t, ExitErr, mapErr(err))
}

func TestClientStatus_AutoDetectSingleSocket(t *testing.T) {
	// Set runtime dir to a temp location with a single fake sock.
	root := shortTempBaseClient(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_RUNTIME_DIR", root)

	doc := statusFixture(t)
	want := makeFakeAtSchemePath(t, root, "auto-single", doc)
	stdout, _, err := runClientCmd(t, context.Background(), "status", "--json")
	require.NoError(t, err, "stdout=%s want path=%s", stdout, want)
	assert.Contains(t, stdout, `"supervisor":"ex"`)
}

func TestClientStatus_AutoDetectZeroSocketsExitInputErr(t *testing.T) {
	root := shortTempBaseClient(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_RUNTIME_DIR", root)

	_, _, err := runClientCmd(t, context.Background(), "status")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSocketAmbiguous))
	assert.Equal(t, ExitInputErr, mapErr(err))
}

func TestClientStatus_AutoDetectMultipleSocketsExitInputErr(t *testing.T) {
	root := shortTempBaseClient(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_RUNTIME_DIR", root)
	makeFakeAtSchemePath(t, root, "one", statusFixture(t))
	makeFakeAtSchemePath(t, root, "two", statusFixture(t))

	_, stderr, err := runClientCmd(t, context.Background(), "status")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSocketAmbiguous))
	assert.Contains(t, stderr, "multiple supervisor sockets")
}

func TestClientStatus_PerOSGrepClean(t *testing.T) {
	src, err := os.ReadFile("client.go")
	require.NoError(t, err)
	body := string(src)
	for _, f := range []string{"runtime.GOOS", `net.Dial("tcp`, "http.Server", "http.ListenAndServe", `"Bearer "`} {
		assert.False(t, strings.Contains(body, f), "forbidden substring %q found in client.go", f)
	}
}

// ============================================================
// refresh
// ============================================================

func TestClientRefresh_AckMapsToExitOK(t *testing.T) {
	path := fakeStatusServer(t, []byte(`{"ok":true}`+"\n"))
	stdout, stderr, err := runClientCmd(t, context.Background(), "refresh", "--socket", path)
	require.NoError(t, err)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
}

func TestClientRefresh_ErrorMapsToExitErr(t *testing.T) {
	path := fakeStatusServer(t, []byte(`{"ok":false,"error":"vault unreachable"}`+"\n"))
	_, stderr, err := runClientCmd(t, context.Background(), "refresh", "--socket", path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSupervisorRefused))
	assert.Equal(t, ExitErr, mapErr(err))
	assert.Contains(t, stderr, "vault unreachable")
}

func TestClientRefresh_SocketUnreachableExitErr(t *testing.T) {
	_, stderr, err := runClientCmd(t, context.Background(), "refresh", "--socket", "/tmp/does-not-exist-h23-r.sock")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSocketUnreachable))
	assert.Equal(t, ExitErr, mapErr(err))
	assert.Contains(t, stderr, "hush: client refresh:")
}

func TestClientRefresh_TimeoutExitErr(t *testing.T) {
	withTimeouts(t, 200*time.Millisecond, 200*time.Millisecond)
	path := fakeStatusServer(t, nil, withHangAfterAccept())
	_, _, err := runClientCmd(t, context.Background(), "refresh", "--socket", path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSocketUnreachable))
	assert.Equal(t, ExitErr, mapErr(err))
}

func TestClientRefresh_MalformedJsonResponseExitErr(t *testing.T) {
	path := fakeStatusServer(t, []byte("garbage\n"))
	_, _, err := runClientCmd(t, context.Background(), "refresh", "--socket", path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSocketUnreachable))
	assert.Equal(t, ExitErr, mapErr(err))
}

func TestClientRefresh_NoFormatFlag(t *testing.T) {
	_, _, err := runClientCmd(t, context.Background(), "refresh", "--json")
	require.Error(t, err)
	// Cobra surfaces unknown-flag errors; not necessarily a sentinel.
	assert.NotEqual(t, ExitOK, mapErr(err))
}

// ============================================================
// helpers
// ============================================================

// makeFakeAtSchemePath binds a fake server at the platform-scheme path
// for the supplied supervisor name and returns the path.
func makeFakeAtSchemePath(t *testing.T, root, name string, reply []byte) string {
	t.Helper()
	// Trigger SocketPathForSupervisor under the overridden env vars so
	// it computes the correct platform path.
	wantPath := socketPathForSupervisor(t, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(wantPath), 0o700))
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "unix", wantPath)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(wantPath)
	})
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
				_, _ = c.Write(reply)
			}(conn)
		}
	}()
	_ = root
	return wantPath
}

// socketPathForSupervisor proxies to the supervise helper.
func socketPathForSupervisor(t *testing.T, name string) string {
	t.Helper()
	return supervise.SocketPathForSupervisor(name)
}

// _ = cobra keeps cobra in the import set.
var _ = cobra.Command{}

// TestClientStatus_NoSecretInOutput — drive every client status error
// path and assert no secret-marker bytes appear on stdout / stderr.
func TestClientStatus_NoSecretInOutput(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"socket unreachable", []string{"status", "--socket", "/tmp/no-such-h23-status.sock"}},
		{"invalid path", []string{"status", "--socket", "relative/path"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, _ := runClientCmd(t, context.Background(), c.args...)
			assert.NotContains(t, stdout, secretMarkerBytes)
			assert.NotContains(t, stderr, secretMarkerBytes)
		})
	}
}

// TestClientRefresh_NoSecretInOutput — drive every client refresh
// error path and assert no secret-marker bytes appear on stdout /
// stderr.
func TestClientRefresh_NoSecretInOutput(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"socket unreachable", []string{"refresh", "--socket", "/tmp/no-such-h23-refresh.sock"}},
		{"invalid path", []string{"refresh", "--socket", "relative/path"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, _ := runClientCmd(t, context.Background(), c.args...)
			assert.NotContains(t, stdout, secretMarkerBytes)
			assert.NotContains(t, stderr, secretMarkerBytes)
		})
	}
}
