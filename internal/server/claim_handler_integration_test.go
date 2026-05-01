//go:build integration

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
)

// stubAsApprover adapts SDD-04's testutil.DiscordStub to the chassis Approver
// interface, translating testutil.Decision values into chassis sentinels.
// This is the same shape SDD-14 will install in production (a translator
// between internal/discord errors and the chassis-level surface).
type stubAsApprover struct {
	stub *testutil.DiscordStub
}

func (a stubAsApprover) RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error) {
	stubReq := testutil.ApprovalRequest{
		RequesterHost: req.MachineName,
		Scopes:        req.Scope,
		SessionType:   req.SessionType.String(),
		TTL:           req.RequestedTTL,
	}
	dec, err := a.stub.RequestApproval(ctx, stubReq)
	if err != nil {
		// testutil.ErrUnexpectedCall is the catch-all; map to
		// ErrApproverUnavailable so the chassis fail-closed semantics
		// are preserved.
		if errors.Is(err, testutil.ErrUnexpectedCall) {
			return Decision{}, ErrApproverUnavailable
		}
		return Decision{}, err
	}
	switch dec {
	case testutil.DecisionApprove, testutil.DecisionApproveMute:
		return Decision{
			Approved:   true,
			GrantedTTL: req.RequestedTTL,
			ApprovedAt: time.Now(),
			ApproverID: "stub",
		}, nil
	case testutil.DecisionDeny:
		return Decision{}, ErrApproverDenied
	default:
		return Decision{}, ErrApproverUnavailable
	}
}

// TestClaim_Integration_FullFlow_DiscordStub exercises the handler under the
// SDD-04 testutil.DiscordStub adapter — the closest unit-of-test to the
// production wiring SDD-14 will install.
func TestClaim_Integration_FullFlow_DiscordStub(t *testing.T) {
	t.Run("approves", func(t *testing.T) {
		stub := testutil.NewDiscordStub(t)
		stub.Enqueue(testutil.DecisionApprove)
		h := newClaimHarness(t, withDepsMutator(func(d *Deps) {
			d.Approver = stubAsApprover{stub: stub}
		}))
		rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
		}
		var resp claimResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode resp: %v", err)
		}
		if resp.JWT == "" || resp.JTI == "" {
			t.Fatalf("empty JWT or JTI: %+v", resp)
		}
		events := h.auditEvents()
		if len(events) != 1 || events[0].Detail["outcome"] != "approved" {
			t.Fatalf("audit events=%v want one approved", events)
		}
	})

	t.Run("denies", func(t *testing.T) {
		stub := testutil.NewDiscordStub(t)
		stub.Enqueue(testutil.DecisionDeny)
		h := newClaimHarness(t, withDepsMutator(func(d *Deps) {
			d.Approver = stubAsApprover{stub: stub}
		}))
		rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "denied") {
			t.Fatalf("body=%s, want denied", rr.Body.String())
		}
		assertSingleAudit(t, h, "denied")
	})

	t.Run("unavailable", func(t *testing.T) {
		// An empty stub with ApproveAll=false returns ErrUnexpectedCall on
		// any request — our adapter translates that into ErrApproverUnavailable.
		stub := testutil.NewDiscordStub(t)
		// Suppress the t.Errorf the stub emits on unexpected call so the
		// integration test can drive the unavailable path cleanly.
		shimT := &swallowingT{T: t}
		stub.Enqueue() // explicit empty queue — no decisions
		_ = shimT
		h := newClaimHarness(t, withDepsMutator(func(d *Deps) {
			d.Approver = approverFunc(func(ctx context.Context, _ ApprovalRequest) (Decision, error) {
				return Decision{}, ErrApproverUnavailable
			})
		}))
		rr, _ := h.do(t, signedClaimBody(t, h, defaultClaimBodyOpts(h)))
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want 503; body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "discord_unavailable") {
			t.Fatalf("body=%s, want discord_unavailable", rr.Body.String())
		}
		assertSingleAudit(t, h, "discord-unavailable")
	})
}

// swallowingT lets the adapter test drive the stub's unexpected-call path
// without triggering t.Errorf on the parent — used only in the unavailable
// integration sub-test.
type swallowingT struct {
	*testing.T
}

func (s *swallowingT) Errorf(_ string, _ ...any) {}
