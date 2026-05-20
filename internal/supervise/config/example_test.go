package config

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// operatorSpecificForbidden is the seed list of identifiers that MUST
// NOT appear in deploy/examples/supervisors/example-daemon.toml. New
// forbidden identifiers are added one at a time as discoveries surface.
// Start empty — no historically-leaked identifiers are known at
// template authoring time.
//
//nolint:gochecknoglobals // sentinel-class: seed list referenced by a single test
var operatorSpecificForbidden = []string{}

// exampleTemplatePath is the relative path from this test file
// (internal/supervise/config/) to the canonical operator-facing supervisor
// TOML template at deploy/examples/supervisors/example-daemon.toml. The
// three ".." segments walk config -> supervise -> internal -> repo-root.
//
//nolint:gochecknoglobals // sentinel-class: relative path shared by two tests in this file
var exampleTemplatePath = filepath.Join(
	"..", "..", "..",
	"deploy", "examples", "supervisors", "example-daemon.toml",
)

// TestExamples_GenericTOMLValidates feeds the canonical operator-facing
// supervisor TOML template through the loader and asserts zero
// validation error.
func TestExamples_GenericTOMLValidates(t *testing.T) {
	t.Parallel()

	sup, err := Load(context.Background(), exampleTemplatePath)
	require.NoError(t, err, "the canonical operator-facing template MUST validate against the loader as-shipped")
	require.NotNil(t, sup)

	assert.Equal(t, "example-daemon", sup.Name)
	assert.Equal(t, "supervisor", sup.SessionType)
}

// TestExamples_NoOperatorSpecificNames asserts that no entry in the
// operatorSpecificForbidden seed list appears anywhere in the
// canonical operator-facing supervisor TOML template. The seed list
// starts empty; new forbidden identifiers are appended one at a time
// as discoveries surface.
func TestExamples_NoOperatorSpecificNames(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(exampleTemplatePath)
	require.NoError(t, err)

	contents := string(body)
	for _, forbidden := range operatorSpecificForbidden {
		assert.False(
			t,
			strings.Contains(contents, forbidden),
			"the canonical operator-facing template MUST NOT contain %q",
			forbidden,
		)
	}
}

// wholeTreeSkipDirs are directories the whole-tree leak check skips:
// frozen archives, VCS metadata, generated trees, IDE state.
//
//nolint:gochecknoglobals // sentinel-class: shared by one test
var wholeTreeSkipDirs = map[string]struct{}{
	".git":          {},
	"specs-archive": {},
	"vendor":        {},
	"node_modules":  {},
	".idea":         {},
	".vscode":       {},
}

// wholeTreeTextExt are file extensions the whole-tree leak check
// scans; everything else is treated as binary and skipped.
//
//nolint:gochecknoglobals // sentinel-class: shared by one test
var wholeTreeTextExt = map[string]struct{}{
	".go":   {},
	".md":   {},
	".toml": {},
	".yml":  {},
	".yaml": {},
	".json": {},
	".sh":   {},
	".txt":  {},
}

// scanFileForForbidden reads path and asserts none of operatorSpecificForbidden
// appears in its contents. Unreadable files are silently skipped (not a leak).
func scanFileForForbidden(t *testing.T, path string) {
	t.Helper()
	body, readErr := os.ReadFile(path)
	if readErr != nil {
		return
	}
	contents := string(body)
	for _, forbidden := range operatorSpecificForbidden {
		assert.False(
			t,
			strings.Contains(contents, forbidden),
			"file %s MUST NOT contain operator-specific token %q",
			path, forbidden,
		)
	}
}

// shouldScanFile returns true if path should be opened and scanned by
// the whole-tree leak gate. Returns false for the test file itself
// and for files whose extension is not a tracked text format.
func shouldScanFile(path, name, thisFileAbs string) bool {
	abs, absErr := filepath.Abs(path)
	if absErr == nil && abs == thisFileAbs {
		return false
	}
	ext := strings.ToLower(filepath.Ext(name))
	_, ok := wholeTreeTextExt[ext]
	return ok
}

// TestExamples_NoOperatorSpecificNames_WholeTree extends the allowlist
// gate to the entire repository tree. It walks every file under the
// repo root, skipping documented exclusions, and asserts no forbidden
// token from operatorSpecificForbidden appears in any text file. The
// seed list is empty by design; the structural value is that this test
// catches a regression the moment a new forbidden token is added.
//
// Documented exclusions:
//   - this test file itself (the seed list literally contains the
//     forbidden tokens it bans elsewhere)
//   - specs-archive/ (frozen historical artifacts)
//   - .git/ (binary objects + pack files; not text)
//
// The walk also skips well-known binary / generated directories that
// `magex test:race` would never re-execute (build outputs, test
// caches, dependency vendor copies). Adding new exclusions outside
// this set MUST be justified in the test comment.
func TestExamples_NoOperatorSpecificNames_WholeTree(t *testing.T) {
	t.Parallel()

	if len(operatorSpecificForbidden) == 0 {
		t.Log("operatorSpecificForbidden is empty; whole-tree check passes trivially. Add a forbidden token to operatorSpecificForbidden to activate.")
		return
	}

	repoRoot := filepath.Join("..", "..", "..")
	thisFile := filepath.Join(repoRoot, "internal", "supervise", "config", "example_test.go")
	thisFileAbs, err := filepath.Abs(thisFile)
	require.NoError(t, err)

	walkErr := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, skip := wholeTreeSkipDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldScanFile(path, d.Name(), thisFileAbs) {
			scanFileForForbidden(t, path)
		}
		return nil
	})
	require.NoError(t, walkErr)
}
