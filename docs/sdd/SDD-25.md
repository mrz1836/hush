# SDD-25 — Lifecycle integration harness (15 scenarios; explicit AC-10 owner)

**Phase:** 5
**Package:** `tests/integration/`
**Files:** `tests/integration/{lifecycle_test.go, scenarios_test.go, harness/*.go}` (build-tagged `//go:build integration`)
**Branch:** `025-lifecycle-harness` (created by the `before_specify` git hook)
**Blocked by:** ALL of SDD-01..SDD-23
**Blocks:** SDD-31
**Primary AC:** AC-9 (test infra completeness), AC-10 (15 scenarios — explicit owner)
**Coverage target:** 15/15 scenarios green; suite < 120s on developer laptop

**Behaviour contracts (MUST):**
- Each scenario is a single test function named `Test_Scenario_NN_<slug>`
- Harness uses `internal/testutil` (SDD-04) for vault fixtures, sentinel helpers, Discord stub
- Every scenario asserts:
  1. Final supervisor/server state matches expected
  2. Audit log records expected events in expected order
  3. Status socket JSON matches expected shape (when supervisor scenario)
  4. `AssertSentinelAbsent` on all captured logs
- Scenarios 1–15 from `docs/LIFECYCLE-SCENARIOS.md` — all 15 implemented

**Anti-contracts (MUST NOT):**
- Hit any external network (Discord, Anthropic, etc.) — all mocked
- Skip a scenario due to "complexity"
- Use `t.Parallel` inside a scenario that mutates shared state

**Tests required:**
- 15 scenarios, each a standalone test function. See `docs/LIFECYCLE-SCENARIOS.md` for the full list.

**Constitutional principles in scope:** VIII (TDD discipline applied to integration), V (every scenario must produce operator-observable artifacts — audit + status), IX (explicit harness lifecycle, no goroutine leaks)

**Exported API to lock in PACKAGE-MAP.md (this chunk — new entry):**
- `tests/integration/harness`: a private package consumed only by the integration tests. PACKAGE-MAP entry should list its purpose and reference the harness types (TestVault, TestSupervisor, TestDiscord, TestChild) without freezing signatures (these will evolve as new chunks add scenarios).

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. **This
is the largest test deliverable in the project — plan carefully and
do NOT chain prompts in one session.**

The `extensions.yml` git hooks auto-commit each artifact (accept in
Prompts 1, 3, 4; conditionally in Prompt 2; **decline** in Prompt 5
— Prompt 5 makes one combined commit covering harness + scenarios +
doc updates).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-25 (lifecycle integration
harness — 15 scenarios; explicit AC-10 owner) of the hush project.

This chunk owns AC-10 (15 lifecycle scenarios). It is the largest
test deliverable in the project. Write the spec carefully.

Read first (in order — entire docs):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principle VIII non-negotiable; the AC → required test types matrix)
- /Users/mrz/projects/hush/docs/SPEC.md  (AC-10 specifically + every FR the scenarios touch)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenarios 1–15 — entire doc, every word matters)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md  (§5 sentinel pattern, §7 lifecycle scenario tests)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (48h walkthrough — useful context for scenario shapes)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-9, AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-25.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
SDD-25 delivers the integration test suite that proves AC-10:
fifteen named lifecycle scenarios from docs/LIFECYCLE-SCENARIOS.md,
each running the real internal/* packages end-to-end (with Discord
and external HTTP mocked) and asserting the supervisor/server
ended in the right state, the audit log recorded the right events
in the right order, the status socket reports correctly, and no
sentinel string ever leaked into any captured log. This suite is
the AC-10 owner of record — every other chunk's tests are unit-
or fuzz-level; only this one proves the system works as a whole.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- All 15 scenarios from docs/LIFECYCLE-SCENARIOS.md MUST be
  implemented; none may be skipped or stubbed.
- Each scenario is a single test function with a deterministic
  name shape: Test_Scenario_NN_<slug>.
- Every scenario MUST assert FOUR things:
    1. final supervisor/server state matches the documented
       expected outcome,
    2. the audit log contains the documented events in the
       documented order,
    3. (for supervisor scenarios) the status socket JSON
       matches the documented shape,
    4. no sentinel string appears in any captured log
       (AssertSentinelAbsent over all log capture).
- The suite MUST run with -tags=integration and complete in
  under 120 seconds on a developer laptop. Suite-wide:
  no flake on 5 consecutive runs.
- The harness MUST NOT hit any external network. Discord,
  Anthropic, OpenAI, GitHub, Google AI — all mocked via
  internal/testutil (SDD-04) and httptest.

The spec MUST NOT encode HOW (no Go-specific harness layout, no
specific mock library choices). Those are plan-phase.

Acceptance criteria: AC-9 (test infra completeness), AC-10 (15
lifecycle scenarios — this chunk is the explicit owner).

Action — run exactly one command:
  /speckit-specify "tests/integration: end-to-end harness running real internal/* packages with mocked external services; implements all 15 lifecycle scenarios from docs/LIFECYCLE-SCENARIOS.md, each asserting final state + audit ordering + status socket shape + no sentinel leak; build-tagged //go:build integration; suite under 120s, no flake on 5 runs"

The before_specify hook will create branch 025-lifecycle-harness.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution / scenarios doc.
Otherwise leave the marker — /speckit-clarify will handle it
next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-25 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-25.md and
docs/LIFECYCLE-SCENARIOS.md (the scenarios doc is the source of
truth — most clarify questions reduce to "what does Scenario N
say?").

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-25 (lifecycle harness +
15 scenarios) of the hush project.

Read first (in order — entire docs):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; VIII / IX / X load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (AC-10 + every FR the 15 scenarios touch)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (entire — every scenario diagram dictates the assertions)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md  (§5 sentinel pattern, §7 lifecycle scenario tests)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (48h walkthrough)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (every internal/* — you wire all of them)
- /Users/mrz/projects/hush/docs/sdd/SDD-25.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: tests/integration (with sub-package tests/integration/harness)
- Files (build-tagged //go:build integration):
    tests/integration/harness/vault.go
    tests/integration/harness/supervisor.go
    tests/integration/harness/discord.go
    tests/integration/harness/child.go
    tests/integration/harness/server.go        (real internal/server in-process)
    tests/integration/harness/log_capture.go   (slogtest sink + AssertSentinelAbsent helper)
    tests/integration/lifecycle_test.go        (suite-wide setup + 15 Test_Scenario_NN_<slug> stubs)
    tests/integration/scenarios_test.go        (the 15 scenario implementations split per scenario)
- All test files build-tagged //go:build integration.

Implementation contract (HOW — locked):
- Reuse internal/testutil (SDD-04) wherever possible: NewTestVault,
  NewTestKeys, SentinelSecret, AssertSentinelAbsent, DiscordStub.
- Each scenario test function:
    1. Builds a harness.Supervisor with the per-scenario config
       (real internal/supervise + internal/server packages).
    2. Programmes the DiscordStub with the per-scenario decision
       sequence.
    3. Drives the scenario's events (clock advance, child exit
       codes, network mocks).
    4. Asserts state, audit, status socket, sentinel absence.
- Mocked external boundaries:
    - Discord: testutil.DiscordStub.
    - Validator HTTP endpoints (anthropic, openai, etc.):
      httptest.Server returning per-scenario fixtures.
    - The clock: an injected func() time.Time for refresh-window
      and TTL tests.
- The 15 scenarios from docs/LIFECYCLE-SCENARIOS.md must be
  implemented in scenarios_test.go with names matching
  Test_Scenario_01_<slug> .. Test_Scenario_15_<slug>. Each
  scenario file should be small enough to read in one screen
  height; refactor harness helpers if a scenario sprawls.
- Suite timing: runs serially (not t.Parallel) at the suite level
  to bound the budget; individual scenarios may parallelize
  internally only where they don't share mutable state.
- AC-MATRIX update: this is the test path of record for AC-10.

Coverage target: 15/15 scenarios green; suite < 120s; 5
consecutive runs flake-free.
Constitutional principles in scope: VIII, IX, X.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-25 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-25.md and
docs/LIFECYCLE-SCENARIOS.md (the scenarios doc lists the 15
scenario names; each becomes a task).

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every scenario BEFORE any harness-builder task that scenario depends on (so each scenario fails first, then we build the harness piece that makes it pass — keeps scope honest). Coverage target: 15/15 scenarios green, suite < 120s, no flake on 5 consecutive runs. Tasks required: one harness-helper task per file (vault.go, supervisor.go, discord.go, child.go, server.go, log_capture.go) and one Test_Scenario_NN_<slug> task per scenario from docs/LIFECYCLE-SCENARIOS.md (15 tasks). Every scenario task MUST assert: final state, audit-event ordering, status-socket JSON shape (where applicable), AssertSentinelAbsent over captured logs. Final phase MUST include magex test:race -tags=integration and 5 consecutive runs of the suite to confirm zero flake."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-25 of the hush project.
This is the largest implement session in the project. Pace yourself.

Read /Users/mrz/projects/hush/docs/sdd/SDD-25.md and
docs/LIFECYCLE-SCENARIOS.md (every scenario diagram is your
specification of expected outcomes).

Run: /speckit-implement

Implement scenarios in dependency order: harness helpers first,
then Scenario 1, then 2, etc. Mark each scenario task [X] in
tasks.md as it goes green. If a scenario uncovers a real gap in
SDD-19..23 that no amount of harness work fixes, STOP and surface
the gap in your final message — that's the explicit trigger for
SDD-24's activation (see docs/sdd/SDD-24.md).

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Integration suite (the AC-10 deliverable):
     magex test:race -tags=integration
3. Flake check — run the suite 5 consecutive times:
     for i in 1 2 3 4 5; do magex test:race -tags=integration || break; done
   Zero failures across all 5 runs.
4. Suite timing — confirm under 120s:
     time magex test:race -tags=integration
5. Append a NEW tests/integration entry to docs/PACKAGE-MAP.md
   titled "Exported API — locked at SDD-25". List the harness's
   purpose; do NOT freeze every harness type signature (these
   evolve as new chunks add scenarios).
6. Update docs/AC-MATRIX.md AC-9 and AC-10 rows: this suite is
   the test path of record for AC-10. List each
   Test_Scenario_NN_<slug> name.
7. Mark SDD-25 status `done` in docs/SDD-PLAYBOOK.md.
8. If SDD-24's activation was triggered: leave SDD-24 status
   `pending` in the playbook AND add a note in your final
   message describing the gap that motivated activation.
   Otherwise leave SDD-24 status `skipped`.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add tests/integration/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "test(integration): 15-scenario lifecycle harness (SDD-25)"

Final message: confirm 15/15 scenarios green with -race, no flake
across 5 consecutive runs, suite under 120s, AC-9/10 rows updated
with each scenario name, SDD-PLAYBOOK updated, SDD-24 status
decided (skipped vs pending with rationale), and the combined
commit created.
```
