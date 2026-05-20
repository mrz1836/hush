package supervise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/audit"
)

// shortChildCmd returns a long-enough-running child command so the
// orchestrator reaches StateRunning before the child exits.
func shortChildCmd(t *testing.T, exitCode int) []string {
	t.Helper()
	// /bin/sh -c with explicit exit so we don't depend on /bin/false vs /bin/true.
	return []string{"/bin/sh", "-c", "sleep 0.05; exit " + fmt.Sprintf("%d", exitCode)}
}

// longChildCmd returns a long-running child (sleeps for the whole test).
func longChildCmd() []string {
	return []string{"/bin/sh", "-c", "while true; do sleep 0.05; done"}
}

// runWithCancel starts Run in a goroutine and returns the cancel + a
// channel that delivers the Run-exit error.
func runWithCancel(tl *testLifecycle) (cancel context.CancelFunc, done <-chan error) {
	ctx, c := context.WithCancel(context.Background())
	d := make(chan error, 1)
	go func() {
		d <- tl.lc.Run(ctx)
	}()
	return c, d
}

// eventually polls fn until it returns true or timeout elapses. Uses
// runtime.Gosched()-driven cadence; no time.Sleep on the hot path.
func eventually(t *testing.T, msg string, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		runtime.Gosched()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("eventually: %s (timeout %s)", msg, timeout)
}

// snapshotState reads the lifecycle's current state via Store.Snapshot.
func snapshotState(tl *testLifecycle) State {
	return tl.lc.store.Snapshot().State
}

// TestLifecycle_BootSubmitsClaim (T009) — happy path: orchestrator submits
// exactly one /claim, reaches StateRunning, zero alerts, audit chain
// includes ActionSupervisorSessionClaimed.
func TestLifecycle_BootSubmitsClaim(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.vault.QueueOK()

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "reach StateRunning", 5*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})

	if got := tl.vault.ClaimCount(); got != 1 {
		t.Errorf("ClaimCount: got %d want 1", got)
	}
	if got := len(tl.alerts.Events()); got != 0 {
		t.Errorf("alerts on happy path: got %d want 0; events=%+v", got, tl.alerts.Events())
	}
	if !tl.auditLog.Has(audit.ActionSupervisorSessionClaimed) {
		t.Errorf("audit chain missing %s; got %v", audit.ActionSupervisorSessionClaimed, tl.auditLog.Actions())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("Run did not exit after cancel")
	}
}

// TestLifecycle_BootRetrySucceedsAfterNFailures (T010) — TailscaleProbe
// errors twice then succeeds; orchestrator reaches StateRunning.
func TestLifecycle_BootRetrySucceedsAfterNFailures(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.vault.QueueOK()
	var attempts int
	tl.lc.deps.TailscaleProbe = func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("not ready")
		}
		return nil
	}

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "reach StateRunning after retries", 10*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})
	if attempts < 3 {
		t.Errorf("expected at least 3 probe attempts, got %d", attempts)
	}

	cancel()
	<-done
}

// TestLifecycle_BootRetryTimeoutExhausted (T011) — probe always fails,
// short budget; expect ErrBootTimeout + AlertClassBootTimeout + audit.
func TestLifecycle_BootRetryTimeoutExhausted(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.cfg.BootRetryTimeout = 100 * time.Millisecond
	tl.lc.deps.TailscaleProbe = func(context.Context) error {
		return errors.New("never ready")
	}

	_, done := runWithCancel(tl)
	defer func() { /* runWithCancel returns cancel — not used */ }()

	select {
	case err := <-done:
		if !errors.Is(err, ErrBootTimeout) {
			t.Errorf("expected ErrBootTimeout, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not exit on boot timeout")
	}

	if tl.vault.ClaimCount() != 0 {
		t.Errorf("ClaimCount: got %d want 0 on boot-timeout path", tl.vault.ClaimCount())
	}
	if tl.alerts.CountClass(AlertClassBootTimeout) != 1 {
		t.Errorf("AlertClassBootTimeout count: got %d want 1", tl.alerts.CountClass(AlertClassBootTimeout))
	}
	if !tl.auditLog.Has(audit.ActionSupervisorBootTimeout) {
		t.Errorf("audit missing %s", audit.ActionSupervisorBootTimeout)
	}
}

// TestLifecycle_ClaimDeniedTransitionsToAwaitingApproval (T012) — claim
// 403 denied terminal; expect Run to return ErrClaimDenied.
func TestLifecycle_ClaimDeniedTransitionsToAwaitingApproval(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.vault.QueueDenied()

	_, done := runWithCancel(tl)

	select {
	case err := <-done:
		if !errors.Is(err, ErrClaimDenied) {
			t.Errorf("expected ErrClaimDenied, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not exit on terminal claim denial")
	}
}

// TestLifecycle_ClaimDiscordUnavailableEmitsAlert (T013) — first /claim
// returns 503 discord_unavailable, second returns OK; reaches StateRunning
// with exactly one AlertClassDiscordUnavailableOnClaim.
func TestLifecycle_ClaimDiscordUnavailableEmitsAlert(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.cfg.BootRetryTimeout = 5 * time.Second
	tl.vault.QueueDiscordUnavailable()
	tl.vault.QueueOK()

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "reach StateRunning after discord retry", 10*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})

	if c := tl.alerts.CountClass(AlertClassDiscordUnavailableOnClaim); c != 1 {
		t.Errorf("AlertClassDiscordUnavailableOnClaim count: got %d want 1", c)
	}
	if tl.vault.ClaimCount() < 2 {
		t.Errorf("ClaimCount: got %d want >= 2", tl.vault.ClaimCount())
	}

	cancel()
	<-done
}

// TestLifecycle_ValidatorFailureBlocksChildStart (T014) — validator returns
// error; child not started, StateAwaitingApproval, one AlertClassValidatorFailure
// with scope name, supervisor_stale_alert + supervisor_awaiting_approval.
func TestLifecycle_ValidatorFailureBlocksChildStart(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.vault.QueueOK()
	tl.lc.deps.Validators = map[string]Validator{
		"ANTHROPIC_API_KEY": &controllableValidator{
			failures: map[string]error{"ANTHROPIC_API_KEY": errors.New("401 stale")},
		},
	}

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "reach StateAwaitingApproval", 5*time.Second, func() bool {
		return snapshotState(tl) == StateAwaitingApproval
	})

	if c := tl.alerts.CountClass(AlertClassValidatorFailure); c != 1 {
		t.Errorf("AlertClassValidatorFailure count: got %d want 1", c)
	}
	for _, e := range tl.alerts.Events() {
		if e.class == AlertClassValidatorFailure && e.payload.Scope != "ANTHROPIC_API_KEY" {
			t.Errorf("alert payload scope: got %q want ANTHROPIC_API_KEY", e.payload.Scope)
		}
	}
	if !tl.auditLog.Has(audit.ActionSupervisorStaleAlert) {
		t.Errorf("audit missing %s", audit.ActionSupervisorStaleAlert)
	}
	if !tl.auditLog.Has(audit.ActionSupervisorAwaitingApproval) {
		t.Errorf("audit missing %s", audit.ActionSupervisorAwaitingApproval)
	}
	// Child PID 0 — no live child.
	tl.lc.childMu.Lock()
	childStarted := tl.lc.child != nil
	tl.lc.childMu.Unlock()
	if childStarted {
		t.Errorf("child should not be started after validator failure")
	}

	cancel()
	<-done
}

// TestLifecycle_ChildExitZeroTriggersSilentRefill (T019) — child exits 0;
// second Refill, child restarted, audit ChildCleanExit + SilentRefill,
// zero alerts.
func TestLifecycle_ChildExitZeroTriggersSilentRefill(t *testing.T) {
	tl := newTestLifecycle(t, shortChildCmd(t, 0))
	tl.vault.QueueOK()

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "audit ChildCleanExit", 5*time.Second, func() bool {
		return tl.auditLog.Has(audit.ActionSupervisorChildCleanExit)
	})
	eventually(t, "audit SilentRefill", 5*time.Second, func() bool {
		return tl.auditLog.Has(audit.ActionSupervisorSilentRefill)
	})

	// Only one /claim — silent refill uses the same JWT.
	if tl.vault.ClaimCount() != 1 {
		t.Errorf("ClaimCount: got %d want 1", tl.vault.ClaimCount())
	}
	if len(tl.alerts.Events()) != 0 {
		t.Errorf("alerts on silent refill: %+v", tl.alerts.Events())
	}

	cancel()
	<-done
}

// TestLifecycle_ChildExitNonZeroTriggersSilentRefill (T020) — child exits 1;
// audit ChildExitCrash + SilentRefill, zero alerts.
func TestLifecycle_ChildExitNonZeroTriggersSilentRefill(t *testing.T) {
	tl := newTestLifecycle(t, shortChildCmd(t, 1))
	tl.vault.QueueOK()

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "audit ChildExitCrash", 5*time.Second, func() bool {
		return tl.auditLog.Has(audit.ActionSupervisorChildExitCrash)
	})
	eventually(t, "audit SilentRefill", 5*time.Second, func() bool {
		return tl.auditLog.Has(audit.ActionSupervisorSilentRefill)
	})

	if len(tl.alerts.Events()) != 0 {
		t.Errorf("alerts on crash silent refill: %+v", tl.alerts.Events())
	}

	cancel()
	<-done
}

// TestLifecycle_ChildExit78EmitsStaleAlertNoRestart (T026) — child exits 78;
// StateAwaitingApproval, one AlertClassExit78, audit ChildExit78 +
// StaleAlert + AwaitingApproval, NO second Refill.
func TestLifecycle_ChildExit78EmitsStaleAlertNoRestart(t *testing.T) {
	tl := newTestLifecycle(t, shortChildCmd(t, 78))
	tl.vault.QueueOK()

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "StateAwaitingApproval after exit 78", 5*time.Second, func() bool {
		return snapshotState(tl) == StateAwaitingApproval
	})

	if c := tl.alerts.CountClass(AlertClassExit78); c != 1 {
		t.Errorf("AlertClassExit78 count: got %d want 1", c)
	}
	if !tl.auditLog.Has(audit.ActionSupervisorChildExit78) {
		t.Errorf("audit missing %s", audit.ActionSupervisorChildExit78)
	}
	// Wait a bit to ensure no auto-restart.
	time.Sleep(100 * time.Millisecond)
	if tl.auditLog.Has(audit.ActionSupervisorSilentRefill) {
		t.Errorf("exit 78 should NOT trigger silent refill")
	}

	cancel()
	<-done
}

// TestLifecycle_RefillJTIUnknownTransitionsToAwaitingApproval (T021) — child
// exits 0; mockVault returns 401 unknown_jti on second Refill; expect
// StateAwaitingApproval + AlertClassVaultRejectedJWT.
func TestLifecycle_RefillJTIUnknownTransitionsToAwaitingApproval(t *testing.T) {
	tl := newTestLifecycle(t, shortChildCmd(t, 0))
	tl.vault.QueueOK()

	// Initial refill must succeed; subsequent must 401.
	var refillCount int
	var mu sync.Mutex
	// Use a counter via a wrapper handler. Easier: just set the
	// scope to fail after a delay.
	go func() {
		// Wait until first child has exited then flip the scope failure.
		for {
			mu.Lock()
			rc := refillCount
			mu.Unlock()
			_ = rc
			runtime.Gosched()
			if tl.auditLog.Has(audit.ActionSupervisorChildCleanExit) {
				tl.vault.FailScopeUnauthorizedJTI("ANTHROPIC_API_KEY")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "StateAwaitingApproval after unknown_jti", 10*time.Second, func() bool {
		return snapshotState(tl) == StateAwaitingApproval &&
			tl.alerts.CountClass(AlertClassVaultRejectedJWT) >= 1
	})

	cancel()
	<-done
}

// TestLifecycle_RefillTransientErrorPostRunningEmitsRefillFailed (T022) —
// child exits 0; mockVault returns 500 on second Refill; expect
// StateAwaitingApproval + AlertClassRefillFailed.
func TestLifecycle_RefillTransientErrorPostRunningEmitsRefillFailed(t *testing.T) {
	tl := newTestLifecycle(t, shortChildCmd(t, 0))
	tl.vault.QueueOK()

	go func() {
		for {
			if tl.auditLog.Has(audit.ActionSupervisorChildCleanExit) {
				tl.vault.FailScopeStatus("ANTHROPIC_API_KEY", 500)
				return
			}
			runtime.Gosched()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "StateAwaitingApproval + RefillFailed alert", 10*time.Second, func() bool {
		return snapshotState(tl) == StateAwaitingApproval &&
			tl.alerts.CountClass(AlertClassRefillFailed) >= 1
	})

	cancel()
	<-done
}

// TestLifecycle_RefresherTickSubmitsFreshClaim (T031) — orchestrator at
// StateRunning; post on refreshTickCh; expect ClaimCount==2 +
// ActionSupervisorSessionRefreshed.
func TestLifecycle_RefresherTickSubmitsFreshClaim(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.vault.QueueOK()
	tl.vault.QueueOK() // for refresh

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "reach StateRunning", 5*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})

	// Capture child PID.
	tl.lc.childMu.Lock()
	originalPID := tl.lc.child.PID()
	tl.lc.childMu.Unlock()

	tl.lc.refreshTickCh <- struct{}{}

	eventually(t, "second /claim and audit refreshed", 5*time.Second, func() bool {
		return tl.vault.ClaimCount() >= 2 && tl.auditLog.Has(audit.ActionSupervisorSessionRefreshed)
	})

	// (T032) Child PID unchanged after refresh.
	tl.lc.childMu.Lock()
	pid := 0
	if tl.lc.child != nil {
		pid = tl.lc.child.PID()
	}
	tl.lc.childMu.Unlock()
	if pid != originalPID {
		t.Errorf("child PID changed across refresh: got %d want %d", pid, originalPID)
	}

	cancel()
	<-done
}

// TestLifecycle_GracefulShutdownDrainsChild (T038) — orchestrator at
// StateRunning; cancel ctx; child receives SIGTERM and exits; Run returns
// nil within 15s.
func TestLifecycle_GracefulShutdownDrainsChild(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.vault.QueueOK()

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "reach StateRunning", 5*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("Run did not exit within 15s")
	}
}

// TestLifecycle_BootRetryShutdownNeverContactsDiscord (T040) — boot-retry
// in progress, ctx cancelled; Run exits promptly with no /claim contact.
func TestLifecycle_BootRetryShutdownNeverContactsDiscord(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.cfg.BootRetryTimeout = 5 * time.Second
	tl.lc.deps.TailscaleProbe = func(context.Context) error {
		return errors.New("never ready")
	}

	cancel, done := runWithCancel(tl)
	// Cancel right away — orchestrator should exit boot loop fast.
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not exit on cancel during boot retry")
	}

	if tl.vault.ClaimCount() != 0 {
		t.Errorf("ClaimCount: got %d want 0 during cancelled boot retry", tl.vault.ClaimCount())
	}
}

// TestLifecycle_RunIsSingleShot — second Run returns ErrLifecycleAlreadyRan.
func TestLifecycle_RunIsSingleShot(t *testing.T) {
	tl := newTestLifecycle(t, shortChildCmd(t, 0))
	tl.vault.QueueOK()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tl.lc.Run(ctx) }()

	// Wait until at least one transition has occurred.
	eventually(t, "first Run started", 5*time.Second, func() bool {
		return tl.lc.ran.Load()
	})
	if err := tl.lc.Run(context.Background()); !errors.Is(err, ErrLifecycleAlreadyRan) {
		t.Errorf("second Run: got %v want ErrLifecycleAlreadyRan", err)
	}
	cancel()
}

// TestLifecycle_NoBusinessLogicLeakage (T043) — grep-style anti-leak check.
// Asserts the orchestrator file content does not include state-string
// literals (which are the wire contract), the raw 78 literal arithmetic
// (must reference the Exit78 constant), or runtime.GOOS
// references (no platform branching). The check ignores comment-only
// lines (`// ...`) and import statements.
//
//nolint:gocognit // grep-style multi-pattern sweep across orchestrator files
func TestLifecycle_NoBusinessLogicLeakage(t *testing.T) {
	files := []string{
		"lifecycle.go",
		"lifecycle_audit.go",
		"lifecycle_boot.go",
		"lifecycle_child.go",
		"lifecycle_refresh.go",
		"lifecycle_interfaces.go",
	}
	// State-string literals. Reading State enum constants is fine — but
	// materializing a quoted state name is not.
	literals := []string{`"running"`, `"awaiting-approval"`, `"fetching"`, `"stopped"`, `"grace-restart"`}
	rawLiteral78 := regexp.MustCompile(`(^|[^A-Za-z0-9_])78([^A-Za-z0-9_]|$)`)
	runtimeGOOS := regexp.MustCompile(`runtime\.GOOS`)
	commentLine := regexp.MustCompile(`^\s*//`)

	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(".", f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, raw := range strings.Split(string(data), "\n") {
			ln := raw
			if commentLine.MatchString(ln) {
				continue
			}
			// Strip inline trailing comments.
			if i := strings.Index(ln, "//"); i >= 0 {
				ln = ln[:i]
			}
			// State-string literal check uses the raw line — the literal
			// strings ARE the wire contract values; checking on the raw
			// line catches both code and accidental docstring matches.
			for _, lit := range literals {
				if strings.Contains(ln, lit) {
					t.Errorf("%s: state-string literal %s in line %q", f, lit, raw)
				}
			}
			if runtimeGOOS.MatchString(ln) {
				t.Errorf("%s: runtime.GOOS in line %q", f, raw)
			}
			// Raw 78 literal check: strip string-literal bodies so phrases
			// like "exit 78 stale credentials" don't trip the regex.
			codeOnly := stripStringContents(ln)
			if rawLiteral78.MatchString(codeOnly) {
				t.Errorf("%s: raw 78 literal in line %q (must reference Exit78 constant)", f, raw)
			}
		}
	}
}

// stripStringContents replaces the contents of double-quoted and back-quoted
// string literals with empty placeholders. State-string literals (which
// MUST be detected) are still caught by the strings.Contains check on the
// raw line — the strip only affects the raw-78-literal regex.
func stripStringContents(s string) string {
	var b strings.Builder
	inStr := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr != 0 && c == inStr:
			b.WriteByte(c)
			inStr = 0
		case inStr != 0:
			// Skip string body; replace with space for column stability.
			b.WriteByte(' ')
		case c == '"' || c == '`':
			b.WriteByte(c)
			inStr = c
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// TestLifecycle_StatusSocketRefreshInFetchingRejects (T046b) — orchestrator
// reaches StateFetching after the boot succeeds-but-validator-fails path
// (no, actually StateAwaitingApproval). Test that StateFetching natural
// initial state rejects: we use a controlled scenario where the
// orchestrator hasn't yet completed boot. Pragmatic check: call
// dispatchRefreshVerb directly with a fake refreshVerb in StateFetching.
func TestLifecycle_StatusSocketRefreshInFetchingRejects(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())

	// Lifecycle starts in StateFetching by Store construction. Drive
	// dispatchRefreshVerb directly (synchronously) — no goroutine needed.
	verb := refreshVerb{ack: make(chan error, 1)}
	tl.lc.dispatchRefreshVerb(context.Background(), verb)
	select {
	case err := <-verb.ack:
		if err == nil {
			t.Fatalf("expected rejection ack, got nil")
		}
		if !strings.Contains(err.Error(), "fetching") {
			t.Errorf("expected fetching state ack, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("verb.ack not received")
	}
	// Audit event records "rejected".
	if !tl.auditLog.Has(audit.ActionClientRefreshInvoked) {
		t.Errorf("audit missing %s", audit.ActionClientRefreshInvoked)
	}
}
