# Implementation Plan: Discord Alert Surface (SDD-28)

**Branch:** `028-discord-alerts` | **Date:** 2026-05-13 | **Spec:** [spec.md](./spec.md) | **Chunk doc:** [docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md)
**Input:** Feature specification from `/specs/028-discord-alerts/spec.md`

## Summary

Add a new sibling sub-package at `internal/discord/alerts` that exports
the operator-visible Discord alert surface: **8 named alert classes**
(verbatim from `docs/LIFECYCLE-SCENARIOS.md` "Required alert classes"),
**3 named tiers** (`TierCritical`, `TierWarning`, `TierInfo`), an
immutable **class→tier binding table** locked at the values in
[research.md R-002](./research.md#r-002--the-8-alert-class-names--their-fixed-tier-binding),
and a single `Router.Route` entry-point that — for every call —
(1) re-derives tier from class (ignoring caller-supplied `Alert.Tier`
per FR-004 / R-010), (2) applies a per-supervisor + per-pattern
**minimum-interval debounce** with **commit-on-success** semantics
(Clarifications Q1 + Q3 + R-009 acquire/commit/refund), (3) renders
the alert via an **omit-empty-lines** per-class template
(Clarification Q4 + R-007), (4) dispatches by tier — Critical →
owner DM via the consumer-side `Sender` seam; Warning → audit-channel
post via the same seam; Info → operational log only, **zero** Discord
network call — and (5) emits **exactly one** `log/slog` record per
call (or zero on `ErrAlertRateLimited`) drawn from the FR-024
attribute allow-list at the level fixed by Clarification Q5 / R-008.
Three sentinel errors gate failure modes: `ErrAlertRateLimited`
(either bucket exhausted), `ErrAlertTransport` (single-shot Sender
call returned an error; wraps the underlying), and
`ErrUnknownAlertClass` (defensive guard for an `AlertClass` outside
the locked 8).

The chunk-doc-locked API
(`AlertClass`, `Tier`, `Alert`, `Router`, `NewRouter`, `Route`,
`ErrAlertRateLimited`) is honoured at the new sub-package with **two
justified plan-time extensions** recorded in
[§ Complexity Tracking](#complexity-tracking):

1. **R-003** — the `NewRouter` `approver discord.Approver` parameter
   becomes a consumer-side `alerts.Sender` interface (Constitution
   IX consumer-interface rule + PACKAGE-MAP.md "SDD-28 MUST NOT
   alter any [SDD-11] symbol above").
2. **R-011** — two additional sentinels (`ErrAlertTransport` per
   Clarification Q3 + `ErrUnknownAlertClass` per FR-009 defensive
   guard) beyond the chunk-doc's single `ErrAlertRateLimited`; both
   mandatory by the spec/clarifications.

Three structural choices follow directly from spec text and need no
extension marker: `Alert.Tier` is informational and re-derived from
`Alert.Class` (FR-004 + Spec Key Entities §Alert + R-010 — spec-
documented behavior, not a chunk-doc divergence); `NewRouter` panics
on nil `Sender` and nil `logger` (Constitution IX startup-wiring
exception, R-011 sentinel discipline); zero or negative bucket
windows fall back to a documented `DefaultBucketWindow =
1 * time.Minute` constant (R-011 defensive default).

The technical approach is pinned in [research.md](./research.md)
(R-001..R-016); the struct shapes and 20 type-level invariants in
[data-model.md](./data-model.md) (A-1..A-20); the typed mirror in
[contracts/api.go](./contracts/api.go); the 25 black-box behaviours
in [contracts/observable-behaviors.md](./contracts/observable-behaviors.md)
(B-A-1..B-A-25); the runbook + 25-test catalogue in
[quickstart.md](./quickstart.md).

## Technical Context

**Language/Version:** Go (version pinned in `go.mod`; floor only per
Constitution IX). Stdlib-first per Constitution XI.

**Primary Dependencies:** Standard library only —
`context`, `errors`, `fmt`, `log/slog`, `strings`, `sync`, `time`.
**Zero new direct dependencies** added by this chunk; verified by
`TestRouter_ZeroNewDependencies` (B-A-19). The package does NOT
import `github.com/bwmarrin/discordgo` (the Discord SDK stays an
SDD-11 implementation detail behind the `Sender` seam — R-016); does
NOT import `github.com/mrz1836/hush/internal/discord` at the
alerts-package level (the boundary is one-directional — the
production `*discord.BotApprover` gains additive `SendOwnerDM` /
`PostChannel` methods in this chunk's implementation phase and a
compile-time guard `var _ alerts.Sender = (*discord.BotApprover)(nil)`
lives in `internal/discord`, NOT in `alerts`); does NOT import
`github.com/mrz1836/hush/internal/vault/securebytes` (no credential
surface — also verified by `TestRouter_ZeroNewDependencies`, B-A-19).

**Storage:** None. The two per-key debounce maps live entirely in
process memory under per-`*ratebucket` `sync.Mutex` instances
(R-009). No on-disk artifacts; no audit-row writes from this chunk
(audit persistence is SDD-13 / SDD-24 — Spec Assumption row 8).

**Testing:** `go test -race -cover ./internal/discord/alerts/` ≥ 90%
(chunk-doc target; SC-011); 25 named tests per
[quickstart.md § 4](./quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4),
all `TestFunctionName_Scenario` per
`.github/tech-conventions/testing-standards.md`; race-clean assertion
via `TestRoute_ConcurrentSafety` (B-A-22 / SC-012); sentinel-byte
assertion via `TestAlert_NoSecretByteLeakage` (B-A-18) proving no
credential-shaped substring and no `Alert.Time` formatting survives
any render path (FR-022 / SC-009 / R-013).

**Target Platform:** macOS (darwin) + Linux. Pure Go (CGO disabled,
per `.goreleaser.yml`). Zero platform-specific code.

**Project Type:** Go module (single binary `cmd/hush`, internal
packages under `internal/`). New sub-package at
`internal/discord/alerts/` per [research.md R-001](./research.md#r-001--package-location-internaldiscordalerts-sub-package).

**Performance Goals:** Per-Route cost is O(1) excluding the
synchronous Sender network call (Critical/Warning) — two map lookups
(`classToTier`, `classToTemplate`) + two `*ratebucket.acquire` calls
under their respective mutexes + one `strings.Builder` render + one
Sender invocation. The debounce window is monotonic-time-based
(`time.Now().Sub(prev)` uses Go's monotonic reading) so NTP/DST jumps
cannot cause early refill or starvation (FR-015 / A-11 / R-015).

**Constraints:**

- **Constitution V** — every failure outcome surfaces loudly:
  `ErrAlertTransport` → WARN slog (B-A-13/14); `ErrUnknownAlertClass`
  → WARN slog (B-A-15); `ErrAlertRateLimited` → NO router-side
  record (caller logs the suppression per FR-016 / FR-024a /
  Clarification Q5 — deliberate single-source-of-truth choice).
- **Constitution IX** — `Route(ctx, alert)` is ctx-first; three
  sentinels declared as exported package-level
  `var Err... = errors.New(...)`; zero `init()`; zero mutable
  package-level state (`classToTier` and `classToTemplate` are
  constructed at declaration time by map literal and never mutated —
  sentinel-class read-only globals per the SDD-21 / SDD-26 / SDD-27
  exemption documented in [data-model.md §2.3](./data-model.md#23-package-level-immutable-maps));
  the package defines its consumer-side `Sender` interface (cohesive
  2-method seam) and accepts it at `NewRouter`; the package spawns
  **zero** goroutines (every Route call is synchronous end-to-end on
  the caller's goroutine — no monitor, no producer, no fire-and-
  forget audit-channel mirror).
- **Constitution X** — `Alert.SupervisorName`, `Alert.MachineName`,
  `Alert.Pattern`, `Alert.Detail` are the only fields touched by
  template machinery (per-class allow-list documented in
  [data-model.md §3.1-3.7](./data-model.md#3-field-by-field-semantics));
  the package does NOT import `securebytes` and has no `*SecureBytes`
  field; `Alert.Time` is NOT rendered into the body (R-013); the
  single `string(...)` site is `string(class)` on the typed
  `AlertClass` string enum (operator-visible name, not credential
  material). Verified by `TestRouter_ZeroNewDependencies` (B-A-19) +
  `TestAlert_NoSecretByteLeakage` (B-A-18).
- **Constitution XI** — zero new direct go.mod dependencies.

**Scale/Scope:** Per-router: up to ~100 distinct supervisor keys ×
~100 distinct pattern keys at steady state (typical deployment is
< 10 supervisors × ≤ 8 patterns); one Router instance per process;
zero goroutines spawned by the package; debounce maps grow
monotonically (no explicit eviction in v0.1.0 — full-map cost is
< 32 KiB at steady state; post-v0.1.0 bounded-LRU is a possible
amendment).

## Constitution Check

*Initial gate (pre-Phase-0):* **PASS**.
*Re-evaluation after Phase 1 design:* **PASS**.

Two locked-API extensions are tracked in
[§ Complexity Tracking](#complexity-tracking) as justified
divergent/additive changes; no Constitution principle is violated.

In-scope principles per chunk doc + user prompt: **V, VIII, IX, X**.
**XI** (dependencies) is verified by `TestRouter_ZeroNewDependencies`
(B-A-19).
**I, II, III, IV, VI, VII** are not exercised by this chunk.

### Principle V — Staleness visible, failure loud

| Requirement | Plan compliance |
|-------------|-----------------|
| Pluggable client-side validators | Out of scope (owned by SDD-26). The alerts package consumes validator-failure signals as `AlertClassValidatorStaleFailure` (TierWarning per Constitution X). |
| Distinct, actionable alerts | All 8 classes carry distinct label prefixes (FR-017 / FR-018 + B-A-16 + A-6); prefixes are pinned in [data-model.md §2.3](./data-model.md#23-package-level-immutable-maps). The three lifecycle stale-alert formats (`[STALE] Validator Failure`, `[STALE] Child Exit 78`, `[STALE] Log Pattern Match`) trace verbatim to the corresponding per-class label prefixes. |
| Log-pattern auth-failure tailing alert-only | The watchdog (SDD-27) emits a typed `Event`; this chunk receives the signal as `AlertClassLogPatternStaleWarning` (TierWarning) and renders the operator notification. The router has zero control-plane authority — no state-machine transition, no child signal, no restart decision (FR-004). |
| Distinct loud failures (no silent drops) | (a) Every transport failure → WARN slog (B-A-13/14 + B-A-20). (b) Every unknown-class → WARN slog (B-A-15 + B-A-20). (c) Rate-limit suppression deliberately emits zero router records — the *caller* logs the suppression with its own context (FR-016 + FR-024a + Q5). The router never silently swallows any other outcome; every Critical/Warning success → exactly one DEBUG record; every Info success → exactly one INFO record (FR-024a / R-008). |
| Stale alerts visible to operator | Class→tier binding (R-002) maps the three stale-* classes to operator-visible destinations: `[STALE] Validator Failure` → Warning → audit channel; `[STALE] Child Exit 78` → Critical → owner DM; `[STALE] Log Pattern Match` → Warning → audit channel. |

### Principle VIII — Testing discipline

| Requirement | Plan compliance |
|-------------|-----------------|
| Table-driven unit tests per `.github/tech-conventions/testing-standards.md` | All 25 tests in [quickstart.md § 4](./quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4) named `TestFunctionName_Scenario`. Table-driven where the matrix axis is meaningful: `TestRoute_TierBindingMatrix` (8 classes × expected tier), `TestRoute_SlogLevelMatrix` (5 outcome × level rows), `TestRateLimit_PerKeyIsolation` (4 isolation pairs). |
| Pre-commit MUST pass `golangci-lint` + `go test -race` | `magex format:fix && magex lint && magex test:race` are the gate commands in [quickstart.md § 3](./quickstart.md#3-test-commands). |
| ≥ 90 % coverage on the new sub-package (chunk-doc target + SC-011) | `go test -coverprofile ./internal/discord/alerts/` ≥ 90.0 %. |
| AC-3 + AC-10 → required test types | Test files map to AC-3 (Discord-side operator surface) and the alert-emission half of AC-10 Lifecycle Scenarios 2, 5, 6, 8, 10, 11, 15. /speckit-implement Prompt 5 step 7 appends the three test-file paths to the AC-3 + AC-10 rows of `docs/AC-MATRIX.md`. |
| Race-clean | `TestRoute_ConcurrentSafety` (B-A-22 / SC-012) runs 8 producers × 100 calls under `-race` with disjoint and overlapping supervisor/pattern keys. |
| TDD-first | /speckit-tasks (Phase 4) generates a task list ordering test-writing tasks BEFORE the corresponding implementation task per the chunk-doc Prompt-4 directive. |
| Fuzz targets | None mandated. The router's input is a typed `Alert` value built by trusted in-process callers; the rendered-body fuzzing is delegated to `TestAlert_NoSecretByteLeakage` (B-A-18) which seeds known marker bytes across the four operator-supplied fields plus a "secret marker" into the test environment, then asserts the rendered output never contains the secret marker (FR-022 / SC-009). |

### Principle IX — Idiomatic Go discipline

| Requirement | Plan compliance |
|-------------|-----------------|
| Context propagation: ctx first param of any I/O / cancellable function | `Route(ctx context.Context, alert Alert) error` — first-param ctx. The ctx threads through to `Sender.SendOwnerDM(ctx, ...)` / `Sender.PostChannel(ctx, ...)` so caller cancellation aborts the Discord network call inside the Sender implementation. |
| Errors wrap with `%w`; sentinels via `var Err... = errors.New(...)` | Three sentinels: `ErrAlertRateLimited`, `ErrAlertTransport`, `ErrUnknownAlertClass`. All declared at package level with the `hush/discord/alerts:` prefix ([data-model.md §1.6](./data-model.md#16-sentinel-errors-r-011)). Transport-failure path wraps via `errors.Join(ErrAlertTransport, underlying)` (or equivalent `fmt.Errorf` with `%w`) so callers can `errors.Is(err, ErrAlertTransport)` AND `errors.As(err, &target)` for the underlying. |
| No globals, no `init()` | Three `var Err... = errors.New(...)` are sentinel-class read-only globals (Constitution IX exemption per SDD-21 / SDD-26 / SDD-27 precedent). The `classToTier` and `classToTemplate` package-level maps are constructed by literal initialization (no `init()` function — values are literal expressions at declaration) and never mutated post-declaration; they count as sentinel-class read-only globals. `TestRoute_TierBindingMatrix` (B-A-3) asserts cardinality and per-class bindings; `TestTemplate_LabelPrefixUniqueAndStable` (B-A-16) asserts the template-map cardinality and prefix uniqueness. Zero `init()`. Zero mutable package-level state. |
| Panic policy | The package returns errors for every runtime failure mode. An `Alert.Class` outside the 8 enum values returns `ErrUnknownAlertClass` — the FR-009 "defensive — should never happen if the caller used the constants" guard. `NewRouter` panics on nil `Sender` and nil `logger` (Constitution IX startup-wiring exception; asserted by `TestNewRouter_NilGuards`, B-A-25); zero panics in the Route hot path; zero `recover()` (no goroutines to recover in). |
| Goroutine discipline | The package spawns **zero** goroutines. `Route` is synchronous end-to-end on the caller's goroutine. No monitor, no producer, no fire-and-forget. Documented in [data-model.md §4](./data-model.md#4-state-machine) and [research.md R-009](./research.md#r-009--acquirecommitrefund-rate-bucket-internals-fr-012a--concurrency). |
| Interfaces: accept interfaces, return concrete types; consumer-side definition | The package defines exactly one exported interface — `type Sender interface { SendOwnerDM(ctx, message); PostChannel(ctx, channelID, message) }` — at the consumer site (R-003). The package returns concrete `*Router` from `NewRouter`. The chunk-doc's `approver discord.Approver` parameter is replaced by `sender Sender` (Complexity Tracking entry #1); the production `*discord.BotApprover` gains additive `SendOwnerDM`/`PostChannel` methods in this chunk's implementation phase (non-locked-surface extension — the `Approver` interface itself is untouched). |
| Package layout | New sub-package at `internal/discord/alerts/` (R-001). No collision with `internal/supervise.AlertClass`: different import paths and different shapes (supervise.AlertClass is `int` for the orchestrator's 10-value producer enum; alerts.AlertClass is `string` for the 8-value delivery enum). SDD-25 owns the producer→delivery mapping at the lifecycle wiring layer. |
| Modules-only, no vendor | No `go.mod` change. |
| CGO disabled | All stdlib; pure Go. |
| Generics | None used. |

### Principle X — Observability & redaction

| Requirement | Plan compliance |
|-------------|-----------------|
| Structured logging via `log/slog` | All log emission via `logger.LogAttrs(ctx, level, "alert routed", attrs...)`. No third-party logger. Exactly one record per Route call (FR-024a) except on `ErrAlertRateLimited` (zero records — Q5 / FR-016). |
| Type-driven secret redaction | The package does NOT import `internal/vault/securebytes` — verified by `TestRouter_ZeroNewDependencies` (B-A-19) — and holds no `*SecureBytes` field. There is no credential surface within this chunk; the Alert struct's four operator-supplied string fields are explicitly NOT credential-bearing (Spec Assumption row 5: `Alert.Detail` carries operator-supplied metadata only). |
| No secret values in errors | All three sentinels carry static category messages (`"hush/discord/alerts: rate limited"`, `"...: transport failed"`, `"...: unknown class"`). The wrapped transport-error chain may carry the underlying network error returned by the `Sender`; the alerts package adds no credential-derived substring. `TestAlert_NoSecretByteLeakage` (B-A-18 / SC-009 / FR-022) asserts no seeded marker byte string survives from an Alert's render path to anywhere observable. |
| Audit log separate from operational log | The alerts package writes ONLY to the operational `*slog.Logger`. The cryptographic audit chain is SDD-13's responsibility; the audit-channel mirror (Warning tier) is operator-visible *operational* observability, NOT the cryptographic audit chain. Both surfaces co-exist by design (Spec Assumption row 8). |
| Discord alert tiers | The package IS the canonical tier-routing implementation per Constitution X. The locked class→tier bindings in [research.md R-002](./research.md#r-002--the-8-alert-class-names--their-fixed-tier-binding) trace each binding back to Constitution X tier rules + `docs/OPERATIONS.md` + LIFECYCLE-SCENARIOS. |
| Metrics over Unix status socket only | Not in this chunk. |
| **Log-attribute allow-list** (FR-024) | Allow-list: `{class, tier, supervisor, machine, pattern, outcome}`. No `detail`, no `rendered`, no `error.<credential-substring>`, no `*http.Request`/`*http.Response`. Verified by `TestRoute_SlogAttributeAllowList` (B-A-21) — a recording slog handler drives every code path and asserts the attribute key set is a strict subset of the allow-list. |
| **slog level per outcome** (FR-024a, Clarification Q5) | Critical-tier success → DEBUG; Warning-tier success → DEBUG; Info-tier success → INFO; transport failure (any tier) → WARN; unknown-class → WARN; rate-limit suppression → NO RECORD. Six-row dispatch verified by `TestRoute_SlogLevelMatrix` (B-A-20). |

### Principle XI — Dependencies (verified, not in-scope per user prompt)

| Requirement | Plan compliance |
|-------------|-----------------|
| Stdlib-first | Imports: `context`, `errors`, `fmt`, `log/slog`, `strings`, `sync`, `time`. Zero third-party. |
| Every NEW direct dep requires justification | No new direct deps. |
| `govulncheck` clean | No new deps to vulncheck. |
| `gitleaks` zero findings | Sentinel-byte tests serve as a redundant adversarial probe. |

### Locked vs implemented exported API

The chunk doc [docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md) rows
30-37 lock the following exported surface:

```text
type AlertClass string
const ( AlertClassXxx, ... ) // 8 values verbatim from docs/LIFECYCLE-SCENARIOS.md
type Tier int
const ( TierCritical Tier = iota; TierWarning; TierInfo )
type Alert struct { Class AlertClass; Tier Tier; SupervisorName, MachineName, Pattern, Detail string; Time time.Time }
type Router struct { /* unexported */ }
func NewRouter(approver discord.Approver, auditChannelID string,
               perSupervisorBucket, perPatternBucket time.Duration,
               logger *slog.Logger) *Router
func (r *Router) Route(ctx context.Context, alert Alert) error
var ErrAlertRateLimited
```

This plan honours every locked symbol **verbatim in name** with two
plan-time extensions:

| Locked-doc shape | Plan-implemented shape | Rationale |
|------------------|------------------------|-----------|
| `NewRouter(approver discord.Approver, ...)` | `NewRouter(sender Sender, auditChannelID string, perSupervisorBucket, perPatternBucket time.Duration, logger *slog.Logger) *Router` — same return type; `approver` parameter renamed and re-typed to a 2-method consumer-side `Sender` interface | R-003: `discord.Approver` exposes only `RequestApproval` and is locked by PACKAGE-MAP.md:1245-1246. Constitution IX consumer-interface rule. Complexity Tracking entry #1. |
| `var ErrAlertRateLimited` (sole sentinel) | `ErrAlertRateLimited` + `ErrAlertTransport` + `ErrUnknownAlertClass` | R-011: Clarification Q3 mandates `ErrAlertTransport`; FR-009 mandates a typed error for unknown classes. Complexity Tracking entry #2. |

Structural choices that need NO extension marker (spec-documented
behavior, not chunk-doc divergence):

| Behavior | Source |
|----------|--------|
| `Alert.Tier` is caller-informational; Route re-derives the authoritative tier from `Alert.Class` via the immutable `classToTier` map. | FR-004 + Spec Key Entities §Alert + R-010. |
| `NewRouter` panics on nil `sender` and nil `logger`. | Constitution IX startup-wiring exception (R-011); asserted by `TestNewRouter_NilGuards` (B-A-25). |
| Zero / negative bucket windows fall back to `DefaultBucketWindow = 1 * time.Minute`. | R-011 defensive default; documented in [data-model.md §2.4](./data-model.md#24-outcome--slog-attribute-constants). |

No other exported symbols are introduced. Total exported surface:
**2 types (AlertClass, Tier) + 11 constants (8 classes + 3 tiers) +
2 structs (Alert, Router) + 1 interface (Sender) + 2 funcs
(NewRouter, Route) + 3 sentinels + 1 default const = 22 exported
symbols** (re-listed in
[contracts/observable-behaviors.md § Locked exported surface](./contracts/observable-behaviors.md)).

## Project Structure

### Documentation (this feature)

```text
specs/028-discord-alerts/
├── plan.md                                # this file
├── spec.md                                # WHAT (5 stories, 30 FRs, 13 SCs, 5 clarifications)
├── research.md                            # WHY (R-001..R-016)
├── data-model.md                          # locked struct shapes + 20 invariants (A-1..A-20)
├── contracts/
│   ├── api.go                             # typed mirror of Go signatures (review-only)
│   └── observable-behaviors.md            # 25 black-box behavior contracts (B-A-1..B-A-25)
├── quickstart.md                          # runbook + 25-test list
├── checklists/                            # filled by /speckit-checklist (later)
└── tasks.md                               # filled by /speckit-tasks (Phase 4)
```

### Source Code (repository root)

The chunk creates a **new sibling sub-package** of `internal/discord`.
Justification in [research.md R-001](./research.md#r-001--package-location-internaldiscordalerts-sub-package).

```text
internal/discord/
├── approver.go                            # existing (SDD-11) — type Approver, ApprovalRequest, Decision, BotConfig
├── audit.go                               # existing (SDD-11) — audit-channel mirror for approval events
├── bot.go                                 # existing (SDD-11) — *BotApprover (DM transport for approval flow)
├── errors.go                              # existing (SDD-11) — ErrDiscordUnavailable, ErrApprovalDenied, ...
├── monitor.go                             # existing (SDD-11) — reconnect/monitor goroutine
├── ratelimit.go                           # existing (SDD-11) — per-(SupervisorName, ClientIP) approval rate limit
├── render.go                              # existing (SDD-11) — approval DM render templates
│
└── alerts/                                # NEW sub-package (this chunk)
    ├── alerts.go                          # NEW — AlertClass + 8 constants + Tier + 3 constants + Alert + Router + NewRouter + Route + Sender interface + classToTier immutable map + 3 sentinels + DefaultBucketWindow
    ├── templates.go                       # NEW — classToTemplate immutable map + classTemplate.render (omit-empty-lines strings.Builder)
    ├── ratelimit.go                       # NEW — ratebucket + bucketState + acquire/commit/refund methods
    ├── alerts_test.go                     # NEW — 16 of 25 tests (B-A-1..7, 13..15, 19..22, 24, 25) + recordingSender + failingSender + failOnInvokeSender + recordingHandler helpers
    ├── templates_test.go                  # NEW — 3 tests (B-A-16, 17, 18) — label-prefix uniqueness, omit-empty-lines, sentinel-byte no-leak
    └── ratelimit_test.go                  # NEW — 6 tests (B-A-8..12, B-A-23) — per-supervisor / per-pattern / isolation / class-fallback / Info-tier rate-limit / monotonic clock
```

In the parent `internal/discord/` package, the SDD-28 implementation
phase additionally adds **two additive methods** on `*BotApprover`
(`SendOwnerDM`, `PostChannel`) plus a compile-time guard
`var _ alerts.Sender = (*BotApprover)(nil)`. These are additive (the
locked `Approver` interface and its method set are unchanged) and
live in a new file `internal/discord/sender.go` written during
/speckit-implement Prompt 5. The compile-time guard means
`internal/discord` imports `internal/discord/alerts`, but `alerts`
never imports back — the import direction stays one-way (asserted
by B-A-19).

**Structure Decision:** Single Go module, new internal sub-package
`internal/discord/alerts` at import path
`github.com/mrz1836/hush/internal/discord/alerts`. Rationale:

- The chunk doc lists the package as `internal/discord/alerts`.
- PACKAGE-MAP.md §`internal/discord/` lines 1244-1246 explicitly
  directs SDD-28 to a sibling sub-package and forbids altering any
  locked SDD-11 symbol.
- Sub-package keeps `internal/discord/`'s SDD-11 surface untouched
  (the additive `SendOwnerDM`/`PostChannel` methods on
  `*BotApprover` are non-locked-surface extensions; the locked
  `Approver` interface and its method set are unchanged).
- `internal/supervise.AlertClass` (10-value int enum for
  orchestrator-internal events, SDD-24) is conceptually distinct
  from this chunk's 8-value string enum: the supervise enum is the
  *producer* token (what the orchestrator emits when reporting an
  event); the alerts enum is the *delivery* class (how Z sees the
  event in Discord). Different import paths, different concerns;
  the mapping is SDD-25's wiring responsibility.
- `internal/discord.Approver` is unrelated to
  `internal/discord/alerts.Sender` — different packages, different
  concerns (Approver: interactive blocking approval; Sender: one-way
  notification transport).

### Cross-references

| Resource | Path |
|----------|------|
| Constitution | [.specify/memory/constitution.md](../../.specify/memory/constitution.md) |
| Spec | [spec.md](./spec.md) |
| Phase 0 research | [research.md](./research.md) |
| Phase 1 data model | [data-model.md](./data-model.md) |
| Phase 1 contracts | [contracts/api.go](./contracts/api.go) · [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) |
| Phase 1 quickstart | [quickstart.md](./quickstart.md) |
| Chunk doc | [docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md) |
| Required Alert Classes (verbatim source) | [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §"Required alert classes" (lines 301-314) |
| Lifecycle scenarios → alert classes | [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §Scenarios 1, 2, 5, 6, 8, 10, 11, 15 |
| Tier-binding source of truth (Constitution X) | [.specify/memory/constitution.md](../../.specify/memory/constitution.md) §"Discord alert tiers" |
| SDD-11 locked surface (anti-contract) | [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) §`internal/discord/` lines 1130-1246 |
| SDD-11 rate-limit precedent (acquire/commit/refund) | [internal/discord/ratelimit.go](../../internal/discord/ratelimit.go) |
| Watchdog producer side (LogPatternStaleWarning source) | [internal/supervise/watchdog/watchdog.go](../../internal/supervise/watchdog/watchdog.go) |
| Orchestrator-internal `AlertClass` enum (orthogonal) | [internal/supervise/lifecycle_interfaces.go:55-72](../../internal/supervise/lifecycle_interfaces.go#L55-L72) |

## Complexity Tracking

The Constitution Check passes without principle-level violations.
Two locked-API extensions warrant explicit recording so the
downstream review can audit them quickly. Both are mandated by spec
text that post-dates the chunk-doc API list, so they are
spec-driven additions rather than design freedoms.

| # | Extension | Why Needed | Simpler Alternative Rejected Because |
|---|-----------|------------|--------------------------------------|
| **1** | **`NewRouter` takes `Sender` (consumer-side 2-method interface) instead of `discord.Approver`** (R-003) | (a) `internal/discord.Approver` exposes only `RequestApproval(ctx, ApprovalRequest) (Decision, error)` — it has no DM-send or channel-post primitive, so threading `Approver` through `NewRouter` fails compilation against the routing contract. (b) PACKAGE-MAP.md §`internal/discord/` lines 1245-1246 explicitly states "SDD-28 MUST NOT alter any [SDD-11] symbol above"; adding methods to the locked `Approver` interface is forbidden. (c) The chunk doc itself acknowledges the gap in Prompt 3 lines 179-184 ("use a simple SendDM helper if Approver doesn't expose one — define one"). (d) Constitution IX mandates "define interfaces at the consumer, prefer single-method or cohesive small interfaces" — a 2-method `Sender` is the cohesive seam for the two routing destinations. (e) `*discord.BotApprover` satisfies `alerts.Sender` via two additive concrete-type methods (`SendOwnerDM`, `PostChannel`) added in this chunk's implementation phase — additive methods on a concrete type are not locked-surface mutations. | (a) **Take `discord.Approver` verbatim AND add `SendDM`/`PostChannel` methods to the `Approver` interface.** Alters a locked surface (PACKAGE-MAP.md:1245-1246 anti-contract); cascades into every existing discord-package test + the SDD-12 server wiring. Rejected. (b) **Take `discord.Approver` and type-assert at the boundary to `*BotApprover`.** Loses the interface-at-the-boundary guarantee; breaks every test that passes a fake; conflicts with Constitution IX "accept interfaces". Rejected. (c) **Take `*discordgo.Session` directly.** Third-party type on the package surface; couples alerts to a specific SDK version; defeats Constitution XI minimal-deps. Rejected. (d) **Two separate single-method interfaces (`DMSender`, `ChannelPoster`) and two constructor args.** Every Router needs BOTH destinations wired at construction; splitting adds a constructor argument with no semantic benefit. The 2-method `Sender` is the natural unit of "alert transport". Rejected. |
| **2** | **Three sentinel errors (`ErrAlertRateLimited` + `ErrAlertTransport` + `ErrUnknownAlertClass`) instead of the chunk-doc's single `ErrAlertRateLimited`** (R-011) | (a) Clarification Q3 (2026-05-13) introduces `ErrAlertTransport` as the typed sentinel for single-shot Discord delivery failure — caller distinguishes rate-limit suppression from transport failure via `errors.Is(err, ErrAlertRateLimited)` vs `errors.Is(err, ErrAlertTransport)`. The clarification post-dates the chunk-doc API list. (b) FR-009 + Spec Edge Case "Unknown class passed to the router" mandate a typed error for the defensive guard: "return a typed error, do NOT contact Discord, do NOT decrement the rate-limit buckets, do NOT panic". Reusing an existing sentinel would conflate distinct outcomes. (c) Constitution IX mandates exported `var Err... = errors.New(...)` for every distinct failure mode. | (a) **Reuse `ErrAlertRateLimited` for transport failure.** Caller cannot distinguish "Discord rate-limited us" from "we self-debounced"; defeats Q3's explicit ask. Rejected. (b) **Use an unexported error type for unknown class and panic.** Violates Constitution IX panic policy on runtime invariants; FR-009 explicitly forbids panic. Rejected. (c) **Wrap the underlying transport error without a class-discriminating sentinel.** Caller relies on `errors.Is(err, ErrAlertTransport)` to write tier-aware retry logic — without the sentinel, callers must string-match the wrapped error. Rejected. |

The "Exported API to lock" list in
[docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md) records: type
`AlertClass` + 8 constants, type `Tier` + 3 constants, struct
`Alert`, type `Router`, `NewRouter`, `Route`, sentinel
`ErrAlertRateLimited`. The plan implements those symbols **verbatim
in name and shape** with the two extensions above and the documented
structural choices. The /speckit-implement Prompt 5 step 6 records
this set under "Exported API — locked at SDD-28" in
`docs/PACKAGE-MAP.md`.

---

## Phase summary

| Phase | Output | Status |
|-------|--------|--------|
| 0 — Research | [research.md](./research.md) (R-001..R-016) | ✅ complete |
| 1 — Data model | [data-model.md](./data-model.md) (A-1..A-20) | ✅ complete |
| 1 — Contracts | [contracts/api.go](./contracts/api.go) + [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) (B-A-1..B-A-25) | ✅ complete |
| 1 — Quickstart | [quickstart.md](./quickstart.md) (25 mandatory tests) | ✅ complete |
| 1 — Agent context | [CLAUDE.md](../../CLAUDE.md) `<!-- SPECKIT START -->` block updated to point at this plan | ✅ complete |
| 2 — Tasks | tasks.md | ⏭ next: `/speckit-tasks` |
| 5 — Implement | `internal/discord/alerts/*.go` + `internal/discord/sender.go` + post-step doc updates | ⏭ later: `/speckit-implement` |

The /speckit-plan command stops here. Phase 2 (`/speckit-tasks`) is
a separate session per the SDD-28 prompt sequence.
