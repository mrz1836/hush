# Observable Behaviors — SDD-27 (`internal/supervise/watchdog`)

This document is the **black-box** contract for the chunk's three
exported types. Every entry maps to at least one test in
[quickstart.md § 4](../quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4)
and to at least one spec FR. Reviewers diff this against the
implemented behavior — any behavior not listed here is
implementation detail and may change without breaking the contract.

---

## Watchdog — construction observable behaviors

### B-W-1 — Construction with valid pattern set succeeds (FR-001, FR-007)
- **Pre:** non-empty, well-formed `[]Pattern` slice; non-nil alerts channel; non-nil logger.
- **Action:** `wd, err := NewWatchdog(patterns, alerts, logger)`.
- **Observable:**
  - `err == nil`.
  - `wd != nil`.
  - No goroutines spawned (matcher loop has not started; Run has not been invoked).
  - Per-pattern token buckets are initialized full (asserted indirectly via B-W-3).

### B-W-2 — Construction rejects duplicate pattern names (FR-007a, Clarification Q5)
- **Pre:** pattern slice containing two entries with the same `Name`.
- **Action:** `wd, err := NewWatchdog(patterns, alerts, logger)`.
- **Observable:**
  - `wd == nil`.
  - `errors.Is(err, ErrDuplicatePatternName) == true`.
  - The error message names the offending duplicate name verbatim (RHS-only; no other pattern metadata).
  - No goroutines spawned.

### B-W-2a — Construction rejects empty pattern name, nil regex, non-positive RateLimit, nil channel, nil logger
- **Pre:** one of the above invalid conditions in the input.
- **Action:** `wd, err := NewWatchdog(...)`.
- **Observable:**
  - `wd == nil`.
  - Each input flaw maps to a distinct `errors.Is(err, X) == true` per `data-model.md §3 Sentinel errors`.
  - No goroutines spawned.

### B-W-3 — First match per pattern always emits (FR-004 "bucket starts full")
- **Pre:** freshly-constructed watchdog with one pattern `P`; Run is running; alerts channel has receive capacity.
- **Action:** `wd.Ingest(line)` where `P.Regex.Match(line) == true`.
- **Observable:**
  - Exactly one `Event` arrives on the alerts channel within SC-001 (100 ms wall-clock).
  - `event.Pattern == P.Name`.
  - `event.Line == string(line)` (defensive copy, equal content).
  - `event.Time` equals the watchdog's `now()` reading at match time (test injects fake clock).
  - Zero WARN log entries.

---

## Watchdog — matching observable behaviors

### B-W-4 — Non-matching line produces nothing (FR-001, Edge Case)
- **Pre:** watchdog with one pattern that does NOT match the supplied line.
- **Action:** `wd.Ingest(line)`.
- **Observable:**
  - Zero alerts emitted (alerts channel read returns timeout).
  - Zero log entries (operational logger captures empty after a bounded drain).
  - Token bucket for the pattern remains untouched.

### B-W-5 — Empty pattern set runs cleanly (FR-014)
- **Pre:** `NewWatchdog([]Pattern{}, alerts, logger)` succeeded; Run is running.
- **Action:** ingest 100 arbitrary lines.
- **Observable:**
  - Zero alerts emitted.
  - Zero WARN entries.
  - On `<-ctx.Done()`, Run returns within SC-004 (250 ms) with wrapped ctx.Err().

### B-W-6 — Multiple patterns matching the same line each emit independently (Edge Case)
- **Pre:** two patterns `P1`, `P2` both match `line`; both buckets full.
- **Action:** `wd.Ingest(line)`.
- **Observable:**
  - Exactly two `Event`s arrive on the alerts channel.
  - The events' `Pattern` field set is `{P1.Name, P2.Name}`; order is implementation-defined.
  - Both events carry the same `Line` content and identical `Time` (single clock read per ingest).

### B-W-7 — Pattern matching multiple non-overlapping spans on one line emits ONCE (Edge Case)
- **Pre:** pattern that matches multiple non-overlapping substrings of `line`.
- **Action:** `wd.Ingest(line)`.
- **Observable:**
  - Exactly one `Event` for that pattern (the alert reports the line, not each span).

---

## Watchdog — rate-limit observable behaviors

### B-W-8 — Rate-limit suppresses excess matches and WARN-logs each (FR-004, FR-005, FR-006)
- **Pre:** watchdog with one pattern `P` whose `RateLimit = 600 s` (default-config-equivalent); bucket starts full; injected clock pinned at `t0`.
- **Action:** ingest `N = 50` lines all matching `P` over a wall-clock span < `RateLimit`.
- **Observable:**
  - Exactly one `Event` on the alerts channel (the first ingest's match).
  - Exactly `N-1 == 49` WARN-level log entries.
  - Each WARN entry contains: `slog.Attr("pattern", P.Name)`, `slog.Attr("ts", ...)` (monotonic timestamp), `slog.Attr("suppressed_count", uint64)` (incrementing per pattern).
  - No WARN entry contains the matched line content as a substring (Clarification Q2 — sentinel-scan assertion).
  - Token bucket state: `tokens == 0`, `lastRefill == t0` (the first emission time).

### B-W-9 — Bucket refills after one RateLimit window (FR-004 refill semantics)
- **Pre:** bucket exhausted at `t0` (one prior emit); injected clock currently reads `t0`.
- **Action:** advance clock to `t0 + RateLimit + 1 ms`; ingest one matching line.
- **Observable:**
  - Exactly one `Event` on the alerts channel.
  - Zero WARN entries (the match is permitted, not suppressed).
  - Bucket state updated: `tokens == 0` (consumed by the new emit), `lastRefill = t0 + RateLimit + 1 ms`.

### B-W-10 — Cross-pattern budget isolation (FR-004, AC Scenario 2)
- **Pre:** two patterns `P1` (bucket empty) and `P2` (bucket full).
- **Action:** ingest one line matching ONLY `P2`.
- **Observable:**
  - Exactly one `Event` for `P2`.
  - `P1`'s bucket and suppressed-count are unchanged.

---

## Watchdog — backpressure observable behaviors

### B-W-11 — Ingest never blocks the caller even under queue saturation (FR-010a)
- **Pre:** watchdog whose internal line channel is full (matcher paused via test-injection point).
- **Action:** call `wd.Ingest(line)` 1,000 times sequentially from a single goroutine; measure wall-clock.
- **Observable:**
  - All 1,000 calls complete within an aggregate budget of 100 ms (each Ingest << 1 ms).
  - The internal channel does NOT grow beyond `lineChannelCapacity` (asserted indirectly via no panic / no allocation explosion).

### B-W-12 — Queue-full drop episode emits one WARN at episode close (FR-010a, Clarification Q4)
- **Pre:** matcher paused; 600 Ingests called → ~500 dropped (cap 512; matcher hasn't drained), then matcher resumed.
- **Action:** after a brief drain, perform one more successful Ingest to close the episode.
- **Observable:**
  - Exactly one WARN-level entry naming `slog.Attr("watchdog", "...")`, `slog.Attr("drop_count", uint64)` (final count for the episode), `slog.Attr("first_drop_at", time.Time)`.
  - The WARN entry does NOT contain any dropped line content.
  - A subsequent (unrelated) drop later in the same Run starts a NEW episode (zero count, new firstDropAt).

### B-W-13 — Alert-output saturation drops emit one WARN per drop (FR-011, Research R-010)
- **Pre:** alerts channel is unbuffered AND no receiver is reading; matcher running; pattern `P` with full bucket.
- **Action:** ingest 5 matching lines for `P` over a span << RateLimit.
- **Observable:**
  - The bucket consumes only ONE token (the first match attempts to emit and falls through to the saturated branch; the bucket is debited only when the alert is successfully placed on the channel — research.md R-010 / R-006 read carefully: bucket debit happens BEFORE the select; tighten the assertion below accordingly).
  - **Refined:** The bucket is debited on each successful emit. Drops on the alert-output-saturated branch produce: incremented `suppressedByAlertOutput` counter, exactly one WARN per dropped match naming `slog.Attr("pattern", P.Name)`, `slog.Attr("ts", ...)`, `slog.Attr("suppressed_total", uint64)`, NO line content.
  - The rate-limit and alert-output WARN entries are distinguishable by their message prefix (e.g., `"watchdog: alert dropped (rate-limited)"` vs `"watchdog: alert dropped (output channel saturated)"`).

> *Implementation note (will be locked in implementation phase):* if the bucket is debited only on successful emit, alert-output drops do NOT exhaust the bucket — the operator sees the same pattern attempt again next match. If the bucket is debited before the emit attempt, alert-output drops exhaust the bucket and the operator sees a clear "matched but dropped" signal. The decision is between **bucket = budget for emits** vs **bucket = budget for matches**. The plan's recommendation is **bucket = budget for emits**: debit only after the channel send succeeds; this preserves operator value when the downstream router is stalled (the next match will attempt again). The WARN per drop ensures audibility. This is consistent with spec FR-005's wording: "When a pattern matches but its alert budget is exhausted, the watchdog MUST record a WARN" — the budget is for emits, not matches.

---

## Watchdog — lifecycle observable behaviors

### B-W-14 — Run is single-shot (FR-009, R-012)
- **Pre:** Run has already been invoked once on `wd`.
- **Action:** call `wd.Run(ctx)` a second time.
- **Observable:**
  - The second call returns immediately with `errors.Is(err, ErrAlreadyRan) == true`.
  - No additional matcher goroutine is spawned.

### B-W-15 — Run returns on ctx cancel within 250ms (SC-004, R-007)
- **Pre:** Run is executing; the internal channel has K lines buffered (matcher paused at a test seam, or K=0).
- **Action:** cancel ctx.
- **Observable:**
  - Run returns within 250 ms wall-clock.
  - Return value satisfies `errors.Is(err, ctx.Err()) == true`.
  - Exactly one INFO-level log entry is emitted before return, with `slog.Attr("dropped_on_cancel", uint64)` set to the count of lines drained-but-not-evaluated (K). NO additional log entries are written after the INFO line.
  - `runtime.NumGoroutine()` returns to the pre-Run baseline within 250 ms of the cancel.
  - The alerts channel passed to NewWatchdog is left OPEN.

### B-W-16 — Ingest after Run returns is a silent no-op (FR-009, R-009)
- **Pre:** Run has returned.
- **Action:** call `wd.Ingest(line)` 10 times.
- **Observable:**
  - Zero alerts emitted.
  - Zero log entries written by Ingest.
  - No goroutines spawned.
  - No panic.

---

## Watchdog — Constitution invariants

### B-W-17 — Watchdog NEVER calls into the supervisor state machine (FR-003, Constitution V)
- **Pre:** watchdog wired in isolation (no `*supervise.Store`, no Refiller, no Refresher visible).
- **Action:** ingest a mix of matching, non-matching, rate-limited, queue-full, and alert-saturated lines.
- **Observable:**
  - The watchdog package's import set, captured via `go list -deps -f '{{.ImportPath}}' ./internal/supervise/watchdog/`, contains `github.com/mrz1836/hush/internal/supervise` ONLY as the host of the `supervise.Watchdog` interface (one symbol reference for the compile-time guard) — NOT as a source of function calls.
  - No state.Store API is invoked by the watchdog (verified by inspection: the watchdog package does not name `Store`, `Refiller`, `Refresher`, `Grace`, `Lifecycle` anywhere in its source).
  - Child process is never signalled, killed, or restarted by the watchdog under any code path (FR-003).

### B-W-18 — No `string(secret)` site in the watchdog package (Constitution X)
- **Pre:** the watchdog source files exist.
- **Action:** static scan.
- **Observable:**
  - The watchdog package does NOT import `internal/vault/securebytes`.
  - No occurrence of `string(*SecureBytes)` or `string(secret*)` in source.
  - The single `string(...)` site is `string(line)` inside the matcher (Event.Line construction); `line` is operator log content, not vault material (Constitution X scope: "secret material" / "vault payload").

### B-W-19 — Zero new direct `go.mod` dependencies (Constitution XI)
- **Pre:** the watchdog source files exist.
- **Action:** `go list -deps -f '{{.ImportPath}}' ./internal/supervise/watchdog/` minus the stdlib snapshot.
- **Observable:**
  - The diff contains only `github.com/mrz1836/hush/internal/...` entries (already-in-module).
  - No new entry in `go.mod` `require` blocks added by this chunk.

### B-W-20 — Watchdog never spawns a goroutine outside Run (Constitution IX)
- **Pre:** any lifecycle phase.
- **Action:** observe `runtime.NumGoroutine()` deltas at: post-NewWatchdog, post-Run-start, post-Run-return, post-Ingest-after-return.
- **Observable:**
  - `delta(NewWatchdog) == 0` (no goroutines started by construction).
  - `delta(Run-start) == 1` (exactly the matcher loop).
  - `delta(Run-return) == -1` (matcher loop exited).
  - `delta(Ingest) == 0` at every phase.

---

## Watchdog — operational behaviors

### B-W-21 — Concurrent Ingest is race-clean under `-race` (FR-010)
- **Pre:** Run executing.
- **Action:** spawn 8 producer goroutines, each calling `wd.Ingest(line)` 500 times for a mix of matching and non-matching lines.
- **Observable:**
  - `go test -race` reports no data race.
  - Total observed alerts ≤ `len(patterns)` (one per pattern, bucket-limited).
  - Total WARN entries (rate-limit) ≤ producers × ingests − len(patterns).
  - No goroutine leak.

### B-W-22 — 10,000-line throughput does not re-compile patterns (FR-008, SC-006)
- **Pre:** watchdog with K=5 patterns whose `Regex` is a test type that records compilation count.
- **Action:** ingest 10,000 lines.
- **Observable:**
  - Compilation count on each Regex remains at 1 (the count incurred BEFORE NewWatchdog was called).
  - Per-line evaluation invokes only `Regex.Match(line []byte)`.

### B-W-23 — SC-001 100 ms emit latency under sequential single-pattern ingest
- **Pre:** watchdog with one pattern; alerts channel buffered cap 16; matcher running.
- **Action:** record wall-clock between `wd.Ingest(matching_line)` and the receiver picking up the Event.
- **Observable:**
  - 99th percentile of 100 sequential trials ≤ 100 ms (spec SC-001).

---

## Type contracts (compile-time)

### B-W-24 — `*Watchdog` satisfies `supervise.Watchdog` (R-003)
- **Action:** static guard `var _ supervise.Watchdog = (*Watchdog)(nil)` in [internal/supervise/watchdog/watchdog.go](../../internal/supervise/watchdog/watchdog.go).
- **Observable:**
  - Build succeeds.
  - `TestWatchdog_SatisfiesSuperviseInterface` runtime-asserts: declare `var w supervise.Watchdog = wd` succeeds at runtime.

### B-W-25 — Event and Pattern are exported value types (FR-013)
- **Observable:**
  - `Pattern` and `Event` have no unexported exported-shape fields.
  - Both can be passed by value, stored in slices, copied without aliasing, and serialized (the watchdog itself never serialises Event, but downstream consumers may).

---

## Cross-reference

| Behavior ID | Spec FR / SC | Test name | Data-model invariant |
|-------------|--------------|-----------|----------------------|
| B-W-1 | FR-001, FR-007 | `TestWatchdog_NewWatchdog_ValidPatternSet` | W-3 (initial bucket full) |
| B-W-2 | FR-007a (Q5) | `TestWatchdog_NewWatchdog_DuplicatePatternName` | P-2 |
| B-W-2a | P-1, P-3, P-4 | `TestWatchdog_NewWatchdog_InvalidInputs` | P-1, P-3, P-4 |
| B-W-3 | FR-004 | `TestWatchdog_PatternMatchEmitsAlert` | W-3 |
| B-W-4 | FR-001 | `TestWatchdog_NoMatchNoAlert` | W-6 |
| B-W-5 | FR-014 | `TestWatchdog_EmptyPatternSetIsBenign` | W-15 |
| B-W-6 | Edge Case | `TestWatchdog_MultipleMatchesOnSameLine` | W-7 |
| B-W-7 | Edge Case | `TestWatchdog_MultipleSpansSingleEmit` | (no new invariant — covered by B-W-3 semantics) |
| B-W-8 | FR-004, FR-005, FR-006 (Q2) | `TestWatchdog_RateLimitBlocksExcess` | W-4, W-17 |
| B-W-9 | FR-004 | `TestWatchdog_BucketRefillsAfterInterval` | W-5 |
| B-W-10 | FR-004 | `TestWatchdog_PerPatternBudgetIsolation` | (covered by W-4 per-pattern semantics) |
| B-W-11 | FR-010a | `TestWatchdog_IngestNonBlockingWhenQueueFull` | W-2 |
| B-W-12 | FR-010a (Q4) | `TestWatchdog_QueueFullDropEpisodeOnceWARN` | W-10 |
| B-W-13 | FR-011 (Q2) | `TestWatchdog_AlertOutputSaturatedDropsWARN` | W-11, W-17 |
| B-W-14 | FR-009 | `TestWatchdog_RunSingleShot` | W-13 |
| B-W-15 | SC-004, FR-009 | `TestWatchdog_RunStopsOnCtxCancel` | W-1, W-12 |
| B-W-16 | FR-009 | `TestWatchdog_IngestAfterRunReturnIsNoop` | W-14 |
| B-W-17 | FR-003, Constitution V | `TestWatchdog_NeverTransitionsState` | W-8 |
| B-W-18 | Constitution X | `TestWatchdog_NoSecureBytesStringConversion` | W-18 |
| B-W-19 | Constitution XI | `TestWatchdog_ZeroNewDependencies` | W-19 |
| B-W-20 | Constitution IX | (covered by W-1, B-W-15, B-W-16) | W-1 |
| B-W-21 | FR-010, SC-004 | `TestWatchdog_ConcurrentLogIngest` | W-16 |
| B-W-22 | FR-008, SC-006 | `TestWatchdog_PrecompiledPatternsReused` | W-9 |
| B-W-23 | SC-001 | `TestWatchdog_SC001_EmitLatencyUnder100ms` | (SC-only) |
| B-W-24 | R-003 | `TestWatchdog_SatisfiesSuperviseInterface` | W-20 |
| B-W-25 | FR-013 | (compile-time; covered by other tests) | E-5 |
