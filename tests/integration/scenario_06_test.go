//go:build integration

// Scenario 6 — Validator Failure (docs/LIFECYCLE-SCENARIOS.md §6).
//
// A configured validator rejects a fetched secret before the child
// starts. The supervisor blocks the child launch, emits a
// [STALE] Validator Failure alert naming the scope, and enters
// `awaiting-approval`.
//
// Contracts:
//
//	A — Final supervise.State == StateAwaitingApproval.
//	B — Audit subsequence: session_claimed → stale_alert → awaiting_approval.
//	C — One AlertClassValidatorFailure alert naming the failed scope.
//	D — 6-stream sentinel sweep + audit-chain continuity.
//
// Sentinel: testutil.SentinelSecret(6).
package integration_test

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_06_ValidatorFailure(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(6),
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
		Name:    "validator-fail",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
		Validators: map[string]supervise.Validator{
			"ANTHROPIC_API_KEY": failingValidator{},
		},
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	// Contract A — validator failure blocks the child and pages the operator.
	sup.WaitState(t, supervise.StateAwaitingApproval, 5*time.Second)

	// Contract B — audit records the validator stale path.
	harness.AssertAuditSubsequence(
		t,
		sup.ReadAudit(),
		[]string{
			"supervisor_session_claimed",
			"supervisor_stale_alert",
			"supervisor_awaiting_approval",
		},
	)

	// Contract C — one ValidatorFailure alert naming the scope.
	var sawValidatorAlert bool
	for _, a := range discord.Alerts() {
		if a.Class == supervise.AlertClassValidatorFailure {
			sawValidatorAlert = true
			if a.Scope != "ANTHROPIC_API_KEY" {
				t.Errorf("scenario_06: validator alert scope=%q, want ANTHROPIC_API_KEY", a.Scope)
			}
		}
	}
	if !sawValidatorAlert {
		t.Errorf("scenario_06: missing AlertClassValidatorFailure; alerts=%+v", discord.Alerts())
	}
	if sup.HasAudit("supervisor_silent_refill") {
		t.Errorf("scenario_06: a blocked child must not produce a silent refill")
	}

	// Contract D — sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(
		t,
		testutil.SentinelSecret(6),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}
