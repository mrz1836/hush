package keys

import (
	"crypto/ecdsa"
	"errors"
	"fmt"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/hdkeychain/v3"
)

// ErrScalarTooLong is returned when a BIP32 private scalar exceeds the canonical
// 32-byte width. hdkeychain.SerializedPrivKey can only ever strip leading zero
// bytes (never add them), so an over-length scalar signals upstream corruption
// and must not be silently truncated into an AES/ECDSA key.
var ErrScalarTooLong = errors.New("hush/keys: private scalar too long")

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

// scalarLen is the canonical byte length of a BIP32 / secp256k1 private scalar
// (ser256): a fixed-width, big-endian 256-bit integer.
const scalarLen = 32

// serializedChildKey extracts the private scalar from a BIP32 extended key into
// a fresh, independent buffer that is ALWAYS exactly scalarLen (32) bytes,
// left-padded with leading zeros.
//
// This padding is load-bearing, not cosmetic. hdkeychain.SerializedPrivKey
// returns an alias into child.key whose length is NOT fixed: the Decred variant
// strips leading zero bytes from the child scalar for legacy-wallet
// compatibility (see hdkeychain extendedkey.go: "the Decred variation strips
// leading zeros"). So a scalar whose most-significant byte is 0x00 comes back as
// 31 bytes (~1/256 of derivations), 30 bytes, and so on. Callers that feed the
// result straight into a fixed-size primitive — notably DeriveVaultEncKey, which
// uses it as a 32-byte AES-256 key — would otherwise fail intermittently with
// "crypto/aes: invalid key size 31". Left-padding restores the canonical ser256
// form; because it preserves the big-endian integer value, the ECDSA callers
// (which reduce the bytes mod N via SetByteSlice) derive the identical key, so
// this is fully backward compatible.
//
// The copy also protects callers from a downstream child.Zero() racing with use
// of the returned slice.
func serializedChildKey(child *hdkeychain.ExtendedKey) ([]byte, error) {
	raw, err := child.SerializedPrivKey()
	if err != nil {
		return nil, err
	}
	return leftPadScalar(raw)
}

// leftPadScalar returns raw as a fresh, canonical scalarLen-byte big-endian
// buffer, prepending leading zero bytes when raw is shorter. A raw slice longer
// than scalarLen is rejected: SerializedPrivKey can only ever strip bytes (never
// add them), so an over-length scalar signals corruption upstream and must not
// be silently truncated into an AES/ECDSA key.
func leftPadScalar(raw []byte) ([]byte, error) {
	if len(raw) > scalarLen {
		return nil, fmt.Errorf("%w: %d bytes (max %d)", ErrScalarTooLong, len(raw), scalarLen)
	}
	out := make([]byte, scalarLen)
	copy(out[scalarLen-len(raw):], raw)
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
