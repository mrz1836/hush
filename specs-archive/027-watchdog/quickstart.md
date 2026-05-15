# Quickstart — SDD-27 (`internal/supervise/watchdog`)

**Feature:** 027-watchdog
**Date:** 2026-05-13

This file is the operator-of-this-feature's runbook. It enumerates
the test commands that prove the chunk works, the lifecycle scenario
it covers (Scenario 15), and the order in which a reader should
engage with the artifacts. Read top to bottom; everything between
fenced blocks is a copy-paste command.

---

## 1. Read the contract

In order:

1. [spec.md](./spec.md) — WHAT (3 user stories, 15 functional requirements, 6 success criteria)
2. [research.md](./research.md) — WHY each implementation decision was made (R-001..R-016)
3. [data-model.md](./data-model.md) — locked struct shapes + 20 invariants (W-1..W-20)
4. [contracts/api.go](./contracts/api.go) — typed-mirror of the locked Go signatures
5. [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) — black-box behavior spec (B-W-1..B-W-25)

The constitution check is in [plan.md § Constitution Check](./plan.md);
all four in-scope principles (V, VIII, IX, X) pass. Constitution XI
(dependencies) is verified by `TestWatchdog_ZeroNewDependencies`.

---

## 2. Lifecycle-scenario coverage map

The chunk's tests cover exactly one [LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md)
row: **Scenario 15 — log-pattern watchdog sees auth failure string**.
This is the alert-only signal documented in `docs/LIFECYCLE-SCENARIOS.md`
§Scenario 15:

> 1. child emits a known auth-failure log line
> 2. watchdog matches the configured pattern
> 3. supervisor emits `[STALE] Log Pattern Match` alert
> 4. **no restart decision is made based on the log alone**
> 5. operator investigates or waits for validator/exit-78 confirmation

This chunk owns steps 1, 2, and the *emission half* of step 3. The
routing of the emitted `Event` into the `[STALE] Log Pattern Match`
Discord alert is SDD-28's responsibility; this chunk's tests stop at
"a typed `Event` is observable on the alerts channel". Step 4
("no restart decision") is verified here by `TestWatchdog_NeverTransitionsState`
(B-W-17) and end-to-end in the lifecycle integration suite (SDD-25,
Test_Scenario_15_LogPatternMatch).

| Spec Story | Spec FRs | Test names | LIFECYCLE-SCENARIOS row |
|-----------|----------|------------|-------------------------|
| Story 1 — Early-warning alert | FR-001, FR-002, FR-004, FR-013 | `TestWatchdog_PatternMatchEmitsAlert`, `TestWatchdog_NoMatchNoAlert`, `TestWatchdog_PerPatternBudgetIsolation` | Scenario 15 step 1+2 |
| Story 2 — No spam under tight loop | FR-004, FR-005, FR-006, FR-011 | `TestWatchdog_RateLimitBlocksExcess`, `TestWatchdog_BucketRefillsAfterInterval`, `TestWatchdog_AlertOutputSaturatedDropsWARN` | Scenario 15 step 3 ("alert is the limit, not the trigger") |
| Story 3 — Alert-only (never state change) | FR-003 | `TestWatchdog_NeverTransitionsState` | Scenario 15 step 4 |

---

## 3. Test commands

```sh
# Unit + race + coverage gate (the v0.1.0 gate)
go test -race -cover ./internal/supervise/watchdog/

# Cover threshold ≥ 90% (chunk-doc target, plan §Constitution Check VIII)
go test -coverprofile=/tmp/wd.cover ./internal/supervise/watchdog/
go tool cover -func=/tmp/wd.cover | tail -1
# expected: total: (statements) >= 90.0%

# Run just the named watchdog tests under the supervise package run:
go test -race -run Watchdog ./internal/supervise/...

# Full repo gate (pre-commit gate; also runs as part of /speckit-implement Prompt 5)
magex format:fix && magex lint && magex test:race
```

The race-clean assertion is non-negotiable per Constitution VIII /
Plan §Constitution Check.

---

## 4. Mandatory test list (per /speckit-tasks Phase 4)

These tests MUST be written BEFORE the implementation code per
Constitution VIII (TDD-mandatory). They are sourced from
[contracts/observable-behaviors.md](./contracts/observable-behaviors.md)
and pinned by [data-model.md §3 W-1..W-20](./data-model.md#7-test-driven-invariants-summary).

| # | Test name | Behavior contract | Spec FR / SC | File |
|---|-----------|-------------------|--------------|------|
|  1 | `TestWatchdog_NewWatchdog_ValidPatternSet` | B-W-1 | FR-001, FR-007 | `watchdog_test.go` |
|  2 | `TestWatchdog_NewWatchdog_DuplicatePatternName` | B-W-2 | FR-007a (Q5) | `watchdog_test.go` |
|  3 | `TestWatchdog_NewWatchdog_InvalidInputs` | B-W-2a | P-1, P-3, P-4 | `watchdog_test.go` |
|  4 | `TestWatchdog_PatternMatchEmitsAlert` | B-W-3 | FR-004 | `watchdog_test.go` |
|  5 | `TestWatchdog_NoMatchNoAlert` | B-W-4 | FR-001 | `watchdog_test.go` |
|  6 | `TestWatchdog_EmptyPatternSetIsBenign` | B-W-5 | FR-014 | `watchdog_test.go` |
|  7 | `TestWatchdog_MultipleMatchesOnSameLine` | B-W-6 | Edge Case | `watchdog_test.go` |
|  8 | `TestWatchdog_MultipleSpansSingleEmit` | B-W-7 | Edge Case | `watchdog_test.go` |
|  9 | `TestWatchdog_RateLimitBlocksExcess` | B-W-8 | FR-004, FR-005, FR-006 (Q2) | `watchdog_test.go` |
| 10 | `TestWatchdog_BucketRefillsAfterInterval` | B-W-9 | FR-004 | `watchdog_test.go` |
| 11 | `TestWatchdog_PerPatternBudgetIsolation` | B-W-10 | FR-004 | `watchdog_test.go` |
| 12 | `TestWatchdog_IngestNonBlockingWhenQueueFull` | B-W-11 | FR-010a | `watchdog_test.go` |
| 13 | `TestWatchdog_QueueFullDropEpisodeOnceWARN` | B-W-12 | FR-010a (Q4) | `watchdog_test.go` |
| 14 | `TestWatchdog_AlertOutputSaturatedDropsWARN` | B-W-13 | FR-011 (Q2) | `watchdog_test.go` |
| 15 | `TestWatchdog_RunSingleShot` | B-W-14 | FR-009 (R-012) | `watchdog_test.go` |
| 16 | `TestWatchdog_RunStopsOnCtxCancel` | B-W-15 | SC-004, FR-009 | `watchdog_test.go` |
| 17 | `TestWatchdog_IngestAfterRunReturnIsNoop` | B-W-16 | FR-009 | `watchdog_test.go` |
| 18 | `TestWatchdog_NeverTransitionsState` | B-W-17 | FR-003, Constitution V | `watchdog_test.go` |
| 19 | `TestWatchdog_NoSecureBytesStringConversion` | B-W-18 | Constitution X | `watchdog_test.go` |
| 20 | `TestWatchdog_ZeroNewDependencies` | B-W-19 | Constitution XI | `watchdog_test.go` |
| 21 | `TestWatchdog_ConcurrentLogIngest` | B-W-21 | FR-010 | `watchdog_test.go` |
| 22 | `TestWatchdog_PrecompiledPatternsReused` | B-W-22 | FR-008, SC-006 | `watchdog_test.go` |
| 23 | `TestWatchdog_SC001_EmitLatencyUnder100ms` | B-W-23 | SC-001 | `watchdog_test.go` |
| 24 | `TestWatchdog_SatisfiesSuperviseInterface` | B-W-24 | R-003 | `watchdog_test.go` |

24 named tests; one test file (`watchdog_test.go`). Test helpers
(fake clock, recording slog handler, capacity-1 alert sink) live in
unexported scope in the same file (per the SDD-21 `helpers_test.go`
precedent — kept inline here because the helper count is small).

The mandatory tests in the chunk doc Prompt 4 (`TestWatchdog_PatternMatchEmitsAlert`,
`TestWatchdog_NoMatchNoAlert`, `TestWatchdog_RateLimitBlocksExcess`,
`TestWatchdog_NeverTransitionsState`, `TestWatchdog_RunStopsOnCtxCancel`,
`TestWatchdog_ConcurrentLogIngest`, `TestWatchdog_PrecompiledPatternsReused`)
map to test rows #4, #5, #9, #18, #16, #21, and #22 above — every
chunk-doc-mandated name appears verbatim in the list.

---

## 5. Constitution check at a glance

Detailed table in [plan.md § Constitution Check](./plan.md#constitution-check).
In-scope principles per chunk doc + user prompt: **V, VIII, IX, X**.
**XI** (dependencies) is verified by `TestWatchdog_ZeroNewDependencies`.

| Principle | Compliance proof |
|-----------|-------------------|
| V Staleness loud | WARN on every rate-limit drop (FR-005, FR-006) + WARN per alert-output drop (FR-011) + WARN per queue-full episode (FR-010a) + INFO on cancel-drop count (R-007); zero silent drops |
| VIII TDD + race + ≥90% | 24-test list above, all written BEFORE implementation; `go test -race -cover ≥90%` |
| IX Context, errors, no globals/init, goroutines | `Run(ctx)` first-param ctx; sentinels via `var Err... = errors.New(...)`; zero `init()`; one goroutine (matcher) with owner = Run caller, termination = ctx.Done() OR ErrAlreadyRan |
| X Observability + redaction | No `*SecureBytes` import; only `string(...)` site is non-secret line content (Event.Line); all WARN entries exclude line content per Clarification Q2 |
| XI Dependencies | Zero new direct deps; only `regexp`, `log/slog`, `context`, `time`, `sync`, `sync/atomic` (all stdlib) + the `supervise.Watchdog` interface for the compile-time guard |

---

## 6. Post-implement checklist (SDD-27 Prompt 5)

The implementation phase commits these doc updates alongside the
code in a single combined commit:

| File | Change |
|------|--------|
| [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) `internal/supervise/` | Append "Exported API — locked at SDD-27" subsection at the sub-package path `github.com/mrz1836/hush/internal/supervise/watchdog`, listing Pattern, Event, Watchdog, NewWatchdog, Ingest, Run, OnStderrLine, and seven sentinel errors |
| [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-10 row | Append the 24 test file paths (`internal/supervise/watchdog/watchdog_test.go`) for the watchdog-alert subset |
| [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md) SDD-27 row | Mark status `done` |
| [specs/027-watchdog/tasks.md](./tasks.md) | Already updated by /speckit-tasks (Phase 4); included in the combined commit |

Combined commit message: `feat(supervise): log-pattern watchdog (alert-only) (SDD-27)`.

---

## 7. Out of scope for this chunk

- **Discord routing of the emitted Event.** Routing lives in SDD-28
  (alert-class catalogue + tiered routing). The watchdog stops at
  channel emit.
- **Audit log entry for the alert.** The audit row is written by
  SDD-28's router when it routes the alert (Clarification Q1).
  This chunk's tests do NOT inspect the audit log.
- **Pattern compilation from operator strings.** Compilation happens
  at SDD-23 CLI wiring time (post-merge), not in the watchdog
  package. The watchdog consumes pre-compiled `*regexp.Regexp` values.
- **Per-pattern regex complexity vetting.** Catastrophic backtracking
  is the config-load path's concern (spec Edge Cases row).
- **Persistent rate-limit budgets.** All budgets are in-process
  memory only (FR-015); restart resets every bucket to full.
- **Multi-watchdog coordination.** One watchdog per supervisor; no
  cross-instance state.

---

## 8. Cross-references

| Resource | Path |
|----------|------|
| Constitution | [.specify/memory/constitution.md](../../.specify/memory/constitution.md) |
| Spec | [spec.md](./spec.md) |
| Phase 0 research | [research.md](./research.md) |
| Phase 1 data model | [data-model.md](./data-model.md) |
| Phase 1 contracts | [contracts/api.go](./contracts/api.go) · [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) |
| Chunk doc | [docs/sdd/SDD-27.md](../../docs/sdd/SDD-27.md) |
| Lifecycle scenarios | [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §Scenario 15 |
| Package map (target) | [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) `internal/supervise/` |
| Config schema | [docs/CONFIG-SCHEMA.md](../../docs/CONFIG-SCHEMA.md) §`[watchdog]` |
| Existing interface contract | [internal/supervise/lifecycle_interfaces.go](../../internal/supervise/lifecycle_interfaces.go) §`type Watchdog interface { OnStderrLine(...) }` |
| Existing stderr plumbing | [internal/supervise/lifecycle_child.go](../../internal/supervise/lifecycle_child.go) §`lineSplittingWriter` |
| Existing alert-class enum | [internal/supervise/lifecycle_interfaces.go:71](../../internal/supervise/lifecycle_interfaces.go#L71) `AlertClassLogPatternMatch` |
