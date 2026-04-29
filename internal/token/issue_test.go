package token

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func defaultIssueParams(now time.Time) IssueParams {
	return IssueParams{
		Now:             now,
		TTL:             time.Hour,
		Scope:           []string{"FAKE_SECRET"},
		ClientIP:        "100.64.0.1",
		RequestID:       "req-1",
		MaxUses:         50,
		EphemeralPubKey: "deadbeef",
		SessionType:     SessionInteractive,
	}
}

func TestIssue_Interactive(t *testing.T) {
	priv := freshKey(t)
	now := time.Now()
	params := defaultIssueParams(now)
	params.MaxUses = 50

	tok, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if tok == nil {
		t.Fatal("nil token")
	}
	if tok.JTI == "" {
		t.Fatal("empty JTI")
	}
	if !tok.ExpiresAt.Equal(now.Add(params.TTL)) {
		t.Errorf("ExpiresAt: got %v, want %v", tok.ExpiresAt, now.Add(params.TTL))
	}
	if tok.SessionType != SessionInteractive {
		t.Errorf("SessionType: got %v, want interactive", tok.SessionType)
	}
	if tok.MaxUses != 50 {
		t.Errorf("MaxUses: got %d, want 50", tok.MaxUses)
	}
	if c := strings.Count(tok.Encoded, "."); c != 2 {
		t.Errorf("Encoded segments: got %d dots, want 2", c)
	}
}

func TestIssue_Supervisor(t *testing.T) {
	priv := freshKey(t)
	now := time.Now()
	params := defaultIssueParams(now)
	params.SessionType = SessionSupervisor
	params.MaxUses = 99

	tok, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if tok.MaxUses != 0 {
		t.Errorf("Token.MaxUses: got %d, want 0", tok.MaxUses)
	}
	parts := strings.Split(tok.Encoded, ".")
	if len(parts) != 3 {
		t.Fatalf("Encoded parts: got %d, want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if c.MaxUses != 0 {
		t.Errorf("Claim max_uses: got %d, want 0", c.MaxUses)
	}
}

func TestIssue_FreshJTIPerCall(t *testing.T) {
	priv := freshKey(t)
	params := defaultIssueParams(time.Now())

	a, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue a: %v", err)
	}
	b, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue b: %v", err)
	}
	if a.JTI == b.JTI {
		t.Fatalf("JTIs collided: %q", a.JTI)
	}
}

func TestIssue_HeaderAlg(t *testing.T) {
	priv := freshKey(t)
	tok, err := Issue(t.Context(), priv, defaultIssueParams(time.Now()))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	parts := strings.Split(tok.Encoded, ".")
	if len(parts) != 3 {
		t.Fatalf("Encoded parts: got %d, want 3", len(parts))
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(header, &hdr); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if hdr.Alg != "ES256K" {
		t.Errorf("alg: got %q, want ES256K", hdr.Alg)
	}
	if hdr.Typ != "JWT" {
		t.Errorf("typ: got %q, want JWT", hdr.Typ)
	}
}

func TestIssue_RejectsUnknownSessionType(t *testing.T) {
	priv := freshKey(t)
	params := defaultIssueParams(time.Now())
	params.SessionType = "delegated"

	_, err := Issue(t.Context(), priv, params)
	if !errors.Is(err, ErrUnknownSessionType) {
		t.Fatalf("Issue: got %v, want ErrUnknownSessionType", err)
	}
}

func TestIssue_RejectsInvalidParams(t *testing.T) {
	priv := freshKey(t)
	now := time.Now()

	type mut func(*IssueParams)
	cases := []struct {
		name string
		mut  mut
	}{
		{"zero now", func(p *IssueParams) { p.Now = time.Time{} }},
		{"zero ttl", func(p *IssueParams) { p.TTL = 0 }},
		{"negative ttl", func(p *IssueParams) { p.TTL = -time.Hour }},
		{"nil scope", func(p *IssueParams) { p.Scope = nil }},
		{"empty scope entry", func(p *IssueParams) { p.Scope = []string{""} }},
		{"empty client ip", func(p *IssueParams) { p.ClientIP = "" }},
		{"malformed client ip", func(p *IssueParams) { p.ClientIP = "not-an-ip" }},
		{"empty request id", func(p *IssueParams) { p.RequestID = "" }},
		{"zero max uses interactive", func(p *IssueParams) { p.MaxUses = 0 }},
		{"empty ephemeral pubkey", func(p *IssueParams) { p.EphemeralPubKey = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := defaultIssueParams(now)
			tc.mut(&p)
			_, err := Issue(t.Context(), priv, p)
			if !errors.Is(err, ErrAlgorithmUnsupported) {
				t.Fatalf("got %v, want ErrAlgorithmUnsupported", err)
			}
		})
	}
}

func TestIssue_RespectsCancelledContext(t *testing.T) {
	priv := freshKey(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Issue(ctx, priv, defaultIssueParams(time.Now()))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestIssue_NilSignKey(t *testing.T) {
	_, err := Issue(t.Context(), nil, defaultIssueParams(time.Now()))
	if !errors.Is(err, ErrAlgorithmUnsupported) {
		t.Fatalf("got %v, want ErrAlgorithmUnsupported", err)
	}
}

func TestGenerateJTI_RandReaderError(t *testing.T) {
	prev := randReader
	randReader = errReader{}
	t.Cleanup(func() { randReader = prev })

	priv := freshKey(t)
	_, err := Issue(t.Context(), priv, defaultIssueParams(time.Now()))
	if !errors.Is(err, ErrAlgorithmUnsupported) {
		t.Fatalf("got %v, want ErrAlgorithmUnsupported", err)
	}
}

var errRandFail = errors.New("rand fail")

var errForcedSign = errors.New("forced sign failure")

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errRandFail }

func TestIssue_SignFailure(t *testing.T) {
	prev := signEncoded
	signEncoded = func(_ jwt.Claims, _ *ecdsa.PrivateKey) (string, error) {
		return "", errForcedSign
	}
	t.Cleanup(func() { signEncoded = prev })

	priv := freshKey(t)
	_, err := Issue(t.Context(), priv, defaultIssueParams(time.Now()))
	if !errors.Is(err, ErrAlgorithmUnsupported) {
		t.Fatalf("got %v, want ErrAlgorithmUnsupported", err)
	}
}
