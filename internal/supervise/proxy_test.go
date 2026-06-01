package supervise_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
)

// proxyTestLogger returns a logger that discards output — proxy tests
// assert behaviour, not log content.
func proxyTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func listenEventually(t *testing.T, addr string) (net.Listener, error) {
	t.Helper()
	var lastErr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var lc net.ListenConfig
		l, err := lc.Listen(context.Background(), "tcp", addr)
		if err == nil {
			return l, nil
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	return nil, lastErr
}

// startBackend stands up an httptest.Server on 127.0.0.1 with the
// supplied handler and returns the parsed loopback port.
func startBackend(t *testing.T, handler http.Handler) (*httptest.Server, uint16) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse backend URL %q: %v", srv.URL, err)
	}
	p, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatalf("parse backend port %q: %v", u.Port(), err)
	}
	return srv, uint16(p)
}

// proxyGet issues a GET against the proxy listener and returns the
// response. The proxy listens on ":0" in tests, so we read the bound
// address from p.ListenAddr() after Start.
func proxyGet(t *testing.T, p *supervise.Proxy, path string) *http.Response {
	t.Helper()
	addr := p.ListenAddr()
	if addr == "" {
		t.Fatalf("proxy ListenAddr empty (proxy not started?)")
	}
	u := "http://" + addr + path
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy GET %s: %v", u, err)
	}
	return resp
}

// TestProxy_StartTwice asserts Start is single-shot.
func TestProxy_StartTwice(t *testing.T) {
	t.Parallel()
	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	if err := p.Start(context.Background()); !errors.Is(err, supervise.ErrProxyAlreadyStarted) {
		t.Fatalf("second Start: want ErrProxyAlreadyStarted, got %v", err)
	}
}

// TestProxy_SetBackendBeforeStart asserts SetBackend rejects calls when
// the proxy has not been Start'd.
func TestProxy_SetBackendBeforeStart(t *testing.T) {
	t.Parallel()
	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.SetBackend(8080); !errors.Is(err, supervise.ErrProxyNotStarted) {
		t.Fatalf("SetBackend pre-Start: want ErrProxyNotStarted, got %v", err)
	}
}

// TestProxy_NoBackend503 asserts a request before SetBackend yields a 503
// with the "no-backend" reason header — and without dialing a real
// backend.
func TestProxy_NoBackend503(t *testing.T) {
	t.Parallel()
	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })

	resp := proxyGet(t, p, "/anything")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Hush-Proxy-Reason"); got != "no-backend" {
		t.Fatalf("reason header: got %q want %q", got, "no-backend")
	}
}

// TestProxy_ForwardsToBackend asserts a request goes through to the
// configured backend and the backend's response body reaches the caller.
func TestProxy_ForwardsToBackend(t *testing.T) {
	t.Parallel()
	hits := atomic.Int32{}
	_, port := startBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("X-Backend-Marker", "v1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello-from-backend"))
	}))

	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	if err := p.SetBackend(port); err != nil {
		t.Fatalf("SetBackend: %v", err)
	}
	if got := p.CurrentBackend(); got != port {
		t.Fatalf("CurrentBackend: got %d want %d", got, port)
	}

	resp := proxyGet(t, p, "/")
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", resp.StatusCode, string(body))
	}
	if string(body) != "hello-from-backend" {
		t.Fatalf("body: got %q want %q", string(body), "hello-from-backend")
	}
	if got := resp.Header.Get("X-Backend-Marker"); got != "v1" {
		t.Fatalf("backend marker: got %q want %q", got, "v1")
	}
	if hits.Load() != 1 {
		t.Fatalf("backend hits: got %d want 1", hits.Load())
	}
}

// TestProxy_SwapBackend asserts SetBackend points subsequent requests at
// the new backend without disturbing the listener. The first backend is
// closed before the swap so a stale pointer would surface as a 502.
func TestProxy_SwapBackend(t *testing.T) {
	t.Parallel()
	srv1, port1 := startBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Backend", "1")
		_, _ = w.Write([]byte("backend-1"))
	}))
	_, port2 := startBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Backend", "2")
		_, _ = w.Write([]byte("backend-2"))
	}))

	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })

	if err := p.SetBackend(port1); err != nil {
		t.Fatalf("SetBackend port1: %v", err)
	}

	resp1 := proxyGet(t, p, "/")
	body1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	if string(body1) != "backend-1" {
		t.Fatalf("pre-swap body: got %q want %q", string(body1), "backend-1")
	}

	// Swap to second backend and close the first to prove the proxy
	// stopped routing to it.
	if err := p.SetBackend(port2); err != nil {
		t.Fatalf("SetBackend port2: %v", err)
	}
	srv1.Close()

	resp2 := proxyGet(t, p, "/")
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if string(body2) != "backend-2" {
		t.Fatalf("post-swap body: got %q want %q", string(body2), "backend-2")
	}
	if got := resp2.Header.Get("X-Backend"); got != "2" {
		t.Fatalf("backend header: got %q want %q", got, "2")
	}
}

// TestProxy_SwapNoDroppedRequests pummels the proxy with concurrent
// requests while the backend is swapped from server A to server B. Every
// request must come back with a 2xx body matching either backend — no
// connection refused, no 5xx during the swap window.
//
// This exercises the AC-4 contract: observed HTTP availability remains
// uninterrupted across an atomic SetBackend.
//
//nolint:gocognit,gocyclo // worker-pool + mid-flight swap + post-run assertions; complexity is inherent
func TestProxy_SwapNoDroppedRequests(t *testing.T) {
	t.Parallel()
	_, portA := startBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("A"))
	}))
	_, portB := startBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("B"))
	}))

	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	if err := p.SetBackend(portA); err != nil {
		t.Fatalf("SetBackend portA: %v", err)
	}

	const workers = 8
	const requestsPerWorker = 40
	var (
		wg       sync.WaitGroup
		bodiesMu sync.Mutex
		bodies   = map[string]int{}
		failures atomic.Int32
		stopFlag atomic.Bool
		swapDone = make(chan struct{})
	)

	// Worker pool — keeps issuing requests until stopFlag is set.
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < requestsPerWorker; j++ {
				if stopFlag.Load() {
					return
				}
				resp := proxyGet(t, p, "/")
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					failures.Add(1)
					continue
				}
				bodiesMu.Lock()
				bodies[string(body)]++
				bodiesMu.Unlock()
			}
		}()
	}

	// Mid-flight swap.
	go func() {
		defer close(swapDone)
		time.Sleep(5 * time.Millisecond)
		_ = p.SetBackend(portB)
	}()

	wg.Wait()
	stopFlag.Store(true)
	<-swapDone

	if got := failures.Load(); got != 0 {
		t.Fatalf("requests failed during swap: %d (want 0); bodies=%v", got, bodies)
	}
	if bodies["A"] == 0 && bodies["B"] == 0 {
		t.Fatalf("expected at least one of A/B to be hit; bodies=%v", bodies)
	}
	// We expect AT LEAST B (swap target) to be hit — if it's not, the
	// swap never took effect.
	if bodies["B"] == 0 {
		t.Fatalf("no requests reached backend B; bodies=%v", bodies)
	}
}

// TestProxy_StopReleasesListener verifies Stop closes the listener so the
// address can be re-bound by a fresh listener.
func TestProxy_StopReleasesListener(t *testing.T) {
	t.Parallel()
	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := p.ListenAddr()
	if addr == "" {
		t.Fatalf("ListenAddr empty after Start")
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Subsequent SetBackend should refuse.
	if err := p.SetBackend(12345); !errors.Is(err, supervise.ErrProxyNotStarted) {
		t.Fatalf("SetBackend after Stop: want ErrProxyNotStarted, got %v", err)
	}
	// Re-bind the address with a fresh listener; under the race detector the
	// kernel may report the just-closed socket for a short interval.
	l, err := listenEventually(t, addr)
	if err != nil {
		t.Fatalf("re-bind %s after Stop: %v", addr, err)
	}
	_ = l.Close()
}

// TestProxy_StopIdempotent asserts Stop can be called multiple times.
func TestProxy_StopIdempotent(t *testing.T) {
	t.Parallel()
	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestProxy_BackendUnreachable502 asserts a request whose backend cannot
// be dialed returns 502 — the ReverseProxy's transport surfaces the dial
// error to our ErrorHandler, which marks the response as upstream-error.
func TestProxy_BackendUnreachable502(t *testing.T) {
	t.Parallel()
	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	// Allocate then close so the port is almost certainly unbound; the
	// proxy then points at it.
	deadPort, err := supervise.AllocateBackendPort(context.Background())
	if err != nil {
		t.Fatalf("AllocateBackendPort: %v", err)
	}
	if err := p.SetBackend(deadPort); err != nil {
		t.Fatalf("SetBackend: %v", err)
	}
	resp := proxyGet(t, p, "/")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d want 502; body=%s", resp.StatusCode, string(body))
	}
	if got := resp.Header.Get("X-Hush-Proxy-Reason"); got != "upstream-error" {
		t.Fatalf("reason header: got %q want %q", got, "upstream-error")
	}
}

// TestProxy_CurrentBackendInitialZero asserts CurrentBackend returns 0
// before any SetBackend call.
func TestProxy_CurrentBackendInitialZero(t *testing.T) {
	t.Parallel()
	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if got := p.CurrentBackend(); got != 0 {
		t.Fatalf("CurrentBackend before Start: got %d want 0", got)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	if got := p.CurrentBackend(); got != 0 {
		t.Fatalf("CurrentBackend before SetBackend: got %d want 0", got)
	}
}

// TestProxy_SetBackendRejectsZero asserts port=0 is refused — the kernel
// would silently bind us to an ephemeral port at dial time which is not
// the contract.
func TestProxy_SetBackendRejectsZero(t *testing.T) {
	t.Parallel()
	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	err := p.SetBackend(0)
	if err == nil {
		t.Fatalf("SetBackend(0): want error, got nil")
	}
	if !strings.Contains(err.Error(), "> 0") {
		t.Fatalf("SetBackend(0): err=%v (want message mentioning > 0)", err)
	}
}

// proxyTestPathPropagation asserts request path is forwarded verbatim —
// no rewriting, no path mutation. The backend records what path it saw.
func TestProxy_PathPropagation(t *testing.T) {
	t.Parallel()
	gotPath := atomic.Value{}
	gotPath.Store("")
	_, port := startBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))

	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	if err := p.SetBackend(port); err != nil {
		t.Fatalf("SetBackend: %v", err)
	}

	want := "/health/deep"
	resp := proxyGet(t, p, want)
	_ = resp.Body.Close()
	if got, _ := gotPath.Load().(string); got != want {
		t.Fatalf("backend saw path %q want %q", got, want)
	}
}

// TestProxy_ListenAddrAfterStop asserts ListenAddr keeps returning the
// last bound address after Stop — useful for diagnostics — without
// re-opening any socket.
func TestProxy_ListenAddrAfterStop(t *testing.T) {
	t.Parallel()
	p := supervise.NewProxy("127.0.0.1:0", proxyTestLogger())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := p.ListenAddr()
	if addr == "" {
		t.Fatalf("ListenAddr empty after Start")
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// ListenAddr still returns a non-empty string (the last bound
	// address); the listener itself is closed, which we proved in
	// TestProxy_StopReleasesListener.
	if got := p.ListenAddr(); got == "" {
		t.Fatalf("ListenAddr empty after Stop; want last-bound %q", addr)
	}
}
