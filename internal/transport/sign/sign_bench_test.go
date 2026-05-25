package sign

import (
	"context"
	"testing"
)

// BenchmarkSign measures end-to-end Sign cost (SHA-256 digest + ECDSA
// ASN.1 sign) over a fixed 256-byte payload using a freshly-generated
// secp256k1 key. Establishes a baseline before any future tuning of the
// hot path (every audit append and every client request triggers Sign).
func BenchmarkSign(b *testing.B) {
	key := generateFuzzKey(b)
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := Sign(ctx, key, payload); err != nil {
			b.Fatalf("Sign: %v", err)
		}
	}
}

// BenchmarkVerify measures Verify cost (SHA-256 digest + ECDSA ASN.1
// verify) over a fixed 256-byte payload with a pre-computed signature.
// Verify runs on every Sec-Bus revoke/refresh path; this baseline pins
// the cost before any future tuning.
func BenchmarkVerify(b *testing.B) {
	key := generateFuzzKey(b)
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}
	ctx := context.Background()
	sig, err := Sign(ctx, key, payload)
	if err != nil {
		b.Fatalf("Sign for setup: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := Verify(ctx, &key.PublicKey, payload, sig); err != nil {
			b.Fatalf("Verify: %v", err)
		}
	}
}
