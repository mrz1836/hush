package keys

import (
	"context"
	"testing"
)

// BenchmarkDeriveMasterSeed pins the cost of the Argon2id key-derivation
// step. With production parameters (time=4, memory=256MiB, threads=4) the
// derivation is intentionally slow (~0.5s on Apple silicon) to make
// brute-force infeasible. This baseline exists so any future tuning that
// changes the parameter set is caught in code review with a concrete delta.
func BenchmarkDeriveMasterSeed(b *testing.B) {
	pass := []byte("benchmark-passphrase-for-argon2id-key-derivation")
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i + 1)
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		seed, err := DeriveMasterSeed(ctx, pass, salt)
		if err != nil {
			b.Fatalf("DeriveMasterSeed: %v", err)
		}
		if len(seed) != argon2KeyLen {
			b.Fatalf("seed len = %d; want %d", len(seed), argon2KeyLen)
		}
	}
}

// BenchmarkDeriveJWTSigningKey pins the cost of one BIP32 hardened-child
// derivation at the JWT path (m/44'/7743'/0'). All three production
// derivations (JWT, Audit, Vault) share the deriveHDChild + path walk
// helpers; benchmarking JWT alone is representative.
func BenchmarkDeriveJWTSigningKey(b *testing.B) {
	// Fixed seed: testutil-style derivation requires *testing.T, so we
	// supply a deterministic 64-byte seed directly. The benchmark stays
	// hermetic and doesn't depend on the Argon2id step measured above.
	seed := make([]byte, 64)
	for i := range seed {
		seed[i] = byte(i*7 + 1)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		key, err := DeriveJWTSigningKey(seed)
		if err != nil {
			b.Fatalf("DeriveJWTSigningKey: %v", err)
		}
		if key == nil || key.D == nil { //nolint:staticcheck // secp256k1 not in crypto/ecdh
			b.Fatal("nil key")
		}
	}
}
