# Phase 0 Research — SDD-25 Lifecycle Integration Harness

The spec + chunk contract leave **zero `[NEEDS CLARIFICATION]` markers**. The clarifications collected during `/speckit-clarify` (spec.md §Clarifications) already resolved the four ambiguities. This document captures the seven design decisions the plan implies — each one is a fork in the implementation that has a defensible single answer plus a rejected alternative.

---

## 1. Harness file allocation

**Decision**: Thread clock injection, audit ordering helper, status-socket client, goroutine-leak detector, validator-upstream mocks into the six harness files the chunk contract enumerates. Do not add a seventh file.

**Allocation**:

| Concern | File | Why |
|---------|------|-----|
| Vault fixture, state-dir setup, clients.json registry | `vault.go` | All on-disk-at-rest concerns belong with the vault helper. |
| Real `internal/server` in-process; validator-upstream `httptest.Server`s; `stubAsApprover` adapter; `ClockSyncProbe`/`InterfaceLister`/`Listener` seam injections | `server.go` | Every server-side concern clusters here. Validator upstreams are remote HTTP boundaries the supervisor calls; clustering them with the server keeps the "real server, mocked outside world" boundary clear. |
| `DiscordStub` wrapping, programmable connectivity/rate-limit/approval sequences, alert-payload recorder interface | `discord.go` | Discord is exactly the boundary this file owns. |
| `Store`, `Refiller`, `Refresher`, `Grace`, `StatusServer`, `PidFile` composition; controllable clock (`func() time.Time`); supervisor-side audit subsequence assertion; goroutine-leak detector; status-socket client | `supervisor.go` | The supervisor IS the lifecycle being tested; every supervisor-side test affordance lives here so a scenario reaches for exactly one helper. |
| Programmable child process (exit codes, lifetime, stdout/stderr-pattern emission) | `child.go` | Child-lifecycle concerns are downstream of the supervisor and self-contained. |
| `slog` handler chain capturing every record; `AssertSentinelAbsent(t, streams...)`; the canonical list of "every captured byte stream" | `log_capture.go` | Sentinel-absence is the cross-cutting redaction contract and naturally lives in a single helper module. |

**Rejected alternative**: separate files per concern (e.g. `clock.go`, `audit.go`, `goroutines.go`, `validators.go`, `status_client.go`). Rejected because the chunk contract fixes the six-file inventory; adding files breaks the contract. Splitting concerns finer-grained also makes scenarios reach for too many import paths, which inflates each scenario function past one-screen-height — `scenarios_test.go` must stay readable.

---

## 2. Refresher clock-injection strategy

**Decision**: For Scenario 8 (DaytimeRefresh) and Scenario 9 (OvernightExpiry), the harness **does not invoke `Refresher.Run()`**. It invokes the `refill` callback directly at the scenario’s scripted moment and asserts the resulting state transition + audit event + status-socket reflection. The Refresher’s tick loop is already covered by its own unit tests under `internal/supervise/refresh_test.go` (which use the package-private `setClockForTest`). The integration suite proves the **integration**: when refill fires, the consequences flow through the supervisor end-to-end.

**Rationale**: FR-025-16 forbids `time.Sleep` to drive a documented transition. The `Refresher` exposes its clock seam only through a package-private `setClockForTest` setter; the integration harness cannot reach it without putting integration-only files inside `internal/supervise/`, which would break the SDD-21 lock ("zero new exported symbols beyond the locked surface"). Driving the refill callback directly is the cleanest way to test the system without breaking the lock — and it is faithful: refill is exactly what the tick loop does at the scheduled instant.

**Rejected alternatives**:
- *Add a public `WithClock(now func() time.Time)` option to `NewRefresher`* — rejected because SDD-21 explicitly locks "three constructors + four methods + two sentinels + Evict"; a fifth method violates the lock and triggers Constitution Check.
- *Add a `//go:build integration` test-helper file inside `internal/supervise/`* — rejected because that places integration-test-only code inside a production package, which Constitution XI ("smallest dependency surface") and SDD-21's anti-API ("zero new test-helper binary") both push back on.
- *Use real `time.Sleep` for ~tick-interval ms* — rejected by FR-025-16 and would re-introduce flakiness.

---

## 3. Audit-event subsequence assertion algorithm

**Decision**: `harness.AssertAuditSubsequence(t, recorded []audit.Event, documented []string)` walks `recorded` left-to-right with a pointer into `documented`. For each `recorded[i]`, if `recorded[i].Action == documented[ptr]`, increment `ptr`. At the end, assert `ptr == len(documented)`. This is a classic subsequence check and runs in O(n+m).

**Rationale**: Spec FR-025-7 + Clarification 1 explicitly require **relative** ordering: documented events must appear in documented order; unmentioned intervening events are tolerated. The subsequence algorithm matches the requirement exactly. The helper additionally calls `audit.Verify(path, verifyKey)` to assert hash-chain continuity (FR-025-24).

**Rejected alternatives**:
- *Strict contiguous subsequence (no intervening events allowed)* — contradicts Clarification 1.
- *Set-equality (order ignored)* — contradicts FR-025-23 ("the sequence of audit events the scenario produces — not merely the set").

---

## 4. Goroutine-leak detection

**Decision**: At scenario entry, `harness.NewSupervisor(t, …)` snapshots `runtime.NumGoroutine()` and the goroutine name set via `runtime.Stack(buf, true)` parsed for "goroutine N" header lines. At `t.Cleanup` time (after all scenario-owned cleanup has run), the harness re-snapshots and asserts the delta is zero modulo a whitelist of test-runtime goroutines (the `testing` framework spawns a few of its own; the harness records the pre-snapshot count, not zero). Detection uses a bounded poll loop (max 100 iterations × `runtime.Gosched()`) — no `time.Sleep`, no real-time wait. If the delta hasn't dropped to zero after 100 yields, the scenario fails with a labeled stack dump pointing at the leaked goroutines.

**Rationale**: FR-025-20 + SC-025-5 require detection; Constitution IX requires every harness-spawned goroutine to have an explicit termination condition. A bounded `runtime.Gosched()` poll is the idiomatic "wait for in-flight goroutines to drain" pattern and avoids the spec's `time.Sleep` prohibition.

**Rejected alternatives**:
- *`goleak` library from Uber* — Constitution XI ("zero new direct dep").
- *Skip leak detection, rely on `-race`* — FR-025-20 mandates explicit detection.

---

## 5. Validator-upstream HTTP mocks

**Decision**: One `httptest.Server` per validator (Anthropic, Anthropic-OAuth, OpenAI, GitHub, Google AI), constructed by `harness.NewServer(t, …)` and torn down via `t.Cleanup`. Each server registers a programmable handler (`func(*scenarioCtx) http.HandlerFunc`) that the scenario configures per-test: 200 OK on `/v1/models`, 401 on `/v1/models`, network failure (close listener), timeout (block on a channel until ctx cancels). The supervisor's `http.Client` is wired to a custom `RoundTripper` that rewrites the hostname (`api.anthropic.com` → the httptest listener) so the real production validator code is exercised without modification.

**Rationale**: FR-025-12 + FR-025-15 + Scenario 6 require validator-failure paths; FR-025-13 forbids real upstream traffic. `httptest` + `RoundTripper` rewrite is the stdlib-idiomatic pattern. The production `internal/supervise/validators` code (delivered by SDD-26 — currently `pending`) is exercised verbatim.

**Rejected alternatives**:
- *Stub the validator interface directly* — violates FR-025-3 + spec Assumption ("scenarios MUST be implemented against the real production packages once they ship").
- *DNS-level shim* — invasive, platform-specific, and racy.

---

## 6. Programmable child-process construction

**Decision**: Use `os.Executable()` re-invocation. The integration test binary itself is re-invoked under a special argv pattern (`os.Args[0] -- --integration-child-mode --exit-code=N --lifetime=N --emit-stderr-pattern=P`) recognized by a sentinel in `tests/integration/lifecycle_test.go`'s `init()` equivalent (a package-private function called from `TestMain`). The child binary process executes the scripted behavior (sleep, emit pattern, exit with code) then exits.

**Rationale**: The SDD-20 `internal/supervise/Child` is `os/exec`-based; the cleanest way to drive deterministic child lifetimes is to make the test binary itself the child. This pattern is in active use in `internal/supervise/child_test.go` and is therefore a known-good seam. The spec forbids `time.Sleep` to drive **documented transitions**; the child's own lifetime is part of the test setup, not a documented transition — its lifetime is whatever the scenario scripts via the `--lifetime` flag.

**Note on Constitution IX**: the chunk's `TestMain` is conceptually `init`-adjacent but is allowed by `testing`'s contract. The harness uses `testing.M.Run` framework hook, not a Go `init()`. No production-style `init()` is introduced.

**Rejected alternatives**:
- *Build a separate `cmd/integration-child` binary* — adds a build artifact + go.mod scope. The os.Executable() pattern is lighter.
- *Use `/bin/sh -c "exit N"` style shell-out* — fragile across macOS/Linux + can't emit stdout/stderr patterns.
- *Use SDD-20's anti-API `cmd/test-helper-*` binary pattern* — explicitly listed as anti-API in the SDD-20 lock; reusing it from the integration suite would be a layering violation.

---

## 7. Status-socket client

**Decision**: `harness.StatusSocketGet(t, socketPath string) ([]byte, error)` opens a Unix-domain dial (2 s context), writes nothing (default-verb path per SDD-22 §2.5), reads to EOF, returns the raw bytes. The scenario then either (a) unmarshals into a `map[string]any` for shape assertion + value comparison or (b) unmarshals into a private DTO matching the FR-12 schema. A second helper `harness.StatusSocketRefresh(t, socketPath string) error` writes `refresh\n` and reads the `{ok, error}` ack (covers Scenario 13).

**Rationale**: The status socket's wire contract is locked at SDD-22 + SDD-23; the harness consumes the bytes verbatim. The exact JSON shape assertion is the responsibility of the scenario.

**Rejected alternatives**:
- *Reach into `internal/supervise.StatusServer` directly* — the socket IS the contract; reading the wire is the truthful assertion.
- *Spawn `hush client status` as a subprocess* — fragile, slow, and tests the wrong layer (the CLI, not the supervisor).

---

## Cross-cutting principle audit

All seven decisions are revisited against Constitution VIII (TDD), IX (idiomatic Go), X (redaction) and XI (minimal deps):

- **VIII**: every decision keeps the test contract intact (no skip path, no soft-fail).
- **IX**: every goroutine the harness spawns is owned, ctx-cancellable, with explicit termination + top-frame `recover`. No `init()`, no package-level mutable globals.
- **X**: no decision introduces a code path that could materialize a secret as a Go `string`. The `AssertSentinelAbsent` helper covers every captured byte stream including the new validator-upstream mock response bodies (FR-025-26).
- **XI**: zero new direct `go.mod` dependencies. Every decision uses stdlib or already-in-module helpers.

Plan is internally consistent.
