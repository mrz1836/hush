package sign

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
)

const (
	validNonce = "nonce-12345678" // 14 bytes, within [8,128]
	validTTL   = 60 * time.Second
)

func TestNonce_AddNewReturnsFirstSeen(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	cache := NewNonceCache()
	firstSeen, err := cache.Add(t.Context(), validNonce, validTTL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !firstSeen {
		t.Error("expected firstSeen=true for fresh nonce")
	}
}

func TestNonce_AddDuplicateReturnsReplay(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	cache := NewNonceCache()
	_, _ = cache.Add(t.Context(), validNonce, validTTL)

	firstSeen, err := cache.Add(t.Context(), validNonce, validTTL)
	if !errors.Is(err, ErrNonceReplay) {
		t.Errorf("expected ErrNonceReplay, got %v", err)
	}
	if firstSeen {
		t.Error("expected firstSeen=false for duplicate nonce")
	}
}

func TestNonce_AddEmptyReturnsEncodingError(t *testing.T) {
	t.Parallel()
	cache := NewNonceCache()
	_, err := cache.Add(t.Context(), "", validTTL)
	if !errors.Is(err, ErrNonceEncoding) {
		t.Errorf("empty nonce: expected ErrNonceEncoding, got %v", err)
	}
}

func TestNonce_AddTooShortReturnsEncodingError(t *testing.T) {
	t.Parallel()
	cache := NewNonceCache()
	_, err := cache.Add(t.Context(), "short", validTTL) // 5 bytes
	if !errors.Is(err, ErrNonceEncoding) {
		t.Errorf("too-short nonce: expected ErrNonceEncoding, got %v", err)
	}
}

func TestNonce_AddTooLongReturnsEncodingError(t *testing.T) {
	t.Parallel()
	cache := NewNonceCache()
	longNonce := strings.Repeat("x", 129)
	_, err := cache.Add(t.Context(), longNonce, validTTL)
	if !errors.Is(err, ErrNonceEncoding) {
		t.Errorf("too-long nonce: expected ErrNonceEncoding, got %v", err)
	}
}

func TestNonce_AddNonPositiveTTLReturnsInvalid(t *testing.T) {
	t.Parallel()
	cache := NewNonceCache()
	for _, ttl := range []time.Duration{0, -1, -time.Hour} {
		_, err := cache.Add(t.Context(), validNonce, ttl)
		if !errors.Is(err, ErrNonceTTLInvalid) {
			t.Errorf("ttl=%v: expected ErrNonceTTLInvalid, got %v", ttl, err)
		}
	}
}

func TestNonce_AddRespectsCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	cache := NewNonceCache()
	_, err := cache.Add(ctx, validNonce, validTTL)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestNonce_ExpiredAllowedAfterSweep confirms that after a nonce's TTL has elapsed
// and the cache is swept, a second Add returns firstSeen=true.
func TestNonce_ExpiredAllowedAfterSweep(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	cache := newNonceCacheForTest(10 * time.Millisecond)
	ttl := 50 * time.Millisecond

	firstSeen, err := cache.Add(t.Context(), validNonce, ttl)
	if err != nil || !firstSeen {
		t.Fatalf("first Add: firstSeen=%v err=%v", firstSeen, err)
	}

	// Advance the frozen clock past TTL (use 200ms to differentiate from other tests).
	advanceClockForTest(t, 200*time.Millisecond)

	// Trigger sweep manually (simulates the Run ticker firing).
	cache.sweep()

	firstSeen2, err2 := cache.Add(t.Context(), validNonce, ttl)
	if err2 != nil {
		t.Fatalf("second Add after sweep: %v", err2)
	}
	if !firstSeen2 {
		t.Error("expected firstSeen=true after TTL elapsed and sweep ran")
	}
}

// TestNonce_ExpiredCASPath covers the CAS (lazy-expired-reuse) branch in Add:
// no sweep is called, so the expired entry is still in the map when the second
// Add arrives — it must win via CompareAndSwap.
func TestNonce_ExpiredCASPath(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	cache := newNonceCacheForTest(10 * time.Millisecond)
	ttl := 50 * time.Millisecond

	firstSeen, err := cache.Add(t.Context(), validNonce, ttl)
	if err != nil || !firstSeen {
		t.Fatalf("first Add: firstSeen=%v err=%v", firstSeen, err)
	}

	// Advance past TTL without calling sweep — entry still in map but expired.
	advanceClockForTest(t, 100*time.Millisecond)

	// Second Add must hit the CAS path (entry present but expired).
	firstSeen2, err2 := cache.Add(t.Context(), validNonce, ttl)
	if err2 != nil {
		t.Fatalf("CAS path Add: %v", err2)
	}
	if !firstSeen2 {
		t.Error("expected firstSeen=true via CAS on expired entry")
	}
}

// TestNonce_ExpiredCASContention covers the CAS-failure branch: concurrent
// goroutines both see the same expired nonce; exactly one CAS wins.
// A start-gate channel is used to maximize simultaneous interleaving.
//
//nolint:gocognit // iteration + goroutine fan-out: inherent complexity for CAS-failure coverage
func TestNonce_ExpiredCASContention(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	const N = 512
	ttl := 50 * time.Millisecond

	// Run many iterations to reliably hit the CAS-failure branch.
	for range 10 {
		cache := newNonceCacheForTest(time.Hour)
		nonce := validNonce + "cas"

		_, _ = cache.Add(t.Context(), nonce, ttl)
		advanceClockForTest(t, 100*time.Millisecond)

		startGate := make(chan struct{})
		var wg sync.WaitGroup
		var firstSeenCount atomic.Int64

		wg.Add(N)
		for range N {
			go func() {
				defer wg.Done()
				<-startGate
				firstSeen, err := cache.Add(t.Context(), nonce, ttl)
				if firstSeen {
					firstSeenCount.Add(1)
				}
				if err != nil && !errors.Is(err, ErrNonceReplay) {
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
		// Release all goroutines simultaneously.
		close(startGate)
		wg.Wait()

		if c := firstSeenCount.Load(); c != 1 {
			t.Errorf("iteration: expected exactly 1 firstSeen=true, got %d", c)
		}
	}
}

// TestNonceCache_RunSweeperFires verifies the sweep path inside Run (ticker case).
// Clock is advanced before Run starts to avoid a race between nowFn writes and reads.
func TestNonceCache_RunSweeperFires(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	cache := newNonceCacheForTest(20 * time.Millisecond)
	ttl := 5 * time.Millisecond

	_, _ = cache.Add(t.Context(), validNonce, ttl)

	// Advance clock past TTL BEFORE starting Run to avoid a race on nowFn.
	advanceClockForTest(t, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		cache.Run(ctx)
		close(done)
	}()

	// Wait for at least one sweep tick (2× the sweep interval = 40ms).
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// After sweep via Run, re-Add should succeed as firstSeen=true.
	firstSeen, err := cache.Add(t.Context(), validNonce, ttl)
	if err != nil || !firstSeen {
		t.Errorf("after Run sweep: firstSeen=%v err=%v", firstSeen, err)
	}
}

func TestNewNonceCache_NoGoroutineSpawned(t *testing.T) {
	// No t.Parallel(): runtime.NumGoroutine() is process-global, so concurrent
	// sibling tests would pollute the before/after comparison below.
	before := runtime.NumGoroutine()
	_ = NewNonceCache()
	after := runtime.NumGoroutine()
	if after > before {
		t.Errorf("NewNonceCache spawned %d new goroutine(s)", after-before)
	}
}

func TestNonceCache_RunStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cache := newNonceCacheForTest(10 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		cache.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return within 500ms after context cancel")
	}
}

func TestNonceCache_RunLogsStoppedOnce(t *testing.T) {
	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(testutil.NewCapturingLogger(&buf, slog.LevelDebug))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	ctx, cancel := context.WithCancel(t.Context())
	cache := newNonceCacheForTest(10 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		cache.Run(ctx)
		close(done)
	}()
	cancel()
	<-done

	logged := buf.String()
	if !strings.Contains(logged, "hush/transport/sign: nonce cache sweep stopped") {
		t.Errorf("expected stopped log line, got: %s", logged)
	}
	if !strings.Contains(logged, "reason") {
		t.Errorf("expected 'reason' attribute, got: %s", logged)
	}
	if strings.Contains(logged, string([]byte(validNonce))) {
		t.Error("log line must not contain nonce content")
	}
	// Exactly one occurrence.
	count := strings.Count(logged, "nonce cache sweep stopped")
	if count != 1 {
		t.Errorf("expected exactly 1 stopped log line, got %d", count)
	}
}

func TestNonceCache_AddWorksWithoutRun(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	cache := NewNonceCache()
	// Add should work without Run ever being called.
	first, err := cache.Add(t.Context(), validNonce, validTTL)
	if err != nil || !first {
		t.Errorf("Add without Run: firstSeen=%v err=%v", first, err)
	}
	_, err2 := cache.Add(t.Context(), validNonce, validTTL)
	if !errors.Is(err2, ErrNonceReplay) {
		t.Errorf("duplicate without Run: expected ErrNonceReplay, got %v", err2)
	}
}

func TestNonceCache_SweepRemovesExpired(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	cache := newNonceCacheForTest(10 * time.Millisecond)
	ttl := 50 * time.Millisecond

	_, _ = cache.Add(t.Context(), validNonce, ttl)

	// Advance clock past TTL, then sweep.
	advanceClockForTest(t, 100*time.Millisecond)
	cache.sweep()

	// Entry should be gone; re-Add returns firstSeen=true.
	firstSeen, err := cache.Add(t.Context(), validNonce, ttl)
	if err != nil || !firstSeen {
		t.Errorf("after sweep: firstSeen=%v err=%v", firstSeen, err)
	}
}

// TestNonceCache_ConcurrentAdd is the load-bearing race test: N goroutines
// adding the same nonce; exactly one must observe firstSeen=true.
func TestNonceCache_ConcurrentAdd(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	const N = 128
	cache := NewNonceCache()

	var wg sync.WaitGroup
	var firstSeenCount atomic.Int64

	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			firstSeen, err := cache.Add(t.Context(), validNonce, validTTL)
			if firstSeen {
				firstSeenCount.Add(1)
			}
			if err != nil && !errors.Is(err, ErrNonceReplay) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if c := firstSeenCount.Load(); c != 1 {
		t.Errorf("expected exactly 1 firstSeen=true, got %d", c)
	}
}

// TestNonceCache_ConcurrentDistinct ensures N goroutines each adding a distinct
// nonce all observe firstSeen=true.
func TestNonceCache_ConcurrentDistinct(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setClockForTest(t, t0)

	const N = 64
	cache := NewNonceCache()

	nonces := make([]string, N)
	for i := range N {
		nonces[i] = validNonce + strings.Repeat("x", i+1)
	}

	var wg sync.WaitGroup
	var firstSeenCount atomic.Int64

	wg.Add(N)
	for i := range N {
		go func(n string) {
			defer wg.Done()
			firstSeen, err := cache.Add(t.Context(), n, validTTL)
			if err != nil {
				t.Errorf("distinct nonce Add: %v", err)
				return
			}
			if firstSeen {
				firstSeenCount.Add(1)
			}
		}(nonces[i])
	}
	wg.Wait()

	if c := firstSeenCount.Load(); c != N {
		t.Errorf("expected %d firstSeen=true (distinct nonces), got %d", N, c)
	}
}
