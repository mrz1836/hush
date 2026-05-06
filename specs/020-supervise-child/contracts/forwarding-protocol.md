# Forwarding & Drain Goroutine Protocol — SDD-20

**Branch**: `020-supervise-child` | **Date**: 2026-05-05

This document captures the **goroutine lifecycle contracts** for
the per-`Start` goroutines spawned by `Child.Start`. It is the
single source of truth for "who starts what, when does it
terminate, and how is it joined". Reviewers can trace each
goroutine from spawn to terminate without reading source.

Constitution IX requires **every goroutine to have a clear owner,
an explicit cancellation path, and a documented termination
condition**. Each goroutine below satisfies all three.

---

## Goroutine roster

| # | Name | Spawn site | Termination condition | Joined via | Build tag |
|---|----|----|----|----|----|
| 1 | Forwarding goroutine | `Start` (after `cmd.Start()` returns nil) | `<-ctx.Done()` OR `<-c.childDone` (whichever first per Clarification 3) | `c.wg.Done()` deferred at top frame | (cross-platform) |
| 2 | Stdout drain goroutine | `Start` (after `cmd.Start()` returns nil) | `<-c.childDone` close (after final drain pass) OR pipe EOF | `c.wg.Done()` deferred at top frame | (cross-platform) |
| 3 | Stderr drain goroutine | `Start` (after `cmd.Start()` returns nil) | `<-c.childDone` close (after final drain pass) OR pipe EOF | `c.wg.Done()` deferred at top frame | (cross-platform) |
| 4 | Darwin death-watch | `startDeathWatch(ctx, c)` (called by `Start` on darwin only) | `<-ctx.Done()` OR `<-c.childDone` (woken via self-pipe) OR kqueue PPID NOTE_EXIT fires (after delivering SIGTERM to pgid) | `c.wg.Done()` deferred at top frame | `//go:build darwin` |

**Total active goroutines per live `Child`:**
- Linux: **3** (forwarding + 2 drain).
- Darwin: **4** (forwarding + 2 drain + death-watch).

**Total active goroutines per `Child` after `Wait` returns:** **0** —
all four (or three) are joined via `c.wg.Wait()` inside the
`waitOnce.Do` body. SC-020-6 (100 restart cycles → goroutine
baseline) is enforced at this seam.

---

## Forwarding goroutine — pseudocode

```go
// Spawned at the end of Start. Owner: *Child. Termination:
// ctx.Done() or childDone close (Clarification 3).
go func() {
    defer c.wg.Done()
    defer func() {
        // Constitution IX: every spawned goroutine recovers at
        // its top frame to surface panics without aborting the
        // supervisor process.
        if r := recover(); r != nil {
            c.cfg.Logger.Error("supervise: forwarding goroutine panicked",
                slog.Any("recovered", r))
        }
    }()

    pgid := c.pid // captured under read lock at spawn time
    for {
        select {
        case <-ctx.Done():
            return
        case <-c.childDone:
            return
        case sig, ok := <-c.forwardCh:
            if !ok {
                return
            }
            // Best-effort signal delivery; ESRCH (child gone)
            // and EPERM (race with Wait clearing) are both
            // legal terminal states.
            _ = syscall.Kill(-pgid, sig.(syscall.Signal))
        }
    }
}()
```

**Invariants**:
- The select arm ordering is fixed but Go's `select` is uniformly
  pseudo-random when multiple arms are ready; this is correct —
  we do not require fairness between exit and signal delivery.
- `pgid` is captured **once** at goroutine spawn (before any
  iteration), so the goroutine never reads `c.cmd.Process.Pid`
  after `Wait` clears `c.cmd`.
- The negative-PID `syscall.Kill(-pgid, sig)` is the canonical
  POSIX way to deliver a signal to every process in the group
  (FR-020-6).
- The signal-cast `sig.(syscall.Signal)` is safe: `Forward`'s
  signature accepts `os.Signal` for stdlib compatibility, but
  the only concrete implementation in stdlib that satisfies the
  interface is `syscall.Signal`. A non-syscall sender panics —
  acceptable; this is a programmer error.

---

## Drain goroutine (per stream) — pseudocode

```go
// Spawned at the end of Start. Owner: *Child. Termination: pipe
// EOF (child closed its stream) or childDone close.
go func(ring *ringBuffer, sink io.Writer) {
    defer c.wg.Done()
    defer func() { recover() }() // Constitution IX top-frame recover

    if sink == nil {
        sink = io.Discard
    }
    for {
        select {
        case <-c.childDone:
            // Final drain pass to flush any residual bytes.
            _, _ = ring.drain(sink)
            return
        case <-ring.notify:
            _, _ = ring.drain(sink) // may block on slow sink (Clarification 4)
        }
    }
}(c.stdoutRing, c.cfg.Stdout)
```

**Invariants**:
- The `sink.Write` call inside `ring.drain` may block
  indefinitely (Clarification 4). When it does, the goroutine
  parks; the `*ringBuffer.Write` continues to FIFO-evict
  oldest content; the daemon never blocks on its own
  `write(2)` syscalls.
- After `childDone` close, the goroutine performs exactly one
  final drain pass before returning. If the sink is still
  blocked, the final drain may park forever — but the test
  suite (T-08) covers the realistic case where sinks accept
  data and the goroutine returns.
- A blocked-sink edge case for `Wait`: if the drain goroutine
  is parked in `sink.Write` when `Wait` is called, `c.wg.Wait()`
  blocks. This is intentional — the caller is responsible for
  ensuring sinks unblock (typical: pipe to a `bytes.Buffer`
  or a tee to stderr). The spec does NOT require Wait to
  short-circuit a blocked drain.

---

## Darwin death-watch goroutine — pseudocode

```go
//go:build darwin

func startDeathWatch(ctx context.Context, c *Child) error {
    kq, err := unix.Kqueue()
    if err != nil {
        return fmt.Errorf("supervise: kqueue: %w", err)
    }

    // Self-pipe to break out of Kevent on ctx cancel / child exit.
    // The pipe's read-end is registered alongside the proc event
    // so a write to the pipe-write-end wakes Kevent.
    pr, pw, err := os.Pipe()
    if err != nil {
        unix.Close(kq)
        return fmt.Errorf("supervise: pipe: %w", err)
    }

    // Register two events: PPID exit + self-pipe readability.
    ppid := os.Getppid()
    events := []unix.Kevent_t{
        {Ident: uint64(ppid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT},
        {Ident: uint64(pr.Fd()), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_ENABLE},
    }
    if _, err := unix.Kevent(kq, events, nil, nil); err != nil {
        unix.Close(kq); pr.Close(); pw.Close()
        return fmt.Errorf("supervise: kevent register: %w", err)
    }

    // Goroutine that wakes the death-watch on ctx/childDone.
    c.wg.Add(1)
    go func() {
        defer c.wg.Done()
        defer func() { recover() }()
        select {
        case <-ctx.Done():
        case <-c.childDone:
        }
        _, _ = pw.Write([]byte{1}) // wake Kevent
        pw.Close()
    }()

    // The death-watch goroutine itself.
    c.wg.Add(1)
    go func() {
        defer c.wg.Done()
        defer func() { recover() }()
        defer unix.Close(kq)
        defer pr.Close()

        out := make([]unix.Kevent_t, 2)
        for {
            n, err := unix.Kevent(kq, nil, out, nil)
            if err != nil {
                if err == unix.EINTR {
                    continue
                }
                return
            }
            for i := 0; i < n; i++ {
                ev := out[i]
                if ev.Filter == unix.EVFILT_PROC && ev.Fflags&unix.NOTE_EXIT != 0 {
                    // PPID exited — send SIGTERM to child pgid.
                    c.mu.RLock()
                    cmd := c.cmd
                    pgid := c.pid
                    c.mu.RUnlock()
                    if cmd != nil && pgid != 0 {
                        _ = syscall.Kill(-pgid, syscall.SIGTERM)
                    }
                    return
                }
                if ev.Filter == unix.EVFILT_READ {
                    // Awakened by ctx/childDone; exit cleanly.
                    return
                }
            }
        }
    }()

    return nil
}
```

**Invariants**:
- Two darwin goroutines per `Start` — the death-watch itself and
  the small "wake on ctx/childDone" notifier — both joined via
  `c.wg`. The roster table at the top counts the death-watch
  pair as a single logical "goroutine #4" but the
  `runtime.NumGoroutine()` budget for SC-020-6 is the **sum**.
  Linux baseline = 3 per `Start`; darwin baseline = 5 per
  `Start`. Both return to zero after `Wait`.
- Per spec FR-020-5 + research R-009: this protocol is
  **best-effort**. If the supervisor itself is SIGKILL'd, both
  goroutines die with the process and cannot deliver SIGTERM
  to the pgid. The lifecycle harness (SDD-25) skips this
  test case on darwin with `t.Skip("R-009 known darwin limitation")`.
- The kqueue uses `EV_ONESHOT` for the proc event so it
  auto-deregisters on first fire — preventing a hot-loop if the
  PPID somehow re-fires.
- The self-pipe wake mechanism is the canonical Go pattern for
  "interrupt a blocking syscall on cancellation" without
  resorting to `runtime.LockOSThread` rituals.

---

## Bounded ring buffer write/drain protocol

The two drain goroutines and the `os/exec` writer goroutine (one
per pipe, owned by the `os/exec` stdlib code — NOT counted in our
goroutine roster because the stdlib joins them on `cmd.Wait()`)
interact with the `*ringBuffer` via:

```go
// os/exec writer side (called by stdlib goroutines):
n, err := ring.Write(buf[:nRead])
// ring.Write ALWAYS returns (len(p), nil) — never blocks, never
// short-writes, never errors on a healthy ring. A closed ring
// returns (0, io.ErrClosedPipe) but Wait closes only after the
// child's stdout/stderr have already EOF'd, so this path is
// terminal-only.

// drain goroutine side (our goroutine):
n, err := ring.drain(sink)
// drain copies the current contents to sink in one Write call.
// May block on sink (Clarification 4). Returns the byte count
// drained or io.ErrClosedPipe if the ring was Closed.
```

**Overflow accounting protocol** (R-006):

```go
func (r *ringBuffer) Write(p []byte) (int, error) {
    r.mu.Lock()
    defer r.mu.Unlock()
    if r.closed {
        return 0, io.ErrClosedPipe
    }

    overflowing := len(r.buf)+len(p) > r.cap
    if overflowing {
        // Drop oldest bytes to make room.
        overflow := len(r.buf) + len(p) - r.cap
        if overflow >= len(r.buf) {
            r.buf = r.buf[:0]
        } else {
            r.buf = r.buf[overflow:] // FIFO eviction
        }
        if !r.atCapacity {
            r.atCapacity = true
            r.logger.Warn("supervise: child output buffer overflowed",
                slog.String("stream", r.streamLabel))
        }
    }
    r.buf = append(r.buf, p...)
    select {
    case r.notify <- struct{}{}:
    default:
    }
    return len(p), nil
}

func (r *ringBuffer) drain(dst io.Writer) (int64, error) {
    r.mu.Lock()
    if r.closed && len(r.buf) == 0 {
        r.mu.Unlock()
        return 0, io.ErrClosedPipe
    }
    out := append([]byte(nil), r.buf...)
    r.buf = r.buf[:0]
    if r.atCapacity {
        r.atCapacity = false // episode ends when we drop below capacity
    }
    r.mu.Unlock()
    n, err := dst.Write(out)
    return int64(n), err
}
```

**Invariants**:
- `Write` always returns `(len(p), nil)` for an open ring — the
  daemon's stdlib write goroutine never sees a short-write,
  never blocks (FR-020-13).
- `atCapacity` transitions:
  - `false → true` only inside the `overflowing && !atCapacity`
    branch — exactly one warning per episode.
  - `true → false` only inside `drain` after the buffer is
    emptied — episode ends.
  - This produces exactly one warning per overflow episode per
    stream (Clarification 5, T-08b coverage).
- The `notify` channel is buffered to capacity 1; a non-blocking
  send is idempotent — multiple concurrent writes coalesce into
  one drain wake-up, which is correct: the drain pulls all
  available bytes per pass.

---

## Goroutine join sequence on `Wait`

```go
func (c *Child) Wait() (exitCode int, signal syscall.Signal, err error) {
    c.waitOnce.Do(func() {
        c.mu.RLock()
        cmd := c.cmd
        c.mu.RUnlock()
        if cmd == nil {
            c.exitErr = ErrChildNotStarted
            return
        }

        // Block on the daemon's exit. The os/exec internal copy
        // goroutines for stdout/stderr will EOF and exit during
        // cmd.Wait().
        cmd.Wait() // ignore error; we extract via ProcessState

        // Compute the exit triple from ProcessState.
        ps := cmd.ProcessState
        if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
            if ws.Signaled() {
                c.exitCode = 0
                c.exitSignal = ws.Signal()
            } else {
                c.exitCode = ws.ExitStatus()
                c.exitSignal = 0
            }
        } else {
            c.exitCode = ps.ExitCode()
            c.exitSignal = 0
        }
        c.exitErr = nil

        // Close the bounded rings (drain goroutines will perform
        // a final drain and exit).
        _ = c.stdoutRing.Close()
        _ = c.stderrRing.Close()

        // Broadcast child exit to all per-Start goroutines.
        close(c.childDone)

        // Clear the cached *exec.Cmd handle (FR-020-11) and pid.
        c.mu.Lock()
        c.cmd = nil
        c.pid = 0
        c.mu.Unlock()

        // Join all per-Start goroutines. Forwarding exits on
        // childDone close; drain goroutines exit after their
        // final drain pass; darwin death-watch exits on its
        // self-pipe wake.
        c.wg.Wait()
    })

    if c.exitErr != nil && errors.Is(c.exitErr, ErrChildNotStarted) {
        return 0, 0, c.exitErr
    }

    // Concurrent / re-entrant callers: the once body has already
    // run; they observe c.cmd == nil and return ErrChildNotStarted.
    c.mu.RLock()
    cmd := c.cmd
    c.mu.RUnlock()
    if cmd == nil && c.exitErr == nil {
        // We are NOT the once-winner. The first caller has
        // already taken the disposition.
        // BUT: we must distinguish "I am the once-winner who just
        // committed the disposition" from "another caller did".
        // The pattern below resolves it via a per-call flag set
        // inside Once.Do — see the actual implementation in
        // child.go for the full plumbing.
        return 0, 0, ErrChildNotStarted
    }
    return c.exitCode, c.exitSignal, c.exitErr
}
```

> **Note**: The pseudocode above sketches the join order. The
> actual implementation must distinguish "I am the once-winner"
> from "I am a concurrent loser" via a goroutine-local flag set
> inside `Once.Do(...)`. The sketch is intentionally simplified
> for readability; `child_test.go` T-09 verifies the exact
> Clarification 1 contract.

**Invariants**:
- After `Wait` returns to the once-winner, `runtime.NumGoroutine()`
  is back to baseline (SC-020-6) because `c.wg.Wait()` has
  observed all four (linux: three) per-`Start` goroutines exit.
- After `Wait` returns to a concurrent loser, the loser performs
  no `wg.Wait()` and observes the cleared `c.cmd` to return
  `ErrChildNotStarted` (Clarification 1).
- The `_ = c.stdoutRing.Close()` ordering matters: ring close
  precedes `close(c.childDone)`. This ensures the drain
  goroutine's final pass observes `closed == true && len(buf) == 0`
  → returns `io.ErrClosedPipe`, exits cleanly. If the ordering
  were reversed, the drain goroutine could race on the
  `<-childDone` arm and miss the final ring contents.
