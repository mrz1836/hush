package keys

import (
	"crypto/ecdsa"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/hdkeychain/v3"
)

// BIP32 derivation constants for the hush key hierarchy (coin-type 7743').
const (
	bip44Purpose  = 44
	hushCoinType  = 7743
	idxJWT        = 0
	idxVault      = 1
	idxAudit      = 2
	idxClientBase = 3
)

// btcMainNet supplies Bitcoin mainnet HD key version bytes for hdkeychain.NewMaster.
type btcMainNet struct{}

func (btcMainNet) HDPrivKeyVersion() [4]byte { return [4]byte{0x04, 0x88, 0xAD, 0xE4} }
func (btcMainNet) HDPubKeyVersion() [4]byte  { return [4]byte{0x04, 0x88, 0xB2, 0x1E} }

// DeriveAuditSigningKey derives the secp256k1 ECDSA private key used to sign
// hash-chained audit-log records.  BIP32 path: m/44'/7743'/2'.
//
// The intermediate and final hdkeychain.ExtendedKey nodes derived along the
// path are zeroed before this returns; only the *ecdsa.PrivateKey (with the
// scalar held in its big.Int field) survives.
func DeriveAuditSigningKey(seed []byte) (*ecdsa.PrivateKey, error) {
	child, err := deriveHDChild(seed, hdkeychain.HardenedKeyStart+idxAudit)
	if err != nil {
		return nil, err
	}
	defer child.Zero()
	return ecPrivKeyFromChild(child)
}

// DeriveJWTSigningKey derives the secp256k1 ECDSA private key used to sign hush
// JWT session tokens (ES256K).  BIP32 path: m/44'/7743'/0'.
//
// The intermediate and final hdkeychain.ExtendedKey nodes derived along the
// path are zeroed before this returns; only the *ecdsa.PrivateKey survives.
func DeriveJWTSigningKey(seed []byte) (*ecdsa.PrivateKey, error) {
	child, err := deriveHDChild(seed, hdkeychain.HardenedKeyStart+idxJWT)
	if err != nil {
		return nil, err
	}
	defer child.Zero()
	return ecPrivKeyFromChild(child)
}

// DeriveVaultEncKey derives the 32-byte symmetric key used to encrypt the vault
// payload with AES-256-GCM.  BIP32 path: m/44'/7743'/1'.
//
// The intermediate and final hdkeychain.ExtendedKey nodes are zeroed before
// this returns; the returned []byte is an independent copy that the caller
// owns (and must zero / wrap in SecureBytes).
func DeriveVaultEncKey(seed []byte) ([]byte, error) {
	child, err := deriveHDChild(seed, hdkeychain.HardenedKeyStart+idxVault)
	if err != nil {
		return nil, err
	}
	defer child.Zero()
	return serializedChildKey(child)
}

// deriveHDChild creates a BIP32 master from seed and walks m/44'/7743'/{childIdx}.
// The master and all intermediate ExtendedKey instances are zeroed by walkPath
// before this returns; only the final-leaf ExtendedKey is handed back, and the
// caller is responsible for zeroing it.
func deriveHDChild(seed []byte, childIdx uint32) (*hdkeychain.ExtendedKey, error) {
	master, err := hdkeychain.NewMaster(seed, btcMainNet{})
	if err != nil {
		return nil, err
	}
	return walkPath(master, []uint32{
		hdkeychain.HardenedKeyStart + bip44Purpose,
		hdkeychain.HardenedKeyStart + hushCoinType,
		childIdx,
	})
}

// ecPrivKeyFromChild converts a private BIP32 extended key to *ecdsa.PrivateKey.
// The intermediate scalar slice is zeroed once the *ecdsa.PrivateKey has been
// constructed (secp256k1.PrivKeyFromBytes copies the bytes internally into its
// own field, so zeroing the source does not affect the returned key).
func ecPrivKeyFromChild(child *hdkeychain.ExtendedKey) (*ecdsa.PrivateKey, error) {
	raw, err := serializedChildKey(child)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(raw)
	return scalarToECDSAKey(raw), nil
}

// scalarToECDSAKey converts a 32-byte BIP32 private scalar to *ecdsa.PrivateKey.
// PrivKeyFromBytes copies the slice into PrivateKey.Key via SetByteSlice; the
// caller may zero the input scalar after this returns. Infallible — the
// secp256k1 primitive accepts any 32-byte input by reducing mod N internally.
func scalarToECDSAKey(scalar []byte) *ecdsa.PrivateKey {
	return secp256k1.PrivKeyFromBytes(scalar).ToECDSA()
}

// serializedChildKey extracts the 32-byte private scalar from a BIP32 extended
// key into a fresh, independent buffer. hdkeychain.SerializedPrivKey returns an
// alias into child.key, so the copy here is what protects callers from a
// downstream child.Zero() racing with use of the returned slice.
func serializedChildKey(child *hdkeychain.ExtendedKey) ([]byte, error) {
	raw, err := child.SerializedPrivKey()
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, nil
}

// walkPath derives a sequence of BIP32 children from master, zeroing each
// intermediate ExtendedKey (including master itself) as it is shadowed by its
// child. On error along the way, the in-hand parent is also zeroed before
// return so no extended-key material is leaked into heap via the failure path.
//
// The final-leaf ExtendedKey is NOT zeroed here — ownership passes to the
// caller, which is responsible for calling Zero on it after extracting the
// scalar (see DeriveJWTSigningKey / DeriveAuditSigningKey / DeriveVaultEncKey
// / DeriveClientKey for the pattern).
func walkPath(master *hdkeychain.ExtendedKey, indices []uint32) (*hdkeychain.ExtendedKey, error) {
	key := master
	for _, idx := range indices {
		next, err := key.Child(idx)
		// Zero the parent unconditionally — on the success path it has been
		// superseded by `next`; on the error path it is no longer needed and
		// leaving it populated would leak the master (or an intermediate
		// chain-code) into heap memory until GC.
		key.Zero()
		if err != nil {
			return nil, err
		}
		key = next
	}
	return key, nil
}

// zeroBytes overwrites every byte of b with 0. Shared with client.go.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
