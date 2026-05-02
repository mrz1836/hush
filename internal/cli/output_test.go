package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestOutput_TTYPicksText asserts Auto picks WriteText on a TTY.
func TestOutput_TTYPicksText(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := newStream(&buf, true, false)
	if err := s.Auto("hello world", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("Auto: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "hello world") || strings.Contains(got, "{") {
		t.Fatalf("Auto on TTY emitted JSON or missed text: %q", got)
	}
}

// TestOutput_NonTTYPicksJSON asserts Auto picks WriteJSON on a pipe.
func TestOutput_NonTTYPicksJSON(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := newStream(&buf, false, true)
	if err := s.Auto("hello world", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("Auto: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "hello world") {
		t.Fatalf("Auto on pipe emitted text: %q", got)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &decoded); err != nil {
		t.Fatalf("decode JSON: %v (raw=%q)", err, got)
	}
	if decoded["k"] != "v" {
		t.Fatalf("decoded[k] = %q, want v", decoded["k"])
	}
}

// TestOutput_NoColorStripsANSI asserts WriteText with noColor=true
// strips ANSI SGR sequences.
func TestOutput_NoColorStripsANSI(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := newStream(&buf, true, true)
	if err := s.WriteText("\x1b[31mred\x1b[0m"); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	got := buf.String()
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("ANSI sequence leaked through --no-color: %q", got)
	}
	if !strings.Contains(got, "red") {
		t.Fatalf("payload text dropped: %q", got)
	}
}

// TestOutput_PerStreamDecision asserts stdout TTY + stderr pipe
// produces text on stdout and JSON on stderr — diagnostics never
// bleed onto stdout.
func TestOutput_PerStreamDecision(t *testing.T) {
	t.Parallel()
	var stdoutBuf, stderrBuf bytes.Buffer
	stdout := newStream(&stdoutBuf, true, false)
	stderr := newStream(&stderrBuf, false, true)
	if err := stdout.Auto("text-on-tty", map[string]string{"a": "1"}); err != nil {
		t.Fatalf("stdout.Auto: %v", err)
	}
	if err := stderr.Auto("nope-not-this", map[string]string{"b": "2"}); err != nil {
		t.Fatalf("stderr.Auto: %v", err)
	}
	if !strings.Contains(stdoutBuf.String(), "text-on-tty") {
		t.Fatalf("stdout: %q", stdoutBuf.String())
	}
	if strings.Contains(stderrBuf.String(), "nope-not-this") {
		t.Fatalf("stderr leaked text: %q", stderrBuf.String())
	}
}

// TestOutput_JSONIndentOnTTY asserts WriteJSON on a TTY uses
// MarshalIndent and on a pipe uses Marshal.
func TestOutput_JSONIndentOnTTY(t *testing.T) {
	t.Parallel()
	doc := map[string]string{"a": "1"}

	var ttyBuf bytes.Buffer
	if err := newStream(&ttyBuf, true, false).WriteJSON(doc); err != nil {
		t.Fatalf("WriteJSON tty: %v", err)
	}
	if !strings.Contains(ttyBuf.String(), "\n  \"a\"") {
		t.Fatalf("tty json not indented: %q", ttyBuf.String())
	}

	var pipeBuf bytes.Buffer
	if err := newStream(&pipeBuf, false, true).WriteJSON(doc); err != nil {
		t.Fatalf("WriteJSON pipe: %v", err)
	}
	got := pipeBuf.String()
	if strings.Contains(got, "\n  \"a\"") {
		t.Fatalf("pipe json got indented: %q", got)
	}
	if got[len(got)-1] != '\n' {
		t.Fatalf("pipe json missing trailing newline: %q", got)
	}
}
