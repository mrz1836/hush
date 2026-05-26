//go:build darwin

package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestLogSupervisorRuntimeLimitations_DarwinEmitsWarn covers V3: on
// darwin builds, runLifecycle (via this helper) must emit a WARN log
// naming the death-watch limitation so operators reading logs see the
// platform-specific orphan-child gap. The wording is intentionally
// asserted via substring ("darwin death-watch") so a future rephrase
// still trips the test if the operator-visible signal is dropped.
func TestLogSupervisorRuntimeLimitations_DarwinEmitsWarn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	logger := slog.New(handler)

	logSupervisorRuntimeLimitations(logger)

	out := buf.String()
	if !strings.Contains(out, "darwin death-watch") {
		t.Errorf("expected WARN naming 'darwin death-watch'; got %q", out)
	}
	// Must explicitly call out the unrecoverable supervisor exits.
	for _, want := range []string{"SIGKILL", "OOM-kill", "panic"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected WARN to mention %q; got %q", want, out)
		}
	}
}

// TestLogSupervisorRuntimeLimitations_NilLoggerNoop ensures the helper
// tolerates a nil logger without panicking — defensive contract because
// runLifecycle's callers may invoke during early bootstrap before a
// logger is wired in some test paths.
func TestLogSupervisorRuntimeLimitations_NilLoggerNoop(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("logSupervisorRuntimeLimitations(nil) panicked: %v", r)
		}
	}()
	logSupervisorRuntimeLimitations(nil)
}
