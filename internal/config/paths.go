package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// userHomeDir is the home-directory lookup function. Overridden in tests to
// inject errors for the os.UserHomeDir error path.
//
//nolint:gochecknoglobals // test-injectable: allows coverage of the UserHomeDir error branch
var userHomeDir = os.UserHomeDir

// expandHome replaces a leading "~" (followed by a path separator or EOF) with
// the current user's home directory. Only a bare leading "~" is expanded;
// "~user/..." and "$VAR/..." are treated as literals.
func expandHome(p string) (string, error) {
	if p == "~" {
		home, err := userHomeDir()
		if err != nil {
			return "", fmt.Errorf("expandHome: %w", err)
		}
		return home, nil
	}
	if strings.HasPrefix(p, "~/") {
		home, err := userHomeDir()
		if err != nil {
			return "", fmt.Errorf("expandHome: %w", err)
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// absPath expands a leading "~" and then canonicalises the path via
// filepath.Abs (resolves relative paths against the working directory).
func absPath(p string) (string, error) {
	expanded, err := expandHome(p)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("absPath: %w", err)
	}
	return abs, nil
}

// isUnderStateDir reports whether audit is lexically contained within stateDir.
// Both paths must already be absolute and canonical (filepath.Abs applied).
// Uses filepath.Rel to avoid the string-prefix false-positive where
// stateDir="/foo" would erroneously match audit="/foobar/x".
func isUnderStateDir(audit, stateDir string) bool {
	rel, err := filepath.Rel(stateDir, audit)
	if err != nil {
		return false
	}
	// filepath.Rel succeeds even for paths on different drives (Windows) or
	// out-of-tree paths, returning a path beginning with "..". We reject any
	// relative path that starts with ".." or equals "." (which would mean
	// audit == stateDir, not under it).
	return rel != "." && !strings.HasPrefix(rel, "..")
}
