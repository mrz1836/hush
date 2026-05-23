package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxUpdateFileSize caps any single file extracted from a release
// tarball at 500 MiB. The hush binary is ~14 MiB today; the ceiling
// is well above realistic future growth and well below the zip-bomb
// danger zone.
const maxUpdateFileSize = 500 * 1024 * 1024

// extractDirPerm is the directory mode applied to subdirectories
// created during extraction. 0o700 mirrors the sensitive-file
// permission used throughout hush so a partial extract never widens
// access to intermediate state.
const extractDirPerm os.FileMode = 0o700

// validateExtractPath joins a tarball entry name with destDir and
// returns the resulting absolute path only if it stays inside destDir.
// Absolute entries and any traversal pattern (.., ./.., etc.) return
// [ErrPathTraversal]. This is the Zip-Slip defence boundary.
func validateExtractPath(destDir, tarPath string) (string, error) {
	if filepath.IsAbs(tarPath) {
		return "", fmt.Errorf("%w: absolute path not allowed: %s", ErrPathTraversal, tarPath)
	}

	destDir = filepath.Clean(destDir)
	targetPath := filepath.Clean(filepath.Join(destDir, tarPath))

	relPath, err := filepath.Rel(destDir, targetPath)
	if err != nil {
		return "", fmt.Errorf("hush/upgrade: relative path: %w", err)
	}

	if strings.HasPrefix(relPath, "..") || strings.Contains(relPath, string(filepath.Separator)+"..") {
		return "", fmt.Errorf("%w: %s", ErrPathTraversal, tarPath)
	}

	return targetPath, nil
}

// normalizeFileMode strips dangerous bits (setuid, setgid, sticky)
// and collapses the access mode to either 0o755 (any execute bit set)
// or 0o644 (no execute bit). This prevents a malicious tarball from
// shipping a 0o4777 setuid binary or anything else exotic.
func normalizeFileMode(mode os.FileMode) os.FileMode {
	mode &^= os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// extractTarGz extracts every regular file in src into dest. Each
// entry's path is validated with validateExtractPath, its permissions
// normalized, and its body capped at maxUpdateFileSize. Directory
// entries are skipped — destination directories are created on demand
// per file so a malformed dir entry can't widen the extract surface.
func extractTarGz(src, dest string) error {
	f, err := os.Open(src) //nolint:gosec // src constructed inside this package from MkdirTemp
	if err != nil {
		return fmt.Errorf("hush/upgrade: open archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("hush/upgrade: gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	return extractAllEntries(tar.NewReader(gz), dest)
}

// extractAllEntries walks every header in tr and dispatches regular
// files to extractOneFile. Split out so extractTarGz stays under the
// gocognit budget.
func extractAllEntries(tr *tar.Reader, dest string) error {
	for {
		header, herr := tr.Next()
		if errors.Is(herr, io.EOF) {
			return nil
		}
		if herr != nil {
			return fmt.Errorf("hush/upgrade: tar header: %w", herr)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if eerr := extractOneFile(tr, header, dest); eerr != nil {
			return eerr
		}
	}
}

// extractOneFile writes a single tarball entry to disk after path
// validation, directory creation, and size capping. Split out from
// extractTarGz so each call site fits inside the funlen/gocognit
// budgets enforced by the linter.
func extractOneFile(tr *tar.Reader, header *tar.Header, dest string) error {
	destPath, err := validateExtractPath(dest, header.Name)
	if err != nil {
		// Skip malicious entries rather than aborting the whole
		// archive — mirrors mage-x behaviour so a single hostile
		// entry does not deny service for the legitimate ones.
		if errors.Is(err, ErrPathTraversal) {
			return nil
		}
		return err
	}

	if dirErr := os.MkdirAll(filepath.Dir(destPath), extractDirPerm); dirErr != nil {
		return fmt.Errorf("hush/upgrade: mkdir for %s: %w", destPath, dirErr)
	}

	mode := normalizeFileMode(os.FileMode(header.Mode & 0o7777))

	destFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) //nolint:gosec // destPath validated above
	if err != nil {
		return fmt.Errorf("hush/upgrade: create %s: %w", destPath, err)
	}

	limited := io.LimitReader(tr, maxUpdateFileSize)
	n, copyErr := io.Copy(destFile, limited)
	closeErr := destFile.Close()

	if n >= maxUpdateFileSize {
		_ = os.Remove(destPath)
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrFileTooLarge, header.Name, maxUpdateFileSize)
	}
	if copyErr != nil {
		return fmt.Errorf("hush/upgrade: extract %s: %w", destPath, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("hush/upgrade: close %s: %w", destPath, closeErr)
	}
	// Chmod explicitly because os.OpenFile honors the caller's
	// umask. We want the goreleaser-shipped 0o755 even when the user
	// runs with a tighter umask.
	if chmodErr := os.Chmod(destPath, mode); chmodErr != nil {
		return fmt.Errorf("hush/upgrade: chmod %s: %w", destPath, chmodErr)
	}
	return nil
}
