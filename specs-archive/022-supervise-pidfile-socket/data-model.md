# Phase 1 — Data Model: SDD-22 PID File + Status Socket

> Types, state machines, JSON wire shape, goroutine inventory, and
> field-by-field Snapshot → FR-12 projection.

---

## 1. New types (this chunk)

### `PidFile` — exported

A handle representing an acquired exclusive flock on a configured filesystem
path plus the file descriptor backing it.

```go
// Lifecycle: AcquirePidFile → (lock held + PID written) → Release
//   (lock dropped + file removed). Single-use; double-Release returns
//   the package-private errAlreadyReleased sentinel (not exported).
type PidFile struct {
    fd   *os.File   // nil after Release
    path string     // configured path, set at Acquire
}
```

State machine:

```text
unacquired (no instance)
   │
   │ AcquirePidFile(path)
   │   ├─ ok                      → acquired
   │   ├─ EWOULDBLOCK              → unacquired + ErrPidLocked
   │   ├─ parent perms loose        → unacquired + ErrSocketPermsLoose
   │   └─ other I/O error           → unacquired + wrapped error
   ▼
acquired (lock held; PID written; mode 0600)
   │
   │ Release()
   │   ├─ ok                      → released
   │   └─ unexpected I/O error    → released-but-error (fd zeroed first;
   │                                  caller informed)
   ▼
released (no inode; flock dropped)
   │
   │ Release()  // safe second call
   ▼
errAlreadyReleased (package-private sentinel, not exported)
```

Source of truth for "live vs stale": the OS-held flock — never the textual
PID inside the file.

---

### `StatusServer` — exported

A long-running listener bound to a Unix domain socket path. For each
accepted connection: takes a single `Store.Snapshot()`, projects to FR-12
JSON, writes the response, force-closes the conn on either request
completion or ctx cancel.

```go
type StatusServer struct {
    socketPath string
    store      *Store        // SDD-19; consumed unmodified
    logger     *slog.Logger

    // Wired post-construction by the orchestrator (SDD-23) via the
    // package-private (*StatusServer).attach. nil pre-attach → zero-value
    // projections in the JSON document (shape-conformant).
    inputs StatusInputs

    // Run lifecycle.
    mu      sync.Mutex
    started bool                            // Run-already-called guard (FR-022-14a)
    conns   map[net.Conn]struct{}           // in-flight handlers tracked under mu
    wg      sync.WaitGroup                  // join all spawned goroutines before Run returns
}
```

State machine:

```text
fresh (NewStatusServer just returned)
   │
   │ Run(ctx)
   │   ├─ second call on this instance        → ErrAlreadyRunning
   │   ├─ parent perms loose                   → ErrSocketPermsLoose
   │   └─ pre-listen ok                        ↓
   ▼
listening
   │   accept loop running; handlers per-conn
   │
   │ ctx cancel  OR  listener-side Accept error
   ▼
draining
   │   listener closed; tracked conns force-closed; handlers exiting
   │   wg.Wait()
   ▼
returned (Run returned nil; instance MUST NOT be Run again — fresh
 NewStatusServer required for rebind)
```

---

### `StatusInputs` — exported (consumer-defined interface)

```go
// StatusInputs is implemented by the orchestrator (SDD-23) and attached
// post-construction via (*StatusServer).attach(StatusInputs). Provides the
// FR-12 fields not held by SDD-19's Snapshot. Defined at the consumer per
// Constitution IX. Implementations MUST be safe for concurrent reads — the
// status server may invoke any getter from any handler goroutine.
type StatusInputs interface {
    // Name is the supervisor's daemon name (FR-12 supervisor field).
    // Empty string when unattached.
    Name() string

    // SessionExpiresAt is the wall-clock expiry of the cached supervisor
    // JWT (FR-12 session_expires_at). Zero time renders as the Go RFC3339
    // zero-value string in JSON.
    SessionExpiresAt() time.Time

    // RefreshWindowNext is the next configured refresh-window opening
    // (FR-12 refresh_window_next). Computed by the orchestrator from
    // refresh_window + the wall clock.
    RefreshWindowNext() time.Time

    // ScopeHealthy returns the current set of names whose validators last
    // succeeded (FR-12 scope_healthy). Returned slice MUST NOT be nil
    // (renders as JSON []); MUST be alphabetical and deduped to keep the
    // wire shape stable.
    ScopeHealthy() []string

    // ScopeStale returns the current set of names whose validators last
    // failed or whose freshness has lapsed (FR-12 scope_stale). Same
    // non-nil + sorted contract as ScopeHealthy.
    ScopeStale() []string

    // LastAuthFailure is the wall-clock time of the most recent vault auth
    // failure (FR-12 last_auth_failure), or nil if none has occurred.
    LastAuthFailure() *time.Time

    // ChildUptime is wall-clock since child process start (FR-12
    // child_uptime). Zero when no child running. The orchestrator computes
    // this — Snapshot.LastTransitionAt is NOT a substitute (see research.md
    // R-1).
    ChildUptime() time.Duration

    // DiscordConnected reports the bot's WebSocket connection status
    // (FR-12 discord_connected).
    DiscordConnected() bool
}
```

---

### `statusJSON` — package-private wire DTO

Holds the exact FR-12 JSON shape with explicit field tags and explicit
string-formatted time/duration projections. Snapshot.Token is intentionally
NOT a field — token bytes never reach the wire (Constitution X / FR-022-13).

```go
type statusJSON struct {
    Supervisor        string   `json:"supervisor"`
    SessionExpiresAt  string   `json:"session_expires_at"`     // time.RFC3339
    RefreshWindowNext string   `json:"refresh_window_next"`    // time.RFC3339
    ScopeHealthy      []string `json:"scope_healthy"`          // never nil → "[]"
    ScopeStale        []string `json:"scope_stale"`            // never nil → "[]"
    LastAuthFailure   *string  `json:"last_auth_failure"`      // RFC3339 or null
    ChildPID          *int     `json:"child_pid"`              // null when no child (PID == 0)
    ChildUptime       string   `json:"child_uptime"`           // time.Duration.String() — e.g., "0s", "8h12m0s"
    DiscordConnected  bool     `json:"discord_connected"`
    State             string   `json:"state"`                  // Snapshot.State string form
}
```

---

### Sentinel errors — exported

```go
var (
    // ErrPidLocked is returned (wrapped) by AcquirePidFile when another
    // live process already holds the configured PID file's flock. The
    // textual PID inside the file is advisory metadata only — the
    // criterion for "stale vs live" is exclusively the OS-held flock.
    // Compare via errors.Is(err, supervise.ErrPidLocked).
    ErrPidLocked = errors.New("supervise: pidfile already locked")

    // ErrSocketPermsLoose is returned (wrapped) by AcquirePidFile and by
    // (*StatusServer).Run when the configured pid_file or status_socket
    // parent directory exists with a mode laxer than 0700. The supervisor
    // refuses to start ("FS perms ARE the auth") — never silently chmods
    // the directory. Compare via errors.Is(err,
    // supervise.ErrSocketPermsLoose).
    ErrSocketPermsLoose = errors.New("supervise: parent directory mode laxer than 0700")

    // ErrAlreadyRunning is returned by (*StatusServer).Run on a second
    // invocation of the same instance — concurrent or sequential. Re-
    // binding requires a fresh StatusServer (FR-022-14a). Compare via
    // errors.Is(err, supervise.ErrAlreadyRunning).
    ErrAlreadyRunning = errors.New("supervise: status server already running")
)
```

Wrapping forms:

```go
fmt.Errorf("supervise: pidfile flock: %w", ErrPidLocked)
fmt.Errorf("supervise: %w (path=%s mode=%v)", ErrSocketPermsLoose, parent, mode.Perm())
fmt.Errorf("supervise: %w", ErrAlreadyRunning)
```

---

## 2. FR-12 ↔ Snapshot + StatusInputs projection table

| FR-12 field | Source | Render |
|---|---|---|
| `supervisor` | `inputs.Name()` (empty string pre-attach) | string |
| `session_expires_at` | `inputs.SessionExpiresAt()` (zero-time pre-attach) | `time.RFC3339` |
| `refresh_window_next` | `inputs.RefreshWindowNext()` (zero-time pre-attach) | `time.RFC3339` |
| `scope_healthy` | `inputs.ScopeHealthy()` (`[]string{}` pre-attach) | JSON array of strings |
| `scope_stale` | `inputs.ScopeStale()` (`[]string{}` pre-attach) | JSON array of strings |
| `last_auth_failure` | `inputs.LastAuthFailure()` (`nil` pre-attach) | `time.RFC3339` string OR `null` |
| `child_pid` | `Snapshot.ChildPID` (0 → render `null`) | int OR `null` |
| `child_uptime` | `inputs.ChildUptime()` (`0` pre-attach) | `time.Duration.String()` |
| `discord_connected` | `inputs.DiscordConnected()` (`false` pre-attach) | bool |
| `state` | `Snapshot.State` (string form, FR-019-1 vocabulary) | string |

The encoder takes ONE `Store.Snapshot()` and ONE pass over `inputs` per
request (FR-022-16 — defensive snapshot, no mid-response re-read).

**Snapshot.Token is not in the table.** It is never marshalled.

---

## 3. Goroutine inventory (Constitution IX)

| Goroutine | Owner | Spawn site | Cancellation | Termination | Recover |
|---|---|---|---|---|---|
| **Accept loop** | `Run`'s caller (= `Run`'s frame) | inline in `Run` (no `go` keyword — `Run` itself is the accept loop) | watcher calls `listener.Close()` | `Accept` returns `net.ErrClosed`; loop returns | not applicable (the accept loop runs in `Run`'s caller goroutine; that goroutine's recover is the orchestrator's concern) |
| **Watcher** | `Run` | `s.wg.Add(1); go s.watch(ctx, listener, done)` inside `Run` after Listen succeeds | `<-ctx.Done()` triggers cleanup; `<-done` lets watcher exit when `Run` returns due to a non-ctx error | both `<-ctx.Done()` and the cleanup work return; `wg.Done()` | top of `watch` (`defer func(){ if r := recover(); r != nil { logger.Error(...) } }()`) |
| **Per-connection handler** | accept loop (parent goroutine) | `s.wg.Add(1); go s.handle(conn)` for each `Accept`-returned conn | watcher's `conn.Close()` (force-close on ctx-done) propagates as Read/Write error | handler returns; `wg.Done()` | top of `handle` |

**Join discipline:** `Run` blocks on `wg.Wait()` after the accept loop
exits, ensuring no goroutine outlives `Run`. SC-022-8 verifies this with
`runtime.NumGoroutine()` before/after across many cycles.

**Counts:** 1 watcher + N handlers per `Run`, where N == in-flight conn
count at ctx-cancel time. Steady-state when no client is connected: 1
(watcher only).

---

## 4. PidFile lifecycle invariants

- `AcquirePidFile` is the ONLY path that creates a `*PidFile` instance.
  Construction without acquire is impossible (the struct is exported but
  has no exported fields and no other constructor).
- After `AcquirePidFile` returns nil error, `os.Stat(path).Mode().Perm() ==
  0o600` and the file's contents are exactly
  `[]byte(strconv.Itoa(os.Getpid()))`.
- The flock is held for the entire lifetime between successful
  `AcquirePidFile` and `Release` — even across `Run`'s lifecycle. The
  orchestrator (SDD-23) holds the `*PidFile` for the supervisor's whole
  lifetime.
- `Release` is the ONLY teardown path. Letting a `*PidFile` go out of
  scope without calling `Release` leaves the inode + flock until process
  exit (the Go runtime will close the fd via finalizer, releasing the
  flock as a side effect — but the inode is NOT removed). Tests assert
  the explicit-`Release` removes the inode.

---

## 5. StatusServer lifecycle invariants

- `NewStatusServer` performs ZERO syscalls — pure value constructor.
- `Run` performs all I/O: parent-dir check, parent-dir create, stale-inode
  unlink, `Listen`, `Chmod`, accept loop, drain.
- `Run` is single-shot per instance (`s.started` guard). Second call
  returns `ErrAlreadyRunning` without binding.
- `Run` returns nil only when ctx was the cause AND every spawned
  goroutine has joined. A non-ctx Accept-side error (e.g., perms drift on
  the parent dir mid-Run — extremely unlikely) returns wrapped.
- `(*StatusServer).attach(inputs StatusInputs)` is package-private. It
  MUST be called before `Run`; calling after `Run` started is a
  programmer error and is detected via a debug assertion (panic in
  tests, ignored in release builds — TBD; conservative implementation:
  `attach` is callable any time, the next request projects through the
  newly-attached inputs).

---

## 6. Concurrency model

| Resource | Guarded by |
|---|---|
| `s.started`, `s.conns` | `s.mu` (`sync.Mutex`) |
| `s.wg` | own internal mutex (stdlib) |
| `Store.Snapshot()` | SDD-19's internal `sync.RWMutex` (already proven race-clean by `TestStore_ConcurrentTransitionsAndSnapshots`) |
| `inputs.*()` | implementation's responsibility — the contract requires concurrency-safe getters |

`-race` clean is a hard gate; tests include parallel start/stop cycles +
concurrent client connections + concurrent transitions on the consumed
Store.

---

## 7. Test fixture types (test-only)

### `stubStatusInputs` (in `socket_test.go`)

Implements `StatusInputs` with field-backed return values; allows tests to
fix wall-clock-derived values to deterministic snapshots.

```go
type stubStatusInputs struct {
    name              string
    sessionExpiresAt  time.Time
    refreshWindowNext time.Time
    scopeHealthy      []string
    scopeStale        []string
    lastAuthFailure   *time.Time
    childUptime       time.Duration
    discordConnected  bool
}

// Implements every StatusInputs getter as a pure field read.
```

### `tempSocketPath(t *testing.T) string` (in `socket_test.go`)

Constructs a per-test temp path under `defaultRuntimeDir()`-derived prefix
using `t.TempDir()` for cleanup. Honours per-OS conventions matching
FR-12 (macOS Library/Caches; Linux $XDG_RUNTIME_DIR or temp).

---

## 8. Field-typed redaction proof obligations (Constitution X)

| Surface | Risk | Proof |
|---|---|---|
| `statusJSON` struct | An accidental `Token *securebytes.SecureBytes` field could marshal token bytes. | The struct definition has NO Token field; lint-level discipline + `TestSocket_TokenInResponseRedacted` marker-byte test asserts no leak. |
| `slog.Info("connection accepted", "peer", conn.RemoteAddr())` | Unix peer address is empty-string for `unix` sockets — no PII. | Tests assert log records contain mode/identifier only. |
| `slog.Error("encode failed", "err", err)` | `err` from json encode could conceivably contain partial buffer state. | Encoder operates on `statusJSON` (no token); buffer state cannot include token bytes. |
| `slog.Warn(...)` on parent-perms refusal | Path + mode are non-secret; safe. | Asserted in test. |

---

**Phase 1 data model: COMPLETE.** Proceed to contracts/api.md.
