package setup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mrz1836/hush/internal/server"
)

// ClockProbe queries the host's NTP-sync state. Mirrors the
// signature used by the chassis ([server.Deps.ClockSyncProbe]) so the
// guided flow and the chassis can share a single platform-default
// implementation ([server.DefaultClockSyncProbe]).
//
// synced reports whether the host clock is NTP-tracked; drift is the
// signed offset from authoritative time (positive = ahead, negative =
// behind). When err is non-nil, the other two return values are
// unspecified.
type ClockProbe func(ctx context.Context) (synced bool, drift time.Duration, err error)

// ClockSyncCheckConfig configures the [ClockSyncCheck] that the
// guided flow registers in its preflight [Registry]. Every field has
// a documented default so callers can supply only the knobs they
// care about (Plan AC-8 / Task 4.1).
type ClockSyncCheckConfig struct {
	// Probe queries the host clock. When nil the check uses
	// [server.DefaultClockSyncProbe] so the init-side check stays in
	// lock-step with the serve-side one (Plan Task 4.1).
	Probe ClockProbe

	// Required mirrors [config.SecuritySection.RequireNTPSync]. When
	// false the check reports [StatusOK] without running the probe —
	// the operator opted out of the gate.
	Required bool

	// MaxDrift mirrors [config.SecuritySection.MaxClockDrift]. A
	// signed drift whose absolute value exceeds this budget fails the
	// check. When zero the check uses [server.DefaultClockSyncTimeout]
	// as both timeout and (defensively) drift budget.
	MaxDrift time.Duration

	// Timeout bounds the probe call. When zero the check defaults to
	// [server.DefaultClockSyncTimeout].
	Timeout time.Duration

	// AllowSkew downgrades a would-be [StatusFail] to [StatusWarn].
	// The CLI sets this from `--allow-clock-skew` (Plan AC-8 /
	// Task 4.2). hush never auto-sudos to fix the clock — this flag
	// is the only override path.
	AllowSkew bool
}

// ClockSyncCheck is the preflight [Check] that wraps a [ClockProbe]
// and renders an init-side verdict mirroring the serve-side gate in
// [server.checkClockSync]. The check stays decoupled from the
// chassis: it accepts the probe + tuning knobs and never reads any
// chassis state.
//
// Use [NewClockSyncCheck] to construct one with sensible defaults.
type ClockSyncCheck struct {
	cfg ClockSyncCheckConfig
}

// NewClockSyncCheck returns a [ClockSyncCheck] backed by cfg, filling
// in defaults for any zero-valued field. The returned check is safe
// to register under [CheckClockSync] in a [Registry].
func NewClockSyncCheck(cfg ClockSyncCheckConfig) ClockSyncCheck {
	if cfg.Probe == nil {
		cfg.Probe = server.DefaultClockSyncProbe
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = server.DefaultClockSyncTimeout
	}
	if cfg.MaxDrift <= 0 {
		cfg.MaxDrift = server.DefaultClockSyncTimeout
	}
	return ClockSyncCheck{cfg: cfg}
}

// Name returns the locked [CheckClockSync] slot identifier so the
// registry installs the check under the matching slot.
func (c ClockSyncCheck) Name() string { return string(CheckClockSync) }

// Run executes the configured probe under cfg.Timeout and returns a
// [SetupCheckResult]:
//
//   - [StatusOK] when the host is NTP-tracked and drift ≤ MaxDrift.
//   - [StatusWarn] when the probe would have failed but AllowSkew is
//     set; the result's Err still wraps [ErrClockUnsynchronised] so
//     callers can branch on it and emit an audit override.
//   - [StatusFail] otherwise.
//
// The probe is called even when cfg.Required is false, because /hz
// uses the same probe and we want a deterministic answer when the
// preflight runs; only the verdict changes (Ok with a "skip" detail).
func (c ClockSyncCheck) Run(ctx context.Context) SetupCheckResult {
	name := c.Name()
	if !c.cfg.Required {
		return Ok(name, "RequireNTPSync=false; skipped")
	}
	probeCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	synced, drift, err := c.cfg.Probe(probeCtx)
	switch {
	case err != nil:
		return c.verdict(fmt.Errorf("probe failed: %w", ErrClockUnsynchronised), "probe: "+err.Error())
	case !synced:
		return c.verdict(
			fmt.Errorf("host clock not NTP-synchronised: %w", ErrClockUnsynchronised),
			"host clock not NTP-synchronised",
		)
	}
	if abs := absDuration(drift); abs > c.cfg.MaxDrift {
		return c.verdict(
			fmt.Errorf("drift %v exceeds %v: %w", drift, c.cfg.MaxDrift, ErrClockUnsynchronised),
			fmt.Sprintf("drift %v exceeds %v", drift, c.cfg.MaxDrift),
		)
	}
	return Ok(name, "NTP-synchronised")
}

// verdict renders the would-be failure as either Fail or Warn,
// honoring cfg.AllowSkew. When AllowSkew is set the result carries
// the same wrapped sentinel so handlePreflightReport (and the
// serve-side override path) can emit a `clock_skew_override` audit
// event from a single err value.
func (c ClockSyncCheck) verdict(err error, detail string) SetupCheckResult {
	name := c.Name()
	if c.cfg.AllowSkew {
		res := warn(name, detail)
		res.Err = err
		var rh RemedyHinter
		if errors.As(err, &rh) {
			res.RemedyHint = rh.RemedyHint()
		}
		return res
	}
	return fail(name, err, detail)
}

// absDuration returns the absolute value of d.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// ClockSyncRemedy returns the exact, copy-pasteable command that
// resynchronises the system clock on the supplied GOOS. The string
// is the value used in [ErrClockUnsynchronised]'s remedy hint and
// is exposed so [Check] implementations can render it without
// re-deriving the platform mapping.
//
// Unrecognized GOOS values return a portable hint that points the
// user at their distribution's clock-sync documentation rather
// than guessing a command that might not exist.
func ClockSyncRemedy(goos string) string {
	switch goos {
	case "darwin":
		return "run `sudo sntp -sS time.apple.com` to resync the clock, then re-run; hush will never auto-sudo on your behalf"
	case "linux":
		return "run `sudo chronyc makestep` (chrony) or `sudo ntpdate -u pool.ntp.org` (ntpdate), then re-run; hush will never auto-sudo on your behalf"
	case "windows":
		return "open an elevated PowerShell and run `w32tm /resync`, then re-run"
	default:
		return fmt.Sprintf("resync your system clock per your OS docs (%s), then re-run; hush will never auto-sudo on your behalf", goos)
	}
}
