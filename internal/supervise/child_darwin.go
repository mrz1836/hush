//go:build darwin

package supervise

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// applyPlatformSysProcAttr sets Setpgid only on darwin. Darwin
// has no Pdeathsig equivalent; death-watch is implemented in
// startDeathWatch as a kqueue goroutine (R-009).
func applyPlatformSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// startDeathWatch spawns the per-Start kqueue death-watch
// goroutine pair (R-009). The watcher fires SIGTERM at the
// child's process group when the supervisor's parent (PPID)
// exits; the waker breaks the kqueue blocker out of Kevent on
// ctx cancellation or child exit.
//
// Known limitation (R-009): if the supervisor itself is
// SIGKILL'd, both goroutines die with the process and cannot
// deliver cleanup. Documented in research.md and surfaced via
// t.Skip in T-11b's SIGKILL subtest.
//
//nolint:gocognit,gocyclo // kqueue+pipe registration + 2 goroutines + EINTR loop: complexity is inherent to the death-watch contract (R-009)
func startDeathWatch(ctx context.Context, c *Child) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("supervise: kqueue: %w", err)
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		_ = unix.Close(kq)
		return fmt.Errorf("supervise: pipe: %w", err)
	}

	ppid := os.Getppid()
	events := []unix.Kevent_t{
		{
			Ident:  uint64(ppid), //nolint:gosec // ppid is a kernel-supplied non-negative pid
			Filter: unix.EVFILT_PROC,
			Flags:  unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT,
			Fflags: unix.NOTE_EXIT,
		},
		{
			Ident:  uint64(pr.Fd()),
			Filter: unix.EVFILT_READ,
			Flags:  unix.EV_ADD | unix.EV_ENABLE,
		},
	}
	if _, err := unix.Kevent(kq, events, nil, nil); err != nil {
		_ = unix.Close(kq)
		_ = pr.Close()
		_ = pw.Close()
		return fmt.Errorf("supervise: kevent register: %w", err)
	}

	// Waker — wakes the kqueue blocker on ctx cancel or child exit.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				c.cfg.Logger.Error("supervise: death-watch waker panicked",
					slog.Any("recovered", r))
			}
		}()
		select {
		case <-ctx.Done():
		case <-c.childDone:
		}
		_, _ = pw.Write([]byte{1})
		_ = pw.Close()
	}()

	// Kqueue blocker.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				c.cfg.Logger.Error("supervise: death-watch blocker panicked",
					slog.Any("recovered", r))
			}
		}()
		defer func() { _ = unix.Close(kq) }()
		defer func() { _ = pr.Close() }()

		out := make([]unix.Kevent_t, 2)
		for {
			n, err := unix.Kevent(kq, nil, out, nil)
			if err != nil {
				if errors.Is(err, unix.EINTR) {
					continue
				}
				return
			}
			for i := 0; i < n; i++ {
				ev := out[i]
				if ev.Filter == unix.EVFILT_PROC && ev.Fflags&unix.NOTE_EXIT != 0 {
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
					return
				}
			}
		}
	}()

	return nil
}
