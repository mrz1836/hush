# Implementation Plan: Supervisor PID File + Unix Status Socket

**Branch**: `022-supervise-pidfile-socket` | **Date**: 2026-05-10 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/022-supervise-pidfile-socket/spec.md`

## Summary

This chunk (SDD-22) extends `package supervise` with two operator-visibility
primitives required by FR-11 / FR-12 / AC-10 Lifecycle Scenarios 12 and 14:

1. **`PidFile`** — a `golang.org/x/sys/unix.Flock(LOCK_EX|LOCK_NB)`-backed
   advisory lock on the configured `pid_file` path. Acquisition writes the
   current PID as text after the lock is held; refused acquisition surfaces
   `ErrPidLocked` without blocking and without modifying the file. OS-released
   stale locks (previous owner died) are picked up cleanly via a single
   re-attempt — no PID parsing, no `kill(0)` probing, no stale-flag heuristic.
2. **`StatusServer`** — a Unix-domain `net.Listen("unix", ...)` listener at the
   configured `status_socket` path that, on every accepted connection, takes a
   single defensive `Store.Snapshot()` and writes the FR-12 JSON document.
   Single-shot per-instance `Run` (sync.Once → `ErrAlreadyRunning` on second
   call), parent dir created at `0700` if missing, refused with
   `ErrSocketPermsLoose` when an existing parent dir is laxer than `0700`,
   socket file `chmod 0600` after `Listen`, stale socket inode cleaned up
   pre-`Listen`, sub-second graceful shutdown on ctx cancel via
   listener-close + force-close of every tracked in-flight connection.

The locked exported API from SDD-22.md is honoured exactly:
`type PidFile struct{}`, `AcquirePidFile(path) (*PidFile, error)`,
`(*PidFile).Release() error`, `type StatusServer struct{}`,
`NewStatusServer(socketPath, store, logger) *StatusServer`,
`(*StatusServer).Run(ctx) error`,
`var ErrPidLocked, ErrSocketPermsLoose`. **Spec clarification 2 adds one
sentinel** (`ErrAlreadyRunning`) and **the FR-12 ↔ Snapshot field gap** is
bridged by a package-private `(*StatusServer).attach(StatusInputs)` post-
construction wiring point — both extensions mirror SDD-21's `Refiller.attach`
precedent and are tracked in Complexity Tracking.

This chunk **does not** implement HTTP-on-localhost, bearer auth, TCP loopback,
or any application-layer authentication: filesystem permissions (`0600` socket,
`0700` parent dir) ARE the auth (Constitution V).

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`)
**Primary Dependencies**: stdlib (`net`, `os`, `encoding/json`, `log/slog`,
`context`, `sync`, `errors`, `fmt`, `path/filepath`, `strconv`, `time`),
`golang.org/x/sys/unix` (already in module — `Flock`, `LOCK_EX`, `LOCK_NB`,
`LOCK_UN`), existing `internal/supervise.Store` + `Snapshot` (SDD-19), existing
`internal/vault/securebytes` (Token redaction via `LogValue()`).
**Storage**: Filesystem only — `pid_file` (mode `0600`, parent `0700`),
`status_socket` (mode `0600`, parent `0700`). No vault, no DB, no in-process
caches beyond a per-server `map[net.Conn]struct{}` for in-flight connection
tracking.
**Testing**: `testify` + `magex test:race`. TDD-mandatory per Constitution VIII.
**Target Platform**: macOS (darwin/amd64, darwin/arm64) and Linux
(linux/amd64). Unix domain sockets and `flock` are POSIX; `golang.org/x/sys/unix`
provides the syscall wrapper. CGO disabled (Constitution IX).
**Project Type**: single Go module / CLI binary; `internal/supervise` package
extension.
**Performance Goals**: status-server graceful shutdown bounded sub-second on
ctx-cancel (FR-022-14, Clarification 3); pid-file acquisition refusal returns
within milliseconds (SC-022-2 — well below normal supervisor startup time).
**Constraints**: no new direct `go.mod` dependency (FR-022-19); no goroutine
leak across any number of start/stop cycles (SC-022-8); ≥95% coverage on the
new files (SC-022-10); race-clean.
**Scale/Scope**: one supervisor per host per daemon name; status socket
serves transient single-request clients (`hush client status`), not a
high-throughput RPC channel. Parent dir of `pid_file` and `status_socket`
is the same per-daemon cache directory in normal config (assumption from
spec).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Gate | Pass / Fail | Evidence |
|---|---|---|---|
| **V — Staleness visible, failure loud** | Status socket exposes FR-12 JSON; FS perms (0600/0700) ARE the auth — no bearer, no HTTP, no TCP loopback. Refused parent-perms config surfaces `ErrSocketPermsLoose` at startup (loud failure, no silent chmod). Duplicate supervisor refused with `ErrPidLocked` (loud, no overwrite). | **PASS** | FR-022-8/9/10/15; SC-022-9 grep-test absent of `net.Listen("tcp", …)` and `http.Server{}` in chunk files. |
| **VIII — Testing Discipline** | TDD: 14+ test names enumerated below before implementation. Race-clean (`magex test:race`). Coverage ≥95% on `pidfile.go` + `socket.go` + platform shims, verifiable via `go test -cover ./internal/supervise/ -run "PidFile\|Socket"`. AC-10 Scenarios 12 and 14 each get a passing test in this chunk. Status-socket JSON has a designated fuzz target (Constitution VIII §Mandatory fuzz targets, item 6) — this chunk authors the JSON encoding fuzz seed in the implement-phase tasks. | **PASS** | SC-022-1, SC-022-5, SC-022-8, SC-022-10. |
| **IX — Idiomatic Go Discipline** | Sentinel errors as `var Err… = errors.New(…)` (`ErrPidLocked`, `ErrSocketPermsLoose`, `ErrAlreadyRunning`); error wrap with `%w`; no `init()`; no package-level mutable state; `context.Context` first param on `Run`; consumer-defined `StatusInputs` interface; no globals; goroutines have explicit owner + ctx-cancellation + termination + top-frame `recover()`; pure-Go (`CGO_ENABLED=0`) — `golang.org/x/sys/unix` is pure-Go. | **PASS** | FR-022-17, FR-022-18, FR-022-19. Goroutine inventory: see "Goroutine Discipline" in research.md. |
| **X — Observability & Redaction** | The FR-12 status JSON has no `token` field; `Snapshot.Token` is never marshalled by the encoder. Defense in depth: a unit test (`TestSocket_TokenInResponseRedacted`) asserts that even when `Snapshot.Token` is non-nil with a known marker-byte plaintext, the rendered JSON contains neither the plaintext nor any prefix of it. `slog` records emitted by the server (accept errors, conn-handler errors) NEVER include connection payload bytes — error messages are mode/identifier only. | **PASS** | FR-022-13, SC-022-6. |

**Other principles** (I, II, III, IV, VI, VII, XI) — out of scope for this
chunk; not gated. (Tailscale-only, Discord-gating, BIP32, etc. are upstream of
SDD-22 — the supervisor's already-claimed JWT is what this chunk renders the
*absence* / *redaction* of, not what it issues.)

**Initial gate decision: PASS.** Proceed to Phase 0.

## Project Structure

### Documentation (this feature)

```text
specs/022-supervise-pidfile-socket/
├── plan.md              # this file (/speckit-plan output)
├── research.md          # Phase 0 — design decisions + alternatives rejected
├── data-model.md        # Phase 1 — types, JSON shape, state, goroutine inventory
├── quickstart.md        # Phase 1 — how to verify the chunk locally
├── contracts/
│   └── api.md           # Phase 1 — Go signatures + sentinel error semantics + JSON contract
├── spec.md              # produced by /speckit-specify (already present)
└── tasks.md             # produced by /speckit-tasks (NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/supervise/
├── pidfile.go              # NEW — PidFile + AcquirePidFile + Release + ErrPidLocked
├── pidfile_test.go         # NEW — flock semantics + parent-perms + release
├── socket.go               # NEW — StatusServer + NewStatusServer + Run + status JSON encoder + ErrSocketPermsLoose + ErrAlreadyRunning + StatusInputs interface + (*StatusServer).attach
├── socket_darwin.go        # NEW — build-tagged platform shim (macOS-specific helpers; see research.md)
├── socket_linux.go         # NEW — build-tagged platform shim (Linux-specific helpers; see research.md)
├── socket_test.go          # NEW — listen mode + parent perms + JSON shape + redaction + shutdown + rebind + double-Run + force-close + goroutine-leak + no-tcp-listener static
│
├── state.go                # UNCHANGED (SDD-19) — Store + Snapshot consumed unmodified
├── child.go                # UNCHANGED (SDD-20)
├── refill.go               # UNCHANGED (SDD-21)
├── refresh.go              # UNCHANGED (SDD-21)
├── grace.go                # UNCHANGED (SDD-21)
├── helpers_test.go         # UNCHANGED (SDD-21) — fakeClock reused if needed
└── config/                 # UNCHANGED (SDD-18)

specs/022-supervise-pidfile-socket/   # planning artifacts
docs/PACKAGE-MAP.md                    # appended in Implement-phase: "Exported API — locked at SDD-22"
docs/AC-MATRIX.md                      # AC-10 row updated in Implement-phase with new test paths
docs/SDD-PLAYBOOK.md                   # SDD-22 row → done in Implement-phase
```

**Structure Decision**: Single Go module, single package `internal/supervise`
extended additively. Two new behaviour files (`pidfile.go`, `socket.go`) plus
two build-tagged shims (`socket_darwin.go`, `socket_linux.go`) plus two test
files. **Zero modification** to SDD-18 (`config/`), SDD-19 (`state.go`),
SDD-20 (`child*.go`), SDD-21 (`refill.go` / `refresh.go` / `grace.go`).

## Complexity Tracking

> Two deliberate deviations from the SDD-22 chunk doc's "locked exported API"
> list. Each is mandated by the spec's Clarifications session or by the
> existing SDD-19 / FR-12 contract; neither violates Constitution IX (both are
> sentinel-class additions or package-private wiring, both follow the
> SDD-21 `Refiller.attach` precedent).

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| **Add a third exported sentinel `ErrAlreadyRunning`** beyond the SDD-22-locked `ErrPidLocked, ErrSocketPermsLoose`. | Spec Clarification 2 + FR-022-14a require single-shot `Run` and a *named* sentinel for the second-call refusal so the orchestrator (SDD-23) can distinguish lifecycle-rebind misuse from generic startup errors via `errors.Is`. | Returning a generic `errors.New` or reusing `ErrAlreadyClosed`-style strings — **rejected** because Constitution IX mandates exported `var Err… = errors.New(…)` for any condition callers need to branch on, and `errors.Is`-comparability is the requirement. Reusing an existing sentinel would conflate distinct failure modes. |
| **Add a package-private `(*StatusServer).attach(StatusInputs)` wiring method** plus a consumer-defined `StatusInputs` interface, not present in the SDD-22 file's exported-API list. | The locked 3-arg `NewStatusServer(socketPath, store, logger)` constructor cannot supply the FR-12 fields not held by SDD-19's `Snapshot` (5 fields: State, ChildPID, LastTransitionAt, Token, Reason) — namely `supervisor` (name), `session_expires_at`, `refresh_window_next`, `scope_healthy`, `scope_stale`, `last_auth_failure`, `child_uptime`, `discord_connected`. The orchestrator (SDD-23) holds those values. **The attach pattern is already established in SDD-21** for the same reason (`(*Refiller).attach(grace, priv, serverURL)` honors a locked 3-arg `NewRefiller` constructor). | Alternative A — extend SDD-19's `Snapshot` struct with the 8 new fields: **rejected** because SDD-19 is locked and PACKAGE-MAP forbids modification ("This chunk does not redefine the state model"). Alternative B — render only the 2 fields available from `Snapshot` and emit hard-coded zero values for the other 8: **rejected** because it makes the orchestrator's wiring untestable in this chunk and produces a status response that, while shape-conformant, is permanently empty until SDD-23 — defeating SC-022-1 (Scenario 12 passes here, not in SDD-23). Alternative C — break the locked 3-arg constructor: **rejected** because the chunk doc explicitly locks it and downstream chunks (SDD-23) wire against that signature. Attach is the only path that honours the lock + delivers a testable FR-12-conformant response in this chunk. |

Both deviations are scoped to be the **minimum** addition that satisfies spec
Clarifications 2 and the FR-12 ↔ SDD-19 reality. Neither introduces a new
package-level mutable global, an `init()` function, or a new direct
dependency.

---

## Phase 0: Outline & Research

**Output**: [research.md](research.md) — resolves design unknowns identified
during Constitution-Check evaluation:

1. **How does StatusServer render the 8 FR-12 fields not in SDD-19's Snapshot?**
   → Decision: package-private `attach(StatusInputs)`; pre-attach defaults
   produce a shape-conformant, semantically-empty document.
2. **What goes in `socket_darwin.go` / `socket_linux.go`?**
   → Decision: minimal build-tagged shim files holding a per-OS test-helper
   that constructs a temporary socket path under the OS's conventional
   directory (so unit tests assert FR-12 path conventions without
   re-implementing config-package responsibility). Production `socket.go`
   uses the configured absolute path verbatim — no platform branching at
   runtime.
3. **How are in-flight connections force-closed sub-second on ctx-cancel
   (FR-022-14, Clarification 3) without leaking a handler goroutine?**
   → Decision: per-conn handler is a goroutine owned by `Run`; active conns
   tracked in a `map[net.Conn]struct{}` under a `sync.Mutex`; ctx-done branch
   in a watcher goroutine closes the listener + ranges the map calling
   `conn.Close()`; per-conn handlers exit on Read/Write error; `wg.Wait()`
   ensures `Run` returns only after every spawned goroutine has exited.
4. **How is the symmetric parent-perms refusal (FR-022-10 — "Same rule
   applies to the `pid_file` parent directory") implemented without
   duplicating logic?**
   → Decision: a package-private helper `ensureParentMode0700(path) error`
   in `socket.go` (or a tiny `perms.go` if extracted) consumed by both
   `AcquirePidFile` and `StatusServer.Run`'s pre-listen path. Returns
   `ErrSocketPermsLoose` when an existing parent dir is laxer than `0700`;
   creates the dir at `0700` when missing.
5. **How does `AcquirePidFile` reliably acquire after a previous-owner death
   (FR-022-3) without parsing stale PID text?**
   → Decision: `os.OpenFile(path, O_RDWR|O_CREATE, 0600)` followed by one
   `unix.Flock(fd, LOCK_EX|LOCK_NB)` attempt. On `EWOULDBLOCK` →
   `ErrPidLocked` (live owner). On success → write current PID via
   `Truncate(0) + WriteAt`. The OS releases the prior owner's flock at
   process death; our single attempt picks it up. No retry, no PID parse,
   no `kill(0, pid)`.
6. **Goroutine inventory** — every goroutine spawned by this chunk, its
   owner, ctx-cancellation path, termination condition, and `recover()` site
   are enumerated in research.md per Constitution IX.

## Phase 1: Design & Contracts

**Prerequisites**: research.md complete.

**Outputs**:
- [data-model.md](data-model.md) — `PidFile`, `StatusServer`, `StatusInputs`,
  `statusJSON` (private DTO), `Snapshot` consumption pattern, FR-12 field-by-
  field projection, state lifecycle of the server (Idle → Listening →
  Draining → Returned), goroutine inventory.
- [contracts/api.md](contracts/api.md) — full Go signatures of every exported
  symbol; sentinel error semantics; JSON wire contract field-by-field;
  `StatusInputs` interface signature; `(*StatusServer).attach` package-private
  contract.
- [quickstart.md](quickstart.md) — local verification recipe (build tests,
  bind a temp socket, hit it from the same UID, assert mode, drive ctx-cancel,
  assert no goroutine leak via `runtime.NumGoroutine`).

**Agent context update**: this plan replaces the SDD-21 SPECKIT pointer in
[/Users/mrz/projects/hush/CLAUDE.md](/Users/mrz/projects/hush/CLAUDE.md)
between the `<!-- SPECKIT START -->` / `<!-- SPECKIT END -->` markers.

### Test surface (TDD-mandatory; Phase 2 will sequence the test-writing tasks)

Every test below is authored **before** the corresponding implementation per
Constitution VIII. Coverage gate: `go test -race -cover
./internal/supervise/ -run "PidFile|Socket"` ≥ 95%.

**`pidfile_test.go`:**
- `TestPidFile_FlockExclusive` — first acquire holds the lock; concurrent acquire on the same path from a sibling fd returns `EWOULDBLOCK` mapped to `ErrPidLocked`.
- `TestPidFile_DuplicateRefused` — `errors.Is(err, ErrPidLocked)`; first owner's PID-file contents and ownership unchanged.
- `TestPidFile_StaleAcquired` — sub-process acquires + exits without `Release`; the parent process (after `Wait` returns) acquires the same path cleanly and writes its own PID.
- `TestPidFile_ReleaseRemovesFile` — after `Release()`, the path no longer exists; double-`Release` is a no-op or surfaces a deterministic error (locked in contract).
- `TestPidFile_WritesOwnPID` — after acquire, file contents `== strconv.Itoa(os.Getpid())`.
- `TestPidFile_Mode0600` — `os.Stat(path).Mode().Perm() == 0o600`.
- `TestPidFile_ParentMode0700Created` — when parent dir does not exist, acquire creates it at `0700`.
- `TestPidFile_ParentLooseRefuses` — when parent dir exists at `0755`, acquire returns `errors.Is(err, ErrSocketPermsLoose)` (parent-perms helper symmetric with socket).

**`socket_test.go`:**
- `TestSocket_Mode0600` — after `Run` is up, `os.Stat(path).Mode().Perm() == 0o600`.
- `TestSocket_ParentMode0700` — when parent dir is missing, `Run` creates it at `0700`.
- `TestSocket_ParentLooseRefuses` — pre-existing `0755` parent dir → `Run` returns `errors.Is(err, ErrSocketPermsLoose)`; no listener bound.
- `TestSocket_StatusJSONShape` — table-driven; every FR-12 field name + Go type is asserted (`supervisor` string, `session_expires_at` RFC3339 string, `refresh_window_next` RFC3339 string, `scope_healthy []string`, `scope_stale []string`, `last_auth_failure` *time.Time-or-null, `child_pid` int, `child_uptime` Go-duration string, `discord_connected` bool, `state` string).
- `TestSocket_StatusJSONFromSnapshot` — feeds a known `Store` (state=running, child_pid=4242) plus a stub `StatusInputs`; asserts the JSON body byte-equals the documented FR-12 example shape (modulo wall-clock fields rendered via fakeClock).
- `TestSocket_TokenInResponseRedacted` — Store is set up with `Snapshot.Token` non-nil holding the marker bytes `"MARKER_d3adb33f"`; rendered JSON contains neither `"MARKER_d3adb33f"` nor any prefix of it; the JSON has no `token` field at all.
- `TestSocket_GracefulShutdownOnCtx` — context cancel returns `Run` with `nil` error; assertion that `time.Since(cancelTime) < 1 * time.Second`.
- `TestSocket_ConnectionForceClosedOnCtxCancel` — a client connects, reads partial response, server side has not closed; ctx cancel; client's next read returns `io.EOF` / connection-closed; `Run` returns within sub-second bound.
- `TestSocket_PreviousSocketCleanedUp` — pre-existing socket inode on disk is removed before bind; a stub stale inode does not block `Run`.
- `TestSocket_RebindAfterStop` — `Run` → ctx cancel → `Run` returns nil → second `NewStatusServer` + `Run` against the same path binds successfully (no `EADDRINUSE`).
- `TestSocket_RunSecondCallReturnsErrAlreadyRunning` — calling `(*StatusServer).Run` twice on the same instance: second call returns `errors.Is(err, ErrAlreadyRunning)` without binding.
- `TestSocket_NoGoroutineLeak` — table-driven start/stop cycle; `runtime.NumGoroutine()` before vs after, equal modulo a small tolerance window for runtime noise.
- `TestSocket_NoTCPListenerOrHTTPServer` — static assertion: file-byte grep over `pidfile.go`, `socket.go`, `socket_darwin.go`, `socket_linux.go` confirms absence of `net.Listen("tcp"`, `http.Server`, `http.ListenAndServe`, `bearer`, `Authorization` (case-insensitive — Constitution V; SC-022-9).
- `TestSocket_PreAttachDefaultsRenderShapeConformant` — when `attach` was never called, the JSON document still contains all 10 FR-12 fields with their zero values (empty string for `supervisor`, RFC3339 zero for timestamps, empty `[]string{}` for scope arrays, `null` for `last_auth_failure`, `0` for `child_pid` or whatever Snapshot reports, `"0s"` for `child_uptime`, `false` for `discord_connected`, `state` from Snapshot).

### Constitution Check (re-evaluation post-design)

| Principle | Re-check | Pass / Fail | Rationale |
|---|---|---|---|
| **V** | Same as initial; the design adds no auth surface and explicitly grep-guards against TCP / HTTP / bearer in the test suite. | **PASS** | Unchanged. |
| **VIII** | TDD test list above (~21 named tests) is authored before implementation; coverage gate ≥95% verifiable; race-clean asserted by `magex test:race`; status-socket JSON encoding fuzz target seeded in tasks-phase. | **PASS** | All Constitution-VIII §Mandatory fuzz target #6 owners are this chunk; the seed is task-phase work. |
| **IX** | Goroutine inventory in research.md / data-model.md: per-server goroutine = 1 (accept loop, owned by `Run`, ctx-cancellation = listener.Close + watcher goroutine, termination = listener.Accept returns ErrClosed, recover at top frame). Per-connection goroutine = 1 per accepted conn (owned by the accept loop's spawn, ctx-cancellation = conn.Close from the watcher, termination = handler returns, recover at top frame). Watcher goroutine = 1 per `Run` (owned by `Run`, ctx-cancellation = `<-ctx.Done()`, termination = ctx done OR `done` chan closed, recover at top frame). All joined via `sync.WaitGroup`; `Run` returns only after every spawned goroutine has exited. No `init()`. No package-level mutable state. Sentinel errors all `var Err… = errors.New(…)`. Errors wrap with `%w`. Pure-Go (no CGO). No new direct `go.mod` dependency (`golang.org/x/sys` already in module). | **PASS** | All five gates met. |
| **X** | `Snapshot.Token` is intentionally NOT a field of the rendered JSON DTO; the encoder emits only the 10 FR-12 fields. Token bytes can never reach the wire. Defense in depth: `TestSocket_TokenInResponseRedacted` proves no marker-byte leak even if the encoder is later mis-extended. `slog` outputs from this chunk are mode/identifier only (e.g. `slog.Info("status: connection accepted", "remote", "<unix-peer>")`) — never connection payload. | **PASS** | FR-022-13 / SC-022-6 satisfied. |

**Post-design gate decision: PASS.** No new violations introduced by the
research.md decisions. Complexity Tracking rows remain the two deliberate,
spec-mandated extensions documented above.

---

**Phase 2 (tasks.md) is NOT created by this command** — `/speckit-tasks` is the
next phase, run in a fresh session per the SDD-22 chunk doc Prompt 4.
