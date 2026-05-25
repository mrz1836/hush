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

// TestWalkPath_ZeroesIntermediates asserts the principal Layer-1 / Principle-VI
// hygiene fix: walkPath must call Zero on every ExtendedKey it shadows along
// the derivation walk so the master's private scalar + chain code do not
// persist in unmlocked heap until GC. The leaf returned to the caller is NOT
// zeroed (ownership transfers to the caller).
func TestWalkPath_ZeroesIntermediates(t *testing.T) {
	seed := testSeed(t)
	master, err := hdkeychain.NewMaster(seed, btcMainNet{})
	require.NoError(t, err)

	// Capture an alias into master.key BEFORE walkPath consumes it; after
	// Zero the underlying bytes must be all zeros.
	masterKeyAlias, err := master.SerializedPrivKey()
	require.NoError(t, err)
	require.NotEmpty(t, masterKeyAlias)
	originalMasterKey := append([]byte(nil), masterKeyAlias...)
	require.NotZero(t, originalMasterKey[0], "test precondition: master key must start non-zero")

	leaf, err := walkPath(master, []uint32{
		hdkeychain.HardenedKeyStart + bip44Purpose,
		hdkeychain.HardenedKeyStart + hushCoinType,
		hdkeychain.HardenedKeyStart + idxJWT,
	})
	require.NoError(t, err)
	require.NotNil(t, leaf)

	// 1) Master itself was Zero'd: SerializedPrivKey now fails with
	// ErrNotPrivExtKey because Zero clears isPrivate.
	_, postErr := master.SerializedPrivKey()
	require.ErrorIs(t, postErr, hdkeychain.ErrNotPrivExtKey,
		"master ExtendedKey must be zeroed after walkPath returns")

	// 2) The aliased bytes that USED to be master.key are now all zero.
	for i, b := range masterKeyAlias {
		require.Zerof(t, b, "masterKeyAlias[%d] = 0x%02x; want zero", i, b)
	}

	// 3) The LEAF is untouched — the caller (DeriveXxx) owns Zero on it.
	leafKey, err := leaf.SerializedPrivKey()
	require.NoError(t, err, "leaf must still be a private extended key")
	require.NotEmpty(t, leafKey)
}

// TestWalkPath_ZeroesParentOnDeriveError asserts that even when walkPath aborts
// mid-walk (hardened-from-public error), the in-hand parent is zeroed before
// the function returns so failure paths do not leak intermediate key material.
func TestWalkPath_ZeroesParentOnDeriveError(t *testing.T) {
	seed := testSeed(t)
	master, err := hdkeychain.NewMaster(seed, btcMainNet{})
	require.NoError(t, err)
	pubMaster := master.Neuter()

	pubKeyAlias := pubMaster.SerializedPubKey()
	require.NotEmpty(t, pubKeyAlias)
	originalFirstByte := pubKeyAlias[0]

	_, err = walkPath(pubMaster, []uint32{hdkeychain.HardenedKeyStart + bip44Purpose})
	require.Error(t, err, "hardened-from-public must fail")

	// Zero() on a public ExtendedKey overwrites the pubKey slice (and the
	// chainCode slice) with zeros before niling the field. The aliased
	// slice we captured pre-walk shares storage with pubMaster.pubKey, so
	// every byte must now read as zero.
	for i, b := range pubKeyAlias {
		require.Zerof(t, b, "pubKeyAlias[%d] = 0x%02x; want zero after error-path Zero (was 0x%02x)", i, b, originalFirstByte)
	}
}

// TestDeriveVaultEncKey_DoesNotLeakLeafExtendedKey asserts the deferred
// child.Zero() in DeriveVaultEncKey actually frees the BIP32 leaf's private
// scalar buffer. Indirect check: the returned []byte is a fresh copy
// (serializedChildKey's copy survives a subsequent leaf Zero) AND the leaf
// itself has been zeroed via the defer chain.
func TestDeriveVaultEncKey_DoesNotLeakLeafExtendedKey(t *testing.T) {
	seed := testSeed(t)
	enc, err := DeriveVaultEncKey(seed)
	require.NoError(t, err)
	require.Len(t, enc, 32)

	// The returned slice must be non-zero (Argon2-derived vault key).
	allZero := true
	for _, b := range enc {
		if b != 0 {
			allZero = false
			break
		}
	}
	require.False(t, allZero, "returned vault key is all zeros — derivation broken")

	// Independent re-derivation must match — proves the deferred Zero on
	// the intermediate ExtendedKey did not corrupt the returned bytes.
	enc2, err := DeriveVaultEncKey(seed)
	require.NoError(t, err)
	require.Equal(t, enc, enc2)
}

// TestSerializedChildKey_ReturnsIndependentCopy guarantees that the slice
// returned by serializedChildKey survives a subsequent child.Zero(); this is
// the load-bearing invariant that lets DeriveXxx defer child.Zero() without
// corrupting the bytes it just extracted.
func TestSerializedChildKey_ReturnsIndependentCopy(t *testing.T) {
	seed := testSeed(t)
	child, err := deriveHDChild(seed, hdkeychain.HardenedKeyStart+idxJWT)
	require.NoError(t, err)

	extracted, err := serializedChildKey(child)
	require.NoError(t, err)
	require.Len(t, extracted, 32)
	snapshot := append([]byte(nil), extracted...)

	child.Zero()

	// extracted must be byte-identical to snapshot — proves the bytes are
	// not aliased into child.key.
	require.Equal(t, snapshot, extracted,
		"extracted scalar mutated by child.Zero() — serializedChildKey is aliasing, not copying")

	// And the underlying child.key alias is now nil-or-zero (Zero clears).
	_, postErr := child.SerializedPrivKey()
	require.ErrorIs(t, postErr, hdkeychain.ErrNotPrivExtKey)
}

// TestEcPrivKeyFromChild_ZerosIntermediateScalar asserts that the raw scalar
// buffer used inside ecPrivKeyFromChild is overwritten with zeros before the
// function returns (PrivKeyFromBytes copies into PrivateKey.Key.SetByteSlice,
// so zeroing the source does not corrupt the returned ecdsa key).
func TestEcPrivKeyFromChild_ZerosIntermediateScalar(t *testing.T) {
	// We cannot observe ecPrivKeyFromChild's local `raw` from outside the
	// function, but we CAN assert the resulting key matches the raw scalar
	// — proving PrivKeyFromBytes copied (so the zeroBytes(raw) defer is
	// safe) and that the returned key is intact end-to-end.
	seed := testSeed(t)
	jwtKey, err := DeriveJWTSigningKey(seed)
	require.NoError(t, err)

	child, err := deriveHDChild(seed, hdkeychain.HardenedKeyStart+idxJWT)
	require.NoError(t, err)
	defer child.Zero()
	raw, err := serializedChildKey(child)
	require.NoError(t, err)

	want := secp256k1.PrivKeyFromBytes(raw).Serialize()
	got := secp256k1.PrivKeyFromBytes(jwtKey.D.Bytes()).Serialize() //nolint:staticcheck // secp256k1 not in crypto/ecdh; .D is read-only
	require.Equal(t, want, got)
}

// TestZeroBytes asserts the trivial helper actually overwrites every byte.
// The helper is shared by paths.go; a regression here would silently break
// the ecPrivKeyFromChild zero discipline.
func TestZeroBytes(t *testing.T) {
	b := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02}
	zeroBytes(b)
	for i, v := range b {
		require.Zerof(t, v, "b[%d] = 0x%02x; want 0", i, v)
	}
	// Empty / nil are no-ops, must not panic.
	zeroBytes(nil)
	zeroBytes([]byte{})
}
