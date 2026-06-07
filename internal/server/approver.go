package server

import (
	"context"
	"net/netip"
	"time"
)

// Approver seeks the configured operator's decision on a fresh secret-session
// request. The chassis itself ships no concrete implementation; a
// Discord-backed BotApprover supplies one.
//
// Implementations MUST be safe for concurrent use — the chassis may invoke
// RequestApproval from multiple request goroutines.
//
// Constitution IX: single-method interface, declared at the consumer (the
// chassis is the consumer of approval; the Discord layer is the producer).
type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// ApprovalRequest is the parameter the chassis passes to an Approver. All
// fields are populated by the claim handler; the chassis itself does
// not invoke RequestApproval directly.
type ApprovalRequest struct {
	// RequestID is the chassis-assigned request identifier.
	RequestID string

	// MachineName is the hostname the requesting client supplied in the
	// /claim payload.
	MachineName string

	// ClientIP is the socket-level peer address of the request. Always set
	// from the connection, never from a header.
	ClientIP netip.Addr

	// Scope is the requested set of secret names (alphabetical).
	Scope []string

	// Reason is the human-readable reason from the /claim payload.
	Reason string

	// SessionType distinguishes interactive shell sessions from long-lived
	// supervisor sessions.
	SessionType SessionType

	// RequestedTTL is the duration the client asked for. The Approver may
	// grant a smaller TTL via Decision.GrantedTTL.
	RequestedTTL time.Duration

	// SupervisorName is the operator-assigned label of the supervisor
	// process making the claim. Required (non-empty) when SessionType
	// is SessionSupervisor; MUST be empty otherwise. The chassis enforces
	// this invariant at the shape-validation stage so Approver
	// implementations may rely on it.
	SupervisorName string

	// Agent-context fields, all caller-optional. Shown to the human
	// approver verbatim. These are operator-visible metadata for
	// anomaly detection — NOT authenticators. A compromised agent
	// could lie in any of these fields; trust the cryptographic
	// identity (client signature, MachineName, ClientIP) for
	// authorization decisions.
	AgentIdentity  string // e.g. "claude-code/1.2.3"
	AgentModel     string // e.g. "claude-opus-4-7"
	ToolName       string // e.g. "Bash"
	CommandPreview string // first 1024 chars of argv, server-side re-redacted
	RecentSummary  string // optional one-line activity summary

	// Metadata is an open extension surface. The chassis treats it as
	// opaque. Values MUST NOT contain secret material or request-body
	// bytes.
	Metadata map[string]string
}

// Decision is the Approver's response.
type Decision struct {
	// Approved is true if and only if the operator clicked Approve.
	Approved bool

	// ApprovedAt is the wall-clock time of approval. Zero when Approved is
	// false.
	ApprovedAt time.Time

	// DeniedAt is the wall-clock time of denial. Zero when Approved is
	// true.
	DeniedAt time.Time

	// GrantedTTL is the TTL the consumer should use when issuing the JWT.
	// May be < ApprovalRequest.RequestedTTL when the operator picked a
	// shorter button.
	GrantedTTL time.Duration

	// ApproverID is an opaque identifier for the approver — the Discord
	// user ID in production, "test" for fakes, etc.
	ApproverID string

	// Reason is an optional free-text reason the operator may have
	// attached to a denial. Empty on approval.
	Reason string
}

// SessionType distinguishes interactive shell sessions from long-lived
// supervisor sessions. Visible to Approver implementations so the rendered
// approval UI can distinguish daemons from interactive users.
type SessionType uint8

// Session-type constants. The zero value is intentionally invalid so that an
// uninitialised SessionType is detectable.
const (
	// SessionInteractive is a short-lived, single-secret session for an
	// interactive shell.
	SessionInteractive SessionType = iota + 1

	// SessionSupervisor is a long-lived, multi-secret session for a
	// supervisor process (systemd, launchd, etc.).
	SessionSupervisor
)

// Wire-format strings for SessionType. Centralized so the request-shape
// validator and the String() projection cannot drift.
const (
	sessionTypeInteractiveStr = "interactive"
	sessionTypeSupervisorStr  = "supervisor"
)

// String returns a human-readable form of the session type. Returns
// "unknown" for the zero value or any value outside the documented set.
func (s SessionType) String() string {
	switch s {
	case SessionInteractive:
		return sessionTypeInteractiveStr
	case SessionSupervisor:
		return sessionTypeSupervisorStr
	default:
		return "unknown"
	}
}

// AuditWriter is the consumer-side interface the chassis uses to emit
// security-relevant events. The audit layer supplies the concrete implementation.
type AuditWriter interface {
	Write(ctx context.Context, event AuditEvent) error
}

// AuditEvent is one record written to the audit log. Detail MUST NEVER
// carry a request body byte, a vault byte (cipher or plain), or a key
// byte. The chassis logs only categories, identifiers, and counters.
type AuditEvent struct {
	Type      AuditEventType    `json:"type"`
	At        time.Time         `json:"at"`
	RequestID string            `json:"request_id,omitempty"`
	ClientIP  netip.Addr        `json:"client_ip,omitzero"`
	Detail    map[string]string `json:"detail,omitempty"`
}

// AuditEventType is the discriminator for [AuditEvent].
type AuditEventType string

// Audit event types the chassis itself emits. The handlers and Discord
// layer emit additional types not listed here.
const (
	AuditServerStart          AuditEventType = "server_start"
	AuditServerStop           AuditEventType = "server_stop"
	AuditVaultReloaded        AuditEventType = "vault_reloaded"
	AuditFilePermCheckFailed  AuditEventType = "file_perm_check_failed"
	AuditAuthFailedNotAllowed AuditEventType = "auth_failed"
	AuditPanicCaptured        AuditEventType = "panic_captured"
	// AuditClockSkewOverride is emitted once when --allow-clock-skew
	// is set AND the clock-sync startup check would have failed. The
	// override is the operator's explicit decision to come up despite
	// known clock skew; hush never auto-sudos to fix the clock.
	AuditClockSkewOverride AuditEventType = "clock_skew_override"

	// AuditClockSyncCacheFallback is emitted when all live clock-sync
	// providers are unavailable and startup falls back to a recent cached
	// drift measurement.
	AuditClockSyncCacheFallback AuditEventType = "clock_sync_cache_fallback"
)
