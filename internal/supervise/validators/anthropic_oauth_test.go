package validators_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/mrz1836/hush/internal/supervise/validators"
)

func anthropicOAuthCase() providerCase { return anthropicOAuthProviderCase() }

func TestValidator_InterfaceSatisfied_AnthropicOAuth(t *testing.T) {
	t.Parallel()
	runInterfaceSatisfied(t, validators.NewAnthropicOAuth(nil))
}

func TestValidator_AnthropicOAuth_HappyPath_200(t *testing.T) {
	t.Parallel()
	runHappyPath(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_StaleCredential_401(t *testing.T) {
	t.Parallel()
	runStale(t, anthropicOAuthCase(), http.StatusUnauthorized)
}

func TestValidator_AnthropicOAuth_StaleCredential_403(t *testing.T) {
	t.Parallel()
	runStale(t, anthropicOAuthCase(), http.StatusForbidden)
}

func TestValidator_AnthropicOAuth_NetworkError_5xx(t *testing.T) {
	t.Parallel()
	runNetwork5xx(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_Timeout(t *testing.T) {
	t.Parallel()
	runTimeout(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_NetworkError_Refused(t *testing.T) {
	t.Parallel()
	runRefused(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_Redirect3xx_ClassifiedAsNetwork(t *testing.T) {
	t.Parallel()
	runRedirect3xx(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_CtxCancelledBeforeSend_NoHandlerInvocation(t *testing.T) {
	t.Parallel()
	runCtxCancelledBefore(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_CtxCancelledMidFlight(t *testing.T) {
	t.Parallel()
	runCtxCancelledMid(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_SingleRequest(t *testing.T) {
	t.Parallel()
	runSingleRequest(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_Concurrent(t *testing.T) {
	t.Parallel()
	runConcurrent(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_DestroyedSecureBytes(t *testing.T) {
	t.Parallel()
	runDestroyedSecureBytes(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_NoLeakOnError(t *testing.T) {
	t.Parallel()
	runNoLeakOnError(t, anthropicOAuthCase())
}

func TestValidator_AnthropicOAuth_NameIsLockedString(t *testing.T) {
	t.Parallel()
	runNameIsLocked(t, anthropicOAuthCase(), "anthropic-oauth")
}

func TestValidator_AnthropicOAuth_AuthHeaderShape(t *testing.T) {
	t.Parallel()
	const secret = "anthropic-oauth-tok-26"
	runAuthHeaderShape(t, anthropicOAuthCase(), secret, func(t *testing.T, h http.Header, _ *url.URL) {
		t.Helper()
		want := "Bearer " + secret
		if got := h.Get("Authorization"); got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if got := h.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
		}
	})
}

func TestValidator_AnthropicOAuth_EmptyCredentialForwarded(t *testing.T) {
	t.Parallel()
	runEmptyCredentialForwarded(t, anthropicOAuthCase())
}
