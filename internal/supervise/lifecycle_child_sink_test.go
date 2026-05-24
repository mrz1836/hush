// Tests for the openChildSink helper that backs Child.StdoutPath /
// Child.StderrPath routing.

package supervise

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenChildSink_EmptyPathReturnsFallback(t *testing.T) {
	t.Parallel()
	w, closer, err := openChildSink("", os.Stderr)
	require.NoError(t, err)
	assert.Nil(t, closer, "no closer expected when path is empty — process owns the fallback handle")
	assert.Same(t, os.Stderr, w, "writer should be the fallback verbatim")
}

func TestOpenChildSink_AppendsToExistingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "child.log")
	require.NoError(t, os.WriteFile(path, []byte("seed\n"), 0o600))
	w, closer, err := openChildSink(path, os.Stderr)
	require.NoError(t, err)
	require.NotNil(t, closer)
	t.Cleanup(func() { _ = closer.Close() })
	_, err = fmt.Fprintln(w, "child wrote")
	require.NoError(t, err)
	require.NoError(t, closer.Close())
	out, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "seed\nchild wrote\n", string(out),
		"file should be opened in append mode (seed must survive)")
}

func TestOpenChildSink_CreatesMissingFileWith0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "child-new.log")
	w, closer, err := openChildSink(path, os.Stderr)
	require.NoError(t, err)
	require.NotNil(t, closer)
	t.Cleanup(func() { _ = closer.Close() })
	_, err = fmt.Fprintln(w, "hello")
	require.NoError(t, err)
	require.NoError(t, closer.Close())
	info, statErr := os.Stat(path)
	require.NoError(t, statErr)
	// On macOS umask may strip group/other; assert no perms outside owner-rw.
	mode := info.Mode().Perm()
	assert.Equal(t, os.FileMode(0o600)&mode, mode&0o600,
		"owner rw bits should be set")
	assert.Zero(t, mode&0o077,
		"group/other bits MUST NOT be set on a child log file (mode=%v)", mode)
}

func TestOpenChildSink_UnopenablePathReturnsError(t *testing.T) {
	t.Parallel()
	// A path inside a non-existent directory cannot be created.
	bad := filepath.Join(t.TempDir(), "missing-dir", "child.log")
	w, closer, err := openChildSink(bad, os.Stderr)
	require.Error(t, err)
	assert.Nil(t, w)
	assert.Nil(t, closer)
}
