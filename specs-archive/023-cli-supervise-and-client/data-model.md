# Phase 1 — Data Model: `hush supervise` + `hush client status` + `hush client refresh`

**Branch**: `023-cli-supervise-and-client` | **Date**: 2026-05-12

This chunk introduces **no new persistent data**. The orchestrator holds
in-memory state for the lifetime of a `hush supervise` invocation; the two
client subcommands hold flag inputs and a single round-trip's request/response
bytes. Every persisted artifact (config TOML, PID file, status socket) is
owned by the underlying chunks (SDD-18, SDD-22).

This document inventories:

1. CLI flag inputs (what cobra parses)
2. Orchestrator in-memory state (what `hush supervise` builds and threads
   through its goroutines)
3. Wire-level data (what crosses the status socket)
4. Internal helper types (file-private, not exported)

---

## 1. CLI flag inputs

### `hush supervise <config-path>`

| Field | Type | Default | Source | Validation |
|---|---|---|---|---|
| `configPath` | `string` (positional) | — | cobra positional arg | non-empty; passed to `supervise/config.Load` |
| `dryRun` | `bool` | `false` | `--dry-run` | none |
| `graceWindow` | `time.Duration` | `0` (sentinel: "use config") | `--grace-window` | if non-zero: `> 0 && ≤ 4h` else `ExitInputErr` |
| `noCache` | `bool` | `false` | `--no-cache` | none |

Global flags (already provided by SDD-14 `addPersistentFlags`):
`--config/-c`, `--verbose/-v`, `--quiet/-q`, `--no-color`. Honored verbatim.

### `hush client status`

| Field | Type | Default | Source | Validation |
|---|---|---|---|---|
| `socketPath` | `string` | `""` | `--socket` | if non-empty: must be a valid absolute path; otherwise empty triggers fall-through to next precedence step |
| `supervisorName` | `string` | `""` | `--supervisor` | if non-empty: path-safe slug `^[a-zA-Z0-9_-]+$` (enforced by `SocketPathForSupervisor`) |
| `jsonOutput` | `bool` | `false` | `--json` | none — overrides TTY auto-detect when true |

### `hush client refresh`

| Field | Type | Default | Source | Validation |
|---|---|---|---|---|
| `socketPath` | `string` | `""` | `--socket` | same as `client status` |
| `supervisorName` | `string` | `""` | `--supervisor` | same as `client status` |

**No** `--json` flag on `refresh` (FR-023-17a explicit).

---

## 2. Orchestrator in-memory state

Held by the body of `superviseRun(cmd *cobra.Command, args []string) error`.
None of these are package-level globals (Constitution IX); every value lives
on the function's stack or in a struct passed by reference into goroutines.

### 2.1 Lifecycle state

| Variable | Type | Source | Lifetime |
|---|---|---|---|
| `cfg` | `*supervise/config.Supervisor` | `config.Load(ctx, configPath)` | full RunE lifetime |
| `logger` | `*slog.Logger` | derived from existing `outputContext` | full RunE lifetime |
| `rootCtx` | `context.Context` | `signal.NotifyContext(cmd.Context(), SIGTERM, SIGINT)` | full RunE lifetime |
| `rootCancel` | `context.CancelFunc` | returned by `NotifyContext` | deferred at RunE exit |
| `pidfile` | `*supervise.PidFile` | `AcquirePidFile(cfg.PIDFile)` | full RunE lifetime; released via `defer pidfile.Release()` after WaitGroup join |
| `store` | `*supervise.Store` | `NewStore(rootCtx, realClock{})` | full RunE lifetime |
| `grace` | `*supervise.Grace` | `NewGrace(effectiveGraceTTL, effectiveCacheEnabled)` | full RunE lifetime |
| `refiller` | `*supervise.Refiller` | `NewRefiller(httpClient, store, logger)` + `attach(grace, eciesPriv, cfg.ServerURL)` | full RunE lifetime |
| `refresher` | `*supervise.Refresher` | `NewRefresher(cfg.RefreshWindow, cfg.RequestedTTL, refiller.Refill, logger)` | full RunE lifetime |
| `statusServer` | `*supervise.StatusServer` | `NewStatusServer(cfg.StatusSocket, store, logger)` + `attach(inputs)` + `attachRefreshHandler(coalescer.Handle)` | full RunE lifetime |
| `inputs` | `*orchestratorInputs` | constructed inline (implements `supervise.StatusInputs`) | full RunE lifetime |
| `coalescer` | `*refreshCoalescer` | constructed inline | full RunE lifetime |
| `child` | `*supervise.Child` | constructed each iteration of the wait loop | one per child generation |
| `wg` | `sync.WaitGroup` | zero value | full RunE lifetime; joins StatusServer and Refresher goroutines |

### 2.2 Effective flag → config projection (post-override)

| Field | Resolution rule |
|---|---|
| `effectiveGraceTTL` | `flag.graceWindow` if non-zero else `cfg.CacheGraceTTL` |
| `effectiveCacheEnabled` | `false` if `flag.noCache` else `cfg.CacheSecretsForRestart` |

These two booleans/durations are the only flag-driven mutations to the
loaded config. They are computed in pure code before any side effect.

### 2.3 `orchestratorInputs` (implements `supervise.StatusInputs`)

Private struct in `supervise.go`. Implements the eight-method interface
declared in `internal/supervise/socket.go`:

```go
type orchestratorInputs struct {
    cfg           *config.Supervisor
    childStartedAt atomic.Pointer[time.Time]   // updated on each Start
    lastAuthFail  atomic.Pointer[time.Time]    // updated on auth-failure transitions
    scopeHealthy  atomic.Pointer[[]string]     // post-refill update
    scopeStale    atomic.Pointer[[]string]     // post-refill update
    sessionExp    atomic.Pointer[time.Time]    // updated when JWT is claimed/refreshed
    refreshNext   atomic.Pointer[time.Time]    // computed from cfg.RefreshWindow + cfg.RequestedTTL
    discordConn   atomic.Bool                   // future SDD-25; defaults true for now
}
```

Each method (`Name`, `SessionExpiresAt`, `RefreshWindowNext`, `ScopeHealthy`,
`ScopeStale`, `LastAuthFailure`, `ChildUptime`, `DiscordConnected`) is a one-line
atomic load. Safe for concurrent reads from any StatusServer handler goroutine
(Constitution IX).

### 2.4 `refreshCoalescer`

Private struct in `supervise.go`. Single-flight for `hush client refresh`
callbacks (R-7, FR-023-22a).

```go
type refreshCoalescer struct {
    mu       sync.Mutex
    inflight *refreshFlight                                   // nil when no refill in flight
    perform  func(ctx context.Context) error                  // injected refill+restart
}
type refreshFlight struct {
    done chan struct{}
    err  error
}
func (c *refreshCoalescer) Handle(ctx context.Context) error { ... }   // attachRefreshHandler payload
```

`perform` is the injected `func(ctx) error` that drives Refill → Forward
SIGTERM → Wait → NewChild + Start. Defined once at RunE entry, captured by
closure. The single-flight inside `Handle` is the entire FR-023-22a
coalescing implementation.

---

## 3. Wire-level data (status socket)

### 3.1 Request payload

A single line terminated by `\n`. Recognised verbs:

| Verb | Behaviour |
|---|---|
| `status\n` (or any unrecognised non-empty payload, or empty payload) | Render `statusJSON` document via `renderStatus(snapshot)`. |
| `refresh\n` | Invoke the orchestrator's `refreshHandler`; write the terminal ack. |

The verb dispatch is implemented in `internal/supervise/socket.go` (R-1).
The "default = status" fallback preserves the SDD-22 §2.5 advisory-payload
backward-compatibility note.

### 3.2 Response payload — status path

The existing SDD-22 `statusJSON` document, unchanged:

```json
{
  "supervisor": "example-daemon",
  "state": "running",
  "session_expires_at": "2026-04-15T06:12:00-07:00",
  "refresh_window_next": "2026-04-15T09:00:00-07:00",
  "scope_healthy": ["ANTHROPIC_API_KEY"],
  "scope_stale": [],
  "last_auth_failure": null,
  "child_pid": 51234,
  "child_uptime": "8h12m0s",
  "discord_connected": true
}
```

Schema authority: `docs/CONFIG-SCHEMA.md §"Client status output schema"`.
Field types and presence locked by SDD-22.

### 3.3 Response payload — refresh path

A single line of JSON, terminated by `\n`. Two shapes:

```json
{"ok":true}
```

```json
{"ok":false,"error":"vault unreachable: dial tcp 100.96.10.4:7743: connect: connection refused"}
```

The `error` field carries the orchestrator's wrapped error message with
secret material stripped. Per Constitution X, error messages identify
the failure mode and any non-secret identifier (scope name, supervisor name,
socket path) but never the secret value.

### 3.4 Client-side decoding

`client status` writes the response bytes verbatim to stdout when in JSON
mode (R-5), or unmarshals into a local Go struct and renders human text
when in TTY mode. Local struct shape (one-to-one with `statusJSON`):

```go
type statusDoc struct {
    Supervisor        string    `json:"supervisor"`
    State             string    `json:"state"`
    SessionExpiresAt  time.Time `json:"session_expires_at"`
    RefreshWindowNext time.Time `json:"refresh_window_next"`
    ScopeHealthy      []string  `json:"scope_healthy"`
    ScopeStale        []string  `json:"scope_stale"`
    LastAuthFailure   *time.Time `json:"last_auth_failure"`
    ChildPID          *int      `json:"child_pid"`
    ChildUptime       string    `json:"child_uptime"`
    DiscordConnected  bool      `json:"discord_connected"`
}
```

`client refresh` decodes into:

```go
type refreshAck struct {
    OK    bool   `json:"ok"`
    Error string `json:"error,omitempty"`
}
```

Both structs are file-private to `client.go`.

---

## 4. Internal helper types (file-private)

### 4.1 `supervise.go`

- `claimPreview` (R-3) — the dry-run canonical payload struct.
- `orchestratorInputs` (§2.3) — implements `supervise.StatusInputs`.
- `refreshCoalescer`, `refreshFlight` (§2.4) — single-flight.
- `realClock` — implements `supervise.Clock` via `time.Now`. One-line type.
- `signalHandler` (optional, only if needed beyond `signal.NotifyContext`) —
  may not be required; revisit at implementation phase.

### 4.2 `client.go`

- `statusDoc` (§3.4) — TTY-rendering target.
- `refreshAck` (§3.4) — refresh terminal-response decode target.
- `errSocketAmbiguous` (sentinel) — auto-detect found 0 or >1 sockets. Mapped
  to `ExitInputErr` via `mapErr` extension.
- `errSocketUnreachable` (sentinel) — dial failed or read timed out. Mapped
  to `ExitErr` via `mapErr` extension.
- `errSupervisorRefused` (sentinel) — refresh ack returned `ok: false`. Wraps
  the supervisor's reason. Mapped to `ExitErr`.

---

## 5. Exit-code mapping additions (`internal/cli/exit_codes.go`)

| Sentinel | Mapped exit | When raised |
|---|---|---|
| `errInvalidGraceWindow` | `ExitInputErr` | `--grace-window` is negative or > 4h |
| `errSocketAmbiguous` | `ExitInputErr` | auto-detect found 0 or > 1 sockets (FR-023-15 (4)) |
| `errSocketUnreachable` | `ExitErr` | dial / read failed or timed out (FR-023-18, FR-023-23) |
| `errSupervisorRefused` | `ExitErr` | refresh ack `ok:false` (FR-023-22) |
| `errDuplicateSupervisor` | `ExitErr` | wraps `supervise.ErrPidLocked` with the "another `hush supervise` is already running" message (AC-10 Scenario 14 / FR-023-6) |
| `supervise.ErrPidLocked` (via wrap) | `ExitErr` | (delegated; the wrapping sentinel above is the operator-facing name) |

Existing `supervise/config` sentinels already mapped to `ExitInputErr` by
`mapErr` (e.g. `ErrRequestedTTLOutOfRange`); no additional `mapErr`
entries needed for config errors.

---

## 6. Relationships and lifetimes

```text
RunE entry
   │
   ├─ Load(cfg)                          ◄── input-error exits here
   ├─ if dryRun → CanonicalJSON, write, return ExitOK
   ├─ AcquirePidFile(cfg.PIDFile)        ◄── ErrPidLocked → errDuplicateSupervisor
   │       defer pidfile.Release()
   │
   ├─ NewStore / NewGrace / NewRefiller / NewRefresher / NewStatusServer
   ├─ statusServer.attach(inputs); statusServer.attachRefreshHandler(coalescer.Handle)
   ├─ wg.Add(1); go statusServer.Run(rootCtx); wg.Done on return
   ├─ wg.Add(1); go refresher.Run(rootCtx); wg.Done on return
   │
   ├─ Initial Refill → if err: state Transition + retry loop OR exit
   ├─ Spawn first child
   │
   ├─ Wait loop (R-8)                    ◄── child exits, transitions, restarts
   │
   ├─ rootCtx fires (SIGTERM/SIGINT)
   ├─ child.Forward(SIGTERM); child.Wait()
   ├─ wg.Wait()                          ◄── join StatusServer + Refresher
   └─ return nil                         ◄── pidfile.Release() runs in defer
```

No goroutine outlives `RunE`. No mutable package-level state. Every
allocation is reachable from RunE's frame and released when the function
returns. Constitution IX clean.
