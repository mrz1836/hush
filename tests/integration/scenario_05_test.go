//go:build integration

// Scenario 5 — Child Exit 78 Stale (docs/LIFECYCLE-SCENARIOS.md §5).
//
// The child detects auth drift and exits with code 78. The supervisor
// treats exit 78 as an authoritative stale-credential signal: it does
// NOT silently restart, enters `awaiting-approval`, and emits a
// [STALE] Child Exit 78 alert.
//
// Contracts:
//
//	A — Final supervise.State == StateAwaitingApproval.
//	B — Audit subsequence: session_claimed → child_exit_78 →
//	    stale_alert → awaiting_approval.
//	C — Exactly one AlertClassExit78 alert; no silent refill.
//	D — 6-stream sentinel sweep + audit-chain continuity.
//
// Sentinel: testutil.SentinelSecret(5).
package integration_test

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_05_ChildExit78Stale(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(5),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	// Scripted child: exits with the stale-credentials code 78.
	child := harness.NewChild(t, logger, harness.ChildOpts{
		ExitCode: 78,
		Lifetime: 150 * time.Millisecond,
	})

	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:   vault,
		Server:  srv,
		Discord: discord,
		Logger:  logger,
		Name:    "exit-78",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
		Child:   child,
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	// Contract A — exit 78 short-circuits the restart loop.
	sup.WaitState(t, supervise.StateAwaitingApproval, 5*time.Second)

	// Contract B — audit records the exit-78 stale path.
	harness.AssertAuditSubsequence(t,
		sup.ReadAudit(),
		[]string{
			"supervisor_session_claimed",
			"supervisor_child_exit_78",
			"supervisor_stale_alert",
			"supervisor_awaiting_approval",
		},
	)

	// Contract C — one Exit78 alert; the child was NOT silently refilled.
	if !discord.HasAlert(supervise.AlertClassExit78) {
		t.Errorf("scenario_05: missing AlertClassExit78; alerts=%+v", discord.Alerts())
	}
	if sup.HasAudit("supervisor_silent_refill") {
		t.Errorf("scenario_05: exit 78 must NOT trigger a silent refill")
	}

	// Contract D — sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(t,
		testutil.SentinelSecret(5),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		child.Stdout(), child.Stderr(),
	)
	sup.AssertAuditChain(t)
}
