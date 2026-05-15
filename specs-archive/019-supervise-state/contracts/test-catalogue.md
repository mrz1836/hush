# Test Catalogue — SDD-19

**Branch**: `019-supervise-state` | **Date**: 2026-05-05

This catalogue maps each required test to the spec FR / SC it
satisfies. `/speckit-tasks` will translate each row into a
TDD-mandatory test-writing task. Constitution VIII requires the
test to be authored **before** the implementation that satisfies
it.

All tests live in `internal/supervise/state_test.go` (R-011). Test
names follow the `TestFunctionName_Scenario` PascalCase
convention from `.github/tech-conventions/testing-standards.md`.

## Test catalogue

| # | Test name | Form | Satisfies | Notes |
|---|-----------|------|-----------|-------|
| T-01 | `TestNewStore_InitialState` | unit | FR-019-16, edge case "Snapshot of an empty store" | Constructs a store with a fake clock; asserts `Snapshot()` returns `State == StateFetching`, `ChildPID == 0`, `Token == nil`, `Reason == "store constructed"`, `LastTransitionAt == fakeClock.Now()` at construction. |
| T-02 | `TestNewStore_NilClockPanics` | unit | R-004 | Asserts `NewStore(ctx, nil)` panics with a clear programmer-error message; recovers via `defer recover()`. |
| T-03 | `TestStore_LegalTransitions` | unit, table-driven | FR-019-3, FR-019-4, FR-019-7, FR-019-20, SC-019-1 (positive half) | Iterates the 19 legal cells from `state-table.md`. For each cell: construct a store with a fake clock, drive it into the source state via the documented prefix sequence, advance the clock, apply the event, assert post-state matches, `LastTransitionAt == newNow`, `Reason == reasons[event]`, no error. |
| T-04 | `TestStore_IllegalTransitionErr` | unit, table-driven | FR-019-5, FR-019-6, FR-019-15, SC-019-1 (negative half), SC-019-7 | Iterates the 56 illegal cells. For each cell: drive the store into the source state, capture pre-snapshot, apply the rejected event, assert `errors.Is(err, ErrInvalidTransition)`, assert the rendered error string contains both the source state and the event name, capture post-snapshot, assert post-snapshot is byte-for-byte equal to pre-snapshot (state, childPID, lastTransitionAt, token, reason all unchanged). |
| T-05 | `TestStore_StopIsIdempotent` | unit | FR-019-17, R-010 | Drive store to `StateStopped` via `EventStopRequested`; advance the fake clock; apply `EventStopRequested` again; assert no error, state still `StateStopped`, `LastTransitionAt` advanced to the new now, `Reason` set to `reasons[EventStopRequested]`. Then assert any non-stop event from `StateStopped` returns `ErrInvalidTransition` with the store unchanged. |
| T-06 | `TestStore_GraceRestartReentry` | unit | FR-019-18, Clarification 3 | Drive `running → grace-restart → running → grace-restart → running` (legal sequence); assert no error at any step; assert no per-session counter caps the re-entry; finally drive `grace-restart → awaiting-approval` via `EventGraceExpired` to confirm the only termination edge. |
| T-07 | `TestStore_TwoStepRecoveryFromAwaitingApproval` | unit | FR-019-19, Clarification 2 | From `StateAwaitingApproval`, apply `EventApprovalGranted` (must land on `StateFetching`, not `StateRunning`); then apply `EventFetchOK` (must land on `StateRunning`). Also assert the inverse: `EventApprovalGranted` from `StateAwaitingApproval` followed by any non-`EventFetchOK` event from `StateFetching` is handled per the table. |
| T-08 | `TestStore_SnapshotIsDefensiveCopy` | unit | FR-019-8, SC-019-4 | Acquire a snapshot from a populated store; mutate every public field of the snapshot value (set `State` to a different value, set `ChildPID` to a different number, advance `LastTransitionAt`, set `Reason` to a different string, set `Token` to nil); acquire a second snapshot; assert second snapshot is byte-for-byte equal to the original (proving the mutation did not propagate to the store). |
| T-09 | `TestStore_TokenLogValueRedacts` | unit | FR-019-9, FR-019-10, SC-019-3, Constitution X | Construct a `*securebytes.SecureBytes` from a known plaintext (e.g. `"sensitive-jwt-bytes-abc"`). Stash it on the store via the test-only seam (R-009). Acquire snapshot. Build a `slog.Handler` writing to a `bytes.Buffer`. Log the snapshot via `slog.LogAttrs(ctx, slog.LevelInfo, "supervise.snapshot", slog.Any("snap", snap))`. Assert the buffer contains the literal `[redacted]` and contains zero bytes from the plaintext. Repeat for the bare `Token` field logged in isolation. |
| T-10 | `TestStore_TokenZeroOnRelease` | unit | FR-019-11, R-007 | Stash a token; drive a transition that releases it (test-only seam in this chunk; production wiring of token-clearing transitions is owned by SDD-21); call `Use(fn)` on the released token, assert `errors.Is(err, securebytes.ErrDestroyed)`. |
| T-11 | `TestStore_ConcurrentTransitionAndSnapshot` | race | FR-019-14, SC-019-2 | Spawn N goroutines (e.g. 8) driving a known-legal cycle of transitions (`fetching → running → fetching → ...`) under a fake clock; spawn M goroutines (e.g. 8) calling `Snapshot()` in a tight loop; run for 100 iterations of the cycle each. Assertions: `go test -race` reports zero races; every observed snapshot's `(State, ChildPID, LastTransitionAt)` triple is from a single commit point (no torn read — verifiable by ensuring `LastTransitionAt` only takes values produced by completed transitions). |
| T-12 | `TestStore_NoSideEffects` | unit | FR-019-12, FR-019-13, SC-019-5 | Capture `runtime.NumGoroutine()` before and after a full lifecycle suite (construct, drive ~30 events covering all legal edges, snapshot, stop). Assert delta is 0. (Filesystem / network checks are covered by code review + the fact that the package imports neither `os`, `net`, nor any side-effecting stdlib.) |
| T-13 | `TestReasons_KeysetMatchesEventVocabulary` | unit | FR-019-21, R-005 | Assert the package-internal `reasons` map's keyset is exactly the closed event vocabulary — no missing event, no extra phrase. Catches drift if a future PR adds an `Event` constant without a phrase or vice versa. |
| T-14 | `TestTransitions_KeysetMatchesStateVocabulary` | unit | FR-019-1, FR-019-21 | Assert the package-internal `transitions` map's outer keyset is exactly the closed state vocabulary, AND that every inner-map entry's destination value is also in the closed state vocabulary. Catches drift if a future PR adds an unsupported state value. |
| T-15 | `TestStore_TransitionUnknownEvent` | unit | FR-019-21 | Apply an `Event("garbage-not-in-set")` value (a typed string outside the closed constant set) to a fresh store; assert `errors.Is(err, ErrInvalidTransition)`; assert the rendered error string names both the current state and the rejected event verbatim. |

## Coverage gating

- **Branch / line coverage ≥ 95%** on `internal/supervise/` files
  in this chunk's scope (`doc.go`, `state.go`). Verified via
  `go test -cover ./internal/supervise/ -run State` plus manual
  inspection that the `config/` subpackage's coverage is reported
  separately.
- All tests run under `go test -race` in CI.
- No new fuzz target is introduced (R-008).

## TDD ordering for `/speckit-tasks`

The implementation tasks must be authored in this order to honour
Constitution VIII (TDD-mandatory):

1. **Test-first phase** — author T-01 through T-15 as red tests
   (compile-error or runtime-fail) against a stub `state.go` that
   declares only the type signatures locked in `contracts/go-api.md`.
2. **Implementation phase** — implement `state.go` until each
   test passes. Order:
   - Constants (`State`, `Event`).
   - Sentinel error and the `reasons` and `transitions` maps.
   - `Clock` interface and the unexported `realClock`.
   - `Store` struct + `NewStore`.
   - `Snapshot` + `Snapshot()` accessor.
   - `Transition` (last — depends on every other piece).
3. **Verification phase**:
   - `magex format:fix && magex lint && magex test:race`
   - `go test -cover ./internal/supervise/ -run State` ≥ 95%
   - Confirm the state-table tests cover every cell (a manual
     audit of the test-table length vs the matrix in
     `contracts/state-table.md`).
4. **PACKAGE-MAP update** — append the `Exported API — locked at
   SDD-19` section per Prompt 5 step 5.
5. **AC-MATRIX update** — populate the AC-10 row's "test files"
   column with `internal/supervise/state_test.go`.
6. **SDD-PLAYBOOK** — mark SDD-19 status `done`.
