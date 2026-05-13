---

description: "Task list for SDD-27 — internal/supervise/watchdog (log-pattern alert-only)"
---

# Tasks: Log-Pattern Watchdog (Alert-Only)

**Input**: Design documents from `/specs/027-watchdog/`
**Prerequisites**: plan.md (✅), spec.md (✅), research.md (✅), data-model.md (✅), contracts/api.go (✅), contracts/observable-behaviors.md (✅), quickstart.md (✅)

**Tests**: MANDATORY per Constitution VIII (TDD-first). Every behaviour-contract test in this list MUST be written and observed FAILING before the corresponding implementation task is started. Coverage target ≥ 90% on the new sub-package.

**Organization**: Tasks are grouped by user story (P1 → P2 → P3 from spec.md) so each story can be implemented and tested as an independent vertical slice.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1, US2, US3) — required only for user-story phases
- Include exact file paths in descriptions
- ★ marks the seven chunk-doc-mandated test names from SDD-27 Prompt 4

## Path Conventions

Single Go module. New sub-package at `internal/supervise/watchdog/`. All source paths are relative to repo root (`/Users/mrz/projects/hush/`).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the sub-package directory and the two empty source files referenced by every subsequent task.

- [X] T001 Create directory `internal/supervise/watchdog/` (sub-package location locked by [research.md R-001](./research.md#r-001--package-location-internalsupervisewatchdog-sub-package-not-internalsupervise)).
- [X] T002 [P] Create `internal/supervise/watchdog/watchdog.go` with `package watchdog` header and the stdlib + `github.com/mrz1836/hush/internal/supervise` imports listed in [plan.md § Technical Context](./plan.md#technical-context) (no symbols yet).
- [X] T003 [P] Create `internal/supervise/watchdog/watchdog_test.go` with `package watchdog_test` header (black-box test package per `.github/tech-conventions/testing-standards.md`) and the `testing`, `context`, `regexp`, `sync`, `time`, `log/slog`, `bytes` imports plus `internal/supervise/watchdog` import (no test functions yet).

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Lay down every symbol the 24 tests will reference so the test file compiles even when the test bodies still fail. Constitution VIII TDD discipline requires tests to FAIL for the right reason (assertion mismatch) — not for "undefined identifier".

**⚠️ CRITICAL**: No user-story work can begin until this phase completes. Every test task in Phases 3–5 expects every symbol below to already exist as a stub.

- [X] T004 Declare the seven sentinel errors `ErrAlreadyRan`, `ErrEmptyPatternName`, `ErrDuplicatePatternName`, `ErrNilPatternRegex`, `ErrNonPositiveRateLimit`, `ErrNilAlertsChannel`, `ErrNilLogger` as `var Err... = errors.New(...)` in `internal/supervise/watchdog/watchdog.go` (per [data-model.md §3 sentinel-errors block](./data-model.md#sentinel-errors)).
- [X] T005 Declare exported types `Pattern`, `Event`, and `Watchdog` (struct shape only, no methods) in `internal/supervise/watchdog/watchdog.go` per [data-model.md §1–3 locked struct shapes](./data-model.md#1-pattern-exported).
- [X] T006 Declare unexported types `bucketState` and `dropEpisode` plus the unexported constant `lineChannelCapacity = 512` in `internal/supervise/watchdog/watchdog.go` per [data-model.md §3](./data-model.md#3-watchdog-exported-single-instance-single-run).
- [X] T007 Declare function stubs `NewWatchdog(patterns []Pattern, alerts chan<- Event, logger *slog.Logger) (*Watchdog, error)`, `(*Watchdog) Ingest(line []byte)`, `(*Watchdog) Run(ctx context.Context) error`, `(*Watchdog) OnStderrLine(ctx context.Context, line []byte)` returning zero values in `internal/supervise/watchdog/watchdog.go` so the test file compiles.
- [X] T008 Add the compile-time interface guard `var _ supervise.Watchdog = (*Watchdog)(nil)` at the end of `internal/supervise/watchdog/watchdog.go` (satisfies invariant W-20 and locks adapter contract from [research.md R-003](./research.md#r-003--adapter-method-watchdog-onstderrlinectx-line-beyond-chunk-doc-api)).
- [X] T009 [P] Implement shared in-file test helpers in `internal/supervise/watchdog/watchdog_test.go` — `newTestLogger()` returning `(*slog.Logger, *recordingHandler)` for WARN/INFO capture, `newFakeClock(t0 time.Time)` exposing `Now()` + `Advance(d)`, `setNowForTest(w *Watchdog, now func() time.Time)` (test-only seam per [data-model.md §3](./data-model.md#3-watchdog-exported-single-instance-single-run) "clock seam"), and `mustCompile(t *testing.T, pat string) *regexp.Regexp` (per [quickstart.md §4](./quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4) "Test helpers ... in unexported scope in the same file").

**Checkpoint**: `go build ./internal/supervise/watchdog/` succeeds. `go vet ./internal/supervise/watchdog/` is clean. No tests yet — that's the next phase.

---

## Phase 3: User Story 1 — Operator sees an early-warning alert (Priority: P1) 🎯 MVP

**Goal**: A configured pattern matches a fresh line → exactly one typed `Event` is observable on the alerts channel within 100 ms (spec FR-001, FR-002, FR-013, SC-001). Construction validates inputs; lifecycle is single-shot; cancellation is clean.

**Independent Test**: `go test -race -run 'TestWatchdog_(NewWatchdog|PatternMatchEmitsAlert|NoMatchNoAlert|EmptyPatternSet|Multiple|RunSingleShot|RunStopsOnCtxCancel|IngestAfterRun|PrecompiledPatternsReused|SC001)' ./internal/supervise/watchdog/` passes. After this phase, the alert-emission half of Scenario 15 works end-to-end against a unit harness.

### Tests for User Story 1 (TDD-mandatory — write BEFORE implementation) ⚠️

> **Each task below adds ONE failing test to `internal/supervise/watchdog/watchdog_test.go`. Run `go test -race ./internal/supervise/watchdog/` after each task and confirm the new test FAILS for the right reason before moving on.**

- [X] T010 [US1] Add `TestWatchdog_NewWatchdog_ValidPatternSet` (B-W-1) to `internal/supervise/watchdog/watchdog_test.go` — table-driven: empty slice → success (FR-014); 1 valid pattern → success; 3 valid patterns → success; assert returned `*Watchdog` is non-nil and `err` is nil.
- [X] T011 [US1] Add `TestWatchdog_NewWatchdog_DuplicatePatternName` (B-W-2) to `internal/supervise/watchdog/watchdog_test.go` — feed `[{Name:"auth", ...}, {Name:"auth", ...}]`; assert `errors.Is(err, watchdog.ErrDuplicatePatternName)` and returned `*Watchdog` is nil (FR-007a, Clarification Q5).
- [X] T012 [US1] Add `TestWatchdog_NewWatchdog_InvalidInputs` (B-W-2a) to `internal/supervise/watchdog/watchdog_test.go` — table-driven, one row per sentinel: empty Name → `ErrEmptyPatternName`; nil Regex → `ErrNilPatternRegex`; zero RateLimit → `ErrNonPositiveRateLimit`; nil alerts → `ErrNilAlertsChannel`; nil logger → `ErrNilLogger`; each assertion uses `errors.Is`.
- [X] T013 [US1] ★ Add `TestWatchdog_PatternMatchEmitsAlert` (B-W-3, REQUIRED) to `internal/supervise/watchdog/watchdog_test.go` — single pattern `401 Unauthorized`, RateLimit 1 hour, capacity-1 alerts channel; Ingest one matching line; assert exactly one `Event` arrives within 100 ms with `Event.Pattern == "auth-401"`, `Event.Line` equals the ingested line, `Event.Time` non-zero (FR-001, FR-002, FR-013, SC-001).
- [X] T014 [US1] ★ Add `TestWatchdog_NoMatchNoAlert` (B-W-4, REQUIRED) to `internal/supervise/watchdog/watchdog_test.go` — one pattern; Ingest a non-matching line; assert no `Event` arrives within 100 ms AND the recording slog handler captured zero WARN/INFO entries referencing the line (FR-001, FR-012).
- [X] T015 [US1] Add `TestWatchdog_EmptyPatternSetIsBenign` (B-W-5) to `internal/supervise/watchdog/watchdog_test.go` — `NewWatchdog(nil, ...)` succeeds; `Run` started; Ingest 100 lines; assert zero `Event`s and clean `Run` return on ctx cancel (FR-014).
- [X] T016 [US1] Add `TestWatchdog_MultipleMatchesOnSameLine` (B-W-6) to `internal/supervise/watchdog/watchdog_test.go` — two patterns ("401", "Unauthorized"), one line matching both; assert two distinct `Event`s within 100 ms, one per pattern.
- [X] T017 [US1] Add `TestWatchdog_MultipleSpansSingleEmit` (B-W-7) to `internal/supervise/watchdog/watchdog_test.go` — pattern `401`, line containing `401 ... 401 ... 401`; assert exactly one `Event` for the line (per-line × per-pattern semantics, spec Edge Cases).
- [X] T018 [US1] Add `TestWatchdog_RunSingleShot` (B-W-14) to `internal/supervise/watchdog/watchdog_test.go` — start `Run` once, cancel ctx, then call `Run(context.Background())`; assert second call returns `ErrAlreadyRan` immediately (no goroutine spawned) (R-012).
- [X] T019 [US1] ★ Add `TestWatchdog_RunStopsOnCtxCancel` (B-W-15, REQUIRED, race-clean) to `internal/supervise/watchdog/watchdog_test.go` — snapshot `runtime.NumGoroutine()` pre-Run; start Run; cancel ctx; assert Run returns within 250 ms (SC-004) AND `runtime.NumGoroutine()` returns to baseline within an additional 50 ms (no leaks, FR-009).
- [X] T020 [US1] Add `TestWatchdog_IngestAfterRunReturnIsNoop` (B-W-16) to `internal/supervise/watchdog/watchdog_test.go` — start Run, cancel ctx, wait for Run return; Ingest 100 more lines; assert zero `Event`s arrive AND zero WARNs reference the post-Run lines (R-009).
- [X] T021 [US1] ★ Add `TestWatchdog_PrecompiledPatternsReused` (B-W-22, REQUIRED) to `internal/supervise/watchdog/watchdog_test.go` — compile pattern via `regexp.MustCompile` once in test setup, capture the `*regexp.Regexp` pointer; ingest 10,000 lines; assert ZERO additional `regexp.Compile` calls happen during ingestion (FR-008, SC-006). Implementation note: assert by verifying the `Pattern.Regex` pointer held by the watchdog is `==` to the pointer the test supplied, AND that `regexp.MatchString` semantics are used (not re-compile) by snapshotting allocations via `testing.AllocsPerRun`.
- [X] T022 [US1] Add `TestWatchdog_SC001_EmitLatencyUnder100ms` (B-W-23) to `internal/supervise/watchdog/watchdog_test.go` — measure wall-clock between `Ingest` call and `Event` receipt over 100 trials; assert p100 < 100 ms (SC-001).

**Checkpoint A (tests-fail)**: `go test -race ./internal/supervise/watchdog/` shows all 13 US1 tests FAILING with assertion mismatches (not compile errors). Proceed to implementation.

### Implementation for User Story 1

- [X] T023 [US1] Implement `NewWatchdog` in `internal/supervise/watchdog/watchdog.go` — input validation (P-1..P-4 invariants, nil-alerts, nil-logger), defensive copy of patterns slice, initialize `lines = make(chan []byte, lineChannelCapacity)`, initialize `buckets` with tokens=1 + lastRefill=now() for every pattern, set `now = time.Now`. Wrap every sentinel via `fmt.Errorf("watchdog: ... : %w", sentinel)`. Makes T010, T011, T012 pass.
- [X] T024 [US1] Implement `Ingest` in `internal/supervise/watchdog/watchdog.go` — early-return when `cancelled.Load()`; defensive copy `dup := append([]byte(nil), line...)`; non-blocking `select { case w.lines <- dup: default: /* drop bookkeeping deferred to US2 */ }`; thread-safe under multi-producer load. Makes T013, T014, T015, T016, T017 partial.
- [X] T025 [US1] Implement `OnStderrLine` adapter in `internal/supervise/watchdog/watchdog.go` — single-line: `func (w *Watchdog) OnStderrLine(_ context.Context, line []byte) { w.Ingest(line) }` (R-003).
- [X] T026 [US1] Implement `Run` skeleton in `internal/supervise/watchdog/watchdog.go` — CAS-guarded single-shot via `w.ran.CompareAndSwap(false, true)` returning `ErrAlreadyRan`; spawn one matcher goroutine with `defer recover` (panic hygiene per Constitution IX); main loop selects `<-w.lines` and `<-ctx.Done()`; on ctx.Done drop pending lines, set `w.cancelled.Store(true)`, INFO-log drop count, return `fmt.Errorf("watchdog: run cancelled: %w", ctx.Err())`. Makes T018, T019, T020 pass.
- [X] T027 [US1] Implement matcher inner loop in `internal/supervise/watchdog/watchdog.go` — for each line, iterate `w.patterns`; for each pattern call `pat.Regex.Match(line)`; on match, construct `Event{Pattern: pat.Name, Line: string(line), Time: w.now()}` and non-blocking-send to `w.alerts`. Rate-limit + saturation handling are stubs returning to the simple "always emit if budget present" path (full token-bucket logic comes in US2). Makes T013, T016, T017, T021, T022 pass.

**Checkpoint B (US1 complete)**: `go test -race -run 'TestWatchdog_(NewWatchdog|PatternMatchEmitsAlert|NoMatchNoAlert|EmptyPatternSet|Multiple|RunSingleShot|RunStopsOnCtxCancel|IngestAfterRun|PrecompiledPatternsReused|SC001)' ./internal/supervise/watchdog/` is GREEN. User Story 1 is independently demonstrable.

---

## Phase 4: User Story 2 — Operator is not spammed (Priority: P2)

**Goal**: A flapping pattern produces exactly one alert per `RateLimit` window. Every suppressed match (rate-limit, queue-full episode, alert-output saturation) surfaces as a WARN with pattern name + monotonic timestamp + counters, **never** with line content (Clarification Q2). Concurrent producers are race-clean.

**Independent Test**: `go test -race -run 'TestWatchdog_(RateLimit|BucketRefills|PerPatternBudget|IngestNonBlocking|QueueFull|AlertOutputSaturated|ConcurrentLogIngest)' ./internal/supervise/watchdog/` passes AND the recording slog handler captures the expected WARN counts with zero matched-line bytes in any WARN attribute.

### Tests for User Story 2 (TDD-mandatory — write BEFORE implementation) ⚠️

- [X] T028 [US2] ★ Add `TestWatchdog_RateLimitBlocksExcess` (B-W-8, REQUIRED — asserts WARN emitted on drop, NOT silent) to `internal/supervise/watchdog/watchdog_test.go` — single pattern, RateLimit 10 min, fake clock at t0; ingest 5 matching lines back-to-back at t0; assert exactly 1 `Event` AND 4 WARN entries (one per suppressed match per FR-006). Each WARN MUST carry attrs `pattern=<name>`, `suppressed_count` monotonically incrementing, and a `time` attribute — and MUST NOT contain any byte from a sentinel-tagged matched line (Clarification Q2 invariant W-17). Assertion via `bytes.Contains` over the captured slog record's serialized bytes.
- [X] T029 [US2] Add `TestWatchdog_BucketRefillsAfterInterval` (B-W-9) to `internal/supervise/watchdog/watchdog_test.go` — one pattern, RateLimit 10 min; emit at t0 (1 alert), suppress at t0+1s (1 WARN); advance fake clock to t0+10m+1s; emit at t0+10m+1s (assert second alert).
- [X] T030 [US2] Add `TestWatchdog_PerPatternBudgetIsolation` (B-W-10) to `internal/supervise/watchdog/watchdog_test.go` — two patterns "A" and "B", each RateLimit 1 hour; exhaust A's budget (1 alert + 1 WARN); ingest line matching only B; assert B emits its first alert independently (FR-004).
- [X] T031 [US2] Add `TestWatchdog_IngestNonBlockingWhenQueueFull` (B-W-11) to `internal/supervise/watchdog/watchdog_test.go` — pause matcher by holding the alerts channel un-drained; fill `w.lines` to capacity (512); measure wall-clock of an additional 1,000 Ingest calls; assert p99 latency < 1 ms (FR-010a, W-2).
- [X] T032 [US2] Add `TestWatchdog_QueueFullDropEpisodeOnceWARN` (B-W-12) to `internal/supervise/watchdog/watchdog_test.go` — fill the queue; ingest 100 lines (all drop into one episode); drain queue; ingest 1 more (succeeds → episode closes); assert exactly ONE WARN was emitted carrying `dropped_count=100` and `first_drop_at` ≈ episode-start time, with NO matched-line bytes (FR-010a Clarification Q4, invariant W-10).
- [X] T033 [US2] Add `TestWatchdog_AlertOutputSaturatedDropsWARN` (B-W-13) to `internal/supervise/watchdog/watchdog_test.go` — unbuffered alerts channel, never-draining receiver; ingest 3 matching lines for the same pattern within its RateLimit; assert the first match attempts send (drops because receiver is paused), 2 subsequent matches drop on the rate-limit path; assert exactly 3 WARNs total (1 alert-output-saturation WARN + 2 rate-limit WARNs), each excluding matched-line content (FR-011, R-010, invariant W-11).
- [X] T034 [US2] ★ Add `TestWatchdog_ConcurrentLogIngest` (B-W-21, REQUIRED, race-clean) to `internal/supervise/watchdog/watchdog_test.go` — 8 producer goroutines × 500 Ingest calls each (4,000 total) against a single watchdog with one always-matching pattern; capacity-5,000 alerts buffer; assert no panic, no data race (the suite is invoked with `-race`), and exactly 1 alert emitted (RateLimit 1 hour, capacity-1 bucket) plus 3,999 WARNs (FR-010).

**Checkpoint C (tests-fail)**: `go test -race -run 'TestWatchdog_(RateLimit|BucketRefills|PerPatternBudget|IngestNonBlocking|QueueFull|AlertOutputSaturated|ConcurrentLogIngest)' ./internal/supervise/watchdog/` shows all 7 tests FAILING.

### Implementation for User Story 2

- [X] T035 [US2] Add the clock seam `now func() time.Time` field initialization in `NewWatchdog` (default `time.Now`) and a package-private `setNowForTest(w *Watchdog, now func() time.Time)` helper in `internal/supervise/watchdog/watchdog.go` reachable from `_test.go` via a build-tag-free file-private hook (per [research.md R-004](./research.md#r-004--clock-seam-now-functimetime-unexported-field-default-timenow)).
- [X] T036 [US2] Implement per-pattern token-bucket `consumeToken(pat *Pattern, bucket *bucketState, atTime time.Time) (consumed bool)` in `internal/supervise/watchdog/watchdog.go` — lazy-refill: if `atTime - bucket.lastRefill >= pat.RateLimit` then `bucket.tokens = 1; bucket.lastRefill = atTime`; if `bucket.tokens > 0` decrement and return true; else return false. Call from matcher inner loop. Makes T028, T029, T030 partial.
- [X] T037 [US2] Implement rate-limit WARN emission in the matcher loop in `internal/supervise/watchdog/watchdog.go` — on `consumeToken` returning false, increment `bucket.suppressedCount`, then `w.logger.LogAttrs(ctx, slog.LevelWarn, "watchdog: alert suppressed by rate limit", slog.String("pattern", pat.Name), slog.Uint64("suppressed_count", bucket.suppressedCount), slog.Time("time", atTime))`. **MUST NOT** include `slog.String("line", ...)` or any line-derived attribute (Clarification Q2). Makes T028, T029, T030 pass.
- [X] T038 [US2] Implement queue-full drop episode coalescing in `internal/supervise/watchdog/watchdog.go` — extend `Ingest` to guard `enqueueMu` around the non-blocking send: on send-success, if `w.drops.count > 0` emit ONE WARN `"watchdog: lines dropped (queue full)"` with `slog.Uint64("dropped_count", drops.count)` + `slog.Time("first_drop_at", drops.firstDropAt)` and reset `drops`; on send-fail (drop), `if drops.count == 0 { drops.firstDropAt = w.now() }; drops.count++`. Makes T031, T032 pass.
- [X] T039 [US2] Implement alert-output saturation drop with WARN-per-drop in `internal/supervise/watchdog/watchdog.go` — in the matcher inner loop, replace the bare `w.alerts <- evt` with `select { case w.alerts <- evt: default: /* drop, WARN */ }`; on drop, increment `w.suppressedByAlertOutput` and emit WARN `"watchdog: alert dropped (output saturated)"` with `slog.String("pattern", pat.Name)` + `slog.Uint64("alert_output_drops", w.suppressedByAlertOutput)` + `slog.Time("time", atTime)`. **MUST NOT** include matched-line content (Q2). Makes T033 pass.
- [X] T040 [US2] Drain-on-cancel implementation review in `internal/supervise/watchdog/watchdog.go` — confirm that on `<-ctx.Done()`, the matcher (a) stops accepting new lines for evaluation, (b) reads and counts pending entries in `w.lines` without evaluating them (R-007), (c) emits ONE INFO log with `slog.Uint64("dropped_pending_count", count)` even when count is zero (presence proves cancel path executed). Required by invariant W-12.

**Checkpoint D (US2 complete)**: `go test -race -run 'TestWatchdog_(RateLimit|BucketRefills|PerPatternBudget|IngestNonBlocking|QueueFull|AlertOutputSaturated|ConcurrentLogIngest)' ./internal/supervise/watchdog/` is GREEN. All loud-suppression paths surface WARNs with zero line content.

---

## Phase 5: User Story 3 — Watchdog never changes supervisor state (Priority: P3)

**Goal**: Prove the load-bearing safety property — the watchdog has zero authority over the supervisor state machine (spec FR-003, Constitution V). No state.Store API calls, no child signals, no session-claim/refresh/revocation. Dependency graph is pure stdlib + the SDD-24 interface contract.

**Independent Test**: `go test -race -run 'TestWatchdog_(NeverTransitionsState|NoSecureBytesStringConversion|ZeroNewDependencies|SatisfiesSuperviseInterface)' ./internal/supervise/watchdog/` passes AND the source file's static grep proves zero forbidden identifiers.

### Tests for User Story 3 (TDD-mandatory — write BEFORE verification) ⚠️

- [X] T041 [US3] ★ Add `TestWatchdog_NeverTransitionsState` (B-W-17, REQUIRED — proves alert-only) to `internal/supervise/watchdog/watchdog_test.go` — define a `recordingStateDouble` struct local to the test that satisfies a method set covering `ClaimSession`, `RefreshSession`, `RevokeSession`, `RequestRestart`, `Transition` (each method panics if invoked, since the watchdog has zero compile-time path to call them); wire it into the harness as a sibling component; ingest lines that match every configured pattern; cancel; assert (a) zero panics, (b) the recorder's method-call counts are all zero, (c) the watchdog's import list — captured via `go list -f '{{ join .Imports "\n" }}' github.com/mrz1836/hush/internal/supervise/watchdog` from inside the test via `os/exec` — contains `github.com/mrz1836/hush/internal/supervise` ONLY (the interface guard) and no other `internal/supervise/*` sub-paths (FR-003, invariant W-8).
- [X] T042 [US3] Add `TestWatchdog_NoSecureBytesStringConversion` (B-W-18) to `internal/supervise/watchdog/watchdog_test.go` — read `internal/supervise/watchdog/watchdog.go` source bytes; assert via regex that (a) no line matches `securebytes` (case-insensitive), (b) no line matches `string\([^)]*[Ss]ecret`, (c) the single `string(...)` site is `string(line)` on a `[]byte` parameter (operator log content, not vault material) (Constitution X, invariant W-18).
- [X] T043 [US3] Add `TestWatchdog_ZeroNewDependencies` (B-W-19) to `internal/supervise/watchdog/watchdog_test.go` — execute `go list -deps -f '{{.ImportPath}}' github.com/mrz1836/hush/internal/supervise/watchdog` via `os/exec`; assert the dep set is a subset of the locked allowlist: stdlib only (`context`, `errors`, `fmt`, `log/slog`, `regexp`, `sync`, `sync/atomic`, `time` + their transitive deps) plus `github.com/mrz1836/hush/internal/supervise` (interface guard) and its transitive deps (Constitution XI, invariant W-19).
- [X] T044 [US3] Add `TestWatchdog_SatisfiesSuperviseInterface` (B-W-24) to `internal/supervise/watchdog/watchdog_test.go` — runtime assertion: `var _ supervise.Watchdog = (*watchdog.Watchdog)(nil)` inside the test function via a typed nil; also call `OnStderrLine(context.Background(), []byte("ignored"))` against a fresh watchdog instance to prove the method invokes Ingest without panic when Run has not started (R-003, invariant W-20).

**Checkpoint E (tests-fail)**: `go test -race -run 'TestWatchdog_(NeverTransitionsState|NoSecureBytesStringConversion|ZeroNewDependencies|SatisfiesSuperviseInterface)' ./internal/supervise/watchdog/` shows all 4 tests FAILING (likely because validation logic in `internal/supervise/watchdog/watchdog.go` does not yet honour the import-set discipline).

### Implementation for User Story 3

- [X] T045 [US3] Audit `internal/supervise/watchdog/watchdog.go` source — confirm zero occurrences of `Store`, `Refiller`, `Refresher`, `Grace`, `Lifecycle` as identifiers; zero function-call usage of `internal/supervise.*` (the only allowed reference is `supervise.Watchdog` as the RHS of the compile-time guard `var _`). Remove any incidental reference uncovered. Makes T041 pass.
- [X] T046 [US3] Audit `internal/supervise/watchdog/watchdog.go` for the single `string(...)` site — verify it is exactly `string(line)` inside the matcher loop, with `line` typed as `[]byte` (non-secret operator log content). Confirm no `internal/vault/securebytes` import. Makes T042 pass.
- [X] T047 [US3] Audit `internal/supervise/watchdog/watchdog.go` import block — verify the import set matches the allowlist locked by [plan.md §Technical Context](./plan.md#technical-context): only `context`, `errors`, `fmt`, `log/slog`, `regexp`, `sync`, `sync/atomic`, `time`, and `github.com/mrz1836/hush/internal/supervise`. Makes T043 pass.

**Checkpoint F (US3 complete)**: All 24 named tests GREEN under `-race`. The watchdog's alert-only contract is proven by the recordingStateDouble + the import-set assertion.

---

## Phase 6: Polish & Gates (Final Phase)

**Purpose**: Run the constitutional gates (Constitution VIII), prove coverage, and confirm zero regressions across the repo. Required by SDD-27 Prompt 4 user directive.

- [X] T048 Run `magex format:fix` from repo root — applies gofmt + goimports across the touched files. Required by quickstart.md §3.
- [X] T049 Run `magex lint` from repo root — invokes `golangci-lint` with the repo's locked config (`.github/.golangci.yml`). MUST pass clean.
- [X] T050 Run `magex test:race` from repo root — invokes `go test -race ./...`. MUST pass clean (Constitution VIII race-clean gate).
- [X] T051 Verify coverage ≥ 90% on the new sub-package — `go test -coverprofile=/tmp/wd.cover ./internal/supervise/watchdog/ && go tool cover -func=/tmp/wd.cover | tail -1`. Expected output: `total: (statements) >= 90.0%`. If below 90%, add tests against any uncovered statement (must be a behaviour, not an internal accident).
- [X] T052 [P] Verify the alert-only contract end-to-end — `go test -race -run 'TestWatchdog_(NeverTransitionsState)' ./internal/supervise/watchdog/ -v` and confirm the test's `recordingStateDouble` reports zero method calls.
- [X] T053 [P] Verify the rate-limit WARN contract — `go test -race -run 'TestWatchdog_RateLimitBlocksExcess' ./internal/supervise/watchdog/ -v` and confirm the captured WARN entries (a) total `N-1` for N matches, (b) contain pattern name + counter, (c) contain ZERO bytes of the sentinel matched line.
- [X] T054 Run quickstart.md §3 commands as a final smoke — `go test -race -cover ./internal/supervise/watchdog/` plus the section-3 coverage check. Cross-check that the 24 test names listed in [quickstart.md §4](./quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4) all appear in `go test -v` output.

**Final checkpoint**: All gates GREEN. Coverage ≥ 90%. Race-clean. Rate-limit tested with WARN-drop (never silent). Never-restart proven. Ready for SDD-27 Prompt 5 (/speckit-implement) post-step doc updates (PACKAGE-MAP, AC-MATRIX, SDD-PLAYBOOK).

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — starts immediately.
- **Phase 2 (Foundational)**: Depends on Phase 1. T004–T008 share `watchdog.go` and run sequentially. T009 ([P]) runs in parallel with T004–T008 against `watchdog_test.go`.
- **Phase 3 (US1, P1)**: Depends on Phase 2. T010–T022 (tests) MUST be written before T023–T027 (impl). Within tests: all 13 [US1] test tasks share the same file and run sequentially; they CANNOT be marked [P]. Within impl: T023–T027 share the same file and run sequentially. T025 (OnStderrLine adapter) is independent of T023/T024/T026/T027 and can be reordered.
- **Phase 4 (US2, P2)**: Depends on Phase 3 (impl makes some US2 tests transition from "compile error" to "assertion fail"). T028–T034 (tests) before T035–T040 (impl).
- **Phase 5 (US3, P3)**: Depends on Phase 4 (the full implementation must exist for the audits in T045–T047 to be meaningful). T041–T044 (tests) before T045–T047 (verification).
- **Phase 6 (Polish)**: Depends on Phases 3–5 being GREEN. T048 → T049 → T050 → T051 sequentially (each shapes the next). T052, T053 [P] independent. T054 final.

### User Story Dependencies

- **US1 (P1)**: Depends only on Foundational (Phase 2). Once Phase 2 is done, US1 is the MVP slice — can be cut, demoed, and reviewed independently.
- **US2 (P2)**: Depends on US1 implementation (the matcher loop in T027 is the seam where rate-limit logic and queue-full coalescing slot in). NOT independently testable without US1's matcher running.
- **US3 (P3)**: Depends on US1 + US2 (the alert-only proof requires the full implementation to assert "did NOT do X" across every pattern path). However, the safety property is **independent of correctness** of US1/US2 — even a buggy implementation must satisfy US3 (it must NEVER call into state machinery), so US3 tests should not regress after US1/US2 changes.

### Within Each User Story

- TDD-mandatory ordering: every test task in the "Tests for User Story N" subsection MUST be authored and observed FAILING before any "Implementation for User Story N" task starts.
- Inside the test block, table-driven sub-cases are encouraged per `.github/tech-conventions/testing-standards.md`.
- Inside the implementation block, tasks are listed in dependency order: data structures (T035 clock seam) → algorithms (T036 bucket) → emission (T037 rate-limit WARN, T038 queue-full WARN, T039 saturation WARN) → cancel path (T040 drain).
- Story complete and GREEN before starting the next priority.

### Parallel Opportunities

- T002 [P] and T003 [P] in Phase 1 — different files, no symbol dependency.
- T009 [P] in Phase 2 — different file from T004–T008.
- T052 [P] and T053 [P] in Phase 6 — both are read-only verification runs against an already-GREEN suite.
- **No parallel test-authoring** within a single user story: every test task in this plan adds a function to the same `watchdog_test.go` file, so Edit-tool contention makes [P] inappropriate. The TDD discipline (write one, observe fail, move on) also implies sequential authoring.

---

## Parallel Example: Phase 2 (Foundational)

```bash
# T002 and T003 can run concurrently (different files):
Task: "Create internal/supervise/watchdog/watchdog.go with package + imports"
Task: "Create internal/supervise/watchdog/watchdog_test.go with package + imports"

# Within Phase 2, T009 can run in parallel with T004–T008 (different file):
Task: "T004 Declare 7 sentinel errors in watchdog.go"   # blocks T005..T008
Task: "T009 [P] Implement shared test helpers in watchdog_test.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001–T003).
2. Complete Phase 2: Foundational (T004–T009) — CRITICAL gate; without it, no test compiles.
3. Complete Phase 3: User Story 1 (T010–T027).
4. **STOP and VALIDATE**: Run `go test -race -run TestWatchdog_PatternMatchEmitsAlert ./internal/supervise/watchdog/`; observe one alert emitted within 100 ms. This is the operator-visible MVP — Scenario 15 alert-emission half.
5. Optional: pause here for review before proceeding to US2.

### Incremental Delivery

1. Setup + Foundational → tests compile (no behaviour yet).
2. Add US1 (P1) → Test → Pattern match emits alert end-to-end (MVP).
3. Add US2 (P2) → Test → Rate-limit + WARN suppression + queue-full + saturation all loud-failed.
4. Add US3 (P3) → Test → Alert-only safety property statically + dynamically verified.
5. Polish & Gates → coverage ≥ 90%, race-clean, format + lint green.
6. Hand off to SDD-27 Prompt 5 for the combined commit + doc updates.

### Parallel Team Strategy (if multi-developer)

- This chunk has a single source file and a single test file, so most tasks serialize on file ownership. Realistic parallelism: one developer drives the chunk while a second reviewer audits the WARN sentinel-byte assertions (T028, T032, T033) for line-content leakage.

---

## Notes

- Every test name listed above appears verbatim in [quickstart.md §4 mandatory test list](./quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4). The seven chunk-doc-mandated names (★) from SDD-27 Prompt 4 are: T013 (PatternMatchEmitsAlert), T014 (NoMatchNoAlert), T019 (RunStopsOnCtxCancel), T021 (PrecompiledPatternsReused), T028 (RateLimitBlocksExcess), T034 (ConcurrentLogIngest), T041 (NeverTransitionsState).
- WARN sentinel-byte invariant (Clarification Q2, W-17): every WARN test (T028, T032, T033) MUST assert via `bytes.Contains(serializedRecord, sentinelMatchedLineBytes) == false`. This is the load-bearing Constitution-X observability gate for this chunk.
- Race-clean is non-negotiable per Constitution VIII / plan.md Constitution Check VIII. Every test command in this file invokes `-race`.
- Commit policy: per SDD-27 Prompt 5, do NOT commit between phases. The /speckit-implement command makes ONE combined commit covering source + tests + post-step doc updates (PACKAGE-MAP, AC-MATRIX, SDD-PLAYBOOK).
- Verify each test FAILS for the right reason before implementing — "undefined identifier" failures mean a Phase 2 stub is missing; "assertion mismatch" failures mean you are correctly TDD'ing.
- Avoid: vague tasks, same-file Edit conflicts disguised as [P], cross-story coupling that breaks US1's MVP independence.
