---

description: "Tasks for SDD-33 — Final repo + docs overhaul (audit-then-fix loop)"
---

# Tasks: Final Repo + Docs Overhaul (SDD-33)

**Input**: Design documents from `/specs/033-final-overhaul/`

**Prerequisites**: [plan.md](./plan.md) (required), [spec.md](./spec.md) (required for user stories), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/), [quickstart.md](./quickstart.md)

**Tests**: This chunk does **not** add product code, so no new unit tests are written. ONE Go test function (`TestExamples_NoOperatorSpecificNames_WholeTree`) is **extended into** an existing file per [research.md R-004](./research.md); it is in scope as **infrastructure**, not as TDD-for-new-feature. All existing tests MUST stay green (FR-018).

**Organization**: The chunk is an **audit-then-fix loop** across categories A..K (per plan §"Scope"). Audit tasks come FIRST and emit records to `specs/033-final-overhaul/findings.jsonl` per [contracts/audit-findings.md](./contracts/audit-findings.md). Fix tasks follow, organized by user story (US1..US7 from spec.md). Each audit task can produce findings that feed multiple fix tasks; fix tasks reference the originating finding IDs.

## Format: `[ID] [P?] [Story?] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: User story label (US1..US7) — only on fix-phase tasks
- File paths are exact and repo-relative

## Path Conventions

- Single Go module rooted at `/Users/mrz/projects/hush/`
- Source: `internal/*/`, `cmd/hush/`
- Docs: `docs/*.md`, `README.md`, `CONTRIBUTING.md` (NEW)
- Tooling: `scripts/` (NEW directory)
- Spec artefacts: `specs/033-final-overhaul/` (this chunk stays in-place; SDD-NN ≤ 31 migrate to `specs-archive/`)

---

## Phase 1: Setup (baseline + scaffolding)

**Purpose**: Confirm prerequisites and create the audit's input + output scaffolding before any audit step runs.

- [ ] T001 Confirm chunk preconditions per [quickstart.md](./quickstart.md) §"Prerequisites": on branch `033-final-overhaul`, `git status` clean, `gh auth status` green; record any uncommitted artefacts before proceeding
- [ ] T002 [P] Capture green-baseline gates: run `magex format:fix && magex lint && magex test:race` from repo root; STOP if any fails (audit must start from a clean baseline per [quickstart.md](./quickstart.md) §"Prerequisites")
- [ ] T003 [P] Create empty `specs/033-final-overhaul/findings.jsonl` (JSONL file populated by every audit task that follows; schema = [contracts/audit-findings.md](./contracts/audit-findings.md))
- [ ] T004 [P] Capture pre-flight coverage baselines for the 7 security-critical packages per [research.md R-008](./research.md): `magex test:coverrace -run '^Test' ./internal/{keys,vault,vault/securebytes,token,transport/sign,transport/ecies,audit}/...` and record per-package % to `/tmp/sdd33-coverage-baseline.txt` (consumed by Phase 6 dead-export removals)
- [ ] T005 [P] Pre-seed `findings.jsonl` with the two pre-findings already surfaced by Phase 0 research: F-001 (= F-PRE-2 duplicate `specs/026-*` dir, category G, minor) and F-002 (= F-PRE-3 stale SDD-25 narrative in `docs/SDD-PLAYBOOK.md`, category G, minor) per [research.md](./research.md) §"Cross-cutting findings"

**Checkpoint**: Baseline green, `findings.jsonl` exists, pre-flight coverage captured. Audit phase may begin.

---

## Phase 2: Foundational (audit pre-work shared by all categories)

**Purpose**: Generate the machine-readable views of code + docs that downstream audit tasks compare against. Blocks every Phase 3+ task.

- [ ] T006 [P] Generate per-package `go doc -short -all ./internal/<pkg>` output to `/tmp/sdd33-godoc/` (one file per package), enumerating `go list ./internal/...` (the 19 packages listed in [plan.md](./plan.md) §"Scale/Scope")
- [ ] T007 [P] Generate as-built package import graph: `go list -f '{{.ImportPath}} {{.Imports}}' ./... > /tmp/sdd33-imports.txt` (consumed by D7 ARCHITECTURE diagram verification)
- [ ] T008 [P] Build the operator-binary for README verification: `magex build` (consumed by F9 README cross-check — `./hush --help`, `./hush serve --help`, etc.)
- [ ] T009 [P] Read current `docs/PACKAGE-MAP.md` end-to-end, extract every "Exported API — locked at SDD-NN" section's symbol list to `/tmp/sdd33-pkgmap-symbols.txt` keyed by `<package> <symbol>` (consumed by A1, B5, I12)

**Checkpoint**: Machine-readable code + doc snapshots captured. Audit may proceed in parallel across A..K.

---

## Phase 3: AUDIT — User Story 2 (Priority: P2) — Code & PACKAGE-MAP audit

**Goal**: Surface every drift, dead export, leftover TODO, naming inconsistency, and PACKAGE-MAP-vs-code divergence. Output: findings appended to `findings.jsonl`.

**Independent Test**: For any one `internal/*` package, the diff between `/tmp/sdd33-godoc/<pkg>.txt` and the package's PACKAGE-MAP.md section is either zero OR produces one or more findings recorded in `findings.jsonl`.

### Audit tasks for User Story 2

- [ ] T010 [P] [US2] **A1 audit-internal-exports**: For each of the 19 `internal/*` packages, diff `/tmp/sdd33-godoc/<pkg>.txt` against the corresponding "Exported API — locked at SDD-NN" section(s) in `docs/PACKAGE-MAP.md`; emit one finding per divergence (category=A, subcategory=A1, severity per [research.md R-008](./research.md) — security-critical pkgs auto-major, others minor unless dead-symbol-collides-with-FR-005-doc-entry → critical)
- [ ] T011 [P] [US2] **A2 audit-symbol-usage**: For each exported symbol in `/tmp/sdd33-godoc/*.txt`, grep cross-package usage: `grep -rn "<pkg>.<Symbol>" --include='*.go' . | grep -v "internal/<pkg>/" | grep -v "_test.go"`; symbols with zero non-self-pkg non-test matches AND not listed in PACKAGE-MAP.md become dead-export findings (category=A, subcategory=A2) per FR-001 conditions (a)(b)(c)
- [ ] T012 [P] [US2] **A3 audit-todo-fixme-xxx**: `grep -rn 'TODO\|FIXME\|XXX' --include='*.go' internal/ cmd/`; emit one finding per comment (category=A, subcategory=A3, severity=minor by default; severity=major if comment names a missing security check or correctness invariant)
- [ ] T013 [P] [US2] **A4 audit-naming-consistency**: For each candidate pair from [research.md R-007](./research.md) ("Approver vs Approval", "Refresh vs Refill", "Validator vs Validate", "Supervise vs Supervisor", "Server vs Service", "Acquire vs Lock", "Issue vs Mint"), grep across `internal/` + `cmd/`; emit one finding per inconsistency (category=A, subcategory=A4) with the three-condition rename gate from R-007 applied as disposition guidance

### Findings sweep for User Story 2 (post-A1..A4)

- [ ] T014 [US2] **A-sweep finalisation**: Re-read all category-A finding rows in `findings.jsonl`; for every `critical`-severity row, escalate to operator immediately (per data-model.md disposition gate); for every `major`-severity row, decide `resolved` (= apply fix in Phase 6) vs `converted-to-issue` (= `gh issue create` and patch `// see #N`); for minor rows, default to `resolved`-batched-with-category if the touched files cluster, else `deferred-to-followup`

**Checkpoint**: Categories A1..A4 fully audited. PACKAGE-MAP-relevant findings (drift, dead symbols requiring doc update) pre-staged for B5 reorganisation in Phase 6.

---

## Phase 4: AUDIT — User Stories 3..7 + cross-cutting K (parallel audit fan-out)

**Goal**: Audit categories B..K, each producing findings in `findings.jsonl`. These run after Phase 2's snapshots are ready and are largely independent of Phase 3's outputs.

**Independent Test**: Each audit task either appends zero findings (clean) or appends a justified set with severity/location/description per the contract.

### Audit tasks for User Story 3 (drift script)

- [ ] T015 [P] [US3] **I12-audit pre-step**: Confirm `scripts/check-package-map-vs-code.sh` does NOT already exist; the script's absence is the only "finding" for I12 at audit time (category=I, severity=minor, disposition pre-set to `resolved` since Phase 7 delivers the script)

### Audit tasks for User Story 4 (AC-MATRIX + fuzz targets)

- [ ] T016 [P] [US4] **C6 verify-ac-matrix-paths**: Extract every cited test path from `docs/AC-MATRIX.md` via `grep -oE 'internal/[a-zA-Z0-9_/]+_test\.go|tests/[a-zA-Z0-9_/]+\.go'`; for each, `test -f`; emit one finding per missing path (category=C, severity=major by default — but **critical if no equivalent test exists under a rename** per FR-006 and spec User Story 4 acceptance scenario 3); resolution-by-rename-path is `resolved`, no-equivalent-test is `critical` and BLOCKS chunk completion
- [ ] T017 [P] [US4] **E8 verify-fuzz-targets**: For each of the 6 constitutional names from FR-008 (`FuzzVaultDecode`, `FuzzJWTValidate`, `FuzzECIESDecrypt`, `FuzzVerifyRequest`, `FuzzServerTOML`, `FuzzSuperviseTOML`), run `grep -r "func <Name>" --include='*.go' .` and assert exactly one match; emit a critical finding for any missing (category=E, severity=critical) — pre-finding F-PRE-1 says all six present, but re-verify against as-shipped tree

### Audit tasks for User Story 5 (architecture diagram)

- [ ] T018 [P] [US5] **D7 verify-architecture-diagram**: Cross-reference `/tmp/sdd33-imports.txt` against the ASCII component diagram in `docs/ARCHITECTURE.md`; emit one finding per orphan package in the diagram (category=D, severity=minor) and one per missing edge that corresponds to a real import (category=D, severity=major unless the edge is intentionally hidden for clarity, in which case minor + footnote disposition)

### Audit tasks for User Story 6 (operator-name leak)

- [ ] T019 [P] [US6] **J13 audit-dry-run**: Read the current `operatorSpecificForbidden` slice from `internal/supervise/config/example_test.go` (empty `[]string{}` at plan time per [research.md R-004](./research.md)); dry-run-grep over the whole tree excluding `.git/`, `specs-archive/` (doesn't exist yet at audit time — exclude `specs/` for the pre-archive run since most of it migrates in Phase 11), and the test file itself; emit one finding per match (category=J, severity=critical) — the empty-list precondition means zero findings expected

### Audit tasks for User Story 1 (README accuracy)

- [ ] T020 [US1] **F9-audit read-and-cross-check**: Read `README.md` end-to-end; for each claim (subcommand, flag, OS prerequisite, security guarantee, doc link), verify against `./hush --help` / `./hush serve --help` / `./hush request --help` / `docs/SPEC.md` / `docs/SECURITY.md` / `docs/CONFIG-SCHEMA.md`; emit one finding per inaccuracy (category=F, severity=major if it would mislead a fresh operator, minor otherwise); ALSO emit one structural finding if no quick-start section exists per FR-010 (category=F, severity=major, disposition=resolved-in-Phase-5)

### Audit tasks for User Story 7 (history + IMPLEMENTATION-PLAN)

- [ ] T021 [P] [US7] **G10-audit actuals diff**: `git log --oneline master | grep -E 'feat|fix|chore'` to `/tmp/sdd33-git-log.txt`; cross-reference against `docs/IMPLEMENTATION-PLAN.md` "Planned delivery" rows and `docs/SDD-PLAYBOOK.md` status table; emit one finding per chunk that ran out-of-order, was deferred, was activated unexpectedly (SDD-24 mid-cycle activation by SDD-25 is the canonical example), or has stale status (F-002 / F-PRE-3) (category=G, severity=minor)
- [ ] T022 [P] [US7] **H11-audit inventory**: `ls specs/` and count subdirectories; verify the duplicate `026-` collision (F-PRE-2 / F-001 — `026-supervisor-orchestration` is actually SDD-24, `026-validators-builtins` is SDD-26) per [research.md F-PRE-2](./research.md); emit findings for every subdir whose chunk-ID prefix disagrees with `docs/SDD-PLAYBOOK.md` (category=H, severity=minor); the policy decision itself (move-to-`specs-archive/` per Clarification 2026-05-15 Q1) is already locked — no audit finding for the decision, only for the rename collisions

### Audit tasks for cross-cutting K (constitution recompliance)

- [ ] T023 [P] **K14 audit pass — Principle I (operator-agnostic)**: Spot-check `deploy/examples/`, `internal/supervise/config/`, `tests/` for any new operator-specific leaks since SDD-30; emit findings if any (category=K, severity=critical if a real operator name leaked, else minor wording drift); cross-references J13 (T019)
- [ ] T024 [P] **K14 audit pass — Principle VIII (testing discipline)**: Verify the constitutional security-critical-packages fenced block in `.specify/memory/constitution.md` (lines 253-261) is byte-identical to the list in `.github/scripts/coverage-threshold/compute.go`; emit finding if not (category=K, severity=critical — FR-016 byte-equality anchor)
- [ ] T025 [P] **K14 audit pass — Principles II/III/IV/V/VI/VII/IX/X/XI**: One-line per-principle check against the as-built code per [plan.md](./plan.md) Constitution Check table; emit a finding only on actual drift (category=K, severity=minor wording-drift / major actual-violation / critical constitutional-breach); pre-finding expectation = 0 critical, ≤5 minor

**Checkpoint**: All audit categories have appended their findings. `findings.jsonl` now contains the complete drift inventory. Disposition decisions for each finding are recorded. Phase 5+ fix tasks consume this list.

---

## Phase 5: FIX — User Story 1 (Priority: P1) 🎯 MVP — README rewrite + quick-start dry-run

**Goal**: A fresh operator following only `README.md` completes one `hush serve` + `hush request` round-trip without consulting another doc (FR-010, AC-1).

**Independent Test**: From a fresh shell, follow the rewritten `README.md` §"Quick start" verbatim and reach `hush request` returning a payload — no other doc opened as a blocking prerequisite.

### Implementation for User Story 1

- [ ] T026 [US1] **F9 resolve-surgical-or-rewrite decision**: Tally category-F findings count from T020; if ≤15, take surgical-edit path; if >15, escalate to from-scratch rewrite (per [research.md R-006](./research.md)); record decision in `findings.jsonl` disposition_ref of T020's structural finding
- [ ] T027 [US1] **F9 README surgical edit**: Apply per-finding fixes to `README.md` — badge target alignment with `.github/workflows/`, tech-stack section vs `go.mod`, doc-table link verification, architecture excerpt vs `docs/ARCHITECTURE.md`; one commit-staged edit per finding ID; preserve existing prose for unchanged claims (FR-019 — no silent doc drop)
- [ ] T028 [US1] **F9 README quick-start section add**: Insert a §"Quick start" section into `README.md` documenting (a) prerequisites (Tailscale account, Discord bot), (b) `magex build` (or equivalent), (c) minimal `hush.toml` example, (d) `hush serve` startup, (e) `hush request <secret>` round-trip; preserve the existing threat-model + security-layers prose untouched
- [ ] T029 [US1] **F9-subtask manual quick-start dry-run**: From a FRESH shell (`/bin/zsh -f` or a container), follow `README.md` §"Quick start" step-by-step WITHOUT opening any other doc; note every step that requires a referenced doc as a blocking prerequisite (those are README gaps and feed back into T028); STOP and loop if any step fails

**Checkpoint**: AC-1 demonstrably green. The README is the v0.1.0 first impression.

---

## Phase 6: FIX — User Story 2 (Priority: P2) — Code audit fixes + PACKAGE-MAP reorganisation

**Goal**: Every exported `internal/*` symbol either has a consumer or a PACKAGE-MAP entry; every PACKAGE-MAP entry matches a real symbol; zero leftover TODOs.

**Independent Test**: For any `internal/*` package, `go doc -short -all ./internal/<pkg>` and the corresponding PACKAGE-MAP.md section list identical symbols with matching signatures.

### Implementation for User Story 2

- [ ] T030 [US2] **A1-fix dead-export removals (security-critical packages)**: For each category-A1 finding in a security-critical package (`internal/keys`, `internal/vault`, `internal/vault/securebytes`, `internal/token`, `internal/transport/sign`, `internal/transport/ecies`, `internal/audit`), apply the R-008 ceremony: confirm zero non-self non-test usage (cross-check T011), remove symbol + its tests, re-run `magex test:coverrace -run '^Test' ./internal/<pkg>/...`, assert coverage ≥ baseline from T004; finding stays severity=major (R-008 forbids minor for security-critical removals)
- [ ] T031 [US2] **A1-fix dead-export removals (non-security-critical packages)**: For each category-A1 finding in non-security-critical packages, remove symbol + its tests, verify per-package coverage threshold from `docs/AC-MATRIX.md` "Coverage targets" still met
- [ ] T032 [US2] **A3-fix resolve-or-issue TODOs**: For each category-A3 finding, decide `resolved` (apply the fix inline) or `converted-to-issue` (`gh issue create` then replace comment with `// see #N`); record GitHub issue number in `findings.jsonl` disposition_ref
- [ ] T033 [US2] **A4-fix naming-consistency renames or doc**: For each category-A4 finding, apply [research.md R-007](./research.md) three-condition gate; if all three conditions met → rename + update PACKAGE-MAP.md + record as intentional breaking change in commit message; if any condition fails → leave code untouched and add a "documented inconsistency" note to PACKAGE-MAP.md (FR-002 second branch)
- [ ] T034 [US2] **B5 reorganize-package-map**: Per [research.md R-002](./research.md), rewrite `docs/PACKAGE-MAP.md` so every `internal/*` package gets one `## \`internal/<pkg>\`` section with sub-headings `Types`/`Functions`/`Constants`/`Variables`/`Sentinel errors` (omit empty sub-sections); footer `*(Originally locked across SDD-NN, SDD-MM, ...)*` preserves attribution per FR-004; preserve every cell of content — only arrangement changes
- [ ] T035 [US2] **B5-verify post-reorg drift check**: Re-run T010's A1 logic against the reorganised `docs/PACKAGE-MAP.md`; assert zero drift (any leftover drift = unresolved finding, loop on T030–T034)

**Checkpoint**: Categories A1..A4 + B5 reconciled. PACKAGE-MAP.md is the per-package single source of truth.

---

## Phase 7: FIX — User Story 3 (Priority: P2) — Drift-detection script

**Goal**: A maintainer running `scripts/check-package-map-vs-code.sh` from the repo root exits 0 against the as-shipped tree and exits non-zero on injected drift.

**Independent Test**: Inject a stub exported function in `internal/audit/doc.go`, run the script, observe exit 1 with `internal/audit: - code-only: StubForDriftCheck`. Remove the stub, run again, observe exit 0.

### Implementation for User Story 3

- [ ] T036 [US3] **I12 write-check-package-map-vs-code-script**: Create `scripts/check-package-map-vs-code.sh` implementing the algorithm in [research.md R-001](./research.md): for each package in `go list ./internal/...`, parse `go doc -short -all` to `<pkg> <symbol>` lines, parse the matching `## \`internal/<pkg>\`` section of `docs/PACKAGE-MAP.md` to the same shape, `diff` the two, emit drift report per [contracts/check-package-map-vs-code.md](./contracts/check-package-map-vs-code.md) §"Stdout / stderr shape"; refuse to run outside repo root (exit 2)
- [ ] T037 [US3] **I12 chmod and shebang**: `chmod +x scripts/check-package-map-vs-code.sh`; assert shebang line is `#!/usr/bin/env bash` and POSIX-portable (no GNU-only flags) per [plan.md](./plan.md) §"Target Platform"
- [ ] T038 [US3] **I12-subtask self-test (inject stub)**: Copy repo to `/tmp/hush-self-test` per the recipe in [contracts/check-package-map-vs-code.md](./contracts/check-package-map-vs-code.md) §"Self-test"; inject `func StubForDriftCheck() {}` into a copy of `internal/audit/doc.go`; run `scripts/check-package-map-vs-code.sh`; assert exit 1 and stdout names `StubForDriftCheck`; cleanup tempdir
- [ ] T039 [US3] **I12-subtask self-test (as-shipped tree)**: From repo root, run `scripts/check-package-map-vs-code.sh`; assert exit 0 and stdout reports `19 packages, <N> exported symbols, 0 drift` per contract; any non-zero exit = drift was not fully resolved in Phase 6, loop back

**Checkpoint**: Drift script delivered + self-tested both directions. SC-002 testable.

---

## Phase 8: FIX — User Story 4 (Priority: P2) — AC-MATRIX paths + fuzz targets

**Goal**: Every AC-MATRIX row cites an extant test path; every TESTING-STRATEGY fuzz target name resolves to exactly one `func Fuzz*` symbol.

**Independent Test**: For every AC-1..AC-10 row in `docs/AC-MATRIX.md`, the cited primary test path resolves to a file in the working tree. For every fuzz name in `docs/TESTING-STRATEGY.md` §2, `grep -r "func <Name>"` finds exactly one match.

### Implementation for User Story 4

- [ ] T040 [US4] **C6 update-ac-matrix-paths**: For each category-C finding where the cited test was renamed, find the equivalent test under its new path (likely via `git log --follow` on the old path), update the row in `docs/AC-MATRIX.md`; for any finding where NO equivalent exists (= critical), STOP — surface to operator and ask whether to resurrect the test or amend the AC (no autonomous resolution)
- [ ] T041 [P] [US4] **E8 verify-fuzz-targets resolve**: Pre-finding F-PRE-1 says all six fuzz targets are present; T017 re-verifies on the as-shipped tree; if any finding was emitted (= regression in Phase 6 dead-export removal), restore the target and re-run T017 — fuzz targets are constitutional FR-008 and may NOT be removed; if no findings, this task is a no-op confirmation

**Checkpoint**: SC-003 (every AC test path resolves) + SC-004 (six fuzz targets present) both green.

---

## Phase 9: FIX — User Story 5 (Priority: P3) — ARCHITECTURE diagram

**Goal**: The component diagram in `docs/ARCHITECTURE.md` matches the as-built `go list` import graph.

**Independent Test**: Cross-check the diagram against `/tmp/sdd33-imports.txt`: zero orphan packages in the diagram, zero missing edges for real imports (or each omission is documented).

### Implementation for User Story 5

- [ ] T042 [US5] **D7 update-architecture-diagram**: For each category-D finding (orphan package or missing edge), edit the ASCII diagram in `docs/ARCHITECTURE.md`; preserve the hand-edited ASCII style; document any deliberate omission as a footnote rather than re-drawing for clarity

**Checkpoint**: SC-008 testable — diagram and import graph agree.

---

## Phase 10: FIX — User Story 6 (Priority: P2) — Operator-name leak gate

**Goal**: Zero forbidden-name matches across the whole tree (excluding documented exclusions). The gate is enforced by a Go test that runs under `magex test:race`.

**Independent Test**: `magex test:race -run TestExamples_NoOperatorSpecificNames_WholeTree ./internal/supervise/config/...` exits 0.

### Implementation for User Story 6

- [ ] T043 [US6] **J13 extend example_test.go**: Per [research.md R-004](./research.md), add a new test function `TestExamples_NoOperatorSpecificNames_WholeTree` into the existing `internal/supervise/config/example_test.go` (NO new test file); function reads the existing `operatorSpecificForbidden` seed list, walks the repo tree from repo-relative `../../../` via `filepath.WalkDir`, asserts no forbidden token in any non-binary file; documented exclusions = (a) the test file itself, (b) `specs-archive/`, (c) `.git/`
- [ ] T044 [US6] **J13-verify run the new test**: `magex test:race -run TestExamples_NoOperatorSpecificNames_WholeTree ./internal/supervise/config/...`; assert PASS (the empty seed list at plan time means trivial pass; the structural gate is the load-bearing value); any FAIL = forbidden name in the tree, STOP and address per category-J finding disposition
- [ ] T045 [US6] **J13-resolve any J-category findings**: For each finding from T019 with a real match, replace the operator-specific name with a generic placeholder (or document the exclusion per FR-014 — exclusions are constrained to the three documented above; new exclusions need a Constitution-I-level justification)

**Checkpoint**: SC-005 (zero leak) green and structurally enforced going forward.

---

## Phase 11: FIX — User Story 7 (Priority: P3) — specs/ archive + IMPLEMENTATION-PLAN actuals + CONTRIBUTING.md

**Goal**: Historical artefacts migrated to `specs-archive/` with history preserved; IMPLEMENTATION-PLAN reflects actuals; CONTRIBUTING.md documents the three policies.

**Independent Test**: `ls specs-archive/` shows the 32 historical subdirs; `git log --follow specs-archive/001-keys-derivation/spec.md` traces back to the original `specs/001-keys-derivation/spec.md`; CONTRIBUTING.md exists at repo root and has three sections (specs policy, drift detection, operator-name allowlist).

### Implementation for User Story 7

- [ ] T046 [US7] **H11 rename duplicate 026 first**: Per F-001 (F-PRE-2), `git mv specs/026-supervisor-orchestration/ specs/024-supervisor-orchestration/` (orchestration is actually SDD-24 per `docs/sdd/SDD-24.md`); `specs/026-validators-builtins/` stays as the SDD-26 chunk; do this BEFORE T047 so the archive receives correctly-named directories
- [ ] T047 [US7] **H11 git mv specs to specs-archive**: `mkdir -p specs-archive/`; iterate every `specs/NNN-*/` directory where NNN is in `{001..032}` and `git mv specs/NNN-*/ specs-archive/`; the in-flight `specs/033-final-overhaul/` stays in `specs/` per [research.md R-003](./research.md); verify with `git status` that rename detection fires (no `deleted`/`added` pairs)
- [ ] T048 [US7] **H11 create root CONTRIBUTING.md**: Create new `CONTRIBUTING.md` at repo root per [research.md R-005](./research.md) with three sections: (1) Spec artefact policy explaining the `specs-archive/` move + retention rationale, (2) Drift detection naming `scripts/check-package-map-vs-code.sh` and the deferred-CI-wiring rationale (Clarification 2026-05-15 Q2), (3) Operator-name allowlist naming the seed list at `internal/supervise/config/example_test.go::operatorSpecificForbidden` and its documented exclusions; preserve existing `.github/CONTRIBUTING.md` as a pointer (FR-019 — no silent doc drop)
- [ ] T049 [US7] **G10 update-implementation-plan-actuals**: For each category-G finding from T021, edit `docs/IMPLEMENTATION-PLAN.md`: add an "Actual delivery" subsection per phase noting (a) chunks that ran out of order, (b) chunks that were deferred or activated unexpectedly (SDD-24 mid-cycle activation by SDD-25 is the canonical example), (c) F-002 / F-PRE-3 staleness in SDD-PLAYBOOK SDD-25 row (also update `docs/SDD-PLAYBOOK.md` SDD-25 to reflect scenarios 11a/11b/12 landed per recent commits)

**Checkpoint**: SC-009 (specs/ policy chosen + reflected on disk) green. Historical chunk-doc collisions resolved.

---

## Phase 12: Cross-cutting K14 — Constitution recompliance final pass

**Purpose**: Walk every Constitution principle one final time against the as-FIXED code (post-Phases 5..11). This is the meta-application of every principle (FR-015).

- [ ] T050 **K14 constitution-recompliance Principle I**: Confirm zero operator-specific identifiers in the committed tree post-Phase-10; cross-check via T044 still PASS
- [ ] T051 [P] **K14 constitution-recompliance Principle VIII**: Re-run `magex test:coverrace` on the 7 security-critical packages; assert each at ≥ T004 baseline; confirm all 6 fuzz targets still present (T041)
- [ ] T052 [P] **K14 constitution-recompliance Principles II/III/IV/V/VI/VII/IX/X/XI**: Per-principle one-line confirmation that the as-fixed code preserves the principle's invariant; record any drift as a new finding (expected: zero, since fix phases preserved invariants by design)
- [ ] T053 **K14-resolve any K-category critical findings**: If T050/T051/T052 emitted any critical finding, STOP — surface to operator; do NOT mark SDD-33 done; critical-K resolution is mandatory before Phase 13

**Checkpoint**: Constitution recompliance pass complete. FR-015 satisfied.

---

## Phase 13: SUMMARY produce-findings-report

**Purpose**: Render the human-readable FINDINGS SUMMARY from `findings.jsonl` per [contracts/audit-findings.md](./contracts/audit-findings.md) §"Summary report"; this block is included verbatim in the chunk's combined commit message trailer.

- [ ] T054 **SUMMARY render-findings-summary**: Read `specs/033-final-overhaul/findings.jsonl`; render the summary block per the contract: total count, by-severity tally, by-category tally (A1/A2/A3/A4 sub-counts for A; B..K simple counts), by-disposition tally, list of GitHub issue refs for `converted-to-issue` rows, list of deferred-to-followup rows + target chunk ID, explicit lines for "OPERATOR-NAME LEAK CHECK: PASS|FAIL" / "DRIFT SCRIPT: exit 0|N" / "SDD-31 CI GATES (local): PASS|FAIL"; write the rendered text to `specs/033-final-overhaul/FINDINGS-SUMMARY.md` for inclusion in the commit message
- [ ] T055 **SUMMARY verify gate-completion invariants**: Confirm all of: critical findings = 0; every major finding has disposition `resolved` or `converted-to-issue`; every minor finding has a disposition; `findings.jsonl` matches contract field constraints (id pattern, severity enum, category enum, location no-absolute-paths); if any invariant fails, STOP — do NOT proceed to Phase 14

**Checkpoint**: FINDINGS report ready. Chunk completion gate criteria reviewed. Phase 14 may run the final gates only if T055 passed.

---

## Phase 14: Polish & Final Gates

**Purpose**: The pre-merge gate sweep. All commands must exit 0. Any failure → fix root cause, do NOT bypass. Per [quickstart.md](./quickstart.md) §"Step 2 — Run the gates".

- [ ] T056 Run `magex format:fix` from repo root; assert exit 0
- [ ] T057 Run `magex lint` from repo root; assert exit 0
- [ ] T058 Run `magex test:race` from repo root; assert exit 0 (includes the new `TestExamples_NoOperatorSpecificNames_WholeTree` from T043)
- [ ] T059 Run `magex test:race -tags=integration` from repo root; assert exit 0
- [ ] T060 Run `scripts/check-package-map-vs-code.sh` from repo root; assert exit 0 (final SC-002 confirmation)
- [ ] T061 [P] Run `go vet ./...` from repo root; assert exit 0 (SDD-31 gate re-run)
- [ ] T062 [P] Run `govulncheck ./...` from repo root; assert exit 0 (SDD-31 gate re-run)
- [ ] T063 [P] Run `gitleaks detect` from repo root; assert exit 0 (SDD-31 gate re-run)
- [ ] T064 Mark SDD-33 status `done` in `docs/SDD-PLAYBOOK.md` (final edit — only after every preceding gate passes)
- [ ] T065 Review `git status` then stage and commit per [quickstart.md](./quickstart.md) §"Step 3": ONE combined commit (or split per category if diff is too large to review — record the choice in the commit message); include the FINDINGS SUMMARY from T054 verbatim in the commit-message trailer; commit message form per the chunk-doc Prompt 5 template

**Checkpoint**: SDD-33 complete. Repo is clean and ready for SDD-32 to cut v0.1.0.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No predecessors. Tasks T002–T005 [P] after T001.
- **Phase 2 (Foundational)**: Depends on Phase 1 complete. Tasks T006–T009 all [P] within phase.
- **Phase 3 (US2 Audit)**: Depends on Phase 2 (`/tmp/sdd33-godoc/`, `/tmp/sdd33-pkgmap-symbols.txt`). T010–T013 [P]; T014 depends on T010–T013.
- **Phase 4 (Audits B..K)**: Depends on Phase 2. Most tasks [P] across categories; T020 (F9-audit) depends on T008 (binary built).
- **Phase 5 (US1 Fix)**: Depends on Phase 4 T020 (F9-audit findings exist). T026 → T027 → T028 → T029 sequential within story.
- **Phase 6 (US2 Fix)**: Depends on Phase 3 (A-category findings) AND Phase 1 T004 (coverage baseline). T030–T033 can interleave but T030 (security-critical removals) is highest-care; T034 depends on T030–T033 complete; T035 depends on T034.
- **Phase 7 (US3 Fix)**: Depends on Phase 6 (PACKAGE-MAP reorganised before script tested). T036 → T037 → T038 → T039 sequential.
- **Phase 8 (US4 Fix)**: Depends on Phase 4 T016/T017 (C/E findings). T040 sequential (touches AC-MATRIX); T041 [P] with T040.
- **Phase 9 (US5 Fix)**: Depends on Phase 4 T018 (D findings). T042 sequential.
- **Phase 10 (US6 Fix)**: Depends on Phase 4 T019 (J audit dry-run findings). T043 → T044 → T045 sequential.
- **Phase 11 (US7 Fix)**: T046 (rename duplicate 026) MUST precede T047 (archive migration); T048 (CONTRIBUTING.md) [P] with T047; T049 (IMPLEMENTATION-PLAN actuals) [P] with T046–T048.
- **Phase 12 (K14)**: Depends on Phases 5–11 complete (verifies as-fixed code). T050 sequential first; T051/T052 [P]; T053 sequential after T050–T052.
- **Phase 13 (SUMMARY)**: Depends on Phase 12 (all findings have terminal dispositions). T054 → T055 sequential.
- **Phase 14 (Gates)**: Depends on Phase 13 T055 (gate-completion invariants pass). T056–T060 sequential through the magex/script chain (some can [P] but operators typically run sequentially to read each gate's output); T061–T063 [P]; T064 → T065 sequential at the end.

### User Story Dependencies

- **US1 (P1 — README)**: Depends on T008 (binary build) and T020 (F9-audit). Can ship as MVP once Phase 5 completes.
- **US2 (P2 — code audit + PACKAGE-MAP)**: Largest body of work. Independent of US1's edits (different files). Phase 6 must precede Phase 7 (US3 script tests against reorganised PACKAGE-MAP).
- **US3 (P2 — drift script)**: Depends on US2 Phase 6 completion (script verifies as-fixed state).
- **US4 (P2 — AC-MATRIX + fuzz)**: Independent of US2 fixes (different files). T040 may surface a critical finding that BLOCKS the chunk.
- **US5 (P3 — diagram)**: Independent of US1..US4 fixes (touches only `docs/ARCHITECTURE.md`).
- **US6 (P2 — operator-name)**: Independent; gate enforced by Go test under `magex test:race` (Phase 14 T058).
- **US7 (P3 — archive + IMPLEMENTATION-PLAN)**: Independent of US1..US6 code/doc edits (touches `specs/`, `docs/IMPLEMENTATION-PLAN.md`, `docs/SDD-PLAYBOOK.md`, new `CONTRIBUTING.md`).

### Within Each User Story

- Audit task → Findings recorded → Fix task per finding (or batched per category) → Verification step
- Renames (FR-002) require all three R-007 conditions; otherwise document inconsistency in PACKAGE-MAP.md and leave code alone
- Security-critical-package dead-export removals (A1) require the R-008 ceremony (pre-flight baseline + post-removal re-check)

### Parallel Opportunities

- Phase 1 T002–T005 [P] after T001
- Phase 2 T006–T009 all [P]
- Phase 3 audit tasks T010–T013 all [P] (different greps, write to same findings.jsonl — appends are serial but the audit logic itself parallelises)
- Phase 4 audits T015–T025 mostly [P] across categories (different docs/files)
- Phase 6 T030 (security-critical) is highest care and benefits from sequential operator attention; T031–T033 can interleave by file
- Phase 12 T051 / T052 [P]
- Phase 14 T061 / T062 / T063 [P] (independent CI gates)

---

## Parallel Example: Phase 4 Audit Fan-Out

```bash
# Launch all category B..K audits together (different files, independent reads):
Task T015 [US3]: confirm scripts/check-package-map-vs-code.sh absent
Task T016 [US4]: extract AC-MATRIX test paths, verify each exists
Task T017 [US4]: grep six fuzz target names, assert exactly one match each
Task T018 [US5]: cross-reference imports.txt vs ARCHITECTURE.md diagram
Task T019 [US6]: dry-run operator-name forbidden-list grep over the tree
Task T020 [US1]: read README, cross-check claims against ./hush --help and docs/
Task T021 [US7]: git log diff against IMPLEMENTATION-PLAN.md
Task T022 [US7]: ls specs/, verify SDD-PLAYBOOK status table
Task T023 [   ]: K14 principle I spot-check
Task T024 [   ]: K14 principle VIII byte-equality check
Task T025 [   ]: K14 principles II..XI one-line check
```

---

## Implementation Strategy

### Audit-then-Fix Loop (this chunk's primary pattern)

1. **Phase 1**: Setup baselines (clean tree, coverage capture, findings.jsonl scaffold)
2. **Phase 2**: Generate machine-readable code + doc snapshots
3. **Phases 3–4**: Audit every category A..K, append findings to `findings.jsonl`; STOP if any critical finding emerges that cannot be self-resolved
4. **Phases 5–11**: Apply fixes per user-story group; each phase delivers a testable independent slice
5. **Phase 12**: Constitution recompliance pass on the as-fixed code
6. **Phase 13**: Render the FINDINGS SUMMARY
7. **Phase 14**: Final gates + combined commit + mark SDD-33 done

### MVP First (User Story 1 only — README)

If the chunk runs out of time, the load-bearing deliverable is the rewritten README satisfying FR-009 / FR-010 / AC-1 (the v0.1.0 first impression). Phases 1, 2, 4 (T020), and 5 alone produce a shippable README improvement; other categories ride to a follow-on chunk via deferred-to-followup minor findings.

### Incremental Delivery

1. Phase 1 + Phase 2 → audit infrastructure ready
2. Phase 3 + Phase 4 → all findings recorded
3. Phase 5 (US1) → README done, MVP demonstrable
4. Phase 6 (US2) → PACKAGE-MAP reconciled, dead exports removed
5. Phase 7 (US3) → drift script delivered
6. Phases 8–11 (US4..US7) → remaining categories closed
7. Phase 12 → constitution recompliance verified
8. Phase 13 + Phase 14 → summary + gates + commit + SDD-PLAYBOOK update

---

## Critical-Finding Stop Conditions

Per FR-020 + [data-model.md](./data-model.md) Entity 1 disposition rules, the chunk MUST NOT complete if any of the following critical findings is unresolved:

- T016 (C6): An AC-MATRIX row cites a test path with no equivalent test anywhere in the tree → STOP and ask operator whether to resurrect the test or amend the AC
- T017 (E8): A constitutional fuzz target is absent → STOP and restore (FR-008 / Principle VIII)
- T023 (K14-I): A real operator-specific identifier leaked into a non-excluded location → STOP and replace (Principle I)
- T024 (K14-VIII): The security-critical-packages fenced block in the constitution disagrees with `.github/scripts/coverage-threshold/compute.go` → STOP (FR-016 byte-equality anchor)
- T053 (K14): Any other Principle I..XI violation surfaced in the recompliance pass → STOP

A critical finding may NEVER be `converted-to-issue` (data-model.md disposition rule). It must be `resolved`.

---

## Notes

- The chunk's purpose is **reconciliation**, not new behaviour (FR-016 / FR-017). Renames are the highest-risk action and are gated by R-007's three conditions.
- Findings are recorded **before** fixes apply (FR-020). The audit phase is read-mostly; the fix phase changes files.
- The `internal/cli` exit-code constants and `Execute(ctx) int` signature are off-limits per FR-016 — they are the de facto public API contract of `cmd/hush`.
- The constitutional fenced block at `.specify/memory/constitution.md` lines 253-261 and `.github/scripts/coverage-threshold/compute.go`'s security-critical-packages list are READ-ONLY for this chunk (FR-016).
- The combined commit message MUST include the FINDINGS SUMMARY block from T054 in the trailer per [contracts/audit-findings.md](./contracts/audit-findings.md) §"Findings list as commit-trailer".
- After SDD-33 merges, SDD-32 picks up: migrate `specs/033-final-overhaul/` to `specs-archive/`, run GoReleaser, tag `v0.1.0`. SDD-33 explicitly does NOT cut the tag.
