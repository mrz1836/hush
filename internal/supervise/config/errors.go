package config

import "errors"

// Sentinel error catalog. Every documented rejection category from the spec
// maps to exactly one sentinel; errors.Is is the only matching primitive.
// Sentinel error messages are static category strings; no message includes
// any byte read from the TOML file beyond the field NAME or the validator
// TYPE NAME.
var (
	// Decode-phase errors.
	ErrTOMLDecode           = errors.New("hush/supervise/config: TOML decode failed")
	ErrUnknownField         = errors.New("hush/supervise/config: unknown field")
	ErrMissingRequiredField = errors.New("hush/supervise/config: missing required field")
	ErrInvalidDuration      = errors.New("hush/supervise/config: invalid duration")

	// Validator allow-list.
	ErrUnknownValidator = errors.New("hush/supervise/config: unknown validator")

	// Grace-cache errors.
	ErrGraceWindowTooLong   = errors.New("hush/supervise/config: grace window exceeds 4h cap")
	ErrGraceTTLWithoutCache = errors.New("hush/supervise/config: cache_grace_ttl set but cache_secrets_for_restart is false")

	// Standing-lease errors.
	ErrStandingLeaseNeedsMachineIndex = errors.New("hush/supervise/config: standing_lease requires a non-zero client_machine_index")

	// Refresh-window errors.
	ErrRefreshWindowFormat = errors.New("hush/supervise/config: refresh_window must be HH:MM-HH:MM")
	ErrRefreshWindowOrder  = errors.New("hush/supervise/config: refresh_window start must be earlier than end")

	// Reseal schedule errors.
	ErrResealTimezoneInvalid = errors.New("hush/supervise/config: reseal.timezone must be a valid IANA location")
	ErrResealTimezoneMissing = errors.New("hush/supervise/config: reseal.timezone is required")
	ErrResealTimeFormat      = errors.New("hush/supervise/config: reseal time must be HH:MM")
	ErrResealTimeMissing     = errors.New("hush/supervise/config: reseal.daily_time is required")
	ErrResealWeekdayInvalid  = errors.New("hush/supervise/config: reseal override weekday is invalid")

	// Child-command errors.
	ErrCommandEmpty        = errors.New("hush/supervise/config: child.command must be a non-empty array")
	ErrCommandPathRelative = errors.New("hush/supervise/config: child.command first element must be an absolute path")

	// Misc value-range errors.
	ErrScopeEmpty                = errors.New("hush/supervise/config: scope must be a non-empty array")
	ErrSessionTypeInvalid        = errors.New(`hush/supervise/config: session_type must be "supervisor"`)
	ErrRequestedTTLOutOfRange    = errors.New("hush/supervise/config: requested_ttl exceeds 24h ceiling")
	ErrServerURLInvalid          = errors.New("hush/supervise/config: server_url must parse with http/https scheme and non-empty host")
	ErrLogLevelInvalid           = errors.New("hush/supervise/config: log_level must be one of debug, info, warn, error")
	ErrWatchdogRateInvalid       = errors.New("hush/supervise/config: watchdog.max_alerts_per_hour must be > 0")
	ErrPathNotClean              = errors.New("hush/supervise/config: path must be lexically clean (no .., duplicate slashes, or trailing /.)")
	ErrBootRetryTimeoutTooLong   = errors.New("hush/supervise/config: boot_retry_timeout exceeds 1h cap")
	ErrRefreshNudgeBeforeTooLong = errors.New("hush/supervise/config: refresh_nudge_before exceeds 6h cap")

	// Reload eligibility errors. The reload-eligible config shape is
	// [child.readiness] + [child.handoff] mode = "http-proxy" with a
	// child command/env that consumes HUSH_BIND_PORT. Each rejection
	// category has exactly one sentinel; AC-3 / AC-7 / AC-8 in T-306.
	ErrReadinessURLInvalid        = errors.New("hush/supervise/config: child.readiness.http_url must parse with http/https scheme and non-empty host")
	ErrReadinessDurationInvalid   = errors.New("hush/supervise/config: child.readiness timeout/interval must be > 0")
	ErrShutdownGraceInvalid       = errors.New("hush/supervise/config: child.shutdown.grace must be > 0")
	ErrHandoffModeInvalid         = errors.New(`hush/supervise/config: child.handoff.mode must be "http-proxy"`)
	ErrHandoffRequiresReadiness   = errors.New("hush/supervise/config: child.handoff requires [child.readiness] for zero-downtime reload")
	ErrHandoffRequiresBindPortRef = errors.New("hush/supervise/config: child.handoff requires child.command or child.env to reference HUSH_BIND_PORT so the child binds the hush-allocated backend port")

	// errNilSupervisor is returned by Supervisor.Validate when called on a
	// nil receiver. Wraps ErrMissingRequiredField so the existing
	// errors.Is gate keeps working.
	errNilSupervisor = errors.New("hush/supervise/config: nil *Supervisor")
)
