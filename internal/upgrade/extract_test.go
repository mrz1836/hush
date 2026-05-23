package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateExtractPath_Valid(t *testing.T) {
	cases := []struct {
		name    string
		destDir string
		tarPath string
	}{
		{"simple", "/tmp/extract", "hush"},
		{"nested", "/tmp/extract", "bin/hush"},
		{"deeper", "/tmp/extract", "deep/nested/dir/file.txt"},
		{"trailing slash", "/tmp/extract", "bin/"},
		{"double slashes normalized", "/tmp/extract", "bin//hush"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateExtractPath(tc.destDir, tc.tarPath)
			require.NoError(t, err)
			rel, err := filepath.Rel(tc.destDir, got)
			require.NoError(t, err)
			assert.False(t, strings.HasPrefix(rel, ".."), "result %q escapes %q", got, tc.destDir)
		})
	}
}

func TestValidateExtractPath_Security_RejectsTraversal(t *testing.T) {
	cases := []struct {
		name    string
		tarPath string
	}{
		{"absolute", "/etc/passwd"},
		{"plain dot-dot", "../etc/passwd"},
		{"deep traversal", "../../../etc/passwd"},
		{"hidden traversal", "subdir/../../../etc/passwd"},
		{"trailing parent", "bin/../../etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateExtractPath("/tmp/extract", tc.tarPath)
			require.ErrorIs(t, err, ErrPathTraversal)
		})
	}
}

func TestNormalizeFileMode(t *testing.T) {
	cases := []struct {
		name string
		in   os.FileMode
		want os.FileMode
	}{
		{"executable 0777 → 0755", 0o777, 0o755},
		{"regular 0666 → 0644", 0o666, 0o644},
		{"user-only exec", 0o700, 0o755},
		{"group-only exec", 0o070, 0o755},
		{"other-only exec", 0o007, 0o755},
		{"read-only", 0o444, 0o644},
		{"setuid stripped", os.ModeSetuid | 0o755, 0o755},
		{"setgid stripped", os.ModeSetgid | 0o755, 0o755},
		{"sticky stripped", os.ModeSticky | 0o755, 0o755},
		{"all special bits stripped", os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0o777, 0o755},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeFileMode(tc.in))
		})
	}
}

func TestExtractTarGz_HappyPath(t *testing.T) {
	dest := t.TempDir()
	tarPath := filepath.Join(dest, "test.tar.gz")
	extractDir := filepath.Join(dest, "out")
	require.NoError(t, os.MkdirAll(extractDir, 0o750))

	writeTarGz(t, tarPath, map[string]tarEntry{
		"hush":            {mode: 0o755, body: []byte("binary")},
		"bin/sub/file.md": {mode: 0o644, body: []byte("docs")},
	})

	require.NoError(t, extractTarGz(tarPath, extractDir))

	body, err := os.ReadFile(filepath.Join(extractDir, "hush"))
	require.NoError(t, err)
	assert.Equal(t, "binary", string(body))

	info, err := os.Stat(filepath.Join(extractDir, "hush"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(extractDir, "bin/sub/file.md"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestExtractTarGz_SkipsMaliciousEntries(t *testing.T) {
	dest := t.TempDir()
	tarPath := filepath.Join(dest, "test.tar.gz")
	extractDir := filepath.Join(dest, "out")
	require.NoError(t, os.MkdirAll(extractDir, 0o750))

	writeTarGz(t, tarPath, map[string]tarEntry{
		"../../etc/passwd": {mode: 0o644, body: []byte("evil")},
		"hush":             {mode: 0o755, body: []byte("binary")},
	})

	require.NoError(t, extractTarGz(tarPath, extractDir))

	// Malicious entry must not exist.
	_, err := os.Stat(filepath.Join(extractDir, "..", "..", "etc", "passwd"))
	require.Error(t, err)

	// Legitimate entry still extracted.
	body, err := os.ReadFile(filepath.Join(extractDir, "hush"))
	require.NoError(t, err)
	assert.Equal(t, "binary", string(body))
}

func TestExtractTarGz_PermissionNormalization(t *testing.T) {
	dest := t.TempDir()
	tarPath := filepath.Join(dest, "test.tar.gz")
	extractDir := filepath.Join(dest, "out")
	require.NoError(t, os.MkdirAll(extractDir, 0o750))

	writeTarGz(t, tarPath, map[string]tarEntry{
		"dangerous-exec": {mode: 0o777, body: []byte("#!/bin/sh\necho hi")},
		"dangerous-data": {mode: 0o666, body: []byte("data")},
	})

	require.NoError(t, extractTarGz(tarPath, extractDir))

	info, err := os.Stat(filepath.Join(extractDir, "dangerous-exec"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(extractDir, "dangerous-data"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestExtractTarGz_SizeLimitConstant(t *testing.T) {
	// Guard against accidental reduction of the zip-bomb ceiling.
	assert.Equal(t, int64(500*1024*1024), int64(maxUpdateFileSize))
}

func TestExtractTarGz_NotAGzip(t *testing.T) {
	dest := t.TempDir()
	tarPath := filepath.Join(dest, "broken.tar.gz")
	require.NoError(t, os.WriteFile(tarPath, []byte("not a gzip"), 0o600))

	err := extractTarGz(tarPath, dest)
	require.Error(t, err)
}

func TestExtractTarGz_EmptyArchive(t *testing.T) {
	dest := t.TempDir()
	tarPath := filepath.Join(dest, "empty.tar.gz")
	writeTarGz(t, tarPath, map[string]tarEntry{})
	require.NoError(t, extractTarGz(tarPath, dest))
}

type tarEntry struct {
	mode int64
	body []byte
}

// writeTarGz creates a tar.gz at tarPath with the given entries.
// Directory entries are emitted automatically for each entry's parent
// chain so the archive resembles a goreleaser output.
func writeTarGz(t *testing.T, tarPath string, entries map[string]tarEntry) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, e := range entries {
		hdr := &tar.Header{
			Name:     name,
			Mode:     e.mode,
			Size:     int64(len(e.body)),
			Typeflag: tar.TypeReg,
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write(e.body)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	require.NoError(t, os.WriteFile(tarPath, buf.Bytes(), 0o600))
}
