package ecies

import (
	"testing"
)

// BenchmarkEncrypt measures envelope construction: ephemeral keygen + ECDH
// + AES-CBC + HMAC. Baseline for the per-secret delivery path.
func BenchmarkEncrypt(b *testing.B) {
	priv := generateFreshKey(b)
	plaintext := []byte("benchmark-secret-value-of-typical-size")

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Encrypt(b.Context(), &priv.PublicKey, plaintext); err != nil {
			b.Fatalf("Encrypt: %v", err)
		}
	}
}

// BenchmarkDecrypt measures the receiver hot path: compressed-pubkey parse +
// ECDH + KDF + HMAC verify + AES-CBC decrypt + securebytes allocation.
func BenchmarkDecrypt(b *testing.B) {
	priv := generateFreshKey(b)
	plaintext := []byte("benchmark-secret-value-of-typical-size")
	envelope, err := Encrypt(b.Context(), &priv.PublicKey, plaintext)
	if err != nil {
		b.Fatalf("Encrypt: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sb, err := Decrypt(b.Context(), priv, envelope)
		if err != nil {
			b.Fatalf("Decrypt: %v", err)
		}
		_ = sb.Destroy()
	}
}
