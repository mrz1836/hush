package logging

import "regexp"

// RedactPatterns is the compiled set of credential-class regexes used by
// RedactString. It is nil until the first call to RedactString or New.
// Consumers MUST treat this slice as read-only; mutation is undefined behavior.
//
//nolint:gochecknoglobals // exported locked API (SDD-05); read-only after sync.Once initialisation
var RedactPatterns []*regexp.Regexp

// ensurePatterns lazily compiles RedactPatterns on first use.
func ensurePatterns() {
	redactPatternsOnce.Do(compileRedactPatterns)
}

// RedactString scans s against every pattern in RedactPatterns and replaces
// each match with "[redacted]". Returns s byte-identical when no pattern
// matches. Safe for concurrent use.
func RedactString(s string) string {
	ensurePatterns()
	for _, re := range RedactPatterns {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	return s
}
