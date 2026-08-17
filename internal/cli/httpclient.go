package cli

import (
	"net/http"
	"time"
)

// newHushHTTPClient builds the CLI's standard outbound HTTP client. Every
// CLI command talks to exactly one short-lived hush endpoint per process,
// so keep-alives buy nothing and idle pooling only risks reusing a stale
// connection to a restarted server: DisableKeepAlives closes each
// connection after its single request and MaxIdleConnsPerHost caps the
// pool at one.
//
// timeout is the whole-request ceiling (http.Client.Timeout). Pass 0 for
// no client-level deadline when the caller bounds each call with a
// per-request context instead.
func newHushHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives:   true,
			MaxIdleConnsPerHost: 1,
		},
	}
}
