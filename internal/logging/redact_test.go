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
		"sk-something-else",              // prefix does not match sk-ant- or sk-proj-
		"ghp",                            // too short — missing underscore + chars
		"AKIA" + strings.Repeat("A", 15), // only 15 chars after AKIA (need 16)
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
