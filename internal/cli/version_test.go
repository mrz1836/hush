package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestExecute_VersionPrintsBuildVersion asserts injected
// Version/Commit/Date appear in TTY-mode output.
func TestExecute_VersionPrintsBuildVersion(t *testing.T) {
	prev := snapshotBuildVars()
	t.Cleanup(func() { restoreBuildVars(prev) })
	Version, Commit, Date = "v0.1.0", "abc1234", "2026-05-01T12:34:56Z"

	ctx, stdout := newVersionTestCtx(true)
	cmd := newVersionCmd()
	cmd.SetContext(ctx)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	got := stdout.String()
	for _, want := range []string{"v0.1.0", "abc1234", "2026-05-01T12:34:56Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, got)
		}
	}
}

// TestVersion_NonTTYJSONShape_ThreeKeys asserts the locked JSON
// shape with three keys in version/commit/date order.
func TestVersion_NonTTYJSONShape_ThreeKeys(t *testing.T) {
	prev := snapshotBuildVars()
	t.Cleanup(func() { restoreBuildVars(prev) })
	Version, Commit, Date = "v0.1.0", "abc1234", "2026-05-01T12:34:56Z"

	ctx, stdout := newVersionTestCtx(false)
	cmd := newVersionCmd()
	cmd.SetContext(ctx)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	raw := strings.TrimSpace(stdout.String())
	// JSON-key order locked.
	wantPrefix := `{"version":"v0.1.0","commit":"abc1234","date":"2026-05-01T12:34:56Z"}`
	if raw != wantPrefix {
		t.Errorf("JSON output = %q, want %q", raw, wantPrefix)
	}
	var doc map[string]string
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if len(doc) != 3 {
		t.Errorf("doc has %d keys, want 3: %v", len(doc), doc)
	}
}

// TestVersion_DevPlaceholderWhenUnset asserts dev/unknown/unknown
// defaults render correctly when -ldflags is not used.
func TestVersion_DevPlaceholderWhenUnset(t *testing.T) {
	prev := snapshotBuildVars()
	t.Cleanup(func() { restoreBuildVars(prev) })
	Version, Commit, Date = "dev", "unknown", "unknown"

	ctx, stdout := newVersionTestCtx(false)
	cmd := newVersionCmd()
	cmd.SetContext(ctx)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	want := `{"version":"dev","commit":"unknown","date":"unknown"}`
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestVersion_AlwaysExitsOK asserts version returns nil error so
// mapErr → ExitOK.
func TestVersion_AlwaysExitsOK(t *testing.T) {
	t.Parallel()
	ctx, _ := newVersionTestCtx(false)
	cmd := newVersionCmd()
	cmd.SetContext(ctx)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE returned err=%v, want nil (mapErr=%d)", err, mapErr(err))
	}
}

// TestVersion_NoColorIrrelevant asserts version output has no ANSI
// styling regardless of --no-color.
func TestVersion_NoColorIrrelevant(t *testing.T) {
	t.Parallel()
	ctx, stdout := newVersionTestCtx(true)
	cmd := newVersionCmd()
	cmd.SetContext(ctx)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.ContainsRune(stdout.String(), '\x1b') {
		t.Errorf("version output contains ANSI: %q", stdout.String())
	}
}

func snapshotBuildVars() [3]string {
	return [3]string{Version, Commit, Date}
}

func restoreBuildVars(s [3]string) {
	Version, Commit, Date = s[0], s[1], s[2]
}

func newVersionTestCtx(isTTY bool) (context.Context, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	out := &outputContext{
		stdout: newStream(&stdout, isTTY, !isTTY),
		stderr: newStream(&stderr, false, true),
	}
	ctx := context.WithValue(context.Background(), outputCtxKey{}, out)
	_ = stderr
	return ctx, &stdout
}
