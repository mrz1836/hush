package vault

import (
	"fmt"
	"io/fs"
	"os"
)

// checkFileMode verifies that path's permission bits equal want exactly.
// Used by tests and by Load's inline mode check (the inline avoids a second os.Stat).
func checkFileMode(path string, want fs.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("vault: stat %q: %w", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		return fmt.Errorf("vault: file %q mode %#o != %#o: %w", path, got, want, ErrFilePermsLoose)
	}
	return nil
}

// checkParentMode verifies that path's parent directory permission bits equal want exactly.
func checkParentMode(path string, want fs.FileMode) error {
	parent := parentDir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("vault: stat parent %q: %w", parent, err)
	}
	if got := info.Mode().Perm(); got != want {
		return fmt.Errorf("vault: parent %q mode %#o != %#o: %w", parent, got, want, ErrFilePermsLoose)
	}
	return nil
}
