// Package cli — tests for the CLI-side reload-proxy auto-attach that
// closes the gap left by PR #48 (hush v0.8 zero-downtime reload).
//
// Why this exists. The reload-handoff lifecycle code, the SwapChild
// orchestration, the pkg/client SDK, and the `hush supervise reload`
// subcommand all shipped together in PR #48 — but the CLI form
// `hush supervise <toml>` never bound the public proxy listener. A
// supervisor TOML with [child.handoff] configured would start its child
// on the hush-allocated private port and then have nothing serve the
// public listen_addr. This was caught on 2026-05-25 16:34 EDT when an
// operator (zai/openclaw) attempted the live cutover; the gateway was
// unreachable on its public port until rollback to the legacy
// direct-bind config completed.
//
// These tests guard the wiring in startProxyIfHandoffConfigured /
// stopProxyGracefully so any future refactor that drops the auto-attach
// is caught at CI time, not in production.
package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	superviseconfig "github.com/mrz1836/hush/internal/supervise/config"
)

// quietLogger returns a slog.Logger that drops every record. Used to keep
// test output clean while still satisfying the *slog.Logger arg.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// stubAttacher records every AttachProxy / AttachReloadHandler call so
// tests can assert that the helpers wire correctly. Production wiring
// uses *supervise.Lifecycle for this seam; instantiating a full Lifecycle
// in a unit test would require many non-nil deps unrelated to proxy
// + reload wiring (see Deps in internal/supervise/lifecycle.go).
type stubAttacher struct {
	attached        []*supervise.Proxy
	reloadHandlers  []func(ctx context.Context, req supervise.ReloadRequest) (supervise.SwapResult, error)
	swapChildCalled int
	swapChildResult supervise.SwapResult
	swapChildErr    error
}

func (s *stubAttacher) AttachProxy(p *supervise.Proxy) {
	s.attached = append(s.attached, p)
}

func (s *stubAttacher) AttachReloadHandler(handler func(ctx context.Context, req supervise.ReloadRequest) (supervise.SwapResult, error)) {
	s.reloadHandlers = append(s.reloadHandlers, handler)
}

func (s *stubAttacher) SwapChild(_ context.Context) (supervise.SwapResult, error) {
	s.swapChildCalled++
	return s.swapChildResult, s.swapChildErr
}

// freeLoopbackAddr returns a 127.0.0.1:<ephemeral> host:port string that
// is unbound at the moment of the call. The OS may reassign before the
// caller binds; tests using this for negative paths should re-bind to
// hold the port.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeLoopbackAddr: %v", err)
	}
	addr := l.Addr().String()
	if cErr := l.Close(); cErr != nil {
		t.Fatalf("freeLoopbackAddr close: %v", cErr)
	}
	return addr
}

func TestStartProxyIfHandoffConfigured_NoHandoff_ReturnsNilNil(t *testing.T) {
	// Non-reload-eligible config (no [child.handoff]) must produce nil
	// proxy + nil error so existing supervisor TOMLs see no behavior
	// change.
	cfg := &superviseconfig.Supervisor{
		Child: superviseconfig.Child{
			Handoff: nil,
		},
	}
	ctx := t.Context()
	attacher := &stubAttacher{}

	proxy, err := startProxyIfHandoffConfigured(ctx, cfg, attacher, quietLogger())
	if err != nil {
		t.Fatalf("want nil err, got %v", err)
	}
	if proxy != nil {
		t.Fatalf("want nil proxy, got non-nil")
	}
}

func TestStartProxyIfHandoffConfigured_UnknownMode_ReturnsNilNil(t *testing.T) {
	// Future handoff modes that aren't HTTP-proxy (e.g. socket-activation)
	// must NOT route through this HTTP proxy. Defensive guard — the
	// config loader also rejects unknown modes; this is belt-and-suspenders.
	cfg := &superviseconfig.Supervisor{
		Child: superviseconfig.Child{
			Handoff: &superviseconfig.ChildHandoff{
				Mode:       "socket-activation",
				ListenAddr: "127.0.0.1:0",
			},
		},
	}
	ctx := t.Context()
	attacher := &stubAttacher{}

	proxy, err := startProxyIfHandoffConfigured(ctx, cfg, attacher, quietLogger())
	if err != nil {
		t.Fatalf("want nil err for unknown mode, got %v", err)
	}
	if proxy != nil {
		t.Fatalf("want nil proxy for unknown mode, got non-nil")
		_ = proxy.Stop(t.Context()) //nolint:contextcheck // defensive cleanup on failure
	}
}

func TestStartProxyIfHandoffConfigured_HTTPProxy_BindsListenerAndAttaches(t *testing.T) {
	// Happy path: handoff configured with mode = "http-proxy" → proxy
	// bound at listen_addr, attached to lifecycle, returned for defer-stop.
	addr := freeLoopbackAddr(t)
	cfg := &superviseconfig.Supervisor{
		Child: superviseconfig.Child{
			Handoff: &superviseconfig.ChildHandoff{
				Mode:       superviseconfig.HandoffModeHTTPProxy,
				ListenAddr: addr,
			},
		},
	}
	ctx := t.Context()
	attacher := &stubAttacher{}

	proxy, err := startProxyIfHandoffConfigured(ctx, cfg, attacher, quietLogger())
	if err != nil {
		t.Fatalf("want nil err, got %v", err)
	}
	if proxy == nil {
		t.Fatalf("want non-nil proxy")
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	// Proxy.ListenAddr returns the address the proxy bound. Must match
	// the address we asked for (allowing for ephemeral resolution).
	if got := proxy.ListenAddr(); got == "" {
		t.Fatalf("proxy.ListenAddr() empty — Start did not bind a listener")
	}

	// A TCP dial to listen_addr should connect; without SetBackend it
	// returns 503, but the listener must be present.
	conn, dialErr := net.DialTimeout("tcp", addr, 2*time.Second)
	if dialErr != nil {
		t.Fatalf("dial bound listen_addr: %v", dialErr)
	}
	_ = conn.Close()

	// HTTP request returns 503 before any backend is set — proves the
	// proxy is serving HTTP, not just an open TCP port.
	resp, httpErr := http.Get("http://" + addr + "/")
	if httpErr != nil {
		t.Fatalf("HTTP GET against proxy: %v", httpErr)
	}
	if cErr := resp.Body.Close(); cErr != nil {
		t.Logf("resp body close: %v", cErr)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("pre-SetBackend status: want 503, got %d", resp.StatusCode)
	}
}

func TestStartProxyIfHandoffConfigured_BindFailure_ReturnsError(t *testing.T) {
	// Negative path: listen_addr already in use → proxy.Start returns
	// an error that startProxyIfHandoffConfigured wraps and propagates.
	// The caller never reaches lc.Run, which is exactly what we want —
	// operator sees the bind failure at boot, not at first reload.
	holdL, lErr := net.Listen("tcp", "127.0.0.1:0")
	if lErr != nil {
		t.Fatalf("hold listener: %v", lErr)
	}
	defer func() { _ = holdL.Close() }()
	heldAddr := holdL.Addr().String()

	cfg := &superviseconfig.Supervisor{
		Child: superviseconfig.Child{
			Handoff: &superviseconfig.ChildHandoff{
				Mode:       superviseconfig.HandoffModeHTTPProxy,
				ListenAddr: heldAddr,
			},
		},
	}
	ctx := t.Context()
	attacher := &stubAttacher{}

	proxy, err := startProxyIfHandoffConfigured(ctx, cfg, attacher, quietLogger())
	if err == nil {
		t.Fatalf("want non-nil err on bind failure")
	}
	if proxy != nil {
		t.Fatalf("want nil proxy on bind failure")
		_ = proxy.Stop(t.Context()) //nolint:contextcheck // defensive cleanup on failure
	}
	// Error message must name the listen_addr so the operator can locate
	// the offending TOML field without log spelunking.
	if !strings.Contains(err.Error(), heldAddr) {
		t.Fatalf("error message missing listen_addr %q: %v", heldAddr, err)
	}
	if !strings.Contains(err.Error(), "proxy start") {
		t.Fatalf("error message missing 'proxy start' tag: %v", err)
	}
}

func TestStartProxyIfHandoffConfigured_AttachedProxyIsTheReturnedProxy(t *testing.T) {
	// Sanity: the attached proxy is the same proxy returned to the caller.
	// If a future refactor accidentally created two distinct instances
	// (one attached, one returned), defer-stop would leak the attached one.
	addr := freeLoopbackAddr(t)
	cfg := &superviseconfig.Supervisor{
		Child: superviseconfig.Child{
			Handoff: &superviseconfig.ChildHandoff{
				Mode:       superviseconfig.HandoffModeHTTPProxy,
				ListenAddr: addr,
			},
		},
	}
	ctx := t.Context()
	attacher := &stubAttacher{}

	proxy, err := startProxyIfHandoffConfigured(ctx, cfg, attacher, quietLogger())
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	if len(attacher.attached) != 1 {
		t.Fatalf("AttachProxy call count: want 1, got %d", len(attacher.attached))
	}
	if attacher.attached[0] != proxy {
		t.Fatalf("AttachProxy received a different *Proxy than the helper returned — defer-stop would leak the attached one")
	}

	// Calling Start again must return ErrProxyAlreadyStarted — proves
	// the helper's Start call took effect (not skipped silently).
	startErr := proxy.Start(ctx)
	if !errors.Is(startErr, supervise.ErrProxyAlreadyStarted) {
		t.Fatalf("second Start: want ErrProxyAlreadyStarted, got %v", startErr)
	}
}

func TestStartProxyIfHandoffConfigured_NoHandoff_DoesNotAttach(t *testing.T) {
	// Belt-and-suspenders on the no-handoff branch: the attacher must
	// receive zero AttachProxy calls so non-reload-eligible configs stay
	// exactly as they were before this fix.
	cfg := &superviseconfig.Supervisor{Child: superviseconfig.Child{Handoff: nil}}
	attacher := &stubAttacher{}

	_, err := startProxyIfHandoffConfigured(t.Context(), cfg, attacher, quietLogger())
	if err != nil {
		t.Fatalf("want nil err, got %v", err)
	}
	if len(attacher.attached) != 0 {
		t.Fatalf("non-handoff config triggered AttachProxy: %d call(s)", len(attacher.attached))
	}
}

func TestWireReloadHandlerIfHandoffConfigured_NoHandoff_DoesNotAttach(t *testing.T) {
	// Non-reload-eligible config must not wire a reload handler — doing
	// so would imply reload is supported on supervisors that aren't.
	cfg := &superviseconfig.Supervisor{Child: superviseconfig.Child{Handoff: nil}}
	attacher := &stubAttacher{}
	wireReloadHandlerIfHandoffConfigured(cfg, attacher, quietLogger())
	if len(attacher.reloadHandlers) != 0 {
		t.Fatalf("non-handoff config wired %d reload handler(s); want 0", len(attacher.reloadHandlers))
	}
}

func TestWireReloadHandlerIfHandoffConfigured_HTTPProxy_WiresHandler(t *testing.T) {
	// Happy path: handoff configured → handler wired exactly once.
	// Invoking the handler must call SwapChild (the only behavior the
	// SDK's Reload call needs from the CLI runtime).
	cfg := &superviseconfig.Supervisor{Child: superviseconfig.Child{
		Handoff: &superviseconfig.ChildHandoff{
			Mode:       superviseconfig.HandoffModeHTTPProxy,
			ListenAddr: "127.0.0.1:0",
		},
	}}
	attacher := &stubAttacher{
		swapChildResult: supervise.SwapResult{
			OldPID:            111,
			NewPID:            222,
			ReadinessDuration: 42 * time.Millisecond,
			Strategy:          "http-proxy",
		},
	}
	wireReloadHandlerIfHandoffConfigured(cfg, attacher, quietLogger())
	if len(attacher.reloadHandlers) != 1 {
		t.Fatalf("AttachReloadHandler call count: want 1, got %d", len(attacher.reloadHandlers))
	}
	// Invoke the wired handler and verify it dispatches to SwapChild
	// with the result propagated unchanged.
	res, err := attacher.reloadHandlers[0](t.Context(), supervise.ReloadRequest{ConfigPath: "/tmp/whatever.toml"})
	if err != nil {
		t.Fatalf("handler returned err: %v", err)
	}
	if attacher.swapChildCalled != 1 {
		t.Fatalf("SwapChild call count: want 1, got %d", attacher.swapChildCalled)
	}
	if res.OldPID != 111 || res.NewPID != 222 {
		t.Fatalf("SwapResult not propagated: %+v", res)
	}
}

func TestWireReloadHandlerIfHandoffConfigured_PropagatesSwapError(t *testing.T) {
	// Errors from SwapChild must reach the SDK unchanged so its typed
	// error sentinels (ErrReloadReadinessFailed, ErrReloadInFlight, ...)
	// classify correctly on the client side.
	cfg := &superviseconfig.Supervisor{Child: superviseconfig.Child{
		Handoff: &superviseconfig.ChildHandoff{
			Mode:       superviseconfig.HandoffModeHTTPProxy,
			ListenAddr: "127.0.0.1:0",
		},
	}}
	wantErr := errors.New("synthetic swap failure")
	attacher := &stubAttacher{swapChildErr: wantErr}
	wireReloadHandlerIfHandoffConfigured(cfg, attacher, quietLogger())
	_, gotErr := attacher.reloadHandlers[0](t.Context(), supervise.ReloadRequest{})
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("handler did not propagate SwapChild error: got %v", gotErr)
	}
}

func TestStopProxyGracefully_IdempotentOnAlreadyStopped(t *testing.T) {
	// stopProxyGracefully is invoked from a defer; it must not panic if
	// the proxy was already stopped (e.g. by an in-flight SwapChild
	// teardown path that future code adds).
	addr := freeLoopbackAddr(t)
	proxy := supervise.NewProxy(addr, quietLogger())
	if err := proxy.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := proxy.Stop(stopCtx); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	// Second Stop via the helper — must not panic, must not error loudly.
	// stopProxyGracefully eats Stop errors at warn level; the test asserts
	// it returns cleanly rather than checking the swallowed error.
	stopProxyGracefully(proxy, quietLogger())
}
