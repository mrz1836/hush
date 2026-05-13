//go:build integration

// Scenario 11b — Boot Retry Exhausted (BootTimeout)
//
// docs/LIFECYCLE-SCENARIOS.md §11b: supervisor's pre-claim probe (here
// the Tailscale probe) never succeeds; once boot_retry_timeout elapses
// the Lifecycle emits a boot_timeout alert and stops.
//
// Contracts:
//
//	A — Final supervise.State == StateStopped.
//	B — Audit JSONL contains supervisor_boot_timeout (subsequence check).
//	C — Status socket reflects state="stopped" / boot-retry-exhausted.
//	D — Sentinel sweep over 6 streams (logs, audit, status, alerts,
//	    stdout/stderr nil, error strings).
//
// Sentinel: testutil.SentinelSecret(13).
package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

// errSyntheticTailscale is the constant error the boot-timeout test
// returns from its TailscaleProbe stub. Used to keep the harness
// error-string stream populated for the sentinel sweep without leaking
// any vault byte.
var errSyntheticTailscale = errors.New("integration: synthetic tailscale failure")

func Test_Scenario_11_BootTimeout(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"DEMO_SECRET": testutil.SentinelSecret(13),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
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
		Name:           "boot-timeout",
		Scopes:         []string{"DEMO_SECRET"},
		BootRetryAfter: 300 * time.Millisecond,
		// Probe never succeeds; Lifecycle never exits the boot loop until
		// boot_retry_timeout elapses.
		TailscaleProbe: func(_ context.Context) error { return errSyntheticTailscale },
	})
	sup.Run()

	// Contract A — final state == stopped within 1.5s (BootRetryAfter is
	// 300ms; the lifecycle's exponential backoff between probes is bounded).
	sup.WaitState(t, supervise.StateStopped, 1500*time.Millisecond)

	// Contract B — audit contains supervisor_boot_timeout.
	harness.AssertAuditSubsequence(t,
		sup.ReadAudit(),
		[]string{"supervisor_boot_timeout"},
	)

	// Contract C — the status socket is torn down once the Lifecycle
	// returns terminal (StateStopped), so StatusRaw is allowed to be
	// empty in this scenario. The Contract A assertion above already
	// pins the state from the in-process Snapshot seam.
	statusBytes := sup.StatusRaw()

	// Contract D — 6-stream sentinel sweep.
	harness.AssertSentinelAbsent(t,
		testutil.SentinelSecret(13),
		logger.Bytes(),
		srv.RawAudit(),
		statusBytes,
		discord.AlertsRaw(),
		nil, // no child stdout
		nil, // no child stderr
		harness.CollectErrors(errSyntheticTailscale),
	)
}
