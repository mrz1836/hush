# API Contract — `internal/discord` (SDD-11)

This document is the locked exported API for
`github.com/mrz1836/hush/internal/discord`. Once the implement
commit lands, this surface is mirrored into
`docs/PACKAGE-MAP.md` §`internal/discord`. Symbol REMOVAL or
signature CHANGES require a new SDD chunk.

---

## 1. Package path

`github.com/mrz1836/hush/internal/discord`

## 2. Imports (illustrative)

```go
import (
    "context"
    "log/slog"
    "time"

    "github.com/mrz1836/hush/internal/token"
    "github.com/mrz1836/hush/internal/vault/securebytes"
)
```

The package depends on **`github.com/bwmarrin/discordgo`** at
the production level; the test seam is a private package-internal
shim (research [R-007](../research.md#r-007--testing-without-discord-shim-strategy)).

---

## 3. Exported types

### 3.1 `Approver`

```go
// Approver gates every secret-claim path. The package's BotApprover
// is the production implementation; a future fake or alternate
// transport may also implement this interface.
//
// RequestApproval blocks until the operator clicks Approve or Deny,
// the per-request ctx deadline elapses, the chat transport becomes
// unavailable, or the rate-limit gate denies the request. The
// returned Decision MUST be inspected only on a nil error; on any
// non-nil error the caller MUST treat the request as not approved
// (Constitution II).
//
// Implementations MUST be safe for concurrent use.
type Approver interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}
```

### 3.2 `ApprovalRequest`

```go
// ApprovalRequest is the input to every approval call. All fields
// are caller-supplied; the package renders them verbatim and never
// validates that, e.g., MachineName exists or ClientIP is reachable.
//
// SupervisorName MUST be non-empty when SessionType is
// token.SessionSupervisor and MUST be empty otherwise.
type ApprovalRequest struct {
    MachineName    string
    ClientIP       string
    Reason         string
    Scope          []string
    RequestedTTL   time.Duration
    SessionType    token.SessionType
    SupervisorName string
}
```

### 3.3 `Decision`

```go
// Decision is returned by RequestApproval only on the operator-
// Approve path. v0.1.0: ApprovedTTL == request.RequestedTTL exactly,
// Reason == "" — the fields exist for forward-compatible UX
// (e.g., a future shorten-TTL modal).
type Decision struct {
    Approved    bool
    ApprovedTTL time.Duration
    Reason      string
}
```

### 3.4 `BotConfig`

```go
// BotConfig parameterises NewBotApprover. Token, OwnerID, and AppID
// are required; AuditChannelID is optional (empty disables audit-
// channel mirroring); DMRateLimit ≤ 0 falls back to DefaultDMRateLimit.
//
// Token is consumed by NewBotApprover: the constructor reads its
// bytes via Use(fn) once at session-init time. Callers MAY (and
// SHOULD) call Token.Destroy() after NewBotApprover returns; the
// package keeps no further reference to the SecureBytes object.
type BotConfig struct {
    Token          *securebytes.SecureBytes
    OwnerID        string
    AppID          string
    AuditChannelID string
    DMRateLimit    time.Duration
}
```

### 3.5 `BotApprover`

```go
// BotApprover is the production Approver, backed by a
// *discordgo.Session. Construct via NewBotApprover.
//
// Lifecycle: the ctx passed to NewBotApprover owns the monitor
// goroutine and the underlying *discordgo.Session. When that ctx
// is cancelled, the monitor closes the session, drains all pending
// requests with ErrDiscordUnavailable, and exits.
//
// Concurrency: safe for concurrent RequestApproval calls from many
// goroutines.
type BotApprover struct {
    // unexported
}
```

`*BotApprover` satisfies `Approver`.

---

## 4. Exported functions

### 4.1 `NewBotApprover`

```go
// NewBotApprover constructs a Discord-backed Approver.
//
// Validation order:
//   1. cfg.Token == nil OR Token.Len() == 0 → ErrMissingToken
//   2. cfg.OwnerID == "" → ErrMissingOwnerID
//   3. cfg.AppID == "" → ErrMissingAppID
//   4. logger == nil → ErrMissingLogger
// Validation failures return the bare sentinel; no side effect
// occurs.
//
// On successful validation:
//   - Apply DMRateLimit default (≤0 → DefaultDMRateLimit).
//   - Construct *discordgo.Session via cfg.Token.Use(fn) (research
//     R-003 — token transitions through string exactly once).
//   - Register Connect / Disconnect / Ready / Resumed /
//     InteractionCreate handlers.
//   - Spawn the monitor goroutine bound to ctx.
//   - Call session.Open(). Open() failure does NOT fail
//     NewBotApprover (FR-013a) — the approver enters the
//     unavailable state and the monitor's reconnect loop drives
//     recovery.
//   - Return *BotApprover, nil.
//
// Construction errors are reserved for cfg validation failures.
// Transport-down at boot is not a construction error.
func NewBotApprover(ctx context.Context, cfg BotConfig, logger *slog.Logger) (*BotApprover, error)
```

### 4.2 `(*BotApprover).RequestApproval`

```go
// RequestApproval delivers an approval prompt to the configured
// operator's DM channel and blocks until one of:
//   - operator clicks Approve → Decision{Approved: true,
//     ApprovedTTL: req.RequestedTTL}, nil
//   - operator clicks Deny → Decision{}, ErrApprovalDenied
//   - ctx deadline elapses → Decision{}, ErrApprovalTimeout (wraps
//     context.DeadlineExceeded — errors.Is on either matches)
//   - ctx cancelled (not deadline) → Decision{}, ctx.Err()
//   - WebSocket disconnects mid-flight → Decision{}, ErrDiscordUnavailable
//
// Pre-conditions checked in order BEFORE any side effect:
//   1. available flag is false → return ErrDiscordUnavailable
//      immediately. The rate-limit bucket is NOT consulted
//      (FR-021a, clarification 2026-04-30 Q5).
//   2. rate-limit bucket Acquire denies the request → return
//      ErrRateLimited.
//   3. DM rendering produces a *discordgo.MessageSend.
//   4. session.UserChannelCreate(OwnerID) +
//      session.ChannelMessageSendComplex(dmChan, msg). Failure
//      refunds the rate-limit bucket and returns ErrDiscordUnavailable.
//   5. Bucket Commit promotes the pending slot to delivered.
//   6. Audit-channel mirror (best-effort, non-blocking) — fires
//      request_received.
//   7. Block on the per-request decision channel.
//
// Side effects on the post-decision path: emit one of
// {approved, denied, timed_out} to the audit channel
// (best-effort). The rate_limited audit event fires from path 2
// before this method returns.
//
// Concurrency: safe to call from many goroutines simultaneously;
// each call allocates its own request UUID and channel slot.
func (a *BotApprover) RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
```

---

## 5. Exported constants

```go
// DefaultDMRateLimit is the default value applied when
// BotConfig.DMRateLimit ≤ 0 (FR-021).
const DefaultDMRateLimit = 5 * time.Minute
```

---

## 6. Sentinel errors

All errors are `var Err... = errors.New("static message")` so
consumers compare via `errors.Is`. Static messages contain ONLY
the failure category — no token byte, no machine name, no scope
item, no key byte (Constitution X).

### 6.1 Runtime sentinels

| Sentinel | Message | Trigger |
|----------|---------|---------|
| `ErrDiscordUnavailable` | `"hush/discord: discord unavailable"` | (a) `available` flag was false at `RequestApproval` entry; (b) DM delivery failed mid-call; (c) WebSocket disconnected while a request was in-flight (the monitor drains pending channels with this sentinel). Caller (SDD-12) maps to HTTP 503. |
| `ErrApprovalDenied` | `"hush/discord: approval denied"` | Operator clicked Deny. |
| `ErrApprovalTimeout` | `"hush/discord: approval timed out"` | Caller's `ctx` deadline elapsed before any operator action. Wraps `context.DeadlineExceeded`. |
| `ErrRateLimited` | `"hush/discord: rate limited"` | Rate-limit bucket for this `(SupervisorName, ClientIP)` key has a `delivered` timestamp within the configured window, OR a concurrent `pending` slot is held. |

### 6.2 Construction sentinels

| Sentinel | Message | Trigger |
|----------|---------|---------|
| `ErrMissingToken` | `"hush/discord: missing token"` | `cfg.Token == nil` OR `cfg.Token.Len() == 0`. |
| `ErrMissingOwnerID` | `"hush/discord: missing owner id"` | `cfg.OwnerID == ""`. |
| `ErrMissingAppID` | `"hush/discord: missing app id"` | `cfg.AppID == ""`. |
| `ErrMissingLogger` | `"hush/discord: missing logger"` | `logger == nil`. |

### 6.3 Error wrapping policy

- Runtime sentinels are returned bare (no `%w`) when the
  package itself originates the failure — `RequestApproval`
  returning `ErrApprovalDenied` returns the bare sentinel.
- `ErrApprovalTimeout` wraps `context.DeadlineExceeded` via
  `fmt.Errorf("hush/discord: approval timed out: %w", context.DeadlineExceeded)` so `errors.Is(err, ErrApprovalTimeout)` AND `errors.Is(err, context.DeadlineExceeded)` both evaluate true.
- `ErrDiscordUnavailable` from a delivery-failure path wraps
  the underlying SDK error via `%w`. `errors.Is(err, ErrDiscordUnavailable)` evaluates true; `errors.Unwrap` reveals the SDK error for forensic logging (the SDK error MUST NOT contain a token byte — discordgo errors are category strings, not token-bearing).
- `ctx.Canceled` is returned verbatim (not wrapped) so
  `errors.Is(err, context.Canceled)` evaluates true.

---

## 7. Behaviour contracts (locked)

### 7.1 No auto-approve

`RequestApproval` returns `Decision{Approved: true}` if and
only if all of:

1. The package received an `InteractionCreate` event whose
   `CustomID` matches a pending request created by the same
   call's UUID generation.
2. The interaction's component CustomID has the `":approve"`
   suffix (the Approve button).
3. The `available` flag was true at the moment the interaction
   was processed.

There is no exported function, no field, no constant, no
environment-variable lookup, no build tag, and no runtime mode
that produces `Approved: true` without the operator-Approve
interaction. Constitution II non-negotiable, FR-012, spec User
Story 3 acceptance scenario 4. Test-asserted by
`TestBotApprover_NoAutoApproveKnobExists` (AST walk).

### 7.2 Disconnect-fast-path

When `available == false`, `RequestApproval` returns
`ErrDiscordUnavailable` within ≤100 ms (SC-002). The
implementation reads the flag with `atomic.Load`; the latency
is dominated by the caller goroutine's scheduling, not by any
network or lock acquisition.

### 7.3 In-flight unblock on disconnect

A `RequestApproval` call that has crossed step 7 (blocked on
the per-request channel) unblocks with `ErrDiscordUnavailable`
within the monitor goroutine's drain window (≤100 ms after the
disconnect event is delivered to the monitor). FR-010, User
Story 3 acceptance scenario 2.

### 7.4 Reconnect resumes normal operation

After `Ready` or `Resumed` fires post-reconnect, subsequent
`RequestApproval` calls operate normally. No degraded mode
persists. FR-013, User Story 3 acceptance scenario 3.

### 7.5 Monotonic-clock rate limiting

Rate-limit timestamps are read from `time.Now()` and compared
via `time.Time.Sub()` — Go's `time.Time` carries a monotonic
component that survives wall-clock changes and host suspend.
FR-019.

### 7.6 Per-key isolation

The rate-limit bucket is keyed by `(SupervisorName, ClientIP)`
for `SessionSupervisor` and `(ClientIP)` for
`SessionInteractive`. Two requests differing in either field
share no bucket. FR-018, User Story 4 acceptance scenarios 3
and 4.

### 7.7 Token absent from artifacts

`cfg.Token`'s byte content does not appear in any `slog`
record, any `err.Error()` string, any audit-channel payload, or
any return value of any exported function. FR-024,
Constitution X. Test-asserted by
`TestBotApprover_TokenAbsentFromAllArtifacts`.

### 7.8 Monitor goroutine ctx-bound

The monitor goroutine spawned by `NewBotApprover` exits within
≤100 ms of the constructor's `ctx` cancellation. On exit it
calls `session.Close()` (idempotent) and drains pending
request channels with `ErrDiscordUnavailable`. FR-026,
Constitution IX.

### 7.9 First-action-wins

If two interaction events arrive for the same `CustomID`, the
first event removes the pending-request map entry before
sending; the second event finds no entry and is silently
dropped. FR-017.

### 7.10 Audit-channel mirroring is best-effort

When `cfg.AuditChannelID != ""`, the package emits five
lifecycle event types (`request_received`, `approved`,
`denied`, `timed_out`, `rate_limited`) to the configured
channel via per-event goroutines. Mirror failures log WARN and
are otherwise swallowed; the primary `RequestApproval` flow is
not affected. FR-008, clarification 2026-04-30 Q4.

---

## 8. Test surface (named, required)

Listed for traceability. Every test name maps to a concrete
behaviour contract above. Coverage target: ≥85% on the package.

### 8.1 `bot_test.go`

- `TestBotApprover_NeverAutoApprovesOnDiscordError` (Constitution II)
- `TestBotApprover_NoAutoApproveKnobExists` (Constitution II, AST walk)
- `TestBotApprover_TokenAbsentFromAllArtifacts` (Constitution X, FR-024)
- `TestBotApprover_DisconnectFastPath` (SC-002, ≤100 ms)
- `TestBotApprover_RaceClean` (Constitution VIII, `magex test:race`)
- `TestNewBotApprover_BootDownStartsUnavailable` (FR-013a)
- `TestNewBotApprover_ValidatesConfig` (construction sentinels)

### 8.2 `approver_test.go`

- `TestApprover_BotApproverImplementsApprover` (interface conformance)
- `TestApprovalRequest_DaemonRequiresSupervisorName` (validation hint)

### 8.3 `monitor_test.go`

- `TestMonitor_DisconnectSurfacesUnavailable` (FR-009)
- `TestMonitor_DisconnectUnblocksInFlightRequest` (FR-010, US3 AS2)
- `TestMonitor_ReconnectRestoresAvailability` (FR-013, US3 AS3)
- `TestMonitor_GoroutineExitsOnCtxCancel` (FR-026)
- `TestMonitor_ReconnectBackoffCappedAt60s` (FR-013b)

### 8.4 `render_test.go`

- `TestApprovalRender_InteractiveLabel` (FR-006)
- `TestApprovalRender_DaemonLabel` (FR-006)
- `TestApprovalRender_DaemonIncludesSupervisorName` (FR-006, US2 AS1)
- `TestApprovalRender_VisuallyDistinctFromInteractive` (US2 AS2)
- `TestApprovalRender_NeverIncludesToken` (FR-007, FR-024)
- `TestApprovalRender_AllRequestFieldsPresent` (FR-007)

### 8.5 `ratelimit_test.go`

- `TestRateLimit_BlocksSecondPromptWithin5Min` (FR-018, FR-020)
- `TestRateLimit_AllowsAfterWindow` (FR-018)
- `TestRateLimit_PerKeyIsolation` (FR-018, US4 AS3)
- `TestRateLimit_InteractiveKeyedByClientIP` (FR-018, US4 AS4)
- `TestRateLimit_TransportUnavailableDoesNotConsumeToken` (FR-021a)
- `TestRateLimit_DeliveryFailureRefundsToken` (FR-021a)
- `TestRateLimit_ZeroDMRateLimitUsesDefault` (FR-021)
- `TestRateLimit_UsesMonotonicClock` (FR-019)

### 8.6 `audit_test.go`

- `TestAuditChannel_AllFiveLifecycleEventsMirrored` (FR-008, clar Q4)
- `TestAuditChannel_FailureDoesNotBlockApproval` (FR-008)
- `TestAuditChannel_NoTokenInPayload` (FR-024)
- `TestAuditChannel_DisabledWhenIDEmpty` (FR-008)

### 8.7 Decision routing (in `bot_test.go` for end-to-end shape)

- `TestDecisionRouting_Approve` (FR-014)
- `TestDecisionRouting_Deny` (FR-015)
- `TestDecisionRouting_Timeout` (FR-016)
- `TestDecisionRouting_FirstActionWins` (FR-017)
- `TestDecisionRouting_CtxCancelled` (FR-016 sibling — `context.Canceled`)

Total: **30+ named tests** across 6 test files.

---

## 9. Future-compatibility notes

The exported surface above is locked at SDD-11. Future SDDs MAY
add symbols (additional sentinels, additional configuration
fields with safe defaults, additional methods on `*BotApprover`)
without breaking SDD-11. Symbol REMOVAL or signature CHANGES
require a new SDD chunk.

Notable forward-compatibility seams already in the API:

- `Decision.ApprovedTTL` — distinct from `RequestedTTL` even
  though they are equal in v0.1.0; a future shorten-TTL modal
  surfaces here without breaking the field shape (clarification
  2026-04-30 Q2).
- `Decision.Reason` — empty in v0.1.0; a future modal that
  collects an operator-supplied reason populates this field.
- `BotConfig.AuditChannelID` — optional; a future SDD that
  splits audit mirroring across multiple channels can extend
  the `BotConfig` with additional channel ID fields.
- `Approver` is a single-method interface — future approver
  back-ends (Slack, PagerDuty, TOTP-second-factor) implement
  the same interface and slot in via the SDD-12 adapter
  without changing this package's surface.
