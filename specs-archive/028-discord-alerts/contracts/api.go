//go:build ignore

// Package alerts — typed mirror of the locked exported Go surface
// for SDD-28. This file is a REVIEW-ONLY artifact (not part of the
// production build). It captures the exact signatures, doc strings,
// and immutable package-level state the production code MUST mirror
// at internal/discord/alerts/.
//
// The data-model and research files (../data-model.md, ../research.md)
// are the structural source of truth; this file lets a reviewer
// compile-check the signatures in their head while skimming the plan.
//
// Production path target: internal/discord/alerts/alerts.go (+
// templates.go + ratelimit.go).
//
// Locked at: SDD-28 (chunk doc rows 30-37) + Clarifications Q1-Q5
// + research.md R-001..R-016.
package alerts

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// AlertClass + 8 constants — chunk-doc-locked names per
// docs/LIFECYCLE-SCENARIOS.md "Required alert classes" (lines 301-314).
// String values use kebab-case (R-002).
// ---------------------------------------------------------------------------

// AlertClass enumerates the 8 operator-visible alert categories.
// The set is CLOSED in v0.1.0 (spec FR-005); adding or removing a
// class is a chunk-level amendment.
type AlertClass string

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

// ---------------------------------------------------------------------------
// Tier + 3 constants — chunk-doc-locked routing-destination enum.
// Numeric values are stable: TierCritical=0, TierWarning=1, TierInfo=2.
// ---------------------------------------------------------------------------

// Tier is the routing destination class. Closed set; v0.1.0
// expressly defines no fourth tier (spec FR-002).
type Tier int

const (
	TierCritical Tier = iota
	TierWarning
	TierInfo
)

// ---------------------------------------------------------------------------
// Alert — chunk-doc-locked caller payload.
// ---------------------------------------------------------------------------

// Alert describes one operator-visible event. Carries NO credential
// material — every field is operator-safe metadata.
//
// Tier is informational only. The Router re-derives the authoritative
// tier from Class via the immutable classToTier table (R-010, FR-004).
// Callers MAY set Tier for downstream readers; the Router IGNORES the
// value it receives.
//
// Detail is operator-supplied metadata only — pattern names,
// identifiers, timestamps, scope names. NEVER credential values
// (Spec Assumption row 5). Callers passing credential values into
// Detail are a caller bug; the per-class sentinel-byte test
// (TestAlerts_NoSecretLeakInRendered_<Class>) catches the bug.
type Alert struct {
	Class          AlertClass
	Tier           Tier
	SupervisorName string
	MachineName    string
	Pattern        string
	Detail         string
	Time           time.Time
}

// ---------------------------------------------------------------------------
// Sender — consumer-side interface (R-005).
// ---------------------------------------------------------------------------

// Sender is the alerts package's consumer-defined Discord transport
// seam. *discord.BotApprover satisfies it via a small adapter
// written by downstream wiring (SDD-25 or a glue layer) — NOT by
// this chunk's own implementation. Tests substitute fakes.
//
// Implementations MUST be safe for concurrent use.
type Sender interface {
	// SendOwnerDM delivers a rendered message to the operator's
	// configured DM destination. ownerID is the Discord user
	// identifier supplied at NewRouter time. Returns nil on success
	// or a transport error on failure.
	SendOwnerDM(ctx context.Context, ownerID, message string) error

	// PostChannel posts a rendered message to the named channel.
	// Returns nil on success or a transport error on failure.
	PostChannel(ctx context.Context, channelID, message string) error
}

// ---------------------------------------------------------------------------
// Router — chunk-doc-locked opaque type.
// ---------------------------------------------------------------------------

// Router is the single entry-point for alert dispatch. Holds the
// configured Sender, owner-ID, audit-channel ID, two rate-limit
// buckets, and a structured logger. Safe for concurrent use (FR-026).
type Router struct {
	sender         Sender
	ownerID        string
	auditChannelID string
	supBucket      *ratebucket // unexported; see data-model.md §2.2
	patBucket      *ratebucket
	logger         *slog.Logger
}

// NewRouter constructs a Router. Returns an error wrapping
// ErrAlertConfig for any operator-input invariant violation
// (validation order, with the wrapped reason naming the offending
// parameter):
//
//  1. sender == nil               → fmt.Errorf("...: %w: nil sender", ErrAlertConfig)
//  2. ownerID == ""               → fmt.Errorf("...: %w: empty ownerID", ErrAlertConfig)
//  3. perSupervisorBucket <= 0    → fmt.Errorf("...: %w: non-positive perSupervisorBucket", ErrAlertConfig)
//  4. perPatternBucket <= 0       → fmt.Errorf("...: %w: non-positive perPatternBucket", ErrAlertConfig)
//  5. logger == nil               → fmt.Errorf("...: %w: nil logger", ErrAlertConfig)
//
// auditChannelID MAY be empty; a Warning-tier route then surfaces
// ErrAlertTransport (the Sender impl rejects the empty channelID).
//
// Returning (*Router, error) is a plan-time extension over the
// chunk-doc's bare *Router signature (R-011), required by
// Constitution IX panic policy — operator-input invariants must
// surface as typed errors, not panics.
func NewRouter(sender Sender, ownerID, auditChannelID string,
	perSupervisorBucket, perPatternBucket time.Duration,
	logger *slog.Logger,
) (*Router, error) {
	_ = sender
	_ = ownerID
	_ = auditChannelID
	_ = perSupervisorBucket
	_ = perPatternBucket
	_ = logger
	return nil, nil // stub — production implementation in alerts.go
}

// Route dispatches alert per the class→tier binding:
//
//   - TierCritical → Sender.SendOwnerDM(ctx, ownerID, rendered) — one call, no retries.
//   - TierWarning  → Sender.PostChannel(ctx, auditChannelID, rendered) — one call, no retries.
//   - TierInfo     → no Sender call; the slog INFO record IS the delivery.
//
// Returns nil on success, ErrAlertRateLimited if either bucket
// rejects the call (caller logs the suppression per FR-016 — Route
// emits NO slog record on this path), an error wrapping
// ErrAlertTransport (recoverable via errors.As to inspect the
// underlying Sender error) on transport failure for Critical/Warning
// tiers, or ErrAlertUnknownClass if alert.Class is outside the
// documented set of 8 (defensive — FR-009).
//
// Concurrency: safe for concurrent use by multiple goroutines.
// Per-key isolation per FR-014; per-call atomicity per R-009.
func (r *Router) Route(ctx context.Context, alert Alert) error {
	_ = ctx
	_ = alert
	return nil // stub — production implementation in alerts.go
}

// ---------------------------------------------------------------------------
// Sentinel errors — 3 Route-time + 1 construction-time = 4 total.
// ---------------------------------------------------------------------------

// ErrAlertRateLimited is returned by Route when either the
// per-supervisor or per-pattern debounce bucket rejects the call.
// The router emits NO slog record on this path; the caller logs the
// suppression per FR-016. Inspect via errors.Is.
//
// Locked at chunk-doc SDD-28 row 36.
var ErrAlertRateLimited = errors.New("hush/discord/alerts: rate limited")

// ErrAlertTransport wraps the underlying Sender error when a
// Critical- or Warning-tier delivery fails at the transport layer.
// The router does NOT consume either debounce slot on this failure
// (commit-on-success per FR-012a). Inspect via errors.Is or
// errors.As to recover the underlying Sender error.
//
// Added by Clarification Q3 — spec FR-012b.
var ErrAlertTransport = errors.New("hush/discord/alerts: transport failed")

// ErrAlertUnknownClass is returned by Route when it receives an
// AlertClass outside the documented set of 8. Defensive only
// (spec FR-009); in-package callers MUST use the public constants
// to avoid this path entirely. Compare via errors.Is.
var ErrAlertUnknownClass = errors.New("hush/discord/alerts: unknown class")

// ErrAlertConfig is the unified construction-time sentinel returned
// (wrapped via fmt.Errorf with a parameter-naming reason) by
// NewRouter for any operator-input invariant violation:
//   - sender == nil
//   - ownerID == ""
//   - perSupervisorBucket <= 0
//   - perPatternBucket <= 0
//   - logger == nil
//
// The wrapped reason names the offending parameter symbolically
// (e.g., "nil sender", "empty ownerID", "non-positive
// perSupervisorBucket"); no caller-supplied value is interpolated
// into the message. Compare via errors.Is.
var ErrAlertConfig = errors.New("hush/discord/alerts: invalid configuration")

// ---------------------------------------------------------------------------
// Unexported types declared here for the typed mirror — production
// counterparts live in ratelimit.go (the production package).
// ---------------------------------------------------------------------------

// ratebucket is the per-key minimum-interval debounce bucket.
// Production lives at internal/discord/alerts/ratelimit.go.
type ratebucket struct {
	mu      sync.Mutex
	window  time.Duration
	entries map[string]bucketState
	now     func() time.Time
}

// bucketState is one per-key entry. delivered is the last
// successful Route timestamp; pending is the in-flight reservation
// timestamp (zero if no reservation held).
type bucketState struct {
	delivered time.Time
	pending   time.Time
}
