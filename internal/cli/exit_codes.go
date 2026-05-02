package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/sign"
	"github.com/mrz1836/hush/internal/vault"
)

// Exit codes form the public CLI contract. Operators script against
// these values; their numeric stability is part of the contract
// (FR-005, FR-006).
const (
	// ExitOK indicates a clean completion.
	ExitOK = 0
	// ExitErr is the catch-all for network failures, server 5xx,
	// partial-health (`health`), panic recovery, and any error not
	// mapped to a more specific code.
	ExitErr = 1
	// ExitInputErr indicates an operator-input error: missing flag,
	// conflicting flags, malformed --jti, unreadable config, or no
	// passphrase source on `serve`.
	ExitInputErr = 2
	// ExitAuth indicates an authentication failure: bad passphrase,
	// signature rejected, JWT rejected.
	ExitAuth = 3
	// ExitNotFound indicates the named entity does not exist:
	// unknown --jti, config file not found.
	ExitNotFound = 4
	// ExitPerm indicates an OS-level permission denied: cannot bind
	// the configured port, cannot read the vault file due to mode.
	ExitPerm = 5
	// ExitConfigStale is the EX_CONFIG sysexits sentinel reserved for
	// the future supervisor↔child contract (SDD-15/SDD-23). It is
	// declared here so the public symbol is stable, but is NEVER
	// raised by any subcommand in this chunk.
	ExitConfigStale = 78
)

// errFlagConflict surfaces the --verbose/--quiet mutual-exclusion
// failure. Mapped to [ExitInputErr] by [mapErr].
var errFlagConflict = errors.New("--verbose and --quiet are mutually exclusive")

// errMissingFlag surfaces a missing required subcommand flag. Mapped
// to [ExitInputErr] by [mapErr].
var errMissingFlag = errors.New("missing required flag")

// errConfigUnreadable surfaces a --config path that cannot be opened.
// Mapped to [ExitInputErr] by [mapErr].
var errConfigUnreadable = errors.New("config file unreadable")

// errInvalidJTI surfaces a malformed --jti value. Mapped to
// [ExitInputErr] by [mapErr].
var errInvalidJTI = errors.New("invalid --jti: must be a UUID")

// errNoPassphraseSource surfaces the no-stdin-pipe-and-no-TTY case on
// `serve`. Mapped to [ExitInputErr] by [mapErr].
var errNoPassphraseSource = errors.New("no passphrase source: stdin is not a pipe and is not a terminal")

// errAuthFailed is the abstract auth-failure sentinel raised by the
// revoke subcommand on HTTP 401/403.
var errAuthFailed = errors.New("auth failed")

// errNotFound is the abstract not-found sentinel raised by the revoke
// subcommand on HTTP 404 (and by serve when --config points at a
// nonexistent path).
var errNotFound = errors.New("not found")

// mapErr resolves an error returned by a subcommand's RunE into one of
// the seven locked exit codes. nil maps to ExitOK; unrecognized errors
// fall back to ExitErr. mapErr never returns ExitConfigStale (78) —
// that code is reserved for future supervisor work and never raised
// in this chunk.
//
//nolint:cyclop // sequential errors.Is dispatch over locked sentinel sets
func mapErr(err error) int {
	if err == nil {
		return ExitOK
	}

	// Input errors — operator typed something wrong.
	switch {
	case errors.Is(err, errFlagConflict),
		errors.Is(err, errMissingFlag),
		errors.Is(err, errConfigUnreadable),
		errors.Is(err, errInvalidJTI),
		errors.Is(err, errNoPassphraseSource),
		errors.Is(err, server.ErrMissingConfig),
		errors.Is(err, config.ErrTOMLDecode),
		errors.Is(err, config.ErrUnknownField),
		errors.Is(err, config.ErrMissingRequiredField),
		errors.Is(err, config.ErrInvalidDuration),
		errors.Is(err, config.ErrPathPrefixInvalid),
		errors.Is(err, config.ErrTailscaleBindRequired),
		errors.Is(err, config.ErrListenMalformed),
		errors.Is(err, config.ErrTailscaleRequired),
		errors.Is(err, config.ErrAuditLogEscape),
		errors.Is(err, config.ErrArgonMemoryTooLow),
		errors.Is(err, config.ErrArgonMemoryTooHigh),
		errors.Is(err, config.ErrArgonTimeTooLow),
		errors.Is(err, config.ErrArgonTimeTooHigh),
		errors.Is(err, config.ErrArgonThreadsTooLow),
		errors.Is(err, config.ErrArgonThreadsTooHigh),
		errors.Is(err, config.ErrSupervisorTTLOutOfRange),
		errors.Is(err, config.ErrClaimApprovalTimeoutOutOfRange):
		return ExitInputErr
	}

	// Auth failures — signature or passphrase rejected.
	switch {
	case errors.Is(err, errAuthFailed),
		errors.Is(err, vault.ErrAuthFailed),
		errors.Is(err, sign.ErrSignatureInvalid),
		errors.Is(err, token.ErrSignatureInvalid),
		errors.Is(err, token.ErrTokenExpired),
		errors.Is(err, token.ErrTokenRevoked),
		errors.Is(err, token.ErrTokenExhausted),
		errors.Is(err, token.ErrIPMismatch):
		return ExitAuth
	}

	// Not-found — config, secret, or jti missing.
	switch {
	case errors.Is(err, errNotFound),
		errors.Is(err, vault.ErrSecretNotFound),
		errors.Is(err, server.ErrSecretMissing),
		errors.Is(err, fs.ErrNotExist),
		errors.Is(err, config.ErrStateDirNotFound):
		return ExitNotFound
	}

	// Permission — bind / file-mode rejected.
	switch {
	case errors.Is(err, os.ErrPermission),
		errors.Is(err, server.ErrFileModeLoose),
		errors.Is(err, vault.ErrFilePermsLoose),
		errors.Is(err, config.ErrConfigFileMode):
		return ExitPerm
	}

	// Context-cancellation surfaces as ExitErr (operator cancelled or
	// timeout).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ExitErr
	}

	return ExitErr
}
