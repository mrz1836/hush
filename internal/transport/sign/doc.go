// Package sign implements the Layer-4 request-signing protocol for hush:
// canonical-JSON encoding, ECDSA sign/verify over SHA-256(canonical), and
// nonce + timestamp replay protection.
//
// Constitutional principles in scope: III (Layer 4), VIII (100% coverage +
// fuzz target #4), IX (no init, explicit goroutine lifecycle), X (no nonces
// in logs), XI (native-first, minimal deps).
//
// Layer-4 roster:
//   - [CanonicalJSON] — deterministic JSON encoding (sorted keys at every depth)
//   - [Sign] — ECDSA over SHA-256(payload) via stdlib crypto/ecdsa
//   - [Verify] — mirrors Sign; returns [ErrSignatureInvalid] for every failure
//   - [NonceCache] — sync.Map-backed nonce store; sweep started by [NonceCache.Run]
//   - [IsFreshTimestamp] — symmetric ±skew freshness check
//
// Composition recipe (anti-burn ordering):
//
//	canonical → Verify → IsFreshTimestamp → NonceCache.Add
package sign
