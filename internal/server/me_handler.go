// Package server: POST /me handler.
//
// /me is the read-only capability-and-freshness endpoint. It lets an
// enrolled client ask "what scopes exist on this server, and what does
// my current session look like?" without burning a Discord approval.
// Every request is signed (same ECDSA + canonical-JSON pattern as
// /claim) so the server can reject anonymous probes; the optional
// Authorization: Bearer header lets the client supplement the response
// with session-specific fields.
//
// /me NEVER triggers an approval, NEVER mutates server state beyond
// the standard nonce-replay window, and NEVER returns any secret
// material. Scope NAMES are exposed (low-sensitivity per
// docs/SECURITY.md §6 confidentiality grading) so agents can plan
// requests intelligently.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"time"

	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// AuditMeQuery is the [AuditEventType] emitted by /me for every
// terminal outcome. Distinct from claim_outcome so operators can
// alert on approval-bypass paths separately.
const AuditMeQuery AuditEventType = "me_query"

const (
	errCodeMeBadRequest   = "bad_request"
	errCodeMeBadSignature = "bad_signature"
	errCodeMeNonceReplay  = "nonce_replay"
	errCodeMeStaleTS      = "stale_timestamp"
	errCodeMeIPNotAllowed = "ip_not_allowed"
)

// meRequest is the JSON-decoded /me request body. The signed payload
// shape is intentionally narrower than /claim — no scope, no
// session_type, no ephemeral pubkey — because /me returns information
// the server already has and does not enter the approval pipeline.
type meRequest struct {
	Nonce                string `json:"nonce"`
	Timestamp            string `json:"timestamp"`
	Signature            string `json:"signature"`
	RequestID            string `json:"request_id"`
	MachineName          string `json:"machine_name"`
	ClientKeyFingerprint string `json:"client_key_fingerprint"`
}

// meSignedPayload is the canonicalised struct over which the client
// signs and the server verifies. Field set excludes the signature
// itself and the fingerprint (fingerprint resolves the pubkey before
// verification, so signing over it would be circular).
type meSignedPayload struct {
	MachineName string `json:"machine_name"`
	Nonce       string `json:"nonce"`
	RequestID   string `json:"request_id"`
	Timestamp   string `json:"timestamp"`
}

// meResponse is the success-path response body. New fields appear with
// omitempty so older SDKs decode newer servers without error.
type meResponse struct {
	SchemaVersion     int            `json:"schema_version"`
	ServerVersion     string         `json:"server_version"`
	ScopesAvailable   []string       `json:"scopes_available"`
	CurrentSession    *meSessionView `json:"current_session,omitempty"`
	NextRefreshWindow string         `json:"next_refresh_window,omitempty"`
}

// meSessionView is the per-session subset, populated only when a valid
// Bearer JWT accompanies the request.
type meSessionView struct {
	JTI         string   `json:"jti"`
	ExpiresAt   string   `json:"expires_at"`
	Scopes      []string `json:"scopes"`
	MaxUses     int      `json:"max_uses"`
	SessionType string   `json:"session_type"`
}

// handleMe is the /me entry point. Middleware (request ID, IP allow-
// list, body cap, recover) wraps this handler in production.
//
//nolint:gocognit,gocyclo,cyclop,funlen // sequential pipeline mirroring /claim; complexity is structural.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	requestID := RequestID(ctx)

	// Stage 1: shape validation.
	req, badReqErr := s.parseMeRequest(r)
	if badReqErr != nil {
		s.emitMeAudit(ctx, requestID, netip.Addr{}, "bad-request")
		writeJSONResponse(w, http.StatusBadRequest, errorResponse{
			Error:     errCodeMeBadRequest,
			RequestID: requestID,
		})
		return
	}

	// Stage 2: signature verification (and unknown-fingerprint check).
	if err := s.verifyMeSignature(ctx, req); err != nil {
		s.emitMeAudit(ctx, requestID, netip.Addr{}, "bad-signature")
		writeJSONResponse(w, http.StatusForbidden, errorResponse{
			Error:     errCodeMeBadSignature,
			RequestID: requestID,
		})
		return
	}

	// Stage 3: nonce uniqueness. /me uses the same nonce cache as
	// /claim — a leaked /me nonce cannot be replayed as a /claim
	// nonce because the signed-payload field set differs.
	firstSeen, err := s.nonceCache.Add(ctx, req.Nonce, s.cfg.Crypto.NonceTTL)
	if err != nil || !firstSeen {
		s.emitMeAudit(ctx, requestID, netip.Addr{}, "nonce-replay")
		writeJSONResponse(w, http.StatusForbidden, errorResponse{
			Error:     errCodeMeNonceReplay,
			RequestID: requestID,
		})
		return
	}

	// Stage 4: timestamp freshness.
	ts, parseErr := time.Parse(time.RFC3339Nano, req.Timestamp)
	if parseErr != nil || !sign.IsFreshTimestamp(ts, s.cfg.Crypto.ClockSkew) {
		s.emitMeAudit(ctx, requestID, netip.Addr{}, "stale-timestamp")
		writeJSONResponse(w, http.StatusForbidden, errorResponse{
			Error:     errCodeMeStaleTS,
			RequestID: requestID,
		})
		return
	}

	// Stage 5: IP allowlist recheck (defense-in-depth).
	peer, ok := parseRemoteAddr(r.RemoteAddr)
	if !ok || !allowedByCIDR(peer, parseAllowedCIDRs(s.cfg.Network.AllowedCIDRs)) {
		s.emitMeAudit(ctx, requestID, peer, "ip-not-allowed")
		writeJSONResponse(w, http.StatusForbidden, errorResponse{
			Error:     errCodeMeIPNotAllowed,
			RequestID: requestID,
		})
		return
	}

	// Stage 6: optional Bearer JWT — populate current_session when
	// present and valid. Absent or invalid bearer is NOT a 401;
	// /me's contract is "respond with whatever you can see."
	// Use token.Inspect (not Validate) so a poll doesn't decrement
	// MaxUses or enforce per-scope binding — /me is read-only.
	var session *meSessionView
	if encoded, hasBearer := extractBearer(r.Header.Get("Authorization")); hasBearer && s.jwtVerifyKey != nil {
		claims, vErr := token.Inspect(ctx, encoded, s.jwtVerifyKey)
		if vErr == nil {
			session = sessionViewFromClaims(claims)
		}
	}

	// Stage 7: vault scope enumeration. Snapshot the store pointer
	// once so a hot-reload mid-request can't bisect the result.
	scopes := []string{}
	if store := s.vaultPtr.Load(); store != nil {
		scopes = (*store).Names()
	}

	// Stage 8: audit + respond.
	s.emitMeAudit(ctx, requestID, peer, "ok")
	resp := meResponse{
		SchemaVersion:   1,
		ServerVersion:   s.serverVersion,
		ScopesAvailable: scopes,
		CurrentSession:  session,
		// NextRefreshWindow intentionally omitted — refresh-window
		// state lives on the supervisor, not the vault server. The
		// SDK fetches it from the supervisor's status socket instead.
	}
	writeJSONResponse(w, http.StatusOK, resp)
}

// parseMeRequest decodes and shape-validates the request body. Reuses
// /claim's field regexes so the validation surface is identical.
//
//nolint:cyclop,gocyclo // sequential per-field validation; complexity is structural
func (s *Server) parseMeRequest(r *http.Request) (*meRequest, error) {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req meRequest
	if err := dec.Decode(&req); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if dec.More() {
		return nil, errShapeTrailingData
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

// verifyMeSignature resolves the client pubkey by fingerprint and
// verifies the signature over canonical-JSON of meSignedPayload.
func (s *Server) verifyMeSignature(ctx context.Context, req *meRequest) error {
	pub, err := s.clientKeyResolver(req.ClientKeyFingerprint)
	if err != nil {
		return err
	}
	payload := meSignedPayload{
		MachineName: req.MachineName,
		Nonce:       req.Nonce,
		RequestID:   req.RequestID,
		Timestamp:   req.Timestamp,
	}
	canonical, err := sign.CanonicalJSON(payload)
	if err != nil {
		return fmt.Errorf("canonicalise: %w", err)
	}
	sigBytes, err := decodeSignature(req.Signature)
	if err != nil {
		return fmt.Errorf("signature decode: %w", err)
	}
	return sign.Verify(ctx, pub, canonical, sigBytes)
}

// emitMeAudit writes a single AuditMeQuery event. Failure to write
// audit is logged at WARN — never blocks the response (same policy
// as emitClaimAudit).
func (s *Server) emitMeAudit(ctx context.Context, requestID string, clientIP netip.Addr, outcome string) {
	detail := map[string]string{"outcome": outcome}
	if err := s.audit.Write(ctx, AuditEvent{
		Type:      AuditMeQuery,
		At:        s.clock(),
		RequestID: requestID,
		ClientIP:  clientIP,
		Detail:    detail,
	}); err != nil {
		s.logger.WarnContext(ctx, "audit write me_query failed", "err", err.Error())
	}
}

// sessionViewFromClaims projects token.Claims into the wire view.
func sessionViewFromClaims(c *token.Claims) *meSessionView {
	view := &meSessionView{
		JTI:         c.ID,
		Scopes:      append([]string(nil), c.Scope...),
		MaxUses:     c.MaxUses,
		SessionType: string(c.SessionType),
	}
	if c.ExpiresAt != nil {
		view.ExpiresAt = c.ExpiresAt.Format(time.RFC3339)
	}
	return view
}
