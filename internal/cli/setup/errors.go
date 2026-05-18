package setup

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/server"
)

// RemedyHinter is the optional interface attached to every setup
// sentinel error. RemedyHint returns a single-line, copy-pasteable
// next-step the guided flow prints under the failure message.
//
// The hint MUST NOT echo any secret value, token byte, or vault
// byte (Constitution X). It MAY include absolute paths, exit codes,
// or shell snippets — those are user inputs the operator already
// owns.
type RemedyHinter interface {
	RemedyHint() string
}

// remedyError is the concrete sentinel type backing every Err* in
// this package. The message is the literal text errors.New would
// produce; remedy carries the one-liner. When alias is non-nil the
// sentinel is errors.Is-equivalent to its alias — used so the
// re-exports below match against the underlying server / keychain
// sentinels without forcing callers to import both packages.
type remedyError struct {
	msg    string
	remedy string
	alias  error
}

// Error returns the sentinel's locked message.
func (e *remedyError) Error() string { return e.msg }

// RemedyHint returns the sentinel's one-line remediation step.
func (e *remedyError) RemedyHint() string { return e.remedy }

// Is reports whether target is this sentinel or its alias. The
// alias bridge is what makes [ErrClockUnsynchronised] and
// [ErrKeychainPermissionDenied] match the server / keychain
// originals via errors.Is.
func (e *remedyError) Is(target error) bool {
	if target == nil {
		return false
	}
	if e == target {
		return true
	}
	if e.alias != nil && errors.Is(e.alias, target) {
		return true
	}
	return false
}

// hinterAs is a small wrapper around errors.As that exists so
// setup.go (which builds results) does not need to import errors
// — kept here so the err discriminator lives next to the
// RemedyHinter interface that drives it.
func hinterAs(err error, target *RemedyHinter) bool {
	return errors.As(err, target)
}

// Setup error taxonomy. Each error is a distinct sentinel so the
// guided flow can branch on it via errors.Is. Wrap with
// fmt.Errorf("%w: ...", sentinel) to add context that the caller
// can still match against the bare sentinel.
//
// Sentinel errors are the only mutable package state and must live
// at package scope so callers can errors.Is against them
// (Constitution VII / IX).
var (
	// ErrTokenAbsent fires when the Discord bot token cannot be
	// located in the Keychain and the env-token fallback is empty.
	// Distinct from [ErrTokenDenied] which means the item exists
	// but the OS refused to read it.
	ErrTokenAbsent = &remedyError{
		msg:    "hush/setup: discord bot token absent",
		remedy: "run `hush init server` and follow the guided flow to store the bot token, or export HUSH_DISCORD_BOT_TOKEN before retrying",
	}

	// ErrTokenDenied fires when the Keychain item exists but the
	// OS denied the read (Darwin exit 51 / errSecAuthFailed /
	// errSecInteractionNotAllowed). The guided flow shows ACL
	// repair commands and a delete-and-recreate option — it does
	// NOT silently switch to env-token mode (Plan Q3=a+b).
	ErrTokenDenied = &remedyError{
		msg:    "hush/setup: discord bot token denied by keychain",
		remedy: "either repair the ACL with `security set-generic-password-partition-list -S apple-tool:,apple: -s hush-discord -a <account>` and re-run, or pick the delete-and-recreate option in the guided flow",
	}

	// ErrTokenBad fires when a token retrieved from Keychain or
	// env is structurally invalid (wrong length, contains
	// disallowed characters, fails a Discord-side preflight). The
	// token value itself never appears in the surfaced message.
	ErrTokenBad = &remedyError{
		msg:    "hush/setup: discord bot token rejected by validation",
		remedy: "rotate the token in the Discord developer portal and re-run `hush init server` to store the new value",
	}

	// ErrBindConflict fires when the configured listen address is
	// either off-CGNAT, already bound, or routes through a non-
	// Tailscale interface.
	ErrBindConflict = &remedyError{
		msg:    "hush/setup: listen address conflict",
		remedy: "pick an unused Tailscale CGNAT address (`tailscale ip -4`) with a free port and re-run `hush init server`",
	}

	// ErrStateStale fires when partial config/vault/state-dir
	// artifacts exist that the classifier marks `repairable` — the
	// guided flow offers a per-artifact reuse / repair / archive
	// choice (Plan Q2=b).
	ErrStateStale = &remedyError{
		msg:    "hush/setup: existing state is incomplete",
		remedy: "re-run `hush init server` and pick reuse / repair / archive per artifact, or pass `--on-existing=archive` for the non-interactive path",
	}

	// ErrArtifactCollision fires when an existing artifact maps
	// 1:1 to one the guided flow is about to create with
	// incompatible contents (e.g. config points at a vault that
	// hashes differently). The guided flow refuses to overwrite
	// without an explicit archive confirmation.
	ErrArtifactCollision = &remedyError{
		msg:    "hush/setup: existing artifact collides with new bootstrap",
		remedy: "archive the colliding artifact to <path>.bak-<RFC3339> via the guided flow's archive option, or move it aside manually before re-running",
	}

	// ErrClockUnsynchronised re-exports the server-side sentinel
	// so the guided flow can match it via errors.Is without
	// importing internal/server. The alias keeps it
	// errors.Is-equivalent to [server.ErrClockUnsynchronised]. The
	// remedy is platform-aware (see [ClockSyncRemedy]).
	ErrClockUnsynchronised = &remedyError{
		msg:    server.ErrClockUnsynchronised.Error(),
		remedy: ClockSyncRemedy(runtime.GOOS),
		alias:  server.ErrClockUnsynchronised,
	}

	// ErrKeychainPermissionDenied re-exports the keychain-package
	// sentinel so the guided flow has one canonical home for the
	// "OS denied" verdict. The alias keeps it errors.Is-equivalent
	// to [keychain.ErrKeychainPermissionDenied]. It is
	// structurally identical to [ErrTokenDenied] but covers the
	// broader case where the guided flow is reading a non-token
	// Keychain item.
	ErrKeychainPermissionDenied = &remedyError{
		msg:    keychain.ErrKeychainPermissionDenied.Error(),
		remedy: "open Keychain Access, locate the offending item, and grant `/usr/local/bin/hush` (or your hush binary path) read access; then re-run",
		alias:  keychain.ErrKeychainPermissionDenied,
	}

	// ErrCheckIncomplete is returned when a [Check] populates a
	// [SetupCheckResult] whose Status is [StatusUnknown]. It is a
	// programmer error caught at registry time; users should
	// never see it in a healthy build.
	ErrCheckIncomplete = &remedyError{
		msg:    "hush/setup: preflight check returned no status",
		remedy: "this is a hush bug — file an issue with the failing check name and the surrounding command",
	}
)

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
