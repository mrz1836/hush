// Package alerts is the operator-facing Discord alert surface for
// hush. The 8 closed AlertClass values + 3 routing Tiers + per-class
// label prefixes + per-supervisor + per-pattern minimum-interval
// debounce form the entire seam between the supervisor lifecycle
// and Discord delivery.
//
// Routing (locked immutable):
//
//	TierCritical → Sender.SendOwnerDM(ctx, rendered) — single-shot, no retry.
//	TierWarning  → Sender.PostChannel(ctx, auditChannelID, rendered) — single-shot.
//	TierInfo     → no Sender call; the slog INFO record IS the delivery.
//
// Debounce (commit-on-success): two independent per-key
// minimum-interval token buckets — one keyed by SupervisorName, one
// keyed by Pattern (falling back to string(Class) when Pattern is
// empty). Buckets reserve on acquire, commit only on
// successful delivery, refund on rate-limit denial of the second
// bucket OR on transport failure.
//
// Scope (Constitution V/IX/X):
//
//   - V — every transport failure / unknown-class emits WARN slog.
//     ErrAlertRateLimited deliberately emits NO router-side record
//     (caller logs the suppression).
//   - IX — Route is synchronous end-to-end; zero goroutines spawned;
//     classToTier + classToTemplate are package-level immutable maps
//     initialized by literal expression; zero init().
//   - X — rendered body formats ONLY SupervisorName, MachineName,
//     Pattern, Detail. Alert.Time is never rendered. The package does
//     not import securebytes; no credential surface lives here.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// AlertClass enumerates the 8 operator-visible alert categories per
// docs/LIFECYCLE-SCENARIOS.md "Required alert classes". The set is
// CLOSED in v0.1.0; adding or removing a class is a
// chunk-level amendment.
type AlertClass string

// The 8 documented alert classes, kebab-case verbatim per
// docs/LIFECYCLE-SCENARIOS.md lines 301-314.
const (
	AlertClassApprovalRequest               AlertClass = "approval-request"
	AlertClassDaemonRefreshRequest          AlertClass = "daemon-refresh-request"
	AlertClassValidatorStaleFailure         AlertClass = "validator-stale-failure"
	AlertClassChildExit78StaleFailure       AlertClass = "child-exit-78-stale-failure"
	AlertClassLogPatternStaleWarning        AlertClass = "log-pattern-stale-warning"
	AlertClassDiscordDisconnected           AlertClass = "discord-disconnected"
	AlertClassDiscordReconnected            AlertClass = "discord-reconnected"
	AlertClassVaultUnreachableAtBootTimeout AlertClass = "vault-unreachable-at-boot-timeout"
)

// Tier is the routing destination class. Closed set; v0.1.0 expressly
// defines no fourth tier.
type Tier int

// The 3 documented tiers. Numeric values are stable across the
// v0.1.0 series: TierCritical=0, TierWarning=1, TierInfo=2.
const (
	TierCritical Tier = iota
	TierWarning
	TierInfo
)

// Alert describes one operator-visible event. Carries NO credential
// material — every field is operator-safe metadata.
//
// Tier is informational only. The Router re-derives the authoritative
// tier from Class via the immutable classToTier table on every Route
// call; the caller-supplied Tier value is IGNORED.
//
// Detail is operator-supplied metadata only — pattern names,
// identifiers, timestamps, scope names. NEVER credential values.
// Callers passing credential values into
// Detail are a caller bug; the per-class sentinel-byte tests catch
// the bug.
type Alert struct {
	Class          AlertClass
	Tier           Tier
	SupervisorName string
	MachineName    string
	Pattern        string
	Detail         string
	Time           time.Time
}

// Sender is the consumer-side Discord transport seam.
// *discord.BotApprover satisfies it via additive methods in the
// parent discord package. Tests substitute fakes.
//
// Implementations MUST be safe for concurrent use.
type Sender interface {
	// SendOwnerDM delivers a rendered message to the operator's
	// configured DM destination. Returns nil on success or a
	// transport error on failure.
	SendOwnerDM(ctx context.Context, message string) error

	// PostChannel posts a rendered message to the named channel.
	// Returns nil on success or a transport error on failure.
	PostChannel(ctx context.Context, channelID, message string) error
}

// Router is the single entry-point for alert dispatch. Holds the
// configured Sender, audit-channel ID, two rate-limit buckets, and
// a structured logger. Safe for concurrent use.
type Router struct {
	sender         Sender
	auditChannelID string
	supBucket      *ratebucket
	patBucket      *ratebucket
	logger         *slog.Logger
}

// DefaultBucketWindow is substituted when NewRouter receives a
// zero or negative bucket duration.
const DefaultBucketWindow = 1 * time.Minute

// Sentinel errors. Compare via errors.Is. ErrAlertTransport
// wraps the underlying Sender error; use errors.As to recover it.
var (
	// ErrAlertRateLimited is returned by Route when either the
	// per-supervisor or per-pattern debounce bucket rejects the
	// call. Route emits NO slog record on this path; the caller logs
	// the suppression.
	ErrAlertRateLimited = errors.New("hush/discord/alerts: rate limited")

	// ErrAlertTransport wraps the underlying Sender error when a
	// Critical- or Warning-tier delivery fails at the transport
	// layer. The router does NOT consume either debounce slot on
	// this failure (commit-on-success).
	ErrAlertTransport = errors.New("hush/discord/alerts: transport failed")

	// ErrUnknownAlertClass is returned by Route when it receives an
	// AlertClass outside the documented set of 8. Defensive only;
	// in-package callers MUST use the public
	// constants to avoid this path entirely.
	ErrUnknownAlertClass = errors.New("hush/discord/alerts: unknown class")
)

// classToTier locks the 8 documented bindings. Constructed at package
// declaration time; never mutated; no init(). Sentinel-class read-only
// global per Constitution IX exemption.
//
//nolint:gochecknoglobals // immutable class→tier binding table
var classToTier = map[AlertClass]Tier{
	AlertClassApprovalRequest:               TierCritical,
	AlertClassDaemonRefreshRequest:          TierCritical,
	AlertClassValidatorStaleFailure:         TierWarning,
	AlertClassChildExit78StaleFailure:       TierCritical,
	AlertClassLogPatternStaleWarning:        TierWarning,
	AlertClassDiscordDisconnected:           TierWarning,
	AlertClassDiscordReconnected:            TierInfo,
	AlertClassVaultUnreachableAtBootTimeout: TierCritical,
}

// outcome and slog-attribute key constants — drawn from the
// observability attribute allow-list.
const (
	outcomeDelivered       = "delivered"
	outcomeTransportFailed = "transport_failed"
	outcomeUnknownClass    = "unknown_class"

	attrClass      = "class"
	attrTier       = "tier"
	attrSupervisor = "supervisor"
	attrMachine    = "machine"
	attrPattern    = "pattern"
	attrOutcome    = "outcome"

	msgRouted = "alert routed"
)

// NewRouter constructs a Router. Panics on nil sender or nil logger
// (Constitution IX startup-wiring exception). Zero or negative bucket
// durations are silently substituted with DefaultBucketWindow
// (defensive default).
func NewRouter(sender Sender, auditChannelID string,
	perSupervisorBucket, perPatternBucket time.Duration,
	logger *slog.Logger,
) *Router {
	if sender == nil {
		panic("hush/discord/alerts: NewRouter: nil sender")
	}
	if logger == nil {
		panic("hush/discord/alerts: NewRouter: nil logger")
	}
	supWindow := perSupervisorBucket
	if supWindow <= 0 {
		supWindow = DefaultBucketWindow
	}
	patWindow := perPatternBucket
	if patWindow <= 0 {
		patWindow = DefaultBucketWindow
	}
	return &Router{
		sender:         sender,
		auditChannelID: auditChannelID,
		supBucket:      newRatebucket(supWindow, time.Now),
		patBucket:      newRatebucket(patWindow, time.Now),
		logger:         logger,
	}
}

// Route dispatches alert per the class→tier binding:
//
//   - TierCritical → Sender.SendOwnerDM(ctx, rendered) — single call, no retries.
//   - TierWarning  → Sender.PostChannel(ctx, auditChannelID, rendered) — single call, no retries.
//   - TierInfo     → no Sender call; the slog INFO record IS the delivery.
//
// Returns nil on success; ErrAlertRateLimited if either bucket
// rejects the call (Route emits NO slog record on this path);
// an error wrapping ErrAlertTransport on transport failure
// for Critical/Warning tiers (use errors.As to recover the underlying
// Sender error); or ErrUnknownAlertClass when Alert.Class is outside
// the documented set of 8 (defensive).
//
// Concurrency: safe for concurrent use by many goroutines.
// Per-call atomicity (acquire/commit/refund).
func (r *Router) Route(ctx context.Context, alert Alert) error {
	tier, ok := classToTier[alert.Class]
	if !ok {
		r.logger.LogAttrs(ctx, slog.LevelWarn, msgRouted,
			slog.String(attrClass, string(alert.Class)),
			slog.String(attrSupervisor, alert.SupervisorName),
			slog.String(attrOutcome, outcomeUnknownClass),
		)
		return ErrUnknownAlertClass
	}

	patternKey := alert.Pattern
	if patternKey == "" {
		patternKey = string(alert.Class)
	}

	if !r.supBucket.acquire(alert.SupervisorName) {
		return ErrAlertRateLimited
	}
	if !r.patBucket.acquire(patternKey) {
		r.supBucket.refund(alert.SupervisorName)
		return ErrAlertRateLimited
	}

	rendered := classToTemplate[alert.Class].render(alert)

	var sendErr error
	switch tier {
	case TierCritical:
		sendErr = r.sender.SendOwnerDM(ctx, rendered)
	case TierWarning:
		sendErr = r.sender.PostChannel(ctx, r.auditChannelID, rendered)
	case TierInfo:
		// No Sender call — slog INFO is the delivery.
	}

	if sendErr != nil {
		r.supBucket.refund(alert.SupervisorName)
		r.patBucket.refund(patternKey)
		r.emitOutcome(ctx, slog.LevelWarn, alert, tier, patternKey, outcomeTransportFailed)
		return fmt.Errorf("alerts: route %s: %w", alert.Class, errors.Join(ErrAlertTransport, sendErr))
	}

	r.supBucket.commit(alert.SupervisorName)
	r.patBucket.commit(patternKey)

	level := slog.LevelDebug
	if tier == TierInfo {
		level = slog.LevelInfo
	}
	r.emitOutcome(ctx, level, alert, tier, patternKey, outcomeDelivered)
	return nil
}

// emitOutcome is the single slog emission site for Route. The
// attribute set is strictly the observability allow-list. Detail / rendered
// / underlying errors are NEVER attached as attributes (per
// Constitution X observability allow-list discipline).
func (r *Router) emitOutcome(ctx context.Context, level slog.Level, alert Alert, tier Tier, patternKey, outcome string) {
	r.logger.LogAttrs(ctx, level, msgRouted,
		slog.String(attrClass, string(alert.Class)),
		slog.Int(attrTier, int(tier)),
		slog.String(attrSupervisor, alert.SupervisorName),
		slog.String(attrMachine, alert.MachineName),
		slog.String(attrPattern, patternKey),
		slog.String(attrOutcome, outcome),
	)
}

// supBucketWindow returns the configured per-supervisor bucket window.
// Used by tests; not part of the exported surface.
func (r *Router) supBucketWindow() time.Duration { return r.supBucket.window }

// patBucketWindow returns the configured per-pattern bucket window.
// Used by tests; not part of the exported surface.
func (r *Router) patBucketWindow() time.Duration { return r.patBucket.window }

// setClock substitutes the bucket time source. Used by tests for the
// fake-clock invariants; not exported.
func (r *Router) setClock(now func() time.Time) {
	r.supBucket.mu.Lock()
	r.supBucket.now = now
	r.supBucket.mu.Unlock()
	r.patBucket.mu.Lock()
	r.patBucket.now = now
	r.patBucket.mu.Unlock()
}
