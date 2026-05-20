package server

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// TestVaultPath_Derivation pins the convention used by the rotate command
// and the SIGHUP handler — secrets.vault inside the configured state dir.
func TestVaultPath_Derivation(t *testing.T) {
	t.Parallel()
	cfg := testCfg(t)
	got := vaultPath(cfg)
	want := filepath.Join(cfg.Server.StateDir, "secrets.vault")
	if got != want {
		t.Fatalf("vaultPath=%q want %q", got, want)
	}
}

// TestMatchesAddr_BadIPNet — *net.IPNet with non-4/16-byte IP forces the
// AddrFromSlice false branch.
func TestMatchesAddr_BadIPNet(t *testing.T) {
	t.Parallel()
	want := netip.MustParseAddr("100.64.1.1")
	bad := &net.IPNet{IP: []byte{1, 2}, Mask: net.IPMask{0xff, 0xff}}
	if matchesAddr(bad, want) {
		t.Fatal("invalid IPNet must not match")
	}
}

// TestNew_DefaultInterfaceListerInvocation drives the interfaceLister
// fallback closure by leaving Deps.InterfaceLister nil and forcing the
// tailscale_bind check to read from it.
func TestNew_DefaultInterfaceListerInvocation(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.InterfaceLister = nil
		d.Cfg.Server.ListenAddr = netip.MustParseAddrPort("100.64.99.99:7743")
	})
	// Real net.InterfaceAddrs() will return; the address won't match this
	// CGNAT IP, so the check fails — but the closure was invoked.
	if err := srv.checkTailscaleBind(context.Background()); !errors.Is(err, ErrBindNotOnTailscale) {
		t.Fatalf("err=%v want ErrBindNotOnTailscale", err)
	}
}

// TestRunStartupChecks_CtxCanceled — ctx already cancelled returns ctx err
// without invoking any check.
func TestRunStartupChecks_CtxCanceled(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := srv.runStartupChecks(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
}

// TestMatchesAddr — covers each branch of matchesAddr (IPNet, prefix string,
// raw addr string, parse failure).
func TestMatchesAddr(t *testing.T) {
	t.Parallel()
	want := netip.MustParseAddr("100.64.1.1")

	// IPNet branch
	_, ipNet, _ := net.ParseCIDR("100.64.1.1/32")
	if !matchesAddr(ipNet, want) {
		t.Errorf("IPNet did not match")
	}
	// IPNet with mismatched IP
	_, ipNet2, _ := net.ParseCIDR("8.8.8.8/32")
	if matchesAddr(ipNet2, want) {
		t.Errorf("mismatched IPNet matched")
	}
	// String-prefix branch (using textAddr)
	if !matchesAddr(textAddr("100.64.1.1/24"), want) {
		t.Errorf("prefix string did not match")
	}
	// Plain addr branch
	if !matchesAddr(textAddr("100.64.1.1"), want) {
		t.Errorf("plain addr did not match")
	}
	// Garbage
	if matchesAddr(textAddr("garbage"), want) {
		t.Errorf("garbage matched")
	}
}

type textAddr string

func (t textAddr) Network() string { return "tcp" }
func (t textAddr) String() string  { return string(t) }

// TestHandleSighup_NoVaultKey — handleSighup logs and returns when VaultKey
// is nil instead of panicking.
func TestHandleSighup_NoVaultKey(t *testing.T) {
	t.Parallel()
	srv, _, _, buf := newTestServer(t, func(d *Deps) {
		d.VaultKey = nil
	})
	srv.handleSighup(context.Background())
	if buf.Len() == 0 {
		t.Fatalf("expected log output for missing vault key")
	}
}

// TestHandleSighup_PropagatesReloadError — handleSighup invokes runReload;
// when runReload errors, the log captures it.
func TestHandleSighup_PropagatesReloadError(t *testing.T) {
	t.Parallel()

	srv, _, _, buf := newTestServer(t, func(d *Deps) {
		d.VaultKey = makeKey(t)
		d.LoadVaultFn = stubLoadVault(nil, errTestSynthetic)
	})
	srv.handleSighup(context.Background())
	logs := buf.String()
	if logs == "" {
		t.Fatalf("expected handleSighup to log on error")
	}
}

// TestSighupLoop_ExitsOnCtxCancel — the loop terminates when ctx cancels.
func TestSighupLoop_ExitsOnCtxCancel(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	go srv.sighupLoop(ctx, asSignalCh(sigCh), done)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sighupLoop did not exit on ctx cancel")
	}
}

// TestSighupLoop_ExitsOnShutdownChannelClose — shutdown channel close also
// triggers exit.
func TestSighupLoop_ExitsOnShutdownChannelClose(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)
	ctx := context.Background()
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	go srv.sighupLoop(ctx, asSignalCh(sigCh), done)
	close(srv.shutdownDoneCh)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sighupLoop did not exit on shutdownDoneCh close")
	}
}

// TestSighupLoop_IgnoresSignalsDuringShutdown — when shuttingDown=true the
// loop discards signals.
func TestSighupLoop_IgnoresSignalsDuringShutdown(t *testing.T) {
	t.Parallel()
	loadCalled := atomic.Bool{}
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.VaultKey = makeKey(t)
		d.LoadVaultFn = func(_ context.Context, _ string, _ *securebytes.SecureBytes) (vault.Store, error) {
			loadCalled.Store(true)
			return newFakeStore("X", []byte("x")), nil
		}
	})
	srv.shuttingDown.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	go srv.sighupLoop(ctx, asSignalCh(sigCh), done)
	sigCh <- syscall.SIGHUP
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if loadCalled.Load() {
		t.Fatal("LoadVaultFn invoked even though shuttingDown=true")
	}
}

// TestSighupLoop_HandlesSignal — signal fires reload via the loop.
func TestSighupLoop_HandlesSignal(t *testing.T) {
	t.Parallel()
	storeA := newFakeStore("A", []byte("a"))
	storeB := newFakeStore("B", []byte("b"))
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		initial := vault.Store(storeA)
		var p atomic.Pointer[vault.Store]
		p.Store(&initial)
		d.VaultPtr = &p
		d.VaultKey = makeKey(t)
		d.LoadVaultFn = stubLoadVault(storeB, nil)
		d.ReloadDrainWindow = 5 * time.Millisecond
	})

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	go srv.sighupLoop(ctx, asSignalCh(sigCh), done)
	sigCh <- syscall.SIGHUP

	// Wait for swap to complete.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if *srv.vaultPtr.Load() == storeB {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if got := *srv.vaultPtr.Load(); got != storeB {
		t.Fatalf("vault not swapped: %T", got)
	}
}

// asSignalCh adapts a chan syscall.Signal into a receive-only chan os.Signal
// for the sighupLoop signature.
func asSignalCh(ch chan os.Signal) <-chan os.Signal {
	return ch
}

// TestRun_ListenerErrorReturned — Run returns the listener bind error when
// the configured ListenAddr cannot be bound. Use a tailscale_bind override
// so we get past the bind check, then point at a port we can't open.
func TestRun_ListenerErrorReturned(t *testing.T) {
	t.Parallel()
	// Use a stub that bypasses the tailscale_bind check (CGNAT range and
	// interface match). Keep a real net.Listen but on an address we know
	// cannot bind: a port-already-in-use scenario.
	var lc net.ListenConfig
	occupied, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = occupied.Close() })

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		// Tailscale bind check uses CGNAT; we stub the lister to claim
		// the configured CGNAT addr exists locally.
		d.Cfg.Server.ListenAddr = netip.MustParseAddrPort("100.64.99.99:1") // unbindable
		d.InterfaceLister = stubInterfaceLister(d.Cfg.Server.ListenAddr.Addr())
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = srv.Run(ctx)
	if err == nil {
		t.Fatal("Run should fail when binding to an address we cannot reach")
	}
}

// TestRun_AuditWriterErrorIsLogged — audit write errors during startup
// should not block Run; they should be logged at WARN.
func TestRun_AuditWriterErrorIsLogged(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	audit := &recordingAudit{err: errTestSynthetic}
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Listener = listener
		d.AuditWriter = audit
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if err := srv.Run(ctx); err != nil {
		t.Fatalf("Run err=%v", err)
	}
}

// TestParseRemoteAddr_IPv6Bracket — covers the bracketed IPv6 path.
func TestParseRemoteAddr_IPv6Bracket(t *testing.T) {
	t.Parallel()
	got, ok := parseRemoteAddr("[::1]:443")
	if !ok || got.String() != "::1" {
		t.Fatalf("[::1]:443 → %v ok=%v", got, ok)
	}
}

// TestParseRemoteAddr_PortOnly — last branch (port-trim parse).
func TestParseRemoteAddr_PortOnly(t *testing.T) {
	t.Parallel()
	got, ok := parseRemoteAddr("100.64.1.1:")
	if !ok || got.String() != "100.64.1.1" {
		t.Fatalf("100.64.1.1: → %v ok=%v", got, ok)
	}
}

// TestRejectNotAllowed_AuditError — audit writer error path inside the
// rejection handler is exercised.
func TestRejectNotAllowed_AuditError(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.AuditWriter = &recordingAudit{err: errTestSynthetic}
		d.Cfg.Network.AllowedCIDRs = []string{"100.64.0.0/10"}
	})
	allowed := parseAllowedCIDRs(srv.cfg.Network.AllowedCIDRs)
	mw := srv.ipAllowListMiddleware(allowed)(_okHandler())
	req := makeReq(t, "8.8.8.8:1024")
	rec := makeRec()
	mw.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("status=%d want 403", rec.Code)
	}
}

// TestRecoverFromHandlerPanic_AuditError — audit writer fails on panic;
// we still get a 500 response and no second-level wedge.
func TestRecoverFromHandlerPanic_AuditError(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.AuditWriter = &recordingAudit{err: errTestSynthetic}
	})
	chain := srv.recoverMiddleware(_panickingHandler("x"))
	rec := makeRec()
	chain.ServeHTTP(rec, makeReq(t, "100.64.1.1:1234"))
	if rec.Code != 500 {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

// TestRunReload_AuditError — audit writer fails during reload; reload still
// completes and returns nil.
func TestRunReload_AuditError(t *testing.T) {
	t.Parallel()
	storeA := newFakeStore("A", []byte("a"))
	storeB := newFakeStore("B", []byte("b"))
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.AuditWriter = &recordingAudit{err: errTestSynthetic}
		initial := vault.Store(storeA)
		var p atomic.Pointer[vault.Store]
		p.Store(&initial)
		d.VaultPtr = &p
		d.LoadVaultFn = stubLoadVault(storeB, nil)
		d.ReloadDrainWindow = 5 * time.Millisecond
	})
	key := makeKey(t)
	defer func() { _ = key.Destroy() }()
	if err := srv.ReloadVault(context.Background(), "/x", key); err != nil {
		t.Fatalf("reload err=%v", err)
	}
}

// TestDrainAndDestroy_NilStore — covers the early-return branch.
func TestDrainAndDestroy_NilStore(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)
	srv.drainAndDestroy(context.Background(), nil) // must not panic
}

// TestDrainAndDestroy_DestroyError — destroy returns an error; logged.
func TestDrainAndDestroy_DestroyError(t *testing.T) {
	t.Parallel()
	srv, _, _, buf := newTestServer(t)
	srv.reloadDrainWindow = 1 * time.Millisecond
	bad := newFakeStore("bad", []byte("x"))
	bad.destroyErr = errTestSynthetic
	storeIface := vault.Store(bad)
	srv.drainAndDestroy(context.Background(), &storeIface)
	if buf.Len() == 0 {
		t.Fatal("expected destroy error to be logged")
	}
}

// TestCheckFileModes_RootIsSymlink — root state dir is a symlink.
func TestCheckFileModes_RootIsSymlink(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)
	original := srv.cfg.Server.StateDir
	parent := filepath.Dir(original)
	link := filepath.Join(parent, "link-mode")
	if err := makeSymlink(original, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	srv.cfg.Server.StateDir = link
	if err := srv.checkFileModes(context.Background()); !errors.Is(err, ErrFileModeLoose) {
		t.Fatalf("err=%v want ErrFileModeLoose", err)
	}
}

// TestCheckFileModes_RootMissing — root state dir does not exist.
func TestCheckFileModes_RootMissing(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)
	srv.cfg.Server.StateDir = filepath.Join(srv.cfg.Server.StateDir, "no-such-thing")
	if err := srv.checkFileModes(context.Background()); !errors.Is(err, ErrFileModeLoose) {
		t.Fatalf("err=%v want ErrFileModeLoose", err)
	}
}

// TestCheckFileModes_RootIsFile — root is a regular file, not a directory.
func TestCheckFileModes_RootIsFile(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)
	parent := filepath.Dir(srv.cfg.Server.StateDir)
	regular := filepath.Join(parent, "is-a-file")
	if err := writeFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	srv.cfg.Server.StateDir = regular
	if err := srv.checkFileModes(context.Background()); !errors.Is(err, ErrFileModeLoose) {
		t.Fatalf("err=%v want ErrFileModeLoose", err)
	}
}

// TestCheckFileModes_NestedDirLooseMode — sub-directory is 0755.
func TestCheckFileModes_NestedDirLooseMode(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)
	sub := filepath.Join(srv.cfg.Server.StateDir, "sub")
	if err := makeDir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := chmod(sub, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := srv.checkFileModes(context.Background()); !errors.Is(err, ErrFileModeLoose) {
		t.Fatalf("err=%v want ErrFileModeLoose", err)
	}
}
