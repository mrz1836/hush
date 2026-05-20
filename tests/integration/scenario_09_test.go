//go:build integration

// Scenario 9 — Overnight Expiry, strict and grace
// (docs/LIFECYCLE-SCENARIOS.md §9).
//
// The session is no longer usable (simulated via FlushSessions) and the
// child needs a restart. The two sub-scenarios differ only in the
// cache_secrets_for_restart config flag:
//
//   - 09a strict (flag false): no last-known-good fallback exists, so the
//     supervisor enters `awaiting-approval` and the child stays down.
//   - 09b grace (flag true): the supervisor restarts the child from the
//     grace-cached plaintext and stays `running` — no 3am page.
//
// The "child needs a restart" trigger is driven via the refresh verb so
// the moment is deterministic; the refill+restart path it exercises is
// identical to a real child crash.
//
// Sentinels: testutil.SentinelSecret(9) strict, SentinelSecret(10) grace.
package integration_test

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_09_OvernightExpiry_Strict(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(9),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:                  vault,
		Server:                 srv,
		Discord:                discord,
		Logger:                 logger,
		Name:                   "overnight-strict",
		Scopes:                 []string{"ANTHROPIC_API_KEY"},
		CacheSecretsForRestart: boolPtr(false),
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	sup.WaitState(t, supervise.StateRunning, 3*time.Second)

	// Session expires overnight; the child later needs a restart.
	srv.FlushSessions()
	_ = sup.TriggerRefresh(t.Context())

	// Strict mode — no grace fallback: the child stays down, operator paged.
	sup.WaitState(t, supervise.StateAwaitingApproval, 3*time.Second)
	if sup.HasAudit("supervisor_grace_entered") {
		t.Errorf("scenario_09a: strict mode must NOT enter grace restart")
	}
	harness.AssertAuditSubsequence(t,
		sup.ReadAudit(),
		[]string{"supervisor_session_claimed", "supervisor_awaiting_approval"},
	)

	harness.AssertSentinelAbsent(t,
		testutil.SentinelSecret(9),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}

func Test_Scenario_09_OvernightExpiry_Grace(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(10),
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
		Name:    "overnight-grace",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
		// Grace mode is the harness default (cache_secrets_for_restart=true).
		CacheSecretsForRestart: boolPtr(true),
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	sup.WaitState(t, supervise.StateRunning, 3*time.Second)

	// Session expires overnight; the child later needs a restart.
	srv.FlushSessions()
	_ = sup.TriggerRefresh(t.Context())

	// Grace mode — the child restarts from cached plaintext, no 3am page.
	sup.WaitState(t, supervise.StateRunning, 3*time.Second)
	harness.AssertAuditSubsequence(t,
		sup.ReadAudit(),
		[]string{
			"supervisor_session_claimed",
			"supervisor_grace_entered",
			"supervisor_grace_exited",
		},
	)
	if sup.HasAudit("supervisor_awaiting_approval") {
		t.Errorf("scenario_09b: grace mode must NOT page the operator")
	}

	harness.AssertSentinelAbsent(t,
		testutil.SentinelSecret(10),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}
