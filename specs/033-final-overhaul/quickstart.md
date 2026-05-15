# Quickstart — SDD-33 Final Repo + Docs Overhaul

This file walks an operator (or future Claude session) through the
SDD-33 audit-then-fix workflow end-to-end, from `/speckit-tasks` to
the combined commit. It is the operator-runnable companion to
[plan.md](./plan.md), [research.md](./research.md), and
[data-model.md](./data-model.md).

The full chunk contract is `docs/sdd/SDD-33.md` (Prompts 4 and 5
matter for this quickstart). This quickstart compresses Prompt 5 into
a checklist a human can follow when /speckit-implement falls behind
or needs operator intervention.

---

## Prerequisites

Confirm before starting `/speckit-implement`:

1. On branch `033-final-overhaul`. `git status` clean.
2. `magex format:fix && magex lint && magex test:race` all green
   against the as-shipped tree (the chunk's audit MUST start from a
   green baseline).
3. `gh` CLI authenticated (`gh auth status` → green) — needed for
   converting deferred TODOs to issues per FR-003.
4. The two pre-findings from [research.md](./research.md) §"Cross-cutting findings"
   (F-PRE-2 duplicate `026-` directory; F-PRE-3 stale SDD-25
   playbook narrative) are noted — they become the first two real
   findings (`F-001`, `F-002`).

---

## Step 1 — Audit, in category order A → K

Walk the audit categories in the order documented in
`docs/sdd/SDD-33.md` Prompt 5. For each category, perform the audit
step, then record findings to `specs/033-final-overhaul/findings.jsonl`
matching the schema in [contracts/audit-findings.md](./contracts/audit-findings.md).

### A — Code audit (per package)

```bash
# A.1 — go doc per package, compare to PACKAGE-MAP.md
for pkg in $(go list ./internal/...); do
  go doc -short -all "$pkg" > "/tmp/godoc-$(basename "$pkg").txt"
done
# Diff each against PACKAGE-MAP.md's section for that package.

# A.2 — find dead exports
for pkg in $(go list ./internal/...); do
  base=$(basename "$pkg")
  for sym in $(go doc -short "$pkg" | awk '{print $2}'); do
    grep -rn "${base}\.${sym}\b" --include='*.go' . \
      | grep -v "internal/${base}/" | grep -v "_test.go" \
      || echo "DEAD-EXPORT: $pkg.$sym"
  done
done

# A.3 — TODO / FIXME / XXX
grep -rn 'TODO\|FIXME\|XXX' --include='*.go' internal/ cmd/

# A.4 — naming consistency
for term in 'Approver Approval' 'Refresh Refill' 'Validator Validate'; do
  set -- $term
  echo "=== Comparing $1 vs $2 ==="
  grep -rn "$1\|$2" --include='*.go' internal/ cmd/ \
    | grep -v "_test.go" | sort
done
```

For each finding: append a JSON record to `findings.jsonl` per the
[contract](./contracts/audit-findings.md). Apply the disposition rule
from [data-model.md](./data-model.md) Entity 1.

### B — PACKAGE-MAP.md reorganisation

Reorganise per [research.md R-002](./research.md): one `## \`internal/<pkg>\``
section per package, sub-sections `Types` / `Functions` / `Constants`
/ `Variables` / `Sentinel errors`, footer `*(Originally locked across
SDD-NN, SDD-MM, ...)*`. Drop empty sub-sections. Preserve every cell
of content; only the **arrangement** changes.

### C — AC-MATRIX.md test-path verification

```bash
# Extract every cited test path from AC-MATRIX.md and check existence:
grep -oE 'internal/[a-zA-Z0-9_/]+/[a-z_]+_test\.go' docs/AC-MATRIX.md \
  | sort -u | while read path; do
    test -f "$path" || echo "MISSING: $path"
  done
```

For each missing path: locate the equivalent test (likely renamed)
and update the row. If no equivalent exists → critical finding,
STOP, surface the gap.

### D — ARCHITECTURE.md diagram verification

```bash
# Generate the as-built import graph:
go list -f '{{.ImportPath}} {{.Imports}}' ./... > /tmp/imports.txt
# Cross-check against the ASCII diagram in docs/ARCHITECTURE.md §3.
```

Update the diagram to add missing packages or arrows. The diagram
style is hand-edited ASCII; preserve that style.

### E — Fuzz-target verification

```bash
for name in FuzzVaultDecode FuzzJWTValidate FuzzECIESDecrypt \
            FuzzVerifyRequest FuzzServerTOML FuzzSuperviseTOML; do
  count=$(grep -r "func $name" --include='*.go' . | wc -l | tr -d ' ')
  if [ "$count" != "1" ]; then
    echo "BAD: $name has $count matches (expected 1)"
  fi
done
```

Per [research.md F-PRE-1](./research.md), all six are present at plan
time — re-verify against as-shipped tree.

### F — README.md surgical-edit pass

Read `README.md`. Cross-check each claim against:
- `docs/SPEC.md` (subcommands, flags, ACs)
- `docs/SECURITY.md` (security guarantees)
- `docs/CONFIG-SCHEMA.md` (config field references)
- The actual binary (`./hush --help`, `./hush serve --help`, etc.)

Apply targeted edits per [research.md R-006](./research.md). The
**critical addition** is a quick-start section satisfying FR-010 — a
fresh reader following only the README MUST be able to complete one
end-to-end install + round-trip.

After the edit, perform the manual quick-start dry-run:

```bash
# Fresh shell. Follow README from §"Quick start" verbatim.
# Goal: hush serve + hush request round-trip, no other doc consulted.
# Note any step that requires a referenced doc as a blocking prerequisite.
```

Failure = README rewrite incomplete; loop on the relevant section.

### G — IMPLEMENTATION-PLAN.md actuals

```bash
git log --oneline master | grep -E 'feat|fix|chore' \
  | awk '{print $1, $2, $3, $4, $5}' > /tmp/git-log.txt
```

Cross-reference against `docs/IMPLEMENTATION-PLAN.md` planned order
and `docs/SDD-PLAYBOOK.md` status table. Update IMPLEMENTATION-PLAN.md
to add an "Actual delivery order" subsection per phase, naming any
chunk that ran out of order or was activated unexpectedly (SDD-24
mid-cycle activation by SDD-25 is the canonical example).

### H — specs/ archive migration

```bash
mkdir -p specs-archive
git mv specs/001-keys-derivation specs-archive/
git mv specs/002-securebytes specs-archive/
# ... (32 directories total; the in-flight 033-final-overhaul stays put)
git status   # verify the rename was detected as moves, not deletions
```

Update or create `CONTRIBUTING.md` per [research.md R-005](./research.md).

### I — Drift-detection script

Author `scripts/check-package-map-vs-code.sh` per the
[contract](./contracts/check-package-map-vs-code.md). Run the
self-test recipe from the script's header comment block. Run the
script against the as-shipped tree — exit 0 required.

```bash
chmod +x scripts/check-package-map-vs-code.sh
scripts/check-package-map-vs-code.sh
echo "exit code: $?"   # expect 0
```

### J — Operator-name whole-tree scan

Extend `internal/supervise/config/example_test.go` per
[research.md R-004](./research.md). Add the new test function
`TestExamples_NoOperatorSpecificNames_WholeTree`. Run:

```bash
magex test:race -run TestExamples_NoOperatorSpecificNames_WholeTree ./internal/supervise/config/...
```

Required: PASS. (The seed list is empty at plan time, so the test
trivially passes; the value is the structural gate against future
leaks.)

### K — Constitution recompliance

Walk every principle (I–XI) one final time against the as-built
code (post-fixes). The Constitution Check in [plan.md](./plan.md)
already documents the expectation per principle; this step
**re-confirms** by reading the as-fixed code and recording any
residual finding.

If any principle fails recompliance → critical finding → resolve
before chunk completes.

---

## Step 2 — Run the gates

```bash
magex format:fix
magex lint
magex test:race
magex test:race -tags=integration
scripts/check-package-map-vs-code.sh
go vet ./...
govulncheck ./...
gitleaks detect
```

All must exit 0. Any failure → fix root cause, do NOT bypass.

---

## Step 3 — Render and commit

Render the FINDINGS SUMMARY per
[contracts/audit-findings.md](./contracts/audit-findings.md). Mark
SDD-33 status `done` in `docs/SDD-PLAYBOOK.md`.

Make ONE combined commit (or split per category if the diff is too
large to review — operator preference, document the choice in the
commit message):

```bash
git add -A
git status     # review carefully before committing
git commit -m "chore: final repo + docs overhaul (SDD-33)

Categories addressed:
- A: dead exports, naming consistency, TODO sweep
- B: PACKAGE-MAP.md reconciled into per-package map
- C: AC-MATRIX.md test paths verified
- D: ARCHITECTURE.md diagram matches as-built
- E: TESTING-STRATEGY.md fuzz targets verified
- F: README.md polished + quick-start added
- G: IMPLEMENTATION-PLAN.md updated with actuals
- H: specs/ migrated to specs-archive/
- I: scripts/check-package-map-vs-code.sh added
- J: zero operator-name leaks
- K: constitution recompliance check passed

[Insert FINDINGS SUMMARY block from contracts/audit-findings.md.]

Findings deferred to issues: <list>
"
```

---

## Step 4 — Confirm chunk completion criteria

Before declaring SDD-33 done, all of these MUST be true:

- [ ] Critical findings: 0.
- [ ] Major findings: each one `resolved` or `converted-to-issue`.
- [ ] Minor findings: documented in implement message (resolved /
  converted / deferred).
- [ ] All gate commands from Step 2 exit 0.
- [ ] `scripts/check-package-map-vs-code.sh` exits 0.
- [ ] Manual README quick-start dry-run completes without consulting
  another doc.
- [ ] `docs/SDD-PLAYBOOK.md` SDD-33 status = `done`.
- [ ] `docs/AC-MATRIX.md` rows still cite extant test paths.
- [ ] `findings.jsonl` exists and matches the contract.
- [ ] CONTRIBUTING.md (root) exists and documents the three policies
  per [research.md R-005](./research.md).

If any item is unchecked → STOP, do NOT mark SDD-33 done, surface
the gap and ask the project owner whether to defer (with explicit
risk acknowledgement) or block on resolution.

---

## Hand-off note

After SDD-33 merges, **SDD-32 is unblocked** to cut the v0.1.0
release tag. SDD-32 will:

1. Migrate `specs/033-final-overhaul/` to `specs-archive/` (the one
   directory SDD-33 cannot move itself per [research.md R-003](./research.md)).
2. Run GoReleaser to produce signed binaries.
3. Tag `v0.1.0`.

SDD-33 explicitly does NOT cut the tag — that responsibility
belongs to SDD-32.
