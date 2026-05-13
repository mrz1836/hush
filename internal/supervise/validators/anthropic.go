package validators

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// anthropicValidator implements Validator for Anthropic API keys
// (header: x-api-key). Endpoint pinned at R-003a.
type anthropicValidator struct {
	name    string
	url     string
	builder authHeaderBuilder
	extra   http.Header
	client  *http.Client
	logger  *slog.Logger
}

var _ Validator = (*anthropicValidator)(nil)

// NewAnthropic constructs the validator for Anthropic API keys.
func NewAnthropic(httpClient *http.Client) Validator {
	return &anthropicValidator{
		name:    anthropicName,
		url:     anthropicEndpoint,
		builder: setAnthropicAuth,
		extra:   http.Header{"anthropic-version": []string{anthropicVersionHeader}},
		client:  effectiveClient(httpClient),
	}
}

func (v *anthropicValidator) Validate(ctx context.Context, secret *securebytes.SecureBytes) error {
	return doRequest(ctx, v.logger, v.client, v.name, v.url, v.extra, secret, v.builder)
}

// setAnthropicAuth sets the Anthropic x-api-key header. The raw secret
// bytes are the header value (no prefix). A fresh []byte is allocated,
// copied into, used once, and zeroed before return.
func setAnthropicAuth(req *http.Request, secret []byte) error {
	buf := make([]byte, len(secret))
	copy(buf, secret)
	req.Header.Set("x-api-key", string(buf))
	for i := range buf {
		buf[i] = 0
	}
	return nil
}
