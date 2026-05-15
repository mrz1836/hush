# Feature Specification: Supervisor PID File + Unix Status Socket

**Feature Branch**: `022-supervise-pidfile-socket`
**Created**: 2026-05-10
**Status**: Draft
**Input**: User description: "internal/supervise: flock-backed PID file (exclusive, NB; stale acquired cleanly after previous owner died) + Unix domain status socket (mode 0600, parent 0700, status JSON per FR-12, graceful shutdown on ctx); FS perms ARE the auth — no bearer-token, no HTTP-on-localhost"

## Overview

This chunk delivers the supervisor's two operator-visibility primitives:

- **PID file with exclusive flock** — guarantees that at most one supervisor process owns a given daemon name on a given host. A duplicate launchd / systemd / human invocation MUST fail fast with a distinct, named error.
- **Unix domain status socket** — exposes a JSON freshness/health snapshot to local clients (`hush client status`) so an agent or operator can inspect the supervisor without poking the vault server or reading process logs.

Both primitives use **filesystem permissions as their only authorization mechanism** (Constitution V): the PID file is a per-supervisor lock that any user-as-self can probe; the status socket is a private mailbox visible only to the user that owns it. The status socket MUST NOT speak HTTP, MUST NOT bind to a TCP loopback port, and MUST NOT require a bearer token. If a process can `connect(2)` the socket, it has already proved (via the kernel's path-based permission check) that it has the rights to ask.

This chunk implements behaviour required by the supervisor portions of FR-11 (`hush supervise` lifecycle — PID file + status socket bullets) and FR-12 (status socket shape + path). It is the primary behavioural carrier for AC-10 Lifecycle Scenarios **12** (agent status check before long task) and **14** (duplicate supervisor start attempt). The orchestrator chunk (SDD-23) wires these primitives into the supervisor's lifecycle; the lifecycle integration harness (SDD-25) drives the AC-10 scenarios end-to-end against them.

## Clarifications

### Session 2026-05-10

- Q: When the configured `status_socket` parent directory (or symmetrically the `pid_file` parent directory) already exists on disk with a mode broader than `0700`, how should the supervisor respond at startup? → A: Refuse to start, surface `ErrSocketPermsLoose`. Same rule applied to both parent directories. ("FS perms ARE the auth" treated as a hard precondition; consistent with FR-15 server-side `~/.hush/` permission check.)
- Q: When `Run` is invoked a second time on the same `StatusServer` instance (concurrent or after a previous `Run` returned), what should the contract be? → A: Single-shot. Any second call returns a new named sentinel `ErrAlreadyRunning`; the caller MUST instantiate a fresh `StatusServer` to bind again. (Mirrors the `sync.Once`-backed `Run` precedent in SDD-21's Refresher; aligns with User Story 4 acceptance scenario 2's "fresh server" rebinding pattern.)
- Q: When the lifecycle ctx is cancelled while one or more status connections are mid-handler (blocked on Read/Write), what's the spec-level shutdown contract? → A: Prompt-regardless. `Run` MUST return within a small fixed sub-second bound on ctx cancel; in-flight connections are force-closed. (Sub-second shutdown protects supervisor restart agility under operator stop, lifecycle reset, and refresh-failure recovery. Local clients are transient/idempotent, so force-close is benign.)

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Duplicate supervisor start is refused (Priority: P1)

A second invocation of `hush supervise` for the same daemon name (e.g. a stale launchd job double-firing, or an operator manually starting an already-running supervisor) MUST fail fast with a distinct, named error and MUST NOT proceed to claim secrets, fork a child, or open the status socket. Only one supervisor can own a daemon name on a host.

**Why this priority**: Lifecycle Scenario 14. Without this, a duplicate supervisor would race with the legitimate one for the same JWT, double-fork the child, and corrupt audit ordering. The single-owner guarantee is the foundation that every other supervisor invariant rests on.

**Independent Test**: Acquire the PID file in one process; in a second process call the same acquire path against the same PID-file location; the second call returns the named error within milliseconds, never blocks, and never overwrites the first process's PID-file contents. Verifiable as a unit test against the PID-file primitive without any vault server, child process, or Discord transport.

**Acceptance Scenarios**:

1. **Given** a supervisor that has already acquired its PID file, **When** a second invocation with the same PID-file path attempts acquisition, **Then** the second call returns a distinct, named error (`ErrPidLocked`) immediately, without blocking and without modifying the file.
2. **Given** a supervisor that has already acquired its PID file, **When** a second invocation is refused, **Then** the first supervisor's lock, PID-file contents, and ownership remain untouched.
3. **Given** a supervisor refused at acquisition, **When** the failure is observed by the caller, **Then** the failure surfaces as the named sentinel error (so the orchestrator can log a clear "another supervisor already owns this name" message and exit with a recognisable status, distinct from generic I/O errors).

---

### User Story 2 - Crashed supervisor recovers cleanly on next start (Priority: P1)

The previous supervisor died without releasing its lock (kill -9, OOM, kernel panic, machine reboot). The PID file remains on disk with a stale PID inside it. When the OS-level supervisor (launchd / systemd) restarts hush, the new process MUST be able to acquire the PID file cleanly — without operator intervention and without manual file deletion. The OS releases the flock automatically on process death; the new supervisor MUST rely on that semantics rather than parsing the stale PID, sending probe signals, or pre-deleting the file based on its contents.

**Why this priority**: Without this, every supervisor crash becomes a manual-intervention incident. A daemon that won't restart after a kernel panic violates Constitution Principle V (loud failure, but only loud — never require a human to clear a dead-flag file before restart).

**Independent Test**: Process A acquires the PID file and exits abnormally (without calling release). Process B, started subsequently, acquires the same PID file successfully and writes its own PID into the file. Verifiable as a unit test using a child-process pattern that exits without cleanup.

**Acceptance Scenarios**:

1. **Given** a PID file on disk whose previous owner died without releasing the flock, **When** a new supervisor invocation attempts acquisition, **Then** acquisition succeeds (the OS has released the flock) and the new supervisor's PID is written into the file.
2. **Given** the same precondition, **When** the new supervisor acquires the PID file, **Then** no operator action (manual file deletion, manual lock break, manual PID inspection) is required.
3. **Given** a PID file whose previous owner is still alive and still holds the flock, **When** a new supervisor invocation attempts acquisition, **Then** the result is User Story 1 (refusal with `ErrPidLocked`), not a stale-acquisition. The presence of stale text in the PID file MUST NOT be the criterion that distinguishes "stale" from "live" — only the OS-held flock is authoritative.

---

### User Story 3 - Agent inspects daemon status via the local socket (Priority: P1)

A downstream agent is about to begin a long-running task and wants to confirm that the daemon's secrets are healthy before starting. The agent connects to the supervisor's local status socket and reads back a structured JSON snapshot — supervisor name, session expiry, next refresh window, healthy/stale scope lists, last auth failure, child PID and uptime, Discord-connected flag, and current state. The agent decides on the basis of that snapshot whether to proceed or refuse.

**Why this priority**: Lifecycle Scenario 12. Without this, agents have no machine-readable way to ask "are my creds OK?", and auth drift becomes guesswork. This is the only operator-visibility surface for a running supervisor (Constitution Principle V — staleness is visible).

**Independent Test**: Run a stub status server backed by a fake supervisor state. Connect from a second process with the same UID, request status, observe the JSON response includes every required field with correct types. Verifiable as a unit test.

**Acceptance Scenarios**:

1. **Given** a running supervisor with an open status socket, **When** a local client owned by the same UID connects to the socket and requests status, **Then** the supervisor responds with a JSON object that contains every field documented in `docs/SPEC.md` FR-12 (`supervisor`, `session_expires_at`, `refresh_window_next`, `scope_healthy`, `scope_stale`, `last_auth_failure`, `child_pid`, `child_uptime`, `discord_connected`, `state`), with field types matching the documented shape.
2. **Given** a status response that includes any reference to the supervisor's session token or any cached secret value, **When** the JSON is rendered, **Then** the response MUST NOT contain any plaintext secret material — token-bearing fields MUST render as the constant string `[redacted]` or be omitted entirely. Audit-log redaction (Constitution Principle X) applies on this surface verbatim.
3. **Given** the status socket is open, **When** any process not entitled by filesystem permissions attempts to read or connect, **Then** the operating system denies the connection at `connect(2)` time — no application-layer authentication runs, and no bearer-token check is consulted.

---

### User Story 4 - Status server stops cleanly on lifecycle shutdown (Priority: P2)

When the supervisor's lifecycle context is cancelled (operator stop, parent context done, lifecycle reset), the status server MUST stop accepting new connections, close its listener, and exit its accept loop without leaking goroutines, leaving an orphan socket inode, or refusing to release the path on the next start.

**Why this priority**: Without graceful shutdown, every supervisor restart leaves behind socket files that the next `Run` would have to manually cope with. Constitution Principle IX (goroutine discipline) requires every spawned goroutine to have a documented termination path.

**Independent Test**: Start the status server bound to a temp path; cancel the context; observe the listener closes within a short bound, the accept goroutine exits, and a subsequent `Run` against the same path succeeds. Verifiable as a unit test using context cancellation and a goroutine-leak assertion.

**Acceptance Scenarios**:

1. **Given** a running status server, **When** its supervisory context is cancelled, **Then** the server's listener is closed, its accept loop exits, every in-flight per-connection handler is unblocked via force-close of its connection, and the `Run` call returns within a small fixed sub-second bound without error — independent of whether any client was mid-Read or mid-Write at the moment of cancellation.
2. **Given** a server that has just shut down, **When** a fresh server is started against the same socket path, **Then** the new `Run` call binds successfully — any leftover socket file from the previous run is cleaned up before binding so a stale inode does not block the rebind.
3. **Given** any pattern of start/stop cycles, **When** goroutine count is sampled before and after a stop, **Then** no goroutine spawned by the status server remains running.

---

### User Story 5 - Filesystem permissions are the only authorization (Priority: P2)

The status socket is private to its owning user. The chunk MUST NOT introduce any application-layer authentication: no bearer token, no HMAC handshake, no shared-secret cookie. The socket MUST NOT bind to a TCP loopback port. The auth model is exactly: "if you can `connect(2)` to the path, you are authorized". Achieving that property requires the socket file mode to be `0600` (owner read/write only) and the parent directory mode to be `0700` (owner enter/list/write only, created with that mode if missing).

**Why this priority**: Constitution V: "filesystem perms as auth". Adding any other auth layer (bearer, HMAC, etc.) to a localhost-only socket actively *weakens* the model — every additional auth surface is a new place a bug can leak privilege. The mode invariants are the entire authorization contract.

**Independent Test**: After starting a status server, stat the socket file and assert mode `0600`; stat the parent directory (created by the start path) and assert mode `0700`. Inspect the codebase to confirm no `net/http` server, no `127.0.0.1:` bind, no bearer-token check exists on this path. Verifiable as a unit test plus a static / integration assertion.

**Acceptance Scenarios**:

1. **Given** a status server has just started against a configured socket path, **When** the socket file's mode is inspected, **Then** the mode is exactly `0600`.
2. **Given** the configured socket path's parent directory does not exist at start, **When** the status server initialises, **Then** the parent directory is created with mode `0700`.
3. **Given** any client of any kind attempts to talk to the supervisor over the network (TCP loopback or otherwise), **When** the supervisor is inspected, **Then** no such network listener exists — the only listener spawned by this chunk is on a Unix domain path.
4. **Given** the status protocol implementation, **When** any incoming connection is read, **Then** the supervisor MUST NOT consult any bearer token, HMAC, signed cookie, or other application-layer credential to decide whether to respond.

---

### Edge Cases

- **Stale PID-file text but live owner**: The PID-file content (the textual PID written by the previous owner) is advisory only. Whether to refuse or acquire MUST be decided exclusively by the OS-held flock — never by parsing the PID and probing the process. A live owner whose recorded PID happens to match a since-recycled OS PID MUST still be detected as "locked" via flock.
- **Stale Unix socket inode from a previous run**: Binding fails on `EADDRINUSE` if a stale socket file lingers. Start MUST clean up any pre-existing inode at the configured socket path before binding (otherwise every supervisor restart after a crash would fail until manual intervention).
- **Parent directory exists with mode laxer than 0700**: If the configured `status_socket` parent directory already exists with mode broader than `0700` (e.g. `0755`), the supervisor MUST refuse to start and surface the named sentinel `ErrSocketPermsLoose`. The supervisor MUST NOT silently `chmod` the directory and MUST NOT proceed with the looser mode in place. This matches the server-side `~/.hush/` permission check (FR-15) and treats "FS perms ARE the auth" as a hard precondition rather than something the supervisor will repair on the operator's behalf.
- **PID-file path's parent directory**: Symmetric to the socket's parent directory. The PID-file path is configured separately from the socket path (CONFIG-SCHEMA `pid_file` vs `status_socket`), but in practice both live under the same per-daemon cache directory. The same parent-directory creation rule (create at `0700` if missing) applies; the same refusal rule applies to the laxer-mode-existing case (refuse with `ErrSocketPermsLoose`).
- **Status request issued while supervisor state is mid-transition**: A concurrent `Snapshot()` taken while the state machine is mid-write MUST return a self-consistent view. The state machine (SDD-19) guarantees this via its defensive snapshot; the status server MUST take the snapshot once per request and serialise from that snapshot, never re-reading individual fields from the live store mid-response.
- **`session_expires_at` is in the past at status read time**: The supervisor is in `awaiting-approval` (or about to enter it). The status response MUST reflect the actual fields at snapshot time without hiding the expired timestamp; consumers (the agent in Scenario 12) decide based on `state` and `scope_stale`, not by re-doing the math.
- **`child_pid` and `child_uptime` while no child is running**: The fields MUST render in a way that an agent can distinguish "no child" from "child running with uptime 0s" (e.g. null `child_pid`, `0s` uptime, or omission). The exact representation follows whatever shape `docs/SPEC.md` FR-12 already encodes; this chunk does not redefine the wire contract.
- **`Run` invoked twice on the same `StatusServer`**: `Run` is single-shot. Any second invocation on the same `StatusServer` instance — concurrent with the first, or sequential after the first returned — MUST return the named sentinel `ErrAlreadyRunning` without attempting to bind. To start the status server again (e.g. after a lifecycle stop), the caller MUST construct a fresh `StatusServer` instance.
- **Process killed mid-`Run` (no graceful shutdown)**: The next start handles this exactly via the "stale socket inode" cleanup above. No on-disk state survives the crash beyond the inode.
- **Different UID attempts to connect**: The kernel returns `EACCES` at `connect(2)` because the socket file is `0600`. The application never sees the connection attempt and MUST NOT log secret material in any error path that surfaces such failures (it should not see them at all).

## Requirements *(mandatory)*

### Functional Requirements

#### PID file (split-brain guard)

- **FR-022-1**: The supervisor MUST acquire an exclusive, non-blocking advisory lock (`flock`) on a configured PID-file path at startup. The lock MUST be exclusive (no shared / read locks) and non-blocking (the call MUST return immediately rather than waiting for the lock).
- **FR-022-2**: When the lock cannot be acquired because another live process already holds it, the supervisor MUST fail fast with a distinct, named sentinel error (`ErrPidLocked`) so callers can distinguish "another supervisor is already running" from generic I/O failures and from misconfiguration.
- **FR-022-3**: When the previous holder of the lock died without releasing it (the PID file exists on disk but no live process holds the flock), the next supervisor MUST acquire the lock cleanly, without operator intervention and without parsing or probing the stale PID. The criterion for "stale vs live" MUST be the OS-held flock, never the textual PID.
- **FR-022-4**: On successful acquisition, the supervisor MUST write its own PID into the file as text (replacing any prior contents). The PID-file write MUST happen after lock acquisition so a refused acquirer cannot corrupt the live owner's PID record.
- **FR-022-5**: The supervisor MUST expose an explicit release primitive that releases the flock and removes the PID file (best-effort removal — losing the race to another startup is acceptable). Release MUST be safe to call exactly once and MUST surface any unexpected I/O error.
- **FR-022-6**: The PID file MUST be created with mode `0600` (owner read/write only). A laxer mode MUST NOT be the result of normal operation.
- **FR-022-7**: The PID-file path MUST be the value supplied by the supervisor's parsed configuration (`pid_file`); this chunk MUST NOT hard-code a path or override the operator's configuration.

#### Status socket (operator visibility)

- **FR-022-8**: The supervisor MUST listen on a Unix domain socket at the configured path (`status_socket`). The socket MUST NOT be a TCP loopback, MUST NOT speak HTTP over TCP, and MUST NOT speak HTTP over the Unix socket either — the wire protocol is a simple request/response over the Unix socket itself.
- **FR-022-9**: The socket file mode MUST be exactly `0600` after the listener is open. The mode MUST be enforced by the supervisor before the first connection is accepted; it MUST NOT rely on operator umask or filesystem defaults.
- **FR-022-10**: When the configured socket path's parent directory does not exist, the supervisor MUST create it with mode `0700` (owner-only enter/list/write). When the parent directory already exists with a mode broader than `0700`, the supervisor MUST refuse to start and surface the named sentinel error `ErrSocketPermsLoose`; it MUST NOT silently `chmod` the directory and MUST NOT proceed with the looser mode in place. The same rule applies to the `pid_file` parent directory.
- **FR-022-11**: When a stale socket file exists at the configured path from a previous run, the supervisor MUST remove it before binding so the new listener can claim the path. Removal of a non-stale (currently-bound) inode is out of scope — that is the duplicate-supervisor case, which is already prevented by the PID-file flock (FR-022-2).
- **FR-022-12**: On every accepted connection, the supervisor MUST respond with a JSON document whose field set, names, and types match `docs/SPEC.md` FR-12 exactly: `supervisor`, `session_expires_at`, `refresh_window_next`, `scope_healthy`, `scope_stale`, `last_auth_failure`, `child_pid`, `child_uptime`, `discord_connected`, `state`. No additional fields are added by this chunk; no fields are dropped.
- **FR-022-13**: The status response MUST NEVER include the supervisor's session token or any cached secret value as plaintext. Token-bearing fields MUST render as the constant string `[redacted]` (or be omitted entirely). Audit and operational logs reached from this code path MUST follow the same rule.
- **FR-022-14**: The status server MUST stop cleanly when its lifecycle context is cancelled: the listener MUST be closed, the accept loop MUST exit, every spawned goroutine MUST terminate, and `Run` MUST return without error within a small fixed sub-second bound. In-flight per-connection handlers blocked on Read or Write MUST be unblocked via force-close of their connection at ctx-cancel time — `Run`'s return time MUST NOT depend on a remote client finishing its request or cooperating with shutdown.
- **FR-022-14a**: `Run` MUST be single-shot per `StatusServer` instance. Any second invocation on the same instance — whether concurrent with the first or sequential after the first returned — MUST return the named sentinel `ErrAlreadyRunning` without attempting to bind a listener. Re-starting the status server after a lifecycle stop requires constructing a new `StatusServer` instance.
- **FR-022-15**: The status server MUST NOT consult any bearer token, HMAC, signed cookie, or other application-layer credential when deciding whether to respond to a connection. The kernel's path-based permission check at `connect(2)` time is the only authorization mechanism.
- **FR-022-16**: The status response MUST be derivable from a single defensive snapshot of the supervisor state taken at request time. The server MUST NOT re-read individual fields from the live state store mid-response (so a concurrent state transition cannot produce a torn / inconsistent JSON document).

#### Cross-cutting

- **FR-022-17**: All goroutines spawned by this chunk (notably the status accept loop and any per-connection handler) MUST have a documented owner (`Run`'s caller), a context-driven cancellation path, and a documented termination condition. No goroutine spawned by this chunk may persist past `Run`'s return.
- **FR-022-18**: All exported sentinel errors (`ErrPidLocked`, `ErrSocketPermsLoose`, `ErrAlreadyRunning`) MUST follow Constitution IX: declared as exported package-level `var Err… = errors.New(…)`, comparable via `errors.Is`, and wrapped with `%w` when surfaced through outer call frames.
- **FR-022-19**: This chunk MUST NOT add any new direct dependency outside the existing supervisor / standard-library surface — operating-system interactions (flock, Unix socket bind, chmod, mkdir) come from the supervisor's already-allowed dependency set.

### Key Entities

- **PidFile**: A handle that represents an acquired exclusive lock on a configured filesystem path plus the file descriptor backing it. Has exactly one observable state machine: *unacquired → acquired (lock held + PID written) → released (lock dropped + file removed)*. The flock semantics are the source of truth; the textual PID is advisory metadata for human operators.
- **StatusServer**: A long-running listener bound to a Unix domain socket path that, for each accepted connection, takes a defensive snapshot of the supervisor state, renders the FR-12 JSON document, and writes it. Owns the listener lifecycle and the accept goroutine. Stops on context cancellation.
- **StatusResponse**: The JSON document defined by `docs/SPEC.md` FR-12. This chunk does not redefine the wire shape; it consumes the existing contract and asserts conformance via test.
- **Acquisition outcome**: Either `acquired` (exclusive lock now held), or `ErrPidLocked` (another live process holds the lock — User Story 1 / Lifecycle Scenario 14), or any other I/O error (path unreachable, parent dir missing, etc.) which MUST be distinguishable from `ErrPidLocked`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-022-1**: AC-10 Lifecycle Scenarios 12 (agent status check) and 14 (duplicate supervisor refused) each have a passing automated test in this chunk.
- **SC-022-2**: A second supervisor invocation against the same PID-file path returns `ErrPidLocked` within a bounded short time (no blocking) — the contention-detection latency is observable in test as well below the supervisor's normal startup time.
- **SC-022-3**: A test that simulates a previous-owner death (a child process that acquires the PID file then exits without releasing) shows the next acquisition succeeds without operator intervention.
- **SC-022-4**: After the status server starts, the socket file's filesystem mode is `0600` (verifiable by `stat`); the parent directory's mode is `0700` when the directory was created by the start path.
- **SC-022-5**: A status request against the running server returns a JSON document containing every field documented in `docs/SPEC.md` FR-12, with field names and types matching that contract, asserted field by field in a unit test.
- **SC-022-6**: A status response generated under a state where the supervisor holds a session token includes no plaintext token bytes — the token-bearing path renders `[redacted]` (or is omitted) on every code path that could surface it.
- **SC-022-7**: After context cancellation, the status server returns from `Run` and a subsequent `Run` against the same socket path succeeds — observable via a stop/start unit test that asserts no listener leaks and no stale inode blocks rebind.
- **SC-022-8**: Across any number of start/stop cycles in a single test process, no goroutine spawned by the status server remains running after `Run` returns, observable via a goroutine-count assertion combined with `-race`.
- **SC-022-9**: No code path in this chunk binds a TCP loopback listener, instantiates an `http.Server`, or evaluates a bearer token / HMAC / signed cookie to authorize a status request. Verifiable by static inspection plus a test that grep-asserts the absence of `net.Listen("tcp", ...)` and `http.Server{}` in the chunk's files.
- **SC-022-10**: Test coverage for the PID-file and status-socket files in `internal/supervise` is at least 95%, with `-race` clean.

## Assumptions

- The supervisor configuration package (SDD-18, `internal/supervise/config`) already supplies parsed, validated values for `pid_file` and `status_socket`, including expansion of `~` in the configured paths and platform-correct defaults (`~/Library/Caches/hush/...` on macOS; `$XDG_RUNTIME_DIR/...` on Linux). This chunk consumes those values; it does not redefine the schema or the path-resolution rules.
- The supervisor state machine (SDD-19, `internal/supervise/state.go`) already exposes a defensive `Snapshot()` method that returns a self-consistent view of the supervisor's current fields (state, expiries, child pid, scope health). This chunk consumes that snapshot directly; it does not redefine the state model.
- The `*SecureBytes`-rooted redaction behaviour (Constitution Principle X) — `LogValue() slog.Value` returning `slog.StringValue("[redacted]")` — is already implemented on every secret-bearing type in the supervisor state. The status server's redaction guarantee (FR-022-13) is satisfied by routing token-bearing fields through that redaction path; this chunk does not invent new redaction logic.
- `docs/SPEC.md` FR-12 is the authoritative source of the status JSON shape. This chunk consumes that contract; conflicts between FR-12 and any other doc MUST be resolved in favour of FR-12.
- The PID-file path and the status-socket path live under the same per-daemon cache directory in normal configuration (`~/Library/Caches/hush/` on macOS; `$XDG_RUNTIME_DIR` on Linux). The chunk does not enforce that constraint — the operator's config does — but the per-daemon-directory convention is what makes the parent-directory creation rule a one-line operation in practice.
- Multi-user vault hosts are out of scope for v0.1.0 (Constitution Principle VI — single trusted host). Path-based filesystem authorization is therefore sufficient; cross-user attacker scenarios on the vault host are not in scope for this chunk.
- The orchestrator (SDD-23) is responsible for invoking `AcquirePidFile` at supervisor start and `Release` at supervisor stop, and for invoking `StatusServer.Run` under the supervisor's lifecycle context. This chunk provides the primitives; it does not wire them into the supervisor's main loop. Likewise, this chunk does NOT consume `hush client status` — the client-side reader of the socket lives in SDD-23 and is asserted end-to-end in SDD-25's Lifecycle Scenario 12 integration test.
- The lifecycle integration harness (SDD-25) is responsible for the cross-chunk acceptance assertion that AC-10 Scenarios 12 and 14 pass end-to-end against a real binary. This chunk's success criteria scope to unit-level proof of the primitives.
