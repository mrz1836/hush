# SDD-28 — `internal/discord/alerts` (8 alert classes + tiered routing + DM rate limit + refresh nudges)

**Phase:** 6
**Package:** `internal/discord/alerts`
**Files:** `alerts.go`, `templates.go`, `ratelimit.go`, `*_test.go`
**Branch:** `028-discord-alerts` (created by the `before_specify` git hook)
**Blocked by:** SDD-11, SDD-27
**Blocks:** SDD-25 (alert assertions in lifecycle scenarios)
**Primary AC:** AC-3, AC-10
**Coverage target:** 90%

**Behaviour contracts (MUST):**
- `type Alert struct { Class AlertClass; Tier Tier; ... fields }`
- 8 named `AlertClass` constants (per `docs/LIFECYCLE-SCENARIOS.md` "Required Alert Classes")
- 3 `Tier` constants: `TierCritical`, `TierWarning`, `TierInfo`
- Templates: distinct label prefixes; format string per class with named-field placeholders
- Routing: Critical → DM owner; Warning → audit channel; Info → audit log only (no Discord call)
- Rate limiters per supervisor and per pattern

**Anti-contracts (MUST NOT):**
- Auto-promote a Warning to Critical
- Skip rate-limit for any class

**Tests required:**
- Render snapshot per class (8 tests), tier routing correct (3 tests, one per tier), rate-limit blocks excess (2 tests, per-supervisor + per-pattern)

**Constitutional principles in scope:** V (operator visibility — these are the Discord-side surface), VIII, X (no secret values in templates)

**Exported API to lock in PACKAGE-MAP.md (this chunk — new entry):**
- `type AlertClass string`  with 8 named constants (per `docs/LIFECYCLE-SCENARIOS.md` Required Alert Classes)
- `type Tier int`  with `TierCritical`, `TierWarning`, `TierInfo`
- `type Alert struct { Class AlertClass; Tier Tier; SupervisorName, MachineName string; Pattern, Detail string; Time time.Time }`
- `type Router struct { ... }`
- `func NewRouter(approver discord.Approver, auditChannelID string, perSupervisorBucket, perPatternBucket time.Duration, logger *slog.Logger) *Router`
- `func (r *Router) Route(ctx context.Context, alert Alert) error`
- `var ErrAlertRateLimited`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-28 (internal/discord/alerts:
8 alert classes + tiered routing + rate limit) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles V, X)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Required Alert Classes section — the 8 named classes are listed here)
- /Users/mrz/projects/hush/docs/OPERATIONS.md  (alert tiers — Critical/Warning/Info routing rules)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-7, AC-3, AC-10)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-3 + AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-28.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
This chunk delivers the operator-facing alert surface: every event
worth telling the operator about (watchdog match, validator failure,
boot-retry exhaustion, vault-reload, etc.) flows through here as a
typed Alert and is routed to the right place — DM the owner for
critical, post to the audit channel for warnings, write to the
audit log only for info. Per-supervisor and per-pattern rate
limiters prevent flooding.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- Exactly 8 named alert classes — the set documented in
  docs/LIFECYCLE-SCENARIOS.md "Required Alert Classes". No
  additions or omissions in this chunk.
- 3 named tiers: Critical, Warning, Info. Routing is strictly:
    Critical → DM the configured owner.
    Warning  → post to the configured audit channel.
    Info     → audit log only; no Discord API call.
- A Warning is NEVER auto-promoted to Critical (and vice
  versa) — the class fixes the tier.
- Per-supervisor and per-pattern rate limiters apply to every
  class. Excess alerts return ErrAlertRateLimited; caller logs.
- Alert templates have distinct visual labels per class; alert
  bodies MUST NEVER include any secret value or token content.

The spec MUST NOT encode HOW (no library names, no specific
discordgo SDK calls). Those are plan-phase.

Acceptance criteria: AC-3 (Discord-side operator surface), AC-10
(supervisor lifecycle alerts).

Action — run exactly one command:
  /speckit-specify "internal/discord/alerts: 8 named alert classes (per docs/LIFECYCLE-SCENARIOS.md Required Alert Classes) and 3 tiers (Critical→DM owner, Warning→audit channel, Info→audit log only); per-supervisor and per-pattern rate limits; templates with distinct visual labels and zero secret-value leakage; tier-class binding is fixed (no auto-promotion)"

The before_specify hook will create branch 028-discord-alerts.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-28 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-28.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-28 (internal/discord/alerts)
of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; V/VIII/X load-bearing)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Required Alert Classes section — the 8 names + their tiers are load-bearing)
- /Users/mrz/projects/hush/docs/OPERATIONS.md  (alert tiers + routing destinations)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-7, AC-3, AC-10)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (no internal/discord/alerts entry yet)
- /Users/mrz/projects/hush/docs/sdd/SDD-28.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/discord/alerts (NEW)
- Files: alerts.go (Alert + AlertClass + Tier + Router),
  templates.go (per-class template strings + render helpers),
  ratelimit.go (per-supervisor + per-pattern token buckets),
  alerts_test.go, templates_test.go, ratelimit_test.go
- Exported API:
    type AlertClass string
    const (
        // exactly the 8 names from docs/LIFECYCLE-SCENARIOS.md
        // — copy the names verbatim from that doc
        AlertClassXxx, AlertClassYyy, ...
    )
    type Tier int
    const ( TierCritical Tier = iota; TierWarning; TierInfo )
    type Alert struct {
        Class AlertClass; Tier Tier;
        SupervisorName, MachineName string;
        Pattern, Detail string;
        Time time.Time
    }
    type Router struct { ... }
    func NewRouter(approver discord.Approver, auditChannelID string,
                   perSupervisorBucket, perPatternBucket time.Duration,
                   logger *slog.Logger) *Router
    func (r *Router) Route(ctx context.Context, alert Alert) error
    var ErrAlertRateLimited

Implementation contract (HOW — locked):
- The 8 alert class constants and their fixed tier assignments
  come verbatim from docs/LIFECYCLE-SCENARIOS.md. The
  package-level classToTier map is documented and immutable.
- Router.Route():
    1. Look up tier for alert.Class. If unknown class → return
       a typed error (defensive — should never happen if the
       caller used the constants).
    2. Apply per-supervisor + per-pattern rate limit. Either
       limiter rejecting → return ErrAlertRateLimited.
    3. Render the alert via templates.go (class-specific
       format string + visual label prefix).
    4. Route by tier:
        TierCritical → discord.Approver-style DM to owner
                       (use a simple SendDM helper if Approver
                       doesn't expose one — define one)
        TierWarning  → post to audit channel via discordgo
        TierInfo     → log INFO via the configured logger; no
                       Discord API call at all.
- Templates: every class has a distinct, descriptive label
  prefix (e.g. "[CRITICAL][stale-credential]") and a format
  string with named-field placeholders that map to Alert
  fields. Templates MUST NEVER format any field that could
  contain secret values — they only format SupervisorName,
  MachineName, Pattern, Detail (Detail is operator-supplied
  metadata, NOT secret material).
- Rate limiter: two token buckets per Route call — one keyed
  by SupervisorName, one keyed by Pattern. Either bucket
  exhaustion → ErrAlertRateLimited.

Coverage target: 90%.
Constitutional principles in scope: V, VIII, IX, X.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-28 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-28.md AND
docs/LIFECYCLE-SCENARIOS.md (the 8 alert class names live there).

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 90%. For each of the 8 alert classes (names per docs/LIFECYCLE-SCENARIOS.md): TestAlert_<Name>_RenderSnapshot (assert label prefix + body format) and TestAlert_<Name>_TierBinding (assert the class maps to the documented tier). Plus tier-routing tests: TestRoute_CriticalSendsDM, TestRoute_WarningPostsToAuditChannel, TestRoute_InfoLogsOnly_NoDiscordCall (use a fake discord.Approver that fails the test if called for an Info-tier alert). Plus rate-limit tests: TestRateLimit_PerSupervisorBlocksExcess, TestRateLimit_PerPatternBlocksExcess. Plus negative tests: TestRoute_UnknownClass_TypedError. Final phase MUST include magex format:fix, magex lint, magex test:race."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-28 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-28.md AND
docs/LIFECYCLE-SCENARIOS.md (verify the 8 names you wrote into the
constants match the doc verbatim).

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 90% on internal/discord/alerts:
     go test -cover ./internal/discord/alerts/
3. Confirm all 8 alert classes have render-snapshot tests AND
   tier-binding tests (16 class-level tests total).
4. Confirm tier routing tests prove TierInfo NEVER calls Discord
   (TestRoute_InfoLogsOnly_NoDiscordCall fails if Approver is
   called).
5. Confirm rate limit tests cover both per-supervisor AND
   per-pattern buckets.
6. Append a NEW internal/discord/alerts entry to
   docs/PACKAGE-MAP.md titled "Exported API — locked at SDD-28"
   listing the locked API from the chunk doc (AlertClass + 8
   constants, Tier + 3 constants, Alert struct, Router,
   NewRouter, Route, ErrAlertRateLimited).
7. Update docs/AC-MATRIX.md AC-3, AC-10 rows with the new test
   file paths.
8. Mark SDD-28 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/discord/alerts/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(discord/alerts): 8 classes + tiered routing + rate limit (SDD-28)"

Final message: confirm gates passed, race-clean, coverage ≥ 90%,
all 8 classes have render samples + tier-binding tests, rate
limit asserted (per-supervisor + per-pattern), Info-tier never
calls Discord, AC-3 + AC-10 rows updated, SDD-PLAYBOOK updated,
and the combined commit created.
```
