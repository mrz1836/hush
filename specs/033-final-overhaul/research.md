# Research — SDD-33 Final Repo + Docs Overhaul

This file resolves the eight `R-NNN` decisions referenced from
[plan.md](./plan.md). Each section follows the
**Decision / Rationale / Alternatives considered** format mandated by
the speckit Phase 0 outline. Decisions are load-bearing for
[tasks.md](./tasks.md) (generated next session) and the audit ordering
followed by /speckit-implement.

The chunk's spec is fully clarified — the Q&A on 2026-05-15 already
resolved the two open questions (specs/ archive policy + drift-script
CI wiring deferral). The R-NNN decisions below are HOW questions
unblocked by those clarifications.

---

## R-001 — Drift-detection script algorithm

**Decision:** `scripts/check-package-map-vs-code.sh` extracts the
**union** of all "Exported API — locked at SDD-NN" sections in
`docs/PACKAGE-MAP.md` (per-package), normalises them to a sorted
`<package> <symbol>` line set, then runs `go doc -short -all
./internal/...` per package, normalises that to the same line shape,
and `diff`s the two. Non-empty diff → exit 1 with an actionable
message naming the offending package and symbol(s) on each side
(`+ doc-only` / `- code-only`). Empty diff → exit 0.

**Rationale:**
- `go doc` is stdlib (Constitution XI clean — no new dependency).
- `-short -all` includes every exported symbol with one-line
  signatures, machine-parseable.
- The "union of locked sections" matches the as-shipped
  PACKAGE-MAP.md format **before** FR-004 reorganises it. The script
  is robust to the post-reorganisation per-package format because the
  parser keys on the `### Exported API` header pattern, not the
  per-chunk attribution.
- `diff` exit codes (0 / 1) map directly to the script's exit
  contract.

**Alternatives considered:**
- **Pure Go program under `cmd/check-package-map/`.** Rejected: adds
  a Go binary with its own `main` and test file — heavier than the
  audit problem demands. Bash + `go doc` keeps the script under 200
  lines and free of Go-build-cycle entanglement.
- **`golang.org/x/tools/go/packages` AST walk.** Rejected: same
  weight problem; requires a new direct dependency (Constitution XI
  flag) when stdlib `cmd/doc` already produces the answer.
- **Hand-curated Markdown parser (e.g., `glamour`, `goldmark`).**
  Rejected: dependency surface is not justified for a one-script
  tool.

**Edge cases handled:**
- A symbol declared with `//go:build` constraints — `go doc` reflects
  the current build tag set; the script runs without `-tags`, so
  build-tagged symbols on darwin-only or linux-only packages
  (`internal/keychain/keychain_darwin.go` etc.) appear in the doc
  output for the host platform. PACKAGE-MAP.md MUST list these
  symbols once with a `(darwin)` / `(linux)` tag in the description
  column. Drift on the off-platform half is detected on a CI matrix
  pass.
- Type-method symbols (e.g., `(*Store).Add`) — `go doc -all` renders
  these with their receiver; the script parses the receiver into the
  package-qualified key.
- Generic types — Go 1.26.1 `go doc` renders type parameters; the
  parser strips type-parameter brackets to make the comparison
  robust.

---

## R-002 — PACKAGE-MAP.md reorganisation format

**Decision:** Each `internal/*` package gets exactly **one** consolidated
section with the heading shape `## \`internal/<pkg>\`` (e.g.,
`## \`internal/server\``). Inside the section, exported symbols are
grouped under standard sub-headings:

- `### Types`
- `### Functions`
- `### Constants`
- `### Variables`
- `### Sentinel errors`

A footer at the end of each package section reads exactly:

> *(Originally locked across SDD-NN, SDD-MM, ...)*

— enumerating the chunk IDs whose "Exported API — locked at SDD-NN"
sections were absorbed (preserves the historical chunk attribution per
FR-004). Sub-sections inside the package retain their existing tabular
format (no migration of cell-level content beyond reordering).

**Rationale:**
- Matches the existing prose style of PACKAGE-MAP.md (already uses
  `## \`<pkg>\``-style headings for the design-goal narrative — see
  lines 44, 72, 224 of the as-shipped file).
- The footer captures the audit-trail value of "which chunk first
  shipped this symbol" without scattering it across 32 appended
  sections.
- The five sub-headings cover the entire exported-symbol vocabulary
  observed in the as-shipped PACKAGE-MAP.md (Types / Functions /
  Constants / Variables / Sentinel errors). Adding more sub-headings
  on demand is fine; deleting any of them when a package has no such
  members is fine.
- A reviewer comparing `go doc ./internal/<pkg>` to the doc lands on
  one section, not 1..N appended ones.

**Alternatives considered:**
- **Keep the appended-per-chunk format and only deduplicate.**
  Rejected: the spec FR-004 explicitly mandates one consolidated
  section per package; reviewer ergonomics is the load-bearing
  reason.
- **Move per-chunk attribution to a separate `docs/PACKAGE-API-HISTORY.md`.**
  Rejected: doubles the doc surface and creates two-place
  maintenance overhead. Inline footer is sufficient.
- **Drop attribution entirely.** Rejected: the chunk contract makes
  it explicit that the historical attribution must be preserved as
  metadata.

---

## R-003 — `specs/` → `specs-archive/` migration mechanic

**Decision:** Use `git mv specs/NNN-*/ specs-archive/` per directory
(in a single batch — the `git mv` calls live in the
/speckit-implement step, not in this plan). `git mv` preserves
file-rename history so `git log --follow` still works on individual
artefacts. The current in-flight chunk's directory
(`specs/033-final-overhaul/`) **stays in `specs/`** — it is migrated
post-merge by SDD-32 (or in a follow-up cleanup pass), since moving
it under its own feature branch would risk a self-referential rename
mid-PR. Future SDD chunks (post-overhaul, starting with SDD-32 if any)
generate their `spec.md` under `specs/NNN-*/`, then the post-implement
hook (or operator manually) moves to `specs-archive/` after merge —
the policy's enforcement mechanism is documented in CONTRIBUTING.md
(see R-005).

**Rationale:**
- `git mv` is the lossless, history-preserving option. `mv` then
  `git add`/`git rm` would also work but loses the rename-detection
  guarantee on older Git versions.
- Excluding the in-flight directory avoids a mid-branch rename of
  the active chunk's own artefacts (would create a circular
  reference in the chunk's commit message).
- 33 directories observed by `ls /Users/mrz/projects/hush/specs/`
  (at plan time): SDD-01..SDD-23, SDD-025 (lifecycle harness), SDD-026
  (validators-builtins) and SDD-026 (supervisor-orchestration —
  duplicate-id! — see R-007 finding), SDD-027..SDD-031, SDD-033. The
  SDD-026 duplicate-id is a real finding to be raised in
  /speckit-implement (audit category B or G, depending on
  disposition).

**Alternatives considered:**
- **`.gitignore` instead of move.** Rejected by Clarification
  2026-05-15 Q1 (operator chose Option B = move-to-sibling).
- **Delete from history with `git filter-repo`.** Rejected: history
  rewrite is destructive and not in scope for this chunk.
- **Keep in-tree under `specs/` but reorganise by phase.** Rejected:
  the appended-per-chunk-by-number ordering is the existing
  convention; reorganising adds churn without clarity.

**Implementation note:** The /speckit-implement step that runs
`git mv` should produce one commit per move (or one batched commit
covering all moves) with a clear message like
`chore: archive SDD-NN spec artefacts to specs-archive/ (SDD-33)`.

---

## R-004 — Operator-name allowlist whole-tree scan

**Decision:** Extend the existing `operatorSpecificForbidden` seed
list (in `internal/supervise/config/example_test.go`) **without
adding a new test file**. Add a new test function in the same file
named `TestExamples_NoOperatorSpecificNames_WholeTree` that:

1. Reads the same `operatorSpecificForbidden` slice (single source of
   truth for the seed list, per Constitution I and SDD-30
   precedent).
2. Walks the entire repo tree from the repo root (relative-path
   `../../../`), excluding **only**: (a) the test file itself
   (`example_test.go`), (b) `specs-archive/` (per FR-014 documented
   exclusion — captures pre-reconciliation snapshots), (c) the `.git/`
   directory.
3. For each non-binary file, asserts no forbidden token appears.
4. Failing assertions name the file path and line number for fast
   triage.

**Rationale:**
- Avoids creating a new test package solely for a one-test concern
  — `TestExamples_NoOperatorSpecificNames` already lives in this
  file, the new function is a topical sibling.
- Single seed list (`operatorSpecificForbidden` slice) means
  Constitution I has one machine-readable source of truth.
- Uses standard `filepath.WalkDir` + `os.ReadFile` — no new
  dependency; runtime well under 1 second on the as-shipped tree.
- Excluding `specs-archive/` is documented in [plan.md
  Constitution Check Principle I](./plan.md) — those artefacts are
  pre-reconciliation snapshots and their forbidden-name content (if
  any historical leaks exist there) is preserved for audit purposes,
  not corrected.

**Alternatives considered:**
- **Standalone shell script `scripts/check-no-operator-names.sh`.**
  Considered. The chunk-doc Implement step §4 even names this
  script. Decision: ship the **Go test** as the load-bearing gate
  (runs under `go test ./...`, blocks PR via existing `magex
  test:race` CI step). The shell script is **also** shipped as
  operator ergonomics for ad-hoc local checks (lower priority — it
  may slip to a follow-on chunk if /speckit-implement overruns;
  document as minor finding if so).
- **Lift the seed list to a new package
  `internal/testutil/operatornames`.** Rejected: the seed list is
  empty at plan time; promoting an empty constant to a public
  package is premature abstraction.
- **Use `gitleaks` regex.** Rejected: `gitleaks` is for secret-shape
  patterns (entropy, prefix patterns); operator-name allowlist is a
  named list of identifiers, not a pattern. Wrong tool.

**Note on FR-014 exclusion list documentation:** The exclusions live
in two places that must agree:

1. The test code itself (`TestExamples_NoOperatorSpecificNames_WholeTree`
   excludes the three paths above).
2. The new `CONTRIBUTING.md` section (per R-005) documents the
   policy and the rationale.

If the two diverge, the test is authoritative. Drift between the two
is itself a finding for the next overhaul chunk.

---

## R-005 — `CONTRIBUTING.md` location and content

**Decision:** Create a **new top-level `CONTRIBUTING.md`** at the repo
root. The existing `.github/CONTRIBUTING.md` is preserved (GitHub UI
references both locations; the root version is canonical for
contributor-facing prose, and the `.github/` version remains as the
machine-readable contributor entry point GitHub auto-discovers). The
root `CONTRIBUTING.md` adds three new sections:

1. **Spec artefact policy** — explains the `specs-archive/` move
   (Clarification 2026-05-15 Q1), why historical artefacts are
   retained committed, and how new SDD chunks generate `spec.md`
   under `specs/NNN-*/` and graduate to `specs-archive/` post-merge.
2. **Drift-detection** — names `scripts/check-package-map-vs-code.sh`,
   describes when to run it (before opening any PR that touches
   `internal/*` exported symbols), and notes the deliberate CI-wiring
   deferral (Clarification 2026-05-15 Q2).
3. **Operator-name allowlist** — names the seed list
   (`internal/supervise/config/example_test.go::operatorSpecificForbidden`),
   the documented exclusions, and the procedure for adding a new
   forbidden identifier (one-at-a-time, per Constitution I).

If the two `CONTRIBUTING.md` files conflict in the future, the root
file is canonical; the `.github/CONTRIBUTING.md` is intentionally a
short pointer.

**Rationale:**
- A repo-root `CONTRIBUTING.md` is the OSS convention; GitHub's UI
  surfaces it on the first contributor visit.
- The existing `.github/CONTRIBUTING.md` is presumably already used
  by operators familiar with the dotfile-conventions style; deleting
  it would be a silent doc drop forbidden by FR-019.
- Three sections cover the three policies introduced or anchored by
  this chunk; future chunks extend.

**Alternatives considered:**
- **Extend `.github/CONTRIBUTING.md` only.** Rejected: less
  discoverable for first-time contributors browsing on github.com.
- **Put policy in `docs/SDD-PLAYBOOK.md`.** Rejected: SDD-PLAYBOOK is
  the chunk-progress dashboard, not a contributor-onboarding
  document.
- **No CONTRIBUTING.md at all; document inline in the PR description
  of SDD-33.** Rejected: PR descriptions are not discoverable;
  policy must live in-repo.

---

## R-006 — README.md rewrite scope (surgical edit vs. rewrite)

**Decision:** **Surgical edit, not from-scratch rewrite.** The
as-shipped `README.md` (221 lines, last polished by SDD-32 partial
delivery per `docs/SDD-PLAYBOOK.md`) already lays out the threat model,
seven security layers, Tailscale + Discord + ECIES claims, and a
reasonable feature-doc index. The audit step (FR-009 / SC-001 / FR-010)
checks each claim against the as-built code and the chunk applies
**targeted edits** for any gap.

Anticipated edits (decided in /speckit-implement, not here):

- **Add a quick-start section** if missing — FR-010 demands a fresh
  reader can complete one round-trip from the README alone. The
  current README has no quick-start; this is the most likely
  rewrite.
- **Verify badges** point to real workflows. Current README cites
  `release-gates.yml`; the SDD-31 workflow is named differently in
  `.github/workflows/`. Cross-check during /speckit-implement.
- **Verify the "Tech stack" section** lists every shipped Go
  dependency (and ONLY shipped ones). E.g., `go-bitcoin` reference
  must match `go.mod`.
- **Verify the "Documentation" table** — every cited doc exists and
  the description matches.
- **Cross-check Architecture summary** against `docs/ARCHITECTURE.md`
  — the README excerpt may have drifted.

The from-scratch rewrite path is reserved for the case where the
audit reveals more than ~15 inaccuracies; the surgical-edit path is
chosen when the count is ≤15.

**Rationale:**
- The current README is already polished prose by SDD-32 partial
  work; throwing it away is wasteful and risks introducing new gaps.
- FR-010 is satisfied by adding a quick-start section; the rest of
  the README is value-additive context.
- Surgical edit keeps the diff reviewable.

**Alternatives considered:**
- **Full rewrite from a SPEC.md / ARCHITECTURE.md merge.** Rejected
  unless audit reveals widespread drift.
- **Branch into README.md + README-DEEP.md.** Rejected: doubles doc
  surface for no compensating clarity.

**FR-010 quick-start verification step:** /speckit-implement Step 5
(README.md quick-start dry-run on a fresh shell) is the load-bearing
test. If a fresh shell cannot complete one approve / fetch / inject
round-trip from the README alone, the README rewrite is incomplete.

---

## R-007 — Naming-consistency rename criteria

**Decision:** Renames are **strongly disfavoured** in this chunk and
permitted only when **all three** conditions hold:

1. PACKAGE-MAP.md drift demands the rename (FR-005 — code says
   `Approver`, doc says `Approval`, and the doc is the locked
   contract).
2. The rename is **internal-to-internal** — no exported symbol of
   `internal/cli` (which is reachable from `cmd/hush`) is touched.
   The exit-code constants (`ExitOK`, `ExitErr`, `ExitInputErr`,
   `ExitAuth`, `ExitNotFound`, `ExitPerm`, `ExitConfigStale`) and
   `Execute(ctx) int` are off-limits per FR-016.
3. The rename is announced in the combined commit message as an
   intentional breaking change; PACKAGE-MAP.md and any cross-doc
   reference are updated in the same commit.

If any of the three conditions fail, the chunk **documents the
inconsistency in PACKAGE-MAP.md** and leaves the code alone (FR-002
second branch).

**Rationale:**
- Renames are the highest-risk action in a sweep chunk — silent
  breakage of integration tests, downstream daemons (OpenClaw,
  Hermes), or operator scripts.
- The chunk's purpose is reconciliation, not refactoring; "documented
  inconsistency" is a perfectly valid disposition.
- The three-condition gate is conservative by design — renames are
  the expensive option.

**Alternatives considered:**
- **Permit any internal-package rename freely.** Rejected: tests in
  one package may import symbols from another by string (struct
  tags, reflection-based test setup); renames cascade unexpectedly.
- **Forbid all renames.** Rejected: FR-005 drift may genuinely
  demand a rename when PACKAGE-MAP.md is the canonical contract and
  the code violates it.

**Naming patterns to grep during audit (A.4):** `Approver` vs
`Approval`, `Refresh` vs `Refill`, `Validator` vs `Validate`,
`Supervise` vs `Supervisor` (function-name vs noun convention),
`Server` vs `Service`, `Acquire` vs `Lock` (PIDFile semantics),
`Issue` vs `Mint` (token issuance). Each grep produces a candidate
list; each candidate is evaluated against the three-condition gate.

---

## R-008 — Coverage-discipline preservation under dead-export removal

**Decision:** A dead-exported symbol in a security-critical package
(per Constitution VIII fenced block: `internal/keys`,
`internal/vault`, `internal/vault/securebytes`, `internal/token`,
`internal/transport/sign`, `internal/transport/ecies`,
`internal/audit`) may be removed **only when**:

1. `grep -rn '<pkg>.<Symbol>' --include='*.go' .` returns zero
   non-test, non-self-package matches.
2. The symbol is NOT listed in PACKAGE-MAP.md under that package's
   locked API.
3. After the removal AND the corresponding test removal, `magex
   test:coverrace` shows **100% coverage on the package** (the
   package's coverage gate per the constitutional fenced block).
4. The removal is recorded as a **major finding** in the FINDINGS
   list (security-critical package symbol removal is never a minor
   finding).

For non-security-critical packages, the same checks apply, but the
coverage threshold is the per-package target documented in
`docs/AC-MATRIX.md` "Coverage targets per package" (95% / 85% / etc.).

**Rationale:**
- A dead symbol with associated tests inflates coverage falsely;
  removing both the symbol and its tests preserves the same
  coverage shape.
- Security-critical packages have 100% coverage by constitutional
  mandate (Principle VIII) AND a byte-equality CI gate
  (`.github/scripts/coverage-threshold/compute.go`); editing these
  packages is an explicit risk that requires major-finding
  ceremony.
- The fenced block in the constitution (lines 253-261) is
  byte-equality-asserted; the chunk MUST NOT touch it.

**Alternatives considered:**
- **Refuse all removal in security-critical packages.** Rejected:
  truly dead code is dead code regardless of package; refusing to
  remove it leaves the constitutional Principle XI ("smallest
  dependency surface is the strongest") in tension with the
  coverage gate. A clean removal with coverage preserved is the
  right answer.
- **Lower coverage threshold to permit removal without test
  changes.** Rejected — this would amend the constitution, which is
  out of scope for SDD-33.
- **Mark dead exports in security-critical packages as
  `//nolint:unused` and leave them.** Rejected: the audit's purpose
  is to remove them.

**Pre-flight check for /speckit-implement:** Before any removal in
a security-critical package, the implementer runs
`magex test:coverrace -run '^Test' ./internal/<pkg>/...` and records
the baseline coverage. After removal, the same command must produce
the same or higher coverage on the package.

---

## Cross-cutting findings already surfaced by Phase 0 recon

The /speckit-plan reconnaissance already turned up two findings worth
recording before /speckit-implement starts the formal audit:

### Pre-finding F-PRE-1 — Six fuzz targets all exist (FR-008 / SC-004 expected to PASS)

`grep -rn 'func Fuzz' --include='*.go' /Users/mrz/projects/hush/internal/`
returned all six constitutional targets with their documented names:
`FuzzVaultDecode`, `FuzzJWTValidate`, `FuzzECIESDecrypt`,
`FuzzVerifyRequest`, `FuzzServerTOML`, `FuzzSuperviseTOML`. Plus two
extras: `FuzzDeriveMaster` (in `internal/keys`) and
`FuzzStatusJSON_Encode` (in `internal/supervise`). Severity: NONE
(this is a pass, not a finding). /speckit-implement category E re-runs
the check against the as-shipped tree.

### Pre-finding F-PRE-2 — Duplicate `026-` directory in `specs/` (FR-011 / G-10)

`ls /Users/mrz/projects/hush/specs/` shows two directories starting
with `026-`: `026-supervisor-orchestration` and
`026-validators-builtins`. The intended chunk-ID convention is one
directory per chunk; the duplicate-026 reflects a chunk-ID collision
during SDD-24 activation (per `docs/sdd/SDD-24.md`'s history,
SDD-24 was activated by SDD-25 mid-cycle). **Severity: minor.**
**Disposition: rename one of the two during /speckit-implement,
record in IMPLEMENTATION-PLAN.md actuals (FR-011), preserve git
history via `git mv`.** The rename target is decided in
/speckit-implement after consulting `docs/sdd/SDD-24.md` and
`docs/sdd/SDD-26.md` to identify which is the orchestration chunk
and which is the validators chunk.

### Pre-finding F-PRE-3 — `docs/SDD-PLAYBOOK.md` SDD-25 status is stale (FR-011)

The playbook lists SDD-25 as `in-progress` (chunk 1 only — Scenario
14 green). Recent commits (`e273b09 test(integration): land scenarios
11a, 11b, 12 plus chassis smoke`) show SDD-25 chunk-2 has landed
(scenarios 11a/11b/12 also green per AC-MATRIX). The playbook narrative
has not absorbed this. **Severity: minor.** **Disposition: update
SDD-PLAYBOOK.md SDD-25 row during /speckit-implement to reflect 4/17
scenarios green, or whatever the actual count is at that time.**

### Pre-finding F-PRE-4 — `CONTRIBUTING.md` only exists at `.github/`, not at repo root

Per R-005 above, the chunk creates a new repo-root `CONTRIBUTING.md`.
The `.github/` file is preserved as a pointer. **Severity: trivial /
intended action, not a finding per se.**

---

## Outstanding `NEEDS CLARIFICATION` markers

**None.** All technical decisions resolved above; the spec's two
operator-facing clarifications (Q1 archive policy, Q2 CI deferral)
were already settled in the spec's Clarifications section. The plan
moves to Phase 1 design.
