# SDD-31 — Release gates (coverage + 6 fuzz + magex + go-pre-commit + govulncheck + gitleaks + CGO=0 + no /vendor)

**Phase:** 8
**Package:** CI / repo-level
**Files:** `.github/workflows/*.yml`, `.golangci.json` (review), `.goreleaser.yml` (review)
**Branch:** `031-release-gates` (created by the `before_specify` git hook)
**Blocked by:** SDD-25 + every prior chunk
**Blocks:** SDD-32
**Primary AC:** AC-9
**Coverage target:** project-wide ≥ 90%; security-critical packages 100%

**Behaviour contracts (MUST):**
- CI matrix: macOS-arm64, ubuntu-amd64; Go 1.26
- Workflow steps: `magex format:fix --check`, `magex lint`, `magex test:race`, `go test -fuzz` on each fuzz target (cron + 30s smoke per PR), `go-pre-commit`, `govulncheck`, `gitleaks`, coverage report with codecov upload
- `.goreleaser.yml`: `CGO_ENABLED=0` in env; `darwin/linux × amd64/arm64`
- A check that fails CI if `/vendor` exists
- Coverage threshold check: total ≥ 90%, security-critical pkgs = 100%

**Anti-contracts (MUST NOT):**
- Skip fuzz targets to make CI faster (run as cron if PR is too slow)
- Disable race detector
- Allow CGO

**Tests required:**
- A green CI run is the test (can't TDD a CI workflow itself; instead, the chunk is validated by an end-to-end CI run on a sample PR)

**Constitutional principles in scope:** VIII (coverage + fuzz gates), XI (CGO=0, no `/vendor`)

**Exported API to lock in PACKAGE-MAP.md (this chunk — new entry):**
- `.github/workflows/`: described as "release-gate workflows — see SDD-31". The locked contract is which workflow exists and what each step does, not a Go API.

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

This chunk is CI plumbing. The "test" is a green CI run on a
sample PR — implement carefully and validate iteratively.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-31 (release gates: coverage
+ 6 fuzz + magex + govulncheck + gitleaks + CGO=0 + no /vendor) of
the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles VIII, XI; Code Quality Gates section)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md  (entire — coverage targets, fuzz targets, gates)
- Existing .github/workflows/* files (read whatever exists)
- /Users/mrz/projects/hush/.golangci.json  (review)
- /Users/mrz/projects/hush/.goreleaser.yml  (review)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-9 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-31.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
This chunk wires every gate from Constitution VIII into CI:
formatting, linting, race-detector tests, fuzz coverage, vulnerability
scanning, secret scanning, coverage thresholds (project-wide ≥ 90%,
security-critical packages = 100%), no-CGO enforcement, and
no-/vendor enforcement. A green CI run on a sample PR is the
acceptance test for AC-9.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- CI runs on macOS-arm64 AND ubuntu-amd64 (matrix), Go 1.26.
- Per PR: magex format:fix --check, magex lint, magex test:race,
  go-pre-commit, govulncheck, gitleaks, 30s fuzz smoke per
  fuzz target, codecov upload.
- Cron (nightly): 60s+ fuzz run per fuzz target (the 6 documented
  in Constitution VIII / TESTING-STRATEGY.md).
- A dedicated CI step fails if /vendor exists (Constitution XI).
- A dedicated CI step fails if any non-test file imports cgo
  or if CGO_ENABLED is non-zero anywhere (Constitution XI).
- Coverage threshold gate: project-wide ≥ 90%, security-critical
  packages (internal/keys, internal/vault, internal/vault/securebytes,
  internal/token, internal/transport/sign, internal/transport/ecies,
  internal/audit) = 100%. Failure of either threshold fails CI.
- .goreleaser.yml has CGO_ENABLED=0 in env; produces darwin and
  linux binaries for amd64 and arm64.
- The fuzz steps MUST NOT be downgraded to skip targets to save
  CI time — if too slow, move to cron.
- The race detector MUST be enabled in the test step.

The spec MUST NOT encode HOW (no specific GitHub Actions versions,
no specific YAML structure beyond what the gates require). Those
are plan-phase.

Acceptance criterion: AC-9 (test infra completeness — the gates
on the green-CI bar).

Action — run exactly one command:
  /speckit-specify "release gates in CI: matrix macOS-arm64 + ubuntu-amd64 on Go 1.26; per-PR steps (format check, lint, test:race, go-pre-commit, govulncheck, gitleaks, 30s fuzz smoke, codecov upload) + nightly cron (60s+ fuzz per target); enforce no /vendor and CGO_ENABLED=0; coverage thresholds (project ≥90%, security-critical = 100%); .goreleaser.yml produces signed darwin/linux × amd64/arm64 binaries"

The before_specify hook will create branch 031-release-gates.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution / existing CI
files. Otherwise leave the marker — /speckit-clarify will handle
it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-31 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-31.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-31 (release gates) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; VIII/XI load-bearing)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md  (entire — every fuzz target listed here MUST appear in CI)
- Existing .github/workflows/* files
- /Users/mrz/projects/hush/.golangci.json  (review)
- /Users/mrz/projects/hush/.goreleaser.yml  (review)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (no .github/ entry yet)
- /Users/mrz/projects/hush/docs/sdd/SDD-31.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- .github/workflows/ci.yml (per-PR matrix: format check, lint,
  test:race, go-pre-commit, govulncheck, gitleaks, 30s fuzz
  smoke per target, codecov upload)
- .github/workflows/fuzz-cron.yml (nightly cron: 5min fuzz per
  target — or longer; budget per Constitution VIII)
- .github/workflows/release.yml (tag → goreleaser publish)
- .github/workflows/no-vendor-no-cgo.yml (or a step inside
  ci.yml) — fails if /vendor exists or any source file uses cgo
- .golangci.json review (ensure linters cover the gates)
- .goreleaser.yml review (CGO_ENABLED=0 in env; matrix
  darwin/linux × amd64/arm64; signed checksums)
- A coverage-threshold script under .github/scripts/ that reads
  the codecov report and fails if project < 90% or any
  security-critical package < 100%

Implementation contract (HOW — locked):
- CI matrix: GOOS=darwin GOARCH=arm64 + GOOS=linux GOARCH=amd64;
  Go 1.26; uses actions/setup-go and actions/cache for module
  cache.
- format check: `magex format:fix --check` (fails on diff, doesn't
  modify files in CI).
- lint: `magex lint` (delegates to .golangci.json).
- test: `magex test:race` (race detector required).
- fuzz smoke per PR: 30s per fuzz target (the 6 from TESTING-
  STRATEGY.md / Constitution VIII):
    FuzzVaultDecode, FuzzJWTValidate, FuzzECIESDecrypt,
    FuzzVerifyRequest, FuzzServerTOML, FuzzSuperviseTOML
- fuzz cron: 5min per target (configurable in cron yml).
- go-pre-commit: invoke the existing pre-commit hook config in
  CI to ensure consistency with local dev.
- govulncheck: official tool; fails CI on any HIGH severity.
- gitleaks: with the existing .gitleaks.toml if present, else
  default config; fails CI on any leak.
- no-vendor: `test ! -d vendor` step.
- no-cgo: `CGO_ENABLED=0` env on every build/test step + a
  `! grep -r 'import "C"' --include='*.go' .` check.
- Coverage threshold script: parses cover.out; computes per-pkg
  coverage; security-critical list is hardcoded (matches the
  Constitution VIII security-critical set).
- .goreleaser.yml: env: [CGO_ENABLED=0]; targets darwin/linux
  arm64+amd64; produces SHA256SUMS + signed (cosign or signed
  via the GoReleaser action).

Coverage target: project-wide ≥ 90%; security-critical = 100%.
Constitutional principles in scope: VIII, IX, X, XI.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-31 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-31.md.

Run:
  /speckit-tasks "Tasks: write the coverage-threshold script in .github/scripts/ FIRST (so we can test it in isolation against a sample cover.out fixture), then write each workflow YAML, then validate. Per-workflow tasks: ci.yml (format check + lint + test:race + go-pre-commit + govulncheck + gitleaks + 30s fuzz × 6 targets + codecov upload + no-vendor check + no-cgo check), fuzz-cron.yml (5min × 6 targets nightly), release.yml (tag → GoReleaser). Plus tasks: review .golangci.json, review .goreleaser.yml. Test tasks: TestCoverageThreshold_ProjectGEThreshold, TestCoverageThreshold_SecurityCriticalEQ100, TestCoverageThreshold_FailsBelowThreshold. Validation: run a sample PR through CI; assert all gates pass. Final phase MUST include magex format:fix, magex lint, magex test:race, AND a manual verification that a sample PR triggers all CI steps."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-31 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-31.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Coverage threshold script self-tests:
     go test ./.github/scripts/...
3. Local sanity:
     test ! -d vendor && echo "no vendor: ok"
     ! grep -r 'import "C"' --include='*.go' . && echo "no cgo: ok"
     CGO_ENABLED=0 go build ./...
4. Open a SAMPLE PR (or push a draft branch) and confirm the
   CI matrix runs and every gate passes:
     - format check
     - lint
     - test:race (both OS × arch)
     - go-pre-commit
     - govulncheck (no HIGH severity)
     - gitleaks (no leaks)
     - 30s fuzz × 6 targets
     - codecov upload
     - no-vendor
     - no-cgo
     - coverage threshold (project ≥ 90%, security-critical = 100%)
5. Confirm the nightly fuzz-cron workflow is scheduled correctly
   (cron syntax + 5min budget per target).
6. Confirm .goreleaser.yml CGO_ENABLED=0 in env and produces
   darwin/linux × amd64/arm64 with signed checksums.
7. Append a NEW .github/workflows/ entry to docs/PACKAGE-MAP.md
   titled "Exported API — locked at SDD-31" describing each
   workflow's purpose.
8. Update docs/AC-MATRIX.md AC-9 row to point at the workflow
   files (this chunk IS the AC-9 owner).
9. Mark SDD-31 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add .github/ .golangci.json .goreleaser.yml \
          docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "ci: release gates (coverage + 6 fuzz + magex + govulncheck + gitleaks + CGO=0 + no /vendor) (SDD-31)"

Final message: confirm every gate green on a sample PR, codecov
badge updated, AC-MATRIX.md AC-9 row points to the workflow
files, SDD-PLAYBOOK updated, and the combined commit created.
If any gate is RED on the sample PR, do NOT mark SDD-31 done —
fix and re-run.
```
