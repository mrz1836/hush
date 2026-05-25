package supervise

import (
	"bytes"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// TestGrace_UsesCacheOnExpiredJWT: Set then Get within the window
// returns the cached SecureBytes intact.
func TestGrace_UsesCacheOnExpiredJWT(t *testing.T) {
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC))
	g := NewGrace(60*time.Minute, true)
	g.setClockForTest(clk.Now)

	sb := newSecureBytes(t, []byte("API_KEY_VALUE"))
	g.Set("API_KEY", sb)

	clk.Advance(30 * time.Minute)
	got, ok := g.Get("API_KEY")
	if !ok {
		t.Fatalf("Get miss before expiry")
	}
	var observed []byte
	if err := got.Use(func(b []byte) { observed = append(observed, b...) }); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if string(observed) != "API_KEY_VALUE" {
		t.Fatalf("got %q want API_KEY_VALUE", observed)
	}
}

// TestGrace_TTLCapAt4h: NewGrace(8h, true) is capped to 4h; an entry
// is evicted at T0+4h+1ns.
func TestGrace_TTLCapAt4h(t *testing.T) {
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC))
	g := NewGrace(8*time.Hour, true)
	g.setClockForTest(clk.Now)

	sb := newSecureBytes(t, []byte("X"))
	g.Set("X", sb)

	clk.Advance(4*time.Hour + time.Nanosecond)
	if _, ok := g.Get("X"); ok {
		t.Fatalf("Get returned hit past 4h cap")
	}
	if err := sb.Use(func(_ []byte) {}); !errors.Is(err, securebytes.ErrDestroyed) {
		t.Fatalf("sb.Use err=%v want ErrDestroyed", err)
	}
}

// TestGrace_DisabledWhenConfigFalse: enabled=false produces no-op
// Set; ownership stays with caller.
func TestGrace_DisabledWhenConfigFalse(t *testing.T) {
	g := NewGrace(60*time.Minute, false)

	sb := newSecureBytes(t, []byte("X"))
	g.Set("X", sb)

	if _, ok := g.Get("X"); ok {
		t.Fatalf("disabled cache returned hit")
	}
	var got []byte
	if err := sb.Use(func(b []byte) { got = append(got, b...) }); err != nil {
		t.Fatalf("sb still owned by caller; Use err=%v", err)
	}
	if string(got) != "X" {
		t.Fatalf("got %q want X", got)
	}
}

// TestGrace_ZeroWindowEqualsDisabled: window=0 produces a permanently-
// empty cache; sb is not destroyed by Set (B-GR-4).
func TestGrace_ZeroWindowEqualsDisabled(t *testing.T) {
	g := NewGrace(0, true)
	sb := newSecureBytes(t, []byte("X"))
	g.Set("X", sb)
	if _, ok := g.Get("X"); ok {
		t.Fatalf("zero-window cache returned hit")
	}
	if err := sb.Use(func(_ []byte) {}); err != nil {
		t.Fatalf("sb destroyed by zero-window Set: %v", err)
	}
}

// TestGrace_LazyEvictsOnGetAfterTTL: Get on an expired entry destroys
// + removes the entry inline.
func TestGrace_LazyEvictsOnGetAfterTTL(t *testing.T) {
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC))
	g := NewGrace(30*time.Minute, true)
	g.setClockForTest(clk.Now)

	sb := newSecureBytes(t, []byte("X"))
	g.Set("X", sb)

	clk.Advance(30*time.Minute + time.Nanosecond)
	if _, ok := g.Get("X"); ok {
		t.Fatalf("Get returned hit past TTL")
	}
	if err := sb.Use(func(_ []byte) {}); !errors.Is(err, securebytes.ErrDestroyed) {
		t.Fatalf("sb.Use err=%v want ErrDestroyed", err)
	}
	g.mu.RLock()
	size := len(g.entries)
	g.mu.RUnlock()
	if size != 0 {
		t.Fatalf("entries=%d, want 0 after lazy-evict", size)
	}
}

// TestGrace_EvictDestroysAndRemoves: explicit Evict destroys the
// cached SecureBytes and removes the map slot.
func TestGrace_EvictDestroysAndRemoves(t *testing.T) {
	g := NewGrace(time.Hour, true)
	sb := newSecureBytes(t, []byte("X"))
	g.Set("X", sb)
	g.Evict("X")

	if err := sb.Use(func(_ []byte) {}); !errors.Is(err, securebytes.ErrDestroyed) {
		t.Fatalf("sb.Use err=%v want ErrDestroyed", err)
	}
	if _, ok := g.Get("X"); ok {
		t.Fatalf("Get returned hit after Evict")
	}
	g.mu.RLock()
	size := len(g.entries)
	g.mu.RUnlock()
	if size != 0 {
		t.Fatalf("entries=%d, want 0", size)
	}
}

// TestGrace_EvictOnAbsentNameIsNoop: Evict on missing key is silent.
func TestGrace_EvictOnAbsentNameIsNoop(t *testing.T) {
	g := NewGrace(time.Hour, true)
	g.Evict("nonexistent")
	g.mu.RLock()
	size := len(g.entries)
	g.mu.RUnlock()
	if size != 0 {
		t.Fatalf("entries=%d, want 0", size)
	}
}

// TestGrace_EvictAllZeroesEveryEntryPreservesEnabled: EvictAll walks
// every cached entry, calls sb.Destroy on each, clears the map, AND
// keeps the cache enabled so subsequent Set calls continue to cache.
// This is the path the orchestrator uses when an authoritative revoke
// (vault returns unknown_jti) invalidates every cached plaintext from
// the now-revoked session — distinct from Destroy, which is permanent.
func TestGrace_EvictAllZeroesEveryEntryPreservesEnabled(t *testing.T) {
	g := NewGrace(time.Hour, true)
	sb1 := newSecureBytes(t, []byte("A"))
	sb2 := newSecureBytes(t, []byte("B"))
	sb3 := newSecureBytes(t, []byte("C"))
	g.Set("scope1", sb1)
	g.Set("scope2", sb2)
	g.Set("scope3", sb3)

	g.EvictAll()

	for label, sb := range map[string]*securebytes.SecureBytes{"sb1": sb1, "sb2": sb2, "sb3": sb3} {
		if err := sb.Use(func(_ []byte) {}); !errors.Is(err, securebytes.ErrDestroyed) {
			t.Fatalf("%s.Use err=%v want ErrDestroyed after Grace.EvictAll", label, err)
		}
	}
	g.mu.RLock()
	size := len(g.entries)
	g.mu.RUnlock()
	if size != 0 {
		t.Fatalf("entries=%d after EvictAll, want 0", size)
	}

	if !g.Enabled() {
		t.Fatalf("Grace.Enabled() = false after EvictAll; want true (EvictAll preserves caching config, unlike Destroy)")
	}

	// Subsequent Set must continue to cache — EvictAll is not permanent.
	revived := newSecureBytes(t, []byte("post-evictall"))
	g.Set("scope1", revived)
	got, ok := g.Get("scope1")
	if !ok || got != revived {
		t.Fatalf("post-EvictAll Set/Get failed; ok=%v got=%v want %v", ok, got, revived)
	}
}

// TestGrace_EvictAllOnEmpty: EvictAll on a never-populated cache is a
// silent no-op and leaves the cache enabled.
func TestGrace_EvictAllOnEmpty(t *testing.T) {
	g := NewGrace(time.Hour, true)
	g.EvictAll()
	if !g.Enabled() {
		t.Fatalf("Grace.Enabled() = false after EvictAll on empty cache; want true")
	}
}

// TestGrace_EvictAllOnDisabledIsNoop: EvictAll on a disabled cache is
// a silent no-op (there are no entries to walk).
func TestGrace_EvictAllOnDisabledIsNoop(t *testing.T) {
	g := NewGrace(time.Hour, false)
	g.EvictAll() // must not panic.
}

// TestGrace_SetOverwriteDestroysPrior: Set on an existing key
// destroys the prior SecureBytes.
func TestGrace_SetOverwriteDestroysPrior(t *testing.T) {
	g := NewGrace(time.Hour, true)
	sb1 := newSecureBytes(t, []byte("A"))
	sb2 := newSecureBytes(t, []byte("B"))

	g.Set("X", sb1)
	if err := sb1.Use(func(_ []byte) {}); err != nil {
		t.Fatalf("sb1 dead too early: %v", err)
	}

	g.Set("X", sb2)
	if err := sb1.Use(func(_ []byte) {}); !errors.Is(err, securebytes.ErrDestroyed) {
		t.Fatalf("sb1.Use err=%v want ErrDestroyed", err)
	}
	got, ok := g.Get("X")
	if !ok {
		t.Fatalf("Get miss after overwrite")
	}
	if got != sb2 {
		t.Fatalf("Get returned a different pointer")
	}
}

// TestGrace_NeverRendersValueAsString: Grace's value pointers go
// through SecureBytes.LogValue() which redacts. A direct slog call on
// the cache must not leak any underlying bytes.
func TestGrace_NeverRendersValueAsString(t *testing.T) {
	const marker = "HUSH-MARKER-21-CACHED"
	g := NewGrace(time.Hour, true)
	sb := newSecureBytes(t, []byte(marker))
	g.Set("X", sb)

	logger, buf := newRecordingLogger()
	got, _ := g.Get("X")
	logger.Info("dump", slog.Any("value", got))
	if bytes.Contains(buf.Bytes(), []byte(marker)) {
		t.Fatalf("log leaked marker: %s", buf.String())
	}
	if v := got.LogValue().String(); v != "[redacted]" {
		t.Fatalf("LogValue=%q want [redacted]", v)
	}
}

// graceWorker hammers Get/Set/Evict against a single Grace key in a
// random interleave. Extracted from TestGrace_ConcurrentRaceClean to
// keep the test below the gocognit ceiling.
func graceWorker(t *testing.T, g *Grace, seed int64) {
	t.Helper()
	r := rand.New(rand.NewPCG(uint64(seed), 0))
	for range 50 {
		switch r.IntN(3) {
		case 0:
			sb := newSecureBytes(t, []byte("v"))
			g.Set("K", sb)
		case 1:
			_, _ = g.Get("K")
		case 2:
			g.Evict("K")
		}
	}
}

// TestGrace_DestroyZeroesAllEntries: Destroy walks every cached entry,
// calls sb.Destroy on each, clears the map, and disables the cache.
// This is the Principle-VI explicit-zeroing path the supervisor's
// SIGTERM handler invokes — finalizers do not run on process exit, so a
// missing Destroy here would leave plaintext until the kernel reclaimed
// the pages.
func TestGrace_DestroyZeroesAllEntries(t *testing.T) {
	g := NewGrace(time.Hour, true)
	sb1 := newSecureBytes(t, []byte("A"))
	sb2 := newSecureBytes(t, []byte("B"))
	sb3 := newSecureBytes(t, []byte("C"))
	g.Set("scope1", sb1)
	g.Set("scope2", sb2)
	g.Set("scope3", sb3)

	g.Destroy()

	for label, sb := range map[string]*securebytes.SecureBytes{"sb1": sb1, "sb2": sb2, "sb3": sb3} {
		if err := sb.Use(func(_ []byte) {}); !errors.Is(err, securebytes.ErrDestroyed) {
			t.Fatalf("%s.Use err=%v want ErrDestroyed after Grace.Destroy", label, err)
		}
	}

	g.mu.RLock()
	size := len(g.entries)
	g.mu.RUnlock()
	if size != 0 {
		t.Fatalf("entries=%d after Destroy, want 0", size)
	}

	// Cache is now disabled; subsequent Get / Set / Enabled reflect that.
	if g.Enabled() {
		t.Fatalf("Grace.Enabled() = true after Destroy; want false")
	}
	if _, ok := g.Get("scope1"); ok {
		t.Fatalf("Get hit after Destroy")
	}
}

// TestGrace_DestroyIsIdempotent: double Destroy is a silent no-op.
func TestGrace_DestroyIsIdempotent(t *testing.T) {
	g := NewGrace(time.Hour, true)
	sb := newSecureBytes(t, []byte("X"))
	g.Set("X", sb)
	g.Destroy()
	g.Destroy() // must not panic, must not call sb.Destroy twice (sb.Destroy is itself idempotent, but the map traversal must be empty)
	if g.Enabled() {
		t.Fatalf("Grace.Enabled() = true after double Destroy")
	}
}

// TestGrace_DestroyOnEmpty: Destroy on a never-populated cache is a no-op.
func TestGrace_DestroyOnEmpty(t *testing.T) {
	g := NewGrace(time.Hour, true)
	g.Destroy() // no entries, no panic.
	if g.Enabled() {
		t.Fatalf("Grace.Enabled() = true after Destroy on empty cache")
	}
}

// TestGrace_DestroyOnDisabled: Destroy on a disabled cache is a no-op.
func TestGrace_DestroyOnDisabled(t *testing.T) {
	g := NewGrace(time.Hour, false)
	g.Destroy() // already disabled, no entries, no panic.
	if g.Enabled() {
		t.Fatalf("Grace.Enabled() = true after Destroy on disabled cache")
	}
}

// TestGrace_PostDestroySetIsNoop: after Destroy, Set leaves the
// caller's SecureBytes intact (ownership-stays-with-caller contract,
// matching disabled mode).
func TestGrace_PostDestroySetIsNoop(t *testing.T) {
	g := NewGrace(time.Hour, true)
	g.Destroy()

	sb := newSecureBytes(t, []byte("late"))
	g.Set("late", sb)

	// sb must still be usable — Grace did NOT take ownership.
	var got []byte
	if err := sb.Use(func(b []byte) { got = append(got, b...) }); err != nil {
		t.Fatalf("sb.Use err=%v want nil; Grace.Set after Destroy must not consume", err)
	}
	if string(got) != "late" {
		t.Fatalf("got %q want %q", got, "late")
	}

	// Map must still be empty.
	g.mu.RLock()
	size := len(g.entries)
	g.mu.RUnlock()
	if size != 0 {
		t.Fatalf("entries=%d after post-Destroy Set, want 0", size)
	}

	// Caller cleans up since Grace did not take ownership.
	_ = sb.Destroy()
}

// TestGrace_ConcurrentRaceClean: 100 goroutines hammering Set/Get/
// Evict against the same key — race-clean and consistent.
func TestGrace_ConcurrentRaceClean(t *testing.T) {
	g := NewGrace(time.Hour, true)
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func(i int) {
			defer wg.Done()
			graceWorker(t, g, int64(i))
		}(i)
	}
	wg.Wait()

	// Final state: either the key is present-and-alive, or absent-
	// and-destroyed. We just assert no panic and that the type
	// invariants are still good — a fresh Set + Get works.
	g.Evict("K")
	probe := newSecureBytes(t, []byte("probe"))
	g.Set("K", probe)
	if _, ok := g.Get("K"); !ok {
		t.Fatalf("post-race Set/Get round-trip failed")
	}
}
