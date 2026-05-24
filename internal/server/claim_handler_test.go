package server

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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/golang-jwt/jwt/v5"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/sign"
	"github.com/mrz1836/hush/internal/vault"
)

// ---- Harness ---------------------------------------------------------------

type recordingTokenIssuer struct {
	mu    sync.Mutex
	calls int
	fn    TokenIssuer
}

func (r *recordingTokenIssuer) Issue(ctx context.Context, params token.IssueParams) (*token.Token, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return r.fn(ctx, params)
}

func (r *recordingTokenIssuer) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type claimTestHarness struct {
	t           *testing.T
	server      *Server
	audit       *recordingAudit
	approver    *fakeApprover
	tokenIssuer *recordingTokenIssuer
	cfg         *config.Server
	clientPriv  *ecdsa.PrivateKey
	jwtPriv     *ecdsa.PrivateKey
	fingerprint string
	slogBuf     *bytes.Buffer
	clientIP    string // IP injected as request RemoteAddr
}

// claimTestKey is a fresh secp256k1 key used by the harness for both client
// signing and JWT signing. We generate a single key per harness for client
// signing and a second for JWT signing.
func claimTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck // secp256k1 unsupported by crypto/ecdh
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

type harnessOpt func(*claimHarnessConfig)

type claimHarnessConfig struct {
	cryptoMutator         func(*config.CryptoSection)
	approverDecisions     []Decision
	approverErrs          []error
	tokenIssuerOverride   TokenIssuer
	allowedCIDROverride   []string
	clientIPOverride      string
	additionalDepsMutator func(*Deps)
}

func withCrypto(fn func(*config.CryptoSection)) harnessOpt {
	return func(c *claimHarnessConfig) { c.cryptoMutator = fn }
}

func withApproverScript(decs []Decision, errs []error) harnessOpt {
	return func(c *claimHarnessConfig) {
		c.approverDecisions = decs
		c.approverErrs = errs
	}
}

func withTokenIssuer(ti TokenIssuer) harnessOpt {
	return func(c *claimHarnessConfig) { c.tokenIssuerOverride = ti }
}

func withAllowedCIDRs(cidrs []string) harnessOpt {
	return func(c *claimHarnessConfig) { c.allowedCIDROverride = cidrs }
}

func withClientIP(ip string) harnessOpt {
	return func(c *claimHarnessConfig) { c.clientIPOverride = ip }
}

func withDepsMutator(fn func(*Deps)) harnessOpt {
	return func(c *claimHarnessConfig) { c.additionalDepsMutator = fn }
}

func newClaimHarness(t *testing.T, opts ...harnessOpt) *claimTestHarness {
	t.Helper()

	cfg := claimTestCfg(t)
	cc := &claimHarnessConfig{}
	for _, o := range opts {
		o(cc)
	}
	if cc.cryptoMutator != nil {
		cc.cryptoMutator(&cfg.Crypto)
	}
	if cc.allowedCIDROverride != nil {
		cfg.Network.AllowedCIDRs = cc.allowedCIDROverride
	}

	clientPriv := claimTestKey(t)
	jwtPriv := claimTestKey(t)
	fingerprint := keys.PublicKeyFingerprint(&clientPriv.PublicKey)

	logger, slogBuf := captureClaimLogger(t)
	audit := &recordingAudit{}
	approver := &fakeApprover{
		decisions: cc.approverDecisions,
		errs:      cc.approverErrs,
	}

	tokIssuer := &recordingTokenIssuer{
		fn: func(ctx context.Context, params token.IssueParams) (*token.Token, error) {
			if cc.tokenIssuerOverride != nil {
				return cc.tokenIssuerOverride(ctx, params)
			}
			return token.Issue(ctx, jwtPriv, params)
		},
	}

	resolver := func(fp string) (*ecdsa.PublicKey, error) {
		if fp == fingerprint {
			return &clientPriv.PublicKey, nil
		}
		return nil, ErrClientUnknown
	}

	clock := time.Now

	initial := vault.Store(newFakeStore("A", []byte("vault-a")))
	var ptr atomic.Pointer[vault.Store]
	ptr.Store(&initial)

	deps := Deps{
		Cfg:               cfg,
		VaultPtr:          &ptr,
		TokenStore:        token.NewStore(),
		TokenIssuer:       tokIssuer.Issue,
		Approver:          approver,
		Logger:            logger,
		AuditWriter:       audit,
		Clock:             clock,
		ClockSyncProbe:    alwaysSyncedClockProbe,
		InterfaceLister:   stubInterfaceLister(cfg.Server.ListenAddr.Addr()),
		ClientKeyResolver: resolver,
	}
	if cc.additionalDepsMutator != nil {
		cc.additionalDepsMutator(&deps)
	}

	srv, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	clientIP := "100.64.1.5:43210"
	if cc.clientIPOverride != "" {
		clientIP = cc.clientIPOverride
	}

	return &claimTestHarness{
		t:           t,
		server:      srv,
		audit:       audit,
		approver:    approver,
		tokenIssuer: tokIssuer,
		cfg:         cfg,
		clientPriv:  clientPriv,
		jwtPriv:     jwtPriv,
		fingerprint: fingerprint,
		slogBuf:     slogBuf,
		clientIP:    clientIP,
	}
}

// captureClaimLogger returns a slog.Logger writing JSON into a buffer the
// caller can grep for sentinel values. JSON handler escapes embedded strings
// without changing their literal content, so a sentinel still appears as a
// substring of the rendered bytes.
func captureClaimLogger(_ *testing.T) (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// claimTestCfg builds a config.Server with every field the claim handler
// reads populated.
func claimTestCfg(t *testing.T) *config.Server {
	t.Helper()
	c := testCfg(t)
	// Tighten/relax claim-relevant fields for the test set.
	c.Crypto.MaxInteractiveTTL = 12 * time.Hour
	c.Crypto.MaxSupervisorTTL = 20 * time.Hour
	c.Crypto.JWTDefaultTTL = 8 * time.Hour
	c.Crypto.NonceTTL = 60 * time.Second
	c.Crypto.ClockSkew = 30 * time.Second
	c.Crypto.DefaultMaxUses = 50
	c.Crypto.ClaimApprovalTimeout = 60 * time.Second
	return c
}

// claimBodyOpts drives signedClaimBody. Sensible zero-value defaults are
// applied per field — set the few fields the test wants to override.
type claimBodyOpts struct {
	Scope           []string
	Reason          string
	TTL             time.Duration
	SessionType     string
	EphemeralPubKey string
	Nonce           string
	Timestamp       time.Time
	RequestID       string
	MachineName     string
	Fingerprint     string
	// SignWithKey lets tests sign with a different (incorrect) private key
	// to drive the bad_signature path.
	SignWithKey *ecdsa.PrivateKey
	// CorruptSignature replaces the signature with a fixed-but-invalid value.
	CorruptSignature bool
	// FingerprintOverride bypasses the harness fingerprint without
	// re-deriving from the key — used to test unknown-fingerprint case.
	FingerprintOverride string
}

func defaultClaimBodyOpts(h *claimTestHarness) claimBodyOpts {
	return claimBodyOpts{
		Scope:           []string{"ANTHROPIC_API_KEY"},
		Reason:          "test reason",
		TTL:             2 * time.Hour,
		SessionType:     "interactive",
		EphemeralPubKey: testEphemeralPubKeyHex(),
		Nonce:           freshNonce(),
		Timestamp:       time.Now(),
		RequestID:       "rq_" + freshNonce(),
		MachineName:     "starbird.local",
		Fingerprint:     h.fingerprint,
	}
}

func freshNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// testEphemeralPubKeyHex returns a deterministic 33-byte compressed pubkey
// hex string. The handler does not verify the curve point — only the regex
// shape — so any 66-char hex passes the shape stage.
func testEphemeralPubKeyHex() string {
	var b [33]byte
	b[0] = 0x02
	for i := 1; i < 33; i++ {
		b[i] = byte(i)
	}
	return hex.EncodeToString(b[:])
}

// signedClaimBody returns a JSON-encoded claim body with a real signature
// over canonical-JSON of the signed-payload field set, using the harness's
// client private key (or a caller-supplied alternative).
func signedClaimBody(t *testing.T, h *claimTestHarness, o claimBodyOpts) []byte {
	t.Helper()
	signKey := o.SignWithKey
	if signKey == nil {
		signKey = h.clientPriv
	}
	timestamp := o.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	payload := signedPayload{
		EphemeralPubKey: o.EphemeralPubKey,
		MachineName:     o.MachineName,
		Nonce:           o.Nonce,
		Reason:          o.Reason,
		RequestID:       o.RequestID,
		Scope:           o.Scope,
		SessionType:     o.SessionType,
		Timestamp:       timestamp.Format(time.RFC3339Nano),
		TTL:             o.TTL.String(),
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
		// 64 'A' bytes encoded in standard base64 — well-formed but
		// guaranteed-invalid against any real key.
		sigB64 = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xAA}, 64))
	}
	fp := o.Fingerprint
	if o.FingerprintOverride != "" {
		fp = o.FingerprintOverride
	}
	body := map[string]any{
		"scope":                  o.Scope,
		"reason":                 o.Reason,
		"ttl":                    o.TTL.String(),
		"session_type":           o.SessionType,
		"ephemeral_pubkey":       o.EphemeralPubKey,
		"nonce":                  o.Nonce,
		"timestamp":              timestamp.Format(time.RFC3339Nano),
		"signature":              sigB64,
		"request_id":             o.RequestID,
		"machine_name":           o.MachineName,
		"client_key_fingerprint": fp,
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return out
}

// do invokes handleClaim directly on the harness server. Wraps the request
// in a context carrying a deterministic chassis-assigned request ID so
// RequestID(ctx) returns a value. Returns the recorder + the chassis ID so
// tests can assert correlation.
func (h *claimTestHarness) do(t *testing.T, body []byte) (*httptest.ResponseRecorder, string) {
	t.Helper()
	chassisID := freshChassisID()
	ctx := context.WithValue(t.Context(), requestIDKey, chassisID)
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/h/abcdef/claim", bytes.NewReader(body))
	r.RemoteAddr = h.clientIP
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.server.handleClaim(rr, r)
	return rr, chassisID
}

func freshChassisID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// auditEvents returns the recorded audit events ordered by insertion.
func (h *claimTestHarness) auditEvents() []AuditEvent { return h.audit.snapshot() }

// ---- Phase 2 (Foundational) tests -----------------------------------------

// TestClaim_DefaultClientKeyResolver_LoadsRegistry — when nil, the chassis
// installs a default that loads cfg.Server.ClientRegistry.
func TestClaim_DefaultClientKeyResolver_LoadsRegistry(t *testing.T) {
	t.Parallel()

	clientPriv := claimTestKey(t)
	pub := &clientPriv.PublicKey
	fp := keys.PublicKeyFingerprint(pub)
	pubBytes := compressedSecp256k1Hex(t, pub)

	dir := t.TempDir()
	regPath := filepath.Join(dir, "clients.json")
	entries := []map[string]string{{"fingerprint": fp, "public_key": pubBytes}}
	raw, mErr := json.Marshal(entries)
	if mErr != nil {
		t.Fatalf("marshal entries: %v", mErr)
	}
	if err := os.WriteFile(regPath, raw, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ClientRegistry = regPath
		d.ClientKeyResolver = nil
	})

	got, err := srv.clientKeyResolver(fp)
	if err != nil {
		t.Fatalf("default resolver err=%v", err)
	}
	if got == nil {
		t.Fatal("default resolver returned nil")
	}
	if compressedSecp256k1Hex(t, got) != compressedSecp256k1Hex(t, pub) {
		t.Fatalf("default resolver returned a different key than registered")
	}

	if _, err := srv.clientKeyResolver("0123456789abcdef"); !errors.Is(err, ErrClientUnknown) {
		t.Fatalf("unknown fingerprint: got %v, want ErrClientUnknown", err)
	}
}

// TestClaim_ClientKeyResolver_Override — explicit resolver replaces the file
// loader. Verified by pointing the registry path at /dev/null and asserting
// the override is consulted instead.
func TestClaim_ClientKeyResolver_Override(t *testing.T) {
	t.Parallel()
	called := 0
	override := func(fp string) (*ecdsa.PublicKey, error) {
		called++
		if fp == "abcdef0123456789" {
			return &claimTestKey(t).PublicKey, nil
		}
		return nil, ErrClientUnknown
	}
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ClientRegistry = "/nonexistent/should-not-be-read"
		d.ClientKeyResolver = override
	})
	_, err := srv.clientKeyResolver("abcdef0123456789")
	if err != nil {
		t.Fatalf("override err=%v", err)
	}
	if called != 1 {
		t.Fatalf("override calls=%d, want 1", called)
	}
}

// TestClaim_RegisterHandlers_MountsClaimRoute — RegisterHandlers records
// the /claim route under the chassis prefix.
func TestClaim_RegisterHandlers_MountsClaimRoute(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)
	if err := srv.RegisterHandlers(); err != nil {
		t.Fatalf("RegisterHandlers: %v", err)
	}
	srv.mu.Lock()
	mounted := append([]mountedRoute(nil), srv.mountedRoutes...)
	srv.mu.Unlock()
	var foundClaim bool
	for _, r := range mounted {
		if r.method == http.MethodPost && r.path == "/claim" {
			foundClaim = true
			break
		}
	}
	if !foundClaim {
		t.Fatalf("RegisterHandlers did not mount POST /claim; got %+v", mounted)
	}
}

// ---- US1: TestClaim_Approved_IssuesJWT etc. -------------------------------

//nolint:gocognit,gocyclo // exhaustive happy-path assertions: status, three-key body, ten redacted-key checks, six audit-detail keys — Constitution VIII demands this granularity
func TestClaim_Approved_IssuesJWT(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: 2 * time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
	)
	o := defaultClaimBodyOpts(h)
	body := signedClaimBody(t, h, o)
	rr, chassisID := h.do(t, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Body shape: exactly three keys.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("response body unmarshal: %v", err)
	}
	if got, want := len(raw), 3; got != want {
		t.Fatalf("response keys=%d want %d (%v)", got, want, raw)
	}
	for _, k := range []string{"jwt", "expires_at", "jti"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("response missing key %q", k)
		}
	}
	for _, k := range []string{"scope", "reason", "ttl", "nonce", "signature", "ephemeral_pubkey", "machine_name", "client_key_fingerprint", "request_id"} {
		if _, ok := raw[k]; ok {
			t.Errorf("response leaks forbidden key %q", k)
		}
	}

	var resp claimResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response decode: %v", err)
	}
	if resp.JWT == "" {
		t.Fatal("empty jwt")
	}
	if resp.JTI == "" {
		t.Fatal("empty jti")
	}

	events := h.auditEvents()
	if len(events) != 1 {
		t.Fatalf("audit events=%d want 1", len(events))
	}
	e := events[0]
	if e.Type != AuditClaimOutcome {
		t.Fatalf("audit type=%v want %v", e.Type, AuditClaimOutcome)
	}
	if got := e.Detail["outcome"]; got != "approved" {
		t.Fatalf("audit outcome=%q want approved", got)
	}
	if got := e.Detail["session_type"]; got != "interactive" {
		t.Fatalf("audit session_type=%q want interactive", got)
	}
	if got := e.Detail["scope"]; got != "ANTHROPIC_API_KEY" {
		t.Fatalf("audit scope=%q want ANTHROPIC_API_KEY", got)
	}
	if e.Detail["granted_ttl"] == "" {
		t.Fatal("audit granted_ttl missing")
	}
	if e.Detail["jti"] != resp.JTI {
		t.Fatalf("audit jti=%q want %q", e.Detail["jti"], resp.JTI)
	}
	if e.RequestID != chassisID {
		t.Fatalf("audit request_id=%q want chassis %q", e.RequestID, chassisID)
	}
}

func TestClaim_TTLCappedAtConfigMax(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withCrypto(func(cs *config.CryptoSection) {
			cs.MaxInteractiveTTL = 1 * time.Hour
		}),
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: 1 * time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
	)
	o := defaultClaimBodyOpts(h)
	o.TTL = 5 * time.Hour // exceeds 1h cap
	start := time.Now()
	rr, _ := h.do(t, signedClaimBody(t, h, o))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	// (a) the value passed to fakeApprover.RequestApproval equals the cap.
	if got := h.approver.calls[0].RequestedTTL; got != 1*time.Hour {
		t.Fatalf("approver received TTL=%s want 1h (cap)", got)
	}
	var resp claimResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response decode: %v", err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, resp.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	// Allow ±5s for clock-skew between start and the handler's clock().
	want := start.Add(1 * time.Hour)
	if delta := expiresAt.Sub(want); delta < -5*time.Second || delta > 5*time.Second {
		t.Fatalf("expires_at=%v, want ~%v (delta=%v)", expiresAt, want, delta)
	}
	// JWT-decoded exp matches the cap.
	exp := jwtExp(t, resp.JWT)
	if delta := exp.Sub(want); delta < -5*time.Second || delta > 5*time.Second {
		t.Fatalf("jwt exp=%v, want ~%v (delta=%v)", exp, want, delta)
	}
}

func TestClaim_SupervisorRequest_DaemonLabel(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: 4 * time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
	)
	o := defaultClaimBodyOpts(h)
	o.SessionType = "supervisor"
	o.TTL = 4 * time.Hour
	rr, _ := h.do(t, signedClaimBody(t, h, o))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	// Approver received the cap from MaxSupervisorTTL.
	if got := h.approver.calls[0].SessionType; got != SessionSupervisor {
		t.Fatalf("approver session_type=%v want supervisor", got)
	}
	var resp claimResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	claims := jwtClaims(t, resp.JWT)
	if got := claims["session_type"]; got != "supervisor" {
		t.Fatalf("jwt session_type=%v want supervisor", got)
	}
	if got, _ := claims["max_uses"].(float64); got != 0 {
		t.Fatalf("jwt max_uses=%v want 0 (supervisor)", got)
	}
	events := h.auditEvents()
	if got := events[0].Detail["session_type"]; got != "supervisor" {
		t.Fatalf("audit session_type=%q want supervisor", got)
	}
}

//nolint:gocognit // table-driven across two TTL forms — Constitution VIII
func TestClaim_TTLZeroOrNegative_400(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ttl  string
	}{
		{"zero", "0s"},
		{"negative", "-5m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newClaimHarness(t)
			o := defaultClaimBodyOpts(h)
			body := signedClaimBodyWithLiteralTTL(t, h, o, tc.ttl)
			rr, _ := h.do(t, body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
			}
			assertErrorBodyShape(t, rr, "bad_request")
			if len(h.approver.calls) != 0 {
				t.Fatalf("approver called %d times, want 0", len(h.approver.calls))
			}
			events := h.auditEvents()
			if len(events) != 1 || events[0].Detail["outcome"] != "bad-request" {
				t.Fatalf("audit events=%v, want one bad-request", events)
			}
		})
	}
}

// signedClaimBodyWithLiteralTTL re-signs the body with an arbitrary TTL
// string (used to exercise non-positive TTL rejection without
// time.Duration.String() reformatting).
func signedClaimBodyWithLiteralTTL(t *testing.T, h *claimTestHarness, o claimBodyOpts, ttlLiteral string) []byte {
	t.Helper()
	timestamp := o.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	payload := signedPayload{
		EphemeralPubKey: o.EphemeralPubKey,
		MachineName:     o.MachineName,
		Nonce:           o.Nonce,
		Reason:          o.Reason,
		RequestID:       o.RequestID,
		Scope:           o.Scope,
		SessionType:     o.SessionType,
		Timestamp:       timestamp.Format(time.RFC3339Nano),
		TTL:             ttlLiteral,
	}
	canonical, _ := sign.CanonicalJSON(payload)
	sig, _ := sign.Sign(t.Context(), h.clientPriv, canonical)
	body := map[string]any{
		"scope":                  o.Scope,
		"reason":                 o.Reason,
		"ttl":                    ttlLiteral,
		"session_type":           o.SessionType,
		"ephemeral_pubkey":       o.EphemeralPubKey,
		"nonce":                  o.Nonce,
		"timestamp":              timestamp.Format(time.RFC3339Nano),
		"signature":              base64.StdEncoding.EncodeToString(sig),
		"request_id":             o.RequestID,
		"machine_name":           o.MachineName,
		"client_key_fingerprint": h.fingerprint,
	}
	out := mustMarshal(t, body)
	return out
}

//nolint:gocognit // table-driven across six shape-rejection variants — Constitution VIII
func TestClaim_BadRequest_400(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		mutator func(t *testing.T, h *claimTestHarness) []byte
	}{
		{
			name: "malformed_json",
			mutator: func(_ *testing.T, _ *claimTestHarness) []byte {
				return []byte(`{not valid json`)
			},
		},
		{
			name: "unknown_extra_field",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				body := signedClaimBody(t, h, defaultClaimBodyOpts(h))
				var m map[string]any
				_ = json.Unmarshal(body, &m)
				m["surprise"] = "field"
				out := mustMarshal(t, m)
				return out
			},
		},
		{
			name: "missing_scope",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				body := signedClaimBody(t, h, defaultClaimBodyOpts(h))
				var m map[string]any
				_ = json.Unmarshal(body, &m)
				delete(m, "scope")
				out := mustMarshal(t, m)
				return out
			},
		},
		{
			name: "missing_request_id",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.RequestID = ""
				return signedClaimBody(t, h, o)
			},
		},
		{
			name: "malformed_request_id",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.RequestID = "short" // < 16 chars
				return signedClaimBody(t, h, o)
			},
		},
		{
			name: "session_type_unknown",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.SessionType = "delegated" // not in {interactive, supervisor}
				return signedClaimBody(t, h, o)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newClaimHarness(t)
			body := tc.mutator(t, h)
			rr, chassisID := h.do(t, body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
			}
			assertErrorBodyShape(t, rr, "bad_request")
			var resp errorResponse
			_ = json.Unmarshal(rr.Body.Bytes(), &resp)
			if resp.RequestID != chassisID {
				t.Fatalf("response request_id=%q want chassis %q", resp.RequestID, chassisID)
			}
			if len(h.approver.calls) != 0 {
				t.Fatalf("approver called %d times, want 0", len(h.approver.calls))
			}
			events := h.auditEvents()
			if len(events) != 1 || events[0].Detail["outcome"] != "bad-request" {
				t.Fatalf("audit events=%v want one bad-request", events)
			}
		})
	}
}

// ---- US2: 503 + no-auto-approve ------------------------------------------

func TestClaim_DiscordUnavailable_503(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{}},
			[]error{ErrApproverUnavailable},
		),
	)
	rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorBodyShape(t, rr, "discord_unavailable")
	if h.tokenIssuer.Calls() != 0 {
		t.Fatalf("token issuer called %d times, want 0", h.tokenIssuer.Calls())
	}
	events := h.auditEvents()
	if len(events) != 1 {
		t.Fatalf("audit events=%d want 1", len(events))
	}
	if events[0].Detail["outcome"] != "discord-unavailable" {
		t.Fatalf("audit outcome=%q want discord-unavailable", events[0].Detail["outcome"])
	}
}

func TestClaim_NoAutoApproveKnobExists(t *testing.T) {
	t.Parallel()

	// Source-grep — every internal/server/*.go (excluding _test.go) must
	// be free of "auto" within five lines of "approve" (case-insensitive).
	matches := grepAutoNearApprove(t, ".")
	if len(matches) > 0 {
		t.Fatalf("forbidden 'auto' near 'approve' references found:\n%s",
			strings.Join(matches, "\n"))
	}

	// Runtime-permutation — drive the unavailable path under several Deps
	// configurations and assert every one returns 503.
	for _, perm := range []struct {
		name string
		opts []harnessOpt
	}{
		{
			name: "default_deps_unavailable",
			opts: []harnessOpt{withApproverScript(
				[]Decision{{}},
				[]error{ErrApproverUnavailable},
			)},
		},
		{
			name: "approve_decision_paired_with_unavailable_error",
			// fakeApprover returns the SECOND tuple's err; both
			// conditions cooperate → 503 (error wins, dec.Approved
			// is irrelevant when err != nil).
			opts: []harnessOpt{withApproverScript(
				[]Decision{{Approved: true, GrantedTTL: time.Hour}},
				[]error{ErrApproverUnavailable},
			)},
		},
		{
			name: "wrapped_unavailable",
			opts: []harnessOpt{withApproverScript(
				[]Decision{{}},
				[]error{fmt.Errorf("transport closed: %w", ErrApproverUnavailable)},
			)},
		},
	} {
		t.Run(perm.name, func(t *testing.T) {
			t.Parallel()
			h := newClaimHarness(t, perm.opts...)
			rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d want 503", rr.Code)
			}
			if h.tokenIssuer.Calls() != 0 {
				t.Fatalf("token issuer called %d times, want 0", h.tokenIssuer.Calls())
			}
		})
	}

	// Also assert New rejects a nil approver — proves no construction-time
	// shortcut to a token-issuing pseudo-approver.
	logger, _ := captureLogger(t)
	cfg := testCfg(t)
	initial := vault.Store(newFakeStore("A", []byte("a")))
	var ptr atomic.Pointer[vault.Store]
	ptr.Store(&initial)
	_, err := New(Deps{
		Cfg:         cfg,
		VaultPtr:    &ptr,
		TokenStore:  token.NewStore(),
		TokenIssuer: noopTokenIssuer,
		Approver:    nil,
		Logger:      logger,
		AuditWriter: &recordingAudit{},
	})
	if !errors.Is(err, ErrMissingApprover) {
		t.Fatalf("New with nil Approver: got %v, want ErrMissingApprover", err)
	}
}

func TestClaim_UnknownOutcome_503(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		dec  Decision
		err  error
	}{
		{"non_sentinel_error", Decision{}, errFakeUnrecognised},
		{"approved_false_no_error", Decision{Approved: false}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newClaimHarness(
				t,
				withApproverScript([]Decision{tc.dec}, []error{tc.err}),
			)
			rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d want 503; body=%s", rr.Code, rr.Body.String())
			}
			assertErrorBodyShape(t, rr, "unknown_outcome")
			events := h.auditEvents()
			if len(events) != 1 || events[0].Detail["outcome"] != "unknown-outcome" {
				t.Fatalf("audit events=%v want one unknown-outcome", events)
			}
		})
	}
}

// ---- US3: pre-approval failures ------------------------------------------

func TestClaim_BadSignature_403(t *testing.T) {
	t.Parallel()

	t.Run("tampered_signature", func(t *testing.T) {
		t.Parallel()
		h := newClaimHarness(t)
		o := defaultClaimBodyOpts(h)
		o.CorruptSignature = true
		rr, _ := h.do(t, signedClaimBody(t, h, o))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status=%d want 403", rr.Code)
		}
		assertErrorBodyShape(t, rr, "bad_signature")
		if len(h.approver.calls) != 0 {
			t.Fatalf("approver invoked")
		}
		assertSingleAudit(t, h, "bad-signature")
	})

	t.Run("unknown_fingerprint", func(t *testing.T) {
		t.Parallel()
		h := newClaimHarness(t)
		o := defaultClaimBodyOpts(h)
		o.FingerprintOverride = "0123456789abcdef" // not registered
		rr, _ := h.do(t, signedClaimBody(t, h, o))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status=%d want 403", rr.Code)
		}
		assertErrorBodyShape(t, rr, "bad_signature")
		assertSingleAudit(t, h, "bad-signature")
	})

	t.Run("signed_with_wrong_key", func(t *testing.T) {
		t.Parallel()
		h := newClaimHarness(t)
		other := claimTestKey(t)
		o := defaultClaimBodyOpts(h)
		o.SignWithKey = other
		rr, _ := h.do(t, signedClaimBody(t, h, o))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status=%d want 403", rr.Code)
		}
		assertErrorBodyShape(t, rr, "bad_signature")
	})
}

func TestClaim_NonceReplay_403(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
	)
	o := defaultClaimBodyOpts(h)
	body := signedClaimBody(t, h, o) // shared body uses the same nonce on both calls

	rr1, _ := h.do(t, body)
	rr2, _ := h.do(t, body)

	if rr1.Code == rr2.Code {
		t.Fatalf("expected one to win and one to lose; both got %d", rr1.Code)
	}
	loser := rr2
	if rr1.Code == http.StatusForbidden {
		loser = rr1
	}
	assertErrorBodyShape(t, loser, "nonce_replay")

	// Two audits: one approved (or whatever winner outcome was), one nonce-replay.
	events := h.auditEvents()
	if len(events) != 2 {
		t.Fatalf("audit events=%d want 2", len(events))
	}
	var sawReplay bool
	for _, e := range events {
		if e.Detail["outcome"] == "nonce-replay" {
			sawReplay = true
		}
	}
	if !sawReplay {
		t.Fatalf("no nonce-replay audit: %+v", events)
	}
}

func TestClaim_StaleTimestamp_403(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		offset time.Duration
	}{
		{"past_outside_skew", -2 * time.Minute},
		{"future_outside_skew", 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newClaimHarness(
				t,
				withCrypto(func(cs *config.CryptoSection) { cs.ClockSkew = 30 * time.Second }),
			)
			o := defaultClaimBodyOpts(h)
			o.Timestamp = time.Now().Add(tc.offset)
			rr, _ := h.do(t, signedClaimBody(t, h, o))
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status=%d want 403", rr.Code)
			}
			assertErrorBodyShape(t, rr, "stale_timestamp")
			if len(h.approver.calls) != 0 {
				t.Fatalf("approver invoked under stale ts")
			}
			assertSingleAudit(t, h, "stale-timestamp")
		})
	}
}

func TestClaim_IPNotAllowed_403(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		// CIDR list disallows the request RemoteAddr we will inject.
		withAllowedCIDRs([]string{"100.64.0.0/24"}),
		withClientIP("100.65.5.5:1234"), // outside 100.64.0.0/24
	)
	rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorBodyShape(t, rr, "ip_not_allowed")
	if len(h.approver.calls) != 0 {
		t.Fatalf("approver invoked under ip_not_allowed")
	}
	assertSingleAudit(t, h, "ip-not-allowed")
}

func TestClaim_ErrorBodyNoSentinel(t *testing.T) {
	t.Parallel()
	const sentinel = "SECRET_SHOULD_NEVER_APPEAR_12"
	h := newClaimHarness(t)
	o := defaultClaimBodyOpts(h)
	o.Reason = sentinel
	o.CorruptSignature = true // forces ErrSignatureInvalid
	rr, _ := h.do(t, signedClaimBody(t, h, o))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte(sentinel)) {
		t.Errorf("response body contains sentinel: %q", rr.Body.String())
	}
	if bytes.Contains(h.slogBuf.Bytes(), []byte(sentinel)) {
		t.Errorf("slog buffer contains sentinel: %q", h.slogBuf.String())
	}
	events := h.auditEvents()
	if len(events) != 1 {
		t.Fatalf("audit events=%d want 1", len(events))
	}
	if _, ok := events[0].Detail["reason"]; ok {
		t.Errorf("audit detail leaks reason key: %+v", events[0].Detail)
	}
	for k, v := range events[0].Detail {
		if strings.Contains(v, sentinel) {
			t.Errorf("audit detail value contains sentinel: %s=%q", k, v)
		}
	}
}

// TestClaim_HappyPath_NoSentinelInResponse is the success-path counterpart
// to TestClaim_ErrorBodyNoSentinel: a future refactor that accidentally
// echoes user-supplied fields (e.g. reason, machine_name) into a 200-OK
// response, slog buffer, or audit Detail must be caught here.
func TestClaim_HappyPath_NoSentinelInResponse(t *testing.T) {
	t.Parallel()
	const sentinel = "SECRET_SHOULD_NEVER_APPEAR_HAPPY_PATH"
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
	)
	o := defaultClaimBodyOpts(h)
	o.Reason = sentinel
	o.MachineName = sentinel + "-host"
	rr, _ := h.do(t, signedClaimBody(t, h, o))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte(sentinel)) {
		t.Errorf("response body leaks sentinel: %q", rr.Body.String())
	}
	if bytes.Contains(h.slogBuf.Bytes(), []byte(sentinel)) {
		t.Errorf("slog buffer leaks sentinel: %q", h.slogBuf.String())
	}
	for _, ev := range h.auditEvents() {
		for k, v := range ev.Detail {
			if strings.Contains(v, sentinel) {
				t.Errorf("audit detail leaks sentinel at %q: %q", k, v)
			}
		}
	}
}

func TestClaim_ShortCircuitOrdering(t *testing.T) {
	t.Parallel()

	t.Run("bad_signature_beats_stale_timestamp", func(t *testing.T) {
		t.Parallel()
		h := newClaimHarness(t)
		o := defaultClaimBodyOpts(h)
		o.CorruptSignature = true
		o.Timestamp = time.Now().Add(-5 * time.Minute) // also stale
		rr, _ := h.do(t, signedClaimBody(t, h, o))
		assertErrorBodyShape(t, rr, "bad_signature")
	})

	t.Run("nonce_replay_beats_ip_not_allowed", func(t *testing.T) {
		t.Parallel()
		// First request to claim the nonce.
		h := newClaimHarness(
			t,
			withAllowedCIDRs([]string{"100.64.0.0/10"}),
			withClientIP("100.64.1.5:1234"),
			withApproverScript(
				[]Decision{{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"}},
				[]error{nil},
			),
		)
		o := defaultClaimBodyOpts(h)
		body := signedClaimBody(t, h, o)
		_, _ = h.do(t, body)

		// Second request: SAME nonce but BAD IP. nonce_replay must win
		// because nonce comes before IP in the pipeline order.
		h.clientIP = "100.65.5.5:1234"
		rr2, _ := h.do(t, body)
		assertErrorBodyShape(t, rr2, "nonce_replay")
	})
}

// ---- US4: deny + rate-limited ---------------------------------------------

func TestClaim_Denied_403(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript([]Decision{{}}, []error{ErrApproverDenied}),
	)
	rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorBodyShape(t, rr, "denied")
	if h.tokenIssuer.Calls() != 0 {
		t.Fatalf("token issuer calls=%d want 0", h.tokenIssuer.Calls())
	}
	assertSingleAudit(t, h, "denied")
}

func TestClaim_RateLimited_429(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript([]Decision{{}}, []error{ErrApproverRateLimited}),
	)
	rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorBodyShape(t, rr, "rate_limited")
	assertSingleAudit(t, h, "rate-limited")
}

// ---- US5: timeout --------------------------------------------------------

func TestClaim_DiscordTimeout_408(t *testing.T) {
	t.Parallel()

	// blockingApprover blocks on its own ctx; returns ErrApproverTimeout
	// when the parent deadline fires.
	bAppr := func(ctx context.Context, _ ApprovalRequest) (Decision, error) {
		<-ctx.Done()
		return Decision{}, ErrApproverTimeout
	}

	h := newClaimHarness(
		t,
		withCrypto(func(cs *config.CryptoSection) { cs.ClaimApprovalTimeout = 50 * time.Millisecond }),
		withDepsMutator(func(d *Deps) {
			d.Approver = approverFunc(bAppr)
		}),
	)

	start := time.Now()
	rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
	elapsed := time.Since(start)

	if rr.Code != http.StatusRequestTimeout {
		t.Fatalf("status=%d want 408; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorBodyShape(t, rr, "approval_timeout")
	if h.tokenIssuer.Calls() != 0 {
		t.Fatalf("token issuer calls=%d want 0", h.tokenIssuer.Calls())
	}
	if elapsed < 50*time.Millisecond {
		t.Fatalf("elapsed=%v < 50ms — handler returned before the deadline", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed=%v > 2s — handler did not enforce the deadline", elapsed)
	}
	assertSingleAudit(t, h, "approval-timeout")
}

// approverFunc adapts a function value to the Approver interface.
type approverFunc func(ctx context.Context, req ApprovalRequest) (Decision, error)

func (f approverFunc) RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error) {
	return f(ctx, req)
}

// ---- US6: every outcome emits one audit event ----------------------------

//nolint:gocognit,gocyclo // table-driven across all eleven outcomes with key-set assertions — Constitution VIII demands this exhaustive shape
func TestClaim_AuditEventEmittedForEveryOutcome(t *testing.T) {
	t.Parallel()

	type expectation struct {
		outcome     string
		grantedTTL  bool
		jti         bool
		hasScope    bool
		sessionType bool
	}
	common := expectation{
		hasScope:    true,
		sessionType: true,
	}

	type driver struct {
		name string
		fn   func(t *testing.T) (events []AuditEvent, exp expectation)
	}

	drivers := []driver{
		{
			name: "approved",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(
					t,
					withApproverScript(
						[]Decision{{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"}},
						[]error{nil},
					),
				)
				_, _ = h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
				e := common
				e.outcome = "approved"
				e.grantedTTL = true
				e.jti = true
				return h.auditEvents(), e
			},
		},
		{
			name: "bad-request",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(t)
				_, _ = h.do(t, []byte(`{not json`))
				e := expectation{} // bad-request omits scope/session_type — body did not parse
				e.outcome = "bad-request"
				return h.auditEvents(), e
			},
		},
		{
			name: "bad-signature",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(t)
				o := defaultClaimBodyOpts(h)
				o.CorruptSignature = true
				_, _ = h.do(t, signedClaimBody(t, h, o))
				e := common
				e.outcome = "bad-signature"
				return h.auditEvents(), e
			},
		},
		{
			name: "nonce-replay",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(
					t,
					withApproverScript(
						[]Decision{{Approved: true, GrantedTTL: time.Hour}},
						[]error{nil},
					),
				)
				body := signedClaimBody(t, h, defaultClaimBodyOpts(h))
				_, _ = h.do(t, body)
				_, _ = h.do(t, body)
				// Filter to the replay event.
				events := h.auditEvents()
				for _, e := range events {
					if e.Detail["outcome"] == "nonce-replay" {
						exp := common
						exp.outcome = "nonce-replay"
						return []AuditEvent{e}, exp
					}
				}
				t.Fatalf("no nonce-replay event in %v", events)
				return nil, expectation{}
			},
		},
		{
			name: "stale-timestamp",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(t)
				o := defaultClaimBodyOpts(h)
				o.Timestamp = time.Now().Add(-5 * time.Minute)
				_, _ = h.do(t, signedClaimBody(t, h, o))
				e := common
				e.outcome = "stale-timestamp"
				return h.auditEvents(), e
			},
		},
		{
			name: "ip-not-allowed",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(
					t,
					withAllowedCIDRs([]string{"100.64.0.0/24"}),
					withClientIP("100.65.5.5:1234"),
				)
				_, _ = h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
				e := common
				e.outcome = "ip-not-allowed"
				return h.auditEvents(), e
			},
		},
		{
			name: "denied",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(
					t,
					withApproverScript([]Decision{{}}, []error{ErrApproverDenied}),
				)
				_, _ = h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
				e := common
				e.outcome = "denied"
				return h.auditEvents(), e
			},
		},
		{
			name: "approval-timeout",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(
					t,
					withCrypto(func(cs *config.CryptoSection) { cs.ClaimApprovalTimeout = 30 * time.Millisecond }),
					withDepsMutator(func(d *Deps) {
						d.Approver = approverFunc(func(ctx context.Context, _ ApprovalRequest) (Decision, error) {
							<-ctx.Done()
							return Decision{}, ErrApproverTimeout
						})
					}),
				)
				_, _ = h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
				e := common
				e.outcome = "approval-timeout"
				return h.auditEvents(), e
			},
		},
		{
			name: "rate-limited",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(
					t,
					withApproverScript([]Decision{{}}, []error{ErrApproverRateLimited}),
				)
				_, _ = h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
				e := common
				e.outcome = "rate-limited"
				return h.auditEvents(), e
			},
		},
		{
			name: "discord-unavailable",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(
					t,
					withApproverScript([]Decision{{}}, []error{ErrApproverUnavailable}),
				)
				_, _ = h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
				e := common
				e.outcome = "discord-unavailable"
				return h.auditEvents(), e
			},
		},
		{
			name: "unknown-outcome",
			fn: func(t *testing.T) ([]AuditEvent, expectation) {
				h := newClaimHarness(
					t,
					withApproverScript([]Decision{{Approved: false}}, []error{nil}),
				)
				_, _ = h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
				e := common
				e.outcome = "unknown-outcome"
				return h.auditEvents(), e
			},
		},
	}

	forbidden := []string{"signature", "nonce", "ephemeral_pubkey", "reason", "jwt", "client_key_fingerprint"}
	for _, d := range drivers {
		t.Run(d.name, func(t *testing.T) {
			t.Parallel()
			events, exp := d.fn(t)
			if len(events) != 1 {
				t.Fatalf("audit events=%d want 1", len(events))
			}
			e := events[0]
			if e.Type != AuditClaimOutcome {
				t.Errorf("type=%v want %v", e.Type, AuditClaimOutcome)
			}
			if got := e.Detail["outcome"]; got != exp.outcome {
				t.Errorf("outcome=%q want %q", got, exp.outcome)
			}
			if exp.sessionType {
				if got := e.Detail["session_type"]; got == "" {
					t.Errorf("session_type empty")
				}
			}
			if exp.hasScope {
				if got := e.Detail["scope"]; got == "" {
					t.Errorf("scope empty")
				}
			}
			if exp.grantedTTL {
				if got := e.Detail["granted_ttl"]; got == "" {
					t.Errorf("granted_ttl empty")
				}
			}
			if exp.jti {
				if got := e.Detail["jti"]; got == "" {
					t.Errorf("jti empty")
				}
			}
			for _, k := range forbidden {
				if _, ok := e.Detail[k]; ok {
					t.Errorf("forbidden key present: %q", k)
				}
			}
		})
	}
}

// ---- helpers --------------------------------------------------------------

func assertErrorBodyShape(t *testing.T, rr *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("response unmarshal: %v; body=%s", err, rr.Body.String())
	}
	if got := len(raw); got != 2 {
		t.Errorf("response keys=%d want 2 (%v)", got, raw)
	}
	for _, k := range []string{"error", "request_id"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	var resp errorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error != wantCode {
		t.Errorf("error=%q want %q", resp.Error, wantCode)
	}
}

func assertSingleAudit(t *testing.T, h *claimTestHarness, wantOutcome string) {
	t.Helper()
	events := h.auditEvents()
	if len(events) != 1 {
		t.Fatalf("audit events=%d want 1: %+v", len(events), events)
	}
	if got := events[0].Detail["outcome"]; got != wantOutcome {
		t.Errorf("outcome=%q want %q", got, wantOutcome)
	}
}

// jwtClaims decodes the middle segment of a JWS-form JWT and returns its
// claims as a generic map. Used in supervisor / TTL assertions.
func jwtClaims(t *testing.T, encoded string) map[string]any {
	t.Helper()
	parts := strings.Split(encoded, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt segments=%d want 3", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode jwt payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return claims
}

// jwtExp extracts the "exp" claim as a time.Time.
func jwtExp(t *testing.T, encoded string) time.Time {
	t.Helper()
	c := jwtClaims(t, encoded)
	exp, ok := c["exp"].(float64)
	if !ok {
		t.Fatalf("jwt has no exp claim: %+v", c)
	}
	return time.Unix(int64(exp), 0)
}

// compressedSecp256k1Hex returns the SEC1-compressed 33-byte hex form used
// in the on-disk client registry fixture. Mirrors keys.PublicKeyFingerprint's
// internal serialization.
func compressedSecp256k1Hex(t *testing.T, pub *ecdsa.PublicKey) string {
	t.Helper()
	out := make([]byte, 33)
	if pub.Y.Bit(0) == 0 { //nolint:staticcheck // secp256k1 unsupported by crypto/ecdh
		out[0] = 0x02
	} else {
		out[0] = 0x03
	}
	xb := pub.X.Bytes() //nolint:staticcheck // secp256k1 unsupported by crypto/ecdh
	if len(xb) > 32 {
		t.Fatalf("X coord too large")
	}
	copy(out[1+32-len(xb):], xb)
	return hex.EncodeToString(out)
}

// grepAutoNearApprove scans internal/server/*.go (excluding
// _test.go) for "auto" within five lines of "approve" (case-insensitive).
// Returns matching descriptions; an empty slice means the source is clean.
//
//nolint:gocognit,gocyclo // file-walking grep with per-line proximity check — Constitution II's no-auto-approve regression test
func grepAutoNearApprove(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.Open(path) //nolint:gosec // path is a known internal/server source file
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		raw, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		lower := strings.ToLower(string(raw))
		lines := strings.Split(lower, "\n")
		for i, line := range lines {
			if !strings.Contains(line, "auto") {
				continue
			}
			start := i - 5
			if start < 0 {
				start = 0
			}
			end := i + 5
			if end >= len(lines) {
				end = len(lines) - 1
			}
			window := strings.Join(lines[start:end+1], "\n")
			if strings.Contains(window, "approve") {
				matches = append(matches, fmt.Sprintf("%s:%d: %q", path, i+1, line))
			}
		}
	}
	return matches
}

// silence unused-import warnings if only some tests are built in a partial
// `go test -run` invocation.
var _ = jwt.SigningMethodHS256

// mustMarshal is a test helper: panic-on-error JSON marshaling. Test
// fixtures are package-private values with no func/chan members, so
// json.Marshal is total in practice.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// Static test-only sentinel errors (err113 — no inline errors.New(...) at
// call sites that the linter would flag).
var (
	errFakeMint         = errors.New("test: forced token-mint failure")
	errFakeStore        = errors.New("test: forced token-store failure")
	errFakeAudit        = errors.New("test: forced audit-writer failure")
	errFakeUnrecognised = errors.New("test: unrecognized approver state")
)

// ---- additional coverage helpers -----------------------------------------

// TestClaim_BadRequest_AllShapeBranches drives every per-field validation
// branch in parseClaimRequest so coverage exercises the regex-rejection
// paths. Each sub-test produces a single bad-request audit and a 400 body.
func TestClaim_BadRequest_AllShapeBranches(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mutator func(t *testing.T, h *claimTestHarness) []byte
	}{
		{
			name: "trailing_junk_after_json",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				body := signedClaimBody(t, h, defaultClaimBodyOpts(h))
				return append(body, []byte(`{"extra":1}`)...)
			},
		},
		{
			name: "empty_reason",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.Reason = ""
				return signedClaimBody(t, h, o)
			},
		},
		{
			name: "oversized_reason",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.Reason = strings.Repeat("a", 257)
				return signedClaimBody(t, h, o)
			},
		},
		{
			name: "malformed_ttl",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				return signedClaimBodyWithLiteralTTL(t, h, defaultClaimBodyOpts(h), "not-a-duration")
			},
		},
		{
			name: "scope_invalid_name",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.Scope = []string{"lower-case-not-allowed"}
				return signedClaimBody(t, h, o)
			},
		},
		{
			name: "ephemeral_pubkey_wrong_length",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.EphemeralPubKey = "deadbeef" // < 66 chars
				return signedClaimBody(t, h, o)
			},
		},
		{
			name: "nonce_too_short",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.Nonce = "abc"
				return signedClaimBody(t, h, o)
			},
		},
		{
			name: "nonce_invalid_chars",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.Nonce = "!@#$%^&*()_+~~~~~"
				return signedClaimBody(t, h, o)
			},
		},
		{
			name: "timestamp_not_rfc3339",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				body := signedClaimBody(t, h, o)
				var m map[string]any
				_ = json.Unmarshal(body, &m)
				m["timestamp"] = "yesterday"
				out := mustMarshal(t, m)
				return out
			},
		},
		{
			name: "empty_signature",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				body := signedClaimBody(t, h, defaultClaimBodyOpts(h))
				var m map[string]any
				_ = json.Unmarshal(body, &m)
				m["signature"] = ""
				out := mustMarshal(t, m)
				return out
			},
		},
		{
			name: "malformed_machine_name",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.MachineName = strings.Repeat("a", 65)
				return signedClaimBody(t, h, o)
			},
		},
		{
			name: "malformed_fingerprint",
			mutator: func(t *testing.T, h *claimTestHarness) []byte {
				o := defaultClaimBodyOpts(h)
				o.Fingerprint = "GG" + strings.Repeat("0", 14) // non-hex
				return signedClaimBody(t, h, o)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newClaimHarness(t)
			rr, _ := h.do(t, tc.mutator(t, h))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
			}
			assertErrorBodyShape(t, rr, "bad_request")
			if len(h.approver.calls) != 0 {
				t.Fatalf("approver invoked")
			}
		})
	}
}

// TestClaim_TokenIssuerError_503 — defense-in-depth: a token-mint failure on
// an otherwise approved claim collapses to unknown_outcome, never to 200.
func TestClaim_TokenIssuerError_503(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
		withTokenIssuer(func(_ context.Context, _ token.IssueParams) (*token.Token, error) {
			return nil, errFakeMint
		}),
	)
	rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorBodyShape(t, rr, "unknown_outcome")
	assertSingleAudit(t, h, "unknown-outcome")
}

// TestClaim_TokenStoreError_503 — same fail-closed behaviour when the
// token Store rejects the new token (e.g. revoked-set hit).
func TestClaim_TokenStoreError_503(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
		withDepsMutator(func(d *Deps) {
			d.TokenStore = &failingTokenStore{}
		}),
	)
	rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorBodyShape(t, rr, "unknown_outcome")
	assertSingleAudit(t, h, "unknown-outcome")
}

type failingTokenStore struct{}

func (failingTokenStore) Add(_ *token.Token) error           { return errFakeStore }
func (failingTokenStore) Get(_ string) (*token.Token, error) { return nil, errFakeStore }
func (failingTokenStore) ConsumeUse(_ string) error          { return errFakeStore }
func (failingTokenStore) Revoke(_ string) error              { return errFakeStore }
func (failingTokenStore) Cleanup(_ context.Context)          {}
func (failingTokenStore) ActiveCount() int                   { return 0 }
func (failingTokenStore) RevokeIdempotent(_ string) (bool, bool) {
	return false, false
}

func (failingTokenStore) FindActiveSession(_ token.SessionType, _ string, _ []string) (*token.Token, bool) {
	return nil, false
}

// TestClaim_AuditWriteFailure_DoesNotBlockResponse — when the audit writer
// returns an error, the handler still writes the documented HTTP response.
func TestClaim_AuditWriteFailure_DoesNotBlockResponse(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
	)
	h.audit.err = errFakeAudit
	rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (audit failure must not block response)", rr.Code)
	}
}

// TestClaim_DecodeSignature_AcceptsAllBase64Forms — the four base64 forms
// the handler accepts (std, raw-std, url, raw-url) all parse successfully.
// Drives the loop in [decodeSignature].
func TestClaim_DecodeSignature_AcceptsAllBase64Forms(t *testing.T) {
	t.Parallel()
	want := []byte("the quick brown fox jumps over the lazy dog 0123")
	for _, tc := range []struct {
		name string
		enc  func([]byte) string
	}{
		{"std", func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }},
		{"raw_std", func(b []byte) string { return base64.RawStdEncoding.EncodeToString(b) }},
		{"url", func(b []byte) string { return base64.URLEncoding.EncodeToString(b) }},
		{"raw_url", func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeSignature(tc.enc(want))
			if err != nil {
				t.Fatalf("decode err=%v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("decoded mismatch")
			}
		})
	}
	if _, err := decodeSignature("$$$ not base64 $$$"); err == nil {
		t.Fatal("expected decode error for non-base64 input")
	}
}

// TestClaim_ParseSessionType_DefaultBranch covers the SessionType(0) sentinel.
func TestClaim_ParseSessionType_DefaultBranch(t *testing.T) {
	t.Parallel()
	if got := parseSessionType("nonsense"); got != SessionType(0) {
		t.Fatalf("parseSessionType(nonsense)=%v, want SessionType(0)", got)
	}
}

// TestClaim_CapTTL_BothCeilings exercises both interactive and supervisor
// caps plus the no-cap (zero ceiling) safety branch.
func TestClaim_CapTTL_BothCeilings(t *testing.T) {
	t.Parallel()
	cs := config.CryptoSection{MaxInteractiveTTL: time.Hour, MaxSupervisorTTL: 4 * time.Hour}
	if got := capTTL(SessionInteractive, 5*time.Hour, cs); got != time.Hour {
		t.Errorf("interactive cap=%s, want 1h", got)
	}
	if got := capTTL(SessionSupervisor, 5*time.Hour, cs); got != 4*time.Hour {
		t.Errorf("supervisor cap=%s, want 4h", got)
	}
	if got := capTTL(SessionInteractive, 30*time.Minute, cs); got != 30*time.Minute {
		t.Errorf("under-cap=%s, want 30m", got)
	}
	zero := config.CryptoSection{} // no ceiling configured
	if got := capTTL(SessionInteractive, 99*time.Hour, zero); got != 99*time.Hour {
		t.Errorf("zero ceiling=%s, want passthrough", got)
	}
	if got := capTTL(SessionType(0), time.Hour, cs); got != time.Hour {
		t.Errorf("default branch=%s, want passthrough at 1h cap", got)
	}
}

// TestClaim_ZeroApprovalTimeout_FallbackApplied — defense-in-depth: a
// misconfigured zero value must NOT cancel ctx immediately. The handler
// falls back to the documented 60s default.
func TestClaim_ZeroApprovalTimeout_FallbackApplied(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withCrypto(func(cs *config.CryptoSection) { cs.ClaimApprovalTimeout = 0 }),
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
	)
	rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// TestClaim_DefaultClientKeyResolver_LoadErrors — the file-loader propagates
// non-ENOENT errors but treats a missing file as an empty registry.
func TestClaim_DefaultClientKeyResolver_LoadErrors(t *testing.T) {
	t.Parallel()

	// Missing file → resolver returns ErrClientUnknown for every lookup.
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-file.json")
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ClientRegistry = missing
		d.ClientKeyResolver = nil
	})
	if _, err := srv.clientKeyResolver("0123456789abcdef"); !errors.Is(err, ErrClientUnknown) {
		t.Fatalf("missing-file resolver: got %v, want ErrClientUnknown", err)
	}

	// Malformed JSON → propagated error (caller sees as bad_signature).
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`not json`), 0o600); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	srv2, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ClientRegistry = bad
		d.ClientKeyResolver = nil
	})
	if _, err := srv2.clientKeyResolver("0123456789abcdef"); err == nil {
		t.Fatal("expected error for malformed registry")
	}
}

// TestClaim_DefaultClientKeyResolver_RetriesAfterFailedLoad pins the
// invariant that a malformed registry file does not get its load error
// cached forever: once the operator repairs the file in place, the
// next /claim attempt must pick up the fix without a chassis restart.
func TestClaim_DefaultClientKeyResolver_RetriesAfterFailedLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry.json")
	// Start with a malformed registry.
	if err := os.WriteFile(registry, []byte(`not json`), 0o600); err != nil {
		t.Fatalf("write malformed: %v", err)
	}

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ClientRegistry = registry
		d.ClientKeyResolver = nil
	})

	// First call — load fails, error surfaces.
	if _, err := srv.clientKeyResolver("0123456789abcdef"); err == nil {
		t.Fatal("first call: expected error from malformed registry")
	}

	// Operator repairs the file in place.
	good := []byte(`[{"fingerprint":"abc","public_key":"02c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"}]`)
	if err := os.WriteFile(registry, good, 0o600); err != nil {
		t.Fatalf("write good: %v", err)
	}

	// Next call must retry the load — a known fingerprint resolves;
	// unknown returns ErrClientUnknown (registry is now well-formed).
	pub, err := srv.clientKeyResolver("abc")
	if err != nil {
		t.Fatalf("post-repair lookup: %v", err)
	}
	if pub == nil {
		t.Fatal("post-repair lookup returned nil pubkey")
	}
	if _, err := srv.clientKeyResolver("nope"); !errors.Is(err, ErrClientUnknown) {
		t.Fatalf("unknown fingerprint after repair: got %v, want ErrClientUnknown", err)
	}
}

// TestClaim_DefaultClientKeyResolver_EmptyPath returns the empty-map default
// when ClientRegistry is unset.
func TestClaim_DefaultClientKeyResolver_EmptyPath(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Server.ClientRegistry = ""
		d.ClientKeyResolver = nil
	})
	if _, err := srv.clientKeyResolver("0123456789abcdef"); !errors.Is(err, ErrClientUnknown) {
		t.Fatalf("empty registry: got %v, want ErrClientUnknown", err)
	}
}

// TestClaim_VerifyClaimSignature_BadBase64 — the signature decoder rejects
// non-base64 values; the handler maps that to bad_signature.
func TestClaim_VerifyClaimSignature_BadBase64(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(t)
	o := defaultClaimBodyOpts(h)
	body := signedClaimBody(t, h, o)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	m["signature"] = "$$$ not base64 $$$"
	out := mustMarshal(t, m)
	rr, _ := h.do(t, out)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
	assertErrorBodyShape(t, rr, "bad_signature")
}

// TestClaim_SupervisorSessionResumption — when a supervisor that already
// holds an active session re-claims for the same (ClientIP, Scope), the
// chassis short-circuits the approver and mints a fresh JWT inheriting
// the existing session's remaining TTL. Eliminates per-restart Discord
// rate-limit waits for long-lived supervisor processes.
func TestClaim_SupervisorSessionResumption(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: 4 * time.Hour, ApproverID: "human"}},
			[]error{nil},
		),
	)

	// First claim — goes through the approver as a normal cold claim.
	o1 := defaultClaimBodyOpts(h)
	o1.SessionType = "supervisor"
	o1.TTL = 4 * time.Hour
	o1.Scope = []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY"}
	rr1, _ := h.do(t, signedClaimBody(t, h, o1))
	if rr1.Code != http.StatusOK {
		t.Fatalf("first claim status=%d want 200; body=%s", rr1.Code, rr1.Body.String())
	}
	var resp1 claimResponse
	_ = json.Unmarshal(rr1.Body.Bytes(), &resp1)
	if got := len(h.approver.calls); got != 1 {
		t.Fatalf("after first claim: approver calls=%d want 1", got)
	}

	// Second claim — same supervisor, same client IP, same scope; only the
	// nonce + ephemeral key change as they would after a process restart.
	o2 := defaultClaimBodyOpts(h)
	o2.SessionType = "supervisor"
	o2.TTL = 4 * time.Hour
	o2.Scope = []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY"}
	o2.Nonce = freshNonce()
	rr2, _ := h.do(t, signedClaimBody(t, h, o2))
	if rr2.Code != http.StatusOK {
		t.Fatalf("resumption status=%d want 200; body=%s", rr2.Code, rr2.Body.String())
	}
	var resp2 claimResponse
	_ = json.Unmarshal(rr2.Body.Bytes(), &resp2)

	// The whole point: approver was NOT invoked a second time.
	if got := len(h.approver.calls); got != 1 {
		t.Fatalf("after resumption: approver calls=%d want 1 (resumption must skip approver); got=%d", got, got)
	}

	// Fresh JTI on the resumed token.
	if resp1.JTI == resp2.JTI {
		t.Fatalf("resumed JTI %q equals original — expected fresh JTI", resp2.JTI)
	}

	// Resumed JWT carries the NEW ephemeral pub key (its own claim
	// payload), so an old listener can no longer decrypt deliveries.
	claims2 := jwtClaims(t, resp2.JWT)
	if got := claims2["ephemeral_pubkey"]; got != o2.EphemeralPubKey {
		t.Fatalf("resumed JWT ephemeral_pubkey=%v want %v", got, o2.EphemeralPubKey)
	}
}

// TestClaim_SupervisorSessionResumption_DifferentScopeStillPromptsApprover —
// resumption ONLY fires when the scope tuple matches exactly. A supervisor
// re-claiming with a broader / different scope must go through the approver.
func TestClaim_SupervisorSessionResumption_DifferentScopeStillPromptsApprover(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{
				{Approved: true, GrantedTTL: 4 * time.Hour, ApproverID: "human"},
				{Approved: true, GrantedTTL: 4 * time.Hour, ApproverID: "human"},
			},
			[]error{nil, nil},
		),
	)

	o1 := defaultClaimBodyOpts(h)
	o1.SessionType = "supervisor"
	o1.TTL = 4 * time.Hour
	o1.Scope = []string{"ANTHROPIC_API_KEY"}
	rr1, _ := h.do(t, signedClaimBody(t, h, o1))
	if rr1.Code != http.StatusOK {
		t.Fatalf("first claim status=%d", rr1.Code)
	}

	// Second claim with a wider scope — must go through approver again.
	o2 := defaultClaimBodyOpts(h)
	o2.SessionType = "supervisor"
	o2.TTL = 4 * time.Hour
	o2.Scope = []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY"}
	o2.Nonce = freshNonce()
	rr2, _ := h.do(t, signedClaimBody(t, h, o2))
	if rr2.Code != http.StatusOK {
		t.Fatalf("scope-widened claim status=%d want 200", rr2.Code)
	}
	if got := len(h.approver.calls); got != 2 {
		t.Fatalf("approver calls=%d want 2 (scope mismatch must NOT resume)", got)
	}
}

// TestClaim_InteractiveSessionDoesNotResume — only supervisor sessions
// resume; interactive sessions are short-lived and each one goes through
// the approver. (No "remember my last shell" surprise.)
func TestClaim_InteractiveSessionDoesNotResume(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{
				{Approved: true, GrantedTTL: 5 * time.Minute, ApproverID: "human"},
				{Approved: true, GrantedTTL: 5 * time.Minute, ApproverID: "human"},
			},
			[]error{nil, nil},
		),
	)

	o1 := defaultClaimBodyOpts(h)
	o1.SessionType = "interactive"
	o1.TTL = 5 * time.Minute
	rr1, _ := h.do(t, signedClaimBody(t, h, o1))
	if rr1.Code != http.StatusOK {
		t.Fatalf("first claim status=%d", rr1.Code)
	}

	o2 := defaultClaimBodyOpts(h)
	o2.SessionType = "interactive"
	o2.TTL = 5 * time.Minute
	o2.Nonce = freshNonce()
	rr2, _ := h.do(t, signedClaimBody(t, h, o2))
	if rr2.Code != http.StatusOK {
		t.Fatalf("second claim status=%d", rr2.Code)
	}
	if got := len(h.approver.calls); got != 2 {
		t.Fatalf("approver calls=%d want 2 (interactive must NOT resume)", got)
	}
}
