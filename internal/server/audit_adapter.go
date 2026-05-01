package server

import (
	"context"

	"github.com/mrz1836/hush/internal/audit"
)

// chassisAuditAdapter implements [AuditWriter] over an [audit.Writer].
// Translates the chassis-level [AuditEvent] into the audit package's
// `(action, data map[string]any)` calling shape so the chassis stays
// decoupled from the chain's wire vocabulary.
//
// Use [NewChassisAuditAdapter] to construct the adapter.
type chassisAuditAdapter struct {
	w audit.Writer
}

// NewChassisAuditAdapter wraps an [audit.Writer] so it can be plugged
// into [Deps.AuditWriter]. The adapter is constructed once at chassis
// boot time (in cmd/hush, SDD-14) and lives for the chassis's lifetime.
func NewChassisAuditAdapter(w audit.Writer) AuditWriter {
	return &chassisAuditAdapter{w: w}
}

// Write satisfies [AuditWriter]. Folds AuditEvent.Detail + RequestID +
// ClientIP into a `data` map (string keys, any values) and forwards to
// `w.Append(ctx, string(ev.Type), data)`.
func (a *chassisAuditAdapter) Write(ctx context.Context, ev AuditEvent) error {
	data := make(map[string]any, len(ev.Detail)+2)
	for k, v := range ev.Detail {
		data[k] = v
	}
	if ev.RequestID != "" {
		if _, ok := data["request_id"]; !ok {
			data["request_id"] = ev.RequestID
		}
	}
	if ev.ClientIP.IsValid() {
		if _, ok := data["client_ip"]; !ok {
			data["client_ip"] = ev.ClientIP.String()
		}
	}
	return a.w.Append(ctx, string(ev.Type), data)
}
