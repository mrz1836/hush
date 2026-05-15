# Phase 1 Data Model — SDD-25 Lifecycle Integration Harness

The "data model" for an integration-test chunk is the *test-fixture entity model*: the harness types that scenarios compose, plus the canonical Scenario entity that defines the documented contract each `Test_Scenario_NN_<slug>` function must honour.

---

## 1. Scenario (the canonical entity)

A **Scenario** is one row of the 17-row table locked in [`spec.md` FR-002](spec.md). It is identified by a number (01..15), a slug, a source diagram in [`docs/LIFECYCLE-SCENARIOS.md`](../../docs/LIFECYCLE-SCENARIOS.md), and the four documented contracts a passing test must demonstrate. Scenarios 9 and 11 each split into two test functions (spec FR-003) — total = 17 functions.

| Field | Type | Origin | Validation |
|-------|------|--------|------------|
| `Number` | `int` (01..15) | spec FR-002 | exactly 15 distinct values; 09 and 11 each have two variants |
| `Slug` | `string` (PascalCase) | spec FR-002 | matches `Test_Scenario_<NN>_<Slug>` symbol name verbatim |
| `Source` | doc reference (`docs/LIFECYCLE-SCENARIOS.md` §N) | spec FR-001 | every scenario has a §N anchor in the source doc |
| `Type` | enum (`Interactive`, `Supervisor`) | spec FR-001 | drives whether the status-socket assertion (FR-006) is required |
| `DocumentedFinalState` | `supervise.State` OR a compound assertion (Scenario 1) | spec FR-004 | one of `fetching`, `running`, `awaiting-approval`, `grace-restart`, `stopped` OR Scenario-1 compound shape |
| `DocumentedAuditEvents` | `[]audit.Action` (in order) | spec FR-005 | every name appears in [`internal/audit/chain.go`](../../internal/audit/chain.go#L34-L81) AND in [`docs/SPEC.md`](../../docs/SPEC.md) §FR-14 |
| `DocumentedStatusProjection` | partial `statusJSON` DTO (state name, scope_healthy, scope_stale, discord_connected, etc.) | spec FR-006 | required for every supervisor scenario; absent for interactive-only |
| `SentinelInjectionPoint` | one or more `Scope[i] := testutil.SentinelSecret(N)` | spec FR-007 + FR-017 | at least one scope per scenario carries the marker |
| `MockedBoundaries` | subset of {Discord, Anthropic, OpenAI, GitHub, Google AI, Clock, TailscaleProbe} | spec FR-012..FR-015 | every other component is real |

**State transitions** (Scenario-level): scenarios do not transition; they are the *unit of assertion*. The supervisor state machine transitions are owned by the `internal/supervise.Store` (locked at SDD-19). The harness asserts the *final* state per scenario (spec FR-004 "test boundary" clause: scenarios stop at the operator-blocked terminal state; Scenario 13 owns the full recovery path), not intermediate transitions (those are unit-test concerns).

---

## 2. The 17 Scenario test functions — final-state + audit-events catalogue

This catalogue is normative. Each scenario's harness builder consumes the rows below to produce the four mandatory assertions. The `Test name` column matches spec FR-002's locked list **verbatim**; renaming any cell here is a spec amendment.

| # | Test name | Type | Final state | Documented audit events (in documented order) | Status-socket required |
|---|-----------|------|-------------|-----------------------------------------------|------------------------|
| 01 | `Test_Scenario_01_InteractiveShellRequest` | Interactive | Compound (see §3) | `session_requested`, `session_approved`, `secret_retrieved` (×N for N scopes) | No |
| 02 | `Test_Scenario_02_FirstDaemonBootstrap` | Supervisor | `running` | `session_requested`, `session_approved`, `supervisor_session_claimed`, `secret_retrieved` (×N) | Yes |
| 03 | `Test_Scenario_03_CleanChildExitRefill` | Supervisor | `running` | `supervisor_child_clean_exit`, `supervisor_silent_refill`, `secret_retrieved` (×N) | Yes |
| 04 | `Test_Scenario_04_ChildCrashRefill` | Supervisor | `running` | `supervisor_child_exit_crash`, `supervisor_silent_refill`, `secret_retrieved` (×N) | Yes |
| 05 | `Test_Scenario_05_ChildExit78Stale` | Supervisor | `awaiting-approval` | `supervisor_child_exit_78`, `supervisor_awaiting_approval`, `supervisor_stale_alert` | Yes |
| 06 | `Test_Scenario_06_ValidatorFailure` | Supervisor | `awaiting-approval` | `supervisor_stale_alert`, `supervisor_awaiting_approval` | Yes |
| 07 | `Test_Scenario_07_VaultRestartInvalidatesSession` | Supervisor | `awaiting-approval` | `supervisor_stale_alert`, `supervisor_awaiting_approval` (spec FR-004 boundary: test stops here; recovery is Scenario 13) | Yes |
| 08 | `Test_Scenario_08_DaytimeRefresh` | Supervisor | `running` (child uninterrupted; spec FR-025) | `session_requested`, `session_approved`, `supervisor_session_refreshed` | Yes |
| 09a | `Test_Scenario_09_OvernightExpiry_Strict` | Supervisor | `awaiting-approval` | `supervisor_child_exit_crash`, `supervisor_awaiting_approval`, `supervisor_stale_alert` | Yes |
| 09b | `Test_Scenario_09_OvernightExpiry_Grace` | Supervisor | `running` (via `grace-restart` sub-state) | `supervisor_child_exit_crash`, `supervisor_grace_entered`, `supervisor_silent_refill`, `supervisor_grace_exited` | Yes |
| 10 | `Test_Scenario_10_DiscordUnavailable` | Supervisor | `stopped` (boot fails — `/claim` returns 503 with `error=="discord_unavailable"`; orchestrator retries within boot budget, then emits `AlertClassDiscordUnavailableOnClaim` and `ErrBootTimeout`) | `supervisor_stale_alert`, `supervisor_boot_timeout` | Yes (boot transient state visible) |
| 11a | `Test_Scenario_11_TailscaleReady` | Supervisor | `running` after Tailscale-probe success on retry N | `supervisor_session_claimed`, `supervisor_silent_refill` | Yes |
| 11b | `Test_Scenario_11_BootTimeout` | Supervisor | `stopped` + `AlertClassBootTimeout` | `supervisor_boot_timeout` | Yes (transient `boot-retry` state visible) |
| 12 | `Test_Scenario_12_AgentStatusCheck` | Supervisor | `running` | (no unique audit event — this scenario IS the status-socket assertion) | Yes — this IS the assertion |
| 13 | `Test_Scenario_13_MidSessionRotation` | Supervisor | `running` (post-restart with new secret) | `vault_reloaded`, `client_refresh_invoked`, `supervisor_silent_refill`, `secret_retrieved` | Yes |
| 14 | `Test_Scenario_14_DuplicateStart` | Supervisor | first supervisor: `running`; second: refused at boot (pidfile collision → `ErrPidFileLocked` propagated to `Lifecycle.Run` caller) | `supervisor_session_claimed` (first instance only) | Yes (first supervisor only) |
| 15 | `Test_Scenario_15_LogPatternMatch` | Supervisor | `running` (alert-only — Constitution V; no state change) | `supervisor_stale_alert` | Yes |

The "Status-socket required" column drives FR-006 enforcement: scenarios marked `Yes` MUST assert against the FR-12 JSON shape (recap of fields in `contracts/scenario-assertions.md` §C).

---

## 3. Scenario 1 compound final-state

[`docs/LIFECYCLE-SCENARIOS.md` §1](../../docs/LIFECYCLE-SCENARIOS.md) defines Scenario 1's "final state" as a four-tuple compound:

| Sub-assertion | Source | Helper |
|---------------|--------|--------|
| (a) `vault_loaded=true` AND `discord_connected=true` via `/h/<prefix>/hz` | spec FR-004 + Scenario-1 step 4 | `harness.GetHealth(t, server)` returns the JSON shape; assert both flags |
| (b) wrapped child shell exited with documented exit code | spec FR-004 + Scenario-1 step 10 | `child.Wait()` returns documented exit code |
| (c) JWT use-count consumed OR expired in server's token store | spec FR-004 + Scenario-1 step 12 | `harness.ReadTokenStore(t, server).Get(jti)` returns matching state (max-uses=0 or expired) |
| (d) approval DM count == 1 | spec FR-004 + LIFECYCLE-SCENARIOS §1 "approval is required exactly once" | `len(harness.Discord.Calls()) == 1` |

---

## 4. Harness types (test-fixture entity model)

These types live in `tests/integration/harness/*.go`; their signatures evolve as scenarios surface needs (per the SDD-25 PACKAGE-MAP entry contract). The *contract* — the surface scenarios depend on — is captured in [`contracts/harness-api.md`](contracts/harness-api.md).

### 4.1 `harness.TestVault`

Owns: vault file path, vault encryption key (`*securebytes.SecureBytes`), state-dir path, clients.json registry path, audit log path.

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewVault(t, secrets map[string]string) *TestVault` | `*TestVault` | constructs via `testutil.NewTestVault`; injects scenario sentinels |
| `Path() string` | absolute vault path | for passing into `internal/server.Deps` |
| `Key() *securebytes.SecureBytes` | vault key handle | for SIGHUP reload tests |
| `AuditPath() string` | absolute audit JSONL path | for `audit.Verify` + raw-bytes sentinel sweep |
| `RegisterClient(machineIndex uint32, pubKey *ecdsa.PublicKey)` | — | writes `clients.json` so the server can verify `/claim` signatures |
| `Rotate(name string, newValue string) error` | — | atomic rewrite + SIGHUP signal seam for Scenario 13 |

### 4.2 `harness.TestServer`

Owns: real `*server.Server`, loopback TCP listener URL, per-validator-upstream `httptest.Server`, the `stubAsApprover` adapter that bridges DiscordStub → `server.Approver`.

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewServer(t, opts ServerOpts) *TestServer` | `*TestServer` | builds Deps; wires real `internal/server`; starts a `net.Listen("tcp","127.0.0.1:0")` listener |
| `URL() string` | `http://127.0.0.1:PORT/h/<prefix>` | for `Supervisor` to point at |
| `MockValidator(name string, h http.HandlerFunc)` | — | programs the per-validator upstream response (forward-compatible with SDD-26) |
| `ReadAudit() []audit.Event` | parsed JSONL | for `AssertAuditSubsequence` |
| `RawAudit() []byte` | raw JSONL bytes | for sentinel-absence sweep |
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
| `Alerts() []AlertPayload` | recorded supervise-side alerts | for FR-023/024 assertion (scope-only, no value) |
| `AlertsRaw() []byte` | every alert payload concatenated as bytes | for sentinel-absence sweep |

### 4.4 `harness.TestSupervisor`

Owns: real `*supervise.Lifecycle` via `supervise.NewLifecycle`; the harness wires Deps (logger from `LogCapture`, fake clock, signing keys, audit writer, pidfile, validators, alerts handle, watchdog, TailscaleProbe, VaultHzProbe).

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewSupervisor(t, opts SupervisorOpts) *TestSupervisor` | `*TestSupervisor` | composes `Deps` against the harness's server URL and starts the lifecycle in a goroutine |
| `Clock() *FakeClock` | clock handle | scenarios call `Clock().Advance(d)` / `Clock().SetTo(t)` to drive transitions |
| `TriggerRefresh(ctx) error` | — | invokes the refill callback directly (research.md §2 — Refresher.Run is NOT used by the harness; the harness drives refill at scripted moments) |
| `Status() StatusDoc` | parsed `statusJSON` | FR-006 assertion source |
| `StatusRaw() []byte` | raw socket bytes | for sentinel-absence assertion |
| `Refresh(ctx) error` | — | hits `refresh\n` verb on socket (Scenario 13) |
| `WaitState(ctx, state supervise.State, deadline time.Duration) error` | — | bounded poll (no `time.Sleep`; uses `runtime.Gosched` + ctx) |
| `AssertAuditSubsequence(t, documented []string)` | — | the subsequence-check helper (per-scenario row from §2) |
| `AssertNoGoroutineLeak(t)` | — | called by `t.Cleanup`; uses pre/post `runtime.NumGoroutine` snapshot |
| `Stop()` | — | cancels the run context, waits for `Lifecycle.Run` to return, closes every socket |

### 4.5 `harness.TestChild`

Owns: programmable child process; exit-code, lifetime, stderr-pattern emission scriptable per scenario. Built via `os.Executable()` re-invocation (research.md §6).

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewChild(t, opts ChildOpts) *TestChild` | `*TestChild` | builds `supervise.ChildConfig` pointing at `os.Executable()` with integration-mode argv |
| `Cmd() *supervise.Child` | underlying handle | what the supervisor consumes |
| `ScriptExitCode(code int)` | — | the exit code the scripted child will return on `--lifetime` expiry |
| `EmitStderr(pattern string)` | — | for Scenario 15 (log-pattern watchdog) |
| `Stdout() []byte` / `Stderr() []byte` | captured streams | for sentinel-absence sweep |

### 4.6 `harness.LogCapture`

Owns: a `slog.Handler` chain capturing every record to a `sync.Mutex`-guarded buffer; the `AssertSentinelAbsent` cross-stream helper.

| Method | Returns | Purpose |
|--------|---------|---------|
| `NewLogCapture(t) *LogCapture` | `*LogCapture` | constructs a per-scenario capturing logger |
| `Logger() *slog.Logger` | logger | hand to `server.Deps.Logger` / `supervise.Deps.Logger` |
| `Bytes() []byte` | captured records | input to `AssertSentinelAbsent` |
| `AssertSentinelAbsent(t, streams ...[]byte)` | — | the canonical multi-stream assertion |

The canonical cross-stream coverage list for `AssertSentinelAbsent` (spec FR-007):

1. `LogCapture.Bytes()` — operational `slog` output
2. `TestServer.RawAudit()` JSONL bytes
3. `TestSupervisor.StatusRaw()` raw socket bytes
4. `TestDiscord.AlertsRaw()` payload strings
5. `TestChild.Stdout()` + `TestChild.Stderr()` buffers
6. Every `error.Error()` string surfaced by the scenario (collected via `harness.CollectErrors`)

The scenario calls `harness.AssertSentinelAbsent(t, …)` once at the end, passing every byte stream it produced.

---

## 5. Suite topology

| Entity | Cardinality per scenario |
|--------|--------------------------|
| `TestVault` | exactly 1 |
| `TestServer` | exactly 1 |
| `TestDiscord` | exactly 1 |
| `TestSupervisor` | 0 (interactive-only Scenario 1) or 1 (every other scenario) or 2 (Scenario 14 — first owns the pidfile, second is refused at boot) |
| `TestChild` | 0 (Scenarios 6, 9a, 10, 11b — child never starts) or 1 |
| `LogCapture` | exactly 1 |

Lifetime: every entity is constructed at scenario start, registers `t.Cleanup` for teardown, and is shut down before the test function returns. Spec FR-020 + FR-021 are enforced by the harness; scenarios never call `Stop()` directly.
