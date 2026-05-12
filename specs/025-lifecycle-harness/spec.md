# Feature Specification: Lifecycle Integration Harness (15 Scenarios — AC-10 Owner)

**Feature Branch**: `025-lifecycle-harness`
**Created**: 2026-05-12
**Status**: Draft
**Input**: User description: "tests/integration: end-to-end harness running real internal/* packages with mocked external services; implements all 15 lifecycle scenarios from docs/LIFECYCLE-SCENARIOS.md, each asserting final state + audit ordering + status socket shape + no sentinel leak; build-tagged //go:build integration; suite under 120s, no flake on 5 runs"

## Overview

This chunk delivers the integration test suite that proves Acceptance Criterion AC-10. Fifteen named lifecycle scenarios from `docs/LIFECYCLE-SCENARIOS.md` are exercised end-to-end against the real `internal/*` packages (vault, server, supervisor, child, status socket, audit chain), with **only the external boundaries mocked** (Discord, Anthropic/OpenAI/GitHub/Google AI validator endpoints, NTP probe). Every other chunk's tests are unit- or fuzz-level; this suite is the AC-10 owner of record — the single place where the system is proven to work as a whole. The suite also lifts AC-9 (test infra completeness) by demonstrating that the in-tree integration tooling can express every lifecycle outcome the project promises.

## Clarifications

### Session 2026-05-12

- Q: How strict is the per-scenario audit-ordering assertion (FR-025-7)? → A: Relative ordering — the documented events appear in the documented order, but additional unmentioned audit events between them are permitted and do not fail the assertion.
- Q: For Scenario 10 (Discord unavailable), which `/claim` origin does the test cover (the scenario doc permits "client or supervisor")? → A: Both — `Test_Scenario_10_DiscordUnavailable` ships as one parent function with two subtests (`Interactive` and `Supervisor`), each asserting 503 + no auto-approve + caller surfaces the failure.
- Q: What is the policy when a scenario depends on production code owned by a chunk outside SDD-25's blocker list (SDD-26 validators, SDD-27 watchdog, SDD-28 alert classes)? → A: Implement all 15 scenarios against real production code; scenarios whose provider chunks are unshipped fail until those chunks land. No stubbing, no harness stand-ins for production behavior. SDD-25's AC-10 row reaches `green` only when all 15 scenarios pass against fully-shipped providers. The SDD-25 chunk contract's blocker list is treated as the floor for harness construction, not the ceiling for green-status.
- Q: How does `Test_Scenario_01_FirstInteractive` (the only scenario with no supervisor) satisfy FR-025-6's "final state" assertion, given that LIFECYCLE-SCENARIOS.md §1's expected outcomes are properties rather than a named state? → A: Compound final-state — the test asserts (a) the vault server is healthy after the flow (`vault_loaded=true`, `discord_connected=true` via `/health`), (b) the wrapped child shell exited cleanly with the documented exit code, (c) the issued JWT's state in the server's token store reflects use-count consumption or expiry, and (d) each of the three §1 expected outcomes (no disk persistence, no log leakage, approval-DM-count exactly one) has a corresponding assertion.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Release manager: prove the system works end-to-end (Priority: P1)

The hush release manager cannot tag v0.1.0 until AC-10 is green. AC-10 demands that all 15 supervisor/server lifecycle scenarios documented in `docs/LIFECYCLE-SCENARIOS.md` run successfully — proving silent refill, exit-78 staleness, Discord-unavailable fail-closed, vault restart recovery, rotation propagation, duplicate-supervisor refusal, and the rest. This story delivers the suite that gates the release.

**Why this priority**: This is the v0.1.0 release gate. Without it, the project ships with eleven security-critical guarantees (G1–G5 plus the seven crypto layers) that have never been demonstrated as a whole, only as parts. Constitution VIII makes the suite non-negotiable: "Every acceptance criterion in `docs/SPEC.md` must map to a concrete, runnable test."

**Independent Test**: Run `magex test:race -tags=integration` from a clean checkout against the real `internal/*` packages with mocked external services. All 15 scenario tests pass; the exit code is zero; no real Discord/Anthropic/OpenAI/GitHub/Google AI host is contacted; the suite returns in under two minutes on a developer laptop.

**Acceptance Scenarios**:

1. **Given** a clean checkout on macOS or Linux with the integration build tag enabled, **When** the release manager runs the integration suite, **Then** all 15 scenarios from `docs/LIFECYCLE-SCENARIOS.md` execute as named `Test_Scenario_NN_<slug>` functions and every test reports PASS.
2. **Given** the suite is running, **When** any scenario completes, **Then** that scenario has asserted: (a) the supervisor/server reached the documented final state, (b) the audit log contains the documented events in the documented order, (c) for any scenario that runs a supervisor, the status socket JSON matches the documented shape, and (d) no sentinel marker string appears anywhere in captured logs/output.
3. **Given** the suite has executed, **When** the release manager inspects the AC-10 row of `docs/AC-MATRIX.md`, **Then** every scenario row lists the matching `Test_Scenario_NN_<slug>` test path and the row status is green.

---

### User Story 2 - Maintainer: trust the suite as a release gate (Priority: P2)

A maintainer running the suite during day-to-day development needs deterministic, reproducible runs. A "mostly-passes" integration suite is worse than no suite at all — it trains the team to retry-until-green and erodes the gate. This story delivers the timing, isolation, and flake guarantees that let a maintainer treat a green run as evidence rather than noise.

**Why this priority**: Constitution VIII (Testing Discipline) and Principle IX (idiomatic Go: explicit lifecycle, no fire-and-forget goroutines) require the suite be observably deterministic. A flaky suite cannot serve as the release gate it is required to be.

**Independent Test**: Run the suite five consecutive times on the same machine without changing any input. All five runs pass; total wall-clock time for one full run is under 120 seconds; no scenario leaves a goroutine, file descriptor, listening socket, child process, or temporary file behind after its test function returns.

**Acceptance Scenarios**:

1. **Given** the suite is run five consecutive times on a single developer laptop, **When** each run completes, **Then** zero scenarios fail across the five runs and the total wall-clock time per run remains under 120 seconds.
2. **Given** a scenario depends on the wall clock (refresh window, TTL expiry, grace cache TTL), **When** the test executes, **Then** time is advanced through a controllable abstraction so the scenario reaches its outcome without real time elapsing and without race-detector findings.
3. **Given** a scenario completes (pass or fail), **When** the test harness tears down, **Then** every supervisor process spawned, every Unix socket bound, every PID file created, every temporary directory used, and every goroutine started by the harness is cleaned up before the test function returns.
4. **Given** the suite is executed with `go test -race -tags=integration`, **When** it runs, **Then** no race-detector findings are reported.

---

### User Story 3 - Security reviewer: prove no secret leaks during the system's worst moments (Priority: P3)

The fifteen scenarios cover the moments most likely to leak a secret: error paths, child crashes, validator failures, vault restarts, Discord-unavailable, log-pattern matches. A security reviewer needs explicit evidence that during each of these failure paths, no plaintext secret value reaches a log line, an error message, an audit record, a status socket response, or any other captured byte stream.

**Why this priority**: Constitution Principle X (Observability & Redaction) demands type-driven redaction; Constitution Principle VIII makes redaction assertions a required test type. The `docs/TESTING-STRATEGY.md` §5 sentinel pattern is the project's canonical proof technique. This story embeds that pattern into every scenario.

**Independent Test**: Inject a sentinel value (the marker string from `internal/testutil`) as the plaintext value of one or more secrets in each scenario. After the scenario completes, assert that the sentinel does not appear in any captured operational log, audit record, status socket response, error message, stdout, or stderr from the harness, the supervisor, the server, the validators, or the child process.

**Acceptance Scenarios**:

1. **Given** any scenario in the suite, **When** the scenario completes, **Then** an automated assertion (`AssertSentinelAbsent` or equivalent) confirms the sentinel marker string is absent from every captured byte stream the scenario produced.
2. **Given** a scenario where a validator returns 401, **When** the supervisor emits its `[STALE] Validator Failure` alert, **Then** the captured alert payload identifies the scope name only — never the secret value.
3. **Given** a scenario where the child exits with code 78, **When** the supervisor emits the `[STALE] Child Exit 78` audit record and Discord alert, **Then** neither the audit record nor the alert payload contains any plaintext secret bytes.

---

### Edge Cases

- **A scenario asserts an audit event that the implementation does not yet emit.** The scenario MUST fail loudly, not skip. The harness MUST NOT allow `t.Skip` as a way to soften a missing-event assertion; the audit log is a normative contract (FR-14).
- **A scenario depends on a clock-driven transition (refresh window, TTL expiry, grace cache).** The scenario MUST drive time deterministically through an injected abstraction; the suite MUST NOT call `time.Sleep` to wait for real time to advance.
- **A scenario requires a supervisor to outlive a child process.** The harness MUST allow the supervisor to remain running while the child exits and restarts, and MUST clean up both supervisor and child by the time the test function returns.
- **A scenario requires Discord to be unavailable mid-flight (Scenario 10).** The mock Discord implementation MUST distinguish between (a) never started, (b) connected, (c) disconnected mid-session, and (d) reconnected after disconnect, and each state MUST produce the response the production server's `claim` handler expects.
- **A scenario covers a documented variant (Scenario 9 has strict and grace modes).** Both variants MUST be implemented as separate test functions (`Test_Scenario_09_OvernightExpiry_Strict`, `Test_Scenario_09_OvernightExpiry_Grace`); neither variant may be omitted.
- **A scenario depends on a per-OS path (`~/Library/Caches/hush/` on macOS, `$XDG_RUNTIME_DIR/` on Linux).** The scenario MUST run on both supported OS targets without modification; per-OS path handling is the supervisor's responsibility, not the test's.
- **A scenario uncovers a real gap in the underlying `internal/*` packages that no harness work can paper over.** The scenario MUST fail and surface the gap rather than be reshaped to pass; the suite is the AC-10 contract and the production code is the artifact under test.
- **A scenario's test function returns before all spawned goroutines exit.** The harness MUST detect and fail the run; a "passing" test that leaves goroutines behind violates Principle IX and is treated as a failure.
- **The suite is run without the `integration` build tag.** Zero integration test files MUST compile or execute; the suite MUST be invisible to default `go test ./...`.

## Requirements *(mandatory)*

### Functional Requirements

#### Suite scope and structure

- **FR-025-1**: The integration suite MUST live in a dedicated test tree separate from production code and from the unit tests that already cover the constituent packages.
- **FR-025-2**: Every test file in the suite MUST be gated by a build tag (`integration`) so that the default `go test ./...` invocation never compiles or executes any scenario.
- **FR-025-3**: The suite MUST implement all 15 scenarios from `docs/LIFECYCLE-SCENARIOS.md`. No scenario may be skipped, stubbed, or replaced by a TODO. Scenarios that depend on production code owned by chunks outside SDD-25's blocker list (SDD-26 validators, SDD-27 watchdog, SDD-28 alert classes) MUST be implemented against the real production packages from those chunks; the harness MUST NOT supply stand-in implementations of production behavior. A scenario that fails because its provider chunk is not yet shipped is a valid, expected failure that surfaces the sequencing gap — the AC-10 row reaches `green` only when all 15 scenarios pass against fully-shipped providers.
- **FR-025-4**: Each scenario MUST be implemented as exactly one Go test function with a deterministic, conventional name of the form `Test_Scenario_NN_<slug>` where `NN` is a two-digit number `01..15` and `<slug>` is a short PascalCase summary derived from the scenario title. Subtests are permitted within a scenario function (FR-025-5a) and do not count as additional top-level scenarios.
- **FR-025-5**: Scenario 9 ("overnight expiry, with and without grace cache") is the only scenario that ships in two **top-level** variants. Both variants — strict mode and grace mode — MUST be implemented as separate top-level test functions and counted toward the 15-of-15 release bar as a single scenario row.
- **FR-025-5a**: Scenario 10 ("Discord unavailable during new claim") is the only scenario whose single test function MUST contain two named subtests covering the two `/claim` origins permitted by `docs/LIFECYCLE-SCENARIOS.md` §10: `Interactive` and `Supervisor`. Each subtest asserts the same outcome contract (503 from the server, no auto-approve fallback, caller surfaces the failure clearly) against its respective origin's code path. Both subtests MUST pass for the scenario row to be green.

#### Per-scenario assertions

- **FR-025-6**: Every scenario test function MUST assert that the supervisor (or, for interactive-only scenarios, the server) ended in the final state documented by `docs/LIFECYCLE-SCENARIOS.md` for that scenario. For Scenarios 2–15, "final state" is the named supervisor state from the state model (`fetching`, `running`, `awaiting-approval`, `grace-restart`, `stopped`) plus any documented scope-health / discord-connected / child-PID facts that the scenario implies. For Scenario 1 (interactive, no supervisor), "final state" is a compound assertion combining (a) the vault server reports `vault_loaded=true` and `discord_connected=true` via `/health` after the flow, (b) the wrapped child shell exited with the exit code the scenario expected, (c) the issued JWT's state in the server's token store reflects use-count consumption or expiry as appropriate to the scenario, and (d) each of the three §1 expected outcomes (no disk persistence of secrets, no plaintext-value logging — covered by FR-025-9, approval DM count exactly one) is asserted individually.
- **FR-025-7**: Every scenario test function MUST assert that the audit log produced during the scenario contains the events documented for that scenario in the documented relative order. The assertion is a subsequence check: every documented event MUST appear, and the order of those documented events relative to one another MUST match the documentation; additional unmentioned audit events between them are permitted and do not fail the assertion. A missing documented event, or two documented events appearing in reversed order, is a scenario failure. The set of recognized event types is the list in SPEC §FR-14.
- **FR-025-8**: Every scenario that runs a supervisor MUST assert that the supervisor's local status socket returns a JSON response whose shape matches FR-12 of the SPEC, and whose field values reflect the supervisor's documented final state for that scenario (state name, scope health, expiry timestamps, discord-connected flag, child PID/uptime).
- **FR-025-9**: Every scenario MUST assert, via a single common helper, that no sentinel marker string appears anywhere in the captured operational log, audit log, status socket response, error messages, stdout, or stderr produced during the scenario.
- **FR-025-10**: The four assertion types in FR-025-6, FR-025-7, FR-025-8, and FR-025-9 MUST all be made before the test function returns; a scenario that omits any of the four assertions is non-compliant even if it otherwise passes.

#### External boundaries are mocked, internals are real

- **FR-025-11**: The suite MUST exercise the real production packages for vault decode, key derivation, JWT issuance and validation, ECIES encryption, request signing, audit chain construction, supervisor state machine, child process management, status socket, refresh scheduler, and grace cache. The suite is not a mock of the system; it is the system under test.
- **FR-025-12**: The suite MUST mock exactly the boundaries that cross a host: the Discord bot (request DM, await decision, deliver decision, disconnect/reconnect simulation), the credential validators' upstream provider endpoints (Anthropic, OpenAI, GitHub, Google AI, Anthropic-OAuth), and the NTP/clock-sync probe. Every other component is real.
- **FR-025-13**: The suite MUST NOT make any TCP, UDP, or HTTPS connection to a host outside the test process. Any attempt to reach a real external host (Anthropic, OpenAI, GitHub, Google AI, Discord, NTP server) MUST be a test failure, not silently bypassed.
- **FR-025-14**: The Discord mock MUST be able to be programmed per-scenario with: an approval decision sequence (approve, deny, timeout), a connectivity sequence (available, unavailable, reconnected), and a rate-limit sequence (under limit, at limit), all driven explicitly by the scenario test function rather than implicitly by elapsed time.
- **FR-025-15**: The validator HTTP mocks MUST be able to be programmed per-scenario to return 200 OK, 401 Unauthorized, 403 Forbidden, network failure, or timeout, with the choice driven by the scenario test function and isolated per scenario.

#### Time and determinism

- **FR-025-16**: The suite MUST drive any wall-clock-dependent transition (refresh window firing, TTL expiry, grace cache expiry, refresh nudge, replay-window expiry, boot retry backoff) through a controllable time source. The suite MUST NOT call `time.Sleep` on real time to wait for any documented transition to fire.
- **FR-025-17**: The suite MUST be deterministic: five consecutive runs on the same host with the same inputs MUST produce identical pass/fail outcomes for every scenario. A scenario that passes on one run and fails on another, with no input change, is a flake and a release-gate failure.
- **FR-025-18**: The suite MUST run with the Go race detector enabled (`go test -race -tags=integration`) and MUST produce zero race-detector findings.

#### Lifecycle hygiene

- **FR-025-19**: Every scenario MUST clean up all resources it allocated (supervisor processes, child processes, listening sockets, PID files, temporary directories, audit log files, captured log buffers, goroutines spawned by the harness) before its test function returns.
- **FR-025-20**: The harness MUST detect goroutine leaks at the end of each scenario and fail the scenario if any harness-spawned goroutine has not exited.
- **FR-025-21**: Scenarios that mutate shared global state MUST run serially at the suite level. Within a scenario, parallelism is permitted only where it does not share mutable state with other scenarios.
- **FR-025-22**: The suite MUST tolerate being invoked from a clean checkout without pre-existing `~/.hush/` state on the developer machine. Every scenario MUST create its own isolated state directory and MUST NOT touch the operator's real `~/.hush/`.

#### Audit ordering

- **FR-025-23**: For every scenario, the suite MUST assert the sequence of audit events the scenario produces — not merely the set of events. An audit record that arrives out of order is treated as a defect, not a tolerated reordering.
- **FR-025-24**: For every scenario, the suite MUST assert that the audit chain's hash-link continuity is unbroken (each record's `prev_hash` equals the prior record's `hash`, signatures verify with the audit key) at the end of the scenario.

#### Sentinel-leak proof

- **FR-025-25**: The suite MUST use a single, common sentinel string (provided by `internal/testutil`) as the plaintext value of at least one secret in every scenario.
- **FR-025-26**: The sentinel-absence assertion MUST cover every byte stream the scenario produces: operational log, audit log (after redaction), status socket JSON response, Discord alert payloads sent to the mock, error message strings returned from the harness, captured stdout, captured stderr.
- **FR-025-27**: A scenario that triggers a Discord alert (validator failure, child exit 78, log-pattern match) MUST assert that the alert payload identifies the scope name only — never the plaintext secret value.

#### AC-MATRIX update

- **FR-025-28**: When the suite is delivered, `docs/AC-MATRIX.md` AC-10 row MUST list the exact `Test_Scenario_NN_<slug>` test path for every one of the 15 scenarios and the row status MUST be `green`.
- **FR-025-29**: When the suite is delivered, `docs/AC-MATRIX.md` AC-9 row MUST cite the integration suite as part of the test-infra completeness evidence.

#### The 15 scenarios

The following table is normative: every row is a required scenario, in implementation order. The scenario number, slug, and source diagram in `docs/LIFECYCLE-SCENARIOS.md` are the contract.

| #  | Slug                              | Source                          | Type        | Status socket assertion required |
|----|-----------------------------------|---------------------------------|-------------|----------------------------------|
| 01 | FirstInteractive                  | LIFECYCLE-SCENARIOS.md §1       | Interactive | No (no supervisor in scenario)   |
| 02 | DaemonBootstrap                   | LIFECYCLE-SCENARIOS.md §2       | Supervisor  | Yes                              |
| 03 | CleanExitSilentRefill             | LIFECYCLE-SCENARIOS.md §3       | Supervisor  | Yes                              |
| 04 | ChildCrashSilentRefill            | LIFECYCLE-SCENARIOS.md §4       | Supervisor  | Yes                              |
| 05 | Exit78StaleCreds                  | LIFECYCLE-SCENARIOS.md §5       | Supervisor  | Yes                              |
| 06 | ValidatorBlocksChild              | LIFECYCLE-SCENARIOS.md §6       | Supervisor  | Yes                              |
| 07 | VaultRestart                      | LIFECYCLE-SCENARIOS.md §7       | Supervisor  | Yes                              |
| 08 | DaytimeRefresh                    | LIFECYCLE-SCENARIOS.md §8       | Supervisor  | Yes                              |
| 09a | OvernightExpiry_Strict           | LIFECYCLE-SCENARIOS.md §9       | Supervisor  | Yes                              |
| 09b | OvernightExpiry_Grace            | LIFECYCLE-SCENARIOS.md §9       | Supervisor  | Yes                              |
| 10 | DiscordUnavailable                | LIFECYCLE-SCENARIOS.md §10      | Mixed       | Yes — in the `Supervisor` subtest only (FR-025-5a); the `Interactive` subtest has no supervisor and therefore no status socket |
| 11 | TailscaleBootRetry                | LIFECYCLE-SCENARIOS.md §11      | Supervisor  | Yes (after recovery)             |
| 12 | StatusGate                        | LIFECYCLE-SCENARIOS.md §12      | Supervisor  | Yes (this scenario IS the status socket assertion) |
| 13 | RotationMidSession                | LIFECYCLE-SCENARIOS.md §13      | Supervisor  | Yes                              |
| 14 | DuplicateSupervisor               | LIFECYCLE-SCENARIOS.md §14      | Supervisor  | Yes (first supervisor only)      |
| 15 | LogPatternAlert                   | LIFECYCLE-SCENARIOS.md §15      | Supervisor  | Yes                              |

Scenario 9 ships as two test functions (one for strict mode, one for grace mode) and counts as one row in AC-10's 15-scenario contract — both variants must pass for the row to be green.

### Key Entities

- **Scenario**: a named, documented end-to-end story from `docs/LIFECYCLE-SCENARIOS.md`. Identified by a number (01–15), a slug, a documented set of events, a documented final state, and a documented set of audit events with their order.
- **Mocked external boundary**: a stand-in for a process or service that lives outside the hush trust boundary (Discord, validator upstreams, NTP). Programmable per-scenario; assertable per-scenario; never reaches a real network.
- **Real internal package**: a production `internal/*` package whose code is the artifact under test. The suite exercises these in-process without modification.
- **Sentinel marker string**: a unique recognizable byte sequence used as the plaintext value of secrets during a scenario. Detection of the marker in any captured byte stream is a scenario failure.
- **Captured byte stream**: any operational log, audit log entry, status socket response, mock-Discord alert payload, error message string, stdout output, or stderr output produced during a scenario's execution. The sentinel-absence assertion covers every such stream.
- **Test function name `Test_Scenario_NN_<slug>`**: the conventional and contractual name shape for the 15 scenarios. The shape is asserted by reviewers against the AC-10 row of `docs/AC-MATRIX.md`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-025-1**: 15 of 15 scenarios pass on a single execution of `magex test:race -tags=integration` from a clean checkout on macOS arm64 and Linux amd64.
- **SC-025-2**: 5 consecutive executions of the suite on the same host with no input changes produce 5 PASS results — zero flakes, zero retries.
- **SC-025-3**: A single execution of the suite completes in under 120 seconds of wall-clock time on a developer laptop.
- **SC-025-4**: Zero race-detector findings are reported by `go test -race -tags=integration` across the suite.
- **SC-025-5**: Zero scenarios produce a goroutine leak at the time the test function returns.
- **SC-025-6**: Zero scenarios leave a supervisor process, child process, Unix socket, PID file, or temporary state directory on disk after the scenario tears down.
- **SC-025-7**: Zero captured byte streams across all 15 scenarios contain the sentinel marker string at any point after the scenario completes.
- **SC-025-8**: Zero scenarios make a TCP/UDP/HTTPS connection to a host outside the test process; the suite passes on a host whose network egress is firewall-blocked except to localhost.
- **SC-025-9**: Every scenario's audit ordering assertion verifies the documented event sequence; every audit-chain hash-link continuity check passes.
- **SC-025-10**: After delivery, the AC-10 row of `docs/AC-MATRIX.md` lists each of the 15 scenario test paths and the row status is `green`; the AC-9 row references the integration suite as part of its evidence.
- **SC-025-11**: After delivery, running the default `go test ./...` (without the `integration` build tag) compiles zero integration test files and exercises zero scenarios — proving the build-tag isolation works.
- **SC-025-12**: A subsequent reviewer reading `docs/AC-MATRIX.md` and `docs/LIFECYCLE-SCENARIOS.md` can trace every documented scenario to its corresponding `Test_Scenario_NN_<slug>` test function in under five minutes.

## Assumptions

- The 22 `internal/*` packages listed across SDD-01 through SDD-23 ship in the state recorded in `docs/AC-MATRIX.md` as of the start of this chunk. This suite consumes them as-is and does not modify their production code. If a scenario uncovers a behavior gap in those packages, the gap surfaces as a scenario failure and the underlying chunk owner is responsible for the fix; this chunk does not patch production code.
- Scenarios 5 (Discord `[STALE] Child Exit 78` alert), 6 (validator catches bad secret), 8 (`[DAEMON] Refresh` prompt), and 15 (log-pattern watchdog `[STALE] Log Pattern Match` alert) reference production behavior owned by chunks outside SDD-25's blocker list — specifically SDD-26 (credential validators), SDD-27 (watchdog), and SDD-28 (the eight alert classes). Per FR-025-3, the harness implements those scenarios against the real production packages once they ship; the harness does not stub the validator HTTP-result interpretation, the watchdog pattern-match emission, or the alert-class rendering. Those scenarios are expected to fail while their provider chunks are `pending` and to pass once the providers reach `green` in `docs/AC-MATRIX.md`. The AC-10 row reaches `green` only when all 15 scenarios pass against fully-shipped providers.
- The list of mockable external boundaries is exactly: Discord (bot DMs and connectivity), the five built-in credential validators' upstream HTTP endpoints (Anthropic, Anthropic-OAuth, OpenAI, Google AI, GitHub), and the NTP/clock-sync probe. No other internal component is permitted to be replaced by a test double.
- The supported developer platforms for the suite are macOS arm64 and Linux amd64 (the v0.1.0 OS coverage matrix from `docs/TESTING-STRATEGY.md` §6). The 120-second wall-clock budget is measured on a representative developer laptop from those platforms.
- The sentinel marker string convention from `internal/testutil` (SDD-04) is the authoritative source of the marker value. This chunk reuses it rather than minting a new marker.
- The audit event vocabulary from SPEC §FR-14 is the authoritative source of event type names. Scenario assertions cite events from this list. If a scenario documents an audit event whose name is not in FR-14, this chunk is responsible for surfacing the discrepancy rather than inventing a new name.
- The status socket JSON schema is locked by SDD-22 and SDD-23. This chunk asserts against that schema verbatim; this chunk does not extend, rename, or omit-empty any field.
- The clock abstraction necessary for deterministic refresh-window/TTL/grace-cache scenarios is provided by (or trivially derivable from) `internal/supervise`'s existing scheduling seams. If a scheduling seam is not deterministically driveable from a test, that limitation is a gap to surface — not a reason to relax FR-025-16.
- "Developer laptop" for SC-025-3 means a machine class with 8+ performance cores and SSD storage at the time of v0.1.0 (the operator's class of machine). The 120-second budget is not held against constrained CI runners; CI may grant more headroom but should remain under five minutes.
- The before_specify git hook has already created branch `025-lifecycle-harness`. The spec directory is `specs/025-lifecycle-harness/`. The plan, tasks, and implementation phases will populate this directory.
