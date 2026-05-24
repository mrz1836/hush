package discord

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/mrz1836/hush/internal/token"
)

// Embed color palette — Discord brand-aligned hex literals.
const (
	colorInteractive = 0x57F287 // green
	colorDaemon      = 0xFEE75C // yellow
	colorDenied      = 0xED4245 // red
)

// Header glyphs and labels.
const (
	headerInteractive = "✅ **Interactive secret request**"
	headerDaemon      = "⚠ **[DAEMON] Supervisor secret request**"
	headerApproved    = "✅ **Approved — request consumed**"
	headerDenied      = "❌ **Denied — request consumed**"
)

// auditEventType is the catalog of mirror event names emitted to
// the configured audit channel.
type auditEventType string

const (
	auditRequestReceived auditEventType = "request_received"
	auditApproved        auditEventType = "approved"
	auditDenied          auditEventType = "denied"
	auditTimedOut        auditEventType = "timed_out"
	auditRateLimited     auditEventType = "rate_limited"
)

// renderApproval dispatches to the interactive or daemon DM template
// based on req.SessionType. customID is the per-request UUID assigned
// by RequestApproval; the Approve and Deny buttons embed it as
// "{uuid}:approve" and "{uuid}:deny".
func renderApproval(req ApprovalRequest, customID string) *discordgo.MessageSend {
	if req.SessionType == token.SessionSupervisor {
		return renderDaemon(req, customID)
	}
	return renderInteractive(req, customID)
}

func renderInteractive(req ApprovalRequest, customID string) *discordgo.MessageSend {
	body := strings.Join([]string{
		headerInteractive,
		"",
		fmt.Sprintf("Machine: %s", req.MachineName),
		fmt.Sprintf("Mesh IP: %s", req.ClientIP),
		fmt.Sprintf("Scope:   %s", strings.Join(req.Scope, ", ")),
		fmt.Sprintf("Reason:  %s", req.Reason),
		fmt.Sprintf("TTL:     %s", req.RequestedTTL),
	}, "\n")
	return &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{{
			Description: body,
			Color:       colorInteractive,
		}},
		Components: approvalButtons(customID),
	}
}

func renderDaemon(req ApprovalRequest, customID string) *discordgo.MessageSend {
	body := strings.Join([]string{
		headerDaemon,
		"",
		fmt.Sprintf("Machine:    %s", req.MachineName),
		fmt.Sprintf("Supervisor: %s", req.SupervisorName),
		fmt.Sprintf("Mesh IP:    %s", req.ClientIP),
		fmt.Sprintf("Scope:      %s", strings.Join(req.Scope, ", ")),
		fmt.Sprintf("Reason:     %s", req.Reason),
		fmt.Sprintf("TTL:        %s", req.RequestedTTL),
	}, "\n")
	return &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{{
			Description: body,
			Color:       colorDaemon,
		}},
		Components: approvalButtons(customID),
	}
}

func approvalButtons(customID string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Approve",
					Style:    discordgo.PrimaryButton,
					CustomID: customID + ":approve",
				},
				discordgo.Button{
					Label:    "Deny",
					Style:    discordgo.DangerButton,
					CustomID: customID + ":deny",
				},
			},
		},
	}
}

func renderResolvedApproval(req ApprovalRequest, approved bool) *discordgo.InteractionResponseData {
	header := headerApproved
	color := colorInteractive
	if !approved {
		header = headerDenied
		color = colorDenied
	}

	lines := []string{
		header,
		"",
		fmt.Sprintf("Machine: %s", req.MachineName),
	}
	if req.SessionType == token.SessionSupervisor {
		lines = append(lines, fmt.Sprintf("Supervisor: %s", req.SupervisorName))
	}
	lines = append(
		lines,
		fmt.Sprintf("Mesh IP: %s", req.ClientIP),
		fmt.Sprintf("Scope:   %s", strings.Join(req.Scope, ", ")),
		fmt.Sprintf("Reason:  %s", req.Reason),
		fmt.Sprintf("TTL:     %s", req.RequestedTTL),
	)
	return &discordgo.InteractionResponseData{
		Embeds: []*discordgo.MessageEmbed{{
			Description: strings.Join(lines, "\n"),
			Color:       color,
		}},
		Components: []discordgo.MessageComponent{},
	}
}

// renderAudit produces a mirror payload — same body shape as the DM
// minus the action buttons, with the event type prefixed. No bot
// token, no secret value, no key material appears in the output.
func renderAudit(eventType auditEventType, req ApprovalRequest) *discordgo.MessageSend {
	headerLine := string(eventType)
	var label string
	if req.SessionType == token.SessionSupervisor {
		label = "[DAEMON] supervisor"
	} else {
		label = "interactive"
	}
	lines := []string{
		fmt.Sprintf("audit: %s (%s)", headerLine, label),
		fmt.Sprintf("Machine: %s", req.MachineName),
	}
	if req.SessionType == token.SessionSupervisor {
		lines = append(lines, fmt.Sprintf("Supervisor: %s", req.SupervisorName))
	}
	lines = append(
		lines,
		fmt.Sprintf("Mesh IP: %s", req.ClientIP),
		fmt.Sprintf("Scope: %s", strings.Join(req.Scope, ", ")),
		fmt.Sprintf("Reason: %s", req.Reason),
		fmt.Sprintf("TTL: %s", req.RequestedTTL),
	)
	return &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{{
			Description: strings.Join(lines, "\n"),
		}},
	}
}
