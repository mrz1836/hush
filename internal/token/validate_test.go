package token

import (
	"context"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func issueAndAdd(t *testing.T, store Store, mut func(*IssueParams)) (*Token, *ecdsa.PrivateKey) {
	t.Helper()
	priv := freshKey(t)
	params := defaultIssueParams(time.Now())
	if mut != nil {
		mut(&params)
	}
	tok, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := store.Add(tok); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return tok, priv
}

//nolint:gocyclo,cyclop // ten-claim recovered-claim assertion: complexity is in the per-field check
func TestValidate_HappyPath(t *testing.T) {
	store := NewStore()
	tok, priv := issueAndAdd(t, store, nil)

	claims, err := Validate(t.Context(), tok.Encoded, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.Issuer != "hush" {
		t.Errorf("Issuer: got %q, want hush", claims.Issuer)
	}
	if claims.ID != tok.JTI {
		t.Errorf("ID: got %q, want %q", claims.ID, tok.JTI)
	}
	if claims.SessionType != SessionInteractive {
		t.Errorf("SessionType: got %v, want interactive", claims.SessionType)
	}
	if claims.ClientIP != "100.64.0.1" {
		t.Errorf("ClientIP: got %q", claims.ClientIP)
	}
	if claims.RequestID != "req-1" {
		t.Errorf("RequestID: got %q", claims.RequestID)
	}
	if claims.MaxUses != 50 {
		t.Errorf("MaxUses: got %d", claims.MaxUses)
	}
	if claims.EphemeralPubKey != "deadbeef" {
		t.Errorf("EphemeralPubKey: got %q", claims.EphemeralPubKey)
	}
	if len(claims.Scope) != 1 || claims.Scope[0] != "FAKE_SECRET" {
		t.Errorf("Scope: got %v", claims.Scope)
	}
}

func TestValidate_HappyPath_Supervisor(t *testing.T) {
	store := NewStore()
	tok, priv := issueAndAdd(t, store, func(p *IssueParams) {
		p.SessionType = SessionSupervisor
	})

	for i := 0; i < 5; i++ {
		if _, err := Validate(t.Context(), tok.Encoded, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET"); err != nil {
			t.Fatalf("Validate iter %d: %v", i, err)
		}
	}
}

func TestValidate_DecrementsInteractive(t *testing.T) {
	store := NewStore()
	tok, priv := issueAndAdd(t, store, func(p *IssueParams) { p.MaxUses = 3 })

	if _, err := Validate(t.Context(), tok.Encoded, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got, err := store.Get(tok.JTI)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MaxUses != 2 {
		t.Fatalf("MaxUses after 1 validate: got %d, want 2", got.MaxUses)
	}
}

func TestValidate_RespectsCancelledContext(t *testing.T) {
	store := NewStore()
	tok, priv := issueAndAdd(t, store, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Validate(ctx, tok.Encoded, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestValidate_WrongIP(t *testing.T) {
	store := NewStore()
	tok, priv := issueAndAdd(t, store, nil)

	_, err := Validate(t.Context(), tok.Encoded, &priv.PublicKey, store, "100.64.0.99", "FAKE_SECRET")
	if !errors.Is(err, ErrIPMismatch) {
		t.Fatalf("got %v, want ErrIPMismatch", err)
	}
}

func TestValidate_IPSemanticallyEqual(t *testing.T) {
	cases := []struct {
		name      string
		issued    string
		requested string
	}{
		{"ipv4 same form", "100.64.0.1", "100.64.0.1"},
		{"ipv6 short vs long", "::1", "0000:0000:0000:0000:0000:0000:0000:0001"},
		{"ipv6 mixed", "2001:db8::1", "2001:0db8:0000:0000:0000:0000:0000:0001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore()
			tok, priv := issueAndAdd(t, store, func(p *IssueParams) { p.ClientIP = tc.issued })
			if _, err := Validate(t.Context(), tok.Encoded, &priv.PublicKey, store, tc.requested, "FAKE_SECRET"); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestValidate_MalformedRequestIP_Refused(t *testing.T) {
	store := NewStore()
	tok, priv := issueAndAdd(t, store, nil)

	_, err := Validate(t.Context(), tok.Encoded, &priv.PublicKey, store, "not-an-ip", "FAKE_SECRET")
	if !errors.Is(err, ErrIPMismatch) {
		t.Fatalf("got %v, want ErrIPMismatch", err)
	}
}

// --- Algorithm-confusion tests --------------------------------------

func hs256SecretFromPub(pub *ecdsa.PublicKey) []byte {
	//nolint:staticcheck // alg-confusion attack uses raw curve coordinates as the HMAC secret; the deprecation does not apply to test-only attacker simulation
	return append(pub.X.Bytes(), pub.Y.Bytes()...)
}

func TestValidate_AlgConfusion_None_Refused(t *testing.T) {
	store := NewStore()
	tok, priv := issueAndAdd(t, store, nil)

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	parts := strings.SplitN(tok.Encoded, ".", 3)
	mangled := header + "." + parts[1] + "."

	_, err := Validate(t.Context(), mangled, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET")
	if !errors.Is(err, ErrAlgorithmUnsupported) {
		t.Fatalf("got %v, want ErrAlgorithmUnsupported", err)
	}
}

func TestValidate_AlgConfusion_HS256_Refused(t *testing.T) {
	store := NewStore()
	tok, priv := issueAndAdd(t, store, nil)

	parts := strings.SplitN(tok.Encoded, ".", 3)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	signingInput := header + "." + parts[1]

	pubBytes := hs256SecretFromPub(&priv.PublicKey)
	mac := hmac.New(sha256.New, pubBytes)
	mac.Write([]byte(signingInput))
	hs256Sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	mangled := signingInput + "." + hs256Sig

	_, err := Validate(t.Context(), mangled, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET")
	if !errors.Is(err, ErrAlgorithmUnsupported) {
		t.Fatalf("got %v, want ErrAlgorithmUnsupported", err)
	}
}

func TestValidate_MalformedHeader_Refused(t *testing.T) {
	store := NewStore()
	priv := freshKey(t)
	pub := &priv.PublicKey
	cases := []struct {
		name    string
		encoded string
	}{
		{"empty", ""},
		{"no separator", "abc"},
		{"bad base64", "!@#$.payload.sig"},
		{"non-json header", base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".payload.sig"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate(t.Context(), tc.encoded, pub, store, "100.64.0.1", "FAKE_SECRET")
			// Pre-parse rejections (no separator, bad base64, bad JSON header)
			// are token-malformation, not algorithm-mismatch.
			if !errors.Is(err, ErrTokenMalformed) {
				t.Fatalf("got %v, want ErrTokenMalformed", err)
			}
		})
	}
}

// --- Expired / scope / unknown-session-type tests --------------------

func TestValidate_ExpiredJWT(t *testing.T) {
	store := NewStore()
	priv := freshKey(t)
	params := defaultIssueParams(time.Now().Add(-2 * time.Hour))
	params.TTL = time.Minute
	tok, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if addErr := store.Add(tok); addErr != nil {
		t.Fatalf("Add: %v", addErr)
	}

	_, err = Validate(t.Context(), tok.Encoded, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET")
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("got %v, want ErrTokenExpired", err)
	}
}

// --- Clock-skew tolerance (Q1) ---

// TestValidate_ClockSkew_JWTLayerAcceptsWithinWindow demonstrates that
// WithClockSkew is plumbed into jwt.WithLeeway. The setup uses a token
// whose exp has just passed but whose JTI is NOT in the store; without
// skew, the JWT-layer parse rejects with ErrTokenExpired before ever
// consulting the store; with skew large enough to cover the gap, the
// JWT-layer parse succeeds and we then surface ErrTokenRevoked from
// store.Get because the JTI is unknown. The error change proves the
// leeway is being applied at the parse step.
//
// (The store enforces its own strict expiry on consume — by design, as
// defense-in-depth. End-to-end "tokens are honored beyond their TTL"
// is intentionally NOT a property of the in-memory store.)
func TestValidate_ClockSkew_JWTLayerAcceptsWithinWindow(t *testing.T) {
	priv := freshKey(t)
	// Issue at T-90s with TTL=60s ⇒ exp is T-30s (expired 30s ago).
	params := defaultIssueParams(time.Now().Add(-90 * time.Second))
	params.TTL = 60 * time.Second
	tok, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Empty store: JTI is not present anywhere.
	emptyStore := NewStore()

	// Without skew: JWT parse rejects with ErrTokenExpired, store never consulted.
	_, errNoSkew := Validate(t.Context(), tok.Encoded, &priv.PublicKey, emptyStore, "100.64.0.1", "FAKE_SECRET")
	if !errors.Is(errNoSkew, ErrTokenExpired) {
		t.Fatalf("without skew: got %v, want ErrTokenExpired", errNoSkew)
	}

	// With 60s skew: JWT parse succeeds (T-30s is within T+30s = exp+skew),
	// flow reaches store.Get which returns ErrTokenRevoked because the
	// JTI is not in the live map.
	_, errWithSkew := Validate(t.Context(), tok.Encoded, &priv.PublicKey, emptyStore, "100.64.0.1", "FAKE_SECRET",
		WithClockSkew(60*time.Second))
	if !errors.Is(errWithSkew, ErrTokenRevoked) {
		t.Fatalf("with skew: got %v, want ErrTokenRevoked (JWT layer accepts via leeway, store has no entry)", errWithSkew)
	}
}

// TestValidate_ClockSkew_RejectsOutsideWindow asserts that even with a
// generous skew, a token expired far beyond the skew window is rejected
// at the JWT parse layer.
func TestValidate_ClockSkew_RejectsOutsideWindow(t *testing.T) {
	priv := freshKey(t)
	// 10 minutes past exp — far outside any sane skew.
	params := defaultIssueParams(time.Now().Add(-10 * time.Minute))
	params.TTL = 60 * time.Second
	tok, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	_, err = Validate(t.Context(), tok.Encoded, &priv.PublicKey, NewStore(), "100.64.0.1", "FAKE_SECRET",
		WithClockSkew(60*time.Second))
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("got %v, want ErrTokenExpired", err)
	}
}

// TestValidate_ClockSkew_ZeroBehavesLikeNoOption asserts that
// WithClockSkew(0) does not enable any tolerance (preserves the historical
// no-leeway behavior). A token expired by 5s is still rejected.
func TestValidate_ClockSkew_ZeroBehavesLikeNoOption(t *testing.T) {
	priv := freshKey(t)
	// Already expired by 5s — no skew should mean rejection at the JWT layer.
	params := defaultIssueParams(time.Now().Add(-65 * time.Second))
	params.TTL = 60 * time.Second
	tok, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	_, err = Validate(t.Context(), tok.Encoded, &priv.PublicKey, NewStore(), "100.64.0.1", "FAKE_SECRET",
		WithClockSkew(0))
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("got %v, want ErrTokenExpired (zero skew should not tolerate anything)", err)
	}
}

// TestValidate_ClockSkew_NegativeSkewIsIgnored asserts that a caller-supplied
// negative skew is treated as zero (rather than passed through as a negative
// leeway, which would tighten — not loosen — the window).
func TestValidate_ClockSkew_NegativeSkewIsIgnored(t *testing.T) {
	priv := freshKey(t)
	tok, err := Issue(t.Context(), priv, defaultIssueParams(time.Now()))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	store := NewStore()
	if err := store.Add(tok); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Fresh token, no skew configured (negative ignored): should validate.
	if _, err := Validate(t.Context(), tok.Encoded, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET",
		WithClockSkew(-30*time.Second)); err != nil {
		t.Fatalf("Validate with negative (ignored) skew: %v", err)
	}
}

func TestValidate_OutOfScope(t *testing.T) {
	store := NewStore()
	tok, priv := issueAndAdd(t, store, func(p *IssueParams) { p.Scope = []string{"FAKE_SECRET_A"} })

	_, err := Validate(t.Context(), tok.Encoded, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET_B")
	if !errors.Is(err, ErrScopeViolation) {
		t.Fatalf("got %v, want ErrScopeViolation", err)
	}
}

func TestValidate_UnknownSessionType_Refused(t *testing.T) {
	store := NewStore()
	priv := freshKey(t)
	now := time.Now()

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "hush",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			ID:        "manual-jti",
		},
		Scope:           []string{"FAKE_SECRET"},
		ClientIP:        "100.64.0.1",
		RequestID:       "req-1",
		MaxUses:         5,
		EphemeralPubKey: "deadbeef",
		SessionType:     "delegated",
	}
	Register()
	encoded, err := jwt.NewWithClaims(es256kMethod{}, claims).SignedString(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if addErr := store.Add(&Token{
		JTI: claims.ID, Encoded: encoded, ExpiresAt: claims.ExpiresAt.Time,
		SessionType: SessionInteractive, MaxUses: 5,
	}); addErr != nil {
		t.Fatalf("Add: %v", addErr)
	}

	_, err = Validate(t.Context(), encoded, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET")
	if !errors.Is(err, ErrUnknownSessionType) {
		t.Fatalf("got %v, want ErrUnknownSessionType", err)
	}
}

func TestValidate_MalformedClaimIP_Refused(t *testing.T) {
	store := NewStore()
	priv := freshKey(t)
	now := time.Now()
	Register()
	c := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "hush", IssuedAt: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			ID:        "claim-bad-ip",
		},
		Scope:           []string{"FAKE_SECRET"},
		ClientIP:        "definitely-not-an-ip",
		RequestID:       "req",
		MaxUses:         1,
		EphemeralPubKey: "deadbeef",
		SessionType:     SessionInteractive,
	}
	enc, err := jwt.NewWithClaims(es256kMethod{}, c).SignedString(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if addErr := store.Add(&Token{
		JTI: c.ID, Encoded: enc, ExpiresAt: c.ExpiresAt.Time,
		SessionType: SessionInteractive, MaxUses: 1,
	}); addErr != nil {
		t.Fatalf("Add: %v", addErr)
	}
	_, err = Validate(t.Context(), enc, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET")
	if !errors.Is(err, ErrIPMismatch) {
		t.Fatalf("got %v, want ErrIPMismatch", err)
	}
}

func TestValidate_BadSignature(t *testing.T) {
	store := NewStore()
	tok, _ := issueAndAdd(t, store, nil)
	otherKey := freshKey(t)

	// A correct ES256K-formed token with a wrong verify key is a signature-
	// invalid case, not an algorithm-unsupported one.
	_, err := Validate(t.Context(), tok.Encoded, &otherKey.PublicKey, store, "100.64.0.1", "FAKE_SECRET")
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("got %v, want ErrSignatureInvalid", err)
	}
}

// --- Sentinel-leak witness ------------------------------------------

const sentinelMarker = "SECRET_SHOULD_NEVER_APPEAR_2"

//nolint:gocognit,gocyclo,cyclop // 8-rejection-category fan-out: complexity is inherent to the sentinel-leak witness
func TestValidate_NoLeakOnError(t *testing.T) {
	store := NewStore()
	priv := freshKey(t)
	pub := &priv.PublicKey
	now := time.Now()

	params := defaultIssueParams(now)
	params.RequestID = "req-" + sentinelMarker + "-id"
	tok, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := store.Add(tok); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cases := []struct {
		name   string
		want   error
		invoke func(t *testing.T) error
	}{
		{"alg-none", ErrAlgorithmUnsupported, func(t *testing.T) error {
			parts := strings.SplitN(tok.Encoded, ".", 3)
			header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
			mangled := header + "." + parts[1] + "."
			_, e := Validate(t.Context(), mangled, pub, store, "100.64.0.1", "FAKE_SECRET")
			return e
		}},
		{"alg-hs256", ErrAlgorithmUnsupported, func(t *testing.T) error {
			parts := strings.SplitN(tok.Encoded, ".", 3)
			header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
			signingInput := header + "." + parts[1]
			pubBytes := hs256SecretFromPub(&priv.PublicKey)
			mac := hmac.New(sha256.New, pubBytes)
			mac.Write([]byte(signingInput))
			mangled := signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
			_, e := Validate(t.Context(), mangled, pub, store, "100.64.0.1", "FAKE_SECRET")
			return e
		}},
		{"expired", ErrTokenExpired, func(t *testing.T) error {
			expParams := defaultIssueParams(now.Add(-2 * time.Hour))
			expParams.TTL = time.Minute
			expTok, err := Issue(t.Context(), priv, expParams)
			if err != nil {
				t.Fatalf("issue expired: %v", err)
			}
			if err := store.Add(expTok); err != nil {
				t.Fatalf("add expired: %v", err)
			}
			_, e := Validate(t.Context(), expTok.Encoded, pub, store, "100.64.0.1", "FAKE_SECRET")
			return e
		}},
		{"wrong-ip", ErrIPMismatch, func(t *testing.T) error {
			_, e := Validate(t.Context(), tok.Encoded, pub, store, "100.64.0.99", "FAKE_SECRET")
			return e
		}},
		{"out-of-scope", ErrScopeViolation, func(t *testing.T) error {
			_, e := Validate(t.Context(), tok.Encoded, pub, store, "100.64.0.1", "OTHER_SECRET")
			return e
		}},
		{"unknown-session-type", ErrUnknownSessionType, func(t *testing.T) error {
			Register()
			c := Claims{
				RegisteredClaims: jwt.RegisteredClaims{
					Issuer: "hush", IssuedAt: jwt.NewNumericDate(now),
					ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
					ID:        "leak-unknown-st",
				},
				Scope:           []string{"FAKE_SECRET"},
				ClientIP:        "100.64.0.1",
				RequestID:       "leak-" + sentinelMarker,
				MaxUses:         1,
				EphemeralPubKey: "deadbeef",
				SessionType:     "delegated",
			}
			enc, err := jwt.NewWithClaims(es256kMethod{}, c).SignedString(priv)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			if err := store.Add(&Token{
				JTI: c.ID, Encoded: enc, ExpiresAt: c.ExpiresAt.Time,
				SessionType: SessionInteractive, MaxUses: 1,
			}); err != nil {
				t.Fatalf("add: %v", err)
			}
			_, e := Validate(t.Context(), enc, pub, store, "100.64.0.1", "FAKE_SECRET")
			return e
		}},
		{"revoked", ErrTokenRevoked, func(t *testing.T) error {
			revParams := defaultIssueParams(now)
			revParams.RequestID = "leak-revoke-" + sentinelMarker
			revTok, err := Issue(t.Context(), priv, revParams)
			if err != nil {
				t.Fatalf("issue rev: %v", err)
			}
			if err := store.Add(revTok); err != nil {
				t.Fatalf("add rev: %v", err)
			}
			if err := store.Revoke(revTok.JTI); err != nil {
				t.Fatalf("revoke: %v", err)
			}
			_, e := Validate(t.Context(), revTok.Encoded, pub, store, "100.64.0.1", "FAKE_SECRET")
			return e
		}},
		{"exhausted", ErrTokenExhausted, func(t *testing.T) error {
			exhParams := defaultIssueParams(now)
			exhParams.MaxUses = 1
			exhParams.RequestID = "leak-exh-" + sentinelMarker
			exhTok, err := Issue(t.Context(), priv, exhParams)
			if err != nil {
				t.Fatalf("issue exh: %v", err)
			}
			if err := store.Add(exhTok); err != nil {
				t.Fatalf("add exh: %v", err)
			}
			if _, err := Validate(t.Context(), exhTok.Encoded, pub, store, "100.64.0.1", "FAKE_SECRET"); err != nil {
				t.Fatalf("first validate: %v", err)
			}
			_, e := Validate(t.Context(), exhTok.Encoded, pub, store, "100.64.0.1", "FAKE_SECRET")
			return e
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.invoke(t)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			for cur := err; cur != nil; cur = errors.Unwrap(cur) {
				if strings.Contains(cur.Error(), sentinelMarker) {
					t.Fatalf("sentinel %q leaked in %q", sentinelMarker, cur.Error())
				}
			}
		})
	}
}
