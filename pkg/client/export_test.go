package client

import (
	"context"
	"time"
)

// Test-only re-exports for direct unit tests in the client_test
// package.
//
// SendBlocking and EnsureDeadline use function wrappers (matching the
// internal/audit/export_test.go convention). RecoverWatchPanic is the
// exception — Go's spec requires recover() to be called directly by a
// deferred function, so wrapping breaks the semantics. The function-
// value variable is the only valid form here.

// RecoverWatchPanic exposes recoverWatchPanic to client_test.
//
// MUST stay a function-value variable (not a wrapper) — recover()
// only works when called directly by the deferred function.
//
//nolint:gochecknoglobals // test-only seam; wrapping would break recover()
var RecoverWatchPanic = recoverWatchPanic

// SendBlocking exposes sendBlocking to client_test.
func SendBlocking(ctx context.Context, ch chan<- Event, ev Event) {
	sendBlocking(ctx, ch, ev)
}

// EnsureDeadline exposes ensureDeadline to client_test.
func EnsureDeadline(ctx context.Context, fallback time.Duration) (context.Context, context.CancelFunc) {
	return ensureDeadline(ctx, fallback)
}

// MeDefaultTimeout exposes the production default for assertion in tests.
const MeDefaultTimeout = meDefaultTimeout

// SupervisorDefaultTimeout exposes the production default for assertion in tests.
const SupervisorDefaultTimeout = supervisorDefaultTimeout

// SupervisorMaxResponseBytes exposes the production cap for assertion in tests.
const SupervisorMaxResponseBytes = supervisorMaxResponseBytes
