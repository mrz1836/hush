package client

import "errors"

// ErrSocketUnavailable is returned when the supervisor's status socket
// cannot be dialed, read, or written — typically because the
// supervisor is not running, the socket path is wrong, or the caller's
// context deadline expired before the round-trip completed.
//
// Callers can switch on this via errors.Is.
var ErrSocketUnavailable = errors.New("hush/client: supervisor socket unavailable")

// ErrInvalidResponse is returned when the supervisor responded but the
// payload could not be parsed as a known wire format. This usually
// indicates a version skew (the supervisor is older or newer than the
// SDK) or a corrupted response.
var ErrInvalidResponse = errors.New("hush/client: supervisor response invalid")

// ErrRefreshDenied is returned by (*SupervisorStatus).Refresh when the
// supervisor accepted the request but reported a refusal — for example,
// because the vault is currently unreachable or the refresh window is
// closed. The wrapped error message carries the supervisor's reason
// string verbatim.
var ErrRefreshDenied = errors.New("hush/client: supervisor refused refresh")

// ErrReloadConfigInvalid is returned by (*SupervisorStatus).Reload when
// the supervisor refused the reload because the running config is not
// reload-eligible — typically missing [child.readiness] or
// [child.handoff] mode = "http-proxy", or the supervisor has no proxy
// listener attached. The wrapped error message carries the supervisor's
// reason string verbatim. Compare via errors.Is.
var ErrReloadConfigInvalid = errors.New("hush/client: supervisor rejected reload: config invalid")

// ErrReloadReadinessFailed is returned by (*SupervisorStatus).Reload
// when the supervisor started a replacement child but its HTTP
// readiness probe did not pass within the configured budget. The old
// child remains the active backend; the rollout failed. The wrapped
// error message carries the supervisor's reason string verbatim.
// Compare via errors.Is.
var ErrReloadReadinessFailed = errors.New("hush/client: supervisor reload failed readiness")

// ErrReloadInFlight is returned by (*SupervisorStatus).Reload when
// another reload is already running against the same supervisor.
// Callers can retry once the in-flight reload completes. Compare via
// errors.Is.
var ErrReloadInFlight = errors.New("hush/client: supervisor reload already in flight")

// ErrReloadFailed is returned by (*SupervisorStatus).Reload for any
// supervisor-side failure that does not match the more specific
// sentinels above — for example a child start failure, backend port
// allocation failure, or the supervisor not being in the running
// state. The wrapped error message carries the supervisor's reason
// string verbatim. Compare via errors.Is.
var ErrReloadFailed = errors.New("hush/client: supervisor reload failed")
