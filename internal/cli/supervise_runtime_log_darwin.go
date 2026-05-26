//go:build darwin

package cli

import "log/slog"

// darwinDeathWatchLimitationMsg is the operator-visible WARN string the
// supervisor emits once at startup on darwin. The wording is exercised by
// supervise_runtime_log_darwin_test.go (substring assertion on
// "darwin death-watch") so a future rephrase still trips the test.
const darwinDeathWatchLimitationMsg = "supervise: darwin death-watch cannot replicate linux Pdeathsig; " +
	"if this supervisor exits via SIGKILL, OOM-kill, segfault, or unrecovered panic, " +
	"the supervised child will be orphaned with its scope env still resident in process memory " +
	"until it exits naturally (see internal/supervise/child_darwin.go for details)"

// logSupervisorRuntimeLimitations emits one WARN log per supervisor process
// start on darwin so operators reading logs see the platform-specific
// orphan-child gap. No-op on other platforms (see the build-tagged sibling).
func logSupervisorRuntimeLimitations(logger *slog.Logger) {
	if logger == nil {
		return
	}
	logger.Warn(darwinDeathWatchLimitationMsg)
}
