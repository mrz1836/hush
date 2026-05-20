package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/supervise"
	supcfg "github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/sign"
	"github.com/mrz1836/hush/internal/vault"
)

// Exit codes form the public CLI contract. Operators script against
// these values; their numeric stability is part of the contract.
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
	// the future supervisor↔child contract. It is
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

// errVaultExists surfaces the existing-vault refusal in init server.
// Mapped to [ExitErr] by [mapErr].
var errVaultExists = errors.New("vault already exists")

// errConfigExists surfaces the existing-config refusal in init
// server. Mapped to [ExitErr] by [mapErr].
var errConfigExists = errors.New("config already exists")

// errKeychainItemExists surfaces a pre-existing keychain item under
// the same (service, account) pair. Mapped to [ExitErr] by [mapErr].
var errKeychainItemExists = errors.New("keychain item already exists")

// errPassphraseTooShort surfaces a passphrase shorter than 12 bytes.
// Mapped to [ExitInputErr] by [mapErr].
var errPassphraseTooShort = errors.New("passphrase too short")

// errPassphraseMismatch surfaces a confirmation mismatch in init.
// Mapped to [ExitInputErr] by [mapErr].
var errPassphraseMismatch = errors.New("passphrase confirmation mismatch")

// errNoTTY surfaces a non-interactive stdin in init. Mapped to
// [ExitInputErr] by [mapErr].
var errNoTTY = errors.New("stdin not a tty")

// errUserAborted surfaces the guided init flow's per-artifact
// recovery [q]uit choice. Mapped to [ExitInputErr] by [mapErr]: an
// operator who picks "quit" supplied a (terminal) input that asked
// hush to stop. Distinct from context cancellation so tests can
// assert the user-driven path.
var errUserAborted = errors.New("hush: init: user aborted")

// errPreflightFailed surfaces a typed-error failure from the
// preflight registry. Wraps the underlying setup sentinel; mapErr
// inspects the wrapped error to pick the right exit code.
var errPreflightFailed = errors.New("hush: init: preflight failed")

// errPlatformACLUnsupported surfaces init's platform refusal on hosts
// without per-binary keychain ACL semantics. Mapped to [ExitErr].
var errPlatformACLUnsupported = errors.New("platform has no per-binary keychain ACL")

// errNotFound is the abstract not-found sentinel raised by the revoke
// subcommand on HTTP 404 (and by serve when --config points at a
// nonexistent path).
var errNotFound = errors.New("not found")

// errMissingExecOrFormat surfaces a hush request invocation that
// supplied neither --exec nor --format eval. Mapped to [ExitInputErr].
// Locked stderr message: "hush: request: must specify --exec or
// --format eval".
var errMissingExecOrFormat = errors.New("hush: request: must specify --exec or --format eval")

// errExecAndFormatBothSet surfaces a hush request invocation that
// supplied both --exec and --format. Mapped to [ExitInputErr]. Locked
// stderr message: "hush: request: --exec and --format eval are
// mutually exclusive".
var errExecAndFormatBothSet = errors.New("hush: request: --exec and --format eval are mutually exclusive")

// errFormatNotEval surfaces a --format value other than the literal
// "eval". Mapped to [ExitInputErr]. Locked stderr message:
// `hush: request: --format only accepts the literal value "eval"`.
var errFormatNotEval = errors.New(`hush: request: --format only accepts the literal value "eval"`)

// errMaxUsesTooLow surfaces a --max-uses value smaller than the
// number of comma-separated --scope entries. Mapped to [ExitInputErr].
// Locked stderr message: "hush: request: --max-uses must be ≥ number
// of scopes".
var errMaxUsesTooLow = errors.New("hush: request: --max-uses must be ≥ number of scopes")

// errInvalidScopeName surfaces a --scope entry that doesn't match the
// POSIX shell-identifier regex. Defence-in-depth so a malformed scope
// can't survive into --format eval output. Mapped to [ExitInputErr].
var errInvalidScopeName = errors.New("hush: request: --scope must match ^[A-Za-z_][A-Za-z0-9_]*$")

// errInvalidGraceWindow surfaces a --grace-window flag value that is
// negative, zero (when explicitly set), or > 4h. Mapped to
// [ExitInputErr] by [mapErr].
var errInvalidGraceWindow = errors.New("invalid --grace-window")

// errSocketAmbiguous surfaces the auto-detect-zero or auto-detect-
// multiple branches of supervisor socket discovery.
// Mapped to [ExitInputErr] by [mapErr].
var errSocketAmbiguous = errors.New("supervisor socket ambiguous")

// errSocketUnreachable surfaces dial / read / write / parse failures
// against the supervisor's status socket.
// Mapped to [ExitErr] by [mapErr].
var errSocketUnreachable = errors.New("supervisor socket unreachable")

// errSupervisorRefused surfaces a `client refresh` ack carrying
// {"ok":false,"error":<msg>}. Mapped to [ExitErr].
var errSupervisorRefused = errors.New("supervisor refused")

// errDuplicateSupervisor surfaces the case where another
// supervisor is already running for the supplied configuration.
// Wraps supervise.ErrPidLocked. Mapped to [ExitErr] by [mapErr].
var errDuplicateSupervisor = errors.New("another supervisor is already running for this configuration")

// errChildExitCode wraps the integer exit status of a child process
// launched by `hush request --exec`. mapErr unwraps it via errors.As
// and returns the child's code verbatim, preserving the exit-code
// propagation contract.
//
//nolint:errname // contract-locked name
type errChildExitCode struct{ code int }

// Error implements error.
func (e *errChildExitCode) Error() string {
	return fmt.Sprintf("hush: request: child exited with code %d", e.code)
}

// mapErr resolves an error returned by a subcommand's RunE into one of
// the seven locked exit codes. nil maps to ExitOK; unrecognized errors
// fall back to ExitErr. mapErr never returns ExitConfigStale (78) —
// that code is reserved for future supervisor work and never raised
// in this chunk.
//
//nolint:cyclop,gocyclo // sequential errors.Is dispatch over locked sentinel sets
func mapErr(err error) int {
	if err == nil {
		return ExitOK
	}

	// Child-exit propagation: --exec returns the child's status
	// verbatim. Must be checked before generic ExitErr
	// classification so the child's code (which may be any int) wins.
	var childExit *errChildExitCode
	if errors.As(err, &childExit) {
		return childExit.code
	}

	// Input errors — operator typed something wrong.
	switch {
	case errors.Is(err, errFlagConflict),
		errors.Is(err, errMissingFlag),
		errors.Is(err, errConfigUnreadable),
		errors.Is(err, errInvalidJTI),
		errors.Is(err, errNoPassphraseSource),
		errors.Is(err, errPassphraseTooShort),
		errors.Is(err, errPassphraseMismatch),
		errors.Is(err, errNoTTY),
		errors.Is(err, errMissingExecOrFormat),
		errors.Is(err, errExecAndFormatBothSet),
		errors.Is(err, errFormatNotEval),
		errors.Is(err, errMaxUsesTooLow),
		errors.Is(err, errInvalidScopeName),
		errors.Is(err, errUserAborted),
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
		errors.Is(err, config.ErrClaimApprovalTimeoutOutOfRange),
		errors.Is(err, errInvalidGraceWindow),
		errors.Is(err, errSocketAmbiguous),
		errors.Is(err, supcfg.ErrTOMLDecode),
		errors.Is(err, supcfg.ErrUnknownField),
		errors.Is(err, supcfg.ErrMissingRequiredField),
		errors.Is(err, supcfg.ErrInvalidDuration),
		errors.Is(err, supcfg.ErrUnknownValidator),
		errors.Is(err, supcfg.ErrGraceWindowTooLong),
		errors.Is(err, supcfg.ErrGraceTTLWithoutCache),
		errors.Is(err, supcfg.ErrRefreshWindowFormat),
		errors.Is(err, supcfg.ErrRefreshWindowOrder),
		errors.Is(err, supcfg.ErrCommandEmpty),
		errors.Is(err, supcfg.ErrCommandPathRelative),
		errors.Is(err, supcfg.ErrScopeEmpty),
		errors.Is(err, supcfg.ErrSessionTypeInvalid),
		errors.Is(err, supcfg.ErrRequestedTTLOutOfRange),
		errors.Is(err, supcfg.ErrServerURLInvalid),
		errors.Is(err, supcfg.ErrLogLevelInvalid),
		errors.Is(err, supcfg.ErrWatchdogRateInvalid):
		return ExitInputErr
	}

	// supervise + client error classes — operational failures
	// (socket unreachable, supervisor refused refresh, duplicate
	// supervisor wrap of supervise.ErrPidLocked) collapse to ExitErr.
	// Checked BEFORE the not-found / perm classes because the
	// underlying dial / read errors wrap fs.ErrNotExist when the
	// supervisor's socket file is missing, but unreachable socket
	// requires ExitErr.
	switch {
	case errors.Is(err, errSocketUnreachable),
		errors.Is(err, errSupervisorRefused),
		errors.Is(err, errDuplicateSupervisor),
		errors.Is(err, supervise.ErrPidLocked):
		return ExitErr
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
		errors.Is(err, config.ErrConfigFileMode),
		errors.Is(err, keychain.ErrKeychainPermissionDenied):
		return ExitPerm
	}

	// init / keychain doctor-repair failures.
	switch {
	case errors.Is(err, errVaultExists),
		errors.Is(err, errConfigExists),
		errors.Is(err, errKeychainItemExists),
		errors.Is(err, errPlatformACLUnsupported):
		return ExitErr
	case errors.Is(err, errKeychainDoctorMissing):
		return ExitNotFound
	case errors.Is(err, errKeychainDoctorDenied),
		errors.Is(err, errKeychainRepairFailed),
		errors.Is(err, errKeychainStoreNonInteractive),
		errors.Is(err, errKeychainStoreRecoveryExhausted):
		return ExitPerm
	}

	// Context-cancellation surfaces as ExitErr (operator cancelled or
	// timeout).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ExitErr
	}

	return ExitErr
}
