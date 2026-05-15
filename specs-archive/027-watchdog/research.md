# Phase 0 Research — SDD-27 (`internal/supervise/watchdog`)

**Feature:** 027-watchdog
**Date:** 2026-05-13
**Spec:** [spec.md](./spec.md) · **Chunk doc:** [docs/sdd/SDD-27.md](../../docs/sdd/SDD-27.md)

This research locks every "HOW" decision the plan needs before
data-model.md / contracts / quickstart can be written. The spec
(WHAT) is fixed at five clarifications and fifteen functional
requirements. The chunk doc (SDD-27) locks an exported API and a
set of behaviour contracts. The job here is to resolve the
*internal* implementation choices the locked surface leaves open,
and to surface every Constitution Check concern (V, VIII, IX, X)
before Phase 1 begins.

---

## R-001 — Package location: `internal/supervise/watchdog` sub-package, NOT `internal/supervise`

**Decision.** The concrete log-pattern watchdog lives in a NEW
sub-package at `internal/supervise/watchdog`, importable as
`github.com/mrz1836/hush/internal/supervise/watchdog`. The chunk's
exported type is therefore `watchdog.Watchdog`, NOT
`supervise.Watchdog`.

**Rationale.** [internal/supervise/lifecycle_interfaces.go:51](../../internal/supervise/lifecycle_interfaces.go#L51)
already declares `type Watchdog interface { OnStderrLine(ctx, line) }`,
locked at SDD-24. The chunk doc names the SDD-27 concrete type
`Watchdog` — placing both in the same package would shadow the
SDD-24 interface and break every existing call site (the SDD-24
`Deps.Watchdog Watchdog` field, the `noopWatchdog{}` default, and
the `lineSplittingWriter` plumbing in [lifecycle_child.go:137](../../internal/supervise/lifecycle_child.go#L137)).
A sub-package preserves both identifiers verbatim: callers refer to
`supervise.Watchdog` for the interface and `watchdog.Watchdog` for
the concrete implementation. This is the same precedent SDD-18 set
for `internal/supervise/config`.

**Alternatives rejected.**
- **Rename the SDD-24 interface to `StderrObserver`.** Touches a
  package that is already merged and locked. Cascades into every
  downstream chunk that has wired `Deps.Watchdog`. SDD-27 must NOT
  alter any locked SDD-19..24 surface (chunk doc anti-contract row).
- **Rename the SDD-27 concrete type to `LogPatternWatchdog`.**
  Contradicts the chunk doc's locked name verbatim and the spec's
  "Watchdog Instance" key-entity row. Rejected.
- **Keep both in `internal/supervise` and reuse the same identifier
  as struct + interface via deduplication tricks (interface
  embedding + struct alias).** Go forbids it (cannot redeclare a
  package-level identifier). Rejected.

**Consequence for plan.** The "Files" line in the chunk doc
("`watchdog.go`, `*_test.go`") is honoured at the sub-package path:
`internal/supervise/watchdog/watchdog.go` and
`internal/supervise/watchdog/watchdog_test.go`. The
docs/PACKAGE-MAP.md update in Prompt-5 step 5 must reflect the
sub-package path. This deviation from the chunk-doc package row is
recorded as Complexity Tracking entry #1.

---

## R-002 — `NewWatchdog` signature returns `(*Watchdog, error)`, not bare `*Watchdog`

**Decision.** The chunk-doc-locked signature
`func NewWatchdog(patterns []Pattern, alerts chan<- Event, logger *slog.Logger) *Watchdog`
is extended to:

```go
func NewWatchdog(patterns []Pattern, alerts chan<- Event, logger *slog.Logger) (*Watchdog, error)
```

The constructor returns a non-nil error and a nil `*Watchdog`
whenever the supplied pattern set contains duplicate names
(spec FR-007a, Clarification Q5).

**Rationale.** Spec FR-007a, added by the 2026-05-13 clarification
session, mandates that `NewWatchdog` MUST reject (fail
construction) any pattern set with non-pairwise-distinct names. A
panic is not appropriate: pattern names come from operator-supplied
config (validated at SDD-18 time but operationally untrusted at
this seam), and Constitution IX's panic policy reserves panics for
unrecoverable invariant violations — not for operator-input errors
that callers (the orchestrator wiring) MUST handle gracefully and
surface to the operator. Returning an error is the only honest
option; the chunk-doc signature predates the clarification.

A sentinel error `ErrDuplicatePatternName` is declared at the
package level and wrapped via `%w` so callers can `errors.Is` it.

**Alternatives rejected.**
- **Keep the bare `*Watchdog` return and panic on duplicates.**
  Violates Constitution IX panic policy (operator input is not
  startup-wiring) and makes the failure mode invisible to the
  orchestrator's structured error reporting. Rejected.
- **Silently de-duplicate by name and continue.** Spec
  Clarification Q5 explicitly forbids this ("a clear
  construction-time error is preferable to silent attribution
  drift"). Rejected.
- **Validate uniqueness in SDD-18's config validator and assume
  uniqueness here.** SDD-18 currently validates the watchdog
  config's `Patterns []string` field for non-empty entries but does
  NOT enforce name-level uniqueness at the type the watchdog
  consumes (`watchdog.Pattern` is a richer struct). Defence-in-depth
  argues for re-checking at construction. Rejected (as sole
  defence).

**Consequence for plan.** Spec FR-007a is satisfied; chunk-doc API
extension recorded as Complexity Tracking entry #2.

---

## R-003 — Adapter method `(*Watchdog) OnStderrLine(ctx, line)` beyond chunk-doc API

**Decision.** The concrete `watchdog.Watchdog` exposes one
additional exported method beyond the chunk-doc list:

```go
func (w *Watchdog) OnStderrLine(ctx context.Context, line []byte)
```

The method discards `ctx` and delegates to `w.Ingest(line)`, satisfying
the `supervise.Watchdog` interface declared at
[internal/supervise/lifecycle_interfaces.go:51](../../internal/supervise/lifecycle_interfaces.go#L51).
A compile-time guard `var _ supervise.Watchdog = (*Watchdog)(nil)`
asserts the interface fit.

**Rationale.** The chunk doc lists six exported symbols (Pattern,
Event, Watchdog, NewWatchdog, Ingest, Run) and explicitly omits
OnStderrLine. But the orchestrator (SDD-24) consumes watchdog
implementations through the `Deps.Watchdog supervise.Watchdog`
field, whose method set is `OnStderrLine(ctx, line)`. Without the
adapter the orchestrator cannot accept a `*watchdog.Watchdog` value
— forcing every caller (the SDD-23 CLI wiring) to write its own
2-line adapter type. That punts a small piece of glue from a
single sub-package to a public API consumer, which costs more
duplication than it saves.

The adapter is a one-line forwarder; it adds no behaviour. The
chunk doc lists symbols required for the chunk's *own* test surface
(spec test list); orchestrator wiring is downstream. The plan
treats this as a non-controversial additive extension and records
it as Complexity Tracking entry #3.

The chunk doc API row is preserved (NewWatchdog/Ingest/Run remain
the documented external entry points); the adapter is a contract
satisfaction site, not a separate entry point.

**Alternatives rejected.**
- **Force every caller to write its own adapter.** Duplicates 4-6
  lines across `internal/cli/supervise.go` and any future caller.
  Rejected for reuse cost.
- **Promote `OnStderrLine` to the only producer entry point and
  drop `Ingest`.** Conflicts with the chunk-doc API freeze and
  makes the producer signature unnecessarily ceremonial (every
  caller would need to thread a context through what is, mechanically,
  a buffered enqueue). Rejected.
- **Define a separate exported adapter type
  `WatchdogObserver` wrapping `*Watchdog`.** Adds a redundant
  exported symbol with the same shape as the one-line method.
  Rejected.

---

## R-004 — Clock seam: unexported `now func() time.Time`, default `time.Now`

**Decision.** Each `Watchdog` instance holds an unexported field
`now func() time.Time` initialised to `time.Now`. Every wall-clock
read inside the run loop (event timestamp, token-bucket refill
calculation, WARN log timestamp) goes through `w.now()`. Tests
inject a fake clock via a package-internal helper
`setNowForTest(now func() time.Time)`; production callers MUST NOT
overwrite it.

**Rationale.** Constitution VIII / IX require deterministic tests
without `time.Sleep` for documented transitions. The spec's
assumption row "wall-clock time comes from the same monotonic clock
source already used elsewhere in the supervisor" pins the seam: an
unexported field with default `time.Now` matches the SDD-21
`Refresher` precedent. A published interface (`type Clock interface
{ Now() time.Time }`) would inflate the exported surface; the
chunk-doc API does not list a clock interface and Constitution IX
("define interfaces at the consumer") argues against publishing
one here.

The token bucket only ever reads `now()`. There is no need for a
mockable `time.Timer`/`time.Sleep` — the run loop is event-driven
on the line channel, not tick-driven. Tests therefore exercise the
bucket by injecting `now()` returns plus calling `Ingest`+ draining
the alert channel.

**Alternatives rejected.**
- **Published `Clock` interface in the watchdog package.** Public
  surface bloat with no consumer demand. Rejected.
- **Re-use `supervise.Clock` (SDD-24).** Cross-package coupling
  for an internal seam. The supervise.Clock interface is consumed
  by the orchestrator, not the watchdog. Rejected.
- **`time.Now()` direct calls without a seam.** Defeats
  deterministic testing of rate-limit semantics (FR-004 verifying
  bucket refill window). Rejected.

---

## R-005 — Internal line channel: buffered, capacity 512, struct-of-bytes element

**Decision.** The `Watchdog` holds an unexported field
`lines chan []byte` of capacity 512 elements. `Ingest` performs a
non-blocking send via the `select { case w.lines <- line: ... default: ... }`
idiom. `Run` reads from `lines` in a single goroutine. The 512
constant is unexported (`lineChannelCapacity = 512`).

**Rationale.** The producer side is the SDD-20 child-tail loop
(`lineSplittingWriter` in [lifecycle_child.go:299](../../internal/supervise/lifecycle_child.go#L299)),
which already truncates lines to `stderrLineCap = 4 KiB`. A
buffered capacity of 512 lines (~2 MiB worst case) absorbs a burst
without blocking the child's stderr writer (Constitution V
loud-failure principle) while staying small enough that the
watchdog's memory footprint is bounded. 512 is comfortably above
the realistic stderr burst rate for a misconfigured retry loop
(spec SC-002 uses 1,000 lines/sec; even at that rate the queue
drains as fast as the matcher loops, and the spec-described drop
WARN handles the residual).

The element type is `[]byte`. The producer passes a slice it owns;
the watchdog MUST defensively copy the slice on Ingest before
enqueueing, because SDD-20's `lineSplittingWriter` reuses its scan
buffer between calls. Without the copy, the matcher would race on
shared bytes.

**Alternatives rejected.**
- **Unbuffered channel + drop on full.** Equivalent to a 0-buffer
  buffered channel; loses the SC-001 "alert within 100ms"
  responsiveness because every Ingest must hand off to a receiver
  before returning. Rejected.
- **Lock-protected slice + signal.** Higher complexity, no
  measurable benefit, and contention model already proven by the
  channel idiom. Rejected.
- **Capacity 64.** Spec SC-002 burst test (1,000 matching lines)
  would slip into drop-WARN territory under normal conditions on
  slower CI; would surface flakes without serving any safety
  purpose. Rejected.
- **Capacity 8,192.** Bounded-memory hygiene fails for a non-flow-
  controlled producer; misconfigured patterns matching every line
  could sustain a 32 MiB queue. Rejected.
- **Pass an unowned byte slice without copy.** Race-detector
  failure with the SDD-20 reused scan buffer. Rejected.

---

## R-006 — Token bucket shape: per-pattern `lastRefill time.Time` + lazy refill on evaluation

**Decision.** Each pattern is paired with a per-pattern bucket
state struct stored in a fixed-size map indexed by pattern name:

```go
type bucketState struct {
    tokens          int        // 0 or 1; capacity 1 (spec FR-004)
    lastRefill      time.Time  // wall-clock of last refill or last alert emission
    suppressedCount uint64     // monotonic counter for WARN logs (spec FR-005)
}
```

The bucket starts with `tokens = 1, lastRefill = constructionTime`
so the first match always alerts (spec FR-004 "starts full").

On every match evaluation the bucket lazily refills:
`if w.now().Sub(state.lastRefill) >= pattern.RateLimit { tokens = 1; lastRefill = w.now() }`.
If `tokens == 1`, the alert is emitted, `tokens = 0`, `lastRefill = w.now()`.
If `tokens == 0`, the alert is suppressed, `suppressedCount++`, WARN
is logged.

**Rationale.** The "classical token bucket with capacity 1 and
refill rate of 1 token per RateLimit seconds" of spec Clarification
Q3 maps perfectly to a single `lastRefill` timestamp + lazy
evaluation. The lazy form needs no goroutine, no ticker, and no
extra synchronisation — Constitution IX wins. The single matcher
goroutine owns the map; no mutex is required.

The `lastRefill` field doubles as "time the last alert was emitted"
when tokens drain. This is mathematically identical to a separate
"last emit" timestamp for capacity-1 buckets.

`suppressedCount` is a monotonic per-pattern counter that resets
NEVER inside one watchdog instance. Spec FR-005 "WARN entry
carries pattern name, monotonic timestamp, and a suppressed-match
counter only" — counter increments only inside the matcher
goroutine, so atomic ops are not needed.

**Alternatives rejected.**
- **`golang.org/x/time/rate.Limiter`.** Introduces a new direct
  dependency; chunk doc anti-contract row forbids new go.mod deps.
  Rejected.
- **Goroutine + ticker per pattern.** Violates Constitution IX
  "every goroutine has a clear owner and termination condition";
  scaling-by-pattern goroutine count is hostile to the single-loop
  invariant. Rejected.
- **Rolling-window counter.** Spec Clarification Q3 picks the
  classical token-bucket shape; rolling-window was the rejected
  alternative there. Locked.
- **Atomic counter for `suppressedCount`.** The counter is mutated
  only by the matcher goroutine. Atomics add cost without
  correctness benefit. Rejected.

---

## R-007 — Run cancellation semantics: drop pending lines on `<-ctx.Done()`, log INFO with count

**Decision.** When `<-ctx.Done()` fires, `Run` exits the select
loop immediately. Lines already buffered in the internal channel
are DROPPED (not drained, not evaluated). Before returning,
`Run` emits one INFO-level structured log entry naming the
watchdog and the count of dropped-on-cancel lines. The function
returns `ctx.Err()` (wrapped).

**Rationale.** The Plan Prompt explicitly invites this decision:
"Run returns when ctx cancels (drains the channel? — document the
choice in the plan; recommended: drop pending lines on cancel,
log INFO with count)." The user's recommendation matches the
spec's SC-004 "run-loop returns within 250 ms of cancel" — a
drain-on-cancel could in principle process thousands of queued
lines and miss the 250 ms target. Cancellation is normal-path
shutdown, not error-path recovery; emitting alerts for a child
process whose supervisor is tearing down is not useful and risks
confusing operators who see a "fresh" alert during shutdown.

INFO (not WARN) because cancellation-time line drops are an
expected lifecycle event, not a degraded operating mode. Spec
FR-009 mandates "stop cleanly — with no leaked goroutines and no
further alert emissions or log writes" once Run returns; the
single INFO line is the LAST write inside the function and is
emitted BEFORE Run returns, so the post-return silence contract
is preserved.

**Alternatives rejected.**
- **Drain the channel and evaluate every queued line, then
  cancel.** Conflicts with SC-004 250 ms budget under burst
  conditions. Rejected.
- **Drain WITHOUT evaluating (sink to /dev/null silently).** No
  benefit over fast-exit; loses the drop count. Rejected.
- **WARN on cancel-drop.** Cancel-drop is not a degraded mode;
  WARN trains operators to ignore real WARNs. Rejected (Constitution V).
- **Return `nil` instead of `ctx.Err()`.** Loses information; the
  caller cannot distinguish clean shutdown from internal failure.
  Constitution IX errors-wrap principle argues for the ctx.Err()
  return. Rejected.

---

## R-008 — Drop episode bookkeeping: emit one WARN on episode close, not on first drop

**Decision.** The watchdog tracks one piece of process-local state
to coalesce queue-full drops into episodes:

```go
type dropEpisode struct {
    count       uint64    // drops in current episode, 0 outside an episode
    firstDropAt time.Time // wall-clock of first drop in current episode
}
```

State machine:
- Outside an episode (`count == 0`): a drop sets `count = 1`,
  `firstDropAt = now()`. NO WARN is emitted yet.
- Inside an episode (`count > 0`): a drop increments `count`. NO
  WARN is emitted yet.
- Inside an episode, a successful enqueue resolves the episode:
  emit ONE WARN entry naming the watchdog, `count`, and
  `firstDropAt`. Reset `count = 0`.
- On `<-ctx.Done()`, if `count > 0`, flush one final WARN with the
  same shape before exiting.

**Rationale.** Spec Clarification Q4 + FR-010a: "single WARN-level
structured log entry per drop *episode* (not per dropped line),
naming the watchdog and the drop count for that episode". The
episode-end semantic gives the operator a final, accurate count
once the burst has passed — emitting on first drop would either
require a second WARN to report the total (defeating "single") or
report an incomplete count (defeating the operator value of the
log). The cancel-time flush prevents an in-progress episode from
silently disappearing on shutdown.

`dropEpisode` is mutated only by the matcher goroutine (which
receives both the enqueue-success signal from Ingest via a
sentinel-tagged channel send and the drop signal from Ingest's
default-branch — see R-010 for the dispatch). Because the
matcher's enqueue path is `select { case w.lines <- copy: } default: { w.recordDrop() }`,
the drop bookkeeping LIVES on the Ingest side (caller goroutine).
A small `sync.Mutex` (`dropMu`) protects the `dropEpisode` value
across concurrent Ingest callers (FR-010 concurrent ingestion).

**Alternatives rejected.**
- **Emit one WARN per drop.** Spec FR-010a explicitly forbids
  per-line WARNs in the queue-full case (vs. FR-005 which mandates
  per-match WARNs for rate-limit drops — these are intentionally
  different). Rejected.
- **Emit WARN on first drop of episode + suppress the rest.**
  Reports incomplete count; operator cannot tell whether 1 or 1000
  lines were lost. Rejected.
- **Lock-free atomic counter without firstDropAt.** Without the
  start timestamp, the WARN cannot tell the operator how long the
  episode ran — a piece of diagnostic information that's free to
  collect. Rejected.
- **Use a `sync/atomic` Uint64 instead of mutex.** Two correlated
  fields (count, firstDropAt) cannot be updated atomically without
  a mutex anyway. Rejected.

---

## R-009 — Concurrent Ingest from multiple producers: mutex-guarded enqueue, drop bookkeeping in lock

**Decision.** `Ingest` is callable concurrently (spec FR-010); the
implementation holds a tiny `sync.Mutex` named `enqueueMu` around
the channel send + drop-episode bookkeeping:

```go
func (w *Watchdog) Ingest(line []byte) {
    if w.cancelled.Load() { return } // post-Run-return no-op
    cp := make([]byte, len(line))
    copy(cp, line)
    w.enqueueMu.Lock()
    defer w.enqueueMu.Unlock()
    select {
    case w.lines <- cp:
        w.flushDropEpisodeIfNeeded() // emit WARN if episode just closed
    default:
        w.recordDropInEpisode()      // increment counter / start episode
    }
}
```

**Rationale.** Spec FR-010 mandates safety under concurrent
ingestion (the SDD-20 plumbing splits stdout + stderr into two
goroutines). Without the mutex, two concurrent Ingest calls could
race on the drop-episode bookkeeping (R-008). The channel send
itself is goroutine-safe — but the surrounding "did this enqueue
close an episode?" check is not. The mutex is held only across
the non-blocking select + a tiny constant amount of bookkeeping;
contention is minimal under realistic two-producer load.

The `cancelled atomic.Bool` early-exit short-circuits Ingest after
Run returns; without it, late Ingest calls could leak `cp` allocations
or trigger a panic on a closed channel. (R-011 argues against
closing the channel, so the panic risk is in fact avoided structurally
— but the no-op-after-Run contract is still operator-visible per
FR-009.)

**Alternatives rejected.**
- **Mutex-free using a `sync/atomic.Pointer[dropEpisode]` CAS
  loop.** Higher complexity for an Ingest path that's already
  bounded by a channel send. Premature optimisation. Rejected.
- **One mutex per producer (shard).** Producers are unknown to the
  watchdog; the sharding key is not visible. Rejected.
- **No mutex; accept the data race on `dropEpisode`.** Fails
  `go test -race`, violates Constitution VIII race-clean gate.
  Rejected.

---

## R-010 — Alert-output-saturation handling: non-blocking send, WARN per drop (NOT episode-based)

**Decision.** Inside the matcher loop, alert emission uses a
non-blocking send:

```go
select {
case w.alerts <- ev:
default:
    w.suppressedByAlertOutput++
    w.logger.LogAttrs(ctx, slog.LevelWarn,
        "watchdog: alert dropped (output channel saturated)",
        slog.String("pattern", pat.Name),
        slog.Time("ts", w.now()),
        slog.Uint64("suppressed_total", w.suppressedByAlertOutput))
}
```

WARN is emitted per drop (no coalescing), matching the rate-limit
WARN semantic. The WARN entry MUST NOT carry the matched line
content (spec Clarification Q2). It MUST carry the pattern name
and a monotonic timestamp.

**Rationale.** Spec FR-011 mandates that a saturated alert sink
MUST drop and MUST WARN. Spec Clarification Q2 explicitly groups
alert-output-saturated drops with rate-limit drops for the
"no-line-content in WARN" rule, so the two follow the same WARN
shape. FR-006 mandates per-suppressed-match WARN for rate-limit
drops; the alert-output case shares the same operator-correlation
need (one WARN per dropped alert lets the investigator
reconstruct exactly which matches got lost).

This is INTENTIONALLY different from the queue-full Ingest drop
case (R-008), which uses episode coalescing. The two
loss-of-information failure modes have different operator
diagnostic value: queue-full drops are bursty and high-volume by
nature; alert-output drops imply the downstream consumer
(SDD-28's router) is stalled and the operator needs to investigate
the consumer.

**Alternatives rejected.**
- **Block on alert send.** Stalls the matcher; the next Ingest
  would queue-full immediately. Spec FR-011 explicitly forbids
  blocking. Rejected.
- **Episode-coalesce alert-output drops.** Loses the per-pattern
  attribution that operators need to map a stalled router back to
  a specific pattern category. Rejected.
- **Include the dropped line content in the WARN.** Spec
  Clarification Q2 forbids it. Rejected.

---

## R-011 — Channel lifecycle: do NOT close the `alerts` channel

**Decision.** The watchdog NEVER closes the `alerts chan<- Event`
that was passed to `NewWatchdog`. The channel is owned by the
caller (the SDD-23 orchestrator wiring or test harness); the
watchdog only sends on it. On Run return, the channel is left
open.

**Rationale.** Go idiom: only the sender should close. But this
watchdog is one of potentially several alert producers in SDD-28's
upcoming router (SDD-28 will wire validator alerts, exit-78
alerts, and refresh-failure alerts onto the same downstream sink).
Premature close from any single producer would break the others.
The chunk doc's "Watchdog NEVER calls into the state machine or
refill/refresh helpers. Its only output is the alerts channel
(consumed by SDD-28)" implies a one-way emit relationship; closing
is not part of that contract.

This decision also avoids the late-Ingest panic risk (R-009): if
the channel were closed, a racing send would panic. Leaving it
open + relying on the cancelled atomic short-circuit is
straightforward.

**Alternatives rejected.**
- **Close `alerts` in `Run` defer.** Breaks the SDD-28 multi-
  producer model. Rejected.
- **Close `lines` in `Run` defer.** `lines` is owned by the
  watchdog (created in NewWatchdog), so closing is permissible in
  principle. But closing while Ingest is in flight would panic. The
  `cancelled atomic.Bool` guard handles late-Ingest correctness;
  closing `lines` adds nothing. Rejected.

---

## R-012 — Single-shot `Run`: sync.Once-guarded; second call returns sentinel

**Decision.** Run is single-shot. A `sync.Once`-style guard via a
`ran atomic.Bool` field prevents a second invocation:

```go
func (w *Watchdog) Run(ctx context.Context) error {
    if !w.ran.CompareAndSwap(false, true) {
        return ErrAlreadyRan
    }
    // ... event loop ...
}
```

`ErrAlreadyRan` is a package-level sentinel
(`var ErrAlreadyRan = errors.New("watchdog: Run already invoked")`).

**Rationale.** Spec FR-007 + FR-009: "The pattern set MUST be
fixed for the lifetime of a watchdog instance — patterns are
accepted at construction time and never mutated afterwards.
Reconfiguring patterns requires constructing a new watchdog." This
implies a one-shot lifecycle: construct → Run → ctx cancel →
discard. Allowing Run to be called a second time on the same
instance would risk the matcher reading from a half-drained
channel or interacting with stale token-bucket state from the
prior run; better to fail fast with a sentinel error so the
orchestrator's wiring bug is caught at boot.

The matcher-loop ownership becomes unambiguous: there is ever
exactly one matcher goroutine alive per `*Watchdog`, and its
termination is tied to the single Run call.

**Alternatives rejected.**
- **Allow Run to be called multiple times concurrently.** Creates
  multiple matcher goroutines racing on the same token-bucket map
  + drop bookkeeping. Mutex coverage would have to expand. Rejected.
- **Allow Run to be called multiple times serially.** The
  cancelled atomic from the first run would short-circuit Ingest
  on the second. Confusing semantics. Rejected.
- **Use `sync.Once.Do(func(){...})` with a captured `err`.** More
  ceremony than a CAS on a single bool. Rejected.

---

## R-013 — Event struct: value semantics, defensive line copy

**Decision.** `Event` is a value type with three fields:

```go
type Event struct {
    Pattern string    // matched pattern name (Pattern.Name)
    Line    string    // matched line, defensively converted from []byte
    Time    time.Time // wall-clock of match (w.now())
}
```

The `Line` field is a `string`, not `[]byte`. The matcher
constructs the string via `string(line)` at the moment of alert
emission; this is a non-secret conversion (operator log content,
not vault material) and is the SOLE `string()` site in the package.

**Rationale.** Constitution X bans `string(*SecureBytes)` and any
`string(...)` of vault secret material. Child stderr is operator
log content, not vault material — Constitution X does not apply.
The chunk doc Event signature pins `Line string`. A value-type
Event with string field gives the downstream router (SDD-28) zero
ownership concerns: the Event can be passed across goroutines,
stored, serialized, without aliasing the internal channel buffer.

The defensive `string()` copy at emit time means the upstream
`lines` channel buffer can be reclaimed (the matcher returns the
slice to gc) without the Event holding a dangling reference.

**Alternatives rejected.**
- **`Line []byte`.** Forces every downstream consumer to copy on
  receive. Chunk-doc Event signature says `Line string`. Rejected.
- **Defer the string conversion to SDD-28.** Forces SDD-28 to
  reason about the line buffer's lifetime. Rejected.

---

## R-014 — Logger field: borrow only; no LogValuer needed on Pattern/Event

**Decision.** `Watchdog` holds a `logger *slog.Logger` borrowed
reference. Neither `Pattern` nor `Event` has a `LogValue() slog.Value`
method; both are non-secret value types (Pattern.Name is operator
config, Pattern.Regex is operator config, Event.Line is operator
log content, Event.Time is a timestamp).

**Rationale.** Constitution X mandates LogValuer redaction for
types holding secret material. None of the watchdog's types do.
Pattern.Regex contains a compiled regex (operator-supplied
string), Event.Line is non-secret child stderr.

The chunk's WARN entries are sourced from explicit slog attributes
(pattern name, drop counts, timestamps); the matched line content
is excluded by Clarification Q2. The line never leaks into a log
attribute.

**Alternatives rejected.**
- **Add LogValuer to Pattern just for hygiene.** No secret risk;
  adds maintenance burden. Rejected.
- **Pass logger through Run instead of constructor.** Chunk-doc
  NewWatchdog signature pins logger at constructor time. Rejected.

---

## R-015 — Pattern source of truth: SDD-18 config's compiled regex set

**Decision.** The `Pattern` slice passed to `NewWatchdog` is built
by the SDD-23 CLI wiring (post-merge) from
`config.Supervisor.Watchdog.Patterns []string` via
`regexp.Compile` (NOT `regexp.MustCompile`). The watchdog package
itself MUST NOT compile regex strings; it consumes pre-compiled
`*regexp.Regexp` values. RateLimit per pattern is derived from
`config.Supervisor.Watchdog.MaxAlertsPerHour` as
`time.Duration((3600.0 / float64(maxAlertsPerHour)) * float64(time.Second))`
(equiv. 600 s for the default of 6).

**Rationale.** Spec FR-008 ("Pattern compilation MUST occur
exactly once, at watchdog construction") combined with spec
assumption row "the operator-supplied regex patterns in the
configuration file have already been validated and compiled by
the config-load path (SDD-18 territory)". Compilation happens at
SDD-18 / SDD-23 wiring time so config errors surface to the
operator at boot, not deep inside the supervisor's hot path. The
watchdog only assumes well-formed patterns.

The chunk-doc instruction "Patterns are compiled by the caller
(operator-supplied regex strings → regexp.MustCompile → Pattern)"
is honoured EXCEPT that `regexp.Compile` is preferred over
`regexp.MustCompile` for the production wiring path (the SDD-23
caller already returns errors from config load; MustCompile would
panic on the operator's typo, violating Constitution IX panic
policy). Test code MAY use MustCompile for brevity.

**Alternatives rejected.**
- **Compile regex inside NewWatchdog from `[]string`.** Violates
  the chunk-doc producer/consumer split and FR-008's
  one-compile-per-instance requirement. Rejected.
- **Cache the compile in the watchdog package using a package-
  level map.** Constitution IX forbids package-level mutable state.
  Rejected.

---

## R-016 — Coverage measurement gate (90%)

**Decision.** Coverage is measured by
`go test -cover ./internal/supervise/watchdog/` after Phase 5
implements. The 90% target (chunk doc) is computed on the
package's statement coverage. The test list in
[quickstart.md](./quickstart.md) is sized so each named test covers
one observable behaviour and is independent of every other —
yielding ≥90% by construction without relying on coincidence-
coverage from neighbouring tests.

**Rationale.** Constitution VIII Test Priority table places
"supervisor state machine, validators" at 95%; the watchdog's
position in the priority hierarchy is below the supervisor state
machine (alert-only, no control-plane authority) so 90% per the
chunk doc is appropriate. The chunk doc and the user prompt
agree on the 90% target.

**Alternatives rejected.**
- **Aim for 100% to align with crypto-tier targets.** The
  watchdog is not crypto-tier; over-specifying coverage would
  push tests into trivial getter coverage. Rejected.
- **Aim for 80% repo-wide constitutional baseline.** Below the
  chunk-doc target. Rejected.

---

## Decision summary

| ID | Decision | Spec / Chunk-doc anchor |
|----|----------|-------------------------|
| R-001 | Sub-package `internal/supervise/watchdog` | Avoids SDD-24 `Watchdog` interface collision |
| R-002 | `NewWatchdog(...) (*Watchdog, error)` returns error | FR-007a (Clarification Q5) |
| R-003 | Adapter `OnStderrLine` for `supervise.Watchdog` interface | SDD-24 plumbing reuse |
| R-004 | Unexported `now func() time.Time` clock seam | Constitution IX, SDD-21 precedent |
| R-005 | Internal channel `chan []byte`, capacity 512, defensive copy | FR-010, FR-010a, SC-002 |
| R-006 | Per-pattern bucket: `tokens (0/1)` + `lastRefill time.Time` | FR-004 (Clarification Q3) |
| R-007 | Run drops pending on ctx cancel, INFO log with count | SC-004, FR-009 |
| R-008 | Queue-full drop episodes: one WARN per episode close | FR-010a (Clarification Q4) |
| R-009 | Mutex-guarded Ingest; atomic `cancelled` for post-Run | FR-009, FR-010 |
| R-010 | Alert-saturation: WARN per drop, line content excluded | FR-011, FR-005 (Clarification Q2) |
| R-011 | Watchdog NEVER closes `alerts` | SDD-28 router multi-producer model |
| R-012 | Single-shot Run via `ran atomic.Bool` | FR-007/009 |
| R-013 | Event value type, defensive `string(line)` at emit | Chunk-doc Event shape |
| R-014 | No LogValuer on Pattern/Event; non-secret types | Constitution X |
| R-015 | Caller pre-compiles regex; watchdog reuses | FR-008 + assumption |
| R-016 | Coverage 90% via `go test -cover` | Chunk doc target |
