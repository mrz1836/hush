package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetChannel(t *testing.T) {
	tests := []struct {
		name    string
		envVals map[string]string
		want    Channel
	}{
		{name: "default when unset", envVals: map[string]string{}, want: Stable},
		{name: "lowercase stable", envVals: map[string]string{"UPDATE_CHANNEL": "stable"}, want: Stable},
		{name: "uppercase STABLE", envVals: map[string]string{"UPDATE_CHANNEL": "STABLE"}, want: Stable},
		{name: "beta", envVals: map[string]string{"UPDATE_CHANNEL": "beta"}, want: Beta},
		{name: "mixed-case Beta", envVals: map[string]string{"UPDATE_CHANNEL": "Beta"}, want: Beta},
		{name: "edge", envVals: map[string]string{"UPDATE_CHANNEL": "edge"}, want: Edge},
		{name: "EDGE uppercase", envVals: map[string]string{"UPDATE_CHANNEL": "EDGE"}, want: Edge},
		{name: "padded spaces tolerated", envVals: map[string]string{"UPDATE_CHANNEL": "  beta  "}, want: Beta},
		{name: "unknown defaults to stable", envVals: map[string]string{"UPDATE_CHANNEL": "garbage"}, want: Stable},
		{name: "empty defaults to stable", envVals: map[string]string{"UPDATE_CHANNEL": ""}, want: Stable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.envVals[k] }
			assert.Equal(t, tt.want, GetChannel(getenv))
		})
	}
}

func TestGetChannel_NilGetenvDefaultsStable(t *testing.T) {
	assert.Equal(t, Stable, GetChannel(nil))
}

// stubSource lets a single test choose what each ReleaseSource method
// returns, and counts call-throughs for verifying the dispatch in
// LatestForChannel.
type stubSource struct {
	stable, beta, edge *Release
	stableErr          error
	betaErr            error
	edgeErr            error
	stableCalls        int
	betaCalls          int
	edgeCalls          int
}

func (s *stubSource) Stable(_ context.Context) (*Release, error) {
	s.stableCalls++
	return s.stable, s.stableErr
}

func (s *stubSource) Beta(_ context.Context) (*Release, error) {
	s.betaCalls++
	return s.beta, s.betaErr
}

func (s *stubSource) Edge(_ context.Context) (*Release, error) {
	s.edgeCalls++
	return s.edge, s.edgeErr
}

func TestLatestForChannel_Dispatches(t *testing.T) {
	src := &stubSource{
		stable: &Release{TagName: "v1.0.0"},
		beta:   &Release{TagName: "v1.1.0-beta.1"},
		edge:   &Release{TagName: "v1.1.0-rc.2"},
	}

	ctx := t.Context()
	r, err := LatestForChannel(ctx, src, Stable)
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0", r.TagName)

	r, err = LatestForChannel(ctx, src, Beta)
	require.NoError(t, err)
	assert.Equal(t, "v1.1.0-beta.1", r.TagName)

	r, err = LatestForChannel(ctx, src, Edge)
	require.NoError(t, err)
	assert.Equal(t, "v1.1.0-rc.2", r.TagName)

	// Unknown channel falls back to stable.
	r, err = LatestForChannel(ctx, src, Channel("nope"))
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0", r.TagName)

	assert.Equal(t, 2, src.stableCalls)
	assert.Equal(t, 1, src.betaCalls)
	assert.Equal(t, 1, src.edgeCalls)
}

// TestGHPath drives ghSource via a fake commandRunner so no `gh`
// binary is required.
func TestGHPath_StableParsesJSON(t *testing.T) {
	rel := ghRelease{
		TagName:      "v0.2.0",
		Body:         "release notes",
		IsPrerelease: false,
		IsDraft:      false,
		PublishedAt:  time.Now().UTC().Format(time.RFC3339),
		URL:          "https://example/release/v0.2.0",
		Assets: []ghReleaseFile{
			{Name: "hush_0.2.0_darwin_arm64.tar.gz", URL: "https://example/asset", Size: 100},
		},
	}
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		assert.Equal(t, "gh", name)
		assert.Contains(t, args, "view")
		b, err := json.Marshal(rel)
		require.NoError(t, err)
		return b, nil
	}
	src := NewGHSource(runner)
	out, err := src.Stable(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.2.0", out.TagName)
	require.Len(t, out.Assets, 1)
	assert.Equal(t, "https://example/asset", out.Assets[0].BrowserDownloadURL)
}

func TestGHPath_BetaFallsBackToStableWhenNoPrerelease(t *testing.T) {
	rels := []ghRelease{
		{TagName: "v0.2.0", IsDraft: false, IsPrerelease: false, PublishedAt: time.Now().UTC().Format(time.RFC3339)},
	}
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		assert.Contains(t, args, "list")
		b, err := json.Marshal(rels)
		require.NoError(t, err)
		return b, nil
	}
	src := NewGHSource(runner)
	out, err := src.Beta(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.2.0", out.TagName)
}

func TestGHPath_BetaErrorsWhenAllDraft(t *testing.T) {
	rels := []ghRelease{
		{TagName: "v0.2.0", IsDraft: true, PublishedAt: time.Now().UTC().Format(time.RFC3339)},
	}
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		b, err := json.Marshal(rels)
		require.NoError(t, err)
		return b, nil
	}
	src := NewGHSource(runner)
	_, err := src.Beta(t.Context())
	require.ErrorIs(t, err, ErrNoBetaReleasesFound)
}

func TestGHPath_EdgeReturnsFirstNonDraft(t *testing.T) {
	rels := []ghRelease{
		{TagName: "v0.3.0-rc.1", IsDraft: false, IsPrerelease: true, PublishedAt: time.Now().UTC().Format(time.RFC3339)},
		{TagName: "v0.2.0", IsDraft: false, PublishedAt: time.Now().UTC().Format(time.RFC3339)},
	}
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		b, err := json.Marshal(rels)
		require.NoError(t, err)
		return b, nil
	}
	src := NewGHSource(runner)
	out, err := src.Edge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.3.0-rc.1", out.TagName)
}

func TestGHPath_EdgeNoReleases(t *testing.T) {
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("[]"), nil
	}
	src := NewGHSource(runner)
	_, err := src.Edge(t.Context())
	require.ErrorIs(t, err, ErrNoReleasesFound)
}

func TestGHPath_GHCLIErrorPropagates(t *testing.T) {
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("boom")
	}
	src := NewGHSource(runner)
	_, err := src.Stable(t.Context())
	require.ErrorIs(t, err, ErrGHCLIFailed)
}

func TestGHPath_MalformedJSONErrors(t *testing.T) {
	runner := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("not json"), nil
	}
	src := NewGHSource(runner)
	_, err := src.Stable(t.Context())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrGHCLIFailed)
}

// TestAPIPath_Stable verifies the API source against an httptest
// server. The url path is the goreleaser-shaped /releases/latest.
func TestAPIPath_Stable(t *testing.T) {
	want := Release{
		TagName: "v0.2.0",
		Assets: []ReleaseAsset{
			{Name: "hush_0.2.0_linux_amd64.tar.gz", BrowserDownloadURL: "https://example/asset"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/mrz1836/hush/releases/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	t.Cleanup(srv.Close)

	src := NewAPISource(srv.Client(), srv.URL)
	got, err := src.Stable(t.Context())
	require.NoError(t, err)
	assert.Equal(t, want.TagName, got.TagName)
	assert.Equal(t, want.Assets[0].BrowserDownloadURL, got.Assets[0].BrowserDownloadURL)
}

func TestAPIPath_BetaPrefersPrerelease(t *testing.T) {
	releases := []Release{
		{TagName: "v0.3.0-beta.1", Prerelease: true},
		{TagName: "v0.2.0"},
	}
	srv := newReleasesServer(t, releases)

	src := NewAPISource(srv.Client(), srv.URL)
	got, err := src.Beta(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.3.0-beta.1", got.TagName)
}

func TestAPIPath_BetaFallsBackToStable(t *testing.T) {
	releases := []Release{{TagName: "v0.2.0"}}
	srv := newReleasesServer(t, releases)

	src := NewAPISource(srv.Client(), srv.URL)
	got, err := src.Beta(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.2.0", got.TagName)
}

func TestAPIPath_BetaNoReleases(t *testing.T) {
	srv := newReleasesServer(t, []Release{})

	src := NewAPISource(srv.Client(), srv.URL)
	_, err := src.Beta(t.Context())
	require.ErrorIs(t, err, ErrNoBetaReleasesFound)
}

func TestAPIPath_EdgeAllDraft(t *testing.T) {
	srv := newReleasesServer(t, []Release{
		{TagName: "v0.3.0", Draft: true},
	})

	src := NewAPISource(srv.Client(), srv.URL)
	_, err := src.Edge(t.Context())
	require.ErrorIs(t, err, ErrNoReleasesFound)
}

func TestAPIPath_404IsGitHubAPIFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"message": "not found"}`)
	}))
	t.Cleanup(srv.Close)

	src := NewAPISource(srv.Client(), srv.URL)
	_, err := src.Stable(t.Context())
	require.ErrorIs(t, err, ErrGitHubAPIFailed)
}

func TestAPIPath_NetworkFailureIsGitHubAPIFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // make every dial fail immediately

	src := NewAPISource(srv.Client(), srv.URL)
	_, err := src.Stable(t.Context())
	require.ErrorIs(t, err, ErrGitHubAPIFailed)
}

// fallbackSource: gh-CLI succeeds → api never called.
func TestFallbackSource_PrefersGHWhenAvailable(t *testing.T) {
	rel := &Release{TagName: "v0.9.0"}
	ghCalled, apiCalled := 0, 0
	fb := &fallbackSource{
		gh: stubFn{onStable: func() (*Release, error) {
			ghCalled++
			return rel, nil
		}},
		api: stubFn{onStable: func() (*Release, error) {
			apiCalled++
			return nil, errors.New("api should not be called")
		}},
		hasGH: func() bool { return true },
	}
	got, err := fb.Stable(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.9.0", got.TagName)
	assert.Equal(t, 1, ghCalled)
	assert.Equal(t, 0, apiCalled)
}

// fallbackSource: gh-CLI errors → api called.
func TestFallbackSource_FallsBackOnGHError(t *testing.T) {
	rel := &Release{TagName: "v0.9.0-api"}
	fb := &fallbackSource{
		gh:    stubFn{onStable: func() (*Release, error) { return nil, errors.New("gh failed") }},
		api:   stubFn{onStable: func() (*Release, error) { return rel, nil }},
		hasGH: func() bool { return true },
	}
	got, err := fb.Stable(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.9.0-api", got.TagName)
}

// fallbackSource: gh missing → api called directly.
func TestFallbackSource_SkipsGHWhenMissing(t *testing.T) {
	ghCalled := 0
	rel := &Release{TagName: "v0.9.0-api"}
	fb := &fallbackSource{
		gh: stubFn{onStable: func() (*Release, error) {
			ghCalled++
			return nil, errStubMethodNotSet
		}},
		api:   stubFn{onStable: func() (*Release, error) { return rel, nil }},
		hasGH: func() bool { return false },
	}
	got, err := fb.Stable(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.9.0-api", got.TagName)
	assert.Equal(t, 0, ghCalled)
}

func TestFallbackSource_BetaAndEdgeAlsoFallBack(t *testing.T) {
	rel := &Release{TagName: "v0.9.0-api"}
	fb := &fallbackSource{
		gh: stubFn{
			onBeta: func() (*Release, error) { return nil, errors.New("gh failed") },
			onEdge: func() (*Release, error) { return nil, errors.New("gh failed") },
		},
		api: stubFn{
			onBeta: func() (*Release, error) { return rel, nil },
			onEdge: func() (*Release, error) { return rel, nil },
		},
		hasGH: func() bool { return true },
	}
	beta, err := fb.Beta(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.9.0-api", beta.TagName)

	edge, err := fb.Edge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.9.0-api", edge.TagName)
}

// stubFn lets each fallbackSource test wire per-method behaviour
// without defining a new type. nil onX maps to (nil, nil).
type stubFn struct {
	onStable func() (*Release, error)
	onBeta   func() (*Release, error)
	onEdge   func() (*Release, error)
}

// errStubMethodNotSet is the sentinel returned by stubFn methods
// that the test left unset. Tests that depend on a particular
// dispatch path supply onStable / onBeta / onEdge explicitly.
var errStubMethodNotSet = errors.New("upgrade test stub: method not set")

func (s stubFn) Stable(_ context.Context) (*Release, error) {
	if s.onStable == nil {
		return nil, errStubMethodNotSet
	}
	return s.onStable()
}

func (s stubFn) Beta(_ context.Context) (*Release, error) {
	if s.onBeta == nil {
		return nil, errStubMethodNotSet
	}
	return s.onBeta()
}

func (s stubFn) Edge(_ context.Context) (*Release, error) {
	if s.onEdge == nil {
		return nil, errStubMethodNotSet
	}
	return s.onEdge()
}

// TestDefaultCommandRunner_RunsBinary ensures the production runner
// actually invokes os/exec. We pick `true`, which is present on every
// supported platform (linux, darwin) and exits 0 with no output.
func TestDefaultCommandRunner_RunsBinary(t *testing.T) {
	out, err := defaultCommandRunner(t.Context(), "true")
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestDefaultCommandRunner_PropagatesExitError(t *testing.T) {
	_, err := defaultCommandRunner(t.Context(), "false")
	require.Error(t, err)
}

func TestFallbackSource_BetaSkipsGHWhenMissing(t *testing.T) {
	ghCalled := 0
	rel := &Release{TagName: "v0.1.0-rc.1"}
	fb := &fallbackSource{
		gh: stubFn{onBeta: func() (*Release, error) {
			ghCalled++
			return nil, errStubMethodNotSet
		}},
		api:   stubFn{onBeta: func() (*Release, error) { return rel, nil }},
		hasGH: func() bool { return false },
	}
	got, err := fb.Beta(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0-rc.1", got.TagName)
	assert.Equal(t, 0, ghCalled)
}

func TestFallbackSource_EdgeSkipsGHWhenMissing(t *testing.T) {
	ghCalled := 0
	rel := &Release{TagName: "v0.1.0-rc.2"}
	fb := &fallbackSource{
		gh: stubFn{onEdge: func() (*Release, error) {
			ghCalled++
			return nil, errStubMethodNotSet
		}},
		api:   stubFn{onEdge: func() (*Release, error) { return rel, nil }},
		hasGH: func() bool { return false },
	}
	got, err := fb.Edge(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0-rc.2", got.TagName)
	assert.Equal(t, 0, ghCalled)
}

// newReleasesServer returns an httptest server that always responds
// to GET /repos/<owner>/<repo>/releases with a JSON-encoded body.
// Used to drive the API source's beta/edge code paths without
// touching the network.
func newReleasesServer(t *testing.T, body any) *httptest.Server {
	t.Helper()
	const wantPath = "/repos/mrz1836/hush/releases"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, wantPath, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}
