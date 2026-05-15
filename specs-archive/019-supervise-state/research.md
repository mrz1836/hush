# Phase 0 Research — SDD-19 Supervisor State Machine

**Branch**: `019-supervise-state` | **Date**: 2026-05-05

The Technical Context in `plan.md` carries no `NEEDS CLARIFICATION`
markers (the spec already absorbed Clarifications 1–5 from
2026-05-05, and the chunk doc locks every HOW knob). Phase 0 here
records the resolved decisions so downstream phases (`/speckit-tasks`,
`/speckit-implement`) and reviewers can verify each choice against
the constitution and the SDD-19 contract without re-deriving them.

Each entry uses the standard form: **Decision** / **Rationale** /
**Alternatives considered**.

---

## R-001 — Concurrency primitive: `sync.RWMutex`, not `sync.Mutex` and not channels

**Decision**: A single `sync.RWMutex` on `Store` guards all internal
fields. `Transition` takes the write lock; `Snapshot` takes the read
lock. No channels, no goroutines, no `atomic.*` types.

**Rationale**:
- The status socket (SDD-22) will issue `Snapshot()` on every
  `GET /status` call. Snapshots are read-mostly relative to
  transitions, so RWMutex matches the access pattern without
  measurable cost over plain `Mutex` for the small critical
  sections involved.
- Channels would imply at least one goroutine to drain them, which
  Constitution IX requires to have an owner and termination
  condition. Owning zero goroutines (FR-019-12) is an explicit
  goal and a much stronger guarantee than any channel-based
  design could offer.
- `atomic.Value` storing a `Snapshot` pointer was considered but
  rejected because the **multi-field commit** (state + childPID +
  reason + lastTransitionAt + token pointer) must be atomic as a
  group. A single `atomic.Pointer[Snapshot]` works for that, but
  then `Transition` becomes a load-modify-store cycle that has to
  re-validate the table under contention. RWMutex is simpler and
  the read path's allocation cost is identical (snapshot is copied
  out either way per FR-019-8).

**Alternatives considered**:
- Plain `sync.Mutex` — works correctly but penalises the
  read-heavy snapshot path with no upside.
- `atomic.Pointer[snapshot]` with copy-on-write transitions —
  doable but sets a precedent for lock-free state transitions
  that the team has not asked for and that complicates table
  validation under contention.
- A goroutine + channel "actor" model — explicitly forbidden by
  FR-019-12 / FR-019-13.

---

## R-002 — Transition table representation: `map[State]map[Event]State`

**Decision**: Package-level `var transitions = map[State]map[Event]State{...}`,
populated as a single literal at package-load time, never mutated
after. Lookup is `next, ok := transitions[current][event]`; on
`!ok`, return `ErrInvalidTransition` wrapped.

**Rationale**:
- Direct, obvious, and Go-idiomatic. `O(1)` lookup. No third-party
  state-machine library (Constitution XI, native-first).
- The table literal is the **diagram-as-code** for Scenarios 2–15:
  a reviewer can read it line-by-line and check it against
  `docs/LIFECYCLE-SCENARIOS.md` without simulator output.
- A package-level `var` populated by a literal is allowed under
  Constitution IX's "no globals" rule on the same footing as
  `var ErrFoo = errors.New(...)` — it is sentinel-class read-only
  state. The plan's Constitution Check section makes this exception
  explicit. A `// transitions is read-only after package init; do
  not mutate.` comment seals the intent.

**Alternatives considered**:
- `[][]State` matrix indexed by ordinals — denser, but loses
  source-readability and forces every reviewer to translate
  ordinals into names.
- A sealed library (e.g. `looplab/fsm`) — adds a dependency for no
  benefit; Constitution XI requires written justification for new
  deps and there is no stdlib gap to fill.
- A `func(State, Event) (State, bool)` — equivalent in semantics,
  but loses the at-a-glance reviewable table form.

---

## R-003 — Initial state: `StateFetching` (FR-019-16)

**Decision**: `NewStore(ctx)` returns a store whose `currentState`
is `StateFetching`, `LastTransitionAt` is `clock.Now()` at
construction, `ChildPID` is `0`, `Token` is `nil`, `Reason` is the
literal phrase `"store constructed"` (or `"awaiting initial claim"`
— locked in `contracts/go-api.md`).

**Rationale**:
- Spec FR-019-16 mandates `fetching` as the initial state.
- Scenarios 2 (first daemon bootstrap) and 11 (Tailscale not ready)
  both begin with the supervisor in `fetching` before any claim
  has been attempted. Any other initial state would diverge from
  the diagrams.
- Setting `LastTransitionAt` at construction (rather than zero
  value) avoids the "snapshot of an empty store" edge case in the
  spec's Edge Cases section.

**Alternatives considered**:
- A separate `StateInitialising` value — rejected; the five-state
  vocabulary is closed (FR-019-1) and adding a sixth requires a
  SPEC amendment.
- Letting `LastTransitionAt` be zero on first construction — would
  surface as `0001-01-01T00:00:00Z` in the status socket JSON,
  which operators read as a bug.

---

## R-004 — Clock seam: minimal `Clock` interface, defined in this package

**Decision**: This package defines

```go
type Clock interface { Now() time.Time }
```

Production wires a concrete `realClock struct{}` whose `Now()`
returns `time.Now()`. Tests pass a fake (e.g. a `fakeClock` whose
`Now()` returns a controllable `time.Time`). `NewStore` accepts
the clock as a parameter; we lock the signature as
`NewStore(ctx context.Context, clock Clock) *Store` and document
that passing `nil` is a programmer error caught by an explicit
nil-check returning a panic at construction (the only allowed
panic per Constitution IX, since this is a startup-wiring
invariant).

**Rationale**:
- FR-019-20 mandates an injected clock.
- Constitution IX requires interfaces to be defined at the
  consumer and prefers single-method interfaces — both honoured.
- Defining the interface in this package (rather than in
  `internal/testutil` or as a stdlib `interface{ Now() time.Time }`
  used inline) keeps the public API self-describing: a downstream
  reader of `state.go` sees the contract without indirection.

**Alternatives considered**:
- Reading `time.Now()` directly inside `Transition` — fails
  FR-019-20.
- A package-level `var nowFunc = time.Now` swappable in tests —
  works, but introduces a mutable package-level global for the
  sake of test seams, contradicting Constitution IX. Injection
  via `NewStore` is the documented pattern.
- Pulling in a third-party clock library — Constitution XI bars
  it; the stdlib `time` plus a one-method interface suffices.

---

## R-005 — Reason field: deterministic event → phrase mapping (Clarification 1)

**Decision**: A package-level `var reasons = map[Event]string{...}`
maps every event in the closed vocabulary to a human-readable
phrase. The store reads this map at transition-commit time and
stores the resulting `Reason` on the snapshot. `Transition` does
NOT accept a caller-supplied reason. The phrase set is locked in
`contracts/go-api.md`.

**Rationale**:
- Spec Clarification 1 mandates store-derived `Reason`.
- A closed map (a) gives operators consistent wording across the
  status socket and audit log, (b) makes wording amendments a
  single-file change with a test asserting completeness, and (c)
  prevents callers from leaking secret material through a free-form
  reason field.
- The `EventStopRequested` event maps to the phrase `"stop requested"`
  even when applied to an already-`stopped` store (idempotent
  re-entry per FR-019-17); the `Reason` field is updated and
  `LastTransitionAt` is bumped to reflect the renewed stop request,
  but the state remains `stopped`.

**Alternatives considered**:
- Caller-supplied reason text — rejected by Clarification 1
  because it diffuses the wording surface and risks secret leakage
  through a free-form string.
- Empty `Reason` (let the consumer compose the phrase) — rejected
  because consistency across status-socket and audit emission is
  the goal; a single source of phrases is simpler to test.

---

## R-006 — Snapshot definition: value type with copied scalars + pointer-shared `*SecureBytes`

**Decision**: `Snapshot` is a struct **value** (not a pointer). All
scalar fields (`State`, `ChildPID`, `LastTransitionAt`, `Reason`)
are simple values copied at snapshot time. The `Token` field is a
`*securebytes.SecureBytes` whose pointer is copied — the underlying
mlocked buffer is **not** copied (Constitution III/X — borrow-only
secret access). Defensive-copy semantics (FR-019-8) are satisfied
because the caller can reassign or zero any **field of the
returned value** without touching the store; the only thing they
share with the store is a `*SecureBytes` whose API exposes only
borrow-via-`Use(fn)`, never extraction.

**Rationale**:
- Returning a value rather than a pointer (a) makes copy semantics
  visible at the call site, (b) eliminates a reachability question
  about whether the caller can mutate the store via the returned
  pointer, and (c) costs negligible extra bytes (5 fields + a
  pointer).
- The `*SecureBytes` is the only field whose underlying memory the
  store needs to share — and that sharing is **safe by
  construction** of `SecureBytes` itself (locked at SDD-02): the
  caller can read inside `Use(fn)` but cannot extract bytes, and
  `Destroy()` is idempotent so the store can release a stale
  token without affecting an in-flight reader (the reader sees
  `ErrDestroyed` cleanly).

**Alternatives considered**:
- Returning `*Snapshot` — would force a documentation note about
  ownership; the value form is self-describing.
- Cloning `*SecureBytes` per snapshot via a hypothetical
  `securebytes.Clone(...)` — would force an mlock allocation per
  status-socket call (potentially many per second under
  monitoring). The borrow-only contract makes the clone
  unnecessary.

---

## R-007 — Token replacement and zeroing semantics

**Decision**: When a transition would replace the cached token
(e.g. a future `EventFetchOK` carrying a fresh JWT — see R-009 for
the **token-write seam**), the store calls `Destroy()` on the
previously-held `*SecureBytes` **after** the transition's
write-lock-protected swap. `Destroy` is idempotent (locked at
SDD-02). The transition that *clears* the token without replacing
(e.g. `EventStopRequested` from a state that held a token) likewise
calls `Destroy` on the outgoing token and sets `Token` to `nil`.

**Rationale**:
- FR-019-11 mandates zeroing on release.
- `Destroy` after the swap (not before) preserves the invariant
  that a snapshot taken concurrently with the transition either
  sees the old token (still live) or the new one — never an
  in-flight half-state. The only window where a snapshot could see
  a `Destroy`'d pointer is one that was acquired before the
  transition began; per the `SecureBytes` contract, that
  snapshot's `Use(fn)` returns `ErrDestroyed` cleanly rather than
  panicking.

**Alternatives considered**:
- `Destroy` before swap — opens a window where a concurrent
  snapshot under the read-lock would see a destroyed token. Lock
  ordering prevents the race, but the post-swap discipline is
  simpler to audit.
- Never `Destroy` (rely on GC + finalizer) — `SecureBytes` does
  install a finalizer (per SDD-02), so this is safe in steady
  state, but explicit `Destroy` shortens the window where mlocked
  memory holds a dead JWT.

---

## R-008 — No new fuzz target at this layer

**Decision**: SDD-19 introduces no new fuzz target. The mandatory
fuzz catalogue (Constitution VIII §6) lists six targets; SDD-19
maps to none of them.

**Rationale**:
- Fuzzing is required for "parsers and crypto entry points"
  (Constitution VIII). The state machine has no parser surface:
  events arrive as typed Go constants from the `Event` constant
  set, not as bytes from the wire. A caller passing an unknown
  `Event` value (e.g. `Event("garbage")`) is exercised by
  table-driven negative tests asserting `ErrInvalidTransition`,
  not by fuzz.
- Fuzz #5 (supervisor TOML) is owned by SDD-18 (already shipped).
- Fuzz #6 (status socket JSON) belongs to SDD-22 (downstream).
- Fuzz #4 (request signature) belongs to SDD-08.

**Alternatives considered**:
- Inventing a "transition fuzz" that fires random `Event` strings
  from `[]byte` corpus — adds noise without uncovering bugs the
  table-driven negative suite doesn't already cover.

---

## R-009 — Token-write seam: a separate `SetToken` method, not piggy-backed on `Transition`

**Decision**: This chunk's locked API is `Transition(ctx, event)`
which takes only an `Event`. A separate write seam for the cached
JWT is **deferred to SDD-21** (refill/refresh/grace) — the chunk
that owns fetch-success handling. SDD-19 ships the `Token` field
on the `Store` struct (with all locking and zeroing rules
encoded), but the only caller path that *sets* it in this chunk
is internal-only and exposed via an unexported method that
SDD-21's wired code can call from inside the same package.

For SDD-19's scope, the publicly-locked surface is:
`NewStore`, `Transition`, `Snapshot`, the closed `Event` set,
`ErrInvalidTransition`, `Clock`, `State` constants. The token-write
seam is not part of this chunk's public contract.

**Rationale**:
- Locking the token-write seam in SDD-19 would freeze the
  signature before SDD-21 has produced its refill state machine,
  inviting a PACKAGE-MAP churn. Deferring keeps the SDD-19 surface
  minimal and the contract auditable.
- All five User Stories in spec.md can be satisfied without a
  public `SetToken` method: tests can construct a `Store` and use
  unexported helpers via `_test.go` files in the same package
  (a standard Go testing pattern; not a privacy violation because
  no consumer outside the package has a need to set the token at
  this stage).
- The `Token` field is non-nil-tested for redaction and zeroing
  (SC-019-3, FR-019-11) using a test-only path inside
  `state_test.go`.

**Alternatives considered**:
- Locking `SetToken(ctx, *SecureBytes)` now — would freeze the
  contract before SDD-21's needs are known.
- Carrying the token as an `Event` payload — events are typed
  string constants; bolting a payload on changes the
  table-lookup model and complicates the spec's "fixed event set"
  guarantee (FR-019-3).

---

## R-010 — Idempotent stop and re-entry semantics

**Decision**:
- `EventStopRequested` is legal from **every** state, including
  `stopped` (FR-019-17). When applied to an already-`stopped`
  store, it succeeds, leaves `currentState == StateStopped`,
  bumps `LastTransitionAt`, and refreshes `Reason` to the locked
  `EventStopRequested → reasons[EventStopRequested]` phrase.
- `EventGraceRestartTriggered` and `EventGraceRestartOK` may
  legally re-enter `running ↔ grace-restart` repeatedly within
  the same session (Clarification 3). The state machine imposes
  no per-session counter; the upper bound (`cache_grace_ttl`) is
  enforced upstream by SDD-21's grace timer, which fires
  `EventGraceExpired` to drive `grace-restart → awaiting-approval`.

**Rationale**:
- Spec FR-019-17 explicitly allows the idempotent stop; the
  alternative ("`EventStopRequested` from `stopped` returns
  `ErrInvalidTransition`") would force every caller to query
  state before stopping, defeating the purpose of an idempotent
  operation.
- Grace re-entry is essential for Scenario 9 (overnight expiry
  with grace cache): a second crash within the grace window must
  re-arm the cached-secret restart path without producing a fresh
  Discord page.

**Alternatives considered**:
- Treating `EventStopRequested` from `stopped` as illegal —
  rejected; defeats idempotence.
- Capping grace re-entries with a per-session counter inside the
  state machine — rejected by Clarification 3; the timer upstream
  is the sole bound.

---

## R-011 — Test fixtures are inline; no new `internal/testutil` helpers

**Decision**: All tests live in `internal/supervise/state_test.go`.
Fakes (e.g. `fakeClock`) are unexported types declared in the
test file, mirroring SDD-18's pattern. Token-bearing tests
construct a `*securebytes.SecureBytes` via the locked SDD-02
constructor `securebytes.New([]byte{...})` and `Destroy` it in
test cleanup.

**Rationale**:
- Locking the test surface to one file mirrors the SDD-18 chunk
  contract ("no new `internal/testutil` helpers"). Future chunks
  (SDD-20+) can refactor shared helpers if reuse pressure
  emerges; this chunk does not pre-bake an abstraction.
- Inline fakes keep the test-file self-contained and reviewable.

**Alternatives considered**:
- Adding a `internal/testutil/clock.go` fake — defers a useful
  helper to a chunk that has only one consumer; YAGNI.
- A package-level `nowFunc var` swap in tests — rejected in R-004.

---

## Summary

All `NEEDS CLARIFICATION` markers from the spec have been
resolved (Clarifications 1–5 are part of the spec; R-001 through
R-011 record HOW decisions). No new third-party dependencies,
no new fuzz targets, no goroutines, no side-effects. The chunk
is ready for Phase 1 design.
