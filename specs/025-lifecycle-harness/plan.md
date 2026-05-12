# Implementation Plan: Lifecycle Integration Harness (SDD-25)

**Branch**: `025-lifecycle-harness` | **Date**: 2026-05-12 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/025-lifecycle-harness/spec.md`

## Summary

SDD-25 delivers the integration test suite that owns AC-10 (15 lifecycle scenarios) and lifts AC-9 (test-infra completeness). The suite lives under `tests/integration/`, gated by `//go:build integration`, and exercises every real `internal/*` package end-to-end with only the external boundaries mocked (Discord via `testutil.DiscordStub`, validator upstreams via `httptest.Server`, NTP via the existing `ClockSyncProbe` seam, wall clock via injected `func() time.Time`). Each of the 15 scenarios is a single `Test_Scenario_NN_<slug>` function asserting four invariants in order: final state, audit-event subsequence, status-socket JSON shape (supervisor scenarios only), and sentinel-marker absence across every captured byte stream. The suite is the AC-10 owner of record — every other chunk's tests are unit- or fuzz-level; this is the single place where the system is proven to work as a whole.

## Technical Context

**Language/Version**: Go 1.26.1 (the `go.mod` floor; CGO_ENABLED=0)
**Primary Dependencies**: stdlib (`net/http/httptest`, `log/slog`, `testing`, `context`, `sync`, `runtime`, `time`), `github.com/stretchr/testify` (already in `go.mod`). **No new direct `go.mod` dependency.** Reuses `internal/testutil` (SDD-04), every locked `internal/*` package surface (SDD-01 through SDD-23), and the existing adapter pattern from `internal/server/claim_handler_integration_test.go::stubAsApprover`.
**Storage**: Per-scenario `t.TempDir()` for vault file, audit log, status socket, pidfile, and state directory. **Never** touches `~/.hush/` (FR-025-22). All disk artifacts are removed by `t.Cleanup`.
**Testing**: `go test -race -tags=integration ./tests/integration/...`. Suite-level serial execution (no `t.Parallel` at the top level); scenarios may spawn internal goroutines only where they own them with explicit termination per Constitution IX.
**Target Platform**: macOS arm64 and Linux amd64 (TESTING-STRATEGY.md §6 floor; SC-025-1).
**Project Type**: Go integration test tree (`tests/integration/`) sibling to `internal/` and `cmd/`. New top-level directory; not part of the production binary.
**Performance Goals**: Whole suite < 120 s wall-clock per run (SC-025-3); zero flakes across 5 consecutive runs (SC-025-2); zero race-detector findings (SC-025-4).
**Constraints**: No external network egress (SC-025-8 — passes on firewall-blocked host). No `time.Sleep` to drive any documented transition (FR-025-16). No new exported symbol added to any `internal/*` package (PACKAGE-MAP locks for SDD-01…SDD-23). No production-behavior stubs — only the four mocked boundaries listed in FR-025-12.
**Scale/Scope**: 15 scenario functions + 16th variant function (Scenario 9 grace) + 2-subtest function (Scenario 10 Interactive/Supervisor) = 17 top-level `Test_Scenario_*` symbols total. Six harness files (~250-400 LOC each, ~1.5-2 KLOC harness total) + two test files (`lifecycle_test.go` ~150 LOC, `scenarios_test.go` ~1.0-1.4 KLOC).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution’s eleven principles are evaluated against this chunk’s deliverable. **Result: PASS, zero violations, zero entries in Complexity Tracking.**

| Principle | Status | Disposition |
|-----------|--------|-------------|
| **I. Zero Files at Rest on Agent Machines** | ✅ pass | Harness state lives in `t.TempDir()`; never touches `~/.hush/`. Sentinels are injected as plaintext secrets ONLY into the test vault’s in-memory representation; `AssertSentinelAbsent` proves the byte never reaches disk outside the vault’s AES-GCM ciphertext. |
| **II. Approval is Human, Approval is Phone** | ✅ pass | `testutil.DiscordStub` substitutes for the human; FR-025-12 forbids any alternative auto-approve path. Scenario 10 explicitly asserts the 503/fail-closed contract. |
| **III. Defense in Depth Through Crypto Layering** | ✅ pass | Every layer is exercised end-to-end by the real packages — Argon2id → BIP32 → ES256K JWT → ECIES envelope → signed `/claim` → audit chain. The harness adds no new crypto surface and replaces no layer with a stub. |
| **IV. Supervisor for Daemons, Wrap-Shell for Humans** | ✅ pass | Scenarios 2-15 drive real `internal/supervise.Store`+`Child`+`Refiller`+`Refresher`+`Grace`+`StatusServer`+`PidFile`. Scenario 1 is the only interactive-shell flow; the rest are supervisor flows. |
| **V. Staleness is Visible, Failure is Loud** | ✅ pass | Every scenario whose documented outcome is an alert asserts the alert-payload shape against the mocked Discord. Status-socket assertions on supervisor scenarios prove the operator-visible freshness API works. Audit subsequence assertions prove every documented event appears in the documented order. |
| **VI. Tailscale-Only, Never Public** | ✅ pass | The real `internal/server` is wired in-process via `httptest.Server` over a local listener; the `InterfaceLister` and `Listener` seams on `server.Deps` already accept test injections (the existing `internal/server/integration_test.go` proves this). No production-bind seam is loosened. |
| **VII. CLI Design Standards** | ✅ pass | The harness is a Go test tree, not a binary. The two CLI scenarios that drive `hush supervise`/`client` execute the cobra command tree in-process (the existing `internal/cli/supervise_integration_test.go` is the template). No new global flag, exit code, or noun-verb surface is added. |
| **VIII. Testing Discipline** | ✅ pass — **load-bearing** | This chunk **is** the AC-10 evidence. Every scenario has an audit-ordering assertion (FR-025-23), a final-state assertion (FR-025-6), a status-socket shape assertion (FR-025-8, supervisor scenarios), and a sentinel-absence assertion (FR-025-9). Suite ships with the race detector enabled (FR-025-18, SC-025-4). |
| **IX. Idiomatic Go Discipline** | ✅ pass — **load-bearing** | No `init()`, no package-level mutable globals in the harness. Every goroutine the harness spawns has an owner + ctx + termination condition + top-frame `recover()`. The goroutine-leak detector in `harness/supervisor.go` snapshots `runtime.NumGoroutine()` pre/post each scenario and fails on leaks (FR-025-20, SC-025-5). |
| **X. Observability & Redaction** | ✅ pass — **load-bearing** | The `harness/log_capture.go` `AssertSentinelAbsent` helper runs over every captured byte stream the scenario produces (operational `slog` records, audit JSONL after redaction, status-socket bytes, Discord-stub alert payloads, captured stdout/stderr, returned error message strings). Every secret in every scenario is constructed via `testutil.SentinelSecret(n)`. |
| **XI. Native-First, Minimal Dependencies, Ephemeral Vault** | ✅ pass | Zero new direct `go.mod` dependencies. The suite consumes the existing `stretchr/testify` (in module since SDD-01) for assertions and the stdlib for everything else. No new crypto library, no new external service binding. |

**Re-check after Phase 1 design**: revisited at the end of this document — still PASS, zero violations.

## Project Structure

### Documentation (this feature)

```text
specs/025-lifecycle-harness/
├── plan.md              # This file
├── research.md          # Phase 0 — design decisions + rejected alternatives
├── data-model.md        # Phase 1 — harness types + scenario entity model
├── quickstart.md        # Phase 1 — how to run + how to add a scenario
├── contracts/
│   ├── harness-api.md           # Phase 1 — exported harness builder API (signatures evolve; doc the contract)
│   └── scenario-assertions.md   # Phase 1 — the 4 mandatory assertion contracts every scenario MUST satisfy
└── tasks.md             # Phase 2 — produced by /speckit-tasks, NOT this command
```

### Source Code (repository root)

```text
tests/
└── integration/                       # NEW — only place AC-10 lives
    ├── harness/                       # NEW Go package: tests/integration/harness
    │   ├── vault.go                   # TestVault helper: temp-dir vault via testutil.NewTestVault
    │   │                              # + state-dir setup + clients.json fixture
    │   ├── server.go                  # TestServer: real internal/server in-process (httptest.Server-backed),
    │   │                              # validator-upstream httptest mocks (Anthropic/OpenAI/GitHub/Google AI),
    │   │                              # stubAsApprover adapter (DiscordStub → server.Approver),
    │   │                              # ClockSyncProbe + InterfaceLister + Listener seam injections
    │   ├── discord.go                 # TestDiscord wrapping testutil.DiscordStub:
    │   │                              # programmable approval / connectivity / rate-limit sequences,
    │   │                              # alert-payload recorder (interface-typed; production discord.Alerts
    │   │                              # will satisfy it when SDD-28 lands)
    │   ├── supervisor.go              # TestSupervisor: composes real internal/supervise primitives
    │   │                              # (Store, NewRefiller, NewRefresher, NewGrace, NewStatusServer,
    │   │                              # AcquirePidFile, NewChild), drives transitions, controllable clock
    │   │                              # (func() time.Time), goroutine-leak detector via runtime.NumGoroutine
    │   │                              # pre/post snapshot, audit subsequence assertion helper,
    │   │                              # status-socket-client helper (dial+read+unmarshal FR-12 JSON)
    │   ├── child.go                   # TestChild: programmable child process via a small
    │   │                              # //go:build integration test-helper binary (cmd/integration-child)
    │   │                              # OR via os.Executable() re-invocation pattern. Exit-code, lifetime,
    │   │                              # stdout/stderr-pattern emission controllable per scenario.
    │   ├── log_capture.go             # slog handler chain capturing every record to a sync.Buffer;
    │   │                              # AssertSentinelAbsent(t, streams...) covers operational log +
    │   │                              # audit JSONL + status-socket bytes + Discord-stub alert payloads +
    │   │                              # captured stdout/stderr + error message strings
    │   └── doc.go                     # Package doc; build-tag explanation
    ├── lifecycle_test.go              # Suite-wide TestMain (no t.Parallel at top); each
    │                                  # Test_Scenario_NN_<slug> is declared here as the
    │                                  # canonical entry point delegating to one builder in scenarios_test.go
    └── scenarios_test.go              # The 15 scenarios' bodies. Implementations per FR-025-3..FR-025-10.
                                       # File MAY split into scenarios_a_test.go, _b_test.go if a single
                                       # 1.4-KLOC file becomes unreadable; the build-tag and package stay
                                       # identical. (Default: single file; split only on demonstrated need.)
```

**Structure Decision**: New top-level `tests/integration/` tree per the chunk contract. The harness lives in a sub-package (`tests/integration/harness`) so test files can `import "github.com/mrz1836/hush/tests/integration/harness"` and the harness’s exported types are addressable. All test files (`*_test.go`) AND every harness file are gated by `//go:build integration`; without the tag, `go test ./...` compiles zero files in this tree (SC-025-11). Package declaration: harness files declare `package harness`; the two test files declare `package integration_test` to avoid leaking test-only symbols.

The chunk contract enumerates the harness files; we follow it verbatim. **Clock seam, validator upstream HTTP mocks, audit subsequence helper, status-socket client, and goroutine-leak detector are threaded into the six listed harness files** rather than spawning new files. Placement rationale is in [research.md](research.md#harness-file-allocation). The optional `cmd/integration-child` helper-binary lives at `tests/integration/harness/cmdchild/main.go` if the os.Executable() approach proves insufficient for some scenario — preference is the os.Executable() approach, decided in research.md.

## Complexity Tracking

> **Empty by design.** No Constitution Check violations triggered, so no entries.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none)    | (none)     | (none)                              |

---

## Phase 0: Outline & Research → [research.md](research.md)

**Unknowns extracted from Technical Context**: zero `NEEDS CLARIFICATION` markers — the chunk contract + spec already lock every choice. The research document captures the design decisions the plan implies (file allocation, clock-injection strategy for the Refresher, audit-subsequence algorithm, goroutine-leak detection approach, validator-mock pattern, child-process construction approach, and status-socket-client approach), each with rationale and the alternative rejected.

## Phase 1: Design & Contracts

**Prerequisites**: research.md complete (see above).

1. **Entities** → [data-model.md](data-model.md). The "data model" here is the harness type model + the canonical Scenario entity (the named test, its documented events, its documented final state, its documented audit ordering, its documented status-socket projection, and its sentinel).

2. **Interface contracts** → [contracts/](contracts/):
   - [contracts/harness-api.md](contracts/harness-api.md) — exported builder signatures consumers depend on (`harness.NewSupervisor(t, …)`, `harness.NewDiscord(t)`, `harness.NewServer(t, …)`, `harness.NewChild(t, …)`, `harness.NewVault(t, …)`, `harness.AssertSentinelAbsent(t, …)`, `harness.AssertAuditSubsequence(t, recorded, documented)`, `harness.StatusSocketGet(t, path)`). Signatures evolve as new scenarios surface needs; this document captures the contract, not the literal Go signatures, per SDD-25’s PACKAGE-MAP entry.
   - [contracts/scenario-assertions.md](contracts/scenario-assertions.md) — the four mandatory assertion contracts every `Test_Scenario_NN_<slug>` function MUST satisfy (FR-025-6, FR-025-7, FR-025-8, FR-025-9). Includes the canonical assertion helper names + the exact byte-stream coverage list for sentinel-absence (FR-025-26).

3. **Agent context update** — the `<!-- SPECKIT START -->`…`<!-- SPECKIT END -->` block in `/Users/mrz/projects/hush/CLAUDE.md` is updated to point at this plan.

---

## Re-check Constitution Check (post-design)

Re-evaluated against the file layout, harness file allocation, clock-injection strategy, and assertion contracts decided above:

| Principle | Status |
|-----------|--------|
| I  | ✅ unchanged — temp-dir state only |
| II | ✅ unchanged — DiscordStub is the only approval seam |
| III | ✅ unchanged — every layer exercised end-to-end |
| IV | ✅ unchanged — real supervise primitives drive supervisor scenarios |
| V  | ✅ unchanged — every alert/audit/status assertion is mandatory |
| VI | ✅ unchanged — existing test seams reused; no production bind loosened |
| VII | ✅ unchanged — no CLI surface added |
| VIII | ✅ unchanged — TDD enforced; suite is the AC-10 contract |
| IX | ✅ unchanged — goroutine-leak detector + explicit goroutine lifecycle on harness |
| X  | ✅ unchanged — `AssertSentinelAbsent` covers all captured streams |
| XI | ✅ unchanged — zero new go.mod deps |

**Result: PASS, no Complexity Tracking entries required.**

---

## Stop & Report

- **Branch**: `025-lifecycle-harness`
- **IMPL_PLAN**: `/Users/mrz/projects/hush/specs/025-lifecycle-harness/plan.md`
- **Phase 0 artifact**: `specs/025-lifecycle-harness/research.md`
- **Phase 1 artifacts**: `specs/025-lifecycle-harness/data-model.md`, `specs/025-lifecycle-harness/contracts/harness-api.md`, `specs/025-lifecycle-harness/contracts/scenario-assertions.md`, `specs/025-lifecycle-harness/quickstart.md`
- **Agent context**: `CLAUDE.md` SPECKIT marker updated to this plan.

Phase 2 (`tasks.md`) is produced by `/speckit-tasks` in a fresh session, per SDD-25 Prompt 4.
