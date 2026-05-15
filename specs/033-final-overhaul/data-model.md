# Data Model — SDD-33 Final Repo + Docs Overhaul

This chunk does not introduce a runtime data model — it adds **no new
Go types, no new structs, no new database, no new on-disk format**.
The "data" SDD-33 produces is a **list of FINDINGS** captured during
the audit and the **reorganised entries in PACKAGE-MAP.md**. Both are
documented as entities below so /speckit-tasks can structure the audit
output consistently and so /speckit-implement has a typed schema to
populate.

---

## Entity 1 — Finding

A `Finding` is one discovered drift, inconsistency, or violation
surfaced during the audit. The chunk produces a **FINDINGS list**
(JSONL or markdown table; format pinned by
[contracts/audit-findings.md](./contracts/audit-findings.md)).

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | yes | Stable identifier of the form `F-<NNN>` (zero-padded; assigned in audit order, never reused). |
| `severity` | enum | yes | One of `critical`, `major`, `minor`. Disposition rules below. |
| `category` | enum | yes | One of `A` (code audit), `B` (PACKAGE-MAP), `C` (AC-MATRIX), `D` (ARCHITECTURE), `E` (TESTING-STRATEGY fuzz), `F` (README), `G` (IMPLEMENTATION-PLAN), `H` (specs/ archive), `I` (drift script), `J` (operator-name leak), `K` (constitution recompliance). |
| `subcategory` | enum (optional per A) | no | For category `A`: `A1` (dead exports), `A2` (cross-pkg usage), `A3` (TODO/FIXME/XXX), `A4` (naming consistency). For other categories: omit. |
| `location` | string | yes | File path (repo-relative), optionally `:LINE` suffix. Multi-file findings use a comma-separated list. |
| `description` | string | yes | One-sentence statement of the drift / inconsistency / violation. |
| `disposition` | enum | yes (post-fix) | One of `resolved` (fixed in this chunk), `converted-to-issue` (replaced by `// see #N` and a GitHub issue), `deferred-to-followup` (minor only; recorded in implement message). |
| `disposition_ref` | string | conditional | If `converted-to-issue`: the GitHub issue number (e.g. `#42`). If `resolved`: the commit SHA-prefix or N/A. If `deferred-to-followup`: the follow-up chunk ID or `TBD`. |
| `discovered_at` | timestamp (RFC3339) | yes | When the finding was recorded during /speckit-implement. |

### Disposition rules

| Severity | Allowed dispositions | Blocks chunk completion? |
|----------|---------------------|--------------------------|
| `critical` | `resolved` only | YES — chunk MUST NOT complete with any unresolved critical finding (FR-020, spec User Story 4 acceptance scenario 3). |
| `major` | `resolved` or `converted-to-issue` | NO, but each finding MUST have one of the two dispositions before chunk completes (FR-020). |
| `minor` | any of the three | NO — `deferred-to-followup` permitted; documented in implement message. |

### State transitions

```
discovered ──(audit step)──► open
open ──(category-specific fix)──► resolved
open ──(gh issue create + // see #N)──► converted-to-issue
open ──(triage decides minor + future)──► deferred-to-followup
```

Once a finding reaches one of the three terminal states, it MUST NOT
transition again within this chunk. A subsequent chunk that picks up
a `deferred-to-followup` finding creates a NEW finding under its own
F-NNN id, optionally cross-referencing the previous one.

### Validation rules

- `id` is unique within the chunk's FINDINGS list.
- `severity = critical` AND `category ∈ {A, C, E, K}` is the most
  common critical-finding shape (a missing AC-cited test path; a
  missing constitutional fuzz target; a constitutional principle
  violated by the as-built code; a non-removable dead export
  collision). Other category/severity combinations are valid but
  rare.
- `severity = critical` AND `disposition = converted-to-issue` is
  ILLEGAL (critical findings cannot be deferred).
- `location` MUST be a repo-relative path (no absolute paths in the
  FINDINGS list — they leak operator filesystem layout).

### Volume estimate

Plan-time estimate (subject to revision in /speckit-implement):
critical: 0; major: 5–15; minor: 15–40. Total: 30–60 findings across
A..K. The two pre-findings already surfaced in
[research.md](./research.md) (F-PRE-2 duplicate `026-` directory,
F-PRE-3 stale SDD-25 status) become F-001 and F-002 (or near the top
of the list) when /speckit-implement runs.

---

## Entity 2 — Locked Exported API entry

A `LockedExportedAPIEntry` is one row in the reorganised
`docs/PACKAGE-MAP.md` describing one exported symbol of a given
package. The shape is documented for /speckit-implement so the
reorganisation produces a uniform table format across all 19
internal packages.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `package` | string | yes | Package import path (e.g. `internal/server`). |
| `symbol_kind` | enum | yes | One of `Type`, `Function`, `Constant`, `Variable`, `SentinelError`. Drives which sub-table the entry lives in. |
| `name` | string | yes | The exported identifier (e.g. `ListenAndServe`, `ErrTOMLDecode`, `ExitOK`). |
| `signature` | string | yes (functions, methods, constants, variables) | One-line Go signature as printed by `go doc -short`. For `Type`, the type kind (`struct`, `interface`, `alias`) plus a brief shape note. For `SentinelError`, the wrapped error chain ("wraps `fs.ErrNotExist`" or "—"). |
| `description` | string | yes | One-sentence purpose, copied verbatim from the existing PACKAGE-MAP.md entry where one exists; freshly written where missing. |
| `originally_locked_in` | list[string] | yes | The chunk IDs whose "Exported API — locked at SDD-NN" sections originally introduced this entry (e.g., `["SDD-14"]` or `["SDD-14", "SDD-23"]` for symbols extended later). Concatenated into the per-package footer per [research.md R-002](./research.md). |

### Validation rules

- `(package, name)` is unique across the consolidated PACKAGE-MAP.md.
- `symbol_kind = SentinelError` AND `name` MUST start with `Err` per
  Go convention (Constitution IX).
- `originally_locked_in` is non-empty (every entry was introduced by
  some chunk).
- The set of `(package, name)` rows MUST be **byte-equal** to the
  set produced by `go doc -short -all ./<package>` after applying
  the kind/qualifier normalisation documented in [research.md
  R-001](./research.md). This is the FR-005 invariant the new
  drift-detection script enforces.

### Per-package section template (after FR-004 reorganisation)

```markdown
## `internal/<pkg>`

[1-paragraph package purpose — copied from existing prose section]

### Types

| Symbol | Description |
|--------|-------------|
| `type Foo struct { ... }` | Purpose statement. |
| ...    | ...                  |

### Functions

| Symbol | Description |
|--------|-------------|
| `func Bar(ctx context.Context, x int) error` | Purpose statement. |
| ...    | ...                  |

### Constants

| Symbol | Value | Meaning |
|--------|-------|---------|
| `MaxRetries` | `5` | Purpose. |
| ...          | ... | ...     |

### Variables (set-once / build-time injected)

| Symbol | Type | Default | Description |
|--------|------|---------|-------------|
| `Version` | `string` | `"dev"` | Build-time injected. |

### Sentinel errors

| Symbol | Wraps | Triggered by |
|--------|-------|-------------|
| `ErrFoo` | — | When X happens. |
| `ErrBar` | `fs.ErrNotExist` | When Y. |

*(Originally locked across SDD-NN, SDD-MM, ...)*
```

Sub-tables with zero rows are omitted entirely (no empty placeholders).

---

## Entity 3 — Cross-doc reference (verification only)

A `CrossDocReference` is a citation in one document pointing at another
document, file, or symbol. SDD-33 does not store these; it audits
them. The shape is documented so the audit's verification step has a
typed checklist.

### Audit checklist (per category)

- **C-6 (AC-MATRIX.md):** Every cited test path resolves to an
  existing file. Every test name resolves to a `func Test*` symbol.
- **D-7 (ARCHITECTURE.md):** Every package shown in the diagram
  appears in `go list ./internal/...`. Every import edge in `go
  list -f '{{.ImportPath}} {{.Imports}}' ./...` is represented in
  the diagram (or omitted with documented reason).
- **E-8 (TESTING-STRATEGY.md):** Every fuzz target name in §2
  resolves to exactly one `func Fuzz*` symbol in code.
- **F-9 (README.md):** Every flag, subcommand, OS, and config field
  cited resolves to the as-built behaviour. Every doc link
  resolves to an existing file.
- **G-10 (IMPLEMENTATION-PLAN.md):** Every chunk listed in the
  phase-to-chunk map exists in `docs/SDD-PLAYBOOK.md` with its
  stated status.

A failed citation is a Finding (typically major) with the failing
file as `location` and the broken citation as `description`.

---

## Out-of-scope: runtime data model changes

Explicitly out of scope (FR-016, FR-017):

- No new Go struct
- No new database / store / index
- No new on-disk file format
- No new HTTP wire format
- No new socket protocol
- No new config field

The chunk operates on **documentation entities** and **process
artefacts** (the FINDINGS list). Any audit-discovered need for a new
runtime entity becomes a Finding with disposition
`converted-to-issue` (the work belongs to a future chunk).
