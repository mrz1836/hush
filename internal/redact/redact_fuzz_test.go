package redact_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/redact"
	"github.com/mrz1836/hush/internal/testutil"
)

// maxInputLen mirrors the unexported redact.maxInputLen truncation cap.
// Kept in sync by the length-bound invariant below; if the production
// constant changes, TestCommandPreview_TruncatesOversizeInput and this
// fuzz target will surface the drift.
const maxInputLen = 8 * 1024

// FuzzCommandPreview exercises the sole command-string redactor on the
// Discord-approval + signed-audit path. It asserts the invariants the
// package doc promises, plus the security no-leak property, against
// arbitrary and adversarial input.
//
// Invariants:
//   - never panics;
//   - idempotent within the size bound: once output is at or under
//     maxInputLen, a second pass is a no-op (redaction labels are inert
//     and no new matches appear). The only non-idempotent corner is when
//     redaction *growth* (many minimal Slack-token matches) pushes output
//     past the truncation cap and the next pass re-trims it; that is
//     outside the documented normal-size regime and never leaks a secret;
//   - output length stays bounded by the maxInputLen truncation contract
//     (plus the small, bounded slack-token growth margin);
//   - a known token-shaped secret is never surfaced verbatim.
func FuzzCommandPreview(f *testing.F) {
	// Seed corpus: known token shapes embedded in junk, plus empty,
	// unicode, and oversize inputs. The fuzzer mutates from here.
	seeds := []string{
		"",
		"git push origin master",
		"emoji 🔐 and unicode ☃ then done",
		`curl -H "x-api-key: sk-ant-api03-AAAAAAAAAAAAAAAAAAAA-bbb_ccc" https://api.anthropic.com`,
		"OPENAI_API_KEY=sk-AAAAAAAAAAAAAAAAAAAAAAAA python script.py",
		"git push https://ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA@github.com/x/y",
		"Authorization: token github_pat_11ABCDEFG0_xxxxxxxxxxxxxxxxxxxx",
		`curl -H "Authorization: Bearer xoxb-1234567890-abcdefghij"`,
		"aws configure set aws_access_key_id AKIAIOSFODNN7EXAMPLE",
		"echo eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9LongRestOfTokenHerePastFiftyChars",
		strings.Repeat("a", maxInputLen+512),
		strings.Repeat("xoxb-aaaaaaaaaa!", 600), // slack-growth past the cap
	}
	for _, s := range seeds {
		f.Add(s)
	}

	// A sentinel wrapped in a token shape: the redactor is guaranteed to
	// match it (a bare sentinel is not token-shaped and, by design, would
	// pass through untouched). Placed FIRST in the probe so the maxInputLen
	// truncation boundary can never straddle it — the secret is always
	// fully scanned regardless of how large the fuzzer input grows.
	sentinel := testutil.SentinelSecret(0)
	secret := "sk-ant-" + sentinel + strings.Repeat("A", 24)

	f.Fuzz(func(t *testing.T, s string) {
		// Invariant: never panics.
		out := redact.CommandPreview(s)

		// Invariant: output length stays bounded. Truncation caps scanning
		// input at maxInputLen (+ marker); redaction only grows output by a
		// small, bounded margin (minimal Slack tokens), so 1KiB of slack is
		// ample headroom — matching TestCommandPreview_TruncatesOversizeInput.
		assert.LessOrEqual(t, len(out), maxInputLen+1024,
			"redacted output must stay within the truncation bound")

		// Invariant: idempotent within the size bound.
		if len(out) <= maxInputLen {
			assert.Equal(t, out, redact.CommandPreview(out),
				"CommandPreview must be idempotent within the size bound")
		}

		// Invariant: no verbatim secret leak. Prepend the token-shaped
		// secret, then the fuzzer junk. The fuzzer could itself emit the
		// bare sentinel (which is not token-shaped and legitimately passes
		// through); skip only that pathological case.
		wrapped := secret + " " + s
		got := redact.CommandPreview(wrapped)
		require.NotContains(t, got, secret, "raw secret token must not survive redaction")
		if !strings.Contains(s, sentinel) {
			testutil.AssertSentinelAbsent(t, sentinel, got)
		}
	})
}
