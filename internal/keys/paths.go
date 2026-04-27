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
func DeriveAuditSigningKey(seed []byte) (*ecdsa.PrivateKey, error) {
	child, err := deriveHDChild(seed, hdkeychain.HardenedKeyStart+idxAudit)
	if err != nil {
		return nil, err
	}
	return ecPrivKeyFromChild(child)
}

// DeriveJWTSigningKey derives the secp256k1 ECDSA private key used to sign hush
// JWT session tokens (ES256K).  BIP32 path: m/44'/7743'/0'.
func DeriveJWTSigningKey(seed []byte) (*ecdsa.PrivateKey, error) {
	child, err := deriveHDChild(seed, hdkeychain.HardenedKeyStart+idxJWT)
	if err != nil {
		return nil, err
	}
	return ecPrivKeyFromChild(child)
}

// DeriveVaultEncKey derives the 32-byte symmetric key used to encrypt the vault
// payload with AES-256-GCM.  BIP32 path: m/44'/7743'/1'.
func DeriveVaultEncKey(seed []byte) ([]byte, error) {
	child, err := deriveHDChild(seed, hdkeychain.HardenedKeyStart+idxVault)
	if err != nil {
		return nil, err
	}
	return serializedChildKey(child)
}

// deriveHDChild creates a BIP32 master from seed and walks m/44'/7743'/{childIdx}.
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
func ecPrivKeyFromChild(child *hdkeychain.ExtendedKey) (*ecdsa.PrivateKey, error) {
	raw, err := serializedChildKey(child)
	if err != nil {
		return nil, err
	}
	return scalarToECDSAKey(raw)
}

// scalarToECDSAKey converts a 32-byte BIP32 private scalar to *ecdsa.PrivateKey.
func scalarToECDSAKey(scalar []byte) (*ecdsa.PrivateKey, error) {
	return secp256k1.PrivKeyFromBytes(scalar).ToECDSA(), nil
}

// serializedChildKey extracts the 32-byte private scalar from a BIP32 extended key.
func serializedChildKey(child *hdkeychain.ExtendedKey) ([]byte, error) {
	raw, err := child.SerializedPrivKey()
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, nil
}

// walkPath derives a sequence of BIP32 children from master.
// Fails immediately if any child derivation fails (e.g. hardened from public key).
func walkPath(master *hdkeychain.ExtendedKey, indices []uint32) (*hdkeychain.ExtendedKey, error) {
	key := master
	for _, idx := range indices {
		var err error
		key, err = key.Child(idx)
		if err != nil {
			return nil, err
		}
	}
	return key, nil
}
