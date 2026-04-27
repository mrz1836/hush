# SDD-19 — `internal/supervise` state machine + transitions + store

**Phase:** 5
**Package:** `internal/supervise`
**Files:** `state.go`, `*_test.go`
**Branch:** `019-supervise-state` (created by the `before_specify` git hook)
**Blocked by:** SDD-07, SDD-18
**Blocks:** SDD-20, SDD-21, SDD-25
**Primary AC:** AC-10
**Coverage target:** 95%

**Behaviour contracts (MUST):**
- `type State string` with constants: `StateFetching`, `StateRunning`, `StateAwaitingApproval`, `StateGraceRestart`, `StateStopped`
- `type Store struct` with `mu sync.RWMutex`
- `Transition(ctx, event Event) error` — table-driven; rejects illegal transitions with `ErrInvalidTransition`
- `Snapshot()` returns defensive copy for status socket use
- `Token` field is `*securebytes.SecureBytes` wrapping the encoded JWT

**Anti-contracts (MUST NOT):**
- Allow caller to read mutable internal fields directly
- Spin a goroutine here (state is data only — goroutines belong in SDD-20/21)

**Tests required:**
- Unit: every legal transition has a positive test; every illegal pair has a negative test (table-driven from the state-table matrix in `docs/LIFECYCLE-SCENARIOS.md`); `Snapshot` returns a defensive copy (mutate the snapshot — verify the source is unchanged); `Token` is wrapped in `SecureBytes` (LogValue returns `[redacted]`)
- Race: `TestStore_ConcurrentTransitionAndSnapshot` — N goroutines transitioning + reading snapshots; race detector clean

**Constitutional principles in scope:** IV (TTL discipline through state model), V (status socket sees Snapshot, not internals), VIII (95% coverage + TDD), IX (idiomatic Go, no implicit goroutines), X (Token never logged in plain)

**Exported API to lock in PACKAGE-MAP.md (this chunk — new entry):**
- `type State string`  with `StateFetching`, `StateRunning`, `StateAwaitingApproval`, `StateGraceRestart`, `StateStopped`
- `type Event string`  (transition triggers — names per `docs/LIFECYCLE-SCENARIOS.md`)
- `type Store struct { ... }`
- `func NewStore(ctx context.Context) *Store`
- `func (s *Store) Transition(ctx context.Context, event Event) error`
- `func (s *Store) Snapshot() Snapshot`
- `type Snapshot struct { State State; ChildPID int; LastTransitionAt time.Time; ... }`
- `var ErrInvalidTransition`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-19 (internal/supervise:
state machine + transitions + store) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles IV, V, VIII, IX, X)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (state diagrams in Scenarios 2..15 — the state-table matrix lives here)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-19.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/supervise/state.go file is the supervisor's state
machine and snapshot store. It is the single source of truth that
SDD-20 (child fork/exec), SDD-21 (refill/refresh/grace), and
SDD-22 (status socket) all consult. It holds NO goroutines and NO
side-effects — purely a guarded state model.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The supervisor occupies exactly one of five states at a time:
  fetching, running, awaiting-approval, grace-restart, stopped.
- Transitions between states are explicit, table-driven events.
  Illegal transition pairs MUST be rejected with a distinct,
  named error.
- The state table matches the diagrams in
  docs/LIFECYCLE-SCENARIOS.md exactly.
- The store provides a Snapshot accessor that returns a
  DEFENSIVE COPY — mutating the returned snapshot MUST NOT
  mutate the underlying store.
- The cached JWT (the supervisor's current token) is held as
  a SecureBytes — when the snapshot is logged via slog, the
  token renders as "[redacted]".
- The state model owns NO goroutines and triggers NO
  side-effects (fetch, spawn, signal — those belong to SDD-20+).

The spec MUST NOT encode HOW (no library names, no Go-specific
struct layouts beyond field names). Those are plan-phase.

Acceptance criterion: AC-10 (supervisor lifecycle).

Action — run exactly one command:
  /speckit-specify "internal/supervise state machine: five states (fetching, running, awaiting-approval, grace-restart, stopped); table-driven transitions from a fixed event set; illegal transitions rejected with named error; Snapshot returns a defensive copy; cached JWT is SecureBytes-wrapped; no goroutines, no side-effects in this layer"

The before_specify hook will create branch 019-supervise-state.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-19 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-19.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-19 (internal/supervise state
machine + store) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; IV/V/VIII/IX/X load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (entire — the state-table matrix you encode here MUST match every Scenario diagram exactly)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (no internal/supervise entry yet)
- /Users/mrz/projects/hush/docs/sdd/SDD-19.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/supervise (NEW)
- Files: state.go (State, Event, Store, Snapshot, Transition,
  table), state_test.go
- Exported API:
    type State string
    const (
        StateFetching         State = "fetching"
        StateRunning          State = "running"
        StateAwaitingApproval State = "awaiting-approval"
        StateGraceRestart     State = "grace-restart"
        StateStopped          State = "stopped"
    )
    type Event string  // EventFetchOK, EventFetchAuthRequired,
                       // EventChildExitClean, EventChildExitError,
                       // EventApprovalGranted, EventApprovalDenied,
                       // EventGraceExpired, EventStopRequested, ...
    type Store struct { ... }
    func NewStore(ctx context.Context) *Store
    func (s *Store) Transition(ctx context.Context, event Event) error
    func (s *Store) Snapshot() Snapshot
    type Snapshot struct {
        State State; ChildPID int; LastTransitionAt time.Time;
        Token *securebytes.SecureBytes;   // never logged plain
        Reason string; ...
    }
    var ErrInvalidTransition

Implementation contract (HOW — locked):
- Internal: sync.RWMutex protecting (currentState, childPID,
  lastTransitionAt, token, reason). Transition takes the write
  lock. Snapshot takes the read lock.
- The transition table is a package-level
  map[State]map[Event]State (or equivalent). On lookup miss:
  ErrInvalidTransition with both the current state and the event
  in the message.
- Snapshot returns a value (not a pointer) and copies the Token
  pointer (NOT the underlying bytes — SecureBytes is borrow-only;
  the caller can read via Use(fn) but cannot extract).
- No goroutines started here. NewStore(ctx) accepts ctx for
  parity with future expansion but the current impl ignores it.
- Token wrapping: the Transition that sets the JWT receives a
  *securebytes.SecureBytes; the store keeps the pointer. The
  store's logger calls (if any) MUST format the snapshot via
  slog so LogValue redacts the token.

Coverage target: 95%.
Constitutional principles in scope: IV, V, VIII, IX, X.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-19 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-19.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 95%. The state-table tests MUST be table-driven, with every legal (state,event) pair covered as a positive test and every illegal pair covered as a negative test (ErrInvalidTransition). Specific tests required: TestStore_LegalTransitions (table-driven from docs/LIFECYCLE-SCENARIOS.md), TestStore_IllegalTransitionErr, TestStore_SnapshotIsDefensiveCopy (mutate snapshot, source unchanged), TestStore_TokenLogValueRedacts (assert SecureBytes redaction in slog output), TestStore_ConcurrentTransitionAndSnapshot (race-clean). Final phase MUST include magex format:fix, magex lint, magex test:race."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-19 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-19.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 95% on internal/supervise (state.go portion):
     go test -cover ./internal/supervise/ -run State
3. Confirm the state-table tests cover every (state,event) cell in
   the matrix from docs/LIFECYCLE-SCENARIOS.md.
4. Confirm TestStore_TokenLogValueRedacts proves the Token is
   never logged in plain.
5. Append "Exported API — locked at SDD-19" section to
   docs/PACKAGE-MAP.md as a NEW entry for internal/supervise
   listing the locked API from the chunk doc.
6. Update docs/AC-MATRIX.md AC-10 row with the new test file paths.
7. Mark SDD-19 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/supervise/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(supervise): state machine + Store + Snapshot (SDD-19)"

Final message: confirm gates passed, race-clean, coverage ≥ 95%,
state-table matches LIFECYCLE-SCENARIOS.md, Snapshot defensiveness
proven, Token redaction proven, AC-10 row updated, SDD-PLAYBOOK
updated, and the combined commit created.
```
