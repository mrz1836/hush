package logging

import (
	"regexp"
	"sync"
)

// redactPatternsOnce ensures patterns are compiled exactly once.
var redactPatternsOnce sync.Once //nolint:gochecknoglobals // one-shot sentinel; immutable after first use

// rawPatterns holds the source strings for the four credential classes
// enumerated in docs/SECURITY.md §1.1.
var rawPatterns = []string{ //nolint:gochecknoglobals // compile-time constant list; never mutated
	`sk-ant-[A-Za-z0-9_\-]+`,
	`sk-proj-[A-Za-z0-9_\-]+`,
	`ghp_[A-Za-z0-9]+`,
	`AKIA[0-9A-Z]{16}`,
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
