package validators_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/mrz1836/hush/internal/supervise/validators"
)

func anthropicCase() providerCase { return anthropicProviderCase() }

func TestValidator_InterfaceSatisfied_Anthropic(t *testing.T) {
	t.Parallel()
	runInterfaceSatisfied(t, validators.NewAnthropic(nil))
}

func TestValidator_Anthropic_HappyPath_200(t *testing.T) {
	t.Parallel()
	runHappyPath(t, anthropicCase())
}

func TestValidator_Anthropic_StaleCredential_401(t *testing.T) {
	t.Parallel()
	runStale(t, anthropicCase(), http.StatusUnauthorized)
}

func TestValidator_Anthropic_StaleCredential_403(t *testing.T) {
	t.Parallel()
	runStale(t, anthropicCase(), http.StatusForbidden)
}

func TestValidator_Anthropic_NetworkError_5xx(t *testing.T) {
	t.Parallel()
	runNetwork5xx(t, anthropicCase())
}

func TestValidator_Anthropic_Timeout(t *testing.T) {
	t.Parallel()
	runTimeout(t, anthropicCase())
}

func TestValidator_Anthropic_NetworkError_Refused(t *testing.T) {
	t.Parallel()
	runRefused(t, anthropicCase())
}

func TestValidator_Anthropic_Redirect3xx_ClassifiedAsNetwork(t *testing.T) {
	t.Parallel()
	runRedirect3xx(t, anthropicCase())
}

func TestValidator_Anthropic_CtxCancelledBeforeSend_NoHandlerInvocation(t *testing.T) {
	t.Parallel()
	runCtxCancelledBefore(t, anthropicCase())
}

func TestValidator_Anthropic_CtxCancelledMidFlight(t *testing.T) {
	t.Parallel()
	runCtxCancelledMid(t, anthropicCase())
}

func TestValidator_Anthropic_SingleRequest(t *testing.T) {
	t.Parallel()
	runSingleRequest(t, anthropicCase())
}

func TestValidator_Anthropic_Concurrent(t *testing.T) {
	t.Parallel()
	runConcurrent(t, anthropicCase())
}

func TestValidator_Anthropic_DestroyedSecureBytes(t *testing.T) {
	t.Parallel()
	runDestroyedSecureBytes(t, anthropicCase())
}

func TestValidator_Anthropic_NoLeakOnError(t *testing.T) {
	t.Parallel()
	runNoLeakOnError(t, anthropicCase())
}

func TestValidator_Anthropic_NameIsLockedString(t *testing.T) {
	t.Parallel()
	runNameIsLocked(t, anthropicCase(), "anthropic")
}

func TestValidator_Anthropic_AuthHeaderShape(t *testing.T) {
	t.Parallel()
	const secret = "anthropic-test-key-26"
	runAuthHeaderShape(t, anthropicCase(), secret, func(t *testing.T, h http.Header, _ *url.URL) {
		t.Helper()
		got := h.Get("x-api-key")
		if got != secret {
			t.Errorf("x-api-key = %q, want %q", got, secret)
		}
		if h.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header: %q", h.Get("Authorization"))
		}
		if got := h.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
		}
	})
}

func TestValidator_Anthropic_EmptyCredentialForwarded(t *testing.T) {
	t.Parallel()
	runEmptyCredentialForwarded(t, anthropicCase())
}
