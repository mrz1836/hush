# Feature Specification: Supervise Child Process Layer

**Feature Branch**: `020-supervise-child`
**Created**: 2026-05-05
**Status**: Draft
**Input**: User description: "internal/supervise child: fork/exec a daemon in its own process group (kernel kills group if supervisor dies), absolute-path command only (no shell), signal forwarding via explicit goroutine, exit-78 recognition (stale-credential signal from daemon), Wait returns (exitCode, signal, err); bounded stdout/stderr pipes"

## Overview

This feature delivers the supervisor's child-process layer: it launches the
configured daemon, forwards signals to it, surfaces the daemon's exit
disposition (status code OR terminating signal), recognizes exit code 78 as
the project-wide stale-credential signal, and ensures the daemon's process
group is reaped if the supervisor itself dies — so a crashed `hush supervise`
never leaves orphaned daemons running with stale state.

This layer is consumed by the refill/refresh wiring (which converts exit-78
into a state-machine event) and by the lifecycle integration harness. It
does NOT own the supervisor state machine, the Discord approval flow, the
JWT, or the vault — those concerns live in adjacent chunks. This layer's
sole job is to be a safe, signal-correct, leak-proof process wrapper.

## Clarifications

### Session 2026-05-05

- Q: When multiple goroutines call the exit-disposition read concurrently on the same Child (before any has returned), what is the contract? → A: First caller receives the exit disposition; all other concurrent callers receive `ErrChildNotStarted` (or equivalent "child no longer live" sentinel).
- Q: Which error sentinel is surfaced when a signal is forwarded after the daemon has exited (whether or not Wait has been called)? → A: Reuse `ErrChildNotStarted` for every "no live child" case (never-started, post-Wait, post-exit-not-yet-Waited); no distinct `ErrChildExited` sentinel.
- Q: What terminates the signal-forwarding goroutine when the daemon exits voluntarily while the supervisor's context is still live? → A: The goroutine exits on context cancellation OR child process exit, whichever comes first; this guarantees no leak across daemon restarts within a long-lived supervisor context.
- Q: If the operator-supplied stdout/stderr sink blocks indefinitely, what is the layer's contract? → A: A blocked sink is treated identically to a slow consumer — the bounded buffer drops oldest content, the daemon's writes never block, and the supervisor (signal forwarding + Wait) remains responsive. The no-deadlock invariant holds end-to-end regardless of sink speed.
- Q: What is the measurable rate-limit budget for overflow warnings? → A: At most one warning per overflow episode per stream, where an episode is a continuous period during which the buffer remains at capacity and resets when the buffer drains below full.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Daemon launches and reports exit disposition (Priority: P1)

The supervisor starts a configured long-running daemon. When the daemon
eventually exits, the supervisor learns three things: (a) was it terminated
by a signal or did it exit voluntarily, (b) if voluntary, what was the exit
code, and (c) if terminated, what signal terminated it. This is the bedrock
contract on which every higher-level lifecycle decision (silent refill,
stale-credential reaction, restart-loop suppression) depends.

**Why this priority**: Nothing else in the supervisor state machine works
correctly without an unambiguous read of how the child exited. Conflating
exit-by-signal with exit-by-status is the source of classic restart-loop
bugs.

**Independent Test**: Spawn an absolute-path noop binary, wait for it,
assert that exit code, terminating signal, and any I/O error are reported
distinctly. Spawn a binary that exits with a non-zero code; assert exit
code is reported, signal is zero. Spawn a binary that is killed by a
signal; assert signal is reported, exit code is the conventional sentinel.

**Acceptance Scenarios**:

1. **Given** a daemon configured with an absolute-path command, **When**
   the supervisor starts it and the daemon exits cleanly with code 0,
   **Then** the supervisor observes exit code 0, no terminating signal,
   and no I/O error.
2. **Given** a daemon configured with an absolute-path command, **When**
   the daemon exits with a non-zero status, **Then** the supervisor
   observes that status verbatim (so callers can compare against exit 78).
3. **Given** a daemon configured with an absolute-path command, **When**
   the daemon is terminated by a signal, **Then** the supervisor observes
   the terminating signal distinctly from a status-coded exit.

---

### User Story 2 — Stale-credential exit (78) is recognized (Priority: P1)

The daemon detects that one of its credentials is stale (e.g. an upstream
auth check returned 401) and exits with code 78. This code is the
documented project-wide contract meaning "my credentials are stale; please
refetch and restart". The supervisor's child layer surfaces this code
faithfully so higher layers can react: trigger a fresh approval, run
validators, restart with new secrets — instead of naively restarting into
the same broken auth state.

**Why this priority**: Exit 78 short-circuits the most damaging supervisor
failure mode — silent restart loops with stale credentials, which produced
the 114 MB log incident this project is explicitly designed to prevent.

**Independent Test**: Spawn a tiny test helper whose only job is to exit
78. Assert that the supervisor's exit-disposition reading reports exit
code 78 (and no terminating signal), and that the layer exposes a stable
named constant for callers to compare against rather than a magic number.

**Acceptance Scenarios**:

1. **Given** a daemon that exits with code 78, **When** the supervisor
   reads the exit disposition, **Then** the exit code is reported as 78
   and is comparable against a stable, exported constant.
2. **Given** a daemon that exits with any other status, **When** the
   supervisor reads the exit disposition, **Then** the exit code MUST
   NOT be silently coerced to 78.

---

### User Story 3 — Signals from supervisor reach the daemon (Priority: P1)

The supervisor needs to deliver lifecycle signals (notably SIGTERM for
graceful shutdown and SIGHUP for reload semantics) to the running daemon.
Forwarding runs on its own dedicated, supervised goroutine that is started
when the daemon starts and is cancelled when the supervisor's context is
cancelled. Goroutine leaks here would silently accumulate over restarts
and eventually exhaust resources.

**Why this priority**: Without reliable signal forwarding, the supervisor
cannot perform graceful shutdown or restart semantics, and Scenario 5
(child SIGTERM from operator) cannot be satisfied.

**Independent Test**: Spawn a helper that traps SIGTERM and writes a
known marker before exiting. Forward SIGTERM through the supervisor;
assert the helper observed the signal. Cancel the supervisor's context;
assert the forwarding goroutine has exited (no goroutine leak).

**Acceptance Scenarios**:

1. **Given** a running daemon, **When** the supervisor forwards SIGTERM,
   **Then** the daemon receives SIGTERM and the supervisor observes the
   resulting termination via its exit-disposition reading.
2. **Given** a running daemon and a forwarding goroutine, **When** the
   supervisor's context is cancelled, **Then** the forwarding goroutine
   terminates without requiring the daemon to exit first.

---

### User Story 4 — Supervisor death does not orphan the daemon (Priority: P1)

If the supervisor process itself dies — by crash, by SIGKILL, by an
operator killing it, by a launchd/systemd-driven termination — the daemon
and any of its descendants MUST also die. Orphaned daemons running under
init with stale, no-longer-managed credentials are exactly the failure
mode that necessitates a supervisor in the first place.

**Why this priority**: An orphaned daemon defeats the entire reason this
layer exists. It would continue to hold (and possibly use) credentials
the supervisor can no longer rotate, refresh, or revoke.

**Independent Test**: Spawn a daemon that itself spawns a grandchild.
Terminate the daemon's process group. Assert that both the daemon and
the grandchild are gone. In a separate scenario, kill the supervisor
itself ungracefully; assert that the daemon process is reaped by the
operating system shortly after.

**Acceptance Scenarios**:

1. **Given** a running daemon and any descendants it spawned, **When**
   the daemon's process group is signalled, **Then** the daemon AND its
   descendants receive the signal — none are isolated from process-group
   delivery.
2. **Given** a running supervisor with a child daemon, **When** the
   supervisor process terminates abnormally (without an opportunity to
   clean up), **Then** the operating system terminates the daemon
   process group; no orphaned daemon remains attached to init.

---

### User Story 5 — Path-injection attacks are refused (Priority: P1)

The daemon's command MUST be specified as an absolute path. The
supervisor MUST refuse a relative path, an empty command, or any input
that would require shell parsing or PATH lookup. This closes a class of
attacks where a compromised PATH or working directory could redirect the
supervisor to a hostile binary.

**Why this priority**: This is a security-critical input-validation
boundary. A relative-path acceptance here would invalidate the entire
"zero secrets at rest" threat model by permitting an attacker who controls
the agent's working directory to substitute the daemon binary.

**Independent Test**: Configure the supervisor with a relative path;
assert it refuses to start and surfaces a stable, comparable error.
Configure with an empty command; assert the same. Configure with an
absolute path; assert it accepts.

**Acceptance Scenarios**:

1. **Given** a configuration whose command is a relative path, **When**
   the supervisor attempts to start the child, **Then** it refuses and
   surfaces a distinct, comparable error indicating relative-path
   rejection.
2. **Given** a configuration whose command is empty, **When** the
   supervisor attempts to start the child, **Then** it refuses with the
   same class of error.
3. **Given** an absolute-path command, **When** the supervisor starts,
   **Then** the command is launched directly with no shell interposed
   and no PATH lookup performed.

---

### User Story 6 — Chatty daemon cannot deadlock the supervisor (Priority: P2)

A daemon that floods stdout or stderr MUST NOT be able to block the
supervisor by virtue of the supervisor failing to drain those pipes
fast enough. The supervisor exposes a bounded buffer for each stream;
when the buffer fills, the oldest content is dropped (and a single
warning is recorded per drop event). The supervisor never blocks on
the daemon's I/O.

**Why this priority**: Real daemons sometimes spew. A supervisor that
deadlocks under high log volume would prevent every other supervisor
function — including signal forwarding and exit detection — from
operating. P2 because correctness of higher-priority stories does not
strictly depend on this, but production reliability does.

**Independent Test**: Spawn a helper that emits well over the buffer
size on stdout in a tight loop. Assert that the supervisor remains
responsive (signal forwarding still works, exit disposition is still
read correctly), and that the daemon does not block on its own write
syscalls long enough to stall its main loop.

**Acceptance Scenarios**:

1. **Given** a daemon that writes far more output than the supervisor's
   bounded buffer can hold, **When** time passes, **Then** the
   supervisor remains responsive and the daemon does not deadlock on
   its writes.
2. **Given** a buffer overflow event, **When** content is dropped
   during a continuous overflow episode, **Then** exactly one warning
   is recorded per episode per stream — a sustained flood produces one
   warning while the buffer remains full, not one warning per byte
   dropped, and a second warning is emitted only after the buffer has
   drained below capacity and overflowed again.

---

### Edge Cases

- The daemon exits between the supervisor's start call and the
  supervisor's first read of exit disposition. Exit information MUST
  still be retrievable.
- The supervisor's context is cancelled before the daemon exits. The
  forwarding goroutine MUST terminate; the daemon MUST receive an
  appropriate termination path (delegated to the higher layer that
  owns shutdown ordering — this layer guarantees no goroutine leak).
- A second attempt to read exit disposition after the first read
  succeeded MUST return a stable, comparable error indicating the
  child is no longer started; the layer MUST NOT cache and re-serve
  stale exit data.
- A signal is forwarded after the daemon has already exited. The
  forward call MUST surface the same single "no live child" sentinel
  used for never-started and post-Wait cases (see FR-020-11) rather
  than panicking or silently succeeding.
- The daemon spawns a grandchild that ignores SIGTERM. Killing the
  process group still delivers the signal; what the grandchild does
  with it is its own concern, but the kernel-level delivery to the
  whole group MUST happen.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-020-1**: The supervisor MUST launch the daemon directly from an
  absolute-path command, with no shell parsing and no PATH lookup.
- **FR-020-2**: The supervisor MUST refuse to start the daemon if the
  configured command is empty, and MUST surface a stable, comparable
  error.
- **FR-020-3**: The supervisor MUST refuse to start the daemon if the
  configured command's first element is not an absolute path, and MUST
  surface a stable, comparable error distinct from the empty-command
  case.
- **FR-020-4**: The supervisor MUST place the daemon in its own process
  group, so that group-targeted signals reach the daemon and every
  process it spawned.
- **FR-020-5**: If the supervisor process terminates — by any means,
  including abnormal termination that prevents user-space cleanup — the
  operating system MUST terminate the daemon's process group. Orphaned
  daemons re-parented to init are not permitted.
- **FR-020-6**: The supervisor MUST forward signals to the daemon's
  process group, so descendants spawned by the daemon also receive
  forwarded signals.
- **FR-020-7**: Signal forwarding MUST run on a dedicated goroutine,
  started when the daemon starts and terminated when EITHER the
  supervisor's context is cancelled OR the daemon process has exited
  (whichever comes first). Forwarding MUST NOT leak goroutines across
  daemon restarts, including when the supervisor's outer context
  outlives many sequential child lifetimes.
- **FR-020-8**: The supervisor MUST expose an exit-disposition read that
  returns three independent pieces of information: the daemon's numeric
  exit code, the signal (if any) that terminated the daemon, and any
  I/O error from waiting on the daemon.
- **FR-020-9**: Exit code 78 MUST be recognized as the project's
  stale-credential signal and MUST be surfaced verbatim in the exit
  disposition. The layer MUST expose a stable, named constant for the
  value 78 so that callers compare against the constant rather than a
  magic number.
- **FR-020-10**: The supervisor MUST NOT coerce, remap, or mask any
  other exit code into 78, and MUST NOT coerce 78 into any other value.
- **FR-020-11**: After a successful exit-disposition read, a subsequent
  attempt to read exit disposition or to forward a signal MUST surface
  a stable, comparable error indicating the child is no longer started.
  The layer MUST NOT cache the released child handle. The same single
  sentinel error MUST cover every "no live child" condition — child
  was never started, child has exited and Wait already returned, and
  signal forwarded after the child has exited but before Wait was
  called. No distinct "child has exited" sentinel is exposed.
- **FR-020-12**: The supervisor MUST consume the daemon's stdout and
  stderr through bounded buffers. The buffers' aggregate memory cost
  per stream MUST be small (kilobyte-scale, not megabyte-scale) so that
  many supervised daemons cost predictable memory.
- **FR-020-13**: When a bounded buffer overflows — whether because the
  daemon is writing faster than the sink consumes OR because the
  operator-supplied sink has blocked — the supervisor MUST drop the
  oldest content (FIFO eviction), MUST NOT block the daemon's writes,
  and MUST surface exactly one warning per overflow episode per stream.
  An overflow episode begins when the buffer reaches capacity and ends
  when the buffer drains below capacity; only one warning MUST be
  emitted for the entire duration of a single episode. The no-deadlock
  invariant (daemon writes never stall; signal forwarding and Wait
  remain responsive) MUST hold regardless of sink speed, including
  indefinitely blocked sinks.
- **FR-020-14**: All public errors surfaced by this layer MUST be
  comparable via the project's standard error-comparison mechanism (so
  callers can branch on "command-was-relative", "child-not-started",
  etc., without string matching).
- **FR-020-15**: The supervisor's reading of exit disposition MUST be
  safe under concurrent invocation and MUST be race-clean. Under
  concurrent invocation, exactly one caller MUST receive the exit
  disposition; every other concurrent caller MUST receive the same
  stable, comparable "child no longer started" error returned by a
  post-success sequential read (see FR-020-11). The exit disposition
  MUST NOT be cached for broadcast to multiple readers.
- **FR-020-16**: This layer MUST NOT initiate any network I/O, vault
  access, Discord access, audit-log writes, or state-machine
  transitions. Its sole responsibility is the daemon process and its
  immediate signal/IO/exit boundaries.

### Key Entities *(include if feature involves data)*

- **Child**: A handle to a single supervised daemon process. Holds the
  process identity, the bounded output buffers, and the forwarding
  goroutine's lifecycle. A Child is single-use: once it has reported
  exit disposition, it is invalid for further use.
- **Child Configuration**: The inputs needed to launch a Child: the
  absolute-path command vector, environment, working directory, output
  sinks for stdout/stderr (which receive content drained from the
  bounded buffers), and a logger for warnings.
- **Exit Disposition**: A three-tuple — exit code, terminating signal,
  I/O error — that fully describes how the daemon left the running
  state.
- **Stale-Credential Exit Code**: The named constant for the value 78,
  used as the contract between daemon and supervisor for "my creds
  are stale; refetch and restart".

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-020-1**: An operator can start the supervisor against a daemon
  configured with an absolute-path command and observe the daemon
  reaching its running state with no shell process interposed in the
  process tree.
- **SC-020-2**: When the daemon exits with status 78, the operator's
  next observation of supervisor state shows the stale-credential
  contract was recognized — the supervisor did not silently restart
  into the same auth state.
- **SC-020-3**: When the supervisor process is terminated abruptly,
  no daemon process from that supervisor remains running. Verifiable
  by listing processes after supervisor termination: zero matches.
- **SC-020-4**: A daemon that emits more than one megabyte of output
  per second for a sustained period does not stall the supervisor's
  ability to forward a SIGTERM within one second of the operator
  issuing it.
- **SC-020-5**: A configuration audit that lists every supervised
  daemon's command finds zero relative paths, zero empty commands,
  and zero shell-style commands. The supervisor refuses to start any
  of those configurations rather than silently accepting them.
- **SC-020-6**: After 100 daemon restart cycles within a single
  supervisor process, the supervisor's goroutine count returns to
  its pre-first-start baseline. No accumulation across restarts.
- **SC-020-7**: Across 100 concurrent exit-disposition reads under a
  race detector, no data race is reported.
- **SC-020-8**: Test coverage on this layer's source files is at least
  90%, and every behavioural contract in the Functional Requirements
  has at least one corresponding automated test that fails before the
  implementation exists.

## Assumptions

- The platforms in scope are macOS (darwin) and Linux. Windows is out
  of scope for this layer; process-group semantics differ enough that
  Windows support would warrant a separate design.
- The higher-level state machine (already specified in the prior
  chunk) is responsible for deciding what to do with the exit
  disposition this layer reports; this layer does not interpret exit
  78 beyond surfacing it.
- The higher-level lifecycle harness is responsible for end-to-end
  Scenario 2..5 coverage; this layer provides the unit-level
  guarantees those scenarios depend on.
- The daemon binary is trusted for the purpose of running — the
  threat model defended here is path-injection (substituting the
  binary at a different path), not malicious behaviour by the
  legitimate daemon.
- The bounded-buffer guarantee defends against both a fast daemon and
  a slow or indefinitely blocked operator-supplied sink. The
  no-deadlock invariant holds end-to-end: daemon writes never stall
  regardless of sink behavior.
- The supervisor's context cancellation is the canonical signal that
  forwarding should stop; no separate shutdown channel is required.
