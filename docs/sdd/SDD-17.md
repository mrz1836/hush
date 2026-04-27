# SDD-17 — `hush secret` (add/remove/list/rotate; interactive TTY enforcement)

**Phase:** 4
**Package:** `internal/cli`
**Files:** `internal/cli/secret.go`, `*_test.go`
**Branch:** `017-cli-secret` (created by the `before_specify` git hook)
**Blocked by:** SDD-03, SDD-15
**Blocks:** SDD-25
**Primary AC:** AC-1, AC-2
**Coverage target:** 85%

**Behaviour contracts (MUST):**
- All write subcommands refuse if stdin is not a TTY (`golang.org/x/term.IsTerminal`)
- Hidden input via `term.ReadPassword` (no echo)
- `list` output: text "NAME — description" or JSON `[{name, description}]`
- `rotate`: signal PID via `syscall.Kill(pid, SIGHUP)` if PID file present at `<state_dir>/hush.pid`; tolerate missing PID file

**Anti-contracts (MUST NOT):**
- Accept value via flag (`--value foo`)
- Read value from stdin pipe
- Print secret values

**Tests required:**
- Unit: `TestSecret_AddRefusesPipedStdin`, `TestSecret_AddTTYHappyPath`, `TestSecret_RemoveAtomic`, `TestSecret_ListNoValues`, `TestSecret_ListJSONOutput`, `TestSecret_RotateAtomic`, `TestSecret_RotateSendsSIGHUP`, `TestSecret_RotateMissingPIDTolerant`

**Constitutional principles in scope:** VII (cobra-only), X (no values printed), Security Requirements (TTY enforcement on management commands)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- internal/cli: subcommand `secret` with `add`, `remove`, `list`, `rotate` (registered via package side-effect in cli.Execute)

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-17 (hush secret
add/remove/list/rotate; TTY-only writes) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles VII, X; Security Requirements — management commands MUST require interactive TTY)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-10, AC-1, AC-2)
- /Users/mrz/projects/hush/docs/SECURITY.md  (specifically the "Rogue process runs hush secret add" threat row)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-1/2 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-17.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
hush secret is the operator-facing vault management command:
add/remove/list/rotate. The write paths (add, remove, rotate)
refuse a piped stdin so that a rogue background process cannot
silently add a secret. list never prints values. rotate signals
the running server via SIGHUP (atomic vault swap from SDD-10).

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- Every write subcommand (add, remove, rotate) MUST refuse to
  run if stdin is not an interactive TTY. The defence covers
  the documented "rogue process" threat in docs/SECURITY.md.
- Secret values MUST NEVER be passed via command-line flag
  (e.g. --value foo). They MUST be entered at a hidden TTY
  prompt only.
- list output is either human text ("NAME — description") on
  a TTY, or JSON ([{name, description}]) when piped. list NEVER
  prints values.
- rotate signals the running server via SIGHUP (so the server's
  SDD-10 reload mechanism picks up the new vault). If no PID
  file is present, rotate completes the file write and exits
  with a warning, NOT an error.

The spec MUST NOT encode HOW (no library names, no specific
syscall names beyond SIGHUP). Those are plan-phase.

Acceptance criteria: AC-1 (server CLI surface), AC-2 (vault
round-trip — write half).

Action — run exactly one command:
  /speckit-specify "hush secret: add/remove/list/rotate vault entries; write subcommands REFUSE if stdin is not an interactive TTY (defends the rogue-process threat); values entered only via hidden TTY prompt, never via flag; list never prints values; rotate signals running server via SIGHUP (tolerates missing PID file with a warning)"

The before_specify hook will create branch 017-cli-secret.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-17 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-17.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-17 (hush secret) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; VII/X/Security Requirements load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-10, AC-1, AC-2)
- /Users/mrz/projects/hush/docs/SECURITY.md  (rogue-process threat row — TTY refusal is the documented defence)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/cli)
- /Users/mrz/projects/hush/docs/sdd/SDD-17.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- internal/cli/secret.go (subcommands: add, remove, list, rotate)
- internal/cli/secret_test.go
- No new exported package-level symbols.

Implementation contract (HOW — locked):
- TTY enforcement: golang.org/x/term.IsTerminal(int(os.Stdin.Fd()))
  must return true for add, remove, rotate. If false:
  ExitInputErr with literal "this command requires an interactive
  TTY (rogue-process defence)".
- Value input: term.ReadPassword (no echo). Confirm by re-prompt
  for add (defence against typo).
- Value type: read into a freshly-allocated []byte → wrap in
  *securebytes.SecureBytes immediately → zero the input []byte.
- add: load existing vault → append/replace by name → vault.Save
  (SDD-03). Atomic by SDD-03's design.
- remove: load → filter out → save.
- list: load → for each Secret print Name + Description (NOT
  Value). TTY → "NAME — description" line per secret. Pipe →
  encoding/json marshal of []{Name, Description}.
- rotate: vault.Save (no content change — same set of secrets,
  re-encrypted with the same key but new nonce/salt) → if a
  PID file exists at <state_dir>/hush.pid → syscall.Kill(pid,
  SIGHUP). Missing PID file → log WARN and continue.
- All subcommands acquire the vault key via the existing
  passphrase-resolution flow from SDD-14 (stdin pipe is NOT
  allowed for these commands per the TTY rule above — must come
  from TTY prompt).

Coverage target: 85%.
Constitutional principles in scope: VII, X, Security Requirements.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-17 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-17.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 85%. Tests required: TestSecret_AddRefusesPipedStdin (proves rogue-process defence), TestSecret_AddTTYHappyPath, TestSecret_AddRefusesValueFlag (no --value), TestSecret_RemoveAtomic, TestSecret_ListNoValues, TestSecret_ListJSONOutput (when piped), TestSecret_ListTTYOutput (text), TestSecret_RotateAtomic, TestSecret_RotateSendsSIGHUP (verify signal sent to fake PID), TestSecret_RotateMissingPIDTolerant (warn, don't error). All tests use a pseudo-TTY for the TTY paths. Final phase MUST include magex format:fix, magex lint, magex test:race, and validate piped-stdin refusal works on darwin AND linux."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-17 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-17.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 85% on internal/cli (secret portion):
     go test -cover ./internal/cli/ -run Secret
3. Confirm piped-stdin refusal works on darwin AND linux —
   manual smoke: `echo foo | hush secret add NAME` returns
   ExitInputErr.
4. Integration smoke: SIGHUP delivery to a fake server (use
   the test stub).
5. Append "Exported API — locked at SDD-17" section to
   docs/PACKAGE-MAP.md under internal/cli noting the secret
   subcommand registration with add/remove/list/rotate verbs.
6. Update docs/AC-MATRIX.md AC-1, AC-2 rows with the new test
   file paths (write half of vault round-trip).
7. Mark SDD-17 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/cli/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(cli): hush secret add/remove/list/rotate (TTY-enforced) (SDD-17)"

Final message: confirm gates passed, race-clean, coverage ≥ 85%,
piped-stdin refusal proven on darwin AND linux, SIGHUP delivery
verified, AC-1 + AC-2 rows updated, SDD-PLAYBOOK updated, and the
combined commit created.
```
