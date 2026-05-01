package server

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/netip"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// eciesEncryptFn is the package-level seam over [ecies.Encrypt] used by
// the secret handler.  Replaceable in tests via export_test.go to drive
// the encrypt-failure path without invoking the real BIE1 envelope
// generator.
//
//nolint:gochecknoglobals // sentinel-class test seam; set-once at package load
var eciesEncryptFn = ecies.Encrypt

// encryptViaSecureBytes runs the ECIES encrypt step inside an `sb.Use`
// closure so the plaintext bytes are released back to securebytes
// immediately after the envelope materializes.  Returns the envelope
// bytes or a non-nil error (either from sb.Use or from the encrypt
// primitive).
func encryptViaSecureBytes(ctx context.Context, sb *securebytes.SecureBytes, pub *ecdsa.PublicKey) ([]byte, error) {
	var (
		envelope []byte
		encErr   error
	)
	if useErr := sb.Use(func(plaintext []byte) {
		env, e := eciesEncryptFn(ctx, pub, plaintext)
		if e != nil {
			encErr = e
			return
		}
		envelope = env
	}); useErr != nil {
		return nil, useErr
	}
	if encErr != nil {
		return nil, encErr
	}
	return envelope, nil
}

// Static error codes the secret handler returns in failure response bodies.
const (
	errCodeSecretBadToken     = "bad_token"
	errCodeSecretTokenExpired = "token_expired"
	errCodeSecretOutOfScope   = "out_of_scope"
	errCodeSecretNotFound     = "not_found"
	errCodeSecretBadRequest   = "bad_request"
	errCodeSecretInternal     = "internal_error"
)

// handleSecret is the SDD-13 entry point for `GET /h/<prefix>/s/<name>`.
// Pipeline: parse name → extract Bearer JWT → token.Validate → vault.Get →
// ecies.Encrypt → write octet-stream body. Every outcome emits exactly one
// audit event (FR-027). The response body NEVER carries the plaintext
// secret value (Constitution X / FR-005).
//
//nolint:gocognit,gocyclo,cyclop,funlen // sequential pipeline: name → token → vault → ECIES; complexity is structural
func (s *Server) handleSecret(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	requestID := RequestID(ctx)

	name := r.PathValue("name")
	if !getScopeNameRe().MatchString(name) {
		s.emitSecretAudit(ctx, audit.ActionSecretBadRequest, requestID, netip.Addr{}, "", "", "bad_request")
		writeStaticError(w, http.StatusBadRequest, errCodeSecretBadRequest, requestID)
		return
	}

	// Reject any request body — /s is GET; non-empty body is a request shape error.
	if r.Body != nil {
		if data, _ := io.ReadAll(io.LimitReader(r.Body, 1)); len(data) > 0 {
			s.emitSecretAudit(ctx, audit.ActionSecretBadRequest, requestID, netip.Addr{}, "", "", "bad_request")
			writeStaticError(w, http.StatusBadRequest, errCodeSecretBadRequest, requestID)
			return
		}
	}

	authz := r.Header.Get("Authorization")
	encoded, ok := extractBearer(authz)
	if !ok {
		s.emitSecretAudit(ctx, audit.ActionSecretBadToken, requestID, netip.Addr{}, name, "", "secret_bad_token")
		writeStaticError(w, http.StatusUnauthorized, errCodeSecretBadToken, requestID)
		return
	}

	peer, peerOK := parseRemoteAddr(r.RemoteAddr)
	if !peerOK {
		s.emitSecretAudit(ctx, audit.ActionSecretBadToken, requestID, netip.Addr{}, name, "", "secret_bad_token")
		writeStaticError(w, http.StatusUnauthorized, errCodeSecretBadToken, requestID)
		return
	}

	if s.jwtVerifyKey == nil {
		s.emitSecretAudit(ctx, audit.ActionSecretInternalError, requestID, peer, name, "", "secret_internal_error")
		writeStaticError(w, http.StatusInternalServerError, errCodeSecretInternal, requestID)
		return
	}

	claims, valErr := token.Validate(ctx, encoded, s.jwtVerifyKey, s.tokenStore, peer.String(), name)
	if valErr != nil {
		s.respondSecretValidationError(w, ctx, requestID, peer, name, valErr)
		return
	}

	store := s.vaultPtr.Load()
	if store == nil {
		s.emitSecretAudit(ctx, audit.ActionSecretInternalError, requestID, peer, name, string(claims.SessionType), "secret_internal_error")
		writeStaticError(w, http.StatusInternalServerError, errCodeSecretInternal, requestID)
		return
	}

	sb, err := (*store).Get(name)
	if err != nil {
		if errors.Is(err, vault.ErrSecretNotFound) {
			s.emitSecretAudit(ctx, audit.ActionSecretMissing, requestID, peer, name, string(claims.SessionType), "secret_missing")
			writeStaticError(w, http.StatusNotFound, errCodeSecretNotFound, requestID)
			return
		}
		s.emitSecretAudit(ctx, audit.ActionSecretInternalError, requestID, peer, name, string(claims.SessionType), "secret_internal_error")
		writeStaticError(w, http.StatusInternalServerError, errCodeSecretInternal, requestID)
		return
	}
	defer func() { _ = sb.Destroy() }()

	pub, decodeErr := decodeEphemeralPub(claims.EphemeralPubKey)
	if decodeErr != nil {
		s.emitSecretAudit(ctx, audit.ActionSecretInternalError, requestID, peer, name, string(claims.SessionType), "secret_internal_error")
		writeStaticError(w, http.StatusInternalServerError, errCodeSecretInternal, requestID)
		return
	}

	envelope, encryptErr := encryptViaSecureBytes(ctx, sb, pub)
	if encryptErr != nil {
		s.emitSecretAudit(ctx, audit.ActionSecretInternalError, requestID, peer, name, string(claims.SessionType), "secret_internal_error")
		writeStaticError(w, http.StatusInternalServerError, errCodeSecretInternal, requestID)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(envelope)))
	w.WriteHeader(http.StatusOK)
	if _, writeErr := w.Write(envelope); writeErr != nil { //nolint:gosec // G705: envelope is opaque ECIES bytes; Content-Type is octet-stream
		s.logger.WarnContext(ctx, "secret response write failed",
			"request_id", requestID,
			"err_class", "response_write_failed",
		)
		// Audit reflects the success path AND the write failure; spec
		// FR-027 requires exactly one event so we record the success
		// outcome (the secret is materialized; the wire-write failure is
		// a downstream concern).
	}

	s.emitSecretAudit(ctx, audit.ActionSecretRetrieved, requestID, peer, name, string(claims.SessionType), "secret_retrieved")
	s.logger.InfoContext(ctx, "secret retrieved",
		"request_id", requestID,
		"client_ip", peer.String(),
		"secret_name", name,
		"session_type", string(claims.SessionType),
		"outcome", "secret_retrieved",
	)
}

// respondSecretValidationError maps a token.Validate sentinel to the
// documented (status, code, audit-action) tuple per R-007.
func (s *Server) respondSecretValidationError(
	w http.ResponseWriter, ctx context.Context,
	requestID string, peer netip.Addr, name string, valErr error,
) {
	switch {
	case errors.Is(valErr, token.ErrTokenExpired):
		s.emitSecretAudit(ctx, audit.ActionSecretTokenExpired, requestID, peer, name, "", "secret_token_expired")
		writeStaticError(w, http.StatusUnauthorized, errCodeSecretTokenExpired, requestID)
	case errors.Is(valErr, token.ErrScopeViolation):
		s.emitSecretAudit(ctx, audit.ActionSecretOutOfScope, requestID, peer, name, "", "secret_out_of_scope")
		writeStaticError(w, http.StatusForbidden, errCodeSecretOutOfScope, requestID)
	case errors.Is(valErr, token.ErrIPMismatch),
		errors.Is(valErr, token.ErrTokenRevoked),
		errors.Is(valErr, token.ErrTokenExhausted),
		errors.Is(valErr, token.ErrSignatureInvalid),
		errors.Is(valErr, token.ErrTokenMalformed),
		errors.Is(valErr, token.ErrAlgorithmUnsupported),
		errors.Is(valErr, token.ErrUnknownSessionType):
		s.emitSecretAudit(ctx, audit.ActionSecretBadToken, requestID, peer, name, "", "secret_bad_token")
		writeStaticError(w, http.StatusUnauthorized, errCodeSecretBadToken, requestID)
	default:
		// Any unanticipated validation error → bad_token (fail-closed).
		s.emitSecretAudit(ctx, audit.ActionSecretBadToken, requestID, peer, name, "", "secret_bad_token")
		writeStaticError(w, http.StatusUnauthorized, errCodeSecretBadToken, requestID)
	}
}

// extractBearer pulls the encoded JWT out of an `Authorization: Bearer <t>`
// header. Scheme comparison is case-insensitive; everything else is
// preserved verbatim. Returns ("", false) on a missing header, an unknown
// scheme, or an empty token.
func extractBearer(h string) (string, bool) {
	if h == "" {
		return "", false
	}
	const schemeLen = len("Bearer ")
	if len(h) <= schemeLen {
		return "", false
	}
	if !strings.EqualFold(h[:schemeLen], "Bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(h[schemeLen:])
	if tok == "" {
		return "", false
	}
	return tok, true
}

// errEphemeralPubLen reports an ephemeral pubkey hex string that
// decodes to a length other than 33 bytes.
var errEphemeralPubLen = errors.New("server: secret: ephemeral pub length != 33")

// decodeEphemeralPub parses a 33-byte SEC1-compressed secp256k1 public key
// (66 hex chars, the format `/claim` writes into Claims.EphemeralPubKey).
func decodeEphemeralPub(s string) (*ecdsa.PublicKey, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode ephemeral pub hex: %w", err)
	}
	if len(raw) != 33 {
		return nil, fmt.Errorf("%w: got %d", errEphemeralPubLen, len(raw))
	}
	pub, err := secp256k1.ParsePubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse ephemeral pub: %w", err)
	}
	x, y := pub.X(), pub.Y()
	return &ecdsa.PublicKey{
		Curve: secp256k1.S256(), //nolint:staticcheck // secp256k1 not in crypto/ecdh
		X:     new(big.Int).SetBytes(x.Bytes()[:]),
		Y:     new(big.Int).SetBytes(y.Bytes()[:]),
	}, nil
}

// emitSecretAudit constructs the allow-list audit detail for /s and
// invokes the chassis audit writer. Empty session_type is omitted.
func (s *Server) emitSecretAudit(
	ctx context.Context,
	action string,
	requestID string,
	peer netip.Addr,
	name string,
	sessionType string,
	outcome string,
) {
	detail := buildSecretAuditDetail(outcome, requestID, peer, name, sessionType)
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

// buildSecretAuditDetail returns the allow-list Detail map. NEVER carries
// the secret value, the JWT, the ECIES envelope, the ephemeral pubkey,
// or the request signature (FR-028, FR-029, FR-030).
func buildSecretAuditDetail(outcome, requestID string, peer netip.Addr, name, sessionType string) map[string]string {
	d := map[string]string{
		"outcome": outcome,
	}
	if requestID != "" {
		d["request_id"] = requestID
	}
	if peer.IsValid() {
		d["client_ip"] = peer.String()
	}
	if name != "" {
		d["secret_name"] = name
	}
	if sessionType != "" {
		d["session_type"] = sessionType
	}
	return d
}

// writeStaticError writes the locked failure body shape for /s and
// /revoke: `{"error":"<code>","request_id":"<id>"}`. NEVER echoes any
// other field.
func writeStaticError(w http.ResponseWriter, status int, code, requestID string) {
	body, _ := json.Marshal(struct { //nolint:errchkjson // closed string-only struct: cannot fail
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}{Error: code, RequestID: requestID})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
