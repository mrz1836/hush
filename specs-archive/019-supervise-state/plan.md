# Implementation Plan: Supervisor State Machine + Snapshot Store (SDD-19)

**Branch**: `019-supervise-state` | **Date**: 2026-05-05 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/019-supervise-state/spec.md`

## Summary

SDD-19 introduces the `internal/supervise` package's first behaviour
file: `state.go`. It is the supervisor daemon's single in-memory
source of truth — a guarded five-state finite state machine
(`fetching`, `running`, `awaiting-approval`, `grace-restart`,
`stopped`) plus a defensive-copy `Snapshot` accessor. Downstream
chunks (SDD-20 child fork/exec, SDD-21 refill/refresh/grace, SDD-22
status socket) read state and metadata exclusively through this
store. The closed event vocabulary is 15 events transcribed from
Scenarios 2–15 of `docs/LIFECYCLE-SCENARIOS.md` (FR-019-21).
Illegal `(state, event)` pairs are rejected with the named sentinel
`ErrInvalidTransition`, leaving the store unchanged. The cached
session JWT is held as a `*securebytes.SecureBytes` so structured
log emission renders `[redacted]`. The store owns **zero
goroutines** and triggers **zero side-effects** — it is a pure
guarded data model whose mutations are commit-style under a
`sync.RWMutex`. A clock seam (`Clock` interface) supplied at
`NewStore` time produces deterministic last-transition timestamps
under test and `time.Now()` in production.

## Technical Context

**Language/Version**: Go (toolchain pinned in `go.mod` — current floor `go 1.23`)
**Primary Dependencies**: Go stdlib only (`context`, `errors`, `fmt`, `sync`, `time`, `log/slog`); internal `github.com/mrz1836/hush/internal/vault/securebytes` (locked at SDD-02). **Zero new direct dependencies.**
**Storage**: N/A — purely in-memory state. No filesystem, no socket, no network touch.
**Testing**: `go test -race` (stdlib `testing`), table-driven per `.github/tech-conventions/testing-standards.md`. Race-detector clean across 100-iteration concurrent transition+snapshot suite.
**Target Platform**: darwin (macOS) and linux. Pure Go (`CGO_ENABLED=0`).
**Project Type**: Single-binary Go CLI (`cmd/hush`) with internal packages under `internal/`. This chunk adds files to the existing `internal/supervise/` directory (which currently contains only the `config/` subpackage from SDD-18) — `state.go` is the **first file in `package supervise` itself**.
**Performance Goals**: `Transition` and `Snapshot` are sub-microsecond; the store is on the hot path of every supervisor lifecycle event but is not throughput-bound. No allocation budget is tightened beyond stdlib defaults; correctness and race-cleanliness dominate.
**Constraints**:
- No goroutines spawned by the package (FR-019-12).
- No side-effects beyond in-memory mutation (FR-019-13).
- Cached JWT redaction is type-driven via `SecureBytes.LogValue` (FR-019-9, Constitution X).
- Race-detector clean under `go test -race` (FR-019-14, SC-019-2).
- Coverage ≥95% on `internal/supervise` for files in this chunk (`state.go` + helpers) (SC-019-6, Constitution VIII High band).
**Scale/Scope**: One `Store` instance per supervisor process. The single-process duplication guard lives upstream (PID-file/flock per Scenario 14); the store does not police uniqueness. State table = 5 states × 15 events = 75 cells (subset legal per the matrix; remainder negative-tested).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principle IV — Supervisor for Daemons, Wrap-Shell for Humans

| Constraint | Plan compliance |
|---|---|
| Supervisor JWTs MUST carry `session_type: "supervisor"` | Out of scope for this chunk; the JWT itself is produced by SDD-07 and the supervisor's claim flow is owned by SDD-20/21. The store accepts an opaque `*securebytes.SecureBytes` from upstream callers and never inspects claims. ✅ no conflict. |
| Supervisor TTL capped at `max_supervisor_session_ttl` | TTL enforcement is the validator/refresh layer's job; the state machine merely transitions on the events those layers emit (`EventFetchOK`, `EventGraceExpired`, etc.). ✅ no conflict. |
| Supervisor sessions are TTL-only, not use-counted | Matches: the state machine has no use-counter field; `Snapshot` exposes `LastTransitionAt`, never a use count. ✅ |
| A child exit MUST NOT kill the supervisor | Encoded directly: `running --EventChildExitClean--> fetching`, `running --EventChildExitCrash--> fetching`, `running --EventChildExit78Stale--> awaiting-approval` — none transition to `stopped`. Only `EventStopRequested` reaches `stopped`. ✅ |
| Supervisor MUST zero secret material after handoff (except grace cache) | The store holds the **session JWT**, not the plaintext secrets — and it holds the JWT in a `*SecureBytes` whose `Destroy()` is idempotent. The store calls `Destroy` on the previously-held token whenever a transition replaces it (e.g. `EventApprovalGranted` → fetching → new token after `EventFetchOK`); SDD-21 owns the actual replacement event sequence. The plaintext secret cache is owned by SDD-21 and never enters this store. ✅ |

**Result:** PASS.

### Principle V — Staleness is Visible, Failure is Loud

| Constraint | Plan compliance |
|---|---|
| Validators run before child sees secrets | The state-table edge `fetching --EventValidatorFailed--> awaiting-approval` (event #5, Scenario 6) makes validator failure a first-class state visible to the status socket. ✅ |
| Exit 78 = "my creds are stale" | Encoded as `running --EventChildExit78Stale--> awaiting-approval` (event #9, Scenario 5). Distinct from `EventChildExitCrash` (event #8) which silent-refills via `running → fetching`. ✅ |
| Local Unix status socket exposes freshness state | The status socket (SDD-22) reads exclusively via `Snapshot()`. Defensive-copy semantics (FR-019-8) let it serialize the snapshot without holding any lock against the store. ✅ |
| Log-pattern auth-failure tailing is alert-only | Confirmed by **omission**: there is no `EventLogPatternMatch` in the v0.1.0 vocabulary (FR-019-21). Scenario 15 says "no restart decision is made based on the log alone" — the watchdog raises an alert via the Discord layer (SDD-25) but does NOT call `Transition`. ✅ |
| Distinct, actionable alerts in Discord | The closed `Reason` mapping ensures every transition has a human-readable phrase the status socket and audit log render verbatim. The `Reason` is **derived from the event by the store** (per Clarification 1) so callers cannot drift the wording. ✅ |

**Result:** PASS.

### Principle VIII — Testing Discipline

| Constraint | Plan compliance |
|---|---|
| Table-driven unit tests, `TestFunctionName_Scenario` PascalCase | Test file `state_test.go` will follow that convention. Test catalogue defined in `contracts/test-catalogue.md`. ✅ |
| Coverage tier — High band (state machine) ≥95% | Plan target: ≥95% on the new files in `internal/supervise/` (state.go + companions). Verified by `go test -cover ./internal/supervise/` excluding the `config/` subpackage already covered by SDD-18. ✅ |
| Race-detector clean | `TestStore_ConcurrentTransitionAndSnapshot` will fan out N transition-driving goroutines + M snapshot-reading goroutines under `go test -race`, asserting no race report and that every observed snapshot's `(State, ChildPID, LastTransitionAt)` triple is from a single commit point. ✅ |
| Mandatory fuzz targets — list of 6 | This chunk adds **no new fuzz target**. Fuzz #5 (supervisor TOML) is owned by SDD-18; fuzz #4 (request signature) and #6 (status socket JSON) belong to SDD-08 and SDD-22 respectively. The state machine has no parser surface (events arrive as typed constants, not strings from the wire), so there is no input domain to fuzz at this layer. Documented under research decision R-008. ✅ |
| AC-10 mapping (supervisor lifecycle, 15 scenarios) | The state-table tests are the unit-test arm of AC-10. Integration coverage of the 15 scenarios end-to-end is owned by SDD-26. ✅ |

**Result:** PASS.

### Principle IX — Idiomatic Go Discipline

| Constraint | Plan compliance |
|---|---|
| Context propagation as first parameter on I/O / cancellable funcs | `NewStore(ctx)` and `Transition(ctx, event)` both take `context.Context` first. The current implementation ignores `ctx` (the store does no I/O), but the parameter is locked into the signature for forward compatibility. `Snapshot()` is a pure read — no `ctx` needed (matches `errors.Is`-style read APIs in the stdlib). ✅ |
| No `Context` stored in struct fields | `Store` has no `ctx` field. ✅ |
| Errors wrapped with `%w`, sentinel `var Err... = errors.New(...)` | `ErrInvalidTransition` is exported, declared via `errors.New`. The error returned from `Transition` wraps the sentinel via `fmt.Errorf("supervise: %w (state=%s event=%s)", ErrInvalidTransition, current, event)` so callers `errors.Is(err, supervise.ErrInvalidTransition)` succeeds and operators reading the printed form see both the state and the event (FR-019-15, SC-019-7). ✅ |
| No globals, no `init()` | Allowed exception: the package-level transition table `var transitions = map[State]map[Event]State{...}` is a sentinel-class read-only `var` — equivalent to the `var Err... = errors.New(...)` exception explicitly tolerated by the constitution. It is **never reassigned** after package initialization. No `init()` function is added. Explicit comment in code declares the table immutable post-construction. ✅ |
| Panic policy — library code returns errors | All public APIs return `error` or a value. No `panic` calls. ✅ |
| Goroutine discipline — every goroutine has an owner and termination | The store starts **zero** goroutines (FR-019-12). The only goroutines that touch the store are caller-owned (SDD-20/21/22). ✅ |
| Interfaces — accept interfaces at consumer | The `Clock` seam (one method, `Now() time.Time`) is defined in this package because it is consumed here; production callers pass a real-time impl, tests pass a fake. Single-method interface, defined at the consumer per the constitution. ✅ |
| Package layout — non-`main` lives under `internal/` | `internal/supervise/` ✅ |
| Modules-only, CGO-disabled | Inherits repo defaults; no new dependency. ✅ |

**Result:** PASS.

### Principle X — Observability & Redaction

| Constraint | Plan compliance |
|---|---|
| Structured logging via `log/slog`, no third-party logger | This chunk emits **no log lines of its own** — observability is the caller's job. The `Snapshot.Token` field implements `slog.LogValuer` indirectly via `*securebytes.SecureBytes.LogValue() → slog.StringValue("[redacted]")` (locked at SDD-02). ✅ |
| Secret redaction is type-driven | `Snapshot.Token *securebytes.SecureBytes` is the type-narrow guarantee. A test (`TestStore_TokenLogValueRedacts`) builds a snapshot with a populated token, passes it to `slog.LogAttrs` against a buffer-backed handler, and asserts the rendered line contains `[redacted]` and zero bytes of the underlying token value. SC-019-3 covers this. ✅ |
| No secret values in errors | `ErrInvalidTransition` carries `(current state, rejected event)` — both are non-secret label types (`State`, `Event` are typed `string`s drawn from a closed set). The sentinel never wraps a `Token` or any byte from one. ✅ |
| Audit log is separate | Audit emission is owned by SDD-21/SDD-13. The state store does not write to any audit channel. ✅ |
| Discord alert tiers | N/A — this chunk emits no alerts. ✅ |
| Metrics over local Unix status socket only | N/A — this chunk does not bind a socket. SDD-22 owns the socket and consumes via `Snapshot()`. ✅ |

**Result:** PASS.

### Other principles — quick clearance

- **I (Zero Files at Rest)**: The store touches no filesystem path. ✅
- **II (Approval is Human)**: The state machine **encodes** the approval requirement (no transition reaches `running` without first traversing `fetching`, and the only ways into `fetching` are `NewStore` or an `EventApprovalGranted`/`EventChildExitClean`/`EventChildExitCrash`/`EventRefreshRequested`). It does not bypass approval. ✅
- **III (Defense in Depth)**: This chunk does not add or weaken a crypto layer. It consumes Layer 5 (`SecureBytes`). ✅
- **VI (Tailscale-Only)**: N/A — no network. ✅
- **VII (CLI Design)**: N/A — internal package, no CLI surface. ✅
- **XI (Native-First, Minimal Deps)**: Stdlib + one internal package. **Zero new direct dependencies.** ✅

**Overall Constitution Check: PASS — no violations, Complexity Tracking section left empty.**

## Project Structure

### Documentation (this feature)

```text
specs/019-supervise-state/
├── plan.md                      # This file (/speckit-plan command output)
├── spec.md                      # Feature spec (already authored by /speckit-specify + /speckit-clarify)
├── research.md                  # Phase 0 output (this command)
├── data-model.md                # Phase 1 output (this command)
├── quickstart.md                # Phase 1 output (this command)
└── contracts/
    ├── go-api.md                # Locked Go signatures for state.go
    ├── state-table.md           # The 5×15 transition matrix transcribed from Scenarios 2..15
    └── test-catalogue.md        # Test → FR/SC mapping (TDD inputs for /speckit-tasks)
```

### Source Code (repository root)

```text
internal/
└── supervise/                   # PRE-EXISTING directory (config/ subpackage already lives here, locked at SDD-18)
    ├── config/                  # SDD-18 — out of scope for this chunk; no edits
    │   ├── config.go
    │   ├── defaults.go
    │   ├── validate.go
    │   ├── errors.go
    │   ├── paths.go
    │   ├── doc.go
    │   ├── config_test.go
    │   ├── validate_test.go
    │   ├── config_fuzz_test.go
    │   └── testdata/
    ├── doc.go                   # NEW — package doc for `package supervise` itself
    ├── state.go                 # NEW — State, Event, Store, Snapshot, transition table, Clock
    └── state_test.go            # NEW — table-driven legal/illegal tests + race + redaction + clock seam
```

**Structure Decision**: Single-binary Go CLI; this chunk seeds `package supervise` with three new files alongside the pre-existing `config/` subpackage. The package directory `internal/supervise/` already exists from SDD-18; SDD-19 introduces the **first file declaring `package supervise`** itself (the existing files declare `package config` inside the subdirectory). `doc.go` carries the package-level godoc; `state.go` is the locked behaviour surface; `state_test.go` is the test arm. The `internal/supervise/` package is consumed by future chunks SDD-20 (`childrunner.go`), SDD-21 (`refresh.go`, `grace.go`), SDD-22 (`status_socket.go`) and SDD-25 (`watchdog.go`) — but those are **out of scope** here.

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

*No violations. Section intentionally empty.*
