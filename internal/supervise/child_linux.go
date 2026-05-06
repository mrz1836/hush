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
// (FR-020-5, R-010).
func applyPlatformSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
}

// startDeathWatch is a no-op on Linux; Pdeathsig is kernel-
// enforced and does not require a userspace goroutine (R-010).
func startDeathWatch(_ context.Context, _ *Child) error {
	return nil
}
