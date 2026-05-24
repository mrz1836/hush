//go:build integration

// Scenario 7 — Vault Restart Invalidates Session
// (docs/LIFECYCLE-SCENARIOS.md §7).
//
// The vault server restarts and loses its in-memory active-session map
// (simulated via TestServer.FlushSessions). A later refill is rejected
// (401). In strict mode (cache_secrets_for_restart=false) there is no
// last-known-good fallback, so the supervisor enters `awaiting-approval`
// — cleanly, and without an infinite silent-refill retry loop.
//
// Contracts:
//
//	A — Final supervise.State == StateAwaitingApproval.
//	B — Audit subsequence: session_claimed → stale_alert → awaiting_approval.
//	C — Bounded: the state stays awaiting-approval (no retry storm).
//	D — 6-stream sentinel sweep + audit-chain continuity.
//
// Sentinel: testutil.SentinelSecret(7).
package integration_test

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_07_VaultRestartInvalidatesSession(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(7),
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
		Name:    "vault-restart",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
		// Strict mode: a refill failure has no grace-cache fallback.
		CacheSecretsForRestart: boolPtr(false),
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	sup.WaitState(t, supervise.StateRunning, 3*time.Second)

	// Vault server "restarts": every issued session JTI is invalidated.
	srv.FlushSessions()

	// Drive a refresh — the refill now fails the bearer check.
	_ = sup.TriggerRefresh(t.Context())

	// Contract A — the supervisor parks in awaiting-approval.
	sup.WaitState(t, supervise.StateAwaitingApproval, 3*time.Second)

	// Contract B — audit records the stale path.
	harness.AssertAuditSubsequence(
		t,
		sup.ReadAudit(),
		[]string{
			"supervisor_session_claimed",
			"supervisor_stale_alert",
			"supervisor_awaiting_approval",
		},
	)

	// Contract C — bounded: no retry storm. The state holds steady.
	time.Sleep(150 * time.Millisecond)
	harness.AssertSupervisorState(t, sup.State(), supervise.StateAwaitingApproval)

	// Contract D — sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(
		t,
		testutil.SentinelSecret(7),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}
