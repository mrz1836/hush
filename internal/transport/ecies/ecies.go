package ecies

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"io"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Test seams. Replaced by export_test.go in unit tests to exercise defensive
// error paths that are unreachable in production (the underlying primitives
// fail only on broken OS entropy or invalid key sizes — both impossible
// through the public API).
//
//nolint:gochecknoglobals // sentinel-class test seams; set-once at package load, replaced only by tests
var (
	randReader     io.Reader                                                      = rand.Reader
	aesNewCipher                                                                  = aes.NewCipher
	secureBytesNew func(b []byte) (*securebytes.SecureBytes, error)               = securebytes.New
	ecdsaGenerate  func(c elliptic.Curve, r io.Reader) (*ecdsa.PrivateKey, error) = ecdsa.GenerateKey
)

// minEnvelopeSize is the BIE1 envelope floor: 4-byte magic + 33-byte
// compressed ephemeral pubkey + 1 AES block (PKCS#7-padded plaintext) +
// 32-byte HMAC-SHA256 tag = 85 bytes. Decrypt rejects shorter envelopes
// with [ErrECIESEnvelopeTooShort] before any cryptographic primitive runs.
const minEnvelopeSize = 4 + 33 + aes.BlockSize + sha256.Size

// bie1MagicLen is the length of the BIE1 magic prefix.
const bie1MagicLen = 4

// compressedPubKeyLen is the length of a secp256k1 compressed pubkey.
const compressedPubKeyLen = 33

// sharedXLen is the fixed-width big-endian X coordinate length.
const sharedXLen = 32

// bie1Magic is the 4-byte BIE1 ASCII prefix.
//
//nolint:gochecknoglobals // sentinel-class literal, set-once at package load, never mutated
var bie1Magic = []byte{'B', 'I', 'E', '1'}

// secureZero overwrites every byte of buf with 0. The simple loop pattern is
// preserved across Go compiler versions for slices reachable through a defer
// registration; see SDD-09 research R-005.
func secureZero(buf []byte) {
	for i := range buf {
		buf[i] = 0
	}
}

// secureZeroBigInt zeros the underlying word slice of n via SetInt64(0) and
// also overwrites the byte representation defensively. nil inputs are a no-op.
func secureZeroBigInt(n *big.Int) {
	if n == nil {
		return
	}
	bz := n.Bytes()
	secureZero(bz)
	n.SetInt64(0)
}

// validateRecipientPub returns nil if pub is a usable secp256k1 public key.
//
// Checks performed:
//  1. Non-nil pointer with non-nil Curve, X, Y.
//  2. Curve identity matches secp256k1.S256() (cheap value-equality of the
//     curve singleton; rejects keys built against P-256 or another curve).
//  3. (X, Y) actually satisfies y² ≡ x³ + 7 (mod p) — i.e. lies on the
//     secp256k1 curve. Without this, an attacker-supplied (X, Y) on a
//     curve twist would still produce ECDH output but on a small subgroup,
//     leaking key bits.
//  4. Coordinates are in [1, p-1] (i.e. not zero, not >= p) and not the
//     point at infinity. ScalarMult on the identity is meaningless.
func validateRecipientPub(pub *ecdsa.PublicKey) error {
	if pub == nil || pub.Curve == nil || pub.X == nil || pub.Y == nil { //nolint:staticcheck // secp256k1 is not in crypto/ecdh; *big.Int field access is the only path
		return ErrECIESInvalidRecipientKey
	}
	if pub.Curve != secp256k1.S256() { //nolint:staticcheck // secp256k1 is not in crypto/ecdh; S256() is the curve identity
		return ErrECIESInvalidRecipientKey
	}
	p := pub.Curve.Params().P
	if !coordsInFieldNonInfinity(pub.X, pub.Y, p) { //nolint:staticcheck // see above
		return ErrECIESInvalidRecipientKey
	}
	if !pointOnSecp256k1(pub.X, pub.Y, p) { //nolint:staticcheck // see above
		return ErrECIESInvalidRecipientKey
	}
	return nil
}

// coordsInFieldNonInfinity returns true iff (x, y) is neither the point at
// infinity (X=0 ∧ Y=0) nor out-of-field (any coordinate negative or >= p).
func coordsInFieldNonInfinity(x, y, p *big.Int) bool {
	if x.Sign() == 0 && y.Sign() == 0 {
		return false
	}
	if x.Sign() < 0 || x.Cmp(p) >= 0 {
		return false
	}
	if y.Sign() < 0 || y.Cmp(p) >= 0 {
		return false
	}
	return true
}

// pointOnSecp256k1 verifies (x, y) satisfies y² ≡ x³ + 7 (mod p), the
// secp256k1 curve equation (B = 7).
func pointOnSecp256k1(x, y, p *big.Int) bool {
	yy := new(big.Int).Mul(y, y)
	yy.Mod(yy, p)
	xxx := new(big.Int).Mul(x, x)
	xxx.Mul(xxx, x)
	xxx.Add(xxx, big.NewInt(7))
	xxx.Mod(xxx, p)
	return yy.Cmp(xxx) == 0
}

// compressPubKey returns the 33-byte BIE1-style compressed encoding of a
// secp256k1 public key: prefix byte (0x02 for even Y, 0x03 for odd Y) followed
// by the 32-byte big-endian X coordinate.
func compressPubKey(pub *ecdsa.PublicKey) []byte {
	out := make([]byte, compressedPubKeyLen)
	if pub.Y.Bit(0) == 0 { //nolint:staticcheck // secp256k1 is not in crypto/ecdh; *big.Int field access is the only path
		out[0] = 0x02
	} else {
		out[0] = 0x03
	}
	pub.X.FillBytes(out[1:]) //nolint:staticcheck // secp256k1 is not in crypto/ecdh; *big.Int field access is the only path
	return out
}

// parseCompressedPubKey parses a 33-byte compressed encoding and returns the
// corresponding [*ecdsa.PublicKey]. Any failure returns
// [ErrECIESDecryptFailed]; the parser validates length, prefix byte, on-curve
// membership, and rejects the point at infinity via [secp256k1.ParsePubKey].
func parseCompressedPubKey(b []byte) (*ecdsa.PublicKey, error) {
	pub, err := secp256k1.ParsePubKey(b)
	if err != nil {
		return nil, ErrECIESDecryptFailed
	}
	return pub.ToECDSA(), nil
}

// ecdh returns the 32-byte big-endian X coordinate of (scalar * (peerX, peerY))
// on curve. The output is fixed-width regardless of leading-zero situation.
func ecdh(curve elliptic.Curve, peerX, peerY, scalar *big.Int) []byte {
	sharedX, _ := curve.ScalarMult(peerX, peerY, scalar.Bytes()) //nolint:staticcheck // secp256k1 is not in crypto/ecdh; ScalarMult is the right primitive here
	out := make([]byte, sharedXLen)
	sharedX.FillBytes(out)
	secureZeroBigInt(sharedX)
	return out
}

// kdf splits SHA-512(sharedX) into the BIE1 IV/keyE/keyM slices. The caller
// owns secureZero of the returned H array — pass it back through closure or
// register defer secureZero(H[:]) at the call site.
func kdf(sharedX []byte) (h [sha512.Size]byte, iv, keyE, keyM []byte) {
	h = sha512.Sum512(sharedX)
	iv = h[0:16]
	keyE = h[16:48]
	keyM = h[48:64]
	return h, iv, keyE, keyM
}

// pkcs7Pad appends padding bytes per RFC 5652 §6.3 so that the result length
// is a positive multiple of blockSize.
func pkcs7Pad(plaintext []byte, blockSize int) []byte {
	padLen := blockSize - (len(plaintext) % blockSize)
	out := make([]byte, len(plaintext)+padLen)
	copy(out, plaintext)
	for i := len(plaintext); i < len(out); i++ {
		out[i] = byte(padLen) //nolint:gosec // padLen is bounded by blockSize (16) so the int→byte conversion cannot overflow
	}
	return out
}

// pkcs7Unpad strips RFC 5652 §6.3 padding from a slice whose length is a
// positive multiple of blockSize. Any malformedness (last byte zero, last byte
// > blockSize, mismatched padding bytes) returns [ErrECIESDecryptFailed].
//
// The returned slice shares storage with padded; the caller is responsible for
// secureZero on the underlying buffer.
func pkcs7Unpad(padded []byte, blockSize int) ([]byte, error) {
	if len(padded) == 0 || len(padded)%blockSize != 0 {
		return nil, ErrECIESDecryptFailed
	}
	padLen := int(padded[len(padded)-1])
	if padLen == 0 || padLen > blockSize {
		return nil, ErrECIESDecryptFailed
	}
	for i := len(padded) - padLen; i < len(padded); i++ {
		if padded[i] != byte(padLen) { //nolint:gosec // padLen is bounded by blockSize (16) so the int→byte conversion cannot overflow
			return nil, ErrECIESDecryptFailed
		}
	}
	return padded[:len(padded)-padLen], nil
}

// Encrypt produces an opaque BIE1 ECIES envelope of plaintext to recipientPub.
// On success, the returned []byte travels over the wire and is decrypted by
// the matching private key via [Decrypt].
//
// Encrypt copies plaintext into an internal buffer and zeros that buffer on
// every return path; the caller's plaintext slice is not mutated. Encrypt
// generates a fresh ephemeral keypair per call (envelope randomisation is by
// design — the same plaintext encrypts to a different envelope each time).
//
// ctx is checked once at entry; pre-cancellation returns ctx.Err() verbatim
// (preserves errors.Is(err, context.Canceled) and
// errors.Is(err, context.DeadlineExceeded)). Mid-operation cancellation is
// not honored.
//
// Failure cases: pre-canceled ctx, nil/wrong-curve recipientPub
// ([ErrECIESInvalidRecipientKey]), zero-length plaintext
// ([ErrECIESEmptyPlaintext]). All three pre-allocation rejections fire before
// any cryptographic primitive runs.
//
//nolint:funlen,cyclop // the BIE1 encrypt pipeline is sequential and intentionally explicit; splitting hides the defer-zeroing discipline
func Encrypt(ctx context.Context, recipientPub *ecdsa.PublicKey, plaintext []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRecipientPub(recipientPub); err != nil {
		return nil, err
	}
	if len(plaintext) == 0 {
		return nil, ErrECIESEmptyPlaintext
	}

	// Internal plaintext copy. Caller's slice is not mutated.
	pt := make([]byte, len(plaintext))
	copy(pt, plaintext)
	defer secureZero(pt)

	// Ephemeral keypair on the recipient's curve (secp256k1).
	ephPriv, err := ecdsaGenerate(recipientPub.Curve, randReader)
	if err != nil {
		return nil, err
	}
	defer secureZeroBigInt(ephPriv.D) //nolint:staticcheck // secp256k1 is not in crypto/ecdh; *big.Int access is the only path

	// ECDH → shared X (32 bytes, fixed-width).
	sharedX := ecdh(recipientPub.Curve, recipientPub.X, recipientPub.Y, ephPriv.D) //nolint:staticcheck // secp256k1 is not in crypto/ecdh; *big.Int field access is the only path
	defer secureZero(sharedX)

	// KDF → IV, AES-256 key, HMAC key.
	hArr, iv, keyE, keyM := kdf(sharedX)
	defer secureZero(hArr[:])

	// PKCS#7-pad and AES-256-CBC encrypt.
	padded := pkcs7Pad(pt, aes.BlockSize)
	defer secureZero(padded)

	block, err := aesNewCipher(keyE)
	if err != nil {
		return nil, err
	}
	ciphertext := make([]byte, len(padded))
	cbc := cipher.NewCBCEncrypter(block, iv)
	cbc.CryptBlocks(ciphertext, padded)

	// Compress the ephemeral pubkey (33 bytes).
	ephPubCompressed := compressPubKey(&ephPriv.PublicKey)

	// HMAC-SHA256 over magic ‖ ephPub ‖ ciphertext.
	mac := hmac.New(sha256.New, keyM)
	mac.Write(bie1Magic)
	mac.Write(ephPubCompressed)
	mac.Write(ciphertext)
	tag := mac.Sum(nil)

	// Assemble envelope = magic ‖ ephPubCompressed ‖ ciphertext ‖ tag.
	envelope := make([]byte, 0, bie1MagicLen+compressedPubKeyLen+len(ciphertext)+sha256.Size)
	envelope = append(envelope, bie1Magic...)
	envelope = append(envelope, ephPubCompressed...)
	envelope = append(envelope, ciphertext...)
	envelope = append(envelope, tag...)
	return envelope, nil
}

// Decrypt parses a BIE1 ECIES envelope under recipientPriv and returns the
// recovered plaintext as a fresh [*securebytes.SecureBytes]. The caller owns
// the SecureBytes lifetime and must eventually call Destroy().
//
// ctx is checked once at entry; pre-cancellation returns ctx.Err() verbatim.
//
// Failure cases:
//   - pre-canceled ctx → ctx.Err()
//   - len(envelope) < 85 → [ErrECIESEnvelopeTooShort] (before any crypto)
//   - any cryptographic failure (magic, pubkey, MAC, AES, PKCS#7,
//     SecureBytes wrap) → [ErrECIESDecryptFailed]
//
// Decrypt is panic-free under any byte input (asserted by FuzzECIESDecrypt).
// Wrong key and tampered envelope share [ErrECIESDecryptFailed] by design
// (FR-004 — no failure-shape leakage).
//
//nolint:funlen,gocognit,gocyclo,cyclop // the BIE1 decrypt pipeline is sequential and intentionally explicit; splitting hides the gate ordering and defer-zeroing discipline
func Decrypt(ctx context.Context, recipientPriv *ecdsa.PrivateKey, envelope []byte) (*securebytes.SecureBytes, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(envelope) < minEnvelopeSize {
		return nil, ErrECIESEnvelopeTooShort
	}
	if recipientPriv == nil || recipientPriv.Curve == nil || recipientPriv.D == nil { //nolint:staticcheck // secp256k1 is not in crypto/ecdh; *big.Int field access is the only path
		return nil, ErrECIESDecryptFailed
	}
	if !hmac.Equal(envelope[:bie1MagicLen], bie1Magic) {
		return nil, ErrECIESDecryptFailed
	}

	ephPub, err := parseCompressedPubKey(envelope[bie1MagicLen : bie1MagicLen+compressedPubKeyLen])
	if err != nil {
		return nil, err
	}

	ctEnd := len(envelope) - sha256.Size
	ctStart := bie1MagicLen + compressedPubKeyLen
	ctLen := ctEnd - ctStart
	if ctLen <= 0 || ctLen%aes.BlockSize != 0 {
		return nil, ErrECIESDecryptFailed
	}

	// ECDH using the recipient's static private scalar.
	sharedX := ecdh(recipientPriv.Curve, ephPub.X, ephPub.Y, recipientPriv.D) //nolint:staticcheck // secp256k1 is not in crypto/ecdh; *big.Int field access is the only path
	defer secureZero(sharedX)

	hArr, iv, keyE, keyM := kdf(sharedX)
	defer secureZero(hArr[:])

	// MAC verify (constant-time) BEFORE decrypt.
	mac := hmac.New(sha256.New, keyM)
	mac.Write(envelope[:ctEnd])
	expected := mac.Sum(nil)
	if !hmac.Equal(expected, envelope[ctEnd:]) {
		return nil, ErrECIESDecryptFailed
	}

	block, err := aesNewCipher(keyE)
	if err != nil {
		return nil, ErrECIESDecryptFailed
	}
	plaintextBuf := make([]byte, ctLen)
	defer secureZero(plaintextBuf)

	cbc := cipher.NewCBCDecrypter(block, iv)
	cbc.CryptBlocks(plaintextBuf, envelope[ctStart:ctEnd])

	unpadded, err := pkcs7Unpad(plaintextBuf, aes.BlockSize)
	if err != nil {
		return nil, err
	}

	sb, err := secureBytesNew(unpadded)
	if err != nil {
		return nil, ErrECIESDecryptFailed
	}
	return sb, nil
}
