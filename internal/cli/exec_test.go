package cli

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

func TestEscapeShellSingleQuote(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                "",
		"plain":           "plain",
		"a'b":             `a'\''b`,
		"'":               `'\''`,
		"don't":           `don'\''t`,
		"don't can't":     `don'\''t can'\''t`,
		"with $special#":  "with $special#",
		"new\nline":       "new\nline",
		"contains\\back":  "contains\\back",
		"emojis 🤖 are ok": "emojis 🤖 are ok",
	}
	for in, want := range cases {
		got := escapeShellSingleQuote([]byte(in))
		if got != want {
			t.Errorf("escapeShellSingleQuote(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRenderEvalLine(t *testing.T) {
	t.Parallel()
	if got := renderEvalLine("FOO", []byte("bar")); got != "export FOO='bar'\n" {
		t.Errorf("renderEvalLine(FOO,bar)=%q", got)
	}
	if got := renderEvalLine("X", []byte("a'b")); got != `export X='a'\''b'`+"\n" {
		t.Errorf("renderEvalLine quote-escape=%q", got)
	}
}

//nolint:gocyclo // table-style assertion list
func TestBuildChildEnv_StripsPreExistingScope(t *testing.T) {
	t.Parallel()
	parent := []string{"PATH=/bin", "FOO=bar", "SECRET_A=oldvalue"}
	sb, err := securebytes.New([]byte("newvalue"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = sb.Destroy() }()
	env := buildChildEnv([]string{"SECRET_A"}, []*securebytes.SecureBytes{sb}, parent)

	hasSecretAOld := false
	hasSecretANew := false
	hasFoo := false
	hasPath := false
	for _, kv := range env {
		switch kv {
		case "SECRET_A=oldvalue":
			hasSecretAOld = true
		case "SECRET_A=newvalue":
			hasSecretANew = true
		case "FOO=bar":
			hasFoo = true
		case "PATH=/bin":
			hasPath = true
		}
	}
	if hasSecretAOld {
		t.Errorf("pre-existing SECRET_A=oldvalue not stripped from env")
	}
	if !hasSecretANew {
		t.Errorf("new SECRET_A=newvalue not appended to env")
	}
	if !hasFoo || !hasPath {
		t.Errorf("non-scope parent env entries dropped: env=%v", env)
	}
}

func TestBuildChildEnv_NilSecretSlot(t *testing.T) {
	t.Parallel()
	env := buildChildEnv([]string{"X"}, []*securebytes.SecureBytes{nil}, []string{"PATH=/bin"})
	for _, kv := range env {
		if strings.HasPrefix(kv, "X=") {
			t.Errorf("nil secret slot produced env entry: %q", kv)
		}
	}
}

func TestRunChild_PropagatesExitCode(t *testing.T) {
	t.Parallel()
	deps := requestDeps{
		looker: exec.LookPath,
		runner: func(cmd *exec.Cmd) error { return cmd.Run() },
	}
	stderr := newStream(io.Discard, false, true)
	err := runChild(context.Background(), deps, "/bin/sh", []string{"-c", "exit 5"}, []string{}, stderr)
	var childExit *errChildExitCode
	if !errors.As(err, &childExit) {
		t.Fatalf("expected *errChildExitCode, got %T: %v", err, err)
	}
	if childExit.code != 5 {
		t.Errorf("code=%d want 5", childExit.code)
	}
}

func TestRunChild_LookPathFailure(t *testing.T) {
	t.Parallel()
	deps := requestDeps{
		looker: exec.LookPath,
		runner: func(cmd *exec.Cmd) error { return cmd.Run() },
	}
	stderr := newStream(io.Discard, false, true)
	err := runChild(context.Background(), deps, "definitely-not-on-path-aaa", nil, nil, stderr)
	if err == nil {
		t.Fatalf("want error for missing program, got nil")
	}
}

func TestFormatEvalWarning_ExactBytes(t *testing.T) {
	t.Parallel()
	const want = "WARNING: --format eval prints secret values to stdout. " +
		"They may be captured by terminal scrollback, tmux, or script. " +
		"Use --exec whenever possible.\n"
	if formatEvalWarning != want {
		t.Errorf("formatEvalWarning has drifted from contract:\n  got = %q\n  want= %q", formatEvalWarning, want)
	}
}
