# Feature Specification: Supervisor State Machine + Snapshot Store

**Feature Branch**: `019-supervise-state`
**Created**: 2026-05-05
**Status**: Draft
**Input**: User description: "internal/supervise state machine: five states (fetching, running, awaiting-approval, grace-restart, stopped); table-driven transitions from a fixed event set; illegal transitions rejected with named error; Snapshot returns a defensive copy; cached JWT is SecureBytes-wrapped; no goroutines, no side-effects in this layer"

## Clarifications

### Session 2026-05-05

- Q: How is the snapshot's `Reason` field populated — derived from the event by the store, or supplied by the caller via a `Transition` argument? → A: Store derives `Reason` deterministically from the event via a closed map (event → human-readable phrase). The `Transition(ctx, event)` signature carries no caller-supplied reason text.
- Q: When the operator re-approves a fresh session out of `awaiting-approval`, does the supervisor pass back through `fetching` or jump straight to `running`? → A: Two-step. `EventApprovalGranted` drives `awaiting-approval → fetching`; a separate `EventFetchOK` then drives `fetching → running`. Mirrors first-bootstrap flow (Scenario 2); preserves single-purpose events and lets validator/fetch failures mid-refetch be expressed without composite events.
- Q: May `grace-restart` be re-entered more than once within a single session? → A: Yes. Re-entry is permitted as long as `cache_grace_ttl` has not elapsed; the grace-window timer (owned upstream by SDD-21's grace logic) is the sole bound. The state machine itself imposes no per-session counter; once the window expires, the existing `grace-restart → awaiting-approval` transition fires as before.
- Q: Where does the snapshot's last-transition timestamp come from — a hard-coded clock, an injected clock seam, or caller-supplied? → A: Injected clock seam. The store accepts a clock interface at construction time (production passes a real-time clock backed by `time.Now()`; tests pass a controllable fake). The transition primitive itself takes no timestamp argument; the store reads the current time from the injected clock at transition commit. Tests can assert exact timestamp values via the fake clock; production behaviour is unaffected.
- Q: Should the closed event vocabulary be enumerated in the spec or deferred to the plan/implementation? → A: Spec encodes the closed event vocabulary explicitly as a normative list, derived line-by-line from Scenarios 2-15 in `docs/LIFECYCLE-SCENARIOS.md`. The list is locked at spec level so SC-019-1 ("100% of cells exercised") has a testable denominator and downstream chunks (SDD-20/21/22) cannot drift the vocabulary unilaterally.

## Overview

The supervisor state machine and snapshot store are the single in-memory
source of truth for what a `hush supervise` daemon is currently doing. It
holds the supervisor's lifecycle state (fetching / running / awaiting
approval / grace-restart / stopped), the cached session JWT, and the
metadata operators inspect through `hush client status`. It owns NO
goroutines and triggers NO side-effects (no fetch, no spawn, no signal,
no Discord call) — those layers are downstream consumers (child
fork/exec, refill/refresh/grace, status socket). This chunk is the
guarded data model the rest of `internal/supervise` builds on.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Single source of truth for supervisor lifecycle (Priority: P1)

A supervisor instance MUST always be in exactly one of five named
lifecycle states. Every other component in `internal/supervise` (child
runner, refill/refresh/grace logic, status socket) reads its current
understanding of the supervisor from this store, so they cannot disagree
about what is happening.

**Why this priority**: Without one authoritative state model, two
sibling layers (e.g. child-runner and refresh-scheduler) can race to
make contradictory decisions — silently relaunching a child while
awaiting approval, or marking a session healthy while it is actually
stale. Every scenario in `docs/LIFECYCLE-SCENARIOS.md` assumes a single
state value the rest of the system honours.

**Independent Test**: Construct a fresh store, drive it through each
of the 15 lifecycle scenarios in `docs/LIFECYCLE-SCENARIOS.md` by
issuing the events the scenario describes, and assert at every step
that the observed state matches the scenario's diagram. Test passes if
every scenario lands the store in the documented state with no
detected ambiguity.

**Acceptance Scenarios**:

1. **Given** a fresh supervisor, **When** the store is created, **Then** its initial state is `fetching` (the supervisor begins by attempting to obtain a session before any child runs).
2. **Given** a supervisor in `fetching` and a successful claim/approval+secret-fetch+validator-pass sequence, **When** the corresponding success event is applied, **Then** the state becomes `running`.
3. **Given** a supervisor in `running` and a child exit with the stale-credential exit code (78), **When** the corresponding stale-exit event is applied, **Then** the state becomes `awaiting-approval`.
4. **Given** a supervisor in `running` whose session has expired and whose child has crashed while the grace cache is enabled, **When** the grace-restart event is applied, **Then** the state becomes `grace-restart`.
5. **Given** a supervisor in any state, **When** a stop event is applied, **Then** the state becomes `stopped` and no further transitions are accepted.

---

### User Story 2 — Illegal transitions rejected with a distinct, named error (Priority: P1)

Callers (the child runner, refresh scheduler, watchdog) submit lifecycle
events to the store. Any combination of (current state, incoming event)
that is not in the documented state table MUST be rejected with a
distinct, named error. The store's existing state and metadata MUST NOT
change as a result of a rejected event.

**Why this priority**: Silent acceptance of an illegal transition is
the canonical way state machines drift into "impossible" states.
Treating "is this transition allowed?" as the store's job — rather
than every caller's job — is what makes the supervisor auditable
and what makes future principals (`hush client status`) trustable.

**Independent Test**: For every (state, event) pair NOT present in
the state table derived from `docs/LIFECYCLE-SCENARIOS.md`, attempt
the transition on a freshly-prepared store and assert (a) the call
returns the named "invalid transition" error, (b) the post-call state
is identical to the pre-call state, (c) the error identifies both the
current state and the rejected event so an operator reading a log can
see what was attempted.

**Acceptance Scenarios**:

1. **Given** a supervisor in `stopped`, **When** any event other than a stop event is submitted, **Then** the call returns the named invalid-transition error and the state remains `stopped`.
2. **Given** a supervisor in `awaiting-approval`, **When** a child-exit event is submitted (the child cannot exit because no child is running), **Then** the call returns the named invalid-transition error and the state remains `awaiting-approval`.
3. **Given** a supervisor in `running`, **When** an "approval granted" event is submitted out of band (no approval was pending), **Then** the call returns the named invalid-transition error and the state remains `running`.
4. **Given** a supervisor in `fetching`, **When** a "grace cache hit" event is submitted (grace is only meaningful after a child has been running), **Then** the call returns the named invalid-transition error and the state remains `fetching`.

---

### User Story 3 — Snapshot accessor returns a defensive copy (Priority: P1)

External readers (the status socket, log emitters, future debug
endpoints) need a consistent view of the supervisor's state and
metadata at a point in time. They MUST receive a copy that they can
read without holding any lock against the store, and any mutation
they perform on the returned snapshot MUST NOT mutate the underlying
store.

**Why this priority**: The status socket is the operator's primary
window into the daemon (Constitution V — "staleness is visible").
If readers hold a reference into mutable internal state, either (a)
the store has to lock during the entire socket response (blocking
transitions), or (b) two readers see torn state. Defensive copying
sidesteps both.

**Independent Test**: Acquire a snapshot from a populated store. Mutate
every field of the returned snapshot value (state field, child-PID
field, reason field, slice fields if any). Read a second snapshot from
the store and assert it is bit-for-bit identical to the original
pre-mutation snapshot — i.e. the first reader's mutation had zero
effect on the store.

**Acceptance Scenarios**:

1. **Given** a populated store, **When** a caller acquires a snapshot and modifies the snapshot's state field, **Then** a subsequently-acquired second snapshot still reflects the store's true state.
2. **Given** a populated store, **When** a caller acquires a snapshot and modifies any slice or composite field on it, **Then** the store's internal copy of that field remains unchanged.
3. **Given** a store undergoing concurrent transitions and snapshot reads, **When** the race detector is enabled, **Then** no data race is reported and every observed snapshot is internally self-consistent (the (state, child-PID, last-transition-time) triple is from a single point in time, not torn across a transition).

---

### User Story 4 — Cached session JWT is held in redacted form (Priority: P1)

The supervisor holds the active session JWT in its store so that
downstream layers (refill, refresh, status) can use it without
re-fetching. The JWT is a bearer token — possession equals authority.
The store MUST hold it in a form that (a) cannot be rendered to a
log line in plaintext under any code path, (b) zeros itself when the
supervisor releases it, (c) is exposed through a borrow-only access
pattern rather than handed out as a copyable byte slice.

**Why this priority**: Constitution Principle X mandates type-driven
redaction — "a developer cannot forget to redact a value because the
type itself refuses to render in plaintext". The store is the most
likely place a future log line accidentally captures the snapshot,
and the JWT is the most consequential thing in that snapshot.

**Independent Test**: Place a snapshot containing the cached token
into a structured-log call and capture the emitted log line. Assert
the log line contains the literal redaction marker `[redacted]` and
does NOT contain any byte from the underlying token value. Repeat
the assertion for both the snapshot value and the token field
read in isolation.

**Acceptance Scenarios**:

1. **Given** a snapshot containing a cached token, **When** the snapshot is logged via the project's structured logger, **Then** the rendered log line contains `[redacted]` in place of the token and contains no byte of the token's value.
2. **Given** a caller holding a snapshot, **When** they attempt to read the token's bytes, **Then** access is via a borrow-only mechanism (the bytes cannot be copied into a long-lived caller buffer through the snapshot API alone).
3. **Given** the supervisor releases the cached token (e.g. on shutdown or session change), **When** the redacted holder is destroyed, **Then** the token's underlying memory is zeroed.

---

### User Story 5 — State model owns no goroutines and triggers no side-effects (Priority: P2)

The state machine layer is a pure data model. Submitting an event
MUST NOT start a goroutine, MUST NOT initiate a network call, MUST NOT
fork a child, MUST NOT signal a process, and MUST NOT touch a file or
socket. It updates state, possibly updates metadata, and returns. All
side-effectful work belongs to the layers above (child runner, refill
scheduler, status socket).

**Why this priority**: Mixing side-effects into the state machine is
how supervisors become un-testable. Constitution Principle IX requires
every goroutine to have a clear owner and a documented termination
condition; the cleanest way to satisfy that here is to own zero
goroutines and let the chunks that DO own goroutines manage them
explicitly.

**Independent Test**: Drive the store through every documented event
under a test harness that fails on any goroutine spawn, network call,
process spawn, or filesystem write attributable to the state package.
The full lifecycle suite must complete with zero such side-effects.

**Acceptance Scenarios**:

1. **Given** a freshly-constructed store, **When** any event is applied, **Then** no new goroutine is spawned by the state package itself.
2. **Given** any state transition, **When** the transition completes, **Then** no network connection has been opened, no child process has been forked, no signal has been delivered, and no file or socket has been written by the state package.
3. **Given** a long sequence of transitions, **When** the sequence completes, **Then** the state package has not registered any background timer, ticker, or finaliser of its own (any timer the JWT holder owns is part of that holder's lifecycle, not the state model's).

---

### Edge Cases

- **Repeated events**: What happens when the same event is applied
  twice in a row? Idempotent events (e.g. a second `stop`) MUST be a
  no-op and return success; non-idempotent repeats (e.g. two consecutive
  approval-granted events without an intervening fetch failure) MUST be
  rejected as illegal transitions.
- **Snapshot of an empty store**: A snapshot taken before any event has
  been applied MUST return a well-defined zero-value-but-self-consistent
  view (initial state is `fetching`, no child PID, no token, last
  transition time set to the construction time).
- **Concurrent reader during transition**: A snapshot acquired at the
  exact moment a transition is committing MUST return either the
  pre-transition or the post-transition view in full — never a torn
  combination of the two.
- **Token field unset**: A snapshot taken in `fetching` (before any
  session has been established) has no cached token. The redacted
  field MUST still render safely (`[redacted]` or an explicit
  "no token" marker — never a panic, never a nil-deref, never a
  partially-zeroed byte slice).
- **State after `stopped`**: Once the supervisor has reached `stopped`,
  no further events are accepted. A subsequent stop event MUST be a
  no-op success; any other event MUST be rejected with the named
  invalid-transition error.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-019-1**: The state machine MUST recognise exactly five lifecycle states, named (in human-readable form) `fetching`, `running`, `awaiting-approval`, `grace-restart`, and `stopped`. No additional state values are valid.
- **FR-019-2**: The store MUST always be in exactly one state at a time. There is no "in-between" state during a transition observable through the store's accessors.
- **FR-019-3**: The store MUST expose a single transition primitive that takes one event from a fixed, named event set and updates the current state per the documented state table.
- **FR-019-4**: The state table MUST cover every transition implied by Scenarios 2 through 15 in `docs/LIFECYCLE-SCENARIOS.md`. No scenario diagram may require a transition the table omits, and no transition the table allows may contradict a scenario diagram.
- **FR-019-5**: Every (current state, incoming event) pair NOT present in the state table MUST be rejected with a distinct, named error sentinel. Callers MUST be able to distinguish this error from any other error condition by identity (not by message string matching).
- **FR-019-6**: A rejected (illegal) transition MUST leave the store unchanged: the state, child PID, last-transition timestamp, cached token, and reason field MUST all be identical before and after the rejected call.
- **FR-019-7**: The store MUST expose a `Snapshot` accessor that returns a value carrying at minimum: the current state, the child process identifier (if any), the last-transition timestamp, the cached token (in redacted form), and a reason string explaining the most recent transition. The reason string MUST be derived deterministically by the store from the most recently applied event via a closed event-to-phrase mapping; callers MUST NOT supply or override the reason text. The transition primitive accepts only an event identifier — no reason-text parameter.
- **FR-019-8**: The returned snapshot MUST be a defensive copy. Mutating any field of the returned value MUST NOT change the store's internal state, and a snapshot MUST remain valid for reading even after subsequent transitions have changed the store.
- **FR-019-9**: The cached session JWT MUST be held in a type whose default rendering through the project's structured logger emits the literal token `[redacted]` and never a byte of the underlying token value.
- **FR-019-10**: Read access to the cached token's bytes MUST be borrow-only: a caller can use the bytes within a scoped operation but cannot extract them into an unbounded copy through the snapshot API alone.
- **FR-019-11**: When the cached token is released (e.g. on session change or store shutdown), its underlying memory MUST be zeroed.
- **FR-019-12**: The state package MUST NOT spawn goroutines. Constructing a store, transitioning, and reading a snapshot MUST each be synchronous operations that return without leaving any background work behind.
- **FR-019-13**: The state package MUST NOT perform any side-effect outside of in-memory mutation: no network I/O, no child-process fork, no signal delivery, no filesystem write, no socket write, no Discord call, no audit-log write. Such side-effects belong to layers above this package.
- **FR-019-14**: Concurrent transitions and snapshot reads from multiple goroutines MUST be safe: the package MUST be race-detector-clean and every snapshot MUST be internally self-consistent (no torn read across a transition boundary).
- **FR-019-15**: The error returned for an illegal transition MUST identify both the current state and the rejected event in a form an operator can read in a log without having to consult source code.
- **FR-019-16**: Initial state on store construction MUST be `fetching` (the supervisor begins by attempting to claim a session before any child runs).
- **FR-019-17**: Once the store reaches `stopped`, no further state-changing transitions are accepted. A repeated stop event is an idempotent no-op success; any other event is rejected as illegal.
- **FR-019-18**: The conceptual `grace-restart` state MUST be reachable only from `running` and only when grace caching is permitted. From `grace-restart`, the only legal next states are `running` (grace restart succeeded) or `awaiting-approval` (grace window expired or grace restart failed). Re-entry is permitted within a single session: `running → grace-restart → running → grace-restart → ...` is a legal sequence as long as the grace window has not elapsed. The state machine MUST NOT impose a per-session re-entry counter; the upper bound is enforced upstream by the grace-window timer (owned by `internal/supervise`'s grace logic, not the state model), and exhaustion drives the standard `grace-restart → awaiting-approval` transition.
- **FR-019-19**: Recovery from `awaiting-approval` after operator re-approval MUST be a two-step transition. An "approval granted" event drives `awaiting-approval → fetching`; a separate "fetch succeeded" event then drives `fetching → running`. There is no single composite event that crosses `awaiting-approval → running` directly. This preserves single-purpose events and lets validator or fetch failures mid-refetch surface as their own table-driven transitions (e.g. `fetching → awaiting-approval`).
- **FR-019-20**: The last-transition timestamp included in every snapshot MUST be set by the store at transition commit time using a clock supplied to the store at construction. The store MUST accept a clock interface (or equivalent seam) at `NewStore` time so that production code provides a real-time clock backed by the system wall clock and tests provide a controllable fake. The transition primitive MUST NOT accept a caller-supplied timestamp argument; the store reads the current time exclusively through its injected clock. Tests MAY assert exact timestamp values via the fake clock; production behaviour MUST be indistinguishable from a direct `time.Now()` read at the same instant.
- **FR-019-21**: The closed event vocabulary at v0.1.0 is exactly the following 15 events. The state package MUST recognise each by name and MUST reject any event identifier outside this set as if it were an illegal transition. Adding, removing, or renaming an event requires a SPEC amendment, not a code change alone.

  | # | Event | Source state(s) | Destination state | Origin scenario(s) |
  |---|-------|-----------------|-------------------|--------------------|
  | 1 | `EventFetchOK` | `fetching` | `running` | 2, 3, 4, 7-after-approval, 13 |
  | 2 | `EventFetchAuthRequired` | `fetching` | `awaiting-approval` | 7 (401 unknown-jti), 9 (expired session during refill) |
  | 3 | `EventClaimDenied` | `fetching` | `awaiting-approval` | (operator denied via Discord) |
  | 4 | `EventClaimUnavailable` | `fetching` | `awaiting-approval` | 10 (Discord unavailable) |
  | 5 | `EventValidatorFailed` | `fetching` | `awaiting-approval` | 6 |
  | 6 | `EventBootRetryExhausted` | `fetching` | `stopped` | 11 |
  | 7 | `EventChildExitClean` | `running` | `fetching` | 3 |
  | 8 | `EventChildExitCrash` | `running` | `fetching` | 4 |
  | 9 | `EventChildExit78Stale` | `running` | `awaiting-approval` | 5 |
  | 10 | `EventRefreshRequested` | `running` | `fetching` | 13 (operator-triggered secret refresh that requires child restart) |
  | 11 | `EventGraceRestartTriggered` | `running` | `grace-restart` | 9 (with grace cache) |
  | 12 | `EventGraceRestartOK` | `grace-restart` | `running` | 9 (with grace cache, success) |
  | 13 | `EventGraceExpired` | `grace-restart` | `awaiting-approval` | 9 (with grace cache, window elapsed) |
  | 14 | `EventApprovalGranted` | `awaiting-approval` | `fetching` | 5, 7 (after re-approval) |
  | 15 | `EventStopRequested` | any state | `stopped` (idempotent no-op when already `stopped`) | (operator or host shutdown) |

  Background work that does NOT change the supervisor's posture toward its child (e.g. a JWT-only refresh in Scenario 8 where the child keeps running throughout) MUST NOT be modeled as a state-machine event at this layer; such operations are owned by the refill/refresh layer (SDD-21) and never reach `Transition`.

### Key Entities *(include if feature involves data)*

- **Supervisor state**: One of five named values describing what the
  supervisor is currently doing. The single piece of data every
  downstream component reads to make a decision.
- **Lifecycle event**: A named occurrence (claim approved, claim
  denied, child exited cleanly, child exited with stale-credential
  code, validator failed, refresh requested, approval granted, grace
  expired, stop requested, etc.) that may cause a state transition.
  The event vocabulary is fixed and closed at this layer; new event
  names require a state-table amendment.
- **Transition table**: The closed set of (current state, event) →
  next state mappings. Derived directly from the diagrams in
  `docs/LIFECYCLE-SCENARIOS.md`. Authoritative for legality decisions.
- **Snapshot**: An immutable point-in-time copy of the store's
  observable fields, safe to hand to a caller that wants to read
  without holding any lock against the store.
- **Cached session token**: The active supervisor session JWT, held
  inside a redacting wrapper. Possession is authority; logging it in
  plaintext is unforgivable per Constitution X.
- **Last-transition metadata**: The timestamp of the most recent
  transition and a reason field (derived from the event via a closed
  event-to-phrase mapping owned by the store, not caller-supplied)
  that the operator surfaces through the status socket — both
  included in every snapshot.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-019-1**: 100% of the (state, event) cells implied by Scenarios 2 through 15 in `docs/LIFECYCLE-SCENARIOS.md` are exercised by a positive test case in the suite, and 100% of cells outside that set are exercised by a negative test case asserting the named invalid-transition error.
- **SC-019-2**: The state package's test suite passes under the race detector with zero races detected over a 100-iteration run of the concurrent-transition-and-snapshot test.
- **SC-019-3**: A structured log line containing a snapshot (taken when a token is cached) contains the redaction marker `[redacted]` and contains zero bytes of the underlying token value, asserted by automated test.
- **SC-019-4**: A test that mutates every mutable field of a returned snapshot, then re-reads the store, observes a second snapshot byte-for-byte identical to the original — proving defensive copying.
- **SC-019-5**: A test harness that fails on any goroutine spawn, network connection, child fork, signal delivery, or filesystem write attributable to the state package completes the full lifecycle suite with zero such events recorded.
- **SC-019-6**: Coverage on `internal/supervise` at this chunk's scope is at least 95%, measured by the project's coverage tooling.
- **SC-019-7**: Every illegal transition produces an error whose printed form names the current state and the rejected event explicitly enough that an operator reading a log can identify both without reading source code.
- **SC-019-8**: Downstream chunks (child runner, refill/refresh/grace, status socket) consume the store through only its public API — they never reach into private fields and never duplicate the state-table logic. Verifiable by inspection at the start of those chunks.

## Assumptions

- **Lifecycle scenarios are authoritative**: The 15 scenarios in `docs/LIFECYCLE-SCENARIOS.md` are the single source of truth for which transitions exist. The state table this chunk produces is a transcription of those diagrams, not an independent design.
- **No new states beyond five**: The five-state vocabulary
  (`fetching`, `running`, `awaiting-approval`, `grace-restart`,
  `stopped`) is sufficient for v0.1.0. New states require a SPEC
  amendment, not just a code change.
- **Event vocabulary is closed at this layer**: Any new event a future
  chunk needs MUST be added explicitly to the event set and to the
  state table. The state machine does not accept anonymous or
  free-form events.
- **Borrow-only token access is enforced by the existing redacting type** (Constitution X, Principle III layer 5). This chunk uses that contract; it does not redefine it.
- **Status socket consumes the snapshot, not the store**: The status
  socket layer (separate chunk) reads via `Snapshot()` and does not
  hold any lock on the store. This chunk's defensive-copy guarantee
  is what makes that safe.
- **Concurrency model**: Transitions are commit-style (all-or-nothing);
  snapshot reads during an in-flight transition see either the pre or
  the post state, never a partial mix.
- **Grace-restart as a first-class state**: `docs/LIFECYCLE-SCENARIOS.md`
  describes grace-restart as "conceptual sub-state when cached secrets
  are being used" and notes "implementation may represent grace as
  flags instead of a distinct enum". This spec elevates grace-restart
  to a first-class state value because the state machine MUST be
  table-driven and the grace path has distinct legal-transition
  semantics (only reachable from `running`; only `running` or
  `awaiting-approval` may follow). Implementation latitude on internal
  representation is preserved at the plan/implementation phase.
- **No network or filesystem touch is acceptable in this layer**, even
  for "diagnostic" purposes (e.g. writing a transition to a log file).
  Logging emission is a side-effect of the caller, not the store.
- **Single supervisor instance per process**: Duplicate-supervisor
  detection (Scenario 14) is enforced by the PID-file/flock layer
  upstream of this store. The store itself does not police uniqueness.
