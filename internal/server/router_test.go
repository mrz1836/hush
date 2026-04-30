package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestMount_AfterRunReturnsAlreadyRun — Mount post-Run is rejected.
func TestMount_AfterRunReturnsAlreadyRun(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Listener = listener
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()
	if runErr := srv.Run(ctx); runErr != nil {
		t.Fatalf("Run err=%v", runErr)
	}

	if mountErr := srv.Mount(http.MethodPost, "/late", http.NotFoundHandler()); !errors.Is(mountErr, ErrAlreadyRun) {
		t.Fatalf("Mount post-Run err=%v want ErrAlreadyRun", mountErr)
	}
}

// TestMount_RejectsBadInputs covers the input-validation branches.
func TestMount_RejectsBadInputs(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)

	// Nil handler.
	if err := srv.Mount(http.MethodGet, "/x", nil); err == nil ||
		!strings.Contains(err.Error(), "nil handler") {
		t.Errorf("nil handler accepted: %v", err)
	}
	// No leading slash.
	if err := srv.Mount(http.MethodGet, "noslash", http.NotFoundHandler()); err == nil ||
		!strings.Contains(err.Error(), "must begin") {
		t.Errorf("missing slash accepted: %v", err)
	}
	// Path repeats /h/ prefix.
	if err := srv.Mount(http.MethodGet, "/h/foo/x", http.NotFoundHandler()); err == nil ||
		!strings.Contains(err.Error(), "must not repeat") {
		t.Errorf("/h/ repeat accepted: %v", err)
	}
	// Unsupported method.
	if err := srv.Mount("CONNECT", "/x", http.NotFoundHandler()); err == nil ||
		!strings.Contains(err.Error(), "unsupported method") {
		t.Errorf("CONNECT accepted: %v", err)
	}
}

// TestRouter_PrefixMount — registered routes are reachable under the
// chassis-mounted prefix; unknown paths return 404.
//
//nolint:gocognit,gocyclo,cyclop // multi-step bind/POST/GET orchestration; complexity is structural
func TestRouter_PrefixMount(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Listener = listener
		d.Cfg.Network.AllowedCIDRs = []string{"127.0.0.0/8"}
	})

	hits := make(chan string, 4)
	if mountErr := srv.Mount(http.MethodPost, "/claim", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits <- "claim"
		w.WriteHeader(http.StatusAccepted)
	})); mountErr != nil {
		t.Fatalf("Mount: %v", mountErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()
	// Wait for Run to bind.
	time.Sleep(50 * time.Millisecond)

	addr := "http://" + listener.Addr().String()
	prefix := "/h/" + srv.cfg.Server.PathPrefix

	// Reach /claim via the mounted prefix.
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, addr+prefix+"/claim", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("NewRequest POST: %v", err)
	}
	postReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("POST /claim: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d want 202", resp.StatusCode)
	}
	select {
	case got := <-hits:
		if got != "claim" {
			t.Fatalf("hits=%q want claim", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("handler not invoked")
	}

	// Unknown path returns 404.
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+prefix+"/nope", nil)
	if err != nil {
		t.Fatalf("NewRequest GET: %v", err)
	}
	resp2, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET /nope: %v", err)
	}
	t.Cleanup(func() { _ = resp2.Body.Close() })
	_, _ = io.Copy(io.Discard, resp2.Body)
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404", resp2.StatusCode)
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run final err=%v", err)
	}
}

// TestIsAllowedMethod — covers each branch of the allow-method helper.
func TestIsAllowedMethod(t *testing.T) {
	t.Parallel()
	for _, m := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodPatch, http.MethodHead, http.MethodOptions,
	} {
		if !isAllowedMethod(m) {
			t.Errorf("isAllowedMethod(%q)=false; want true", m)
		}
	}
	for _, m := range []string{"CONNECT", "TRACE", "JUNK", ""} {
		if isAllowedMethod(m) {
			t.Errorf("isAllowedMethod(%q)=true; want false", m)
		}
	}
}
