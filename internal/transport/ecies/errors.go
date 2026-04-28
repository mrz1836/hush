package ecies

import "errors"

// Sentinel errors. All messages are static category strings; no envelope,
// plaintext, or key byte ever appears in any error message (FR-007).
var (
	// ErrECIESDecryptFailed is returned by [Decrypt] for every cryptographic
	// failure: BIE1 magic mismatch, malformed compressed pubkey, MAC mismatch,
	// AES-block-shape failure, PKCS#7 unpad failure, SecureBytes wrap failure.
	// Wrong key and tampered envelope share this sentinel by design (FR-004).
	ErrECIESDecryptFailed = errors.New("hush/transport/ecies: ECIES decrypt failed")

	// ErrECIESEnvelopeTooShort is returned by [Decrypt] when the envelope is
	// shorter than the documented minimum (85 bytes). Fires BEFORE any
	// cryptographic primitive runs (FR-005).
	ErrECIESEnvelopeTooShort = errors.New("hush/transport/ecies: envelope too short")

	// ErrECIESEmptyPlaintext is returned by [Encrypt] when called with a
	// zero-length plaintext. Fires BEFORE any cryptographic primitive runs
	// (FR-015).
	ErrECIESEmptyPlaintext = errors.New("hush/transport/ecies: empty plaintext")

	// ErrECIESInvalidRecipientKey is returned by [Encrypt] when the recipient
	// public key is nil, has a nil curve, or is not on secp256k1 (FR-015a).
	ErrECIESInvalidRecipientKey = errors.New("hush/transport/ecies: invalid recipient key")
)
