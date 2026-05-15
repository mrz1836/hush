# State Transition Table — SDD-19

**Branch**: `019-supervise-state` | **Date**: 2026-05-05

This is the **single source of truth** for the legal
`(State, Event) → State` mappings encoded by the package-internal
`var transitions = map[State]map[Event]State{...}` literal in
`state.go`. Every cell below is transcribed from the diagrams in
`docs/LIFECYCLE-SCENARIOS.md` (Scenarios 2–15).

`SC-019-1` requires that **100% of legal cells** in this table
have a positive test case in `state_test.go`, and **100% of
illegal cells** have a negative test case asserting
`ErrInvalidTransition`. The 5 × 15 grid below has 75 cells; cells
marked `→ <state>` are legal (positive test required), cells
marked `✗` are illegal (negative test required).

## Matrix

Rows = current state. Columns = event. Cell = next state on
success, or `✗` for illegal.

| Event \\ State                  | `fetching`              | `running`              | `awaiting-approval`   | `grace-restart`         | `stopped`               |
|---------------------------------|-------------------------|------------------------|-----------------------|-------------------------|-------------------------|
| `EventFetchOK`                  | → `running`             | ✗                      | ✗                     | ✗                       | ✗                       |
| `EventFetchAuthRequired`        | → `awaiting-approval`   | ✗                      | ✗                     | ✗                       | ✗                       |
| `EventClaimDenied`              | → `awaiting-approval`   | ✗                      | ✗                     | ✗                       | ✗                       |
| `EventClaimUnavailable`         | → `awaiting-approval`   | ✗                      | ✗                     | ✗                       | ✗                       |
| `EventValidatorFailed`          | → `awaiting-approval`   | ✗                      | ✗                     | ✗                       | ✗                       |
| `EventBootRetryExhausted`       | → `stopped`             | ✗                      | ✗                     | ✗                       | ✗                       |
| `EventChildExitClean`           | ✗                       | → `fetching`           | ✗                     | ✗                       | ✗                       |
| `EventChildExitCrash`           | ✗                       | → `fetching`           | ✗                     | ✗                       | ✗                       |
| `EventChildExit78Stale`         | ✗                       | → `awaiting-approval`  | ✗                     | ✗                       | ✗                       |
| `EventRefreshRequested`         | ✗                       | → `fetching`           | ✗                     | ✗                       | ✗                       |
| `EventGraceRestartTriggered`    | ✗                       | → `grace-restart`      | ✗                     | ✗                       | ✗                       |
| `EventGraceRestartOK`           | ✗                       | ✗                      | ✗                     | → `running`             | ✗                       |
| `EventGraceExpired`             | ✗                       | ✗                      | ✗                     | → `awaiting-approval`   | ✗                       |
| `EventApprovalGranted`          | ✗                       | ✗                      | → `fetching`          | ✗                       | ✗                       |
| `EventStopRequested`            | → `stopped`             | → `stopped`            | → `stopped`           | → `stopped`             | → `stopped` (idempotent)|

## Legal-cell scenario provenance

Each legal cell traces back to an explicit scenario in
`docs/LIFECYCLE-SCENARIOS.md`:

| From | Event | To | Scenario reference |
|------|-------|----|--------------------|
| `fetching` | `EventFetchOK` | `running` | Scenario 2 (first daemon bootstrap, step 12); Scenario 3 (clean restart refill); Scenario 4 (crash restart refill); Scenario 7 (after re-approval); Scenario 13 (refresh-driven refetch) |
| `fetching` | `EventFetchAuthRequired` | `awaiting-approval` | Scenario 7 (vault server lost session); Scenario 9-without-grace (silent refill rejected) |
| `fetching` | `EventClaimDenied` | `awaiting-approval` | Scenario 2 inverted: operator denies the Discord prompt |
| `fetching` | `EventClaimUnavailable` | `awaiting-approval` | Scenario 10 (Discord unavailable) |
| `fetching` | `EventValidatorFailed` | `awaiting-approval` | Scenario 6 (validator catches bad secret) |
| `fetching` | `EventBootRetryExhausted` | `stopped` | Scenario 11 (Tailscale not ready, boot retry timeout) |
| `running` | `EventChildExitClean` | `fetching` | Scenario 3 |
| `running` | `EventChildExitCrash` | `fetching` | Scenario 4 |
| `running` | `EventChildExit78Stale` | `awaiting-approval` | Scenario 5 |
| `running` | `EventRefreshRequested` | `fetching` | Scenario 13 (rotate + `client refresh`) |
| `running` | `EventGraceRestartTriggered` | `grace-restart` | Scenario 9 (overnight expiry with grace cache, child crashes) |
| `grace-restart` | `EventGraceRestartOK` | `running` | Scenario 9 (success path) |
| `grace-restart` | `EventGraceExpired` | `awaiting-approval` | Scenario 9 (grace window elapsed) |
| `awaiting-approval` | `EventApprovalGranted` | `fetching` | Scenarios 5, 7 (operator re-approves) |
| any | `EventStopRequested` | `stopped` | Operator/host shutdown — all states |

## Notes on omitted edges

The matrix deliberately **omits** transitions that
`docs/LIFECYCLE-SCENARIOS.md` does not encode at the state-machine
layer:

- **No `fetching → fetching` retry edge**: claim retries (Scenario
  11 boot retries with backoff) are owned by the refill layer
  (SDD-21), not by the state machine. The state machine sees one
  successful or failed claim per `fetching` entry.
- **No JWT-only refresh edge from `running`**: Scenario 8 (session
  TTL nears expiry, refresh during waking hours) does NOT change
  the supervisor's posture toward its child — the child keeps
  running. FR-019-21's note locks this: "Background work that
  does NOT change the supervisor's posture toward its child …
  MUST NOT be modeled as a state-machine event at this layer."
  SDD-21 owns the refresh side-effect; the store stays in
  `StateRunning` throughout.
- **No `grace-restart → grace-restart` self-loop**: Re-entry into
  the grace path is modelled as `running → grace-restart →
  running → grace-restart → ...` (Clarification 3). The
  `EventGraceRestartOK` edge returns to `running` between grace
  attempts.
- **No edges out of `stopped` other than `EventStopRequested`**:
  `stopped` is terminal (FR-019-17). A fresh supervisor process
  with a fresh `Store` is required to recover.
- **No `awaiting-approval → stopped` event other than
  `EventStopRequested`**: The operator can deny re-approval, but
  that surfaces as continued waiting (the next claim attempt
  yields `EventClaimDenied` again from the next `fetching`
  entry); it does not auto-`stopped`.
- **No `fetching → running` shortcut after grace**: After
  `EventGraceExpired → awaiting-approval`, recovery is a two-step
  through `fetching` (Clarification 2 / FR-019-19) — there is no
  composite `awaiting-approval → running` event.

## Cell counts (audit aid)

- Total cells: 5 states × 15 events = **75**.
- Legal cells: 6 + 5 + 2 + 1 + 1 = **15** (events 1–6 from
  `fetching`, events 7–11 from `running`, events 12–13 from
  `grace-restart`, event 14 from `awaiting-approval`, event 15
  from any of the 5 states = 6 + 5 + 2 + 1 + 5 = **19**).
- Illegal cells: 75 − 19 = **56**.

`TestStore_LegalTransitions` MUST exercise all 19 legal cells.
`TestStore_IllegalTransitionErr` MUST exercise all 56 illegal
cells, asserting `errors.Is(err, ErrInvalidTransition)`,
unchanged-state, and unchanged-`LastTransitionAt`.
