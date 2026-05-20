// Supervisor orchestration glue: consumer-defined interfaces.
//
// lifecycle_interfaces.go declares the three single-method interfaces the
// orchestrator consumes (Validator, Alerts, Watchdog), the closed
// AlertClass enum (10 values LOCKED), the AlertPayload struct (3 string
// fields — structurally cannot carry secret bytes), and the no-op default
// implementations. No business logic lives in this file. No init() and
// no package-level mutable vars are introduced.
//
// The validators, watchdog, and alerts packages supply concrete
// implementations that satisfy these interfaces; the orchestrator hosts
// the hooks via Deps.Validators / Deps.Alerts / Deps.Watchdog with the
// no-op defaults wired automatically.

package supervise

import (
	"context"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Validator validates one freshly-fetched (or Grace-resident) secret for
// one scope. The orchestrator calls Validate(ctx, scope, *SecureBytes)
// once per scope before each child start, and once per scope when
// re-fetching after a silent refill.
//
// Implementations MUST:
//   - Use secret.Use(func(b []byte) { ... }) to access plaintext.
//   - Return nil on success.
//   - Return a wrapped error on failure; the wrapper MUST name the
//     scope but MUST NOT include the secret value (Constitution X).
//
// The validators package supplies the v0.1.0 builtins (anthropic,
// anthropic-oauth, openai, google-ai, github). This file ships only the
// no-op default.
type Validator interface {
	Validate(ctx context.Context, scope string, secret *securebytes.SecureBytes) error
}

// Alerts is the operator-visible alert sink. The orchestrator calls
// Emit at the documented sites (see AlertClass below). Each Emit call
// is synchronous from the orchestrator's perspective; implementations
// MUST NOT block. The alerts package supplies the rendering layer.
type Alerts interface {
	Emit(ctx context.Context, class AlertClass, payload AlertPayload)
}

// Watchdog observes child stderr lines. Alert-only — MUST NOT influence
// state-machine transitions. The watchdog package supplies the pattern
// engine.
type Watchdog interface {
	OnStderrLine(ctx context.Context, line []byte)
}

// AlertClass is the closed enum of orchestrator-emitted alert classes.
// LOCKED at exactly 10 values. The alerts package MUST NOT extend the
// enum without a spec amendment.
type AlertClass int

// AlertClass enum values. iota+1 leaves the zero value invalid so an
// uninitialized payload cannot accidentally emit as ValidatorFailure.
const (
	AlertClassValidatorFailure AlertClass = iota + 1
	AlertClassExit78
	AlertClassVaultRejectedJWT
	AlertClassRefillFailed
	AlertClassDiscordUnavailableOnClaim
	AlertClassRefreshDenied
	AlertClassRefreshTimeout
	AlertClassGraceEntered
	AlertClassLogPatternMatch
	AlertClassBootTimeout
)

// String returns the locked human-readable form of c. Names feed
// AlertPayload.Reason, audit event Data.class, and the alerts renderer.
//
//nolint:gocyclo // closed-set enum dispatch over 10 LOCKED AlertClass values
func (c AlertClass) String() string {
	switch c {
	case AlertClassValidatorFailure:
		return "ValidatorFailure"
	case AlertClassExit78:
		return "Exit78"
	case AlertClassVaultRejectedJWT:
		return "VaultRejectedJWT"
	case AlertClassRefillFailed:
		return "RefillFailed"
	case AlertClassDiscordUnavailableOnClaim:
		return "DiscordUnavailableOnClaim"
	case AlertClassRefreshDenied:
		return "RefreshDenied"
	case AlertClassRefreshTimeout:
		return "RefreshTimeout"
	case AlertClassGraceEntered:
		return "GraceEntered"
	case AlertClassLogPatternMatch:
		return "LogPatternMatch"
	case AlertClassBootTimeout:
		return "BootTimeout"
	}
	return "Unknown"
}

// AlertPayload carries the non-secret labels accompanying every
// Alerts.Emit call. Structurally cannot carry secret bytes — every field
// is a string and the orchestrator never sources any field from secret
// material (Constitution X).
type AlertPayload struct {
	// Scope is the failed scope name (e.g. "ANTHROPIC_API_KEY") or "" when N/A.
	Scope string
	// ErrorClass is the coarse error class from a closed set:
	// "transient" | "unknown_jti" | "discord_unavailable" | "deny" |
	// "timeout" | "cancelled" | "".
	ErrorClass string
	// Reason is a human-readable phrase drawn from a closed phrase map
	// inside lifecycle_audit.go.
	Reason string
}

// noopValidator returns nil for every Validate call. Wired automatically
// when Deps.Validators[scope] is missing or nil.
type noopValidator struct{}

// Validate always returns nil — the no-op contract.
func (noopValidator) Validate(context.Context, string, *securebytes.SecureBytes) error {
	return nil
}

// noopAlerts discards every Emit call. Wired automatically when
// Deps.Alerts is nil.
type noopAlerts struct{}

// Emit discards the call.
func (noopAlerts) Emit(context.Context, AlertClass, AlertPayload) {}

// noopWatchdog discards every OnStderrLine call. Wired automatically
// when Deps.Watchdog is nil.
type noopWatchdog struct{}

// OnStderrLine discards the call.
func (noopWatchdog) OnStderrLine(context.Context, []byte) {}

// Compile-time guards.
var (
	_ Validator = noopValidator{}
	_ Alerts    = noopAlerts{}
	_ Watchdog  = noopWatchdog{}
)
