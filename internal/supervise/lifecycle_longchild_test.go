package supervise

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLongChildCmd_EndsWithItsParent — the long-running test child
// exits once the process that started it is gone, so a test run that
// is killed or times out leaves no loop behind holding the checkout.
//
// An outer shell stands in for the test binary: it starts the loop in
// the background, waits until the loop has started, and exits. Run
// reaps it, and the loop must then end on its own.
func TestLongChildCmd_EndsWithItsParent(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	ended := filepath.Join(dir, "ended")

	loop := longChildCmd()
	inner := "echo $$ > " + shQuote(started) + "; " + loop[2] + "; echo > " + shQuote(ended)
	outer := loop[0] + " -c " + shQuote(inner) + " & " +
		"while [ ! -s " + shQuote(started) + " ]; do sleep 0.01; done"

	t.Cleanup(func() { killIfRunning(t, started, ended) })

	if err := exec.CommandContext(t.Context(), loop[0], "-c", outer).Run(); err != nil { //nolint:gosec // fixed test command
		t.Fatalf("outer shell: %v", err)
	}

	eventually(t, "the loop ends after its parent exits", 5*time.Second, func() bool {
		_, err := os.Stat(ended)
		return err == nil
	})
}

// killIfRunning kills the loop when the test failed to see it end, so
// a failing run of TestLongChildCmd_EndsWithItsParent does not leak it.
func killIfRunning(t *testing.T, started, ended string) {
	t.Helper()
	if _, err := os.Stat(ended); err == nil {
		return
	}
	raw, err := os.ReadFile(started) //nolint:gosec // path is under t.TempDir
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Logf("kill leftover loop %d: %v", pid, err)
	}
}

// shQuote quotes s as one POSIX shell word.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
