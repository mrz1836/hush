package supervise

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mrz1836/hush/internal/supervise/config"
)

// ErrReadinessTimeout is the sentinel returned (wrapped) by
// ProbeHTTPReady when the configured Timeout budget elapses before
// any probe attempt observes a 2xx response. Identifiable via
// errors.Is.
var ErrReadinessTimeout = errors.New("supervise: readiness probe timed out")

// errReadinessConfigInvalid backs the sentinel we wrap when a caller
// passes a nil client or non-positive durations. Programmer-error
// class; not part of any operator-visible contract.
var errReadinessConfigInvalid = errors.New("supervise: readiness probe configuration invalid")

// ProbeHTTPReady polls cfg.HTTPURL with HTTP GET requests until any
// 2xx response is observed, the supplied ctx is canceled, or the
// cfg.Timeout budget is exhausted. On success it returns the elapsed
// wall-clock duration from the first attempt to the 2xx observation.
//
// Polling cadence is cfg.Interval. Each attempt inherits a deadline
// equal to the remaining budget so a hung server cannot pin the
// goroutine past budget exhaustion.
//
// Error contract:
//   - Budget exhaustion → wraps ErrReadinessTimeout.
//   - Caller cancellation (outer ctx done with non-deadline cause) →
//     wraps ctx.Err() (context.Canceled or context.DeadlineExceeded).
//   - client nil or non-positive Timeout/Interval → wraps
//     errReadinessConfigInvalid; programmer-error class.
//
// Returned errors are log-safe by construction: they include only the
// configured probe URL (operator-supplied, non-secret), the latest
// observed HTTP status code (when an attempt completed), and elapsed
// time. Response bodies are read and discarded — they never appear in
// the error chain. The function never logs, never reads child env
// values, and never touches the supervisor's secret material.
func ProbeHTTPReady(ctx context.Context, client *http.Client, cfg config.ChildReadiness) (time.Duration, error) {
	if err := validateProbeArgs(client, cfg); err != nil {
		return 0, err
	}
	start := time.Now()
	deadline := start.Add(cfg.Timeout)
	probeCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return runProbeLoop(ctx, probeCtx, client, cfg, start, deadline)
}

// runProbeLoop is the iteration body of ProbeHTTPReady. It is split
// out so the entry point stays well under the package's cognitive
// complexity cap. The loop guarantees one bounded HTTP attempt per
// cfg.Interval cadence and a deterministic exit on either 2xx, ctx
// cancellation, or budget exhaustion.
func runProbeLoop(ctx, probeCtx context.Context, client *http.Client, cfg config.ChildReadiness, start, deadline time.Time) (time.Duration, error) {
	var (
		lastStatus int
		lastErr    error
	)
	for {
		status, attemptErr := probeOnce(probeCtx, client, cfg.HTTPURL)
		if attemptErr == nil && status >= 200 && status < 300 {
			return time.Since(start), nil
		}
		lastStatus, lastErr = recordLast(status, attemptErr, lastStatus, lastErr)
		if err := postAttemptStatus(ctx, deadline); err != nil {
			return 0, finalizeErr(err, cfg.HTTPURL, time.Since(start), lastStatus, lastErr)
		}
		if err := waitForNextTick(ctx, probeCtx, deadline, cfg.Interval); err != nil {
			return 0, finalizeErr(err, cfg.HTTPURL, time.Since(start), lastStatus, lastErr)
		}
	}
}

// recordLast updates the carried "most recent" status and transport
// error after a failed attempt, preserving meaningful values when an
// attempt produces only one of the two (a 503 with no transport error,
// or a connection-refused with no status code).
func recordLast(status int, attemptErr error, lastStatus int, lastErr error) (int, error) {
	if attemptErr != nil {
		lastErr = attemptErr
	}
	if status != 0 {
		lastStatus = status
	}
	return lastStatus, lastErr
}

// validateProbeArgs enforces the programmer-error preconditions on
// ProbeHTTPReady — pulled out so the main loop stays readable.
func validateProbeArgs(client *http.Client, cfg config.ChildReadiness) error {
	if client == nil {
		return fmt.Errorf("%w: nil *http.Client", errReadinessConfigInvalid)
	}
	if cfg.Timeout <= 0 {
		return fmt.Errorf("%w: Timeout=%s must be > 0", errReadinessConfigInvalid, cfg.Timeout)
	}
	if cfg.Interval <= 0 {
		return fmt.Errorf("%w: Interval=%s must be > 0", errReadinessConfigInvalid, cfg.Interval)
	}
	return nil
}

// probeOnce issues a single GET against url with a context-bound
// request, drains the body, and returns the status code (or 0 when
// the request itself failed before producing a response).
//
// The body is always drained-and-closed so the underlying connection
// can be reused — important because a tight probe loop otherwise
// leaks idle sockets at cfg.Interval cadence.
func probeOnce(ctx context.Context, client *http.Client, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("supervise: readiness probe build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("supervise: readiness probe request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// postAttemptStatus checks the loop-control conditions after a failed
// attempt: caller cancellation always wins; otherwise budget exhaustion
// is detected wall-clock-style. Returns nil when the loop should
// proceed to its inter-attempt sleep.
func postAttemptStatus(ctx context.Context, deadline time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// waitForNextTick blocks for at most interval (capped by remaining
// budget) and returns nil when the next attempt should fire. It
// returns the appropriate cancellation cause otherwise: ctx.Err()
// when the caller cancels, context.DeadlineExceeded when the budget
// expires mid-sleep.
func waitForNextTick(ctx, probeCtx context.Context, deadline time.Time, interval time.Duration) error {
	sleep := interval
	if remaining := time.Until(deadline); remaining < sleep {
		sleep = remaining
	}
	if sleep <= 0 {
		return nil
	}
	timer := time.NewTimer(sleep)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-probeCtx.Done():
		return context.DeadlineExceeded
	case <-timer.C:
		return nil
	}
}

// finalizeErr converts a loop-control cause into the final
// caller-visible error. context.Canceled and context.DeadlineExceeded
// from the outer ctx become caller-cancellation errors; the local
// budget-exhaustion path becomes an ErrReadinessTimeout wrap.
func finalizeErr(cause error, url string, elapsed time.Duration, lastStatus int, lastErr error) error {
	if errors.Is(cause, context.Canceled) {
		return fmt.Errorf("supervise: readiness probe interrupted for %s after %s: %w",
			url, elapsed, cause)
	}
	return wrapTimeout(url, elapsed, lastStatus, lastErr)
}

// wrapTimeout formats the ErrReadinessTimeout return path. It includes
// the URL (non-secret, operator-configured), elapsed time, and the
// most recently observed transport-layer failure or status code. The
// caller's lastErr is wrapped via %w so callers can errors.Is /
// errors.As against it while still pivoting on ErrReadinessTimeout.
func wrapTimeout(url string, elapsed time.Duration, lastStatus int, lastErr error) error {
	switch {
	case lastErr != nil && lastStatus != 0:
		return fmt.Errorf("supervise: readiness probe for %s exhausted budget after %s (last status %d, last error: %w): %w",
			url, elapsed, lastStatus, lastErr, ErrReadinessTimeout)
	case lastErr != nil:
		return fmt.Errorf("supervise: readiness probe for %s exhausted budget after %s (last error: %w): %w",
			url, elapsed, lastErr, ErrReadinessTimeout)
	case lastStatus != 0:
		return fmt.Errorf("supervise: readiness probe for %s exhausted budget after %s (last status %d): %w",
			url, elapsed, lastStatus, ErrReadinessTimeout)
	default:
		return fmt.Errorf("supervise: readiness probe for %s exhausted budget after %s: %w",
			url, elapsed, ErrReadinessTimeout)
	}
}
