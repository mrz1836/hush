package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// RestartHint is the locked stdout substring printed after a
// successful upgrade so daemonized `hush serve` processes get
// restarted. AC-8 asserts the exact phrase.
const RestartHint = "Restart any running 'hush serve' to pick up the new version"

// devVersion is the string used by [internal/cli].Version on
// non-released builds; carried here so the driver does not import the
// cli package (that would create a cycle).
const devVersion = "dev"

// binaryName is the basename of the hush binary inside a release
// archive; used to locate the executable after extraction.
const binaryName = "hush"

// downloadTimeout is the wall-clock ceiling on the tarball download.
// Five minutes is enough for a multi-MB binary over a slow link and
// short enough to detect a stalled mirror.
const downloadTimeout = 5 * time.Minute

// maxDownloadBytes caps the tarball download. Same 500 MiB ceiling
// as the per-file extract limit — defence against a hostile mirror
// serving an unbounded body.
const maxDownloadBytes int64 = 500 * 1024 * 1024

// Config wires together every external seam the driver depends on.
// All fields are optional; nil/zero values trigger production
// defaults via [Config.normalize].
type Config struct {
	// ReleaseSource resolves release metadata. Nil → gh CLI with
	// REST API fallback.
	ReleaseSource ReleaseSource
	// HTTPClient downloads tarballs and checksum files. Nil → a
	// fresh http.Client with downloadTimeout.
	HTTPClient *http.Client
	// Logger receives diagnostic messages. Nil → slog.Default().
	Logger *slog.Logger
	// CurrentVersion is the version of the running binary; treated
	// as [devVersion] when empty. Production callers pass cli.Version.
	CurrentVersion string
	// ExecPath is the path that will be replaced. Nil/empty →
	// os.Executable() with symlinks resolved.
	ExecPath string
	// Channel selects which release the driver targets. Empty →
	// [Stable].
	Channel Channel
	// Force, when true, downloads and installs the latest release
	// even when [Check] reports !UpdateAvailable.
	Force bool
	// Stdout receives the restart hint and progress messages. Nil →
	// os.Stdout.
	Stdout io.Writer
}

// normalize fills in production defaults for any zero-valued field.
// Returns a copy; callers' Config is never mutated.
func (c Config) normalize() (Config, error) {
	applyServiceDefaults(&c)
	applyValueDefaults(&c)
	if c.ExecPath == "" {
		exe, err := resolveExecPath()
		if err != nil {
			return c, err
		}
		c.ExecPath = exe
	}
	return c, nil
}

// applyServiceDefaults fills in non-trivial defaults (clients,
// loggers, writers) for nil fields.
func applyServiceDefaults(c *Config) {
	if c.ReleaseSource == nil {
		c.ReleaseSource = DefaultReleaseSource(nil, nil, "")
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: downloadTimeout}
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
}

// applyValueDefaults fills in scalar string defaults.
func applyValueDefaults(c *Config) {
	if c.CurrentVersion == "" {
		c.CurrentVersion = devVersion
	}
	if c.Channel == "" {
		c.Channel = Stable
	}
}

// resolveExecPath returns the running binary path with symlinks
// resolved so writability probes and renames target the real file.
func resolveExecPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("hush/upgrade: resolve executable: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return exe, nil
}

// Info is the structured result returned by [Check]. The cobra layer
// renders this as either a TTY-friendly summary or a JSON document.
type Info struct {
	Channel         Channel `json:"channel"`
	CurrentVersion  string  `json:"current_version"`
	LatestVersion   string  `json:"latest_version"`
	UpdateAvailable bool    `json:"update_available"`
	DownloadURL     string  `json:"download_url,omitempty"`
	ChecksumSHA256  string  `json:"checksum_sha256,omitempty"`
	ChecksumURL     string  `json:"-"`
	AssetName       string  `json:"asset_name,omitempty"`
	ReleaseNotes    string  `json:"release_notes,omitempty"`
}

// Check resolves the latest release on the configured channel, picks
// the platform asset, fetches its checksum, and returns a populated
// [Info]. Performs no writes. Refuses to even ask about updates when
// the current platform is outside the linux/darwin × amd64/arm64
// matrix the goreleaser pipeline ships for.
func Check(ctx context.Context, cfg Config) (*Info, error) {
	if err := platformGuard(); err != nil {
		return nil, err
	}
	cfg, err := cfg.normalize()
	if err != nil {
		return nil, err
	}

	release, err := LatestForChannel(ctx, cfg.ReleaseSource, cfg.Channel)
	if err != nil {
		return nil, err
	}

	info := &Info{
		Channel:        cfg.Channel,
		CurrentVersion: cfg.CurrentVersion,
		LatestVersion:  release.TagName,
		ReleaseNotes:   release.Body,
	}
	info.UpdateAvailable = isNewer(release.TagName, cfg.CurrentVersion)

	assetName, assetURL, checksumURL := pickAssetAndChecksumURL(release)
	info.DownloadURL = assetURL
	info.ChecksumURL = checksumURL
	info.AssetName = assetName

	if assetURL == "" {
		// We resolved a release but found no asset matching the
		// current platform. Surface clearly rather than blowing up
		// later in Install.
		return info, fmt.Errorf("%w: %s/%s in %s", ErrAssetNotFound, runtime.GOOS, runtime.GOARCH, release.TagName)
	}

	if checksumURL != "" {
		if sum, sumErr := fetchChecksum(ctx, cfg.HTTPClient, checksumURL, info.AssetName); sumErr == nil {
			info.ChecksumSHA256 = sum
		} else {
			cfg.Logger.Warn("hush/upgrade: checksum lookup failed",
				"asset", info.AssetName, "err", sumErr)
		}
	}

	return info, nil
}

// pickAssetAndChecksumURL walks the release's assets and returns the
// asset filename, the asset download URL, and the checksums download
// URL. Any of the returned strings may be empty when the release is
// malformed (e.g. artifacts still uploading).
func pickAssetAndChecksumURL(release *Release) (assetName, assetURL, checksumURL string) {
	pattern := fmt.Sprintf("_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	for _, a := range release.Assets {
		switch {
		case strings.HasSuffix(a.Name, pattern):
			assetName = a.Name
			assetURL = a.BrowserDownloadURL
		case strings.HasSuffix(a.Name, "checksums.txt"):
			checksumURL = a.BrowserDownloadURL
		}
	}
	return assetName, assetURL, checksumURL
}

// Install runs the full upgrade pipeline: Check → probe install dir
// → download → verify checksum → extract → atomic install → print
// restart hint. Returns nil with a "already current" log message when
// the release is not newer than the current version and Force is
// false.
//
//nolint:gocognit,gocyclo,funlen // sequential pipeline; splitting would obscure the ordering invariants
func Install(ctx context.Context, cfg Config) error {
	if err := platformGuard(); err != nil {
		return err
	}
	cfg, err := cfg.normalize()
	if err != nil {
		return err
	}

	info, err := Check(ctx, cfg)
	if err != nil {
		return err
	}

	if !info.UpdateAvailable && !cfg.Force {
		_, _ = fmt.Fprintf(cfg.Stdout, "hush is already up to date (%s)\n", info.CurrentVersion)
		return nil
	}
	if info.DownloadURL == "" {
		return fmt.Errorf("%w: %s/%s in %s", ErrAssetNotFound, runtime.GOOS, runtime.GOARCH, info.LatestVersion)
	}
	if info.ChecksumSHA256 == "" {
		return fmt.Errorf("%w: %s", ErrChecksumMissing, info.AssetName)
	}

	if probeErr := probeInstallDirWritable(cfg.ExecPath); probeErr != nil {
		return probeErr
	}

	workDir, err := os.MkdirTemp("", "hush-upgrade-*")
	if err != nil {
		return fmt.Errorf("hush/upgrade: create work dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	tarballPath := filepath.Join(workDir, info.AssetName)
	if dlErr := downloadAndVerify(ctx, cfg.HTTPClient, info, tarballPath); dlErr != nil {
		return dlErr
	}

	extractDir := filepath.Join(workDir, "extract")
	if mkErr := os.MkdirAll(extractDir, extractDirPerm); mkErr != nil {
		return fmt.Errorf("hush/upgrade: create extract dir: %w", mkErr)
	}
	if exErr := extractTarGz(tarballPath, extractDir); exErr != nil {
		return exErr
	}

	binaryPath, err := locateBinary(extractDir)
	if err != nil {
		return err
	}

	if instErr := installBinaryFallback(binaryPath, cfg.ExecPath); instErr != nil {
		return instErr
	}

	cfg.Logger.Info("hush/upgrade: installed",
		"from", info.CurrentVersion, "to", info.LatestVersion, "path", cfg.ExecPath)
	_, _ = fmt.Fprintf(cfg.Stdout, "Upgraded hush from %s to %s\n", info.CurrentVersion, info.LatestVersion)
	_, _ = fmt.Fprintln(cfg.Stdout, RestartHint)
	return nil
}

// platformGuard refuses early when the current GOOS/GOARCH is outside
// the goreleaser asset matrix. Returning before any network call
// avoids leaking that an unsupported user even tried.
func platformGuard() error {
	osOK := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	archOK := runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	if !osOK || !archOK {
		return fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

// downloadAndVerify streams the tarball to disk, hashing as it goes,
// and compares the digest against info.ChecksumSHA256.
func downloadAndVerify(ctx context.Context, client *http.Client, info *Info, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.DownloadURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("hush/upgrade: build download request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDownloadFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%w: status %d", ErrDownloadFailed, resp.StatusCode)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // dst constructed from MkdirTemp
	if err != nil {
		return fmt.Errorf("hush/upgrade: create download file: %w", err)
	}

	hasher := sha256.New()
	body := io.TeeReader(io.LimitReader(resp.Body, maxDownloadBytes), hasher)
	n, copyErr := io.Copy(out, body)
	if closeErr := out.Close(); closeErr != nil && copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("%w: %w", ErrDownloadFailed, copyErr)
	}
	if n >= maxDownloadBytes {
		return fmt.Errorf("%w: download exceeds %d bytes", ErrFileTooLarge, maxDownloadBytes)
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actual, info.ChecksumSHA256) {
		return fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, info.ChecksumSHA256, actual)
	}
	return nil
}

// locateBinary returns the absolute path of the hush executable
// inside the extracted directory. Walks recursively so layouts with
// an enclosing `bin/` directory keep working.
func locateBinary(extractDir string) (string, error) {
	var found string
	walkErr := filepath.Walk(extractDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		if fi.Name() == binaryName {
			found = path
			return io.EOF // sentinel to short-circuit Walk
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, io.EOF) {
		return "", fmt.Errorf("hush/upgrade: walk extract dir: %w", walkErr)
	}
	if found == "" {
		return "", ErrBinaryNotFound
	}
	return found, nil
}

// isNewer reports whether candidate (newVersion) is strictly newer
// than current (oldVersion). Stripped-down port of mage-x's
// SemanticVersion.IsNewerThan: trims a leading "v", strips any
// prerelease suffix for the numeric compare, treats "dev" or empty as
// older than any real semver, and falls back to "false" on unparseable
// strings (defensive — never claim an upgrade is needed without
// evidence).
func isNewer(newVersion, oldVersion string) bool {
	oldClean := strings.TrimPrefix(strings.TrimSpace(oldVersion), "v")
	if oldClean == devVersion || oldClean == "" {
		return true
	}
	newParts, err := parseVersionTuple(newVersion)
	if err != nil {
		return false
	}
	oldParts, err := parseVersionTuple(oldVersion)
	if err != nil {
		return false
	}
	for i := range 3 {
		switch {
		case newParts[i] > oldParts[i]:
			return true
		case newParts[i] < oldParts[i]:
			return false
		}
	}
	return false
}

// parseVersionTuple extracts the major.minor.patch components from a
// version string. The "v" prefix and any prerelease/build suffix are
// stripped. Returns an error on malformed input — callers (isNewer)
// fall back to a conservative "not newer" decision.
func parseVersionTuple(v string) ([3]int, error) {
	clean := strings.TrimPrefix(strings.TrimSpace(v), "v")
	if idx := strings.IndexAny(clean, "-+"); idx >= 0 {
		clean = clean[:idx]
	}
	parts := strings.Split(clean, ".")
	if len(parts) != 3 {
		return [3]int{}, fmt.Errorf("%w: %q", errInvalidSemverTuple, v)
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, fmt.Errorf("hush/upgrade: parse %q: %w", v, err)
		}
		out[i] = n
	}
	return out, nil
}
