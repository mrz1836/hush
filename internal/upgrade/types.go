package upgrade

import "time"

// Channel selects which GitHub release the upgrade driver resolves.
// Stable maps to /releases/latest; Beta prefers the most recent
// prerelease but falls back to stable; Edge picks the most recent
// release of any kind.
type Channel string

const (
	// Stable is the default release channel; resolves to GitHub's
	// "latest" release (which excludes prereleases and drafts).
	Stable Channel = "stable"
	// Beta resolves to the most recent prerelease, falling back to
	// the latest stable when no prerelease exists.
	Beta Channel = "beta"
	// Edge resolves to the most recent release of any kind
	// (including prereleases).
	Edge Channel = "edge"
)

// Release is the channel-agnostic representation of a GitHub release
// used by the upgrade driver. Both the gh CLI source and the direct
// REST API source converge on this shape; downstream code never sees
// the wire-format struct.
type Release struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	Prerelease  bool           `json:"prerelease"`
	Draft       bool           `json:"draft"`
	PublishedAt time.Time      `json:"published_at"`
	Body        string         `json:"body"`
	HTMLURL     string         `json:"html_url"`
	Assets      []ReleaseAsset `json:"assets"`
}

// ReleaseAsset is one downloadable file attached to a Release.
type ReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// ghRelease mirrors the JSON shape returned by `gh release view --json
// ...`. It is converted to [Release] by [convertGHReleaseToRelease]
// before leaving the package boundary.
type ghRelease struct {
	TagName      string          `json:"tagName"`
	Body         string          `json:"body"`
	IsPrerelease bool            `json:"isPrerelease"`
	IsDraft      bool            `json:"isDraft"`
	PublishedAt  string          `json:"publishedAt"`
	URL          string          `json:"url"`
	Assets       []ghReleaseFile `json:"assets"`
}

// ghReleaseFile mirrors one asset entry inside a `gh release view`
// JSON document.
type ghReleaseFile struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}
