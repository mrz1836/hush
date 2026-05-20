//go:build linux

package supervise_test

import (
	"context"
	"os"
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

	// Wait for the sub-supervisor to print the grandchild PID.
	var grandPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := out.String()
		if strings.Contains(s, "GRANDCHILD_PID=") {
			for _, line := range strings.Split(s, "\n") {
				if v, ok := strings.CutPrefix(line, "GRANDCHILD_PID="); ok {
					grandPID, _ = strconv.Atoi(strings.TrimSpace(v))
				}
			}
			if grandPID != 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
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

	if !waitGone(t, grandPID, 3*time.Second) {
		// Probe: signal-0 to confirm.
		if processAlive(grandPID) {
			t.Fatalf("grandchild %d still alive after sub-supervisor SIGKILL", grandPID)
		}
	}

	// Cleanup: reap any straggler.
	if processAlive(grandPID) {
		_ = syscall.Kill(grandPID, syscall.SIGKILL)
	}
	_ = os.Getpid() // keep import if needed
}
