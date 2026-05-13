//go:build integration

// File compiled only under `-tags=integration`. Exposes Lifecycle internals
// that the tests/integration harness needs WITHOUT widening the production
// API. The locked anti-contract (CLAUDE.md SDD-25) forbids new exported
// methods on Lifecycle in the production build; this file is excluded from
// production by the build tag.

package supervise

import "context"

// SnapshotForTest returns the current Store snapshot. Used by the
// integration harness to poll for state transitions without dialing the
// status socket.
func (l *Lifecycle) SnapshotForTest() Snapshot {
	return l.store.Snapshot()
}

// TriggerRefreshForTest synchronously invokes the silent-refill path,
// blocking until the refill completes or ctx cancels. The harness uses
// this in Scenario 13 to drive a mid-session rotation without waiting
// for the refresh-window tick.
func (l *Lifecycle) TriggerRefreshForTest(ctx context.Context) error {
	return l.coalescer.Handle(ctx)
}

// ConfigForTest exposes the read-only *config.Supervisor the Lifecycle
// was constructed with. The integration harness uses this to derive
// derived paths (status socket, pidfile) without re-reading the TOML.
func (l *Lifecycle) ConfigForTest() any {
	return l.config
}
