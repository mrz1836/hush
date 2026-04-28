package ecies

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// generateFreshKey returns a freshly-generated secp256k1 ECDSA private key.
// Round-trip and failure-mode tests use a fresh key per run to honor the
// spec's "freshly-generated ephemeral keypair on each run" property.
func generateFreshKey(tb testing.TB) *ecdsa.PrivateKey {
	tb.Helper()
	key, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck // secp256k1 is not in crypto/ecdh; S256() is the correct curve accessor
	if err != nil {
		tb.Fatalf("generateFreshKey: %v", err)
	}
	return key
}

// generateFuzzKey returns a deterministic secp256k1 ECDSA private key derived
// from a fixed scalar (32 × 0x42). Used by FuzzECIESDecrypt so that fuzz
// repro IDs are byte-exact across runs.
func generateFuzzKey(tb testing.TB) *ecdsa.PrivateKey {
	tb.Helper()
	seed := bytes.Repeat([]byte{0x42}, 32)
	priv := secp256k1.PrivKeyFromBytes(seed)
	return priv.ToECDSA()
}

// mangleByte returns a copy of envelope with one byte XOR-flipped at position.
func mangleByte(envelope []byte, position int) []byte {
	out := bytes.Clone(envelope)
	out[position] ^= 0x01
	return out
}

// truncateEnvelope returns a fresh copy of envelope truncated to length bytes.
func truncateEnvelope(envelope []byte, length int) []byte {
	if length < 0 {
		length = 0
	}
	if length > len(envelope) {
		length = len(envelope)
	}
	out := make([]byte, length)
	copy(out, envelope[:length])
	return out
}

// appendByte returns a fresh copy of envelope with one extra byte appended.
func appendByte(envelope []byte) []byte {
	out := make([]byte, len(envelope)+1)
	copy(out, envelope)
	out[len(envelope)] = 0xAB
	return out
}
