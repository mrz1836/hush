# SDD-32 — Open-source release: README + DAEMONS + repo-level OSS files + docs polish + GoReleaser + v0.1.0 tag

**Phase:** 8
**Package:** `docs/` + repo root
**Files:** `README.md` (verify), `docs/DAEMONS.md` (already created — verify), `LICENSE` (verify), `CONTRIBUTING.md` (already created — verify), `CODE_OF_CONDUCT.md` (already created — verify), `SECURITY.md` at repo root (already created — verify), `.github/ISSUE_TEMPLATE/{bug_report.md, feature_request.md}` (already created — verify), `.github/PULL_REQUEST_TEMPLATE.md` (already created — verify), `docs/{ARCHITECTURE.md, API.md, SECURITY.md, OPERATIONS.md}` polish, `.goreleaser.yml` final, git tag `v0.1.0`
**Branch:** `032-release-v010` (created by the `before_specify` git hook)
**Blocked by:** SDD-31, SDD-33 (final overhaul must precede the tag so the tag captures clean state)
**Blocks:** none (final chunk)
**Primary AC:** AC-1
**Coverage target:** N/A

**Behaviour contracts (MUST):**
- `README.md` follows the operator-facing structure documented in the existing `README.md` (verify accuracy on a clean machine)
- Quick-start tested on a fresh macOS or Linux box / VM / container
- `DAEMONS.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, repo-root `SECURITY.md` are accurate and operator-agnostic
- Repo-root `SECURITY.md` does NOT duplicate `docs/SECURITY.md` (different documents — disclosure policy vs threat model)
- All polished docs are operator-agnostic
- Pre-tag check: `docs/AC-MATRIX.md` has every AC-1..AC-10 row marked complete with test paths
- Pre-tag check: every fuzz target in Constitution VIII has a 60s-clean run recorded in CI logs
- v0.1.0 tag is annotated; release notes auto-generated from commit log following conventional-commits
- GoReleaser produces signed artifacts (sigstore/cosign or signed SHA256SUMS minimum)

**Anti-contracts (MUST NOT):**
- Make the repo public (the project owner transitions to public manually)
- Tag v0.1.0 if any AC row is incomplete or any CI gate is red
- Commit any operator-specific names in any OSS deliverable
- Auto-publish to homebrew tap or any package index without explicit project-owner go-ahead
- Duplicate `docs/SECURITY.md` content into the repo-root `SECURITY.md`

**Tests required:**
- Quick-start verification on a clean VM/container (manual)
- Link checker over all `docs/` (CI step or manual run via `markdown-link-check`)
- `git tag --verify v0.1.0` confirms annotation present and signature valid (if signing enabled)

**Constitutional principles in scope:** I (operator-agnostic), VIII (every AC row green is the release gate)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- This is the release-tag chunk; the locked "API" is the v0.1.0 git tag itself + the binary artefacts GoReleaser produces. PACKAGE-MAP entry: a release-notes pointer.

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

This is a verify-and-polish-and-tag chunk — most files already
exist; this chunk closes the loop with the v0.1.0 release tag.
The most important pre-tag check is that EVERY AC-MATRIX row is
complete and CI is green on master.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-32 (open-source release:
README + DAEMONS + repo-level OSS files + docs polish + GoReleaser
+ v0.1.0 tag) of the hush project. This is the final chunk.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (entire — final compliance check)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (must be 100% green before tagging — verify state)
- /Users/mrz/projects/hush/docs/SDD-PLAYBOOK.md  (every chunk SDD-01..31 must be `done` or `skipped`)
- All existing /docs/* files (skim — you'll polish them)
- /Users/mrz/projects/hush/README.md  (already created — verify accuracy)
- /Users/mrz/projects/hush/CONTRIBUTING.md, CODE_OF_CONDUCT.md, SECURITY.md  (already created — verify accuracy)
- /Users/mrz/projects/hush/.github/* templates (already created — verify)
- /Users/mrz/projects/hush/docs/sdd/SDD-32.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
SDD-32 closes v0.1.0. Most files already exist; this chunk verifies
each file is accurate against the current code, polishes the
operator-facing /docs, runs the GoReleaser dry-run, and (only if
every AC row is green and CI is clean on master) cuts the v0.1.0
annotated tag. The repo stays private — the project owner flips
to public manually after this chunk completes.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The v0.1.0 tag MUST NOT be created if any AC-1..AC-10 row in
  docs/AC-MATRIX.md is incomplete OR if any CI gate from SDD-31
  is red on master OR if any chunk in SDD-PLAYBOOK is still
  `pending` or `in-progress` (besides the deliberately-skipped
  SDD-24 unless activated).
- README.md verifies on a clean machine — quick-start steps
  produce a working hush install end-to-end.
- DAEMONS.md, CONTRIBUTING.md, CODE_OF_CONDUCT.md, repo-root
  SECURITY.md are accurate, operator-agnostic, no internal
  project codenames.
- Repo-root SECURITY.md is the disclosure policy (where to
  report a vuln, response SLA). docs/SECURITY.md is the threat
  model. They are NEVER duplicated.
- GoReleaser produces signed artefacts: SHA256SUMS minimum,
  sigstore/cosign preferred.
- The repo MUST stay private — this chunk does NOT toggle the
  GitHub visibility setting.
- This chunk does NOT auto-publish to homebrew or any package
  index; the project owner approves any package-manager push.

The spec MUST NOT encode HOW (no specific markdown structure
beyond what already exists, no specific GoReleaser DSL choices).
Those are plan-phase.

Acceptance criterion: AC-1 (CLI surface — README walks through it).

Action — run exactly one command:
  /speckit-specify "v0.1.0 release: verify and polish README + DAEMONS + repo-root OSS files (CONTRIBUTING, CODE_OF_CONDUCT, SECURITY, .github templates); polish docs/ (link-check, version stamp, operator-agnostic); finalize .goreleaser.yml (signed artefacts); cut annotated v0.1.0 tag ONLY if AC-MATRIX 100% green AND CI green on master AND every chunk done/skipped; repo stays private (owner flips manually); no auto-publish to package indexes"

The before_specify hook will create branch 032-release-v010.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution / existing files.
Otherwise leave the marker — /speckit-clarify will handle it
next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-32 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-32.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-32 (v0.1.0 release) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; I/VIII load-bearing)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (must be 100% green to tag)
- /Users/mrz/projects/hush/docs/SDD-PLAYBOOK.md  (every chunk done/skipped to tag)
- All existing /docs/* files
- /Users/mrz/projects/hush/README.md
- /Users/mrz/projects/hush/CONTRIBUTING.md, CODE_OF_CONDUCT.md, SECURITY.md
- /Users/mrz/projects/hush/.goreleaser.yml
- /Users/mrz/projects/hush/.github/* (templates)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md
- /Users/mrz/projects/hush/docs/sdd/SDD-32.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope (verify-and-polish; most files already exist):
- README.md  (verify quick-start runs on a clean VM/container)
- docs/DAEMONS.md  (verify accurate)
- LICENSE  (verify present at repo root; if missing, ASK the
  project owner before committing a license choice — do NOT
  auto-pick MIT or Apache-2.0)
- CONTRIBUTING.md, CODE_OF_CONDUCT.md, repo-root SECURITY.md
  (verify accurate; polish — operator-agnostic only)
- .github/ISSUE_TEMPLATE/{bug_report.md, feature_request.md},
  .github/PULL_REQUEST_TEMPLATE.md  (verify)
- docs/{ARCHITECTURE.md, API.md, SECURITY.md, OPERATIONS.md}
  polish (link check, version stamp, code-fence sanity, no
  operator-specific names)
- .goreleaser.yml final tweaks (signed checksums)
- git tag v0.1.0 + GoReleaser publish (binary artefacts only —
  NOT public repo flip, NOT homebrew push)

Implementation contract (HOW — locked):
- Verification scripts:
    - scripts/check-ac-matrix.sh: parses docs/AC-MATRIX.md;
      fails if any AC row is incomplete (no test paths cited).
    - scripts/check-playbook.sh: parses docs/SDD-PLAYBOOK.md;
      fails if any row is `pending` or `in-progress`
      (excluding SDD-24 unless activated).
    - scripts/check-no-operator-names.sh: greps all committed
      docs + deploy/ for an operator-private-name allowlist
      (provided as a small known-bad-list); fails on any match.
- Quick-start verification: documented procedure in CONTRIBUTING.md
  for running on a fresh VM/container; a maintainer runs it
  manually and reports.
- Link check: markdown-link-check over docs/ + README.md;
  added as a CI step OR a one-shot manual run noted in the
  release procedure.
- Repo-root SECURITY.md vs docs/SECURITY.md: ensure they are
  semantically distinct documents (disclosure-policy vs threat-
  model). A scripted check could grep for accidental copy-paste
  of the threat-model heading from docs/SECURITY.md into the
  repo-root file.
- LICENSE: if absent, the plan MUST surface this as a question
  for the project owner — do NOT auto-pick.
- .goreleaser.yml signing: sigstore/cosign signature plus signed
  SHA256SUMS file at minimum; release notes auto-generated from
  conventional-commits log.
- Tag: `git tag -a v0.1.0 -m "hush v0.1.0"`. Confirm
  annotation present via `git tag -v v0.1.0`. If GPG signing
  is configured, the signature must be valid.
- The repo visibility flip is OUT OF SCOPE for this chunk.
  Document the flip as a manual step the project owner takes
  AFTER tag.

Coverage target: N/A. Gate: every AC-MATRIX row green; every
playbook row done; CI green on master; quick-start verified
on clean machine.
Constitutional principles in scope: I, VIII.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-32 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-32.md.

Run:
  /speckit-tasks "Tasks (verify-and-polish-and-tag): write the three verification scripts FIRST (check-ac-matrix.sh, check-playbook.sh, check-no-operator-names.sh) so we can run them as gates. Then for each existing file in scope, do a verify task (read it; cross-check against current code/docs/AC) followed by a polish task (apply needed corrections). Quick-start tasks: write a fresh-VM quick-start procedure into CONTRIBUTING.md if not already there; perform the procedure on a clean macOS or Linux VM/container and record the result. Link-check task: run markdown-link-check over docs/ and README.md. Pre-tag verification tasks (MUST all pass before tagging): scripts/check-ac-matrix.sh, scripts/check-playbook.sh, scripts/check-no-operator-names.sh, CI green on master, repo-root SECURITY.md != docs/SECURITY.md content. Final tasks: .goreleaser.yml signing review; git tag -a v0.1.0; goreleaser dry-run produces signed darwin+linux × amd64+arm64 artefacts; verify tag annotation. Final phase MUST include magex format:fix, magex lint, magex test:race, magex test:race -tags=integration."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-32 of the hush project.

This is the FINAL CHUNK. Cut v0.1.0 only after every gate is green.

Read /Users/mrz/projects/hush/docs/sdd/SDD-32.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root
IN ORDER. STOP at any step that fails.

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Integration tests:
     magex test:race -tags=integration
3. AC-MATRIX completeness gate:
     scripts/check-ac-matrix.sh
4. Playbook completeness gate:
     scripts/check-playbook.sh
5. Operator-name leak gate:
     scripts/check-no-operator-names.sh
6. Repo-root SECURITY.md vs docs/SECURITY.md distinctness:
     manual diff confirming they're different documents
7. Quick-start verification on a clean VM/container — record
   the procedure outcome in your final message.
8. Link check:
     markdown-link-check docs/**/*.md README.md
9. CI on master is green for the LATEST commit (verify via
   gh run list --branch master --limit 1).
10. .goreleaser.yml signing review: confirm CGO_ENABLED=0,
    confirm signature config (cosign or signed SHA256SUMS).
11. GoReleaser dry-run:
     goreleaser release --snapshot --skip-publish --clean
    Confirm output: darwin+linux × amd64+arm64 binaries +
    signed checksums.
12. LICENSE confirmation: file present at repo root. If
    absent, STOP and ask the project owner which license.
13. Update docs/AC-MATRIX.md if any row needs final test path
    additions.
14. Update docs/SDD-PLAYBOOK.md: mark SDD-32 status `done`.
15. Append a NEW "release v0.1.0" entry to docs/PACKAGE-MAP.md
    titled "Exported API — locked at SDD-32" pointing to the
    annotated tag.

Only after all 15 steps above pass:

16. Cut the annotated tag:
     git tag -a v0.1.0 -m "hush v0.1.0"
     git tag -v v0.1.0   (confirm annotation; signature if signed)
17. Make one combined commit:
     git add docs/ scripts/ .goreleaser.yml \
             specs/<feature-dir>/tasks.md
     git commit -m "release: hush v0.1.0 (SDD-32)"
18. Push the branch and the tag:
     git push origin 032-release-v010
     git push origin v0.1.0
19. (DO NOT) make the repo public — that is the project
    owner's manual step. (DO NOT) push to homebrew or any
    package index — that is also a manual project-owner step.

Final message:
- Confirm every gate from steps 1–15 passed.
- Confirm the annotated v0.1.0 tag was created and pushed.
- Confirm the GoReleaser dry-run produced the four binary
  variants with signed checksums.
- Explicitly note that the repo is STILL PRIVATE and that the
  project owner must flip visibility manually + make any
  package-index push manually.
- If any gate failed, STOP — do NOT cut the tag, and report
  the specific gate.
```
