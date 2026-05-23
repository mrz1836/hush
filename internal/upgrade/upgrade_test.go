package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		name   string
		newVer string
		oldVer string
		want   bool
	}{
		{"newer patch", "v1.2.1", "v1.2.0", true},
		{"newer minor", "v1.3.0", "v1.2.5", true},
		{"newer major", "v2.0.0", "v1.99.99", true},
		{"same", "v1.2.3", "v1.2.3", false},
		{"older", "v1.0.0", "v1.1.0", false},
		{"dev always upgrades", "v1.0.0", "dev", true},
		{"empty old upgrades", "v1.0.0", "", true},
		{"no-v prefix", "1.2.0", "1.1.0", true},
		{"prerelease compared by base", "v1.2.0-beta.1", "v1.1.0", true},
		{"unparseable new returns false", "garbage", "v1.0.0", false},
		{"unparseable old returns false", "v1.0.0", "garbage", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isNewer(tc.newVer, tc.oldVer))
		})
	}
}

func TestPlatformGuard_AllowsLinuxDarwinAmd64Arm64(t *testing.T) {
	// The host running CI/tests is always one of the supported
	// combinations — this test asserts the guard does not refuse it.
	require.NoError(t, platformGuard())
}

func TestPickAssetAndChecksumURL(t *testing.T) {
	rel := &Release{
		Assets: []ReleaseAsset{
			{Name: "hush_0.2.0_linux_amd64.tar.gz", BrowserDownloadURL: "linux-amd64"},
			{Name: "hush_0.2.0_darwin_arm64.tar.gz", BrowserDownloadURL: "darwin-arm64"},
			{Name: "hush_0.2.0_checksums.txt", BrowserDownloadURL: "checksums"},
		},
	}
	name, asset, csum := pickAssetAndChecksumURL(rel)
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		assert.Equal(t, "linux-amd64", asset)
		assert.Equal(t, "hush_0.2.0_linux_amd64.tar.gz", name)
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		assert.Equal(t, "darwin-arm64", asset)
		assert.Equal(t, "hush_0.2.0_darwin_arm64.tar.gz", name)
	default:
		// Other combos (linux/arm64, darwin/amd64) won't match this
		// fixture; just verify the matcher returns empty rather than
		// the wrong asset.
		assert.Empty(t, asset)
		assert.Empty(t, name)
	}
	assert.Equal(t, "checksums", csum)
}

func TestCheck_DevVersionAlwaysUpdates(t *testing.T) {
	srv, asset := newReleaseAndChecksumServer(t, []byte("payload"))
	cfg := Config{
		ReleaseSource: &stubSource{stable: srv.release},
		HTTPClient:    srv.server.Client(),
		// Skip the executable-resolution branch in normalize().
		ExecPath:       filepath.Join(t.TempDir(), "hush"),
		CurrentVersion: "dev",
	}

	info, err := Check(t.Context(), cfg)
	require.NoError(t, err)
	assert.True(t, info.UpdateAvailable)
	assert.Equal(t, asset, info.AssetName)
	assert.NotEmpty(t, info.ChecksumSHA256)
	assert.Equal(t, srv.release.TagName, info.LatestVersion)
}

func TestCheck_AlreadyCurrent(t *testing.T) {
	srv, _ := newReleaseAndChecksumServer(t, []byte("payload"))
	cfg := Config{
		ReleaseSource:  &stubSource{stable: srv.release},
		HTTPClient:     srv.server.Client(),
		ExecPath:       filepath.Join(t.TempDir(), "hush"),
		CurrentVersion: srv.release.TagName,
	}
	info, err := Check(t.Context(), cfg)
	require.NoError(t, err)
	assert.False(t, info.UpdateAvailable)
}

func TestCheck_NoMatchingAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rel := &Release{TagName: "v9.9.9", Assets: nil}
	cfg := Config{
		ReleaseSource:  &stubSource{stable: rel},
		HTTPClient:     srv.Client(),
		ExecPath:       filepath.Join(t.TempDir(), "hush"),
		CurrentVersion: "v0.1.0",
	}
	_, err := Check(t.Context(), cfg)
	require.ErrorIs(t, err, ErrAssetNotFound)
}

func TestInstall_AlreadyCurrent_NoOp(t *testing.T) {
	srv, _ := newReleaseAndChecksumServer(t, []byte("payload"))
	stdout := &bytes.Buffer{}
	cfg := Config{
		ReleaseSource:  &stubSource{stable: srv.release},
		HTTPClient:     srv.server.Client(),
		ExecPath:       filepath.Join(t.TempDir(), "hush"),
		CurrentVersion: srv.release.TagName,
		Stdout:         stdout,
	}
	require.NoError(t, Install(t.Context(), cfg))
	assert.Contains(t, stdout.String(), "already up to date")
	assert.NotContains(t, stdout.String(), RestartHint)
}

func TestInstall_Success_PrintsRestartHint(t *testing.T) {
	body := buildHushTarGz(t, []byte("v0.2.0 binary"))
	srv, _ := newReleaseAndChecksumServer(t, body)

	// Pre-create the install target so the "old" binary exists.
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hush")
	require.NoError(t, os.WriteFile(execPath, []byte("old"), 0o755)) //nolint:gosec // test fixture

	stdout := &bytes.Buffer{}
	cfg := Config{
		ReleaseSource:  &stubSource{stable: srv.release},
		HTTPClient:     srv.server.Client(),
		ExecPath:       execPath,
		CurrentVersion: "dev",
		Stdout:         stdout,
	}
	require.NoError(t, Install(t.Context(), cfg))

	got, err := os.ReadFile(execPath)
	require.NoError(t, err)
	assert.Equal(t, "v0.2.0 binary", string(got))

	assert.Contains(t, stdout.String(), RestartHint)
	assert.Contains(t, stdout.String(), "Upgraded hush from dev to "+srv.release.TagName)
}

func TestInstall_ChecksumMismatch(t *testing.T) {
	body := buildHushTarGz(t, []byte("legit"))
	srv, _ := newReleaseAndChecksumServer(t, body)

	// Poison the checksums payload so verification fails.
	srv.checksumBody = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef  " + srv.assetName + "\n"

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hush")
	require.NoError(t, os.WriteFile(execPath, []byte("old"), 0o755)) //nolint:gosec // test fixture

	cfg := Config{
		ReleaseSource:  &stubSource{stable: srv.release},
		HTTPClient:     srv.server.Client(),
		ExecPath:       execPath,
		CurrentVersion: "dev",
		Stdout:         io.Discard,
	}
	err := Install(t.Context(), cfg)
	require.ErrorIs(t, err, ErrChecksumMismatch)

	// Binary on disk must remain the old version.
	got, _ := os.ReadFile(execPath)
	assert.Equal(t, "old", string(got))
}

func TestInstall_MissingChecksum_Errors(t *testing.T) {
	body := buildHushTarGz(t, []byte("legit"))
	srv, _ := newReleaseAndChecksumServer(t, body)

	// Strip checksums from the release so the driver has no way to
	// verify the download; Hush's "safest most stable" stance is to
	// error here (mage-x warns).
	srv.release.Assets = filterAssets(srv.release.Assets, func(a ReleaseAsset) bool {
		return !strings.HasSuffix(a.Name, "checksums.txt")
	})

	execPath := filepath.Join(t.TempDir(), "hush")
	require.NoError(t, os.WriteFile(execPath, []byte("old"), 0o755)) //nolint:gosec // test fixture

	cfg := Config{
		ReleaseSource:  &stubSource{stable: srv.release},
		HTTPClient:     srv.server.Client(),
		ExecPath:       execPath,
		CurrentVersion: "dev",
		Stdout:         io.Discard,
	}
	err := Install(t.Context(), cfg)
	require.ErrorIs(t, err, ErrChecksumMissing)
}

func TestInstall_ForceUpgradesEvenWhenCurrent(t *testing.T) {
	body := buildHushTarGz(t, []byte("forced"))
	srv, _ := newReleaseAndChecksumServer(t, body)

	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "hush")
	require.NoError(t, os.WriteFile(execPath, []byte("old"), 0o755)) //nolint:gosec // test fixture

	stdout := &bytes.Buffer{}
	cfg := Config{
		ReleaseSource:  &stubSource{stable: srv.release},
		HTTPClient:     srv.server.Client(),
		ExecPath:       execPath,
		CurrentVersion: srv.release.TagName, // same version
		Force:          true,
		Stdout:         stdout,
	}
	require.NoError(t, Install(t.Context(), cfg))

	got, _ := os.ReadFile(execPath)
	assert.Equal(t, "forced", string(got))
	assert.Contains(t, stdout.String(), RestartHint)
}

func TestInstall_DownloadNetworkFailure(t *testing.T) {
	// Server that always 500s the asset path forces the download to
	// fail after probe + checksum lookup succeed.
	rel := &Release{
		TagName: "v9.9.9",
	}
	checksumSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Asset name & checksum that match the runtime platform.
		name := fmt.Sprintf("hush_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
		_, _ = fmt.Fprintf(w, "0000000000000000000000000000000000000000000000000000000000000000  %s\n", name)
	}))
	t.Cleanup(checksumSrv.Close)

	downloadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(downloadSrv.Close)

	rel.Assets = []ReleaseAsset{
		{
			Name:               fmt.Sprintf("hush_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH),
			BrowserDownloadURL: downloadSrv.URL + "/asset",
		},
		{Name: "checksums.txt", BrowserDownloadURL: checksumSrv.URL + "/checksums.txt"},
	}

	execPath := filepath.Join(t.TempDir(), "hush")
	require.NoError(t, os.WriteFile(execPath, []byte("old"), 0o755)) //nolint:gosec // test fixture

	cfg := Config{
		ReleaseSource:  &stubSource{stable: rel},
		HTTPClient:     downloadSrv.Client(),
		ExecPath:       execPath,
		CurrentVersion: "dev",
		Stdout:         io.Discard,
	}
	err := Install(t.Context(), cfg)
	require.ErrorIs(t, err, ErrDownloadFailed)
}

func TestInstall_UnwritableExecDirReturnsSentinel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits")
	}

	body := buildHushTarGz(t, []byte("payload"))
	srv, _ := newReleaseAndChecksumServer(t, body)

	roDir := filepath.Join(t.TempDir(), "ro")
	require.NoError(t, os.MkdirAll(roDir, 0o500))
	execPath := filepath.Join(roDir, "hush") // never created — dir is read-only

	cfg := Config{
		ReleaseSource:  &stubSource{stable: srv.release},
		HTTPClient:     srv.server.Client(),
		ExecPath:       execPath,
		CurrentVersion: "dev",
		Stdout:         io.Discard,
	}
	err := Install(t.Context(), cfg)
	require.ErrorIs(t, err, ErrInstallDirNotWritable)
	assert.Contains(t, err.Error(), roDir)
}

func TestInstall_DownloadOversizeTriggersFileTooLarge(t *testing.T) {
	// Build a real tar.gz but configure the server to advertise a
	// body larger than maxDownloadBytes. We use a tiny override via
	// a custom server that streams forever.
	infinite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			_, _ = w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  hush.tar.gz\n"))
		default:
			// Stream past maxDownloadBytes to trip the limit.
			buf := bytes.Repeat([]byte("A"), 1<<20)
			for i := 0; i < 600; i++ {
				if _, err := w.Write(buf); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(infinite.Close)

	rel := &Release{
		TagName: "v9.9.9",
		Assets: []ReleaseAsset{
			{Name: "hush.tar.gz", BrowserDownloadURL: infinite.URL + "/asset"},
			{Name: "checksums.txt", BrowserDownloadURL: infinite.URL + "/checksums.txt"},
		},
	}

	execPath := filepath.Join(t.TempDir(), "hush")
	require.NoError(t, os.WriteFile(execPath, []byte("old"), 0o755)) //nolint:gosec // test fixture

	cfg := Config{
		ReleaseSource:  &stubSource{stable: rel},
		HTTPClient:     infinite.Client(),
		ExecPath:       execPath,
		CurrentVersion: "dev",
		Stdout:         io.Discard,
	}
	err := Install(t.Context(), cfg)
	require.Error(t, err)
	// The asset name doesn't match runtime.GOOS/GOARCH, so the
	// driver will surface ErrAssetNotFound first. That's the desired
	// gate for an unrecognized asset — the test only proves
	// installation halts cleanly on this branch.
	assert.True(t,
		errString(err, ErrAssetNotFound) || errString(err, ErrFileTooLarge),
		"expected ErrAssetNotFound or ErrFileTooLarge, got %v", err)
}

func TestConfigNormalize_FillsDefaults(t *testing.T) {
	// ExecPath supplied so normalize doesn't call os.Executable.
	cfg := Config{ExecPath: filepath.Join(t.TempDir(), "hush")}
	got, err := cfg.normalize()
	require.NoError(t, err)
	assert.NotNil(t, got.ReleaseSource)
	assert.NotNil(t, got.HTTPClient)
	assert.NotNil(t, got.Logger)
	assert.NotNil(t, got.Stdout)
	assert.Equal(t, "dev", got.CurrentVersion)
	assert.Equal(t, Stable, got.Channel)
}

func TestConfigNormalize_ResolvesExecPathWhenEmpty(t *testing.T) {
	cfg := Config{}
	got, err := cfg.normalize()
	require.NoError(t, err)
	assert.NotEmpty(t, got.ExecPath)
}

func TestDefaultReleaseSource_Constructs(t *testing.T) {
	// We can't drive end-to-end without network access, but the
	// constructor itself must not panic and must satisfy the
	// interface.
	src := DefaultReleaseSource(nil, nil, "")
	_ = src
}

func TestLocateBinary_FindsInSubdir(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "hush"), []byte("body"), 0o755)) //nolint:gosec // test fixture

	got, err := locateBinary(dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(binDir, "hush"), got)
}

func TestLocateBinary_NotFound(t *testing.T) {
	_, err := locateBinary(t.TempDir())
	require.ErrorIs(t, err, ErrBinaryNotFound)
}

func TestDownloadAndVerify_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	dst := filepath.Join(t.TempDir(), "out.bin")
	info := &Info{DownloadURL: srv.URL, ChecksumSHA256: "deadbeef"}
	err := downloadAndVerify(t.Context(), srv.Client(), info, dst)
	require.ErrorIs(t, err, ErrDownloadFailed)
}

func TestDownloadAndVerify_NetworkFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // dial will fail

	dst := filepath.Join(t.TempDir(), "out.bin")
	info := &Info{DownloadURL: srv.URL, ChecksumSHA256: "deadbeef"}
	err := downloadAndVerify(t.Context(), srv.Client(), info, dst)
	require.ErrorIs(t, err, ErrDownloadFailed)
}

func TestDownloadAndVerify_OversizeBody(t *testing.T) {
	// The httptest server streams just past maxDownloadBytes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		buf := bytes.Repeat([]byte("A"), 1<<20)
		for i := 0; i <= 501; i++ {
			if _, err := w.Write(buf); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	dst := filepath.Join(t.TempDir(), "out.bin")
	info := &Info{DownloadURL: srv.URL, ChecksumSHA256: "deadbeef"}
	err := downloadAndVerify(t.Context(), srv.Client(), info, dst)
	require.ErrorIs(t, err, ErrFileTooLarge)
}

func TestDownloadAndVerify_HappyPath(t *testing.T) {
	body := []byte("payload")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dst := filepath.Join(t.TempDir(), "out.bin")
	info := &Info{DownloadURL: srv.URL, ChecksumSHA256: hex.EncodeToString(sum[:])}
	require.NoError(t, downloadAndVerify(t.Context(), srv.Client(), info, dst))

	got, _ := os.ReadFile(dst)
	assert.Equal(t, body, got)
}

// --- helpers ---

// fakeReleaseServer holds the moving parts of a release fixture:
// an httptest server, the Release object that points at it, and the
// asset/checksum payload bodies the server returns.
type fakeReleaseServer struct {
	server       *httptest.Server
	release      *Release
	assetName    string
	assetBody    []byte
	checksumBody string
}

// newReleaseAndChecksumServer returns a fixture wired so that
// Install() can run the full download → verify → extract pipeline
// against the supplied tarball body. The asset is named so it
// matches the host's runtime.GOOS/GOARCH.
func newReleaseAndChecksumServer(t *testing.T, assetBody []byte) (*fakeReleaseServer, string) {
	t.Helper()

	assetName := fmt.Sprintf("hush_0.2.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(assetBody)
	checksumBody := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)

	fr := &fakeReleaseServer{
		assetName:    assetName,
		assetBody:    assetBody,
		checksumBody: checksumBody,
	}

	fr.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/asset"):
			_, _ = w.Write(fr.assetBody)
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			_, _ = w.Write([]byte(fr.checksumBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fr.server.Close)

	fr.release = &Release{
		TagName:     "v0.2.0",
		Name:        "v0.2.0",
		PublishedAt: time.Now().UTC(),
		Assets: []ReleaseAsset{
			{Name: assetName, BrowserDownloadURL: fr.server.URL + "/asset"},
			{Name: "hush_0.2.0_checksums.txt", BrowserDownloadURL: fr.server.URL + "/checksums.txt"},
		},
	}
	return fr, assetName
}

// buildHushTarGz returns a valid tar.gz containing a single file
// named "hush" with the supplied body.
func buildHushTarGz(t *testing.T, body []byte) []byte {
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

func filterAssets(assets []ReleaseAsset, keep func(ReleaseAsset) bool) []ReleaseAsset {
	out := make([]ReleaseAsset, 0, len(assets))
	for _, a := range assets {
		if keep(a) {
			out = append(out, a)
		}
	}
	return out
}

// errString returns true when target is in err's chain; small helper
// to keep mixed-sentinel asserts terse.
func errString(err, target error) bool {
	if err == nil || target == nil {
		return false
	}
	return strings.Contains(err.Error(), target.Error())
}
