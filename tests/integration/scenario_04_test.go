//go:build integration

// Scenario 4 — Child Crash Refill (docs/LIFECYCLE-SCENARIOS.md §4).
//
// The child crashes (non-zero, non-78 exit) inside a still-valid
// session. The supervisor stays alive, silently refetches secrets, and
// restarts the child — no new Discord approval, no approval spam.
//
// Contracts:
//
//	A — Audit subsequence: session_claimed → child_exit_crash → silent_refill.
//	B — No operator alert was emitted (silent path).
//	C — 6-stream sentinel sweep + audit-chain continuity.
//
// Sentinel: testutil.SentinelSecret(4).
package integration_test

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_04_ChildCrashRefill(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(4),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	// Scripted child: crashes (exit 1) after a brief lifetime.
	child := harness.NewChild(t, logger, harness.ChildOpts{
		ExitCode: 1,
		Lifetime: 150 * time.Millisecond,
	})

	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:   vault,
		Server:  srv,
		Discord: discord,
		Logger:  logger,
		Name:    "crash-refill",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
		Child:   child,
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	sup.WaitState(t, supervise.StateRunning, 3*time.Second)

	// Contract A — crash triggered a silent refill.
	sup.WaitAudit(t, "supervisor_silent_refill", 5*time.Second)
	harness.AssertAuditSubsequence(t,
		sup.ReadAudit(),
		[]string{"supervisor_session_claimed", "supervisor_child_exit_crash", "supervisor_silent_refill"},
	)

	// Contract B — crash refill must not page the operator (no approval spam).
	if alerts := discord.Alerts(); len(alerts) != 0 {
		t.Errorf("scenario_04: crash refill emitted %d alert(s); want 0: %+v", len(alerts), alerts)
	}

	// Contract C — sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(t,
		testutil.SentinelSecret(4),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		child.Stdout(), child.Stderr(),
	)
	sup.AssertAuditChain(t)
}
