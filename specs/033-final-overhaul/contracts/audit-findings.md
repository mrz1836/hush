# Contract — Audit FINDINGS list

The structured output of the SDD-33 audit phase. Each finding is one
record matching the `Finding` entity in [data-model.md](../data-model.md).
This file pins the **on-disk format** so /speckit-implement can produce
the list in a uniform, parseable shape and so future chunks can grep
across past findings.

---

## On-disk location

`specs/033-final-overhaul/findings.jsonl` — JSONL (one JSON object per
line). Created and populated during /speckit-implement, NOT by
/speckit-plan.

After SDD-33 completes and merges, the file is migrated to
`specs-archive/033-final-overhaul/findings.jsonl` along with the rest
of the chunk's spec artefacts (per FR-012 / R-003).

---

## Record shape (one JSON object per line)

```json
{
  "id": "F-001",
  "severity": "minor",
  "category": "G",
  "subcategory": null,
  "location": "specs/026-supervisor-orchestration/, specs/026-validators-builtins/",
  "description": "Two `specs/` subdirectories share the chunk-ID prefix `026-` (validators-builtins and supervisor-orchestration). Per IMPLEMENTATION-PLAN.md actuals, SDD-26 is the validators chunk and SDD-24 is the orchestration chunk — the orchestration directory was misnamed during /speckit-specify.",
  "disposition": "resolved",
  "disposition_ref": "renamed `specs/026-supervisor-orchestration/` to `specs/024-supervisor-orchestration/` via `git mv`",
  "discovered_at": "2026-05-15T12:34:56Z"
}
```

### Field constraints (from data-model.md Entity 1)

- `id`: `^F-\d{3,4}$` — zero-padded counter, assigned in audit order.
- `severity`: one of `"critical"`, `"major"`, `"minor"`.
- `category`: one of `"A"`, `"B"`, `"C"`, `"D"`, `"E"`, `"F"`, `"G"`,
  `"H"`, `"I"`, `"J"`, `"K"` (matching the audit categories).
- `subcategory`: one of `"A1"`, `"A2"`, `"A3"`, `"A4"` for category
  `A`, otherwise `null`.
- `location`: repo-relative path(s); comma-separated for multi-file
  findings; line-suffix `:NNN` permitted but not required.
- `description`: one or two sentences (no internal newlines —
  preserve JSONL parseability).
- `disposition`: one of `"resolved"`, `"converted-to-issue"`,
  `"deferred-to-followup"`.
- `disposition_ref`: free-form string; conventional values are
  GitHub issue refs (`"#42"`), short commit SHAs (`"abc1234"`),
  follow-up chunk IDs (`"SDD-34"`), or `"TBD"`.
- `discovered_at`: RFC3339 timestamp.

---

## Disposition gates (from data-model.md Entity 1)

| Severity | Allowed dispositions | Blocks chunk completion? |
|----------|---------------------|--------------------------|
| `critical` | `resolved` only | YES |
| `major` | `resolved` or `converted-to-issue` | NO (each MUST have one of the two before chunk completes) |
| `minor` | `resolved`, `converted-to-issue`, or `deferred-to-followup` | NO |

---

## Summary report (rendered at end of /speckit-implement)

After the audit completes, /speckit-implement renders a
human-readable summary derived from `findings.jsonl`:

```text
SDD-33 FINDINGS SUMMARY (final repo + docs overhaul)
====================================================

Total: 47 findings
  by severity:  critical=0  major=8  minor=39
  by category:
    A — code audit              : 12  (A1=4 dead, A2=2 usage, A3=5 TODO, A4=1 naming)
    B — PACKAGE-MAP             : 14  (consolidation drift; all major)
    C — AC-MATRIX               :  3  (cited path renamed; all resolved)
    D — ARCHITECTURE            :  2  (one missing edge, one orphan box)
    E — TESTING-STRATEGY        :  0  (all 6 fuzz targets present)
    F — README                  :  6  (badge link, 4 flag descriptions, quick-start added)
    G — IMPLEMENTATION-PLAN     :  3  (actuals diverge from planned order)
    H — specs/ archive          :  1  (32 dirs migrated; 026 collision resolved)
    I — drift script            :  1  (script delivered, self-tested locally)
    J — operator-name leak      :  0  (zero matches whole-tree)
    K — constitution recompliance:  5  (5 minor wording-drift findings; 0 violations)

  by disposition:
    resolved              : 32
    converted-to-issue    :  8  (#101..#108)
    deferred-to-followup  :  7  (target chunk: SDD-34 cleanup pass)

CRITICAL FINDINGS: 0
  → SDD-33 is unblocked from completion.

MAJOR FINDINGS unresolved-but-converted-to-issues:
  F-007  internal/discord  Approver vs Approval rename deferred       #101
  F-019  internal/server   Dead handler removed; reload semantics doc #102
  ... [6 more]

OPERATOR-NAME LEAK CHECK: PASS (0 matches in committed tree).
DRIFT SCRIPT (`scripts/check-package-map-vs-code.sh`): exit 0 against as-shipped tree.
SDD-31 CI GATES (re-run locally): PASS.
SDD-33 STATUS: ready to mark `done` in docs/SDD-PLAYBOOK.md.
```

The exact field counts and IDs are populated by /speckit-implement
based on actual audit output.

---

## Findings list as commit-trailer (informative)

The combined commit message for SDD-33 SHOULD include the FINDINGS
SUMMARY (above) verbatim in the trailer so the merge commit captures
the audit output without requiring readers to fetch
`findings.jsonl`. JSONL stays in the spec artefact for machine
consumption; the markdown summary is the human-facing record.
