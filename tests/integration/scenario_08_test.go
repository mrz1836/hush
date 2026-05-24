//go:build integration

// Scenario 8 — Daytime Refresh (docs/LIFECYCLE-SCENARIOS.md §8).
//
// The refresh window arrives during waking hours. The supervisor swaps
// its JWT for the next session window via a fresh /claim. The child
// keeps running throughout — a refresh never forces a restart.
//
// Contracts:
//
//	A — Audit subsequence: session_claimed → session_refreshed.
//	B — The supervisor state stays StateRunning (no forced restart).
//	C — 6-stream sentinel sweep + audit-chain continuity.
//
// Sentinel: testutil.SentinelSecret(8).
package integration_test

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_08_DaytimeRefresh(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(8),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:   vault,
		Server:  srv,
		Discord: discord,
		Logger:  logger,
		Name:    "daytime-refresh",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	sup.WaitState(t, supervise.StateRunning, 3*time.Second)

	// The refresh window arrives — drive the claim-swap deterministically.
	sup.TriggerWindowRefresh(t.Context())

	// Contract A — the session JWT was refreshed.
	sup.WaitAudit(t, "supervisor_session_refreshed", 3*time.Second)
	harness.AssertAuditSubsequence(
		t,
		sup.ReadAudit(),
		[]string{"supervisor_session_claimed", "supervisor_session_refreshed"},
	)

	// Contract B — the child kept running across the refresh.
	harness.AssertSupervisorState(t, sup.State(), supervise.StateRunning)

	// Contract C — sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(
		t,
		testutil.SentinelSecret(8),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}
