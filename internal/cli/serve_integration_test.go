//go:build integration

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

	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// hasTailscaleAddr mirrors internal/server/integration_test.go —
// returns the first 100.64.0.0/10 address on the host.
func hasTailscaleAddr() (string, bool) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", false
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		if ip4[0] == 100 && (ip4[1]&0xC0) == 64 {
			return ip4.String(), true
		}
	}
	return "", false
}

// writeTestConfig produces a minimal valid TOML config that
// config.LoadServer will accept.
func writeTestConfig(t *testing.T, dir, listenAddr, prefix string) string {
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
		"allowed_cidrs = [\"100.64.0.0/10\"]\n" +
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

// makeRealVault writes a real HUSH-format vault file to <dir>/secrets.vault
// using testutil.NewTestKeys-derived encryption, then patches the
// file header's salt bytes to the testutil testSalt so serve's
// header-based KDF lands on the same vault encryption key.
func makeRealVault(t *testing.T, dir string) {
	t.Helper()
	srcPath, _, _ := testutil.NewTestVault(t, map[string]string{"hello": "world"})
	dstPath := filepath.Join(dir, "secrets.vault")
	srcBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read src vault: %v", err)
	}
	// Bytes 5..21 are the salt field. Overwrite with the same salt
	// testutil uses for its deterministic master-seed derivation so
	// serve's salt-from-header → KDF chain yields the same vault
	// encryption key.
	patched := append([]byte(nil), srcBytes...)
	for i, b := range []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10,
	} {
		patched[5+i] = b
	}
	if err := os.WriteFile(dstPath, patched, 0o600); err != nil {
		t.Fatalf("write dst vault: %v", err)
	}
}

// TestServe_StartAndShutdown is the AC-1 integration test: serve
// brings the chassis online, /hz returns 200, ctx-cancel triggers
// clean shutdown.
func TestServe_StartAndShutdown(t *testing.T) {
	tsAddr, ok := hasTailscaleAddr()
	if !ok {
		t.Skip("no Tailscale CGNAT address on this host")
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	listener, err := net.Listen("tcp", tsAddr+":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listenAddr := listener.Addr().(*net.TCPAddr)
	listenAddrStr := netip.AddrPortFrom(netip.MustParseAddr(tsAddr), uint16(listenAddr.Port)).String()

	configPath := writeTestConfig(t, dir, listenAddrStr, "abcdef")
	makeRealVault(t, dir)

	// Override the chassis's interface lister via a custom
	// approverFactory that doesn't matter — but we DO need to
	// disable the chassis-internal interface lister; the test config
	// already binds to a real Tailscale-CGNAT addr so the check
	// passes naturally.

	deps := serveDeps{
		configPath: configPath,
		passphraseSource: func(_ context.Context, _ *os.File, _ io.Writer) (*securebytes.SecureBytes, error) {
			return securebytes.New([]byte("hush-test-seed-NEVER-USE-IN-PROD"))
		},
		approverFactory: testApproverFactory,
		listener:        listener,
		clockSyncProbe:  func(_ context.Context) (bool, time.Duration, error) { return true, 0, nil },
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)

	errCh := make(chan error, 1)
	go func() { errCh <- runServe(ctx, out, errStream, deps) }()

	// Poll /hz until 200 OK or 5s timeout — but exit early if
	// runServe returned an error before binding.
	target := "http://" + listenAddrStr + "/h/abcdef/hz"
	deadline := time.Now().Add(5 * time.Second)
	var resp *http.Response
poll:
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("runServe returned early: %v; stderr=%q", err, stderr.String())
		default:
		}
		client := &http.Client{Timeout: 500 * time.Millisecond}
		r, getErr := client.Get(target)
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
		t.Fatalf("decode /hz body: %v (raw=%q)", err, string(body))
	}
	if snap.Status != "ok" {
		t.Errorf("status = %q, want ok", snap.Status)
	}

	// Trigger clean shutdown.
	cancel()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("runServe err = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not return within 10s of cancel")
	}

	// Sentinel-leak assertion on captured stderr.
	testutil.AssertSentinelAbsent(t, testutil.SentinelSecret(14), stderr.String())
}

// TestServe_BadPassphrase_ExitAuth asserts that a wrong passphrase
// surfaces vault.ErrAuthFailed mapping to ExitAuth.
func TestServe_BadPassphrase_ExitAuth(t *testing.T) {
	tsAddr, ok := hasTailscaleAddr()
	if !ok {
		t.Skip("no Tailscale CGNAT address on this host")
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	listener, err := net.Listen("tcp", tsAddr+":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	listenAddr := listener.Addr().(*net.TCPAddr)
	listenAddrStr := netip.AddrPortFrom(netip.MustParseAddr(tsAddr), uint16(listenAddr.Port)).String()

	configPath := writeTestConfig(t, dir, listenAddrStr, "abcdef")
	makeRealVault(t, dir)

	deps := serveDeps{
		configPath: configPath,
		passphraseSource: func(_ context.Context, _ *os.File, _ io.Writer) (*securebytes.SecureBytes, error) {
			return securebytes.New([]byte("WRONG-passphrase-12345"))
		},
		approverFactory: testApproverFactory,
		listener:        listener,
		clockSyncProbe:  func(_ context.Context) (bool, time.Duration, error) { return true, 0, nil },
	}

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	err = runServe(t.Context(), out, errStream, deps)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := mapErr(err); got != ExitAuth {
		t.Errorf("mapErr = %d, want ExitAuth", got)
	}
}

// silenceUnused keeps the imports satisfied across builds where no
// chassis types are referenced.
var _ = server.ErrAlreadyRun
