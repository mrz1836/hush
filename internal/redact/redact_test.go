package redact_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mrz1836/hush/internal/redact"
)

func TestCommandPreview_Empty(t *testing.T) {
	assert.Empty(t, redact.CommandPreview(""))
}

func TestCommandPreview_PassesPlainStringThrough(t *testing.T) {
	in := "git push origin master"
	assert.Equal(t, in, redact.CommandPreview(in))
}

func TestCommandPreview_AnthropicKey(t *testing.T) {
	in := `curl -H "x-api-key: sk-ant-api03-AAAAAAAAAAAAAAAAAAAA-bbb_ccc" https://api.anthropic.com`
	got := redact.CommandPreview(in)
	assert.NotContains(t, got, "sk-ant-api03")
	assert.Contains(t, got, "[redacted:anthropic]")
}

func TestCommandPreview_OpenAIKey(t *testing.T) {
	in := `OPENAI_API_KEY=sk-AAAAAAAAAAAAAAAAAAAAAAAA python script.py`
	got := redact.CommandPreview(in)
	assert.NotContains(t, got, "sk-AAAA")
	assert.Contains(t, got, "[redacted:openai]")
}

func TestCommandPreview_GithubPATClassic(t *testing.T) {
	in := `git push https://ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA@github.com/x/y`
	got := redact.CommandPreview(in)
	assert.NotContains(t, got, "ghp_AAAA")
	assert.Contains(t, got, "[redacted:github]")
}

func TestCommandPreview_GithubFineGrainedPAT(t *testing.T) {
	in := `Authorization: token github_pat_11ABCDEFG0_xxxxxxxxxxxxxxxxxxxx`
	got := redact.CommandPreview(in)
	assert.NotContains(t, got, "github_pat_11")
	assert.Contains(t, got, "[redacted:github-fine]")
}

func TestCommandPreview_SlackToken(t *testing.T) {
	in := `curl -X POST -H "Authorization: Bearer xoxb-1234567890-abcdefghij"`
	got := redact.CommandPreview(in)
	assert.NotContains(t, got, "xoxb-1234567890")
	assert.Contains(t, got, "[redacted:slack]")
}

func TestCommandPreview_AwsAccessKey(t *testing.T) {
	in := `aws configure set aws_access_key_id AKIAIOSFODNN7EXAMPLE`
	got := redact.CommandPreview(in)
	assert.NotContains(t, got, "AKIAIOSFODNN7EXAMPLE")
	assert.Contains(t, got, "[redacted:aws-akid]")
}

func TestCommandPreview_HighEntropyBase64Catchall(t *testing.T) {
	// Random-looking 50-char base64 → high-entropy match.
	in := `echo eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9LongRestOfTokenHerePastFiftyChars`
	got := redact.CommandPreview(in)
	assert.Contains(t, got, "[redacted:")
}

func TestCommandPreview_Idempotent(t *testing.T) {
	in := `sk-ant-api03-AAAAAAAAAAAAAAAAAAAA-bbb_ccc and ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA`
	once := redact.CommandPreview(in)
	twice := redact.CommandPreview(once)
	assert.Equal(t, once, twice, "Redaction must be idempotent")
}

func TestCommandPreview_PreservesShortIdentifiers(t *testing.T) {
	// Short identifiers (function names, file paths) must NOT be matched.
	cases := []string{
		"func ProcessThing()",
		"src/main.go",
		"my_variable_name",
		"foo-bar-baz",
	}
	for _, in := range cases {
		got := redact.CommandPreview(in)
		assert.Equal(t, in, got, "short identifier %q should not match", in)
	}
}

func TestCommandPreview_TruncatesOversizeInput(t *testing.T) {
	in := strings.Repeat("a", 16*1024)
	got := redact.CommandPreview(in)
	assert.LessOrEqual(t, len(got), 9*1024, "output should be truncated to ≤ ~maxInputLen")
}

// Regex-DoS resistance: a long all-base64 input must still complete
// well under a second. This is a smoke test, not a strict benchmark.
func TestCommandPreview_NoCatastrophicBacktrackOnLongInput(t *testing.T) {
	in := strings.Repeat("A", 8000)
	// Just ensure the call returns; if a pathological backtrack
	// existed, this would hang.
	_ = redact.CommandPreview(in)
}
