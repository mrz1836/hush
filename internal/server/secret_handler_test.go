package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// secretTestHarness drives the /s handler.
type secretTestHarness struct {
	t          *testing.T
	server     *Server
	audit      *recordingAudit
	store      *secretFakeStore
	tokenStore token.Store
	jwtPriv    *ecdsa.PrivateKey
	ephPriv    *ecdsa.PrivateKey
	clientIP   string
	slogBuf    *bytes.Buffer
}

// secretFakeStore is a vault.Store that returns configurable bytes (or a
// configurable error) for Get(name).
type secretFakeStore struct {
	values    map[string][]byte
	getErr    error
	destroyed bool
}

func (f *secretFakeStore) Get(name string) (*securebytes.SecureBytes, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	v, ok := f.values[name]
	if !ok {
		return nil, vault.ErrSecretNotFound
	}
	return securebytes.New(append([]byte(nil), v...))
}

func (f *secretFakeStore) Names() []string {
	out := make([]string, 0, len(f.values))
	for k := range f.values {
		out = append(out, k)
	}
	return out
}

func (f *secretFakeStore) Destroy() error { f.destroyed = true; return nil }

// newSecretHarness builds a chassis Server with a real token store and a
// fake vault. The caller supplies values to seed and can override
// behaviour via the option funcs.
func newSecretHarness(t *testing.T, vaultValues map[string][]byte, mods ...func(*Deps)) *secretTestHarness {
	t.Helper()

	cfg := testCfg(t)
	logger, slogBuf := captureClaimLogger(t)
	auditRec := &recordingAudit{}

	jwtPriv, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck // secp256k1 not in crypto/ecdh
	if err != nil {
		t.Fatalf("generate jwt key: %v", err)
	}
	ephPriv, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck // secp256k1 not in crypto/ecdh
	if err != nil {
		t.Fatalf("generate eph key: %v", err)
	}

	store := &secretFakeStore{values: vaultValues}
	initial := vault.Store(store)
	var ptr atomic.Pointer[vault.Store]
	ptr.Store(&initial)

	tokenStore := token.NewStore()

	deps := Deps{
		Cfg:             cfg,
		VaultPtr:        &ptr,
		TokenStore:      tokenStore,
		TokenIssuer:     noopTokenIssuer,
		JWTVerifyKey:    &jwtPriv.PublicKey,
		Approver:        &fakeApprover{},
		Logger:          logger,
		AuditWriter:     auditRec,
		Clock:           time.Now,
		ClockSyncProbe:  alwaysSyncedClockProbe,
		InterfaceLister: stubInterfaceLister(cfg.Server.ListenAddr.Addr()),
	}
	for _, m := range mods {
		m(&deps)
	}

	srv, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return &secretTestHarness{
		t:          t,
		server:     srv,
		audit:      auditRec,
		store:      store,
		tokenStore: tokenStore,
		jwtPriv:    jwtPriv,
		ephPriv:    ephPriv,
		clientIP:   "100.64.1.5:43210",
		slogBuf:    slogBuf,
	}
}

// issueToken mints a real JWT under the harness JWT key with the supplied
// claim shape. Adds it to the token store.
func (h *secretTestHarness) issueToken(t *testing.T, scope []string, sessionType token.SessionType, maxUses int, ttl time.Duration, ephPubHex string) (jti, encoded string) {
	t.Helper()
	clientIP := strings.Split(h.clientIP, ":")[0]
	tok, err := token.Issue(t.Context(), h.jwtPriv, token.IssueParams{
		Now:             time.Now(),
		TTL:             ttl,
		Scope:           scope,
		ClientIP:        clientIP,
		RequestID:       "rq_" + freshNonceForSecretTest(),
		MaxUses:         maxUses,
		EphemeralPubKey: ephPubHex,
		SessionType:     sessionType,
	})
	if err != nil {
		t.Fatalf("token.Issue: %v", err)
	}
	if err := h.tokenStore.Add(tok); err != nil {
		t.Fatalf("tokenStore.Add: %v", err)
	}
	return tok.JTI, tok.Encoded
}

// do invokes handleSecret directly with a context carrying a chassis
// request ID.
func (h *secretTestHarness) do(t *testing.T, name, bearer string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/s/"+name, nil)
	r.SetPathValue("name", name)
	r.RemoteAddr = h.clientIP
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	h.server.handleSecret(rr, r)
	return rr, chassisID
}

// ephPubHex returns the 33-byte SEC1-compressed pubkey for the harness's
// ephemeral key, suitable for the EphemeralPubKey claim field.
func (h *secretTestHarness) ephPubHex() string {
	pub := h.ephPriv.PublicKey
	out := make([]byte, 33)
	if pub.Y.Bit(0) == 0 { //nolint:staticcheck // secp256k1 not in crypto/ecdh
		out[0] = 0x02
	} else {
		out[0] = 0x03
	}
	pub.X.FillBytes(out[1:]) //nolint:staticcheck // secp256k1 not in crypto/ecdh
	return hex.EncodeToString(out)
}

func freshNonceForSecretTest() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ---- Tests -----------------------------------------------------------------

func TestSecret_HappyPath_ECIESPayload(t *testing.T) {
	t.Parallel()
	plaintext := []byte("the-real-secret-value")
	h := newSecretHarness(t, map[string][]byte{"API_KEY": plaintext})
	jti, encoded := h.issueToken(t, []string{"API_KEY"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())

	rr, _ := h.do(t, "API_KEY", encoded)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type=%q want application/octet-stream", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q", got)
	}
	envelope := rr.Body.Bytes()
	if len(envelope) == 0 {
		t.Fatal("empty body")
	}

	// Body must NOT be the plaintext.
	if bytes.Contains(envelope, plaintext) {
		t.Fatal("response body contains plaintext bytes — ECIES envelope expected")
	}

	// Decrypt with the ephemeral private key — round-trip proof.
	sb, err := ecies.Decrypt(t.Context(), h.ephPriv, envelope)
	if err != nil {
		t.Fatalf("ecies.Decrypt: %v", err)
	}
	defer func() { _ = sb.Destroy() }()
	var got []byte
	if err := sb.Use(func(b []byte) { got = append([]byte(nil), b...) }); err != nil {
		t.Fatalf("sb.Use: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypted=%q want %q", got, plaintext)
	}

	// Token's MaxUses decremented to 0.
	tok, err := h.tokenStore.Get(jti)
	if err != nil {
		t.Fatalf("tokenStore.Get: %v", err)
	}
	if tok.MaxUses != 0 {
		t.Fatalf("MaxUses=%d after retrieval; want 0", tok.MaxUses)
	}

	// Exactly one audit event with action=secret_retrieved.
	events := h.audit.snapshot()
	if len(events) != 1 {
		t.Fatalf("audit events=%d want 1; %+v", len(events), events)
	}
	if got := string(events[0].Type); got != audit.ActionSecretRetrieved {
		t.Fatalf("audit type=%q want %q", got, audit.ActionSecretRetrieved)
	}
	if events[0].Detail["secret_name"] != "API_KEY" {
		t.Fatalf("audit secret_name=%q", events[0].Detail["secret_name"])
	}
	if _, has := events[0].Detail["secret_value"]; has {
		t.Fatal("audit Detail must NOT contain secret_value key")
	}
}

func TestSecret_SupervisorIgnoresMaxUses(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"API_KEY": []byte("v")})
	// Supervisor: pass 0 maxUses (issuer normalises to 0).
	_, encoded := h.issueToken(t, []string{"API_KEY"}, token.SessionSupervisor, 0, time.Hour, h.ephPubHex())

	for i := 0; i < 5; i++ {
		rr, _ := h.do(t, "API_KEY", encoded)
		if rr.Code != http.StatusOK {
			t.Fatalf("call #%d: status=%d body=%s", i, rr.Code, rr.Body.String())
		}
	}
	if len(h.audit.snapshot()) != 5 {
		t.Fatalf("audit events=%d want 5", len(h.audit.snapshot()))
	}
}

func TestSecret_ExpiredJWT_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})

	// Issue a token already expired by setting TTL to 1ns and waiting.
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Nanosecond, h.ephPubHex())
	time.Sleep(5 * time.Millisecond)

	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorCode(t, rr, "token_expired")
	assertOneAudit(t, h, audit.ActionSecretTokenExpired)
}

func TestSecret_OutOfScope_403(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"OTHER": []byte("v")})
	_, encoded := h.issueToken(t, []string{"ALLOWED"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())

	rr, _ := h.do(t, "OTHER", encoded)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorCode(t, rr, "out_of_scope")
	assertOneAudit(t, h, audit.ActionSecretOutOfScope)
}

func TestSecret_WrongIP_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	// Issue with current clientIP (100.64.1.5) but call from a different IP.
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())

	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/s/X", nil)
	r.SetPathValue("name", "X")
	r.RemoteAddr = "100.64.1.99:1234"
	r.Header.Set("Authorization", "Bearer "+encoded)
	rr := httptest.NewRecorder()
	h.server.handleSecret(rr, r)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorCode(t, rr, "bad_token")
	assertOneAudit(t, h, audit.ActionSecretBadToken)
}

func TestSecret_ExhaustedInteractive_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())

	// First call succeeds; MaxUses → 0.
	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusOK {
		t.Fatalf("first call status=%d", rr.Code)
	}
	// Second call → 401 bad_token.
	rr2, _ := h.do(t, "X", encoded)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("second status=%d want 401; body=%s", rr2.Code, rr2.Body.String())
	}
	assertErrorCode(t, rr2, "bad_token")
}

func TestSecret_RevokedJWT_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	jti, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	if err := h.tokenStore.Revoke(jti); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorCode(t, rr, "bad_token")
}

func TestSecret_MalformedJWT_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	rr, _ := h.do(t, "X", "not-a-real.jwt")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorCode(t, rr, "bad_token")
}

func TestSecret_BadSignature_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	// Issue a real token under a DIFFERENT JWT key, bypass tokenStore.Add
	// for the verify path — verify will fail before the store is consulted.
	otherKey, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck // secp256k1 not in crypto/ecdh
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tok, err := token.Issue(t.Context(), otherKey, token.IssueParams{
		Now:             time.Now(),
		TTL:             time.Hour,
		Scope:           []string{"X"},
		ClientIP:        strings.Split(h.clientIP, ":")[0],
		RequestID:       "rq_x12345678901234567",
		MaxUses:         1,
		EphemeralPubKey: h.ephPubHex(),
		SessionType:     token.SessionInteractive,
	})
	if err != nil {
		t.Fatalf("token.Issue: %v", err)
	}

	rr, _ := h.do(t, "X", tok.Encoded)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorCode(t, rr, "bad_token")
}

func TestSecret_MissingAuthHeader_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	rr, _ := h.do(t, "X", "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
	assertErrorCode(t, rr, "bad_token")
}

func TestSecret_UnsupportedScheme_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/s/X", nil)
	r.SetPathValue("name", "X")
	r.RemoteAddr = h.clientIP
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rr := httptest.NewRecorder()
	h.server.handleSecret(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
	assertErrorCode(t, rr, "bad_token")
}

func TestSecret_BadName_400(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	rr, _ := h.do(t, "lowercase_invalid", encoded)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
	assertErrorCode(t, rr, "bad_request")
	// vault must NOT have been consulted — no Names/Get ran.
}

func TestSecret_SecretMissingInVault_404(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{}) // vault empty
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorCode(t, rr, "not_found")
	assertOneAudit(t, h, audit.ActionSecretMissing)
}

func TestSecret_VaultReadError_500(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	h.store.getErr = errTestSynthetic
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorCode(t, rr, "internal_error")
	assertOneAudit(t, h, audit.ActionSecretInternalError)
}

func TestSecret_BadEphemeralPubKey_500(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	// Issue with a malformed ephemeral pubkey (66 hex chars but wrong prefix).
	bad := strings.Repeat("ff", 33) // not on the curve
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, bad)
	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorCode(t, rr, "internal_error")
}

func TestSecret_ErrorBodyNoSentinel(t *testing.T) {
	t.Parallel()
	sentinel := testutil.SentinelSecret(13)

	type row struct {
		name  string
		drive func(h *secretTestHarness) *httptest.ResponseRecorder
	}
	rows := []row{
		{"missing_header", func(h *secretTestHarness) *httptest.ResponseRecorder {
			rr, _ := h.do(t, "X", "")
			return rr
		}},
		{"bad_token_malformed", func(h *secretTestHarness) *httptest.ResponseRecorder {
			rr, _ := h.do(t, "X", "not.a.jwt")
			return rr
		}},
		{"bad_name", func(h *secretTestHarness) *httptest.ResponseRecorder {
			_, enc := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
			rr, _ := h.do(t, "lowercase", enc)
			return rr
		}},
		{"out_of_scope", func(h *secretTestHarness) *httptest.ResponseRecorder {
			_, enc := h.issueToken(t, []string{"OTHER"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
			rr, _ := h.do(t, "X", enc)
			return rr
		}},
		{"missing_in_vault", func(h *secretTestHarness) *httptest.ResponseRecorder {
			h.store.values = map[string][]byte{} // empty
			_, enc := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
			rr, _ := h.do(t, "X", enc)
			return rr
		}},
		{"vault_read_error", func(h *secretTestHarness) *httptest.ResponseRecorder {
			h.store.getErr = errTestSynthetic
			_, enc := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
			rr, _ := h.do(t, "X", enc)
			return rr
		}},
	}
	for _, rr := range rows {
		t.Run(rr.name, func(t *testing.T) {
			t.Parallel()
			// Plant the sentinel into the vault value AND log buffer
			// substrate so we can prove neither leaks.
			h := newSecretHarness(t, map[string][]byte{"X": []byte(sentinel)})
			out := rr.drive(h)
			testutil.AssertSentinelAbsent(t, sentinel, out.Body.String())
			testutil.AssertSentinelAbsent(t, sentinel, h.slogBuf.String())
			for _, ev := range h.audit.snapshot() {
				blob, _ := json.Marshal(ev.Detail)
				testutil.AssertSentinelAbsent(t, sentinel, string(blob))
			}
		})
	}
}

func TestSecret_NilJWTVerifyKey_500(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	h.server.jwtVerifyKey = nil
	rr, _ := h.do(t, "X", "anything")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rr.Code)
	}
	assertErrorCode(t, rr, "internal_error")
}

func TestSecret_NilVaultPtr_500(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	// Swap vault pointer to nil to simulate the early-startup race.
	var ptr atomic.Pointer[vault.Store]
	h.server.vaultPtr = &ptr // nil-loaded
	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestSecret_AuditWriteFailure_StillResponds(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	h.audit.err = errTestSynthetic
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 even when audit write fails", rr.Code)
	}
}

func TestSecret_BadEphemeralPubHexLen_500(t *testing.T) {
	t.Parallel()
	// /claim's regex is 66 hex chars; if a token slipped past with a
	// shorter hex-decodable value, decodeEphemeralPub returns an error
	// and the handler maps to 500. Drive this by minting a token with
	// a 64-hex-char string (a hex parse success but wrong length).
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	wrongLen := strings.Repeat("aa", 32) // 32 bytes, not 33
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, wrongLen)
	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rr.Code)
	}
}

func TestSecret_BadEphemeralPubHex_500(t *testing.T) {
	t.Parallel()
	// Non-hex characters in the pubkey field.
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, strings.Repeat("zz", 33))
	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rr.Code)
	}
}

func TestSecret_BadRemoteAddr_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/s/X", nil)
	r.SetPathValue("name", "X")
	r.RemoteAddr = "" // unparseable
	r.Header.Set("Authorization", "Bearer "+encoded)
	rr := httptest.NewRecorder()
	h.server.handleSecret(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
}

func TestSecret_BearerSchemeOnlyPrefix_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/s/X", nil)
	r.SetPathValue("name", "X")
	r.RemoteAddr = h.clientIP
	r.Header.Set("Authorization", "Bearer ")
	rr := httptest.NewRecorder()
	h.server.handleSecret(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
}

func TestSecret_BearerEmptyTokenAfterTrim_401(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/s/X", nil)
	r.SetPathValue("name", "X")
	r.RemoteAddr = h.clientIP
	r.Header.Set("Authorization", "Bearer        ")
	rr := httptest.NewRecorder()
	h.server.handleSecret(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
}

func TestSecret_CancelledContext_401(t *testing.T) {
	t.Parallel()
	// token.Validate returns ctx.Err() if ctx is already cancelled; the
	// handler maps this through the default branch of
	// respondSecretValidationError (fail-closed → bad_token).
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	chassisID := freshChassisID()
	ctx = context.WithValue(ctx, requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/s/X", nil)
	r.SetPathValue("name", "X")
	r.RemoteAddr = h.clientIP
	r.Header.Set("Authorization", "Bearer "+encoded)
	rr := httptest.NewRecorder()
	h.server.handleSecret(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
}

func TestSecret_ResponseWriteFailure_StillSucceeds(t *testing.T) {
	t.Parallel()
	// flushAfterPanicWriter panics on Write; the handler's deferred
	// destruction should still run and the panic propagates back to the
	// test.
	//
	// We verify the WARN-log path is reachable by using a writer that
	// returns an error from Write but does NOT panic.
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/s/X", nil)
	r.SetPathValue("name", "X")
	r.RemoteAddr = h.clientIP
	r.Header.Set("Authorization", "Bearer "+encoded)
	w := &errOnWriteResponseWriter{header: http.Header{}, status: 200}
	h.server.handleSecret(w, r)
	if w.status != http.StatusOK {
		t.Fatalf("status=%d want 200", w.status)
	}
	if w.writes == 0 {
		t.Fatal("Write not invoked")
	}
	// Audit event for retrieval still fired.
	if got := len(h.audit.snapshot()); got != 1 {
		t.Fatalf("audit events=%d want 1", got)
	}
}

// errOnWriteResponseWriter records header & status, returns an error from
// Write so the handler exercises its WARN-log branch.
type errOnWriteResponseWriter struct {
	header http.Header
	status int
	writes int
}

func (e *errOnWriteResponseWriter) Header() http.Header { return e.header }
func (e *errOnWriteResponseWriter) WriteHeader(s int)   { e.status = s }
func (e *errOnWriteResponseWriter) Write(b []byte) (int, error) {
	e.writes++
	return 0, errTestSynthetic
}

func TestSecret_ECIESEncryptError_500(t *testing.T) {
	// NOT parallel: this test mutates the package-level eciesEncryptFn
	// seam.
	prev := eciesEncryptFn
	eciesEncryptFn = func(_ context.Context, _ *ecdsa.PublicKey, _ []byte) ([]byte, error) {
		return nil, errTestSynthetic
	}
	t.Cleanup(func() { eciesEncryptFn = prev })

	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rr.Code)
	}
	assertErrorCode(t, rr, "internal_error")
	assertOneAudit(t, h, audit.ActionSecretInternalError)
}

func TestSecret_BodyPresent_400(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/s/X", bytes.NewReader([]byte("body")))
	r.SetPathValue("name", "X")
	r.RemoteAddr = h.clientIP
	r.Header.Set("Authorization", "Bearer "+encoded)
	rr := httptest.NewRecorder()
	h.server.handleSecret(rr, r)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
	assertErrorCode(t, rr, "bad_request")
}

// helpers

func assertErrorCode(t *testing.T, rr *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body unmarshal: %v (%s)", err, rr.Body.String())
	}
	if body.Error != want {
		t.Fatalf("error=%q want %q", body.Error, want)
	}
	if body.RequestID == "" {
		t.Fatal("request_id empty")
	}
}

func assertOneAudit(t *testing.T, h *secretTestHarness, wantAction string) {
	t.Helper()
	events := h.audit.snapshot()
	if len(events) != 1 {
		t.Fatalf("audit events=%d want 1; %+v", len(events), events)
	}
	if got := string(events[0].Type); got != wantAction {
		t.Fatalf("audit type=%q want %q", got, wantAction)
	}
}

// orderingAudit records call-ordering against a shared monotonic counter.
// Used to assert audit.Write happens before the first response-write call.
type orderingAudit struct {
	seq      *atomic.Int64
	auditAt  atomic.Int64
	auditErr error
}

func (a *orderingAudit) Write(_ context.Context, _ AuditEvent) error {
	a.auditAt.Store(a.seq.Add(1))
	return a.auditErr
}

// orderingResponseWriter wraps an httptest.ResponseRecorder and records the
// monotonic position of the first Write/WriteHeader call.
type orderingResponseWriter struct {
	rr      *httptest.ResponseRecorder
	seq     *atomic.Int64
	writeAt atomic.Int64
}

func (w *orderingResponseWriter) Header() http.Header { return w.rr.Header() }
func (w *orderingResponseWriter) WriteHeader(s int) {
	w.writeAt.CompareAndSwap(0, w.seq.Add(1))
	w.rr.WriteHeader(s)
}

func (w *orderingResponseWriter) Write(b []byte) (int, error) {
	w.writeAt.CompareAndSwap(0, w.seq.Add(1))
	return w.rr.Write(b)
}

// TestSecret_HappyPath_NoSentinelInArtifacts is the success-path
// counterpart to TestSecret_ErrorBodyNoSentinel: the secret value is
// the sentinel; the ECIES envelope must not contain it as plaintext,
// and the audit Detail map + slog buffer must not contain it at all.
// Pins the redaction contract on the 200-OK path so a future refactor
// that accidentally logs the secret value or copies it into audit
// detail is caught immediately.
func TestSecret_HappyPath_NoSentinelInArtifacts(t *testing.T) {
	t.Parallel()
	sentinel := testutil.SentinelSecret(13)
	h := newSecretHarness(t, map[string][]byte{"X": []byte(sentinel)})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())

	rr, _ := h.do(t, "X", encoded)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte(sentinel)) {
		t.Fatal("response body contains plaintext sentinel — ECIES envelope expected")
	}
	testutil.AssertSentinelAbsent(t, sentinel, h.slogBuf.String())
	for _, ev := range h.audit.snapshot() {
		blob, _ := json.Marshal(ev.Detail)
		testutil.AssertSentinelAbsent(t, sentinel, string(blob))
	}
}

// TestSecret_AuditEmittedBeforeResponseWrite is a regression test for the
// audit-chain integrity invariant: a crash between WriteHeader and the
// audit append must not leave the client with the secret and the chain
// without a record. Pins handler ordering for /s.
func TestSecret_AuditEmittedBeforeResponseWrite(t *testing.T) {
	t.Parallel()
	var seq atomic.Int64
	auditRec := &orderingAudit{seq: &seq}
	h := newSecretHarness(t, map[string][]byte{"X": []byte("v")}, func(d *Deps) {
		d.AuditWriter = auditRec
	})
	_, encoded := h.issueToken(t, []string{"X"}, token.SessionInteractive, 1, time.Hour, h.ephPubHex())

	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/h/abcdef/s/X", nil)
	r.SetPathValue("name", "X")
	r.RemoteAddr = h.clientIP
	r.Header.Set("Authorization", "Bearer "+encoded)

	w := &orderingResponseWriter{rr: httptest.NewRecorder(), seq: &seq}
	h.server.handleSecret(w, r)

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

// silence unused
var (
	_ = big.NewInt
)
