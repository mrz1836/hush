package validators

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// provider is the single concrete Validator implementation shared by every
// built-in provider. The five public constructors (NewAnthropic,
// NewAnthropicOAuth, NewOpenAI, NewGoogleAI, NewGitHub) each instantiate
// this struct with the per-provider name, endpoint URL, extra-header set,
// and auth-header builder.
//
// The struct is unexported; callers interact via the Validator interface
// returned by the constructors. The single concrete type replaces the
// five identical typed-struct files that preceded it.
type provider struct {
	name    string
	url     string
	builder authHeaderBuilder
	extra   http.Header
	client  *http.Client
	logger  *slog.Logger
}

var _ Validator = (*provider)(nil)

// Validate dispatches to the shared doRequest pipeline. The per-provider
// behavioral differences are entirely carried by the builder + extra fields.
func (v *provider) Validate(ctx context.Context, secret *securebytes.SecureBytes) error {
	return doRequest(ctx, v.logger, v.client, v.name, v.url, v.extra, secret, v.builder)
}

// NewAnthropic constructs the validator for Anthropic API keys
// (header: x-api-key).
func NewAnthropic(httpClient *http.Client) Validator {
	return &provider{
		name:    anthropicName,
		url:     anthropicEndpoint,
		builder: setAnthropicAuth,
		extra:   http.Header{"anthropic-version": []string{anthropicVersionHeader}},
		client:  effectiveClient(httpClient),
	}
}

// NewAnthropicOAuth constructs the validator for Anthropic OAuth bearer
// tokens (header: Authorization: Bearer <token>).
func NewAnthropicOAuth(httpClient *http.Client) Validator {
	return &provider{
		name:    anthropicOAuthName,
		url:     anthropicEndpoint,
		builder: setAnthropicOAuthAuth,
		extra:   http.Header{"anthropic-version": []string{anthropicVersionHeader}},
		client:  effectiveClient(httpClient),
	}
}

// NewOpenAI constructs the validator for OpenAI API keys
// (header: Authorization: Bearer <key>).
func NewOpenAI(httpClient *http.Client) Validator {
	return &provider{
		name:    openaiName,
		url:     openaiEndpoint,
		builder: setOpenAIAuth,
		extra:   nil,
		client:  effectiveClient(httpClient),
	}
}

// NewGoogleAI constructs the validator for Google Generative Language keys
// (header: x-goog-api-key — never ?key= query string).
func NewGoogleAI(httpClient *http.Client) Validator {
	return &provider{
		name:    googleAIName,
		url:     googleAIEndpoint,
		builder: setGoogleAIAuth,
		extra:   nil,
		client:  effectiveClient(httpClient),
	}
}

// NewGitHub constructs the validator for GitHub personal access tokens
// (header: Authorization: token <pat>).
func NewGitHub(httpClient *http.Client) Validator {
	return &provider{
		name:    githubName,
		url:     githubEndpoint,
		builder: setGitHubAuth,
		extra:   http.Header{"Accept": []string{"application/vnd.github+json"}},
		client:  effectiveClient(httpClient),
	}
}

// --- Auth-header builders ----------------------------------------------------
//
// Each builder allocates a fresh []byte exactly len(prefix)+len(secret)
// long, copies prefix+secret in, calls req.Header.Set once, then zeroes
// every byte of the local buffer before returning. The secret bytes never
// escape into a Go string outside this single Header.Set conversion.
//
// SECURITY INVARIANT (Constitution VIII; enforced by AST test
// TestPackage_AllBuildersZeroLocalBuffer): every builder must end with a
// for-range zero-loop over its OWN local buffer immediately before its
// return statement. Do NOT extract the zero-loop into a shared helper —
// per-function independence is what makes each builder independently
// auditable. The visual duplication is intentional and load-bearing.

// setAnthropicAuth sets the Anthropic x-api-key header.
// The raw secret bytes are the header value (no prefix).
func setAnthropicAuth(req *http.Request, secret []byte) error {
	buf := make([]byte, len(secret))
	copy(buf, secret)
	req.Header.Set("x-api-key", string(buf))
	for i := range buf {
		buf[i] = 0
	}
	return nil
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

// setGoogleAIAuth sets the x-goog-api-key header.
// The raw secret bytes are the header value (no prefix).
func setGoogleAIAuth(req *http.Request, secret []byte) error {
	buf := make([]byte, len(secret))
	copy(buf, secret)
	req.Header.Set("x-goog-api-key", string(buf))
	for i := range buf {
		buf[i] = 0
	}
	return nil
}
