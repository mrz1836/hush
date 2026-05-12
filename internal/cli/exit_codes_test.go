package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"testing"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/sign"
	"github.com/mrz1836/hush/internal/vault"
)

// TestExitCodes_ConstantValues asserts the public, contract-locked
// numeric values of the seven Exit* constants.
func TestExitCodes_ConstantValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"ExitOK", ExitOK, 0},
		{"ExitErr", ExitErr, 1},
		{"ExitInputErr", ExitInputErr, 2},
		{"ExitAuth", ExitAuth, 3},
		{"ExitNotFound", ExitNotFound, 4},
		{"ExitPerm", ExitPerm, 5},
		{"ExitConfigStale", ExitConfigStale, 78},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestExitCodes_NoStaleConfigInThisChunk asserts mapErr never returns
// ExitConfigStale (78) for any input — that code is reserved for
// the future supervisor↔child contract.
func TestExitCodes_NoStaleConfigInThisChunk(t *testing.T) {
	t.Parallel()
	inputs := []error{
		nil,
		errSyntheticTest,
		errFlagConflict,
		errMissingFlag,
		errConfigUnreadable,
		errInvalidJTI,
		errNoPassphraseSource,
		errAuthFailed,
		errNotFound,
		vault.ErrAuthFailed,
		vault.ErrSecretNotFound,
		sign.ErrSignatureInvalid,
		token.ErrSignatureInvalid,
		os.ErrPermission,
		server.ErrFileModeLoose,
		config.ErrTOMLDecode,
		fs.ErrNotExist,
		context.Canceled,
		context.DeadlineExceeded,
	}
	for _, in := range inputs {
		if got := mapErr(in); got == ExitConfigStale {
			t.Errorf("mapErr(%v) = %d, must never be ExitConfigStale (78)", in, got)
		}
	}
}

// TestExitCodes_AllSentinelsCovered table-drives mapErr over the
// locked sentinel sets from research.md §9.
func TestExitCodes_AllSentinelsCovered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   error
		want int
	}{
		{"nil", nil, ExitOK},
		{"flag conflict", errFlagConflict, ExitInputErr},
		{"missing flag", errMissingFlag, ExitInputErr},
		{"config unreadable", errConfigUnreadable, ExitInputErr},
		{"invalid jti", errInvalidJTI, ExitInputErr},
		{"no passphrase", errNoPassphraseSource, ExitInputErr},
		{"missing config", server.ErrMissingConfig, ExitInputErr},
		{"toml decode", config.ErrTOMLDecode, ExitInputErr},
		{"vault auth failed", vault.ErrAuthFailed, ExitAuth},
		{"sign invalid", sign.ErrSignatureInvalid, ExitAuth},
		{"token expired", token.ErrTokenExpired, ExitAuth},
		{"token revoked", token.ErrTokenRevoked, ExitAuth},
		{"auth failed sentinel", errAuthFailed, ExitAuth},
		{"vault not found", vault.ErrSecretNotFound, ExitNotFound},
		{"server secret missing", server.ErrSecretMissing, ExitNotFound},
		{"fs not exist", fs.ErrNotExist, ExitNotFound},
		{"not found sentinel", errNotFound, ExitNotFound},
		{"os perm denied", os.ErrPermission, ExitPerm},
		{"server file mode loose", server.ErrFileModeLoose, ExitPerm},
		{"vault file perms loose", vault.ErrFilePermsLoose, ExitPerm},
		{"config file mode", config.ErrConfigFileMode, ExitPerm},
		{"context canceled", context.Canceled, ExitErr},
		{"unknown error", errSyntheticUnknown, ExitErr},
		// SDD-23 sentinels.
		{"invalid grace window", errInvalidGraceWindow, ExitInputErr},
		{"socket ambiguous", errSocketAmbiguous, ExitInputErr},
		{"socket unreachable", errSocketUnreachable, ExitErr},
		{"supervisor refused", errSupervisorRefused, ExitErr},
		{"duplicate supervisor", errDuplicateSupervisor, ExitErr},
		{"pidfile locked", supervise.ErrPidLocked, ExitErr},
		{"wrapped pidfile locked", fmt.Errorf("%w: %w", errDuplicateSupervisor, supervise.ErrPidLocked), ExitErr},
	}
	for _, c := range cases {
		if got := mapErr(c.in); got != c.want {
			t.Errorf("%s: mapErr(%v) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}
