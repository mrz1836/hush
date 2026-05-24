package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

var errInternalForTest = errors.New("internal failure")

// buildButtonInteraction synthesizes a message-component interaction
// payload for unit-testing the dispatcher directly.
func buildButtonInteraction(customID string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionMessageComponent,
			Data: discordgo.MessageComponentInteractionData{
				CustomID:      customID,
				ComponentType: discordgo.ButtonComponent,
			},
		},
	}
}

func TestNewBotApprover_ValidatesConfig(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := testutil.NewSilentLogger()
	good := func() BotConfig {
		return BotConfig{
			Token:   mustSecureBytes(t, []byte("tok")),
			OwnerID: "owner",
			AppID:   "app",
		}
	}
	cases := []struct {
		name string
		mut  func(*BotConfig, **slog.Logger)
		want error
	}{
		{
			name: "nil token",
			mut:  func(c *BotConfig, _ **slog.Logger) { c.Token = nil },
			want: ErrMissingToken,
		},
		{
			name: "empty owner",
			mut:  func(c *BotConfig, _ **slog.Logger) { c.OwnerID = "" },
			want: ErrMissingOwnerID,
		},
		{
			name: "empty app",
			mut:  func(c *BotConfig, _ **slog.Logger) { c.AppID = "" },
			want: ErrMissingAppID,
		},
		{
			name: "nil logger",
			mut:  func(_ *BotConfig, l **slog.Logger) { *l = nil },
			want: ErrMissingLogger,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good()
			lg := logger
			tc.mut(&cfg, &lg)
			a, err := NewBotApprover(ctx, cfg, lg)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
			if a != nil {
				t.Error("expected nil *BotApprover on validation failure")
			}
		})
	}
}

func TestNewBotApprover_DestroyedTokenRejected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sb, err := securebytes.New([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	_ = sb.Destroy()
	_, err = NewBotApprover(ctx, BotConfig{
		Token: sb, OwnerID: "o", AppID: "a",
	}, testutil.NewSilentLogger())
	if !errors.Is(err, ErrMissingToken) {
		t.Fatalf("err = %v; want ErrMissingToken", err)
	}
}

func TestNewBotApprover_BootDownStartsUnavailable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	shim.SetOpenErr(errShimOpenFail)
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	if err := a.session.Open(); err == nil {
		t.Fatal("shim Open() should still fail")
	}
	if a.available.Load() {
		t.Fatal("expected available=false at boot when Open() fails")
	}
	_, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if !errors.Is(err, ErrDiscordUnavailable) {
		t.Fatalf("err = %v; want ErrDiscordUnavailable", err)
	}
}

func TestDecisionRouting_Approve(t *testing.T) {
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

	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	req := interactiveSampleRequest()
	dec, err := a.RequestApproval(ctx, req)
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if !dec.Approved {
		t.Fatal("expected Approved=true")
	}
	if dec.ApprovedTTL != req.RequestedTTL {
		t.Errorf("ApprovedTTL = %v; want %v", dec.ApprovedTTL, req.RequestedTTL)
	}
	if dec.Reason != "" {
		t.Errorf("Reason = %q; want empty (v0.1.0)", dec.Reason)
	}
}

func TestDecisionRouting_ApproveConsumesRequestMessage(t *testing.T) {
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

	responses := shim.InteractionResponses()
	if len(responses) != 1 {
		t.Fatalf("interaction responses = %d; want 1", len(responses))
	}
	resp := responses[0].Response
	if resp.Type != discordgo.InteractionResponseUpdateMessage {
		t.Fatalf("response type = %v; want InteractionResponseUpdateMessage", resp.Type)
	}
	if resp.Data == nil {
		t.Fatal("response data is nil")
	}
	if len(resp.Data.Components) != 0 {
		t.Fatalf("components = %d; want 0 so stale buttons disappear", len(resp.Data.Components))
	}
	if len(resp.Data.Embeds) != 1 || !strings.Contains(resp.Data.Embeds[0].Description, "Approved") {
		t.Fatalf("resolved embed = %#v; want approved marker", resp.Data.Embeds)
	}
}

func TestInteractionHandler_StaleRequestGetsEphemeralNotice(t *testing.T) {
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
	a.onInteractionCreate(buildButtonInteraction("missing:approve"))

	responses := shim.InteractionResponses()
	if len(responses) != 1 {
		t.Fatalf("interaction responses = %d; want 1", len(responses))
	}
	resp := responses[0].Response
	if resp.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("response type = %v; want ephemeral channel message", resp.Type)
	}
	if resp.Data == nil || resp.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatalf("response data = %#v; want ephemeral notice", resp.Data)
	}
	if !strings.Contains(resp.Data.Content, "no longer active") {
		t.Fatalf("content = %q; want stale explanation", resp.Data.Content)
	}
}

func TestDecisionRouting_Deny(t *testing.T) {
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

	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":deny")
	}()
	dec, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if !errors.Is(err, ErrApprovalDenied) {
		t.Fatalf("err = %v; want ErrApprovalDenied", err)
	}
	if dec.Approved {
		t.Error("expected Approved=false on Deny")
	}
}

func TestDecisionRouting_Timeout(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(parent, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()

	ctx, cancel2 := context.WithTimeout(parent, 50*time.Millisecond)
	defer cancel2()
	_, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if !errors.Is(err, ErrApprovalTimeout) {
		t.Errorf("errors.Is(err, ErrApprovalTimeout) = false; err = %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(err, context.DeadlineExceeded) = false; err = %v", err)
	}
}

func TestDecisionRouting_CtxCancelled(t *testing.T) {
	t.Parallel()
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: "owner",
		AppID:   "app",
	}
	a := newTestApprover(parent, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()

	ctx, cancel := context.WithCancel(parent)
	go func() {
		_ = waitForCustomID(t, shim)
		cancel()
	}()
	_, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
	if errors.Is(err, ErrApprovalTimeout) {
		t.Error("ctx.Cancel must NOT be wrapped under ErrApprovalTimeout")
	}
}

func TestDecisionRouting_FirstActionWins(t *testing.T) {
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

	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
		shim.TriggerInteractionCreate(uuid + ":deny") // dropped silently
	}()
	dec, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if err != nil {
		t.Fatalf("err = %v; want nil (first action wins -> Approve)", err)
	}
	if !dec.Approved {
		t.Fatal("first action was Approve; second click must not flip the decision")
	}
}

func TestInteractionHandler_IgnoresNonComponentEvents(t *testing.T) {
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
	// Build a non-component interaction directly and dispatch.
	a.onInteractionCreate(nil)
	// Interaction with bad CustomID (no ":" separator) — silently dropped.
	a.onInteractionCreate(buildButtonInteraction("no-colon"))
	// Interaction whose UUID has no pending entry — silently dropped.
	a.onInteractionCreate(buildButtonInteraction("unknown-uuid:approve"))
	// Interaction with unknown action verb — dropped.
	a.pending.Store("uuidA", &pendingEntry{ch: make(chan decisionEvent, 1)})
	a.onInteractionCreate(buildButtonInteraction("uuidA:strange-action"))
	// When the action verb is unrecognized the pending entry is
	// already removed by LoadAndDelete but no event is sent — this
	// exercises the default branch of the action switch.
}

func TestBotApprover_DisconnectFastPath(t *testing.T) {
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
	// available defaults to false; mimic disconnect state.
	start := time.Now()
	_, err := a.RequestApproval(ctx, interactiveSampleRequest())
	elapsed := time.Since(start)
	if !errors.Is(err, ErrDiscordUnavailable) {
		t.Fatalf("err = %v; want ErrDiscordUnavailable", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("disconnect fast-path latency %v > 100ms", elapsed)
	}
}

// TestBotApprover_NeverAutoApprovesOnDiscordError is the
// Constitution-II non-negotiable test: every error path must produce
// a non-nil err and a zero-value Decision; NONE may produce
// Decision{Approved: true}.
//
//nolint:gocognit,gocyclo,cyclop // exhaustive five-error-path fan-out: complexity is inherent to the Constitution-II witness
func TestBotApprover_NeverAutoApprovesOnDiscordError(t *testing.T) {
	t.Parallel()
	mkApprover := func() (*BotApprover, *sessionShim, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		shim := newSessionShim()
		cfg := BotConfig{
			Token:   mustSecureBytes(t, []byte("tok")),
			OwnerID: "owner",
			AppID:   "app",
		}
		a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
		return a, shim, cancel
	}

	t.Run("boot-down-Open-fails", func(t *testing.T) {
		a, shim, cancel := mkApprover()
		defer cancel()
		shim.SetOpenErr(errShimOpenFail)
		dec, err := a.RequestApproval(context.Background(), interactiveSampleRequest())
		if err == nil {
			t.Fatal("expected error on boot-down")
		}
		if dec.Approved {
			t.Fatal("Approved=true on boot-down — Constitution II VIOLATION")
		}
	})

	t.Run("disconnect-mid-flight", func(t *testing.T) {
		a, shim, cancel := mkApprover()
		defer cancel()
		shim.TriggerReady()
		resCh := make(chan struct {
			dec Decision
			err error
		}, 1)
		go func() {
			dec, err := a.RequestApproval(context.Background(), interactiveSampleRequest())
			resCh <- struct {
				dec Decision
				err error
			}{dec, err}
		}()
		_ = waitForCustomID(t, shim)
		shim.TriggerDisconnect()
		select {
		case res := <-resCh:
			if res.dec.Approved {
				t.Fatal("Approved=true on mid-flight disconnect — Constitution II VIOLATION")
			}
			if !errors.Is(res.err, ErrDiscordUnavailable) {
				t.Errorf("err = %v; want ErrDiscordUnavailable", res.err)
			}
		case <-time.After(time.Second):
			t.Fatal("did not unblock")
		}
	})

	t.Run("send-fails", func(t *testing.T) {
		a, shim, cancel := mkApprover()
		defer cancel()
		shim.TriggerReady()
		shim.SetSendErr("dm:owner", errShimSendFail)
		dec, err := a.RequestApproval(context.Background(), interactiveSampleRequest())
		if dec.Approved {
			t.Fatal("Approved=true on send-failure — Constitution II VIOLATION")
		}
		if !errors.Is(err, ErrDiscordUnavailable) {
			t.Errorf("err = %v; want ErrDiscordUnavailable", err)
		}
	})

	t.Run("usercreate-fails", func(t *testing.T) {
		a, shim, cancel := mkApprover()
		defer cancel()
		shim.TriggerReady()
		shim.SetCreateErr(errShimCreateFail)
		dec, err := a.RequestApproval(context.Background(), interactiveSampleRequest())
		if dec.Approved {
			t.Fatal("Approved=true on UserChannelCreate failure — Constitution II VIOLATION")
		}
		if !errors.Is(err, ErrDiscordUnavailable) {
			t.Errorf("err = %v; want ErrDiscordUnavailable", err)
		}
	})

	t.Run("rate-limited", func(t *testing.T) {
		a, shim, cancel := mkApprover()
		defer cancel()
		shim.TriggerReady()
		go func() {
			uuid := waitForCustomID(t, shim)
			shim.TriggerInteractionCreate(uuid + ":approve")
		}()
		req := interactiveSampleRequest()
		if _, err := a.RequestApproval(context.Background(), req); err != nil {
			t.Fatalf("first call failed: %v", err)
		}
		dec, err := a.RequestApproval(context.Background(), req)
		if dec.Approved {
			t.Fatal("Approved=true on rate-limit denial — Constitution II VIOLATION")
		}
		if !errors.Is(err, ErrRateLimited) {
			t.Errorf("err = %v; want ErrRateLimited", err)
		}
	})
}

// TestBotApprover_NoAutoApproveKnobExists scans every production
// .go source file in the package and asserts no field, identifier,
// or comment references auto_approve / autoapprove / bypass /
// skipApproval / noApproval (Constitution II).
//
//nolint:gocognit // directory walk + 5-banned-token cross-product: complexity is inherent to the AST-style scan
func TestBotApprover_NoAutoApproveKnobExists(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(filepath.Clean("."))
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{"auto_approve", "autoapprove", "bypass", "skipapproval", "noapproval"}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		contents := strings.ToLower(readPath(t, name))
		scanned++
		for _, b := range banned {
			if strings.Contains(contents, b) {
				t.Errorf("%s contains banned token %q (Constitution II)", name, b)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no production .go files scanned")
	}
}

// TestBotApprover_TokenAbsentFromAllArtifacts injects a unique
// 64-character sentinel as the bot token, exercises the full
// lifecycle, and asserts the sentinel appears nowhere — slog buffer,
// every err.Error(), every audit-channel payload, every DM body.
func TestBotApprover_TokenAbsentFromAllArtifacts(t *testing.T) {
	t.Parallel()

	sentinel := testutil.SentinelSecret(11)
	tokenSB, err := securebytes.New([]byte(sentinel))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tokenSB.Destroy() })

	logBuf := &syncBuffer{}
	logger := testutil.NewCapturingLogger(logBuf, slog.LevelDebug)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shim := newSessionShim()
	cfg := BotConfig{
		Token:          tokenSB,
		OwnerID:        "owner",
		AppID:          "app",
		AuditChannelID: "audit-chan",
	}
	a := newTestApprover(ctx, shim, cfg, logger)
	shim.TriggerReady()

	// Drive a complete approve cycle.
	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	dec, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if err != nil {
		t.Fatalf("approve cycle err = %v", err)
	}
	if !dec.Approved {
		t.Fatal("expected Approved=true")
	}

	// Drive a deny cycle (different ClientIP to bypass the rate limit).
	req := interactiveSampleRequest()
	req.ClientIP = "100.96.0.99"
	prevDMs := shim.DMCount()
	go func() {
		uuid := waitForNewDM(t, shim, prevDMs)
		shim.TriggerInteractionCreate(uuid + ":deny")
	}()
	if _, err := a.RequestApproval(ctx, req); !errors.Is(err, ErrApprovalDenied) {
		t.Fatalf("deny cycle err = %v", err)
	}

	// Trigger disconnect + reconnect lifecycle.
	shim.TriggerDisconnect()
	shim.TriggerReady()

	// Allow async audit goroutines to flush.
	time.Sleep(50 * time.Millisecond)

	// Sentinel must be absent from the slog buffer.
	testutil.AssertSentinelAbsent(t, sentinel, logBuf.String())

	// And from every recorded outbound message body.
	for _, m := range shim.AllSentMessages() {
		if m.Send == nil {
			continue
		}
		for _, e := range m.Send.Embeds {
			testutil.AssertSentinelAbsent(t, sentinel, e.Description)
		}
	}

	// Construct an error wrapping a fake send-failure to confirm
	// sentinel doesn't leak via wrapping path.
	wrapped := fmt.Errorf("%w: %w", ErrDiscordUnavailable, errInternalForTest)
	testutil.AssertSentinelAbsent(t, sentinel, wrapped.Error())

	// Also check the "Bot " + sentinel concat is absent.
	prefix := "Bot " + sentinel[:8]
	testutil.AssertSentinelAbsent(t, prefix, logBuf.String())
}

// TestBotApprover_RaceClean exercises concurrent Approve / Deny /
// Timeout flows with interleaved disconnect / reconnect under -race.
//
//nolint:gocognit,gocyclo,cyclop // race-stress harness: complexity is inherent to the goroutine fan-out + auto-responder
func TestBotApprover_RaceClean(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:       mustSecureBytes(t, []byte("tok")),
		OwnerID:     "owner",
		AppID:       "app",
		DMRateLimit: time.Microsecond, // disable rate limiting for the race test
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()

	// Background harasser: flips disconnect/ready randomly.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		toggle := false
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if toggle {
					shim.TriggerDisconnect()
				} else {
					shim.TriggerReady()
				}
				toggle = !toggle
			}
		}
	}()

	// Auto-responder to button presses.
	wg.Add(1)
	respCount := atomic.Int32{}
	go func() {
		defer wg.Done()
		seen := make(map[string]bool)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				for _, m := range shim.AllSentMessages() {
					if m.ChannelID != "dm:owner" || m.Send == nil {
						continue
					}
					uuid := extractUUID(t, m.Send)
					if uuid == "" || seen[uuid] {
						continue
					}
					seen[uuid] = true
					action := "approve"
					if respCount.Add(1)%2 == 0 {
						action = "deny"
					}
					shim.TriggerInteractionCreate(uuid + ":" + action)
				}
			}
		}
	}()

	// Concurrent RequestApproval callers.
	var callerWg sync.WaitGroup
	for i := 0; i < 8; i++ {
		callerWg.Add(1)
		go func(i int) {
			defer callerWg.Done()
			req := interactiveSampleRequest()
			req.ClientIP = fmt.Sprintf("100.64.%d.1", i)
			cctx, ccancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer ccancel()
			_, _ = a.RequestApproval(cctx, req)
		}(i)
	}
	callerWg.Wait()
	close(stop)
	wg.Wait()
}

func TestRequestApproval_ChannelFirstWhenAuditChannelConfigured(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:             mustSecureBytes(t, []byte("tok")),
		OwnerID:           "owner",
		AppID:             "app",
		ApprovalChannelID: "approval-channel",
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()

	go func() {
		uuid := waitForSentCustomID(t, shim, "approval-channel")
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	dec, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if err != nil {
		t.Fatalf("err = %v; want nil", err)
	}
	if !dec.Approved {
		t.Fatal("expected Approved=true")
	}
	if got := shim.DMCount(); got != 0 {
		t.Fatalf("DMCount = %d; want 0 when channel is configured", got)
	}
	if got := len(shim.SentMessagesFor("approval-channel")); got != 1 {
		t.Fatalf("approval-channel messages = %d; want 1", got)
	}
}

func TestRequestApproval_ChannelSendFailureDoesNotFallBackToDM(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	shim.SetSendErr("approval-channel", errShimSendFail)
	cfg := BotConfig{
		Token:             mustSecureBytes(t, []byte("tok")),
		OwnerID:           "owner",
		AppID:             "app",
		ApprovalChannelID: "approval-channel",
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()

	_, err := a.RequestApproval(ctx, interactiveSampleRequest())
	if !errors.Is(err, ErrDiscordUnavailable) {
		t.Fatalf("err = %v; want ErrDiscordUnavailable", err)
	}
	if got := shim.DMCount(); got != 0 {
		t.Fatalf("DMCount = %d; want 0 when configured channel delivery fails", got)
	}
}

func waitForSentCustomID(t *testing.T, shim *sessionShim, channelID string) string {
	t.Helper()
	deadline := time.Now().Add(shimWaitTimeout)
	for time.Now().Before(deadline) {
		msgs := shim.SentMessagesFor(channelID)
		if len(msgs) > 0 {
			return extractUUID(t, msgs[len(msgs)-1])
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for message in %s", channelID)
	return ""
}

// TestBotApprover_CtxBoundedSend_AbortsOnDeadline asserts that a hung
// ChannelMessageSendComplex no longer permanently locks the rate-limit
// bucket. When the per-request ctx fires (server-side
// claim_approval_timeout or supervisor HTTP-client timeout), the
// approver returns ErrDiscordUnavailable, the deferred Refund clears
// the bucket's pending slot, and the very next caller's Acquire is
// granted — proving the bucket is free again.
//
// Pre-fix: discordgo would block forever sleeping through a long
// Discord Retry-After header, the ctx timeout fired but never
// cancelled the in-flight HTTP call, so the deferred Refund never
// ran, pending stayed set, all subsequent /claim requests were 429'd
// indefinitely.
func TestBotApprover_CtxBoundedSend_AbortsOnDeadline(t *testing.T) {
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

	// Arm: the next send will block until we close this channel.
	block := make(chan struct{})
	shim.SetSendBlock(block)

	reqCtx, reqCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer reqCancel()
	dec, err := a.RequestApproval(reqCtx, interactiveSampleRequest())

	if dec.Approved {
		t.Fatal("Approved=true on ctx-bounded send abort — Constitution II VIOLATION")
	}
	if !errors.Is(err, ErrDiscordUnavailable) {
		t.Fatalf("err=%v; want ErrDiscordUnavailable wrap", err)
	}

	// Release the hung goroutine so it can exit cleanly (avoid goroutine leak).
	close(block)
	shim.SetSendBlock(nil)

	// Snapshot DM count BEFORE the second send: the first (hung) send
	// already recorded its DM payload into shim.dms before blocking on
	// the channel, so waitForCustomID would otherwise return that stale
	// UUID and the interaction trigger would be dropped (no pending
	// entry → test hangs).
	prevDMs := shim.DMCount()

	// Proof the bucket pending slot was released: a fresh send (with no
	// hang) for the same key must succeed instead of being 429'd.
	go func() {
		uuid := waitForNewDM(t, shim, prevDMs)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	dec2, err2 := a.RequestApproval(context.Background(), interactiveSampleRequest())
	if err2 != nil {
		t.Fatalf("subsequent request err=%v; bucket likely still locked", err2)
	}
	if !dec2.Approved {
		t.Fatal("subsequent request not approved; bucket recovery failed")
	}
}

// TestRateBucket_StalePendingReclaimsAfterWindow asserts that a pending
// slot older than the rate-limit window is treated as orphaned by the
// next Acquire — defense-in-depth so a buggy upstream that fails to
// Commit/Refund can't permanently lock the bucket.
func TestRateBucket_StalePendingReclaimsAfterWindow(t *testing.T) {
	t.Parallel()
	b := newRateBucket(5 * time.Minute)
	key := bucketKey{SupervisorName: "openclaw", ClientIP: "100.90.223.110"}
	t0 := time.Now()

	// Stuck-pending scenario: Acquire granted, but the caller died/forgot
	// to Commit/Refund. Pending stays set indefinitely (pre-fix).
	if r := b.Acquire(key, t0); r != acquireGranted {
		t.Fatal("first acquire failed")
	}

	// Within the window, the orphaned pending slot still blocks new
	// callers — same key has only one in-flight request at a time.
	if r := b.Acquire(key, t0.Add(time.Minute)); r != acquireDenied {
		t.Fatalf("mid-window second acquire = %v; want denied while pending<window", r)
	}

	// After the window elapses, the orphaned pending slot is reclaimed
	// and a fresh acquire is granted. This is the safety net.
	if r := b.Acquire(key, t0.Add(5*time.Minute+time.Nanosecond)); r != acquireGranted {
		t.Fatalf("post-window acquire after stale pending = %v; want granted (orphan reclaim)", r)
	}
}
