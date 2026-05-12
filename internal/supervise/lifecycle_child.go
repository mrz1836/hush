// SDD-24 — Supervisor orchestration glue: child env / start / wait / exit dispatch.
//
// lifecycle_child.go owns the initial child env build from Grace, the
// validator pass, NewChild + Start, the childWaitLoop goroutine, the
// childExit dispatch (0 / non-zero non-Exit78 / Exit78 — referencing
// SDD-20's Exit78 constant, never the raw literal), and the silent refill
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
	"time"
)

// initialRefillAndStart performs the boot-time Refiller.Refill → validators
// → child env build → Child.Start sequence. Returns nil on entry to mainLoop
// with the child running.
func (l *Lifecycle) initialRefillAndStart(ctx context.Context) error {
	if err := l.refiller.Refill(ctx, l.config.Scope); err != nil {
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
		// Boot-time generic refill failure (spec FR-026-010a boot-time branch).
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
	// and transitioned to StateAwaitingApproval; the err is informational.
	_ = l.validateAllScopes(ctx)
	if snap := l.store.Snapshot(); snap.State == StateAwaitingApproval {
		return nil
	}

	if err := l.startChild(ctx); err != nil {
		return err
	}
	l.transition(ctx, EventFetchOK)
	return nil
}

// validateAllScopes runs every configured Validator once per scope. On any
// non-nil return, emits AlertClassValidatorFailure with the scope name,
// appends supervisor_awaiting_approval(cause=validator) +
// supervisor_stale_alert(class=ValidatorFailure), transitions to
// StateAwaitingApproval, and returns ErrValidatorFailed.
func (l *Lifecycle) validateAllScopes(ctx context.Context) error {
	for _, scope := range l.config.Scope {
		v := l.lookupValidator(scope)
		sb, ok := l.grace.Get(scope)
		if !ok || sb == nil {
			// Refill succeeded but Grace didn't cache (cache disabled).
			// Skip the validator for this scope — the no-op default semantics
			// still apply, and the secret will be re-fetched on restart.
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

// startChild builds ChildConfig.Env from Grace-resident secrets, instantiates
// the Child, calls Start(ctx), spawns childWaitLoop, and updates inputs.
//
// Constitution X: this is the ONE permitted `string(*SecureBytes)` site
// introduced by SDD-24 — the OS fork boundary. The env slice is zeroed
// after Start returns (FR-026-008).
func (l *Lifecycle) startChild(ctx context.Context) error {
	env := append([]string(nil), l.config.Child.EnvPassthrough...)
	for _, scope := range l.config.Scope {
		sb, ok := l.grace.Get(scope)
		if !ok || sb == nil {
			return fmt.Errorf("%w: %q (enable cache_secrets_for_restart)", errGraceEmptyScope, scope)
		}
		var added bool
		if useErr := sb.Use(func(b []byte) {
			// FR-026-008 / FR-026-028: single permitted string(*SecureBytes)
			// site at the OS-execve fork boundary. Mirrors SDD-20 Env []string.
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

	// Zero env slice after Start (FR-026-008). Drop reference; GC takes the rest.
	for i := range env {
		env[i] = ""
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

// dispatchChildExit branches on the exit code per FR-026-009:
//   - 0           → emit clean_exit → silent refill + restart
//   - !=0 && !=78 → emit crash      → silent refill + restart
//   - Exit78      → emit exit_78    → stale alert + StateAwaitingApproval
//
// The orchestrator references SDD-20's Exit78 constant — never the raw
// `78` literal (FR-026-023).
func (l *Lifecycle) dispatchChildExit(ctx context.Context, exit childExit) {
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
// re-runs validators, re-builds env, re-instantiates Child + Start.
// Spawns a fresh childWaitLoop. Per FR-026-010 / FR-026-010a:
//   - errors.Is(err, ErrJTIUnknown) → AlertClassVaultRejectedJWT + awaiting-approval
//   - any other error              → AlertClassRefillFailed + awaiting-approval
func (l *Lifecycle) silentRefillAndRestart(ctx context.Context) error {
	if err := l.refiller.Refill(ctx, l.config.Scope); err != nil {
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

	if err := l.validateAllScopes(ctx); err != nil {
		return err
	}
	if err := l.startChild(ctx); err != nil {
		l.deps.Logger.Warn("supervise: child restart failed", slog.Any("err", err))
		l.transition(ctx, EventClaimUnavailable)
		return err
	}
	l.emitSilentRefill(ctx, l.config.Scope)
	l.transition(ctx, EventFetchOK)
	return nil
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
// watchdog. Always returns (len(p), nil) so it never blocks the SDD-20
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
