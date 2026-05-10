# Contracts — SDD-22 Exported API + Wire Shape

> Locked Go signatures, sentinel error semantics, the FR-12 JSON wire
> contract, and the package-private `(*StatusServer).attach` extension
> point. This file is consumed by SDD-23 (orchestrator), SDD-25 (lifecycle
> integration harness), and the Phase-2 task list (`/speckit-tasks`).

---

## 1. Exported Go API (locked)

Path: `github.com/mrz1836/hush/internal/supervise`

### 1.1 PID file

```go
// PidFile is a handle representing an acquired exclusive flock on a
// configured PID-file path plus the file descriptor backing it. Construct
// via AcquirePidFile; release via Release. The zero value is NOT usable.
// Lifecycle: AcquirePidFile → Release. Single-use.
type PidFile struct{ /* opaque */ }

// AcquirePidFile opens (creating if absent) the file at path with mode
// 0600, ensures the parent directory exists at mode 0700 (creating if
// absent; refusing with ErrSocketPermsLoose if the existing parent is
// laxer), then attempts a non-blocking exclusive flock. On success,
// writes the current PID (textual base-10 form) into the file and
// returns a *PidFile. The PID write happens AFTER the lock is held
// (FR-022-4) so a refused acquirer cannot corrupt the live owner's
// record. The criterion for "stale vs live" is exclusively the OS-held
// flock — never the textual PID inside the file (FR-022-3).
//
// Errors:
//   - errors.Is(err, ErrPidLocked):         another live process holds the lock
//   - errors.Is(err, ErrSocketPermsLoose):  parent dir exists with mode laxer than 0700
//   - any other I/O error wrapped with %w
func AcquirePidFile(path string) (*PidFile, error)

// Release drops the flock, closes the underlying fd, and removes the
// file (best-effort — losing the race to a subsequent acquirer is
// acceptable). Safe to call on a *PidFile that has not been Released
// before; calling again on an already-Released *PidFile returns a
// deterministic error.
func (p *PidFile) Release() error
```

### 1.2 Status server

```go
// StatusServer is a Unix-domain status listener. Construct via
// NewStatusServer; drive via Run(ctx). Single-shot Run per instance:
// re-binding after a lifecycle stop requires a fresh StatusServer
// (FR-022-14a).
type StatusServer struct{ /* opaque */ }

// NewStatusServer constructs a fresh StatusServer. Pure value
// constructor — performs ZERO syscalls. Panics if logger is nil
// (Constitution IX startup-wiring exemption). store may be nil (the
// resulting server emits a shape-conformant document with state=""
// and child_pid=null) for unit-testing flexibility, but production
// callers MUST supply a non-nil *Store.
func NewStatusServer(socketPath string, store *Store, logger *slog.Logger) *StatusServer

// Run binds the listener at the configured socketPath and serves status
// requests until ctx is cancelled. Pre-listen, Run:
//   1. ensures the parent directory exists at mode 0700 (creates if
//      missing; refuses with ErrSocketPermsLoose if existing is laxer);
//   2. removes any stale socket inode at the configured path;
//   3. opens net.Listen("unix", socketPath);
//   4. chmods the socket file to mode 0600.
//
// While running, on each accepted connection:
//   - reads one line of request from the client (request payload format
//     is documented in §3 — currently a single literal "status\n");
//   - takes a single Store.Snapshot() (FR-022-16);
//   - encodes the FR-12 JSON document (§2);
//   - writes the response, then closes the connection.
//
// On ctx cancel:
//   - the listener is closed (unblocking the accept loop);
//   - every in-flight per-connection handler has its conn force-closed
//     (unblocking any blocked Read/Write);
//   - Run returns nil within a small fixed sub-second bound (FR-022-14
//     / Clarification 3);
//   - every goroutine spawned by Run has joined before Run returns.
//
// Errors:
//   - errors.Is(err, ErrAlreadyRunning):      Run was previously called
//                                              on this instance (FR-022-14a)
//   - errors.Is(err, ErrSocketPermsLoose):    parent dir mode laxer than 0700
//   - nil:                                     ctx-cancelled clean shutdown
//   - any other I/O error wrapped with %w
func (s *StatusServer) Run(ctx context.Context) error
```

### 1.3 Sentinel errors

```go
// All exported package-level var per Constitution IX. Identifiable via
// errors.Is.
var (
    ErrPidLocked        = errors.New("supervise: pidfile already locked")
    ErrSocketPermsLoose = errors.New("supervise: parent directory mode laxer than 0700")
    ErrAlreadyRunning   = errors.New("supervise: status server already running")
)
```

Wrapping conventions:

```go
fmt.Errorf("supervise: pidfile flock: %w", ErrPidLocked)
fmt.Errorf("supervise: %w (path=%s mode=%v)", ErrSocketPermsLoose, parent, mode.Perm())
fmt.Errorf("supervise: %w", ErrAlreadyRunning)
```

---

## 2. FR-12 Status JSON wire contract

Every accepted connection receives ONE response, exactly conforming to
this shape. No fields are added by this chunk; no fields are dropped
(FR-022-12).

### 2.1 Field schema

| Field | Go type | JSON type | Format |
|---|---|---|---|
| `supervisor` | string | string | non-empty when attached; `""` pre-attach |
| `session_expires_at` | string | string | RFC3339 timestamp; pre-attach renders zero-time RFC3339 string `"0001-01-01T00:00:00Z"` |
| `refresh_window_next` | string | string | RFC3339 timestamp; same zero-rendering as above |
| `scope_healthy` | []string | array of strings | never `null` — empty renders `[]`; sorted ascending; deduped |
| `scope_stale` | []string | array of strings | never `null` — empty renders `[]`; sorted ascending; deduped |
| `last_auth_failure` | *string | string OR null | RFC3339 string when non-nil; `null` otherwise |
| `child_pid` | *int | int OR null | integer when `Snapshot.ChildPID > 0`; `null` when `0` |
| `child_uptime` | string | string | `time.Duration.String()` — e.g. `"0s"`, `"8h12m0s"` |
| `discord_connected` | bool | bool | `true` / `false` |
| `state` | string | string | one of `"fetching"`, `"running"`, `"awaiting-approval"`, `"grace-restart"`, `"stopped"` (FR-019-1 vocabulary); pre-attach with `store == nil` renders `""` |

### 2.2 Reference-shape example (matches `docs/SPEC.md` FR-12)

```json
{
  "supervisor": "openclaw",
  "session_expires_at": "2026-04-15T06:12:00-07:00",
  "refresh_window_next": "2026-04-15T09:00:00-07:00",
  "scope_healthy": ["ANTHROPIC_API_KEY", "OPENAI_API_KEY"],
  "scope_stale": [],
  "last_auth_failure": null,
  "child_pid": 51234,
  "child_uptime": "8h12m0s",
  "discord_connected": true,
  "state": "running"
}
```

### 2.3 Pre-attach (default) shape — for SDD-22 unit-test fixtures

When `(*StatusServer).attach` has never been called and `store == nil`:

```json
{
  "supervisor": "",
  "session_expires_at": "0001-01-01T00:00:00Z",
  "refresh_window_next": "0001-01-01T00:00:00Z",
  "scope_healthy": [],
  "scope_stale": [],
  "last_auth_failure": null,
  "child_pid": null,
  "child_uptime": "0s",
  "discord_connected": false,
  "state": ""
}
```

This shape is shape-conformant with FR-12 — every field present, every
type matches — but semantically empty. SDD-23 (orchestrator) calls
`attach` before `Run` to provide live values.

### 2.4 Anti-fields (forbidden)

The following fields MUST NOT appear in the JSON document, ever:

- `token` — Snapshot.Token bytes never reach the wire (Constitution X /
  FR-022-13).
- Any `*SecureBytes` field — by construction, the encoder only marshals
  the `statusJSON` DTO which has no `*SecureBytes` field.
- Any auth header field, bearer prefix, HMAC signature, signed cookie —
  Constitution V; the kernel's path-based check is the entire auth
  contract.

### 2.5 Wire framing

- Request: a single ASCII line `status\n`. Future extensions (e.g.
  request scoped to a sub-resource) MAY add other verbs in later chunks
  — for SDD-22, only `status` is recognised. Unrecognised request
  strings: server returns FR-12 JSON regardless (the request payload is
  advisory in v0.1.0; the connection IS the auth).
- Response: a single line — the JSON document followed by `\n`. The
  server then closes the connection.
- No length-prefix, no chunked framing, no HTTP, no TLS, no bearer.

---

## 3. Package-private extension point

```go
// StatusInputs is the consumer-defined seam for FR-12 fields not held by
// SDD-19's Snapshot. Defined at the consumer (this package) per
// Constitution IX. Implementations MUST be safe for concurrent reads —
// the status server may invoke any getter from any handler goroutine.
//
// Pre-attach (the StatusServer's inputs field is nil), the document
// renders zero values for the fields below. SDD-23 (orchestrator)
// implements this and calls (*StatusServer).attach once before Run.
type StatusInputs interface {
    Name() string                  // FR-12 supervisor
    SessionExpiresAt() time.Time   // FR-12 session_expires_at
    RefreshWindowNext() time.Time  // FR-12 refresh_window_next
    ScopeHealthy() []string        // FR-12 scope_healthy (non-nil, sorted, deduped)
    ScopeStale() []string          // FR-12 scope_stale  (non-nil, sorted, deduped)
    LastAuthFailure() *time.Time   // FR-12 last_auth_failure (nullable)
    ChildUptime() time.Duration    // FR-12 child_uptime
    DiscordConnected() bool        // FR-12 discord_connected
}

// attach wires inputs into the status server. Package-private; called by
// the orchestrator (SDD-23) from inside package supervise. Honours the
// locked 3-arg NewStatusServer constructor (Complexity Tracking row 2 in
// plan.md). Mirrors SDD-21's (*Refiller).attach precedent.
func (s *StatusServer) attach(inputs StatusInputs)
```

---

## 4. Anti-API (deliberately NOT exported)

The following are forbidden additions to the chunk's exported surface
(these would either break the locked SDD-22 API list or violate
Constitution IX/V/X):

- `WithStatusInputs`, `SetInputs`, `WithLogger`, `WithStore`, or any
  functional-options or builder pattern on the constructor.
- `(*StatusServer).Stop()` / `Shutdown()` — ctx is the only stop signal.
- `(*StatusServer).Restart()` — single-shot is the contract.
- `(*PidFile).IsAcquired() bool`, `(*PidFile).PID() int`,
  `(*PidFile).Path() string` — internal accessors, not part of the
  exported surface; tests use file inspection on the path operator the
  caller already knows.
- `var ErrAlreadyReleased` — package-private; double-Release is a
  programmer error, not a control-flow input.
- `func ListenStatus(...)` — global entry point. Constitution IX forbids
  package-level mutable state and globals.
- An `init()` function in any of the new files.

---

## 5. Test contract

All test names use the project's `TestSubject_Scenario` underscore-
delimited convention (matching the existing supervise package precedent
locked by SDD-19/20/21 tests).

### 5.1 `pidfile_test.go`

| Test | FR/SC | Asserts |
|---|---|---|
| `TestPidFile_FlockExclusive` | FR-022-1 | `LOCK_EX|LOCK_NB` semantics — first holder owns the lock |
| `TestPidFile_DuplicateRefused` | FR-022-2, SC-022-2 | second acquire returns `errors.Is(err, ErrPidLocked)`; first owner unchanged |
| `TestPidFile_StaleAcquired` | FR-022-3, SC-022-3 | sub-process acquires-then-exits; new acquirer succeeds without intervention |
| `TestPidFile_ReleaseRemovesFile` | FR-022-5 | post-Release, `os.Stat` returns ErrNotExist |
| `TestPidFile_WritesOwnPID` | FR-022-4 | post-Acquire, file contents == `strconv.Itoa(os.Getpid())` |
| `TestPidFile_Mode0600` | FR-022-6 | post-Acquire, mode `0o600` |
| `TestPidFile_ParentMode0700Created` | FR-022-10 | missing parent → created at `0o700` |
| `TestPidFile_ParentLooseRefuses` | FR-022-10 | existing `0o755` parent → `errors.Is(err, ErrSocketPermsLoose)` |

### 5.2 `socket_test.go`

| Test | FR/SC | Asserts |
|---|---|---|
| `TestSocket_Mode0600` | FR-022-9, SC-022-4 | post-Run, socket inode mode `0o600` |
| `TestSocket_ParentMode0700` | FR-022-10, SC-022-4 | missing parent → created at `0o700` |
| `TestSocket_ParentLooseRefuses` | FR-022-10 | existing `0o755` parent → `errors.Is(err, ErrSocketPermsLoose)`; no listener bound |
| `TestSocket_StatusJSONShape` | FR-022-12, SC-022-5 | every FR-12 field name + Go type present |
| `TestSocket_StatusJSONFromSnapshot` | FR-022-12, FR-022-16 | byte-equal expected JSON given known Store + stub StatusInputs |
| `TestSocket_TokenInResponseRedacted` | FR-022-13, SC-022-6 | marker bytes in `Snapshot.Token` never appear in response body |
| `TestSocket_PreAttachDefaultsRenderShapeConformant` | FR-022-12 | unattached server emits the §2.3 default-shape JSON |
| `TestSocket_GracefulShutdownOnCtx` | FR-022-14, SC-022-7 | ctx cancel → `Run` returns `nil` within sub-second bound |
| `TestSocket_ConnectionForceClosedOnCtxCancel` | FR-022-14, Clarification 3 | mid-handler ctx cancel → conn force-closed; `Run` returns within sub-second bound |
| `TestSocket_PreviousSocketCleanedUp` | FR-022-11 | stale inode at path is unlinked pre-bind |
| `TestSocket_RebindAfterStop` | SC-022-7 | fresh `StatusServer` rebinds same path post-Run |
| `TestSocket_RunSecondCallReturnsErrAlreadyRunning` | FR-022-14a | second `Run` on same instance → `errors.Is(err, ErrAlreadyRunning)` |
| `TestSocket_NoGoroutineLeak` | FR-022-17, SC-022-8 | `runtime.NumGoroutine()` before/after start/stop cycles equal |
| `TestSocket_NoTCPListenerOrHTTPServer` | SC-022-9 | static-grep absence of `net.Listen("tcp"`, `http.Server`, bearer/authorization tokens in chunk source files |

Coverage gate: `go test -race -cover ./internal/supervise/ -run
"PidFile|Socket"` ≥ 95% (SC-022-10).

---

## 6. Integration contract (downstream chunks)

- **SDD-23 (orchestrator)**: constructs `*PidFile` at supervisor start
  via `AcquirePidFile(cfg.PIDFile)`; `Release` at supervisor stop.
  Constructs `*StatusServer` via `NewStatusServer(cfg.StatusSocket,
  store, logger)`; calls package-private `attach(inputs)` with its own
  `StatusInputs` impl; runs under the supervisor's lifecycle ctx.
  Translates the chunk's sentinel errors into operator-visible exit
  codes per `docs/SPEC.md` §CLI exit codes.
- **SDD-25 (lifecycle integration harness)**: drives AC-10 Lifecycle
  Scenarios 12 (agent status check) and 14 (duplicate supervisor) end-
  to-end against a real binary. SDD-22's unit-level success criteria
  (SC-022-1) are independent of SDD-25's integration assertions.
- **No other chunks** import `internal/supervise.PidFile`,
  `StatusServer`, or any chunk-22 sentinel.

---

**Phase 1 contracts: COMPLETE.**
