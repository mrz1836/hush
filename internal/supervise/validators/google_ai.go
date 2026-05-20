package validators

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// googleAIValidator implements Validator for Google Generative Language
// API keys (header: x-goog-api-key — never ?key= query string).
type googleAIValidator struct {
	name    string
	url     string
	builder authHeaderBuilder
	extra   http.Header
	client  *http.Client
	logger  *slog.Logger
}

var _ Validator = (*googleAIValidator)(nil)

// NewGoogleAI constructs the validator for Google Generative Language keys.
func NewGoogleAI(httpClient *http.Client) Validator {
	return &googleAIValidator{
		name:    googleAIName,
		url:     googleAIEndpoint,
		builder: setGoogleAIAuth,
		extra:   nil,
		client:  effectiveClient(httpClient),
	}
}

func (v *googleAIValidator) Validate(ctx context.Context, secret *securebytes.SecureBytes) error {
	return doRequest(ctx, v.logger, v.client, v.name, v.url, v.extra, secret, v.builder)
}

// setGoogleAIAuth sets the x-goog-api-key header (header only, never
// query string).
func setGoogleAIAuth(req *http.Request, secret []byte) error {
	buf := make([]byte, len(secret))
	copy(buf, secret)
	req.Header.Set("x-goog-api-key", string(buf))
	for i := range buf {
		buf[i] = 0
	}
	return nil
}
