package server

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// execClockOffset runs `sntp -t <timeout> <provider>` and returns the trimmed
// combined output. `sntp` does not require administrator privileges for a
// read-only probe; `sudo sntp -sS ...` is only the explicit remediation command.
//
//nolint:gochecknoglobals // OS bridge; test-hookable for clock_sync coverage
var execClockOffset = func(ctx context.Context, provider string, timeout time.Duration) (string, error) {
	cmd := exec.CommandContext(ctx, "sntp", "-t", fmt.Sprintf("%.0f", timeout.Seconds()), provider)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// DefaultClockSyncProbe is the platform-default probe used by the chassis on
// darwin. It performs a read-only NTP offset probe against an ordered provider
// list, which works without administrator privileges. Drift is parsed from
// sntp's leading signed seconds value, e.g. `+0.029191 +/- ...`.
func DefaultClockSyncProbe(ctx context.Context) (bool, time.Duration, error) {
	errs := make([]error, 0, len(DefaultClockSyncProviders))
	for _, provider := range DefaultClockSyncProviders {
		probeCtx, cancel := context.WithTimeout(ctx, DefaultClockSyncProviderTimeout)
		out, err := execClockOffset(probeCtx, provider, DefaultClockSyncProviderTimeout)
		cancel()
		trimmed := strings.TrimSpace(out)
		if err != nil {
			if trimmed == "" {
				errs = append(errs, fmt.Errorf("%s: %w", provider, err))
			} else if drift, parseErr := parseSNTPDrift(trimmed); parseErr == nil {
				return true, drift, nil
			} else {
				errs = append(errs, fmt.Errorf("%s: %w: %s", provider, err, trimmed))
			}
			continue
		}
		drift, parseErr := parseSNTPDrift(trimmed)
		if parseErr != nil {
			return false, 0, parseErr
		}
		return true, drift, nil
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no clock-sync providers configured"))
	}
	return false, 0, fmt.Errorf("server: clock_sync: sntp: %w: %w", ErrClockProbeUnavailable, errors.Join(errs...))
}
