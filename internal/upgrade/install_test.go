package upgrade

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyFile_SuccessfulRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	body := []byte("hello world\nbinary contents")
	require.NoError(t, os.WriteFile(src, body, 0o600))

	require.NoError(t, copyFile(src, dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, body, got)

	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, installFilePerm, info.Mode().Perm())
}

func TestCopyFile_MissingSrc(t *testing.T) {
	dir := t.TempDir()
	err := copyFile(filepath.Join(dir, "nope"), filepath.Join(dir, "out"))
	require.Error(t, err)
}

func TestInstallBinaryFallback_AtomicReplace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "extracted")
	dst := filepath.Join(dir, "hush")

	// Pre-existing destination simulates the running binary.
	require.NoError(t, os.WriteFile(dst, []byte("old"), 0o755)) //nolint:gosec // test fixture
	require.NoError(t, os.WriteFile(src, []byte("new"), 0o755)) //nolint:gosec // test fixture

	require.NoError(t, installBinaryFallback(src, dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))

	// .new sidecar should have been renamed away.
	_, err = os.Stat(dst + ".new")
	require.True(t, os.IsNotExist(err))

	// src should have been best-effort removed.
	_, err = os.Stat(src)
	require.True(t, os.IsNotExist(err))
}

func TestInstallBinaryFallback_CreatesDestWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "extracted")
	dst := filepath.Join(dir, "hush") // does not exist

	require.NoError(t, os.WriteFile(src, []byte("body"), 0o755)) //nolint:gosec // test fixture
	require.NoError(t, installBinaryFallback(src, dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "body", string(got))
}

func TestInstallBinaryFallback_SrcMissing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "absent")
	dst := filepath.Join(dir, "hush")

	err := installBinaryFallback(src, dst)
	require.Error(t, err)
	// .new should be cleaned up after the copy failure.
	_, statErr := os.Stat(dst + ".new")
	require.True(t, os.IsNotExist(statErr))
}

func TestProbeInstallDirWritable_Allows(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, probeInstallDirWritable(filepath.Join(dir, "hush")))
	// Probe file is cleaned up.
	_, err := os.Stat(filepath.Join(dir, ".hush-upgrade-probe"))
	require.True(t, os.IsNotExist(err))
}

func TestProbeInstallDirWritable_ReturnsSentinelOnReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits behave differently on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits are bypassed")
	}
	dir := t.TempDir()
	roDir := filepath.Join(dir, "ro")
	require.NoError(t, os.MkdirAll(roDir, 0o500)) // no write for owner

	err := probeInstallDirWritable(filepath.Join(roDir, "hush"))
	require.ErrorIs(t, err, ErrInstallDirNotWritable)
	assert.Contains(t, err.Error(), roDir)
}

func TestProbeInstallDirWritable_MissingDir(t *testing.T) {
	dir := t.TempDir()
	err := probeInstallDirWritable(filepath.Join(dir, "absent", "hush"))
	require.ErrorIs(t, err, ErrInstallDirNotWritable)
}

func TestInstallBinaryFallback_RenameTargetIsDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	require.NoError(t, os.WriteFile(src, []byte("body"), 0o755))                 //nolint:gosec // test fixture
	require.NoError(t, os.MkdirAll(dst+".new", 0o700))                           // rename target busy
	require.NoError(t, os.WriteFile(filepath.Join(dst+".new", "x"), nil, 0o600)) //nolint:gosec // test fixture

	err := installBinaryFallback(src, dst)
	require.Error(t, err)
}

func TestCopyFile_DestPathInvalid(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("body"), 0o600))

	// Use a path inside a non-existent directory so OpenFile fails.
	err := copyFile(src, filepath.Join(dir, "absent", "dst"))
	require.Error(t, err)
}
