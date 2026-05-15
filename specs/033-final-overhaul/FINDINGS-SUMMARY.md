SDD-33 FINDINGS SUMMARY (final repo + docs overhaul)
====================================================

Total: 16 findings
  by severity:  critical=0  major=6  minor=10
  by category:
    A — code audit               :  2  (A2=2 dead-export sentinel errors documented via manifest)
    B — PACKAGE-MAP              :  0  (no signature drift detected; consolidation deferred to manifest mechanism)
    C — AC-MATRIX                :  0  (98 cited test paths, all extant)
    D — ARCHITECTURE             :  4  (4 description-clarity edits in §4 Components table)
    E — TESTING-STRATEGY         :  0  (all 6 fuzz targets present)
    F — README                   :  5  (badge URL, 2 dependency claims, missing Quick start, Spec-Kit mischar)
    G — IMPLEMENTATION-PLAN/PLAYBOOK :  2  (duplicate 026 dir, stale SDD-25 row)
    H — specs/ archive           :  0  (policy was pre-locked; F-001 covers the rename)
    I — drift script             :  1  (script absent — delivered via T036)
    J — operator-name leak       :  0  (zero matches whole-tree)
    K — constitution recompliance:  2  (sec-crit byte-equality verified PASS; integration sentinel red baseline)

  by disposition:
    resolved              : 15
    converted-to-issue    :  0  (gh auth broken on operator setup; no issues created — see ENVIRONMENTAL CAVEAT)
    deferred-to-followup  :  1  (F-015 SDD-25 sentinel scenarios — not in SDD-33 scope)

CRITICAL FINDINGS: 0
  → SDD-33 is unblocked from completion.

MAJOR FINDINGS unresolved-but-converted-to-issues: 0
  All 6 major findings (F-003 F-004 F-005 F-006 F-008 F-009) resolved
  inline by editing the offending file.

OPERATOR-NAME LEAK CHECK: PASS (0 matches in committed tree, whole-tree
  Go test `TestExamples_NoOperatorSpecificNames_WholeTree` green).

DRIFT SCRIPT (`scripts/check-package-map-vs-code.sh`): exit 0 against
  as-shipped tree (19 packages, 472 exported symbols, 0 drift).
  Self-test PASS: stub injection produced exit 1 + correct output;
  cleanup restored exit 0.

SDD-31 CI GATES (re-run locally):
  - magex format:fix  : PASS
  - magex lint        : PASS (golangci-lint 0 issues, go vet PASS)
  - magex test:race   : PASS for base tests; integration tests fail
                        loudly per FR-001 (sentinel-pending scenarios
                        from SDD-25 — pre-existing red baseline, not a
                        regression introduced by SDD-33).
  - go vet ./...      : PASS (rolled into magex lint)
  - govulncheck/gitleaks : not re-run locally this session (binaries
                           require operator install; SDD-31 gate set is
                           green per playbook row).

SDD-33 STATUS: ready to mark `done` in docs/SDD-PLAYBOOK.md after
final-gate confirmations in Phase 14.

---

## Per-finding detail (mirrors findings.jsonl)

| ID    | Sev   | Cat | Subcat | Disposition | Disposition ref                                              |
|-------|-------|-----|--------|-------------|--------------------------------------------------------------|
| F-001 | minor | G   | —      | resolved    | git mv specs/026-supervisor-orchestration → specs/024-... → archived |
| F-002 | minor | G   | —      | resolved    | docs/SDD-PLAYBOOK.md SDD-25 row updated                      |
| F-003 | major | F   | —      | resolved    | README badge URL → fortress.yml                              |
| F-004 | major | F   | —      | resolved    | README tech-stack → decred/dcrd/*                            |
| F-005 | major | F   | —      | resolved    | README sigil reframed as inspiration                          |
| F-006 | major | F   | —      | resolved    | README §Quick start added                                    |
| F-007 | minor | F   | —      | resolved    | README Spec-Kit moved out of tech stack                      |
| F-008 | major | A   | A2     | resolved    | ErrChainLocked added to Symbol manifest (kept exported)      |
| F-009 | major | A   | A2     | resolved    | ErrInvalidIssuer added to Symbol manifest (kept exported)    |
| F-010 | minor | D   | —      | resolved    | ARCHITECTURE.md §4 footnote covering 11 supporting packages  |
| F-011 | minor | D   | —      | resolved    | ARCHITECTURE.md Supervisor row clarified                     |
| F-012 | minor | D   | —      | resolved    | ARCHITECTURE.md Audit-log row corrected to internal/audit    |
| F-013 | minor | D   | —      | resolved    | ARCHITECTURE.md Discord row clarified (audit→discord direction) |
| F-014 | minor | I   | —      | resolved    | scripts/check-package-map-vs-code.sh + check-no-operator-names.sh |
| F-015 | minor | K   | —      | deferred    | SDD-25 chunk 3+ (out of SDD-33 scope)                        |
| F-016 | minor | K   | —      | resolved    | constitution byte-equality verification receipt              |

---

## ENVIRONMENTAL CAVEAT — gh auth + operator setup

`gh auth status` fails on the operator setup at SDD-33 implement time
(token in keyring is invalid; reported by `gh` as
"Failed to log in to github.com account mrz1836 (keyring)"). No
findings were converted to GitHub issues because issue creation is not
available in this session.

Per data-model.md disposition rules, this is acceptable: critical
findings are 0, all major findings are `resolved` (no major required
issue creation), and minor findings are `resolved` or
`deferred-to-followup` with explicit chunk targeting.

Per the operator's standing memory (`feedback-defer-ci-wiring`):
neither `scripts/check-package-map-vs-code.sh` nor
`scripts/check-no-operator-names.sh` was wired into CI by this chunk.
Both are runnable repo-local gates; CI wiring is deferred until the
v0.1.0 end-to-end flow is independently verified on operator setup.

Per the operator's standing memory (`project-v010-unproven`): the
README §"Quick start" section added in T028 carries an explicit
"v0.1.0 unproven on a freshly-built operator setup" disclaimer at the
top, so a new reader is not misled into thinking the documented happy
path has been independently smoke-tested.
