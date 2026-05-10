// Package-private SDD-22 PID file: flock-backed exclusive supervisor lock.
package supervise

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// ErrPidLocked is returned (wrapped) by AcquirePidFile when another live
// process already holds the configured PID file's flock. The textual PID
// inside the file is advisory metadata only — the criterion for "stale vs
// live" is exclusively the OS-held flock (FR-022-3). Compare via
// errors.Is(err, supervise.ErrPidLocked).
var ErrPidLocked = errors.New("supervise: pidfile already locked")

// ErrSocketPermsLoose is returned (wrapped) by AcquirePidFile and by
// (*StatusServer).Run when the configured pid_file or status_socket parent
// directory exists with a mode laxer than 0700. The supervisor refuses to
// start ("FS perms ARE the auth") — never silently chmods the directory.
// Compare via errors.Is(err, supervise.ErrSocketPermsLoose).
var ErrSocketPermsLoose = errors.New("supervise: parent directory mode laxer than 0700")

// errAlreadyReleased is the package-private sentinel returned by
// (*PidFile).Release on a second call. Not exported — double-Release is a
// programmer error, not a control-flow input (research.md R-6).
var errAlreadyReleased = errors.New("supervise: pidfile already released")

// PidFile is a handle representing an acquired exclusive flock on a
// configured PID-file path plus the file descriptor backing it. Construct
// via AcquirePidFile; release via Release. The zero value is NOT usable.
// Lifecycle: AcquirePidFile → Release. Single-use.
type PidFile struct {
	fd   *os.File
	path string
}

// AcquirePidFile opens (creating if absent) the file at path with mode
// 0600, ensures the parent directory exists at mode 0700 (creating if
// absent; refusing with ErrSocketPermsLoose if the existing parent is
// laxer), then attempts a non-blocking exclusive flock. On success,
// writes the current PID (textual base-10 form) into the file and returns
// a *PidFile. The PID write happens AFTER the lock is held (FR-022-4) so
// a refused acquirer cannot corrupt the live owner's record.
func AcquirePidFile(path string) (*PidFile, error) {
	if err := ensureParentMode0700(filepath.Dir(path)); err != nil {
		return nil, err
	}
	fd, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // FR-022-6 mandates 0600
	if err != nil {
		return nil, fmt.Errorf("supervise: pidfile open: %w", err)
	}
	if err := unix.Flock(int(fd.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = fd.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("supervise: pidfile flock: %w", ErrPidLocked)
		}
		return nil, fmt.Errorf("supervise: pidfile flock: %w", err)
	}
	if err := fd.Truncate(0); err != nil {
		_ = unix.Flock(int(fd.Fd()), unix.LOCK_UN)
		_ = fd.Close()
		return nil, fmt.Errorf("supervise: pidfile truncate: %w", err)
	}
	if _, err := fd.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		_ = unix.Flock(int(fd.Fd()), unix.LOCK_UN)
		_ = fd.Close()
		return nil, fmt.Errorf("supervise: pidfile write: %w", err)
	}
	return &PidFile{fd: fd, path: path}, nil
}

// Release drops the flock, closes the underlying fd, and removes the file
// (best-effort — losing the race to a subsequent acquirer is acceptable).
// Calling Release on an already-released *PidFile returns the package-
// private errAlreadyReleased sentinel.
func (p *PidFile) Release() error {
	if p == nil || p.fd == nil {
		return errAlreadyReleased
	}
	fd, path := p.fd, p.path
	p.fd = nil

	if err := unix.Flock(int(fd.Fd()), unix.LOCK_UN); err != nil {
		_ = fd.Close()
		return fmt.Errorf("supervise: pidfile unlock: %w", err)
	}
	if err := fd.Close(); err != nil {
		return fmt.Errorf("supervise: pidfile close: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("supervise: pidfile remove: %w", err)
	}
	return nil
}
