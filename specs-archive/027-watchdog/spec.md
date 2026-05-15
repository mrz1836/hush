# Feature Specification: Log-Pattern Watchdog (Alert-Only)

**Feature Branch**: `027-watchdog`
**Created**: 2026-05-13
**Status**: Draft
**Input**: User description: "internal/supervise/watchdog: tail child stdout/stderr and emit typed alert events on regex pattern match; alert-only (NEVER triggers state transitions or restarts); per-pattern token-bucket rate limit with WARN log on drop; goroutine started by explicit Run(ctx)"

## Overview

The supervisor already detects stale credentials through two trusted
control-plane signals: pre-flight validators (read-only provider auth
probes) and the child-exit-78 contract. Both are authoritative — the
supervisor changes state when they fire.

The watchdog adds a **third, alert-only** signal: it tails the running
child's stdout/stderr and emits a typed alert event whenever any
operator-configured pattern matches a log line. The match is **never**
promoted to a state-machine transition or a child restart. Its sole
purpose is to give the operator early visibility into anomalies that
the trusted control-plane signals will eventually confirm — for
example, "401 Unauthorized" appearing in the child's log seconds
before exit-78 is recorded.

Because log signals are produced by untrusted child output (and the
configured patterns are operator-supplied regex), false positives are
expected. The watchdog must therefore be loud about the **first**
occurrence, quiet about repeats inside a per-pattern rate budget, and
incapable of taking action on its own.

## Clarifications

### Session 2026-05-13

- Q: Who writes the audit row for a watchdog-emitted alert — the watchdog itself, or the downstream alert router (SDD-28)? → A: Watchdog only emits the typed event; SDD-28 alone writes the audit row when it routes the alert.
- Q: Does the WARN suppression log entry (rate-limit drops and alert-output-saturated drops) include the matched line content? → A: No — the WARN entry contains pattern name, suppressed-match count for the episode, and monotonic timestamp only; matched-line content is excluded to keep untrusted child output out of the operational slog stream that Constitution X sweeps for sentinels.
- Q: What shape is the per-pattern rate limiter — rolling-window counter, or classical token bucket? → A: Classical token bucket with capacity 1 and refill rate of 1 token per (3600 / `max_alerts_per_hour`) seconds. With the default `max_alerts_per_hour = 6`, that is one token per 600 seconds (10 min). Burst onset emits at most one alert; subsequent matches are suppressed (with WARN) until the next token refills.
- Q: When the watchdog's internal ingestion queue is full, does `Ingest` block the caller or drop the line? → A: Non-blocking drop. `Ingest` MUST NEVER block its caller. When the internal queue is full the line is dropped and a single WARN-level structured log entry is recorded per drop *episode* (not per dropped line), naming the watchdog and the drop count for that episode. This protects the child-tail loop (SDD-20) from back-pressuring the child's stderr/stdout writes.
- Q: Are duplicate pattern names allowed at construction time? → A: No — `NewWatchdog` MUST reject (fail construction) any pattern set whose names are not pairwise distinct. Names are the only identifier downstream consumers (alert events, WARN logs, operator correlation) see, so ambiguity is not tolerable; a clear construction-time error is preferable to silent attribution drift after the supervisor boots.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Operator sees an early-warning alert when a known auth-failure pattern hits the child log (Priority: P1)

Z is asleep at 03:14. A daemon under `hush supervise` starts emitting
`401 Unauthorized` on stderr because the provider rotated its
back-end and the JWT is now stale. Three seconds later the child will
exit with code 78 and the supervisor will route a stale-credential
alert. But before that, the watchdog matches the configured pattern
on the very first occurrence and emits a typed alert that the
downstream alert router (SDD-28) labels `[STALE] Log Pattern Match`
in Discord. Z sees the alert on his phone, opens the dashboard, and
discovers the rotation context faster than he would from the
exit-78 signal alone.

**Why this priority**: This is the Scenario-15 deliverable from
`docs/LIFECYCLE-SCENARIOS.md` and the only operator-visible behavior
this chunk ships. Without it, the watchdog feature does nothing for
the operator. Every other story refines it.

**Independent Test**: Configure the watchdog with one pattern; feed
a single line that matches; observe that exactly one typed alert
event surfaces on the watchdog's alert output. (Audit-log persistence
of the alert is the responsibility of the downstream alert router
(SDD-28) consuming that event and is verified by SDD-28's tests, not
by this chunk's unit tests.)

**Acceptance Scenarios**:

1. **Given** the watchdog is running with a single pattern `401 Unauthorized` and a fresh per-pattern alert budget, **When** the child emits a line containing `401 Unauthorized` on stderr, **Then** exactly one alert event is emitted on the alert output identifying the pattern name and the matched line.
2. **Given** the watchdog is running with two configured patterns and one is currently rate-limited while the other has budget remaining, **When** the child emits a line that matches only the second pattern, **Then** exactly one alert event is emitted for the second pattern and the first pattern's budget is unaffected.
3. **Given** the watchdog is running, **When** the child emits a line that matches **no** configured pattern, **Then** zero alert events are emitted and no log entry references that line.

---

### User Story 2 — Operator is not spammed when the child emits the same pattern in a tight loop (Priority: P2)

A misconfigured daemon enters a retry loop and emits the configured
auth-failure pattern dozens of times per second. Without rate
limiting, the operator's Discord channel would receive a flood of
identical alerts, training the operator to mute or ignore Discord —
which defeats the entire purpose of the alerting tier (Constitution V).
The watchdog therefore enforces a per-pattern alert budget via a
token bucket (capacity 1, refill rate derived from
`max_alerts_per_hour`). Matches that arrive while the bucket is empty
are dropped, but the drop itself is **recorded** in the operator's
structured log at WARN level so a future investigator can correlate
suppressed matches with the surfaced alert.

**Why this priority**: A noisy watchdog is worse than no watchdog
(Constitution V). Rate limiting is the difference between a useful
operator signal and an ignored channel. Lower than P1 only because
P1 must work before rate-limiting matters.

**Independent Test**: Configure one pattern (token bucket: capacity 1,
refill 1 per fixed interval); feed M matching lines in rapid succession
within an interval shorter than the refill period; assert exactly one
alert is emitted on the alert output AND that M-1 structured log
entries at WARN level identify the pattern and record the suppression.

**Acceptance Scenarios**:

1. **Given** the watchdog is running with a single pattern whose token bucket has capacity 1 and a fixed refill interval, **When** the child emits M matching lines back-to-back within a span shorter than one refill interval, **Then** exactly one alert event is emitted (consuming the only token) and M-1 WARN-level structured log entries each record one suppressed match, naming the pattern.
2. **Given** a pattern's token bucket is empty and enough wall-clock time has elapsed for one refill, **When** the next matching line arrives, **Then** an alert event is emitted again (consuming the refilled token) and no WARN suppression log is recorded for that line.
3. **Given** a pattern's token bucket is empty and additional matches keep arriving before the next refill, **When** suppression continues, **Then** every suppressed match produces its own WARN log entry — suppression is never silent.

---

### User Story 3 — Watchdog never changes supervisor state (alert-only contract) (Priority: P3)

The watchdog observes log output produced by an untrusted child
process. Operator-supplied regex patterns can match incidentally
(e.g., a colourised CLI tool printing `[401] Unauthorized` from a
non-auth code path). If a log match could trigger a restart or push
the supervisor into `awaiting-approval`, false positives would create
destructive feedback loops: the supervisor would wake Z at 03:14 for
a pattern that has no underlying credential problem, exactly the
self-inflicted-DoS failure mode Constitution IV forbids. The
watchdog must therefore have **zero** authority over the
state machine. Its only side effects are: emit an alert event, and
write WARN logs for suppressed matches.

**Why this priority**: This is the load-bearing safety property of
the whole chunk. P3 not because it is less important than P1/P2 —
without this property the feature is unshippable — but because it is
a *forbidden-action* property rather than a user-visible journey. It
is verified by tests that prove non-existence of control-plane
interactions.

**Independent Test**: Wire the watchdog against a state-machine
test double that records every interaction. Feed inputs that
exercise every pattern, including patterns that resemble the
exit-78 and validator-failure signals. Assert the state-machine
double records **zero** transition requests, **zero** child-restart
requests, and **zero** session-claim or refresh invocations from
the watchdog.

**Acceptance Scenarios**:

1. **Given** the watchdog is running alongside a recording state-machine double, **When** the watchdog processes any sequence of matching and non-matching log lines, **Then** the state-machine double records zero transition requests originating from the watchdog.
2. **Given** the supervisor receives an exit-78 signal from the child while the watchdog has just emitted a log-pattern alert for the same root cause, **When** the supervisor transitions to `awaiting-approval`, **Then** the transition is attributable to the exit-78 contract alone and the audit trail distinguishes the two signal sources.
3. **Given** a configured pattern matches a line in the child's log, **When** the watchdog emits the alert, **Then** the child process is not signalled, killed, or restarted by the watchdog under any code path.

---

### Edge Cases

- **Empty pattern set**: A watchdog with no patterns must run, accept log lines, emit zero alerts, and stop cleanly on context cancel.
- **Multiple patterns match the same line**: Each matching pattern is evaluated independently against its own alert budget. A single line may produce zero, one, or several alert events — one per matching pattern that still has budget.
- **Pattern matches multiple non-overlapping spans in one line**: Counts as one match for that line + pattern pair (the alert event reports the line, not each span).
- **Watchdog receives a log line before its run-loop is active, or after its context has cancelled**: The line is dropped without panic. If it occurs in volume, the watchdog records a single WARN-level structured log entry per drop episode (not per line).
- **Operator-supplied regex is pathological** (catastrophic backtracking, multi-second per-match cost): Out of scope — pattern compilation and complexity vetting belongs to the config-load path that produced the pattern set, not to the watchdog itself. The watchdog assumes patterns are well-formed.
- **Downstream alert sink is slow to drain emitted events**: The watchdog must not block log ingestion on a slow downstream consumer. If alert delivery cannot keep up, additional matches must drop with WARN logs (same loud-suppression rule as the rate-limit case) rather than stall the ingestion path.
- **Supervisor restart**: All per-pattern alert budgets are reset to full on watchdog construction. The watchdog holds no persistent state.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The watchdog MUST evaluate every ingested log line against the operator-configured pattern set and emit exactly one typed alert event for each (pattern, line) pair whose pattern has remaining alert budget.
- **FR-002**: An emitted alert event MUST identify, at minimum: the matched pattern's operator-facing name, the matched line content, and the wall-clock time of the match.
- **FR-003**: The watchdog MUST NOT initiate any supervisor state-machine transition, child-restart, child-signal, session-claim, session-refresh, or session-revocation as a consequence of a pattern match. Its only effects are alert emission and structured logging.
- **FR-004**: Each pattern MUST have an independent alert budget enforced by a classical token bucket: capacity = 1 token, refill rate = 1 token per (3600 / `max_alerts_per_hour`) seconds. With the default `max_alerts_per_hour = 6` (per `docs/CONFIG-SCHEMA.md`), refill is one token per 600 seconds. A match consumes one token if available and emits the alert; if the bucket is empty the match is suppressed (FR-005). The bucket starts full at watchdog construction so the first match always alerts.
- **FR-005**: When a pattern matches but its alert budget is exhausted, the watchdog MUST record a WARN-level structured log entry that names the pattern and explicitly indicates the alert was suppressed. The WARN entry MUST NOT include the matched line content; it carries the pattern name, a monotonic timestamp, and a suppressed-match counter only. Suppression MUST NEVER be silent.
- **FR-006**: Suppressed-match WARN entries MUST be emitted on every suppressed match (no coalescing across multiple suppressed matches into a single entry), so post-incident investigators can correlate suppressed-match counts with the surfaced alerts. The same line-content exclusion as FR-005 applies.
- **FR-007**: The pattern set MUST be fixed for the lifetime of a watchdog instance — patterns are accepted at construction time and never mutated afterwards. Reconfiguring patterns requires constructing a new watchdog.
- **FR-007a**: Pattern names MUST be pairwise distinct within a single watchdog instance. Construction MUST fail with a clear error when the supplied pattern set contains duplicate names, since names are the only identifier downstream consumers (alert events, WARN logs, operator correlation) see.
- **FR-008**: Pattern compilation MUST occur exactly once, at watchdog construction. Per-line evaluation MUST reuse the compiled pattern set without re-compilation.
- **FR-009**: The watchdog MUST expose a single explicit entry point that runs its evaluation loop, and it MUST stop cleanly — with no leaked goroutines and no further alert emissions or log writes — when the entry point's context is cancelled.
- **FR-010**: The watchdog MUST be safe for concurrent log-line ingestion from multiple producers (e.g., one goroutine per child output stream).
- **FR-010a**: `Ingest` MUST be non-blocking from the caller's perspective. If the watchdog's internal queue cannot accept the line immediately, the line MUST be dropped rather than block the caller. Dropped-line episodes MUST be recorded as a single WARN-level structured log entry per drop *episode* (an episode being a contiguous run of drops uninterrupted by successful enqueues), naming the watchdog and the drop count for that episode. Dropped lines MUST NEVER cause `Ingest` to block the child-tail loop.
- **FR-011**: If the alert output is not ready to receive an event when a match occurs, the watchdog MUST drop the alert and record a WARN-level structured log entry naming the pattern (subject to the same line-content exclusion as FR-005). The watchdog MUST NOT block log ingestion on a slow alert consumer.
- **FR-012**: The watchdog MUST NOT leak the matched line content, pattern name, or alert metadata into operational logs above WARN level on the normal-match path. Routine matches surface as alert events only; only suppressions (rate-limit or alert-output-saturation) appear in operational logs.
- **FR-013**: An emitted alert event MUST be representable by a single typed value that downstream alert routing (SDD-28) can consume without re-parsing free-form text.
- **FR-014**: The watchdog MUST tolerate an empty pattern set: it runs, accepts ingested lines, emits zero alerts, and stops cleanly on context cancel.
- **FR-015**: Per-pattern alert budgets MUST be held entirely in process memory — the watchdog MUST NOT persist budget state to disk, the audit log, or any external store.

### Key Entities

- **Pattern**: An operator-named regex predicate paired with a rate-limit budget. Configured at construction time. Has a stable human-readable name — required to be unique within the watchdog instance — used in alert events and WARN logs so operators can correlate.
- **Alert Event**: A typed value emitted on a match, carrying the pattern name, the matched line, and the wall-clock time of the match. Consumed by the downstream alert router (SDD-28).
- **Watchdog Instance**: A single-instance, single-run construct that owns a fixed pattern set, the per-pattern budget state, and the run-loop goroutine. Lifetime is bounded by the context passed to its run entry point.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: When a configured pattern matches a fresh log line, an alert event is observable on the watchdog's alert output within 100 milliseconds of ingestion (well under the multi-second window before exit-78 routing would surface the same problem).
- **SC-002**: Under a synthetic burst of 1,000 matching lines for the same pattern within one second (with the bucket starting full), the operator's downstream alert channel receives **exactly one** alert (the bucket capacity), and 100% of the suppressed matches produce a WARN log entry naming the pattern.
- **SC-003**: Across the full integration suite for AC-10 Scenario 15, the supervisor state machine records **zero** transitions whose proximate cause is a log-pattern match — every observed transition is attributable to either exit-78, validator failure, vault 401-unknown-jti, refresh-window expiry, or operator action.
- **SC-004**: When the watchdog's context is cancelled, its run-loop returns within 250 milliseconds and the process's goroutine count returns to the pre-watchdog baseline (no leaked goroutines).
- **SC-005**: Operator-facing rate-limit budgets behave as documented in `docs/CONFIG-SCHEMA.md`: the `[watchdog].max_alerts_per_hour` field controls the per-pattern budget, and changing that value changes the observed alert cap during a synthetic burst test.
- **SC-006**: Pattern compilation cost is incurred exactly once per watchdog construction; ingesting 10,000 log lines through a watchdog with K compiled patterns does not invoke pattern compilation more than K times in total.

## Assumptions

- The supplier of log lines to the watchdog (the supervisor's child-output tail loop) is provided by an upstream chunk (SDD-20) and feeds the watchdog one logical log line per ingest call. The watchdog assumes line boundaries are already established and does not perform its own line-buffering.
- The operator-supplied regex patterns in the configuration file have already been validated and compiled by the config-load path (SDD-18 territory). The watchdog receives ready-to-use compiled patterns.
- The downstream alert router (SDD-28) provides the alert output channel and is responsible for tier-routing (Discord, audit log, etc.). The watchdog only emits typed events; it does not know which channel they reach.
- The watchdog operates in-process within the supervisor; cross-process or cross-host concerns are out of scope.
- Per-pattern alert budgets are in-memory only; supervisor restart resets all budgets. Persistence would expand attack surface (an offline copy of suppression history could leak operator intelligence about provider failure patterns) and is not justified by operator value at this tier.
- Wall-clock time for alert-event timestamps and rate-limit accounting comes from the same monotonic clock source already used elsewhere in the supervisor. Clock drift handling is upstream's concern.
- The watchdog is the **only** consumer of the alert-only tier produced from log signals in this chunk. Future stale-detection channels (validators, exit-78) emit through their own dedicated paths and do not share this alert output.
