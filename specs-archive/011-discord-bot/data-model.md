# Data Model — `internal/discord` (SDD-11)

This document enumerates the entities, state transitions, and
validation rules implemented by the package. Every entity here
maps to a field in the locked exported API
([contracts/api.md](./contracts/api.md)) or a private
implementation detail of `*BotApprover`.

---

## 1. Exported types

### 1.1 `ApprovalRequest`

A structured message describing a pending claim. Constructed by
the SDD-12 adapter from `server.ApprovalRequest`; consumed by
`Approver.RequestApproval`.

| Field | Type | Required | Source / validation |
|-------|------|----------|--------------------|
| `MachineName` | `string` | yes | Caller-supplied; rendered to operator. **No package-level validation** (the value is operator-visible, but the operator's denial is the recourse — FR-006 Edge Case "Operator's chat client and request's machine identifier disagree"). |
| `ClientIP` | `string` | yes | Caller-supplied; rendered to operator AND used as a rate-limit key component. The chassis-side adapter (SDD-12) populates from `netip.Addr.String()` so values are always valid IP textual form. |
| `Reason` | `string` | yes | Caller-supplied; rendered to operator verbatim. |
| `Scope` | `[]string` | yes | List of secret **names** (never values). The package never resolves names to values — that is `internal/vault`'s concern. Rendered as a comma-joined list. |
| `RequestedTTL` | `time.Duration` | yes | Caller-supplied. Forms `Decision.ApprovedTTL` exactly when approved (clarification 2026-04-30 Q2: v0.1.0 has no shorten-TTL UX). |
| `SessionType` | `token.SessionType` | yes | One of `token.SessionInteractive` or `token.SessionSupervisor`. Drives template selection (interactive vs `[DAEMON]`) and rate-limit key composition. |
| `SupervisorName` | `string` | conditional | Required when `SessionType == token.SessionSupervisor`; empty for `SessionInteractive`. Forms part of the rate-limit key for supervisor requests. Rendered immediately after `MachineName` in the daemon DM template. |

**Validation behaviour**: `RequestApproval` does not validate
fields — it renders what the caller supplies. The operator's
denial is the recourse (per FR-006 Edge Case). A future SDD
may add caller-side validation in the SDD-12 adapter, but this
chunk is the producer-of-record for the field shapes only.

---

### 1.2 `Decision`

The successful outcome of an approval. Returned by
`RequestApproval` only on the operator-Approve path.

| Field | Type | Source / value |
|-------|------|---------------|
| `Approved` | `bool` | `true` if and only if the operator clicked Approve. The only path that returns `Approved=true` is the operator-Approve interaction handler (Constitution II). |
| `ApprovedTTL` | `time.Duration` | Equals `ApprovalRequest.RequestedTTL` exactly in v0.1.0 (clarification 2026-04-30 Q2). The field exists for forward-compatible UX (e.g., a future shorten-TTL modal) but is never reduced in v0.1.0. |
| `Reason` | `string` | Empty in v0.1.0 (clarification 2026-04-30 Q2). The field exists for forward-compatible UX. |

**Non-approval paths return errors, not Decisions**:

- Operator clicks Deny → `ErrApprovalDenied`.
- Operator times out (caller's `ctx` deadline) → `ErrApprovalTimeout`.
- Transport unavailable → `ErrDiscordUnavailable`.
- Rate-limited → `ErrRateLimited`.

---

### 1.3 `BotConfig`

Construction parameters for `NewBotApprover`.

| Field | Type | Required | Validation |
|-------|------|----------|-----------|
| `Token` | `*securebytes.SecureBytes` | yes | Non-nil, `Len() > 0`. Failure → `ErrMissingToken`. |
| `OwnerID` | `string` | yes | Non-empty. Failure → `ErrMissingOwnerID`. The configured operator's Discord snowflake (e.g., `"123456789012345678"`). |
| `AppID` | `string` | yes | Non-empty. Failure → `ErrMissingAppID`. The bot's Discord application ID. |
| `AuditChannelID` | `string` | no | Empty disables audit-channel mirroring; non-empty enables the best-effort mirror to the named channel. No syntactic validation in this package. |
| `DMRateLimit` | `time.Duration` | no | `≤ 0` (zero or negative) → `DefaultDMRateLimit = 5 * time.Minute` (FR-021). Positive → use as-is. |

The `Token` field's `*SecureBytes` is **consumed** by
`NewBotApprover`: the constructor reads it via `Use(fn)` and
the SDK retains its own internal `string` copy thereafter. The
caller MAY (and SHOULD) call `cfg.Token.Destroy()` after
`NewBotApprover` returns, since the package keeps no further
reference to the SecureBytes object (the SDK's string copy is
not derived from this object after construction).

---

### 1.4 `BotApprover`

Concrete implementation of `Approver`.

```go
type BotApprover struct {
    // unexported
    session       sessionAPI            // *discordgo.Session in production; shim in tests
    ownerID       string                // copied from cfg.OwnerID
    auditChan     string                // copied from cfg.AuditChannelID; "" disables mirroring
    rateLimitWin  time.Duration         // copied from cfg.DMRateLimit (defaulted)
    available     atomic.Bool           // gateway connection state
    pending       sync.Map              // map[customID string]chan decisionEvent — request correlation
    bucket        *rateBucket           // ratelimit.go state
    logger        *slog.Logger
    monitorDone   chan struct{}         // closed when monitor goroutine exits
}
```

**Concurrency**: `*BotApprover` is safe for concurrent use.
`RequestApproval` may be invoked from many goroutines; each
allocates its own `customID` and channel slot. The
`available` flag is read with `atomic.Load` and written with
`atomic.Store`. The `bucket` mutates under its own internal
mutex.

---

## 2. State machine

### 2.1 Connection state (`available` flag)

```
                  +------ Disconnect / boot-down / Open()-fail ------+
                  |                                                  |
                  v                                                  |
       +-------------------+                                         |
       |   Unavailable     |                                         |
       |  (available=false)|                                         |
       +-------------------+                                         |
                  |                                                  |
                  | Ready / Resumed                                  |
                  v                                                  |
       +-------------------+         Disconnect                      |
       |    Available      | -----------------------> +--------------+
       |  (available=true) |                          |
       +-------------------+                          |
                  |                                   |
                  | ctx.Done() (terminal)             |
                  v                                   |
       +-------------------+                          |
       |   Terminated      | <------------------------+
       |  (monitor exited) |                         (ctx.Done() during disconnect)
       +-------------------+
```

**Transitions:**

- **Initial state**: `Unavailable` (set in the constructor
  before `session.Open()` is called).
- **Boot success** (`Open()` succeeds + `Ready` event fires):
  `Unavailable → Available`. `available.Store(true)`.
- **Boot failure** (`Open()` returns an error, OR `Open()`
  succeeds but `Ready` never fires before any disconnect):
  remain `Unavailable`. The monitor's reconnect loop drives
  retries.
- **Runtime disconnect**: `Available → Unavailable`.
  `available.Store(false)`. All pending request channels
  receive `ErrDiscordUnavailable` (FR-010, User Story 3
  acceptance scenario 2).
- **Reconnect** (post-disconnect `Ready` or `Resumed` fires):
  `Unavailable → Available`. `available.Store(true)`.
- **Terminal** (`ctx` cancelled): monitor goroutine calls
  `session.Close()`, drains pending channels with
  `ErrDiscordUnavailable`, exits. `available` stays in
  whatever state it last held; subsequent `RequestApproval`
  calls would race on the flag, but in practice the chassis
  has already shut down.

### 2.2 Per-request state

Every `RequestApproval` call goes through this sequence:

```
  Caller invokes RequestApproval(ctx, req)
              │
              ▼
  ┌──────────────────────┐
  │ Check available flag │ ───> ErrDiscordUnavailable (rate limiter not consulted; FR-021a)
  └──────────────────────┘
              │ available
              ▼
  ┌──────────────────────┐
  │ rateBucket.Acquire   │ ───> ErrRateLimited
  └──────────────────────┘
              │ granted
              ▼
  ┌──────────────────────┐
  │ render DM payload    │
  └──────────────────────┘
              │
              ▼
  ┌──────────────────────┐
  │ session.UCC + CMSC   │ ───> bucket.Refund() + ErrDiscordUnavailable
  └──────────────────────┘
              │ delivered
              ▼
  ┌──────────────────────┐
  │ bucket.Commit        │
  │ mirror request_recvd │ (best-effort, non-blocking)
  └──────────────────────┘
              │
              ▼
  ┌──────────────────────┐
  │ wait on per-req chan │
  │ select {              │
  │   approve  -> Decision│ -> mirror "approved"
  │   deny     -> error   │ -> mirror "denied"
  │   ctx.Done -> error   │ -> mirror "timed_out" (or context.Canceled)
  │   disconnect -> error │ -> mirror "rate_limited" not applicable here
  │ }                     │
  └──────────────────────┘
```

**Notes**:

- `UCC` = `UserChannelCreate` (resolves the operator's DM
  channel ID); `CMSC` = `ChannelMessageSendComplex` (sends the
  rendered DM with components).
- Mirror events are emitted from the `RequestApproval`
  goroutine after the per-event work completes, dispatched
  asynchronously (best-effort).
- The rate-limit `pending` slot transitions:
  - `pending = 0 → pending = now` on `Acquire(granted)`.
  - `pending = now → delivered = now, pending = 0` on
    `Commit`.
  - `pending = now → pending = 0` on `Refund` (delivered
    unchanged).

---

## 3. Rate-limit bucket model

### 3.1 Key composition

```go
type bucketKey struct {
    SupervisorName string  // "" for SessionInteractive
    ClientIP       string
}
```

Per-request key derivation:

```go
func makeKey(req ApprovalRequest) bucketKey {
    if req.SessionType == token.SessionSupervisor {
        return bucketKey{SupervisorName: req.SupervisorName, ClientIP: req.ClientIP}
    }
    return bucketKey{SupervisorName: "", ClientIP: req.ClientIP}
}
```

### 3.2 Bucket state

```go
type bucketState struct {
    delivered time.Time  // monotonic component honoured; zero = no prior delivery
    pending   time.Time  // monotonic component honoured; zero = no in-flight delivery
}
```

The bucket is `map[bucketKey]bucketState` under a single
`sync.Mutex`. Every operation locks-then-mutates-then-unlocks
(no per-key locks — the contention model is operator-driven
and far below a level that justifies finer-grained locking).

### 3.3 Operations

```go
type acquireResult uint8
const (
    acquireGranted acquireResult = iota // pending slot taken
    acquireDenied                       // window not elapsed OR concurrent pending
)

func (b *bucket) Acquire(key bucketKey, now time.Time) acquireResult
func (b *bucket) Commit(key bucketKey)
func (b *bucket) Refund(key bucketKey)
```

**Invariants:**

- After `Acquire` returns `acquireGranted`, exactly one
  matching `Commit` or `Refund` MUST follow (no leak
  permitted). `RequestApproval`'s control flow guarantees this
  via `defer bucket.Refund(...)` registered immediately after
  `Acquire`, with explicit `bucket.Commit(...)` cancelling the
  defer when delivery succeeds.
- `Acquire` never blocks. There is no goroutine inside the
  bucket; reaping happens lazily on the next `Acquire` for a
  given key (the on-call cleanup runs only for the key being
  acquired).
- The map grows monotonically with the number of distinct
  keys seen. For a vault server with N supervisors and M
  agents, the worst-case map size is `N + M` entries. This is
  small enough that no LRU eviction is needed in v0.1.0.

---

## 4. DM rendering model

### 4.1 Template selection

```go
func renderApproval(req ApprovalRequest, customID string) *discordgo.MessageSend
```

Switches on `req.SessionType`:

- `token.SessionInteractive` → `renderInteractive(...)`.
- `token.SessionSupervisor` → `renderDaemon(...)`.

### 4.2 Interactive template

```
✅ **Interactive secret request**

Machine: <MachineName>
Mesh IP: <ClientIP>
Scope:   <comma-joined Scope>
Reason:  <Reason>
TTL:     <RequestedTTL>

[Approve]  [Deny]
```

- Embed colour: green (`0x57F287`).
- Component row: two buttons with `CustomID = customID + ":approve"` and `customID + ":deny"`.
- Style: `discordgo.PrimaryButton` (Approve) + `discordgo.DangerButton` (Deny).

### 4.3 Daemon template

```
⚠️ **[DAEMON] Supervisor secret request**

Machine:    <MachineName>
Supervisor: <SupervisorName>
Mesh IP:    <ClientIP>
Scope:      <comma-joined Scope>
Reason:     <Reason>
TTL:        <RequestedTTL>

[Approve]  [Deny]
```

- Embed colour: yellow (`0xFEE75C`).
- Component row identical to interactive.
- The `Supervisor` line MUST appear immediately after `Machine`
  (the operator's primary disambiguator beyond the prefix).

### 4.4 Field redaction invariants

All rendered fields pass through a per-template assertion in
test code that the rendered message bytes contain none of:

- The bot token (any byte sequence from `cfg.Token`).
- The strings "secret", "value", "key" (informally — captured
  as "no `Secret value:` line in any template").
- Any private-key material (the package never sees private
  keys).

`TestApprovalRender_NeverIncludesToken` injects a unique 64-
character sentinel via the SecureBytes wrapper and asserts the
sentinel is absent from every rendered message body, embed
field, and component label.

---

## 5. Audit-channel event catalogue

When `cfg.AuditChannelID != ""`, the package emits exactly five
event types per approval lifecycle:

| Event type | Triggered by | Payload fields | Buttons? |
|-----------|--------------|---------------|----------|
| `request_received` | DM successfully delivered | All FR-003 fields | no |
| `approved` | Operator clicks Approve | Same as request_received + `decision: approved` | no |
| `denied` | Operator clicks Deny | Same as request_received + `decision: denied` | no |
| `timed_out` | Caller's `ctx` deadline elapses | Same as request_received + `decision: timed_out` | no |
| `rate_limited` | `RequestApproval` returns `ErrRateLimited` | Same as request_received (no DM was delivered) | no |

**Non-events**: `ErrDiscordUnavailable` does NOT mirror — the
audit channel is unreachable when transport is down, so the
mirror would fail anyway. `context.Canceled` (caller cancelled
explicitly, not deadline-elapsed) does NOT mirror — caller-driven
cancellation is not a lifecycle event.

**Same redaction**: every payload runs through the same
field-redaction invariants as DM rendering (§4.4). The audit
channel is a convenience layer; the on-disk hash-chained log
(SDD-13) is the authoritative record.

**Best-effort dispatch**: each event spawns a goroutine bounded
by the constructor's `ctx`; the goroutine calls
`session.ChannelMessageSendComplex(auditChan, payload)`;
failure logs WARN and exits. The primary `RequestApproval` flow
does not block on the mirror.

---

## 6. Sentinel error catalogue (summary)

Full trigger conditions in [contracts/api.md](./contracts/api.md).

| Sentinel | Class | Operational alert |
|----------|-------|-------------------|
| `ErrDiscordUnavailable` | runtime, transport | warning |
| `ErrApprovalDenied` | runtime, decision | info |
| `ErrApprovalTimeout` | runtime, decision | info |
| `ErrRateLimited` | runtime, gate | warning |
| `ErrMissingToken` | construction | startup-fatal |
| `ErrMissingOwnerID` | construction | startup-fatal |
| `ErrMissingAppID` | construction | startup-fatal |
| `ErrMissingLogger` | construction | startup-fatal |

All eight are exported `var Err... = errors.New("static
message")` with messages that contain ONLY the failure category
(no token byte, no machine name, no scope item, no key byte —
Constitution X).

---

## 7. Lifecycle invariants

Asserted by tests in the `bot_test.go` / `monitor_test.go` /
`ratelimit_test.go` suites:

1. **Constitution-II non-negotiable**: `RequestApproval` returns
   `Decision{Approved: true}` if and only if an
   `InteractionCreate` event arrived with the matching
   `CustomID` AND `interaction.Data.CustomID` ended in
   `:approve` AND `available == true`. The implementation has
   no other code path producing `Approved: true`.
2. **No-auto-approve-knob**: AST walk over the package source
   confirms no field, constant, env-var lookup, or build tag
   contains the strings `auto_approve` / `autoapprove` /
   `bypass` / `skipApproval` / `noApproval`.
3. **Token absent from artifacts**: a unique 64-character
   sentinel injected via `cfg.Token` is absent from every
   `slog` record captured during a full lifecycle exercise,
   every `err.Error()` returned by the package, and every
   audit-channel payload.
4. **Monitor goroutine ctx-bound**: a `ctx.Cancel()` causes
   the monitor goroutine to exit within 100 ms (drain time).
   `monitorDone` channel closes as the exit signal.
5. **Rate-limit token never consumed on transport-unavailable**:
   asserted by `TestRateLimit_TransportUnavailableDoesNotConsumeToken`.
6. **Disconnect surfaces unavailable within 100 ms**: asserted
   by `TestBotApprover_DisconnectFastPath` (SC-002).
7. **In-flight requests unblock on disconnect**: asserted by
   `TestMonitor_DisconnectUnblocksInFlightRequest` (FR-010,
   User Story 3 acceptance scenario 2).
8. **First action wins**: asserted by
   `TestDecisionRouting_FirstActionWins` — a double-Approve or
   Approve+Deny race resolves to the first-received action;
   subsequent actions are silently dropped (FR-017).
9. **Per-key isolation**: asserted by
   `TestRateLimit_PerKeyIsolation` — two requests differing in
   either `SupervisorName` or `ClientIP` deliver both prompts
   regardless of the rate-limit window.
10. **Best-effort audit mirroring**: asserted by
    `TestAuditChannel_FailureDoesNotBlockApproval` — an
    audit-channel send returning an error logs a WARN but does
    not affect the primary approval flow.
