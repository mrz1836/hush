package discord

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/mrz1836/hush/internal/discord/alerts"
)

// TestBotApprover_SatisfiesAlertsSender verifies the compile-time
// guard at runtime: *BotApprover may be passed where alerts.Sender is
// expected, and the additive methods route through the session shim
// to the expected discordgo entrypoints.
func TestBotApprover_SatisfiesAlertsSender(t *testing.T) {
	t.Parallel()

	var _ alerts.Sender = (*BotApprover)(nil)

	shim := newSessionShim()
	cfg := BotConfig{
		OwnerID:        "owner-123",
		AppID:          "app",
		AuditChannelID: "audit-ch",
	}
	a := newBotApproverWithSession(context.Background(), cfg, slog.Default(), shim)

	// SendOwnerDM → UserChannelCreate(owner) + ChannelMessageSendComplex(dm:owner, ...)
	if err := a.SendOwnerDM(context.Background(), "hello-owner"); err != nil {
		t.Fatalf("SendOwnerDM: %v", err)
	}
	if len(shim.created) != 1 || shim.created[0] != "owner-123" {
		t.Errorf("UserChannelCreate args: got %+v", shim.created)
	}
	if len(shim.dms) != 1 || shim.dms[0].Send == nil || shim.dms[0].Send.Content != "hello-owner" {
		t.Errorf("DM body: got %+v", shim.dms)
	}

	// PostChannel(channelID, message) → ChannelMessageSendComplex(channelID, ...)
	if err := a.PostChannel(context.Background(), "audit-ch", "hello-channel"); err != nil {
		t.Fatalf("PostChannel: %v", err)
	}
	var found bool
	for _, m := range shim.allMessages {
		if m.ChannelID == "audit-ch" && m.Send != nil && m.Send.Content == "hello-channel" {
			found = true
		}
	}
	if !found {
		t.Errorf("PostChannel did not emit message via shim; allMessages=%+v", shim.allMessages)
	}

	// Empty channel ID is rejected with a static error.
	err := a.PostChannel(context.Background(), "", "x")
	if err == nil {
		t.Errorf("PostChannel(empty channelID): want error, got nil")
	}
	if !errors.Is(err, errEmptyChannelID) {
		t.Errorf("PostChannel(empty channelID): want errEmptyChannelID, got %v", err)
	}
}
