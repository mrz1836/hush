package ecies

import (
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"io"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// SetRandReader replaces the package's entropy source. Returns a cleanup
// function that restores the previous reader.
func SetRandReader(r io.Reader) func() {
	prev := randReader
	randReader = r
	return func() { randReader = prev }
}

// SetAESNewCipher replaces the package's aes.NewCipher hook. Returns a
// cleanup function that restores the previous hook.
func SetAESNewCipher(f func([]byte) (cipher.Block, error)) func() {
	prev := aesNewCipher
	aesNewCipher = f
	return func() { aesNewCipher = prev }
}

// SetSecureBytesNew replaces the package's securebytes.New hook. Returns a
// cleanup function that restores the previous hook.
func SetSecureBytesNew(f func(b []byte) (*securebytes.SecureBytes, error)) func() {
	prev := secureBytesNew
	secureBytesNew = f
	return func() { secureBytesNew = prev }
}

// SetEcdsaGenerate replaces the package's ecdsa.GenerateKey hook. Returns a
// cleanup function that restores the previous hook.
func SetEcdsaGenerate(f func(c elliptic.Curve, r io.Reader) (*ecdsa.PrivateKey, error)) func() {
	prev := ecdsaGenerate
	ecdsaGenerate = f
	return func() { ecdsaGenerate = prev }
}

// MinEnvelopeSize re-exports the file-private constant for test assertions.
const MinEnvelopeSize = minEnvelopeSize

// Pkcs7UnpadForTest re-exports the file-private helper for whitebox tests.
func Pkcs7UnpadForTest(padded []byte, blockSize int) ([]byte, error) {
	return pkcs7Unpad(padded, blockSize)
}

// SecureZeroBigIntForTest re-exports the file-private helper for whitebox tests.
func SecureZeroBigIntForTest() {
	secureZeroBigInt(nil)
}

// CompressPubKeyForTest re-exports the file-private helper for whitebox tests.
func CompressPubKeyForTest(pub *ecdsa.PublicKey) []byte {
	return compressPubKey(pub)
}

// ParseCompressedPubKeyForTest re-exports the file-private helper for
// whitebox tests.
func ParseCompressedPubKeyForTest(b []byte) (*ecdsa.PublicKey, error) {
	return parseCompressedPubKey(b)
}
