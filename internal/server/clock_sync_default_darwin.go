package server

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sntpOffsetRE = regexp.MustCompile(`(?m)^\s*([+-]?[0-9]+(?:\.[0-9]+)?)\s*(?:\+/-|$)`)

// execClockOffset runs `sntp -t 5 time.apple.com` and returns the trimmed
// combined output. `sntp` does not require administrator privileges for a
// read-only probe; `sudo sntp -sS ...` is only the explicit remediation command.
//
//nolint:gochecknoglobals // OS bridge; test-hookable for clock_sync coverage
var execClockOffset = func(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "sntp", "-t", "5", "time.apple.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// DefaultClockSyncProbe is the platform-default probe used by the chassis on
// darwin. It performs a read-only NTP offset probe with `sntp -t 5
// time.apple.com`, which works without administrator privileges. Drift is parsed
// from sntp's leading signed seconds value, e.g. `+0.029191 +/- ...`.
func DefaultClockSyncProbe(ctx context.Context) (bool, time.Duration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, DefaultClockSyncTimeout)
	defer cancel()

	out, err := execClockOffset(probeCtx)
	trimmed := strings.TrimSpace(out)
	if err != nil {
		if trimmed == "" {
			return false, 0, fmt.Errorf("server: clock_sync: sntp: %w", err)
		}
		return false, 0, fmt.Errorf("server: clock_sync: sntp: %w: %s", err, trimmed)
	}

	m := sntpOffsetRE.FindStringSubmatch(trimmed)
	if len(m) < 2 {
		return false, 0, fmt.Errorf("%w: sntp %q", ErrClockProbeUnexpectedOutput, trimmed)
	}
	seconds, parseErr := strconv.ParseFloat(m[1], 64)
	if parseErr != nil {
		return false, 0, fmt.Errorf("%w: sntp %q", ErrClockProbeUnexpectedOutput, trimmed)
	}
	return true, time.Duration(seconds * float64(time.Second)), nil
}
