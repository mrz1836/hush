# Feature Specification: internal/server — HTTP server skeleton, ordered startup checks, and SIGHUP atomic vault reload

**Feature Branch**: `010-server-skeleton`
**Created**: 2026-04-30
**Status**: Draft
**Input**: User description: "internal/server: HTTP server skeleton with stdlib router, middleware stack (request ID, IP allow-list, panic recover that never logs request bodies), ordered startup checks (clock → file modes → Tailscale bind → state dir, refuse to start on first failure), and SIGHUP-driven atomic vault reload with drain window"

## Overview

The `internal/server` package is the chassis on which every secret
the project hands out is delivered. Concretely, it provides four
things: a registered HTTP route surface under the configured
opaque path prefix, a middleware stack every request passes
through, a deterministic ordered set of startup checks the process
runs before it ever binds a socket, and a SIGHUP-driven reload
path that swaps the live vault under in-flight traffic without
dropping a request.

The package does not implement the project's HTTP handlers.
The `/claim` handler (SDD-12), the `/secrets/{name}` and
`/revoke/{jti}` and `/hz` handlers (SDD-13), the runtime entry
point `cmd/hush serve` (SDD-14), and the Discord-backed approver
(SDD-11) all sit on top of this chassis. This specification fixes
only the chassis: the lifecycle, the middleware contract, the
refuse-to-start semantics, and the reload semantics.

The reason the chassis is its own package is that the security
guarantees it enforces — "the server never starts on a
misconfigured host", "a captured request body never appears in a
panic log", "an in-flight request is never dropped because of a
secret rotation" — are properties of the lifecycle itself, not of
any individual handler. They MUST hold the moment the process
starts and they MUST hold during every reload thereafter, no
matter which handlers the higher layers later mount.

The package is the implementation of acceptance criterion AC-1
(`hush serve` starts and serves), the SIGHUP-reload half of AC-2
(vault round-trip + atomic hot-reload), and AC-8 (server
hardening: refuse-to-start on bind / file-mode / NTP failures).
It is also the home of the `Approver` interface placeholder that
SDD-11 implements — every other consumer of the approval surface
takes this package's interface, so the type lives here even though
the wiring lives elsewhere.

## Clarifications

### Session 2026-04-30

- Q: What default drain window does the chassis ship with for SIGHUP vault reload (between atomic swap and destroy of the previous vault)? → A: 30s
- Q: What HTTP status code does the chassis return when a request's source IP is not on the configured client-IP allow-list? → A: 403 Forbidden
- Q: What HTTP status code does the recover middleware return to the client when a handler panics? → A: 500 Internal Server Error
- Q: What default graceful-shutdown timeout does the chassis ship with (between context cancellation and lifecycle return)? → A: 30s
- Q: What is the chassis's clock-drift tolerance threshold for the NTP startup check (above which the launch refuses to start)? → A: 60s (aligns with `docs/SPEC.md`)

## User Scenarios & Testing *(mandatory)*

### User Story 1 - The server refuses to start on a misconfigured host (Priority: P1)

The operator runs `hush serve`. Before the process binds any
socket, it executes a fixed sequence of startup checks against
the host environment: the system clock is NTP-synchronised within
tolerance; every file inside the configured state directory is at
or below mode 0600 and the directory itself is at or below mode
0700; the configured listen address resolves to a Tailscale CGNAT
interface; the state directory exists, is owned by the running
user, and is a regular directory. If any check fails, the
process exits with a non-zero status, prints a distinct
identifiable error for that specific check, and never opens a
listening socket. The remaining checks after the failing one are
not run.

**Why this priority**: This is the single largest piece of the
project's defense-in-depth posture that can be tested at the
chassis layer. The whole reason `hush` is "Tailscale-only and
never public" (Constitution VI) and "file permissions enforced
at startup" (`docs/SPEC.md` FR-15) and "NTP-sync verified at
startup" (FR-17) is that a misconfigured host would otherwise
silently expose the entire vault. Refuse-to-start is the cheapest
and loudest way to hold those properties — and it MUST hold on
every launch, including launchd auto-restarts, not just the first.

**Independent Test**: Build a server with each kind of
misconfiguration in turn — clock unsynced, a state-directory
file at mode 0644, listen addr `0.0.0.0`, state dir missing —
and assert that each launch returns a distinct, named startup-
check error, exits non-zero, and never binds a socket. Build a
correctly-configured host and assert the server starts and
accepts a TCP connection on the configured listen address.

**Acceptance Scenarios**:

1. **Given** a host whose system clock is not NTP-synchronised
   (or whose absolute drift exceeds 60 seconds), **When** the
   server is launched, **Then** the launch returns the named
   clock-unsynchronised startup-check error, exits with a
   non-zero status, and never opens a listening socket.
2. **Given** a state directory that contains a file whose mode
   is more permissive than 0600 (or whose parent directory is
   more permissive than 0700), **When** the server is launched,
   **Then** the launch returns the named file-mode startup-check
   error, exits with a non-zero status, and never opens a
   listening socket.
3. **Given** a configured listen address that does not resolve
   to a Tailscale CGNAT interface (including `0.0.0.0`,
   `127.0.0.1`, an empty host, or a non-Tailscale interface),
   **When** the server is launched, **Then** the launch returns
   the named bind-not-on-tailscale startup-check error, exits
   with a non-zero status, and never opens a listening socket.
4. **Given** a configured state directory that is missing, that
   is not a directory, or whose ownership does not match the
   running user, **When** the server is launched, **Then** the
   launch returns the named state-directory startup-check error,
   exits with a non-zero status, and never opens a listening
   socket.
5. **Given** any combination of multiple startup-check failures
   present at once, **When** the server is launched, **Then**
   the launch returns the error for the first failing check in
   the documented order (clock, then file modes, then bind, then
   state directory) and never runs the remaining checks.

---

### User Story 2 - SIGHUP rotates the vault under live traffic without dropping a request (Priority: P1)

The server is running with vault A loaded into protected memory.
A request arrives, passes the middleware, enters its handler, and
begins reading a secret from the in-memory vault store. While
that read is in flight, the operator rotates a secret on the
vault host and the server receives SIGHUP. The server reloads
the vault file from the configured path, validates the new
ciphertext decrypts under the configured passphrase, and on
success atomically swaps the in-memory store pointer so that any
new request observes vault B. The in-flight request that already
captured a reference to vault A continues with vault A's contents
and completes successfully. The protected memory holding vault A
is zeroed only after a configured drain window has elapsed —
long enough that no in-flight request can still hold a live
reference to it.

**Why this priority**: This is the SIGHUP-reload half of AC-2.
The whole reason the project supports rotation at all is so that
a compromised secret can be replaced quickly, but the rotation
MUST NOT cause a denial-of-service for the very requests the
operator is trying to protect. Atomic swap with a drain window
is the contract that lets rotation be operationally cheap. If
this property breaks, every rotation becomes a small outage and
operators learn to delay rotations — the worst possible outcome
for a secrets broker.

**Independent Test**: Start the server with a vault containing
secret X with value `oldX`. Begin a slow secret-fetch request.
While the request is in flight, replace the vault file on disk
with a vault that contains secret X with value `newX`, then
send SIGHUP to the server process. Assert the in-flight request
returns `oldX`, a fresh request after the swap returns `newX`,
and the protected memory holding `oldX` is zeroed at or after
the documented drain window — not before, and not significantly
after.

**Acceptance Scenarios**:

1. **Given** a running server with vault A loaded, **When**
   SIGHUP is delivered after the vault file at the configured
   path has been replaced with vault B and the new ciphertext
   decrypts successfully, **Then** the in-memory vault pointer
   is replaced atomically with vault B and every request that
   begins after the swap observes vault B's contents.
2. **Given** a request that is in-flight at the moment of
   SIGHUP and that has already captured a reference to vault A,
   **When** the swap completes, **Then** the in-flight request
   continues with vault A's contents and finishes successfully
   without seeing partial bytes, a panic, or a use-after-destroy
   error.
3. **Given** a successful SIGHUP swap, **When** the configured
   drain window has elapsed, **Then** the protected memory
   holding the previous vault is destroyed (zeroed and released)
   exactly once. No subsequent SIGHUP, shutdown, or request can
   re-trigger that destroy.
4. **Given** a SIGHUP whose new vault file is missing, whose
   ciphertext fails to decrypt, or whose decoded contents are
   structurally invalid, **When** the reload runs, **Then** the
   active in-memory vault pointer is unchanged, the failure is
   reported as a typed reload error, and the server continues
   serving requests against the previous vault as if SIGHUP had
   never been delivered.
5. **Given** a SIGHUP that arrives while a previous SIGHUP's
   reload (including its drain window) has not yet completed,
   **When** the new SIGHUP is observed, **Then** reloads are
   serialised — the new reload does not begin until the previous
   reload's swap and drain are done; the system never destroys a
   vault that any in-flight request could still be reading.

---

### User Story 3 - A panic in a handler is captured without leaking the request body (Priority: P1)

A request reaches a handler and the handler panics — a nil
dereference, a deliberate `panic` in test code, a slice out-of-
bounds, anything. The server's recover middleware captures the
panic, writes a structured log entry that contains the panic
value and the stack trace, returns a generic error response to
the client, and keeps the server process alive for every other
in-flight and future request. The log entry MUST NEVER contain
the HTTP request body or any portion of it. The log entry MUST
contain the request ID for correlation.

**Why this priority**: A `/claim` request body is canonical-JSON
that includes a nonce, a timestamp, the requested scope and
reason, and an ECDSA signature. A `/revoke/{jti}` request body
is similarly authenticated. None of these bodies should ever
appear in a panic log on a production host — leaking them turns
operational logs into a discovery surface for an attacker who
has compromised log storage. The constitutional rule is
load-bearing: secret material and request-bound metadata MUST
NOT appear in operational logs (Constitution X). Recover-middleware
log redaction is the chassis-level enforcement of that rule.

**Independent Test**: Mount a handler that panics with a value
chosen to be searchable. Send a request whose body contains a
sentinel marker byte sequence (e.g.
`SECRET_SHOULD_NEVER_APPEAR_10`). Capture the structured log
output produced by the recover middleware. Assert the panic
value is present in the log, a stack trace is present in the
log, the request ID is present in the log, and the sentinel
marker from the request body is absent from the log output and
from the response body the client receives.

**Acceptance Scenarios**:

1. **Given** a handler that panics during request processing,
   **When** the recover middleware captures the panic, **Then**
   the server emits a structured log entry that contains the
   panic value, the captured stack trace, and the request ID,
   and returns a generic error response to the client.
2. **Given** any request whose body contains arbitrary bytes,
   **When** that request triggers a handler panic, **Then** the
   recover-middleware log entry MUST NOT contain any portion of
   the request body — neither the raw bytes, a decoded
   representation, nor a length-and-prefix summary that could
   re-derive sensitive content.
3. **Given** a captured panic, **When** the recover middleware
   has finished handling it, **Then** the server process is
   still running, the listener is still accepting connections,
   and no other in-flight request was affected.
4. **Given** a panic that occurs inside the recover middleware
   itself (a programming error), **When** the second-level
   panic happens, **Then** the server fails closed for that
   single request only and the process remains alive — the
   recover layer is not a single point of failure.

---

### User Story 4 - Every request carries a stable, server-generated request ID (Priority: P1)

Every request that reaches the server is assigned a unique
identifier the moment the chassis sees it. That identifier is
exposed to every handler that runs underneath the chassis and to
every log entry the server emits in the course of processing the
request. The identifier is generated by the server itself; the
chassis does not adopt a value supplied by the client, even if
the client sends a header that looks like a request ID, because
trusting client-supplied IDs would let a hostile or buggy client
collide IDs across machines and corrupt audit correlation.

**Why this priority**: Without a stable request ID the audit log
(`docs/SPEC.md` FR-14), the panic log (User Story 3), and the
operational diagnostics for every handler downstream become
uncorrelated noise. The chassis is the only place where every
request must pass; assigning IDs here is the only way the
property holds for the whole system. Forbidding client-supplied
IDs is a security choice — the project's threat model treats
agent-side processes as untrusted (`docs/ARCHITECTURE.md` §2),
so no chassis property may rely on a header an untrusted process
controls.

**Independent Test**: Send N requests against the server, each
with no request-ID header and each with a forged request-ID
header. For each, capture the request ID the chassis assigns
(visible to a test handler that echoes the value the chassis
made available). Assert all N IDs are unique across the run and
none of them equals the forged client-supplied value.

**Acceptance Scenarios**:

1. **Given** any incoming request, **When** the request enters
   the middleware stack, **Then** the chassis assigns a fresh
   server-generated request ID before any handler runs and
   makes that ID visible to every handler and to every log
   entry produced in service of that request.
2. **Given** a request that arrives with a header whose name
   resembles a conventional request-ID header, **When** the
   chassis processes the request, **Then** the chassis ignores
   the supplied value and assigns its own — the chassis never
   adopts a client-supplied request ID.
3. **Given** any request that produces a structured log entry
   anywhere in the server, **When** the entry is emitted,
   **Then** the entry carries the request ID associated with
   that request.

---

### User Story 5 - Requests from a non-allowlisted client IP are rejected before any handler runs (Priority: P1)

A request arrives over the Tailscale interface. The chassis
inspects the source IP, compares it against the configured
client-IP allow-list, and — if the source IP is not on the list
— rejects the request with an HTTP error response before any
handler is invoked, before any vault store is read, before any
ECDSA signature verification is attempted. The rejection is
recorded in the operational log with the request ID and the
source IP for audit correlation. The rejected request never
appears in any handler's logs because no handler ever sees it.

**Why this priority**: The Tailscale ACL grants `tag:trusted →
tag:sandbox:7743` (`docs/SPEC.md` FR-8), but the project's
defense-in-depth principle requires the server to enforce the
same boundary again at the application layer — Tailscale ACLs
might be misconfigured, an attacker might reach the server from
inside the mesh via a compromised peer, and an unallowed IP
should never be able to mount a probing attack against the
handler logic. Rejecting at the chassis layer means no handler
ever has to defend itself against unallowed source IPs.

**Independent Test**: Configure the server with an allow-list
containing a single IP A. Send a request from IP A and assert
it reaches the (test) handler. Send a request from IP B (not on
the list) and assert it is rejected with HTTP `403 Forbidden`
before any handler runs — verifiable by mounting a handler that
records every invocation and asserting it was not invoked for
the second request.

**Acceptance Scenarios**:

1. **Given** a configured client-IP allow-list and an incoming
   request whose source IP is not on the list, **When** the
   request enters the middleware stack, **Then** the chassis
   rejects it before any handler runs and emits a log entry
   carrying the request ID, the source IP, and the rejection
   category.
2. **Given** a rejected request, **When** the rejection is
   recorded, **Then** no handler-level state — no token store
   read, no vault store read, no signature verification, no
   audit-log append — is performed for that request.
3. **Given** an allow-listed source IP, **When** the request
   enters the middleware stack, **Then** the request reaches
   the handler stack normally (subject to the remaining chassis
   middleware: panic recover and request-ID assignment).

---

### User Story 6 - The server shuts down gracefully when its lifecycle context is cancelled (Priority: P2)

The server's lifecycle is bounded by a context the runtime owns
(`cmd/hush serve` in SDD-14). When that context is cancelled —
on SIGTERM, SIGINT, or any orderly shutdown signal — the server
stops accepting new connections, allows in-flight requests up
to a configured shutdown timeout to finish, then returns from
its lifecycle entry point. Any pending SIGHUP reload that has
already swapped the vault but is still inside its drain window
is allowed to complete its destroy before the server returns.
A SIGHUP that arrives during shutdown is ignored — shutdown
takes precedence and the new vault is not loaded.

**Why this priority**: Graceful shutdown is the property launchd
and systemd rely on to perform clean restarts (`docs/SPEC.md`
FR-19, boot retry behavior). A server that drops in-flight
requests on shutdown causes the supervisor (SDD-19) to trip into
`awaiting-approval` unnecessarily and pages the operator over a
non-event. The chassis MUST give the runtime a clean shutdown
hook; the runtime MUST NOT have to manually orchestrate it.

**Independent Test**: Start the server. Begin a slow in-flight
request. Cancel the lifecycle context. Assert the in-flight
request completes normally; the server stops accepting new
connections from the moment of cancellation; the lifecycle
entry point returns within the configured shutdown timeout.

**Acceptance Scenarios**:

1. **Given** a running server and a request in flight, **When**
   the lifecycle context is cancelled, **Then** the in-flight
   request completes (subject to the configured shutdown
   timeout) and the server's lifecycle entry point returns
   without an error after the request is done.
2. **Given** a running server and no requests in flight, **When**
   the lifecycle context is cancelled, **Then** the server
   stops accepting new connections immediately and the
   lifecycle entry point returns promptly.
3. **Given** SIGHUP delivered during shutdown, **When** the
   shutdown is already in progress, **Then** the SIGHUP is
   ignored — shutdown is not interrupted to load a new vault.

---

### User Story 7 - The Approver dependency is a typed interface placeholder (Priority: P2)

The chassis package defines an `Approver` interface — a single
type whose method, when invoked by the claim handler that
SDD-12 will mount on this chassis, returns an approval decision
or an error. The chassis itself supplies no concrete approver;
it accepts an `Approver` value as a dependency. SDD-11 will
later supply the Discord-backed implementation. Tests inside
this chunk supply a fake `Approver` whose decisions are
script-controlled. The interface is small enough that any future
approval surface (test, manual, alternative chat platform) can
implement it without modifying any consumer.

**Why this priority**: Defining the approval interface in the
chassis decouples the lifecycle work in this chunk from the
Discord integration in SDD-11 and lets both proceed in parallel.
It also keeps the consumer (the claim handler in SDD-12)
agnostic of the concrete approver. Without this placeholder,
SDD-11 and SDD-12 would have to be merged into one chunk, which
defeats the project's chunked-development discipline.

**Independent Test**: Construct a server with a fake `Approver`
that records its calls and returns scripted decisions. Mount a
trivial test handler that invokes the approver. Assert the
fake's call log shows exactly the parameters the chassis (or
the handler under test) passed in.

**Acceptance Scenarios**:

1. **Given** a chassis instance constructed with a non-nil
   `Approver` dependency, **When** any consumer of the chassis
   needs to seek an approval decision, **Then** the consumer
   uses the supplied `Approver` value rather than a
   package-internal default — the chassis ships no concrete
   approver of its own.
2. **Given** a future Discord-backed approver implementation
   (SDD-11), **When** that implementation is supplied as the
   `Approver` dependency, **Then** the chassis accepts it
   without code changes to the chassis itself — the contract
   is fixed at the interface layer.
3. **Given** a test that supplies a fake `Approver`, **When**
   the chassis is constructed with the fake, **Then** the
   chassis exercises every code path that depends on approval
   without contacting Discord, the network, or any other
   external system.

---

### Edge Cases

- A startup-check call whose underlying syscall fails for
  reasons unrelated to the configured value (the NTP query
  daemon is missing; the state directory cannot be `stat`-ed
  because the running user lacks permission) is treated as a
  failure of that check — the server refuses to start with the
  named error for that check rather than skipping the check
  silently.
- A SIGHUP delivered before the server has finished its first
  startup is queued or ignored — startup completes first; only
  a fully-started server processes a reload.
- A SIGHUP whose new vault file decrypts successfully but whose
  decoded contents fail higher-level validation (for example,
  the file is a vault for a different deployment) is rejected
  as a reload failure; the active vault is unchanged and the
  typed reload error explains the failure category without
  leaking any byte from the new file's contents.
- A request whose source IP appears on the allow-list but whose
  TCP socket reports a different peer address (e.g. due to a
  proxying middlebox the operator has not configured) is
  treated as not-allow-listed — the chassis trusts the
  socket-level peer address, not any client-supplied header.
- A request whose body is large (up to the project-configured
  cap of 64 KiB per `docs/ARCHITECTURE.md` §5.3) and that
  triggers a handler panic produces a recover-middleware log
  entry that does not contain any of those bytes — the
  redaction property holds at every body size, not just small
  ones.
- A panic in a goroutine spawned by a handler (rather than in
  the handler's own request goroutine) is the goroutine
  owner's responsibility to recover. The chassis's recover
  middleware only protects the request-handling goroutine the
  chassis itself owns.
- A SIGHUP that arrives at the exact instant a previous reload
  is finishing its drain window is serialised after the
  previous reload's destroy; the previous vault is destroyed
  exactly once.
- A startup-check failure during a runtime reload (for example,
  the file modes inside the state directory drift back above
  0600 between launches) is detected only at the next launch —
  startup checks are start-time enforcement, not continuous
  enforcement. This is by design and is documented rather than
  tested for.
- A SIGHUP delivered to a shutting-down server is ignored;
  shutdown takes precedence.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The server MUST execute a fixed sequence of
  startup checks before opening a listening socket. The
  sequence is, in order: (1) host-clock NTP-sync verification,
  (2) state-directory file-mode verification, (3) configured
  listen-address verification, (4) state-directory presence and
  ownership verification.
- **FR-002**: The server MUST refuse to start if any startup
  check fails. A failed startup check produces a non-zero
  process exit and a distinct, named sentinel error identifying
  which check failed.
- **FR-003**: The first failing startup check MUST short-
  circuit the remaining checks. The server MUST NOT continue
  running later checks after a failure, and MUST NOT report a
  composite error containing multiple failures — a single
  launch fails for exactly one named reason.
- **FR-004**: The clock-sync startup check MUST refuse to start
  the server when the host clock is not NTP-synchronised, when
  absolute drift exceeds 60 seconds (matching `docs/SPEC.md`
  lines 210 / 295), or when the underlying query cannot
  determine sync state with confidence within a bounded
  timeout. Any of these conditions produces the
  clock-unsynchronised sentinel.
- **FR-005**: The file-mode startup check MUST refuse to start
  the server when any file inside the configured state
  directory is at a mode more permissive than 0600 or when the
  state directory itself is at a mode more permissive than
  0700. The check produces the file-mode sentinel and
  identifies the offending path category in the error without
  disclosing the contents of any file.
- **FR-006**: The bind startup check MUST refuse to start the
  server when the configured listen address resolves to any
  interface that is not a Tailscale CGNAT interface, including
  but not limited to `0.0.0.0`, an empty host, a loopback
  address, or a non-Tailscale public address. The check
  produces the bind-not-on-tailscale sentinel.
- **FR-007**: The state-directory startup check MUST refuse to
  start the server when the configured state directory does
  not exist, is not a regular directory, or is not owned by
  the running user. The check produces the state-directory
  sentinel.
- **FR-008**: The startup-check sentinel errors MUST be
  individually identifiable by callers using sentinel-error
  comparison (the project's idiomatic-Go pattern, Constitution
  IX). A composite "startup failed" error is not a substitute.
- **FR-009**: The server MUST treat SIGHUP as a signal to
  reload the vault from the configured vault path. A SIGHUP
  reload is a server-internal operation; it MUST NOT block on
  any in-flight request and MUST NOT cancel any in-flight
  request.
- **FR-010**: A successful SIGHUP reload MUST atomically
  replace the in-memory vault store pointer such that any
  request that begins after the swap observes the new vault
  and any request that began before the swap continues to
  observe the previous vault for the duration of its handler.
- **FR-011**: After an atomic swap, the protected memory of
  the previous vault MUST be destroyed (zeroed and released)
  exactly once, and only after a configured drain window has
  elapsed since the swap. The drain window MUST be long enough
  that no in-flight request can still hold a live reference
  to the previous vault when the destroy runs. The chassis
  default drain window is 30 seconds; the value is
  configuration-overridable but operators are not expected to
  change it without a specific reason.
- **FR-012**: A SIGHUP whose reload fails — because the new
  vault file is missing, the ciphertext does not decrypt, the
  decoded contents are structurally invalid, or any other
  reload-time error — MUST leave the active in-memory vault
  pointer unchanged. The failure MUST be reported as a typed
  reload error and the server MUST continue serving requests
  against the previous vault.
- **FR-013**: A reload failure error MUST contain the failure
  category and identifying metadata (e.g. the failing path),
  but MUST NEVER contain any byte from the vault file's
  ciphertext or any byte from the decrypted plaintext.
- **FR-014**: SIGHUP reloads MUST be serialised with respect
  to one another. A SIGHUP that arrives while a previous
  reload (including its drain window) is still in flight MUST
  NOT begin until the previous reload's swap and destroy have
  completed. The server MUST NEVER destroy a vault that any
  in-flight reload-or-request operation could still be
  referencing.
- **FR-015**: A SIGHUP delivered to a shutting-down server
  MUST be ignored. Shutdown takes precedence; no new vault is
  loaded once shutdown has begun.
- **FR-016**: The chassis MUST assign a unique, server-
  generated request identifier to every incoming request
  before any handler runs. The request identifier MUST be
  visible to every handler that runs underneath the chassis
  and MUST be carried in every structured log entry produced
  in service of that request.
- **FR-017**: The chassis MUST NOT adopt a request identifier
  supplied by the client in any HTTP header. The request
  identifier is generated by the server alone; client-supplied
  values are ignored regardless of the header name.
- **FR-018**: The chassis MUST inspect every incoming
  request's source IP — taken from the underlying TCP socket's
  peer address, not from any client-supplied header — and
  MUST reject the request before any handler runs when the
  source IP is not on the configured client-IP allow-list.
  The rejection MUST be returned to the client as HTTP
  `403 Forbidden`. The rejection MUST emit a log entry carrying
  the request identifier, the source IP, and the rejection
  category, and MUST NOT trigger any token-store, vault-store,
  or signature-verification work.
- **FR-019**: The chassis MUST capture every panic that
  arises in any handler or middleware below the recover
  layer and MUST keep the server process alive for every
  other in-flight and future request. A captured panic MUST
  produce a structured log entry that contains the panic
  value, the captured stack trace, and the request
  identifier of the request whose handler panicked. The
  client whose request triggered the panic MUST receive an
  HTTP `500 Internal Server Error` response with a generic
  body that contains no panic detail, no stack trace, and no
  portion of the request body.
- **FR-020**: A recover-middleware log entry MUST NEVER
  contain the HTTP request body or any portion of it —
  neither the raw bytes, a decoded representation, nor any
  derived summary that could re-construct sensitive content.
  This property MUST hold for request bodies of every size up
  to the project-configured body cap and MUST be asserted by
  an automated sentinel-leak test.
- **FR-021**: The chassis MUST mount its registered routes
  under the opaque path prefix supplied by configuration
  (`/h/<prefix>/...`). The chassis itself does not register
  the project's handler bodies; it provides the route surface
  on which SDD-12 (claim) and SDD-13 (secret, revoke, health)
  attach their handlers.
- **FR-022**: The chassis MUST expose an `Approver` interface
  (and its associated approval-request and approval-decision
  types) that consumers of the chassis (notably the claim
  handler in SDD-12) use to seek the operator's approval
  decision. The chassis MUST accept an `Approver` value as a
  construction-time dependency and MUST NOT carry a concrete
  approver of its own. The interface MUST be small enough
  that any future approval surface (test, manual, alternative
  chat platform) can implement it without modifying any
  consumer.
- **FR-023**: The chassis MUST be constructed via a single
  dependency-injection entry point that accepts the
  configuration, the vault store pointer, the token store,
  the `Approver`, the structured logger, and any other
  collaborators it needs. Construction MUST validate the
  dependencies and return a typed error when any required
  collaborator is missing — the chassis MUST NOT panic at
  construction time on a missing dependency.
- **FR-024**: The chassis's lifecycle entry point MUST accept
  a context. Cancelling that context MUST initiate a graceful
  shutdown: the server stops accepting new connections,
  allows in-flight requests up to a configured shutdown
  timeout to complete, and then the entry point returns. The
  chassis default shutdown timeout is 30 seconds; the value
  is configuration-overridable. The chassis MUST NOT store
  the context in a struct field — context flows through the
  call stack only (Constitution IX).
- **FR-025**: A SIGHUP reload whose drain window is still
  active when shutdown begins MUST be allowed to complete its
  destroy. Shutdown waits for the previous reload's destroy
  before returning, so no protected vault memory is leaked
  across the process exit.
- **FR-026**: The middleware stack MUST execute in an order
  that satisfies three observable properties: (1) every log
  entry produced for a request carries the request
  identifier; (2) every panic that occurs in any handler or
  middleware below the recover layer is captured before the
  process can exit; (3) a request from a non-allow-listed
  source IP never reaches any handler logic, including the
  panic recover layer.
- **FR-027**: The chassis MUST NOT perform any I/O at
  construction time other than the dependency validation
  described in FR-023. All blocking I/O (socket bind, file
  reads, NTP query, signal registration) happens during the
  lifecycle entry point.
- **FR-028**: The package MUST NOT use `init()` functions
  and MUST NOT carry mutable package-level state
  (Constitution IX). The chassis is constructed and used
  through explicit values; there is no global server.

### Key Entities

- **Server**: The chassis instance. Owns the listener, the
  middleware stack, the SIGHUP reload coordinator, and the
  shutdown coordination. Constructed once, run once, shut
  down once.
- **Startup check**: A single named verification that runs
  before the server binds a socket. Each check has a fixed
  ordinal in the documented sequence, a named sentinel error
  for its failure mode, and a single-line description of
  what host condition it asserts.
- **Vault store pointer**: An indirection through which the
  chassis hands the live vault to handlers. The pointer is
  swapped atomically on SIGHUP; consumers always read it
  through the indirection so a swap is invisible to them
  beyond the change in observed contents.
- **Reload**: A SIGHUP-driven operation that loads a new
  vault from the configured path, validates it, and (on
  success) atomically swaps the vault store pointer and
  schedules destroy of the previous vault after the drain
  window. Reloads are serialised; only one reload runs at a
  time.
- **Drain window**: The configured duration the chassis
  waits between an atomic vault swap and the destroy of the
  previous vault's protected memory. Long enough that any
  in-flight request can complete; short enough that the
  previous secret material does not linger indefinitely.
  Default: 30 seconds.
- **Request identifier**: A server-generated, per-request
  identifier the chassis assigns before any handler runs.
  Carried in every log entry produced for that request.
  Never adopted from a client-supplied header.
- **Allow-list rejection**: The chassis-level error response
  produced when a request's socket-level source IP is not on
  the configured allow-list. Returned to the client as HTTP
  `403 Forbidden`. Recorded in the operational log with the
  request identifier and the source IP.
- **Recover entry**: The structured log record the chassis
  emits when it captures a handler panic. Contains the panic
  value, the stack trace, and the request identifier — never
  the request body.
- **Approver**: A single-method interface the chassis
  exposes for consumers that need to seek the operator's
  approval decision. The chassis accepts an `Approver` as a
  construction-time dependency and ships no concrete
  implementation; SDD-11 supplies the Discord-backed
  implementation, tests supply fakes.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: For each of the four named startup-check
  failure modes (clock unsynchronised, file-mode laxer than
  0600/0700, listen address not on Tailscale, state directory
  missing or unsafe), an automated test launches the server
  on a host crafted with that exact misconfiguration and
  asserts the launch returns the corresponding named sentinel
  error and exits with a non-zero status without ever opening
  a listening socket.
- **SC-002**: For a host crafted with multiple
  misconfigurations present at once, an automated test
  asserts the launch returns the error for the first failing
  check in the documented order (clock, then file modes, then
  bind, then state directory) and not the error for any
  later check — the documented order is observable.
- **SC-003**: An automated test starts the server with vault
  A, begins a slow secret-fetch request, replaces the vault
  file on disk with vault B, sends SIGHUP, and asserts: the
  in-flight request returns vault A's contents; a fresh
  request after the swap returns vault B's contents; the
  protected memory holding vault A is destroyed exactly once,
  at or after the configured drain window has elapsed since
  the swap.
- **SC-004**: An automated test sends SIGHUP with a vault
  file whose ciphertext does not decrypt and asserts the
  in-memory vault is unchanged, the failure is reported as a
  typed reload error, and a subsequent secret fetch returns
  the previous vault's contents — the failed reload did not
  corrupt or partially update the active vault.
- **SC-005**: An automated test sends two SIGHUPs in rapid
  succession (the second arriving while the first is still
  inside its drain window) and asserts the two reloads are
  serialised — the previous vault's destroy happens before
  the second reload's swap; both vaults are destroyed
  exactly once across the test.
- **SC-006**: An automated test mounts a handler that panics,
  sends a request whose body contains the sentinel marker
  `SECRET_SHOULD_NEVER_APPEAR_10`, captures the recover-
  middleware log entry produced by the panic, and asserts
  the entry contains the panic value, the stack trace, and
  the request identifier, and does not contain the sentinel
  marker — the request body is absent from the log. The
  client receives HTTP `500 Internal Server Error` and the
  response body contains no portion of the panic value,
  stack trace, or request body.
- **SC-007**: An automated test sends N requests against the
  server (with N ≥ 100), each with a forged request-ID
  header, captures the request identifier the chassis
  assigned to each (visible to a test handler that echoes
  the chassis-supplied value), and asserts: the N
  identifiers are unique; none of them equals the
  client-supplied forged value; every log entry produced in
  service of each request carries that request's identifier.
- **SC-008**: An automated test configures the server with
  an allow-list containing a single IP A and a probe handler
  that records every invocation. The test sends a request
  from IP A and a request from IP B (not on the list) and
  asserts: the request from IP A reaches the probe handler;
  the request from IP B is rejected with HTTP `403 Forbidden`
  before any handler runs and the probe handler was not
  invoked for it; the rejection log entry carries the request
  identifier and the source IP.
- **SC-009**: An automated test starts the server, begins a
  slow in-flight request, cancels the lifecycle context, and
  asserts: the in-flight request completes; the server
  stops accepting new connections from the moment of
  cancellation; the lifecycle entry point returns within
  the configured shutdown timeout.
- **SC-010**: A race-detector run of the SIGHUP reload test
  (the test from SC-003) completes without the race detector
  firing — the vault store pointer swap is observably
  race-free across concurrent readers.
- **SC-011**: A construction-time test asserts that
  constructing the chassis with a missing required
  dependency (nil `Approver`, nil vault store pointer, nil
  configuration) returns a typed error and does not panic.
- **SC-012**: An external review of the package's logs and
  error messages confirms that no vault byte (ciphertext or
  plaintext), no request body byte, and no key byte appears
  in any diagnostic surface produced by the chassis.
- **SC-013**: Coverage of the chassis package is at least
  95% as measured by the project's coverage tooling
  (`docs/AC-MATRIX.md` AC-1, AC-2, AC-8 owning-chunk row;
  Constitution VIII §"Test Priority" — High tier).

## Assumptions

- The configured vault path, the configured state
  directory, the configured drain window, the configured
  shutdown timeout, the configured client-IP allow-list,
  the configured opaque path prefix, and the configured
  listen address all live in the project's server-config
  schema (`docs/CONFIG-SCHEMA.md`, validated by SDD-06).
  The chassis consumes those values; it does not validate
  the schema itself.
- The Tailscale CGNAT-interface determination is performed
  by querying the host's interface table at startup time;
  the precise identification mechanism is plan-phase detail.
- The vault file format, the vault decryption path, and
  the protected-memory `SecureBytes` wrapper are owned by
  `internal/vault` (SDD-03) and `internal/vault/securebytes`
  (SDD-02). The chassis consumes those guarantees and does
  not re-implement them.
- The token store interface and the JWT primitives are
  owned by `internal/token` (SDD-07). The chassis takes a
  token store as a dependency and does not re-implement
  any token logic.
- The Discord-backed `Approver` implementation is owned by
  `internal/discord` (SDD-11). The chassis defines the
  `Approver` interface and accepts any conforming
  implementation as a dependency.
- The project's structured-logging contract (`log/slog`,
  type-driven redaction, Constitution X) is the chassis's
  log surface. The chassis does not introduce a logger of
  its own; it accepts the configured logger as a dependency
  and uses it.
- The clock used by the lifecycle (for the drain-window
  timer, the shutdown timeout, and any other time-based
  decision the chassis owns) is supplied as a dependency
  rather than read directly from the host clock — so that
  tests can advance time deterministically. The default
  injection at production wiring time is the host clock.
- The HTTP request body cap of 64 KiB
  (`docs/ARCHITECTURE.md` §5.3) is enforced by the chassis
  before any handler runs. Bodies larger than the cap are
  rejected with the project's body-too-large error; the
  recover-middleware redaction property holds for bodies of
  every size up to that cap.
- The chassis does not implement the project's HTTP
  handlers (`/claim`, `/secrets/{name}`, `/revoke/{jti}`,
  `/hz`). Those are owned by SDD-12 and SDD-13 and are
  mounted onto the chassis at server-construction time.
- The chassis does not implement the runtime entry point
  `cmd/hush serve`. That is owned by SDD-14 and is the
  only caller of the chassis's lifecycle entry point in
  production.
- The startup-check sequence is start-time enforcement
  only. A drift in host configuration that occurs while
  the server is already running (file modes loosen mid-
  flight, system clock drifts mid-flight) is not detected
  by the chassis at runtime and is not a defect of this
  package; the operator's host-configuration discipline is
  the boundary that catches drift, and a launchd/systemd
  restart re-runs the checks.
