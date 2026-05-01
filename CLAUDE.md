# CLAUDE.md

See [.github/CLAUDE.md](.github/CLAUDE.md) for the canonical agent guide.

<!-- SPECKIT START -->
Active feature plan: [specs/011-discord-bot/plan.md](specs/011-discord-bot/plan.md) — SDD-11 `internal/discord` (Approver interface + Discord-backed BotApprover via `bwmarrin/discordgo`; distinct interactive vs `[DAEMON]` DM templates; per-`(SupervisorName, ClientIP)` token-bucket rate limit defaulting to 5 min, deferred-consume on transport-unavailable; WebSocket monitor flips `available` flag, drains in-flight on disconnect, 60s exponential-backoff cap on reconnect, indefinite retry; bot token loaded once via `*securebytes.SecureBytes`; AC-3).
<!-- SPECKIT END -->
