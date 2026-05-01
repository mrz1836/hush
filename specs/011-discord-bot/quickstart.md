# Quickstart — `internal/discord` (SDD-11)

Consumer-side integration recipe. Two consumers are anticipated
in the v0.1.0 build:

1. **`cmd/hush serve`** (SDD-14 wiring) — constructs a
   `*BotApprover` at server startup and passes it to the SDD-10
   chassis via the SDD-12 adapter.
2. **Test harnesses** — every package that wants approval
   semantics in tests uses the `internal/testutil`
   `DiscordStub` (SDD-04) instead of `*BotApprover`.

This document shows both shapes.

---

## 1. Server-side wiring (`cmd/hush serve`)

```go
package main

import (
    "context"
    "log/slog"
    "os/signal"
    "syscall"
    "time"

    "github.com/mrz1836/hush/internal/config"
    "github.com/mrz1836/hush/internal/discord"
    "github.com/mrz1836/hush/internal/keychain"     // SDD-05; loads token from OS keychain into SecureBytes
    "github.com/mrz1836/hush/internal/server"       // SDD-10 chassis
    "github.com/mrz1836/hush/internal/serverwiring" // SDD-12; the adapter from server.Approver to discord.Approver
)

func runServe(cfg *config.Server, logger *slog.Logger) error {
    // ctx owns every long-lived component for this server lifetime.
    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
    defer cancel()

    // Step 1: load the bot token from the OS keychain into SecureBytes.
    // The keychain helper (SDD-05) returns a *securebytes.SecureBytes
    // that the caller owns and may Destroy when no longer needed.
    botToken, err := keychain.LoadSecureBytes(ctx, cfg.Discord.BotTokenKeychainItem)
    if err != nil {
        return fmt.Errorf("load bot token: %w", err)
    }

    // Step 2: construct the BotApprover.
    // ctx owns the monitor goroutine; cfg.Token is consumed once
    // by NewBotApprover and may be Destroyed afterwards.
    approver, err := discord.NewBotApprover(ctx, discord.BotConfig{
        Token:          botToken,
        OwnerID:        cfg.Server.DiscordOwnerID,
        AppID:          cfg.Discord.ApplicationID,
        AuditChannelID: cfg.Server.DiscordAuditChannelID, // "" disables mirroring
        DMRateLimit:    5 * time.Minute,                  // explicit; ≤0 falls back to DefaultDMRateLimit
    }, logger)
    if err != nil {
        return fmt.Errorf("construct bot approver: %w", err)
    }
    botToken.Destroy() // package keeps no reference to the SecureBytes; the SDK has its own internal copy

    // Step 3: adapt *discord.BotApprover to server.Approver.
    // The adapter (SDD-12) lives in internal/serverwiring or
    // internal/server itself; it translates server.ApprovalRequest
    // to discord.ApprovalRequest and discord.Decision to
    // server.Decision. It is not part of this package.
    serverApprover := serverwiring.NewDiscordApproverAdapter(approver)

    // Step 4: hand the adapter to the chassis.
    chassis, err := server.New(server.Deps{
        Cfg:         cfg,
        Approver:    serverApprover,
        // ... other deps per SDD-10
    })
    if err != nil {
        return fmt.Errorf("server.New: %w", err)
    }

    // Step 5: run. ctx cancellation propagates through chassis →
    // approver → monitor goroutine; everything shuts down cleanly.
    return chassis.Run(ctx)
}
```

**Lifecycle notes:**

- `NewBotApprover` does not block on the gateway connection.
  If Discord is unreachable at boot, the constructor returns
  successfully; the approver enters the unavailable state; the
  chassis starts; new claims receive `ErrDiscordUnavailable`
  (mapped to HTTP 503 by SDD-12) until the monitor's reconnect
  loop succeeds (FR-013a).
- The single `ctx` passed to both `NewBotApprover` and
  `chassis.Run` is the lifecycle context. Cancelling it (e.g.,
  via `signal.NotifyContext`) closes everything: the monitor
  goroutine drains, the session closes, in-flight `RequestApproval`
  calls unblock with `ErrDiscordUnavailable`, the chassis
  shuts down its HTTP listener.
- The bot token's `*SecureBytes` may be Destroyed immediately
  after `NewBotApprover` returns. The SDK's internal `string`
  copy persists for the session's lifetime — see research
  [R-003](./research.md#r-003--bot-token-ingestion-through-securebytes)
  for the residual-risk discussion.

---

## 2. SDD-12 adapter (forward reference)

This adapter does NOT live in `internal/discord` — SDD-12 owns
it. Sketched here for context.

```go
package serverwiring // illustrative; final package name TBD by SDD-12

import (
    "context"

    "github.com/mrz1836/hush/internal/discord"
    "github.com/mrz1836/hush/internal/server"
    "github.com/mrz1836/hush/internal/token"
)

type discordApproverAdapter struct {
    inner *discord.BotApprover
}

func NewDiscordApproverAdapter(inner *discord.BotApprover) server.Approver {
    return &discordApproverAdapter{inner: inner}
}

func (a *discordApproverAdapter) RequestApproval(
    ctx context.Context,
    req server.ApprovalRequest,
) (server.Decision, error) {
    discordReq := discord.ApprovalRequest{
        MachineName:    req.MachineName,
        ClientIP:       req.ClientIP.String(),
        Reason:         req.Reason,
        Scope:          req.Scope,
        RequestedTTL:   req.RequestedTTL,
        SessionType:    serverSessionTypeToToken(req.SessionType),
        SupervisorName: req.Metadata["supervisor_name"],
    }

    decision, err := a.inner.RequestApproval(ctx, discordReq)
    if err != nil {
        return server.Decision{}, err // sentinels propagate verbatim; SDD-12 maps to HTTP status
    }

    return server.Decision{
        Approved:    decision.Approved,
        ApprovedAt:  time.Now(),
        GrantedTTL:  decision.ApprovedTTL,
        ApproverID:  "discord",          // SDD-12 may populate from the bot config
        Reason:      decision.Reason,
    }, nil
}

func serverSessionTypeToToken(s server.SessionType) token.SessionType {
    switch s {
    case server.SessionInteractive:
        return token.SessionInteractive
    case server.SessionSupervisor:
        return token.SessionSupervisor
    default:
        return "" // SDD-12 will reject this earlier in the chassis flow
    }
}
```

**Responsibility split**:

- `internal/discord` owns the bot, the rendering, the rate
  limit, the monitor.
- The adapter (SDD-12) owns the type translation and the
  HTTP-status mapping (`ErrDiscordUnavailable` → 503,
  `ErrApprovalDenied` → 403, `ErrApprovalTimeout` → 408,
  `ErrRateLimited` → 429).
- The chassis (`internal/server`, SDD-10) owns the request
  routing, audit-event emission, and graceful shutdown.

---

## 3. Test-harness recipe

Production tests that need approval semantics MUST NOT
construct a `*BotApprover` — that requires a real Discord
session, which violates the chunk's "no live Discord in tests"
rule and is far heavier than necessary.

### 3.1 SDD-04's `DiscordStub` (already exists)

```go
import "github.com/mrz1836/hush/internal/testutil"

func TestSomething(t *testing.T) {
    stub := testutil.NewDiscordStub(t)
    stub.ApproveAll = true // tail default after queue exhausted

    // hand stub to the system under test, which expects
    // testutil.Approver (an alias for the SDD-04 narrow surface).
    sut := newSomething(stub)
    // ...
}
```

The `testutil.DiscordStub` satisfies the
`testutil.Approver` interface — which is the SDD-04 narrow
surface. It is NOT the `discord.Approver` declared at SDD-11.
Tests that need the SDD-11 surface use the package-internal
`session_shim_test.go` shim documented next.

### 3.2 Package-internal shim (this package's tests)

Tests inside `internal/discord/` use the
`session_shim_test.go` programmable `*discordgo.Session` fake
(research [R-007](./research.md#r-007--testing-without-discord-shim-strategy)).
Sketch:

```go
// session_shim_test.go (test-only)

type sessionShim struct {
    sentMessages chan *discordgo.MessageSend
    handlers     map[string]interface{}
    // ...
}

func (s *sessionShim) Open() error  { return nil }
func (s *sessionShim) Close() error { return nil }

func (s *sessionShim) UserChannelCreate(userID string) (*discordgo.Channel, error) {
    return &discordgo.Channel{ID: "dm:" + userID}, nil
}

func (s *sessionShim) ChannelMessageSendComplex(
    channelID string, data *discordgo.MessageSend,
) (*discordgo.Message, error) {
    s.sentMessages <- data
    return &discordgo.Message{ID: fakeMessageID()}, nil
}

func (s *sessionShim) AddHandler(handler interface{}) func() {
    name := handlerName(handler)
    s.handlers[name] = handler
    return func() { delete(s.handlers, name) }
}

// Test helpers (synchronous):

func (s *sessionShim) TriggerInteractionCreate(customID string, ...) { /* ... */ }
func (s *sessionShim) TriggerDisconnect()                           { /* ... */ }
func (s *sessionShim) TriggerReady()                                { /* ... */ }
```

The package's production source uses an unexported
`sessionAPI` field whose interface methods match the shim;
production assigns `*discordgo.Session` (which satisfies the
interface structurally); tests use `newBotApproverWithSession`
(a package-private constructor) to inject the shim.

### 3.3 Sentinel-token redaction test

```go
func TestBotApprover_TokenAbsentFromAllArtifacts(t *testing.T) {
    // Inject a unique 64-character sentinel as the bot token.
    sentinel := testutil.SentinelSecret(11) // SECRET_SHOULD_NEVER_APPEAR_11
    tokenSB, err := securebytes.New([]byte(sentinel))
    require.NoError(t, err)

    // Capture slog output.
    var buf bytes.Buffer
    logger := slog.New(slog.NewJSONHandler(&buf, nil))

    ctx, cancel := context.WithCancel(context.Background())
    t.Cleanup(cancel)

    shim := newSessionShim()
    a, err := newBotApproverWithSession(ctx, BotConfig{
        Token:   tokenSB,
        OwnerID: "owner123",
        AppID:   "app456",
    }, logger, shim)
    require.NoError(t, err)

    // Exercise every public entry point.
    shim.TriggerReady()
    _, err = a.RequestApproval(ctx, ApprovalRequest{
        MachineName: "darwin", ClientIP: "100.96.10.4",
        Reason: "test", Scope: []string{"X"},
        RequestedTTL: 1 * time.Hour, SessionType: token.SessionInteractive,
    })
    // ...

    // Assert sentinel absent everywhere.
    testutil.AssertSentinelAbsent(t, sentinel, buf.String())
    if err != nil {
        testutil.AssertSentinelAbsent(t, sentinel, err.Error())
    }
    // Also walk every audit-channel payload sent through the shim.
    // ...
}
```

---

## 4. Common pitfalls

- **Holding `cfg.Token` after `NewBotApprover` returns**: the
  SDK has its own copy; you don't need yours. Destroy it.
- **Reusing the same `ctx` for both `NewBotApprover` and a
  per-request `ctx`**: don't. The `NewBotApprover` `ctx` is
  the lifecycle context; the per-request `ctx` is a child
  scoped to one approval. Cancelling the lifecycle ctx
  drains all in-flight requests; cancelling a per-request ctx
  cancels only that one.
- **Forgetting to handle `ErrApprovalTimeout` and
  `context.DeadlineExceeded` symmetrically**: `errors.Is(err,
  ErrApprovalTimeout)` AND `errors.Is(err,
  context.DeadlineExceeded)` both evaluate true on the
  timeout path. Choose whichever is clearest at the
  consumer.
- **Mapping `ErrRateLimited` to 503**: don't.
  `ErrRateLimited` maps to HTTP 429 (Too Many Requests).
  503 is reserved for `ErrDiscordUnavailable` (the
  transport-down case).
- **Treating `Decision{Approved: false}` as a denial**:
  `Approved: false` only appears when the function returned a
  non-nil error. The error tells you which kind of non-approval
  it is. Always check `err != nil` first; only inspect
  `Decision` when `err == nil`.

---

## 5. Operational sanity check

After wiring is complete, run the SDD-11 verification suite
from repo root:

```bash
# Race-clean tests + coverage
magex test:race
go test -cover ./internal/discord/

# Lint + format gates
magex format:fix
magex lint

# Manual sentinel-leak audit (the test asserts this, but a fresh
# greppable check is good hygiene before tagging):
grep -r "SECRET_SHOULD_NEVER_APPEAR" internal/discord/  # should match only test sources
```

The implement-phase release-step list (chunk contract Prompt 5)
adds explicit `magex test:race`, lint, format, and
coverage gates. The `magex` workflow is the authoritative
invocation; `go test -cover` is the spot-check.
