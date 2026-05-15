---
description: "Task list for SDD-19 — internal/supervise state machine + snapshot store"
---

# Tasks: Supervisor State Machine + Snapshot Store (SDD-19)

**Input**: Design documents from `/specs/019-supervise-state/`
**Prerequisites**: plan.md (loaded), spec.md (loaded), research.md (loaded), data-model.md (loaded), contracts/go-api.md (loaded), contracts/state-table.md (loaded), contracts/test-catalogue.md (loaded)

**Tests**: REQUIRED. Constitution VIII (Testing Discipline) is TDD-mandatory for this chunk — every behaviour contract has a test-writing task BEFORE the implementation task that satisfies it. Coverage target: **≥95%** on `internal/supervise/state.go`. The state-table tests MUST be table-driven over the 5×15 matrix in `contracts/state-table.md`: every legal cell exercised by a positive test, every illegal cell exercised by a negative test asserting `errors.Is(err, ErrInvalidTransition)`.

**Organization**: Tasks are grouped by user story (US1..US5 from `spec.md`) so each story's behaviour contract can be verified independently. All five user stories live in a single Go file (`internal/supervise/state.go`) so within a story the test-writing tasks may run in parallel, but implementation is sequential per the build order in `contracts/test-catalogue.md` §"TDD ordering" (constants → sentinels & maps → Clock → Store/NewStore → Snapshot → Transition).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different file or independent test-only edit, no dependencies on incomplete tasks)
- **[Story]**: Maps the task to a user story from `spec.md` (US1..US5)
- All file paths are absolute or repo-root-relative
- Single Go module; package paths under `internal/`

## Path Conventions

- Source: `internal/supervise/state.go`, `internal/supervise/doc.go`
- Tests: `internal/supervise/state_test.go`
- Spec artifacts: `specs/019-supervise-state/`
- Docs to update: `docs/PACKAGE-MAP.md`, `docs/AC-MATRIX.md`, `docs/SDD-PLAYBOOK.md`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Confirm preconditions, scaffold the new package files alongside the pre-existing `internal/supervise/config/` (SDD-18) subpackage. No behaviour yet.

- [X] T001 Verify the working tree is on branch `019-supervise-state` and that `internal/supervise/` currently contains only the SDD-18 `config/` subpackage (no `package supervise` files yet) — run `git rev-parse --abbrev-ref HEAD` and `ls internal/supervise/` and abort if either check fails
- [X] T002 [P] Confirm SDD-02 `*securebytes.SecureBytes` API is locked at the version this chunk relies on (`New([]byte) *SecureBytes`, `LogValue() slog.Value`, `Use(func([]byte) error) error`, `Destroy()`, `ErrDestroyed`) — grep `internal/vault/securebytes/*.go` for each symbol; abort if any is missing
- [X] T003 Create `internal/supervise/doc.go` carrying the package-level godoc verbatim from `contracts/go-api.md` lines 13-19 (the `// Package supervise owns the supervisor daemon's lifecycle...` comment block) — file declares only `package supervise` plus the doc comment, no other code

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Lay down the locked type-signature stub for `internal/supervise/state.go` so every test in `state_test.go` can compile against the contract before any behaviour exists. Also write the three vocabulary-protection tests (T-02, T-13, T-14) that all five user stories depend on for closed-set integrity.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete. Tests written here MUST FAIL (red) when run against the stub.

- [X] T004 Create `internal/supervise/state.go` as a compile-only stub matching `contracts/go-api.md` exactly: declare `type State string` + 5 state constants, `type Event string` + 15 event constants, `type Clock interface { Now() time.Time }`, `type Store struct { ... }` (private fields per `data-model.md` §Store), `func NewStore(ctx context.Context, clock Clock) *Store` (body: `panic("not implemented")`), `func (s *Store) Transition(ctx context.Context, event Event) error` (body: `panic("not implemented")`), `func (s *Store) Snapshot() Snapshot` (body: `panic("not implemented")`), `type Snapshot struct { ... }` (public fields per `data-model.md` §Snapshot), `var ErrInvalidTransition = errors.New("supervise: invalid transition")`, plus declared-but-empty package-level `var transitions = map[State]map[Event]State{}` and `var reasons = map[Event]string{}` placeholders — file MUST compile clean against `go build ./internal/supervise/`
- [X] T005 [P] Write **TestNewStore_NilClockPanics** (T-02) in `internal/supervise/state_test.go`: asserts `NewStore(context.Background(), nil)` panics with a message identifying the nil-clock programmer error; uses `defer func() { recover() }()` to capture the panic. Test MUST be red against the T004 stub (which currently panics with `"not implemented"` not the expected message)
- [X] T006 [P] Write **TestReasons_KeysetMatchesEventVocabulary** (T-13) in `internal/supervise/state_test.go`: enumerates the 15 `Event` constants from `contracts/go-api.md` in a slice, asserts the package-internal `reasons` map's keyset equals exactly that slice (no missing key, no extra key). Test MUST be red against the T004 stub (empty `reasons` map)
- [X] T007 [P] Write **TestTransitions_KeysetMatchesStateVocabulary** (T-14) in `internal/supervise/state_test.go`: asserts the package-internal `transitions` map's outer keyset is exactly the 5 closed-vocabulary `State` values, and that every inner-map value (destination state) is also drawn from the closed `State` set. Test MUST be red against the T004 stub (empty `transitions` map)
- [X] T008 Add inline test-only fakes to `internal/supervise/state_test.go`: `type fakeClock struct { mu sync.Mutex; now time.Time }` with `Now()`, `Set(time.Time)`, `Advance(time.Duration)` methods; per R-011 the fake MUST live inline in `state_test.go` (no `internal/testutil` package). All subsequent test tasks reuse this fake
- [X] T009 Run `go test ./internal/supervise/ -run 'TestNewStore_NilClockPanics|TestReasons_KeysetMatchesEventVocabulary|TestTransitions_KeysetMatchesStateVocabulary'` and confirm all three tests **FAIL** (red gate — proves the stub does not yet honour the contract)

**Checkpoint**: Foundation ready — `state.go` stub compiles, `state_test.go` has the fake clock and three failing closed-vocabulary tests. User story implementation can now begin.

---

## Phase 3: User Story 1 — Single source of truth for supervisor lifecycle (Priority: P1) 🎯 MVP

**Goal**: A supervisor instance is always in exactly one of five named states. The 19 legal `(state, event) → state` cells from `contracts/state-table.md` all transition correctly under `Transition()`, with `Reason` derived deterministically from the event and `LastTransitionAt` stamped from the injected `Clock`.

**Independent Test**: Construct a fresh store with a fake clock, drive it through all 19 legal cells (covering every Scenario 2-15 path from `docs/LIFECYCLE-SCENARIOS.md`); at every step assert the post-state, `LastTransitionAt`, and `Reason` match the contract. Story-level success requires `TestStore_LegalTransitions`, `TestNewStore_InitialState`, `TestStore_StopIsIdempotent`, `TestStore_GraceRestartReentry`, and `TestStore_TwoStepRecoveryFromAwaitingApproval` all green.

### Tests for User Story 1 (TDD-mandatory — write FIRST, ensure FAIL before implementation) ⚠️

- [X] T010 [P] [US1] Write **TestNewStore_InitialState** (T-01) in `internal/supervise/state_test.go`: constructs a store with `fakeClock` set to a fixed instant; asserts `Snapshot()` returns `State == StateFetching`, `ChildPID == 0`, `Token == nil`, `Reason == "store constructed"`, `LastTransitionAt == fakeClock.Now()` at construction. Satisfies FR-019-16 + edge case "Snapshot of an empty store"
- [X] T011 [P] [US1] Write **TestStore_LegalTransitions** (T-03) in `internal/supervise/state_test.go` as a table-driven test over all **19 legal cells** from `contracts/state-table.md`. Each table row carries: source state, event, expected destination state, prefix sequence (events to drive the store from `StateFetching` to source state). Per row: construct a fresh store with `fakeClock`; apply prefix; advance clock by 1s; apply the event under test; assert no error, `Snapshot().State == destination`, `Snapshot().LastTransitionAt == newNow`, `Snapshot().Reason == reasons[event]` (matching the locked phrase from `contracts/go-api.md` §"Closed event-to-phrase mapping"). Satisfies FR-019-3, FR-019-4, FR-019-7, FR-019-20, SC-019-1 (positive half)
- [X] T012 [P] [US1] Write **TestStore_StopIsIdempotent** (T-05) in `internal/supervise/state_test.go`: drives store to `StateStopped` via `EventStopRequested` from `StateFetching`; advances `fakeClock` by 1s; applies `EventStopRequested` again; asserts no error, state still `StateStopped`, `LastTransitionAt` advanced, `Reason == "stop requested"`. Then asserts that any non-stop event from `StateStopped` returns `ErrInvalidTransition` and the snapshot is byte-for-byte unchanged. Satisfies FR-019-17, R-010
- [X] T013 [P] [US1] Write **TestStore_GraceRestartReentry** (T-06) in `internal/supervise/state_test.go`: drives the legal sequence `running → grace-restart → running → grace-restart → running` (asserting no error and correct state at each step); then drives `running → grace-restart → awaiting-approval` via `EventGraceExpired`. Confirms no per-session counter caps re-entry. Satisfies FR-019-18, Clarification 3
- [X] T014 [P] [US1] Write **TestStore_TwoStepRecoveryFromAwaitingApproval** (T-07) in `internal/supervise/state_test.go`: from `StateAwaitingApproval`, applies `EventApprovalGranted` and asserts the post-state is `StateFetching` (NOT `StateRunning`); then applies `EventFetchOK` and asserts `StateRunning`. Also asserts that `EventApprovalGranted` followed by `EventValidatorFailed` from `StateFetching` lands on `StateAwaitingApproval` (per the table). Satisfies FR-019-19, Clarification 2
- [X] T015 [US1] Run `go test ./internal/supervise/ -run 'TestNewStore_InitialState|TestStore_LegalTransitions|TestStore_StopIsIdempotent|TestStore_GraceRestartReentry|TestStore_TwoStepRecoveryFromAwaitingApproval'` and confirm all five tests **FAIL** (red gate)

### Implementation for User Story 1

- [X] T016 [US1] In `internal/supervise/state.go`, populate the package-internal `var reasons = map[Event]string{...}` literal with all 15 entries from `contracts/go-api.md` §"Closed event-to-phrase mapping" verbatim (e.g. `EventFetchOK: "fetch succeeded"`, `EventStopRequested: "stop requested"`, etc.). Map is read-only post-init per Constitution IX sentinel-class exception (R-002, R-005). T-13 turns green at this step
- [X] T017 [US1] In `internal/supervise/state.go`, populate the package-internal `var transitions = map[State]map[Event]State{...}` literal with all 19 legal edges from `contracts/state-table.md` §"Matrix" verbatim. Outer keys: 5 states. Inner-map entries per source state: only the events legal from that state per the matrix (e.g. `StateFetching` has 7 entries: 6 events 1-6 plus `EventStopRequested`; `StateStopped` has 1 entry: `EventStopRequested → StateStopped` for idempotency). Map is read-only post-init (R-002). T-14 turns green at this step
- [X] T018 [US1] In `internal/supervise/state.go`, define an unexported `realClock struct{}` type with `func (realClock) Now() time.Time { return time.Now() }` and an unexported `defaultClock = realClock{}`. Production callers MAY pass `realClock{}` directly to `NewStore` (the public Clock interface accepts any impl); this type is provided as a convenience for `cmd/hush` wiring in SDD-20+ but is not exported (R-009)
- [X] T019 [US1] In `internal/supervise/state.go`, implement `NewStore(ctx context.Context, clock Clock) *Store`: panic with `"supervise: NewStore requires a non-nil Clock"` if `clock == nil`; otherwise return `&Store{ currentState: StateFetching, lastTransitionAt: clock.Now(), reason: "store constructed", clock: clock }`. T-02 (TestNewStore_NilClockPanics) turns green at this step
- [X] T020 [US1] In `internal/supervise/state.go`, implement `func (s *Store) Snapshot() Snapshot`: take the read lock (`s.mu.RLock()` / `defer s.mu.RUnlock()`); construct and return a by-value `Snapshot{ State: s.currentState, ChildPID: s.childPID, LastTransitionAt: s.lastTransitionAt, Token: s.token, Reason: s.reason }`. By-value return + pointer-copied `*SecureBytes` (borrow-only per SDD-02) constitutes the defensive copy per FR-019-8. T-01 (TestNewStore_InitialState) turns green at this step
- [X] T021 [US1] In `internal/supervise/state.go`, implement `func (s *Store) Transition(ctx context.Context, event Event) error` for the **legal** path only: take the write lock (`s.mu.Lock()` / `defer s.mu.Unlock()`); look up `transitions[s.currentState][event]`; if found, set `s.currentState = next`, `s.lastTransitionAt = s.clock.Now()`, `s.reason = reasons[event]`, return `nil`; if not found, return a placeholder error (final wrapping is implemented in US2). The lookup MUST happen before any write so that an illegal transition cannot mutate state mid-method (FR-019-6). T-03, T-05, T-06, T-07 turn green at this step
- [X] T022 [US1] Run `go test ./internal/supervise/ -run 'TestNewStore_InitialState|TestStore_LegalTransitions|TestStore_StopIsIdempotent|TestStore_GraceRestartReentry|TestStore_TwoStepRecoveryFromAwaitingApproval|TestNewStore_NilClockPanics|TestReasons_KeysetMatchesEventVocabulary|TestTransitions_KeysetMatchesStateVocabulary'` and confirm all eight tests **PASS** (green gate)

**Checkpoint**: User Story 1 functional. The store enforces the closed five-state vocabulary and drives every legal transition correctly. Vocabulary-protection tests T-02, T-13, T-14 also pass at this point.

---

## Phase 4: User Story 2 — Illegal transitions rejected with a distinct, named error (Priority: P1)

**Goal**: Every `(state, event)` pair NOT in the 19 legal cells of `contracts/state-table.md` is rejected with `ErrInvalidTransition` (identifiable via `errors.Is`); the wrapped error names both the source state and the rejected event in plain text; the store's fields are unchanged after a rejected call.

**Independent Test**: Drive the store into each of the 5 source states; from each, attempt every illegal event; assert (a) `errors.Is(err, ErrInvalidTransition)`, (b) error message contains the source state string and the event string, (c) post-snapshot byte-for-byte equals pre-snapshot. Story-level success requires `TestStore_IllegalTransitionErr` and `TestStore_TransitionUnknownEvent` green.

### Tests for User Story 2 (TDD-mandatory — write FIRST, ensure FAIL before implementation) ⚠️

- [X] T023 [P] [US2] Write **TestStore_IllegalTransitionErr** (T-04) in `internal/supervise/state_test.go` as a table-driven test over all **56 illegal cells** from `contracts/state-table.md` (75 total minus 19 legal). Each table row carries source state, event under test, prefix sequence. Per row: construct fresh store with `fakeClock`; apply prefix; capture pre-snapshot; apply rejected event; assert `errors.Is(err, ErrInvalidTransition)`, `err.Error()` contains both `string(sourceState)` (e.g. `"running"`) and `string(event)` (e.g. `"approval-granted"`); capture post-snapshot; assert post-snapshot equals pre-snapshot field-by-field (state, childPID, lastTransitionAt, token, reason). Satisfies FR-019-5, FR-019-6, FR-019-15, SC-019-1 (negative half), SC-019-7
- [X] T024 [P] [US2] Write **TestStore_TransitionUnknownEvent** (T-15) in `internal/supervise/state_test.go`: applies an `Event("garbage-not-in-set")` (typed string outside the closed constant set) to a fresh store; asserts `errors.Is(err, ErrInvalidTransition)`; asserts the rendered error string contains `"fetching"` and `"garbage-not-in-set"` verbatim. Satisfies FR-019-21
- [X] T025 [US2] Run `go test ./internal/supervise/ -run 'TestStore_IllegalTransitionErr|TestStore_TransitionUnknownEvent'` and confirm both tests **FAIL** (red gate — current `Transition` returns a placeholder error, not the wrapped sentinel)

### Implementation for User Story 2

- [X] T026 [US2] In `internal/supervise/state.go`, replace the placeholder error in `Transition` with the locked wrapped form `fmt.Errorf("supervise: %w (state=%s event=%s)", ErrInvalidTransition, s.currentState, event)` per `contracts/go-api.md` §"Wrapping form". The lookup-miss branch covers BOTH (a) source state has no entry for this event, AND (b) the event is outside the closed vocabulary entirely (typed-string drift) — both surface identically as a missing inner-map key per R-003
- [X] T027 [US2] Confirm `Transition` does not perform ANY field write before the table lookup: re-read the function in `state.go` and verify the only `s.<field> = ...` assignments live in the success branch after the lookup hit. This guarantees FR-019-6 (rejected transition leaves store unchanged)
- [X] T028 [US2] Run `go test ./internal/supervise/` and confirm all tests authored so far **PASS** (green gate, including all of US1 plus US2)

**Checkpoint**: User Stories 1 AND 2 functional. The state machine accepts every legal `(state, event)` pair and rejects every illegal pair with a typed sentinel error that names both halves.

---

## Phase 5: User Story 3 — Snapshot accessor returns a defensive copy (Priority: P1)

**Goal**: External readers (status socket, audit, debug) get a by-value snapshot they can read without holding any lock against the store and freely mutate without affecting the store. Concurrent transitions and snapshot reads from many goroutines remain race-detector-clean and never produce a torn read across a transition boundary.

**Independent Test**: Acquire a snapshot, mutate every public field, re-acquire — second snapshot is byte-for-byte equal to the original. Concurrently fan out goroutines driving transitions and others calling `Snapshot()`; `go test -race` reports zero races over 100 iterations.

### Tests for User Story 3 (TDD-mandatory — write FIRST, ensure FAIL before implementation) ⚠️

- [X] T029 [P] [US3] Write **TestStore_SnapshotIsDefensiveCopy** (T-08) in `internal/supervise/state_test.go`: drives the store through `EventFetchOK` so it has populated `State`, `LastTransitionAt`, `Reason`; acquires `snap1`; mutates every public field of `snap1` (set `State` to a different value, set `ChildPID` to 99, advance `LastTransitionAt` by 1h, set `Reason` to `"tampered"`, set `Token` to nil); acquires `snap2`; asserts `snap2.State == snap1.PreMutationState` and so on for each field — i.e. `snap1`'s mutation did not leak into the store. Satisfies FR-019-8, SC-019-4
- [X] T030 [P] [US3] Write **TestStore_ConcurrentTransitionAndSnapshot** (T-11) in `internal/supervise/state_test.go`: spawns 8 transition-driving goroutines each looping a known-legal cycle (`StateFetching → StateRunning → StateFetching → ...` via `EventFetchOK` then `EventChildExitClean`) for 100 iterations under a `fakeClock`; spawns 8 snapshot-reading goroutines each calling `Store.Snapshot()` in a tight loop and recording observed `(State, LastTransitionAt)` tuples; uses `sync.WaitGroup` to join all goroutines; asserts every observed `LastTransitionAt` is a value the fake clock actually produced (no torn read across a transition); test MUST be runnable under `go test -race` without race reports. Satisfies FR-019-14, SC-019-2
- [X] T031 [US3] Run `go test ./internal/supervise/ -run 'TestStore_SnapshotIsDefensiveCopy|TestStore_ConcurrentTransitionAndSnapshot'` AND `go test -race ./internal/supervise/ -run TestStore_ConcurrentTransitionAndSnapshot` and confirm both tests **FAIL OR ARE INCOMPLETE** initially. (The defensive-copy guarantee should already hold from T020's by-value return; this gate proves the test itself drives the contract correctly. The race test may already pass thanks to the locking discipline established in T020/T021 — that is acceptable; the test MUST still be authored to lock the contract in)

### Implementation for User Story 3

- [X] T032 [US3] Audit `internal/supervise/state.go` to confirm the locking discipline is correct: `Transition` holds `s.mu.Lock()` for the entire success block (lookup + all five field writes) so observers see all-or-nothing; `Snapshot` holds `s.mu.RLock()` for the entire field-read block so the returned `Snapshot` value is internally self-consistent. Make any adjustments required to ensure single-commit-point semantics per FR-019-14 (no torn reads)
- [X] T033 [US3] Run `go test -race -count=10 ./internal/supervise/ -run TestStore_ConcurrentTransitionAndSnapshot` and confirm zero race reports across 10 invocations of the 100-iteration test (per SC-019-2 the official measure is one 100-iteration run; we run 10× as defence in depth)
- [X] T034 [US3] Run `go test ./internal/supervise/` and confirm all tests authored so far **PASS** (green gate, including US1+US2+US3)

**Checkpoint**: User Stories 1, 2, AND 3 all functional. The store is concurrency-safe, race-clean, and snapshots are guaranteed defensive copies.

---

## Phase 6: User Story 4 — Cached session JWT is held in redacted form (Priority: P1)

**Goal**: The store's `Token` field is `*securebytes.SecureBytes`. Logging a snapshot via `log/slog` renders the token as `[redacted]` and emits zero bytes of the underlying token value. Token bytes are borrow-only via `Use(fn)`. When the store releases a token (test-driven seam in this chunk; production wiring is owned by SDD-21), `Destroy()` zeroes the underlying memory and subsequent `Use(fn)` calls return `securebytes.ErrDestroyed`.

**Independent Test**: Stash a `*SecureBytes` wrapping a known plaintext via the test-only seam; log the snapshot through a `bytes.Buffer`-backed `slog.Handler`; assert the buffer contains `[redacted]` and contains zero substring matches for the plaintext. Drive a release; assert subsequent `Use(fn)` returns `ErrDestroyed`.

**NOTE on R-009**: This chunk does NOT export a `SetToken` method. For tests to populate the token field, the test file uses an unexported package-private helper (allowed because `state_test.go` lives in `package supervise`, not `package supervise_test`) or a `// +build internaltest` style helper added in `state.go` and called only from tests. Production wiring of token writes is owned by SDD-21.

### Tests for User Story 4 (TDD-mandatory — write FIRST, ensure FAIL before implementation) ⚠️

- [X] T035 [US4] In `internal/supervise/state.go`, add a package-private helper `func (s *Store) setTokenForTest(tok *securebytes.SecureBytes)` that takes the write lock and assigns `s.token = tok`. The helper is unexported (lowercase) — callers outside `package supervise` cannot reach it. R-009: this is the agreed-on test seam for SDD-19; the public token-write API is deferred to SDD-21. Document with a one-line comment: `// setTokenForTest is a package-private seam used by state_test.go; production token writes are owned by SDD-21.`
- [X] T036 [P] [US4] Write **TestStore_TokenLogValueRedacts** (T-09) in `internal/supervise/state_test.go`: constructs a `*securebytes.SecureBytes` wrapping the plaintext `"sensitive-jwt-bytes-abc"`; uses `setTokenForTest` to attach it to the store; acquires snapshot; constructs a `slog.NewJSONHandler(&buf, nil)` against a `bytes.Buffer`; logs `slog.New(handler).LogAttrs(ctx, slog.LevelInfo, "supervise.snapshot", slog.Any("snap", snap))`; asserts `buf.String()` contains the literal substring `[redacted]` and contains zero occurrences of `"sensitive-jwt-bytes-abc"` and zero occurrences of any 6-byte sub-window of that plaintext. Repeats the assertion for the bare `Token` field logged in isolation: `slog.LogAttrs(ctx, slog.LevelInfo, "token", slog.Any("token", snap.Token))`. Satisfies FR-019-9, FR-019-10, SC-019-3, Constitution X
- [X] T037 [P] [US4] Write **TestStore_TokenZeroOnRelease** (T-10) in `internal/supervise/state_test.go`: constructs `*SecureBytes` from `"plaintext-to-zero"`; attaches via `setTokenForTest`; reads the token bytes once via `tok.Use(fn)` to confirm the plaintext is reachable; calls `tok.Destroy()` directly (mirroring the post-swap zero that R-007 requires); asserts a subsequent `tok.Use(fn)` returns an error matching `errors.Is(err, securebytes.ErrDestroyed)`. Satisfies FR-019-11, R-007
- [X] T038 [US4] Run `go test ./internal/supervise/ -run 'TestStore_TokenLogValueRedacts|TestStore_TokenZeroOnRelease'` and confirm both tests **PASS** (the redaction is type-driven via SDD-02's `*SecureBytes.LogValue()`; the zeroing is upstream behaviour from SDD-02 — neither requires code in `state.go` beyond the `setTokenForTest` seam already added in T035). If either test fails, fix the seam or test setup until both pass

### Implementation for User Story 4

- [X] T039 [US4] Confirm by code reading that `internal/supervise/state.go` does NOT call `slog.LogAttrs` itself (the package emits no log lines of its own per Constitution X — observability is the caller's job) and that the `Snapshot.Token` field renders correctly through caller-side slog because `*securebytes.SecureBytes` already implements `slog.LogValuer` via `LogValue() slog.Value` (locked at SDD-02). No new code required in `state.go`
- [X] T040 [US4] Run `go test ./internal/supervise/` and confirm all tests authored so far **PASS** (green gate, US1+US2+US3+US4)

**Checkpoint**: User Stories 1-4 all functional. The cached JWT is type-narrowed to `*SecureBytes`; structured logging renders `[redacted]` with zero plaintext leakage; the token zeroes on release.

---

## Phase 7: User Story 5 — State model owns no goroutines and triggers no side-effects (Priority: P2)

**Goal**: The state package spawns ZERO goroutines, opens ZERO network connections, forks ZERO child processes, delivers ZERO signals, and writes ZERO bytes to filesystem or sockets. The full lifecycle suite leaves no background work behind.

**Independent Test**: Capture `runtime.NumGoroutine()` before and after running a full lifecycle (construct store, drive ~30 events covering all 19 legal edges plus a handful of stops, snapshot a few times, stop). Assert delta is exactly 0. Filesystem/network constraints are enforced at code-review time by the absence of `os`, `net`, and other side-effecting stdlib imports.

### Tests for User Story 5 (TDD-mandatory — write FIRST, ensure FAIL before implementation) ⚠️

- [X] T041 [P] [US5] Write **TestStore_NoSideEffects** (T-12) in `internal/supervise/state_test.go`: captures `before := runtime.NumGoroutine()`; constructs a fresh store with `fakeClock`; drives ~30 events covering all 19 legal edges (a sequence covering every Scenario 2-15 path is fine — reuse the table from T011 if helpful); calls `Snapshot()` between transitions; finally drives `EventStopRequested`; sleeps for a short bounded interval (e.g. `time.Sleep(10*time.Millisecond)`) to let any leaked goroutine schedule; asserts `runtime.NumGoroutine() - before == 0`. Satisfies FR-019-12, FR-019-13, SC-019-5
- [X] T042 [US5] Run `go test ./internal/supervise/ -run TestStore_NoSideEffects` and confirm the test **PASSES** against the existing implementation (the package never spawned a goroutine, so the delta should already be zero). If the test fails, audit `state.go` for any accidental `go func()` and remove it

### Implementation for User Story 5 (verification-only — no new code)

- [X] T043 [US5] Audit `internal/supervise/state.go` and `internal/supervise/doc.go` import lists: assert the only stdlib imports are `context`, `errors`, `fmt`, `sync`, `time` (and optionally `log/slog` if a typed value is referenced — but no slog calls). Assert ZERO imports of `os`, `net`, `net/http`, `os/exec`, `syscall`, `path/filepath` from the package (FR-019-13). Document any deviation with a comment explaining the necessity
- [X] T044 [US5] Audit `internal/supervise/state.go` for any `go func()`, `time.AfterFunc`, `time.NewTimer`, `time.NewTicker`, or `runtime.SetFinalizer` calls; assert there are NONE (FR-019-12). The Clock interface produces values via direct `Now()` calls, not via background tickers
- [X] T045 [US5] Run `go test ./internal/supervise/` and confirm ALL 15 tests (T-01..T-15) **PASS** in a single run

**Checkpoint**: User Stories 1-5 all functional and verified. The state model is a pure guarded data type with zero side-effects.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Run the constitutional gates (Principles VIII, IX, X), verify coverage targets, update documentation handles, and produce the combined commit per Prompt 5 of `docs/sdd/SDD-19.md`.

- [X] T046 Run `magex format:fix` from repo root and confirm exit 0 (gofmt/goimports clean across `internal/supervise/`)
- [X] T047 Run `magex lint` from repo root and confirm exit 0 with zero findings on `internal/supervise/state.go`, `internal/supervise/doc.go`, and `internal/supervise/state_test.go` (golangci-lint policy from `.github/tech-conventions/`)
- [X] T048 Run `magex test:race` from repo root and confirm exit 0 with race-detector clean across the entire test suite (full repo, not just `internal/supervise/`); this is the canonical Constitution VIII race gate
- [X] T049 Run `go test -race -count=1 -run TestStore_ConcurrentTransitionAndSnapshot ./internal/supervise/` directly and confirm zero races (SC-019-2: 100-iteration concurrent test is built into the test itself; this command explicitly invokes it once under `-race`)
- [X] T050 Run `go test -cover ./internal/supervise/ -run State` and confirm coverage on `internal/supervise/state.go` is **≥ 95%** per Constitution VIII High band and SC-019-6. If below 95%, identify uncovered branches in `state.go` and add targeted tests to `state_test.go` until the threshold is met (the `config/` subpackage's coverage is reported separately and is out of scope for this gate)
- [X] T051 Manual audit: open `contracts/state-table.md` alongside `internal/supervise/state_test.go`; confirm every cell of the 5×15 matrix appears either in `TestStore_LegalTransitions`'s table (19 legal cells) or in `TestStore_IllegalTransitionErr`'s table (56 illegal cells) — total 75 cells. SC-019-1: 100% cell coverage. Discrepancies require new test rows
- [X] T052 Manual audit: re-read `internal/supervise/state.go` for the anti-API list in `contracts/go-api.md` §"Anti-API"; confirm none of the following are exported: `SetToken`, `ChildPID()`, `State()`, `Reset()`, `Stop()`, package-level `Now`/`nowFunc`, any `LoadReader(io.Reader)`. Confirm `setTokenForTest` (T035) is lowercase and thus unexported
- [X] T053 Append a new section "Exported API — locked at SDD-19" to `docs/PACKAGE-MAP.md` covering `internal/supervise` per Prompt 5 step 5: list `type State` (5 constants), `type Event` (15 constants), `type Clock interface`, `type Store struct` (opaque), `func NewStore(ctx, clock) *Store`, `func (*Store) Transition(ctx, event) error`, `func (*Store) Snapshot() Snapshot`, `type Snapshot struct`, `var ErrInvalidTransition`. Insert in alphabetical order relative to existing package entries
- [X] T054 [P] Update `docs/AC-MATRIX.md` AC-10 row's "test files" column to include `internal/supervise/state_test.go` per Prompt 5 step 6
- [X] T055 [P] Update `docs/SDD-PLAYBOOK.md` SDD-19 row: change status from `in-progress` (or whatever the current value is) to `done` per Prompt 5 step 7
- [X] T056 Stage and commit all changes in a single combined commit per Prompt 5 step 8 of `docs/sdd/SDD-19.md`: `git add internal/supervise/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md specs/019-supervise-state/tasks.md` then `git commit -m "feat(supervise): state machine + Store + Snapshot (SDD-19)"`. Do NOT run `git push` — pushing is operator-discretion at the chunk boundary
- [X] T057 Final verification: print the final-message confirmation per Prompt 5: gates passed (`format:fix`, `lint`, `test:race` all clean), race-clean on the concurrent test, coverage ≥ 95% on `internal/supervise/state.go`, state-table matches `docs/LIFECYCLE-SCENARIOS.md` (75 cells, 19 legal, 56 illegal), Snapshot defensiveness proven (T-08), Token redaction proven (T-09), AC-10 row updated, SDD-PLAYBOOK updated, combined commit created

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately on a fresh `019-supervise-state` branch
- **Foundational (Phase 2)**: Depends on Setup — T004 (the stub `state.go`) BLOCKS every test-writing task since tests must compile against the locked types
- **User Stories (Phase 3-7)**: All depend on Foundational completion. US1 must precede US2..US5 in Go-implementation terms because US1 lays down the `Transition` happy path and the locking discipline that US2..US5 build on; the test-writing halves of US2..US5 may be written in parallel with US1's tests since `state_test.go` accepts simultaneous edits if coordinated (in practice, since one developer is implementing this whole chunk, work them sequentially within `state_test.go` to avoid merge conflicts)
- **Polish (Phase 8)**: Depends on US1-US5 all complete

### User Story Dependencies

- **US1 (P1) — Lifecycle source-of-truth**: First. Lays down constants, maps, `Store`, `NewStore`, `Snapshot`, and the `Transition` happy path
- **US2 (P1) — Illegal rejection**: Depends on US1's `Transition` happy path (extends the lookup-miss branch with the wrapped sentinel error)
- **US3 (P1) — Defensive copy + race-clean**: Depends on US1's `Snapshot` and locking discipline (audits and reinforces them)
- **US4 (P1) — Token redaction**: Depends on US1's `Snapshot` (Token field already declared) and on T035's package-private `setTokenForTest` seam
- **US5 (P2) — No goroutines / no side-effects**: Verification of US1-US4; no new behaviour code, only audits

### Within Each User Story

- Test-writing tasks (T010-T014, T023-T024, T029-T030, T036-T037, T041) MUST land **first** and MUST FAIL (red) before any implementation in that story
- Implementation tasks satisfy the red tests in the order locked by `contracts/test-catalogue.md` §"TDD ordering": constants → sentinels & maps → Clock → Store/NewStore → Snapshot → Transition

### Parallel Opportunities

- T002 [P] (verify SDD-02 surface) runs in parallel with T001 (verify branch)
- T005, T006, T007 [P] (the three vocabulary-protection tests) can be authored in parallel since each targets an independent map/panic check
- T010-T014 [P] (US1 tests) can be drafted in parallel — different sub-functions inside `state_test.go`. Coordinate the file-merge step
- T023, T024 [P] (US2 tests) can be drafted in parallel
- T029, T030 [P] (US3 tests) can be drafted in parallel
- T036, T037 [P] (US4 tests, after T035) can be drafted in parallel
- T054, T055 [P] (final docs updates to two different files) can run in parallel

---

## Parallel Example: User Story 1

```bash
# Write all five US1 tests in parallel (different sub-functions of state_test.go):
Task: "Write TestNewStore_InitialState in internal/supervise/state_test.go (T010)"
Task: "Write TestStore_LegalTransitions table-driven over 19 legal cells (T011)"
Task: "Write TestStore_StopIsIdempotent (T012)"
Task: "Write TestStore_GraceRestartReentry (T013)"
Task: "Write TestStore_TwoStepRecoveryFromAwaitingApproval (T014)"

# Then run the red gate:
go test ./internal/supervise/ -run 'TestNewStore_InitialState|TestStore_Legal|TestStore_Stop|TestStore_Grace|TestStore_TwoStep'
# Expect: FAIL on all five (the implementation is still stubbed)
```

---

## Implementation Strategy

### MVP (recommended for SDD-19): All five user stories at once

This chunk is small (one file) and the five user stories share a single implementation file. The recommended path is:

1. Phase 1 (Setup) — verify preconditions, create `doc.go`
2. Phase 2 (Foundational) — write the stub `state.go` so tests can compile; write the three vocabulary-protection tests and confirm they fail
3. Phase 3 (US1) — write the five US1 tests, then implement `state.go` step by step until they pass; vocabulary-protection tests turn green along the way
4. Phase 4 (US2) — write the two US2 tests, then add the wrapped-sentinel error path
5. Phase 5 (US3) — write the two US3 tests, audit the locking discipline, run the race gate
6. Phase 6 (US4) — write the two US4 tests, add the package-private `setTokenForTest` seam, confirm SDD-02 redaction holds
7. Phase 7 (US5) — write the goroutine-leak test, run the import-list audit
8. Phase 8 (Polish) — gates, coverage, docs updates, combined commit

### Incremental delivery within the chunk

Each user story leaves the test suite green at the end of its phase (US1 also turns the foundational vocabulary tests green). A reviewer can pause at any User Story checkpoint and verify story-level success independently.

---

## Notes

- [P] tasks = different file or independent sub-edit within the same file (test-writing tasks within `state_test.go` count as parallel because each authors a distinct top-level test function)
- [Story] label maps task to user story (US1..US5); foundational and polish tasks have NO story label
- Tests MUST fail (red) before the implementation that satisfies them (Constitution VIII — TDD-mandatory)
- Reasons map (T016) and transitions map (T017) are read-only `var` literals populated at package init — equivalent to the `var Err... = errors.New(...)` exception per Constitution IX (R-002, R-005)
- Token writes use the package-private `setTokenForTest` seam (T035); a public `SetToken` is **deliberately not exported** at this chunk per R-009 — production token writes are owned by SDD-21
- No `internal/testutil` package per R-011 — fakes (`fakeClock`) live inline in `state_test.go`
- No fuzz target at this layer per R-008 — events arrive as typed Go constants, not parsed strings
- The 5×15 = 75-cell matrix in `contracts/state-table.md` is the single source of truth: 19 legal cells = positive tests, 56 illegal cells = negative tests; SC-019-1 mandates 100% cell coverage
- Coverage ≥ 95% on `internal/supervise/state.go` per Constitution VIII High band and SC-019-6
- Combined commit at end of Phase 8 per Prompt 5 of `docs/sdd/SDD-19.md` — no intermediate commits between phases
