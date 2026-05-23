package upgrade

import (
	"fmt"
	"time"
)

// convertGHReleaseToRelease lifts a gh-CLI shaped release into the
// canonical Release type. The gh CLI does not emit a separate "name"
// field, so we reuse TagName for both. PublishedAt is parsed as
// RFC 3339 — gh always emits that format.
func convertGHReleaseToRelease(gh *ghRelease) (*Release, error) {
	publishedAt, err := time.Parse(time.RFC3339, gh.PublishedAt)
	if err != nil {
		return nil, fmt.Errorf("hush/upgrade: parse publishedAt: %w", err)
	}

	assets := make([]ReleaseAsset, len(gh.Assets))
	for i, a := range gh.Assets {
		assets[i] = ReleaseAsset{
			Name:               a.Name,
			BrowserDownloadURL: a.URL,
			Size:               a.Size,
		}
	}

	return &Release{
		TagName:     gh.TagName,
		Name:        gh.TagName,
		Prerelease:  gh.IsPrerelease,
		Draft:       gh.IsDraft,
		PublishedAt: publishedAt,
		Body:        gh.Body,
		HTMLURL:     gh.URL,
		Assets:      assets,
	}, nil
}
