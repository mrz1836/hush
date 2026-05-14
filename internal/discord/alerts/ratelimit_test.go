package alerts

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
)

func newBucketRouter(t *testing.T, sender Sender, supWindow, patWindow time.Duration) (*Router, *testutil.FakeClock) {
	t.Helper()
	r := NewRouter(sender, "audit-ch-id", supWindow, patWindow, slog.New(&recordingHandler{}))
	c := testutil.NewFakeClock(time.Unix(1_700_000_000, 0))
	r.setClock(c.Now)
	return r, c
}

// --- B-A-8 ------------------------------------------------------------

func TestRateLimit_PerSupervisorBlocksExcess(t *testing.T) {
	t.Parallel()
	sender := &recordingSender{}
	rh := &recordingHandler{}
	r := NewRouter(sender, "audit-ch-id", 1*time.Second, 1*time.Microsecond, slog.New(rh))
	c := testutil.NewFakeClock(time.Unix(1_700_000_000, 0))
	r.setClock(c.Now)

	first := Alert{Class: AlertClassApprovalRequest, SupervisorName: "sup-x", Pattern: "p1"}
	second := Alert{Class: AlertClassApprovalRequest, SupervisorName: "sup-x", Pattern: "p2"}

	if err := r.Route(context.Background(), first); err != nil {
		t.Fatalf("first: %v", err)
	}
	c.Advance(100 * time.Millisecond)

	recsBefore := len(rh.snapshot())
	err := r.Route(context.Background(), second)
	if !errors.Is(err, ErrAlertRateLimited) {
		t.Fatalf("second: want ErrAlertRateLimited, got %v", err)
	}
	if calls := sender.snapshot(); len(calls) != 1 {
		t.Errorf("expected 1 Sender call (first only); got %d", len(calls))
	}
	if recsAfter := len(rh.snapshot()); recsAfter != recsBefore {
		t.Errorf("rate-limited path emitted slog records: before %d after %d", recsBefore, recsAfter)
	}

	c.Advance(1100 * time.Millisecond)
	third := Alert{Class: AlertClassApprovalRequest, SupervisorName: "sup-x", Pattern: "p3"}
	if err := r.Route(context.Background(), third); err != nil {
		t.Fatalf("third: %v", err)
	}
	if calls := sender.snapshot(); len(calls) != 2 {
		t.Errorf("expected 2 Sender calls after advance; got %d", len(calls))
	}
}

// --- B-A-9 ------------------------------------------------------------

func TestRateLimit_PerPatternBlocksExcess(t *testing.T) {
	t.Parallel()
	sender := &recordingSender{}
	r, c := newBucketRouter(t, sender, 1*time.Microsecond, 1*time.Second)

	a := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "sup-a", Pattern: "401-unauthorized"}
	b := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "sup-b", Pattern: "401-unauthorized"}

	if err := r.Route(context.Background(), a); err != nil {
		t.Fatalf("first: %v", err)
	}
	c.Advance(50 * time.Millisecond)
	err := r.Route(context.Background(), b)
	if !errors.Is(err, ErrAlertRateLimited) {
		t.Fatalf("second (same pattern, different supervisor): want ErrAlertRateLimited, got %v", err)
	}

	c.Advance(1100 * time.Millisecond)
	if err := r.Route(context.Background(), b); err != nil {
		t.Fatalf("third (after advance): %v", err)
	}
}

// --- B-A-10 -----------------------------------------------------------

func TestRateLimit_PerKeyIsolation(t *testing.T) {
	t.Parallel()
	sender := &recordingSender{}
	r, _ := newBucketRouter(t, sender, 1*time.Hour, 1*time.Hour)

	a := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "sup-A", Pattern: "p1"}
	b := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "sup-B", Pattern: "p2"}
	if err := r.Route(context.Background(), a); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := r.Route(context.Background(), b); err != nil {
		t.Fatalf("b (different sup + different pat): %v", err)
	}

	// Same supervisor, different pattern (per-pattern bucket isolation).
	r2, _ := newBucketRouter(t, sender, 1*time.Hour, 1*time.Hour)
	c := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "S", Pattern: "p1"}
	d := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "S2", Pattern: "p2"}
	if err := r2.Route(context.Background(), c); err != nil {
		t.Fatalf("c: %v", err)
	}
	if err := r2.Route(context.Background(), d); err != nil {
		t.Fatalf("d: %v", err)
	}
}

// --- B-A-11 -----------------------------------------------------------

func TestRateLimit_EmptyPatternUsesClassFallback(t *testing.T) {
	t.Parallel()
	sender := &recordingSender{}
	r, _ := newBucketRouter(t, sender, 1*time.Microsecond, 1*time.Hour)

	a := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "s1"}
	b := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "s2"} // same class → same fallback key
	if err := r.Route(context.Background(), a); err != nil {
		t.Fatalf("a: %v", err)
	}
	err := r.Route(context.Background(), b)
	if !errors.Is(err, ErrAlertRateLimited) {
		t.Fatalf("same-class fallback: want ErrAlertRateLimited, got %v", err)
	}

	// Different classes with empty Pattern → distinct fallback keys → both succeed.
	r2, _ := newBucketRouter(t, sender, 1*time.Microsecond, 1*time.Hour)
	c := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "x"}
	d := Alert{Class: AlertClassDiscordDisconnected, SupervisorName: "y"}
	if err := r2.Route(context.Background(), c); err != nil {
		t.Fatalf("c: %v", err)
	}
	if err := r2.Route(context.Background(), d); err != nil {
		t.Fatalf("d (different class fallback): %v", err)
	}
}

// --- B-A-12 -----------------------------------------------------------

func TestRateLimit_AppliesToInfoTier(t *testing.T) {
	t.Parallel()
	// Use a non-fataling sender to avoid t.Fatal on the rate-limited path.
	sender := &recordingSender{}
	r, _ := newBucketRouter(t, sender, 1*time.Hour, 1*time.Hour)
	a := Alert{Class: AlertClassDiscordReconnected, SupervisorName: "sup-i"}
	if err := r.Route(context.Background(), a); err != nil {
		t.Fatalf("first Info: %v", err)
	}
	err := r.Route(context.Background(), a)
	if !errors.Is(err, ErrAlertRateLimited) {
		t.Fatalf("second Info: want ErrAlertRateLimited, got %v", err)
	}
}

// --- B-A-23 -----------------------------------------------------------

func TestRateLimit_MonotonicClock(t *testing.T) {
	t.Parallel()
	bucket := newRatebucket(1*time.Second, time.Now)
	if !bucket.acquire("k") {
		t.Fatalf("first acquire denied")
	}
	bucket.commit("k")
	// Verify second acquire blocked immediately (no clock advance).
	if bucket.acquire("k") {
		t.Fatalf("second acquire granted within window")
	}

	// Inject a fake clock advance > window.
	start := time.Unix(1_700_000_000, 0)
	c := testutil.NewFakeClock(start)
	bucket2 := newRatebucket(10*time.Millisecond, c.Now)
	if !bucket2.acquire("m") {
		t.Fatalf("bucket2 first acquire denied")
	}
	bucket2.commit("m")
	c.Advance(11 * time.Millisecond)
	if !bucket2.acquire("m") {
		t.Fatalf("bucket2 acquire after window+1ms denied — monotonic clock invariant broken")
	}
}
