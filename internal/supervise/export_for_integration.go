//go:build integration

// File compiled only under `-tags=integration`. Exposes Lifecycle internals
// that the tests/integration harness needs WITHOUT widening the production
// API. The anti-contract forbids new exported methods on Lifecycle in the
// production build; this file is excluded from production by the build tag.

package supervise

import (
	"context"
	"time"
)

// PrimeRefresherForTest marks the refresh window as "already fired today"
// (and the T-30 fallback as spent) so the Refresher's initial tick is inert.
// Without this, an integration test whose wall clock happens to fall inside
// the configured refresh window would see a spurious mid-boot /claim swap.
// Tests drive the refresh window explicitly via TriggerWindowRefreshForTest.
// MUST be called before Run.
func (l *Lifecycle) PrimeRefresherForTest() {
	l.refresher.primeForTest(time.Now(), true)
}

// SnapshotForTest returns the current Store snapshot. Used by the
// integration harness to poll for state transitions without dialing the
// status socket.
func (l *Lifecycle) SnapshotForTest() Snapshot {
	return l.store.Snapshot()
}

// TriggerRefreshForTest drives an operator-style `hush client refresh`: it
// posts the refresh verb to mainLoop exactly as the status socket does and
// blocks on the ack. The harness uses this in Scenarios 7 and 13 to drive a
// mid-session refresh without dialing the Unix socket. Routing through
// mainLoop keeps silentRefillAndRestart single-threaded.
func (l *Lifecycle) TriggerRefreshForTest(ctx context.Context) error {
	return l.handleStatusRefreshVerb(ctx)
}

// TriggerWindowRefreshForTest nudges the claim-refresh loop exactly as the
// Refresher's window tick does, driving a fresh /claim swap (a new JWT for
// the next session window) without restarting the child. The harness uses
// this in Scenario 8 to exercise the daytime refresh window deterministically.
func (l *Lifecycle) TriggerWindowRefreshForTest(_ context.Context) {
	select {
	case l.refreshTickCh <- struct{}{}:
	default:
	}
}

// ConfigForTest exposes the read-only *config.Supervisor the Lifecycle
// was constructed with. The integration harness uses this to derive
// derived paths (status socket, pidfile) without re-reading the TOML.
func (l *Lifecycle) ConfigForTest() any {
	return l.config
}

// BackendPortForTest returns the lifecycle's allocated private backend
// port (set by the child startup path when [child.handoff] mode =
// "http-proxy" is configured). Returns 0 when the lifecycle is not
// reload-eligible or the boot-time child has not yet started.
// Concurrent-safe: reads under the same mutex SwapChild writes through.
func (l *Lifecycle) BackendPortForTest() uint16 {
	l.backendMu.Lock()
	defer l.backendMu.Unlock()
	return l.backendPort
}

// AttachReloadHandler wires the supplied handler onto the lifecycle's
// status server so the reload verb routes to SwapChild. The production
// CLI wiring layer will eventually expose an equivalent seam; until then
// the integration harness uses this to install the bridge.
//
// Single-shot — calling twice panics (mirrors StatusServer.AttachReloadHandler).
func (l *Lifecycle) AttachReloadHandler(handler func(ctx context.Context, req ReloadRequest) (SwapResult, error)) {
	l.statusServer.AttachReloadHandler(handler)
}
