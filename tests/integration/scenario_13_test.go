//go:build integration

// Scenario 13 — Mid-Session Rotation (docs/LIFECYCLE-SCENARIOS.md §13).
//
// The operator rotates a secret on the vault host while a daemon
// session is active. The vault file is atomically rewritten and the
// server reloads it. The operator then runs `hush client refresh`: the
// supervisor refetches the rotated secret, validates it, and restarts
// the child cleanly — rotation propagation is intentional and visible.
//
// Contracts:
//
//	A — After refresh the supervisor is back in StateRunning.
//	B — Audit subsequence: session_claimed → silent_refill.
//	C — The original (pre-rotation) sentinel never leaks across the
//	    6 streams; audit-chain continuity holds.
//
// Sentinel: testutil.SentinelSecret(15).
package integration_test

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_13_MidSessionRotation(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(15),
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
		Name:    "mid-rotation",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	sup.WaitState(t, supervise.StateRunning, 3*time.Second)

	// Operator rotates the secret; the vault file is rewritten and the
	// server reloads it (SIGHUP-equivalent atomic swap).
	vault.Rotate(t, "ANTHROPIC_API_KEY", "rotated-value-deadbeef-not-a-sentinel")
	if err := srv.Reload(t.Context()); err != nil {
		t.Fatalf("scenario_13: srv.Reload: %v", err)
	}

	// `hush client refresh` — refetch the rotated secret and restart.
	if err := sup.TriggerRefresh(t.Context()); err != nil {
		t.Fatalf("scenario_13: TriggerRefresh: %v", err)
	}

	// Contract A — the supervisor is running again on the rotated secret.
	sup.WaitState(t, supervise.StateRunning, 3*time.Second)

	// Contract B — the refetch + restart was recorded as a silent refill.
	sup.WaitAudit(t, "supervisor_silent_refill", 3*time.Second)
	harness.AssertAuditSubsequence(t,
		sup.ReadAudit(),
		[]string{"supervisor_session_claimed", "supervisor_silent_refill"},
	)

	// Contract C — the pre-rotation sentinel never leaked.
	harness.AssertSentinelAbsent(t,
		testutil.SentinelSecret(15),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}
