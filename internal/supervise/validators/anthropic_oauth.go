package validators

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// anthropicOAuthValidator implements Validator for Anthropic OAuth
// bearer tokens (header: Authorization: Bearer <token>).
type anthropicOAuthValidator struct {
	name    string
	url     string
	builder authHeaderBuilder
	extra   http.Header
	client  *http.Client
	logger  *slog.Logger
}

var _ Validator = (*anthropicOAuthValidator)(nil)

// NewAnthropicOAuth constructs the validator for Anthropic OAuth tokens.
func NewAnthropicOAuth(httpClient *http.Client) Validator {
	return &anthropicOAuthValidator{
		name:    anthropicOAuthName,
		url:     anthropicEndpoint,
		builder: setAnthropicOAuthAuth,
		extra:   http.Header{"anthropic-version": []string{anthropicVersionHeader}},
		client:  effectiveClient(httpClient),
	}
}

func (v *anthropicOAuthValidator) Validate(ctx context.Context, secret *securebytes.SecureBytes) error {
	return doRequest(ctx, v.logger, v.client, v.name, v.url, v.extra, secret, v.builder)
}

// setAnthropicOAuthAuth sets Authorization: Bearer <secret>.
func setAnthropicOAuthAuth(req *http.Request, secret []byte) error {
	const prefix = "Bearer "
	buf := make([]byte, len(prefix)+len(secret))
	copy(buf, prefix)
	copy(buf[len(prefix):], secret)
	req.Header.Set("Authorization", string(buf))
	for i := range buf {
		buf[i] = 0
	}
	return nil
}
