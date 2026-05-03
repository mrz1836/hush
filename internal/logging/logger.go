package logging

import (
	"context"
	"io"
	"log/slog"
	"os"

	"golang.org/x/term"
)

// Format selects the log output format.
type Format int

const (
	FormatAuto Format = iota // auto-detect: text on TTY, JSON otherwise (zero value)
	FormatText               // force human-readable text
	FormatJSON               // force JSON
)

// Options configures a logger constructed by New.
type Options struct {
	Level  slog.Level
	Format Format
	Out    io.Writer
}

// redactingHandler wraps an inner slog.Handler and enforces two redaction rails:
// (1) message-string redaction via RedactString before delegation, and
// (2) source-location suppression for non-ERROR JSON records (PC cleared).
type redactingHandler struct {
	inner  slog.Handler
	format Format // resolved: always FormatText or FormatJSON after New
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle redacts the message and clears PC when source location must be
// suppressed, then delegates to the inner handler.
func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Message = RedactString(r.Message)
	// Suppress source location for: text format (always), or JSON non-ERROR.
	if h.format == FormatText || r.Level < slog.LevelError {
		r.PC = 0
	}
	return h.inner.Handle(ctx, r)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &redactingHandler{inner: h.inner.WithAttrs(attrs), format: h.format}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name), format: h.format}
}

// replaceAttr resolves LogValuer values and runs RedactString over every
// leaf attribute, regardless of its slog.Kind.
//
// Why every kind, not only KindString: a LogValuer (or slog.Any payload) may
// emit a credential via a non-string value — for example a struct whose
// String() method returns a token, a map[string]string with a secret in a
// value, or a []byte that base64-decodes to a key. Restricting redaction to
// KindString attributes leaves a bypass for any of these. Coercing to string,
// redacting, and only replacing when redaction actually rewrote something
// preserves typed logging for the common case (Int64, Bool, Float64, Time,
// Duration) while defending against credential-bearing payloads.
//
// Group attributes are not handled here: slog visits each member of a group
// and calls replaceAttr on the leaves, so group-nested secrets are caught
// by the per-leaf scan.
func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(RedactString(a.Value.String()))
	case slog.KindGroup:
		// Members visited individually by slog; nothing to do at the group level.
	case slog.KindAny, slog.KindBool, slog.KindDuration, slog.KindFloat64,
		slog.KindInt64, slog.KindTime, slog.KindUint64, slog.KindLogValuer:
		original := a.Value.String()
		if redacted := RedactString(original); redacted != original {
			a.Value = slog.StringValue(redacted)
		}
	}
	return a
}

// New constructs a configured *slog.Logger with the package's redaction
// handler chain installed. It never mutates slog.Default.
//
// The zero Options{} produces a logger that writes JSON to os.Stderr at INFO
// level. FormatAuto auto-detects: text for a TTY *os.File, JSON otherwise.
func New(opts Options) *slog.Logger {
	out := opts.Out
	if out == nil {
		out = os.Stderr
	}

	// Resolve FormatAuto to a concrete format, then build the inner handler.
	useText := false
	switch opts.Format {
	case FormatText:
		useText = true
	case FormatJSON:
		useText = false
	case FormatAuto:
		f, ok := out.(*os.File)
		useText = ok && term.IsTerminal(int(f.Fd()))
	}

	resolved := FormatJSON
	var inner slog.Handler
	if useText {
		resolved = FormatText
		inner = slog.NewTextHandler(out, &slog.HandlerOptions{
			Level:       opts.Level,
			AddSource:   false,
			ReplaceAttr: replaceAttr,
		})
	} else {
		inner = slog.NewJSONHandler(out, &slog.HandlerOptions{
			Level:       opts.Level,
			AddSource:   true,
			ReplaceAttr: replaceAttr,
		})
	}

	return slog.New(&redactingHandler{inner: inner, format: resolved})
}
