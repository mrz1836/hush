package discord

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMonitor_DisconnectSurfacesUnavailable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(ctx, shim, cfg, newSilentLogger())
	shim.TriggerReady()
	if !a.available.Load() {
		t.Fatal("expected available=true after Ready")
	}
	shim.TriggerDisconnect()
	if a.available.Load() {
		t.Fatal("expected available=false after Disconnect")
	}
	_, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if !errors.Is(err, ErrDiscordUnavailable) {
		t.Fatalf("RequestApproval err = %v; want ErrDiscordUnavailable", err)
	}
}

func TestMonitor_DisconnectUnblocksInFlightRequest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(ctx, shim, cfg, newSilentLogger())
	shim.TriggerReady()

	type result struct {
		dec Decision
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		dec, err := a.RequestApproval(ctx, interactiveSampleRequest())
		resCh <- result{dec, err}
	}()

	// Wait for delivery.
	_ = waitForCustomID(t, shim)

	start := time.Now()
	shim.TriggerDisconnect()

	select {
	case res := <-resCh:
		elapsed := time.Since(start)
		if elapsed > 100*time.Millisecond {
			t.Errorf("unblock latency %v > 100ms", elapsed)
		}
		if !errors.Is(res.err, ErrDiscordUnavailable) {
			t.Errorf("err = %v; want ErrDiscordUnavailable", res.err)
		}
		if res.dec.Approved {
			t.Error("Decision.Approved must be false on transport-down path")
		}
	case <-time.After(time.Second):
		t.Fatal("RequestApproval did not unblock within 1s of disconnect")
	}
}

func TestMonitor_ReconnectRestoresAvailability(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(ctx, shim, cfg, newSilentLogger())
	shim.TriggerReady()
	shim.TriggerDisconnect()
	shim.TriggerReady()
	if !a.available.Load() {
		t.Fatal("expected available=true after second Ready")
	}
	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	dec, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if !dec.Approved {
		t.Fatal("expected Approved=true")
	}
}

func TestMonitor_ReconnectBackoffCappedAt60s(t *testing.T) {
	t.Parallel()
	base := time.Second
	maxDelay := 60 * time.Second
	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		60 * time.Second,
		60 * time.Second,
		60 * time.Second,
	}
	for i, w := range want {
		got := backoffDelay(uint32(i), base, maxDelay)
		if got != w {
			t.Errorf("backoffDelay(%d) = %v; want %v", i, got, w)
		}
		if got > maxDelay {
			t.Errorf("backoffDelay(%d) = %v exceeds 60s cap", i, got)
		}
	}
}

func TestMonitor_ResumedFlipsAvailable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(ctx, shim, cfg, newSilentLogger())
	if a.available.Load() {
		t.Fatal("available should default to false")
	}
	shim.TriggerResumed()
	if !a.available.Load() {
		t.Fatal("Resumed should flip available to true")
	}
}

func TestBackoffDelay_EdgeCases(t *testing.T) {
	t.Parallel()
	// Zero base + zero max — fall back to defaults (1s base, 60s cap).
	if got := backoffDelay(0, 0, 0); got != time.Second {
		t.Errorf("backoffDelay(0, 0, 0) = %v; want 1s", got)
	}
	// Already-at-cap on entry — must not multiply past max.
	if got := backoffDelay(10, time.Minute, 60*time.Second); got != 60*time.Second {
		t.Errorf("got %v; want 60s", got)
	}
	// Smaller cap clamps the result.
	if got := backoffDelay(5, time.Second, 5*time.Second); got != 5*time.Second {
		t.Errorf("got %v; want 5s", got)
	}
}

func TestMonitor_ReconnectLoopHandlesOpenFailures(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(ctx, shim, cfg, newSilentLogger())
	a.reconnectBaseDelay = 500 * time.Microsecond
	a.reconnectMaxDelay = time.Millisecond
	shim.SetOpenErr(errShimOpenFail)
	shim.TriggerReady()      // available -> true
	shim.TriggerDisconnect() // available -> false; monitor begins reconnect attempts

	// Let several Open() failures pile up to exercise the
	// failures-increment branch of runReconnectLoop.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if shim.OpenCalls() >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if shim.OpenCalls() < 2 {
		t.Errorf("expected ≥ 2 Open() retries; got %d", shim.OpenCalls())
	}

	// Heal the shim and let reconnect succeed.
	shim.SetOpenErr(nil)
	shim.TriggerReady()
	if !a.available.Load() {
		t.Fatal("expected available=true after recovery")
	}
}

func TestMonitor_GoroutineExitsOnCtxCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(ctx, shim, cfg, newSilentLogger())
	shim.TriggerReady()
	shim.TriggerDisconnect()

	cancel()
	select {
	case <-a.monitorDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("monitor goroutine did not exit within 500ms of ctx cancel")
	}
	if shim.CloseCalls() == 0 {
		t.Error("expected session.Close() to be invoked on monitor exit")
	}
}
