// Package logging provides the project's single slog.Logger constructor and
// the credential-redaction primitives every log call passes through.
//
// All loggers produced by [New] enforce two independent redaction rails:
//
//  1. Message redaction: slog.Record.Message is scanned by [RedactString]
//     before delegation to the inner handler.
//  2. Attribute redaction: a ReplaceAttr hook resolves any LogValuer and
//     coerces every leaf attribute (regardless of slog.Kind — string, any,
//     int, byte slice, struct via Stringer) through [RedactString], so a
//     non-string LogValuer cannot smuggle a credential past the rail.
//
// The compiled regex set targets the credential classes enumerated in
// docs/SECURITY.md §1.1 (Anthropic, OpenAI prefix-tagged + legacy, GitHub
// PAT/OAuth/user-to-server/server-to-server/refresh, AWS access key, Google
// API/AI keys, Slack bot/user/app/refresh + webhook URLs, PEM private key
// blocks). The list is read-only after first use; callers must NOT mutate.
//
// Constitutional principles in scope: IX (context-first, no init, no
// globals beyond sentinels — the regex set is sync.Once-gated), X (no
// secrets in logs — the redaction rail is the package's reason for being).
//
// # Concurrency
//
// Loggers produced by New are safe for concurrent use (inherited from
// slog.Logger). Each call returns an independent logger; slog.Default is
// never mutated by this package.
//
// # Exported entry points
//
//   - [New] — constructs a configured *slog.Logger with redaction installed.
//   - [Options] — controls level, format (text/json/auto-by-tty), and writer.
//   - [Format], [FormatAuto], [FormatText], [FormatJSON] — format selectors.
//   - [RedactString] — applies the regex set to a string; idempotent.
//   - [RedactPatterns] — exported (read-only) compiled regex slice; nil
//     until first use.
//
// # Usage sketch
//
//	logger := logging.New(logging.Options{
//	    Format: logging.FormatAuto, // text on TTY, JSON otherwise
//	    Level:  slog.LevelInfo,
//	})
//	logger.Info("user login", slog.String("username", "alice"))
package logging
