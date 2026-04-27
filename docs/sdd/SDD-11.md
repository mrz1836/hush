# SDD-11 — `internal/discord` (Approver interface + bot connection + disconnect monitoring)

**Phase:** 3
**Package:** `internal/discord`
**Files:** `bot.go`, `approver.go`, `monitor.go`, `render.go`, `ratelimit.go`, `*_test.go`
**Branch:** `011-discord-bot` (created by the `before_specify` git hook)
**Blocked by:** SDD-05, SDD-06, SDD-10
**Blocks:** SDD-12, SDD-28
**Primary AC:** AC-3
**Coverage target:** 85%

**Behaviour contracts (MUST):**
- `BotConfig` fields: `Token *securebytes.SecureBytes`, `OwnerID string`, `AppID string`, `AuditChannelID string` (optional), `DMRateLimit time.Duration`
- Bot token read once via `internal/keychain` wrapper, immediately wrapped in `SecureBytes`
- Use `github.com/bwmarrin/discordgo` for connection + interaction handlers
- DM rendering: distinct visual labels for INTERACTIVE vs `[DAEMON]`
- `RequestApproval` blocks until user clicks Approve/Deny OR ctx times out OR `ErrDiscordUnavailable`
- WebSocket unexpected close → `ErrDiscordUnavailable` (caller maps to 503)
- Rate limiter is per-(supervisor name + machine fingerprint) keyed; default 1 per 5 min

**Anti-contracts (MUST NOT):**
- Read bot token from env var
- Auto-approve under any circumstance (Constitution II non-negotiable)
- Use `init()`
- Hold `ctx` in struct field

**Tests required:**
- Unit: `TestApprovalRender_InteractiveLabel`, `TestApprovalRender_DaemonLabel`, `TestRateLimit_BlocksSecondPromptWithin5Min`, `TestDecisionRouting_ApproveDenyTimeout` (uses fake `discordgo.Session`; no live Discord)
- Race: monitor goroutine race-clean

**Constitutional principles in scope:** II (no auto-approve), V (operator visibility via Discord), VIII, IX (no `init`, no ctx in struct), X (token never in logs)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `type Approver interface { RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error) }`
- `type ApprovalRequest struct { MachineName, ClientIP, Reason string; Scope []string; RequestedTTL time.Duration; SessionType token.SessionType; SupervisorName string }`
- `type Decision struct { Approved bool; ApprovedTTL time.Duration; Reason string }`
- `type BotApprover struct { ... }`
- `func NewBotApprover(ctx context.Context, cfg BotConfig, logger *slog.Logger) (*BotApprover, error)`
- `var ErrDiscordUnavailable, ErrApprovalDenied, ErrApprovalTimeout, ErrRateLimited`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-11 (internal/discord:
Approver interface + bot + disconnect monitoring) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles II, V, VIII, IX, X — no auto-approve, operator visibility, etc.)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-7, FR-19, AC-3)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Discord trust boundary, bot disconnect monitoring)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  ([discord] section + supervisor_dm_rate_limit)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-3 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-11.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/discord package owns the Discord-backed approval flow:
the Approver interface every approval consumer (SDD-12 /claim
handler) implements against, the BotApprover backed by a real
Discord bot, the WebSocket disconnect monitor that fails closed,
and the per-supervisor rate limiter that prevents prompt floods.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The Approver interface gates every secret claim. There is NO
  configuration knob, env var, or code path that auto-approves
  on Discord failure (Constitution II non-negotiable).
- A WebSocket disconnect immediately surfaces ErrDiscordUnavailable
  to in-flight RequestApproval calls; callers map that to a 503.
- DM rendering visually distinguishes INTERACTIVE requests
  (human at terminal) from SUPERVISOR/[DAEMON] requests (long-
  running daemon refilling its token), so the operator never
  approves the wrong kind of request by mistake.
- A rate limiter prevents a misconfigured supervisor from
  flooding the operator's DMs (default 1 prompt per 5 minutes
  per supervisor + machine pair).
- The bot token is loaded once from the OS keychain, wrapped
  in SecureBytes, and never exposed as a string anywhere.

The spec MUST NOT encode HOW (no library names beyond stdlib
references, no specific Discord SDK choice). Those are plan-phase.

Acceptance criterion: AC-3 (Discord approver gates every claim).

Action — run exactly one command:
  /speckit-specify "internal/discord: Approver interface + Discord-backed BotApprover (loads token from OS keychain into SecureBytes); DM rendering distinguishes INTERACTIVE vs [DAEMON] requests; per-supervisor + machine rate limiter; WebSocket disconnect surfaces ErrDiscordUnavailable (caller returns 503, never auto-approves)"

The before_specify hook will create branch 011-discord-bot.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-11 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-11.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-11 (internal/discord) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; II/V/VIII/IX/X load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-7, FR-19, AC-3)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Discord trust boundary, bot disconnect monitoring — read carefully)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  ([discord] section)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/discord — the API contract you will lock)
- /Users/mrz/projects/hush/docs/sdd/SDD-11.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/discord
- Files: bot.go (connection lifecycle), approver.go (Approver
  interface — but note SDD-10 already declares it; this chunk
  provides BotApprover impl), monitor.go (WebSocket health),
  render.go (DM templates), ratelimit.go, bot_test.go,
  approver_test.go, monitor_test.go, render_test.go,
  ratelimit_test.go
- Exported API:
    type Approver interface { RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error) }   // re-exported from SDD-10's declaration
    type ApprovalRequest struct { MachineName, ClientIP, Reason string; Scope []string; RequestedTTL time.Duration; SessionType token.SessionType; SupervisorName string }
    type Decision struct { Approved bool; ApprovedTTL time.Duration; Reason string }
    type BotConfig struct { Token *securebytes.SecureBytes; OwnerID, AppID, AuditChannelID string; DMRateLimit time.Duration }
    type BotApprover struct { ... }
    func NewBotApprover(ctx context.Context, cfg BotConfig, logger *slog.Logger) (*BotApprover, error)
    var ErrDiscordUnavailable, ErrApprovalDenied, ErrApprovalTimeout, ErrRateLimited

Implementation contract (HOW — locked):
- Discord SDK: github.com/bwmarrin/discordgo. Constitution XI:
  this dep needs a Phase-0 research note confirming it's the
  pinned version.
- BotApprover holds a *discordgo.Session, the rate limiter, a
  monitor goroutine started via NewBotApprover (cancelled
  via the ctx passed to NewBotApprover; document this lifecycle
  contract in the godoc).
- monitor.go subscribes to discordgo's connection events;
  unexpected close transitions an internal "available" flag
  to false; RequestApproval checks the flag first and returns
  ErrDiscordUnavailable immediately if down.
- render.go produces two distinct DM templates — INTERACTIVE
  has a green-checkmark prefix, [DAEMON] has a yellow-warning
  prefix and explicit machine + supervisor name. Never include
  bot token, secret name, or secret value in templates.
- ratelimit.go uses a token bucket per (SupervisorName +
  ClientIP) key. Default 1 per 5 min from cfg.DMRateLimit.
  Excess attempts return ErrRateLimited (caller decides how
  to surface).
- BotConfig.Token is *securebytes.SecureBytes; the discordgo
  session reads the raw token via Use(fn) at session-init
  time and the function-local []byte is zeroed by the SDK
  callback wrapper (document this contract).
- Tests use a fake discordgo.Session shim — NEVER hit live
  Discord in tests.

Coverage target: 85%.
Constitutional principles in scope: II, V, VIII, IX, X, XI.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-11 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-11.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 85%. Tests required: TestApprovalRender_InteractiveLabel, TestApprovalRender_DaemonLabel, TestApprovalRender_NeverIncludesToken, TestRateLimit_BlocksSecondPromptWithin5Min, TestRateLimit_AllowsAfterWindow, TestDecisionRouting_Approve, TestDecisionRouting_Deny, TestDecisionRouting_Timeout, TestMonitor_DisconnectSurfacesUnavailable, TestBotApprover_NeverAutoApprovesOnDiscordError. All tests use a fake discordgo session shim — NEVER hit live Discord. Final phase MUST include magex format:fix, magex lint, magex test:race."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-11 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-11.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 85% on internal/discord:
     go test -cover ./internal/discord/
3. Confirm bot token never appears as a string in any test fixture
   or captured log (manual grep over the test files).
4. Confirm TestBotApprover_NeverAutoApprovesOnDiscordError proves
   the no-auto-approve invariant from Constitution II.
5. Append "Exported API — locked at SDD-11" section to
   docs/PACKAGE-MAP.md under internal/discord listing the locked
   API from the chunk doc.
6. Update docs/AC-MATRIX.md AC-3 row with the new test file paths.
7. Mark SDD-11 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add internal/discord/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(discord): Approver + BotApprover + disconnect monitor + rate limiter (SDD-11)"

Final message: confirm gates passed, race-clean, coverage ≥ 85%,
bot token absent from all fixtures and logs, no-auto-approve
invariant proven, AC-3 row updated, SDD-PLAYBOOK updated, and
the combined commit created.
```
