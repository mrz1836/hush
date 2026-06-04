// Supervisor orchestration glue: refresh path.
//
// lifecycle_refresh.go owns the claimRefreshLoop goroutine (consumes
// refreshTickCh and submits a fresh signed /claim), the refresh-result
// dispatch arm of mainLoop (Store.setToken atomic swap, child PID unchanged),
// and the state-conditional status-socket refresh-verb dispatcher.

package supervise

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Refresh-path sentinels for err113 compliance.
var (
	errRefreshDenied  = errors.New("supervise: refresh denied")
	errRefreshTimeout = errors.New("supervise: refresh timeout")
	errRefreshStatus  = errors.New("supervise: refresh non-OK status")
)

// rejectStateError is the typed ack returned by dispatchRefreshVerb on
// pre-running states (boot-retry / fetching / stopped). Carries the state
// name so the status-socket handler can serialize it as {"ok":false,
// "error":"<state>"}.
type rejectStateError struct {
	state string
}

// Error returns the state name.
func (e *rejectStateError) Error() string { return e.state }

// errRenewPanic is returned to renew callers when the asynchronous renew
// worker recovers from a panic. This keeps the socket caller from hanging.
var errRenewPanic = errors.New("supervise: renew worker panic")

// claimRefreshLoop is owned by Lifecycle.wg; consumes refreshTickCh and
// performs each refresh /claim swap. Posts the outcome on refreshDoneCh
// so mainLoop swaps the Store JWT atomically.
func (l *Lifecycle) claimRefreshLoop(ctx context.Context) {
	defer l.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			l.deps.Logger.Error("supervise: claimRefreshLoop panic", slog.Any("recover", r))
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.refreshTickCh:
			res := l.performRefreshClaim(ctx)
			select {
			case <-ctx.Done():
				return
			case l.refreshDoneCh <- res:
			}
		}
	}
}

// performRefreshClaim issues a fresh signed /claim and returns the outcome.
// On 200 OK the JWT is wrapped and the caller (mainLoop) calls Store.setToken.
func (l *Lifecycle) performRefreshClaim(ctx context.Context) refreshResult {
	resp, status, errBody, err := l.doClaimRequest(ctx)
	switch {
	case err != nil:
		// Network / decode failure → treat as timeout.
		return refreshResult{err: fmt.Errorf("supervise: refresh transport: %w", err)}
	case status == http.StatusOK:
		// Stash the new JWT into Store synchronously here so mainLoop's
		// dispatch arm just emits the audit event. setToken is package-
		// private — safe from within package supervise.
		sb, sbErr := securebytes.New([]byte(resp.JWT))
		if sbErr != nil {
			return refreshResult{err: fmt.Errorf("supervise: refresh jwt wrap: %w", sbErr)}
		}
		l.store.setToken(sb)
		exp, _ := time.Parse(time.RFC3339, resp.ExpiresAt)
		l.sessionMu.Lock()
		prevJTI := l.sessionJTI
		l.sessionExp = exp
		l.sessionJTI = resp.JTI
		l.sessionMu.Unlock()
		l.inputs.sessionExp.Store(&exp)
		jti := resp.JTI
		l.inputs.sessionJTI.Store(&jti)
		l.emitSessionRefreshed(ctx, resp.JTI, prevJTI, exp)
		return refreshResult{}
	case status == http.StatusForbidden:
		return refreshResult{deny: true, err: errRefreshDenied}
	case status == http.StatusRequestTimeout:
		return refreshResult{err: fmt.Errorf("%w: %s", errRefreshTimeout, errBody.Error)}
	default:
		return refreshResult{err: fmt.Errorf("%w: status=%d code=%s", errRefreshStatus, status, errBody.Error)}
	}
}

// dispatchRefreshResult is mainLoop's arm for refresh outcomes:
//   - nil err          → already swapped in performRefreshClaim
//   - deny             → AlertClassRefreshDenied; session preserved
//   - timeout          → AlertClassRefreshTimeout; session preserved
func (l *Lifecycle) dispatchRefreshResult(ctx context.Context, res refreshResult) {
	switch {
	case res.err == nil:
		// Swap already applied; child PID unchanged.
	case res.deny:
		l.deps.Alerts.Emit(ctx, AlertClassRefreshDenied, AlertPayload{
			ErrorClass: errorClassDeny,
			Reason:     alertReasonFor(AlertClassRefreshDenied),
		})
		l.emitStaleAlert(ctx, AlertClassRefreshDenied, "", errorClassDeny)
	default:
		l.deps.Alerts.Emit(ctx, AlertClassRefreshTimeout, AlertPayload{
			ErrorClass: errorClassTimeout,
			Reason:     alertReasonFor(AlertClassRefreshTimeout),
		})
		l.emitStaleAlert(ctx, AlertClassRefreshTimeout, "", errorClassTimeout)
	}
}

// handleStatusRefreshVerb is bound to StatusServer.AttachRefreshHandler.
// On every status-socket `refresh\n` verb arrival, it posts on refreshVerbCh
// and blocks on the ack channel so the status handler sees the terminal
// error.
func (l *Lifecycle) handleStatusRefreshVerb(ctx context.Context) error {
	verb := refreshVerb{ack: make(chan error, 1)}
	select {
	case l.refreshVerbCh <- verb:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-verb.ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleStatusRenewVerb is bound to StatusServer.AttachRenewHandler.
// It posts on renewVerbCh so the state gate runs inside mainLoop, then
// blocks on the renew worker's terminal ack.
func (l *Lifecycle) handleStatusRenewVerb(ctx context.Context, req RenewRequest) (RenewResult, error) {
	verb := renewVerb{req: req, ack: make(chan renewResult, 1)}
	select {
	case l.renewVerbCh <- verb:
	case <-ctx.Done():
		return RenewResult{}, ctx.Err()
	}
	select {
	case res := <-verb.ack:
		return res.res, res.err
	case <-ctx.Done():
		return RenewResult{}, ctx.Err()
	}
}

// dispatchRefreshVerb is mainLoop's arm for the status-socket refresh verb:
//   - StateAwaitingApproval → re-approve: refill + validate + restart
//   - StateRunning / StateGraceRestart → stop the child, refetch, restart it
//     (docs §13 — rotation propagation is intentional and visible)
//   - StateFetching / StateStopped → reject with state ack
//
// Both the awaiting-approval and running arms route through
// silentRefillAndRestart, which itself attempts a grace-cache restart before
// paging. Each arm first normalizes the state machine into StateFetching so
// silentRefillAndRestart's transitions are always legal.
//
//nolint:gocognit // closed-set state switch with documented per-arm side effects
func (l *Lifecycle) dispatchRefreshVerb(ctx context.Context, verb refreshVerb) {
	snap := l.store.Snapshot()
	state := snap.State
	switch state {
	case StateAwaitingApproval:
		l.transition(ctx, EventApprovalGranted)
		err := l.silentRefillAndRestart(ctx)
		outcome := "recovered"
		if err != nil {
			outcome = "failed"
		}
		l.emitClientRefreshInvoked(ctx, string(state), outcome)
		select {
		case verb.ack <- err:
		default:
		}
	case StateRunning, StateGraceRestart:
		l.stopChildForRefresh()
		l.transition(ctx, EventRefreshRequested)
		err := l.silentRefillAndRestart(ctx)
		outcome := "ok"
		if err != nil {
			outcome = "failed"
		}
		l.emitClientRefreshInvoked(ctx, string(state), outcome)
		select {
		case verb.ack <- err:
		default:
		}
	case StateFetching, StateStopped, StateSwapping:
		// Reject — preserves boot-retry / fetching natural flow and avoids
		// overlapping refreshes while a hot swap is already in flight.
		l.emitClientRefreshInvoked(ctx, string(state), "rejected")
		select {
		case verb.ack <- &rejectStateError{state: string(state)}:
		default:
		}
	default:
		select {
		case verb.ack <- &rejectStateError{state: "unknown:" + string(state)}:
		default:
		}
	}
}

// dispatchRenewVerb is mainLoop's arm for the status-socket renew verb.
// It performs only the state gate and single-flight check in mainLoop, then
// runs the potentially long human approval wait in a wg-tracked worker.
func (l *Lifecycle) dispatchRenewVerb(ctx context.Context, verb renewVerb) {
	snap := l.store.Snapshot()
	state := snap.State
	switch state {
	case StateRunning, StateGraceRestart, StateAwaitingApproval:
	case StateFetching, StateStopped, StateSwapping:
		l.emitClientRenewInvoked(ctx, string(state), "rejected", false)
		ackRenew(verb, RenewResult{Outcome: RenewOutcomeRefusedState}, &rejectStateError{state: string(state)})
		return
	default:
		l.emitClientRenewInvoked(ctx, string(state), "rejected", false)
		ackRenew(verb, RenewResult{Outcome: RenewOutcomeRefusedState}, &rejectStateError{state: "unknown:" + string(state)})
		return
	}

	if l.renewInFlight.Swap(true) {
		l.emitClientRenewInvoked(ctx, string(state), "rejected", false)
		ackRenew(verb, RenewResult{Outcome: RenewOutcomeRefusedState}, &rejectStateError{state: "renew already in flight"})
		return
	}

	l.wg.Add(1)
	go l.runRenewVerb(ctx, state, verb)
}

func (l *Lifecycle) runRenewVerb(ctx context.Context, state State, verb renewVerb) {
	defer l.wg.Done()
	defer l.renewInFlight.Store(false)
	defer func() {
		if r := recover(); r != nil {
			l.deps.Logger.Error("supervise: renew worker panic", slog.Any("recover", r))
			ackRenew(verb, RenewResult{Outcome: RenewOutcomeError}, fmt.Errorf("supervise: %w", errRenewPanic))
		}
	}()

	res, err := l.performRenewClaim(ctx)
	if err != nil {
		l.emitClientRenewInvoked(ctx, string(state), res.Outcome, verb.req.Restart)
		ackRenew(verb, res, err)
		return
	}

	restartErr := l.applyRenewPostClaim(ctx, state, verb.req)
	if restartErr != nil {
		err = fmt.Errorf("supervise: renew restart: %w", restartErr)
	}
	res.Restarted = restartErr == nil && verb.req.Restart
	l.emitClientRenewInvoked(ctx, string(state), res.Outcome, res.Restarted)
	ackRenew(verb, res, err)
}

func (l *Lifecycle) performRenewClaim(ctx context.Context) (RenewResult, error) {
	refresh := l.performRefreshClaim(ctx)
	outcome := RenewOutcomeRenewed
	if refresh.err != nil {
		outcome = renewOutcomeForRefreshError(refresh.err)
		return RenewResult{Outcome: outcome}, refresh.err
	}
	return RenewResult{
		Outcome:          outcome,
		SessionExpiresAt: l.inputs.SessionExpiresAt(),
		JTI:              l.inputs.SessionJTI(),
	}, nil
}

func renewOutcomeForRefreshError(err error) string {
	switch {
	case errors.Is(err, errRefreshDenied):
		return RenewOutcomeDenied
	case errors.Is(err, errRefreshTimeout):
		return RenewOutcomeTimeout
	}
	return RenewOutcomeError
}

func (l *Lifecycle) applyRenewPostClaim(ctx context.Context, state State, req RenewRequest) error {
	if state == StateAwaitingApproval {
		l.transition(ctx, EventApprovalGranted)
		return l.silentRefillAndRestart(ctx)
	}
	if req.Restart {
		l.stopChildForRefresh()
		l.transition(ctx, EventRefreshRequested)
		return l.silentRefillAndRestart(ctx)
	}
	return nil
}

func ackRenew(verb renewVerb, res RenewResult, err error) {
	select {
	case verb.ack <- renewResult{res: res, err: err}:
	default:
	}
}
