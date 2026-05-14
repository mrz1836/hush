package discord

import (
	"context"
	"errors"
	"fmt"

	"github.com/bwmarrin/discordgo"

	"github.com/mrz1836/hush/internal/discord/alerts"
)

// errEmptyChannelID is returned by PostChannel when the configured
// audit channel ID is empty (e.g. operator deployed without
// auditChannelID set but a Warning-tier alert reached the router).
var errEmptyChannelID = errors.New("hush/discord: empty channel id")

// errNilDMChannel is returned by SendOwnerDM if the underlying
// UserChannelCreate call returned a nil channel without an error
// (defensive — discordgo should never do this, but the alerts router
// surfaces it as a transport failure rather than panicking).
var errNilDMChannel = errors.New("hush/discord: nil dm channel")

// Compile-time guard that *BotApprover satisfies the alerts.Sender
// interface. The implementation lives in the additive methods below;
// it does NOT alter any locked SDD-11 symbol (PACKAGE-MAP.md:1245-1246).
var _ alerts.Sender = (*BotApprover)(nil)

// SendOwnerDM delivers a rendered alert body to the operator's
// configured DM destination. The owner identifier was captured at
// NewBotApprover time. Wraps any underlying transport error in a
// stable category string ("send owner dm"); no Alert field or
// credential material is interpolated.
func (a *BotApprover) SendOwnerDM(_ context.Context, message string) error {
	dm, err := a.session.UserChannelCreate(a.ownerID)
	if err != nil {
		return fmt.Errorf("hush/discord: send owner dm: %w", err)
	}
	if dm == nil {
		return errNilDMChannel
	}
	if _, err := a.session.ChannelMessageSendComplex(dm.ID, &discordgo.MessageSend{Content: message}); err != nil {
		return fmt.Errorf("hush/discord: send owner dm: %w", err)
	}
	return nil
}

// PostChannel posts a rendered alert body to the named channel.
// Empty channel ID is rejected with a static error to surface
// misconfiguration loudly (Constitution V) without exposing any
// caller-supplied value.
func (a *BotApprover) PostChannel(_ context.Context, channelID, message string) error {
	if channelID == "" {
		return errEmptyChannelID
	}
	if _, err := a.session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{Content: message}); err != nil {
		return fmt.Errorf("hush/discord: post channel: %w", err)
	}
	return nil
}
