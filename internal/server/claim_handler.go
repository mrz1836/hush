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
	outcomeBadRequest         outcomeLabel = "bad-request"
	outcomeBadSignature       outcomeLabel = "bad-signature"
	outcomeNonceReplay        outcomeLabel = "nonce-replay"
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
	errShapeTrailingData       = errors.New("server: claim: trailing data after JSON value")
	errShapeScopeEmpty         = errors.New("server: claim: scope is empty")
	errShapeScopeBadName       = errors.New("server: claim: scope element invalid")
	errShapeReasonLen          = errors.New("server: claim: reason length out of [1, 256]")
	errShapeTTLInvalid         = errors.New("server: claim: ttl invalid or non-positive")
	errShapeSessionTypeUnknown = errors.New("server: claim: session_type not in {interactive, supervisor}")
	errShapeEphemeralPub       = errors.New("server: claim: ephemeral_pubkey not 33-byte hex")
	errShapeNonceInvalid       = errors.New("server: claim: nonce invalid")
	errShapeTimestampFormat    = errors.New("server: claim: timestamp not RFC3339Nano")
	errShapeSignatureEmpty     = errors.New("server: claim: signature empty")
	errShapeRequestIDInvalid   = errors.New("server: claim: request_id invalid")
	errShapeMachineNameInvalid = errors.New("server: claim: machine_name invalid")
	errShapeFingerprintInvalid = errors.New("server: claim: client_key_fingerprint invalid")
	errShapeBase64Invalid      = errors.New("server: claim: signature not a recognized base64 encoding")
)

// Static error codes returned in response bodies. The set is exhaustive;
// future codes may be appended (never repurposed).
const (
	errCodeBadRequest         = "bad_request"
	errCodeBadSignature       = "bad_signature"
	errCodeNonceReplay        = "nonce_replay"
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

func getEphemeralPubKeyRe() *regexp.Regexp {
	ephemeralPubKeyOnce.Do(func() { ephemeralPubKeyRe = regexp.MustCompile(`^[0-9a-fA-F]{66}$`) })
	return ephemeralPubKeyRe
}

func getNonceCharsRe() *regexp.Regexp {
	// base64url alphabet — A-Z a-z 0-9 - _ , length asserted separately
	nonceCharsOnce.Do(func() { nonceCharsRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`) })
	return nonceCharsRe
}

// claimRequest is the JSON-decoded request body. All fields are required.
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
	ClientKeyFingerprint string   `json:"client_key_fingerprint"`
}

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
// result is byte-identical between client and server.
type signedPayload struct {
	EphemeralPubKey string   `json:"ephemeral_pubkey"`
	MachineName     string   `json:"machine_name"`
	Nonce           string   `json:"nonce"`
	Reason          string   `json:"reason"`
	RequestID       string   `json:"request_id"`
	Scope           []string `json:"scope"`
	SessionType     string   `json:"session_type"`
	Timestamp       string   `json:"timestamp"`
	TTL             string   `json:"ttl"`
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

	// Stage 3: nonce uniqueness within the configured replay window.
	firstSeen, err := s.nonceCache.Add(ctx, req.Nonce, s.cfg.Crypto.NonceTTL)
	if err != nil || !firstSeen {
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
	cappedTTL := capTTL(sessionType, requestedTTL, s.cfg.Crypto)

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
		RequestID:    requestID,
		MachineName:  req.MachineName,
		ClientIP:     peer,
		Scope:        sortedScope(req.Scope),
		Reason:       req.Reason,
		SessionType:  sessionType,
		RequestedTTL: cappedTTL,
	})

	switch {
	case apprErr == nil && dec.Approved && dec.GrantedTTL > 0:
		// ONLY path to a 200. Constitution II — no other branch may issue.
		s.issueAndRespond(w, r, ctx, req, sessionType, peer, cappedTTL, dec)
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
	_ Decision,
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
		Now:             s.clock(),
		TTL:             cappedTTL,
		Scope:           sortedScope(req.Scope),
		ClientIP:        peer.String(),
		RequestID:       RequestID(ctx),
		MaxUses:         maxUses,
		EphemeralPubKey: req.EphemeralPubKey,
		SessionType:     tokenSession,
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

	detail := buildAuditDetail(outcomeApproved, sortedScope(req.Scope), sessionType, cappedTTL, tok.JTI)
	s.emitClaimAudit(ctx, RequestID(ctx), peer, detail)
	s.logOpsEvent(ctx, "claim approved",
		"request_id", RequestID(ctx),
		"client_ip", peer.String(),
		"outcome", string(outcomeApproved),
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
	if req.SessionType != "interactive" && req.SessionType != "supervisor" {
		return nil, errShapeSessionTypeUnknown
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
		EphemeralPubKey: req.EphemeralPubKey,
		MachineName:     req.MachineName,
		Nonce:           req.Nonce,
		Reason:          req.Reason,
		RequestID:       req.RequestID,
		Scope:           req.Scope,
		SessionType:     req.SessionType,
		Timestamp:       req.Timestamp,
		TTL:             req.TTL,
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
	s.logOpsEvent(ctx, "claim rejected",
		"request_id", requestID,
		"outcome", string(outcomeBadRequest),
	)
	writeJSONResponse(w, http.StatusBadRequest, errorResponse{
		Error:     errCodeBadRequest,
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
	s.logOpsEvent(ctx, "claim rejected",
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
	s.logOpsEvent(ctx, "claim rejected",
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

// capTTL clamps the requested TTL to the per-session-type configured maximum.
// Applied BEFORE the approver dispatch so the operator's prompt shows the
// actual TTL.
func capTTL(sessionType SessionType, requested time.Duration, cs config.CryptoSection) time.Duration {
	var ceiling time.Duration
	switch sessionType {
	case SessionSupervisor:
		ceiling = cs.MaxSupervisorTTL
	case SessionInteractive:
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
	case "interactive":
		return SessionInteractive
	case "supervisor":
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
