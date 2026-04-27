# SDD-23 — `hush supervise` + `hush client status` + `hush client refresh` CLI

**Phase:** 5
**Package:** `internal/cli`
**Files:** `internal/cli/supervise.go`, `internal/cli/client.go`, `*_test.go`
**Branch:** `023-cli-supervise-and-client` (created by the `before_specify` git hook)
**Blocked by:** SDD-14, SDD-18, SDD-19, SDD-20, SDD-21, SDD-22
**Blocks:** SDD-25
**Primary AC:** AC-10
**Coverage target:** 85%

**Behaviour contracts (MUST):**
- `supervise` wires together state, child, refill/refresh/grace, pidfile, socket (orchestrator only — no business logic added here)
- `--dry-run` prints rendered `/claim` payload and exits 0
- `--grace-window` override flag takes precedence over config; `--no-cache` forces strict mode
- `client status`: TTY → human summary; pipe → JSON (auto)
- `client refresh`: send "refresh" command to the supervisor Unix socket; receive ack/error

**Anti-contracts (MUST NOT):**
- Move business logic from `internal/supervise` here
- Add per-OS branches in this file (delegate to `internal/supervise/{darwin,linux}`)

**Tests required:**
- Unit: flag wiring, dry-run path, status output formatting (TTY + JSON), refresh socket round-trip with a fake supervisor
- Integration: dry-run round-trip with a fake supervisor config and `DiscordStub`

**Constitutional principles in scope:** IV (TTL discipline applies to supervisor invocation), V (operator-visible status + refresh), VII (cobra-only)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- internal/cli: subcommands `supervise`, `client status`, `client refresh` (registered via package side-effect in cli.Execute)

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-23 (hush supervise + hush
client status + hush client refresh) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles IV, V, VII)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11, FR-12, FR-22, AC-10)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (Supervisor Config File)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (status socket usage)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-23.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
This chunk wires the supervisor pieces (config from SDD-18, state
from SDD-19, child from SDD-20, refill/refresh/grace from SDD-21,
pidfile/socket from SDD-22) into the operator-facing `hush
supervise` command, plus the two operator query commands `hush
client status` and `hush client refresh`. This is orchestration
glue — no business logic is added here.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- `hush supervise <config-path>` runs a single supervisor in
  the foreground (the OS service manager — launchd / systemd —
  is what backgrounds it).
- `--dry-run` prints the canonical /claim payload that would
  be sent and exits 0 (operator can preview their config
  without burning a Discord prompt).
- `--grace-window <duration>` overrides the config's grace
  window for this run; `--no-cache` forces grace_enabled=false.
- `hush client status [--socket <path>]` queries the status
  socket and prints a human summary on a TTY or JSON when
  piped.
- `hush client refresh` sends a refresh command to the
  supervisor's status socket, prompting an immediate
  refresh-window-style refill.

The spec MUST NOT encode HOW (no library names, no specific
goroutine layout). Those are plan-phase.

Acceptance criterion: AC-10 (supervisor lifecycle).

Action — run exactly one command:
  /speckit-specify "hush supervise (foreground supervisor — wires config + state + child + refill/refresh/grace + pidfile + socket; --dry-run prints canonical /claim payload; --grace-window override; --no-cache strict mode); hush client status (TTY-aware status JSON or human summary); hush client refresh (signals running supervisor via status socket)"

The before_specify hook will create branch 023-cli-supervise-and-client.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-23 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-23.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-23 (hush supervise + client
status + client refresh) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; IV/V/VII load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11, FR-12, FR-22)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (Supervisor Config File)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (status socket usage, refresh semantics)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/cli, internal/supervise)
- /Users/mrz/projects/hush/docs/sdd/SDD-23.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- internal/cli/supervise.go (supervise subcommand orchestrator)
- internal/cli/client.go (client status + client refresh)
- internal/cli/supervise_test.go, client_test.go
- No new exported package-level symbols.

Implementation contract (HOW — locked):
- supervise subcommand:
    1. Load supervise/config (SDD-18) from positional argument.
    2. If --dry-run: build claim payload via SDD-08 canonicalise +
       sign helpers (caller layer — not the actual signer; just
       render); print to stdout; exit 0.
    3. Otherwise:
        a. Acquire pidfile (SDD-22).
        b. Build state.Store (SDD-19).
        c. Spawn StatusServer (SDD-22) goroutine.
        d. Spawn Refresher (SDD-21) goroutine.
        e. Initial Refill (SDD-21).
        f. Spawn Child (SDD-20) with current secrets in env.
        g. Wait loop: child exit → if Exit78 → state Refill →
           if 401 → state AwaitingApproval → re-claim →
           grace cache fallback if enabled → re-spawn child.
        h. On supervisor SIGTERM: cancel ctx, signal child,
           wait for clean exit, release pidfile.
    4. --grace-window <dur> overrides cfg.Grace.Window.
       --no-cache sets cfg.Grace.Enabled = false.
- client status subcommand:
    1. Resolve socket path (--socket OR auto from config).
    2. Dial unix socket; send "status\n"; read JSON response.
    3. TTY: pretty-print human summary.
       Pipe: write JSON to stdout.
- client refresh subcommand:
    1. Resolve socket path.
    2. Send "refresh\n"; read ack/error.
    3. Map ack to ExitOK; error to ExitErr.
- This file MUST NOT contain platform-specific code; delegate
  via SDD-22's socket_{darwin,linux}.go for path resolution.
- This file MUST NOT contain business logic from internal/supervise
  (no state-table reasoning, no exit-code mapping logic — call
  the package).

Coverage target: 85%.
Constitutional principles in scope: IV, V, VII, IX.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-23 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-23.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 85%. Tests required: TestSupervise_DryRunPrintsCanonicalPayload, TestSupervise_DryRunExitsZero, TestSupervise_GraceWindowOverrideTakesPrecedence, TestSupervise_NoCacheForcesStrict, TestSupervise_OrchestrationDelegatesToInternalSupervise (assert no business logic in this file via grep — no state table, no exit-code mapping), TestClientStatus_TTYHumanSummary, TestClientStatus_PipeJSON, TestClientStatus_SocketUnreachableExitErr, TestClientRefresh_AckMapsToExitOK, TestClientRefresh_ErrorMapsToExitErr. Integration test: full dry-run with a fake supervisor config + DiscordStub. Final phase MUST include magex format:fix, magex lint, magex test:race, and magex test:race -tags=integration."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-23 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-23.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Integration tests:
     magex test:race -tags=integration
3. Verify coverage ≥ 85% on internal/cli (supervise + client
   portions):
     go test -cover ./internal/cli/ -run "Supervise|Client"
4. Confirm dry-run produces machine-parseable canonical-JSON
   output (manual smoke).
5. Confirm client status pretty-print is readable on a TTY
   (manual smoke).
6. Confirm internal/cli/supervise.go and client.go contain NO
   per-OS branches (grep for runtime.GOOS — should only appear
   in internal/supervise's platform files).
7. Append "Exported API — locked at SDD-23" section to
   docs/PACKAGE-MAP.md under internal/cli noting the supervise +
   client subcommand registrations.
8. Update docs/AC-MATRIX.md AC-10 row with the new test file paths.
9. Mark SDD-23 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/cli/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(cli): hush supervise + client status/refresh (SDD-23)"

Final message: confirm gates passed (unit + integration), race-
clean, coverage ≥ 85%, dry-run output machine-parseable, status
output pretty on TTY, no per-OS branches in this file, AC-10 row
updated, SDD-PLAYBOOK updated, and the combined commit created.
```
