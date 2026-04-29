package logging

import (
	"regexp"
	"sync"
)

// redactPatternsOnce ensures patterns are compiled exactly once.
var redactPatternsOnce sync.Once //nolint:gochecknoglobals // one-shot sentinel; immutable after first use

// rawPatterns holds the source strings for every credential class
// scanned by docs/SECURITY.md §1.1's commodity-malware threat model.
//
// Ordering: most-specific prefixes first so that more-targeted matches
// (e.g. sk-ant-) replace before more-general ones (e.g. sk-[48 chars]).
// Replacement is sequential; once a substring is rewritten to "[redacted]"
// later patterns cannot re-match it.
var rawPatterns = []string{ //nolint:gochecknoglobals // compile-time constant list; never mutated
	// Anthropic / OpenAI prefix-tagged keys
	`sk-ant-[A-Za-z0-9_\-]+`,
	`sk-proj-[A-Za-z0-9_\-]+`,
	// OpenAI service account / generic legacy keys (exactly 48 alnum after sk-)
	`sk-[A-Za-z0-9]{48}`,
	// GitHub tokens (PAT, OAuth, user-to-server, server-to-server, refresh)
	`gh[oupsr]_[A-Za-z0-9]+`,
	// AWS access key id (uppercase alnum, fixed length)
	`AKIA[0-9A-Z]{16}`,
	// Google API / Google AI keys (39 chars total, 35 after AIza)
	`AIza[0-9A-Za-z_\-]{35}`,
	// Slack tokens (bot, user, app, refresh, cookie, legacy)
	`xox[abcprs]-[0-9A-Za-z\-]+`,
	`xapp-[0-9]+-[A-Z0-9]+-[0-9A-Za-z\-]+`,
	// Slack incoming-webhook URLs (path component carries the secret)
	`https://hooks\.slack\.com/services/[A-Z0-9]+/[A-Z0-9]+/[A-Za-z0-9]+`,
	// PEM-encoded private key blocks (RSA / EC / OpenSSH / generic)
	`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`,
}

// compileRedactPatterns populates RedactPatterns from rawPatterns.
// Called exactly once via redactPatternsOnce.Do.
func compileRedactPatterns() {
	compiled := make([]*regexp.Regexp, len(rawPatterns))
	for i, p := range rawPatterns {
		compiled[i] = regexp.MustCompile(p)
	}
	RedactPatterns = compiled
}
