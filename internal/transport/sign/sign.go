package sign

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
)

// Sign returns a DER-encoded ASN.1 ECDSA signature over SHA-256(payload).
// Returns ctx.Err() if the context is already canceled at entry.
func Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	return ecdsa.SignASN1(rand.Reader, key, digest[:])
}
