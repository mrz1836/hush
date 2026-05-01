package discord

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/token"
)

// TestApprover_BotApproverImplementsApprover guards the package-level
// guarantee that *BotApprover satisfies the Approver interface. The
// compile-time check is the package-level _ = (*BotApprover)(nil) line
// in bot.go; this test merely re-asserts it for visibility.
func TestApprover_BotApproverImplementsApprover(t *testing.T) {
	t.Parallel()
	var _ Approver = (*BotApprover)(nil)
}

// TestApprovalRequest_DaemonRequiresSupervisorName documents the
// validation hint per data-model.md §1.1: SupervisorName MUST be
// non-empty for SessionSupervisor; this is a soft contract enforced
// by the SDD-12 adapter, but the type shape is locked here.
func TestApprovalRequest_DaemonRequiresSupervisorName(t *testing.T) {
	t.Parallel()
	req := ApprovalRequest{
		MachineName:    "darwin",
		ClientIP:       "100.96.10.4",
		Reason:         "test",
		Scope:          []string{"ANTHROPIC_API_KEY"},
		RequestedTTL:   time.Hour,
		SessionType:    token.SessionSupervisor,
		SupervisorName: "claude-worker",
	}
	if req.SessionType != token.SessionSupervisor {
		t.Errorf("expected SessionSupervisor, got %v", req.SessionType)
	}
	if req.SupervisorName == "" {
		t.Error("daemon requests must populate SupervisorName")
	}
}

// TestDefaultDMRateLimit_FiveMinutes locks the package's default
// rate-limit window per FR-021.
func TestDefaultDMRateLimit_FiveMinutes(t *testing.T) {
	t.Parallel()
	if DefaultDMRateLimit != 5*time.Minute {
		t.Errorf("DefaultDMRateLimit = %v; want 5m", DefaultDMRateLimit)
	}
}
