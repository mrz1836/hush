// Supervisor orchestration glue: HTTP-proxy reload swap (T-306 Phase 5).
//
// lifecycle_swap.go owns SwapChild, the single entry point that takes the
// supervisor from StateRunning through StateSwapping and back. It is the
// orchestration counterpart to proxy.go (the public-listener side) and to
// the readiness prober + backend-port allocator built in Phases 3 and 4.
//
// The swap is intentionally synchronous from the caller's perspective:
// the status-socket / CLI layer (Phases 6 and 7) blocks on SwapChild and
// surfaces the typed error to the operator. Single-flight is enforced by
// Lifecycle.swapInFlight; concurrent calls return ErrSwapInFlight.
//
// Anti-contract: every code path either reaches a single emitChildSwap
// audit call (on success) or returns a wrapped sentinel error without
// emitting that event. The audit payload contains PIDs, timing, and the
// strategy string — never any secret/env value.

package supervise

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/mrz1836/hush/internal/supervise/config"
)

// HandoffStrategyHTTPProxy is the audit-event strategy string emitted on
// every successful HTTP-proxy reload swap. Mirrored as a const so future
// strategies (socket-activation) can be added without restructuring the
// caller surface.
const HandoffStrategyHTTPProxy = "http-proxy"

// reapHardCeiling caps how long reapSwapCandidate waits for child.Wait()
// to return after SIGKILL. Beyond this the reap goroutine is allowed to
// outlive the caller (it dies with the supervisor process). 5s comfortably
// covers any normal kernel reap path; an unkillable-sleep child past this
// window is a kernel/driver problem, not a hush problem.
const reapHardCeiling = 5 * time.Second

// Swap-related sentinel errors. Compare via errors.Is.
var (
	// ErrSwapInFlight indicates another SwapChild call is already in
	// progress. Maps to the public reload result string "swap-in-flight".
	ErrSwapInFlight = errors.New("supervise: swap already in flight")

	// ErrSwapNotEligible indicates the supervisor's config does not opt
	// into HTTP-proxy handoff. Maps to "config-invalid".
	ErrSwapNotEligible = errors.New("supervise: swap requires [child.handoff] mode = http-proxy")

	// ErrSwapWrongState indicates the lifecycle is not in StateRunning
	// at SwapChild entry. The caller should retry once the supervisor
	// settles into running.
	ErrSwapWrongState = errors.New("supervise: swap requires StateRunning")

	// ErrSwapReadinessFailed wraps a readiness probe failure on the new
	// child. The old child is left untouched. Maps to "readiness-failed".
	ErrSwapReadinessFailed = errors.New("supervise: swap readiness probe failed")

	// ErrSwapNoChild indicates no live child exists to swap from. The
	// lifecycle was likely past mainLoop entry but child died between
	// the state check and child read.
	ErrSwapNoChild = errors.New("supervise: no live child to swap from")

	// ErrSwapProxyMissing indicates the lifecycle has no proxy attached.
	// Callers MUST AttachProxy before SwapChild.
	ErrSwapProxyMissing = errors.New("supervise: proxy not attached")

	// ErrSwapBackendAllocate wraps any backend port allocation failure
	// during the swap path.
	ErrSwapBackendAllocate = errors.New("supervise: swap backend port allocate")

	// ErrSwapChildStart wraps any child Start failure during the swap
	// path.
	ErrSwapChildStart = errors.New("supervise: swap child start")

	// ErrPromoteNoBackendPort indicates promoteFirstChildToProxy was
	// invoked before the backend port was recorded. Defensive — backend
	// allocation precedes promote in normal flow; this guards a future
	// reorder.
	ErrPromoteNoBackendPort = errors.New("supervise: promote child to proxy: no backend port allocated")

	// errSwapDispatchPanic is the synthetic error dispatchSwapVerb sends
	// on a verb's ack channel when executeSwap panics. Package-private —
	// callers see it wrapped as "supervise: swap dispatch panicked"
	// without the recovered value (which may carry implementation-detail
	// strings unsafe to surface to operators).
	errSwapDispatchPanic = errors.New("supervise: swap dispatch panicked")
)

// SwapResult is the public outcome of a successful SwapChild call.
// Every field is non-secret: PIDs are kernel identifiers, the duration
// is wall-clock, and the strategy is a fixed enum string.
type SwapResult struct {
	OldPID            int
	NewPID            int
	ReadinessDuration time.Duration
	Strategy          string
}

// AttachProxy registers p as the supervisor's reload-side reverse proxy.
// SwapChild reads this under l.proxyMu. AttachProxy does NOT start the
// proxy — the caller controls p.Start lifetime so the proxy can bind
// before the first child receives traffic.
//
// AttachProxy is the seam Phase 6/7's wiring layer uses; tests construct
// a Proxy and attach it directly.
func (l *Lifecycle) AttachProxy(p *Proxy) {
	l.proxyMu.Lock()
	defer l.proxyMu.Unlock()
	l.proxy = p
}

// proxyHandle returns the attached proxy under the proxy lock.
func (l *Lifecycle) proxyHandle() *Proxy {
	l.proxyMu.Lock()
	defer l.proxyMu.Unlock()
	return l.proxy
}

// AttachReloadHandler registers handler as the status-socket reload
// dispatcher. The handler is invoked once per `hush supervise reload`
// SDK call against this supervisor's status socket; it MUST return a
// SwapResult (or error) — typically by delegating to
// (*Lifecycle).SwapChild for HTTP-proxy handoff supervisors.
//
// Without this wiring, `hush supervise reload <toml>` calls the SDK,
// the SDK reaches the status socket, and the server responds with
// `reload handler not wired` (see internal/supervise/socket.go).
//
// Single-shot: a second call panics, matching the StatusServer's
// AttachReloadHandler contract. Callers should invoke exactly once
// during supervisor boot, ideally after AttachProxy + Proxy.Start.
//
// This mirrors the integration-only export at
// export_for_integration.go but is available in production builds so
// the CLI can wire reload without an `integration` build tag. The
// thin delegate keeps the StatusServer surface as the single source
// of truth for handler semantics.
func (l *Lifecycle) AttachReloadHandler(handler func(ctx context.Context, req ReloadRequest) (SwapResult, error)) {
	l.statusServer.AttachReloadHandler(handler)
}

// promoteChildToProxy is the post-startChild step that probes the freshly
// spawned child's readiness URL against its allocated backend port and,
// on success, points the attached reload proxy at that backend so the
// public listener routes traffic to the live child. Without this, the
// proxy keeps responding `503 no-backend` after initial boot (and after
// every non-swap restart, e.g. a crash recovery) — exactly the trap the
// 2026-05-25 16:45 cutover hit before this method existed.
//
// No-op on three branches so callers can invoke unconditionally:
//   - [child.handoff] is not configured at all
//   - [child.handoff].mode is not "http-proxy" (defensive against
//     future strategies that don't route through this proxy)
//   - no proxy has been attached (embedded pkg/client users manage the
//     proxy lifetime themselves and may set the backend out-of-band)
//
// On failure the just-spawned child is SIGTERM'd via the configured
// shutdown grace so the caller's rollback (destroy secrets, return
// error) leaves no orphaned process behind the dead-but-bound public
// listener. The pattern mirrors SwapChild's readiness-failure path.
func (l *Lifecycle) promoteChildToProxy(ctx context.Context) error {
	if l.config.Child.Handoff == nil {
		return nil
	}
	if l.config.Child.Handoff.Mode != config.HandoffModeHTTPProxy {
		return nil
	}
	proxy := l.proxyHandle()
	if proxy == nil {
		return nil
	}
	if l.config.Child.Readiness == nil {
		// Config loader rejects handoff without readiness; defensive
		// guard so a programmatic constructor can't smuggle a partial
		// config past validate().
		return fmt.Errorf("supervise: %w: handoff requires [child.readiness]", ErrSwapNotEligible)
	}

	l.backendMu.Lock()
	port := l.backendPort
	l.backendMu.Unlock()
	if port == 0 {
		return ErrPromoteNoBackendPort
	}

	readinessCfg := *l.config.Child.Readiness
	readinessCfg.HTTPURL = swapReadinessURL(readinessCfg.HTTPURL, port)

	if _, probeErr := ProbeHTTPReady(ctx, l.deps.HTTPClient, readinessCfg); probeErr != nil {
		l.deps.Logger.Warn(
			"supervise: promote readiness failed; terminating child",
			slog.Int("port", int(port)),
			slog.Any("err", probeErr),
		)
		l.terminateCurrentChild(ctx)
		return fmt.Errorf("supervise: promote child readiness: %w", probeErr)
	}

	if setErr := proxy.SetBackend(port); setErr != nil {
		l.deps.Logger.Warn(
			"supervise: promote backend swap failed; terminating child",
			slog.Int("port", int(port)),
			slog.Any("err", setErr),
		)
		l.terminateCurrentChild(ctx)
		return fmt.Errorf("supervise: promote backend pointer: %w", setErr)
	}

	l.deps.Logger.Info(
		"supervise: child promoted to proxy backend",
		slog.Int("port", int(port)),
	)
	return nil
}

// terminateCurrentChild reads the live child reference and applies
// terminateChildWithGrace so promoteChildToProxy's failure paths leave
// no orphaned process behind. No-op when no child is currently held.
func (l *Lifecycle) terminateCurrentChild(ctx context.Context) {
	l.childMu.Lock()
	child := l.child
	l.childMu.Unlock()
	if child == nil {
		return
	}
	l.terminateChildWithGrace(ctx, child, l.config.Child.Shutdown.Grace)
}

// SwapChild orchestrates an HTTP-proxy reload: starts a new child on a
// fresh private backend port, probes readiness, atomically points the
// proxy at the new backend, audits the swap, and terminates the old
// child with the configured shutdown grace. On readiness failure the
// new child is reaped and the old child is left serving.
//
// Pre-conditions:
//   - [child.handoff] mode = "http-proxy" (otherwise ErrSwapNotEligible).
//   - The lifecycle is in StateRunning with a live child (otherwise
//     ErrSwapWrongState / ErrSwapNoChild).
//   - A Proxy has been attached via AttachProxy (otherwise
//     ErrSwapProxyMissing).
//
// Post-conditions on success: l.child references the new child, the
// proxy backend points at the new private port, l.backendPort is the
// new port, an emitChildSwap audit event has been appended, and the
// state machine has returned to StateRunning via EventSwapOK.
//
// SwapChild is a thin wrapper: it fast-fails the two cheap preconditions
// (handoff-eligibility, proxy attached) plus the swapInFlight single-
// flight CAS, then posts a swapVerb on swapVerbCh so the actual
// orchestration (executeSwap) runs inside mainLoop's single goroutine.
// Routing through mainLoop guarantees executeSwap cannot interleave
// with dispatchRefreshVerb, dispatchChildExit, or
// dispatchRefreshResult — fixes the V2 race in
// hush-audit/supervisor where a concurrent status-socket refresh could
// spawn a parallel child with fresh secrets.
//
// Cancellable via ctx (returns ctx.Err() if the verb cannot be posted
// or the ack does not arrive). Single-flight: concurrent callers
// receive ErrSwapInFlight.
func (l *Lifecycle) SwapChild(ctx context.Context) (SwapResult, error) {
	if !l.isHandoffEligible() {
		return SwapResult{}, fmt.Errorf("supervise: %w", ErrSwapNotEligible)
	}
	if l.proxyHandle() == nil {
		return SwapResult{}, fmt.Errorf("supervise: %w", ErrSwapProxyMissing)
	}
	if !l.swapInFlight.CompareAndSwap(false, true) {
		return SwapResult{}, fmt.Errorf("supervise: %w", ErrSwapInFlight)
	}
	defer l.swapInFlight.Store(false)

	verb := swapVerb{ack: make(chan swapVerbResult, 1)}
	select {
	case l.swapVerbCh <- verb:
	case <-ctx.Done():
		return SwapResult{}, ctx.Err()
	}
	select {
	case res := <-verb.ack:
		return res.result, res.err
	case <-ctx.Done():
		return SwapResult{}, ctx.Err()
	}
}

// dispatchSwapVerb is mainLoop's arm for swap verbs. It calls executeSwap
// inside a recover() so a panic on the orchestration path does not (a)
// crash mainLoop and (b) deadlock the SwapChild caller blocked on the
// verb's ack channel. On a recovered panic, sends a synthetic
// errSwapDispatchPanic on the ack channel.
func (l *Lifecycle) dispatchSwapVerb(ctx context.Context, verb swapVerb) {
	defer func() {
		if r := recover(); r != nil {
			l.deps.Logger.Error(
				"supervise: dispatchSwapVerb panic",
				slog.Any("recover", r),
			)
			select {
			case verb.ack <- swapVerbResult{err: fmt.Errorf("supervise: %w", errSwapDispatchPanic)}:
			default:
			}
		}
	}()
	res, err := l.executeSwap(ctx)
	select {
	case verb.ack <- swapVerbResult{result: res, err: err}:
	default:
	}
}

// executeSwap performs the HTTP-proxy reload swap. It runs inside
// mainLoop's goroutine (via dispatchSwapVerb), so concurrent
// dispatchChildExit / dispatchRefreshVerb / dispatchRefreshResult arms
// cannot interleave with it. The proxy and handoff-eligibility
// preconditions were already verified by the SwapChild wrapper.
//
// The state move into StateSwapping uses Store.TransitionIf — atomic
// under the state write lock — so a concurrent direct mutator (tests
// or future code paths) cannot race the precondition check against
// the transition.
//
//nolint:funlen,cyclop // sequential orchestration; each step has explicit error handling
func (l *Lifecycle) executeSwap(ctx context.Context) (SwapResult, error) {
	p := l.proxyHandle()
	if p == nil {
		return SwapResult{}, fmt.Errorf("supervise: %w", ErrSwapProxyMissing)
	}

	// Atomic single critical section: requires StateRunning AND moves
	// into StateSwapping. Even though mainLoop already serializes us
	// against the other dispatch arms, TransitionIf gives unit tests
	// a clean way to assert the invariant — and protects future code
	// paths that might call executeSwap from outside mainLoop.
	if err := l.store.TransitionIf(ctx, StateRunning, EventReloadRequested); err != nil {
		return SwapResult{}, fmt.Errorf("supervise: %w (transition: %w)", ErrSwapWrongState, err)
	}

	l.childMu.Lock()
	oldChild := l.child
	l.childMu.Unlock()
	if oldChild == nil {
		// State already moved to StateSwapping; restore via EventSwapFailed.
		l.transition(ctx, EventSwapFailed)
		return SwapResult{}, fmt.Errorf("supervise: %w", ErrSwapNoChild)
	}
	oldPID := oldChild.PID()

	// Gather secrets for the replacement child. We re-fetch via Refiller
	// using the existing JWT — A5 in the plan: no fresh /claim required.
	secrets, secErr := l.refiller.Refill(ctx, l.config.Scope)
	if secErr != nil {
		l.transition(ctx, EventSwapFailed)
		return SwapResult{}, fmt.Errorf("supervise: swap refill: %w", secErr)
	}

	// Start the new child as a "candidate" — manually call Child.Start
	// without spawning the standard wait loop, so we control wait-channel
	// wiring until readiness succeeds.
	newChild, stdoutCloser, stderrCloser, newPort, startErr := l.startSwapCandidate(ctx, secrets)
	// Secrets ownership: startSwapCandidate borrowed them. On any post-call
	// error path we still need them destroyed; on success the orchestrator's
	// pre-existing grace cache continues to hold the canonical copy. We
	// destroy the borrowed set unconditionally here — Grace already holds
	// the long-lived plaintext.
	destroySecrets(secrets)
	if startErr != nil {
		l.transition(ctx, EventSwapFailed)
		return SwapResult{}, startErr
	}
	newPID := newChild.PID()

	if l.config.Child.Readiness == nil {
		// Defensive: isHandoffEligible already checked readiness via
		// validate, but guard the dereference anyway.
		l.reapSwapCandidate(ctx, newChild, l.config.Child.Shutdown.Grace, reasonReapReadinessConfigMissing)
		closeIfNotNil(stdoutCloser)
		closeIfNotNil(stderrCloser)
		l.transition(ctx, EventSwapFailed)
		return SwapResult{}, fmt.Errorf("supervise: %w: missing [child.readiness]", ErrSwapNotEligible)
	}
	readinessCfg := *l.config.Child.Readiness
	// Rewrite the readiness URL to target the new child's private port.
	// The operator-configured URL host:port is the placeholder; we
	// substitute the loopback:port the new child is bound on.
	readinessCfg.HTTPURL = swapReadinessURL(readinessCfg.HTTPURL, newPort)

	readinessDur, probeErr := ProbeHTTPReady(ctx, l.deps.HTTPClient, readinessCfg)
	if probeErr != nil {
		l.deps.Logger.Warn("supervise: swap readiness failed",
			slog.Int("new_pid", newPID),
			slog.Any("err", probeErr))
		l.reapSwapCandidate(ctx, newChild, l.config.Child.Shutdown.Grace, reasonReapReadinessProbeFailed)
		closeIfNotNil(stdoutCloser)
		closeIfNotNil(stderrCloser)
		l.transition(ctx, EventSwapFailed)
		return SwapResult{}, fmt.Errorf("%w: %w", ErrSwapReadinessFailed, probeErr)
	}

	// Atomic backend swap. Past this line the proxy routes new traffic
	// to the replacement child.
	if setErr := p.SetBackend(newPort); setErr != nil {
		l.reapSwapCandidate(ctx, newChild, l.config.Child.Shutdown.Grace, reasonReapBackendSetFailed)
		closeIfNotNil(stdoutCloser)
		closeIfNotNil(stderrCloser)
		l.transition(ctx, EventSwapFailed)
		return SwapResult{}, fmt.Errorf("supervise: swap backend pointer: %w", setErr)
	}

	// Promote the new child as the lifecycle child. Take ownership of
	// the existing child slot under l.childMu.
	now := l.deps.NowFn()
	l.childMu.Lock()
	l.child = newChild
	l.childStarted = now
	l.childMu.Unlock()
	l.inputs.childStartedAt.Store(&now)
	l.childRunning.Store(true)
	l.store.setChildPID(newPID)

	l.backendMu.Lock()
	l.backendPort = newPort
	l.backendMu.Unlock()

	// Spawn the standard wait loop on the new child so future exits are
	// processed via the orchestrator's main dispatch path.
	l.wg.Add(1)
	go l.childWaitLoop(ctx, newChild, stdoutCloser, stderrCloser)

	// The old child's pending exit (post-SIGTERM) is NOT a regular
	// crash — flag it so dispatchChildExit drops it.
	l.suppressNextChildExit.Store(true)

	// Audit the successful swap with PIDs/timing/strategy — no secret
	// material by construction.
	l.emitChildSwap(ctx, oldPID, newPID, readinessDur, HandoffStrategyHTTPProxy)

	// Transition back to running BEFORE we terminate the old child so
	// dispatchChildExit (which reads via store.Snapshot indirectly) sees
	// the post-swap state.
	l.transition(ctx, EventSwapOK)

	// Terminate the old child with the configured shutdown grace.
	l.terminateChildWithGrace(ctx, oldChild, l.config.Child.Shutdown.Grace)

	l.inputs.restartCount.Add(1)
	return SwapResult{
		OldPID:            oldPID,
		NewPID:            newPID,
		ReadinessDuration: readinessDur,
		Strategy:          HandoffStrategyHTTPProxy,
	}, nil
}

// isHandoffEligible reports whether the supervisor's config opts into
// HTTP-proxy handoff. Validation in package config already enforces the
// related constraints (readiness present, HUSH_BIND_PORT referenced) so
// SwapChild can rely on those being satisfied when this returns true.
func (l *Lifecycle) isHandoffEligible() bool {
	if l.config == nil || l.config.Child.Handoff == nil {
		return false
	}
	return l.config.Child.Handoff.Mode == config.HandoffModeHTTPProxy
}

// startSwapCandidate builds the env overlay, opens output sinks, and
// invokes Child.Start without spawning the standard wait loop. Returns
// the live child plus its stdout/stderr closers so the swap orchestrator
// can either promote (transferring the closers to the new wait loop) or
// terminate (closing the closers) without leaking file descriptors.
//
// The secrets argument is borrowed — startSwapCandidate neither retains
// nor destroys them. The caller owns destruction on every exit path.
//
//nolint:funlen // sequential setup mirroring startChild minus the wait-loop spawn
func (l *Lifecycle) startSwapCandidate(ctx context.Context, secrets secretSet) (*Child, io.Closer, io.Closer, uint16, error) {
	port, allocErr := AllocateBackendPort(ctx)
	if allocErr != nil {
		return nil, nil, nil, 0, fmt.Errorf("%w: %w", ErrSwapBackendAllocate, allocErr)
	}
	overlay := map[string]string{
		config.EnvVarBindPort: strconv.FormatUint(uint64(port), 10),
	}

	env, envErr := l.buildChildEnv(secrets, overlay)
	defer func() {
		// Zero the env slice on every exit path — Child.Start copies the
		// slice into cmd.Env, so wiping the parent view is safe.
		for i := range env {
			env[i] = ""
		}
	}()
	if envErr != nil {
		return nil, nil, nil, 0, fmt.Errorf("%w: %w", ErrSwapChildStart, envErr)
	}

	stdoutSink, stdoutCloser, stderrSink, stderrCloser, sinkErr := l.openChildSinks()
	if sinkErr != nil {
		return nil, nil, nil, 0, fmt.Errorf("%w: %w", ErrSwapChildStart, sinkErr)
	}
	lsw := newLineSplittingWriter(ctx, stderrSink, l.deps.Watchdog, l.deps.Logger)

	childCfg := ChildConfig{
		Command: l.config.Child.Command,
		Env:     env,
		Dir:     l.config.Child.WorkingDir,
		Stdout:  stdoutSink,
		Stderr:  lsw,
		Logger:  l.deps.Logger,
	}
	newChild := NewChild(childCfg)
	if err := newChild.Start(ctx); err != nil {
		closeIfNotNil(stdoutCloser)
		closeIfNotNil(stderrCloser)
		return nil, nil, nil, 0, fmt.Errorf("%w: %w", ErrSwapChildStart, err)
	}
	return newChild, stdoutCloser, stderrCloser, port, nil
}

// reapSwapCandidate terminates AND reaps a candidate child spawned by
// startSwapCandidate but never promoted (i.e. no childWaitLoop was
// attached). It is the only correct teardown path for the three SwapChild
// failure branches — terminateChildWithGrace alone leaves the candidate
// as a zombie + leaks the per-Child drain/forward/death-watch goroutines
// (no one ever calls child.Wait, which is what closes c.childDone +
// joins c.wg).
//
// Sequence:
//  1. Spawn an UNTRACKED reap goroutine that calls child.Wait() and
//     signals waitDone. The goroutine is not on Lifecycle.wg because we
//     do not want runShutdown to block on a stuck (uninterruptible-
//     sleep) child past the hard ceiling — bounded by supervisor
//     lifetime is acceptable.
//  2. SIGTERM.
//  3. Wait for {waitDone, grace, ctx.Done}.
//  4. If grace expired or ctx cancelled, SIGKILL.
//  5. Wait for {waitDone, reapHardCeiling}.
//  6. Emit ActionSupervisorSwapCandidateReaped with timing + reason.
//
// grace<=0 collapses to a short floor (50ms) so unit tests passing zero
// do not loop forever. Production callers receive
// [child.shutdown.grace] which validate ensures is positive.
//
// Returns nothing — the audit event is the operator-visible contract.
func (l *Lifecycle) reapSwapCandidate(ctx context.Context, child *Child, grace time.Duration, reason string) {
	if child == nil {
		return
	}
	candidatePID := child.PID()
	start := l.deps.NowFn()

	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		defer func() {
			if r := recover(); r != nil {
				l.deps.Logger.Error("supervise: swap-candidate reap goroutine panic", slog.Any("recover", r))
			}
		}()
		_, _, _ = child.Wait()
	}()

	_ = child.Forward(syscall.SIGTERM)
	if grace <= 0 {
		grace = 50 * time.Millisecond
	}
	graceTimer := time.NewTimer(grace)
	defer graceTimer.Stop()

	var (
		escalated       bool
		ceilingExceeded bool
	)
	select {
	case <-waitDone:
		l.emitSwapCandidateReaped(ctx, candidatePID, escalated, ceilingExceeded, l.deps.NowFn().Sub(start), reason)
		return
	case <-graceTimer.C:
	case <-ctx.Done():
	}

	escalated = true
	_ = child.Forward(syscall.SIGKILL)
	hardTimer := time.NewTimer(reapHardCeiling)
	defer hardTimer.Stop()
	select {
	case <-waitDone:
	case <-hardTimer.C:
		ceilingExceeded = true
		l.deps.Logger.Warn(
			"supervise: swap-candidate reap exceeded hard ceiling",
			slog.Int("candidate_pid", candidatePID),
			slog.String("reason", reason),
		)
	}
	l.emitSwapCandidateReaped(ctx, candidatePID, escalated, ceilingExceeded, l.deps.NowFn().Sub(start), reason)
}

// terminateChildWithGrace sends SIGTERM to child, polls for natural exit
// up to grace, then sends SIGKILL if still alive. Always returns once
// either the child exits (child.PID()==0) or grace+ctx is exhausted.
//
// grace<=0 collapses to a short floor (50ms) so test fixtures that pass
// zero do not loop forever. Production callers configure
// [child.shutdown.grace] which validate ensures is positive.
//
// Used for children that already have a parallel wait loop (the
// supervisor's main child, started via initialRefillAndStart or
// silentRefillAndRestart). The wait loop zeroes child.PID() inside
// Wait, allowing the poll here to short-circuit on natural exit. For
// SwapChild's candidate children — which never get a wait loop — use
// reapSwapCandidate instead.
func (l *Lifecycle) terminateChildWithGrace(ctx context.Context, child *Child, grace time.Duration) {
	if child == nil {
		return
	}
	_ = child.Forward(syscall.SIGTERM)
	if grace <= 0 {
		grace = 50 * time.Millisecond
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if child.PID() == 0 {
			return
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	if child.PID() != 0 {
		_ = child.Forward(syscall.SIGKILL)
	}
}

// swapReadinessURL rewrites a configured readiness URL so it targets the
// new private backend port. The configured URL's host:port is replaced
// with 127.0.0.1:<port>; the path/query/fragment are preserved. When
// the URL is malformed (validation should prevent this), the original
// URL is returned unchanged.
func swapReadinessURL(raw string, port uint16) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return raw
	}
	u.Host = "127.0.0.1:" + strconv.FormatUint(uint64(port), 10)
	return u.String()
}
