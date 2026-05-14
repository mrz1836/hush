package validators

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// githubValidator implements Validator for GitHub personal access tokens
// (header: Authorization: token <pat>). Endpoint pinned at R-003e.
type githubValidator struct {
	name    string
	url     string
	builder authHeaderBuilder
	extra   http.Header
	client  *http.Client
	logger  *slog.Logger
}

var _ Validator = (*githubValidator)(nil)

// NewGitHub constructs the validator for GitHub personal access tokens.
func NewGitHub(httpClient *http.Client) Validator {
	return &githubValidator{
		name:    githubName,
		url:     githubEndpoint,
		builder: setGitHubAuth,
		extra:   http.Header{"Accept": []string{"application/vnd.github+json"}},
		client:  effectiveClient(httpClient),
	}
}

func (v *githubValidator) Validate(ctx context.Context, secret *securebytes.SecureBytes) error {
	return doRequest(ctx, v.logger, v.client, v.name, v.url, v.extra, secret, v.builder)
}

// setGitHubAuth sets Authorization: token <secret>.
func setGitHubAuth(req *http.Request, secret []byte) error {
	const prefix = "token "
	buf := make([]byte, len(prefix)+len(secret))
	copy(buf, prefix)
	copy(buf[len(prefix):], secret)
	req.Header.Set("Authorization", string(buf))
	for i := range buf {
		buf[i] = 0
	}
	return nil
}
