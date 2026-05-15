# Implementation Plan: Generic Supervisor Example + Verified Operator Docs (SDD-30)

**Branch**: `030-examples-and-tailscale` | **Date**: 2026-05-14 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/030-examples-and-tailscale/spec.md`

## Summary

Ship the canonical operator-facing supervisor TOML template at
`deploy/examples/supervisors/example-daemon.toml` and re-verify two
pre-existing operator-agnostic docs (`docs/TAILSCALE-ACLS.md`,
`docs/CLEAN-MACHINE.md`) against the current spec / config schema /
installer state. The template is documentation written in TOML — every
field is an active example value with a one-sentence inline comment, the
file is shipped in the repo but never loaded by any runtime path, and it
validates against the SDD-18 supervisor-config loader as-shipped. Two
tests guard the artefact: `TestExamples_GenericTOMLValidates` (loader
round-trip) and `TestExamples_NoOperatorSpecificNames` (grep gate, seed
list maintained in test source). The chunk produces no new runtime code,
no new dependencies, and no coverage delta (AC-9 unaffected).

The chunk surfaces three documentation divergences during verification
(prompt-snippet vs. SDD-18 loader; constitution-VI tag pair vs.
TAILSCALE-ACLS tag pair; install.sh banner Keychain-item name vs.
CLEAN-MACHINE.md §8). Resolutions are encoded in
[research.md](./research.md) and applied as patch-level doc edits to
TAILSCALE-ACLS.md and CLEAN-MACHINE.md inside this chunk's scope. No
constitution edits, no installer edits, no loader edits.

## Technical Context

**Language/Version**: Go 1.26.1 (pinned in `go.mod`) — for the single new
test `TestExamples_GenericTOMLValidates`. The TOML artefact itself is
plain TOML 1.0.

**Primary Dependencies**:
- `github.com/pelletier/go-toml/v2` (already an indirect dep via
  `internal/supervise/config`) — the SDD-18 loader's strict decoder; the
  test uses the package's public `Load` function, no direct TOML import.
- `github.com/stretchr/testify/{require,assert}` (already in use) for
  test assertions.
- No new direct dependencies. Constitution XI clean.

**Storage**: One static `.toml` file in `deploy/examples/supervisors/`.
Two doc files edited in place under `docs/`. No runtime artefacts.

**Testing**: Standard Go test (`go test ./internal/supervise/config/`).
No build tag needed — the test runs in the default suite alongside
existing `config_test.go` entries. A second test
`TestExamples_NoOperatorSpecificNames` (added in the /speckit-tasks
phase per FR-007) is co-located in the same file.

**Target Platform**: darwin/linux (template is operator-facing
documentation; runtime not invoked by it). Test runs in CI matrix per
SDD-31 (macos-arm64 + linux-amd64).

**Project Type**: Single Go module + repo-level documentation tree.

**Performance Goals**: N/A. Single TOML parse on test run (<10 ms);
single file-read for grep test (<5 ms).

**Constraints**: 
- Zero operator-specific identifiers anywhere in the three files
  delivered or touched by this chunk (FR-007, FR-011).
- Every CONFIG-SCHEMA.md §Supervisor-config field appears in the
  template as an active value with an inline comment (FR-002, FR-003).
- The `server_url` value must be a concrete CGNAT literal inside
  `100.64.0.0/10` that the SDD-18 loader accepts as-shipped (FR-008).
- The template must reference both `docs/TAILSCALE-ACLS.md` and
  `docs/CLEAN-MACHINE.md` in its top-of-file comment block, plus the
  per-binary Keychain ACL contract (AC-6) and the
  `[child].command[0]`-as-ACL-bound-path note (FR-006).

**Scale/Scope**: 
- 1 new TOML file (~120 lines including comments).
- 1 new test function (≤30 lines) added to an existing test file —
  `internal/supervise/config/example_test.go` (NEW, package-co-located).
- 2 doc files re-verified and patch-edited (each ≤10 changed lines
  per the divergences identified in research.md).
- 1 PACKAGE-MAP.md extension under the existing `deploy/` entry
  (deferred to /speckit-implement per chunk-doc step 6).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1
design.* The full constitution sits at
`.specify/memory/constitution.md` (version 1.1.1, ratified 2026-04-26,
last amended 2026-05-14). The chunk-doc names **Principles I and VI**
as in scope; this section walks every principle to confirm no other
principle fires.

| Principle | In scope? | Evaluation |
|-----------|-----------|------------|
| **I — Zero files at rest on agent machines** | **YES (load-bearing)** | The template MUST use placeholder secret names only (`EXAMPLE_API_KEY_1`, `REPLACE_ME`) and reference NO operator-specific identifier. Verified by `TestExamples_NoOperatorSpecificNames` (seed list per FR-007 Clarification). The template is documentation, not a runtime artefact — it cannot place secrets on disk. ✅ Pass. |
| **II — Approval is human, approval is phone** | No | Template documents how a supervisor is configured for approval routing but does not change the approval mechanism. ✅ Pass — neutral. |
| **III — Defense in depth through crypto layering** | No | No crypto code added; the template's `server_url` placeholder uses HTTP-over-Tailscale (CGNAT) consistent with v0.1.0 Layer 6 (TLS-within-Tailscale is out of scope). ✅ Pass — neutral. |
| **IV — Supervisor for daemons, wrap-shell for humans** | No | Template targets the daemon pattern only (`session_type = "supervisor"`); the schema forbids any other value. ✅ Pass — neutral. |
| **V — Staleness is visible, failure is loud** | No | Template populates validators, watchdog patterns, and grace-cache settings consistent with the schema, but does not change the staleness contract. ✅ Pass — neutral. |
| **VI — Tailscale-only, never public** | **YES (load-bearing)** | The template's `server_url` is a concrete CGNAT literal inside `100.64.0.0/10` per FR-008 (e.g., `http://100.64.0.1:7743/h/example`). The template's top-of-file comment block references `docs/TAILSCALE-ACLS.md` per FR-006. The verify-and-polish pass on `docs/TAILSCALE-ACLS.md` (FR-009) realigns its tag-naming pattern with `docs/SECURITY.md §2.3` — see research.md R-002. ✅ Pass after R-002 applied. |
| **VII — CLI design standards** | No | No CLI surface changes. ✅ Pass — neutral. |
| **VIII — Testing discipline** | YES (lightweight) | Two new tests: `TestExamples_GenericTOMLValidates` (loader round-trip) and `TestExamples_NoOperatorSpecificNames` (grep gate). Both follow the `TestFunctionName_Scenario` PascalCase convention (`.github/tech-conventions/testing-standards.md`). The chunk has no coverage target per chunk-doc; AC-9 unaffected. Mandatory fuzz target list unchanged. ✅ Pass. |
| **IX — Idiomatic Go discipline** | YES (lightweight) | Test uses the existing package-co-located pattern (`example_test.go` in `internal/supervise/config/`). No new globals, no `init()`, no panics, errors wrap with `%w`. The test is read-only, single-shot, and spawns no goroutines. ✅ Pass. |
| **X — Observability & redaction** | YES (lightweight) | The template contains zero secret VALUES — only secret NAMES (`EXAMPLE_API_KEY_1`, etc.), which are non-secret labels per the schema's design rule "secret values do not belong in config files on agent machines". The Discord snowflake placeholder is `REPLACE_ME` (an obvious marker, not a leaked ID). ✅ Pass. |
| **XI — Native-first, minimal dependencies, ephemeral vault** | YES (lightweight) | Zero new Go direct dependencies. Test uses only stdlib + already-imported test helpers + the `internal/supervise/config` package's existing `Load` function. ✅ Pass. |

**Initial Constitution Check verdict:** ✅ **PASS** — no violations,
no Complexity Tracking entries required.

**Post-Phase-1 re-check:** The Phase 1 artefacts (`data-model.md`,
`contracts/`, `quickstart.md`) introduce no new principles in scope and
no new files beyond those already counted. Re-evaluation confirmed
clean.

## Project Structure

### Documentation (this feature)

```text
specs/030-examples-and-tailscale/
├── plan.md              # This file (/speckit-plan output)
├── spec.md              # Feature spec (already created by /speckit-specify)
├── research.md          # Phase 0 output — divergence resolutions + verification methodology
├── data-model.md        # Phase 1 output — TOML field census + placeholder taxonomy
├── quickstart.md        # Phase 1 output — operator copy/paste workflow
├── contracts/           # Phase 1 output
│   ├── template-field-census.md        # Every CONFIG-SCHEMA.md §Supervisor-config field ↔ template line
│   ├── tailscale-acls-verification.md  # SECURITY.md §2.3 + CONFIG-SCHEMA.md [network] ↔ TAILSCALE-ACLS.md cross-check
│   └── clean-machine-verification.md   # deploy/install.sh ↔ CLEAN-MACHINE.md cross-check
└── tasks.md             # Phase 2 output (/speckit-tasks — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
deploy/
└── examples/
    └── supervisors/
        └── example-daemon.toml          # NEW — fully commented, fully generic supervisor TOML

docs/
├── TAILSCALE-ACLS.md                    # EDIT — patch-level alignment per research.md R-002
├── CLEAN-MACHINE.md                     # EDIT — patch-level alignment per research.md R-003
├── PACKAGE-MAP.md                       # EDIT — extend deploy/ entry (deferred to /speckit-implement)
└── AC-MATRIX.md                         # EDIT — AC-6/8/10 rows reference new file (deferred)

internal/supervise/config/
└── example_test.go                      # NEW — TestExamples_GenericTOMLValidates;
                                         #       TestExamples_NoOperatorSpecificNames added in
                                         #       /speckit-tasks per FR-007 + chunk-doc Prompt 4

CLAUDE.md                                # EDIT — SPECKIT START/END marker repointed to this plan
```

**Structure Decision**: The repo is a single Go module with a flat
top-level layout. The new template ships under `deploy/examples/` (a
pattern established by SDD-29 for operator-facing artefacts; see
`docs/PACKAGE-MAP.md` §`deploy/`). The validation test is
package-co-located in `internal/supervise/config/` rather than under
`tests/` because (a) the SDD-18 loader's public `Load` function is the
exact surface under test, (b) the existing package-test pattern already
loads fixtures via relative paths from the test source, and (c) the
chunk-doc says the test goes "added to `internal/supervise/config`
tests OR a new `deploy/examples/`-level test file" — the
package-co-located choice keeps the test pinned to the loader it
exercises and avoids a new `tests/deploy-examples/` directory.

## Phase 0 — Research Outputs

Resolved in [research.md](./research.md). Six research items resolved:

| ID | Topic | Decision |
|----|-------|----------|
| R-001 | Schema authority — user-prompt "locked HOW" snippet diverges from SDD-18 loader. | The SDD-18 loader (`internal/supervise/config`) + `docs/CONFIG-SCHEMA.md` are the source of truth per spec Assumptions; the prompt's snippet is superseded. Template structure follows `testdata/valid_maximal.toml`. |
| R-002 | Tailscale tag-naming divergence — constitution VI + SECURITY.md §2.3 use `tag:trusted/tag:sandbox`; TAILSCALE-ACLS.md uses `tag:hush-agent/tag:hush-vault`. | Patch-edit TAILSCALE-ACLS.md to acknowledge both pairs explicitly: the constitution/SECURITY.md uses `tag:trusted → tag:sandbox` as the canonical *minimum* pattern; `tag:hush-agent → tag:hush-vault` is a more descriptive operator-friendly alternative. Both are valid. No constitution edit. |
| R-003 | Keychain-item naming divergence — install.sh banner names `hush-vault-passphrase`; CLEAN-MACHINE.md §8 references `hush`, `hush-discord`, `hush-client`. | Patch-edit CLEAN-MACHINE.md §8 to list the actual entries used by install.sh + the documented bot-token + client-key entries: `hush-vault-passphrase` (vault passphrase, vault host), `hush-discord` (Discord bot token, vault host), `hush-client` (per-machine client key derivation marker, agent host). |
| R-004 | CGNAT placeholder choice for `server_url`. | `http://100.64.0.1:7743/h/example` — first usable address in `100.64.0.0/10`, port 7743 (canonical hush port), path-prefix `example` (the constituent characters are URL-safe and operator-replaceable). The loader's `validateServerURL` accepts http+host+scheme; the `[network] allowed_cidrs` Tailscale check happens at server runtime, not in the supervisor loader, so the literal is loader-clean. |
| R-005 | Test location and helper pattern. | Co-locate in `internal/supervise/config/example_test.go` (NEW file, normal test package). Test reads the template via a relative path (`../../deploy/examples/supervisors/example-daemon.toml`) and feeds it through `config.Load(context.Background(), path)`. No build tag — the test runs in the default suite. |
| R-006 | Optional-field commenting convention — clarification 2 says "Every field appears as an active example with an inline comment naming the loader default". | Inline comments follow a fixed grammar: `# <one-sentence purpose>. Default: <loader-default-or-"required">.` for optional fields; `# <one-sentence purpose>. Required.` for required fields. Top-of-file block carries the single CONFIG-SCHEMA.md anchor link (FR-003 / clarification 5). |

## Phase 1 — Design & Contracts

Phase 1 artefacts:

- [data-model.md](./data-model.md) — TOML entity layout: top-of-file
  comment block grammar, three placeholder categories (slugs, scoped
  secret names, REPLACE_ME markers), field-comment grammar, validator
  map entries.
- [contracts/template-field-census.md](./contracts/template-field-census.md)
  — Every CONFIG-SCHEMA.md §Supervisor-config field with its
  required/optional status, loader default, intended placeholder
  value, and one-sentence inline comment.
- [contracts/tailscale-acls-verification.md](./contracts/tailscale-acls-verification.md)
  — Side-by-side audit table: each TAILSCALE-ACLS.md claim ↔
  CONFIG-SCHEMA.md `[network]` clause ↔ SECURITY.md §2.3 statement;
  flagged divergence (R-002) with proposed patch.
- [contracts/clean-machine-verification.md](./contracts/clean-machine-verification.md)
  — Side-by-side audit table: each CLEAN-MACHINE.md install-time
  step ↔ deploy/install.sh action; flagged divergence (R-003) with
  proposed patch.
- [quickstart.md](./quickstart.md) — Operator copy/paste workflow:
  the five steps from "open the repo" to "first `hush supervise` boot"
  using the new template.

Agent context update: The SPECKIT START/END markers in
[CLAUDE.md](../../CLAUDE.md) are repointed to this plan at the end of
Phase 1.

## Phase 2 (Out of Scope for /speckit-plan)

`/speckit-tasks` will turn this plan into an ordered tasks.md, then
`/speckit-implement` will execute. Per the chunk-doc:

- T1 (test-first): write `TestExamples_GenericTOMLValidates` against
  a *not-yet-existing* file — confirm RED.
- T2: write `deploy/examples/supervisors/example-daemon.toml` — confirm
  the test goes GREEN.
- T3: verify-and-polish `docs/TAILSCALE-ACLS.md` per R-002.
- T4: verify-and-polish `docs/CLEAN-MACHINE.md` per R-003.
- T5: write `TestExamples_NoOperatorSpecificNames` with the seed
  forbidden list per FR-007 / clarification 1.
- T6 (gate): `magex format:fix && magex lint && magex test:race`.
- T7 (deferred edits inside /speckit-implement): PACKAGE-MAP.md `deploy/`
  extension; AC-MATRIX.md AC-6/8/10 row updates; SDD-PLAYBOOK.md SDD-30
  mark done; single combined commit.

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

Zero rows — Constitution Check passes clean both before and after
Phase 1. No principles are weakened, no exceptions taken.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none)    | (none)     | (none)                              |
