package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mrz1836/hush/internal/token"
)

func TestRender_AgentContext_InteractivePopulatesAllFields(t *testing.T) {
	req := ApprovalRequest{
		MachineName:    "starbird.local",
		ClientIP:       "100.64.1.5",
		Reason:         "refactor auth",
		Scope:          []string{"GITHUB_TOKEN"},
		RequestedTTL:   10 * time.Minute,
		SessionType:    token.SessionInteractive,
		RequestID:      "req-abc",
		AgentIdentity:  "claude-code/1.2.3",
		AgentModel:     "claude-opus-4-7",
		ToolName:       "Bash",
		CommandPreview: "git push origin main",
		RecentSummary:  "Refactoring auth module",
	}
	msg := renderApproval(req, "uuid")
	body := msg.Embeds[0].Description
	for _, want := range []string{
		"claude-code/1.2.3",
		"claude-opus-4-7",
		"Bash",
		"git push origin main",
		"Refactoring auth module",
	} {
		assert.Contains(t, body, want, "missing agent-context field %q in:\n%s", want, body)
	}
}

func TestRender_AgentContext_DaemonPopulatesAllFields(t *testing.T) {
	req := ApprovalRequest{
		MachineName:    "starbird.local",
		ClientIP:       "100.64.1.5",
		Reason:         "scheduled refill",
		Scope:          []string{"OPENAI_API_KEY"},
		RequestedTTL:   24 * time.Hour,
		SessionType:    token.SessionSupervisor,
		SupervisorName: "hermes",
		RequestID:      "req-xyz",
		AgentIdentity:  "hermes-gateway/0.9.1",
		ToolName:       "validator",
	}
	msg := renderApproval(req, "uuid")
	body := msg.Embeds[0].Description
	assert.Contains(t, body, "Supervisor: hermes")
	assert.Contains(t, body, "hermes-gateway/0.9.1")
	assert.Contains(t, body, "validator")
}

func TestRender_AgentContext_EmptyFieldsAreOmitted(t *testing.T) {
	// All five agent fields empty — the render should look identical
	// to a pre-PR-4 prompt (no Agent/Model/Tool/Command/Summary rows).
	req := ApprovalRequest{
		MachineName:  "starbird.local",
		ClientIP:     "100.64.1.5",
		Reason:       "test",
		Scope:        []string{"SCOPE_A"},
		RequestedTTL: time.Minute,
		SessionType:  token.SessionInteractive,
		RequestID:    "req-empty",
	}
	msg := renderApproval(req, "uuid")
	body := msg.Embeds[0].Description
	for _, banned := range []string{"Agent:", "Model:", "Tool:", "Command:", "Summary:"} {
		assert.NotContains(t, body, banned, "empty field should not appear as row label %q in:\n%s", banned, body)
	}
}

func TestRender_AgentContext_LongCommandIsTruncated(t *testing.T) {
	long := strings.Repeat("x", 700)
	req := ApprovalRequest{
		MachineName:    "starbird.local",
		ClientIP:       "100.64.1.5",
		Reason:         "test",
		Scope:          []string{"SCOPE_A"},
		RequestedTTL:   time.Minute,
		SessionType:    token.SessionInteractive,
		RequestID:      "req-long",
		CommandPreview: long,
	}
	msg := renderApproval(req, "uuid")
	body := msg.Embeds[0].Description
	assert.Contains(t, body, "…[truncated]")
}
