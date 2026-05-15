# Phase 1 — Data Model (SDD-24)

**Branch**: `026-supervisor-orchestration`
**Plan**: [plan.md](plan.md) · **Research**: [research.md](research.md)

This document defines the new types introduced by SDD-24. The orchestrator
consumes the SDD-19..22 primitives without adding fields to their existing
types; only NEW SDD-24-owned types are listed here. Every type lives in
`package supervise` (Plan §1 Option C).

---

## 1. `Lifecycle` (orchestrator)

The long-lived, single-process struct that composes the SDD-19..22
primitives into the documented daemon lifecycle.

```go
// Lifecycle is the supervisor orchestrator. Construct via NewLifecycle;
// drive via Run(ctx). Single-shot — calling Run twice returns
// ErrLifecycleAlreadyRan.
type Lifecycle struct {
    // — dependencies (set at construction) —
    deps     Deps
    config   *config.Supervisor

    // — composed primitives (constructed inside NewLifecycle) —
    store        *Store
    grace        *Grace
    refiller     *Refiller
    refresher    *Refresher
    statusServer *StatusServer
    pidfile      *PidFile          // owned by Lifecycle.Run; released on shutdown

    // — orchestrator state —
    coalescer    *refreshCoalescer // existing single-flight gate from FR-023-22a
    inputs       *statusInputs     // implements StatusInputs (moved out of cli/)
    childExitCh  chan childExit
    refreshTickCh chan struct{}
    refreshDoneCh chan refreshResult
    refreshVerbCh chan refreshVerb

    // — lifecycle guard —
    runOnce sync.Once
    ran     atomic.Bool
    wg      sync.WaitGroup

    // — child handle (lifecycle owns it; nil between exits) —
    childMu sync.Mutex
    child   *Child
}
```

**Field invariants**:
- `deps` and `config` are immutable post-construction.
- `store` / `grace` / `refiller` / `refresher` / `statusServer` are
  constructed inside `NewLifecycle` and never replaced.
- `pidfile` is acquired by the cli shim BEFORE `NewLifecycle` is called
  and passed into `Run`. (See `(*Lifecycle).Run` contract below.)
- `coalescer.perform` is the closure that drives the full
  refill+validate+restart path; wired in `NewLifecycle`.
- All channels are constructed buffered = 1 (single-buffer suffices —
  every producer/consumer pair has a documented coalescing semantics
  upstream).

**Lifecycle states** (synthesized — the actual state is `Store.Snapshot().State`):

| Orchestrator perspective | `supervise.State` | Notes |
|--------------------------|-------------------|-------|
| boot-retry | `StateFetching` | initial state; before /claim succeeds |
| fetching (post-claim) | `StateFetching` | claim returned, Refiller.Refill in flight |
| running | `StateRunning` | child started, Wait pending |
| awaiting-approval | `StateAwaitingApproval` | any stale path |
| grace-restart | `StateGraceRestart` | grace cache restart in progress |
| stopped | `StateStopped` | terminal |

The orchestrator NEVER materializes a state-string literal in its own
code (FR-026-023); it always calls `Store.Transition(ctx, EventX)` and
reads back via `Store.Snapshot().State`.

---

## 2. `Deps` (injected dependencies)

```go
// Deps carries every injected dependency the Lifecycle needs to
// compose against. Construct in the cli shim; pass into NewLifecycle.
// Fields with no-op defaults are zero-value-safe — nil acceptable.
type Deps struct {
    // — required —
    Logger          *slog.Logger
    HTTPClient      *http.Client      // outgoing to vault server
    Clock           Clock             // wall-clock seam (existing interface)
    ClaimSigningKey *ecdsa.PrivateKey // BIP32-derived per FR-3 + SDD-08
    DecryptKey      *ecdsa.PrivateKey // ECIES private key for Refiller decrypt
    AuditWriter     *audit.Writer     // emits the 12 new + 3 reused actions

    // — required: existing pidfile (acquired by cli shim before NewLifecycle) —
    PidFile *PidFile

    // — injected interfaces (nil → no-op default) —
    Validators map[string]Validator // keyed by scope name; nil → no-op for every scope
    Alerts     Alerts               // nil → discards
    Watchdog   Watchdog             // nil → discards

    // — boot probes (nil → defaults wired from SDD-10 lister + 2s GET) —
    TailscaleProbe func(ctx context.Context) error
    VaultHzProbe   func(ctx context.Context, serverURL string) error
}
```

**Validation** (performed in `NewLifecycle`):
- `Logger`, `HTTPClient`, `Clock`, `ClaimSigningKey`, `DecryptKey`,
  `AuditWriter`, `PidFile` MUST be non-nil. Nil → panic at construction
  (Constitution IX startup-wiring exemption).
- `Validators` MAY be nil OR may omit any scope — missing scopes use
  the no-op default validator.
- `Alerts`, `Watchdog`, `TailscaleProbe`, `VaultHzProbe` MAY be nil —
  each gets a default impl.

---

## 3. `Validator` / `Alerts` / `Watchdog` interfaces

```go
// Validator validates one secret value for one scope. Returns nil on
// success or a wrapped error naming the scope on failure.
// Implementations MUST NOT log the secret value (Constitution X).
type Validator interface {
    Validate(ctx context.Context, scope string, secret *securebytes.SecureBytes) error
}

// noopValidator is the default — returns nil for every call.
type noopValidator struct{}

func (noopValidator) Validate(context.Context, string, *securebytes.SecureBytes) error {
    return nil
}

// Alerts is the operator-visible alert sink. Implementations MUST be
// non-blocking and MUST NOT include secret values in any payload field
// (AlertPayload is constructed to make this structurally impossible).
type Alerts interface {
    Emit(ctx context.Context, class AlertClass, payload AlertPayload)
}

type noopAlerts struct{}

func (noopAlerts) Emit(context.Context, AlertClass, AlertPayload) {}

// Watchdog observes child stderr lines. Alert-only — MUST NOT influence
// state-machine transitions (Constitution V, spec FR-026-013a).
type Watchdog interface {
    OnStderrLine(ctx context.Context, line []byte)
}

type noopWatchdog struct{}

func (noopWatchdog) OnStderrLine(context.Context, []byte) {}
```

---

## 4. `AlertClass` enum (LOCKED at 10 values per spec FR-026-016)

```go
type AlertClass int

const (
    AlertClassValidatorFailure         AlertClass = iota + 1
    AlertClassExit78
    AlertClassVaultRejectedJWT
    AlertClassRefillFailed
    AlertClassDiscordUnavailableOnClaim
    AlertClassRefreshDenied
    AlertClassRefreshTimeout
    AlertClassGraceEntered
    AlertClassLogPatternMatch
    AlertClassBootTimeout
)

// String returns the locked human-readable form; used in AlertPayload.Reason,
// audit event Data.class, and SDD-28's renderer. MUST match the spec
// FR-026-016 names verbatim.
func (c AlertClass) String() string {
    switch c {
    case AlertClassValidatorFailure:          return "ValidatorFailure"
    case AlertClassExit78:                    return "Exit78"
    case AlertClassVaultRejectedJWT:          return "VaultRejectedJWT"
    case AlertClassRefillFailed:              return "RefillFailed"
    case AlertClassDiscordUnavailableOnClaim: return "DiscordUnavailableOnClaim"
    case AlertClassRefreshDenied:             return "RefreshDenied"
    case AlertClassRefreshTimeout:            return "RefreshTimeout"
    case AlertClassGraceEntered:              return "GraceEntered"
    case AlertClassLogPatternMatch:           return "LogPatternMatch"
    case AlertClassBootTimeout:               return "BootTimeout"
    }
    return "Unknown"
}
```

**Anti-extension**: SDD-28 MUST NOT extend this enum without a spec
amendment. The four server-side LIFECYCLE classes (`approval request`,
`daemon refresh request`, `Discord disconnected`, `Discord reconnected`)
are emitted on other channels — they are NOT in this enum.

---

## 5. `AlertPayload`

```go
// AlertPayload carries the non-secret labels accompanying every Alerts.Emit
// call. Structurally cannot carry a secret value — every field is a string.
type AlertPayload struct {
    Scope      string // failed scope name, e.g. "ANTHROPIC_API_KEY"; "" when N/A
    ErrorClass string // coarse error class: "transient", "unknown_jti",
                      // "discord_unavailable", "deny", "timeout", "cancelled"
    Reason     string // human-readable phrase from the supervise.reasons map
                      // OR from spec Clarification 1
}
```

**Validation** (orchestrator emission-site):
- Scope ALWAYS quoted by the orchestrator from the `cfg.Scope` slice or
  the `Validator.Validate` call argument — never user-controlled.
- ErrorClass is one of a closed set; orchestrator computes via
  `classifyOutcome(err)` (existing SDD-21 helper at refill.go:230).
- Reason is selected from a closed phrase map in `lifecycle_audit.go`.

---

## 6. Sentinel errors

```go
// ErrLifecycleAlreadyRan is returned by Run on a second invocation
// of the same Lifecycle. Compare via errors.Is.
var ErrLifecycleAlreadyRan = errors.New("supervise: lifecycle already ran")

// ErrValidatorFailed is the sentinel orchestrator emits (wrapped with
// the scope name in the message) on any Validator.Validate non-nil
// return. Compare via errors.Is.
var ErrValidatorFailed = errors.New("supervise: validator failed")

// ErrRefillFailedPostRunning is the sentinel orchestrator emits
// (wrapped) when Refiller.Refill returns any non-ErrJTIUnknown error
// during a post-running silent refill. Compare via errors.Is.
var ErrRefillFailedPostRunning = errors.New("supervise: post-running refill failed")
```

Reused from SDD-19..22 (no new declarations):
- `supervise.ErrPidLocked` (SDD-22)
- `supervise.ErrSocketPermsLoose` (SDD-22)
- `supervise.ErrAlreadyRunning` (SDD-22)
- `supervise.ErrJTIUnknown` (SDD-21)
- `supervise.ErrBootTimeout` (SDD-21 — declared there; emitted here for the first time per A-021-10)
- `supervise.ErrInvalidTransition` (SDD-19)
- `supervise.ErrChildNotStarted`, `ErrCommandEmpty`, `ErrCommandPathRelative` (SDD-20)
- `supervise.Exit78` (SDD-20 constant; not an error)

---

## 7. Internal channel types

```go
// childExit is the message childWaitLoop emits per child instance.
type childExit struct {
    code   int
    signal syscall.Signal
    err    error // typically nil; non-nil only on Wait-internal failure
}

// refreshResult is the message claimRefreshLoop emits per swap attempt.
type refreshResult struct {
    err error // nil on success; non-nil categorizes deny / timeout / network
    deny bool // true when the approver explicitly denied (vs. timeout)
}

// refreshVerb is the message the status-socket refresh handler posts
// when in awaiting-approval / running / grace-restart state.
type refreshVerb struct {
    ack chan error // orchestrator sends the terminal error (or nil) to unblock the status handler
}
```

---

## 8. Audit-event `Data` field projections

Each new audit action carries a `Data map[string]any` projection. The
orchestrator constructs these inside `lifecycle_audit.go`'s helpers;
the projection rules are:

| Action | Data keys |
|--------|-----------|
| `ActionSupervisorSessionClaimed` | `jti`, `session_type`, `exp` (RFC3339), `scope` ([]string), `outcome` (always `"approved"`) |
| `ActionSupervisorSessionRefreshed` | `jti`, `prev_jti`, `exp`, `outcome` |
| `ActionSupervisorSilentRefill` | `scopes` ([]string), `outcome` |
| `ActionSupervisorChildCleanExit` | `child_pid`, `uptime` (Duration.String()) |
| `ActionSupervisorChildExitCrash` | `child_pid`, `exit_code`, `signal` (when ws.Signaled()), `uptime` |
| `ActionSupervisorChildExit78` | `child_pid`, `uptime` |
| `ActionSupervisorAwaitingApproval` | `cause` (one of: `"validator"`, `"unknown_jti"`, `"exit_78"`, `"refill_failed"`, `"boot_timeout"`) |
| `ActionSupervisorStaleAlert` | `class` (AlertClass.String()), `scope`, `error_class` |
| `ActionSupervisorGraceEntered` | `scopes`, `grace_ttl_remaining` |
| `ActionSupervisorGraceExited` | `scopes`, `outcome` (one of: `"restart_ok"`, `"refresh_window"`, `"expired"`) |
| `ActionSupervisorBootTimeout` | `boot_retry_timeout`, `last_error_class` |
| `ActionClientRefreshInvoked` | `state` (state name at verb arrival), `outcome` |

**Anti-contract**: NO data key may carry:
- A `*SecureBytes` pointer
- A `[]byte` field whose source is secret material
- A `string` field whose source is `string(secretBytes)` for the
  vault payload

The `data.jti` key carries the JWT's JTI (a UUID inside the JWT
claims) — JTI is metadata not secret material; SDD-08 documents this.

---

## 9. Status-socket `statusInputs` projection (moved into `package supervise`)

The existing `orchestratorInputs` struct currently in
`internal/cli/supervise.go` lines 54-119 moves into
`internal/supervise/lifecycle.go` as an unexported `statusInputs`
struct. Its fields and methods are unchanged (atomic.Pointer / atomic.Bool
per field, getter methods implementing the locked SDD-22
`StatusInputs` interface).

The orchestrator drives `statusInputs.scopeHealthy` /
`statusInputs.scopeStale` updates via the existing atomic-store pattern
on every state transition that changes scope freshness:

| Transition | scopeHealthy | scopeStale | sessionExp | refreshNext |
|-----------|--------------|------------|------------|-------------|
| /claim approved | `cfg.Scope` | `[]` | from `/claim` response `exp` | next refresh window |
| Refill success | `cfg.Scope` | `[]` | unchanged | unchanged |
| Validator fail on scope X | `cfg.Scope \ {X}` | `[X]` | unchanged | unchanged |
| Exit 78 | `[]` | `cfg.Scope` | unchanged | unchanged |
| ErrJTIUnknown | `[]` | `cfg.Scope` | unchanged | unchanged |
| Refresh swap | unchanged | unchanged | new `exp` | next window |
| Boot timeout | `[]` | `cfg.Scope` | zero | zero |

---

## 10. Constants

```go
// bootBackoffInitial is the first interval before the first boot retry.
const bootBackoffInitial = 500 * time.Millisecond

// bootBackoffMultiplier doubles each subsequent interval.
const bootBackoffMultiplier = 2.0

// bootBackoffCap caps any single backoff interval (jittered).
const bootBackoffCap = 30 * time.Second

// bootProbeTimeout is the per-attempt HTTP/probe timeout (≤ 2s per A-026-2).
const bootProbeTimeout = 2 * time.Second

// shutdownGraceTimeout is the SIGTERM honour window before SIGKILL escalation.
const shutdownGraceTimeout = 10 * time.Second

// shutdownHardCeiling is the total Run-exit budget after ctx cancel.
const shutdownHardCeiling = 15 * time.Second

// stderrLineCap is the max bytes per emitted watchdog line; matches SDD-20.
const stderrLineCap = 64 * 1024
```
