// Tests for promoteChildToProxy — the post-startChild step that probes
// the freshly spawned child's readiness URL and points the reload proxy
// at the new backend on success. Without this, the public listener
// keeps responding `503 no-backend` after initial boot (and after every
// non-swap restart), which is what the 2026-05-25 16:45 cutover attempt
// hit before this method existed.
//
// The tests below split into:
//   - unit-shape no-op branches that do not require a running child
//   - one integration-shape happy-path that proves the proxy serves 200
//     through it AFTER initial boot — the assertion the cutover failure
//     would have caught had this test existed beforehand.
//
// Readiness-failure rollback and the restart-time re-pointing path are
// already covered indirectly by the SwapChild tests (lifecycle_swap_test
// .go) which exercise the same proxy.SetBackend + terminateChildWithGrace
// pair under load. The intent here is targeted coverage of the *new*
// initial-boot promote path, not a full reimplementation of the swap
// matrix.
package supervise

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise/config"
)

// quietPromoteLogger returns a slog.Logger that drops every record, so
// tests don't spam test output with INFO/WARN lines from the helper.
func quietPromoteLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestPromoteChildToProxy_NoHandoff_IsNoOp(t *testing.T) {
	// Non-reload-eligible config must not error and must not touch the
	// proxy. The boot path calls promoteChildToProxy unconditionally;
	// regression here would surface as every legacy supervisor TOML
	// failing boot.
	lc := &Lifecycle{
		config: &config.Supervisor{Child: config.Child{Handoff: nil}},
		deps:   Deps{Logger: quietPromoteLogger()},
	}
	if err := lc.promoteChildToProxy(t.Context()); err != nil {
		t.Fatalf("want nil err for no-handoff, got %v", err)
	}
}

func TestPromoteChildToProxy_HandoffButNoProxyAttached_IsNoOp(t *testing.T) {
	// Embedded pkg/client users may opt into [child.handoff] in the
	// TOML but manage proxy lifetime themselves and never call
	// AttachProxy on the boot path. promoteChildToProxy must not error
	// in that case — the proxy is the embedder's responsibility.
	lc := &Lifecycle{
		config: &config.Supervisor{Child: config.Child{
			Handoff: &config.ChildHandoff{
				Mode:       config.HandoffModeHTTPProxy,
				ListenAddr: "127.0.0.1:0",
			},
		}},
		deps: Deps{Logger: quietPromoteLogger()},
	}
	if err := lc.promoteChildToProxy(t.Context()); err != nil {
		t.Fatalf("want nil err for no-proxy-attached, got %v", err)
	}
}

func TestPromoteChildToProxy_UnknownMode_IsNoOp(t *testing.T) {
	// Future modes (e.g. socket-activation) don't route through this
	// HTTP proxy; the helper must defer to whatever wiring that mode
	// brings instead of forcing SetBackend on the HTTP path.
	lc := &Lifecycle{
		config: &config.Supervisor{Child: config.Child{
			Handoff: &config.ChildHandoff{
				Mode:       "socket-activation",
				ListenAddr: "127.0.0.1:0",
			},
		}},
		deps: Deps{Logger: quietPromoteLogger()},
	}
	if err := lc.promoteChildToProxy(t.Context()); err != nil {
		t.Fatalf("want nil err for unknown mode, got %v", err)
	}
}

func TestPromoteChildToProxy_InitialBoot_SetsBackend(t *testing.T) {
	// The headline assertion: after the lifecycle reaches StateRunning
	// with [child.handoff] configured AND a proxy attached BEFORE
	// boot, the proxy routes requests to the live child — not 503.
	// This is the failure mode the 16:45 cutover hit; this test would
	// have caught the regression at CI time.
	tl := newTestLifecycle(t, nil, makeReloadCfg(t))

	// Attach the proxy BEFORE Run so promoteChildToProxy finds it on
	// the initial-boot path. The proxy's listen_addr is taken from
	// the cfg (makeReloadCfg sets it to a free 127.0.0.1 port).
	proxy := NewProxy(tl.cfg.Child.Handoff.ListenAddr, tl.lc.deps.Logger)
	if err := proxy.Start(t.Context()); err != nil {
		t.Fatalf("proxy Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})
	tl.lc.AttachProxy(proxy)

	cancel, done := runUntilRunning(t, tl)
	defer shutdownLifecycle(t, cancel, done)

	// Verify the proxy serves through to the child — NOT 503. This
	// proves promoteChildToProxy ran its full happy-path: backend
	// port allocated → readiness probed → SetBackend called.
	//
	// We read the resolved address from the proxy (not cfg) because
	// makeReloadCfg uses listen_addr=127.0.0.1:0 — the actual port is
	// kernel-allocated and only known after Start.
	publicAddr := proxy.ListenAddr()
	if publicAddr == "" {
		t.Fatalf("proxy.ListenAddr empty after Start")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+publicAddr+"/health", http.NoBody)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Read X-Hush-Proxy-Reason if present — 503 with no-backend
		// means promoteChildToProxy didn't fire (regression).
		reason := resp.Header.Get("X-Hush-Proxy-Reason")
		t.Fatalf("proxy returned %d (X-Hush-Proxy-Reason=%q); expected 200 — promoteChildToProxy did not point the proxy at the live child", resp.StatusCode, reason)
	}
}

func TestPromoteChildToProxy_NoBackendPort_Errors(t *testing.T) {
	// If a future refactor calls promoteChildToProxy before startChild
	// allocates the backend port, the helper must return a clear error
	// instead of silently calling SetBackend(0). The error message
	// names "backend port not allocated" so triage is unambiguous.
	logger := quietPromoteLogger()
	proxy := NewProxy("127.0.0.1:0", logger)
	if err := proxy.Start(t.Context()); err != nil {
		t.Fatalf("proxy Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	lc := &Lifecycle{
		config: &config.Supervisor{Child: config.Child{
			Handoff: &config.ChildHandoff{
				Mode:       config.HandoffModeHTTPProxy,
				ListenAddr: "127.0.0.1:0",
			},
			Readiness: &config.ChildReadiness{
				HTTPURL:  "http://127.0.0.1:0/health",
				Timeout:  100 * time.Millisecond,
				Interval: 25 * time.Millisecond,
			},
			Shutdown: config.ChildShutdown{Grace: 50 * time.Millisecond},
		}},
		deps:        Deps{Logger: logger, HTTPClient: &http.Client{Timeout: time.Second}},
		proxy:       proxy,
		backendPort: 0, // explicit — emulates pre-startChild state
	}

	err := lc.promoteChildToProxy(t.Context())
	if err == nil {
		t.Fatalf("want non-nil err when backend port=0")
	}
	if !contains(err.Error(), "no backend port allocated") {
		t.Fatalf("error message missing 'no backend port allocated': %v", err)
	}
}

// contains is a tiny strings.Contains shim that avoids the extra import
// noise in a file that otherwise needs no string helpers.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
