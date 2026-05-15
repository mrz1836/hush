---
description: "Task list for SDD-11 internal/discord — Approver interface + BotApprover + disconnect monitor + rate limiter"
---

# Tasks: Discord-Backed Approver (SDD-11)

**Input**: Design documents from `/specs/011-discord-bot/`
**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/api.md](./contracts/api.md), [quickstart.md](./quickstart.md)
**Chunk contract**: [docs/sdd/SDD-11.md](../../docs/sdd/SDD-11.md)
**Branch**: `011-discord-bot`
**Coverage target**: ≥85% on `internal/discord/` (Constitution VIII Medium band)
**Discipline**: **TDD-mandatory** per Constitution VIII — every behaviour contract has a test-writing task scheduled BEFORE its implementation task; tests are written to fail first. **Every test uses the package-internal `session_shim_test.go` fake; no live Discord connection EVER opens under `go test`** (chunk contract + research [R-007](./research.md#r-007--testing-without-discord-shim-strategy)).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Maps the task to a spec.md user story (US1–US5). Setup, Foundational, and Polish phases carry no story label.
- File paths are absolute or relative to repo root.

## Path Conventions

- Production source: `internal/discord/` (single Go module `github.com/mrz1836/hush`, flat `internal/<domain>` layout per `docs/PACKAGE-MAP.md`).
- Tests: `internal/discord/*_test.go` (package-internal — no separate `tests/` tree).
- Module file: `go.mod` at repo root.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Land the new direct dependency and the package skeleton.

- [X] T001 Add `github.com/bwmarrin/discordgo` v0.29.0 as a direct dependency in [go.mod](go.mod) and run `go mod tidy` to refresh [go.sum](go.sum) (Constitution XI — justified in research [R-001](specs/011-discord-bot/research.md#r-001--discord-sdk-bwmarrin-discordgo)).
- [X] T002 [P] Create [internal/discord/doc.go](internal/discord/doc.go) with the package-level doc comment citing Constitution II/V/VIII/IX/X/XI and listing the package's Layer-V roster (FR-022..FR-025 token discipline).

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Lock the exported types, sentinel errors, the test session-shim seam, and constructor validation. Every user story depends on these. **TDD ordering is enforced — every test task precedes the implementation task it covers.**

**⚠️ CRITICAL**: No user story work can begin until Phase 2 is complete.

- [X] T003 [P] Declare the eight sentinel errors (`ErrDiscordUnavailable`, `ErrApprovalDenied`, `ErrApprovalTimeout`, `ErrRateLimited`, `ErrMissingToken`, `ErrMissingOwnerID`, `ErrMissingAppID`, `ErrMissingLogger`) as `var Err... = errors.New("hush/discord: <static category>")` in [internal/discord/errors.go](internal/discord/errors.go) per [contracts/api.md §6](specs/011-discord-bot/contracts/api.md) — static category messages only, no token bytes / no req fields (Constitution X).
- [X] T004 [P] Declare the exported `Approver` interface, `ApprovalRequest` struct, `Decision` struct, `BotConfig` struct, and `DefaultDMRateLimit = 5 * time.Minute` constant in [internal/discord/approver.go](internal/discord/approver.go) per [contracts/api.md §3](specs/011-discord-bot/contracts/api.md) and [data-model.md §1](specs/011-discord-bot/data-model.md).
- [X] T005 Declare the unexported `sessionAPI` interface (Open/Close/UserChannelCreate/ChannelMessageSendComplex/AddHandler/ShouldReconnectOnError seam) and the `BotApprover` struct skeleton (unexported fields per [data-model.md §1.4](specs/011-discord-bot/data-model.md): `session sessionAPI`, `ownerID`, `auditChan`, `rateLimitWin`, `available atomic.Bool`, `pending sync.Map`, `bucket *rateBucket`, `logger`, `monitorDone chan struct{}`) in [internal/discord/bot.go](internal/discord/bot.go) — research [R-007](specs/011-discord-bot/research.md#r-007--testing-without-discord-shim-strategy).
- [X] T006 Author the programmable `*sessionShim` fake in [internal/discord/session_shim_test.go](internal/discord/session_shim_test.go) (test-only) implementing `sessionAPI`: synchronous `Open`/`Close` no-ops, recording `ChannelMessageSendComplex`, channel-of-handlers via `AddHandler`, and the test trigger helpers `TriggerInteractionCreate`, `TriggerDisconnect`, `TriggerReady`, `TriggerResumed`, `TriggerConnect`, plus a `newBotApproverWithSession` package-private constructor that bypasses production `discordgo.New(...)` and injects the shim (research [R-007](specs/011-discord-bot/research.md#r-007--testing-without-discord-shim-strategy)). **No `go test` path opens a Discord socket.**
- [X] T007 [P] Write `TestApprover_BotApproverImplementsApprover` in [internal/discord/approver_test.go](internal/discord/approver_test.go) — compile-time interface conformance via `var _ Approver = (*BotApprover)(nil)` plus a runtime assertion. **Fails until T011 lands.**
- [X] T008 [P] Write `TestApprovalRequest_DaemonRequiresSupervisorName` in [internal/discord/approver_test.go](internal/discord/approver_test.go) — documents the validation hint per [data-model.md §1.1](specs/011-discord-bot/data-model.md). **Fails until type shape is confirmed.**
- [X] T009 Write `TestNewBotApprover_ValidatesConfig` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — table-driven cases asserting (nil Token, zero-len Token, empty OwnerID, empty AppID, nil logger) each produce the matching `ErrMissing*` sentinel via `errors.Is`. **Fails until T010 lands.**
- [X] T010 Implement `NewBotApprover` validation order per [contracts/api.md §4.1](specs/011-discord-bot/contracts/api.md): Token nil/empty → `ErrMissingToken`; OwnerID empty → `ErrMissingOwnerID`; AppID empty → `ErrMissingAppID`; logger nil → `ErrMissingLogger`. Bare-sentinel return; no `%w`. No side effects on validation failure. Implement in [internal/discord/bot.go](internal/discord/bot.go).
- [X] T011 Implement the `NewBotApprover` happy-path skeleton (apply `DMRateLimit ≤ 0 → DefaultDMRateLimit`; allocate `*BotApprover` with `available.Store(false)`; allocate `pending sync.Map` and `*rateBucket`; return `*BotApprover, nil`) in [internal/discord/bot.go](internal/discord/bot.go) — leaves session construction, handler registration, and monitor spawn as no-ops to be filled in within US1/US3 phases.

**Checkpoint**: Foundation ready — types and sentinels locked, the test shim is wired, and validation passes T009. User-story phases can now proceed.

---

## Phase 3: User Story 1 — Operator approves an interactive shell session (Priority: P1) 🎯 MVP

**Goal**: Vault server requests approval for an interactive session; operator receives an interactive-labelled DM with all FR-003 fields and Approve/Deny buttons; clicking Approve returns `Decision{Approved: true, ApprovedTTL: req.RequestedTTL}` with empty Reason (clarification 2026-04-30 Q2).

**Independent Test**: With the session shim, request approval for a `SessionInteractive` request, simulate the operator pressing Approve, and verify the returned `Decision` has `Approved=true` and `ApprovedTTL` equal to `RequestedTTL`. No live Discord connection required.

### Tests for User Story 1 ⚠️ TDD — write FIRST, ensure they FAIL before implementation

- [X] T012 [P] [US1] Write `TestApprovalRender_InteractiveLabel` in [internal/discord/render_test.go](internal/discord/render_test.go) — asserts the rendered `*discordgo.MessageSend` for a `SessionInteractive` request begins with the U+2705 (✅) glyph + `"**Interactive secret request**"` header per [data-model.md §4.2](specs/011-discord-bot/data-model.md) (FR-006).
- [X] T013 [P] [US1] Write `TestApprovalRender_AllRequestFieldsPresent` in [internal/discord/render_test.go](internal/discord/render_test.go) — asserts MachineName, ClientIP, Reason, comma-joined Scope, and RequestedTTL are all visible in the embed body for an interactive request (FR-007).
- [X] T014 [US1] Write `TestDecisionRouting_Approve` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — drives `RequestApproval` against the shim, calls `shim.TriggerReady()`, then `shim.TriggerInteractionCreate(customID + ":approve")`, and asserts the returned `Decision{Approved: true, ApprovedTTL: req.RequestedTTL, Reason: ""}` with `err == nil` (FR-014, US1 acceptance scenario 2).
- [X] T015 [US1] Write `TestDecisionRouting_FirstActionWins` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — fires two interaction events for the same `CustomID` (Approve then Deny) and asserts only the first decides the call; the second is silently dropped (FR-017, [contracts/api.md §7.9](specs/011-discord-bot/contracts/api.md)).

### Implementation for User Story 1

- [X] T016 [US1] Implement `renderInteractive` in [internal/discord/render.go](internal/discord/render.go) — emits a `*discordgo.MessageSend` with embed colour `0x57F287` (green), header `"✅ **Interactive secret request**"`, body listing `Machine`/`Mesh IP`/`Scope`/`Reason`/`TTL`, and a component row of two buttons (`PrimaryButton` "Approve" with `CustomID = customID + ":approve"`, `DangerButton` "Deny" with `CustomID = customID + ":deny"`).
- [X] T017 [US1] Implement the `renderApproval(req, customID)` dispatch in [internal/discord/render.go](internal/discord/render.go) switching on `req.SessionType` — `SessionInteractive → renderInteractive`; the `SessionSupervisor` arm is a stub returning an empty payload until US2 fills it in.
- [X] T018 [US1] Implement `(*BotApprover).RequestApproval` happy-path in [internal/discord/bot.go](internal/discord/bot.go) — generate a UUID via `crypto/rand`-backed helper (research [R-006](specs/011-discord-bot/research.md#r-006--pending-approval-correlation)); call `renderApproval`; call `session.UserChannelCreate(ownerID)` then `session.ChannelMessageSendComplex(dmChan, msg)`; register a buffered `chan decisionEvent` (size 1) under the UUID in `pending` `sync.Map`; block on the channel for the Approve case and return `Decision{Approved: true, ApprovedTTL: req.RequestedTTL}`.
- [X] T019 [US1] Register the `InteractionCreate` handler in [internal/discord/bot.go](internal/discord/bot.go) — splits `interaction.Data.CustomID` on `":"`, looks up the UUID in `pending`, **deletes the map entry before sending** (first-action-wins, [contracts/api.md §7.9](specs/011-discord-bot/contracts/api.md)), then sends `decisionEvent{kind: approve}` non-blocking to the buffered channel.
- [X] T020 [US1] Wire the `Ready` event handler in [internal/discord/monitor.go](internal/discord/monitor.go) — flips `available.Store(true)` on initial gateway-ready signal so US1 can succeed end-to-end against the shim (research [R-004](specs/011-discord-bot/research.md#r-004--reconnect-mechanics-fr-013-fr-013a-fr-013b)). Spawn the monitor goroutine from `NewBotApprover` bound to its `ctx` (Constitution IX) — termination details fill in under US3.

**Checkpoint**: US1 is independently functional. `go test -run TestDecisionRouting_Approve ./internal/discord/` passes against the shim with no live Discord.

---

## Phase 4: User Story 2 — Operator approves a long-running daemon's refill (Priority: P1)

**Goal**: A `SessionSupervisor` request renders a visually distinct `[DAEMON]` DM (warning glyph + supervisor name shown immediately after machine name) so the operator never confuses a daemon refill for an interactive shell.

**Independent Test**: Request approval for `SessionSupervisor` with a populated `SupervisorName`; capture the rendered message; assert the daemon marker, the supervisor line, and a measurable difference (different colour/header glyph) versus the interactive rendering from US1.

### Tests for User Story 2 ⚠️ TDD — write FIRST, ensure they FAIL before implementation

- [X] T021 [US2] Write `TestApprovalRender_DaemonLabel` in [internal/discord/render_test.go](internal/discord/render_test.go) — asserts the `SessionSupervisor` rendering begins with the U+26A0 (⚠️) glyph + `"**[DAEMON] Supervisor secret request**"` header per [data-model.md §4.3](specs/011-discord-bot/data-model.md) (FR-006).
- [X] T022 [US2] Write `TestApprovalRender_DaemonIncludesSupervisorName` in [internal/discord/render_test.go](internal/discord/render_test.go) — asserts the rendered body contains a `Supervisor:` line carrying `req.SupervisorName` immediately after the `Machine:` line (FR-006, US2 AS1).
- [X] T023 [US2] Write `TestApprovalRender_VisuallyDistinctFromInteractive` in [internal/discord/render_test.go](internal/discord/render_test.go) — renders the same `(MachineName, ClientIP, Reason, Scope, RequestedTTL)` as both `SessionInteractive` and `SessionSupervisor`; asserts the embed colours differ (`0x57F287` vs `0xFEE75C`) AND the header glyphs differ (US2 AS2).

### Implementation for User Story 2

- [X] T024 [US2] Implement `renderDaemon` in [internal/discord/render.go](internal/discord/render.go) — `*discordgo.MessageSend` with embed colour `0xFEE75C` (yellow), header `"⚠ **[DAEMON] Supervisor secret request**"`, body lines `Machine` / `Supervisor` / `Mesh IP` / `Scope` / `Reason` / `TTL` (Supervisor immediately after Machine), and the same two-button component row as `renderInteractive`. Replace the US1 stub branch in `renderApproval` so `SessionSupervisor` routes here.

**Checkpoint**: US1 and US2 both work independently. The operator sees unmistakably different prompts for human vs daemon requests.

---

## Phase 5: User Story 3 — Approval fails closed when the chat transport is unavailable (Priority: P1)

**Goal**: Constitution-II load-bearing — the approver NEVER returns `Approved: true` while the WebSocket is down. `available == false` produces `ErrDiscordUnavailable` within ≤100 ms (SC-002); in-flight requests unblock with the same sentinel; the reconnect loop runs indefinitely with a 60s exponential backoff cap (FR-013b); no auto-approve knob exists in the entire package.

**Independent Test**: Boot the approver against a shim whose `Open()` returns an error; assert `NewBotApprover` returns nil error and the approver starts in the unavailable state (FR-013a). Then `shim.TriggerDisconnect()`, call `RequestApproval`, and assert `errors.Is(err, ErrDiscordUnavailable)` within 100 ms. Run an in-flight request, fire `TriggerDisconnect()`, and assert the in-flight call unblocks with the same sentinel. AST-walk the package source and assert no `auto_approve` / `bypass` / `skipApproval` knob exists anywhere.

### Tests for User Story 3 ⚠️ TDD — write FIRST, ensure they FAIL before implementation

- [X] T025 [US3] Write `TestMonitor_DisconnectSurfacesUnavailable` in [internal/discord/monitor_test.go](internal/discord/monitor_test.go) — `shim.TriggerReady()` then `shim.TriggerDisconnect()`; assert subsequent `RequestApproval` returns `ErrDiscordUnavailable` (FR-009).
- [X] T026 [US3] Write `TestMonitor_DisconnectUnblocksInFlightRequest` in [internal/discord/monitor_test.go](internal/discord/monitor_test.go) — start a `RequestApproval` in a goroutine, wait for the DM-sent signal, fire `shim.TriggerDisconnect()`; assert the call unblocks with `ErrDiscordUnavailable` within 100 ms (FR-010, US3 AS2).
- [X] T027 [US3] Write `TestMonitor_ReconnectRestoresAvailability` in [internal/discord/monitor_test.go](internal/discord/monitor_test.go) — TriggerReady → TriggerDisconnect → TriggerReady; assert the next `RequestApproval` proceeds normally (FR-013, US3 AS3).
- [X] T028 [US3] Write `TestMonitor_ReconnectBackoffCappedAt60s` in [internal/discord/monitor_test.go](internal/discord/monitor_test.go) — using an injectable clock + sleeper, fire repeated `Disconnect`s and assert the computed backoff sequence is `min(2^n * time.Second, 60*time.Second)` and never exceeds 60 s (FR-013b, research [R-004](specs/011-discord-bot/research.md#r-004--reconnect-mechanics-fr-013-fr-013a-fr-013b)).
- [X] T029 [US3] Write `TestMonitor_GoroutineExitsOnCtxCancel` in [internal/discord/monitor_test.go](internal/discord/monitor_test.go) — `cancel()` the constructor ctx and assert `<-monitorDone` returns within 100 ms; assert `session.Close()` was invoked (FR-026, [contracts/api.md §7.8](specs/011-discord-bot/contracts/api.md)).
- [X] T030 [US3] Write `TestBotApprover_DisconnectFastPath` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — with `available.Store(false)`, assert `RequestApproval` returns `ErrDiscordUnavailable` in **≤100 ms** measured wall time (SC-002, [contracts/api.md §7.2](specs/011-discord-bot/contracts/api.md)).
- [X] T031 [US3] Write `TestNewBotApprover_BootDownStartsUnavailable` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — shim's `Open()` returns an error; assert `NewBotApprover` returns `*BotApprover, nil` (no error), `available.Load() == false`, and `RequestApproval` returns `ErrDiscordUnavailable` (FR-013a, clarification 2026-04-30 Q1).
- [X] T032 [US3] Write `TestBotApprover_NeverAutoApprovesOnDiscordError` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — exhaustively drive every error path (Open()-fail at boot, Disconnect mid-flight, ChannelMessageSendComplex returns error, UserChannelCreate fails) and assert NONE produce `Decision{Approved: true}`; every path returns a non-nil error and a `Decision{}` zero value (Constitution II non-negotiable, US3 AS4, the load-bearing safety property of the entire chunk).
- [X] T033 [US3] Write `TestBotApprover_NoAutoApproveKnobExists` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — AST walk via `go/parser.ParseDir` over `internal/discord/` (production sources only); assert no field name, constant, env-var lookup, build tag, or comment string contains `auto_approve` / `autoapprove` / `bypass` / `skipApproval` / `noApproval` (FR-012, US3 AS4, [data-model.md §7 invariant 2](specs/011-discord-bot/data-model.md)).
- [X] T034 [US3] Write `TestDecisionRouting_Deny` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — Approve+Deny flow returning `Decision{}, ErrApprovalDenied` (FR-015).
- [X] T035 [US3] Write `TestDecisionRouting_Timeout` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — `ctx.WithDeadline` elapses; assert `errors.Is(err, ErrApprovalTimeout)` AND `errors.Is(err, context.DeadlineExceeded)` both true (FR-016, [contracts/api.md §6.3](specs/011-discord-bot/contracts/api.md)).
- [X] T036 [US3] Write `TestDecisionRouting_CtxCancelled` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — caller cancels (not deadline); assert returned error `errors.Is(err, context.Canceled)` and is **not** wrapped under `ErrApprovalTimeout` ([contracts/api.md §6.3](specs/011-discord-bot/contracts/api.md)).

### Implementation for User Story 3

- [X] T037 [US3] Register `Disconnect` and `Connect` event handlers in [internal/discord/monitor.go](internal/discord/monitor.go) — `Disconnect` flips `available.Store(false)` and emits a WARN slog record; `Connect` is a no-op for the available flag (research [R-004](specs/011-discord-bot/research.md#r-004--reconnect-mechanics-fr-013-fr-013a-fr-013b)).
- [X] T038 [US3] Register `Resumed` event handler in [internal/discord/monitor.go](internal/discord/monitor.go) — flips `available.Store(true)` on resumed-session post-reconnect; emits an INFO slog record.
- [X] T039 [US3] Implement the hush-controlled reconnect loop in [internal/discord/monitor.go](internal/discord/monitor.go) — disable SDK reconnect (`session.ShouldReconnectOnError = false`); on `Disconnect` start a `time.NewTimer(backoff)` loop where `backoff = min(2^n * time.Second, 60*time.Second)` reset on `Ready`; loop forever until `ctx.Done()` (FR-013b indefinite retry).
- [X] T040 [US3] Implement the available-flag fast-path at `RequestApproval` entry in [internal/discord/bot.go](internal/discord/bot.go) — `if !a.available.Load() { return Decision{}, ErrDiscordUnavailable }` BEFORE any rate-limit consultation (FR-009, FR-021a, [contracts/api.md §4.2](specs/011-discord-bot/contracts/api.md) step 1). Must complete within ≤100 ms (SC-002).
- [X] T041 [US3] Implement the drain-pending-on-disconnect path in [internal/discord/monitor.go](internal/discord/monitor.go) — on `Disconnect`, walk `pending` `sync.Map` and send `decisionEvent{kind: unavailable}` non-blocking to every channel; the monitor's exit path on `ctx.Done()` performs the same drain (FR-010, [contracts/api.md §7.3](specs/011-discord-bot/contracts/api.md), [data-model.md §7 invariant 7](specs/011-discord-bot/data-model.md)).
- [X] T042 [US3] Implement deadline + cancel handling inside `RequestApproval`'s decision-channel `select` in [internal/discord/bot.go](internal/discord/bot.go) — `ctx.Done()` returns `fmt.Errorf("hush/discord: approval timed out: %w", context.DeadlineExceeded)` matching `ErrApprovalTimeout` via the wrapped sentinel pattern (or returns `context.Canceled` verbatim when `ctx.Err() == context.Canceled`); the unavailable channel arm returns `ErrDiscordUnavailable` (FR-016, [contracts/api.md §6.3](specs/011-discord-bot/contracts/api.md)).
- [X] T043 [US3] Make `NewBotApprover` Open()-failure-tolerant in [internal/discord/bot.go](internal/discord/bot.go) — on `session.Open()` error, log WARN, leave `available = false`, return `*BotApprover, nil`; the monitor's reconnect loop drives recovery (FR-013a, clarification 2026-04-30 Q1, [contracts/api.md §4.1](specs/011-discord-bot/contracts/api.md)).
- [X] T044 [US3] Implement `monitorDone` close on monitor exit + `session.Close()` (idempotent) in [internal/discord/monitor.go](internal/discord/monitor.go) — guarantees `TestMonitor_GoroutineExitsOnCtxCancel` passes within 100 ms ([contracts/api.md §7.8](specs/011-discord-bot/contracts/api.md)).

**Checkpoint**: Constitution-II non-negotiable is asserted by `TestBotApprover_NeverAutoApprovesOnDiscordError` AND `TestBotApprover_NoAutoApproveKnobExists`. Vault server boots cleanly even when Discord is unreachable.

---

## Phase 6: User Story 4 — A misconfigured daemon cannot flood the operator (Priority: P2)

**Goal**: Per-`(SupervisorName, ClientIP)` rate limit (default 1 per 5 min, configurable) prevents prompt floods. Tokens are consumed only on successful delivery — transport-unavailable and delivery-failure paths refund (FR-021a, clarification 2026-04-30 Q5). Per-key isolation: different supervisors or different machines do NOT share buckets. Interactive requests key on `ClientIP` only.

**Independent Test**: With the shim, fire two `RequestApproval` calls for the same `(SupervisorName, ClientIP)` pair within 5 minutes; assert the first delivers and the second returns `ErrRateLimited` without delivery. Wait past the window; assert the third delivers. Fire two requests with different keys; assert both deliver.

### Tests for User Story 4 ⚠️ TDD — write FIRST, ensure they FAIL before implementation

- [X] T045 [US4] Write `TestRateLimit_BlocksSecondPromptWithin5Min` in [internal/discord/ratelimit_test.go](internal/discord/ratelimit_test.go) — Acquire(key, t0) → granted; Acquire(key, t0 + 4min) → denied; bucket logic only, no `*BotApprover` (FR-018, FR-020).
- [X] T046 [US4] Write `TestRateLimit_AllowsAfterWindow` in [internal/discord/ratelimit_test.go](internal/discord/ratelimit_test.go) — Acquire(key, t0)+Commit; Acquire(key, t0 + 5min + 1ns) → granted (FR-018, US4 AS2).
- [X] T047 [US4] Write `TestRateLimit_PerKeyIsolation` in [internal/discord/ratelimit_test.go](internal/discord/ratelimit_test.go) — Acquire(`{Sup:"A", IP:"1.1.1.1"}`)+Commit then Acquire(`{Sup:"B", IP:"1.1.1.1"}`) and Acquire(`{Sup:"A", IP:"2.2.2.2"}`) both granted within the same window (FR-018, US4 AS3).
- [X] T048 [US4] Write `TestRateLimit_InteractiveKeyedByClientIP` in [internal/discord/ratelimit_test.go](internal/discord/ratelimit_test.go) — for `SessionInteractive`, `makeKey` uses `bucketKey{SupervisorName: "", ClientIP: req.ClientIP}`; two interactive requests from the same ClientIP within the window — second is rate-limited (FR-018, US4 AS4).
- [X] T049 [US4] Write `TestRateLimit_TransportUnavailableDoesNotConsumeToken` in [internal/discord/ratelimit_test.go](internal/discord/ratelimit_test.go) — drives `*BotApprover` against the shim with `available=false`; first call returns `ErrDiscordUnavailable` (no Acquire); a subsequent call after `available=true` is delivered (FR-021a, [data-model.md §7 invariant 5](specs/011-discord-bot/data-model.md)).
- [X] T050 [US4] Write `TestRateLimit_DeliveryFailureRefundsToken` in [internal/discord/ratelimit_test.go](internal/discord/ratelimit_test.go) — shim's `ChannelMessageSendComplex` returns an error on the first call only; assert the first call returns `ErrDiscordUnavailable` AND a second call within the window IS delivered (Refund worked) (FR-021a).
- [X] T051 [US4] Write `TestRateLimit_ZeroDMRateLimitUsesDefault` in [internal/discord/ratelimit_test.go](internal/discord/ratelimit_test.go) — construct with `DMRateLimit: 0` and `DMRateLimit: -1 * time.Second`; assert the effective window is `DefaultDMRateLimit = 5 * time.Minute` (FR-021).
- [X] T052 [US4] Write `TestRateLimit_UsesMonotonicClock` in [internal/discord/ratelimit_test.go](internal/discord/ratelimit_test.go) — verifies bucket stores `time.Time` from `time.Now()` and uses `Sub` (which honours the monotonic component). Documentation-style assertion: structurally walk the source AST to confirm no `time.Now().UnixNano()` cast that would strip monotonic, OR use a wall-clock-only injected clock and confirm a wall-clock-rewind does not unblock the window (FR-019, [contracts/api.md §7.5](specs/011-discord-bot/contracts/api.md)).

### Implementation for User Story 4

- [X] T053 [US4] Implement `bucketKey` struct + `bucketState` struct + `makeKey(req)` helper in [internal/discord/ratelimit.go](internal/discord/ratelimit.go) per [data-model.md §3.1–§3.2](specs/011-discord-bot/data-model.md).
- [X] T054 [US4] Implement `rateBucket` with `sync.Mutex`-guarded `map[bucketKey]bucketState` and the three operations `Acquire(key, now) → acquireResult`, `Commit(key)`, `Refund(key)` per [data-model.md §3.3](specs/011-discord-bot/data-model.md) and research [R-005](specs/011-discord-bot/research.md#r-005--rate-limit-semantics) — including the `pending != 0` "already pending" denial path. Implement in [internal/discord/ratelimit.go](internal/discord/ratelimit.go).
- [X] T055 [US4] Wire the rate-limit gate into `RequestApproval` in [internal/discord/bot.go](internal/discord/bot.go) — sequence: (1) available-flag check (US3), (2) `bucket.Acquire(makeKey(req), time.Now())` → `acquireDenied` returns `ErrRateLimited`, (3) render + deliver, (4) on delivery failure call `bucket.Refund(key)` and return `ErrDiscordUnavailable`, (5) on delivery success call `bucket.Commit(key)`. Use `defer` to guarantee Refund-on-leak per [data-model.md §3.3](specs/011-discord-bot/data-model.md) invariants ([contracts/api.md §4.2](specs/011-discord-bot/contracts/api.md) steps 2–5).
- [X] T056 [US4] Apply the `DMRateLimit ≤ 0 → DefaultDMRateLimit` defaulting in `NewBotApprover` and store the result in `*BotApprover.rateLimitWin`; pass it to the bucket on construction in [internal/discord/bot.go](internal/discord/bot.go) (FR-021).

**Checkpoint**: A misconfigured supervisor in a tight loop produces at most one prompt per window per `(SupervisorName, ClientIP)` (SC-004). Transport-down and delivery-failure paths leave the bucket untouched.

---

## Phase 7: User Story 5 — Bot credential is never exposed as plain text (Priority: P1)

**Goal**: The bot token is loaded once into `*SecureBytes` upstream, consumed by `NewBotApprover` via `Use(fn)` exactly at session-init time, and never appears in any log record, error string, audit event, configuration artifact, or test fixture (FR-022..FR-025, Constitution X). The discordgo SDK's internal string copy is the documented residual risk (research [R-003](specs/011-discord-bot/research.md#r-003--bot-token-ingestion-through-securebytes)).

**Independent Test**: Inject a unique 64-character sentinel as the bot token via SecureBytes; exercise every public entry point against the shim; capture all slog output, `err.Error()` strings, and audit-channel payloads; assert via `testutil.AssertSentinelAbsent` that the sentinel appears nowhere. Grep the test sources to confirm no fixture stores the token as a plain string.

### Tests for User Story 5 ⚠️ TDD — write FIRST, ensure they FAIL before implementation

- [X] T057 [US5] Write `TestBotApprover_TokenAbsentFromAllArtifacts` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — uses `testutil.SentinelSecret(11)` (unique 64-char string), wraps in `*securebytes.SecureBytes`, drives `NewBotApprover` + a full lifecycle (Ready → RequestApproval → Approve → audit-channel mirror → Disconnect → reconnect), captures slog buffer + every returned `err.Error()` + every `*discordgo.MessageSend` body sent to the shim, and asserts via `testutil.AssertSentinelAbsent` that the sentinel appears nowhere. The same test also greps the captured artifacts for the substring "Bot " + sentinel-prefix to catch the `discordgo.New("Bot " + tok)` concat path leaking through diagnostics (FR-024, [contracts/api.md §7.7](specs/011-discord-bot/contracts/api.md), [data-model.md §7 invariant 3](specs/011-discord-bot/data-model.md)).

### Implementation for User Story 5

- [X] T058 [US5] Implement bot-token ingestion via `cfg.Token.Use(fn)` exactly once in `NewBotApprover` in [internal/discord/bot.go](internal/discord/bot.go) — the closure performs `discordgo.New("Bot " + string(b))` and returns the constructed `*discordgo.Session`. The function-local `string` is reachable only on the closure stack until `Use` returns; the SecureBytes wrapper zeroes the closure's `[]byte` view per SDD-02 contract (research [R-003](specs/011-discord-bot/research.md#r-003--bot-token-ingestion-through-securebytes)). Document the residual risk in the godoc on `BotConfig.Token` and `NewBotApprover`.
- [X] T059 [US5] Verify the sentinel-error catalogue at [internal/discord/errors.go](internal/discord/errors.go) is static-message only (no token bytes, no req fields, no `cfg` fields) — Constitution X. Add a comment header documenting the no-payload rule. (Review-style task — no behaviour change beyond what T003 produced.)
- [X] T060 [US5] Implement the structured-logging surface in [internal/discord/bot.go](internal/discord/bot.go) and [internal/discord/monitor.go](internal/discord/monitor.go) — INFO on `session opened` / `available=true`; WARN on unexpected disconnect / rate-limit denial / audit-mirror failure / `Open()` failure; DEBUG on per-request flow keyed by request UUID. NEVER log `cfg.Token`, `session.Token`, the concatenated `"Bot " + tok` string, or any byte derived from these (Constitution X, US5 AS2).

**Checkpoint**: The unique-sentinel test passes — bot token appears in zero captured artifacts. The package's ingestion path keeps the credential in protected memory until the unavoidable SDK-side `string` copy.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Audit-channel mirroring (FR-008 — required by spec but not tied to a single user story), the race-clean concurrency assertion, the gate suite (`magex format:fix`, `magex lint`, `magex test:race`), and the coverage check.

### Audit-channel mirroring (FR-008, clarification 2026-04-30 Q4)

- [X] T061 [P] Write `TestAuditChannel_AllFiveLifecycleEventsMirrored` in [internal/discord/audit_test.go](internal/discord/audit_test.go) — drive `request_received`, `approved`, `denied`, `timed_out`, and `rate_limited` paths; assert one mirror payload per event lands on the shim's `auditChannelID` recorder; payload fields equal the DM fields minus the action buttons (FR-008).
- [X] T062 [P] Write `TestAuditChannel_FailureDoesNotBlockApproval` in [internal/discord/audit_test.go](internal/discord/audit_test.go) — shim's audit-channel `ChannelMessageSendComplex` returns an error; assert the primary `RequestApproval` flow still returns the approve decision in the expected timing window and a WARN slog record was emitted (FR-008, [contracts/api.md §7.10](specs/011-discord-bot/contracts/api.md)).
- [X] T063 [P] Write `TestAuditChannel_NoTokenInPayload` in [internal/discord/audit_test.go](internal/discord/audit_test.go) — sentinel-injection style assert that no audit payload contains the bot token (FR-024).
- [X] T064 [P] Write `TestAuditChannel_DisabledWhenIDEmpty` in [internal/discord/audit_test.go](internal/discord/audit_test.go) — `cfg.AuditChannelID = ""`; assert ZERO sends land on any audit channel for a full lifecycle (FR-008).
- [X] T065 Implement audit-channel mirror payload constructors in [internal/discord/render.go](internal/discord/render.go) — same field set as DM minus the action buttons, with a `type` field (`request_received` / `approved` / `denied` / `timed_out` / `rate_limited`); same redaction rules.
- [X] T066 Implement best-effort mirror dispatch in [internal/discord/bot.go](internal/discord/bot.go) — per-event goroutine bound to constructor `ctx`; calls `session.ChannelMessageSendComplex(auditChan, payload)`; failure logs WARN and exits; the primary `RequestApproval` flow does not wait. `request_received` fires post-Commit; the terminal event (`approved`/`denied`/`timed_out`) fires after the decision is returned to the caller; `rate_limited` fires before `RequestApproval` returns the sentinel ([contracts/api.md §4.2](specs/011-discord-bot/contracts/api.md), [data-model.md §5](specs/011-discord-bot/data-model.md)).

### Race-clean concurrency

- [X] T067 Write `TestBotApprover_RaceClean` in [internal/discord/bot_test.go](internal/discord/bot_test.go) — N concurrent `RequestApproval` goroutines (different `(SupervisorName, ClientIP)` keys, mixed Approve/Deny/Timeout outcomes), interleaved `TriggerDisconnect`/`TriggerReady`, ctx cancel partway through. Run under `-race`; asserts all goroutines exit cleanly and `monitorDone` closes (Constitution VIII race-clean, [data-model.md §1.4 Concurrency](specs/011-discord-bot/data-model.md)).

### Gate suite

- [X] T068 Run `magex format:fix` from repo root — the gofmt/goimports gate per `.github/CLAUDE.md` and chunk contract Prompt 5; fix any formatting drift. **Final phase per the chunk contract.**
- [X] T069 Run `magex lint` from repo root — the linter gate; resolve every reported issue. **Final phase per the chunk contract.**
- [X] T070 Run `magex test:race` from repo root — race-clean across the entire module; `internal/discord` must pass cleanly alongside upstream packages. **Final phase per the chunk contract.**
- [X] T071 Run `go test -cover ./internal/discord/` — assert coverage is **≥85%** per Constitution VIII Medium band (SC-007, chunk contract Prompt 5 step 2). If under target, add tests for any uncovered branch in `monitor.go` reconnect path or `bot.go` audit-mirror dispatcher.
- [X] T072 Run `grep -r "SECRET_SHOULD_NEVER_APPEAR" internal/discord/` — sanity check that the sentinel is referenced only in test sources (chunk contract Prompt 5 step 3); manually confirm no `*.go` outside `*_test.go` mentions a token-like literal.

### Documentation updates (post-implement, pre-commit)

- [X] T073 [P] Append "Exported API — locked at SDD-11" section to [docs/PACKAGE-MAP.md](docs/PACKAGE-MAP.md) under `internal/discord` listing the locked surface from [contracts/api.md §3–§6](specs/011-discord-bot/contracts/api.md) (chunk contract Prompt 5 step 5).
- [X] T074 [P] Update [docs/AC-MATRIX.md](docs/AC-MATRIX.md) AC-3 row with the new test file paths in `internal/discord/` (chunk contract Prompt 5 step 6).
- [X] T075 [P] Mark SDD-11 status `done` in [docs/SDD-PLAYBOOK.md](docs/SDD-PLAYBOOK.md) (chunk contract Prompt 5 step 7).

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: T001 must precede T002 (doc.go); both must precede Phase 2 since the package directory must exist.
- **Foundational (Phase 2)**: T003, T004 are parallelizable. T005 must follow T004 (uses the types). T006 must follow T005 (shim implements `sessionAPI`). T007 / T008 (tests) follow T004's type declarations. T009 (validation test) precedes T010 (validation impl) per TDD. T011 follows T010. **Phase 2 BLOCKS all user stories.**
- **User Stories (Phase 3+)**: All depend on Phase 2 completion.
  - US1 (Phase 3) is the MVP — implement first.
  - US2 (Phase 4) reuses US1's render dispatch; can run after US1 or in parallel after Phase 2 if a developer takes only the daemon-render slice.
  - US3 (Phase 5) implements the available-flag fast-path and reconnect — can run in parallel with US2 once Phase 2 is done.
  - US4 (Phase 6) depends on US3's available-flag (the fast-path runs BEFORE Acquire) but only on the API surface; the bucket logic is independently buildable after Phase 2.
  - US5 (Phase 7) is largely a guard test + documentation pass; its impl (T058) lives in the same `NewBotApprover` body as Phase 2's T011.
- **Polish (Phase 8)**: depends on Phases 3–7 to land their handler wiring.

### User Story Dependencies

- **US1 (P1)**: Phase 2 only.
- **US2 (P1)**: Phase 2 + the `renderApproval` dispatch from US1 (T017).
- **US3 (P1)**: Phase 2; independent of US1/US2 except that the in-flight-disconnect test (T026) needs `RequestApproval`'s delivery path from US1.
- **US4 (P2)**: Phase 2 + US3's available-flag fast-path (T040) for the transport-unavailable-no-consume invariant test (T049).
- **US5 (P1)**: Phase 2 only; the sentinel token test (T057) drives all of US1+US2+US3+US4 paths so pragmatically lands last.

### Within Each User Story

- Tests MUST be written and FAIL before their paired implementation task. The TDD ordering is the **explicit task ordering** in each story phase above.
- Models / types (Phase 2) before services (per-story implementation).
- Services before audit-mirror dispatch (Phase 8 cross-cutting layer).

### Parallel Opportunities

- T003 + T004 can run in parallel (different files, no dependency between sentinels and types).
- T007 + T008 can run in parallel (both in `approver_test.go`? — actually, both touch the same file, so NOT parallel; corrected: T007 and T008 are sequential within `approver_test.go`).
- T012 + T013 (US1 render tests) touch the same file — run sequentially.
- T021 + T022 + T023 (US2 render tests) touch the same file — run sequentially. They can run in parallel with US1 implementation tasks T016–T020 (different files) if a separate developer takes US2.
- T025–T029 (US3 monitor tests) all touch `monitor_test.go` — run sequentially. Can run in parallel with US3 bot tests T030–T036 (`bot_test.go`) on different files.
- T045–T052 (US4 bucket tests) touch `ratelimit_test.go` — sequential. Can run in parallel with US4 bot-wiring test T049/T050 if those land in `bot_test.go` (note: T049/T050 cross-cutting test target chosen to be `ratelimit_test.go` per the contract test list, so all sequential).
- T061–T064 (audit tests) all touch `audit_test.go` — sequential. T067 (race test) is in `bot_test.go`.
- T068, T069, T070 (gate suite) are sequential — `format:fix` first, then `lint`, then `test:race`.
- T073, T074, T075 (doc updates) touch different files — fully parallel.

---

## Parallel Example: User Story 3 (US3)

After Phase 2 completes, two developers can split US3:

```bash
# Developer A — monitor surface:
Task: "Write TestMonitor_DisconnectSurfacesUnavailable in monitor_test.go"
Task: "Write TestMonitor_DisconnectUnblocksInFlightRequest in monitor_test.go"
Task: "Write TestMonitor_ReconnectRestoresAvailability in monitor_test.go"
Task: "Write TestMonitor_ReconnectBackoffCappedAt60s in monitor_test.go"
Task: "Write TestMonitor_GoroutineExitsOnCtxCancel in monitor_test.go"
# Then the impl tasks T037–T041, T044 in monitor.go.

# Developer B — Constitution-II AST + decision routing:
Task: "Write TestBotApprover_NeverAutoApprovesOnDiscordError in bot_test.go"
Task: "Write TestBotApprover_NoAutoApproveKnobExists in bot_test.go"
Task: "Write TestBotApprover_DisconnectFastPath in bot_test.go"
Task: "Write TestNewBotApprover_BootDownStartsUnavailable in bot_test.go"
Task: "Write TestDecisionRouting_Deny in bot_test.go"
Task: "Write TestDecisionRouting_Timeout in bot_test.go"
Task: "Write TestDecisionRouting_CtxCancelled in bot_test.go"
# Then the impl tasks T040, T042, T043 in bot.go.
```

The two developers integrate at T044 (`monitorDone` close + `session.Close()`) which closes the loop on race-clean.

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Phase 1: T001, T002 — dependency landed, package skeleton in place.
2. Phase 2: T003–T011 — types, sentinels, validation, shim, constructor stub. **STOP and run the foundational tests to confirm validation works.**
3. Phase 3: T012–T020 — interactive approval end-to-end against the shim. **STOP and run `go test -run TestDecisionRouting_Approve ./internal/discord/`** — if green, US1 is the MVP.
4. **STOP. Validate AC-3 partially: an interactive approval flow gates the secret claim.** Daemon-mode and rate-limit are not yet there but can be added incrementally.

### Incremental Delivery

1. MVP = Phase 1 + Phase 2 + US1 (interactive approval works).
2. Add US2 (daemon rendering).
3. Add US3 (fail-closed + Constitution-II load-bearing tests). After this phase, AC-3 is fully satisfied for the no-auto-approve invariant.
4. Add US4 (rate limiting). Now SC-004 is satisfied.
5. Add US5 (token-confidentiality sentinel test). Now SC-005 is satisfied.
6. Phase 8 polish (audit channel + race-clean + gates + coverage). All success criteria green.

### Parallel Team Strategy

With three developers post-Phase 2:

- Developer A: US1 (Phases 3) → US4 (Phase 6).
- Developer B: US2 (Phase 4) → US5 (Phase 7) (US5's primary task is one big test).
- Developer C: US3 (Phase 5) — the monitor + Constitution-II guards, the highest-cost slice.
- All three converge on Phase 8 (audit + race + gates + docs). T073/T074/T075 split three ways.

---

## Notes

- **TDD-mandatory**: every test task is scheduled BEFORE its paired implementation task — Constitution VIII non-negotiable. Verify each test FAILS before writing the implementation.
- **Coverage target**: ≥85% on `internal/discord/` per Constitution VIII Medium band. Gate enforced by T071.
- **Final-phase gates (chunk contract Prompt 5)**: T068 (`magex format:fix`), T069 (`magex lint`), T070 (`magex test:race`) — all three MUST pass clean before the combined commit.
- **No live Discord in tests**: every test exercises the package via the `session_shim_test.go` programmable shim. The package's production source imports `discordgo`; test files import `discordgo` only for the type declarations the shim references.
- **Combined commit**: per chunk contract, all changes for SDD-11 land in one combined commit at the end of the implement phase. Do NOT commit between phases.
- **No-auto-approve invariant**: T032 (`TestBotApprover_NeverAutoApprovesOnDiscordError`) and T033 (`TestBotApprover_NoAutoApproveKnobExists`) are the load-bearing Constitution-II tests. Treat any failure on either as a release-blocker.
- **Rate-limit refund discipline**: T055 (`defer bucket.Refund(...)` cancelled by an explicit `bucket.Commit(...)` on success) is the sole guard against the FR-021a invariant leaking. Review the implementation by hand before declaring T055 complete.
- **Token absent from artifacts**: T057 is the canonical Constitution-X test. Add new captured-artifact dimensions (e.g., a future audit format) to that test as they land.
