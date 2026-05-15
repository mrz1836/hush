# Feature Specification: hush supervise + hush client status + hush client refresh

**Feature Branch**: `023-cli-supervise-and-client`
**Created**: 2026-05-12
**Status**: Draft
**Input**: User description: "hush supervise (foreground supervisor — wires config + state + child + refill/refresh/grace + pidfile + socket; --dry-run prints canonical /claim payload; --grace-window override; --no-cache strict mode); hush client status (TTY-aware status JSON or human summary); hush client refresh (signals running supervisor via status socket)"

## Overview

This chunk delivers the three operator-facing CLI subcommands that turn the
already-implemented supervisor building blocks into a usable product:

- `hush supervise <config-path>` — the foreground supervisor entry point that
  the OS service manager (launchd / systemd) wraps. It orchestrates the
  building blocks delivered in chunks SDD-18 through SDD-22 (config loader,
  state machine, child process manager, refill/refresh/grace logic, PID file,
  status socket) into one running daemon. This chunk adds **no new business
  logic**; it is pure orchestration glue.
- `hush client status` — the operator's "is this daemon healthy?" query.
  Connects to a running supervisor's status socket, retrieves the current
  status document, and prints either a human-friendly summary (when stdout
  is a terminal) or the underlying JSON (when stdout is piped to another
  program).
- `hush client refresh` — the operator's "rotate now" trigger. Sends a
  refresh command to a running supervisor's status socket, prompting the
  supervisor to perform an immediate refresh-window-style refill of its
  scoped secrets.

Together these three subcommands close the **AC-10 supervisor lifecycle**
acceptance criterion (in particular Scenarios 12 "agent status check before
long task" and 14 "duplicate supervisor start attempt", and they enable
Scenario 13 "secret rotated mid-session" by giving the operator the refresh
trigger).

## Clarifications

### Session 2026-05-12

- Q: When `--socket` is omitted, how does the client locate the supervisor's socket? → A: `--supervisor NAME` flag (or single-host auto-detect when exactly one socket exists); socket path derived the same way the supervisor derives its own. `--socket <path>` always wins when supplied.
- Q: When does the supervisor's acknowledgement to `hush client refresh` arrive? → A: After the refill completes (re-fetch + re-validate + child restart). Exit 0 = new secret in flight; non-zero = supervisor-reported refill failure. The client must tolerate refill-window-bounded waits.
- Q: When `hush client refresh` arrives while a prior refill is in flight, what does the supervisor return? → A: Coalesce — the second refresh returns the same terminal ack/error as the in-flight refill. No "busy" exit class is exposed to the client; exit 0 on shared success, non-zero on shared failure.
- Q: What are the upper-bound timeouts for `hush client status` and `hush client refresh`? → A: `status` = 2 s total (connect + read). `refresh` = 90 s total (covers Discord-prompted refill on the refresh-window path). Timeouts continue to map to connection-failure exits.
- Q: Should `hush client status` expose an explicit output-format override? → A: Yes — add a `--json` boolean flag to `hush client status` that forces JSON regardless of TTY detection. No human override flag (TTY auto-detection remains the default). `hush client refresh` does not gain a format flag.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator runs a daemon under supervisor (Priority: P1)

The operator has authored a supervisor configuration file for one of their
long-running daemons (for example, an AI-agent runtime). They want to start
that daemon under hush's supervisor lifecycle so a single Discord approval
covers crashes, updates, and restarts within the session TTL, and they want
the OS service manager (launchd on macOS, systemd on Linux) to keep the
supervisor itself running.

The operator's expectation: invoke `hush supervise <config-path>` from a
service-manager script in the foreground. The command stays attached to
the terminal (or service-manager-managed stdout/stderr), runs the configured
child, performs silent refills on crashes within session TTL, performs the
daily refresh prompt at the configured window, and only exits when the
operator (or the service manager) signals it to stop. On exit, it cleans up
its PID file and status socket.

**Why this priority**: This is the canonical "daemon under hush" workflow.
Without it, no daemon can run under hush at all, and AC-10 (the v0.1.0
supervisor-lifecycle exit gate) cannot pass.

**Independent Test**: An operator can run `hush supervise <path>` against
a fully-populated supervisor config plus a Discord stub, confirm the
expected Discord approval prompt fires, observe that an approved request
yields a fork of the configured child with the configured secrets injected,
and verify that the supervisor terminates cleanly on SIGTERM with no leaked
PID file or status socket.

**Acceptance Scenarios**:

1. **Given** a valid supervisor configuration at `~/.hush/supervisors/<name>.toml`
   and an approving Discord stub, **When** the operator runs
   `hush supervise ~/.hush/supervisors/<name>.toml`, **Then** the supervisor
   acquires its PID file, binds its status socket, claims a supervisor JWT
   via the configured vault server, fetches all scoped secrets, validates
   them, and starts the configured child with those secrets injected as
   environment variables.
2. **Given** a running supervisor, **When** the child exits with a non-78
   exit code within the session TTL, **Then** the supervisor performs a
   silent refill (no Discord traffic) and restarts the child within seconds.
3. **Given** a running supervisor, **When** the operator sends `SIGTERM`
   to the supervisor process, **Then** the supervisor forwards the signal
   to the child, waits for the child to exit, releases its PID file, removes
   its status socket, and exits 0.
4. **Given** a second `hush supervise` invocation targeting the same
   configuration on the same host while the first is still running,
   **When** the second invocation starts, **Then** it detects the existing
   PID file lock, exits with a non-zero exit code, and prints a clear
   error message identifying the duplicate-supervisor condition (AC-10
   Scenario 14).
5. **Given** an invalid or missing configuration file path, **When** the
   operator runs `hush supervise <bad-path>`, **Then** the command exits
   with the input-error exit code and prints a clear error pointing at the
   offending file or field.

---

### User Story 2 - Operator previews a configuration without burning a Discord prompt (Priority: P2)

Before binding a supervisor to launchd / systemd, the operator wants to
verify that their configuration produces the request payload they expect
(correct scope list, correct supervisor name, correct requested TTL, etc.)
**without** firing a Discord approval prompt at themselves.

The operator's expectation: invoke `hush supervise <config-path> --dry-run`
and see the canonical request payload that would have been sent to the
vault server's claim endpoint, printed to stdout in its canonical-JSON
form, with the command exiting 0 and **no Discord call made**.

**Why this priority**: This is a meaningful safety / authoring affordance —
operators iterating on configuration files would otherwise need to send
themselves a real Discord prompt and tap "Deny" to test their wiring. It
is, however, not on the daemon-startup critical path; P2 is correct.

**Independent Test**: An operator can run `hush supervise <path> --dry-run`
against any valid config, pipe stdout into a JSON parser, confirm the
payload's `name`, `scope`, `requested_ttl`, and `session_type=supervisor`
fields match the config, observe that no Discord request was issued, and
verify the command exited 0.

**Acceptance Scenarios**:

1. **Given** a valid supervisor configuration, **When** the operator runs
   `hush supervise <path> --dry-run`, **Then** stdout contains the canonical
   request payload (canonical JSON form: keys in alphabetical order, compact
   spacing) that would have been sent to the vault server, no Discord
   request is issued, no PID file is acquired, no status socket is bound,
   no child process is started, and the command exits 0.
2. **Given** any invalid supervisor configuration, **When** the operator
   runs `hush supervise <path> --dry-run`, **Then** the command exits with
   the input-error exit code (same as a non-dry-run startup), prints a
   clear error, and does not print a partial payload.

---

### User Story 3 - Operator overrides the configured grace window for one run (Priority: P3)

The operator wants to start a supervisor with a tighter (or strict) grace
behaviour for a single run, without editing the configuration file. Two
related affordances:

- `--grace-window <duration>` overrides the config's `cache_grace_ttl` for
  this run only.
- `--no-cache` forces `cache_secrets_for_restart=false` regardless of what
  the configuration file says, dropping the supervisor into strict mode.

**Why this priority**: Convenient for the operator who wants to run a single
debug session in strict mode without rewriting a TOML file, or to test what
a different grace window feels like before committing it. It is not on the
"daemon runs at all" critical path; P3 is correct.

**Independent Test**: An operator can run `hush supervise <path> --no-cache`
against a config that has `cache_secrets_for_restart=true`, induce a crash
outside the JWT TTL window, and confirm the supervisor transitions to
`awaiting-approval` (strict path) rather than using a cached secret.
Similarly, running with `--grace-window 30m` against a config declaring
`cache_grace_ttl=60m` yields the 30-minute cache lifetime.

**Acceptance Scenarios**:

1. **Given** a configuration with `cache_secrets_for_restart=true`, **When**
   the operator runs `hush supervise <path> --no-cache`, **Then** the
   supervisor behaves as though the configuration had set
   `cache_secrets_for_restart=false` for this run.
2. **Given** a configuration with `cache_grace_ttl=60m`, **When** the
   operator runs `hush supervise <path> --grace-window 30m`, **Then** the
   supervisor uses a 30-minute grace-cache lifetime for this run.
3. **Given** both flags supplied together, **When** the operator runs
   `hush supervise <path> --grace-window 30m --no-cache`, **Then**
   `--no-cache` wins and the supervisor runs in strict mode (the value
   passed to `--grace-window` is ignored without error).
4. **Given** an invalid duration string passed to `--grace-window`,
   **When** the supervisor starts, **Then** the command exits with the
   input-error exit code and prints a clear error identifying the offending
   flag value.
5. **Given** a `--grace-window` value that exceeds the configured maximum
   (4h cap from `docs/CONFIG-SCHEMA.md`), **When** the supervisor starts,
   **Then** the command exits with the input-error exit code and prints
   a clear error.

---

### User Story 4 - Operator (or downstream agent) checks daemon freshness before running a task (Priority: P2)

A downstream tool — typically a shell script or another agent — wants to
know whether the daemon's credentials are fresh before kicking off a long
task. This closes the "the agent has no way to know its credentials are bad"
gap that motivated `hush supervise` in the first place (G5 from
`docs/SPEC.md` §2). The operator also occasionally wants to check daemon
status by hand from a terminal.

**Why this priority**: This is the agent-visible freshness API, and it
unlocks AC-10 Scenario 12. P2 because the daemon can still function without
it; the gate is a defensive layer that prevents a known failure mode
(running long tasks on stale creds).

**Independent Test**: With a running supervisor in known state, the
operator can run `hush client status [--socket <path>]` from a terminal
and see a readable human summary; the same command piped through `jq`
yields a parseable JSON document conforming to the status-document shape
defined in `docs/CONFIG-SCHEMA.md`.

**Acceptance Scenarios**:

1. **Given** a running supervisor reachable at its status socket,
   **When** the operator runs `hush client status --socket <path>` from
   an interactive terminal, **Then** stdout contains a human-readable
   summary that includes the supervisor name, current state, child PID
   (or "no child" if not running), session expiry, next refresh window,
   and the lists of healthy and stale scopes.
2. **Given** the same running supervisor, **When** the operator runs
   `hush client status --socket <path>` with stdout redirected to a pipe
   or file, **Then** stdout contains the supervisor's full status document
   as JSON conforming to the shape defined in `docs/CONFIG-SCHEMA.md`
   §"Client status output schema", and the command exits 0.
3. **Given** no supervisor is running at the requested socket path,
   **When** the operator runs `hush client status --socket <path>`,
   **Then** the command exits with a non-zero exit code, prints a clear
   error explaining that the socket is unreachable or absent, and (in
   non-TTY mode) writes nothing to stdout that could be confused for a
   valid status document.
4. **Given** a downstream script that gates its work on freshness,
   **When** it runs `hush client status --socket <path>` and inspects the
   resulting JSON's `scope_stale` array, **Then** an empty array signals
   "safe to proceed" and a non-empty array signals "refuse to run on
   stale credentials" (AC-10 Scenario 12).

---

### User Story 5 - Operator triggers an immediate refresh after rotating a secret (Priority: P2)

After rotating a secret on the vault host (`hush secret rotate NAME`), the
operator wants the running supervisor to immediately re-fetch its scoped
secrets and gracefully restart the child with the new value — without
waiting for the configured refresh window and without restarting the
supervisor itself (which would burn a Discord approval).

**Why this priority**: This is the operator-facing recovery affordance
that makes mid-session rotation (AC-10 Scenario 13) work end-to-end. P2
because without it, mid-session rotation still eventually succeeds at the
next refresh window — but the operator-friendly path is to refresh on
demand.

**Independent Test**: With a running supervisor, the operator can run
`hush client refresh --socket <path>` and observe (in audit log / Discord
warning tier / status socket) that a refill was triggered, completed
successfully, and the child was restarted with the freshly-fetched
secrets.

**Acceptance Scenarios**:

1. **Given** a running supervisor reachable at its status socket and a
   valid session, **When** the operator runs `hush client refresh
   --socket <path>`, **Then** the supervisor performs a refill (re-fetch
   + re-validate + restart child), acknowledges success to the client,
   and the client command exits 0.
2. **Given** a running supervisor that responds with an error during
   refresh (for example: vault unreachable, validator failed, refill
   denied), **When** the operator runs `hush client refresh --socket
   <path>`, **Then** the command exits with a non-zero exit code and
   prints a clear error including the supervisor-reported reason.
3. **Given** no supervisor is running at the requested socket path,
   **When** the operator runs `hush client refresh --socket <path>`,
   **Then** the command exits with a non-zero exit code and prints a
   clear error explaining that the socket is unreachable or absent.

---

### Edge Cases

- **Stale PID file from a crashed prior supervisor**: When `hush supervise`
  starts and finds a PID file whose owning process is no longer alive,
  the supervisor reacquires the PID file cleanly (delegated to SDD-22's
  flock-on-PID-file behaviour) and starts normally.
- **Stale status socket inode from a crashed prior supervisor**: When
  `hush supervise` starts and finds a stale socket file at the configured
  path, the supervisor cleans it up and rebinds (delegated to SDD-22).
- **Child exits during dry-run preview**: `--dry-run` never spawns the
  child, so this cannot happen by design. If a future flag were added that
  did spawn the child, that flag would be an extension, not part of this
  spec.
- **Status request races a supervisor shutdown**: If the supervisor is
  in the middle of shutting down when `hush client status` connects, the
  client receives either a final status snapshot or a connection-closed
  error. Either is acceptable; the client never hangs indefinitely.
- **Refresh request arrives while a prior refill is in flight**: The
  supervisor coalesces — the second `hush client refresh` returns the
  same terminal ack/error as the in-flight refill (FR-023-22a). No
  "busy" exit class is exposed to the client; the client never hangs
  indefinitely.
- **Mismatched supervisor name in config vs. socket path**: Out of scope
  for this command — config validation already enforces consistency
  between `name` and the derived socket path.
- **TTY detection on a half-pipe (stdout piped, stderr terminal)**: TTY
  detection follows the project-wide rule (NFR-10): output format is
  determined by stdout's TTY-ness only. Stderr is always human-readable.
- **`hush client status` / `refresh` invoked with no `--socket` flag**:
  Resolution proceeds per FR-023-15: try `--supervisor NAME` to derive
  the path the same way the supervisor itself does; else if exactly one
  supervisor socket is present on the host auto-select it; else exit
  with the input-error code naming the ambiguity (zero or >1 sockets).
- **`hush client status --json` invoked on a TTY**: emits JSON to stdout
  (FR-023-17a). The flag bypasses TTY auto-detection; stderr remains
  human-readable.

## Requirements *(mandatory)*

### Functional Requirements

#### `hush supervise`

- **FR-023-1**: `hush supervise <config-path>` MUST accept exactly one
  positional argument — the path to a supervisor TOML configuration file
  conforming to `docs/CONFIG-SCHEMA.md` §"Supervisor config".
- **FR-023-2**: `hush supervise` MUST run in the foreground. It is the
  service manager's responsibility (launchd / systemd) to background it.
  The command MUST NOT fork into the background by itself.
- **FR-023-3**: `hush supervise` MUST orchestrate the already-delivered
  building blocks — configuration loading, state machine, child process
  management, refill / refresh / grace logic, PID file acquisition, and
  status socket binding — into one running daemon. It MUST NOT add new
  business logic; every behavioural rule of the supervisor lifecycle is
  owned by the underlying chunks (SDD-18 through SDD-22).
- **FR-023-4**: On SIGTERM or SIGINT, `hush supervise` MUST initiate a
  graceful shutdown: signal the child, wait for the child to exit,
  release the PID file, remove the status socket, and exit 0.
- **FR-023-5**: On any unrecoverable startup error (missing config,
  invalid config, NTP unsynced, PID file lock held by a live owner, vault
  server unreachable past the configured retry timeout), `hush supervise`
  MUST exit with a non-zero exit code and print a clear error to stderr
  that identifies the failing precondition.
- **FR-023-6**: When the PID file is locked by a live owner — i.e. a
  duplicate supervisor start attempt — `hush supervise` MUST exit with a
  distinct error that clearly identifies the duplicate-supervisor
  condition (AC-10 Scenario 14).

#### `hush supervise --dry-run`

- **FR-023-7**: `hush supervise <path> --dry-run` MUST print to stdout
  the canonical request payload (canonical JSON: alphabetical key order,
  compact spacing — same canonicalisation that the supervisor would use
  to sign a real claim) that would have been sent to the vault server's
  claim endpoint.
- **FR-023-8**: `hush supervise <path> --dry-run` MUST exit 0 after
  printing the payload on success.
- **FR-023-9**: `hush supervise <path> --dry-run` MUST NOT:
  - issue any Discord request,
  - acquire the PID file,
  - bind the status socket,
  - start the child process,
  - contact the vault server (the dry-run renders the payload only; it
    does not need a reachable vault to function).
- **FR-023-10**: Configuration validation MUST run BEFORE the dry-run
  branch is taken — invalid config produces an input-error exit and no
  partial payload, regardless of `--dry-run`.

#### `hush supervise --grace-window` and `--no-cache`

- **FR-023-11**: `--grace-window <duration>` MUST override the
  configuration's grace-cache TTL for this run only. The flag accepts a
  Go-style duration string.
- **FR-023-12**: `--grace-window` values that exceed the v0.1.0 cap of
  4h (per `docs/CONFIG-SCHEMA.md`) MUST be rejected with an input-error
  exit. Negative or zero values MUST also be rejected.
- **FR-023-13**: `--no-cache` MUST force the supervisor into strict mode
  for this run, behaving as though the configuration had set
  `cache_secrets_for_restart=false`.
- **FR-023-14**: When both `--grace-window` and `--no-cache` are supplied,
  `--no-cache` takes precedence; the `--grace-window` value is ignored
  without error.

#### `hush client status`

- **FR-023-15**: `hush client status [--socket <path>] [--supervisor NAME]`
  MUST connect to the supervisor's status socket and retrieve the
  current status document (the JSON shape defined in
  `docs/CONFIG-SCHEMA.md` §"Client status output schema"). Socket
  selection order: (1) `--socket <path>` when supplied wins; (2) else
  derive the path from `--supervisor NAME` using the same rule the
  supervisor itself uses (delegated to SDD-22's platform shims); (3)
  else, if exactly one supervisor socket is present on the host,
  auto-select it; (4) else exit with the input-error code and a clear
  message naming the ambiguity (zero or >1 sockets discovered).
- **FR-023-16**: When stdout is a terminal, `hush client status` MUST
  print a human-readable summary that includes at minimum: supervisor
  name, current state, child PID (or an explicit "no child" indicator
  if no child is running), session expiry, next refresh window, the list
  of healthy scopes, and the list of stale scopes.
- **FR-023-17**: When stdout is not a terminal (i.e. piped or
  redirected), `hush client status` MUST emit the full status document
  as JSON conforming to the shape in `docs/CONFIG-SCHEMA.md`. TTY
  detection follows the project-wide rule from NFR-10. The explicit
  `--json` flag (FR-023-17a) overrides TTY auto-detection.
- **FR-023-17a**: `hush client status --json` MUST emit JSON regardless
  of stdout TTY-ness. There is no symmetric `--human` flag; TTY
  auto-detection is the sole route to human-summary output. `--json`
  is defined only on `hush client status`; `hush client refresh` does
  not accept a format flag.
- **FR-023-18**: When the requested status socket is unreachable, absent,
  or returns an error, `hush client status` MUST exit with a non-zero
  exit code (mapped to the standard exit-code taxonomy in Principle VII)
  and print a clear error to stderr.
- **FR-023-19**: `hush client status` MUST NOT block indefinitely on a
  hung supervisor — the command bounds its total wait (connect + read)
  at **2 s** and treats timeouts as connection failures (mapped to the
  same non-zero exit code as an unreachable socket per FR-023-18).

#### `hush client refresh`

- **FR-023-20**: `hush client refresh [--socket <path>] [--supervisor NAME]`
  MUST send a refresh command to the supervisor's status socket and wait
  for the supervisor's terminal acknowledgement — the ack that the
  supervisor emits **after** the refill completes (re-fetch + re-validate
  + child restart), not on request receipt. Socket selection follows the
  same precedence rule defined in FR-023-15.
- **FR-023-21**: A successful terminal acknowledgement (refill
  completed: re-fetch + re-validate + child restart succeeded) MUST
  map to exit 0.
- **FR-023-22**: A supervisor-reported error during refresh (vault
  unreachable, validator failure, refill denied, etc.) MUST map to a
  non-zero exit code; the supervisor's reason string MUST be surfaced
  to the operator on stderr.
- **FR-023-22a**: When a refresh request arrives while a prior refill
  is in flight, the supervisor MUST coalesce — both refresh callers
  receive the same terminal ack/error from the single in-flight refill.
  The client MUST NOT expose a "busy" exit class; the exit code reflects
  the shared refill outcome (0 on success, non-zero on shared failure).
- **FR-023-23**: When the requested status socket is unreachable or
  absent, `hush client refresh` MUST exit with a non-zero exit code
  and print a clear error to stderr.
- **FR-023-24**: `hush client refresh` MUST NOT block indefinitely on a
  hung supervisor — the command bounds its total wait at **90 s**
  (chosen to cover a Discord-prompted refill on the refresh-window path)
  and treats timeouts as connection failures (mapped to the same
  non-zero exit code as an unreachable socket per FR-023-23).

#### Cross-cutting

- **FR-023-25**: All three subcommands MUST honour the project's global
  flags from Principle VII (`--config/-c`, `--verbose/-v`, `--quiet/-q`,
  `--no-color`).
- **FR-023-26**: All three subcommands MUST use the project's standard
  exit-code taxonomy from Principle VII: 0 success, 1 generic error, 2
  input error, 3 auth error, 4 not found, 5 permission, 78 stale
  credentials (the last used only by `hush supervise` when surfacing
  child exit 78 through to the service manager; not directly used by
  `client status` / `client refresh`).
- **FR-023-27**: No secret value (no API key, no JWT, no decrypted secret)
  may appear in any stdout, stderr, or log output produced by any of
  these three subcommands.
- **FR-023-28**: Errors in any of these three subcommands MUST identify
  the failure mode and any non-secret identifier (supervisor name,
  socket path, scope name) but MUST NOT include the secret value, even
  partially.
- **FR-023-29**: All three subcommands MUST behave consistently across
  macOS and Linux; platform-specific socket path conventions are owned
  by the underlying status-socket chunk (SDD-22), not by these
  subcommands directly.

### Key Entities

- **Supervisor configuration**: A TOML file at a path the operator
  supplies. Defined by `docs/CONFIG-SCHEMA.md` §"Supervisor config".
  Owned (parsed + validated) by SDD-18.
- **Canonical request payload (`/claim`)**: The JSON request body that
  would be sent to the vault server's claim endpoint. Canonicalised
  (alphabetical key order, compact spacing) per `docs/SPEC.md` FR-6.
  The shape is owned by `docs/API.md`; this chunk renders it but does
  not define it.
- **Status document**: The JSON document returned over the status socket.
  Shape defined in `docs/CONFIG-SCHEMA.md` §"Client status output
  schema". Authored by SDD-22; consumed here.
- **PID file**: Per-supervisor file under
  `~/Library/Caches/hush/supervise-<name>.pid` (macOS) or platform
  equivalent (Linux). Lock semantics owned by SDD-22.
- **Status socket**: Per-supervisor Unix socket under
  `~/Library/Caches/hush/supervise-<name>.sock` (macOS) or
  `$XDG_RUNTIME_DIR/hush-supervise-<name>.sock` (Linux). Mode `0600`,
  parent directory `0700`. Owned by SDD-22.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-023-1**: AC-10 Scenarios 12 (status check) and 14 (duplicate
  supervisor refused) both have passing end-to-end coverage in the
  lifecycle test suite. SC-023-1 is the AC-MATRIX row for AC-10 owned
  by this chunk.
- **SC-023-2**: `hush supervise <path> --dry-run` produces a
  machine-parseable canonical-JSON payload on stdout in under 500 ms
  for any valid configuration, with no Discord traffic and no vault
  contact required. The payload round-trips through a JSON parser
  without modification.
- **SC-023-3**: `hush client status` returns a status document (in
  either format) within 1 s for a healthy local supervisor.
- **SC-023-4**: `hush client refresh` either succeeds (exit 0) or
  surfaces a supervisor-reported error (non-zero exit + readable stderr
  message) within the FR-023-24 90 s ceiling — never hangs indefinitely.
- **SC-023-5**: When stdout is a terminal, `hush client status` output
  is judged readable by an operator on first read (subjective, but the
  formatting test asserts the presence of expected human-facing labels
  and the absence of raw JSON delimiters).
- **SC-023-6**: When stdout is piped, `hush client status` output is
  parseable by `jq` and conforms to the schema in
  `docs/CONFIG-SCHEMA.md` §"Client status output schema".
- **SC-023-7**: A duplicate `hush supervise` invocation against the
  same configuration on the same host exits non-zero within 1 s with
  a message that names the duplicate-supervisor condition explicitly.
- **SC-023-8**: A clean shutdown (`SIGTERM`) of `hush supervise`
  releases the PID file and removes the status socket within 5 s under
  normal conditions; a subsequent `hush supervise` invocation against
  the same configuration starts cleanly.
- **SC-023-9**: No subcommand under this spec ever writes a secret
  value (API key, JWT, decrypted scope value) to stdout, stderr, log
  files, or any other operator-visible surface. This is asserted by
  sentinel-leak tests modelled on the existing `internal/cli`
  sentinel-leak conventions.
- **SC-023-10**: The `internal/cli` subdirectory containing
  `supervise.go` + `client.go` (and the corresponding tests) meets
  the SDD-23 coverage target of ≥ 85 %.

## Assumptions

- The OS service manager (launchd on macOS, systemd on Linux) is what
  backgrounds `hush supervise`. This spec assumes the operator is using
  a service manager (or running interactively in a terminal for
  debugging); `hush supervise` does NOT implement its own
  daemonisation, fork-to-background, or process-detachment logic.
- `--socket <path>` is the explicit socket-selection flag for `hush
  client status` and `hush client refresh`; when supplied it always
  wins. Resolution when omitted is normative and defined by FR-023-15:
  (a) `--supervisor NAME` derives the socket path the same way the
  supervisor itself does (SDD-22 platform shims); (b) when no
  `--supervisor` is supplied and exactly one supervisor socket is
  present on the host the client auto-selects it; (c) zero or >1
  sockets without a disambiguating flag exits with the input-error
  code. `--supervisor NAME` is exposed as a first-class flag on both
  client subcommands (matches `docs/SPEC.md` FR-12 shorthand).
- The canonical request payload printed by `--dry-run` is not signed;
  it is rendered for the operator to inspect, not transmitted. The
  real signing path is invoked only on a non-dry-run claim.
- TTY auto-detection follows the project-wide rule from NFR-10 — TTY
  on stdout means human output; non-TTY means JSON. Stderr is always
  human-readable. `--no-color` and `--quiet` from the global flag set
  apply normally.
- The status-socket protocol (commands, framing, response shape) is
  owned by SDD-22 and is treated as a stable contract by this chunk.
  Adding new commands (beyond `status` and `refresh`) is out of scope.
- Platform-specific path resolution for sockets and PID files is owned
  by SDD-22's platform shims. This chunk's files MUST NOT contain
  `runtime.GOOS` branches.
- The OS service manager scripts that invoke `hush supervise` are
  delivered separately (see `deploy/supervise-launch.sh.template`) and
  are out of scope for this spec.
- Bot-down handling on dry-run: dry-run does not contact Discord at
  all, so Discord availability is irrelevant to `--dry-run`. Real
  startup with an unavailable Discord is governed by the vault
  server's 503 contract (FR-20) and is exercised by AC-10 Scenario 10
  (owned elsewhere).
