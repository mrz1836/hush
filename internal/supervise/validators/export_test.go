package validators

import "log/slog"

// SetLoggerForTest is a test-build-only seam: it lets tests inject a
// *slog.Logger into a concrete provider Validator so they can capture
// the FR-020 records without racing slog.Default(). The panic on the
// default branch guards an invariant that can only fire under test
// development (someone adds a sixth provider and forgets to extend the
// type switch).
func SetLoggerForTest(v Validator, logger *slog.Logger) {
	switch concrete := v.(type) {
	case *anthropicValidator:
		concrete.logger = logger
	case *anthropicOAuthValidator:
		concrete.logger = logger
	case *openaiValidator:
		concrete.logger = logger
	case *googleAIValidator:
		concrete.logger = logger
	case *githubValidator:
		concrete.logger = logger
	default:
		panic("validators: SetLoggerForTest called with unknown Validator type")
	}
}
