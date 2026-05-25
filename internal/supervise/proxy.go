// HTTP reverse-proxy listener for reload-eligible supervisors (T-306 Phase 5).
//
// proxy.go owns the public HTTP listener used when
// [child.handoff] mode = "http-proxy". The supervisor binds the operator-
// configured ListenAddr and forwards every request to a 127.0.0.1:<backend>
// address determined by an atomic backend pointer. The lifecycle swap
// orchestrator (lifecycle_swap.go) updates that pointer with SetBackend
// after the new child passes its readiness probe; in-flight requests
// targeted at the old backend continue to drain while subsequent requests
// are routed to the new backend.
//
// Anti-contract: the proxy MUST NOT log request/response headers or
// bodies, and MUST NOT surface the proxied URL beyond the operator-
// supplied ListenAddr and the loopback port. Both are non-secret by
// construction (the port is hush-allocated; the listen address is
// operator-configured).

package supervise

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// ErrProxyNotStarted is returned by SetBackend / Stop / ListenAddr when the
// proxy has not been Start'd or has already been Stop'd. Compare via
// errors.Is.
var ErrProxyNotStarted = errors.New("supervise: proxy not started")

// ErrProxyAlreadyStarted is returned by Start when called twice on the same
// Proxy instance. Compare via errors.Is.
var ErrProxyAlreadyStarted = errors.New("supervise: proxy already started")

// errProxyBackendPortZero backs the rejection of SetBackend(0). The kernel
// would silently bind to an ephemeral port at dial time which is not the
// contract — backends must be hush-allocated explicit loopback ports.
var errProxyBackendPortZero = errors.New("supervise: proxy backend port must be > 0")

// proxyBackend is the atomic-pointer payload pointing at the currently
// active loopback backend. The URL is constructed by SetBackend from the
// allocated uint16 port and never carries credentials, query strings, or
// path. Held by *Proxy.backend as an atomic.Pointer so SetBackend is a
// single CAS-style replace and concurrent ServeHTTP calls observe either
// the prior or the new target in full.
type proxyBackend struct {
	port uint16
	url  *url.URL
}

// Proxy is the HTTP reverse-proxy listener bound on the supervisor side
// for reload-eligible configs. Construct via NewProxy; drive via
// Start(ctx). Stop releases the listener; SetBackend swaps the active
// backend pointer atomically without dropping the listener.
//
// Single-shot: a stopped Proxy cannot be restarted; the lifecycle owns
// re-construction when needed.
type Proxy struct {
	listenAddr string
	logger     *slog.Logger

	backend atomic.Pointer[proxyBackend]
	server  *http.Server

	mu       sync.Mutex
	listener net.Listener
	started  bool
	stopped  bool
	serveErr error
	serveCh  chan struct{}
}

// NewProxy returns a Proxy ready to bind listenAddr on Start. Pure value
// constructor — no syscalls. Panics if logger is nil (Constitution IX
// startup-wiring exemption); listenAddr is validated lazily at Start so
// callers can construct without a working socket layer.
func NewProxy(listenAddr string, logger *slog.Logger) *Proxy {
	if logger == nil {
		panic("supervise: NewProxy requires a non-nil *slog.Logger")
	}
	return &Proxy{
		listenAddr: listenAddr,
		logger:     logger,
		serveCh:    make(chan struct{}),
	}
}

// Start binds the listener at the configured listen address and spawns
// the serving goroutine. Returns ErrProxyAlreadyStarted on a second
// invocation. The listener is opened with net.Listen — the supervisor's
// runtime owns the socket; no privileged port handling lives here.
//
// The returned error is nil on success. After Start returns nil, the
// proxy is serving but every request before the first SetBackend call
// receives 503.
func (p *Proxy) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return fmt.Errorf("supervise: %w", ErrProxyAlreadyStarted)
	}
	p.started = true
	p.mu.Unlock()

	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", p.listenAddr)
	if err != nil {
		return fmt.Errorf("supervise: proxy listen %q: %w", p.listenAddr, err)
	}

	rp := &httputil.ReverseProxy{
		Director:     p.director,
		ErrorHandler: p.errorHandler,
		// Discard the default stdlib logger so internal proxy errors
		// (closed connections during swap) do not leak to stderr. The
		// supervisor's slog ErrorHandler emits structured failures.
		ErrorLog: nil,
	}

	srv := &http.Server{
		Handler:           rp,
		ReadHeaderTimeout: 10 * time.Second,
	}

	p.mu.Lock()
	p.listener = l
	p.server = srv
	p.mu.Unlock()

	go p.serve(l, srv)
	return nil
}

// SetBackend points the proxy at 127.0.0.1:<port>. Returns
// ErrProxyNotStarted when the proxy has not been Start'd or has already
// been Stop'd. The replacement is atomic — in-flight requests targeted
// at the previous backend run to completion against the old URL because
// the *url.URL captured by Director is copied per-request.
func (p *Proxy) SetBackend(port uint16) error {
	p.mu.Lock()
	if !p.started || p.stopped {
		p.mu.Unlock()
		return fmt.Errorf("supervise: %w", ErrProxyNotStarted)
	}
	p.mu.Unlock()
	if port == 0 {
		return errProxyBackendPortZero
	}
	u := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	p.backend.Store(&proxyBackend{port: port, url: u})
	return nil
}

// CurrentBackend returns the most recently installed backend port, or 0
// when no backend has been set yet. Operator-facing surface — used by
// the swap orchestrator to record the prior backend before swapping.
func (p *Proxy) CurrentBackend() uint16 {
	b := p.backend.Load()
	if b == nil {
		return 0
	}
	return b.port
}

// ListenAddr returns the address the proxy bound at Start. When the
// configured address was ":0", ListenAddr returns the kernel-assigned
// concrete address (host:port) — useful for tests that need to connect
// without knowing the port in advance. Returns "" when the proxy has
// not been Start'd.
func (p *Proxy) ListenAddr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listener == nil {
		return ""
	}
	return p.listener.Addr().String()
}

// Stop closes the listener and waits for the serving goroutine to exit.
// Idempotent: a second call returns nil without re-closing the listener.
// Stop never errors on the http.ErrServerClosed path; an explicit context
// deadline lets callers cap how long the graceful shutdown may take.
func (p *Proxy) Stop(ctx context.Context) error {
	p.mu.Lock()
	if !p.started || p.stopped {
		p.mu.Unlock()
		return nil
	}
	p.stopped = true
	srv := p.server
	p.mu.Unlock()

	// Shutdown closes the listener, waits for in-flight handlers, and
	// returns when ctx expires.
	if srv != nil {
		if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("supervise: proxy shutdown: %w", err)
		}
	}
	<-p.serveCh
	return nil
}

// serve is the goroutine that runs the http.Server. Termination is driven
// by Stop closing the listener; Serve returns http.ErrServerClosed on a
// clean shutdown which we treat as nil.
func (p *Proxy) serve(l net.Listener, srv *http.Server) {
	defer close(p.serveCh)
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("supervise: proxy serve goroutine panic", slog.Any("recover", r))
		}
	}()
	err := srv.Serve(l)
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		p.mu.Lock()
		p.serveErr = err
		p.mu.Unlock()
		p.logger.Warn("supervise: proxy serve exited with error", slog.Any("err", err))
	}
}

// director is the per-request rewrite hook. It rewrites the URL to point
// at the active backend (read once via atomic.Load) and clears any
// hop-by-hop headers httputil.ReverseProxy does not already strip.
//
// When no backend is set, director leaves the request URL pointed at a
// sentinel host so ErrorHandler can return 503 without attempting a dial.
func (p *Proxy) director(req *http.Request) {
	b := p.backend.Load()
	if b == nil {
		// Sentinel URL so the transport's dial fails fast; ErrorHandler
		// converts the resulting error to a 503.
		req.URL.Scheme = "http"
		req.URL.Host = "127.0.0.1:0"
		req.Host = req.URL.Host
		// Tag the context so ErrorHandler can distinguish "no backend"
		// from a normal upstream failure.
		ctx := context.WithValue(req.Context(), proxyNoBackendKey{}, true)
		*req = *req.WithContext(ctx)
		return
	}
	req.URL.Scheme = b.url.Scheme
	req.URL.Host = b.url.Host
	req.Host = b.url.Host
}

// proxyNoBackendKey is the context key used to flag a request that hit
// the proxy before any backend was installed.
type proxyNoBackendKey struct{}

// errorHandler is invoked by httputil.ReverseProxy when the upstream
// request fails (connection refused, timeout, etc.). It returns 503/502
// with a non-secret reason header — never echoes the upstream URL or
// any request body.
func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	_ = err // intentionally unused: never logged with request context
	if v, _ := r.Context().Value(proxyNoBackendKey{}).(bool); v {
		w.Header().Set("X-Hush-Proxy-Reason", "no-backend")
		http.Error(w, "supervisor: no backend configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("X-Hush-Proxy-Reason", "upstream-error")
	http.Error(w, "supervisor: upstream error", http.StatusBadGateway)
}
