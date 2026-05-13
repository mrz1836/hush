package validators

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// openaiValidator implements Validator for OpenAI API keys (header:
// Authorization: Bearer <key>). Endpoint pinned at R-003c.
type openaiValidator struct {
	name    string
	url     string
	builder authHeaderBuilder
	extra   http.Header
	client  *http.Client
	logger  *slog.Logger
}

var _ Validator = (*openaiValidator)(nil)

// NewOpenAI constructs the validator for OpenAI API keys.
func NewOpenAI(httpClient *http.Client) Validator {
	return &openaiValidator{
		name:    openaiName,
		url:     openaiEndpoint,
		builder: setOpenAIAuth,
		extra:   nil,
		client:  effectiveClient(httpClient),
	}
}

func (v *openaiValidator) Validate(ctx context.Context, secret *securebytes.SecureBytes) error {
	return doRequest(ctx, v.logger, v.client, v.name, v.url, v.extra, secret, v.builder)
}

// setOpenAIAuth sets Authorization: Bearer <secret>.
func setOpenAIAuth(req *http.Request, secret []byte) error {
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
