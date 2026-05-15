# Feature Specification: internal/token — ES256K JWT issuance, validation, store, and revocation

**Feature Branch**: `007-token-jwt`
**Created**: 2026-04-28
**Status**: Draft
**Input**: User description: "internal/token: issue + validate + store + revoke ES256K-signed JWTs gating every secret retrieval; two session types (INTERACTIVE TTL+max-uses, SUPERVISOR TTL-only); explicit alg-confusion defence; concurrent-safe use-count decrement"

## Overview

The `internal/token` package is the session-authority of the vault
server. After the human approver has approved a request over Discord,
the server mints a session token from this package, embeds the
client's per-request ephemeral public key, and hands the token back
to the client. Every subsequent secret retrieval the client attempts
goes through this package's validation path before the secret-fetch
handler is allowed to read a secret value out of the vault.

A session token represents one of exactly two distinct session
shapes. An **interactive** session is short-lived and use-count
bounded — it is for a human shell or a one-off automation and dies
either on TTL expiry or after its budgeted number of secret fetches
is exhausted, whichever comes first. A **supervisor** session is
TTL-only — it is for a long-lived daemon that can fetch secrets
freely within the session's lifetime so a child crash or restart
within the TTL does not page the operator at 03:00. The two session
shapes share an issuance code path and a validation code path, but
their lifecycle rules are distinct and the difference is recorded
on the token itself.

This package is consumed by the server's claim handler (SDD-12, mints
a token after Discord approval), the server's secret-fetch and
revoke handlers (SDD-13, validates and decrements / revokes a
token), and the supervisor CLI (SDD-23, holds and refreshes a
supervisor session across child restarts). It is the realisation of
**Layer 2** of the project's seven security layers
(`docs/SECURITY.md`) — asymmetric session signing — and the package
that delivers acceptance criterion **AC-4**.

The package never performs network I/O. It receives the signing key
and verification key as parameters, holds session state in an
in-process store, and reports outcomes through distinct, named
sentinel errors. Every rejection mode the validator can produce is
identifiable by sentinel identity — operators classifying token
failures (audit log, alert tiering, fuzz output triage) MUST NOT
need to parse error message strings to tell rejection categories
apart.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — A human approval mints a session token that gates every subsequent secret fetch (Priority: P1)

The approver has clicked "Approve" on the Discord DM for an
incoming claim. The server invokes the issue operation with the
parameters that defined the claim — the requesting client's
Tailscale IP, the approved scope of secret names, the request
identifier that ties this session to that specific Discord
approval, the client's per-request ephemeral public key, the
agreed TTL, the agreed max-uses (interactive only), and the
session shape the claim asked for. The issue operation returns
an opaque session token plus a record the package can later
recognise. Every subsequent secret retrieval the client attempts
is validated against that record before any secret value leaves
the vault.

**Why this priority**: This is the smallest end-to-end slice
that proves the package exists. Until issue and validate share a
working contract, no secret can be retrieved at all and AC-4
cannot be demonstrated.

**Independent Test**: Issue a token from a known signing key with
a known set of issuance parameters. Validate the resulting token
against the matching verification key, the matching client IP,
and a secret name within the approved scope. The validator
returns a successful result whose claims match the issuance
parameters.

**Acceptance Scenarios**:

1. **Given** a valid issuance with session shape INTERACTIVE, a
   non-empty scope, a client IP, an unexpired TTL, and a
   non-zero max-uses, **When** the resulting token is validated
   against the matching verification key, the matching client
   IP, and a secret name in the approved scope, **Then** the
   validator returns success and the recovered claims expose
   the issuance parameters in their original form.
2. **Given** a successful validation, **When** the consumer
   inspects the recovered claims, **Then** the session shape on
   the recovered claims matches the session shape that was
   issued and is one of exactly two named values.
3. **Given** any successful issuance, **When** the consumer
   inspects the issued token's identifier, **Then** the
   identifier is unique within the lifetime of the store and
   was generated from a cryptographically secure random source.

---

### User Story 2 — A token from a different IP is rejected (Priority: P1)

A token has been issued for a specific requesting client whose
Tailscale IP was recorded in the token's claims. A request now
arrives carrying that token but originating from a different
Tailscale IP — either an honest mistake (the client moved
networks mid-session) or a hostile replay from a different host
that has somehow obtained the token. The validator MUST reject
the request with a distinct, named IP-mismatch sentinel error.
The reject MUST be cheap to identify by sentinel, so an
auditor or alert-tiering policy can treat IP-mismatch as a
distinct operational signal from "expired token" or "exhausted
token".

**Why this priority**: IP-binding is one of the two factors
holding up the wire-level guarantee in `docs/SECURITY.md` Layer 2
("two factors: what the client has + where the client is"). If
this rejection path is missing or collapses into a generic
"invalid token" string, the IP factor disappears.

**Independent Test**: Issue a token with a recorded client IP.
Validate the token against the matching verification key but
present a different IP. The validator returns the IP-mismatch
sentinel and no other claim-validation work proceeds beyond
that point.

**Acceptance Scenarios**:

1. **Given** a token issued for one client IP, **When** the
   token is validated against a different client IP, **Then**
   the validator returns the named IP-mismatch sentinel error
   and the consumer can identify the rejection category by
   sentinel identity alone.
2. **Given** a token issued for one client IP, **When** the
   token is validated against the same IP expressed in a
   different but semantically equivalent textual form, **Then**
   the validator treats the IPs as equal and the validation
   proceeds.

---

### User Story 3 — An interactive token exhausts after its budgeted uses; a supervisor token does not (Priority: P1)

An interactive session was issued with a budget of N successful
secret fetches. Each successful validation against that token
decrements the remaining budget. When the budget reaches zero
the next validation MUST return a distinct, named
use-exhausted sentinel error and MUST NOT permit a secret
fetch. A supervisor session was issued with no use budget — its
record carries the supervisor session shape and validation MUST
NOT decrement any budget and MUST NOT reject the session for
"use exhaustion". A supervisor session ends only by TTL expiry
or explicit revocation.

**Why this priority**: The two session shapes exist because
their failure modes serve different operators. Conflating them
either trains the operator to auto-approve (interactive without
a use cap) or pages the operator at 03:00 (supervisor with a
use cap) — the system is unusable in either degenerate form.
The distinction must be load-bearing in this package.

**Independent Test**: Issue an interactive token with a max-uses
of 3 and a supervisor token with no max-uses. Validate the
interactive token four times against an in-scope secret name
and assert the fourth attempt returns the use-exhausted
sentinel. Validate the supervisor token a thousand times
against an in-scope secret name and assert every attempt
succeeds while the TTL is still in the future.

**Acceptance Scenarios**:

1. **Given** an interactive token with a max-uses of N, **When**
   the token is validated N+1 times against an in-scope secret,
   **Then** the first N validations succeed and the (N+1)-th
   returns the named use-exhausted sentinel error.
2. **Given** a supervisor token, **When** the token is validated
   any number of times against an in-scope secret while its
   TTL is in the future, **Then** every validation succeeds and
   the validator does not reject the session for use
   exhaustion.
3. **Given** an interactive token whose budget has reached zero,
   **When** the token is validated again, **Then** the rejection
   is identifiable as use-exhaustion specifically, distinct from
   expiry, IP-mismatch, scope-violation, and revocation.

---

### User Story 4 — An algorithm-confusion attack is rejected (Priority: P1)

An attacker has captured an issued token and wishes to mutate
its header so the validator accepts a different signing
algorithm — specifically the JWT specification's "none" pseudo-
algorithm (which removes the signature requirement entirely),
or a symmetric algorithm such as HS256 (which lets the attacker
sign a forged token with the verification key as if it were a
secret). The validator MUST reject every such attempt with a
distinct, named algorithm-unsupported sentinel error. The
rejection MUST happen on header inspection, before any
signature verification work begins. Both the "none" attack and
the "HS256" attack MUST be exercised by separate test cases —
each must independently produce the named rejection — so the
defence is proven against both well-known classes of
alg-confusion.

**Why this priority**: This is the single most well-documented
JWT vulnerability class and the constitutional security bar for
Layer 2 (`docs/SECURITY.md`) requires it be defended against
explicitly. A silent or generic rejection is not enough — both
"none" and a non-ES256K named algorithm must be observably,
testably rejected with the same identifiable sentinel.

**Independent Test**: Hand-craft a token whose header asserts
algorithm "none" and present it to the validator. Hand-craft a
second token whose header asserts algorithm "HS256" and present
it to the validator. Both calls return the named
algorithm-unsupported sentinel error, and the rejection occurs
without invoking the underlying ES256K verification primitive.

**Acceptance Scenarios**:

1. **Given** a token whose header asserts algorithm "none",
   **When** the token is presented to the validator, **Then**
   the validator returns the named algorithm-unsupported
   sentinel error and the rejection occurs before any
   signature-verification primitive is invoked.
2. **Given** a token whose header asserts a symmetric algorithm
   such as "HS256", **When** the token is presented to the
   validator, **Then** the validator returns the named
   algorithm-unsupported sentinel error and the rejection
   occurs before any signature-verification primitive is
   invoked.
3. **Given** any token whose header asserts an algorithm other
   than the project's ES256K signing algorithm, **When** the
   token is presented to the validator, **Then** the rejection
   is identifiable as algorithm-unsupported specifically, not
   collapsed into a generic "invalid token" string.

---

### User Story 5 — A revoked token is rejected for the lifetime of the store (Priority: P1)

A session has been compromised — the operator has noticed a
suspicious request, the agent host has been wiped, or a
supervisor refresh has implicitly invalidated an older session.
The revoke operation marks the token's identifier as revoked.
Every subsequent validation of that token MUST return a
distinct, named revoked sentinel error. The revocation MUST
persist for the lifetime of the store, even after the token's
own TTL has elapsed. The revocation record cannot be cleared by
re-issuing — a token identifier that has been revoked is dead
permanently within the store.

**Why this priority**: Revocation is the operator's emergency
brake. If it is racy, lossy, or expirable, the brake fails when
needed most. The constitutional security requirements
(`docs/SPEC.md` §"Security Requirements" — Token revocation row,
and Constitution Principle III) treat revocation as a hard,
permanent control.

**Independent Test**: Issue an interactive token with TTL 1
hour. Revoke its identifier. Validate the token immediately —
the validator returns the revoked sentinel. Wait until past the
token's natural expiry. Validate the same token again — the
validator still returns either the revoked sentinel or the
expired sentinel, never success. Issue a new token using the
same parameters as the revoked one — assert the new token
receives a different identifier and that the revoked
identifier remains revoked in the store's state.

**Acceptance Scenarios**:

1. **Given** an issued token whose identifier has been revoked,
   **When** the token is validated, **Then** the validator
   returns the named revoked sentinel error and never returns
   success.
2. **Given** a revoked identifier whose original token has also
   passed its TTL, **When** the store's cleanup operation is
   asked to remove expired entries, **Then** the cleanup MUST
   remove only entries that have expired through TTL. Whether
   a separately revoked record may also be reclaimed is a
   plan-phase decision, but cleanup MUST NOT cause the
   identifier to become re-usable — a freshly issued token MUST
   receive a new identifier from the cryptographically secure
   source, and any future presentation of the original
   revoked-or-expired token MUST be rejected.
3. **Given** a token whose identifier has been revoked, **When**
   any code path attempts to re-issue a token whose identifier
   collides with the revoked one, **Then** the re-issue MUST
   NOT succeed silently. Issuance MUST draw a fresh identifier
   from the cryptographically secure source on every call.

---

### User Story 6 — Many concurrent secret fetches against one interactive token decrement exactly once each (Priority: P1)

A single interactive token has a max-uses budget of N. The
agent's secret consumer has launched N goroutines that each
attempt a secret fetch with that token at the same instant.
The store is asked to decrement the use-count N times in
parallel. The package MUST guarantee that exactly N successful
decrements occur — no decrement is missed (the store would
otherwise let the token survive past its budget) and no
decrement is double-counted (the store would otherwise
exhaust the token early). After the burst, the next attempt
MUST observe the use-exhausted sentinel.

**Why this priority**: A racy use-count breaks the use-budget
contract. If the package over-decrements, the system rejects
honest fetches that the operator approved. If the package
under-decrements, an interactive token that should have been
spent can outlive its budget — collapsing the difference
between the two session shapes. The race detector MUST stay
clean across this scenario.

**Independent Test**: Issue an interactive token with max-uses
of N (e.g. 100). Spawn N goroutines that each call validate
once for an in-scope secret. Wait for all goroutines to
complete. Assert exactly N successes and no failures. Then
call validate once more — assert the result is the
use-exhausted sentinel. Run under the race detector with no
warnings.

**Acceptance Scenarios**:

1. **Given** an interactive token with max-uses of N, **When**
   N goroutines simultaneously validate that token against an
   in-scope secret, **Then** exactly N successful validations
   are observed and zero failures occur during the burst.
2. **Given** the burst from (1) has completed, **When** any
   further validation of that token is attempted, **Then** the
   result is the named use-exhausted sentinel error.
3. **Given** any concurrent burst against the store, **When**
   the package's tests are run with the race detector enabled,
   **Then** no race detector warnings are produced.

---

### User Story 7 — Each rejection mode is individually nameable (Priority: P1)

Validation can fail for distinct reasons — the signature is
not valid, the token has expired, the requested secret is not
in the token's approved scope, the requesting IP does not
match the token's bound IP, the token's identifier has been
revoked, the token's interactive use budget has been
exhausted, the token's session shape is not one of the
defined values, or the token's header asserts an unsupported
algorithm. Every one of these MUST be reported as a distinct,
named, comparable sentinel error. Operators classifying
failures (audit log entries, alert-tier policy, post-mortem)
MUST be able to identify the failure category from the error's
identity without parsing message strings.

**Why this priority**: A single "invalid token" error makes
incident response harder than it needs to be ("did a network
glitch present a stale token, or is a hostile client probing
us?"). The constitution's observability principle (X) requires
that operational signals be legible without string parsing.

**Independent Test**: For each defined rejection category,
build an input that triggers exactly that category and assert
the returned error is identifiable as the corresponding named
sentinel. No rejection collapses into a generic "invalid
token" message.

**Acceptance Scenarios**:

1. **Given** a token whose header asserts an unsupported
   signing algorithm, **When** validation runs, **Then** the
   returned error is identifiable as the algorithm-unsupported
   sentinel.
2. **Given** a token whose TTL has elapsed, **When** validation
   runs, **Then** the returned error is identifiable as the
   expired sentinel.
3. **Given** a token whose approved scope does not include the
   requested secret name, **When** validation runs, **Then**
   the returned error is identifiable as the
   scope-violation sentinel.
4. **Given** a token whose recorded client IP differs from the
   requesting IP, **When** validation runs, **Then** the
   returned error is identifiable as the IP-mismatch sentinel.
5. **Given** a token whose identifier has been revoked,
   **When** validation runs, **Then** the returned error is
   identifiable as the revoked sentinel.
6. **Given** an interactive token whose use budget has reached
   zero, **When** validation runs, **Then** the returned error
   is identifiable as the use-exhausted sentinel.
7. **Given** a token whose session-shape claim is not one of
   the two defined values, **When** validation runs, **Then**
   the returned error is identifiable as the
   unknown-session-type sentinel.

---

### User Story 8 — Cleanup reclaims expired records but never resurrects a revoked identifier (Priority: P2)

The store accumulates records over the server's uptime. To
keep memory bounded, a caller-controlled cleanup operation
walks the store at an interval and removes records whose TTL
has elapsed. Cleanup MUST NOT remove a record in a way that
allows a revoked identifier to become re-usable, MUST NOT
remove a record that is still within its TTL and still
usable, and MUST cooperate with concurrent validation calls
without producing race-detector warnings.

**Why this priority**: Without cleanup the store grows
without bound; with broken cleanup a revoked identifier could
be resurrected. Both outcomes are unacceptable but both have
reasonable mitigations (process restart, audit chain) so this
is P2 rather than P1.

**Independent Test**: Populate the store with a mix of
unexpired-not-revoked, unexpired-revoked, expired-not-revoked,
and expired-revoked entries. Drive a cleanup pass. Assert
unexpired entries are still present (revoked or not).
Assert expired-not-revoked entries have been removed. Assert
that any subsequent presentation of an originally-revoked
identifier still results in a rejection (revoked or expired,
never success). Run under the race detector concurrently with
validation calls.

**Acceptance Scenarios**:

1. **Given** the store contains both expired and unexpired
   records, **When** the cleanup pass runs, **Then** records
   whose TTL has elapsed are removed and records still within
   TTL remain.
2. **Given** a record whose identifier has been revoked,
   **When** the cleanup pass runs, **Then** any later
   presentation of that token MUST NOT succeed — it is
   rejected as either revoked or expired, but never as
   "unknown identifier with success".
3. **Given** the cleanup pass is running, **When** other
   goroutines are validating tokens concurrently, **Then** no
   race-detector warnings are produced and no validator
   observes a partially-removed record.

---

### Edge Cases

- A validation call against a token whose header carries the
  literal algorithm "none" returns the named
  algorithm-unsupported sentinel — never a generic "invalid
  token" error and never a partial result.
- A validation call against a token whose header carries the
  symmetric algorithm "HS256" (a textbook alg-confusion attack
  in JWT libraries that accept a verification key as a shared
  secret) returns the named algorithm-unsupported sentinel.
  The defence is observable independently from the "none"
  case.
- A token whose claims are well-formed but whose signature
  does not verify under the supplied verification key returns
  the named algorithm-unsupported sentinel — the system MUST
  never return success for an unverified signature, regardless
  of any other claim's validity.
- A token whose approved scope is empty rejects every secret
  name with the scope-violation sentinel.
- A token whose TTL has just elapsed at the moment of
  validation returns the expired sentinel — the boundary
  condition is "exp ≤ now" (already-expired) is rejected.
- A token whose session-shape claim is missing or is a value
  other than the two defined values returns the
  unknown-session-type sentinel — a missing or unknown shape
  is never silently treated as either of the two defined
  shapes.
- A validation call whose context has already been cancelled
  returns a typed error rather than performing further work
  on the token.
- A re-issue attempt that draws a new identifier MUST always
  produce a fresh identifier value distinct from any previously
  issued or revoked identifier — the package never reuses an
  identifier already known to the store.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST mint session tokens signed with
  ES256K (ECDSA over the secp256k1 curve, the project's locked
  asymmetric signing algorithm per `docs/SECURITY.md` Layer 2).
  No other signing algorithm is acceptable for issuance.
- **FR-002**: The validator MUST explicitly reject any token
  whose header asserts the JWT specification's "none"
  pseudo-algorithm. The rejection MUST be reported via a
  distinct, named algorithm-unsupported sentinel error and
  MUST occur before any signature-verification primitive is
  invoked.
- **FR-003**: The validator MUST explicitly reject any token
  whose header asserts a signing algorithm other than the
  project's ES256K algorithm — including but not limited to
  symmetric algorithms such as "HS256" — via the same
  algorithm-unsupported sentinel as FR-002. The "none" case
  and the non-ES256K case MUST each be exercised by an
  independent test case.
- **FR-004**: The system MUST encode each issued token's
  session shape as a claim whose value is one of exactly two
  distinct, named values: an INTERACTIVE shape and a
  SUPERVISOR shape. The two values are the package's
  authoritative session-type vocabulary; a third value is
  not a defined session shape.
- **FR-005**: An INTERACTIVE token MUST carry both an
  expiration time and a non-zero remaining-use count. A
  successful validation against an INTERACTIVE token MUST
  decrement the remaining-use count by exactly one before
  the secret-fetch handler is permitted to read the secret.
- **FR-006**: A SUPERVISOR token MUST carry an expiration
  time. The validator MUST NOT consult, decrement, or reject
  a SUPERVISOR session for any use-count reason. The
  remaining-use count is not a meaningful claim on a
  SUPERVISOR session and MUST NOT cause a rejection.
- **FR-007**: The validator MUST verify, in addition to the
  signature and the header algorithm, every one of the
  following six checks. Each failure MUST be reported via a
  distinct, named sentinel error so the rejection category
  is identifiable by sentinel identity alone:
  1. The token's expiration time has not elapsed (expired
     sentinel).
  2. The requested secret name is included in the token's
     approved scope (scope-violation sentinel).
  3. The requesting client's IP equals the token's recorded
     client IP (IP-mismatch sentinel).
  4. The token's identifier has not been revoked in the
     store (revoked sentinel).
  5. For an INTERACTIVE token, the token's remaining-use
     count is greater than zero (use-exhausted sentinel).
  6. The token's session-shape claim is one of the two
     defined values (unknown-session-type sentinel).
- **FR-008**: Each issued token MUST carry a unique
  identifier (the JTI). The identifier MUST be drawn from a
  cryptographically secure random source. A non-cryptographic
  random source is not an acceptable substitute. The identifier
  is the key by which the store recognises the token for
  use-count decrement, revocation, and cleanup.
- **FR-009**: Each issued token's claim set MUST include the
  following named claims: an issuer marker identifying the
  token as a hush session token; an issued-at time; an
  expiration time; the unique identifier; the approved
  scope (a list of secret names); the recorded client IP;
  the request identifier tying the session to the upstream
  Discord approval; the remaining-use count (interactive
  only — present-but-meaningless on supervisor); the
  client's per-request ephemeral public key; and the session
  shape (one of the two named values per FR-004). Internal
  layout (struct fields, JSON keys) is plan-phase detail and
  is not fixed by this specification.
- **FR-010**: The store MUST be safe for concurrent use by
  multiple goroutines. When N goroutines simultaneously
  attempt to consume a use against the same INTERACTIVE
  token whose budget is at least N, the store MUST permit
  exactly N successes and the next consumption attempt MUST
  return the use-exhausted sentinel. No race-detector
  warning may be observed under this scenario.
- **FR-011**: The store's revoke operation MUST mark a
  token's identifier as revoked. The revocation MUST persist
  for the lifetime of the store. A revoked identifier MUST
  never be presentable as a valid token again — every
  subsequent validation MUST return either the revoked
  sentinel or, if the token's TTL has additionally elapsed,
  the expired sentinel. The store MUST NOT permit a future
  issuance to reuse a revoked identifier.
- **FR-012**: The store MUST expose a cleanup operation
  that removes records whose TTL has elapsed. Cleanup MUST
  NOT cause an originally-revoked token's identifier to
  become acceptable again — even if the original record is
  reclaimed for memory reasons, any subsequent presentation
  of the originally-revoked token MUST result in a
  rejection (the revocation guarantee in FR-011 holds for
  the lifetime of the store).
- **FR-013**: Every distinct rejection category MUST be
  reported as a distinct, comparable sentinel error value.
  The package MUST export at minimum the following sentinel
  identities: algorithm-unsupported, expired, scope-violation,
  IP-mismatch, revoked, use-exhausted, unknown-session-type.
  Callers MUST be able to identify every rejection category
  by sentinel identity, not by parsing the error message.
- **FR-014**: Error messages MUST contain the failure
  category and nothing else. Error messages MUST NEVER
  contain the encoded token string, the token's signature
  bytes, the signing key, the verification key, or any
  portion of the secret value or the recipient ephemeral
  key. This property MUST be asserted by an automated
  sentinel-leak test.
- **FR-015**: The issue and validate operations MUST accept
  a context as their first parameter and MUST honour
  context cancellation by returning promptly with a typed
  error. A cancelled context MUST NOT cause the package to
  perform further token work.
- **FR-016**: Client-IP equality MUST be evaluated as
  semantic IP equality (two textual representations of the
  same address compare equal). The validator MUST NOT
  perform a naive byte-for-byte string comparison that
  would distinguish equivalent representations of the
  same address.
- **FR-017**: The package MUST NOT log the encoded token
  string, the signature bytes, the signing key, the
  verification key, or the recipient ephemeral key value.
  Diagnostic surfaces are limited to the typed sentinel
  errors defined by FR-013 / FR-014.
- **FR-018**: The package MUST NOT introduce mutable
  package-level state that holds session material between
  calls; the store's per-instance state is the only place
  session records live. Registering the ES256K signing
  method with the underlying JWT library is the package's
  one bounded exception, performed via a one-shot
  registration helper rather than implicit module
  initialization, so test isolation and constitutional
  Principle IX both hold.

### Key Entities

- **Session token**: The opaque, signed string the package
  produces on issuance and accepts on validation. It carries
  the claim set defined by FR-009 and is signed with ES256K.
- **Token record**: The store's per-token bookkeeping —
  identifier, expiration time, session shape, remaining-use
  count, revoked flag. The record is the source of truth
  for FR-005, FR-006, FR-010, FR-011, and FR-012.
- **Session shape**: One of two named values — INTERACTIVE
  or SUPERVISOR — distinguishing a TTL+use-bounded session
  from a TTL-only session. The vocabulary is closed; an
  unknown value is itself a rejection category (FR-007 #6).
- **Approved scope**: The list of secret names the
  approver permitted on this session. Validation rejects
  any secret name outside this list (FR-007 #2).
- **JTI (token identifier)**: The cryptographically random,
  per-token identifier (FR-008). It is the store's lookup
  key and the revocation handle (FR-011).
- **Sentinel error**: A named, comparable error value
  representing one of seven rejection categories
  (FR-013). Callers identify rejection by sentinel
  identity.
- **Token store**: The package-internal, concurrent-safe
  in-process repository of token records. It exposes the
  add, get, consume-use, revoke, and cleanup operations
  the validator depends on (FR-010, FR-011, FR-012).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A round-trip of (issue, validate) succeeds for
  both an INTERACTIVE token and a SUPERVISOR token, asserted
  by automated tests that present a fresh signing key, fresh
  issuance parameters, and matching validation parameters
  each run.
- **SC-002**: A token presented from a different IP than the
  one recorded at issuance fails with the named IP-mismatch
  sentinel and never with a generic error, asserted by an
  automated test that issues with one IP and validates with
  another.
- **SC-003**: An INTERACTIVE token with a max-uses budget
  of N succeeds for exactly N validations and the (N+1)-th
  validation returns the named use-exhausted sentinel —
  asserted by an automated test for sequential validations
  and by a separate automated test for concurrent
  validations under the race detector.
- **SC-004**: A SUPERVISOR token validated repeatedly during
  its TTL never returns the use-exhausted sentinel,
  asserted by an automated test that validates a single
  supervisor token at least 1000 times within TTL.
- **SC-005**: A token whose header carries the algorithm
  "none" is rejected with the named algorithm-unsupported
  sentinel — asserted by an automated test whose name makes
  the alg-confusion intent explicit.
- **SC-006**: A token whose header carries the symmetric
  algorithm "HS256" is rejected with the named
  algorithm-unsupported sentinel — asserted by an
  independent automated test whose name makes the
  alg-confusion intent explicit. The "none" case (SC-005)
  and the "HS256" case (SC-006) are independently
  observable.
- **SC-007**: A revoked token's identifier is rejected on
  every subsequent validation for the lifetime of the
  store, asserted by automated tests that revoke a token
  and re-validate it both before and after its natural TTL
  has elapsed.
- **SC-008**: A 60-second random-input fuzz run against the
  validate operation completes without panic, without
  exhausting memory, and produces only typed errors —
  satisfying constitutional fuzz target #2.
- **SC-009**: A sentinel-leak test that issues a token with
  a known marker request identifier and induces every
  defined rejection category passes — the marker MUST NOT
  appear in any returned error message and the encoded
  token string MUST NOT appear in any returned error
  message.
- **SC-010**: Coverage of the package is at least 100% per
  the constitutional bar for security-critical packages
  (`docs/SPEC.md` AC-9, Constitution Principle VIII).
- **SC-011**: Calling issue or validate with an
  already-cancelled context returns a typed error and does
  not perform further token work, asserted by automated
  tests for both operations.
- **SC-012**: Concurrent decrement of an INTERACTIVE
  token's use-count by N goroutines produces exactly N
  successful decrements and zero race-detector warnings,
  asserted by an automated test.
- **SC-013**: An external review of the package's logs and
  error messages confirms that no encoded token, no
  signature, no key bytes, and no portion of any other
  sensitive material appears in any diagnostic surface.

## Assumptions

- The signing algorithm is ES256K (ECDSA over secp256k1),
  already locked by the project's crypto stack
  (`docs/SECURITY.md` Layer 2, `docs/SPEC.md` FR-4,
  Constitution Principle III). The choice of curve and the
  registration of the algorithm with the underlying JWT
  surface are plan-phase detail and are not re-litigated by
  this specification.
- The signing key is the JWT signing key derived at runtime
  from the operator's passphrase via BIP32
  (`docs/SECURITY.md` Layer 1, path `m/44'/7743'/0'`,
  delivered by SDD-01). The package receives the signing key
  and the verification key as parameters; key generation,
  rotation, and lifetime are out of scope for this package.
- The client's per-request ephemeral public key is generated
  by the client (`hush request`, SDD-16) and supplied to the
  server inside the upstream claim request before issuance
  (`docs/SPEC.md` FR-4, FR-5). The package treats the
  ephemeral public key as an opaque claim value.
- The Discord approval flow that authorises issuance lives
  in SDD-11 and SDD-12; this package mints a token only
  after that flow has produced an approval and is invoked
  by SDD-12.
- The wire-level encryption of the secret-fetch response
  body is the responsibility of `internal/transport/ecies`
  (SDD-09); this package never encrypts or transports
  secret values, only session-state bytes.
- The package's store is in-process and is not persisted
  across restarts. A server restart is, by design, a
  revocation of every active session — operators rely on
  the daily refresh-window prompt and the supervisor TTL
  to recover after restart (Constitution Principle V,
  `docs/SPEC.md` FR-18). Persistent storage of session
  records is out of scope for v0.1.0.
- The clock used for TTL evaluation is the host system
  clock; clock-sync is enforced at server startup by an
  NTP drift check elsewhere (`docs/SPEC.md` FR-17). This
  package consumes the system clock and does not perform
  its own drift detection.
- The package is consumed by the server's claim handler
  (SDD-12, mints a token after approval), the server's
  secret-fetch and revoke handlers (SDD-13, validates and
  decrements / revokes a token), and the supervise CLI
  (SDD-23, holds and refreshes a supervisor session). All
  three consumers receive the same exported surface and
  the same set of sentinel errors.
