# Implementation Plan: Final Repo + Docs Overhaul (SDD-33)

**Branch**: `033-final-overhaul` | **Date**: 2026-05-15 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/033-final-overhaul/spec.md`

## Summary

SDD-33 is the deliberate sweep that reconciles 32 chunks' worth of drift
into a clean, coherent state ready for the SDD-32 v0.1.0 tag. The chunk
**removes, renames, documents, and fixes** — it does NOT add new
behaviour and does NOT change public API (only `cmd/hush`'s
`internal/cli.Execute` surface counts as public; `internal/*` is
internal by Go semantics and renames within it are governed by FR-002
/ FR-016 reconciliation rules).

The work is an audit-then-fix loop across eleven categories (A..K, as
laid out in the chunk contract). Each category produces a structured
**Finding** with severity (critical / major / minor), category,
location, description, and disposition. Critical findings BLOCK the
chunk completing; major findings are resolved here OR converted to
GitHub issues; minor findings may ride to a follow-on chunk and are
recorded for posterity. The chunk delivers **one shippable artefact
beyond reconciliations** — `scripts/check-package-map-vs-code.sh` — a
runnable local drift detector (FR-013, CI wiring deliberately deferred
per Clarification 2026-05-15 Q2).

The `specs/` directory's 32 subdirectories are migrated to a sibling
`specs-archive/` directory under version control (Clarification
2026-05-15 Q1), and the policy is documented in `CONTRIBUTING.md`. The
operator-name allowlist (the SDD-30 seed list defined in
`internal/supervise/config/example_test.go`) is re-applied over the
entire committed tree, with documented exclusions only for the test
fixture that defines the list and for `specs-archive/` artefacts.

Coverage target is N/A — no production code is added. The gate is the
existing SDD-31 CI gate set staying green, plus the new
`scripts/check-package-map-vs-code.sh` exiting 0 against the
as-shipped tree.

## Technical Context

**Language/Version**: Go 1.26.1 (pinned in `go.mod`) for any code
touched by the audit; **POSIX shell** (`bash`) for
`scripts/check-package-map-vs-code.sh`. The script's interpreter is
`#!/usr/bin/env bash` so it runs identically on macOS and Linux CI
runners.

**Primary Dependencies**:
- `go doc ./...` (stdlib `cmd/doc`) — the authoritative source of
  truth for "what is exported from this package right now."
- `go list -f '{{.ImportPath}} {{.Imports}}' ./...` (stdlib `cmd/go`)
  — generates the as-built package import graph for ARCHITECTURE.md
  verification.
- `grep` / `awk` / `sed` (POSIX userland) — drives the audit script and
  the operator-name leak check.
- `gh` (GitHub CLI) — converts deferred TODO/FIXME/XXX comments into
  GitHub issues. Already installed in operator environment.
- `git mv` — moves `specs/NNN-*/` → `specs-archive/NNN-*/` while
  preserving history. Per Clarification 2026-05-15 Q1.
- No new Go module dependencies. **Constitution XI clean.**

**Storage**:
- Existing repo files edited in place (`docs/PACKAGE-MAP.md`,
  `docs/AC-MATRIX.md`, `docs/ARCHITECTURE.md`,
  `docs/IMPLEMENTATION-PLAN.md`, `docs/TESTING-STRATEGY.md`,
  `README.md`, possibly `CONTRIBUTING.md` — see structure section).
- New file: `scripts/check-package-map-vs-code.sh`.
- New directory: `specs-archive/` (32+ historical artefact subdirs).
- Touched but not bulk-rewritten: `internal/*/*.go` (only for FR-001
  dead-export removal, FR-002 rename reconciliation, FR-003 comment
  conversion).

**Testing**:
- Existing `magex test:race` and `magex test:race -tags=integration`
  must remain green after the sweep. **No test added that changes
  coverage shape; no test removed that protects an AC** (FR-018).
- The new shell script gets a self-test pattern (inject a stub
  exported function in a tempdir copy, assert non-zero exit) inlined
  as a comment block in the script header — operators run it manually
  per Clarification 2026-05-15 Q2.
- One new test file is permitted by FR-018: a test that exercises the
  whole-tree operator-name leak check **only if** the existing
  `TestExamples_NoOperatorSpecificNames` cannot be extended without
  scope creep. Preferred path: extend the existing test's seed list +
  search scope. Decided in [research.md R-004](./research.md).

**Target Platform**: macOS arm64 + Linux amd64 (matches the SDD-31 CI
matrix). The script uses POSIX-portable shell constructs only; no
GNU-specific flags.

**Project Type**: Single Go module + repo-level documentation +
tooling tree. No new package added.

**Performance Goals**: N/A. The audit is a one-shot. The drift script
must complete in ≤30s on a developer laptop (target — observable by
`time scripts/check-package-map-vs-code.sh`).

**Constraints**:
- **No public API change.** `internal/cli.Execute(ctx) int` and the
  cobra command tree are the contract; nothing under `cmd/hush` or
  `internal/cli`'s exported package symbols may change name or
  signature except as an intentional, documented breaking change
  required by FR-005 drift (none anticipated at plan time — see
  [research.md R-007](./research.md)).
- **No new behaviour.** Code changes are restricted to: removing dead
  exports, renaming for documented consistency, fixing typos,
  resolving comments, and the new drift-detection script.
- **No silent doc drop.** A removed doc must be replaced by equivalent
  or better content elsewhere, with the move documented in the
  combined commit message (FR-019).
- **No test deletion.** Only literal duplicates may be consolidated;
  ACs (AC-1..AC-10) must remain provably exercised (FR-018, Principle
  VIII).
- **Zero operator-specific identifiers in the committed tree** outside
  the documented exclusions (FR-014, Principle I).
- **Findings discipline.** Every drift discovered is recorded with
  severity / category / location / disposition before fixes apply
  (FR-020); critical findings block completion.

**Scale/Scope**:
- **Audit surface (read-only):** 19 internal packages
  (`internal/audit`, `internal/cli`, `internal/config`,
  `internal/discord`, `internal/discord/alerts`, `internal/keychain`,
  `internal/keys`, `internal/logging`, `internal/server`,
  `internal/supervise`, `internal/supervise/config`,
  `internal/supervise/validators`, `internal/supervise/watchdog`,
  `internal/testutil`, `internal/token`, `internal/transport/ecies`,
  `internal/transport/sign`, `internal/vault`,
  `internal/vault/securebytes`) + `cmd/hush` + every `docs/*` file +
  `README.md` + `.github/CLAUDE.md`.
- **Files expected to materially change:** ~10 docs + ~5–15 internal
  Go files (dead-export removals, comment conversions; volume
  decided by audit findings, capped by FR-016/FR-017) + 1 new shell
  script + 32+ moved spec directories.
- **New artefacts:** `scripts/check-package-map-vs-code.sh` (≈150
  lines), `specs-archive/` (32+ migrated subdirs), `CONTRIBUTING.md`
  (NEW or extended — TBD by research, see [research.md R-005](./research.md)).
- **Findings volume estimate:** 30–60 total across A..K (rough guess
  from skim — the audit phase quantifies); critical expected to be 0
  (the project has passed coverage + integration gates), majors
  expected to be 5–15 (PACKAGE-MAP drift after 14 chunks of appended
  sections is non-trivial), minors expected to be 15–40 (naming
  inconsistencies, typos, stale comments).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1
design.* The full constitution sits at
`.specify/memory/constitution.md` (version 1.1.1, ratified 2026-04-26,
last amended 2026-05-14). The chunk contract names **Principles I and
VIII** as in scope, and explicitly invokes a **meta-application of every
principle** (FR-015 / K-14). This section walks every principle.

| Principle | In scope? | Evaluation |
|-----------|-----------|------------|
| **I — Zero files at rest on agent machines** | **YES (load-bearing — FR-014)** | The chunk re-applies the SDD-30 operator-name allowlist to the entire committed tree. Documented exclusions: (a) the test fixture that defines the seed list (`internal/supervise/config/example_test.go::operatorSpecificForbidden`), (b) `specs-archive/` historical artefacts (those captured pre-reconciliation snapshots — purging them retroactively would rewrite history). The check is a hard gate: SC-005 = zero matches. The drift-detection script does NOT scan for secrets; that remains the dedicated job of `gitleaks` (Principle XI). ✅ Pass once SC-005 = 0. |
| **II — Approval is human, approval is phone** | No | No approval flow changes. The chunk may rename internal symbols related to approval (e.g. `Approver` vs `Approval`) under FR-002, but only if PACKAGE-MAP.md drift demands it AND the rename is documented as an intentional breaking change. ✅ Pass — neutral. |
| **III — Defense in depth through crypto layering** | No | No crypto code touched. Dead-export removal in `internal/keys`, `internal/vault`, `internal/token`, `internal/transport/*` requires constitutional caution (these are 100%-coverage security-critical packages — see Principle VIII), so any removal there demands: (a) confirmed zero non-test usage outside the package, (b) confirmed not listed in PACKAGE-MAP.md, (c) coverage not regressed. ✅ Pass — neutral, with extra care on security-critical packages. |
| **IV — Supervisor for daemons, wrap-shell for humans** | No | No lifecycle code touched. The chunk may consolidate documentation language about "supervisor" vs "wrap-shell" if drift is found between docs, but the runtime contract is untouched. ✅ Pass — neutral. |
| **V — Staleness is visible, failure is loud** | No | No staleness contract changes. README-rewrite (FR-009) must preserve the operator-facing description of stale-credential semantics (exit 78 contract, Discord alerts, validator failure surfaces). Cross-checked against `docs/SECURITY.md` and `docs/OPERATIONS.md` in [research.md R-006](./research.md). ✅ Pass — neutral. |
| **VI — Tailscale-only, never public** | No | No network code touched. The README rewrite preserves the "vault server bound to Tailscale interface only" claim (already true; only the explanation might be polished). The drift script never opens a network connection. ✅ Pass — neutral. |
| **VII — CLI design standards** | No | No CLI surface changes. The chunk MAY rename an exported helper inside `internal/cli` ONLY if PACKAGE-MAP.md drift demands it AND it is treated as an intentional breaking change (FR-016). No new global flags; no new subcommands. ✅ Pass — neutral. |
| **VIII — Testing discipline** | **YES (load-bearing — FR-018)** | This is the chunk's hardest constraint. (a) The chunk MUST NOT delete a test that protects an AC-1..AC-10 row (consolidate-if-duplicate is the only allowed reduction). (b) Coverage thresholds (≥90% project + 100% on security-critical packages: `internal/keys`, `internal/vault`, `internal/vault/securebytes`, `internal/token`, `internal/transport/sign`, `internal/transport/ecies`, `internal/audit`) MUST hold. Dead-export removal in these packages can ONLY proceed if coverage stays at 100% after removal — i.e., the dead symbol's tests are also removed, AND the remaining code's coverage stays at 100%. (c) All six mandatory fuzz targets MUST exist with their documented names (FR-008 / SC-004) — `FuzzVaultDecode`, `FuzzJWTValidate`, `FuzzECIESDecrypt`, `FuzzVerifyRequest`, `FuzzServerTOML`, `FuzzSuperviseTOML`. (d) The constitutional security-critical-packages fenced block (line 253-261 of the constitution) is byte-equality-asserted by `.github/scripts/coverage-threshold/compute.go` — the chunk MUST NOT edit either the constitution block or the compute.go list. ✅ Pass once verified — see [research.md R-008](./research.md). |
| **IX — Idiomatic Go discipline** | Indirect | The chunk may rename symbols. Every rename must respect: context propagation (no Context-in-struct), sentinel-error vocabulary preserved, no introduction of globals or init(), no panics added. The audit step explicitly checks for goroutine ownership comments and recover() at top frames — but the chunk does NOT touch goroutine code. ✅ Pass — guarded by linting (SDD-31 `magex lint` gate). |
| **X — Observability & redaction** | Indirect | `SecureBytes.LogValue()` and the audit-log redaction discipline are untouched. The README rewrite (FR-009) preserves the redaction story. Removing a dead exported symbol that participates in a redaction path requires verifying no log call site loses its redaction guarantee — but a truly dead symbol has no live call sites, so this is satisfied by definition. ✅ Pass — neutral. |
| **XI — Native-first, minimal dependencies, ephemeral vault** | Indirect | **Zero new direct or indirect Go module dependencies** (the chunk uses only stdlib `cmd/doc`, `cmd/go`, plus POSIX userland and `gh`). The drift script is shell, not Go. `govulncheck` and `gitleaks` gates from SDD-31 keep applying. The ephemeral-vault posture is documentation-level only and is preserved verbatim in the README rewrite. ✅ Pass. |

**Outcome:** No principle violates. **Constitution Check passes
clean.** No Complexity Tracking rows required.

## Project Structure

### Documentation (this feature)

```text
specs/033-final-overhaul/
├── plan.md              # This file (/speckit-plan output)
├── research.md          # Phase 0 — resolves the 8 R-NNN decisions (see below)
├── data-model.md        # Phase 1 — Finding entity + Locked-Exported-API entry entity
├── quickstart.md        # Phase 1 — operator-runnable walkthrough of the overhaul
├── contracts/           # Phase 1 — the drift-script CLI contract + the audit-finding JSONL contract
│   ├── check-package-map-vs-code.md   # CLI contract for the new script
│   └── audit-findings.md              # JSONL schema for the FINDINGS list
├── spec.md              # /speckit-specify output (pre-existing)
└── tasks.md             # /speckit-tasks output (NOT created by this command)
```

### Source Code (repository root)

This is a repo-wide sweep, not a new package. The structure below shows
**what is touched (read-only audit vs. edited)** and **what is new**.

```text
.
├── README.md                                 [REWRITE — FR-009, FR-010 — surgical-or-from-scratch decision in research.md R-006]
├── CONTRIBUTING.md                           [NEW or EXTENDED — documents specs-archive policy per FR-012; decision in research.md R-005]
├── cmd/hush/                                 [AUDIT — main.go only; expected zero changes]
│   └── main.go
├── internal/                                 [AUDIT — every package; touches for dead-export / rename / comment]
│   ├── audit/                                [AUDIT]
│   ├── cli/                                  [AUDIT — largest surface; care: exit-code constants are public contract]
│   ├── config/                               [AUDIT]
│   ├── discord/                              [AUDIT]
│   │   └── alerts/                           [AUDIT]
│   ├── keychain/                             [AUDIT — Keychain ACL invariant per Principle I]
│   ├── keys/                                 [AUDIT — security-critical; 100% coverage gate]
│   ├── logging/                              [AUDIT]
│   ├── server/                               [AUDIT — 95% coverage gate]
│   ├── supervise/                            [AUDIT — 95% coverage gate]
│   │   ├── config/                           [AUDIT — includes example_test.go (operator-name seed list)]
│   │   ├── validators/                       [AUDIT]
│   │   └── watchdog/                         [AUDIT]
│   ├── testutil/                             [AUDIT]
│   ├── token/                                [AUDIT — security-critical; 100% coverage gate]
│   ├── transport/                            [AUDIT — security-critical; 100% coverage gate]
│   │   ├── ecies/
│   │   └── sign/
│   └── vault/                                [AUDIT — security-critical; 100% coverage gate]
│       └── securebytes/                      [AUDIT — security-critical; 100% coverage gate]
├── docs/                                     [VERIFY + EDIT — every file]
│   ├── AC-MATRIX.md                          [VERIFY — FR-006 / SC-003: every cited test path resolves; update on rename]
│   ├── API.md                                [SKIM — verify against actual handlers]
│   ├── ARCHITECTURE.md                       [VERIFY — FR-007 / SC-008: diagram matches `go list` import graph]
│   ├── CLEAN-MACHINE.md                      [SKIM — already patched by SDD-30]
│   ├── CONFIG-SCHEMA.md                      [SKIM — verify against config/loader types]
│   ├── DAEMONS.md                            [SKIM]
│   ├── IMPLEMENTATION-PLAN.md                [REWRITE actuals — FR-011: planned vs delivered order]
│   ├── LIFECYCLE-SCENARIOS.md                [SKIM — verify against AC-10 17 scenario names]
│   ├── MVP.md                                [SKIM — may be legacy; decide in research]
│   ├── OPERATIONS.md                         [SKIM]
│   ├── PACKAGE-MAP.md                        [REORGANIZE — FR-004: per-chunk → per-package consolidated]
│   ├── SDD-CATALOG.md                        [SKIM]
│   ├── SDD-GUIDE.md                          [SKIM]
│   ├── SDD-PLAYBOOK.md                       [UPDATE — mark SDD-33 done at end of /speckit-implement]
│   ├── SECURITY.md                           [SKIM — Principles I/III/VI cross-check]
│   ├── SPEC.md                               [SKIM — AC-1..AC-10 verbatim against AC-MATRIX]
│   ├── TAILSCALE-ACLS.md                     [SKIM — already patched by SDD-30]
│   ├── TESTING-STRATEGY.md                   [VERIFY — FR-008 / SC-004: 6 fuzz target names exist]
│   └── sdd/                                  [SKIM — per-chunk contracts; cross-check chunk-doc claims]
├── scripts/                                  [NEW directory]
│   └── check-package-map-vs-code.sh          [NEW — FR-013 / SC-002 drift detector]
├── specs/                                    [MIGRATE → specs-archive/ — per FR-012 Clarification 2026-05-15 Q1]
│   └── 033-final-overhaul/                   [STAYS in specs/ — current in-flight chunk]
├── specs-archive/                            [NEW directory — receives SDD-01..32 historical artefacts]
│   ├── 001-keys-derivation/
│   ├── 002-securebytes/
│   ├── ... (32 subdirs migrated by `git mv`)
│   └── 031-release-gates/
├── tests/                                    [AUDIT — integration test paths cited in AC-MATRIX must resolve]
│   ├── deploy/
│   └── integration/
├── deploy/                                   [SKIM — already audited by SDD-30 for FR-014]
│   ├── examples/
│   ├── install.sh
│   ├── hush.plist
│   ├── hush.service
│   └── supervise-launch.sh.template
├── .github/                                  [SKIM — workflows + tech-conventions; AC-9 anchor]
│   ├── CLAUDE.md
│   ├── tech-conventions/
│   ├── workflows/                            [referenced by AC-9; do not edit]
│   └── scripts/coverage-threshold/           [constitutional FR-016 byte-equality anchor; do not edit]
└── .specify/
    └── memory/
        └── constitution.md                   [DO NOT EDIT — out of scope per chunk contract]
```

**Structure Decision**: This is a **repo-wide reconciliation sweep**,
not a new feature. The chunk's deliverables are: (a) edits to existing
files (no new packages), (b) one new shell script under a new
`scripts/` directory, (c) one new top-level directory `specs-archive/`
populated by `git mv` from `specs/`, (d) one new or extended
`CONTRIBUTING.md` documenting the archive policy. No new Go package,
no new test file (except possibly an extension of
`internal/supervise/config/example_test.go` for the whole-tree
operator-name scan — decided in research.md R-004).

## Complexity Tracking

> Fill ONLY if Constitution Check has violations that must be justified.

**No rows.** The Constitution Check above passes clean across all 11
principles. The chunk reduces complexity (drift removed, dead code
removed, documentation consolidated) rather than adding it. The single
new artefact (`scripts/check-package-map-vs-code.sh`) is justified by
FR-013 / SC-002 as a durable check making the reconciliation
repeatable; CI wiring is deliberately deferred per the spec
Clarification 2026-05-15 Q2, so the script's complexity cost is local
to operators who run it on demand.

## Constitution Check — Re-evaluation (post-Phase 1 design)

The Phase 1 outputs ([research.md](./research.md),
[data-model.md](./data-model.md), [contracts/](./contracts/),
[quickstart.md](./quickstart.md)) introduce no new architectural
surface beyond what the Pre-Phase 0 Constitution Check above already
covered. Specifically:

- **Principle I (operator-agnostic).** [research.md
  R-004](./research.md) extends the existing
  `operatorSpecificForbidden` seed list test to whole-tree scope via
  a new sibling test function in the same file. No new package; no
  new global; the seed list remains the single source of truth (its
  current value is `[]string{}` — confirmed at plan time). The
  `data-model.md` rules forbid absolute paths in `findings.jsonl`
  records (Entity 1 validation rule), preventing operator-filesystem
  leakage into a committed artefact. ✅ Still passes.
- **Principle VIII (testing discipline).** [research.md
  R-008](./research.md) codifies the pre-flight coverage baseline +
  post-removal re-check ceremony for security-critical packages,
  preserving the constitutional 100% gate. Data-model Entity 1
  disposition rules forbid `critical` + `converted-to-issue` to
  prevent silent deferral of a constitutional violation. ✅ Still
  passes.
- **Principle XI (minimal dependencies).** [contracts/check-package-map-vs-code.md](./contracts/check-package-map-vs-code.md)
  is stdlib-only (POSIX bash + Go `cmd/doc`); the Go test added by
  R-004 uses stdlib `filepath.WalkDir` + `os.ReadFile`. Zero new Go
  module dependencies; the `go.mod` diff for this chunk is expected
  to be empty. ✅ Still passes.
- **Principles II/IV/V/VI/VII/X** remain **neutral** —
  Phase 1 outputs do not touch the relevant subsystems.
- **Principles III/IX** remain **indirect** — Phase 1 outputs
  preserve the disciplines (crypto stack untouched, idiomatic Go
  patterns preserved in the one test extension).

**Outcome:** Constitution Check **passes clean post-design**. No
Complexity Tracking rows required. /speckit-tasks may proceed.
