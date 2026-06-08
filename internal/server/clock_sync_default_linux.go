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
// read-only probe.
//
//nolint:gochecknoglobals // OS bridge; test-hookable for clock_sync coverage
var execClockOffset = func(ctx context.Context, provider string, timeout time.Duration) (string, error) {
	// #nosec G204 -- provider comes from the fixed default provider list or a
	// test override; the argv vector is fixed and never shell-interpreted.
	cmd := exec.CommandContext(ctx, "sntp", "-t", fmt.Sprintf("%.0f", timeout.Seconds()), provider)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

type clockProviderProbeResult struct {
	drift     time.Duration
	ok        bool
	retryable bool
	err       error
}

// DefaultClockSyncProbe is the platform-default probe used by the chassis on
// linux. It performs a read-only NTP offset probe against an ordered provider
// list. Drift is parsed from sntp's leading signed seconds value, e.g.
// `+0.029191 +/- ...`.
func DefaultClockSyncProbe(ctx context.Context) (bool, time.Duration, error) {
	errs := make([]error, 0, len(DefaultClockSyncProviders))
	for _, provider := range DefaultClockSyncProviders {
		result := probeClockSyncProvider(ctx, provider)
		if result.ok {
			return true, result.drift, nil
		}
		if !result.retryable {
			return false, 0, result.err
		}
		errs = append(errs, result.err)
	}
	return false, 0, unavailableClockProbeError(errs)
}

func probeClockSyncProvider(ctx context.Context, provider string) clockProviderProbeResult {
	probeCtx, cancel := context.WithTimeout(ctx, DefaultClockSyncProviderTimeout)
	defer cancel()

	out, err := execClockOffset(probeCtx, provider, DefaultClockSyncProviderTimeout)
	trimmed := strings.TrimSpace(out)
	if err != nil {
		return clockProviderExecError(provider, trimmed, err)
	}

	drift, parseErr := parseSNTPDrift(trimmed)
	if parseErr != nil {
		return clockProviderProbeResult{err: parseErr}
	}
	return clockProviderProbeResult{drift: drift, ok: true}
}

func clockProviderExecError(provider, trimmed string, err error) clockProviderProbeResult {
	if trimmed == "" {
		return clockProviderProbeResult{retryable: true, err: fmt.Errorf("%s: %w", provider, err)}
	}
	if drift, parseErr := parseSNTPDrift(trimmed); parseErr == nil {
		return clockProviderProbeResult{drift: drift, ok: true}
	}
	return clockProviderProbeResult{retryable: true, err: fmt.Errorf("%s: %w: %s", provider, err, trimmed)}
}

func unavailableClockProbeError(errs []error) error {
	if len(errs) == 0 {
		errs = append(errs, ErrClockProbeNoProviders)
	}
	return fmt.Errorf("server: clock_sync: sntp: %w: %w", ErrClockProbeUnavailable, errors.Join(errs...))
}
