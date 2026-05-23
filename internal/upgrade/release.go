package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// owner and repo identify the GitHub project the upgrade flow targets.
// Hardcoded because the binary's identity is build-time stable — there
// is no scenario in which a `hush` binary should update itself from a
// different project.
const (
	owner = "mrz1836"
	repo  = "hush"

	// apiTimeout caps every release-lookup HTTP request. Independent
	// of the larger downloadTimeout that wraps tarball downloads.
	apiTimeout = 10 * time.Second
)

// ReleaseSource is the seam between the upgrade driver and the actual
// data source that produces release metadata. The two real
// implementations are [ghSource] (shells out to the `gh` CLI) and
// [apiSource] (hits api.github.com directly). Tests inject a stub.
type ReleaseSource interface {
	// Stable returns the latest non-prerelease, non-draft release.
	Stable(ctx context.Context) (*Release, error)
	// Beta returns the latest prerelease, falling back to the latest
	// non-draft release if no prerelease exists.
	Beta(ctx context.Context) (*Release, error)
	// Edge returns the most recent release of any kind (prerelease
	// or stable), excluding drafts.
	Edge(ctx context.Context) (*Release, error)
}

// GetChannel parses a getenv-style function and returns the resolved
// release channel. Case-insensitive; unknown values default to
// [Stable]. Accepting a getenv lambda (rather than calling os.Getenv
// directly) keeps the function trivially testable.
func GetChannel(getenv func(string) string) Channel {
	if getenv == nil {
		return Stable
	}
	switch strings.ToLower(strings.TrimSpace(getenv("UPDATE_CHANNEL"))) {
	case "beta":
		return Beta
	case "edge":
		return Edge
	default:
		return Stable
	}
}

// LatestForChannel dispatches to the appropriate ReleaseSource method.
// All error wrapping is deferred to the source implementations.
func LatestForChannel(ctx context.Context, src ReleaseSource, channel Channel) (*Release, error) {
	switch channel {
	case Beta:
		return src.Beta(ctx)
	case Edge:
		return src.Edge(ctx)
	case Stable:
		return src.Stable(ctx)
	default:
		return src.Stable(ctx)
	}
}

// commandRunner abstracts exec.Command so tests can stub the gh CLI
// without spawning real processes. Returns the trimmed stdout, or an
// error if the command failed.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// defaultCommandRunner shells out to the real binary via os/exec.
func defaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // caller-controlled; name is always "gh"
	return cmd.Output()
}

// ghSource shells the `gh` CLI to read releases. Used when `gh` is on
// PATH; falls back to [apiSource] inside [DefaultReleaseSource] if the
// CLI errors out.
type ghSource struct {
	run commandRunner
}

// NewGHSource returns a [ReleaseSource] backed by the `gh` CLI. The
// runner argument is exposed only for tests; production callers
// should pass nil to get the default exec-based runner.
func NewGHSource(runner commandRunner) ReleaseSource {
	if runner == nil {
		runner = defaultCommandRunner
	}
	return &ghSource{run: runner}
}

// ghRepoFlag returns the "--repo owner/repo" pair used by every gh
// release subcommand.
func ghRepoFlag() string { return fmt.Sprintf("%s/%s", owner, repo) }

// Stable returns the latest non-prerelease, non-draft release.
func (g *ghSource) Stable(ctx context.Context) (*Release, error) {
	out, err := g.run(
		ctx, "gh", "release", "view",
		"--repo", ghRepoFlag(),
		"--json", "tagName,assets,body,isPrerelease,isDraft,publishedAt,url",
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGHCLIFailed, err)
	}
	var r ghRelease
	if jerr := json.Unmarshal(out, &r); jerr != nil {
		return nil, fmt.Errorf("hush/upgrade: parse gh response: %w", jerr)
	}
	return convertGHReleaseToRelease(&r)
}

// Beta returns the latest prerelease, falling back to the latest
// non-draft release of any kind when no prerelease exists.
func (g *ghSource) Beta(ctx context.Context) (*Release, error) {
	releases, err := g.list(ctx, 20)
	if err != nil {
		return nil, err
	}
	for i := range releases {
		if !releases[i].IsDraft && releases[i].IsPrerelease {
			return convertGHReleaseToRelease(&releases[i])
		}
	}
	for i := range releases {
		if !releases[i].IsDraft {
			return convertGHReleaseToRelease(&releases[i])
		}
	}
	return nil, ErrNoBetaReleasesFound
}

// Edge returns the most recent release of any kind, excluding drafts.
func (g *ghSource) Edge(ctx context.Context) (*Release, error) {
	releases, err := g.list(ctx, 5)
	if err != nil {
		return nil, err
	}
	for i := range releases {
		if !releases[i].IsDraft {
			return convertGHReleaseToRelease(&releases[i])
		}
	}
	return nil, ErrNoReleasesFound
}

// list runs `gh release list` and returns up to limit releases.
func (g *ghSource) list(ctx context.Context, limit int) ([]ghRelease, error) {
	out, err := g.run(
		ctx, "gh", "release", "list",
		"--repo", ghRepoFlag(),
		"--json", "tagName,assets,body,isPrerelease,isDraft,publishedAt,url",
		"--limit", fmt.Sprintf("%d", limit),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGHCLIFailed, err)
	}
	var releases []ghRelease
	if jerr := json.Unmarshal(out, &releases); jerr != nil {
		return nil, fmt.Errorf("hush/upgrade: parse gh list response: %w", jerr)
	}
	return releases, nil
}

// apiSource talks directly to the GitHub REST API. It is the fallback
// when the gh CLI is unavailable or errors.
type apiSource struct {
	httpClient *http.Client
	baseURL    string
}

// NewAPISource returns a [ReleaseSource] that hits api.github.com via
// httpClient. baseURL is exposed only for tests; production callers
// pass an empty string to get the real api.github.com endpoint.
func NewAPISource(httpClient *http.Client, baseURL string) ReleaseSource {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: apiTimeout}
	}
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &apiSource{httpClient: httpClient, baseURL: strings.TrimRight(baseURL, "/")}
}

// Stable resolves /repos/<owner>/<repo>/releases/latest.
func (a *apiSource) Stable(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", a.baseURL, owner, repo)
	var r Release
	if err := a.getJSON(ctx, url, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Beta resolves /repos/<owner>/<repo>/releases and picks the first
// prerelease, falling back to the first non-draft release.
func (a *apiSource) Beta(ctx context.Context) (*Release, error) {
	releases, err := a.allReleases(ctx)
	if err != nil {
		return nil, err
	}
	for i := range releases {
		if !releases[i].Draft && releases[i].Prerelease {
			return &releases[i], nil
		}
	}
	for i := range releases {
		if !releases[i].Draft {
			return &releases[i], nil
		}
	}
	return nil, ErrNoBetaReleasesFound
}

// Edge resolves /repos/<owner>/<repo>/releases and returns the first
// non-draft entry.
func (a *apiSource) Edge(ctx context.Context) (*Release, error) {
	releases, err := a.allReleases(ctx)
	if err != nil {
		return nil, err
	}
	for i := range releases {
		if !releases[i].Draft {
			return &releases[i], nil
		}
	}
	return nil, ErrNoReleasesFound
}

// allReleases fetches the unfiltered release list.
func (a *apiSource) allReleases(ctx context.Context) ([]Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases", a.baseURL, owner, repo)
	var releases []Release
	if err := a.getJSON(ctx, url, &releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// getJSON issues a GET request and decodes the response into out.
// Non-200 responses are surfaced as [ErrGitHubAPIFailed] with the
// status code appended for operator debugging.
func (a *apiSource) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("hush/upgrade: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrGitHubAPIFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Drain the body to keep the connection reusable. Capped at
		// 4KiB because GitHub error envelopes are tiny.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%w: status %d", ErrGitHubAPIFailed, resp.StatusCode)
	}

	dec := json.NewDecoder(resp.Body)
	if derr := dec.Decode(out); derr != nil {
		return fmt.Errorf("hush/upgrade: decode response: %w", derr)
	}
	return nil
}

// DefaultReleaseSource returns a ReleaseSource that prefers the gh
// CLI when available and falls back to the REST API on any error.
// Pass nil for runner / httpClient / baseURL to use the production
// defaults; tests override one or more for hermetic execution.
func DefaultReleaseSource(runner commandRunner, httpClient *http.Client, baseURL string) ReleaseSource {
	return &fallbackSource{
		gh:  NewGHSource(runner),
		api: NewAPISource(httpClient, baseURL),
		// hasGH is evaluated lazily so tests with empty PATH still
		// exercise the api fallback.
		hasGH: func() bool {
			_, err := exec.LookPath("gh")
			return err == nil
		},
	}
}

// fallbackSource composes a gh-CLI source and an API source. Every
// method tries gh first (only when gh is on PATH) and falls back to
// the API path on any error.
type fallbackSource struct {
	gh    ReleaseSource
	api   ReleaseSource
	hasGH func() bool
}

// Stable tries gh first, then falls back to the REST API.
func (f *fallbackSource) Stable(ctx context.Context) (*Release, error) {
	if f.hasGH != nil && f.hasGH() {
		if r, err := f.gh.Stable(ctx); err == nil {
			return r, nil
		}
	}
	return f.api.Stable(ctx)
}

// Beta tries gh first, then falls back to the REST API.
func (f *fallbackSource) Beta(ctx context.Context) (*Release, error) {
	if f.hasGH != nil && f.hasGH() {
		if r, err := f.gh.Beta(ctx); err == nil {
			return r, nil
		}
	}
	return f.api.Beta(ctx)
}

// Edge tries gh first, then falls back to the REST API.
func (f *fallbackSource) Edge(ctx context.Context) (*Release, error) {
	if f.hasGH != nil && f.hasGH() {
		if r, err := f.gh.Edge(ctx); err == nil {
			return r, nil
		}
	}
	return f.api.Edge(ctx)
}
