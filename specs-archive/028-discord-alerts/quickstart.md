# Quickstart — SDD-28 (`internal/discord/alerts`)

**Feature:** 028-discord-alerts
**Date:** 2026-05-13

This file is the operator-of-this-feature's runbook. It enumerates
the test commands that prove the chunk works, the lifecycle scenarios
it serves, and the order in which a reader should engage with the
artifacts. Read top to bottom; everything between fenced blocks is a
copy-paste command.

---

## 1. Read the contract

In order:

1. [spec.md](./spec.md) — WHAT (5 user stories, ~27 functional requirements + 4 sub-FRs, 13 success criteria, 5 clarifications)
2. [research.md](./research.md) — WHY each implementation decision was made (R-001..R-016)
3. [data-model.md](./data-model.md) — locked struct shapes + 22 invariants (A-1..A-22)
4. [contracts/api.go](./contracts/api.go) — typed mirror of the locked Go signatures
5. [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) — black-box behavior spec (B-A-1..B-A-28)

The constitution check is in [plan.md § Constitution Check](./plan.md#constitution-check);
all four in-scope principles (V, VIII, IX, X) pass. Constitution XI
(dependencies) is verified by `TestAlerts_ZeroNewDependencies`.

---

## 2. Lifecycle-scenario coverage map

This chunk owns the alert-routing surface; it does NOT own the
event sources. The router consumes typed `Alert` values from
whichever supervisor or bot component detected the underlying
condition.

8 of the 15 lifecycle rows emit alerts via this router (the
remaining 7 produce no alert classes):

| Spec Story | Lifecycle row | Alert classes emitted | Test names |
|------------|---------------|------------------------|------------|
| Story 1 — Critical → DM owner | LIFECYCLE Scenarios 2, 5, 8, 11 | `AlertClassApprovalRequest`, `AlertClassDaemonRefreshRequest`, `AlertClassChildExit78StaleFailure`, `AlertClassVaultUnreachableAtBootTimeout` | `TestAlerts_CriticalSendsDM`, `TestAlerts_TierBindingMatrix` |
| Story 2 — Warning → audit channel | LIFECYCLE Scenarios 6, 10, 15 | `AlertClassValidatorStaleFailure`, `AlertClassDiscordDisconnected`, `AlertClassLogPatternStaleWarning` | `TestAlerts_WarningPostsToAuditChannel`, `TestAlerts_TierBindingMatrix` |
| Story 3 — Info → audit log only | LIFECYCLE Scenario 10 (recovery) | `AlertClassDiscordReconnected` | `TestAlerts_InfoLogsOnly_NoDiscordCall`, `TestAlerts_SlogLevelMatrix` |
| Story 4 — Rate limit blocks flooding | (cross-cutting) | (all 8) | `TestAlerts_RateLimitPerSupervisorBlocksExcess`, `TestAlerts_RateLimitPerPatternBlocksExcess`, `TestAlerts_RateLimitPerKeyIsolation`, `TestAlerts_RateLimitAppliesToInfoTier`, `TestAlerts_RateLimitEmptyPatternUsesClassFallback`, `TestAlerts_RateLimitMonotonicClock` |
| Story 5 — Distinct visual labels | (cross-cutting) | (all 8) | `TestAlerts_LabelPrefixUniqueAndStable`, `TestAlerts_TemplateOmitEmptyLines`, `TestAlerts_NoSecretLeakInRendered_<Class>` (×8) |

The transport-failure recovery path (SC-010a) is exercised by
`TestAlerts_CriticalTransportFailureRefundsBuckets` and
`TestAlerts_WarningTransportFailureRefundsBuckets` for both DM
and channel-post paths.

The eight emission sources downstream of this chunk (validator
package SDD-26 emits ValidatorStaleFailure, watchdog package
SDD-27 emits LogPatternStaleWarning, the bot package SDD-11 emits
Discord disconnect/reconnect via its monitor goroutine, etc.) wire
into the Router from their respective callers via the SDD-25
lifecycle orchestrator. This chunk's tests stop at "Route()
returns the expected outcome for each typed Alert input".

---

## 3. Test commands

```sh
# Unit + race + coverage gate (the v0.1.0 gate)
go test -race -cover ./internal/discord/alerts/

# Cover threshold ≥ 90% (chunk-doc target, plan §Constitution Check VIII)
go test -coverprofile=/tmp/alerts.cover ./internal/discord/alerts/
go tool cover -func=/tmp/alerts.cover | tail -1
# expected: total: (statements) >= 90.0%

# Run just the named alerts tests under the discord package:
go test -race -run Alerts ./internal/discord/...

# Full repo gate (pre-commit gate; also runs as part of /speckit-implement Prompt 5)
magex format:fix && magex lint && magex test:race
```

The race-clean assertion is non-negotiable per Constitution VIII /
Plan §Constitution Check.

---

## 4. Mandatory test list (per /speckit-tasks Phase 4)

These tests MUST be written BEFORE the implementation code per
Constitution VIII (TDD-mandatory). They are sourced from
[contracts/observable-behaviors.md](./contracts/observable-behaviors.md)
(B-A-1..B-A-28) and pinned by [data-model.md §7 A-1..A-22](./data-model.md#7-invariants-a-1a-22).

| #  | Test name                                                | Behavior | Spec FR / SC          | File                  |
|----|----------------------------------------------------------|----------|------------------------|-----------------------|
|  1 | `TestAlerts_AlertClassExportedSet`                       | B-A-1    | SC-001, FR-001         | `alerts_test.go`      |
|  2 | `TestAlerts_ClassToTierIsImmutable`                      | B-A-2    | FR-003, FR-005         | `alerts_test.go`      |
|  3 | `TestAlerts_TierExportedSet`                             | B-A-3    | SC-002, FR-002         | `alerts_test.go`      |
|  4 | `TestAlerts_TierBindingMatrix`                           | B-A-4    | SC-003, FR-003         | `alerts_test.go`      |
|  5 | `TestAlerts_CriticalSendsDM`                             | B-A-5    | SC-004, FR-006         | `alerts_test.go`      |
|  6 | `TestAlerts_WarningPostsToAuditChannel`                  | B-A-6    | SC-004, FR-007         | `alerts_test.go`      |
|  7 | `TestAlerts_InfoLogsOnly_NoDiscordCall`                  | B-A-7    | SC-005, FR-008, FR-024a | `alerts_test.go`     |
|  8 | `TestAlerts_CallerSuppliedTierIgnored`                   | B-A-8    | FR-004                  | `alerts_test.go`      |
|  9 | `TestAlerts_RateLimitPerSupervisorBlocksExcess`          | B-A-9    | SC-006, FR-010, FR-016 | `ratelimit_test.go`   |
| 10 | `TestAlerts_RateLimitPerPatternBlocksExcess`             | B-A-10   | SC-006, FR-011         | `ratelimit_test.go`   |
| 11 | `TestAlerts_RateLimitPerKeyIsolation`                    | B-A-11   | SC-007, FR-014         | `ratelimit_test.go`   |
| 12 | `TestAlerts_RateLimitEmptyPatternUsesClassFallback`      | B-A-12   | Q2, FR-011a            | `ratelimit_test.go`   |
| 13 | `TestAlerts_RateLimitAppliesToInfoTier`                  | B-A-13   | FR-013                  | `ratelimit_test.go`   |
| 14 | `TestAlerts_CriticalTransportFailureRefundsBuckets`      | B-A-14   | SC-010a, FR-012a/b     | `alerts_test.go`      |
| 15 | `TestAlerts_WarningTransportFailureRefundsBuckets`       | B-A-15   | SC-010a, FR-012a/b     | `alerts_test.go`      |
| 16 | `TestAlerts_UnknownClassTypedError`                      | B-A-16   | SC-010, FR-009         | `alerts_test.go`      |
| 17 | `TestAlerts_LabelPrefixUniqueAndStable`                  | B-A-17   | SC-008, FR-017/018     | `templates_test.go`   |
| 18 | `TestAlerts_TemplateOmitEmptyLines`                      | B-A-18   | Q4, FR-021             | `templates_test.go`   |
| 19 | `TestAlerts_NoSecretLeakInRendered_<Class>` (×8 sub-tests) | B-A-19 | SC-009, FR-022, FR-023 | `templates_test.go`   |
| 20 | `TestAlerts_LogAttrAllowList`                            | B-A-20   | FR-024, R-008          | `alerts_test.go`      |
| 21 | `TestAlerts_SlogLevelMatrix`                             | B-A-21   | Q5, FR-024a            | `alerts_test.go`      |
| 22 | `TestAlerts_RateLimitMonotonicClock`                     | B-A-22   | FR-015, R-015          | `ratelimit_test.go`   |
| 23 | `TestAlerts_NewRouterConfigGuards`                       | B-A-23   | R-011                   | `alerts_test.go`      |
| 24 | `TestAlerts_SentinelDisjointness`                        | B-A-24   | FR-012/012b, FR-009    | `alerts_test.go`      |
| 25 | `TestAlerts_ConcurrentRoute`                             | B-A-25   | SC-012, FR-026         | `alerts_test.go`      |
| 26 | `TestAlerts_NoSecureBytesImport` / `TestAlerts_NoSecureBytesStringConversion` | B-A-26 | Constitution X | `alerts_test.go` |
| 27 | `TestAlerts_ZeroNewDependencies`                         | B-A-27   | Constitution IX/XI     | `alerts_test.go`      |
| 28 | `TestAlerts_NoStrayClassStringsEmitted`                  | B-A-28   | SC-001, FR-005         | `alerts_test.go`      |

28 named tests (counting B-A-19's 8 per-class sub-tests as one
row); three test files
(`alerts_test.go`, `templates_test.go`, `ratelimit_test.go`) +
three production files
(`alerts.go`, `templates.go`, `ratelimit.go`).

Test helpers (`recordingSender`, `failingSender`,
`failOnInvokeSender`, fake-clock function value, recording slog
handler) live in unexported scope inside `alerts_test.go`
(mirrors the SDD-21 `helpers_test.go` inline-helper precedent).

The chunk-doc Prompt-4 mandatory test names map to this list as
follows:

- `TestAlert_<Name>_RenderSnapshot` (per chunk-doc, 1 per class) +
  `TestAlert_<Name>_TierBinding` (per chunk-doc, 1 per class) are
  realised here as the TABLE-DRIVEN tests `TestAlerts_LabelPrefixUniqueAndStable`
  (#17) + `TestAlerts_TemplateOmitEmptyLines` (#18) +
  `TestAlerts_NoSecretLeakInRendered_<Class>` (#19, ×8 sub-tests) for
  render snapshots, and `TestAlerts_TierBindingMatrix` (#4) for
  tier bindings. Table-driven tests are the
  `.github/tech-conventions/testing-standards.md` preferred form
  for "N parallel assertions over a closed enumeration"; the chunk
  doc's "16 class-level tests" count is preserved via the table
  rows + 8 explicit per-class sentinel-byte sub-tests in B-A-19.
- `TestRoute_CriticalSendsDM` → `TestAlerts_CriticalSendsDM` (#5).
- `TestRoute_WarningPostsToAuditChannel` → `TestAlerts_WarningPostsToAuditChannel` (#6).
- `TestRoute_InfoLogsOnly_NoDiscordCall` → `TestAlerts_InfoLogsOnly_NoDiscordCall` (#7).
- `TestRateLimit_PerSupervisorBlocksExcess` → `TestAlerts_RateLimitPerSupervisorBlocksExcess` (#9).
- `TestRateLimit_PerPatternBlocksExcess` → `TestAlerts_RateLimitPerPatternBlocksExcess` (#10).
- `TestRoute_UnknownClass_TypedError` → `TestAlerts_UnknownClassTypedError` (#16).

Every chunk-doc-mandated test maps to a row in the table.

---

## 5. Constitution check at a glance

Detailed table in [plan.md § Constitution Check](./plan.md#constitution-check).
In-scope principles per chunk doc + user prompt: **V, VIII, IX, X**.
**XI** (dependencies) is verified by `TestAlerts_ZeroNewDependencies`.

| Principle | Compliance proof |
|-----------|-------------------|
| V Staleness loud | WARN on every transport failure (B-A-14/15), WARN on unknown class (B-A-16); router NEVER auto-promotes / demotes tier (B-A-8); class→tier binding is code-asserted (B-A-4). |
| VIII TDD + race + ≥90% | 28-test list above, all written BEFORE implementation; `go test -race -cover` ≥ 90.0%; concurrent-safety test (B-A-25) under `-race`. |
| IX Context, errors, no globals/init, goroutines | `Route(ctx, alert)` first-param ctx; four sentinels via `var Err... = errors.New(...)`; zero `init()`; ZERO goroutines spawned by this package; consumer-side `Sender` interface defined inside the package; `NewRouter` returns `(*Router, error)` for operator-input invariants (Constitution IX panic policy preserved). |
| X Observability + redaction | NO `*SecureBytes` import (B-A-26); rendered body excludes `Time` and contains only the 4 operator-safe fields; per-class sentinel-byte tests assert no credential-shaped substring survives (B-A-19, ×8); slog attribute allow-list excludes `detail` and any credential-derived field (B-A-20). |
| XI Dependencies | Zero new direct deps; imports only `context`, `errors`, `fmt`, `log/slog`, `strings`, `sync`, `time` (B-A-27). |

---

## 6. Post-implement checklist (SDD-28 Prompt 5)

The implementation phase commits these doc updates alongside the
code in a single combined commit (per the chunk-doc Prompt 5):

| File | Change |
|------|--------|
| [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) `internal/discord/` | Append "Exported API — locked at SDD-28" subsection at the sub-package path `github.com/mrz1836/hush/internal/discord/alerts`, listing AlertClass + 8 constants, Tier + 3 constants, Alert struct, Sender interface, Router (opaque), NewRouter (`(*Router, error)`), Route, four sentinel errors (ErrAlertRateLimited, ErrAlertTransport, ErrAlertUnknownClass, ErrAlertConfig). |
| [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-3 row | Append `internal/discord/alerts/alerts_test.go`, `templates_test.go`, `ratelimit_test.go` (the Discord-side operator surface subset). |
| [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-10 row | Append the same three test files (alert emission subset of supervisor lifecycle). |
| [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md) SDD-28 row | Mark status `done`. |
| [specs/028-discord-alerts/tasks.md](./tasks.md) | Already updated by /speckit-tasks (Phase 4); included in the combined commit. |

Combined commit message:
`feat(discord/alerts): 8 classes + tiered routing + rate limit (SDD-28)`.

---

## 7. Out of scope for this chunk

- **Audit-row writing.** The router emits operational slog records
  per Q5/R-008; it does NOT write to the hash-chained audit
  ledger. SDD-13 / SDD-24 own audit-row persistence (Spec
  Assumption row 8).
- **Emission of any specific alert class.** The router consumes
  `Alert` values; it does NOT generate them. SDD-24 lifecycle
  orchestration, SDD-26 validators, SDD-27 watchdog, SDD-11 bot
  monitor are the upstream emission sources.
- **Audit-channel mirror for the approval flow.** SDD-11's
  `internal/discord/audit.go` mirrors approval-flow events to the
  audit channel as part of SDD-11. The alerts package handles
  generic alert routing, NOT approval-prompt mirroring.
- **Adapter from `*discord.BotApprover` to `alerts.Sender`.**
  Downstream wiring (SDD-25 or a future glue layer) writes the
  adapter; this chunk only defines the `Sender` interface and
  consumes whatever satisfies it.
- **Configurable class→tier mapping.** The mapping is fixed code
  (FR-003). Adding or removing a class is a chunk-level amendment
  (FR-005, SC-007).
- **Internal retries on transport failure.** Single-shot per
  FR-012b (Clarification Q3); the caller (supervisor lifecycle)
  owns back-off.
- **Discord SDK calls.** All transport stays behind the `Sender`
  interface (R-004, R-016).

---

## 8. Cross-references

| Resource | Path |
|----------|------|
| Constitution | [.specify/memory/constitution.md](../../.specify/memory/constitution.md) |
| Spec | [spec.md](./spec.md) |
| Phase 0 research | [research.md](./research.md) |
| Phase 1 data model | [data-model.md](./data-model.md) |
| Phase 1 contracts | [contracts/api.go](./contracts/api.go) · [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) |
| Chunk doc | [docs/sdd/SDD-28.md](../../docs/sdd/SDD-28.md) |
| Lifecycle scenarios | [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md) §"Required alert classes" + Scenarios 5, 6, 8, 10, 11, 15 |
| Operations | [docs/OPERATIONS.md](../../docs/OPERATIONS.md) |
| Package map (target) | [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) `internal/discord/` |
| SDD-11 ratelimit precedent | [internal/discord/ratelimit.go](../../internal/discord/ratelimit.go) |
| SDD-11 Approver interface | [internal/discord/approver.go](../../internal/discord/approver.go) |
| SDD-27 sub-package precedent | [specs/027-watchdog/plan.md](../027-watchdog/plan.md) |
