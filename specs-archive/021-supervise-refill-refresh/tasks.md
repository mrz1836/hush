# Tasks: Supervisor Refill, Refresh, and Grace Cache (SDD-21)

**Input**: Design documents from `/specs/021-supervise-refill-refresh/`
**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/api.go](./contracts/api.go), [contracts/observable-behaviors.md](./contracts/observable-behaviors.md), [quickstart.md](./quickstart.md), [docs/sdd/SDD-21.md](../../docs/sdd/SDD-21.md)

**Tests**: TDD-mandatory per Constitution VIII. Every behaviour contract has a test-writing task BEFORE its implementation task. Coverage target: ≥95% on the three new files (SC-021-10), race-clean (`go test -race`).

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story. The chunk extends `package supervise`; no new directories are created.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1, US2, US3, US4, US5)
- All file paths are absolute from repo root `/Users/mrz/projects/hush/`

## Path Conventions

- All implementation files live in [internal/supervise/](../../internal/supervise/) alongside existing SDD-18/19/20 files (`config/`, `state.go`, `child.go`)
- Three new production files: `refill.go`, `refresh.go`, `grace.go`
- Three new test files: `refill_test.go`, `refresh_test.go`, `grace_test.go`
- One new test-helper file: `helpers_test.go`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Verify branch + workspace prerequisites. The chunk extends an existing package; no scaffolding work required.

- [X] T001 Verify current branch is `021-supervise-refill-refresh` and the working tree is clean: run `git status` and `git rev-parse --abbrev-ref HEAD` from repo root; if not on the feature branch, abort and resolve before proceeding.
- [X] T002 Confirm baseline gates pass on `main` parity before adding new files: run `magex lint && magex test:race` from repo root; baseline must be green so any failure introduced by this chunk is attributable.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Declare the locked exported types as compile-only stubs plus the two sentinel errors so that every story's tests can compile in parallel BEFORE any behaviour is implemented. Also lay down the shared test-helper file consumed by all three story phases.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete. Without these stubs, the test files for any story will fail to compile and TDD ordering breaks.

- [X] T003 Declare sentinel errors in [internal/supervise/refill.go](../../internal/supervise/refill.go) — `var ErrJTIUnknown = errors.New("supervise: vault rejected JWT (unknown jti)")` and `var ErrBootTimeout = errors.New("supervise: boot retry timeout exhausted")` per [data-model.md § 4](./data-model.md#4-sentinel-errors); both exported, both package-level read-only sentinels (Constitution IX exemption).
- [X] T004 [P] Declare `Refiller` struct + `NewRefiller(client *http.Client, store *Store, logger *slog.Logger) *Refiller` + `(*Refiller) Refill(ctx context.Context, scopes []string) error` + package-private `(*Refiller) attach(grace *Grace, priv *ecdsa.PrivateKey, serverURL string)` as compile-only stubs in [internal/supervise/refill.go](../../internal/supervise/refill.go) per [contracts/api.go:47-88](./contracts/api.go) and [data-model.md § 1](./data-model.md#1-refiller). Methods return zero values; godoc copied verbatim from [contracts/api.go](./contracts/api.go).
- [X] T005 [P] Declare `Refresher` struct + `NewRefresher(window string, ttl time.Duration, refill func(context.Context) error, logger *slog.Logger) *Refresher` + `(*Refresher) Run(ctx context.Context) error` + package-private `(*Refresher) setClockForTest(now func() time.Time)` as compile-only stubs in [internal/supervise/refresh.go](../../internal/supervise/refresh.go) per [contracts/api.go:96-140](./contracts/api.go) and [data-model.md § 2](./data-model.md#2-refresher). Methods return zero values; godoc copied verbatim.
- [X] T006 [P] Declare `Grace` struct + `graceEntry` (unexported) + `NewGrace(window time.Duration, enabled bool) *Grace` + `(*Grace) Get(name string) (*securebytes.SecureBytes, bool)` + `(*Grace) Set(name string, value *securebytes.SecureBytes)` + `(*Grace) Evict(name string)` + package-private `(*Grace) setClockForTest(now func() time.Time)` as compile-only stubs in [internal/supervise/grace.go](../../internal/supervise/grace.go) per [contracts/api.go:150-212](./contracts/api.go) and [data-model.md § 3](./data-model.md#3-grace). Methods return zero values; godoc copied verbatim.
- [X] T007 Create shared test fixtures in [internal/supervise/helpers_test.go](../../internal/supervise/helpers_test.go) per [data-model.md § 6](./data-model.md#6-test-only-seams-package-private): `fakeClock` (advanceable injectable clock with `Now()`/`Advance(d)`), `roundTripFunc` type implementing `http.RoundTripper` from a `func(*http.Request) (*http.Response, error)`, `recordingHandler` (`*slog.Logger` writing to a `bytes.Buffer`), and `newTestRefiller`/`newTestRefresher`/`newTestGrace` constructors that wire injected seams via the package-private `attach`/`setClockForTest` setters. All helpers are unexported and `_test.go`-scoped.
- [X] T008 Verify the package compiles and the existing supervise tests still pass with the new stubs in place: `go build ./internal/supervise/ && go test -count=1 ./internal/supervise/ -run "TestStore|TestChild"` from repo root. The new stubs MUST NOT break SDD-18/19/20 tests.

**Checkpoint**: Stubs compile, sentinels are exported, helpers are available. Each user story can now be implemented in parallel by writing tests against the stubs (which will FAIL), then filling in behaviour.

---

## Phase 3: User Story 1 — Silent refill on clean child exit (Priority: P1) 🎯 MVP

**Goal**: Implement `Refiller.Refill` so that a clean child exit while the supervisor's session is valid produces zero Discord prompts and a fresh secret set delivered to the next child invocation. The hot path of the supervisor model — without this, every restart becomes a re-approval and the operator gets trained to auto-approve.

**Independent Test**: With a stub vault server (`roundTripFunc`) returning HTTP 200 + valid ECIES envelopes and a stub `Grace`, `Refill(ctx, scopes)` returns `nil`, calls `Grace.Set` once per scope, never converts decrypted bytes to a Go string, and never invokes any Discord-bound dependency. Verifiable as a unit test against `package supervise` without any real network or Discord transport.

**Maps to**: FR-021-1 .. FR-021-6, FR-021-15, Lifecycle Scenarios 3 + 7, AC-10, [B-RR-1..B-RR-7](./contracts/observable-behaviors.md#refiller--observable-behaviors).

### Tests for User Story 1 (TDD-mandatory — write FIRST, ensure they FAIL before implementation)

> All tests live in [internal/supervise/refill_test.go](../../internal/supervise/refill_test.go); table-driven style per [`.github/tech-conventions/testing-standards.md`](../../.github/tech-conventions/testing-standards.md). Each test sub-test name follows `TestFunctionName_Scenario`. Tests use the shared fixtures from [helpers_test.go](../../internal/supervise/helpers_test.go) (T007).

- [X] T009 [P] [US1] Write `TestRefill_SilentOnCleanExit` in [internal/supervise/refill_test.go](../../internal/supervise/refill_test.go): two scopes, stub HTTP 200 + valid ECIES envelopes, assert `Refill` returns `nil`, asserts one `Grace.Set` per scope, asserts zero Approver-equivalent calls (FR-021-1, FR-021-2, B-RR-1, Lifecycle Scenario 3). MUST fail before T016.
- [X] T010 [P] [US1] Write `TestRefill_401UnknownJTITransitions`: stub HTTP 401 with body `{"error":"unknown_jti"}` for one scope, assert returned error satisfies `errors.Is(err, ErrJTIUnknown)`, assert refill stops at the failing scope, assert no `Grace.Set` calls (FR-021-3, B-RR-2, Lifecycle Scenario 7, Story 1 Scenario 2). MUST fail before T016.
- [X] T011 [P] [US1] Write `TestRefill_NetworkErrorIsRetryable`: stub `roundTripFunc` returning a `*net.OpError`, assert `errors.Is(err, ErrJTIUnknown) == false`, assert `errors.As(err, &netErr)` succeeds where `netErr` is `*net.OpError` or wraps the underlying transport error per [data-model.md § Error mapping](./data-model.md#error-mapping-locked-fr-021-3--fr-021-4) (FR-021-4, B-RR-3, Story 1 Scenario 3). MUST fail before T016.
- [X] T012 [P] [US1] Write `TestRefill_AtomicDestructionOnPartialFailure`: three scopes, scopes 1+2 succeed (200), scope 3 fails (any reason); after `Refill` returns the error, assert `sb1.Use(...)` and `sb2.Use(...)` both return `ErrDestroyed`; assert no `Grace.Set` call for any of the three scopes (FR-021-5, B-RR-4, Edge case "Cached JWT mid-rotation"). MUST fail before T016.
- [X] T013 [P] [US1] Write `TestRefill_NeverStringifiesDecryptedBytes`: ECIES envelope whose plaintext is the marker `[]byte("HUSH-MARKER-21-PLAINTEXT")`; capture the operational logger output into a `bytes.Buffer`; assert the buffer never contains the marker substring; assert `Grace.Set("S1", sb)` is called and `sb.Use(func(b []byte) {})` reveals the marker bytes intact (FR-021-15, SC-021-8, B-RR-6, Constitution X). MUST fail before T016.
- [X] T014 [P] [US1] Write `TestRefill_AuditEventsDistinctByOutcome`: three table rows (success / `ErrJTIUnknown` / transient error), each asserts the operational logger emits a structured event with a distinguishable `outcome` attribute and zero secret bytes in any attribute (FR-021-6). MUST fail before T016.
- [X] T015 [P] [US1] Write `TestRefill_BearerTokenNeverLeaksToLogs`: prime `Store.Token` with a marker JWT `[]byte("HUSH-MARKER-JWT-CAFEBABE")`; capture logger output; assert the bearer-marker bytes never appear in the buffer; assert `SecureBytes.LogValue()` returns `"[redacted]"` (B-RR-5, Constitution X). MUST fail before T016.

### Implementation for User Story 1

- [X] T016 [US1] Implement `Refiller.Refill` end-to-end in [internal/supervise/refill.go](../../internal/supervise/refill.go) per [data-model.md § 1](./data-model.md#1-refiller) and [research.md R-005, R-007](./research.md#r-005--refill-http-layer): per-scope `http.NewRequestWithContext` GET to `r.server + "/s/" + name`; bearer header set inside `snap.Token.Use(func(b []byte) { req.Header.Set("Authorization", "Bearer "+string(b)) })` closure (the SOLE permitted `string(...)` site, scoped to JWT — never to vault payload); `io.LimitReader(resp.Body, 64*1024)` cap; HTTP-status mapping per [data-model.md § Error mapping](./data-model.md#error-mapping-locked-fr-021-3--fr-021-4) including the 401-unparseable-body → transient default; ECIES decrypt via existing `internal/transport/ecies.Decrypt` → `*SecureBytes`; ciphertext bytes zeroed post-decrypt (`for i := range raw { raw[i] = 0 }`); `committed bool` defer pattern destroying `decrypted []*SecureBytes` on any error path; on success iterate `decrypted` and call `r.grace.Set(name, sb)` then set `committed = true` (FR-021-1..FR-021-6, RR-1..RR-7).
- [X] T017 [US1] Implement `(*Refiller).attach(grace *Grace, priv *ecdsa.PrivateKey, serverURL string)` in [internal/supervise/refill.go](../../internal/supervise/refill.go) — package-private setter that wires the post-construction dependencies the locked `NewRefiller` signature cannot accept; mirror SDD-19's `setTokenForTest` precedent ([plan.md Complexity Tracking row 2](./plan.md#complexity-tracking)).
- [X] T018 [US1] Run `go test -race -run "TestRefill_" ./internal/supervise/` from repo root; confirm all seven Story-1 tests pass and `-race` is clean. If `TestRefill_NeverStringifiesDecryptedBytes` fails, audit `refill.go` for any `string(...)` of a non-JWT byte slice — Constitution X violation BLOCKS merge.

**Checkpoint**: User Story 1 fully functional. Refill produces fresh secrets via Grace.Set on success, `ErrJTIUnknown` on JTI-rejection, wrapped underlying error otherwise; atomic destruction of partial decrypts on any error; no string-materialization of decrypted vault payload anywhere.

---

## Phase 4: User Story 2 — Refresh window fires at the configured time (Priority: P1)

**Goal**: Implement `Refresher.Run` so that a Discord refresh prompt arrives inside the operator-configured local-time window with at most one fire per (window, calendar-day) pair, plus an at-most-one T-30 fallback per session when today's window has already passed and the session is within 30 minutes of expiry. The core human-factors guarantee of the supervisor model: pages happen during waking hours.

**Independent Test**: With an injected `fakeClock` and a stub `refill` callback, the scheduler can be advanced through a synthetic day; the callback is observed exactly once inside the configured window for the normal path and exactly once at the T-30 boundary for the fallback path. No real Discord transport, no real time elapse.

**Maps to**: FR-021-7 .. FR-021-11a, Lifecycle Scenario 8, AC-10, [B-RF-1..B-RF-9](./contracts/observable-behaviors.md#refresher--observable-behaviors).

### Tests for User Story 2 (TDD-mandatory — write FIRST, ensure they FAIL before implementation)

> All tests live in [internal/supervise/refresh_test.go](../../internal/supervise/refresh_test.go); table-driven; clock injected via `(*Refresher).setClockForTest(fakeClock.Now)` after construction. Tests use a `chan struct{}` from a wrapped `refill` callback to deterministically wait for fires under `-race`.

- [X] T019 [P] [US2] Write `TestRefresh_FiresInWindow` in [internal/supervise/refresh_test.go](../../internal/supervise/refresh_test.go): `window = "09:00-10:00"`, fakeClock starts 08:55, advance to 09:05; assert `refill` callback invoked exactly once; advance to 09:30, 09:55 → callback NOT invoked again; advance to next day 09:05 → invoked once more (FR-021-7, B-RF-1, Story 2 Scenario 1). MUST fail before T028.
- [X] T020 [P] [US2] Write `TestRefresh_T30MinFallback`: `window = "09:00-10:00"`, fakeClock starts 11:00 (window passed today), `bornAt + ttl = 11:25` (25 min remaining < 30 min); on first tick assert `refill` invoked exactly once; subsequent ticks within 30 min do NOT re-invoke; assert `lastFiredDay = today` and `t30Fired = true` (FR-021-8, B-RF-2, Story 2 Scenario 3). MUST fail before T028.
- [X] T021 [P] [US2] Write `TestRefresh_StopsOnCtxCancel`: start `Run(ctx)` in a goroutine, capture `runtime.NumGoroutine()` baseline before/after, cancel ctx, assert `Run` returns `ctx.Err()` within 100ms, assert goroutine count returns to baseline within 100ms; run under `-race` and assert clean (FR-021-9, SC-021-9, B-RF-5, RF-3). MUST fail before T028.
- [X] T022 [P] [US2] Write `TestRefresh_NoDoubleFireSameWindow`: prime `lastFiredDay = today`, fakeClock at 09:30 (in-window); advance ticks; assert `refill` NOT invoked; `lastFiredDay` unchanged (FR-021-10, B-RF-3, Story 2 Scenario 4). MUST fail before T028.
- [X] T023 [P] [US2] Write `TestRefresh_FiresOnStartIfInsideWindow`: fresh `*Refresher` (zero-valued `lastFiredDay`), fakeClock at 09:30 (already in-window for `09:00-10:00`); start `Run(ctx)`; assert `refill` invoked exactly once on the first tick (well before 10:00); assert `lastFiredDay = today` (FR-021-10 second sentence, B-RF-4, Story 2 Scenario 2, Clarification 1). MUST fail before T028.
- [X] T024 [P] [US2] Write `TestRefresh_RateLimitedTreatedAsIssued`: `refill` callback returns a non-nil error (e.g. a stand-in `errors.New("rate-limited")`); on tick, assert callback invoked exactly once; assert logger emits one WARN line naming the error class; assert subsequent in-window ticks do NOT re-invoke; assert `Run` does NOT propagate the error (FR-021-11a, B-RF-7, Clarification 4, RF-6). MUST fail before T028.
- [X] T025 [P] [US2] Write `TestRefresh_BackwardsClockNoDoubleFire`: prime `lastFiredDay = today` after a 09:30 fire; step fakeClock back to 09:15; advance ticks; assert `refill` NOT invoked; assert `lastFiredDay` already set blocks the re-fire path (FR-021-11, B-RF-6, Edge case "Clock changes"). MUST fail before T028.
- [X] T026 [P] [US2] Write `TestRefresh_WindowCrossesMidnight`: `window = "23:00-01:00"`, fakeClock at 23:30; assert tick fires (in-window predicate treats the interval as contiguous); `lastFiredDay` set to today's date; subsequent tick at 00:30 does NOT re-fire (Edge case "Refresh window crosses midnight", B-RF-9). MUST fail before T028.
- [X] T027 [P] [US2] Write `TestRefresh_RunIsSingleShot`: call `Run(ctx1)` and let it return on cancel; call `Run(ctx2)` on the same `*Refresher`; assert second call returns sentinel error immediately, no goroutine spawned, no callback invoked (RF-7, B-RF-8). MUST fail before T028.

### Implementation for User Story 2

- [X] T028 [US2] Implement `Refresher.Run` end-to-end in [internal/supervise/refresh.go](../../internal/supervise/refresh.go) per [data-model.md § 2 Tick algorithm](./data-model.md#tick-algorithm-r-002) and [research.md R-002, R-003, R-004](./research.md#r-002--window-crossing-semantics--idempotency-flag): `sync.Once`-guarded entry; window string parsed eagerly into four ints `(startHour, startMin, endHour, endMin)` (panic on parse failure per [data-model.md Validation](./data-model.md#constructor-1)); set `r.bornAt = r.now()` on entry; `time.Timer` re-arm loop with `select { case <-ctx.Done(): return ctx.Err(); case <-timer.C: continue }`; per-tick wall-clock predicate `windowContains(now, start, end)` honouring the midnight-crossing case; `lastFiredDay` calendar-date key for FR-021-10/RF-1/RF-4; `t30Fired bool` for RF-2; first-tick on-init fire if `lastFiredDay != today && inWindow`; `fire()` calls `r.refill(ctx)` INLINE (zero sub-goroutines, R-014/RF-8); on non-nil error log WARN naming error class and advance `lastFiredDay` regardless (RF-6); top-frame `defer func() { _ = recover() }()` for crash safety (Constitution IX). Update godoc on `Run` to document the FR-021-11a "non-nil error counts as issued" contract.
- [X] T029 [US2] Implement `NewRefresher` validation: panic on `refill == nil`, `logger == nil`, or window-string parse failure (Constitution IX startup-wiring exemption); store the four parsed ints in unexported fields; default `now = time.Now`.
- [X] T030 [US2] Run `go test -race -run "TestRefresh_" ./internal/supervise/` from repo root; confirm all nine Story-2 tests pass and `-race` is clean. The `TestRefresh_StopsOnCtxCancel` goroutine-baseline assertion catches any leaked Refresher tick loop — failure here is a Constitution IX violation.

**Checkpoint**: User Story 2 fully functional. Exactly one fire per (window, calendar-day); T-30 fallback at most once per session; ctx cancellation is the sole exit path; rate-limited fires count as issued; race-clean tick loop with zero sub-goroutines.

---

## Phase 5: User Story 3 — Overnight crash absorbed by grace cache (Priority: P1)

**Goal**: Implement `Grace` so that an opt-in cache holds last-decrypted secrets in `*SecureBytes` per name, with effective TTL `min(window, 4h)`, lazy-evict on `Get`, silent no-op when disabled, and an explicit `Evict` primitive for orchestrator-driven invalidation. The headline availability/secrecy tradeoff documented in `SECURITY.md §6`.

**Independent Test**: With an injected `fakeClock`, the cache can be primed, advanced past TTL, and observed to evict + destroy; with `enabled=false`, no entry is ever stored and the incoming sb is NOT destroyed (caller retains ownership); with a configured-too-large TTL, the value is clamped to 4 hours; `Evict("name")` destroys + removes; `Evict` of an absent name is a silent no-op.

**Maps to**: FR-021-12 .. FR-021-17, Lifecycle Scenario 9, AC-10, [B-GR-1..B-GR-10](./contracts/observable-behaviors.md#grace--observable-behaviors).

### Tests for User Story 3 (TDD-mandatory — write FIRST, ensure they FAIL before implementation)

> All tests live in [internal/supervise/grace_test.go](../../internal/supervise/grace_test.go); table-driven; clock injected via `(*Grace).setClockForTest(fakeClock.Now)`. Real `*securebytes.SecureBytes` instances used (existing SDD-02 type).

- [X] T031 [P] [US3] Write `TestGrace_UsesCacheOnExpiredJWT` in [internal/supervise/grace_test.go](../../internal/supervise/grace_test.go): `NewGrace(60*time.Minute, true)`, fakeClock at T0, `Set("API_KEY", sb)`, advance fakeClock to T0+30min; assert `Get("API_KEY")` returns `(sb, true)` and `sb.Use` returns the expected bytes (FR-021-12, B-GR-1, Story 3 Scenario 1, Lifecycle Scenario 9). MUST fail before T041.
- [X] T032 [P] [US3] Write `TestGrace_TTLCapAt4h`: `NewGrace(8*time.Hour, true)`, `Set("X", sb)` at T0; advance fakeClock to T0+4h+1ns; assert `Get("X")` returns `(nil, false)` and `sb.Use(...)` returns `ErrDestroyed` — effective TTL was exactly 4h (FR-021-12, GR-1, B-GR-5, SC-021-5, Story 3 Scenario 4). MUST fail before T041.
- [X] T033 [P] [US3] Write `TestGrace_DisabledWhenConfigFalse`: `NewGrace(60*time.Minute, false)`; `Set("X", sb)` then `Get("X")`; assert `Get` returns `(nil, false)`; assert `sb.Use(...)` STILL returns the expected bytes (sb NOT destroyed by Set — caller retains ownership per R-009, FR-021-14, B-GR-3, Story 3 Scenario 3). MUST fail before T041.
- [X] T034 [P] [US3] Write `TestGrace_ZeroWindowEqualsDisabled`: `NewGrace(0, true)`; `Set("X", sb)` then `Get("X")`; assert `Get` returns `(nil, false)`; assert sb NOT destroyed (Edge case "Grace TTL configured as 0", B-GR-4, FR-021-14). MUST fail before T041.
- [X] T035 [P] [US3] Write `TestGrace_LazyEvictsOnGetAfterTTL` (replaces SDD-21's `TestGrace_SweeperDestroysExpired` per R-008 final / [plan.md Complexity Tracking row 3](./plan.md#complexity-tracking) — same FR-021-13 destruction semantics, lazy-evict trigger): `Set("X", sb)` at T0; advance fakeClock to T0+window+1ns; call `Get("X")`; assert returns `(nil, false)`; assert `sb.Use(...)` returns `ErrDestroyed`; assert internal map size 0 after the Get (FR-021-13, GR-4, B-GR-2, Story 3 Scenario 2). MUST fail before T041.
- [X] T036 [P] [US3] Write `TestGrace_EvictDestroysAndRemoves`: `Set("X", sb)`; `Evict("X")`; assert `sb.Use(...)` returns `ErrDestroyed`; assert `Get("X")` returns `(nil, false)`; assert internal map size 0 (FR-021-16, B-GR-7, Clarification 5). MUST fail before T041.
- [X] T037 [P] [US3] Write `TestGrace_EvictOnAbsentNameIsNoop`: empty cache; `Evict("nonexistent")` — assert no panic, no error path, cache state unchanged (FR-021-16 second sentence, B-GR-8, GR-5, Clarification 5). MUST fail before T041.
- [X] T038 [P] [US3] Write `TestGrace_SetOverwriteDestroysPrior`: `Set("X", sb1)`; assert `sb1` is alive (`sb1.Use` returns expected bytes); `Set("X", sb2)`; assert `sb1.Use(...)` returns `ErrDestroyed`; assert `Get("X")` returns `(sb2, true)` (FR-021-13, GR-3, B-GR-6). MUST fail before T041.
- [X] T039 [P] [US3] Write `TestGrace_NeverRendersValueAsString`: `Set("X", sb)` where `sb` wraps marker bytes `[]byte("HUSH-MARKER-21-CACHED")`; capture any stray slog output via `recordingHandler`; render `Grace` directly via `slog.Info("dump", "grace", g)`; assert output buffer does NOT contain marker bytes; verify `*SecureBytes.LogValue()` returns `"[redacted]"` (FR-021-15, SC-021-8, B-GR-9, Constitution X). MUST fail before T041.
- [X] T040 [P] [US3] Write `TestGrace_ConcurrentRaceClean`: spawn N=100 goroutines, each performing a random interleave of `Set`/`Get`/`Evict` against the same key with random small sleeps; assert no race detected under `-race`; assert no double-Destroy panic; assert final state is consistent (entry either present-and-alive, or absent-and-destroyed) (SC-021-9, GR-8, B-GR-10). MUST fail before T041.

### Implementation for User Story 3

- [X] T041 [US3] Implement `NewGrace`, `Get`, `Set`, `Evict` end-to-end in [internal/supervise/grace.go](../../internal/supervise/grace.go) per [data-model.md § 3](./data-model.md#3-grace) and [research.md R-008, R-009](./research.md#r-008--grace-cache-concurrency--sweeper-goroutine): `NewGrace` applies `window = min(window, 4*time.Hour)` (GR-1, FR-021-12), records `enabled` + `window`, defaults `now = time.Now`, allocates `entries map[string]graceEntry`; `Set` under write lock — silent return when `!g.enabled || g.window == 0` (R-009, FR-021-14, ownership stays with caller — DO NOT destroy `value`); on overwrite Destroy prior `entry.sb` (FR-021-13, GR-3); insert `graceEntry{sb: value, expires: g.now().Add(g.window)}`; `Get` with the lazy-evict pattern from [data-model.md § Get path detail](./data-model.md#get-path-detail--lazy-eviction-r-008) (RLock → check → upgrade to write lock on expiry → re-check → Destroy + delete → return); `Evict` under write lock — silent no-op on absent name (Clarification 5, FR-021-16, GR-5), Destroy + delete on present.
- [X] T042 [US3] Verify `Grace` owns ZERO goroutines: grep [internal/supervise/grace.go](../../internal/supervise/grace.go) for `go func` and `go ` patterns; the only acceptable matches are inside test files. Constitution IX violation if any production goroutine exists in `grace.go`.
- [X] T043 [US3] Run `go test -race -run "TestGrace_" ./internal/supervise/` from repo root; confirm all ten Story-3 tests pass and `-race` is clean. `TestGrace_NeverRendersValueAsString` failure is a Constitution X violation BLOCKING merge.

**Checkpoint**: User Story 3 fully functional. Cache hits before TTL elapse; lazy-evict + destroy on TTL elapse via Get; silent no-op when disabled OR window=0 (caller retains ownership); explicit `Evict` primitive for orchestrator; effective TTL hard-capped at 4h; race-clean concurrent access; zero goroutines owned.

---

## Phase 6: User Story 4 — Boot retry tolerates startup races without paging (Priority: P2)

**Goal**: Smoke-prove the boot-retry building blocks this chunk supplies — the `ErrBootTimeout` sentinel is exported and identifiable, `Refill` never internally retries, and a stub Refill loop produces zero Discord-bound side effects. Full boot-retry implementation lives in SDD-23; this chunk's job is to make sure the surface SDD-23 needs is present and unambiguous.

**Independent Test**: Two smoke tests in `refill_test.go` — one asserts `errors.Is(supervise.ErrBootTimeout, supervise.ErrBootTimeout)` is true (sentinel is exported and stable); one asserts that a `Refiller` whose stub HTTP client always returns 5xx fails on each individual `Refill` call without internally retrying and without invoking any Discord-bound dependency.

**Maps to**: FR-021-19 (smoke), FR-021-20 (sentinel only), Lifecycle Scenario 11, [B-SE-2](./contracts/observable-behaviors.md#b-se-2--errboottimeout-is-exported-but-never-produced-here-r-010-fr-021-20), [research.md R-010](./research.md#r-010--boot-retry--out-of-scope-for-this-chunk).

### Tests for User Story 4 (TDD-mandatory — write FIRST, ensure they FAIL before implementation)

> Two smoke tests appended to [internal/supervise/refill_test.go](../../internal/supervise/refill_test.go). These are intentionally minimal — full boot-retry coverage lives in SDD-23 ([quickstart.md § 6](./quickstart.md#6-boot-retry-verification-sdd-23-cross-link)).

- [X] T044 [P] [US4] Write `TestBootRetry_BackoffRespected` in [internal/supervise/refill_test.go](../../internal/supervise/refill_test.go): construct a `Refiller` with a stub `roundTripFunc` returning HTTP 503; call `Refill` twice in succession with a 50ms gap; assert each call returns a non-nil non-`ErrJTIUnknown` error individually; assert `Refill` does NOT internally retry (each call results in exactly one HTTP request, observable via the roundTripFunc invocation count); document via comment that this is a smoke test and full backoff coverage is in SDD-23 (FR-021-19 smoke, R-010). MUST fail before T046.
- [X] T045 [P] [US4] Write `TestBootRetry_NeverPromptsDiscord`: construct a `Refiller` with a stub HTTP client that always 5xx; call `Refill` five times; assert no Approver-equivalent dependency was ever invoked (since this chunk has no Approver dep, assert via "the only logger calls are WARN-class refill error lines and the only HTTP calls are GETs to `/s/<name>`"); assert `errors.Is(supervise.ErrBootTimeout, supervise.ErrBootTimeout) == true` (sentinel-stability assertion per B-SE-2) (FR-021-19 smoke, R-010, B-SE-2). MUST fail before T046.

### Implementation for User Story 4

- [X] T046 [US4] Verify `Refiller.Refill` already satisfies the boot-retry smoke contract: `Refill` does NOT contain any retry loop, does NOT have any backoff sleep, returns the first error encountered. Re-read [internal/supervise/refill.go](../../internal/supervise/refill.go) end to end and confirm zero internal retry sites; if any retry exists, REMOVE it (chunk doc anti-contract: "NEVER retry inside `Refill`"). The sentinel `ErrBootTimeout` was declared in T003; no production code in this chunk produces it (R-010, FR-021-20 — production lives in SDD-23).
- [X] T047 [US4] Run `go test -race -run "TestBootRetry_" ./internal/supervise/` from repo root; confirm both smoke tests pass and `-race` is clean.

**Checkpoint**: User Story 4 building blocks verified. `ErrBootTimeout` is exported, stable, identifiable via `errors.Is`. `Refill` never internally retries, never invokes Discord-bound dependencies. SDD-23 can wire its boot-retry helper around `Refill` with caller-managed exp-backoff using `errors.Is(err, ErrJTIUnknown)` to distinguish JTI-rejection (exit retry, transition state) from transient (continue backoff).

---

## Phase 7: User Story 5 — DM rate limiter prevents prompt floods (Priority: P2)

**Goal**: Honour the FR-021-11a "rate-limited refresh fire counts as issued" contract that the DM rate limiter implies, without adding a `RateLimiter` type to this chunk's exported API. The rate limiter itself lives in SDD-11 (BotApprover); this chunk's responsibility is to surface `ErrRateLimited` from the `refill` callback as a WARN log in `Refresher` and never as a state transition or retry.

**Independent Test**: Already covered by `TestRefresh_RateLimitedTreatedAsIssued` (T024) in Story 2 — the test asserts that when the `refill` callback returns a non-nil error, the Refresher logs a WARN, advances `lastFiredDay`, never retries within the window, and never propagates the error to its caller. This phase confirms the test exists, passes under the rate-limit interpretation, and that no additional rate-limiter code lives in this chunk (R-011).

**Maps to**: FR-021-23 .. FR-021-27 (consumed via SDD-11; this chunk only consumes), [research.md R-011](./research.md#r-011--dm-rate-limiter--pass-through-not-implemented).

### Verification for User Story 5

- [X] T048 [US5] Confirm `TestRefresh_RateLimitedTreatedAsIssued` (T024) was written and passes: it directly satisfies the "rate-limited fire counted as issued" semantics required by Story 5 / FR-021-11a. Re-run `go test -race -run "TestRefresh_RateLimitedTreatedAsIssued" ./internal/supervise/` and confirm pass.
- [X] T049 [US5] Audit [internal/supervise/refill.go](../../internal/supervise/refill.go), [refresh.go](../../internal/supervise/refresh.go), [grace.go](../../internal/supervise/grace.go) and confirm NO `RateLimiter` type is declared, NO token-bucket logic exists in this chunk, NO `ErrRateLimited` sentinel is added — the rate limiter implementation lives in SDD-11 (BotApprover) per [research.md R-011](./research.md#r-011--dm-rate-limiter--pass-through-not-implemented). The chunk's role is purely consumer.

**Checkpoint**: User Story 5 satisfied via Story 2 wiring. Rate-limited refresh fires drop with a WARN log via the existing `Refresher` non-nil-error path. No new code in this chunk for the rate limiter itself.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Final-quality gates per the SDD-21 chunk-doc post-step list ([docs/sdd/SDD-21.md § Prompt 5](../../docs/sdd/SDD-21.md)). All tasks below are MANDATORY before the chunk is considered done. The `magex format:fix && magex lint && magex test:race` triple is the gate the user explicitly required.

- [X] T050 Run `magex format:fix` from repo root; confirm zero diff on the three new production files plus the test files (formatter is idempotent on already-formatted code).
- [X] T051 Run `magex lint` from repo root; confirm zero lint violations across the chunk. Common pitfalls: `gochecknoglobals` triggering on a non-sentinel package var (Constitution IX); `gosec` flagging the JWT bearer-header `string(...)` site (this is the SOLE permitted site per [plan.md Constitution X table](./plan.md#principle-x--observability--redaction) — annotate with a `//nolint:gosec // FR-021-15: JWT bearer-header materialization, scoped to Snapshot.Token.Use closure` comment if `gosec` is noisy).
- [X] T052 Run `magex test:race` from repo root; confirm full repo race-clean, all packages pass, no goroutine leak from the supervise package.
- [X] T053 Verify coverage ≥95% on the three new files: `go test -cover ./internal/supervise/ -run "Refill|Refresh|Grace"` from repo root; output line MUST report ≥95.0% coverage. If below threshold, identify uncovered branches (typically: error wrap sites, nil-input panics, midnight-crossing edge in window predicate, lock-upgrade race in Grace.Get) and add targeted tests until SC-021-10 is met.
- [X] T054 Verify Lifecycle Scenarios 3, 7, 8, 9, 11 each have at least one passing test in this chunk per SC-021-1: cross-reference [quickstart.md § 2](./quickstart.md#2-lifecycle-scenario-coverage-map) — Scenario 3 → `TestRefill_SilentOnCleanExit` (T009); Scenario 7 → `TestRefill_401UnknownJTITransitions` (T010); Scenario 8 → `TestRefresh_FiresInWindow` (T019) + `TestRefresh_FiresOnStartIfInsideWindow` (T023); Scenario 9 → `TestGrace_UsesCacheOnExpiredJWT` (T031) + `TestGrace_TTLCapAt4h` (T032); Scenario 11 → `TestBootRetry_BackoffRespected` (T044) + `TestBootRetry_NeverPromptsDiscord` (T045). All five scenarios MUST have at least one green test.
- [X] T055 Run a final marker-byte never-stringify check in isolation: `go test -race -v ./internal/supervise/ -run "TestRefill_NeverStringifiesDecryptedBytes|TestGrace_NeverRendersValueAsString"`. Both tests MUST pass; failure is a Constitution X violation BLOCKING merge per [quickstart.md § 5](./quickstart.md#5-how-a-reviewer-validates-the-chunk).
- [X] T056 Run a final goroutine-baseline assertion in isolation: `go test -race -v ./internal/supervise/ -run "TestRefresh_StopsOnCtxCancel"`. The single goroutine type owned by this chunk (Refresher tick loop) MUST return `runtime.NumGoroutine()` to baseline within 100ms of ctx cancellation.
- [X] T057 Append "Exported API — locked at SDD-21" extension to the `internal/supervise/` entry in [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md), listing the three constructors + four methods + two sentinels + the Clarification-5-added `Evict` method per [data-model.md § Locked vs implemented exported API](./plan.md#locked-vs-implemented-exported-api). Format mirrors the SDD-19 lock block already in PACKAGE-MAP.
- [X] T058 Update [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-10 row with the new test file paths: `internal/supervise/refill_test.go`, `internal/supervise/refresh_test.go`, `internal/supervise/grace_test.go`. Reference [plan.md Constitution Check § Principle VIII](./plan.md#principle-viii--testing-discipline) for the AC-10 → test mapping.
- [X] T059 Mark SDD-21 status `done` in [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md) — find the SDD-21 row in the playbook table and change its status column from `in-progress` (or whatever the current value is) to `done`.
- [X] T060 Audit anti-contracts one final time per [docs/sdd/SDD-21.md § Anti-contracts](../../docs/sdd/SDD-21.md) and CLAUDE.md: confirm NO `string(decryptedBytes)` of a non-JWT byte slice anywhere; NO goroutine in `NewGrace`; NO error propagation from `Refresher.Run` for `refill`-callback errors; NO destruction of value on disabled `Grace.Set`; NO retry inside `Refill`; NO `ErrJTIUnknown` for any 4xx other than 401-with-`unknown_jti`-body; NO `ErrTransient` sentinel added; NO `RunSweeper` exported method on `Grace`; NO `init()`; NO package-level mutable state; NO modifications to SDD-18/19/20 symbols; NO direct audit-log writes; ZERO new direct dependencies in `go.mod`.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — can start immediately.
- **Phase 2 (Foundational)**: Depends on Phase 1. **BLOCKS all user story phases** — without the compile-only stubs (T004/T005/T006), test files for any story will fail to compile and TDD ordering breaks. T003 (sentinels), T004/T005/T006 (stubs), T007 (helpers) can run in parallel after T001/T002 pass; T008 is the gate.
- **Phase 3 (US1 Refill, P1)**: Depends on Phase 2 completion. Can proceed in parallel with Phase 4 and Phase 5 once Phase 2 is done.
- **Phase 4 (US2 Refresh, P1)**: Depends on Phase 2 completion. Independent of Phase 3 and Phase 5 (Refresher invokes a `func(ctx) error` callback — does NOT directly depend on Refiller or Grace types beyond compile).
- **Phase 5 (US3 Grace, P1)**: Depends on Phase 2 completion. Independent of Phase 3 and Phase 4 (Grace has no upstream dependencies in the chunk).
- **Phase 6 (US4 Boot retry smoke, P2)**: Depends on Phase 3 (US1) — the smoke tests are appended to `refill_test.go` and assert behaviour of the implemented `Refill`.
- **Phase 7 (US5 DM rate limit, P2)**: Depends on Phase 4 (US2) — the satisfying test (`TestRefresh_RateLimitedTreatedAsIssued`, T024) lives in `refresh_test.go` and verifies the implemented `Refresher`.
- **Phase 8 (Polish)**: Depends on all User Story phases (3, 4, 5, 6, 7) being complete. The format/lint/race triple plus coverage check is the final gate.

### User Story Dependencies

- **US1 (Refill, P1)**: No dependency on other user stories. Independently testable via stub HTTP + stub Grace (uses real `NewGrace(0, true)` which is a silent no-op cache → Refill's success path still completes). MVP scope candidate.
- **US2 (Refresh, P1)**: No dependency on other user stories. Independently testable via stub `refill` callback closure.
- **US3 (Grace, P1)**: No dependency on other user stories. Independently testable via real `*SecureBytes` instances + injected clock.
- **US4 (Boot retry, P2)**: Depends on US1 (smoke tests assert Refill's no-internal-retry contract).
- **US5 (DM rate limit, P2)**: Depends on US2 (the rate-limit-as-issued semantic is implemented inside Refresher).

### Within Each User Story

- **TDD ordering MANDATORY**: Test-writing tasks (TXXX) MUST be completed BEFORE the corresponding implementation task and MUST be confirmed FAILING before implementation begins (red → green discipline per Constitution VIII).
- Within a phase: all `[P]`-marked test tasks can be authored in parallel (different test functions in the same file — go fmt resolves trailing-newline conflicts automatically; if file-edit conflicts arise, serialize within the phase).
- Implementation task in each phase is a single non-`[P]` task that touches the production file.
- Verification task (final task per phase) confirms green tests + race-clean.

### Parallel Opportunities

- **Phase 2**: T003 (sentinels), T004 (Refiller stubs), T005 (Refresher stubs), T006 (Grace stubs), T007 (helpers) all touch different files and can run in parallel after T001/T002.
- **Phase 3**: T009..T015 (seven test-writing tasks for Refill) all add functions to the same `refill_test.go` — author in parallel and resolve any merge by appending in order if conflicts arise.
- **Phase 4**: T019..T027 (nine test-writing tasks for Refresh) — same parallel pattern.
- **Phase 5**: T031..T040 (ten test-writing tasks for Grace) — same parallel pattern.
- **Phase 6**: T044, T045 (two boot-retry smoke tests) appended to `refill_test.go` — author in parallel.
- **Cross-phase**: Once Phase 2 completes, Phases 3, 4, 5 can be executed in parallel by different developers (or by a single developer interleaving). Phase 6 must wait for Phase 3; Phase 7 must wait for Phase 4. Phase 8 must wait for everything.

---

## Parallel Example: User Story 1 (Refill) test authorship

```bash
# After Phase 2 completes (stubs + helpers in place), launch the 7 Refill
# test-writing tasks in parallel:
Task: "T009 [P] [US1] Write TestRefill_SilentOnCleanExit in internal/supervise/refill_test.go"
Task: "T010 [P] [US1] Write TestRefill_401UnknownJTITransitions in internal/supervise/refill_test.go"
Task: "T011 [P] [US1] Write TestRefill_NetworkErrorIsRetryable in internal/supervise/refill_test.go"
Task: "T012 [P] [US1] Write TestRefill_AtomicDestructionOnPartialFailure in internal/supervise/refill_test.go"
Task: "T013 [P] [US1] Write TestRefill_NeverStringifiesDecryptedBytes in internal/supervise/refill_test.go"
Task: "T014 [P] [US1] Write TestRefill_AuditEventsDistinctByOutcome in internal/supervise/refill_test.go"
Task: "T015 [P] [US1] Write TestRefill_BearerTokenNeverLeaksToLogs in internal/supervise/refill_test.go"

# Verify all seven tests FAIL (red phase complete):
go test -run "TestRefill_" ./internal/supervise/

# Then proceed with implementation:
Task: "T016 [US1] Implement Refiller.Refill end-to-end in internal/supervise/refill.go"
Task: "T017 [US1] Implement (*Refiller).attach in internal/supervise/refill.go"

# Verify all seven tests now PASS (green phase complete):
go test -race -run "TestRefill_" ./internal/supervise/
```

---

## Parallel Example: Cross-phase parallelism after Phase 2

```bash
# With three developers and Phase 2 complete, all three P1 stories can
# proceed in parallel because they touch independent files:
Developer A: Phase 3 (US1 Refill) — owns refill.go + refill-portion of refill_test.go
Developer B: Phase 4 (US2 Refresh) — owns refresh.go + refresh_test.go
Developer C: Phase 5 (US3 Grace) — owns grace.go + grace_test.go
# helpers_test.go was finalized in T007 (Phase 2) and is read-only from here.
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Complete Phase 1 (Setup) → branch verified, baseline green.
2. Complete Phase 2 (Foundational) → stubs + sentinels + helpers compile.
3. Complete Phase 3 (US1 Refill) → MVP: clean child exit produces silent refill, JTI-rejection surfaces typed error, atomic destruction on partial failure.
4. **STOP and VALIDATE**: `go test -race -run "TestRefill_" ./internal/supervise/` clean.
5. Demo: a stub orchestrator can now drive a Refill cycle without involving Discord.

### Incremental Delivery

1. Setup + Foundational → foundation ready.
2. Add US1 (Refill) → silent-refill hot path; orchestrator can refill secrets. → Demo.
3. Add US2 (Refresh) → orchestrator can schedule the next Discord prompt within the operator window. → Demo.
4. Add US3 (Grace) → orchestrator can absorb an overnight crash within the configured grace window. → Demo.
5. Add US4 (Boot retry smoke) → SDD-23 has the building blocks it needs.
6. Add US5 (DM rate limit verify) → confirms FR-021-11a wiring.
7. Polish → format/lint/race triple, coverage gate, doc updates.

### Parallel Team Strategy

- All three P1 stories (US1, US2, US3) touch independent files after Phase 2; assign one per developer.
- US4 + US5 are short verification phases (<10 min each) and naturally fall to whoever finishes their P1 phase first.
- Polish (Phase 8) is sequential by nature (gates + doc updates) and is the integration step.

---

## Notes

- **TDD-mandatory** per Constitution VIII: every test-writing task MUST land BEFORE its implementation task and MUST be confirmed FAILING (red) before the implementation task starts. The `go test` command after each test-write task verifies the red phase.
- **Coverage ≥95%** per SC-021-10: verified by T053 in Phase 8. If a test fails to cover a branch (e.g. the lock-upgrade re-check in `Grace.Get`), add a targeted test before declaring done.
- **Race-clean** per Constitution VIII: every `go test` invocation in this task list uses `-race`; the chunk owns one goroutine type (Refresher tick loop, R-014) and zero in `Grace`/`Refiller`.
- **Constitution X strictness**: NO `string(decryptedBytes)` site anywhere in the three production files. The SOLE permitted `string(...)` site is the JWT bearer-header materialization inside `Snapshot.Token.Use(func(b []byte) { req.Header.Set("Authorization", "Bearer "+string(b)) })` — JWT material, NOT vault payload.
- **Anti-contracts**: T060 audits the full anti-contract list from CLAUDE.md / SDD-21.md / spec.md before declaring done.
- **Single combined commit**: per [docs/sdd/SDD-21.md § How to run this chunk](../../docs/sdd/SDD-21.md), all five phase-prompts (specify/clarify/plan/tasks/implement) defer commits to ONE combined commit at the end of /speckit-implement (Phase 5). Do NOT commit between phases of this task list either; the implement phase makes the single commit per the chunk-doc Prompt-5 instructions.
- **[P] markers**: parallel-safe = different files OR independent test functions in the same file (resolve trailing-newline merges by appending in order).
- **Story labels**: every Phase-3..7 task carries `[USn]`; Setup/Foundational/Polish carry no story label per the format spec.
