package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/upgrade"
)

// TestUpgrade_CheckOnly_PrintsAvailableVersion drives `hush upgrade
// --check` against a stubbed release source and asserts the rendered
// summary includes the latest version + "update available: true". No
// install side effect.
func TestUpgrade_CheckOnly_PrintsAvailableVersion(t *testing.T) {
	srv := newFakeReleaseServer(t, []byte("payload"))

	restore := withReleaseSource(t, &fakeStubSource{stable: srv.release})
	defer restore()

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hush")
	require.NoError(t, os.WriteFile(execPath, []byte("old"), 0o755)) //nolint:gosec // test fixture
	restoreExec := withExecPath(t, execPath)
	defer restoreExec()

	prev := snapshotBuildVars()
	t.Cleanup(func() { restoreBuildVars(prev) })
	Version = "v0.1.0"

	ctx, stdout, stderr := newUpgradeTestCtx(true)
	cmd := newUpgradeCmd()
	cmd.SetContext(ctx)
	require.NoError(t, cmd.Flags().Set(upgradeFlagCheck, "true"))

	require.NoError(t, cmd.RunE(cmd, nil))

	out := stdout.String()
	assert.Contains(t, out, "latest version:")
	assert.Contains(t, out, srv.release.TagName)
	assert.Contains(t, out, "update available:  true")
	// --check must not perform any install side effect.
	body, readErr := os.ReadFile(execPath)
	require.NoError(t, readErr)
	assert.Equal(t, "old", string(body))
	// Dev warning must not fire when Version is a real semver.
	assert.NotContains(t, stderr.String(), "warning: running a dev build")
}

// TestUpgrade_ChannelFlag_OverridesEnv asserts the --channel flag wins
// over the UPDATE_CHANNEL env var.
func TestUpgrade_ChannelFlag_OverridesEnv(t *testing.T) {
	t.Setenv("UPDATE_CHANNEL", "edge")

	stub := &fakeStubSource{
		stable: &upgrade.Release{TagName: "v0.2.0"},
		beta:   &upgrade.Release{TagName: "v0.3.0-beta.1"},
		edge:   &upgrade.Release{TagName: "v0.4.0-rc.1"},
	}
	restore := withReleaseSource(t, stub)
	defer restore()

	execPath := filepath.Join(t.TempDir(), "hush")
	require.NoError(t, os.WriteFile(execPath, []byte("old"), 0o755)) //nolint:gosec // test fixture
	restoreExec := withExecPath(t, execPath)
	defer restoreExec()

	prev := snapshotBuildVars()
	t.Cleanup(func() { restoreBuildVars(prev) })
	Version = "v0.1.0"

	ctx, _, _ := newUpgradeTestCtx(false)
	cmd := newUpgradeCmd()
	cmd.SetContext(ctx)
	require.NoError(t, cmd.Flags().Set(upgradeFlagCheck, "true"))
	require.NoError(t, cmd.Flags().Set(upgradeFlagChannel, "beta"))

	// The flag should route to Beta even though the env says edge.
	// We expect ErrAssetNotFound (the beta release has no assets) but
	// only after the beta source was hit.
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, upgrade.ErrAssetNotFound)

	assert.Equal(t, 1, stub.betaCalls, "beta source must be consulted when --channel=beta")
	assert.Equal(t, 0, stub.edgeCalls, "edge must be skipped when --channel overrides env")
	assert.Equal(t, 0, stub.stableCalls, "stable must be skipped when --channel=beta")
}

// TestUpgrade_DevVersion_WarnsAndProceeds asserts that a dev build
// emits a stderr warning but still drives the upgrade pipeline.
func TestUpgrade_DevVersion_WarnsAndProceeds(t *testing.T) {
	srv := newFakeReleaseServer(t, buildFakeHushTarGz(t, []byte("v0.2.0 binary")))
	restore := withReleaseSource(t, &fakeStubSource{stable: srv.release})
	defer restore()

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hush")
	require.NoError(t, os.WriteFile(execPath, []byte("old"), 0o755)) //nolint:gosec // test fixture
	restoreExec := withExecPath(t, execPath)
	defer restoreExec()

	prev := snapshotBuildVars()
	t.Cleanup(func() { restoreBuildVars(prev) })
	Version = "dev"

	ctx, stdout, stderr := newUpgradeTestCtx(false)
	cmd := newUpgradeCmd()
	cmd.SetContext(ctx)

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Contains(t, stderr.String(), "warning: running a dev build")
	assert.Contains(t, stdout.String(), upgrade.RestartHint)

	got, err := os.ReadFile(execPath)
	require.NoError(t, err)
	assert.Equal(t, "v0.2.0 binary", string(got))
}

// TestUpgrade_UnwritableDir_ErrorsCleanly asserts the cobra layer
// returns the ErrInstallDirNotWritable sentinel verbatim and writes
// the locked stderr prefix.
func TestUpgrade_UnwritableDir_ErrorsCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits")
	}

	srv := newFakeReleaseServer(t, buildFakeHushTarGz(t, []byte("payload")))
	restore := withReleaseSource(t, &fakeStubSource{stable: srv.release})
	defer restore()

	roDir := filepath.Join(t.TempDir(), "ro")
	require.NoError(t, os.MkdirAll(roDir, 0o500))
	execPath := filepath.Join(roDir, "hush")
	restoreExec := withExecPath(t, execPath)
	defer restoreExec()

	prev := snapshotBuildVars()
	t.Cleanup(func() { restoreBuildVars(prev) })
	Version = "dev"

	ctx, _, stderr := newUpgradeTestCtx(false)
	cmd := newUpgradeCmd()
	cmd.SetContext(ctx)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, upgrade.ErrInstallDirNotWritable)
	assert.Equal(t, ExitPerm, mapErr(err))

	stderrOut := stderr.String()
	assert.Contains(t, stderrOut, "hush: upgrade:")
	assert.Contains(t, stderrOut, roDir)
	assert.Contains(t, stderrOut, "sudo hush upgrade")
}

// TestResolveChannel covers the flag-overrides-env precedence rules
// in isolation.
func TestResolveChannel(t *testing.T) {
	t.Parallel()
	getenvEdge := func(string) string { return "edge" }
	getenvEmpty := func(string) string { return "" }

	cases := []struct {
		name string
		flag string
		env  func(string) string
		want upgrade.Channel
	}{
		{"flag beta beats env edge", "beta", getenvEdge, upgrade.Beta},
		{"flag edge beats env empty", "edge", getenvEmpty, upgrade.Edge},
		{"flag stable beats env", "stable", getenvEdge, upgrade.Stable},
		{"unknown flag defaults to stable", "garbage", getenvEdge, upgrade.Stable},
		{"empty flag uses env", "", getenvEdge, upgrade.Edge},
		{"empty flag and empty env default to stable", "", getenvEmpty, upgrade.Stable},
		{"flag whitespace + mixed case", "  Beta  ", getenvEmpty, upgrade.Beta},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, resolveChannel(tc.flag, tc.env))
		})
	}
}

// TestIsDevVersion covers the dev-build detection used to gate the
// stderr warning.
func TestIsDevVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		v    string
		want bool
	}{
		{"", true},
		{"   ", true},
		{"dev", true},
		{"v0.1.0", false},
		{"0.1.0", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, isDevVersion(c.v), "version=%q", c.v)
	}
}

// TestFormatUpgradeErr_StripsPrefixAndAppendsHint verifies the
// formatter trims the wrapped "hush/upgrade: " prefix and only adds
// the sudo hint for the ErrInstallDirNotWritable branch.
func TestFormatUpgradeErr_StripsPrefixAndAppendsHint(t *testing.T) {
	t.Parallel()
	t.Run("nil err", func(t *testing.T) {
		assert.Empty(t, formatUpgradeErr(nil))
	})
	t.Run("strips prefix", func(t *testing.T) {
		err := fmt.Errorf("%w: %s/%s in v0.2.0", upgrade.ErrAssetNotFound, "linux", "amd64")
		got := formatUpgradeErr(err)
		assert.NotContains(t, got, "hush/upgrade:")
		assert.Contains(t, got, "no matching release asset")
	})
	t.Run("adds sudo hint for unwritable dir", func(t *testing.T) {
		err := fmt.Errorf("%w: /usr/local/bin", upgrade.ErrInstallDirNotWritable)
		got := formatUpgradeErr(err)
		assert.Contains(t, got, "/usr/local/bin")
		assert.Contains(t, got, "sudo hush upgrade")
	})
}

// --- helpers ---

// withReleaseSource swaps in a stub release source for the duration of
// a single test and returns a restore function. Mirrors the existing
// snapshotBuildVars pattern for global test seams.
func withReleaseSource(t *testing.T, src upgrade.ReleaseSource) func() {
	t.Helper()
	prev := releaseSourceForTests
	releaseSourceForTests = src
	return func() { releaseSourceForTests = prev }
}

// withExecPath swaps in a tempdir-backed ExecPath override for the
// duration of a single test.
func withExecPath(t *testing.T, path string) func() {
	t.Helper()
	prev := execPathForTests
	execPathForTests = path
	return func() { execPathForTests = prev }
}

// newUpgradeTestCtx builds a cobra context wired with stdout/stderr
// buffers — same pattern as newVersionTestCtx but with stderr also
// captured.
func newUpgradeTestCtx(isTTY bool) (context.Context, *bytes.Buffer, *bytes.Buffer) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	out := &outputContext{
		stdout: newStream(stdout, isTTY, !isTTY),
		stderr: newStream(stderr, false, true),
	}
	ctx := context.WithValue(context.Background(), outputCtxKey{}, out)
	return ctx, stdout, stderr
}

// fakeStubSource is a minimal upgrade.ReleaseSource that returns
// pre-canned releases per channel and counts each call. Identical in
// shape to the package-internal stubSource but reimplemented here so
// the cli tests don't import unexported helpers.
type fakeStubSource struct {
	stable, beta, edge                *upgrade.Release
	stableErr, betaErr, edgeErr       error
	stableCalls, betaCalls, edgeCalls int
}

func (s *fakeStubSource) Stable(_ context.Context) (*upgrade.Release, error) {
	s.stableCalls++
	if s.stableErr != nil {
		return nil, s.stableErr
	}
	if s.stable == nil {
		return nil, errors.New("fakeStubSource: stable not configured")
	}
	return s.stable, nil
}

func (s *fakeStubSource) Beta(_ context.Context) (*upgrade.Release, error) {
	s.betaCalls++
	if s.betaErr != nil {
		return nil, s.betaErr
	}
	if s.beta == nil {
		return nil, errors.New("fakeStubSource: beta not configured")
	}
	return s.beta, nil
}

func (s *fakeStubSource) Edge(_ context.Context) (*upgrade.Release, error) {
	s.edgeCalls++
	if s.edgeErr != nil {
		return nil, s.edgeErr
	}
	if s.edge == nil {
		return nil, errors.New("fakeStubSource: edge not configured")
	}
	return s.edge, nil
}

// fakeReleaseFixture bundles the httptest server plus the upgrade.Release
// it advertises. Constructed by newFakeReleaseServer.
type fakeReleaseFixture struct {
	server  *httptest.Server
	release *upgrade.Release
}

// newFakeReleaseServer spins up an httptest server that serves the
// supplied asset body and a matching checksums.txt entry. The returned
// release is wired to point at that server with an asset name matching
// the host's runtime.GOOS/GOARCH.
func newFakeReleaseServer(t *testing.T, assetBody []byte) *fakeReleaseFixture {
	t.Helper()

	assetName := fmt.Sprintf("hush_0.2.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(assetBody)
	checksumBody := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/asset"):
			_, _ = w.Write(assetBody)
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			_, _ = w.Write([]byte(checksumBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	rel := &upgrade.Release{
		TagName: "v0.2.0",
		Assets: []upgrade.ReleaseAsset{
			{Name: assetName, BrowserDownloadURL: srv.URL + "/asset"},
			{Name: "hush_0.2.0_checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}
	return &fakeReleaseFixture{server: srv, release: rel}
}

// buildFakeHushTarGz returns a tar.gz containing a single file named
// "hush" whose body is `body`. Used by tests that exercise the full
// Install pipeline through the cobra layer.
func buildFakeHushTarGz(t *testing.T, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "hush",
		Mode:     0o755,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}
