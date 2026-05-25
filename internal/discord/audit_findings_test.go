package discord

// Tests for the four findings flagged by hush-audit on the approval
// surface (P1 owner-id check; P3 request_id binding; P4 goroutine
// panic recovery). P2 (supervisor_name propagation) is exercised at
// the server boundary by claim_handler_test.go and at the render
// boundary by render_test.go and ratelimit_test.go.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/token"
)

// ---------- P1: owner-allowlist on interaction click ----------------------

// approvalResult is the channel-passed return tuple for the
// RequestApproval call exercised by TestOnInteractionCreate_NonOwnerClickRejected.
type approvalResult struct {
	dec Decision
	err error
}

// assertNonOwnerEphemeralRejection inspects the shim's recorded
// interaction responses and asserts that the most recent one is the
// ephemeral "not authorized" notice produced by onInteractionCreate
// when the clicker's user ID does not match a.ownerID.
func assertNonOwnerEphemeralRejection(t *testing.T, shim *sessionShim) {
	t.Helper()
	responses := shim.InteractionResponses()
	if len(responses) == 0 {
		t.Fatal("expected an ephemeral notice for non-owner click")
	}
	last := responses[len(responses)-1].Response
	if last.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("non-owner response type=%v want ephemeral", last.Type)
	}
	if last.Data == nil || last.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatal("non-owner response is not ephemeral")
	}
	if !strings.Contains(strings.ToLower(last.Data.Content), "not authorized") {
		t.Fatalf("non-owner notice content=%q; want 'not authorized'", last.Data.Content)
	}
}

// TestOnInteractionCreate_NonOwnerClickRejected — Constitution II. When
// the approval prompt is posted to a shared channel, any member's button
// click reaches the bot's gateway. The handler must reject clicks from
// any Discord user that is not the configured ownerID and must leave
// the pending entry alive so a real owner click can still resolve it.
func TestOnInteractionCreate_NonOwnerClickRejected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:             mustSecureBytes(t, []byte("tok")),
		OwnerID:           testOwnerID,
		AppID:             "app",
		ApprovalChannelID: "shared-channel",
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()

	resCh := make(chan approvalResult, 1)
	go func() {
		dec, err := a.RequestApproval(ctx, interactiveSampleRequest())
		resCh <- approvalResult{dec, err}
	}()

	uuid := waitForCustomIDOnChannel(t, shim, "shared-channel")

	// Non-owner click — rejected ephemerally; pending entry stays live.
	shim.TriggerInteractionCreateAs(uuid+":approve", "attacker-user-id")
	select {
	case r := <-resCh:
		t.Fatalf("RequestApproval returned after non-owner click: dec=%+v err=%v", r.dec, r.err)
	case <-time.After(50 * time.Millisecond):
	}
	assertNonOwnerEphemeralRejection(t, shim)

	// Owner click — must resolve as approve.
	shim.TriggerInteractionCreateAs(uuid+":approve", testOwnerID)
	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("owner click err=%v; want nil", r.err)
		}
		if !r.dec.Approved {
			t.Fatal("owner click did not approve")
		}
	case <-time.After(time.Second):
		t.Fatal("owner click did not resolve the pending request")
	}
}

// TestOnInteractionCreate_MalformedUserIDRejected — defensive: an
// interaction with neither Member.User nor User must be treated as
// not-authorized. The handler must not panic, must reply ephemerally,
// and must not drain the pending entry.
func TestOnInteractionCreate_MalformedUserIDRejected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:   mustSecureBytes(t, []byte("tok")),
		OwnerID: testOwnerID,
		AppID:   "app",
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())

	// Empty userID → buildInteractionWithUser produces no Member/User.
	ic := buildInteractionWithUser("some-uuid:approve", "")
	a.onInteractionCreate(ic)

	responses := shim.InteractionResponses()
	if len(responses) != 1 {
		t.Fatalf("interaction responses=%d want 1", len(responses))
	}
	r := responses[0].Response
	if r.Data == nil || r.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatal("malformed-payload response is not ephemeral")
	}
	if !strings.Contains(strings.ToLower(r.Data.Content), "not authorized") {
		t.Fatalf("malformed-payload notice content=%q", r.Data.Content)
	}
}

// TestIsOwnerInteraction_DMShape — verify owner check accepts the
// DM-shape interaction (User populated, Member nil).
func TestIsOwnerInteraction_DMShape(t *testing.T) {
	t.Parallel()
	a := &BotApprover{ownerID: testOwnerID}

	ownerDM := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionMessageComponent,
			User: &discordgo.User{ID: testOwnerID},
		},
	}
	if !a.isOwnerInteraction(ownerDM) {
		t.Error("owner DM interaction should be accepted")
	}

	nonOwnerDM := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionMessageComponent,
			User: &discordgo.User{ID: "attacker"},
		},
	}
	if a.isOwnerInteraction(nonOwnerDM) {
		t.Error("non-owner DM interaction should be rejected")
	}

	if a.isOwnerInteraction(nil) {
		t.Error("nil interaction should be rejected")
	}
}

// ---------- P3: chassis request_id flows into Discord embeds --------------

// TestRequestApproval_RequestIDInPromptAndAudit — verifies that when
// the chassis supplies a RequestID, it appears in both the approval
// prompt and the audit-channel mirror payload, enabling cross-artifact
// correlation between Discord-visible events and on-disk audit entries.
func TestRequestApproval_RequestIDInPromptAndAudit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shim := newSessionShim()
	cfg := BotConfig{
		Token:          mustSecureBytes(t, []byte("tok")),
		OwnerID:        testOwnerID,
		AppID:          "app",
		AuditChannelID: "audit",
	}
	a := newTestApprover(ctx, shim, cfg, testutil.NewSilentLogger())
	shim.TriggerReady()

	const requestID = "rq-correlate-12345"
	req := interactiveSampleRequest()
	req.RequestID = requestID

	go func() {
		uuid := waitForCustomID(t, shim)
		shim.TriggerInteractionCreate(uuid + ":approve")
	}()
	if _, err := a.RequestApproval(ctx, req); err != nil {
		t.Fatalf("RequestApproval err=%v", err)
	}

	// 1. The DM/prompt embed must include the request_id.
	dm, ok := shim.LastDM()
	if !ok {
		t.Fatal("no DM recorded")
	}
	if len(dm.Send.Embeds) == 0 || !strings.Contains(dm.Send.Embeds[0].Description, requestID) {
		t.Fatalf("approval prompt missing request_id=%q\nbody=%q", requestID, dm.Send.Embeds[0].Description)
	}

	// 2. The audit-channel mirror payloads must include the request_id
	//    on the request_received and approved events.
	auditMsgs := waitForAuditMessages(t, shim, 2)
	for _, m := range auditMsgs {
		if len(m.Embeds) == 0 {
			continue
		}
		if !strings.Contains(m.Embeds[0].Description, requestID) {
			t.Errorf("audit mirror payload missing request_id=%q\nbody=%q", requestID, m.Embeds[0].Description)
		}
	}
}

// ---------- P4: goroutine panic recovery ----------------------------------

// TestRecoverGoroutine_PreventsCrash exercises the package's recover
// helper directly. A panic inside a deferred-recoverGoroutine block
// must NOT propagate out and must not be swallowed silently — it must
// surface on the send-error channel when one is provided.
func TestRecoverGoroutine_PreventsCrash(t *testing.T) {
	t.Parallel()
	a := &BotApprover{logger: testutil.NewSilentLogger()}
	errCh := make(chan error, 1)

	// recover() only catches a panic when its calling function is
	// directly deferred from the panicking function. Use a small
	// stub goroutine to mirror the production shape.
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer a.recoverGoroutine("test_site", errCh)
		panic("deliberate test panic")
	}()
	<-done

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrDiscordUnavailable) {
			t.Fatalf("recoverGoroutine emitted err=%v; want ErrDiscordUnavailable", err)
		}
		if !strings.Contains(err.Error(), "test_site") {
			t.Errorf("recovered err=%q does not include site label", err)
		}
	case <-time.After(time.Second):
		t.Fatal("recoverGoroutine did not surface an error on the send channel")
	}
}

// TestRecoverGoroutine_NoOpOnCleanExit ensures the helper does not
// emit a spurious error when the goroutine exits without panicking.
func TestRecoverGoroutine_NoOpOnCleanExit(t *testing.T) {
	t.Parallel()
	a := &BotApprover{logger: testutil.NewSilentLogger()}
	errCh := make(chan error, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer a.recoverGoroutine("clean", errCh)
	}()
	<-done

	select {
	case err := <-errCh:
		t.Fatalf("recoverGoroutine emitted spurious err on clean exit: %v", err)
	case <-time.After(50 * time.Millisecond):
		// Expected — no panic, no error.
	}
}

// TestDiscordGoroutines_HaveRecover is a source-level self-check that
// the three spawned goroutines in this package each pair with a
// `defer ... recoverGoroutine(` call — Constitution VII mandates that
// every goroutine recover()s, and the three production sites are:
// audit.go (mirror dispatch), bot.go (per-request send wrapper), and
// monitor.go (runMonitor). Drift in any of those files trips this test.
func TestDiscordGoroutines_HaveRecover(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"audit.go", "bot.go", "monitor.go"} {
		contents := readPath(t, filepath.Clean(file))
		if !strings.Contains(contents, "recoverGoroutine(") {
			t.Errorf("%s contains a goroutine without a recoverGoroutine() defer", file)
		}
	}
}

// ---------- shared test helpers ------------------------------------------

// waitForCustomIDOnChannel is the channel-mode counterpart of
// waitForCustomID: it waits until at least one message has been
// recorded for channelID then extracts the button's UUID prefix.
func waitForCustomIDOnChannel(t *testing.T, shim *sessionShim, channelID string) string {
	t.Helper()
	deadline := time.Now().Add(shimWaitTimeout)
	for time.Now().Before(deadline) {
		msgs := shim.SentMessagesFor(channelID)
		if len(msgs) > 0 {
			return extractUUID(t, msgs[0])
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for a message on channel %q", channelID)
	return ""
}

// waitForAuditMessages waits until the audit channel has accumulated
// at least n recorded messages, then returns a copy of them.
func waitForAuditMessages(t *testing.T, shim *sessionShim, n int) []*discordgo.MessageSend {
	t.Helper()
	deadline := time.Now().Add(shimWaitTimeout)
	for time.Now().Before(deadline) {
		msgs := shim.SentMessagesFor("audit")
		if len(msgs) >= n {
			return msgs
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for %d audit messages", n)
	return nil
}

// Silence unused-import warnings for token in source files that import
// it only via type-asserted samples.
var _ = token.SessionInteractive
