// Package contracts is the review-only typed mirror of the exported
// API surface for SDD-26 (internal/supervise/validators). It is NOT
// the implementation; it is a compile-checked specification of the
// public symbols the implementation MUST export verbatim.
//
// The package is named "contracts" and lives at
// specs/026-validators-builtins/contracts/api.go so it does not collide
// with any importable package. It is excluded from the production
// build via the SDD-26 build tag below.
//
//go:build ignore
// +build ignore

package contracts

import (
	"context"
	"errors"
	"net/http"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// -----------------------------------------------------------------------------
// Validator interface — locked at SDD-26.
// -----------------------------------------------------------------------------

// Validator answers "is this credential currently accepted by the
// upstream provider?" via a single read-only HTTP probe against the
// provider's documented credential-validation endpoint. See
// data-model.md §1 for invariants V-1..V-3.
type Validator interface {
	// Validate returns nil on HTTP 2xx, or exactly one of the three
	// sentinel-wrapped errors below.
	//
	// FR-001: single-method interface.
	// FR-003: exactly one of four mutually-exclusive verdicts.
	// FR-006: consumes secret exclusively via securebytes.Use(fn).
	// FR-013: honours ctx (pre-cancel fast-path + mid-flight cancellation).
	// FR-019: at most one outbound HTTP request per invocation.
	// FR-020: emits exactly one slog record per invocation.
	Validate(ctx context.Context, secret *securebytes.SecureBytes) error
}

// -----------------------------------------------------------------------------
// Registry — locked at SDD-26.
// -----------------------------------------------------------------------------

// Registry maps each of the five fixed FR-010 names to its
// corresponding *Validator. Opaque struct; consumed only via
// NewRegistry + Get. See data-model.md §2 for invariants R-1..R-5.
type Registry struct {
	// unexported byName map[string]Validator — locked at implementation
}

// NewRegistry constructs a Registry pre-populated with the five
// built-in validators. The supplied httpClient is shared across all
// five (each validator may shallow-copy it per call to override
// CheckRedirect — see research.md R-005). Passing nil yields a
// default *http.Client{Timeout: 5s, CheckRedirect: <ErrUseLastResponse>}.
//
// FR-012: 5-second default; operator override accepted.
// FR-016: no global HTTP client default; no package-level mutable state.
func NewRegistry(httpClient *http.Client) *Registry { panic("contract: unimplemented") }

// Get returns (registered Validator, true) for any of the five fixed
// FR-010 lowercase names; returns (nil, false) for everything else
// including misspellings, case variants, whitespace-padded variants,
// and the empty string.
//
// FR-011: closed lookup.
// SC-007: exactly five recognised names.
func (r *Registry) Get(name string) (Validator, bool) { panic("contract: unimplemented") }

// -----------------------------------------------------------------------------
// Per-provider constructors — locked at SDD-26.
// -----------------------------------------------------------------------------
//
// Each constructor returns the Validator interface (NOT a concrete type)
// per the chunk-doc-locked API. The five concrete validator types are
// unexported (see data-model.md §3).
//
// Each constructor follows the same shape:
//   - accepts an *http.Client (nil → default 5s-timeout client);
//   - constructs an unexported <provider>Validator with the pinned
//     endpoint URL, FR-010 name string, per-provider auth-header
//     builder, and optional extra headers;
//   - returns the resulting *<provider>Validator cast to Validator.
//
// Endpoint URLs are pinned in research.md R-003a..R-003e.

// NewAnthropic constructs the validator for Anthropic API keys
// (header: x-api-key + anthropic-version).
// Endpoint: GET https://api.anthropic.com/v1/models (research.md R-003a).
func NewAnthropic(httpClient *http.Client) Validator { panic("contract: unimplemented") }

// NewAnthropicOAuth constructs the validator for Anthropic OAuth tokens
// (header: Authorization: Bearer <token> + anthropic-version).
// Endpoint: GET https://api.anthropic.com/v1/models (research.md R-003b).
func NewAnthropicOAuth(httpClient *http.Client) Validator { panic("contract: unimplemented") }

// NewOpenAI constructs the validator for OpenAI API keys
// (header: Authorization: Bearer <key>).
// Endpoint: GET https://api.openai.com/v1/models (research.md R-003c).
func NewOpenAI(httpClient *http.Client) Validator { panic("contract: unimplemented") }

// NewGoogleAI constructs the validator for Google Generative Language API keys
// (header: x-goog-api-key).
// Endpoint: GET https://generativelanguage.googleapis.com/v1beta/models
// (research.md R-003d).
func NewGoogleAI(httpClient *http.Client) Validator { panic("contract: unimplemented") }

// NewGitHub constructs the validator for GitHub personal access tokens
// (header: Authorization: token <pat> + Accept).
// Endpoint: GET https://api.github.com/user (research.md R-003e).
func NewGitHub(httpClient *http.Client) Validator { panic("contract: unimplemented") }

// -----------------------------------------------------------------------------
// Sentinel errors — locked at SDD-26.
// -----------------------------------------------------------------------------

// ErrStaleCredential is returned when the upstream provider responds
// with HTTP 401 or HTTP 403 — the credential is well-formed bytes but
// the provider rejects it as no longer valid.
//
// FR-002, FR-004. Compare via errors.Is(err, ErrStaleCredential).
var ErrStaleCredential = errors.New("validators: credential rejected by provider")

// ErrValidatorTimeout is returned when the configured request timeout
// fires, or when the supplied context.Context returns
// context.DeadlineExceeded.
//
// FR-002, FR-005. Compare via errors.Is(err, ErrValidatorTimeout).
var ErrValidatorTimeout = errors.New("validators: probe timeout")

// ErrValidatorNetwork is returned for every other failure: any non-2xx-
// non-401/403 HTTP response (3xx, 4xx-other, 5xx, 429), connection
// refused, DNS failure, TLS handshake failure, mid-flight reset,
// context.Canceled on a not-yet-sent request, or
// securebytes.ErrDestroyed (preserved in the wrapped chain per spec
// Clarification Q6).
//
// FR-002, FR-005, FR-021, spec Clarification Q3 + Q6 + Q9.
// Compare via errors.Is(err, ErrValidatorNetwork).
var ErrValidatorNetwork = errors.New("validators: probe network failure")
