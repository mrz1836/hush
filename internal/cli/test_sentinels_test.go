package cli

import "errors"

// errSyntheticTest is the shared "any error" sentinel used by tests
// that need a non-nil error to drive a fallback branch. Static so
// the err113 linter is satisfied.
var errSyntheticTest = errors.New("synthetic")

// errSyntheticUnknown is the shared "unrecognized error" sentinel
// for the mapErr default-branch tests.
var errSyntheticUnknown = errors.New("unknown")

// errSyntheticDial / errSyntheticNoRoute / errSyntheticEOF /
// errSyntheticRefused / errSyntheticTimeout / errSyntheticDeadline
// are the canned classifier inputs for TestClassifyTransportErr.
var (
	errSyntheticDial     = errors.New("dial tcp: lookup foo: no such host")
	errSyntheticNoRoute  = errors.New("network is unreachable: no route to host")
	errSyntheticEOF      = errors.New("read tcp: EOF")
	errSyntheticRefused  = errors.New("connection refused")
	errSyntheticTimeout  = errors.New("net/http: request canceled (Client.Timeout exceeded)")
	errSyntheticDeadline = errors.New("context deadline exceeded")
)

// secretMarkerBytes is the marker pattern used by the
// sentinel-leak tests. Any operator-visible surface that contains
// this marker is a failure.
const secretMarkerBytes = "SECRET_MARKER_DEADBEEF"
