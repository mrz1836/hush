// Package server: POST /claim handler.
//
// The /claim handler is the constitutional choke point of the vault server.
// Every secret request walks the same pipeline:
//
//	shape → canonical-JSON+verify → nonce → timestamp → ip allowlist
//	  → TTL cap (per session_type) → Approver.RequestApproval → token.Issue
//
// Every outcome — approved, denied, timed-out, transport-unavailable,
// rate-limited, unknown, OR any pre-approval failure — emits exactly ONE
// audit event and returns a documented (status, error) pair. There is NO
// reachable code path that issues a JWT without a successful
// `(Decision{Approved:true, GrantedTTL>0}, nil)` return from the configured
// Approver (Constitution II). The 503 path is structurally fail-closed.
//
// Error response bodies contain ONLY the static `error` code and the
// chassis-assigned `request_id`. The signature, nonce, ephemeral
// pubkey, supplied reason, machine name, and scope contents NEVER appear in
// any response body or operational log line (Constitution X). Audit events
// emit the scope (audit-vs-log asymmetry) but redact the same forbidden set.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/redact"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// AuditClaimOutcome is the [AuditEventType] emitted by the claim handler for
// every terminal outcome. Distinguished from chassis-emitted constants in
// [approver.go]; the secret/revoke handlers add `secret_fetch`, `revoke`, etc.
const AuditClaimOutcome AuditEventType = "claim_outcome"

// outcomeLabel is the audit-side label per Outcome.
type outcomeLabel string

const (
	outcomeApproved           outcomeLabel = "approved"
	outcomeStandingReissue    outcomeLabel = "standing-reissue"
	outcomeBadRequest         outcomeLabel = "bad-request"
	outcomeBadSignature       outcomeLabel = "bad-signature"
	outcomeNonceReplay        outcomeLabel = "nonce-replay"
	outcomeNonceCacheFull     outcomeLabel = "nonce-cache-full"
	outcomeStaleTimestamp     outcomeLabel = "stale-timestamp"
	outcomeIPNotAllowed       outcomeLabel = "ip-not-allowed"
	outcomeDenied             outcomeLabel = "denied"
	outcomeApprovalTimeout    outcomeLabel = "approval-timeout"
	outcomeRateLimited        outcomeLabel = "rate-limited"
	outcomeDiscordUnavailable outcomeLabel = "discord-unavailable"
	outcomeUnknown            outcomeLabel = "unknown-outcome"
)

// Shape-validation sentinel errors. Compared via errors.Is in tests; the
// handler maps every one to the same `400 bad_request` outcome
// without echoing the failing field, so the caller cannot distinguish them
// on the wire — but the error chain remains useful for triage logs.
var (
	errShapeTrailingData          = errors.New("server: claim: trailing data after JSON value")
	errShapeScopeEmpty            = errors.New("server: claim: scope is empty")
	errShapeScopeBadName          = errors.New("server: claim: scope element invalid")
	errShapeReasonLen             = errors.New("server: claim: reason length out of [1, 256]")
	errShapeTTLInvalid            = errors.New("server: claim: ttl invalid or non-positive")
	errShapeSessionTypeUnknown    = errors.New("server: claim: session_type not in {interactive, supervisor}")
	errShapeEphemeralPub          = errors.New("server: claim: ephemeral_pubkey not 33-byte hex")
	errShapeNonceInvalid          = errors.New("server: claim: nonce invalid")
	errShapeTimestampFormat       = errors.New("server: claim: timestamp not RFC3339Nano")
	errShapeSignatureEmpty        = errors.New("server: claim: signature empty")
	errShapeRequestIDInvalid      = errors.New("server: claim: request_id invalid")
	errShapeMachineNameInvalid    = errors.New("server: claim: machine_name invalid")
	errShapeFingerprintInvalid    = errors.New("server: claim: client_key_fingerprint invalid")
	errShapeBase64Invalid         = errors.New("server: claim: signature not a recognized base64 encoding")
	errShapeSupervisorNameMissing = errors.New("server: claim: supervisor session requires non-empty supervisor_name")
	errShapeSupervisorNameSet     = errors.New("server: claim: interactive session must not carry supervisor_name")
	errShapeSupervisorNameInvalid = errors.New("server: claim: supervisor_name invalid")
	errShapeAgentFieldTooLong     = errors.New("server: claim: agent context field exceeds maximum length")
	errShapeStandingLeaseInvalid  = errors.New("server: claim: standing_lease requires supervisor session and non-zero client_machine_index")
)

// Static error codes returned in response bodies. The set is exhaustive;
// future codes may be appended (never repurposed).
const (
	errCodeBadRequest         = "bad_request"
	errCodeBadSignature       = "bad_signature"
	errCodeNonceReplay        = "nonce_replay"
	errCodeNonceCacheFull     = "nonce_cache_full"
	errCodeStaleTimestamp     = "stale_timestamp"
	errCodeIPNotAllowed       = "ip_not_allowed"
	errCodeDenied             = "denied"
	errCodeApprovalTimeout    = "approval_timeout"
	errCodeRateLimited        = "rate_limited"
	errCodeDiscordUnavailable = "discord_unavailable"
	errCodeUnknownOutcome     = "unknown_outcome"
)

// Validation regexes for the claim request shape.
//
//nolint:gochecknoglobals // sentinel-class: lazily initialized compiled regex
var (
	scopeNameOnce sync.Once
	scopeNameRe   *regexp.Regexp

	requestIDOnce sync.Once
	requestIDRe   *regexp.Regexp

	machineNameOnce sync.Once
	machineNameRe   *regexp.Regexp

	fingerprintOnce sync.Once
	fingerprintRe   *regexp.Regexp

	supervisorNameOnce sync.Once
	supervisorNameRe   *regexp.Regexp

	ephemeralPubKeyOnce sync.Once
	ephemeralPubKeyRe   *regexp.Regexp

	nonceCharsOnce sync.Once
	nonceCharsRe   *regexp.Regexp
)

func getScopeNameRe() *regexp.Regexp {
	scopeNameOnce.Do(func() { scopeNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`) })
	return scopeNameRe
}

func getRequestIDRe() *regexp.Regexp {
	requestIDOnce.Do(func() { requestIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`) })
	return requestIDRe
}

func getMachineNameRe() *regexp.Regexp {
	machineNameOnce.Do(func() { machineNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`) })
	return machineNameRe
}

func getFingerprintRe() *regexp.Regexp {
	fingerprintOnce.Do(func() { fingerprintRe = regexp.MustCompile(`^[0-9a-f]{16}$`) })
	return fingerprintRe
}

func getSupervisorNameRe() *regexp.Regexp {
	// Same character class as machine_name; supervisor labels are
	// operator-assigned text identifiers (e.g. "claude-worker-1").
	supervisorNameOnce.Do(func() { supervisorNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`) })
	return supervisorNameRe
}

func getEphemeralPubKeyRe() *regexp.Regexp {
	ephemeralPubKeyOnce.Do(func() { ephemeralPubKeyRe = regexp.MustCompile(`^[0-9a-fA-F]{66}$`) })
	return ephemeralPubKeyRe
}

func getNonceCharsRe() *regexp.Regexp {
	// base64url alphabet — A-Z a-z 0-9 - _ , length asserted separately
	nonceCharsOnce.Do(func() { nonceCharsRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`) })
	return nonceCharsRe
}

// claimRequest is the JSON-decoded request body. All fields are required
// except SupervisorName (required iff SessionType=="supervisor") and
// the five agent-context fields, which are optional and shown to the
// human approver verbatim. Length caps are enforced by parseClaimRequest;
// CommandPreview is re-redacted server-side as belt-and-braces.
type claimRequest struct {
	Scope                []string `json:"scope"`
	Reason               string   `json:"reason"`
	TTL                  string   `json:"ttl"`
	SessionType          string   `json:"session_type"`
	EphemeralPubKey      string   `json:"ephemeral_pubkey"`
	Nonce                string   `json:"nonce"`
	Timestamp            string   `json:"timestamp"`
	Signature            string   `json:"signature"`
	RequestID            string   `json:"request_id"`
	MachineName          string   `json:"machine_name"`
	SupervisorName       string   `json:"supervisor_name,omitempty"`
	ClientKeyFingerprint string   `json:"client_key_fingerprint"`
	ForceApproval        bool     `json:"force_approval,omitempty"`

	// StandingLease + ClientMachineIndex opt a supervisor claim into the
	// machine-bound standing lease. When true, a later claim from the same
	// machine (matching client_machine_index) may reissue a fresh full-window
	// session against the grant a human already established — no recurring
	// human approval. Both are absent (zero) on ordinary claims. The first
	// grant on a machine still walks the human approver (Constitution II).
	StandingLease      bool   `json:"standing_lease,omitempty"`
	ClientMachineIndex uint32 `json:"client_machine_index,omitempty"`

	// Optional agent-context fields. Visible to the Discord approver
	// and recorded in the audit log. Empty values are omitted from
	// canonical-JSON so old clients (no agent context) remain
	// signature-compatible with the new server.
	AgentIdentity  string `json:"agent_identity,omitempty"`
	AgentModel     string `json:"agent_model,omitempty"`
	ToolName       string `json:"tool_name,omitempty"`
	CommandPreview string `json:"command_preview,omitempty"`
	RecentSummary  string `json:"recent_summary,omitempty"`
}

// Maximum lengths for the optional agent-context fields. Enforced by
// parseClaimRequest so a malicious or buggy client cannot bloat the
// Discord prompt or the audit log.
const (
	maxAgentIdentityLen  = 128
	maxAgentModelLen     = 64
	maxToolNameLen       = 64
	maxCommandPreviewLen = 1024
	maxRecentSummaryLen  = 256
)

// claimResponse is the success-path response body. Encoded with explicit field
// order via the json tags — exactly three keys, no others.
type claimResponse struct {
	JWT       string `json:"jwt"`
	ExpiresAt string `json:"expires_at"`
	JTI       string `json:"jti"`
}

// errorResponse is the failure-path response body. Always exactly two keys.
type errorResponse struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id"`
}

// signedPayload is the canonicalised struct over which the client computes
// (and the server verifies) the signature. The field set matches the spec's
// "every field above plus request_id and machine_name" — i.e. excludes the
// signature itself and the client_key_fingerprint.
//
// CanonicalJSON sorts struct fields alphabetically by JSON tag name; the
// result is byte-identical between client and server. Agent-context
// fields are tagged omitempty so a client that supplies none of them
// produces the same canonical bytes as a pre-PR-4 client — preserving
// signature compatibility across the upgrade.
type signedPayload struct {
	AgentIdentity      string   `json:"agent_identity,omitempty"`
	AgentModel         string   `json:"agent_model,omitempty"`
	ClientMachineIndex uint32   `json:"client_machine_index,omitempty"`
	CommandPreview     string   `json:"command_preview,omitempty"`
	EphemeralPubKey    string   `json:"ephemeral_pubkey"`
	ForceApproval      bool     `json:"force_approval,omitempty"`
	MachineName        string   `json:"machine_name"`
	Nonce              string   `json:"nonce"`
	Reason             string   `json:"reason"`
	RecentSummary      string   `json:"recent_summary,omitempty"`
	RequestID          string   `json:"request_id"`
	Scope              []string `json:"scope"`
	SessionType        string   `json:"session_type"`
	StandingLease      bool     `json:"standing_lease,omitempty"`
	SupervisorName     string   `json:"supervisor_name,omitempty"`
	Timestamp          string   `json:"timestamp"`
	ToolName           string   `json:"tool_name,omitempty"`
	TTL                string   `json:"ttl"`
}

// RegisterHandlers mounts the POST /claim route on the chassis. Must be
// called pre-Run; subsequent calls (or calls after Run) return [ErrAlreadyRun].
//
// This method also registers `/secrets/<name>`, `/revoke/<jti>`, and `/hz`.
// The single entry point keeps the route table inside `internal/server`.
func (s *Server) RegisterHandlers() error {
	if err := s.Mount(http.MethodPost, "/claim", http.HandlerFunc(s.handleClaim)); err != nil {
		return fmt.Errorf("server: register /claim: %w", err)
	}
	if err := s.Mount(http.MethodGet, "/s/{name}", http.HandlerFunc(s.handleSecret)); err != nil {
		return fmt.Errorf("server: register /s: %w", err)
	}
	if err := s.Mount(http.MethodPost, "/revoke", http.HandlerFunc(s.handleRevoke)); err != nil {
		return fmt.Errorf("server: register /revoke: %w", err)
	}
	if err := s.Mount(http.MethodGet, "/hz", http.HandlerFunc(s.handleHealth)); err != nil {
		return fmt.Errorf("server: register /hz: %w", err)
	}
	if err := s.Mount(http.MethodPost, "/me", http.HandlerFunc(s.handleMe)); err != nil {
		return fmt.Errorf("server: register /me: %w", err)
	}
	return nil
}

// handleClaim is the claim entry point. The middleware stack
// (request ID → IP allow-list → body cap → panic recover) wraps this handler
// in production; unit tests invoke it directly with a context pre-populated
// via [requestIDKey].
//
//nolint:gocognit,gocyclo,cyclop,funlen // sequential pipeline: shape → sig → nonce → ts → ip → cap → approver → issue; complexity is structural
func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	requestID := RequestID(ctx)

	// Stage 1: shape validation.
	req, badReqErr := s.parseClaimRequest(r)
	if badReqErr != nil {
		s.respondBadRequest(w, ctx, requestID)
		return
	}

	// Stage 2: canonical-JSON signature verify (and unknown-fingerprint check).
	if err := s.verifyClaimSignature(ctx, req); err != nil {
		// Signature failures and unknown fingerprints both map to bad_signature
		// (including the unknown registered-client-key fingerprint case) —
		// preventing client-fingerprint enumeration through error variation.
		s.respondError(w, ctx, errCodeBadSignature, outcomeBadSignature, requestID, req)
		return
	}

	// Stage 2.5: post-verify defensive redaction. We must NOT touch
	// the bytes used in signature verification, so this runs only
	// after Stage 2. Belt-and-braces: the SDK already redacts
	// client-side, but a malicious client could skip that.
	req.CommandPreview = redact.CommandPreview(req.CommandPreview)

	// Stage 3: nonce uniqueness within the configured replay window.
	// ErrNonceCacheFull is broken out as its own loud failure (503 +
	// distinct audit outcome) so cache saturation cannot hide as replay —
	// Constitution VI requires saturation to be observable in the audit
	// stream before the kernel reaps the process for OOM.
	firstSeen, err := s.nonceCache.Add(ctx, req.Nonce, s.cfg.Crypto.NonceTTL)
	switch {
	case errors.Is(err, sign.ErrNonceCacheFull):
		s.respondNonceCacheFull(w, ctx, requestID, req)
		return
	case err != nil || !firstSeen:
		s.respondError(w, ctx, errCodeNonceReplay, outcomeNonceReplay, requestID, req)
		return
	}

	// Stage 4: timestamp freshness within ±ClockSkew.
	ts, parseErr := time.Parse(time.RFC3339Nano, req.Timestamp)
	if parseErr != nil || !sign.IsFreshTimestamp(ts, s.cfg.Crypto.ClockSkew) {
		s.respondError(w, ctx, errCodeStaleTimestamp, outcomeStaleTimestamp, requestID, req)
		return
	}

	// Stage 5: IP allowlist recheck (defense-in-depth — the middleware
	// already enforced this; the L7 recheck protects against
	// future middleware-stack changes).
	peer, ok := parseRemoteAddr(r.RemoteAddr)
	if !ok || !allowedByCIDR(peer, parseAllowedCIDRs(s.cfg.Network.AllowedCIDRs)) {
		s.respondError(w, ctx, errCodeIPNotAllowed, outcomeIPNotAllowed, requestID, req)
		return
	}

	// Stage 6: TTL cap — apply BEFORE invoking the approver so the
	// operator's prompt and the issued JWT carry the same value.
	requestedTTL, _ := time.ParseDuration(req.TTL) // shape stage already accepted this
	sessionType := parseSessionType(req.SessionType)
	// standing=false: the establishing claim walks the ordinary per-session
	// ceiling. The unattended standing-reissue path (Stage 6.5) supplies
	// standing=true once the machine-bound standing-lease grant is wired in.
	cappedTTL := capTTL(sessionType, requestedTTL, s.cfg.Crypto, false)

	// Stage 6.5: Session resumption. For a supervisor that already holds a
	// live, non-revoked session for this exact (ClientIP, Scope) tuple,
	// issue a fresh JWT carrying the caller's NEW EphemeralPubKey while
	// inheriting the remaining TTL from the old session, then revoke the
	// old JTI. Bypasses the approver entirely — no DM, no rate-limit, no
	// keychain prompt. The TTL cap means we never silently extend an
	// existing approval window; if the operator wants longer, they must
	// wait for the old session to expire and re-approve.
	//
	// A machine-bound standing lease (standing_lease) is the one exception to
	// the "never extend" rule: it reissues a fresh full-window session against
	// the grant a human already established for the same machine — the
	// requestedTTL is passed through so the standing path can re-anchor to the
	// full standing ceiling instead of the remaining window.
	//
	// Eliminates the per-restart user-visible "wait 5 minutes for Discord
	// rate-limit window" cycle for long-lived supervisors — the supervisor
	// process restarts cheap, the human's approval cadence stays intact.
	if !req.ForceApproval && s.tryResumeSupervisorSession(w, r, ctx, req, sessionType, peer, requestedTTL, cappedTTL) {
		return
	}

	// Stage 7: dispatch to approver under a config-driven deadline.
	deadline := s.cfg.Crypto.ClaimApprovalTimeout
	if deadline <= 0 {
		// Defense-in-depth: a misconfigured zero-value here would otherwise
		// produce an immediately-cancelled context. Fall back to the
		// documented default rather than denying-by-deadline.
		deadline = 60 * time.Second
	}
	apprCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	dec, apprErr := s.approverImpl.RequestApproval(apprCtx, ApprovalRequest{
		RequestID:      requestID,
		MachineName:    req.MachineName,
		ClientIP:       peer,
		Scope:          sortedScope(req.Scope),
		Reason:         req.Reason,
		SessionType:    sessionType,
		RequestedTTL:   cappedTTL,
		SupervisorName: req.SupervisorName,
		AgentIdentity:  req.AgentIdentity,
		AgentModel:     req.AgentModel,
		ToolName:       req.ToolName,
		CommandPreview: req.CommandPreview,
		RecentSummary:  req.RecentSummary,
	})

	switch {
	case apprErr == nil && dec.Approved && dec.GrantedTTL > 0:
		// ONLY path to a 200. Constitution II — no other branch may issue.
		s.issueAndRespond(w, r, ctx, req, sessionType, peer, cappedTTL, outcomeApproved)
		return
	case errors.Is(apprErr, ErrApproverDenied):
		s.respondApproverError(w, ctx, http.StatusForbidden, errCodeDenied,
			outcomeDenied, requestID, sessionType, req)
	case errors.Is(apprErr, ErrApproverTimeout) || errors.Is(apprErr, context.DeadlineExceeded):
		s.respondApproverError(w, ctx, http.StatusRequestTimeout, errCodeApprovalTimeout,
			outcomeApprovalTimeout, requestID, sessionType, req)
	case errors.Is(apprErr, ErrApproverRateLimited):
		s.respondApproverError(w, ctx, http.StatusTooManyRequests, errCodeRateLimited,
			outcomeRateLimited, requestID, sessionType, req)
	case errors.Is(apprErr, ErrApproverUnavailable):
		s.respondApproverError(w, ctx, http.StatusServiceUnavailable, errCodeDiscordUnavailable,
			outcomeDiscordUnavailable, requestID, sessionType, req)
	default:
		// Any non-sentinel error OR (Decision{Approved:false}, nil) lands
		// here. Constitution II — fail-closed.
		s.respondApproverError(w, ctx, http.StatusServiceUnavailable, errCodeUnknownOutcome,
			outcomeUnknown, requestID, sessionType, req)
	}
}

// tryResumeSupervisorSession implements Stage 6.5 session resumption: when a
// SessionSupervisor caller already holds a live, non-revoked token for this
// exact (ClientIP, Scope) tuple, issue a fresh JWT carrying the caller's NEW
// EphemeralPubKey. Returns true when resumption fired (caller MUST return).
// Returns false when the request should flow through the human approver path.
//
// Two shapes ride this fast path, both bypassing the approver (no DM, no
// rate-limit, no keychain prompt):
//
//   - Ordinary supervisor resume — inherits only the REMAINING TTL of the
//     live session; it never extends the window a human granted. If the
//     operator wants longer, they wait for the old session to expire and
//     re-approve.
//   - Machine-bound standing lease — when the incoming claim opts into a
//     standing lease AND the live session is itself a standing grant for the
//     SAME client_machine_index, reissue a fresh FULL-WINDOW session (the
//     standing ceiling) riding the one-time human-established grant, so a
//     scheduled daemon never needs a recurring human approval. Any mismatch —
//     a standing claim over an ordinary grant, or a different machine index —
//     falls through to the human approver so the FIRST grant on a machine is
//     always human-established (Constitution II).
func (s *Server) tryResumeSupervisorSession(
	w http.ResponseWriter,
	r *http.Request,
	ctx context.Context,
	req *claimRequest,
	sessionType SessionType,
	peer netip.Addr,
	requestedTTL time.Duration,
	cappedTTL time.Duration,
) bool {
	if sessionType != SessionSupervisor {
		return false
	}
	existing, found := s.tokenStore.FindActiveSession(token.SessionSupervisor, token.NewClientIP(peer), token.NewScope(req.Scope))
	if !found {
		return false
	}
	remaining := existing.ExpiresAt.Sub(s.clock())
	if remaining <= 0 {
		return false
	}

	// Standing-lease reissue. Only fires when the live grant is a standing
	// grant bound to the same machine index the caller presents; otherwise the
	// claim must obtain a fresh human-established grant.
	if req.StandingLease {
		if !existing.StandingLease || existing.ClientMachineIndex != req.ClientMachineIndex {
			return false
		}
		standingTTL := capTTL(sessionType, requestedTTL, s.cfg.Crypto, true)
		s.issueAndRespond(w, r, ctx, req, sessionType, peer, standingTTL, outcomeStandingReissue)
		// Best-effort revoke of the old token so a stale JWT fails closed.
		_, _ = s.tokenStore.RevokeIdempotent(existing.JTI)
		s.logOpsEvent(
			ctx, "claim standing-reissue",
			"request_id", RequestID(ctx),
			"client_ip", peer.String(),
			"old_jti", existing.JTI,
			"machine_index", req.ClientMachineIndex,
			"reissued_ttl", standingTTL.String(),
		)
		return true
	}

	resumedTTL := cappedTTL
	if resumedTTL > remaining {
		resumedTTL = remaining
	}
	s.issueAndRespond(w, r, ctx, req, sessionType, peer, resumedTTL, outcomeApproved)
	// Best-effort revoke of the old token so subsequent /request calls
	// bearing the prior JWT fail closed. Failure is non-fatal: the new token
	// was already issued and the old one will expire naturally within
	// `remaining`. Return values intentionally discarded.
	_, _ = s.tokenStore.RevokeIdempotent(existing.JTI)
	s.logOpsEvent(
		ctx, "claim resumed",
		"request_id", RequestID(ctx),
		"client_ip", peer.String(),
		"old_jti", existing.JTI,
		"inherited_ttl", resumedTTL.String(),
	)
	return true
}

// issueAndRespond is the SOLE path through which a JWT is minted and a 200
// is written. Inlining the comment for the constitutional invariant: the
// caller's switch case asserts (apprErr == nil && dec.Approved &&
// dec.GrantedTTL > 0) — Constitution II is encoded in that one condition.
func (s *Server) issueAndRespond(
	w http.ResponseWriter,
	r *http.Request,
	ctx context.Context,
	req *claimRequest,
	sessionType SessionType,
	peer netip.Addr,
	cappedTTL time.Duration,
	outcome outcomeLabel,
) {
	maxUses := s.cfg.Crypto.DefaultMaxUses
	if maxUses <= 0 {
		maxUses = 50
	}
	tokenSession := token.SessionInteractive
	if sessionType == SessionSupervisor {
		tokenSession = token.SessionSupervisor
	}

	tok, err := s.tokenIssuer(ctx, token.IssueParams{
		Now:                s.clock(),
		TTL:                cappedTTL,
		Scope:              sortedScope(req.Scope),
		ClientIP:           peer.String(),
		RequestID:          RequestID(ctx),
		MaxUses:            maxUses,
		EphemeralPubKey:    req.EphemeralPubKey,
		SessionType:        tokenSession,
		StandingLease:      req.StandingLease,
		ClientMachineIndex: req.ClientMachineIndex,
	})
	if err != nil {
		// Token mint failure is treated as unknown-outcome (fail-closed):
		// no JWT in the body, audit reflects the failed mint. This branch
		// exists for defense-in-depth — a healthy issuer never errors here
		// because params are pre-validated.
		s.respondApproverError(w, ctx, http.StatusServiceUnavailable, errCodeUnknownOutcome,
			outcomeUnknown, RequestID(ctx), sessionType, req)
		return
	}
	if err := s.tokenStore.Add(tok); err != nil {
		s.respondApproverError(w, ctx, http.StatusServiceUnavailable, errCodeUnknownOutcome,
			outcomeUnknown, RequestID(ctx), sessionType, req)
		return
	}

	detail := buildAuditDetail(outcome, sortedScope(req.Scope), sessionType, cappedTTL, tok.JTI)
	s.emitClaimAudit(ctx, RequestID(ctx), peer, detail)
	s.logOpsEvent(
		ctx, "claim approved",
		"request_id", RequestID(ctx),
		"client_ip", peer.String(),
		"outcome", string(outcome),
		"session_type", sessionType.String(),
		"scope_count", len(req.Scope),
		"granted_ttl", cappedTTL.String(),
	)
	_ = r // r is the original request; kept for symmetry with future middleware needs
	writeJSONResponse(w, http.StatusOK, claimResponse{
		JWT:       tok.Encoded,
		ExpiresAt: tok.ExpiresAt.Format(time.RFC3339Nano),
		JTI:       tok.JTI,
	})
}

// parseClaimRequest decodes the request body and validates every required
// field. Returns (nil, err) on any failure; the caller maps to 400
// bad_request without echoing the malformed input.
//
//nolint:cyclop,gocyclo,gocognit // sequential per-field validation: branching is inherent to the request contract
func (s *Server) parseClaimRequest(r *http.Request) (*claimRequest, error) {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req claimRequest
	if err := dec.Decode(&req); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	// Reject trailing junk after the JSON value.
	if dec.More() {
		return nil, errShapeTrailingData
	}

	if len(req.Scope) == 0 {
		return nil, errShapeScopeEmpty
	}
	for _, name := range req.Scope {
		if !getScopeNameRe().MatchString(name) {
			return nil, errShapeScopeBadName
		}
	}
	if l := len(req.Reason); l == 0 || l > 256 {
		return nil, errShapeReasonLen
	}
	dur, parseErr := time.ParseDuration(req.TTL)
	if parseErr != nil || dur <= 0 {
		return nil, errShapeTTLInvalid
	}
	if req.SessionType != sessionTypeInteractiveStr && req.SessionType != sessionTypeSupervisorStr {
		return nil, errShapeSessionTypeUnknown
	}
	switch req.SessionType {
	case sessionTypeSupervisorStr:
		if req.SupervisorName == "" {
			return nil, errShapeSupervisorNameMissing
		}
		if !getSupervisorNameRe().MatchString(req.SupervisorName) {
			return nil, errShapeSupervisorNameInvalid
		}
	case sessionTypeInteractiveStr:
		if req.SupervisorName != "" {
			return nil, errShapeSupervisorNameSet
		}
	}
	// A machine-bound standing lease is supervisor-only and MUST carry a
	// non-zero client_machine_index (the machine anchor the reissue path
	// matches against). Reject any other shape before it reaches the pipeline.
	if req.StandingLease && (req.SessionType != sessionTypeSupervisorStr || req.ClientMachineIndex == 0) {
		return nil, errShapeStandingLeaseInvalid
	}
	if !getEphemeralPubKeyRe().MatchString(req.EphemeralPubKey) {
		return nil, errShapeEphemeralPub
	}
	if l := len(req.Nonce); l < 8 || l > 128 || !getNonceCharsRe().MatchString(req.Nonce) {
		return nil, errShapeNonceInvalid
	}
	if _, err := time.Parse(time.RFC3339Nano, req.Timestamp); err != nil {
		return nil, errShapeTimestampFormat
	}
	if req.Signature == "" {
		return nil, errShapeSignatureEmpty
	}
	if !getRequestIDRe().MatchString(req.RequestID) {
		return nil, errShapeRequestIDInvalid
	}
	if !getMachineNameRe().MatchString(req.MachineName) {
		return nil, errShapeMachineNameInvalid
	}
	if !getFingerprintRe().MatchString(req.ClientKeyFingerprint) {
		return nil, errShapeFingerprintInvalid
	}
	// Agent-context length caps. Each is optional; only enforced
	// when present. Belt-and-braces redaction on CommandPreview
	// (the SDK / CLI redacts client-side, but a malicious client
	// could omit redaction — the server re-runs it).
	if len(req.AgentIdentity) > maxAgentIdentityLen {
		return nil, errShapeAgentFieldTooLong
	}
	if len(req.AgentModel) > maxAgentModelLen {
		return nil, errShapeAgentFieldTooLong
	}
	if len(req.ToolName) > maxToolNameLen {
		return nil, errShapeAgentFieldTooLong
	}
	if len(req.CommandPreview) > maxCommandPreviewLen {
		return nil, errShapeAgentFieldTooLong
	}
	if len(req.RecentSummary) > maxRecentSummaryLen {
		return nil, errShapeAgentFieldTooLong
	}
	// CommandPreview redaction is deliberately deferred to
	// handleClaim, AFTER signature verification. Redacting here
	// would mutate the bytes the client signed over and produce
	// bad_signature on every claim that supplied a CommandPreview.
	return &req, nil
}

// verifyClaimSignature resolves the client's registered pubkey by fingerprint
// and verifies the signature over canonical-JSON of the signed-payload
// fields. Returns ErrSignatureInvalid (sign package) or ErrClientUnknown.
func (s *Server) verifyClaimSignature(ctx context.Context, req *claimRequest) error {
	pub, err := s.clientKeyResolver(req.ClientKeyFingerprint)
	if err != nil {
		// Unknown fingerprint OR loader error → bad_signature (no enumeration).
		return err
	}
	payload := signedPayload{
		AgentIdentity:      req.AgentIdentity,
		AgentModel:         req.AgentModel,
		ClientMachineIndex: req.ClientMachineIndex,
		CommandPreview:     req.CommandPreview,
		EphemeralPubKey:    req.EphemeralPubKey,
		ForceApproval:      req.ForceApproval,
		MachineName:        req.MachineName,
		Nonce:              req.Nonce,
		Reason:             req.Reason,
		RecentSummary:      req.RecentSummary,
		RequestID:          req.RequestID,
		Scope:              req.Scope,
		SessionType:        req.SessionType,
		StandingLease:      req.StandingLease,
		SupervisorName:     req.SupervisorName,
		Timestamp:          req.Timestamp,
		ToolName:           req.ToolName,
		TTL:                req.TTL,
	}
	canonical, err := sign.CanonicalJSON(payload)
	if err != nil {
		return fmt.Errorf("canonicalise: %w", err)
	}
	sigBytes, err := decodeSignature(req.Signature)
	if err != nil {
		return fmt.Errorf("signature decode: %w", err)
	}
	if err := sign.Verify(ctx, pub, canonical, sigBytes); err != nil {
		return err
	}
	return nil
}

// respondBadRequest writes a 400 with a server-generated request_id
// and emits an outcome=bad-request audit event. The body did not parse, so
// scope/session_type are unknown — the audit detail omits them.
func (s *Server) respondBadRequest(w http.ResponseWriter, ctx context.Context, requestID string) {
	detail := map[string]string{"outcome": string(outcomeBadRequest)}
	s.emitClaimAudit(ctx, requestID, netip.Addr{}, detail)
	s.logOpsEvent(
		ctx, "claim rejected",
		"request_id", requestID,
		"outcome", string(outcomeBadRequest),
	)
	writeJSONResponse(w, http.StatusBadRequest, errorResponse{
		Error:     errCodeBadRequest,
		RequestID: requestID,
	})
}

// respondNonceCacheFull writes the dedicated 503 response for the
// nonce-cache saturation path. Distinct from [Server.respondError] so the
// loud signal (status, ops log, audit detail) cannot be conflated with a
// replay-defense rejection. Constitution VI: silent OOM is unacceptable —
// every cap-hit MUST emit a [outcomeNonceCacheFull] audit entry the
// operator can alert on before the kernel reaps the process.
func (s *Server) respondNonceCacheFull(
	w http.ResponseWriter,
	ctx context.Context,
	requestID string,
	req *claimRequest,
) {
	sessionType := parseSessionType(req.SessionType)
	scope := sortedScope(req.Scope)
	detail := buildAuditDetail(outcomeNonceCacheFull, scope, sessionType, 0, "")
	peer, _ := parseRemoteAddr("")
	s.emitClaimAudit(ctx, requestID, peer, detail)
	s.logger.ErrorContext(
		ctx, "nonce cache saturated",
		"request_id", requestID,
		"outcome", string(outcomeNonceCacheFull),
		"session_type", sessionType.String(),
		"scope_count", len(req.Scope),
	)
	writeJSONResponse(w, http.StatusServiceUnavailable, errorResponse{
		Error:     errCodeNonceCacheFull,
		RequestID: requestID,
	})
}

// respondError writes a non-200 pre-approval outcome (stages 2-5: signature,
// nonce, timestamp, IP). Audit detail includes session_type and scope — req
// has been successfully parsed. Always emits HTTP 403.
func (s *Server) respondError(
	w http.ResponseWriter,
	ctx context.Context,
	errCode string,
	outcome outcomeLabel,
	requestID string,
	req *claimRequest,
) {
	sessionType := parseSessionType(req.SessionType)
	scope := sortedScope(req.Scope)
	detail := buildAuditDetail(outcome, scope, sessionType, 0, "")
	peer, _ := parseRemoteAddr("") // ClientIP optional in audit; not load-bearing here
	s.emitClaimAudit(ctx, requestID, peer, detail)
	s.logOpsEvent(
		ctx, "claim rejected",
		"request_id", requestID,
		"outcome", string(outcome),
		"session_type", sessionType.String(),
		"scope_count", len(req.Scope),
	)
	writeJSONResponse(w, http.StatusForbidden, errorResponse{
		Error:     errCode,
		RequestID: requestID,
	})
}

// respondApproverError writes a non-200 outcome that arose from the approver
// dispatch step — same shape as respondError but with the session_type the
// approver actually saw.
func (s *Server) respondApproverError(
	w http.ResponseWriter,
	ctx context.Context,
	status int,
	errCode string,
	outcome outcomeLabel,
	requestID string,
	sessionType SessionType,
	req *claimRequest,
) {
	scope := sortedScope(req.Scope)
	detail := buildAuditDetail(outcome, scope, sessionType, 0, "")
	peer, _ := parseRemoteAddr("")
	s.emitClaimAudit(ctx, requestID, peer, detail)
	s.logOpsEvent(
		ctx, "claim rejected",
		"request_id", requestID,
		"outcome", string(outcome),
		"session_type", sessionType.String(),
		"scope_count", len(req.Scope),
	)
	writeJSONResponse(w, status, errorResponse{
		Error:     errCode,
		RequestID: requestID,
	})
}

// emitClaimAudit emits a single AuditClaimOutcome event. AuditWriter errors
// are logged at WARN — the chassis cannot let an audit-writer outage block a
// security-relevant response.
func (s *Server) emitClaimAudit(ctx context.Context, requestID string, clientIP netip.Addr, detail map[string]string) {
	if err := s.audit.Write(ctx, AuditEvent{
		Type:      AuditClaimOutcome,
		At:        s.clock(),
		RequestID: requestID,
		ClientIP:  clientIP,
		Detail:    detail,
	}); err != nil {
		s.logger.WarnContext(ctx, "audit write claim_outcome failed", "err", err.Error())
	}
}

// logOpsEvent emits an INFO operational log line. The handler explicitly
// omits the signature, nonce, ephemeral pubkey, supplied reason, JWT, machine
// name, and scope contents (Constitution X — log-vs-audit asymmetry).
func (s *Server) logOpsEvent(ctx context.Context, msg string, kvs ...any) {
	s.logger.InfoContext(ctx, msg, kvs...)
}

// buildAuditDetail returns the allow-list audit detail map.
// Scope is the sorted-comma-joined names; granted_ttl and jti are
// populated only on the `approved` outcome. NEVER returns reason, signature,
// nonce, ephemeral_pubkey, jwt, or client_key_fingerprint.
func buildAuditDetail(outcome outcomeLabel, scope []string, sessionType SessionType, grantedTTL time.Duration, jti string) map[string]string {
	d := map[string]string{
		"outcome":      string(outcome),
		"session_type": sessionType.String(),
	}
	if len(scope) > 0 {
		d["scope"] = strings.Join(scope, ",")
	}
	if grantedTTL > 0 {
		d["granted_ttl"] = grantedTTL.String()
	}
	if jti != "" {
		d["jti"] = jti
	}
	return d
}

// capTTL clamps the requested TTL to the applicable configured maximum.
// Applied BEFORE the approver dispatch so the operator's prompt shows the
// actual TTL.
//
// standing selects the distinguished machine-bound standing-lease ceiling
// (config.DefaultStandingLeaseTTLMax) instead of the per-session-type maximum:
// only the unattended standing-reissue path — riding a grant a human already
// established — sets it true, so an opted-in daemon may exceed the ordinary 24h
// supervisor ceiling. Every ordinary claim passes standing=false and keeps the
// 24h supervisor / interactive ceiling below.
func capTTL(sessionType SessionType, requested time.Duration, cs config.CryptoSection, standing bool) time.Duration {
	var ceiling time.Duration
	switch {
	case standing:
		ceiling = config.DefaultStandingLeaseTTLMax
	case sessionType == SessionSupervisor:
		ceiling = cs.MaxSupervisorTTL
	case sessionType == SessionInteractive:
		ceiling = cs.MaxInteractiveTTL
	default:
		ceiling = cs.MaxInteractiveTTL
	}
	if ceiling > 0 && requested > ceiling {
		return ceiling
	}
	return requested
}

// parseSessionType maps the wire string to the chassis SessionType enum.
// "unknown" sentinel returned for unparseable input — should never happen
// past the shape-validation stage.
func parseSessionType(raw string) SessionType {
	switch raw {
	case sessionTypeInteractiveStr:
		return SessionInteractive
	case sessionTypeSupervisorStr:
		return SessionSupervisor
	default:
		return SessionType(0)
	}
}

// sortedScope returns a fresh slice with the input sorted ascending. Used in
// the canonical signing payload AND in the audit detail so both are stable.
func sortedScope(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

// writeJSONResponse marshals body and writes it with the chassis's
// canonical security headers. Every response — success or failure — receives
// the same set. Marshal-then-write is preferred over Encoder.Encode so a
// failed marshal does NOT half-write a body before the error path runs;
// however the only types we marshal here are package-private struct values
// with no functions/channels, so json.Marshal is total in practice.
func writeJSONResponse(w http.ResponseWriter, status int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		// Should never happen given the closed type set above; fail loud.
		raw = []byte(`{"error":"internal_marshal_error"}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// decodeSignature accepts standard and url-safe base64 forms (with or without
// padding) so a future client-side library change does not require a server
// release.
func decodeSignature(s string) ([]byte, error) {
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	} {
		if b, err := dec(s); err == nil {
			return b, nil
		}
	}
	return nil, errShapeBase64Invalid
}

// silence the unused-import linter when the sign package's NonceCache type is
// only referenced via the [Server] field. The handler reads s.nonceCache and
// the constructor [sign.NewNonceCache] in [server.go]; this anchors the type
// reference so future refactors do not accidentally remove the import.
var _ sign.NonceCache
