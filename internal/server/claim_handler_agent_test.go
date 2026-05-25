package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/transport/sign"
)

// agentContextOpts captures the five PR 4 agent-context fields for
// signedClaimBodyWithAgent.
type agentContextOpts struct {
	AgentIdentity  string
	AgentModel     string
	ToolName       string
	CommandPreview string
	RecentSummary  string
}

// defaultClaimOpts is a thin alias over the existing
// defaultClaimBodyOpts helper so the agent-context tests read clean.
func defaultClaimOpts(h *claimTestHarness) claimBodyOpts {
	return defaultClaimBodyOpts(h)
}

// signedClaimBodyWithAgent builds a /claim body identical to
// signedClaimBody but with the five agent-context fields populated and
// included in the canonical signed payload (CanonicalJSON includes
// every exported field regardless of omitempty, so both sides must
// agree on the field set).
func signedClaimBodyWithAgent(t *testing.T, h *claimTestHarness, o claimBodyOpts, agent agentContextOpts) []byte {
	t.Helper()
	signKey := o.SignWithKey
	if signKey == nil {
		signKey = h.clientPriv
	}
	ts := o.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	payload := signedPayload{
		AgentIdentity:   agent.AgentIdentity,
		AgentModel:      agent.AgentModel,
		CommandPreview:  agent.CommandPreview,
		EphemeralPubKey: o.EphemeralPubKey,
		MachineName:     o.MachineName,
		Nonce:           o.Nonce,
		Reason:          o.Reason,
		RecentSummary:   agent.RecentSummary,
		RequestID:       o.RequestID,
		Scope:           o.Scope,
		SessionType:     o.SessionType,
		SupervisorName:  o.SupervisorName,
		Timestamp:       ts.Format(time.RFC3339Nano),
		ToolName:        agent.ToolName,
		TTL:             o.TTL.String(),
	}
	canonical, err := sign.CanonicalJSON(payload)
	require.NoError(t, err)
	sig, err := sign.Sign(t.Context(), signKey, canonical)
	require.NoError(t, err)

	body := map[string]any{
		"scope":                  o.Scope,
		"reason":                 o.Reason,
		"ttl":                    o.TTL.String(),
		"session_type":           o.SessionType,
		"ephemeral_pubkey":       o.EphemeralPubKey,
		"nonce":                  o.Nonce,
		"timestamp":              ts.Format(time.RFC3339Nano),
		"signature":              base64.StdEncoding.EncodeToString(sig),
		"request_id":             o.RequestID,
		"machine_name":           o.MachineName,
		"client_key_fingerprint": o.Fingerprint,
	}
	// Only include the agent fields when non-empty so omitempty
	// wire-coexistence with pre-PR-4 servers stays valid.
	if agent.AgentIdentity != "" {
		body["agent_identity"] = agent.AgentIdentity
	}
	if agent.AgentModel != "" {
		body["agent_model"] = agent.AgentModel
	}
	if agent.ToolName != "" {
		body["tool_name"] = agent.ToolName
	}
	if agent.CommandPreview != "" {
		body["command_preview"] = agent.CommandPreview
	}
	if agent.RecentSummary != "" {
		body["recent_summary"] = agent.RecentSummary
	}
	out, err := json.Marshal(body)
	require.NoError(t, err)
	return out
}

// silence unused-import linter for the test-helper-only imports when
// only one test exercises bytes/http/httptest/context.
var (
	_ = bytes.NewReader
	_ = httptest.NewRecorder
	_ = http.MethodPost
	_ = context.Background
)

// TestClaim_AcceptsOptionalAgentFields proves the new claim fields
// flow through the full claim pipeline (shape → sig → approver →
// audit) without breaking the existing happy path.
func TestClaim_AcceptsOptionalAgentFields(t *testing.T) {
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: 5 * time.Minute, ApproverID: "op-1"}},
			[]error{nil},
		),
	)
	opts := defaultClaimOpts(h)
	body := signedClaimBodyWithAgent(t, h, opts, agentContextOpts{
		AgentIdentity:  "claude-code/1.2.3",
		AgentModel:     "claude-opus-4-7",
		ToolName:       "Bash",
		CommandPreview: "git push origin master",
		RecentSummary:  "Refactoring auth module",
	})
	rr, _ := h.do(t, body)
	require.Equal(t, 200, rr.Code, "body=%s", rr.Body.String())

	// The approver should have seen the agent context fields verbatim.
	require.Len(t, h.approver.calls, 1)
	got := h.approver.calls[0]
	assert.Equal(t, "claude-code/1.2.3", got.AgentIdentity)
	assert.Equal(t, "claude-opus-4-7", got.AgentModel)
	assert.Equal(t, "Bash", got.ToolName)
	assert.Equal(t, "git push origin master", got.CommandPreview)
	assert.Equal(t, "Refactoring auth module", got.RecentSummary)
}

// TestClaim_NoAgentFields_StillWorks proves backward compat — a
// client that omits all agent fields still gets a 200.
func TestClaim_NoAgentFields_StillWorks(t *testing.T) {
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: 5 * time.Minute, ApproverID: "op-1"}},
			[]error{nil},
		),
	)
	body := signedClaimBody(t, h, defaultClaimOpts(h))
	rr, _ := h.do(t, body)
	require.Equal(t, 200, rr.Code, "body=%s", rr.Body.String())
	require.Len(t, h.approver.calls, 1)
	assert.Empty(t, h.approver.calls[0].AgentIdentity)
	assert.Empty(t, h.approver.calls[0].CommandPreview)
}

// TestClaim_RejectsOversizedAgentField — the server must reject any
// agent context field that exceeds its documented length cap.
func TestClaim_RejectsOversizedAgentField(t *testing.T) {
	cases := []struct {
		name     string
		setField func(*agentContextOpts)
	}{
		{"agent_identity > 128", func(o *agentContextOpts) { o.AgentIdentity = strings.Repeat("x", 129) }},
		{"agent_model > 64", func(o *agentContextOpts) { o.AgentModel = strings.Repeat("x", 65) }},
		{"tool_name > 64", func(o *agentContextOpts) { o.ToolName = strings.Repeat("x", 65) }},
		{"command_preview > 1024", func(o *agentContextOpts) { o.CommandPreview = strings.Repeat("x", 1025) }},
		{"recent_summary > 256", func(o *agentContextOpts) { o.RecentSummary = strings.Repeat("x", 257) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newClaimHarness(t)
			agent := agentContextOpts{}
			c.setField(&agent)
			body := signedClaimBodyWithAgent(t, h, defaultClaimOpts(h), agent)
			rr, _ := h.do(t, body)
			assert.Equal(t, 400, rr.Code, "body=%s", rr.Body.String())
			assert.Contains(t, rr.Body.String(), errCodeBadRequest)
		})
	}
}

// TestClaim_ServerRedactsCommandPreview — even if a client sends a raw
// secret-looking string, the server re-runs redaction before passing
// it to the approver / audit log.
func TestClaim_ServerRedactsCommandPreview(t *testing.T) {
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: 5 * time.Minute, ApproverID: "op-1"}},
			[]error{nil},
		),
	)
	body := signedClaimBodyWithAgent(t, h, defaultClaimOpts(h), agentContextOpts{
		// Client deliberately did not redact:
		CommandPreview: `curl -H "x-api-key: sk-ant-api03-AAAAAAAAAAAAAAAAAAAA-bbb_ccc" https://api.anthropic.com`,
	})
	rr, _ := h.do(t, body)
	require.Equal(t, 200, rr.Code, "body=%s", rr.Body.String())
	require.Len(t, h.approver.calls, 1)
	got := h.approver.calls[0].CommandPreview
	assert.NotContains(t, got, "sk-ant-api03")
	assert.Contains(t, got, "[redacted:")
}
