# SDD-30 — Generic example supervisor TOML + Tailscale ACL + clean-machine checklist

**Phase:** 7
**Package:** `deploy/examples/` + `docs/`
**Files:** `deploy/examples/supervisors/example-daemon.toml`, `docs/TAILSCALE-ACLS.md` (already present — verify accuracy), `docs/CLEAN-MACHINE.md` (already present — verify accuracy)
**Branch:** `030-examples-and-tailscale` (created by the `before_specify` git hook)
**Blocked by:** SDD-18, SDD-29
**Blocks:** SDD-32
**Primary AC:** AC-6, AC-8, AC-10
**Coverage target:** N/A (config + docs)

**Behaviour contracts (MUST):**
- `example-daemon.toml` is fully commented, fully generic; uses placeholder secret names like `EXAMPLE_API_KEY_1`
- `example-daemon.toml` validates against the SDD-18 loader as-is
- Reference `docs/TAILSCALE-ACLS.md` and `docs/CLEAN-MACHINE.md` from the example's comments

**Anti-contracts (MUST NOT):**
- Hard-code any operator's specific secret names, daemon names, hostnames, or Tailscale tags
- Reference any private/internal project name

**Tests required:**
- `TestExamples_GenericTOMLValidates` (added to `internal/supervise/config` tests OR a new `deploy/examples/`-level test file) — feeds the example through the SDD-18 loader and asserts no validation error
- Manual review: confirm both pre-existing docs (`TAILSCALE-ACLS.md`, `CLEAN-MACHINE.md`) are still accurate against the current SPEC + config schema

**Constitutional principles in scope:** I (operator-agnostic), VI (Tailscale-only bind — the ACL doc is the operator's reference)

**Exported API to lock in PACKAGE-MAP.md (this chunk — extends deploy/ entry):**
- `deploy/examples/supervisors/example-daemon.toml`: described as the canonical operator-facing supervisor template; references the SDD-18 schema and the two existing docs.

> **Originator overlay note:** project-specific supervisor configs (for any private daemon) belong in a private fork or sibling overlay repo, NOT in the public `hush` repo. SDD-30 ships ONLY the generic template.

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

This is a config + docs chunk — the spec and plan are short.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-30 (generic example
supervisor TOML + Tailscale ACL + clean-machine checklist) of the
hush project (open-source release).

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles I, VI — operator-agnostic, Tailscale-only)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (Supervisor Config File — the example must populate every documented section)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (multi-daemon pattern)
- /Users/mrz/projects/hush/docs/TAILSCALE-ACLS.md  (existing operator-agnostic ACL guide — verify it still matches the current Tailscale tag-naming convention)
- /Users/mrz/projects/hush/docs/CLEAN-MACHINE.md  (existing operator-agnostic checklist)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-6/8/10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-30.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
This chunk delivers the canonical operator-facing supervisor TOML
template (deploy/examples/supervisors/example-daemon.toml) AND
re-validates two pre-existing operator-agnostic docs
(TAILSCALE-ACLS.md, CLEAN-MACHINE.md) against the current spec
state. The template uses placeholder names (EXAMPLE_API_KEY_1
etc.) so an operator can copy it, find/replace, and have a
working supervisor config without leaking anyone else's daemon
identity into their copy.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The example TOML is fully commented (every field has an
  inline explanation) and fully generic (zero operator-
  specific names).
- The example validates cleanly against the SDD-18 loader
  with no errors.
- The example references docs/TAILSCALE-ACLS.md and
  docs/CLEAN-MACHINE.md in its comments so a copy-pasting
  operator finds the related docs immediately.
- TAILSCALE-ACLS.md and CLEAN-MACHINE.md (already present)
  are re-verified for accuracy against the current SPEC.
- NO file in this chunk references any operator's private
  daemon names, hostnames, Tailscale tags, or project codenames.

The spec MUST NOT encode HOW (no specific TOML syntax beyond what
SDD-18's schema dictates). Those are plan-phase.

Acceptance criteria: AC-6 (Keychain ACL — referenced by the
example), AC-8 (startup hardening — example bind matches CGNAT),
AC-10 (supervisor lifecycle — example exercises the full schema).

Action — run exactly one command:
  /speckit-specify "deploy/examples/supervisors/example-daemon.toml: fully commented, fully generic supervisor TOML using placeholder secret names; validates against SDD-18 loader; references docs/TAILSCALE-ACLS.md and docs/CLEAN-MACHINE.md in comments; both pre-existing docs re-verified for accuracy; zero operator-specific names anywhere"

The before_specify hook will create branch 030-examples-and-tailscale.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-30 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-30.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-30 (example supervisor TOML
+ doc verification) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; I/VI load-bearing)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (Supervisor Config File — every documented field MUST appear in the example, with comments matching the schema's prose)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (multi-daemon pattern — operator workflow context for the comments)
- /Users/mrz/projects/hush/docs/TAILSCALE-ACLS.md  (cross-reference target)
- /Users/mrz/projects/hush/docs/CLEAN-MACHINE.md  (cross-reference target)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (deploy/ entry from SDD-29)
- /Users/mrz/projects/hush/docs/sdd/SDD-30.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- deploy/examples/supervisors/example-daemon.toml (NEW)
- docs/TAILSCALE-ACLS.md (verify-and-polish — already exists)
- docs/CLEAN-MACHINE.md (verify-and-polish — already exists)
- One test: TestExamples_GenericTOMLValidates added to
  internal/supervise/config/example_test.go (//go:build !nointegration
  or just under the normal test pkg).

Implementation contract (HOW — locked):
- example-daemon.toml structure follows docs/CONFIG-SCHEMA.md
  Supervisor Config section exactly:
    name = "example-daemon"
    [child]
      command = ["/usr/local/bin/your-daemon", "--flag"]
      env = ["EXAMPLE_API_KEY_1", "EXAMPLE_API_KEY_2"]
    [discord]
      audit_channel_id = "REPLACE_ME"
    [validators.example_api_key_1]
      type = "anthropic"
    [watchdog]
      patterns = [
        { name = "rate-limit", regex = "(?i)rate.limit", rate_limit = "5m" },
      ]
    [grace]
      window = "30m"
      cache_secrets_for_restart = true
    refresh_window = "03:00-05:00"
    boot_retry_timeout = "10m"
    dm_rate_limit = "5m"
- Every field gets a header comment explaining its purpose,
  citing docs/CONFIG-SCHEMA.md for the full spec. The file's
  top-of-file comment block links to docs/TAILSCALE-ACLS.md
  and docs/CLEAN-MACHINE.md.
- The validation test loads the example via supervise/config.Load
  and asserts no error.
- TAILSCALE-ACLS.md verification: cross-check against
  docs/SECURITY.md Layer 0 (network) and docs/CONFIG-SCHEMA.md
  listen_addr — flag any divergence.
- CLEAN-MACHINE.md verification: cross-check the install steps
  against deploy/install.sh (SDD-29) — flag any divergence.

Coverage target: N/A. Gate: example validates; cross-doc check
flags zero divergences.
Constitutional principles in scope: I, VI.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-30 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-30.md.

Run:
  /speckit-tasks "Tasks: write TestExamples_GenericTOMLValidates BEFORE writing the example file (test fails until file exists, then passes). Then write deploy/examples/supervisors/example-daemon.toml. Then verify docs/TAILSCALE-ACLS.md is accurate (compare to docs/SECURITY.md Layer 0 + docs/CONFIG-SCHEMA.md listen_addr). Then verify docs/CLEAN-MACHINE.md is accurate (compare to deploy/install.sh from SDD-29). Final tasks: TestExamples_NoOperatorSpecificNames (grep the example for any name that looks operator-specific — should match zero). Final phase MUST include magex format:fix, magex lint, magex test:race."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-30 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-30.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Confirm TestExamples_GenericTOMLValidates passed.
3. Confirm TestExamples_NoOperatorSpecificNames passed.
4. Manual cross-doc check:
     - docs/TAILSCALE-ACLS.md is consistent with docs/SECURITY.md
       Layer 0 + docs/CONFIG-SCHEMA.md listen_addr.
     - docs/CLEAN-MACHINE.md is consistent with
       deploy/install.sh (SDD-29).
   Note any divergence in the final message.
5. Confirm the example file references docs/TAILSCALE-ACLS.md
   and docs/CLEAN-MACHINE.md in its top-of-file comment.
6. Append a "Exported API — locked at SDD-30" extension to the
   deploy/ entry in docs/PACKAGE-MAP.md noting
   deploy/examples/supervisors/example-daemon.toml as the
   canonical operator-facing template.
7. Update docs/AC-MATRIX.md AC-6, AC-8, AC-10 rows with the new
   example file path.
8. Mark SDD-30 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add deploy/examples/ docs/TAILSCALE-ACLS.md docs/CLEAN-MACHINE.md \
          docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          internal/supervise/config/ specs/<feature-dir>/tasks.md
  git commit -m "feat(deploy/examples,docs): generic supervisor template + verify Tailscale ACL + clean-machine docs (SDD-30)"

Final message: confirm gates passed, example validates, no
operator-specific names, both reference docs verified accurate
against current spec/config/install.sh, AC-6/8/10 rows updated,
SDD-PLAYBOOK updated, and the combined commit created.
```
