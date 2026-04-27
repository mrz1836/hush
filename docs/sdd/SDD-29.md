# SDD-29 — Deploy artifacts (launchd plist, systemd unit, install.sh, generic supervisor launcher template)

**Phase:** 7
**Package:** `deploy/`
**Files:** `deploy/{hush.plist, hush.service, install.sh, supervise-launch.sh.template}`
**Branch:** `029-deploy-artifacts` (created by the `before_specify` git hook)
**Blocked by:** SDD-15, SDD-23
**Blocks:** SDD-30, SDD-32
**Primary AC:** AC-1, AC-6, AC-10
**Coverage target:** N/A (smoke test only — `bash -n` parsing, shellcheck if available, install.sh idempotency in tempdir)

**Behaviour contracts (MUST):**
- `install.sh` idempotent (re-running leaves the system in the same state)
- `install.sh` adds `tmutil` exclusion on macOS (Constitution XI — vault is ephemeral, never backed up)
- Keychain entries use `-T /usr/local/bin/hush` ACL (or the resolved binary path)
- launchd plist + systemd unit BOTH set non-root user
- `supervise-launch.sh.template` execs `hush supervise` (NOT `hush request --exec`); placeholders `<NAME>` / `<KEYCHAIN_ITEM>` are clearly marked for operator substitution

**Anti-contracts (MUST NOT):**
- Use `hush request --exec` for daemons (would re-prompt on every restart — defeats Constitution IV)
- Skip the `tmutil` exclusion (Constitution XI non-negotiable)
- Run as root
- Hard-code any operator's specific daemon names in committed files

**Tests required:**
- `bash -n` parsing on every shell file
- `shellcheck` clean (if shellcheck available; otherwise documented as a CI prerequisite)
- `install.sh` runs idempotently in `t.TempDir`

**Constitutional principles in scope:** I (operator-agnostic), IV (daemons never re-prompt), XI (tmutil exclusion + non-root)

**Exported API to lock in PACKAGE-MAP.md (this chunk — new entry):**
- `deploy/`: this is a deploy-artifact directory, not a Go package. PACKAGE-MAP entry should describe the four files and their operator-facing contract, with the note "no exported Go symbols — see install.sh --help for installation usage".

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

This chunk is mostly tasks-list-driven (the spec and plan are
short — the deliverables are deploy artifacts, not Go code).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-29 (deploy artifacts:
launchd plist, systemd unit, install.sh, generic supervisor
launcher template) of the hush project (open-source release).

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles I, IV, XI — operator-agnostic, daemons don't re-prompt, vault never backed up + non-root)
- /Users/mrz/projects/hush/docs/OPERATIONS.md  (deployment topology, runbooks)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Keychain ACLs)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11 — the supervisor pattern is for daemons, NOT hush request --exec)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (multi-daemon pattern)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-1 + AC-6 + AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-29.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
This chunk delivers the four files an operator needs to deploy
hush on macOS or Linux: a launchd plist for the hush server, a
systemd unit for the hush server, an idempotent install.sh
that lays down binaries + Keychain entries with proper ACLs +
macOS tmutil exclusion, and a generic supervisor launcher
template operators copy and customise per daemon.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- install.sh MUST be idempotent — running it twice is safe and
  leaves the system in the same state.
- On macOS, install.sh MUST add a tmutil exclusion for the
  vault state directory (Constitution XI — ephemeral data must
  never be backed up).
- Keychain entries created by install.sh use the -T flag with
  the absolute path to the installed hush binary (typically
  /usr/local/bin/hush) so only that binary can read them.
- BOTH the launchd plist AND the systemd unit set a non-root
  user; the daemons never run as root.
- The generic supervisor launcher template execs `hush
  supervise` — it MUST NOT use `hush request --exec`, because
  request --exec re-prompts the operator on every restart
  (defeats Constitution IV's TTL discipline for daemons).
- The launcher template uses clearly-marked placeholders like
  <NAME> and <KEYCHAIN_ITEM> that operators substitute when
  they fork the template for their own daemons.
- NO file in this chunk hard-codes any operator's specific
  daemon names, hostnames, or Tailscale tags.

The spec MUST NOT encode HOW (no specific systemd directive
choices, no specific launchd schema details). Those are
plan-phase.

Acceptance criteria: AC-1 (CLI surface), AC-6 (Keychain ACL), AC-10
(daemon lifecycle).

Action — run exactly one command:
  /speckit-specify "deploy/: launchd plist + systemd unit + install.sh (idempotent, adds macOS tmutil exclusion, sets up Keychain entries with -T binary-path ACL, non-root) + supervise-launch.sh.template (execs hush supervise, NOT hush request --exec, with clearly-marked <NAME>/<KEYCHAIN_ITEM> placeholders); zero operator-specific names hard-coded"

The before_specify hook will create branch 029-deploy-artifacts.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-29 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-29.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-29 (deploy artifacts) of
the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; I/IV/XI load-bearing)
- /Users/mrz/projects/hush/docs/OPERATIONS.md  (deployment topology — operator-facing layout)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Keychain ACLs — the -T mechanism)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (multi-daemon pattern)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (no deploy/ entry yet)
- /Users/mrz/projects/hush/docs/sdd/SDD-29.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- deploy/hush.plist           (launchd; macOS)
- deploy/hush.service         (systemd unit; linux)
- deploy/install.sh           (idempotent installer)
- deploy/supervise-launch.sh.template (operator-customizable)
- Plus one test file: deploy/install_test.sh OR a Go test
  under tests/deploy/install_test.go (//go:build integration)
  that invokes install.sh in t.TempDir twice and asserts
  idempotency.

Implementation contract (HOW — locked):
- launchd plist:
    - Label: com.hush.server (or operator-customizable label
      via install.sh substitution? — choose in plan)
    - Program: /usr/local/bin/hush  (resolved at install time)
    - ProgramArguments: ["hush", "serve", "--config",
      "/usr/local/etc/hush/config.toml"]
    - UserName: a non-root account (install.sh creates it if
      missing; document the convention)
    - RunAtLoad: true; KeepAlive: true.
- systemd unit:
    - [Service] User=<non-root>  (install.sh substitutes)
    - ExecStart=/usr/local/bin/hush serve --config /etc/hush/config.toml
    - Restart=on-failure
- install.sh structure:
    1. Resolve install paths (PREFIX defaults to /usr/local).
    2. Detect OS (uname).
    3. Copy hush binary to ${PREFIX}/bin/.
    4. Copy plist or unit to the right system location.
    5. macOS-only: tmutil addexclusion on the state-dir path.
    6. Idempotency: every step uses install -d / install -m or
       checks-then-acts so repeated runs are no-ops.
    7. Print next-steps banner pointing to docs/CLEAN-MACHINE.md.
- supervise-launch.sh.template:
    - Single-line core: exec /usr/local/bin/hush supervise <CONFIG_PATH>
    - Header comments explain <NAME> and <KEYCHAIN_ITEM>
      placeholders and warn DO NOT use hush request --exec.
- Tests: bash -n on every shell file in CI; shellcheck if
  available (document as recommended); install.sh idempotency
  test in t.TempDir.

Coverage target: N/A (artifacts) — gate is bash -n + shellcheck
+ install.sh idempotency.
Constitutional principles in scope: I, IV, IX, XI.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-29 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-29.md.

Run:
  /speckit-tasks "Tasks (no TDD per se — these are deploy artifacts, not Go code; 'tests' here are smoke tests that should be written BEFORE the artifacts they validate). Tasks required: write deploy/install_test.sh (or Go integration test) BEFORE writing install.sh — the test must assert: bash -n parses install.sh, install.sh runs in t.TempDir, install.sh runs again in the same tempdir without error, the resulting tree contains the hush binary, the plist or unit at the right path, AND on macOS the tmutil addexclusion command was invoked (mock by stubbing tmutil in PATH for the test). Then write deploy/install.sh. Then write deploy/hush.plist. Then write deploy/hush.service. Then write deploy/supervise-launch.sh.template. Then write smoke checks: TestDeploy_PlistParsesAsXML, TestDeploy_ServiceParsesAsINI, TestDeploy_LauncherTemplateExecsSupervise (grep proves no 'request --exec'). Final phase MUST include magex format:fix, magex lint, bash -n on every .sh file, and shellcheck if available."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-29 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-29.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint
2. Shell parse on every committed .sh file:
     bash -n deploy/install.sh
     bash -n deploy/supervise-launch.sh.template
3. shellcheck if available:
     command -v shellcheck && shellcheck deploy/install.sh deploy/supervise-launch.sh.template || echo "shellcheck not installed — document in CI prerequisites"
4. Run the install-idempotency test:
     magex test:race -tags=integration -run TestDeploy_InstallIdempotent
5. Confirm tmutil addexclusion present in install.sh's macOS
   path (grep for "tmutil addexclusion").
6. Confirm supervise-launch.sh.template uses `hush supervise`
   and contains NO `hush request --exec` (grep proves it).
7. Confirm placeholder markers <NAME> and <KEYCHAIN_ITEM> appear
   in the template with documenting comments.
8. Confirm no operator-specific daemon names committed (grep
   the four files for any name that looks personal — should
   be zero matches).
9. Append a NEW deploy/ entry to docs/PACKAGE-MAP.md titled
   "Exported API — locked at SDD-29" describing the four files
   and their operator-facing contract.
10. Update docs/AC-MATRIX.md AC-1, AC-6, AC-10 rows with the new
    deploy file paths.
11. Mark SDD-29 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add deploy/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(deploy): launchd plist + systemd unit + install.sh + launcher template (SDD-29)"

Final message: confirm gates passed, bash -n clean, shellcheck
clean (or noted), install.sh idempotent in tempdir, tmutil
exclusion present, launcher template uses hush supervise (no
request --exec), placeholders clearly marked, no operator-
specific names, AC-1/6/10 rows updated, SDD-PLAYBOOK updated,
and the combined commit created.
```
