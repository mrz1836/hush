//go:build integration

package harness

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
)

// AlertPayload is the harness's recorded form of one supervise.Alerts.Emit
// call. It carries non-secret labels only (Scope, ErrorClass, Reason) per
// the supervise.AlertPayload contract — Constitution X structurally
// forbids secret bytes from appearing here.
type AlertPayload struct {
	Class      supervise.AlertClass
	Scope      string
	ErrorClass string
	Reason     string
	At         time.Time
}

// TestDiscord wraps testutil.DiscordStub with the per-scenario state
// SDD-25 needs:
//
//   - Connectivity-sequence driver for Scenario 10 (Discord unavailable).
//   - Supervise-side alert recorder satisfying supervise.Alerts.
//   - Adapter (AsApprover) bridging stub Decision → server.Decision with
//     zero policy logic (Constitution II).
type TestDiscord struct {
	stub      *testutil.DiscordStub
	connected atomic.Bool

	mu     sync.Mutex
	alerts []AlertPayload
}

// NewDiscord constructs a TestDiscord with an embedded DiscordStub. The
// stub starts in "connected" state; use SetConnected(false) for Scenario
// 10. The stub's t.Cleanup is registered automatically by
// testutil.NewDiscordStub.
func NewDiscord(t *testing.T) *TestDiscord {
	t.Helper()
	d := &TestDiscord{stub: testutil.NewDiscordStub(t)}
	d.connected.Store(true)
	return d
}

// Stub returns the embedded testutil.DiscordStub for direct Enqueue access.
func (d *TestDiscord) Stub() *testutil.DiscordStub { return d.stub }

// SetConnected drives the connectivity-sequence used by Scenario 10. The
// connected flag is read by AsApprover-style adapters in server-side code
// paths the harness wires up.
func (d *TestDiscord) SetConnected(b bool) { d.connected.Store(b) }

// Connected reports whether the stub is currently in "connected" state.
// Used by harness-internal Approver adapters to short-circuit /claim with
// the discord_unavailable sentinel when the operator unplugs Discord.
func (d *TestDiscord) Connected() bool { return d.connected.Load() }

// Alerts returns a defensive copy of every recorded supervise-side alert.
func (d *TestDiscord) Alerts() []AlertPayload {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]AlertPayload, len(d.alerts))
	copy(out, d.alerts)
	return out
}

// AlertsRaw returns the concatenated byte stream of every recorded alert
// for the cross-stream sentinel-absence sweep. Each alert's three string
// fields are joined with a separator that cannot collide with the
// sentinel prefix (`SECRET_SHOULD_NEVER_APPEAR_`).
func (d *TestDiscord) AlertsRaw() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	var b bytes.Buffer
	for _, a := range d.alerts {
		b.WriteString(a.Class.String())
		b.WriteByte('|')
		b.WriteString(a.Scope)
		b.WriteByte('|')
		b.WriteString(a.ErrorClass)
		b.WriteByte('|')
		b.WriteString(a.Reason)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// SuperviseAlerts is the supervise.Alerts adapter; install via
// Deps.Alerts. Records every Emit into the harness alert log.
type SuperviseAlerts struct {
	d *TestDiscord
}

// AsSuperviseAlerts returns the adapter for Deps.Alerts.
func (d *TestDiscord) AsSuperviseAlerts() *SuperviseAlerts { return &SuperviseAlerts{d: d} }

// Emit records (class, payload) into the harness alert log.
func (s *SuperviseAlerts) Emit(_ context.Context, class supervise.AlertClass, p supervise.AlertPayload) {
	if s == nil || s.d == nil {
		return
	}
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	s.d.alerts = append(s.d.alerts, AlertPayload{
		Class:      class,
		Scope:      p.Scope,
		ErrorClass: p.ErrorClass,
		Reason:     p.Reason,
		At:         time.Now(),
	})
}
