package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"

	"golang.org/x/term"
)

// ansiSeqRe matches a CSI SGR escape sequence (the ANSI-color form
// used by every terminal styling library). Used by Stream.WriteText to
// strip styling under --no-color.
var ansiSeqRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Stream is one writable destination (stdout or stderr) plus the
// resolved TTY/no-color decision. Constructed once per cli.Execute
// call and threaded through cobra's context to every subcommand.
type Stream struct {
	w       io.Writer
	isTTY   bool
	noColor bool
}

// newStream returns a Stream wrapping w with the supplied isTTY /
// noColor flags. Tests prefer this constructor; production paths use
// streamFor.
func newStream(w io.Writer, isTTY, noColor bool) *Stream {
	return &Stream{w: w, isTTY: isTTY, noColor: noColor}
}

// streamFor wraps an *os.File. The TTY check is performed once at
// construction; --no-color forces noColor=true regardless of the TTY
// state.
func streamFor(f *os.File, noColorFlag bool) *Stream {
	isTTY := false
	if f != nil {
		isTTY = term.IsTerminal(int(f.Fd()))
	}
	noColor := noColorFlag || !isTTY
	return &Stream{w: f, isTTY: isTTY, noColor: noColor}
}

// IsTTY reports whether the underlying file descriptor is a terminal.
func (s *Stream) IsTTY() bool { return s.isTTY }

// WriteText writes a printf-style line to the stream, stripping ANSI
// SGR sequences when noColor is true. Always emits a single trailing
// newline if the formatted text doesn't already end in one.
func (s *Stream) WriteText(format string, args ...any) error {
	out := fmt.Sprintf(format, args...)
	if s.noColor {
		out = ansiSeqRe.ReplaceAllString(out, "")
	}
	if len(out) == 0 || out[len(out)-1] != '\n' {
		out += "\n"
	}
	_, err := io.WriteString(s.w, out)
	return err
}

// WriteJSON marshals v and writes it. On a TTY the encoding uses
// MarshalIndent for human inspection; on a pipe it uses Marshal
// for machine consumption. A single trailing newline always follows.
func (s *Stream) WriteJSON(v any) error {
	var (
		body []byte
		err  error
	)
	if s.isTTY {
		body, err = json.MarshalIndent(v, "", "  ")
	} else {
		body, err = json.Marshal(v)
	}
	if err != nil {
		return fmt.Errorf("hush/cli: encode json: %w", err)
	}
	if _, err = s.w.Write(body); err != nil {
		return err
	}
	_, err = io.WriteString(s.w, "\n")
	return err
}

// Auto picks WriteText on a TTY, WriteJSON on a pipe — the workhorse
// used by version, health, and revoke success rendering.
func (s *Stream) Auto(text string, jsonV any) error {
	if s.isTTY {
		return s.WriteText("%s", text)
	}
	return s.WriteJSON(jsonV)
}

// outputContext bundles the stdout and stderr streams. It is stored
// on the cobra command's context so every subcommand reads the same
// per-stream decision.
type outputContext struct {
	stdout *Stream
	stderr *Stream
}

// outputCtxKey is the cobra-context key used to attach an
// *outputContext to every subcommand invocation.
type outputCtxKey struct{}
