package config

import (
	"errors"
	"fmt"
	"io/fs"
)

// Decode-phase errors.
var (
	ErrTOMLDecode           = errors.New("hush/config: TOML decode failed")
	ErrUnknownField         = errors.New("hush/config: unknown field")
	ErrMissingRequiredField = errors.New("hush/config: missing required field")
	ErrInvalidDuration      = errors.New("hush/config: invalid duration")
)

// Network/listen-addr errors. The loopback/unspecified/public sentinels each
// wrap ErrTailscaleBindRequired so errors.Is(err, ErrTailscaleBindRequired)
// matches any of them.
var (
	ErrTailscaleBindRequired = errors.New("hush/config: Tailscale bind required (100.64.0.0/10)")
	ErrListenLoopback        = fmt.Errorf("hush/config: listen address is loopback: %w", ErrTailscaleBindRequired)
	ErrListenUnspecified     = fmt.Errorf("hush/config: listen address is unspecified: %w", ErrTailscaleBindRequired)
	ErrListenPublic          = fmt.Errorf("hush/config: listen address is not in Tailscale CGNAT: %w", ErrTailscaleBindRequired)
	ErrListenMalformed       = errors.New("hush/config: listen address is malformed")
	ErrTailscaleRequired     = errors.New("hush/config: require_tailscale must be true (v0.1.0)")
)

// Path-safety errors.
var (
	ErrPathPrefixInvalid = errors.New("hush/config: path_prefix invalid (must be 6-32 chars, [A-Za-z0-9_-])")
	ErrAuditLogEscape    = errors.New("hush/config: audit_log resolves outside state_dir")
	// ErrStateDirNotFound wraps fs.ErrNotExist so errors.Is(err, fs.ErrNotExist) is true.
	ErrStateDirNotFound = fmt.Errorf("hush/config: state_dir does not exist: %w", fs.ErrNotExist)
	ErrStateDirUnsafe   = errors.New("hush/config: state_dir is not a directory")
)

// Crypto-floor errors — Constitution III.
var (
	ErrArgonMemoryTooLow  = errors.New("hush/config: argon_memory_mb below floor (256 MiB)")
	ErrArgonTimeTooLow    = errors.New("hush/config: argon_time below floor (4)")
	ErrArgonThreadsTooLow = errors.New("hush/config: argon_threads below floor (4)")
)

// TTL-bound error.
var ErrSupervisorTTLOutOfRange = errors.New("hush/config: max_supervisor_ttl out of range (must be > jwt_default_ttl and ≤ 24h)")
