//go:build integration

// Scenario 12 — Agent Status Check.
//
// docs/LIFECYCLE-SCENARIOS.md §12: an operator dials the supervisor's
// status socket and the supervisor returns a status JSON document
// reflecting state=running and populated session metadata.
//
// Contracts:
//
//	A — Final supervise.State == StateRunning.
//	B — Audit JSONL contains supervisor_session_claimed.
//	C — Status doc parses, has state="running" and a name field.
//	D — Sentinel sweep over 6 streams.
//
// Sentinel: testutil.SentinelSecret(14).
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

func Test_Scenario_12_AgentStatusCheck(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(14),
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
		Name:    "agent-status",
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
	harness.AssertAuditSubsequence(t,
		sup.ReadAudit(),
		[]string{"supervisor_session_claimed"},
	)

	// Contract C — status doc shape + values.
	statusBytes := sup.StatusRaw()
	if !strings.Contains(string(statusBytes), `"running"`) {
		t.Errorf("scenario_12: status missing state=running; got %s", statusBytes)
	}
	var doc map[string]any
	if err := json.Unmarshal(statusBytes, &doc); err != nil {
		t.Fatalf("scenario_12: status JSON unmarshal: %v\nbytes=%s", err, statusBytes)
	}
	if got, _ := doc["supervisor"].(string); got != "agent-status" {
		t.Errorf("scenario_12: status supervisor=%q, want %q", got, "agent-status")
	}
	if got, _ := doc["state"].(string); got != "running" {
		t.Errorf("scenario_12: status state=%q, want %q", got, "running")
	}

	// Contract D — 6-stream sentinel sweep.
	harness.AssertSentinelAbsent(t,
		testutil.SentinelSecret(14),
		logger.Bytes(),
		srv.RawAudit(),
		statusBytes,
		discord.AlertsRaw(),
		nil, nil, // no captured child stdout/stderr in v0.1.0
	)
}
