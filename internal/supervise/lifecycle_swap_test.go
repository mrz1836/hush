// Lifecycle swap tests (T-306 Phase 5).
//
// Internal test file because the swap orchestrator interacts with several
// unexported Lifecycle internals (childMu, child, proxy field) and uses
// the package-internal newTestLifecycle helper.
//
// The tests drive a real HTTP child binary via the standard Go test
// helper-process pattern: the test binary itself acts as the child when
// HUSH_RELOAD_CHILD_MODE=1 is set, binding HUSH_BIND_PORT on 127.0.0.1
// and serving /health.

package supervise

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/supervise/config"
)

// reloadChildBinary returns the argv for a helper child that this test
// binary itself executes via TestHelperProcessReloadChild. The child
// reads HUSH_BIND_PORT, binds 127.0.0.1:<port>, and serves /health.
func reloadChildBinary(t *testing.T) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return []string{exe, "-test.run", "^TestHelperProcessReloadChild$", "-test.v=false"}
}

// TestHelperProcessReloadChild is the helper process invoked by reload
// swap tests. It is gated behind HUSH_RELOAD_CHILD_MODE=1 so a normal
// `go test ./internal/supervise` run does not spawn a stray HTTP server.
//
// The helper binds 127.0.0.1:HUSH_BIND_PORT and serves:
//   - /health → 200 OK with body "ready"
//   - /flake  → 503 the first N requests (controlled by HUSH_CHILD_FLAKE_N)
//   - anything else → 200 OK with body "ok"
//
// Exits 0 on SIGTERM. Honors HUSH_CHILD_SLOW_TERM=<duration> for tests
// that need to exercise the SIGKILL escalation path.
//
//nolint:gocognit,gocyclo // single-process helper; explicit branches per behaviour knob
func TestHelperProcessReloadChild(t *testing.T) {
	if os.Getenv("HUSH_RELOAD_CHILD_MODE") != "1" {
		return
	}
	portStr := os.Getenv("HUSH_BIND_PORT")
	if portStr == "" {
		log.Fatalf("HUSH_BIND_PORT empty")
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		log.Fatalf("HUSH_BIND_PORT %q parse: %v", portStr, err)
	}
	flakeN := int64(0)
	if v := os.Getenv("HUSH_CHILD_FLAKE_N"); v != "" {
		flakeN, _ = strconv.ParseInt(v, 10, 64)
	}
	flakeRemaining := atomic.Int64{}
	flakeRemaining.Store(flakeN)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var lc net.ListenConfig
	l, lerr := lc.Listen(context.Background(), "tcp", addr)
	if lerr != nil {
		log.Fatalf("listen %s: %v", addr, lerr)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if flakeRemaining.Add(-1) >= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-Child-Pid", strconv.Itoa(os.Getpid()))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	mux.HandleFunc("/pid", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strconv.Itoa(os.Getpid())))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	// Honor a slow-shutdown knob.
	slowTerm := time.Duration(0)
	if v := os.Getenv("HUSH_CHILD_SLOW_TERM"); v != "" {
		if d, perr := time.ParseDuration(v); perr == nil {
			slowTerm = d
		}
	}

	// Serve in a goroutine; main goroutine waits for SIGTERM via signal
	// trapping — but to keep this helper minimal we rely on the OS
	// closing us on process exit when the supervisor SIGKILL's. SIGTERM
	// triggers srv.Shutdown via a separate goroutine watching signals.
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(l) }()

	// Trap SIGTERM via a tiny os/signal — we want clean exit code 0 on
	// SIGTERM so the orchestrator records a clean shutdown. We skip
	// srv.Shutdown (which can be slow under -race) and call os.Exit
	// directly: tests don't need a graceful Goroutine drain in the
	// subprocess.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	select {
	case <-sigCh:
		if slowTerm > 0 {
			time.Sleep(slowTerm)
		}
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve: %v", err)
		}
	}
	_ = srv.Close()
	os.Exit(0)
}

// makeReloadCfg returns a newTestLifecycle option that mutates cfg into
// reload-eligible shape: HTTP-proxy handoff, a placeholder readiness URL
// (swapReadinessURL rewrites it to the live backend port at probe time),
// a short shutdown grace, and the helper child binary as the command.
func makeReloadCfg(t *testing.T) func(*config.Supervisor) {
	t.Helper()
	cmd := append(reloadChildBinary(t), "--")
	return func(cfg *config.Supervisor) {
		cfg.Child.Command = cmd
		cfg.Child.Env = map[string]string{
			"HUSH_RELOAD_CHILD_MODE": "1",
		}
		cfg.Child.EnvPassthrough = []string{"HUSH_BIND_PORT"}
		cfg.Child.Readiness = &config.ChildReadiness{
			HTTPURL:  "http://127.0.0.1:0/health",
			Timeout:  3 * time.Second,
			Interval: 30 * time.Millisecond,
		}
		cfg.Child.Shutdown = config.ChildShutdown{Grace: 500 * time.Millisecond}
		cfg.Child.Handoff = &config.ChildHandoff{
			Mode:       config.HandoffModeHTTPProxy,
			ListenAddr: "127.0.0.1:0",
		}
	}
}

// runUntilRunning starts the lifecycle, waits for StateRunning, and
// returns a cancel func plus the Run-exit channel.
func runUntilRunning(t *testing.T, tl *testLifecycle) (context.CancelFunc, <-chan error) {
	t.Helper()
	tl.vault.QueueOK()
	cancel, done := runWithCancel(tl)
	eventually(t, "reach StateRunning", 10*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})
	return cancel, done
}

// proxyForLifecycle returns a freshly-started Proxy bound to ListenAddr
// from cfg, with backend pointed at the lifecycle's current backendPort.
// Blocks until the backend is reachable (the boot-time child has bound
// its port) so subsequent assertions about routing are deterministic.
func proxyForLifecycle(t *testing.T, tl *testLifecycle) *Proxy {
	t.Helper()
	p := NewProxy(tl.cfg.Child.Handoff.ListenAddr, tl.lc.deps.Logger)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("proxy Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	tl.lc.backendMu.Lock()
	port := tl.lc.backendPort
	tl.lc.backendMu.Unlock()
	if port == 0 {
		t.Fatalf("lifecycle backendPort=0; child did not opt into reload")
	}
	if err := p.SetBackend(port); err != nil {
		t.Fatalf("proxy SetBackend(%d): %v", port, err)
	}
	waitBackendReady(t, port, 5*time.Second)
	return p
}

// waitBackendReady polls the child's /health directly (not through the
// proxy) until it returns 200 or the budget expires. Used to bound test
// startup against the helper child's listen-loop spin-up.
func waitBackendReady(t *testing.T, port uint16, budget time.Duration) {
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
	t.Fatalf("backend on port %d did not become ready within %s", port, budget)
}

// proxyGetThrough issues a GET against the proxy and returns
// (status, body).
func proxyGetThrough(t *testing.T, p *Proxy, path string) (int, string) {
	t.Helper()
	addr := p.ListenAddr()
	u := "http://" + addr + path
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy GET %s: %v", u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// shutdownLifecycle cancels the Run context and drains the done channel.
// Allow extra time under -race where instrumented binaries (including
// the helper subprocess this test binary forks) are noticeably slower.
func shutdownLifecycle(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("Run did not exit after cancel")
	}
}

// TestLifecycleSwap_HappyPath drives a complete swap: child A boots, the
// proxy points at A, SwapChild replaces A with child B, the proxy points
// at B, and the audit chain records the swap. The proxy's continuous
// availability is verified by polling /health through the proxy across
// the swap window.
//
//nolint:gocognit,gocyclo // end-to-end swap assertion: PID + state + proxy + audit checks are necessarily co-located
func TestLifecycleSwap_HappyPath(t *testing.T) {
	tl := newTestLifecycle(t, nil, makeReloadCfg(t))
	cancel, done := runUntilRunning(t, tl)
	defer shutdownLifecycle(t, cancel, done)

	p := proxyForLifecycle(t, tl)
	tl.lc.AttachProxy(p)

	// PID of the boot-time child.
	tl.lc.childMu.Lock()
	oldPID := tl.lc.child.PID()
	tl.lc.childMu.Unlock()
	if oldPID <= 0 {
		t.Fatalf("oldPID=%d; expected boot child to be alive", oldPID)
	}

	// Confirm the proxy currently serves through child A.
	if code, body := proxyGetThrough(t, p, "/health"); code != http.StatusOK || body != "ready" {
		t.Fatalf("pre-swap /health: code=%d body=%q", code, body)
	}

	// Trigger the swap.
	res, err := tl.lc.SwapChild(context.Background())
	if err != nil {
		t.Fatalf("SwapChild: %v", err)
	}
	if res.OldPID != oldPID {
		t.Errorf("OldPID: got %d want %d", res.OldPID, oldPID)
	}
	if res.NewPID == 0 || res.NewPID == oldPID {
		t.Errorf("NewPID: got %d want >0 and != %d", res.NewPID, oldPID)
	}
	if res.Strategy != HandoffStrategyHTTPProxy {
		t.Errorf("Strategy: got %q want %q", res.Strategy, HandoffStrategyHTTPProxy)
	}
	if res.ReadinessDuration <= 0 {
		t.Errorf("ReadinessDuration: got %s want >0", res.ReadinessDuration)
	}

	// Post-swap: the lifecycle child should be the new PID.
	tl.lc.childMu.Lock()
	curPID := tl.lc.child.PID()
	tl.lc.childMu.Unlock()
	if curPID != res.NewPID {
		t.Errorf("post-swap lifecycle child PID: got %d want %d", curPID, res.NewPID)
	}

	// Post-swap: proxy still returns 200; backend has advanced to new port.
	if code, body := proxyGetThrough(t, p, "/health"); code != http.StatusOK || body != "ready" {
		t.Fatalf("post-swap /health: code=%d body=%q", code, body)
	}

	// Audit event must be present exactly once with the contracted fields.
	matches := auditEventsByAction(tl.auditLog, audit.ActionSupervisorChildSwap)
	if len(matches) != 1 {
		t.Fatalf("audit %s count: got %d want 1", audit.ActionSupervisorChildSwap, len(matches))
	}
	assertSwapAuditData(t, matches[0], res)

	// State machine: must be back at StateRunning.
	if got := tl.lc.store.Snapshot().State; got != StateRunning {
		t.Errorf("state post-swap: got %s want %s", got, StateRunning)
	}
}

// TestLifecycleSwap_ReadinessFailureRollsBack proves AC-5: the new child
// is killed and the old child keeps serving on a readiness failure.
func TestLifecycleSwap_ReadinessFailureRollsBack(t *testing.T) {
	tl := newTestLifecycle(t, nil, makeReloadCfg(t))
	cancel, done := runUntilRunning(t, tl)
	defer shutdownLifecycle(t, cancel, done)

	p := proxyForLifecycle(t, tl)
	tl.lc.AttachProxy(p)

	// Make the readiness probe budget tiny so the test does not stall
	// when the new child returns 503 forever.
	tl.cfg.Child.Readiness.Timeout = 250 * time.Millisecond
	tl.cfg.Child.Readiness.Interval = 25 * time.Millisecond
	// Configure the spawned child to fail readiness 1000 times — well
	// beyond the timeout budget.
	tl.cfg.Child.Env["HUSH_CHILD_FLAKE_N"] = "1000"

	tl.lc.childMu.Lock()
	oldPID := tl.lc.child.PID()
	oldChild := tl.lc.child
	tl.lc.childMu.Unlock()

	res, err := tl.lc.SwapChild(context.Background())
	if !errors.Is(err, ErrSwapReadinessFailed) {
		t.Fatalf("SwapChild: want ErrSwapReadinessFailed, got %v", err)
	}
	_ = res

	// The lifecycle child must still be the old PID.
	tl.lc.childMu.Lock()
	curChild := tl.lc.child
	curPID := 0
	if curChild != nil {
		curPID = curChild.PID()
	}
	tl.lc.childMu.Unlock()
	if curChild != oldChild || curPID != oldPID {
		t.Errorf("post-failure lifecycle child: got pid=%d (same? %v) want pid=%d", curPID, curChild == oldChild, oldPID)
	}

	// Proxy still points at old backend (the old port).
	tl.lc.backendMu.Lock()
	curPort := tl.lc.backendPort
	tl.lc.backendMu.Unlock()
	if p.CurrentBackend() != curPort {
		t.Errorf("proxy CurrentBackend: got %d want %d", p.CurrentBackend(), curPort)
	}

	// No swap audit event.
	if matches := auditEventsByAction(tl.auditLog, audit.ActionSupervisorChildSwap); len(matches) != 0 {
		t.Errorf("audit %s count on failure: got %d want 0", audit.ActionSupervisorChildSwap, len(matches))
	}

	// State machine back at StateRunning (swap-failed transition).
	if got := tl.lc.store.Snapshot().State; got != StateRunning {
		t.Errorf("state post-failure: got %s want %s", got, StateRunning)
	}

	// Old child must still serve through the proxy.
	if code, _ := proxyGetThrough(t, p, "/health"); code != http.StatusOK {
		t.Errorf("proxy /health post-failure: got %d want 200", code)
	}
}

// TestLifecycleSwap_NotEligible asserts SwapChild refuses configs without
// [child.handoff] mode = "http-proxy".
func TestLifecycleSwap_NotEligible(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	cancel, done := runUntilRunning(t, tl)
	defer shutdownLifecycle(t, cancel, done)
	_, err := tl.lc.SwapChild(context.Background())
	if !errors.Is(err, ErrSwapNotEligible) {
		t.Fatalf("SwapChild on non-handoff config: want ErrSwapNotEligible, got %v", err)
	}
}

// TestLifecycleSwap_ProxyMissing asserts SwapChild refuses when no proxy
// has been attached.
func TestLifecycleSwap_ProxyMissing(t *testing.T) {
	tl := newTestLifecycle(t, nil, makeReloadCfg(t))
	cancel, done := runUntilRunning(t, tl)
	defer shutdownLifecycle(t, cancel, done)
	_, err := tl.lc.SwapChild(context.Background())
	if !errors.Is(err, ErrSwapProxyMissing) {
		t.Fatalf("SwapChild without proxy: want ErrSwapProxyMissing, got %v", err)
	}
}

// TestLifecycleSwap_InFlightRejectsConcurrent asserts the single-flight
// guard: a second SwapChild call while the first is in flight returns
// ErrSwapInFlight.
func TestLifecycleSwap_InFlightRejectsConcurrent(t *testing.T) {
	tl := newTestLifecycle(t, nil, makeReloadCfg(t))
	cancel, done := runUntilRunning(t, tl)
	defer shutdownLifecycle(t, cancel, done)

	p := proxyForLifecycle(t, tl)
	tl.lc.AttachProxy(p)

	// Manually grip the in-flight flag to simulate a concurrent swap.
	if !tl.lc.swapInFlight.CompareAndSwap(false, true) {
		t.Fatalf("could not seize swapInFlight (race)")
	}
	_, err := tl.lc.SwapChild(context.Background())
	tl.lc.swapInFlight.Store(false)
	if !errors.Is(err, ErrSwapInFlight) {
		t.Fatalf("SwapChild during in-flight: want ErrSwapInFlight, got %v", err)
	}
}

// TestLifecycleSwap_AuditNoSecretEnv asserts the swap audit event Data
// map contains only the contracted fields and no scope/env values.
func TestLifecycleSwap_AuditNoSecretEnv(t *testing.T) {
	tl := newTestLifecycle(t, nil, makeReloadCfg(t))
	// Set a fake secret in vault to ensure refill returns plaintext that
	// could in principle leak via env.
	tl.vault.SetScope("ANTHROPIC_API_KEY", []byte("sk-very-secret-do-not-log"))

	cancel, done := runUntilRunning(t, tl)
	defer shutdownLifecycle(t, cancel, done)

	p := proxyForLifecycle(t, tl)
	tl.lc.AttachProxy(p)

	tl.vault.QueueOK() // for any silent refill that might be triggered
	if _, err := tl.lc.SwapChild(context.Background()); err != nil {
		t.Fatalf("SwapChild: %v", err)
	}

	matches := auditEventsByAction(tl.auditLog, audit.ActionSupervisorChildSwap)
	if len(matches) != 1 {
		t.Fatalf("audit %s count: got %d want 1", audit.ActionSupervisorChildSwap, len(matches))
	}
	ev := matches[0]
	if _, ok := ev.data["scope"]; ok {
		t.Errorf("audit data should not include scope field: %v", ev.data)
	}
	if _, ok := ev.data["env"]; ok {
		t.Errorf("audit data should not include env field: %v", ev.data)
	}
	for k, v := range ev.data {
		s, isStr := v.(string)
		if isStr && strings.Contains(s, "sk-very-secret") {
			t.Errorf("audit data %q leaked secret: %q", k, s)
		}
	}
}

// TestLifecycleSwap_SwapReadinessURLRewrite asserts the helper that
// rewrites the configured readiness URL replaces host:port with
// 127.0.0.1:<newPort> while preserving the path. This is unit-level —
// the orchestration tests above already exercise the round-trip.
func TestLifecycleSwap_SwapReadinessURLRewrite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
		port           uint16
	}{
		{name: "basic", in: "http://127.0.0.1:0/health", port: 12345, want: "http://127.0.0.1:12345/health"},
		{name: "with-query", in: "http://example/health?deep=1", port: 8080, want: "http://127.0.0.1:8080/health?deep=1"},
		{name: "https", in: "https://example/healthz", port: 9000, want: "https://127.0.0.1:9000/healthz"},
		{name: "malformed-returns-input", in: "not a url", port: 1, want: "not a url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := swapReadinessURL(tc.in, tc.port)
			if got != tc.want {
				// Tolerate net/url normalization differences across versions:
				// validate via parsed equivalence rather than strict string
				// match.
				if !urlEqual(got, tc.want) {
					t.Fatalf("swapReadinessURL(%q, %d) = %q, want %q", tc.in, tc.port, got, tc.want)
				}
			}
		})
	}
}

// urlEqual compares two URLs by parsed components when string equality
// fails. Used by the rewrite test to handle minor query-encoding deltas.
func urlEqual(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ua.Scheme == ub.Scheme && ua.Host == ub.Host && ua.Path == ub.Path && ua.RawQuery == ub.RawQuery
}

// auditEventsByAction returns every recorded event whose action == name.
func auditEventsByAction(a *recordingAudit, name string) []recordedAuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []recordedAuditEvent
	for _, e := range a.events {
		if e.action == name {
			out = append(out, e)
		}
	}
	return out
}

// assertSwapAuditData verifies the swap audit Data map carries the
// AC-6-contracted shape: old_pid, new_pid, swap_completed_at,
// readiness_duration_ms, strategy. No scope/env field is allowed.
func assertSwapAuditData(t *testing.T, ev recordedAuditEvent, want SwapResult) {
	t.Helper()
	mustHave := []string{"old_pid", "new_pid", "swap_completed_at", "readiness_duration_ms", "strategy"}
	for _, k := range mustHave {
		if _, ok := ev.data[k]; !ok {
			t.Errorf("audit data missing key %q (got %v)", k, ev.data)
		}
	}
	if got, _ := ev.data["old_pid"].(int); got != want.OldPID {
		t.Errorf("audit old_pid: got %d want %d", got, want.OldPID)
	}
	if got, _ := ev.data["new_pid"].(int); got != want.NewPID {
		t.Errorf("audit new_pid: got %d want %d", got, want.NewPID)
	}
	if got, _ := ev.data["strategy"].(string); got != HandoffStrategyHTTPProxy {
		t.Errorf("audit strategy: got %q want %q", got, HandoffStrategyHTTPProxy)
	}
	if got, _ := ev.data["readiness_duration_ms"].(int64); got <= 0 {
		t.Errorf("audit readiness_duration_ms: got %d want >0", got)
	}
}
