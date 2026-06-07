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
	ErrMissingTokenIssuer = errors.New("server: missing token issuer")
)

// Chassis-level Approver outcome sentinels. The chassis itself never
// produces these — the production wiring (cmd/hush) installs an
// adapter that translates internal/discord sentinels into these chassis-level
// constants so the claim handler stays decoupled from the discord package.
//
// Compare via errors.Is. Messages are static lowercase categories — they MUST
// NOT contain echoed input or token bytes (Constitution X).
var (
	ErrApproverDenied      = errors.New("server: approver denied")
	ErrApproverTimeout     = errors.New("server: approver timeout")
	ErrApproverUnavailable = errors.New("server: approver unavailable")
	ErrApproverRateLimited = errors.New("server: approver rate limited")
	ErrClientUnknown       = errors.New("server: client key unknown")
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
// so callers can match the category via errors.Is.
var (
	ErrClockProbeUnexpectedOutput = errors.New("server: clock_sync: unexpected probe output")
	ErrClockProbeUnavailable      = errors.New("server: clock_sync: probe unavailable")
)

// Secret-handler sentinel errors. The /s handler maps
// ErrSecretMissing to the documented `404 not_found` outcome and the
// `secret_missing` audit action; the surfaced response body never echoes
// the secret name beyond the path the caller already supplied.
var (
	ErrSecretMissing = errors.New("server: secret missing")
)
