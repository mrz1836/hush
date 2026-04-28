package testutil

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Decision represents the outcome of a Discord approval request.
type Decision int

const (
	DecisionApprove     Decision = iota // request approved
	DecisionDeny                        // request denied
	DecisionApproveMute                 // approved and alert muted for the session TTL
)

// ApprovalRequest carries the parameters of a single approval call.
type ApprovalRequest struct {
	RequesterHost string
	Scopes        []string
	SessionType   string
	TTL           time.Duration
	MaxUses       int
}

// LimitDescription returns a human-readable summary of the TTL + MaxUses limit.
func (r ApprovalRequest) LimitDescription() string {
	if r.MaxUses > 0 {
		return fmt.Sprintf("%s, %d uses", r.TTL, r.MaxUses)
	}
	return fmt.Sprintf("%s, TTL-only", r.TTL)
}

// ApprovalCall records a single call to RequestApproval.
type ApprovalCall struct {
	Request  ApprovalRequest
	Decision Decision
	Err      error
	Index    int
}

// Approver is the narrow interface DiscordStub satisfies.
// SDD-11 will define the production Approver in internal/discord.
type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// ErrUnexpectedCall is returned by RequestApproval when the queue is empty and
// ApproveAll is false. Compare via errors.Is(err, ErrUnexpectedCall).
var ErrUnexpectedCall = errors.New("hush/testutil: unexpected approval call")

// DiscordStub is a programmable, network-free substitute for the production
// Discord approval flow. Use NewDiscordStub to construct one.
type DiscordStub struct {
	// ApproveAll is the tail-default: when the queue is exhausted and ApproveAll
	// is true, RequestApproval returns DecisionApprove. When false and the queue
	// is empty, it calls t.Errorf and returns ErrUnexpectedCall.
	ApproveAll bool

	mu        sync.Mutex
	responses []Decision
	calls     []ApprovalCall
	t         *testing.T
}

// NewDiscordStub constructs a DiscordStub bound to t and registers a t.Cleanup
// callback that drains the recorded calls and response queue.
func NewDiscordStub(t *testing.T) *DiscordStub {
	t.Helper()
	s := &DiscordStub{t: t}
	t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.responses = nil
		s.calls = nil
	})
	return s
}

// Enqueue adds decisions to the FIFO response queue. Multiple calls are additive.
func (s *DiscordStub) Enqueue(decisions ...Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = append(s.responses, decisions...)
}

// Calls returns a defensive copy of the recorded call list.
func (s *DiscordStub) Calls() []ApprovalCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ApprovalCall(nil), s.calls...)
}

// RequestApproval implements Approver. Decision order:
//  1. If the response queue is non-empty, pop and return the head.
//  2. If ApproveAll is true, return DecisionApprove.
//  3. Otherwise call t.Errorf and return (DecisionDeny, ErrUnexpectedCall).
func (s *DiscordStub) RequestApproval(_ context.Context, req ApprovalRequest) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := len(s.calls)

	if len(s.responses) > 0 {
		d := s.responses[0]
		s.responses = s.responses[1:]
		s.calls = append(s.calls, ApprovalCall{Request: req, Decision: d, Err: nil, Index: idx})
		return d, nil
	}

	if s.ApproveAll {
		s.calls = append(s.calls, ApprovalCall{Request: req, Decision: DecisionApprove, Err: nil, Index: idx})
		return DecisionApprove, nil
	}

	err := ErrUnexpectedCall
	s.t.Errorf(
		"hush/testutil: unexpected Discord approval call: host=%q scopes=%v session=%q limit=%s",
		req.RequesterHost, req.Scopes, req.SessionType, req.LimitDescription(),
	)
	s.calls = append(s.calls, ApprovalCall{Request: req, Decision: DecisionDeny, Err: err, Index: idx})
	return DecisionDeny, err
}
