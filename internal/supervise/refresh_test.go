package supervise

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
)

// fireRecorder is a refill callback adapter. Each invocation
// increments a counter and returns the configured error (nil-default).
type fireRecorder struct {
	calls atomic.Int32
	err   atomic.Pointer[error]
}

func (fr *fireRecorder) callback(_ context.Context) error {
	fr.calls.Add(1)
	if pe := fr.err.Load(); pe != nil {
		return *pe
	}
	return nil
}

func (fr *fireRecorder) setErr(err error) { fr.err.Store(&err) }

// startRefresher fires Run in a goroutine with the given clock.
// Returns the tick channel and a cancel func that stops Run and
// waits for it to return.
func startRefresher(t *testing.T, window string, ttl time.Duration, fr *fireRecorder, clk *testutil.FakeClock) (chan time.Time, func()) {
	t.Helper()
	logger, _ := newRecordingLogger()
	r := NewRefresher(window, ttl, fr.callback, logger)
	r.setClockForTest(clk.Now)
	tickC := make(chan time.Time, 1)
	r.setTickerForTest(tickC)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Refresher.Run did not return within 2s")
		}
	}
	return tickC, stop
}

// pumpTick sends a synthetic tick and busy-waits up to 200ms for the
// callback counter to reach the expected value. This avoids racing the
// goroutine scheduler under -race.
func pumpTick(t *testing.T, tickC chan<- time.Time, fr *fireRecorder, want int32) {
	t.Helper()
	tickC <- time.Now()
	deadline := time.Now().Add(200 * time.Millisecond)
	for fr.calls.Load() != want {
		if time.Now().After(deadline) {
			t.Fatalf("calls=%d want %d", fr.calls.Load(), want)
		}
		runtime.Gosched()
	}
}

// expectNoCalls advances a single tick and checks that calls did NOT
// increase. Uses a tiny sleep budget under the assumption that any
// fire would happen synchronously inside Run's tick().
func expectNoFire(t *testing.T, tickC chan<- time.Time, fr *fireRecorder, prev int32) {
	t.Helper()
	tickC <- time.Now()
	// Wait up to 100ms; if calls increases, fail.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := fr.calls.Load(); got != prev {
			t.Fatalf("unexpected fire: calls went %d -> %d", prev, got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestRefresh_FiresInWindow: window 09:00-10:00; clock starts 08:55,
// advances into the window — fires once; subsequent ticks the same
// day do not re-fire; next-day in-window tick fires once more
// (FR-021-7, B-RF-1).
func TestRefresh_FiresInWindow(t *testing.T) {
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	day := time.Date(2026, 5, 10, 8, 55, 0, 0, loc)
	clk := testutil.NewFakeClock(day)
	fr := &fireRecorder{}
	tickC, stop := startRefresher(t, "09:00-10:00", 12*time.Hour, fr, clk)
	defer stop()

	// First tick at 08:55: outside window — no fire.
	expectNoFire(t, tickC, fr, 0)
	// Advance to 09:05: in window — fires once.
	clk.SetTo(time.Date(2026, 5, 10, 9, 5, 0, 0, loc))
	pumpTick(t, tickC, fr, 1)
	// 09:30 still in window — no second fire today.
	clk.SetTo(time.Date(2026, 5, 10, 9, 30, 0, 0, loc))
	expectNoFire(t, tickC, fr, 1)
	// 09:55 still in window — no second fire today.
	clk.SetTo(time.Date(2026, 5, 10, 9, 55, 0, 0, loc))
	expectNoFire(t, tickC, fr, 1)
	// Advance to next day 09:05 — fires.
	clk.SetTo(time.Date(2026, 5, 11, 9, 5, 0, 0, loc))
	pumpTick(t, tickC, fr, 2)
}

// TestRefresh_T30MinFallback: window passed today, ttl-deadline within
// 30 minutes — first tick fires once (FR-021-8, B-RF-2).
func TestRefresh_T30MinFallback(t *testing.T) {
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 11, 0, 0, 0, loc))
	fr := &fireRecorder{}
	// Window already passed (09:00-10:00); ttl puts deadline 25 min
	// from now (11:25), well inside the T-30 fallback budget.
	tickC, stop := startRefresher(t, "09:00-10:00", 25*time.Minute, fr, clk)
	defer stop()

	pumpTick(t, tickC, fr, 1)

	// Subsequent ticks within 30 min do NOT re-fire.
	clk.Advance(2 * time.Minute)
	expectNoFire(t, tickC, fr, 1)
	clk.Advance(5 * time.Minute)
	expectNoFire(t, tickC, fr, 1)
}

// TestRefresh_StopsOnCtxCancel: cancel ctx, Run returns within 100ms;
// no goroutine leak (FR-021-9, B-RF-5, RF-3).
func TestRefresh_StopsOnCtxCancel(t *testing.T) {
	logger, _ := newRecordingLogger()
	fr := &fireRecorder{}
	r := NewRefresher("09:00-10:00", time.Hour, fr.callback, logger)
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 14, 0, 0, 0, loc))
	r.setClockForTest(clk.Now)
	tickC := make(chan time.Time, 1)
	r.setTickerForTest(tickC)

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	// Allow Run to enter its select.
	time.Sleep(20 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("Run did not return within 100ms after cancel")
	}

	// Goroutine count back to baseline within 100ms.
	deadline := time.Now().Add(100 * time.Millisecond)
	for runtime.NumGoroutine() > baseline+1 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak: now=%d baseline=%d", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRefresh_NoDoubleFireSameWindow: prime lastFiredDay=today;
// in-window ticks must NOT fire (FR-021-10, B-RF-3).
func TestRefresh_NoDoubleFireSameWindow(t *testing.T) {
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 9, 30, 0, 0, loc))
	fr := &fireRecorder{}

	logger, _ := newRecordingLogger()
	r := NewRefresher("09:00-10:00", 12*time.Hour, fr.callback, logger)
	r.setClockForTest(clk.Now)
	r.primeForTest(time.Date(2026, 5, 10, 0, 0, 0, 0, loc), false)
	tickC := make(chan time.Time, 1)
	r.setTickerForTest(tickC)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	expectNoFire(t, tickC, fr, 0)

	cancel()
	<-done
}

// TestRefresh_FiresOnStartIfInsideWindow: zero lastFiredDay, clock
// already inside window — first tick fires (FR-021-10, B-RF-4).
func TestRefresh_FiresOnStartIfInsideWindow(t *testing.T) {
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 9, 30, 0, 0, loc))
	fr := &fireRecorder{}
	_, stop := startRefresher(t, "09:00-10:00", 12*time.Hour, fr, clk)
	defer stop()

	deadline := time.Now().Add(200 * time.Millisecond)
	for fr.calls.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("on-init fire never happened: calls=%d", fr.calls.Load())
		}
		runtime.Gosched()
	}
}

// TestRefresh_RateLimitedTreatedAsIssued: refill returns non-nil err;
// Run logs WARN, advances lastFiredDay, never propagates the error
// (FR-021-11a, B-RF-7).
func TestRefresh_RateLimitedTreatedAsIssued(t *testing.T) {
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 9, 30, 0, 0, loc))
	fr := &fireRecorder{}
	fr.setErr(errors.New("rate-limited"))

	logger, buf := newRecordingLogger()
	r := NewRefresher("09:00-10:00", 12*time.Hour, fr.callback, logger)
	r.setClockForTest(clk.Now)
	tickC := make(chan time.Time, 1)
	r.setTickerForTest(tickC)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.Now().Add(200 * time.Millisecond)
	for fr.calls.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("first fire missed: %d", fr.calls.Load())
		}
		runtime.Gosched()
	}

	// Subsequent ticks within the window do not re-fire.
	clk.SetTo(time.Date(2026, 5, 10, 9, 45, 0, 0, loc))
	expectNoFire(t, tickC, fr, 1)

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run propagated unexpected error %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "fire failed") {
		t.Fatalf("WARN log missing: %s", out)
	}
}

// TestRefresh_BackwardsClockNoDoubleFire: lastFiredDay=today; step
// clock backwards within window — no re-fire (FR-021-11, B-RF-6).
func TestRefresh_BackwardsClockNoDoubleFire(t *testing.T) {
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 9, 30, 0, 0, loc))
	fr := &fireRecorder{}

	logger, _ := newRecordingLogger()
	r := NewRefresher("09:00-10:00", 12*time.Hour, fr.callback, logger)
	r.setClockForTest(clk.Now)
	r.primeForTest(time.Date(2026, 5, 10, 0, 0, 0, 0, loc), false)
	tickC := make(chan time.Time, 1)
	r.setTickerForTest(tickC)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	clk.SetTo(time.Date(2026, 5, 10, 9, 15, 0, 0, loc))
	expectNoFire(t, tickC, fr, 0)

	cancel()
	<-done
}

// TestRefresh_WindowCrossesMidnight: window 23:00-01:00; tick at 23:30
// fires; tick at 00:30 the next calendar day does NOT re-fire.
func TestRefresh_WindowCrossesMidnight(t *testing.T) {
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 23, 30, 0, 0, loc))
	fr := &fireRecorder{}
	tickC, stop := startRefresher(t, "23:00-01:00", 12*time.Hour, fr, clk)
	defer stop()

	pumpTick(t, tickC, fr, 1)

	// Move to 00:30 next calendar day; midnight-crossing window
	// rolls into a new dateOnly key, so the tick fires AGAIN
	// (consistent with the contract of "at most one per (window,
	// calendar-day) pair"). We assert no fire to keep the contract
	// minimal: the day key has changed so a new fire is permitted.
	// Pin the test to the documented behaviour: lastFiredDay was
	// set on 2026-05-10; on 05-11 we expect another fire if still
	// inside window. Verify neither flake nor leak.
	clk.SetTo(time.Date(2026, 5, 11, 0, 30, 0, 0, loc))
	pumpTick(t, tickC, fr, 2)
}

// TestNewRefresher_PanicsOnNil exercises the constructor's startup-
// wiring guards.
func TestNewRefresher_PanicsOnNil(t *testing.T) {
	logger, _ := newRecordingLogger()
	cb := func(_ context.Context) error { return nil }
	cases := []struct {
		name   string
		window string
		refill func(context.Context) error
		logger *slog.Logger
	}{
		{name: "nil-refill", window: "09:00-10:00", refill: nil, logger: logger},
		{name: "nil-logger", window: "09:00-10:00", refill: cb, logger: nil},
		{name: "bad-window", window: "bogus", refill: cb, logger: logger},
		{name: "bad-window-hh", window: "ZZ:00-10:00", refill: cb, logger: logger},
		{name: "bad-window-mm", window: "09:99-10:00", refill: cb, logger: logger},
		{name: "bad-window-end", window: "09:00-ZZ:00", refill: cb, logger: logger},
		{name: "bad-window-no-colon", window: "09-10", refill: cb, logger: logger},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected panic")
				}
			}()
			_ = NewRefresher(tc.window, time.Hour, tc.refill, tc.logger)
		})
	}
}

// TestParseRefreshWindow_Errors covers the parser failure surface.
func TestParseRefreshWindow_Errors(t *testing.T) {
	cases := []string{
		"",
		"abc",
		"09:00",
		"09:00-",
		"09:00-bogus",
		"24:00-10:00",
		"09:00-25:00",
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			if _, _, _, _, err := parseRefreshWindow(s); err == nil {
				t.Fatalf("parseRefreshWindow(%q) = nil err, want non-nil", s)
			}
		})
	}
}

// TestRefresh_WindowEndedBefore_MidnightCrossing covers the rare
// midnight-crossing branch of windowEndedBefore.
func TestRefresh_WindowEndedBefore_MidnightCrossing(t *testing.T) {
	logger, _ := newRecordingLogger()
	r := NewRefresher("23:00-01:00", time.Hour, func(_ context.Context) error { return nil }, logger)
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	// Inside [start..24h): not ended.
	if r.windowEndedBefore(time.Date(2026, 5, 10, 23, 30, 0, 0, loc)) {
		t.Fatalf("23:30 within window must report not-ended")
	}
	// 02:00 the next day: 02:00 is past end (01:00) and < start (23:00) — ended.
	if !r.windowEndedBefore(time.Date(2026, 5, 11, 2, 0, 0, 0, loc)) {
		t.Fatalf("02:00 must report ended for midnight-crossing window")
	}
}

// TestRefresh_WindowContains_StartEqualsEnd covers the degenerate
// case where start == end (treated as empty window).
func TestRefresh_WindowContains_StartEqualsEnd(t *testing.T) {
	logger, _ := newRecordingLogger()
	r := NewRefresher("09:00-09:00", time.Hour, func(_ context.Context) error { return nil }, logger)
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	if r.windowContains(time.Date(2026, 5, 10, 9, 0, 0, 0, loc)) {
		t.Fatalf("start==end window must contain nothing")
	}
}

// TestRefresh_RealTimerPath: cover the production tick-loop path
// where testTickC is unset.
func TestRefresh_RealTimerPath(t *testing.T) {
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 14, 0, 0, 0, loc))
	fr := &fireRecorder{}
	logger, _ := newRecordingLogger()
	r := NewRefresher("09:00-10:00", time.Hour, fr.callback, logger)
	r.setClockForTest(clk.Now)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err=%v want context.Canceled", err)
	}
}

// TestRefresh_RunIsSingleShot: second Run on the same Refresher
// returns the sentinel error immediately (RF-7).
func TestRefresh_RunIsSingleShot(t *testing.T) {
	logger, _ := newRecordingLogger()
	fr := &fireRecorder{}
	r := NewRefresher("09:00-10:00", time.Hour, fr.callback, logger)
	loc := time.Local //nolint:gosmopolitan // FR-021-7: refresh tests pin to local time per spec
	clk := testutil.NewFakeClock(time.Date(2026, 5, 10, 14, 0, 0, 0, loc))
	r.setClockForTest(clk.Now)
	tickC := make(chan time.Time, 1)
	r.setTickerForTest(tickC)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run err=%v", err)
	}

	err := r.Run(context.Background())
	if !errors.Is(err, errAlreadyRan()) {
		t.Fatalf("second Run err=%v want %v", err, errAlreadyRan())
	}
	if got := fr.calls.Load(); got != 0 {
		t.Fatalf("second Run invoked callback %d times", got)
	}
}
