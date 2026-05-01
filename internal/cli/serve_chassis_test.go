package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// stubInterfaceLister returns a single 100.64.x.x address so the
// chassis's tailscale_bind check passes without a real Tailscale
// interface. Mirrors the chassis's own test_support pattern.
func stubInterfaceLister(t *testing.T, listen netip.Addr) func() ([]net.Addr, error) {
	t.Helper()
	return func() ([]net.Addr, error) {
		_, ipNet, err := net.ParseCIDR(listen.String() + "/32")
		if err != nil {
			return nil, err
		}
		return []net.Addr{ipNet}, nil
	}
}

// makeRealVaultUnit is the test-only counterpart of the integration
// helper. Writes a real HUSH vault to <dir>/secrets.vault using
// testutil.NewTestVault and patches the salt header so serve's
// derivation lands on the same vault encryption key.
func makeRealVaultUnit(t *testing.T, dir string) {
	t.Helper()
	srcPath, _, _ := testutil.NewTestVault(t, map[string]string{"hello": "world"})
	dstPath := filepath.Join(dir, "secrets.vault")
	srcBytes, err := os.ReadFile(srcPath) //nolint:gosec // path comes from t.TempDir-rooted helper
	if err != nil {
		t.Fatalf("read src vault: %v", err)
	}
	patched := append([]byte(nil), srcBytes...)
	for i, b := range []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10,
	} {
		patched[5+i] = b
	}
	if err := os.WriteFile(dstPath, patched, 0o600); err != nil { //nolint:gosec // 0o600 is the correct mode for vault files
		t.Fatalf("write dst vault: %v", err)
	}
}

// writeUnitConfig produces a minimal valid TOML config for chassis
// unit-testing. Identical structure to the integration helper but
// uses the supplied listenAddr verbatim so the test can bind on
// 127.0.0.1 (with InterfaceLister stubbed to spoof the Tailscale
// check).
func writeUnitConfig(t *testing.T, dir, listenAddr, prefix string) string {
	t.Helper()
	configPath := filepath.Join(dir, "config.toml")
	clientReg := filepath.Join(dir, "clients.json")
	if err := os.WriteFile(clientReg, []byte("[]"), 0o600); err != nil {
		t.Fatalf("write client registry: %v", err)
	}
	body := "" +
		"[server]\n" +
		"listen_addr = \"" + listenAddr + "\"\n" +
		"path_prefix = \"" + prefix + "\"\n" +
		"state_dir = \"" + dir + "\"\n" +
		"audit_log = \"" + filepath.Join(dir, "audit.jsonl") + "\"\n" +
		"discord_owner_id = \"100000000000000000\"\n" +
		"client_registry = \"" + clientReg + "\"\n" +
		"\n[discord]\n" +
		"bot_token_keychain_item = \"hush-discord\"\n" +
		"application_id = \"100000000000000000\"\n" +
		"\n[crypto]\n" +
		"argon_time = 4\n" +
		"argon_memory_mb = 256\n" +
		"argon_threads = 4\n" +
		"jwt_default_ttl = \"15m\"\n" +
		"max_interactive_ttl = \"30m\"\n" +
		"max_supervisor_ttl = \"6h\"\n" +
		"default_max_uses = 5\n" +
		"nonce_ttl = \"5m\"\n" +
		"clock_skew = \"1m\"\n" +
		"claim_approval_timeout = \"30s\"\n" +
		"\n[network]\n" +
		"require_tailscale = true\n" +
		"allowed_cidrs = [\"100.64.0.0/10\", \"127.0.0.0/8\"]\n" +
		"\n[security]\n" +
		"require_file_mode_checks = true\n" +
		"require_keychain_acl = false\n" +
		"require_ntp_sync = true\n" +
		"max_clock_drift = \"1m\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath
}

// TestRunServe_ChassisLifecycle drives the full runServe composition
// path with stubbed interface lister and clock probe — covers the
// chassis-startup branches under the regular `go test` invocation
// (no integration tag required).
//
//nolint:gocognit,gocyclo // multi-step orchestration: bind → start → poll → cancel → drain
func TestRunServe_ChassisLifecycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0o700 is the chassis-required state-dir mode
		t.Fatalf("chmod: %v", err)
	}

	// Stub-listener bound to 100.64.1.1:port. We don't actually
	// listen on that address; we use a 127.0.0.1 listener and only
	// pretend (via stubInterfaceLister) that the host has a
	// Tailscale CGNAT interface.
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rawPort := listener.Addr().(*net.TCPAddr).Port
	if rawPort < 0 || rawPort > 65535 {
		t.Fatalf("port out of uint16 range: %d", rawPort)
	}
	listenPort := uint16(rawPort) //nolint:gosec // bounds-checked above
	listenAddrStr := netip.AddrPortFrom(netip.MustParseAddr("100.64.1.1"), listenPort).String()

	configPath := writeUnitConfig(t, dir, listenAddrStr, "abcdef")
	makeRealVaultUnit(t, dir)

	deps := serveDeps{
		configPath: configPath,
		passphraseSource: func(_ context.Context, _ *os.File, _ io.Writer) (*securebytes.SecureBytes, error) {
			return securebytes.New([]byte("hush-test-seed-NEVER-USE-IN-PROD"))
		},
		approverFactory: testApproverFactory,
		listener:        listener,
		interfaceLister: stubInterfaceLister(t, netip.MustParseAddr("100.64.1.1")),
		clockSyncProbe:  func(_ context.Context) (bool, time.Duration, error) { return true, 0, nil },
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	errCh := make(chan error, 1)
	go func() { errCh <- runServe(ctx, out, errStream, deps) }()

	// Poll /h/abcdef/hz until 200 or 5s.
	target := "http://" + listener.Addr().String() + "/h/abcdef/hz"
	deadline := time.Now().Add(5 * time.Second)
	var resp *http.Response
poll:
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("runServe returned early: %v; stderr=%q", err, stderr.String())
		default:
		}
		client := &http.Client{Timeout: 200 * time.Millisecond}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		r, getErr := client.Do(req)
		if getErr == nil && r.StatusCode == http.StatusOK {
			resp = r
			break poll
		}
		if r != nil {
			_ = r.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	if resp == nil {
		cancel()
		<-errCh
		t.Fatalf("/hz never returned 200 within 5s; stderr=%q", stderr.String())
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var snap healthSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("decode /hz body: %v", err)
	}
	if snap.Status != "ok" {
		t.Errorf("status = %q, want ok", snap.Status)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("runServe err = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not return within 10s of cancel")
	}

	testutil.AssertSentinelAbsent(t, testutil.SentinelSecret(14), stderr.String())
}
