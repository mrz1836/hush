package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// meBodyOpts mirrors claimBodyOpts but for /me's narrower payload.
type meBodyOpts struct {
	Nonce               string
	Timestamp           time.Time
	RequestID           string
	MachineName         string
	Fingerprint         string
	SignWithKey         *ecdsa.PrivateKey
	CorruptSignature    bool
	FingerprintOverride string
}

// defaultMeOpts populates a fresh, valid set of /me request fields.
func defaultMeOpts(h *claimTestHarness) meBodyOpts {
	return meBodyOpts{
		Nonce:       freshNonce(),
		Timestamp:   time.Now(),
		RequestID:   "rq_" + freshNonce(),
		MachineName: "starbird.local",
		Fingerprint: h.fingerprint,
	}
}

// signedMeBody mirrors signedClaimBody. Signs over the meSignedPayload
// field set.
func signedMeBody(t *testing.T, h *claimTestHarness, o meBodyOpts) []byte {
	t.Helper()
	signKey := o.SignWithKey
	if signKey == nil {
		signKey = h.clientPriv
	}
	ts := o.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	payload := meSignedPayload{
		MachineName: o.MachineName,
		Nonce:       o.Nonce,
		RequestID:   o.RequestID,
		Timestamp:   ts.Format(time.RFC3339Nano),
	}
	canonical, err := sign.CanonicalJSON(payload)
	require.NoError(t, err)
	sig, err := sign.Sign(t.Context(), signKey, canonical)
	require.NoError(t, err)
	sigB64 := base64.StdEncoding.EncodeToString(sig)
	if o.CorruptSignature {
		sigB64 = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xAA}, 64))
	}
	fp := o.Fingerprint
	if o.FingerprintOverride != "" {
		fp = o.FingerprintOverride
	}
	body := map[string]any{
		"nonce":                  o.Nonce,
		"timestamp":              ts.Format(time.RFC3339Nano),
		"signature":              sigB64,
		"request_id":             o.RequestID,
		"machine_name":           o.MachineName,
		"client_key_fingerprint": fp,
	}
	out, err := json.Marshal(body)
	require.NoError(t, err)
	return out
}

// doMe sends a signed /me request and returns the recorder. The
// per-request chassis ID is stored in the context but tests do not
// currently assert correlation, so it's not surfaced.
func doMe(t *testing.T, h *claimTestHarness, body []byte, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/h/abcdef/me", bytes.NewReader(body))
	r.RemoteAddr = h.clientIP
	r.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	h.server.handleMe(rr, r)
	return rr
}

// ----- Happy paths --------------------------------------------------

func TestMe_OK_NoBearer(t *testing.T) {
	h := newClaimHarness(t)
	body := signedMeBody(t, h, defaultMeOpts(h))

	rr := doMe(t, h, body, "")
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp meResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, 1, resp.SchemaVersion)
	assert.NotEmpty(t, resp.ServerVersion, "server_version should default to 'dev' when ldflags unset")
	assert.Equal(t, []string{"A"}, resp.ScopesAvailable)
	assert.Nil(t, resp.CurrentSession, "no bearer → no current_session")
}

func TestMe_OK_WithBearer(t *testing.T) {
	// The shared claim harness omits JWTVerifyKey by default (the
	// claim path doesn't need it server-side — the issuer signs);
	// /me uses it to verify the bearer, so we wire it explicitly.
	var jwtKey *ecdsa.PrivateKey
	h := newClaimHarness(t, withDepsMutator(func(d *Deps) {
		jwtKey = claimTestKey(t)
		d.JWTVerifyKey = &jwtKey.PublicKey
	}))

	tok, err := token.Issue(t.Context(), jwtKey, token.IssueParams{
		Now:             time.Now(),
		TTL:             10 * time.Minute,
		Scope:           []string{"A"},
		ClientIP:        "100.64.1.5",
		RequestID:       "rq-test",
		MaxUses:         3,
		EphemeralPubKey: testEphemeralPubKeyHex(),
		SessionType:     token.SessionInteractive,
	})
	require.NoError(t, err)
	require.NoError(t, h.server.tokenStore.Add(tok))

	body := signedMeBody(t, h, defaultMeOpts(h))
	rr := doMe(t, h, body, tok.Encoded)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

	var resp meResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotNil(t, resp.CurrentSession, "valid bearer → current_session populated; body=%s", rr.Body.String())
	assert.Equal(t, tok.JTI, resp.CurrentSession.JTI)
	assert.Equal(t, []string{"A"}, resp.CurrentSession.Scopes)
	assert.Equal(t, 3, resp.CurrentSession.MaxUses)
	assert.Equal(t, "interactive", resp.CurrentSession.SessionType)
}

func TestMe_DoesNotTriggerApproval(t *testing.T) {
	h := newClaimHarness(t)
	body := signedMeBody(t, h, defaultMeOpts(h))
	doMe(t, h, body, "")
	doMe(t, h, signedMeBody(t, h, defaultMeOpts(h)), "")

	assert.Empty(t, h.approver.calls, "/me must never call the approver")
}

func TestMe_AuditEmitted_OnSuccess(t *testing.T) {
	h := newClaimHarness(t)
	body := signedMeBody(t, h, defaultMeOpts(h))
	doMe(t, h, body, "")

	events := h.auditEvents()
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, AuditMeQuery, last.Type)
	assert.Equal(t, "ok", last.Detail["outcome"])
}

// ----- Failure paths ------------------------------------------------

func TestMe_MalformedJSON_400(t *testing.T) {
	h := newClaimHarness(t)
	rr := doMe(t, h, []byte("not json"), "")
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	var errResp errorResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &errResp))
	assert.Equal(t, errCodeMeBadRequest, errResp.Error)
}

func TestMe_BadSignature_403(t *testing.T) {
	h := newClaimHarness(t)
	opts := defaultMeOpts(h)
	opts.CorruptSignature = true
	body := signedMeBody(t, h, opts)

	rr := doMe(t, h, body, "")
	assert.Equal(t, http.StatusForbidden, rr.Code)

	var errResp errorResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &errResp))
	assert.Equal(t, errCodeMeBadSignature, errResp.Error)
}

func TestMe_UnknownFingerprint_403(t *testing.T) {
	h := newClaimHarness(t)
	opts := defaultMeOpts(h)
	opts.FingerprintOverride = "0123456789abcdef"
	body := signedMeBody(t, h, opts)

	rr := doMe(t, h, body, "")
	assert.Equal(t, http.StatusForbidden, rr.Code)

	var errResp errorResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &errResp))
	// Unknown fingerprint maps to bad_signature so callers cannot
	// enumerate enrolled clients via error-code variation.
	assert.Equal(t, errCodeMeBadSignature, errResp.Error)
}

func TestMe_NonceReplay_403(t *testing.T) {
	h := newClaimHarness(t)
	opts := defaultMeOpts(h)
	body := signedMeBody(t, h, opts)

	rr1 := doMe(t, h, body, "")
	require.Equal(t, http.StatusOK, rr1.Code, "first request must succeed; body=%s", rr1.Body.String())

	// Identical body → replayed nonce.
	rr2 := doMe(t, h, body, "")
	require.Equal(t, http.StatusForbidden, rr2.Code)

	var errResp errorResponse
	require.NoError(t, json.Unmarshal(rr2.Body.Bytes(), &errResp))
	assert.Equal(t, errCodeMeNonceReplay, errResp.Error)
}

func TestMe_StaleTimestamp_403(t *testing.T) {
	h := newClaimHarness(t)
	opts := defaultMeOpts(h)
	opts.Timestamp = time.Now().Add(-2 * time.Hour) // well outside default clock skew
	body := signedMeBody(t, h, opts)

	rr := doMe(t, h, body, "")
	assert.Equal(t, http.StatusForbidden, rr.Code)

	var errResp errorResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &errResp))
	assert.Equal(t, errCodeMeStaleTS, errResp.Error)
}

func TestMe_InvalidBearer_StillReturnsScopes(t *testing.T) {
	// /me must not fail when the bearer is malformed — it just omits
	// the current_session field. This is the documented contract.
	h := newClaimHarness(t)
	body := signedMeBody(t, h, defaultMeOpts(h))

	rr := doMe(t, h, body, "not.a.real.jwt")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp meResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Nil(t, resp.CurrentSession)
	assert.Equal(t, []string{"A"}, resp.ScopesAvailable)
}

func TestMe_ServerVersion_FromDeps(t *testing.T) {
	h := newClaimHarness(t, withDepsMutator(func(d *Deps) {
		d.ServerVersion = "0.42.0"
	}))
	body := signedMeBody(t, h, defaultMeOpts(h))

	rr := doMe(t, h, body, "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp meResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "0.42.0", resp.ServerVersion)
}
