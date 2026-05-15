# Phase 1 Data Model — SDD-28 (`internal/discord/alerts`)

**Feature:** 028-discord-alerts
**Date:** 2026-05-13
**Plan:** [plan.md](./plan.md)
**Research:** [research.md](./research.md) (R-001..R-016)

This file pins the locked Go shapes and 22 type-level invariants
(A-1..A-22). The contract files [contracts/api.go](./contracts/api.go)
+ [contracts/observable-behaviors.md](./contracts/observable-behaviors.md)
are the typed mirror + the black-box behavior list; this file is
the structural source of truth.

---

## 1. Locked exported types

### 1.1 `type AlertClass string` (1 type + 8 constants)

```go
// AlertClass enumerates the 8 operator-visible alert categories
// per docs/LIFECYCLE-SCENARIOS.md "Required alert classes".
// The set is CLOSED in v0.1.0 (spec FR-005).
type AlertClass string

const (
    AlertClassApprovalRequest                AlertClass = "approval-request"
    AlertClassDaemonRefreshRequest           AlertClass = "daemon-refresh-request"
    AlertClassValidatorStaleFailure          AlertClass = "validator-stale-failure"
    AlertClassChildExit78StaleFailure        AlertClass = "child-exit-78-stale-failure"
    AlertClassLogPatternStaleWarning         AlertClass = "log-pattern-stale-warning"
    AlertClassDiscordDisconnected            AlertClass = "discord-disconnected"
    AlertClassDiscordReconnected             AlertClass = "discord-reconnected"
    AlertClassVaultUnreachableAtBootTimeout  AlertClass = "vault-unreachable-at-boot-timeout"
)
```

| Constant | String value (verbatim per R-002 + LIFECYCLE-SCENARIOS) | Bound Tier |
|---|---|---|
| `AlertClassApprovalRequest` | `"approval-request"` | `TierCritical` |
| `AlertClassDaemonRefreshRequest` | `"daemon-refresh-request"` | `TierCritical` |
| `AlertClassValidatorStaleFailure` | `"validator-stale-failure"` | `TierWarning` |
| `AlertClassChildExit78StaleFailure` | `"child-exit-78-stale-failure"` | `TierCritical` |
| `AlertClassLogPatternStaleWarning` | `"log-pattern-stale-warning"` | `TierWarning` |
| `AlertClassDiscordDisconnected` | `"discord-disconnected"` | `TierWarning` |
| `AlertClassDiscordReconnected` | `"discord-reconnected"` | `TierInfo` |
| `AlertClassVaultUnreachableAtBootTimeout` | `"vault-unreachable-at-boot-timeout"` | `TierCritical` |

### 1.2 `type Tier int` (1 type + 3 constants)

```go
// Tier is the routing destination class. Closed set; v0.1.0
// expressly defines no fourth tier (spec FR-002).
type Tier int

const (
    TierCritical Tier = iota
    TierWarning
    TierInfo
)
```

Numeric values are stable across the v0.1.0 series: `TierCritical
== 0`, `TierWarning == 1`, `TierInfo == 2`. A new tier MAY be
added only as a chunk-level amendment (spec FR-002).

### 1.3 `type Alert struct` (caller payload)

```go
type Alert struct {
    Class          AlertClass
    Tier           Tier           // informational only — Route re-derives from Class (R-010)
    SupervisorName string
    MachineName    string
    Pattern        string
    Detail         string
    Time           time.Time
}
```

All fields are operator-safe metadata. `Detail` is documented
as "operator-supplied metadata, NEVER credential material"
(spec Assumption row 5).

### 1.4 `type Router struct` (opaque, all fields unexported)

```go
type Router struct {
    sender         Sender
    ownerID        string
    auditChannelID string
    supBucket      *ratebucket
    patBucket      *ratebucket
    logger         *slog.Logger
    // classToTier and classToTemplate are package-level immutable
    // maps; they live in the production package, not on the Router
    // (see §2.3).
}
```

Constructor: `NewRouter(sender Sender, ownerID, auditChannelID
string, perSupervisorBucket, perPatternBucket time.Duration,
logger *slog.Logger) (*Router, error)`.

### 1.5 `type Sender interface` (R-005 extension)

```go
// Sender is the alerts package's consumer-defined interface for
// the Discord transport. *discord.BotApprover satisfies it via an
// adapter written by downstream wiring (SDD-25 or a glue layer) —
// NOT by this chunk's own implementation. Tests substitute fakes.
//
// Implementations MUST be safe for concurrent use.
type Sender interface {
    SendOwnerDM(ctx context.Context, ownerID, message string) error
    PostChannel(ctx context.Context, channelID, message string) error
}
```

### 1.6 Sentinel errors (R-011) — 3 Route-time + 1 construction-time = 4 total

**Route-time sentinels** (callers inspect after `Route(...)`):

```go
var (
    // ErrAlertRateLimited is returned when either the per-supervisor
    // or per-pattern debounce bucket rejects the call. Inspect via
    // errors.Is. (Locked at chunk-doc SDD-28 row 36.)
    ErrAlertRateLimited = errors.New("hush/discord/alerts: rate limited")

    // ErrAlertTransport wraps the underlying Sender error when a
    // Critical or Warning tier delivery fails at the transport
    // layer. The router NEVER consumes a debounce slot on this
    // failure (FR-012a). Inspect via errors.Is / errors.As.
    // (Added by Clarification Q3 — spec FR-012b.)
    ErrAlertTransport = errors.New("hush/discord/alerts: transport failed")

    // ErrAlertUnknownClass is returned when Route receives an
    // AlertClass outside the documented set of 8. Defensive only
    // (spec FR-009); in-package callers MUST use the public
    // constants to avoid this path entirely.
    ErrAlertUnknownClass = errors.New("hush/discord/alerts: unknown class")
)
```

**Construction-time sentinel** (caller inspects after
`NewRouter(...)`):

```go
var (
    // ErrAlertConfig is the unified construction-time sentinel.
    // NewRouter returns fmt.Errorf("...: %w: <param-reason>",
    // ErrAlertConfig) for each of the five invariant branches
    // (nil sender / empty ownerID / non-positive perSupervisorBucket
    // / non-positive perPatternBucket / nil logger). The
    // <param-reason> string identifies the offending parameter
    // symbolically; no caller-supplied value is interpolated.
    ErrAlertConfig = errors.New("hush/discord/alerts: invalid configuration")
)
```

One unified construction sentinel (vs five separate sentinels)
keeps the exported sentinel count to four (matching plan.md and
quickstart.md). `errors.Is(err, ErrAlertConfig)` matches all five
branches; the wrapped reason distinguishes which parameter was
bad without exporting a separate type per branch.

---

## 2. Internal (unexported) types

### 2.1 `classTemplate`

```go
type classTemplate struct {
    labelPrefix string  // e.g., "[CRITICAL][approval-request]"
}

// render formats the Alert per R-007 omit-empty-lines + R-002/R-012
// allow-list. Only SupervisorName, MachineName, Pattern, Detail
// fields reach the rendered string.
func (t classTemplate) render(a Alert) string
```

### 2.2 `ratebucket` + `bucketState` (R-009)

```go
type ratebucket struct {
    mu      sync.Mutex
    window  time.Duration
    entries map[string]bucketState
    now     func() time.Time
}

type bucketState struct {
    delivered time.Time // monotonic; last successful Route timestamp
    pending   time.Time // monotonic; in-flight reservation; zero if none
}

func (b *ratebucket) acquire(key string) bool
func (b *ratebucket) commit(key string)
func (b *ratebucket) refund(key string)
```

### 2.3 Package-level immutable maps

```go
// classToTier locks the 8 documented bindings. Constructed at
// package declaration time; never mutated; no init().
var classToTier = map[AlertClass]Tier{
    AlertClassApprovalRequest:                TierCritical,
    AlertClassDaemonRefreshRequest:           TierCritical,
    AlertClassValidatorStaleFailure:          TierWarning,
    AlertClassChildExit78StaleFailure:        TierCritical,
    AlertClassLogPatternStaleWarning:         TierWarning,
    AlertClassDiscordDisconnected:            TierWarning,
    AlertClassDiscordReconnected:             TierInfo,
    AlertClassVaultUnreachableAtBootTimeout:  TierCritical,
}

// classToTemplate locks the per-class label prefix. Constructed
// at package declaration time; never mutated; no init().
var classToTemplate = map[AlertClass]classTemplate{
    AlertClassApprovalRequest:                {labelPrefix: "[CRITICAL][approval-request]"},
    AlertClassDaemonRefreshRequest:           {labelPrefix: "[CRITICAL][daemon-refresh]"},
    AlertClassValidatorStaleFailure:          {labelPrefix: "[WARNING][validator-stale]"},
    AlertClassChildExit78StaleFailure:        {labelPrefix: "[CRITICAL][child-exit-78]"},
    AlertClassLogPatternStaleWarning:         {labelPrefix: "[WARNING][log-pattern]"},
    AlertClassDiscordDisconnected:            {labelPrefix: "[WARNING][discord-disconnected]"},
    AlertClassDiscordReconnected:             {labelPrefix: "[INFO][discord-reconnected]"},
    AlertClassVaultUnreachableAtBootTimeout:  {labelPrefix: "[CRITICAL][vault-unreachable]"},
}
```

These two maps are declared at package level with literal values.
Constitution IX forbids `init()` and mutable package-level state;
both maps are immutable-after-declaration by convention. The
production code MUST NOT expose any write path; the defensive
test `TestAlerts_ClassToTierIsImmutable` (B-A-2) proves
state-independence across multiple Router instances.

### 2.4 Outcome + slog-attribute constants

```go
const (
    outcomeDelivered        = "delivered"
    outcomeTransportFailed  = "transport_failed"
    outcomeUnknownClass     = "unknown_class"

    attrClass      = "class"
    attrTier       = "tier"
    attrSupervisor = "supervisor"
    attrMachine    = "machine"
    attrPattern    = "pattern"
    attrOutcome    = "outcome"

    msgRouted = "alerts route outcome"
)
```

No `DefaultBucketWindow` constant exists: NewRouter returns
`ErrNonPositiveSupervisorBucket` / `ErrNonPositivePatternBucket`
for non-positive bucket durations rather than silently
substituting a default (R-011).

---

## 3. Field-by-field semantics

### 3.1 `Alert.Class` (AlertClass; required)

- MUST be one of the 8 documented constants for routing to
  succeed. Any other value triggers the defensive
  `ErrAlertUnknownClass` path (FR-009, B-A-16 below).
- Case-sensitive comparison.
- Used as the per-pattern bucket key when `Pattern == ""`
  (R-004 / FR-011a).

### 3.2 `Alert.Tier` (Tier; informational only per R-010)

- Field exists per chunk-doc lock.
- Callers MAY set it for downstream readability; the Router
  re-derives the authoritative tier from `Class` via
  `classToTier`.
- Test `TestAlerts_CallerSuppliedTierIgnored` (B-A-8) seeds a
  mismatched Tier and asserts the route follows the documented
  binding, not the caller-supplied tier.

### 3.3 `Alert.SupervisorName` (string; required)

- Always rendered in the template (R-007 floor: label prefix
  + supervisor are the only always-present fields).
- Used as the per-supervisor bucket key (FR-010).
- Empty string is permitted by the type system but operationally
  meaningless: callers MUST set this; no defensive rejection at
  the boundary.

### 3.4 `Alert.MachineName` (string; optional)

- Rendered only when non-empty (R-007 omit-empty-lines).
- NOT used as a bucket key.

### 3.5 `Alert.Pattern` (string; optional except for `AlertClassLogPatternStaleWarning`)

- Rendered only when non-empty.
- Used as the per-pattern bucket key when non-empty; falls back
  to `string(Class)` when empty (R-004 / FR-011a).

### 3.6 `Alert.Detail` (string; optional)

- Rendered only when non-empty (R-007).
- Documented contract (Spec Assumption row 5):
  "operator-supplied metadata only — pattern names, identifiers,
  timestamps, scope names. NEVER credential values."
- `Detail` MUST NOT be threaded into any slog attribute (R-008
  attribute allow-list excludes it).

### 3.7 `Alert.Time` (time.Time; documented but not rendered)

- NOT included in the rendered body (R-007/R-012 — Time is not
  in the template allow-list).
- Implicit in the slog record (slog auto-adds `time` attribute).
- Field exists in the chunk-doc-locked struct shape.

### 3.8 `Router.sender` (Sender; required)

- Wired at construction.
- Nil → `NewRouter` returns an error wrapping `ErrAlertConfig`
  with the reason "nil sender" (R-011 / B-A-23).

### 3.9 `Router.ownerID` (string; required)

- Wired at construction.
- Empty → `NewRouter` returns an error wrapping `ErrAlertConfig`
  with the reason "empty ownerID" (R-011 / B-A-23).
- Threaded into `Sender.SendOwnerDM(ctx, ownerID, message)` for
  every TierCritical route call.

### 3.10 `Router.auditChannelID` (string; optional)

- Empty string is permitted at construction; Warning-tier
  routes will then call `Sender.PostChannel(ctx, "", msg)` and
  the Sender's behavior is implementation-defined (the
  production adapter rejects the empty channelID — surfaces as
  `ErrAlertTransport`).

### 3.11 `Router.classToTier` (package-level immutable map; required)

- Constructed at package declaration via `var classToTier = ...`.
- Never mutated by production code.
- Safe for concurrent read (Go memory model: maps are race-safe
  for read-only access after the program-start happens-before).

### 3.12 `Router.classToTemplate` (package-level immutable map; required)

- Same shape as `classToTier`.

### 3.13 `Router.supBucket` / `Router.patBucket` (*ratebucket; required)

- Constructed at `NewRouter` time with the operator-supplied
  `time.Duration` window. Non-positive → `NewRouter` returns an
  error wrapping `ErrAlertConfig` with the reason
  "non-positive perSupervisorBucket" or "non-positive
  perPatternBucket" respectively.
- Each bucket maintains its own per-key state map; the two
  buckets do not share entries.

### 3.14 `Router.logger` (*slog.Logger; required)

- Wired at construction.
- Nil → `NewRouter` returns an error wrapping `ErrAlertConfig`
  with the reason "nil logger" (R-011 / B-A-23).
- Used per the R-008 level matrix.

---

## 4. Concurrency model

`Route()` operates as a synchronous, single-pass state machine
per invocation. **No goroutines spawned**; no persistent state
beyond the two buckets. Concurrent safety is achieved entirely
via the per-bucket mutex (R-009) + the immutability of the two
package-level maps.

```text
Route(ctx, alert)
├── (1) class lookup
│       ├── known     → continue
│       └── unknown   → emit WARN slog (outcome=unknown_class); return ErrAlertUnknownClass
├── (2) derive tier from class (R-010 — ignore alert.Tier)
├── (3) derive pattern key (R-004 — fallback to string(class))
├── (4) supBucket.acquire(supervisorName)
│       └── denied    → return ErrAlertRateLimited (NO slog record — FR-016/Q5)
├── (5) patBucket.acquire(patternKey)
│       └── denied    → supBucket.refund(supervisorName); return ErrAlertRateLimited (NO slog record)
├── (6) render template
├── (7) dispatch by tier
│       ├── TierCritical → sender.SendOwnerDM(ctx, ownerID, rendered)
│       ├── TierWarning  → sender.PostChannel(ctx, auditChannelID, rendered)
│       └── TierInfo     → no Sender call; slog.LogAttrs INFO with outcome=delivered
├── (8) outcome handling
│       ├── tier ∈ {Critical, Warning} && err == nil → commit both buckets; emit DEBUG slog (outcome=delivered); return nil
│       ├── tier == Info                              → commit both buckets; (slog INFO already emitted at step 7); return nil
│       └── tier ∈ {Critical, Warning} && err != nil → refund both buckets; emit WARN slog (outcome=transport_failed); return fmt.Errorf("alerts: route %s: %w", class, errors.Join(ErrAlertTransport, underlyingErr))
```

For TierInfo (step 7 = no Sender call), there is NO transport
failure path; the slog INFO record IS the delivery. Bucket
commits happen at step 8 unconditionally for Info.

Cancellation: `ctx.Done()` at step 7 surfaces from the
`Sender.SendOwnerDM` / `Sender.PostChannel` call as an error
(the Sender impl honors ctx); the router treats it as a
transport failure (refund + ErrAlertTransport-wrap). For
TierInfo, ctx is not consulted by the router itself (no I/O at
the alerts layer); ctx is passed to slog handlers but the slog
interface treats it advisory.

---

## 5. Template field allow-list

Templates render exactly four fields from the Alert:
`SupervisorName`, `MachineName`, `Pattern`, `Detail`. The
package-level immutable `classToTemplate` map carries the
per-class label prefix; the `render(alert)` helper composes the
final message via `strings.Builder` in fixed field order
(label-prefix → supervisor → machine → pattern → detail), each
non-empty field separated by a single space.

`Alert.Class`, `Alert.Tier`, and `Alert.Time` are NEVER reachable
from any template format string. The single source of `class`
strings in any rendered output is the per-class label prefix;
no other production code path emits a class string into
operator-visible text.

The per-class sentinel-byte tests
(`TestAlerts_NoSecretLeakInRendered_<Class>`, B-A-19) seed a
secret-marker byte string into the test's environment and
fields outside the allow-list, then assert the marker NEVER
appears in the rendered output for any class. The same test
seeds the marker into the underlying `Sender` error and asserts
the returned error chain ALSO excludes it (a transport-failure
sub-scenario).

---

## 6. Allowed and forbidden imports

**Allowed** (alerts package):
- `context`
- `errors`
- `fmt`
- `log/slog`
- `strings`
- `sync`
- `time`

**Forbidden** (asserted by `TestAlerts_ZeroNewDependencies`,
B-A-27 + `TestAlerts_NoSecureBytesImport`, B-A-26):
- `github.com/bwmarrin/discordgo` (transport stays behind the
  `Sender` seam — R-016)
- `github.com/mrz1836/hush/internal/discord` at the package
  level (alerts package does not import its parent — R-016;
  downstream wiring in SDD-25 adapts `*discord.BotApprover` to
  `alerts.Sender`, but that adapter lives in the wiring layer,
  not in the alerts package itself)
- `github.com/mrz1836/hush/internal/vault/securebytes` (no
  credential surface — Constitution X)
- Any third-party package not in the stdlib.

---

## 7. Invariants (A-1..A-22)

These are the structural and behavioral invariants the tests
assert. Each maps to one or more spec FR/SC and to a test in
[quickstart.md §4](./quickstart.md#4-mandatory-test-list).

| #     | Invariant                                                                                                       | Source              |
|-------|-----------------------------------------------------------------------------------------------------------------|---------------------|
| A-1   | `len(classToTier) == 8` AND every key is one of the 8 documented constants (no extra, no missing).             | SC-001, FR-001      |
| A-2   | Three `Tier` constants exist and are stable: `TierCritical=0, TierWarning=1, TierInfo=2`.                       | SC-002, FR-002      |
| A-3   | The router IGNORES `Alert.Tier` and re-derives from `Alert.Class` on every Route call.                          | FR-004, R-010       |
| A-4   | `classToTier` is constructed at package declaration time; no `init()`; no mutation post-construction (proved by an immutability test that drives two Router instances and asserts state independence). | Constitution IX, B-A-2 |
| A-5   | `classToTemplate` is constructed at package declaration time; no `init()`; no mutation; one entry per class.    | Constitution IX     |
| A-6   | Every per-class label prefix is unique within the 8-class set.                                                  | FR-017, SC-008      |
| A-7   | A rendered alert body NEVER contains the formatted `Alert.Time`.                                                | R-007, R-012        |
| A-8   | A rendered alert body NEVER contains any field outside the allow-list `{SupervisorName, MachineName, Pattern, Detail}`. | FR-019/020, SC-009 |
| A-9   | An Alert with empty `MachineName`/`Pattern`/`Detail` renders WITHOUT placeholder strings (`<missing>`, `?`, trailing `key:`). | FR-021, Q4   |
| A-10  | An Alert with empty `Pattern` uses `string(Class)` as the per-pattern bucket key.                               | FR-011a, R-004      |
| A-11  | Both buckets honor `time.Now()`'s monotonic component; wall-clock change does NOT shorten or lengthen the window. | FR-015, R-015    |
| A-12  | `Route()` calls `Sender.SendOwnerDM` EXACTLY ONCE for a Critical-tier success path; ZERO times for Warning and Info. | FR-006, FR-007, FR-008 |
| A-13  | `Route()` calls `Sender.PostChannel` EXACTLY ONCE for a Warning-tier success path; ZERO times for Critical and Info. | FR-007, FR-006, FR-008 |
| A-14  | For TierInfo, the router NEVER invokes `Sender.SendOwnerDM` OR `Sender.PostChannel`.                            | FR-008, SC-005      |
| A-15  | An unknown `Alert.Class` returns `ErrAlertUnknownClass` AND consumes zero bucket slots AND makes zero Sender calls. | FR-009, SC-010   |
| A-16  | A transport failure refunds BOTH buckets (per-supervisor + per-pattern) before returning a wrapped `ErrAlertTransport`. | FR-012a, SC-010a |
| A-17  | `errors.Is(err, ErrAlertRateLimited)` matches if and only if either bucket's acquire denied the call.           | FR-012, R-011       |
| A-18  | `errors.Is(err, ErrAlertTransport)` matches if and only if Sender returned a non-nil error AND tier ∈ {Critical, Warning}. | FR-012b      |
| A-19  | `errors.As(err, &someTransportErr)` recovers the underlying Sender error from a wrapped `ErrAlertTransport`.   | FR-012b, R-011       |
| A-20  | Exactly ONE slog record is emitted per Route call EXCEPT on `ErrAlertRateLimited` (NO record) per Q5/FR-024a.    | FR-024a, R-008       |
| A-21  | `NewRouter` returns an error wrapping `ErrAlertConfig` (with a parameter-naming reason) for each of the five invariant violations, in the documented validation order; `errors.Is(err, ErrAlertConfig)` matches every construction failure. | R-011, B-A-23 |
| A-22  | Concurrent Route calls from N goroutines complete race-clean under `-race`; cumulative success count matches the analytic bucket-capacity calculation (no double-decrement, no missed enforcement). | SC-012, FR-026, B-A-25 |

---

## 8. Test-driven invariants summary

The mandatory test list in
[quickstart.md §4](./quickstart.md#4-mandatory-test-list)
maps each invariant A-1..A-22 to at least one named test. The
list is the TDD-first surface per Constitution VIII; no
implementation code is written until every named test exists
(and fails).

---

## 9. Cross-references

| Resource | Path |
|----------|------|
| Plan | [plan.md](./plan.md) |
| Spec | [spec.md](./spec.md) |
| Research | [research.md](./research.md) |
| Contracts — typed mirror | [contracts/api.go](./contracts/api.go) |
| Contracts — behaviors | [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) |
| Quickstart | [quickstart.md](./quickstart.md) |
| Constitution | [.specify/memory/constitution.md](../../.specify/memory/constitution.md) |
| Lifecycle scenarios | [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §"Required alert classes" |
| Operations | [docs/OPERATIONS.md](../../docs/OPERATIONS.md) |
| Locked parent surface (SDD-11) | [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) §`internal/discord/` |
| SDD-11 ratelimit precedent | [internal/discord/ratelimit.go](../../internal/discord/ratelimit.go) |
| SDD-11 approver precedent | [internal/discord/approver.go](../../internal/discord/approver.go) |
