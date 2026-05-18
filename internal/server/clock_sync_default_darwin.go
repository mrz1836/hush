package server

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// execNetworkTime runs `systemsetup -getusingnetworktime` and returns the
// trimmed stdout. Replaceable in tests.
//
//nolint:gochecknoglobals // OS bridge; test-hookable for clock_sync coverage
var execNetworkTime = func(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "systemsetup", "-getusingnetworktime")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// DefaultClockSyncProbe is the platform-default probe used by the chassis on
// darwin. Parses the yes/no answer from `systemsetup -getusingnetworktime`.
// Drift is reported as zero — the binary's accuracy is sub-second-class on
// modern macOS, well inside [config.SecuritySection.MaxClockDrift].
func DefaultClockSyncProbe(ctx context.Context) (bool, time.Duration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, DefaultClockSyncTimeout)
	defer cancel()

	out, err := execNetworkTime(probeCtx)
	if err != nil {
		return false, 0, fmt.Errorf("server: clock_sync: systemsetup: %w", err)
	}
	trimmed := strings.TrimSpace(out)
	switch {
	case strings.Contains(trimmed, "Network Time: On"):
		return true, 0, nil
	case strings.Contains(trimmed, "Network Time: Off"):
		return false, 0, nil
	default:
		return false, 0, fmt.Errorf("%w: systemsetup %q", ErrClockProbeUnexpectedOutput, trimmed)
	}
}
