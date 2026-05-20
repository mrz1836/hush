package supervise

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tightTempDir returns a 0o700 subdirectory inside t.TempDir(). t.TempDir
// itself uses an 0o755 counter dir which would trip ensureParentMode0700.
func tightTempDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "tight")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatalf("mkdir tight: %v", err)
	}
	return d
}

// TestPidFile_FlockExclusive — first AcquirePidFile holds the lock; a
// concurrent acquire on the same path returns a non-nil error.
func TestPidFile_FlockExclusive(t *testing.T) {
	path := filepath.Join(tightTempDir(t), "supervisor.pid")

	first, err := AcquirePidFile(path)
	require.NoError(t, err)
	require.NotNil(t, first)
	t.Cleanup(func() { _ = first.Release() })

	second, err := AcquirePidFile(path)
	require.Error(t, err)
	require.Nil(t, second)
}

// TestPidFile_DuplicateRefused — refused acquire returns
// errors.Is(err, ErrPidLocked) within milliseconds AND does NOT modify
// the live owner's PID-file contents.
func TestPidFile_DuplicateRefused(t *testing.T) {
	path := filepath.Join(tightTempDir(t), "supervisor.pid")

	first, err := AcquirePidFile(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Release() })

	originalContents, err := os.ReadFile(path) //nolint:gosec // test reads its own temp file
	require.NoError(t, err)

	start := time.Now()
	_, err = AcquirePidFile(path)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPidLocked), "got %v, want errors.Is(err, ErrPidLocked)", err)
	assert.Less(t, elapsed, 100*time.Millisecond, "refusal should be near-instant; took %v", elapsed)

	postContents, err := os.ReadFile(path) //nolint:gosec // test reads its own temp file
	require.NoError(t, err)
	assert.Equal(t, originalContents, postContents, "live owner's PID file must not be modified by refused acquire")
}

// TestPidFile_StaleAcquired — sub-process acquires the lock, exits
// without Release; the OS auto-releases the flock at process death; the
// next AcquirePidFile against the same path succeeds without operator
// intervention.
func TestPidFile_StaleAcquired(t *testing.T) {
	path := filepath.Join(tightTempDir(t), "stale.pid")

	exePath, err := os.Executable()
	require.NoError(t, err)

	cmd := exec.CommandContext(context.Background(), exePath, "-test.run=^$") //nolint:gosec // helper mode re-invokes the test binary
	cmd.Env = append(os.Environ(), "HUSH_CHILD_TEST_HELPER_MODE=pidfile-acquire-and-exit", "HUSH_PIDFILE_TEST_PATH="+path)
	require.NoError(t, cmd.Run(), "stale-acquire helper should exit 0")

	// After the sub-process exits, OS has released the flock.
	pf, err := AcquirePidFile(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pf.Release() })

	contents, err := os.ReadFile(path) //nolint:gosec // test reads its own temp file
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid()), string(contents))
}

// TestPidFile_ReleaseRemovesFile — Release removes the inode; second
// Release returns the package-private errAlreadyReleased sentinel.
func TestPidFile_ReleaseRemovesFile(t *testing.T) {
	path := filepath.Join(tightTempDir(t), "release.pid")

	pf, err := AcquirePidFile(path)
	require.NoError(t, err)

	require.NoError(t, pf.Release())

	_, err = os.Stat(path)
	assert.True(t, errors.Is(err, fs.ErrNotExist), "post-Release stat should be ErrNotExist, got %v", err)

	err = pf.Release()
	assert.True(t, errors.Is(err, errAlreadyReleased), "double-Release should return errAlreadyReleased, got %v", err)
}

// TestPidFile_WritesOwnPID — post-Acquire, file contents == strconv.Itoa(os.Getpid()).
func TestPidFile_WritesOwnPID(t *testing.T) {
	path := filepath.Join(tightTempDir(t), "ownpid.pid")

	pf, err := AcquirePidFile(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pf.Release() })

	contents, err := os.ReadFile(path) //nolint:gosec // test reads its own temp file
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid()), string(contents))
}

// TestPidFile_Mode0600 — post-Acquire, file mode is exactly 0o600.
func TestPidFile_Mode0600(t *testing.T) {
	path := filepath.Join(tightTempDir(t), "mode.pid")

	pf, err := AcquirePidFile(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pf.Release() })

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestPidFile_ParentMode0700Created — when parent dir does not exist,
// AcquirePidFile creates it at 0o700.
func TestPidFile_ParentMode0700Created(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "nested", "supervisor")
	path := filepath.Join(parent, "supervisor.pid")

	pf, err := AcquirePidFile(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pf.Release() })

	info, err := os.Stat(parent)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// TestPidFile_ParentLooseRefuses — when parent dir exists at 0o755,
// AcquirePidFile refuses with ErrSocketPermsLoose AND no PID file is
// created.
func TestPidFile_ParentLooseRefuses(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "loose")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	// Re-chmod explicitly because umask (e.g. 0o077 on hardened macOS
	// hosts) silently masks MkdirAll's perm bits down to 0o700, which is
	// exactly the tight mode this test is trying to NOT use.
	require.NoError(t, os.Chmod(parent, 0o755))
	path := filepath.Join(parent, "supervisor.pid")

	_, err := AcquirePidFile(path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSocketPermsLoose), "got %v, want errors.Is(err, ErrSocketPermsLoose)", err)

	_, statErr := os.Stat(path)
	assert.True(t, errors.Is(statErr, fs.ErrNotExist), "PID file must not be created on perms-refusal; got stat err %v", statErr)
}

// TestPidFile_OpenFailureWhenPathIsDirectory — AcquirePidFile surfaces
// the OpenFile error (wrapped) when the configured path itself is an
// existing directory rather than a file.
func TestPidFile_OpenFailureWhenPathIsDirectory(t *testing.T) {
	parent := tightTempDir(t)
	path := filepath.Join(parent, "iam-a-dir")
	require.NoError(t, os.Mkdir(path, 0o700))

	_, err := AcquirePidFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "supervise: pidfile")
	assert.False(t, errors.Is(err, ErrPidLocked))
}

// TestPidFile_NilReceiverReleaseReturnsAlreadyReleased — invoking
// Release on a nil *PidFile (zero-value misuse guard) returns the
// package-private errAlreadyReleased sentinel.
func TestPidFile_NilReceiverReleaseReturnsAlreadyReleased(t *testing.T) {
	var p *PidFile
	err := p.Release()
	assert.True(t, errors.Is(err, errAlreadyReleased))
}

// TestPidFile_ReleaseUnlockErrorOnClosedFd — when the underlying fd has
// already been closed externally, unix.Flock(LOCK_UN) returns EBADF and
// Release surfaces the wrapped error.
func TestPidFile_ReleaseUnlockErrorOnClosedFd(t *testing.T) {
	parent := tightTempDir(t)
	path := filepath.Join(parent, "manual.pid")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Close()) // fd now invalid

	pf := &PidFile{fd: f, path: path}
	relErr := pf.Release()
	require.Error(t, relErr)
	assert.Contains(t, relErr.Error(), "supervise: pidfile unlock")
}

// TestPidFile_ReleaseRemoveErrorSurfaced — when the parent directory
// becomes read-only between AcquirePidFile and Release, os.Remove fails
// with EACCES. Release surfaces the wrapped error.
func TestPidFile_ReleaseRemoveErrorSurfaced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory ACLs; cannot exercise EACCES path")
	}
	parent := tightTempDir(t)
	path := filepath.Join(parent, "supervisor.pid")

	pf, err := AcquirePidFile(path)
	require.NoError(t, err)

	// Drop write perms on parent → Remove of inode fails.
	require.NoError(t, os.Chmod(parent, 0o500))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	relErr := pf.Release()
	require.Error(t, relErr)
	assert.Contains(t, relErr.Error(), "supervise: pidfile remove")
}
