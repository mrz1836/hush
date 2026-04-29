package token

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"sync"

	"github.com/golang-jwt/jwt/v5"
)

// es256kMethod is the custom jwt.SigningMethod for ES256K (ECDSA over
// secp256k1 with SHA-256 digest, ASN.1 DER signature framing).
type es256kMethod struct{}

func (es256kMethod) Alg() string { return "ES256K" }

func (es256kMethod) Sign(signingInput string, key any) ([]byte, error) {
	priv, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, jwt.ErrInvalidKeyType
	}
	digest := sha256.Sum256([]byte(signingInput))
	return ecdsa.SignASN1(rand.Reader, priv, digest[:])
}

// signEncoded signs the canonical JWT for Issue. Wrapping
// jwt.NewWithClaims in a function gives us a testable seam for the
// SignedString error path without exposing internal types.
//
//nolint:gochecknoglobals // sentinel-class test seam; set-once at package load, replaced only by tests
var signEncoded = func(claims jwt.Claims, signKey *ecdsa.PrivateKey) (string, error) {
	return jwt.NewWithClaims(es256kMethod{}, claims).SignedString(signKey)
}

func (es256kMethod) Verify(signingInput string, sig []byte, key any) error {
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return jwt.ErrInvalidKeyType
	}
	digest := sha256.Sum256([]byte(signingInput))
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return jwt.ErrTokenSignatureInvalid
	}
	return nil
}

//nolint:gochecknoglobals // bounded sync.Once exception per chunk contract; ES256K registration must happen once-only without init()
var registerOnce sync.Once

// Register installs the ES256K signing method exactly once. Issue and
// Validate call it before any JWT work; consumers MUST NOT call it
// directly.
func Register() {
	registerOnce.Do(func() {
		jwt.RegisterSigningMethod("ES256K", func() jwt.SigningMethod {
			return es256kMethod{}
		})
	})
}
