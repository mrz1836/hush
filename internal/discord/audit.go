package discord

import (
	"context"
	"log/slog"
)

// mirrorAudit dispatches an audit-channel mirror payload for the
// given lifecycle event. Best-effort, non-blocking: the actual
// network call runs in a goroutine bound to ctx so the primary
// RequestApproval flow does not wait on it (FR-008, clarification
// 2026-04-30 Q4).
//
// When auditChan is empty the call is a no-op (mirroring disabled).
// Failures log a WARN and are otherwise swallowed; the on-disk
// hash-chained audit log (SDD-13) remains the authoritative record.
func (a *BotApprover) mirrorAudit(ctx context.Context, eventType auditEventType, req ApprovalRequest) {
	if a.auditChan == "" {
		return
	}
	payload := renderAudit(eventType, req)
	go func() {
		if ctx.Err() != nil {
			return
		}
		if _, err := a.session.ChannelMessageSendComplex(a.auditChan, payload); err != nil {
			a.logger.Warn("hush/discord: audit-channel mirror failed",
				slog.String("event_type", string(eventType)),
				slog.String("err_class", "audit_mirror"))
		}
	}()
}
