//go:build integration

// Scenario 2 — First Daemon Bootstrap (docs/LIFECYCLE-SCENARIOS.md §2).
//
// The supervisor boots, submits one signed /claim, fetches its scoped
// secret, runs validators, starts the child, and enters `running`.
//
// Contracts:
//
//	A — Final supervise.State == StateRunning.
//	B — Audit JSONL contains supervisor_session_claimed.
//	C — Status socket reports state="running".
//	D — 6-stream sentinel sweep + audit-chain continuity.
//
// Sentinel: testutil.SentinelSecret(2).
package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_02_FirstDaemonBootstrap(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(2),
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
		Name:    "first-boot",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	// Contract A — running within 3s.
	sup.WaitState(t, supervise.StateRunning, 3*time.Second)

	// Contract B — audit contains supervisor_session_claimed.
	harness.AssertAuditSubsequence(
		t,
		sup.ReadAudit(),
		[]string{"supervisor_session_claimed"},
	)

	// Contract C — status doc state=running.
	statusBytes := sup.StatusRaw()
	var doc map[string]any
	if err := json.Unmarshal(statusBytes, &doc); err != nil {
		t.Fatalf("scenario_02: status JSON unmarshal: %v\nbytes=%s", err, statusBytes)
	}
	if got, _ := doc["state"].(string); got != "running" {
		t.Errorf("scenario_02: status state=%q, want running", got)
	}
	if !strings.Contains(string(statusBytes), `"running"`) {
		t.Errorf("scenario_02: status missing running marker; got %s", statusBytes)
	}

	// Contract D — sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(
		t,
		testutil.SentinelSecret(2),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		statusBytes,
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}
