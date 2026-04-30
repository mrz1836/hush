//go:build integration

package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// errIntUnexpectedReload is the canned error returned when the integration
// loadFn is called more times than scripted.
var errIntUnexpectedReload = errors.New("test: unexpected reload")

// hasTailscaleAddr reports whether the host has any local interface inside
// the Tailscale CGNAT range, returning the address as a string.
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
		// CGNAT range 100.64.0.0/10
		if ip4[0] == 100 && (ip4[1]&0xC0) == 64 {
			return ip4.String(), true
		}
	}
	return "", false
}

// TestStartupChecks_HappyPath — correctly-configured chassis on a real
// Tailscale-CGNAT host runs through every check and binds a listener.
func TestStartupChecks_HappyPath(t *testing.T) {
	t.Parallel()
	tsAddr, ok := hasTailscaleAddr()
	if !ok {
		t.Skip("no Tailscale CGNAT address on this host")
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", tsAddr+":0")
	if err != nil {
		t.Fatalf("listen on %s: %v", tsAddr, err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	srv, audit, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ListenAddr = netip.MustParseAddrPort(tsAddr + ":1234")
		d.Listener = listener
		d.InterfaceLister = nil // use the real lister
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if err := srv.Run(ctx); err != nil {
		t.Fatalf("Run err=%v", err)
	}

	hits := 0
	for _, e := range audit.snapshot() {
		if e.Type == AuditServerStart && e.Detail["status"] == "ok" {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("AuditServerStart status=ok count=%d, want 1", hits)
	}
}

// TestSIGHUP_AtomicReload — the load-bearing integration test:
// vault A → SIGHUP → in-flight request still sees A → fresh request sees B
// → A is destroyed exactly once at or after the drain window.
//
//nolint:gocognit,cyclop,funlen // multi-step orchestration: bind → request → SIGHUP → assert; complexity is structural
func TestSIGHUP_AtomicReload(t *testing.T) {
	tsAddr, ok := hasTailscaleAddr()
	if !ok {
		t.Skip("no Tailscale CGNAT address on this host")
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", tsAddr+":0")
	if err != nil {
		t.Fatalf("listen on %s: %v", tsAddr, err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	storeA := newFakeStore("A", []byte("A-secret"))
	storeB := newFakeStore("B", []byte("B-secret"))

	loadCount := atomic.Int32{}
	loadFn := func(_ context.Context, _ string, _ *securebytes.SecureBytes) (vault.Store, error) {
		c := loadCount.Add(1)
		if c == 1 {
			return storeB, nil
		}
		return nil, errIntUnexpectedReload
	}

	releaseHandler := make(chan struct{})
	handlerVaultObserved := make(chan vault.Store, 4)

	// vaultPtrRef will be assigned to point at the chassis's atomic pointer
	// before Run, so the handler can read it.
	var vaultPtrRef *atomic.Pointer[vault.Store]

	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := *vaultPtrRef.Load()
		handlerVaultObserved <- v
		// First request stalls until released.
		if r.URL.Path == "/h/abcdef/slow" {
			<-releaseHandler
		}
		w.WriteHeader(http.StatusOK)
	})

	initial := vault.Store(storeA)
	var ptr atomic.Pointer[vault.Store]
	ptr.Store(&initial)
	vaultPtrRef = &ptr

	srv, audit, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ListenAddr = netip.MustParseAddrPort(tsAddr + ":1234")
		d.Cfg.Server.StateDir = stateDir
		d.Cfg.Network.AllowedCIDRs = []string{"100.64.0.0/10"}
		d.Listener = listener
		d.InterfaceLister = nil
		d.LoadVaultFn = loadFn
		d.VaultKey = makeKey(t)
		d.ReloadDrainWindow = 100 * time.Millisecond
		d.VaultPtr = &ptr
	})

	if err := srv.Mount(http.MethodGet, "/slow", probe); err != nil {
		t.Fatalf("Mount /slow: %v", err)
	}
	if err := srv.Mount(http.MethodGet, "/fast", probe); err != nil {
		t.Fatalf("Mount /fast: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	addr := "http://" + listener.Addr().String() + "/h/abcdef"

	// Begin the slow request that will hold the old vault.
	slowDone := make(chan *http.Response, 1)
	slowErr := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/slow", nil)
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			slowErr <- err
			return
		}
		slowDone <- resp
	}()

	// Wait until handler captured the vault pointer.
	var inflight vault.Store
	select {
	case inflight = <-handlerVaultObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not observe vault in time")
	}
	if inflight != storeA {
		t.Fatalf("in-flight observed %T, want storeA", inflight)
	}

	// Send SIGHUP.
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("SIGHUP: %v", err)
	}

	// Wait for the swap AND drain to complete. ReloadVault holds the
	// mutex through the drain window before destroying the old store, so
	// the destroy lags the swap by ReloadDrainWindow.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if *vaultPtrRef.Load() == storeB && storeA.destroys() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := *vaultPtrRef.Load(); got != storeB {
		t.Fatalf("after SIGHUP, vault still %T, want storeB", got)
	}
	if got := storeA.destroys(); got != 1 {
		t.Fatalf("storeA destroys=%d, want 1 after drain", got)
	}

	// Release the slow request.
	close(releaseHandler)

	// Slow request response should be 200 (it ran to completion under A).
	select {
	case resp := <-slowDone:
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("slow status=%d want 200", resp.StatusCode)
		}
	case err := <-slowErr:
		t.Fatalf("slow request err=%v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("slow request did not return")
	}

	// New request now sees B.
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/fast", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp2, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET /fast: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	_ = resp2.Body.Close()

	var observed2 vault.Store
	select {
	case observed2 = <-handlerVaultObserved:
	case <-time.After(time.Second):
		t.Fatal("fast handler did not observe vault")
	}
	if observed2 != storeB {
		t.Fatalf("fresh request observed %T, want storeB", observed2)
	}

	// A must be destroyed exactly once.
	if got := storeA.destroys(); got != 1 {
		t.Fatalf("storeA destroys=%d, want 1", got)
	}
	if got := storeB.destroys(); got != 0 {
		t.Fatalf("storeB destroys=%d, want 0", got)
	}

	hits := 0
	for _, e := range audit.snapshot() {
		if e.Type == AuditVaultReloaded {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("AuditVaultReloaded count=%d, want 1", hits)
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run final err=%v", err)
	}
}

// TestRun_GracefulShutdown_DrainsInflight — cancellation drains in-flight
// requests; SIGHUP during shutdown is dropped.
//
//nolint:gocognit,cyclop // multi-step lifecycle test
func TestRun_GracefulShutdown_DrainsInflight(t *testing.T) {
	tsAddr, ok := hasTailscaleAddr()
	if !ok {
		t.Skip("no Tailscale CGNAT address on this host")
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", tsAddr+":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	release := make(chan struct{})
	probe := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	})

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ListenAddr = netip.MustParseAddrPort(tsAddr + ":1234")
		d.Cfg.Network.AllowedCIDRs = []string{"100.64.0.0/10"}
		d.Listener = listener
		d.InterfaceLister = nil
		d.ShutdownTimeout = 2 * time.Second
		d.VaultKey = makeKey(t)
	})
	if err := srv.Mount(http.MethodGet, "/wait", probe); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	addr := "http://" + listener.Addr().String() + "/h/abcdef"

	var wg sync.WaitGroup
	wg.Add(1)
	var inflightStatus int
	go func() {
		defer wg.Done()
		client := &http.Client{Timeout: 4 * time.Second}
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, addr+"/wait", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("inflight err: %v", err)
			return
		}
		inflightStatus = resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	time.Sleep(50 * time.Millisecond)

	cancel()
	_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)

	close(release)
	wg.Wait()
	if inflightStatus != http.StatusOK {
		t.Fatalf("inflight status=%d want 200", inflightStatus)
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within ShutdownTimeout")
	}

	postReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, addr+"/wait", nil)
	_, err = http.DefaultClient.Do(postReq)
	if err == nil {
		t.Fatal("post-shutdown request should fail")
	}
}
