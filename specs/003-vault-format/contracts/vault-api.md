# Contract — `internal/vault` exported API (SDD-03)

This file is the locked Go API for `internal/vault`. It will be
copied verbatim into `docs/PACKAGE-MAP.md` under "## `internal/vault/`
→ ### Exported API — locked at SDD-03 (`internal/vault` package)"
once SDD-03 enters the implement phase. **No symbol may be added,
removed, or renamed without an SDD amendment.**

Path: `github.com/mrz1836/hush/internal/vault`

```go
// Package vault owns the on-disk vault file (a binary HUSH-format
// envelope encrypted with AES-256-GCM, written atomically with file
// mode 0600 under a 0700 parent) and the in-memory Store from which
// callers retrieve secret values as fresh, independently-owned
// *securebytes.SecureBytes containers (Layer 5 — see SDD-02 and
// docs/SECURITY.md §3.5).
//
// The package is the persistent-custody half of AC-2 (vault
// round-trip; the SIGHUP-reload half is SDD-10's responsibility).
//
// Constitutional principles in scope: III (Encryption at Rest +
// Layer 5 secure memory), VIII (100% coverage; mandatory fuzz target
// #1), X (no secret values in errors or logs), XI (stdlib-only crypto;
// no new dependencies).
package vault

import (
    "context"
    "errors"

    "github.com/mrz1836/hush/internal/vault/securebytes"
)

// Secret is one named, described, value-bearing entry in the vault.
//
// The Value pointer MUST be non-nil and live (not destroyed) at the
// moment Save is called. The package does not retain a reference to
// the caller's *SecureBytes after Save returns; the caller continues
// to own the lifetime of the Value containers it passed in.
type Secret struct {
    Name        string
    Description string
    Value       *securebytes.SecureBytes
}

// Store is the in-memory view of a loaded vault. Implementations are
// safe for concurrent Get and Names from many goroutines. Get returns
// a fresh, independently-owned *SecureBytes per call; the caller owns
// the returned container's lifecycle. Destroy is idempotent.
type Store interface {
    // Get returns a fresh, independently-owned *SecureBytes wrapping
    // a copy of the named secret's value. Destroying the returned
    // container does not affect any other consumer's view of the same
    // secret, nor any subsequent Get against the same name.
    //
    // Get returns ErrSecretNotFound if name is not in the store, and
    // ErrStoreDestroyed if Destroy has previously been called.
    Get(name string) (*securebytes.SecureBytes, error)

    // Names returns the list of secret names held by the store, in the
    // same stable order they appeared in the loaded file. The returned
    // slice is a defensive copy; callers may mutate it freely.
    Names() []string

    // Destroy zeroes every internally-held *SecureBytes and marks the
    // store as destroyed. After Destroy returns, all subsequent Get
    // calls return ErrStoreDestroyed. Destroy is idempotent.
    Destroy() error
}

// Load reads, validates, and decrypts the vault file at path using
// vaultKey, returning a Store from which secrets can be retrieved.
//
// ctx is inspected once at entry; pre-cancellation returns ctx.Err()
// immediately. The file is read entirely into memory before
// decryption (no streaming).
//
// Load enforces, in order:
//
//   - File must exist and be a regular file at path.
//   - The file's mode must be exactly 0600; the parent directory's mode
//     must be exactly 0700. A laxer (or stricter) mode returns
//     ErrFilePermsLoose.
//   - The file's size must be at most 64 MiB; larger returns
//     ErrFileTooLarge before any read or allocation.
//   - The first 4 bytes must equal {0x48, 0x55, 0x53, 0x48}; otherwise
//     ErrBadMagic.
//   - Byte 5 must equal 0x01 (the v0.1.0 format version); otherwise
//     ErrBadVersion.
//   - The file's total length must be at least 4 + 1 + 16 + 12 + 16 =
//     49 bytes (header + AES-GCM minimum tag); otherwise
//     ErrShortHeader.
//   - AES-256-GCM authenticated decryption with vaultKey must succeed;
//     any failure (wrong key, tampering, truncation below the tag)
//     returns ErrAuthFailed.
//
// Load never returns a partially-constructed Store; on any error,
// the returned Store is nil. The vaultKey container is borrowed via
// its Use callback only for the duration of the AES-GCM Open call;
// the package retains no reference to it after Load returns.
//
// No reachable error path returns a free-form error string that
// would force the caller to parse text — every failure mode is one
// of the typed sentinel errors below or wraps one with %w.
func Load(ctx context.Context, path string, vaultKey *securebytes.SecureBytes) (Store, error)

// Save encrypts secrets to the vault file at path using vaultKey,
// committing the result atomically.
//
// ctx is inspected once at entry; pre-cancellation returns ctx.Err()
// immediately. Save MUST be called single-writer per path; concurrent
// Save calls against the same path are an operator error and are not
// guaranteed to produce a consistent result. (The PID-file / flock
// discipline that prevents this is upstream's responsibility.)
//
// Save enforces, in order:
//
//   - The input list must contain no duplicate names; a duplicate
//     returns ErrDuplicateName before any encryption or filesystem
//     write.
//   - Every entry's Name must be non-empty, at most 256 bytes,
//     printable ASCII (0x20-0x7E inclusive), with no NUL or control
//     characters; every entry's Description must be at most 4096
//     bytes with no NUL or control characters (0x00-0x1F, 0x7F). A
//     violation returns ErrInvalidName before any encryption or
//     filesystem write.
//   - The parent directory's mode must be exactly 0700; otherwise
//     ErrFilePermsLoose, with no filesystem write.
//
// Save then commits atomically:
//
//   - Marshal the JSON plaintext into an in-memory buffer.
//   - Generate a fresh 16-byte salt and a fresh 12-byte AES-GCM nonce
//     via crypto/rand.
//   - AES-256-GCM seal the JSON plaintext.
//   - Write magic + version + salt + nonce + ciphertext+tag to
//     <path>.tmp in the same directory, fsync, close.
//   - Set <path>.tmp's mode to 0600 (neutralising umask).
//   - os.Rename(<path>.tmp, <path>).
//   - Set <path>'s mode to 0600 (belt-and-braces).
//
// On any controlled mid-flight error after the working file is
// created, Save attempts a best-effort os.Remove(<path>.tmp) before
// returning. The remove error is logged at debug level but is not
// surfaced to the caller and does not mask the original error. The
// SIGKILL case (process death between working-file creation and
// rename) leaves <path>.tmp on disk for upstream cleanup; the file
// at <path> is unchanged in either case (FR-013).
//
// On success, every secret in secrets has been encrypted to disk.
// The caller continues to own the lifetime of the Value containers;
// Save does not Destroy them.
func Save(ctx context.Context, path string, vaultKey *securebytes.SecureBytes, secrets []Secret) error

// Sentinel errors. Compare with errors.Is. Every error returned by
// Load, Save, or any Store method is one of these or wraps one with
// %w. The rendered text of any error returned by this package never
// contains any byte of any secret value (FR-030).
var (
    // ErrBadMagic is returned by Load when the file's first 4 bytes
    // do not equal {0x48, 0x55, 0x53, 0x48} ("HUSH" in ASCII).
    ErrBadMagic = errors.New("hush/vault: bad magic")

    // ErrBadVersion is returned by Load when the version byte (byte
    // index 4) does not equal 0x01.
    ErrBadVersion = errors.New("hush/vault: bad version")

    // ErrShortHeader is returned by Load when the file is shorter
    // than the minimum bytes required to contain the header, salt,
    // nonce, and AES-GCM authentication tag (49 bytes total).
    ErrShortHeader = errors.New("hush/vault: short header")

    // ErrAuthFailed is returned by Load when AES-256-GCM
    // authenticated decryption fails — caused by a wrong key, by
    // tampering with the ciphertext, or by truncation below the
    // authentication tag's minimum size. The error's rendered text
    // contains no byte of any secret value.
    ErrAuthFailed = errors.New("hush/vault: authentication failed")

    // ErrFilePermsLoose is returned by Save or Load when the file
    // mode is not exactly 0600 or the parent directory mode is not
    // exactly 0700.
    ErrFilePermsLoose = errors.New("hush/vault: file permissions loose")

    // ErrSecretNotFound is returned by Store.Get when name is not in
    // the store.
    ErrSecretNotFound = errors.New("hush/vault: secret not found")

    // ErrStoreDestroyed is returned by Store.Get after Store.Destroy
    // has previously been called against the same store. It is
    // programmatically distinguishable from ErrSecretNotFound.
    ErrStoreDestroyed = errors.New("hush/vault: store destroyed")

    // ErrDuplicateName is returned by Save when its input list
    // contains two or more entries that share a Name. The error is
    // returned before any encryption or filesystem write.
    ErrDuplicateName = errors.New("hush/vault: duplicate secret name")

    // ErrFileTooLarge is returned by Load when the file at path
    // exceeds 64 MiB. The error is returned at os.Stat time, before
    // any read or allocation of the file's contents.
    ErrFileTooLarge = errors.New("hush/vault: file too large")

    // ErrInvalidName is returned by Save when an entry's Name or
    // Description violates the FR-008 constraints (Name: non-empty,
    // ≤256 bytes, printable ASCII; Description: ≤4096 bytes, no
    // NUL/control characters). The error is returned before any
    // encryption or filesystem write.
    ErrInvalidName = errors.New("hush/vault: invalid secret name or description")
)
```

---

## Behavioural guarantees (test-anchored)

Every guarantee below maps to one or more enumerated test names.
The unit-test names are taken verbatim from the SDD-03 chunk
contract; additions specific to spec clarifications are flagged.

| Guarantee | Test name |
|-----------|-----------|
| Round-trip exactness for an empty list | `TestVault_RoundTrip_0Secrets` |
| Round-trip exactness for a single secret | `TestVault_RoundTrip_1Secret` |
| Round-trip exactness for a small list | `TestVault_RoundTrip_5Secrets` |
| Round-trip exactness for the in-scope worst case | `TestVault_RoundTrip_500Secrets` |
| Wrong-key rejection produces `ErrAuthFailed` | `TestVault_LoadWrongPass_ReturnsAuthFailed` |
| Truncation at the magic boundary classifies as `ErrShortHeader` (or `ErrBadMagic` for non-empty short prefixes) | `TestVault_LoadTruncatedAtMagic_ShortHeader` |
| Truncation inside the salt classifies as `ErrShortHeader` | `TestVault_LoadTruncatedAtSalt_ShortHeader` |
| Truncation inside the nonce classifies as `ErrShortHeader` | `TestVault_LoadTruncatedAtNonce_ShortHeader` |
| Truncation inside the ciphertext (above the tag minimum) classifies as `ErrAuthFailed` | `TestVault_LoadTruncatedCiphertext_AuthFailed` |
| Loose file mode classifies as `ErrFilePermsLoose` | `TestVault_LoadLooseFileMode_PermsLoose` |
| Loose parent directory mode classifies as `ErrFilePermsLoose` | `TestVault_LoadLooseParentMode_PermsLoose` |
| Save commits atomically — no observable intermediate file at the target path | `TestVault_SaveAtomic_NoIntermediate` |
| Save sets file mode to `0600` regardless of umask | `TestVault_SaveSetsMode0600` |
| Sentinel-leak: wrong-key load does not leak `SECRET_SHOULD_NEVER_APPEAR_3` into err.Error() or any captured slog line | `TestVault_NoLeakInError` |
| Concurrent `Store.Get` from 100 goroutines is race-clean | `TestStore_ConcurrentGet` |
| `FuzzVaultDecode` runs ≥60 s with no panic, ≤50 MiB allocation per call, and every error returned is one of the ten typed sentinels | `FuzzVaultDecode` |

Additional coverage required by spec clarifications (Q1–Q5) and
covered by tests added beside the chunk-contract list:

| Guarantee | Anchor |
|-----------|--------|
| Post-`Destroy` `Get` returns `ErrStoreDestroyed` (not `ErrSecretNotFound`) | `TestStore_GetAfterDestroy_ReturnsErrStoreDestroyed` |
| `Save` rejects a duplicate name without writing the filesystem | `TestVault_Save_DuplicateName_NoFilesystemTouch` |
| `Save` rejects an invalid Name or Description without writing the filesystem | `TestVault_Save_InvalidName_NoFilesystemTouch` |
| `Load` rejects a >64 MiB file at stat time, before any read | `TestVault_Load_OversizedFile_ReturnsErrFileTooLarge` |
| `Save` cleans up its working file on a controlled mid-flight error | `TestVault_Save_MidFlightFailure_RemovesTmp` |

---

## Integration boundaries (informative)

This package's exported surface is consumed by:

- **SDD-10 (`internal/server`)** — server startup and SIGHUP reload.
  Holds a `Store` in `atomic.Pointer[Store]`; on SIGHUP, calls
  `Load` to construct a new `Store`, atomically swaps the pointer,
  and calls `Destroy` on the old `Store` after a drain of in-flight
  requests.
- **SDD-13 (`internal/server` secret-fetch handler)** — calls
  `store.Get(name)` per inbound request; consumes the returned
  `*SecureBytes` via `Use(fn)` to ECIES-encrypt the payload, then
  `Destroy`s the per-request container.
- **SDD-17 (`internal/cli/secret.go`)** — the `hush secret` rotation
  CLI calls `Load` to enumerate names (`Names()`), then calls
  `Save` with the new list to produce the rotated vault.
- **SDD-25 (lifecycle harness)** — exercises round-trip,
  atomic-write, and SIGHUP-reload (the latter via SDD-10).

This package's exported surface is NOT consumed by:

- **`cmd/hush`** — only `internal/cli` orchestrates `internal/vault`.
- **`internal/discord`** — Constitution-mapped boundary
  (`docs/PACKAGE-MAP.md` "`internal/vault` should not import
  `internal/discord`"; the converse — discord importing vault — is
  also out of scope).
