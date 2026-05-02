package discord

import "errors"

// Sentinel errors. Compare with errors.Is. Static category messages —
// no token bytes, no ApprovalRequest fields, no key material
// (Constitution X).

var (
	// ErrDiscordUnavailable is returned when the WebSocket gateway is
	// closed (boot-down, mid-flight disconnect, or delivery failure).
	// Caller (SDD-12) maps to HTTP 503.
	ErrDiscordUnavailable = errors.New("hush/discord: discord unavailable")

	// ErrApprovalDenied is returned when the operator clicks the Deny
	// button.
	ErrApprovalDenied = errors.New("hush/discord: approval denied")

	// ErrApprovalTimeout is returned when the caller's ctx deadline
	// elapses before any operator action. The actual returned error
	// wraps both ErrApprovalTimeout and context.DeadlineExceeded so
	// errors.Is matches each independently.
	ErrApprovalTimeout = errors.New("hush/discord: approval timed out")

	// ErrRateLimited is returned when the bucket for this
	// (SupervisorName, ClientIP) key already has a delivered prompt
	// within the configured window OR when a concurrent pending slot
	// is held.
	ErrRateLimited = errors.New("hush/discord: rate limited")

	// ErrMissingToken is returned by NewBotApprover when cfg.Token is
	// nil or already destroyed / zero-length.
	ErrMissingToken = errors.New("hush/discord: missing token")

	// ErrMissingOwnerID is returned by NewBotApprover when cfg.OwnerID
	// is empty.
	ErrMissingOwnerID = errors.New("hush/discord: missing owner id")

	// ErrMissingAppID is returned by NewBotApprover when cfg.AppID is
	// empty.
	ErrMissingAppID = errors.New("hush/discord: missing app id")

	// ErrMissingLogger is returned by NewBotApprover when the logger
	// argument is nil.
	ErrMissingLogger = errors.New("hush/discord: missing logger")

	// errSessionInitFailed anonymises a discordgo.New construction
	// failure so the wrapped error chain at NewBotApprover never relies
	// on a third-party package's redaction discipline. Caller-facing
	// behavior is unchanged: errors.Is(err, ErrDiscordUnavailable) still
	// matches.
	errSessionInitFailed = errors.New("hush/discord: session init failed")
)
