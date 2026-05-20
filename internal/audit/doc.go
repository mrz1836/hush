// Package audit owns the hush server's tamper-evident audit log
// (Constitution III Layer 6).
//
// Every server outcome flows through a single goroutine that hashes the
// previous record's hash with the canonical-JSON form of the current event,
// signs the hash with the BIP32-derived audit signing key
// (`m/44'/7743'/2'`), and persists the line to the on-disk JSONL chain
// file.  The chain is contiguous from Seq=1 (genesis prevHash) and is
// re-verifiable end-to-end by [Verify]; the first inconsistency surfaces
// [ErrAuditChainBroken] wrapped in a [*ChainError] carrying the offending
// Seq and Reason.
//
// The producer-facing API is intentionally tiny:
//
//   - [Writer.Append] — synchronously rendezvous with the writer goroutine
//     via an unbuffered channel.  Returns nil only AFTER the event has
//     been hashed, signed, written, and flushed to disk.
//   - [Writer.Run] — single-call lifecycle; spawns the writer goroutine
//     and (when configured) the mirror goroutine; returns when the supplied
//     ctx cancels and every in-flight pending event has been drained.
//
// Discord mirroring is best-effort and lives in [DiscordMirror]: a
// 64-deep buffered channel and a separate goroutine that calls
// `ChannelMessageSendComplex`; failures log WARN with seq + action only
// (never the bot token, never the event signature) and never block the
// on-disk path.
//
// The package imports only stdlib + `internal/transport/sign` (canonical
// JSON) + `github.com/bwmarrin/discordgo` (transitively, via the narrow
// [MirrorSession] seam).  It does NOT import `internal/discord` so the
// chassis can wire a fake mirror in tests.
//
// See [docs/SECURITY.md] Layer 6 for the audit-log security model.
package audit
