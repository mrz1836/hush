package cli

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"io"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/transport/sign"
)

// ephemeralRevokeKey generates a fresh secp256k1 keypair for one
// revoke call when the production wiring did not supply a derived
// client key. The key never leaves the process and is discarded
// when runRevoke returns; the server rejects the signature with
// 401/403 unless the caller has wired a registered key. This is the
// stand-in path until SDD-15 / SDD-23 land local key derivation
// for the operator-side `client` flow.
func ephemeralRevokeKey(r io.Reader) (*ecdsa.PrivateKey, error) {
	scalar := make([]byte, 32)
	if _, err := io.ReadFull(r, scalar); err != nil {
		return nil, err
	}
	priv := secp256k1.PrivKeyFromBytes(scalar)
	return priv.ToECDSA(), nil
}

// canonicaliseRevokePayload produces the canonical-JSON byte form of
// the revoke payload via internal/transport/sign.CanonicalJSON
// (locked at SDD-08). The returned bytes are what the client signs
// and what the server re-canonicalises to verify.
func canonicaliseRevokePayload(p revokePayload) ([]byte, error) {
	return sign.CanonicalJSON(p)
}

// signRevokePayload signs the canonical bytes via SDD-08's Sign and
// returns the base64-standard-encoded signature ready for the
// envelope's Signature field.
func signRevokePayload(ctx context.Context, key *ecdsa.PrivateKey, canonical []byte) (string, error) {
	sig, err := sign.Sign(ctx, key, canonical)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}
