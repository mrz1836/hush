package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// pipeFile makes an os.Pipe-backed *os.File primed with the supplied
// payload. Used by the stdin-pipe passphrase tests.
func pipeFile(t *testing.T, payload []byte) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	go func() {
		defer func() { _ = w.Close() }()
		_, _ = w.Write(payload)
	}()
	return r
}

// TestServe_PassphraseFromStdinPipe asserts the pipe-read path
// honors POSIX-line semantics.
func TestServe_PassphraseFromStdinPipe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		payload []byte
		want    []byte
	}{
		{"bare \\n", []byte("correct horse\n"), []byte("correct horse")},
		{"bare \\r\\n", []byte("correct horse\r\n"), []byte("correct horse")},
		{"two trailing \\n preserves one", []byte("correct horse\n\n"), []byte("correct horse\n")},
		{"leading whitespace preserved", []byte("  pass\n"), []byte("  pass")},
		{"no trailing newline", []byte("correct horse"), []byte("correct horse")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			in := pipeFile(t, c.payload)
			var prompt bytes.Buffer
			sb, err := resolvePassphrase(t.Context(), in, &prompt)
			if err != nil {
				t.Fatalf("resolvePassphrase: %v", err)
			}
			defer func() { _ = sb.Destroy() }()
			if got := readSecure(sb); !bytes.Equal(got, c.want) {
				t.Errorf("payload=%q: got %q want %q", c.payload, got, c.want)
			}
			if prompt.Len() != 0 {
				t.Errorf("prompt should not be written on pipe path: %q", prompt.String())
			}
		})
	}
}

// readSecure copies the underlying bytes out of a SecureBytes for
// assertion. NEVER use in production.
func readSecure(sb *securebytes.SecureBytes) []byte {
	var got []byte
	_ = sb.Use(func(b []byte) {
		got = append([]byte(nil), b...)
	})
	return got
}

// TestServe_PassphraseFromTTYPrompt drives a real PTY to assert the
// no-echo property of term.ReadPassword.
func TestServe_PassphraseFromTTYPrompt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping pty test in short mode")
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("pty.Open unavailable on this platform: %v", err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})

	// Write the passphrase to the master end of the PTY.
	const passphrase = "correct horse battery"
	doneRead := make(chan struct{})
	go func() {
		defer close(doneRead)
		_, _ = master.WriteString(passphrase + "\n")
	}()

	var prompt bytes.Buffer
	sb, err := resolvePassphrase(t.Context(), slave, &prompt)
	<-doneRead
	if err != nil {
		t.Fatalf("resolvePassphrase: %v", err)
	}
	defer func() { _ = sb.Destroy() }()
	if got := readSecure(sb); string(got) != passphrase {
		t.Errorf("got %q, want %q", got, passphrase)
	}
	if !strings.Contains(prompt.String(), "Vault passphrase:") {
		t.Errorf("prompt missing label: %q", prompt.String())
	}
}

// TestServe_NoStdinNoTTY_ExitInputErr asserts a regular file as
// stdin (neither pipe nor TTY) → errNoPassphraseSource. We use a
// tempfile in this test which IS a regular file (mode bits show no
// ModeCharDevice) — so the function reads it like a pipe.
//
// To exercise the "neither pipe nor TTY" path, we open /dev/null
// explicitly, which has ModeCharDevice but is not a real terminal.
func TestServe_NoStdinNoTTY_ExitInputErr(t *testing.T) {
	t.Parallel()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open /dev/null: %v", err)
	}
	t.Cleanup(func() { _ = devNull.Close() })

	var prompt bytes.Buffer
	_, err = resolvePassphrase(t.Context(), devNull, &prompt)
	if !errors.Is(err, errNoPassphraseSource) {
		t.Errorf("err = %v, want errNoPassphraseSource", err)
	}
}

// TestServe_ZeroByteStdinPipe asserts an empty pipe → errNoPassphraseSource.
func TestServe_ZeroByteStdinPipe(t *testing.T) {
	t.Parallel()
	in := pipeFile(t, nil)
	var prompt bytes.Buffer
	_, err := resolvePassphrase(t.Context(), in, &prompt)
	if !errors.Is(err, errNoPassphraseSource) {
		t.Errorf("err = %v, want errNoPassphraseSource", err)
	}
}

// TestStripPOSIXLineEnd directly covers the helper.
func TestStripPOSIXLineEnd(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"x\n", "x"},
		{"x\r\n", "x"},
		{"x\n\n", "x\n"},
		{"x", "x"},
		{"", ""},
		{"\r\n", ""},
		{"\n", ""},
	}
	for _, c := range cases {
		got := stripPOSIXLineEnd([]byte(c.in))
		if string(got) != c.want {
			t.Errorf("strip(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLoadBotToken_ItemNameValidation asserts invalid item names
// fail before any subprocess runs.
func TestLoadBotToken_ItemNameValidation(t *testing.T) {
	t.Setenv("HUSH_DISCORD_BOT_TOKEN", "")
	bad := []string{
		"foo;rm -rf /",
		"foo bar",
		"foo&",
		"$VAR",
		strings.Repeat("a", 129),
		"",
	}
	for _, item := range bad {
		_, err := loadBotToken(t.Context(), item, "")
		if err == nil {
			t.Errorf("loadBotToken(%q) succeeded, want error", item)
		}
	}
}

func TestLoadBotToken_EnvFallbackBeforeKeychain(t *testing.T) {
	t.Setenv("HUSH_DISCORD_BOT_TOKEN", "smoke-token")

	got, err := loadBotToken(t.Context(), "hush-nonexistent-test-item", "")
	if err != nil {
		t.Fatalf("loadBotToken: %v", err)
	}
	defer func() { _ = got.Destroy() }()

	if err := got.Use(func(b []byte) {
		if string(b) != "smoke-token" {
			t.Fatalf("token = %q, want smoke-token", string(b))
		}
	}); err != nil {
		t.Fatalf("token use: %v", err)
	}
}

// TestServe_OutputNoSentinel asserts the SECRET sentinel planted as
// the piped passphrase never appears on captured stderr.
func TestServe_OutputNoSentinel(t *testing.T) {
	t.Parallel()
	sentinel := testutil.SentinelSecret(14)
	in := pipeFile(t, []byte(sentinel+"\n"))
	var stderr bytes.Buffer
	sb, err := resolvePassphrase(t.Context(), in, &stderr)
	if err != nil {
		t.Fatalf("resolvePassphrase: %v", err)
	}
	defer func() { _ = sb.Destroy() }()
	testutil.AssertSentinelAbsent(t, sentinel, stderr.String())
}

// TestExpandTilde covers the leading-~ expansion helper.
func TestExpandTilde(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	cases := []struct {
		in, want string
	}{
		{"~", home},
		{"~/x/y.toml", filepath.Join(home, "x", "y.toml")},
		{"/abs/path", "/abs/path"},
		{"relative/path", "relative/path"},
	}
	for _, c := range cases {
		got, err := expandTilde(c.in)
		if err != nil {
			t.Fatalf("expandTilde(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("expandTilde(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestReadVaultSalt asserts the helper extracts bytes 5..21 from a
// vault file header.
func TestReadVaultSalt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0o700 is the chassis-required state-dir mode
		t.Fatalf("chmod: %v", err)
	}
	path := filepath.Join(dir, "secrets.vault")
	header := make([]byte, 0, 33)
	header = append(header, 0x48, 0x55, 0x53, 0x48, 0x01)
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i + 1)
	}
	header = append(header, salt...)
	header = append(header, make([]byte, 12)...) // nonce
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := readVaultSalt(path)
	if err != nil {
		t.Fatalf("readVaultSalt: %v", err)
	}
	if !bytes.Equal(got, salt) {
		t.Errorf("salt = %x, want %x", got, salt)
	}
}

// TestRunServe_MissingConfig_ExitInputErr asserts the config-load
// failure path returns an error mapping to ExitInputErr (or
// ExitNotFound for the missing-file subclass).
func TestRunServe_MissingConfig_ExitInputErr(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	deps := serveDeps{
		configPath: filepath.Join(t.TempDir(), "nonexistent.toml"),
		passphraseSource: func(_ context.Context, _ *os.File, _ io.Writer) (*securebytes.SecureBytes, error) {
			return securebytes.New([]byte("never reached"))
		},
		approverFactory: testApproverFactory,
	}
	err := runServe(t.Context(), out, errStream, deps)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	got := mapErr(err)
	if got != ExitNotFound && got != ExitInputErr {
		t.Errorf("mapErr = %d, want ExitInputErr or ExitNotFound", got)
	}
}

func TestWatchVaultChanges_ReloadsAfterVaultRewrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "secrets.vault")
	requireWriteFile(t, path, []byte("old"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reloaded := make(chan struct{}, 1)
	watchVaultChanges(ctx, nil, path, 10*time.Millisecond, 10*time.Millisecond, func(context.Context) error {
		reloaded <- struct{}{}
		return nil
	})

	requireWriteFile(t, path, []byte("new-value"))
	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("vault change watcher did not trigger reload")
	}
}

func TestWatchVaultChanges_DebouncesRapidRewrites(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "secrets.vault")
	requireWriteFile(t, path, []byte("old"))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reloaded := make(chan struct{}, 4)
	watchVaultChanges(ctx, nil, path, 5*time.Millisecond, 75*time.Millisecond, func(context.Context) error {
		reloaded <- struct{}{}
		return nil
	})

	requireWriteFile(t, path, []byte("new-1"))
	time.Sleep(15 * time.Millisecond)
	requireWriteFile(t, path, []byte("new-2-longer"))

	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("vault change watcher did not trigger reload")
	}
	select {
	case <-reloaded:
		t.Fatal("rapid rewrites produced duplicate reloads")
	case <-time.After(150 * time.Millisecond):
	}
}

func requireWriteFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
