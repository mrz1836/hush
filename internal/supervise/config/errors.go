package config

import "errors"

// Sentinel error catalog. Every documented rejection category from the spec
// maps to exactly one sentinel; errors.Is is the only matching primitive.
// Sentinel error messages are static category strings; no message includes
// any byte read from the TOML file beyond the field NAME or the validator
// TYPE NAME (FR-014 + FR-020 + Constitution X).
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

	// Refresh-window errors.
	ErrRefreshWindowFormat = errors.New("hush/supervise/config: refresh_window must be HH:MM-HH:MM")
	ErrRefreshWindowOrder  = errors.New("hush/supervise/config: refresh_window start must be earlier than end")

	// Child-command errors.
	ErrCommandEmpty        = errors.New("hush/supervise/config: child.command must be a non-empty array")
	ErrCommandPathRelative = errors.New("hush/supervise/config: child.command first element must be an absolute path")

	// Misc value-range errors.
	ErrScopeEmpty             = errors.New("hush/supervise/config: scope must be a non-empty array")
	ErrSessionTypeInvalid     = errors.New(`hush/supervise/config: session_type must be "supervisor"`)
	ErrRequestedTTLOutOfRange = errors.New("hush/supervise/config: requested_ttl exceeds 24h ceiling")
	ErrServerURLInvalid       = errors.New("hush/supervise/config: server_url must parse with http/https scheme and non-empty host")
	ErrLogLevelInvalid        = errors.New("hush/supervise/config: log_level must be one of debug, info, warn, error")
	ErrWatchdogRateInvalid    = errors.New("hush/supervise/config: watchdog.max_alerts_per_hour must be > 0")

	// errNilSupervisor is returned by Supervisor.Validate when called on a
	// nil receiver. Wraps ErrMissingRequiredField so the existing
	// errors.Is gate keeps working.
	errNilSupervisor = errors.New("hush/supervise/config: nil *Supervisor")
)
