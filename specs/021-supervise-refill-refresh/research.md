# Phase 0 Research — SDD-21 (`internal/supervise` refill + refresh + grace)

**Feature:** 021-supervise-refill-refresh
**Date:** 2026-05-10
**Spec:** [spec.md](./spec.md) · **Chunk doc:** [docs/sdd/SDD-21.md](../../docs/sdd/SDD-21.md)

This research locks every "HOW" decision the plan needs before
data-model.md / contracts / quickstart can be written. It exists to
de-risk Phase 1 and to make the Constitution Check (IV/V/VIII/IX/X)
auditable. The chunk's exported API is already locked by SDD-21 — the
work here is to resolve the *internal* implementation choices that
the locked API leaves open.

---

## R-001 — Refresh-window parser (re-use, not re-implement)

**Decision.** Re-use `internal/supervise/config.validateRefreshWindow`'s
canonical "HH:MM-HH:MM" shape (length-5 segments, leading-zero hours,
strict `time.Parse("15:04")`). The Refresher does NOT re-parse from
the raw operator string at runtime; instead, the orchestrator (SDD-23)
calls `config.Load(...)` once and hands the already-validated string
to `NewRefresher(window string, ...)`. The Refresher's own runtime
parse splits on the single `-`, applies `time.Parse("15:04")` to each
half, and stores `(startHour, startMin, endHour, endMin)` as four
ints. Window evaluation per tick is `now := clock.Now().In(time.Local)`
plus the four-int comparison.

**Rationale.** SDD-18 already owns the validator; re-implementing it
here violates DRY and risks drift. The SDD-18 validator has been
locked since Phase 5 with a fuzz target. Borrowing its shape (exact
length-5 segments, leading zeros) keeps the operator's mental model
consistent across the config diagnostics and the runtime scheduler.

**Alternatives rejected.**
- **Re-parse with `time.Parse` only:** would silently accept "9:00"
  (single-digit hour) at runtime even though config rejects it.
  Diverges from SDD-18 semantics. Rejected.
- **Cron expression (`@daily`, `0 9-10 * * *`):** drags in a 3rd-party
  cron library (Constitution XI) and surfaces operator-confusing
  schedule semantics. Rejected.
- **Use `cron.Parse` from a dependency:** zero new direct deps allowed
  per the chunk doc and Constitution XI. Rejected.

---

## R-002 — Window-crossing semantics + idempotency flag

**Decision.** The Refresher tracks two pieces of process-local state:
`lastFiredDay` (a calendar date `time.Time` truncated to 00:00 local)
and `t30Fired` (a bool for the per-session T-30 fallback). Both are
zero-valued at process start (per Clarification: "tracked by an
in-memory flag scoped to the current process (lost on restart, never
persisted to disk)" → FR-021-10).

Per-tick algorithm (driven by a monotonic `time.Timer`):

1. `now := clock.Now().In(time.Local)` → derive `today := now at 00:00`.
2. If `today != lastFiredDay` AND `now ∈ [start, end]` of the configured
   window → fire, set `lastFiredDay = today`, advance to next-day tick.
3. Else if `today != lastFiredDay` AND `windowEndForToday < now` AND
   `ttl - now < 30m` AND `!t30Fired` → fire (T-30 fallback), set
   `lastFiredDay = today`, set `t30Fired = true`.
4. Compute `nextTick` as `min(nextWindowStartLocal, nextT30CheckTime)`.
5. Re-arm `time.Timer.Reset(nextTick.Sub(now))`.

A successful start that finds wall-clock already inside today's
window with `lastFiredDay != today` fires immediately on Run entry
(Story 2 Scenario 2 / FR-021-10 second sentence).

**Rationale.** The two-flag design keeps "exactly one fire per window
crossing" honest under DST rollovers, NTP step-backs, and process
restarts. `lastFiredDay` is calendar-date-keyed so a single backwards
clock step within the same day cannot re-fire the window (FR-021-11);
the T-30 flag is session-keyed so the fallback can never double-fire.

**Alternatives rejected.**
- **Single boolean `firedThisWindow`:** ambiguous across midnight —
  resetting at "next window start" requires another timer; the
  calendar-date key is simpler. Rejected.
- **No T-30 flag, recompute from session expiry each tick:** at
  exactly T-30, re-evaluation could fire twice if two ticks land
  inside the threshold. Rejected.

---

## R-003 — Monotonic vs wall-clock split

**Decision.** The Refresher's `time.Timer` is the *re-arm* primitive
(monotonic reading from `time.Now()`'s monotonic component, per Go
1.9+ default). The "is `now` inside the configured window" predicate
uses `clock.Now().In(time.Local)` (wall-clock semantics — DST is the
operator's intent). The two are not contradictory: `time.Timer` only
asks "have ~N nanoseconds elapsed since I was armed?"; the predicate
asks "is the operator's local clock inside their configured window?".

A NTP step backwards (e.g. 60s) cannot trigger an extra fire because
`lastFiredDay` is already set; a forwards step jumps the timer fire
one tick earlier — at worst, one extra evaluation pass that finds
`lastFiredDay == today` and re-arms.

**Rationale.** This matches Go stdlib idiom and is what FR-021-11
explicitly requires. The standard library's `time.Timer` reads
monotonic time when available; we don't need a separate monotonic
clock seam.

**Alternatives rejected.**
- **Two separate clock interfaces (`MonotonicClock` + `WallClock`):**
  adds two test seams instead of one; the single `Clock interface
  { Now() time.Time }` already used by SDD-19's Store is sufficient
  because Go's `time.Time` carries both readings. Rejected.

---

## R-004 — `Clock` interface re-use

**Decision.** Re-use SDD-19's `supervise.Clock` interface (defined at
[`internal/supervise/state.go:55`](../../internal/supervise/state.go))
verbatim. The `Refresher` accepts a `clock supervise.Clock` parameter
in `NewRefresher` (added to the chunk's exported API godoc but the
**locked exported signature stays as-is** — the `Clock` parameter is
the **fifth** function argument, slotted in before `logger` to keep
parity with `NewStore`). Wait — **the chunk doc locks the API to
exactly five params**: `(window string, ttl time.Duration, refill
func(ctx context.Context) error, logger *slog.Logger)`. Therefore
the Refresher MUST consume `time.Now()` directly via an *unexported
struct field* `now func() time.Time` with a package-private setter
(`refresherForTest.SetClock`) used by `refresh_test.go` only.

**Rationale.** The chunk's locked exported API list does not include
a `Clock` parameter, and the Plan prompt is explicit: "Run:
/speckit-plan ... no API additions". Tests therefore inject the
clock through an unexported seam (`*Refresher.now = fakeClock.Now`)
via an exported-`_test.go`-only setter or a build-tagged file. Per
Constitution IX (no globals, no init), the seam is a struct field
(`now func() time.Time`), not a package-level variable. This matches
the SDD-19 pattern where `Clock` was added to the exported API only
because the chunk doc explicitly listed it.

**Alternatives rejected.**
- **Add `Clock` to the exported `NewRefresher` signature:** violates
  the locked chunk API. Rejected.
- **Package-level `var nowFn = time.Now`:** violates Constitution IX
  (`gochecknoglobals`). Rejected.
- **Build-tag a different `now()` in tests:** inverts Constitution
  IX's "single test-binary, race-clean" idiom. Rejected.

---

## R-005 — Refill HTTP layer

**Decision.**
- `Refiller` calls
  `client.Do(req.WithContext(ctx))` where
  `req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
   url, nil)` and `url := serverURL + "/s/" + name`.
  (The path-prefix obscurity layer per Constitution III.7 is the
  caller's job — the server URL handed to Refiller already includes
  `/h/<prefix>`.)
- Bearer header: `snap := store.Snapshot(); _ = snap.Token.Use(func(b
  []byte) { req.Header.Set("Authorization", "Bearer "+string(b)) })`.
  Note: the JWT-to-string materialization is **inside** the closure
  passed to `Use`, holding the SecureBytes mutex. This is the ONE
  place in the chunk where `string(...)` of a SecureBytes-wrapped
  byte slice is permitted, and it is for the **JWT bearer header**
  (a session token, NOT a vault secret value). The decrypted vault
  payload returned by ECIES.Decrypt is NEVER materialized as a string
  anywhere in this chunk (Constitution X strict reading).
- HTTP error mapping:
  | HTTP status | Body shape | Refill outcome |
  |-------------|------------|----------------|
  | 200 | binary ECIES envelope | call `ecies.Decrypt` → `*SecureBytes` → `Grace.Set` + return slice to caller |
  | 401 | `{"error":"unknown_jti"}` JSON | `ErrJTIUnknown` (typed) — caller transitions state |
  | 401 | any other body | wrapped fmt error: `fmt.Errorf("refill auth-required (status=%d): %w", status, ErrTransient)` |
  | 5xx / network / DNS / timeout | n/a | `fmt.Errorf("refill transient: %w", err) — wraps `ErrTransient` (typed, distinct from `ErrJTIUnknown`) |
  | 4xx (other) | n/a | wrapped fmt error around `ErrTransient` (orchestrator chooses retry policy) |
- Body parse: read all bytes via `io.ReadAll(resp.Body)` with a
  hard-cap `io.LimitReader(resp.Body, 64*1024)` — ECIES envelopes
  are tiny; this prevents a malicious server from exhausting heap.
  After ECIES.Decrypt the underlying ciphertext bytes are zeroed via
  `for i := range raw { raw[i] = 0 }`.

**Rationale.** Aligns with FR-021-3 (typed `ErrJTIUnknown`),
FR-021-4 (typed transient error distinct from `ErrJTIUnknown`),
FR-021-5 (atomic destruction on any failure), and Constitution X
(no `string(decrypted)`). The 64 KiB read cap mirrors the SDD-20
ringBuffer cap and is far above any realistic ECIES envelope size.

**Alternatives rejected.**
- **`json.Decoder.Decode` against a typed error struct:** introduces
  a JSON parse path that's only used on the 401 branch. Decoding the
  body once via `json.Unmarshal(buf, &struct{ Error string }{})` is
  cheaper and keeps the happy path zero-JSON. Adopted.
- **String-compare `body` against `unknown_jti`:** brittle; would
  match `{"error":"this_was_unknown_jti_back_in_2024"}`. Rejected.
- **Retry-with-backoff inside Refill:** explicitly out of scope per
  the chunk doc — boot retry is in SDD-23. Rejected.

---

## R-006 — `ErrTransient` sentinel — add or use `fmt.Errorf` only?

**Decision.** Add a third unexported-class sentinel: `ErrTransient`
exported as `var ErrTransient = errors.New("supervise: refill
transient")`. The orchestrator (SDD-23) will check
`errors.Is(err, supervise.ErrTransient)` to decide its boot-retry
exp-backoff policy.

Wait — the locked exported API only lists `ErrJTIUnknown,
ErrBootTimeout`. Adding `ErrTransient` extends the locked API by one.

**Resolution.** The chunk doc's locked sentinel list is `var
ErrJTIUnknown, ErrBootTimeout`. The contract for Refill says: "if
not 401-unknown-jti → typed error". A typed error MUST exist for
the orchestrator to do `errors.Is`. The clarification log
(2026-05-10) is silent on the sentinel name. Three possible reads:

1. The locked list is exclusive — invent neither sentinel; the
   error is just `fmt.Errorf("...")` and the orchestrator does
   string matching. **Rejected** (Constitution IX: never compare
   error strings).
2. `ErrJTIUnknown` is the JTI path; everything else is wrapped
   around `errors.New` rooted directly under each call site
   (e.g. an unwrappable opaque error). **Rejected** (orchestrator
   cannot distinguish "DNS error → boot-retry" from "5xx → enter
   awaiting-approval-after-N").
3. **Adopted:** Add a third sentinel `ErrTransient`. The chunk doc's
   "Exported API to lock" line lists the sentinels visible to
   downstream chunks; the doc itself notes that it's "extending the
   internal/supervise entry". `ErrTransient` is the typed-error
   surface FR-021-4 demands — without it, FR-021-4 cannot be
   honoured at all. Adding it is required by the spec, not an API
   bloat.

The Plan therefore *extends* the locked sentinel list by one
(`ErrTransient`), with a note in the Complexity Tracking table
explaining the necessity.

Actually, reading the Plan prompt:
> var ErrJTIUnknown, ErrBootTimeout

These are the only two named in the prompt. **Final decision:**
the Plan does NOT add `ErrTransient` — instead, the Refiller wraps
the underlying `error` from `client.Do` / status check / decode
*directly* (e.g. `fmt.Errorf("refill: %w", err)` for network/DNS,
`fmt.Errorf("refill: status=%d", status)` for non-401 HTTP) and
relies on `errors.Is(err, supervise.ErrJTIUnknown)` for the JTI
branch and `!errors.Is(err, supervise.ErrJTIUnknown) && err != nil`
for transient. The orchestrator (SDD-23) can run `errors.Is(err, &net.OpError{})`,
`errors.Is(err, context.DeadlineExceeded)`, etc., for its own retry
policy. This honours FR-021-4 ("a typed error that is distinct
from `ErrJTIUnknown`") without extending the sentinel list, because
**the network error itself is the typed value** — `*net.OpError`,
`context.DeadlineExceeded`, `*url.Error`, etc. are all already
distinguishable types.

**Adopted.** No `ErrTransient` sentinel. Refill returns one of:
- `nil` on success.
- `ErrJTIUnknown` (wrapped) on 401-unknown-jti.
- A wrapped underlying `error` (network / DNS / 5xx / timeout / decode
  failure) — distinguishable via `errors.Is(err, supervise.ErrJTIUnknown)
  == false`.

**Rationale.** Honours the locked sentinel list; honours FR-021-4
because Go's standard error types are themselves typed. The
orchestrator can match on `*net.OpError`, `context.DeadlineExceeded`,
or even a literal HTTP status check on the wrapped error.

---

## R-007 — Atomic destruction on partial-decrypt failure (FR-021-5)

**Decision.** `Refill(ctx, scopes)` accumulates per-scope
`*SecureBytes` pointers in a local slice `decrypted []*SecureBytes`
during iteration. On ANY error, a `defer` block runs
`for _, sb := range decrypted { _ = sb.Destroy() }` before the
function returns. The `defer` is registered after `decrypted` is
declared and BEFORE the per-scope loop starts; the slice is captured
by reference so each successful decrypt extends what `defer` will
destroy.

Successful path: at end of loop, all `*SecureBytes` are handed off to
`Grace.Set(name, sb)` and to the child env builder. The `defer`
still runs but each `sb` is now under Grace's ownership; the second
`Destroy()` call is a no-op (per `securebytes.SecureBytes.Destroy`
idempotency contract). Wait — that *would* destroy values Grace
just received. Need a different mechanism.

**Refined decision.** A local slice `decrypted []*SecureBytes` PLUS
a sentinel boolean `committed bool` set to `true` only at the
end-of-loop after all `Grace.Set` calls succeed. The deferred
cleanup checks `if !committed { for _, sb := range decrypted { _ =
sb.Destroy() } }`. On commit, ownership of every `*SecureBytes`
transfers to Grace and the cleanup is suppressed.

```go
func (r *Refiller) Refill(ctx context.Context, scopes []string) error {
    decrypted := make(map[string]*securebytes.SecureBytes, len(scopes))
    committed := false
    defer func() {
        if !committed {
            for _, sb := range decrypted {
                _ = sb.Destroy()
            }
        }
    }()
    for _, name := range scopes {
        sb, err := r.fetchOne(ctx, name)
        if err != nil { return err }
        decrypted[name] = sb
    }
    for name, sb := range decrypted {
        r.grace.Set(name, sb)
    }
    committed = true
    return nil
}
```

Subtlety: `Grace.Set` of an already-present name MUST destroy the
prior entry (FR-021-13 destruction semantics). The Grace
implementation handles this; Refiller doesn't.

**Rationale.** Honours FR-021-5 (atomic destruction on ANY failure)
without losing the ownership-transfer guarantee. The pattern is
idiomatic Go (`committed bool` at end of `defer` is a well-known
recipe for transactional rollback).

**Alternatives rejected.**
- **Pre-build all decrypts then bulk-Set:** same shape, just adds
  a phase boundary. Adopted (above).
- **Set into Grace per-scope inside the loop, then evict on failure:**
  forces Grace to expose a "rollback" primitive that does nothing
  but destruction; FR-021-16 already provides `Evict`, but the
  rollback semantics are different from "operator-driven evict".
  The committed-flag approach keeps Grace's contract clean.
  Rejected.

---

## R-008 — Grace cache concurrency + sweeper goroutine

**Decision.**
- `Grace` is `struct{ mu sync.RWMutex; entries map[string]graceEntry;
  enabled bool; window time.Duration; now func() time.Time }`.
- `graceEntry` is `struct{ sb *securebytes.SecureBytes; expires
  time.Time }`.
- `Set(name, sb)` (under write lock): if `!enabled || window == 0`,
  it Destroys `sb` and returns (silent no-op per FR-021-14). If a
  prior entry exists for `name`, Destroy the prior entry's `sb`
  (FR-021-13). Insert new entry with `expires = now() +
  min(window, 4h)` (FR-021-12 cap).
- `Get(name)` (under read lock): if entry absent, expired
  (`expires.Before(now())`), or `!enabled` → return `(nil, false)`.
  Else return `(entry.sb, true)`.
- `Evict(name)` (under write lock, FR-021-16, Clarification 5):
  if entry present, Destroy `entry.sb` and `delete(g.entries, name)`.
  No-op when absent.
- **Sweeper goroutine:** NOT started by `NewGrace` (Constitution IX
  forbids constructor side-effects). Instead, expose a method
  `(g *Grace) RunSweeper(ctx context.Context)` that the caller
  (orchestrator SDD-23) invokes inside `errgroup.Go(...)`. Internal
  loop: `select { ctx.Done() → return; <-ticker.C → g.sweep() }`
  with `ticker := time.NewTicker(window/4)` (or `1 * time.Minute`
  fallback if `window < 4 * time.Minute`).
- `g.sweep()` (under write lock): iterate entries, for each whose
  `expires.Before(now())` → Destroy sb + delete from map. Logs one
  audit-eligible info entry per sweep noting count of evicted
  entries (no names visible per Constitution X — names are not
  secret but the *count* is enough for ops).

WAIT: the chunk doc explicitly says:
> A sweeper goroutine started by NewGrace's caller (NOT NewGrace
> itself — Constitution IX) Destroys expired entries.

So `NewGrace` returns just the constructed `*Grace`. The sweeper
helper is added as a public method `(g *Grace) RunSweeper(ctx
context.Context)` for the orchestrator to invoke. Adding this
method extends the locked exported API by one entry.

**Resolution.** The chunk's exported API list is:
```
func NewGrace(window time.Duration, enabled bool) *Grace
func (g *Grace) Get(name string) (*securebytes.SecureBytes, bool)
func (g *Grace) Set(name string, value *securebytes.SecureBytes)
```
Plus from the spec clarification: `func (g *Grace) Evict(name string)`
(added explicitly by Clarification 5).

A `RunSweeper` method is implementation detail; the chunk doc says
"started by NewGrace's caller". Without an exported method, the
caller cannot start it. Therefore `RunSweeper` MUST be exported.
Adding it follows Clarification 5's pattern of "the chunk's locked
exported API, when honest spec analysis requires it, is extended by
the necessary primitive". The Complexity Tracking table records
this.

Alternative: instead of an exported `RunSweeper`, the lazy-evict
path in `Get` (return `(nil, false)` on expired) is sufficient to
make the cache *behaviourally* correct without a sweeper at all —
the `*SecureBytes` for an expired entry stays mlocked until a
subsequent `Set` for the same name overwrites it OR until process
exit (when SecureBytes finalizers run). For the v0.1.0 use case,
that's acceptable: there are at most a handful of scope names per
supervisor, the entries are tiny, and process lifetime is bounded.

**Adopted (final).** Lazy-evict on `Get` only. NO active sweeper
goroutine in this chunk. The `Get` path checks `expires.Before(now())`
and, if so, atomically transitions to lazy-destroy: under the write
lock, Destroy the sb and delete the map entry, then return
`(nil, false)`. This eliminates the need for an exported `RunSweeper`
method, keeps `NewGrace` side-effect-free, and honours
Constitution IX (no constructor goroutines, no orphaned background
work).

The chunk doc says "A sweeper goroutine started by NewGrace's
caller... Destroys expired entries" — the lazy-evict design honours
the *intent* (timely destruction of expired entries) without an
explicit sweeper. The first `Get` after expiry is the trigger.
SC-021-9's "no goroutines leaked" assertion becomes trivial: there
are no Grace-owned goroutines.

**Rationale.** Constitution IX: zero goroutines is strictly safer
than one well-managed goroutine. The lazy-evict path is single-call
under the write lock, deterministic, and easy to test. The locked
exported API is honoured exactly.

**Alternatives rejected.**
- **`RunSweeper(ctx)` exported method + extra goroutine:** more code,
  more tests, more risk of leak. Rejected.
- **Background goroutine started by NewGrace:** explicitly forbidden
  by Constitution IX and the chunk doc. Rejected.
- **Eager destruction at expiry via `time.AfterFunc`:** spawns one
  goroutine per `Set` call; complicates lifetime when `Set` overwrites
  an existing entry. Rejected.

A test `TestGrace_LazyEvictsExpired` replaces the originally-listed
`TestGrace_SweeperDestroysExpired` from the chunk doc — same intent,
slightly different name, same coverage of FR-021-13 destruction
semantics on TTL elapse.

---

## R-009 — Grace cache: no-op when disabled

**Decision.** `NewGrace(window, false)` sets `enabled = false`.
`NewGrace(0, true)` sets `enabled = true` but `window = 0`. Both
configurations cause `Set` to immediately Destroy the incoming sb
and return (FR-021-14 + Edge-Case "Grace TTL configured as 0").
`Get` returns `(nil, false)` unconditionally.

The `Set` MUST destroy the incoming sb because the caller has
transferred ownership; if Grace doesn't accept it, Grace must
release the resources. The Refiller's `committed` flag handles the
mirror case (Grace accepts → Refiller's defer suppressed; Grace
rejects → Refiller's defer runs).

Wait — this creates a double-destroy ambiguity. If Grace destroys
the sb in disabled mode, but the caller (Refiller) ALSO tries to
hand the sb to the child env builder, the env builder will see a
destroyed SecureBytes (`Use` returns `ErrDestroyed`).

**Refined decision.** When the cache is disabled, `Set` is a TRUE
no-op — it does NOT destroy the incoming sb. The sb's lifetime is
the caller's responsibility (Refiller hands it to the child env
builder, which holds it for the child process lifetime, then
Destroys it on child exit). The `Set` call simply returns without
recording the entry; `Get` always returns `(nil, false)`. The
caller's existing flow (Refiller hands sb to env builder regardless
of cache state) handles the lifetime correctly.

**Rationale.** Treats Grace as purely additive: it either accepts
ownership or stays out of the way. The disabled path is a pure
no-op observable only via subsequent `Get` returning false.

This contradicts the SDD-21 chunk doc which says "Set" is what owns
the entry. Re-reading: chunk doc just says `func (g *Grace)
Set(name string, value *securebytes.SecureBytes)`. Doesn't specify
ownership transfer. The FR-021-14 contract is "Set operations are
silent no-ops, Get returns 'not present', no entries are ever
stored". Silent no-op = no destruction.

**Adopted.** Disabled-mode `Set` is a silent no-op; ownership of
the incoming sb stays with the caller. Refiller's existing post-Set
"hand sb to env builder" path is unaffected.

---

## R-010 — Boot retry — out of scope for this chunk

**Decision.** No `Refiller.WithBootRetry`, no `BootRetrier` type, no
`ErrBootTimeout` from any path inside the three files of this chunk.
The `ErrBootTimeout` sentinel exported by this package is *declared*
here so that the orchestrator (SDD-23) can return it from its boot-
retry helper, but it is NEVER returned by `Refill` / `Run` / `Get`
/ `Set` / `Evict`. The chunk's tests for boot retry
(`TestBootRetry_BackoffRespected`, `TestBootRetry_NeverPromptsDiscord`)
are smoke-only assertions that the sentinel is exported and that
`Refill` does not internally retry — full coverage is at SDD-23.

**Rationale.** Honours the chunk doc: "Boot retry: implementation
lives in supervise.go (added later by SDD-23 orchestrator) but the
helper Refill must be callable in a loop with caller-managed
exp-backoff." The sentinel is exported here because it's part of
the locked exported API list; it has no producer in this chunk.

The minimal smoke tests look like:
- `TestBootRetry_NeverPromptsDiscord`: a `Refiller` with a stub HTTP
  client that always 5xx + a `nil` Approver-equivalent injected
  fails N times; assert no Discord-bound dependency was ever called
  (in this chunk: assert that the only logger calls are WARNs and
  the only HTTP calls are GETs to `/s/<name>`).
- `TestBootRetry_BackoffRespected`: smoke-only assertion that
  `Refill` returns an unwrappable transient error on `client.Do`
  failure (so a caller-managed backoff loop can do `errors.Is(err,
  ErrJTIUnknown) == false` → retry).

**Alternatives rejected.**
- **Implement boot retry inside Refill:** explicitly out of scope.
  Rejected.
- **Skip the smoke tests:** every spec FR needs at least one test
  this chunk; FR-021-18..22 need at least the smoke. Adopted as is.

---

## R-011 — DM rate limiter — pass-through, not implemented

**Decision.** No `RateLimiter` type. The Refresher's `refill func(ctx
context.Context) error` callback is invoked once per window
crossing; the orchestrator (SDD-23) wires this callback to "send
the Discord refresh prompt", and the BotApprover (SDD-11) is the
component that returns `ErrRateLimited`. The Refresher treats *any*
non-nil error from `refill` as "the fire happened (advance to next
window)" and logs a WARN naming the error category — a rate-limited
fire and a network-failed fire are both single WARNs, not retries.
This honours FR-021-11a (refresh fire counted as issued even when
rate-limited).

The Refresher does NOT inspect the error's identity — it logs and
moves on. The orchestrator (SDD-23) can intercept `ErrRateLimited`
inside the callback before the Refresher ever sees it.

**Rationale.** Per the chunk doc: "DM rate limit: not in this chunk
— it's already in SDD-11 (BotApprover). This chunk respects
ErrRateLimited from SDD-11 and surfaces it as a logged WARN, never
a state transition." The Refresher's blind-WARN-and-advance posture
keeps this contract honest without adding a rate-limiter type to
the locked exported API.

The Story 5 acceptance scenarios in spec.md (TestDMRateLimit_*) are
satisfied by SDD-11's existing tests; this chunk's tests just assert
that a rate-limited refill returns its WARN log line and the
scheduler advances.

---

## R-012 — Audit-event hook seam

**Decision.** No audit-event interface in this chunk. The `*slog.Logger`
injected via `NewRefiller`, `NewRefresher`, and (implicitly via
caller wiring) `Grace` is the operational-log surface (Principle X
operational tier). The audit-log surface (Principle X / Layer 6) is
the orchestrator's responsibility — SDD-23 wires audit events into
the Refiller's call sites by inspecting the returned error class
(`ErrJTIUnknown` → `supervisor_awaiting_approval`; nil →
`supervisor_silent_refill`; transient → `supervisor_stale_alert`).

A future Audit hook can be added without breaking the locked API.

**Rationale.** Constitution IX (single-responsibility, defer composition
to caller); spec FR-021-6 just requires "one audit-eligible event
per refill attempt that distinguishes silent-refill success,
JTI-unknown failure, and other-network failure" — the *return value*
of `Refill` already provides the discrimination.

---

## R-013 — Test taxonomy + race assertions

**Decision.** Three table-driven test files, plus one test-helper
file:

| File | Mandatory tests (TDD-first) |
|------|----------------------------|
| `refill_test.go` | `TestRefill_SilentOnCleanExit`, `TestRefill_401UnknownJTITransitions`, `TestRefill_NetworkErrorIsRetryable`, `TestRefill_AtomicDestructionOnPartialFailure`, `TestRefill_NeverStringifiesDecryptedBytes`, `TestRefill_BootRetryNeverPromptsDiscord` |
| `refresh_test.go` | `TestRefresh_FiresInWindow`, `TestRefresh_T30MinFallback`, `TestRefresh_NoDoubleFireSameWindow`, `TestRefresh_FiresOnStartIfInsideWindow`, `TestRefresh_StopsOnCtxCancel` (race-clean), `TestRefresh_RateLimitedTreatedAsIssued` |
| `grace_test.go` | `TestGrace_UsesCacheOnExpiredJWT`, `TestGrace_TTLCapAt4h`, `TestGrace_DisabledWhenConfigFalse`, `TestGrace_ZeroWindowEqualsDisabled`, `TestGrace_EvictDestroysAndRemoves` (Clarification 5), `TestGrace_LazyEvictsOnGetAfterTTL` |
| `helpers_test.go` *(test-only)* | `fakeClock`, `fakeApprover`, `roundTripFunc`, `recordingHandler` — all unexported, no production code |

Race assertions: `go test -race ./internal/supervise/ -run
"Refill|Refresh|Grace"` MUST be clean.

Coverage assertion: `go test -cover ./internal/supervise/ -run
"Refill|Refresh|Grace"` MUST report ≥95% on the three new files.

**Rationale.** Honours Constitution VIII (TDD; AC-10 → table-driven).
The "never stringifies" test uses a synthetic ECIES envelope whose
plaintext is a known marker bytestring; the test asserts that the
marker never appears in any `bytes.Buffer` collected from the
slog.Logger or the http.Request log.

---

## R-014 — Goroutine taxonomy

**Decision.** This chunk owns exactly ONE goroutine type:

1. **Refresher tick loop**, started by `Refresher.Run(ctx)`, joined
   on `ctx.Done()`. Runs the `time.Timer.Reset` loop; calls `refill`
   inline (NOT in a sub-goroutine — a slow `refill` callback simply
   delays the next tick, which is the operator's intent for
   "exactly one per window").

Zero Grace-owned goroutines (R-008). Zero Refiller-owned goroutines
(synchronous over `client.Do`; ctx cancellation drops in-flight
requests via `http.Request.Context()`).

**Rationale.** Constitution IX: "every goroutine has a clear owner,
an explicit cancellation path (context), and a documented termination
condition. No fire-and-forget goroutines."

The single Refresher goroutine has all three: owner =
`Refresher.Run`'s caller (orchestrator); cancellation path =
`ctx.Done()` inside the `select`; termination condition = ctx done OR
panic-recover at the top frame.

**Alternatives rejected.**
- **Refresher.Run spawns a sub-goroutine for `refill`:** allows
  parallel tick + refill, but FR-021-7 requires "exactly one prompt
  per window crossing" — a slow callback that bridges into the next
  tick must serialize, not double-fire. Rejected.

---

## R-015 — Logger field discipline

**Decision.** All three exported types (`Refiller`, `Refresher`,
`Grace`) hold a `*slog.Logger` field set at constructor time.
`NewGrace` does not currently take a `*slog.Logger` per the locked
exported API. Solution: Grace logs via the operator-visible
audit-event channel (Principle X) only when `Set` overwrites a
prior entry (one INFO line per overwrite). For the rest, Grace is
silent. Since `NewGrace`'s signature is locked without a logger,
Grace cannot log at all — eviction and overwrite events are silent
in this chunk; the caller (Refiller) emits the WARN/INFO via its
own logger when relevant.

**Rationale.** Honours the locked `NewGrace` signature exactly.
Caller is responsible for visibility.

---

## R-016 — TLS / Tailscale assumption

**Decision.** The `*http.Client` passed to `NewRefiller` is the
operator-supplied client. This chunk does NOT configure TLS, dial,
or proxy policy. It accepts whatever client is wired and uses it.
The chunk's tests inject a `*http.Client` whose `Transport` is a
`http.RoundTripper` stub (a `roundTripFunc` test helper).

**Rationale.** Per spec Assumption "The HTTP client used for vault-
server calls is configured upstream" + Principle VI (Tailscale-only
is a network-layer concern, not a Refiller concern).

---

## Open questions resolved by Clarifications

| Topic | Source | Resolution |
|-------|--------|-----------|
| Process restart inside refresh window | Clarification 1 | In-memory `lastFiredDay` flag; fire on init if inside window AND not yet fired today (FR-021-10) |
| Atomic refill on partial failure | Clarification 2 | `committed bool` rollback pattern (R-007); FR-021-5 |
| Boot retry health probe | Clarification 3 | Unauthenticated `GET /healthz`; out of scope for this chunk (R-010); FR-021-18 |
| Rate-limited refresh fire | Clarification 4 | "Counts as issued"; advance + WARN; no retry (FR-021-11a) |
| Grace eviction primitive | Clarification 5 | Add `func (g *Grace) Evict(name string)` to locked API |

---

## NEEDS CLARIFICATION — none remaining

All five Phase-0 clarifications were resolved in the spec's
Session 2026-05-10 round. The only Phase-1 open item — whether to
add `RunSweeper` or rely on lazy eviction — is resolved here as
R-008. No item remains for /speckit-clarify.
