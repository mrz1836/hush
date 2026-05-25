package keys

import (
	"crypto/ecdsa"

	"github.com/decred/dcrd/hdkeychain/v3"
)

// DeriveClientKey derives the per-machine client signing keypair used by an agent
// host to authenticate requests to the vault server.
// BIP32 path: m/44'/7743'/3'/{machineIndex}.
//
// machineIndex is passed directly to Child, covering the full uint32 range.
// Indices below HardenedKeyStart yield non-hardened children; at or above yield
// hardened children.  All values succeed and produce distinct keypairs.
//
// The intermediate and final hdkeychain.ExtendedKey nodes derived along the
// path are zeroed before this returns; only the *ecdsa.PrivateKey survives.
func DeriveClientKey(seed []byte, machineIndex uint32) (*ecdsa.PrivateKey, error) {
	child, err := deriveHDChildClient(seed, machineIndex)
	if err != nil {
		return nil, err
	}
	defer child.Zero()
	return ecPrivKeyFromChild(child)
}

// deriveHDChildClient creates a BIP32 master from seed and walks
// m/44'/7743'/3'/{machineIndex}.  Separated from DeriveClientKey to
// keep the exported function's error surface at a single check point.
func deriveHDChildClient(seed []byte, machineIndex uint32) (*hdkeychain.ExtendedKey, error) {
	master, err := hdkeychain.NewMaster(seed, btcMainNet{})
	if err != nil {
		return nil, err
	}
	return walkPath(master, []uint32{
		hdkeychain.HardenedKeyStart + bip44Purpose,
		hdkeychain.HardenedKeyStart + hushCoinType,
		hdkeychain.HardenedKeyStart + idxClientBase,
		machineIndex,
	})
}
