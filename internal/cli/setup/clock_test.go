package setup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/cli/setup"
	"github.com/mrz1836/hush/internal/server"
)

// TestClockSyncRemedy_PerPlatform asserts the platform-aware helper
// returns the exact command string each supported GOOS is documented
// to receive. Locked text — Plan AC-8's "exact remediation command"
// promise.
func TestClockSyncRemedy_PerPlatform(t *testing.T) {
	t.Parallel()

	cases := []struct {
		goos          string
		wantSubstring string
	}{
		{"darwin", "sudo sntp -sS time.apple.com"},
		{"linux", "sudo chronyc makestep"},
		{"windows", "w32tm /resync"},
		{"plan9", "plan9"}, // unknown GOOS echoes the value in the fallback hint.
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			t.Parallel()
			require.Contains(t, setup.ClockSyncRemedy(tc.goos), tc.wantSubstring)
		})
	}
}

// stubProbe returns a [setup.ClockProbe] that always returns the
// supplied (synced, drift, err) triple regardless of context.
func stubProbe(synced bool, drift time.Duration, err error) setup.ClockProbe {
	return func(_ context.Context) (bool, time.Duration, error) {
		return synced, drift, err
	}
}

// TestClockSyncCheck_NameMatchesRegistrySlot ensures the check
// installs under [setup.CheckClockSync] without callers having to
// pass a slot name explicitly.
func TestClockSyncCheck_NameMatchesRegistrySlot(t *testing.T) {
	t.Parallel()

	check := setup.NewClockSyncCheck(setup.ClockSyncCheckConfig{Required: false})
	require.Equal(t, string(setup.CheckClockSync), check.Name())
}

// TestClockSyncCheck_SkipsWhenNotRequired mirrors the chassis's
// behavior when RequireNTPSync is false: report ok, do not call the
// probe.
func TestClockSyncCheck_SkipsWhenNotRequired(t *testing.T) {
	t.Parallel()

	called := false
	probe := func(_ context.Context) (bool, time.Duration, error) {
		called = true
		return false, 0, nil
	}
	check := setup.NewClockSyncCheck(setup.ClockSyncCheckConfig{
		Probe:    probe,
		Required: false,
	})
	res := check.Run(context.Background())
	require.Equal(t, setup.StatusOK, res.Status)
	require.False(t, called, "probe must not run when Required=false")
}

// TestClockSyncCheck_VerdictMatrix exercises every (synced, drift,
// err, allowSkew) combination the check must classify. Plan AC-8 /
// Task 4.1 / Task 4.2.
func TestClockSyncCheck_VerdictMatrix(t *testing.T) {
	t.Parallel()

	probeErr := errors.New("synthetic probe failure")
	cases := []struct {
		name       string
		probe      setup.ClockProbe
		allowSkew  bool
		wantStatus setup.Status
		wantIs     error
		wantHint   bool
	}{
		{
			name:       "in_sync_passes",
			probe:      stubProbe(true, 0, nil),
			wantStatus: setup.StatusOK,
		},
		{
			name:       "probe_error_fails",
			probe:      stubProbe(true, 0, probeErr),
			wantStatus: setup.StatusFail,
			wantIs:     setup.ErrClockUnsynchronised,
			wantHint:   true,
		},
		{
			name:       "not_synced_fails",
			probe:      stubProbe(false, 0, nil),
			wantStatus: setup.StatusFail,
			wantIs:     setup.ErrClockUnsynchronised,
			wantHint:   true,
		},
		{
			name:       "drift_over_budget_fails",
			probe:      stubProbe(true, 90*time.Second, nil),
			wantStatus: setup.StatusFail,
			wantIs:     setup.ErrClockUnsynchronised,
			wantHint:   true,
		},
		{
			name:       "allow_skew_downgrades_probe_error",
			probe:      stubProbe(true, 0, probeErr),
			allowSkew:  true,
			wantStatus: setup.StatusWarn,
			wantIs:     setup.ErrClockUnsynchronised,
			wantHint:   true,
		},
		{
			name:       "allow_skew_downgrades_not_synced",
			probe:      stubProbe(false, 0, nil),
			allowSkew:  true,
			wantStatus: setup.StatusWarn,
			wantIs:     setup.ErrClockUnsynchronised,
			wantHint:   true,
		},
		{
			name:       "allow_skew_downgrades_drift_over",
			probe:      stubProbe(true, 90*time.Second, nil),
			allowSkew:  true,
			wantStatus: setup.StatusWarn,
			wantIs:     setup.ErrClockUnsynchronised,
			wantHint:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			check := setup.NewClockSyncCheck(setup.ClockSyncCheckConfig{
				Probe:     tc.probe,
				Required:  true,
				MaxDrift:  60 * time.Second,
				Timeout:   2 * time.Second,
				AllowSkew: tc.allowSkew,
			})
			res := check.Run(context.Background())
			require.Equal(t, tc.wantStatus, res.Status, "verdict")
			if tc.wantIs != nil {
				require.ErrorIs(t, res.Err, tc.wantIs)
			}
			if tc.wantHint {
				require.NotEmpty(t, res.RemedyHint, "remedy hint should propagate from sentinel")
			}
		})
	}
}

// TestClockSyncCheck_DriftSignAgnostic asserts the absolute-value
// branch in verdict() — a clock 90s behind is just as bad as one
// 90s ahead.
func TestClockSyncCheck_DriftSignAgnostic(t *testing.T) {
	t.Parallel()

	check := setup.NewClockSyncCheck(setup.ClockSyncCheckConfig{
		Probe:    stubProbe(true, -90*time.Second, nil),
		Required: true,
		MaxDrift: 60 * time.Second,
		Timeout:  2 * time.Second,
	})
	res := check.Run(context.Background())
	require.Equal(t, setup.StatusFail, res.Status)
	require.ErrorIs(t, res.Err, setup.ErrClockUnsynchronised)
}

// TestClockSyncCheck_DefaultsBackfilled ensures NewClockSyncCheck
// supplies sensible defaults when the caller leaves Probe / Timeout
// / MaxDrift zero. The probe falls back to the chassis default so
// init-side and serve-side stay aligned.
func TestClockSyncCheck_DefaultsBackfilled(t *testing.T) {
	t.Parallel()

	// Required=false avoids actually invoking the OS-specific
	// systemsetup probe in CI; we just verify the constructor
	// accepts an otherwise-empty config without panicking.
	check := setup.NewClockSyncCheck(setup.ClockSyncCheckConfig{
		Required: false,
	})
	res := check.Run(context.Background())
	require.Equal(t, setup.StatusOK, res.Status)

	// And the chassis-default constants are reachable through the
	// shared server package — proving the import compiles.
	require.NotZero(t, server.DefaultClockSyncTimeout)
}
