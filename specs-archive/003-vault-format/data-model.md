# Data Model — `internal/vault` (SDD-03)

This document enumerates the data structures, on-disk layout, and
state transitions for the `internal/vault` package. It is the
single source of truth for the on-wire envelope, the in-memory
plaintext shape, and the in-memory `Store` lifecycle. The exported
Go API itself is locked separately in `contracts/vault-api.md`.

---

## 1. On-disk envelope (the HUSH file)

The vault file is a single, fixed-layout binary blob. There is no
TLV framing, no length prefix, no padding, no trailer. The exact
file length is `4 + 1 + 16 + 12 + ciphertextLen` bytes, where
`ciphertextLen ≥ cipher.Overhead()` (16 bytes for AES-GCM).

### Field layout

| Offset | Length | Field | Allowed values | Validation rule |
|-------:|-------:|-------|----------------|------------------|
| `0` | 4 | `magic` | exactly `0x48 0x55 0x53 0x48` ("HUSH" in ASCII) | `bytes.Equal(buf[0:4], magic)` else `ErrBadMagic` |
| `4` | 1 | `version` | exactly `0x01` for the v0.1.0 format | `buf[4] == version` else `ErrBadVersion` |
| `5` | 16 | `salt` | any 16 bytes (carried, not interpreted, by this package) | filled by `crypto/rand.Read` on `Save`; copied verbatim into the in-memory carry-back on `Load` |
| `21` | 12 | `nonce` | any 12 bytes; AES-GCM standard nonce length | filled by `crypto/rand.Read` on `Save`; reused as the AEAD nonce on `Load` |
| `33` | `n ≥ 16` | `ciphertext-plus-tag` | output of `cipher.AEAD.Seal` over the JSON plaintext | `n` must be at least `cipher.Overhead()` (i.e. 16) — else `ErrShortHeader` |

### Constants (named in `file.go`)

```go
var magic = []byte{0x48, 0x55, 0x53, 0x48}

const (
    version    byte = 0x01
    saltLen         = 16
    nonceLen        = 12
    headerLen       = 4 + 1 + saltLen + nonceLen // = 33
    maxFileLen      = 64 * 1024 * 1024          // 64 MiB; FR-019a
)
```

### Length-class invariants (informative)

- `len(file) < 4` → `ErrShortHeader` (cannot read magic).
- `len(file) >= 4 && len(file) < 5` → `ErrShortHeader` (cannot
  read version).
- `len(file) >= 5 && bytes[0:4] != magic` → `ErrBadMagic`.
- `len(file) >= 5 && bytes[4] != version` → `ErrBadVersion`.
- `len(file) < headerLen + cipher.Overhead()` (= 49) →
  `ErrShortHeader`.
- `len(file) > maxFileLen` (= 67,108,864) → `ErrFileTooLarge`
  (caught at `os.Stat`, before any read).

---

## 2. Plaintext payload (inside the AEAD envelope)

The plaintext is a JSON-encoded array of objects:

```text
[
  {
    "name":        string,   // operator-readable identifier
    "description": string,   // operator-readable context
    "value":       string    // base64-encoded secret bytes
  },
  ...
]
```

### Wire types (package-private, defined in `codec.go`)

```go
// wireSecret is the on-the-wire shape of a single secret entry.
// Only used inside the AEAD envelope; never crosses the package
// boundary.
type wireSecret struct {
    Name        string    `json:"name"`
    Description string    `json:"description"`
    Value       wireValue `json:"value"`
}

// wireValue holds the secret payload via a SecureBytes pointer.
// Custom (Un)MarshalJSON bypasses any Go-string allocation of the
// raw secret value.
type wireValue struct {
    sb *securebytes.SecureBytes
}
```

### Custom JSON behaviour for `wireValue`

| Direction | Source token | Action | Failure mode |
|-----------|--------------|--------|--------------|
| `MarshalJSON` | `sb` (live) | `Use(fn)` borrows the bytes; `base64.StdEncoding.EncodeToString` runs over the borrow; output is a JSON-quoted base64 string | `ErrDestroyed` (from `Use`) wrapped as a JSON marshal error — should not occur on the `Save` path because the caller controls lifetime |
| `UnmarshalJSON` | JSON-quoted base64 string | (1) verify token is `"…"` (else fail); (2) `base64.StdEncoding.DecodeString` into a fresh `[]byte`; (3) `securebytes.New(buf)` (which copies + mlocks + zeroes the input slice); (4) store the resulting `*SecureBytes` pointer in `sb` | base64 decode failure → typed JSON error (caller of `Load` sees this wrapped in the JSON-decode top-level error path; classified as a load-failure though it is technically a payload-failure) |

### Field-level invariants

| Field | Constraint | Enforced where | On violation |
|-------|------------|----------------|--------------|
| `name` | non-empty, ≤256 bytes, printable ASCII (0x20–0x7E), no NUL/control | `Save` pre-pass over `[]Secret` | `ErrInvalidName` |
| `name` | unique within the array | `Save` pre-pass | `ErrDuplicateName` |
| `description` | ≤4096 bytes, no `0x00`–`0x1F`, no `0x7F` | `Save` pre-pass | `ErrInvalidName` |
| `value` | any byte sequence (including empty) | n/a (no constraint) | n/a |

The `Load` path *does not* re-validate names and descriptions — the
file was produced by a prior `Save`, so the constraints have already
been enforced. (Validating again would force the package to refuse
to load a vault produced by a future `Save` that loosened the rules,
which is the wrong direction of compatibility.)

---

## 3. Exported types

### `Secret`

```go
type Secret struct {
    Name        string
    Description string
    Value       *securebytes.SecureBytes
}
```

- Used by both `Save` (as input) and conceptually by `Load` (the
  package decodes into this shape internally and then publishes
  the values via `Store.Get`).
- `Value` MUST be non-nil and live (not destroyed) at the moment
  `Save` is called; a nil or destroyed `Value` is a programmer
  error and produces a wrapped `ErrDestroyed` from the inner
  `MarshalJSON`.

### `Store`

```go
type Store interface {
    Get(name string) (*securebytes.SecureBytes, error)
    Names() []string
    Destroy() error
}
```

- Returned by `Load`.
- The concrete type is `*memStore` (unexported); `Load` returns
  the interface so consumers can substitute test doubles and so
  SDD-10's SIGHUP swap can hold the interface in
  `atomic.Pointer[Store]`.

---

## 4. `memStore` internal layout

```go
type memStore struct {
    mu        sync.RWMutex
    names     []string                              // ordered, immutable after Load
    byName    map[string]*securebytes.SecureBytes   // immutable after Load (entries' lifetimes managed by Destroy)
    destroyed bool
}
```

### Operation table

| Method | Lock | Reads | Writes | Failure modes |
|--------|------|-------|--------|---------------|
| `Get(name)` | `RLock` | `destroyed`, `byName[name]` (then enters `Use` on the inner container) | none on the store; constructs a NEW `*SecureBytes` from a copy of the inner payload | `ErrStoreDestroyed` if `destroyed`; `ErrSecretNotFound` if `name` absent; `ErrStoreDestroyed` if the inner `Use` returns `ErrDestroyed` (race-with-destroy) |
| `Names()` | `RLock` | `names` | none; returns `append([]string(nil), names...)` | (none — returns empty slice on a destroyed store; `Get` is the gate, `Names` is the cheap-list operation) |
| `Destroy()` | `Lock` | `destroyed` | sets `destroyed = true`; iterates `byName` calling each container's `Destroy()` | (none — idempotent; per-container destroy errors are aggregated and returned via `errors.Join`, but in practice `*SecureBytes.Destroy` returns `nil` on the supported platforms) |

### Lifecycle states

```text
                +------------------+
                |     LIVE         |
                |  Get → SecureBytes
                |  Names → []string
                +---------+--------+
                          |
                       Destroy()
                          |
                          v
                +------------------+
                |    DESTROYED     |
                |  Get → ErrStoreDestroyed
                |  Names → []string{} (or stale; defensive copy)
                |  Destroy → no-op (idempotent)
                +------------------+
```

Transitions:
- LIVE → DESTROYED: `Destroy()` (single transition; idempotent).
- DESTROYED → LIVE: not permitted. To get a live store again,
  the caller must `Load` afresh.

---

## 5. Sentinel errors

All ten sentinels are exported package-level `var Err... =
errors.New(...)`. Comparable via `errors.Is`.

| Sentinel | Trigger | Source FR |
|----------|---------|-----------|
| `ErrBadMagic` | `Load`: first 4 bytes ≠ `0x48 0x55 0x53 0x48` | FR-001, FR-020 |
| `ErrBadVersion` | `Load`: byte 5 ≠ `0x01` | FR-002, FR-020 |
| `ErrShortHeader` | `Load`: file shorter than `headerLen + cipher.Overhead()` | FR-020, FR-006 |
| `ErrAuthFailed` | `Load`: AES-GCM `Open` returns any error (wrong key, tampered ciphertext, truncated below tag) | FR-021 |
| `ErrFilePermsLoose` | `Save` or `Load`: file mode ≠ `0600` or parent mode ≠ `0700` | FR-014, FR-015, FR-018, FR-019 |
| `ErrSecretNotFound` | `Store.Get`: `name` not in `byName` | FR-024 |
| `ErrStoreDestroyed` | `Store.Get`: `destroyed == true`, OR inner `Use` returns `ErrDestroyed` | FR-027 (clarification Q1) |
| `ErrDuplicateName` | `Save` pre-pass: same `name` appears more than once | FR-033 (clarification Q2) |
| `ErrFileTooLarge` | `Load`: `os.Stat(path).Size() > 64 MiB` | FR-019a (clarification Q3) |
| `ErrInvalidName` | `Save` pre-pass: name or description violates FR-008 | FR-034 (clarification Q5) |

### Wrapping policy

- All sentinels are wrapped with `fmt.Errorf("vault: ...: %w",
  ErrFoo)` so `errors.Is(err, ErrFoo)` works while the rendered
  text identifies the package and (where applicable) the file
  path or secret name.
- The rendered text MUST NEVER include any byte of any secret
  value (FR-030). The package's slog usage uses the
  `*SecureBytes`'s built-in `LogValue() = "[redacted]"` for any
  field that could conceivably hold secret material.

---

## 6. Concurrency model

| Invariant | Mechanism |
|-----------|-----------|
| Concurrent `Get` calls do not race | `sync.RWMutex.RLock`; `byName` is read-only after `Load`; per-container `Use` holds its own internal mutex (SDD-02) |
| `Destroy` is safe against concurrent `Get` | `Lock` serialises destroy; an in-flight `Get` either completes its `Use(fn)` callback fully (last-retrieval-wins) or observes the inner `ErrDestroyed` and surfaces `ErrStoreDestroyed` |
| `Destroy` is idempotent | `destroyed` flag check at the top of `Destroy` |
| `Load` produces a fully-constructed store before publication | The `*memStore` pointer is only returned from `Load` after the JSON unmarshal completes; consumers cannot observe a partial map |
| `Save` does not mutate any shared state | Pure function over `(path, key, []Secret)`; no package-level state is touched |

---

## 7. Memory bounds

| Operation | Worst-case allocation | Source |
|-----------|----------------------:|--------|
| `Save([]Secret of N entries, total plaintext P bytes)` | `~P` for the JSON marshal buffer + `~P + cipher.Overhead()` for the ciphertext buffer + `headerLen` for the header | sum: `~2P + 33 + 16` |
| `Load(file of size F)` | `F` for the read buffer (≤64 MiB by FR-019a) + `F - headerLen - cipher.Overhead()` for the plaintext + `~F` for the JSON-decoded `[]wireSecret` slice | sum: `~3F` ceiling |
| `Store.Get(name → V bytes)` | `V` for the borrow-copy buffer + `V` for the new mlocked `SecureBytes` buffer | sum: `~2V` per call |

The fuzz harness (Decision 14) asserts a 50 MiB allocation ceiling
during `Load` per call, which implies a maximum fuzz input of
~16 MiB — well below the 64 MiB production cap.

---

## 8. Cross-references

- **`docs/SPEC.md` FR-2**: on-disk envelope (magic / version / salt
  / nonce / ciphertext); AES-256-GCM + Argon2id parameters.
- **`docs/SPEC.md` FR-10**: atomic vault writes + SIGHUP reload
  (this package owns the atomic-write half; SDD-10 owns the
  SIGHUP-reload half).
- **`docs/SPEC.md` FR-15**: file permissions enforced at startup
  (this package owns the per-file enforcement).
- **`docs/SPEC.md` AC-2**: vault round-trip exit gate.
- **`docs/SECURITY.md` §3.1**: Layer 1 (BIP32-derived 32-byte AES
  key — supplied by SDD-01, consumed here).
- **`docs/SECURITY.md` §3.5**: Layer 5 (mlocked secure memory —
  supplied by SDD-02, consumed here).
- **`docs/PACKAGE-MAP.md` `internal/vault/`**: the file list and
  responsibilities. `reload.go` named there is owned by SDD-10
  (server-side SIGHUP wiring), not SDD-03.
- **`docs/sdd/SDD-03.md`**: chunk contract (the locked HOW).
