package discord

import (
	"context"
	"time"

	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// DefaultDMRateLimit is the default value applied when
// BotConfig.DMRateLimit ≤ 0.
const DefaultDMRateLimit = 5 * time.Minute

// Approver gates every secret-claim path. The package's BotApprover
// is the production implementation; tests may substitute alternative
// implementations.
//
// RequestApproval blocks until the operator clicks Approve or Deny,
// the per-request ctx deadline elapses, the chat transport becomes
// unavailable, or the rate-limit gate denies the request. The
// returned Decision MUST be inspected only on a nil error; on any
// non-nil error the caller MUST treat the request as not approved
// (Constitution II).
//
// Implementations MUST be safe for concurrent use.
type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// ApprovalRequest is the input to every approval call. All fields are
// caller-supplied; the package renders them verbatim and never
// validates that, e.g., MachineName exists or ClientIP is reachable.
//
// SupervisorName MUST be non-empty when SessionType is
// token.SessionSupervisor and MUST be empty otherwise.
//
// RequestID is the chassis-assigned correlation identifier. The package
// embeds it verbatim into the approval prompt body and the audit-channel
// mirror payload so operators can cross-reference Discord-visible events
// with the on-disk hash-chained audit entry (Layer 6).
type ApprovalRequest struct {
	MachineName    string
	ClientIP       string
	Reason         string
	Scope          []string
	RequestedTTL   time.Duration
	SessionType    token.SessionType
	SupervisorName string
	RequestID      string
}

// Decision is returned by RequestApproval only on the
// operator-Approve path. v0.1.0: ApprovedTTL == request.RequestedTTL
// exactly, Reason == "" — the fields exist for forward-compatible UX
// (e.g., a future shorten-TTL modal).
type Decision struct {
	Approved    bool
	ApprovedTTL time.Duration
	Reason      string
}

// BotConfig parameterises NewBotApprover. Token, OwnerID, and AppID
// are required; ApprovalChannelID is optional (empty sends approval prompts
// to the owner DM); AuditChannelID is optional (empty disables audit-
// channel mirroring); DMRateLimit ≤ 0 falls back to
// DefaultDMRateLimit.
//
// Token is consumed by NewBotApprover: the constructor reads its
// bytes via Use(fn) once at session-init time. The discordgo SDK
// retains its own internal string copy thereafter — that residual
// risk is unavoidable with the SDK.
// Callers MAY (and SHOULD) call Token.Destroy() after NewBotApprover
// returns; the package keeps no further reference to the SecureBytes
// object.
type BotConfig struct {
	Token             *securebytes.SecureBytes
	OwnerID           string
	AppID             string
	ApprovalChannelID string
	AuditChannelID    string
	DMRateLimit       time.Duration
}
