package setup_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/cli/setup"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/server"
)

// TestSetupErrors_MessagesAndRemedies asserts every taxonomy entry
// renders a locked message and a non-empty, single-line remedy
// hint. The list mirrors AC-3: any new sentinel must be added here
// before it ships.
func TestSetupErrors_MessagesAndRemedies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		err            error
		wantMsg        string
		wantHintSubstr string
	}{
		{
			name:           "ErrTokenAbsent",
			err:            setup.ErrTokenAbsent,
			wantMsg:        "hush/setup: discord bot token absent",
			wantHintSubstr: "hush init server",
		},
		{
			name:           "ErrTokenDenied",
			err:            setup.ErrTokenDenied,
			wantMsg:        "hush/setup: discord bot token denied by keychain",
			wantHintSubstr: "set-generic-password-partition-list",
		},
		{
			name:           "ErrTokenBad",
			err:            setup.ErrTokenBad,
			wantMsg:        "hush/setup: discord bot token rejected by validation",
			wantHintSubstr: "Discord developer portal",
		},
		{
			name:           "ErrBindConflict",
			err:            setup.ErrBindConflict,
			wantMsg:        "hush/setup: listen address conflict",
			wantHintSubstr: "tailscale ip -4",
		},
		{
			name:           "ErrStateStale",
			err:            setup.ErrStateStale,
			wantMsg:        "hush/setup: existing state is incomplete",
			wantHintSubstr: "reuse / repair / archive",
		},
		{
			name:           "ErrArtifactCollision",
			err:            setup.ErrArtifactCollision,
			wantMsg:        "hush/setup: existing artifact collides with new bootstrap",
			wantHintSubstr: "archive",
		},
		{
			name:           "ErrClockUnsynchronised",
			err:            setup.ErrClockUnsynchronised,
			wantMsg:        server.ErrClockUnsynchronised.Error(),
			wantHintSubstr: "re-run",
		},
		{
			name:           "ErrKeychainPermissionDenied",
			err:            setup.ErrKeychainPermissionDenied,
			wantMsg:        keychain.ErrKeychainPermissionDenied.Error(),
			wantHintSubstr: "Keychain Access",
		},
		{
			name:           "ErrCheckIncomplete",
			err:            setup.ErrCheckIncomplete,
			wantMsg:        "hush/setup: preflight check returned no status",
			wantHintSubstr: "hush bug",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.wantMsg, tc.err.Error(), "locked sentinel message")

			var hinter setup.RemedyHinter
			require.True(t, errors.As(tc.err, &hinter), "every sentinel implements RemedyHinter")
			hint := hinter.RemedyHint()
			require.NotEmpty(t, hint, "remedy hint must be non-empty")
			require.False(t, strings.Contains(hint, "\n"), "remedy hint must be a single line")
			require.Contains(t, hint, tc.wantHintSubstr, "remedy hint substring locked")
		})
	}
}

// TestSetupErrors_DistinctIdentity confirms no two sentinels
// collapse to the same pointer — errors.Is branching relies on
// each being a distinct *remedyError.
func TestSetupErrors_DistinctIdentity(t *testing.T) {
	t.Parallel()

	all := []error{
		setup.ErrTokenAbsent,
		setup.ErrTokenDenied,
		setup.ErrTokenBad,
		setup.ErrBindConflict,
		setup.ErrStateStale,
		setup.ErrArtifactCollision,
		setup.ErrClockUnsynchronised,
		setup.ErrKeychainPermissionDenied,
		setup.ErrCheckIncomplete,
	}
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			require.False(t, errors.Is(all[i], all[j]),
				"sentinel %d should not Is sentinel %d", i, j)
		}
	}
}

// TestSetupErrors_ClockSyncReExportMatchesServer ensures the setup
// re-export is errors.Is-equivalent to the underlying server
// sentinel so init-side and serve-side handling agree.
func TestSetupErrors_ClockSyncReExportMatchesServer(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, setup.ErrClockUnsynchronised, server.ErrClockUnsynchronised)

	wrapped := fmt.Errorf("%w: drift exceeds budget", setup.ErrClockUnsynchronised)
	require.ErrorIs(t, wrapped, setup.ErrClockUnsynchronised)
	require.ErrorIs(t, wrapped, server.ErrClockUnsynchronised)
}

// TestSetupErrors_KeychainReExportMatchesPackage ensures the setup
// re-export is errors.Is-equivalent to the underlying keychain
// sentinel so guided flow code can match either form.
func TestSetupErrors_KeychainReExportMatchesPackage(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, setup.ErrKeychainPermissionDenied, keychain.ErrKeychainPermissionDenied)

	wrapped := fmt.Errorf("%w: item hush-discord", setup.ErrKeychainPermissionDenied)
	require.ErrorIs(t, wrapped, setup.ErrKeychainPermissionDenied)
	require.ErrorIs(t, wrapped, keychain.ErrKeychainPermissionDenied)
}

// TestClockSyncRemedy_PerPlatform asserts the platform-aware
// helper returns the exact command string each supported GOOS is
// documented to receive. Locked text — AC-8's "exact remediation
// command" promise.
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
