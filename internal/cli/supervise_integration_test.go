//go:build integration

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// TestSuperviseIntegration_DryRunWithDiscordStub exercises the full
// `hush supervise <fixture-config> --dry-run` path against a fake
// supervisor config and a DiscordStub. Asserts the dry-run produces a
// machine-parseable canonical-JSON payload, no Discord call is
// issued, and no pidfile / socket binding occurs.
func TestSuperviseIntegration_DryRunWithDiscordStub(t *testing.T) {
	stub := testutil.NewDiscordStub(t)

	dir := testutil.ShortTempDir(t, "h23int-")

	socketPath := filepath.Join(dir, "supervise-example.sock")
	pidPath := filepath.Join(dir, "supervise-example.pid")

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := "" +
		"name = \"example-daemon\"\n" +
		"reason = \"Example long-running daemon\"\n" +
		"server_url = \"http://100.96.10.4:7743/h/a8k2f9\"\n" +
		"client_machine_index = 2\n" +
		"session_type = \"supervisor\"\n" +
		"requested_ttl = \"20h\"\n" +
		"refresh_window = \"09:00-10:00\"\n" +
		"status_socket = \"" + socketPath + "\"\n" +
		"pid_file = \"" + pidPath + "\"\n" +
		"\n" +
		"scope = [\"ANTHROPIC_API_KEY\", \"GITHUB_TOKEN\"]\n" +
		"\n" +
		"[child]\n" +
		"command = [\"/usr/local/bin/your-daemon-binary\", \"start\"]\n" +
		"working_dir = \"/tmp\"\n" +
		"env_passthrough = [\"PATH\"]\n" +
		"\n" +
		"[validators]\n" +
		"ANTHROPIC_API_KEY = \"anthropic\"\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o600))

	var stdout, stderr bytes.Buffer
	root := newRootCmd(&outputContext{stdout: newStream(&stdout, false, true), stderr: newStream(&stderr, false, true)})
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetContext(context.Background())
	root.SetArgs([]string{"supervise", cfgPath, "--dry-run"})
	require.NoError(t, root.Execute())

	// 1. stdout is a parseable JSON document.
	body := bytes.TrimSpace(stdout.Bytes())
	var doc map[string]any
	require.NoError(t, json.Unmarshal(body, &doc), "stdout must parse as JSON: %s", body)

	assert.Equal(t, "example-daemon", doc["name"])
	assert.Equal(t, "Example long-running daemon", doc["reason"])
	assert.Equal(t, "supervisor", doc["session_type"])
	assert.Equal(t, "20h0m0s", doc["requested_ttl"])
	scope, ok := doc["scope"].([]any)
	require.True(t, ok, "scope must be a JSON array")
	assert.Equal(t, []any{"ANTHROPIC_API_KEY", "GITHUB_TOKEN"}, scope)

	// 2. Bytes are canonical (alphabetised keys, compact spacing).
	canonical, err := sign.CanonicalJSON(claimPreview{
		MachineIndex: 2,
		Name:         "example-daemon",
		Reason:       "Example long-running daemon",
		RequestedTTL: "20h0m0s",
		Scope:        []string{"ANTHROPIC_API_KEY", "GITHUB_TOKEN"},
		SessionType:  "supervisor",
	})
	require.NoError(t, err)
	assert.Equal(t, string(canonical), string(body))

	// 3. No Discord call issued.
	assert.Empty(t, stub.Calls(), "dry-run must NOT contact Discord")

	// 4. No pidfile, no socket file on disk.
	_, perr := os.Stat(pidPath)
	assert.True(t, os.IsNotExist(perr), "pidfile must NOT be acquired on dry-run")
	_, serr := os.Stat(socketPath)
	assert.True(t, os.IsNotExist(serr), "socket must NOT be bound on dry-run")
}
