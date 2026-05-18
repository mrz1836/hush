package setup_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// zshUnsafeReadPattern matches the bash-only `read -p` / `read -s`
// constructs that crash zsh on macOS. The pattern is locked at the
// AC-7 contract: hush's guided flow and every doc snippet it ships
// MUST stay zsh-safe by default because macOS users open `zsh`, not
// `bash`, after running `hush init server`.
//
// Word boundaries (\b) on both sides keep the regex from matching
// substrings (e.g. "spread -path").
//
//nolint:gochecknoglobals // shared regex used by every guard sub-test
var zshUnsafeReadPattern = regexp.MustCompile(`\bread\s+-[ps]\b`)

// repoRootFromSetupPkg walks up from internal/cli/setup until it
// finds the go.mod file (the canonical repo root marker). All
// path-globbed guards use this so the test runs the same whether
// invoked via `go test ./...` from the repo root or directly with
// `go test ./internal/cli/setup`.
func repoRootFromSetupPkg(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod walking up from %q", cwd)
		}
		dir = parent
	}
}

// zshGuardScannedGlobs is the locked list of file globs the guard
// walks. Every glob is repo-relative. Adding a new doc surface that
// could ship user-facing shell snippets requires extending this list
// (and is the correct way to add coverage).
//
//nolint:gochecknoglobals // locked guard-scope list shared by sub-tests
var zshGuardScannedGlobs = []string{
	"docs/*.md",
	"README.md",
}

// zshGuardAllowlist enumerates explicit (path, substring) pairs that
// the guard tolerates because the surrounding text is documenting the
// rule itself — not emitting a bash-only snippet. Add new entries
// sparingly and with a comment that explains why the prose is the
// rule's documentation, not a violation.
//
//nolint:gochecknoglobals // locked guard-scope allowlist
var zshGuardAllowlist = []struct {
	path    string // repo-relative
	snippet string // unique substring on the line whose match is tolerated
}{
	// OPERATIONS.md documents the zsh-safety rule itself; the lines
	// below introduce / explain / cross-reference the forbidden
	// constructs and therefore must name them in prose.
	{path: "docs/OPERATIONS.md", snippet: "Bash-only constructs that crash zsh"},
	{path: "docs/OPERATIONS.md", snippet: "instantly bricks the first interaction"},
	{path: "docs/OPERATIONS.md", snippet: "is allowlisted by exact-substring match"},
}

// TestZshSafeSnippetsGuard scans every file matched by
// [zshGuardScannedGlobs] for [zshUnsafeReadPattern] matches and fails
// if any match falls outside [zshGuardAllowlist]. The intent is to
// keep the guided flow and its documentation zsh-safe by default,
// because macOS users land in `zsh` after running `hush init server`
// and a stray `read -p` / `read -s` instantly breaks the operator's
// first interaction with hush (T-273 Hush 101 incident; Plan AC-7).
func TestZshSafeSnippetsGuard(t *testing.T) {
	t.Parallel()
	root := repoRootFromSetupPkg(t)

	for _, glob := range zshGuardScannedGlobs {
		matches, err := filepath.Glob(filepath.Join(root, glob))
		require.NoError(t, err, "glob %q", glob)
		for _, path := range matches {
			scanFileForZshUnsafeSnippet(t, root, path)
		}
	}
}

// scanFileForZshUnsafeSnippet reads path and reports the first
// non-allowlisted [zshUnsafeReadPattern] match via t.Fatalf. Extracted
// from [TestZshSafeSnippetsGuard] so the per-line loop stays under
// gocognit's complexity bar.
func scanFileForZshUnsafeSnippet(t *testing.T, root, path string) {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	require.NoError(t, err)
	body, err := os.ReadFile(path) //nolint:gosec // guard test, paths are repo-relative globs
	require.NoError(t, err, "read %s", rel)
	for lineNo, line := range strings.Split(string(body), "\n") {
		if !zshUnsafeReadPattern.MatchString(line) || isAllowlisted(rel, line) {
			continue
		}
		t.Fatalf(
			"zsh-safety guard violation in %s:%d: line contains bash-only `read -p`/`read -s`\n"+
				"  line: %s\n"+
				"  fix:  use `printf '%%s ' 'prompt:'; read REPLY` (zsh-safe) or rewrite the snippet to avoid interactive read",
			rel, lineNo+1, line)
	}
}

// isAllowlisted reports whether the (repo-relative path, matching
// line) pair is registered in [zshGuardAllowlist]. The check is
// exact-substring on the line so an allowlisted entry cannot
// accidentally whitelist an unrelated occurrence elsewhere in the
// same file.
func isAllowlisted(relPath, line string) bool {
	for _, e := range zshGuardAllowlist {
		if e.path == relPath && strings.Contains(line, e.snippet) {
			return true
		}
	}
	return false
}

// TestZshSafeSnippetsGuard_RegexCatchesPlantedSnippet locks the
// pattern itself: the regex MUST catch the canonical bash-only
// constructs and MUST NOT catch the zsh-safe alternatives. Adding a
// new construct (e.g. `read -r -p`) requires extending the regex AND
// adding it to this fixture.
func TestZshSafeSnippetsGuard_RegexCatchesPlantedSnippet(t *testing.T) {
	t.Parallel()

	mustMatch := []string{
		`read -p "prompt: " VAR`,
		`read -s -p "passphrase: " VAR`,
		`read -s VAR`,
		`    read -p 'leading whitespace ok' VAR`,
	}
	mustNotMatch := []string{
		// zsh-safe equivalents.
		`printf '%s ' 'prompt:'; read REPLY`,
		`IFS= read -r VAR`,
		// Should not match other commands that happen to contain "read".
		`spread -p something`,
		// Should not match `read -r` (read raw — works in zsh).
		`read -r VAR`,
		// Should not match a code block fence containing "read".
		"```bash",
	}

	for _, s := range mustMatch {
		require.True(t, zshUnsafeReadPattern.MatchString(s),
			"regex must catch bash-only snippet: %q", s)
	}
	for _, s := range mustNotMatch {
		require.False(t, zshUnsafeReadPattern.MatchString(s),
			"regex must NOT catch zsh-safe / unrelated snippet: %q", s)
	}
}

// TestZshSafeSnippetsGuard_PlantedBadLineWouldFail verifies the guard
// loop logic: if a bash-only snippet appeared in a scanned file
// without an allowlist entry, the matcher would flag it. The test
// constructs a synthetic file body and runs the same per-line
// matching the production guard uses.
func TestZshSafeSnippetsGuard_PlantedBadLineWouldFail(t *testing.T) {
	t.Parallel()

	body := strings.Join([]string{
		"# Some doc",
		"Run this:",
		"```bash",
		"read -s -p 'enter token: ' TOKEN  # bash-only",
		"```",
	}, "\n")

	var hits []string
	for lineNo, line := range strings.Split(body, "\n") {
		if zshUnsafeReadPattern.MatchString(line) && !isAllowlisted("synthetic.md", line) {
			hits = append(hits, line)
			_ = lineNo
		}
	}
	require.Len(t, hits, 1,
		"the planted bash-only line must be flagged by the same matcher the guard uses")
}
