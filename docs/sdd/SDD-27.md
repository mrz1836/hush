# SDD-27 — `internal/supervise/watchdog` (log-pattern alert-only)

**Phase:** 6
**Package:** `internal/supervise`
**Files:** `watchdog.go`, `*_test.go`
**Branch:** `027-watchdog` (created by the `before_specify` git hook)
**Blocked by:** SDD-20
**Blocks:** SDD-28
**Primary AC:** AC-10
**Coverage target:** 90%

**Behaviour contracts (MUST):**
- Goroutine started via `Run(ctx)` — explicit cancellation (Constitution IX)
- Pattern matching via `regexp` (compiled once, reused)
- Rate limit per-pattern token bucket
- Emit alert via the alert channel (typed `Event` sent to SDD-28's channel)

**Anti-contracts (MUST NOT):**
- Trigger any state transition (alert-only)
- Drop alerts silently — log WARN when rate-limited

**Tests required:**
- Unit: `TestWatchdog_PatternMatchEmitsAlert`, `TestWatchdog_RateLimitBlocksExcess`, `TestWatchdog_NeverTransitionsState` (proves alert-only contract), `TestWatchdog_RunStopsOnCtxCancel`
- Race: `TestWatchdog_ConcurrentLogIngest`

**Constitutional principles in scope:** V (operator visibility — alerts surface via Discord), VIII, IX (explicit goroutine lifecycle)

**Exported API to lock in PACKAGE-MAP.md (this chunk — extends internal/supervise entry):**
- `type Watchdog struct { ... }`
- `type Pattern struct { Name string; Regex *regexp.Regexp; RateLimit time.Duration }`
- `func NewWatchdog(patterns []Pattern, alerts chan<- Event, logger *slog.Logger) *Watchdog`
- `func (w *Watchdog) Ingest(line []byte)`
- `func (w *Watchdog) Run(ctx context.Context) error`
- `type Event struct { Pattern string; Line string; Time time.Time }`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-27 (internal/supervise/
watchdog: log-pattern alert-only) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principle V — operator visibility)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11, AC-10)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  ([watchdog] section)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenario 15 — log pattern matched)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-27.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The watchdog tails the child daemon's stdout/stderr (via the bounded
pipes from SDD-20) and emits a typed alert event whenever a
configured regex pattern matches. It is alert-only — a match never
restarts the child or otherwise changes supervisor state. Rate
limits prevent a flapping pattern from spamming the operator.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The watchdog MATCHES log lines against operator-configured
  regex patterns. It MUST NEVER trigger a state transition or
  restart — it is purely alert-only. (SDD-28 routes the
  alerts; this chunk only emits them.)
- Each pattern has a token-bucket rate limit; matches beyond
  the bucket are dropped with a WARN log (NOT silently).
- The watchdog runs as a single goroutine started via Run(ctx);
  it MUST stop cleanly when ctx cancels.
- The pattern set is compiled ONCE at construction; every Ingest
  call uses the precompiled patterns.

The spec MUST NOT encode HOW (no library names, no specific channel
buffering choice). Those are plan-phase.

Acceptance criterion: AC-10 (supervisor lifecycle — alert subset).

Action — run exactly one command:
  /speckit-specify "internal/supervise/watchdog: tail child stdout/stderr and emit typed alert events on regex pattern match; alert-only (NEVER triggers state transitions or restarts); per-pattern token-bucket rate limit with WARN log on drop; goroutine started by explicit Run(ctx)"

The before_specify hook will create branch 027-watchdog.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-27 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-27.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-27 (internal/supervise/
watchdog) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; V/VIII/IX load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  ([watchdog] section — pattern config shape)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenario 15)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/supervise — extending the SDD-19 entry)
- /Users/mrz/projects/hush/docs/sdd/SDD-27.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/supervise
- Files: watchdog.go (Watchdog + Pattern + Event + Run), watchdog_test.go
- Exported API:
    type Pattern struct { Name string; Regex *regexp.Regexp; RateLimit time.Duration }
    type Event struct { Pattern string; Line string; Time time.Time }
    type Watchdog struct { ... }
    func NewWatchdog(patterns []Pattern, alerts chan<- Event, logger *slog.Logger) *Watchdog
    func (w *Watchdog) Ingest(line []byte)
    func (w *Watchdog) Run(ctx context.Context) error

Implementation contract (HOW — locked):
- Patterns are compiled by the caller (operator-supplied regex
  strings → regexp.MustCompile → Pattern). NewWatchdog receives
  pre-compiled patterns.
- Internal: a buffered channel of incoming lines; Ingest writes
  to it (non-blocking with backpressure log if buffer full).
  Run reads from the channel in a single goroutine, evaluates
  every pattern, emits Event to the alerts channel for matches.
- Per-pattern rate limit: token bucket keyed by pattern name.
  When a match exceeds the bucket: log WARN with pattern name
  and skip the alert emission. NEVER silent drop.
- Run returns when ctx cancels (drains the channel? — document
  the choice in the plan; recommended: drop pending lines on
  cancel, log INFO with count).
- Watchdog NEVER calls into the state machine or refill/refresh
  helpers. Its only output is the alerts channel (consumed by
  SDD-28).

Coverage target: 90%.
Constitutional principles in scope: V, VIII, IX, X.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-27 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-27.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 90%. Tests required: TestWatchdog_PatternMatchEmitsAlert, TestWatchdog_NoMatchNoAlert, TestWatchdog_RateLimitBlocksExcess (assert WARN log emitted on drop, NOT silent), TestWatchdog_NeverTransitionsState (proves alert-only — assert no state.Store API calls via test double), TestWatchdog_RunStopsOnCtxCancel (race-clean), TestWatchdog_ConcurrentLogIngest (race-clean), TestWatchdog_PrecompiledPatternsReused (assert Regex.MatchString call count, not re-compile). Final phase MUST include magex format:fix, magex lint, magex test:race."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-27 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-27.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 90% on internal/supervise (watchdog portion):
     go test -cover ./internal/supervise/ -run Watchdog
3. Confirm rate limit tested (TestWatchdog_RateLimitBlocksExcess
   asserts WARN, not silent drop).
4. Confirm never-restart contract proven
   (TestWatchdog_NeverTransitionsState).
5. Append "Exported API — locked at SDD-27" extension to the
   internal/supervise entry in docs/PACKAGE-MAP.md listing the
   Watchdog/Pattern/Event API from the chunk doc.
6. Update docs/AC-MATRIX.md AC-10 row with the new test file paths
   (alert-only watchdog entry).
7. Mark SDD-27 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add internal/supervise/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(supervise): log-pattern watchdog (alert-only) (SDD-27)"

Final message: confirm gates passed, race-clean, coverage ≥ 90%,
rate limit tested with WARN drop (not silent), never-restart
proven, AC-10 row updated, SDD-PLAYBOOK updated, and the combined
commit created.
```
