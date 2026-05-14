package testutil

import (
	"io"
	"log/slog"
)

// NewSilentLogger returns a slog.Logger that discards everything below
// WARN. Suitable for tests that need a real *slog.Logger but do not
// care about output.
func NewSilentLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// NewCapturingLogger returns a slog.Logger that writes JSON records to
// dst at the supplied minimum level. Useful for asserting on log
// output in tests.
func NewCapturingLogger(dst io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(dst, &slog.HandlerOptions{Level: level}))
}
