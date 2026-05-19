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
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		probeCtx, cancel := context.WithTimeout(ctx, DefaultClockSyncTimeout)
		out, err := execClockOffset(probeCtx)
		cancel()
		trimmed := strings.TrimSpace(out)
		if err != nil {
			if trimmed == "" {
				lastErr = fmt.Errorf("server: clock_sync: sntp: %w", err)
			} else if drift, parseErr := parseSNTPDrift(trimmed); parseErr == nil {
				return true, drift, nil
			} else {
				lastErr = fmt.Errorf("server: clock_sync: sntp: %w: %s", err, trimmed)
			}
			continue
		}
		drift, parseErr := parseSNTPDrift(trimmed)
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		return true, drift, nil
	}
	return false, 0, lastErr
}

func parseSNTPDrift(trimmed string) (time.Duration, error) {
	m := sntpOffsetRE.FindStringSubmatch(trimmed)
	if len(m) < 2 {
		return 0, fmt.Errorf("%w: sntp %q", ErrClockProbeUnexpectedOutput, trimmed)
	}
	seconds, parseErr := strconv.ParseFloat(m[1], 64)
	if parseErr != nil {
		return 0, fmt.Errorf("%w: sntp %q", ErrClockProbeUnexpectedOutput, trimmed)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
