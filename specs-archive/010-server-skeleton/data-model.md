# Phase 1 Data Model — internal/server

Entities the chassis owns. Each entry: shape, fields, invariants,
state transitions where applicable.

---

## Server

The chassis instance. Constructed once via `New(deps Deps)`, run
once via `Run(ctx)`, shut down once via cancellation of `ctx`.

**Fields (unexported):**

| Field | Type | Purpose |
|-------|------|---------|
| `cfg` | `*config.Server` | The validated TOML server config (read-only) |
| `vaultPtr` | `*atomic.Pointer[vault.Store]` | Shared with handlers; swapped on reload |
| `tokenStore` | `token.Store` | Passed to handlers via per-request context |
| `approver` | `Approver` | Used by SDD-12's claim handler |
| `logger` | `*slog.Logger` | Single redacting logger; never mutated |
| `audit` | `AuditWriter` | Emits security-relevant events |
| `clock` | `func() time.Time` | Defaults to `time.Now`; injectable for tests |
| `clockProbe` | `func(ctx) (bool, time.Duration, error)` | Defaults to host probe |
| `mux` | `*http.ServeMux` | Stdlib router rooted at `/h/<prefix>/...` |
| `httpServer` | `*http.Server` | The std listener wrapper; created during `Run` |
| `reloadMu` | `sync.Mutex` | Serialises reloads (FR-014) |
| `drainWG` | `sync.WaitGroup` | Tracks active drain goroutines (FR-025) |
| `shuttingDown` | `atomic.Bool` | Signals SIGHUP loop to ignore (FR-015) |

**Invariants:**

- `vaultPtr.Load()` is never nil after `New` returns successfully.
- `cfg`, `vaultPtr`, `tokenStore`, `approver`, `logger`, `audit`
  are non-nil after `New` returns.
- Lifetime: `Run` may only be called once per `Server`. A second
  call returns `ErrAlreadyRun`.
- `ctx` is never stored in the struct (Constitution IX); only the
  derived `shutdownCtx` lives inside the closure that calls
  `httpServer.Shutdown`.

---

## Deps

The dependency-injection bundle. Locked surface — see plan.md.

| Field | Type | Required? | Default |
|-------|------|-----------|---------|
| `Cfg` | `*config.Server` | yes | — |
| `VaultPtr` | `*atomic.Pointer[vault.Store]` | yes | — |
| `TokenStore` | `token.Store` | yes | — |
| `Approver` | `Approver` | yes | — |
| `Logger` | `*slog.Logger` | yes | — |
| `AuditWriter` | `AuditWriter` | yes | — |
| `Clock` | `func() time.Time` | no | `time.Now` |
| `ClockSyncProbe` | `func(ctx) (bool, time.Duration, error)` | no | platform-default host probe |

**Validation (in `New`):**

Every required field's nil case maps to a distinct sentinel:

```go
ErrMissingConfig       // Cfg
ErrMissingVaultPtr     // VaultPtr (or VaultPtr.Load() returns nil)
ErrMissingTokenStore   // TokenStore
ErrMissingApprover     // Approver
ErrMissingLogger       // Logger
ErrMissingAuditWriter  // AuditWriter
```

`New` performs zero I/O — only nil-checks and direct field
assignment (FR-027).

---

## Approver, ApprovalRequest, Decision

Declared in `approver.go`. Consumed by SDD-12's claim handler;
implemented by SDD-11's `BotApprover`.

```go
type Approver interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}

type ApprovalRequest struct {
    RequestID    string        // chassis-assigned request ID, for correlation
    MachineName  string        // from /claim payload
    ClientIP     netip.Addr    // socket-level peer address
    Scope        []string      // requested secret names
    Reason       string        // human-readable reason from /claim
    SessionType  SessionType   // Interactive | Supervisor
    RequestedTTL time.Duration // upper-bounded by config
    Metadata     map[string]string // open extension surface for SDD-11
}

type Decision struct {
    Approved   bool
    DeniedAt   time.Time     // zero when Approved
    ApprovedAt time.Time     // zero when !Approved
    GrantedTTL time.Duration // may be < RequestedTTL
    ApproverID string        // who approved/denied (Discord user ID)
    Reason     string        // optional free-text reason on denial
}

type SessionType uint8
const (
    SessionInteractive SessionType = iota + 1
    SessionSupervisor
)
```

**Invariants:**

- An `Approver` implementation MUST be safe for concurrent use —
  the chassis may invoke it from multiple request goroutines.
- `RequestApproval` returning `(Decision{Approved: false}, nil)` is
  a valid denial; returning `(Decision{}, ctx.Err())` is a clean
  cancellation. Returning `(Decision{}, otherErr)` is a transport
  failure (Discord unreachable, etc.).
- `Metadata` is opaque to the chassis. SDD-11 may use it to carry
  Discord-specific state (e.g. `ephemeral_pubkey_fingerprint`) but
  the chassis never reads it.

---

## AuditEvent, AuditWriter

Declared in `approver.go` (or split into `audit.go` if file size
demands; chunk contract pins five files but allows additional
small files for clarity).

```go
type AuditWriter interface {
    Write(ctx context.Context, event AuditEvent) error
}

type AuditEvent struct {
    Type      AuditEventType  // see SPEC FR-14 list
    At        time.Time
    RequestID string
    ClientIP  netip.Addr
    Detail    map[string]string // structured, never any secret/body byte
}

type AuditEventType string
const (
    AuditServerStart            AuditEventType = "server_start"
    AuditServerStop             AuditEventType = "server_stop"
    AuditVaultReloaded          AuditEventType = "vault_reloaded"
    AuditFilePermCheckFailed    AuditEventType = "file_perm_check_failed"
    AuditAuthFailedNotAllowed   AuditEventType = "auth_failed" // IP allow-list
    AuditPanicCaptured          AuditEventType = "panic_captured"
    // additional event types added by SDD-12, SDD-13, SDD-11
)
```

**Invariants:**

- `Detail` MUST NEVER carry a request body byte, a vault byte
  (cipher or plain), or a key byte. The chassis logs only categories,
  identifiers, and counters.
- The chassis emits at most these event types directly:
  `AuditServerStart`, `AuditServerStop`, `AuditVaultReloaded`,
  `AuditAuthFailedNotAllowed`, `AuditPanicCaptured`. Other
  event types are emitted by SDD-12/13.

---

## StartupCheck

A single named verification. Declared in `startup_checks.go`.

```go
type StartupCheck struct {
    Name string                          // human-readable, stable
    Run  func(context.Context) error     // returns sentinel-wrapped error on failure
}
```

**Ordered sequence (lock):**

```go
func (s *Server) startupChecks() []StartupCheck {
    return []StartupCheck{
        {Name: "clock_sync",     Run: s.checkClockSync},
        {Name: "file_modes",     Run: s.checkFileModes},
        {Name: "tailscale_bind", Run: s.checkTailscaleBind},
        {Name: "state_dir",      Run: s.checkStateDir},
    }
}
```

**Sentinel errors (one per check):**

```go
var (
    ErrClockUnsynchronised = errors.New("server: startup: clock unsynchronised")
    ErrFileModeLoose       = errors.New("server: startup: file mode laxer than 0600/0700")
    ErrBindNotOnTailscale  = errors.New("server: startup: listen address not on Tailscale CGNAT")
    ErrStateDirUnsafe      = errors.New("server: startup: state directory missing or unsafe")
)
```

**Short-circuit rule:** `Run` iterates the slice in order; the
first non-nil error returns immediately (FR-003). The remaining
checks are not invoked.

---

## Reload coordinator state

The `reload.go` file owns this state machine. There is at most
one active reload at a time (FR-014).

```
              ┌───────────┐
              │   Idle    │◄───────────────────┐
              └─────┬─────┘                    │
            SIGHUP / ReloadVault                │
                    ▼                           │
              ┌───────────┐ load fails        │
              │  Loading  │─────► (Idle, error returned, log)
              └─────┬─────┘
              load ok
                    ▼
              ┌───────────┐
              │  Swapped  │  vaultPtr.Store(new); spawn drain goroutine
              └─────┬─────┘
                    ▼
              ┌───────────┐  drain timer expires OR shutdown deadline
              │ Draining  │─────► oldStore.Destroy()
              └─────┬─────┘
                    ▼
                  Idle
```

**Concurrency:**

- `reloadMu` (a `sync.Mutex`) is held for the entire `Loading →
  Swapped` transition. It is released *before* the drain goroutine
  starts (so the next reload can begin loading while the previous
  drain is still ticking down — but the next reload's swap waits
  on the previous drain's WaitGroup token if the drain has not
  completed; see FR-014).
- Implementation: each reload acquires `reloadMu`, performs Load
  + Swap, releases the mutex, and starts the drain goroutine.
  Before releasing the mutex it `s.drainWG.Add(1)`. A *new*
  reload that starts while a drain is in progress goes through
  the mutex normally — the new swap can happen even if the
  previous drain is still running, because `vault.Store.Destroy`
  is idempotent and each drain owns a private `oldStore`
  reference. FR-014 is satisfied because `vaultPtr.Swap` is
  atomic and each drain's `oldStore` is stale by the time the
  next reload runs.

**Wait — clarification of FR-014 concurrency:** the spec says
"reloads MUST be serialised — the new reload does not begin until
the previous reload's swap and destroy have completed." This is
stricter than the implementation sketched above. The implementation
MUST therefore hold `reloadMu` across the entire drain too: the
mutex is released only when `oldStore.Destroy()` returns. This
costs throughput (a SIGHUP-storm waits 30 s × N), but it is the
literal contract the spec locks. Tests will assert this.

```go
// pseudocode
func (s *Server) runReload(ctx context.Context, newPath string, key *securebytes.SecureBytes) error {
    s.reloadMu.Lock()
    defer s.reloadMu.Unlock()

    if s.shuttingDown.Load() {
        return ErrShuttingDown
    }
    newStore, err := vault.Load(ctx, newPath, key)
    if err != nil {
        return wrapReloadError(err) // categorised sentinel
    }
    oldStore := s.vaultPtr.Swap(&newStore)
    s.audit.Write(ctx, AuditEvent{Type: AuditVaultReloaded, At: s.clock(), ...})

    // Drain inline (under mutex) so FR-014 holds: next reload waits.
    s.drainWG.Add(1)
    defer s.drainWG.Done()
    select {
    case <-time.After(s.cfg.ReloadDrainWindow):
    case <-ctx.Done():
    }
    if oldStore != nil {
        if err := (*oldStore).Destroy(); err != nil {
            s.logger.ErrorContext(ctx, "vault destroy after drain", "err", err)
        }
    }
    return nil
}
```

The drain runs *under the mutex*. SIGHUP-storm throughput is a
non-goal for v0.1.0; the operator does not rotate secrets at
megahertz.

---

## RequestID context key

A package-private typed key:

```go
type requestIDKeyType struct{}
var requestIDKey = requestIDKeyType{}

func RequestID(ctx context.Context) string {
    if v, ok := ctx.Value(requestIDKey).(string); ok {
        return v
    }
    return ""
}
```

**Invariants:**

- `RequestID` returns `""` when the context did not pass through
  the chassis middleware (e.g. a unit test that builds the
  context manually). Handlers must not rely on this never being
  empty in production — but in production it is always a 32-char
  hex string, because no request reaches a handler without
  passing the request-ID middleware first.

---

## Configuration consumed (read-only)

The chassis reads these fields from `*config.Server` and never
writes to them:

| Field | From | Used by |
|-------|------|---------|
| `Server.ListenAddr` | TOML `[server].listen_addr` | `Run` (httpServer bind), `checkTailscaleBind` |
| `Server.PathPrefix` | TOML `[server].path_prefix` | `router.go` mount |
| `Server.StateDir` | TOML `[server].state_dir` | `checkFileModes`, `checkStateDir` |
| `Network.AllowedCIDRs` | TOML `[network].allowed_cidrs` | IP allow-list middleware |
| `Network.HealthBind` | TOML `[network].health_bind` | not bound by SDD-10 — SDD-13 attaches `/hz` to the same mux for v0.1.0 (the secondary bind is a future extension) |
| `Security.RequireNTPSync` | TOML `[security].require_ntp_sync` | `checkClockSync` (skip if false — but default is true) |
| `Security.MaxClockDrift` | TOML `[security].max_clock_drift` | `checkClockSync` |
| `Security.RequireFileModeChecks` | TOML `[security].require_file_mode_checks` | `checkFileModes` (skip if false) |

**New config fields needed (additions to `config.Server` —
forward-compat: SDD-06 already shipped, so SDD-10 introduces these
as a follow-up to `internal/config` if not present):**

| Field | Default | Purpose |
|-------|---------|---------|
| `Server.ReloadDrainWindow` | 30 s | drain time between swap and destroy |
| `Server.ShutdownTimeout`  | 30 s | graceful-shutdown deadline |
| `Server.ReadHeaderTimeout` | 10 s | hardening default for `http.Server` |
| `Server.ReadTimeout` | 30 s | hardening default |
| `Server.WriteTimeout` | 30 s | hardening default |
| `Server.IdleTimeout` | 60 s | hardening default |

If `internal/config` does not yet expose these, the chassis
introduces minimal `Cfg`-aware defaults via constants (`var
DefaultReloadDrainWindow = 30 * time.Second`, etc.) and reads
them through accessor functions that fall back to the constant
when the config field is absent. The implementation chunk will
either land the config additions in the same chunk or document
the follow-up under `Tasks` (Phase 2).
