package discord

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/mrz1836/hush/internal/testutil"
	tk "github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

func TestRateLimit_BlocksSecondPromptWithin5Min(t *testing.T) {
	t.Parallel()
	b := newRateBucket(5 * time.Minute)
	key := bucketKey{SupervisorName: "claude", ClientIP: "100.64.0.1"}
	t0 := time.Now()
	if r := b.Acquire(key, t0); r != acquireGranted {
		t.Fatalf("first acquire: got %v, want granted", r)
	}
	b.Commit(key)
	t1 := t0.Add(4 * time.Minute)
	if r := b.Acquire(key, t1); r != acquireDenied {
		t.Fatalf("second acquire within 5m: got %v, want denied", r)
	}
}

func TestRateLimit_AllowsAfterWindow(t *testing.T) {
	t.Parallel()
	b := newRateBucket(5 * time.Minute)
	key := bucketKey{SupervisorName: "claude", ClientIP: "100.64.0.1"}
	t0 := time.Now()
	if r := b.Acquire(key, t0); r != acquireGranted {
		t.Fatal("first acquire failed")
	}
	b.Commit(key)
	t1 := t0.Add(5*time.Minute + time.Nanosecond)
	if r := b.Acquire(key, t1); r != acquireGranted {
		t.Fatalf("post-window acquire: got %v, want granted", r)
	}
}

func TestRateLimit_AlreadyPendingDenies(t *testing.T) {
	t.Parallel()
	b := newRateBucket(time.Hour)
	key := bucketKey{ClientIP: "1.1.1.1"}
	now := time.Now()
	if r := b.Acquire(key, now); r != acquireGranted {
		t.Fatal("first acquire should be granted")
	}
	// Second acquire while first is still pending — denied even
	// though the window check would otherwise allow it.
	if r := b.Acquire(key, now); r != acquireDenied {
		t.Fatalf("got %v; want acquireDenied (pending slot held)", r)
	}
}

func TestRateLimit_CommitNoopWhenNoPending(t *testing.T) {
	t.Parallel()
	b := newRateBucket(time.Hour)
	key := bucketKey{ClientIP: "1.2.3.4"}
	// Commit without prior Acquire should be a no-op (no panic, no
	// state mutation).
	b.Commit(key)
	if r := b.Acquire(key, time.Now()); r != acquireGranted {
		t.Errorf("commit-without-acquire should not consume the bucket; got %v", r)
	}
}

func TestRateLimit_PerKeyIsolation(t *testing.T) {
	t.Parallel()
	b := newRateBucket(5 * time.Minute)
	now := time.Now()
	keyA := bucketKey{SupervisorName: "A", ClientIP: "1.1.1.1"}
	keyB := bucketKey{SupervisorName: "B", ClientIP: "1.1.1.1"}
	keyC := bucketKey{SupervisorName: "A", ClientIP: "2.2.2.2"}
	if r := b.Acquire(keyA, now); r != acquireGranted {
		t.Fatal("A failed")
	}
	b.Commit(keyA)
	if r := b.Acquire(keyB, now); r != acquireGranted {
		t.Fatalf("B should be granted; got %v", r)
	}
	b.Commit(keyB)
	if r := b.Acquire(keyC, now); r != acquireGranted {
		t.Fatalf("C should be granted; got %v", r)
	}
}

func TestRateLimit_InteractiveKeyedByClientIP(t *testing.T) {
	t.Parallel()
	req1 := ApprovalRequest{
		ClientIP:    "100.64.0.1",
		SessionType: tk.SessionInteractive,
	}
	k1 := makeKey(req1)
	if k1.SupervisorName != "" {
		t.Errorf("interactive key should have empty SupervisorName, got %q", k1.SupervisorName)
	}
	if k1.ClientIP != "100.64.0.1" {
		t.Errorf("interactive key.ClientIP = %q; want %q", k1.ClientIP, "100.64.0.1")
	}
	req2 := ApprovalRequest{
		ClientIP:       "100.64.0.1",
		SessionType:    tk.SessionSupervisor,
		SupervisorName: "claude",
	}
	k2 := makeKey(req2)
	if k1 == k2 {
		t.Error("interactive and supervisor keys for the same IP must differ")
	}
}

func TestRateLimit_TransportUnavailableDoesNotConsumeToken(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("not-a-real-token")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	// available defaults to false
	req := interactiveSampleRequest()
	if _, err := a.RequestApproval(ctx, req); !errors.Is(err, ErrDiscordUnavailable) {
		t.Fatalf("got %v; want ErrDiscordUnavailable", err)
	}
	// Now flip available and assert the same call delivers (token wasn't consumed).
	shim.TriggerReady()
	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	dec, err := a.RequestApproval(ctx, req)
	if err != nil {
		t.Fatalf("second call err = %v; want nil", err)
	}
	if !dec.Approved {
		t.Fatal("second call should be approved (rate-limit token wasn't consumed)")
	}
}

func TestRateLimit_DeliveryFailureRefundsToken(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()

	// Program first send to fail, then succeed.
	shim.SetSendOnceErr("dm:owner", errShimSendFail)

	req := interactiveSampleRequest()
	if _, err := a.RequestApproval(ctx, req); !errors.Is(err, ErrDiscordUnavailable) {
		t.Fatalf("first call err = %v; want ErrDiscordUnavailable", err)
	}

	// Second call within the rate-limit window must be delivered (refund worked).
	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	dec, err := a.RequestApproval(ctx, req)
	if err != nil {
		t.Fatalf("second call err = %v; want nil", err)
	}
	if !dec.Approved {
		t.Fatal("second call should be approved")
	}
}

func TestRateLimit_ZeroDMRateLimitUsesDefault(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	for _, w := range []time.Duration{0, -time.Second} {
		cfg := BotConfig{
			Token:       mustSecureBytes(t, []byte("tok")),
			OwnerID:     "owner",
			AppID:       "app",
			DMRateLimit: w,
		}
		a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
		if a.rateLimitWin != DefaultDMRateLimit {
			t.Errorf("DMRateLimit=%v: rateLimitWin=%v; want %v",
				w, a.rateLimitWin, DefaultDMRateLimit)
		}
	}
}

// TestRateLimit_ShouldEmitRateLimitAuditCoalescesWithinWindow asserts
// that the first denial in a window returns true (emit) and that
// subsequent denials within the same window return false. This is the
// guard that prevents an in-process supervisor retry loop from turning
// the protective rate limiter into a Discord audit-channel spam loop.
func TestRateLimit_ShouldEmitRateLimitAuditCoalescesWithinWindow(t *testing.T) {
	t.Parallel()
	b := newRateBucket(5 * time.Minute)
	key := bucketKey{SupervisorName: "openclaw", ClientIP: "100.90.223.110"}
	t0 := time.Now()
	if !b.ShouldEmitRateLimitAudit(key, t0) {
		t.Fatal("first call within an empty window should emit")
	}
	for i, dt := range []time.Duration{time.Second, 30 * time.Second, 2 * time.Minute, 4*time.Minute + 59*time.Second} {
		if b.ShouldEmitRateLimitAudit(key, t0.Add(dt)) {
			t.Errorf("call %d at +%s within window should be suppressed", i+1, dt)
		}
	}
}

func TestRateLimit_ShouldEmitRateLimitAuditAllowsAfterWindow(t *testing.T) {
	t.Parallel()
	b := newRateBucket(5 * time.Minute)
	key := bucketKey{SupervisorName: "openclaw", ClientIP: "100.90.223.110"}
	t0 := time.Now()
	if !b.ShouldEmitRateLimitAudit(key, t0) {
		t.Fatal("first call should emit")
	}
	if b.ShouldEmitRateLimitAudit(key, t0.Add(4*time.Minute)) {
		t.Fatal("mid-window call should be suppressed")
	}
	if !b.ShouldEmitRateLimitAudit(key, t0.Add(5*time.Minute+time.Nanosecond)) {
		t.Fatal("post-window call should emit again")
	}
}

func TestRateLimit_ShouldEmitRateLimitAuditPerKeyIsolation(t *testing.T) {
	t.Parallel()
	b := newRateBucket(5 * time.Minute)
	now := time.Now()
	keyA := bucketKey{SupervisorName: "openclaw", ClientIP: "1.1.1.1"}
	keyB := bucketKey{SupervisorName: "hermes", ClientIP: "1.1.1.1"}
	if !b.ShouldEmitRateLimitAudit(keyA, now) {
		t.Fatal("A first call should emit")
	}
	if !b.ShouldEmitRateLimitAudit(keyB, now) {
		t.Fatal("B first call should emit (different key)")
	}
	if b.ShouldEmitRateLimitAudit(keyA, now.Add(time.Second)) {
		t.Fatal("A second call should be suppressed (same key)")
	}
}

// TestRateLimit_UsesMonotonicClock asserts the bucket stores
// time.Time values (which carry the monotonic component) and uses
// Sub() — never a UnixNano cast that would strip monotonic.
func TestRateLimit_UsesMonotonicClock(t *testing.T) {
	t.Parallel()
	contents := readPath(t, filepath.Clean("ratelimit.go"))
	if strings.Contains(contents, ".UnixNano()") {
		t.Error("ratelimit.go contains UnixNano() — strips monotonic clock")
	}
	if !strings.Contains(contents, "time.Time") {
		t.Error("ratelimit.go does not reference time.Time")
	}
	if !strings.Contains(contents, ".Sub(") {
		t.Error("ratelimit.go does not use time.Time.Sub")
	}
}

// shimWaitTimeout caps how long test helpers wait for shim signals.
const shimWaitTimeout = time.Second

// waitForCustomID blocks until a DM is recorded by the shim, then
// returns the UUID prefix of the first button's CustomID.
func waitForCustomID(t *testing.T, shim *sessionShim) string {
	t.Helper()
	deadline := time.Now().Add(shimWaitTimeout)
	for time.Now().Before(deadline) {
		if rec, ok := shim.LastDM(); ok {
			return extractUUID(t, rec.Send)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timeout waiting for DM")
	return ""
}

// waitForNewDM blocks until DMCount() exceeds prev, then returns the
// UUID prefix of the most-recently-recorded DM.
func waitForNewDM(t *testing.T, shim *sessionShim, prev int) string {
	t.Helper()
	deadline := time.Now().Add(shimWaitTimeout)
	for time.Now().Before(deadline) {
		if shim.DMCount() > prev {
			rec, ok := shim.LastDM()
			if !ok {
				continue
			}
			return extractUUID(t, rec.Send)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for a new DM beyond %d", prev)
	return ""
}

//nolint:gocognit // nested type-assertion walker over discordgo.MessageSend → ActionsRow → Button
func extractUUID(t *testing.T, ms *discordgo.MessageSend) string {
	t.Helper()
	for _, comp := range ms.Components {
		row, ok := comp.(discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, inner := range row.Components {
			btn, ok := inner.(discordgo.Button)
			if !ok {
				continue
			}
			if idx := strings.Index(btn.CustomID, ":"); idx > 0 {
				return btn.CustomID[:idx]
			}
		}
	}
	t.Fatal("no button CustomID found in message")
	return ""
}

func mustSecureBytes(t *testing.T, b []byte) *securebytes.SecureBytes {
	t.Helper()
	sb, err := securebytes.New(b)
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	t.Cleanup(func() { _ = sb.Destroy() })
	return sb
}

func readPath(t *testing.T, path string) string {
	t.Helper()
	clean := filepath.Clean(path)
	b, err := os.ReadFile(clean)
	if err != nil {
		t.Fatalf("read %s: %v", clean, err)
	}
	return string(b)
}
