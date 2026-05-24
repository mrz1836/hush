package server

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// TestReloadVault_HappyPath_SwapsPointer — pointer is swapped to the new
// store; AuditVaultReloaded is emitted exactly once.
func TestReloadVault_HappyPath_SwapsPointer(t *testing.T) {
	t.Parallel()

	storeA := newFakeStore("A", []byte("a"))
	storeB := newFakeStore("B", []byte("b"))
	srv, audit, _, _ := newTestServer(t, func(d *Deps) {
		initial := vault.Store(storeA)
		var p atomic.Pointer[vault.Store]
		p.Store(&initial)
		d.VaultPtr = &p
		d.LoadVaultFn = stubLoadVault(storeB, nil)
		d.ReloadDrainWindow = 5 * time.Millisecond
	})

	key := makeKey(t)
	defer func() { _ = key.Destroy() }()

	if err := srv.ReloadVault(context.Background(), "/some/path", key); err != nil {
		t.Fatalf("ReloadVault err=%v", err)
	}

	got := *srv.vaultPtr.Load()
	if got != storeB {
		t.Fatalf("vaultPtr post-swap = %T %p, want %p", got, got, storeB)
	}

	events := audit.snapshot()
	count := 0
	for _, e := range events {
		if e.Type == AuditVaultReloaded {
			count++
			if e.Detail["to_path"] != "/some/path" {
				t.Errorf("audit detail to_path=%q want /some/path", e.Detail["to_path"])
			}
		}
	}
	if count != 1 {
		t.Fatalf("AuditVaultReloaded count=%d, want 1", count)
	}
}

// TestReloadVault_FailedReload_PointerUnchanged — three rows for the three
// reload error categories.
//
//nolint:gocognit // table-driven test with N cases × multiple assertions
func TestReloadVault_FailedReload_PointerUnchanged(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		loadErr error
		want    error
	}{
		{"missing", fs.ErrNotExist, ErrReloadFileMissing},
		{"decrypt", vault.ErrAuthFailed, ErrReloadDecryptFailed},
		{"invalid", vault.ErrBadMagic, ErrReloadInvalid},
		{"perms-loose", vault.ErrFilePermsLoose, ErrReloadInvalid},
		{"too-large", vault.ErrFileTooLarge, ErrReloadInvalid},
		{"other", errTestSynthetic, ErrReloadInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			storeA := newFakeStore("A", []byte("a"))
			srv, _, _, _ := newTestServer(t, func(d *Deps) {
				initial := vault.Store(storeA)
				var p atomic.Pointer[vault.Store]
				p.Store(&initial)
				d.VaultPtr = &p
				d.LoadVaultFn = stubLoadVault(nil, tc.loadErr)
			})
			key := makeKey(t)
			defer func() { _ = key.Destroy() }()

			err := srv.ReloadVault(context.Background(), "/path/x", key)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want errors.Is(_, %v)", err, tc.want)
			}
			if !strings.Contains(err.Error(), "/path/x") {
				t.Fatalf("error should name failing path; got %v", err)
			}

			// Pointer unchanged.
			if got := *srv.vaultPtr.Load(); got != storeA {
				t.Fatalf("vaultPtr changed after failed reload")
			}
			// Old store NOT destroyed.
			if storeA.destroys() != 0 {
				t.Fatalf("storeA.Destroy called %d times after failed reload", storeA.destroys())
			}
		})
	}
}

// TestReloadVault_DrainWindowDestroysOnce — the previous store is destroyed
// after the drain window expires, exactly once.
func TestReloadVault_DrainWindowDestroysOnce(t *testing.T) {
	t.Parallel()

	storeA := newFakeStore("A", []byte("a"))
	storeB := newFakeStore("B", []byte("b"))

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		initial := vault.Store(storeA)
		var p atomic.Pointer[vault.Store]
		p.Store(&initial)
		d.VaultPtr = &p
		d.LoadVaultFn = stubLoadVault(storeB, nil)
		d.ReloadDrainWindow = 30 * time.Millisecond
	})

	key := makeKey(t)
	defer func() { _ = key.Destroy() }()

	start := time.Now()
	if err := srv.ReloadVault(context.Background(), "/x", key); err != nil {
		t.Fatalf("ReloadVault err=%v", err)
	}
	elapsed := time.Since(start)

	// The reload holds the mutex through the drain window, so the call
	// blocks for at least that long.
	if elapsed < 25*time.Millisecond {
		t.Fatalf("reload returned in %v; expected ≥ drain window", elapsed)
	}

	if got := storeA.destroys(); got != 1 {
		t.Fatalf("storeA.Destroy count=%d, want 1", got)
	}
	if got := storeB.destroys(); got != 0 {
		t.Fatalf("storeB.Destroy count=%d, want 0", got)
	}
}

// TestReloadVault_Serialised_TwoSighupsBackToBack — second reload waits for
// first drain to complete; both old vaults destroyed exactly once.
//
//nolint:gocognit // multi-goroutine timing test
func TestReloadVault_Serialised_TwoSighupsBackToBack(t *testing.T) {
	t.Parallel()

	storeA := newFakeStore("A", []byte("a"))
	storeB := newFakeStore("B", []byte("b"))
	storeC := newFakeStore("C", []byte("c"))

	loadCount := 0
	loadFn := func(_ context.Context, _ string, _ *securebytes.SecureBytes) (vault.Store, error) { //nolint:unparam // signature locked by LoadVaultFn; both branches succeed by design
		loadCount++
		if loadCount == 1 {
			return storeB, nil
		}
		return storeC, nil
	}

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		initial := vault.Store(storeA)
		var p atomic.Pointer[vault.Store]
		p.Store(&initial)
		d.VaultPtr = &p
		d.LoadVaultFn = loadFn
		d.ReloadDrainWindow = 25 * time.Millisecond
	})

	key := makeKey(t)
	defer func() { _ = key.Destroy() }()

	var wg sync.WaitGroup
	wg.Add(2)
	start := time.Now()
	go func() {
		defer wg.Done()
		if err := srv.ReloadVault(context.Background(), "/p1", key); err != nil {
			t.Errorf("reload 1 err=%v", err)
		}
	}()
	// Slight delay so the second reload arrives while the first holds the
	// mutex (during drain).
	time.Sleep(5 * time.Millisecond)
	go func() {
		defer wg.Done()
		if err := srv.ReloadVault(context.Background(), "/p2", key); err != nil {
			t.Errorf("reload 2 err=%v", err)
		}
	}()
	wg.Wait()
	elapsed := time.Since(start)

	// Both reloads serialise through the mutex, so the second one cannot
	// finish until the first drain completes; total elapsed ≥ ~2 windows.
	if elapsed < 40*time.Millisecond {
		t.Fatalf("two reloads completed in %v; expected ≥ 2× drain window", elapsed)
	}
	// The final pointer is C.
	if got := *srv.vaultPtr.Load(); got != storeC {
		t.Fatalf("final vaultPtr = %T %p, want %p", got, got, storeC)
	}
	// A and B each destroyed exactly once; C still live.
	if storeA.destroys() != 1 {
		t.Errorf("storeA destroys=%d, want 1", storeA.destroys())
	}
	if storeB.destroys() != 1 {
		t.Errorf("storeB destroys=%d, want 1", storeB.destroys())
	}
	if storeC.destroys() != 0 {
		t.Errorf("storeC destroys=%d, want 0 (still live)", storeC.destroys())
	}
}

// TestReloadVault_DuringShutdown_ReturnsErrShuttingDown — shuttingDown=true
// makes ReloadVault return ErrShuttingDown.
func TestReloadVault_DuringShutdown_ReturnsErrShuttingDown(t *testing.T) {
	t.Parallel()

	storeA := newFakeStore("A", []byte("a"))
	storeB := newFakeStore("B", []byte("b"))
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		initial := vault.Store(storeA)
		var p atomic.Pointer[vault.Store]
		p.Store(&initial)
		d.VaultPtr = &p
		d.LoadVaultFn = stubLoadVault(storeB, nil)
	})
	srv.shuttingDown.Store(true)

	key := makeKey(t)
	defer func() { _ = key.Destroy() }()

	err := srv.ReloadVault(context.Background(), "/x", key)
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("err=%v want ErrShuttingDown", err)
	}
	// Pointer unchanged.
	if got := *srv.vaultPtr.Load(); got != storeA {
		t.Fatalf("vaultPtr changed during shutdown")
	}
}

// TestReloadVault_NilKey_RejectedBeforeLoad — protecting against caller bug.
func TestReloadVault_NilKey_RejectedBeforeLoad(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.LoadVaultFn = func(_ context.Context, _ string, _ *securebytes.SecureBytes) (vault.Store, error) {
			t.Fatalf("LoadVaultFn invoked with nil key")
			return nil, errTestSynthetic
		}
	})
	err := srv.ReloadVault(context.Background(), "/x", nil)
	if !errors.Is(err, ErrReloadInvalid) {
		t.Fatalf("err=%v want ErrReloadInvalid", err)
	}
}

// TestReloadVault_NilStore_RejectedAfterLoad — load returns (nil, nil).
func TestReloadVault_NilStore_RejectedAfterLoad(t *testing.T) {
	t.Parallel()

	storeA := newFakeStore("A", []byte("a"))
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		initial := vault.Store(storeA)
		var p atomic.Pointer[vault.Store]
		p.Store(&initial)
		d.VaultPtr = &p
		d.LoadVaultFn = stubLoadVault(nil, nil)
	})
	key := makeKey(t)
	defer func() { _ = key.Destroy() }()

	err := srv.ReloadVault(context.Background(), "/x", key)
	if !errors.Is(err, ErrReloadInvalid) {
		t.Fatalf("err=%v want ErrReloadInvalid", err)
	}
	if got := *srv.vaultPtr.Load(); got != storeA {
		t.Fatalf("vaultPtr drifted after nil-store reload")
	}
}

// TestVaultPointerSwap_NoRace — N reader goroutines + M reload goroutines
// drive heavy concurrent traffic; race detector validates correctness.
//
//nolint:gocognit // concurrency stress test
func TestVaultPointerSwap_NoRace(t *testing.T) {
	t.Parallel()

	storeA := newFakeStore("A", []byte("a"))
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		initial := vault.Store(storeA)
		var p atomic.Pointer[vault.Store]
		p.Store(&initial)
		d.VaultPtr = &p
		// Each call yields a fresh fake store so we exercise every swap.
		d.LoadVaultFn = func(_ context.Context, _ string, _ *securebytes.SecureBytes) (vault.Store, error) {
			return newFakeStore("X", []byte("x")), nil
		}
		d.ReloadDrainWindow = 1 * time.Millisecond
	})

	key := makeKey(t)
	defer func() { _ = key.Destroy() }()

	const (
		readers = 100
		writers = 10
	)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = *srv.vaultPtr.Load()
			}
		}()
	}
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = srv.ReloadVault(context.Background(), "/x", key)
			}
		}()
	}

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestReloadVault_NonceCacheCoherency pins the invariant that the nonce
// cache (which tracks replay-protection across the lifetime of the
// chassis) is NOT cleared when the vault is reloaded — the cache lives
// outside the vault pointer. A nonce burned by /claim before reload
// must still be rejected as a replay after reload, otherwise SIGHUP
// would silently widen the replay window.
func TestReloadVault_NonceCacheCoherency(t *testing.T) {
	t.Parallel()

	storeB := newFakeStore("B", []byte("b"))
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
		withDepsMutator(func(d *Deps) {
			d.LoadVaultFn = stubLoadVault(storeB, nil)
			d.ReloadDrainWindow = 1 * time.Millisecond
		}),
	)

	o := defaultClaimBodyOpts(h)
	body := signedClaimBody(t, h, o)

	// First /claim with nonce N1 → 200.
	rr1, _ := h.do(t, body)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first claim status=%d want 200; body=%s", rr1.Code, rr1.Body.String())
	}

	// SIGHUP-equivalent: swap the vault.
	key := makeKey(t)
	defer func() { _ = key.Destroy() }()
	if err := h.server.ReloadVault(context.Background(), "/reload/path", key); err != nil {
		t.Fatalf("ReloadVault: %v", err)
	}

	// Second /claim with the SAME body (and therefore same nonce) → 403
	// nonce_replay. If the cache had been cleared by reload, this would
	// otherwise have approved a second time.
	rr2, _ := h.do(t, body)
	if rr2.Code != http.StatusForbidden {
		t.Fatalf("post-reload claim status=%d want 403 (nonce_replay); body=%s", rr2.Code, rr2.Body.String())
	}
	assertErrorBodyShape(t, rr2, "nonce_replay")
}

// TestWrapReloadError_Categories pins each branch of the categoriser.
func TestWrapReloadError_Categories(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   error
		want error
	}{
		{fs.ErrNotExist, ErrReloadFileMissing},
		{vault.ErrAuthFailed, ErrReloadDecryptFailed},
		{vault.ErrFilePermsLoose, ErrReloadInvalid},
		{vault.ErrBadMagic, ErrReloadInvalid},
		{vault.ErrBadVersion, ErrReloadInvalid},
		{vault.ErrShortHeader, ErrReloadInvalid},
		{vault.ErrFileTooLarge, ErrReloadInvalid},
		{vault.ErrInvalidName, ErrReloadInvalid},
		{vault.ErrDuplicateName, ErrReloadInvalid},
		{errTestSynthetic, ErrReloadInvalid},
	}
	for _, tc := range cases {
		got := wrapReloadError("/p", tc.in)
		if !errors.Is(got, tc.want) {
			t.Errorf("wrapReloadError(%v) → %v, want errors.Is(_, %v)", tc.in, got, tc.want)
		}
	}
}
