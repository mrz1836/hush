# Implementation Plan: Log-Pattern Watchdog (alert-only)

**Branch:** `027-watchdog` | **Date:** 2026-05-13 | **Spec:** [spec.md](./spec.md) | **Chunk doc:** [docs/sdd/SDD-27.md](../../docs/sdd/SDD-27.md)
**Input:** Feature specification from `/specs/027-watchdog/spec.md`

## Summary

Add a new sub-package at `internal/supervise/watchdog` implementing a
single-instance, single-run regex-pattern engine that tails child
stdout/stderr (via the SDD-20 line plumbing) and emits a typed
`Event` on every non-suppressed match. Every facet of the design is
governed by one rule: **the watchdog has zero authority over the
supervisor state machine** (spec FR-003, Constitution V). Its only
side effects are typed `Event` emission on a caller-owned channel
and structured `log/slog` WARN/INFO entries — never a state
transition, never a child signal/restart, never a session-claim or
refresh call. The downstream alert router (SDD-28) consumes the
emitted Events; this chunk stops at the channel.

Internally the design hangs together as follows: producers (the
SDD-20 stderr+stdout tail goroutines) call `Ingest(line []byte)`
concurrently; Ingest defensively copies the line and non-blocking-
sends it to an internal buffered channel of capacity 512. A single
matcher goroutine spawned by `Run(ctx)` drains the channel,
evaluates every pattern, and emits `Event`s on a non-blocking send
to the caller-provided alerts channel. Per-pattern token buckets
(capacity 1, refill interval = `Pattern.RateLimit`, derived from
`config.Watchdog.MaxAlertsPerHour`) suppress excess matches; each
suppression emits a WARN naming the pattern (loud-failure per
Constitution V) and excluding the matched line content
(Clarification Q2). Queue-full Ingest drops are episode-coalesced
into ONE WARN per episode (Clarification Q4); alert-output-
saturated drops emit ONE WARN per drop (Research R-010). On
`<-ctx.Done()`, Run drops pending lines, INFO-logs the count, and
returns within SC-004's 250 ms budget.

The chunk-doc-locked API
(`Pattern`, `Event`, `Watchdog`, `NewWatchdog`, `Ingest`, `Run`) is
honoured at the sub-package path, with three justified extensions
recorded in Complexity Tracking: (1) sub-package location avoids
the name collision with the SDD-24 `supervise.Watchdog` interface;
(2) `NewWatchdog` returns `(*Watchdog, error)` per spec FR-007a;
(3) one additive `OnStderrLine` adapter method satisfies the
SDD-24 interface so callers wire `*Watchdog` directly into
`Deps.Watchdog`. The technical approach is locked in
[research.md](./research.md) (R-001..R-016); the struct shapes and
20 invariants in [data-model.md](./data-model.md); the typed
mirror in [contracts/api.go](./contracts/api.go); the 25 black-box
behaviors in
[contracts/observable-behaviors.md](./contracts/observable-behaviors.md);
the runbook + 24-test list in [quickstart.md](./quickstart.md).

## Technical Context

**Language/Version:** Go (the version pinned in `go.mod`; floor only — see Constitution IX). Stdlib-first per Constitution XI.
**Primary Dependencies:** Standard library only (`regexp`, `log/slog`, `context`, `time`, `sync`, `sync/atomic`, `errors`); plus a compile-time reference to `github.com/mrz1836/hush/internal/supervise` for the `Watchdog` interface guard ([research.md R-003](./research.md#r-003--adapter-method-watchdog-onstderrlinectx-line-beyond-chunk-doc-api)). **Zero new direct dependencies** added by this chunk — verified by `TestWatchdog_ZeroNewDependencies` (B-W-19).
**Storage:** None. Per-pattern token buckets and drop-episode bookkeeping live entirely in process memory (spec FR-015). No on-disk artifacts, no audit-log writes from this chunk (Clarification Q1 — audit row is SDD-28's responsibility).
**Testing:** `go test -race -cover ./internal/supervise/watchdog/` ≥90% (chunk-doc target); 24 named tests per [quickstart.md § 4](./quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4); table-driven `TestFunctionName_Scenario` per `.github/tech-conventions/testing-standards.md`; race-clean assertion on the matcher goroutine; sentinel-byte test for the Clarification Q2 "no line content in WARN" invariant.
**Target Platform:** macOS (darwin) + Linux (CGO disabled, pure Go release binaries per `.goreleaser.yml`); the chunk has no platform-specific code.
**Project Type:** Go module (single binary `cmd/hush`, internal packages under `internal/`). New sub-package at `internal/supervise/watchdog/`.
**Performance Goals:** Per-line matcher cost is `O(K)` for K patterns; the per-line evaluation invokes only `Regex.Match([]byte)` (no recompile, FR-008 / SC-006). SC-001 sets a 100 ms emit-latency budget per match under sequential load; SC-002 sets a "≥1,000 matching lines / sec, exactly one alert emitted" cap proof; SC-004 sets a 250 ms run-loop-cancel budget.
**Constraints:** Constitution V — every suppressed match (rate-limit, queue-full episode, alert-output saturation) MUST surface a WARN; no silent drop. Constitution IX — one matcher goroutine with owner = Run caller, ctx-cancel termination, panic-recover at the top frame; zero `init()`; zero package-level mutable state. Constitution X — no `*SecureBytes` import; the watchdog handles operator log content, not vault material; the single `string(...)` site is `string(line)` for `Event.Line` construction (non-secret). Constitution XI — zero new direct go.mod dependencies.
**Scale/Scope:** Per-supervisor: 0..10 configured patterns typical; one watchdog instance per supervisor process; one matcher goroutine alive between Run start and Run return; internal channel capacity 512 lines (~2 MiB worst-case at 4 KiB line cap).

## Constitution Check

*Initial gate (pre-Phase-0):* PASS. *Re-evaluation after Phase 1 design:* PASS. Three locked-API extensions tracked in [§ Complexity Tracking](#complexity-tracking) as justified additive changes; no Constitution principle is violated.

In-scope principles per chunk doc + user prompt: **V, VIII, IX, X**.
**XI** is verified by `TestWatchdog_ZeroNewDependencies` (B-W-19);
**IV** is non-applicable (no TTL / grace-window code in this chunk);
**I, II, III, VI, VII** are not exercised here.

### Principle V — Staleness visible, failure loud

| Requirement | Plan compliance |
|-------------|-----------------|
| Pluggable client-side validators | Out of scope (SDD-26). The watchdog is the **third, alert-only** stale-credential signal, complementary to validators and exit-78. |
| Distinct, actionable alerts | The watchdog emits a single typed `Event` per non-suppressed match; SDD-28 maps it to `AlertClassLogPatternMatch` → `[STALE] Log Pattern Match` Discord alert (`docs/LIFECYCLE-SCENARIOS.md` Scenario 15). |
| Log-pattern auth-failure tailing is **alert-only** | Spec FR-003 + B-W-17 (`TestWatchdog_NeverTransitionsState`) prove the watchdog has no control-plane authority. The watchdog's source MUST NOT name `Store`, `Refiller`, `Refresher`, `Grace`, `Lifecycle` (W-8). |
| Distinct loud failures (no silent drops) | Three WARN emission sites cover every loss-of-information path: (1) rate-limit suppression per match (FR-005/006), (2) queue-full Ingest drops per episode (FR-010a Q4), (3) alert-output saturation drops per drop (FR-011 / R-010). Cancellation-time pending-line drops emit an INFO with count (R-007). |
| Stale alerts visible to operator via Discord | This chunk emits the typed Event; SDD-28 routes it. |

### Principle VIII — Testing discipline

| Requirement | Plan compliance |
|-------------|-----------------|
| Table-driven unit tests per `.github/tech-conventions/testing-standards.md` | 24 tests in [quickstart.md § 4](./quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4) all named `TestWatchdog_Scenario`. |
| Pre-commit MUST pass `golangci-lint` + `go test -race` | `magex format:fix && magex lint && magex test:race` are the gate commands in [quickstart.md § 3](./quickstart.md#3-test-commands). |
| ≥90% coverage on the new sub-package (chunk-doc target) | SC-021-style coverage proof; verified by `go test -coverprofile ... ./internal/supervise/watchdog/` ≥ 90.0%. |
| AC-10 → required test types | This chunk's tests cover Lifecycle Scenario 15 (alert subset). The /speckit-implement Prompt 5 step 6 appends the 24 test file paths to the AC-10 row in `docs/AC-MATRIX.md`. |
| Race-clean | `TestWatchdog_ConcurrentLogIngest` (B-W-21) runs 8 producers × 500 ingests under `-race`; `TestWatchdog_RunStopsOnCtxCancel` (B-W-15) snapshots `runtime.NumGoroutine` pre/post. |
| TDD-first | /speckit-tasks (Phase 4) generates a task list ordering test-writing tasks BEFORE implementation tasks per the chunk doc Prompt-4 directive. |
| Fuzz targets | None mandated for this chunk. The watchdog consumes pre-compiled regex (the operator-supplied string parse happens in SDD-18, which already has FuzzSuperviseTOML); the watchdog's input is `[]byte` log lines, which are non-secret operator output. Pattern complexity vetting is upstream's concern (spec Edge Cases). |

### Principle IX — Idiomatic Go discipline

| Requirement | Plan compliance |
|-------------|-----------------|
| Context propagation: `context.Context` first param of any I/O / cancellable function | `Run(ctx context.Context) error` — first-param ctx. `Ingest(line []byte)` does no I/O and is non-cancellable by design (FR-010a non-blocking + post-Run no-op). `OnStderrLine(ctx context.Context, line []byte)` adapter is first-param-ctx (the ctx is discarded, since the watchdog already holds Run's ctx). |
| Errors wrap with `%w`; sentinel `var Err... = errors.New(...)` | Seven sentinels declared at package level: `ErrAlreadyRan`, `ErrEmptyPatternName`, `ErrDuplicatePatternName`, `ErrNilPatternRegex`, `ErrNonPositiveRateLimit`, `ErrNilAlertsChannel`, `ErrNilLogger`. All NewWatchdog returns wrap via `fmt.Errorf("...: %w", sentinel)`. Run's ctx.Err() is returned via `fmt.Errorf("watchdog: run cancelled: %w", ctx.Err())`. |
| No globals, no `init()` | Seven `var Err... = errors.New(...)` are sentinel-class read-only globals (Constitution IX exemption per SDD-21 precedent); one unexported `const lineChannelCapacity = 512` is a compile-time constant. Zero `init()`. Zero mutable package-level state. |
| Panic policy | NewWatchdog returns errors for operator-input issues (Constitution IX — operator input is not startup-wiring). The matcher goroutine has a top-frame `defer func() { if r := recover(); r != nil { logger.Error(...) } }()` (panic-recover hygiene; spec implies no panics should escape, but the defence-in-depth recovery prevents one bad pattern from killing the supervisor). |
| Goroutine discipline | Exactly one goroutine owned by the watchdog: the matcher loop spawned inside Run. Owner = caller of Run. Cancellation = `<-ctx.Done()`. Termination = ctx done OR ErrAlreadyRan-rejected (in which case no goroutine is spawned at all). Documented in [data-model.md §4](./data-model.md#4-state-machine). |
| Interfaces: accept interfaces, return concrete types; consumer-side definition | The watchdog package defines NO new exported interfaces. It satisfies one consumer-side interface (`supervise.Watchdog`, defined at SDD-24) via the `OnStderrLine` adapter (R-003). The clock seam is the unexported field `now func() time.Time` (R-004), not a published interface — minimal exported surface. |
| Package layout | New sub-package at `internal/supervise/watchdog/` (R-001). The chunk doc names the package row `internal/supervise` but the SDD-24 `Watchdog` interface already occupies that identifier in the parent package — sub-package is the only collision-free location. |
| Modules-only, no vendor | No `go.mod` change. |
| CGO disabled | All stdlib; pure Go. |
| Generics | None used. |

### Principle X — Observability & redaction

| Requirement | Plan compliance |
|-------------|-----------------|
| Structured logging via `log/slog` | All log emission via `logger.LogAttrs(ctx, slog.LevelWarn, ...)` and `logger.LogAttrs(ctx, slog.LevelInfo, ...)`. No third-party logger. |
| Type-driven secret redaction (`SecureBytes.LogValue() → "[redacted]"`) | Pattern, Event, Watchdog hold no secret material (E-5, W-18 in [data-model.md](./data-model.md)). The package does NOT import `internal/vault/securebytes` — verified by `TestWatchdog_ZeroNewDependencies` (B-W-19). |
| No secret values in errors | All NewWatchdog rejection errors include the offending pattern's NAME only (the RHS); not the regex source (which is operator config and could in principle leak operator-internal labels — defensive choice), not the matched line, not anything else. The match-path errors do not exist (the matcher does not return errors; it logs WARN and continues). |
| Audit log separate from operational log | The watchdog writes ONLY to the operational logger (`*slog.Logger`). The audit row for a routed log-pattern alert is SDD-28's responsibility per Clarification Q1. This chunk's tests do NOT inspect the audit log. |
| Discord alert tiers | The Event flows to SDD-28's router; this chunk has no Discord coupling. |
| Metrics over Unix status socket only | Not in this chunk. |
| **No `string(secret)` anywhere** (FR-021-15 / SC-021-8 / Constitution X scope) | The watchdog package does NOT import `internal/vault/securebytes` and has no `*SecureBytes` field. The single `string(...)` site is `string(line)` inside the matcher (Event.Line construction); `line` is **operator log content** (child stderr), **not vault material**. Constitution X's prohibition is scoped to **secret material** — child stderr falls outside scope. Verified by `TestWatchdog_NoSecureBytesStringConversion` (B-W-18). |
| **Match-line content excluded from WARN log entries** (Clarification Q2) | All three WARN emission sites (rate-limit suppression, queue-full episode, alert-output saturation) build their `slog.Attr` set from pattern name + monotonic timestamp + counters only; the matched line is NEVER threaded into any WARN attribute. Sentinel-byte assertion in each of the three WARN tests (B-W-8, B-W-12, B-W-13) proves it. |

### Principle XI — Dependencies

| Requirement | Plan compliance |
|-------------|-----------------|
| Stdlib-first | Imports: `context`, `errors`, `fmt`, `log/slog`, `regexp`, `sync`, `sync/atomic`, `time` (all stdlib) + `github.com/mrz1836/hush/internal/supervise` for the interface guard. Zero third-party. |
| Every NEW direct dep requires justification | No new direct deps in this chunk. |
| `govulncheck` clean | No new deps to vulncheck. |
| `gitleaks` zero findings | Sentinel-byte tests for line content in logs (Clarification Q2) also serve as a redundant gitleaks-style assertion. |

### Locked vs implemented exported API

The chunk doc lists exactly **three structs + three functions/methods + zero sentinels** as the locked exported surface:

```text
type Pattern struct { Name string; Regex *regexp.Regexp; RateLimit time.Duration }
type Event struct { Pattern string; Line string; Time time.Time }
type Watchdog struct { ... }
func NewWatchdog(patterns []Pattern, alerts chan<- Event, logger *slog.Logger) *Watchdog
func (w *Watchdog) Ingest(line []byte)
func (w *Watchdog) Run(ctx context.Context) error
```

This plan honours that surface **with three justified extensions**
(Complexity Tracking entries #1–#3 below). No other exported
symbols are introduced.

The seven sentinel errors (`ErrAlreadyRan`, `ErrEmptyPatternName`,
`ErrDuplicatePatternName`, `ErrNilPatternRegex`,
`ErrNonPositiveRateLimit`, `ErrNilAlertsChannel`, `ErrNilLogger`)
are required by FR-007a (Clarification Q5) and by Constitution IX
("declare sentinel errors as exported package-level
`var Err... = errors.New(...)`"). They are NOT in the chunk-doc
"Exported API to lock" list but are mandated by the spec's
clarification and the Constitution; their addition is explicit and
recorded in the data-model and contracts files. They count as
sentinel-class read-only globals, not mutable globals (Constitution
IX exemption).

## Project Structure

### Documentation (this feature)

```text
specs/027-watchdog/
├── plan.md                               # this file
├── spec.md                               # WHAT (3 stories, 15 FRs, 6 SCs)
├── research.md                           # WHY (R-001..R-016)
├── data-model.md                         # locked struct shapes + 20 invariants
├── contracts/
│   ├── api.go                            # typed mirror of Go signatures (review-only)
│   └── observable-behaviors.md           # 25 black-box behavior contracts (B-W-1..B-W-25)
├── quickstart.md                         # runbook + 24-test list
├── checklists/                           # filled by /speckit-checklist (later)
└── tasks.md                              # filled by /speckit-tasks (Phase 4)
```

### Source Code (repository root)

The chunk creates a **new sub-package**. Justification in
[research.md R-001](./research.md#r-001--package-location-internalsupervisewatchdog-sub-package-not-internalsupervise).

```text
internal/supervise/
├── doc.go                                # existing (SDD-19)
├── state.go                              # existing (SDD-19)
├── child.go                              # existing (SDD-20)
├── lifecycle_*.go                        # existing (SDD-24)
├── lifecycle_interfaces.go               # existing (SDD-24) — owns `type Watchdog interface { OnStderrLine(...) }`
├── refill.go                             # existing (SDD-21)
├── refresh.go                            # existing (SDD-21)
├── grace.go                              # existing (SDD-21)
├── pidfile.go                            # existing (SDD-22)
├── socket*.go                            # existing (SDD-22)
├── config/                               # existing (SDD-18 sub-package)
│
└── watchdog/                             # NEW sub-package (this chunk)
    ├── watchdog.go                       # NEW — Pattern + Event + Watchdog + NewWatchdog + Ingest + Run + OnStderrLine + 7 sentinels
    └── watchdog_test.go                  # NEW — 24 mandatory tests + inline helpers
```

**Structure Decision:** Single Go module, new internal sub-package
`internal/supervise/watchdog` at import path
`github.com/mrz1836/hush/internal/supervise/watchdog`. Rationale:
the parent `package supervise` already declares
`type Watchdog interface { OnStderrLine(...) }` (SDD-24, locked);
re-declaring `type Watchdog struct` in the same package is not
expressible in Go and would either require renaming the SDD-24
interface (breaks a locked surface) or renaming the SDD-27 type
(contradicts the chunk doc). Sub-package preserves both verbatim
and mirrors the SDD-18 `internal/supervise/config` precedent.

### Cross-references

| Resource | Path |
|----------|------|
| Constitution | [.specify/memory/constitution.md](../../.specify/memory/constitution.md) |
| Spec | [spec.md](./spec.md) |
| Phase 0 research | [research.md](./research.md) |
| Phase 1 data model | [data-model.md](./data-model.md) |
| Phase 1 contracts | [contracts/api.go](./contracts/api.go) · [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) |
| Phase 1 quickstart | [quickstart.md](./quickstart.md) |
| Chunk doc | [docs/sdd/SDD-27.md](../../docs/sdd/SDD-27.md) |
| Lifecycle scenarios | [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §Scenario 15 |
| Config schema | [docs/CONFIG-SCHEMA.md](../../docs/CONFIG-SCHEMA.md) §`[watchdog]` |
| Package map (target) | [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) `internal/supervise/` |
| Existing interface contract | [internal/supervise/lifecycle_interfaces.go:51](../../internal/supervise/lifecycle_interfaces.go#L51) `type Watchdog interface { OnStderrLine(...) }` |
| Existing stderr plumbing | [internal/supervise/lifecycle_child.go:299](../../internal/supervise/lifecycle_child.go#L299) `lineSplittingWriter` |
| Existing alert-class enum | [internal/supervise/lifecycle_interfaces.go:71](../../internal/supervise/lifecycle_interfaces.go#L71) `AlertClassLogPatternMatch` |

## Complexity Tracking

The Constitution Check passes without principle-level violations.
Three locked-API extensions warrant explicit recording so the
downstream review can audit them quickly:

| Extension | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|--------------------------------------|
| **Sub-package `internal/supervise/watchdog` (vs the chunk-doc's "Package: internal/supervise")** | SDD-24 already declares `type Watchdog interface { OnStderrLine(...) }` at [internal/supervise/lifecycle_interfaces.go:51](../../internal/supervise/lifecycle_interfaces.go#L51) and locks it. The chunk doc names the SDD-27 type `Watchdog` — the same identifier. Go forbids declaring `type Watchdog struct` and `type Watchdog interface` in the same package. The sub-package keeps both verbatim. | (a) **Rename the SDD-24 interface.** Touches a merged-and-locked surface; cascades into every downstream Deps wiring. Chunk-doc anti-contract row explicitly forbids modifying any locked SDD-19..24 surface. Rejected. (b) **Rename the SDD-27 type to `LogPatternWatchdog`.** Contradicts the chunk-doc API list verbatim and the spec's "Watchdog Instance" key-entity row. Rejected. (c) **Single package with interface/struct deduplication tricks.** Go forbids it (cannot redeclare). Rejected. |
| **`NewWatchdog(...)` returns `(*Watchdog, error)` (vs the chunk-doc's bare `*Watchdog`)** | Spec FR-007a (Clarification Q5, 2026-05-13) mandates that the constructor MUST reject pattern sets with duplicate names. Constitution IX panic policy forbids panicking on operator-input errors — only on startup-wiring invariants. Returning `(*Watchdog, error)` is the only honest signature for an operator-input validator. The chunk-doc signature predates the clarification. | (a) **Panic on duplicates.** Violates Constitution IX panic policy. Operator config is not startup wiring. Rejected. (b) **Silently de-duplicate.** Spec Clarification Q5 explicitly forbids this ("clear construction-time error is preferable to silent attribution drift"). Rejected. (c) **Validate only at SDD-18 config-load time.** SDD-18 validates the watchdog config's `Patterns []string` field for non-empty entries but does NOT enforce name-level uniqueness at the richer `watchdog.Pattern` type the consumer constructs. Defence-in-depth argues for re-checking at the type boundary. Rejected (as sole defence). |
| **Additive `(*Watchdog) OnStderrLine(ctx context.Context, line []byte)` method (vs chunk-doc's 3-method API)** | The orchestrator (SDD-24) consumes watchdog implementations through `Deps.Watchdog supervise.Watchdog`, whose method set is `OnStderrLine(ctx, line)`. Without the adapter the orchestrator cannot accept a `*watchdog.Watchdog` value — every caller (the SDD-23 CLI wiring + any future caller) would need to write its own 2-line adapter type. The chunk doc lists symbols required for the chunk's own tests; orchestrator wiring is downstream. | (a) **Force every caller to write its own adapter.** Duplicates 4–6 lines across `internal/cli/supervise.go` and any future caller. Rejected for reuse cost. (b) **Promote OnStderrLine to the only producer entry point and drop Ingest.** Conflicts with the chunk-doc-locked Ingest signature and makes the producer ceremony heavier (every caller threads a context through what is mechanically a buffered enqueue). Rejected. (c) **Define a separate exported adapter type `WatchdogObserver` wrapping `*Watchdog`.** Adds a redundant exported symbol. Rejected. |

The "Exported API to lock" list in
[docs/sdd/SDD-27.md](../../docs/sdd/SDD-27.md) records six symbols
(Pattern, Event, Watchdog, NewWatchdog, Ingest, Run). The plan
implements those six verbatim plus the three extensions above,
yielding **3 exported types + 3 chunk-doc-named methods + 1
adapter method + 7 sentinel errors**. The /speckit-implement
Prompt 5 step 5 records this set verbatim under "Exported API —
locked at SDD-27" in `docs/PACKAGE-MAP.md`.

---

## Phase summary

| Phase | Output | Status |
|-------|--------|--------|
| 0 — Research | [research.md](./research.md) (R-001..R-016) | ✅ complete |
| 1 — Data model | [data-model.md](./data-model.md) (W-1..W-20) | ✅ complete |
| 1 — Contracts | [contracts/api.go](./contracts/api.go) + [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) (B-W-1..B-W-25) | ✅ complete |
| 1 — Quickstart | [quickstart.md](./quickstart.md) (24 mandatory tests) | ✅ complete |
| 1 — Agent context | [CLAUDE.md](../../CLAUDE.md) `<!-- SPECKIT START -->` block updated to point at this plan | ✅ complete |
| 2 — Tasks | tasks.md | ⏭ next: `/speckit-tasks` |
| 5 — Implement | `internal/supervise/watchdog/watchdog.go` + `watchdog_test.go` + post-step doc updates | ⏭ later: `/speckit-implement` |

The /speckit-plan command stops here. Phase 2 (`/speckit-tasks`) is
a separate session per the SDD-27 prompt sequence.
