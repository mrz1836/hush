# Feature Specification: internal/transport/ecies — wire-level encryption of secret responses

**Feature Branch**: `009-transport-ecies`
**Created**: 2026-04-28
**Status**: Draft
**Input**: User description: "internal/transport/ecies: encrypt secret-response payloads from the server to a per-request ephemeral client key (ECIES); Decrypt returns a fresh SecureBytes; errors are typed and never include any envelope or plaintext byte"

## Overview

The `internal/transport/ecies` package is the wire-level encryption
boundary for every secret value the vault server hands back to a
client. The server takes a fresh secret value plus the client's
per-request ephemeral public key and produces an opaque envelope of
bytes that travels in the HTTP response body. The client takes that
envelope and its matching ephemeral private key and recovers the
plaintext as a freshly-allocated, mlocked `SecureBytes` whose
lifetime the caller owns.

This package is consumed by the server's `/secrets/{name}` handler
(SDD-13) and by the `hush request` client (SDD-16). Both consumers
agree on a single envelope shape; otherwise an honest secret fetch
would fail in the field and the system would be unusable. This
specification fixes the behaviour both sides depend on.

The package never holds a long-lived secret value of its own. It
receives the caller's recipient key as a parameter, returns either
an envelope (encrypt path) or a fresh `SecureBytes` (decrypt path),
and never logs the envelope, the plaintext, or any portion of
either. Every rejection mode is a distinct, named sentinel error
whose message contains the failure category and nothing more.

The package is the implementation of Layer 3 of the project's seven
security layers (`docs/SECURITY.md`). Captured traffic at the
Tailscale interface MUST contain only opaque envelope bytes — no
plaintext secret value ever appears on the wire, in HTTP middleware,
in proxies, or in memory dumps of the HTTP stack. This property is
the load-bearing guarantee of acceptance criterion AC-7.

## Clarifications

### Session 2026-04-28

- Q: How should encrypt's input-validation errors (empty plaintext, invalid/nil recipient public key) be named? → A: Two distinct sentinels — `ErrECIESEmptyPlaintext` and `ErrECIESInvalidRecipientKey`.
- Q: When `ctx` is already cancelled or its deadline already exceeded at call entry, what does the package return? → A: A wrapped error such that `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)` both hold; no new package sentinel for cancellation.
- Q: Does the package emit any log lines of its own, or is observability entirely caller-driven? → A: Caller-driven only — the package emits no logs and takes no logger dependency. FR-014's "no plaintext/envelope/key bytes in diagnostics" holds by construction.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A secret-response envelope round-trips from server to client (Priority: P1)

The server holds a freshly decrypted secret value in protected
memory and the client's per-request ephemeral public key. The
server calls the encrypt operation with the secret value and that
public key and receives an opaque envelope of bytes. The envelope
is sent over the wire and then handed to the decrypt operation on
the client together with the matching ephemeral private key. The
decrypt operation returns a fresh `SecureBytes` whose contents are
byte-for-byte equal to the original secret value. The caller of
decrypt destroys the `SecureBytes` when done.

**Why this priority**: This is the smallest end-to-end slice that
proves the protocol exists. Without it, no other secret-bearing
chunk can ship — the server's secret handler (SDD-13) and the
`hush request` client (SDD-16) both sit on top of this contract.

**Independent Test**: Generate an ephemeral keypair. Build a
plaintext byte slice. Encrypt against the public key, then decrypt
the resulting envelope against the matching private key. The
returned `SecureBytes` exposes contents byte-for-byte equal to the
original plaintext. Repeat across plaintext sizes 1 byte, 1 KiB,
and 1 MiB.

**Acceptance Scenarios**:

1. **Given** a plaintext of N bytes (for N in {1, 1024, 1048576})
   and an ephemeral keypair, **When** the plaintext is encrypted
   to the public key and the resulting envelope is decrypted with
   the matching private key, **Then** the recovered plaintext is
   byte-for-byte equal to the original plaintext.
2. **Given** any successful encrypt-then-decrypt round-trip,
   **When** the caller of decrypt inspects the returned value,
   **Then** the value is a freshly-allocated `SecureBytes` distinct
   from any source slice the caller previously held, and the
   caller — not the package — owns the destroy lifetime.
3. **Given** any successful encrypt operation, **When** the
   resulting envelope is observed by an outside party, **Then** the
   envelope bytes do not contain the plaintext as a substring (a
   sentinel-byte search across the envelope finds nothing).

---

### User Story 2 - An attacker capturing the wire cannot recover the plaintext (Priority: P1)

An on-path observer captures a complete envelope as it crosses the
Tailscale interface. The observer attempts to decrypt the envelope
using a different private key — either a randomly-generated key,
the wrong client's ephemeral key, or a long-lived key from another
context. Every such attempt fails with a distinct, named decrypt
failure sentinel. No fragment of the plaintext appears in the
returned error, and no fragment of the plaintext is recoverable
from any side-channel of the failed call.

**Why this priority**: ECIES exists precisely to prevent a captured
secret-fetch response from being a usable bearer credential to a
non-owner. Without a clean, typed wrong-key failure, an attacker's
decrypt attempt could leak a partial plaintext or reveal the
failure mode through error-message variation. Both are
unacceptable in a secrets-broker.

**Independent Test**: Generate two ephemeral keypairs. Encrypt a
plaintext to the first public key. Attempt decrypt with the second
private key. Verify the call returns the named decrypt-failure
sentinel and that no portion of the plaintext appears in the
returned error or in any logged diagnostic.

**Acceptance Scenarios**:

1. **Given** an envelope produced by encrypting to a recipient
   public key, **When** decrypt is invoked with a private key that
   does not match the recipient public key, **Then** the call
   returns the named decrypt-failure sentinel and no portion of
   the plaintext appears in the error or in any side-effect.
2. **Given** an envelope and the correct private key, but where
   the envelope has been altered after encryption (any byte
   flipped, byte appended, byte removed, or arbitrary mid-envelope
   substitution), **When** decrypt is invoked, **Then** the call
   returns the named decrypt-failure sentinel — the package does
   not distinguish "wrong key" from "tampered envelope" by error
   identity (both are a single failure mode).
3. **Given** any decrypt failure, **When** the rejection is
   surfaced to the caller or to a log sink, **Then** the rejection
   carries the failure category and no portion of the envelope,
   no portion of the plaintext, and no portion of the recipient
   private key.

---

### User Story 3 - A malformed or short envelope is rejected without panic and without leakage (Priority: P1)

A caller hands the decrypt operation a byte slice that is empty,
too short to be a valid envelope, or otherwise structurally
malformed before any cryptographic operation is even possible.
The package recognises the input as structurally invalid and
returns a distinct, named envelope-too-short sentinel error
without invoking any cryptographic primitive and without producing
a partial plaintext. Random byte sequences supplied as the
envelope produce typed errors and never panic, exhaust memory, or
expose a partial plaintext.

**Why this priority**: A secrets-broker that panics or hangs on a
malformed envelope is itself a vulnerability — a hostile client
can otherwise crash the consuming code. The constitutional
fuzz-target requirement (Principle VIII §2 target #3) exists to
prove this property holds under hostile pressure. Distinguishing
"too short to be cryptography" from "cryptography failed" gives
operators an actionable signal in audit logs.

**Independent Test**: Invoke decrypt with envelopes of length 0,
length 1, and lengths up to but not including the documented
minimum envelope size. For each, assert the returned error is the
envelope-too-short sentinel. Run a fuzz test that submits random
byte sequences as the envelope and assert no input causes a panic
or a partial plaintext disclosure.

**Acceptance Scenarios**:

1. **Given** an envelope whose length is below the minimum size
   that any valid envelope can have, **When** decrypt is invoked,
   **Then** the call returns the named envelope-too-short
   sentinel error and the implementation MUST NOT invoke any
   cryptographic primitive.
2. **Given** an envelope of arbitrary random bytes whose length
   meets or exceeds the minimum size, **When** decrypt is
   invoked, **Then** the call returns the named decrypt-failure
   sentinel error and never panics, exhausts memory, or produces
   a partial plaintext.
3. **Given** any envelope-too-short rejection or any
   decrypt-failure rejection, **When** the rejection is surfaced
   to the caller or to a log sink, **Then** the rejection
   message contains the failure category and nothing else — no
   envelope byte, no plaintext byte, no key byte, no length
   value that could feed a probing attack.

---

### User Story 4 - The decrypted plaintext lives only in caller-owned protected memory (Priority: P1)

The decrypt operation returns a fresh `SecureBytes` whose backing
storage was allocated by the package and into which the recovered
plaintext was written. No copy of that plaintext lives in the
package's own memory after decrypt returns. No copy of that
plaintext is held in any package-level cache, finalizer, or
deferred goroutine. The caller that received the `SecureBytes` is
the sole owner of the plaintext lifetime and is responsible for
calling `Destroy` (or equivalent) on the returned value when done.

**Why this priority**: A package that retains its own copy of a
plaintext defeats the constitutional zero-files-at-rest /
mlocked-only / explicit-destroy contract (Principle III Layer 5
and Principle X). The whole reason ECIES is the wire-level
boundary is so that the transport layer never holds a secret a
moment longer than it must. A leak here would silently undermine
every layer above it.

**Independent Test**: Call decrypt and capture the returned
`SecureBytes`. Inspect package-level state (counters, caches,
finalizer registrations) and assert no reference to the
plaintext is retained. Destroy the returned `SecureBytes` and
assert that any subsequent attempt to use it returns the
SecureBytes destroyed-handle error — confirming the package did
not silently retain a usable handle to the underlying memory.

**Acceptance Scenarios**:

1. **Given** a successful decrypt that returns a `SecureBytes`,
   **When** the caller destroys that `SecureBytes`, **Then** any
   subsequent attempt to read it returns the SecureBytes
   destroyed-handle error (the package surrenders ownership; it
   keeps no parallel handle).
2. **Given** a successful decrypt, **When** package-level state
   is inspected after the call returns, **Then** the package
   holds no cached, memoised, or pooled copy of the plaintext or
   the recipient private key.
3. **Given** any decrypt path, **When** the call's intermediate
   buffers (the working plaintext slice the package allocates
   before wrapping it in `SecureBytes`) are inspected after
   wrap, **Then** ownership of those bytes has transferred to
   the `SecureBytes` and the package no longer holds a separate
   reference.

---

### User Story 5 - Encrypt leaves no copy of the plaintext in package memory (Priority: P1)

The encrypt operation copies the caller's input plaintext into an
internal buffer to perform the cryptographic work. Before the
encrypt call returns, that internal buffer is overwritten with
zero bytes. No reference to the plaintext is retained in any
package-level state (no cache, no pool, no finalizer queue, no
deferred goroutine). The caller's input slice is the caller's
responsibility to zero — but the package's working copy is the
package's responsibility, and that responsibility is honoured on
every return path including the error path.

**Why this priority**: The server holds a plaintext secret value
for the shortest possible window — only long enough to produce
the envelope. If the encrypt path retained the plaintext beyond
that window (even briefly, even as a zeroed-on-GC artefact), the
process-memory attack surface from a root compromise expands
proportionally. Zeroing on every return path closes that window
deterministically.

**Independent Test**: Wrap the encrypt call with a hook that
captures the package's internal buffer at the moment of return.
Across happy paths and induced error paths, assert the captured
buffer is all zeroes. Repeat under the race detector.

**Acceptance Scenarios**:

1. **Given** a successful encrypt operation, **When** the call
   returns, **Then** every internal buffer the package allocated
   to hold plaintext bytes during encryption is zero across its
   entire length.
2. **Given** an encrypt call that fails (for example, an invalid
   recipient public key), **When** the call returns the error,
   **Then** every internal buffer the package allocated to hold
   plaintext bytes is zero across its entire length — error
   paths zero with the same discipline as happy paths.
3. **Given** any encrypt call, **When** package-level state is
   inspected after the call returns, **Then** the package holds
   no cached, memoised, or pooled copy of the plaintext or the
   recipient public key.

---

### User Story 6 - Failure modes are individually nameable (Priority: P2)

Every distinct way a decrypt call can fail — envelope too short
to be cryptography, cryptographic decrypt failure (wrong key,
tampered envelope, wrong-curve key, etc.) — is reported as a
distinct, named sentinel error. Callers (the server's
secret-handler audit hook, the client's `hush request`
diagnostic surface) identify the failure category by the error's
identity, not by parsing a message string.

**Why this priority**: A single "decrypt failed" error makes
incident response harder than it needs to be ("did a network
glitch truncate the response, or is a malicious client probing
us with hostile envelopes?"). The two failure categories already
exist in the protocol; surfacing them as distinct sentinels is
the cheap way to make operational signals legible.

**Independent Test**: For each defined rejection category, build
a decrypt input that triggers exactly that category. Assert the
returned error is identifiable as the corresponding named
sentinel. No rejection collapses into a generic "decrypt failed"
string.

**Acceptance Scenarios**:

1. **Given** an envelope whose length is below the minimum a
   valid envelope can have, **When** decrypt runs, **Then** the
   returned error is identifiable as the envelope-too-short
   sentinel.
2. **Given** an envelope whose length meets the minimum but
   whose contents do not decrypt under the supplied private
   key, **When** decrypt runs, **Then** the returned error is
   identifiable as the decrypt-failure sentinel.
3. **Given** a code path that needs to distinguish "structural
   malformedness" from "cryptographic failure" (for example,
   audit-log alert tiering or a fuzz test classifying inputs),
   **When** the code inspects a returned error, **Then** the
   two categories are distinguishable without any string
   parsing.

---

### Edge Cases

- A decrypt call with an envelope of length 0 returns the
  envelope-too-short sentinel without invoking any cryptographic
  primitive — not a generic decrypt failure and not a panic.
- An encrypt call with an empty plaintext is rejected with a
  distinct, named error before any cryptographic primitive is
  invoked. The package does not produce envelopes for empty
  inputs; consumers MUST NOT request encryption of zero-length
  plaintext.
- A decrypt attempt using a private key whose curve does not
  match the curve the envelope was encrypted to returns the
  decrypt-failure sentinel — the failure mode is not
  distinguishable from "wrong key on the same curve".
- An envelope generated by a different ECIES variant or any
  unrelated encoding is rejected as decrypt-failure (or, if
  too short to be a valid envelope of the package's variant,
  envelope-too-short). The package never crashes, never produces
  partial output, and never leaks input bytes through the
  returned error.
- A random or hostile byte stream supplied as the envelope —
  including envelopes whose first bytes resemble a valid
  envelope header but whose tail is corrupt — does not panic,
  does not exhaust memory, and always produces a typed error.
  Decrypt is required to be panic-free under fuzz pressure.
- An envelope appended with a single trailing byte (or any
  number of trailing bytes beyond the variant's expected
  length) is rejected with the decrypt-failure sentinel — the
  package does not silently truncate input.
- A decrypt call whose context has already been cancelled
  returns a typed error rather than performing cryptographic
  work.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST expose an encrypt operation that
  takes a plaintext byte slice and a recipient public key and
  returns an opaque envelope byte slice. The operation MUST
  accept any non-empty plaintext that fits in a single byte
  slice.
- **FR-002**: The system MUST expose a decrypt operation that
  takes an envelope byte slice and a recipient private key and
  returns a freshly-allocated `SecureBytes` whose contents are
  byte-for-byte equal to the original plaintext when the
  envelope was produced by the matching encrypt operation
  against the recipient's public key.
- **FR-003**: The decrypt operation MUST transfer ownership of
  the returned `SecureBytes` to the caller. The package MUST
  NOT retain a parallel handle to the underlying memory and
  MUST NOT register a finalizer that would zero or destroy the
  returned `SecureBytes` from inside the package.
- **FR-004**: The decrypt operation MUST return a distinct,
  named decrypt-failure sentinel error for every cryptographic
  failure (wrong recipient private key, tampered envelope,
  wrong-curve key, any failure that arises only after the
  package has begun cryptographic work). The decrypt operation
  MUST NOT distinguish "wrong key" from "tampered envelope" by
  error identity.
- **FR-005**: The decrypt operation MUST return a distinct,
  named envelope-too-short sentinel error for any envelope
  whose length is below the minimum that any valid envelope of
  the package's variant can have. The envelope-too-short
  rejection MUST occur before the package invokes any
  cryptographic primitive.
- **FR-006**: The package MUST export four distinct, comparable
  sentinel error values: a decrypt-failure sentinel
  (`ErrECIESDecryptFailed`), an envelope-too-short sentinel
  (`ErrECIESEnvelopeTooShort`), an empty-plaintext sentinel
  (`ErrECIESEmptyPlaintext`), and an invalid-recipient-key
  sentinel (`ErrECIESInvalidRecipientKey`). Callers MUST be able
  to identify every rejection category by sentinel identity, not
  by parsing the error message.
- **FR-007**: Error messages produced by the encrypt or decrypt
  operations MUST contain the failure category and nothing
  else. Error messages MUST NEVER contain any byte from the
  plaintext, any byte from the envelope, any byte from the
  recipient public or private key, any length value derived
  from those inputs, or any other content that could feed a
  probing attack. This property MUST be asserted by an
  automated sentinel-leak test.
- **FR-008**: The encrypt operation MUST zero every internal
  buffer it allocated to hold plaintext bytes before returning,
  on both happy paths and error paths. The package MUST NOT
  rely on the garbage collector to zero such buffers.
- **FR-009**: The decrypt operation MUST allocate a fresh byte
  slice for the recovered plaintext, wrap that slice in a
  `SecureBytes` (whose constructor is responsible for zeroing
  any source bytes), and return the `SecureBytes`. The package
  MUST NOT keep a separate reference to the recovered plaintext
  bytes after wrap.
- **FR-010**: The encrypt operation MUST accept the caller's
  input plaintext as a byte slice and MUST NOT accept the input
  plaintext as a Go `string`. The decrypt operation MUST return
  the recovered plaintext only as a `SecureBytes` and MUST NOT
  return the recovered plaintext as a plain byte slice or a
  Go `string`.
- **FR-011**: The package MUST NOT cache or memoise any
  recipient key (public or private) across calls. Each call
  receives the key it needs as a parameter and the package
  retains no reference to that key after the call returns.
- **FR-012**: The decrypt operation MUST be panic-free under
  hostile inputs. Random or malformed envelopes — including
  envelopes whose first bytes resemble a valid header but whose
  tail is corrupt — MUST always produce a typed error and MUST
  NEVER cause the decrypting process to panic, exhaust memory,
  or expose a partial plaintext.
- **FR-013**: The encrypt and decrypt operations MUST accept a
  context as their first parameter and MUST honour context
  cancellation by returning promptly with a typed error. A
  cancelled context MUST NOT cause the package to perform
  cryptographic work, and MUST NOT cause the package to leak a
  partially-zeroed plaintext buffer. The returned error MUST
  wrap the underlying `ctx.Err()` such that
  `errors.Is(err, context.Canceled)` and
  `errors.Is(err, context.DeadlineExceeded)` both evaluate true
  for their respective cancellation causes. The package MUST
  NOT introduce a separate cancellation sentinel; the standard
  library's two cancellation causes are the canonical
  identities for this rejection.
- **FR-014**: The package MUST NOT emit log lines of its own and
  MUST NOT take a logger dependency. All observability for
  encrypt/decrypt outcomes is caller-driven — emitted by the
  consumers (SDD-13 server handler, SDD-16 client) using the
  request context they own. Because the package does not log,
  the prohibition on plaintext, envelope, key, or derived
  intermediate bytes appearing in any diagnostic emitted by
  this package holds by construction; error messages remain the
  only diagnostic surface and are constrained by FR-007.
- **FR-015**: The encrypt operation MUST reject an empty
  plaintext input with the `ErrECIESEmptyPlaintext` sentinel
  before invoking any cryptographic primitive. Producing an
  envelope for a zero-length plaintext is not a defined
  operation of this package.
- **FR-015a**: The encrypt operation MUST reject an invalid
  recipient public key (nil pointer, wrong-curve key, or any
  structurally unusable key) with the
  `ErrECIESInvalidRecipientKey` sentinel before invoking any
  cryptographic primitive. The rejection MUST occur before the
  package allocates or copies any plaintext bytes.
- **FR-016**: The package MUST NOT introduce any cryptographic
  dependency beyond the curve and primitives already locked by
  the project's crypto stack (Constitution Principle XI).
  Adding a new cryptographic library is a constitutional
  amendment, not a package-local decision.

### Key Entities

- **Plaintext**: The bytes the server is encrypting toward a
  client — a secret value whose lifetime in any unencrypted
  form is the shortest the protocol allows. Treated as
  caller-owned input on the encrypt path; treated as
  package-allocated, caller-owned `SecureBytes` on the decrypt
  path.
- **Envelope**: The opaque byte sequence the encrypt operation
  produces. The envelope carries the ECIES output (ephemeral
  public key, ciphertext, integrity tag, and any other
  variant-specified metadata) as a single sequence. Its
  internal layout is plan-phase detail; its only externally
  observable property is "decrypt with the matching private
  key recovers the plaintext".
- **Recipient public key**: An elliptic-curve public key
  supplied to the encrypt operation. Originates from the
  client's per-request ephemeral keypair and is registered in
  the issued JWT's `ephemeral_pubkey` claim.
- **Recipient private key**: The matching elliptic-curve
  private key supplied to the decrypt operation. Lives only in
  the client's per-request ephemeral memory and is destroyed
  at the end of the session.
- **Sentinel error**: A named, comparable error value
  representing one of four rejection categories — two
  decrypt-side and two encrypt-side. Decrypt-side:
  decrypt-failure (any cryptographic failure under decrypt) or
  envelope-too-short (the input is structurally too short to be
  a valid envelope before any cryptography runs). Encrypt-side:
  empty-plaintext (the caller passed a zero-length input) or
  invalid-recipient-key (the caller passed a nil or
  structurally unusable public key). Callers identify rejection
  by sentinel identity.
- **SecureBytes handle**: The mlocked, zero-on-destroy
  protected-memory wrapper the decrypt operation returns. Its
  ownership semantics, destroy contract, and post-destroy
  error behaviour are fixed by `internal/vault/securebytes`
  (SDD-02) and reused here without modification.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A round-trip of (encrypt, decrypt) succeeds for
  plaintext sizes 1 byte, 1 KiB, and 1 MiB, asserted by an
  automated test that uses a freshly-generated ephemeral
  keypair on each run and asserts byte-for-byte plaintext
  equality.
- **SC-002**: A decrypt with a non-matching private key fails
  with the named decrypt-failure sentinel and never with a
  generic error, asserted by an automated test that generates
  two ephemeral keypairs and crosses them.
- **SC-003**: A decrypt with a tampered envelope (any byte
  flipped, byte appended, byte removed) fails with the named
  decrypt-failure sentinel — the same sentinel as the
  wrong-key case — asserted by an automated test.
- **SC-004**: A decrypt with an envelope of length 0, length
  1, and any length up to but not including the documented
  minimum returns the named envelope-too-short sentinel
  without invoking any cryptographic primitive, asserted by
  an automated test.
- **SC-005**: A 60-second random-input fuzz run against the
  decrypt operation completes without panic, without
  exhausting memory, and produces only typed errors for every
  malformed input — satisfying constitutional fuzz target #3.
- **SC-006**: A sentinel-leak test that encrypts a known
  marker byte sequence (`SECRET_SHOULD_NEVER_APPEAR_9`),
  mangles the resulting envelope, and asserts the marker is
  absent from the returned error's message and from any
  captured log output passes on every run.
- **SC-007**: After a successful decrypt, the caller-destroyed
  `SecureBytes` returns the destroyed-handle error on
  subsequent reads — confirming the package surrenders
  ownership and keeps no parallel handle, asserted by an
  automated test.
- **SC-008**: After every encrypt return path (happy and
  error), the package's internal plaintext buffer is zero
  across its entire length, asserted by an automated test
  that captures the buffer at the moment of return.
- **SC-009**: Coverage of the package is at least 100% per
  the constitutional bar for security-critical packages
  (`docs/SPEC.md` AC-9, Constitution Principle VIII).
- **SC-010**: An external review of the package's logs and
  error messages confirms that no plaintext byte, envelope
  byte, or key byte appears in any diagnostic surface.
- **SC-011**: Calling encrypt or decrypt with an already-cancelled
  context returns an error for which
  `errors.Is(err, context.Canceled)` is true; calling with an
  already-deadline-exceeded context returns an error for which
  `errors.Is(err, context.DeadlineExceeded)` is true — asserted
  by automated tests for both operations.

## Assumptions

- The encryption scheme is ECIES over the elliptic curve
  already locked by the project's crypto stack (Constitution
  Principle III, `docs/SECURITY.md` Layer 3, `docs/SPEC.md`
  FR-5). The choice of curve and the specific ECIES variant
  (KDF, symmetric cipher, integrity primitive) are plan-phase
  detail and are not re-litigated by this specification.
- The recipient ephemeral keypair is generated per request by
  the client (`hush request`, SDD-16) and its public half is
  delivered to the server inside the JWT's `ephemeral_pubkey`
  claim (`docs/SPEC.md` FR-4, FR-5). Key generation,
  registration, and lifetime are out of scope for this
  package.
- The `SecureBytes` type is the project's mlocked,
  zero-on-destroy protected-memory wrapper defined by
  `internal/vault/securebytes` (SDD-02). Its constructor
  zeroes any source slice it copies, its `Destroy` is
  idempotent, and reads after destroy return a typed
  destroyed-handle error. This package consumes those
  guarantees and does not re-implement them.
- The Tailscale-only network boundary, the IP-allowlist, and
  the request-signing layer (SDD-08) are enforced by other
  layers. This package only addresses what is encrypted on
  the wire and what is recovered on the receiving end; it
  does not perform IP checks, signature verification, or
  audit-log emission.
- The caller of decrypt is responsible for destroying the
  returned `SecureBytes` when done. The package's contract
  ends at "fresh `SecureBytes`, ownership transferred";
  enforcement of caller destroy hygiene lives in the
  consumer's tests (`hush request` post-exec key-zeroing
  assertion in SDD-16).
- The caller of encrypt is responsible for the lifetime of
  its own input plaintext slice (whether to zero it after
  the call). The package's contract is to zero only its own
  internal copy; it does not mutate or reach into the
  caller's slice.
- Envelope size is bounded by the secret value's plaintext
  size plus a fixed envelope overhead. The package does not
  impose its own upper bound on plaintext size beyond what
  the underlying ECIES variant supports; an upstream consumer
  that wants a hard cap on secret size enforces that cap at
  its own boundary, not inside this package.
- The clock used for any operational diagnostic (audit logs,
  alert timestamps) is the host system clock; clock-sync is
  enforced at startup elsewhere (`docs/SPEC.md` FR-17, NTP
  drift check).
- The package is consumed by the server's secret-fetch
  handler (SDD-13) and by the `hush request` client
  (SDD-16). Both consumers receive the same exported surface
  and the same set of sentinel errors.
