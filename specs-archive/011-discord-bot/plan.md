# Implementation Plan: `internal/discord` — Approver Interface + Discord-Backed BotApprover (SDD-11)

**Branch**: `011-discord-bot` | **Date**: 2026-04-30 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/011-discord-bot/spec.md`
**Chunk contract**: [docs/sdd/SDD-11.md](../../docs/sdd/SDD-11.md)

## Summary

`internal/discord` is the human-in-the-loop approval surface for hush. The
package owns the contract every secret-claim path invokes before the
vault server issues a session token, and ships the production
implementation: a Discord-bot-backed `BotApprover` that DMs the configured
operator, distinguishes interactive (human-at-terminal) from
`[DAEMON]` (long-running supervisor) prompts with a
visually-unmistakable header, monitors its own WebSocket and fails closed
to `ErrDiscordUnavailable` when the chat transport is down, and
rate-limits prompt delivery per `(SupervisorName, ClientIP)` so a
misconfigured daemon cannot flood the operator's phone. The bot token is
loaded into `*securebytes.SecureBytes` upstream (caller's
responsibility — typically `cmd/hush serve` via the keychain helper) and
never converted to a Go `string` outside the single, audited
session-init window required by the Discord SDK.

The chunk underpins **AC-3** (Discord approval flow) and is the canonical
realisation of **Constitution Principle II** ("Approval is human;
approval is phone; no auto-approve under any circumstance").

Approach (locked by SDD-11 chunk contract + spec clarifications
2026-04-30 + Constitution II/V/VIII/IX/X/XI; not subject to research
alternatives):

- **Discord SDK — `github.com/bwmarrin/discordgo` v0.29.0** (the only
  Go Discord library named in `README.md` and in
  `internal/testutil/discord_stub_test.go`'s ban-list — the latter
  proves the project has already committed to the import path being
  test-only off-limits, which only makes sense if the production-side
  import path is exactly that one). Constitution XI requires a
  Phase-0 dependency justification — recorded in
  research [R-001](./research.md#r-001--discord-sdk-bwmarrin-discordgo).

- **Package surface — locked exactly per SDD-11 chunk contract** (the
  type shapes here intentionally differ from `server.ApprovalRequest`
  / `server.Decision` declared at SDD-10 — see research
  [R-002](./research.md#r-002--approver-type-duality-server-vs-discord)
  for the divergence and the SDD-12 adapter that bridges the two):

  ```go
  // Package github.com/mrz1836/hush/internal/discord

  type Approver interface {
      RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
  }

  type ApprovalRequest struct {
      MachineName    string
      ClientIP       string
      Reason         string
      Scope          []string
      RequestedTTL   time.Duration
      SessionType    token.SessionType
      SupervisorName string
  }

  type Decision struct {
      Approved    bool
      ApprovedTTL time.Duration
      Reason      string
  }

  type BotConfig struct {
      Token          *securebytes.SecureBytes
      OwnerID        string
      AppID          string
      AuditChannelID string
      DMRateLimit    time.Duration
  }

  type BotApprover struct { /* unexported */ }

  func NewBotApprover(ctx context.Context, cfg BotConfig, logger *slog.Logger) (*BotApprover, error)

  // Sentinel errors — full catalogue + trigger conditions in contracts/api.md.
  var ErrDiscordUnavailable error
  var ErrApprovalDenied     error
  var ErrApprovalTimeout    error
  var ErrRateLimited        error
  ```

  Plus the construction-time validation sentinels `ErrMissingToken`,
  `ErrMissingOwnerID`, `ErrMissingAppID`, `ErrMissingLogger` (see
  research [R-008](./research.md#r-008--construction-time-validation)
  — these are the natural completion of the chunk contract's
  ellipsis, in the same way SDD-09's plan expanded its sentinel set
  for FR-006-style "every rejection identifiable by sentinel
  identity" guarantees).

- **`NewBotApprover` lifecycle (FR-026, Constitution IX):**
  1. Validate `cfg` — `Token` non-nil and not destroyed, `OwnerID`
     non-empty, `AppID` non-empty, `logger` non-nil. Failure returns
     a wrapping sentinel before any side effect.
  2. Apply defaults — `DMRateLimit` ≤ 0 → `DefaultDMRateLimit = 5 * time.Minute`
     (FR-021).
  3. Construct the `*discordgo.Session` via `discordgo.New("Bot " + token)`
     where `token` is read from `cfg.Token.Use(fn)` and the closure
     immediately constructs the session and returns. The transient
     Go `string` produced by the `"Bot " + token` concatenation is
     unavoidable — `discordgo.New` accepts `string` only — and is
     documented as a residual risk (research
     [R-003](./research.md#r-003--bot-token-ingestion-through-securebytes)).
  4. Register handlers: a `Ready` handler that flips the available
     flag to `true`; a `Disconnect` handler that flips to `false`;
     an `InteractionCreate` handler that demuxes button clicks to
     pending-request channels keyed by interaction `CustomID`.
  5. Spawn the monitor goroutine, owned by the `ctx` argument
     (FR-026 / Constitution IX). Termination contract: the goroutine
     exits when `ctx.Done()` fires, after which it closes the
     `*discordgo.Session` and drains pending requests with
     `ErrDiscordUnavailable`.
  6. Call `session.Open()`. **`Open()` failure does NOT fail
     `NewBotApprover`** (FR-013a, clarification 2026-04-30 Q1) —
     the approver enters the unavailable state, the monitor
     goroutine starts a background reconnect loop, and `RequestApproval`
     returns `ErrDiscordUnavailable` until `Ready` fires.
  7. Return the constructed `*BotApprover`. Construction is
     side-effect-bounded: no goroutines outlive `ctx`; no
     package-level state is mutated.

- **`(*BotApprover).RequestApproval(ctx, req)` flow:**
  1. **Available-flag gate** (FR-009 / SC-002): if the internal
     `available` flag is `false`, return `ErrDiscordUnavailable`
     immediately, **before** consulting the rate limiter (FR-021a,
     clarification 2026-04-30 Q5: transport-unavailable does NOT
     consume the rate-limit token).
  2. **Rate-limit gate** (FR-018, FR-020): construct the bucket key
     from `req` — `(SupervisorName, ClientIP)` for
     `SessionSupervisor`, `(ClientIP)` only for `SessionInteractive`
     (the `SupervisorName` field is empty for interactive — spec
     User Story 4 acceptance scenario 4). Acquire a token from the
     bucket; on failure return `ErrRateLimited`. The token is held
     in a "pending" slot — only committed on successful prompt
     delivery (FR-021a).
  3. **Render** the DM via `render.go` — distinct templates for
     interactive vs `[DAEMON]`, including all FR-003 fields, with
     Approve / Deny components. Returns a serialised
     `*discordgo.MessageSend` with `Components` (no token, no
     secret value, no key material — guarded by
     `TestApprovalRender_NeverIncludesToken`).
  4. **Deliver** via `session.ChannelMessageSendComplex(dmChannelID, msg)`
     where `dmChannelID` is obtained via `session.UserChannelCreate(OwnerID)`.
     Delivery failure (transport drop mid-call,
     `discordgo.ErrJSONUnmarshal`, etc.) returns `ErrDiscordUnavailable`
     and **refunds** the rate-limit token (FR-021a). Mirror the
     `request_received` event to the audit channel (best-effort,
     non-blocking — research
     [R-008](./research.md#r-008--audit-channel-mirroring)).
  5. **Commit** the rate-limit token (mark the bucket as consumed
     with the current monotonic timestamp, FR-019).
  6. **Block** on a per-request channel populated by the
     `InteractionCreate` handler. Three exit conditions:
     - Operator clicks Approve → return `Decision{Approved: true,
       ApprovedTTL: req.RequestedTTL}` (clarification 2026-04-30
       Q2: `ApprovedTTL` equals `RequestedTTL` exactly; `Reason`
       empty in v0.1.0).
     - Operator clicks Deny → return `ErrApprovalDenied`.
     - `ctx.Done()` fires → return `ctx.Err()` if it is
       `context.DeadlineExceeded`, mapped to `ErrApprovalTimeout`
       so callers compare via `errors.Is(err, ErrApprovalTimeout)`
       cleanly (FR-016). `context.Canceled` is returned verbatim.
     - **Or** the `available` flag flips to `false` mid-wait
       (unexpected disconnect during in-flight) → return
       `ErrDiscordUnavailable` (FR-010, spec User Story 3
       acceptance scenario 2).
  7. Mirror the terminal lifecycle event (`approved` /
     `denied` / `timed_out` / `rate_limited` — `request_received`
     was already mirrored at step 4; on the rate-limit path,
     `rate_limited` is the only mirrored event) to the audit
     channel best-effort.

- **`monitor.go` — WebSocket health monitor:**
  Owns the `available *atomic.Bool` flag and the reconnect loop.
  `discordgo` already implements an internal reconnect with
  exponential backoff — the monitor's job is to (a) translate
  `discordgo`'s connection events into the public available-flag
  semantics this package exposes, and (b) cap reconnect interval
  at 60 s per FR-013b (clarification 2026-04-30 Q3) by
  re-overriding `discordgo`'s default reconnect interval where
  the SDK's behaviour exceeds the cap. Research
  [R-004](./research.md#r-004--reconnect-mechanics) documents the
  exact event-handler layout. Termination: the monitor goroutine
  exits when the constructor's `ctx` is cancelled; on exit it
  calls `session.Close()` (idempotent) and broadcasts
  `ErrDiscordUnavailable` to every pending request channel so
  in-flight `RequestApproval` calls unblock (FR-010, User Story 3
  acceptance scenario 2).

- **`render.go` — DM templates:**
  Two templates, switched on `req.SessionType`:
  - **Interactive** prefix: Unicode green-checkmark glyph
    (`U+2705`) followed by `**Interactive secret request**`. Body:
    `Machine`, `Mesh IP`, `Scope`, `Reason`, `Requested TTL`.
  - **Daemon** prefix: Unicode warning glyph (`U+26A0`) followed
    by `**[DAEMON] Supervisor secret request**`. Body adds
    `Supervisor` immediately after `Machine`.
  Both templates emit Discord-API `*discordgo.MessageSend`
  containing `Embeds[0]` (formatted body) and `Components[0]`
  (action row with Approve + Deny buttons; component `CustomID`
  is the per-request UUID assigned in `RequestApproval`).
  **No template path embeds the bot token, any secret value, or
  any key material** — guarded by `TestApprovalRender_NeverIncludesToken`
  (FR-007, Constitution X).

- **`ratelimit.go` — token bucket:**
  Implementation is a simple last-delivery-time map keyed by
  `(SupervisorName, ClientIP)` for supervisor sessions and
  `(ClientIP)` for interactive. Acquire returns one of three
  states: `granted` (timestamp recorded into a `pending` slot),
  `pending` (already pending — fail closed with `ErrRateLimited`,
  the same as `denied`; this prevents a concurrent-request flood
  bypassing the gate), or `denied` (window not elapsed — return
  `ErrRateLimited`). Commit promotes the pending slot to
  delivered; refund clears it. The clock used is
  `time.Now().UnixNano()` against a stored monotonic timestamp
  (FR-019, Constitution V). Default window is `5 * time.Minute`;
  `cfg.DMRateLimit ≤ 0` falls back to default (FR-021).

- **`bot.go` — `BotApprover` struct + constructor (above) + the
  `RequestApproval` method.** No business logic that does not
  belong in `monitor.go`, `render.go`, or `ratelimit.go` —
  `bot.go` is the orchestration seam.

- **`approver.go` — interface, request/decision types, sentinels.**
  Single-method interface (Constitution IX): `Approver` defined
  here so consumers (SDD-12) can depend on the discord package
  without dragging the `*BotApprover` concrete type. Note: this
  is the package-internal `Approver`; the SDD-10 `server.Approver`
  is a *separate* surface — SDD-12's claim handler will compose
  the two via a thin adapter (research
  [R-002](./research.md#r-002--approver-type-duality-server-vs-discord)).

- **Memory zeroization discipline (FR-022 / FR-023 / FR-024 /
  FR-025, Constitution X):**
  - The bot token is held by the caller in `*SecureBytes` until
    `NewBotApprover` is called.
  - `NewBotApprover` reads it via `cfg.Token.Use(fn)` exactly
    once. The closure `fn` performs the `discordgo.New("Bot " + tok)`
    call and returns the constructed session. The function-local
    `string` produced by `"Bot " + tok` lives on the goroutine
    stack until `Use` returns, at which point the SecureBytes
    callback wrapper zeroes the closure's `[]byte` view (SDD-02
    contract). The transient `string` is unreachable thereafter
    — **but** Go's GC may have copied it into a heap-allocated
    string, which `discordgo` itself stores as a struct field for
    the life of the session. This is an unavoidable consequence
    of the SDK's `string`-typed token API; documented as a
    residual risk in research
    [R-003](./research.md#r-003--bot-token-ingestion-through-securebytes).
    The mitigation Constitution X requires (`LogValue() →
    "[redacted]"`) holds for the SecureBytes wrapper itself; the
    discordgo string copy is invisible to slog because no code in
    `internal/discord` ever passes `session.Token` (or the
    pre-concatenated form) to a logger or an error message.
  - The DM templates render `req.Scope` as a comma-joined string
    of secret **names** — never values (the package never sees
    secret values; they live one chunk over in `internal/vault`).
  - All sentinel error messages are static category strings; no
    token, owner ID, app ID, or scope item appears in any
    `Error()` text. `TestBotApprover_TokenAbsentFromAllArtifacts`
    asserts this against a captured slog buffer + every
    `err.Error()` string + every audit-event payload, using
    `testutil.AssertSentinelAbsent` against a unique sentinel
    bot token injected at test setup (User Story 5 acceptance
    scenario 1).

- **Error mapping (FR-001 / FR-009 / FR-015 / FR-016 / FR-020 /
  Constitution X):**
  - `ErrDiscordUnavailable` ("discord unavailable"): WebSocket
    closed unexpectedly OR delivery failed mid-call OR available
    flag was false at entry. Caller (SDD-12) maps to HTTP 503.
  - `ErrApprovalDenied` ("approval denied"): operator clicked
    Deny.
  - `ErrApprovalTimeout` ("approval timed out"): caller's `ctx`
    deadline elapsed before any operator action. Wraps
    `context.DeadlineExceeded` — `errors.Is(err,
    ErrApprovalTimeout)` AND `errors.Is(err,
    context.DeadlineExceeded)` both evaluate true.
  - `ErrRateLimited` ("rate limited"): the bucket for this
    `(SupervisorName, ClientIP)` key already has a delivered
    prompt within the configured window.
  - `ErrMissingToken`, `ErrMissingOwnerID`, `ErrMissingAppID`,
    `ErrMissingLogger`: `NewBotApprover` validation failures.
  - All errors are exported `var Err... = errors.New("static
    message")` declarations. Errors compare with `errors.Is`.
    Static messages contain ONLY the failure category, never an
    identifier from `req`, never a byte from `cfg.Token`.

- **Cancellation (FR-010 / FR-026):**
  - The `ctx` passed to `NewBotApprover` is the lifecycle context
    for the monitor goroutine and the `*discordgo.Session`. It
    MUST NOT be the per-request `ctx`; the per-request `ctx` is
    the `ctx` passed to `RequestApproval`.
  - `RequestApproval` honours its `ctx`: a deadline elapsing
    returns `ErrApprovalTimeout` (wrapping
    `context.DeadlineExceeded`); a `ctx.Cancel()` returns
    `context.Canceled` verbatim.
  - The constructor's `ctx` cancelling mid-`RequestApproval`
    surfaces `ErrDiscordUnavailable` to the in-flight call (the
    monitor's exit path drains pending channels with that
    sentinel — User Story 3 acceptance scenario 2).

- **Logging & observability (Constitution X):**
  - The package takes a `*slog.Logger` injected at construction.
  - INFO records on lifecycle transitions (`session opened`,
    `session closed`, `available flipped`).
  - WARN records on unexpected disconnect, rate-limit denial,
    audit-channel mirroring failure.
  - DEBUG records on per-request flow (request received,
    delivered, decision returned) — keyed by request UUID.
  - **Zero token bytes appear in any log record** — guarded by
    `TestBotApprover_TokenAbsentFromAllArtifacts`. The token is
    held in `*SecureBytes` (`LogValue() → "[redacted]"`); the
    discordgo session's internal `Token` field is never logged
    by this package.

- **Audit channel mirroring (FR-008, clarification 2026-04-30
  Q4):**
  - All five lifecycle events (`request_received`, `approved`,
    `denied`, `timed_out`, `rate_limited`) are mirrored when
    `cfg.AuditChannelID != ""`. Each carries the same fields as
    the corresponding DM minus the action buttons.
  - Mirroring is **best-effort, non-blocking**: a goroutine
    spawned per event with the constructor's `ctx`; failure logs
    a WARN and is otherwise swallowed (the on-disk hash-chained
    audit log owned by SDD-13 is the authoritative record).
  - Same redaction as DM rendering: no token, no secret value,
    no key material.

- **Test strategy (Constitution VIII, 85% coverage band — Discord
  bot logic is the "Medium" priority class in `docs/SPEC.md`
  AC-9):**
  - **Fake `discordgo.Session` shim.** Tests interact with an
    interface seam (`type sessionAPI interface { ... }`) defined
    inside the package; production code wraps `*discordgo.Session`
    behind a thin adapter that satisfies it; tests use a
    programmable in-memory shim. The package source MUST NOT
    open a network socket under `go test`. (The
    `internal/testutil/discord_stub_test.go` already-existing
    AST-walk test asserts no production source under
    `internal/testutil/` imports `github.com/bwmarrin/discordgo`;
    the analogous discipline for `internal/discord/` itself is
    that the package imports `discordgo` only in the production
    files, never in `*_test.go` — research
    [R-007](./research.md#r-007--testing-without-discord-shim-strategy)).
  - Named tests required (chunk contract Prompt 4 + spec
    user-story coverage):
    `TestApprovalRender_InteractiveLabel`,
    `TestApprovalRender_DaemonLabel`,
    `TestApprovalRender_NeverIncludesToken`,
    `TestApprovalRender_DaemonIncludesSupervisorName`,
    `TestRateLimit_BlocksSecondPromptWithin5Min`,
    `TestRateLimit_AllowsAfterWindow`,
    `TestRateLimit_PerKeyIsolation`,
    `TestRateLimit_TransportUnavailableDoesNotConsumeToken`,
    `TestRateLimit_DeliveryFailureRefundsToken`,
    `TestRateLimit_ZeroDMRateLimitUsesDefault`,
    `TestRateLimit_UsesMonotonicClock`,
    `TestDecisionRouting_Approve`,
    `TestDecisionRouting_Deny`,
    `TestDecisionRouting_Timeout`,
    `TestDecisionRouting_FirstActionWins`,
    `TestMonitor_DisconnectSurfacesUnavailable`,
    `TestMonitor_DisconnectUnblocksInFlightRequest`,
    `TestMonitor_ReconnectRestoresAvailability`,
    `TestMonitor_GoroutineExitsOnCtxCancel`,
    `TestNewBotApprover_BootDownStartsUnavailable`,
    `TestNewBotApprover_ValidatesConfig`,
    `TestBotApprover_NeverAutoApprovesOnDiscordError`,
    `TestBotApprover_TokenAbsentFromAllArtifacts`,
    `TestBotApprover_NoAutoApproveKnobExists`,
    `TestAuditChannel_AllFiveLifecycleEventsMirrored`,
    `TestAuditChannel_FailureDoesNotBlockApproval`.
  - **`TestBotApprover_NoAutoApproveKnobExists`** is the
    Constitution-II guard test: it walks the package AST via
    `go/parser.ParseDir` and asserts no `cfg` field name, no
    constant, and no environment-variable lookup mentions
    `auto_approve` / `autoapprove` / `bypass` / `skipApproval`
    anywhere in the production source. Spec User Story 3
    acceptance scenario 4 ("no setting causes the approver to
    return Approved while the chat transport is unavailable").
  - Race-clean (`magex test:race`) — the monitor goroutine, the
    pending-request map, and the rate-limit bucket map are all
    exercised concurrently in `TestBotApprover_RaceClean`.

Exported API (locked at SDD-11; mirrored into `docs/PACKAGE-MAP.md`
once the implement commit lands). Full catalogue with trigger
conditions: [contracts/api.md](./contracts/api.md).

```go
// Package github.com/mrz1836/hush/internal/discord

type Approver interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}

type ApprovalRequest struct {
    MachineName    string
    ClientIP       string
    Reason         string
    Scope          []string
    RequestedTTL   time.Duration
    SessionType    token.SessionType
    SupervisorName string
}

type Decision struct {
    Approved    bool
    ApprovedTTL time.Duration
    Reason      string
}

type BotConfig struct {
    Token          *securebytes.SecureBytes
    OwnerID        string
    AppID          string
    AuditChannelID string
    DMRateLimit    time.Duration
}

type BotApprover struct { /* unexported */ }

func NewBotApprover(ctx context.Context, cfg BotConfig, logger *slog.Logger) (*BotApprover, error)

const DefaultDMRateLimit = 5 * time.Minute

// Sentinel errors.
var ErrDiscordUnavailable error
var ErrApprovalDenied     error
var ErrApprovalTimeout    error
var ErrRateLimited        error
var ErrMissingToken       error
var ErrMissingOwnerID     error
var ErrMissingAppID       error
var ErrMissingLogger      error
```

The chunk contract (`docs/sdd/SDD-11.md` §"Exported API to lock in
PACKAGE-MAP.md") names four sentinels; the catalogue above
expands the construction-time validation into four named
sentinels (`ErrMissingToken`, `ErrMissingOwnerID`, `ErrMissingAppID`,
`ErrMissingLogger`) — same rationale SDD-09's plan applied when it
expanded its sentinel set (one sentinel per distinct rejection
category so `errors.Is` consumers can distinguish them). The
chunk contract's named four are the **runtime** sentinels;
construction-time is purely additive and does not change the
runtime surface.

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`); `CGO_ENABLED=0`
(Constitution IX).

**Primary Dependencies**:
- Go stdlib: `context`, `errors`, `fmt`, `log/slog`, `net/url`,
  `strings`, `sync`, `sync/atomic`, `time`.
- **NEW direct dependency: `github.com/bwmarrin/discordgo` v0.29.0**
  — the only Go Discord SDK that is actively maintained, has a
  documented WebSocket-event surface (which FR-009 / FR-010 / FR-013b
  require), and is named in `README.md`'s tech-stack list. Phase-0
  research [R-001](./research.md#r-001--discord-sdk-bwmarrin-discordgo)
  documents the maintainer-activity / supply-chain-provenance /
  transitive-dependency-footprint argument required by Constitution
  XI for every new direct dep.
- Existing direct dependencies (no version change):
  - `github.com/golang-jwt/jwt/v5` — pulled transitively via
    `internal/token` (for `token.SessionType`).
- Intra-repo:
  - `github.com/mrz1836/hush/internal/vault/securebytes` (SDD-02)
    for the `*SecureBytes` token wrapper.
  - `github.com/mrz1836/hush/internal/token` (SDD-07) for
    `token.SessionType` (the `string`-valued enum used in
    `ApprovalRequest.SessionType` per the locked contract).
- **No imports of `internal/server`** — Constitution IX
  ("define interfaces at the consumer") and `docs/PACKAGE-MAP.md`
  §Dependency Rules ("`internal/discord` should not import
  `internal/server`"). Bridging to `server.Approver` is SDD-12's
  problem.

**Storage**: stateless; no disk I/O; no filesystem surface. The
bot session holds a connection to Discord's WebSocket gateway; that
is network I/O owned by the upstream SDK. The package itself
allocates an in-memory rate-limit bucket map and an in-memory
pending-request map, both scoped to the `*BotApprover`'s lifetime.

**Testing**: `go test ./internal/discord/...` (table-driven unit
tests per `.github/tech-conventions/testing-standards.md`);
`magex test:race` race-clean. Coverage measured via `go test -cover
./internal/discord/`; target **≥85%** per Constitution VIII Medium
band ("Discord bot logic, CLI flags, config parsing"). Tests use a
programmable in-memory `discordgo.Session` shim — **no live
Discord connection ever opens under `go test`** (research
[R-007](./research.md#r-007--testing-without-discord-shim-strategy)).

**Target Platform**: macOS (darwin amd64/arm64) and Linux server
(amd64/arm64) per `.goreleaser.yml`. Windows is out of scope. The
package is platform-portable — `discordgo` is pure-Go (no CGO),
and `securebytes`'s mlock seam (the only platform-conditional
intra-repo import) is consumed only through its public
constructor / `Use(fn)` API.

**Project Type**: Single Go module (`github.com/mrz1836/hush`)
with a flat `internal/<domain>` layout per `docs/PACKAGE-MAP.md`.
`internal/discord/` is a new top-level domain package; no
sub-packages.

**Performance Goals**:
- `RequestApproval` end-to-end latency when the operator clicks
  Approve immediately: ≤500 ms p95 (dominated by the Discord
  HTTP round-trip for the DM send + the WebSocket round-trip for
  the interaction event).
- `RequestApproval` time-to-error when `available == false`:
  **≤100 ms** (FR-009, SC-002 — assertion in
  `TestBotApprover_DisconnectFastPath`).
- Rate-limit acquire/commit/refund: O(1) amortised against the
  bucket map; the map's lifetime is bounded by the
  `*BotApprover`'s lifetime; per-bucket records are reaped lazily
  on the next acquire for the same key (no scan goroutine
  needed).
- Monitor goroutine wake-up latency on disconnect: O(SDK event
  delivery) — typically sub-millisecond; the package adds no
  polling.

**Constraints**:
- **No `init()` function** (Constitution IX).
- **No mutable package-level globals** beyond the read-only
  sentinel-class exported `var Err...` declarations (Constitution IX).
- **No goroutines without an explicit ctx-scoped owner** —
  Constitution IX. The monitor goroutine is owned by the
  `NewBotApprover` `ctx`; the per-event audit-mirror goroutine
  is also owned by the same `ctx` (audit mirroring is
  best-effort and bounded by the constructor lifetime).
- **No bot-token bytes in any log record / error message / audit
  event** (FR-024, Constitution X). Test-asserted.
- **No live Discord connection in tests** (chunk contract,
  Constitution VIII).
- **No `auto_approve` / `bypass` / `skipApproval` knob** in any
  config field, environment variable, build tag, or runtime mode
  (Constitution II non-negotiable; FR-012; spec User Story 3
  acceptance scenario 4). Test-asserted via AST walk.
- **`internal/discord` does not import `internal/server`**
  (`docs/PACKAGE-MAP.md` §Dependency Rules).
- **No `discordgo` import in `*_test.go`** — the fake-session
  shim makes test files import-clean of the SDK (research
  [R-007](./research.md#r-007--testing-without-discord-shim-strategy)).

**Scale/Scope**:
- Five production source files matching the chunk contract:
  `bot.go` (BotApprover + NewBotApprover + RequestApproval),
  `approver.go` (interface + types + sentinels), `monitor.go`
  (WebSocket health monitor + reconnect cap), `render.go` (DM
  templates), `ratelimit.go` (token-bucket gate). Plus
  `errors.go` (sentinel error declarations colocated for
  grep-locality, the same locality refinement SDD-08, SDD-09,
  SDD-10 each made — sentinels live in their own file even when
  the chunk contract names a single primary source file) and
  `doc.go` (package doc + Constitution citations + Layer-V
  roster).
- Five test files matching the chunk contract:
  `bot_test.go`, `approver_test.go`, `monitor_test.go`,
  `render_test.go`, `ratelimit_test.go`. Plus
  `session_shim_test.go` (the `discordgo.Session` fake shim
  shared across the five test files; lives in the package's
  test code only — research
  [R-007](./research.md#r-007--testing-without-discord-shim-strategy))
  and `audit_test.go` (the audit-channel mirroring tests; not
  named in the chunk contract because the FR-008 + clarification
  2026-04-30 Q4 audit-channel surface is a layer the chunk
  contract subsumes under "alert formatting").
- Estimated ~600 LOC of production Go (`bot.go` largest at
  ~250 LOC, `monitor.go` ~120 LOC, `ratelimit.go` ~90 LOC,
  `render.go` ~80 LOC, `approver.go` ~30 LOC, `errors.go` ~30
  LOC, `doc.go` ~10 LOC) and ~1300 LOC of tests.
- Exported surface: 1 interface (`Approver`), 4 structs
  (`ApprovalRequest`, `Decision`, `BotConfig`, `BotApprover`),
  1 constructor (`NewBotApprover`), 1 default constant
  (`DefaultDMRateLimit`), 8 sentinel errors. Total exported
  identifiers: **15**.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principles in scope (per SDD-11)

| Principle | Constraint | Plan compliance |
|-----------|------------|-----------------|
| **II. Approval is Human, Approval is Phone** | Every fresh secret request MUST require explicit operator approval via Discord DM with interactive buttons. There is no auto-approve mode. Discord-bot unavailability MUST surface as a 503 to the client; the server MUST NOT fall back to auto-approve under any circumstance. | The package's only success path is `Decision{Approved: true}` returned after an `InteractionCreate` event whose `CustomID` matches a pending request and whose component is the Approve button. Every other path returns one of `ErrDiscordUnavailable`, `ErrApprovalDenied`, `ErrApprovalTimeout`, `ErrRateLimited` — none of which the caller can interpret as "approved". `BotApprover` exposes no field, no method, no constant, and no constructor option named `AutoApprove` / `Bypass` / `SkipApproval` — guarded by `TestBotApprover_NoAutoApproveKnobExists` (AST walk). The `available` flag gates `RequestApproval` before the rate limiter (FR-009 / FR-021a) — meaning even a misbehaving caller cannot synthesise an `Approved=true` decision when `available == false`; the only return is `ErrDiscordUnavailable`. ✅ |
| **V. Staleness is Visible, Failure is Loud** | Stale credentials and transport failures MUST be detectable and produce loud, distinct alerts. Discord bot disconnect produces a distinct, actionable alert. | `Disconnect` events flip `available` to false and emit a WARN slog record; `Ready` events flip it back and emit an INFO record. Audit-channel mirroring sends all five lifecycle events with distinct `type` field values (`request_received`, `approved`, `denied`, `timed_out`, `rate_limited`) — each renderable to a distinct alert format downstream (SDD-28). The DM templates use unmistakably distinct prefixes for interactive (green-checkmark) vs `[DAEMON]` (warning glyph) so the operator never approves the wrong type silently — guarded by `TestApprovalRender_InteractiveLabel` + `TestApprovalRender_DaemonLabel` (FR-006, spec User Story 2 acceptance scenario 2). ✅ |
| **VIII. Testing Discipline — TDD + 85% coverage (Medium band) + race-clean** | Test-first; **≥85% coverage** for Discord bot logic; race-clean under `-race`; every spec FR + every spec SC has at least one named test. | The /speckit-tasks-phase prompt (chunk contract Prompt 4) enforces test-first ordering: every behaviour-contract task has a paired test-writing task scheduled before it. Coverage gate is `go test -cover ./internal/discord/` ≥85% in the implement-phase release-step list. Race-clean is `magex test:race`. The named test floor is enumerated above (26+ tests across 6 test files, including the AST-walk Constitution-II guard test, the sentinel-leak token-absence test, the ≤100 ms disconnect fast-path test, and the monitor-goroutine ctx-cancel termination test). Tests use an in-memory `discordgo.Session` shim — never a live Discord connection (chunk contract). ✅ |
| **IX. Idiomatic Go Discipline — context first, no init, no globals, single-method interfaces, internal-only, modules, CGO disabled, goroutine ownership** | `context.Context` is the first parameter of every cancellable operation. No `init()`. No mutable package-level globals beyond sentinel errors. Single-method interfaces declared at the consumer. Internal-only. Go modules; no `vendor/`. CGO disabled. Every goroutine has an owner, an explicit cancellation path, and a documented termination condition. | `NewBotApprover` and `RequestApproval` take `ctx context.Context` as **first** parameter. **Zero `init()` functions.** Package-level `var`s are sentinel errors only. `Approver` is a single-method interface declared in this package (the consumer side per Constitution IX) — SDD-12's claim handler will declare its own sibling `server.Approver` interface against which `*BotApprover` is adapted (research [R-002](./research.md#r-002--approver-type-duality-server-vs-discord)). Package lives under `internal/`. `go.mod` is authoritative; no `vendor/`. `discordgo` is pure-Go; CGO stays disabled. The monitor goroutine is owned by the `NewBotApprover` `ctx`; termination is `ctx.Done() → session.Close() → drain pending → return`. Audit-mirror goroutines are owned by the same `ctx` and are bounded by the per-event work — they exit naturally when the event publish completes or when `ctx` cancels. Errors wrap with `%w` where there is an underlying cause (e.g., the SDK's send failure is wrapped under `ErrDiscordUnavailable`); pure-sentinel cases return the bare sentinel. **No `ctx` is held in a struct field** — the monitor's `ctx` is captured in the goroutine closure on spawn, not stored on `*BotApprover`. ✅ |
| **X. Observability & Redaction — no token / secret value / key bytes in logs or errors; type-driven redaction** | Structured logging via stdlib `log/slog`. Secret material wrapped in types whose `LogValue() → "[redacted]"`. No secret values in errors. Audit log is separate from operational log. | The package takes `*slog.Logger` at construction; emits INFO/WARN/DEBUG records keyed by request UUID; **never** logs a token byte (the token lives in `*SecureBytes` whose `LogValue() → "[redacted]"`; the discordgo session's internal `Token` field is never accessed by package code outside the construction-time `Use(fn)` callback). All sentinel error messages are static category strings (`"discord unavailable"`, `"approval denied"`, `"approval timed out"`, `"rate limited"`, etc.) — no token byte, no scope item, no machine name appears in any `Error()` text. The DM templates render `req.Scope` as a comma-joined list of secret **names** — never values (the package never sees secret values; they live one chunk over in `internal/vault`). Audit-channel mirroring carries the same operator-visible fields as the DM minus action buttons; same redaction rules. The on-disk hash-chained audit log (SDD-13, Layer 6) is the authoritative record — audit-channel is the convenience layer. Test-asserted by `TestBotApprover_TokenAbsentFromAllArtifacts` against a unique sentinel token. ✅ |
| **XI. Native-First, Minimal Dependencies — every new direct dep needs a written justification** | Stdlib first. Every NEW direct dependency requires a written justification covering maintainer activity, supply-chain provenance, transitive dependency footprint, and why no stdlib option suffices. The crypto stack is the only cryptographic dependency surface. | **One new direct dependency**: `github.com/bwmarrin/discordgo` v0.29.0. Stdlib substitution is **not viable** — Discord is not an open protocol, the WebSocket gateway shape and JSON event schema are non-standard and version-controlled by Discord, and re-implementing them is materially worse for security than using the only actively-maintained Go SDK. discordgo is named in `README.md`'s tech stack and in `internal/testutil/discord_stub_test.go`'s import-ban list, both of which presume the production-side import path. The dependency is **non-cryptographic** (the only crypto path through it is the WebSocket TLS handshake, which delegates to stdlib `crypto/tls`); Constitution III's "only cryptographic dependency surface" clause therefore does not apply. Phase-0 research [R-001](./research.md#r-001--discord-sdk-bwmarrin-discordgo) records: (a) maintainer activity (active commits 2025-2026; 4k+ stars; release tags every 2-3 months); (b) supply-chain provenance (single-org GitHub project; one core maintainer + community PRs); (c) transitive footprint (1 transitive dep: `github.com/gorilla/websocket`, itself stdlib-grade); (d) the constitutional XI checklist is satisfied. The implement-commit PR description will document this verbatim. ✅ |

### Other principles (not in scope but checked for non-violation)

- **I (Zero Files at Rest on Agent Machines):** out of scope —
  this package runs on the vault host, not the agent. ✅
- **III (Defense in Depth Through Crypto Layering):** indirectly
  in scope — Layer 5 (mlocked SecureBytes) is consumed for the
  bot token. The package adds no new cryptographic primitive;
  the WebSocket TLS is owned by the SDK + stdlib `crypto/tls`. ✅
- **IV (Supervisor for Daemons, Wrap-Shell for Humans):** the
  package's `SessionType` distinction is the operator-visible
  realisation of this principle (interactive vs `[DAEMON]`
  rendering). ✅
- **VI (Tailscale-Only, Never Public):** out of scope at this
  package level — Discord is outbound from the vault host, not
  inbound. The vault host's Tailscale boundary is enforced by
  `internal/server` (SDD-10). ✅
- **VII (CLI Design Standards):** out of scope. ✅

### Gate result

**PASS** — every principle in scope is satisfied. **One
Complexity Tracking entry** for the new direct dependency
`github.com/bwmarrin/discordgo` (the dependency is justified, not
a deviation, but Constitution XI requires a written record). The
Constitution Check is re-evaluated post-design (after Phase 1)
below.

## Project Structure

### Documentation (this feature)

```text
specs/011-discord-bot/
├── plan.md                  # This file (/speckit-plan command output)
├── research.md              # Phase 0 output — locked HOW decisions
├── data-model.md            # Phase 1 output — types + state machine + bucket model + audit-event catalogue
├── quickstart.md            # Phase 1 output — consumer integration recipe (cmd/hush serve + SDD-12 adapter)
├── contracts/
│   └── api.md               # Phase 1 output — exported API contract (locks PACKAGE-MAP §internal/discord)
├── checklists/              # Pre-existing (untouched by /speckit-plan)
├── spec.md                  # WHAT contract (already written by /speckit-specify + /speckit-clarify)
└── tasks.md                 # Phase 2 output (/speckit-tasks command — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/discord/
├── doc.go                   # Package doc: Constitution II/V/VIII/IX/X/XI citations + Layer-V roster
├── approver.go              # Approver interface + ApprovalRequest + Decision + BotConfig types
├── bot.go                   # BotApprover struct, NewBotApprover constructor, RequestApproval method
├── monitor.go               # WebSocket health monitor + 60s reconnect cap + drain-on-cancel
├── render.go                # DM templates (interactive vs [DAEMON]) + audit-mirror payloads
├── ratelimit.go             # Token-bucket gate (per-(SupervisorName, ClientIP)) with acquire/commit/refund
├── errors.go                # Sentinel error declarations (8 sentinels, static messages)
├── approver_test.go         # Interface conformance + ApprovalRequest validation
├── bot_test.go              # End-to-end (with shim) decision-routing + ctx-cancel + race-clean + token-absence + no-auto-approve-knob
├── monitor_test.go          # Disconnect → unavailable → reconnect; 60s cap; goroutine ctx-cancel termination
├── render_test.go           # Distinct labels + token-absence in templates + supervisor-name in daemon prompt
├── ratelimit_test.go        # Window blocking + per-key isolation + transport-unavailable refund + monotonic clock + zero-default
├── audit_test.go            # All-five-events mirroring + best-effort failure swallowing
└── session_shim_test.go     # Programmable discordgo.Session fake; not imported by production source

go.mod                       # +github.com/bwmarrin/discordgo v0.29.0 (one new direct dep, justified in research [R-001])
go.sum                       # corresponding lockfile additions
```

**Structure Decision**: hush is a single Go module
(`github.com/mrz1836/hush`) with a flat `internal/<domain>` layout
defined in `docs/PACKAGE-MAP.md`. SDD-11 fills the new top-level
domain `internal/discord/`. The package ships seven production
source files; the chunk contract's "Files:" list named five
(`bot.go`, `approver.go`, `monitor.go`, `render.go`, `ratelimit.go`).
The plan adds `errors.go` (sentinel error declarations colocated
for grep-locality, the same locality refinement SDD-08, SDD-09,
SDD-10 each made under the same constitutional reading) and
`doc.go` (package-level doc comment with Constitution citations).
The chunk-contract file list is read as the **minimum** set:
every file the contract names is present, and the package may add
purely declarative files where idiomatic Go discipline calls for
them. No production logic is added beyond what the chunk contract
describes.

The package import path is
`github.com/mrz1836/hush/internal/discord`. Per
`docs/PACKAGE-MAP.md` §Dependency Rules, allowed dependency
direction is `internal/server → internal/discord` (SDD-12 will
adapt `*BotApprover` to satisfy `server.Approver`); the inverse
is forbidden, and the package therefore does NOT import
`internal/server`.

The `session_shim_test.go` file holds a programmable in-memory
`discordgo.Session` shim used by every other test file in the
package. It is `*_test.go` only — the production source files
import `discordgo` directly (the production-side seam is a
narrow `sessionAPI` interface in `bot.go` that `*discordgo.Session`
satisfies structurally). Research
[R-007](./research.md#r-007--testing-without-discord-shim-strategy)
documents the seam shape; it is the same locality refinement
`internal/transport/sign` made for the `NonceCache` test seam.

Tests use `internal/testutil` (SDD-04) for the
`SECRET_SHOULD_NEVER_APPEAR_<n>` sentinel scaffolding and the
`AssertSentinelAbsent(t, sentinel, haystack)` helper — adapted
for bot tokens by injecting a unique 64-character sentinel
through the SecureBytes wrapper at test setup, exercising every
public entry point, and asserting absence across the captured
slog buffer + every `err.Error()` string + every audit-event
payload.

## Post-Design Constitution Re-check

Re-evaluated after Phase 1 design artifacts (`research.md`,
`data-model.md`, `contracts/api.md`, `quickstart.md`) were
drafted:

| Principle | Phase 1 introduced | Re-check |
|-----------|--------------------|----------|
| **II** | [data-model.md](./data-model.md) formalises the `BotApprover` state machine (Available ↔ Unavailable) and proves no transition labelled "auto-approve" exists. [contracts/api.md](./contracts/api.md) documents that no exported function returns `Decision{Approved: true}` except via the operator-Approve interaction path. The AST-walk test `TestBotApprover_NoAutoApproveKnobExists` is in the test floor. | PASS — the no-auto-approve invariant is enforced by code shape, not by an additional in-band guard. |
| **V** | The five lifecycle events (`request_received`, `approved`, `denied`, `timed_out`, `rate_limited`) are catalogued in [data-model.md](./data-model.md) with their field shapes, and the audit-channel mirroring path is locked in [contracts/api.md](./contracts/api.md) as best-effort and non-blocking. The two distinct DM template prefixes (green-checkmark vs warning glyph) are visible in [data-model.md](./data-model.md) §"DM rendering". | PASS — operator-visibility surface is enumerated and tested. |
| **VIII** | [contracts/api.md](./contracts/api.md) enumerates 26+ named tests across 6 test files. Coverage gate is ≥85% per the Medium band. | PASS — every spec FR and every spec SC has at least one named test. |
| **IX** | Phase 1 confirmed: zero `init()`, zero mutable globals beyond the documented sentinel-class `var Err...`, all primitives ctx-first, every goroutine owned by the constructor's ctx with a documented termination condition. The `Approver` single-method interface is declared at the consumer side (this package consumes the abstraction; SDD-12 will declare its own `server.Approver` for its own consumer-side use). | PASS — no new violations introduced. |
| **X** | The error catalogue is finalised. Every sentinel's identity is the failure category; no error message embeds token bytes, machine names, scope items, or any other request-content. Bot-token bytes flow through `*SecureBytes` whose `LogValue() → "[redacted]"`. The `TestBotApprover_TokenAbsentFromAllArtifacts` test scaffolding is in the test floor and walks the captured slog buffer + all `err.Error()` strings + all audit payloads against a unique sentinel token. | PASS — diagnostic surfaces audited and clean. |
| **XI** | One new direct dependency (`github.com/bwmarrin/discordgo` v0.29.0); the `go.mod` diff for the implement commit is one line. The justification is recorded in research [R-001](./research.md#r-001--discord-sdk-bwmarrin-discordgo); the implement-phase PR description re-asserts it. The dependency is non-cryptographic — Constitution III's "only cryptographic dependency surface" clause does not apply. | PASS — Complexity Tracking entry recorded; constitutional process honoured. |

**Final result**: PASS. **One Complexity Tracking entry** for
the new direct dependency `github.com/bwmarrin/discordgo`. No
constitutional deviations.

## Complexity Tracking

> Fill ONLY if Constitution Check has violations that must be justified.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| **+1 direct dependency: `github.com/bwmarrin/discordgo` v0.29.0** (Constitution XI requires a written justification for every new direct dep — recorded as a "complexity entry" per the constitutional process, even when the dep is justified rather than a deviation) | The package's job is to talk to Discord's gateway and REST API. Discord is not an open protocol; its WebSocket gateway shape and event JSON schema are non-standard and version-controlled by Discord. Re-implementing the SDK against Discord's published HTTP+WebSocket surface using stdlib only would be ~2000+ LOC of high-risk parsing and protocol code, all of it uniquely Discord's, all of it exposed to a hostile network, and all of which would need its own fuzz harness. The `bwmarrin/discordgo` SDK is already named in `README.md`'s tech stack and in `internal/testutil/discord_stub_test.go`'s import-ban list, presuming the production-side import path. | Stdlib + hand-rolled gateway client: rejected — re-implementing Discord's protocol is materially worse for security than depending on the actively-maintained Go SDK with 4k+ stars and a community fuzz history. Other Go Discord SDKs (`disgord`, `arikawa`, `diamondburned/arikawa`): rejected — `discordgo` has the longest maintenance history (since 2015), the largest community (4k+ stars vs <500), and the simplest API for the narrow surface this package needs (DM send + interaction handler + connection events). The trusted-sources hierarchy walk: stdlib (no Discord), sigil baseline (no Discord), `bsv-blockchain` org (no Discord), wider ecosystem → `bwmarrin/discordgo` is the obvious choice. |
