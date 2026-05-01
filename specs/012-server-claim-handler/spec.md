# Feature Specification: Server `/claim` Handler

**Feature Branch**: `012-server-claim-handler`
**Created**: 2026-04-30
**Status**: Draft
**Input**: User description: "POST /claim handler: shape validation → canonical-JSON signature verify → nonce/timestamp freshness → IP allowlist → Discord approval (Approve→200 with JWT, Deny→403, Timeout→408, DiscordUnavailable→503 with NO auto-approve fallback); audit every outcome; error bodies contain only request_id + static error code"

## Overview

The `/claim` handler is the entry point for every secret request that reaches the vault server. It accepts a signed claim from a registered client, verifies the request through a fixed pipeline of checks, asks the configured human approver to gate the request, and — only on a positive operator decision — mints a short-lived session token bound to the requested scope, the requesting client's mesh IP, the requested time-to-live, and a per-session use cap. It is the constitutional choke point that enforces "approval is human, approval is phone" (Constitution Principle II): there is no code path through this handler that issues a session token without a successfully delivered, explicitly approved operator decision.

This chunk encodes the *what* of the handler — the order of checks, the four named outcomes the operator-approval step can produce, the HTTP status code each outcome maps to, the audit obligation on every outcome, and the redaction obligation on every error response. It does not encode *how* the handler is wired internally; the pipeline composition and the underlying primitives (signature verification, nonce cache, timestamp window, token mint) live in upstream chunks and the plan phase decides their integration.

## Clarifications

### Session 2026-04-30

- Q: How should the handler treat a claim whose `request_id` field is absent, empty, or malformed? → A: Reject with HTTP `400` `bad_request`; the response body's `request_id` is server-generated for correlation. This is consistent with `docs/API.md` listing `request_id` as a required request field; the failure mode stays binary and testable, and the server-generated value preserves the response-body redaction rule (static error code + request_id, nothing else).
- Q: How should the handler map the approver's `rate_limited` sentinel (returned per SDD-11 when a caller's per-(supervisor, machine) prompt-delivery window is exceeded) to an HTTP response? → A: HTTP `429 Too Many Requests` with a static `rate_limited` error code in the response body; `rate_limited` is added to the FR-018 error-code enumeration and to the FR-022 outcome-label set. The 503/`discord_unavailable` bucket remains reserved for transport unavailability so SC-004's "no auto-approve under transport-unavailable" assertion stays unambiguous.
- Q: Where does the approval-wait deadline that drives the `approval_timeout` outcome come from? → A: A per-server-config setting (`claim_approval_timeout`, default 60 s) applied uniformly to every claim, regardless of the requested session TTL or the client's HTTP read timeout. The handler derives a child context from the request context with this deadline before invoking the approver. Operator UX stays consistent across all claims; ops tune the value without a code change; a misbehaving client cannot hold an approver-dispatch slot open with a long deadline.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Operator approves a valid claim, server issues a session token (Priority: P1)

A registered client sends a well-formed, properly signed claim to the vault server's claim endpoint. The handler walks the verification pipeline — request shape, signature, nonce uniqueness, timestamp freshness, IP allowlist — and every step passes. The handler asks the configured approver to gate the request; the operator presses Approve from their phone within the request's deadline. The handler caps the time-to-live to the configured maximum for the request's session type (interactive or supervisor), mints a session token bound to the requested scope, the client's mesh IP, the capped time-to-live, and the per-session-type use cap, and returns it to the client. An audit event records the approved outcome.

**Why this priority**: This is the canonical happy path for the entire product. Without it, no client ever gets a session token, no daemon ever boots, no shell ever wraps. It is the realization of acceptance criterion AC-1 ("`hush serve` starts and serves") and the front half of AC-3 ("Discord approval flow") and AC-4 ("JWT lifecycle").

**Independent Test**: With an in-process fake approver that returns an approve decision and an in-process token issuer, send a fully signed valid claim to the handler and assert that the response carries the issued token, a token expiry consistent with the capped time-to-live, and a token identifier; assert that an audit event for the approved outcome was emitted; assert that the response body does not include the request's signature, nonce, or any other client-supplied field beyond what the client must receive to use the token.

**Acceptance Scenarios**:

1. **Given** a valid signed claim from an allowlisted client IP with a fresh nonce and timestamp, **When** the operator approves within the deadline, **Then** the response is HTTP `200` with a body containing the issued session token, its expiry, and its identifier, and an audit event records the approved decision.
2. **Given** the requested time-to-live exceeds the configured maximum for the request's session type, **When** the request is approved, **Then** the issued token's expiry corresponds to the configured maximum, not the requested value.
3. **Given** the request's session type is supervisor (daemon), **When** the request is approved, **Then** the issued token's claims identify the session as a supervisor session and the per-session-type use-cap rules apply (supervisor sessions are TTL-only; interactive sessions carry a use cap).

---

### User Story 2 — Discord unavailable → `503` with no auto-approve fallback (Priority: P1)

A claim arrives at the handler. Verification succeeds. The handler asks the approver to gate the request, but the approver reports that the chat transport is unavailable (the bot's connection is down or has not yet recovered). The handler immediately returns HTTP `503` with a static error code; it does not block, it does not queue, it does not fall back to any auto-approve path under any circumstance. An audit event records the unavailable outcome.

**Why this priority**: Constitution Principle II is explicit and non-negotiable: "Discord bot unavailability MUST return HTTP 503 to the client; the server MUST NOT fall back to auto-approve under any circumstances." This is the load-bearing safety property of the vault server. If this story fails, the system silently bypasses the human-approval gate, which is the single mistake that defeats hush as a product. (`docs/LIFECYCLE-SCENARIOS.md` Scenario 10.)

**Independent Test**: With an in-process fake approver that returns the transport-unavailable sentinel error, send a valid signed claim to the handler and assert that the response is HTTP `503`, that no token was issued, that the response body contains only a static error code and the request identifier, and that an audit event records the unavailable outcome. Additionally, search the handler's code paths and configuration surface and assert there is no configuration value, environment variable, command-line flag, or build tag that causes a token to be issued in this scenario.

**Acceptance Scenarios**:

1. **Given** the approver reports the chat transport unavailable, **When** a fully verified claim reaches the approval step, **Then** the handler returns HTTP `503` with a body containing only a static error code and the request identifier, no token is issued, and an audit event records the unavailable outcome.
2. **Given** any configuration file, environment variable, command-line flag, or build tag is altered, **When** the handler is exercised in the same scenario, **Then** there is no setting that causes the handler to issue a token while the approver reports the transport unavailable.
3. **Given** the chat transport later recovers, **When** subsequent claims arrive, **Then** the handler resumes normal operation (no degraded mode persists past recovery).

---

### User Story 3 — Bad signature, nonce replay, stale timestamp, or disallowed IP fails closed (Priority: P1)

A claim arrives at the handler that fails one of the pre-approval checks: the signature does not verify against the registered client key for the supplied fingerprint, or the nonce has already been seen within the replay window, or the timestamp is outside the freshness window, or the requesting client IP is not on the configured allowlist. The handler does not invoke the approver. The handler returns the documented HTTP status for the failure class with a body that contains only a static error code and the request identifier. The response body never echoes the rejected signature, the nonce, the supplied reason, or any other client-supplied field. An audit event records the failure outcome.

**Why this priority**: These are the cryptographic-integrity and replay-protection obligations of the vault server. They are first-line defenses against a compromised client, a captured request, or a misrouted host. They must reject without ever consulting the operator (so that an attacker cannot weaponize the operator into approving a forged claim) and must redact rigorously (so that a verbose error response does not become an oracle that tells an attacker which check failed in a way that exposes a request artifact).

**Independent Test**: With an in-process pipeline whose individual checks can be forced to fail one at a time, submit four claims — one with a tampered signature, one with a replayed nonce, one with a stale timestamp, one from a disallowed IP — and for each, assert the response is HTTP `403`, the response body contains only a static error code identifying the failure class and the request identifier, and the response body does not contain the request's signature, nonce, or reason. Assert that no approver call was made for any of these claims. Assert that an audit event was emitted for each.

**Acceptance Scenarios**:

1. **Given** a claim whose signature does not verify, **When** the handler processes it, **Then** the response is HTTP `403` with a body containing only a static error code and the request identifier, the approver is not invoked, and an audit event records the failure.
2. **Given** a claim whose nonce has already been seen within the replay window, **When** the handler processes it, **Then** the response is HTTP `403` with a body containing only a static error code and the request identifier, the approver is not invoked, and an audit event records the failure.
3. **Given** a claim whose timestamp is outside the freshness window (past or future), **When** the handler processes it, **Then** the response is HTTP `403` with a body containing only a static error code and the request identifier, the approver is not invoked, and an audit event records the failure.
4. **Given** a claim whose requesting client IP is not on the configured allowlist, **When** the handler processes it, **Then** the response is HTTP `403` with a body containing only a static error code and the request identifier, the approver is not invoked, and an audit event records the failure.
5. **Given** any of the above failures, **When** the response body is inspected, **Then** the body does not contain the request's signature, nonce, supplied reason, ephemeral public key, machine name, or any other client-supplied field beyond the request identifier.

---

### User Story 4 — Operator denies → `403`, no token issued (Priority: P1)

A claim passes every pre-approval check. The handler asks the approver to gate the request. The operator presses Deny within the deadline. The handler returns HTTP `403` with a body containing only a static `denied` error code and the request identifier. No token is minted. An audit event records the denied outcome. The client does not retry automatically.

**Why this priority**: A denial is a deliberate operator action and is part of every realistic threat-model exercise: the operator sees a prompt they did not initiate and refuses it. The handler must surface that refusal cleanly with a status code distinct from a verification failure (so the client can tell "the operator said no" apart from "your request was malformed") and must not expose any request artifact in the response.

**Independent Test**: With an in-process fake approver that returns a denied decision, send a fully verified valid claim and assert the response is HTTP `403`, the body contains only the static `denied` error code and the request identifier, no token was issued, and an audit event records the denied outcome.

**Acceptance Scenarios**:

1. **Given** the approver returns a denied decision, **When** the handler returns its response, **Then** the response is HTTP `403` with a body containing only a static `denied` error code and the request identifier, no token is issued, and an audit event records the denied outcome.

---

### User Story 5 — Operator does not respond → `408`, no token issued (Priority: P2)

A claim passes every pre-approval check. The handler asks the approver to gate the request, passing a context whose deadline is the configured `claim_approval_timeout` (default 60 s). The operator does not press Approve or Deny before that deadline expires. The handler returns HTTP `408` with a body containing only a static `approval_timeout` error code and the request identifier. No token is minted. An audit event records the timeout outcome.

**Why this priority**: A timeout is a distinct outcome from a denial: the operator's intent was not expressed. The client should treat it differently (a denial is final; a timeout might warrant a polite retry initiated by a human action, never an automatic loop). The HTTP status must distinguish it. The body must redact like any other error.

**Independent Test**: With an in-process fake approver that returns the timed-out sentinel, send a fully verified valid claim and assert the response is HTTP `408`, the body contains only the static `approval_timeout` error code and the request identifier, no token was issued, and an audit event records the timeout outcome.

**Acceptance Scenarios**:

1. **Given** the approver returns a timed-out decision, **When** the handler returns its response, **Then** the response is HTTP `408` with a body containing only a static `approval_timeout` error code and the request identifier, no token is issued, and an audit event records the timeout outcome.

---

### User Story 6 — Every outcome is recorded in the audit log (Priority: P2)

Every claim — approved, denied, timed out, transport-unavailable, or rejected at any pre-approval check — produces exactly one audit event that captures the request identifier, the requesting client IP, the requested scope, the session type, and the outcome. The audit event MUST NOT include the request's signature, nonce, ephemeral public key, supplied reason, the issued token (when one is issued), or any portion of any of those values.

**Why this priority**: The audit chain is the system's truth about who fetched what and when. A handler that fails to emit an event on a denied or timed-out claim leaves a gap that an attacker can exploit to obscure failed attempts; a handler that emits a verbose event that includes the signature or token leaks material that defeats the whole point of redaction. Both failures undermine the constitutional Principle X obligation that "operational logs are for debugging and MUST NOT duplicate audit entries" while the audit chain remains the source of truth.

**Independent Test**: Drive the handler through each documented outcome (approved, denied, timed-out, transport-unavailable, rate-limited, bad-request, bad-signature, nonce-replay, stale-timestamp, ip-not-allowed, unknown-outcome) and capture the audit-write callback invocations. Assert exactly one event per outcome. Assert each event carries the outcome label, request identifier, client IP, scope, and session type. Assert no event carries the signature, nonce, ephemeral public key, supplied reason, or token bytes.

**Acceptance Scenarios**:

1. **Given** any successful or unsuccessful outcome (approve, deny, timeout, transport-unavailable, signature failure, nonce replay, stale timestamp, IP not allowed), **When** the handler completes, **Then** exactly one audit event is emitted that names the outcome and includes the request identifier, client IP, scope, and session type.
2. **Given** any audit event emitted by the handler, **When** the event is inspected, **Then** the event does not contain the request's signature, nonce, ephemeral public key, supplied reason, or any byte of an issued token.

---

### Edge Cases

- **Request body is malformed JSON or missing required fields.** The handler returns HTTP `400` with a body containing only a static `bad_request` error code and the request identifier (a server-generated identifier, since the client-supplied request identifier may itself be missing or malformed in this case). No approver call is made. An audit event records the failure.
- **Requested time-to-live exceeds the configured maximum for the session type.** The handler does not reject; it caps the requested time-to-live to the configured maximum *before* invoking the approver, so the operator sees the actual time-to-live they are approving. The issued token's expiry corresponds to the capped value.
- **Requested time-to-live is zero, negative, or absent.** The handler treats absent/zero/negative time-to-live as an invalid request and returns HTTP `400` with a body containing only a static error code and the request identifier. The approver is not invoked.
- **Approver returns an outcome the handler does not recognize.** The handler treats any unrecognized outcome as fail-closed: it returns HTTP `503` (the safest non-leaking status that signals the request did not produce a token), emits an audit event labeling the outcome as unknown, and never issues a token. There is no code path where an unrecognized approver outcome leads to a `200`. The rate-limited sentinel is **not** an unrecognized outcome; per FR-007a it has its own HTTP `429` mapping.
- **Approver returns the rate-limited sentinel.** The handler returns HTTP `429` with the static `rate_limited` error code and emits an audit event labeled `rate-limited`. This status is reserved for this outcome only; it is not used for transport unavailability (503) or denial (403). A supervisor receiving 429 is expected to back off per its own retry policy; the handler does not advise on retry timing.
- **Multiple checks would fail.** The handler returns the status and error code corresponding to the *first* failing check in the documented order (shape → signature → nonce → timestamp → IP allowlist). The handler does not aggregate failures or re-check after the first rejection.
- **Concurrent claims with the same nonce.** Only one of them passes the nonce-uniqueness check; the others receive HTTP `403` with the static `nonce_replay` error code. Both audit events are emitted (one for the accepted claim, one or more for the rejected duplicates).
- **Request includes a known, registered client-key fingerprint but the signature does not verify against that key.** The handler treats this as a signature failure, not as an unknown-client failure: HTTP `403`, static `bad_signature` error code, audit event recorded. The handler does not enumerate which clients are registered through error-message variation.
- **Client supplies an unknown registered-client-key fingerprint.** The handler treats this as a signature failure (same status code, same static error code) — it does not return a distinct "client unknown" status, because doing so would let an attacker enumerate which fingerprints are registered.
- **`request_id` field is missing, empty, or malformed in the request body.** Per FR-009, this is treated as a shape-validation failure: HTTP `400`, static `bad_request` error code, with a server-generated identifier in the response body's `request_id` field so the client can correlate with the audit-log entry for this rejection.

## Requirements *(mandatory)*

### Functional Requirements

#### Verification pipeline

- **FR-001**: The handler MUST run the following checks, in this order, before invoking the approver: (1) request shape validation, (2) canonical-form signature verification against the registered client key identified by the supplied fingerprint, (3) nonce uniqueness within the configured replay window, (4) timestamp freshness within the configured ±window, (5) requesting client IP membership in the configured allowlist.
- **FR-002**: The handler MUST short-circuit on the first failing check; subsequent checks MUST NOT be evaluated for that request.
- **FR-003**: The handler MUST NOT invoke the approver for any request that fails any check in FR-001.

#### Outcome routing

- **FR-004**: When all FR-001 checks pass and the approver returns an approved decision, the handler MUST cap the requested time-to-live to the configured maximum for the request's session type (interactive or supervisor) before issuing the token, mint a session token whose claims include the capped time-to-live, the requesting client's mesh IP, the requested scope, the session type, and a server-generated unique token identifier, and return HTTP `200` with a body containing the issued token, the token's expiry, and the token identifier.
- **FR-005**: When all FR-001 checks pass and the approver returns a denied decision, the handler MUST return HTTP `403` with a body containing only a static `denied` error code and the request identifier, and MUST NOT issue a token.
- **FR-006**: When all FR-001 checks pass and the approver returns a timed-out decision, the handler MUST return HTTP `408` with a body containing only a static `approval_timeout` error code and the request identifier, and MUST NOT issue a token. The deadline is derived from the configured `claim_approval_timeout` value (default 60 s), applied uniformly to every claim by deriving a child context from the request context before invoking the approver. The deadline MUST NOT be derived from the requested session TTL, the client's HTTP read timeout, or any client-supplied field.
- **FR-007**: When all FR-001 checks pass and the approver returns the transport-unavailable sentinel, the handler MUST return HTTP `503` with a body containing only a static `discord_unavailable` error code and the request identifier, and MUST NOT issue a token.
- **FR-007a**: When all FR-001 checks pass and the approver returns the rate-limited sentinel (per SDD-11 FR-020 — the request would exceed the configured per-key prompt-delivery window), the handler MUST return HTTP `429 Too Many Requests` with a body containing only a static `rate_limited` error code and the request identifier, and MUST NOT issue a token. This status is reserved for the rate-limited approver outcome and MUST NOT be conflated with `discord_unavailable` (503) or `denied` (403).
- **FR-008**: When the approver returns any outcome the handler does not explicitly recognize as approved, denied, timed-out, transport-unavailable, or rate-limited, the handler MUST treat it as fail-closed: HTTP `503`, no token issued, audit event labeled as an unknown outcome.

#### Pre-approval failure routing

- **FR-009**: When the request shape is invalid (malformed body, missing required fields, malformed required-field values), the handler MUST return HTTP `400` with a body containing only a static `bad_request` error code and a request identifier. An absent, empty, or malformed `request_id` field is itself a shape-validation failure (`request_id` is a required request field per `docs/API.md`); in that specific case the response body's `request_id` is server-generated so the client can correlate with the audit log.
- **FR-010**: When the signature does not verify, the handler MUST return HTTP `403` with a body containing only a static `bad_signature` error code and the request identifier.
- **FR-011**: When the nonce has already been seen within the replay window, the handler MUST return HTTP `403` with a body containing only a static `nonce_replay` error code and the request identifier.
- **FR-012**: When the timestamp is outside the freshness window, the handler MUST return HTTP `403` with a body containing only a static `stale_timestamp` error code and the request identifier.
- **FR-013**: When the requesting client IP is not on the configured allowlist, the handler MUST return HTTP `403` with a body containing only a static `ip_not_allowed` error code and the request identifier.

#### Constitutional fail-closed property

- **FR-014**: There MUST NOT exist any configuration knob, environment variable, command-line flag, build tag, or runtime mode that causes the handler to return HTTP `200` and an issued token when the approver has returned the transport-unavailable sentinel. Auto-approve mode does not exist (Constitution Principle II).
- **FR-015**: There MUST NOT exist any code path through the handler that issues a token without first having received a successful approve decision from the approver. In particular, a transport failure, a timeout, a denial, or any pre-approval failure MUST NOT result in token issuance.

#### Time-to-live discipline

- **FR-016**: The handler MUST cap the requested time-to-live to the configured maximum for the request's session type *before* invoking the approver, so that the operator's prompt displays the actual time-to-live that will be issued.
- **FR-017**: The handler MUST refuse a request whose requested time-to-live is absent, zero, or negative with HTTP `400` and a static `bad_request` error code.

#### Response-body redaction

- **FR-018**: Every error response body MUST contain exactly two pieces of data: a static error code drawn from a fixed enumeration ({`bad_request`, `bad_signature`, `nonce_replay`, `stale_timestamp`, `ip_not_allowed`, `denied`, `approval_timeout`, `discord_unavailable`, `rate_limited`, `unknown_outcome`}) and the request identifier.
- **FR-019**: Error response bodies MUST NOT contain the request's signature, nonce, timestamp, ephemeral public key, supplied reason, machine name, requested scope, or any other client-supplied field beyond the request identifier.
- **FR-020**: Successful (`200`) response bodies MUST contain the issued session token, the token's expiry, and the token's identifier — and no other client-supplied or server-internal fields.

#### Audit obligation

- **FR-021**: The handler MUST emit exactly one audit event per request, regardless of outcome (approved, denied, timed-out, transport-unavailable, unknown outcome, or any pre-approval failure).
- **FR-022**: Each audit event MUST include: the request identifier, the requesting client IP, the requested scope (the list of secret names), the session type, and the outcome label.
- **FR-023**: No audit event emitted by the handler MAY include: the request's signature, nonce, ephemeral public key, supplied reason field, the issued session token (when one is issued), or any byte of any of those values.

#### Operational logging

- **FR-024**: The handler's operational log records MUST never include the request's signature, the issued session token, the bot-token value, or any secret value. Operational logs are for debugging and MUST NOT duplicate audit content (Constitution Principle X).

#### Concurrency and lifecycle

- **FR-025**: The handler MUST be safe for concurrent invocation; concurrent requests with the same nonce MUST result in exactly one passing the nonce check and the rest receiving the `nonce_replay` outcome.
- **FR-026**: The handler MUST honour the request context: when the request context is cancelled (client disconnects, server shutdown), any in-flight approver call MUST be cancelled and the handler MUST NOT issue a token even if the approver subsequently returns approve.

### Key Entities

- **Claim request**: The signed request body from a registered client. Carries the requested scope, reason, time-to-live, session type, ephemeral public key, nonce, timestamp, signature, request identifier, machine name, and client-key fingerprint (per `docs/API.md`).
- **Outcome**: One of ten labels — approved, denied, approval-timeout, transport-unavailable, rate-limited, bad-request, bad-signature, nonce-replay, stale-timestamp, ip-not-allowed, unknown-outcome — drawn from the fixed enumeration in FR-018.
- **Session token response**: The successful response body — issued token, expiry, and token identifier.
- **Error response**: The unsuccessful response body — static error code and request identifier, nothing else.
- **Audit event**: The handler's per-request record of outcome — request identifier, client IP, scope, session type, outcome label.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of claim outcomes that are not "approved" produce zero issued tokens — measured by exhaustively driving the handler through every non-approved outcome (deny, timeout, transport-unavailable, rate-limited, unknown outcome, all five pre-approval failures) and asserting in each test that no token was issued.
- **SC-002**: 100% of error responses produced by the handler contain only the static error code and the request identifier — measured by capturing every error-response body produced in the test suite, parsing it, and asserting the field set is exactly two and matches the documented enumeration.
- **SC-003**: 100% of claim outcomes (approved or otherwise) emit exactly one audit event with the documented field set, and that field set never contains the signature, nonce, ephemeral public key, supplied reason, or token bytes — measured by a test that drives every outcome, captures every audit-write invocation, and asserts the count and field set per outcome.
- **SC-004**: There is zero configuration surface (knob, flag, env var, build tag, runtime mode) that causes the handler to return HTTP `200` while the approver reports the transport unavailable — measured by an exhaustive search of the handler's code paths and configuration loaders, and by a regression test that asserts the transport-unavailable outcome maps to HTTP `503` under every reachable configuration.
- **SC-005**: When a claim's requested time-to-live exceeds the configured maximum for its session type, the operator's prompt and the issued token both reflect the capped value, not the requested value — measured by a test that submits a too-long time-to-live, captures the value passed to the approver, and asserts equality with the configured maximum (and not the requested value).
- **SC-006**: The handler's own contribution to end-to-end latency (excluding signature verification, nonce-cache lookup, approver wait time, and token-mint cost — i.e., the work this chunk owns directly) is under 50 ms in 99% of runs — measured by a test that issues an approved claim with synthetic primitives whose I/O cost is zero and asserts the wall-clock time the handler adds.
- **SC-007**: Test coverage of the handler is at least 95% — measured by the project's test-coverage tooling against the handler package's coverage policy (Constitution Principle VIII).
- **SC-008**: A claim whose body contains a known-unique sentinel string (e.g., a synthetic reason value) and whose verification fails for any reason produces a response body and operational log records that do not contain the sentinel — measured by a sentinel-leak test that injects the sentinel into the claim, forces a verification failure, and greps the captured response and log output for the sentinel.

## Assumptions

- The handler's pre-approval primitives (canonical-form signature verification, the nonce-replay cache, the timestamp-freshness window, the IP allowlist) are owned by upstream chunks (request signing in SDD-08, server skeleton + middleware in SDD-10, configuration in SDD-06) and are available to this chunk through the server's already-established wiring. This chunk depends on those primitives but does not implement them.
- The approver contract (interface, sentinel error set including the transport-unavailable sentinel, decision shape) is owned by SDD-11 and is available as a value the server can invoke. This chunk depends on, but does not implement, the chat-platform integration.
- The session-token issuer (issue, validate, store, revoke) is owned by SDD-07 and exposes a function that mints a token from validated parameters (scope, IP, time-to-live, session type, ephemeral public key). This chunk depends on, but does not implement, the token primitives.
- The audit writer is owned by SDD-13 (audit chain) and is wired into the server. If the audit writer is not yet available at integration time, this chunk emits structured operational log records of the same outcome data and adopts the audit writer when it lands; the redaction rules and per-request "exactly one record" obligation apply unchanged in either path.
- The configured maximum time-to-live values (one for interactive sessions, one for supervisor sessions) are loaded from the server configuration and are available at request time without additional I/O.
- The `claim_approval_timeout` value (default 60 s) is loaded from the server configuration and is available at request time without additional I/O. SDD-06 owns the field's surface in the server config TOML schema; if it is not yet present there, the plan phase coordinates the addition before this chunk's implementation lands.
- The handler is mounted under the random opaque API prefix established by the server skeleton (`/h/<prefix>/claim`); the prefix is not part of this chunk's contract.
- The HTTP layer (router, middleware, request-id assignment, structured logging, panic recovery) is owned by SDD-10. This chunk's handler is registered through the server's already-defined registration entry point and inherits middleware behaviour.
- Tests for this chunk run against in-process fakes for the approver and the token issuer; they never open a real chat-platform connection or write a real session token to a real client.
- "Request identifier" in the response body refers to the server-side request identifier carried through the middleware chain. When the client supplies a `request_id` field in its claim body and that field is well-formed, it is used; otherwise the server generates one. This single value is what every error response body and audit event carry.
- Concurrency safety is provided by the underlying primitives (the nonce cache is concurrent-safe, the token store is concurrent-safe, the approver's per-key rate limiter is concurrent-safe); the handler itself holds no mutable state.

## Out of scope

The following are explicitly out of scope for this chunk and are owned elsewhere:

- The chat-platform integration that turns a `RequestApproval` call into a DM and operator buttons (SDD-11).
- The cryptographic primitives (signature verification, ECIES, BIP32 derivation) (SDD-01, SDD-08, SDD-09).
- The session-token mint/validate/store machinery (SDD-07).
- The other server endpoints (`/secrets/<name>`, `/revoke/<jti>`, `/hz`) (SDD-13).
- The CLI surface that calls this endpoint (`hush request`, `hush supervise`) (SDD-16, SDD-23).
- The integration harness that exercises end-to-end lifecycle scenarios (SDD-25).
