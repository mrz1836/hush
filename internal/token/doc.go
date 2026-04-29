// Package token implements the Layer-2 session-authority for hush:
// ES256K JWT issuance, validation, in-memory revocation/use-count store,
// and the cleanup loop that reclaims expired live records.
//
// Constitutional principles in scope: III (Layer 2 — asymmetric JWT
// signing), IV (TTL discipline — supervisor TTL-only, interactive
// TTL+max-uses), VIII (100% coverage + fuzz target #2), IX (no init,
// ctx-first, caller-owned goroutines), X (no JWT bytes in logs/errors).
//
// Layer-2 roster:
//   - [Issue] — sign Claims with ES256K via stdlib crypto/ecdsa
//   - [Validate] — parse, verify, decrement use, return *Claims
//   - [Store] — in-memory live + revoked maps under sync.RWMutex
//   - [NewStore] / [NewStoreWithTick] — concurrent-safe constructors
//   - Sentinel errors — seven distinct, comparable identities
//
// The package never logs, never spawns goroutines, never reads or
// writes disk. Cleanup is a synchronous method whose caller owns the
// goroutine. Error messages are static category strings; encoded JWT
// bytes never appear in any error.
package token
