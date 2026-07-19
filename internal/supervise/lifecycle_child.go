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
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/mrz1836/hush/internal/supervise/config"
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
	// When [child.handoff] is configured and a proxy is attached, the
	// freshly spawned child is bound on a private port — the public
	// listener is the proxy. Probe readiness + point the proxy at the
	// new backend; on failure the child is terminated to avoid an
	// orphan behind a dead-but-bound public socket.
	if err := l.promoteChildToProxy(ctx); err != nil {
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

// buildChildEnv assembles the child env []string from four layered sources,
// lowest priority first:
//  1. EnvPassthrough: named vars copied from the supervisor's own os.Environ()
//     — only keys present in the supervisor env land here.
//  2. Env: explicit KEY=VALUE pairs declared in the supervisor config.
//  3. overlay: non-secret KEY=VALUE pairs injected by the orchestrator (e.g.
//     HUSH_BIND_PORT for reload-eligible configs). Overlay keys whose name
//     matches a configured scope are silently dropped — the vault is the
//     source of truth for any scope-named env var, and an overlay must not
//     mask it.
//  4. Scope: per-scope secrets fetched from the vault.
//
// Later layers overwrite earlier layers on key collision. This is the ONE
// permitted `string(*SecureBytes)` site — the OS fork boundary. The caller
// owns zeroing the returned slice once the child has been started (or the
// start path errors out).
//
// The overlay is "non-secret" by contract: callers MUST only put values
// here that are safe to surface in logs or audit events (port numbers,
// listener addresses, strategy strings). Scope secrets always flow through
// the secretSet path; they are never read through the overlay.
func (l *Lifecycle) buildChildEnv(secrets secretSet, overlay map[string]string) ([]string, error) {
	capHint := len(l.config.Child.EnvPassthrough) + len(l.config.Child.Env) + len(overlay) + len(l.config.Scope)
	env := make([]string, 0, capHint)
	seen := make(map[string]int, capHint)
	upsertEnv := func(key, value string) {
		entry := key + "=" + value
		if idx, ok := seen[key]; ok {
			env[idx] = entry
			return
		}
		seen[key] = len(env)
		env = append(env, entry)
	}
	for _, key := range l.config.Child.EnvPassthrough {
		if value, ok := os.LookupEnv(key); ok {
			upsertEnv(key, value)
		}
	}
	for key, value := range l.config.Child.Env {
		upsertEnv(key, value)
	}
	applyEnvOverlay(overlay, l.config.Scope, upsertEnv)
	for _, scope := range l.config.Scope {
		if err := appendScopeEnv(secrets, scope, upsertEnv); err != nil {
			return env, err
		}
	}
	return env, nil
}

// applyEnvOverlay feeds the non-secret overlay into upsert, skipping any
// key that collides with a configured scope name. Scope-name collisions
// are dropped silently because the vault MUST remain the source of truth
// for any scope-named env var, and an overlay is by contract non-secret —
// allowing it to mask a scope would let a logged overlay value substitute
// for a vault-backed secret.
func applyEnvOverlay(overlay map[string]string, scope []string, upsert func(key, value string)) {
	if len(overlay) == 0 {
		return
	}
	scopeNames := make(map[string]struct{}, len(scope))
	for _, name := range scope {
		scopeNames[name] = struct{}{}
	}
	for key, value := range overlay {
		if _, isScope := scopeNames[key]; isScope {
			continue
		}
		upsert(key, value)
	}
}

// appendScopeEnv resolves one scope's plaintext from secrets and feeds it to
// upsert. Scope wins over EnvPassthrough/Env on key collision — the vault is
// the source of truth for any name in scope.
func appendScopeEnv(secrets secretSet, scope string, upsert func(key, value string)) error {
	sb := secrets[scope]
	if sb == nil {
		return fmt.Errorf("%w: scope %q", errEnvBuildScope, scope)
	}
	var added bool
	if useErr := sb.Use(func(b []byte) {
		// Single permitted string(*SecureBytes) site at the
		// OS-execve fork boundary. Mirrors Child's Env []string.
		upsert(scope, string(b))
		added = true
	}); useErr != nil {
		return fmt.Errorf("supervise: env build: %w", useErr)
	}
	if !added {
		return fmt.Errorf("%w: scope %q", errEnvBuildScope, scope)
	}
	return nil
}

// buildChildEnvOverlay returns the non-secret env overlay the orchestrator
// injects into the child env on top of the operator's EnvPassthrough/Env.
// Returns an empty map (never nil) when the config has not opted into
// reload-eligibility — non-reload configs see the same env shape they
// always have.
//
// For reload-eligible configs ([child.handoff] mode = "http-proxy"), the
// overlay carries HUSH_BIND_PORT, set to a freshly allocated loopback port.
// The port is also recorded on Lifecycle.backendPort so Phase 5's proxy
// can target it without re-allocating.
//
// The overlay MUST contain only non-secret values — its contents are safe
// to surface in audit events. Vault-backed scope secrets flow through the
// secretSet path in buildChildEnv, never through this overlay.
func (l *Lifecycle) buildChildEnvOverlay(ctx context.Context) (map[string]string, error) {
	overlay := map[string]string{}
	if l.config.Child.Handoff == nil || l.config.Child.Handoff.Mode != config.HandoffModeHTTPProxy {
		return overlay, nil
	}
	port, err := AllocateBackendPort(ctx)
	if err != nil {
		return nil, fmt.Errorf("supervise: child env overlay: %w", err)
	}
	l.backendMu.Lock()
	l.backendPort = port
	l.backendMu.Unlock()
	overlay[config.EnvVarBindPort] = strconv.FormatUint(uint64(port), 10)
	return overlay, nil
}

// startChild builds ChildConfig.Env from the supplied per-scope plaintext,
// instantiates the Child, calls Start(ctx), spawns childWaitLoop, and updates
// inputs. The secrets are borrowed — startChild neither retains nor destroys
// them.
//
// When [child.handoff] is configured, startChild allocates a private
// loopback backend port and injects it as HUSH_BIND_PORT via the
// non-secret env overlay. The allocated port is recorded on the Lifecycle
// so Phase 5's proxy can forward traffic to it.
//
// This is the ONE permitted `string(*SecureBytes)` site — the OS fork
// boundary. The env slice is zeroed after Start returns.
func (l *Lifecycle) startChild(ctx context.Context, secrets secretSet) error {
	overlay, err := l.buildChildEnvOverlay(ctx)
	if err != nil {
		return err
	}
	env, err := l.buildChildEnv(secrets, overlay)
	// Zero the env slice on every exit path — success, every
	// error return below, and any panic that unwinds through this frame.
	// Child.Start makes its own defensive copy of the slice into cmd.Env,
	// so wiping the parent's view does not blank the child's environment.
	defer func() {
		for i := range env {
			env[i] = ""
		}
	}()
	if err != nil {
		return err
	}

	stdoutSink, stdoutCloser, stderrSink, stderrCloser, err := l.openChildSinks()
	if err != nil {
		return err
	}
	// On any error return below, close the file handles we just opened.
	// On the successful path, the file handles outlive startChild — the
	// childWaitLoop closes them after Wait returns.
	successfulStart := false
	defer func() {
		if successfulStart {
			return
		}
		closeIfNotNil(stdoutCloser)
		closeIfNotNil(stderrCloser)
	}()
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
	successfulStart = true

	now := l.deps.NowFn()
	l.childMu.Lock()
	l.child = child
	l.childStarted = now
	l.childMu.Unlock()
	l.inputs.childStartedAt.Store(&now)
	l.childRunning.Store(true)

	l.wg.Add(1)
	go l.childWaitLoop(ctx, child, stdoutCloser, stderrCloser)
	return nil
}

// openChildSinks opens both the stdout and stderr sinks for the child. On a
// stderr-sink error the already-opened stdout closer is closed before
// returning so the caller does not leak the descriptor.
//
// Routing rules per sink:
//   - StdoutPath / StderrPath set → open file (append mode, 0600). Operators
//     get a stable on-disk view of child output independent of the
//     supervisor's own stdout, which is useful when the supervisor itself is
//     logged elsewhere (e.g. launchd StandardOutPath).
//   - Empty → inherit the supervisor process's stdout/stderr. This is the
//     "do something useful by default" choice: under launchd that surfaces
//     in the supervisor's StandardOutPath / StandardErrorPath rather than
//     vanishing into io.Discard.
//
// Either way the stderr stream still feeds the watchdog line splitter so
// pattern-based alerts continue to fire.
func (l *Lifecycle) openChildSinks() (io.Writer, io.Closer, io.Writer, io.Closer, error) {
	stdoutSink, stdoutCloser, err := openChildSink(l.config.Child.StdoutPath, os.Stdout)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("supervise: child stdout sink: %w", err)
	}
	stderrSink, stderrCloser, err := openChildSink(l.config.Child.StderrPath, os.Stderr)
	if err != nil {
		closeIfNotNil(stdoutCloser)
		return nil, nil, nil, nil, fmt.Errorf("supervise: child stderr sink: %w", err)
	}
	return stdoutSink, stdoutCloser, stderrSink, stderrCloser, nil
}

// closeIfNotNil closes c when it is non-nil and discards the error. Used in
// the child-sink cleanup paths where the file descriptor must be released but
// any close error is non-actionable.
func closeIfNotNil(c io.Closer) {
	if c == nil {
		return
	}
	_ = c.Close()
}

// openChildSink returns the io.Writer to use as a child stdout/stderr sink
// plus an optional io.Closer the caller must close once the child exits.
// When path is non-empty the file is opened in append mode with mode 0600;
// the closer is the *os.File. When path is empty the supplied fallback
// (typically os.Stdout / os.Stderr) is used as-is with no closer — the
// process owns those handles.
func openChildSink(path string, fallback *os.File) (io.Writer, io.Closer, error) {
	if path == "" {
		return fallback, nil, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // path is operator-configured and validated upstream
	if err != nil {
		return nil, nil, err
	}
	return f, f, nil
}

// childWaitLoop is owned by Lifecycle.wg; invokes Child.Wait once and sends
// the result on childExitCh. Top-frame recover per Constitution IX.
// The stdoutCloser / stderrCloser handles (when non-nil) own dedicated
// child-sink files and MUST be closed once Wait returns so the file
// descriptors are released on every restart.
func (l *Lifecycle) childWaitLoop(ctx context.Context, child *Child, stdoutCloser, stderrCloser io.Closer) {
	defer l.wg.Done()
	defer func() {
		closeIfNotNil(stdoutCloser)
		closeIfNotNil(stderrCloser)
	}()
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
// transition first) and with no live child. On a refill failure the
// response depends on the error class:
//   - errors.Is(err, ErrJTIUnknown) → AUTHORITATIVE revoke. The grace
//     cache is fully evicted (its plaintext was materialized under the
//     now-revoked session and reusing it would silently bypass the
//     operator's revoke). Emit AlertClassVaultRejectedJWT + transition
//     to StateAwaitingApproval. NO grace-cache restart.
//   - any other error               → TRANSIENT (network / vault 5xx /
//     decrypt). The operator's prior approval still stands; if grace
//     is enabled and every scope is still cached, restart the child
//     from cache (docs §9). Otherwise emit AlertClassRefillFailed +
//     transition to StateAwaitingApproval.
func (l *Lifecycle) silentRefillAndRestart(ctx context.Context) error {
	secrets, err := l.refiller.Refill(ctx, l.config.Scope)
	if err != nil {
		if errors.Is(err, ErrJTIUnknown) {
			// Authoritative revoke: invalidate every cached plaintext
			// from the now-revoked session before paging. Layer 6 +
			// Principle VI — the operator's revoke MUST NOT be silently
			// bypassed by grace-cache fallback.
			l.grace.EvictAll()
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
		// Transient failure: grace-cache restart is permitted (docs §9 —
		// when the operator opted into cache_secrets_for_restart, a
		// refill failure restarts the child from the last-known-good
		// plaintext instead of paging).
		if l.tryGraceRestart(ctx) {
			return nil
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
	// Re-point the reload proxy at the freshly restarted child's new
	// backend port (every startChild allocates a fresh port via the
	// overlay). Without this the proxy keeps serving 503 against a
	// dead backend after every crash recovery.
	if perr := l.promoteChildToProxy(ctx); perr != nil {
		destroySecrets(secrets)
		l.deps.Logger.Warn("supervise: child restart promote failed", slog.Any("err", perr))
		l.transition(ctx, EventClaimUnavailable)
		return perr
	}
	l.retainSecrets(secrets)
	// Recovery succeeded: secrets were re-fetched, validated, and the child
	// restarted with them. Clear the stale markers left by the failure that
	// triggered this recovery (see markAllScopesHealthy) so the status socket
	// reports healthy — otherwise an approval-driven recovery surfaces a false
	// DEGRADED until the next cold boot.
	l.markAllScopesHealthy()
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

// markAllScopesHealthy marks every configured scope as healthy on
// statusInputs and clears the stale list — the inverse of markAllScopesStale.
// It mirrors the health reset applyClaimResponse performs on the boot/claim
// path so that an approval-driven recovery (renew/refresh →
// silentRefillAndRestart) reports the same clean status a cold boot would.
//
// Without it, a successful refill+validate+restart leaves the pre-recovery
// stale markers in place, surfacing a false DEGRADED on the status socket
// until the next supervisor cold boot: the renew/refresh path never routes
// through applyClaimResponse, so nothing else clears them.
func (l *Lifecycle) markAllScopesHealthy() {
	healthy := append([]string(nil), l.config.Scope...)
	empty := []string{}
	l.inputs.scopeHealthy.Store(&healthy)
	l.inputs.scopeStale.Store(&empty)
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
