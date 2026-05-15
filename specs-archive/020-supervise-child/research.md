# Phase 0 Research — SDD-20 Supervise Child Process Layer

**Branch**: `020-supervise-child` | **Date**: 2026-05-05

The Technical Context in `plan.md` carries no `NEEDS CLARIFICATION`
markers — the spec already absorbed Clarifications 1–5 from
2026-05-05 (concurrent `Wait` semantics, `ErrChildNotStarted` reuse
across "no live child" cases, forwarding-goroutine termination,
indefinitely-blocked sink contract, overflow-warning episode
budget), and the chunk doc (`docs/sdd/SDD-20.md`) locks every HOW
knob. Phase 0 records the resolved decisions so downstream phases
(`/speckit-tasks`, `/speckit-implement`) and reviewers can verify
each choice against the constitution and the SDD-20 contract
without re-deriving them.

Each entry uses the standard form: **Decision** / **Rationale** /
**Alternatives considered**.

---

## R-001 — Process management primitive: `os/exec.Cmd`, no shell

**Decision**: The child layer uses `os/exec.Command(cmd[0],
cmd[1:]...)` with the verbatim caller-supplied argv. **Never**
prepend `/bin/sh -c`, **never** call `LookPath`. `cmd.Path` is set
to `cmd[0]` directly so `os/exec` does not perform PATH resolution
on a relative name (which would defeat FR-020-1).

**Rationale**:
- Constitution IX requires native-first stdlib; `os/exec` is the
  canonical Go fork/exec wrapper.
- Spec FR-020-1 mandates "no shell parsing and no PATH lookup".
  Any shell prefix (a) creates a parse surface for argv injection
  if the caller drifts from `[]string` into a single string and
  (b) splits the process tree (shell parent + daemon child),
  defeating PGID delivery.
- Constitution XI bars introducing a process-management library
  for a need stdlib already covers.

**Alternatives considered**:
- `os.StartProcess` directly — works but loses the convenient
  `Cmd.SysProcAttr` plumbing and forces us to re-implement
  pipe-attaching. No upside.
- `github.com/google/shlex` + `/bin/sh -c` — explicitly forbidden
  by the chunk doc and FR-020-1.

---

## R-002 — Path validation: at `Start`, not at `NewChild`

**Decision**: `NewChild(cfg ChildConfig) *Child` is a pure value
constructor: it copies fields, allocates the two ring buffers, and
returns the handle. **No validation happens here.** `Start(ctx)`
performs the path-validation gate:

```go
if len(cfg.Command) == 0 {
    return ErrCommandEmpty
}
if !filepath.IsAbs(cfg.Command[0]) {
    return fmt.Errorf("supervise: %w (got %q)", ErrCommandPathRelative, cfg.Command[0])
}
```

**Rationale**:
- Validating at `Start` keeps `NewChild` cheap and panic-free.
- Failing at `Start` is the canonical Go idiom: "constructors don't
  fail; the operation fails." Mirrors `os/exec.Command` itself
  (which never errors at construction).
- Centralising validation at `Start` means a future caller that
  builds a `Child` from partial config and overwrites a field
  later still gets the same gate — no bypass surface.

**Alternatives considered**:
- Validating at `NewChild` and returning `(*Child, error)` —
  requires every caller to handle a constructor error for what is
  effectively a programmer-mistake input. Less idiomatic.
- Late validation inside `cmd.Start()` (relying on `os/exec`'s
  own ENOENT) — too late: the error then carries the OS's wording
  rather than our stable sentinel, and FR-020-14's `errors.Is`
  comparability is harder to honour cleanly.

---

## R-003 — Empty-command sentinel: `ErrCommandEmpty` (separate from `ErrCommandPathRelative`)

**Decision**: This chunk exports **three** sentinel errors:
`ErrChildNotStarted`, `ErrCommandPathRelative`, and
`ErrCommandEmpty`. The chunk doc's exported-API listing names only
the first two; SDD-20's plan **expands** the locked surface by one
sentinel to honour spec FR-020-3, which requires the empty-command
case to surface a "stable, comparable error **distinct from** the
empty-command case" relative-path counterpart.

**Rationale**:
- Spec FR-020-2 + FR-020-3 require **two distinct, comparable
  sentinels** — empty vs. non-absolute first element. A single
  `ErrCommandPathRelative` covering both would violate the
  distinctness clause; a single `ErrChildNotStarted` covering both
  would conflate config-validation errors with lifecycle errors.
- The `internal/supervise/config` subpackage (locked at SDD-18)
  already exports its own `ErrCommandEmpty` and
  `ErrCommandPathRelative`. SDD-20's child runner is **defence in
  depth** behind config validation: by the time `Start` runs, the
  config layer has already rejected empty commands. The child
  layer's sentinels are used only when a hand-built `ChildConfig`
  bypasses config validation (test code; future programmatic
  callers). Different package, different sentinel — a caller
  using `errors.Is(err, supervise.ErrCommandEmpty)` does not
  match `config.ErrCommandEmpty` and vice versa, which is the
  correct behaviour (different layers, different error
  identities).
- Adding the third sentinel is **additive** to the chunk doc's
  locked API and does not break any future caller. Reviewers
  get a clean trace from FR-020-3 → R-003 → exported sentinel.

**Alternatives considered**:
- Re-export `config.ErrCommandEmpty` — couples two layers that the
  package boundary deliberately separates and forces a downstream
  type-check on which layer raised the error.
- Map both empty + relative to `ErrCommandPathRelative` —
  violates FR-020-3 distinctness.
- Map empty to `ErrChildNotStarted` — conflates "bad config
  caught at Start" with "child handle has been released after
  Wait". Operators reading logs would lose information.

---

## R-004 — Concurrency model for `Wait`: `sync.Once` + cached result, others get `ErrChildNotStarted`

**Decision**: `Child` carries a `sync.Once` guarding `cmd.Wait()`
and a cached `(exitCode, signal, err)` triple. The first caller to
invoke `Wait()` runs the `Once.Do(...)` body — which calls
`cmd.Wait()`, populates the cached triple, releases the `*exec.Cmd`
handle (sets `c.cmd = nil` under the write lock), and signals a
`done` channel. **Every subsequent caller** — whether already
blocked on a concurrent `Wait()` or arriving sequentially after the
first `Wait` returns — observes `c.cmd == nil` under the read lock
and returns `(0, 0, ErrChildNotStarted)`.

This satisfies spec Clarification 1 verbatim: "First caller
receives the exit disposition; all other concurrent callers
receive `ErrChildNotStarted` (or equivalent 'child no longer live'
sentinel)."

**Rationale**:
- `sync.Once` is the idiomatic Go primitive for "exactly one
  caller does the work". It serialises without spinning and
  composes cleanly with the `*exec.Cmd` handle release ordering.
- Caching the triple to broadcast it to all callers was
  considered and **rejected** by Clarification 1 — the contract
  is "exactly one caller gets it; the rest get `ErrChildNotStarted`".
- The cached `*exec.Cmd` is cleared inside the `Once.Do(...)` body
  immediately after `cmd.Wait()` returns. FR-020-11 forbids
  caching the released child handle (Constitution IX echo: "no
  caching of released resources"). The cache release is
  write-locked so concurrent `Forward` calls observe the
  monotonic transition `cmd != nil → cmd == nil` without race.

**Alternatives considered**:
- A buffered `chan` of capacity 1 holding the exit triple —
  lets one caller receive it; subsequent receives block forever.
  Worse than the sentinel return.
- Broadcast via a `chan struct{}` close + cached triple —
  violates Clarification 1.
- A `sync.Mutex` + a `bool waited` flag — works but `sync.Once`
  is the precise semantic. Mutex requires hand-rolling
  exactly-once.

---

## R-005 — Bounded ring buffer: 64 KB lock-protected per stream, FIFO eviction

**Decision**: Each of stdout/stderr is wrapped in a private
`*ringBuffer` of capacity 65 536 bytes (64 × 1024, the chunk doc
constant). The buffer is allocated at `NewChild` (zero-value
`Child` is invalid; constructor is the contract entry point) and
lives for the `Child`'s entire lifetime. Writes from `os/exec`'s
goroutine-driven copy loop call `ringBuffer.Write([]byte) (int,
error)` which:
- Acquires a `sync.Mutex`.
- If `len + p > cap`: drop the oldest `(len + p - cap)` bytes from
  the head, accept the full `p`, return `(len(p), nil)`.
- If `len + p ≤ cap`: append the full `p`, return `(len(p), nil)`.
- **Always** returns `len(p)` so the daemon's `os/exec` write loop
  never sees a short-write and never blocks (FR-020-13 no-deadlock
  invariant).

A separate **drain goroutine** per stream copies bytes out of the
ring into the operator-supplied `cfg.Stdout`/`cfg.Stderr`
`io.Writer`. The drain goroutine uses a buffered notify channel
(capacity 1) which the writer signals after every successful
write. The drain goroutine's `Write` call to the operator-supplied
sink **may block indefinitely** — that is exactly the contract
(spec Clarification 4): the daemon writes into the ring (which
NEVER blocks), the drain races to copy out, and a slow/blocked
sink causes ring overflow + FIFO eviction without back-propagating
to the daemon. The drain goroutine terminates on pipe EOF (i.e.
when the child's stdout/stderr closes — typically at child exit)
plus `ctx.Done()` early-exit.

**Rationale**:
- A lock-protected ring is the simplest data structure that meets
  all four contracts: bounded memory (FR-020-12 — 64 KB per
  stream is "kilobyte-scale" not "megabyte-scale"), non-blocking
  writes (FR-020-13), FIFO eviction (FR-020-13), thread-safe
  read/write (drain goroutine reads while pipe writer writes).
- A `bytes.Buffer` would grow unbounded.
- A `chan []byte` of fixed capacity would either block writers
  when full (violates FR-020-13) or drop **newest** content (LIFO
  eviction; violates "drop the oldest").
- A `sync.RWMutex` was considered for the ring, but writes
  outnumber reads under any high-volume scenario; a plain `Mutex`
  is faster on contention.

**Alternatives considered**:
- Pipe directly to the operator `io.Writer` with no buffer — the
  spec rejects this: a slow/blocked sink would back-pressure
  through `os/exec`'s copy loop into the daemon's `write(2)`
  syscalls, defeating the no-deadlock invariant (Clarification 4).
- A larger 1 MB ring — violates FR-020-12's "kilobyte-scale".
- A `lock-free queue` like Yi Chen's MPMC ring — overengineered
  for a 1-writer-1-reader scenario and adds a dependency.

---

## R-006 — Overflow warning rate-limit: episode-based, single `slog.Warn` per episode per stream

**Decision**: Each `*ringBuffer` holds a private `bool atCapacity`
flag and a `*slog.Logger` reference. The flag transitions:
- `false → true` when a write would overflow (reaches capacity
  with bytes dropped). On this transition, emit
  `logger.Warn("supervise: child output buffer overflowed",
  slog.String("stream", "stdout"|"stderr"))`.
- `true → false` when a subsequent successful drain reduces the
  occupancy strictly below capacity.

While `atCapacity == true`, every additional write that drops
bytes is silent (no log line). The next overflow episode (after a
drain below cap → cap again) produces exactly one new warning.

This satisfies spec Clarification 5 verbatim: "At most one
warning per overflow episode per stream, where an episode is a
continuous period during which the buffer remains at capacity and
resets when the buffer drains below full."

**Rationale**:
- A "one warning per dropped byte" or "one per write" model would
  produce `O(daemon write rate)` warning lines under sustained
  flood — itself a denial-of-service against the operator log
  surface. Constitution X's "logs are not noise" principle
  applies.
- An episode boundary is the natural correlation unit: an
  operator sees "stdout overflowed" once and knows the bounded
  buffer is doing its job; the next episode warning means the
  flood resumed after a quiet interval — actionable signal.
- Token-bucket or time-windowed throttling was considered and
  rejected because it requires a clock + parameter that adds
  knobs the spec does not require. The episode model is
  parameter-free.

**Alternatives considered**:
- Token-bucket with a 1-per-minute budget — adds two knobs
  (window, burst) the spec does not justify.
- One warning per `len(ring)/2` bytes dropped — arbitrary and
  inconsistent with Clarification 5.
- One warning per stream lifetime (i.e. only the first overflow
  ever) — loses signal for a daemon that recovers and re-floods.

---

## R-007 — Exit disposition: `(exitCode int, signal syscall.Signal, err error)` from `cmd.ProcessState`

**Decision**: `Wait` blocks on `cmd.Wait()`, then derives the
disposition from `cmd.ProcessState`:

```go
ps := cmd.ProcessState
if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
    if ws.Signaled() {
        return 0, ws.Signal(), nil
    }
    return ws.ExitStatus(), 0, nil
}
return ps.ExitCode(), 0, nil // fallback (non-unix; not v0.1.0 platform)
```

`ExitCode()` returns the actual integer the daemon passed to
`exit(2)`, including 78 (FR-020-9, FR-020-10). The signal is the
zero value when the daemon exited via `exit(2)`. The `err`
component is non-nil only when `cmd.Wait()` itself returned an
I/O error or the cached cmd handle was already nil
(post-`Wait` re-entry case → `ErrChildNotStarted`).

**Rationale**:
- The three-tuple is the locked spec contract (FR-020-8).
- Distinguishing exit-by-status from exit-by-signal is the
  bedrock contract on which the supervisor state machine
  depends (User Story 1, Why-this-priority).
- `cmd.ProcessState.Sys()` returns the platform-specific
  `WaitStatus`; on darwin and linux this is
  `syscall.WaitStatus`. The cast-and-fallback pattern is the
  idiomatic Go way to reach the fields the stdlib `ProcessState`
  abstraction does not surface directly.
- `Exit78` is a `const`, not a sentinel `var`. Callers do
  `if exitCode == supervise.Exit78` (FR-020-9). The constant is
  in `child.go` (cross-platform) — there is no platform-specific
  numbering.

**Alternatives considered**:
- Returning a struct `ExitDisposition{Code, Signal, Err}` —
  cleaner ergonomics but the chunk doc locks the three-return
  signature, and changing it would force every test and SDD-21
  call site.
- Returning `*os.ProcessState` directly — leaks an `os` type
  into the supervisor surface and forces every caller to dig
  into `Sys()`.

---

## R-008 — No new fuzz target at this layer

**Decision**: SDD-20 introduces no new fuzz target. The
mandatory fuzz catalogue (Constitution VIII §6) lists six
targets; SDD-20 maps to none of them.

**Rationale**:
- Fuzzing is required for "parsers and crypto entry points"
  (Constitution VIII). The child runner has no parser surface:
  commands arrive as a typed `[]string` from validated config
  (SDD-18), signals as typed `os.Signal`, exit codes as typed
  `int`. A caller passing nonsense is exercised by the
  table-driven tests in `child_test.go` (T-04 empty / T-05
  relative), not by fuzz.
- Fuzz #5 (supervisor TOML) is owned by SDD-18 (already shipped).
- No other fuzz target maps onto this layer.

**Alternatives considered**:
- A "fuzz the ring buffer's `Write` against random byte sizes" —
  exhaustively covered by deterministic property tests in T-08
  (stdout flood) without the fuzz harness overhead.

---

## R-009 — Darwin death-watch via `kqueue`/`EVFILT_PROC`: best-effort, documented limitation

**Decision**: `child_darwin.go` spawns a third per-`Start`
goroutine — the **death-watch** goroutine — alongside the
forwarding and drain goroutines. The goroutine:
1. Opens a kqueue via `unix.Kqueue()`.
2. Registers an `EVFILT_PROC | NOTE_EXIT` event on the
   supervisor's parent PID (`os.Getppid()`) — a best-effort signal
   that the surrounding launchd/systemd-equivalent context has
   collapsed.
3. Blocks in `unix.Kevent(kq, nil, events, nil)`.
4. On wake (parent exit), issues
   `syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)` to deliver
   SIGTERM to the **child's process group** (PGID == child PID
   because `Setpgid: true` makes the child its own group leader).
5. Terminates on `ctx.Done()` (woken via a self-pipe `EVFILT_READ`
   registered alongside the proc event), or on child exit
   (signalled via the same self-pipe by `Wait`).

**Known limitation** — and this is the honest part: the goroutine
runs **inside the supervisor process**. If the supervisor itself
is `SIGKILL`'d, the goroutine dies with the process and cannot
deliver the cleanup signal. This is a Darwin-specific gap
relative to spec FR-020-5 ("by any means, including abnormal
termination that prevents user-space cleanup").

**Mitigation in v0.1.0**:
- **Linux** (the production-priority target — this is where
  `hush supervise` runs in real deployments per OPERATIONS.md):
  the kernel-enforced `Pdeathsig` mechanism delivers SIGTERM
  unconditionally on parent death (R-010).
- **Darwin** (developer workstation only): The goroutine handles
  graceful supervisor exits (panic with `recover` at the
  forwarding goroutine top frame, normal `cmd Wait` cleanup, and
  `os.Interrupt`-induced shutdown). The **SIGKILL gap** is
  documented in `quickstart.md` and a `// TODO(SDD-20-darwin):`
  comment in `child_darwin.go`. v0.1.1 may revisit by adding a
  guardian-subprocess pattern (a mini `os.StartProcess` of the
  hush binary in a special "deathwatch" mode that becomes the
  child's parent and survives the supervisor's death).

**Rationale**:
- The chunk doc explicitly mandates "separate goroutine that uses
  kqueue" — this plan honours the mandate while documenting the
  inherent limitation. Adding a guardian subprocess in v0.1.0
  would (a) double the syscall surface and (b) require the hush
  binary to dispatch into a dedicated subcommand on a private
  env-var trigger, which crosses layers (CLI dispatch belongs to
  SDD-15+). Defer to v0.1.1.
- Constitution VIII requires that what we ship is **honest**
  about its guarantees. R-009 records the gap so the lifecycle
  harness (SDD-25) tests for it on linux (where Pdeathsig works)
  and is permitted to skip the SIGKILL-supervisor case on darwin
  with a clear `t.Skip("R-009 known darwin limitation")`.

**Alternatives considered**:
- Watch the supervisor's **own PID** via kqueue from inside the
  supervisor — useless: the goroutine dies with the process it
  watches.
- A "guardian subprocess" pattern (double-fork; child has the
  pgid; survives supervisor death; reacts via kqueue on PPID
  reparenting to init) — correct and complete, but doubles the
  process tree and adds CLI dispatch surface. Deferred to v0.1.1.
- Use a self-pipe so the supervisor can graceful-cleanup the
  child before exiting — already the linux Pdeathsig path on
  graceful exit; the Darwin gap is specifically the
  ungraceful-exit case where no Go code runs.
- Rely on launchd's `KeepAlive` policy — out of scope (operator
  configuration, not runtime code).

---

## R-010 — Linux `Pdeathsig` via `SysProcAttr`: kernel-enforced, no goroutine needed

**Decision**: `child_linux.go` populates
`cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM` at `Start` time
(in addition to the cross-platform `Setpgid: true`). This kernel
attribute causes the Linux kernel to deliver SIGTERM to the child
when its parent (the supervisor) exits, by **any** means
including SIGKILL. No supervisor-side goroutine is required.

The `child_linux.go` `startDeathWatch` helper is a no-op — it
returns nil immediately. The platform-specific seam keeps the
cross-platform `Start` body uniform.

**Rationale**:
- `Pdeathsig` is the canonical Linux primitive for parent-death
  cleanup; it is kernel-enforced and survives every supervisor
  termination path.
- Setting it via `SysProcAttr` is the idiomatic Go way; the
  stdlib `syscall.SysProcAttr` exposes the field directly on
  Linux build-tagged code paths.
- The no-op `startDeathWatch` keeps the cross-platform code
  symmetric — `child.go` calls `startDeathWatch(ctx, c)`
  unconditionally; the build-tagged file decides whether to
  spawn a real goroutine.

**Alternatives considered**:
- `prctl(PR_SET_PDEATHSIG, SIGTERM)` from a goroutine inside the
  child after fork — would require either CGO or a `runtime.LockOSThread`
  + raw `syscall.RawSyscall` ritual, both more fragile than
  `SysProcAttr.Pdeathsig`.
- A linux self-pipe death-watch goroutine paralleling the darwin
  approach — works but is strictly worse than the kernel
  primitive, and adds a goroutine that would have to be joined
  on cleanup.

---

## R-011 — Sentinel error semantics: `ErrChildNotStarted` covers all "no live child" cases

**Decision**: Per spec Clarification 2 + FR-020-11, the single
sentinel `ErrChildNotStarted` covers every "no live child"
condition:

| Caller scenario | Returned error |
|----|----|
| `Wait()` before `Start()` | `ErrChildNotStarted` |
| `Wait()` after a successful `Start()` and a prior successful `Wait()` (sequential re-entry) | `ErrChildNotStarted` |
| `Wait()` concurrent with another `Wait()` that wins the `sync.Once` race | `ErrChildNotStarted` (Clarification 1) |
| `Forward(sig)` before `Start()` | `ErrChildNotStarted` |
| `Forward(sig)` after the child has exited but before `Wait()` returned | `ErrChildNotStarted` (per spec Edge Case "signal forwarded after the daemon has already exited") |
| `Forward(sig)` after `Wait()` returned | `ErrChildNotStarted` |
| `PID()` before `Start()` | returns `0` (per spec; `int` zero value) — **NOT** an error path |
| `PID()` after `Wait()` returned | returns `0` (cached pid is cleared post-Wait alongside the `*exec.Cmd` handle, per FR-020-11's "MUST NOT cache the released child handle") |

`Forward` detects "no live child" by reading the child handle
under the read lock; if `c.cmd == nil`, return
`ErrChildNotStarted` immediately. The check is monotonic — once
`c.cmd` is cleared, it never returns to non-nil for this
`Child` instance.

**Rationale**:
- Clarification 2 verbatim: "Reuse `ErrChildNotStarted` for every
  'no live child' case (never-started, post-Wait,
  post-exit-not-yet-Waited); no distinct `ErrChildExited`
  sentinel."
- A single sentinel simplifies caller branches: the supervisor's
  refill code does `errors.Is(err, supervise.ErrChildNotStarted)`
  once and routes to the "spawn a fresh child" path regardless of
  the underlying cause. Multiple sentinels would force a
  cause-discrimination branch with no behavioural difference.
- `PID()` returning `0` (instead of an error) preserves the
  scalar-read shape and matches `os.Process.Pid` conventions.
  Callers who care about "is there a live PID" check `pid != 0`.

**Alternatives considered**:
- Distinct `ErrChildExited` sentinel — rejected by Clarification 2.
- `Forward` returning `nil` silently when no child is live —
  rejected by spec Edge Case "signal forwarded after the daemon
  has already exited"; the case must surface an error rather
  than panic or silently succeed.

---

## R-012 — Test-helper protocol: re-invoke the test binary via `os.Executable()`

**Decision**: Tests that need a child process (every test
covering `Start`/`Wait`/`Forward`/PGID/Exit78) re-invoke the test
binary itself via `os.Executable()` with a private env-var switch
that triggers `TestMain` to dispatch into the helper code path.

Skeleton (in `child_test.go`):

```go
func TestMain(m *testing.M) {
    switch os.Getenv("HUSH_CHILD_TEST_HELPER_MODE") {
    case "exit78":
        os.Exit(78)
    case "sigterm-trap":
        // install handler; print "SIGTERM_TRAPPED"; exit 0 on SIGTERM
        ...
    case "stdout-flood":
        // emit > 64KB to stdout
        ...
    case "spawn-grandchild":
        // exec /bin/sleep 30 in the same pgid; print PID; wait
        ...
    case "":
        os.Exit(m.Run())
    default:
        fmt.Fprintln(os.Stderr, "unknown helper mode")
        os.Exit(2)
    }
}
```

Tests build a `ChildConfig.Command = []string{exePath,
"-test.run=^$"}` with `Env = []string{"HUSH_CHILD_TEST_HELPER_MODE=exit78"}`.
The `-test.run=^$` selector ensures the re-invoked test binary
runs no Go tests; the `TestMain` switch fires before
`m.Run()` and dispatches into the helper.

**Rationale**:
- No new binary in the repo; no new build target. The test
  binary is already produced by `go test`.
- Constitution VIII pattern: tests are self-contained.
- Mirrors the pattern used by `os/exec` itself in its stdlib
  tests.

**Alternatives considered**:
- A `cmd/test-helper-supervise` binary — adds a real `cmd/`
  directory entry (Constitution IX requires `cmd/hush` to be the
  only public binary surface) and a `go build` target that does
  not exist in production.
- Calling `/bin/sh` with embedded scripts — violates "no shell
  parsing" spirit and is platform-fragile (BSD sh on darwin vs
  GNU sh on linux differ in `trap` semantics).
- Calling `/bin/sleep`, `/bin/echo` directly with no controlled
  exit-code — works for some tests but cannot cover Exit78 or
  SIGTERM-trap scenarios.

---

## R-013 — Forwarding goroutine architecture: `select` over `ctx.Done()`, `forwardCh`, and `childDone`

**Decision**: The forwarding goroutine is started by `Start`
after `cmd.Start()` returns successfully. Its body:

```go
for {
    select {
    case <-ctx.Done():
        return
    case <-c.childDone:
        return
    case sig := <-c.forwardCh:
        _ = syscall.Kill(-c.cmd.Process.Pid, sig.(syscall.Signal))
        // ignore error: ESRCH (child already gone) and EPERM
        // (race with Wait closing) are both legal terminal states
    }
}
```

`Forward(sig os.Signal) error` is the public entry point:

```go
func (c *Child) Forward(sig os.Signal) error {
    c.mu.RLock()
    cmd := c.cmd
    c.mu.RUnlock()
    if cmd == nil {
        return ErrChildNotStarted
    }
    select {
    case c.forwardCh <- sig:
        return nil
    default:
        // forwardCh is buffered to capacity 1; a full channel
        // means the previous signal hasn't been delivered yet.
        // Spec doesn't require coalescing; we surface the
        // backpressure as ErrChildNotStarted only if the channel
        // closes (which we never do) — otherwise we block briefly.
        c.forwardCh <- sig
        return nil
    }
}
```

`childDone` is a `chan struct{}` closed inside the `Once.Do`
body of `Wait` immediately after `cmd.Wait()` returns. This
guarantees the forwarding goroutine terminates either on:
- `ctx` cancellation (caller-driven shutdown); OR
- child exit observed by `Wait` (whether voluntary, by signal,
  or by parent-death cleanup).

Per spec Clarification 3 verbatim: "the goroutine exits on
context cancellation OR child process exit, whichever comes
first; this guarantees no leak across daemon restarts within a
long-lived supervisor context."

**Rationale**:
- Three select arms is the minimum that meets Clarification 3.
- Closing `childDone` (rather than sending on it) lets multiple
  per-`Start` goroutines (forwarding + drain + darwin
  death-watch) all observe child exit via the same broadcast.
- A buffered `forwardCh` of capacity 1 is sufficient: in
  practice the supervisor sends at most a small number of
  signals per child lifetime (SIGHUP, SIGTERM); the buffer
  absorbs a single in-flight signal while the forwarding
  goroutine is between iterations. No coalescing needed.

**Alternatives considered**:
- Synchronous `Forward` that calls `syscall.Kill` directly from
  the caller's goroutine — reasonable but loses the
  ctx-cancellation termination property the spec requires
  (Clarification 3): a synchronous call has no goroutine to
  terminate. The chunk doc explicitly mandates "dedicated
  goroutine started in Start".
- An unbuffered `forwardCh` — would block `Forward` until the
  goroutine receives, which is fine but adds latency under
  load. Buffer of 1 covers the common case without waiting.

---

## R-014 — Drain goroutine architecture: per-stream, terminates on pipe EOF

**Decision**: For each of stdout/stderr, `Start` launches one
drain goroutine. Each:
1. Holds a `*ringBuffer` and the operator-supplied `io.Writer`
   sink (`cfg.Stdout` / `cfg.Stderr`).
2. Selects on a notify channel (`*ringBuffer` signals after each
   successful write) and `c.childDone`.
3. On notify, copies the ring contents into the sink. If the
   sink blocks indefinitely, the goroutine sits in `Write`; the
   ring continues to overwrite oldest content until child exit
   closes the pipe (which closes the ring) and the drain
   goroutine drains the remainder and returns.
4. On `c.childDone` close, drains the ring once more (final
   bytes) and returns.

**Rationale**:
- One goroutine per stream is the minimum that decouples slow
  sinks from the daemon's writes. Two goroutines per `Start`
  for drain (plus forwarding, plus darwin death-watch) is
  consistent with FR-020-7's "no leak across restarts" once
  joined via a `sync.WaitGroup` released by `Wait`.
- Pipe EOF (child closes its stdout/stderr at exit) is the
  canonical termination signal in stdlib `os/exec` patterns;
  the `*ringBuffer.Close()` plumbing lets the goroutine
  observe it.

**Alternatives considered**:
- A single drain goroutine handling both streams via select on
  two notify channels — correct but harder to reason about for
  stream-correlated overflow accounting.
- No drain goroutine; let `os/exec` write directly to the
  operator's sink — already rejected in R-005.

---

## R-015 — Test fixtures inline; no new `internal/testutil` helpers

**Decision**: All tests live in `internal/supervise/{child_test,
child_linux_test, child_darwin_test}.go`. The
`HUSH_CHILD_TEST_HELPER_MODE` switch (R-012) and any small
inline helpers (e.g. `mustStartChild(t, cfg)`) are unexported
declarations in `child_test.go`. This mirrors SDD-18's pattern
(R-011 in SDD-19).

**Rationale**:
- Locking the test surface to one file mirrors prior chunk
  contracts. Future chunks (SDD-21+) may refactor shared
  helpers if reuse pressure emerges; this chunk does not
  pre-bake an abstraction.
- Inline fakes keep the test files self-contained and
  reviewable without cross-file navigation.

**Alternatives considered**:
- Adding `internal/testutil/childhelper.go` — defers a useful
  helper to a chunk that has only one consumer; YAGNI.
- A separate `cmd/test-helper-supervise` binary — already
  rejected in R-012.

---

## Summary

All `NEEDS CLARIFICATION` markers from the spec have been
resolved (Clarifications 1–5 are part of the spec; R-001 through
R-015 record HOW decisions). No new third-party dependencies, no
new fuzz targets, three goroutines per `Start` with explicit
termination paths joined via `sync.WaitGroup` released in
`Wait`. The Darwin SIGKILL gap (R-009) is documented honestly;
Linux Pdeathsig provides the FR-020-5 guarantee; the lifecycle
harness (SDD-25) will exercise both paths.

The chunk is ready for Phase 1 design.
