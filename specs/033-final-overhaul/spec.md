# Feature Specification: Final Repo + Docs Overhaul (SDD-33)

**Feature Branch**: `033-final-overhaul`

**Created**: 2026-05-15

**Status**: Draft

**Input**: User description: "final repo + docs overhaul: audit every internal/* for dead exports + naming drift + leftover TODOs; reconcile PACKAGE-MAP.md against actual exported symbols; verify every fuzz target in TESTING-STRATEGY.md exists; verify every AC-MATRIX test path still exists; verify ARCHITECTURE diagram matches as-built; rewrite README.md to reflect what actually got built; zero operator-specific names anywhere; no new behavior, no public API changes"

## Overview

Thirty-two incremental chunks (SDD-01..SDD-31) accumulated drift. Names diverge between packages. Documentation lags implementation. Exported API gets added or renamed without `docs/PACKAGE-MAP.md` catching up. `README.md` claims behaviour the as-built code does not exactly match. `docs/AC-MATRIX.md` cites test paths that may have moved during refactors. The architecture diagram in `docs/ARCHITECTURE.md` may not match the real package import graph. SDD-33 is the deliberate sweep that reconciles every form of drift into a clean, coherent state so SDD-32 can cut the v0.1.0 tag against a repository whose documentation, code, and constitution are mutually consistent.

This chunk **removes, renames, documents, and fixes**. It MUST NOT add new behaviour or change public API.

## Clarifications

### Session 2026-05-15

- Q: `specs/NNN-*/` historical-artefact policy (FR-012)? → A: Move to sibling `specs-archive/` directory; committed and retained as history for now (possible future deletion is explicitly out of scope for this chunk).
- Q: drift-detection script CI wiring (FR-013)? → A: Out of scope for this chunk. Ship the script as a repo-local runnable that exits non-zero on drift; CI wiring is deferred to a later cleanup pass. Rationale: a broader cleanup is anticipated and v0.1.0 end-to-end functionality on operator setup is not yet proven — encoding a CI commitment now is premature.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator follows README to a working install (Priority: P1)

A first-time operator clones the public repo, opens `README.md`, and follows the quick-start. Every claim in the README maps to a real flag, a real subcommand, a real config field, or a real OS prerequisite. The operator reaches a working `hush serve` + `hush request` round-trip without consulting any other doc as a blocking prerequisite.

**Why this priority**: This is the v0.1.0 first impression and the primary acceptance criterion (AC-1). If a fresh reader cannot get hush working from the README alone, every other reconciliation in this chunk is academic.

**Independent Test**: A reviewer who has never read any other hush doc walks through the README quick-start in a fresh shell (or container) and reaches the documented end state. Every step works as written; every flag exists; every OS prerequisite is real.

**Acceptance Scenarios**:

1. **Given** a fresh operator with no prior hush context, **When** they follow the README quick-start end-to-end, **Then** they reach a working install and can complete one approve / fetch / inject round-trip.
2. **Given** the rewritten README, **When** any feature claim is checked against the as-built code, **Then** every claim corresponds to a shipped subcommand, flag, config field, or behaviour.
3. **Given** a feature that did ship in SDD-01..SDD-31 but is absent from the original README, **When** the README is rewritten, **Then** that feature is documented at an operator-appropriate level.
4. **Given** a feature mentioned in the original README that did NOT ship, **When** the README is rewritten, **Then** the feature is either removed or moved to an explicit "Post-v0.1.0 / future scope" section.

---

### User Story 2 - Reviewer audits a package against its locked API (Priority: P2)

A reviewer opens `docs/PACKAGE-MAP.md`, finds the section for `internal/<pkg>`, and sees a single coherent list of that package's exported API. They run `go doc ./internal/<pkg>` and the two outputs agree exactly: every documented symbol exists in the code with the documented signature, and every exported symbol in the code is either (a) consumed by another `internal/*` or `cmd/hush` package, (b) consumed by an integration test, or (c) explicitly listed in PACKAGE-MAP.md under that package.

**Why this priority**: PACKAGE-MAP.md is the source of truth for "what is the public surface of each internal package." Drift in either direction (extra symbol in code = dead export; extra entry in doc = lying spec) erodes the contract that lets future maintainers refactor with confidence.

**Independent Test**: For any one package, `go doc ./internal/<pkg>` and the corresponding PACKAGE-MAP.md section list the same exported symbols with matching signatures. Symbols not listed and not consumed externally are flagged as dead and removed.

**Acceptance Scenarios**:

1. **Given** any `internal/*` package, **When** the reviewer compares its exported symbols against PACKAGE-MAP.md, **Then** the two sets are identical with matching signatures.
2. **Given** an exported symbol that is neither listed in PACKAGE-MAP.md nor imported by any other `internal/*`, `cmd/hush`, or integration test, **When** the audit runs, **Then** the symbol is removed in this chunk OR added to PACKAGE-MAP.md if intentionally exported for a documented reason.
3. **Given** PACKAGE-MAP.md currently consists of appended "Exported API — locked at SDD-NN" sections, **When** the reconciliation completes, **Then** each package has one consolidated section listing its locked API, with the historical chunk attribution preserved as a footer.

---

### User Story 3 - Drift-detection script catches PACKAGE-MAP / code drift on demand (Priority: P2)

After the overhaul, a maintainer (or a future contributor reviewing a PR locally) runs `scripts/check-package-map-vs-code.sh` from the repo root. If any `internal/*` package's exported symbols diverge from its PACKAGE-MAP.md entry, the script exits non-zero with an actionable message naming the symbol and its package. CI wiring is deliberately deferred to a later cleanup pass — the durable artefact delivered by SDD-33 is the runnable script itself.

**Why this priority**: Reconciling drift once is necessary; having a repeatable check makes the reconciliation durable. Until v0.1.0 functionality is independently proven on operator setup, encoding the check as a blocking CI gate is premature; the script's value is preserved by being runnable on demand.

**Independent Test**: Add a stub exported function to any `internal/*` package without updating PACKAGE-MAP.md, run `scripts/check-package-map-vs-code.sh`, observe a non-zero exit with a message naming the new symbol. Conversely, against the as-shipped tree at the end of this chunk, the script MUST exit 0.

**Acceptance Scenarios**:

1. **Given** an `internal/*` package whose exported symbols match PACKAGE-MAP.md, **When** the drift-detection script runs, **Then** it exits 0.
2. **Given** an `internal/*` package with an exported symbol absent from PACKAGE-MAP.md, **When** the drift-detection script runs, **Then** it exits non-zero and names the offending package and symbol.
3. **Given** a PACKAGE-MAP.md entry referencing a symbol that no longer exists in code, **When** the drift-detection script runs, **Then** it exits non-zero and names the missing symbol.

---

### User Story 4 - Every documented test path and fuzz target is real (Priority: P2)

A reviewer opens `docs/AC-MATRIX.md` and clicks each cited primary test path; every path resolves to an existing test in the current tree. They open `docs/TESTING-STRATEGY.md` §2 and confirm each of the six fuzz targets (`FuzzVaultDecode`, `FuzzJWTValidate`, `FuzzECIESDecrypt`, `FuzzVerifyRequest`, `FuzzServerTOML`, `FuzzSuperviseTOML`) exists in the code with the documented name.

**Why this priority**: AC-MATRIX.md is the v0.1.0 release gate. A row whose cited test path no longer exists is a silent gate failure — nothing fails CI, but the AC is no longer demonstrably proven.

**Independent Test**: For every AC-1..AC-10 row, the cited primary test path is present in the working tree. For every fuzz target name in TESTING-STRATEGY.md, `grep -r "func Fuzz<Name>"` finds exactly one match.

**Acceptance Scenarios**:

1. **Given** any row in AC-MATRIX.md, **When** the cited primary test path is checked, **Then** the file exists in the current tree.
2. **Given** any fuzz target name in TESTING-STRATEGY.md §2, **When** the codebase is searched, **Then** a function with that exact name exists.
3. **Given** a missing test path or fuzz target is discovered during the audit, **When** the gap is real (no equivalent exists under a renamed path), **Then** the gap is surfaced as a critical finding that BLOCKS the chunk completing.

---

### User Story 5 - Architecture diagram matches the as-built import graph (Priority: P3)

A new contributor opens `docs/ARCHITECTURE.md` and compares the component diagram to the actual `go list` import graph. There are no orphan packages in the diagram and no missing arrows for real import edges.

**Why this priority**: An inaccurate architecture diagram misleads new contributors and reviewers about how the system is wired. It does not break the build, so it is P3 rather than P1.

**Independent Test**: Generate the as-built package import graph; cross-check against the diagram in ARCHITECTURE.md. No orphan packages, no missing edges.

**Acceptance Scenarios**:

1. **Given** the as-built `go list` import graph, **When** compared against the diagram in ARCHITECTURE.md, **Then** every package shown in the diagram exists and every actual import edge is represented.
2. **Given** a package that exists in code but is missing from the diagram, **When** the audit runs, **Then** the diagram is updated to include it (or its omission is documented if intentional).

---

### User Story 6 - Zero operator-specific name leakage anywhere in the tree (Priority: P2)

A reviewer searches the entire committed tree for any operator-specific identifier (the SDD-30 forbidden list, applied broadly). Zero matches occur outside the test fixture that defines the forbidden list itself.

**Why this priority**: Constitutional Principle I (operator-agnostic) is non-negotiable. SDD-30 enforced this within `deploy/examples/` and the supervisor template; SDD-33 extends the check to the whole repo because incremental chunks may have leaked operator-specific names elsewhere.

**Independent Test**: Run the operator-name allowlist grep over the entire committed tree (excluding the test that defines the list and the historical `specs-archive/` artefacts per FR-012). Result MUST be zero matches.

**Acceptance Scenarios**:

1. **Given** the SDD-30 forbidden-name list, **When** grepped against the entire committed tree, **Then** zero matches occur outside the test fixture defining the list.
2. **Given** an operator-specific name discovered during the audit, **When** the location is examined, **Then** the name is replaced with a generic placeholder OR the file is excluded from scope and the exclusion is documented.

---

### User Story 7 - Implementation history is preserved or explicitly archived (Priority: P3)

The `specs/` directory contains 30+ subdirectories of historical `spec.md` / `plan.md` / `tasks.md` artefacts from each SDD chunk. A future contributor reading the repo knows whether these are kept in-tree, moved to a sibling archive directory, or removed from version control. The decision and its rationale are documented.

**Why this priority**: The historical artefacts are valuable for understanding past decisions, but they are large and noisy. A clear policy prevents future contributors from either accidentally deleting them or being confused by their continued growth.

**Independent Test**: A new contributor reads the documented policy and immediately knows where SDD-NN spec artefacts live and whether they are committed to the repo.

**Acceptance Scenarios**:

1. **Given** the current `specs/` directory of historical artefacts, **When** a policy is chosen, **Then** the policy is documented in a discoverable location and the directory is reorganised to match it.
2. **Given** the chosen policy, **When** SDD-32 generates new spec artefacts post-overhaul, **Then** those artefacts conform to the policy without manual intervention.

---

### Edge Cases

- **Dead exported symbol whose removal would change a public type signature in another package's PACKAGE-MAP entry**: Treat as a real public-API change — surface as a finding, do NOT silently remove. The chunk forbids public-API changes; the symbol stays and PACKAGE-MAP gains a documenting entry.
- **TODO / FIXME / XXX comment in `internal/*` or `cmd/hush` whose resolution requires non-trivial work**: Convert to a GitHub issue and replace the comment with `// see #N`. The chunk MUST NOT defer work silently.
- **AC-MATRIX row whose primary test path no longer exists AND no equivalent test exists under a renamed path**: Real gap, surface as a critical finding, BLOCK the chunk completing until the gap is resolved by either resurrecting the test or amending the AC.
- **Fuzz target listed in TESTING-STRATEGY.md but missing from code**: Same treatment — critical finding, block the chunk.
- **Operator-specific name discovered in a file outside the chunk's editing scope** (e.g., a third-party vendored asset): If the file is genuinely out of scope for ownership reasons, document the exclusion in CONTRIBUTING.md or the relevant doc; otherwise replace.
- **Naming inconsistency that would require a public-rename of an exported symbol** (e.g., `Approver` vs `Approval` across packages): Allow the rename ONLY if (a) the new name is the one PACKAGE-MAP.md already documents and (b) the rename is treated and announced as an intentional breaking change. Otherwise document the inconsistency and leave the code alone.
- **README rewrite reveals a missing or broken link to a doc that was renamed during SDD-01..SDD-31**: Update the link; if the target doc no longer exists, either resurrect equivalent content or remove the reference.
- **`go doc ./internal/<pkg>` lists a symbol that exists for cross-package consumption but is never imported by `cmd/hush` and never listed in PACKAGE-MAP** (e.g., utility used by integration tests under `tests/integration/`): Treat integration-test consumption as legitimate consumption — the symbol is not dead. Document the consumption pattern in PACKAGE-MAP.

## Requirements *(mandatory)*

### Functional Requirements

#### A. Dead-export removal and naming consistency

- **FR-001**: Every exported symbol in every `internal/*` package MUST be one of: (a) imported by at least one other `internal/*` or `cmd/hush` package, (b) imported by an integration test under `tests/integration/`, or (c) explicitly listed in `docs/PACKAGE-MAP.md` for that package. Symbols that meet none of these MUST be removed in this chunk.
- **FR-002**: Symbol-name inconsistencies across packages (the same conceptual operation named differently in two packages) MUST be either reconciled by rename — only when the rename matches PACKAGE-MAP.md and is treated as an intentional, documented breaking change — OR documented as an accepted inconsistency in PACKAGE-MAP.md.
- **FR-003**: Every TODO / FIXME / XXX comment in `internal/*` and `cmd/hush` MUST be resolved in this chunk OR converted to a GitHub issue with the comment replaced by `// see #N`. Silent deferral is forbidden.

#### B. PACKAGE-MAP reconciliation

- **FR-004**: `docs/PACKAGE-MAP.md` MUST list one consolidated section per `internal/*` package, replacing the current per-chunk "Exported API — locked at SDD-NN" appended sections. The historical chunk attribution MUST be preserved as a per-section footer (e.g., "(originally locked across SDD-NN, SDD-MM, ...)").
- **FR-005**: Every entry in the consolidated PACKAGE-MAP.md MUST match an actual exported symbol in the corresponding package, with a signature that matches the documented signature. Drift in either direction (extra entry / extra symbol) MUST be reconciled.

#### C. AC-MATRIX verification

- **FR-006**: Every AC-1..AC-10 row in `docs/AC-MATRIX.md` MUST cite at least one primary test path that exists in the current tree. Rows whose cited path has been renamed MUST be updated to the new path; rows whose cited path no longer has any equivalent test MUST be surfaced as a critical finding before this chunk completes.

#### D. ARCHITECTURE diagram verification

- **FR-007**: The component diagram in `docs/ARCHITECTURE.md` MUST be consistent with the as-built package import graph. Every package shown in the diagram MUST exist in code; every actual import edge between shown packages MUST be represented in the diagram (or explicitly omitted with a documented reason for the omission).

#### E. TESTING-STRATEGY fuzz-target verification

- **FR-008**: Every fuzz target named in `docs/TESTING-STRATEGY.md` §2 MUST exist in the code with the exact documented name. The required set is: `FuzzVaultDecode`, `FuzzJWTValidate`, `FuzzECIESDecrypt`, `FuzzVerifyRequest`, `FuzzServerTOML`, `FuzzSuperviseTOML`. A missing target MUST be surfaced as a critical finding before this chunk completes.

#### F. README rewrite

- **FR-009**: `README.md` MUST accurately describe the as-built v0.1.0 system. Specifically: every documented subcommand, flag, config field, and OS prerequisite MUST exist in the as-built code; every shipped feature whose absence from the original README would mislead a fresh operator MUST be documented; every feature mentioned in the original README that did NOT ship MUST be removed or moved to an explicit "Post-v0.1.0 / future scope" section.
- **FR-010**: A fresh reader, following only `README.md`, MUST be able to complete one end-to-end install and one approve / fetch / inject round-trip without consulting any other doc as a blocking prerequisite (other docs may be linked for deeper context).

#### G. IMPLEMENTATION-PLAN actuals

- **FR-011**: `docs/IMPLEMENTATION-PLAN.md` MUST reflect the actual chunk delivery order, noting any chunk that was deferred, skipped (e.g., SDD-24), reordered, or activated unexpectedly relative to the planned sequence.

#### H. specs/ directory cleanup

- **FR-012**: The historical `specs/NNN-*/` artefacts MUST be moved to a sibling `specs-archive/` directory at the repository root, retained under version control as committed historical record. The chosen policy and its rationale MUST be documented in a discoverable location (e.g., `CONTRIBUTING.md`), and the on-disk layout MUST be reorganised to match. New SDD chunks generated post-overhaul (starting with SDD-32) MUST conform to the policy without manual intervention. Possible future deletion or further archival of `specs-archive/` is explicitly out of scope for this chunk.

#### I. Drift-detection automation

- **FR-013**: A new repository script MUST automate the PACKAGE-MAP-vs-code drift check (FR-005). The script MUST exit non-zero when any drift is detected and MUST name the offending package and symbol in its output. The script MUST be runnable locally from the repository root and MUST pass (exit 0) at the end of this chunk. CI wiring is explicitly out of scope for SDD-33 and is deferred to a later cleanup pass.

#### J. Operator-specific name leak check

- **FR-014**: The SDD-30 operator-name allowlist MUST be grepped over the entire committed tree (with explicit, documented exclusions for any test fixtures that define the list itself, and for the historical artefacts now under `specs-archive/` per FR-012). Zero matches MUST be observed before this chunk completes.

#### K. Constitutional re-compliance

- **FR-015**: Every Constitution principle (I..XI) MUST be re-evaluated against the as-built code. Any drift discovered MUST be either reconciled in this chunk or surfaced as a finding before SDD-32 (release tag) proceeds.

#### Cross-cutting invariants

- **FR-016**: This chunk MUST NOT change public API. Renames are permitted only when (a) explicitly required to reconcile FR-005 drift, (b) treated as intentional breaking changes, and (c) re-documented in PACKAGE-MAP.md and the chunk's commit message.
- **FR-017**: This chunk MUST NOT add new behaviour. Code changes are restricted to: removing dead exports, renaming for documented consistency, fixing typos, resolving comments, and adding the FR-013 drift-detection script.
- **FR-018**: This chunk MUST NOT delete a test that protects an AC-1..AC-10 row. Tests may be consolidated only if literally duplicate; otherwise they remain.
- **FR-019**: This chunk MUST NOT silently drop a doc. A removed doc MUST be replaced by equivalent or better content elsewhere, with the move documented in the chunk's commit message.
- **FR-020**: All findings discovered during the audit MUST be recorded with severity (critical / major / minor), category (one of A..K above), location, and disposition (resolved / converted-to-issue / deferred-to-followup). Critical findings BLOCK chunk completion; major findings are resolved here OR converted to issues; minor findings may ride to a follow-on chunk and MUST be documented.

### Key Entities

- **Finding**: A discovered drift or inconsistency. Attributes: severity (critical/major/minor), category (A..K), location (file path, optionally line), description, disposition (resolved / converted-to-issue / deferred). Findings produced during the audit are the primary output of the chunk before fixes are applied.
- **Locked Exported API entry**: A single line in `docs/PACKAGE-MAP.md` describing one exported symbol of a given package — its name, signature, and the SDD chunk(s) that originally locked it. After this chunk, each package has exactly one consolidated block of these entries.
- **Operator-specific name**: Any identifier on the SDD-30 forbidden list (real human names, real machine hostnames, real channel/server IDs, real API keys or fragments thereof). Zero occurrences in the committed tree are required.
- **Drift-detection script**: A repo-local script (sketched as `scripts/check-package-map-vs-code.sh` in the chunk contract) that compares `go doc ./internal/...` output to PACKAGE-MAP.md entries and exits non-zero on any divergence.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A fresh operator who has never read any other hush doc can follow the rewritten `README.md` quick-start to a working `hush serve` + `hush request` round-trip in under 30 minutes (excluding upstream provisioning of Tailscale account and Discord bot).
- **SC-002**: For every `internal/*` package, the set of symbols listed in `docs/PACKAGE-MAP.md` is identical to the set of exported symbols in the package code (counted by the FR-013 drift-detection script). Zero drift, in either direction.
- **SC-003**: Every AC-1..AC-10 row in `docs/AC-MATRIX.md` cites at least one primary test path; 100% of cited paths resolve to existing files in the current tree.
- **SC-004**: All six fuzz targets named in `docs/TESTING-STRATEGY.md` §2 (`FuzzVaultDecode`, `FuzzJWTValidate`, `FuzzECIESDecrypt`, `FuzzVerifyRequest`, `FuzzServerTOML`, `FuzzSuperviseTOML`) are found in the codebase with exactly one matching function each.
- **SC-005**: Operator-specific-name grep over the entire committed tree (with documented exclusions only) returns zero matches.
- **SC-006**: Zero TODO / FIXME / XXX comments remain in `internal/*` and `cmd/hush`. Every comment that was present before this chunk is either resolved in code or replaced with a `// see #N` GitHub-issue reference.
- **SC-007**: The full pre-existing CI gate set (the SDD-31 gates: format, lint, race tests, integration tests, govulncheck, gitleaks, fuzz targets, CGO=0, no /vendor) passes after the chunk completes. The new FR-013 drift script, run locally from the repo root, also exits 0 against the as-shipped tree (CI wiring deferred).
- **SC-008**: The architecture diagram in `docs/ARCHITECTURE.md` and the as-built package import graph agree: zero orphan packages in the diagram, zero missing arrows for real import edges (or each omission is documented with a reason).
- **SC-009**: The historical `specs/` artefact policy is chosen, documented in a discoverable location, and the on-disk layout matches the policy. New SDD chunks generated post-overhaul (starting with SDD-32) conform to the policy without manual intervention.
- **SC-010**: Public API surface (every symbol whose name is unchanged before/after this chunk) is byte-identical in signature; renames are limited to the set explicitly documented in the chunk's commit message as intentional breaking changes.

## Assumptions

- Constitution v1.1.1 (the version current at the start of this chunk) is the compliance bar; SDD-33 does not amend the constitution.
- All chunks SDD-01..SDD-31 have reached `done` status (or the explicit `skipped` status documented for SDD-24) per `docs/SDD-PLAYBOOK.md` before SDD-33 begins. Drift originating from in-progress chunks is out of scope.
- The SDD-30 operator-name allowlist is the authoritative source for "operator-specific names"; SDD-33 does not redefine the list, only re-applies it more broadly.
- The `tests/integration/` directory is a legitimate consumer of `internal/*` exported symbols for the purposes of dead-export analysis (FR-001 condition (b)).
- "No public API change" means no change to symbols importable from outside `internal/*` — i.e., from `cmd/hush`. Internal-to-internal renames are permitted under FR-002 / FR-016 with documented rationale.
- Resolving every leftover TODO/FIXME/XXX comment in non-trivial form is out of scope; conversion to GitHub issues is an acceptable disposition (FR-003).
- The new drift-detection script (FR-013) targets the audit problem; deeper static analysis (e.g., dead-code analysis for unexported symbols) is out of scope for this chunk.
- SDD-32 (release tag) is explicitly downstream of SDD-33; the v0.1.0 tag is created by SDD-32, not by this chunk.
