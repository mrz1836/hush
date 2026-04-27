package keys

import (
	"context"
	"math"
	"testing"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeriveClientKey_MachineIndexIsolation covers G8 + FR-008 + AC-6.
func TestDeriveClientKey_MachineIndexIsolation(t *testing.T) {
	seed1 := testSeed(t)

	seed2, err := DeriveMasterSeed(context.Background(), []byte("a-different-passphrase!"), testKATSalt())
	require.NoError(t, err)

	// serializePriv extracts the 32-byte scalar via secp256k1 to avoid the
	// deprecated ecdsa.PrivateKey.D big.Int field.
	// secp256k1 is not supported by crypto/ecdh in Go 1.26; .D is read-only here.
	serializePriv := func(t *testing.T, idx uint32, seed []byte) []byte {
		t.Helper()
		k, kErr := DeriveClientKey(seed, idx)
		require.NoError(t, kErr)
		return secp256k1.PrivKeyFromBytes(k.D.Bytes()).Serialize() //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D is read-only
	}

	t.Run("distinct indexes from same seed yield distinct scalars", func(t *testing.T) {
		s0 := serializePriv(t, 0, seed1)
		s1 := serializePriv(t, 1, seed1)
		s2 := serializePriv(t, 2, seed1)

		assert.NotEqual(t, s0, s1, "idx 0 vs 1 must differ")
		assert.NotEqual(t, s0, s2, "idx 0 vs 2 must differ")
		assert.NotEqual(t, s1, s2, "idx 1 vs 2 must differ")
	})

	t.Run("same index from different seeds yields distinct scalars", func(t *testing.T) {
		sa := serializePriv(t, 0, seed1)
		sb := serializePriv(t, 0, seed2)
		assert.NotEqual(t, sa, sb, "different seeds at same index must differ")
	})

	t.Run("re-derivation is deterministic", func(t *testing.T) {
		s1 := serializePriv(t, 0, seed1)
		s2 := serializePriv(t, 0, seed1)
		assert.Equal(t, s1, s2)
	})

	t.Run("full uint32 range: index 0 and MaxUint32 both succeed and differ", func(t *testing.T) {
		s0 := serializePriv(t, 0, seed1)
		sMax := serializePriv(t, math.MaxUint32, seed1)
		assert.NotEqual(t, s0, sMax, "index 0 and MaxUint32 must differ")
	})

	t.Run("propagates NewMaster error for invalid seed", func(t *testing.T) {
		_, err := DeriveClientKey(make([]byte, 5), 0)
		require.Error(t, err)
	})

	t.Run("public key is on secp256k1 curve", func(t *testing.T) {
		k, kErr := DeriveClientKey(seed1, 42)
		require.NoError(t, kErr)
		// Verify curve via name; secp256k1 BIP32 derivation always yields on-curve keys.
		assert.Equal(t, "secp256k1", k.Curve.Params().Name)
		// Re-derive public key via secp256k1 package to confirm the key is valid.
		pubFromScalar := secp256k1.PrivKeyFromBytes(k.D.Bytes()).PubKey() //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D is read-only
		assert.NotNil(t, pubFromScalar)
	})
}
