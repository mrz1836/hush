---

description: "Task list for SDD-25 Lifecycle Integration Harness (AC-10 owner)"
---

# Tasks: Lifecycle Integration Harness (SDD-25)

**Input**: Design documents from `/Users/mrz/projects/hush/specs/025-lifecycle-harness/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/harness-api.md, contracts/scenario-assertions.md, quickstart.md

**Tests**: TDD-mandatory per Constitution VIII. Every scenario test is written **before** the harness builder it depends on so the scenario fails first (red), then the harness piece is built (green). Skip paths and stub paths are forbidden by FR-025-3 / FR-025-7 — a missing event surfaces a real gap and stays a failure until the underlying chunk ships.

**Organization**: Tasks are grouped by user story (US1 = release manager green-light; US2 = maintainer-trustable suite; US3 = security-reviewer leak-free).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no shared mutable state, no dependency on incomplete tasks)
- **[Story]**: Maps task to user story (US1 / US2 / US3)
- All paths are absolute or relative to repo root `/Users/mrz/projects/hush/`

## Path Conventions

- All harness code lives at `tests/integration/harness/*.go` (build tag `//go:build integration`)
- All scenario tests live at `tests/integration/scenarios_test.go` (build tag `//go:build integration`)
- TestMain + suite-wide setup lives at `tests/integration/lifecycle_test.go` (build tag `//go:build integration`)
- No new files under `internal/*` (every SDD-01..SDD-23 PACKAGE-MAP is locked)
- No new `go.mod` direct dependency (Constitution XI)

---

## ⛔ Implement-phase pause — SDD-24 activated 2026-05-12

The implement-phase agent halted before T001 after read-and-plan analysis
surfaced a load-bearing gap in SDD-19..23: `internal/cli/supervise.go`
(SDD-23) ships only the dry-run path, primitive constructors, and a
refresh-coalescer wired to the status socket. It performs **no initial
`/claim`, no secret fetch, no `NewChild`/`Start`, no `Child.Wait`, no
exit-code dispatch, no boot-retry, and no validator/alert/watchdog
plumbing**. Without that orchestrator, scenarios 02–15 cannot reach
their documented final state regardless of harness completeness.

See [docs/sdd/SDD-24.md](../../docs/sdd/SDD-24.md) (now active) for the
full gap analysis and the contract for the orchestrator that must ship
before this tasks list is resumed.

When SDD-24 lands, return to this file at T001.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Stand up the `tests/integration/` tree, the build-tag gate, and the depguard rule. Zero production code touched.

- [ ] T001 Create directory `tests/integration/harness/` and `tests/integration/` at repo root; add `tests/integration/harness/doc.go` declaring `package harness` with `//go:build integration` build tag and a one-paragraph package doc citing SDD-25 + the file allocation in research.md §1.
- [ ] T002 [P] Add a `depguard` rule to `.golangci.yml` forbidding any non-test file from importing `github.com/mrz1836/hush/tests/integration/harness`; verify with `magex lint` after writing.
- [ ] T003 [P] Confirm SC-025-11 (default-build invisibility): run `go test ./tests/integration/...` (no `-tags=integration`) and confirm output `no Go files in /Users/mrz/projects/hush/tests/integration` — adds the "default-build excludes integration" regression check to the suite's expected behavior.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Build the suite skeleton — TestMain, integration-child-mode dispatcher, http.RoundTripper allow-list — that every scenario and every harness builder consumes.

**⚠️ CRITICAL**: No scenario test can be authored, and no harness builder can be filled in, until this phase completes.

- [ ] T004 Create `tests/integration/lifecycle_test.go` with `//go:build integration`, `package integration_test`, and a `TestMain(m *testing.M)` that: (a) detects `--integration-child-mode` argv and dispatches to the scripted-child entrypoint (per research.md §6); (b) installs a process-wide `http.DefaultTransport` `RoundTripper` allow-list rejecting any host outside `127.0.0.1`/`::1`/registered httptest listeners (per FR-025-13 + contracts/harness-api.md Builder property #4); (c) calls `m.Run()` and exits with its code.
- [ ] T005 Add the integration-child-mode entrypoint inside `tests/integration/lifecycle_test.go` (or a private helper file alongside it) that parses `--exit-code=N --lifetime=D --emit-stderr-pattern=P` flags and exits per script — pattern from `internal/supervise/child_test.go`; supports scenarios 3/4/5/15.
- [ ] T006 Declare 17 empty `Test_Scenario_*` symbols in `tests/integration/scenarios_test.go` (`//go:build integration`, `package integration_test`) using `t.Skip("not yet implemented")` as the body, named per the FR-025-29 + data-model.md §2 table: `Test_Scenario_01_FirstInteractive`, `Test_Scenario_02_DaemonBootstrap`, `Test_Scenario_03_CleanExitSilentRefill`, `Test_Scenario_04_ChildCrashSilentRefill`, `Test_Scenario_05_Exit78StaleCreds`, `Test_Scenario_06_ValidatorBlocksChild`, `Test_Scenario_07_VaultRestart`, `Test_Scenario_08_DaytimeRefresh`, `Test_Scenario_09_OvernightExpiry_Strict`, `Test_Scenario_09_OvernightExpiry_Grace`, `Test_Scenario_10_DiscordUnavailable` (with `Interactive` + `Supervisor` subtests stubbed), `Test_Scenario_11_TailscaleBootRetry`, `Test_Scenario_12_StatusGate`, `Test_Scenario_13_RotationMidSession`, `Test_Scenario_14_DuplicateSupervisor`, `Test_Scenario_15_LogPatternAlert`. **This is the only task that uses `t.Skip` — every Phase 3 task strips it.**

**Checkpoint**: `go test -tags=integration ./tests/integration/...` compiles and reports 17 skipped tests; default `go test ./...` compiles zero integration files.

---

## Phase 3: User Story 1 — Release manager: prove all 15 scenarios work end-to-end (Priority: P1) 🎯 MVP

**Goal**: Deliver the AC-10-green suite — 15 scenarios passing against the real `internal/*` packages with only Discord + validator-upstream HTTP + NTP mocked.

**Independent Test**: From clean checkout, `magex test:race -tags=integration ./tests/integration/...` returns exit 0 with all 15 scenarios PASS, no real Anthropic/OpenAI/GitHub/Google AI/Discord host contacted, under 120 s.

### Sub-phase 3a — Author scenario tests FIRST (TDD red)

> **Each scenario test below replaces its `t.Skip` from T006 with the four mandatory assertions per contracts/scenario-assertions.md (A: final state, B: audit subsequence + chain continuity, C: status-socket shape where applicable, D: AssertSentinelAbsent across the 6-stream coverage list).** Each task must compile-fail or runtime-fail because harness builders are not yet implemented — that failure is the TDD evidence the harness has work to do.

- [ ] T007 [P] [US1] Implement `Test_Scenario_01_FirstInteractive` in `tests/integration/scenarios_test.go`: drive `request --scope ANTHROPIC_API_KEY,GITHUB_TOKEN --exec`, signed `/claim`, single DiscordStub approve, fetch both `/s/<name>`, exec scripted child; assert Contract A (Scenario 1 compound 4-tuple per data-model.md §3: `/health` flags + child exit code + token-store state + DM count == 1), Contract B (`session_requested`, `session_approved`, `secret_fetched` × 2 in order; `audit.Verify` on chain), Contract D (`AssertSentinelAbsent` over 6 streams). **Skip Contract C** — no supervisor.
- [ ] T008 [P] [US1] Implement `Test_Scenario_02_DaemonBootstrap` in `tests/integration/scenarios_test.go`: start supervisor (`supervise.Store`+`PidFile`+`StatusServer`), claim, fetch, validators pass, child start; assert Contract A (state=`running`), Contract B (`session_requested`, `session_approved`, `supervisor_session_claimed`, `secret_fetched` × N), Contract C (FR-12 shape + `state="running"`, `discord_connected=true`, `scope_healthy` populated), Contract D.
- [ ] T009 [P] [US1] Implement `Test_Scenario_03_CleanExitSilentRefill` in `tests/integration/scenarios_test.go`: child exits 0 within valid JWT TTL, supervisor invokes refill silently, child restarts; assert Contract A (state=`running`), Contract B (`supervisor_child_clean_exit`, `supervisor_silent_refill`, `secret_fetched`), Contract C (DM count from DiscordStub stays at original count — no new approval), Contract D.
- [ ] T010 [P] [US1] Implement `Test_Scenario_04_ChildCrashSilentRefill` in `tests/integration/scenarios_test.go`: scripted child exits with non-zero (not 78) within JWT TTL, supervisor refills silently, child restarts; assert Contract A (state=`running`), Contract B (`supervisor_silent_refill`, `secret_fetched`), Contract C (`scope_healthy` populated), Contract D.
- [ ] T011 [P] [US1] Implement `Test_Scenario_05_Exit78StaleCreds` in `tests/integration/scenarios_test.go`: scripted child exits 78; assert Contract A (state=`awaiting-approval`), Contract B (`supervisor_child_exit_78`, `supervisor_awaiting_approval`, `supervisor_stale_alert`), Contract C (`state="awaiting-approval"`, `scope_stale` populated), Contract D (sentinel absent from `[STALE] Child Exit 78` alert payload — FR-025-27).
- [ ] T012 [P] [US1] Implement `Test_Scenario_06_ValidatorBlocksChild` in `tests/integration/scenarios_test.go`: mock validator returns 401; assert Contract A (state=`awaiting-approval`, child never started — `child.PID==0`), Contract B (`supervisor_stale_alert`, `supervisor_awaiting_approval`), Contract C (`scope_stale` lists offending scope), Contract D (alert payload contains scope name only, never secret value — FR-025-27).
- [ ] T013 [P] [US1] Implement `Test_Scenario_07_VaultRestart` in `tests/integration/scenarios_test.go`: stop+restart `TestServer`, supervisor attempts silent refill, server returns 401-unknown-jti, supervisor enters `awaiting-approval`, DiscordStub approve, supervisor refreshes; assert Contract A (final state=`running` after re-approval), Contract B (`auth_failed`, `supervisor_awaiting_approval`, `session_requested`, `session_approved`, `supervisor_session_refreshed`), Contract C (post-recovery `state="running"`), Contract D.
- [ ] T014 [P] [US1] Implement `Test_Scenario_08_DaytimeRefresh` in `tests/integration/scenarios_test.go`: advance `FakeClock` to refresh window, invoke `Refresher.refill` callback directly (per research.md §2 — do NOT call `Refresher.Run()`), DiscordStub approves `[DAEMON] Refresh` prompt, child continues running uninterrupted; assert Contract A (state=`running`, same child PID as pre-refresh), Contract B (`supervisor_session_refreshed`, `session_approved`), Contract C (`expires_at` advanced), Contract D.
- [ ] T015 [P] [US1] Implement `Test_Scenario_09_OvernightExpiry_Strict` in `tests/integration/scenarios_test.go`: advance `FakeClock` past JWT expiry overnight, scripted child crashes, supervisor cannot silently refill, enters `awaiting-approval`; assert Contract A (state=`awaiting-approval`, child not restarted), Contract B (`token_expired`, `supervisor_awaiting_approval`, `supervisor_stale_alert`), Contract C (`scope_stale` populated), Contract D.
- [ ] T016 [P] [US1] Implement `Test_Scenario_09_OvernightExpiry_Grace` in `tests/integration/scenarios_test.go`: same setup as T015 but with grace cache enabled and `cache_grace_ttl` not exceeded; supervisor uses cached plaintext via `Grace` primitive, child restarts; assert Contract A (state=`running` via `grace-restart` sub-state), Contract B (`token_expired`, `supervisor_grace_entered`, `supervisor_silent_refill`, `supervisor_grace_exited`), Contract C (`grace_active=true` while in sub-state), Contract D.
- [ ] T017 [P] [US1] Implement `Test_Scenario_10_DiscordUnavailable` in `tests/integration/scenarios_test.go` as **one parent function with two subtests** (FR-025-5a): `Interactive` subtest drives a client `/claim` with DiscordStub set unavailable, asserts 503 + caller surfaces failure + no auto-approve fallback + Contract D; `Supervisor` subtest drives supervisor `/claim` with DiscordStub unavailable, asserts 503 + Contract A (state=`fetching` or `awaiting-approval` per docs) + Contract B (`discord_disconnected`, `auth_failed`) + Contract C (supervisor subtest only; `discord_connected=false`) + Contract D. **Interactive subtest skips Contract C** — no supervisor.
- [ ] T018 [P] [US1] Implement `Test_Scenario_11_TailscaleBootRetry` in `tests/integration/scenarios_test.go`: configure `InterfaceLister` seam to report no Tailscale on first N attempts then yes, supervisor retries with backoff via `FakeClock` advancement; assert Contract A (state=`running` after retry succeeds), Contract B (`supervisor_session_claimed` after delayed first attempt), Contract C (`state="running"`, `discord_connected=true`), Contract D.
- [ ] T019 [P] [US1] Implement `Test_Scenario_12_StatusGate` in `tests/integration/scenarios_test.go`: supervisor in `running` state with healthy scope, then mock validator flips one scope to 401 making it stale; assert Contract A (state=`running`), Contract B (`supervisor_stale_alert` for the flipped scope), Contract C (this scenario IS the status assertion — exhaustively check every FR-12 field: `state`, `scope_healthy`, `scope_stale`, `discord_connected`, `expires_at`, `child_pid`, `child_uptime`, `grace_active`), Contract D.
- [ ] T020 [P] [US1] Implement `Test_Scenario_13_RotationMidSession` in `tests/integration/scenarios_test.go`: call `TestVault.Rotate(name, newValue)` mid-session, invoke `Refresh` via status socket `refresh\n` verb, supervisor refetches + validators + restarts child; assert Contract A (state=`running`, new child PID different from pre-rotation), Contract B (`vault_reloaded`, `client_refresh_invoked`, `supervisor_silent_refill`, `secret_fetched`), Contract C (`child_pid` matches post-restart PID), Contract D.
- [ ] T021 [P] [US1] Implement `Test_Scenario_14_DuplicateSupervisor` in `tests/integration/scenarios_test.go`: first `TestSupervisor` acquires pidfile + runs to `running`; second `TestSupervisor` attempts boot, fails at `AcquirePidFile` with explicit split-brain error; assert Contract A (first: `running`; second: surfaces split-brain error message), Contract B (first only: `supervisor_session_claimed`), Contract C (first supervisor only), Contract D (split-brain error message contains no sentinel).
- [ ] T022 [P] [US1] Implement `Test_Scenario_15_LogPatternAlert` in `tests/integration/scenarios_test.go`: scripted child emits configured auth-failure pattern to stderr, watchdog matches, supervisor emits `[STALE] Log Pattern Match` alert but does not change state; assert Contract A (state=`running` — alert-only, no state change), Contract B (`supervisor_stale_alert` kind=`LogPatternMatch`), Contract C (`state="running"`, alert flag visible in status), Contract D (alert payload contains pattern name + scope but never the full stderr line if it contains the sentinel — FR-025-27).

**TDD red-checkpoint**: All 17 `Test_Scenario_*` symbols now exercise the harness API (T007–T022 strip the `t.Skip` from T006). Suite fails to compile (harness types don't exist yet) or fails at runtime. **This failure is the load-bearing TDD evidence — do not proceed to Sub-phase 3b until every scenario test file references the harness builders the next sub-phase will create.**

### Sub-phase 3b — Build harness files (TDD green)

> Each harness file below makes its consuming scenarios compile and reach their assertions. After each file is committed, re-run `magex test:race -tags=integration ./tests/integration/...` and confirm the matrix of compile-or-fail vs. pass shifts as expected — the scenarios in scope for that file go from compile-fail to passing.

- [ ] T023 [P] [US1] Implement `tests/integration/harness/log_capture.go` (`//go:build integration`, `package harness`) per data-model.md §4.6: `NewLogCapture(t) *LogCapture` builds a `slog.Handler` chain writing to a sync-safe buffer; `Logger()` returns the `*slog.Logger`; `Bytes()` accessor; `AssertSentinelAbsent(t, sentinel, streams ...[]byte)` runs `testutil.AssertSentinelAbsent` over every supplied stream with labeled stream names. **Builder contract** (contracts/harness-api.md §Builder contract) properties #1, #2, #3, #4 all satisfied. No `init()`, no package-level mutable globals (Constitution IX).
- [ ] T024 [P] [US1] Implement `tests/integration/harness/vault.go` (`//go:build integration`, `package harness`) per data-model.md §4.1: `NewVault(t, secrets map[string]string) *TestVault` uses `testutil.NewTestVault` + temp dir from `t.TempDir()`; `Path()`, `Key()`, `RegisterClient(idx, pubKey)` writes `clients.json` fixture; `Rotate(name, newValue) error` does atomic rewrite + emits SIGHUP-equivalent signal for Scenario 13; `AuditPath()` returns the audit log path. All on-disk state under `t.TempDir()` per FR-025-22 — never `~/.hush/`.
- [ ] T025 [P] [US1] Implement `tests/integration/harness/discord.go` (`//go:build integration`, `package harness`) per data-model.md §4.3: `NewDiscord(t) *TestDiscord` wraps `testutil.DiscordStub`; `Stub()` exposes embedded stub for `Enqueue`; `SetConnected(b bool)` drives Scenario 10's connectivity sequence; `SetRateLimit(n, per)` drives Scenario 15 throttling; `Alerts() []AlertPayload` exposes recorded alert payloads (interface-typed so `internal/discord.Alerts` can satisfy when SDD-28 lands); `AlertsRaw() [][]byte` for sentinel-absence assertion source. Adapter `stubAsApprover` (mirroring `internal/server/claim_handler_integration_test.go`) plugs into `server.Approver` — constructor `(t *TestDiscord) AsApprover() server.Approver`.
- [ ] T026 [P] [US1] Implement `tests/integration/harness/child.go` (`//go:build integration`, `package harness`) per data-model.md §4.5: `NewChild(t, opts ChildOpts) *TestChild` builds `supervise.ChildConfig` pointing at `os.Executable()` with `--integration-child-mode --exit-code=N --lifetime=D --emit-stderr-pattern=P` argv (T005 entrypoint consumes these); `Cmd()` returns the `*supervise.Child` handle; `ExitCode()`, `EmitStderr(pattern)`, `Stdout()/Stderr() []byte` captured-buffer accessors for sentinel-absence stream coverage; `Wait()` blocks until child exits and returns observed exit code.
- [ ] T027 [US1] Implement `tests/integration/harness/server.go` (`//go:build integration`, `package harness`) per data-model.md §4.2 — **depends on T023 + T024 + T025**: `NewServer(t, opts ServerOpts) *TestServer` builds `server.Deps` against the harness's `LogCapture.Logger()` + `TestVault` + `TestDiscord.AsApprover()`; `httptest.Server`-style listener; `MockValidator(name, h)` constructs per-validator `httptest.Server` + wires `http.Client.Transport` `RoundTripper` rewriting `api.anthropic.com`/`api.openai.com`/`api.github.com`/`generativelanguage.googleapis.com` → loopback (per research.md §5; FR-025-13 enforced); `SetClockSyncProbe`, `ReadAudit()` parses JSONL → `[]audit.Event`, `RawAudit() []byte` raw bytes for sentinel-absence stream, `TokenStore()` exposes live store for Scenario 1 sub-assertion (c), `Health() HealthDoc` for Scenario 1 sub-assertion (a), `Reload()` SIGHUP-equivalent for Scenario 13, `Stop()` graceful shutdown registered with `t.Cleanup`.
- [ ] T028 [US1] Implement `tests/integration/harness/supervisor.go` (`//go:build integration`, `package harness`) per data-model.md §4.4 — **depends on T023 + T026 + T027**: `NewSupervisor(t, opts SupervisorOpts) *TestSupervisor` composes real `supervise.Store`+`NewRefiller`+`NewRefresher`+`NewGrace`+`NewStatusServer`+`AcquirePidFile`+`NewChild` against the harness server URL; `Clock() *FakeClock` with `Advance(d)` driving documented transitions (FR-025-16 — no `time.Sleep`); `Refill(ctx)`, `TriggerRefresh(ctx)`, `Refresh(ctx)` invoke supervise APIs directly; `Status() StatusDoc` + `StatusRaw() []byte` open Unix-domain dial per research.md §7 (default verb → status JSON, `refresh\n` verb → ack); `WaitState(ctx, state, deadline)` bounded `runtime.Gosched()` poll (max 100 iterations) — no `time.Sleep`; `AssertAuditSubsequence(t, recorded, documented)` per research.md §3 (O(n+m) two-pointer); `AssertAuditChainContinuity` calls `audit.Verify`; `AssertNoGoroutineLeak(t)` snapshots `runtime.NumGoroutine` pre/post per research.md §4; `Stop()` tears down every goroutine + closes every socket. Every harness-spawned goroutine has owner+ctx+termination+top-frame `recover()` (Constitution IX).

**TDD green-checkpoint**: All 17 `Test_Scenario_*` symbols compile and the entire suite runs. Scenarios depending on SDD-26/27/28 production behavior may still fail until those chunks ship (per FR-025-3 + Assumptions) — those failures are expected and not addressed in this chunk.

### Sub-phase 3c — Cross-cutting verification helpers

- [ ] T029 [P] [US1] Implement `harness.AssertSupervisorState(t, sup, expected supervise.State)` in `tests/integration/harness/supervisor.go` and `harness.AssertScenario1Compound(t, server, child, discord, expectedExit int)` in `tests/integration/harness/server.go` per contracts/scenario-assertions.md Contract A. Wire both into the corresponding scenario assertions in T007–T022 (replace any inline state-check with the helper).
- [ ] T030 [P] [US1] Implement `harness.AssertStatusShape(t, raw []byte) StatusDoc` in `tests/integration/harness/supervisor.go` — unmarshals into a strictly-typed DTO matching SPEC §FR-12 + SDD-22 locked `statusJSON`; fails if any FR-12 field absent (no `omitempty` per spec Assumptions). Used by Contract C in all supervisor scenarios.
- [ ] T031 [US1] **Verify all 15 scenarios green** with `magex test:race -tags=integration ./tests/integration/...`. Document any scenario still failing because its provider chunk (SDD-26 validators / SDD-27 watchdog / SDD-28 alert classes) is unshipped — those failures are *expected* per FR-025-3 and surface the sequencing gap; do NOT add stubs. If a scenario fails for any other reason, root-cause and fix in either the harness or the production package per data-model.md §1 Validation rules.

**Checkpoint**: AC-10 row of `docs/AC-MATRIX.md` can list each `Test_Scenario_NN_<slug>` path; row reaches `green` when all provider chunks ship.

---

## Phase 4: User Story 2 — Maintainer: deterministic, flake-free suite (Priority: P2)

**Goal**: Suite is reproducible, race-free, lifecycle-clean. Five consecutive runs all pass; total wall-clock per run under 120 s; no goroutine/file-descriptor/socket/process leak after scenario teardown.

**Independent Test**: `for i in 1..5; do magex test:race -tags=integration ./tests/integration/... || break; done` — five consecutive PASS results, each run under 120 s, zero race-detector findings.

- [ ] T032 [US2] Add the goroutine-leak detector teardown to every `harness.New*` builder's `t.Cleanup` (called from T023–T028): `runtime.NumGoroutine` pre-snapshot at construction time, post-snapshot at `t.Cleanup`, bounded `runtime.Gosched()` poll (max 100 iterations) per research.md §4. On leak, fail with labeled `runtime.Stack` dump. FR-025-20 + SC-025-5 enforcement.
- [ ] T033 [US2] Verify `FakeClock` covers every documented transition in T014/T015/T016/T018: refresh-window firing, JWT expiry, grace cache TTL, boot retry backoff. Grep `tests/integration/` for any `time.Sleep` call — fail the review if one drives a documented transition (FR-025-16). Bounded `runtime.Gosched` polls inside private harness helpers are allowed; exported `Sleep` is not (per contracts/harness-api.md Anti-API).
- [ ] T034 [US2] Add a wall-clock budget check to `tests/integration/lifecycle_test.go`'s `TestMain`: capture `time.Now()` at suite start, log elapsed at end, fail soft (`t.Log`) if total > 120 s on this host (SC-025-3). Add the leak-test for FR-025-13: `TestMain`'s `RoundTripper` allow-list records every host attempted; on suite teardown, log the attempted-host set and fail if any host outside the registered httptest endpoints appears.

**Checkpoint**: SC-025-2 + SC-025-3 + SC-025-4 + SC-025-5 + SC-025-6 + SC-025-8 measurable from one command.

---

## Phase 5: User Story 3 — Security reviewer: zero sentinel leakage (Priority: P3)

**Goal**: Every captured byte stream across every scenario is sentinel-clean after the scenario completes. Alert payloads identify scope name only — never the secret value.

**Independent Test**: For each of the 15 scenarios, `AssertSentinelAbsent` runs successfully over the 6-stream coverage list (operational slog, audit JSONL, status-socket bytes, Discord alerts, child stdout/stderr, error message strings).

- [ ] T035 [P] [US3] Audit every scenario test file (T007–T022) to confirm Contract D's exact call-shape from contracts/scenario-assertions.md: `harness.AssertSentinelAbsent(t, sentinel, logs.Bytes(), server.RawAudit(), sup.StatusRaw(), discord.AlertsRaw(), child.Stdout(), child.Stderr(), errorMessages(t)...)` with all 6 streams present. Any scenario missing a stream fails Constitution X. Add a `harness.CollectErrors(...)` helper if scenario error-collection is not yet centralized.
- [ ] T036 [P] [US3] Audit Scenarios 05, 06, 15 (the alert-emitting trio) to confirm FR-025-27: their `discord.Alerts()` recorded payloads MUST contain the scope name + alert class but never the plaintext secret value. Add explicit per-scenario assertion: `for _, a := range discord.Alerts() { require.NotContains(t, a.Body, sentinel) }`. Same check on `[STALE] Validator Failure` (Scenario 6) and `[STALE] Log Pattern Match` (Scenario 15) payload bodies.
- [ ] T037 [US3] Ensure every scenario constructs at least one secret via `testutil.SentinelSecret(N)` with `N` unique per scenario (FR-025-25). Grep `tests/integration/scenarios_test.go` for `SentinelSecret(` occurrences and confirm 1+ per scenario; document the sentinel-N table in a code comment block at the top of `scenarios_test.go` for reviewer cross-reference.

**Checkpoint**: SC-025-7 (zero captured streams contain the sentinel) verifiable from a single run.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Confirm the release-gate criteria (5-run flake check, race-detector run, AC-MATRIX update) and tie off documentation.

- [ ] T038 Run `magex test:race -tags=integration ./tests/integration/...` once and confirm zero race-detector findings (SC-025-4). Capture the elapsed time and confirm < 120 s on a macOS arm64 / Linux amd64 developer-class machine (SC-025-3). If a race appears, root-cause in the harness or production code — do NOT add `sync.Mutex` to mask a real race (Constitution IX).
- [ ] T039 Run the 5-consecutive-run flake gate: `for i in 1 2 3 4 5; do magex test:race -tags=integration ./tests/integration/... || break; done`. All five runs MUST PASS with no input change (SC-025-2). On any flake, root-cause + fix; do NOT add a retry loop or a `t.Skip`-on-flake. After five consecutive passes, record the result in the commit message / release notes.
- [ ] T040 [P] Update `docs/AC-MATRIX.md` AC-10 row: list all 15 scenario test paths (`tests/integration/scenarios_test.go::Test_Scenario_NN_<slug>`) with their pass/fail status; row status reaches `green` when all 15 pass against fully-shipped providers (FR-025-28). Update AC-9 row to cite the integration suite as part of test-infra completeness evidence (FR-025-29).
- [ ] T041 [P] Verify SC-025-11 (default-build invisibility) one final time: `go test ./tests/integration/...` (no `-tags=integration`) prints `no Go files in ...` and exits 0. Verify `go test -race ./...` (full repo, no `-tags=integration`) does not pull a single integration file into the build.
- [ ] T042 [P] Run `magex lint` to confirm the depguard rule from T002 fires if any production file imports `tests/integration/harness`; intentionally add a temporary import in a `_test.go` (NON-integration) file, confirm depguard catches it, revert.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — starts immediately.
- **Foundational (Phase 2)**: Depends on Phase 1 completion. **BLOCKS all scenario authoring + harness building.**
- **US1 Sub-phase 3a (Scenarios)**: Depends on Phase 2 completion. **MUST be authored before Sub-phase 3b** (TDD per Constitution VIII + user request).
- **US1 Sub-phase 3b (Harness)**: Depends on Sub-phase 3a completion (the failing tests are the contract the harness fulfills).
  - Within 3b: T023 (log_capture), T024 (vault), T025 (discord), T026 (child) are all independent → [P].
  - T027 (server) depends on T023 + T024 + T025.
  - T028 (supervisor) depends on T023 + T026 + T027.
- **US1 Sub-phase 3c**: Depends on Sub-phase 3b completion.
- **US2 (Phase 4)**: Depends on Sub-phase 3b (harness exists) — properties layered on top.
- **US3 (Phase 5)**: Depends on Sub-phase 3a + 3b — every scenario must be authored + harness must capture streams.
- **Polish (Phase 6)**: Depends on Phases 3, 4, 5 complete.

### Within Each Story

- US1: Tests (Sub-phase 3a) written FIRST and FAILING before harness (Sub-phase 3b) is built. TDD red-green explicit.
- US2: Validation-only — no new code path; audits the harness built in US1.
- US3: Validation-only — audits the AssertSentinelAbsent calls placed during US1.

### Parallel Opportunities

- **Phase 1**: T002 + T003 in parallel after T001.
- **Phase 3a (Scenarios)**: T007–T022 all in parallel — each writes to a **different test function** in `tests/integration/scenarios_test.go`. **Note**: file is shared, so coordinate via clean diffs (one function per task) and merge as separate commits.
- **Phase 3b (Harness)**: T023, T024, T025, T026 in parallel (different files). T027 + T028 sequential after their deps.
- **Phase 3c**: T029 + T030 in parallel.
- **Phase 4 / Phase 5**: All tasks in parallel within phase (audit work, different review angles).
- **Phase 6**: T040 + T041 + T042 in parallel (documentation + verification).

---

## Parallel Example: Sub-phase 3a (Scenario authoring — TDD red)

```bash
# Author all 15 scenario tests concurrently (each is one Go function in scenarios_test.go):
Task: "Implement Test_Scenario_01_FirstInteractive (T007)"
Task: "Implement Test_Scenario_02_DaemonBootstrap (T008)"
Task: "Implement Test_Scenario_03_CleanExitSilentRefill (T009)"
Task: "Implement Test_Scenario_04_ChildCrashSilentRefill (T010)"
Task: "Implement Test_Scenario_05_Exit78StaleCreds (T011)"
Task: "Implement Test_Scenario_06_ValidatorBlocksChild (T012)"
Task: "Implement Test_Scenario_07_VaultRestart (T013)"
Task: "Implement Test_Scenario_08_DaytimeRefresh (T014)"
Task: "Implement Test_Scenario_09_OvernightExpiry_Strict (T015)"
Task: "Implement Test_Scenario_09_OvernightExpiry_Grace (T016)"
Task: "Implement Test_Scenario_10_DiscordUnavailable (T017)"
Task: "Implement Test_Scenario_11_TailscaleBootRetry (T018)"
Task: "Implement Test_Scenario_12_StatusGate (T019)"
Task: "Implement Test_Scenario_13_RotationMidSession (T020)"
Task: "Implement Test_Scenario_14_DuplicateSupervisor (T021)"
Task: "Implement Test_Scenario_15_LogPatternAlert (T022)"
```

## Parallel Example: Sub-phase 3b (Harness construction — TDD green)

```bash
# Build leaf harness files in parallel (no inter-dep):
Task: "Implement harness/log_capture.go (T023)"
Task: "Implement harness/vault.go (T024)"
Task: "Implement harness/discord.go (T025)"
Task: "Implement harness/child.go (T026)"
# Then sequential:
Task: "Implement harness/server.go (T027) — depends on T023+T024+T025"
Task: "Implement harness/supervisor.go (T028) — depends on T023+T026+T027"
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Complete Phase 1: Setup.
2. Complete Phase 2: Foundational (TestMain + scenario stubs + RoundTripper allow-list).
3. Complete Phase 3a: author all 15 scenario tests as failing tests (TDD red).
4. Complete Phase 3b: build the 6 harness files (TDD green).
5. Complete Phase 3c: cross-cutting assertion helpers.
6. **STOP and VALIDATE**: 15 scenarios green (modulo provider-chunk dependencies). AC-10 row of `docs/AC-MATRIX.md` partially populated.

### Incremental Delivery

1. Setup + Foundational → suite skeleton compiles, default `go test ./...` ignores it.
2. Add US1 → 15 scenarios green → AC-10 ships.
3. Add US2 → flake-free + race-free + leak-free → maintainer-trusted gate.
4. Add US3 → sentinel-leak proof → security-reviewer evidence.
5. Polish → 5-run flake gate + AC-MATRIX final + lint check.

### TDD Discipline (Constitution VIII, load-bearing)

- Every scenario test in Sub-phase 3a MUST fail (compile-or-runtime) before its supporting harness piece in Sub-phase 3b is written. That failure is the evidence the harness has work to do.
- Skipping a scenario to soften a missing audit event is forbidden (FR-025-7 + edge-case row 1). The audit log is a normative contract.
- A scenario that "almost passes" is a failure. The four contracts (A/B/C/D) are mandatory and binary per FR-025-10.

---

## Notes

- [P] tasks = different files OR fully-disjoint mutable state, no dependency on incomplete tasks.
- [US1/US2/US3] label maps task to spec.md user story for traceability.
- All harness code lives at `tests/integration/harness/*.go`; all scenario tests at `tests/integration/scenarios_test.go`. **Both gated by `//go:build integration`.**
- Zero new files under `internal/*` — every SDD-01..SDD-23 PACKAGE-MAP is locked (per plan.md Constraints).
- Zero new direct `go.mod` dependency — stdlib + already-in-module `stretchr/testify` only (Constitution XI).
- Constitution IX enforcement: no `init()`, no package-level mutable globals in the harness; every harness-spawned goroutine has owner + ctx + termination + top-frame `recover()` + leak-detector teardown.
- Constitution X enforcement: every captured byte stream per scenario flows through `AssertSentinelAbsent`; alert payloads scope-name-only per FR-025-27.
- Verify TDD ordering before commit: `git log --oneline` should show every scenario test commit *before* its harness-builder commit.
- Commit boundary suggestion: one commit per task (T0NN) for clean traceability + bisectability; squash on PR if reviewer prefers.
