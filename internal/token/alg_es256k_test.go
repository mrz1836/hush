package token

import (
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/golang-jwt/jwt/v5"
)

func freshKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck // secp256k1 is not in crypto/ecdh; S256() is the curve identity
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

func TestRegisterOnce_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	const N = 100
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			Register()
		}()
	}
	wg.Wait()

	method := jwt.GetSigningMethod("ES256K")
	if method == nil {
		t.Fatal("ES256K signing method not registered")
	}
	if method.Alg() != "ES256K" {
		t.Fatalf("Alg() = %q, want %q", method.Alg(), "ES256K")
	}
}

func TestES256KMethod_RoundTrip(t *testing.T) {
	priv := freshKey(t)
	pub := &priv.PublicKey
	const signingInput = "header.payload"

	sig, err := es256kMethod{}.Sign(signingInput, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	method := es256kMethod{}
	if err := method.Verify(signingInput, sig, pub); err != nil {
		t.Fatalf("verify: %v", err)
	}

	tampered := make([]byte, len(sig))
	copy(tampered, sig)
	tampered[len(tampered)-1] ^= 0xff
	verifyErr := es256kMethod{}.Verify(signingInput, tampered, pub)
	if !errors.Is(verifyErr, jwt.ErrTokenSignatureInvalid) {
		t.Fatalf("verify tampered: got %v, want ErrTokenSignatureInvalid", verifyErr)
	}

	_, sErr := es256kMethod{}.Sign(signingInput, "not-a-key")
	if !errors.Is(sErr, jwt.ErrInvalidKeyType) {
		t.Fatalf("sign with non-key: got %v, want ErrInvalidKeyType", sErr)
	}
	vErr := es256kMethod{}.Verify(signingInput, sig, "not-a-key")
	if !errors.Is(vErr, jwt.ErrInvalidKeyType) {
		t.Fatalf("verify with non-key: got %v, want ErrInvalidKeyType", vErr)
	}
}
