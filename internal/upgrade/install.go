package upgrade

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// installFilePerm is the executable permission applied to the
// installed binary. 0o755 mirrors the goreleaser default.
const installFilePerm os.FileMode = 0o755

// copyFile copies src to dst, fsyncing the destination. The caller is
// expected to have already created the destination directory.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src) //nolint:gosec // src controlled by caller (extract tmpdir)
	if err != nil {
		return fmt.Errorf("hush/upgrade: open src: %w", err)
	}
	defer func() { _ = srcFile.Close() }()

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, installFilePerm) //nolint:gosec // dst controlled by caller (install dir)
	if err != nil {
		return fmt.Errorf("hush/upgrade: create dst: %w", err)
	}

	if _, copyErr := io.Copy(dstFile, srcFile); copyErr != nil {
		_ = dstFile.Close()
		return fmt.Errorf("hush/upgrade: copy: %w", copyErr)
	}
	if syncErr := dstFile.Sync(); syncErr != nil {
		_ = dstFile.Close()
		return fmt.Errorf("hush/upgrade: fsync: %w", syncErr)
	}
	if closeErr := dstFile.Close(); closeErr != nil {
		return fmt.Errorf("hush/upgrade: close dst: %w", closeErr)
	}
	// Explicit chmod overrides any umask that masked the OpenFile
	// mode argument.
	if chmodErr := os.Chmod(dst, installFilePerm); chmodErr != nil {
		return fmt.Errorf("hush/upgrade: chmod dst: %w", chmodErr)
	}
	return nil
}

// installBinaryFallback writes src into dst via a sibling temp file
// then renames it into place. This avoids truncating a running binary
// in-place on Linux (which would corrupt its memory map and raise
// SIGBUS). The src file is best-effort removed after success.
//
// Returns the original copy/rename error verbatim so callers can
// errors.Is against the io / os sentinels.
func installBinaryFallback(src, dst string) error {
	tmpPath := dst + ".new"

	if copyErr := copyFile(src, tmpPath); copyErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("hush/upgrade: stage binary: %w", copyErr)
	}

	if renErr := os.Rename(tmpPath, dst); renErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("hush/upgrade: rename into place: %w", renErr)
	}

	// chmod after rename in case the destination filesystem stripped
	// the mode bits (some network filesystems do this on create).
	if chmodErr := os.Chmod(dst, installFilePerm); chmodErr != nil {
		return fmt.Errorf("hush/upgrade: chmod installed binary: %w", chmodErr)
	}

	// Best-effort cleanup; the .new file no longer exists at this
	// point but src (the extracted temp binary) does. Failure is
	// logged but never propagated — the install already succeeded.
	_ = os.Remove(src)
	return nil
}

// probeInstallDirWritable verifies that the directory containing the
// final install target can be written to before any download begins.
// Surfaces [ErrInstallDirNotWritable] wrapped with the resolved path
// so the operator can copy/paste a sudo command.
func probeInstallDirWritable(installPath string) error {
	dir := filepath.Dir(installPath)
	probe := filepath.Join(dir, ".hush-upgrade-probe")

	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // probe path constructed locally
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w: %s", ErrInstallDirNotWritable, dir)
		}
		// Other create failures (e.g. ENOSPC, ENOENT for a missing
		// dir) are still install-blocking — surface them under the
		// same sentinel so the cobra layer can render one consistent
		// message.
		return fmt.Errorf("%w: %s: %w", ErrInstallDirNotWritable, dir, err)
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return nil
}
