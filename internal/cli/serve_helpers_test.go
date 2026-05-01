package cli

import (
	"context"
	"log/slog"
	"time"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/server"
)

// testApproverFunc is a server.Approver implementation used by serve
// unit tests. Returns Approved=true for every call.
type testApproverFunc func(ctx context.Context, req server.ApprovalRequest) (server.Decision, error)

func (f testApproverFunc) RequestApproval(ctx context.Context, req server.ApprovalRequest) (server.Decision, error) {
	return f(ctx, req)
}

// testApproverFactory is a serveDeps.approverFactory that returns a
// canned-Approve approver and a "always connected" probe.
func testApproverFactory(_ context.Context, _ *config.Server, _ *slog.Logger) (server.Approver, func() bool, error) {
	approver := testApproverFunc(func(_ context.Context, req server.ApprovalRequest) (server.Decision, error) {
		return server.Decision{Approved: true, ApprovedAt: time.Now(), GrantedTTL: req.RequestedTTL, ApproverID: "stub"}, nil
	})
	return approver, func() bool { return true }, nil
}
