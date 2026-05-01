package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/token"
)

func interactiveSampleRequest() ApprovalRequest {
	return ApprovalRequest{
		MachineName:  "darwin-laptop",
		ClientIP:     "100.96.10.4",
		Reason:       "ad-hoc shell test",
		Scope:        []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		RequestedTTL: 90 * time.Minute,
		SessionType:  token.SessionInteractive,
	}
}

func daemonSampleRequest() ApprovalRequest {
	r := interactiveSampleRequest()
	r.SessionType = token.SessionSupervisor
	r.SupervisorName = "claude-worker-1"
	return r
}

func renderedDescription(t *testing.T, req ApprovalRequest, customID string) string {
	t.Helper()
	msg := renderApproval(req, customID)
	if msg == nil {
		t.Fatal("renderApproval returned nil")
	}
	if len(msg.Embeds) == 0 {
		t.Fatal("rendered message has no embed")
	}
	return msg.Embeds[0].Description
}

func TestApprovalRender_InteractiveLabel(t *testing.T) {
	t.Parallel()
	desc := renderedDescription(t, interactiveSampleRequest(), "uuid-1")
	if !strings.HasPrefix(desc, headerInteractive) {
		t.Errorf("interactive description does not start with %q; got prefix %q",
			headerInteractive, desc[:min(len(desc), len(headerInteractive)+8)])
	}
	if !strings.Contains(desc, "✅") {
		t.Error("expected green checkmark glyph in interactive header")
	}
}

func TestApprovalRender_DaemonLabel(t *testing.T) {
	t.Parallel()
	desc := renderedDescription(t, daemonSampleRequest(), "uuid-2")
	if !strings.HasPrefix(desc, headerDaemon) {
		t.Errorf("daemon description does not start with %q", headerDaemon)
	}
	if !strings.Contains(desc, "[DAEMON]") {
		t.Error("expected [DAEMON] marker in daemon header")
	}
	if !strings.Contains(desc, "⚠") {
		t.Error("expected warning glyph in daemon header")
	}
}

func TestApprovalRender_DaemonIncludesSupervisorName(t *testing.T) {
	t.Parallel()
	req := daemonSampleRequest()
	desc := renderedDescription(t, req, "uuid-3")
	machineIdx := strings.Index(desc, "Machine:")
	supIdx := strings.Index(desc, "Supervisor:")
	if machineIdx < 0 || supIdx < 0 {
		t.Fatalf("missing Machine or Supervisor line: %q", desc)
	}
	if supIdx < machineIdx {
		t.Errorf("Supervisor line must appear after Machine line; machineIdx=%d supIdx=%d",
			machineIdx, supIdx)
	}
	if !strings.Contains(desc, req.SupervisorName) {
		t.Errorf("daemon description missing supervisor name %q", req.SupervisorName)
	}
}

func TestApprovalRender_VisuallyDistinctFromInteractive(t *testing.T) {
	t.Parallel()
	common := ApprovalRequest{
		MachineName:  "node-7",
		ClientIP:     "100.64.0.10",
		Reason:       "shared",
		Scope:        []string{"X"},
		RequestedTTL: time.Hour,
	}
	interactive := common
	interactive.SessionType = token.SessionInteractive
	daemon := common
	daemon.SessionType = token.SessionSupervisor
	daemon.SupervisorName = "supA"

	im := renderApproval(interactive, "u1")
	dm := renderApproval(daemon, "u2")
	if im.Embeds[0].Color == dm.Embeds[0].Color {
		t.Errorf("interactive and daemon share embed color 0x%x", im.Embeds[0].Color)
	}
	if im.Embeds[0].Color != colorInteractive {
		t.Errorf("interactive color %v; want %v", im.Embeds[0].Color, colorInteractive)
	}
	if dm.Embeds[0].Color != colorDaemon {
		t.Errorf("daemon color %v; want %v", dm.Embeds[0].Color, colorDaemon)
	}
	if strings.HasPrefix(im.Embeds[0].Description, dm.Embeds[0].Description[:5]) {
		t.Error("expected first 5 bytes of headers to differ between interactive and daemon")
	}
}

func TestApprovalRender_AllRequestFieldsPresent(t *testing.T) {
	t.Parallel()
	req := interactiveSampleRequest()
	desc := renderedDescription(t, req, "uuid-4")
	mustContain := []string{
		req.MachineName,
		req.ClientIP,
		req.Reason,
		"ANTHROPIC_API_KEY, OPENAI_API_KEY",
		req.RequestedTTL.String(),
	}
	for _, want := range mustContain {
		if !strings.Contains(desc, want) {
			t.Errorf("rendered description missing %q\n%s", want, desc)
		}
	}
}

func TestApprovalRender_NeverIncludesToken(t *testing.T) {
	t.Parallel()
	req := ApprovalRequest{
		MachineName:    "node",
		ClientIP:       "100.64.0.1",
		Reason:         "test",
		Scope:          []string{"X"},
		RequestedTTL:   time.Hour,
		SupervisorName: "supX",
	}
	sentinel := "SECRET_SHOULD_NEVER_APPEAR_11_RENDER"
	for _, sessionType := range []token.SessionType{token.SessionInteractive, token.SessionSupervisor} {
		req.SessionType = sessionType
		ms := renderApproval(req, "uuid")
		body := ms.Embeds[0].Description
		if strings.Contains(body, sentinel) {
			t.Errorf("sentinel leaked into rendered body for session_type=%s: %s", sessionType, body)
		}
	}
	for _, eventType := range []auditEventType{auditRequestReceived, auditApproved, auditDenied, auditTimedOut, auditRateLimited} {
		ms := renderAudit(eventType, req)
		if strings.Contains(ms.Embeds[0].Description, sentinel) {
			t.Errorf("sentinel leaked into audit payload for %s", eventType)
		}
	}
}
