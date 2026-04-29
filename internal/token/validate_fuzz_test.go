package token

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func fuzzKey(f *testing.F) *ecdsa.PrivateKey {
	f.Helper()
	k, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck // secp256k1 is not in crypto/ecdh; S256() is the curve identity
	if err != nil {
		f.Fatalf("generate key: %v", err)
	}
	return k
}

func FuzzJWTValidate(f *testing.F) {
	priv := fuzzKey(f)
	pub := &priv.PublicKey
	store := NewStore()

	seeds := []string{
		"",
		"not.valid.base64",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, encoded string) {
		_, err := Validate(t.Context(), encoded, pub, store, "100.64.0.1", "FAKE_SECRET")
		if err == nil {
			return
		}
		switch {
		case errors.Is(err, ErrAlgorithmUnsupported),
			errors.Is(err, ErrTokenMalformed),
			errors.Is(err, ErrSignatureInvalid),
			errors.Is(err, ErrTokenExpired),
			errors.Is(err, ErrTokenRevoked),
			errors.Is(err, ErrTokenExhausted),
			errors.Is(err, ErrIPMismatch),
			errors.Is(err, ErrScopeViolation),
			errors.Is(err, ErrUnknownSessionType),
			errors.Is(err, context.Canceled),
			errors.Is(err, context.DeadlineExceeded):
			return
		default:
			t.Errorf("Validate returned non-sentinel error type %T: %v", err, err)
		}
	})
}
