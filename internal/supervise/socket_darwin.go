//go:build darwin

package supervise

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

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

// supervisorNameRe is the locked path-safe slug regex documented in
// CONFIG-SCHEMA.md. The CLI flag layer is expected to pre-validate
// names against this pattern; SocketPathForSupervisor panics on a
// non-matching name (programmer error per socket-protocol.md §4.1).
var supervisorNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// SocketPathForSupervisor returns the absolute Unix-socket path the
// supervisor with the supplied name binds. Darwin scheme:
// <UserCacheDir>/hush/supervise-<name>.sock. Pure path-derivation; no
// syscalls beyond the os.UserCacheDir lookup.
//
// Panics if name does not match the path-safe slug pattern — the CLI
// flag layer validates the cobra flag value before invoking this
// helper (socket-protocol.md §4.1).
func SocketPathForSupervisor(name string) string {
	if !supervisorNameRe.MatchString(name) {
		panic("supervise: SocketPathForSupervisor: invalid supervisor name: " + name)
	}
	root := defaultRuntimeDir()
	return filepath.Join(root, "supervise-"+name+".sock")
}

// EnumerateSupervisorSockets returns the sorted absolute paths of
// every file in the platform runtime directory matching the
// supervisor-socket naming scheme. Darwin scheme:
// <UserCacheDir>/hush/supervise-*.sock.
//
// The function does NOT verify mode, ownership, or liveness — every
// pattern-matching name is returned and the caller (client.go)
// attempts to connect. A missing runtime directory returns
// ([]string{}, nil) — no sockets found is a normal state, not an
// error (socket-protocol.md §4.2).
func EnumerateSupervisorSockets() ([]string, error) {
	root := defaultRuntimeDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("supervise: enumerate sockets: %w", err)
	}
	out := make([]string, 0, len(entries))
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasPrefix(name, "supervise-") || !strings.HasSuffix(name, ".sock") {
			continue
		}
		out = append(out, filepath.Join(root, name))
	}
	sort.Strings(out)
	return out, nil
}
