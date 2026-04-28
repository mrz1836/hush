// Package ecies implements the Layer-3 wire-level secret-response encryption
// for hush: BIE1 ECIES over secp256k1 (4-byte magic, 33-byte compressed
// ephemeral public key, AES-256-CBC ciphertext, 32-byte HMAC-SHA256 tag).
//
// The server side runs [Encrypt] with the client's per-request ephemeral
// public key (delivered in the JWT's ephemeral_pubkey claim) and produces an
// opaque envelope; the client side runs [Decrypt] with the matching ephemeral
// private key and recovers the plaintext as a fresh
// [*securebytes.SecureBytes] whose lifetime the caller owns.
//
// Constitutional principles in scope: III (Layer 3 — ECIES transport
// encryption), VIII (100% coverage + fuzz target #3), IX (context-first, no
// init, no globals beyond sentinels), X (no envelope/plaintext/key bytes in
// logs or errors), XI (native-first, minimal deps).
//
// Layer-3 roster:
//   - [Encrypt] — server-side BIE1 ECIES encrypt; opaque envelope output
//   - [Decrypt] — client-side BIE1 ECIES decrypt; returns *SecureBytes
//   - [ErrECIESDecryptFailed] — every cryptographic decrypt failure
//   - [ErrECIESEnvelopeTooShort] — envelope length below the documented minimum
//   - [ErrECIESEmptyPlaintext] — Encrypt rejection: zero-length plaintext
//   - [ErrECIESInvalidRecipientKey] — Encrypt rejection: nil/wrong-curve pub
package ecies
