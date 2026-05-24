package discord

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
)

//nolint:gocognit,gocyclo,cyclop // five-event lifecycle witness: complexity is inherent to the per-event audit-payload assertion list
func TestAuditChannel_AllFiveLifecycleEventsMirrored(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:          mustSecureBytes(t, []byte("tok")),
		OwnerID:        "owner",
		AppID:          "app",
		AuditChannelID: "audit-chan",
		DMRateLimit:    time.Microsecond,
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()

	// Approve cycle → request_received + approved
	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	if _, err := a.RequestApproval(ctx, interactiveSampleRequest()); err != nil {
		t.Fatalf("approve cycle: %v", err)
	}

	// Deny cycle → request_received + denied (different IP to skip rate-limit)
	denyReq := interactiveSampleRequest()
	denyReq.ClientIP = "100.96.0.42"
	prevDM := shim.DMCount()
	go func() {
		uuid := waitForNewDM(t, shim, prevDM)
		shim.TriggerInteractionCreate(uuid + ":deny")
	}()
	if _, err := a.RequestApproval(ctx, denyReq); !errors.Is(err, ErrApprovalDenied) {
		t.Fatalf("deny cycle: %v", err)
	}

	// Timeout cycle → request_received + timed_out
	timeoutReq := interactiveSampleRequest()
	timeoutReq.ClientIP = "100.96.0.43"
	tctx, tcancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer tcancel()
	if _, err := a.RequestApproval(tctx, timeoutReq); !errors.Is(err, ErrApprovalTimeout) {
		t.Fatalf("timeout cycle: %v", err)
	}

	// Rate-limited cycle → rate_limited only (no DM delivered)
	rlReq := interactiveSampleRequest()
	rlReq.ClientIP = "100.96.0.44"
	cfg2 := cfg
	cfg2.DMRateLimit = time.Hour
	a2 := newTestApprover(ctx, shim, cfg2, testutil.NewSilentLogger())
	shim.TriggerReady()
	primerPrev := shim.DMCount()
	go func() {
		uuid := waitForNewDM(t, shim, primerPrev)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	if _, err := a2.RequestApproval(ctx, rlReq); err != nil {
		t.Fatalf("primer call: %v", err)
	}
	if _, err := a2.RequestApproval(ctx, rlReq); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate-limit call: %v", err)
	}

	// Allow async audit dispatch goroutines to flush.
	time.Sleep(100 * time.Millisecond)

	auditMsgs := shim.SentMessagesFor("audit-chan")
	wantTypes := []string{
		"request_received",
		"approved",
		"denied",
		"timed_out",
		"rate_limited",
	}
	seen := make(map[string]int)
	for _, m := range auditMsgs {
		for _, e := range m.Embeds {
			for _, w := range wantTypes {
				if strings.Contains(e.Description, w+" ") || strings.Contains(e.Description, w+"\n") || strings.Contains(e.Description, "audit: "+w) {
					seen[w]++
				}
			}
		}
	}
	for _, w := range wantTypes {
		if seen[w] == 0 {
			t.Errorf("audit channel missing event type %q (got %v)", w, seen)
		}
	}
}

func TestAuditChannel_FailureDoesNotBlockApproval(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:          mustSecureBytes(t, []byte("tok")),
		OwnerID:        "owner",
		AppID:          "app",
		AuditChannelID: "audit-chan",
	}
	logBuf := &syncBuffer{}
	logger := testutil.NewCapturingLogger(logBuf, slog.LevelDebug)
	a := newTestApprover(ctx, shim, cfg, logger)
	shim.TriggerReady()

	// Audit-channel send fails; DM send succeeds.
	shim.SetSendErr("audit-chan", errShimSendFail)

	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	start := time.Now()
	dec, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if !dec.Approved {
		t.Fatal("expected Approved=true")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("approval took %v; audit-mirror should not block", time.Since(start))
	}
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(logBuf.String(), "audit-channel mirror failed") {
		t.Errorf("expected WARN log on audit-mirror failure; got: %s", logBuf.String())
	}
}

func TestAuditChannel_NoTokenInPayload(t *testing.T) {
	t.Parallel()
	sentinel := testutil.SentinelSecret(11)
	tokenSB := mustSecureBytes(t, []byte(sentinel))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:          tokenSB,
		OwnerID:        "owner",
		AppID:          "app",
		AuditChannelID: "audit-chan",
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()
	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	if _, err := a.RequestApproval(ctx, interactiveSampleRequest()); err != nil {
		t.Fatalf("err = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	for _, m := range shim.SentMessagesFor("audit-chan") {
		for _, e := range m.Embeds {
			testutil.AssertSentinelAbsent(t, sentinel, e.Description)
		}
	}
}

func TestAuditChannel_DisabledWhenIDEmpty(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
		// AuditChannelID empty
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()
	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	if _, err := a.RequestApproval(ctx, interactiveSampleRequest()); err != nil {
		t.Fatalf("err = %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if msgs := shim.SentMessagesFor("audit-chan"); len(msgs) != 0 {
		t.Errorf("expected zero messages on audit-chan, got %d", len(msgs))
	}
	// Also ensure there are no audit-style embeds anywhere.
	for _, m := range shim.AllSentMessages() {
		if m.ChannelID != "dm:owner" {
			t.Errorf("unexpected message on channel %q", m.ChannelID)
		}
	}
}
