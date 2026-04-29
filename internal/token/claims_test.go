package token

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSessionType_Vocabulary(t *testing.T) {
	cases := []struct {
		s    SessionType
		want bool
	}{
		{SessionInteractive, true},
		{SessionSupervisor, true},
		{"", false},
		{"delegated", false},
		{"INTERACTIVE", false},
		{"super", false},
	}
	for _, tc := range cases {
		if got := validSessionType(tc.s); got != tc.want {
			t.Errorf("validSessionType(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

//nolint:gocognit,gocyclo,cyclop // ten-key JSON round-trip: complexity is in the per-key assertion list
func TestClaims_JSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	c := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "hush",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			ID:        "abc-123",
		},
		Scope:           []string{"FOO", "BAR"},
		ClientIP:        "100.64.0.1",
		RequestID:       "req-1",
		MaxUses:         5,
		EphemeralPubKey: "deadbeef",
		SessionType:     SessionInteractive,
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"scope":`,
		`"client_ip":`,
		`"request_id":`,
		`"max_uses":`,
		`"ephemeral_pubkey":`,
		`"session_type":`,
		`"iss":`,
		`"iat":`,
		`"exp":`,
		`"jti":`,
	} {
		if !contains(string(encoded), key) {
			t.Errorf("encoded JSON missing %q: %s", key, encoded)
		}
	}

	var got Claims
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Issuer != c.Issuer || got.ID != c.ID {
		t.Errorf("registered claims mismatch: got %+v", got.RegisteredClaims)
	}
	if got.ClientIP != c.ClientIP || got.RequestID != c.RequestID ||
		got.MaxUses != c.MaxUses || got.EphemeralPubKey != c.EphemeralPubKey ||
		got.SessionType != c.SessionType {
		t.Errorf("hush claims mismatch: got %+v, want %+v", got, c)
	}
	if len(got.Scope) != len(c.Scope) || got.Scope[0] != c.Scope[0] {
		t.Errorf("scope mismatch: got %v, want %v", got.Scope, c.Scope)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
