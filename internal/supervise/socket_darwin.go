//go:build darwin

package supervise

import "os"

// defaultRuntimeDir returns the OS-conventional runtime directory under
// which unit tests construct temporary status-socket / pid-file paths.
// Production socket.go and pidfile.go consume the configured absolute
// path verbatim — this helper is test-fixture-only (research.md R-2).
func defaultRuntimeDir() string {
	if cache, err := os.UserCacheDir(); err == nil {
		return cache + "/hush"
	}
	return os.TempDir()
}
