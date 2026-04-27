package keys

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
)

// PublicKeyFingerprint returns the 16-character lowercase hex fingerprint used in
// operator-facing client-registration UX.
//
// Algorithm: hex.EncodeToString(sha256(SEC1_compressed(pub))[:8]).
// The result is stable across processes and machines; distinct public keys produce
// distinct fingerprints with overwhelming probability.
func PublicKeyFingerprint(pub *ecdsa.PublicKey) string {
	compressed := make([]byte, 33)
	// secp256k1 is not supported by crypto/ecdh (Go 1.26), so pub.Bytes()
	// always errors for our keys. .X and .Y are read-only here; the deprecation
	// warning is about mutation safety, which does not apply to this read path.
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .X/.Y are read-only
	if pub.Y.Bit(0) == 0 {
		compressed[0] = 0x02
	} else {
		compressed[0] = 0x03
	}
	//nolint:staticcheck // see above
	xBytes := pub.X.Bytes()
	copy(compressed[1+32-len(xBytes):], xBytes)
	digest := sha256.Sum256(compressed)
	return hex.EncodeToString(digest[:8])
}
