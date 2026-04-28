package testutil

import (
	"fmt"
	"strings"
	"testing"
)

// SentinelSecret returns the canonical SECRET_SHOULD_NEVER_APPEAR_<n> marker string.
// It is stateless and pure — safe to call from multiple goroutines.
func SentinelSecret(n int) string {
	return fmt.Sprintf("SECRET_SHOULD_NEVER_APPEAR_%d", n)
}

// AssertSentinelAbsent fails t if sentinel appears anywhere in haystack.
// On failure it reports the byte offset of the first match and a 64-byte
// context window around it so the operator can see what leaked.
func AssertSentinelAbsent(t *testing.T, sentinel, haystack string) {
	t.Helper()
	i := strings.Index(haystack, sentinel)
	if i < 0 {
		return
	}
	start, end := sentinelContextWindow(i, len(sentinel), len(haystack))
	t.Errorf("hush/testutil: sentinel %q leaked at offset %d; context: %q",
		sentinel, i, haystack[start:end])
}

// sentinelContextWindow returns the [start, end) byte range for a 64-byte context
// window centered on the match at offset i in a string of length n.
func sentinelContextWindow(i, sentinelLen, n int) (start, end int) {
	start = i - 32
	if start < 0 {
		start = 0
	}
	end = i + sentinelLen + 32
	if end > n {
		end = n
	}
	return start, end
}
