package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSuperviseReloadConfig writes a valid reload-eligible supervisor
// TOML whose status_socket points at the supplied path. Other fields
// are valid but not exercised by the CLI's reload path — they're only
// here so config.Load succeeds.
func writeSuperviseReloadConfig(t *testing.T, dir, socketPath string) string {
	t.Helper()
	pidPath := filepath.Join(dir, "supervise-reload.pid")
	lines := []string{
		`name = "reload-daemon"`,
		`reason = "Reload-eligible daemon"`,
		`server_url = "http://100.96.10.4:7743/h/a8k2f9"`,
		`client_machine_index = 2`,
		`session_type = "supervisor"`,
		`requested_ttl = "20h"`,
		`refresh_window = "09:00-10:00"`,
		fmt.Sprintf(`status_socket = %q`, socketPath),
		fmt.Sprintf(`pid_file = %q`, pidPath),
		``,
		`scope = ["ANTHROPIC_API_KEY"]`,
		``,
		`[child]`,
		`command = ["/usr/local/bin/your-daemon-binary", "start", "--port=$HUSH_BIND_PORT"]`,
		`working_dir = "/tmp"`,
		`env_passthrough = ["PATH"]`,
		``,
		`[child.readiness]`,
		`http_url = "http://127.0.0.1:0/health"`,
		`timeout = "30s"`,
		`interval = "200ms"`,
		``,
		`[child.shutdown]`,
		`grace = "30s"`,
		``,
		`[child.handoff]`,
		`mode = "http-proxy"`,
		`listen_addr = "127.0.0.1:8080"`,
		``,
		`[validators]`,
		`ANTHROPIC_API_KEY = "anthropic"`,
	}
	configPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return configPath
}

// reloadFakeSocket starts a Unix-domain socket at path inside dir, and
// for every accepted connection captures the first up-to-512-byte
// request and writes reply. captured is updated atomically per
// connection — tests read it once the round-trip has completed. The
// listener is closed via t.Cleanup.
func reloadFakeSocket(t *testing.T, dir string, reply []byte, captured *[]byte) string {
	t.Helper()
	path := filepath.Join(dir, "supervisor.sock")

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
				buf := make([]byte, 512)
				n, _ := c.Read(buf)
				if captured != nil {
					*captured = append((*captured)[:0], buf[:n]...)
				}
				_, _ = c.Write(reply)
			}(conn)
		}
	}()
	return path
}

// newSuperviseReloadCmdForTest mirrors newSuperviseCmdForTest for the
// reload subcommand — runs `supervise reload <args...>` against a
// fresh root command so cobra dispatch into the subcommand is
// exercised end-to-end.
func newSuperviseReloadCmdForTest(t *testing.T, ctx context.Context, stdout, stderr *bytes.Buffer, args ...string) *cobra.Command {
	t.Helper()
	root := newRootCmd(&outputContext{stdout: newStream(stdout, false, true), stderr: newStream(stderr, false, true)}) //nolint:contextcheck // ctx is attached via SetContext below.
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetContext(ctx)
	root.SetArgs(append([]string{"supervise", "reload"}, args...))
	return root
}

// TestSuperviseReload_HappyPath — fake socket replies with ok ack;
// CLI prints the locked `hush: supervise: reload: ok ...` line and
// returns nil. Also asserts the wire frame the SDK sent is the
// `reload {json}\n` shape the supervisor parses.
func TestSuperviseReload_HappyPath(t *testing.T) {
	dir := shortTempBase(t)
	ack := []byte(`{"ok":true,"result":"ok","old_pid":4242,"new_pid":4243,"readiness_ms":150,"strategy":"http-proxy"}` + "\n")
	var captured []byte
	socketPath := reloadFakeSocket(t, dir, ack, &captured)
	cfgPath := writeSuperviseReloadConfig(t, dir, socketPath)

	var stdout, stderr bytes.Buffer
	root := newSuperviseReloadCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath)
	require.NoError(t, root.Execute())
	assert.Empty(t, stderr.String(), "stderr must be empty on success")
	assert.Contains(t, stdout.String(), "hush: supervise: reload: ok (readiness 150ms, strategy http-proxy)")

	// Wait for the goroutine to record the captured request.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(captured) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	require.NotEmpty(t, captured, "fake socket did not capture a request")
	require.True(t, strings.HasPrefix(string(captured), "reload "), "wire frame must start with reload verb: %q", captured)
	require.True(t, strings.HasSuffix(string(captured), "\n"), "wire frame must end with newline")
	jsonPart := strings.TrimSuffix(strings.TrimPrefix(string(captured), "reload "), "\n")
	var sent map[string]string
	require.NoError(t, json.Unmarshal([]byte(jsonPart), &sent))
	assert.Equal(t, cfgPath, sent["config_path"])
}

// TestSuperviseReload_ConfigInvalid — supervisor replies with
// config-invalid; CLI returns an error that maps to ExitInputErr
// because the operator's running config is not zero-downtime
// eligible and they need to fix it.
func TestSuperviseReload_ConfigInvalid(t *testing.T) {
	dir := shortTempBase(t)
	ack := []byte(`{"ok":false,"result":"config-invalid","error":"swap requires [child.handoff] mode = http-proxy"}` + "\n")
	socketPath := reloadFakeSocket(t, dir, ack, nil)
	cfgPath := writeSuperviseReloadConfig(t, dir, socketPath)

	var stdout, stderr bytes.Buffer
	root := newSuperviseReloadCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath)
	err := root.Execute()
	require.Error(t, err)
	assert.True(t, errors.Is(err, errReloadConfigInvalid), "got %v", err)
	assert.Equal(t, ExitInputErr, mapErr(err))
	assert.Contains(t, stderr.String(), "hush: supervise: reload:")
	assert.Contains(t, stderr.String(), "swap requires")
	assert.Empty(t, stdout.String(), "stdout must be empty on failure")
}

// TestSuperviseReload_ReadinessFailed — supervisor replies with
// readiness-failed; CLI returns an error that maps to ExitErr (the
// reload itself is an operational failure, not operator input).
func TestSuperviseReload_ReadinessFailed(t *testing.T) {
	dir := shortTempBase(t)
	ack := []byte(`{"ok":false,"result":"readiness-failed","error":"probe timeout after 30s"}` + "\n")
	socketPath := reloadFakeSocket(t, dir, ack, nil)
	cfgPath := writeSuperviseReloadConfig(t, dir, socketPath)

	var stdout, stderr bytes.Buffer
	root := newSuperviseReloadCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath)
	err := root.Execute()
	require.Error(t, err)
	assert.True(t, errors.Is(err, errReloadReadinessFailed), "got %v", err)
	assert.Equal(t, ExitErr, mapErr(err))
	assert.Contains(t, stderr.String(), "hush: supervise: reload:")
	assert.Contains(t, stderr.String(), "probe timeout")
}

// TestSuperviseReload_SwapInFlight — supervisor replies with
// swap-in-flight; CLI returns an error that maps to ExitErr so
// operators can retry once the in-flight reload completes.
func TestSuperviseReload_SwapInFlight(t *testing.T) {
	dir := shortTempBase(t)
	ack := []byte(`{"ok":false,"result":"swap-in-flight","error":"another reload is in progress"}` + "\n")
	socketPath := reloadFakeSocket(t, dir, ack, nil)
	cfgPath := writeSuperviseReloadConfig(t, dir, socketPath)

	var stdout, stderr bytes.Buffer
	root := newSuperviseReloadCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath)
	err := root.Execute()
	require.Error(t, err)
	assert.True(t, errors.Is(err, errReloadInFlight), "got %v", err)
	assert.Equal(t, ExitErr, mapErr(err))
	assert.Contains(t, stderr.String(), "hush: supervise: reload:")
}

// TestSuperviseReload_UnknownResult — supervisor replies with an
// unfamiliar result code; CLI falls back to ExitErr and includes the
// supervisor's reason string in stderr.
func TestSuperviseReload_UnknownResult(t *testing.T) {
	dir := shortTempBase(t)
	ack := []byte(`{"ok":false,"result":"error","error":"backend port allocate failed"}` + "\n")
	socketPath := reloadFakeSocket(t, dir, ack, nil)
	cfgPath := writeSuperviseReloadConfig(t, dir, socketPath)

	var stdout, stderr bytes.Buffer
	root := newSuperviseReloadCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath)
	err := root.Execute()
	require.Error(t, err)
	assert.True(t, errors.Is(err, errReloadFailed), "got %v", err)
	assert.Equal(t, ExitErr, mapErr(err))
	assert.Contains(t, stderr.String(), "backend port allocate failed")
}

// TestSuperviseReload_SocketUnreachable — config points at a path
// where no supervisor is listening; CLI returns errSocketUnreachable
// which maps to ExitErr.
func TestSuperviseReload_SocketUnreachable(t *testing.T) {
	dir := shortTempBase(t)
	socketPath := filepath.Join(dir, "missing.sock")
	cfgPath := writeSuperviseReloadConfig(t, dir, socketPath)

	var stdout, stderr bytes.Buffer
	root := newSuperviseReloadCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath)
	err := root.Execute()
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSocketUnreachable), "got %v", err)
	assert.Equal(t, ExitErr, mapErr(err))
	assert.Contains(t, stderr.String(), "hush: supervise: reload:")
}

// TestSuperviseReload_ConfigNotFound — missing config path is caught
// locally before any socket I/O and surfaces as ExitNotFound.
func TestSuperviseReload_ConfigNotFound(t *testing.T) {
	dir := shortTempBase(t)
	missing := filepath.Join(dir, "does-not-exist.toml")

	var stdout, stderr bytes.Buffer
	root := newSuperviseReloadCmdForTest(t, context.Background(), &stdout, &stderr, missing)
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, ExitNotFound, mapErr(err))
	assert.Contains(t, stderr.String(), "hush: supervise: reload:")
	assert.Empty(t, stdout.String())
}

// TestSuperviseReload_ConfigInvalidTOML — malformed config is caught
// locally and surfaces as ExitInputErr; no socket I/O is attempted.
func TestSuperviseReload_ConfigInvalidTOML(t *testing.T) {
	dir := shortTempBase(t)
	bad := filepath.Join(dir, "bad.toml")
	require.NoError(t, os.WriteFile(bad, []byte("not toml ====\n"), 0o600))

	var stdout, stderr bytes.Buffer
	root := newSuperviseReloadCmdForTest(t, context.Background(), &stdout, &stderr, bad)
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, ExitInputErr, mapErr(err))
	assert.Empty(t, stdout.String())
}

// TestSuperviseReload_DoesNotBreakParentSupervise — running the parent
// `supervise <config-path>` with a path that is NOT the literal
// "reload" still dispatches to the parent's RunE, not the subcommand.
// Exercises the cobra dispatch table the AC-1/AC-2 contract relies on.
func TestSuperviseReload_DoesNotBreakParentSupervise(t *testing.T) {
	dir := shortTempBase(t)
	cfgPath := writeSuperviseConfig(t, dir)

	var stdout, stderr bytes.Buffer
	root := newSuperviseCmdForTest(t, context.Background(), &stdout, &stderr, cfgPath, "--dry-run")
	require.NoError(t, root.Execute())
	// Dry-run prints the canonical claim payload — proving the parent
	// supervise RunE ran, not the reload subcommand.
	assert.Contains(t, stdout.String(), `"name":"example-daemon"`)
}

// TestSuperviseReload_RequiresConfigArg — invoking the subcommand with
// no positional argument fails cobra ExactArgs(1) validation.
func TestSuperviseReload_RequiresConfigArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newSuperviseReloadCmdForTest(t, context.Background(), &stdout, &stderr)
	err := root.Execute()
	require.Error(t, err)
}

// TestSuperviseReload_PrintErrCollapsesNewlines — multi-line error
// messages render as one line on stderr.
func TestSuperviseReload_PrintErrCollapsesNewlines(t *testing.T) {
	var buf bytes.Buffer
	printSuperviseReloadErr(&buf, errors.New("line1\nline2"))
	out := buf.String()
	assert.NotContains(t, out, "line1\nline2")
	assert.Contains(t, out, "line1 line2")
	assert.Contains(t, out, "hush: supervise: reload:")
}
