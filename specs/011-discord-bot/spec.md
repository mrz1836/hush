# Feature Specification: Discord-Backed Approver

**Feature Branch**: `011-discord-bot`
**Created**: 2026-04-30
**Status**: Draft
**Input**: User description: "internal/discord: Approver interface + Discord-backed BotApprover (loads token from OS keychain into SecureBytes); DM rendering distinguishes INTERACTIVE vs [DAEMON] requests; per-supervisor + machine rate limiter; WebSocket disconnect surfaces ErrDiscordUnavailable (caller returns 503, never auto-approves)"

## Clarifications

### Session 2026-04-30

- Q: When the chat transport cannot connect at vault server startup, what should the approver do? → A: Start in unavailable state; run a background reconnect loop with bounded exponential backoff; recover automatically when transport comes up. Vault server starts; in-flight claims receive the transport-unavailable sentinel (caller maps to HTTP 503). No operator-driven restart required.
- Q: How does the operator's input populate Decision.ApprovedTTL and Decision.Reason in v0.1.0? → A: Approve/Deny buttons only. ApprovedTTL equals the requested TTL exactly; Reason is empty. The Decision fields remain in the exported API for forward compatibility, but no modal/short-TTL/reason UX ships in v0.1.0.
- Q: After an unexpected WebSocket disconnect at runtime, what is the reconnect policy? → A: Indefinite exponential backoff with a 60s cap between attempts. The approver stays in the unavailable state (returning the transport-unavailable sentinel to all callers) until the WebSocket Ready event fires, at which point it flips back to available. No bounded give-up; no operator restart required.
- Q: When an audit channel is configured, which events are mirrored and with what content? → A: Mirror all five lifecycle events — request received, approved, denied, timed out, rate-limited — with the same field set as the operator DM minus the action buttons. The same redaction rules apply: no bot-token value, no secret value, no key material. Mirroring remains best-effort and never blocks or alters the primary approval flow.
- Q: When transport is unavailable and the approver returns the transport-unavailable sentinel without delivering a prompt, does the attempt consume the rate-limit token for its key? → A: No. Only attempts whose prompt is successfully delivered to the operator consume the token. Transport-unavailable returns (and any other path that never delivers) leave the token bucket untouched, so a supervisor that retried during a Discord outage is not already rate-limited at recovery.

## Overview

The Discord approver chunk owns the human-in-the-loop gate that stands between every secret claim and the vault. It defines the single approval contract that the vault server's claim handler invokes whenever a client requests a fresh secret session, and it provides the production implementation of that contract: a Discord-bot-backed approver that DMs the configured operator, surfaces approve/deny buttons distinguishing interactive (human at terminal) from daemon (long-running supervisor) requests, monitors its own connection and fails closed when the chat transport is unavailable, and rate-limits per supervisor + machine pair so a misconfigured daemon cannot flood the operator's phone. The bot token is loaded once from the operating system keychain into protected memory and is never exposed as an ordinary string anywhere in the system.

This chunk is the operator's experience of approving a secret. Every guarantee here is acceptance-level: there is no operator-visible behavior left to plan-phase decisions about which Discord client library is used or how the connection state machine is encoded. The spec encodes the *what* — that approval is human, that failure is loud, that the operator never approves the wrong kind of session by mistake — and leaves the *how* to the planning phase.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Operator approves an interactive shell session (Priority: P1)

The operator is on their phone. A developer at a workstation runs an interactive secret request to wrap a shell. The vault server, before issuing any token, asks the approver to gate the request. The operator receives a direct message that clearly identifies this as a *human/interactive* request — labelled with an interactive marker, showing the requesting machine name, the client's mesh IP, the requested scope, the human-supplied reason, and the requested time-to-live. The DM exposes Approve and Deny actions. The operator taps Approve; the approver returns an approved decision; the vault server proceeds to issue the session token.

**Why this priority**: This is the single most common path through the system and the canonical realization of acceptance criterion AC-3 ("Discord approver gates every claim"). Without it, the vault never issues an interactive session.

**Independent Test**: With a fake chat-platform stub standing in for the live transport, request approval for an interactive session, simulate the operator pressing Approve, and verify the approver returns a decision marked approved with the requested time-to-live. No live chat-platform connection is required.

**Acceptance Scenarios**:

1. **Given** the chat transport is connected and the configured operator is reachable, **When** the vault server requests approval for an interactive session with a specified machine name, mesh IP, scope, reason, and requested time-to-live, **Then** the approver delivers a direct message visually marked as interactive, including all five fields, with Approve and Deny actions.
2. **Given** the operator presses Approve within the request's deadline, **When** the action is received, **Then** the approver returns a decision marked approved with the approved time-to-live equal to the originally requested time-to-live and an empty operator-supplied reason (per FR-004's v0.1.0 UX constraint).
3. **Given** the operator presses Deny, **When** the action is received, **Then** the approver returns an explicit denial decision, and the caller (the vault claim handler) does not issue a token.

---

### User Story 2 — Operator approves a long-running daemon's refill (Priority: P1)

A long-running daemon supervisor on an agent host needs a fresh session token (initial bootstrap, refresh window, or post-stale recovery). The supervisor asks the approver to gate the refill. The operator receives a direct message that is *visually distinct* from an interactive request — labelled with a daemon marker (for example, `[DAEMON]`), showing the supervisor's configured name in addition to the machine name, IP, scope, reason, and requested time-to-live. The visual distinction is unmistakable: the operator never confuses a daemon refill (which carries a longer TTL and a different operational meaning) for an interactive shell.

**Why this priority**: A daemon refill is approved with much longer time-to-live than a one-shot interactive session. Mistakenly approving a daemon refill thinking it was a shell — or vice versa — would either grant too much credit or trigger churn. Visual disambiguation at the prompt is the only place where the operator can catch the difference before the token is issued.

**Independent Test**: With the fake chat-platform stub, request approval for a daemon-typed session that includes a supervisor name, capture the rendered direct message, and assert that the daemon marker is present, that the supervisor name is shown, and that the rendering differs in a human-discernible way from the interactive rendering produced by Story 1.

**Acceptance Scenarios**:

1. **Given** an approval request is tagged as a supervisor (daemon) session and includes a supervisor name, **When** the approver renders the prompt, **Then** the prompt carries a daemon marker, displays the supervisor name, and is visually distinct from an interactive prompt for the same machine.
2. **Given** a daemon prompt and an interactive prompt are placed side by side in the operator's DM history, **When** the operator scans them, **Then** they can tell which is which without reading any field other than the marker.

---

### User Story 3 — Approval fails closed when the chat transport is unavailable (Priority: P1)

The chat-platform connection unexpectedly drops while a vault server is running. A new claim arrives. The approver does *not* fall back to any auto-approve, queued-for-later, or fail-open behavior. The approver immediately returns a sentinel "transport unavailable" error to the caller, and the vault server's claim handler maps that error to an HTTP `503 Service Unavailable` response. In-flight `RequestApproval` calls that were waiting for an operator action also surface the same sentinel error so callers can return `503` rather than block forever.

**Why this priority**: Constitution Principle II is non-negotiable: "Discord bot unavailability MUST return HTTP 503 to the client; the server MUST NOT fall back to auto-approve under any circumstances." This is the load-bearing safety property of the entire chunk. If this story fails, the system silently bypasses the human-approval gate, which is the single mistake that defeats hush as a product.

**Independent Test**: With the fake chat-platform stub, simulate an unexpected disconnect, then request approval. Assert the approver returns the transport-unavailable sentinel error within 100 ms (i.e., does not block), and assert that no decision marked approved was returned. Repeat with a request that was *already in flight* when the disconnect occurred and assert the same sentinel is returned.

**Acceptance Scenarios**:

1. **Given** the approver has detected an unexpected loss of the chat-platform connection, **When** a new approval request arrives, **Then** the approver returns the transport-unavailable sentinel error without prompting the operator and without blocking on a deadline.
2. **Given** an approval request is in flight and waiting for the operator's action, **When** an unexpected disconnect occurs, **Then** the in-flight request unblocks with the transport-unavailable sentinel error.
3. **Given** the chat transport later reconnects, **When** subsequent approval requests arrive, **Then** the approver resumes normal operation (no degraded mode persists past reconnection).
4. **Given** the configuration file, environment, or any runtime knob, **When** any value is changed, **Then** there is no setting that causes the approver to return an approved decision while the chat transport is reported unavailable.

---

### User Story 4 — A misconfigured daemon cannot flood the operator (Priority: P2)

A supervisor is misbehaving — for example, looping on bootstrap because the vault server is briefly unreachable — and is repeatedly asking the approver to prompt the operator. The approver applies a per-(supervisor name, machine) rate limit: after one prompt, further prompts within the rate-limit window are rejected with a distinct "rate-limited" sentinel error rather than delivered to the operator. The caller decides how to surface that error (the supervisor will typically wait and retry per its own bootstrap-retry policy). The default rate-limit window is one prompt per five minutes per supervisor + machine pair; the window is configurable.

**Why this priority**: Operator-trust is a finite resource. If the operator's phone receives ten daemon prompts in a minute they will start ignoring or auto-approving, which trains them to defeat Principle II by reflex. Suppressing flood is a usability *and* security requirement.

**Independent Test**: With the fake chat-platform stub, send two approval requests for the same (supervisor name, machine) pair within the configured window; assert the first results in a delivered prompt and the second returns the rate-limited sentinel without delivery. Wait past the window and send a third; assert it is delivered.

**Acceptance Scenarios**:

1. **Given** an approval request for a given (supervisor name, machine) pair has been delivered, **When** a second request for the same pair arrives within the configured rate-limit window, **Then** the approver returns the rate-limited sentinel error and does not deliver a prompt.
2. **Given** the rate-limit window has elapsed since the last delivered prompt, **When** another request for the same pair arrives, **Then** it is delivered normally.
3. **Given** two requests arrive for *different* (supervisor name, machine) pairs within the window, **When** they are processed, **Then** both are delivered (rate limiting is per-pair, not global).
4. **Given** an interactive (non-supervisor) request, **When** it arrives, **Then** rate limiting is keyed on the requesting machine alone (interactive requests do not carry a supervisor name) and applies the same per-window default.

---

### User Story 5 — Bot credential is never exposed as plain text (Priority: P1)

The bot identity is held by a long-lived bot credential ("bot token") that, if leaked, lets an attacker impersonate the approver. The approver loads this credential exactly once at startup from the operating-system keychain, immediately wraps it in protected memory (the project's `SecureBytes` mlocked-and-zeroing buffer type), and from that point on never converts it to an ordinary string. Logs, error messages, audit events, and test fixtures never contain the credential's value. The credential is read out of protected memory only at the moment a chat-transport session needs it; the temporary plain-bytes view is zeroed after use.

**Why this priority**: A leaked bot token is the specific failure mode described in `docs/SECURITY.md` §1.4 ("Discord bot token stolen → auto-approve sessions"). The mitigation listed there is exactly this story: keychain-backed, ACL-restricted, never an environment variable, never a string in process. Failing this story silently undermines every other layer.

**Independent Test**: A fixture-and-log audit: instantiate the approver against a fake chat-platform stub, exercise every public approver entry point, capture all emitted log records, audit events, and error strings; assert the configured bot-token value (a known sentinel injected via the keychain stub) appears nowhere in the captured output. Additionally, search the test sources to confirm no test fixture stores the bot-token value as a plain string field.

**Acceptance Scenarios**:

1. **Given** the keychain holds the bot-token value, **When** the approver starts up, **Then** the value is loaded once from the keychain, wrapped in protected memory, and the keychain is not read again during the approver's lifetime.
2. **Given** the approver is operating, **When** any log record, error, or audit event is produced, **Then** the bot-token value is not present in any field of any such record.
3. **Given** environment variables, configuration files, or command-line arguments, **When** any of them are inspected, **Then** none of them contain or are configured to contain the bot-token value (only a keychain item name is acceptable as configuration).

---

### Edge Cases

- **Operator does not respond before the request's deadline.** The approver returns a distinct "approval timed out" sentinel error. The caller maps this to an appropriate HTTP response (typically `408`) — not `503` (which is reserved for transport unavailability) and not an approved decision.
- **Operator presses Deny.** The approver returns a distinct "approval denied" sentinel error with the operator's optional reason if one was supplied through the chat platform. The caller MUST NOT retry automatically.
- **Chat-platform connection is *known to be down* at startup.** The vault server starts normally; the approver enters an "unavailable" state and runs a background reconnect loop with bounded exponential backoff. Claims that arrive while the transport is unavailable receive the transport-unavailable sentinel (HTTP `503`), not an auto-approve and not a startup error. The approver resumes normal operation as soon as the reconnect succeeds. No operator restart is required.
- **Two operator actions race** (operator double-taps Approve, or presses Approve and then Deny in quick succession). The approver honours the first received action and ignores the rest for that request.
- **Approval request arrives for a supervisor session whose configured rate-limit value is missing or zero.** The approver applies the default (one per five minutes) rather than treating zero as "unlimited."
- **Rate-limit clock skew or system suspend.** Rate-limit windows are computed against the system's monotonic clock so that suspending the host or wall-clock changes do not let a flood through.
- **Operator's chat client and the request's machine identifier disagree** (e.g., the request claims to come from `darwin-laptop` but the operator does not recognize that name). The approver does not fact-check; it renders what the caller supplied. The operator's denial path is the recourse. (This is a Principle II / V property: failures are loud to the operator, and the human is the gate.)
- **The audit channel is configured but unreachable.** Audit-channel mirroring is best-effort and does not block or fail the approval flow.

## Requirements *(mandatory)*

### Functional Requirements

#### Approval contract

- **FR-001**: The system MUST expose a single approval contract (an `Approver`-shaped operation) that takes a context, a structured approval request, and returns either an approved decision or one of a fixed, named set of failure conditions: transport unavailable, denied, timed out, rate limited.
- **FR-002**: Every secret-claim path in the vault server MUST invoke the approval contract before issuing a session token. There MUST be no code path, configuration value, environment variable, command-line flag, or build tag that causes a session token to be issued without first receiving an approved decision from the approval contract.
- **FR-003**: The approval request MUST carry, at minimum: requesting machine name, requesting client mesh IP, human-readable reason, requested scope (the list of secret names), requested time-to-live, session type (interactive or supervisor/daemon), and — for supervisor-typed requests — the supervisor's configured name.
- **FR-004**: The approved decision MUST carry: an approval flag, the approved time-to-live, and an optional operator-supplied reason. In v0.1.0 the operator UX is Approve/Deny only: the approved time-to-live equals the requested time-to-live exactly, and the operator-supplied reason is empty. The fields remain part of the exported API for forward-compatible UX (e.g., a future shorten-TTL modal).

#### Operator visibility

- **FR-005**: The system MUST deliver the approval prompt to the configured operator's direct-message channel on the chat platform, including a visible Approve action and a visible Deny action.
- **FR-006**: The system MUST render an interactive (human-at-terminal) request and a supervisor (daemon) request with *visibly distinct* prompts. The distinction MUST be discernible to the operator from the prompt header alone, without reading every field. The supervisor prompt MUST include the supervisor's configured name in addition to the machine name.
- **FR-007**: The rendered prompt MUST display the approval-request fields listed in FR-003 in a human-readable form. The prompt MUST NOT contain the bot-token value, any secret value, or any cryptographic key material.
- **FR-008**: When an audit channel is configured, the approver MUST mirror all five approval lifecycle events to that channel: request received, approved, denied, timed out, and rate-limited. Each mirrored event MUST carry the same operator-visible fields as the corresponding DM (machine name, mesh IP, scope, reason, requested time-to-live, session type, and — for supervisor sessions — supervisor name) but MUST NOT include the action buttons. Mirroring MUST apply the same redaction rules as DM rendering: no bot-token value, no secret value, no cryptographic key material. Mirroring is best-effort: a failure to deliver to the audit channel MUST NOT block, fail, or alter the primary approval flow, and the on-disk hash-chained audit log remains the authoritative record.

#### Fail-closed semantics

- **FR-009**: When the chat-platform transport is in an unexpected-disconnect state, the approval contract MUST immediately return a transport-unavailable sentinel error, without prompting the operator and without blocking on a deadline.
- **FR-010**: When an approval request is in flight (already prompted, waiting for the operator's action) and the chat-platform transport unexpectedly disconnects, the in-flight request MUST unblock with the transport-unavailable sentinel error.
- **FR-011**: The vault server claim handler MUST map the transport-unavailable sentinel to an HTTP `503 Service Unavailable` response.
- **FR-012**: There MUST NOT exist any configuration knob, command-line flag, build flag, or runtime mode that causes the approval contract to return an approved decision while the chat transport is reported unavailable. Auto-approve mode does not exist.
- **FR-013**: When the chat-platform transport reconnects after an unexpected disconnect, the approver MUST resume normal operation. No degraded mode is allowed to persist past reconnection.
- **FR-013a**: The approver MUST NOT fail vault server startup when the chat transport cannot connect at boot. Instead, it MUST start in the unavailable state defined by FR-009 and run a background reconnect loop that resumes normal operation as soon as the transport becomes reachable. Operator intervention (manual restart, configuration change) MUST NOT be required for the approver to recover from a startup-time or runtime transport outage.
- **FR-013b**: The reconnect loop MUST use exponential backoff with a 60-second cap between attempts and MUST retry indefinitely (no bounded give-up). The approver MUST remain in the unavailable state between attempts and MUST transition to available only on the underlying transport's "ready/connected" signal.

#### Decision routing

- **FR-014**: When the operator presses Approve within the request's deadline, the contract MUST return an approved decision and MUST NOT continue accepting late actions on the same request.
- **FR-015**: When the operator presses Deny, the contract MUST return a denied sentinel error. Denied requests MUST NOT be retried automatically by any caller.
- **FR-016**: When the request's deadline passes without an operator action, the contract MUST return a timed-out sentinel error. The deadline is supplied by the caller via context.
- **FR-017**: If the operator submits multiple actions for the same request (e.g., double-tap), the contract MUST honour the first action received and ignore the rest for that request.

#### Rate limiting

- **FR-018**: The approver MUST rate-limit prompt delivery on a per-(supervisor name, machine) key for supervisor requests and on a per-machine key for interactive requests. The default window is one prompt per five minutes; the value MUST be configurable.
- **FR-019**: Rate-limit windows MUST be computed against a monotonic clock so that suspending the host or changing the wall clock does not allow a flood through.
- **FR-020**: When a request would exceed the rate-limit window for its key, the contract MUST return a rate-limited sentinel error and MUST NOT deliver a prompt.
- **FR-021**: A configured rate-limit value of zero or missing MUST be interpreted as "use the default," not "unlimited."
- **FR-021a**: Only attempts whose prompt is successfully delivered to the operator MUST consume a token from the rate-limit bucket for their key. Attempts that return the transport-unavailable sentinel (FR-009/FR-010), or that fail before delivery for any other reason, MUST NOT consume the token. This keeps a supervisor that retried during a transport outage from arriving at recovery already rate-limited, delaying the operator's first visibility into a real claim.

#### Bot-credential handling

- **FR-022**: The bot token MUST be loaded exactly once, at approver startup, from the configured operating-system keychain entry. The keychain entry name (not the value) is the only acceptable configuration.
- **FR-023**: After load, the bot token MUST live in the project's protected-memory wrapper (mlocked, zeroed on free). It MUST NOT be converted to an ordinary string at any point in the approver's lifetime.
- **FR-024**: The bot-token value MUST NOT appear in any log record, error message, audit event, configuration file, environment variable, command-line argument, or test fixture.
- **FR-025**: If the chat-platform client library requires a plain-bytes view of the token to initialize a session, the view MUST be obtained through the protected-memory wrapper's controlled-access mechanism and the temporary buffer MUST be zeroed before release.

#### Lifecycle

- **FR-026**: The approver MUST be constructed with an explicit context whose cancellation terminates all background goroutines (notably the connection-monitor goroutine). No background goroutine may outlive the context passed to the constructor.
- **FR-027**: The approver MUST NOT use package-level mutable state, package `init()` side effects, or context fields stored on long-lived structs.

### Key Entities

- **Approval request**: A structured message describing a pending claim — fields per FR-003.
- **Decision**: The successful outcome of an approval — fields per FR-004.
- **Approver**: The contract a caller invokes to gate a claim.
- **Bot-token reference**: A keychain entry name pointing to the long-lived bot credential. The value is never present in this entity's representation.
- **Rate-limit key**: A `(supervisor name, machine)` tuple for supervisor requests, a `machine` value for interactive requests.
- **Sentinel error set**: The fixed set of named failure conditions the contract may return — transport unavailable, denied, timed out, rate limited — each distinct so callers can map to distinct HTTP status codes and operator messages.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of code paths that issue a session token first await an approved decision from the approval contract — measured by code review and by a test that exhausts every claim entry point and asserts an approver call precedes token issuance.
- **SC-002**: When the chat transport is unavailable, the approval contract returns the transport-unavailable sentinel within 100 ms in 100% of calls — measured by an integration test that simulates disconnect and asserts time-to-error.
- **SC-003**: The operator distinguishes interactive from daemon prompts at a glance — measured by a rendering test that asserts the daemon marker is present in supervisor prompts, absent in interactive prompts, and that the supervisor name is rendered for daemon prompts.
- **SC-004**: A misconfigured supervisor that requests approval in a tight loop (e.g., once per second) results in at most one delivered prompt per configured rate-limit window per (supervisor, machine) pair — measured by a rate-limit test that submits N requests and asserts exactly one delivery.
- **SC-005**: The bot-token value never appears in any captured log record, error message, audit event, configuration artifact, or test fixture — measured by a sentinel-leak test that injects a known-unique token via the keychain stub and greps every captured artifact for it.
- **SC-006**: There is zero configuration surface (knob, flag, env var, build tag) that causes the contract to return an approved decision while the transport is unavailable — measured by an exhaustive search of configuration code paths and by a regression test asserting no such knob exists.
- **SC-007**: Test coverage of the approver package is at least 85% — measured by the project's test-coverage tooling per the package's coverage policy.

## Assumptions

- The configured operator (the single owner identifier) is reachable on a 2FA-protected phone with a working chat client and notifications enabled. v0.1.0 supports exactly one configured approver; multi-owner approval is explicitly out of scope (`docs/SPEC.md` §7).
- The chat platform is treated as transport, not as a security boundary: the approval *decision* is trusted only because it comes from the operator's authenticated chat session, which is itself protected by the operator's device security.
- The vault host's operating-system keychain is available, supports per-binary access-control restriction, and holds the bot-token entry under a configured name. Keychain availability and ACL enforcement are owned by upstream chunks (SDD-05, SDD-15); this chunk depends on, but does not implement, those primitives.
- The protected-memory wrapper from the project's vault crypto package is available and provides a controlled-access mechanism that delivers a transient plain-bytes view to a closure and zeros it after the closure returns. (This chunk depends on SDD-02.)
- The chat-platform client surface used in plan-phase exposes connection-state events (a "ready" event and an "unexpected disconnect" event the approver can listen on). If this turns out to require polling, the spec's fail-closed-within-100-ms criterion is still binding and the plan must meet it some other way.
- Tests for this chunk run against an in-process fake chat-platform stub. No test ever opens a live connection to the real chat platform.
- The audit channel mirroring path (when configured) is a convenience; the authoritative audit record remains the on-disk hash-chained audit log owned by chunk SDD-13.
- The "machine" component of the rate-limit key is the `MachineName` field carried in the approval request. Stronger client identity (the registered client public-key fingerprint) is available from upstream chunks but is not required for the rate-limit key in v0.1.0.
- Supervisors and interactive callers respect the rate-limit sentinel: on receiving it they back off rather than retrying immediately. Misbehavior of *upstream* callers (rapid retry that bypasses the rate limit by changing the key) is not in scope for this chunk.
