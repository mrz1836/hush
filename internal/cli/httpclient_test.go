package cli

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewHushHTTPClient asserts the transport contract every CLI command
// depends on: single-use connections (no keep-alives), a one-connection
// idle cap, and the caller-supplied whole-request timeout (0 == none).
func TestNewHushHTTPClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		timeout time.Duration
	}{
		{name: "with timeout", timeout: 5 * time.Second},
		{name: "no timeout", timeout: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := newHushHTTPClient(tc.timeout)
			require.NotNil(t, c)
			assert.Equal(t, tc.timeout, c.Timeout)

			tr, ok := c.Transport.(*http.Transport)
			require.True(t, ok, "transport must be *http.Transport")
			assert.True(t, tr.DisableKeepAlives, "keep-alives must be disabled")
			assert.Equal(t, 1, tr.MaxIdleConnsPerHost)
		})
	}
}
