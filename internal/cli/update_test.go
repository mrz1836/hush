package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	selfupdate "github.com/mrz1836/go-selfupdate"
	"github.com/mrz1836/go-selfupdate/cobracmd"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeReleaseSource is a ReleaseSource that answers with a fixed release or
// error, so the update command never shells out to gh or calls GitHub.
type fakeReleaseSource struct {
	rel *selfupdate.Release
	err error
}

func (f fakeReleaseSource) Latest(context.Context) (*selfupdate.Release, error) {
	return f.rel, f.err
}

// fakeRelease returns a release whose platform archive and checksums file
// both point at baseURL. The platform asset is present even for an
// up-to-date release because asset selection runs before the version compare.
func fakeRelease(baseURL, tag string) *selfupdate.Release {
	ver := strings.TrimPrefix(tag, "v")
	return &selfupdate.Release{
		TagName: tag,
		Assets: []selfupdate.ReleaseAsset{
			{Name: fmt.Sprintf("hush_%s_%s_%s.tar.gz", ver, runtime.GOOS, runtime.GOARCH), BrowserDownloadURL: baseURL + "/archive"},
			{Name: fmt.Sprintf("hush_%s_checksums.txt", ver), BrowserDownloadURL: baseURL + "/checksums"},
		},
	}
}

// newTestUpdateConfig returns an update config for a running "0.8.2" whose
// latest release is tag, served by an httptest server that answers 503 on
// every path. The target is a temp file reading "old" inside dir (a fresh
// temp dir when dir is empty); its path is returned so a test can prove it
// was never replaced.
func newTestUpdateConfig(t *testing.T, tag, dir string) (selfupdate.Config, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	if dir == "" {
		dir = t.TempDir()
	}
	target := filepath.Join(dir, "hush")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o600))

	return selfupdate.Config{
		Owner:          "mrz1836",
		Repo:           "hush",
		BinaryName:     "hush",
		CurrentVersion: "0.8.2",
		TargetPath:     target,
		Client:         srv.Client(),
		Source:         fakeReleaseSource{rel: fakeRelease(srv.URL, tag)},
	}, target
}

// runUpdate executes args against a minimal root carrying the real global
// flags, update command, and usage-error wiring, then reports and maps the
// error exactly as Execute does — so "printed once" holds for the whole
// pipeline. The root has no persistentPreRun, which would rebind the
// injected buffers to the process streams.
func runUpdate(t *testing.T, cfg selfupdate.Config, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	errStream := newStream(&errBuf, false, true)

	root := &cobra.Command{Use: "hush", SilenceUsage: true, SilenceErrors: true}
	addPersistentFlags(root)
	cfg.Stdout = &outBuf
	attachUpdateCommand(root, cfg)
	wireUsageErrors(root)

	ctx := context.WithValue(t.Context(), outputCtxKey{}, &outputContext{
		stdout: newStream(&outBuf, false, true),
		stderr: errStream,
	})
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	reportUsageError(errStream, err)
	return outBuf.String(), errBuf.String(), mapErr(err)
}

// skipInstallOnWindows skips cases that reach the install path, which the
// library refuses on Windows before any network access.
func skipInstallOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("self-update install is not supported on windows")
	}
}

// TestPrintUpdateErr locks the one-line stderr report a failed update leaves
// behind, so a self-update failure is never a silent non-zero exit.
func TestPrintUpdateErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string // the full report; "" means no output at all
	}{
		{name: "nil", err: nil},
		{name: "usage error already reported", err: &usageError{cmdPath: "hush update", err: errors.New("bad flag")}},
		{
			name: "library error verbatim",
			err:  fmt.Errorf("%w: status 503", selfupdate.ErrChecksumFetchFailed),
			want: "hush: update: go-selfupdate: failed to fetch checksums file: status 503\n",
		},
		{
			name: "multi-line error flattened to one line",
			err:  errors.New("download failed\nretry later"),
			want: "hush: update: download failed retry later\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			printUpdateErr(newStream(&buf, false, true), tt.err)
			assert.Equal(t, tt.want, buf.String())
		})
	}
}

// TestUpdateCmd_ReportsChecksumFailureOnce drives the real update path to a
// failed checksums download and asserts the library's error reaches stderr
// exactly once, with the exit code and the target binary unchanged.
func TestUpdateCmd_ReportsChecksumFailureOnce(t *testing.T) {
	t.Parallel()
	skipInstallOnWindows(t)

	cfg, target := newTestUpdateConfig(t, "v9.9.9", "")
	stdout, stderr, code := runUpdate(t, cfg, "update")

	assert.Equal(t, ExitErr, code)
	assert.Equal(t, 1, strings.Count(stderr, "failed to fetch checksums file"), "stderr = %q", stderr)
	assert.True(t, strings.HasPrefix(stderr, "hush: update: go-selfupdate: failed to fetch checksums file"), "stderr = %q", stderr)
	assert.Equal(t, 1, strings.Count(stderr, "\n"), "stderr is not one line: %q", stderr)
	assert.NotContains(t, stdout, "failed to fetch")

	body, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "old", string(body), "target binary was replaced")
}

// TestUpdateCmd_AliasAndQuietStillReport asserts the upgrade alias reports
// the same failure, and --quiet does not suppress it.
func TestUpdateCmd_AliasAndQuietStillReport(t *testing.T) {
	t.Parallel()
	skipInstallOnWindows(t)

	cfg, _ := newTestUpdateConfig(t, "v9.9.9", "")
	_, stderr, code := runUpdate(t, cfg, "upgrade", "--quiet")

	assert.Equal(t, ExitErr, code)
	assert.Equal(t, 1, strings.Count(stderr, "hush: update: "), "stderr = %q", stderr)
	assert.Equal(t, 1, strings.Count(stderr, "\n"), "stderr is not one line: %q", stderr)
}

// TestUpdateCmd_ReportsCheckLookupFailure asserts --check reports a failed
// release lookup instead of exiting silently.
func TestUpdateCmd_ReportsCheckLookupFailure(t *testing.T) {
	t.Parallel()

	cfg, _ := newTestUpdateConfig(t, "v9.9.9", "")
	cfg.Source = fakeReleaseSource{err: fmt.Errorf("%w: boom", selfupdate.ErrGitHubAPIFailed)}
	_, stderr, code := runUpdate(t, cfg, "update", "--check")

	assert.Equal(t, ExitErr, code)
	assert.Equal(t, "hush: update: go-selfupdate: GitHub API request failed: boom\n", stderr)
}

// TestUpdateCmd_InstallDirNotWritableExitPerm asserts the preflight refusal
// is printed and still maps to the permission exit code.
func TestUpdateCmd_InstallDirNotWritableExitPerm(t *testing.T) {
	t.Parallel()
	skipInstallOnWindows(t)
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only directory")
	}

	dir := t.TempDir()
	cfg, target := newTestUpdateConfig(t, "v9.9.9", dir)
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, stderr, code := runUpdate(t, cfg, "update")

	assert.Equal(t, ExitPerm, code)
	assert.Equal(t, 1, strings.Count(stderr, "install dir not writable"), "stderr = %q", stderr)
	assert.True(t, strings.HasPrefix(stderr, "hush: update: "), "stderr = %q", stderr)
	assert.Equal(t, 1, strings.Count(stderr, "\n"), "stderr is not one line: %q", stderr)

	body, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "old", string(body), "target binary was replaced")
}

// TestUpdateCmd_UpToDateWritesNoStderr asserts a successful no-op update
// adds nothing to stderr.
func TestUpdateCmd_UpToDateWritesNoStderr(t *testing.T) {
	t.Parallel()

	cfg, _ := newTestUpdateConfig(t, "v0.8.2", "")
	stdout, stderr, code := runUpdate(t, cfg, "update")

	assert.Equal(t, ExitOK, code)
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "already up to date")
}

// TestUpdateCmd_UsageErrorReportedOnce asserts a usage error on update is
// reported only by the root handler: cobra rejects it before RunE, so the
// wrapper never runs.
func TestUpdateCmd_UsageErrorReportedOnce(t *testing.T) {
	t.Parallel()

	cfg, _ := newTestUpdateConfig(t, "v9.9.9", "")
	_, stderr, code := runUpdate(t, cfg, "update", "extra")

	assert.Equal(t, ExitInputErr, code)
	assert.Equal(t, 1, strings.Count(stderr, "Run 'hush update --help' for usage."), "stderr = %q", stderr)
	assert.NotContains(t, stderr, "hush: update: go-selfupdate:")
}

// TestUpdateCmd_KeepsLibraryShape asserts wrapping RunE leaves the rest of the
// library's command intact: it is registered on root with the upgrade alias,
// keeps the annotation that keeps the passive banner quiet during an update,
// and has --check's -c shorthand dropped so the global --config keeps it.
func TestUpdateCmd_KeepsLibraryShape(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "hush"}
	addPersistentFlags(root)
	cmd := attachUpdateCommand(root, newUpdateConfig())

	found, _, err := root.Find([]string{"upgrade"})
	require.NoError(t, err)
	assert.Same(t, cmd, found)
	assert.Equal(t, cobracmd.New(newUpdateConfig()).Annotations, cmd.Annotations)
	check := cmd.Flags().Lookup(flagCheck)
	require.NotNil(t, check)
	assert.Empty(t, check.Shorthand)
}

// TestNewUpdateConfig_ProductionIdentity pins the production update identity
// and leaves the release source, transport, and target to the library.
func TestNewUpdateConfig_ProductionIdentity(t *testing.T) {
	t.Parallel()

	cfg := newUpdateConfig()

	assert.Equal(t, "mrz1836", cfg.Owner)
	assert.Equal(t, "hush", cfg.Repo)
	assert.Equal(t, "hush", cfg.BinaryName)
	assert.Equal(t, "HUSH_GITHUB_TOKEN", cfg.TokenEnvVar)
	assert.Equal(t, Version, cfg.CurrentVersion)
	assert.Nil(t, cfg.Source)
	assert.Nil(t, cfg.Client)
	assert.Empty(t, cfg.TargetPath)
}
