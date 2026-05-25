package server

// P2 tests: supervisor_name shape validation + propagation to the
// Approver. The wire-format field is the source of truth for the
// per-supervisor rate-limit bucket and for the operator's Discord
// prompt rendering, so the server must (a) reject malformed combinations
// at the shape stage and (b) pass the value through unchanged on the
// happy path.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestClaim_SupervisorMissingName_400 — a session_type=supervisor claim
// with no supervisor_name violates the new contract and must be
// rejected with bad_request. The approver MUST NOT be invoked.
func TestClaim_SupervisorMissingName_400(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(t)
	o := defaultClaimBodyOpts(h)
	o.SessionType = "supervisor"
	o.TTL = 4 * time.Hour
	// SupervisorName intentionally empty.
	rr, _ := h.do(t, signedClaimBody(t, h, o))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorBodyShape(t, rr, "bad_request")
	if len(h.approver.calls) != 0 {
		t.Fatalf("approver called %d times; want 0", len(h.approver.calls))
	}
	events := h.auditEvents()
	if len(events) != 1 || events[0].Detail["outcome"] != "bad-request" {
		t.Fatalf("audit events=%v; want one bad-request", events)
	}
}

// TestClaim_InteractiveWithSupervisorName_400 — an interactive claim
// must NOT carry supervisor_name. The server rejects to enforce the
// session_type ↔ supervisor_name binding.
func TestClaim_InteractiveWithSupervisorName_400(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(t)
	o := defaultClaimBodyOpts(h)
	o.SessionType = "interactive"
	o.SupervisorName = "should-not-be-here"
	rr, _ := h.do(t, signedClaimBody(t, h, o))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
	assertErrorBodyShape(t, rr, "bad_request")
	if len(h.approver.calls) != 0 {
		t.Fatalf("approver called %d times; want 0", len(h.approver.calls))
	}
}

// TestClaim_SupervisorNameInvalidShape_400 — non-empty but malformed
// supervisor_name (e.g. whitespace, illegal characters, over-length)
// must be rejected with bad_request.
func TestClaim_SupervisorNameInvalidShape_400(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{"contains_space", "claude worker"},
		{"contains_slash", "claude/worker"},
		{"too_long", strings.Repeat("a", 65)},
		{"empty_after_quote", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newClaimHarness(t)
			o := defaultClaimBodyOpts(h)
			o.SessionType = "supervisor"
			o.SupervisorName = tc.in
			rr, _ := h.do(t, signedClaimBody(t, h, o))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
			}
			if len(h.approver.calls) != 0 {
				t.Fatalf("approver invoked for shape-rejected supervisor_name=%q", tc.in)
			}
		})
	}
}

// TestClaim_SupervisorNamePropagatesToApprover — a valid
// session_type=supervisor + supervisor_name claim must reach the
// Approver with the SupervisorName field populated. This is the
// per-supervisor rate-limit isolation key in production.
func TestClaim_SupervisorNamePropagatesToApprover(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: 4 * time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
	)
	o := defaultClaimBodyOpts(h)
	o.SessionType = "supervisor"
	o.SupervisorName = "claude-worker-7"
	o.TTL = 4 * time.Hour
	rr, _ := h.do(t, signedClaimBody(t, h, o))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if len(h.approver.calls) != 1 {
		t.Fatalf("approver calls=%d want 1", len(h.approver.calls))
	}
	if got := h.approver.calls[0].SupervisorName; got != "claude-worker-7" {
		t.Fatalf("approver received SupervisorName=%q; want %q", got, "claude-worker-7")
	}
}

// TestClaim_TwoSupervisorsOnSameIP_BothReachApprover — the operator's
// motivating scenario for P2: two distinct supervisors on the same
// Tailscale host must both reach the approver (the chassis itself does
// not gate per-supervisor; the discord rate-limit bucket is the one that
// keys on (SupervisorName, ClientIP)). This test pins the chassis side
// of that contract: SupervisorName flows through unchanged for both,
// the approver sees both calls, and the wire field is the differentiator.
func TestClaim_TwoSupervisorsOnSameIP_BothReachApprover(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{
				{Approved: true, GrantedTTL: 4 * time.Hour, ApproverID: "test"},
				{Approved: true, GrantedTTL: 4 * time.Hour, ApproverID: "test"},
			},
			[]error{nil, nil},
		),
	)

	o1 := defaultClaimBodyOpts(h)
	o1.SessionType = "supervisor"
	o1.SupervisorName = "supA"
	o1.TTL = 4 * time.Hour
	rr1, _ := h.do(t, signedClaimBody(t, h, o1))
	if rr1.Code != http.StatusOK {
		t.Fatalf("supA status=%d body=%s", rr1.Code, rr1.Body.String())
	}

	o2 := defaultClaimBodyOpts(h)
	o2.SessionType = "supervisor"
	o2.SupervisorName = "supB"
	o2.TTL = 4 * time.Hour
	o2.Scope = []string{"GEMINI_API_KEY"}
	o2.Nonce = freshNonce()
	rr2, _ := h.do(t, signedClaimBody(t, h, o2))
	if rr2.Code != http.StatusOK {
		t.Fatalf("supB status=%d body=%s", rr2.Code, rr2.Body.String())
	}

	if len(h.approver.calls) != 2 {
		t.Fatalf("approver calls=%d want 2", len(h.approver.calls))
	}
	if got := h.approver.calls[0].SupervisorName; got != "supA" {
		t.Errorf("call[0].SupervisorName=%q want supA", got)
	}
	if got := h.approver.calls[1].SupervisorName; got != "supB" {
		t.Errorf("call[1].SupervisorName=%q want supB", got)
	}
}

// TestClaim_SupervisorNameInAuditDetail_NotLeaked — supervisor_name is
// a stable label (no secret), so it's safe to include in the audit
// detail. This test pins that the audit detail emitted by the chassis
// continues to comply with the Constitution X allow-list (outcome,
// session_type, scope, granted_ttl, jti) and does NOT accidentally
// echo the supervisor_name into reasons / forbidden fields. We don't
// emit supervisor_name in the audit detail yet — keep that surface
// minimal until the operator asks for it.
func TestClaim_SupervisorNameInAuditDetail_NotLeaked(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(
		t,
		withApproverScript(
			[]Decision{{Approved: true, GrantedTTL: 4 * time.Hour, ApproverID: "test"}},
			[]error{nil},
		),
	)
	o := defaultClaimBodyOpts(h)
	o.SessionType = "supervisor"
	o.SupervisorName = "supA"
	o.TTL = 4 * time.Hour
	if rr, _ := h.do(t, signedClaimBody(t, h, o)); rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	events := h.auditEvents()
	if len(events) != 1 {
		t.Fatalf("audit events=%d want 1", len(events))
	}
	for k, v := range events[0].Detail {
		switch k {
		case "outcome", "session_type", "scope", "granted_ttl", "jti":
			// allow-listed
		default:
			t.Errorf("unexpected audit detail key %q=%q", k, v)
		}
	}
}

// TestClaim_SignatureCoversSupervisorName — the supervisor_name is part
// of the signed payload. Mutating it on the wire after signing yields
// bad_signature, proving the canonical bytes include the field.
func TestClaim_SignatureCoversSupervisorName(t *testing.T) {
	t.Parallel()
	h := newClaimHarness(t)
	o := defaultClaimBodyOpts(h)
	o.SessionType = "supervisor"
	o.SupervisorName = "supA"
	o.TTL = 4 * time.Hour
	body := signedClaimBody(t, h, o)

	// Decode, mutate supervisor_name, re-encode (keeping the original
	// signature). Server must reject as bad_signature.
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	m["supervisor_name"] = "attacker-tampered"
	tampered, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	rr, _ := h.do(t, tampered)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("tampered status=%d want 403", rr.Code)
	}
	assertErrorBodyShape(t, rr, "bad_signature")
}
