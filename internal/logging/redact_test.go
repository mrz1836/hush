package logging_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/logging"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// --- US1: Sentinel leak test ---

// TestLogger_RedactionSentinel is the load-bearing acceptance test for
// Constitution Principle X. It wraps the sentinel in SecureBytes (which
// implements slog.LogValuer) and asserts that zero bytes of the sentinel
// survive in captured log output regardless of how the value is passed.
func TestLogger_RedactionSentinel(t *testing.T) {
	const sentinel = "SECRET_SHOULD_NEVER_APPEAR_5"

	b := []byte(sentinel)
	sb, err := securebytes.New(b)
	require.NoError(t, err)
	defer func() { _ = sb.Destroy() }()

	var buf bytes.Buffer
	logger := logging.New(logging.Options{Format: logging.FormatJSON, Out: &buf})

	// Log under three distinct attribute keys.
	logger.Info("sentinel-test-1", slog.Any("key1", sb))
	logger.Info("sentinel-test-2", slog.Any("key2", sb))
	logger.Info("sentinel-test-3", slog.Any("key3", sb))
	// Log inside a nested group.
	logger.Info("sentinel-test-group",
		slog.Group("creds", slog.Any("value", sb)),
	)

	output := buf.String()
	require.NotContains(t, output, sentinel,
		"sentinel must never appear in log output (Constitution Principle X)")
	require.Contains(t, output, "[redacted]",
		"output must contain at least one [redacted] marker")
}

// --- US2: Pattern-positive tests (one per credential class) ---

func TestRedactPattern_AnthropicKey(t *testing.T) {
	const sample = "sk-ant-fake0123456789abcdef"
	assertRedacted(t, sample)
}

func TestRedactPattern_OpenAIProjectKey(t *testing.T) {
	const sample = "sk-proj-fake0123456789abcdef"
	assertRedacted(t, sample)
}

func TestRedactPattern_GitHubPAT(t *testing.T) {
	const sample = "ghp_fakeABCDEF0123456789"
	assertRedacted(t, sample)
}

func TestRedactPattern_AWSAccessKey(t *testing.T) {
	// AKIA + exactly 16 uppercase letters/digits.
	const sample = "AKIAFAKEFAKEFAKEFAKE" //nolint:gosec // test fixture — not a real credential
	assertRedacted(t, sample)
}

func TestRedactPattern_OpenAILegacyKey(t *testing.T) {
	// sk- + exactly 48 alphanumeric (no hyphens) — distinct from sk-ant-/sk-proj-.
	const sample = "sk-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEFGHIJKL"
	assertRedacted(t, sample)
}

func TestRedactPattern_GitHubOAuth(t *testing.T) {
	const sample = "gho_abcdefghijklmnopqrstuvwxyz0123456789AB"
	assertRedacted(t, sample)
}

func TestRedactPattern_GitHubUserToServer(t *testing.T) {
	const sample = "ghu_abcdefghijklmnopqrstuvwxyz0123456789AB"
	assertRedacted(t, sample)
}

func TestRedactPattern_GitHubServerToServer(t *testing.T) {
	const sample = "ghs_abcdefghijklmnopqrstuvwxyz0123456789AB"
	assertRedacted(t, sample)
}

func TestRedactPattern_GitHubRefresh(t *testing.T) {
	const sample = "ghr_abcdefghijklmnopqrstuvwxyz0123456789AB"
	assertRedacted(t, sample)
}

func TestRedactPattern_GoogleAPIKey(t *testing.T) {
	// AIza + exactly 35 chars from [0-9A-Za-z_-].
	const sample = "AIza0123456789ABCDEFGHIJKLMNOPQRSTUVWXY"
	assertRedacted(t, sample)
}

func TestRedactPattern_SlackBotToken(t *testing.T) {
	const sample = "xoxb-1234567890-0987654321-aBcDeFgHiJkLmNoPqRsTuVwX"
	assertRedacted(t, sample)
}

func TestRedactPattern_SlackUserToken(t *testing.T) {
	const sample = "xoxp-1234567890-1234567890-1234567890-aBcDeFgHiJkL"
	assertRedacted(t, sample)
}

func TestRedactPattern_SlackAppToken(t *testing.T) {
	const sample = "xapp-1-A12345-67890-aBcDeFgHiJkLmNoPqRsTuVwX"
	assertRedacted(t, sample)
}

func TestRedactPattern_SlackWebhook(t *testing.T) {
	const sample = "https://hooks.slack.com/services/T01234567/B01234567/aBcDeFgHiJkLmNoPqRsTuVwX" //nolint:gosec // test fixture
	assertRedacted(t, sample)
}

func TestRedactPattern_RSAPrivateKey(t *testing.T) {
	const sample = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA1234567890abcdef\nfakebody==\n-----END RSA PRIVATE KEY-----" //nolint:gosec // test fixture
	assertRedacted(t, sample)
}

func TestRedactPattern_ECPrivateKey(t *testing.T) {
	const sample = "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIA1234567890abcdef\n-----END EC PRIVATE KEY-----" //nolint:gosec // test fixture
	assertRedacted(t, sample)
}

func TestRedactPattern_GenericPrivateKey(t *testing.T) {
	const sample = "-----BEGIN PRIVATE KEY-----\nMIIBVAIBADANBgkq\n-----END PRIVATE KEY-----"
	assertRedacted(t, sample)
}

// assertRedacted logs sample as both the record message and a string
// attribute, then asserts the sample never appears in output and
// "[redacted]" appears at least twice.
func assertRedacted(t *testing.T, sample string) {
	t.Helper()
	var buf bytes.Buffer
	logger := logging.New(logging.Options{Format: logging.FormatJSON, Out: &buf})

	// Once as the message text.
	logger.Info(sample)
	// Once as a string attribute value.
	logger.Info("credential in attribute", slog.String("key", sample))

	output := buf.String()
	require.NotContains(t, output, sample,
		"credential sample must not appear in log output")
	count := strings.Count(output, "[redacted]")
	require.GreaterOrEqual(t, count, 2,
		"output must contain at least two [redacted] markers (message + attribute)")
}

// --- US2: RedactString unit tests ---

func TestRedactString_NoMatch(t *testing.T) {
	inputs := []string{
		"",
		"hello world",
		"no credentials here",
		"sk-something-else",              // prefix does not match sk-ant-/sk-proj- and too short for legacy 48-char form
		"ghp",                            // too short — missing underscore + chars
		"AKIA" + strings.Repeat("A", 15), // only 15 chars after AKIA (need 16)
		// 64-char SHA256 hex: must NOT trigger any pattern (common in logs)
		"a3f7d9e2b8c1d4f5e6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9",
		// 40-char SHA-1 hex (commit hash): must NOT trigger
		"e1d4f5a3b2c8e7d6f0a9b1c2d3e4f5a6b7c8d9e0",
		// AIza-prefixed but wrong length (only 20 alphanumeric, need 35)
		"AIza0123456789ABCDEFG",
		// xox-prefixed but wrong tag character (xoxq is not a Slack token class)
		"xoxq-1234567890-0987654321",
		// gh-prefixed but wrong tag character (ghx is not a token class)
		"ghx_abcdefghijklmnopqrstuvwxyz",
		// Slack-like URL but wrong domain
		"https://hooks.example.com/services/T01234567/B01234567/aBcDeFgHiJkL",
		// PEM PUBLIC key (not private) must NOT trigger the private-key pattern
		"-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE\n-----END PUBLIC KEY-----",
		// Bare "sk-" with hyphens ⇒ does not satisfy sk-[48 alnum] (no hyphens allowed in body)
		"sk-not-a-real-key-just-text",
	}
	for _, s := range inputs {
		got := logging.RedactString(s)
		require.Equal(t, s, got, "RedactString must return input unchanged on no match: %q", s)
	}
}

func TestRedactString_MultipleMatchesSameString(t *testing.T) {
	// Two Anthropic keys in one string — both must be replaced.
	s := "first sk-ant-fake0123 second sk-ant-fake9876"
	got := logging.RedactString(s)
	require.NotContains(t, got, "sk-ant-fake0123")
	require.NotContains(t, got, "sk-ant-fake9876")
	require.Equal(t, 2, strings.Count(got, "[redacted]"),
		"two matches must produce two [redacted] tokens")
}

func TestRedactString_AdjacentMatches(t *testing.T) {
	// AWS key immediately followed by another AWS key — adjacent, no separator.
	// AKIA + 16 chars + AKIA + 16 chars.
	key1 := "AKIAFAKEFAKEFAKEFAKE" //nolint:gosec // test fixture — not a real credential
	key2 := "AKIA0987654321ABCDEF"
	s := key1 + key2
	got := logging.RedactString(s)
	require.NotContains(t, got, "AKIA", "no AKIA bytes must survive adjacent match")
	require.Equal(t, 2, strings.Count(got, "[redacted]"),
		"two adjacent AWS keys must each be replaced")
}

func TestRedactString_EmbeddedInSurroundingText(t *testing.T) {
	s := "the key is sk-ant-fake0123456789abcdef and you should redact it"
	got := logging.RedactString(s)
	require.NotContains(t, got, "sk-ant-fake0123456789abcdef")
	require.Contains(t, got, "[redacted]")
	// Surrounding text must be preserved.
	require.Contains(t, got, "the key is ")
	require.Contains(t, got, " and you should redact it")
}

func TestRedactString_LongInput(t *testing.T) {
	// 10 KB string with one GitHub PAT in the middle.
	prefix := strings.Repeat("a", 5*1024)
	suffix := strings.Repeat("b", 5*1024)
	const pat = "ghp_fakeABCDEF0123456789"
	s := prefix + pat + suffix
	got := logging.RedactString(s)
	require.NotContains(t, got, pat, "PAT must not survive in a 10 KB input")
	require.Contains(t, got, "[redacted]")
	// No panic — implicit (if it panicked the test would fail).
}

func TestRedactString_UTF8Boundaries(t *testing.T) {
	// Multi-byte Latin-extended sequences flanking an Anthropic key.
	// Uses non-ASCII Latin chars (café, naïve) to exercise UTF-8 boundary
	// handling without triggering the gosmopolitan Han-script linter rule.
	prefix := "café résumé "
	suffix := " naïve"
	const pat = "sk-ant-fake0123456789abcdef"
	s := prefix + pat + suffix
	got := logging.RedactString(s)
	require.NotContains(t, got, pat)
	require.Contains(t, got, "[redacted]")
	require.Contains(t, got, prefix, "surrounding UTF-8 text must be preserved")
	require.Contains(t, got, suffix, "surrounding UTF-8 text must be preserved")
}

// TestRedactString_EmbeddedFormats covers H4: realistic dev-error log strings
// where credentials live inside JSON, URL-query strings, or punctuation-
// surrounded fragments. The redacted output must (a) contain no byte from
// any credential class and (b) preserve every non-credential token
// (usernames, request IDs, timestamps, etc.) so logs remain debuggable.
func TestRedactString_EmbeddedFormats(t *testing.T) {
	// All fixtures intentionally contain a single credential; the test
	// asserts the credential is gone and the surrounding tokens survive.
	cases := []struct {
		name    string
		input   string
		gone    string   // credential bytes that must NOT appear in output
		keep    []string // surrounding tokens that MUST appear in output
		minRedX int      // expected minimum number of [redacted] markers
	}{
		{
			name:    "json with anthropic key",
			input:   `{"token":"sk-ant-fakeABCDEF0123456789","user":"alice","ts":1234567890}`,
			gone:    "sk-ant-fakeABCDEF0123456789",
			keep:    []string{`"user":"alice"`, `"ts":1234567890`},
			minRedX: 1,
		},
		{
			name:    "url query with openai project key",
			input:   `https://api.example.com/?key=sk-proj-fakeABCDEF0123456789&user=bob`,
			gone:    "sk-proj-fakeABCDEF0123456789",
			keep:    []string{"https://api.example.com/", "user=bob"},
			minRedX: 1,
		},
		{
			name:    "log line with github token + request id",
			input:   `2026-04-29T10:00:00Z error="auth failed" token="ghp_fakeABCDEF0123" request_id="req-abc-123"`,
			gone:    "ghp_fakeABCDEF0123",
			keep:    []string{"2026-04-29T10:00:00Z", `request_id="req-abc-123"`},
			minRedX: 1,
		},
		{ //nolint:gosec // test fixture — embedded fake AWS key
			name:    "two credentials in one line (anthropic + aws)",
			input:   `keys: AnthKey=sk-ant-fakeABCDEFGHIJ0123 AwsKey=AKIAFAKEFAKEFAKEFAKE`,
			gone:    "sk-ant-fakeABCDEFGHIJ0123",
			keep:    []string{"keys:", "AnthKey=", "AwsKey="},
			minRedX: 2,
		},
		{
			name:    "credential surrounded by punctuation",
			input:   `(token=sk-ant-fakeABCDEFGHIJ0123) — see logs`,
			gone:    "sk-ant-fakeABCDEFGHIJ0123",
			keep:    []string{"(token=", ") — see logs"},
			minRedX: 1,
		},
		{
			name:    "google api key in error message",
			input:   `failed to create client: google api key "AIza0123456789ABCDEFGHIJKLMNOPQRSTUVWXY" rejected (403)`,
			gone:    "AIza0123456789ABCDEFGHIJKLMNOPQRSTUVWXY",
			keep:    []string{"failed to create client", "rejected (403)"},
			minRedX: 1,
		},
		{ //nolint:gosec // test fixture — fake Slack webhook URL
			name:    "slack webhook url",
			input:   `webhook=https://hooks.slack.com/services/T01234567/B01234567/aBcDeFgHiJkLmNoPqRsTuVwX url_id=42`,
			gone:    "https://hooks.slack.com/services/T01234567/B01234567/aBcDeFgHiJkLmNoPqRsTuVwX",
			keep:    []string{"webhook=", "url_id=42"},
			minRedX: 1,
		},
		{
			name:    "stack trace line with embedded key",
			input:   `panic: invalid token "ghs_fakeABCDEFGHIJ0123" at github.com/example/pkg.Auth (auth.go:123)`,
			gone:    "ghs_fakeABCDEFGHIJ0123",
			keep:    []string{"panic:", "github.com/example/pkg.Auth", "auth.go:123"},
			minRedX: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := logging.RedactString(tc.input)
			require.NotContains(t, got, tc.gone,
				"credential %q must not survive", tc.gone)
			for _, k := range tc.keep {
				require.Contains(t, got, k,
					"surrounding token %q must be preserved", k)
			}
			require.GreaterOrEqual(t, strings.Count(got, "[redacted]"), tc.minRedX,
				"expected at least %d [redacted] markers", tc.minRedX)
		})
	}
}

func TestRedactString_Idempotent(t *testing.T) {
	cases := []string{
		"sk-ant-fake0123456789abcdef",
		"sk-proj-fake0123456789abcdef",
		"ghp_fakeABCDEF0123456789",
		"AKIAFAKEFAKEFAKEFAKE",
		"no credentials here",
		"sk-ant-a sk-proj-b ghp_c AKIAFAKEFAKEFAKEFAKE",
		"",
	}
	for _, s := range cases {
		once := logging.RedactString(s)
		twice := logging.RedactString(once)
		require.Equal(t, once, twice,
			"RedactString must be idempotent for input: %q", s)
	}
}
