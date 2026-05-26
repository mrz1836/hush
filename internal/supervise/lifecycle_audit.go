// Supervisor orchestration glue: audit emission helpers.
//
// lifecycle_audit.go binds the Lifecycle to the audit writer. Every
// helper constructs an audit.Event Data map and calls
// Deps.AuditWriter.Append with one of the 12 new ActionSupervisor* /
// ActionClientRefresh* constants declared in internal/audit/chain.go.
//
// Anti-contract: no Data key carries a *SecureBytes, a []byte sourced from
// secret material, or a string(secretBytes) value. All "reason" strings
// are drawn from a closed phrase map below.

package supervise

import (
	"context"
	"log/slog"
	"time"

	"github.com/mrz1836/hush/internal/audit"
)

// Closed reason / cause phrase map. Strings used in audit Data fields and
// AlertPayload.Reason. The orchestrator never derives reason strings from
// secret material — they are sentinel-class read-only labels (Constitution X).
const (
	causeValidator    = "validator"
	causeUnknownJTI   = "unknown_jti"
	causeExit78       = "exit_78"
	causeRefillFailed = "refill_failed"
	causeBootTimeout  = "boot_timeout"
	causeClaimDenied  = "claim_denied"

	errorClassTransient          = "transient"
	errorClassUnknownJTI         = "unknown_jti"
	errorClassDiscordUnavailable = "discord_unavailable"
	errorClassDeny               = "deny"
	errorClassTimeout            = "timeout"
	errorClassCancelled          = "cancelled"

	reasonValidatorRejected    = "validator rejected fetched secret"
	reasonExit78               = "child exit 78 stale credentials"
	reasonVaultRejectedJWT     = "vault rejected JWT (unknown jti)"
	reasonRefillFailed         = "refill failed; awaiting approval"
	reasonDiscordUnavailable   = "discord unavailable; retrying"
	reasonBootTimeout          = "boot preconditions did not recover in time"
	reasonRefreshDenied        = "refresh approval denied"
	reasonRefreshTimeout       = "refresh approval timed out"
	reasonGraceEntered         = "entered grace cache restart"
	reasonLogPatternMatch      = "log pattern matched"
	reasonClientRefreshInvoked = "client refresh invoked"

	// reasonReap* are the closed-set reason labels for the
	// supervisor_swap_candidate_reaped audit event emitted by
	// reapSwapCandidate. Drawn from a closed map so audit consumers can
	// switch on them without parsing free-form strings.
	reasonReapReadinessConfigMissing = "readiness_config_missing"
	reasonReapReadinessProbeFailed   = "readiness_probe_failed"
	reasonReapBackendSetFailed       = "backend_set_failed"
)

// emitSessionClaimed appends supervisor_session_claimed after a successful
// initial /claim + JWT persist. Data carries jti, session_type, exp (RFC3339),
// scope ([]string), outcome.
func (l *Lifecycle) emitSessionClaimed(ctx context.Context, jti string, exp time.Time, scope []string) {
	l.appendAudit(ctx, audit.ActionSupervisorSessionClaimed, map[string]any{
		"jti":          jti,
		"session_type": "supervisor",
		"exp":          exp.UTC().Format(time.RFC3339),
		"scope":        append([]string(nil), scope...),
		"outcome":      "approved",
	})
}

// emitSessionRefreshed appends supervisor_session_refreshed after a
// successful refresh-window claim swap.
func (l *Lifecycle) emitSessionRefreshed(ctx context.Context, jti, prevJTI string, exp time.Time) {
	l.appendAudit(ctx, audit.ActionSupervisorSessionRefreshed, map[string]any{
		"jti":      jti,
		"prev_jti": prevJTI,
		"exp":      exp.UTC().Format(time.RFC3339),
		"outcome":  "approved",
	})
}

// emitSilentRefill appends supervisor_silent_refill after a successful
// silent refill following clean exit OR crash.
func (l *Lifecycle) emitSilentRefill(ctx context.Context, scopes []string) {
	l.appendAudit(ctx, audit.ActionSupervisorSilentRefill, map[string]any{
		"scopes":  append([]string(nil), scopes...),
		"outcome": "ok",
	})
}

// emitChildCleanExit appends supervisor_child_clean_exit when Child.Wait
// returns exit code 0.
func (l *Lifecycle) emitChildCleanExit(ctx context.Context, pid int, uptime time.Duration) {
	l.appendAudit(ctx, audit.ActionSupervisorChildCleanExit, map[string]any{
		"child_pid": pid,
		"uptime":    uptime.String(),
	})
}

// emitChildExitCrash appends supervisor_child_exit_crash when Child.Wait
// returns a non-zero exit code other than Exit78.
func (l *Lifecycle) emitChildExitCrash(ctx context.Context, pid, code, sig int, uptime time.Duration) {
	data := map[string]any{
		"child_pid": pid,
		"exit_code": code,
		"uptime":    uptime.String(),
	}
	if sig != 0 {
		data["signal"] = sig
	}
	l.appendAudit(ctx, audit.ActionSupervisorChildExitCrash, data)
}

// emitChildExit78 appends supervisor_child_exit_78 when Child.Wait returns
// the Exit78 stale-credentials code.
func (l *Lifecycle) emitChildExit78(ctx context.Context, pid int, uptime time.Duration) {
	l.appendAudit(ctx, audit.ActionSupervisorChildExit78, map[string]any{
		"child_pid": pid,
		"uptime":    uptime.String(),
	})
}

// emitAwaitingApproval appends supervisor_awaiting_approval with the cause
// label. cause MUST be drawn from the closed cause* constants above.
func (l *Lifecycle) emitAwaitingApproval(ctx context.Context, cause string) {
	l.appendAudit(ctx, audit.ActionSupervisorAwaitingApproval, map[string]any{
		"cause": cause,
	})
}

// emitStaleAlert appends supervisor_stale_alert. class is AlertClass.String(),
// scope is the offending scope (never the secret), errorClass is from the
// closed errorClass* map.
func (l *Lifecycle) emitStaleAlert(ctx context.Context, class AlertClass, scope, errorClass string) {
	l.appendAudit(ctx, audit.ActionSupervisorStaleAlert, map[string]any{
		"class":       class.String(),
		"scope":       scope,
		"error_class": errorClass,
	})
}

// emitGraceEntered appends supervisor_grace_entered when grace-cache
// restart activates.
func (l *Lifecycle) emitGraceEntered(ctx context.Context, scopes []string, ttlRem time.Duration) {
	l.appendAudit(ctx, audit.ActionSupervisorGraceEntered, map[string]any{
		"scopes":              append([]string(nil), scopes...),
		"grace_ttl_remaining": ttlRem.String(),
	})
}

// emitGraceExited appends supervisor_grace_exited when grace-cache restart
// completes. outcome ∈ {"restart_ok", "refresh_window", "expired"}.
func (l *Lifecycle) emitGraceExited(ctx context.Context, scopes []string, outcome string) {
	l.appendAudit(ctx, audit.ActionSupervisorGraceExited, map[string]any{
		"scopes":  append([]string(nil), scopes...),
		"outcome": outcome,
	})
}

// emitBootTimeout appends supervisor_boot_timeout when boot_retry_timeout
// exhausts.
func (l *Lifecycle) emitBootTimeout(ctx context.Context, lastErrClass string) {
	l.appendAudit(ctx, audit.ActionSupervisorBootTimeout, map[string]any{
		"boot_retry_timeout": l.config.BootRetryTimeout.String(),
		"last_error_class":   lastErrClass,
	})
}

// emitChildSwap appends supervisor_child_swap after a successful HTTP-proxy
// reload swap. Data carries old_pid, new_pid, swap_completed_at,
// readiness_duration_ms, strategy. By construction this event never
// carries any secret/env value: only PIDs, an RFC3339 timestamp, a
// duration in ms, and the swap strategy string.
func (l *Lifecycle) emitChildSwap(ctx context.Context, oldPID, newPID int, readiness time.Duration, strategy string) {
	l.appendAudit(ctx, audit.ActionSupervisorChildSwap, map[string]any{
		"old_pid":               oldPID,
		"new_pid":               newPID,
		"swap_completed_at":     l.deps.NowFn().UTC().Format(time.RFC3339),
		"readiness_duration_ms": readiness.Milliseconds(),
		"strategy":              strategy,
	})
}

// emitSwapCandidateReaped appends supervisor_swap_candidate_reaped after
// reapSwapCandidate retires a candidate child following a swap-failure
// branch (readiness probe, missing readiness config, backend set failure).
// Data carries: candidate_pid (kernel id, non-secret), escalated_to_sigkill
// (whether SIGTERM grace expired before exit), ceiling_exceeded (whether
// the post-SIGKILL hard ceiling tripped), reap_duration_ms (wall-clock from
// SIGTERM to Wait return or ceiling), and reason (closed-set string from
// the reasonReap* constants above). No secret/env value can flow here by
// construction — reapSwapCandidate sees only PIDs and timestamps.
func (l *Lifecycle) emitSwapCandidateReaped(ctx context.Context, candidatePID int, escalated, ceilingExceeded bool, dur time.Duration, reason string) {
	l.appendAudit(ctx, audit.ActionSupervisorSwapCandidateReaped, map[string]any{
		"candidate_pid":        candidatePID,
		"escalated_to_sigkill": escalated,
		"ceiling_exceeded":     ceilingExceeded,
		"reap_duration_ms":     dur.Milliseconds(),
		"reason":               reason,
	})
}

// emitClientRefreshInvoked appends client_refresh_invoked when the
// status-socket refresh verb is consumed.
func (l *Lifecycle) emitClientRefreshInvoked(ctx context.Context, state, outcome string) {
	l.appendAudit(ctx, audit.ActionClientRefreshInvoked, map[string]any{
		"state":   state,
		"outcome": outcome,
	})
}

// appendAudit is the single producer-side wrapper around the audit writer.
// Append failures are logged but do not stop the orchestrator (audit chain
// integrity is guarded by the writer's own contract).
func (l *Lifecycle) appendAudit(ctx context.Context, action string, data map[string]any) {
	if err := l.deps.AuditWriter.Append(ctx, action, data); err != nil {
		l.deps.Logger.Warn("supervise: audit append failed",
			slog.String("action", action), slog.Any("err", err))
	}
}

// alertReasonFor returns the closed Reason phrase for an AlertClass.
//
//nolint:gocyclo // closed-set enum dispatch over 10 LOCKED AlertClass values
func alertReasonFor(class AlertClass) string {
	switch class {
	case AlertClassValidatorFailure:
		return reasonValidatorRejected
	case AlertClassExit78:
		return reasonExit78
	case AlertClassVaultRejectedJWT:
		return reasonVaultRejectedJWT
	case AlertClassRefillFailed:
		return reasonRefillFailed
	case AlertClassDiscordUnavailableOnClaim:
		return reasonDiscordUnavailable
	case AlertClassRefreshDenied:
		return reasonRefreshDenied
	case AlertClassRefreshTimeout:
		return reasonRefreshTimeout
	case AlertClassGraceEntered:
		return reasonGraceEntered
	case AlertClassLogPatternMatch:
		return reasonLogPatternMatch
	case AlertClassBootTimeout:
		return reasonBootTimeout
	}
	return ""
}
