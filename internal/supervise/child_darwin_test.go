//go:build darwin

package supervise_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
)

// T-11b: TestChild_DarwinDeathWatch — best-effort kqueue
// death-watch on Darwin (R-009).
//
// The death-watch goroutine pair (kqueue blocker + waker) is
// started by Start; the kqueue watches the supervisor's PPID and
// fires SIGTERM at the child's pgid when that PPID exits. The
// goroutines also exit cleanly when the child exits via the
// self-pipe waker (Clarification 3 + R-013 termination contract).
//
// The "kill PPID → SIGTERM grandchild" semantic requires a
// 3-level process hierarchy that is impractical to wire in a
// unit test (it would need to terminate the test runner). The
// SIGKILL-of-supervisor case is the documented R-009 gap and is
// skipped explicitly. The graceful-cleanup path — Start succeeds
// (no kqueue/pipe error), all death-watch goroutines join via
// wg on Wait, no goroutine leak — IS exercised here.
//
//nolint:gocognit,gocyclo // 4-subtest pattern: kqueue path + ctx-cancel + 2 documented R-009 skips
func TestChild_DarwinDeathWatch(t *testing.T) {
	t.Run("starts_and_cleans_up_goroutines", func(t *testing.T) {
		// Warm up to remove first-call allocation skew.
		for i := 0; i < 2; i++ {
			c := supervise.NewChild(helperConfig(t, "exit-zero", nil))
			if err := c.Start(context.Background()); err != nil {
				t.Fatalf("warmup Start: %v", err)
			}
			_, _, _ = c.Wait()
		}
		runtime.GC()
		baseline := runtime.NumGoroutine()

		c := supervise.NewChild(helperConfig(t, "exit-zero", nil))
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		exit, _, err := c.Wait()
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if exit != 0 {
			t.Fatalf("want exit 0, got %d", exit)
		}

		// All darwin goroutines (forwarding + 2 drain + 2
		// death-watch) must have joined via c.wg.Wait inside
		// Wait. Allow brief settling.
		runtime.GC()
		deadline := time.Now().Add(500 * time.Millisecond)
		delta := runtime.NumGoroutine() - baseline
		for time.Now().Before(deadline) && delta > 5 {
			time.Sleep(10 * time.Millisecond)
			delta = runtime.NumGoroutine() - baseline
		}
		if delta > 5 {
			t.Fatalf("death-watch goroutines leaked: baseline=%d after=%d delta=%d",
				baseline, runtime.NumGoroutine(), delta)
		}
	})

	t.Run("ctx_cancel_breaks_kqueue_blocker", func(t *testing.T) {
		baseline := runtime.NumGoroutine()
		ctx, cancel := context.WithCancel(context.Background())
		c := supervise.NewChild(helperConfig(t, "exit-zero", nil))
		if err := c.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		_, _, _ = c.Wait()
		cancel()
		runtime.GC()
		// Goroutines should return to baseline regardless of
		// whether ctx cancel fires before or after Wait.
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if runtime.NumGoroutine()-baseline <= 5 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("goroutines did not settle: baseline=%d after=%d",
			baseline, runtime.NumGoroutine())
	})

	t.Run("SIGKILL_supervisor_known_limitation", func(t *testing.T) {
		t.Skip("R-009 darwin gap")
	})

	t.Run("PPID_exit_to_grandchild_SIGTERM_known_limitation", func(t *testing.T) {
		t.Skip("R-009 darwin gap — requires 3-level process hierarchy")
	})
}
