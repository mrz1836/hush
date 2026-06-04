package supervise

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/audit"
)

func setLifecycleStateForRenewTest(t *testing.T, tl *testLifecycle, state State) {
	t.Helper()
	ctx := context.Background()
	switch state {
	case StateFetching:
		return
	case StateRunning:
		require.NoError(t, tl.lc.store.Transition(ctx, EventFetchOK))
	case StateGraceRestart:
		require.NoError(t, tl.lc.store.Transition(ctx, EventFetchOK))
		require.NoError(t, tl.lc.store.Transition(ctx, EventGraceRestartTriggered))
	case StateAwaitingApproval:
		require.NoError(t, tl.lc.store.Transition(ctx, EventFetchAuthRequired))
	case StateStopped:
		require.NoError(t, tl.lc.store.Transition(ctx, EventStopRequested))
	case StateSwapping:
		require.NoError(t, tl.lc.store.Transition(ctx, EventFetchOK))
		require.NoError(t, tl.lc.store.Transition(ctx, EventReloadRequested))
	default:
		t.Fatalf("unsupported renew test state %q", state)
	}
}

func dispatchRenewForTest(t *testing.T, tl *testLifecycle, req RenewRequest) (RenewResult, error) {
	t.Helper()
	verb := renewVerb{req: req, ack: make(chan renewResult, 1)}
	tl.lc.dispatchRenewVerb(context.Background(), verb)
	select {
	case got := <-verb.ack:
		return got.res, got.err
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchRenewVerb did not ack")
		return RenewResult{}, nil
	}
}

func currentChildPID(t *testing.T, tl *testLifecycle) int {
	t.Helper()
	tl.lc.childMu.Lock()
	defer tl.lc.childMu.Unlock()
	if tl.lc.child == nil {
		return 0
	}
	return tl.lc.child.PID()
}

func TestLifecycleRenew_StateGateMatrix(t *testing.T) {
	for _, state := range []State{StateRunning, StateGraceRestart, StateAwaitingApproval} {
		t.Run(string(state)+"-allowed", func(t *testing.T) {
			tl := newTestLifecycle(t, longChildCmd())
			setLifecycleStateForRenewTest(t, tl, state)
			tl.vault.QueueOK()

			res, err := dispatchRenewForTest(t, tl, RenewRequest{})
			require.NoError(t, err)
			assert.Equal(t, RenewOutcomeRenewed, res.Outcome)
			assert.Equal(t, 1, tl.vault.ClaimCount(), "renew must request a fresh claim")
			assert.True(t, tl.auditLog.Has(audit.ActionClientRenewInvoked))
		})
	}

	for _, state := range []State{StateFetching, StateStopped, StateSwapping} {
		t.Run(string(state)+"-refused", func(t *testing.T) {
			tl := newTestLifecycle(t, longChildCmd())
			setLifecycleStateForRenewTest(t, tl, state)

			res, err := dispatchRenewForTest(t, tl, RenewRequest{})
			require.Error(t, err)
			assert.Equal(t, RenewOutcomeRefusedState, res.Outcome)
			var stateErr *rejectStateError
			assert.True(t, errors.As(err, &stateErr), "got %v", err)
			assert.Equal(t, string(state), stateErr.state)
			assert.Equal(t, 0, tl.vault.ClaimCount(), "refused renew must not request a claim")
			assert.True(t, tl.auditLog.Has(audit.ActionClientRenewInvoked))
		})
	}
}

func TestLifecycleRenew_SeamlessSwapsSessionWithoutRestart(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	cancel, done := runUntilRunning(t, tl)
	defer shutdownLifecycle(t, cancel, done)

	originalPID := currentChildPID(t, tl)
	exp := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	tl.vault.QueueClaim(claimOutcome{
		status: http.StatusOK,
		jwt:    "renew-jwt-seamless",
		jti:    "renew-jti-seamless",
		exp:    exp,
	})

	res, err := dispatchRenewForTest(t, tl, RenewRequest{})
	require.NoError(t, err)
	assert.Equal(t, RenewOutcomeRenewed, res.Outcome)
	assert.False(t, res.Restarted)
	assert.Equal(t, exp, res.SessionExpiresAt)
	assert.Equal(t, "renew-jti-seamless", res.JTI)
	assert.Equal(t, originalPID, currentChildPID(t, tl), "seamless renew must leave child running")
	assert.Equal(t, 2, tl.vault.ClaimCount(), "boot claim + renew claim")
	assert.True(t, tl.auditLog.Has(audit.ActionSupervisorSessionRefreshed))
	assert.True(t, tl.auditLog.Has(audit.ActionClientRenewInvoked))

	matches := auditEventsByAction(tl.auditLog, audit.ActionClientRenewInvoked)
	require.NotEmpty(t, matches)
	last := matches[len(matches)-1]
	assert.Equal(t, "renewed", last.data["outcome"])
	assert.Equal(t, false, last.data["restarted"])
}

func TestLifecycleRenew_RestartRefillsAndRestartsChild(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	cancel, done := runUntilRunning(t, tl)
	defer shutdownLifecycle(t, cancel, done)

	originalPID := currentChildPID(t, tl)
	tl.vault.QueueOK()

	res, err := dispatchRenewForTest(t, tl, RenewRequest{Restart: true})
	require.NoError(t, err)
	assert.Equal(t, RenewOutcomeRenewed, res.Outcome)
	assert.True(t, res.Restarted)

	eventually(t, "child restarted after renew", 5*time.Second, func() bool {
		pid := currentChildPID(t, tl)
		return pid > 0 && pid != originalPID && snapshotState(tl) == StateRunning
	})
	assert.True(t, tl.auditLog.Has(audit.ActionSupervisorSilentRefill))
}

func TestLifecycleRenew_Outcomes(t *testing.T) {
	cases := []struct {
		name    string
		queue   func(*mockVault)
		outcome string
	}{
		{
			name: "denied",
			queue: func(v *mockVault) {
				v.QueueDenied()
			},
			outcome: RenewOutcomeDenied,
		},
		{
			name: "timeout",
			queue: func(v *mockVault) {
				v.QueueClaim(claimOutcome{
					status: http.StatusRequestTimeout,
					body:   `{"error":"approval_timeout","request_id":"test"}`,
				})
			},
			outcome: RenewOutcomeTimeout,
		},
		{
			name: "transport-error",
			queue: func(v *mockVault) {
				v.srv.Close()
			},
			outcome: RenewOutcomeError,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tl := newTestLifecycle(t, longChildCmd())
			setLifecycleStateForRenewTest(t, tl, StateRunning)
			c.queue(tl.vault)

			res, err := dispatchRenewForTest(t, tl, RenewRequest{})
			require.Error(t, err)
			assert.Equal(t, c.outcome, res.Outcome)
			assert.True(t, tl.auditLog.Has(audit.ActionClientRenewInvoked))
		})
	}
}

func TestLifecycleRenew_SingleFlightRefusesSecondRenew(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	setLifecycleStateForRenewTest(t, tl, StateRunning)
	tl.lc.renewInFlight.Store(true)
	defer tl.lc.renewInFlight.Store(false)

	res, err := dispatchRenewForTest(t, tl, RenewRequest{})
	require.Error(t, err)
	assert.Equal(t, RenewOutcomeRefusedState, res.Outcome)
	assert.Contains(t, err.Error(), "renew already in flight")
	assert.Equal(t, 0, tl.vault.ClaimCount())
	assert.True(t, tl.auditLog.Has(audit.ActionClientRenewInvoked))
}
