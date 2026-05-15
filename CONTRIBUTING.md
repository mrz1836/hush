# Contributing to hush

This file documents the three repository-level policies introduced by
SDD-33 (the final repo + docs overhaul). For day-to-day Go contribution
mechanics — branching, pull-request style, pre-commit hooks, testing
conventions — see [`.github/CONTRIBUTING.md`](.github/CONTRIBUTING.md);
this file does not replace it.

The three policies below are load-bearing for the v0.1.0 release and
are referenced from
[`docs/PACKAGE-MAP.md`](docs/PACKAGE-MAP.md),
[`docs/AC-MATRIX.md`](docs/AC-MATRIX.md), and the SDD chunk-doc
catalogue. Anything that conflicts with one of them needs a chunk
doc, a Constitution amendment, or both.

## 1. Spec artefact policy (`specs/` and `specs-archive/`)

The repository follows the [Spec-Kit](https://github.com/github/spec-kit)
five-prompt SDD methodology: each chunk lives under `specs/NNN-<slug>/`
and emits `spec.md`, `plan.md`, `tasks.md`, `research.md`,
`data-model.md`, optional `contracts/`, optional `checklists/`, and
optional `quickstart.md`.

Once a chunk is **merged to master and marked `done`** in
[`docs/SDD-PLAYBOOK.md`](docs/SDD-PLAYBOOK.md), its directory is moved
from `specs/` to `specs-archive/` via `git mv` so file-rename history is
preserved. The `specs/` directory is reserved for **in-flight chunks
only** — at most one or two at a time. The 32 historical chunks
(SDD-01 through SDD-31, including the renamed SDD-24, plus this chunk
once the next one starts) live under `specs-archive/`.

**The migration is operator-driven, not automated.** There is no hook,
CI step, or release script that performs the move; the operator who
opens the next chunk's branch is responsible for migrating the previous
chunk's directory in the same PR (or in a small follow-up PR). The
rationale for keeping it manual: an automated mover risks moving a
chunk before its post-merge dust has settled, and the diff produced by
`git mv` of ~10 files per chunk is trivially reviewable on demand.

The current in-flight chunk (`specs/033-final-overhaul/`) stays in
`specs/` for the duration of its own branch and is migrated by the
**next** chunk (SDD-32, which cuts the v0.1.0 release tag).

## 2. Drift detection (`scripts/check-package-map-vs-code.sh`)

[`docs/PACKAGE-MAP.md`](docs/PACKAGE-MAP.md) ends with a fenced
**Symbol manifest** block listing `<package> <symbol>` for every
exported symbol in every `internal/*` package. The script
[`scripts/check-package-map-vs-code.sh`](scripts/check-package-map-vs-code.sh)
recomputes the symbol set from `go doc -short -all ./internal/...`
and `diff`s it against the manifest:

- exit `0` — manifest matches code; no drift.
- exit `1` — drift detected; stdout names every offending symbol with
  `+ doc-only` (manifest entry has no matching code symbol) or
  `- code-only` (code exports a symbol absent from the manifest).
- exit `2` / `3` — usage / internal error (script docstring has the
  full table).

When you add, remove, or rename an exported symbol in any `internal/*`
package, run the script before sending the PR and update the manifest
block until the script exits 0. The script header carries a self-test
recipe that injects a stub function and verifies the script catches it.

**CI wiring is intentionally deferred.** Per the SDD-33 clarification
on 2026-05-15, the script ships as a runnable repo-local gate but is
**not** wired into a blocking CI step until v0.1.0 has been
independently verified end-to-end on operator setup. A blocking gate
against a not-yet-proven system creates more friction than value.

## 3. Operator-name allowlist (`internal/supervise/config/example_test.go`)

Per Constitution Principle I and SDD-30 / SDD-33 (FR-014, SC-005),
**zero** operator-specific identifiers — operator names, hostnames,
private domain names, account handles, internal project slugs — may
appear anywhere in the committed repository tree.

The seed list lives in:

```
internal/supervise/config/example_test.go::operatorSpecificForbidden
```

The list starts empty by design; new forbidden identifiers are
appended one at a time as discoveries surface. Two Go tests enforce
the gate:

- `TestExamples_NoOperatorSpecificNames` — checks the canonical
  operator-facing supervisor TOML template at
  `deploy/examples/supervisors/example-daemon.toml` (SDD-30 scope).
- `TestExamples_NoOperatorSpecificNames_WholeTree` — walks the whole
  repository tree from the repo root, skipping documented exclusions
  (the test file itself; `specs-archive/`; `.git/`; and well-known
  binary / generated directories such as `vendor/`, `node_modules/`,
  `.idea/`, `.vscode/`).

Run either test directly with `go test`, or run the convenience
wrapper [`scripts/check-no-operator-names.sh`](scripts/check-no-operator-names.sh)
from the repo root.

Any addition to the documented exclusion set is a
Constitution-Principle-I-level decision and MUST be justified inline in
the test comment with a finding ID (the SDD-33 audit findings live at
`specs-archive/033-final-overhaul/findings.jsonl` after this chunk
merges).
