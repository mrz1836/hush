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
