package supervise

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/internal/vault/securebytes"
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

// TestLifecycle_ClaimApprovalTransientRetriesInProcess ensures launchd does not
// turn transient approval-path failures into a fast process-restart spam loop.
func TestLifecycle_ClaimApprovalTransientRetriesInProcess(t *testing.T) {
	for name, outcome := range map[string]claimOutcome{
		"approval_timeout": {status: http.StatusRequestTimeout, body: `{"error":"approval_timeout","request_id":"test"}`},
		"rate_limited":     {status: http.StatusTooManyRequests, body: `{"error":"rate_limited","request_id":"test"}`},
	} {
		t.Run(name, func(t *testing.T) {
			tl := newTestLifecycle(t, longChildCmd())
			tl.cfg.BootRetryTimeout = 5 * time.Second
			tl.vault.QueueClaim(outcome)
			tl.vault.QueueOK()

			cancel, done := runWithCancel(tl)
			defer cancel()

			eventually(t, "reach StateRunning after transient approval-path retry", 10*time.Second, func() bool {
				return snapshotState(tl) == StateRunning
			})
			if tl.vault.ClaimCount() < 2 {
				t.Errorf("ClaimCount: got %d want >= 2", tl.vault.ClaimCount())
			}

			cancel()
			<-done
		})
	}
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
	// Strict mode (grace cache disabled): a refill failure has no
	// last-known-good fallback and MUST page the operator.
	tl := newTestLifecycle(t, shortChildCmd(t, 0), func(c *config.Supervisor) {
		c.CacheSecretsForRestart = false
		c.CacheGraceTTL = 0
	})
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

// TestLifecycle_RefillJTIUnknownWithGraceEvictsAndPages verifies that
// an authoritative revoke (vault returns 401 unknown_jti) is NOT
// silently absorbed by the grace cache — even when
// cache_secrets_for_restart=true and every scope is still cached. The
// cached plaintext was materialized under the now-revoked session, so
// reusing it would bypass the operator's revoke decision.
//
// Expected on unknown_jti, grace-cache mode:
//   - State transitions to StateAwaitingApproval (NOT StateRunning).
//   - Grace cache is empty after the rejection (every entry evicted).
//   - AlertClassVaultRejectedJWT fires (the specific revoke-related
//     alert, not the generic AlertClassGraceEntered).
//   - NO ActionSupervisorGraceEntered audit event — the grace-cache
//     fallback path is NOT taken.
func TestLifecycle_RefillJTIUnknownWithGraceEvictsAndPages(t *testing.T) {
	// Grace cache enabled — the very mode the original V3 finding
	// exposed as silently bypassing operator revoke.
	tl := newTestLifecycle(t, shortChildCmd(t, 0), func(c *config.Supervisor) {
		c.CacheSecretsForRestart = true
		c.CacheGraceTTL = 4 * time.Hour
	})
	tl.vault.QueueOK()

	// Flip the scope to unknown_jti after the first child exits — the
	// silent-refill path is the surface that V3 fixed. Before the fix,
	// silentRefillAndRestart fell through to tryGraceRestart for
	// ErrJTIUnknown and the cached plaintext restarted the child.
	go func() {
		for {
			if tl.auditLog.Has(audit.ActionSupervisorChildCleanExit) {
				tl.vault.FailScopeUnauthorizedJTI("ANTHROPIC_API_KEY")
				return
			}
			runtime.Gosched()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	cancel, done := runWithCancel(tl)
	defer cancel()

	// First, the supervisor must reach StateRunning so retainSecrets
	// has populated the grace cache.
	eventually(t, "reach StateRunning before revoke", 5*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})
	tl.lc.grace.mu.RLock()
	preEntries := len(tl.lc.grace.entries)
	tl.lc.grace.mu.RUnlock()
	if preEntries == 0 {
		t.Fatalf("grace.entries empty at StateRunning; test precondition broken (V3 fix requires a populated cache to evict)")
	}

	// After the revoke, the supervisor must page the operator.
	eventually(t, "StateAwaitingApproval + VaultRejectedJWT after revoke", 10*time.Second, func() bool {
		return snapshotState(tl) == StateAwaitingApproval &&
			tl.alerts.CountClass(AlertClassVaultRejectedJWT) >= 1
	})

	// V3 invariant: the grace cache MUST be fully evicted on
	// authoritative revoke — every plaintext from the now-revoked
	// session is zeroed.
	tl.lc.grace.mu.RLock()
	postEntries := len(tl.lc.grace.entries)
	tl.lc.grace.mu.RUnlock()
	if postEntries != 0 {
		t.Errorf("grace.entries=%d after unknown_jti; want 0 (grace must be evicted on authoritative revoke)", postEntries)
	}

	// V3 invariant: the grace-cache fallback path was NOT taken — no
	// AlertClassGraceEntered should fire under unknown_jti.
	if c := tl.alerts.CountClass(AlertClassGraceEntered); c != 0 {
		t.Errorf("AlertClassGraceEntered count=%d on unknown_jti; want 0 (grace fallback is forbidden on authoritative revoke)", c)
	}
	if tl.auditLog.Has(audit.ActionSupervisorGraceEntered) {
		t.Errorf("audit chain contains %s on unknown_jti; the grace-cache restart path must NOT execute", audit.ActionSupervisorGraceEntered)
	}

	cancel()
	<-done
}

// TestLifecycle_RefillTransientErrorPostRunningEmitsRefillFailed (T022) —
// child exits 0; mockVault returns 500 on second Refill; expect
// StateAwaitingApproval + AlertClassRefillFailed.
func TestLifecycle_RefillTransientErrorPostRunningEmitsRefillFailed(t *testing.T) {
	// Strict mode (grace cache disabled): a transient refill failure has
	// no last-known-good fallback and MUST page the operator.
	tl := newTestLifecycle(t, shortChildCmd(t, 0), func(c *config.Supervisor) {
		c.CacheSecretsForRestart = false
		c.CacheGraceTTL = 0
	})
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

// TestLifecycle_RefillTransientErrorPostRunningFallsBackToGraceCache
// is the positive companion to TestLifecycle_RefillJTIUnknownWithGraceEvictsAndPages:
// a TRANSIENT refill failure (vault 500, not unknown_jti) with grace
// enabled MUST restart the child from the cached plaintext (docs §9)
// — the operator's prior approval still stands, so silent recovery is
// the correct response. This anchors the V3 fix: only authoritative
// revoke (unknown_jti) skips the grace fallback; transient failures
// continue to use it.
func TestLifecycle_RefillTransientErrorPostRunningFallsBackToGraceCache(t *testing.T) {
	tl := newTestLifecycle(t, shortChildCmd(t, 0), func(c *config.Supervisor) {
		c.CacheSecretsForRestart = true
		c.CacheGraceTTL = 4 * time.Hour
	})
	tl.vault.QueueOK()

	go func() {
		for {
			if tl.auditLog.Has(audit.ActionSupervisorChildCleanExit) {
				// 500 is transient (network blip / vault temp outage),
				// NOT an authoritative revoke.
				tl.vault.FailScopeStatus("ANTHROPIC_API_KEY", 500)
				return
			}
			runtime.Gosched()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	cancel, done := runWithCancel(tl)
	defer cancel()

	// Reach Running so retainSecrets populates the grace cache.
	eventually(t, "reach StateRunning before transient failure", 5*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})

	// Grace-cache restart is the expected response — operator must NOT
	// be paged.
	eventually(t, "grace-entered audit after transient failure", 10*time.Second, func() bool {
		return tl.auditLog.Has(audit.ActionSupervisorGraceEntered)
	})
	if c := tl.alerts.CountClass(AlertClassVaultRejectedJWT); c != 0 {
		t.Errorf("AlertClassVaultRejectedJWT count=%d on transient 500; want 0 (this is not a revoke)", c)
	}

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

// TestLifecycle_ShutdownDestroysGraceCache verifies the Principle-VI
// explicit-zeroing path: when the supervisor's runShutdown returns, the
// Grace cache has been Destroy'd so any retained per-scope plaintext is
// zeroed rather than left for the runtime finalizer (which does not run
// on process exit).
func TestLifecycle_ShutdownDestroysGraceCache(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.vault.QueueOK()

	cancel, done := runWithCancel(tl)
	defer cancel()

	// Reach StateRunning so retainSecrets has populated the grace cache.
	eventually(t, "reach StateRunning", 5*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})
	if !tl.lc.grace.Enabled() {
		t.Fatalf("grace cache disabled at StateRunning; test precondition broken")
	}
	tl.lc.grace.mu.RLock()
	preShutdownEntries := len(tl.lc.grace.entries)
	tl.lc.grace.mu.RUnlock()
	if preShutdownEntries == 0 {
		t.Fatalf("grace.entries empty at StateRunning; expected at least 1 cached scope")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("Run did not exit within 15s")
	}

	// Post-shutdown: grace must be drained and disabled.
	tl.lc.grace.mu.RLock()
	post := len(tl.lc.grace.entries)
	tl.lc.grace.mu.RUnlock()
	if post != 0 {
		t.Fatalf("grace.entries=%d after shutdown; want 0 (Destroy not called)", post)
	}
	if tl.lc.grace.Enabled() {
		t.Fatalf("grace.Enabled() = true after shutdown; want false (Destroy not called)")
	}
}

// TestLifecycle_ShutdownDestroysStoreToken verifies the V4 fix: the
// supervisor's runShutdown explicitly destroys the Store's current
// JWT *SecureBytes (in addition to the grace cache) so the bearer
// token plaintext is zeroed on the SIGTERM path rather than relying
// on the runtime finalizer (which does NOT run on process exit per
// Principle VI). Pre-V4, the final JWT lingered in mlocked memory
// until the OS reclaimed the page.
func TestLifecycle_ShutdownDestroysStoreToken(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.vault.QueueOK()

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "reach StateRunning", 5*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})

	// Capture the live token pointer BEFORE shutdown so we can probe it
	// post-shutdown via Use() — the orchestrator's runShutdown is the
	// only safe place that calls store.destroyToken().
	snapBefore := tl.lc.store.Snapshot()
	if snapBefore.Token == nil {
		t.Fatalf("Snapshot.Token nil at StateRunning; test precondition broken")
	}
	if useErr := snapBefore.Token.Use(func(_ []byte) {}); useErr != nil {
		t.Fatalf("pre-shutdown Token.Use err=%v; want nil (token must be live before shutdown)", useErr)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("Run did not exit within 15s")
	}

	// Post-shutdown: the captured pointer MUST report destroyed.
	if useErr := snapBefore.Token.Use(func(_ []byte) {}); !errors.Is(useErr, securebytes.ErrDestroyed) {
		t.Errorf("post-shutdown Token.Use err=%v; want ErrDestroyed (V4: runShutdown must destroyToken)", useErr)
	}
	if post := tl.lc.store.Snapshot(); post.Token != nil {
		t.Errorf("Snapshot.Token=%v after shutdown; want nil", post.Token)
	}
}

// TestLifecycle_RefreshRotationDestroysPriorToken verifies that the
// V4 fix is also wired through the refresh path: when the Refresher
// performs a /claim swap mid-session, the OLD JWT *SecureBytes is
// explicitly destroyed by store.setToken (no accumulation of stale
// bearer tokens in mlocked memory across rotations).
func TestLifecycle_RefreshRotationDestroysPriorToken(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	tl.vault.QueueOK() // boot claim
	tl.vault.QueueOK() // refresh claim

	cancel, done := runWithCancel(tl)
	defer cancel()

	eventually(t, "reach StateRunning before refresh", 5*time.Second, func() bool {
		return snapshotState(tl) == StateRunning
	})

	priorSnap := tl.lc.store.Snapshot()
	if priorSnap.Token == nil {
		t.Fatalf("Snapshot.Token nil at StateRunning; test precondition broken")
	}
	priorPtr := priorSnap.Token

	// Drive a refresh swap by nudging the refresh tick channel — same
	// path the Refresher window-fire goroutine uses.
	select {
	case tl.lc.refreshTickCh <- struct{}{}:
	default:
		t.Fatalf("refreshTickCh full; cannot nudge refresh")
	}

	// Wait for the new claim to be issued by the mock vault.
	eventually(t, "vault sees second claim", 5*time.Second, func() bool {
		return tl.vault.ClaimCount() >= 2
	})
	// Wait for the orchestrator to swap the token in the store.
	eventually(t, "store token pointer rotated", 5*time.Second, func() bool {
		return tl.lc.store.Snapshot().Token != priorPtr
	})

	// V4 invariant: the prior token MUST be destroyed after the swap.
	if useErr := priorPtr.Use(func(_ []byte) {}); !errors.Is(useErr, securebytes.ErrDestroyed) {
		t.Errorf("prior Token.Use after refresh swap: %v want ErrDestroyed (V4: setToken must destroy prior)", useErr)
	}

	cancel()
	<-done
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
