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

// TestApprovalRender_IncludesRequestID — P3: the chassis-assigned
// request_id must appear in the rendered prompt, the resolved
// approval message, and the audit-channel mirror so operators can
// cross-reference Discord-visible events with the hash-chained on-disk
// audit entry (Layer 6). Covers interactive, daemon, and audit shapes.
func TestApprovalRender_IncludesRequestID(t *testing.T) {
	t.Parallel()
	const reqID = "rq-correlate-abc"

	interactive := interactiveSampleRequest()
	interactive.RequestID = reqID
	if desc := renderedDescription(t, interactive, "uuid"); !strings.Contains(desc, reqID) {
		t.Errorf("interactive prompt missing request_id %q:\n%s", reqID, desc)
	}

	daemon := daemonSampleRequest()
	daemon.RequestID = reqID
	if desc := renderedDescription(t, daemon, "uuid"); !strings.Contains(desc, reqID) {
		t.Errorf("daemon prompt missing request_id %q:\n%s", reqID, desc)
	}

	for _, et := range []auditEventType{auditRequestReceived, auditApproved, auditDenied, auditTimedOut, auditRateLimited} {
		am := renderAudit(et, daemon)
		if len(am.Embeds) == 0 {
			t.Fatalf("renderAudit(%s) produced no embed", et)
		}
		if !strings.Contains(am.Embeds[0].Description, reqID) {
			t.Errorf("audit mirror (%s) missing request_id %q:\n%s", et, reqID, am.Embeds[0].Description)
		}
	}

	// Resolved-approval update also carries the request_id.
	resolved := renderResolvedApproval(daemon, true)
	if len(resolved.Embeds) == 0 || !strings.Contains(resolved.Embeds[0].Description, reqID) {
		t.Errorf("resolved approval missing request_id %q", reqID)
	}
}

// TestApprovalRender_OmitsRequestIDWhenEmpty — backward compat: an
// ApprovalRequest with RequestID="" must NOT render a stub "Request: "
// line (which would be cosmetically wrong and could appear in audit
// records as a stale label). Use the absence of "Request:" as the
// invariant; this guards against accidental always-on rendering.
func TestApprovalRender_OmitsRequestIDWhenEmpty(t *testing.T) {
	t.Parallel()
	req := interactiveSampleRequest()
	req.RequestID = ""
	desc := renderedDescription(t, req, "uuid")
	if strings.Contains(desc, "Request:") {
		t.Errorf("interactive prompt should not render 'Request:' for empty RequestID:\n%s", desc)
	}

	// Audit mirror — same rule.
	am := renderAudit(auditRequestReceived, req)
	if strings.Contains(am.Embeds[0].Description, "Request:") {
		t.Errorf("audit mirror should not render 'Request:' for empty RequestID:\n%s", am.Embeds[0].Description)
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
