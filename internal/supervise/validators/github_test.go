package validators_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/mrz1836/hush/internal/supervise/validators"
)

func githubCase() providerCase { return githubProviderCase() }

func TestValidator_InterfaceSatisfied_GitHub(t *testing.T) {
	t.Parallel()
	runInterfaceSatisfied(t, validators.NewGitHub(nil))
}

func TestValidator_GitHub_HappyPath_200(t *testing.T) {
	t.Parallel()
	runHappyPath(t, githubCase())
}

func TestValidator_GitHub_StaleCredential_401(t *testing.T) {
	t.Parallel()
	runStale(t, githubCase(), http.StatusUnauthorized)
}

func TestValidator_GitHub_StaleCredential_403(t *testing.T) {
	t.Parallel()
	runStale(t, githubCase(), http.StatusForbidden)
}

func TestValidator_GitHub_NetworkError_5xx(t *testing.T) {
	t.Parallel()
	runNetwork5xx(t, githubCase())
}

func TestValidator_GitHub_Timeout(t *testing.T) {
	t.Parallel()
	runTimeout(t, githubCase())
}

func TestValidator_GitHub_NetworkError_Refused(t *testing.T) {
	t.Parallel()
	runRefused(t, githubCase())
}

func TestValidator_GitHub_Redirect3xx_ClassifiedAsNetwork(t *testing.T) {
	t.Parallel()
	runRedirect3xx(t, githubCase())
}

func TestValidator_GitHub_CtxCancelledBeforeSend_NoHandlerInvocation(t *testing.T) {
	t.Parallel()
	runCtxCancelledBefore(t, githubCase())
}

func TestValidator_GitHub_CtxCancelledMidFlight(t *testing.T) {
	t.Parallel()
	runCtxCancelledMid(t, githubCase())
}

func TestValidator_GitHub_SingleRequest(t *testing.T) {
	t.Parallel()
	runSingleRequest(t, githubCase())
}

func TestValidator_GitHub_Concurrent(t *testing.T) {
	t.Parallel()
	runConcurrent(t, githubCase())
}

func TestValidator_GitHub_DestroyedSecureBytes(t *testing.T) {
	t.Parallel()
	runDestroyedSecureBytes(t, githubCase())
}

func TestValidator_GitHub_NoLeakOnError(t *testing.T) {
	t.Parallel()
	runNoLeakOnError(t, githubCase())
}

func TestValidator_GitHub_NameIsLockedString(t *testing.T) {
	t.Parallel()
	runNameIsLocked(t, githubCase(), "github")
}

func TestValidator_GitHub_AuthHeaderShape(t *testing.T) {
	t.Parallel()
	const secret = "github-pat-26"
	runAuthHeaderShape(t, githubCase(), secret, func(t *testing.T, h http.Header, _ *url.URL) {
		t.Helper()
		want := "token " + secret
		if got := h.Get("Authorization"); got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if got := h.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q, want application/vnd.github+json", got)
		}
	})
}

func TestValidator_GitHub_EmptyCredentialForwarded(t *testing.T) {
	t.Parallel()
	runEmptyCredentialForwarded(t, githubCase())
}
