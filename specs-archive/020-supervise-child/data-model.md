# Phase 1 Data Model — SDD-20 Supervise Child Process Layer

**Branch**: `020-supervise-child` | **Date**: 2026-05-05

This document captures the **data entities** defined by SDD-20 and
their relationships. The exact Go type signatures live in
`contracts/go-api.md`; this file states the intent, fields,
invariants, and validation rules in a form that survives a Go
rewrite.

## Entities

### `Child` — supervised daemon handle

A handle to a single supervised daemon process. Carries the
process identity, the bounded output buffers, the cached
`*exec.Cmd` while the child is live, the per-`Start` channel
plumbing, and the `sync.WaitGroup` for goroutine lifecycle. A
`Child` is **single-use**: once `Wait` has returned the exit
disposition, the cached `*exec.Cmd` handle is cleared and every
subsequent call returns `ErrChildNotStarted` (FR-020-11). To
launch another daemon, the caller constructs a fresh `Child`.

**Fields (private)**:
| Field | Type | Purpose | Zero value |
|-------|------|---------|-----------|
| `cfg` | `ChildConfig` | Caller-supplied configuration; copied at construction. The slices (`Command`, `Env`) are reference-shared with the caller — the layer never mutates them. | (caller-supplied) |
| `mu` | `sync.RWMutex` | Guards `cmd` and `pid`. Held in write mode by `Start` (set) and `Wait` (clear after `cmd.Wait()` returns); held in read mode by `Forward`, `PID`, and the forwarding/drain goroutines. | (zero-usable) |
| `cmd` | `*exec.Cmd` | The live child handle, or `nil` when no child is live. **Cleared post-`Wait`** (FR-020-11). | `nil` |
| `pid` | `int` | The cached child PID, populated by `Start` and cleared by `Wait`. | `0` |
| `stdoutRing` | `*ringBuffer` | The 64 KB FIFO ring buffer wrapping stdout. Allocated once at `NewChild`; lives for the `Child`'s entire lifetime. | (allocated by `NewChild`) |
| `stderrRing` | `*ringBuffer` | The 64 KB FIFO ring buffer wrapping stderr. | (allocated by `NewChild`) |
| `forwardCh` | `chan os.Signal` | Buffered (capacity 1) channel from `Forward` callers to the forwarding goroutine. | (allocated by `NewChild`) |
| `childDone` | `chan struct{}` | Closed by `Wait` after `cmd.Wait()` returns. Broadcast signal for the forwarding, drain, and (darwin) death-watch goroutines to terminate. | (allocated by `Start`; recreated each `Start` to reset the close-once contract) |
| `wg` | `sync.WaitGroup` | Joins all per-`Start` goroutines; `Wait` returns only after every goroutine has terminated, so `runtime.NumGoroutine()` returns to baseline (SC-020-6). | (zero-usable) |
| `waitOnce` | `sync.Once` | Guarantees the `cmd.Wait` body runs at most once, regardless of concurrent callers (Clarification 1). | (zero-usable) |
| `exitCode` | `int` | The cached exit code populated inside `waitOnce.Do(...)`. Read by no caller — only the first `Wait` returns it; concurrent and subsequent callers return `ErrChildNotStarted`. Kept here for assertions in tests. | `0` |
| `exitSignal` | `syscall.Signal` | The cached terminating signal, similarly. | `0` |
| `exitErr` | `error` | The cached I/O error from `cmd.Wait()`, similarly. | `nil` |

**Invariants**:
- `Child` is never copied by value (the embedded `sync.RWMutex`,
  `sync.WaitGroup`, and `sync.Once` forbid this — `go vet` flags
  it). Always passed as `*Child`.
- `cfg` is set exactly once at `NewChild` and never reassigned.
- `cmd` and `pid` transition `nil → non-nil → nil` exactly once
  per `Child` lifetime. The first transition happens inside
  `Start`'s critical section; the second inside `Wait`'s
  `waitOnce.Do(...)` body.
- `stdoutRing` and `stderrRing` are allocated at `NewChild` and
  never reassigned. Their `Close()` method is idempotent and
  called by `Wait`.
- `childDone` is allocated **per `Start`** (not at `NewChild`);
  this mirrors `cmd` so that a fresh `Child` does not carry an
  already-closed channel.
- `waitOnce` is the **single** sync primitive that orders
  Wait's effects. Concurrent callers either run the once body
  (the winner) or skip it entirely (the losers, who immediately
  observe `cmd == nil` and return `ErrChildNotStarted`).

---

### `ChildConfig` — daemon launch configuration

The inputs needed to launch a `Child`. A by-value struct passed
into `NewChild`. Reference-shared slices are read-only from the
layer's perspective — callers may safely zero `Env` strings after
`Start` returns (the kernel has consumed them via `execve`).

**Fields (public)**:
| Field | Type | Purpose | Constraints |
|-------|------|---------|-------------|
| `Command` | `[]string` | The argv vector passed to `os/exec`. Element 0 is the absolute path to the daemon binary; subsequent elements are arguments. | At `Start`: `len ≥ 1` (else `ErrCommandEmpty`); `filepath.IsAbs(Command[0])` (else `ErrCommandPathRelative`). |
| `Env` | `[]string` | Environment variables in `KEY=VALUE` form, passed verbatim to `os/exec.Cmd.Env`. | The layer never inspects, logs, or copies values; consumed by kernel on `execve`. |
| `Dir` | `string` | The child's working directory. | Empty string means inherit the supervisor's CWD (matches `os/exec.Cmd.Dir` semantics). |
| `Stdout` | `io.Writer` | The operator-supplied sink for daemon stdout. Drained by a per-`Start` goroutine (R-014). May block indefinitely (Clarification 4). | `nil` is permitted and treated as `io.Discard`-equivalent (the drain goroutine simply discards). |
| `Stderr` | `io.Writer` | The operator-supplied sink for daemon stderr, same semantics as `Stdout`. | `nil` is permitted and treated as `io.Discard`-equivalent. |
| `Logger` | `*slog.Logger` | The structured logger for overflow warnings (R-006). | `nil` is a programmer error caught at `NewChild` — panic with `"supervise: NewChild requires a non-nil Logger"` (Constitution IX startup-wiring exemption). The `*slog.Logger` is the canonical stdlib handle per Constitution X. |

**Invariants**:
- `ChildConfig` is a passive input record; it has no methods and
  no internal invariants beyond the constraints above.
- The layer copies `cfg` by value into `Child`; subsequent
  caller mutations to the original `ChildConfig` do not affect a
  live `Child`. Slice mutations (e.g. clearing `Env` strings)
  ARE visible because slices share backing storage — but
  `os/exec` has already consumed `Env` by the time `Start`
  returns, so this is safe.

---

### `ExitDisposition` — three-tuple return from `Wait`

A conceptual entity (not a Go type — the chunk doc locks the
return as three positional values). Captured here for
documentation completeness.

| Component | Type | Meaning | Mutually exclusive with |
|----|----|----|----|
| `exitCode` | `int` | The integer the daemon passed to `exit(2)`. **Verbatim** — no remapping (FR-020-10). `78` indicates stale-credential per the locked `Exit78` constant (FR-020-9). | `signal != 0` |
| `signal` | `syscall.Signal` | The terminating signal if the daemon was killed by a signal. `0` (the zero value) when the daemon exited via `exit(2)`. | `exitCode != 0` (formally; an unsigned daemon return path always pairs with `signal == 0`) |
| `err` | `error` | Non-nil only when (a) `cmd.Wait()` itself returned an I/O error or (b) the cached cmd handle was already nil (post-`Wait` re-entry → `ErrChildNotStarted`). | (independent — orthogonal to `exitCode`/`signal`) |

**Invariants**:
- A successful first `Wait` returns either `(N, 0, nil)` or
  `(0, sig, nil)` where `sig != 0`. The two arms are mutually
  exclusive at the OS level: a process that was signal-terminated
  has no exit code in the conventional sense.
- A re-entrant or losing-race `Wait` returns `(0, 0,
  ErrChildNotStarted)` — the `(0, 0)` zero-pair signals "no
  data", and the `err` carries the reason.

---

### `ringBuffer` — bounded FIFO output buffer

Private to `package supervise`; not exported. One instance per
output stream (stdout/stderr) per `Child`. Backed by a fixed-size
byte slice (capacity 65 536 = 64 × 1024 — the chunk-doc constant)
and protected by a `sync.Mutex`.

**Fields (private)**:
| Field | Type | Purpose | Zero value |
|-------|------|---------|-----------|
| `mu` | `sync.Mutex` | Guards the byte slice and indices. | (zero-usable) |
| `buf` | `[]byte` | The fixed-capacity backing slice. Length is initial 0; capacity is 64 KB. | (allocated by `newRingBuffer`) |
| `cap` | `int` | The fixed capacity, set at construction. | `65536` |
| `streamLabel` | `string` | The label `"stdout"` or `"stderr"` used in overflow warnings (R-006). | (set at construction) |
| `logger` | `*slog.Logger` | The logger for overflow warnings (R-006). | (set at construction) |
| `atCapacity` | `bool` | Tracks whether the buffer is currently in an overflow episode (R-006). Transitions `false → true` on first overflow; emit one `slog.Warn`. Transitions `true → false` when occupancy drops below `cap`. | `false` |
| `notify` | `chan struct{}` | Buffered (capacity 1) signal channel: writers signal after each successful append; drain goroutine receives. Coalesces multiple writes into a single drain pass — that is intentional. | (allocated by `newRingBuffer`) |
| `closed` | `bool` | Set by `Close()`. Once closed, subsequent `Write` calls discard silently and `Read` drains the remainder + returns `io.EOF`. | `false` |

**Methods (private)**:
| Method | Signature | Purpose |
|----|----|----|
| `newRingBuffer` | `(streamLabel string, logger *slog.Logger) *ringBuffer` | Constructor. Pre-allocates the 64 KB backing slice. |
| `Write` | `(p []byte) (int, error)` | FIFO-evicting write. **Always** returns `(len(p), nil)` — never blocks, never short-writes. Triggers overflow `slog.Warn` on the `false → true` `atCapacity` transition. Signals `notify` non-blockingly. |
| `drain` | `(dst io.Writer) (int64, error)` | Drains the current contents to `dst` once. Called by the drain goroutine on `notify` ticks and on `childDone` close. Returns `io.ErrClosedPipe` after `Close()`. |
| `Close` | `() error` | Idempotent. Marks the ring closed; signals `notify` once to wake the drain goroutine. |

**Invariants**:
- Total backing-storage cost is `cap` bytes — `64 × 1024 = 65 536`
  (FR-020-12 "kilobyte-scale, not megabyte-scale"). Aggregate
  per `Child` is `2 × cap = 128 KB`.
- `Write` is non-blocking and constant-time relative to the
  caller (the FIFO eviction copies at most `cap` bytes per call
  — bounded). No syscall, no I/O.
- The `notify` channel is buffered to capacity 1: a non-blocking
  send on a full channel is idempotent (the drain goroutine will
  notice the work on its next iteration). This prevents writer
  back-pressure under bursty floods.
- `streamLabel` is one of `"stdout"` or `"stderr"` exactly —
  not free-form. The label is the only field that distinguishes
  the two ring buffers in overflow log lines.

---

### Sentinel errors

Three exported package-level sentinel `var`s, declared via
`errors.New(...)`.

| Sentinel | Returned when |
|----------|---------------|
| `ErrChildNotStarted` | Every "no live child" case: `Wait` re-entry; concurrent `Wait` losers (Clarification 1); `Forward` before `Start`; `Forward` after child exit (whether or not `Wait` returned). Detail: see R-011 + spec FR-020-11 + Edge Cases. |
| `ErrCommandEmpty` | `Start` rejection when `len(cfg.Command) == 0` (FR-020-2). |
| `ErrCommandPathRelative` | `Start` rejection when `cfg.Command[0]` is not `filepath.IsAbs` (FR-020-3). Distinct from `ErrCommandEmpty` to honour FR-020-3's distinctness clause. |

**Wrapping form** (FR-020-14, Constitution IX):

```go
ErrCommandPathRelative wraps as: fmt.Errorf("supervise: %w (got %q)", ErrCommandPathRelative, cmd[0])
ErrCommandEmpty wraps as:        fmt.Errorf("supervise: %w", ErrCommandEmpty)
ErrChildNotStarted wraps as:     fmt.Errorf("supervise: %w", ErrChildNotStarted)
```

Operators reading a log MUST be able to identify the rejection
class via `errors.Is(err, supervise.ErrXxx)` without string
matching.

**Invariants**:
- All three are package-level `var Err... = errors.New(...)` —
  sentinel-class read-only state, allowed under Constitution IX.
- No sentinel ever wraps an `Env` value, an output buffer, or
  any byte from the daemon (Constitution X — no secret values
  in errors).

---

### `Exit78` — stale-credential exit-code constant

A package-level `const Exit78 = 78`. The named constant for the
project-wide "my credentials are stale; refetch and restart"
contract (FR-020-9, Constitution V).

**Invariants**:
- The value is exactly `78` — the documented sysexits
  `EX_CONFIG`. Any change requires a SPEC amendment (this is the
  child→supervisor protocol).
- `const`, not `var` — operators compare `exitCode ==
  supervise.Exit78`, not `errors.Is(...)`. The constant is the
  audited "magic number alias" pattern — it gives the comparison
  a name without introducing a sentinel error path.

---

## Relationships

```text
   ┌──────────────────────────────────────────────────────────┐
   │              package supervise (extends SDD-19)          │
   │                                                          │
   │   const Exit78 = 78                                      │
   │   var ErrChildNotStarted, ErrCommandEmpty,               │
   │       ErrCommandPathRelative                             │
   │                                                          │
   │   ┌───────────┐                                          │
   │   │   Child   │  (RWMutex; WaitGroup; Once)              │
   │   │   ├ cfg          : ChildConfig                       │
   │   │   ├ cmd          : *exec.Cmd  (nil → live → nil)     │
   │   │   ├ pid          : int                               │
   │   │   ├ stdoutRing   : *ringBuffer ──┐                   │
   │   │   ├ stderrRing   : *ringBuffer ──┤                   │
   │   │   ├ forwardCh    : chan os.Signal│                   │
   │   │   ├ childDone    : chan struct{} │                   │
   │   │   ├ wg           : WaitGroup     │                   │
   │   │   └ waitOnce     : Once          │                   │
   │   └───────────┘                       │                   │
   │        │                              │                   │
   │        │ Start ──► spawn 2 (linux) /  │                   │
   │        │            3 (darwin) GR     ▼                   │
   │        │     ┌──────────────────────────────┐             │
   │        │     │ forwarding goroutine         │             │
   │        │     │   select on ctx.Done(),       │             │
   │        │     │   forwardCh, childDone        │             │
   │        │     ├──────────────────────────────┤             │
   │        │     │ stdout drain goroutine        │             │
   │        │     │   reads stdoutRing → cfg.Stdout │            │
   │        │     ├──────────────────────────────┤             │
   │        │     │ stderr drain goroutine        │             │
   │        │     │   reads stderrRing → cfg.Stderr │            │
   │        │     ├──────────────────────────────┤             │
   │        │     │ darwin death-watch goroutine  │ //go:build darwin
   │        │     │   kqueue EVFILT_PROC on PPID   │             │
   │        │     └──────────────────────────────┘             │
   │        │                                                  │
   │        │ Wait ──► waitOnce.Do { cmd.Wait();               │
   │        │              cache (exitCode, signal, err);      │
   │        │              clear cmd, pid;                     │
   │        │              close(childDone);                   │
   │        │              wg.Wait() }                         │
   │        │                                                  │
   │        ▼                                                  │
   │   (returns triple to caller; subsequent Wait/Forward      │
   │    return ErrChildNotStarted)                             │
   └──────────────────────────────────────────────────────────┘
                                                  │
                                                  │ consumed by
                                                  ▼
                                         SDD-21 refill / refresh / grace
                                         (wires Wait disposition into
                                          SDD-19 state-machine events)

                                         SDD-25 lifecycle harness
                                         (drives end-to-end Scenarios 2..5)
```

## Validation rules

| Rule | Source | Enforcement point |
|------|--------|-------------------|
| `cfg.Command` non-empty | FR-020-2 | `Start` body: `len(cfg.Command) == 0` → `ErrCommandEmpty` |
| `cfg.Command[0]` absolute path | FR-020-3 | `Start` body: `!filepath.IsAbs(cfg.Command[0])` → `ErrCommandPathRelative` (distinct from empty case) |
| Child placed in own process group | FR-020-4 | `cmd.SysProcAttr.Setpgid = true` (cross-platform; both linux + darwin) |
| Linux Pdeathsig | FR-020-5 (linux arm) | `child_linux.go`: `cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM` |
| Darwin death-watch (best-effort) | FR-020-5 (darwin arm) | `child_darwin.go`: `startDeathWatch` goroutine; SIGKILL gap documented (R-009) |
| Forwarding to PGID | FR-020-6 | `syscall.Kill(-cmd.Process.Pid, sig)` — negative PID = process group |
| Forwarding goroutine has explicit termination | FR-020-7 + Clarification 3 | `select { case <-ctx.Done(): … case <-childDone: … case sig := <-forwardCh: … }` |
| Three-tuple exit disposition | FR-020-8 | `Wait` returns `(int, syscall.Signal, error)` |
| Exit-78 surfaced verbatim | FR-020-9 | `cmd.ProcessState.ExitCode()` is returned untouched |
| Exit-78 not coerced to/from other codes | FR-020-10 | No remapping anywhere — the integer flows through |
| Post-`Wait` calls return `ErrChildNotStarted` | FR-020-11 | `Wait` clears `cmd` and `pid` under write lock; subsequent calls observe `cmd == nil` |
| Bounded buffers (kilobyte-scale) | FR-020-12 | `defaultRingBufferSize = 64 * 1024` per stream |
| FIFO eviction; no daemon-side block | FR-020-13 | `*ringBuffer.Write` returns `len(p), nil` always |
| One overflow warning per episode per stream | FR-020-13 + Clarification 5 | `*ringBuffer.atCapacity` flag tracked under mutex |
| All public errors comparable via `errors.Is` | FR-020-14 | Sentinel `var Err...` + `%w` wrapping |
| Concurrent-`Wait` race-clean | FR-020-15 + SC-020-7 | `sync.Once` + write-locked cmd-clear |
| First-caller-wins exit disposition | FR-020-15 + Clarification 1 | `sync.Once` body broadcasts exit only to first caller |
| No network / vault / Discord / audit / state-machine I/O | FR-020-16 | Code review; imports limited to `os`, `os/exec`, `syscall`, `path/filepath`, `context`, `errors`, `fmt`, `io`, `sync`, `sync/atomic`, `log/slog`, `time`, `golang.org/x/sys/unix` (darwin only). |
| 100 restart cycles → goroutine baseline | SC-020-6 | `wg.Wait()` inside `Wait`'s `Once.Do` body releases all per-`Start` goroutines |
| Darwin SIGKILL gap | R-009 | Documented; lifecycle harness skips with `t.Skip("R-009")` on darwin |

## Out of scope

- Persistence: `Child` is in-memory only.
- State-machine wiring: SDD-21 owns the conversion from `Wait`'s
  exit triple into SDD-19 `Event`s (e.g.
  `exitCode == Exit78 → EventChildExit78Stale`).
- Validator orchestration, refresh-window scheduling, grace-cache
  policy: SDD-21.
- Discord alert emission: SDD-21/SDD-25.
- PID file / flock split-brain guard: upstream of `Child`
  (Scenario 14 — separate chunk).
- Status socket: SDD-22 (consumes SDD-19 `Snapshot.ChildPID`,
  which is written by SDD-21 internal helpers, not by SDD-20).
- `cmd/test-helper-supervise` binary: not added (R-012 uses
  `os.Executable()` re-invocation).
- Windows: out of scope per spec Assumptions §1.
