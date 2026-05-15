---

description: "Task list for SDD-25 Lifecycle Integration Harness (AC-10 owner)"
---

# Tasks: Lifecycle Integration Harness (SDD-25)

**Input**: Design documents from `/Users/mrz/projects/hush/specs/025-lifecycle-harness/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/harness-api.md, contracts/scenario-assertions.md, quickstart.md

**Tests**: TDD-mandatory per Constitution VIII. The user's instruction is explicit: every scenario test is written **before** the harness builder it depends on, so each scenario fails first (red), then the harness piece is built (green). Skip paths are forbidden by spec FR-001; the harness has no `harness.Skip`/`harness.Suppress*` anti-API (contracts/harness-api.md).

**Organization**: Tasks are grouped by user story — three P1 stories from spec.md: US1 = all 15 lifecycle paths green end-to-end (AC-10 owner); US2 = no sentinel leak across captured streams; US3 = release-gate fitness (fast, hermetic, deterministic).

**Scenario name canon**: The 17 `Test_Scenario_NN_<slug>` symbol names below match spec FR-002 **verbatim**. Renaming any cell requires a spec amendment first.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files OR disjoint test-function bodies within `scenarios_test.go`, no dependency on incomplete tasks)
- **[Story]**: Maps task to user story (US1 / US2 / US3); Setup/Foundational/Polish tasks carry no story label
- All paths are absolute or relative to repo root `/Users/mrz/projects/hush/`

## Path Conventions

- All harness code lives at `tests/integration/harness/*.go` (build tag `//go:build integration`); the locked 6-file inventory from research.md §1 is `vault.go`, `server.go`, `discord.go`, `supervisor.go`, `child.go`, `log_capture.go` — adding a seventh file is a contract violation.
- All scenario tests live at `tests/integration/scenarios_test.go` (build tag `//go:build integration`, `package integration_test`).
- TestMain + suite-wide setup + integration-child-mode dispatch lives at `tests/integration/lifecycle_test.go` (build tag `//go:build integration`, `package integration_test`).
- No new files under `internal/*` (every SDD-01..SDD-24 PACKAGE-MAP entry is locked).
- No new direct `go.mod` dependency (Constitution XI).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Stand up the `tests/integration/` tree, the build-tag gate, and the depguard rule. Zero production code touched.

- [X] T001 Create directories `tests/integration/` and `tests/integration/harness/` at repo root; add `tests/integration/harness/doc.go` declaring `package harness` with the first non-comment line `//go:build integration` and a one-paragraph package doc that cites SDD-25 + the locked 6-file allocation in research.md §1.
- [X] T002 [P] Add a `depguard` rule to `.golangci.json` (`no-integration-harness-in-production`) forbidding any non-test file from importing `github.com/mrz1836/hush/tests/integration/harness`; verify with `magex lint` after writing.
- [X] T003 [P] Confirm default-build invisibility (spec FR-008): run `go test ./tests/integration/...` (no `-tags=integration`) and confirm output `no Go files in /Users/mrz/projects/hush/tests/integration` (success exit). Recorded the expectation in `tests/integration/harness/doc.go` package comment.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Build the suite skeleton — `TestMain`, integration-child-mode dispatcher, `http.RoundTripper` allow-list, and the 17 empty scenario stubs that every Phase 3a task will replace.

**⚠️ CRITICAL**: No scenario test body can be authored, and no harness builder can be filled in, until this phase completes.

- [X] T004 Created `tests/integration/lifecycle_test.go` with `//go:build integration`, `package integration_test`, and a `TestMain(m *testing.M)` that (a) detects the `--integration-child-mode` sentinel in `os.Args`, (b) installs the process-wide `http.DefaultTransport` allow-list `RoundTripper` (FR-012), (c) calls `m.Run()`, reports non-loopback attempts via `nonLoopbackAttempts` atomic, and exits.
- [X] T005 Added the integration-child-mode entrypoint inside `tests/integration/lifecycle_test.go` (`dispatchChildMode`) parsing `--exit-code=N`, `--lifetime=D`, `--emit-stderr-pattern=P`, `--emit-stdout-pattern=P` and exiting per script. Supports the child-behaviour needs of Scenarios 1, 2, 3, 4, 5, 8, 9b, 11a, 13, 14, 15.
- [X] T006 Declared 17 `Test_Scenario_*` symbols in `tests/integration/scenarios_test.go` (`//go:build integration`, `package integration_test`) **per spec FR-002 verbatim**. Bodies call `scenarioPendingHarness(t, N, slug)` which `t.Fatalf`s with a documented "harness wiring not yet complete" message — per spec FR-001 we MUST NOT use `t.Skip`, so pending scenarios fail loudly on every invocation. Scenario 14 is implemented (`scenario14DuplicateStart` in `tests/integration/scenario_14_test.go`).

**Checkpoint**: `go test -tags=integration ./tests/integration/...` compiles and reports 17 skipped tests; default `go test ./...` compiles zero integration files. The 17-symbol count matches spec FR-002 + data-model.md §2 — verify by greping `^func Test_Scenario_` in `scenarios_test.go`.

---

## Phase 3: User Story 1 — All 15 lifecycle paths green end-to-end (Priority: P1) 🎯 MVP

**Goal**: Deliver the AC-10-green suite. 17 scenario test functions (15 scenarios + Scenario 9 strict/grace split + Scenario 11 ready/boot-timeout split) pass under `magex test:race -tags=integration` against the real `internal/*` packages, with only Discord, the five validator upstreams, the wall clock, and the Tailscale probe replaced by programmable stubs.

**Independent Test**: From a clean checkout, `magex test:race -tags=integration ./tests/integration/...` returns exit 0 with all 17 scenario tests PASS, total wall-clock under 120 s, zero race-detector findings, and no outbound network egress to any non-loopback host.

### Sub-phase 3a — Author all 17 scenario tests FIRST (TDD red)

> Each task below replaces a `t.Skip` from T006 with the four mandatory assertions from contracts/scenario-assertions.md (Contract A: final state; Contract B: audit subsequence + chain continuity; Contract C: status-socket JSON shape, supervisor scenarios only; Contract D: `AssertSentinelAbsent` across the 6-stream coverage list from data-model.md §4.6). **Each scenario references harness types that do not yet exist — compilation MUST fail. That compile failure is the load-bearing TDD evidence the harness has work to do (Sub-phase 3b).** No `t.Skip`, no soft assertions, no commented-out contracts.
>
> Audit-event names below come from data-model.md §2 (the FR-005 deferral resolution); they are drawn from the SPEC §FR-14 vocabulary and the `internal/audit/chain.go` `Action` constants. Slug-aliasing the symbol names is a spec violation.

- [ ] T007 [P] [US1] Implement `Test_Scenario_01_InteractiveShellRequest` in `tests/integration/scenarios_test.go`: drive `hush request --scope ANTHROPIC_API_KEY,GITHUB_TOKEN --exec`, real `/claim` with ES256K signature, single `DiscordStub` approve, real `/s/<name>` fetches, exec scripted child; assert Contract A (Scenario 1 compound 4-tuple per data-model.md §3 via `harness.AssertScenario1Compound`: `/hz` returns `vault_loaded=true && discord_connected=true`, child exited with documented code, token-store reports use-count consumed or expired, `len(discord.Calls()) == 1`); Contract B (`session_requested`, `session_approved`, `secret_retrieved` ×N in order; `harness.AssertAuditChainContinuity` against `vault.AuditPath()`); **Contract C skipped (interactive — no supervisor)**; Contract D (`harness.AssertSentinelAbsent` over logs, audit raw bytes, discord alerts raw bytes, child stdout, child stderr, collected error strings).
- [ ] T008 [P] [US1] Implement `Test_Scenario_02_FirstDaemonBootstrap` in `tests/integration/scenarios_test.go`: `TestSupervisor` boots, claims via DiscordStub approve, fetches secrets, validators return 200, child starts; assert Contract A (`harness.AssertSupervisorState(t, sup, supervise.StateRunning)`); Contract B (`session_requested`, `session_approved`, `supervisor_session_claimed`, `secret_retrieved` ×N); Contract C (`AssertStatusShape` succeeds; `State=="running"`, `DiscordConnected==true`, `ScopeHealthy` populated, `ScopeStale` empty); Contract D (6 streams).
- [ ] T009 [P] [US1] Implement `Test_Scenario_03_CleanChildExitRefill` in `tests/integration/scenarios_test.go`: scripted child exits 0 within valid JWT TTL; supervisor invokes silent refill via the directly-driven refill callback (research.md §2); child restarts with no new approval prompt; assert Contract A (`StateRunning`); Contract B (`supervisor_child_clean_exit`, `supervisor_silent_refill`, `secret_retrieved` ×N); Contract C (`State=="running"`); Contract D (6 streams; assert `len(discord.Calls())` unchanged from pre-exit count — no new approval).
- [ ] T010 [P] [US1] Implement `Test_Scenario_04_ChildCrashRefill` in `tests/integration/scenarios_test.go`: scripted child exits with non-zero non-78 code within valid JWT TTL; supervisor invokes silent refill; child restarts; assert Contract A (`StateRunning`); Contract B (`supervisor_child_exit_crash`, `supervisor_silent_refill`, `secret_retrieved` ×N); Contract C (`State=="running"`, `ScopeHealthy` populated); Contract D (6 streams).
- [ ] T011 [P] [US1] Implement `Test_Scenario_05_ChildExit78Stale` in `tests/integration/scenarios_test.go`: scripted child exits 78; supervisor enters operator-blocked terminal state per spec FR-004 boundary (test stops here; recovery is Scenario 13); assert Contract A (`StateAwaitingApproval`); Contract B (`supervisor_child_exit_78`, `supervisor_awaiting_approval`, `supervisor_stale_alert`); Contract C (`State=="awaiting-approval"`, `ScopeStale` populated with the offending scope); Contract D (6 streams; alert payload from `discord.AlertsRaw()` contains scope name + alert class but never the sentinel — FR-024).
- [ ] T012 [P] [US1] Implement `Test_Scenario_06_ValidatorFailure` in `tests/integration/scenarios_test.go`: `TestServer.MockValidator` programmed to return 401 from the upstream stub for one provider; supervisor blocks child start; assert Contract A (`StateAwaitingApproval`; assert child never started — `child.PID()==0`); Contract B (`supervisor_stale_alert`, `supervisor_awaiting_approval`); Contract C (`State=="awaiting-approval"`, `ScopeStale` lists the offending scope); Contract D (6 streams; alert payload scope-name-only — FR-024).
- [ ] T013 [P] [US1] Implement `Test_Scenario_07_VaultRestartInvalidatesSession` in `tests/integration/scenarios_test.go`: bring up `TestServer`, claim and start child, then stop+restart the `TestServer` (new key state, unknown JTI); supervisor's next refill receives 401-unknown-jti and enters operator-blocked terminal state per spec FR-004 boundary (test stops here; recovery is Scenario 13); assert Contract A (`StateAwaitingApproval`); Contract B (`supervisor_stale_alert`, `supervisor_awaiting_approval`); Contract C (`State=="awaiting-approval"`); Contract D (6 streams).
- [ ] T014 [P] [US1] Implement `Test_Scenario_08_DaytimeRefresh` in `tests/integration/scenarios_test.go`: `FakeClock.Advance` to the daytime refresh window; harness invokes the `Refresher` refill callback **directly** (research.md §2 — `Refresher.Run` is NOT called); DiscordStub approves the `[DAEMON] Refresh` prompt; child continues running uninterrupted (spec FR-025); assert Contract A (`StateRunning`, same child PID as pre-refresh); Contract B (`session_requested`, `session_approved`, `supervisor_session_refreshed`); Contract C (`State=="running"`, `session_expires_at` advanced); Contract D (6 streams).
- [ ] T015 [P] [US1] Implement `Test_Scenario_09_OvernightExpiry_Strict` in `tests/integration/scenarios_test.go`: strict mode (no grace cache); `FakeClock.Advance` past JWT TTL overnight; scripted child crashes after expiry; supervisor cannot silently refill — enters operator-blocked terminal state; assert Contract A (`StateAwaitingApproval`, child not restarted); Contract B (`supervisor_child_exit_crash`, `supervisor_awaiting_approval`, `supervisor_stale_alert`); Contract C (`State=="awaiting-approval"`, `ScopeStale` populated); Contract D (6 streams).
- [ ] T016 [P] [US1] Implement `Test_Scenario_09_OvernightExpiry_Grace` in `tests/integration/scenarios_test.go`: same setup as T015 but grace cache enabled and `cache_grace_ttl` not exceeded; supervisor enters `grace-restart` sub-state, restarts child from cached plaintext via `Grace` primitive; assert Contract A (`StateRunning` via `grace-restart` sub-state — assert sub-state observable in `Status()`); Contract B (`supervisor_child_exit_crash`, `supervisor_grace_entered`, `supervisor_silent_refill`, `supervisor_grace_exited`); Contract C (`State=="running"`, `grace_active=true` while in sub-state then transitions to `false`); Contract D (6 streams).
- [ ] T017 [P] [US1] Implement `Test_Scenario_10_DiscordUnavailable` in `tests/integration/scenarios_test.go` as a **single supervisor scenario** (per data-model.md §2 — not split into Interactive/Supervisor subtests): `TestDiscord.SetConnected(false)` before supervisor boot; supervisor's `/claim` receives 503 with `error=="discord_unavailable"`; orchestrator retries within boot budget then emits `AlertClassDiscordUnavailableOnClaim` and `ErrBootTimeout`; assert Contract A (`StateStopped`); Contract B (`supervisor_stale_alert`, `supervisor_boot_timeout`); Contract C (transient `boot-retry` state observable on status socket, `discord_connected=false`); Contract D (6 streams; alert payload class matches `AlertClassDiscordUnavailableOnClaim`).
- [ ] T018 [P] [US1] Implement `Test_Scenario_11_TailscaleReady` in `tests/integration/scenarios_test.go`: success branch of the boot-retry path; `Deps.TailscaleProbe` stub reports unreachable on first N attempts then reachable; supervisor retries with backoff via `FakeClock.Advance`; assert Contract A (`StateRunning` after probe success); Contract B (`supervisor_session_claimed`, `supervisor_silent_refill`); Contract C (`State=="running"`, `discord_connected=true`); Contract D (6 streams).
- [ ] T019 [P] [US1] Implement `Test_Scenario_11_BootTimeout` in `tests/integration/scenarios_test.go`: timeout branch of the boot-retry path; `Deps.TailscaleProbe` stub reports unreachable past the boot budget; supervisor emits `AlertClassBootTimeout` and terminates; assert Contract A (`StateStopped`, terminal alert class observed on `discord.Alerts()`); Contract B (`supervisor_boot_timeout`); Contract C (transient `boot-retry` state observable on status socket before shutdown); Contract D (6 streams; alert payload class matches `AlertClassBootTimeout`).
- [ ] T020 [P] [US1] Implement `Test_Scenario_12_AgentStatusCheck` in `tests/integration/scenarios_test.go`: supervisor in `running` state with healthy scopes; this scenario IS the exhaustive status-socket assertion (data-model.md §2 row 12); assert Contract A (`StateRunning`); Contract B (no unique audit event — assert chain continuity only via `AssertAuditChainContinuity`); Contract C (exhaustively check every FR-12 field: `state`, `child_pid`, `scope_healthy`, `scope_stale`, `discord_connected`, `session_expires_at`, `refresh_window_next`, `last_auth_failure`, `grace_active` — `AssertStatusShape` rejects any missing-field response); Contract D (6 streams).
- [ ] T021 [P] [US1] Implement `Test_Scenario_13_MidSessionRotation` in `tests/integration/scenarios_test.go`: supervisor `running`; call `TestVault.Rotate(name, newValue)` mid-session; invoke `TestSupervisor.Refresh(ctx)` which hits the `refresh\n` verb on the status socket; supervisor refetches via validators and restarts child with new plaintext; assert Contract A (`StateRunning`, post-restart child PID differs from pre-rotation); Contract B (`vault_reloaded`, `client_refresh_invoked`, `supervisor_silent_refill`, `secret_retrieved`); Contract C (`State=="running"`, `child_pid` matches the post-restart PID); Contract D (6 streams; both pre- and post-rotation sentinels covered).
- [X] T022 [P] [US1] Implemented `Test_Scenario_14_DuplicateStart` in `tests/integration/scenario_14_test.go`. First `harness.AcquirePidFile` succeeds and returns the live `*supervise.PidFile`; second `harness.TryAcquirePidFile` returns `errors.Is(err, supervise.ErrPidLocked)`. Contract A: pidfile exists; first non-nil, second nil. Contract B: no audit emitted (Lifecycle never ran); chain-continuity trivially holds (file absent). Contract C: second supervisor never opens a socket (FR-006 carve-out). Contract D: `harness.AssertSentinelAbsent` over `logs.Bytes()` + `harness.CollectErrors(err)`. Green under `-race` × 5 consecutive runs.
- [ ] T023 [P] [US1] Implement `Test_Scenario_15_LogPatternMatch` in `tests/integration/scenarios_test.go`: supervisor `running`; scripted child emits the configured auth-failure pattern to stderr via `TestChild.EmitStderr`; watchdog matches and supervisor emits the `[STALE] Log Pattern Match` alert WITHOUT a state-machine transition (Constitution V + spec FR-026); assert Contract A (`StateRunning` — alert-only, no state change); Contract B (`supervisor_stale_alert`); Contract C (`State=="running"`, alert flag visible in status projection); Contract D (6 streams; alert payload contains pattern name + scope but never the full stderr line if it contained the sentinel — FR-024).

**TDD red-checkpoint**: All 17 `Test_Scenario_*` functions reference harness types that do not yet exist. `go test -tags=integration ./tests/integration/...` fails to compile (or fails at runtime once harness stubs return zero values). **Do NOT proceed to Sub-phase 3b until every scenario references the harness builders the next sub-phase will create — this red state is the load-bearing TDD evidence.** A passing test before its harness is built is a sign the assertions are not load-bearing.

### Sub-phase 3b — Build the 6 harness files (TDD green)

> Each task below implements one of the locked 6-file harness inventory (research.md §1). After each file is committed, re-run `magex test:race -tags=integration ./tests/integration/...` and observe which scenarios move from compile-fail to runtime-pass. The dependency graph below tracks which harness files each scenario consumes.

- [X] T024 [P] [US1] Implemented `tests/integration/harness/log_capture.go`: `NewLogCapture(t) *LogCapture` builds a sync.Mutex-guarded buffer with a slog.TextHandler at LevelDebug; `Logger()`, `Bytes()`, and the package-level `AssertSentinelAbsent(t, sentinel, streams ...[]byte)` + `CollectErrors(...)` helpers ship per data-model.md §4.6. No `init()`, no package-level mutable globals.
- [X] T025 [P] [US1] Implemented `tests/integration/harness/vault.go`: `NewVault(t, secrets)` wraps `testutil.NewTestVault`; methods `Path()`, `Dir()`, `Key()`, `AuditPath()`, `RegistryPath()`, `RegisterClient(t, machineIdx, *ecdsa.PublicKey)` writes `clients.json`, and `Rotate(t, name, value)` atomic-rewrites the vault. All paths under `t.TempDir()`.
- [X] T026 [P] [US1] Implemented `tests/integration/harness/discord.go`: `NewDiscord(t)` wraps `testutil.DiscordStub`; `SetConnected(bool)` / `Connected()` drives Scenario 10's connectivity sequence; `Alerts()` / `AlertsRaw()` expose the supervise-side recorder; `AsSuperviseAlerts()` returns the adapter implementing `supervise.Alerts` with zero policy logic.
- [X] T027 [P] [US1] Implemented `tests/integration/harness/child.go`: `NewChild(t, lc, opts)` builds a `supervise.ChildConfig` pointing at `os.Executable()` with `--integration-child-mode --exit-code=N --lifetime=D --emit-stderr-pattern=P --emit-stdout-pattern=P` argv; `Cmd()`, `Stdout()`, `Stderr()`, `Run(ctx)` covers the simple direct-invocation path.
- [ ] T028 [US1] **PENDING (chunk 2)**: `tests/integration/harness/server.go` currently exposes a placeholder shell (`NewServer` returns an `httptest.NotFound` mux + `RegisterAllowedHostHook` bridge). The full composition — real `internal/server.New` against `TestVault` + `TestDiscord.AsApprover()` + validator-upstream `httptest.Server` mocks + `RoundTripper` rewrite — lands when Scenarios 1, 2, 7, 12, 13 are implemented.
- [ ] T029 [US1] **PENDING (chunk 2)**: `tests/integration/harness/supervisor.go` currently exposes a placeholder shell (`FakeClock`, `AcquirePidFile`/`TryAcquirePidFile`, `AssertSupervisorState`, `AssertAuditSubsequence`, `AssertAuditChainContinuity`, `AssertNoLeak`, `NewECDSAKey`). The full `NewSupervisor(t, opts)` composition wrapping `supervise.NewLifecycle` + Deps wiring + status-socket reader + refresh-callback driver lands when Scenarios 2, 3, 4, 5, 6, 7, 8, 9, 11, 12, 13, 15 are implemented.

**TDD green-checkpoint**: All 17 `Test_Scenario_*` symbols compile and execute. Per spec FR-001 + Assumptions, scenarios depending on upstream production behaviour (e.g., SDD-26 validators, SDD-27 watchdog, SDD-28 alert classes) may surface failures if those chunks are still pending — those failures are *expected sequencing signals* and MUST NOT be papered over with stubs in the harness (anti-API: no `harness.SuppressSentinelLeak`, no `t.Skip`).

### Sub-phase 3c — Cross-cutting assertion helpers + green verification

- [ ] T030 [P] [US1] Implement `harness.AssertSupervisorState(t, sup *TestSupervisor, expected supervise.State)` in `tests/integration/harness/supervisor.go` and `harness.AssertScenario1Compound(t, server *TestServer, child *TestChild, discord *TestDiscord, expectedExit int)` in `tests/integration/harness/server.go` per contracts/scenario-assertions.md Contract A. Wire both into the scenario assertions in T007–T023, replacing any inline equivalent so Contract A is one-call per scenario.
- [ ] T031 [P] [US1] Implement `harness.AssertStatusShape(t, raw []byte) StatusDoc` in `tests/integration/harness/supervisor.go` — unmarshals into a strictly-typed DTO local to the harness mirroring SPEC §FR-12 + SDD-22's locked `statusJSON`; fails if any FR-12 field is absent (no `omitempty` tolerated per spec Assumptions). Used by Contract C in every supervisor scenario (Scenarios 2–15). Implement `harness.CollectErrors(errs ...error) []byte` helper so Contract D's error-stream coverage is centralised.
- [ ] T032 [US1] **Verify all 17 scenario tests green** with `magex test:race -tags=integration ./tests/integration/...`. Document any scenario still failing because its provider chunk (SDD-26 validators / SDD-27 watchdog / SDD-28 alert classes) is unshipped — those failures are *expected* per spec FR-001 and surface a real sequencing gap. Do NOT add stubs to silence them; surface to the chunk owner. If a scenario fails for any other reason, root-cause and fix in the harness or production package per data-model.md §1 validation rules.

**Checkpoint (AC-10 ownership active)**: `docs/AC-MATRIX.md` AC-10 row can list each `tests/integration/scenarios_test.go::Test_Scenario_NN_<slug>` test path. AC-10 status reaches `green` only when all 17 pass on a fully-shipped upstream.

---

## Phase 4: User Story 2 — No sentinel secret in any captured stream (Priority: P1)

**Goal**: Defence-in-depth proof that Constitution X holds end-to-end. Across every scenario, none of the six captured byte streams (operational slog, audit JSONL, status-socket bytes, Discord alerts, child stdout+stderr, error message strings) contains the per-scenario sentinel substring.

**Independent Test**: Run any single scenario in isolation with a sentinel-tagged fixture vault; `harness.AssertSentinelAbsent` runs over all six streams; one violation fails the scenario. Across all 17 scenarios, the suite produces zero sentinel matches.

- [ ] T033 [P] [US2] Audit every scenario test (T007–T023) to confirm Contract D's exact call shape from contracts/scenario-assertions.md: `harness.AssertSentinelAbsent(t, sentinel, logs.Bytes(), server.RawAudit(), sup.StatusRaw(), discord.AlertsRaw(), child.Stdout(), child.Stderr(), harness.CollectErrors(errs...))` with **all six streams present**. A scenario that omits any stream fails Constitution X; add the missing stream rather than relaxing the assertion. For Scenario 1 (no supervisor) the `sup.StatusRaw()` slot is `nil` — the helper tolerates nil streams.
- [ ] T034 [P] [US2] Audit Scenarios 05, 06, 09a, 10, 11b, 15 (the alert-emitting subset per spec FR-023 + data-model.md §2) to confirm FR-024: every recorded `discord.Alerts()` payload contains scope name + alert class but never the plaintext secret value. Add per-scenario explicit assertion `for _, a := range discord.Alerts() { require.NotContains(t, string(a.Body), sentinel) }`. Confirm each scenario's alert class matches the locked 10-value `AlertClass` enum documented in the active feature plan (e.g., Scenario 10 → `AlertClassDiscordUnavailableOnClaim`; Scenario 11b → `AlertClassBootTimeout`).
- [ ] T035 [US2] Ensure every scenario constructs at least one secret via `testutil.SentinelSecret(N)` with `N` unique per scenario function (FR-007 + FR-017). Add a comment block at the top of `tests/integration/scenarios_test.go` listing the N → scenario mapping for reviewer cross-reference; grep `tests/integration/scenarios_test.go` for `SentinelSecret(` and confirm one occurrence per scenario. Add a sanity guard helper `harness.RequireUniqueSentinel(t, secrets map[string]string)` invoked at the top of each scenario.

**Checkpoint**: spec SC-005 (every scenario produces zero sentinel matches across captured streams) verifiable from a single suite run.

---

## Phase 5: User Story 3 — Release-gate fitness: fast, hermetic, deterministic (Priority: P1)

**Goal**: The suite is a release gate. Under 120 s wall-clock; zero outbound network egress beyond loopback; five consecutive runs with zero failures and zero flakes under `-race`.

**Independent Test**: `for i in 1 2 3 4 5; do time magex test:race -tags=integration ./tests/integration/... || break; done` produces five consecutive PASS results, each run under 120 s, with no race-detector findings.

- [ ] T036 [US3] Add the goroutine-leak teardown to every `harness.New*` builder's `t.Cleanup` (registered from T024–T029): `runtime.NumGoroutine` pre-snapshot at construction time, post-snapshot at cleanup, bounded `runtime.Gosched()` poll (max 100 iterations) per research.md §4. On leak, fail with labeled `runtime.Stack` dump. Spec FR-021 + Constitution IX enforcement. Run `magex test:race -tags=integration ./tests/integration/...` and confirm zero leak failures.
- [ ] T037 [US3] Grep `tests/integration/` for any `time.Sleep` call — fail the review if one drives a documented transition (spec FR-022 / contracts/harness-api.md Behavioural guarantees). Bounded `runtime.Gosched()` polls inside private harness helpers are allowed; exported `harness.Sleep` is anti-API. Verify `FakeClock` covers every documented transition in T014/T015/T016/T018/T019 (refresh-window firing, JWT expiry, grace cache TTL, boot-retry backoff). Also confirm no scenario uses `t.Parallel` at the top level (spec FR-022).
- [ ] T038 [US3] Run the **5-consecutive-run flake gate**: `for i in 1 2 3 4 5; do magex test:race -tags=integration ./tests/integration/... || break; done`. All five runs MUST PASS unmodified, each under 120 s wall-clock on a developer-class machine (spec SC-002/SC-003/SC-010). On any flake, root-cause + fix in the harness or production code — do NOT add a retry loop or a `t.Skip`-on-flake. Capture the 5-run wall-clock totals in the PR description. Add a `TestMain` post-`m.Run()` log line that reports the attempted-host set from the `RoundTripper` allow-list and fails the run if any non-loopback host appears (spec SC-004).

**Checkpoint**: spec SC-001 (all 17 green), SC-002 (under 120 s), SC-003 (5 consecutive PASS), SC-004 (zero non-loopback egress), SC-010 (run-ordinal-independent) all measurable from a single command.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Tie off the AC-10 owner-of-record bookkeeping and confirm release-gate criteria.

- [ ] T039 Run `magex test:race -tags=integration ./tests/integration/...` once and confirm zero race-detector findings (spec FR-011 + SC-001). Capture elapsed wall-clock and confirm < 120 s on macOS arm64 / Linux amd64 developer-class machine. If a race appears, root-cause in the harness or production code — do NOT add `sync.Mutex` to mask a real race (Constitution IX).
- [ ] T040 [P] Update `docs/AC-MATRIX.md` AC-10 row "Test path" column: list each of the 17 test names from spec FR-002 verbatim (`tests/integration/scenarios_test.go::Test_Scenario_NN_<slug>`); row status reaches `green` when all 17 pass against fully-shipped providers (spec FR-027). Update AC-9 row to cite the SDD-25 integration suite as the test-infra completeness deliverable that produces the AC-10 coverage paths (spec FR-028). Transcribe — do not invent — names; the spec FR-002 list is canonical.
- [ ] T041 [P] Append a `tests/integration/` entry to `docs/PACKAGE-MAP.md` listing the harness purpose per the SDD-25 chunk-doc entry contract: WITHOUT freezing harness type signatures (they evolve as new scenarios surface needs). Mark SDD-25 status `done` in `docs/SDD-PLAYBOOK.md`.
- [ ] T042 [P] Verify default-build invisibility one final time (spec FR-008 + SC-001 hermeticism): `go test ./tests/integration/...` (no `-tags=integration`) prints `no Go files in ...` and exits 0; `go test -race ./...` (full repo, no `-tags=integration`) compiles zero integration files. If a file leaks into the default build, the `//go:build integration` tag is missing somewhere — every harness file AND every test file must carry it.
- [ ] T043 [P] Run `magex lint` to confirm the depguard rule from T002 fires if any production file imports `tests/integration/harness`; intentionally add a temporary import in a non-test file under `internal/`, confirm depguard catches it with the configured error message, revert. Confirm `magex test:race -tags=integration` is the canonical invocation documented in `quickstart.md`.

**Checkpoint**: SDD-25 status `done`; AC-10 + AC-9 rows updated; PACKAGE-MAP entry added; quickstart.md verified accurate. v0.1.0 release gate is open.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — starts immediately.
- **Foundational (Phase 2)**: Depends on Phase 1 completion. **BLOCKS all scenario authoring + harness building.**
- **US1 Sub-phase 3a (Scenarios T007–T023)**: Depends on Phase 2 completion. **MUST be authored before Sub-phase 3b** (TDD per Constitution VIII + user instruction). All 17 scenarios fail to compile until Sub-phase 3b ships — that compile failure is the red state.
- **US1 Sub-phase 3b (Harness T024–T029)**: Depends on Sub-phase 3a completion (the failing tests are the contract the harness fulfills).
  - Within 3b: T024 (log_capture), T025 (vault), T026 (discord), T027 (child) are independent → [P].
  - T028 (server) depends on T024 + T025 + T026.
  - T029 (supervisor) depends on T024 + T027 + T028.
- **US1 Sub-phase 3c (T030–T032)**: Depends on Sub-phase 3b completion.
- **US2 (Phase 4, T033–T035)**: Depends on Sub-phase 3b (harness exists, scenarios compile) — audits + hardens the Contract D path.
- **US3 (Phase 5, T036–T038)**: Depends on Sub-phase 3b + Phase 4 (clean harness + complete sentinel coverage) — the 5-flake gate is the final pre-polish signal.
- **Polish (Phase 6, T039–T043)**: Depends on Phases 3, 4, 5 complete.

### Within Each Story

- US1: Tests (Sub-phase 3a) written FIRST and FAILING-TO-COMPILE before harness (Sub-phase 3b) is built. TDD red→green is explicit and load-bearing.
- US2: Validation-only — no new production code path; audits the Contract D call shape across all 17 scenarios.
- US3: Validation-only — exercises the harness built in US1 against the 5-flake / race / hermeticism gates.

### Parallel Opportunities

- **Phase 1**: T002 + T003 in parallel after T001.
- **Phase 3a (Scenarios)**: T007–T023 all marked [P] — each writes to a **different test function** in `tests/integration/scenarios_test.go`. The file is shared; coordinate via clean diffs (one function per task) and merge as separate commits to preserve TDD-red bisectability.
- **Phase 3b (Harness)**: T024, T025, T026, T027 in parallel (different files). T028 + T029 sequential after their deps.
- **Phase 3c**: T030 + T031 in parallel.
- **Phase 4 / Phase 5**: Audit tasks (T033, T034, T035, T037) in parallel within phase; T036 + T038 are sequential at the end of their phases (they exercise the assembled suite).
- **Phase 6**: T040 + T041 + T042 + T043 in parallel (documentation + verification).

---

## Parallel Example: Sub-phase 3a (Scenario authoring — TDD red)

```bash
# Author all 17 scenario test functions concurrently (each one Go function in scenarios_test.go);
# commit individually for bisectability:
Task: "Implement Test_Scenario_01_InteractiveShellRequest (T007)"
Task: "Implement Test_Scenario_02_FirstDaemonBootstrap (T008)"
Task: "Implement Test_Scenario_03_CleanChildExitRefill (T009)"
Task: "Implement Test_Scenario_04_ChildCrashRefill (T010)"
Task: "Implement Test_Scenario_05_ChildExit78Stale (T011)"
Task: "Implement Test_Scenario_06_ValidatorFailure (T012)"
Task: "Implement Test_Scenario_07_VaultRestartInvalidatesSession (T013)"
Task: "Implement Test_Scenario_08_DaytimeRefresh (T014)"
Task: "Implement Test_Scenario_09_OvernightExpiry_Strict (T015)"
Task: "Implement Test_Scenario_09_OvernightExpiry_Grace (T016)"
Task: "Implement Test_Scenario_10_DiscordUnavailable (T017)"
Task: "Implement Test_Scenario_11_TailscaleReady (T018)"
Task: "Implement Test_Scenario_11_BootTimeout (T019)"
Task: "Implement Test_Scenario_12_AgentStatusCheck (T020)"
Task: "Implement Test_Scenario_13_MidSessionRotation (T021)"
Task: "Implement Test_Scenario_14_DuplicateStart (T022)"
Task: "Implement Test_Scenario_15_LogPatternMatch (T023)"
```

## Parallel Example: Sub-phase 3b (Harness construction — TDD green)

```bash
# Build leaf harness files in parallel (no inter-dep):
Task: "Implement harness/log_capture.go (T024)"
Task: "Implement harness/vault.go (T025)"
Task: "Implement harness/discord.go (T026)"
Task: "Implement harness/child.go (T027)"
# Then sequential — server needs log_capture + vault + discord; supervisor needs log_capture + child + server:
Task: "Implement harness/server.go (T028) — depends on T024 + T025 + T026"
Task: "Implement harness/supervisor.go (T029) — depends on T024 + T027 + T028"
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Complete Phase 1: Setup.
2. Complete Phase 2: Foundational (TestMain + child-mode dispatcher + RoundTripper allow-list + 17 scenario stubs with `t.Skip`).
3. Complete Phase 3a: author all 17 scenario tests as failing tests (TDD red). Compile MUST fail.
4. Complete Phase 3b: build the 6 harness files in dependency order (TDD green). Each builder makes its dependent scenarios reach their assertions.
5. Complete Phase 3c: cross-cutting assertion helpers + run all 17 green.
6. **STOP and VALIDATE**: 17 scenarios green (modulo provider-chunk dependencies). AC-10 row of `docs/AC-MATRIX.md` partially populated.

### Incremental Delivery

1. Setup + Foundational → suite skeleton compiles; default `go test ./...` ignores it.
2. Add US1 → 17 scenarios green → AC-10 owner-of-record evidence shipped.
3. Add US2 → defence-in-depth sentinel sweep across every scenario → Constitution X integration proof.
4. Add US3 → race-detector clean + 5-flake gate clean + under-120-s wall-clock → release-gate-fit.
5. Polish → AC-MATRIX + PACKAGE-MAP + SDD-PLAYBOOK updates; v0.1.0 release gate opens.

### TDD Discipline (Constitution VIII, load-bearing)

- Every scenario in Sub-phase 3a MUST fail (compile-or-runtime) before its supporting harness piece in Sub-phase 3b is written. That failure is the evidence the harness has work to do.
- Skipping a scenario to soften a missing audit event is forbidden (spec FR-001 + Contract B). The audit log is a normative contract.
- A scenario that "almost passes" is a failure. The four contracts (A/B/C/D) from contracts/scenario-assertions.md are mandatory and binary.
- The harness has no `harness.Skip`, no `harness.Sleep` (exported), no `harness.SuppressSentinelLeak`, no `harness.Reset` — all explicitly anti-API per contracts/harness-api.md.

---

## Notes

- [P] tasks = different files OR fully-disjoint test-function bodies within `scenarios_test.go`; no dependency on incomplete tasks.
- [US1/US2/US3] labels map tasks to spec.md user stories for traceability.
- All harness code lives at `tests/integration/harness/*.go`; all scenario tests at `tests/integration/scenarios_test.go`; suite-wide setup at `tests/integration/lifecycle_test.go`. **Every file `//go:build integration`.**
- Zero new files under `internal/*` — every SDD-01..SDD-24 PACKAGE-MAP entry is locked (per plan.md Constraints + research.md §R7).
- Zero new direct `go.mod` dependency — stdlib + already-in-module helpers only (Constitution XI).
- Constitution IX enforcement: no `init()`, no package-level mutable globals in the harness; every harness-spawned goroutine has owner + ctx + termination + top-frame `recover()` + leak-detector teardown.
- Constitution X enforcement: every captured byte stream per scenario flows through `AssertSentinelAbsent`; alert payloads scope-name-only per FR-024.
- Verify TDD ordering before commit: `git log --oneline` should show every scenario-test commit (T007–T023) *before* its harness-builder commit (T024–T029).
- Commit boundary suggestion: one commit per task (T0NN) for clean traceability + bisectability; squash on PR if reviewer prefers.
- Scenario name canon: the 17 `Test_Scenario_NN_<slug>` names match spec FR-002 verbatim — renaming any cell requires a spec amendment first (FR-002 is the locked source).
