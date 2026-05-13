# Feature Specification: Lifecycle Integration Harness (SDD-25)

**Feature Branch**: `025-lifecycle-harness`
**Created**: 2026-05-12
**Status**: Draft
**Input**: User description: "tests/integration: end-to-end harness running real internal/* packages with mocked external services; implements all 15 lifecycle scenarios from docs/LIFECYCLE-SCENARIOS.md, each asserting final state + audit ordering + status socket shape + no sentinel leak; build-tagged //go:build integration; suite under 120s, no flake on 5 runs"

## Overview

SDD-25 is the integration test suite that proves Acceptance Criterion AC-10 ("Supervisor lifecycle — 15 named scenarios") from [`docs/SPEC.md`](../../docs/SPEC.md). It runs the **real** supervisor, server, audit, token, vault, and Discord-bot packages in-process and drives them through the fifteen named operational paths documented in [`docs/LIFECYCLE-SCENARIOS.md`](../../docs/LIFECYCLE-SCENARIOS.md). Every other chunk in v0.1.0 owns unit and fuzz tests; **this is the only chunk that proves the system works as a whole.**

The suite mocks exactly four external boundaries — Discord, the five provider validator endpoints (Anthropic, Anthropic-OAuth, OpenAI, Google AI, GitHub), the wall clock, and (for boot-retry scenarios) the Tailscale-reachability probe. Nothing else is mocked: real `internal/supervise`, `internal/server`, `internal/audit`, `internal/token`, `internal/vault`, `internal/transport/ecies`, `internal/transport/sign`, and `internal/keys` packages execute end-to-end.

The deliverable is the explicit owner-of-record for AC-10 in [`docs/AC-MATRIX.md`](../../docs/AC-MATRIX.md). Until the suite is green, the v0.1.0 release gate is closed.

## Clarifications

### Session 2026-05-12

- Q: When a scenario documents a full flow including operator recovery (e.g., Scenario 5: stale alert → `awaiting-approval` → operator approves → supervisor refetches → child restarts), where does the test boundary fall? → A: Test asserts the operator-blocked terminal state and alert emission; recovery to `running` is out of scope for these scenarios. Scenario 13 (`hush client refresh`) owns the full refresh-then-resume path.
- Q: Should Scenario 11 ("Tailscale not ready at boot") be split into two test functions like Scenario 9? → A: Yes — split into `Test_Scenario_11_TailscaleReady` (success branch, final state `running`) and `Test_Scenario_11_BootTimeout` (timeout branch, final state `stopped` + `AlertClassBootTimeout`). Total scenario test functions = 17 (15 + Scenario 9 split + Scenario 11 split).
- Q: Where should the canonical list of 17 `Test_Scenario_NN_<slug>` names live so plan/tasks/implement phases all reference one authoritative source? → A: Pin all 17 names in this spec under FR-002. Plan/tasks reference the spec; AC-MATRIX update at implement-phase transcribes (not invents) the names.
- Q: The LIFECYCLE-SCENARIOS "Required alert classes" section lists "Discord disconnected" and "Discord reconnected" but no documented scenario exercises a connection-state transition. Where do those alert classes get coverage? → A: Out of scope for SDD-25. The disconnect/reconnect signals originate in the Discord bot's connection-monitor loop (SDD-11) and have their own unit-test coverage there; CLAUDE.md confirms `ActionDiscordDisconnected`/`ActionDiscordReconnected` are emitted by code paths other than the supervisor orchestrator. Documented in Out of Scope.
- Q: Where does the concrete `scenario → ordered audit events` table live (FR-005 says "documented order" but neither LIFECYCLE-SCENARIOS nor SPEC §FR-14 contains a per-scenario events list)? → A: Defer to plan-phase. plan.md produces a 17-row scenario-to-ordered-events table derived from the §FR-14 vocabulary and the scenario flows; spec FR-005 stays as-is (vocabulary source + ordering-strict semantics).

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Prove all 15 documented lifecycle paths end-to-end (Priority: P1)

The project owner ("the operator") and any future maintainer must be able to run a single command and learn whether the supervisor lifecycle behaves as the design documents claim. The 15 scenarios in [`docs/LIFECYCLE-SCENARIOS.md`](../../docs/LIFECYCLE-SCENARIOS.md) collectively cover first-boot, silent refill on clean exit and crash, exit-78 stale-credentials contract, validator-blocked startup, vault session invalidation, daytime refresh, overnight expiry (with and without grace cache), Discord unavailability, Tailscale boot retry, agent status gating, mid-session rotation, duplicate-start refusal, and log-pattern watchdog alerts. Each scenario is a separate, named test function so a failure points unambiguously at one documented behaviour.

**Why this priority**: This is the AC-10 release-gate deliverable. Without it, v0.1.0 cannot ship. Every other chunk produces unit/fuzz tests that cover individual packages; only this suite proves the end-to-end behaviour the design documents promise.

**Independent Test**: Run `magex test:race -tags=integration ./tests/integration/...`. All 17 named test functions pass (15 scenarios with Scenarios 9 and 11 split into Strict/Grace and TailscaleReady/BootTimeout respectively). Each test's failure message points at exactly one scenario in `docs/LIFECYCLE-SCENARIOS.md`.

**Acceptance Scenarios**:

1. **Given** a clean test environment with a fixture vault containing sentinel-tagged secrets, **When** `magex test:race -tags=integration ./tests/integration/...` is run, **Then** all 17 named test functions pass: `Test_Scenario_01_<slug>` … `Test_Scenario_15_<slug>` for the 13 single-scenario tests, plus `Test_Scenario_09_OvernightExpiry_Strict` and `Test_Scenario_09_OvernightExpiry_Grace` (Scenario 9 split), plus `Test_Scenario_11_TailscaleReady` and `Test_Scenario_11_BootTimeout` (Scenario 11 split). All 17 must pass for the suite to be considered green.
2. **Given** a supervisor scenario (Scenarios 2–15 except 1), **When** the scenario completes, **Then** the supervisor's local status socket returns JSON conforming to the shape documented in [`docs/SPEC.md`](../../docs/SPEC.md) §FR-12 with the specific field values that scenario expects (e.g., `state=="running"` for Scenario 2, `state=="awaiting-approval"` for Scenario 5).
3. **Given** any scenario, **When** the scenario completes, **Then** the on-disk audit log (the hash-chained JSONL the audit chain writes) contains the documented audit events for that scenario in the documented order, with no extra events from a different scenario's flow.
4. **Given** a scenario that exercises a supervisor lifecycle (Scenarios 2–15 except 1), **When** the supervisor's terminal state is read after the scenario's driver completes, **Then** the supervisor's state-machine state matches the final state documented in `docs/LIFECYCLE-SCENARIOS.md` for that scenario (e.g., Scenario 3 ends at `running`; Scenario 5 ends at `awaiting-approval`).

---

### User Story 2 — No sentinel secret ever appears in captured output (Priority: P1)

Every scenario runs with a fixture vault whose secrets contain a known sentinel byte sequence (e.g. `SECRET_SHOULD_NEVER_APPEAR_<scope>`). The suite captures every byte written to any logger, every audit-log record, every status-socket response, and every error returned from any boundary during the scenario. None of those captured streams may contain the sentinel. This is the primary defence-in-depth assertion that proves Constitution Principle X ("Observability & Redaction") holds end-to-end.

**Why this priority**: A secrets broker that leaks secrets into logs is worse than no broker. Unit-level redaction tests prove the leaf path; this integration-level assertion proves no compose-time logging path resurrects a plaintext byte.

**Independent Test**: Run any one scenario in isolation with a sentinel-tagged fixture vault. Inspect every captured byte stream (operational log, audit JSONL, status socket response, error strings) for the sentinel substring. The suite's `AssertSentinelAbsent` helper fails the test if any stream contains the sentinel.

**Acceptance Scenarios**:

1. **Given** a fixture vault whose secrets each begin with a unique sentinel prefix, **When** any of the 15 scenarios runs, **Then** the test asserts via `AssertSentinelAbsent` over the operational log capture, the audit log file content, the status socket response body (when applicable), and the captured stderr of any spawned child process; the assertion passes for all 15.
2. **Given** a scenario that exercises an error path (e.g. Scenario 6 validator failure, Scenario 10 Discord 503), **When** the error propagates to the operator-facing surface, **Then** the error message and any associated alert payload contain only scope names, JTIs, and error classes — never the secret value or any substring of it.

---

### User Story 3 — Release-gate fitness: fast, hermetic, deterministic (Priority: P1)

For the suite to function as a release gate, it must be (a) **fast** — under 120 s wall-clock on a developer laptop so it can run on every commit without becoming friction; (b) **hermetic** — zero outbound network calls, so it runs identically on a developer laptop, in CI, and on a flight with no wifi; and (c) **deterministic** — five consecutive invocations produce five consecutive passes with no flake. All three properties are non-negotiable, because a flaky or slow or network-dependent gate gets disabled and stops protecting the product.

**Why this priority**: A release gate that flakes once a week trains the operator to re-run it without reading the failure. A release gate that takes ten minutes is not run on every commit. A release gate that hits Discord or a provider endpoint fails in CI behind a proxy. Each property compounds the others.

**Independent Test**: From a freshly checked-out branch on a stock developer-class machine, run `for i in 1 2 3 4 5; do time magex test:race -tags=integration ./tests/integration/... || break; done`. The loop must complete five times with zero failures and every individual run must take under 120 s wall-clock.

**Acceptance Scenarios**:

1. **Given** a developer-class machine (M-series Mac or modern Linux x86_64 with ≥8 cores) with the project's standard `magex` tooling installed, **When** the integration suite is run, **Then** wall-clock time is under 120 s.
2. **Given** the same machine with all outbound network blocked except loopback and the project's Tailscale interface, **When** the suite runs, **Then** every scenario passes and the test process makes no outbound DNS resolution or HTTP request to any host other than the suite's own httptest-bound loopback servers.
3. **Given** the suite has just passed once, **When** it is re-run four more consecutive times back-to-back without modifying source, **Then** all four re-runs pass with zero failures and zero `t.Skip` invocations.
4. **Given** the suite is run under the race detector (`-race`), **When** any scenario completes, **Then** no data race is reported by the Go runtime in any goroutine the harness or the real `internal/*` packages spawn during the scenario.

---

### Edge Cases

- **Scenario 9 split**: The "overnight expiry" scenario documents two distinct paths (strict mode vs. grace-cache mode). They produce different final states (`awaiting-approval` vs. `running` from cache) and different audit-event sequences. The suite must implement them as two separate test functions, both required to pass.
- **OS-specific status socket path**: The supervisor's status-socket path differs by OS (macOS: `~/Library/Caches/hush/supervise-<daemon>.sock`; Linux: `$XDG_RUNTIME_DIR/hush-supervise-<daemon>.sock`). Each scenario that asserts status-socket shape must work on both supported OSes; on whichever OS the suite is invoked, the harness must resolve the correct path without hard-coded assumptions.
- **Clock injection for time-sensitive scenarios**: Scenarios 8 (daytime refresh window) and 9 (overnight expiry) only make sense against a virtual clock; running them against `time.Now()` would either take 20+ hours or be impossible to test deterministically. The harness must inject a controllable clock into the supervisor under test.
- **Audit log read-back determinism**: The hash-chained audit log appends asynchronously. Each scenario's audit assertions must wait for a documented quiescence point (the supervisor reaching its expected terminal state for that scenario) before reading the log, to avoid a race between "test asserts" and "supervisor finishes writing".
- **Sentinel collision**: A vault may legitimately contain a secret whose value happens to start with the same prefix as the test-suite sentinel constant. Tests must use a sentinel constant chosen to be infeasibly improbable in any real or fixture secret (e.g. a high-entropy unique-per-scope string). The sentinel constant must be defined exactly once across the suite.
- **Goroutine leakage between scenarios**: Each scenario spins up a real supervisor with its own status-socket goroutine, refresh-window goroutine, child-wait goroutine, etc. The harness must tear each one down before the next scenario starts so goroutines from Scenario N don't pollute Scenario N+1's race-detector output or port/path namespace.
- **Process-spawning scenarios on a shared CI machine**: Scenarios 2–15 fork real child processes (test stub binaries). Two parallel scenario tests must not collide on a shared resource (status-socket path, pidfile path, audit log path, fixture vault). The suite must isolate each scenario into its own ephemeral state directory.
- **Discord-stub semantics for negative paths**: Scenarios 10 (Discord unavailable) and 5/6/15 (stale alerts) must be able to programmatically force the Discord stub into "unavailable", "deny", "no response within timeout", and "delivery succeeds" states. The Discord stub's surface must support all four.
- **Existing pidfile from a previous flaked run**: If a prior run left a stale pidfile on disk, the duplicate-start scenario (14) must still pass — the test's fixture-state setup must produce a clean state regardless of leftover files in the ephemeral test directory.

## Requirements *(mandatory)*

### Functional Requirements

**Scenario coverage (the deliverable):**

- **FR-001**: The suite MUST implement all 15 scenarios from `docs/LIFECYCLE-SCENARIOS.md`. No scenario may be skipped, stubbed, or marked `t.Skip`.
- **FR-002**: Each scenario MUST be a single test function named with the deterministic shape `Test_Scenario_NN_<slug>`, where `NN` is the two-digit scenario number from `docs/LIFECYCLE-SCENARIOS.md` and `<slug>` is a short PascalCase descriptor. **The canonical list of 17 names is locked here**; plan-phase, tasks-phase, and the implement-phase AC-MATRIX update MUST use these exact identifiers:
  1. `Test_Scenario_01_InteractiveShellRequest`
  2. `Test_Scenario_02_FirstDaemonBootstrap`
  3. `Test_Scenario_03_CleanChildExitRefill`
  4. `Test_Scenario_04_ChildCrashRefill`
  5. `Test_Scenario_05_ChildExit78Stale`
  6. `Test_Scenario_06_ValidatorFailure`
  7. `Test_Scenario_07_VaultRestartInvalidatesSession`
  8. `Test_Scenario_08_DaytimeRefresh`
  9. `Test_Scenario_09_OvernightExpiry_Strict`
  10. `Test_Scenario_09_OvernightExpiry_Grace`
  11. `Test_Scenario_10_DiscordUnavailable`
  12. `Test_Scenario_11_TailscaleReady`
  13. `Test_Scenario_11_BootTimeout`
  14. `Test_Scenario_12_AgentStatusCheck`
  15. `Test_Scenario_13_MidSessionRotation`
  16. `Test_Scenario_14_DuplicateStart`
  17. `Test_Scenario_15_LogPatternMatch`
- **FR-003**: Scenario 9 ("Overnight expiry") MUST be implemented as two separate test functions — `Test_Scenario_09_OvernightExpiry_Strict` and `Test_Scenario_09_OvernightExpiry_Grace` — because the strict and grace paths produce different final states and different audit sequences. Scenario 11 ("Tailscale not ready at boot") MUST be split for the same reason — `Test_Scenario_11_TailscaleReady` (success branch, final state `running`) and `Test_Scenario_11_BootTimeout` (timeout branch, final state `stopped` + `AlertClassBootTimeout`). **Total scenario test functions in the suite = 17** (13 single-scenario tests + Scenario 9 split into 2 + Scenario 11 split into 2).

**Per-scenario assertion shape (the four required assertions):**

- **FR-004**: Every scenario MUST assert that the **final state** of the supervisor (or, for Scenario 1, the final state of the interactive request flow) matches the outcome documented in `docs/LIFECYCLE-SCENARIOS.md` for that scenario. Concretely: the supervisor's state-machine value matches (`running`, `awaiting-approval`, `stopped`, or grace-mode-restart-running as applicable). **Test boundary**: scenarios whose documented flow includes operator-recovery (5, 6, 7, 9-strict, 15) end at the operator-blocked terminal state (`awaiting-approval` or `stopped`) and assert the alert emission; they do NOT drive through to post-recovery `running`. The full refresh-then-resume recovery path is owned by Scenario 13 (`hush client refresh`).
- **FR-005**: Every scenario MUST assert that the **on-disk audit log** (the hash-chained JSONL file written by `internal/audit`) contains the audit events listed for that scenario in the documented order, drawn from the vocabulary in [`docs/SPEC.md`](../../docs/SPEC.md) §FR-14. The assertion MUST verify both the events and their ordering, not just set-equality. The concrete per-scenario events table (17 rows × N events each) is produced in plan-phase (`plan.md`) by deriving from the §FR-14 vocabulary and the scenario flows; spec-phase locks only the vocabulary source and the ordering-strict semantics.
- **FR-006**: Every supervisor scenario (Scenarios 2–15) MUST assert that the **status socket JSON** returned by `GET /status` over the local Unix socket conforms to the shape documented in [`docs/SPEC.md`](../../docs/SPEC.md) §FR-12 AND that the specific field values for that scenario are present (e.g., `state`, `child_pid`, `scope_healthy`/`scope_stale`, `discord_connected`, `session_expires_at`, `refresh_window_next`, `last_auth_failure`). Scenario 1 (interactive) is exempt because no supervisor exists.
- **FR-007**: Every scenario MUST assert via the suite's `AssertSentinelAbsent` helper that **no sentinel byte sequence** appears in any captured log stream produced during the scenario. The streams that MUST be captured and asserted against include, at minimum: the operational slog output of the in-process supervisor and server; the contents of the audit JSONL file; the body of every status-socket response read; the stderr and stdout of every spawned child process; and the string form of every error returned to the test from any boundary.

**Suite-wide quality properties:**

- **FR-008**: The suite MUST be build-tagged so it only compiles under `-tags=integration`. Standard `go test ./...` invocations without the tag MUST NOT compile any harness or scenario file.
- **FR-009**: The full suite MUST complete in **under 120 seconds wall-clock** on a developer-class machine (M-series Mac or modern Linux x86_64 with ≥8 cores).
- **FR-010**: The full suite MUST pass **5 consecutive invocations** under `-race` with **zero failures and zero flakes**. A "flake" includes timeouts, intermittent race-detector reports, intermittent goroutine-leak failures, and any non-deterministic outcome.
- **FR-011**: The suite MUST be runnable cleanly under the Go race detector (`magex test:race -tags=integration`). No scenario may pass without `-race`; the gate is `-race`-on.

**Network and external-service hermeticism:**

- **FR-012**: The suite MUST NOT make any outbound network call to any host other than loopback and the Tailscale-interface stub the harness controls. Concretely, no DNS resolution and no HTTP/TCP/UDP connection to any of: Discord (discord.com, gateway.discord.gg), Anthropic (api.anthropic.com), OpenAI (api.openai.com), Google AI (generativelanguage.googleapis.com), GitHub (api.github.com), or any other production endpoint. CI environments without internet access MUST run the suite identically.
- **FR-013**: The Discord boundary MUST be replaced with a programmable stub supplied by `internal/testutil` (SDD-04). Each scenario MUST be able to program the stub with a per-scenario sequence of decisions including, at minimum: approve, deny, timeout, unavailable (boot-down), and disconnect-during-pending.
- **FR-014**: The five provider validator HTTP endpoints (Anthropic, Anthropic-OAuth, OpenAI, Google AI, GitHub) MUST be replaced with loopback-bound HTTP fixtures programmable per scenario to return 200, 401, or simulated network failure. The supervisor's validators MUST be pointed at these fixtures during the scenario.
- **FR-015**: The Tailscale-reachability probe (used in boot-retry, Scenario 11) MUST be replaceable with a programmable in-process stub so the boot-retry path can be exercised without actually waiting for Tailscale.

**Real-package surface (what is NOT mocked):**

- **FR-016**: The suite MUST exercise the **real** packages: `internal/supervise` (including the SDD-24 orchestrator `Lifecycle`), `internal/server`, `internal/audit`, `internal/token`, `internal/vault`, `internal/transport/ecies`, `internal/transport/sign`, `internal/keys`, and `internal/cli` for the request/supervise/client-status/client-refresh entry points used by scenarios. The suite MUST NOT substitute fakes for these packages.
- **FR-017**: The fixture vault used by each scenario MUST be produced by the real `internal/vault` codec from a sentinel-tagged plaintext payload, so a leak of any secret byte through any path is detectable by `AssertSentinelAbsent`.
- **FR-018**: The JWT issued during each scenario MUST be a real ES256K-signed token produced by `internal/token`, not a stub claim object, so signature-verification paths in the server are exercised.

**Determinism and isolation:**

- **FR-019**: The wall clock observed by the supervisor under test MUST be injectable per scenario so refresh-window (Scenario 8), TTL expiry (Scenario 9), and boot-retry backoff (Scenario 11) can be exercised without real elapsed time. The injected clock MUST be controllable by the test (advance-by, set-to).
- **FR-020**: Each scenario MUST run in its own ephemeral state directory (vault path, audit log path, pidfile path, status-socket path) so two scenarios run sequentially in the same suite do not collide on filesystem state, and a stale file from a prior aborted run does not contaminate a fresh run.
- **FR-021**: The suite MUST clean up every goroutine, every child process, every file handle, and every Unix socket it created before the test function returns, regardless of pass or fail. A failed scenario MUST NOT leak state into a later scenario.
- **FR-022**: The suite MUST NOT use `t.Parallel` at the scenario level (scenarios share suite-wide setup boundaries and global state in some real packages). Individual scenarios MAY parallelize internal sub-checks only where no shared mutable state is touched.

**Operator-observable signals each scenario must produce:**

- **FR-023**: Scenarios that exercise an alert path (Scenarios 5, 6, 9-strict-overnight, 10, `Test_Scenario_11_BootTimeout`, 15) MUST assert that the corresponding Discord alert payload was delivered to the Discord stub, and that the alert's class matches the documented class for that scenario from the locked 10-value `AlertClass` enum (see active feature plan note in `CLAUDE.md`).
- **FR-024**: Scenarios that exercise a `[STALE]` alert (Scenarios 5, 6, 15) MUST assert the alert is visually distinct from a routine approval prompt in the captured Discord-stub message stream, per the requirement in `docs/LIFECYCLE-SCENARIOS.md` "Required alert classes" section.
- **FR-025**: Scenarios that exercise the refresh-window prompt (Scenario 8) MUST assert that no child restart occurs purely as a result of the refresh succeeding — only the supervisor's refill capability is renewed.
- **FR-026**: Scenarios that exercise the watchdog (Scenario 15) MUST assert that the log-pattern match produces an alert but does NOT change the supervisor's state-machine state, per Constitution Principle V and the locked scope of SDD-24.

**AC-MATRIX bookkeeping:**

- **FR-027**: When the suite is green, the AC-10 row in `docs/AC-MATRIX.md` MUST list the exact 17 test function names (13 single-scenario tests + Scenario 9 split + Scenario 11 split) under the "Test path" column for the SDD-25 owner row.
- **FR-028**: The AC-9 row in `docs/AC-MATRIX.md` MUST be updated to acknowledge that the SDD-25 suite is the test-infra completeness deliverable that lifts the AC-10 paths into the coverage budget.

### Key Entities

- **Scenario**: A single Go test function in the integration suite, named `Test_Scenario_NN_<slug>`, that drives one end-to-end lifecycle path from `docs/LIFECYCLE-SCENARIOS.md`. Carries: per-scenario Discord-stub script; per-scenario clock; per-scenario validator-endpoint fixture responses; per-scenario child-process exit script; per-scenario expected final state, expected audit event ordering, and (for supervisor scenarios) expected status-socket field values.
- **Captured log stream**: A per-scenario buffer (or set of buffers) accumulating every byte written to slog, every byte written to the audit JSONL, every response body served by the status socket, and every byte written to stdout/stderr by any spawned child process during the scenario.
- **Sentinel**: A unique per-scope, high-entropy byte sequence injected into the fixture vault before the scenario starts; serves as the canary whose absence in every captured stream proves the redaction path is intact. Defined once for the suite.
- **Discord stub**: A programmable in-process replacement for the Discord bot, supplied by `internal/testutil` (SDD-04). Accepts a per-scenario decision script and records every approval request it receives.
- **Validator endpoint fixture**: A loopback-bound HTTP server (one per provider) programmable per scenario to return 200/401/network-failure for the cheapest read-only endpoint of each provider, so the supervisor's validators exercise their real code path without hitting production.
- **Injectable clock**: A function-typed value injected into the supervisor under test; the harness can advance it forward by an interval or set it to a fixed instant, deterministically exercising refresh-window, TTL-expiry, and boot-retry backoff timing.
- **Ephemeral state directory**: A per-scenario temporary directory containing the fixture vault file, the audit log file, the pidfile, and the status-socket path. Removed after the scenario completes.
- **Test child binary**: A small stub executable spawned in place of a real daemon for scenarios 2–15. Its behaviour (exit code, exit timing, stdout/stderr content, signal-handling behaviour) is parameterised per scenario so the scenario can simulate clean exit, crash, exit-78, hang-until-SIGTERM, or auth-failure-line-emission as needed.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: All 17 scenario test functions (13 single-scenario tests + the Scenario 9 strict/grace split + the Scenario 11 ready/boot-timeout split) pass under `magex test:race -tags=integration`.
- **SC-002**: The full suite's wall-clock time is **under 120 seconds** on a developer-class machine (M-series Mac or modern Linux x86_64 with ≥8 cores).
- **SC-003**: **Five consecutive** invocations of `magex test:race -tags=integration` complete with zero failures and zero retries.
- **SC-004**: A network capture taken during a suite run shows **zero** DNS resolutions or TCP/UDP connections to any host outside loopback and the harness's own loopback-bound httptest listeners; specifically, zero packets to discord.com, gateway.discord.gg, api.anthropic.com, api.openai.com, generativelanguage.googleapis.com, or api.github.com.
- **SC-005**: For every one of the 17 scenario test functions, `AssertSentinelAbsent` passes against every captured log stream — meaning a grep for the suite sentinel across every captured slog buffer, the audit JSONL file, every status-socket response body, and every child stdout/stderr buffer returns zero matches.
- **SC-006**: The AC-10 row in `docs/AC-MATRIX.md` lists each of the 17 test function names under "Test path" and reports status `green`.
- **SC-007**: The AC-9 row in `docs/AC-MATRIX.md` acknowledges this suite as the test-infra completeness deliverable that produces the AC-10 coverage paths.
- **SC-008**: Coverage of `internal/supervise` measured with `magex test:race -tags=integration` plus the existing unit tests is at least **95%** line coverage, matching the AC-9 target for that package in [`docs/AC-MATRIX.md`](../../docs/AC-MATRIX.md).
- **SC-009**: A failed scenario produces a failure message that names exactly one of the 16 documented scenarios — i.e., a green-or-red signal per scenario, never an opaque "supervisor crashed" without scenario attribution.
- **SC-010**: Running the suite a sixteenth, seventeenth, eighteenth time (after the five-run flake check) continues to produce zero failures — the suite's stability is not a function of run ordinal.

## Assumptions

- **SDD-04 testutil is the upstream dependency for harness primitives.** The chunk contract requires `internal/testutil`'s `NewTestVault`, `NewTestKeys`, `SentinelSecret`, `AssertSentinelAbsent`, and `DiscordStub` (per [`docs/sdd/SDD-25.md`](../../docs/sdd/SDD-25.md)). At spec time SDD-04 is listed `pending` in the AC-MATRIX; this spec assumes SDD-04 will be implemented (or its required helpers stubbed inline) before the implement phase of SDD-25 runs. The plan phase MUST surface SDD-04 status as a precondition.
- **The 15 scenarios are stable.** `docs/LIFECYCLE-SCENARIOS.md` is the source of truth and is not expected to change during SDD-25's execution. If a real bug in the design is discovered while implementing a scenario, the scenarios doc is updated first and the test follows — the test does not silently diverge.
- **The SDD-24 orchestrator (`internal/supervise.Lifecycle`) is the system under test.** SDD-24 is already merged (commit `f36ab26`); the harness composes the SDD-24 orchestrator and the SDD-19..22 primitives without modification.
- **Developer-class machine baseline.** "Under 120 s" is measured on an M-series Mac or a modern Linux x86_64 with at least 8 cores and SSD storage. CI runner hardware that is significantly slower may take longer but MUST still pass within the wall-clock budget the CI workflow allocates; the 120 s target is the developer-laptop expectation, not a hard CI ceiling.
- **OS support matrix matches v0.1.0.** The suite is required to pass on the two supported OSes (macOS arm64 and Linux amd64). Platform-specific status-socket and runtime paths resolve at scenario setup time via the same code paths the production supervisor uses.
- **No process-wide state leakage between Go tests.** Real `internal/*` packages must be re-instantiable per scenario (no `init()` side effects, no package-level mutable state) — that is already a Constitution Principle IX requirement, so the harness assumes it holds. If a real package proves to have a hidden global, that is a real-code bug to be fixed in that package, not worked around in the harness.
- **The "developer laptop" runs `magex`.** Suite invocation is via the project's standard `magex test:race -tags=integration` entry point; raw `go test` invocations are not the contracted interface (though they SHOULD work as an unofficial path).
- **Audit log assertions are ordering-strict, not whitelist-strict.** A scenario asserts the events it documents appear in order. The suite tolerates additional housekeeping audit events only if the per-scenario ephemeral state directory cleanly isolates the audit-log file, which FR-020 requires.
- **The "child process" in scenarios is a small test stub.** Scenarios that fork a child do not link or invoke any real daemon binary; they use a parameterised test stub whose behaviour is controlled by the scenario's input.
- **The chunk contract (`docs/sdd/SDD-25.md`) is the authoritative scope ceiling.** Anything the chunk contract explicitly excludes (e.g., `t.Parallel` at the scenario level, hitting external networks, skipping scenarios) is excluded by this spec regardless of any softer phrasing here.

## Dependencies

- **Upstream SDD chunks (all required-done before implement phase)**: SDD-01 (keys), SDD-02 (SecureBytes), SDD-03 (vault), SDD-04 (testutil — currently `pending`; must reach at least the harness helpers used here), SDD-05 (logging), SDD-06 (config), SDD-07 (token/JWT), SDD-08 (sign), SDD-09 (ECIES), SDD-10 (server skeleton), SDD-11 (Discord bot — providing the `Approver` interface the stub will implement), SDD-12 (claim handler), SDD-13 (other handlers + audit), SDD-14 (CLI root + serve/health/version/revoke), SDD-15 (init + keychain), SDD-16 (CLI request), SDD-17 (CLI secret), SDD-18 (supervise config), SDD-19 (supervisor state), SDD-20 (child fork/exec), SDD-21 (refill/refresh/grace), SDD-22 (pidfile + status socket), SDD-23 (CLI supervise + client), SDD-24 (orchestrator `Lifecycle` — already merged).
- **Constitutional principles in scope**: VIII (Testing Discipline — TDD-mandatory for integration suite; the gate enforced by AC-9/AC-10), V (Staleness is Visible — every documented alert must be operator-observable and the suite proves it), IX (Idiomatic Go Discipline — no goroutine leaks, no global mutable state, every goroutine has owner/ctx/termination), X (Observability & Redaction — sentinel-absent assertion is the integration-level proof of redaction).
- **Documentation kept in sync**: this chunk's implement phase updates [`docs/AC-MATRIX.md`](../../docs/AC-MATRIX.md) AC-9 + AC-10 rows, [`docs/PACKAGE-MAP.md`](../../docs/PACKAGE-MAP.md) (new `tests/integration/` entry), and [`docs/SDD-PLAYBOOK.md`](../../docs/SDD-PLAYBOOK.md) (SDD-25 status → `done`).

## Out of Scope

- **Unit tests for individual `internal/*` packages.** Those belong in the owning chunk and are already largely complete.
- **Fuzz targets.** The six required fuzz targets are owned by their respective chunks (vault decode → SDD-03; JWT validate → SDD-07; ECIES decrypt → SDD-09; request signature → SDD-08; supervisor config TOML → SDD-18; status socket JSON if custom → SDD-22). This suite does not add fuzz targets.
- **Cross-platform CI matrix setup.** The chunk produces scenarios that run on both supported OSes; the CI workflow that exercises them on both is owned by SDD-31 (release gates).
- **Performance benchmarks.** The 120 s budget is a wall-clock gate, not a per-package microbenchmark. No `*_bench_test.go` files are produced by this chunk.
- **Real Discord, real Tailscale, real provider validators.** All four are mocked. Validating against the real services is an operational concern documented in `docs/OPERATIONS.md`, not a test concern.
- **Additional scenarios beyond the documented 15.** New scenarios may be added in future chunks (e.g., SDD-26 validators, SDD-27 watchdog, SDD-28 alerts) but are out of scope for SDD-25.
- **Replacing existing per-package unit/integration tests.** The harness composes the real packages; their unit tests continue to exist and run under the standard test invocation.
- **Discord connection-state transition alerts (`AlertClassDiscordDisconnected`, `AlertClassDiscordReconnected`).** Coverage for these two alert classes lives in SDD-11 (the Discord bot's connection-monitor loop) and its unit tests, not in SDD-25. CLAUDE.md confirms `ActionDiscordDisconnected` and `ActionDiscordReconnected` are emitted by code paths other than the supervisor orchestrator. No SDD-25 scenario exercises a disconnect/reconnect transition; the closest scenario (Scenario 10, Discord unavailable at `/claim` submission) maps to the distinct `AlertClassDiscordUnavailableOnClaim` class.
