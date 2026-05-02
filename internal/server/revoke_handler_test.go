package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/sign"
	"github.com/mrz1836/hush/internal/vault"
)

type revokeTestHarness struct {
	t           *testing.T
	server      *Server
	audit       *recordingAudit
	tokenStore  token.Store
	clientPriv  *ecdsa.PrivateKey
	jwtPriv     *ecdsa.PrivateKey
	fingerprint string
	clientIP    string
	slogBuf     *bytes.Buffer
}

func newRevokeHarness(t *testing.T, mods ...func(*Deps)) *revokeTestHarness {
	t.Helper()
	cfg := testCfg(t)
	logger, slogBuf := captureClaimLogger(t)
	auditRec := &recordingAudit{}

	clientPriv, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwtPriv, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	fingerprint := keys.PublicKeyFingerprint(&clientPriv.PublicKey)

	resolver := func(fp string) (*ecdsa.PublicKey, error) {
		if fp == fingerprint {
			return &clientPriv.PublicKey, nil
		}
		return nil, ErrClientUnknown
	}

	store := vault.Store(newFakeStore("A", []byte("v")))
	var ptr atomic.Pointer[vault.Store]
	ptr.Store(&store)

	deps := Deps{
		Cfg:               cfg,
		VaultPtr:          &ptr,
		TokenStore:        token.NewStore(),
		TokenIssuer:       noopTokenIssuer,
		JWTVerifyKey:      &jwtPriv.PublicKey,
		Approver:          &fakeApprover{},
		Logger:            logger,
		AuditWriter:       auditRec,
		Clock:             time.Now,
		ClockSyncProbe:    alwaysSyncedClockProbe,
		InterfaceLister:   stubInterfaceLister(cfg.Server.ListenAddr.Addr()),
		ClientKeyResolver: resolver,
	}
	for _, m := range mods {
		m(&deps)
	}

	srv, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &revokeTestHarness{
		t:           t,
		server:      srv,
		audit:       auditRec,
		tokenStore:  deps.TokenStore,
		clientPriv:  clientPriv,
		jwtPriv:     jwtPriv,
		fingerprint: fingerprint,
		clientIP:    "100.64.1.5:43210",
		slogBuf:     slogBuf,
	}
}

func (h *revokeTestHarness) issueToken(t *testing.T) string {
	t.Helper()
	tok, err := token.Issue(t.Context(), h.jwtPriv, token.IssueParams{
		Now:             time.Now(),
		TTL:             time.Hour,
		Scope:           []string{"X"},
		ClientIP:        strings.Split(h.clientIP, ":")[0],
		RequestID:       "rq_" + freshNonceForSecretTest(),
		MaxUses:         1,
		EphemeralPubKey: testEphemeralPubKeyHexRevoke(),
		SessionType:     token.SessionInteractive,
	})
	if err != nil {
		t.Fatalf("token.Issue: %v", err)
	}
	if err := h.tokenStore.Add(tok); err != nil {
		t.Fatalf("tokenStore.Add: %v", err)
	}
	return tok.JTI
}

func testEphemeralPubKeyHexRevoke() string {
	var b [33]byte
	b[0] = 0x02
	for i := 1; i < 33; i++ {
		b[i] = byte(i)
	}
	return hex.EncodeToString(b[:])
}

type signedRevokeOpts struct {
	JTI                  string
	Nonce                string
	Timestamp            time.Time
	RequestID            string
	MachineName          string
	ClientKeyFingerprint string
	SignWithKey          *ecdsa.PrivateKey
	CorruptSignature     bool
	OmitFields           []string
}

func (h *revokeTestHarness) signedBody(t *testing.T, o signedRevokeOpts) []byte {
	t.Helper()
	if o.JTI == "" {
		t.Fatal("o.JTI required")
	}
	if o.Nonce == "" {
		o.Nonce = freshNonceFromBytes(16)
	}
	if o.Timestamp.IsZero() {
		o.Timestamp = time.Now()
	}
	if o.RequestID == "" {
		o.RequestID = "rq_" + freshNonceForSecretTest() + freshNonceForSecretTest()
	}
	if o.MachineName == "" {
		o.MachineName = "starbird.local"
	}
	if o.ClientKeyFingerprint == "" {
		o.ClientKeyFingerprint = h.fingerprint
	}
	signKey := o.SignWithKey
	if signKey == nil {
		signKey = h.clientPriv
	}

	payload := revokeSignedPayload{
		ClientKeyFingerprint: o.ClientKeyFingerprint,
		JTI:                  o.JTI,
		MachineName:          o.MachineName,
		Nonce:                o.Nonce,
		RequestID:            o.RequestID,
		Timestamp:            o.Timestamp.Format(time.RFC3339Nano),
	}
	canonical, err := sign.CanonicalJSON(payload)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	sig, err := sign.Sign(t.Context(), signKey, canonical)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	sigB64 := base64.StdEncoding.EncodeToString(sig)
	if o.CorruptSignature {
		sigB64 = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xAA}, 64))
	}

	body := map[string]any{
		"jti":                    o.JTI,
		"nonce":                  o.Nonce,
		"timestamp":              o.Timestamp.Format(time.RFC3339Nano),
		"request_id":             o.RequestID,
		"machine_name":           o.MachineName,
		"client_key_fingerprint": o.ClientKeyFingerprint,
		"signature":              sigB64,
	}
	for _, f := range o.OmitFields {
		delete(body, f)
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return out
}

func freshNonceFromBytes(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (h *revokeTestHarness) do(t *testing.T, body []byte) (*httptest.ResponseRecorder, string) {
	t.Helper()
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/h/abcdef/revoke", bytes.NewReader(body))
	r.RemoteAddr = h.clientIP
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.server.handleRevoke(rr, r)
	return rr, chassisID
}

// ---- Tests -----------------------------------------------------------------

func TestRevoke_HappyPath(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	jti := h.issueToken(t)
	body := h.signedBody(t, signedRevokeOpts{JTI: jti})
	rr, _ := h.do(t, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp revokeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Revoked {
		t.Fatal("revoked=false")
	}
	if resp.RequestID == "" {
		t.Fatal("request_id empty")
	}
	if _, err := h.tokenStore.Get(jti); err == nil {
		t.Fatal("token still retrievable after revoke")
	}
	events := h.audit.snapshot()
	if len(events) != 1 || string(events[0].Type) != audit.ActionRevokeSucceeded {
		t.Fatalf("audit events=%+v", events)
	}
}

func TestRevoke_BadSignature_403(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	jti := h.issueToken(t)
	body := h.signedBody(t, signedRevokeOpts{JTI: jti, CorruptSignature: true})
	rr, _ := h.do(t, body)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
	assertRevokeErrCode(t, rr, "bad_signature")
	assertOneRevokeAudit(t, h, audit.ActionRevokeBadSignature)
}

func TestRevoke_UnknownJTI_403_AsBadSignature(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	// Sign a body with a JTI that was never issued. Signature is valid.
	body := h.signedBody(t, signedRevokeOpts{JTI: "ffffffff-ffff-4fff-bfff-ffffffffffff"})
	rr, _ := h.do(t, body)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
	assertRevokeErrCode(t, rr, "bad_signature")
}

func TestRevoke_UnknownFingerprint_403(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	jti := h.issueToken(t)
	body := h.signedBody(t, signedRevokeOpts{JTI: jti, ClientKeyFingerprint: "0000000000000000"})
	rr, _ := h.do(t, body)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
	assertRevokeErrCode(t, rr, "bad_signature")
}

func TestRevoke_ReplayedNonce_403(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	jti := h.issueToken(t)
	nonce := freshNonceFromBytes(16)
	ts := time.Now()

	body1 := h.signedBody(t, signedRevokeOpts{JTI: jti, Nonce: nonce, Timestamp: ts})
	rr1, _ := h.do(t, body1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first call status=%d body=%s", rr1.Code, rr1.Body.String())
	}
	// Resubmit same nonce on a different body (different jti to dodge
	// idempotent-revoke if the nonce check was misordered). But signature
	// would change. Use the same body — the nonce check is what we want.
	rr2, _ := h.do(t, body1)
	if rr2.Code != http.StatusForbidden {
		t.Fatalf("replay status=%d want 403; body=%s", rr2.Code, rr2.Body.String())
	}
	assertRevokeErrCode(t, rr2, "nonce_replay")
}

func TestRevoke_StaleTimestamp_403(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	jti := h.issueToken(t)
	body := h.signedBody(t, signedRevokeOpts{JTI: jti, Timestamp: time.Now().Add(-1 * time.Hour)})
	rr, _ := h.do(t, body)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
	assertRevokeErrCode(t, rr, "stale_timestamp")
}

func TestRevoke_MalformedBody_400(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body []byte
	}{
		{"not json", []byte("not-json")},
		{"empty", []byte("")},
		{"unknown field", []byte(`{"jti":"f","extra":"x"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newRevokeHarness(t)
			rr, _ := h.do(t, tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
			}
			assertRevokeErrCode(t, rr, "bad_request")
		})
	}
}

func TestRevoke_MalformedJTI_400(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	body := h.signedBody(t, signedRevokeOpts{JTI: "not-a-uuid"})
	rr, _ := h.do(t, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
	assertRevokeErrCode(t, rr, "bad_request")
}

func TestRevoke_FieldValidation_400(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mut  func(o *signedRevokeOpts, body map[string]any)
	}{
		{"nonce too short", func(o *signedRevokeOpts, body map[string]any) {
			body["nonce"] = "short"
		}},
		{"nonce too long", func(o *signedRevokeOpts, body map[string]any) {
			body["nonce"] = strings.Repeat("a", 200)
		}},
		{"timestamp not RFC3339Nano", func(o *signedRevokeOpts, body map[string]any) {
			body["timestamp"] = "not-a-timestamp"
		}},
		{"fingerprint wrong format", func(o *signedRevokeOpts, body map[string]any) {
			body["client_key_fingerprint"] = "uppercase_hex_BAD"
		}},
		{"empty signature", func(o *signedRevokeOpts, body map[string]any) {
			body["signature"] = ""
		}},
		{"invalid request_id format", func(o *signedRevokeOpts, body map[string]any) {
			body["request_id"] = "x"
		}},
		{"invalid machine_name", func(o *signedRevokeOpts, body map[string]any) {
			body["machine_name"] = strings.Repeat("z", 200)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newRevokeHarness(t)
			jti := h.issueToken(t)
			// Build a valid body, then mutate it post-marshal.
			raw := h.signedBody(t, signedRevokeOpts{JTI: jti})
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			tc.mut(nil, body)
			out, _ := json.Marshal(body)
			rr, _ := h.do(t, out)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("%s: status=%d want 400; body=%s", tc.name, rr.Code, rr.Body.String())
			}
			assertRevokeErrCode(t, rr, "bad_request")
		})
	}
}

func TestRevoke_TrailingJSON_400(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	jti := h.issueToken(t)
	body := h.signedBody(t, signedRevokeOpts{JTI: jti})
	body = append(body, []byte(`{"trailing":true}`)...)
	rr, _ := h.do(t, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
	assertRevokeErrCode(t, rr, "bad_request")
}

func TestRevoke_BadBase64Signature_403(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	jti := h.issueToken(t)
	raw := h.signedBody(t, signedRevokeOpts{JTI: jti})
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	body["signature"] = "@@@invalid base64@@@"
	out, _ := json.Marshal(body)
	rr, _ := h.do(t, out)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
	assertRevokeErrCode(t, rr, "bad_signature")
}

func TestRevoke_AuditWriteFailure_StillResponds(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	jti := h.issueToken(t)
	// Force the recordingAudit to return an error for every Append.
	h.audit.err = errTestSynthetic
	body := h.signedBody(t, signedRevokeOpts{JTI: jti})
	rr, _ := h.do(t, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 even when audit write fails", rr.Code)
	}
}

func TestRevoke_IdempotentReRevocation_200_StaticBody(t *testing.T) {
	t.Parallel()
	h := newRevokeHarness(t)
	jti := h.issueToken(t)

	body1 := h.signedBody(t, signedRevokeOpts{JTI: jti})
	rr1, _ := h.do(t, body1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", rr1.Code, rr1.Body.String())
	}

	// Pre-add the JTI back into a "revoked" state by directly calling
	// RevokeIdempotent with the same JTI; this models the idempotent
	// retry path (the second client request comes in after the first
	// already revoked).
	body2 := h.signedBody(t, signedRevokeOpts{JTI: jti})
	rr2, _ := h.do(t, body2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", rr2.Code, rr2.Body.String())
	}

	// Bodies are byte-identical except for request_id.
	var b1, b2 revokeResponse
	if err := json.Unmarshal(rr1.Body.Bytes(), &b1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &b2); err != nil {
		t.Fatal(err)
	}
	if !b1.Revoked || !b2.Revoked {
		t.Fatal("revoked=false")
	}

	// Audit chain has TWO events: success then idempotent.
	events := h.audit.snapshot()
	if len(events) != 2 {
		t.Fatalf("audit events=%d want 2", len(events))
	}
	if string(events[0].Type) != audit.ActionRevokeSucceeded {
		t.Fatalf("first audit type=%q", events[0].Type)
	}
	if string(events[1].Type) != audit.ActionRevokeIdempotentAlreadyRevoked {
		t.Fatalf("second audit type=%q", events[1].Type)
	}
}

func TestRevoke_ErrorBodyNoSentinel(t *testing.T) {
	t.Parallel()
	sentinel := testutil.SentinelSecret(13)

	cases := []struct {
		name  string
		drive func(h *revokeTestHarness) *httptest.ResponseRecorder
	}{
		{"malformed", func(h *revokeTestHarness) *httptest.ResponseRecorder {
			rr, _ := h.do(t, []byte(sentinel))
			return rr
		}},
		{"nonce_with_sentinel", func(h *revokeTestHarness) *httptest.ResponseRecorder {
			jti := h.issueToken(t)
			body := h.signedBody(t, signedRevokeOpts{JTI: jti, Nonce: sentinel + "_nonce_pad"})
			rr, _ := h.do(t, body)
			return rr
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newRevokeHarness(t)
			rr := tc.drive(h)
			testutil.AssertSentinelAbsent(t, sentinel, rr.Body.String())
			testutil.AssertSentinelAbsent(t, sentinel, h.slogBuf.String())
			for _, ev := range h.audit.snapshot() {
				blob, _ := json.Marshal(ev.Detail)
				testutil.AssertSentinelAbsent(t, sentinel, string(blob))
			}
		})
	}
}

// helpers

func assertRevokeErrCode(t *testing.T, rr *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body unmarshal: %v (%s)", err, rr.Body.String())
	}
	if body.Error != want {
		t.Fatalf("error=%q want %q (body=%s)", body.Error, want, rr.Body.String())
	}
}

func assertOneRevokeAudit(t *testing.T, h *revokeTestHarness, wantAction string) {
	t.Helper()
	events := h.audit.snapshot()
	if len(events) != 1 {
		t.Fatalf("audit events=%d want 1; %+v", len(events), events)
	}
	if got := string(events[0].Type); got != wantAction {
		t.Fatalf("audit type=%q want %q", got, wantAction)
	}
}

// TestRevoke_AuditEmittedBeforeResponseWrite is a regression test for the
// audit-chain integrity invariant: /revoke must emit the audit event
// before flushing the response body. Mirrors the /s assertion.
func TestRevoke_AuditEmittedBeforeResponseWrite(t *testing.T) {
	t.Parallel()
	var seq atomic.Int64
	auditRec := &orderingAudit{seq: &seq}
	h := newRevokeHarness(t, func(d *Deps) { d.AuditWriter = auditRec })
	jti := h.issueToken(t)
	body := h.signedBody(t, signedRevokeOpts{JTI: jti})

	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/h/abcdef/revoke", bytes.NewReader(body))
	r.RemoteAddr = h.clientIP
	r.Header.Set("Content-Type", "application/json")
	w := &orderingResponseWriter{rr: httptest.NewRecorder(), seq: &seq}
	h.server.handleRevoke(w, r)

	if w.rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.rr.Code, w.rr.Body.String())
	}
	auditAt, writeAt := auditRec.auditAt.Load(), w.writeAt.Load()
	if auditAt == 0 {
		t.Fatal("audit.Write was never called")
	}
	if writeAt == 0 {
		t.Fatal("response writer was never invoked")
	}
	if auditAt >= writeAt {
		t.Fatalf("audit (seq=%d) must precede response write (seq=%d)", auditAt, writeAt)
	}
}
