# Locked Go API — `internal/supervise` (SDD-20 child runner)

**Branch**: `020-supervise-child` | **Date**: 2026-05-05

This is the contractual surface this chunk locks into the
codebase. The exact Go signatures, constant values, error
sentinels, and platform-specific seams below are non-negotiable
for the duration of v0.1.0 unless a SPEC amendment changes them.

Path: `github.com/mrz1836/hush/internal/supervise`

The cross-platform symbols live in `child.go`. Platform-specific
behaviour is encapsulated in `child_linux.go` (`//go:build linux`)
and `child_darwin.go` (`//go:build darwin`). No SDD-18 (`config/`)
or SDD-19 (`state.go`) symbol is altered.

---

## Cross-platform — `child.go`

```go
package supervise

import (
    "context"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "os"
    "os/exec"
    "path/filepath"
    "sync"
    "syscall"
)

// ---------- Sentinel errors ----------

// ErrChildNotStarted is returned (wrapped) by Wait, Forward, and any
// other operation that requires a live child process when no child
// is running. Per spec Clarification 2 + FR-020-11, this single
// sentinel covers every "no live child" condition: never started,
// post-Wait re-entry, signal forwarded after the child has exited
// but before Wait returned, and concurrent-Wait callers who lost
// the sync.Once race (Clarification 1). Identifiable via errors.Is.
var ErrChildNotStarted = errors.New("supervise: child not started")

// ErrCommandEmpty is returned (wrapped) by Start when
// len(cfg.Command) == 0 (FR-020-2). Distinct from
// ErrCommandPathRelative per FR-020-3.
var ErrCommandEmpty = errors.New("supervise: command empty")

// ErrCommandPathRelative is returned (wrapped) by Start when
// cfg.Command[0] is not an absolute path (FR-020-3). Distinct from
// ErrCommandEmpty.
var ErrCommandPathRelative = errors.New("supervise: command path not absolute")

// ---------- Stale-credential exit-code ----------

// Exit78 is the project-wide stale-credential exit-code contract
// (FR-020-9, Constitution V). A daemon that exits with this code
// signals "my credentials are stale; refetch and restart". The
// child layer surfaces the value verbatim via Wait's exitCode
// return; callers compare against this constant rather than a
// magic number.
const Exit78 = 78

// ---------- ChildConfig ----------

// ChildConfig is the input to NewChild. Reference-shared slice
// fields (Command, Env) are read-only from the layer's
// perspective. The layer never copies, logs, or inspects Env
// values (Constitution X — type-driven redaction at the boundary).
//
// Logger is the canonical *slog.Logger handle; nil panics at
// NewChild (Constitution IX startup-wiring exemption).
//
// Stdout / Stderr may be nil — the layer treats nil as
// io.Discard-equivalent; the drain goroutine simply discards.
//
// Stdout / Stderr may also block indefinitely without affecting
// daemon writes (spec Clarification 4): the bounded ring buffer
// absorbs the burst with FIFO eviction.
type ChildConfig struct {
    Command []string      // argv; element 0 absolute path
    Env     []string      // KEY=VALUE pairs; consumed by execve
    Dir     string        // working directory; "" inherits supervisor CWD
    Stdout  io.Writer     // stdout sink; nil → discard
    Stderr  io.Writer     // stderr sink; nil → discard
    Logger  *slog.Logger  // structured logger; non-nil required
}

// ---------- Child ----------

// Child is a handle to a single supervised daemon process. Single-
// use: once Wait returns the exit disposition, the cached
// *exec.Cmd is cleared (FR-020-11) and every subsequent call
// returns ErrChildNotStarted. To launch another daemon, construct
// a fresh Child via NewChild.
//
// Owns no goroutines at rest. Per Start: spawns the forwarding
// goroutine, two drain goroutines (stdout/stderr), and (on darwin)
// the death-watch goroutine. All four are joined via a per-Start
// sync.WaitGroup that Wait blocks on after cmd.Wait() returns; on
// Wait's return, runtime.NumGoroutine() is back to baseline
// (SC-020-6).
//
// Safe for concurrent Wait, Forward, and PID from many goroutines.
// Concurrent Wait callers: exactly one wins the sync.Once race and
// returns the exit disposition; every other concurrent caller
// returns (0, 0, ErrChildNotStarted) per Clarification 1.
type Child struct {
    // (private fields — see data-model.md)
}

// NewChild constructs a Child handle from cfg. Pure value
// constructor: no validation, no syscalls. Allocates two ring
// buffers of capacity defaultRingBufferSize (64 KB) for the
// stdout/stderr streams. Panics if cfg.Logger is nil
// (Constitution IX startup-wiring exemption).
func NewChild(cfg ChildConfig) *Child

// Start launches the daemon. Validates cfg.Command (returns
// ErrCommandEmpty or ErrCommandPathRelative on failure), then
// invokes cmd.Start() with SysProcAttr.Setpgid = true plus
// platform-specific death-watch attributes (Pdeathsig on linux;
// kqueue goroutine on darwin). Spawns the forwarding goroutine,
// the two drain goroutines, and (on darwin) the death-watch
// goroutine — all joined via Child.wg.
//
// Start returns nil on success. The caller is then expected to
// call Wait to block on the daemon's exit, and may concurrently
// call Forward to deliver lifecycle signals to the daemon's
// process group.
//
// Start may only be called once per Child. A second call after
// the first succeeded returns ErrChildNotStarted (the cmd handle
// is still live but the contract is single-use). A second call
// after Wait returned likewise returns ErrChildNotStarted.
func (c *Child) Start(ctx context.Context) error

// Wait blocks until the daemon exits, then returns the three-tuple
// exit disposition (FR-020-8):
//   - exitCode: the integer the daemon passed to exit(2). 78
//     indicates stale credentials per Exit78. Verbatim — no
//     remapping (FR-020-10).
//   - signal: the terminating signal if the daemon was killed by
//     a signal; 0 (zero value) when exited via exit(2).
//   - err: non-nil on cmd.Wait() I/O error or on a re-entrant
//     /losing-race call (returns ErrChildNotStarted with
//     (exitCode, signal) zeroed).
//
// Concurrent Wait callers: exactly one wins the sync.Once race
// and observes the real disposition; every other concurrent
// caller returns (0, 0, ErrChildNotStarted) per Clarification 1.
//
// After the winning Wait returns, the cached *exec.Cmd handle is
// cleared (FR-020-11); subsequent Wait or Forward calls return
// ErrChildNotStarted.
//
// Wait blocks the calling goroutine. The supervisor's ctx
// cancellation does NOT cancel Wait (cmd.Wait() is uncancellable
// by Go contract); cancellation flows through Forward(SIGTERM)
// instead.
func (c *Child) Wait() (exitCode int, signal syscall.Signal, err error)

// Forward sends sig to the daemon's process group via
// syscall.Kill(-pgid, sig) — descendants spawned by the daemon
// receive the signal too (FR-020-6). Returns immediately; actual
// delivery is performed by the per-Start forwarding goroutine
// (FR-020-7).
//
// If no live child exists at call time — never started, or post-
// Wait, or post-exit-pre-Wait — Forward returns ErrChildNotStarted
// (FR-020-11; spec Edge Case "signal forwarded after the daemon
// has already exited"). The single sentinel covers every "no
// live child" case (Clarification 2; no distinct ErrChildExited).
func (c *Child) Forward(sig os.Signal) error

// PID returns the daemon's process ID, or 0 if no child is live
// (never started, or post-Wait). Pure scalar read; not an error
// path.
func (c *Child) PID() int

// ---------- Platform seams (build-tagged implementations) ----------

// applyPlatformSysProcAttr is implemented in child_linux.go and
// child_darwin.go. Linux sets Pdeathsig = SIGTERM; darwin is a
// no-op (death-watch is a goroutine, not a SysProcAttr).
func applyPlatformSysProcAttr(cmd *exec.Cmd)

// startDeathWatch is implemented in child_linux.go (no-op) and
// child_darwin.go (spawns the kqueue death-watch goroutine, joined
// via Child.wg). Returns an error only if kqueue setup fails on
// darwin; never errors on linux.
func startDeathWatch(ctx context.Context, c *Child) error
```

---

## Linux — `child_linux.go`

```go
//go:build linux

package supervise

import (
    "context"
    "os/exec"
    "syscall"
)

// applyPlatformSysProcAttr sets the kernel-enforced parent-death
// signal on Linux: when the supervisor exits — by ANY means
// including SIGKILL — the kernel delivers SIGTERM to the child
// (FR-020-5). No supervisor-side goroutine is needed.
func applyPlatformSysProcAttr(cmd *exec.Cmd) {
    if cmd.SysProcAttr == nil {
        cmd.SysProcAttr = &syscall.SysProcAttr{}
    }
    cmd.SysProcAttr.Setpgid = true
    cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
}

// startDeathWatch is a no-op on Linux; Pdeathsig is kernel-
// enforced and does not require a userspace goroutine. The
// signature matches the cross-platform seam called from Start.
func startDeathWatch(_ context.Context, _ *Child) error {
    return nil
}
```

---

## Darwin — `child_darwin.go`

```go
//go:build darwin

package supervise

import (
    "context"
    "os"
    "os/exec"
    "syscall"

    "golang.org/x/sys/unix"
)

// applyPlatformSysProcAttr sets Setpgid only on darwin. Darwin
// has no Pdeathsig equivalent; death-watch is implemented in
// startDeathWatch as a kqueue goroutine.
func applyPlatformSysProcAttr(cmd *exec.Cmd) {
    if cmd.SysProcAttr == nil {
        cmd.SysProcAttr = &syscall.SysProcAttr{}
    }
    cmd.SysProcAttr.Setpgid = true
}

// startDeathWatch spawns the per-Start kqueue death-watch goroutine
// (R-009). The goroutine watches the supervisor's parent PID
// (os.Getppid()) for NOTE_EXIT and, on fire, sends SIGTERM to
// the child's process group.
//
// Known limitation: if the supervisor itself is SIGKILL'd, this
// goroutine dies with the process and cannot deliver cleanup. The
// gap is documented in research.md R-009 and quickstart.md;
// v0.1.1 may add a guardian-subprocess pattern for full coverage.
//
// The goroutine terminates on Child.childDone close OR ctx
// cancellation (R-009). Joined via Child.wg.
func startDeathWatch(ctx context.Context, c *Child) error
```

---

## Wrapping forms

The exact wrapping form for each sentinel:

```go
// ErrCommandEmpty
fmt.Errorf("supervise: %w", ErrCommandEmpty)

// ErrCommandPathRelative
fmt.Errorf("supervise: %w (got %q)", ErrCommandPathRelative, cmd[0])

// ErrChildNotStarted
fmt.Errorf("supervise: %w", ErrChildNotStarted)
```

All three are identifiable via `errors.Is(err,
supervise.ErrXxx)`. The `ErrCommandPathRelative` wrap includes
the offending command's first element — note that `cmd[0]` is a
**path**, not a secret value (Constitution X — paths are non-
secret labels).

---

## Anti-API (NOT exported, NOT added)

The following are explicitly **not** in the locked surface;
adding them would either violate the spec or pre-commit a
contract a downstream chunk has the right to define:

- `func (c *Child) Restart()` — single-use is the locked
  contract (FR-020-11). Callers construct a fresh `Child`.
- `func (c *Child) Stdout() []byte` / `Stderr() []byte` — the
  bounded buffers are drained into operator-supplied
  `io.Writer`s; no read-back accessor is exposed.
- `func (c *Child) ExitCode() int` / similar per-component
  accessors — the three-tuple Wait return is the locked
  shape (FR-020-8).
- `var ErrChildExited` — explicitly forbidden by Clarification 2;
  one sentinel covers all "no live child" cases.
- `type ExitDisposition struct{...}` — the chunk doc locks
  three positional return values; a struct return would
  silently change the SDD-21 call-site shape.
- A `cmd/test-helper-supervise` binary — R-012 uses
  `os.Executable()` re-invocation instead.
- A `Wait(ctx context.Context) (...)` ctx-cancellable variant —
  `cmd.Wait()` is uncancellable; the chunk doc + research R-013
  flow cancellation through `Forward(SIGTERM)`.
- Per-stream ring-buffer size override — the chunk doc locks
  64 KB; a knob would multiply the configuration surface
  without operational need.

---

## Build-tag matrix

| File | Build tag | Symbols | Tests file |
|----|----|----|----|
| `child.go` | (none — cross-platform) | `Child`, `ChildConfig`, `NewChild`, `Start`, `Wait`, `Forward`, `PID`, sentinels, `Exit78` | `child_test.go` |
| `child_linux.go` | `//go:build linux` | `applyPlatformSysProcAttr` (sets Pdeathsig), `startDeathWatch` (no-op) | `child_linux_test.go` |
| `child_darwin.go` | `//go:build darwin` | `applyPlatformSysProcAttr` (no Pdeathsig), `startDeathWatch` (kqueue goroutine) | `child_darwin_test.go` |
