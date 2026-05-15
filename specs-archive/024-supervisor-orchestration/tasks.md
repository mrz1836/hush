# Tasks: Supervisor Orchestrator (SDD-24)

**Input**: Design documents from [/specs/026-supervisor-orchestration/](.)
**Prerequisites**: [plan.md](plan.md) ✅, [spec.md](spec.md) ✅, [research.md](research.md) ✅, [data-model.md](data-model.md) ✅, [contracts/](contracts/) ✅, [quickstart.md](quickstart.md) ✅

**Tests**: **MANDATORY** (Constitution VIII — TDD). Every behaviour contract gets a failing test BEFORE its implementation task. Orchestrator behaviour is the lock, not the implementation. Coverage target: **≥85% line** on `internal/supervise/lifecycle*.go` under `magex test:race`.

**Organization**: Tasks are grouped by user story (US1..US5 from [spec.md](spec.md)) to enable independent verification of each acceptance scenario. Setup and Foundational phases must complete first.

## Format: `[ID] [P?] [Story?] Description`

- **[P]**: parallelizable — different files, no dependencies on unfinished tasks
- **[Story]**: maps task to US1..US5 from [spec.md](spec.md); Setup/Foundational/Polish phases carry no story label
- File paths are absolute-relative-to-repo-root

## Path Conventions

- Production code: [internal/supervise/](../../internal/supervise/), [internal/cli/](../../internal/cli/), [internal/audit/](../../internal/audit/)
- Unit tests: `*_test.go` co-located in [internal/supervise/](../../internal/supervise/)
- Integration tests: `lifecycle_integration_test.go` with `//go:build integration` build tag

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: docs alignment + audit-vocabulary scaffold so later phases can emit declared constants only.

- [ ] T001 Extend [internal/audit/chain.go](../../internal/audit/chain.go) constants block by appending the 12 new `Action*` constants per [contracts/audit-vocabulary.md](contracts/audit-vocabulary.md) §1 (`ActionSupervisorSessionClaimed`, `…SessionRefreshed`, `…SilentRefill`, `…ChildCleanExit`, `…ChildExitCrash`, `…ChildExit78`, `…AwaitingApproval`, `…StaleAlert`, `…GraceEntered`, `…GraceExited`, `…BootTimeout`, `ActionClientRefreshInvoked`). Append-only per the file's line-33 header; do NOT rename or remove any existing constant.
- [ ] T002 [P] Amend [docs/SPEC.md](../../docs/SPEC.md) §FR-14 audit-event list — add the two missing supervisor-scope names `supervisor_child_exit_crash` and `supervisor_boot_timeout` (Plan ADR-1; SC-026-008). Existing 10 entries unchanged.
- [ ] T003 [P] Add `TestSpecFR14AuditSync` to [internal/audit/chain_test.go](../../internal/audit/chain_test.go) — parses the `Action*` constants block from [internal/audit/chain.go](../../internal/audit/chain.go), parses the §FR-14 supervisor-scope subset from [docs/SPEC.md](../../docs/SPEC.md), asserts identical sets (SC-026-008 mechanical check). Test MUST run under `magex test:race` without `//go:build integration`.

**Checkpoint**: audit vocabulary is reconciled docs↔code; the orchestrator's later emission sites have constants to reference.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: package-private seams and shared test scaffolding required before any user story can be implemented. **⚠️ No US-phase task may begin until this phase is complete.**

- [ ] T004 Rename package-private `setTokenForTest` → `setToken` in [internal/supervise/state.go](../../internal/supervise/state.go) (production-path seam now, not just a test seam). Update every existing callsite inside [internal/supervise/](../../internal/supervise/). Keep the method unexported — the rename does NOT mutate the SDD-19 PACKAGE-MAP exported-surface lock.
- [ ] T005 Move the `orchestratorInputs` struct from [internal/cli/supervise.go](../../internal/cli/supervise.go) lines 54-119 into a new unexported `statusInputs` struct inside [internal/supervise/lifecycle.go](../../internal/supervise/lifecycle.go). Fields and methods unchanged (atomic.Pointer / atomic.Bool per field, getter methods implementing the locked SDD-22 `StatusInputs` interface). The cli shim will be rewired in Phase 7.
- [ ] T006 [P] Create [internal/supervise/lifecycle_interfaces.go](../../internal/supervise/lifecycle_interfaces.go) with `Validator`, `Alerts`, `Watchdog` interfaces, `AlertClass` enum (10 values LOCKED per [contracts/interfaces.go.md](contracts/interfaces.go.md) §3), `AlertPayload` struct (3 string fields), `AlertClass.String()` method, and the three no-op default types (`noopValidator`, `noopAlerts`, `noopWatchdog`). No business logic — pure type declarations. NO `init()`. NO package-level mutable vars beyond `var Err… = errors.New(…)`.
- [ ] T007 [P] Add shared test fixtures to [internal/supervise/lifecycle_testutil_test.go](../../internal/supervise/lifecycle_testutil_test.go) (new file): `recordingAlerts` (per [contracts/interfaces.go.md](contracts/interfaces.go.md) §7), `controllableValidator`, `recordingWatchdog`, `mockVaultServer` (httptest.Server with `/hz`, `/claim`, `/s/{name}` handlers + `ClaimCount()` accessor), `testECDSAKey(t)` helper, `newTestLifecycle(t)` helper per [quickstart.md](quickstart.md) §1. NO production code — all helpers exist only under `_test.go`.
- [ ] T008 [P] Add `audit.NewTestWriter(t)` (if not already present) to [internal/audit/](../../internal/audit/) for in-memory audit assertions consumed by `recordingAlerts` tests. Test-only helper; skip if equivalent already exists.

**Checkpoint**: package-private seams renamed, shared types declared, test fixtures wired. Every US phase below can construct a `*Lifecycle` in tests via the shared helpers.

---

## Phase 3: User Story 1 — First daemon bootstrap reaches `running` on one approval (Priority: P1) 🎯 MVP

**Goal**: orchestrator boots, retries against Tailscale + vault, submits exactly one signed `/claim`, persists JWT, calls `Refiller.Refill`, runs every configured validator, builds child env from `Grace`, starts `Child`, transitions to `running`. Twelve SDD-25 scenarios depend on this.

**Independent Test**: with a `httptest`-backed vault server and a child binary that prints its environment and exits, the supervisor emits exactly one `/claim` request, transitions through `fetching → running`, starts the child with the requested scopes as env vars, emits the audit subsequence `supervisor_session_claimed → secret_retrieved (×n) → supervisor_running`, and exposes the `running` state on the status socket within a bounded `runtime.Gosched()` poll.

### Tests for User Story 1 ⚠️ (write FIRST; ensure all fail before implementation)

- [ ] T009 [P] [US1] Add `TestLifecycle_BootSubmitsClaim` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — happy path: drives `Run` against `newTestLifecycle(t)` with an always-up TailscaleProbe and a 200-`/claim` mockVault; asserts `mockVault.ClaimCount() == 1`, `lc.store.Snapshot().State == StateRunning` within `Eventually`, zero alerts, audit chain contains `ActionSupervisorSessionClaimed`. NO `time.Sleep` — use `require.Eventually` with `runtime.Gosched()` cadence.
- [ ] T010 [P] [US1] Add `TestLifecycle_BootRetrySucceedsAfterNFailures` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — `Deps.TailscaleProbe` returns an error N times then nil; asserts `Run` reaches `StateRunning`, exactly one `/claim`, cumulative wait ≤ `cfg.BootRetryTimeout`, exponential intervals follow `500ms × 2.0^n` jittered ±20%. Use a fake `Clock` for deterministic backoff observation.
- [ ] T011 [P] [US1] Add `TestLifecycle_BootRetryTimeoutExhausted` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — `Deps.TailscaleProbe` always errors; `cfg.BootRetryTimeout` set to a small window; asserts `Run` returns `errors.Is(err, ErrBootTimeout)`, audit chain contains `ActionSupervisorBootTimeout`, `recordingAlerts.events` contains exactly one `AlertClassBootTimeout`, NO Discord prompt ever issued (mockVault.ClaimCount() == 0).
- [ ] T012 [P] [US1] Add `TestLifecycle_ClaimDeniedTransitionsToAwaitingApproval` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — mockVault returns 401 on `/claim`; asserts `Run` returns a wrapped terminal error mapped to non-zero exit, audit chain contains `ActionSupervisorAwaitingApproval` with `data.cause=="claim_denied"` (or equivalent), no child started.
- [ ] T013 [P] [US1] Add `TestLifecycle_ClaimDiscordUnavailableEmitsAlert` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — mockVault returns 503 with body `{"error":"discord_unavailable"}` for the first attempt, then 200; asserts exactly one `AlertClassDiscordUnavailableOnClaim` emitted, then `StateRunning` reached on the retry, child started.
- [ ] T014 [P] [US1] Add `TestLifecycle_ValidatorFailureBlocksChildStart` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — `Deps.Validators["ANTHROPIC_API_KEY"]` returns an error; asserts no child PID, `StateAwaitingApproval`, exactly one `AlertClassValidatorFailure` with `payload.Scope=="ANTHROPIC_API_KEY"`, audit `ActionSupervisorAwaitingApproval` with `data.cause=="validator"` AND `ActionSupervisorStaleAlert` with `data.class=="ValidatorFailure"`, sentinel-secret bytes ABSENT from alert payload.

### Implementation for User Story 1

- [ ] T015 [US1] Create [internal/supervise/lifecycle.go](../../internal/supervise/lifecycle.go) with the `Lifecycle` struct (per [data-model.md](data-model.md) §1), `Deps` struct (per [data-model.md](data-model.md) §2), `NewLifecycle(ctx, cfg, deps) *Lifecycle` constructor, single-shot `Run(ctx) error` skeleton (returns `ErrLifecycleAlreadyRan` on second call via `sync.Once`), `mainLoop` goroutine stub with `<-ctx.Done()` cancellation + top-frame `defer recover()`, `Lifecycle.wg` WaitGroup, sentinel `ErrLifecycleAlreadyRan` / `ErrValidatorFailed` / `ErrRefillFailedPostRunning` declarations, `statusInputs` field wired. NO state-string literals, NO raw `78`, NO `runtime.GOOS` in this file. Validate required `Deps` fields with panic on nil (Constitution IX startup-wiring exemption). Internal channels constructed buffered=1.
- [ ] T016 [US1] Create [internal/supervise/lifecycle_audit.go](../../internal/supervise/lifecycle_audit.go) with audit-emission helpers per [contracts/audit-vocabulary.md](contracts/audit-vocabulary.md) §4 and [data-model.md](data-model.md) §8: `emitSessionClaimed(jti, sessionType, exp, scope)`, `emitAwaitingApproval(cause)`, `emitStaleAlert(class, scope, errorClass)`, `emitBootTimeout(lastErrClass)`, `emitChildCleanExit(pid, uptime)`, `emitChildExitCrash(pid, code, sig, uptime)`, `emitChildExit78(pid, uptime)`, `emitSilentRefill(scopes)`, `emitGraceEntered(scopes, ttlRem)`, `emitGraceExited(scopes, outcome)`, `emitSessionRefreshed(jti, prev, exp)`, `emitClientRefreshInvoked(state, outcome)`. Each helper calls `Deps.AuditWriter.Append` with a `Data` map constrained by the §8 projections — NO secret material allowed in any value. Closed phrase map for `Reason` strings lives here.
- [ ] T017 [US1] Create [internal/supervise/lifecycle_boot.go](../../internal/supervise/lifecycle_boot.go) with the boot-retry loop: `bootPreconditionsLoop(ctx)` calls `Deps.TailscaleProbe(ctx)` + `Deps.VaultHzProbe(ctx, cfg.ServerURL)` with per-attempt 2s `context.WithTimeout` (`bootProbeTimeout`), exponential backoff `bootBackoffInitial=500ms × bootBackoffMultiplier=2.0` jittered ±20% capped at `bootBackoffCap=30s`, total budget `cfg.BootRetryTimeout`; on exhaustion emits `AlertClassBootTimeout` + `ActionSupervisorBootTimeout` and returns wrapped `ErrBootTimeout`. Then `submitClaim(ctx)` builds canonical-JSON `/claim` body, signs via `sign.CanonicalJSON` + `sign.Sign` (SDD-08), POSTs to `<server>/claim`, parses 200 response (`{token, jti, scope, exp}`), wraps token bytes in `*securebytes.SecureBytes`, calls package-private `Store.setToken(sb)`, emits `ActionSupervisorSessionClaimed`. 503 body parsing: switches on `error` field — `"discord_unavailable"` → `AlertClassDiscordUnavailableOnClaim` + retry in boot loop; any other 5xx/network → generic retry; 401/4xx-non-401 terminal (no audit `awaiting_approval` for terminal — cli maps to ExitErr).
- [ ] T018 [US1] Create [internal/supervise/lifecycle_child.go](../../internal/supervise/lifecycle_child.go) with `initialRefillAndStart(ctx)`: calls `Refiller.Refill(ctx, cfg.Scope)` (boot-time error → `errors.Is(err, ErrJTIUnknown)` → `AlertClassVaultRejectedJWT` + `StateAwaitingApproval`; any other error → boot-retry loop per FR-026-010a); for each scope calls `Deps.Validators[scope].Validate(ctx, scope, sb)` (nil map / missing key → `noopValidator`); on any validator failure → `AlertClassValidatorFailure` + `ActionSupervisorAwaitingApproval(cause="validator")` + `ActionSupervisorStaleAlert` + return without starting child; on all validators nil, build child env via `Grace.Get(scope).Use(func(b []byte) { env = append(env, scope+"="+string(b)) })` (Constitution X: exactly one `string(*SecureBytes)` site at OS fork boundary; env slice zeroed in place via helper after `Child.Start` returns), construct `ChildConfig`, instantiate `Child` via `NewChild(cfg)`, call `Child.Start(ctx)`, transition `Store.Transition(ctx, EventChildStarted)`. Wire `lineSplittingWriter` (declared in this file) as `ChildConfig.Stderr` — tees to operator stderr + fans line-buffered emit to `Deps.Watchdog.OnStderrLine(ctx, line)` with 64 KiB cap. NO second drain on `Child.Stderr` (FR-026-030).

**Checkpoint**: Tests T009-T014 now pass. The supervisor reaches `running` on one approval and blocks child start on validator failure. SDD-25 Scenario 2 is unblocked end-to-end.

---

## Phase 4: User Story 2 — Crash/clean exit silently refills (Priority: P2)

**Goal**: child exits cleanly (code `0`) or by crashing (non-zero non-`78`) → orchestrator calls `Refiller.Refill` again WITHOUT prompting the approver → re-runs validators → restarts the child. ErrJTIUnknown during silent refill transitions to `awaiting-approval`.

**Independent Test**: with an approved session and a child that exits with code `0` (or `1`, `137`, signal-induced) immediately after start, the supervisor emits `supervisor_child_clean_exit` (or `_crash`), calls `Refiller.Refill` again, restarts the child, emits `supervisor_silent_refill`, NEVER emits an alert, and the child's restart count is observable on the status socket. ErrJTIUnknown branch tested separately.

### Tests for User Story 2 ⚠️ (write FIRST)

- [ ] T019 [P] [US2] Add `TestLifecycle_ChildExitZeroTriggersSilentRefill` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — child binary exits code `0` immediately after start; asserts second `Refiller.Refill` call (no second `/claim`!), child restarted with new PID, audit contains `ActionSupervisorChildCleanExit` + `ActionSupervisorSilentRefill`, zero `Alerts.Emit` calls (silent refill is normal).
- [ ] T020 [P] [US2] Add `TestLifecycle_ChildExitNonZeroTriggersSilentRefill` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — child exits code `1` (or `137`); asserts same silent-refill path as T019, audit contains `ActionSupervisorChildExitCrash` (not `_clean_exit`), zero `Alerts.Emit` calls.
- [ ] T021 [P] [US2] Add `TestLifecycle_RefillJTIUnknownTransitionsToAwaitingApproval` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — child exits `0`, mockVault's `Refiller.Refill` returns `ErrJTIUnknown` on the second call; asserts `StateAwaitingApproval`, exactly one `AlertClassVaultRejectedJWT`, child stays stopped (no third `Refill` call without operator action), audit `ActionSupervisorAwaitingApproval(cause="unknown_jti")` + `ActionSupervisorStaleAlert(class="VaultRejectedJWT")`.
- [ ] T022 [P] [US2] Add `TestLifecycle_RefillTransientErrorPostRunningEmitsRefillFailed` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — child exits `0`, mockVault's `/s/{name}` returns 500 (non-JTI error); asserts `StateAwaitingApproval`, exactly one `AlertClassRefillFailed`, audit `ActionSupervisorAwaitingApproval(cause="refill_failed")` + `ActionSupervisorStaleAlert(class="RefillFailed")` — NOT another boot-retry loop (FR-026-010a post-running branch).

### Implementation for User Story 2

- [ ] T023 [US2] Extend [internal/supervise/lifecycle_child.go](../../internal/supervise/lifecycle_child.go) with `childWaitLoop(ctx)` goroutine (owner: `Lifecycle.wg`; cancellation: implicit via `Child.Forward(SIGTERM)`; termination: one send on `childExitCh` per child instance; top-frame `defer recover()`). On each cycle: `code, sig, err := Child.Wait(ctx)` → constructs `childExit{code, signal, err}` → sends on `Lifecycle.childExitCh`.
- [ ] T024 [US2] Extend [internal/supervise/lifecycle.go](../../internal/supervise/lifecycle.go) `mainLoop` with the `childExit` dispatch arm (per FR-026-009): on `code == 0` → emit `ActionSupervisorChildCleanExit` → call `silentRefillAndRestart(ctx)`; on `code != 0 && code != Exit78` → emit `ActionSupervisorChildExitCrash` → same `silentRefillAndRestart`; on `code == Exit78` → US3 work (T028). The orchestrator MUST reference `Exit78` via the SDD-20 named constant — NO raw `78` literal in this file.
- [ ] T025 [US2] Extend [internal/supervise/lifecycle_child.go](../../internal/supervise/lifecycle_child.go) with `silentRefillAndRestart(ctx)`: re-calls `Refiller.Refill(ctx, cfg.Scope)` (using the cached JWT in `Store.token`); on `errors.Is(err, ErrJTIUnknown)` → emit `AlertClassVaultRejectedJWT` + `ActionSupervisorAwaitingApproval(cause="unknown_jti")` + `ActionSupervisorStaleAlert` + transition `StateAwaitingApproval`; on any other non-nil error → emit `AlertClassRefillFailed` + `ActionSupervisorAwaitingApproval(cause="refill_failed")` + `ActionSupervisorStaleAlert` + transition `StateAwaitingApproval` (FR-026-010a post-running branch — NO auto-retry); on nil → re-run validators (same path as T018; validator-fail handling identical) → re-build env → re-instantiate `Child` → `Start(ctx)` → emit `ActionSupervisorSilentRefill` → spawn fresh `childWaitLoop`. No Discord prompt anywhere on this path.

**Checkpoint**: Tests T019-T022 pass. Crash/clean exits restart silently inside a valid session; stale JWT and refill failures surface loudly.

---

## Phase 5: User Story 3 — Stale credentials surface loudly (Priority: P2)

**Goal**: child exit `78` OR validator failure OR `ErrJTIUnknown` → orchestrator transitions to `awaiting-approval`, emits a distinctly-classed `[STALE] …` alert, refuses to restart child until fresh approval or `hush client refresh` arrives. Validator-failure and ErrJTIUnknown paths already covered by US1+US2 tests; this story adds exit-78 + the recovery path.

**Independent Test**: for each of the three stale paths, inject the failure into a `running` supervisor and assert: `StateAwaitingApproval`, exactly one `Emit` call with a class distinct from the other two paths, payload carries scope name (never secret bytes, never JWT bytes), no child restart until operator action, audit `ActionSupervisorStaleAlert` appended with `data.class` matching the AlertClass.

### Tests for User Story 3 ⚠️ (write FIRST)

- [ ] T026 [P] [US3] Add `TestLifecycle_ChildExit78EmitsStaleAlertNoRestart` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — child binary exits code `78`; asserts `StateAwaitingApproval`, exactly one `AlertClassExit78` with `payload.Reason` from the closed phrase map, audit `ActionSupervisorChildExit78` + `ActionSupervisorStaleAlert(class="Exit78")` + `ActionSupervisorAwaitingApproval(cause="exit_78")`, NO second `Refiller.Refill` call (child stays stopped regardless of remaining session TTL).
- [ ] T027 [P] [US3] Add `TestLifecycle_StatusSocketRefreshDrivesRestartAfterStale` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — orchestrator in `StateAwaitingApproval` after T026; test posts on `refreshVerbCh` (or invokes the status-socket refresh handler directly); asserts orchestrator drives `Refiller.Refill` → validators → child restart → `StateRunning`, audit `ActionClientRefreshInvoked(state="awaiting-approval", outcome="recovered")`.

### Implementation for User Story 3

- [ ] T028 [US3] Extend [internal/supervise/lifecycle.go](../../internal/supervise/lifecycle.go) `mainLoop` `childExit` dispatch with the `code == Exit78` arm: emit `ActionSupervisorChildExit78` → emit `AlertClassExit78` → emit `ActionSupervisorStaleAlert` → emit `ActionSupervisorAwaitingApproval(cause="exit_78")` → transition `StateAwaitingApproval` → DO NOT restart child. The arm references SDD-20's `Exit78` constant — NO raw `78` literal anywhere in `lifecycle*.go`.
- [ ] T029 [US3] Create [internal/supervise/lifecycle_refresh.go](../../internal/supervise/lifecycle_refresh.go) (initial skeleton; refresh-window machinery in Phase 6) with `dispatchRefreshVerb(ctx, verb refreshVerb)` per [plan.md](plan.md) §10 + [research.md](research.md) §R-8: state-conditional dispatch — `StateAwaitingApproval` → drive full refill+validate+restart via T025's `silentRefillAndRestart` (which handles validator-fail and JTI re-fail consistently) + emit `ActionClientRefreshInvoked(state="awaiting-approval", outcome=...)`; `StateRunning` / `StateGraceRestart` → coalesce with any in-flight refill via `coalescer.perform` + emit `ActionClientRefreshInvoked` once per coalesced cohort; `StateFetching` / `StateStopped` → reject with `{"ok":false,"error":"<state>"}` ack via `verb.ack` channel; ack channel writes never block (default no-receiver case logs and drops).
- [ ] T030 [US3] Wire the `refreshVerb` dispatch into `mainLoop` `select` in [internal/supervise/lifecycle.go](../../internal/supervise/lifecycle.go). Add the `Lifecycle.refreshVerbCh` arm. The status server's `AttachRefreshHandler` is bound (in `NewLifecycle`) to a closure that constructs a `refreshVerb{ack: make(chan error, 1)}`, posts on `refreshVerbCh`, and blocks on `ack` (with the existing SDD-22 handler-timeout budget).

**Checkpoint**: Tests T026-T027 pass. All three stale paths emit distinct alerts, and the recovery path via `hush client refresh` works end-to-end.

---

## Phase 6: User Story 4 — Refresh window advances the session without restarting the child (Priority: P3)

**Goal**: `Refresher` window-tick callback fires → orchestrator submits fresh signed `/claim` → swaps cached JWT atomically in `Store` → child PID unchanged. Refresh-denied / refresh-timeout emit distinct alerts but keep the existing session active until expiry.

**Independent Test**: with a `running` supervisor whose `Refresher` is driven by a controllable seam, fire the tick callback once and assert: fresh signed `/claim` submitted, new JWT in `Store.Snapshot()`, child PID unchanged, audit `supervisor_session_refreshed`. Deny path: alert emitted, session unchanged, `StateRunning` preserved.

### Tests for User Story 4 ⚠️ (write FIRST)

- [ ] T031 [P] [US4] Add `TestLifecycle_RefresherTickSubmitsFreshClaim` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — orchestrator at `StateRunning`; test posts on `lc.refreshTickCh`; asserts `mockVault.ClaimCount() == 2` (initial + refresh), new JWT visible in `lc.store.Snapshot().Token`, audit `ActionSupervisorSessionRefreshed`.
- [ ] T032 [P] [US4] Add `TestLifecycle_RefresherTickKeepsChildRunning` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — same setup as T031; captures `lc.child.PID()` before tick; asserts PID unchanged after tick, child still running, `StateRunning` preserved (no transition through `StateFetching`).
- [ ] T033 [P] [US4] Add `TestLifecycle_RefreshDeniedEmitsAlert` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — mockVault returns 200 on initial claim, then 200 with a `decision=deny` body (or whatever the SDD-09 envelope encodes for refresh-deny) on the refresh claim; asserts exactly one `AlertClassRefreshDenied`, existing session preserved in `Store`, `StateRunning` preserved, child PID unchanged.
- [ ] T034 [P] [US4] Add `TestLifecycle_RefreshTimeoutEmitsAlert` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — mockVault delays refresh `/claim` past the approval timeout; asserts exactly one `AlertClassRefreshTimeout`, same preservation invariants as T033.

### Implementation for User Story 4

- [ ] T035 [US4] Extend [internal/supervise/lifecycle_refresh.go](../../internal/supervise/lifecycle_refresh.go) with `claimRefreshLoop(ctx)` goroutine (owner: `Lifecycle.wg`; cancellation: `<-ctx.Done()`; termination: drains `refreshTickCh` and exits; top-frame `defer recover()`). On each tick: re-signs and re-submits `/claim` (same machinery as T017's `submitClaim` factored out), parses outcome, posts `refreshResult{err, deny}` on `refreshDoneCh`.
- [ ] T036 [US4] Wire `Refresher`'s `refill` callback (passed into `NewRefresher` inside `NewLifecycle`) to a non-blocking post on `Lifecycle.refreshTickCh`. The callback returns immediately — `claimRefreshLoop` does the actual vault round-trip without blocking the Refresher's tick anchor (per [research.md](research.md) §R-5).
- [ ] T037 [US4] Extend [internal/supervise/lifecycle.go](../../internal/supervise/lifecycle.go) `mainLoop` with the `refreshDone` dispatch arm: on `result.err == nil` → atomic JWT swap via `Store.setToken(newSb)` + emit `ActionSupervisorSessionRefreshed` (child PID unchanged — no `Child.Forward`, no `Child.Wait` cycle); on `result.deny` → emit `AlertClassRefreshDenied` + keep existing session; on timeout `result.err` → emit `AlertClassRefreshTimeout` + keep existing session. NO transition through `StateFetching` on the success branch.

**Checkpoint**: Tests T031-T034 pass. Refresh-window swaps survive a 24-hour deployment; deny/timeout don't kill a still-valid session.

---

## Phase 7: User Story 5 — Clean shutdown on SIGTERM/SIGINT releases all resources (Priority: P3)

**Goal**: `SIGTERM` (or `SIGINT`) → cancel root ctx → `Child.Forward(SIGTERM)` → wait 10s → escalate to `SIGKILL` if needed → wait 5s → `wg.Wait()` → cli shim's `defer pidfile.Release()` fires. Total `Run` exit ≤ 15s. No goroutine leak. No leftover socket inode.

**Independent Test**: send `SIGTERM` to a `running` supervisor and assert child receives `SIGTERM` and exits, pidfile is released (a fresh `hush supervise` for the same config can acquire it), status-socket inode is removed, supervisor process exits `0` within configured shutdown timeout, `runtime.NumGoroutine` returns to baseline.

### Tests for User Story 5 ⚠️ (write FIRST)

- [ ] T038 [P] [US5] Add `TestLifecycle_GracefulShutdownDrainsChild` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — orchestrator at `StateRunning`; test cancels root ctx; asserts child receives `SIGTERM` (via mock-child observer), `Run` returns nil within 15s, `runtime.NumGoroutine` returns to pre-test baseline (bounded `runtime.Gosched()` poll, NO `time.Sleep`), pidfile.Release was called.
- [ ] T039 [P] [US5] Add `TestLifecycle_SigkillEscalationOnWedgedChild` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — orchestrator at `StateRunning` with a mock child that ignores `SIGTERM`; test cancels root ctx; asserts after 10s the orchestrator calls `Child.Forward(SIGKILL)`, child exits, `Run` returns within the 15s hard ceiling. Uses a fake `Clock` for deterministic 10s/5s observation.
- [ ] T040 [P] [US5] Add `TestLifecycle_BootRetryShutdownNeverContactsDiscord` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — orchestrator in boot-retry loop (TailscaleProbe error); test cancels root ctx before timeout; asserts `Run` returns nil promptly, `mockVault.ClaimCount() == 0`, no Discord prompt issued. Edge case: "duplicate supervisor start" already covered by SDD-22 PidFile tests — no orchestrator-side test needed.

### Implementation for User Story 5

- [ ] T041 [US5] Extend [internal/supervise/lifecycle.go](../../internal/supervise/lifecycle.go) `mainLoop` with the `<-ctx.Done()` shutdown sequence (per [research.md](research.md) §R-4): call `Child.Forward(syscall.SIGTERM)` if child is non-nil → spawn 10s `shutdownGraceTimeout` watchdog; if `childExit` hasn't arrived by deadline, call `Child.Forward(syscall.SIGKILL)`; spawn 5s `shutdownHardCeiling` watchdog; if `wg.Wait()` hasn't drained, log + return. Pidfile release is owned by the cli shim's `defer` (Phase 8) — NOT by `Lifecycle.Run`. All durations sourced from constants in [data-model.md](data-model.md) §10.
- [ ] T042 [US5] Rewrite [internal/cli/supervise.go](../../internal/cli/supervise.go) from ~335 LOC to ~80 LOC per [quickstart.md](quickstart.md) §7. Keep: cobra command wiring, `applyFlagOverrides`, dry-run rendering, `signal.NotifyContext(ctx, SIGTERM, SIGINT)`, `AcquirePidFile` + `defer pidfile.Release()`, `printSuperviseErr`. Delete: `orchestratorInputs` struct (moved to package supervise in T005), inline state-machine reasoning, child-wait machinery, refresh-coalescer construction (moved into `NewLifecycle`), any `<-rootCtx.Done()` post-orchestrator wait. The shim's responsibility is config load → pidfile → build `Deps` → `supervise.NewLifecycle(rootCtx, cfg, deps).Run(rootCtx)`. Map terminal errors to `ExitErr` / `ExitOK` as before.

**Checkpoint**: Tests T038-T040 pass. SIGTERM cleanly releases all resources; the cli shim is ≤80 LOC and contains zero business logic. SDD-25's AC-10 precondition is now satisfied.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: anti-leak tests, integration tests, docs/PACKAGE-MAP/AC-MATRIX updates, SDD-25 unblock, and the final gate run.

### Anti-leak tests (Constitution VII + X)

- [ ] T043 [P] Add `TestLifecycle_NoBusinessLogicLeakage` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — grep-style assertion: parses every `lifecycle*.go` source file (NOT `_test.go`), asserts ABSENCE of: state-string literals (`"running"`, `"awaiting-approval"`, `"fetching"`, `"stopped"`, `"grace-restart"`), the raw `78` literal (must be referenced via SDD-20's `Exit78` constant), `runtime.GOOS` references, `case StateRunning` (state-table reasoning belongs to SDD-19). Fails loudly with file:line on any hit. (FR-026-023.)
- [ ] T044 [P] Add `TestLifecycle_NoSentinelLeakage` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — drives `Run` to `StateRunning` with `testutil.SentinelSecret(N)` as the fetched secret value; captures all 5 byte streams: operational slog records, audit JSONL records, status-socket JSON, alert payloads, error message strings. Asserts `testutil.AssertSentinelAbsent` across each stream. (FR-026-024, SC-026-007.)
- [ ] T045 [P] Add `TestLifecycle_NoGoroutineLeakOnShutdown` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — drives `Run` to `StateRunning`, captures `runtime.NumGoroutine`, cancels ctx, asserts post-shutdown count returns to baseline via bounded `runtime.Gosched()` poll. (SC-026-011.)
- [ ] T046 [P] Add `TestLifecycle_StatusSocketRefreshInBootRetryRejects` and `TestLifecycle_StatusSocketRefreshInFetchingRejects` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — post on `refreshVerbCh` while orchestrator is in `boot-retry` (T046a) and `fetching` (T046b); assert the verb's `ack` channel receives an error with payload `{"ok":false,"error":"boot-retry"}` (or `"fetching"`); assert NO state mutation, NO claim re-submitted, NO audit event emitted. (Spec Clarification 4.)
- [ ] T047 [P] Add `TestLifecycle_GraceRestartUsesCachedSecrets` to [internal/supervise/lifecycle_test.go](../../internal/supervise/lifecycle_test.go) — orchestrator at `StateRunning` with `cfg.CacheSecretsForRestart=true`; child crashes; mockVault's `/s/{name}` returns 500; asserts orchestrator pulls secrets from `Grace` (cached) instead of re-fetching, emits `AlertClassGraceEntered` + `ActionSupervisorGraceEntered`, child restarts successfully, emits `ActionSupervisorGraceExited` on next clean cycle. NO Discord prompt.

### Integration tests (`//go:build integration`)

- [ ] T048 Create [internal/supervise/lifecycle_integration_test.go](../../internal/supervise/lifecycle_integration_test.go) with `//go:build integration` build tag and `TestLifecycle_Integration_FullBootRoundTrip` — full boot → claim → refill → validator → child-start round trip via `httptest.Server` (vault stub) + `testutil.DiscordStub` (approver stub) + a real child binary that prints env and exits cleanly. Asserts the full audit subsequence and the integration-level `runtime.NumGoroutine` invariant.
- [ ] T049 [P] Add `TestLifecycle_Integration_SilentRefillOnChildExit` to [internal/supervise/lifecycle_integration_test.go](../../internal/supervise/lifecycle_integration_test.go) — child binary exits `0` three times; supervisor performs three silent refills; zero approver prompts.
- [ ] T050 [P] Add `TestLifecycle_Integration_GracefulShutdownReleasesPidfile` to [internal/supervise/lifecycle_integration_test.go](../../internal/supervise/lifecycle_integration_test.go) — full daemon lifecycle through SIGTERM; asserts a fresh `supervise.AcquirePidFile` call succeeds after the first supervisor exits (Scenario 14 precondition).

### Docs & playbook updates

- [ ] T051 Append the SDD-24 locked surface section to [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md): `Lifecycle`, `Deps`, `NewLifecycle`, `Run`, `Validator`, `Alerts`, `AlertClass` (10 values), `AlertPayload`, `Watchdog`, `ErrLifecycleAlreadyRan`, `ErrValidatorFailed`, `ErrRefillFailedPostRunning`. The SDD-19..22 sections remain UNCHANGED. Lock string for SDD-24: "one struct + one Deps + two constructors/methods (NewLifecycle, Run) + three interfaces + one enum (10 values) + one payload struct + three sentinels".
- [ ] T052 [P] Update [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-10 row — replace the "blocked by SDD-24" marker with references to the new orchestrator unit + integration tests (T009-T050) as evidence. Mark AC-10 precondition unblocked.
- [ ] T053 [P] Update [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md) — mark SDD-24 done; SDD-25 implement resumes from T001 of [specs/025-lifecycle-harness/tasks.md](../025-lifecycle-harness/tasks.md).
- [ ] T054 [P] Remove the SDD-24-pause banner from the top of [specs/025-lifecycle-harness/tasks.md](../025-lifecycle-harness/tasks.md). SDD-25 implement phase is now unblocked.

### Final gate run (mandatory, in order)

- [ ] T055 Run `magex format:fix` — applies gofmt/goimports across the diff.
- [ ] T056 Run `magex lint` — verifies no new lint findings (Constitution VIII).
- [ ] T057 Run `magex test:race` — verifies all unit tests pass under the race detector AND the orchestrator file coverage gate is `≥85%` on `lifecycle*.go`. If coverage gate fails, return to the appropriate US phase and add the missing branch test BEFORE adjusting code.
- [ ] T058 Run `magex test:race -tags=integration` — verifies T048-T050 pass end-to-end against the httptest server scaffolding.

**Checkpoint**: SDD-24 implementation complete. AC-10 unblocked. SDD-25 implement-phase can resume. PACKAGE-MAP + SPEC.md + AC-MATRIX + SDD-PLAYBOOK in sync with the implementation.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: independent — can start immediately.
- **Phase 2 (Foundational)**: depends on Phase 1 (audit constants must exist before any orchestrator code references them). **BLOCKS all US phases.**
- **Phase 3 (US1, P1, MVP)**: depends on Phase 2.
- **Phase 4 (US2, P2)**: depends on Phase 3 (child-start path must exist before child-exit dispatch is meaningful).
- **Phase 5 (US3, P2)**: depends on Phase 3 (validator path) and Phase 4 (child-exit dispatch infrastructure).
- **Phase 6 (US4, P3)**: depends on Phase 3 (claim submission factored out by T017).
- **Phase 7 (US5, P3)**: depends on Phase 3 (must have a running child to shut down) — can run in parallel with Phases 4-6 once Phase 3 is done.
- **Phase 8 (Polish)**: depends on Phases 3-7 (anti-leak tests sweep the full code base; integration tests need full lifecycle; docs reflect final state).

### Critical Path

T001 → T004 → T005 → T006/T007 → T009 (+ T010-T014 in parallel) → T015 → T016 → T017 → T018 → checkpoint Phase 3 (MVP). Phases 4/5/6/7 can then proceed largely in parallel modulo the shared `mainLoop` `select` arms.

### Within Each User Story

- **TDD-mandatory**: every test task T(N) for a US must be authored AND failing BEFORE the implementation task for that contract begins. Phase 3 example: T009-T014 all written and failing → THEN T015-T018 in order.
- Models/interfaces (T006) before constructors (T015).
- Constructors before goroutine spawners.
- Goroutine spawners before state-machine arms.

### Parallel Opportunities

- All [P]-marked test-authoring tasks within a US phase can be written in parallel (they touch different test functions in the same file but no shared state).
- All [P]-marked docs tasks (T002, T052, T053, T054) can be edited in parallel — different files.
- T043-T047 anti-leak tests can be authored in parallel after Phase 7 lands.
- T048-T050 integration tests can be authored in parallel after Phase 7 lands.
- T055-T058 final-gate commands MUST run sequentially in order (lint sees format's output; test:race needs lint-clean code; integration needs unit-test-pass baseline).

---

## Parallel Example: User Story 1 test-authoring burst

```bash
# Write all six US1 test tasks in parallel (same file, distinct test functions):
Task T009: TestLifecycle_BootSubmitsClaim          → internal/supervise/lifecycle_test.go
Task T010: TestLifecycle_BootRetrySucceedsAfterNFailures → internal/supervise/lifecycle_test.go
Task T011: TestLifecycle_BootRetryTimeoutExhausted → internal/supervise/lifecycle_test.go
Task T012: TestLifecycle_ClaimDeniedTransitionsToAwaitingApproval → internal/supervise/lifecycle_test.go
Task T013: TestLifecycle_ClaimDiscordUnavailableEmitsAlert → internal/supervise/lifecycle_test.go
Task T014: TestLifecycle_ValidatorFailureBlocksChildStart → internal/supervise/lifecycle_test.go

# Verify ALL fail (no implementation yet), THEN start T015 → T018 sequentially.
```

---

## Implementation Strategy

### MVP First (Phases 1-3)

1. Phase 1 Setup (T001-T003) — audit vocabulary reconciled.
2. Phase 2 Foundational (T004-T008) — seams + test fixtures in place.
3. Phase 3 US1 (T009-T018) — write all 6 tests, watch them fail, implement the 4 core files.
4. **STOP and VALIDATE**: SDD-25 Scenario 2 (DaemonBootstrap) reaches `running` via the harness. The pause banner stays on SDD-25 until Phase 8, but Scenario 2 should pass in isolation against the new orchestrator.

### Incremental Delivery (constitutional path)

1. MVP (Phases 1-3) → Validates US1 boot path.
2. Add Phase 4 (US2) → silent-refill path validated; SDD-25 Scenarios 5, 6 unblocked.
3. Add Phase 5 (US3) → stale-credential paths validated; SDD-25 Scenarios 4, 7 unblocked.
4. Add Phase 6 (US4) → refresh-window survives 24h deployment; SDD-25 Scenarios 12, 13 unblocked.
5. Add Phase 7 (US5) → clean shutdown verified; SDD-25 Scenario 14 unblocked.
6. Phase 8 Polish (T043-T054) → anti-leak gates + docs sync.
7. Final gate run (T055-T058) → format, lint, race, integration ≥85% coverage.

### Sequential Strategy (single implementer)

Phases 3 → 4 → 5 → 6 → 7 → 8 in order, with all tests in a phase authored and failing before that phase's implementation tasks begin. Total estimated work: ~700-900 LOC production + ~600 LOC unit tests + ~300 LOC integration tests + ~12 LOC audit additions + ~80 LOC cli shim.

---

## Notes

- **TDD is non-negotiable (Constitution VIII)**: every test task is mandatory. Coverage gate `≥85%` on `lifecycle*.go` enforced by T057.
- **No goroutine without owner + ctx + termination + top-frame recover** (Constitution IX, FR-026-029). Reviewed for every goroutine-spawning task (T015 mainLoop, T023 childWaitLoop, T035 claimRefreshLoop).
- **No `string(*SecureBytes)` outside the two documented sites** (FR-026-028): JWT bearer header inside `Snapshot.Token.Use` (existing SDD-21 site) AND child-env build at fork boundary (T018). T044 sentinel-leak test guards this.
- **No `init()`, no package-level mutable vars** beyond `var Err… = errors.New(…)` (FR-026-031). T006 declares the only permitted sentinels.
- **No mutation of SDD-19..22 PACKAGE-MAP locks** (FR-026-025): T051 appends a new SDD-24 section; T004 `setTokenForTest` rename keeps the method unexported.
- **Commit cadence**: commit after each US phase completes its checkpoint (commit message references the phase + the unblocked SDD-25 scenarios).
- **Stop at any checkpoint** to validate the just-completed US story independently against SDD-25's harness before proceeding.
- **Avoid**: state-string literals in `lifecycle*.go` (T043 grep); raw `78` (T043 grep); `runtime.GOOS` (T043 grep); second drain on `Child.Stderr` (FR-026-030); auto-retry of post-running refill failure (FR-026-010a); extending `AlertClass` beyond 10 (FR-026-016).
