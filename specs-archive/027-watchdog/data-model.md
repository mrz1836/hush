# Phase 1 Data Model — SDD-27 (`internal/supervise/watchdog`)

**Feature:** 027-watchdog
**Date:** 2026-05-13
**Spec:** [spec.md](./spec.md) · **Research:** [research.md](./research.md) · **Chunk doc:** [docs/sdd/SDD-27.md](../../docs/sdd/SDD-27.md)

This document is the locked structural inventory for the chunk's
single file `internal/supervise/watchdog/watchdog.go`. It pins
struct shapes, field semantics, ownership rules, and the
per-entity invariants the tests will assert. Exported field names
and method signatures are locked; unexported fields MAY be renamed
without notice but their semantics MUST NOT drift.

---

## Entity overview

| Entity | Kind | Purpose | Owns |
|--------|------|---------|------|
| `Pattern` | exported struct | Operator-named regex predicate + per-pattern refill interval | nothing (value type; the `Regex *regexp.Regexp` field is a borrow reference compiled by the caller) |
| `Event` | exported struct | Typed alert emitted to the downstream router (SDD-28) | nothing (value type; `Line string` is a defensive copy) |
| `Watchdog` | exported struct | Single-instance, single-run pattern engine | unexported `lines chan []byte` (creates + reads); per-pattern `bucketState` map (creates + mutates); `dropEpisode` (creates + mutates); compile-time guard `_ supervise.Watchdog = (*Watchdog)(nil)` |
| `bucketState` *(internal)* | unexported struct | Per-pattern token-bucket + suppressed-match counter | nothing |
| `dropEpisode` *(internal)* | unexported struct | Queue-full episode bookkeeping | nothing |

**Borrow vs own.** A "borrow" reference is one whose lifetime
exceeds the entity's; the entity MUST NOT close, free, or destroy
it. An "own" reference is one the entity creates and is
responsible for. The `alerts chan<- Event` passed to NewWatchdog
is a **borrow** — the watchdog only sends, never closes (R-011).

---

## 1. `Pattern` (exported)

### Struct shape (locked)

```go
// Pattern is an operator-named regex predicate paired with a per-pattern
// alert refill interval. Constructed by the SDD-23 CLI wiring from
// config.Supervisor.Watchdog.Patterns + .MaxAlertsPerHour; passed
// verbatim to NewWatchdog and never mutated thereafter.
//
// Name is the only identifier downstream consumers (Event.Pattern,
// WARN logs, operator correlation) see and MUST be pairwise distinct
// within a single watchdog instance (spec FR-007a). NewWatchdog
// rejects duplicate names with ErrDuplicatePatternName.
//
// Regex is the pre-compiled matcher; the watchdog assumes well-
// formedness and does not vet for pathological backtracking (spec
// "Edge cases — pathological regex" row).
//
// RateLimit is the token-bucket refill interval (capacity 1).
// Caller derives from MaxAlertsPerHour as
//     time.Duration((3600.0 / float64(MaxAlertsPerHour)) * float64(time.Second))
// With the default MaxAlertsPerHour=6, RateLimit is 600s (10 min).
type Pattern struct {
    Name      string
    Regex     *regexp.Regexp
    RateLimit time.Duration
}
```

### Invariants

| ID | Invariant | Enforced where |
|----|-----------|----------------|
| P-1 | `Name` is non-empty | NewWatchdog rejects empty names with `ErrEmptyPatternName` (spec Edge Cases — the empty-name case is structurally indistinguishable from "duplicate of another empty name", so reject early) |
| P-2 | `Name` is pairwise distinct within one watchdog instance | NewWatchdog rejects with `ErrDuplicatePatternName` (FR-007a) |
| P-3 | `Regex` is non-nil | NewWatchdog rejects with `ErrNilPatternRegex` |
| P-4 | `RateLimit` is positive (`> 0`) | NewWatchdog rejects with `ErrNonPositiveRateLimit`; spec FR-004 requires a refill rate, and a zero/negative rate would either disable rate limiting or refill in the past |
| P-5 | Pattern values are immutable after construction | Watchdog never mutates the Pattern slice or any field (FR-007) |
| P-6 | Empty pattern slice is permitted | NewWatchdog returns a valid Watchdog with zero patterns; Run accepts lines, emits no alerts (FR-014) |

### Constructor wiring

Constructed by the SDD-23 CLI orchestrator (post-merge), not by
the watchdog package itself. See [research.md R-015](./research.md#r-015--pattern-source-of-truth-sdd-18-configs-compiled-regex-set).
The watchdog package exports no Pattern constructor.

---

## 2. `Event` (exported)

### Struct shape (locked)

```go
// Event is the typed alert emitted by a Watchdog on each non-
// suppressed pattern match. Consumed by the downstream alert router
// (SDD-28); represented as a value type so downstream consumers
// have no ownership concerns.
//
// Pattern identifies the matched Pattern.Name verbatim.
// Line is a defensive copy of the matched line content (string,
// owned by Event — the watchdog's internal channel buffer is
// reclaimable after Event construction).
// Time is the wall-clock of the match per the watchdog's clock
// seam (research.md R-004).
type Event struct {
    Pattern string
    Line    string
    Time    time.Time
}
```

### Invariants

| ID | Invariant | Enforced where |
|----|-----------|----------------|
| E-1 | `Pattern` equals some `Pattern.Name` from the watchdog's pattern slice | matcher loop sources `Pattern` from `pat.Name` directly |
| E-2 | `Line` is non-empty when emitted (matcher never emits on empty lines) | matcher early-exits on `len(line) == 0` before pattern evaluation |
| E-3 | `Line` content does NOT leak into operational logs above DEBUG (spec FR-012) | matcher never logs Event.Line; WARN entries explicitly exclude line content (FR-005/Q2) |
| E-4 | `Time` is monotonically non-decreasing within a single watchdog run | clock seam is one `now func() time.Time` (R-004) — the wall clock is the only source |
| E-5 | Event carries no secret material (Constitution X) | by construction: Pattern.Name is operator config; Line is child stderr; Time is a timestamp; none source from `*SecureBytes` |

---

## 3. `Watchdog` (exported, single-instance, single-run)

### Struct shape (locked)

```go
// Watchdog is the single-instance, single-run pattern engine. Lifecycle:
//
//   wd, err := watchdog.NewWatchdog(patterns, alertsCh, logger)
//   if err != nil { /* fail boot */ }
//   go wd.Run(ctx)   // owner: the SDD-23 CLI orchestrator
//   // ... external producers call wd.Ingest(line) ...
//   <-ctx.Done()     // cancellation drains-or-drops per R-007
//   // Run returns ctx.Err() (wrapped); wd is now in post-Run state.
//
// After Run returns:
//   - wd.Ingest(...) is a no-op (cancelled atomic short-circuit)
//   - No goroutines remain alive that were owned by wd (FR-009)
//   - The alerts channel passed to NewWatchdog is left open (R-011)
//
// Second call to Run returns ErrAlreadyRan; the type is single-shot
// (R-012).
type Watchdog struct {
    // Immutable after NewWatchdog returns.
    patterns []Pattern        // owned slice (defensive copy of caller's input)
    alerts   chan<- Event     // borrow; never closed (R-011)
    logger   *slog.Logger     // borrow

    // Clock seam (R-004). Default time.Now; test override via setNowForTest.
    now func() time.Time

    // Internal line queue (R-005). Created in NewWatchdog; read in Run.
    lines chan []byte // capacity lineChannelCapacity (=512)

    // Per-pattern bucket state, keyed by Pattern.Name. Mutated only
    // inside the matcher goroutine (R-006). The map size equals
    // len(patterns) and never grows after NewWatchdog.
    buckets map[string]*bucketState

    // Queue-full drop bookkeeping. Mutated by Ingest under enqueueMu
    // and by Run on ctx cancel (flush) under enqueueMu (R-008).
    enqueueMu sync.Mutex
    drops     dropEpisode

    // Lifecycle flags.
    ran       atomic.Bool // CAS-guarded single-shot Run (R-012)
    cancelled atomic.Bool // set just before Run returns; short-circuits Ingest (R-009)

    // Alert-output saturation counter, mutated only by matcher (R-010).
    suppressedByAlertOutput uint64
}

// bucketState — per-pattern token-bucket. Capacity 1 (spec
// Clarification Q3 / FR-004). Mutated only inside the matcher
// goroutine — no mutex required.
type bucketState struct {
    tokens          int       // 0 or 1
    lastRefill      time.Time // wall-clock of last refill or last emit
    suppressedCount uint64    // monotonic per-pattern WARN counter (FR-005)
}

// dropEpisode — queue-full drop bookkeeping. Mutated under
// enqueueMu (R-008).
type dropEpisode struct {
    count       uint64
    firstDropAt time.Time
}
```

### Constructor (locked, error-returning per FR-007a)

```go
// NewWatchdog constructs a Watchdog instance.
//
// Rejections (returns nil, error):
//   - len(patterns) > 0 && len(Pattern.Name) == 0 → ErrEmptyPatternName (P-1)
//   - duplicate Name within patterns                → ErrDuplicatePatternName (P-2, FR-007a)
//   - any Pattern.Regex == nil                       → ErrNilPatternRegex (P-3)
//   - any Pattern.RateLimit <= 0                     → ErrNonPositiveRateLimit (P-4)
//   - alerts == nil                                  → ErrNilAlertsChannel
//   - logger == nil                                  → ErrNilLogger
//
// Empty patterns slice is permitted (FR-014).
//
// On success: returns a *Watchdog with all per-pattern buckets
// initialized full (tokens=1, lastRefill=now) so the first match
// always alerts (FR-004 "starts full").
func NewWatchdog(patterns []Pattern, alerts chan<- Event, logger *slog.Logger) (*Watchdog, error)
```

The bare-return signature in the chunk doc is extended to return an
error per [research.md R-002](./research.md#r-002--newwatchdog-signature-returns-watchdog-error-not-bare-watchdog).
Recorded in plan.md Complexity Tracking entry #2.

### Method surface

```go
// Ingest is non-blocking. The line is defensively copied and
// enqueued on the internal channel. If the channel is full, the
// line is dropped and the drop is bookkept in the current drop
// episode (R-008). If the watchdog's Run has already returned,
// Ingest is a silent no-op (R-009).
//
// Ingest is safe for concurrent invocation from multiple producer
// goroutines (FR-010, spec assumption row stdout+stderr split).
func (w *Watchdog) Ingest(line []byte)

// Run drives the matcher loop. Single-shot: returns ErrAlreadyRan
// on the second invocation (R-012). On <-ctx.Done(), pending lines
// are dropped (NOT evaluated), one INFO log is emitted with the
// drop count, the cancelled atomic is set, and Run returns the
// wrapped ctx.Err() (R-007).
//
// Run never panics on normal-path errors; the only error returns
// are ErrAlreadyRan and wrapped ctx.Err().
//
// Spawns no goroutines beyond the matcher loop itself.
func (w *Watchdog) Run(ctx context.Context) error

// OnStderrLine satisfies the supervise.Watchdog interface declared
// at internal/supervise/lifecycle_interfaces.go:51 (locked at
// SDD-24). The method discards ctx and delegates to Ingest(line).
// Allows the SDD-23 CLI orchestrator to pass *Watchdog directly
// into Deps.Watchdog without an inline adapter (R-003).
func (w *Watchdog) OnStderrLine(ctx context.Context, line []byte)
```

The `OnStderrLine` method is additive beyond the chunk-doc API.
Recorded in plan.md Complexity Tracking entry #3.

### Sentinel errors

```go
var (
    ErrAlreadyRan            = errors.New("watchdog: Run already invoked")
    ErrEmptyPatternName      = errors.New("watchdog: pattern name is empty")
    ErrDuplicatePatternName  = errors.New("watchdog: duplicate pattern name")
    ErrNilPatternRegex       = errors.New("watchdog: pattern Regex is nil")
    ErrNonPositiveRateLimit  = errors.New("watchdog: pattern RateLimit must be positive")
    ErrNilAlertsChannel      = errors.New("watchdog: alerts channel is nil")
    ErrNilLogger             = errors.New("watchdog: logger is nil")
)
```

### Invariants

| ID | Invariant | Verified by test |
|----|-----------|------------------|
| W-1 | One matcher goroutine alive between `Run` start and `Run` return; zero before / after (FR-009) | `TestWatchdog_RunStopsOnCtxCancel` asserts `runtime.NumGoroutine` returns to pre-Run baseline within 250ms of cancel (SC-004) |
| W-2 | `Ingest` never blocks (FR-010a) | `TestWatchdog_IngestNonBlockingWhenQueueFull` measures wall-clock of Ingest under full-queue load; expect <1ms |
| W-3 | First match per pattern always emits (FR-004 "bucket starts full") | `TestWatchdog_PatternMatchEmitsAlert` |
| W-4 | Within `RateLimit` window after a match, additional matches for the same pattern are suppressed + WARN-logged (FR-004, FR-005) | `TestWatchdog_RateLimitBlocksExcess` asserts exactly one alert + N-1 WARN entries, each naming the pattern, none containing the matched line content |
| W-5 | Across `RateLimit` window boundary, the bucket refills and the next match emits (FR-004) | `TestWatchdog_BucketRefillsAfterInterval` uses the injected clock to step `now()` past `RateLimit` |
| W-6 | A line that matches no pattern emits zero events and zero log entries (FR-001, edge case) | `TestWatchdog_NoMatchNoAlert` |
| W-7 | A line that matches multiple patterns produces one event per pattern with budget (Edge Cases — multi-match) | `TestWatchdog_MultipleMatchesOnSameLine` configures two patterns, asserts both emit |
| W-8 | Watchdog NEVER calls into the state machine, the refill helpers, or the refresh scheduler (Constitution V, FR-003) | `TestWatchdog_NeverTransitionsState` wires the watchdog with no `Store` / `Refiller` / `Refresher` references in scope; the package's import list is asserted via `go list -f '{{ join .Imports "\n" }}'` — `internal/supervise` MUST NOT appear EXCEPT via the `supervise.Watchdog` interface contract referenced inside the OnStderrLine guard. (Note: the import is allowed for the compile-time interface guard `var _ supervise.Watchdog = (*Watchdog)(nil)` — but no function-call usage.) |
| W-9 | Pattern compilation happens exactly once (FR-008, SC-006) | `TestWatchdog_PrecompiledPatternsReused` uses a test pattern whose `Regex` is observable; ingests 10,000 lines and asserts compilation count remains 1 (compilation happens before NewWatchdog, not inside) |
| W-10 | Queue-full drops emit one WARN per episode (FR-010a, Clarification Q4) | `TestWatchdog_QueueFullDropEpisodeOnceWARN` fills queue, asserts one WARN naming the watchdog + final drop count + first-drop timestamp |
| W-11 | Alert-saturation drops emit one WARN per drop (FR-011, R-010) | `TestWatchdog_AlertOutputSaturatedDropsWARN` uses an unbuffered alerts channel + paused receiver; asserts one WARN per dropped alert with pattern name + monotonic timestamp, no line content |
| W-12 | Run returns within 250ms of ctx cancel (SC-004); pending lines dropped (R-007) | `TestWatchdog_RunStopsOnCtxCancel` |
| W-13 | Second Run returns ErrAlreadyRan immediately (R-012) | `TestWatchdog_RunSingleShot` |
| W-14 | After Run returns, Ingest is a no-op (FR-009, R-009) | `TestWatchdog_IngestAfterRunReturnIsNoop` |
| W-15 | Empty pattern set: NewWatchdog succeeds, Run accepts lines, emits zero events, stops on cancel (FR-014) | `TestWatchdog_EmptyPatternSetIsBenign` |
| W-16 | Concurrent Ingest is race-clean (FR-010) | `TestWatchdog_ConcurrentLogIngest` runs 8 goroutines × 500 ingests under `-race` |
| W-17 | WARN entries for rate-limit AND queue-full AND alert-saturation drops MUST NOT contain the matched line content (Clarification Q2, FR-005/006) | All three WARN-asserting tests above use a sentinel-tagged matched line and assert the sentinel does NOT appear in any captured WARN attribute |
| W-18 | No `string(secret)` site exists in the watchdog package (Constitution X) | `TestWatchdog_NoSecureBytesStringConversion` is a static grep over the source: `^\s*string\(.*[Ss]ecret` and `securebytes` — both expected to be absent (the package does not import securebytes by design) |
| W-19 | Watchdog does not import any non-stdlib + non-`internal/supervise/{interface-only}` package (Constitution XI) | `go list -deps -f '{{.ImportPath}}'` snapshot test asserts the dep set equals a locked allowlist |
| W-20 | `*Watchdog` satisfies the `supervise.Watchdog` interface (R-003) | compile-time guard `var _ supervise.Watchdog = (*Watchdog)(nil)` + runtime assertion in `TestWatchdog_SatisfiesSuperviseInterface` |

---

## 4. State machine

The watchdog itself has NO interaction with the supervisor state
machine (FR-003). The internal lifecycle of a single instance is:

```text
                  NewWatchdog
                       │
                       ▼
                ┌──────────────┐
                │  constructed │   ← Ingest is a no-op (Run not started)
                └──────┬───────┘     (cancelled=false; ran=false)
                       │ Run() invoked
                       ▼
                ┌──────────────┐
        ┌──── ─│   running    │ ◄── Ingest enqueues to lines channel
        │      └──────┬───────┘     matcher loop drains lines, emits Events
        │             │                or WARNs (rate-limit / queue-full / saturated)
        │             │ ctx.Done()
        │             ▼
        │      ┌──────────────┐
        │      │ shutting down│ ← drop pending lines (R-007)
        │      └──────┬───────┘   emit INFO log with drop count
        │             │           set cancelled=true
        │             ▼           release matcher
        │      ┌──────────────┐
        │      │   returned   │ ← Run has returned ctx.Err() (wrapped)
        │      └──────┬───────┘   Ingest now silently no-ops
        │             │ second Run() invocation
        │             ▼
        │      ┌──────────────┐
        └────► │  ErrAlreadyRan│ ← (CAS-rejected; no state change)
               └──────────────┘
```

Each transition is unidirectional. There are no resume / restart
paths inside the watchdog — a new pattern set or a fresh budget
state requires a fresh `*Watchdog`.

---

## 5. Memory and ownership rules

| Resource | Created by | Owned by | Released by | Released when |
|----------|------------|----------|-------------|---------------|
| `patterns []Pattern` (defensive copy in Watchdog) | NewWatchdog | Watchdog | GC | `*Watchdog` becomes unreachable after Run returns |
| `lines chan []byte` | NewWatchdog | Watchdog | GC (never closed; R-011 covers `alerts`, but `lines` is closed neither, for simpler late-Ingest handling) | `*Watchdog` becomes unreachable |
| line byte-slice elements inside `lines` | Ingest (defensive copy) | The channel slot until drained; then the matcher; then GC | matcher returns the slice to GC after evaluation | matcher loop iteration ends |
| `Event` value | matcher | the alerts channel and then SDD-28 router | GC (value types; no embedded pointer to internal buffer) | downstream consumer goroutine completes its read |
| `buckets map[string]*bucketState` | NewWatchdog | Watchdog | GC | `*Watchdog` becomes unreachable |
| `alerts chan<- Event` (borrow) | caller of NewWatchdog | caller | caller's responsibility | outside this package |
| `logger *slog.Logger` (borrow) | caller of NewWatchdog | caller | caller's responsibility | outside this package |

The watchdog allocates exactly two long-lived heap regions per
instance: the `lines` channel buffer (capacity * sizeof byte-slice
header) and the `buckets` map. Neither carries secret material
(Constitution X — see W-18 / E-5).

---

## 6. Test-driven invariants summary

Twenty invariants (W-1..W-20) are asserted across the test list
locked in [quickstart.md § 4](./quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4).
Coverage target (≥90%) is met by construction: every exported
symbol, every error return, every state-machine transition (§4),
and every WARN-emission site has at least one named test.

---

## 7. Integration with locked SDD-19..24 surfaces

| External symbol | Used as | Site | Constraint |
|-----------------|---------|------|------------|
| `supervise.Watchdog` (interface, SDD-24) | satisfied by `*watchdog.Watchdog` | compile-time guard in [internal/supervise/watchdog/watchdog.go](../../internal/supervise/watchdog/watchdog.go) | `OnStderrLine` adapter (R-003) |
| `supervise.Deps.Watchdog` (field, SDD-24) | populated with `*watchdog.Watchdog` | SDD-23 CLI wiring (post-merge of this chunk) | this chunk does NOT touch the wiring; only provides the value |
| `supervise.AlertClassLogPatternMatch` (enum, SDD-24) | downstream router classification | NOT consumed inside the watchdog | SDD-28 maps emitted `Event` → AlertClass; the watchdog is unaware |

The watchdog has zero outbound calls into `internal/supervise` at
function-call level. The only `internal/supervise` reference in
source is the `var _ supervise.Watchdog = (*Watchdog)(nil)`
compile-time guard (W-20). This satisfies the chunk-doc
anti-contract "Watchdog NEVER calls into the state machine or
refill/refresh helpers."
