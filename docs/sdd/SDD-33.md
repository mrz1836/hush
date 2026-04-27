# SDD-33 — Final repo + docs overhaul (drift reconciliation, dead-code sweep, README rewrite)

**Phase:** 8
**Package:** repo-wide (no Go package added; this is a sweep)
**Files:** every `internal/*`, every `docs/*`, `README.md`, `PACKAGE-MAP.md`, `AC-MATRIX.md`, `IMPLEMENTATION-PLAN.md`, `ARCHITECTURE.md`, plus `specs/` cleanup decision and a new `scripts/check-package-map-vs-code.sh`
**Branch:** `033-final-overhaul` (created by the `before_specify` git hook)
**Blocked by:** SDD-25 (lifecycle harness — tells you what works end-to-end) + SDD-31 (CI gates locked)
**Blocks:** SDD-32 (release tag — must happen AFTER overhaul so the tag captures clean state)
**Primary AC:** AC-1 (final operator-facing README is the v0.1.0 first impression); indirectly tightens every other AC
**Coverage target:** N/A — this chunk reconciles, it does not add code

**Behaviour contracts (MUST):**
- Audit every `internal/*` package for: dead exported symbols (declared, never imported outside its own tests), drift between `PACKAGE-MAP.md` "Exported API — locked at SDD-NN" sections and actual exported symbols, inconsistent naming patterns across packages, leftover TODO/FIXME/XXX comments (resolve or convert to GitHub issues)
- Audit every `docs/*` file for: drift between documented behavior and implemented behavior, broken cross-doc links, stale version numbers / dates, operator-specific names that may have leaked in
- Rewrite `README.md` from scratch IF needed, reflecting what actually got built (32 chunks may have shifted scope; the original README may overstate or understate)
- Verify the architecture diagram in `docs/ARCHITECTURE.md` still matches the as-built package graph (regenerate if needed)
- Update `docs/IMPLEMENTATION-PLAN.md` to reflect actual delivery order (vs planned) — flag any chunks that ran out of order or were deferred
- Reconcile `docs/PACKAGE-MAP.md` from the appended-per-chunk format into a single coherent map of the as-built code
- Confirm `docs/AC-MATRIX.md` is fully populated and every AC has a primary test path that still exists
- Confirm every fuzz target listed in `docs/TESTING-STRATEGY.md` actually exists in the code (`grep -r "func Fuzz"`)
- `specs/` directory cleanup: decide whether to keep the per-feature `spec.md` / `plan.md` / `tasks.md` artifacts in-tree (they're useful history) or archive them to a `specs-archive/` directory or `.gitignore` them entirely; document the decision

**Anti-contracts (MUST NOT):**
- Add new features under the guise of "polish" (in scope: rename, remove, document, fix typos; out of scope: new behavior)
- Change any public API (would break the SDD-31 release gates and force re-running every chunk's tests against the change)
- Delete tests (only consolidate if literal duplicates)
- Silently drop documentation (a dropped doc must be replaced by an equivalent or better explanation elsewhere)
- Tag v0.1.0 — that is SDD-32's job

**Tests required:**
- The full CI suite (every gate from SDD-31) must still be green after the overhaul. That is the test.
- One new check: `scripts/check-package-map-vs-code.sh` — diffs the union of `PACKAGE-MAP.md` "Exported API — locked at SDD-NN" sections against actual exported symbols (`go doc ./...`). Fails on drift.
- Manual operator quick-start dry-run: a fresh reader can follow the rewritten README to a working install.

**Constitutional principles in scope:** I (operator-agnostic remains true after the sweep), VIII (no test deleted that protects an AC; coverage gate from SDD-31 still passes), and a meta-application of every principle (this chunk asks "is the as-built code still constitutionally compliant?" one more time)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- This chunk RECONCILES PACKAGE-MAP.md into its final form; it does not add a new locked API. The final PACKAGE-MAP.md becomes the single source of truth as v0.1.0 tag captures it.

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

This is the "second-to-last" chunk by design — it MUST run after
SDD-31 (CI gates locked) and BEFORE SDD-32 (v0.1.0 tag). The point
is to catch the drift that 32 incremental chunks accumulate.

The Implement session is intentionally large; pace yourself and
work through the audit list one section at a time.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-33 (final repo + docs
overhaul) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (entire — final compliance check; this overhaul re-asks "is the codebase still constitutional?")
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current state — every row should already be green from SDD-25/31)
- /Users/mrz/projects/hush/docs/SDD-PLAYBOOK.md  (every chunk SDD-01..31 should be `done` or `skipped`)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (currently a sequence of "Exported API — locked at SDD-NN" sections — you'll reconcile this into a single coherent map)
- /Users/mrz/projects/hush/docs/IMPLEMENTATION-PLAN.md  (planned delivery order — compare to actual git history)
- /Users/mrz/projects/hush/docs/ARCHITECTURE.md  (architecture diagram — verify against the as-built package graph)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md  (the 6 fuzz target list — confirm each exists in code)
- /Users/mrz/projects/hush/README.md  (current state — rewrite if it no longer matches what got built)
- /Users/mrz/projects/hush/docs/sdd/SDD-33.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
Thirty-two incremental chunks accumulate drift. Names diverge.
Documentation lags. Exported API gets added or renamed without
PACKAGE-MAP catching up. README claims behavior the code doesn't
quite match. Specs may have been clarified by chunks that the spec
doc never absorbed. SDD-33 is the deliberate sweep that
reconciles all of it into a clean, coherent state ready for the
SDD-32 v0.1.0 tag.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- Every exported symbol in every internal/* package is either
  (a) used by another internal/* or cmd/hush package, OR
  (b) listed in PACKAGE-MAP.md under that package's locked API.
  Symbols that are neither are dead and MUST be removed.
- Every entry in docs/PACKAGE-MAP.md "Exported API — locked at
  SDD-NN" sections matches an actual exported symbol with the
  documented signature. Drift in either direction (extra symbol
  in code, extra entry in doc) MUST be reconciled.
- Every fuzz target listed in docs/TESTING-STRATEGY.md exists
  in the code with the documented name (FuzzVaultDecode,
  FuzzJWTValidate, FuzzECIESDecrypt, FuzzVerifyRequest,
  FuzzServerTOML, FuzzSuperviseTOML).
- Every AC-1..AC-10 row in docs/AC-MATRIX.md cites a primary
  test path that still exists in the repo at the cited path.
- The architecture diagram in docs/ARCHITECTURE.md matches
  the as-built package graph (no orphan packages, no missing
  arrows for actual import edges).
- README.md, when followed by a fresh reader, leads to a
  working hush install. Statements about features that didn't
  ship are removed. Features that DID ship but aren't mentioned
  are added.
- Operator-specific names: zero leakage anywhere in the
  committed tree (re-run the SDD-30 check broadly).
- This chunk MUST NOT change public API or add new behavior.
  It removes, renames (only if breaking change is intentional
  and re-documented), documents, and fixes.

The spec MUST NOT encode HOW (no specific tooling choice for the
audit beyond stdlib `go doc` + `grep`). Those are plan-phase.

Acceptance criterion: AC-1 (the rewritten README is the v0.1.0
operator-facing surface); indirectly tightens every other AC by
ensuring the documentation matches the implementation.

Action — run exactly one command:
  /speckit-specify "final repo + docs overhaul: audit every internal/* for dead exports + naming drift + leftover TODOs; reconcile PACKAGE-MAP.md against actual exported symbols; verify every fuzz target in TESTING-STRATEGY.md exists; verify every AC-MATRIX test path still exists; verify ARCHITECTURE diagram matches as-built; rewrite README.md to reflect what actually got built; zero operator-specific names anywhere; no new behavior, no public API changes"

The before_specify hook will create branch 033-final-overhaul.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-33 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-33.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-33 (final repo + docs overhaul)
of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; the overhaul itself must be constitutionally compliant)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md
- /Users/mrz/projects/hush/docs/SDD-PLAYBOOK.md
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md
- /Users/mrz/projects/hush/docs/IMPLEMENTATION-PLAN.md
- /Users/mrz/projects/hush/docs/ARCHITECTURE.md
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md
- /Users/mrz/projects/hush/README.md
- Every other file under /Users/mrz/projects/hush/docs/  (skim)
- /Users/mrz/projects/hush/docs/sdd/SDD-33.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope (each item below becomes one or more tasks in SDD-33's tasks.md):

A. Code audit (per package)
   1. For each internal/* package, run `go doc ./internal/<pkg>`;
      compare against PACKAGE-MAP.md "Exported API — locked at
      SDD-NN" entries for that package. Note every drift
      (missing in doc, missing in code, signature mismatch).
   2. For each exported symbol, grep usage:
        grep -rn "<pkg>.<Symbol>" --include='*.go' .
      Symbols with zero non-test usages outside their own
      package are candidates for deletion.
   3. Grep for TODO / FIXME / XXX in internal/* and cmd/hush:
        grep -rn 'TODO\|FIXME\|XXX' --include='*.go' internal/ cmd/
      For each: resolve in this chunk OR convert to a GitHub
      issue (gh issue create) and replace with a // see #N
      comment. Document the disposition in the implement
      message.
   4. Naming consistency: pick a small set of "Should-be-
      named-the-same" patterns (e.g. Approver vs Approval,
      Refresh vs Refill) and grep for each across packages.
      Rename ONLY if the rename is locally safe AND breaking
      is acceptable (a renamed exported symbol means breaking
      change, only do it if PACKAGE-MAP.md drift demands it).
B. PACKAGE-MAP.md reconciliation
   5. Currently PACKAGE-MAP.md is a series of appended
      "Exported API — locked at SDD-NN" sections. Re-organize
      into a single coherent per-package map: each package gets
      one section listing its locked API, with a footer
      "(originally locked across SDD-NN, SDD-MM, ...)". Keep
      the historical chunk attribution as metadata.
C. AC-MATRIX.md verification
   6. For every row AC-1..AC-10: confirm the cited primary test
      path exists. If not, find the equivalent test in the
      current code and update the row. If no equivalent test
      exists, that's a real gap — STOP and surface it.
D. ARCHITECTURE.md verification
   7. Generate the as-built package import graph (e.g.
      `go list -f '{{.ImportPath}} {{.Imports}}' ./...`)
      and compare to the diagram in ARCHITECTURE.md. Update
      the diagram if drift is found. Tools: anything from
      Mermaid to a hand-edited ASCII diagram — match what's
      already in the doc.
E. TESTING-STRATEGY.md fuzz target verification
   8. For each fuzz target name in TESTING-STRATEGY.md §2,
      confirm `grep -r "func Fuzz<Name>"` finds it. Missing
      target → real gap, surface it.
F. README.md rewrite (if needed)
   9. Read the current README. Cross-check every claim against
      the as-built code:
       - Does the quick-start work?
       - Are all listed flags real?
       - Are the supported OSes accurate?
       - Are the security guarantees consistent with
         docs/SECURITY.md?
      Rewrite from scratch if the gap is wide; surgical edit
      if the gap is small.
G. IMPLEMENTATION-PLAN.md actual-vs-planned
   10. Read git log on master (or `gh run` history) and update
       IMPLEMENTATION-PLAN.md with the actual chunk delivery
       order. Note any chunk that was deferred, skipped (SDD-24),
       or activated unexpectedly.
H. specs/ directory cleanup decision
   11. The specs/ directory contains 32+ subdirectories of
       spec.md/plan.md/tasks.md from the 5-prompt sessions.
       Decide:
         a. Keep in-tree as historical record.
         b. Move to specs-archive/ as historical record.
         c. .gitignore (keep on disk locally but not in repo).
       Document the decision in CONTRIBUTING.md.
I. New tooling
   12. Write scripts/check-package-map-vs-code.sh that
       automates step A.1 (PACKAGE-MAP vs go doc diff). Wire
       into CI as a non-blocking warning step (or blocking
       if you're confident).
J. Operator-specific name leak check
   13. Re-run the SDD-30 operator-name allowlist over the WHOLE
       tree (not just deploy/examples/). Zero matches required.
K. Final compliance check
   14. Re-evaluate every Constitution principle against the
       as-built code. Note any drift in the implement message.

Implementation contract (HOW — locked):
- The audit produces a list of FINDINGS. Each finding has:
    - Severity (critical / major / minor)
    - Category (one of A..K above)
    - Location (file path)
    - Action taken (delete / rename / document / convert-to-issue)
- Critical findings BLOCK the chunk completing — they must be
  resolved before the implement session ends.
- Major findings are resolved in this chunk OR converted to
  GitHub issues (referenced in the implement message).
- Minor findings can ride to a follow-on chunk; document in
  the implement message.
- The implement session produces ONE combined commit covering
  all reconciliations. If the diff is too large to review,
  split by category (one commit per A..K) — operator preference
  documented in the implement message.

Coverage target: N/A. Gate: full CI green after overhaul; new
scripts/check-package-map-vs-code.sh passes; operator-name leak
check is zero.
Constitutional principles in scope: I (operator-agnostic), VIII
(no test deleted that protects an AC), and a meta-application of
every principle.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-33 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-33.md.

Run:
  /speckit-tasks "Tasks (audit-then-fix loop). The audit categories A..K from the plan each become a task block. Audit tasks come first (produce the FINDINGS list); fix tasks follow (one fix task per finding, or one batched fix task per category). Specific tasks required: A1 audit-internal-exports (go doc per package), A2 audit-symbol-usage (grep), A3 audit-todo-fixme-xxx, A4 audit-naming-consistency, B5 reorganize-package-map, C6 verify-ac-matrix-paths, D7 verify-architecture-diagram, E8 verify-fuzz-targets, F9 rewrite-readme-if-needed (with manual quick-start dry-run sub-task), G10 update-implementation-plan-actuals, H11 decide-specs-cleanup-policy, I12 write-check-package-map-vs-code-script (with self-test), J13 operator-name-leak-check-whole-tree, K14 constitution-recompliance-check. Plus one summary task: SUMMARY produce-findings-report. Final phase MUST include magex format:fix, magex lint, magex test:race, magex test:race -tags=integration, AND scripts/check-package-map-vs-code.sh."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-33 of the hush project.

This is the largest verify-and-clean session in the project. Pace
yourself; work through the audit categories A..K from the plan in
order. Produce a FINDINGS list as you go.

Read /Users/mrz/projects/hush/docs/sdd/SDD-33.md.

Run: /speckit-implement

Then walk the audit categories from the plan. For each category:
1. Run the audit step.
2. Record findings (severity / location / proposed action).
3. Apply fixes for critical and major findings; defer minor to
   GitHub issues.

After /speckit-implement and the audit categories complete, do
these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Integration tests:
     magex test:race -tags=integration
3. New audit script:
     scripts/check-package-map-vs-code.sh
   Must exit 0.
4. Operator-name leak check (whole tree):
     scripts/check-no-operator-names.sh
   Must exit 0.
5. Manual: README.md quick-start dry-run on a fresh shell
   (or VM/container if available). Note any step that fails.
6. Re-run the SDD-31 CI gates locally if possible:
     go vet ./... && govulncheck ./... && gitleaks detect
7. Confirm docs/AC-MATRIX.md is fully green AND every cited
   test path exists in the current tree.
8. Confirm docs/SDD-PLAYBOOK.md status table is clean (every
   chunk done or skipped).
9. Mark SDD-33 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit (or split per category if the diff is too
large to review; note your choice in the final message):

  git add -A   (be careful — review with git status first)
  git commit -m "chore: final repo + docs overhaul (SDD-33)

Categories addressed:
- A: dead exports, naming consistency, TODO sweep
- B: PACKAGE-MAP.md reconciled into per-package map
- C: AC-MATRIX.md test paths verified
- D: ARCHITECTURE.md diagram matches as-built
- E: TESTING-STRATEGY.md fuzz targets verified
- F: README.md rewritten/polished
- G: IMPLEMENTATION-PLAN.md updated with actuals
- H: specs/ cleanup policy decided
- I: scripts/check-package-map-vs-code.sh added
- J: zero operator-name leaks
- K: constitution recompliance check passed

Findings deferred to issues: <list any GitHub issue numbers>
"

Final message:
- Confirm every gate from steps 1–8 passed.
- Print the FINDINGS summary: total, by severity, by category.
- List any GitHub issues created for deferred findings.
- Confirm SDD-33 marked done in SDD-PLAYBOOK.
- Confirm the repo is now clean and ready for SDD-32 to cut
  the v0.1.0 tag.
- If any critical finding could NOT be resolved in this chunk,
  STOP — do NOT mark SDD-33 done — surface the gap and ask the
  project owner whether to defer (with explicit risk
  acknowledgement) or block on resolution.
```
