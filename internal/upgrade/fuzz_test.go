package upgrade

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzValidateExtractPath asserts that validateExtractPath never
// returns a path that escapes destDir, regardless of input. Mirrors
// the mage-x update_fuzz_test.go corpus.
func FuzzValidateExtractPath(f *testing.F) {
	seeds := []struct {
		destDir string
		tarPath string
	}{
		{"/tmp/safe", "file.txt"},
		{"/tmp/safe", "subdir/file.txt"},
		{"/tmp/safe", "deep/nested/dir/file.txt"},

		{"/tmp/safe", "../etc/passwd"},
		{"/tmp/safe", "../../etc/passwd"},
		{"/tmp/safe", "../../../../etc/passwd"},
		{"/tmp/safe", "valid/../../../etc/passwd"},

		{"/tmp/safe", "/etc/passwd"},
		{"/tmp/safe", "/root/.ssh/id_rsa"},

		{"/tmp/safe", "C:\\Windows\\System32"},
		{"/tmp/safe", "\\\\server\\share\\file"},
		{"/tmp/safe", "..\\..\\..\\Windows\\System32"},

		{"/tmp/safe", "../\\../etc/passwd"},
		{"/tmp/safe", "..\\/../etc/passwd"},

		{"/tmp/safe", "..."},
		{"/tmp/safe", "...."},

		{"/tmp/safe", ""},
		{"/tmp/safe", " "},

		{"/tmp/safe", "file\x00.txt"},
		{"/tmp/safe", "../../etc/passwd"},

		{"/tmp/safe", strings.Repeat("../", 100)},
		{"/tmp/safe", strings.Repeat("a/", 100) + "file.txt"},

		{"/", "file.txt"},
		{".", "file.txt"},
	}
	for _, s := range seeds {
		f.Add(s.destDir, s.tarPath)
	}

	f.Fuzz(func(t *testing.T, destDir, tarPath string) {
		got, err := validateExtractPath(destDir, tarPath)
		assertNoAbsoluteEscape(t, tarPath, got, err)
		assertWithinDest(t, destDir, tarPath, got, err)
	})
}

// assertNoAbsoluteEscape fails the test if an absolute tar path was
// accepted; Zip-Slip's primary disguise.
func assertNoAbsoluteEscape(t *testing.T, tarPath, got string, err error) {
	t.Helper()
	if filepath.IsAbs(tarPath) && err == nil {
		t.Errorf("SECURITY: absolute path accepted: %q → %q", tarPath, got)
	}
}

// assertWithinDest fails the test if validateExtractPath returned a
// path whose lexical relation to destDir escapes the directory.
// Non-traversal errors are tolerated (e.g. internal relpath failure)
// — they just don't get inspected here.
func assertWithinDest(t *testing.T, destDir, tarPath, got string, err error) {
	t.Helper()
	if err != nil {
		if !errors.Is(err, ErrPathTraversal) {
			_ = err.Error() // ensure no panic in stringification
		}
		return
	}
	rel, relErr := filepath.Rel(filepath.Clean(destDir), filepath.Clean(got))
	if relErr != nil {
		return
	}
	if strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)+"..") {
		t.Errorf("SECURITY: escape via %q → rel=%q", tarPath, rel)
	}
}
