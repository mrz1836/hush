// Supervisor orchestration glue: child env / start / wait / exit dispatch.
//
// lifecycle_child.go owns the initial child env build from Grace, the
// validator pass, NewChild + Start, the childWaitLoop goroutine, the
// childExit dispatch (0 / non-zero non-Exit78 / Exit78 — referencing the
// Exit78 constant, never the raw literal), and the silent refill
// path (post-running). It also implements the lineSplittingWriter that
// fans bytes to both the operator stderr sink AND the Watchdog hook
// WITHOUT opening a second drain on Child.Stderr.

package supervise

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// secretSet is the per-scope plaintext map handed from the Refiller to the
// validator pass and the child env builder. Ownership of every *SecureBytes
// either transfers to the Grace cache (retainSecrets) or is destroyed
// (destroySecrets) before the orchestrator drops its reference.
type secretSet = map[string]*securebytes.SecureBytes

// initialRefillAndStart performs the boot-time Refiller.Refill → validators
// → child env build → Child.Start sequence. Returns nil on entry to mainLoop
// with the child running.
func (l *Lifecycle) initialRefillAndStart(ctx context.Context) error {
	secrets, err := l.refiller.Refill(ctx, l.config.Scope)
	if err != nil {
		if errors.Is(err, ErrJTIUnknown) {
			l.deps.Alerts.Emit(ctx, AlertClassVaultRejectedJWT, AlertPayload{
				ErrorClass: errorClassUnknownJTI,
				Reason:     alertReasonFor(AlertClassVaultRejectedJWT),
			})
			l.emitStaleAlert(ctx, AlertClassVaultRejectedJWT, "", errorClassUnknownJTI)
			l.emitAwaitingApproval(ctx, causeUnknownJTI)
			l.transition(ctx, EventFetchAuthRequired)
			return nil
		}
		// Boot-time generic refill failure.
		l.deps.Logger.Warn("supervise: initial refill failed",
			slog.Any("err", err))
		l.deps.Alerts.Emit(ctx, AlertClassRefillFailed, AlertPayload{
			ErrorClass: errorClassTransient,
			Reason:     alertReasonFor(AlertClassRefillFailed),
		})
		l.emitStaleAlert(ctx, AlertClassRefillFailed, "", errorClassTransient)
		l.emitAwaitingApproval(ctx, causeRefillFailed)
		l.transition(ctx, EventClaimUnavailable)
		return nil
	}

	// Validator failure: validateAllScopes already emitted the alert + audit
	// and transitioned to StateAwaitingApproval. Boot itself succeeded — the
	// supervisor is up, parked awaiting approval — so Run returns nil here.
	if verr := l.validateAllScopes(ctx, secrets); verr != nil {
		destroySecrets(secrets)
		return nil //nolint:nilerr // verr is handled in-band (state → awaiting-approval); boot did not fail
	}

	if err := l.startChild(ctx, secrets); err != nil {
		destroySecrets(secrets)
		return err
	}
	l.retainSecrets(secrets)
	l.transition(ctx, EventFetchOK)
	return nil
}

// destroySecrets destroys every *SecureBytes in the set. Used on any path
// where ownership is NOT transferred to the Grace cache.
func destroySecrets(secrets secretSet) {
	for name := range secrets {
		if secrets[name] != nil {
			_ = secrets[name].Destroy()
		}
	}
}

// retainSecrets hands every secret to the Grace cache. When the cache is
// enabled the cache takes ownership (and enforces the grace TTL); when it is
// disabled the secrets are destroyed immediately — no plaintext outlives the
// child env build (docs §2 step 11: zeroed unless grace cache is enabled).
func (l *Lifecycle) retainSecrets(secrets secretSet) {
	if !l.grace.Enabled() {
		destroySecrets(secrets)
		return
	}
	for name := range secrets {
		l.grace.Set(name, secrets[name])
	}
}

// validateAllScopes runs every configured Validator once per scope against
// the freshly fetched plaintext. On any non-nil return, emits
// AlertClassValidatorFailure with the scope name, appends
// supervisor_awaiting_approval(cause=validator) +
// supervisor_stale_alert(class=ValidatorFailure), transitions to
// StateAwaitingApproval, and returns ErrValidatorFailed. The secrets are
// borrowed — validateAllScopes neither retains nor destroys them.
func (l *Lifecycle) validateAllScopes(ctx context.Context, secrets secretSet) error {
	for _, scope := range l.config.Scope {
		v := l.lookupValidator(scope)
		sb := secrets[scope]
		if sb == nil {
			continue
		}
		if err := v.Validate(ctx, scope, sb); err != nil {
			l.deps.Alerts.Emit(ctx, AlertClassValidatorFailure, AlertPayload{
				Scope:      scope,
				ErrorClass: errorClassTransient,
				Reason:     alertReasonFor(AlertClassValidatorFailure),
			})
			l.emitStaleAlert(ctx, AlertClassValidatorFailure, scope, errorClassTransient)
			l.emitAwaitingApproval(ctx, causeValidator)
			l.markScopeStale(scope)
			l.transition(ctx, EventValidatorFailed)
			return fmt.Errorf("supervise: %w (scope=%s)", ErrValidatorFailed, scope)
		}
	}
	return nil
}

// lookupValidator returns the configured Validator for scope, or the no-op
// default when missing.
func (l *Lifecycle) lookupValidator(scope string) Validator {
	if l.deps.Validators == nil {
		return noopValidator{}
	}
	if v, ok := l.deps.Validators[scope]; ok && v != nil {
		return v
	}
	return noopValidator{}
}

// startChild builds ChildConfig.Env from the supplied per-scope plaintext,
// instantiates the Child, calls Start(ctx), spawns childWaitLoop, and updates
// inputs. The secrets are borrowed — startChild neither retains nor destroys
// them.
//
// This is the ONE permitted `string(*SecureBytes)` site — the OS fork
// boundary. The env slice is zeroed after Start returns.
func (l *Lifecycle) startChild(ctx context.Context, secrets secretSet) error {
	env := append([]string(nil), l.config.Child.EnvPassthrough...)
	// Zero the env slice on every exit path — success, every
	// error return below, and any panic that unwinds through this frame.
	// Child.Start makes its own defensive copy of the slice into cmd.Env,
	// so wiping the parent's view does not blank the child's environment.
	defer func() {
		for i := range env {
			env[i] = ""
		}
	}()
	for _, scope := range l.config.Scope {
		sb := secrets[scope]
		if sb == nil {
			return fmt.Errorf("%w: scope %q", errEnvBuildScope, scope)
		}
		var added bool
		if useErr := sb.Use(func(b []byte) {
			// Single permitted string(*SecureBytes) site at the
			// OS-execve fork boundary. Mirrors Child's Env []string.
			env = append(env, scope+"="+string(b))
			added = true
		}); useErr != nil {
			return fmt.Errorf("supervise: env build: %w", useErr)
		}
		if !added {
			return fmt.Errorf("%w: scope %q", errEnvBuildScope, scope)
		}
	}

	stderrSink := io.Discard
	stdoutSink := io.Discard
	lsw := newLineSplittingWriter(ctx, stderrSink, l.deps.Watchdog, l.deps.Logger)

	childCfg := ChildConfig{
		Command: l.config.Child.Command,
		Env:     env,
		Dir:     l.config.Child.WorkingDir,
		Stdout:  stdoutSink,
		Stderr:  lsw,
		Logger:  l.deps.Logger,
	}
	child := NewChild(childCfg)
	if err := child.Start(ctx); err != nil {
		return fmt.Errorf("supervise: child start: %w", err)
	}

	now := l.deps.NowFn()
	l.childMu.Lock()
	l.child = child
	l.childStarted = now
	l.childMu.Unlock()
	l.inputs.childStartedAt.Store(&now)
	l.childRunning.Store(true)

	l.wg.Add(1)
	go l.childWaitLoop(ctx, child)
	return nil
}

// childWaitLoop is owned by Lifecycle.wg; invokes Child.Wait once and sends
// the result on childExitCh. Top-frame recover per Constitution IX.
func (l *Lifecycle) childWaitLoop(ctx context.Context, child *Child) {
	defer l.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			l.deps.Logger.Error("supervise: childWaitLoop panic", slog.Any("recover", r))
		}
	}()
	code, sig, err := child.Wait()
	exit := childExit{code: code, signal: sig, err: err}
	select {
	case <-ctx.Done():
		return
	case l.childExitCh <- exit:
	}
}

// dispatchChildExit branches on the exit code:
//   - 0           → emit clean_exit → silent refill + restart
//   - !=0 && !=78 → emit crash      → silent refill + restart
//   - Exit78      → emit exit_78    → stale alert + StateAwaitingApproval
//
// A child the orchestrator deliberately terminated for a refresh (see
// stopChildForRefresh) is flagged via suppressNextChildExit; that exit is not
// a lifecycle event and is dropped here.
//
// The orchestrator references the Exit78 constant — never the raw
// `78` literal.
func (l *Lifecycle) dispatchChildExit(ctx context.Context, exit childExit) {
	if l.suppressNextChildExit.Swap(false) {
		l.deps.Logger.Debug("supervise: child exit suppressed (refresh restart)")
		return
	}
	l.childRunning.Store(false)
	l.childMu.Lock()
	pid := 0
	if l.child != nil {
		pid = l.child.PID()
	}
	startedAt := l.childStarted
	l.child = nil
	l.childMu.Unlock()
	zeroTime := time.Time{}
	l.inputs.childStartedAt.Store(&zeroTime)
	uptime := time.Duration(0)
	if !startedAt.IsZero() {
		uptime = l.deps.NowFn().Sub(startedAt)
	}

	switch exit.code {
	case Exit78:
		l.emitChildExit78(ctx, pid, uptime)
		l.deps.Alerts.Emit(ctx, AlertClassExit78, AlertPayload{
			ErrorClass: errorClassTransient,
			Reason:     alertReasonFor(AlertClassExit78),
		})
		l.emitStaleAlert(ctx, AlertClassExit78, "", errorClassTransient)
		l.emitAwaitingApproval(ctx, causeExit78)
		l.markAllScopesStale()
		l.transition(ctx, EventChildExit78Stale)
	case 0:
		l.emitChildCleanExit(ctx, pid, uptime)
		l.transition(ctx, EventChildExitClean)
		_ = l.silentRefillAndRestart(ctx)
	default:
		l.emitChildExitCrash(ctx, pid, exit.code, int(exit.signal), uptime)
		l.transition(ctx, EventChildExitCrash)
		_ = l.silentRefillAndRestart(ctx)
	}
}

// silentRefillAndRestart re-calls Refiller.Refill using the cached JWT,
// re-runs validators, re-builds env, re-instantiates Child + Start, and
// spawns a fresh childWaitLoop.
//
// It MUST be called with the state machine in StateFetching (callers
// transition first) and with no live child. On a refill failure it first
// attempts a grace-cache restart (tryGraceRestart); only when that is
// unavailable does it page the operator:
//   - errors.Is(err, ErrJTIUnknown) → AlertClassVaultRejectedJWT + awaiting-approval
//   - any other error              → AlertClassRefillFailed + awaiting-approval
func (l *Lifecycle) silentRefillAndRestart(ctx context.Context) error {
	secrets, err := l.refiller.Refill(ctx, l.config.Scope)
	if err != nil {
		// Grace-cache restart: docs §9 — when the operator opted into
		// cache_secrets_for_restart, a refill failure restarts the child
		// from the last-known-good plaintext instead of paging.
		if l.tryGraceRestart(ctx) {
			return nil
		}
		if errors.Is(err, ErrJTIUnknown) {
			l.deps.Alerts.Emit(ctx, AlertClassVaultRejectedJWT, AlertPayload{
				ErrorClass: errorClassUnknownJTI,
				Reason:     alertReasonFor(AlertClassVaultRejectedJWT),
			})
			l.emitStaleAlert(ctx, AlertClassVaultRejectedJWT, "", errorClassUnknownJTI)
			l.emitAwaitingApproval(ctx, causeUnknownJTI)
			l.markAllScopesStale()
			l.transition(ctx, EventFetchAuthRequired)
			return fmt.Errorf("supervise: silent refill: %w", err)
		}
		l.deps.Alerts.Emit(ctx, AlertClassRefillFailed, AlertPayload{
			ErrorClass: errorClassTransient,
			Reason:     alertReasonFor(AlertClassRefillFailed),
		})
		l.emitStaleAlert(ctx, AlertClassRefillFailed, "", errorClassTransient)
		l.emitAwaitingApproval(ctx, causeRefillFailed)
		l.markAllScopesStale()
		l.transition(ctx, EventClaimUnavailable)
		return fmt.Errorf("supervise: %w: %w", ErrRefillFailedPostRunning, err)
	}

	if verr := l.validateAllScopes(ctx, secrets); verr != nil {
		destroySecrets(secrets)
		return verr
	}
	if serr := l.startChild(ctx, secrets); serr != nil {
		destroySecrets(secrets)
		l.deps.Logger.Warn("supervise: child restart failed", slog.Any("err", serr))
		l.transition(ctx, EventClaimUnavailable)
		return serr
	}
	l.retainSecrets(secrets)
	l.inputs.restartCount.Add(1)
	l.emitSilentRefill(ctx, l.config.Scope)
	l.transition(ctx, EventFetchOK)
	return nil
}

// tryGraceRestart restarts the child from the Grace cache when every scope is
// still cached and unexpired. Returns true when it handled the refill failure
// (whether the restart itself succeeded or fell through to awaiting-approval).
// Cached secrets are borrowed from Grace — never destroyed or re-retained.
func (l *Lifecycle) tryGraceRestart(ctx context.Context) bool {
	if !l.grace.Enabled() {
		return false
	}
	cached := make(secretSet, len(l.config.Scope))
	for _, scope := range l.config.Scope {
		sb, ok := l.grace.Get(scope)
		if !ok || sb == nil {
			return false
		}
		cached[scope] = sb
	}

	l.emitGraceEntered(ctx, l.config.Scope, l.config.CacheGraceTTL)
	l.deps.Alerts.Emit(ctx, AlertClassGraceEntered, AlertPayload{
		ErrorClass: errorClassTransient,
		Reason:     alertReasonFor(AlertClassGraceEntered),
	})
	if verr := l.validateAllScopes(ctx, cached); verr != nil {
		// validateAllScopes already transitioned to StateAwaitingApproval.
		l.emitGraceExited(ctx, l.config.Scope, "expired")
		return true
	}
	if serr := l.startChild(ctx, cached); serr != nil {
		l.deps.Logger.Warn("supervise: grace restart child start failed", slog.Any("err", serr))
		l.emitGraceExited(ctx, l.config.Scope, "expired")
		l.emitAwaitingApproval(ctx, causeRefillFailed)
		l.markAllScopesStale()
		l.transition(ctx, EventClaimUnavailable)
		return true
	}
	l.inputs.restartCount.Add(1)
	l.emitGraceExited(ctx, l.config.Scope, "restart_ok")
	l.transition(ctx, EventFetchOK)
	return true
}

// stopChildForRefresh terminates the live child (if any) ahead of an
// operator-driven refresh. The child's pending exit is flagged so
// dispatchChildExit drops it — the refresh path, not the exit path, owns the
// restart. No-op when no child is running.
func (l *Lifecycle) stopChildForRefresh() {
	l.childMu.Lock()
	child := l.child
	l.child = nil
	l.childMu.Unlock()
	if child == nil {
		return
	}
	l.suppressNextChildExit.Store(true)
	l.childRunning.Store(false)
	zeroTime := time.Time{}
	l.inputs.childStartedAt.Store(&zeroTime)
	_ = child.Forward(syscall.SIGTERM)
}

// markAllScopesStale marks every configured scope as stale on statusInputs.
func (l *Lifecycle) markAllScopesStale() {
	stale := append([]string(nil), l.config.Scope...)
	empty := []string{}
	l.inputs.scopeStale.Store(&stale)
	l.inputs.scopeHealthy.Store(&empty)
}

// markScopeStale removes one scope from healthy and adds it to stale.
func (l *Lifecycle) markScopeStale(scope string) {
	healthy := []string{}
	for _, s := range l.config.Scope {
		if s != scope {
			healthy = append(healthy, s)
		}
	}
	stale := []string{scope}
	l.inputs.scopeHealthy.Store(&healthy)
	l.inputs.scopeStale.Store(&stale)
}

// lineSplittingWriter is the io.Writer wrapper passed as ChildConfig.Stderr.
// It tees writes to the operator-supplied sink AND fans each emitted line
// to Watchdog.OnStderrLine. Lines longer than stderrLineCap are truncated
// to the first stderrLineCap bytes.
type lineSplittingWriter struct {
	ctx    context.Context //nolint:containedctx // captured at construction per spec
	sink   io.Writer
	wd     Watchdog
	logger *slog.Logger

	mu  sync.Mutex
	buf []byte
}

// newLineSplittingWriter constructs a writer that fans bytes to sink AND
// emits each completed line to wd.OnStderrLine. Captures ctx so calls
// observe cancellation.
func newLineSplittingWriter(ctx context.Context, sink io.Writer, wd Watchdog, logger *slog.Logger) *lineSplittingWriter {
	if sink == nil {
		sink = io.Discard
	}
	if wd == nil {
		wd = noopWatchdog{}
	}
	return &lineSplittingWriter{
		ctx:    ctx,
		sink:   sink,
		wd:     wd,
		logger: logger,
		buf:    make([]byte, 0, 1024),
	}
}

// Write tees p to sink and emits any newline-terminated lines to the
// watchdog. Always returns (len(p), nil) so it never blocks the child
// drain goroutine.
func (w *lineSplittingWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// Forward to the sink first.
	_, _ = w.sink.Write(p)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		idx := indexByte(w.buf, '\n')
		if idx < 0 {
			// Cap protection: drop the head if buf would exceed cap.
			if len(w.buf) > stderrLineCap {
				w.buf = w.buf[len(w.buf)-stderrLineCap:]
			}
			break
		}
		line := append([]byte(nil), w.buf[:idx]...)
		w.buf = w.buf[idx+1:]
		if len(line) > stderrLineCap {
			line = line[:stderrLineCap]
		}
		w.wd.OnStderrLine(w.ctx, line)
	}
	return len(p), nil
}

// indexByte returns the first index of c in b, or -1.
func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}
