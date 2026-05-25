package client

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// MeRequest carries the inputs to a /me query.
//
// ServerURL is the canonical vault-server URL, including the random
// path prefix (e.g. "http://100.64.0.1:7743/h/abcd1234"). Discover it
// via `hush server-url` on the agent host.
//
// ClientKey is the per-machine secp256k1 ECDSA signing key the agent
// was enrolled with. The fingerprint is derived from its public half.
//
// BearerJWT is optional; when present the server populates the
// CurrentSession field of the response. An invalid bearer does not
// fail the request — the response simply omits CurrentSession.
//
// HTTPClient defaults to http.DefaultClient when nil. Tests inject a
// stub to drive failure modes.
//
// MachineName is included in the signed payload for parity with
// /claim. A free-form identifier (e.g. os.Hostname). Required.
//
// RequestID is an optional client-generated identifier echoed back in
// the audit log. When empty the SDK generates one.
type MeRequest struct {
	ServerURL   string
	ClientKey   *ecdsa.PrivateKey
	BearerJWT   string
	HTTPClient  *http.Client
	MachineName string
	RequestID   string
}

// MeResponse is the parsed /me response.
//
// NextRefreshWindow is currently always zero — the vault server has no
// refresh-window concept. Use SupervisorStatus.Snapshot to read the
// supervisor's next refresh window when the agent runs under
// `hush supervise`.
type MeResponse struct {
	SchemaVersion     int
	ServerVersion     string
	ScopesAvailable   []string
	CurrentSession    *MeSession
	NextRefreshWindow time.Time
}

// MeSession describes the bearer-authenticated session, populated only
// when MeRequest.BearerJWT is set and the JWT validates.
type MeSession struct {
	JTI         string
	ExpiresAt   time.Time
	Scopes      []string
	MaxUses     int
	SessionType string
}

// ErrUnauthenticated is returned when the server rejects the signed
// request — typically because the client fingerprint is not enrolled
// or the signature does not verify.
var ErrUnauthenticated = errors.New("hush/client: /me request rejected by server")

// errBadServerURLScheme is the sentinel underlying ErrInvalidResponse
// when MeRequest.ServerURL uses a scheme other than http/https.
var errBadServerURLScheme = errors.New("hush/client: ServerURL scheme must be http or https")

// Me queries the vault server's /me endpoint and returns the parsed
// response. The call does NOT trigger a Discord approval; it is safe
// to invoke repeatedly for planning purposes.
//
// Errors returned wrap one of:
//   - ErrSocketUnavailable: HTTP transport failure or context cancel.
//   - ErrInvalidResponse: server returned a non-2xx status or
//     unparseable body.
//   - ErrUnauthenticated: server returned 401/403 (bad signature,
//     unknown fingerprint, stale timestamp, replayed nonce, or
//     non-Tailscale source IP).
func Me(ctx context.Context, req MeRequest) (*MeResponse, error) {
	if err := validateMeRequest(req); err != nil {
		return nil, err
	}
	endpoint, err := joinMeURL(req.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("%w: ServerURL invalid: %w", ErrInvalidResponse, err)
	}
	body, err := buildSignedMeBody(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%w: build payload: %w", ErrInvalidResponse, err)
	}
	respBytes, err := doMeHTTP(ctx, endpoint, body, req)
	if err != nil {
		return nil, err
	}
	var wire meResponseWire
	if err := json.Unmarshal(respBytes, &wire); err != nil {
		return nil, fmt.Errorf("%w: parse: %w", ErrInvalidResponse, err)
	}
	return wire.toMeResponse()
}

// validateMeRequest enforces the required-field contract.
func validateMeRequest(req MeRequest) error {
	switch {
	case req.ServerURL == "":
		return fmt.Errorf("%w: ServerURL required", ErrInvalidResponse)
	case req.ClientKey == nil:
		return fmt.Errorf("%w: ClientKey required", ErrInvalidResponse)
	case req.MachineName == "":
		return fmt.Errorf("%w: MachineName required", ErrInvalidResponse)
	}
	return nil
}

// doMeHTTP issues the HTTP request and returns either the response
// bytes (on a 2xx) or the appropriate typed error.
func doMeHTTP(ctx context.Context, endpoint string, body []byte, req MeRequest) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", ErrSocketUnavailable, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.BearerJWT != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.BearerJWT)
	}
	httpClient := req.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: do request: %w", ErrSocketUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %w", ErrSocketUnavailable, err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: status=%d body=%s", ErrUnauthenticated, resp.StatusCode, summarizeBody(respBytes))
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%w: status=%d body=%s", ErrInvalidResponse, resp.StatusCode, summarizeBody(respBytes))
	}
	return respBytes, nil
}

// joinMeURL appends "/me" to base, preserving any existing path prefix.
func joinMeURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%w: got %q", errBadServerURLScheme, u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/me"
	return u.String(), nil
}

// buildSignedMeBody constructs the JSON request body, generating a
// fresh nonce, RFC3339Nano timestamp, and ECDSA signature over the
// canonical-JSON of the signed-payload field set.
func buildSignedMeBody(ctx context.Context, req MeRequest) ([]byte, error) {
	nonce, err := newNonce()
	if err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	requestID := req.RequestID
	if requestID == "" {
		requestID, err = newRequestID()
		if err != nil {
			return nil, fmt.Errorf("request_id: %w", err)
		}
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	fingerprint := keys.PublicKeyFingerprint(&req.ClientKey.PublicKey)

	signed := meSignedPayloadWire{
		MachineName: req.MachineName,
		Nonce:       nonce,
		RequestID:   requestID,
		Timestamp:   ts,
	}
	canonical, err := sign.CanonicalJSON(signed)
	if err != nil {
		return nil, fmt.Errorf("canonical: %w", err)
	}
	sigBytes, err := sign.Sign(ctx, req.ClientKey, canonical)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}

	body := meRequestWire{
		Nonce:                nonce,
		Timestamp:            ts,
		Signature:            base64.StdEncoding.EncodeToString(sigBytes),
		RequestID:            requestID,
		MachineName:          req.MachineName,
		ClientKeyFingerprint: fingerprint,
	}
	return json.Marshal(body)
}

// newNonce returns a 22-character base64url-encoded random nonce
// (16 bytes of entropy, matching the server's nonce_chars regex).
func newNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// newRequestID returns a 32-character hex string suitable for
// request_id (matches the server's ^[A-Za-z0-9_-]{16,64}$ regex).
func newRequestID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// summarizeBody trims response bodies for inclusion in error messages.
func summarizeBody(b []byte) string {
	const maxLen = 256
	s := strings.TrimSpace(string(b))
	if len(s) > maxLen {
		return s[:maxLen] + "…"
	}
	return s
}

// meRequestWire is the JSON-encoded request body.
type meRequestWire struct {
	Nonce                string `json:"nonce"`
	Timestamp            string `json:"timestamp"`
	Signature            string `json:"signature"`
	RequestID            string `json:"request_id"`
	MachineName          string `json:"machine_name"`
	ClientKeyFingerprint string `json:"client_key_fingerprint"`
}

// meSignedPayloadWire mirrors the server's signed-payload field set.
// CanonicalJSON sorts fields alphabetically by tag; keep names in
// lockstep with internal/server/me_handler.go::meSignedPayload.
type meSignedPayloadWire struct {
	MachineName string `json:"machine_name"`
	Nonce       string `json:"nonce"`
	RequestID   string `json:"request_id"`
	Timestamp   string `json:"timestamp"`
}

// meResponseWire is the JSON-decoded response.
type meResponseWire struct {
	SchemaVersion     int                `json:"schema_version"`
	ServerVersion     string             `json:"server_version"`
	ScopesAvailable   []string           `json:"scopes_available"`
	CurrentSession    *meSessionViewWire `json:"current_session,omitempty"`
	NextRefreshWindow string             `json:"next_refresh_window,omitempty"`
}

// meSessionViewWire mirrors internal/server/me_handler.go::meSessionView.
type meSessionViewWire struct {
	JTI         string   `json:"jti"`
	ExpiresAt   string   `json:"expires_at"`
	Scopes      []string `json:"scopes"`
	MaxUses     int      `json:"max_uses"`
	SessionType string   `json:"session_type"`
}

func (w *meResponseWire) toMeResponse() (*MeResponse, error) {
	resp := &MeResponse{
		SchemaVersion:   w.SchemaVersion,
		ServerVersion:   w.ServerVersion,
		ScopesAvailable: w.ScopesAvailable,
	}
	if w.NextRefreshWindow != "" {
		t, err := time.Parse(time.RFC3339, w.NextRefreshWindow)
		if err != nil {
			return nil, fmt.Errorf("%w: next_refresh_window: %w", ErrInvalidResponse, err)
		}
		resp.NextRefreshWindow = t
	}
	if w.CurrentSession != nil {
		sess := &MeSession{
			JTI:         w.CurrentSession.JTI,
			Scopes:      w.CurrentSession.Scopes,
			MaxUses:     w.CurrentSession.MaxUses,
			SessionType: w.CurrentSession.SessionType,
		}
		if w.CurrentSession.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, w.CurrentSession.ExpiresAt)
			if err != nil {
				return nil, fmt.Errorf("%w: expires_at: %w", ErrInvalidResponse, err)
			}
			sess.ExpiresAt = t
		}
		resp.CurrentSession = sess
	}
	return resp, nil
}
