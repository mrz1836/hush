//go:build darwin

package supervise

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestApplyPlatformSysProcAttr_NilSysProcAttrBranch(t *testing.T) {
	t.Parallel()
	// Cmd with nil SysProcAttr — exercises the allocation branch.
	cmd := &exec.Cmd{}
	applyPlatformSysProcAttr(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatalf("SysProcAttr should be allocated")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Fatalf("Setpgid should be true")
	}
}

func TestApplyPlatformSysProcAttr_PreservesExisting(t *testing.T) {
	t.Parallel()
	cmd := &exec.Cmd{}
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	applyPlatformSysProcAttr(cmd)
	if !cmd.SysProcAttr.Setpgid {
		t.Fatalf("Setpgid should be true even when SysProcAttr was preset")
	}
}
