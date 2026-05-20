package keys

import (
	"context"
	"testing"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSeed returns a deterministic 64-byte seed used by path derivation tests.
func testSeed(t *testing.T) []byte {
	t.Helper()
	seed, err := DeriveMasterSeed(context.Background(), []byte(katPassphrase), testKATSalt())
	require.NoError(t, err)
	return seed
}

func TestDeriveJWTSigningKey_Path(t *testing.T) {
	seed := testSeed(t)

	t.Run("returns secp256k1 ECDSA key", func(t *testing.T) {
		key, err := DeriveJWTSigningKey(seed)
		require.NoError(t, err)
		require.NotNil(t, key)
		assert.Equal(t, "secp256k1", key.Curve.Params().Name)
	})

	t.Run("deterministic re-derivation", func(t *testing.T) {
		key1, err := DeriveJWTSigningKey(seed)
		require.NoError(t, err)
		key2, err := DeriveJWTSigningKey(seed)
		require.NoError(t, err)
		// secp256k1 not in crypto/ecdh; .D is read-only and safe to use for comparison.
		s1 := secp256k1.PrivKeyFromBytes(key1.D.Bytes()).Serialize() //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D is read-only
		s2 := secp256k1.PrivKeyFromBytes(key2.D.Bytes()).Serialize() //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D is read-only
		assert.Equal(t, s1, s2, "same seed must yield same scalar")
	})

	t.Run("matches raw BIP32 derivation at m/44'/7743'/0'", func(t *testing.T) {
		key, err := DeriveJWTSigningKey(seed)
		require.NoError(t, err)

		child, err := deriveHDChild(seed, hdkeychain.HardenedKeyStart+idxJWT)
		require.NoError(t, err)
		raw, err := child.SerializedPrivKey()
		require.NoError(t, err)

		got := secp256k1.PrivKeyFromBytes(key.D.Bytes()).Serialize() //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D is read-only
		assert.Equal(t, raw, got, "scalar must match raw BIP32 child at idx 0'")
	})
}

func TestDeriveVaultEncKey_Length(t *testing.T) {
	seed := testSeed(t)

	t.Run("returns 32 bytes", func(t *testing.T) {
		enc, err := DeriveVaultEncKey(seed)
		require.NoError(t, err)
		assert.Len(t, enc, 32)
	})

	t.Run("deterministic", func(t *testing.T) {
		enc1, err := DeriveVaultEncKey(seed)
		require.NoError(t, err)
		enc2, err := DeriveVaultEncKey(seed)
		require.NoError(t, err)
		assert.Equal(t, enc1, enc2)
	})

	t.Run("matches BIP32 child scalar at m/44'/7743'/1'", func(t *testing.T) {
		enc, err := DeriveVaultEncKey(seed)
		require.NoError(t, err)

		child, err := deriveHDChild(seed, hdkeychain.HardenedKeyStart+idxVault)
		require.NoError(t, err)
		raw, err := child.SerializedPrivKey()
		require.NoError(t, err)

		assert.Equal(t, raw, enc)
	})
}

func TestDeriveAuditSigningKey_Path(t *testing.T) {
	seed := testSeed(t)

	t.Run("returns secp256k1 ECDSA key", func(t *testing.T) {
		key, err := DeriveAuditSigningKey(seed)
		require.NoError(t, err)
		require.NotNil(t, key)
		assert.Equal(t, "secp256k1", key.Curve.Params().Name)
	})

	t.Run("deterministic", func(t *testing.T) {
		key1, err := DeriveAuditSigningKey(seed)
		require.NoError(t, err)
		key2, err := DeriveAuditSigningKey(seed)
		require.NoError(t, err)
		s1 := secp256k1.PrivKeyFromBytes(key1.D.Bytes()).Serialize() //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D is read-only
		s2 := secp256k1.PrivKeyFromBytes(key2.D.Bytes()).Serialize() //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D is read-only
		assert.Equal(t, s1, s2)
	})

	t.Run("distinct from JWT signing key", func(t *testing.T) {
		jwtKey, err := DeriveJWTSigningKey(seed)
		require.NoError(t, err)
		auditKey, err := DeriveAuditSigningKey(seed)
		require.NoError(t, err)
		sj := secp256k1.PrivKeyFromBytes(jwtKey.D.Bytes()).Serialize()   //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D is read-only
		sa := secp256k1.PrivKeyFromBytes(auditKey.D.Bytes()).Serialize() //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D is read-only
		assert.NotEqual(t, sj, sa, "JWT and audit keys must differ")
	})
}

// TestDerivationErrorPaths exercises the error branches in helpers that are
// unreachable through normal (valid-seed) usage.  Uses public keys and invalid
// seeds to trigger the guarded return paths and achieve 100% statement coverage.
func TestDerivationErrorPaths(t *testing.T) {
	shortSeed := make([]byte, 5) // below hdkeychain MinSeedBytes (16)

	t.Run("DeriveJWTSigningKey propagates NewMaster error", func(t *testing.T) {
		_, err := DeriveJWTSigningKey(shortSeed)
		require.Error(t, err)
	})

	t.Run("DeriveVaultEncKey propagates NewMaster error", func(t *testing.T) {
		_, err := DeriveVaultEncKey(shortSeed)
		require.Error(t, err)
	})

	t.Run("DeriveAuditSigningKey propagates NewMaster error", func(t *testing.T) {
		_, err := DeriveAuditSigningKey(shortSeed)
		require.Error(t, err)
	})

	t.Run("walkPath fails on hardened derivation from public key", func(t *testing.T) {
		seed := testSeed(t)
		master, err := hdkeychain.NewMaster(seed, btcMainNet{})
		require.NoError(t, err)
		pubMaster := master.Neuter()
		_, err = walkPath(pubMaster, []uint32{hdkeychain.HardenedKeyStart + bip44Purpose})
		require.Error(t, err, "hardened child from public key must fail")
	})

	t.Run("ecPrivKeyFromChild propagates SerializedPrivKey error for public key", func(t *testing.T) {
		seed := testSeed(t)
		master, err := hdkeychain.NewMaster(seed, btcMainNet{})
		require.NoError(t, err)
		pubKey := master.Neuter()
		_, err = ecPrivKeyFromChild(pubKey)
		require.Error(t, err, "extracting private key from public ExtendedKey must fail")
	})

	t.Run("serializedChildKey propagates SerializedPrivKey error for public key", func(t *testing.T) {
		seed := testSeed(t)
		master, err := hdkeychain.NewMaster(seed, btcMainNet{})
		require.NoError(t, err)
		pubKey := master.Neuter()
		_, err = serializedChildKey(pubKey)
		require.Error(t, err)
	})
}
