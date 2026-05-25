// Package redact provides best-effort scrubbing of likely-secret
// patterns from operator-visible strings such as the /claim
// command_preview field.
//
// Redaction is NOT a confidentiality guarantee. It exists to reduce
// the chance that an agent accidentally surfaces a fresh credential
// in the Discord approval prompt or the signed audit log. The
// approver is already a trusted operator who holds every secret in
// the vault; the goal here is to keep ephemeral material (e.g. a
// token the agent is in the middle of rotating) out of long-lived
// log artifacts.
//
// Patterns are matched conservatively — preferring false negatives
// (a secret slips through) over false positives (a non-secret looks
// suspicious and gets mangled). Callers MUST not rely on Redact to
// pass through arbitrary user input safely.
package redact

import (
	"regexp"
	"strings"
)

// maxInputLen pre-caps regex input to prevent pathological
// runtime against a malicious or buggy caller.
const maxInputLen = 8 * 1024

// patterns are matched in order; first hit wins per byte range. Each
// entry's label appears in the redacted output as `[redacted:label]`.
//
//nolint:gochecknoglobals // pattern catalog; compiled once at init.
var patterns = []struct {
	label string
	re    *regexp.Regexp
}{
	// Anthropic — sk-ant-* prefix.
	{"anthropic", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
	// OpenAI / generic `sk-` prefix.
	{"openai", regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`)},
	// GitHub PATs (classic + fine-grained + app + OAuth).
	{"github", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	// GitHub fine-grained PAT.
	{"github-fine", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	// Slack legacy.
	{"slack", regexp.MustCompile(`xox[abopr]-[A-Za-z0-9-]{10,}`)},
	// AWS access key ID.
	{"aws-akid", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	// Generic high-entropy base64 — 40+ chars of [A-Za-z0-9+/=_-].
	// Last in the chain so it only fires when nothing more specific
	// matched. Long enough to avoid colliding with most identifiers.
	{"high-entropy", regexp.MustCompile(`[A-Za-z0-9+/=_-]{40,}`)},
}

// CommandPreview returns a copy of s with any recognized secret
// patterns replaced by `[redacted:<label>]`. Inputs longer than
// maxInputLen are pre-truncated before scanning to bound runtime.
//
// CommandPreview is idempotent: applying it twice yields the same
// result as applying it once.
func CommandPreview(s string) string {
	if s == "" {
		return s
	}
	if len(s) > maxInputLen {
		s = s[:maxInputLen] + "…[truncated]"
	}
	out := s
	for _, p := range patterns {
		out = p.re.ReplaceAllString(out, "[redacted:"+p.label+"]")
	}
	// Re-pass for high-entropy in case a specific pattern's
	// replacement created another match (unlikely with current
	// patterns but cheap defensively).
	if strings.ContainsAny(out, "+/=_-") {
		out = patterns[len(patterns)-1].re.ReplaceAllString(out, "[redacted:high-entropy]")
	}
	return out
}
