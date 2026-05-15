# Phase 0 — Research (SDD-24)

**Branch**: `026-supervisor-orchestration`
**Plan**: [plan.md](plan.md)

This file resolves every Plan-phase decision the chunk doc and spec
designated as Plan-phase. Each entry follows the Decision / Rationale /
Alternatives format from the SpecKit research template. No
`NEEDS CLARIFICATION` markers remain.

---

## R-1 — Scope decision: orchestrator location

**Decision**: **Option C** — orchestrator lives inside `package supervise`
itself as `internal/supervise/lifecycle.go` + 4 sibling files
(`lifecycle_interfaces.go`, `lifecycle_boot.go`, `lifecycle_child.go`,
`lifecycle_refresh.go`, `lifecycle_audit.go`). The cli shim
`internal/cli/supervise.go` shrinks to ~80 LOC and delegates to
`supervise.NewLifecycle(deps).Run(ctx)`.

**Rationale**:
- SDD-21's PACKAGE-MAP entry (`docs/PACKAGE-MAP.md` lines 1628-1714)
  locks the Refiller exported surface at "three constructors + four
  methods + two sentinels + one Clarification-5 addition (`Evict`)".
- The Refiller carries three post-construction dependencies (Grace
  handle, ECIES private key, server URL) wired via a package-private
  `(*Refiller).attach(grace, priv, server)` — line 200 of
  `internal/supervise/refill.go`. The PACKAGE-MAP comment at line 1652
  explicitly names "the orchestrator" as the documented caller of
  `attach`.
- SDD-19's `Store` similarly carries the JWT in `token *SecureBytes`
  and provides only a package-private `setTokenForTest` (line 220 of
  `internal/supervise/state.go`) to populate it. There is no exported
  setter.
- A sub-package `internal/supervise/lifecycle/` (chunk-doc Option B
  literal) cannot reach either package-private method.
- Adding `(*Refiller).Attach` and `(*Store).SetToken` as exported
  methods would mutate two PACKAGE-MAP lock strings verbatim — the
  SDD-21 "three ctors + four methods + ..." count and the SDD-19 lock.
- Option C keeps every SDD-19..22 PACKAGE-MAP lock intact and introduces
  ONLY new SDD-24-owned exported symbols (`Lifecycle`, `Deps`,
  `NewLifecycle`, `Run`, `Validator`, `Alerts`, `AlertClass`,
  `AlertPayload`, `Watchdog`, sentinels). PACKAGE-MAP gets an APPENDED
  SDD-24 section at implement-phase.
- Constitution VII intent ("cli/ free of business logic") is preserved
  identically to chunk-doc Option B: cli shim ≤ 100 LOC, no state
  machine reasoning in cli/.

**Alternatives considered**:
- **Option A (orchestrator inline in `internal/cli/supervise.go`)**:
  REJECTED. Estimated LOC ~800 puts business logic in the cli layer in
  direct violation of SDD-23's anti-contract and Constitution VII.
- **Option B literal (sub-package `internal/supervise/lifecycle/`)**:
  REJECTED. Requires either exposing `(*Refiller).attach` and
  `(*Store).setTokenForTest` (mutates two PACKAGE-MAP locks) or
  duplicating Refiller's fetch logic in the sub-package (mutates SDD-21
  by widening its responsibility and creates a maintenance fork).
- **Option D (move `attach` + `setToken` to exported and stay with
  Option B)**: REJECTED. The PACKAGE-MAP lock strings are explicit
  counts; adding methods changes the count. Plan-phase Complexity
  Tracking would have to carry two surface-addition violations instead
  of the one (Option C) deviation it carries today, and the surface
  additions persist across future SDDs while Option C deviates from
  doc-text only inside SDD-24.

---

## R-2 — Boot retry schedule

**Decision**:
- Initial backoff: 500 ms.
- Multiplier: 2.0.
- Cap (per attempt): 30 s.
- Jitter: ±20% on each computed interval.
- Per-attempt HTTP probe timeout: 2 s (hard cap per spec A-026-2).
- Total budget: `cfg.BootRetryTimeout` (default 10 minutes per
  `docs/CONFIG-SCHEMA.md`'s Supervisor Config section).

**Rationale**:
- Spec A-026-2 mandates `≤ 2s` per attempt to prevent a stuck network
  from consuming the entire budget on the first attempt. 500 ms initial
  + 2.0 multiplier reaches the 30 s cap inside the first 6 attempts
  (0.5, 1, 2, 4, 8, 16, 30, 30, ...) so the loop spends most of its
  10-minute budget at the cap — appropriate for a launchd boot race.
- ±20% jitter avoids thundering-herd retry alignment when multiple
  hush-supervised daemons start in the same launchd cohort
  (Constitution V — operator-visible failure must not amplify into a
  vault-server DoS).

**Alternatives considered**:
- Fixed-interval polling (every 5s): rejected — wastes early attempts
  on a fast-recovery network and underuses the budget on a
  slow-recovery one.
- Capless exponential: rejected — risks single attempts blocking >1
  minute against a partially-up network.

---

## R-3 — Tailscale + vault `/hz` probe seam

**Decision**:
- Tailscale presence probe: orchestrator's `Deps` carries
  `TailscaleProbe func(context.Context) error` with a default backed
  by the same interface lister `internal/server` consumes for AC-8
  enforcement. Default returns nil when ≥1 interface with a Tailscale
  CIDR is present; returns a typed error otherwise.
- Vault `/hz` probe: orchestrator issues `GET <cfg.ServerURL>/hz` via
  `Deps.HTTPClient` with a 2s `context.WithTimeout`. 200 → ok; any
  other status / network error → not ok.

**Rationale**:
- Wrapping the existing AC-8 lister in an injected interface preserves
  the unit-test seam (tests inject a stub) without exposing a new
  package-private symbol. The default impl is a one-liner that calls
  the existing lister.
- The 2s probe timeout matches spec A-026-2.

**Alternatives considered**:
- Calling Tailscale's local API daemon (`tailscaled` over its Unix
  socket): rejected — adds a new dependency surface, doesn't work in
  CI, and the existing interface-lister approach already covers the
  AC-8 enforcement path.

---

## R-4 — Shutdown timeout schedule

**Decision**:
- `shutdown_grace_timeout`: 10 s (orchestrator waits this long for the
  child to honour SIGTERM before escalating to SIGKILL).
- `shutdown_hard_ceiling`: 15 s (total `Run` exit budget after ctx
  cancel).
- Sequence on SIGTERM/SIGINT:
  1. Root ctx cancelled (by the cli's `signal.NotifyContext`).
  2. mainLoop observes `<-ctx.Done()` and calls
     `Child.Forward(syscall.SIGTERM)`.
  3. mainLoop spawns a 10-second watchdog timer; if `childWaitLoop`
     hasn't reported the exit by deadline, mainLoop calls
     `Child.Forward(syscall.SIGKILL)`.
  4. mainLoop spawns a 5-second watchdog timer; if the WaitGroup
     hasn't drained by deadline, log + return.
  5. Cli shim's `defer pidfile.Release()` fires unconditionally.

**Rationale**:
- Spec A-026-3 mandates "≤ the SDD-22 `StatusServer` shutdown ceiling".
  The SDD-22 socket shutdown ceiling is sub-second (the listener Close
  + per-conn force-close inside `(*StatusServer).watch`).
- 10s honour-SIGTERM grace matches typical launchd / systemd
  expectations for daemon shutdown — long enough for the child to
  flush logs + close upstream connections, short enough that
  `launchctl unload` doesn't appear hung.
- The 15s hard ceiling is verified by integration test
  `TestLifecycle_Integration_GracefulShutdownReleasesPidfile`.

**Alternatives considered**:
- No SIGKILL escalation (rely on the child honouring SIGTERM):
  rejected — a wedged child would hang the orchestrator past the
  launchd kill-timeout, leading to launchd SIGKILLing the entire
  supervisor process group (which orphans the pidfile and breaks
  Scenario 14).
- Configurable knob in the supervisor TOML: rejected for v0.1.0 —
  10s/15s is a sensible default; expose the knob in a future spec
  amendment if operators hit a wall.

---

## R-5 — Three-goroutine inventory (Constitution IX)

**Decision**: orchestrator owns three NEW goroutines beyond the two
carried over from SDD-21 (`Refresher.Run`) and SDD-22 (`StatusServer.Run`):

1. **childWaitLoop**: invokes `Child.Wait`, sends the returned
   `exitCode` on `chan childExit`. Owner: `Lifecycle`. Cancellation:
   implicit via `Child.Forward(SIGTERM)` from mainLoop. Termination:
   exits after one send per child instance.
2. **claimRefreshLoop**: receives on `chan struct{}` posted by the
   Refresher's `refill` callback, performs the signed-/claim swap,
   posts the result on `chan refreshResult`. Owner: `Lifecycle`.
   Cancellation: `<-ctx.Done()`. Termination: drains the result chan
   and exits.
3. **mainLoop**: the dispatcher. `select` over `chan childExit`,
   `chan refreshResult`, `chan refreshVerbInvocation` (status-socket
   refresh), `<-ctx.Done()`. Owner: `Lifecycle`. Cancellation:
   `<-ctx.Done()`. Termination: runs the shutdown sequence (R-4) then
   returns.

Each has a `defer func() { if r := recover(); r != nil { ... } }()`
top-frame recover that logs the panic via `Deps.Logger.Error` (Principle
IX).

**Rationale**:
- The chunk doc explicitly enumerates these three. Pinning them in
  Plan-phase prevents an implementation-phase drift into a 4-goroutine
  design that bypasses the WaitGroup gate.
- Keeping the Refresher's `refill` callback as a fast post-and-return
  (rather than calling `claimRefreshLoop`'s work synchronously) means
  the Refresher's tick loop never blocks waiting for a vault round-trip
  — the 24h tick-anchor stays accurate.

**Alternatives considered**:
- Fusing claimRefreshLoop into mainLoop: rejected — vault round-trips
  are seconds-scale (network); mainLoop must respond to child exit /
  ctx cancel at millisecond scale.
- Separate goroutine per signal-handling concern (e.g., per
  `Alerts.Emit` call): rejected — `Alerts.Emit` is contractually
  non-blocking (no-op default discards; SDD-28 will implement async
  rendering internally). mainLoop calls `Emit` synchronously.

---

## R-6 — Validator / Alerts / Watchdog interface signatures

**Decision**: see plan.md §5/§6/§7 for the locked signatures. Summary:

```go
type Validator interface { Validate(ctx, scope, secret) error }
type Alerts    interface { Emit(ctx, class, payload) }
type Watchdog  interface { OnStderrLine(ctx, line) }
```

`AlertClass` is `iota+1` over exactly 10 enum values (spec FR-026-016
locks the count and the names). `AlertPayload` is a struct with three
string fields: Scope, ErrorClass, Reason — no secret bytes by
construction.

**Rationale**:
- Single-method interfaces per Constitution IX ("Prefer single-method
  interfaces").
- `iota+1` (not `iota+0`) leaves the zero value as an invalid
  AlertClass, so an uninitialized payload can't accidentally emit as
  `AlertClassValidatorFailure`.
- The three-string AlertPayload deliberately lacks any byte-slice or
  pointer field so it's structurally impossible for an implementation
  to include the secret value (Principle X).

**Alternatives considered**:
- Multi-method interfaces (e.g., `Alerts.EmitValidatorFailure(scope)` +
  `Alerts.EmitExit78(...)` etc., one method per class): rejected —
  Constitution IX prefers single-method; the enum-dispatch pattern is
  more idiomatic and easier to mock.
- `AlertPayload.Extra map[string]string` for forward extension:
  rejected — open-ended extra fields invite future leaks; a spec
  amendment is required to add a new payload field.

---

## R-7 — Audit vocabulary reconciliation

**Decision**: append exactly 12 constants to `internal/audit/chain.go`'s
`Action*` block (see plan.md §8 table). Reuse `ActionSecretRetrieved`,
`ActionDiscordDisconnected`, `ActionDiscordReconnected` for the cross-
cutting events.

`docs/SPEC.md` §FR-14 amendment (Plan-phase ADR-1): add
`supervisor_child_exit_crash` and `supervisor_boot_timeout` to the §FR-14
documented audit-event list. The other 10 supervisor-scope names already
appear in §FR-14.

**Rationale**:
- The audit constants block carries a "Future SDDs MAY append (never
  repurpose)" comment (`internal/audit/chain.go` line 33). Appending
  is in-contract.
- spec FR-026-014 requires the orchestrator to emit ONLY declared
  constants. SC-026-008 is a grep-style test asserting docs/SPEC.md
  §FR-14 ↔ chain.go ↔ orchestrator emissions all agree 1:1.

**Alternatives considered**:
- Reusing existing generic action names (e.g., `ActionSecretRetrieved`
  for the supervisor silent-refill case): rejected — the supervisor
  silent-refill records a coarser event than per-secret retrieval; the
  per-secret retrieval is still emitted by the server during refill.
  Distinct constants preserve audit-log readability.
- Renaming the SDD-25 data-model.md table names to match existing
  constants: rejected — the 12 chosen names are the operator-facing
  vocabulary documented in SPEC.md FR-14, SDD-25 data-model.md, and
  LIFECYCLE-SCENARIOS.md. Renaming would propagate edits across three
  docs and contradict the operator-visible audit narrative.

---

## R-8 — Status-socket refresh-verb behaviour per state

**Decision**: see plan.md §10 table. Summary:
- `boot-retry` / `fetching` / `stopped`: reject with
  `{"ok":false,"error":"<state>"}\n` ack; no state mutation.
- `awaiting-approval`: drive the full refill+validate+restart path.
- `running` / `grace-restart`: coalesce with any in-flight refill.

**Rationale**:
- spec Clarification 4 (2026-05-12) resolves the pre-running behaviour:
  reject explicitly with state name; do not mutate state. This makes
  the rejection operator-visible instead of silently swallowing the
  verb.
- The existing `refreshCoalescer` in `internal/cli/supervise.go` lines
  132-172 already implements single-flight coalescing for the `running`
  state — the orchestrator MUST NOT regress that.

**Alternatives considered**:
- Allow `refresh` during `boot-retry` to short-circuit the wait:
  rejected — the boot-retry budget already converges on the same
  outcome; short-circuiting would require a separate code path with
  different audit-event vocabulary.

---

## R-9 — Line-splitting watchdog stderr writer

**Decision**: implement `lineSplittingWriter` inside
`internal/supervise/lifecycle_child.go`. It is an `io.Writer` that:
1. Forwards every `Write(p []byte) (int, error)` call to the
   operator-supplied stderr sink (the cli's `cmd.ErrOrStderr()` by
   default).
2. Maintains an internal line buffer; on every newline boundary, emits
   the completed line (without the newline) to
   `Watchdog.OnStderrLine(ctx, line)`.
3. Discards lines longer than 64 KiB (matches SDD-20's `defaultRingBufferSize`).
4. Carries a `context.Context` captured at construction so
   `OnStderrLine` calls observe ctx cancel.

The wrapper is passed as `ChildConfig.Stderr` to `NewChild`. SDD-20's
`Child.drainLoop` remains the sole reader of the child's stderr fd
(spec FR-026-030).

**Rationale**:
- Matches spec Clarification 3 (2026-05-12) verbatim: "tee + line-buffered
  adapter that fans every line to both the operator stderr sink AND
  `Watchdog.OnStderrLine`".
- 64 KiB cap matches SDD-20's ring buffer — keeping the watchdog
  cap-bounded the same way the ring is keeps the producer side simple.

**Alternatives considered**:
- A separate `bufio.Scanner`-driven goroutine reading from a `io.Pipe`:
  rejected — adds a goroutine, adds a pipe, doesn't materially improve
  the design.
- Configurable line cap via supervisor TOML: rejected for v0.1.0 —
  64 KiB is generous; expose the knob later if operators hit a wall.

---

## R-10 — Test seams summary

**Decision**: every test seam is one of:
1. An injected interface on `Deps` (Validator / Alerts / Watchdog /
   TailscaleProbe).
2. An injected concrete with an interface contract (Clock,
   *http.Client, *audit.Writer).
3. An existing package-private seam already in SDD-19..22
   (`setClockForTest` on Refresher, `setTickerForTest` on Refresher,
   `setTokenForTest` renamed `setToken` on Store) — usable from the
   `package supervise` test files only.

NO new package-private seams are introduced in `internal/supervise/`
beyond the rename. NO test seam crosses a package boundary outside its
defining package (chunk-doc anti-contract reinforced).

**Rationale**:
- Test seams that cross package boundaries make refactoring fragile;
  staying inside the defining package keeps the seam invariant local.
- The `setTokenForTest` rename to `setToken` reflects its new
  production-path role (the orchestrator inside `package supervise`
  invokes it post-/claim). The Test suffix would mislead future
  readers.

**Alternatives considered**:
- Adding a `Clock` field on `Deps` and threading it through to
  Refiller/Refresher: rejected — Refiller has no Clock dependency, and
  Refresher's Clock is already injectable via the existing
  package-private seams.

---

## R-11 — Secret-bytes audit (Principle X)

**Decision**: the orchestrator introduces exactly ONE new `string(...)`
materialization site: child-env construction. The site is at the OS
fork boundary, mirrors the existing SDD-20 `ChildConfig.Env` contract
(which is `[]string`), and the env slice is zeroed in place after
`Child.Start` returns.

The JWT bearer-header materialization inside `Snapshot.Token.Use` is
already permitted by Constitution X and SDD-21.

**Rationale**:
- Plan-phase audit traced every secret-byte touch (plan.md §13 table)
  and confirmed no other `string(secretBytes)` site exists.
- The env-construction site is gated by a `Use(func(b []byte) {})`
  closure on each `*SecureBytes` so the conversion happens inside the
  SecureBytes pin/unpin scope.

**Alternatives considered**:
- Using `os.Exec` directly with a `[][]byte` env (avoiding the
  `[]string` materialization): rejected — Go's stdlib `os/exec.Cmd.Env`
  is `[]string`; SDD-20's `ChildConfig.Env` mirrors that. Inventing a
  byte-slice alternative requires CGo or unsafe pointer arithmetic
  against `execve` directly, violating Constitution IX (no CGO).

---

## R-12 — Coverage gate

**Decision**: ≥ 85% line coverage on the new orchestrator file(s)
under `magex test:race`. Verified by running `magex test:coverage` and
asserting the report's lifecycle*.go lines hit threshold.

**Rationale**: spec FR-026-021 + SC-026-006 lock 85%. Below 85% indicates
behaviour branches without tests — Constitution VIII gate.

**Alternatives considered**:
- 100% (matching crypto/key packages): rejected — orchestrator is
  protocol-glue, not security-critical; 100% would require synthetic
  tests for unreachable error branches, which adds maintenance overhead
  without proportionate safety benefit.
