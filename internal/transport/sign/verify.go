package sign

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"fmt"
)

// Verify checks that sig is a valid DER-encoded ECDSA signature of
// SHA-256(payload) by key. Returns nil on success. Returns a wrapped
// [ErrSignatureInvalid] for every signature failure — wrong key, tampered
// payload, malformed DER, nil/incomplete key — so callers cannot
// distinguish failure modes. Returns ctx.Err() if the context
// is already canceled at entry.
func Verify(ctx context.Context, key *ecdsa.PublicKey, payload, sig []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key == nil || key.Curve == nil || key.X == nil || key.Y == nil { //nolint:staticcheck // secp256k1 not in crypto/ecdh; field access is intentional
		return fmt.Errorf("hush/transport/sign: verify: %w", ErrSignatureInvalid)
	}
	digest := sha256.Sum256(payload)
	if !ecdsa.VerifyASN1(key, digest[:], sig) {
		return fmt.Errorf("hush/transport/sign: verify: %w", ErrSignatureInvalid)
	}
	return nil
}
