// Package upgrade implements the hush self-upgrade pattern: resolve a
// GitHub release for the requested channel, download the platform-
// specific tarball, verify its SHA256 against the published checksums
// file, extract it with Zip-Slip / zip-bomb defenses, and atomically
// replace the running binary via a copy-and-rename dance that is safe
// against the SIGBUS class of in-place binary mutation bugs.
//
// The package is intentionally self-contained: every external seam
// (release-source lookup, HTTP client, exec path, current version) is
// passed in through [Config] so unit tests can drive every code path
// without touching the network or the real filesystem layout.
//
// Sentinel errors are collected in errors.go for easy errors.Is
// matching by [internal/cli/upgrade.go] and the exit-code mapper.
package upgrade

import "errors"

// Sentinel error catalog. Every documented failure mode maps to
// exactly one sentinel; errors.Is is the matching primitive used by
// [internal/cli.mapErr]. Sentinel messages are static category
// strings — they never echo user input.
var (
	// Release-lookup errors.
	ErrNoReleasesFound     = errors.New("hush/upgrade: no releases found")
	ErrNoBetaReleasesFound = errors.New("hush/upgrade: no beta releases found")
	ErrGHCLIFailed         = errors.New("hush/upgrade: gh CLI command failed")
	ErrGitHubAPIFailed     = errors.New("hush/upgrade: GitHub API request failed")

	// Asset / platform errors.
	ErrAssetNotFound       = errors.New("hush/upgrade: no matching release asset for platform")
	ErrUnsupportedPlatform = errors.New("hush/upgrade: unsupported platform (require linux/darwin × amd64/arm64)")
	ErrBinaryNotFound      = errors.New("hush/upgrade: hush binary not found in extracted files")

	// Download / network errors.
	ErrDownloadFailed = errors.New("hush/upgrade: download failed")

	// Checksum errors.
	ErrChecksumFetchFailed = errors.New("hush/upgrade: failed to fetch checksums file")
	ErrChecksumNotFound    = errors.New("hush/upgrade: checksum not found in checksums file")
	ErrChecksumMismatch    = errors.New("hush/upgrade: checksum verification failed")
	ErrChecksumMissing     = errors.New("hush/upgrade: release has no checksums file; refusing to install unverified binary")

	// Extract errors.
	ErrPathTraversal = errors.New("hush/upgrade: path traversal attempt detected")
	ErrFileTooLarge  = errors.New("hush/upgrade: extracted file exceeds maximum allowed size")
	ErrNoTarGzFound  = errors.New("hush/upgrade: no tar.gz file found in update directory")

	// Install errors.
	ErrInstallDirNotWritable = errors.New("hush/upgrade: install directory not writable")

	// errInvalidSemverTuple is returned by parseVersionTuple when the
	// input is not parseable as major.minor.patch. Kept package-private
	// because callers don't need to errors.Is against it; isNewer
	// already collapses both this and parse-int failures to a single
	// "not newer" outcome.
	errInvalidSemverTuple = errors.New("hush/upgrade: not a semver tuple")
)
