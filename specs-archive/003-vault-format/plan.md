# Implementation Plan: HUSH Vault File Format + In-Memory Store (SDD-03)

**Branch**: `003-vault-format` | **Date**: 2026-04-27 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/003-vault-format/spec.md`
**Chunk contract**: [docs/sdd/SDD-03.md](../../docs/sdd/SDD-03.md)

## Summary

`internal/vault` owns the on-disk vault file: a binary `HUSH`-format
envelope (4-byte magic + 1-byte version + 16-byte salt + 12-byte
AES-GCM nonce + AES-256-GCM ciphertext-plus-tag) wrapping a JSON
payload of named, described secret values, written atomically with
`0600` mode under a `0700` parent, and decoded into a concurrent-safe
in-memory `Store` that hands callers fresh, independently-owned
`*securebytes.SecureBytes` containers (Layer 5 — SDD-02). The package
is the persistent-custody half of AC-2 (the SIGHUP-reload half
remains SDD-10's responsibility) and serves SDD-10 (server reload),
SDD-13 (secret-fetch handler), SDD-17 (`hush secret` rotation CLI),
and SDD-25 (lifecycle harness).

Approach (locked by SDD-03 + Constitution III/VIII/X/XI; not subject
to research alternatives):

- **Header layout** (`file.go`): four named constants — `magic =
  []byte{0x48,0x55,0x53,0x48}`, `version byte = 0x01`, `saltLen = 16`,
  `nonceLen = 12`. Header is parsed and written with explicit
  `binary`-free byte-slice arithmetic; no `encoding/binary`
  multi-byte fields exist beyond the literal salt and nonce slices.
- **AES-256-GCM** (`codec.go`): `crypto/aes.NewCipher` →
  `crypto/cipher.NewGCM`. The 32-byte key is borrowed from the
  caller's `*securebytes.SecureBytes` via `Use(fn)` for the duration
  of `Seal` / `Open`; the unwrapped key is never assigned to a
  package-level variable, never returned, and never logged.
- **Randomness**: `crypto/rand.Read` for the 16-byte salt and the
  12-byte nonce on every `Save`. `math/rand` is not imported by this
  package (Constitution III non-negotiable).
- **Plaintext payload**: JSON array of wire-shape entries
  `{"name":string,"description":string,"value":string-base64}`. The
  `value` field is a wrapper type with a custom `UnmarshalJSON` that
  base64-decodes the JSON string token directly into a freshly
  allocated `[]byte`, constructs a `*securebytes.SecureBytes` (which
  copies, mlocks, and zeroes the input slice), and stores the
  pointer. The wrapper's `MarshalJSON` base64-encodes the borrowed
  payload from inside `SecureBytes.Use` into the JSON output. The
  raw secret value never inhabits a Go `string` on either path.
- **Save flow** (`file.go`):
  1. Pre-pass: scan `[]Secret` for duplicate names → `ErrDuplicateName`,
     and for `name`/`description` violating FR-008 → `ErrInvalidName`.
     No filesystem touch on failure.
  2. `os.Stat(parent)` → refuse if mode != `0700` → `ErrFilePermsLoose`.
  3. Marshal the wire-shape JSON into an in-memory buffer using
     `encoding/json`.
  4. Generate fresh 16-byte salt + 12-byte nonce via `crypto/rand`.
  5. AES-256-GCM seal the JSON payload.
  6. Open `<path>.tmp` in the same directory with
     `O_WRONLY|O_CREATE|O_TRUNC`, mode `0600`, write `magic + version
     + salt + nonce + ciphertext-plus-tag`, `f.Sync()`, `f.Close()`.
  7. `os.Chmod(<path>.tmp, 0600)` to neutralise umask.
  8. `os.Rename(<path>.tmp, <path>)`.
  9. `os.Chmod(<path>, 0600)` for belt-and-braces.
  10. On any error after step 6, best-effort `os.Remove(<path>.tmp)`;
      remove failure is logged at debug level and does not mask the
      original error (FR-013).
- **Load flow** (`file.go`):
  1. `os.Stat(path)` → refuse if mode != `0600` → `ErrFilePermsLoose`,
     refuse if `Size() > 64 MiB` → `ErrFileTooLarge`.
  2. `os.Stat(parent)` → refuse if mode != `0700` → `ErrFilePermsLoose`.
  3. `os.OpenFile(path, O_RDONLY, 0)` → `io.ReadAll` (capped by the
     prior size check, so the call cannot exceed 64 MiB).
  4. Parse: bytes `[0..4)` must equal `magic` → `ErrBadMagic`; byte
     `[4]` must equal `version` → `ErrBadVersion`; total length must
     be ≥ `4 + 1 + 16 + 12 + cipher.Overhead()` → `ErrShortHeader`.
  5. AES-256-GCM open over `ciphertext-plus-tag` using the caller's
     key → `ErrAuthFailed` on any failure (`crypto/cipher` returns
     a single sealed-failure error; we wrap once with `%w`).
  6. `encoding/json.Unmarshal` into `[]wireSecret` (the value
     wrapper is the only path through which the plaintext value
     materialises, and only as `[]byte` → `*SecureBytes`).
  7. Construct `Store` with the ordered list of names and a
     `map[string]*securebytes.SecureBytes` over the values.
- **Permissions checks** (`permissions.go`): two helpers,
  `checkFileMode(path, want fs.FileMode)` and `checkParentMode(path,
  want fs.FileMode)`, both wrapping `os.Stat` and asserting
  `info.Mode().Perm() == want` (exact equality — laxer **and**
  stricter both fail; the spec mandates the *laxer* case but exact
  equality matches the chunk contract's "refuse if mode `!= 0600`"
  wording). Both return `ErrFilePermsLoose` on any mismatch.
- **Store** (`store.go`):
  - Concrete type `memStore` implementing the exported `Store`
    interface. Fields: `names []string` (ordered, immutable after
    `Load`), `byName map[string]*securebytes.SecureBytes`,
    `mu sync.RWMutex`, `destroyed bool`.
  - `Get(name)` takes `mu.RLock`, refuses with `ErrStoreDestroyed`
    if `destroyed`, looks up `name`, copies the payload via the
    inner container's `Use(fn)` into a freshly allocated `[]byte`,
    then constructs a NEW `*securebytes.SecureBytes` via
    `securebytes.New(buf)` (which itself copies, mlocks, zeroes the
    transient slice). The returned container is owned by the caller;
    destruction has no effect on the store's internal copy
    (FR-023, SC-011).
  - `Names()` takes `mu.RLock`, returns a defensive copy of the
    `names` slice — the slice is never mutated after `Load`, but a
    copy prevents an external caller's `slices.Sort` from corrupting
    the canonical order (FR-025).
  - `Destroy()` takes `mu.Lock`, walks `byName` calling each
    container's `Destroy()`, sets `destroyed = true`. Idempotent
    (FR-027).
- **Sentinel errors** are colocated at the top of `file.go` (or
  inlined where the chunk contract permits — see the file layout
  below). The four spec-clarification additions
  (`ErrStoreDestroyed`, `ErrDuplicateName`, `ErrFileTooLarge`,
  `ErrInvalidName`) are added on top of the SDD-03 chunk contract's
  six baseline sentinels; this is a *strict superset*, satisfies
  every FR-028 named failure mode, and is the locked exported
  surface for the `internal/vault` row of `docs/PACKAGE-MAP.md`.
- **Context discipline**: `Load` and `Save` accept `ctx
  context.Context` as the first parameter (Constitution IX) and
  inspect `ctx.Err()` once on entry; the body itself does not
  cooperatively cancel mid-`Seal`/`Open` since AES-GCM sealing on
  ≤64 MiB completes well inside any reasonable budget. This matches
  the SDD-01 / SDD-02 precedent.
- **No new dependencies.** All cryptographic primitives are stdlib
  (`crypto/aes`, `crypto/cipher`, `crypto/rand`). The only non-test
  intra-repo import is
  `github.com/mrz1836/hush/internal/vault/securebytes` — the same
  module's sub-package, locked at SDD-02.

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`); `CGO_ENABLED=0`
(Constitution IX).
**Primary Dependencies**:
- Go stdlib: `context`, `crypto/aes`, `crypto/cipher`, `crypto/rand`,
  `encoding/base64`, `encoding/json`, `errors`, `fmt`, `io`, `io/fs`,
  `log/slog`, `os`, `path/filepath`, `sync`.
- Intra-repo: `github.com/mrz1836/hush/internal/vault/securebytes`
  (SDD-02; locked).
- Test-only: `testing`, `testing/quick`, `bytes`, plus an in-test
  slog handler for the sentinel-leak assertion.
- **No new direct dependency is added.** Constitution XI satisfied
  trivially; no crypto-dep amendment is required.

**Storage**: A single file at the documented path on the trusted
host (`~/.hush/secrets.vault` per `docs/SPEC.md` FR-2; the absolute
path is supplied by the caller and not interpreted by this package).
The file is `0600`, parent is `0700`. No database, no network, no
process-IPC.

**Testing**: Go stdlib `testing`. Table-driven unit tests, race
detector (`magex test:race`), `go test -fuzz=FuzzVaultDecode
-fuzztime=60s` for fuzz target #1, and a sentinel-leak test that
asserts `SECRET_SHOULD_NEVER_APPEAR_3` does not appear in
`err.Error()` or in any captured `slog` line on the wrong-key path.

**Target Platform**: macOS (darwin amd64/arm64) and Linux (amd64/
arm64), per `.goreleaser.yml` and Spec Assumption "Supported
platforms". Windows is out of scope.

**Project Type**: Single Go module (`github.com/mrz1836/hush`) with a
flat `internal/<domain>` layout per `docs/PACKAGE-MAP.md`.
`internal/vault` is the parent package being filled here; the
existing `internal/vault/securebytes` sub-package (SDD-02) remains
untouched.

**Performance Goals**:
- `Save` round trip on a 500-secret payload (~2 MiB plaintext)
  completes well under 1 s — dominated by `f.Sync()`, not by
  AES-GCM. Save is operator-driven (rotation CLI), not on a hot path.
- `Load` round trip on the same payload completes well under 100 ms
  — dominated by reading and JSON-decoding ~2 MiB.
- `Store.Get` is `RLock` → map lookup → one `[]byte` copy → one
  `securebytes.New` (mlock + zero of the transient slice). Acceptable
  for the project's expected request rate (Constitution VI: vault
  server is Tailscale-only, ≤100 concurrent secret fetches per
  NFR-6). No allocation pool, no caching layer.
- Memory ceiling per `Load` call: bounded above by the 64 MiB file
  size cap (FR-019a) plus a transient JSON-decoded copy (≤64 MiB) —
  total worst-case ~130 MiB, well below the 50 MiB-per-call fuzz
  ceiling that applies to the *fuzz harness* specifically (which
  generates much smaller inputs by construction).

**Constraints**:
- 100% test coverage required on `internal/vault/...` (Constitution
  VIII; codecov gate for the four security-critical packages —
  `vault`, `keys`, `token`, `transport`).
- No `[]byte → string` conversion of secret material anywhere in
  this package (Constitution X anti-contract; Spec FR-009, FR-022).
- Custom JSON `UnmarshalJSON` on the value-wrapper type is the ONLY
  decode path; the standard library's default reflection-based
  string-then-decode behaviour is bypassed by construction.
- No `init()`, no mutable package-level state (Constitution IX).
- AES-GCM via stdlib only — no new crypto dependency (Constitution
  XI; SDD-03 anti-contract).
- Must compile and run identically on darwin and linux. Test suite
  must pass under `go test -race` and `go test -fuzz=FuzzVaultDecode
  -fuzztime=60s`.
- Public symbols must match the contract in
  `contracts/vault-api.md` exactly. No additional exported
  identifier.

**Scale/Scope**:
- Eleven exported symbols (one struct, one interface, two
  functions, ten sentinel errors → twelve exported identifiers
  total: `Secret`, `Store`, `Load`, `Save`, `ErrBadMagic`,
  `ErrBadVersion`, `ErrShortHeader`, `ErrAuthFailed`,
  `ErrFilePermsLoose`, `ErrSecretNotFound`, `ErrStoreDestroyed`,
  `ErrDuplicateName`, `ErrFileTooLarge`, `ErrInvalidName`).
- Four files of production code: `file.go`, `codec.go`, `store.go`,
  `permissions.go`. Sentinels live at the top of `file.go`.
- Five test files: `file_test.go`, `codec_test.go`, `store_test.go`,
  `permissions_test.go`, `vault_fuzz_test.go`.
- One package, no further sub-packages introduced.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principles in scope (per SDD-03)

| Principle | Constraint | Plan compliance |
|-----------|------------|-----------------|
| **III. Defense in Depth — Encryption at Rest + Layer 5 secure memory** | AES-256-GCM authenticated encryption at rest; Argon2id-derived 32-byte key (SDD-01) consumed via Layer 5 `*SecureBytes`; `crypto/rand` for entropy (never `math/rand`); custom vault format is "obscurity layer 7" — additive only, never load-bearing for the encryption guarantee. | The codec uses `crypto/aes` + `crypto/cipher.NewGCM` (stdlib AES-256-GCM with 16-byte tag). The 32-byte key is supplied by the caller as `*securebytes.SecureBytes` and borrowed via `Use(fn)` for the duration of `Seal` / `Open` — the raw key never leaves `securebytes` ownership and never appears in a package-level variable. The 16-byte salt and the 12-byte nonce are generated with `crypto/rand.Read` on every `Save`; `math/rand` is not imported. Plaintext secret values are placed into `*SecureBytes` directly by the value-wrapper's `UnmarshalJSON` (no Go-`string` intermediate). The HUSH magic + version are fixed bytes; the file format is documented as "obscurity layer 7 — never load-bearing", matching Constitution III's "additive only" rule. ✅ |
| **VIII. Testing Discipline** | 100% coverage on `internal/vault/...`; mandatory fuzz target #1 (`FuzzVaultDecode`) running clean ≥60 s in CI; AC-2 (vault round-trip) maps to concrete tests; sentinel-leak (`SECRET_SHOULD_NEVER_APPEAR_3`) absent from any error or log; `go test -race` clean. | The chunk contract's eleven test names + `TestVault_NoLeakInError` + `TestStore_ConcurrentGet` collectively cover every FR and acceptance scenario in `spec.md`. `FuzzVaultDecode` drives `Load` with a random byte-stream corpus seeded from the round-trip test fixtures and asserts (a) no panic, (b) no >50 MiB allocation, (c) every error returned is one of the ten typed sentinels (or wraps one). Coverage is verified by `go test -cover ./internal/vault/` (target: 100%). The race test spawns 100 goroutines against `Store.Get` and runs under `-race`. The sentinel-leak test packs `SECRET_SHOULD_NEVER_APPEAR_3`, triggers `ErrAuthFailed` with a wrong key, captures the `err.Error()` AND a buffered `slog.JSONHandler` log line, and asserts the sentinel byte sequence is absent from both. ✅ |
| **X. Observability & Redaction** | Type-driven `[redacted]` rendering inherited from `*SecureBytes`; no secret values in errors or logs (length-only summaries, prefixes, suffixes, hashes all forbidden); `log/slog` is the structured logger; secret name MAY appear in errors, secret value MUST NOT. | Every secret value lives in a `*securebytes.SecureBytes` from the moment of decryption forward — `*SecureBytes`' `MarshalJSON`, `String`, and `LogValue` already render `[redacted]` (SDD-02 contract). Error messages identify failure mode + (where applicable) file path + secret name, never value bytes; `errors.Is` / `errors.As` is the comparison primitive (Constitution IX), no string compares. The package uses `log/slog` with a passed-in `*slog.Logger` from the caller (no global logger created here). The `TestVault_NoLeakInError` sentinel test is the executable assertion of the no-secret-in-error rule. ✅ |
| **XI. Native-First, Minimal Dependencies, Ephemeral Vault** | Stdlib first; no new crypto dependency; no `/vendor`; `CGO_ENABLED=0`; vault file is *explicitly not backed up* (rebuildable from upstream, not a disaster on loss). | All cryptographic primitives are stdlib (`crypto/aes`, `crypto/cipher`, `crypto/rand`). The only intra-repo import is `internal/vault/securebytes` — the same module's sub-package, no new go.mod entry. No `/vendor` directory introduced. The package contains no CGO. The 64-MiB load-side size cap (FR-019a) is consistent with the ephemeral-vault posture: even a worst-case attacker-controlled vault file at the documented path cannot blow per-call memory. ✅ |

### Other principles (not in scope but checked for non-violation)

- **I (Zero Files at Rest on Agent Machines):** the vault file lives
  on the *vault host*, not on agent machines. This package has no
  agent-side surface. ✅
- **II (Approval is Human):** out of scope — no approval surface.
  ✅
- **IV (Supervisor / Wrap-Shell), V (Staleness), VI (Tailscale-Only),
  VII (CLI):** out of scope — no supervisor, network, or CLI
  surface. ✅
- **IX (Idiomatic Go Discipline):** in scope as a baseline
  (sentinel errors, no globals, no `init`, no panic in library code,
  context as first parameter, accept-interfaces-return-concrete-types
  for the consumer-defined `Store` interface). All satisfied — the
  exported `Store` interface is defined here because the *consumers*
  (server, secret CLI, harness) want a polymorphic seam for tests
  and for the SDD-10 SIGHUP swap; the implementation type
  (`memStore`) is unexported, returning the interface from `Load`
  matches the producer-side discipline expected here for
  test-double substitution. ✅

### Gate result

**PASS** — every principle in scope is satisfied without exception.
**Complexity Tracking is empty.** The Constitution Check is
re-evaluated post-design (after Phase 1) below.

## Project Structure

### Documentation (this feature)

```text
specs/003-vault-format/
├── plan.md                       # This file (/speckit-plan command output)
├── research.md                   # Phase 0 output (decisions on locked HOW questions)
├── data-model.md                 # Phase 1 output (entities + state)
├── quickstart.md                 # Phase 1 output (consumer integration recipe)
├── contracts/
│   └── vault-api.md              # Phase 1 output (exported API contract — locks PACKAGE-MAP §internal/vault)
├── checklists/                   # Pre-existing artifact directory (untouched by /speckit-plan)
├── spec.md                       # WHAT contract (already written by /speckit-specify + /speckit-clarify)
└── tasks.md                      # Phase 2 output (/speckit-tasks command — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/vault/
├── file.go                       # HUSH header constants + sentinel errors + Load + Save + atomic-write driver
├── codec.go                      # AES-256-GCM seal/open + JSON wire types + custom UnmarshalJSON / MarshalJSON
├── store.go                      # type memStore + Get + Names + Destroy (implements exported Store interface)
├── permissions.go                # checkFileMode / checkParentMode helpers (FR-018, FR-019)
├── file_test.go                  # Round-trip, atomic-save, mode-0600, parent-mode-0700, magic/version/short-header/auth-failed unit tests
├── codec_test.go                 # AES-GCM seal/open round-trip + custom JSON marshal/unmarshal + base64 edge cases + no-string-leak assertion
├── store_test.go                 # Get/Names/Destroy unit tests + TestStore_ConcurrentGet (100 goroutines, race-clean) + ErrStoreDestroyed
├── permissions_test.go           # Loose-file-mode + loose-parent-mode + FilePermsLoose classification
├── vault_fuzz_test.go            # FuzzVaultDecode (corpus seeded from round-trip fixtures; ≥60 s clean; no panic; ≤50 MiB; every error typed)
└── securebytes/                  # SDD-02 sub-package — UNCHANGED by this chunk
    ├── doc.go
    ├── securebytes.go
    ├── securebytes_darwin.go
    ├── securebytes_linux.go
    ├── export_test.go
    └── securebytes_test.go
```

**Structure Decision**: hush is a single Go module
(`github.com/mrz1836/hush`) with a flat `internal/<domain>` layout
defined in `docs/PACKAGE-MAP.md`. SDD-03 fills the parent
`internal/vault/` package itself, leaving the SDD-02
`internal/vault/securebytes/` sub-package untouched. The four
production files match the SDD-03 chunk contract exactly (no extra
file like `errors.go`, `vault.go`, or `reload.go` is introduced;
sentinels live at the top of `file.go`, the top-level `Load` /
`Save` orchestration also lives in `file.go`, and the `reload.go`
named in `docs/PACKAGE-MAP.md` is owned by SDD-10 not SDD-03). The
package import path is `github.com/mrz1836/hush/internal/vault`.
Per `docs/PACKAGE-MAP.md` ("`internal/vault` should not import
`internal/server` or `internal/discord`"), the only intra-repo
import is the `securebytes` sub-package — even tighter than the
documented constraint.

## Constitution Re-check (post-design)

Re-evaluated after Phase 1 design artifacts (`research.md`,
`data-model.md`, `contracts/vault-api.md`, `quickstart.md`) were
drafted:

- The Phase 0 research locks every HOW choice to a stdlib primitive
  or to the SDD-02 `securebytes` sub-package. **No new dependency
  emerged.** ✅ Principle XI.
- The contract documents the exact twelve exported identifiers
  matching the SDD-03 baseline (six sentinels + two functions + one
  struct + one interface) plus the four spec-clarification-driven
  sentinels (`ErrStoreDestroyed`, `ErrDuplicateName`,
  `ErrFileTooLarge`, `ErrInvalidName`). The four additions are a
  *strict superset* of the chunk contract — the chunk contract's
  rule is "MAY NOT silently weaken" (Principle III); a strict
  superset is by definition not a weakening, and FR-028 mandates
  the additions. **No leaked internals; no missing sentinel.** ✅
  Principle IX.
- `data-model.md` confirms the payload is `[]byte` (inside
  `*SecureBytes`) throughout the value's lifetime; the only Go
  `string`s on either side of the codec are the secret *name*, the
  secret *description*, the literal `"[redacted]"` (non-secret),
  and the static sentinel error messages (non-secret). The custom
  `UnmarshalJSON` is the only path through which a value ever
  becomes plaintext, and it goes straight to `securebytes.New`
  without an intermediate `string` allocation. ✅ Principle X.
- `quickstart.md` shows callers wiring the public surface — and
  only the public surface — confirming the locked-API shape demanded
  by Constitution III + the SDD-03 anti-contract. ✅ Principle III.
- `data-model.md` documents the explicit `Store` lifecycle states
  (live, destroyed) and the transitions (`Destroy`); the
  destruction-then-`Get` race is documented as "either outcome is
  safe; what the store must not do is return a partially-zeroed
  payload" (Spec edge case). The race test
  (`TestStore_ConcurrentGet`) is enumerated in the contract's
  behavioural-guarantee table, run under `-race`. ✅ Principle
  VIII.
- The contract enumerates the eleven SDD-03-mandated unit test
  names + `TestVault_NoLeakInError` (sentinel leak) + the fuzz
  target name `FuzzVaultDecode`, matching `docs/TESTING-STRATEGY.md`
  §2 (mandatory fuzz target #1) and §5 (sentinel-leak pattern). ✅
  Principle VIII.

**Gate result (post-design): PASS.** No new violations introduced
by the design phase. **Complexity Tracking remains empty.**

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

*(empty — no violations)*

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| — | — | — |
