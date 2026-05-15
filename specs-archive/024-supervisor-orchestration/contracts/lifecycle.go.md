# Contract: `Lifecycle` (SDD-24)

**Package**: `github.com/mrz1836/hush/internal/supervise`
**Files**: `lifecycle.go` (primary), `lifecycle_boot.go`, `lifecycle_child.go`,
`lifecycle_refresh.go`, `lifecycle_audit.go`.

This contract locks the exported surface of SDD-24 inside
`package supervise`. The contract is appended to `docs/PACKAGE-MAP.md` at
implement-phase as a new "SDD-24 — Supervisor orchestration glue"
section; the existing SDD-19..22 PACKAGE-MAP sections are NOT edited.

---

## 1. Exported types

```go
// Lifecycle is the supervisor orchestrator. Construct via NewLifecycle;
// drive via Run(ctx). Single-shot. Owns three goroutines beyond the two
// carried over from SDD-21 / SDD-22 (childWaitLoop, claimRefreshLoop,
// mainLoop). Each goroutine has owner + ctx + termination + top-frame
// recover per Constitution IX.
type Lifecycle struct { /* opaque — fields defined in data-model.md §1 */ }

// Deps carries every injected dependency NewLifecycle requires. Nil
// fields with documented defaults remain nil-safe (see data-model.md §2).
type Deps struct { /* opaque — fields defined in data-model.md §2 */ }
```

## 2. Constructor

```go
// NewLifecycle constructs a Lifecycle. Validates required Deps fields
// and panics on nil for any required dependency (Constitution IX
// startup-wiring exemption). Optional Deps fields (Validators / Alerts /
// Watchdog / TailscaleProbe / VaultHzProbe) receive their no-op or
// stdlib-backed default when nil.
//
// NewLifecycle constructs the SDD-19..22 primitives internally:
//   - supervise.NewStore(ctx, deps.Clock)
//   - supervise.NewGrace(cfg.CacheGraceTTL, cfg.CacheSecretsForRestart)
//   - supervise.NewRefiller(deps.HTTPClient, store, deps.Logger).attach(grace, deps.DecryptKey, cfg.ServerURL)
//   - supervise.NewRefresher(cfg.RefreshWindow, cfg.RequestedTTL, lifecycleRefreshCallback, deps.Logger)
//   - supervise.NewStatusServer(cfg.StatusSocket, store, deps.Logger)
//     .AttachStatusInputs(statusInputs)
//     .AttachRefreshHandler(refreshCoalescer.Handle)
//
// On any construction failure (impossible from the locked SDD-19..22
// constructors, which panic on bad input), NewLifecycle propagates the
// panic. The caller (cli shim) catches via cobra's RunE error return.
func NewLifecycle(ctx context.Context, cfg *config.Supervisor, deps Deps) *Lifecycle
```

**Pre-conditions**:
- `cfg` is non-nil and has passed `config.Load`'s validation.
- `deps.PidFile` is already acquired (`AcquirePidFile` was called by
  the cli shim and returned non-nil).
- The current process is running on a Tailscale-attached host (the
  TailscaleProbe will verify at boot, but the cli shim assumes the
  daemon was started under launchd/systemd with Tailscale prerequisites).

**Post-condition**:
- Returns a non-nil `*Lifecycle` ready for `Run`.
- The status server / refresher have NOT yet started; `Run` starts
  them in dependency order.

## 3. Run

```go
// Run drives the supervisor lifecycle. Blocks until ctx is cancelled
// OR a terminal failure (boot timeout, vault rejects /claim with a
// terminal 4xx, pidfile lost). Returns nil on clean ctx-cancelled
// shutdown; returns a wrapped error on terminal failure.
//
// Run is single-shot — a second invocation on the same *Lifecycle
// returns ErrLifecycleAlreadyRan. Construct a fresh Lifecycle to
// re-run.
//
// Run sequence (high level):
//   1. Spawn StatusServer.Run goroutine.
//   2. Spawn Refresher.Run goroutine.
//   3. Boot-retry loop: TailscaleProbe + VaultHzProbe with exponential
//      backoff up to cfg.BootRetryTimeout.
//      On exhaustion: emit AlertClassBootTimeout, append
//      ActionSupervisorBootTimeout, return wrapped ErrBootTimeout.
//   4. Submit signed /claim. On 503+discord_unavailable: alert and
//      retry in same boot loop. On any other terminal status: return
//      wrapped error.
//   5. Persist JWT into Store via package-private setToken.
//   6. Append ActionSupervisorSessionClaimed.
//   7. Call Refiller.Refill(ctx, cfg.Scope).
//   8. For each scope, call configured Validator (no-op default).
//      Failure: transition to StateAwaitingApproval, emit
//      AlertClassValidatorFailure, append ActionSupervisorAwaitingApproval
//      with cause="validator".
//   9. Build child env from Grace.Get(scope), construct Child via
//      NewChild(cfg), call Start(ctx). Transition to StateRunning.
//  10. Spawn childWaitLoop goroutine.
//  11. mainLoop dispatches on:
//       - childExit chan: per FR-026-009 (0 / non-zero non-78 / 78)
//       - refreshDone chan: per FR-026-011 / FR-026-012
//       - refreshVerb chan: per Plan §10 (state-conditional)
//       - <-ctx.Done(): shutdown sequence per Plan §R-4
//  12. Shutdown sequence: Child.Forward(SIGTERM); wait 10s; if alive,
//      Child.Forward(SIGKILL); wait 5s; wg.Wait(); deps.PidFile.Release().
//
// Errors returned wrap the underlying cause and identify the call-site
// for the cli shim to project onto an exit code:
//   - ErrBootTimeout (wrapped) → ExitErr
//   - terminal /claim 4xx (wrapped) → ExitErr
//   - context cancel → nil
//   - any other → ExitErr
func (l *Lifecycle) Run(ctx context.Context) error
```

**Goroutines spawned**: 5 total (3 owned + 2 carried). All join via
`Lifecycle.wg` before `Run` returns. `runtime.NumGoroutine` pre/post
snapshot at integration-test teardown MUST show zero growth (spec
SC-026-011).

**Concurrency contract**:
- `Run` is the sole driver; never invoked concurrently.
- Individual `Alerts.Emit` calls from inside `Run` are synchronous and
  MUST NOT block (no-op default discards; SDD-28 implementation must
  honour this).
- Status-socket reads happen concurrently with `Run`; the StatusInputs
  contract requires every getter to be safe for concurrent read
  (existing SDD-22 contract).

## 4. Sentinels

```go
var ErrLifecycleAlreadyRan      = errors.New("supervise: lifecycle already ran")
var ErrValidatorFailed           = errors.New("supervise: validator failed")
var ErrRefillFailedPostRunning   = errors.New("supervise: post-running refill failed")
```

## 5. Anti-API (deliberately NOT exported)

- `(*Lifecycle).Stop()` / `(*Lifecycle).Restart()` — shutdown is driven
  ONLY by ctx cancel (Constitution IX — single cancellation path per
  goroutine owner). Any future "restart" semantics require a fresh
  `NewLifecycle`.
- Any field access on `Lifecycle` from outside package supervise —
  the struct is opaque; the only interaction surface is `Run`.
- A `WithClock` / `WithHTTPClient` builder pattern — all injection is
  via `Deps` at construction (Constitution IX — accept interfaces at
  the consumer, no fluent builders).

## 6. Locked behaviours (one-line per FR)

| FR | One-line lock |
|----|---------------|
| FR-026-001 | Pidfile MUST be acquired by cli shim BEFORE NewLifecycle; passed via `Deps.PidFile`. NewLifecycle panics on nil. |
| FR-026-002 | NewLifecycle constructs Store / Grace / StatusServer / Refiller / Refresher in dependency order; Run spawns the two carry-over goroutines before the boot-retry loop. |
| FR-026-003 | Run's boot loop calls `Deps.TailscaleProbe(ctx)` AND `Deps.VaultHzProbe(ctx, cfg.ServerURL)` with per-attempt 2s timeout. |
| FR-026-004 | Boot timeout exit: wraps `ErrBootTimeout`, audits `ActionSupervisorBootTimeout`, no Discord prompt. |
| FR-026-005 | Submits exactly one signed /claim per boot success; signed via `sign.CanonicalJSON + sign.Sign` (SDD-08). |
| FR-026-006 | After JWT persists, calls `Refiller.Refill(ctx, cfg.Scope)` exactly once. |
| FR-026-007 | For each scope, calls `Deps.Validators[scope].Validate` (no-op default) before child start. |
| FR-026-008 | Builds env via `Grace.Get(scope).Use(func(b []byte) { env = append(env, scope+"="+string(b)) })`; calls NewChild + Start. |
| FR-026-009 | childExit dispatch per Plan §11 test table. |
| FR-026-010 | `errors.Is(err, ErrJTIUnknown)` → StateAwaitingApproval + AlertClassVaultRejectedJWT. |
| FR-026-010a | Boot-time refill error → boot-retry loop; post-running refill error → StateAwaitingApproval + AlertClassRefillFailed. |
| FR-026-011 | Refresher tick → claimRefreshLoop posts result; mainLoop swaps Store.token atomically; child PID unchanged. |
| FR-026-012 | Refresh denied / timeout → existing session preserved; AlertClassRefreshDenied or AlertClassRefreshTimeout. |
| FR-026-013 | `Deps.Alerts.Emit` called at exactly 10 sites; default discards. |
| FR-026-013a | `Deps.Watchdog.OnStderrLine` wired via line-splitting `io.Writer` passed as `ChildConfig.Stderr`. |
| FR-026-014 | Orchestrator emits ONLY constants from `internal/audit/chain.go` (extended in this chunk; see audit-vocabulary.md). |
| FR-026-015..018 | Three single-method interfaces defined at the consumer; no-op defaults. |
| FR-026-019..020 | Shutdown: ctx cancel → SIGTERM → 10s grace → SIGKILL → 5s wait → wg.Wait → pidfile.Release. |
| FR-026-021..024 | ≥85% coverage; per-branch tests; sentinel-leak; no-business-logic-leakage. |
| FR-026-025 | Consumes SDD-19..22 only via locked APIs — no surface mutation. |
| FR-026-026 | No validator/alert/watchdog implementation pre-defined. |
| FR-026-027 | No new `go.mod` direct dependency. |
| FR-026-028 | No `string(secretBytes)` outside JWT bearer-header AND child-env build sites. |
| FR-026-029 | Every spawned goroutine has owner + ctx + termination + top-frame recover. |
| FR-026-030 | No second drain on Child.Stdout/Stderr — watchdog hook is a single fanout on the existing drain's writer. |
| FR-026-031 | No init(); no package-level mutable vars beyond `var Err… = errors.New(…)`. |
