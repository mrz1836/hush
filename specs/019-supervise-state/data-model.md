# Phase 1 Data Model — SDD-19 Supervisor State Machine

**Branch**: `019-supervise-state` | **Date**: 2026-05-05

This document captures the **data entities** defined by SDD-19 and
their relationships. The exact Go type signatures live in
`contracts/go-api.md`; this file states the intent, fields,
invariants, and validation rules in a form that survives a Go
rewrite.

## Entities

### `State` — supervisor lifecycle state

A finite, closed enumeration of exactly five values. Every
supervisor process holds exactly one `State` at a time
(FR-019-1, FR-019-2).

| Constant | String | Meaning |
|----------|--------|---------|
| `StateFetching` | `"fetching"` | The supervisor is attempting to obtain or renew a session JWT (claim/refresh/silent-refill). No child is running yet, OR the previous child has exited cleanly and a refill is in flight. |
| `StateRunning` | `"running"` | A session JWT is held, validators have passed, the child has been forked and is alive. |
| `StateAwaitingApproval` | `"awaiting-approval"` | Operator action required: either the most recent claim/refresh failed in a way that requires Discord re-approval (validator failure, exit-78, vault 401-unknown-jti, claim denied, claim unavailable) OR the grace window has elapsed without recovery. |
| `StateGraceRestart` | `"grace-restart"` | Session JWT has expired, the child has crashed, but `cache_grace_ttl` permits a one-time restart from cached secrets. Reachable only from `StateRunning`. |
| `StateStopped` | `"stopped"` | Terminal. Supervisor has been asked to shut down. No further state-changing transitions are accepted (idempotent `EventStopRequested` is the lone exception per R-010). |

**Invariants**:
- The string forms are part of the locked operator-facing contract
  — they appear in status-socket JSON (`"state": "running"`) and
  audit-log records. Renaming requires a SPEC amendment.
- Validation: any `State` value not in the closed set is invalid.
  The state machine itself never produces such a value; defensive
  type narrowing is enforced by the typed string constants.

**Initial value (FR-019-16)**: `StateFetching` on `NewStore`.

**Terminal value (FR-019-17)**: `StateStopped`. Once entered, the
only legal event is the idempotent `EventStopRequested`.

---

### `Event` — lifecycle event

A finite, closed enumeration of exactly 15 values (FR-019-21). Each
event represents a single externally-observed lifecycle occurrence
that may legally drive a state change. The vocabulary is locked at
the spec level; adding/removing requires a SPEC amendment.

| Constant | String | Source state(s) | Destination | Origin scenario |
|----------|--------|-----------------|-------------|-----------------|
| `EventFetchOK` | `"fetch-ok"` | `fetching` | `running` | 2, 3, 4, 7-after-approval, 13 |
| `EventFetchAuthRequired` | `"fetch-auth-required"` | `fetching` | `awaiting-approval` | 7, 9 |
| `EventClaimDenied` | `"claim-denied"` | `fetching` | `awaiting-approval` | (operator denied via Discord) |
| `EventClaimUnavailable` | `"claim-unavailable"` | `fetching` | `awaiting-approval` | 10 |
| `EventValidatorFailed` | `"validator-failed"` | `fetching` | `awaiting-approval` | 6 |
| `EventBootRetryExhausted` | `"boot-retry-exhausted"` | `fetching` | `stopped` | 11 |
| `EventChildExitClean` | `"child-exit-clean"` | `running` | `fetching` | 3 |
| `EventChildExitCrash` | `"child-exit-crash"` | `running` | `fetching` | 4 |
| `EventChildExit78Stale` | `"child-exit-78-stale"` | `running` | `awaiting-approval` | 5 |
| `EventRefreshRequested` | `"refresh-requested"` | `running` | `fetching` | 13 |
| `EventGraceRestartTriggered` | `"grace-restart-triggered"` | `running` | `grace-restart` | 9 (grace cache) |
| `EventGraceRestartOK` | `"grace-restart-ok"` | `grace-restart` | `running` | 9 (grace cache, success) |
| `EventGraceExpired` | `"grace-expired"` | `grace-restart` | `awaiting-approval` | 9 (grace cache, window elapsed) |
| `EventApprovalGranted` | `"approval-granted"` | `awaiting-approval` | `fetching` | 5, 7 (after re-approval) |
| `EventStopRequested` | `"stop-requested"` | any | `stopped` (idempotent) | (operator/host shutdown) |

**Invariants**:
- The string forms are part of the locked audit-log contract.
- Any `Event` value outside this closed set is rejected by
  `Transition` with `ErrInvalidTransition` (the lookup miss in the
  transition table).
- `EventStopRequested` from `StateStopped` is an idempotent
  no-op-success (R-010): state remains `stopped`,
  `LastTransitionAt` is bumped, `Reason` is refreshed.

---

### `Store` — guarded state container

The single in-memory aggregate the rest of `internal/supervise`
consults. Holds the current state, child PID, last-transition
timestamp, cached JWT, and reason. Concurrency-safe via an
internal `sync.RWMutex`.

**Fields (private)**:
| Field | Type | Purpose | Zero value |
|-------|------|---------|-----------|
| `mu` | `sync.RWMutex` | Guards every other field. Held in write mode by `Transition`, in read mode by `Snapshot`. | (zero-usable) |
| `currentState` | `State` | The current lifecycle state. | `StateFetching` after `NewStore` |
| `childPID` | `int` | The PID of the running child if `currentState == StateRunning`; 0 otherwise. The store does not enforce the relationship — SDD-20 sets/clears the PID via internal helpers. | `0` |
| `lastTransitionAt` | `time.Time` | Wall-clock timestamp of the most recent successful transition, sourced from the injected `Clock`. | `clock.Now()` at `NewStore` |
| `token` | `*securebytes.SecureBytes` | The cached supervisor session JWT. `nil` when no session is held (e.g. in `StateFetching` before first claim). | `nil` |
| `reason` | `string` | Human-readable phrase describing the most recent transition. Set deterministically from the event-to-phrase map. | locked initial phrase per R-005 |
| `clock` | `Clock` | Injected at `NewStore` time; used to stamp `lastTransitionAt`. Never reassigned post-construction. | (required, must be non-nil) |

**Invariants**:
- `Store` is never copied by value (the embedded `sync.RWMutex`
  forbids this — `go vet` flags it). Always passed as `*Store`.
- `clock` is set exactly once at construction and never changes
  for the lifetime of the store.
- `currentState` is only ever assigned a value that appears in the
  closed `State` set; the table lookup guarantees this.
- `lastTransitionAt` is monotonic per session (each successful
  transition assigns a `clock.Now()` value; production clock is
  monotonic-aware via `time.Now()`'s monotonic reading).

---

### `Snapshot` — defensive-copy point-in-time view

A by-value struct returned from `Store.Snapshot()`. Carries
exactly the fields a downstream reader (status socket, audit
emitter, debug log) needs.

**Fields (public)**:
| Field | Type | Notes |
|-------|------|-------|
| `State` | `State` | The current state at snapshot time. |
| `ChildPID` | `int` | The cached child PID; `0` when no child is running. |
| `LastTransitionAt` | `time.Time` | The store-stamped timestamp of the most recent transition. |
| `Token` | `*securebytes.SecureBytes` | Pointer-copy of the store's cached JWT. **Never** the underlying bytes. May be `nil`. Renders `[redacted]` via `LogValue` (Constitution X). |
| `Reason` | `string` | The deterministic event-to-phrase mapping output for the most recent transition. |

**Invariants**:
- The returned `Snapshot` is a value, not a pointer (R-006). Caller
  may freely mutate any field of their local copy without affecting
  the store (FR-019-8).
- The `Token` field, if non-`nil`, points to the same `SecureBytes`
  the store holds. The contract on `*SecureBytes` (locked at
  SDD-02) guarantees the caller cannot extract bytes — only borrow
  via `Use(fn)` — so pointer-sharing does not violate the
  defensive-copy intent. If the store later `Destroy`s the token,
  in-flight readers calling `Use(fn)` see `ErrDestroyed`, never a
  panic, never a partial buffer.
- A snapshot taken concurrently with a transition observes either
  the pre-state or the post-state in full (FR-019-14, "no torn
  read across a transition boundary"). Enforced by the RWMutex
  ordering: `Transition` holds the write lock for the entire
  field-write block; `Snapshot` holds the read lock for the
  entire field-read block.

**Anti-fields (do NOT add)**:
- `Use bool` (no use-counter — supervisor sessions are TTL-only,
  Constitution IV).
- `RawTokenBytes []byte` (would defeat redaction).
- A back-pointer to `*Store` (would smuggle the lock out).

---

### `Clock` — pluggable time source

A single-method interface defined in this package and consumed by
the `Store`. Production wires a real-time impl backed by
`time.Now()`; tests wire a fake.

**Method**:
- `Now() time.Time` — returns the current wall-clock instant the
  store should record on the next transition commit.

**Invariants**:
- `Clock` is **not** stored in `Snapshot`. Only the produced
  `time.Time` value travels into snapshots.
- `nil` `Clock` passed to `NewStore` is a programmer error caught
  at construction (Constitution IX explicit-panic exemption for
  startup wiring).

---

### `ErrInvalidTransition` — sentinel error

A package-level `var`, declared via `errors.New(...)`. Returned
(wrapped) from `Transition` when no edge exists for the
`(currentState, event)` pair, when the event is outside the
closed set, or when both.

**Contract**:
- Identifiable via `errors.Is(err, supervise.ErrInvalidTransition)`.
- The wrapped form names both the current state and the rejected
  event: `fmt.Errorf("supervise: %w (state=%s event=%s)", ErrInvalidTransition, current, event)`.
- The store's internal fields are unchanged after a rejected call
  (FR-019-6).

---

## Relationships

```text
   ┌─────────────────────────────────┐
   │        package supervise        │
   │                                 │
   │   var transitions  ← read-only  │
   │   var reasons      ← read-only  │
   │   var ErrInvalidTransition      │
   │                                 │
   │   ┌──────────┐                  │
   │   │  Store   │  (RWMutex)       │
   │   │   ├ currentState : State    │
   │   │   ├ childPID     : int      │
   │   │   ├ lastAt       : Time     │
   │   │   ├ token        : *SB ─────┼────► internal/vault/securebytes
   │   │   ├ reason       : string   │       (LogValuer; Use; Destroy)
   │   │   └ clock        : Clock ───┼────► realClock or fakeClock
   │   └──────────┘                  │
   │        │                        │
   │        │ Snapshot()             │
   │        ▼                        │
   │   ┌──────────┐                  │
   │   │ Snapshot │  (value type)    │
   │   │   ├ State            │     │
   │   │   ├ ChildPID         │     │
   │   │   ├ LastTransitionAt │     │
   │   │   ├ Token   *SB  ────┼─────┼────► same SB as store (borrow-only)
   │   │   └ Reason  string   │     │
   │   └──────────┘                  │
   └─────────────────────────────────┘
                │
                │ consumed by (downstream chunks)
                ▼
       SDD-20 child runner
       SDD-21 refill/refresh/grace
       SDD-22 status socket
       SDD-25 watchdog (read-only)
```

## State transitions (matrix)

The full transition matrix lives in `contracts/state-table.md`.
It is the single source of truth for what `transitions[*][*]` must
populate; any deviation is a constitution-level violation
(SC-019-1, FR-019-4).

## Validation rules

| Rule | Source | Enforcement point |
|------|--------|-------------------|
| Initial state is `StateFetching` | FR-019-16 | `NewStore` body |
| Exactly one current state at a time | FR-019-2 | RWMutex; transitions are commit-style |
| Closed event vocabulary (15 events) | FR-019-21 | Table lookup miss → `ErrInvalidTransition` |
| Closed state vocabulary (5 states) | FR-019-1 | Typed `State` constants; only the table can produce a new value |
| Illegal `(state, event)` rejected with named error | FR-019-5 | Wrapped `ErrInvalidTransition` from `Transition` |
| Rejected transition leaves store unchanged | FR-019-6 | Lookup happens **before** any field write; the field-write block is reached only on a successful lookup |
| `EventStopRequested` from `StateStopped` is idempotent | FR-019-17 | Explicit table entry: `transitions[StateStopped][EventStopRequested] = StateStopped` |
| Snapshot is defensive copy | FR-019-8 | By-value return; pointer-copied `*SecureBytes` enforced by SDD-02 contract |
| Token redacts in slog output | FR-019-9, Constitution X | `*SecureBytes.LogValue() → slog.StringValue("[redacted]")` (SDD-02 lock) |
| Token bytes are borrow-only | FR-019-10 | `*SecureBytes.Use(fn)` is the only read path (SDD-02 lock) |
| Token zeroed on release | FR-019-11 | Store calls `Destroy()` post-swap (R-007) |
| `LastTransitionAt` from injected clock at commit time | FR-019-20 | `s.lastTransitionAt = s.clock.Now()` inside the write-lock block |
| `Reason` derived deterministically from event | FR-019-7 (Clarification 1) | `s.reason = reasons[event]` inside the write-lock block |
| No goroutines | FR-019-12 | Code review + `TestStore_NoSideEffects` checks `runtime.NumGoroutine()` delta is zero across a full lifecycle |
| No I/O | FR-019-13 | Code review; no stdlib I/O imports beyond `time` and `log/slog` typed values |
| Race-clean | FR-019-14, SC-019-2 | `TestStore_ConcurrentTransitionAndSnapshot` under `go test -race`, 100 iterations |

## Out of scope

- Persistence: `Store` is in-memory only. Recovery on supervisor
  restart is upstream of this layer.
- Audit emission: SDD-21/SDD-13.
- Status socket: SDD-22.
- Token issuance: SDD-07 + SDD-21.
- PID-file/flock duplication guard: upstream of `Store` (Scenario 14).
