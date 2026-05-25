//go:build integration

// reload.go extends the harness with reload-eligible supervisor wiring
// for Scenario 16 (T-306 Phase 8). The production CLI wiring layer that
// stands up the HTTP-proxy listener and the status-socket reload handler
// is not exported, so the harness mirrors it here against the integration
// build-tag's exported seams (AttachProxy, AttachReloadHandler,
// SwapChild). The behavior is identical to the production path; only the
// wiring frame differs.

package harness

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	superviseconfig "github.com/mrz1836/hush/internal/supervise/config"
)

// ReloadOpts configures the reload-eligible child binary and the
// [child.readiness] / [child.shutdown] / [child.handoff] sections of the
// supervisor TOML. When SupervisorOpts.Reload is non-nil, NewSupervisor
// builds the testdata/reload-child binary, points the supervisor at it,
// and stamps the readiness/handoff sections so the supervisor's
// reload-eligibility validator accepts the config.
type ReloadOpts struct {
	// Version is exposed on the child's /version endpoint and the
	// X-Child-Version response header. Empty defaults to "v0".
	Version string
	// ForceUnready, when true, makes the child return 503 from /health
	// forever — used by the readiness-failure scenario.
	ForceUnready bool
	// IgnoreSIGTERM, when true, makes the child ignore SIGTERM so the
	// supervisor's SIGKILL escalation path is exercisable.
	IgnoreSIGTERM bool
	// ReadinessTimeout overrides [child.readiness.timeout]. 0 → 2s.
	ReadinessTimeout time.Duration
	// ReadinessInterval overrides [child.readiness.interval]. 0 → 25ms.
	ReadinessInterval time.Duration
	// ShutdownGrace overrides [child.shutdown.grace]. 0 → 500ms.
	ShutdownGrace time.Duration
	// HandoffListenAddr overrides [child.handoff.listen_addr]. Empty →
	// "127.0.0.1:0" (kernel-assigned).
	HandoffListenAddr string
	// OmitReadiness, when true, leaves [child.readiness] out of the
	// generated TOML so config.Load returns ErrHandoffRequiresReadiness.
	// Used by the config-refusal sub-scenarios.
	OmitReadiness bool
	// OmitHandoff, when true, leaves [child.handoff] out of the
	// generated TOML so the supervisor lifecycle is not reload-eligible.
	OmitHandoff bool
	// InvalidHandoffMode, when non-empty, overrides [child.handoff.mode]
	// with the supplied string so config.Load returns
	// ErrHandoffModeInvalid. Useful for asserting the config-invalid
	// refusal path.
	InvalidHandoffMode string
}

// reloadChildBuild guards the singleton build of the reload-child binary.
// Each test process builds the binary once into a per-process temp dir
// the first time NewSupervisor sees a ReloadOpts; subsequent supervisors
// reuse the same binary.
//
//nolint:gochecknoglobals // per-process singleton; set once on first reload-eligible scenario
var reloadChildBuild struct {
	once sync.Once
	path string
	err  error
}

// buildReloadChildBinary compiles the testdata reload-child binary and
// caches the path. Returns the cached path on every subsequent call. Any
// error is sticky: a single compilation failure permanently disables
// reload tests until the process restarts.
func buildReloadChildBinary(t *testing.T) string {
	t.Helper()
	reloadChildBuild.once.Do(func() {
		dir, err := os.MkdirTemp("", "hush-reload-child-")
		if err != nil {
			reloadChildBuild.err = fmt.Errorf("harness: mkdir reload-child: %w", err)
			return
		}
		out := filepath.Join(dir, "reload-child")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		repoRoot, err := harnessRepoRoot()
		if err != nil {
			reloadChildBuild.err = err
			return
		}
		pkg := "./tests/integration/testdata/reload-child"
		cmd := exec.Command("go", "build", "-o", out, pkg)
		cmd.Dir = repoRoot
		cmd.Stderr = os.Stderr
		if buildErr := cmd.Run(); buildErr != nil {
			reloadChildBuild.err = fmt.Errorf("harness: build %s: %w", pkg, buildErr)
			return
		}
		reloadChildBuild.path = out
	})
	if reloadChildBuild.err != nil {
		t.Fatalf("%v", reloadChildBuild.err)
	}
	return reloadChildBuild.path
}

// harnessRepoRoot returns the absolute path to the hush module root. The
// reload-child binary is built via `go build ./tests/integration/testdata/reload-child`
// from that root.
func harnessRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("harness: runtime.Caller failed")
	}
	// tests/integration/harness/reload.go → repo root is three up.
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
		return "", fmt.Errorf("harness: cannot locate go.mod at %s: %w", root, statErr)
	}
	return root, nil
}

// reloadTOMLFragments builds the TOML strings the supervisor builder
// splices into the [child] section: the command argv, the [child.env]
// block, plus optional [child.readiness] / [child.shutdown] /
// [child.handoff] blocks. Returned strings are concatenated in
// supervisor.go's buildSupervisorConfig.
type reloadTOMLFragments struct {
	commandArgv []string
	envBlock    string
	readiness   string
	shutdown    string
	handoff     string
}

// buildReloadTOMLFragments resolves the binary path and assembles the
// TOML fragments for a reload-eligible supervisor config.
func buildReloadTOMLFragments(t *testing.T, opts *ReloadOpts) reloadTOMLFragments {
	t.Helper()
	binary := buildReloadChildBinary(t)
	version := opts.Version
	if version == "" {
		version = "v0"
	}
	listenAddr := opts.HandoffListenAddr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	readinessTimeout := opts.ReadinessTimeout
	if readinessTimeout == 0 {
		readinessTimeout = 2 * time.Second
	}
	readinessInterval := opts.ReadinessInterval
	if readinessInterval == 0 {
		readinessInterval = 25 * time.Millisecond
	}
	shutdownGrace := opts.ShutdownGrace
	if shutdownGrace == 0 {
		shutdownGrace = 500 * time.Millisecond
	}

	frag := reloadTOMLFragments{
		commandArgv: []string{binary},
	}

	frag.envBlock = "[child.env]\n"
	frag.envBlock += fmt.Sprintf("HUSH_CHILD_VERSION = %q\n", version)
	if opts.ForceUnready {
		frag.envBlock += "HUSH_CHILD_FORCE_UNREADY = \"1\"\n"
	}
	if opts.IgnoreSIGTERM {
		frag.envBlock += "HUSH_CHILD_IGNORE_SIGTERM = \"1\"\n"
	}

	if !opts.OmitReadiness {
		frag.readiness = fmt.Sprintf(`[child.readiness]
http_url = "http://127.0.0.1:0/health"
timeout = %q
interval = %q
`, readinessTimeout.String(), readinessInterval.String())
	}

	frag.shutdown = fmt.Sprintf(`[child.shutdown]
grace = %q
`, shutdownGrace.String())

	if !opts.OmitHandoff {
		mode := superviseconfig.HandoffModeHTTPProxy
		if opts.InvalidHandoffMode != "" {
			mode = opts.InvalidHandoffMode
		}
		frag.handoff = fmt.Sprintf(`[child.handoff]
mode = %q
listen_addr = %q
`, mode, listenAddr)
	}
	return frag
}

// AttachProxyForReload constructs a supervise.Proxy bound at the
// configured [child.handoff.listen_addr], points it at the boot-time
// child's private backend port, and attaches it plus a reload handler
// to the supervisor's status server. Returns the proxy so the test can
// poll through it across the swap.
//
// The boot-time child's private port is allocated by lifecycle_child.go
// when it sees [child.handoff] mode = "http-proxy". This helper blocks
// until that port is observable AND the child's /health returns 200,
// then installs it as the proxy backend. Idempotent: a second call
// returns the same proxy.
//
// Cleanup (Stop on the proxy) is registered via t.Cleanup.
func (s *TestSupervisor) AttachProxyForReload(t *testing.T) *supervise.Proxy {
	t.Helper()
	return s.attachProxy(t, true)
}

// AttachProxyForReloadSkipHealthWait is the readiness-failure-friendly
// sibling of AttachProxyForReload: it stands up the proxy and points it
// at the boot-time child's private port, but does NOT wait for /health
// to return 200. The readiness-failure scenario sets HUSH_CHILD_FORCE_UNREADY
// on every child instance, so the boot-time child returns 503 from
// /health indefinitely — the proxy still serves (forwarding the 503),
// and the swap-side readiness probe drives the test's failure path.
func (s *TestSupervisor) AttachProxyForReloadSkipHealthWait(t *testing.T) *supervise.Proxy {
	t.Helper()
	return s.attachProxy(t, false)
}

// attachProxy is the shared implementation of AttachProxyForReload and
// AttachProxyForReloadSkipHealthWait. waitHealth controls whether the
// boot-time child's /health endpoint must return 200 before the proxy
// backend pointer is set.
func (s *TestSupervisor) attachProxy(t *testing.T, waitHealth bool) *supervise.Proxy {
	t.Helper()
	if s.proxy != nil {
		return s.proxy
	}
	cfg := s.cfg
	if cfg.Child.Handoff == nil {
		t.Fatalf("harness.attachProxy: supervisor config has no [child.handoff]; not reload-eligible")
	}
	listenAddr := cfg.Child.Handoff.ListenAddr

	p := supervise.NewProxy(listenAddr, s.logger.Logger())
	if err := p.Start(t.Context()); err != nil {
		t.Fatalf("harness.attachProxy: proxy.Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	})
	registerAllowedHostExternal(p.ListenAddr())

	port := s.waitBackendPort(t, 5*time.Second)
	if waitHealth {
		waitReloadBackendReady(t, port, 5*time.Second)
	}
	if err := p.SetBackend(port); err != nil {
		t.Fatalf("harness.attachProxy: proxy.SetBackend(%d): %v", port, err)
	}
	s.lifecycle.AttachProxy(p)
	s.lifecycle.AttachReloadHandler(s.statusServerReloadHandler)
	s.proxy = p
	return p
}

// AttachReloadHandlerOnly wires the reload handler without standing up a
// proxy. Used by config-refusal scenarios where the supervisor is NOT
// reload-eligible and lifecycle.SwapChild returns ErrSwapNotEligible /
// ErrSwapProxyMissing on its own.
func (s *TestSupervisor) AttachReloadHandlerOnly(t *testing.T) {
	t.Helper()
	s.lifecycle.AttachReloadHandler(s.statusServerReloadHandler)
}

// statusServerReloadHandler is the bridge between status-socket
// `reload <json>` verbs and lifecycle.SwapChild. Production wires this
// from internal/cli; the harness pins the same signature.
func (s *TestSupervisor) statusServerReloadHandler(ctx context.Context, _ supervise.ReloadRequest) (supervise.SwapResult, error) {
	return s.lifecycle.SwapChild(ctx)
}

// waitBackendPort polls the lifecycle's allocated backend port via the
// integration-only BackendPortForTest seam until non-zero or the budget
// expires. Returns the allocated port; the /health gate is deferred to
// the caller (attachProxy gates on it conditionally).
func (s *TestSupervisor) waitBackendPort(t *testing.T, budget time.Duration) uint16 {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		port := s.lifecycle.BackendPortForTest()
		if port != 0 {
			return port
		}
		runtime.Gosched()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("harness.waitBackendPort: backend port not observed within %s", budget)
	return 0
}

// waitReloadBackendReady polls /health on 127.0.0.1:<port> until 200 or
// the budget expires. Used by AttachProxyForReload to bound supervisor
// startup against the helper child's listen-loop spin-up.
func waitReloadBackendReady(t *testing.T, port uint16, budget time.Duration) {
	t.Helper()
	u := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(budget)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, u, http.NoBody)
		resp, err := client.Do(req)
		if err == nil {
			code := resp.StatusCode
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if code == http.StatusOK {
				return
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("harness.waitReloadBackendReady: backend on port %d did not become ready within %s", port, budget)
}

// ProxyGet issues a GET against the attached proxy and returns
// (statusCode, body, headers). Test helper used by Scenario 16 to assert
// continuous availability across a swap.
func (s *TestSupervisor) ProxyGet(t *testing.T, path string) (int, string, http.Header) {
	t.Helper()
	if s.proxy == nil {
		t.Fatalf("harness.TestSupervisor.ProxyGet: proxy not attached; call AttachProxyForReload first")
	}
	addr := s.proxy.ListenAddr()
	u := "http://" + addr + path
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, http.NoBody)
	if err != nil {
		t.Fatalf("harness.ProxyGet: new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("harness.ProxyGet: GET %s: %v", u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// StatusSocketPath exposes the supervisor's Unix status socket path so
// the test can dial it via pkg/client.SupervisorStatus.
func (s *TestSupervisor) StatusSocketPath() string { return s.statusSocket }

// ConfigPath returns the on-disk path of the supervisor's TOML config
// file. The harness writes this once per supervisor; reload tests pass
// it to pkg/client.SupervisorStatus.Reload as the configPath argument.
func (s *TestSupervisor) ConfigPath() string { return s.cfgPath }
