// Package cli is the operator-facing command-line surface of the hush
// binary. It owns the cobra root command, the four global persistent
// flags (--config, --verbose, --quiet, --no-color), the per-stream
// TTY-aware output formatter, the seven public exit-code constants,
// and the four operator subcommands delivered by SDD-14: serve,
// health, version, revoke.
//
// The package's only exported surface is [Execute] (returning the
// process exit code) and the seven Exit* constants. Subcommands are
// registered on the root via unexported entry points; the chassis
// composition for `serve` is a sequence of already-locked surfaces
// from internal/config, internal/keys, internal/vault, internal/audit,
// internal/discord, internal/server.
//
// Constitutional principles in scope:
//   - VII (CLI design standards): cobra subcommands; NO viper; the four
//     global flags; the seven exit codes; TTY-aware output.
//   - IX (idiomatic Go): ctx-first; no init(); no globals carrying
//     mutable runtime state; goroutine ownership obvious.
//   - X (observability & redaction): no secret value, no JWT byte, no
//     signing-key byte ever appears in any output stream. Sentinel-leak
//     tests assert this with SECRET_SHOULD_NEVER_APPEAR_14.
//   - XI (native-first, minimal deps): one new direct dep
//     (github.com/spf13/cobra); one new test-only dep
//     (github.com/creack/pty).
package cli
