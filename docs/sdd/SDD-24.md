# SDD-24 — Supervisor orchestration glue (ACTIVATED — gap surfaced by SDD-25)

**Phase:** 5
**Status:** PENDING (activated by SDD-25 implement-phase analysis on 2026-05-12)
**Branch:** `024-supervisor-orchestration` (created when implementation starts)
**Blocked by:** SDD-19, SDD-20, SDD-21, SDD-22, SDD-23 (all done — primitives + CLI skeleton in place)
**Blocks:** SDD-25 (lifecycle harness — 15 scenarios — cannot complete without this chunk)
**Primary AC:** AC-10 (necessary prerequisite for the harness to drive supervisor scenarios end-to-end)
**Coverage target:** orchestrator unit tests ≥85% line coverage; SDD-25 integration scenarios become reachable once this chunk ships.

---

## Why this slot was activated

SDD-25's implement phase began on 2026-05-12. Within the read-and-plan phase
the implementing agent attempted to compose the locked SDD-19..22 primitives
inside the harness and discovered that **the production orchestrator that
glues those primitives into a daemon lifecycle does not exist**. The CLI
entrypoint `internal/cli/supervise.go` (the SDD-23 deliverable) ships only:

- the dry-run rendering path,
- pidfile acquisition,
- `Store`/`Grace`/`StatusServer`/`Refiller`/`Refresher` *construction*,
- a refresh-coalescer that fires only when an external caller sends
  `refresh\n` on the status socket,
- and the goroutine-management scaffolding that waits on `ctx.Done`.

There is **no code anywhere in the repository** that performs the daemon
lifecycle documented in `docs/LIFECYCLE-SCENARIOS.md`:

1. submit the initial signed `/claim` to the vault server,
2. receive the JWT + JTI and persist it to the Store,
3. call `Refiller.Refill(ctx, scopes)` to fetch + decrypt + cache secrets
   into `Grace`,
4. run the configured validators against each fetched secret,
5. build the `supervise.ChildConfig` env block from the cached `Grace`
   secrets and call `NewChild` + `Start`,
6. invoke `Child.Wait()` on a supervisor goroutine,
7. branch on the returned exit code (0 → silent refill; non-zero,
   non-78 → silent refill; 78 → emit `[STALE] Child Exit 78` alert
   and transition to `awaiting-approval`),
8. on `Refresher` window-tick, submit a fresh signed `/claim` and update
   the cached JWT atomically,
9. on `Refiller.Refill` returning `ErrJTIUnknown`, transition to
   `awaiting-approval` and emit the `[STALE] Vault Rejected JWT` alert,
10. on boot, retry-with-backoff against Tailscale + vault reachability
    up to `boot_retry_timeout` before exiting with `ErrBootTimeout`.

`grep` confirms the absence: `Refiller.Refill` is referenced exactly once in
`internal/cli/supervise.go` — inside the refresh-coalescer's `perform`
closure that the status socket invokes; it is never called at boot. `NewChild`
is referenced zero times outside test files in `internal/supervise/`.
`Child.Wait` is invoked nowhere in `internal/cli/`. No code anywhere emits
audit events for the supervisor-scope vocabulary documented in
`specs/025-lifecycle-harness/data-model.md §2`
(`supervisor_session_claimed`, `supervisor_silent_refill`,
`supervisor_child_exit_78`, `supervisor_awaiting_approval`,
`supervisor_stale_alert`, `supervisor_grace_entered`, `supervisor_grace_exited`,
`supervisor_session_refreshed`, `session_requested`, `session_approved`,
`secret_fetched`, `token_expired`, `client_refresh_invoked`).

## Scope of the gap

| SDD-25 scenario | Why it cannot reach its documented final state |
|-----------------|------------------------------------------------|
| 02 DaemonBootstrap | nothing submits the initial `/claim` or fetches secrets; child never starts |
| 03 CleanExitSilentRefill | no `Child.Wait` consumer; nothing reacts to exit 0 |
| 04 ChildCrashSilentRefill | same as 03 |
| 05 Exit78StaleCreds | no exit-78 dispatcher; no alert emitter (SDD-28 also pending) |
| 06 ValidatorBlocksChild | no validator invocation point; SDD-26 also pending |
| 07 VaultRestart | no `ErrJTIUnknown` handler in the lifecycle layer |
| 08 DaytimeRefresh | `Refresher.Run` runs but its callback only triggers refresh-coalescer; never re-submits a fresh `/claim` |
| 09a/09b OvernightExpiry | no `token_expired` detection; no grace-restart dispatcher |
| 10/Supervisor DiscordUnavailable | nothing submits the initial `/claim` |
| 11 TailscaleBootRetry | no boot-retry loop exists |
| 12 StatusGate | status socket works; but the `scope_healthy`/`scope_stale` fields are populated only by orchestration logic that does not exist |
| 13 RotationMidSession | refresh-coalescer wiring works; but it never restarts the child because no child-lifecycle owner exists |
| 14 DuplicateSupervisor | the pidfile flock works; the *second* process refuses correctly — partial pass possible if Scenario 14 is run with only the pidfile primitive, no full lifecycle |
| 15 LogPatternAlert | no watchdog (SDD-27 pending); no log-pattern matcher in the supervisor |

Scenario 01 (`FirstInteractive`) is the only scenario that does **not** need
the supervisor orchestrator — it exercises `hush request` → `internal/server`
end-to-end. It would still need a harness, but it is not blocked by this
gap.

## Decision: activate SDD-24 before resuming SDD-25

Per the original `docs/sdd/SDD-24.md` Outcome B decision rule, this slot is
now activated. The orchestrator MUST be designed and shipped before SDD-25
can complete its 15-scenario contract.

## Chunk identity (to be filled out by the Specify phase)

- **Package:** `internal/supervise/lifecycle` (new sub-package; do NOT
  pollute `internal/supervise` proper since SDD-19..22 are locked) — OR
  expand `internal/cli/supervise.go` with all orchestration inline. The
  decision belongs to the Plan phase.
- **Branch:** `024-supervisor-orchestration`
- **Primary AC:** AC-10 (precondition for SDD-25's 15 scenarios)
- **Blockers:** none beyond SDD-19..23 (already done)
- **Blocks:** SDD-25, SDD-26, SDD-27, SDD-28 (all depend on the orchestrator
  to host their alert/validator/watchdog hooks)
- **Coverage target:** 85% line coverage on the new orchestrator package;
  one unit test per branch of the exit-code dispatcher and the boot-retry
  loop.

## Anti-contracts (so the orchestrator does not become its own gap)

- Do NOT mutate the SDD-19..22 locked surfaces. The orchestrator consumes
  only the exported APIs (`Store.Transition`, `Store.Snapshot`,
  `Refiller.Refill`, `Refresher.Run`, `Grace.Set`/`.Get`,
  `StatusServer.AttachStatusInputs`/`AttachRefreshHandler`,
  `Child.Start`/`Wait`/`Forward`, `AcquirePidFile`).
- Do NOT pre-define the validator interface here — SDD-26 owns that. The
  orchestrator MUST accept a `Validator` interface as a `Deps` field with
  a no-op default so SDD-25 scenarios that do not test validation can pass.
- Do NOT pre-define the alert classes here — SDD-28 owns that. The
  orchestrator MUST accept an `Alerts` interface as a `Deps` field with a
  no-op default for the same reason.
- Do NOT inline the watchdog — SDD-27 owns it. Provide a hook interface.
- Do NOT introduce a new audit-event vocabulary by side-channel. Either
  reuse the existing `audit.Action*` constants (`secret_retrieved`,
  `vault_reloaded`, `claim_outcome`, `auth_failed`, `discord_disconnected`,
  etc.) or extend `internal/audit/chain.go`'s action constant block
  before the orchestrator references new names. The names referenced by
  SDD-25's data-model.md §2 are aspirational; SDD-24's Plan phase MUST
  reconcile the documented set against the implemented set (either by
  renaming the documentation, by adding constants, or both).

## How to handle this slot from here

1. Run the standard 5-prompt SDD workflow against this file (Specify,
   Clarify, Plan, Tasks, Implement). Specify and Plan are verbose; the
   remainder are lean.
2. After SDD-24 ships green, restart SDD-25 from its Specify prompt.
   The harness file allocation and the 17 `Test_Scenario_*` symbol set
   remain valid; only the bodies that depend on the orchestrator can be
   filled in.
3. Update `docs/AC-MATRIX.md` AC-9 + AC-10 rows once SDD-25 actually
   reaches 15/15 green.

## Cross-references

- Original SDD-25 owner of the lifecycle scenarios: [docs/sdd/SDD-25.md](SDD-25.md)
- Workflow expectations: [docs/SDD-PLAYBOOK.md](../SDD-PLAYBOOK.md)
- Spec referencing the supervisor lifecycle: [docs/LIFECYCLE-SCENARIOS.md](../LIFECYCLE-SCENARIOS.md)
- Audit-event vocabulary referenced by SDD-25 scenarios: [specs/025-lifecycle-harness/data-model.md §2](../../specs/025-lifecycle-harness/data-model.md)
- Implemented audit-event vocabulary: [internal/audit/chain.go](../../internal/audit/chain.go) (`Action*` constants) + [internal/server/approver.go](../../internal/server/approver.go) (`Audit*` event types)
