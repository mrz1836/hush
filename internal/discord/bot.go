package discord

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
)

// sessionAPI is the narrow seam over *discordgo.Session that bot.go
// actually invokes. *discordgo.Session satisfies it structurally; the
// session_shim_test.go fake implements the same surface so tests
// never open a real Discord connection.
type sessionAPI interface {
	Open() error
	Close() error
	UserChannelCreate(recipientID string, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
	InteractionRespond(interaction *discordgo.Interaction, resp *discordgo.InteractionResponse, options ...discordgo.RequestOption) error
	AddHandler(handler interface{}) func()
}

// decisionKind enumerates the three terminal events that can land on
// a per-request channel.
type decisionKind uint8

const (
	decisionApprove decisionKind = iota + 1
	decisionDeny
	decisionUnavailable
)

type decisionEvent struct {
	kind decisionKind
}

// pendingEntry holds the per-call channel plus a snapshot of the
// request used by the audit-mirror dispatcher on the terminal path.
type pendingEntry struct {
	ch  chan decisionEvent
	req ApprovalRequest
}

// BotApprover is the production Approver, backed by a *discordgo.Session.
// Construct via NewBotApprover.
//
// Lifecycle: the ctx passed to NewBotApprover owns the monitor
// goroutine and the underlying *discordgo.Session. When that ctx is
// cancelled, the monitor closes the session, drains all pending
// requests with ErrDiscordUnavailable, and exits.
//
// Concurrency: safe for concurrent RequestApproval calls from many
// goroutines.
type BotApprover struct {
	session      sessionAPI
	ownerID      string
	auditChan    string
	approvalChan string
	rateLimitWin time.Duration
	available    atomic.Bool
	pending      sync.Map // map[string]*pendingEntry
	bucket       *rateBucket
	logger       *slog.Logger

	monitorDone     chan struct{}
	reconnectSignal chan struct{}

	// reconnect knobs — overridable by the test shim via
	// newBotApproverWithSession to keep TestMonitor_ReconnectBackoffCappedAt60s
	// fast and deterministic.
	reconnectBaseDelay time.Duration
	reconnectMaxDelay  time.Duration
	now                func() time.Time
}

// Compile-time guard that *BotApprover satisfies the Approver
// interface.
var _ Approver = (*BotApprover)(nil)

// NewBotApprover constructs a Discord-backed Approver.
//
// Validation order:
//  1. cfg.Token == nil OR Token.Len() == 0 → ErrMissingToken
//  2. cfg.OwnerID == ""                    → ErrMissingOwnerID
//  3. cfg.AppID   == ""                    → ErrMissingAppID
//  4. logger      == nil                   → ErrMissingLogger
//
// Validation failures return the bare sentinel; no side effect occurs.
//
// On successful validation the constructor reads the bot token via
// cfg.Token.Use(fn) exactly once, hands the resulting *discordgo.Session
// to the monitor, registers gateway/event handlers, and calls
// session.Open(). Open() failure does NOT fail NewBotApprover:
// the approver enters the unavailable state and the monitor's
// reconnect loop drives recovery.
//
// Construction errors are reserved for cfg validation failures —
// transport-down at boot is not a construction error.
func NewBotApprover(ctx context.Context, cfg BotConfig, logger *slog.Logger) (*BotApprover, error) {
	if cfg.Token == nil || cfg.Token.Len() == 0 {
		return nil, ErrMissingToken
	}
	if cfg.OwnerID == "" {
		return nil, ErrMissingOwnerID
	}
	if cfg.AppID == "" {
		return nil, ErrMissingAppID
	}
	if logger == nil {
		return nil, ErrMissingLogger
	}

	var session *discordgo.Session
	var openErr error
	if useErr := cfg.Token.Use(func(b []byte) {
		s, err := discordgo.New("Bot " + string(b))
		if err != nil {
			// Anonymise: discordgo's error formatter is normally
			// token-safe, but this boundary does not depend on a
			// third-party redaction guarantee.
			openErr = errSessionInitFailed
			return
		}
		s.ShouldReconnectOnError = false
		session = s
	}); useErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscordUnavailable, useErr)
	}
	if openErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscordUnavailable, openErr)
	}

	a := newBotApproverWithSession(ctx, cfg, logger, session)

	// Best-effort initial Open(); failure is not fatal.
	if err := a.session.Open(); err != nil {
		a.logger.Warn("hush/discord: initial Open() failed; monitor will retry",
			slog.String("err_class", "discord_unavailable"))
	}
	return a, nil
}

// Connected reports whether the underlying Discord WebSocket gateway is
// currently in the available state. Used by the chassis's `/hz` handler
// to populate the `discord_connected` field.
//
// Returns false at boot, after a Disconnect handler fires, and during
// reconnect attempts; returns true after a Ready or Resumed handler
// fires.
func (a *BotApprover) Connected() bool { return a.available.Load() }

// newBotApproverWithSession is the package-private constructor used
// by tests to inject a session shim. It performs none of the
// production cfg validation (callers — including production
// NewBotApprover — have already validated).
func newBotApproverWithSession(ctx context.Context, cfg BotConfig, logger *slog.Logger, session sessionAPI) *BotApprover {
	window := cfg.DMRateLimit
	if window <= 0 {
		window = DefaultDMRateLimit
	}
	a := &BotApprover{
		session:            session,
		ownerID:            cfg.OwnerID,
		auditChan:          cfg.AuditChannelID,
		approvalChan:       cfg.ApprovalChannelID,
		rateLimitWin:       window,
		bucket:             newRateBucket(window),
		logger:             logger,
		monitorDone:        make(chan struct{}),
		reconnectSignal:    make(chan struct{}, 1),
		reconnectBaseDelay: time.Second,
		reconnectMaxDelay:  60 * time.Second,
		now:                time.Now,
	}
	a.available.Store(false)
	a.registerHandlers()
	go a.runMonitor(ctx)
	return a
}

// RequestApproval delivers an approval prompt to the configured
// approval channel when ApprovalChannelID is set, otherwise to the
// configured operator's DM channel, and blocks until one of:
//   - operator clicks Approve → Decision{Approved: true,
//     ApprovedTTL: req.RequestedTTL}, nil
//   - operator clicks Deny → Decision{}, ErrApprovalDenied
//   - ctx deadline elapses → Decision{}, error matching both
//     ErrApprovalTimeout and context.DeadlineExceeded
//   - ctx cancelled (not deadline) → Decision{}, ctx.Err()
//   - WebSocket disconnects mid-flight → Decision{}, ErrDiscordUnavailable
//
// Pre-conditions checked in order BEFORE any side effect:
//  1. available flag is false → return ErrDiscordUnavailable
//     immediately (rate-limit bucket is NOT consulted).
//  2. rate-limit bucket Acquire denies the request → return
//     ErrRateLimited.
//  3. DM rendering produces a *discordgo.MessageSend.
//  4. Deliver the message to ApprovalChannelID when configured, otherwise
//     session.UserChannelCreate(OwnerID) + ChannelMessageSendComplex(dmChan, msg).
//     Failure refunds the rate-limit bucket and returns ErrDiscordUnavailable.
//  5. Bucket Commit promotes the pending slot to delivered.
//  6. Audit-channel mirror (best-effort, non-blocking).
//  7. Block on the per-request decision channel.
//
// Concurrency: safe to call from many goroutines simultaneously; each
// call allocates its own request UUID and channel slot.
//
//nolint:gocognit,gocyclo,cyclop // sequential available-rate-deliver-wait state machine; complexity is inherent to the per-request flow contract
func (a *BotApprover) RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error) {
	if !a.available.Load() {
		return Decision{}, ErrDiscordUnavailable
	}

	key := makeKey(req)
	if a.bucket.Acquire(key, a.now()) != acquireGranted {
		a.logger.Warn("hush/discord: rate limit denied",
			slog.String("session_type", string(req.SessionType)))
		if a.bucket.ShouldEmitRateLimitAudit(key, a.now()) {
			a.mirrorAudit(ctx, auditRateLimited, req)
		}
		return Decision{}, ErrRateLimited
	}

	committed := false
	defer func() {
		if !committed {
			a.bucket.Refund(key)
		}
	}()

	customID, err := newRequestID()
	if err != nil {
		return Decision{}, fmt.Errorf("%w: %w", ErrDiscordUnavailable, err)
	}
	msg := renderApproval(req, customID)

	ch := make(chan decisionEvent, 1)
	entry := &pendingEntry{ch: ch, req: req}
	a.pending.Store(customID, entry)

	dest, err := a.approvalDestination()
	if err != nil {
		a.pending.Delete(customID)
		return Decision{}, err
	}
	// ChannelMessageSendComplex is a blocking REST call inside discordgo
	// that does NOT honor ctx. When Discord is silently rate-limiting our
	// bot (returning a long Retry-After) the call sleeps until the window
	// expires — visible to us as a multi-minute hang that would otherwise
	// hold the rate-limit bucket's pending slot the whole time and
	// 429-loop every concurrent caller. Wrap it in a ctx-bounded send so
	// the caller's deadline aborts the wait cleanly; the orphaned
	// goroutine is bounded by the underlying Discord backoff and exits
	// once the REST call returns.
	sendErr := make(chan error, 1)
	go func() {
		defer a.recoverGoroutine("send_wrapper", sendErr)
		_, sendCallErr := a.session.ChannelMessageSendComplex(dest, msg)
		sendErr <- sendCallErr
	}()
	select {
	case sErr := <-sendErr:
		if sErr != nil {
			a.pending.Delete(customID)
			a.logger.Warn("hush/discord: ChannelMessageSendComplex failed",
				slog.String("delivery", a.approvalDeliveryMode()),
				slog.String("err_class", "discord_unavailable"))
			return Decision{}, fmt.Errorf("%w: %w", ErrDiscordUnavailable, sErr)
		}
	case <-ctx.Done():
		a.pending.Delete(customID)
		a.logger.Warn("hush/discord: ChannelMessageSendComplex timed out",
			slog.String("delivery", a.approvalDeliveryMode()),
			slog.String("err_class", "discord_unavailable"),
			slog.String("ctx_err", ctx.Err().Error()))
		return Decision{}, fmt.Errorf("%w: %w", ErrDiscordUnavailable, ctx.Err())
	}

	a.bucket.Commit(key)
	committed = true
	a.mirrorAudit(ctx, auditRequestReceived, req)

	// Re-check available between insert and wait — handles the race
	// where Disconnect fired between the entry-time available check
	// and the pending insert. The monitor's drain may have already
	// emitted into ch; if not, we drain ourselves.
	if !a.available.Load() {
		if _, ok := a.pending.LoadAndDelete(customID); ok {
			return Decision{}, ErrDiscordUnavailable
		}
		// Already drained by the monitor — fall through to the
		// channel select; the buffered slot holds the event.
	}

	a.logger.Debug("hush/discord: approval request delivered",
		slog.String("request_id", customID),
		slog.String("delivery", a.approvalDeliveryMode()),
		slog.String("session_type", string(req.SessionType)))

	select {
	case ev := <-ch:
		switch ev.kind {
		case decisionApprove:
			a.mirrorAudit(ctx, auditApproved, req)
			return Decision{Approved: true, ApprovedTTL: req.RequestedTTL}, nil
		case decisionDeny:
			a.mirrorAudit(ctx, auditDenied, req)
			return Decision{}, ErrApprovalDenied
		case decisionUnavailable:
			return Decision{}, ErrDiscordUnavailable
		default:
			return Decision{}, ErrDiscordUnavailable
		}
	case <-ctx.Done():
		a.pending.Delete(customID)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// Audit dispatch must outlive the per-request ctx, which
			// is why we mirror against context.Background here. The
			// goroutine inside mirrorAudit is bounded by the
			// constructor's ctx, not this one.
			a.mirrorAudit(context.Background(), auditTimedOut, req) //nolint:contextcheck // see comment above
			return Decision{}, fmt.Errorf("%w: %w", ErrApprovalTimeout, context.DeadlineExceeded)
		}
		return Decision{}, ctx.Err()
	}
}

func (a *BotApprover) approvalDeliveryMode() string {
	if strings.TrimSpace(a.approvalChan) != "" {
		return "channel"
	}
	return "dm"
}

func (a *BotApprover) approvalDestination() (string, error) {
	if channelID := strings.TrimSpace(a.approvalChan); channelID != "" {
		return channelID, nil
	}
	dm, err := a.session.UserChannelCreate(a.ownerID)
	if err != nil {
		a.logger.Warn("hush/discord: UserChannelCreate failed",
			slog.String("err_class", "discord_unavailable"))
		return "", fmt.Errorf("%w: %w", ErrDiscordUnavailable, err)
	}
	if dm == nil {
		return "", ErrDiscordUnavailable
	}
	return dm.ID, nil
}

// registerHandlers wires the InteractionCreate / Connect / Disconnect /
// Ready / Resumed handlers onto the underlying session. The handler
// functions are method closures so the test shim's TriggerXxx helpers
// invoke them with a nil *discordgo.Session arg without dereference.
func (a *BotApprover) registerHandlers() {
	a.session.AddHandler(func(_ *discordgo.Session, i *discordgo.InteractionCreate) {
		a.onInteractionCreate(i)
	})
	a.session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Connect) {
		// Connect is socket-open only; Ready is the authoritative
		// gateway-functional signal.
	})
	a.session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		a.onDisconnect()
	})
	a.session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Ready) {
		a.onReady()
	})
	a.session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) {
		a.onResumed()
	})
}

func (a *BotApprover) onInteractionCreate(i *discordgo.InteractionCreate) {
	if i == nil || i.Type != discordgo.InteractionMessageComponent {
		return
	}
	data := i.MessageComponentData()
	uuid, action, ok := strings.Cut(data.CustomID, ":")
	if !ok {
		return
	}

	// Approver allow-list gate (Constitution II). The bot may post the
	// approval prompt to a shared channel via cfg.ApprovalChannelID; any
	// channel member is technically able to click the buttons. Discord's
	// gateway delivers the interaction with the clicker's user ID — verify
	// it matches the configured owner. Mismatch is treated like a stale
	// click: ephemeral notice, pending entry stays live so a real owner
	// click can still resolve the request.
	if !a.isOwnerInteraction(i) {
		a.logger.Warn("hush/discord: rejecting interaction from non-owner",
			slog.String("err_class", "approver_not_authorized"))
		a.respondInteractionNotice(i, "Not authorized. Only the configured operator can approve secret requests.")
		return
	}

	var ev decisionEvent
	var approved bool
	switch action {
	case "approve":
		ev.kind = decisionApprove
		approved = true
	case "deny":
		ev.kind = decisionDeny
	default:
		a.respondInteractionNotice(i, "Unknown approval action. No secret was released.")
		return
	}

	// First-action-wins: remove the entry BEFORE sending so a
	// concurrent second click finds nothing.
	raw, found := a.pending.LoadAndDelete(uuid)
	if !found {
		a.respondInteractionNotice(i, "This request is no longer active. It was already handled, expired, or the bot restarted; check the audit message for the final decision.")
		return
	}
	entry, _ := raw.(*pendingEntry)
	if entry == nil {
		a.respondInteractionNotice(i, "This request is no longer active. No secret was released by this click.")
		return
	}

	a.respondInteractionUpdate(i, renderResolvedApproval(entry.req, approved))
	select {
	case entry.ch <- ev:
	default:
	}
}

func (a *BotApprover) respondInteractionUpdate(i *discordgo.InteractionCreate, data *discordgo.InteractionResponseData) {
	if i == nil || i.Interaction == nil {
		return
	}
	if err := a.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: data,
	}); err != nil {
		a.logger.Warn("hush/discord: failed to update approval interaction",
			slog.String("err_class", "discord_unavailable"))
	}
}

func (a *BotApprover) respondInteractionNotice(i *discordgo.InteractionCreate, content string) {
	if i == nil || i.Interaction == nil {
		return
	}
	if err := a.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		a.logger.Warn("hush/discord: failed to acknowledge stale approval interaction",
			slog.String("err_class", "discord_unavailable"))
	}
}

// recoverGoroutine is the package's standard panic-recovery shim for
// spawned goroutines (Constitution VII — every goroutine recover()s).
// Catches a panic, WARN-logs it (no value bytes), and — when sendErr is
// non-nil — surfaces a wrapped error onto the caller's channel so the
// outer RequestApproval flow returns a discord_unavailable instead of
// hanging on a deadlocked receive.
func (a *BotApprover) recoverGoroutine(site string, sendErr chan<- error) {
	r := recover()
	if r == nil {
		return
	}
	a.logger.Warn("hush/discord: goroutine panic recovered",
		slog.String("site", site),
		slog.String("err_class", "panic_recovered"))
	if sendErr != nil {
		select {
		case sendErr <- fmt.Errorf("%w: panic in %s", ErrDiscordUnavailable, site):
		default:
		}
	}
}

// isOwnerInteraction reports whether the clicker of an interaction
// is the configured operator. discordgo populates Member.User for
// guild-channel interactions and User for DM interactions; either may
// be nil depending on context — both are checked. An absent user ID
// is treated as not-authorized (fail-closed, Constitution II).
func (a *BotApprover) isOwnerInteraction(i *discordgo.InteractionCreate) bool {
	if i == nil || i.Interaction == nil {
		return false
	}
	uid := interactionUserID(i.Interaction)
	return uid != "" && uid == a.ownerID
}

// interactionUserID extracts the clicker's Discord user ID from an
// Interaction. Returns "" when neither Member.User nor User carries an ID
// (e.g. malformed payload, future Discord schema drift).
func interactionUserID(i *discordgo.Interaction) string {
	if i.Member != nil && i.Member.User != nil && i.Member.User.ID != "" {
		return i.Member.User.ID
	}
	if i.User != nil && i.User.ID != "" {
		return i.User.ID
	}
	return ""
}

// newRequestID returns a 32-character hex-encoded random identifier
// used as the Discord component CustomID prefix.
func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
