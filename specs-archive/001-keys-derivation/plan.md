# Implementation Plan: Hush Key Hierarchy Derivation (SDD-01)

**Branch**: `001-keys-derivation` | **Date**: 2026-04-27 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/001-keys-derivation/spec.md`
**Chunk contract**: [docs/sdd/SDD-01.md](../../docs/sdd/SDD-01.md)

## Summary

`internal/keys` deterministically derives the entire hush key hierarchy
(JWT-signing, vault-encryption, audit-signing, per-machine client keypair)
from an operator passphrase + 16-byte salt, using Argon2id as the master
KDF and BIP32 hierarchical derivation for the four sub-keys. It is a
leaf package — zero key files exist on disk, zero imports from
`internal/*`, zero logging surface — consumed by SDD-03 (vault),
SDD-07 (JWT), SDD-08 (request signing), SDD-09 (ECIES), and SDD-15
(`hush init`).

Approach (locked by SDD-01 + Constitution III/XI; not subject to research
alternatives):

- KDF: `golang.org/x/crypto/argon2.IDKey` with `time=4`,
  `memory=256*1024 KiB`, `threads=4`, `keyLen=64`.
- HD derivation: `github.com/bitcoinschema/go-bitcoin/v2` (secp256k1 +
  BIP32), with hush coin-type `7743'`.
- Path layout: `m/44'/7743'/{0,1,2}'` for JWT / vault / audit;
  `m/44'/7743'/3'/{machine_index}` for per-machine client keys.
- Validation: passphrase `< 12` bytes → `ErrPassphraseTooShort`; salt
  length `!= 16` → `ErrSaltMissing`. Both fail BEFORE Argon2id runs.
- Cancellation: `ctx.Err()` checked once at entry of
  `DeriveMasterSeed`; argon2 is non-interruptible by design.

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`); CGO disabled (constitution IX).
**Primary Dependencies**:
- `golang.org/x/crypto/argon2` (Argon2id KDF — stdlib-adjacent, golang.org/x).
- `github.com/bitcoinschema/go-bitcoin/v2` (secp256k1, BIP32 HD wallet).
- Go stdlib: `context`, `crypto/ecdsa`, `crypto/sha256`, `encoding/hex`,
  `encoding/binary`, `errors`.
- No other crypto dependencies (Constitution XI prohibition).

**Storage**: None. Derived material lives in caller-owned `[]byte` /
`*ecdsa.PrivateKey` only. No file writes, no env-var writes, no
keychain writes.
**Testing**: Go stdlib `testing` (table-driven unit tests + native
fuzz target `FuzzDeriveMaster`). Race detector enabled via `magex
test:race`. Coverage tool via `go test -cover`.
**Target Platform**: macOS (darwin amd64/arm64) + Linux (amd64/arm64),
per `.goreleaser.yml`. CPU-architecture-independent (FR-002, edge case
"Cross-process determinism").
**Project Type**: Single Go module (`github.com/mrz1836/hush`).
`internal/keys` is one package within that module.
**Performance Goals**:
- Argon2id seed derivation completes in `< 5s` on a 2026-class server
  CPU (NFR-5; the locked parameters are tuned to this budget).
- BIP32 sub-key derivation is sub-millisecond (pure CPU, no allocation
  hot-path concerns at v0.1.0).
- Validation rejection (`ErrPassphraseTooShort` / `ErrSaltMissing`)
  returns in `< 100ms` (SC-004).
**Constraints**:
- 100% test coverage required (Constitution VIII; codecov gate for
  security-critical packages).
- 60-second `FuzzDeriveMaster` run must produce zero panics, zero
  crashes, deterministic re-derivation (SC-002).
- Determinism across machines, OSes, and CPU architectures (FR-002).
- Concurrent invocations safe; no shared mutable state.
- No `[]byte → string` conversion of secret material anywhere in the
  package (Constitution X anti-contract).
- No `math/rand` anywhere; no `crypto/rand` either (this package
  consumes randomness from upstream, never generates it).
- No `init()`, no package-level mutable state (Constitution IX).
**Scale/Scope**:
- Six exported symbols (five functions + one helper + two sentinel
  errors).
- Four files of production code: `derive.go`, `paths.go`, `client.go`,
  `fingerprint.go`.
- Four test files: `derive_test.go`, `paths_test.go`,
  `client_test.go`, `derive_fuzz_test.go`.
- One package, no sub-packages.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principles in scope (per SDD-01)

| Principle | Constraint | Plan compliance |
|-----------|-----------|-----------------|
| **III. Defense in Depth — Layer 1 (Key hierarchy, no key files on disk)** | Argon2id `time=4 / memory=256MB / threads=4 / keyLen=64`; BIP32 paths `m/44'/7743'/{0,1,2,3}'`; `crypto/rand` only (no `math/rand`); no key files persisted. | KDF parameters encoded as named constants in `derive.go`. BIP32 paths encoded as named constants in `paths.go`. No randomness generated in this package — entropy is consumed from caller-provided passphrase + salt. No file I/O. |
| **VIII. Testing Discipline** | 100% coverage on security-critical packages (`vault`, **`keys`**, `token`, `transport`); table-driven unit tests; mandatory fuzz `FuzzDeriveMaster`; `go test -race` clean. | Six unit-test names enumerated in SDD-01 covered by `derive_test.go`, `paths_test.go`, `client_test.go`. Fuzz target lives in `derive_fuzz_test.go`. Race-detector run is part of the gate. AC-7 mapping recorded in `docs/AC-MATRIX.md` during the implement phase. |
| **IX. Idiomatic Go Discipline** | Context-first on cancellable / I/O / timeout-bearing functions; sentinel errors; no globals (mutable); no `init()`; pure-Go (`CGO_ENABLED=0`); modules only; no panics in library code. | `DeriveMasterSeed` is the sole exported function with a cancellable workload (Argon2id) — it takes `ctx context.Context` as its first parameter. `DeriveJWTSigningKey`, `DeriveVaultEncKey`, `DeriveAuditSigningKey`, `DeriveClientKey`, `PublicKeyFingerprint` are pure CPU work (sub-ms BIP32 / SHA-256 derivation), so per Principle IX they do **not** carry `ctx`. Sentinel errors `ErrPassphraseTooShort` and `ErrSaltMissing` are exported package-level `var = errors.New(...)`. No mutable globals; no `init()`. Pure Go (no CGO). All errors returned (no panics). |
| **X. Observability & Redaction** | No third-party logger; no secret values in errors; no `[]byte → string` conversion of secret material. | This package has **no logging surface** — it is a pure-function library (verified by `grep` in the implement phase). Errors return failure mode + identifier (e.g. `ErrPassphraseTooShort`) without echoing passphrase / salt bytes. Secret material (seed, sub-keys) flows out as `[]byte` / `*ecdsa.PrivateKey` only — never converted to `string` inside this package. The 16-hex-char `PublicKeyFingerprint` operates on the public key only (non-secret per spec). |
| **XI. Native-First, Minimal Dependencies, Ephemeral Vault** | Crypto stack frozen at BIP32 + secp256k1 + ECIES via go-bitcoin + golang.org/x/crypto; new crypto deps require a constitutional amendment. | Direct deps for this package: `github.com/bitcoinschema/go-bitcoin/v2` and `golang.org/x/crypto/argon2` only. Both are pre-existing in the locked crypto stack. No new direct deps introduced. `govulncheck` runs in CI; `gitleaks` runs pre-commit. Phase-0 research **does not** evaluate alternatives — the contract is locked. |

### Other principles (not in scope but checked for non-violation)

- **Principle I (Zero Files at Rest on Agent Machines):** plan persists no
  files; complies trivially.
- **Principle II (Approval is Human):** out of scope (no approval surface).
- **Principle IV–VII:** out of scope (no daemon, network, or CLI surface).

### Gate result

**PASS** — every principle in scope is satisfied without exception.
No items in **Complexity Tracking**. The Constitution Check is re-evaluated
post-design (after Phase 1) below.

## Project Structure

### Documentation (this feature)

```text
specs/001-keys-derivation/
├── plan.md              # This file (/speckit-plan command output)
├── research.md          # Phase 0 output (decisions on locked HOW questions)
├── data-model.md        # Phase 1 output (entities + state)
├── quickstart.md        # Phase 1 output (consumer integration recipe)
├── contracts/
│   └── keys-api.md      # Phase 1 output (exported API contract — locks PACKAGE-MAP §internal/keys)
├── checklists/          # Pre-existing artifact directory (untouched by /speckit-plan)
├── spec.md              # WHAT contract (already written by /speckit-specify)
└── tasks.md             # Phase 2 output (/speckit-tasks command — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/keys/
├── derive.go              # DeriveMasterSeed (Argon2id) + sentinel errors
├── paths.go               # DeriveJWTSigningKey / DeriveVaultEncKey / DeriveAuditSigningKey + path constants
├── client.go              # DeriveClientKey (machine-indexed)
├── fingerprint.go         # PublicKeyFingerprint
├── derive_test.go         # TestDeriveMasterSeed_* (determinism, validation, ctx)
├── paths_test.go          # TestDeriveJWTSigningKey_Path + sibling sub-key tests
├── client_test.go         # TestDeriveClientKey_MachineIndexIsolation
└── derive_fuzz_test.go    # FuzzDeriveMaster (≥60s clean, deterministic re-derivation)
```

**Structure Decision**: hush is a single Go module
(`github.com/mrz1836/hush`) with a flat `internal/<domain>` layout
defined in `docs/PACKAGE-MAP.md`. SDD-01 fills exactly one of those
domain packages (`internal/keys`) and adds nothing outside it. This
matches the canonical "single project" Go layout — no separate
`backend/`, `frontend/`, or platform-specific tree is required. All
production code lives under `internal/keys/`; all tests live alongside
the production code (Go convention, enforced by golangci-lint).

## Constitution Re-check (post-design)

Re-evaluated after Phase 1 design artifacts (`research.md`,
`data-model.md`, `contracts/keys-api.md`, `quickstart.md`) were drafted:

- The Phase 0 research locks every HOW choice to a stdlib or to one of
  the two pre-existing crypto deps. **No new dependency emerged.**
  ✅ Principle XI.
- The contract documents the exact exported API matching what SDD-01
  pre-locked. No additional exported symbols required. **No leaked
  internals.** ✅ Principle IX (interfaces at consumer, not producer).
- `data-model.md` confirms every secret-bearing value is `[]byte` or
  `*ecdsa.PrivateKey`; no `string` representations of secrets.
  ✅ Principle X.
- `quickstart.md` shows callers wiring the public surface — and only
  the public surface — confirming the leaf-package shape demanded by
  Constitution III + the SDD-01 anti-contract. ✅ Principle III.
- The contract enumerates one fuzz target (`FuzzDeriveMaster`),
  matching the §2 list in `docs/TESTING-STRATEGY.md`. ✅ Principle VIII.

**Gate result (post-design): PASS.** No new violations introduced by
the design phase. **Complexity Tracking remains empty.**

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

*(empty — no violations)*

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| — | — | — |
