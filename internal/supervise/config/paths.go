package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// userHomeDir is the home-directory lookup function. Overridden in tests to
// inject errors for the os.UserHomeDir error path. $HOME is non-secret per
// Constitution X (see FR-015 — only env-touching call permitted).
//
//nolint:gochecknoglobals // test-injectable: allows coverage of the UserHomeDir error branch
var userHomeDir = os.UserHomeDir

// expandHome replaces a leading "~" (followed by a path separator or EOF) with
// the current user's home directory. Only a bare leading "~" is expanded;
// "~user/..." and "$VAR/..." are treated as literals. Mirrors the
// internal/config (SDD-06) helper — duplicated rather than shared per
// research.md R-010 (Constitution IX prefers tiny duplication over thin
// abstractions until a third caller emerges).
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
// The lexical form (post-tilde-expansion, pre-Abs) is rejected as
// non-clean if filepath.Clean would have changed it — this catches
// "..", duplicate slashes, and trailing "/.". Defense-in-depth against
// operator typos and confused-deputy paths.
func absPath(p string) (string, error) {
	expanded, err := expandHome(p)
	if err != nil {
		return "", err
	}
	if cleaned := filepath.Clean(expanded); cleaned != expanded {
		return "", fmt.Errorf("%w: %q (cleaned form would be %q)", ErrPathNotClean, expanded, cleaned)
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("absPath: %w", err)
	}
	return abs, nil
}
