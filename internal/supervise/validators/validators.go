// Package validators provides the pre-flight credential Validator
// interface plus five built-in implementations (anthropic,
// anthropic-oauth, openai, google-ai, github). Each validator answers
// "is this credential currently accepted by the upstream provider?"
// via a single read-only HTTP probe and returns one of three typed
// sentinel errors on failure: ErrStaleCredential (HTTP 401/403),
// ErrValidatorTimeout (request timeout / DeadlineExceeded), or
// ErrValidatorNetwork (any other transport / status failure).
//
// The credential is consumed exclusively via securebytes.Use(fn);
// no credential value is ever materialized as a Go string outside
// the single net/http.Header.Set conversion. No credential value,
// *http.Request, or *http.Response is ever passed to a logger, error
// formatter, or other byte sink.
package validators

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Sentinel errors — sentinel-class read-only globals. Compared via
// errors.Is, never mutated.
var (
	// ErrStaleCredential is returned when the upstream provider responds
	// with HTTP 401 or HTTP 403 — the credential is well-formed bytes but
	// the provider rejects it as no longer valid. Compare via
	// errors.Is(err, ErrStaleCredential).
	ErrStaleCredential = errors.New("validators: credential rejected by provider")

	// ErrValidatorTimeout is returned when the configured request timeout
	// fires, or when the supplied context.Context returns
	// context.DeadlineExceeded. Compare via
	// errors.Is(err, ErrValidatorTimeout).
	ErrValidatorTimeout = errors.New("validators: probe timeout")

	// ErrValidatorNetwork is returned for every other failure: any non-
	// 2xx-non-401/403 HTTP response (3xx, 4xx-other, 5xx, 429),
	// connection refused, DNS failure, TLS handshake failure, mid-flight
	// reset, context.Canceled on a not-yet-sent request, or
	// securebytes.ErrDestroyed (preserved in the wrapped chain).
	// Compare via errors.Is(err, ErrValidatorNetwork).
	ErrValidatorNetwork = errors.New("validators: probe network failure")
)

// Validator names. Compile-time constants.
const (
	anthropicName      = "anthropic"
	anthropicOAuthName = "anthropic-oauth"
	openaiName         = "openai"
	googleAIName       = "google-ai"
	githubName         = "github"
)

// Endpoint pinning. Compile-time constants.
const (
	anthropicEndpoint = "https://api.anthropic.com/v1/models"
	openaiEndpoint    = "https://api.openai.com/v1/models"
	googleAIEndpoint  = "https://generativelanguage.googleapis.com/v1beta/models"
	githubEndpoint    = "https://api.github.com/user"

	anthropicVersionHeader = "2023-06-01"
)

// Log-record outcome attribute values.
const (
	outcomeSuccess = "success"
	outcomeStale   = "stale"
	outcomeTimeout = "timeout"
	outcomeNetwork = "network"
)

const defaultTimeout = 5 * time.Second

// Validator answers "is this credential currently accepted by the
// upstream provider?" via a single read-only HTTP probe.
//
// Implementations are safe for concurrent invocation on the same
// instance with distinct *SecureBytes values. The credential MUST be
// consumed exclusively via SecureBytes.Use(fn). Validate returns nil
// on success (HTTP 2xx); on failure the returned error satisfies
// exactly one of errors.Is(err, ErrStaleCredential),
// errors.Is(err, ErrValidatorTimeout), or
// errors.Is(err, ErrValidatorNetwork).
type Validator interface {
	Validate(ctx context.Context, secret *securebytes.SecureBytes) error
}

// authHeaderBuilder constructs and sets the Authorization header on
// req inside the SecureBytes.Use(fn) scope. The implementation
// allocates a fresh []byte exactly len(prefix)+len(secret) long,
// copies prefix+secret in, calls req.Header.Set once, then zeroes
// every byte of the local buffer before returning.
type authHeaderBuilder func(req *http.Request, secret []byte) error

// Registry is the read-only lookup that maps each of the five fixed
// validator names to its corresponding Validator. Constructed once
// via NewRegistry; not mutated thereafter; concurrent Get calls are
// race-safe (the underlying map is never written after construction).
type Registry struct {
	byName map[string]Validator
}

// NewRegistry builds a Registry pre-populated with the five built-in
// validators. The supplied httpClient is shared across all five.
// Passing nil yields a default *http.Client with a 5-second timeout
// and redirect-follow disabled.
func NewRegistry(httpClient *http.Client) *Registry {
	return &Registry{
		byName: map[string]Validator{
			anthropicName:      NewAnthropic(httpClient),
			anthropicOAuthName: NewAnthropicOAuth(httpClient),
			openaiName:         NewOpenAI(httpClient),
			googleAIName:       NewGoogleAI(httpClient),
			githubName:         NewGitHub(httpClient),
		},
	}
}

// Get returns (registered Validator, true) for any of the five fixed
// lowercase names; returns (nil, false) for everything else
// including misspellings, case variants, whitespace-padded variants,
// and the empty string.
func (r *Registry) Get(name string) (Validator, bool) {
	v, ok := r.byName[name]
	return v, ok
}

// effectiveClient returns the caller-supplied client if non-nil,
// otherwise a default 5s-timeout client with redirect-follow disabled.
// No package-level singleton.
func effectiveClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{
		Timeout:       defaultTimeout,
		CheckRedirect: noFollowRedirect,
	}
}

func noFollowRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// doRequest is the single source of truth for: pre-cancel fast-path,
// request construction with extra headers, SecureBytes.Use(fn)-scoped
// builder invocation, per-request CheckRedirect override, the single
// http.Client.Do call, body drain+close, status-code classification,
// and slog record emission.
func doRequest(
	ctx context.Context,
	logger *slog.Logger,
	client *http.Client,
	name string,
	url string,
	extra http.Header,
	secret *securebytes.SecureBytes,
	builder authHeaderBuilder,
) error {
	log := logger
	if log == nil {
		log = slog.Default()
	}
	req, prepErr := prepareRequest(ctx, url, extra)
	if prepErr != nil {
		return classifyTransportError(ctx, log, name, prepErr)
	}
	if secErr := applySecret(req, secret, builder); secErr != nil {
		return emitWarnAndWrap(ctx, log, name, outcomeNetwork, 0, secErr)
	}
	return performRequest(ctx, log, client, name, req)
}

// prepareRequest honors pre-cancel, builds the request, and adds
// extra headers. Returns the bare transport-level error on failure so
// the caller can route it through classifyTransportError.
func prepareRequest(ctx context.Context, url string, extra http.Header) (*http.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	for k, vv := range extra {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	return req, nil
}

// performRequest installs the per-request CheckRedirect override,
// dispatches the single http.Client.Do call, drains+closes the body,
// and classifies the status code.
func performRequest(ctx context.Context, log *slog.Logger, client *http.Client, name string, req *http.Request) error {
	effective := *client
	effective.CheckRedirect = noFollowRedirect

	resp, err := effective.Do(req)
	if err != nil {
		return classifyTransportError(ctx, log, name, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	return classifyStatus(ctx, log, name, resp.StatusCode)
}

// applySecret runs the builder under SecureBytes.Use and folds any
// builder/destroyed error into a single ErrValidatorNetwork-wrapped chain.
func applySecret(req *http.Request, secret *securebytes.SecureBytes, builder authHeaderBuilder) error {
	var builderErr error
	if useErr := secret.Use(func(b []byte) {
		builderErr = builder(req, b)
	}); useErr != nil {
		return errors.Join(ErrValidatorNetwork, useErr)
	}
	if builderErr != nil {
		return errors.Join(ErrValidatorNetwork, builderErr)
	}
	return nil
}

// classifyStatus maps an HTTP status code to one of {nil, stale, network}.
func classifyStatus(ctx context.Context, log *slog.Logger, name string, status int) error {
	switch {
	case status >= 200 && status < 300:
		log.LogAttrs(ctx, slog.LevelDebug, "validator outcome",
			slog.String("validator", name),
			slog.String("outcome", outcomeSuccess),
			slog.Int("status", status))
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return emitWarnAndWrap(ctx, log, name, outcomeStale, status,
			fmt.Errorf("validators: %s: stale: %w", name, ErrStaleCredential))
	default:
		return emitWarnAndWrap(ctx, log, name, outcomeNetwork, status,
			fmt.Errorf("validators: %s: network: status %d: %w", name, status, ErrValidatorNetwork))
	}
}

// classifyTransportError maps a transport-level error chain to the
// correct sentinel via a single switch.
func classifyTransportError(ctx context.Context, log *slog.Logger, name string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return emitWarnAndWrap(ctx, log, name, outcomeTimeout, 0, errors.Join(ErrValidatorTimeout, err))
	}
	return emitWarnAndWrap(ctx, log, name, outcomeNetwork, 0, errors.Join(ErrValidatorNetwork, err))
}

// isTimeout inspects the error chain for net.Error.Timeout() == true.
func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}

// emitWarnAndWrap is the single WARN-level emission site. status == 0
// means "omit the status attribute" (transport-level failure without an
// HTTP response, pre-cancel fast-path, or destroyed-SecureBytes).
func emitWarnAndWrap(ctx context.Context, log *slog.Logger, name, outcome string, status int, returned error) error {
	attrs := []slog.Attr{
		slog.String("validator", name),
		slog.String("outcome", outcome),
	}
	if status > 0 {
		attrs = append(attrs, slog.Int("status", status))
	}
	log.LogAttrs(ctx, slog.LevelWarn, "validator outcome", attrs...)
	return returned
}
