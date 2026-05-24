//go:build integration

// Scenario 15 — Log Pattern Match (docs/LIFECYCLE-SCENARIOS.md §15).
//
// The child emits a known auth-failure log line. The watchdog matches
// the configured pattern and emits a [STALE] Log Pattern Match alert.
// The match is advisory only: NO restart, NO state change.
//
// Contracts:
//
//	A — One AlertClassLogPatternMatch alert is emitted.
//	B — The supervisor state stays StateRunning (watchdog has zero
//	    state-machine authority).
//	C — 6-stream sentinel sweep + audit-chain continuity.
//
// Sentinel: testutil.SentinelSecret(17).
package integration_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/supervise/watchdog"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_15_LogPatternMatch(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(17),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	// Scripted child: emits a matching auth-failure line on stderr, then
	// stays alive — the scenario asserts the watchdog does NOT stop it.
	child := harness.NewChild(t, logger, harness.ChildOpts{
		ExitCode:   0,
		Lifetime:   10 * time.Minute,
		EmitStderr: "ERROR provider rejected request: authentication failed (401)",
	})

	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:   vault,
		Server:  srv,
		Discord: discord,
		Logger:  logger,
		Name:    "log-watchdog",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
		Child:   child,
		WatchdogPatterns: []watchdog.Pattern{{
			Name:      "auth-failure",
			Regex:     regexp.MustCompile(`authentication failed`),
			RateLimit: time.Minute,
		}},
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	sup.WaitState(t, supervise.StateRunning, 3*time.Second)

	// Contract A — the watchdog raised the log-pattern alert.
	discord.WaitAlert(t, supervise.AlertClassLogPatternMatch, 5*time.Second)

	// Contract B — a log match is advisory: the state machine is untouched.
	harness.AssertSupervisorState(t, sup.State(), supervise.StateRunning)
	if sup.HasAudit("supervisor_awaiting_approval") {
		t.Errorf("scenario_15: a log-pattern match must not drive a state transition")
	}

	// Contract C — sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(
		t,
		testutil.SentinelSecret(17),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		child.Stdout(), child.Stderr(),
	)
	sup.AssertAuditChain(t)
}
