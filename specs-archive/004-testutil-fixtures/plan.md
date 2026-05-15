# Implementation Plan: Test Fixtures, Sentinel Helpers, and Programmable Discord Approval Stub (SDD-04)

**Branch**: `004-testutil-fixtures` | **Date**: 2026-04-27 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/004-testutil-fixtures/spec.md`
**Chunk contract**: [docs/sdd/SDD-04.md](../../docs/sdd/SDD-04.md)

## Summary

`internal/testutil` is the project's shared, test-only harness package.
It ships five primitives that every downstream test inside `internal/`
relies on: a deterministic test-keys helper, a temp-dir-scoped vault
fixture that writes a real HUSH-format file, a sentinel-string helper,
a sentinel-absent assertion, and a programmable Discord approval stub
with a per-call response queue. The package's reason for existing is
**indirect support of AC-9** (constitutional coverage and fuzz-target
gates): every chunk that follows leans on these primitives to meet the
100% / 95% / 85% coverage bands without each test author re-inventing —
and accidentally weakening — the same fixtures.

Approach (locked by SDD-04 + Constitution VIII/IX/X/I; not subject to
research alternatives):

- **Test-keys helper** (`keys_fixture.go`): `NewTestKeys(t)` invokes
  `keys.DeriveMasterSeed(ctx, []byte("hush-test-seed-NEVER-USE-IN-PROD"),
  testSalt)` where `testSalt` is a fixed 16-byte literal
  (`hex"0102030405060708090A0B0C0D0E0F10"`) defined as a `const`-style
  package-level slice constructor (no mutable global; returned by an
  unexported zero-arg constructor). The 32-byte-min passphrase length
  is satisfied (`"hush-test-seed-NEVER-USE-IN-PROD"` is 32 bytes), and
  the salt length is exactly 16 bytes — both gates pass deterministically.
  The returned 64-byte master seed is the same on every host. The
  helper takes `*testing.T` as its only parameter; it has no cleanup
  side-effect to register (the seed is a pure value), so the
  `t.Cleanup` discipline is satisfied trivially (no resource → no leak).
- **Vault fixture** (`vault_fixture.go`): `NewTestVault(t, secrets)`
  derives the master seed via the same deterministic call as
  `NewTestKeys`, derives the 32-byte AES-256-GCM key via
  `keys.DeriveVaultEncKey(seed)`, wraps the key in
  `*securebytes.SecureBytes` via `securebytes.New(rawKey)`, materialises
  the `map[string]string` into `[]vault.Secret` (each value wrapped via
  `securebytes.New`), and calls `vault.Save(ctx, path, vaultKey,
  secrets)` where `path = filepath.Join(t.TempDir(), "test.vault")`.
  The fixture registers `t.Cleanup(func() { _ = vaultKey.Destroy(); for
  _, sb := range valueSBs { _ = sb.Destroy() } })` BEFORE returning. The
  returned `cleanup` closure runs the same `Destroy` calls (idempotent
  via `securebytes`'s contract — `Destroy` returns `nil` when already
  destroyed). The `t.TempDir()` directory is `0o700` by Go stdlib
  contract, satisfying `vault.Save`'s parent-mode check.
- **Sentinel helper** (`sentinel.go`): `SentinelSecret(n int) string`
  returns `fmt.Sprintf("SECRET_SHOULD_NEVER_APPEAR_%d", n)`. Pure,
  stateless, deterministic. Negative indices render as
  `SECRET_SHOULD_NEVER_APPEAR_-1` — still recognisable, no panic
  (FR-edge: sentinel index of zero or negative). No whitespace or
  punctuation in the marker substring, so a substring search via
  `strings.Contains` is reliable.
- **Sentinel-absent assertion** (`sentinel.go`): `AssertSentinelAbsent(t,
  sentinel, haystack)` calls `t.Helper()`, then `if
  strings.Contains(haystack, sentinel) { t.Errorf(...) }`. On failure,
  the message names the sentinel, locates the byte offset of the first
  match (`strings.Index`), and prints a 64-byte window around the match
  (clamped to haystack bounds) so the operator can see the leak in
  context.
- **Discord stub** (`discord_stub.go`): `DiscordStub` is a struct with
  `ApproveAll bool` (the public scenario knob), an unexported
  `responses []Decision` queue (fed via `Enqueue(...Decision)`), an
  unexported `calls []ApprovalCall` recorded list (read via the
  exported `Calls() []ApprovalCall` defensive-copy accessor), an
  unexported `t *testing.T` (held only for the lifetime of the test
  that constructed the stub — Constitution IX-compliant: not stored as
  a `Context`, not a goroutine-leak vector), and an unexported
  `mu sync.Mutex`. `NewDiscordStub(t)` registers a `t.Cleanup`
  callback that drains the recorded calls and the queue (so a leaked
  stub reference cannot carry state into a sibling test). The
  `RequestApproval(ctx, req) (Decision, error)` method:
  1. Locks `mu`.
  2. Appends the request to the recorded-calls list.
  3. If `len(responses) > 0`, pops the head, returns it.
  4. Else if `ApproveAll`, returns `DecisionApprove`.
  5. Else (`ApproveAll == false` AND queue empty): calls
     `t.Errorf("hush/testutil: unexpected Discord approval call: host=%q
     scopes=%v session=%q ttl/uses=%s", req.RequesterHost, req.Scopes,
     req.SessionType, req.LimitDescription())` and returns
     `DecisionDeny, ErrUnexpectedCall`. (FR-018a — loud failure, never
     silent default-deny, never blocking wait.)
  6. Unlocks `mu` (deferred).
  The stub never opens a network socket, never imports a Discord SDK,
  never spawns a goroutine.
- **Local `Approver` interface** (`discord_stub.go`): the narrowest
  contract `DiscordStub` satisfies — single method
  `RequestApproval(ctx context.Context, req ApprovalRequest)
  (Decision, error)`. Defined here because *consumers* (downstream test
  code that wants to inject the stub) need a polymorphic seam before
  SDD-11 introduces the production `Approver` in `internal/discord`.
  When SDD-11 lands, downstream tests migrate to the production
  interface; this local one is renamed or aliased. Until then, this
  local definition is the only one in the project.
- **Package-level documentation** (`doc.go`): a single-paragraph
  package comment naming the package as "test-only", listing the five
  exported helpers, and citing Constitution Principles I and IX as the
  basis for the test-only constraint.
- **Test-only enforcement**: the package contains no `init()` (Constitution
  IX). All helpers accept the test handle as their first parameter.
  Production-import enforcement is layered: (a) `golangci-lint`'s
  `depguard` rule will be configured (in this chunk) to forbid any
  `internal/<production>` package from importing
  `internal/testutil`; (b) the package's own self-test will include a
  build-tag-free repository search that fails if any non-`*_test.go`
  file under `internal/` imports `internal/testutil`.
- **No new dependencies.** All primitives use Go stdlib (`context`,
  `crypto/rand` is NOT used — the test-keys path is fully
  deterministic; `fmt`, `os`, `path/filepath`, `strings`, `sync`,
  `testing`) plus the three intra-repo dependencies already locked
  upstream: `internal/keys`, `internal/vault`,
  `internal/vault/securebytes`.

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`); `CGO_ENABLED=0`
(Constitution IX).

**Primary Dependencies**:
- Go stdlib: `context`, `fmt`, `os`, `path/filepath`, `strings`,
  `sync`, `testing`.
- Intra-repo (locked upstream):
  - `github.com/mrz1836/hush/internal/keys` — SDD-01;
    `DeriveMasterSeed`, `DeriveVaultEncKey`.
  - `github.com/mrz1836/hush/internal/vault` — SDD-03; `Save`,
    `Secret`.
  - `github.com/mrz1836/hush/internal/vault/securebytes` — SDD-02;
    `New`, `*SecureBytes`, `Destroy`.
- Test-only (within self-tests): `testing`, `testing/quick` (NOT
  required), `bytes`.
- **No new direct dependency is added.** Constitution XI satisfied
  trivially.

**Storage**: One temporary file per `NewTestVault` invocation, scoped
to the per-test `t.TempDir()`. The file is `0o600`, parent is `0o700`
(both per `vault.Save`'s contract; `t.TempDir()` produces `0o700` by
the Go stdlib). No database, no network, no IPC.

**Testing**: Go stdlib `testing`. Table-driven unit tests for every
helper, race-detector pass (`magex test:race`), no fuzz target (the
package IS the sentinel infrastructure, per the chunk contract:
"No fuzz, no sentinel-leak"). Coverage measured via `go test -cover
./internal/testutil/`.

**Target Platform**: macOS (darwin amd64/arm64) and Linux (amd64/
arm64), per `.goreleaser.yml`. Windows is out of scope (project-wide).
The package introduces no platform-specific code path.

**Project Type**: Single Go module (`github.com/mrz1836/hush`) with a
flat `internal/<domain>` layout per `docs/PACKAGE-MAP.md`.
`internal/testutil` is a new sibling under `internal/`, the first
test-only package in the tree.

**Performance Goals**:
- `NewTestKeys` execution time: ≤2 s on the project's CI machines —
  dominated by Argon2id's locked cost (`time=4`, `memory=256 MiB`,
  `threads=4`), inherited from SDD-01. The test suite invokes
  Argon2id at most once per process via a `sync.Once`-guarded
  package-level memoised seed (the only "global" introduced — see
  Constitution Check).
- `NewTestVault` execution time: ≤2 s on first invocation in a
  process (Argon2id), ≤50 ms thereafter (cache hit + AES-GCM seal of
  ≤2 MiB plaintext + a temp-dir write/sync).
- `SentinelSecret` and `AssertSentinelAbsent`: O(len(haystack)).
  Sub-microsecond for typical haystacks (≤1 MiB log buffer).
- `DiscordStub.RequestApproval`: mutex-bounded; sub-microsecond.

**Constraints**:
- ≥80% test coverage on `internal/testutil/` (relaxed from the
  100%-on-security-critical bar because helpers without external
  surface area are exercised by the consuming tests; per SDD-04
  chunk contract).
- No `init()` (Constitution IX).
- Production source files (`*.go` not ending in `_test.go`) MUST
  NOT import `internal/testutil`. Enforced by both lint
  (`depguard`) and a self-test in this package.
- All helpers take `*testing.T` as first parameter (Constitution
  VIII discipline + spec FR-020 leak-safety).
- `vault.Save`'s parent-directory mode check (`0o700`) is satisfied
  by `t.TempDir()` (Go stdlib guarantee).
- No CGO, no new module dependency, no `vendor/` directory
  (Constitution IX, XI).
- The Argon2id-derived seed is memoised behind a `sync.Once`-guarded
  unexported package-level cache. This is the ONLY package-level
  mutable state — see Constitution Check for why this is permitted
  under Principle IX (it is set-once, monotonic, observable only
  through a function call, and the lint exception is documented inline).

**Scale/Scope**:
- Eight exported identifiers (matching the chunk contract):
  `NewTestVault`, `NewTestKeys`, `SentinelSecret`,
  `AssertSentinelAbsent`, `DiscordStub`, `NewDiscordStub`,
  `ApprovalCall`, `Approver`. Plus three exported support
  identifiers entailed by the API: `Decision` (enum type) with
  three values (`DecisionApprove`, `DecisionDeny`,
  `DecisionApproveMute`), `ApprovalRequest` (struct), and
  `ErrUnexpectedCall` (sentinel error returned by the
  `RequestApproval` failure path). Total exported surface: 12
  identifiers.
- Five files of production code: `vault_fixture.go`,
  `keys_fixture.go`, `sentinel.go`, `discord_stub.go`, `doc.go`.
- Four test files: `vault_fixture_test.go`, `keys_fixture_test.go`,
  `sentinel_test.go`, `discord_stub_test.go`. (Plus optionally a
  `package_test.go` for the no-production-import grep self-test.)
- One package, no sub-packages, no platform-specific build tags.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principles in scope (per SDD-04)

| Principle | Constraint | Plan compliance |
|-----------|------------|-----------------|
| **VIII. Testing Discipline** | TDD-mandatory (test-first); ≥80% coverage on test-infrastructure packages; every helper has a self-test exercising happy path + cleanup safety; concurrency tests under `-race`. | The implementation order is locked test-first per the chunk contract's Prompt 4 (TASKS) directive: a self-test task precedes every helper-implementation task. The self-test suite covers `NewTestVault` round-trip + cleanup safety + parallel-subtest isolation, `NewTestKeys` determinism (two invocations same bytes; cross-subtest same bytes), `SentinelSecret` format stability + uniqueness, `AssertSentinelAbsent` positive + negative cases + empty-haystack edge case, `DiscordStub` queue exhaustion + `ApproveAll` fallback + queue-then-`ApproveAll` composition (clarification 1) + unexpected-call failure (clarification 2 / FR-018a) + thread-safety (100 goroutines under `-race`). Coverage target: ≥80% on `internal/testutil/`, verified by `go test -cover` in the IMPLEMENT-phase release-step list. ✅ |
| **IX. Idiomatic Go Discipline** | No `init()`; no mutable package-level globals (single, narrow exception documented inline); accept-context-as-first-param for I/O; errors wrapped with `%w`; `errors.Is`/`errors.As` for comparison; no `vendor/`; CGO disabled; accept interfaces, return concrete types; consumer-defined interfaces. | No `init()` is present. ONE package-level mutable state exists: a `sync.Once` + cached `[]byte` for the deterministic master seed (memoisation across the test process — Argon2id costs ~1.5 s and a per-call invocation would balloon CI runtime by minutes for the full suite). The exception is justified inline with a `//nolint:gochecknoglobals` comment citing the cost-of-recomputation rationale and the single-set-monotonic-read access pattern; the cache is observable only through `NewTestKeys` (a function call), never written outside the `sync.Once` body, never destroyed (a 64-byte deterministic test-only seed is not secret material). The `Approver` interface is defined here because the *consumer* of the abstraction is downstream test code that wants to substitute the stub for the (yet-to-exist) production approver — exactly the consumer-side discipline Constitution IX prescribes. The implementation type `DiscordStub` is exported (it carries observable state — `ApproveAll`, `Calls()`, `Enqueue` — that test authors need to manipulate); the `Approver` interface is the narrow seam the code under test sees. `errors.Is(err, ErrUnexpectedCall)` is the comparison primitive; no string compares. No `vendor/` directory introduced. CGO disabled. ✅ |
| **X. Observability & Redaction** | The package supplies the sentinel infrastructure for downstream redaction tests but MUST NOT itself log secret material; it has no `log/slog` usage, and its error messages identify failure mode + the request's identifying attributes (host, scopes, session type, TTL/uses) — never a secret value. | The package emits no log records (no `*slog.Logger` is constructed or accepted). Error messages are limited to the single sentinel `ErrUnexpectedCall` and the descriptive `t.Errorf` failure path; both name the request's identifying attributes (already non-secret per Constitution X — the *secret value* is what must be redacted, not the request metadata). The vault key returned by `NewTestVault` is a `*securebytes.SecureBytes`, which already implements `LogValue() slog.Value` returning `slog.StringValue("[redacted]")` — type-driven redaction is inherited by construction. The sentinel helper itself returns a *non-secret* string (`SECRET_SHOULD_NEVER_APPEAR_<n>` is the synthetic marker downstream tests inject; it is not a real secret), so no redaction is required for it. ✅ |
| **I. Zero Files at Rest on Agent Machines** | The test harness MUST NOT be importable from production code: a test-only artefact in a production binary is a file at rest the malware threat model targets. | The package is enforced as test-only via two layers: (a) `golangci-lint`'s `depguard` rule (configured in this chunk's PR — listed in the IMPLEMENT release-step list) blocks any non-`*_test.go` file from importing `github.com/mrz1836/hush/internal/testutil`; (b) the package's own `package_test.go` (or equivalent self-test) walks the repository's `internal/` tree, parses each `.go` file, and fails the test if any non-`*_test.go` file imports the package. Belt-and-braces: a CI lint failure AND a unit-test failure for the same violation. ✅ |

### Other principles (not in scope but checked for non-violation)

- **II (Approval is Human):** out of scope — the Discord stub
  satisfies the test-substitution role only; it does not pretend to
  implement the production approval flow. SDD-11 owns the real
  `Approver`. ✅
- **III (Defense in Depth):** out of scope — no production crypto
  surface; the helpers consume SDD-01 and SDD-03 surfaces, never
  reimplement them. The deterministic test passphrase
  (`hush-test-seed-NEVER-USE-IN-PROD`) is a literal containing the
  substring `NEVER-USE-IN-PROD`, satisfying SC-002. ✅
- **IV–VII:** no supervisor, network, CLI, or wrap-shell surface in
  scope. ✅
- **XI (Native-First, Minimal Dependencies):** no new module
  dependency introduced. The package is a strict superset of stdlib
  + already-locked intra-repo packages. ✅

### Gate result

**PASS** — every principle in scope is satisfied. **One narrow
exception is justified inline** (the `sync.Once`-guarded memoised
seed cache) and is documented in the Complexity Tracking section
below. The Constitution Check is re-evaluated post-design (after
Phase 1) below.

## Project Structure

### Documentation (this feature)

```text
specs/004-testutil-fixtures/
├── plan.md                          # This file (/speckit-plan command output)
├── research.md                      # Phase 0 output (decisions on locked HOW questions)
├── data-model.md                    # Phase 1 output (entities + state)
├── quickstart.md                    # Phase 1 output (consumer integration recipe)
├── contracts/
│   └── testutil-api.md              # Phase 1 output (exported API contract — locks PACKAGE-MAP §internal/testutil)
├── checklists/                      # Pre-existing artifact directory (untouched by /speckit-plan)
├── spec.md                          # WHAT contract (already written by /speckit-specify + /speckit-clarify)
└── tasks.md                         # Phase 2 output (/speckit-tasks command — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/testutil/
├── doc.go                           # Package doc: test-only invariant + helper roster + Constitution I/IX citation
├── keys_fixture.go                  # NewTestKeys (sync.Once-cached deterministic master seed)
├── vault_fixture.go                 # NewTestVault (writes a real HUSH file via internal/vault.Save into t.TempDir())
├── sentinel.go                      # SentinelSecret + AssertSentinelAbsent (Testing Strategy §5)
├── discord_stub.go                  # DiscordStub + Approver interface + Decision + ApprovalCall + ApprovalRequest + ErrUnexpectedCall + NewDiscordStub
├── doc_test.go                      # (optional) repository-wide grep self-test enforcing the no-production-import invariant
├── keys_fixture_test.go             # Determinism (two invocations same bytes; cross-subtest same bytes); passphrase-marker-substring assertion (SC-002); concurrent-invocation safety
├── vault_fixture_test.go            # Round-trip (Save → vault.Load round-trips the supplied secrets); path-containment in t.TempDir(); cleanup zeroes vault key (SecureBytes.Len() == 0); empty-secrets-map; parallel-subtest isolation
├── sentinel_test.go                 # Format stability (literal prefix "SECRET_SHOULD_NEVER_APPEAR_"); uniqueness across indices; positive (absent) + negative (present) cases; empty-haystack; negative-index edge case
└── discord_stub_test.go             # ApproveAll path; queue path; queue-then-ApproveAll composition (clarification 1); unexpected-call failure (clarification 2 / FR-018a) using a sub-`testing.T`; thread-safety (100 goroutines under -race); no-network assertion (no socket dial in any code path)
```

**Structure Decision**: hush is a single Go module
(`github.com/mrz1836/hush`) with a flat `internal/<domain>` layout
defined in `docs/PACKAGE-MAP.md`. SDD-04 introduces a new
`internal/testutil/` package — the first test-only package in the
tree. The five production files match the SDD-04 chunk contract
exactly (no extra file like `helpers.go`, `errors.go`, or
`approver.go`). The package import path is
`github.com/mrz1836/hush/internal/testutil`. Per Constitution I and
the inline `depguard` rule added in this chunk, ONLY `*_test.go`
files inside the project's `internal/` tree may import this package
— no production source file (`cmd/hush/...` or `internal/...` non-
test) may import it, directly or transitively. The single allowed
sub-relationship is `internal/testutil → internal/keys,
internal/vault, internal/vault/securebytes` (the three SDD-01,
SDD-02, SDD-03 producers); no other intra-repo import is permitted.

## Constitution Re-check (post-design)

Re-evaluated after Phase 1 design artifacts (`research.md`,
`data-model.md`, `contracts/testutil-api.md`, `quickstart.md`) were
drafted:

- The Phase 0 research confirms every HOW choice is satisfied by Go
  stdlib + the three already-locked intra-repo packages. **No new
  dependency emerged.** ✅ Principle XI.
- The contract documents the exact 12 exported identifiers
  (`NewTestVault`, `NewTestKeys`, `SentinelSecret`,
  `AssertSentinelAbsent`, `DiscordStub`, `NewDiscordStub`,
  `ApprovalCall`, `Approver`, `Decision`, `DecisionApprove`,
  `DecisionDeny`, `DecisionApproveMute`, `ApprovalRequest`,
  `ErrUnexpectedCall`) — a **strict superset** of the chunk contract's
  eight named identifiers, with the four supplements
  (`Decision`+three values, `ApprovalRequest`, `ErrUnexpectedCall`)
  driven by FR-014/FR-017/FR-018a/edge-case-1 in the spec. ✅
  Principle IX (no leaked internals; no missing sentinel).
- `data-model.md` confirms the package's only mutable
  package-level state is the `sync.Once`-guarded memoised seed cache
  — set-once, monotonic, observable only through `NewTestKeys`. The
  `//nolint:gochecknoglobals` justification is colocated with the
  declaration. ✅ Principle IX (with the single documented exception).
- `quickstart.md` shows callers wiring the public surface — and only
  the public surface — with consumer-defined `Approver` interface
  injection. The recipes confirm the locked-API shape. ✅ Principle IX.
- `data-model.md` enumerates the `DiscordStub`'s lifecycle states
  (live, drained-by-cleanup) and the transitions (`Enqueue`,
  `RequestApproval`, `t.Cleanup` drain). The race-test
  (`TestDiscordStub_Concurrent`) is enumerated in the contract's
  behavioural-guarantee table, run under `-race`. ✅ Principle VIII.
- The contract enumerates the 14 self-test names spread across the
  four `*_test.go` files — every spec FR and every spec SC has at
  least one named test, plus `TestNoProductionImport` (the
  Constitution I belt-and-braces enforcement). ✅ Principle VIII.
- The `depguard` rule update committed in this chunk is the
  lint-enforced half of the test-only invariant; the
  `TestNoProductionImport` self-test is the runtime-enforced half.
  Together they satisfy the spec FR-025 "enforceable by repository
  search and by linter configuration" requirement. ✅ Principle I.

**Gate result (post-design): PASS.** No new violations introduced
by the design phase. Complexity Tracking remains as-documented (one
narrow `sync.Once` exception, justified inline).

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| One package-level mutable variable (`sync.Once`-guarded `[]byte` cache for the deterministic master seed in `keys_fixture.go`) — formally a violation of Principle IX's "no globals" non-negotiable. | Argon2id at the locked SDD-01 cost (`time=4`, `memory=256 MiB`, `threads=4`) takes ~1.5 s per invocation. The downstream test suite invokes `NewTestKeys` (and transitively `NewTestVault`) hundreds of times per `go test ./...` run; recomputing on every call would extend CI by tens of minutes per run, defeating the constitutional gate `magex test:race` is meant to enforce. | A per-test cache via `t.Helper()` + `t.Setenv` is unsafe under `t.Parallel()` (the `*T` is per-test, not per-process). A package-level cache keyed by `sync.Map` adds complexity without removing the global. The `sync.Once` + single `[]byte` is the minimal, set-once, monotonic-read shape that survives concurrent use under `-race`. The cache holds a 64-byte deterministic test-only seed (literal `hush-test-seed-NEVER-USE-IN-PROD`-derived) — it is NOT secret material; the masthead constant `NEVER-USE-IN-PROD` is its own annotation that the cached bytes are safe to live for the process's lifetime. The `//nolint:gochecknoglobals` comment colocated with the declaration cites this rationale. |
