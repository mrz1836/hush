package token

import (
	"crypto/ecdsa"
	"crypto/rand"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// BenchmarkValidate measures the request-path hot loop: JWT parse + ES256K
// signature verify + store lookup, on a freshly-issued token. Establishes a
// baseline so later refactors (handleClaim pipeline extraction, Server
// decomposition) can prove no allocation or wall-time regression.
func BenchmarkValidate(b *testing.B) {
	//nolint:staticcheck // secp256k1 not in crypto/ecdh; S256() is the curve identity
	priv, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	store := NewStore()
	// Supervisor session: ConsumeUse never decrements, so a single token
	// validates indefinitely under the benchmark loop.
	params := defaultIssueParams(time.Now())
	params.SessionType = SessionSupervisor
	tok, err := Issue(b.Context(), priv, params)
	if err != nil { //nolint:govet // err shadowed from key gen is intentional
		b.Fatalf("Issue: %v", err)
	}
	if err := store.Add(tok); err != nil {
		b.Fatalf("Add: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Validate(b.Context(), tok.Encoded, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET"); err != nil {
			b.Fatalf("Validate: %v", err)
		}
	}
}
