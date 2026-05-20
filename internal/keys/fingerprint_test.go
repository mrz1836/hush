package keys

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// katFingerprintHex is the expected PublicKeyFingerprint output for
// testKATFingerprintScalar(). Frozen after first green run.
const katFingerprintHex = "d136e9438ef1b044"

// testKATFingerprintScalar returns the fixed 32-byte scalar used for the KAT fingerprint.
func testKATFingerprintScalar() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return b
}

func TestPublicKeyFingerprint_Stable(t *testing.T) {
	key1, err := scalarToECDSAKey(testKATFingerprintScalar())
	require.NoError(t, err)

	scalar2 := make([]byte, 32)
	for i := range scalar2 {
		scalar2[i] = byte(i + 2)
	}
	key2, err := scalarToECDSAKey(scalar2)
	require.NoError(t, err)

	t.Run("two invocations on same key return identical strings", func(t *testing.T) {
		fp1 := PublicKeyFingerprint(&key1.PublicKey)
		fp2 := PublicKeyFingerprint(&key1.PublicKey)
		assert.Equal(t, fp1, fp2)
	})

	t.Run("length is exactly 16", func(t *testing.T) {
		fp := PublicKeyFingerprint(&key1.PublicKey)
		assert.Len(t, fp, 16)
	})

	t.Run("output matches lowercase hex pattern", func(t *testing.T) {
		fp := PublicKeyFingerprint(&key1.PublicKey)
		assert.Regexp(t, `^[0-9a-f]{16}$`, fp)
	})

	t.Run("distinct keys produce distinct fingerprints", func(t *testing.T) {
		fp1 := PublicKeyFingerprint(&key1.PublicKey)
		fp2 := PublicKeyFingerprint(&key2.PublicKey)
		assert.NotEqual(t, fp1, fp2, "distinct public keys must have distinct fingerprints")
	})

	t.Run("KAT: fixed scalar pins fixed fingerprint", func(t *testing.T) {
		fp := PublicKeyFingerprint(&key1.PublicKey)
		t.Logf("KAT fingerprint hex: %s", fp)
		assert.Equal(t, katFingerprintHex, fp)
	})
}

// TestPublicKeyFingerprint_PanicsOnNilPub asserts that the documented
// precondition (non-nil *ecdsa.PublicKey) is enforced via panic with a
// useful message rather than a nil-deref crash deep in copy().
func TestPublicKeyFingerprint_PanicsOnNilPub(t *testing.T) {
	assert.PanicsWithValue(
		t,
		"hush/keys: PublicKeyFingerprint: nil public key",
		func() { PublicKeyFingerprint(nil) },
	)
}

// TestPublicKeyFingerprint_PanicsOnNilXY asserts that a malformed pub with
// nil X or Y triggers a clear panic instead of a nil-deref later.
func TestPublicKeyFingerprint_PanicsOnNilXY(t *testing.T) {
	t.Run("nil X", func(t *testing.T) {
		pub := &ecdsa.PublicKey{Curve: secp256k1.S256(), X: nil, Y: big.NewInt(1)} //nolint:staticcheck // secp256k1 not in crypto/ecdh; S256() is the correct curve accessor
		assert.PanicsWithValue(
			t,
			"hush/keys: PublicKeyFingerprint: public key X or Y is nil",
			func() { PublicKeyFingerprint(pub) },
		)
	})
	t.Run("nil Y", func(t *testing.T) {
		pub := &ecdsa.PublicKey{Curve: secp256k1.S256(), X: big.NewInt(1), Y: nil} //nolint:staticcheck // secp256k1 not in crypto/ecdh; S256() is the correct curve accessor
		assert.PanicsWithValue(
			t,
			"hush/keys: PublicKeyFingerprint: public key X or Y is nil",
			func() { PublicKeyFingerprint(pub) },
		)
	})
}

// TestPublicKeyFingerprint_PanicsOnOversizedX asserts that an X coordinate
// larger than 32 bytes (cryptographically impossible for a valid secp256k1
// point but possible with a malformed input) triggers an explicit panic
// rather than a negative-index slice panic in copy().
func TestPublicKeyFingerprint_PanicsOnOversizedX(t *testing.T) {
	// Build a 33-byte big.Int: a value > 2^256 - 1.
	oversized := new(big.Int).Lsh(big.NewInt(1), 257)                                // 2^257
	pub := &ecdsa.PublicKey{Curve: secp256k1.S256(), X: oversized, Y: big.NewInt(1)} //nolint:staticcheck // secp256k1 not in crypto/ecdh; S256() is the correct curve accessor
	assert.PanicsWithValue(
		t,
		"hush/keys: PublicKeyFingerprint: X coordinate exceeds 32 bytes (not a valid secp256k1 point)",
		func() { PublicKeyFingerprint(pub) },
	)
}
