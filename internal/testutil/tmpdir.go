package testutil

import (
	"os"
	"testing"
)

// shortTempBudget is the headroom reserved for "<prefix><random>/<sock>"
// inside the macOS AF_UNIX sun_path limit (104 bytes). If a candidate
// base directory plus this budget exceeds the limit, the base is
// rejected.
const shortTempBudget = 40

// shortTempLimit mirrors macOS's sun_path size for AF_UNIX sockets.
const shortTempLimit = 104

// ShortTempDir creates a temp directory short enough to hold AF_UNIX
// socket files under macOS's 104-byte sun_path limit, returns its
// path, and registers t.Cleanup to remove it.
//
// Base selection tries /tmp first (shortest on every OS when writable)
// and falls back to $TMPDIR — used by sandboxed runners that set
// $TMPDIR to a writable, short location and forbid /tmp. Bases longer
// than the budget are skipped. The returned directory is chmod 0700.
func ShortTempDir(t *testing.T, prefix string) string {
	t.Helper()
	bases := []string{"/tmp"}
	if v := os.Getenv("TMPDIR"); v != "" && v != "/tmp" {
		bases = append(bases, v)
	}

	var lastErr error
	for _, base := range bases {
		if len(base)+shortTempBudget > shortTempLimit {
			continue
		}
		d, err := os.MkdirTemp(base, prefix)
		if err != nil {
			lastErr = err
			continue
		}
		if err := os.Chmod(d, 0o700); err != nil { //nolint:gosec // dir needs exec bit; test-only helper
			_ = os.RemoveAll(d) //nolint:gosec // path produced by os.MkdirTemp; not user-tainted
			t.Fatalf("hush/testutil: ShortTempDir: chmod %q: %v", d, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(d) }) //nolint:gosec // path produced by os.MkdirTemp; not user-tainted
		return d
	}
	t.Fatalf("hush/testutil: ShortTempDir: no usable temp base (last err: %v)", lastErr)
	return ""
}
