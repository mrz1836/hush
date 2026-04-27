package keys

import (
	"testing"

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

// TestPublicKeyFingerprint_Stable covers G9 + FR-009 + SC-006.
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
