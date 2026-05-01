package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"sync"
	"time"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// Revoke-handler error codes.
const (
	errCodeRevokeBadRequest     = "bad_request"
	errCodeRevokeBadSignature   = "bad_signature"
	errCodeRevokeNonceReplay    = "nonce_replay"
	errCodeRevokeStaleTimestamp = "stale_timestamp"
)

// jtiRe is the UUIDv4 form `8-4-4-4-12` lowercase hex with dashes.
//
//nolint:gochecknoglobals // sentinel-class: lazily initialized compiled regex
var (
	jtiOnce sync.Once
	jtiRe   *regexp.Regexp
)

func getJTIRe() *regexp.Regexp {
	jtiOnce.Do(func() {
		jtiRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	})
	return jtiRe
}

// revokeRequest is the JSON-decoded body. Validation per data-model §5.
type revokeRequest struct {
	JTI                  string `json:"jti"`
	Nonce                string `json:"nonce"`
	Timestamp            string `json:"timestamp"`
	RequestID            string `json:"request_id,omitempty"`
	MachineName          string `json:"machine_name,omitempty"`
	ClientKeyFingerprint string `json:"client_key_fingerprint"`
	Signature            string `json:"signature"`
}

// revokeSignedPayload is the canonical-JSON shape over which the client
// computes the ECDSA signature. Order is alphabetical via CanonicalJSON.
type revokeSignedPayload struct {
	ClientKeyFingerprint string `json:"client_key_fingerprint"`
	JTI                  string `json:"jti"`
	MachineName          string `json:"machine_name,omitempty"`
	Nonce                string `json:"nonce"`
	RequestID            string `json:"request_id,omitempty"`
	Timestamp            string `json:"timestamp"`
}

// revokeResponse is the success-path body — identical for first-time AND
// idempotent re-revocation per FR-014.
type revokeResponse struct {
	Revoked   bool   `json:"revoked"`
	RequestID string `json:"request_id"`
}

// handleRevoke is the SDD-13 entry point for `POST /h/<prefix>/revoke`.
//
//nolint:gocognit,gocyclo,cyclop,funlen // sequential pipeline: shape → fingerprint → verify → nonce → ts → revoke
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	requestID := RequestID(ctx)
	peer, _ := parseRemoteAddr(r.RemoteAddr)

	var req revokeRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadRequest, requestID, peer, "", "", "revoke_bad_request")
		writeStaticError(w, http.StatusBadRequest, errCodeRevokeBadRequest, requestID)
		return
	}
	if dec.More() {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadRequest, requestID, peer, "", "", "revoke_bad_request")
		writeStaticError(w, http.StatusBadRequest, errCodeRevokeBadRequest, requestID)
		return
	}

	// Field-level validation per data-model §5.
	if !getJTIRe().MatchString(req.JTI) {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadRequest, requestID, peer, req.JTI, "", "revoke_bad_request")
		writeStaticError(w, http.StatusBadRequest, errCodeRevokeBadRequest, requestID)
		return
	}
	if l := len(req.Nonce); l < 8 || l > 128 || !getNonceCharsRe().MatchString(req.Nonce) {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadRequest, requestID, peer, req.JTI, "", "revoke_bad_request")
		writeStaticError(w, http.StatusBadRequest, errCodeRevokeBadRequest, requestID)
		return
	}
	ts, parseErr := time.Parse(time.RFC3339Nano, req.Timestamp)
	if parseErr != nil {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadRequest, requestID, peer, req.JTI, "", "revoke_bad_request")
		writeStaticError(w, http.StatusBadRequest, errCodeRevokeBadRequest, requestID)
		return
	}
	if !getFingerprintRe().MatchString(req.ClientKeyFingerprint) {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadRequest, requestID, peer, req.JTI, "", "revoke_bad_request")
		writeStaticError(w, http.StatusBadRequest, errCodeRevokeBadRequest, requestID)
		return
	}
	if req.Signature == "" {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadRequest, requestID, peer, req.JTI, "", "revoke_bad_request")
		writeStaticError(w, http.StatusBadRequest, errCodeRevokeBadRequest, requestID)
		return
	}
	if req.RequestID != "" && !getRequestIDRe().MatchString(req.RequestID) {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadRequest, requestID, peer, req.JTI, "", "revoke_bad_request")
		writeStaticError(w, http.StatusBadRequest, errCodeRevokeBadRequest, requestID)
		return
	}
	if req.MachineName != "" && !getMachineNameRe().MatchString(req.MachineName) {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadRequest, requestID, peer, req.JTI, "", "revoke_bad_request")
		writeStaticError(w, http.StatusBadRequest, errCodeRevokeBadRequest, requestID)
		return
	}

	// Resolve client key by fingerprint. Unknown → bad_signature
	// (anti-enumeration; FR-015).
	pub, resolveErr := s.clientKeyResolver(req.ClientKeyFingerprint)
	if resolveErr != nil {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadSignature, requestID, peer, req.JTI, req.ClientKeyFingerprint, "revoke_bad_signature")
		writeStaticError(w, http.StatusForbidden, errCodeRevokeBadSignature, requestID)
		return
	}

	payload := revokeSignedPayload{
		ClientKeyFingerprint: req.ClientKeyFingerprint,
		JTI:                  req.JTI,
		MachineName:          req.MachineName,
		Nonce:                req.Nonce,
		RequestID:            req.RequestID,
		Timestamp:            req.Timestamp,
	}
	canonical, err := sign.CanonicalJSON(payload)
	if err != nil {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadSignature, requestID, peer, req.JTI, req.ClientKeyFingerprint, "revoke_bad_signature")
		writeStaticError(w, http.StatusForbidden, errCodeRevokeBadSignature, requestID)
		return
	}
	sigBytes, decodeErr := decodeRevokeSignature(req.Signature)
	if decodeErr != nil {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadSignature, requestID, peer, req.JTI, req.ClientKeyFingerprint, "revoke_bad_signature")
		writeStaticError(w, http.StatusForbidden, errCodeRevokeBadSignature, requestID)
		return
	}
	if verifyErr := sign.Verify(ctx, pub, canonical, sigBytes); verifyErr != nil {
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadSignature, requestID, peer, req.JTI, req.ClientKeyFingerprint, "revoke_bad_signature")
		writeStaticError(w, http.StatusForbidden, errCodeRevokeBadSignature, requestID)
		return
	}

	// Nonce + timestamp gates.
	firstSeen, nonceErr := s.nonceCache.Add(ctx, req.Nonce, s.cfg.Crypto.NonceTTL)
	if nonceErr != nil || !firstSeen {
		s.emitRevokeAudit(ctx, audit.ActionRevokeNonceReplay, requestID, peer, req.JTI, req.ClientKeyFingerprint, "revoke_nonce_replay")
		writeStaticError(w, http.StatusForbidden, errCodeRevokeNonceReplay, requestID)
		return
	}
	if !sign.IsFreshTimestamp(ts, s.cfg.Crypto.ClockSkew) {
		s.emitRevokeAudit(ctx, audit.ActionRevokeStaleTimestamp, requestID, peer, req.JTI, req.ClientKeyFingerprint, "revoke_stale_timestamp")
		writeStaticError(w, http.StatusForbidden, errCodeRevokeStaleTimestamp, requestID)
		return
	}

	existed, alreadyRevoked := s.tokenStore.RevokeIdempotent(req.JTI)
	if !existed {
		// Unknown JTI maps to bad_signature for anti-enumeration (FR-015).
		s.emitRevokeAudit(ctx, audit.ActionRevokeBadSignature, requestID, peer, req.JTI, req.ClientKeyFingerprint, "revoke_bad_signature")
		writeStaticError(w, http.StatusForbidden, errCodeRevokeBadSignature, requestID)
		return
	}

	action := audit.ActionRevokeSucceeded
	outcome := "revoke_succeeded"
	if alreadyRevoked {
		action = audit.ActionRevokeIdempotentAlreadyRevoked
		outcome = "revoke_idempotent_already_revoked"
	}
	s.emitRevokeAudit(ctx, action, requestID, peer, req.JTI, req.ClientKeyFingerprint, outcome)
	writeRevokeSuccess(w, requestID)
}

// writeRevokeSuccess writes the static success body. Identical for
// first-time and idempotent re-revoke per FR-014.
func writeRevokeSuccess(w http.ResponseWriter, requestID string) {
	body, _ := json.Marshal(revokeResponse{Revoked: true, RequestID: requestID}) //nolint:errchkjson // closed bool+string struct
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// emitRevokeAudit emits exactly one audit event for /revoke per FR-027.
func (s *Server) emitRevokeAudit(
	ctx context.Context,
	action string,
	requestID string,
	peer netip.Addr,
	jti string,
	fingerprint string,
	outcome string,
) {
	detail := buildRevokeAuditDetail(outcome, requestID, peer, jti, fingerprint)
	if err := s.audit.Write(ctx, AuditEvent{
		Type:      AuditEventType(action),
		At:        s.clock(),
		RequestID: requestID,
		ClientIP:  peer,
		Detail:    detail,
	}); err != nil {
		s.logger.WarnContext(ctx, "audit write failed",
			"action", action,
			"err", err.Error(),
		)
	}
}

// buildRevokeAuditDetail returns the allow-list Detail map for /revoke.
// NEVER carries the supplied signature, the supplied nonce, or the
// request body bytes (FR-029).
func buildRevokeAuditDetail(outcome, requestID string, peer netip.Addr, jti, fingerprint string) map[string]string {
	d := map[string]string{
		"outcome": outcome,
	}
	if requestID != "" {
		d["request_id"] = requestID
	}
	if peer.IsValid() {
		d["client_ip"] = peer.String()
	}
	if jti != "" {
		d["jti"] = jti
	}
	if fingerprint != "" {
		d["client_key_fingerprint"] = fingerprint
	}
	return d
}

// errRevokeBadBase64 reports a signature field that does not decode
// under any of the supported base64 forms.
var errRevokeBadBase64 = errors.New("revoke: signature not a recognized base64 encoding")

// decodeRevokeSignature accepts standard and url-safe base64 forms (with
// or without padding), matching the /claim handler's tolerance.
func decodeRevokeSignature(s string) ([]byte, error) {
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
	return nil, errRevokeBadBase64
}
