# Implementation Plan: SDD-25 Lifecycle Integration Harness

**Branch**: `025-lifecycle-harness` | **Date**: 2026-05-12 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification at `/Users/mrz/projects/hush/specs/025-lifecycle-harness/spec.md`
**Chunk doc**: [`docs/sdd/SDD-25.md`](../../docs/sdd/SDD-25.md) — locked

## Summary

Build the integration test suite that proves Acceptance Criterion **AC-10** ("Supervisor lifecycle — 15 named scenarios") for hush v0.1.0. Sixteen documented operational scenarios from [`docs/LIFECYCLE-SCENARIOS.md`](../../docs/LIFECYCLE-SCENARIOS.md) become **17 named Go test functions** (`Test_Scenario_NN_<slug>`) under `tests/integration/`, build-tagged `//go:build integration`. Each function drives the **real** `internal/supervise.Lifecycle` (the SDD-24 orchestrator) plus real `internal/server`, `internal/audit`, `internal/token`, `internal/vault`, `internal/transport/{ecies,sign}`, `internal/keys`, and `internal/cli` packages in-process. Discord, the five provider validator endpoints, the wall clock, and the Tailscale-reachability probe are the only four boundaries replaced with programmable in-process stubs (via `internal/testutil` + `net/http/httptest` + the existing `Deps.Clock`/`Deps.TailscaleProbe`/`Deps.NowFn` injection seams that SDD-24 already exposes).

Every scenario asserts **four contracts** (per `contracts/scenario-assertions.md`):
1. Final supervisor / interactive state matches `docs/LIFECYCLE-SCENARIOS.md`.
2. Audit JSONL contains the documented events in the documented relative order (intervening events tolerated; hash-chain continuity verified via `audit.Verify`).
3. (Supervisor scenarios only) Status-socket JSON conforms to SPEC.md §FR-12 and the documented per-scenario projection.
4. `AssertSentinelAbsent` succeeds over six captured byte streams (operational `slog`, audit JSONL, status-socket response, Discord alert payloads, child stdout/stderr, scenario error strings).

Suite gates: **17/17 green, < 120s wall-clock, 5-consecutive-runs flake-free under `-race`**.

The harness is internally composed of six files (`vault.go`, `server.go`, `discord.go`, `supervisor.go`, `child.go`, `log_capture.go`) per the SDD-25 chunk-doc locked inventory. The 17 scenario implementations live together in `scenarios_test.go`; suite-wide setup (`TestMain` if needed, the canonical sentinel, the integration-child-mode entry point recognised by `os.Executable()` re-invocation) lives in `lifecycle_test.go`. All files build only with `-tags=integration`.

This chunk is the **explicit AC-10 owner-of-record**. The implement-phase commit updates [`docs/AC-MATRIX.md`](../../docs/AC-MATRIX.md) AC-10 (test path) and AC-9 (test-infra completeness) rows with the 17 test function names, appends a new entry to [`docs/PACKAGE-MAP.md`](../../docs/PACKAGE-MAP.md) for `tests/integration/`, and marks SDD-25 status `done` in [`docs/SDD-PLAYBOOK.md`](../../docs/SDD-PLAYBOOK.md).

## Technical Context

**Language/Version**: Go 1.24 (toolchain pinned in `go.mod`).
**Primary Dependencies**: stdlib only — `testing`, `net/http/httptest`, `net`, `log/slog`, `crypto/ecdsa`, `runtime`, `sync`, `time`, `os`, `os/exec`. Plus in-module packages: `internal/testutil`, `internal/supervise`, `internal/server`, `internal/audit`, `internal/token`, `internal/vault`, `internal/vault/securebytes`, `internal/transport/sign`, `internal/transport/ecies`, `internal/keys`, `internal/supervise/config`, `internal/cli` (for any CLI seams the scenarios exercise).
**No new direct `go.mod` deps**: Constitution XI forbids it without justification; none required.
**Storage**: Per-scenario `t.TempDir()` ephemeral state (vault file, audit JSONL, pidfile, status socket); none shared across scenarios.
**Testing**: `go test -race -tags=integration ./tests/integration/...` (driven via `magex test:race -tags=integration`).
**Target Platform**: macOS arm64 + Linux amd64 (the v0.1.0 OS matrix); status-socket-path resolution bypassed via explicit absolute paths in `cfg.StatusSocket` so neither OS-default code path is exercised by scenarios.
**Project Type**: Single project (Go module). New top-level directory `tests/integration/` with one sub-package `tests/integration/harness/`.
**Performance Goals**: Suite < 120 s wall-clock on a developer-class machine (M-series Mac / Linux amd64 ≥ 8 cores). Budget breakdown in research.md §R10.
**Constraints**:
- Zero outbound network egress to any non-loopback host (spec FR-012 + SC-004).
- Zero `t.Parallel` at scenario level (spec FR-022).
- Zero `t.Skip` (spec FR-001).
- Zero `time.Sleep` to drive a documented transition (use `runtime.Gosched()` bounded poll + injectable clock).
- Zero secret-bytes coercion outside the two existing Constitution-X-permitted sites already audited in SDD-24.
- Zero new exported methods on locked SDD-19..24 types (research.md §R7 resolves the clock seam without touching locks).
**Scale/Scope**: 17 test functions × ~50–80 LOC each ≈ 1300 scenario LOC; 6 harness files × ~120 LOC each ≈ 720 harness LOC; suite-wide setup ≈ 200 LOC. Total ≈ 2200 LOC of build-tagged test code.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

Constitutional principles in scope (from spec.md Dependencies): **VIII** (Testing Discipline — TDD-mandatory; the gate enforced by AC-9/AC-10), **V** (Staleness is Visible — every documented alert must be operator-observable and the suite proves it), **IX** (Idiomatic Go Discipline — no goroutine leaks, no global mutable state, every goroutine owner+ctx+termination+recover), **X** (Observability & Redaction — sentinel-absent assertion is the integration-level proof of redaction).

Each principle is evaluated against this plan's design (the six-file harness + 17 scenarios + four-contract assertion shape).

### Principle II (Approval is Human, Approval is Phone)

- This is a test-only deliverable; no production approval path changes.
- The Discord stub (`testutil.DiscordStub`) is a programmable substitute, NOT an auto-approve. Each scenario explicitly scripts approve/deny decisions; the no-script tail behaviour (`ApproveAll`) only fires when the scenario explicitly opts in.
- **No production code path that "could auto-approve" is added.** The server's real `Approver` interface is fed through an adapter that translates `server.ApprovalRequest` ↔ `testutil.ApprovalRequest`; the adapter contains zero policy logic.
- **PASS.**

### Principle V (Staleness is Visible, Failure is Loud)

- Spec FR-023 mandates every alert-emitting scenario asserts the corresponding Discord alert payload reached the stub with the documented `AlertClass`.
- Spec FR-024 mandates the `[STALE] …` alerts are visually distinct in the captured Discord message stream.
- The data-model.md §2 table makes each scenario's expected alert visible; the harness's `AssertAuditSubsequence` covers the corresponding `supervisor_stale_alert` audit emission.
- Scenario 15 (log-pattern watchdog) explicitly asserts that an alert fires AND the state machine does NOT transition — the alert-only contract.
- **PASS.**

### Principle VIII (Testing Discipline)

- AC-10 maps directly to this chunk; spec FR-001 forbids skipping any scenario.
- Per-scenario assertions cover state, audit-order, status-socket shape, and sentinel-absence — the four contracts encode the AC's full surface.
- Test names follow the locked pattern from `.github/tech-conventions/testing-standards.md` (PascalCase, `Test_<Function>_<Scenario>` shape).
- Race detector mandatory (spec FR-011); 5-flake gate per FR-010.
- Coverage gate: SDD-25's PRIMARY contribution to `internal/supervise` coverage is the end-to-end exercise of `Lifecycle.Run` through every documented path. The chunk doc's `Coverage target: 15/15 scenarios green` is the test-count gate; spec SC-008 reports the line-coverage gate (≥ 95% on `internal/supervise` combining unit + integration coverage).
- No `t.Skip` permitted (spec FR-001); the harness exposes no skip helper (anti-API in `contracts/harness-api.md`).
- **PASS.**

### Principle IX (Idiomatic Go Discipline)

- Every harness builder registers `t.Cleanup` for teardown; every goroutine the harness OR the real `internal/*` packages spawn has an owner, a ctx, an explicit termination, and a top-frame `recover` (carried in by SDD-24 + asserted by `TestSupervisor.AssertNoGoroutineLeak`).
- No `init()` in any harness file (contract A in `contracts/harness-api.md`); the integration-child-mode dispatch in `lifecycle_test.go` uses `TestMain` (allowed by the `testing` package's contract) — not a Go `init()`.
- No package-level mutable state in the harness; the canonical sentinel constant is the only package-level identifier, declared `const`.
- `context.Context` is the first parameter of every harness method that does I/O.
- Consumer-side single-method interfaces: `Validator`, `Alerts`, `Watchdog` already exist in `internal/supervise/lifecycle_interfaces.go` (locked by SDD-24); the harness injects test impls of these without adding new interfaces.
- No `//go:build integration` files inside any production package (research.md §R7 resolves this without breaching the SDD-21 lock).
- One use of `reflect` is contemplated in research.md §R7 for ticker injection on `*Refresher`; final resolution avoids `reflect` by driving the refill callback directly. Plan therefore claims **zero `reflect` usage**.
- **PASS.**

### Principle X (Observability & Redaction)

- The four-contract per-scenario assertion shape ends with Contract D — `AssertSentinelAbsent` over six captured streams (data-model.md §4.6 list).
- The fixture vault uses `testutil.SentinelSecret(N)` plaintext bytes; the assertion fails on any leaked byte at the offset.
- The audit log is read by `os.ReadFile` and scanned for sentinels BEFORE any JSON-aware processing — a raw-byte sweep that catches even malformed leaks.
- The harness's `LogCapture` replaces `slog.Default()` for the duration of each scenario via `slog.SetDefault` inside `t.Cleanup` (the existing harness sets `slog.SetDefault` at construction and restores on cleanup — already a known-good pattern in the repo).
- No new `string(*SecureBytes)` site is introduced by this chunk. The two existing Constitution-X-permitted sites (JWT bearer header at `Snapshot.Token.Use` and child-env build at fork boundary) remain the only sites and are exercised by every scenario.
- **PASS.**

### Principle XI (Native-First, Minimal Dependencies, Ephemeral Vault)

- Zero new direct `go.mod` dependencies. The harness uses stdlib + already-in-module helpers exclusively.
- Per-scenario ephemeral vault directories under `t.TempDir()` — automatic OS-managed cleanup mirrors the "vault is ephemeral" production policy.
- No backup of any fixture vault; each test produces its own from in-memory secrets via `testutil.NewTestVault`.
- **PASS.**

### Initial gate verdict

**PASS** — no violations to record in Complexity Tracking. Plan proceeds.

## Project Structure

### Documentation (this feature)

```text
specs/025-lifecycle-harness/
├── plan.md                          # This file (/speckit-plan command output)
├── research.md                      # Phase 0 — decisions + rejected alternatives
├── data-model.md                    # Phase 1 — Scenario entity catalogue + harness types
├── quickstart.md                    # Phase 1 — developer entry points (run, debug, extend)
├── contracts/
│   ├── harness-api.md               # Phase 1 — locked behavioural contract (signatures illustrative)
│   └── scenario-assertions.md       # Phase 1 — locked four-contract assertion shape
├── checklists/                      # carried over from /speckit-clarify (if produced)
├── spec.md                          # /speckit-specify + /speckit-clarify output (locked)
└── tasks.md                         # /speckit-tasks output — NOT updated by /speckit-plan
```

### Source Code (repository root)

```text
hush/
├── internal/                        # production packages — UNMODIFIED by this chunk
│   ├── audit/
│   ├── cli/
│   ├── config/
│   ├── discord/
│   ├── keychain/
│   ├── keys/
│   ├── logging/
│   ├── server/
│   ├── supervise/                   # SDD-24 orchestrator lives here — driven, not modified
│   │   ├── lifecycle.go
│   │   ├── lifecycle_*.go
│   │   ├── config/
│   │   └── …
│   ├── testutil/                    # SDD-04 helpers — consumed via existing exported API
│   ├── token/
│   ├── transport/{ecies,sign}/
│   └── vault/{securebytes/}
└── tests/                           # NEW top-level test tree (created by this chunk)
    └── integration/
        ├── harness/                 # private sub-package, build-tagged //go:build integration
        │   ├── vault.go             # TestVault — fixture vault + clients.json registry
        │   ├── server.go            # TestServer — real internal/server in-process
        │   ├── discord.go           # TestDiscord — testutil.DiscordStub + adapter
        │   ├── supervisor.go        # TestSupervisor — composes Lifecycle Deps + drives Run
        │   ├── child.go             # TestChild — os.Executable() re-invocation
        │   └── log_capture.go       # LogCapture + AssertSentinelAbsent
        ├── lifecycle_test.go        # suite-wide setup; TestMain (if needed) for integration-child-mode dispatch
        └── scenarios_test.go        # the 17 Test_Scenario_NN_<slug> functions
```

Every file under `tests/integration/` (including the `harness/` sub-package) carries `//go:build integration` as the first non-comment line. Default `go test ./...` compiles zero files in this tree (spec FR-008).

**Structure Decision**: Single Go project. The new `tests/integration/` tree is one Go test package plus one private sub-package (`harness/`). Test files in `tests/integration/` live in `package integration_test` (the conventional external-test-package pattern, so the harness import is `harness "github.com/mrz1836/hush/tests/integration/harness"` and scenario tests never see harness internals); the harness sub-package is `package harness`. Build tags isolate everything from default builds. No production code is modified by this chunk.

## Phase 0 — Outline & Research

The Phase 0 output is [`research.md`](research.md). It records seven design decisions (harness file allocation, Refresher clock-injection strategy, audit-event subsequence assertion algorithm, goroutine-leak detection, validator-upstream HTTP mocks, programmable child-process construction, status-socket client) plus a cross-cutting principle audit. Every decision has a stated rationale and a rejected alternative. Zero `NEEDS CLARIFICATION` markers remain (all four candidates were resolved during `/speckit-clarify` and recorded in `spec.md §Clarifications`).

**Output**: `research.md` (already on disk from clarify-phase; unchanged by /speckit-plan).

## Phase 1 — Design & Contracts

**Prerequisites**: `research.md` complete (verified above).

### 1. Entities → `data-model.md`

The Phase 1 entity model is the *test-fixture* entity model. [`data-model.md`](data-model.md) captures:

- **§1 Scenario** — the canonical entity, with `Number`, `Slug`, `Source`, `Type`, `DocumentedFinalState`, `DocumentedAuditEvents`, `DocumentedStatusProjection`, `SentinelInjectionPoint`, `MockedBoundaries` fields. Validation rules tie each field back to a spec FR.
- **§2 The 17 Scenario test functions** — the normative catalogue. Each row supplies (test name verbatim from spec FR-002, type, final state, ordered audit events, status-socket-required flag). **This table is the FR-005 deferral resolution**: it is the canonical per-scenario audit-event ordering table that spec FR-005 said plan-phase would produce.
- **§3 Scenario 1 compound final-state** — the four-tuple from LIFECYCLE-SCENARIOS §1 (health flags, child exit code, token-store state, approval DM count).
- **§4 Harness types** — `TestVault`, `TestServer`, `TestDiscord`, `TestSupervisor`, `TestChild`, `LogCapture` — with method tables. Signatures are illustrative (per the SDD-25 PACKAGE-MAP entry contract); the behavioural contract is locked.
- **§5 Suite topology** — entity cardinality per scenario.

### 2. Contracts → `contracts/`

Two contract documents are locked in Phase 1:

- [`contracts/harness-api.md`](contracts/harness-api.md) — the **builder contract** every harness helper satisfies (cleanup-registered, isolated, fail-loud, no-external-network) and the **anti-API** list of helpers that MUST NOT exist (no `harness.Reset()`, no global state, no `harness.Sleep`, no `harness.SkipScenario`, no `harness.SuppressSentinelLeak`).
- [`contracts/scenario-assertions.md`](contracts/scenario-assertions.md) — the **four mandatory assertion contracts** every `Test_Scenario_NN_<slug>` function must satisfy (final state, audit subsequence, status-socket shape, sentinel-absence) and the **anti-shapes** that violate them.

The status-socket wire shape is NOT re-locked here — it is owned by SDD-22 and present in `internal/supervise/socket.go::statusJSON`. The harness's `AssertStatusShape` consumes the wire bytes and unmarshals into a *mirror* DTO local to the harness so the assertion is decoupled from any future SDD-22 wire changes.

### 3. `quickstart.md`

[`quickstart.md`](quickstart.md) is the developer's entry document: how to run the suite, run a single scenario, verify default-build invisibility, add a regression to an existing scenario, add a new harness builder, debug a failure, and avoid the common gotchas (`t.Parallel`, real upstream URLs, `time.Sleep` for documented transitions, "almost-passing" scenarios).

### 4. Agent context update

The active-feature pointer in [`CLAUDE.md`](../../CLAUDE.md) between `<!-- SPECKIT START -->` and `<!-- SPECKIT END -->` is updated to point at this plan, replacing the prior SDD-24 marker. The replacement is in this plan's commit (not a separate /speckit-plan side effect).

## Phase 2 — Tasks

Out of scope for `/speckit-plan`. `tasks.md` already exists from a prior `/speckit-tasks` run; future re-runs of `/speckit-tasks` will be informed by this plan's data-model.md §2 catalogue (the locked 17-row table) and the four-contract shape in `contracts/scenario-assertions.md`.

## Complexity Tracking

> **No Constitution Check violations.** This table is intentionally empty.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|--------------------------------------|
| *(none)* | — | — |

The chunk introduces zero new exported symbols on locked production packages, zero new `go.mod` dependencies, zero new init() functions, zero new package-level mutable state, zero new `string(*SecureBytes)` sites. Every gate passes by construction.

## Re-evaluated Constitution Check (post-Phase 1 design)

The five principles in scope (II, V, VIII, IX, X, XI) were each re-evaluated against the Phase 1 artefacts:

- **II**: data-model.md §4.3 (`TestDiscord`) confirms approve/deny decisions are scripted per-scenario; no auto-approve path is introduced.
- **V**: data-model.md §2 confirms every alert-emitting scenario has a matching `supervisor_stale_alert` audit row; `contracts/scenario-assertions.md` Contract B enforces it.
- **VIII**: data-model.md §2's 17 rows match spec FR-002 verbatim; `contracts/scenario-assertions.md` enforces no `t.Skip`, no soft assertions.
- **IX**: data-model.md §4.4 confirms `TestSupervisor.AssertNoGoroutineLeak` is registered via `t.Cleanup`; no harness goroutine outlives the test.
- **X**: data-model.md §4.6 enumerates the six captured streams; `contracts/scenario-assertions.md` Contract D requires `AssertSentinelAbsent` over all six.
- **XI**: harness uses stdlib + already-in-module packages only; verified by source-file imports listed in §Technical Context.

**Verdict**: **PASS post-design**. Plan is ready for `/speckit-tasks`.

## Cross-references

| Topic | Document |
|-------|----------|
| Spec | [spec.md](spec.md) |
| Research | [research.md](research.md) |
| Data model | [data-model.md](data-model.md) |
| Harness API contract | [contracts/harness-api.md](contracts/harness-api.md) |
| Assertion contract | [contracts/scenario-assertions.md](contracts/scenario-assertions.md) |
| Quickstart | [quickstart.md](quickstart.md) |
| Source-of-truth scenarios | [`docs/LIFECYCLE-SCENARIOS.md`](../../docs/LIFECYCLE-SCENARIOS.md) |
| AC-10 row to update at implement-phase | [`docs/AC-MATRIX.md`](../../docs/AC-MATRIX.md) |
| SDD chunk contract | [`docs/sdd/SDD-25.md`](../../docs/sdd/SDD-25.md) |
| Constitution | [`.specify/memory/constitution.md`](../../.specify/memory/constitution.md) |
