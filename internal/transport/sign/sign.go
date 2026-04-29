package sign

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// Sign returns a DER-encoded ASN.1 ECDSA signature over SHA-256(payload).
// Returns ctx.Err() if the context is already canceled at entry.
//
// Returns [ErrSignatureInvalid] (wrapped) when key is nil or
// incompletely-initialized; the underlying ecdsa.SignASN1 may produce
// undefined output for malformed key structs across stdlib versions.
func Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	//nolint:staticcheck // secp256k1 not in crypto/ecdh; field access is intentional
	if key == nil || key.D == nil || key.PublicKey.Curve == nil ||
		key.PublicKey.X == nil || key.PublicKey.Y == nil {
		return nil, fmt.Errorf("hush/transport/sign: sign: %w", ErrSignatureInvalid)
	}
	digest := sha256.Sum256(payload)
	return ecdsa.SignASN1(rand.Reader, key, digest[:])
}
