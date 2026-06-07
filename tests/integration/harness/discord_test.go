//go:build integration

package harness

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
)

// TestNewDiscordStartsConnected confirms the stub is reachable and the
// connectivity flag defaults to true.
func TestNewDiscordStartsConnected(t *testing.T) {
	d := NewDiscord(t)
	require.NotNil(t, d.Stub())
	assert.True(t, d.Connected())
}

// TestDiscordSetConnectedTogglesFlag drives the Scenario-10 connectivity seam.
func TestDiscordSetConnectedTogglesFlag(t *testing.T) {
	d := NewDiscord(t)
	d.SetConnected(false)
	assert.False(t, d.Connected())
	d.SetConnected(true)
	assert.True(t, d.Connected())
}

// TestDiscordAlertsRecordAndCopy verifies SuperviseAlerts.Emit records
// alerts and Alerts() / AlertsRaw() reflect them, with Alerts() returning
// a defensive copy.
func TestDiscordAlertsRecordAndCopy(t *testing.T) {
	d := NewDiscord(t)
	alerts := d.AsSuperviseAlerts()
	alerts.Emit(context.Background(), supervise.AlertClassBootTimeout, supervise.AlertPayload{
		Scope:      "ANTHROPIC_API_KEY",
		ErrorClass: "timeout",
		Reason:     "boot exceeded budget",
	})

	got := d.Alerts()
	require.Len(t, got, 1)
	assert.Equal(t, supervise.AlertClassBootTimeout, got[0].Class)
	assert.Equal(t, "ANTHROPIC_API_KEY", got[0].Scope)
	assert.Equal(t, "timeout", got[0].ErrorClass)
	assert.Equal(t, "boot exceeded budget", got[0].Reason)
	assert.False(t, got[0].At.IsZero())

	// Mutating the returned copy must not affect the internal log.
	got[0].Reason = "mutated"
	assert.Equal(t, "boot exceeded budget", d.Alerts()[0].Reason)

	raw := string(d.AlertsRaw())
	assert.Contains(t, raw, "BootTimeout")
	assert.Contains(t, raw, "ANTHROPIC_API_KEY")
	assert.Contains(t, raw, "timeout")
	assert.Contains(t, raw, "boot exceeded budget")
}

// TestDiscordHasAlert covers both the present and absent branches.
func TestDiscordHasAlert(t *testing.T) {
	d := NewDiscord(t)
	assert.False(t, d.HasAlert(supervise.AlertClassRefillFailed))
	d.AsSuperviseAlerts().Emit(context.Background(), supervise.AlertClassRefillFailed, supervise.AlertPayload{})
	assert.True(t, d.HasAlert(supervise.AlertClassRefillFailed))
	assert.False(t, d.HasAlert(supervise.AlertClassExit78))
}

// TestDiscordWaitAlertSucceeds confirms WaitAlert returns once a matching
// alert is recorded asynchronously.
func TestDiscordWaitAlertSucceeds(t *testing.T) {
	d := NewDiscord(t)
	go func() {
		time.Sleep(10 * time.Millisecond)
		d.AsSuperviseAlerts().Emit(context.Background(), supervise.AlertClassGraceEntered, supervise.AlertPayload{})
	}()
	d.WaitAlert(t, supervise.AlertClassGraceEntered, 2*time.Second)
}

// TestDiscordWaitAlertTimesOut covers the deadline-expiry branch (drives
// the helper's t.Fatalf path via a detached T).
func TestDiscordWaitAlertTimesOut(t *testing.T) {
	d := NewDiscord(t)
	expectFatal(t, "no-alert", func(ft *testing.T) {
		d.WaitAlert(ft, supervise.AlertClassExit78, 30*time.Millisecond)
	})
}

// TestSuperviseAlertsEmitNilSafe covers the defensive nil guards in Emit.
func TestSuperviseAlertsEmitNilSafe(t *testing.T) {
	var s *SuperviseAlerts
	assert.NotPanics(t, func() {
		s.Emit(context.Background(), supervise.AlertClassExit78, supervise.AlertPayload{})
	})
	empty := &SuperviseAlerts{}
	assert.NotPanics(t, func() {
		empty.Emit(context.Background(), supervise.AlertClassExit78, supervise.AlertPayload{})
	})
}

// TestDiscordApproverApproves covers the connected + approved path.
func TestDiscordApproverApproves(t *testing.T) {
	d := NewDiscord(t)
	d.Stub().ApproveAll = true
	approver := d.AsApprover()

	dec, err := approver.RequestApproval(context.Background(), server.ApprovalRequest{
		MachineName:  "host-1",
		Scope:        []string{"ANTHROPIC_API_KEY"},
		SessionType:  server.SessionInteractive,
		RequestedTTL: 5 * time.Minute,
	})
	require.NoError(t, err)
	assert.True(t, dec.Approved)
	assert.Equal(t, 5*time.Minute, dec.GrantedTTL)
	assert.Equal(t, "harness-discord", dec.ApproverID)
	assert.False(t, dec.ApprovedAt.IsZero())
}

// TestDiscordApproverUnavailableWhenDisconnected covers the SetConnected(false)
// short-circuit.
func TestDiscordApproverUnavailableWhenDisconnected(t *testing.T) {
	d := NewDiscord(t)
	d.SetConnected(false)
	_, err := d.AsApprover().RequestApproval(context.Background(), server.ApprovalRequest{
		MachineName: "host", Scope: []string{"X"}, SessionType: server.SessionSupervisor, SupervisorName: "sup",
	})
	assert.ErrorIs(t, err, server.ErrApproverUnavailable)
}

// TestDiscordApproverDenied covers the queued-deny → ErrApproverDenied path.
func TestDiscordApproverDenied(t *testing.T) {
	d := NewDiscord(t)
	d.Stub().Enqueue(testutil.DecisionDeny)
	_, err := d.AsApprover().RequestApproval(context.Background(), server.ApprovalRequest{
		MachineName: "host", Scope: []string{"X"}, SessionType: server.SessionInteractive,
	})
	assert.ErrorIs(t, err, server.ErrApproverDenied)
}

// TestDiscordApproverPropagatesStubError covers the `err != nil` branch.
// The stub's unexpected-call path calls t.Errorf, so it is bound to a
// detached T to avoid failing this test; we only assert the adapter
// surfaces the error.
func TestDiscordApproverPropagatesStubError(t *testing.T) {
	var d *TestDiscord
	runIsolated(func(ft *testing.T) { d = NewDiscord(ft) }) // ApproveAll=false, empty queue
	_, err := d.AsApprover().RequestApproval(context.Background(), server.ApprovalRequest{
		MachineName: "host", Scope: []string{"X"}, SessionType: server.SessionInteractive,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, testutil.ErrUnexpectedCall)
}

// TestSessionTypeLabel covers all switch arms including the default.
func TestSessionTypeLabel(t *testing.T) {
	assert.Equal(t, "supervisor", sessionTypeLabel(server.SessionSupervisor))
	assert.Equal(t, "interactive", sessionTypeLabel(server.SessionInteractive))
	assert.Equal(t, "interactive", sessionTypeLabel(server.SessionType(0)))
}

// TestDiscordApproverErrorsAreTyped sanity-checks the sentinel errors differ.
func TestDiscordApproverErrorsAreTyped(t *testing.T) {
	assert.False(t, errors.Is(server.ErrApproverDenied, server.ErrApproverUnavailable))
}
