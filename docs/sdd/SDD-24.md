# SDD-24 — Supervisor orchestration glue (ACTIVATED — gap surfaced by SDD-25)

**Phase:** 5
**Package:** `internal/cli` (expansion of `internal/cli/supervise.go`) — Plan phase may instead place the orchestrator in a new `internal/supervise/lifecycle` sub-package; SDD-19..22 surfaces are locked either way.
**Files:** `internal/cli/supervise.go` (expanded), `internal/cli/supervise_lifecycle.go` (new, ~600 LOC), `internal/cli/supervise_lifecycle_test.go`, `internal/cli/supervise_lifecycle_integration_test.go`
**Branch:** `024-supervisor-orchestration` (created by the `before_specify` git hook when Prompt 1 runs)
**Blocked by:** SDD-19, SDD-20, SDD-21, SDD-22, SDD-23 (all done — primitives + CLI skeleton in place)
**Blocks:** SDD-25 (lifecycle harness — 15 scenarios), SDD-26 (validators), SDD-27 (watchdog), SDD-28 (alerts) — all need the orchestrator as the host for their hooks
**Primary AC:** AC-10 (precondition for SDD-25's 15-scenario contract)
**Coverage target:** 85% line coverage on the orchestrator file(s); one unit test per branch of the exit-code dispatcher (0 / non-zero non-78 / 78) and per branch of the boot-retry loop (immediate success / N failures then success / timeout exhausted).

**Behaviour contracts (MUST):**
- Submit the initial signed `/claim` to the vault server at boot, persist the JWT + JTI into `supervise.Store`.
- Call `Refiller.Refill(ctx, scopes)` at boot AFTER the claim succeeds; on success, cached secrets are held in `Grace`.
- Run every configured validator against the fetched secrets BEFORE building the child env. Validators are an injected interface (`Validator`) with a no-op default — SDD-26 supplies the real impl. Failure → emit alert, transition to `awaiting-approval`, do NOT start child.
- Build `supervise.ChildConfig` env from cached `Grace` secrets, call `NewChild(cfg)` and `Start(ctx)`. Zero secret bytes BEFORE child env serialisation per Constitution X.
- Run `Child.Wait()` on a supervisor goroutine. Branch on returned exit code:
  - `0` → silent refill + restart;
  - non-zero non-`78` → silent refill + restart (existing JWT TTL is still valid);
  - `78` → emit `[STALE] Child Exit 78` alert, transition to `awaiting-approval`, do NOT restart.
- On `Refresher` window-tick, submit a fresh signed `/claim`, swap the cached JWT atomically, keep child running uninterrupted.
- On `Refiller.Refill` returning `supervise.ErrJTIUnknown`, transition to `awaiting-approval`, emit `[STALE] Vault Rejected JWT` alert, await re-approval (via Discord OR `client refresh`).
- Boot retry with exponential backoff against Tailscale reachability + vault `/hz` reachability up to `boot_retry_timeout`. Exhaustion → `supervise.ErrBootTimeout` + exit ExitErr.
- Wire SDD-27's watchdog as a `Watchdog` interface field (no-op default) — orchestrator forwards `Child.Stderr` reads to the watchdog when configured.
- Wire SDD-28's alert classes via an `Alerts` interface field (no-op default) — orchestrator calls `Alerts.Emit(class, payload)` at every documented alert site.
- Reconcile audit-event vocabulary referenced by SDD-25 with what `internal/audit/chain.go` actually emits. Either extend the constant block OR rename the data-model.md table. Both are acceptable; Plan phase decides.

**Anti-contracts (MUST NOT):**
- Mutate any SDD-19..22 exported surface. Consume only the locked APIs (`Store.Transition`, `Store.Snapshot`, `Refiller.Refill`, `Refresher.Run`, `Grace.Set/.Get`, `StatusServer.AttachStatusInputs`/`AttachRefreshHandler`, `Child.Start/.Wait/.Forward`, `AcquirePidFile`).
- Pre-define the validator implementation (SDD-26 owns that).
- Pre-define alert classes (SDD-28 owns those).
- Inline the watchdog (SDD-27 owns it). Hook via interface.
- Add platform branching in this file (delegate to existing platform files per Constitution VII).
- Add `runtime.GOOS`, raw `78` literal arithmetic, raw state-string literals, or `case StateRunning`/`switch state` patterns. State-table reasoning is owned by SDD-19; exit-78 reasoning is owned by SDD-20. Call into the packages.
- `string(*securebytes.SecureBytes)` or any plaintext-byte materialisation outside `Refiller`'s permitted JWT bearer-header site (Constitution X).
- New `go.mod` direct dependency (Constitution XI).
- Spawn a goroutine without owner + ctx + termination + top-frame `recover()` (Constitution IX).
- Block the supervisor on a slow child stdout/stderr drain (the SDD-20 `Child.drainLoop` already handles this; the orchestrator MUST NOT add a second drain).

**Tests required:**
- Unit (in `supervise_lifecycle_test.go`, ~25 cases): TestOrchestrator_BootSubmitsClaim, TestOrchestrator_ClaimDeniedTransitionsToAwaitingApproval, TestOrchestrator_RefillJTIUnknownTransitionsToAwaitingApproval, TestOrchestrator_ValidatorFailureBlocksChildStart, TestOrchestrator_ChildExitZeroTriggersSilentRefill, TestOrchestrator_ChildExitNonZeroTriggersSilentRefill, TestOrchestrator_ChildExit78EmitsStaleAlertNoRestart, TestOrchestrator_RefresherTickSubmitsFreshClaim, TestOrchestrator_BootRetryExponentialBackoff, TestOrchestrator_BootRetryTimeoutExhausted, TestOrchestrator_GracefulShutdownDrainsChild, TestOrchestrator_NoBusinessLogicInOrchestrator (grep test asserts no state-table / exit-78 / state-string literals).
- Integration (in `supervise_lifecycle_integration_test.go`, build-tagged `//go:build integration`, ~5 cases driving full boot → claim → refill → child-start against a `httptest.Server` + `testutil.DiscordStub`).
- All tests inject a `FakeClock` and a controllable `Validator`/`Alerts`/`Watchdog` triple.

**Constitutional principles in scope:**
- **IV** (TTL discipline; supervisor zeroes secrets after child handoff via Grace; `--no-cache` strict mode behaves as documented).
- **V** (every documented alert site fires; status socket reflects the orchestrator's transitions).
- **VII** (no per-OS branches in the orchestrator; delegate to SDD-22 platform helpers for socket paths).
- **VIII** (TDD-mandatory; tests written before implementation; race detector enabled).
- **IX** (every spawned goroutine has owner + ctx + termination + recover; no `init`; no package-level mutable globals; the orchestrator owns the supervisor goroutine inventory).
- **X** (no `string(secretBytes)` outside the permitted JWT bearer-header site; alert payloads carry scope name only — never the secret value; sentinel-leak tests assert no marker bytes escape stdout/stderr/logs/alert payloads).

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- No new package-level exported symbols in `internal/cli` (cobra command tree is the locked surface).
- If the Plan phase chooses the new sub-package `internal/supervise/lifecycle`: lock its `Orchestrator` type, `Deps` struct, `New(Deps) (*Orchestrator, error)`, `Run(ctx) error`. Interfaces `Validator`, `Alerts`, `Watchdog` on `Deps` — no-op defaults wired when nil.

---

## Why this slot was activated

SDD-25's implement phase began on 2026-05-12. Within the read-and-plan phase the implementing agent attempted to compose the locked SDD-19..22 primitives inside the harness and discovered that **the production orchestrator that glues those primitives into a daemon lifecycle does not exist**. The CLI entrypoint `internal/cli/supervise.go` (the SDD-23 deliverable) ships only:

- the dry-run rendering path,
- pidfile acquisition,
- `Store`/`Grace`/`StatusServer`/`Refiller`/`Refresher` *construction*,
- a refresh-coalescer that fires only when an external caller sends `refresh\n` on the status socket,
- and the goroutine-management scaffolding that waits on `ctx.Done`.

There is **no code anywhere in the repository** that performs the daemon lifecycle documented in `docs/LIFECYCLE-SCENARIOS.md`:

1. submit the initial signed `/claim` to the vault server,
2. receive the JWT + JTI and persist it to the Store,
3. call `Refiller.Refill(ctx, scopes)` to fetch + decrypt + cache secrets into `Grace`,
4. run the configured validators against each fetched secret,
5. build the `supervise.ChildConfig` env block from the cached `Grace` secrets and call `NewChild` + `Start`,
6. invoke `Child.Wait()` on a supervisor goroutine,
7. branch on the returned exit code (0 → silent refill; non-zero, non-78 → silent refill; 78 → emit `[STALE] Child Exit 78` alert and transition to `awaiting-approval`),
8. on `Refresher` window-tick, submit a fresh signed `/claim` and update the cached JWT atomically,
9. on `Refiller.Refill` returning `ErrJTIUnknown`, transition to `awaiting-approval` and emit the `[STALE] Vault Rejected JWT` alert,
10. on boot, retry-with-backoff against Tailscale + vault reachability up to `boot_retry_timeout` before exiting with `ErrBootTimeout`.

`grep` confirms the absence: `Refiller.Refill` is referenced exactly once in `internal/cli/supervise.go` — inside the refresh-coalescer's `perform` closure that the status socket invokes; it is never called at boot. `NewChild` is referenced zero times outside test files in `internal/supervise/`. `Child.Wait` is invoked nowhere in `internal/cli/`. No code anywhere emits audit events for the supervisor-scope vocabulary documented in `specs/025-lifecycle-harness/data-model.md §2` (`supervisor_session_claimed`, `supervisor_silent_refill`, `supervisor_child_exit_78`, `supervisor_awaiting_approval`, `supervisor_stale_alert`, `supervisor_grace_entered`, `supervisor_grace_exited`, `supervisor_session_refreshed`, `session_requested`, `session_approved`, `secret_fetched`, `token_expired`, `client_refresh_invoked`).

### Scope of the gap

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
| 12 StatusGate | status socket works; but `scope_healthy`/`scope_stale` are populated only by orchestration logic that does not exist |
| 13 RotationMidSession | refresh-coalescer wiring works; but it never restarts the child because no child-lifecycle owner exists |
| 14 DuplicateSupervisor | the pidfile flock works; the second process refuses correctly — partial pass possible with only the pidfile primitive |
| 15 LogPatternAlert | no watchdog (SDD-27 pending); no log-pattern matcher in the supervisor |

Scenario 01 (`FirstInteractive`) is the only scenario that does NOT need the supervisor orchestrator — it exercises `hush request` → `internal/server` end-to-end. It would still need a harness, but it is not blocked by this gap.

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All commits for this chunk are deferred to a single combined commit at the end of Prompt 5 (Implement). Do not commit between phases.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-24 (supervisor
orchestration glue — ACTIVATED by SDD-25's implement phase) of
the hush project.

Read first (in order — entire docs):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles IV, V, VII, VIII, IX, X all load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11, FR-12, FR-13, FR-22; AC-10)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (the 15 scenarios — every diagram drives a behaviour contract)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (48h walkthrough)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (Supervisor Config File — the orchestrator consumes it)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (every internal/* package the orchestrator wires)
- /Users/mrz/projects/hush/docs/sdd/SDD-24.md  (the full chunk contract — this file; especially "Why this slot was activated")
- /Users/mrz/projects/hush/docs/sdd/SDD-23.md  (what SDD-23 actually shipped vs. the gap)
- /Users/mrz/projects/hush/internal/cli/supervise.go  (the existing skeleton — read every line)

About this chunk (one-paragraph intent, for the spec's overview):
SDD-24 ships the production orchestrator that drives the
documented supervisor daemon lifecycle end-to-end against the
locked SDD-19..22 primitives. The orchestrator submits the
initial signed /claim, calls Refiller.Refill at boot, runs the
configured validators (via injected interface), builds the
child env from Grace, starts the child via NewChild, runs
Child.Wait on a supervisor goroutine, branches on exit codes
0 / non-zero-non-78 / 78, handles ErrJTIUnknown by transitioning
to awaiting-approval, fires alerts (via injected interface) at
every documented alert site, advances the JWT on Refresher
window ticks, and exits cleanly on SIGTERM/SIGINT. It does NOT
pre-define validators, alerts, or the watchdog — those are
SDD-26/27/28 — but it MUST host the interfaces those chunks fill.

The spec MUST encode these acceptance-level (WHAT)
requirements. Override any /speckit-specify "informed guess"
that would soften them:

- Boot sequence: pidfile → state.Store init → spawn StatusServer
  + Refresher goroutines → boot-retry loop (Tailscale + vault
  /hz reachable) up to boot_retry_timeout → initial /claim
  (signed via SDD-08) → JWT into Store → Refiller.Refill →
  validators → Child.Start → Child.Wait loop.
- Child-exit dispatch: 0 → silent refill + restart; non-zero
  non-78 → silent refill + restart; 78 → [STALE] alert +
  transition to awaiting-approval + DO NOT restart.
- Silent refill: Refiller.Refill succeeds → validators → Child.Start.
  Refiller returns ErrJTIUnknown → transition to awaiting-approval
  + [STALE] alert.
- Refresh: Refresher window-tick → submit fresh /claim → swap
  JWT atomically in Store → child continues uninterrupted.
- Shutdown: SIGTERM/SIGINT → ctx cancel → child.Forward(SIGTERM)
  → wait on child + every spawned goroutine → release pidfile.
- Validator interface: orchestrator accepts Validator on Deps
  with a no-op default. SDD-26 supplies the real impl.
- Alert interface: orchestrator accepts Alerts on Deps with a
  no-op default. SDD-28 supplies the 8 classes.
- Watchdog interface: orchestrator accepts Watchdog on Deps with
  a no-op default. SDD-27 supplies the log-pattern matcher.
- Audit reconciliation: orchestrator either uses existing
  audit.Action* constants OR extends the constant block before
  emitting new names. Spec MUST list the final reconciled
  vocabulary — no aspirational names that no code emits.
- Suite gates: orchestrator unit tests ≥85% line coverage; one
  test per exit-code branch; one test per boot-retry branch.

The spec MUST NOT encode HOW (no Go-specific goroutine layout,
no specific timer library choices, no library names). Those are
plan-phase.

Acceptance criterion: AC-10 (supervisor lifecycle — precondition
for SDD-25's 15-scenario contract).

Action — run exactly one command:
  /speckit-specify "internal/cli supervisor orchestrator: at boot submits signed /claim, runs initial Refiller.Refill, runs injected validators, builds child env from Grace and starts Child via NewChild, runs Child.Wait, dispatches on exit codes 0/non-zero-non-78/78, handles Refiller.ErrJTIUnknown by transitioning to awaiting-approval, advances JWT on Refresher window ticks, fires alerts via injected interface at every documented alert site, retries-with-backoff against Tailscale+vault reachability up to boot_retry_timeout, shuts down cleanly on SIGTERM; injected Validator/Alerts/Watchdog interfaces with no-op defaults so SDD-26/27/28 can fill them later; reconciles audit-event vocabulary with internal/audit/chain.go before emitting new names"

The before_specify hook will create branch 024-supervisor-orchestration.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution / LIFECYCLE-SCENARIOS.
Otherwise leave the marker — /speckit-clarify will handle it next
session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-24 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-24.md and
docs/LIFECYCLE-SCENARIOS.md (the scenarios doc is the source of
truth for every documented transition the orchestrator must
implement).

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-24 (supervisor orchestration
glue) of the hush project.

Read first (in order — entire docs):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; IV/V/VII/VIII/IX/X load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11..13, FR-22, AC-10)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (entire doc — every scenario diagram dictates an orchestrator branch)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (operational walkthrough)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (Supervisor Config — orchestrator consumes every field)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (every internal/* surface the orchestrator depends on — SDD-19..22 locks)
- /Users/mrz/projects/hush/docs/sdd/SDD-24.md  (this file)
- /Users/mrz/projects/hush/internal/cli/supervise.go  (the existing skeleton you will replace)
- /Users/mrz/projects/hush/internal/supervise/state.go  (Store API + Event/State vocabulary)
- /Users/mrz/projects/hush/internal/supervise/refill.go  (Refiller API + ErrJTIUnknown + ErrBootTimeout)
- /Users/mrz/projects/hush/internal/supervise/refresh.go  (Refresher API)
- /Users/mrz/projects/hush/internal/supervise/grace.go  (Grace API)
- /Users/mrz/projects/hush/internal/supervise/child.go  (Child API + Exit78 constant)
- /Users/mrz/projects/hush/internal/supervise/socket.go  (StatusServer API)
- /Users/mrz/projects/hush/internal/supervise/pidfile.go  (AcquirePidFile API)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope decision (the Plan phase MUST pick one):
- **Option A**: expand internal/cli/supervise.go inline. The
  orchestrator lives in the cli package. Simpler; reuses the
  existing skeleton. Plan-phase decision criterion: keeps file
  under ~700 LOC AND keeps cli/ free of business logic per the
  SDD-23 anti-contract.
- **Option B**: new sub-package internal/supervise/lifecycle
  housing the orchestrator. internal/cli/supervise.go becomes a
  thin shim that calls lifecycle.New + lifecycle.Run. Lifts
  business logic out of the cli layer (Constitution VII friendly).
  Plan-phase decision criterion: orchestrator > 700 LOC.

Implementation contract (HOW — Plan phase locks):
- Orchestrator owns three goroutines beyond StatusServer/Refresher:
    1. childWaitLoop: invokes Child.Wait, sends ExitCode on a chan.
    2. claimRefreshLoop: subscribes to Refresher window-tick
       callbacks, submits fresh /claim, updates Store atomically.
    3. mainLoop: state machine dispatcher consuming
       ExitCode chan, claim-result chan, ctx.Done.
  Each goroutine has owner + ctx + termination + top-frame recover.
- Boot retry: exponential backoff (Plan picks ratio + cap),
  Tailscale check via SDD-10's InterfaceLister seam, vault /hz
  check via a 2-second HTTP GET. Exhaustion → ErrBootTimeout +
  ExitErr.
- /claim submission: re-uses SDD-08 canonical-sign helpers.
  Caller layer DOES the signing (orchestrator owns the signing
  key handle; passes bytes into Refiller via Store.token).
- Validator interface MUST be defined here:
    type Validator interface {
        Validate(ctx context.Context, scope string, secret *securebytes.SecureBytes) error
    }
  with sentinel ErrValidationFailed wrapping a scope-named error.
- Alerts interface MUST be defined here:
    type Alerts interface {
        Emit(ctx context.Context, class AlertClass, payload AlertPayload)
    }
  with AlertClass enum (8 values from SDD-28 anticipation) +
  AlertPayload carrying scope/name/error-class strings (never
  the secret).
- Watchdog interface MUST be defined here:
    type Watchdog interface {
        OnStderrLine(ctx context.Context, line []byte)
    }
  no-op default.
- Audit reconciliation: Plan MUST list the final mapping table.
  Names referenced by SDD-25's data-model.md §2 that DO NOT
  exist in internal/audit/chain.go MUST either be added as
  constants OR renamed in the SDD-25 docs. Plan locks which.
- No platform branching in this file. Delegate to existing SDD-22
  platform helpers.

Coverage target: 85% line coverage on the orchestrator file(s);
SDD-19..22 coverage MUST remain at their locked levels (no
regression on the primitives).

Constitutional principles in scope: IV, V, VII, VIII, IX, X.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-24 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-24.md and
docs/LIFECYCLE-SCENARIOS.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE its implementation task — orchestrator behaviour is the lock, not the implementation. Coverage target: 85% line on the new orchestrator file(s). Tests required (illustrative — Plan phase pins the final list): TestOrchestrator_BootSubmitsClaim, TestOrchestrator_ClaimDeniedTransitionsToAwaitingApproval, TestOrchestrator_RefillJTIUnknownTransitionsToAwaitingApproval, TestOrchestrator_ValidatorFailureBlocksChildStart, TestOrchestrator_ChildExitZeroTriggersSilentRefill, TestOrchestrator_ChildExitNonZeroTriggersSilentRefill, TestOrchestrator_ChildExit78EmitsStaleAlertNoRestart, TestOrchestrator_RefresherTickSubmitsFreshClaim, TestOrchestrator_RefresherTickKeepsChildRunning, TestOrchestrator_BootRetryExponentialBackoff, TestOrchestrator_BootRetryTimeoutExhausted, TestOrchestrator_GracefulShutdownDrainsChild, TestOrchestrator_NoBusinessLogicLeakage (grep test asserting no state-string literals, no raw 78 arithmetic, no runtime.GOOS in the orchestrator file), TestOrchestrator_NoSentinelLeakage (sentinel marker never appears in any captured byte stream — operational logs, audit JSONL, alert payloads, status JSON, error message strings). One integration test asserting full boot → claim → refill → validator → child-start round-trip via httptest.Server + testutil.DiscordStub. Final phase MUST include magex format:fix, magex lint, magex test:race (≥85% coverage gate), and magex test:race -tags=integration."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-24 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-24.md and
docs/LIFECYCLE-SCENARIOS.md (every scenario diagram is a
behaviour contract this orchestrator MUST satisfy).

Run: /speckit-implement

Implement tasks in dependency order: interfaces (Validator,
Alerts, Watchdog) first, then audit-vocabulary reconciliation,
then the orchestrator boot path, then the child-exit dispatch
path, then the refresh path, then the shutdown path. Tests
written BEFORE the implementation they cover (Constitution VIII).

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Coverage gate — ≥85% line on the orchestrator file(s):
     magex test:coverage
3. Integration suite (if Plan phase added integration cases):
     magex test:race -tags=integration
4. Update docs/PACKAGE-MAP.md with the orchestrator's locked
   exported API (Plan-phase Option A → no new symbols; Option B →
   the lifecycle package's Orchestrator type + Deps + New + Run).
5. Update docs/AC-MATRIX.md AC-10 row: list the orchestrator
   tests as new evidence; row remains pending until SDD-25 ships
   green.
6. If audit-vocabulary reconciliation added new constants to
   internal/audit/chain.go, update docs/SPEC.md §FR-14 list.
7. Mark SDD-24 status `done` in docs/SDD-PLAYBOOK.md.
8. Update specs/025-lifecycle-harness/tasks.md: REMOVE the
   pause banner that currently sits before Phase 1, since the
   gap is now closed and SDD-25 implement can resume.

Make one combined commit:
  git add internal/cli/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md docs/sdd/SDD-24.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(supervise): orchestrator boot→claim→refill→child→exit dispatch (SDD-24)"

Final message: confirm all unit tests pass with -race + ≥85%
coverage, integration suite (if added) passes, AC-10 row cites
the orchestrator tests, SDD-PLAYBOOK updated, SDD-25 pause
banner removed, and the combined commit created. Note in the
final message that SDD-25 is now unblocked and can resume from
T001.

```

---

## Cross-references

- Original SDD-25 owner of the lifecycle scenarios: [docs/sdd/SDD-25.md](SDD-25.md)
- Workflow expectations: [docs/SDD-PLAYBOOK.md](../SDD-PLAYBOOK.md)
- Spec referencing the supervisor lifecycle: [docs/LIFECYCLE-SCENARIOS.md](../LIFECYCLE-SCENARIOS.md)
- Audit-event vocabulary referenced by SDD-25 scenarios: [specs/025-lifecycle-harness/data-model.md §2](../../specs/025-lifecycle-harness/data-model.md)
- Implemented audit-event vocabulary: [internal/audit/chain.go](../../internal/audit/chain.go) (`Action*` constants) + [internal/server/approver.go](../../internal/server/approver.go) (`Audit*` event types) + [internal/server/claim_handler.go](../../internal/server/claim_handler.go) (`AuditClaimOutcome`)
