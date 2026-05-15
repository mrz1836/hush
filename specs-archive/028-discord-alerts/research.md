# Phase 0 Research — SDD-28 (`internal/discord/alerts`)

**Feature:** 028-discord-alerts
**Date:** 2026-05-13
**Spec:** [spec.md](./spec.md)
**Chunk doc:** [docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md)

This file resolves every WHY decision the plan rests on. Each entry
follows the same shape: **Decision · Rationale · Alternatives
considered · Source of truth.**

The 5 spec Clarifications (Q1..Q5) are load-bearing inputs; each is
called out where it lands.

---

## R-001 — Package location: `internal/discord/alerts` sub-package

**Decision:** Create a new sibling sub-package at
`internal/discord/alerts/` at import path
`github.com/mrz1836/hush/internal/discord/alerts`.

**Rationale:**
- The chunk doc names the path `internal/discord/alerts` verbatim
  (SDD-28 row 4, row 29).
- The parent `internal/discord` package is already locked at SDD-11
  (PACKAGE-MAP.md §`internal/discord/` "Exported API — locked at SDD-11",
  line 1244-1246: "SDD-28 will add the alert-class catalogue + tiered
  routing as a sibling sub-package (`internal/discord/alerts`). SDD-28
  MUST NOT alter any symbol above.").
- Sub-package keeps the alert-class catalogue + routing surface
  separable from the approval-DM surface; the two have distinct
  concerns (approval = blocking interactive DM with operator buttons;
  alerts = fire-and-forget tiered routing with debounce).
- Mirrors the SDD-18 (`internal/supervise/config`) and SDD-27
  (`internal/supervise/watchdog`) sub-package precedents.

**Alternatives considered:**
- (a) **Add the catalogue to `internal/discord` directly.** Rejected
  by PACKAGE-MAP.md:1245-1246 anti-contract verbatim and by
  separation-of-concerns: discord's approval-DM machinery and
  alerts' fire-and-forget routing have different dependency
  shapes (approver blocks on operator click; alerts return
  immediately).
- (b) **Place at `internal/alerts` (top-level under internal).**
  Rejected. The package consumes a Discord-specific transport
  (`Sender`, satisfied by `*discord.BotApprover`); placing under
  `discord/` keeps the import cone tight.

**Source of truth:** [docs/PACKAGE-MAP.md:1244-1246](../../docs/PACKAGE-MAP.md#L1244-L1246),
[docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md) row 4.

---

## R-002 — The 8 alert class names + their fixed tier binding

**Decision:** Lock the 8 `AlertClass` constants and their
class→tier binding verbatim per docs/LIFECYCLE-SCENARIOS.md
"Required alert classes" section + docs/OPERATIONS.md tier rules +
Constitution Principle X tier categorisation. The binding is a
package-level immutable `map[AlertClass]Tier` constructed once at
declaration time; no `init()` and no mutation post-construction.

| # | `AlertClass` constant (Go identifier)          | String value                          | `Tier`         | Driver                                                                                                              |
|---|------------------------------------------------|---------------------------------------|----------------|---------------------------------------------------------------------------------------------------------------------|
| 1 | `AlertClassApprovalRequest`                    | `"approval-request"`                  | `TierCritical` | Spec Story 1; SDD-11 FR-7 DMs the owner for every fresh approval. Operator decision required.                       |
| 2 | `AlertClassDaemonRefreshRequest`               | `"daemon-refresh-request"`            | `TierCritical` | LIFECYCLE-SCENARIOS.md §Scenario 8; the operator must approve refresh from phone.                                   |
| 3 | `AlertClassValidatorStaleFailure`              | `"validator-stale-failure"`           | `TierWarning`  | Constitution X "Warning: validator failure". LIFECYCLE-SCENARIOS.md §Scenario 6.                                    |
| 4 | `AlertClassChildExit78StaleFailure`            | `"child-exit-78-stale-failure"`       | `TierCritical` | LIFECYCLE-SCENARIOS.md §Scenario 5 — operator must rotate secret and reapprove. SPEC.md FR-7 "[STALE] Child Exit 78". |
| 5 | `AlertClassLogPatternStaleWarning`             | `"log-pattern-stale-warning"`         | `TierWarning`  | Constitution X "Warning: log-pattern watchdog auth-failure detection". LIFECYCLE-SCENARIOS.md §Scenario 15.         |
| 6 | `AlertClassDiscordDisconnected`                | `"discord-disconnected"`              | `TierWarning`  | docs/OPERATIONS.md "Discord outage behavior" — operational state change, no paging.                                 |
| 7 | `AlertClassDiscordReconnected`                 | `"discord-reconnected"`               | `TierInfo`     | Constitution X "Info: routine ... recovery"; documents reconnect for audit, not paging.                             |
| 8 | `AlertClassVaultUnreachableAtBootTimeout`      | `"vault-unreachable-at-boot-timeout"` | `TierCritical` | Constitution X "Critical: vault server unreachable". LIFECYCLE-SCENARIOS.md §Scenario 11 (boot-retry exhaustion).   |

**Rationale:**
- The 8 names are reproduced VERBATIM from
  `docs/LIFECYCLE-SCENARIOS.md` "Required alert classes" section
  (lines 301-314); the string-value column kebab-cases them for
  use as the `Pattern`-fallback key (FR-011a) and as the slog
  attribute value.
- Tier assignments derive from Constitution X "Discord alert tiers"
  + LIFECYCLE-SCENARIOS scenarios + docs/OPERATIONS.md routing
  rules; each row's Driver column cites the binding source.
- The binding is FIXED (FR-003, FR-004): no auto-promotion or
  demotion. The router re-derives tier from class on every Route
  call and ignores the caller-supplied `Alert.Tier` field
  (defensive — see [R-010](#r-010) and [data-model.md A-3](./data-model.md)).

**Alternatives considered:**
- (a) **Configurable class→tier mapping.** Rejected. Spec FR-003
  + Constitution V require fixed, code-asserted binding. Runtime
  configuration of which class pages the operator violates "loud
  failure" (operator could silently demote Critical to Info).
- (b) **Derive tier from class string prefix (parse-based).**
  Rejected. String-derived tier hides the binding; a single
  static map is auditable in one glance.
- (c) **Class string values as natural-language strings (`"approval
  request"` with space).** Rejected. Kebab-case strings are
  URL-safe, log-attribute-clean, and convention-consistent with
  hush's slog conventions (e.g. `err_class: "discord_unavailable"`
  in `internal/discord/bot.go`).

**Source of truth:** [docs/LIFECYCLE-SCENARIOS.md lines 301-314](../../docs/LIFECYCLE-SCENARIOS.md#L301-L314),
[.specify/memory/constitution.md §X](../../.specify/memory/constitution.md),
[docs/OPERATIONS.md](../../docs/OPERATIONS.md).

---

## R-003 — Minimum-interval debounce rate-limit semantics (Clarification Q1)

**Decision:** Both `perSupervisorBucket` and `perPatternBucket`
parameters are `time.Duration` values interpreted as
**minimum-interval debounce**: each key permits at most one
successful Route per duration. Implicit capacity is 1; no
separate token-count parameter exists. The internal data
structure is a per-key `lastDelivered time.Time` (monotonic),
plus a per-key `pending time.Time` to handle the acquire→commit
race (mirrors SDD-11 `internal/discord/ratelimit.go` `bucketState`
shape).

**Rationale:**
- Spec Clarification Q1 (2026-05-13) locks this semantics
  verbatim.
- The SDD-11 `rateBucket` already uses identical semantics
  (`window time.Duration`, `bucketState{delivered, pending}`) —
  we mirror the proven pattern so the audit surface stays
  uniform.
- Token-bucket-with-capacity-N was rejected by the clarification.

**Alternatives considered:**
- (a) **Sliding-window of N tokens.** Rejected by Clarification
  Q1 explicitly.
- (b) **Per-key linked-list of recent timestamps for averaging.**
  Rejected — minimum-interval debounce only needs the most recent
  successful timestamp.

**Source of truth:** [spec.md §Clarifications Q1](./spec.md#clarifications),
SDD-11 `internal/discord/ratelimit.go` (production pattern).

---

## R-004 — `Sender` consumer-side interface (replaces chunk-doc `discord.Approver` parameter)

**Decision:** Define a 2-method consumer-side interface in
`internal/discord/alerts`:

```go
type Sender interface {
    SendOwnerDM(ctx context.Context, ownerID, message string) error
    PostChannel(ctx context.Context, channelID, message string) error
}
```

`NewRouter` accepts `Sender` (not `discord.Approver`) AND an
explicit `ownerID string` parameter that the Router passes to
`SendOwnerDM` on every Critical-tier call. Downstream wiring
(SDD-25 or a future glue layer) writes the
`*discord.BotApprover` → `alerts.Sender` adapter. Tests
substitute fakes (R-015).

**Rationale:**
- The chunk-doc-locked SDD-11 `discord.Approver` interface exposes
  only `RequestApproval(ctx, ApprovalRequest) (Decision, error)`
  — it has no DM-send or channel-post primitive.
- The chunk doc Prompt 3 itself acknowledges the gap:
  "use a simple SendDM helper if Approver doesn't expose one —
  define one" (line 180-182). The cleanest "define one" path that
  honours Constitution IX "accept interfaces, return concrete
  types; define interfaces at the consumer" is a
  consumer-defined `Sender` here.
- Adding `SendDM`/`PostChannel` to the locked `discord.Approver`
  interface is forbidden by PACKAGE-MAP.md:1245-1246 anti-contract.
- An explicit `ownerID` parameter (vs baking it into the Sender
  impl) keeps the Sender a pure transport seam — the same Sender
  instance can be reused across Routers with different owners in
  hypothetical multi-tenant deployments.
- Two-method interface (not a single composed `Send`) preserves
  the per-tier routing distinction in the call site.

**Alternatives considered:**
- (a) **Take `discord.Approver` literally + extend the locked
  interface.** Rejected — alters a locked surface.
- (b) **Take `discord.Approver` + type-assert to `*BotApprover`.**
  Rejected — Go anti-pattern; conflicts with Constitution IX.
- (c) **Take `*discordgo.Session` directly.** Rejected —
  third-party type leak.
- (d) **Bake ownerID into Sender.** Rejected — couples Sender to
  a single owner.
- (e) **Two single-method interfaces (`DMSender`,
  `ChannelPoster`).** Rejected on cohesion.

**Source of truth:** [docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md) Prompt 3 lines 179-184,
[.specify/memory/constitution.md §IX](../../.specify/memory/constitution.md),
[docs/PACKAGE-MAP.md:1244-1246](../../docs/PACKAGE-MAP.md#L1244-L1246).

---

## R-005 — Class-name fallback for empty `Pattern` field (Clarification Q2)

**Decision:** When `Alert.Pattern == ""`, the router uses
`string(Alert.Class)` as the per-pattern bucket key. When
`Alert.Pattern != ""`, the operator-supplied pattern value is
used verbatim. The substitution happens inside `Route()` before
the per-pattern bucket lookup; the substituted key is also the
slog `pattern` attribute value so logs are unambiguous about
which key was bucketised.

**Rationale:**
- Spec Clarification Q2 (2026-05-13) locks this semantics
  verbatim.
- 7 of the 8 classes carry no natural pattern identifier; only
  `AlertClassLogPatternStaleWarning` is expected to carry an
  operator-supplied pattern (the regex source name). Without the
  fallback, a single empty-string key would conflate every
  pattern-less class into one bucket, so a Discord-disconnect
  alert would silently debounce a vault-unreachable alert (FR-014
  isolation violation).
- Using the class name as the fallback key gives each pattern-
  less class its own per-pattern bucket while preserving the
  "two-bucket-per-Route" contract (FR-010 + FR-011) unchanged.

**Alternatives considered:**
- (a) **Treat empty `Pattern` as bucket-bypass.** Rejected.
  FR-013 requires every class to be rate-limited — no class
  bypass.
- (b) **Use `SupervisorName + ":" + string(Class)` as fallback.**
  Rejected — destroys orthogonality with the supervisor bucket.
- (c) **Reject empty-Pattern alerts at the router boundary.**
  Rejected — 7 of 8 classes legitimately carry empty Pattern.

**Source of truth:** [spec.md §Clarifications Q2](./spec.md#clarifications).

---

## R-006 — Single-shot send + commit-on-success debounce (Clarification Q3)

**Decision:**
1. `Route()` performs **exactly one** `Sender.SendOwnerDM` or
   `Sender.PostChannel` call per invocation — zero internal
   retries. For TierInfo, no `Sender` call occurs at all.
2. Per-supervisor and per-pattern debounce timestamps are
   recorded **only after** a successful transport call (Critical/
   Warning) or a successful Info-tier log write (TierInfo). A
   transport failure leaves both buckets untouched.
3. Transport failures wrap the underlying error: the router
   returns
   `fmt.Errorf("alerts: route %s: %w", class, errors.Join(ErrAlertTransport, underlying))`
   so callers distinguish via
   `errors.Is(err, ErrAlertRateLimited)` vs
   `errors.Is(err, ErrAlertTransport)`, and recover the underlying
   transport error via `errors.As`.

**Rationale:**
- Spec Clarification Q3 (2026-05-13) locks this triad verbatim.
- Single-shot prevents the router from converting a transport
  flap into a flood.
- Commit-on-success is essential: a transport failure that
  consumed the debounce slot would block legitimate retries
  through the caller's lifecycle.
- `ErrAlertTransport` as a separate sentinel (vs reusing
  `ErrAlertRateLimited`) is required because the caller's
  retry/back-off decision differs: rate-limit means "wait at
  least until window expires"; transport means "connection is
  broken — supervisor lifecycle owns back-off".

**Alternatives considered:**
- (a) **One internal retry before surfacing failure.** Rejected
  by Clarification Q3 "single-shot send (zero internal retries)".
- (b) **Debounce on attempt-not-success.** Rejected by
  Clarification Q3 "commit-on-success".
- (c) **Reuse `ErrAlertRateLimited` for transport failures.**
  Rejected — `errors.Is` clarity is the explicit clarification ask.

**Source of truth:** [spec.md §Clarifications Q3](./spec.md#clarifications).

---

## R-007 — Omit-empty-lines template rendering (Clarification Q4)

**Decision:** Every per-class template renders only the
operator-safe fields that are non-empty. The rendered floor is
always the class's static label prefix + `SupervisorName`. The
`MachineName`, `Pattern`, and `Detail` fields each render only
when their value is non-empty. The render uses a
`strings.Builder` constructed in a fixed field order:

```
<label-prefix> supervisor=<SupervisorName>[ machine=<MachineName>][ pattern=<Pattern>][ detail=<Detail>]
```

No `text/template` package; no placeholder text (`<missing>`,
`?`, trailing `key:`); no trailing whitespace.

**Rationale:**
- Spec Clarification Q4 (2026-05-13) locks this verbatim.
- Static `strings.Builder` rendering is auditable at a glance:
  the FR-022 sentinel-byte test can read every template's source
  to prove it formats ONLY the allow-listed fields. `text/template`
  introduces a parse step that obscures which fields each
  template can touch.
- The fixed field order is documented in data-model.md so the
  render-snapshot tests (SC-008) lock the exact byte sequence.

**Alternatives considered:**
- (a) **`text/template` with allow-list filter.** Rejected for
  auditability.
- (b) **Render placeholder text for missing fields.** Rejected
  by Clarification Q4 verbatim.
- (c) **JSON-encode the Alert struct's allow-listed fields.**
  Rejected — operator UX is "scan the prefix" (Spec Story 5).

**Source of truth:** [spec.md §Clarifications Q4](./spec.md#clarifications).

---

## R-008 — Slog level matrix + attribute allow-list (Clarification Q5)

**Decision:** The router emits exactly ONE structured slog
record per Route call, with the level determined by outcome and
tier:

| Outcome                                       | Level   | Attribute set                                                |
|-----------------------------------------------|---------|--------------------------------------------------------------|
| Success, `TierCritical`                       | DEBUG   | `class, tier, supervisor, machine, pattern, outcome=delivered` |
| Success, `TierWarning`                        | DEBUG   | `class, tier, supervisor, machine, pattern, outcome=delivered` |
| Success, `TierInfo`                           | INFO    | `class, tier, supervisor, machine, pattern, outcome=delivered` |
| Failure, `ErrAlertTransport` (any tier)       | WARN    | `class, tier, supervisor, machine, pattern, outcome=transport_failed` |
| Failure, unknown class (defensive)            | WARN    | `class, supervisor, machine, pattern, outcome=unknown_class` (no `tier` — class did not resolve) |
| Failure, `ErrAlertRateLimited` (any tier)     | NO RECORD | (caller logs the suppression per FR-016)                  |

Attribute allow-list per FR-024: `{class, tier, supervisor,
machine, pattern, outcome}`. NEVER `detail`, `message`,
`rendered`, `error`, or any field whose value derives from
credential material.

The message string is the literal `"alerts route outcome"` — a
static category, never interpolated.

**Rationale:**
- Spec Clarification Q5 (2026-05-13) locks the level matrix
  verbatim. FR-024a locks "exactly one slog record per Route
  call" + the allow-list.
- DEBUG-on-success for Critical/Warning means the operational
  log is quiet under steady state.
- WARN-on-failure surfaces transport degradation to operator
  log feeds even when the Discord destination cannot itself
  report it.
- No-record-on-rate-limit prevents the operational log from
  becoming a high-frequency mirror of a hot-loop flap.
- Attribute allow-list closes the secret-leak vector: an attacker
  who induced credential bytes into `Alert.Detail` could not
  exfiltrate them via slog because `detail` is excluded from
  every log record.

**Alternatives considered:**
- (a) **WARN on every routing decision.** Rejected by
  Clarification Q5 — too noisy.
- (b) **Include the rendered message body as a slog attribute.**
  Rejected by FR-024.
- (c) **No log record at all on TierInfo.** Rejected — breaks
  the "exactly one record per Route call" symmetry.

**Source of truth:** [spec.md §Clarifications Q5](./spec.md#clarifications),
[spec.md FR-024 + FR-024a](./spec.md#functional-requirements).

---

## R-009 — Acquire/commit/refund rate-bucket internals (FR-012a + concurrency)

**Decision:** The two buckets (`supervisorBucket`,
`patternBucket`) are independent `*ratebucket` instances, each
with the shape:

```go
type ratebucket struct {
    mu      sync.Mutex
    window  time.Duration
    entries map[string]bucketState // keyed by supervisor-name or pattern-name
    now     func() time.Time       // monotonic; injectable for tests
}

type bucketState struct {
    delivered time.Time // last successful Route timestamp (monotonic)
    pending   time.Time // in-flight reservation timestamp; zero if no reservation held
}
```

Three methods:

1. `(b *ratebucket) acquire(key string) bool` — atomically
   checks: if `pending != 0` OR `now - delivered < window`, return
   false; else set `pending = now`, return true. Holds `mu` for
   the full check-and-set.
2. `(b *ratebucket) commit(key string)` — atomically: set
   `delivered = pending`, clear `pending`. (No-op if no pending.)
3. `(b *ratebucket) refund(key string)` — atomically: clear
   `pending` without touching `delivered`. (No-op if no pending.)

Route call order:
1. supBucket.acquire(supKey) — if false, return `ErrAlertRateLimited`.
2. patBucket.acquire(patKey) — if false, supBucket.refund(supKey), return `ErrAlertRateLimited`.
3. render template.
4. dispatch by tier (DM / channel / log).
5. on success: supBucket.commit(supKey) AND patBucket.commit(patKey); emit DEBUG/INFO slog record per R-008.
6. on transport failure: supBucket.refund(supKey) AND patBucket.refund(patKey); return `ErrAlertTransport`-wrapped error; emit WARN slog record per R-008.

**Rationale:**
- FR-012a (commit-on-success) requires that a transport failure
  NOT consume either debounce slot; refund pattern delivers this
  atomically.
- FR-026 (concurrent-safe) requires a mutex around the bucket's
  state mutation; SDD-11 already proves this works under `-race`.
- Per-key isolation (FR-014): each map entry is independent.
- Two separate `*ratebucket` instances (vs one bucket with
  composite key) preserve the per-bucket-window configurability.
- The `pending` field is essential under concurrency: two
  simultaneous `Route` calls for the same supervisor MUST NOT
  both proceed past `acquire`.

**Monotonic time:** the `now func() time.Time` field is wired to
`time.Now` in production (returns monotonic clock on POSIX per
Go documentation since 1.9); tests inject a fake clock to
simulate window passage without `time.Sleep`. FR-015 (monotonic
time source) is satisfied because `time.Sub` on
monotonic-bearing `time.Time` values uses the monotonic
component, surviving wall-clock changes.

**Alternatives considered:**
- (a) **One mutex protecting both buckets.** Rejected — coarsens
  the lock unnecessarily.
- (b) **`sync.Map` keyed by composite (bucket-name, key).**
  Rejected — `sync.Map` doesn't compose well with read-modify-
  write semantics.
- (c) **Lock-free CAS via `atomic.Pointer[bucketState]`.**
  Rejected — premature optimization.

**Source of truth:** [spec.md FR-010..FR-015](./spec.md#rate-limit-enforcement),
SDD-11 `internal/discord/ratelimit.go` (proven pattern).

---

## R-010 — Caller-supplied `Alert.Tier` is informational; Router re-derives from class

**Decision:** Although the `Alert` struct exposes a `Tier`
field (chunk-doc signature line 32), the router NEVER trusts the
caller-supplied value. Inside `Route()`, the tier is re-derived
from `Alert.Class` via the package-level `classToTier` map. A
caller who sets `Alert.Class == AlertClassValidatorStaleFailure`
but `Alert.Tier == TierCritical` (mismatched) gets routed
according to the documented binding (`TierWarning`), not the
caller-supplied value.

**Rationale:**
- Spec FR-004 forbids re-tiering based on "content, frequency,
  recency, or any runtime signal". The caller-supplied Tier is a
  runtime signal that, if trusted, would let any caller silently
  promote a Warning to Critical (or demote vice versa).
- Spec Key Entities §Alert documents the rule verbatim.
- The `Alert.Tier` field exists in the struct because the
  chunk-doc API list pins it. Keeping it populated by callers
  (and tested for consistency at the boundary) gives downstream
  consumers (the audit log writer, future code) a cheap pre-
  computed tier without forcing them to re-import the alerts
  package's class-to-tier map.

**Alternatives considered:**
- (a) **Trust `Alert.Tier` verbatim.** Rejected — FR-004
  violation.
- (b) **Reject mismatched (Class, Tier) pairs.** Rejected. Strict
  rejection turns a legitimate zero-value `Tier{}` into an error.
- (c) **Drop `Tier` from the Alert struct.** Rejected — chunk-doc
  lock.

**Source of truth:** [spec.md FR-004](./spec.md#alert-classes-and-tier-binding),
[spec.md Key Entities §Alert](./spec.md#key-entities),
[docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md) row 32.

---

## R-011 — Sentinel error set + `NewRouter` returns `(*Router, error)`

**Decision:** The package exports three sentinel errors:

- `ErrAlertRateLimited` — locked in chunk-doc (SDD-28 row 36).
- `ErrAlertTransport` — added by Clarification Q3 (spec
  FR-012b + spec Key Entities).
- `ErrAlertUnknownClass` — defensive sentinel for FR-009 ("the
  router MUST handle an alert with an unknown class
  defensively: return a typed error").

All three follow Constitution IX sentinel discipline:
`var ErrXxx = errors.New("hush/discord/alerts: ...")` at package
level (Constitution IX exempts sentinel-class globals from the
no-globals rule per the SDD-21 / SDD-26 / SDD-27 precedent).

**Constructor signature:** `NewRouter` returns `(*Router, error)`
— extension from the chunk-doc's bare `*Router`. Validation
order:

1. `sender == nil` → return `nil, fmt.Errorf("alerts: %w", ErrNilSender)`
2. `ownerID == ""` → return `nil, fmt.Errorf("alerts: %w", ErrEmptyOwnerID)`
3. `perSupervisorBucket <= 0` → return `nil, fmt.Errorf("alerts: %w", ErrNonPositiveSupervisorBucket)`
4. `perPatternBucket <= 0` → return `nil, fmt.Errorf("alerts: %w", ErrNonPositivePatternBucket)`
5. `logger == nil` → return `nil, fmt.Errorf("alerts: %w", ErrNilLogger)`

The five construction-time sentinels are documented separately
from the three Route-time sentinels (the latter are what callers
inspect via `errors.Is` after a Route call; the former surface
at construction). They keep the constructor an honest error
returner per Constitution IX panic policy (operator-driven input
must not panic).

**Rationale:**
- Three Route-time sentinels reflect the smaller failure surface:
  rate-limit, transport, unknown-class. They are exactly what
  spec FR-009 + Clarification Q3 require.
- Returning `(*Router, error)` from `NewRouter` (extension over
  the chunk-doc's `*Router`) is the only Constitution-IX-compliant
  way to handle operator-input invariants: `time.Duration` values
  come from operator config (per-bucket TOML fields); `ownerID`
  comes from operator config; `logger` comes from the supervisor
  wiring. Panicking would violate Constitution IX panic policy
  (panics reserved for "unrecoverable invariant violations" in
  startup wiring). The same precedent applies in SDD-27's
  `NewWatchdog(...) (*Watchdog, error)` (SDD-27 plan §Complexity
  Tracking entry #2).
- Defensive `ErrAlertUnknownClass` exists because the chunk-doc
  API uses `type AlertClass string` (any string literal compiles);
  a typo at the call site would silently route nothing without a
  typed rejection.

**Alternatives considered:**
- (a) **NewRouter returns `*Router` with panic on invalid input.**
  Rejected — Constitution IX panic policy ("panics reserved for
  startup wiring", operator-input is NOT startup wiring).
- (b) **NewRouter returns `*Router` with silent defaults for
  every invalid input.** Rejected — silent defaults hide operator
  config errors; loud failure preferable.
- (c) **Single sentinel `ErrAlertFailed` with sub-codes via an
  unexported method.** Rejected — `errors.Is(err,
  ErrAlertRateLimited)` is the explicit caller contract from
  Clarification Q3.
- (d) **`AlertClass` as an unexported `int` enum.** Rejected —
  `AlertClass string` is chunk-doc-locked.

**Source of truth:** [docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md) row 36,
[spec.md §Clarifications Q3 + FR-009 + Key Entities](./spec.md),
[.specify/memory/constitution.md §IX sentinel + panic policy](../../.specify/memory/constitution.md),
[specs/027-watchdog/plan.md](../027-watchdog/plan.md) §Complexity Tracking entry #2.

---

## R-012 — Render label prefixes (FR-017 + FR-018 distinct visual labels)

**Decision:** Each class gets a unique 2-bracket label prefix in
the form `[<TIER>][<class-slug>]`:

| Class                                          | Label prefix                                   |
|------------------------------------------------|------------------------------------------------|
| `AlertClassApprovalRequest`                    | `[CRITICAL][approval-request]`                 |
| `AlertClassDaemonRefreshRequest`               | `[CRITICAL][daemon-refresh]`                   |
| `AlertClassValidatorStaleFailure`              | `[WARNING][validator-stale]`                   |
| `AlertClassChildExit78StaleFailure`            | `[CRITICAL][child-exit-78]`                    |
| `AlertClassLogPatternStaleWarning`             | `[WARNING][log-pattern]`                       |
| `AlertClassDiscordDisconnected`                | `[WARNING][discord-disconnected]`              |
| `AlertClassDiscordReconnected`                 | `[INFO][discord-reconnected]`                  |
| `AlertClassVaultUnreachableAtBootTimeout`      | `[CRITICAL][vault-unreachable]`                |

Property: every prefix is unique within the 8-class set (FR-017
+ SC-008 + SC-013 unique-prefix property). The TIER bracket
matches the class's documented tier from R-002.

**Rationale:**
- Two-bracket form mirrors SDD-11 FR-7 precedent ("[STALE]
  Validator Failure", "[STALE] Child Exit 78", "[DAEMON]").
- Tier prefix lets the operator triage by glancing at the first
  bracket.
- Class-slug abbreviation fits a phone notification line.

**Alternatives considered:**
- (a) **Single bracket.** Rejected — two distinct brackets parse
  faster.
- (b) **Emoji prefix.** Rejected — inconsistent platform rendering.
- (c) **Lowercase tier name.** Rejected — uppercase is louder
  and matches Constitution X naming.

**Source of truth:** [spec.md FR-017/018](./spec.md#templates-and-visual-labels),
[docs/SPEC.md FR-7 stale-alert label precedent](../../docs/SPEC.md#L106).

---

## R-013 — No-secret-byte rendering proof + template allow-list

**Decision:** The render path formats exactly four Alert fields:
`SupervisorName`, `MachineName`, `Pattern`, `Detail`. The render
function is a static `strings.Builder` chain (R-007); no
reflection, no `text/template`. Per-class tests
(`TestAlerts_NoSecretLeakInRendered_<Class>`, B-A-19, one per
class — 8 sub-tests) seed known sentinel byte sequences into
all four allow-listed fields AND a known "secret marker" into a
test-only environment variable, then render every class and
assert:
- the rendered output contains the four operator-safe sentinel
  bytes;
- the rendered output does NOT contain the secret-marker byte
  string;
- the rendered output does NOT contain the formatted `Time`
  value (Time is excluded from the allow-list — its purpose is
  the implicit slog `time` attribute, not body rendering);
- error chains returned from any Route call (success or
  failure) also exclude the secret marker.

**Rationale:**
- FR-022 + SC-009 lock the no-secret-byte invariant. The most
  defensible proof is a sentinel-byte assertion: the test seeds
  a unique random byte string into a place it should never be
  read from, then asserts the render output excludes it.
- `Time` exclusion is a deliberate design choice: keeping Time
  off the rendered body keeps the destination message terse for
  the operator's phone, and the timestamp is preserved in slog
  + (when SDD-13 routes audit rows) the audit log.
- Per-class assertion (×8) is preferable to a single combined
  test: it gives a precise failure surface when a new caller
  introduces a credential-shaped substring through a single
  class's path.

**Alternatives considered:**
- (a) **Render `Time` in the body too.** Rejected — adds noise
  to the operator notification.
- (b) **Use a struct-tag-driven allow-list.** Rejected —
  reflection at the boundary; harder to audit at a glance than
  explicit field references.
- (c) **Single combined sentinel-byte test across all classes.**
  Rejected — per-class isolation gives a sharper failure surface
  for future caller bugs.

**Source of truth:** [spec.md FR-022 + SC-009](./spec.md#zero-secret-leakage),
[data-model.md §5 Template field allow-list](./data-model.md#5-template-field-allow-list).

---

## R-014 — Orthogonality with `internal/supervise.AlertClass`

**Decision:** The pre-existing `internal/supervise.AlertClass`
enum (int-typed, SDD-24, 10 values for orchestrator-internal
event dispatch) and this chunk's `internal/discord/alerts.AlertClass`
enum (string-typed, 8 values for operator-visible notification
classes) are DIFFERENT types with DIFFERENT import paths and
DIFFERENT roles:

- `supervise.AlertClass` (int) is the **producer side** — what
  the supervisor emits when reporting a lifecycle event. It is
  internal-orchestrator-dispatch routing.
- `alerts.AlertClass` (string) is the **delivery side** — how
  the operator sees the event in Discord. It is operator-visible
  notification classification.

The mapping between them — `supervise.AlertClassValidatorFailure
→ alerts.AlertClassValidatorStaleFailure`, etc. — is SDD-25's
wiring responsibility, not this chunk's. No Go-level name
collision exists because the packages are distinct.

**Rationale:**
- The naming is admittedly close, but the types serve different
  layers (orchestrator-internal vs operator-visible) and have
  different cardinality (10 vs 8); merging would either lose
  operator-visible distinctions (collapse 10 to 8) or pollute
  the operator surface with orchestrator-internal events.
- Keeping them separate documents the layering: SDD-25 wiring
  translates from one to the other; SDD-28 owns only the
  operator-visible side.
- Tests can independently verify each enum's invariants without
  cross-package coupling.

**Alternatives considered:**
- (a) **Merge into a single enum across packages.** Rejected —
  cross-package enum coupling violates Constitution IX package
  layering; mapping is appropriately SDD-25's responsibility.
- (b) **Rename `supervise.AlertClass` to avoid the name
  similarity.** Rejected — SDD-24 surface is locked; renaming
  cascades.
- (c) **Rename this chunk's `AlertClass`.** Rejected — chunk-doc
  locks the name.

**Source of truth:** SDD-28 chunk doc row 31; SDD-24 locked
surface; [docs/PACKAGE-MAP.md §`internal/supervise/`](../../docs/PACKAGE-MAP.md).

---

## R-015 — Test-double `Sender` injection seam + test-double clock

**Decision:** Tests substitute a fake `Sender` value
(`type fakeSender struct { ... }`) declared in `alerts_test.go`.
Three pre-built fakes cover the three Sender behaviors needed:

| Fake                | Behavior                                                                                       | Used by                              |
|---------------------|-----------------------------------------------------------------------------------------------|--------------------------------------|
| `recordingSender`   | Records every (method, args) tuple in a slice; returns nil; safe for concurrent use.          | tier-routing + render-snapshot tests |
| `failingSender`     | Returns a fixed injected error from every method.                                              | transport-failure tests (SC-010a)    |
| `failOnInvokeSender`| Calls `t.Fatal` from any method invocation; safe for use with `Cleanup` to assert no calls.   | Info-tier no-Discord-call test (SC-005) |

For rate-limit tests, both `*ratebucket` instances accept an
injectable `now func() time.Time` field (unexported, set via a
non-exported constructor option). Production uses `time.Now`;
tests use a fake-clock helper that advances on demand. Real
`time.Sleep` is avoided in tests to keep the suite fast.

**Rationale:**
- Constitution IX: consumer-side interfaces are the seam.
  `Sender` is already exported (the production wiring's adapter
  satisfies it); tests inject directly via the same interface.
- `failOnInvokeSender` for the Info-tier test gives SC-005
  proof.
- FR-015 requires monotonic time; fake clock under test gives
  deterministic refill without `time.Sleep` flake.

**Alternatives considered:**
- (a) **Exported test helper in `export_test.go`.** Rejected —
  black-box tests through the exported `Sender` + observable
  Route return values + slog handler capture are sufficient.
- (b) **Sleep-based tests.** Rejected — flake under CI load.
- (c) **Function-shaped seam (`SenderFunc` helper).** Rejected —
  the 2-method interface is the natural cohesive unit.

**Source of truth:** Constitution IX consumer-interface rule;
SDD-21 fake-clock precedent.

---

## R-016 — Anti-coupling: alerts package does NOT import discordgo, internal/discord, or securebytes

**Decision:** The `internal/discord/alerts` package imports ONLY
the standard library (`context`, `errors`, `fmt`, `log/slog`,
`strings`, `sync`, `time`). It does NOT import:

- `github.com/bwmarrin/discordgo` (transport stays behind the
  `Sender` seam — R-005)
- `github.com/mrz1836/hush/internal/discord` (the parent package
  remains untouched per PACKAGE-MAP.md:1245-1246; the `Sender`
  interface is the seam, and the downstream glue (SDD-25 or a
  future adapter file) writes the `*discord.BotApprover` →
  `alerts.Sender` adapter)
- `github.com/mrz1836/hush/internal/vault/securebytes` (no
  credential surface — Constitution X)
- Any third-party package not in the stdlib.

A `TestAlerts_ZeroNewDependencies` test (B-A-27) asserts the
import set against a static allow-list; mirrors the SDD-27
precedent (`TestWatchdog_ZeroNewDependencies`).

**Rationale:**
- Constitution IX (define interfaces at the consumer) + XI
  (minimal dependencies) both push for zero third-party imports.
- Avoiding `internal/discord` import keeps the build cone tight
  (discord brings in discordgo + bot.go's session machinery).
  The boundary is one-directional: downstream wiring may import
  both `internal/discord` and `internal/discord/alerts` to wire
  them together, but alerts does not depend on discord.
- A future reverse import (discord importing alerts for the
  class constants) would not cycle because alerts wouldn't
  import discord back.

**Alternatives considered:**
- (a) **Import `internal/discord` for a compile-time guard
  `var _ Sender = (*discord.BotApprover)(nil)` directly in
  alerts.** Rejected — the guard belongs in the wiring layer
  (SDD-25 or glue), not in alerts. Keeps the dependency
  direction crisp.
- (b) **Import `discordgo` for direct channel-post.** Rejected
  by R-005 (consumer-side interface) + Constitution XI.

**Source of truth:** [.specify/memory/constitution.md §IX + §XI](../../.specify/memory/constitution.md).

---

## Summary table — R-001..R-016 ↔ FR / Clarification source

| R-#   | Topic                                                  | Source (FR / Clarification)                   |
|-------|--------------------------------------------------------|-----------------------------------------------|
| R-001 | Package location `internal/discord/alerts`            | PACKAGE-MAP.md:1244-1246; SDD-28 row 4        |
| R-002 | 8 class names + class→tier binding                     | LIFECYCLE-SCENARIOS lines 301-314; Constitution X |
| R-003 | Minimum-interval debounce semantics                    | Clarification Q1                              |
| R-004 | `Sender` consumer-side interface + `ownerID` param     | Constitution IX; SDD-28 Prompt 3 lines 179-184 |
| R-005 | Class-name fallback for empty `Pattern`                | Clarification Q2; FR-011a                     |
| R-006 | Single-shot send + commit-on-success                   | Clarification Q3; FR-012a + FR-012b           |
| R-007 | Omit-empty-lines rendering                             | Clarification Q4; FR-021                      |
| R-008 | Slog level matrix + attribute allow-list               | Clarification Q5; FR-024 + FR-024a            |
| R-009 | Acquire/commit/refund bucket internals                 | FR-010..FR-015; SDD-11 ratelimit.go precedent |
| R-010 | Caller-supplied `Alert.Tier` informational only        | FR-004; Spec Key Entities §Alert              |
| R-011 | 3 Route-time sentinels + 1 construction sentinel; `NewRouter` returns `(*Router, error)` | SDD-28 row 36; Clarification Q3; FR-009; Constitution IX |
| R-012 | `[TIER][class-slug]` label prefixes                    | FR-017 + FR-018 + SDD-11 [STALE] precedent    |
| R-013 | No-secret-byte rendering proof + template allow-list   | FR-022 + SC-009                                |
| R-014 | Orthogonality with `internal/supervise.AlertClass`     | SDD-24 locked surface; SDD-25 wiring          |
| R-015 | Test-double `Sender` (3 fakes) + fake clock            | Constitution IX; SDD-21 precedent             |
| R-016 | Zero third-party imports (alerts pkg)                  | Constitution IX + XI; SDD-27 precedent        |

All Q1..Q5 clarifications are mapped to a single R-row; no
unresolved clarification remains. Every chunk-doc-locked symbol
(SDD-28 rows 30-37) has a corresponding R-row that justifies its
shape or its plan-time extension.
