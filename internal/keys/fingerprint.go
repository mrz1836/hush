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
//
// Preconditions: pub MUST be non-nil with non-nil X and Y on secp256k1.
// Violating any of these (nil pointer, oversized X coordinate) is a caller
// bug — the function panics with a message identifying the violation rather
// than returning a meaningless or undefined fingerprint. A valid secp256k1
// public key always has |X| <= 32 bytes; the bounds check exists strictly
// to catch malformed inputs at the call site rather than producing a
// negative-index slice panic deep in copy().
func PublicKeyFingerprint(pub *ecdsa.PublicKey) string {
	if pub == nil {
		panic("hush/keys: PublicKeyFingerprint: nil public key")
	}
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .X/.Y are read-only
	if pub.X == nil || pub.Y == nil {
		panic("hush/keys: PublicKeyFingerprint: public key X or Y is nil")
	}
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
	if len(xBytes) > 32 {
		panic("hush/keys: PublicKeyFingerprint: X coordinate exceeds 32 bytes (not a valid secp256k1 point)")
	}
	copy(compressed[1+32-len(xBytes):], xBytes)
	digest := sha256.Sum256(compressed)
	return hex.EncodeToString(digest[:8])
}
