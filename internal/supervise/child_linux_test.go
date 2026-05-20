//go:build linux

package supervise_test

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
)

// T-11a: TestChild_LinuxPdeathsig — kernel-enforced parent-death
// cleanup on Linux. SIGKILL the sub-supervisor; grandchild is
// reaped within 2s via Pdeathsig=SIGTERM.
func TestChild_LinuxPdeathsig(t *testing.T) {
	out := &syncBuffer{}
	c := supervise.NewChild(helperConfig(t, "subsupervisor-with-grandchild", out))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _, _, _ = c.Wait() }()

	grandPID := waitForGrandchildPID(out, 5*time.Second)
	if grandPID == 0 {
		t.Fatalf("grandchild pid not observed: %q", out.String())
	}

	// SIGKILL the sub-supervisor; the kernel must deliver SIGTERM
	// to the grandchild via Pdeathsig.
	subPID := c.PID()
	if subPID == 0 {
		t.Fatalf("sub-supervisor PID is zero")
	}
	if err := syscall.Kill(subPID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill sub-supervisor: %v", err)
	}

	if !waitGone(t, grandPID, 3*time.Second) && processAlive(grandPID) {
		t.Fatalf("grandchild %d still alive after sub-supervisor SIGKILL", grandPID)
	}

	// Cleanup: reap any straggler.
	if processAlive(grandPID) {
		_ = syscall.Kill(grandPID, syscall.SIGKILL)
	}
}

// waitForGrandchildPID polls the helper-process output until a
// "GRANDCHILD_PID=" line carrying a non-zero PID appears, returning
// 0 if the timeout elapses first.
func waitForGrandchildPID(out *syncBuffer, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid := parsePIDLine(out.String(), "GRANDCHILD_PID="); pid != 0 {
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	return parsePIDLine(out.String(), "GRANDCHILD_PID=")
}

// parsePIDLine scans newline-delimited text for the last line
// beginning with prefix and returns the non-zero PID that follows,
// or 0 when no such line carries a parseable PID.
func parsePIDLine(s, prefix string) int {
	var pid int
	for line := range strings.SplitSeq(s, "\n") {
		v, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n != 0 {
			pid = n
		}
	}
	return pid
}
