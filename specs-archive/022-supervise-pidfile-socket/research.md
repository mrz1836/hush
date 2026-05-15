# Phase 0 — Research: SDD-22 PID File + Status Socket

**Status:** all NEEDS CLARIFICATION items from plan.md Technical Context
resolved.

This document records every non-obvious design decision required to honour
the SDD-22 locked exported API + the spec's Clarifications + every functional
requirement under Constitution V / VIII / IX / X. Each section is structured
as **Decision → Rationale → Alternatives Considered**.

---

## R-1. Bridging the FR-12 ↔ SDD-19 `Snapshot` field gap

**Question:** FR-022-12 requires the status JSON to include all 10 FR-12
fields (`supervisor`, `session_expires_at`, `refresh_window_next`,
`scope_healthy`, `scope_stale`, `last_auth_failure`, `child_pid`,
`child_uptime`, `discord_connected`, `state`). SDD-19's locked
`Snapshot` only carries 5 fields (`State`, `ChildPID`, `LastTransitionAt`,
`Token`, `Reason`). The locked `NewStatusServer(socketPath, store, logger)`
signature provides no other input. How is the gap bridged?

**Decision:** introduce a consumer-defined `StatusInputs` interface and a
package-private `(*StatusServer).attach(StatusInputs)` post-construction
wiring method. Pre-attach state produces a shape-conformant document with
zero values; SDD-23 (orchestrator) constructs a `StatusServer`, calls
`attach` once with its own `StatusInputs` impl, then `Run`. SDD-22's tests
provide a stub `StatusInputs` impl and assert the full FR-12 shape with
realistic values.

```go
// StatusInputs is the consumer-defined seam for FR-12 fields not held by
// SDD-19's Snapshot. Wired post-construction via (*StatusServer).attach.
// Defined at the consumer per Constitution IX. Pre-attach default behaviour
// (statusServer.inputs == nil) renders zero values for these fields.
type StatusInputs interface {
    Name() string                        // FR-12 supervisor
    SessionExpiresAt() time.Time         // FR-12 session_expires_at
    RefreshWindowNext() time.Time        // FR-12 refresh_window_next
    ScopeHealthy() []string              // FR-12 scope_healthy (alphabetical, deduped, non-nil)
    ScopeStale() []string                // FR-12 scope_stale  (alphabetical, deduped, non-nil)
    LastAuthFailure() *time.Time         // FR-12 last_auth_failure (nullable)
    ChildUptime() time.Duration          // FR-12 child_uptime
    DiscordConnected() bool              // FR-12 discord_connected
}
```

**Rationale:**
- The locked 3-arg `NewStatusServer` constructor is part of the chunk's
  exported API contract (SDD-22.md, downstream chunks) and cannot change.
- The `attach` pattern is precedented in this very package: SDD-21's
  `Refiller.attach(grace, priv, serverURL)` solves the same problem with the
  same constraint (locked 3-arg `NewRefiller`). PACKAGE-MAP.md SDD-21 entry
  documents the pattern; CLAUDE.md SDD-21 chunk-doc references it explicitly.
- An interface (rather than a concrete value) defined at the consumer
  satisfies Constitution IX ("Define interfaces at the consumer, not the
  producer. Prefer single-method interfaces.") — and lets tests use a
  one-off stub without importing SDD-23.
- Pre-attach defaults (`inputs == nil` → emit zero/empty values) mean
  unit tests in this chunk can assert wire-shape conformance with and
  without a `StatusInputs` attached, satisfying SC-022-1 (Scenario 12 has a
  passing test in this chunk, not deferred to SDD-25).
- The 8 fields chosen for `StatusInputs` are exactly those NOT in SDD-19's
  `Snapshot`. `state` and `child_pid` come from `Snapshot.State` /
  `Snapshot.ChildPID`. `child_uptime` is sourced from `StatusInputs` rather
  than computed from `Snapshot.LastTransitionAt` because LastTransitionAt
  resets on every state transition (e.g., on `EventChildExitClean → silent
  refill`, the Running-state stamp is overwritten); only the orchestrator
  knows the *child*'s start time.

**Alternatives considered:**
1. **Extend SDD-19's `Snapshot` with the 8 missing fields.** Rejected:
   PACKAGE-MAP.md and SDD-22 both forbid altering SDD-19 ("Anti-API ...
   `SetToken` ... per-field accessors"). Touching SDD-19 would also touch
   `state.go` test coverage and require re-locking the Snapshot type for
   downstream consumers — out of scope for this chunk.
2. **Take a 4th constructor arg `inputs StatusInputs`.** Rejected: the
   SDD-22 chunk doc explicitly locks the 3-arg constructor signature.
   Downstream chunks (SDD-23, SDD-25) wire against the locked signature.
3. **Render only `state` + `child_pid` and emit constant zero/empty for the
   other 8 fields permanently.** Rejected: makes Scenario 12 (agent reads
   meaningful status) impossible to verify in this chunk; SC-022-1 requires
   passing test in *this* chunk.
4. **Functional-options pattern (`StatusOption ...func`)** added to the
   constructor. Rejected: extends the constructor signature in spirit (now
   variadic), and the SDD-21 attach precedent is more explicit about the
   single wiring point.
5. **Concrete `StatusInputs` struct (value type) instead of an interface.**
   Rejected: a value snapshot would be stale within a single Run; the
   server must re-read every field per request to satisfy FR-022-16
   (defensive snapshot at request time). An interface lets the orchestrator
   plug in live getters that consult its own state under its own locks.

**Anti-API:** no exported `SetInputs`, no `WithStatusInputs(...)` option, no
package-level mutable global. The `attach` method is package-private
(lowercase) — orchestrator wires it from inside `package supervise` only.

---

## R-2. Contents of `socket_darwin.go` and `socket_linux.go`

**Question:** SDD-22's locked Files list includes `socket_darwin.go
(platform path resolution)` and `socket_linux.go (platform path
resolution)`. SDD-18's `config/` package already does ~ expansion and per-OS
default selection for `status_socket` and `pid_file`. What goes in the
build-tagged shims at runtime?

**Decision:** the build-tagged shims hold a per-OS `defaultRuntimeDir()
string` helper used **only by tests** to construct an absolute, OS-
conventional, mode-`0700`-creatable temporary directory under which the
unit-test fixtures bind their socket / pidfile. The production `socket.go`
and `pidfile.go` consume the configured absolute path verbatim — they do
NOT branch on `runtime.GOOS`.

```go
// socket_darwin.go
//go:build darwin
package supervise
import "os"
func defaultRuntimeDir() string {
    if cache, err := os.UserCacheDir(); err == nil {
        return cache + "/hush"
    }
    return os.TempDir()
}
```

```go
// socket_linux.go
//go:build linux
package supervise
import "os"
func defaultRuntimeDir() string {
    if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
        return xdg
    }
    return os.TempDir()
}
```

**Rationale:**
- Honours the SDD-22 locked Files list verbatim.
- Avoids duplicating SDD-18's responsibilities (config-package owns the
  path-resolution rules per the spec's Assumptions section).
- Production `socket.go` is platform-agnostic — Unix domain sockets, flock,
  chmod, mkdir all behave identically on darwin and linux (modulo the syscall
  ABI which `golang.org/x/sys/unix` already abstracts).
- Tests can construct paths matching the `docs/SPEC.md` FR-12 conventions
  (macOS: `~/Library/Caches/hush/...`; Linux: `$XDG_RUNTIME_DIR/...`)
  without inventing a runtime path-resolution layer in this chunk.
- `defaultRuntimeDir` is package-private — never exported; tests reference
  it only inside `socket_test.go`.

**Alternatives considered:**
1. **Empty shim files with only a build tag and a comment.** Rejected:
   `golangci-lint`'s `unused` checker would flag the file as deadweight; the
   test helper gives them a real, narrow purpose.
2. **Per-OS production branching in `socket.go` (e.g., default-socket-path
   logic).** Rejected: violates spec's "this chunk does not redefine the
   path-resolution rules" — config-package owns that.
3. **A single `socket_unix.go` with `//go:build darwin || linux`.** Rejected:
   the SDD-22 chunk doc explicitly locks two separate files, one per OS.

---

## R-3. Sub-second graceful shutdown with in-flight connection force-close

**Question:** FR-022-14 + Clarification 3 require `Run` to return within a
small fixed sub-second bound on ctx-cancel, with in-flight per-connection
handlers force-closed via `conn.Close()` rather than waiting for the remote
to cooperate. How is this implemented without leaking goroutines (Constitution
IX) and without races (`magex test:race` clean)?

**Decision:**

1. `Run` opens `net.Listen("unix", path)` (after pre-listen prep — see R-4).
2. `Run` spawns 1 *watcher* goroutine that blocks on `<-ctx.Done()`. On wake:
   it calls `listener.Close()` (unblocks the accept loop) and then ranges a
   `map[net.Conn]struct{}` under a mutex calling `conn.Close()` on each
   active connection (unblocks each handler's Read/Write).
3. `Run` runs the *accept loop* inline (no extra goroutine — `Run`'s caller
   *is* the accept loop's goroutine). Every successful `Accept` registers
   the conn in the map under the mutex, then spawns 1 *handler* goroutine
   per conn.
4. Each handler: `defer wg.Done()`, `defer conn.Close()`, `defer
   unregister(conn)`, `defer recover()`; reads one line, encodes JSON from a
   single `Snapshot` + `inputs` projection, writes the response, returns.
5. When the accept loop exits (Accept returns `net.ErrClosed`), `Run` calls
   `wg.Wait()` to drain handlers, then returns `nil`.
6. The watcher goroutine has a `select { case <-ctx.Done(): ... case
   <-done: }` shape so it does not leak when `Run` exits because of an
   `Accept`-side error before ctx cancel. `Run`'s `defer close(done)`
   guarantees the watcher exits on the non-ctx path too.

**Rationale:**
- Sub-second bound (FR-022-14): no handler can block more than `conn.Close()`
  semantics permit — kernel-level. No timeouts, no `time.AfterFunc`, no
  retry budget — pure cancellation propagation.
- Constitution IX: every spawned goroutine has a documented owner (`Run`'s
  caller via `Run`), an explicit cancellation path (ctx for the watcher;
  conn-close for the handler), and a documented termination condition
  (Accept-returns-ErrClosed; conn-Close-causes-Read-error). Each has
  `defer recover()` at top frame.
- `wg.Wait()` ensures `Run` does not return until every spawned goroutine
  has joined — no leaks observable by `runtime.NumGoroutine()` post-`Run`.
- Tracked-conn map under mutex is the simplest correctness-first approach;
  the cardinality is bounded by the number of in-flight clients, which is
  small (`hush client status` is a single-shot CLI). Alternatives like
  `sync.Map` add complexity without payoff.

**Goroutine inventory** (Constitution IX §Goroutine discipline):

| Goroutine | Owner | Spawn site | Cancellation | Termination | Recover |
|---|---|---|---|---|---|
| accept loop | `Run`'s caller (= `Run` itself) | inline in `Run` | `listener.Close()` triggered by watcher on ctx-done | Accept returns `net.ErrClosed`; loop returns | top of `Run` |
| watcher | `Run` | `go s.watch(ctx, listener, done)` inside `Run` | `<-ctx.Done()` OR `<-done` (close on early Run exit) | both `Close` and `closed conns` complete; goroutine returns | top of `watch` |
| handler (per conn) | accept loop | `go s.handle(conn, wg)` inside accept loop body | watcher's `conn.Close()` (force-close) OR ctx-cancellation propagated through Read/Write error | handler returns; `wg.Done()` | top of `handle` |

Total runtime cost: 1 accept goroutine (= `Run`'s frame) + 1 watcher + N
handlers, where N == in-flight connection count, capped by client demand.

**Alternatives considered:**
1. **Serialise all handlers in the accept loop (no per-conn goroutine).**
   Rejected: simpler but means a single slow client blocks the next
   `Accept`, defeating the spec's "in-flight handler" semantics. Also
   harder to test for force-close behaviour in isolation.
2. **Use `(*net.UnixListener).SetDeadline` / `SetReadDeadline` instead of
   `Close`.** Rejected: deadlines are time-based; force-close is the
   spec'd mechanism (Clarification 3 — "force-close their connection").
   Mixing the two adds confusion.
3. **`golang.org/x/sync/errgroup` for the goroutine join.** Rejected: adds
   a non-stdlib dep (FR-022-19 forbids); `sync.WaitGroup` suffices.

---

## R-4. Pre-listen ordering: parent-dir creation, perms refusal, stale-inode cleanup

**Question:** FR-022-10 requires `0700` parent dir create-if-missing and
refusal-with-`ErrSocketPermsLoose` when an existing parent is laxer than
`0700`. FR-022-11 requires removing a stale socket inode at the configured
path before bind. The order of these operations matters: a wrong order
either leaks a partially-created listener on perms-refusal or misses the
stale-inode case.

**Decision:** the canonical pre-listen sequence in `StatusServer.Run`:

```text
1. parent := filepath.Dir(socketPath)
2. ensureParentMode0700(parent)            // FR-022-10 — refuse early; nothing bound yet
3. os.Remove(socketPath)                    // FR-022-11 — best-effort; ignore IsNotExist
4. listener, err := net.Listen("unix", socketPath)
5. os.Chmod(socketPath, 0o600)              // FR-022-9 — explicit, never relies on umask
6. start watcher goroutine
7. start accept loop
```

`AcquirePidFile` mirrors steps 1-2 then opens the file at mode `0600` and
`Flock`s. Step 3 is socket-only (PID files are not unlinked pre-acquire —
the OS-released-flock-on-process-death contract relies on the *file* being
reused).

The shared parent-perms helper:

```go
// ensureParentMode0700 is called by both AcquirePidFile and
// StatusServer.Run. Returns ErrSocketPermsLoose (wrapped) when the parent
// exists but its mode is laxer than 0700. Creates the parent at 0700 when
// missing. Any other I/O error is returned wrapped (distinguishable from
// ErrSocketPermsLoose via errors.Is).
func ensureParentMode0700(parent string) error
```

**Rationale:**
- Refuse early (step 2) — no listener bound, no PID file open, no socket
  inode created — clean rollback on perms-refusal.
- Stale-inode unlink (step 3) is best-effort: `IsNotExist` is success;
  any other error is surfaced (so a permissions-denied unlink doesn't
  silently fall through to a confusing `EADDRINUSE` at step 4).
- Step 5's explicit chmod after Listen is required by FR-022-9: the kernel
  applies umask to the socket inode, so even at file mode `0666 &
  ~umask=022 = 0644` the result would be world-readable; we forcibly
  narrow to `0600`.
- The same helper used by both `pidfile.go` and `socket.go` ensures
  symmetry of behaviour without duplication (FR-022-10 closing sentence:
  "Same rule applies to the pid_file parent directory").

**Alternatives considered:**
1. **Listen first, then chmod-and-remove on error.** Rejected: leaves
   partial state (listener bound but path mode laxer than 0600) inside the
   error path — a brief window where another process could connect.
2. **Use `umask(0)` for the duration of Listen.** Rejected: process-global
   side effect; would race with concurrent supervisor goroutines if any
   ever existed. Explicit chmod is local + race-free.
3. **`os.MkdirAll(parent, 0o700)` blindly without checking existing perms.**
   Rejected: `MkdirAll` returns nil if the dir already exists — even if it
   exists at `0755`. We need to *refuse*, not fix-up. Hence the explicit
   stat-then-act pattern in `ensureParentMode0700`.

---

## R-5. `flock` semantics — stale acquired without parsing PID

**Question:** FR-022-3 requires the next supervisor to acquire the PID file
cleanly when the previous owner died without releasing. The criterion MUST
be the OS-held flock, never parsed PID text. How is this implemented in one
straightforward call sequence?

**Decision:**

```go
// AcquirePidFile (sketch):
fd, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
// ...
err = unix.Flock(int(fd.Fd()), unix.LOCK_EX|unix.LOCK_NB)
if errors.Is(err, unix.EWOULDBLOCK) {
    fd.Close()
    return nil, ErrPidLocked        // live owner — duplicate-supervisor case
}
if err != nil {
    fd.Close()
    return nil, fmt.Errorf("supervise: pidfile flock: %w", err)
}
// lock held — write PID:
if err := fd.Truncate(0); err != nil { /* close + return */ }
if _, err := fd.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil { /* close + return */ }
return &PidFile{fd: fd, path: path}, nil
```

**Rationale:**
- `LOCK_EX | LOCK_NB` is non-blocking exclusive — exactly the "fail fast"
  semantics of FR-022-1. On contention, returns `EWOULDBLOCK` (sometimes
  spelled `EAGAIN` on Linux; both are mapped to the same numeric value
  `unix.EWOULDBLOCK == unix.EAGAIN` in practice — `errors.Is` covers it).
- The OS releases all flocks held by a process when the process dies (POSIX
  `fcntl`-style locks differ here; `flock(2)` is OS-fd-based and is
  released on the **last fd close**, which the kernel does on process
  termination). So a stale PID file from a dead-without-Release owner has
  no live flock; our single `Flock` attempt succeeds. **Zero retries
  needed.**
- We never parse the existing file contents. The textual PID is advisory
  metadata for human operators (`cat /path/to.pid` shows them who's
  running) — never a control-flow input.
- `Truncate(0) + WriteAt` rather than `os.WriteFile` because we hold the
  fd and want to overwrite-in-place; `os.WriteFile` would re-open via
  `O_TRUNC` and lose the flock.

**Alternatives considered:**
1. **Parse the existing file's PID text and `kill(pid, 0)` to probe.**
   Rejected: PIDs are recycled; a probe-success could be a stranger process
   that happens to have the recycled PID. The flock is the OS's
   authoritative answer; using anything else introduces a TOCTOU window.
   Also forbidden by FR-022-3 ("MUST NOT be the criterion that distinguishes
   stale from live").
2. **Open with `O_TRUNC` to atomically clear the file.** Rejected: would
   wipe the previous owner's PID text *before* we know we have the lock —
   if the lock fails, we've corrupted the live owner's record (FR-022-2
   second clause: "without modifying the file" on refusal).
3. **Use `fcntl`-style record locks (`SETLK`).** Rejected: POSIX record locks
   have surprising semantics (released on **any** close in the process,
   not just the locking fd's). `flock(2)` is simpler and matches the
   "second invocation = `EWOULDBLOCK`" model exactly.

---

## R-6. `Release` semantics

**Question:** FR-022-5 requires `Release` to release the flock and remove
the PID file (best-effort), be safe to call exactly once, and surface
unexpected I/O errors. What's the call order, and what happens on double-
`Release`?

**Decision:**

```go
func (p *PidFile) Release() error {
    if p == nil || p.fd == nil {
        return ErrAlreadyReleased  // package-private sentinel — see anti-API note
    }
    fd, path := p.fd, p.path
    p.fd = nil

    // 1. unlock first so a concurrent acquirer can pick up the file even
    //    if step 2/3 errors.
    if err := unix.Flock(int(fd.Fd()), unix.LOCK_UN); err != nil {
        fd.Close()
        return fmt.Errorf("supervise: pidfile unlock: %w", err)
    }
    if err := fd.Close(); err != nil {
        return fmt.Errorf("supervise: pidfile close: %w", err)
    }
    if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
        return fmt.Errorf("supervise: pidfile remove: %w", err)
    }
    return nil
}
```

**Rationale:**
- Order: unlock → close → remove. Unlock first so even if `Close` or
  `Remove` errors, the flock is no longer held — a subsequent supervisor
  can acquire.
- `os.Remove` is best-effort: `fs.ErrNotExist` is success (an external
  process might have unlinked the file; FR-022-5 explicitly permits "losing
  the race to another startup"). Other errors (e.g., permission) are
  surfaced.
- Setting `p.fd = nil` first makes `Release` idempotent in spirit: a second
  `Release` call returns the package-private sentinel `ErrAlreadyReleased`
  rather than panicking on a nil-fd dereference.

**Anti-API note:** `ErrAlreadyReleased` is *package-private* (lowercase
`errAlreadyReleased`) — not exported. The orchestrator should never
double-`Release` in correct code; this is an internal safety belt. A test
asserts the second-Release-returns-error behaviour, but the sentinel is not
part of the locked exported surface.

**Alternatives considered:**
1. **Order: close → unlock → remove.** Rejected: closing the fd implicitly
   releases the lock on POSIX `flock`, but the explicit unlock makes the
   intent visible and survives a future move to `fcntl` locks if ever
   considered.
2. **Idempotent `Release` returning nil on second call.** Rejected: hides a
   programmer error (callers should know the lifecycle); a deterministic
   sentinel is preferable.

---

## R-7. JSON encoding of FR-12 — types, formats, and zero-value rendering

**Question:** the FR-12 example shows `session_expires_at:
"2026-04-15T06:12:00-07:00"`, `child_uptime: "8h12m"`,
`last_auth_failure: null`. Go's `time.Time.MarshalJSON` produces RFC3339
with nanosecond precision; `time.Duration` marshals as int64 nanoseconds by
default, not the `"8h12m"` form. How is the encoding aligned with FR-12 to
satisfy SC-022-5 (every field name + type matches)?

**Decision:** introduce a private DTO `statusJSON` with explicit string-
formatted fields and a custom encoder helper:

```go
type statusJSON struct {
    Supervisor        string   `json:"supervisor"`
    SessionExpiresAt  string   `json:"session_expires_at"`     // time.RFC3339
    RefreshWindowNext string   `json:"refresh_window_next"`    // time.RFC3339
    ScopeHealthy      []string `json:"scope_healthy"`          // never nil
    ScopeStale        []string `json:"scope_stale"`            // never nil
    LastAuthFailure   *string  `json:"last_auth_failure"`      // RFC3339 string or nil
    ChildPID          *int     `json:"child_pid"`              // null when no child (PID == 0)
    ChildUptime       string   `json:"child_uptime"`           // time.Duration.String() — "8h12m0s" form
    DiscordConnected  bool     `json:"discord_connected"`
    State             string   `json:"state"`                  // Snapshot.State string form
}
```

**Rationale:**
- Time fields render as RFC3339 strings, matching the FR-12 example.
- `child_uptime` uses `time.Duration.String()` (e.g., `"0s"`, `"8h12m0s"`)
  to match the FR-12 spirit. The exact format string is locked in
  contracts/api.md; tests assert it.
- `last_auth_failure: null` is realised by `*string` (pointer-to-RFC3339);
  zero `time.Time` from `inputs.LastAuthFailure() == nil` projects to JSON
  `null`.
- `child_pid` is `*int` so the FR-12 example's `null` representation for
  "no child running" is honoured (spec edge case "child_pid and
  child_uptime while no child is running ... null child_pid, 0s uptime, or
  omission" — we pick "null child_pid + 0s uptime"). When `Snapshot.ChildPID
  == 0`, the encoder emits `null`.
- Empty `ScopeHealthy` / `ScopeStale` are explicitly initialised to `[]string{}`
  before encoding so JSON renders `[]` rather than `null` (avoids consumer
  ambiguity).

**Alternatives considered:**
1. **Marshal `time.Time` directly without a string DTO.** Rejected: nanos
   in the RFC3339 output (`...000000+07:00`) differ from FR-12's example
   (no nanos); a string-DTO with explicit format is the simplest fix.
2. **Encode `time.Duration` as int64 nanos.** Rejected: spec example shows
   `"8h12m"`; an int field would silently change the wire shape consumers
   expect.
3. **Use `omitempty` on `ChildPID` and `LastAuthFailure`.** Rejected:
   FR-022-12 says no fields are dropped — every field must be present in
   the document. Pointer-to-T with explicit `null` rendering is the
   shape-conformant solution.

---

## R-8. Single-shot `Run` via `sync.Once` (Clarification 2 / FR-022-14a)

**Question:** Calling `Run` a second time on the same `StatusServer`
instance (concurrent or sequential) must return `ErrAlreadyRunning` without
binding. Mirrors SDD-21's Refresher pattern.

**Decision:** `sync.Once` at the head of `Run`:

```go
func (s *StatusServer) Run(ctx context.Context) error {
    var ranAlready bool
    s.runOnce.Do(func() { /* sets a flag indicating first call */ })
    s.mu.Lock()
    if s.started {
        s.mu.Unlock()
        return ErrAlreadyRunning
    }
    s.started = true
    s.mu.Unlock()
    // ... pre-listen, listen, watcher, accept loop, drain ...
}
```

(Implementation detail: `started bool` under `s.mu` is sufficient on its
own; `sync.Once` is one alternative.)

**Rationale:** matches SDD-21's `Refresher.Run` precedent (single-shot
guard); identifiable via `errors.Is(err, ErrAlreadyRunning)` for the
orchestrator. To restart status after a lifecycle stop, the orchestrator
constructs a fresh `StatusServer` (spec edge case explicit).

**Alternatives considered:**
1. **Allow `Run` to be re-called after a previous `Run` returned.**
   Rejected: spec Clarification 2 explicitly closes this door. Re-binding a
   listener on a fresh instance is cleaner than a state-resetting
   second-`Run`.

---

## R-9. Constitution-X token redaction — defense in depth

**Question:** FR-022-13 requires the status response to never include the
token. The natural implementation (don't add a `token` field to the DTO at
all) seems sufficient. What's the test guarding against?

**Decision:** the implementation does NOT include `token` in `statusJSON`.
Defense in depth: `TestSocket_TokenInResponseRedacted` constructs a `Store`
whose `Snapshot.Token` is a `*SecureBytes` containing the marker bytes
`"MARKER_d3adb33f"`. The server emits the JSON response. The test asserts
`!bytes.Contains(body, []byte("MARKER_d3adb33f"))` — proving no leak path
exists even if a future encoder modification accidentally added a token-
adjacent field.

Additionally: any `slog.Info` / `slog.Warn` / `slog.Error` emitted by the
server during request handling MUST NOT pass `Snapshot.Token` as a slog
attribute. If it ever did, `*SecureBytes.LogValue() →
slog.StringValue("[redacted]")` would render it safely — but the chunk's
policy is to not pass token through slog at all, satisfying both the
"never include" and the "type-driven redaction is the safety net" angles.

---

## R-10. Fuzz target seeding (Constitution VIII §Mandatory fuzz targets, item 6)

**Question:** Constitution VIII names "Status socket JSON encoding" as
mandatory fuzz target #6. Does this chunk own the fuzz target?

**Decision:** yes. The Phase-2 (`/speckit-tasks`) task list will include a
fuzz target `FuzzStatusJSON_Encode` seeded by `socket_test.go`'s table
entries. Goal: panic-free, no unbounded memory, malformed `StatusInputs`
returns produce a deterministic shape-conformant JSON document or a
deterministic error. The fuzz target hits the encoder helper, not the
network.

**Rationale:** seeding belongs in `/speckit-tasks` (Phase 2), not
`/speckit-plan` (Phase 1). This research entry exists to make the ownership
explicit.

---

## R-11. Static-grep test for "no TCP, no HTTP, no bearer"

**Question:** SC-022-9 requires a test that grep-asserts the absence of
`net.Listen("tcp", ...)`, `http.Server{}`, and bearer-token references in
the chunk's source files. How is this realised in Go?

**Decision:** a `TestSocket_NoTCPListenerOrHTTPServer` reads each of
`pidfile.go`, `socket.go`, `socket_darwin.go`, `socket_linux.go` via
`os.ReadFile`, lowercases the contents, and asserts `!bytes.Contains` for
each forbidden token: `net.listen("tcp"`, `http.server`,
`http.listenandserve`, `bearer`, `authorization` (header-name).

**Rationale:**
- Pure Go, runs in-process under `go test`, no shell-out.
- Catches regressions immediately; cheap to maintain.
- Lowercased substring match catches casing drift.

**Alternatives considered:**
1. **`grep -r` shell-out from `make` / CI.** Rejected: less hermetic,
   harder to keep in sync with the chunk's own file list.
2. **AST-based check via `go/parser`.** Rejected: substring is sufficient
   for the small forbidden vocabulary; AST adds complexity.

---

## R-12. Coverage strategy

Coverage gate: `go test -race -cover ./internal/supervise/ -run
"PidFile|Socket"` ≥ 95% (SC-022-10).

The new lines of code are all covered by:
- Pidfile tests: 8 tests cover acquire success, refusal, stale, release,
  PID write, mode 0600, parent-dir create, parent-dir refuse.
- Socket tests: 14+ tests cover mode, parent perms, JSON shape, JSON values
  with stub inputs, redaction, shutdown, force-close, rebind, double-Run,
  goroutine leak, no-TCP-listener static, pre-attach defaults, stale inode
  cleanup, snapshot consistency.
- Helper test: `ensureParentMode0700` is exercised through both pidfile
  and socket paths.

Untested-by-design (and excluded from coverage by way of being trivially
exercised): the build-tagged `defaultRuntimeDir()` helper functions (one
each on darwin / linux). These are exercised on the host's GOOS by
`go test`; the other-OS file is not compiled. Both are tiny (≤5 lines)
and are exercised via `socket_test.go` indirectly when constructing the
test temp path.

---

## R-13. Anti-contracts (Constitution-driven negative invariants)

This chunk MUST NOT:

1. Add HTTP — no `net/http` import in any new file (Constitution V).
2. Add TCP — no `net.Listen("tcp", ...)`, no `127.0.0.1:` literal, no
   `tcp4`/`tcp6` (Constitution V).
3. Add bearer-token / HMAC / signed-cookie auth on the socket
   (Constitution V).
4. `string(decryptedBytes)` anywhere — `Snapshot.Token` is never
   materialized as a Go string (Constitution X / FR-022-13). The token
   does not appear in the rendered JSON; it does not appear in slog output.
5. Spawn an `init()` (Constitution IX).
6. Introduce a package-level mutable global (Constitution IX).
7. Add a new direct dependency to `go.mod` (FR-022-19).
8. Modify any SDD-18 (`config/`), SDD-19 (`state.go`), SDD-20
   (`child*.go`), or SDD-21 (`refill.go` / `refresh.go` / `grace.go`)
   symbol.
9. Parse the existing PID-file text as a control-flow input (FR-022-3).
10. Silently `chmod` a laxer-than-`0700` parent dir (FR-022-10) —
    `ErrSocketPermsLoose` instead.
11. Silently `os.Remove` a parent dir or unlink a non-stale PID file.
12. Log connection payload bytes in any slog record (Constitution X).
13. Spawn a goroutine without a documented owner / cancellation /
    termination / `recover()` (Constitution IX).
14. Allow `Run` to return before every spawned goroutine has joined.
15. Internally retry `Accept`, `flock`, or any other syscall — surface
    errors to the caller verbatim wrapped with `%w`.
16. Bind a listener on the socket path before the parent-perms check
    succeeds — refuse early.
17. Re-bind a listener on a `Run`-already-called instance — return
    `ErrAlreadyRunning`.

---

**Phase 0 status: COMPLETE.** No NEEDS CLARIFICATION remaining.
