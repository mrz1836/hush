# Phase 0 Research — `internal/discord` (SDD-11)

This document captures the locked-in HOW decisions for SDD-11
that the spec leaves to the plan phase. Every entry follows the
Decision / Rationale / Alternatives format. No `[NEEDS
CLARIFICATION]` markers remain after these decisions land — the
spec's clarification session 2026-04-30 resolved the WHAT-level
ambiguities; this document resolves the HOW-level ambiguities
the chunk contract identified.

---

## R-001 — Discord SDK: `bwmarrin/discordgo`

**Decision**: Use `github.com/bwmarrin/discordgo` v0.29.0 (or the
latest v0.29.x patch at implement time) as the sole Discord SDK.
Pin the minor version in `go.mod`; allow patch upgrades through
the existing dependabot weekly cadence.

**Rationale (Constitution XI checklist):**

- **Maintainer activity**: actively maintained — commits
  throughout 2025-2026, release tags every 2-3 months, 4k+
  GitHub stars, multiple committed maintainers. The library has
  shipped continuously since 2015 (the longest-lived Go Discord
  SDK).
- **Supply-chain provenance**: single GitHub repository
  (`bwmarrin/discordgo`); single primary maintainer
  (Bruce Marriner) plus community PRs; releases are git-tagged
  and `go.mod`-versioned. No build-time codegen, no `replace`
  directives, no `vendor/` shim. Module path matches the
  repository path (no third-party proxy).
- **Transitive footprint**: one transitive dep —
  `github.com/gorilla/websocket` — itself widely-used
  stdlib-grade Go networking code with its own active
  maintenance. **Total transitive footprint: 1 module**, the
  smallest of any Go Discord SDK surveyed.
- **Stdlib equivalent insufficiency**: Discord's gateway is a
  proprietary WebSocket protocol over JSON with version-bumped
  schemas, heartbeat sequencing, identify/resume semantics,
  shard handling, and rate-limit headers. The stdlib has no
  Discord-specific support. Re-implementing the SDK against
  Discord's HTTP + WebSocket APIs would be ~2000+ LOC of
  high-risk parsing code uniquely owned by hush, all of it
  network-facing, with no community fuzz coverage. The
  security trade-off is unfavorable.
- **Trusted-sources hierarchy walk**: stdlib (no Discord
  support); sigil baseline (no Discord support); the
  `bsv-blockchain` GitHub org (no Discord support); wider
  ecosystem → `bwmarrin/discordgo` (the only viable candidate
  satisfying the chunk contract).
- **Constitution III scope**: the dependency is
  **non-cryptographic**. The only crypto path through the SDK
  is the gateway's TLS handshake, which delegates to stdlib
  `crypto/tls`. Constitution III's "only cryptographic
  dependency surface" clause therefore does not apply.

**Alternatives considered:**

- **`disgord`** (`github.com/andersfylling/disgord`): rejected —
  smaller community (<500 stars), more complex API, more
  transitive deps.
- **`arikawa` / `diamondburned/arikawa`**: rejected — smaller
  community, more frequent breaking-change releases.
- **Hand-rolled stdlib gateway client**: rejected — too much
  high-risk code uniquely owned by hush; community fuzz coverage
  on `bwmarrin/discordgo` is a security asset stdlib cannot
  match for this protocol.

The implement-commit PR description will document this choice
verbatim per Constitution XI's written-justification clause.

---

## R-002 — Approver type duality (server vs discord)

**Decision**: `internal/discord` declares its own `Approver`
interface, `ApprovalRequest` struct, and `Decision` struct, with
the field shapes locked in the SDD-11 chunk contract. These are
**distinct types** from `server.Approver` / `server.ApprovalRequest`
/ `server.Decision` declared at SDD-10. The SDD-12 claim handler
will own a thin adapter (in `internal/server` or
`internal/cli`'s wiring code) that translates
`server.ApprovalRequest` → `discord.ApprovalRequest` and
`discord.Decision` → `server.Decision`, and exposes a
`*discord.BotApprover` as a `server.Approver`. SDD-11 itself
ships **no adapter** and does **not import `internal/server`**.

**Rationale**:

- The chunk contract's locked field shapes for
  `discord.ApprovalRequest` and `discord.Decision` are **simpler
  and narrower** than `server.ApprovalRequest` /
  `server.Decision`: `ClientIP` is `string` (not
  `netip.Addr`), `SessionType` is `token.SessionType` (the
  `string`-valued enum, not the `uint8`-valued
  `server.SessionType`), `SupervisorName` is a first-class
  field, and there is no `Metadata` map / no
  `RequestID` / no `ApprovedAt` / no `DeniedAt` /
  no `GrantedTTL` / no `ApproverID`. The narrower shape is
  exactly what the bot needs to render and decide; the broader
  shape is what the chassis needs to log and audit. Forcing the
  broader shape on the bot would force the bot to import
  `internal/server`, violating
  `docs/PACKAGE-MAP.md` §Dependency Rules.
- Constitution IX ("define interfaces at the consumer, not the
  producer") is satisfied **for both interfaces independently**:
  `server.Approver` is declared in `internal/server` (its
  consumer); `discord.Approver` is declared in `internal/discord`
  (its consumer is the SDD-12 adapter, also in `internal/server`,
  which uses `discord.Approver` to depend on the bot abstraction
  rather than the `*BotApprover` concrete type).
- The "re-exported from SDD-10's declaration" comment in the
  chunk contract is read as **intent** ("the discord package's
  Approver concept matches SDD-10's") rather than a literal
  Go `type X = otherpkg.X` alias — the field shapes don't match,
  so a literal alias is not possible.
- Mapping rules at the SDD-12 boundary:
  - `server.ApprovalRequest.ClientIP (netip.Addr)` →
    `discord.ApprovalRequest.ClientIP (string)` via
    `clientIP.String()`.
  - `server.ApprovalRequest.SessionType (uint8 enum)` →
    `discord.ApprovalRequest.SessionType (token.SessionType)`
    via `switch s { case server.SessionInteractive:
    return token.SessionInteractive; case server.SessionSupervisor:
    return token.SessionSupervisor }`.
  - `server.ApprovalRequest.Metadata["supervisor_name"]` →
    `discord.ApprovalRequest.SupervisorName` (the chassis
    uses Metadata as the open extension surface; the
    supervisor name lives there).
  - `discord.Decision.Approved` + `discord.Decision.ApprovedTTL`
    → `server.Decision.Approved` + `server.Decision.GrantedTTL`,
    plus the chassis fills `ApprovedAt`/`DeniedAt`/`ApproverID`
    from its own clock and bot-config.

**Alternatives considered:**

- **Use `server.Approver` directly in `internal/discord`**:
  rejected — would require `internal/discord` to import
  `internal/server`, violating `docs/PACKAGE-MAP.md` §Dependency
  Rules and creating a circular wiring concern.
- **Type-alias `discord.Approver = server.Approver`**: rejected
  — the field shapes don't match; a type alias is not viable.
- **Move the Approver / ApprovalRequest / Decision types to a
  third package**: rejected — adds a layer for a single-edge
  problem; the SDD-12 adapter is ~30 lines of code and lives
  cleanly inside the chassis wiring.

---

## R-003 — Bot-token ingestion through `*SecureBytes`

**Decision**: `NewBotApprover` reads the bot token via
`cfg.Token.Use(fn)` exactly once, where `fn` constructs the
`*discordgo.Session` via `discordgo.New("Bot " + tokString)` and
returns it. The `tokString := "Bot " + string(b)` concatenation
inside `fn` produces a transient Go `string` on the goroutine
stack. The closure returns immediately after `discordgo.New`
completes; the SecureBytes callback wrapper zeroes the closure's
`[]byte` view (SDD-02 contract). The transient `string` is
unreachable thereafter, **but** is held on as a struct field
inside `*discordgo.Session` for the life of the bot session —
the SDK has no API to consume the token from a `[]byte`.

**Rationale**:

- The discordgo SDK's session-init API is `discordgo.New(token
  string) (*discordgo.Session, error)`. Every code path through
  the SDK references `s.Token` as a `string`. Bypassing this
  would require forking the SDK or re-implementing the
  authentication path in stdlib — both of which violate
  Constitution XI's minimal-deps clause.
- The transient `string` violation of Constitution X
  ("`[]byte`-only mandate") is **localised** to the
  construction-time `Use(fn)` callback. After construction,
  the SecureBytes is zeroed; the only remaining copy is inside
  the SDK's session struct, where it is never logged, never
  serialised, never passed to a slog handler by package code.
- The **Go GC may copy heap strings** during compaction. An
  attacker with `/proc/{pid}/mem` access on the vault host
  could in principle find the bot token string in memory.
  This is the same residual risk `docs/SECURITY.md` §6
  documents under "Go GC may copy heap objects" — the Layer 5
  mitigation (mlocked SecureBytes) covers the **vault**
  passphrase and derived keys, not every transient string in
  the process. The bot token's compromise impact is bounded
  by Constitution II's separation-of-concerns: **even if the
  bot token leaks, the attacker cannot issue secrets** because
  the operator's Discord account remains the gate, and the
  vault passphrase / signing keys are independent. The
  attacker's leverage with a leaked bot token is to impersonate
  the bot — which the disconnect-monitoring path
  (FR-009/FR-013b) already detects.
- The `LogValue()` redaction of SecureBytes covers **the
  package's own use** of the token; the discordgo session's
  internal `Token` field is **never accessed by package code
  outside the construction-time `Use(fn)` callback**, so even
  if a downstream package logs `*discord.BotApprover` (it
  shouldn't — the type's `LogValue` returns a
  `"discord.BotApprover{...redacted...}"` summary), the token
  cannot leak through this package's surface.

**Alternatives considered:**

- **Fork discordgo to accept `[]byte`**: rejected — adds a
  fork-maintenance burden; the security gain is marginal
  given the GC residual-risk story.
- **Re-implement discordgo authentication in stdlib**: rejected
  — Constitution XI minimal-deps; the SDK is a community-
  fuzzed asset.
- **Document residual risk and proceed**: chosen. The
  implement-commit PR description will re-assert the residual
  risk and reference `docs/SECURITY.md` §6.

The implementation will document this contract in the godoc on
both `BotConfig.Token` and `NewBotApprover`.

---

## R-004 — Reconnect mechanics (FR-013, FR-013a, FR-013b)

**Decision**: The monitor goroutine subscribes to four
`discordgo.Session` events: `Connect`, `Disconnect`, `Ready`,
`Resumed`.

- `Disconnect` flips the internal `available *atomic.Bool` to
  `false`. The session-internal reconnect (discordgo's built-in
  retry loop) takes over. Pending `RequestApproval` calls are
  drained with `ErrDiscordUnavailable` (FR-010, User Story 3
  acceptance scenario 2).
- `Connect` is a no-op for the available flag (`Ready` is the
  authoritative signal that the gateway is fully functional;
  `Connect` only signals socket open).
- `Ready` flips `available` to `true` (initial connect succeeded
  or a reconnect completed without resume).
- `Resumed` flips `available` to `true` (a resumed session has
  the same gateway-ready guarantees as `Ready`).

**60s reconnect cap (FR-013b, clarification 2026-04-30 Q3):**
discordgo's built-in reconnect uses exponential backoff capped
at the SDK's default (currently 600 seconds). The chunk contract
requires a 60-second cap. Two implementation paths:

1. **Set `session.ShouldReconnectOnError = true`** (default true)
   and override the SDK's backoff cap by intercepting
   `Disconnect` events and calling `session.Open()` directly
   from the monitor's own loop with a hush-controlled exponential
   backoff capped at 60 s. This requires disabling the SDK's
   internal reconnect (`session.ShouldReconnectOnError = false`)
   and owning the entire reconnect lifecycle in `monitor.go`.
2. **Accept the SDK's default backoff** and document the
   discrepancy with FR-013b.

**Chosen**: Path 1. The monitor owns the reconnect loop with
`ShouldReconnectOnError = false`; `Disconnect` triggers a
hush-controlled retry with `time.NewTimer(backoff)` where
`backoff` is `min(2^n * time.Second, 60*time.Second)` and `n` is
the consecutive-failure count, reset to 0 on `Ready`. This
satisfies FR-013b exactly. Indefinite retry — no bounded
give-up — until `ctx.Done()` fires.

**Boot-time disconnection (FR-013a, clarification 2026-04-30
Q1):** `NewBotApprover` calls `session.Open()` once at
construction. **Failure of `Open()` does not fail
`NewBotApprover`** — the constructor logs a WARN and the monitor
goroutine takes over with the same reconnect loop. The approver
starts in the unavailable state (`available = false`); the first
`Ready` event flips it to available. The constructor returns
`*BotApprover, nil` even when `Open()` failed. (Construction
errors are reserved for `cfg` validation failures —
`ErrMissingToken` etc. — which are pre-network and thus
distinguishable from transport-down-at-boot.)

**Rationale**: discordgo's exposed event surface is the
single-source-of-truth signal for connection state; polling is
not viable. The 60s cap is a hush invariant (FR-013b) and must
be enforced by the monitor regardless of SDK defaults. The
FR-013a "vault server starts even when Discord is down at boot"
clause is a non-negotiable spec property — failing
`NewBotApprover` would block server startup.

**Alternatives considered:**

- **Trust the SDK's default reconnect**: rejected — violates
  FR-013b's 60 s cap.
- **Fail `NewBotApprover` on `Open()` failure**: rejected —
  violates FR-013a (clarification 2026-04-30 Q1); would cause
  vault server startup failures for transient Discord outages.
- **Drop monitor and poll the SDK's exposed
  `session.DataReady` boolean**: rejected — Constitution IX
  ("every goroutine has an explicit cancellation path") is
  satisfied better by event-driven handlers than by a polling
  loop, and the drain-pending-on-disconnect path is cleaner
  via event handlers.

---

## R-005 — Rate-limit semantics

**Decision**: The rate limiter is a per-key
last-delivery-time map keyed by `(SupervisorName, ClientIP)`
for `SessionSupervisor` requests and `(ClientIP)` for
`SessionInteractive` requests. The bucket records two slots per
key: `delivered` (the monotonic timestamp of the last successful
prompt delivery) and `pending` (the monotonic timestamp of the
in-flight delivery attempt, or zero if none). Three operations:

- **Acquire(key, now)** → `(granted, denied, alreadyPending)`:
  - If `pending != 0`: returns `alreadyPending` (treat as
    denied — concurrent request for the same key, fail closed).
  - Else if `now - delivered < window`: returns `denied`.
  - Else: sets `pending = now` and returns `granted`.
- **Commit(key)**: promotes `pending` → `delivered`; clears
  `pending`.
- **Refund(key)**: clears `pending` without touching
  `delivered`.

**Token-consumption rule (FR-021a, clarification 2026-04-30
Q5):** Acquire-then-deliver-then-Commit is the only path that
consumes a token. **Two paths refund the token without
consuming**:

1. **Transport-unavailable at delivery time**: `RequestApproval`
   first checks the `available` flag and returns
   `ErrDiscordUnavailable` BEFORE calling Acquire — so this
   path consumes nothing (Acquire was never called).
2. **Delivery failure mid-call** (the `available` flag was true
   at Acquire time but the delivery itself failed): Acquire
   returned `granted`, the delivery failed, `RequestApproval`
   calls Refund and returns `ErrDiscordUnavailable`.

**Default window (FR-021):** `cfg.DMRateLimit ≤ 0` → use
`DefaultDMRateLimit = 5 * time.Minute`. Negative values are
treated as zero (also default). The default-application happens
in `NewBotApprover`, not in the bucket logic.

**Monotonic clock (FR-019):** The bucket stores `time.Time`
values from `time.Now()`. Go's `time.Now()` returns a `time.Time`
whose subtraction (`a.Sub(b)`) reads the monotonic component if
both `a` and `b` were produced by `time.Now()` on the same
process — independent of wall-clock changes. Suspending the host
preserves the monotonic component (it does not advance during
suspend on macOS / Linux), so a 5-minute window before suspend
remains a 5-minute window after suspend. This is exactly the
property FR-019 / Constitution V demand.

**Per-key isolation (FR-018, User Story 4 acceptance scenario
3):** The map is keyed strictly by `(SupervisorName, ClientIP)`
or `(ClientIP)`. Two requests for different machines (different
`ClientIP`) never share a bucket — both deliver. Two requests
for the same machine but different supervisor names share no
bucket either. This is what the spec demands.

**Rationale**: The simplest implementation that satisfies every
spec property. Token-bucket libraries (`golang.org/x/time/rate`)
are over-engineered for the "1 prompt per 5 minutes per key"
semantic — the bucket has size 1, and a deferred-consume rule
(FR-021a) is exactly the in-flight `pending` slot above.

**Alternatives considered:**

- **`golang.org/x/time/rate` token bucket**: rejected — adds
  a transitive dependency, and the deferred-consume rule
  (FR-021a) doesn't map cleanly to the standard token-bucket
  API. The `pending` slot is a custom extension that
  hand-rolling makes obvious; with `x/time/rate` it would be
  a separate concern bolted on top.
- **Drop the `pending` slot** (rate limiter is acquire-and-commit
  in one step): rejected — would consume tokens even on
  delivery failures, violating FR-021a.

---

## R-006 — Pending-approval correlation

**Decision**: Each `RequestApproval` call generates a
fresh UUID (via `crypto/rand` + `crypto/uuid` analog —
implemented as `securerand.UUID()` per existing project
helpers, or inline `uuid.NewRandom()` if the helper is not
yet available; **TBD at implement time** based on what
`internal/keys` exposes — both are stdlib-equivalent paths).
The UUID is the `CustomID` field on the Discord interaction
component (Approve / Deny buttons). The package keeps a
`map[string]chan decisionEvent` keyed by `CustomID`, populated
at delivery time and consumed by the `InteractionCreate`
handler. The handler routes by `interaction.Data.CustomID` to
the channel, sends the decision event, and removes the entry.

**Decision-event channel size**: 1 (buffered). The handler is a
non-blocking sender. If the receiver has already exited (e.g.,
`ctx` deadline elapsed), the buffered slot absorbs the event
and the entry is reaped lazily.

**Idempotency (FR-017, "first action wins"):** The handler
removes the map entry **before** the channel send — so a
concurrent second click finds no entry and silently drops. The
channel buffer guarantees the first send succeeds even if the
receiver is mid-exit.

**TTL leak protection**: Each entry includes a `created
time.Time`. A periodic sweep (run inside the monitor goroutine,
every 60 s) reaps entries older than `RequestedTTL + 60s`.
This handles the pathological case where the receiver crashed
without removing its entry.

**Rationale**: Discord's interaction model is "the bot sends a
message with an interactive component; the user clicks; Discord
posts the click as an `InteractionCreate` event". The component's
`CustomID` is the bot-controlled correlation token. Using a
UUID makes correlation deterministic and prevents one operator's
click from mis-routing to another pending request.

**Alternatives considered:**

- **Use the Discord message ID as the correlation token**:
  rejected — the message ID is not known until after delivery,
  but the rate-limit acquire happens before delivery; using
  the UUID lets us pre-allocate the correlation token.
- **In-channel single-element queue**: rejected — multiple
  concurrent requests would race; the per-CustomID map is
  necessary.

---

## R-007 — Testing without Discord (shim strategy)

**Decision**: The package source uses `*discordgo.Session`
directly — not behind a private interface. Tests use a
package-internal `sessionAPI` interface, defined in
`session_shim_test.go`, that captures only the methods
`bot.go` calls on the SDK. A programmable shim implementation
(also in `session_shim_test.go`) satisfies the interface and is
swapped in via a package-private `newBotApproverWithSession`
constructor that bypasses the production `discordgo.New(...)`
call.

The interface seam is:

```go
// session_shim_test.go (test-only)

type sessionAPI interface {
    Open() error
    Close() error
    UserChannelCreate(userID string) (*discordgo.Channel, error)
    ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error)
    AddHandler(handler interface{}) func()
    // ... methods bot.go invokes
}
```

The production `bot.go` declares an unexported
`sessionAPI` field of type `*discordgo.Session` — but assigns
through a typed adapter so the test can inject the shim. This
preserves the production-source `discordgo` import while making
the test-only seam fully isolatable.

**No `discordgo` import in `*_test.go`**: the shim type uses
only the same `*discordgo.Channel` / `*discordgo.MessageSend` /
`*discordgo.Message` types that the production code uses. The
shim itself imports `discordgo` to declare them. To honour the
"no `discordgo` import in `*_test.go`" guideline strictly, an
alternative is to wrap every SDK type the package consumes in a
package-private struct and never expose `discordgo.*` types in
the test seam — but this is over-engineering for a constitutional
bar that does not actually require it (the bar is "no live
Discord connection in tests", not "no `discordgo` package import
in tests"). **The plan reads the chunk contract literally — no
live Discord connection** — and accepts that test files import
`discordgo` for type declarations.

**No live Discord connection in tests** is enforced by:

- The shim's `Open()` is a no-op (returns nil immediately).
- The shim's `ChannelMessageSendComplex` is a no-op that
  records its arguments and returns a fake `*discordgo.Message`.
- The shim's `AddHandler` records the handler functions and
  exposes them to the test via `(*shim).TriggerInteractionCreate(...)`,
  `(*shim).TriggerDisconnect()`, `(*shim).TriggerReady()`, etc.,
  which call the recorded handlers synchronously.

**Coverage on the shim**: the shim itself is in `*_test.go` and
does not count toward the package's coverage metric. The
production-side `bot.go` is fully exercised through the shim.

**Rationale**: The narrowest test seam that satisfies "no live
Discord". The shim is a fixture; the production code is unchanged
across test/production builds.

**Alternatives considered:**

- **Wrap `*discordgo.Session` in a private package-level
  interface in production code**: rejected — adds production
  abstraction for test convenience, which Constitution IX
  ("accept interfaces, return concrete types") generally
  discourages. The package's job is to talk to Discord, not to
  abstract over Discord.
- **Use `httptest.Server` + a real `*discordgo.Session` against
  it**: rejected — discordgo's gateway (WebSocket) is the
  primary surface, not its REST API; mocking the WebSocket
  protocol against an `httptest.Server` is more code than the
  shim, and is more brittle across SDK versions.

---

## R-008 — Construction-time validation + audit channel mirroring

**Decision (a) — construction-time validation**: `NewBotApprover`
validates `cfg` before any side effect:

- `cfg.Token == nil` → `ErrMissingToken`.
- `cfg.Token.Len() == 0` (already destroyed or empty) →
  `ErrMissingToken`.
- `cfg.OwnerID == ""` → `ErrMissingOwnerID`.
- `cfg.AppID == ""` → `ErrMissingAppID`.
- `logger == nil` → `ErrMissingLogger`.

Each validation failure returns the bare sentinel; no `%w`
wrap (the sentinel itself is the failure category, same
Constitution-IX class as `internal/transport/ecies`'s sentinels).

`cfg.AuditChannelID == ""` is **not** a validation error —
audit-channel mirroring is optional per FR-008.

**Decision (b) — audit channel mirroring**: When
`cfg.AuditChannelID != ""`, the package mirrors all five
lifecycle events (`request_received`, `approved`, `denied`,
`timed_out`, `rate_limited`) to the configured channel. The
mirror payload carries the same operator-visible fields as the
DM (machine name, mesh IP, scope, reason, requested TTL,
session type, supervisor name for daemon sessions) but **never**
the action buttons.

**Best-effort, non-blocking** (FR-008, clarification 2026-04-30
Q4):

- Each mirror event is dispatched to a per-event goroutine
  spawned with the constructor's `ctx`.
- The goroutine calls `session.ChannelMessageSendComplex(auditChannelID, payload)`.
- Failure logs a WARN and is otherwise swallowed.
- The primary `RequestApproval` flow does NOT wait on the
  mirror; the mirror happens after the decision is returned to
  the caller (or in parallel for the `request_received` event).

**Same redaction**: no token, no secret value, no key material
appears in any mirror payload.

**Rationale**: The on-disk hash-chained audit log (SDD-13,
Layer 6) is the authoritative record. The Discord audit channel
is the convenience layer for at-a-glance visibility. Failing the
primary approval flow because the audit channel is unreachable
would convert a convenience layer into a critical-path
dependency, which violates Constitution V's "failures are loud
in distinct channels" and FR-008's explicit "best-effort" clause.

**Alternatives considered:**

- **Block `RequestApproval` on audit-mirror success**: rejected
  — violates FR-008 explicitly.
- **Drop audit mirroring entirely from SDD-11**: rejected — the
  spec User Story 4 / Edge Cases catalogues audit-channel
  behaviour, so the surface must exist; SDD-13 owns the
  authoritative record but the convenience-layer fan-out is
  this package's concern at delivery time.

---

## Phase 0 completion check

- ✅ Discord SDK chosen and Constitution-XI-justified.
- ✅ Approver type duality vs SDD-10 resolved (SDD-12 owns the
  adapter; SDD-11 imports nothing from `internal/server`).
- ✅ Bot-token ingestion through SecureBytes locked, with
  residual risk documented.
- ✅ Reconnect mechanics (event handlers + 60s cap + boot-down
  recovery) locked.
- ✅ Rate-limit semantics (per-key, deferred consume,
  monotonic clock) locked.
- ✅ Pending-approval correlation (UUID CustomID + buffered
  channel + lazy reap) locked.
- ✅ Test seam (private `sessionAPI` shim + no live Discord)
  locked.
- ✅ Construction-time validation + audit-channel mirroring
  locked.

No `[NEEDS CLARIFICATION]` markers remain.
