//go:build integration

// Scenario 3 — Clean Child Exit Refill (docs/LIFECYCLE-SCENARIOS.md §3).
//
// The child exits 0 inside a still-valid supervisor session. The
// supervisor silently refetches secrets with the existing JWT, re-runs
// validators, and restarts the child — no Discord approval, no alert.
//
// Contracts:
//
//	A — Audit subsequence: session_claimed → child_clean_exit → silent_refill.
//	B — No operator alert was emitted (silent path).
//	C — 6-stream sentinel sweep + audit-chain continuity.
//
// Sentinel: testutil.SentinelSecret(3).
package integration_test

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_03_CleanChildExitRefill(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(3),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	// Scripted child: exits cleanly (code 0) after a brief lifetime.
	child := harness.NewChild(t, logger, harness.ChildOpts{
		ExitCode: 0,
		Lifetime: 150 * time.Millisecond,
	})

	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:   vault,
		Server:  srv,
		Discord: discord,
		Logger:  logger,
		Name:    "clean-exit",
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

	// Contract A — the silent refill completed after a clean exit.
	sup.WaitAudit(t, "supervisor_silent_refill", 5*time.Second)
	harness.AssertAuditSubsequence(t,
		sup.ReadAudit(),
		[]string{"supervisor_session_claimed", "supervisor_child_clean_exit", "supervisor_silent_refill"},
	)

	// Contract B — clean refill is silent: no operator alert.
	if alerts := discord.Alerts(); len(alerts) != 0 {
		t.Errorf("scenario_03: clean-exit refill emitted %d alert(s); want 0: %+v", len(alerts), alerts)
	}

	// Contract C — sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(t,
		testutil.SentinelSecret(3),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		child.Stdout(), child.Stderr(),
	)
	sup.AssertAuditChain(t)
}
