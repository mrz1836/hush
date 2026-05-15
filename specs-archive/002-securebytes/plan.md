# Implementation Plan: Secure Bytes Container (SDD-02)

**Branch**: `002-securebytes` | **Date**: 2026-04-27 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/002-securebytes/spec.md`
**Chunk contract**: [docs/sdd/SDD-02.md](../../docs/sdd/SDD-02.md)

## Summary

`internal/vault/securebytes` provides the `SecureBytes` container —
a pointer-only, opaque type that wraps a binary payload under three
simultaneous protections: `mlock`-pinned memory (no swap, no
relocation of the pinned region), type-driven `[redacted]` rendering
on every standard log/format/JSON path, and zero-on-destroy semantics
(both explicit `Destroy` and a runtime finalizer). It is the leaf
that every other secret-handling package downstream depends on
(SDD-03 vault payload, SDD-07 JWT signing key, SDD-09 ECIES envelope,
SDD-13 server handlers, SDD-16 client-side decrypt, SDD-21 supervisor
grace cache).

Approach (locked by SDD-02 + Constitution III/IX/X/XI; not subject to
research alternatives):

- Memory pinning: `golang.org/x/sys/unix.Mlock` / `unix.Munlock` via
  build-tagged per-OS files (`_darwin.go`, `_linux.go`). NO cgo, NO
  `unsafe` outside the syscall wrappers.
- Construction: `New(b []byte)` copies into a fresh buffer, mlocks the
  copy, then zeroes the input slice. Constructor accepts `[]byte`
  only — `string` is forbidden because Go strings cannot be reliably
  zeroed.
- Read path: `Use(fn func(b []byte)) error` is the ONLY way to read
  the payload. No `Bytes()` accessor. Closure is documented as
  borrow-only.
- Destruction: `Destroy()` zeroes the buffer with a volatile-style
  write, calls `Munlock`, marks destroyed; idempotent.
- Reclamation safety net: `runtime.SetFinalizer` wired in `New` calls
  `Destroy` if the value becomes unreachable without explicit destroy.
- Concurrency: a single `sync.Mutex` protects the destroyed flag and
  the buffer pointer. `Use` holds the mutex for the duration of the
  callback (Spec edge case "Concurrent borrow and destroy" — destroy
  cannot yank the buffer mid-borrow).
- Render protection: `LogValue() slog.Value` returns
  `slog.StringValue("[redacted]")`; `String()` returns `"[redacted]"`;
  `MarshalJSON()` returns `[]byte("[redacted]")`. None of the three
  touches the underlying bytes. The same applies before AND after
  destruction (Spec FR-017).
- Documented residual risk (per `docs/SECURITY.md` Layer 5 / §6): the
  Go runtime may transiently copy heap objects during GC compaction
  in pathological cases. This is documented in the package `doc.go`;
  no bandaid mitigation is added beyond the pinned-region design.

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`); CGO disabled
(constitution IX).
**Primary Dependencies**:
- `golang.org/x/sys/unix` (`Mlock`, `Munlock`) — already a transitive
  dep via `golang.org/x/crypto`; `go.sum` already pins
  `golang.org/x/sys v0.43.0`. Promoted from indirect to direct here.
- Go stdlib: `errors`, `fmt`, `log/slog`, `runtime`, `sync`.
- Test-only: `testing`, `bytes`, `encoding/json`, plus an in-test
  slog handler for the redaction sentinel test.
- No other crypto, FFI, or third-party deps.

**Storage**: None. The container is in-process only. No file I/O,
no network, no env-var writes, no Keychain access.
**Testing**: Go stdlib `testing` (table-driven unit tests, race
detector, finalizer-trigger test, sentinel-redaction test, concurrent-
use race test). No fuzz target — `SecureBytes` has no parser surface
(SDD-02 contract; cross-checked against `docs/TESTING-STRATEGY.md` §2
fuzz list).
**Target Platform**: macOS (darwin amd64/arm64) + Linux (amd64/arm64),
per `.goreleaser.yml` and Spec FR-019. Windows is out of scope.
**Project Type**: Single Go module (`github.com/mrz1836/hush`).
`internal/vault/securebytes` is a sub-package under the existing
`internal/vault/` domain (currently empty per
`docs/PACKAGE-MAP.md`).
**Performance Goals**:
- `New`, `Destroy`, `Len`, `Use` are O(n) in the payload length for
  copy/zero, O(1) for everything else. `mlock` / `munlock` cost is
  syscall + page fault on the locked pages — acceptable for the
  expected payload sizes (32-byte AES keys, 65-byte secp256k1 keys,
  1–8 KiB secret values).
- No allocation hot path in the borrow read; the closure receives the
  underlying buffer directly.

**Constraints**:
- 100% test coverage required (Constitution VIII; codecov gate for
  security-critical packages — `internal/vault/...`).
- No `[]byte → string` conversion of secret material anywhere in this
  package (Constitution X anti-contract).
- No `cgo` (Constitution IX); `unsafe` only inside the syscall
  wrappers if absolutely required (in practice
  `golang.org/x/sys/unix` already encapsulates this).
- No package-level mutable state; no `init()`.
- Must compile and run identically on darwin and linux. Test suite
  must pass under `go test -race`.
- Public symbols must match the contract in `contracts/securebytes-api.md`
  exactly. No additional exported identifier.
- Documented Go-runtime memory-copy residual risk (`docs/SECURITY.md`
  §6) is in scope to document, NOT to mitigate.

**Scale/Scope**:
- Eight exported symbols (one type, six methods, one constructor,
  one sentinel error).
- Four files of production code: `securebytes.go` (cross-platform
  API + finalizer wiring + render methods), `securebytes_darwin.go`
  (mlock/munlock for darwin), `securebytes_linux.go` (mlock/munlock
  for linux), `doc.go` (package overview + Layer 5 residual-risk
  note).
- One test file: `securebytes_test.go`. Tests are pure-Go and
  platform-agnostic; the per-OS `_darwin.go` / `_linux.go` files are
  thin syscall wrappers exposing the same `mlock` / `munlock`
  identifiers to the cross-platform code.
- One package, no sub-packages.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principles in scope (per SDD-02)

| Principle | Constraint | Plan compliance |
|-----------|-----------|-----------------|
| **III. Defense in Depth — Layer 5 (mlocked secure memory + zero on free)** | Sensitive material held in `SecureBytes` (mlock + zero on free); heap-copy hazard documented and avoided; `[]byte`-only mandate. | Constructor signature is `New(b []byte)` — string is rejected at the type level. Buffer is `mlock`-pinned for the container's full lifetime; `munlock` runs as part of `Destroy` (explicit and finalizer paths). Volatile-style zero-write defeats compiler dead-store elimination. The package `doc.go` documents the residual Go-runtime-copy risk (`docs/SECURITY.md` Layer 5 + §6) explicitly, per the user prompt. |
| **VIII. Testing Discipline** | 100% coverage on security-critical packages (`vault`, `keys`, `token`, `transport`); table-driven unit tests; `go test -race` clean; sentinel-leak redaction test (per `docs/TESTING-STRATEGY.md` §5). | Eight test names enumerated below cover every behaviour and anti-contract: `TestSecureBytes_New_CopiesAndZeroesInput`, `TestSecureBytes_Use_DeliversPayload`, `TestSecureBytes_Destroy_ZeroesAndIdempotent`, `TestSecureBytes_PostDestroy_ReturnsErrDestroyed`, `TestSecureBytes_Render_RedactsAllPaths`, `TestSecureBytes_RedactionSentinel` (sentinel-leak), `TestSecureBytes_FinalizerZerosOnGC` (forces GC), `TestSecureBytes_ConcurrentUse` (`-race` clean). Coverage tooling via `go test -cover`; race detector via `magex test:race`. No fuzz target — there is no parser surface in this package (cross-checked against the `docs/TESTING-STRATEGY.md` §2 mandatory fuzz list, which names file/JWT/ECIES/signature/TOML/socket-JSON parsers — none of which live here). |
| **IX. Idiomatic Go Discipline** | Sentinel errors as exported `var Err... = errors.New(...)`; no globals (mutable); no `init()`; pure-Go (`CGO_ENABLED=0`); no panics in library code; goroutine discipline; interfaces at consumer. | Sentinel `ErrDestroyed = errors.New("hush/vault/securebytes: destroyed")` — exported, no string compares. No mutable globals; no `init()`. CGO disabled — `golang.org/x/sys/unix` is the syscall surface (it does NOT depend on cgo on darwin/linux for `Mlock`/`Munlock`). No panics in library code: `Use` returns `ErrDestroyed`, `Destroy` returns `error` (always `nil` after success, propagates `Munlock` errors otherwise). The package spawns no goroutines. The `slog.LogValuer` interface is implemented at the producer (the type) — the recognised exception to "interfaces at consumer" — because `log/slog` defines this exact interface for type-driven redaction; this matches Constitution X's directive. |
| **X. Observability & Redaction** | Type-driven `[redacted]` rendering: `LogValue() slog.Value` returns `slog.StringValue("[redacted]")`; `[]byte` carrying secret material wrapped before any logging; no secret values in errors. | The type implements `slog.LogValuer`, `fmt.Stringer`, and `json.Marshaler`, all returning the literal `"[redacted]"`. None of the three render paths reads the underlying buffer. `ErrDestroyed`'s message identifies the failure mode only; no payload bytes leak. The `TestSecureBytes_RedactionSentinel` test wraps the canonical sentinel `SECRET_SHOULD_NEVER_APPEAR_2`, passes the container to a `slog.JSONHandler` writing into a buffer, and asserts the sentinel never appears in any captured byte of output (per `docs/TESTING-STRATEGY.md` §5). |
| **XI. Native-First, Minimal Dependencies, Ephemeral Vault** | Stdlib first; new direct dependencies need PR justification; crypto stack frozen; govulncheck + gitleaks in CI. | No NEW direct dep is added. `golang.org/x/sys` is already in `go.sum` as an indirect dep of `golang.org/x/crypto`; it is promoted to a direct dep here. This satisfies the "trusted-sources hierarchy" rule (golang.org/x is the sigil baseline tier). No new crypto dep — this package contains zero cryptographic primitives. `govulncheck` runs in CI; `gitleaks` runs pre-commit. |

### Other principles (not in scope but checked for non-violation)

- **Principle I (Zero Files at Rest):** the container holds only
  in-memory state; no file is written. ✅
- **Principle II (Approval is Human):** out of scope (no approval
  surface). ✅
- **Principle IV (Supervisor / Wrap-Shell), V (Staleness),
  VI (Tailscale-Only), VII (CLI):** out of scope (no daemon, network,
  or CLI surface). ✅

### Gate result

**PASS** — every principle in scope is satisfied without exception.
**Complexity Tracking is empty.** The Constitution Check is
re-evaluated post-design (after Phase 1) below.

## Project Structure

### Documentation (this feature)

```text
specs/002-securebytes/
├── plan.md                       # This file (/speckit-plan command output)
├── research.md                   # Phase 0 output (decisions on locked HOW questions)
├── data-model.md                 # Phase 1 output (entities + state)
├── quickstart.md                 # Phase 1 output (consumer integration recipe)
├── contracts/
│   └── securebytes-api.md        # Phase 1 output (exported API contract — locks PACKAGE-MAP §internal/vault/securebytes)
├── checklists/                   # Pre-existing artifact directory (untouched by /speckit-plan)
├── spec.md                       # WHAT contract (already written by /speckit-specify + /speckit-clarify)
└── tasks.md                      # Phase 2 output (/speckit-tasks command — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/vault/securebytes/
├── doc.go                        # Package overview + Layer 5 residual-risk note (Go runtime memory-copy)
├── securebytes.go                # type SecureBytes; New; Use; Len; Destroy; LogValue; String; MarshalJSON; ErrDestroyed; finalizer wiring
├── securebytes_darwin.go         # //go:build darwin — mlock/munlock via golang.org/x/sys/unix
├── securebytes_linux.go          # //go:build linux  — mlock/munlock via golang.org/x/sys/unix
└── securebytes_test.go           # All eight test names listed in Constitution Check row VIII (race-clean, 100% coverage)
```

**Structure Decision**: hush is a single Go module
(`github.com/mrz1836/hush`) with a flat `internal/<domain>` layout
defined in `docs/PACKAGE-MAP.md`. SDD-02 introduces the FIRST
sub-package under `internal/vault/` (`securebytes/`); the parent
`internal/vault/` package itself remains empty until SDD-03 fills the
vault file format. The sub-package layout matches Go conventions
(import path `github.com/mrz1836/hush/internal/vault/securebytes`)
and respects the dependency rule from `docs/PACKAGE-MAP.md`
("`internal/vault` should not import `internal/server` or
`internal/discord`") — `securebytes` is even more conservative: it
imports nothing from `internal/...` at all (leaf-package shape, same
as `internal/keys`). All production code lives under
`internal/vault/securebytes/`; tests live alongside.

## Constitution Re-check (post-design)

Re-evaluated after Phase 1 design artifacts (`research.md`,
`data-model.md`, `contracts/securebytes-api.md`, `quickstart.md`)
were drafted:

- The Phase 0 research locks every HOW choice to a stdlib primitive
  or to `golang.org/x/sys/unix` — already in `go.sum`. **No new
  dependency emerged.** ✅ Principle XI.
- The contract documents the exact eight exported symbols matching
  the SDD-02 pre-locked list. No additional exported identifier
  required. **No leaked internals.** ✅ Principle IX.
- `data-model.md` confirms the payload is `[]byte` throughout the
  container's lifetime; the only `string` returned by the package is
  the literal `"[redacted]"` (non-secret) and `ErrDestroyed`'s static
  message (non-secret). ✅ Principle X.
- `quickstart.md` shows callers wiring the public surface — and only
  the public surface — confirming the leaf-package shape demanded by
  Constitution III + the SDD-02 anti-contract. ✅ Principle III.
- `data-model.md` documents the explicit lifecycle states (live,
  destroyed) and the transitions (`Destroy`, finalizer). The
  finalizer-trigger test (`TestSecureBytes_FinalizerZerosOnGC`) is
  enumerated in the contract's behavioural-guarantee table. ✅
  Principle VIII.
- The contract enumerates one redaction sentinel test
  (`TestSecureBytes_RedactionSentinel`), matching `docs/TESTING-
  STRATEGY.md` §5. No fuzz target is enumerated, matching the §2
  fuzz list (which does not include this package's surface).
  ✅ Principle VIII.

**Gate result (post-design): PASS.** No new violations introduced by
the design phase. **Complexity Tracking remains empty.**

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

*(empty — no violations)*

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| — | — | — |
