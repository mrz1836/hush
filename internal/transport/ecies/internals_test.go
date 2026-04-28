package ecies

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// TestSecureZeroBigInt_NilIsNoOp asserts the nil-guard branch.
func TestSecureZeroBigInt_NilIsNoOp(t *testing.T) {
	require.NotPanics(t, func() { secureZeroBigInt(nil) })
}

// TestPkcs7Unpad_RejectsEmptyOrMisaligned exercises the empty-or-not-multiple
// branch.
func TestPkcs7Unpad_RejectsEmptyOrMisaligned(t *testing.T) {
	cases := [][]byte{
		{},
		{0x01},
		{0x02, 0x02, 0x02},
	}
	for _, c := range cases {
		_, err := pkcs7Unpad(c, aes.BlockSize)
		require.ErrorIs(t, err, ErrECIESDecryptFailed)
	}
}

// TestPkcs7Unpad_RejectsBadPadLen exercises the padLen==0 and padLen>blockSize
// branches.
func TestPkcs7Unpad_RejectsBadPadLen(t *testing.T) {
	zeroLast := make([]byte, aes.BlockSize)
	_, err := pkcs7Unpad(zeroLast, aes.BlockSize)
	require.ErrorIs(t, err, ErrECIESDecryptFailed)

	tooLarge := make([]byte, aes.BlockSize)
	tooLarge[len(tooLarge)-1] = byte(aes.BlockSize + 1)
	_, err = pkcs7Unpad(tooLarge, aes.BlockSize)
	require.ErrorIs(t, err, ErrECIESDecryptFailed)
}

// TestPkcs7Unpad_RejectsMismatchedPaddingByte exercises the mismatched-byte
// branch.
func TestPkcs7Unpad_RejectsMismatchedPaddingByte(t *testing.T) {
	padded := make([]byte, aes.BlockSize)
	padded[len(padded)-1] = 0x04 // claims 4 bytes of padding…
	padded[len(padded)-4] = 0x00 // …but one of the four is wrong.
	_, err := pkcs7Unpad(padded, aes.BlockSize)
	require.ErrorIs(t, err, ErrECIESDecryptFailed)
}

// TestPkcs7Unpad_HappyPath exercises the success branch.
func TestPkcs7Unpad_HappyPath(t *testing.T) {
	padded := []byte{0x41, 0x42, 0x43, 0x01}
	got, err := pkcs7Unpad(padded, 4)
	require.NoError(t, err)
	require.Equal(t, []byte{0x41, 0x42, 0x43}, got)
}

// errForcedTestFailure is a static sentinel used by the seam-injection tests
// to force defensive error paths that are unreachable in production.
var errForcedTestFailure = errors.New("ecies test seam: forced failure")

// TestEncrypt_RandReaderError forces the ecdsa.GenerateKey error path.
func TestEncrypt_RandReaderError(t *testing.T) {
	priv := generateFreshKey(t)

	cleanup := SetEcdsaGenerate(func(_ elliptic.Curve, _ io.Reader) (*ecdsa.PrivateKey, error) {
		return nil, errForcedTestFailure
	})
	defer cleanup()

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, []byte("trigger"))
	require.Error(t, err)
	require.Nil(t, envelope)
}

// TestEncrypt_AESNewCipherError forces the aes.NewCipher error path inside
// Encrypt.
func TestEncrypt_AESNewCipherError(t *testing.T) {
	priv := generateFreshKey(t)

	cleanup := SetAESNewCipher(func(_ []byte) (cipher.Block, error) {
		return nil, errForcedTestFailure
	})
	defer cleanup()

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, []byte("trigger"))
	require.Error(t, err)
	require.Nil(t, envelope)
}

// TestDecrypt_AESNewCipherError forces the aes.NewCipher error path inside
// Decrypt by toggling the seam after the (Encrypt-side) call to bake an
// envelope, then before Decrypt runs.
func TestDecrypt_AESNewCipherError(t *testing.T) {
	priv := generateFreshKey(t)

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, []byte("decrypt-aes-error"))
	require.NoError(t, err)

	cleanup := SetAESNewCipher(func(_ []byte) (cipher.Block, error) {
		return nil, errForcedTestFailure
	})
	defer cleanup()

	sb, err := Decrypt(t.Context(), priv, envelope)
	require.ErrorIs(t, err, ErrECIESDecryptFailed)
	require.Nil(t, sb)
}

// TestDecrypt_SecureBytesNewError forces the securebytes.New error path
// inside Decrypt.
func TestDecrypt_SecureBytesNewError(t *testing.T) {
	priv := generateFreshKey(t)

	envelope, err := Encrypt(t.Context(), &priv.PublicKey, []byte("decrypt-sb-error"))
	require.NoError(t, err)

	cleanup := SetSecureBytesNew(func(_ []byte) (*securebytes.SecureBytes, error) {
		return nil, errForcedTestFailure
	})
	defer cleanup()

	sb, err := Decrypt(t.Context(), priv, envelope)
	require.ErrorIs(t, err, ErrECIESDecryptFailed)
	require.Nil(t, sb)
}

// TestDecrypt_NilPriv covers the nil-priv defensive gate inside Decrypt.
// The envelope is a synthetic minimum-sized blob that passes the length gate
// and lands on the priv check.
func TestDecrypt_NilPriv(t *testing.T) {
	envelope := make([]byte, minEnvelopeSize)
	copy(envelope[0:bie1MagicLen], bie1Magic)

	sb, err := Decrypt(t.Context(), nil, envelope)
	require.ErrorIs(t, err, ErrECIESDecryptFailed)
	require.Nil(t, sb)
}

// TestDecrypt_PartiallyConstructedPriv covers the priv.Curve == nil and
// priv.D == nil branches.
func TestDecrypt_PartiallyConstructedPriv(t *testing.T) {
	envelope := make([]byte, minEnvelopeSize)
	copy(envelope[0:bie1MagicLen], bie1Magic)

	cases := []*ecdsa.PrivateKey{
		{}, // Curve is nil
		{PublicKey: ecdsa.PublicKey{Curve: ellipticS()}}, // D is nil
	}
	for _, priv := range cases {
		sb, err := Decrypt(t.Context(), priv, envelope)
		require.ErrorIs(t, err, ErrECIESDecryptFailed)
		require.Nil(t, sb)
	}
}

// ellipticS returns the package's expected curve singleton via the test file
// to avoid pulling secp256k1 into ecies_test.go's whitebox region.
func ellipticS() elliptic.Curve {
	return generateFreshKeyForCurve().Curve
}

func generateFreshKeyForCurve() *ecdsa.PublicKey {
	// Reuse the shared test helper to obtain a secp256k1 *ecdsa.PublicKey
	// without re-importing the curve package here.
	pub, _ := parseCompressedPubKey(canonicalGeneratorCompressed())
	return pub
}

// canonicalGeneratorCompressed returns the compressed form of the secp256k1
// generator point. Used only as a curve-identity fixture for the
// PartiallyConstructedPriv test; never as a real key.
func canonicalGeneratorCompressed() []byte {
	return []byte{
		0x02,
		0x79, 0xbe, 0x66, 0x7e, 0xf9, 0xdc, 0xbb, 0xac,
		0x55, 0xa0, 0x62, 0x95, 0xce, 0x87, 0x0b, 0x07,
		0x02, 0x9b, 0xfc, 0xdb, 0x2d, 0xce, 0x28, 0xd9,
		0x59, 0xf2, 0x81, 0x5b, 0x16, 0xf8, 0x17, 0x98,
	}
}

// TestDecrypt_BadPaddingThroughForgedEnvelope drives the pkcs7Unpad error
// path inside Decrypt by forging an envelope whose AES-decrypted plaintext
// has malformed padding, then re-computing the MAC so the gate passes.
//
// This is the only way to exercise the unpad branch through the public
// Decrypt API — with a normal envelope the MAC check rejects mutations
// before unpad ever runs.
func TestDecrypt_BadPaddingThroughForgedEnvelope(t *testing.T) {
	priv := generateFreshKey(t)
	envelope, err := Encrypt(t.Context(), &priv.PublicKey, []byte("forged-padding-input"))
	require.NoError(t, err)

	// Decrypt up to and including the AES decrypt step manually so we can
	// scribble over plaintextBuf with deliberately-malformed padding bytes,
	// then re-encrypt with a known-bad block, recompute the MAC, and assert
	// Decrypt returns ErrECIESDecryptFailed via the unpad path.
	ctEnd := len(envelope) - sha256.Size
	ctStart := bie1MagicLen + compressedPubKeyLen

	ephPub, err := parseCompressedPubKey(envelope[bie1MagicLen:ctStart])
	require.NoError(t, err)

	sharedX := ecdh(priv.Curve, ephPub.X, ephPub.Y, priv.D) //nolint:staticcheck // secp256k1 is not in crypto/ecdh; *big.Int field access is the only path
	hArr, iv, keyE, keyM := kdf(sharedX)
	defer secureZero(hArr[:])
	defer secureZero(sharedX)

	// Build an all-zeros plaintext block (last byte = 0 → padding invalid).
	bad := make([]byte, aes.BlockSize)
	block, err := aes.NewCipher(keyE)
	require.NoError(t, err)
	cbc := cipher.NewCBCEncrypter(block, iv)
	badCT := make([]byte, aes.BlockSize)
	cbc.CryptBlocks(badCT, bad)

	forged := make([]byte, 0, ctEnd-ctStart+sha256.Size+ctStart)
	forged = append(forged, envelope[:ctStart]...)
	forged = append(forged, badCT...)

	// Recompute MAC over magic ‖ ephPub ‖ badCT.
	mac := hmac.New(sha256.New, keyM)
	mac.Write(forged)
	tag := mac.Sum(nil)
	forged = append(forged, tag...)

	sb, err := Decrypt(t.Context(), priv, forged)
	require.ErrorIs(t, err, ErrECIESDecryptFailed)
	require.Nil(t, sb)
}
