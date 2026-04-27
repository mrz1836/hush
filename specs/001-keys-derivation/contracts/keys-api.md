# Contract — `internal/keys` exported API (SDD-01)

This file is the locked Go API for `internal/keys`. It will be
copied verbatim into `docs/PACKAGE-MAP.md` under
"## `internal/keys/` → ### Exported API — locked" once SDD-01 enters
the implement phase. **No symbol may be added, removed, or
renamed without an SDD amendment.**

Path: `github.com/mrz1836/hush/internal/keys`

```go
package keys

import (
    "context"
    "crypto/ecdsa"
)

// DeriveMasterSeed derives the 64-byte hush master seed from a
// passphrase and a 16-byte salt using Argon2id with the project's
// locked parameters (time=4, memory=256*1024 KiB, threads=4,
// keyLen=64).
//
// The function inspects ctx exactly once at entry. If ctx.Err()
// is non-nil at entry, it is returned without invoking Argon2id.
// Cancellation arriving after entry does NOT abort the in-progress
// derivation (Argon2id is non-interruptible by design).
//
// Returns ErrPassphraseTooShort if len(passphrase) < 12.
// Returns ErrSaltMissing if len(salt) != 16.
// Both validation errors return BEFORE Argon2id runs.
//
// The returned []byte has length 64 and is secret material; the
// caller is responsible for wrapping it in a SecureBytes-style
// container (mlock, zero-on-free).
func DeriveMasterSeed(ctx context.Context, passphrase, salt []byte) ([]byte, error)

// DeriveJWTSigningKey derives the secp256k1 ECDSA private key used
// to sign hush JWT session tokens (ES256K).
// BIP32 path: m/44'/7743'/0'.
//
// Same seed yields the same key; key differs from the audit-signing
// key (FR-007).
func DeriveJWTSigningKey(seed []byte) (*ecdsa.PrivateKey, error)

// DeriveVaultEncKey derives the 32-byte symmetric key used to
// encrypt the vault payload with AES-256-GCM.
// BIP32 path: m/44'/7743'/1'.
//
// The key is the 32-byte private scalar of the BIP32 child node,
// taken directly. Returns a fresh []byte of length 32. Secret
// material — caller is responsible for memory hygiene.
func DeriveVaultEncKey(seed []byte) ([]byte, error)

// DeriveAuditSigningKey derives the secp256k1 ECDSA private key
// used to sign hash-chained audit-log records.
// BIP32 path: m/44'/7743'/2'.
//
// Same seed yields the same key; key differs from the JWT-signing
// key (FR-007).
func DeriveAuditSigningKey(seed []byte) (*ecdsa.PrivateKey, error)

// DeriveClientKey derives the per-machine client signing keypair
// used by an agent host to sign requests to the vault server.
// BIP32 path: m/44'/7743'/3'/{machineIndex}.
//
// Distinct machineIndex values produce distinct keypairs from the
// same seed (User Story 2 / AC-6). Distinct seeds produce distinct
// keypairs even at the same machineIndex (AC-6).
func DeriveClientKey(seed []byte, machineIndex uint32) (*ecdsa.PrivateKey, error)

// PublicKeyFingerprint returns the 16-character lowercase hex
// fingerprint used in operator-facing client-registration UX.
//
// Algorithm: hex.EncodeToString(sha256(SEC1_compressed(pub))[:8]).
//
// Stable across processes, machines, and time. Distinct public
// keys produce distinct fingerprints with overwhelming probability
// (FR-009). Operates on public material only — return type is
// string by design.
func PublicKeyFingerprint(pub *ecdsa.PublicKey) string

// Sentinel errors.
//
// Callers compare via errors.Is. The error messages do NOT echo
// input bytes (Constitution X — Observability & Redaction).
var (
    ErrPassphraseTooShort = errors.New("hush/keys: passphrase too short")
    ErrSaltMissing        = errors.New("hush/keys: salt missing or wrong length")
)
```

## Behavioural guarantees (test-enforced)

| # | Guarantee | Test |
|---|-----------|------|
| G1 | Same `(passphrase, salt)` ⇒ identical 64-byte seed (determinism). | `TestDeriveMasterSeed_Deterministic` |
| G2 | `len(passphrase) < 12` ⇒ `ErrPassphraseTooShort`, no Argon2id call, latency `< 100ms`. | `TestDeriveMasterSeed_RejectsShortPassphrase` |
| G3 | `len(salt) != 16` ⇒ `ErrSaltMissing`, no Argon2id call, latency `< 100ms`. | `TestDeriveMasterSeed_RejectsBadSalt` |
| G4 | Pre-cancelled ctx ⇒ ctx.Err() returned, no Argon2id call. Cancellation after entry does NOT abort derivation. | `TestDeriveMasterSeed_RejectsBadSalt` (ctx subcase) and `TestDeriveMasterSeed_Deterministic` (background-ctx subcase) |
| G5 | `DeriveJWTSigningKey` returns a secp256k1 ECDSA key derivable at path `m/44'/7743'/0'`; key is distinct from the audit key. | `TestDeriveJWTSigningKey_Path` |
| G6 | `DeriveVaultEncKey` returns 32 bytes derived at `m/44'/7743'/1'`. | `TestDeriveJWTSigningKey_Path` (sibling case) |
| G7 | `DeriveAuditSigningKey` returns a secp256k1 ECDSA key at `m/44'/7743'/2'`; distinct from the JWT key. | `TestDeriveJWTSigningKey_Path` (sibling case) |
| G8 | `DeriveClientKey` returns distinct keys for distinct machineIndex values; distinct seeds yield distinct keys at the same machineIndex; full `uint32` range works without truncation. | `TestDeriveClientKey_MachineIndexIsolation` |
| G9 | `PublicKeyFingerprint` returns 16 lowercase hex chars; stable for the same key; distinct keys produce distinct fingerprints. | `TestPublicKeyFingerprint_Stable` |
| G10 | `FuzzDeriveMaster` runs ≥ 60s with no panics, no crashes, deterministic re-derivation per fuzz iteration. | `FuzzDeriveMaster` (in `derive_fuzz_test.go`) |
| G11 | `go test -race ./internal/keys/` clean (no shared mutable state). | All unit tests (race-detector enabled by `magex test:race`). |
| G12 | 100% line coverage. | `go test -cover ./internal/keys/` reports 100.0%. |

## Negative-space contract (test-enforced)

The package MUST NOT:

- import any path under `github.com/mrz1836/hush/internal/...`
  (leaf-package rule). **Verification:** `go list -deps
  ./internal/keys/` lists only stdlib + pinned crypto deps.
- import `math/rand`. **Verification:** `grep math/rand
  internal/keys/*.go` returns nothing.
- convert any secret-bearing `[]byte` to `string` (Constitution X).
  **Verification:** `grep -nE 'string\([^)]*passphrase|string\(seed|string\(salt' internal/keys/*.go` returns nothing.
- have any `init()` function (Constitution IX). **Verification:**
  `grep -nE '^func init\(\)' internal/keys/*.go` returns nothing.
- have any package-level mutable state. **Verification:**
  `gochecknoglobals` lint (already enabled in
  `.golangci.json`).
- log anything (no logger import). **Verification:** `grep -nE
  'log/slog|logging' internal/keys/*.go` returns nothing.

## Constants exposed (unexported, but documented for review)

These are not part of the public API but are pinned by the chunk
contract and Constitution III; they MUST be encoded as named
package constants (not magic numbers) so a reviewer can grep them
in one place.

```go
// Argon2id parameters — non-negotiable per Constitution III + Security Requirements.
const (
    argon2Time    = 4
    argon2MemoryK = 256 * 1024 // KiB → 256 MiB
    argon2Threads = 4
    argon2KeyLen  = 64
)

// BIP32 paths — non-negotiable per SPEC FR-3.
const (
    bip44Purpose       = 44 // hardened in path derivation
    hushCoinType       = 7743
    pathJWTSigning     = "m/44'/7743'/0'"
    pathVaultEnc       = "m/44'/7743'/1'"
    pathAuditSigning   = "m/44'/7743'/2'"
    pathClientKeyBase  = "m/44'/7743'/3'"
)

// Validation thresholds.
const (
    minPassphraseLen = 12
    saltLen          = 16
)
```

The exact representation (string paths vs. `[]uint32` index slices)
is an implementation detail; what matters is that the values are
named, grep-able, and documented in this contract.
