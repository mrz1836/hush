package redact_test

import (
	"testing"

	"github.com/mrz1836/hush/internal/redact"
)

// benchInput is a representative long, mixed command line that exercises
// the full pattern chain plus the high-entropy re-pass: several distinct
// token shapes interleaved with ordinary shell text so every regex in the
// catalog is consulted and at least one replacement triggers the re-scan.
const benchInput = `env OPENAI_API_KEY=sk-AAAAAAAAAAAAAAAAAAAAAAAA ` +
	`ANTHROPIC_API_KEY=sk-ant-api03-AAAAAAAAAAAAAAAAAAAA-bbb_ccc ` +
	`GITHUB_TOKEN=ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA ` +
	`FINE_PAT=github_pat_11ABCDEFG0_xxxxxxxxxxxxxxxxxxxx ` +
	`SLACK_TOKEN=xoxb-1234567890-abcdefghij ` +
	`AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE ` +
	`BLOB=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9LongRestOfTokenHerePastFiftyChars ` +
	`bash -c 'deploy --region us-east-1 --profile prod && kubectl rollout status deploy/api'`

// BenchmarkCommandPreview pins the redaction hot path. CommandPreview runs
// on every /claim before the preview reaches the Discord prompt and the
// signed audit log, so a regression here is felt on the approval path.
func BenchmarkCommandPreview(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = redact.CommandPreview(benchInput)
	}
}
