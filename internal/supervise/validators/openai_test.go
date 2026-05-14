package validators_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/mrz1836/hush/internal/supervise/validators"
)

func openaiCase() providerCase { return openaiProviderCase() }

func TestValidator_InterfaceSatisfied_OpenAI(t *testing.T) {
	t.Parallel()
	runInterfaceSatisfied(t, validators.NewOpenAI(nil))
}

func TestValidator_OpenAI_HappyPath_200(t *testing.T) {
	t.Parallel()
	runHappyPath(t, openaiCase())
}

func TestValidator_OpenAI_StaleCredential_401(t *testing.T) {
	t.Parallel()
	runStale(t, openaiCase(), http.StatusUnauthorized)
}

func TestValidator_OpenAI_StaleCredential_403(t *testing.T) {
	t.Parallel()
	runStale(t, openaiCase(), http.StatusForbidden)
}

func TestValidator_OpenAI_NetworkError_5xx(t *testing.T) {
	t.Parallel()
	runNetwork5xx(t, openaiCase())
}

func TestValidator_OpenAI_Timeout(t *testing.T) {
	t.Parallel()
	runTimeout(t, openaiCase())
}

func TestValidator_OpenAI_NetworkError_Refused(t *testing.T) {
	t.Parallel()
	runRefused(t, openaiCase())
}

func TestValidator_OpenAI_Redirect3xx_ClassifiedAsNetwork(t *testing.T) {
	t.Parallel()
	runRedirect3xx(t, openaiCase())
}

func TestValidator_OpenAI_CtxCancelledBeforeSend_NoHandlerInvocation(t *testing.T) {
	t.Parallel()
	runCtxCancelledBefore(t, openaiCase())
}

func TestValidator_OpenAI_CtxCancelledMidFlight(t *testing.T) {
	t.Parallel()
	runCtxCancelledMid(t, openaiCase())
}

func TestValidator_OpenAI_SingleRequest(t *testing.T) {
	t.Parallel()
	runSingleRequest(t, openaiCase())
}

func TestValidator_OpenAI_Concurrent(t *testing.T) {
	t.Parallel()
	runConcurrent(t, openaiCase())
}

func TestValidator_OpenAI_DestroyedSecureBytes(t *testing.T) {
	t.Parallel()
	runDestroyedSecureBytes(t, openaiCase())
}

func TestValidator_OpenAI_NoLeakOnError(t *testing.T) {
	t.Parallel()
	runNoLeakOnError(t, openaiCase())
}

func TestValidator_OpenAI_NameIsLockedString(t *testing.T) {
	t.Parallel()
	runNameIsLocked(t, openaiCase(), "openai")
}

func TestValidator_OpenAI_AuthHeaderShape(t *testing.T) {
	t.Parallel()
	const secret = "openai-test-key-26"
	runAuthHeaderShape(t, openaiCase(), secret, func(t *testing.T, h http.Header, _ *url.URL) {
		t.Helper()
		want := "Bearer " + secret
		if got := h.Get("Authorization"); got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if got := h.Get("anthropic-version"); got != "" {
			t.Errorf("unexpected anthropic-version header: %q", got)
		}
		if got := h.Get("x-api-key"); got != "" {
			t.Errorf("unexpected x-api-key header: %q", got)
		}
	})
}

func TestValidator_OpenAI_EmptyCredentialForwarded(t *testing.T) {
	t.Parallel()
	runEmptyCredentialForwarded(t, openaiCase())
}
