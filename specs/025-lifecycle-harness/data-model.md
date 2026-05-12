# Phase 1 Data Model — SDD-25 Lifecycle Integration Harness

The "data model" for an integration-test chunk is the *test-fixture entity model*: the harness types that scenarios compose, plus the canonical Scenario entity that defines the documented contract each `Test_Scenario_NN_<slug>` function must honour.

---

## 1. Scenario (the canonical entity)

A **Scenario** is one row of the 15-row table in `specs/025-lifecycle-harness/spec.md` FR-025-29. It is identified by a number (01..15), a slug, a source diagram in `docs/LIFECYCLE-SCENARIOS.md`, and the four documented contracts a passing test must demonstrate.

| Field | Type | Origin | Validation |
|-------|------|--------|------------|
| `Number` | `int` (01..15) | spec FR-025-3..FR-025-5a | exactly 15 distinct values; 09 has two variants (09a/09b) and 10 has two subtests (Interactive/Supervisor) |
| `Slug` | `string` (PascalCase) | spec FR-025-4 | matches `Test_Scenario_<NN>_<Slug>` symbol name verbatim |
| `Source` | doc reference (`docs/LIFECYCLE-SCENARIOS.md` §N) | spec FR-025-3 | every scenario has a §N anchor in the source doc |
| `Type` | enum (`Interactive`, `Supervisor`, `Mixed`) | spec table | drives whether the status-socket assertion (FR-025-8) is required |
| `DocumentedFinalState` | `supervise.State` OR a compound assertion (Scenario 1) | spec FR-025-6 + Clarification 4 | one of `fetching`, `running`, `awaiting-approval`, `grace-restart`, `stopped` OR Scenario-1 compound shape |
| `DocumentedAuditEvents` | `[]audit.AuditEventType` (in order) | spec FR-025-7 + Clarification 1 | every name appears in `SPEC.md §FR-14` |
| `DocumentedStatusProjection` | partial `statusJSON` DTO (state name, scope_healthy, scope_stale, discord_connected, etc.) | spec FR-025-8 | required for every supervisor scenario; absent for interactive-only |
| `SentinelInjectionPoint` | one or more `Scope[i] := testutil.SentinelSecret(N)` | spec FR-025-25 | at least one scope per scenario carries the marker |
| `MockedBoundaries` | subset of {Discord, Anthropic, OpenAI, GitHub, Google AI, NTP, Clock} | spec FR-025-12 | every other component is real |

**State transitions** (Scenario-level): scenarios do not transition; they are the *unit of assertion*. The supervisor state machine transitions are owned by the `internal/supervise.Store` (locked at SDD-19). The harness asserts the *final* state per scenario, not intermediate transitions (those are unit-test concerns).

---

## 2. The 15 Scenarios — final-state + audit-events catalogue

This catalogue is normative. Each scenario’s harness builder consumes the rows below to produce the four mandatory assertions.

| # | Test name | Final state | Documented audit events (in order) | Status-socket required |
|---|-----------|-------------|------------------------------------|------------------------|
| 01 | `Test_Scenario_01_FirstInteractive` | Compound (see §3) | `session_requested`, `session_approved`, `secret_fetched` (×N for N scopes) | No |
| 02 | `Test_Scenario_02_DaemonBootstrap` | `running` | `session_requested`, `session_approved`, `supervisor_session_claimed`, `secret_fetched` (×N) | Yes |
| 03 | `Test_Scenario_03_CleanExitSilentRefill` | `running` | `supervisor_child_clean_exit`, `supervisor_silent_refill`, `secret_fetched` (×N) | Yes |
| 04 | `Test_Scenario_04_ChildCrashSilentRefill` | `running` | `supervisor_silent_refill`, `secret_fetched` (×N) | Yes |
| 05 | `Test_Scenario_05_Exit78StaleCreds` | `awaiting-approval` | `supervisor_child_exit_78`, `supervisor_awaiting_approval`, `supervisor_stale_alert` | Yes |
| 06 | `Test_Scenario_06_ValidatorBlocksChild` | `awaiting-approval` | `supervisor_stale_alert`, `supervisor_awaiting_approval` | Yes |
| 07 | `Test_Scenario_07_VaultRestart` | `awaiting-approval` then `running` after re-approval | `auth_failed`, `supervisor_awaiting_approval`, `session_requested`, `session_approved`, `supervisor_session_refreshed` | Yes |
| 08 | `Test_Scenario_08_DaytimeRefresh` | `running` (child uninterrupted) | `supervisor_session_refreshed`, `session_approved` | Yes |
| 09a | `Test_Scenario_09_OvernightExpiry_Strict` | `awaiting-approval` | `token_expired`, `supervisor_awaiting_approval`, `supervisor_stale_alert` | Yes |
| 09b | `Test_Scenario_09_OvernightExpiry_Grace` | `running` (via `grace-restart` sub-state) | `token_expired`, `supervisor_grace_entered`, `supervisor_silent_refill`, `supervisor_grace_exited` | Yes |
| 10 | `Test_Scenario_10_DiscordUnavailable` (parent) — `Interactive` + `Supervisor` subtests | server returns 503; caller surfaces failure | `discord_disconnected`, `auth_failed` (one per subtest) | Supervisor subtest: yes; Interactive subtest: no |
| 11 | `Test_Scenario_11_TailscaleBootRetry` | `running` after retry succeeds | `supervisor_session_claimed` (after delayed first attempt) | Yes (after recovery) |
| 12 | `Test_Scenario_12_StatusGate` | `running` | (none unique — this scenario IS the status-socket assertion) | Yes — this IS the assertion |
| 13 | `Test_Scenario_13_RotationMidSession` | `running` (post-restart with new secret) | `vault_reloaded`, `client_refresh_invoked`, `supervisor_silent_refill`, `secret_fetched` | Yes |
| 14 | `Test_Scenario_14_DuplicateSupervisor` | first supervisor: `running`; second: refused at boot | `supervisor_session_claimed` (first instance only) | Yes (first supervisor only) |
| 15 | `Test_Scenario_15_LogPatternAlert` | `running` (alert-only — no state change) | `supervisor_stale_alert` (kind=`LogPatternMatch`) | Yes |

The status-socket required column drives FR-025-8 enforcement: scenarios marked `Yes` MUST assert against the FR-12 JSON shape.

---

## 3. Scenario 1 compound final-state (Clarification 4)

Spec Clarification 4 resolves Scenario 1's "final state" to a four-tuple compound:

| Sub-assertion | Source | Helper |
|---------------|--------|--------|
| (a) `vault_loaded=true` AND `discord_connected=true` via `/health` | spec FR-025-6 + Clarification 4 | `harness.GetHealth(t, server)` returns the locked JSON shape; assert both flags |
| (b) wrapped child shell exited with documented exit code | spec FR-025-6 + Clarification 4 | `child.Wait()` returns documented exit code |
| (c) JWT use-count consumed OR expired in server's token store | spec FR-025-6 + Clarification 4 | `harness.ReadTokenStore(t, server).Get(jti)` returns matching state (max-uses=0 or expired) |
| (d) approval DM count == 1 | spec FR-025-6 + Clarification 4 + LIFECYCLE-SCENARIOS §1 | `len(harness.Discord.Calls()) == 1` |

---

## 4. Harness types (test-fixture entity model)

These types live in `tests/integration/harness/*.go`; their signatures evolve as scenarios surface needs (per the SDD-25 PACKAGE-MAP entry contract). The *contract* — the surface scenarios depend on — is captured in [contracts/harness-api.md](contracts/harness-api.md).

### 4.1 `harness.TestVault`

Owns: vault file path, vault encryption key (`*securebytes.SecureBytes`), state-dir path, clients.json registry path.

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewVault(t, secrets map[string]string) *TestVault` | `*TestVault` | constructs via `testutil.NewTestVault`; injects scenario sentinels |
| `Path() string` | absolute vault path | for passing into `internal/server.Deps` |
| `Key() *securebytes.SecureBytes` | vault key handle | for SIGHUP reload tests |
| `RegisterClient(machineIndex uint32, pubKey *ecdsa.PublicKey)` | — | writes `clients.json` so the server can verify `/claim` signatures |
| `Rotate(name string, newValue string) error` | — | atomic rewrite + SIGHUP signal seam for Scenario 13 |

### 4.2 `harness.TestServer`

Owns: real `*server.Server`, `httptest.Server`-style listener URL, per-validator-upstream `httptest.Server`, the `stubAsApprover` adapter that bridges DiscordStub → `server.Approver`.

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewServer(t, opts ServerOpts) *TestServer` | `*TestServer` | builds Deps; wires real `internal/server`; starts httptest listener |
| `URL() string` | `http://127.0.0.1:PORT/h/<prefix>` | for `Supervisor` to point at |
| `SetClockSyncProbe(probe server.ClockSyncProbe)` | — | for clock-sync failure scenarios (none in SDD-25 — kept for future) |
| `MockValidator(name string, h http.HandlerFunc)` | — | programs the per-validator upstream response |
| `ReadAudit() []audit.Event` | parsed JSONL | for `AssertAuditSubsequence` |
| `TokenStore() token.Store` | live token store | for Scenario 1 sub-assertion (c) |
| `Health() HealthDoc` | the `/hz` JSON | for Scenario 1 sub-assertion (a) |
| `Reload() error` | — | trigger SIGHUP-equivalent for Scenario 13 |
| `Stop()` | — | graceful shutdown; called by `t.Cleanup` |

### 4.3 `harness.TestDiscord`

Owns: `*testutil.DiscordStub`, the alert-payload recorder (slice of structured alerts), the connectivity-sequence driver (available/unavailable/reconnected).

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewDiscord(t) *TestDiscord` | `*TestDiscord` | constructs DiscordStub with `t.Cleanup` |
| `Stub() *testutil.DiscordStub` | embedded stub | direct access for `Enqueue` |
| `SetConnected(b bool)` | — | drives Scenario 10's connectivity-sequence |
| `SetRateLimit(n int, per time.Duration)` | — | drives rate-limit sequence (Scenario 15 alert throttling) |
| `Alerts() []AlertPayload` | recorded alerts | for FR-025-27 assertion (scope-only, no value) |

### 4.4 `harness.TestSupervisor`

Owns: real `*supervise.Store`, `*supervise.Refiller`, `*supervise.Refresher`, `*supervise.Grace`, `*supervise.StatusServer`, `*supervise.PidFile`, controllable `Clock`, audit Writer, goroutine snapshot, status-socket client, ECIES ephemeral key.

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewSupervisor(t, opts SupervisorOpts) *TestSupervisor` | `*TestSupervisor` | composes the SDD-19..22 primitives against the harness’s server URL |
| `Clock() *FakeClock` | clock handle | scenarios call `Clock().Advance(d)` to drive transitions |
| `Refill(ctx) error` | — | scenarios invoke directly to test silent-refill paths |
| `TriggerRefresh(ctx) error` | — | scenarios invoke for Scenario 8 / 13 |
| `Status() StatusDoc` | parsed `statusJSON` | FR-025-8 assertion source |
| `StatusRaw() []byte` | raw socket bytes | for sentinel-absence assertion (FR-025-26) |
| `Refresh(ctx) error` | — | hits `refresh\n` verb on socket (Scenario 13) |
| `WaitState(ctx, state supervise.State, deadline time.Duration) error` | — | bounded poll (no `time.Sleep`; uses `runtime.Gosched` + ctx) |
| `AssertAuditSubsequence(t, documented []string)` | — | the subsequence-check helper |
| `AssertNoGoroutineLeak(t)` | — | called by `t.Cleanup`; uses pre/post `runtime.NumGoroutine` snapshot |
| `Stop()` | — | tears down every goroutine + closes every socket |

### 4.5 `harness.TestChild`

Owns: programmable child process; exit-code, lifetime, stderr-pattern emission scriptable per scenario.

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewChild(t, opts ChildOpts) *TestChild` | `*TestChild` | builds `supervise.ChildConfig` pointing at `os.Executable()` with integration-mode argv |
| `Cmd() *supervise.Child` | underlying handle | what the supervisor consumes |
| `ExitCode() int` | — | the exit code the scripted child will return |
| `EmitStderr(pattern string)` | — | for Scenario 15 (log-pattern watchdog) |

### 4.6 `harness.LogCapture`

Owns: a `slog.Handler` chain capturing every record to a `sync.Buffer`; the `AssertSentinelAbsent` cross-stream helper.

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewLogCapture(t) *LogCapture` | `*LogCapture` | replaces default logger; registers cleanup |
| `Logger() *slog.Logger` | logger | hand to `server.Deps.Logger` / `supervisor.Logger` |
| `Bytes() []byte` | captured records | input to `AssertSentinelAbsent` |
| `AssertSentinelAbsent(t, streams ...[]byte)` | — | the canonical multi-stream assertion |

The canonical cross-stream coverage list for `AssertSentinelAbsent` (FR-025-26):

1. `LogCapture.Bytes()` — operational `slog` output
2. `TestServer.ReadAudit()` JSONL bytes
3. `TestSupervisor.StatusRaw()` raw socket bytes
4. `TestDiscord.Alerts()` payload strings
5. `TestChild` captured `stdout` + `stderr` buffers
6. Every `error.Error()` string surfaced by the scenario

The scenario calls `harness.AssertSentinelAbsent(t, …)` once at the end, passing every byte stream it produced.

---

## 5. Suite topology

| Entity | Cardinality per scenario |
|--------|--------------------------|
| `TestVault` | exactly 1 |
| `TestServer` | exactly 1 |
| `TestDiscord` | exactly 1 |
| `TestSupervisor` | 0 (interactive-only Scenario 1, Scenario 10/Interactive subtest) or 1 (every other scenario) or 2 (Scenario 14 — first owns the pidfile, second is refused at boot) |
| `TestChild` | 0 (Scenarios 6, 9a, 10/Interactive, 14 second supervisor — child never starts) or 1 |
| `LogCapture` | exactly 1 |

Lifetime: every entity is constructed at scenario start, registers `t.Cleanup` for teardown, and is shut down before the test function returns. FR-025-19 + FR-025-20 are enforced by the harness; scenarios never call `Stop()` directly.
