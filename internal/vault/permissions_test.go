package vault

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

//nolint:gocognit // table-driven permission test; complexity is structural
func TestCheckFileMode_ExactEquality(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)

	table := []struct {
		mode    fs.FileMode
		want    fs.FileMode
		wantErr bool
	}{
		{0o600, 0o600, false},
		{0o644, 0o600, true},
		{0o400, 0o600, true}, // stricter also fails
		{0o700, 0o600, true},
		{0o666, 0o600, true},
	}

	for _, tc := range table {
		t.Run(tc.mode.String(), func(t *testing.T) {
			path := filepath.Join(dir, "file_"+tc.mode.String())
			f, err := os.Create(path) //nolint:gosec // test-controlled path
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if err = f.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if err = os.Chmod(path, tc.mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}

			err = checkFileMode(path, tc.want)
			if tc.wantErr {
				if !errors.Is(err, ErrFilePermsLoose) {
					t.Fatalf("want ErrFilePermsLoose, got %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("want nil error, got %v", err)
				}
			}
		})
	}
}

//nolint:gocognit // table-driven permission test; complexity is structural
func TestCheckParentMode_ExactEquality(t *testing.T) {
	t.Parallel()
	baseDir := makeTestDir(t)

	table := []struct {
		parentMode fs.FileMode
		want       fs.FileMode
		wantErr    bool
	}{
		{0o700, 0o700, false},
		{0o755, 0o700, true},
		{0o770, 0o700, true},
		{0o500, 0o700, true}, // stricter also fails
		{0o777, 0o700, true},
	}

	for _, tc := range table {
		t.Run(tc.parentMode.String(), func(t *testing.T) {
			// Create a sub-directory, add a file, THEN chmod to target mode.
			subDir := filepath.Join(baseDir, "parent_"+tc.parentMode.String())
			if err := os.Mkdir(subDir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			filePath := filepath.Join(subDir, "f")
			f, err := os.Create(filePath) //nolint:gosec // test-controlled path
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if err = f.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if err = os.Chmod(subDir, tc.parentMode); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(subDir, 0o700) }) //nolint:gosec // 0700 is correct for directories

			err = checkParentMode(filePath, tc.want)
			if tc.wantErr {
				if !errors.Is(err, ErrFilePermsLoose) {
					t.Fatalf("want ErrFilePermsLoose, got %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("want nil error, got %v", err)
				}
			}
		})
	}
}
