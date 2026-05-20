package server

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// execNTPSynchronised runs `timedatectl show --property=NTPSynchronized
// --value` and returns the trimmed combined output. timedatectl ships with
// systemd — the init system on every hush-supported Linux host — and the
// read-only `show` query needs no privileges.
//
//nolint:gochecknoglobals // OS bridge; test-hookable for clock_sync coverage
var execNTPSynchronised = func(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "timedatectl", "show", "--property=NTPSynchronized", "--value")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// DefaultClockSyncProbe is the platform-default probe used by the chassis on
// linux. It reads the kernel's NTP-sync flag via timedatectl.
//
// A synced host reports drift 0: once the kernel marks the clock disciplined
// the residual offset sits below any budget hush enforces, and timedatectl
// exposes no per-probe offset that is portable across chrony, ntpd, and
// systemd-timesyncd. The boolean NTPSynchronized flag is the authoritative
// sync verdict the drift gate exists to approximate.
func DefaultClockSyncProbe(ctx context.Context) (bool, time.Duration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, DefaultClockSyncTimeout)
	defer cancel()

	out, err := execNTPSynchronised(probeCtx)
	trimmed := strings.TrimSpace(out)
	if err != nil {
		if trimmed == "" {
			return false, 0, fmt.Errorf("server: clock_sync: timedatectl: %w", err)
		}
		return false, 0, fmt.Errorf("server: clock_sync: timedatectl: %w: %s", err, trimmed)
	}

	switch trimmed {
	case "yes":
		return true, 0, nil
	case "no":
		return false, 0, nil
	default:
		return false, 0, fmt.Errorf("%w: timedatectl %q", ErrClockProbeUnexpectedOutput, trimmed)
	}
}
