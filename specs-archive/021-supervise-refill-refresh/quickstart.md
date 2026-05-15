# Quickstart — SDD-21 (`internal/supervise` refill + refresh + grace)

**Feature:** 021-supervise-refill-refresh
**Date:** 2026-05-10

This file is the operator-of-this-feature's runbook. It enumerates
the test commands that prove the chunk works, the lifecycle scenarios
they cover, and the order in which a reader should engage with the
artifacts. Read top to bottom; everything that's a copy-paste command
is between fenced blocks.

---

## 1. Read the contract

In order:

1. [spec.md](./spec.md) — WHAT (5 user stories, 27 functional requirements, 10 success criteria)
2. [research.md](./research.md) — WHY each implementation decision was made (R-001..R-016)
3. [data-model.md](./data-model.md) — locked struct shapes + invariants
4. [contracts/api.go](./contracts/api.go) — typed-mirror of the locked Go signatures
5. [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) — black-box behavior spec

The constitution check is in [plan.md § Constitution Check](./plan.md);
all five in-scope principles (IV, V, VIII, IX, X) pass without
exceptions.

---

## 2. Lifecycle-scenario coverage map

The chunk's tests cover these LIFECYCLE-SCENARIOS rows directly. Every
listed test MUST pass before the chunk is considered complete (SC-021-1).

| LIFECYCLE-SCENARIO | Spec story | Test name | File |
|--------------------|-----------|-----------|------|
| 3 — Clean child exit → silent refill | Story 1 | `TestRefill_SilentOnCleanExit` | `refill_test.go` |
| 7 — Vault server restart (401 unknown-jti) | Story 1 (2nd accept) | `TestRefill_401UnknownJTITransitions` | `refill_test.go` |
| 8 — Daytime refresh-window prompt | Story 2 | `TestRefresh_FiresInWindow`, `TestRefresh_FiresOnStartIfInsideWindow` | `refresh_test.go` |
| 9 — Overnight expiry with grace cache | Story 3 | `TestGrace_UsesCacheOnExpiredJWT`, `TestGrace_TTLCapAt4h` | `grace_test.go` |
| 11 — Tailscale boot retry / startup ordering | Story 4 (smoke) | `TestBootRetry_BackoffRespected`, `TestBootRetry_NeverPromptsDiscord` | `refill_test.go` |

Stories 4 (boot retry full path) and 5 (DM rate limit) are out-of-
scope for this chunk per the chunk doc; their primary coverage lives
in SDD-23 / SDD-11.

---

## 3. Test-command quickstart

```bash
# from repo root, on branch 021-supervise-refill-refresh

# 1) Race-clean test pass over the three new files
go test -race ./internal/supervise/ -run "Refill|Refresh|Grace"

# 2) Coverage check — must be ≥95% on the new files
go test -cover ./internal/supervise/ -run "Refill|Refresh|Grace"

# 3) Race + cover combined (the gate the implementation must pass)
go test -race -cover ./internal/supervise/ -run "Refill|Refresh|Grace"

# 4) Full lint pass
magex lint

# 5) Format
magex format:fix

# 6) Full repo race-test (final gate, before commit)
magex test:race
```

A successful run prints `ok ... coverage: 95.X%` for the supervise
package or higher.

---

## 4. Mandatory test list (per /speckit-tasks Phase 4)

These tests MUST exist and MUST pass. Each maps to at least one FR.

**Refill (`refill_test.go`):**

```text
TestRefill_SilentOnCleanExit                 — FR-021-1, FR-021-2 (Story 1)
TestRefill_401UnknownJTITransitions          — FR-021-3 (Story 1 Scenario 2)
TestRefill_NetworkErrorIsRetryable           — FR-021-4 (Story 1 Scenario 3)
TestRefill_AtomicDestructionOnPartialFailure — FR-021-5 (Edge case "Cached JWT mid-rotation")
TestRefill_NeverStringifiesDecryptedBytes    — FR-021-15 / SC-021-8 (Constitution X)
TestRefill_BootRetryNeverPromptsDiscord      — FR-021-19 (smoke; full coverage at SDD-23)
TestRefill_AuditEventsDistinctByOutcome      — FR-021-6
```

**Refresh (`refresh_test.go`):**

```text
TestRefresh_FiresInWindow                    — FR-021-7 (Story 2 Scenario 1)
TestRefresh_T30MinFallback                   — FR-021-8 (Story 2 Scenario 3)
TestRefresh_NoDoubleFireSameWindow           — FR-021-10 (Story 2 Scenario 4)
TestRefresh_FiresOnStartIfInsideWindow       — FR-021-10 second sentence (Story 2 Scenario 2)
TestRefresh_StopsOnCtxCancel                 — FR-021-9 / SC-021-9 (race-clean)
TestRefresh_RateLimitedTreatedAsIssued       — FR-021-11a (Clarification 4)
TestRefresh_BackwardsClockNoDoubleFire       — FR-021-11 (Edge case "Clock changes")
TestRefresh_WindowCrossesMidnight            — Edge case "Refresh window crosses midnight"
TestRefresh_RunIsSingleShot                  — defensive (RF-7)
```

**Grace (`grace_test.go`):**

```text
TestGrace_UsesCacheOnExpiredJWT              — FR-021-12 (Story 3 Scenario 1)
TestGrace_TTLCapAt4h                         — FR-021-12 / SC-021-5 (Story 3 Scenario 4)
TestGrace_DisabledWhenConfigFalse            — FR-021-14 (Story 3 Scenario 3)
TestGrace_ZeroWindowEqualsDisabled           — Edge case "Grace TTL configured as 0"
TestGrace_LazyEvictsOnGetAfterTTL            — FR-021-13 (Story 3 Scenario 2; replaces SDD-21's Sweeper test name per R-008)
TestGrace_EvictDestroysAndRemoves            — FR-021-16 / Clarification 5
TestGrace_EvictOnAbsentNameIsNoop            — FR-021-16 second sentence
TestGrace_SetOverwriteDestroysPrior          — FR-021-13
TestGrace_NeverRendersValueAsString          — FR-021-15 / SC-021-8 (Constitution X)
TestGrace_ConcurrentRaceClean                — SC-021-9 (race-clean)
```

---

## 5. How a reviewer validates the chunk

1. **Read [spec.md](./spec.md) §Clarifications first.** If any of
   the five resolved clarifications has been weakened in the
   implementation, that's a regression.

2. **Read [contracts/observable-behaviors.md](./contracts/observable-behaviors.md).**
   Each B-RR-/ B-RF-/ B-GR- entry must have a passing test.

3. **Run the gate commands** in §3 above. CI must agree.

4. **Diff** [contracts/api.go](./contracts/api.go) against
   [internal/supervise/refill.go](../../internal/supervise/refill.go),
   [refresh.go](../../internal/supervise/refresh.go),
   [grace.go](../../internal/supervise/grace.go). Exported types,
   constructors, methods, sentinel errors must match exactly.

5. **Run the marker-byte never-stringify test** in isolation:

   ```bash
   go test -race ./internal/supervise/ -run "TestRefill_NeverStringifiesDecryptedBytes|TestGrace_NeverRendersValueAsString" -v
   ```

   The test plants `b"HUSH-MARKER-21-PLAINTEXT"` in the decrypted
   payload, captures all logger output into a buffer, and asserts
   the buffer never contains the marker substring. A regression
   here is a Constitution X violation and BLOCKS merge.

6. **Confirm goroutine baseline.** `TestRefresh_StopsOnCtxCancel`
   asserts `runtime.NumGoroutine()` returns to baseline within
   100ms of ctx cancellation; this catches the only goroutine type
   the chunk owns (the Refresher tick loop).

---

## 6. Boot-retry verification (SDD-23 cross-link)

This chunk does NOT implement boot retry — it provides the building
block. The smoke tests assert:

- `Refiller.Refill(ctx, ...)` never internally retries. Two consecutive
  calls with a stub HTTP client returning 5xx both fail individually.
- Calling `Refill` from a caller-managed exp-backoff loop is the
  intended boot-retry pattern (verified in SDD-23 Phase-5 tests).
- `ErrBootTimeout` is exported and identifiable via `errors.Is`.

---

## 7. Common-mistakes checklist

Reviewers should reject the chunk if any of these is true:

- [ ] Any `string(decryptedBytes)` site exists in `refill.go` /
      `refresh.go` / `grace.go`.
- [ ] `NewGrace` spawns a goroutine.
- [ ] `Refresher.Run` propagates the `refill` callback's error to
      its caller.
- [ ] `Grace.Set` destroys the value when disabled (instead of
      silently returning).
- [ ] `Refill` retries on transient failure.
- [ ] `Refill` returns `ErrJTIUnknown` for any 4xx other than
      `401 + {"error":"unknown_jti"}`.
- [ ] Any new dependency in `go.mod`.
- [ ] `init()` function in any of the three files.
- [ ] Package-level mutable state.
- [ ] A `RunSweeper`-style exported method on `Grace` (R-008 final).
- [ ] Adding `ErrTransient` or any sentinel beyond the two listed
      in [contracts/api.go](./contracts/api.go) (R-006 final).

---

## 8. Where to go next

After /speckit-plan finishes:

1. Run `/speckit-tasks` to generate `tasks.md` (the task list with
   TDD-mandatory ordering).
2. Run `/speckit-implement` to execute the tasks.
3. Run the gate commands in §3.
4. Update [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md)
   `internal/supervise/` entry with the locked SDD-21 API block (per
   the chunk doc's Implement-phase post-step list).
5. Update [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-10 row
   with the new test paths.
6. Mark SDD-21 done in [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md).
