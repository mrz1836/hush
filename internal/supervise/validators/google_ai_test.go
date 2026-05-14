package validators_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mrz1836/hush/internal/supervise/validators"
)

func googleAICase() providerCase { return googleAIProviderCase() }

func TestValidator_InterfaceSatisfied_GoogleAI(t *testing.T) {
	t.Parallel()
	runInterfaceSatisfied(t, validators.NewGoogleAI(nil))
}

func TestValidator_GoogleAI_HappyPath_200(t *testing.T) {
	t.Parallel()
	runHappyPath(t, googleAICase())
}

func TestValidator_GoogleAI_StaleCredential_401(t *testing.T) {
	t.Parallel()
	runStale(t, googleAICase(), http.StatusUnauthorized)
}

func TestValidator_GoogleAI_StaleCredential_403(t *testing.T) {
	t.Parallel()
	runStale(t, googleAICase(), http.StatusForbidden)
}

func TestValidator_GoogleAI_NetworkError_5xx(t *testing.T) {
	t.Parallel()
	runNetwork5xx(t, googleAICase())
}

func TestValidator_GoogleAI_Timeout(t *testing.T) {
	t.Parallel()
	runTimeout(t, googleAICase())
}

func TestValidator_GoogleAI_NetworkError_Refused(t *testing.T) {
	t.Parallel()
	runRefused(t, googleAICase())
}

func TestValidator_GoogleAI_Redirect3xx_ClassifiedAsNetwork(t *testing.T) {
	t.Parallel()
	runRedirect3xx(t, googleAICase())
}

func TestValidator_GoogleAI_CtxCancelledBeforeSend_NoHandlerInvocation(t *testing.T) {
	t.Parallel()
	runCtxCancelledBefore(t, googleAICase())
}

func TestValidator_GoogleAI_CtxCancelledMidFlight(t *testing.T) {
	t.Parallel()
	runCtxCancelledMid(t, googleAICase())
}

func TestValidator_GoogleAI_SingleRequest(t *testing.T) {
	t.Parallel()
	runSingleRequest(t, googleAICase())
}

func TestValidator_GoogleAI_Concurrent(t *testing.T) {
	t.Parallel()
	runConcurrent(t, googleAICase())
}

func TestValidator_GoogleAI_DestroyedSecureBytes(t *testing.T) {
	t.Parallel()
	runDestroyedSecureBytes(t, googleAICase())
}

func TestValidator_GoogleAI_NoLeakOnError(t *testing.T) {
	t.Parallel()
	runNoLeakOnError(t, googleAICase())
}

func TestValidator_GoogleAI_NameIsLockedString(t *testing.T) {
	t.Parallel()
	runNameIsLocked(t, googleAICase(), "google-ai")
}

func TestValidator_GoogleAI_AuthHeaderShape(t *testing.T) {
	t.Parallel()
	const secret = "google-ai-test-key-26"
	runAuthHeaderShape(t, googleAICase(), secret, func(t *testing.T, h http.Header, u *url.URL) {
		t.Helper()
		if got := h.Get("x-goog-api-key"); got != secret {
			t.Errorf("x-goog-api-key = %q, want %q", got, secret)
		}
		if h.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header: %q", h.Get("Authorization"))
		}
		// Critical R-003d: NEVER use ?key=... query string for the secret.
		if strings.Contains(u.RawQuery, "key=") {
			t.Errorf("URL query carries key= (forbidden): %q", u.RawQuery)
		}
	})
}

func TestValidator_GoogleAI_EmptyCredentialForwarded(t *testing.T) {
	t.Parallel()
	runEmptyCredentialForwarded(t, googleAICase())
}
