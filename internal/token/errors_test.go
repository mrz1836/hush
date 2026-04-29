package token

import (
	"errors"
	"testing"
)

//nolint:gocognit // 7-sentinel cross-product comparison: complexity is inherent to the discipline check
func TestErrors_DistinctIdentities(t *testing.T) {
	sentinels := []struct {
		name string
		err  error
		msg  string
	}{
		{"ErrAlgorithmUnsupported", ErrAlgorithmUnsupported, "hush/token: algorithm unsupported"},
		{"ErrTokenExpired", ErrTokenExpired, "hush/token: token expired"},
		{"ErrTokenRevoked", ErrTokenRevoked, "hush/token: token revoked"},
		{"ErrTokenExhausted", ErrTokenExhausted, "hush/token: token exhausted"},
		{"ErrIPMismatch", ErrIPMismatch, "hush/token: ip mismatch"},
		{"ErrScopeViolation", ErrScopeViolation, "hush/token: scope violation"},
		{"ErrUnknownSessionType", ErrUnknownSessionType, "hush/token: unknown session type"},
		{"ErrInvalidIssueParams", ErrInvalidIssueParams, "hush/token: invalid issue params"},
		{"ErrJTIGeneration", ErrJTIGeneration, "hush/token: jti generation failed"},
		{"ErrSigningFailed", ErrSigningFailed, "hush/token: signing failed"},
		{"ErrTokenMalformed", ErrTokenMalformed, "hush/token: token malformed"},
		{"ErrSignatureInvalid", ErrSignatureInvalid, "hush/token: signature invalid"},
	}
	for _, s := range sentinels {
		if s.err == nil {
			t.Fatalf("%s is nil", s.name)
		}
		if got := s.err.Error(); got != s.msg {
			t.Fatalf("%s message: got %q, want %q", s.name, got, s.msg)
		}
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(a.err, b.err) {
				t.Fatalf("%s should not be Is %s", a.name, b.name)
			}
		}
	}
}
