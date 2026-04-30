package server

import "errors"

// Construction-time sentinel errors returned by [New] when a required
// dependency is missing.
var (
	ErrMissingConfig      = errors.New("server: missing config")
	ErrMissingVaultPtr    = errors.New("server: missing vault pointer")
	ErrMissingTokenStore  = errors.New("server: missing token store")
	ErrMissingApprover    = errors.New("server: missing approver")
	ErrMissingLogger      = errors.New("server: missing logger")
	ErrMissingAuditWriter = errors.New("server: missing audit writer")
)

// Lifecycle sentinel errors.
var (
	ErrAlreadyRun   = errors.New("server: Run already called")
	ErrShuttingDown = errors.New("server: server is shutting down")
)

// Startup-check sentinel errors. Each check returns a wrapped error using
// fmt.Errorf("...: %w", ...) so callers can match the category via
// errors.Is.
var (
	ErrClockUnsynchronised = errors.New("server: startup: clock unsynchronised")
	ErrFileModeLoose       = errors.New("server: startup: file mode laxer than 0600/0700")
	ErrBindNotOnTailscale  = errors.New("server: startup: listen address not on Tailscale CGNAT")
	ErrStateDirUnsafe      = errors.New("server: startup: state directory missing or unsafe")
)

// Reload sentinel errors. ReloadVault wraps the underlying vault error in
// one of these so callers can distinguish categories without parsing
// strings.
var (
	ErrReloadFileMissing   = errors.New("server: reload: vault file missing")
	ErrReloadDecryptFailed = errors.New("server: reload: vault decrypt failed")
	ErrReloadInvalid       = errors.New("server: reload: vault invalid")
	ErrReloadInProgress    = errors.New("server: reload: another reload is in progress")
)

// Mount sentinel errors. Wrap with fmt.Errorf("%w: ...", sentinel) so callers
// can match categories via errors.Is.
var (
	ErrMountNilHandler   = errors.New("server: mount: nil handler")
	ErrMountBadPath      = errors.New("server: mount: invalid path")
	ErrMountUnsupported  = errors.New("server: mount: unsupported method")
	ErrReloadInternalNil = errors.New("server: reload: load returned nil store with nil error")
)

// Clock-sync probe sentinel errors. Wrap with fmt.Errorf("%w: ...", sentinel)
// so callers can match the unexpected-output category via errors.Is.
var (
	ErrClockProbeUnexpectedOutput = errors.New("server: clock_sync: unexpected probe output")
)
