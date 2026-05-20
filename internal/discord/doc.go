// Package discord owns the Discord-backed approval surface.
//
// The package exposes the Approver interface every secret-claim path
// invokes before the vault server issues a session token, plus the
// production BotApprover backed by github.com/bwmarrin/discordgo.
// BotApprover DMs the configured operator, distinguishes interactive
// (human-at-terminal) from [DAEMON] (long-running supervisor) prompts
// with visually unmistakable headers, monitors its own WebSocket and
// fails closed to ErrDiscordUnavailable when the chat transport is
// down, and rate-limits prompt delivery per (SupervisorName, ClientIP)
// so a misconfigured daemon cannot flood the operator.
//
// Constitution touchpoints:
//   - II — Approval is human, approval is phone; no auto-approve. The
//     only path returning Decision{Approved: true} is an
//     InteractionCreate event whose CustomID matches a pending request
//     and whose component is the Approve button.
//   - V — Disconnects flip a distinct available flag and broadcast
//     ErrDiscordUnavailable to every in-flight RequestApproval.
//   - VIII — TDD-mandatory; ≥85% coverage on this package; race-clean.
//   - IX — context-first APIs; no init(); no mutable globals beyond
//     sentinel errors; the monitor goroutine is owned by the
//     constructor's ctx.
//   - X — Bot token flows through *securebytes.SecureBytes; sentinel
//     error messages are static categories carrying no token bytes,
//     no request fields, and no key material.
//   - XI — One new direct dependency, github.com/bwmarrin/discordgo.
package discord
