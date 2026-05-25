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
	lines := []string{
		headerInteractive,
		"",
		fmt.Sprintf("Machine: %s", req.MachineName),
		fmt.Sprintf("Mesh IP: %s", req.ClientIP),
		fmt.Sprintf("Scope:   %s", strings.Join(req.Scope, ", ")),
		fmt.Sprintf("Reason:  %s", req.Reason),
		fmt.Sprintf("TTL:     %s", req.RequestedTTL),
	}
	lines = appendAgentContextLines(lines, req, "%s: %s")
	lines = appendRequestIDLine(lines, "Request: %s", req.RequestID)
	return &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{{
			Description: strings.Join(lines, "\n"),
			Color:       colorInteractive,
		}},
		Components: approvalButtons(customID),
	}
}

func renderDaemon(req ApprovalRequest, customID string) *discordgo.MessageSend {
	lines := []string{
		headerDaemon,
		"",
		fmt.Sprintf("Machine:    %s", req.MachineName),
		fmt.Sprintf("Supervisor: %s", req.SupervisorName),
		fmt.Sprintf("Mesh IP:    %s", req.ClientIP),
		fmt.Sprintf("Scope:      %s", strings.Join(req.Scope, ", ")),
		fmt.Sprintf("Reason:     %s", req.Reason),
		fmt.Sprintf("TTL:        %s", req.RequestedTTL),
	}
	lines = appendAgentContextLines(lines, req, "%-11s %s")
	lines = appendRequestIDLine(lines, "Request:    %s", req.RequestID)
	return &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{{
			Description: strings.Join(lines, "\n"),
			Color:       colorDaemon,
		}},
		Components: approvalButtons(customID),
	}
}

// appendAgentContextLines appends one line per populated agent-context
// field. format is the per-line printf template with two positional
// args (label, value) — callers pass alignment-tweaked variants for
// interactive vs daemon prompts.
//
// Long CommandPreview values are truncated at 512 chars with a
// `…[truncated]` marker so the embed never blows Discord's per-field
// limit. Empty fields are silently skipped — the approver sees only
// what the agent actually supplied.
func appendAgentContextLines(lines []string, req ApprovalRequest, format string) []string {
	add := func(label, value string) {
		if value == "" {
			return
		}
		if len(value) > 512 {
			value = value[:512] + "…[truncated]"
		}
		lines = append(lines, fmt.Sprintf(format, label+":", value))
	}
	add("Agent", req.AgentIdentity)
	add("Model", req.AgentModel)
	add("Tool", req.ToolName)
	add("Command", req.CommandPreview)
	add("Summary", req.RecentSummary)
	return lines
}

// appendRequestIDLine appends a "Request: <id>" line when the chassis
// has supplied one. The format string carries the column-alignment
// padding the caller uses for the rest of its fields. A missing
// RequestID is silently elided so legacy callers (no chassis wiring)
// still render valid prompts.
func appendRequestIDLine(lines []string, format, requestID string) []string {
	if requestID == "" {
		return lines
	}
	return append(lines, fmt.Sprintf(format, requestID))
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
	lines = appendRequestIDLine(lines, "Request: %s", req.RequestID)
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
	lines = appendRequestIDLine(lines, "Request: %s", req.RequestID)
	return &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{{
			Description: strings.Join(lines, "\n"),
		}},
	}
}
