# Feature Specification: Secure Bytes Container

**Feature Branch**: `002-securebytes`
**Created**: 2026-04-27
**Status**: Draft
**Input**: User description: "internal/vault/securebytes: provide a SecureBytes type that mlocks its bytes, zeroes them on destroy and on GC, and renders as [redacted] in every standard log/format/JSON path; the only read path is a borrow-checked callback"

## Overview

The `internal/vault/securebytes` package provides a `SecureBytes`
container that wraps a binary payload (e.g. a derived key, a
decrypted secret value, a session token, an ephemeral private key)
under three simultaneous protections:

1. **Memory protection** — the payload is held in non-swappable
   memory so it cannot be paged to disk.
2. **Render protection** — the value renders as the literal string
   `"[redacted]"` in every standard logging, formatting, and JSON
   serialisation path, so accidental disclosure through telemetry is
   impossible by construction rather than by reviewer vigilance.
3. **Lifecycle protection** — the payload is overwritten with zero
   bytes both on explicit destruction and when the value becomes
   unreachable, so a forgotten container does not leave plaintext
   lingering in the process address space.

The container is the foundation for every secret-handling package
downstream of it: vault payload decoding, JWT signing-key custody,
ECIES envelope key handling, registered client keys, and the
supervisor's optional grace-window cache. Its acceptance criterion
is **AC-7 (Layer 5 — secure memory)**.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Hold and use a secret without ever leaking it (Priority: P1)

An internal package needs to load a secret value (e.g. a derived
encryption key, a decrypted vault entry, a JWT signing key), use it
for a bounded operation (encrypt/decrypt/sign), and discard it —
without ever risking the secret showing up in a log line, a panic
trace, an error message, a JSON-encoded debug dump, or a copy paged
to swap.

**Why this priority**: Without this story, no other secret-handling
package in the system can be implemented safely. Every downstream
chunk (SDD-03 vault, SDD-07 token, SDD-09 ECIES, SDD-13 handlers,
SDD-16 request, SDD-21 grace cache) is blocked on it.

**Independent Test**: A test harness wraps a known sentinel byte
sequence in a container, exercises every standard rendering path
(log entry, formatted string, JSON encoding), and asserts that the
sentinel never appears in any captured output. The harness then
performs a borrow read of the wrapped bytes and confirms the
callback receives the exact original bytes.

**Acceptance Scenarios**:

1. **Given** a container holding a known byte sequence,
   **When** the container is logged through the standard structured
   logger, formatted via the standard formatting facility, or
   encoded as JSON,
   **Then** every captured output contains the literal string
   `[redacted]` and contains zero bytes of the original sequence.
2. **Given** a container holding a known byte sequence,
   **When** a caller invokes the borrow read with a callback,
   **Then** the callback receives a buffer whose contents equal the
   original sequence and whose length equals the original length.
3. **Given** a container constructed from a caller-supplied buffer,
   **When** the constructor returns,
   **Then** the caller-supplied buffer contains only zero bytes.
4. **Given** a container holding a known byte sequence,
   **When** the host operating system experiences memory pressure
   sufficient to swap arbitrary user pages,
   **Then** the container's payload region is not paged to disk.

---

### User Story 2 — Explicitly destroy a secret the moment it is no longer needed (Priority: P1)

The same internal package must be able to deterministically zero a
secret as soon as the bounded operation completes — not "eventually",
but immediately, before the next line of code runs. After
destruction, any further attempt to read the container must fail
with a distinct, identifiable failure rather than silently returning
stale or zeroed bytes.

**Why this priority**: Deterministic zeroing on a known boundary
(handler return, process shutdown, SIGTERM, session end) is the
primary way the system bounds the lifetime of plaintext secrets in
memory. Relying solely on automatic reclamation is insufficient
because reclamation timing is non-deterministic.

**Independent Test**: A test creates a container, destroys it,
inspects the previously-occupied memory region for residual content,
and then attempts a borrow read on the destroyed container — the
read must return the named "destroyed" failure.

**Acceptance Scenarios**:

1. **Given** a live container holding a known byte sequence,
   **When** the caller invokes destroy,
   **Then** the previously-held payload region contains only zero
   bytes and the memory is no longer marked non-swappable.
2. **Given** a container that has been destroyed,
   **When** the caller invokes the borrow read,
   **Then** the operation returns a distinct, named failure
   indicating the container is destroyed; the callback is not
   invoked.
3. **Given** a container that has been destroyed,
   **When** the caller invokes destroy a second time,
   **Then** the operation succeeds without panic, error, or state
   corruption (destruction is idempotent).
4. **Given** a destroyed container,
   **When** the destroyed container is logged, formatted, or
   JSON-encoded,
   **Then** the output renders as `[redacted]` and contains no
   diagnostic about the previously-held payload.

---

### User Story 3 — Forgotten secrets are zeroed automatically before reclamation (Priority: P2)

A consumer may, in error or in a panic-recovery path, drop the last
reference to a container without first calling destroy. The
container's payload must still be overwritten with zero bytes
before the underlying memory is returned to the allocator, so that a
forgotten container cannot leave plaintext in the process address
space awaiting later inspection (e.g. a memory dump generated for an
unrelated bug).

**Why this priority**: Defence in depth. Story 2 covers the happy
path; this story covers the failure path where the consumer forgets
its responsibility. Without it, a bug in any caller becomes a
secret-disclosure bug.

**Independent Test**: A test allocates a container, drops the last
reference to it, forces a memory-reclamation cycle, and verifies via
an instrumentation hook that the destruction routine ran (and
therefore the payload was zeroed and the memory unpinned) before the
underlying allocation could be reused.

**Acceptance Scenarios**:

1. **Given** a container with no remaining live references and no
   prior explicit destroy call,
   **When** the runtime performs a forced memory-reclamation cycle,
   **Then** the destruction routine for that container runs before
   the allocation is recycled, the payload region is overwritten
   with zero bytes, and the memory is no longer marked
   non-swappable.

---

### User Story 4 — The package is portable across the project's supported platforms (Priority: P2)

The container must function identically on macOS and Linux without
introducing any C-language interop dependency, because the project
ships pure-Go binaries on both platforms (no C toolchain at build
time, no shared libraries at run time).

**Why this priority**: Cross-platform portability is a constitutional
requirement of the project (no C interop). A container that worked
only on one platform, or that required a C toolchain, would block
the v0.1.0 release on the other supported platform.

**Independent Test**: The full behavioural test suite for the
container runs and passes on both macOS and Linux build environments
with no toolchain or runtime dependency on a C compiler or shared
library.

**Acceptance Scenarios**:

1. **Given** the project's standard pure-Go build configuration,
   **When** the package is built on macOS,
   **Then** the build succeeds and every behavioural test passes.
2. **Given** the project's standard pure-Go build configuration,
   **When** the package is built on Linux,
   **Then** the build succeeds and every behavioural test passes.

---

### Edge Cases

- **Empty payload**: Constructing a container from a zero-length
  buffer must succeed and yield a container of length zero. Borrow
  reads on it must invoke the callback with a zero-length buffer.
- **Memory-protection request denied by the OS**: If the host
  operating system refuses the non-swappable allocation request
  (e.g. a per-process locked-memory limit is exhausted), construction
  must fail with a distinct, identifiable failure rather than fall
  back to unprotected memory.
- **Concurrent borrow and destroy**: When one caller is mid-borrow
  and another caller invokes destroy, the destroy must not yank the
  payload out from under the borrow callback. After all in-flight
  borrows complete, the destroy takes effect and subsequent borrows
  fail with the named "destroyed" failure.
- **Concurrent borrows**: Multiple simultaneous borrow callbacks on
  the same live container must each see the correct payload bytes,
  with no data race observable under race-detector instrumentation.
- **Borrow callback that retains the buffer**: A callback that
  smuggles the buffer reference out beyond the call (e.g. assigns it
  to a long-lived variable) cannot be prevented at the language
  level. This is documented as a caller contract; a violation is a
  caller bug, not a container failure. The package's reading-path
  documentation must call this out.
- **Borrow callback that panics**: A panic from the callback must
  not leave the container in a corrupted state — it must remain a
  live, undamaged container that subsequent callers can borrow,
  destroy, or render.
- **Logging or JSON-encoding a destroyed container**: Continues to
  render `[redacted]`. The destroy state is not exposed by the
  rendering path.
- **Reading length of a destroyed container**: Defined behaviour —
  reports zero (the underlying buffer has been zeroed and is no
  longer in use).

## Requirements *(mandatory)*

### Functional Requirements

**Memory protection**

- **FR-001**: A `SecureBytes` container MUST hold its payload in
  memory protected against being paged to swap by the host operating
  system, for as long as the container is live.
- **FR-002**: When a `SecureBytes` container is destroyed (whether
  explicitly or by reclamation), the system MUST release the
  swap-protection on the payload memory before that memory is
  recycled.

**Construction**

- **FR-003**: Construction MUST accept exactly one input: a raw
  binary buffer. No string-typed input MUST be accepted, because
  inputs typed as strings cannot be reliably zeroed by the
  constructor.
- **FR-004**: Before the constructor returns, the system MUST
  overwrite the caller-supplied input buffer with zero bytes, so the
  caller's local copy of the secret is destroyed at the same moment
  the protected copy is established.
- **FR-005**: If the host operating system refuses to grant
  swap-protection (e.g. a resource limit is exhausted), construction
  MUST fail with a distinct, identifiable failure and MUST NOT
  silently fall back to unprotected memory.

**Reading**

- **FR-006**: The ONLY supported way to read the payload MUST be a
  borrow read — the caller supplies a callback, and the system
  invokes that callback with the payload buffer for the duration of
  the call only.
- **FR-007**: No accessor (getter, exporter, copy-out, or any other
  named method) on the container MUST return the payload directly to
  the caller. The payload MUST NOT be reachable except through the
  borrow read.
- **FR-008**: A borrow read MUST be safe to invoke from multiple
  callers concurrently against the same live container.
- **FR-009**: A borrow read on a destroyed container MUST NOT invoke
  the callback. Instead, it MUST return a distinct, named failure
  identifying the destroyed state.

**Explicit destruction**

- **FR-010**: A container MUST expose an explicit destroy operation
  that overwrites the payload with zero bytes and releases the
  swap-protection.
- **FR-011**: The destroy operation MUST be idempotent — invoking it
  on an already-destroyed container MUST succeed without panic,
  error, or any change to the destroyed state.
- **FR-012**: Any read attempt on a destroyed container (whether the
  container was destroyed explicitly or by reclamation) MUST return
  the named "destroyed" failure rather than returning stale or
  zeroed bytes through the borrow callback.

**Reclamation safety net**

- **FR-013**: When a container becomes unreachable (no remaining
  live references) without first being explicitly destroyed, the
  system MUST overwrite the payload with zero bytes and release the
  swap-protection before the underlying memory is recycled.

**Render protection**

- **FR-014**: When a container is rendered through the project's
  standard structured logging facility, the rendered value MUST be
  the literal string `[redacted]` and MUST NOT include any byte of
  the payload, the payload length, or any other identifying detail.
- **FR-015**: When a container is rendered through the standard
  string-formatting facility (the rendering used by the project's
  default print/format paths), the rendered value MUST be the
  literal string `[redacted]`.
- **FR-016**: When a container is encoded as JSON, the encoded value
  MUST be the literal string `[redacted]` (encoded as a JSON string
  value).
- **FR-017**: The render-protection requirements (FR-014, FR-015,
  FR-016) MUST hold both before and after destruction. A destroyed
  container MUST render exactly the same as a live one.

**Length introspection**

- **FR-018**: The container MUST expose a length operation that
  returns the byte length of the payload without exposing the
  payload itself. After destruction, the length operation MUST
  report zero.

**Portability**

- **FR-019**: The package MUST function correctly on macOS and on
  Linux.
- **FR-020**: The package MUST NOT introduce any dependency on C
  interop. The project's pure-Go build configuration MUST be
  preserved.

### Key Entities

- **SecureBytes container** — An opaque, reference-typed value that
  holds exactly one binary payload under simultaneous memory
  protection (non-swappable), render protection (always renders as
  `[redacted]`), and lifecycle protection (zeroed on explicit
  destroy and on reclamation). Has these observable properties:
  payload length; live-or-destroyed state. Has these observable
  operations: borrow read, destroy, length query, standard-render
  paths.
- **Borrow callback** — A short-lived function supplied by the
  caller of the borrow read. Receives the payload buffer for the
  duration of the call. By contract, MUST NOT retain the buffer
  beyond the call; this contract is documented but not enforceable
  at the language level.
- **Destroyed-state failure** — The distinct, named failure returned
  by any read attempt against a container whose payload has already
  been zeroed (whether by explicit destroy or by reclamation).
  Callers can detect this failure programmatically and distinguish
  it from any other failure mode.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001 (Render protection — sentinel)**: For every standard
  rendering path (logging, formatting, JSON), a captured copy of the
  output produced for a container holding a known sentinel byte
  sequence contains the literal string `[redacted]` and contains
  zero occurrences of the sentinel.
- **SC-002 (Explicit destruction zeroes payload)**: After explicit
  destruction, an inspection of the previously-locked payload region
  shows only zero bytes.
- **SC-003 (Reclamation zeroes payload)**: A container that loses
  all live references without explicit destruction has its payload
  region overwritten with zero bytes before the underlying memory is
  recycled, verified by a forced reclamation cycle in test.
- **SC-004 (No exposing accessor)**: The container's public surface
  contains exactly one operation that produces the payload bytes,
  and that operation is the borrow read. There is no accessor that
  returns the payload to the caller.
- **SC-005 (String-typed construction is impossible)**: It is not
  possible to construct a container from a string-typed input
  through any public surface of the package.
- **SC-006 (Caller's input buffer is zeroed)**: After construction
  returns, the caller-supplied input buffer contains only zero bytes.
- **SC-007 (Idempotent destruction)**: Two consecutive destroy calls
  on the same container both succeed, and the second is a no-op
  with no state change.
- **SC-008 (Named destroyed failure)**: A borrow read invoked on a
  destroyed container returns a failure that callers can detect
  programmatically and distinguish from every other failure mode the
  package can emit.
- **SC-009 (Cross-platform)**: The package's full behavioural test
  suite passes on both macOS and Linux under the project's standard
  pure-Go build configuration, with no C-toolchain dependency.
- **SC-010 (Race-clean)**: With many concurrent callers performing
  borrow reads against the same live container, the package's test
  suite passes under race-detector instrumentation with no reported
  data races.
- **SC-011 (Explicit failure on memory-protection denial)**: When
  the host operating system denies the swap-protection request,
  construction returns an identifiable failure to the caller.

## Assumptions

- **Caller honours the borrow contract**: Callers do not retain the
  payload buffer beyond the lifetime of the borrow callback. This
  cannot be enforced at the language level and is documented as a
  caller responsibility.
- **OS provides a non-swappable memory primitive without C interop**:
  Both supported operating systems expose a way to mark a memory
  region as non-swappable that is reachable from the project's
  pure-Go build configuration.
- **Runtime memory-management residual risk is accepted**: The Go
  runtime may, under some pathological conditions, copy heap objects
  in ways that leave a transient unprotected duplicate in memory.
  This risk is documented in `docs/SECURITY.md` Layer 5 and §6 and
  is outside the threat model the package is designed to defeat
  (commodity malware enumerating dotfiles for secrets, not
  root-level memory forensics).
- **Internal-only consumption**: The package is consumed only by
  other packages inside the project's `internal/` tree; it is not
  exposed across the project's external API boundary.
- **Supported platforms**: macOS and Linux. Windows is out of scope
  for v0.1.0.
- **No alternative read paths in scope**: The package does not
  provide any "copy-out", "read-only view", "iterator", or
  comparison-by-content (e.g. "equals another secret") accessor in
  this iteration. If a downstream consumer requires constant-time
  comparison of two secrets, that operation is added through a
  borrow-read-based helper in a follow-up chunk, not through a new
  accessor on the container.

## Out of Scope

- Comparison primitives (constant-time equality between two
  containers). Deferred until the first downstream consumer needs
  it.
- Resizing or appending to a container after construction. The
  payload is immutable for the container's lifetime.
- Serialisation of the payload to disk, the network, or any other
  durable medium. The container is for in-process use only.
- Cross-process sharing. A container is owned by exactly one process
  and is not transferable.
- Windows support.
- Mitigations beyond the documented design for the runtime-copy
  residual risk noted in `docs/SECURITY.md`.

## Dependencies

- **Constitutional principle III, layer 5**
  (`.specify/memory/constitution.md`) — defines the security layer
  this package implements.
- **Constitutional principle X (Observability & Redaction)** —
  mandates the type-driven `[redacted]` rendering this package
  enforces, and explicitly names this container type as the reason
  redaction can be relied upon by every other package.
- **Constitutional principle IX (Idiomatic Go Discipline)** —
  requires the package to ship without C interop.
- **`docs/SECURITY.md` §3 Layer 5** — the threat-model and known
  limitations narrative this package implements.
- **`docs/AC-MATRIX.md` AC-7** — the release-gate row this package
  contributes to.
- **Downstream packages blocked on this one**: SDD-03 (vault file
  format), SDD-07 (JWT issue/validate/store), SDD-13 (server
  handlers), SDD-16 (`hush request` decrypt path), SDD-21
  (supervisor refill / refresh / grace cache).
