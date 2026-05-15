# Feature Specification: internal/transport/sign — canonical-JSON request signing + replay protection

**Feature Branch**: `008-transport-sign`
**Created**: 2026-04-28
**Status**: Draft
**Input**: User description: "internal/transport/sign: canonical-JSON encoding + ECDSA sign/verify over SHA-256(canonical) + nonce cache (sweep goroutine started by explicit Run(ctx)) + timestamp freshness check; concurrent-safe nonce Add with exactly-one firstSeen=true semantics"

## Overview

The `internal/transport/sign` package gives every signed client request a
single, deterministic protocol: a canonical JSON form of the request
payload, an ECDSA signature over the SHA-256 digest of that canonical
form, and a nonce + timestamp pair that defeats replay. The recipient
verifies the signature against a registered client public key, rejects a
nonce it has already seen within its time-to-live, and rejects a
timestamp outside the configured freshness window.

This package is consumed by the server's `/claim` and `/revoke` paths
(SDD-12) and by `hush request` on the client side (SDD-16). The two
sides MUST agree on a single canonicalisation; otherwise every honest
request would fail signature verification and the system would be
unusable. This specification fixes the behaviour both sides depend on.

The package never holds a secret value of its own. It receives the
caller's signing key as a parameter, returns a signature, and never logs
the nonce, the signature, or any portion of the payload.

## Clarifications

### Session 2026-04-28

- Q: How are the nonce and timestamp cryptographically bound to a signed request? → A: Consumers MUST include the nonce and the timestamp as fields of the canonical payload before signing. The package exports only primitives (`Sign`/`Verify` operate on a payload); the signature covers the nonce and the timestamp transitively because they are part of the canonical bytes that are hashed and signed. The contract is documented on callers; the package does not introduce a separate envelope and does not enforce payload shape.
- Q: What upper bound (if any) does the nonce cache enforce on its own memory occupancy, and what happens when it is reached? → A: No explicit cap. The cache's effective size is bounded by `incoming-request-rate × max-nonce-TTL`. Rate-limiting and DoS resistance are the consumer's responsibility (the network boundary plus server-side hardening); the package does not introduce a "cache full" failure mode or evict unexpired entries.
- Q: What is the structural validation rule for a nonce value (length, encoding, character set)? → A: A nonce is a non-empty opaque byte string whose length is bounded (8 ≤ len ≤ 128 bytes inclusive). The package validates only emptiness and length bounds; specific encoding (hex of 32 random bytes, base64, UUID, etc.) is the consumer's choice and is documented in `docs/SECURITY.md` Layer 4. Empty or out-of-range nonces are rejected with a distinct, named encoding-rejection error before any cache lookup.
- Q: Which value types is the canonical encoder required to accept as inputs? → A: JSON-native types only — `nil`, `bool`, signed/unsigned integers, finite floats, strings, slices/arrays, and maps/structs whose field values recursively satisfy the same constraint — plus a `RawMessage`-style escape hatch for embedding already-canonical bytes verbatim. The encoder MUST NOT honour `json.Marshaler` (a non-deterministic `MarshalJSON` would silently break signature verification). Non-native Go types (e.g., `time.Time`, `[]byte`, `time.Duration`) are the consumer's responsibility to convert before calling the encoder — typically timestamps as RFC 3339 strings or epoch integers, byte slices as base64 strings.
- Q: Should the package emit operational metrics for sign / verify / rejection events, or push that responsibility entirely to the consumer? → A: The package emits no metrics. Consumers derive counters from the returned sentinel-error identity in their request handler. This keeps the package a leaf primitives library with zero metrics-system coupling and lets each consumer pick its own backend (Prometheus, OpenTelemetry, expvar, etc.). The package's only operational signals are the sentinel error categories already locked by FR-015.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A signed request reaches the server and is accepted (Priority: P1)

A client serialises a request payload, hashes the canonical JSON form
of that payload, signs the digest with its registered private key, and
sends the request together with the signature, a fresh random nonce,
and the current timestamp. The server reproduces the canonical JSON
form of the same logical payload, recomputes the digest, verifies the
signature against the client's registered public key, accepts the
nonce as never-before-seen within its time-to-live, accepts the
timestamp as inside the freshness window, and admits the request.

**Why this priority**: This is the smallest end-to-end slice that
proves the protocol exists. Without it, no other request-bearing
chunk can ship — `/claim`, `/revoke`, and `hush request` all sit on
top of this contract.

**Independent Test**: Generate a keypair. Build a request payload as
typed values. Compute the canonical form, hash it, sign it, and verify
the signature with the matching public key. Verification succeeds.
Repeat with a tampered payload and verification fails with a typed
signature-rejection error.

**Acceptance Scenarios**:

1. **Given** a request payload, a client private key, a fresh random
   nonce, and the current timestamp, **When** the client signs the
   payload and the server verifies the signature with the matching
   public key, **Then** verification succeeds without error.
2. **Given** a payload that has been altered after signing (any
   field changed, added, or removed), **When** the server verifies
   the signature, **Then** verification fails with a distinct, named
   signature-rejection error and no portion of the payload appears in
   the error message.
3. **Given** a signature produced by a key other than the registered
   client public key, **When** the server verifies the signature,
   **Then** verification fails with the same named
   signature-rejection error (the recipient cannot distinguish
   "wrong key" from "tampered payload" by error type — both are a
   single failure mode).

---

### User Story 2 - An attacker cannot replay a captured request (Priority: P1)

An on-path observer captures a complete, validly-signed request as it
crosses the Tailscale interface and replays the byte-for-byte
identical request seconds later. The server rejects the replay with a
distinct, named nonce-replay error. After the nonce's time-to-live has
elapsed, the same nonce becomes available again — but by then the
captured timestamp is outside the freshness window and the request is
rejected with a distinct, named timestamp-stale error.

**Why this priority**: A signed request without replay defence is a
bearer credential. Replay protection is the load-bearing part of
"signed request" in the threat model — without it, every captured
request becomes a re-issuable token until the underlying private key
rotates.

**Independent Test**: Build a valid signed request. Verify it once
(succeeds). Verify it again with the same nonce (fails with the
nonce-replay error). Advance the simulated clock past the freshness
window. Verify it again with a fresh nonce but the original timestamp
(fails with the timestamp-stale error). Each rejection is a distinct,
named error.

**Acceptance Scenarios**:

1. **Given** a valid signed request that the server has already
   accepted, **When** the same request is presented again with the
   same nonce and the same timestamp, **Then** the server rejects it
   with a distinct, named nonce-replay error.
2. **Given** a valid signed request whose timestamp is older than the
   configured freshness window, **When** the server verifies the
   request, **Then** the server rejects it with a distinct, named
   timestamp-stale error.
3. **Given** a valid signed request whose timestamp is further in
   the future than the configured freshness window allows (clock
   skew on the client side), **When** the server verifies the
   request, **Then** the server rejects it with the same
   timestamp-stale error category.
4. **Given** a nonce that has been accepted and whose time-to-live
   has fully elapsed, **When** the same nonce is presented again
   in a fresh request with a fresh timestamp, **Then** the nonce
   cache no longer holds the prior entry and the request is not
   rejected for nonce reasons.
5. **Given** any rejected request, **When** the rejection is
   logged or returned to the caller, **Then** the rejection
   message contains the failure category but no portion of the
   payload, no signature bytes, and no nonce value.

---

### User Story 3 - Canonical encoding is the same on both sides regardless of map ordering (Priority: P1)

The canonical encoder produces byte-identical output for two
semantically-equal inputs whose underlying representations differ only
in the iteration order of their map-typed fields. A request whose
fields are encoded in one order and a request whose fields are encoded
in a different order — but whose semantic content is identical —
produce the same canonical bytes and therefore the same digest and the
same signature.

**Why this priority**: Two ECDSA-signing implementations that agree on
the keypair but disagree on canonical JSON cannot interoperate. The
client signs `{ "a": 1, "b": 2 }`; the server hashes
`{ "b": 2, "a": 1 }`; verification fails for every honest request.
Determinism of the canonical form is the bridge that makes the entire
chunk usable.

**Independent Test**: Build two structurally distinct in-memory values
that represent the same logical payload (for example, the same set of
key-value pairs presented in different orders). Run both through the
canonical encoder. Assert byte-identical output. Repeat across nested
levels.

**Acceptance Scenarios**:

1. **Given** two payload values that contain the same map keys with
   the same values but where the underlying iteration order differs,
   **When** each value is canonically encoded, **Then** the two
   resulting byte sequences are identical.
2. **Given** payload values whose semantically-equal content occurs
   inside nested maps at every depth, **When** each is canonically
   encoded, **Then** the encoder produces byte-identical output for
   semantically-equal inputs at every depth.
3. **Given** a payload that contains a numeric value that cannot be
   represented in standard JSON (a non-finite floating-point value),
   **When** the encoder is asked to canonicalise the payload,
   **Then** the encoder returns a distinct, named encoding error and
   does not produce a partial canonical form.
4. **Given** a payload value that is already in a canonical-JSON
   shape (a pre-encoded raw segment), **When** that value is
   embedded in an outer payload and the outer payload is
   canonicalised, **Then** the embedded segment is included verbatim
   and the outer encoding remains byte-identical for any
   semantically-equal outer arrangement.

---

### User Story 4 - The nonce cache's background sweep is started explicitly and stops on context cancel (Priority: P1)

The nonce cache exposes a separate "Run" entry point that the caller
invokes — typically the server's startup wiring — to begin the
background sweep that removes expired nonces. The cache does not
spawn any goroutine on its own at construction time. When the caller
cancels the context passed into the Run entry point, the sweep stops
and the cache no longer has any background activity.

**Why this priority**: The project's idiomatic-Go discipline forbids
implicit goroutines and forbids any package that spawns work without
an explicit owner. A goroutine that nobody started cannot be stopped
by anybody, and stale background work in a security package is itself
a defect. Every consumer of this package needs a single, reviewable
lifecycle hook.

**Independent Test**: Construct a nonce cache. Without invoking the
Run entry point, observe (via a goroutine count or equivalent
mechanism) that the cache has not started any background work.
Invoke Run with a fresh context. Observe that exactly one new
goroutine has started. Cancel the context. Observe that the new
goroutine has stopped.

**Acceptance Scenarios**:

1. **Given** a freshly constructed nonce cache that has not had its
   Run entry point invoked, **When** the cache's background activity
   is inspected, **Then** the cache has not spawned any goroutine.
2. **Given** a nonce cache whose Run entry point has been invoked
   with a fresh context, **When** the caller cancels that context,
   **Then** the background sweep stops and no further sweep work
   occurs.
3. **Given** a nonce cache whose Run entry point has not been
   invoked, **When** a caller adds nonces to the cache, **Then** the
   cache still tracks the nonces correctly during their
   time-to-live, but expired entries are not removed until Run is
   invoked.

---

### User Story 5 - Concurrent insertion of the same nonce produces exactly one "first seen" winner (Priority: P1)

Multiple goroutines that submit the same nonce to the cache at the
same instant collectively produce exactly one "first seen" outcome.
Every other concurrent submitter of that nonce sees the
"already-seen" outcome (the nonce-replay error). The winner is not
required to be any particular caller — only that exactly one caller
wins.

**Why this priority**: Without this guarantee, two simultaneous
attempts to use the same nonce might both succeed, breaking the
"each nonce accepted exactly once" property that the entire replay
defence depends on. This is the kind of bug that would never appear
in a single-threaded test and would silently undermine production.

**Independent Test**: Launch N goroutines (where N is large enough
to expose ordering races). Each goroutine submits the same nonce
to the cache with the same time-to-live. Count the number of
"first seen" results across all goroutines. Assert the count is
exactly 1. Repeat under the race detector.

**Acceptance Scenarios**:

1. **Given** N goroutines that each attempt to insert the same
   nonce into the cache concurrently, **When** all goroutines have
   completed, **Then** exactly one goroutine observes a "first seen"
   outcome and N-1 observe an "already-seen" outcome.
2. **Given** N goroutines inserting N distinct nonces concurrently,
   **When** all goroutines have completed, **Then** every goroutine
   observes a "first seen" outcome and the cache contains all N
   nonces.
3. **Given** any concurrent insertion test, **When** the test is
   executed under the race detector, **Then** no data race is
   reported.

---

### User Story 6 - Replay-defence failure modes are individually nameable (Priority: P2)

Every distinct way a signed request can fail — signature mismatch,
nonce already seen, timestamp out of freshness window — is reported
as a distinct, named error. Callers (operations dashboards, audit
logs, error-mapping HTTP handlers) identify the failure category by
the error's identity, not by parsing a message string.

**Why this priority**: A single "request rejected" error makes
incident response harder than it needs to be ("was the bot under a
replay attack, or did a client clock drift?"). The failure
categories already exist in the protocol; surfacing them as named
errors is the cheap way to make operational signals legible.

**Independent Test**: For each defined rejection category, build a
request that triggers exactly that category. Assert the returned
error is identifiable as the corresponding named sentinel. No
rejection collapses into a generic "request rejected" string.

**Acceptance Scenarios**:

1. **Given** a request whose signature does not verify against the
   registered public key, **When** verification runs, **Then** the
   returned error is identifiable as the signature-rejection
   sentinel.
2. **Given** a request whose nonce has already been accepted within
   its time-to-live, **When** verification runs, **Then** the
   returned error is identifiable as the nonce-replay sentinel.
3. **Given** a request whose timestamp is outside the freshness
   window (in either direction), **When** verification runs,
   **Then** the returned error is identifiable as the
   timestamp-stale sentinel.
4. **Given** a code path that needs to distinguish replay from
   forgery (for example, audit log alert tiering), **When** the
   code inspects a returned error, **Then** the three categories
   are distinguishable without any string parsing.

---

### Edge Cases

- A request with a nonce of zero length, or whose length exceeds
  the upper bound (128 bytes), is rejected with a distinct, named
  encoding-rejection error before any cache lookup occurs — not
  silently accepted and not collapsed into the nonce-replay
  sentinel.
- A request whose timestamp is exactly at the freshness boundary
  (past or future) is treated consistently — either always accepted
  or always rejected at that exact boundary, with no off-by-one
  ambiguity. The exact direction is plan-phase, but the behaviour
  is deterministic and asserted by tests.
- A nonce that is presented exactly at the moment its
  time-to-live expires is treated consistently: if the sweep has
  removed it, it is "first seen"; if not, it is "already seen".
  The order of these two events is not user-observable, but the
  caller never receives an inconsistent result on any single call.
- A canonical-encoding attempt against a value that cannot be
  represented (non-finite numeric, unsupported value type) returns
  a distinct, named encoding error and does not produce a partial
  result.
- Random or hostile byte streams supplied as the payload, the
  signature, or both do not panic, do not exhaust memory, and
  always produce a typed verification error. Verification is
  required to be panic-free under fuzz pressure.
- A signed request that is structurally well-formed but whose
  signature has the wrong byte length, wrong DER framing, or any
  other shape problem is rejected with the signature-rejection
  sentinel — never with an untyped panic.
- A nonce-cache lookup by a caller whose context has already been
  cancelled returns a typed error rather than blocking on the
  sweep goroutine.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST expose a canonical JSON encoder that
  produces byte-identical output for any two inputs that are
  semantically equal regardless of map iteration order, at every
  level of nesting.
- **FR-002**: The canonical JSON encoder MUST reject any input
  containing a value that cannot be represented in standard JSON
  (in particular, non-finite floating-point values), returning a
  distinct, named encoding error and no partial output.
- **FR-003**: The system MUST provide a signing operation that
  takes a registered private key and a payload, computes the
  SHA-256 digest of the canonical JSON form of the payload, and
  returns an ECDSA signature over that digest.
- **FR-004**: The system MUST provide a verification operation
  that takes a registered public key, a payload, and a candidate
  signature, recomputes the SHA-256 digest of the canonical JSON
  form of the payload, and returns success only if the signature
  is a valid ECDSA signature for that digest under the supplied
  public key.
- **FR-005**: The verification operation MUST return a distinct,
  named signature-rejection sentinel error for every signature
  failure (wrong key, tampered payload, structurally invalid
  signature). The verification operation MUST NOT return a
  generic untyped error for any signature-failure case.
- **FR-006**: The system MUST provide a nonce cache that records
  each accepted nonce together with its time-to-live, treats a
  re-submitted nonce within that time-to-live as a replay, and
  forgets the nonce after the time-to-live elapses.
- **FR-007**: The nonce cache's insertion operation MUST report
  one of two outcomes per call: "first seen" (the nonce was not
  in the cache and has now been recorded) or "already seen" (the
  nonce was already in the cache within its time-to-live). The
  "already seen" outcome MUST be reported via a distinct, named
  nonce-replay sentinel error.
- **FR-008**: The nonce cache's insertion operation MUST be safe
  to call concurrently from multiple goroutines and MUST
  guarantee that, when N goroutines insert the same nonce
  concurrently, exactly one of them observes the "first seen"
  outcome and the other N-1 observe the "already seen" outcome.
- **FR-009**: The nonce cache MUST NOT spawn any goroutine at
  construction time and MUST NOT spawn any goroutine implicitly
  on first use. The background sweep that removes expired nonces
  MUST be started exclusively via an explicit "Run" entry point
  that the caller invokes with a context.
- **FR-010**: When the context passed to the nonce cache's Run
  entry point is cancelled, the background sweep goroutine MUST
  stop in bounded time. After the sweep has stopped, the cache
  MUST NOT have any background activity attributable to this
  package.
- **FR-011**: The system MUST provide a timestamp freshness
  check that takes a candidate timestamp and a freshness window
  and returns whether the candidate is inside the window. A
  candidate outside the window in either direction (too old or
  too far in the future) MUST produce a distinct, named
  timestamp-stale sentinel error when consumed by the
  verification path.
- **FR-012**: The freshness window MUST be configurable by the
  caller per call. The package MUST NOT impose a hard-coded
  window, but MUST validate that the supplied window is a
  positive duration.
- **FR-013**: A signed request whose signature is valid and
  whose nonce is "first seen" but whose timestamp is outside the
  freshness window MUST be rejected with the timestamp-stale
  sentinel — the package MUST NOT enrol the nonce into the cache
  in that case (an attacker MUST NOT be able to "burn" a nonce
  for an honest client by replaying with a stale timestamp).
- **FR-014**: A signed request whose signature is invalid MUST
  be rejected with the signature-rejection sentinel — the
  package MUST NOT enrol the nonce into the cache in that case
  (an attacker MUST NOT be able to "burn" a nonce by submitting
  a forged signature).
- **FR-015**: The signature-rejection sentinel, the nonce-replay
  sentinel, and the timestamp-stale sentinel MUST be three
  distinct, comparable error values. Callers MUST be able to
  identify the rejection category by sentinel identity, not by
  parsing the error message.
- **FR-016**: Verification MUST be panic-free under hostile
  inputs. Random or malformed payloads, signatures, or
  combinations thereof MUST always produce a typed error and
  MUST NEVER cause the verifying process to panic, exhaust
  memory, or expose a partial signature.
- **FR-017**: The package MUST NOT log any nonce value, any
  signature byte, any portion of the canonical payload, or any
  private key material. Diagnostic surfaces MUST identify the
  failure category and MUST NOT leak request content.
- **FR-018**: Operations on the package's APIs that perform
  cancellable work (verification involving the nonce cache,
  insertion into the nonce cache) MUST accept a context as
  their first parameter and MUST honour context cancellation
  by returning promptly with a typed error.
- **FR-019**: Consumers of the signing operation MUST include
  the nonce and the timestamp as fields of the canonical
  payload that is signed. The package itself MUST NOT introduce
  a separate envelope around the payload; the signature
  transitively covers the nonce and the timestamp because they
  are part of the canonical bytes that are hashed and signed.
  A consumer that omits the nonce or the timestamp from the
  signed payload breaks the replay-defence contract: an
  attacker who captured a valid (payload, signature, nonce,
  timestamp) tuple could otherwise replay the same payload and
  signature with a fresh nonce and a fresh timestamp and the
  request would be admitted. The package documents this
  requirement as part of the calling convention but does not
  enforce payload shape.
- **FR-020**: The nonce cache MUST NOT impose its own upper
  bound on entry count and MUST NOT evict unexpired entries to
  reclaim memory. The cache's effective size is bounded by
  `incoming-request-rate × max-nonce-TTL`; rate-limiting and
  DoS resistance live in upstream layers (network boundary,
  server hardening). The cache MUST NOT expose a distinct
  "cache full" failure mode — `Add` returns only "first seen"
  or the nonce-replay sentinel, never a capacity error.
- **FR-021**: A nonce value MUST be a non-empty opaque byte
  string whose length is between 8 and 128 bytes inclusive. The
  nonce-cache insertion operation MUST reject an empty or
  out-of-range nonce with a distinct, named
  encoding-rejection sentinel error before performing any cache
  lookup — the rejection MUST NOT be reported as a nonce
  replay. The package MUST NOT validate the internal encoding
  (hex, base64, UUID, etc.) of the nonce; the choice of
  encoding belongs to the consumer and is documented in
  `docs/SECURITY.md` Layer 4.
- **FR-022**: The canonical encoder's accepted input domain
  MUST be the JSON-native value set: `nil`, booleans, signed
  and unsigned integers, finite floating-point numbers (per
  FR-002, non-finite floats are rejected), strings,
  slices/arrays, and maps/structs whose field values
  recursively satisfy the same constraint. The encoder MUST
  also accept a `RawMessage`-style value type whose bytes are
  embedded verbatim into the canonical output (the caller
  guarantees those bytes are themselves already canonical).
  The encoder MUST NOT honour any user-supplied `MarshalJSON`
  hook or equivalent serialisation interface — a
  non-deterministic hook would silently break signature
  verification and the failure mode would be undiagnosable.
  Conversion of non-JSON-native Go types (timestamps, byte
  slices, durations, custom marshalable types) to JSON-native
  form is the caller's responsibility before signing.
- **FR-023**: A canonical-encoding attempt against an input
  that contains a value outside the accepted domain (per
  FR-022) — for example, a Go type the encoder does not
  support, a function value, a channel — MUST return a
  distinct, named canonical-encoding-rejection sentinel error
  and MUST NOT produce partial output. This rejection is the
  same sentinel category as the non-finite-float rejection in
  FR-002.
- **FR-024**: The package MUST NOT emit operational metrics
  (counters, gauges, histograms) directly and MUST NOT depend
  on any metrics-system interface. Consumers derive
  counters and other operational signals from the identity of
  the sentinel error returned by sign / verify / nonce-cache
  operations (per FR-015 and FR-021). This division of
  responsibility keeps the package a leaf primitives library
  with zero metrics-backend coupling; each consumer chooses
  its own backend.

### Key Entities

- **Canonical payload**: The byte-deterministic form of a
  request payload, produced by walking the input value and
  serialising it with sorted map keys at every depth. The
  accepted input domain is the JSON-native value set (per
  FR-022) plus a verbatim raw-bytes escape hatch; the encoder
  ignores any user-supplied serialisation hook so that
  determinism cannot be compromised. Equivalent inputs produce
  equivalent canonical payloads.
- **Signed request envelope**: The trio of (canonical payload,
  signature, replay-defence pair). The replay-defence pair is
  a fresh nonce and a current timestamp. The recipient checks
  all three before admitting the request.
- **Nonce-cache entry**: A nonce paired with its time-to-live.
  Lives in the cache until expiry; participates in
  exactly-one-winner concurrent insertion.
- **Sentinel error**: A named, comparable error value
  representing one of the rejection categories: signature
  rejection, nonce replay, timestamp stale, canonical-encoding
  rejection (a payload value that cannot be canonicalised), or
  nonce-encoding rejection (a nonce that is empty or
  out-of-bounds in length). Callers identify rejection by
  sentinel identity.
- **Freshness window**: A caller-supplied positive duration
  bounding how far a candidate timestamp may deviate from the
  current moment in either direction.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Two payload values that are semantically equal
  but differ only in the iteration order of their map-typed
  fields produce byte-identical canonical output, and at least
  ten distinct shape variants (flat, nested, mixed-typed,
  embedded raw-canonical, and similar) are exercised by
  automated tests.
- **SC-002**: A round-trip of (sign, verify) succeeds for any
  payload accepted by the canonical encoder, asserted by an
  automated test that uses a freshly generated keypair on each
  run.
- **SC-003**: A verification with the wrong public key or a
  tampered payload fails with the signature-rejection sentinel
  and never with a generic error, asserted by automated tests.
- **SC-004**: A re-submitted nonce within its time-to-live
  fails with the nonce-replay sentinel; the same nonce
  re-submitted after the time-to-live has fully elapsed is
  accepted as "first seen", asserted by automated tests with a
  controllable clock or sweep trigger.
- **SC-005**: A timestamp older than the configured freshness
  window fails with the timestamp-stale sentinel; a timestamp
  further in the future than the window allows fails with the
  same sentinel; a timestamp inside the window succeeds.
- **SC-006**: Under N concurrent same-nonce insertions
  (N large enough to expose races, exercised under the race
  detector), exactly one goroutine observes the "first seen"
  outcome, asserted by an automated test.
- **SC-007**: The nonce cache spawns zero goroutines at
  construction and spawns its sweep goroutine only on
  invocation of the explicit Run entry point. Cancelling the
  context passed to Run causes the sweep to stop, asserted by
  an automated test.
- **SC-008**: A 60-second random-input fuzz run against the
  verification path completes without panic, without
  exhausting memory, and produces only typed errors for every
  malformed input.
- **SC-009**: Coverage of the package is at least 100% per
  the constitutional bar for security-critical packages.
- **SC-010**: An external review of the package's logs and
  error messages confirms that no nonce, signature byte,
  payload byte, or private key material appears in any
  diagnostic surface.

## Assumptions

- The signing curve and signature shape are fixed by the
  project's crypto stack (secp256k1 ECDSA per the constitution
  and `docs/SECURITY.md` Layer 4); the choice of curve is not
  re-litigated by this specification.
- The hash function is SHA-256, fixed by `docs/SECURITY.md`
  Layer 4 and `docs/SPEC.md` FR-6; alternative hashes are out
  of scope for v0.1.0.
- "Tailscale-only" network boundary protections (FR-8) and
  IP-allowlisting are enforced by other layers (server
  hardening, SDD-10). This package only addresses what is
  cryptographically signed and what is replay-defended; it
  does not perform IP checks.
- The caller is responsible for supplying a registered client
  public key for verification; key registration and rotation
  live elsewhere (SDD-01 derivation, SDD-06 server config).
- Time-to-live and freshness window values are caller-supplied
  on each call. The defaults documented in `docs/SECURITY.md`
  Layer 4 (60s nonce window, ±30s timestamp skew) are
  applied by the consumer (server `/claim` and `/revoke`
  handlers in SDD-12); this package does not assume them.
- The nonce-cache implementation lives entirely in process
  memory. There is no shared cache across server restarts;
  a server restart resets the nonce set. Replays that span
  a restart are still rejected by the timestamp-stale check.
- The package is consumed by the server (SDD-12) and by the
  `hush request` client (SDD-16). Both consumers receive the
  same exported surface and the same set of sentinel errors.
- The clock used by the timestamp freshness check is the host
  system clock; clock-sync is enforced at startup elsewhere
  (FR-17, NTP drift check). Tests inject a fake clock to
  exercise out-of-window cases without waiting in real time.
