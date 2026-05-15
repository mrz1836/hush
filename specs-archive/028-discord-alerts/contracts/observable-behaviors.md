# Observable Behaviors — SDD-28 (`internal/discord/alerts`)

**Feature:** 028-discord-alerts
**Date:** 2026-05-13
**Plan:** [../plan.md](../plan.md)
**Data model:** [../data-model.md](../data-model.md)
**Research:** [../research.md](../research.md)

This file enumerates the 28 black-box behavior contracts the
package MUST satisfy (B-A-1..B-A-28). They are the test target
list for /speckit-tasks Phase 4 and the TDD-first surface per
Constitution VIII. Names use the prefix `B-A-` (B=behaviour,
A=alerts) and test names use the `TestAlerts_` prefix per
`.github/tech-conventions/testing-standards.md`.

Each row gives: the behavior, the spec FR/SC/Clarification that
drives it, the data-model invariant it proves, and the named test
that asserts it (the test name is the Phase-4 task title).

---

## Catalogue

| #  | Behavior | Test name (locked) | Driver | Invariant |
|----|----------|--------------------|--------|-----------|
| B-A-1  | Eight `AlertClass` constants exist, exactly matching the kebab-case strings in data-model.md §1.1. | `TestAlerts_AlertClassExportedSet` | SC-001, FR-001 | A-1 |
| B-A-2  | `classToTier` is constructed at declaration time, has cardinality 8, and is logically immutable (constructing a second Router does NOT see runtime state of a first). | `TestAlerts_ClassToTierIsImmutable` | FR-003, FR-005 | A-4 |
| B-A-3  | Three `Tier` constants exist with stable integer values 0/1/2. | `TestAlerts_TierExportedSet` | SC-002, FR-002 | A-2 |
| B-A-4  | The class→tier binding matches data-model.md §1.1 verbatim for every class (table-driven over the 8 classes). | `TestAlerts_TierBindingMatrix` | SC-003, FR-003 | A-1, A-2 |
| B-A-5  | `Route` for a TierCritical alert calls `Sender.SendOwnerDM(ctx, ownerID, rendered)` exactly once and makes ZERO `PostChannel` calls. | `TestAlerts_CriticalSendsDM` | SC-004, FR-006 | A-12 |
| B-A-6  | `Route` for a TierWarning alert calls `Sender.PostChannel(ctx, auditChannelID, rendered)` exactly once and makes ZERO `SendOwnerDM` calls. | `TestAlerts_WarningPostsToAuditChannel` | SC-004, FR-007 | A-13 |
| B-A-7  | `Route` for a TierInfo alert makes ZERO `Sender` calls. Uses a fail-on-invoke fake Sender. Emits exactly one slog INFO record. | `TestAlerts_InfoLogsOnly_NoDiscordCall` | SC-005, FR-008, FR-024a | A-14, A-20 |
| B-A-8  | The Router ignores caller-supplied `Alert.Tier` and re-derives from `Alert.Class`. A mismatched `(Class=ValidatorStaleFailure, Tier=TierCritical)` still routes as Warning. | `TestAlerts_CallerSuppliedTierIgnored` | FR-004 | A-3 |
| B-A-9  | Two TierCritical alerts for the same supervisor inside the perSupervisorBucket window: first succeeds, second returns `ErrAlertRateLimited` AND makes zero Sender calls AND emits zero slog records. After window elapses, next call succeeds. | `TestAlerts_RateLimitPerSupervisorBlocksExcess` | SC-006, FR-010, FR-016, FR-024a | A-17, A-20 |
| B-A-10 | Two TierCritical alerts with the same `Pattern` from DIFFERENT supervisors inside the perPatternBucket window: first succeeds, second returns `ErrAlertRateLimited`. After window elapses, next call succeeds. | `TestAlerts_RateLimitPerPatternBlocksExcess` | SC-006, FR-011 | A-17 |
| B-A-11 | Bucket isolation: exhausting supervisor-A does NOT affect supervisor-B; exhausting pattern-X does NOT affect pattern-Y. | `TestAlerts_RateLimitPerKeyIsolation` | SC-007, FR-014 | A-17 |
| B-A-12 | An alert with `Pattern == ""` uses `string(Class)` as the per-pattern bucket key; two distinct empty-Pattern classes do NOT share a bucket. | `TestAlerts_RateLimitEmptyPatternUsesClassFallback` | Q2, FR-011a | A-10 |
| B-A-13 | Rate-limit applies to every tier: a TierInfo alert returns `ErrAlertRateLimited` when its bucket is exhausted. | `TestAlerts_RateLimitAppliesToInfoTier` | FR-013 | A-17 |
| B-A-14 | A Critical-tier transport failure (Sender.SendOwnerDM returns injected error) → router returns an error matching `errors.Is(err, ErrAlertTransport)` AND `errors.As` recovers the injected underlying error AND BOTH buckets are NOT recorded as delivered (next attempt for same keys succeeds without waiting). | `TestAlerts_CriticalTransportFailureRefundsBuckets` | SC-010a, FR-012a, FR-012b | A-16, A-18, A-19 |
| B-A-15 | Same as B-A-14 but for Warning-tier (Sender.PostChannel failure). | `TestAlerts_WarningTransportFailureRefundsBuckets` | SC-010a, FR-012a, FR-012b | A-16, A-18, A-19 |
| B-A-16 | An unknown `Alert.Class` returns `ErrAlertUnknownClass`, makes zero Sender calls, and consumes zero bucket capacity. Emits one slog WARN record with `outcome=unknown_class`. | `TestAlerts_UnknownClassTypedError` | SC-010, FR-009 | A-15, A-20 |
| B-A-17 | Every class's rendered output begins with the documented label prefix (data-model.md §2.3). The prefix is unique across the 8-class set. | `TestAlerts_LabelPrefixUniqueAndStable` | SC-008, FR-017, FR-018 | A-6 |
| B-A-18 | An Alert with empty `MachineName`/`Pattern`/`Detail` renders WITHOUT the corresponding `machine=`/`pattern=`/`detail=` segments; no placeholder text appears. | `TestAlerts_TemplateOmitEmptyLines` | Q4, FR-021 | A-9 |
| B-A-19 | (Per class — 8 sub-tests via table) A rendered alert NEVER contains a seeded "secret marker" byte string. Seeded operator-safe values DO appear. The returned error chain on a transport failure ALSO excludes the secret marker. | `TestAlerts_NoSecretLeakInRendered_<Class>` | SC-009, FR-022, FR-023 | A-7, A-8 |
| B-A-20 | Slog attribute allow-list matches R-008 exactly: `{class, tier, supervisor, machine, pattern, outcome}`. NO `detail`, `message`, `rendered`, `error`. | `TestAlerts_LogAttrAllowList` | FR-024, R-008 | (attribute-set invariant) |
| B-A-21 | Slog level matrix matches R-008 exactly: DEBUG on Critical/Warning success, INFO on Info success, WARN on transport failure, WARN on unknown class, NO record on rate-limit. Exactly one record per Route call (zero when rate-limited). | `TestAlerts_SlogLevelMatrix` | Q5, FR-024a | A-20 |
| B-A-22 | Bucket refill uses monotonic time: a fake clock that advances `now` by `window + 1ns` after a successful Route → next call succeeds. Wall-clock manipulation does NOT alter the bucket's view. | `TestAlerts_RateLimitMonotonicClock` | FR-015, R-015 | A-11 |
| B-A-23 | `NewRouter` returns an error matching `errors.Is(err, ErrAlertConfig)` for each of the five invariant violations (table-driven: nil sender / empty ownerID / non-positive perSupervisorBucket / non-positive perPatternBucket / nil logger). The wrapped reason names the offending parameter without leaking caller-supplied values. | `TestAlerts_NewRouterConfigGuards` | R-011 | A-21 |
| B-A-24 | All 4 sentinel errors (`ErrAlertRateLimited`, `ErrAlertTransport`, `ErrAlertUnknownClass`, `ErrAlertConfig`) are pairwise disjoint — `errors.Is` for one never matches another's return values. | `TestAlerts_SentinelDisjointness` | FR-012, FR-012b, FR-009, R-011 | A-17, A-18 |
| B-A-25 | Concurrent Route calls from 8 goroutines × 100 calls each, mixed across all 8 classes and many supervisor keys, complete race-clean under `-race`; cumulative success count matches the analytic bucket-capacity calculation (no double-decrement, no missed enforcement). | `TestAlerts_ConcurrentRoute` | SC-012, FR-026 | A-22 |
| B-A-26 | The package does NOT import `internal/vault/securebytes` AND has zero `string(...)` conversion sites for any byte slice that could carry credential material. The single allowed `string(...)` site is `string(class)` on the typed `AlertClass` (a non-secret operator-visible name). | `TestAlerts_NoSecureBytesImport` / `TestAlerts_NoSecureBytesStringConversion` | Constitution X | (import-set invariant) |
| B-A-27 | The package imports only the allowed stdlib set (data-model.md §6). No third-party packages, no `internal/discord`, no `internal/vault/securebytes`. | `TestAlerts_ZeroNewDependencies` | Constitution IX/XI, R-016 | (import-set invariant) |
| B-A-28 | (Closure-of-the-set proof) Every routed alert observable in the test suite carries a `class` slog attribute equal to one of the 8 kebab-case strings; the production code emits no other class string anywhere. | `TestAlerts_NoStrayClassStringsEmitted` | SC-001, FR-005 | A-1 |

28 named tests; three test files:
- `alerts_test.go` — B-A-1..8, B-A-14..16, B-A-20..28 (router + tier-binding + slog + sentinel + concurrency + immutability + import/conversion guards)
- `templates_test.go` — B-A-17..19 (label-prefix uniqueness, omit-empty rendering, per-class sentinel-byte tests)
- `ratelimit_test.go` — B-A-9..13, B-A-22 (debounce mechanics, isolation, monotonic clock)

The list is locked at /speckit-plan time; /speckit-tasks Phase 4
generates a task per row in the same order (test-writing tasks
BEFORE the corresponding implementation task per Constitution VIII).

---

## Anti-contracts (assertions the package MUST never have)

These are the invariants that must STAY false at every commit;
they translate into assertions inside the named tests above.

- **AC-N-1** No production code path under `internal/discord/alerts/`
  imports `github.com/bwmarrin/discordgo` (B-A-27).
- **AC-N-2** No production code path imports
  `github.com/mrz1836/hush/internal/discord` at the package level
  (B-A-27; the consumer-side `Sender` interface is the seam).
- **AC-N-3** No production code path imports
  `github.com/mrz1836/hush/internal/vault/securebytes` (B-A-26;
  Constitution X — alerts has no credential surface).
- **AC-N-4** No production type or function names a parameter
  `secret`, `creds`, `credential`, or `token` (defensive lint —
  Constitution X scope).
- **AC-N-5** No production code path mutates `classToTier` or
  the per-class template strings after declaration (B-A-2 / A-4).
- **AC-N-6** No production code path retries a Sender call (FR-012b
  single-shot — B-A-14, B-A-15 assert via fake-Sender call count =
  1).
- **AC-N-7** No production code path uses `time.Sleep` (test
  determinism + FR-015 monotonic; the rate-limiter consults
  `now()` and Route does not sleep).
- **AC-N-8** No production code path uses `init()` (Constitution
  IX).
- **AC-N-9** No production code path defines a mutable package-
  level variable (Constitution IX; immutable maps + sentinels are
  exempt per the IX exemption documented in data-model.md §2.3).
- **AC-N-10** No production code path emits more than one slog
  record per Route call (B-A-21 cardinality assertion).
- **AC-N-11** No production code path spawns a goroutine
  (Constitution IX goroutine discipline — `Route` is synchronous
  end-to-end on the caller's goroutine).

---

## Locked exported surface (re-listed for review convenience)

Per chunk-doc SDD-28 rows 30-37 + the three plan-time extensions
documented in plan.md §Complexity Tracking + research.md R-005, R-011:

```text
type AlertClass string
const (
    AlertClassApprovalRequest, AlertClassDaemonRefreshRequest,
    AlertClassValidatorStaleFailure, AlertClassChildExit78StaleFailure,
    AlertClassLogPatternStaleWarning, AlertClassDiscordDisconnected,
    AlertClassDiscordReconnected, AlertClassVaultUnreachableAtBootTimeout
)

type Tier int
const ( TierCritical, TierWarning, TierInfo )

type Alert struct { Class AlertClass; Tier Tier; SupervisorName, MachineName string; Pattern, Detail string; Time time.Time }

type Sender interface {                                                  // R-005 extension
    SendOwnerDM(ctx context.Context, ownerID, message string) error
    PostChannel(ctx context.Context, channelID, message string) error
}

type Router struct { /* unexported */ }

func NewRouter(sender Sender, ownerID, auditChannelID string,            // R-005 + R-011 extension
               perSupervisorBucket, perPatternBucket time.Duration,
               logger *slog.Logger) (*Router, error)
func (r *Router) Route(ctx context.Context, alert Alert) error

// Route-time sentinels (3)
var ErrAlertRateLimited   error                                          // chunk-doc-locked
var ErrAlertTransport     error                                          // Clarification Q3 / FR-012b
var ErrAlertUnknownClass  error                                          // FR-009 / R-011

// Construction-time sentinel (1 — unified)
var ErrAlertConfig        error                                          // R-011 / B-A-23
```

Total exported surface: **2 enum types (AlertClass, Tier) + 11
enum constants (8 + 3) + 1 struct (Alert) + 1 opaque struct
(Router) + 1 interface (Sender) + 2 funcs (NewRouter, Route) + 4
sentinel errors (3 Route-time + 1 construction-time) = 22
exported symbols.**

`ErrAlertConfig` is the unified construction-time sentinel
(Constitution IX panic policy: operator-input invariants surface
as typed errors, not panics). Each of the five validation
branches wraps `ErrAlertConfig` via `fmt.Errorf("...: %w: <reason>",
ErrAlertConfig)` so the caller distinguishes "which parameter
was bad" via the wrapped string while still matching via
`errors.Is(err, ErrAlertConfig)`. This keeps the exported sentinel
count to four (matching plan.md and quickstart.md) while still
satisfying B-A-23's per-parameter-branch assertion.
