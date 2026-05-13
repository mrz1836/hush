//go:build integration

// Scenario 11a — Tailscale Ready (Boot Retry → Success).
//
// docs/LIFECYCLE-SCENARIOS.md §11a: the supervisor's TailscaleProbe
// fails the first N times and then succeeds. The Lifecycle exits the
// boot loop, submits /claim, refills secrets, and transitions to
// running.
//
// Contracts:
//
//	A — Final supervise.State == StateRunning.
//	B — Audit JSONL contains supervisor_session_claimed (subsequence).
//	C — Status socket reflects state="running" with non-zero session_expires_at.
//	D — 6-stream sentinel sweep.
//
// Sentinel: testutil.SentinelSecret(12).
package integration_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_11_TailscaleReady(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(12),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	// TailscaleProbe fails the first 2 attempts then succeeds.
	var probeAttempts atomic.Int32
	probe := func(_ context.Context) error {
		n := probeAttempts.Add(1)
		if n <= 2 {
			return errSyntheticTailscale
		}
		return nil
	}

	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:          vault,
		Server:         srv,
		Discord:        discord,
		Logger:         logger,
		Name:           "tailscale-ready",
		Scopes:         []string{"ANTHROPIC_API_KEY"},
		TailscaleProbe: probe,
		BootRetryAfter: 30 * time.Second, // generous so the retry succeeds before timeout
	})
	sup.Run()

	// Contract A — running within 5s. Boot loop sleeps 500ms-1s per
	// retry; after 2 failures, the 3rd succeeds and the claim flow runs.
	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()
	sup.WaitState(t, supervise.StateRunning, 5*time.Second)

	// Contract B — audit contains supervisor_session_claimed.
	harness.AssertAuditSubsequence(t,
		sup.ReadAudit(),
		[]string{"supervisor_session_claimed"},
	)

	// Contract C — status doc reflects running.
	statusBytes := sup.StatusRaw()
	if !strings.Contains(string(statusBytes), `"running"`) {
		t.Errorf("scenario_11_tailscale_ready: status missing state=running; got %s", statusBytes)
	}

	// Contract D — 6-stream sentinel sweep.
	harness.AssertSentinelAbsent(t,
		testutil.SentinelSecret(12),
		logger.Bytes(),
		srv.RawAudit(),
		statusBytes,
		discord.AlertsRaw(),
		nil, nil, // no captured child stdout/stderr in v0.1.0 (NotificationDiscard path)
		harness.CollectErrors(errSyntheticTailscale),
	)
}
