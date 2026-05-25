package sign

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewNonceCache_DefaultCap pins the public-API contract: NewNonceCache()
// returns a cache with DefaultNonceCacheMaxEntries as its hard cap. Without
// this guard, a future refactor that drops the cap by accident would
// reintroduce the unbounded-growth DoS (audit finding T1).
func TestNewNonceCache_DefaultCap(t *testing.T) {
	t.Parallel()

	c, ok := NewNonceCache().(*nonceCache)
	if !ok {
		t.Fatalf("NewNonceCache returned unexpected concrete type %T", c)
	}
	if c.maxEntries != DefaultNonceCacheMaxEntries {
		t.Errorf("maxEntries = %d, want %d (DefaultNonceCacheMaxEntries)",
			c.maxEntries, DefaultNonceCacheMaxEntries)
	}
}

// TestNewNonceCacheWithCap_NonPositiveFallsBackToDefault confirms the
// fail-closed clamp: a caller that supplies <=0 cannot disable the cap.
func TestNewNonceCacheWithCap_NonPositiveFallsBackToDefault(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, -1, -1 << 30} {
		c, ok := NewNonceCacheWithCap(n).(*nonceCache)
		if !ok {
			t.Fatalf("NewNonceCacheWithCap(%d) returned unexpected type %T", n, c)
		}
		if c.maxEntries != DefaultNonceCacheMaxEntries {
			t.Errorf("cap %d: maxEntries = %d, want %d",
				n, c.maxEntries, DefaultNonceCacheMaxEntries)
		}
	}
}

// TestNonce_AddRejectsAtCapWithErrNonceCacheFull is the load-bearing test for
// the T1 fix: once the cache reaches its cap with all-valid (unexpired)
// entries, the next distinct nonce MUST resolve as ErrNonceCacheFull — a
// loud, distinct sentinel that the handler maps to 503 + saturation audit.
// Returning ErrNonceReplay here would let saturation hide as replay and
// defeat the whole purpose of the cap.
func TestNonce_AddRejectsAtCapWithErrNonceCacheFull(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	const capN = 4
	cache := newCappedNonceCacheForTest(capN)

	// Fill to exactly capN with distinct, still-valid nonces.
	for i := range capN {
		nonce := fmt.Sprintf("cap-nonce-%04d", i)
		first, err := cache.Add(t.Context(), nonce, validTTL)
		if err != nil || !first {
			t.Fatalf("fill i=%d: firstSeen=%v err=%v", i, first, err)
		}
	}
	if got := cache.len(); got != int64(capN) {
		t.Fatalf("after fill: entry count = %d, want %d", got, capN)
	}

	// One past capN — must reject with ErrNonceCacheFull, not replay.
	first, err := cache.Add(t.Context(), "cap-nonce-overflow", validTTL)
	if first {
		t.Error("expected firstSeen=false on cap overflow")
	}
	if !errors.Is(err, ErrNonceCacheFull) {
		t.Errorf("expected ErrNonceCacheFull, got %v", err)
	}
	if errors.Is(err, ErrNonceReplay) {
		t.Error("cap-overflow must not masquerade as ErrNonceReplay (saturation-hides-replay regression)")
	}
	if got := cache.len(); got != int64(capN) {
		t.Errorf("after overflow: entry count = %d, want %d (must not have inserted)", got, capN)
	}
}

// TestNonce_ReplayWinsOverCapFull pins the ordering invariant: a replay
// attempt against an ALREADY-PRESENT nonce must always resolve as
// ErrNonceReplay even when the cache is saturated. Otherwise an attacker
// could detect cache pressure by toggling between cap-full and replay
// outcomes for the same nonce — and operators would lose the ability to
// distinguish "we're under replay attack" from "we're under flood".
func TestNonce_ReplayWinsOverCapFull(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	const capN = 2
	cache := newCappedNonceCacheForTest(capN)

	// Seed the cache: first nonce occupies one slot; second fills.
	knownNonce := "known-replay-test"
	if _, err := cache.Add(t.Context(), knownNonce, validTTL); err != nil {
		t.Fatalf("seed knownNonce: %v", err)
	}
	if _, err := cache.Add(t.Context(), "filler-nonce-x", validTTL); err != nil {
		t.Fatalf("seed filler: %v", err)
	}
	if got := cache.len(); got != int64(capN) {
		t.Fatalf("seed: count = %d, want %d", got, capN)
	}

	// Cap is hit. A NEW nonce must be rejected as full…
	_, err := cache.Add(t.Context(), "new-distinct-nonce", validTTL)
	if !errors.Is(err, ErrNonceCacheFull) {
		t.Errorf("new nonce at cap: want ErrNonceCacheFull, got %v", err)
	}

	// …but a replay of the known nonce must STILL be flagged as replay.
	first, err := cache.Add(t.Context(), knownNonce, validTTL)
	if first {
		t.Error("replay at cap: firstSeen=true (replay defense regressed)")
	}
	if !errors.Is(err, ErrNonceReplay) {
		t.Errorf("replay at cap: want ErrNonceReplay, got %v", err)
	}
	if errors.Is(err, ErrNonceCacheFull) {
		t.Error("replay at cap leaked ErrNonceCacheFull (saturation-masks-replay regression)")
	}
}

// TestNonce_SweepFreesCapacity verifies that the timer-driven sweep
// reclaims space after entries expire, so a saturated cache recovers
// without operator intervention.
func TestNonce_SweepFreesCapacity(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	const capN = 3
	cache := newCappedNonceCacheForTest(capN)
	ttl := 100 * time.Millisecond

	for i := range capN {
		if _, err := cache.Add(t.Context(), fmt.Sprintf("expiring-%d", i), ttl); err != nil {
			t.Fatalf("fill i=%d: %v", i, err)
		}
	}
	if _, err := cache.Add(t.Context(), "overflow-pre-sweep", ttl); !errors.Is(err, ErrNonceCacheFull) {
		t.Fatalf("pre-sweep overflow: want ErrNonceCacheFull, got %v", err)
	}

	// Advance past TTL, then sweep.
	advanceClockForTest(t, 500*time.Millisecond)
	cache.sweep()

	if got := cache.len(); got != 0 {
		t.Errorf("post-sweep entry count = %d, want 0", got)
	}

	// Cache has capacity again.
	first, err := cache.Add(t.Context(), "post-sweep-success", ttl)
	if err != nil || !first {
		t.Errorf("post-sweep add: firstSeen=%v err=%v (want true, nil)", first, err)
	}
}

// TestNonce_CASReplaceDoesNotInflateCount confirms that the lazy expired-entry
// CAS path reuses the slot in place instead of double-counting. Without this,
// a long-running cache that sees the same nonces re-arrive after TTL would
// drift the count upward and falsely trip the cap.
func TestNonce_CASReplaceDoesNotInflateCount(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	cache := newCappedNonceCacheForTest(8)
	ttl := 50 * time.Millisecond

	if _, err := cache.Add(t.Context(), validNonce, ttl); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if got := cache.len(); got != 1 {
		t.Fatalf("after first add: count = %d, want 1", got)
	}

	// Advance past TTL without sweeping — entry is still resident but expired.
	advanceClockForTest(t, 500*time.Millisecond)

	// Same nonce via CAS-replace path — must succeed but NOT bump count.
	first, err := cache.Add(t.Context(), validNonce, ttl)
	if err != nil || !first {
		t.Fatalf("CAS-replace add: firstSeen=%v err=%v", first, err)
	}
	if got := cache.len(); got != 1 {
		t.Errorf("after CAS-replace: count = %d, want 1 (CAS must reuse slot)", got)
	}
}

// TestNonce_ReplayDoesNotConsumeCapacity confirms that a hostile flood of
// REPLAYS (same nonce hammered N times) does not eat cache capacity.
// Otherwise, a single signing-key holder could lock out new clients by
// repeating one nonce forever.
func TestNonce_ReplayDoesNotConsumeCapacity(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	cache := newCappedNonceCacheForTest(8)
	if _, err := cache.Add(t.Context(), validNonce, validTTL); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for range 100 {
		_, err := cache.Add(t.Context(), validNonce, validTTL)
		if !errors.Is(err, ErrNonceReplay) {
			t.Fatalf("replay loop: want ErrNonceReplay, got %v", err)
		}
	}
	if got := cache.len(); got != 1 {
		t.Errorf("replay loop inflated count to %d, want 1", got)
	}
}

// concurrentCapOutcome aggregates the counters used by
// [TestNonce_ConcurrentFillRespectsApproxCap]. Extracted so the per-worker
// loop stays under the cognitive-complexity budget.
type concurrentCapOutcome struct {
	insertedOK    atomic.Int64
	rejectedFull  atomic.Int64
	otherFailures atomic.Int64
}

// runConcurrentCapWorker is the per-worker body. Each call performs
// perWorker distinct-nonce Adds against cache and bumps the matching
// counter on the outcome.
func runConcurrentCapWorker(
	t *testing.T,
	cache *nonceCache,
	workerID, perWorker int,
	out *concurrentCapOutcome,
) {
	t.Helper()
	for i := range perWorker {
		nonce := fmt.Sprintf("w%03d-i%04d", workerID, i)
		first, err := cache.Add(t.Context(), nonce, validTTL)
		switch {
		case err == nil && first:
			out.insertedOK.Add(1)
		case errors.Is(err, ErrNonceCacheFull):
			out.rejectedFull.Add(1)
		default:
			out.otherFailures.Add(1)
		}
	}
}

// TestNonce_ConcurrentFillRespectsApproxCap is the race-test for the cap.
// We do NOT require the count to land at exactly maxEntries (the cap check is
// deliberately racy — two goroutines may both win the Load-cap check and both
// insert before either notices). What MUST hold: (1) at least one
// ErrNonceCacheFull surfaces under saturation; (2) the final count is bounded
// by maxEntries + numWorkers (drift is bounded by the parallel-insert race
// width, not unlimited). Without (1) the cap is a no-op under concurrency;
// without (2) the design regressed to unbounded growth.
func TestNonce_ConcurrentFillRespectsApproxCap(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	const (
		capN       = 64
		numWorkers = 32
		perWorker  = 100
	)
	cache := newCappedNonceCacheForTest(capN)

	var (
		wg        sync.WaitGroup
		out       concurrentCapOutcome
		startGate = make(chan struct{})
	)

	wg.Add(numWorkers)
	for w := range numWorkers {
		go func(workerID int) {
			defer wg.Done()
			<-startGate
			runConcurrentCapWorker(t, cache, workerID, perWorker, &out)
		}(w)
	}
	close(startGate)
	wg.Wait()

	if got := out.otherFailures.Load(); got != 0 {
		t.Errorf("unexpected non-Full / non-OK outcomes: %d", got)
	}
	if got := out.rejectedFull.Load(); got == 0 {
		t.Errorf("no ErrNonceCacheFull seen under saturation — cap is a no-op (T1 regression)")
	}
	finalCount := cache.len()
	upperBound := int64(capN + numWorkers) // race width — see test docstring.
	if finalCount > upperBound {
		t.Errorf("final count = %d, exceeds cap+workers upper bound %d", finalCount, upperBound)
	}
	if out.insertedOK.Load() != finalCount {
		t.Errorf("counter drift: insertedOK=%d, finalCount=%d", out.insertedOK.Load(), finalCount)
	}
}

// TestNonce_CapStillEnforcedAfterContextDeadline is a paranoia test: the
// canceled-context check sits before the cap check, so a flood of canceled
// contexts must not consume capacity (each returns ctx.Err() before any map
// write). Without this guard, an attacker who can produce canceled requests
// at the wire layer would have a side-channel to "use up" the cap counter.
func TestNonce_CapStillEnforcedAfterContextDeadline(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	cache := newCappedNonceCacheForTest(2)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	for i := range 100 {
		_, err := cache.Add(canceledCtx, fmt.Sprintf("ctx-nonce-%d", i), validTTL)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("i=%d: want context.Canceled, got %v", i, err)
		}
	}
	if got := cache.len(); got != 0 {
		t.Errorf("canceled-context floods consumed %d capacity slots", got)
	}

	// Fresh context: cap is intact.
	for i := range 2 {
		if _, err := cache.Add(t.Context(), fmt.Sprintf("fresh-nonce-%d", i), validTTL); err != nil {
			t.Fatalf("fresh add i=%d: %v", i, err)
		}
	}
	if _, err := cache.Add(t.Context(), "fresh-overflow", validTTL); !errors.Is(err, ErrNonceCacheFull) {
		t.Errorf("fresh overflow: want ErrNonceCacheFull, got %v", err)
	}
}
