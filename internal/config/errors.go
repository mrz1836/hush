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

// Crypto-ceiling errors — DoS-via-config prevention. Looser misconfig
// (e.g. argon_memory_mb=1000000) would OOM the process at first KDF call.
var (
	ErrArgonMemoryTooHigh  = errors.New("hush/config: argon_memory_mb above ceiling (4096 MiB)")
	ErrArgonTimeTooHigh    = errors.New("hush/config: argon_time above ceiling (16)")
	ErrArgonThreadsTooHigh = errors.New("hush/config: argon_threads above ceiling (128)")
)

// TTL-bound error.
var ErrSupervisorTTLOutOfRange = errors.New("hush/config: max_supervisor_ttl out of range (must be > jwt_default_ttl and ≤ 24h)")

// File-permissions error. Surfaced when require_file_mode_checks is true and
// the config file's own permissions are looser than 0600.
var ErrConfigFileMode = errors.New("hush/config: config file permissions must be 0600")
