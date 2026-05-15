# Implementation Plan: Supervisor Refill, Refresh, and Grace Cache

**Branch:** `021-supervise-refill-refresh` | **Date:** 2026-05-10 | **Spec:** [spec.md](./spec.md) | **Chunk doc:** [docs/sdd/SDD-21.md](../../docs/sdd/SDD-21.md)
**Input:** Feature specification from `/specs/021-supervise-refill-refresh/spec.md`

## Summary

Extend `package supervise` with three new behaviour files —
`refill.go`, `refresh.go`, `grace.go` — implementing the supervisor's
credential-lifecycle helpers. **Refill** fetches per-scope ECIES-
encrypted secrets from the vault server using the cached supervisor
JWT, decrypts to `*securebytes.SecureBytes`, hands ownership to the
**Grace** cache, and surfaces `ErrJTIUnknown` so the orchestrator
(SDD-23) can transition state. **Refresher** schedules at most one
fire per (window, calendar-day) pair plus an at-most-one T-30
fallback per session, anchored to a configured local-time window
parsed at SDD-18 time. **Grace** is an `enabled`-gated, 4-hour-capped,
lazy-evicting `map[string]*SecureBytes` that holds the last-decrypted
set across child crashes for opt-in restart resilience. No goroutines
beyond the Refresher's own tick loop; no `string(decryptedBytes)`
anywhere; no init-side-effects; the locked exported API of
[docs/sdd/SDD-21.md](../../docs/sdd/SDD-21.md) is honoured exactly,
extended only by the `(*Grace) Evict(name string)` method that
[Clarification 5](./spec.md#clarifications) explicitly added.

The technical approach is laid out in
[research.md](./research.md) (R-001..R-016), the locked struct
shapes in [data-model.md](./data-model.md), the typed mirror in
[contracts/api.go](./contracts/api.go), the black-box behavior
spec in [contracts/observable-behaviors.md](./contracts/observable-behaviors.md),
and the runbook in [quickstart.md](./quickstart.md).

## Technical Context

**Language/Version:** Go (the version pinned in `go.mod`; floor only — see Constitution IX). Stdlib-first per Constitution XI.
**Primary Dependencies:** Standard library (`net/http`, `log/slog`, `crypto/ecdsa`, `sync`, `time`, `errors`, `context`, `encoding/json`, `io`); `github.com/mrz1836/hush/internal/vault/securebytes` (existing); `github.com/mrz1836/hush/internal/transport/ecies` (existing, SDD-09). **Zero new direct dependencies** added by this chunk.
**Storage:** None. The grace cache is in-process `mlock`'d memory only (`*SecureBytes`); no on-disk artifacts. The vault file at the trusted host is owned by `internal/vault/` and is out of scope here. Refill produces ECIES envelope bytes that are zeroed after decrypt.
**Testing:** `go test -race -cover ./internal/supervise/ -run "Refill|Refresh|Grace"` ≥95%; table-driven `TestFunctionName_Scenario` per `.github/tech-conventions/testing-standards.md`; race-clean assertion on the Refresher tick goroutine; marker-byte test for the Constitution X "no string materialization" invariant.
**Target Platform:** macOS (darwin) + Linux (CGO disabled, pure Go release binaries per `.goreleaser.yml`); status-socket and supervisor host platforms only — the chunk has no network-bound platform-specific code.
**Project Type:** Go module (single binary `cmd/hush`, internal packages under `internal/`). The chunk is `internal/supervise` package extension only.
**Performance Goals:** Refill latency dominated by `len(scopes) × (HTTP RTT + ECIES decrypt)`; the chunk imposes no additional per-call bookkeeping >100µs. Refresher tick interval is bounded below by the next-window-start evaluation, never sub-second. Grace.Get is `O(1)` map access under read lock; lazy-evict is `O(1)` under write lock.
**Constraints:** Constitution X — no `string(decryptedBytes)` materialization (the JWT bearer-header path is the single permitted `string(...)` site, scoped to `Snapshot.Token.Use(func(b []byte) {...})` per [research.md R-005](./research.md#r-005)). Constitution IX — no `init()`, no `gochecknoglobals`, every goroutine has owner+ctx-cancel+termination. Constitution VIII — TDD mandatory, ≥95% coverage, race-clean. Constitution IV — TTL hard-capped at 4h; lifecycle integrity preserved (this chunk emits typed errors; SDD-23 drives transitions). Constitution V — operator-visible loud failure via `slog.Warn` on rate-limit drops and refresh fires.
**Scale/Scope:** Per-supervisor instance: 3..10 scope names typical; 1..2 active grace entries typical; one Refresher per supervisor process; one Refiller per supervisor. v0.1.0 has no multi-supervisor coordination — each supervisor is independent and stateless across restarts.

## Constitution Check

*Initial gate (pre-Phase-0):* PASS. *Re-evaluation after Phase 1 design:* PASS. No violations to track.

In-scope principles: **IV, V, VIII, IX, X** (chunk doc § Constitutional principles in scope). XI (dependencies) is verified by "zero new direct deps". I, II, III, VI, VII are not exercised by this chunk.

### Principle IV — Supervisor for daemons; TTL discipline; grace cap

| Requirement | Plan compliance |
|-------------|-----------------|
| Supervisor TTL ≤ `max_supervisor_session_ttl` (default 20h) | Refresher consumes `ttl time.Duration` from caller; the chunk imposes no additional cap, the TTL is enforced upstream by SDD-18 (`MaxRequestedTTL = 24h`) |
| Supervisor sessions are TTL-only (not use-count) | Refresher fires by wall-clock anchor, not request count |
| Child exit MUST NOT cause supervisor exit | This chunk does not orchestrate child lifecycle; Refill is invoked by the orchestrator after a child exit and surfaces typed errors only |
| Supervisor MUST zero secret material after handoff to child, EXCEPT during grace-window cache | Refill hands `*SecureBytes` to Grace.Set (which destroys the prior entry on overwrite) and to the child env builder (out of scope here); the `committed bool` rollback pattern (R-007) destroys all decrypted bytes on any failure |
| Grace window capped at 4h | `NewGrace` applies `min(window, 4*time.Hour)` at construction (GR-1, FR-021-12); GR-5 test asserts an 8h request → 4h effective TTL |
| Lifecycle integrity (child exit never reaches `stopped`) | This chunk never calls `Store.Transition`; the orchestrator (SDD-23) drives transitions via the typed errors documented in [data-model.md § State-machine integration](./data-model.md#7-state-machine-integration-read-only-consumers) |

### Principle V — Staleness visible, failure loud

| Requirement | Plan compliance |
|-------------|-----------------|
| Pluggable validators run BEFORE child injection | Out of scope here; orchestrator runs validators between Refill returning and child env builder consuming |
| Exit code 78 contract | Out of scope (SDD-20); Refill is invoked by the orchestrator after exit-78 disposition |
| Distinct, actionable alerts | Refill returns three error classes (`nil`, `ErrJTIUnknown`-wrapped, other-wrapped); Refresher's WARN log on rate-limit drop names the error class; Grace is silent (no logger field by locked API) |
| Loud failure principle | Refresher logs WARN on every non-nil `refill` callback error; orchestrator escalates to Discord alert via SDD-23 |

### Principle VIII — Testing discipline

| Requirement | Plan compliance |
|-------------|-----------------|
| Table-driven unit tests per `.github/tech-conventions/testing-standards.md` | All 26 tests in [quickstart.md § 4](./quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4) follow `TestFunctionName_Scenario` |
| Pre-commit MUST pass `golangci-lint` + `go test -race` | `magex lint` + `magex test:race` are gate commands in [quickstart.md § 3](./quickstart.md#3-test-command-quickstart) |
| ≥95% coverage on the three new files | SC-021-10; verified by `go test -cover ./internal/supervise/ -run "Refill\|Refresh\|Grace"` |
| AC-10 → required test types | This chunk's tests cover Lifecycle Scenarios 3, 7, 8, 9, 11; mapping in [quickstart.md § 2](./quickstart.md#2-lifecycle-scenario-coverage-map) |
| Race-clean | `TestRefresh_StopsOnCtxCancel` asserts goroutine baseline; `TestGrace_ConcurrentRaceClean` asserts under `-race` |
| TDD-first | /speckit-tasks (Phase 4) generates a task list ordering test-writing tasks BEFORE implementation tasks per the chunk doc Prompt-4 directive |
| Fuzz targets | None mandated for this chunk per [research.md R-013](./research.md#r-013); the JSON 401-body parse path is small and explicit, not a parser surface |

### Principle IX — Idiomatic Go discipline

| Requirement | Plan compliance |
|-------------|-----------------|
| Context propagation: `context.Context` first param of any I/O / cancellable function | `Refiller.Refill(ctx, scopes)`, `Refresher.Run(ctx)` — both first-param ctx |
| Errors wrap with `%w`; sentinel `var Err... = errors.New(...)` | `ErrJTIUnknown`, `ErrBootTimeout` declared as package-level `var Err... = errors.New(...)`; all error returns wrap via `fmt.Errorf("...: %w", ...)` |
| No globals, no `init()` | `errors.New` sentinel-class globals are exempt (sentinel-class read-only) per Constitution IX wording — same exemption SDD-19 uses for its `transitions`/`reasons` maps; otherwise no package-level state |
| Panic policy | `NewRefiller` / `NewRefresher` / `NewGrace` panic on nil dependencies (startup-wiring exemption); every goroutine recovers at top frame (Refresher tick loop has a `defer func() { recover() }()`) |
| Goroutine discipline | One owned goroutine type: Refresher tick loop (R-014). Owner = caller of `Run`. Cancellation = `<-ctx.Done()`. Termination = ctx done OR panic-recover. Documented in [data-model.md § 2](./data-model.md#2-refresher) |
| Interfaces: accept interfaces, return concrete types; consumer-side definition | No new interfaces in the locked exported API. The clock seam is an unexported field `now func() time.Time`, not a published interface — minimal surface (R-004) |
| Package layout | Three new files under `internal/supervise/`; no new public package |
| Modules-only, no vendor | No `go.mod` change |
| CGO disabled | All Go stdlib; ECIES-decrypt + securebytes already pure Go |
| Generics | None used here |

### Principle X — Observability & redaction

| Requirement | Plan compliance |
|-------------|-----------------|
| Structured logging via `log/slog` | All loggers are `*slog.Logger`; no third-party logger |
| Type-driven secret redaction (`SecureBytes.LogValue() → "[redacted]"`) | Existing SDD-02 contract; verified by [B-RR-5](./contracts/observable-behaviors.md#b-rr-5--bearer-token-never-leaks-to-logs-constitution-x) and [B-GR-9](./contracts/observable-behaviors.md#b-gr-9--cached-values-never-become-a-go-string-fr-021-15-constitution-x) |
| No secret values in errors | All Refill error wraps include URL/scope-name/status only — never bytes; tests assert via marker-byte capture |
| Audit log separate from operational log | Refill emits one operational INFO line per call; orchestrator (SDD-23) emits the audit-chain entry. The chunk does not write to the audit log directly (R-012) |
| Discord alert tiers | Out of scope (orchestrator); Refresher's WARN on `refill`-callback error is Principle V loud-failure, NOT a Discord prompt — the prompt itself goes through the `refill` callback |
| Metrics over Unix status socket only | Not in this chunk (SDD-22) |
| **No `string(decryptedBytes)` anywhere in chunk** (FR-021-15, SC-021-8) | Decrypted bytes flow `[]byte → ECIES.Decrypt → *SecureBytes → Grace.Set / child env builder` — never assigned to a Go `string`. Verified by `TestRefill_NeverStringifiesDecryptedBytes` and `TestGrace_NeverRendersValueAsString` (marker-byte capture). The single permitted `string(...)` site is the JWT bearer-header path inside `Snapshot.Token.Use(func(b []byte) { req.Header.Set("Authorization", "Bearer "+string(b)) })`, and applies to **JWT material, not vault secrets** — Constitution X's prohibition is on **secret material** (vault payload), not session tokens. The closure scope is the smallest possible, holds the SecureBytes mutex, and is documented in godoc on `Refiller`. |

### Locked vs implemented exported API

The chunk doc lists exactly **three constructors + four methods + two
sentinels** as the locked exported surface:

```text
type Refiller struct{...}
func NewRefiller(client *http.Client, store *Store, logger *slog.Logger) *Refiller
func (r *Refiller) Refill(ctx context.Context, scopes []string) error
type Refresher struct{...}
func NewRefresher(window string, ttl time.Duration, refill func(context.Context) error, logger *slog.Logger) *Refresher
func (r *Refresher) Run(ctx context.Context) error
type Grace struct{...}
func NewGrace(window time.Duration, enabled bool) *Grace
func (g *Grace) Get(name string) (*securebytes.SecureBytes, bool)
func (g *Grace) Set(name string, value *securebytes.SecureBytes)
var ErrJTIUnknown, ErrBootTimeout
```

[Clarification 5](./spec.md#clarifications) explicitly extended the
locked API by exactly **one** method — `func (g *Grace) Evict(name
string)` — to satisfy FR-021-16 (operator-driven cache eviction
after `hush client refresh`). The plan honours that addition. No
other exported symbols are introduced. The Complexity Tracking table
records this extension.

## Project Structure

### Documentation (this feature)

```text
specs/021-supervise-refill-refresh/
├── plan.md                               # this file
├── spec.md                               # WHAT (5 stories, 27 FRs, 10 SCs)
├── research.md                           # WHY (R-001..R-016)
├── data-model.md                         # locked struct shapes + invariants
├── contracts/
│   ├── api.go                            # typed mirror of Go signatures (review-only)
│   └── observable-behaviors.md           # black-box behavior contract
├── quickstart.md                         # runbook + test commands
├── checklists/                           # filled by /speckit-checklist (later)
└── tasks.md                              # filled by /speckit-tasks (Phase 4)
```

### Source Code (repository root)

The chunk extends an **existing** package; no new directories.

```text
internal/supervise/
├── doc.go                                # existing (SDD-19)
├── state.go                              # existing (SDD-19)
├── state_test.go                         # existing (SDD-19)
├── child.go                              # existing (SDD-20)
├── child_linux.go                        # existing (SDD-20)
├── child_darwin.go                       # existing (SDD-20)
├── child_*_test.go                       # existing (SDD-20)
├── config/                               # existing (SDD-18 sub-package)
│   ├── config.go
│   ├── defaults.go
│   ├── errors.go
│   ├── paths.go
│   ├── validate.go
│   └── *_test.go
│
├── refill.go                             # NEW — Refiller + Refill (this chunk)
├── refresh.go                            # NEW — Refresher + Run + tick loop
├── grace.go                              # NEW — Grace + Get + Set + Evict
├── refill_test.go                        # NEW — 7 mandatory tests
├── refresh_test.go                       # NEW — 9 mandatory tests
├── grace_test.go                         # NEW — 10 mandatory tests
└── helpers_test.go                       # NEW — fakeClock, roundTripFunc, etc.
```

**Structure Decision:** Single Go module, internal package extension.
The chunk adds three behaviour files alongside the SDD-19 (`state.go`)
and SDD-20 (`child.go`) files in the same `package supervise`. No
sub-package is created — the locked exported API explicitly lives at
`github.com/mrz1836/hush/internal/supervise` (mirrors SDD-19's lock
in [docs/PACKAGE-MAP.md:1366](../../docs/PACKAGE-MAP.md)).

### Cross-references

| Resource | Path |
|----------|------|
| Constitution | [.specify/memory/constitution.md](../../.specify/memory/constitution.md) |
| Spec | [spec.md](./spec.md) |
| Phase 0 research | [research.md](./research.md) |
| Phase 1 data model | [data-model.md](./data-model.md) |
| Phase 1 contracts | [contracts/api.go](./contracts/api.go) · [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) |
| Phase 1 quickstart | [quickstart.md](./quickstart.md) |
| Chunk doc | [docs/sdd/SDD-21.md](../../docs/sdd/SDD-21.md) |
| Lifecycle scenarios | [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §3, §7, §8, §9, §11 |
| Package map (target) | [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) `internal/supervise/` |
| Operator tuning | [docs/DAEMONS.md](../../docs/DAEMONS.md) §6 (grace tradeoff) |
| Security posture | [docs/SECURITY.md](../../docs/SECURITY.md) §6 row "Grace-window plaintext cache in supervisor memory" |
| Existing dependency: SecureBytes | [internal/vault/securebytes/securebytes.go](../../internal/vault/securebytes/securebytes.go) |
| Existing dependency: ECIES decrypt | [internal/transport/ecies/ecies.go](../../internal/transport/ecies/ecies.go) §`Decrypt` |
| Existing dependency: state machine | [internal/supervise/state.go](../../internal/supervise/state.go) |
| Existing dependency: refresh-window parser | [internal/supervise/config/validate.go:79](../../internal/supervise/config/validate.go#L79) `validateRefreshWindow` |

## Complexity Tracking

The Constitution Check passes without justified violations. Two
locked-API decisions warrant explicit recording for downstream
auditability:

| Decision | Why Needed | Simpler Alternative Rejected Because |
|----------|------------|-------------------------------------|
| Add `(g *Grace) Evict(name string)` to the locked exported API list | FR-021-16 + Clarification 5 require the orchestrator to invalidate cache entries when `hush client refresh` triggers post-rotation eviction. Without an exported primitive, the orchestrator cannot keep the cache honest after vault-side rotation. | (a) "Reuse `Set(name, nil)`" — would silently destroy on nil value, but the SecureBytes type does not accept nil. Rejected. (b) "Time out via TTL only" — leaves window-of-staleness up to grace.window even when the operator has explicitly invalidated. Rejected. The Clarification is part of the spec, not a plan-side decision. |
| `(*Refiller).attach(grace, priv, serverURL)` package-private wiring method | The locked `NewRefiller` signature accepts only `(client, store, logger)`; the Refiller needs three additional dependencies (Grace handle, ECIES private key, server URL prefix) that are not available at constructor time without breaking the locked signature. | (a) "Inflate `NewRefiller`'s signature" — violates the SDD-21 locked API. (b) "Package-level globals" — violates Constitution IX (`gochecknoglobals`). (c) "Functional options pattern" — same problem as inflating the signature. The package-private method is invoked by the orchestrator (SDD-23, same package), is unexported, mirrors the SDD-19 `setTokenForTest` precedent, and adds zero public surface. |
| Replace `TestGrace_SweeperDestroysExpired` (chunk doc) with `TestGrace_LazyEvictsOnGetAfterTTL` | R-008 final adopts a lazy-evict design (no goroutine in NewGrace, no exported `RunSweeper`) to honour Constitution IX strictly. Same FR-021-13 destruction semantics, different trigger. | (a) "Sweeper goroutine started by NewGrace" — explicitly forbidden by Constitution IX and the chunk doc. (b) "Exported `RunSweeper(ctx)` method" — extends the locked API by one method that lazy-evict makes redundant. (c) "Background goroutine via `time.AfterFunc` per `Set`" — one goroutine per Set call; complicates lifetime on overwrite. Lazy-evict is the minimal-surface choice. |

---

## Phase summary

| Phase | Output | Status |
|-------|--------|--------|
| 0 — Research | [research.md](./research.md) (R-001..R-016) | ✅ complete |
| 1 — Data model | [data-model.md](./data-model.md) | ✅ complete |
| 1 — Contracts | [contracts/api.go](./contracts/api.go) + [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) | ✅ complete |
| 1 — Quickstart | [quickstart.md](./quickstart.md) | ✅ complete |
| 1 — Agent context | CLAUDE.md `<!-- SPECKIT START -->` block updated to point at this plan | ✅ complete |
| 2 — Tasks | tasks.md | ⏭ next: `/speckit-tasks` |
| 5 — Implement | three new files + tests + post-step doc updates | ⏭ later: `/speckit-implement` |

The /speckit-plan command stops here. Phase 2 (`/speckit-tasks`) is
a separate session per the SDD-21 prompt sequence.
