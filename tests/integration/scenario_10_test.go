//go:build integration

// Scenario 10 — Discord Unavailable (docs/LIFECYCLE-SCENARIOS.md §10).
//
// Discord is disconnected when the supervisor submits its first /claim.
// The server returns 503 (no auto-approve fallback). The supervisor
// surfaces the failure, retries until boot_retry_timeout, then stops —
// it never issues a session without approval (fail closed).
//
// Contracts:
//
//	A — Final supervise.State == StateStopped (no `running`).
//	B — No supervisor_session_claimed audit event (no session issued).
//	C — At least one AlertClassDiscordUnavailableOnClaim alert.
//	D — 6-stream sentinel sweep + audit-chain continuity.
//
// Sentinel: testutil.SentinelSecret(11).
package integration_test

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_10_DiscordUnavailable(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(11),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	// Discord is unplugged before the supervisor ever submits a /claim.
	discord.SetConnected(false)
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:          vault,
		Server:         srv,
		Discord:        discord,
		Logger:         logger,
		Name:           "discord-down",
		Scopes:         []string{"ANTHROPIC_API_KEY"},
		BootRetryAfter: 400 * time.Millisecond,
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	// Contract A — fail closed: the supervisor stops, never reaches running.
	sup.WaitState(t, supervise.StateStopped, 3*time.Second)

	// Contract B — no session was ever issued.
	if sup.HasAudit("supervisor_session_claimed") {
		t.Errorf("scenario_10: a session was claimed despite Discord being unavailable")
	}

	// Contract C — the operator saw the Discord-unavailable alert.
	if !discord.HasAlert(supervise.AlertClassDiscordUnavailableOnClaim) {
		t.Errorf("scenario_10: missing AlertClassDiscordUnavailableOnClaim; alerts=%+v", discord.Alerts())
	}

	// Contract D — sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(t,
		testutil.SentinelSecret(11),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}
