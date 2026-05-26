//go:build !darwin

package cli

import "log/slog"

// logSupervisorRuntimeLimitations is a no-op on platforms other than darwin.
// On linux, Pdeathsig is kernel-enforced (see internal/supervise/child_linux.go)
// so there is no orphan-child gap to warn operators about.
func logSupervisorRuntimeLimitations(_ *slog.Logger) {}
