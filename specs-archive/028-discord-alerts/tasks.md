# Tasks: Discord Alert Surface (SDD-28)

**Input**: Design documents from `/specs/028-discord-alerts/`
**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/), [quickstart.md](./quickstart.md), [docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md), [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §"Required alert classes"

**Tests**: TDD-MANDATORY per Constitution VIII. Every test-writing task MUST appear BEFORE the implementation task that satisfies it. Tests MUST fail before implementation is started.

**Organization**: Tasks grouped by user story. US1, US2, US4 are P1 (MVP scope). US3, US5 are P2.

**Coverage target**: ≥90% on `internal/discord/alerts/` via `magex test:race`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel — different files, no dependencies on incomplete tasks
- **[Story]**: User story label (US1..US5) for story-phase tasks; cross-cutting/setup/polish tasks have no story label
- File paths absolute or rooted at repo

## Path Conventions

- Package path: `github.com/mrz1836/hush/internal/discord/alerts`
- Production files: `internal/discord/alerts/{alerts.go, templates.go, ratelimit.go}`
- Test files: `internal/discord/alerts/{alerts_test.go, templates_test.go, ratelimit_test.go}`
- Additive SDD-11 wiring: `internal/discord/bot_alerts.go` (new — additive methods on `*BotApprover`)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the empty sub-package skeleton + test helpers. No production logic yet.

- [X] T001 Create directory `internal/discord/alerts/` and write a package doc comment in `internal/discord/alerts/alerts.go` (header only — `// Package alerts ...` block describing the consumer-side alert-routing surface, 8 classes, 3 tiers, debounce semantics, Constitution V/IX/X scope).
- [X] T002 [P] Add the import path `github.com/mrz1836/hush/internal/discord/alerts` to no consumer yet — just verify the directory builds (`go build ./internal/discord/alerts/...` returns no error for the empty package).
- [X] T003 [P] Plan the test helper set in `internal/discord/alerts/alerts_test.go` header comment: enumerate the three Sender doubles (`recordingSender`, `failingSender`, `failOnInvokeSender`), a `recordingHandler` slog handler that captures `slog.Record` values, and a `fakeClock` `func() time.Time`. Helpers will land in T011 once the production types compile.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Lock the closed-set types and immutable maps so every downstream test can compile. **No Route logic yet** — Route stub returns `nil` so foundational tests of types/maps run.

**⚠️ CRITICAL**: No user story phase may begin until this phase is complete. The 8 `AlertClass` constants + `classToTier` map are the load-bearing surface that every downstream test references.

- [X] T004 In `internal/discord/alerts/alerts.go`, declare `type AlertClass string` and the 8 exported constants verbatim per [data-model.md §1.1](./data-model.md#11-type-alertclass-string-1-type--8-constants): `AlertClassApprovalRequest = "approval-request"`, `AlertClassDaemonRefreshRequest = "daemon-refresh-request"`, `AlertClassValidatorStaleFailure = "validator-stale-failure"`, `AlertClassChildExit78StaleFailure = "child-exit-78-stale-failure"`, `AlertClassLogPatternStaleWarning = "log-pattern-stale-warning"`, `AlertClassDiscordDisconnected = "discord-disconnected"`, `AlertClassDiscordReconnected = "discord-reconnected"`, `AlertClassVaultUnreachableAtBootTimeout = "vault-unreachable-at-boot-timeout"`. Source-of-truth: [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §"Required alert classes" lines 301-314.
- [X] T005 In `internal/discord/alerts/alerts.go`, declare `type Tier int` with the 3 iota constants `TierCritical=0`, `TierWarning=1`, `TierInfo=2` (data-model.md §1.2).
- [X] T006 [P] In `internal/discord/alerts/alerts.go`, declare `type Alert struct { Class AlertClass; Tier Tier; SupervisorName, MachineName, Pattern, Detail string; Time time.Time }` (data-model.md §1.3).
- [X] T007 [P] In `internal/discord/alerts/alerts.go`, declare the consumer-side `type Sender interface { SendOwnerDM(ctx context.Context, message string) error; PostChannel(ctx context.Context, channelID, message string) error }` (data-model.md §1.5; R-003).
- [X] T008 [P] In `internal/discord/alerts/alerts.go`, declare the three sentinel errors as package-level `var Err... = errors.New(...)` with the static category strings from [data-model.md §1.6](./data-model.md#16-sentinel-errors-r-011): `ErrAlertRateLimited`, `ErrAlertTransport`, `ErrUnknownAlertClass`.
- [X] T009 [P] In `internal/discord/alerts/alerts.go`, declare the immutable `classToTier map[AlertClass]Tier` package-level map by literal initialization (data-model.md §2.3). Bindings: ApprovalRequest→Critical, DaemonRefreshRequest→Critical, ValidatorStaleFailure→Warning, ChildExit78StaleFailure→Critical, LogPatternStaleWarning→Warning, DiscordDisconnected→Warning, DiscordReconnected→Info, VaultUnreachableAtBootTimeout→Critical. No `init()`.
- [X] T010 [P] In `internal/discord/alerts/templates.go`, declare the immutable `classToTemplate map[AlertClass]classTemplate` package-level map by literal initialization with the 8 label prefixes per [data-model.md §2.3](./data-model.md#23-package-level-immutable-maps) (`[CRITICAL][approval-request]`, `[CRITICAL][daemon-refresh]`, `[WARNING][validator-stale]`, `[CRITICAL][child-exit-78]`, `[WARNING][log-pattern]`, `[WARNING][discord-disconnected]`, `[INFO][discord-reconnected]`, `[CRITICAL][vault-unreachable]`). Add an UNEXPORTED `type classTemplate struct { labelPrefix string }` with a stub `render(a Alert) string` method that returns just the prefix (real omit-empty body will land in T031).
- [X] T011 [P] In `internal/discord/alerts/alerts.go`, declare `type Router struct { sender Sender; auditChannelID string; supBucket *ratebucket; patBucket *ratebucket; logger *slog.Logger }` plus `const DefaultBucketWindow = 1 * time.Minute`. Add stub `NewRouter(sender Sender, auditChannelID string, perSupervisorBucket, perPatternBucket time.Duration, logger *slog.Logger) *Router` that panics on nil sender / nil logger, applies the DefaultBucketWindow fallback, but constructs `*ratebucket` stubs (real impl in T025). Add stub `func (r *Router) Route(ctx context.Context, alert Alert) error` returning `nil`.
- [X] T012 [P] In `internal/discord/alerts/ratelimit.go`, declare unexported `type ratebucket struct { mu sync.Mutex; window time.Duration; entries map[string]bucketState; now func() time.Time }` + `type bucketState struct { delivered, pending time.Time }` per [data-model.md §2.2](./data-model.md#22-ratebucket--bucketstate-r-009). Stub `acquire/commit/refund` methods returning trivial values (real impl in T025).
- [X] T013 [US-FOUNDATIONAL] Write `TestAlertClass_ExportedSet` in `internal/discord/alerts/alerts_test.go` (B-A-1): assert the 8 constants exist with the exact kebab-case string values from data-model.md §1.1. **Must fail until T004 lands → must pass after T004.**
- [X] T014 [P] [US-FOUNDATIONAL] Write `TestTier_ExportedSet` in `internal/discord/alerts/alerts_test.go` (B-A-2): assert `TierCritical == 0`, `TierWarning == 1`, `TierInfo == 2`. **Must pass after T005.**
- [X] T015 [US-FOUNDATIONAL] Write `TestNewRouter_NilGuards` in `internal/discord/alerts/alerts_test.go` (B-A-25): assert `NewRouter(nil, "ch", 1*time.Second, 1*time.Second, slog.Default())` panics; assert nil-logger panics; assert zero/negative bucket windows fall back to `DefaultBucketWindow` (verify via the constructed Router's bucket window — use a small same-package accessor in `export_test.go` or assert behavior indirectly via Route). **Must pass after T011.**
- [X] T016 [P] [US-FOUNDATIONAL] Write the 8 per-class `TestAlert_<ClassName>_TierBinding` tests in `internal/discord/alerts/alerts_test.go` (chunk-doc Prompt-4 mandated names) — one per class: `TestAlert_ApprovalRequest_TierBinding`, `TestAlert_DaemonRefreshRequest_TierBinding`, `TestAlert_ValidatorStaleFailure_TierBinding`, `TestAlert_ChildExit78StaleFailure_TierBinding`, `TestAlert_LogPatternStaleWarning_TierBinding`, `TestAlert_DiscordDisconnected_TierBinding`, `TestAlert_DiscordReconnected_TierBinding`, `TestAlert_VaultUnreachableAtBootTimeout_TierBinding`. Each asserts `classToTier[AlertClass<X>] == Tier<Expected>` per the locked binding table in research.md R-002 / data-model.md §1.1. **Must pass after T009.**

**Checkpoint**: Types compile, immutable maps locked, stub Router exists, 8 tier bindings asserted, foundational tests green. User story work may now begin.

---

## Phase 3: User Story 1 — Critical → owner DM (Priority: P1) 🎯 MVP

**Goal**: Critical-tier alerts reach the operator's phone via `Sender.SendOwnerDM` exactly once; the audit channel receives nothing for Critical-tier alerts; transport failures surface `ErrAlertTransport` and refund both debounce slots.

**Independent Test**: A router built with a recording Sender + a fake clock routes a Critical-tier alert. Assert exactly one `SendOwnerDM` invocation with the rendered body, zero `PostChannel` invocations, no returned error. Then route a second Critical-tier alert for the same supervisor/pattern with an injected transport failure — assert `errors.Is(err, ErrAlertTransport)`, both buckets refunded (proven by a third successful Route immediately after, with no time advance).

### Tests for User Story 1 ⚠️

> Write these tests FIRST. They MUST fail before T020 lands.

- [X] T017 [US1] Implement the three test-helper Sender doubles + the `recordingHandler` slog handler in `internal/discord/alerts/alerts_test.go` (unexported, same package): `recordingSender` records every (method, ctx, args) tuple in a mutex-guarded slice and returns `nil`; `failingSender` returns a stored injected error from every method; `failOnInvokeSender` calls `t.Fatal` from any method. Also write the `fakeClock` value-type (an atomic `time.Time` with `advance(d)` method) — re-used by US4 tests.
- [X] T018 [P] [US1] Write `TestRoute_CriticalSendsDM` in `internal/discord/alerts/alerts_test.go` (B-A-4 + chunk-doc Prompt-4 mandated name): build a router with `recordingSender`, route an `Alert{Class: AlertClassApprovalRequest, SupervisorName: "sup-a", MachineName: "host-1", Detail: "scope=ANTHROPIC_API_KEY"}`, assert `SendOwnerDM` called exactly once with the rendered body, `PostChannel` called zero times, return nil. Cover the second Critical class (`AlertClassChildExit78StaleFailure`) in a sub-test.
- [X] T019 [US1] Write `TestRoute_CriticalTransportFailureRefundsBuckets` in `internal/discord/alerts/alerts_test.go` (B-A-13): inject a `failingSender`, route a Critical alert, assert `errors.Is(err, ErrAlertTransport)` AND `errors.As(err, &injectedErr)` recovers the underlying. Then swap to `recordingSender` AND immediately route again with the same supervisor/pattern (no time advance) — assert success, proving both buckets were refunded.

### Implementation for User Story 1

- [X] T020 [US1] In `internal/discord/alerts/alerts.go`, replace the `Route` stub with the synchronous flow per [data-model.md §4](./data-model.md#4-state-machine) steps 1-7. Initially implement steps 1 (class lookup), 6 (rendering — call the existing `classToTemplate[class].render(alert)` even though render is still a label-only stub at this point), and the Critical-tier dispatch branch in step 7 (call `sender.SendOwnerDM(ctx, rendered)`); the success/failure handling in step 8 (commit on success, refund + `errors.Join(ErrAlertTransport, underlying)` on failure). Bucket acquire/commit/refund is wired but the underlying `ratebucket` still stubs to "always granted" — real semantics land in T025. Run T018 + T019 → both pass; T015 still passes; the 8 tier-binding tests still pass.

**Checkpoint**: Critical-tier DM path works end-to-end with a real Sender. US1 MVP is testable independently.

---

## Phase 4: User Story 2 — Warning → audit channel (Priority: P1)

**Goal**: Warning-tier alerts post to the configured `auditChannelID` via `Sender.PostChannel` exactly once; the owner DM receives nothing for Warning-tier alerts; transport failures surface `ErrAlertTransport` and refund both debounce slots.

**Independent Test**: A router built with a recording Sender + audit-channel ID `"audit-ch-id"` routes a Warning-tier alert. Assert exactly one `PostChannel("audit-ch-id", ...)` invocation, zero `SendOwnerDM` invocations, no returned error. Mirror the transport-failure test from US1 for Warning-tier.

### Tests for User Story 2 ⚠️

- [X] T021 [P] [US2] Write `TestRoute_WarningPostsToAuditChannel` in `internal/discord/alerts/alerts_test.go` (B-A-5 + chunk-doc Prompt-4 mandated name): build a router with `recordingSender` + `auditChannelID = "audit-ch-id"`, route an `Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "sup-b", Detail: "scope=OPENAI_API_KEY"}`, assert `PostChannel` called exactly once with `("audit-ch-id", <rendered>)`, `SendOwnerDM` called zero times. Cover `AlertClassLogPatternStaleWarning` + `AlertClassDiscordDisconnected` in sub-tests.
- [X] T022 [US2] Write `TestRoute_WarningTransportFailureRefundsBuckets` in `internal/discord/alerts/alerts_test.go` (B-A-14): mirror T019's structure for the Warning path — inject `failingSender`, route a Warning alert, assert `errors.Is(err, ErrAlertTransport)`. Swap to `recordingSender` + immediately re-route same keys → asserts buckets refunded.

### Implementation for User Story 2

- [X] T023 [US2] In `internal/discord/alerts/alerts.go` `Route`, add the Warning-tier dispatch branch: call `sender.PostChannel(ctx, r.auditChannelID, rendered)`. Same commit-on-success / refund-on-failure semantics as US1. Run T021 + T022 → both pass.

**Checkpoint**: Warning-tier audit-channel path works. US1 and US2 are independently testable; the router correctly routes by tier with no auto-promotion.

---

## Phase 5: User Story 3 — Info → operational log only (Priority: P2)

**Goal**: Info-tier alerts trigger zero Discord network calls; the slog record at INFO level IS the delivery; rate-limit enforcement still applies to Info (FR-013).

**Independent Test**: A router built with `failOnInvokeSender` (`t.Fatal` on any invocation) + a recording slog handler routes an Info-tier alert. Assert no test failure, the slog handler captured exactly one INFO-level record with `outcome=delivered`, and the Sender was never called.

### Tests for User Story 3 ⚠️

- [X] T024 [P] [US3] Write `TestRoute_InfoLogsOnly_NoDiscordCall` in `internal/discord/alerts/alerts_test.go` (B-A-6 + chunk-doc Prompt-4 mandated name): build a router with `failOnInvokeSender` + a `recordingHandler` slog handler, route an `Alert{Class: AlertClassDiscordReconnected, SupervisorName: "sup-c"}`. Assert `Route` returns nil; the recording handler captured exactly one record at `slog.LevelInfo` with `msg == "alert routed"`; the Sender was NEVER invoked (proven by `failOnInvokeSender`'s lack of `t.Fatal` trigger).

### Implementation for User Story 3

- [X] T025 [US3] In `internal/discord/alerts/alerts.go` `Route`, add the Info-tier dispatch branch: emit `logger.LogAttrs(ctx, slog.LevelInfo, "alert routed", <allow-listed attrs>)`; commit both buckets; return nil. Critical/Warning success paths emit at `slog.LevelDebug`; both attribute sets are drawn from the FR-024 allow-list `{class, tier, supervisor, machine, pattern, outcome}`. Run T024 → passes.

**Checkpoint**: All three tier dispatch branches work. The router can be wired into the supervisor lifecycle. US1, US2, US3 are independently testable.

---

## Phase 6: User Story 4 — Rate-limit prevents flooding (Priority: P1)

**Goal**: Per-supervisor + per-pattern minimum-interval debounce blocks excess alerts; both buckets are isolated per key; empty `Alert.Pattern` falls back to `string(Class)` as the per-pattern key; rate-limit applies to every tier; monotonic time survives wall-clock changes.

**Independent Test**: A router with `perSupervisorBucket = 1 * time.Second` + `perPatternBucket = 1 * time.Second` + a fake clock — route two alerts for the same supervisor 100ms apart; first returns nil, second returns `ErrAlertRateLimited`; advance fake clock by 1.1s; third route returns nil.

### Tests for User Story 4 ⚠️

- [X] T026 [US4] Write `TestRateLimit_PerSupervisorBlocksExcess` in `internal/discord/alerts/ratelimit_test.go` (B-A-8 + chunk-doc Prompt-4 mandated name): build a router with `perSupervisorBucket = 1*time.Second`, route two Critical alerts for `SupervisorName: "sup-x"` 100ms apart on the fake clock — assert first returns nil, second returns `errors.Is(err, ErrAlertRateLimited)` AND zero Sender invocations from the second call AND zero slog records from the second call (caller logs per FR-016). Advance fake clock 1.1s, route a third — assert nil and one Sender invocation.
- [X] T027 [P] [US4] Write `TestRateLimit_PerPatternBlocksExcess` in `internal/discord/alerts/ratelimit_test.go` (B-A-9 + chunk-doc Prompt-4 mandated name): build a router with `perPatternBucket = 1*time.Second`, route two Warning alerts with the SAME `Pattern: "401-unauthorized"` from DIFFERENT supervisors — assert first nil, second `ErrAlertRateLimited` from the per-pattern bucket. Advance fake clock; third call succeeds.
- [X] T028 [P] [US4] Write `TestRateLimit_PerKeyIsolation` in `internal/discord/alerts/ratelimit_test.go` (B-A-10): route an alert for supervisor-A, then immediately route an alert for supervisor-B — assert both succeed (per-supervisor isolation). Same for two distinct patterns under the same supervisor.
- [X] T029 [P] [US4] Write `TestRateLimit_EmptyPatternUsesClassFallback` in `internal/discord/alerts/ratelimit_test.go` (B-A-11): route two alerts with `Pattern: ""` and the SAME `Class` (e.g. `AlertClassValidatorStaleFailure`) — assert second blocks. Then route two alerts with `Pattern: ""` and DIFFERENT classes (e.g. `AlertClassValidatorStaleFailure` + `AlertClassDiscordDisconnected`) — assert both succeed (each pattern-less class has its own bucket).
- [X] T030 [P] [US4] Write `TestRateLimit_AppliesToInfoTier` in `internal/discord/alerts/ratelimit_test.go` (B-A-12): build a router, route two Info-tier alerts for the same supervisor inside the window — assert second returns `ErrAlertRateLimited`. Proves rate-limit applies to every tier including Info (FR-013 — no class exemption).
- [X] T031 [P] [US4] Write `TestRateLimit_MonotonicClock` in `internal/discord/alerts/ratelimit_test.go` (B-A-23): construct a `*ratebucket` directly with an injected `now func() time.Time` that returns monotonic-bearing `time.Time` values; advance the fake clock by `window + 1ns` after a successful Route; assert the next Route succeeds. Demonstrates that `time.Sub` uses the monotonic reading regardless of wall-clock manipulation (FR-015).

### Implementation for User Story 4

- [X] T032 [US4] In `internal/discord/alerts/ratelimit.go`, replace the `acquire/commit/refund` stubs with the real implementation per [data-model.md §2.2](./data-model.md#22-ratebucket--bucketstate-r-009) + research.md R-009. `acquire(key)`: under mutex, return false if `pending != 0` OR `now() - delivered < window`; else set `pending = now()` and return true. `commit(key)`: under mutex, set `delivered = pending`, clear `pending`. `refund(key)`: under mutex, clear `pending` without touching `delivered`. All three methods race-safe via `sync.Mutex`. Wire the `*ratebucket` constructor to use a passed-in `now func() time.Time` (defaulting to `time.Now` from `NewRouter`).
- [X] T033 [US4] In `internal/discord/alerts/alerts.go` `Route`, wire the bucket calls per [data-model.md §4](./data-model.md#4-state-machine) steps 2-5: derive the per-pattern key (use `string(alert.Class)` when `alert.Pattern == ""`); call `supBucket.acquire(alert.SupervisorName)` — if false, return `ErrAlertRateLimited` directly (no slog record per FR-024a); call `patBucket.acquire(patternKey)` — if false, `supBucket.refund(alert.SupervisorName)` and return `ErrAlertRateLimited`. On success after dispatch: `supBucket.commit(...)` AND `patBucket.commit(...)`. On transport failure: refund both. Run T026 → T031: all pass; T018/T021/T024 still pass (single-call cases land inside their own first window).

**Checkpoint**: Rate-limit fully working. Both buckets isolated. Class-name fallback for empty Pattern works. Info-tier still rate-limited. Monotonic clock survives wall-clock changes.

---

## Phase 7: User Story 5 — Distinct visual labels per class (Priority: P2)

**Goal**: Every class has a distinct `[TIER][class-slug]` label prefix; the rendered body uses omit-empty-lines (empty optional fields produce NO `key=` segment); no template ever formats a field outside `{SupervisorName, MachineName, Pattern, Detail}`; the rendered output excludes `Alert.Time`; sentinel-byte assertion proves no credential-shaped substring survives any field into the rendered output.

**Independent Test**: For each of the 8 classes, build a fully-populated Alert (all four optional fields non-empty), render it, and assert the rendered string starts with the documented label prefix. Then render the same alert with one optional field empty at a time → assert the omitted field's segment is absent from the output. Then seed sentinel-byte markers into every field + a "secret-marker" byte string into the test environment (NEVER reachable from any Alert field) → render → assert the rendered output contains the operator-safe sentinels but never the secret-marker.

### Tests for User Story 5 ⚠️

- [X] T034 [US5] Write the 8 per-class `TestAlert_<ClassName>_RenderSnapshot` tests in `internal/discord/alerts/templates_test.go` (chunk-doc Prompt-4 mandated names) — one per class: `TestAlert_ApprovalRequest_RenderSnapshot`, `TestAlert_DaemonRefreshRequest_RenderSnapshot`, `TestAlert_ValidatorStaleFailure_RenderSnapshot`, `TestAlert_ChildExit78StaleFailure_RenderSnapshot`, `TestAlert_LogPatternStaleWarning_RenderSnapshot`, `TestAlert_DiscordDisconnected_RenderSnapshot`, `TestAlert_DiscordReconnected_RenderSnapshot`, `TestAlert_VaultUnreachableAtBootTimeout_RenderSnapshot`. Each builds a fully-populated `Alert` for its class, calls the unexported `render(alert)` (via same-package access), and asserts: (a) the rendered output begins with the locked label prefix from data-model.md §2.3; (b) the rendered output contains `SupervisorName`, `MachineName`, `Pattern`, `Detail` substrings.
- [X] T035 [P] [US5] Write `TestTemplate_LabelPrefixUniqueAndStable` in `internal/discord/alerts/templates_test.go` (B-A-16): iterate over all 8 entries in `classToTemplate`, collect each `labelPrefix`, assert the resulting slice contains 8 distinct strings (`len(uniqSet) == 8`). Also assert each prefix matches the locked values in data-model.md §2.3 verbatim.
- [X] T036 [P] [US5] Write `TestTemplate_OmitEmptyLines` in `internal/discord/alerts/templates_test.go` (B-A-17): table-driven over the 4 optional-field axes. For each (class, missing-field) pair: render an Alert with that field empty + others non-empty; assert the rendered output does NOT contain the empty field's marker (e.g., `machine=`, `pattern=`, `detail=`) and does NOT contain placeholder text like `<missing>`, `?`, or trailing `: `. The label prefix + `supervisor=<SupervisorName>` floor remains.
- [X] T037 [US5] Write `TestAlert_NoSecretByteLeakage` in `internal/discord/alerts/templates_test.go` (B-A-18): seed unique 16-byte random markers into `SupervisorName`, `MachineName`, `Pattern`, `Detail` (4 distinct operator-safe sentinels) AND a separate "secret-marker" byte string into the test's environment (e.g., a package-level test variable that is NEVER referenced by any Alert field). Render every one of the 8 classes; for each render assert: (a) all 4 operator-safe sentinels appear in the output; (b) the secret-marker NEVER appears; (c) the rendered output does NOT contain a formatted form of `Alert.Time` (e.g., no `time=` or `RFC3339`-formatted string).

### Implementation for User Story 5

- [X] T038 [US5] In `internal/discord/alerts/templates.go`, replace the stub `classTemplate.render` with the real omit-empty-lines implementation per [research.md R-007](./research.md#r-007--omit-empty-lines-template-rendering-clarification-q4): use a `strings.Builder`; always emit the `labelPrefix` and a single-line ` supervisor=<SupervisorName>` segment; emit ` machine=<MachineName>`, ` pattern=<Pattern>`, ` detail=<Detail>` ONLY when each field is non-empty. NO `text/template`. NEVER format `Alert.Time` or `Alert.Class` (Class is implicit in the prefix) or any field outside the 4-allow-list. Run T034 → T037: all pass.

**Checkpoint**: Templates render correctly with omit-empty-lines; sentinel-byte invariant holds; 8 distinct labels — operator triage by glance works.

---

## Phase 8: Cross-Cutting Hardening

**Purpose**: Tests that span every user story — slog discipline, attribute allow-list, concurrency, sentinel disjointness, zero-deps audit, unknown-class defensive path, caller-supplied-Tier-ignored.

- [X] T039 [P] Write `TestRoute_UnknownClass_TypedError` in `internal/discord/alerts/alerts_test.go` (B-A-15 + chunk-doc Prompt-4 mandated name): build a router with `recordingSender`, route an `Alert{Class: AlertClass("not-a-real-class")}`. Assert `errors.Is(err, ErrUnknownAlertClass)`; Sender called zero times; both buckets unaffected (a follow-up route for the same supervisor succeeds immediately); the slog handler captured exactly one WARN record with `outcome=unknown_class`.
- [X] T040 [P] Write `TestRoute_CallerSuppliedTierIgnored` in `internal/discord/alerts/alerts_test.go` (B-A-7): route an `Alert{Class: AlertClassValidatorStaleFailure (→ Warning), Tier: TierCritical (caller bug — Critical doesn't match Warning)}`. Assert the router calls `PostChannel` (Warning), NOT `SendOwnerDM` (Critical). Proves FR-004 "tier is class-derived, not caller-supplied".
- [X] T041 [P] Write `TestRoute_SlogLevelMatrix` in `internal/discord/alerts/alerts_test.go` (B-A-20): for each (tier × outcome) pair from research.md R-008, drive the corresponding Route call through a `recordingHandler` and assert the captured record's level matches the matrix: Critical-success=DEBUG, Warning-success=DEBUG, Info-success=INFO, transport-failure=WARN (any tier), unknown-class=WARN, rate-limited=NO RECORD (record count == 0).
- [X] T042 [P] Write `TestRoute_SlogAttributeAllowList` in `internal/discord/alerts/alerts_test.go` (B-A-21): drive every code path through a `recordingHandler`; collect the union of attribute keys across all captured records; assert the key set is a strict subset of `{class, tier, supervisor, machine, pattern, outcome}`. NO `detail`, `rendered`, `message`, `error`, `*http.*` keys.
- [X] T043 [P] Write `TestRoute_SentinelDisjointness` in `internal/discord/alerts/alerts_test.go` (B-A-24): assert `errors.Is(ErrAlertRateLimited, ErrAlertTransport) == false`, `errors.Is(ErrAlertRateLimited, ErrUnknownAlertClass) == false`, `errors.Is(ErrAlertTransport, ErrUnknownAlertClass) == false`, and the reverse pairs. Then drive each of the three error-producing code paths and assert the returned error matches ONLY its sentinel.
- [X] T044 [P] Write `TestRoute_ConcurrentSafety` in `internal/discord/alerts/alerts_test.go` (B-A-22 / SC-012): launch 8 goroutines × 100 Route calls each, mixed across all 8 classes and a range of supervisor keys (e.g., `"sup-0"..."sup-7"`) and pattern keys. All goroutines share one Router with `recordingSender`. Assert the run completes under `-race` clean; assert no double-decrement on any bucket (the cumulative successful Route count matches an analytic capacity calculation given the fake-clock window and the call interleave).
- [X] T045 [P] Write `TestRouter_ZeroNewDependencies` in `internal/discord/alerts/alerts_test.go` (B-A-19): use `go list -deps -f '{{.ImportPath}}'` (or equivalent in-test approach reading the package's `*.go` AST imports) to collect the import set of `internal/discord/alerts`; assert the set is a subset of `{context, errors, fmt, log/slog, strings, sync, time}` + the package's own files. Assert NONE of `github.com/bwmarrin/discordgo`, `github.com/mrz1836/hush/internal/discord`, `github.com/mrz1836/hush/internal/vault/securebytes` appear. (R-016, AC-N-1, AC-N-2, AC-N-3.)
- [X] T046 Implement the slog record emission discipline per R-008 in `internal/discord/alerts/alerts.go` (consolidate the per-tier emit calls into a single `emitOutcome(ctx, level, alert, outcome)` helper invoked at every terminal Route branch); ensure exactly one record per Route call except the `ErrAlertRateLimited` paths (zero records there); attribute set drawn strictly from the FR-024 allow-list. Run T041 + T042 → both pass.
- [X] T047 Implement the unknown-class WARN-and-return path in `internal/discord/alerts/alerts.go` `Route` step 1: when `classToTier[alert.Class]` lookup fails, emit `slog.LevelWarn` record with `outcome=unknown_class` (no `tier` attribute since the class didn't resolve) AND return `ErrUnknownAlertClass` BEFORE consulting either bucket. Run T039 → passes.
- [X] T048 In `internal/discord/alerts/alerts.go` `Route` step 2, ensure `Alert.Tier` is NEVER read — the local `tier` variable is sourced ONLY from `classToTier[alert.Class]`. Run T040 → passes.

**Checkpoint**: Every cross-cutting invariant asserted. The package's exported surface matches the chunk-doc + plan.md + data-model.md verbatim.

---

## Phase 9: SDD-11 Wiring — additive Sender methods on `*BotApprover`

**Purpose**: Make the production `*discord.BotApprover` structurally satisfy `alerts.Sender` without altering any locked SDD-11 symbol. The chunk-doc anti-contract (`PACKAGE-MAP.md:1245-1246` "SDD-28 MUST NOT alter any [SDD-11] symbol above") allows additive methods on the concrete type; this phase adds them in a NEW file so the diff against `internal/discord` is auditably small.

- [X] T049 Create `internal/discord/bot_alerts.go` (NEW file in the existing `internal/discord` package) and add two methods on `*BotApprover`:
  - `func (a *BotApprover) SendOwnerDM(ctx context.Context, message string) error` — uses `a.session.UserChannelCreate(a.ownerID)` then `a.session.ChannelMessageSendComplex(dm.ID, &discordgo.MessageSend{Content: message})`; on either failure returns `fmt.Errorf("hush/discord: send owner dm: %w", err)` (no Alert fields or secret material — the existing approver error redaction discipline carries through).
  - `func (a *BotApprover) PostChannel(ctx context.Context, channelID, message string) error` — calls `a.session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{Content: message})`; same error wrap. Reject empty `channelID` early with a static error (`errors.New("hush/discord: empty channel id")`).
- [X] T050 [P] Add the compile-time guard to `internal/discord/bot_alerts.go` (or alongside the existing approver guard in `internal/discord/bot.go`): `var _ alerts.Sender = (*BotApprover)(nil)`. Add the matching import `github.com/mrz1836/hush/internal/discord/alerts` at the top of `bot_alerts.go`. This is the ONLY place the discord pkg references alerts; alerts NEVER imports discord (asserted by T045).
- [X] T051 Write `TestBotApprover_SatisfiesAlertsSender` in `internal/discord/bot_alerts_test.go` (NEW file): use a sessionAPI shim (the existing `session_shim_test.go` precedent) to drive `(*BotApprover).SendOwnerDM` + `PostChannel`; assert the shim's `UserChannelCreate` + `ChannelMessageSendComplex` calls fire with the expected arguments. Also assert the compile-time `var _ alerts.Sender = (*BotApprover)(nil)` does not produce a build error (covered implicitly by `go build`).

**Checkpoint**: Production wiring exists. The supervisor lifecycle (SDD-24/25) can now wire `NewRouter(botApprover, ...)` directly without an adapter type.

---

## Phase 10: Polish & Gates

**Purpose**: Coverage proof, format/lint/test:race gates, documentation updates per the chunk-doc Prompt-5 checklist, and the combined commit.

- [X] T052 Run `go test -race -cover ./internal/discord/alerts/`; capture the final coverage percentage; assert ≥ 90.0% (chunk-doc target; SC-011). If under 90%, identify uncovered branches via `go tool cover -html=/tmp/alerts.cover` and add targeted tests until threshold is met.
- [X] T053 [P] Run `magex format:fix` from repo root; commit any formatting changes inline.
- [X] T054 [P] Run `magex lint`; resolve any golangci-lint findings on the new files (the alerts package + bot_alerts.go) — no `//nolint:` directives without an inline rationale comment.
- [X] T055 Run `magex test:race`; the entire repo test suite must pass clean under `-race`. Existing SDD-11/24/26/27 tests MUST continue passing — this chunk adds files but alters no locked symbol.
- [X] T056 Append a new "Exported API — locked at SDD-28" subsection to [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) under `## internal/discord/` (or as a new `## internal/discord/alerts/` block). List the 22 exported symbols verbatim per [data-model.md §1](./data-model.md#1-locked-exported-types) + [contracts/api.go](./contracts/api.go): `AlertClass` + 8 constants, `Tier` + 3 constants, `Alert` struct, `Sender` interface, `Router` (opaque), `NewRouter`, `Route`, three sentinels, `DefaultBucketWindow`. Add a note about the two additive methods on `*BotApprover` (`SendOwnerDM`, `PostChannel`) and the compile-time `var _ alerts.Sender = (*BotApprover)(nil)` guard.
- [X] T057 [P] Update [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-3 row: append `internal/discord/alerts/alerts_test.go`, `internal/discord/alerts/templates_test.go`, `internal/discord/alerts/ratelimit_test.go` as the alert-routing test files; note the 8 RenderSnapshot + 8 TierBinding tests cover the Discord-side operator surface.
- [X] T058 [P] Update [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-10 row: append the same three test files for the alert-emission half of Scenarios 2, 5, 6, 8, 10, 11, 15.
- [X] T059 [P] Update [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md) SDD-28 row: mark status `done`. Include a one-line summary listing the 22 exported symbols + the additive `*BotApprover` methods.
- [X] T060 Run the quickstart validation per [quickstart.md §3](./quickstart.md#3-test-commands): `go test -race -run Alert ./internal/discord/...` and `go test -race -run Route ./internal/discord/...` and `go test -race -run RateLimit ./internal/discord/...` and `go test -race -run Template ./internal/discord/...`. All four selector runs MUST complete green.
- [X] T061 Create the single combined commit per the chunk-doc Prompt-5 directive: `git add internal/discord/alerts/ internal/discord/bot_alerts.go internal/discord/bot_alerts_test.go docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md specs/028-discord-alerts/tasks.md && git commit -m "feat(discord/alerts): 8 classes + tiered routing + rate limit (SDD-28)"`. NO `--no-verify`. Pre-commit hooks (gitleaks, format check) MUST pass.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)** → no dependencies. Can start immediately.
- **Foundational (Phase 2)** → depends on Phase 1. BLOCKS all user stories. The 8 `AlertClass` constants, 3 `Tier` constants, `Alert` struct, `Sender` interface, `Router` skeleton, immutable `classToTier` + `classToTemplate` maps, and the test-helper plan are all required before any test in US1..US5 can compile.
- **US1 (Phase 3, P1)** → depends on Phase 2. **MVP candidate.**
- **US2 (Phase 4, P1)** → depends on Phase 2. May be parallel with US1 (different test names, different code branches in same `Route` function — sequential within `alerts.go` but tests compose independently).
- **US3 (Phase 5, P2)** → depends on Phase 2. May be parallel with US1, US2.
- **US4 (Phase 6, P1)** → depends on Phase 2. May be parallel with US1, US2, US3 for test writing (T026..T031); T032/T033 must merge after US1..US3's Route implementations land or together with them. Once T032 lands, the prior phases' tests stay green because the buckets are gated by `1 * time.Second` windows and each test creates its own fresh Router.
- **US5 (Phase 7, P2)** → depends on Phase 2 (T010 for `classToTemplate`). Test writing (T034..T037) can run in parallel with all other US phases. T038 (real render impl) can merge after T010 + T020 (US1's call site for `render`).
- **Cross-Cutting Hardening (Phase 8)** → depends on Phase 2 and the relevant user-story implementations (slog tests need T025; concurrency test needs T032; unknown-class test needs T020).
- **SDD-11 Wiring (Phase 9)** → depends on Phase 2 (the `alerts.Sender` interface). Can be done in parallel with US1..US5 by a second developer.
- **Polish (Phase 10)** → depends on all prior phases.

### Story-Level Dependencies

- US1 ↔ US2: orthogonal in test scope but both modify the same `Route` function in `alerts.go`. Tests can be written in parallel; the `Route` implementation lands incrementally (US1 first, then US2 adds the Warning branch). The function compiles cleanly at every step.
- US3 ↔ US1, US2: orthogonal — Info tier dispatch is a separate branch.
- US4 ↔ all others: rate-limit is wired AFTER tier dispatch lands. T033 modifies the same `Route` function; coordinate sequencing.
- US5 ↔ all others: template rendering is invoked from `Route` from T020 onward; the real render impl in T038 lands after the stub renders correctly enough for prefix assertions.

### Within Each User Story (TDD-mandatory per Constitution VIII)

- **Test tasks MUST precede implementation tasks in the same phase.** Verify each test FAILS against the foundational stubs before writing the implementation.
- Run `go test -race ./internal/discord/alerts/` after each implementation task to confirm the relevant tests turn green and no prior tests regress.

### Parallel Opportunities

- **Phase 1**: T002 + T003 can run in parallel.
- **Phase 2**: T006, T007, T008, T009, T010, T011, T012 can ALL run in parallel (each touches a different declaration block in `alerts.go` / `templates.go` / `ratelimit.go`); T013, T014, T015, T016 can ALL run in parallel after T004/T005/T009/T011 land.
- **Phase 3 + Phase 4 + Phase 5 + Phase 6**: test writing (T018, T019, T021, T022, T024, T026..T031) can ALL run in parallel — the tests compile against the foundational types and don't depend on each other's implementation.
- **Phase 7**: T034..T037 can all run in parallel.
- **Phase 8**: T039..T045 can all run in parallel (all are test files in the same package but each test is independent).
- **Phase 9**: T049, T050 are sequential (T050 imports the package T049 establishes); T051 parallel to T050.
- **Phase 10**: T053, T054 run in parallel after T052; T057, T058, T059 run in parallel after T056.

---

## Parallel Example: Phase 2 Foundational

```bash
# After T004 (AlertClass + 8 constants) + T005 (Tier + 3 constants) land,
# launch the remaining foundational tasks in parallel:
Task: "T006 - Declare Alert struct"            → internal/discord/alerts/alerts.go
Task: "T007 - Declare Sender interface"         → internal/discord/alerts/alerts.go
Task: "T008 - Declare three sentinel errors"   → internal/discord/alerts/alerts.go
Task: "T009 - Declare classToTier immutable map" → internal/discord/alerts/alerts.go
Task: "T010 - Declare classToTemplate + label prefixes" → internal/discord/alerts/templates.go
Task: "T011 - Declare Router + NewRouter stub" → internal/discord/alerts/alerts.go
Task: "T012 - Declare ratebucket + bucketState stubs" → internal/discord/alerts/ratelimit.go

# After all type declarations compile, launch the foundational tests in parallel:
Task: "T013 - TestAlertClass_ExportedSet"
Task: "T014 - TestTier_ExportedSet"
Task: "T015 - TestNewRouter_NilGuards"
Task: "T016 - 8 TestAlert_<Name>_TierBinding tests"
```

---

## Implementation Strategy

### MVP First (US1 + US2 + US4 — all P1)

1. Complete Phase 1: Setup (T001..T003).
2. Complete Phase 2: Foundational (T004..T016) — checkpoint: 4 tests green (T013, T014, T015, T016×8).
3. Complete Phase 3: US1 Critical → DM (T017..T020) — checkpoint: 6 tests green.
4. Complete Phase 4: US2 Warning → channel (T021..T023) — checkpoint: 8 tests green.
5. Complete Phase 6: US4 Rate limit (T026..T033) — checkpoint: 14 tests green.
6. **STOP and VALIDATE**: Test US1 + US2 + US4 — the P1 MVP scope (Critical DM + Warning channel + rate-limit enforcement) is independently testable.
7. Capture coverage; if ≥ 90%, MVP is shippable.

### Incremental Delivery

1. Setup + Foundational → foundation ready.
2. Add US1 → Critical-tier alerts reach the operator's phone → demo to operator (the load-bearing path).
3. Add US2 → Warning-tier audit-channel routing works → demo (the operator-review path).
4. Add US3 → Info-tier silent log capture works → demo (the unified Alert shape).
5. Add US4 → rate-limit prevents a flapping daemon from drowning the operator → demo under simulated crash loop.
6. Add US5 → distinct visual labels → operator can glance-triage → demo screenshot.
7. Cross-cutting hardening (Phase 8) → slog discipline + concurrency + sentinel hygiene → demo with `-race` clean.
8. SDD-11 wiring (Phase 9) → production `*BotApprover` satisfies `alerts.Sender` → wire into a supervisor lifecycle dry-run (caller side; integration validated in SDD-25).
9. Polish (Phase 10) → coverage gate ≥ 90% + format/lint/test:race + doc updates + combined commit.

### Parallel Team Strategy

With multiple developers:

1. All hands: Setup + Foundational together (Phases 1-2) — Foundational's T006..T012 can be split across developers (one per file or per declaration block).
2. Once Foundational is done:
   - Developer A: US1 (Critical DM) — Phase 3.
   - Developer B: US2 (Warning channel) — Phase 4 (parallel to A; both modify `Route` in `alerts.go` — coordinate via small, atomic commits).
   - Developer C: US3 (Info log only) — Phase 5.
   - Developer D: US4 (Rate limit) — Phase 6 (test writing parallel to A/B/C; T032/T033 merge last).
   - Developer E: US5 (Templates) — Phase 7.
   - Developer F: SDD-11 wiring — Phase 9.
3. Cross-cutting tests (Phase 8) and Polish (Phase 10) handled by the developer wrapping up last.

---

## Notes

- **[P] tasks** = different files OR independent declaration blocks in the same file; no dependency on incomplete tasks.
- **[Story] label** maps tasks to user stories from spec.md for traceability.
- **Verify tests fail before implementing** — that's the TDD-mandatory contract per Constitution VIII.
- **Commit after each task or logical group** — but the chunk-doc Prompt-5 specifies a SINGLE combined commit at the end (T061). The intermediate WIP can live on a feature branch and be squashed at T061 time.
- **Stop at any checkpoint to validate** — checkpoints after Phase 2, Phase 3, Phase 4, Phase 6 each give a usable mini-MVP.
- **AVOID**: vague tasks, same-file conflicts (Route function modified across US1..US4 — explicit sequencing required), cross-story dependencies that break independence (US3 Info-tier is the most independent because it doesn't touch the Sender; US4 rate-limit gates EVERY tier so it depends on all dispatch branches existing).

---

## Task Count Summary

- **Phase 1 — Setup**: 3 tasks (T001..T003)
- **Phase 2 — Foundational**: 13 tasks (T004..T016)
- **Phase 3 — US1 (P1)**: 4 tasks (T017..T020)
- **Phase 4 — US2 (P1)**: 3 tasks (T021..T023)
- **Phase 5 — US3 (P2)**: 2 tasks (T024..T025)
- **Phase 6 — US4 (P1)**: 8 tasks (T026..T033)
- **Phase 7 — US5 (P2)**: 5 tasks (T034..T038)
- **Phase 8 — Cross-Cutting**: 10 tasks (T039..T048)
- **Phase 9 — SDD-11 Wiring**: 3 tasks (T049..T051)
- **Phase 10 — Polish**: 10 tasks (T052..T061)

**Total: 61 tasks.**

Tests written: **27 named tests** (8 RenderSnapshot + 8 TierBinding + 11 cross-cutting/dispatch/rate-limit/negative tests — exceeds the 25 named in [quickstart.md §4](./quickstart.md#4-mandatory-test-list-per-speckit-tasks-phase-4) because the chunk-doc Prompt-4 expansion to 8 per-class RenderSnapshot + 8 per-class TierBinding produces 16 named functions where the original quickstart used 4 table-driven tests). Every test is named in this tasks.md so /speckit-implement can match task → test → assertion in one pass.

Coverage target: **≥ 90%** on `internal/discord/alerts/` via `magex test:race` (T052 + T055).

---

## Cross-references

| Resource | Path |
|----------|------|
| Plan | [plan.md](./plan.md) |
| Spec | [spec.md](./spec.md) |
| Research | [research.md](./research.md) (R-001..R-016) |
| Data model | [data-model.md](./data-model.md) (A-1..A-20) |
| Contracts — typed mirror | [contracts/api.go](./contracts/api.go) |
| Contracts — behaviors | [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) (B-A-1..B-A-25) |
| Quickstart | [quickstart.md](./quickstart.md) |
| Chunk doc | [docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md) |
| Lifecycle scenarios | [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §"Required alert classes" |
| Constitution | [.specify/memory/constitution.md](../../.specify/memory/constitution.md) §§V, VIII, IX, X |
| SDD-11 locked surface (anti-contract source) | [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) §`internal/discord/` |
| SDD-11 BotApprover | [internal/discord/bot.go](../../internal/discord/bot.go) |
| Testing standards | [.github/tech-conventions/testing-standards.md](../../.github/tech-conventions/testing-standards.md) |
