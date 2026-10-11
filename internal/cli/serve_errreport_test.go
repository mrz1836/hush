package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrz1836/hush/internal/vault"
)

// TestPrintServeErr locks the one-line stderr report a failed serve leaves
// behind, so a startup failure is never a silent non-zero exit.
func TestPrintServeErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantLine string // substring of the report; "" means no output at all
		wantHint bool
	}{
		{name: "nil", err: nil},
		{name: "clean shutdown", err: fmt.Errorf("serve: %w", context.Canceled)},
		{name: "usage error already reported", err: &usageError{cmdPath: "hush serve", err: errors.New("bad flag")}},
		{
			name:     "rejected passphrase",
			err:      fmt.Errorf("vault: %w", vault.ErrAuthFailed),
			wantLine: "hush: serve: vault: hush/vault: authentication failed",
			wantHint: true,
		},
		{
			name:     "other startup failure flattened to one line",
			err:      errors.New("bind 100.64.0.1:7744: address in use\nretry later"),
			wantLine: "hush: serve: bind 100.64.0.1:7744: address in use retry later",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			printServeErr(newStream(&buf, false, true), tt.err)
			assertServeErrReport(t, buf.String(), tt.wantLine, tt.wantHint)
		})
	}
}

// assertServeErrReport checks a printServeErr report: no output at all when
// wantLine is empty, otherwise wantLine as a whole line plus the passphrase
// hint exactly when wantHint is set.
func assertServeErrReport(t *testing.T, got, wantLine string, wantHint bool) {
	t.Helper()
	if wantLine == "" {
		if got != "" {
			t.Fatalf("expected no output, got %q", got)
		}
		return
	}
	if !strings.Contains(got, wantLine+"\n") {
		t.Errorf("report = %q, want line %q", got, wantLine)
	}
	if hinted := strings.Contains(got, "passphrase was rejected"); hinted != wantHint {
		t.Errorf("passphrase hint present = %v, want %v (report %q)", hinted, wantHint, got)
	}
}

// TestServeCmd_ReportsStartupFailure asserts the serve command itself prints
// its startup failure to stderr (not only returning it for the exit code).
func TestServeCmd_ReportsStartupFailure(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	ctx := context.WithValue(t.Context(), outputCtxKey{}, &outputContext{
		stdout: newStream(&stdout, false, true),
		stderr: newStream(&stderr, false, true),
	})
	cmd := newServeCmd()
	cmd.Flags().String(flagConfig, "", "")
	cmd.SetArgs([]string{"--" + flagConfig, filepath.Join(t.TempDir(), "missing.toml")})

	if err := cmd.ExecuteContext(ctx); err == nil {
		t.Fatal("expected a startup error for a missing config, got nil")
	}
	if !strings.HasPrefix(stderr.String(), "hush: serve: ") {
		t.Errorf("stderr = %q, want a 'hush: serve: ' startup report", stderr.String())
	}
}
