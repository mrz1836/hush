// SDD-20 — Supervised child process layer.
//
// child.go declares the cross-platform Child handle, ChildConfig
// input record, the ring-buffered output drain protocol, and the
// sentinel errors plus Exit78 stale-credential constant
// (FR-020-9, FR-020-11, FR-020-14, R-003).
//
// Platform-specific behaviour lives in child_linux.go (Pdeathsig)
// and child_darwin.go (kqueue death-watch).
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

// ErrChildNotStarted is returned (wrapped) for every "no live
// child" condition: never started, post-Wait re-entry, signal
// forwarded after the child has exited but before Wait returned,
// and concurrent Wait callers who lost the sync.Once race
// (Clarification 1, Clarification 2, FR-020-11, R-011).
var ErrChildNotStarted = errors.New("supervise: child not started")

// ErrCommandEmpty is returned (wrapped) by Start when
// len(cfg.Command) == 0 (FR-020-2, R-003). Distinct from
// ErrCommandPathRelative.
var ErrCommandEmpty = errors.New("supervise: command empty")

// ErrCommandPathRelative is returned (wrapped) by Start when
// cfg.Command[0] is not an absolute path (FR-020-3, R-003).
// Distinct from ErrCommandEmpty.
var ErrCommandPathRelative = errors.New("supervise: command path not absolute")

// ---------- Stale-credential exit-code ----------

// Exit78 is the project-wide stale-credential exit-code contract
// (FR-020-9, Constitution V). A daemon that exits with this code
// signals "my credentials are stale; refetch and restart". Wait
// surfaces the value verbatim (FR-020-10).
const Exit78 = 78

// defaultRingBufferSize is the per-stream bounded ring capacity
// (FR-020-12). 64 KB — kilobyte-scale, not megabyte-scale.
const defaultRingBufferSize = 64 * 1024

// ---------- ChildConfig ----------

// ChildConfig is the input to NewChild. The slice fields
// (Command, Env) are reference-shared with the caller and are
// read-only from the layer's perspective; the layer never logs,
// inspects, or copies Env values (Constitution X).
type ChildConfig struct {
	Command []string     // argv; element 0 absolute path (FR-020-1/2/3)
	Env     []string     // KEY=VALUE pairs; consumed by execve
	Dir     string       // working directory; "" inherits supervisor CWD
	Stdout  io.Writer    // stdout sink; nil → discard
	Stderr  io.Writer    // stderr sink; nil → discard
	Logger  *slog.Logger // structured logger; non-nil required
}

// ---------- Child ----------

// Child is a handle to a single supervised daemon process.
// Single-use: once Wait returns the exit disposition, the cached
// *exec.Cmd is cleared (FR-020-11) and every subsequent call
// returns ErrChildNotStarted. To launch another daemon, construct
// a fresh Child via NewChild.
//
// Child is not safe to copy; pass as *Child.
type Child struct {
	cfg ChildConfig

	mu  sync.RWMutex
	cmd *exec.Cmd
	pid int

	stdoutRing *ringBuffer
	stderrRing *ringBuffer

	forwardCh chan os.Signal
	childDone chan struct{}

	wg       sync.WaitGroup
	waitOnce sync.Once

	exitCode   int
	exitSignal syscall.Signal
	exitErr    error
}

// NewChild constructs a Child handle from cfg. Pure value
// constructor: no validation, no syscalls. Allocates two ring
// buffers of capacity defaultRingBufferSize for stdout/stderr.
// Panics if cfg.Logger is nil (Constitution IX startup-wiring
// exemption).
func NewChild(cfg ChildConfig) *Child {
	if cfg.Logger == nil {
		panic("supervise: NewChild requires a non-nil Logger")
	}
	c := &Child{cfg: cfg}
	c.stdoutRing = newRingBuffer("stdout", cfg.Logger)
	c.stderrRing = newRingBuffer("stderr", cfg.Logger)
	c.forwardCh = make(chan os.Signal, 1)
	return c
}

// Start launches the daemon. Validates cfg.Command (returns
// ErrCommandEmpty or ErrCommandPathRelative on failure), then
// invokes cmd.Start with SysProcAttr.Setpgid=true plus
// platform-specific death-watch attributes (Pdeathsig on linux;
// kqueue goroutine on darwin). Spawns the forwarding goroutine,
// the two drain goroutines, and (on darwin) the death-watch
// goroutine — all joined via Child.wg.
//
//nolint:gocognit // sequential goroutine wiring with explicit per-step rationale (FR-020-7, R-013, R-014)
func (c *Child) Start(ctx context.Context) error {
	if len(c.cfg.Command) == 0 {
		return fmt.Errorf("supervise: %w", ErrCommandEmpty)
	}
	if !filepath.IsAbs(c.cfg.Command[0]) {
		return fmt.Errorf("supervise: %w (got %q)", ErrCommandPathRelative, c.cfg.Command[0])
	}

	cmd := exec.CommandContext(ctx, c.cfg.Command[0], c.cfg.Command[1:]...) //nolint:gosec // cfg.Command[0] is validated as an absolute path; argv-style call avoids shell parsing per FR-020-1.
	cmd.Path = c.cfg.Command[0]
	// Defensive copy of the env slice: the caller (Lifecycle.startChild)
	// owns cfg.Env and zeroes its contents after Start returns to scrub
	// SECRET=value pairs from the parent's heap. Sharing the backing
	// array would either race the zero-loop against exec.Cmd's own use
	// of the slice or, worse, blank the env the child sees if the wipe
	// happens between cmd.Start's slice read and the fork. Constitution X.
	cmd.Env = make([]string, len(c.cfg.Env))
	copy(cmd.Env, c.cfg.Env)
	cmd.Dir = c.cfg.Dir
	cmd.Stdout = c.stdoutRing
	cmd.Stderr = c.stderrRing
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	applyPlatformSysProcAttr(cmd)

	c.childDone = make(chan struct{})

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("supervise: child start: %w", err)
	}

	c.mu.Lock()
	c.cmd = cmd
	c.pid = cmd.Process.Pid
	c.mu.Unlock()

	pgid := cmd.Process.Pid

	// Forwarding goroutine — terminates on ctx.Done or childDone (Clarification 3, R-013).
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				c.cfg.Logger.Error("supervise: forwarding goroutine panicked",
					slog.Any("recovered", r))
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.childDone:
				return
			case sig := <-c.forwardCh:
				ssig, _ := sig.(syscall.Signal)
				_ = syscall.Kill(-pgid, ssig)
			}
		}
	}()

	// Drain goroutines — one per stream (R-014).
	c.wg.Add(1)
	go c.drainLoop(c.stdoutRing, c.cfg.Stdout)
	c.wg.Add(1)
	go c.drainLoop(c.stderrRing, c.cfg.Stderr)

	if err := startDeathWatch(ctx, c); err != nil {
		c.cfg.Logger.Warn("supervise: death-watch setup failed",
			slog.Any("err", err))
	}

	return nil
}

// Wait blocks until the daemon exits, then returns the three-tuple
// exit disposition (FR-020-8). The first caller wins the
// sync.Once race; concurrent and subsequent callers observe
// (0, 0, ErrChildNotStarted) per Clarification 1 + R-004.
func (c *Child) Wait() (exitCode int, signal syscall.Signal, err error) {
	c.mu.RLock()
	cmd := c.cmd
	c.mu.RUnlock()
	if cmd == nil {
		return 0, 0, fmt.Errorf("supervise: %w", ErrChildNotStarted)
	}

	won := false
	c.waitOnce.Do(func() {
		won = true
		_ = cmd.Wait()
		ws := cmd.ProcessState.Sys().(syscall.WaitStatus) //nolint:forcetypeassert // linux + darwin always return syscall.WaitStatus per R-007
		if ws.Signaled() {
			c.exitCode = 0
			c.exitSignal = ws.Signal()
		} else {
			c.exitCode = ws.ExitStatus()
			c.exitSignal = 0
		}
		c.exitErr = nil

		// Close rings BEFORE childDone close so drain goroutines
		// observe the close-induced notify and flush final bytes
		// (R-006 + R-014 ordering invariant).
		_ = c.stdoutRing.Close()
		_ = c.stderrRing.Close()

		close(c.childDone)

		c.mu.Lock()
		c.cmd = nil
		c.pid = 0
		c.mu.Unlock()

		c.wg.Wait()
	})
	if won {
		return c.exitCode, c.exitSignal, c.exitErr
	}
	return 0, 0, fmt.Errorf("supervise: %w", ErrChildNotStarted)
}

// Forward sends sig to the daemon's process group via the
// per-Start forwarding goroutine. Returns ErrChildNotStarted if
// no live child exists at call time (Clarification 2, FR-020-11,
// R-011).
func (c *Child) Forward(sig os.Signal) error {
	c.mu.RLock()
	cmd := c.cmd
	c.mu.RUnlock()
	if cmd == nil {
		return fmt.Errorf("supervise: %w", ErrChildNotStarted)
	}
	select {
	case c.forwardCh <- sig:
	default:
		// Buffer is full — coalesce by replacing the queued signal.
		select {
		case <-c.forwardCh:
		default:
		}
		select {
		case c.forwardCh <- sig:
		default:
		}
	}
	return nil
}

// PID returns the daemon's process ID, or 0 if no child is live
// (FR-020-11, R-011). Pure scalar read.
func (c *Child) PID() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pid
}

// drainLoop reads ring contents into sink. Terminates on
// childDone (after a final drain pass). May park indefinitely on
// a slow sink (Clarification 4, R-014) — that is the contract.
func (c *Child) drainLoop(ring *ringBuffer, sink io.Writer) {
	defer c.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			c.cfg.Logger.Error("supervise: drain goroutine panicked",
				slog.Any("recovered", r))
		}
	}()
	if sink == nil {
		sink = io.Discard
	}
	for {
		select {
		case <-c.childDone:
			_, _ = ring.drain(sink)
			return
		case <-ring.notify:
			_, _ = ring.drain(sink)
		}
	}
}

// ---------- Bounded ring buffer ----------

// ringBuffer is a 64 KB FIFO byte buffer protected by a mutex.
// Write always returns (len(p), nil) — never blocks, never
// short-writes (FR-020-13). On overflow, oldest bytes are
// dropped and a single slog.Warn is emitted per episode per
// stream (Clarification 5, R-006).
type ringBuffer struct {
	mu          sync.Mutex
	buf         []byte
	cap         int
	streamLabel string
	logger      *slog.Logger
	atCapacity  bool
	notify      chan struct{}
	closed      bool
}

func newRingBuffer(streamLabel string, logger *slog.Logger) *ringBuffer {
	return &ringBuffer{
		buf:         make([]byte, 0, defaultRingBufferSize),
		cap:         defaultRingBufferSize,
		streamLabel: streamLabel,
		logger:      logger,
		notify:      make(chan struct{}, 1),
	}
}

// Write appends p to the ring with FIFO eviction on overflow.
// Always returns (len(p), nil) for an open ring; returns
// (len(p), nil) silently for a closed ring so the os/exec writer
// goroutine does not see an error during teardown.
func (r *ringBuffer) Write(p []byte) (int, error) {
	written := len(p)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return written, nil
	}
	if len(r.buf)+len(p) > r.cap {
		overflow := len(r.buf) + len(p) - r.cap
		switch {
		case overflow >= len(r.buf):
			// Drop the entire current buffer, then drop the
			// oldest prefix of p so only the trailing cap bytes
			// of p land in the ring.
			drop := overflow - len(r.buf)
			r.buf = r.buf[:0]
			p = p[drop:]
		default:
			// Drop a prefix of the current buffer.
			r.buf = append(r.buf[:0], r.buf[overflow:]...)
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
	return written, nil
}

// Close marks the ring closed. Idempotent. Wakes the drain
// goroutine via notify so it observes the closed state and
// performs a final drain pass.
func (r *ringBuffer) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
	return nil
}

// drain copies the current ring contents to dst in one Write
// call. May block on a slow dst (Clarification 4). Returns the
// byte count drained and any error from dst.Write. Resets
// atCapacity once the ring drains below cap (R-006).
func (r *ringBuffer) drain(dst io.Writer) (int64, error) {
	r.mu.Lock()
	if len(r.buf) == 0 {
		closed := r.closed
		r.mu.Unlock()
		if closed {
			return 0, io.EOF
		}
		return 0, nil
	}
	out := append([]byte(nil), r.buf...)
	r.buf = r.buf[:0]
	if r.atCapacity {
		r.atCapacity = false
	}
	r.mu.Unlock()
	if dst == nil {
		dst = io.Discard
	}
	n, err := dst.Write(out)
	return int64(n), err
}
