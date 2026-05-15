# Implementation Plan: Pre-Flight Credential Validators (Interface + 5 Builtins)

**Branch:** `026-validators-builtins` | **Date:** 2026-05-13 | **Spec:** [spec.md](./spec.md) | **Chunk doc:** [docs/sdd/SDD-26.md](../../docs/sdd/SDD-26.md)
**Input:** Feature specification from `/specs/026-validators-builtins/spec.md`

## Summary

Add a new package at `internal/supervise/validators` that ships the
`Validator` interface (one method: `Validate(ctx, *SecureBytes) error`)
together with the five built-in implementations locked by SDD-18's
TOML allow-list: `anthropic`, `anthropic-oauth`, `openai`, `google-ai`,
`github`. The supervisor (SDD-24) wires `Deps.Validators[scope].Validate`
per-scope before child start; on `ErrStaleCredential` it emits
`AlertClassValidatorFailure` and transitions to `awaiting-approval`
(Lifecycle Scenario 6, FR-13, AC-10).

Three load-bearing invariants govern every line of the implementation:

1. **Credential never becomes a Go `string`.** Every validator consumes
   the secret exclusively via `securebytes.Use(fn)`; inside that
   callback it copies the borrowed bytes into a freshly-allocated
   `[]byte` exactly long enough to construct the outbound
   `Authorization` header value (`Bearer …`, `x-api-key`,
   `x-goog-api-key`, or `token …` depending on provider), uses that
   buffer for the single `http.Client.Do` call, and **zeroes the
   buffer before the `Use(fn)` callback returns** (FR-007, FR-021-15,
   Constitution X). There is **zero** `string(secret)` / `fmt.Sprintf("%s", secret)`
   / `%v secret` / `%+v secret` in non-test code (SC-005).
2. **Typed-error verdict is mutually exclusive.** `Validate` returns
   exactly one of: `nil` (HTTP 2xx), `ErrStaleCredential` (HTTP 401/403),
   `ErrValidatorTimeout` (request timeout or `context.DeadlineExceeded`),
   `ErrValidatorNetwork` (any 3xx, any non-2xx-non-401/403 status,
   transport failure, `context.Canceled` on a not-yet-issued request,
   or wrapped `securebytes.ErrDestroyed`). `errors.Is` against the
   three sentinels is pairwise distinct (FR-003 / FR-004 / FR-005,
   Spec Clarification 2026-05-13 Q3 + Q6).
3. **Single outbound HTTP request per `Validate` call.** No internal
   retry. Redirect-follow is **disabled per-request** by setting
   `Request.GetBody = nil` and using a per-call `*http.Client` wrapper
   whose `CheckRedirect` returns `http.ErrUseLastResponse`, regardless
   of the operator-supplied `*http.Client`'s `CheckRedirect`
   (FR-019, FR-021, Spec Clarification Q9). Caller-supplied
   `*http.Client.Timeout` overrides the 5-second default (FR-012,
   Clarification Q1).

The technical approach is locked in [research.md](./research.md)
(R-001..R-014); the struct shapes and per-entity invariants in
[data-model.md](./data-model.md); the typed mirror in
[contracts/api.go](./contracts/api.go); the observable behaviors in
[contracts/observable-behaviors.md](./contracts/observable-behaviors.md);
the runbook + mandatory test list in [quickstart.md](./quickstart.md).

## Technical Context

**Language/Version:** Go (version pinned in `go.mod`; floor only — see Constitution IX). Stdlib-first per Constitution XI.
**Primary Dependencies:** Standard library only (`context`, `errors`, `fmt`, `io`, `log/slog`, `net/http`, `net/http/httptest` for tests, `sync`, `time`); plus a compile-time reference to `github.com/mrz1836/hush/internal/vault/securebytes` for `*SecureBytes` and `Use(fn)` consumption (FR-006). **Zero new direct dependencies** added by this chunk — verified by `TestPackage_ZeroNewDependencies`.
**Storage:** None. Validators are stateless functions of `(credential, single upstream response) → verdict`. No on-disk artifacts; no caching of upstream responses (FR-019 single-shot).
**Testing:** `go test -race -cover ./internal/supervise/validators/` ≥ 90% (chunk-doc target). Table-driven `TestFunctionName_Scenario` per `.github/tech-conventions/testing-standards.md`. Every test drives validators against `net/http/httptest.Server` instances; the suite passes with the host network interface administratively disabled (SC-004). Race-clean via `TestValidator_<Name>_Concurrent` per provider.
**Target Platform:** macOS (darwin) + Linux (CGO disabled per `.goreleaser.yml`); the package has no platform-specific code.
**Project Type:** Go module (single binary `cmd/hush`, internal packages under `internal/`). New package at `internal/supervise/validators/` — parallel sibling to `internal/supervise/config/` (SDD-18) and `internal/supervise/watchdog/` (SDD-27).
**Performance Goals:** SC-001 caps total time from "supervisor fetched a stale credential" to "supervisor refused to start child" at **6 seconds** (5-second validator timeout + state-transition overhead). SC-008 caps an already-cancelled-context `Validate` at **50 ms** wall-clock with **zero** outbound HTTP requests (handler-invocation counter on the fixture remains at 0).
**Constraints:** Constitution V — every failure outcome surfaces a WARN log record (FR-020); steady-state success is DEBUG-only. Constitution VIII — TDD-first, 90% coverage, race-clean, sentinel-leak fuzz-style assertion. Constitution IX — every function that does I/O takes `context.Context` as first parameter; sentinel errors via `var Err… = errors.New(…)`; zero `init()`; zero mutable package-level state; goroutines are forbidden (validators are synchronous). Constitution X — zero `string(secret)` in non-test code (SC-005); `*SecureBytes` is the only credential surface; no `*http.Request` / `*http.Header` is ever passed to a logger / error formatter / byte sink (FR-008 / SC-005). Constitution XI — zero new direct `go.mod` deps.
**Scale/Scope:** Per-supervisor: at most one `Validator` per scope (typical: 1–3 scopes per supervisor TOML); each `Validator` is concurrency-safe (FR-017) and may be invoked from multiple goroutines simultaneously; one validator instance per provider name in the registry; five recognised names total (FR-010, SC-007).

## Constitution Check

*Initial gate (pre-Phase-0):* **PASS**. *Re-evaluation after Phase 1 design:* **PASS**. Two locked-API extensions tracked in [§ Complexity Tracking](#complexity-tracking) as justified additive changes; no Constitution principle is violated.

In-scope principles per chunk doc + user prompt: **V, VIII, IX, X**.
**XI** is verified by `TestPackage_ZeroNewDependencies`;
**I, II, III, IV, VI, VII** are not exercised here.

### Principle V — Staleness visible, failure loud

| Requirement | Plan compliance |
|-------------|-----------------|
| Pluggable client-side validators MUST run on the supervisor (not the vault server) | This package is the SDD-18 / FR-13 deliverable. It imports zero vault-server packages and is consumed by `internal/supervise/lifecycle_*` only. The chunk's `docs/DAEMONS.md` §5 row pins this. |
| Validators MUST exist for: anthropic, anthropic-oauth, openai, google-ai, github | Five files (`anthropic.go`, `anthropic_oauth.go`, `openai.go`, `google_ai.go`, `github.go`), five `New*` constructors, five registry entries. SC-007 verifies the registry set is exactly that. |
| Exit code 78 is the **child→supervisor** stale-credential contract | Out of scope here (SDD-20 / SDD-24 own exit-78 dispatch). Validators are the **supervisor-side** stale-credential gate that fires BEFORE the child is spawned. |
| Distinct, actionable alerts in Discord | This chunk emits the typed `ErrStaleCredential`; SDD-24 routes it to `AlertClassValidatorFailure` → `[STALE] Validator Failure`. Validators themselves do NOT touch Discord. |
| **Loud failures, no silent drops** | Every failure path (`stale` / `timeout` / `network`) emits exactly one WARN `slog` record (FR-020). The success path emits exactly one DEBUG record. Zero failure path is silent. |
| Log-pattern auth-failure tailing is alert-only (no state authority) | Out of scope (SDD-27). Mentioned here because validators are the **supervisor-state-changing** stale signal, complementary to the watchdog's alert-only signal. |

### Principle VIII — Testing discipline

| Requirement | Plan compliance |
|-------------|-----------------|
| Table-driven unit tests per `.github/tech-conventions/testing-standards.md` (`TestFunctionName_Scenario`, PascalCase) | All mandatory tests are named per the standard in [quickstart.md § 4](./quickstart.md). Five providers × seven scenarios + shared/registry tests + sentinel-leak tests = the locked test inventory. |
| Pre-commit MUST pass `golangci-lint` + `go test -race` | `magex format:fix && magex lint && magex test:race` are the gate commands in [quickstart.md § 3](./quickstart.md). |
| ≥ 90% coverage (chunk-doc target; "High" band in Constitution VIII test-priority table) | Verified by `go test -cover ./internal/supervise/validators/` ≥ 90.0%. The shared `doRequest` helper is hit by every per-provider test; the registry is hit by `TestRegistry_*`; the per-provider files are tiny adapters around `doRequest`. The 90% bar is met without contrivance. |
| AC-10 → required test types (unit + integration) | This chunk is **unit-only** at the package boundary (httptest is in-process). The /speckit-implement Prompt 5 step 6 appends the new test file paths to the AC-10 row in `docs/AC-MATRIX.md`. |
| Race-clean | Per-provider `TestValidator_<Name>_Concurrent` spawns ≥ 4 goroutines invoking `Validate` against an httptest fixture simultaneously; the test runs under `-race`. |
| TDD-first | /speckit-tasks (Phase 4) generates tasks ordering each test-writing task BEFORE the corresponding implementation task per the chunk-doc Prompt-4 directive. |
| Sentinel-leak fuzz-style assertion (FR-015 / SC-006) | Per-provider `TestValidator_<Name>_NoLeakOnError` wraps `SECRET_SHOULD_NEVER_APPEAR_26` in `*SecureBytes`, triggers the 401 path, and asserts the sentinel is absent from `err.Error()` AND every wrapped error's `Error()` AND every captured `slog.Record` at every level. |
| Fuzz targets | None mandated for this chunk (the validator does not parse untrusted bytes; the credential is opaque and the upstream response is consumed only for its status code). The package's input surface is `*SecureBytes` (whose fuzz target lives in SDD-02) and `*http.Response` (whose parsing is stdlib's job). |

### Principle IX — Idiomatic Go discipline

| Requirement | Plan compliance |
|-------------|-----------------|
| Context propagation: `context.Context` is first parameter of any I/O / cancellable function | `Validate(ctx context.Context, secret *securebytes.SecureBytes) error` — first-param ctx. The internal `doRequest(ctx, …)` helper is also ctx-first. Zero ctx-in-struct-field anywhere. |
| Errors wrap with `%w`; compare with `errors.Is`; sentinels via `var Err… = errors.New(…)` | Three exported sentinels: `ErrStaleCredential`, `ErrValidatorTimeout`, `ErrValidatorNetwork`. All return paths wrap the precise cause via `fmt.Errorf("validator: <provider>: <class>: %w", ErrXxx)` (and additionally wrap the precise transport error via `%w` chain so `errors.Is(err, context.DeadlineExceeded)` etc. remain inspectable downstream). |
| No globals, no `init()` | Three `var Err… = errors.New(…)` are sentinel-class read-only globals (Constitution IX exemption per SDD-21 precedent). Zero `init()`. Zero mutable package-level state. Five unexported `const xxxValidatorName = "..."` are compile-time constants — see Complexity Tracking entry #2 below. |
| Panic policy | The package returns errors for every failure mode. No `panic` in the package. No `recover` is needed (the package owns no goroutines). |
| Goroutine discipline | The package spawns **zero** goroutines. `Validate` is synchronous; concurrency is the caller's. (Stdlib `net/http` spawns its own transport goroutines, but those are stdlib's contract — not this package's owners.) |
| Interfaces: accept interfaces, return concrete types; consumer-side definition | The package defines exactly one exported interface (`Validator`) — required by the chunk doc as the producer surface (the supervisor consumes it). It returns `*Registry` (concrete) from `NewRegistry`. Each `New<Provider>` returns the `Validator` interface (per the locked API in the chunk doc) — see Complexity Tracking entry #1. |
| Package layout | New package at `internal/supervise/validators/` parallel to `internal/supervise/config/` (SDD-18) and `internal/supervise/watchdog/` (SDD-27). Public surface area remains `cmd/hush` only. |
| Modules-only, no vendor | No `go.mod` change. |
| CGO disabled | All stdlib; pure Go. |
| Generics | None used. |

### Principle X — Observability & redaction

| Requirement | Plan compliance |
|-------------|-----------------|
| Structured logging via `log/slog` | All log emission via `logger.LogAttrs(ctx, slog.LevelDebug \| slog.LevelWarn, …)`. No third-party logger. |
| Type-driven secret redaction (`SecureBytes.LogValue() → "[redacted]"`) | The package consumes `*SecureBytes` exclusively via `Use(fn)`; the value is never threaded into a `slog.Attr` even by accident — the package's WARN/DEBUG attrs are an explicit allow-list (validator name string, outcome class string, optional HTTP status int). See FR-020 + data-model § "Log record schema". |
| **No secret values in errors** (FR-009) | Every wrapped error is constructed with literal format strings + the sentinel + the underlying transport-error chain. The credential byte slice and the constructed `Authorization` header value never enter an `errors.New` / `fmt.Errorf` argument list. SC-006 sentinel-leak test asserts this per provider. |
| Audit log separate from operational log | This chunk writes ONLY to the operational logger. The audit row for "validator ran and verdict was X" is SDD-24's responsibility (Spec Assumption row 5). Validators have **zero** audit responsibility. |
| Discord alert tiers | Validators emit WARN at the moment of failure (operator-visible). The Discord routing (`AlertClassValidatorFailure` → `[STALE] Validator Failure` DM) is SDD-24's responsibility. |
| Metrics over Unix status socket only | Not in this chunk. |
| **No `string(secret)` anywhere** (FR-006 / SC-005 / Constitution X scope) | Verified by source-grep in `TestPackage_NoStringConversionsOfSecret` (a Go-source-scanning unit test that loads every non-`_test.go` file and asserts the patterns from SC-005 produce zero matches). |
| **`Authorization` header never logged / formatted / sunk** (FR-008 / User Story 4) | The `Authorization` header byte slice is constructed inside the `Use(fn)` scope, used exactly once for `req.Header.Set` (after which Go's `Header` map owns its own copy — see R-008 for the unavoidable string copy and its zeroing strategy), then the local builder slice is zeroed. No code path passes `*http.Request` / `*http.Header` / the credential-derived byte slice to a logger / error formatter / byte sink. |
| **HTTP redirects classified as `ErrValidatorNetwork`** (FR-021, Spec Clarification Q9) | Per-request `CheckRedirect` returning `http.ErrUseLastResponse` is applied via an internal `*http.Client` wrapper for every `doRequest` call; the resulting 3xx response is mapped to `ErrValidatorNetwork` by the same status-code switch that handles 5xx / 429. |

### Principle XI — Native-First, Minimal Dependencies

| Requirement | Plan compliance |
|-------------|-----------------|
| Stdlib-first | Imports: `context`, `errors`, `fmt`, `io`, `log/slog`, `net/http`, `sync` (registry mutex / construction), `time` (timeout) — all stdlib. Plus `github.com/mrz1836/hush/internal/vault/securebytes` (already in repo scope per SDD-02). Tests add `net/http/httptest`, `testing`, `strings`, `bytes`, `sync/atomic`. Zero third-party direct deps. |
| Every NEW direct dep requires justification | No new direct deps in this chunk (FR-018, verified by `TestPackage_ZeroNewDependencies`). |
| `govulncheck` clean | No new deps to vulncheck. |
| `gitleaks` zero findings | The sentinel-leak tests for the credential value in error chains and log records also serve as a redundant gitleaks-style assertion. Production endpoint constants (URLs) are committed as plain text — not secrets. |

### Locked vs implemented exported API

The chunk doc lists exactly **one interface + one struct + two registry funcs + one per-provider func × 5 + three sentinels** as the locked exported surface:

```go
type Validator interface {
    Validate(ctx context.Context, secret *securebytes.SecureBytes) error
}
type Registry struct { ... }
func NewRegistry(httpClient *http.Client) *Registry
func (r *Registry) Get(name string) (Validator, bool)
func NewAnthropic(httpClient *http.Client) Validator
func NewAnthropicOAuth(httpClient *http.Client) Validator
func NewOpenAI(httpClient *http.Client) Validator
func NewGoogleAI(httpClient *http.Client) Validator
func NewGitHub(httpClient *http.Client) Validator
var ErrStaleCredential, ErrValidatorTimeout, ErrValidatorNetwork
```

This plan honours that surface **with two justified extensions**
(Complexity Tracking entries #1–#2 below). No other exported
symbols are introduced.

The three sentinel errors (`ErrStaleCredential`,
`ErrValidatorTimeout`, `ErrValidatorNetwork`) are sentinel-class
read-only globals required by FR-002 and by Constitution IX
("declare sentinel errors as exported package-level
`var Err… = errors.New(…)`"). They are part of the chunk-doc
"Exported API to lock" list.

## Project Structure

### Documentation (this feature)

```text
specs/026-validators-builtins/
├── plan.md                               # this file
├── spec.md                               # WHAT (6 stories, 21 FRs, 8 SCs, 9 clarifications)
├── research.md                           # WHY (R-001..R-014)
├── data-model.md                         # locked struct shapes + per-entity invariants
├── contracts/
│   ├── api.go                            # typed mirror of Go signatures (review-only)
│   └── observable-behaviors.md           # black-box behavior contracts (B-V-1..B-V-N)
├── quickstart.md                         # runbook + mandatory test list
├── checklists/                           # filled by /speckit-checklist (optional)
└── tasks.md                              # filled by /speckit-tasks (Phase 4)
```

### Source Code (repository root)

The chunk creates a **new sibling package** at
`internal/supervise/validators`, parallel to
`internal/supervise/config` (SDD-18) and
`internal/supervise/watchdog` (SDD-27).

```text
internal/supervise/
├── doc.go                                # existing (SDD-19)
├── state.go                              # existing (SDD-19)
├── child.go                              # existing (SDD-20)
├── lifecycle_*.go                        # existing (SDD-24) — owns Deps.Validators wiring
├── lifecycle_interfaces.go               # existing (SDD-24) — owns consumer-side `type Validator interface { Validate(ctx, scope, secret) }`
├── refill.go                             # existing (SDD-21)
├── refresh.go                            # existing (SDD-21)
├── grace.go                              # existing (SDD-21)
├── pidfile.go                            # existing (SDD-22)
├── socket*.go                            # existing (SDD-22)
├── config/                               # existing (SDD-18 sub-package)
├── watchdog/                             # existing (SDD-27 sub-package)
│
└── validators/                           # NEW package (this chunk)
    ├── validators.go                     # NEW — Validator interface + Registry + 3 sentinels + shared doRequest helper
    ├── anthropic.go                      # NEW — NewAnthropic(*http.Client) Validator
    ├── anthropic_oauth.go                # NEW — NewAnthropicOAuth(*http.Client) Validator
    ├── openai.go                         # NEW — NewOpenAI(*http.Client) Validator
    ├── google_ai.go                      # NEW — NewGoogleAI(*http.Client) Validator
    ├── github.go                         # NEW — NewGitHub(*http.Client) Validator
    ├── validators_test.go                # NEW — Registry tests + shared-helper tests + package-level invariant tests
    ├── anthropic_test.go                 # NEW — 7 named tests (happy + 401 + 403 + 5xx + timeout + network + sentinel-leak + concurrent)
    ├── anthropic_oauth_test.go           # NEW — same shape, OAuth bearer variant
    ├── openai_test.go                    # NEW — same shape
    ├── google_ai_test.go                 # NEW — same shape
    └── github_test.go                    # NEW — same shape
```

**Structure Decision:** Single Go module, new internal package
`internal/supervise/validators` at import path
`github.com/mrz1836/hush/internal/supervise/validators`. Sibling to
`internal/supervise/config` and `internal/supervise/watchdog`.
Rationale: SDD-24's `internal/supervise/lifecycle_interfaces.go`
already declares the **consumer-side** interface
`type Validator interface { Validate(ctx, scope, secret) error }`
(three-arg signature). The chunk doc names the SDD-26 **producer-side**
interface `Validator` with a two-arg `Validate(ctx, secret) error`
signature. Placing both interfaces in the same package would be a
name collision and would also lock the chunk's two-arg surface into
SDD-24's three-arg shape — neither is acceptable. The sibling package
keeps both identifiers verbatim: callers refer to
`supervise.Validator` (consumer-side, three-arg) for the orchestrator
seam and `validators.Validator` (producer-side, two-arg) for the
package surface. The supervisor's `Deps.Validators` adapter at
SDD-24 thinly closes over the scope name and forwards to the
validators package's two-arg method — see [research.md R-002](./research.md#r-002).

### Cross-references

| Resource | Path |
|----------|------|
| Constitution | [.specify/memory/constitution.md](../../.specify/memory/constitution.md) |
| Spec | [spec.md](./spec.md) |
| Phase 0 research | [research.md](./research.md) |
| Phase 1 data model | [data-model.md](./data-model.md) |
| Phase 1 contracts | [contracts/api.go](./contracts/api.go) · [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) |
| Phase 1 quickstart | [quickstart.md](./quickstart.md) |
| Chunk doc | [docs/sdd/SDD-26.md](../../docs/sdd/SDD-26.md) |
| Lifecycle scenarios | [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §Scenario 6 |
| SPEC FR-13 | [docs/SPEC.md](../../docs/SPEC.md#fr-13--pluggable-credential-validators) |
| Daemon author guide §5 | [docs/DAEMONS.md](../../docs/DAEMONS.md#5-authoring-credential-validators) |
| Package map (target) | [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) `internal/supervise/` |
| SecureBytes contract | [internal/vault/securebytes/securebytes.go](../../internal/vault/securebytes/securebytes.go) |
| SDD-24 consumer-side Validator interface | [internal/supervise/lifecycle_interfaces.go](../../internal/supervise/lifecycle_interfaces.go) (three-arg `Validate(ctx, scope, secret)`) |
| AlertClassValidatorFailure | [internal/supervise/lifecycle_interfaces.go](../../internal/supervise/lifecycle_interfaces.go) |

## Complexity Tracking

The Constitution Check passes without principle-level violations.
Two locked-API extensions warrant explicit recording so the
downstream review can audit them quickly:

| Extension | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|--------------------------------------|
| **Package `internal/supervise/validators` (vs the chunk-doc's "Package: internal/supervise")** | SDD-24 already declares a **consumer-side** `type Validator interface { Validate(ctx, scope, secret) error }` at `internal/supervise/lifecycle_interfaces.go`, locked. The chunk doc names this chunk's **producer-side** interface `Validator` with a different two-arg signature (`Validate(ctx, secret) error`). Two `Validator` interfaces with different method sets cannot coexist in the same package, and changing either one breaks a locked surface. A sibling package preserves both identifiers verbatim; mirrors the SDD-18 `internal/supervise/config` and SDD-27 `internal/supervise/watchdog` precedent. | (a) **Rename the SDD-24 interface to `ScopeValidator`.** Touches a merged-and-locked surface; cascades into every downstream `Deps.Validators` consumer (the lifecycle file, the SDD-23 CLI wiring, the SDD-25 lifecycle-harness scenario tests). SDD-26 anti-contracts forbid altering any locked SDD-19..25 surface. Rejected. (b) **Rename the SDD-26 interface to `Probe` / `Checker`.** Contradicts the chunk-doc API list verbatim and the spec's "Validator" key-entity row used throughout User Stories 1–6. Rejected. (c) **Drop the producer-side interface and have each `New<Provider>` return a concrete unexported struct pointer cast to the consumer-side interface.** Loses the per-provider strong typing in tests (the chunk doc lists `Validator` as locked; tests in `quickstart.md § 4` declare `var v validators.Validator = validators.NewOpenAI(nil)`). Rejected. |
| **Per-provider unexported `nameXxx` constants + per-provider validator structs** | The five `New<Provider>` constructors share the same internal helper (`doRequest`) but differ in: (1) endpoint URL (R-005..R-009), (2) Authorization-header builder (header name + value prefix per R-007), (3) validator-name string used in the FR-020 log record. Encoding each provider as a tiny unexported struct (`anthropicValidator`, `openaiValidator`, …) with three immutable fields (`url`, `name`, `headerBuilder`) keeps each per-provider file under ~40 lines, isolates the per-provider knowledge in one place, and gives the test files a clear black-box surface to assert against. The alternative — a single struct with five `switch name {}` branches — buries per-provider knowledge in `validators.go` and forces every test to construct the switch. | (a) **Single struct + switch on a name field.** Concentrates all per-provider knowledge in the shared file, defeating the chunk-doc's "one provider per file" decomposition; turns the per-provider test files into thin wrappers around a constructor call; obscures the per-provider Authorization-header semantics that differ across the five providers (Bearer vs x-api-key vs x-goog-api-key vs token). Rejected. (b) **Inline the entire provider implementation into each `New<Provider>`.** Duplicates the ~30 lines of HTTP-machinery glue across five files; defeats Constitution IX's interface-discipline by putting the shared logic at the leaf instead of the root. Rejected. (c) **Closure-only**: `New<Provider>` returns a `validatorFunc` type-alias of `func(ctx, secret) error`. Loses the ability to attach the `validator.name` field that the FR-020 log record requires; the helper has to thread it through every call site as a parameter, growing the helper signature. Rejected for shape-fidelity. |

The "Exported API to lock" list in [docs/sdd/SDD-26.md](../../docs/sdd/SDD-26.md) records the Validator interface, the Registry, NewRegistry, Get, the five `New<Provider>` constructors, and the three sentinel errors. The plan implements those verbatim plus the two extensions above, yielding **1 exported interface + 1 exported struct + 7 exported funcs/methods + 3 exported sentinels** = the package's complete public surface. The /speckit-implement Prompt 5 step 5 records this set verbatim under "Exported API — locked at SDD-26" in `docs/PACKAGE-MAP.md`.

---

## Phase summary

| Phase | Output | Status |
|-------|--------|--------|
| 0 — Research | [research.md](./research.md) (R-001..R-014) | ✅ complete |
| 1 — Data model | [data-model.md](./data-model.md) (V-1..V-N) | ✅ complete |
| 1 — Contracts | [contracts/api.go](./contracts/api.go) + [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) (B-V-1..B-V-N) | ✅ complete |
| 1 — Quickstart | [quickstart.md](./quickstart.md) (mandatory tests) | ✅ complete |
| 1 — Agent context | [CLAUDE.md](../../CLAUDE.md) `<!-- SPECKIT START -->` block updated to point at this plan | ✅ complete |
| 2 — Tasks | tasks.md | ⏭ next: `/speckit-tasks` |
| 5 — Implement | `internal/supervise/validators/*.go` + post-step doc updates | ⏭ later: `/speckit-implement` |

The /speckit-plan command stops here. Phase 2 (`/speckit-tasks`) is a separate session per the SDD-26 prompt sequence.
