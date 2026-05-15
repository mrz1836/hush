---
description: "Task list for SDD-31 release gates (coverage + 6 fuzz + magex + go-pre-commit + govulncheck + gitleaks + CGO=0 + no /vendor)"
---

# Tasks: Release Gates (Coverage + Fuzz + Vulnerability + Secret Scans + CGO=0 + No Vendor)

**Input**: Design documents from `/Users/mrz/projects/hush/specs/031-release-gates/`

**Prerequisites**: plan.md (required), spec.md (required for user stories), research.md (R-001…R-007), data-model.md (Gate / Fuzz Target / Security-Critical Package / CI Matrix Entry / Release Artefact / Waiver Entry / Coverage Snapshot), contracts/ (ci-workflow.md, fuzz-cron-workflow.md, release-workflow.md, coverage-threshold-cli.md), quickstart.md

**Tests**: REQUIRED — the chunk-doc / plan / FR-016 self-test all demand a Go test suite for the coverage-threshold tool. Workflow YAML files themselves are validated by a green CI run on a sample PR (cannot TDD a workflow file — spec assumption).

**Organization**: Tasks are grouped by user story so each story can be implemented and validated independently. Phase 2 (Foundational) ships the coverage-threshold tool first per the user's TASKS-phase brief — the tool is a hard prerequisite for ci.yml's `coverage-threshold` job and can be tested in isolation against a sample `cover.out` fixture before any workflow YAML exists.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1, US2, US3); setup/foundational/polish tasks omit the label
- Include exact file paths in descriptions

## Path Conventions

This chunk lives entirely at the repo root + `.github/`:
- New workflow files → `.github/workflows/*.yml`
- New Go tooling → `.github/scripts/coverage-threshold/` (+ a thin sibling under `.github/scripts/govulncheck-filter/` for FR-008 waiver filtering)
- New repo-root config → `.govulncheck-allow.yml`
- Edited config → `.goreleaser.yml`, `.specify/memory/constitution.md`
- Reviewed-only (no edit) → `.golangci.json` (per Research R-005)
- Doc updates → `docs/AC-MATRIX.md`, `docs/PACKAGE-MAP.md`, `docs/SDD-PLAYBOOK.md`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Repo-level prerequisites every user story depends on — constitution amendment for FR-016 byte-equality, govulncheck waiver file scaffolding, and read-only verifications that no edit is needed to `.golangci.json` / that the six canonical fuzz targets exist.

- [ ] T001 [P] Verify all six canonical fuzz targets from Research R-001 exist in the codebase: run `grep -rEn '^func (FuzzVaultDecode|FuzzJWTValidate|FuzzECIESDecrypt|FuzzVerifyRequest|FuzzSuperviseTOML|FuzzStatusJSON_Encode)\(' --include='*.go' internal/` and confirm each name appears under the package paths recorded in `specs/031-release-gates/data-model.md` §Entity 2. Capture the resolved file paths in a one-paragraph note appended to the bottom of `specs/031-release-gates/research.md` under R-001 (preserve the existing table; just record the verification timestamp + grep output).
- [ ] T002 [P] Review `.golangci.json` against Constitution IX § "Code Quality Gates" enumerated linters (`gochecknoglobals`, `gochecknoinits`, `containedctx`, `contextcheck`, `noctx`, `errcheck`, `errorlint`, `err113`, `errname`, `gosec`). Per Research R-005 the file already covers Principle IX, so this task is read-only verification: open `.golangci.json`, tick each linter against R-005's enumeration, and append a one-line "linters reviewed YYYY-MM-DD — no change required (R-005)" note to `specs/031-release-gates/research.md` under R-005.
- [ ] T003 [P] Snapshot current `.goreleaser.yml` shape vs target shape from Research R-004 / contracts/release-workflow.md §".goreleaser.yml edits required": record (a) where `CGO_ENABLED=0` currently lives (builds[0]-scoped today, must move to top-level), (b) current `builds[0].goos` value (`[darwin]` today, must become `[darwin, linux]`), (c) absence of a `signs:` block (must be added). Write the snapshot inline in T022's task description below if any divergence is found from the plan's locked HOW item 12 — no file edit yet.
- [ ] T004 Amend `.specify/memory/constitution.md` per Research R-002: (a) append a fenced block under Principle VIII delimited by `<!-- security-critical-packages: BEGIN (FR-016 anchor — DO NOT EDIT without amending coverage-threshold/compute.go) -->` and `<!-- security-critical-packages: END -->`, listing the seven security-critical packages one per line in the exact order `internal/keys` / `internal/vault` / `internal/vault/securebytes` / `internal/token` / `internal/transport/sign` / `internal/transport/ecies` / `internal/audit`; (b) bump the version stamp 1.1.0 → 1.1.1 (PATCH per constitution governance §"Versioning Policy" — codification of existing FR-014 intent, no policy change); (c) update the SYNC IMPACT REPORT comment block at the top of the constitution to reference SDD-31 / FR-016 as the trigger. This is the byte-equal source-of-truth for T009 / T011 / T016 below — match its bytes exactly.
- [ ] T005 Create `.govulncheck-allow.yml` at the repository root with starter contents `{ version: 1, vulns: [] }` (YAML, one entry per `vulns` array item once populated; schema documented at `specs/031-release-gates/data-model.md` §Entity 6). File MUST be checked in empty — implement-phase populates entries only on maintainer review per FR-008 (PR descriptions are non-authoritative).

**Checkpoint**: Constitution carries the FR-016 byte-equality anchor; `.govulncheck-allow.yml` exists; `.golangci.json` confirmed clean; six fuzz targets confirmed present. Foundational phase can begin.

---

## Phase 2: Foundational — Coverage-Threshold Tool (Blocking Prerequisites)

**Purpose**: Ship `.github/scripts/coverage-threshold/` BEFORE any workflow YAML so the tool can be exercised in isolation against synthetic `cover.out` fixtures. ci.yml's `coverage-threshold` job (T019) and the FR-016 byte-equality self-test (T011) both hard-depend on this phase completing.

**⚠️ CRITICAL**: No user story work (Phases 3–5) can begin until Phase 2 is complete and `go test ./.github/scripts/coverage-threshold/...` is green.

- [ ] T006 Create the directory `.github/scripts/coverage-threshold/` (placeholder — the three Go files below populate it). No `go.mod` in the subdirectory (per Research R-006 the tool lives in the parent module so it can `import "github.com/mrz1836/hush/..."` if ever needed, even though today it is stdlib-only).
- [ ] T007 Write the package scaffold at `.github/scripts/coverage-threshold/compute.go`: declare `package main`, declare sentinel errors `var ErrMalformedCoverOut = errors.New("malformed cover.out")` / `var ErrCoverageBelowThreshold = errors.New("coverage below threshold")` / `var ErrConstitutionMismatch = errors.New("security-critical list diverges from constitution")` per contracts/coverage-threshold-cli.md §"Constitution IX compliance", and declare the `securityCriticalPackages` constant as a `[]string` literal listing the seven packages in the exact order recorded in T004's fenced block (`internal/keys`, `internal/vault`, `internal/vault/securebytes`, `internal/token`, `internal/transport/sign`, `internal/transport/ecies`, `internal/audit`). Define empty function signatures `parseCoverOut(r io.Reader) (snapshot, error)` / `checkThresholds(s snapshot, minProject float64) error` / `verifyConstitutionList(constitutionPath string) error` and a `type snapshot struct { totalPct float64; perPkgPct map[string]float64; missingSecCrit []string }` — bodies returning a sentinel "not implemented" error are acceptable at this step; T009 fills them in.
- [ ] T008 [P] Write the table-driven test file `.github/scripts/coverage-threshold/compute_test.go` covering all seven test functions from contracts/coverage-threshold-cli.md §"Test contract":
    - `TestCoverageThreshold_ProjectGEThreshold` — synthetic cover.out where every security-critical pkg is at 100 % and project totals 92.3 %; expect `checkThresholds` returns `nil`.
    - `TestCoverageThreshold_SecurityCriticalEQ100` — table-driven sub-cases at exactly 100.0 %, at 99.9 %, and at 100.0 % across a non-sec-crit pkg: only the 99.9 % sec-crit case must return `ErrCoverageBelowThreshold`.
    - `TestCoverageThreshold_FailsBelowThreshold` — synthetic cover.out where project totals 89.9 %; expect `checkThresholds(..., 90.0)` returns `ErrCoverageBelowThreshold`.
    - `TestCoverageThreshold_FailsOnMissingPackage` — synthetic cover.out that omits `internal/audit`; expect `checkThresholds` returns `ErrCoverageBelowThreshold` with the missing-package message (FR-015 "missing report ≠ pass").
    - `TestCoverageThreshold_FailsOnMalformedCoverOut` — input is `"this is not a cover.out"`; expect `parseCoverOut` returns `ErrMalformedCoverOut` wrapped with `%w` (FR-015).
    - `TestCoverageThreshold_FailsOnEmptyCoverOut` — input is only `"mode: atomic\n"` with zero statement lines; expect `parseCoverOut` returns `ErrMalformedCoverOut` (FR-015 — empty report is a failure, not a pass).
    - `TestSecurityCriticalListMatchesConstitution` — read `.specify/memory/constitution.md` from a known relative path (`../../../.specify/memory/constitution.md`), extract bytes between the BEGIN/END markers from T004, and assert byte-equality with `securityCriticalPackages` joined by `\n`. Diverging whitespace, ordering, or line endings MUST fail the test (FR-016 lock).

  Each fixture is built inline via `strings.NewReader(...)` — no `testdata/` files except the real constitution. Sub-tests use `t.Run(name, ...)`; cases live in `tests := []struct{...}{...}` per Constitution IX § Testing Discipline "table-driven required".
- [ ] T009 Implement the bodies of `parseCoverOut` / `checkThresholds` / `verifyConstitutionList` in `.github/scripts/coverage-threshold/compute.go` per contracts/coverage-threshold-cli.md §"`cover.out` parsing contract" and §"Constitution IX compliance":
    - `parseCoverOut`: read first line via `bufio.Scanner`, require it to be one of `mode: atomic` / `mode: count` / `mode: set` (else return `fmt.Errorf("first line %q: %w", line, ErrMalformedCoverOut)`); for each remaining line split on whitespace into four fields (`file:Lstart.Cstart,Lend.Cend numStatements hitCount`), accumulate covered + total per package after stripping module prefix `github.com/mrz1836/hush/`; reject lines that don't parse to four fields with `ErrMalformedCoverOut`; reject an empty body (zero statement lines) with `ErrMalformedCoverOut`.
    - `checkThresholds`: compute project pct = `sum(covered)/sum(total)*100`; if `<minProject` return `ErrCoverageBelowThreshold` with a `%w` wrap that names the actual percentage; for each entry in `securityCriticalPackages`, if absent from `s.perPkgPct` append to `missingSecCrit` and return `ErrCoverageBelowThreshold` (FR-015); if present and `<100.0` return `ErrCoverageBelowThreshold` naming the pkg + pct.
    - `verifyConstitutionList`: read the file at the given path, scan for the BEGIN/END marker pair, extract the byte slice between them, normalise to `\n`-joined lines (trim trailing blank line), and compare byte-for-byte against `strings.Join(securityCriticalPackages, "\n")`; on mismatch return `ErrConstitutionMismatch` wrapping a diff hint.
- [ ] T010 Write the entrypoint at `.github/scripts/coverage-threshold/main.go`: declare `package main`; in `func main()` parse flags `-cover` / `-min-project` / `-constitution` per contracts/coverage-threshold-cli.md §"Invocation contract" (defaults: `min-project=90`); open the cover.out path, invoke `parseCoverOut` (on error exit 2 with `::error::` prefix); invoke `verifyConstitutionList(*constitutionFlag)` (on error exit 3 with `::error::`); invoke `checkThresholds` (on error exit 1 with `::error::` per stdout contract in coverage-threshold-cli.md §"stdout contract"); on success print the PASS summary and exit 0. File MUST stay ≤40 lines per contracts/coverage-threshold-cli.md header. No `init()`, no globals.
- [ ] T011 Run `go test ./.github/scripts/coverage-threshold/...` from the repo root; iterate on T009 / T010 until every test from T008 passes green. Then run `go run ./.github/scripts/coverage-threshold -cover <a-real-cover.out-generated-by-magex-test:race> -min-project 90 -constitution .specify/memory/constitution.md` against an actual repo coverage profile and confirm the exit code matches the current project's coverage truth — fail-loud if the script exits non-zero against a known-green build (would mean a parser bug). Mark this task complete only when both invocations are clean.

**Checkpoint**: Coverage-threshold tool is shipped, tested in isolation against synthetic + real cover.out, and the FR-016 byte-equality self-test ties the script's constant to the constitution from T004. Phase 3 (US1 ci.yml) can now reference the tool by explicit path.

---

## Phase 3: User Story 1 — Per-Pull-Request Release Gate (Priority: P1) 🎯 MVP

**Goal**: Every pull request against `main` triggers all eleven required gates (FR-004…FR-019) on both matrix legs (macos-arm64 + linux-amd64); any single failing gate blocks merge; coverage is uploaded to Codecov; the coverage-threshold gate from Phase 2 runs as a separate downstream job.

**Independent Test** (per spec User Story 1): Open a draft PR with one trivial commit → observe every required check (`ci / gates (macos-arm64)`, `ci / gates (linux-amd64)`, `ci / coverage-threshold`, `ci / coverage-upload`) goes green. Push a second commit that intentionally breaks one gate (e.g. add a stray `\t` to force a format-check failure) → observe the corresponding gate's check turns red and merge is blocked.

### Implementation for User Story 1

- [ ] T012 [US1] Create `.github/workflows/ci.yml` with the workflow-level header per contracts/ci-workflow.md §"Trigger contract" / §"Permissions" / §"Concurrency": `name: ci`, `on: { pull_request: { branches: [main] }, push: { branches: [main] }, workflow_dispatch: {} }`, `permissions: { contents: read, pull-requests: read, checks: write, id-token: write }`, `concurrency: { group: ci-${{ github.workflow }}-${{ github.ref }}, cancel-in-progress: true }`. No jobs yet — subsequent tasks add them.
- [ ] T013 [US1] Add the `gates` matrix job skeleton to `.github/workflows/ci.yml` per contracts/ci-workflow.md §"Job: `gates`": `name: gates (${{ matrix.os_label }})`, `strategy: { fail-fast: false, matrix: { include: [ { os_label: macos-arm64, runs-on: macos-14, goos: darwin, goarch: arm64 }, { os_label: linux-amd64, runs-on: ubuntu-24.04, goos: linux, goarch: amd64 } ] } }`, `runs-on: ${{ matrix.runs-on }}`, job-level `env: { CGO_ENABLED: "0", GOOS: ${{ matrix.goos }}, GOARCH: ${{ matrix.goarch }} }`. Add the prelude steps: `actions/checkout@v4` with `fetch-depth: 0` (gitleaks needs full history), `actions/setup-go@v5` with `go-version-file: go.mod` + `cache: true`, and three `go install` steps for magex / go-pre-commit / govulncheck (pin versions at implement-phase — use whatever `.github/CONTRIBUTING.md` recommends, fall back to `@latest` only with an inline TODO).
- [ ] T014 [US1] Add the four cheap pre-flight gate steps to the `gates` job in `.github/workflows/ci.yml` in this order (each as a separate `- name:` step with a single `run:` body — never `continue-on-error: true`):
    1. `no-vendor` (FR-017): `test ! -d vendor || (echo "::error::/vendor directory forbidden (Principle XI)"; exit 1)`
    2. `no-cgo` (FR-018): grep for `import "C"` in `*.go` excluding `*_test.go`; `::error::` + exit 1 on match (Principle XI defence-in-depth complement to job-level `CGO_ENABLED=0`)
    3. `format-check` (FR-004): `magex format:fix --check`
    4. `lint` (FR-005): `magex lint`
- [ ] T015 [US1] Add the `pre-commit` and `test-race` steps to the `gates` job in `.github/workflows/ci.yml`:
    - `pre-commit` (FR-007): `go-pre-commit run --all`
    - `test-race` (FR-006): `magex test:race -- -coverprofile=cover.out -covermode=atomic ./...` — race detector MUST stay on (FR-006 anti-contract). The step produces `cover.out` consumed by T017 / T019 / T020.
- [ ] T016 [US1] Add the `govulncheck` gate to the `gates` job in `.github/workflows/ci.yml` plus its colocated waiver-filter helper at `.github/scripts/govulncheck-filter/main.go`. Per plan locked-HOW item 6 + contracts/ci-workflow.md §"Job: `gates`" govulncheck step:
    - Step body: `govulncheck -format=json ./... > vulns.json` then `go run ./.github/scripts/govulncheck-filter -input vulns.json -allow .govulncheck-allow.yml`.
    - The filter is a ≤80-line stdlib-only Go tool: reads the JSON stream of findings, reads `.govulncheck-allow.yml` (one-shot `os.ReadFile` + `yaml.Unmarshal` — but stdlib has no yaml, so parse the simple `{version, vulns: [{id, justification, expires}]}` shape via a tiny line scanner that accepts only the two key shapes the file contains, or vendor `gopkg.in/yaml.v3` via the parent `go.mod` if already present; verify the parent `go.mod` before deciding). For each finding, drop it if its OSV ID matches an unexpired waiver (today < `expires`); fail with exit 1 if any finding remains. Sentinel errors + `%w` wrap + table-driven tests in a sibling `main_test.go` per Constitution IX. (If the helper turns out to require a non-stdlib YAML dep, prefer a 30-line inline `yq` invocation from a shell step instead — record the decision in research.md as R-008 before merging.)
- [ ] T017 [US1] Add the `gitleaks` + `fuzz-smoke` + coverage-artefact-upload steps to the `gates` job in `.github/workflows/ci.yml`:
    - `gitleaks` (FR-009): `uses: gitleaks/gitleaks-action@v2` with `env: { GITLEAKS_CONFIG: .gitleaks.toml }`.
    - `fuzz-smoke` (FR-010): a single step running six sequential `go test -run=^$ -fuzz=^<target>$ -fuzztime=30s ./<pkg>` invocations in the exact order from data-model.md §Entity 2 (FuzzVaultDecode → FuzzJWTValidate → FuzzECIESDecrypt → FuzzVerifyRequest → FuzzSuperviseTOML → FuzzStatusJSON_Encode). Wrap in `set -e` so the first failing target fails CI; never split into six steps (chunk-doc plan locked-HOW item 4).
    - Coverage artefact upload (linux-amd64 only — `if: matrix.os_label == 'linux-amd64'`): `uses: actions/upload-artifact@v4` with `name: coverage`, `path: cover.out`, `retention-days: 7`. Downstream jobs in T019 / T020 consume this artefact.
- [ ] T018 [US1] Add the `coverage-threshold` job to `.github/workflows/ci.yml` per contracts/ci-workflow.md §"Job: `coverage-threshold`": `needs: gates`, `runs-on: ubuntu-24.04`. Steps: `actions/checkout@v4`, `actions/setup-go@v5` with `go-version-file: go.mod`, `actions/download-artifact@v4` with `name: coverage`, a `self-test` step running `go test ./.github/scripts/coverage-threshold/...`, then a `threshold` step running `go run ./.github/scripts/coverage-threshold -cover cover.out -min-project 90 -constitution .specify/memory/constitution.md`. Branch protection requires the check name `ci / coverage-threshold` — do not rename the job without coordinating the branch-protection update.
- [ ] T019 [US1] Add the `coverage-upload` job to `.github/workflows/ci.yml` per contracts/ci-workflow.md §"Job: `coverage-upload`": `needs: gates`, `runs-on: ubuntu-24.04`. Steps: `actions/checkout@v4`, `actions/download-artifact@v4` with `name: coverage`, `uses: codecov/codecov-action@v4` with `files: ./cover.out` + `fail_ci_if_error: true` (FR-011 binds the upload; SC-003 binds the badge). Branch protection requires the check name `ci / coverage-upload`.

**Checkpoint**: ci.yml is complete. The four required check names exist (`ci / gates (macos-arm64)`, `ci / gates (linux-amd64)`, `ci / coverage-threshold`, `ci / coverage-upload`). US1's Independent Test is now runnable against a draft PR — see Phase 6's T030.

---

## Phase 4: User Story 2 — Nightly Deep-Fuzz Cron (Priority: P2)

**Goal**: A scheduled `0 7 * * *` UTC job (plus `workflow_dispatch` escape hatch with `seconds_per_target` input, default 300) runs each of the six canonical fuzz targets in parallel on linux-amd64 for the configured budget; failing targets surface independently; any crashing input is preserved as a 30-day workflow artefact for local reproduction (FR-020/021/022).

**Independent Test** (per spec User Story 2): `gh workflow run fuzz-cron.yml -f seconds_per_target=60` → observe six matrix legs run ~60 s each, all green; the workflow summary lists six green check-runs.

### Implementation for User Story 2

- [ ] T020 [P] [US2] Create `.github/workflows/fuzz-cron.yml` with the workflow-level header per contracts/fuzz-cron-workflow.md §"Trigger contract" / §"Permissions" / §"Concurrency": `name: fuzz-cron`, `on: { schedule: [ { cron: '0 7 * * *' } ], workflow_dispatch: { inputs: { seconds_per_target: { description: "Per-target fuzz budget in seconds (default 300)", required: false, default: "300" } } } }`, `permissions: { contents: read, actions: write }`, `concurrency: { group: fuzz-cron, cancel-in-progress: false }`.
- [ ] T021 [US2] Add the `fuzz` matrix-by-target job to `.github/workflows/fuzz-cron.yml` per contracts/fuzz-cron-workflow.md §"Job: `fuzz`": `name: fuzz-${{ matrix.fuzz_target.name }}`, `strategy: { fail-fast: false, matrix: { fuzz_target: [ <six entries with name + pkg from data-model.md §Entity 2 in the same order as ci.yml's fuzz-smoke> ] } }`, `runs-on: ubuntu-24.04` (linux-only per Research R-003 — no OS-coverage gain doubling onto macOS), job-level `env: { CGO_ENABLED: "0" }`. Steps: `actions/checkout@v4`, `actions/setup-go@v5` with `go-version-file: go.mod` + `cache: true`, a `fuzz` step running `BUDGET="${{ github.event.inputs.seconds_per_target || '300' }}s" && go test -run=^$ -fuzz=^${{ matrix.fuzz_target.name }}$ -fuzztime="$BUDGET" ${{ matrix.fuzz_target.pkg }}`, and a `preserve crash corpus` step gated by `if: failure()` using `actions/upload-artifact@v4` with `name: corpus-${{ matrix.fuzz_target.name }}`, `path: ${{ matrix.fuzz_target.pkg }}/testdata/fuzz/${{ matrix.fuzz_target.name }}/`, `retention-days: 30`, `if-no-files-found: warn` (FR-022 lock). The six matrix entries MUST be byte-equal in name + ordering to ci.yml's fuzz-smoke list — FR-029 forbids divergence between smoke and cron sets.

**Checkpoint**: fuzz-cron.yml is shipped. US2's Independent Test is runnable via `gh workflow run fuzz-cron.yml -f seconds_per_target=60` — see Phase 6's T031.

---

## Phase 5: User Story 3 — Tagged Release Builds (Priority: P2)

**Goal**: On `push` of a tag matching `v*`, GoReleaser produces four binaries (`{darwin,linux} × {amd64,arm64}`) with `CGO_ENABLED=0`, plus a SHA-256 checksums manifest, plus a cosign-keyless signature + Fulcio certificate published alongside the manifest. Verification by a consumer via `cosign verify-blob --certificate-identity-regexp '^https://github.com/<org>/hush/.github/workflows/release.yml@refs/tags/v.*$' --certificate-oidc-issuer https://token.actions.githubusercontent.com` succeeds (SC-006 lock).

**Independent Test** (per spec User Story 3): Cut a pre-release tag (`v0.1.0-rc1`) on a feature branch → release.yml runs → four binaries + checksums + .sig + .pem appear on the release page → `cosign verify-blob` succeeds against the published manifest.

### Implementation for User Story 3

- [ ] T022 [P] [US3] Edit `.goreleaser.yml` per Research R-004 + contracts/release-workflow.md §".goreleaser.yml edits required":
    1. Promote `env: [CGO_ENABLED=0]` from `builds[0]` scope to top-level (FR-023 belt-and-braces so archives + signs also inherit the env).
    2. Change `builds[0].goos` from `[darwin]` to `[darwin, linux]` (FR-024 — keeps existing `builds[0].goarch: [amd64, arm64]` so the matrix produces four binaries).
    3. Add the `signs:` block exactly as shown in Research R-004 (cosign keyless, `artifacts: checksum` — sign the SHA256SUMS manifest only, never per-binary per FR-025 lock). The block invokes `cosign sign-blob --yes --output-signature=${signature} --output-certificate=${certificate} ${artifact}` so consumers get `.sig` + `.pem` next to the manifest.
    4. Confirm `before.hooks` still runs `magex test` (FR-026 — release-time test bar matches the per-PR bar, race detector on by default). Do NOT relax the test step.
- [ ] T023 [US3] Create `.github/workflows/release.yml` per contracts/release-workflow.md §"Job: `release`": `name: release`, `on: { push: { tags: ['v*'] }, workflow_dispatch: { inputs: { tag: { description: "Tag to release (must already exist)", required: true } } } }`, `permissions: { contents: write, id-token: write, packages: write }` (`id-token: write` is load-bearing — cosign cannot mint a Fulcio cert without it; FR-025), `concurrency: { group: release-${{ github.ref }}, cancel-in-progress: false }`. Job body on `ubuntu-24.04` with job-level `env: { CGO_ENABLED: "0" }`. Steps: `actions/checkout@v4` with `fetch-depth: 0` (GoReleaser needs full history for changelog), `actions/setup-go@v5` with `go-version-file: go.mod` + `cache: true`, `sigstore/cosign-installer@v3` with `cosign-release: 'v2.4.0'` (pin to a tested version), `go install` for magex, then `goreleaser/goreleaser-action@v6` with `args: release --clean` and `env: { GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }} }`. No explicit race / lint step in the workflow — FR-026 enforces this through `.goreleaser.yml`'s `before.hooks: magex test` from T022 step 4.

**Checkpoint**: All three workflow files (ci.yml, fuzz-cron.yml, release.yml) and the supporting `.goreleaser.yml` edit are in place. Foundational tool + every user story workflow are shipped. Phase 6 validates the result end-to-end.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Per the user's TASKS-phase brief — the final phase MUST include `magex format:fix`, `magex lint`, `magex test:race`, and a manual sample-PR verification that triggers all CI steps end-to-end. Plus the chunk-doc Prompt 5 deliverables (AC-MATRIX edit, PACKAGE-MAP edit, SDD-PLAYBOOK status flip, combined commit).

- [ ] T024 [P] Run `magex format:fix` from the repo root to format the new Go files under `.github/scripts/coverage-threshold/` and `.github/scripts/govulncheck-filter/`. Expect zero diff against the staged version of those files (if `format:fix` produces a diff, commit it before T025). YAML workflow files under `.github/workflows/` are not Go-formatted; their formatting is human-review.
- [ ] T025 [P] Run `magex lint` from the repo root. The new Go files MUST pass every linter that `.golangci.json` enables — Constitution IX § Code Quality Gates expects table-driven tests / `%w` error wrap / sentinel errors / no globals / no `init()` / stdlib-only (verify the import list with `goimports -l .github/scripts/...`). Iterate on T009 / T010 / T016 until clean.
- [ ] T026 Run `magex test:race ./...` from the repo root. The race detector MUST stay on (FR-006 anti-contract). Expect the existing test suite to remain green — this chunk adds no production code so coverage should not regress. If it does, the regression is unrelated to SDD-31 and MUST be filed as a separate issue before tagging SDD-31 done.
- [ ] T027 [P] Update `docs/AC-MATRIX.md` AC-9 row per FR-030 + chunk-doc Prompt 5 step 8: change the "Owner" / "Evidence" column(s) to cite the three new workflow files by path — `.github/workflows/ci.yml`, `.github/workflows/fuzz-cron.yml`, `.github/workflows/release.yml` — and the coverage-threshold tool at `.github/scripts/coverage-threshold/`. Mark the AC-9 row status `green` once T030 (the sample-PR end-to-end check) is also green. Leave the status `pending` until T030 completes.
- [ ] T028 [P] Append a new section to `docs/PACKAGE-MAP.md` titled `### .github/workflows/ (locked at SDD-31)` listing each of the three workflow files with a one-line purpose statement (per chunk-doc Prompt 5 step 7): `ci.yml — per-PR matrix gates (FR-004…FR-019)`, `fuzz-cron.yml — nightly deep-fuzz cron (FR-020…FR-022)`, `release.yml — tag-driven GoReleaser + cosign keyless (FR-023…FR-027)`. Plus a sub-bullet noting `.github/scripts/coverage-threshold/` is the FR-016 byte-equality enforcer.
- [ ] T029 [P] Update `docs/SDD-PLAYBOOK.md` to mark SDD-31 status `done` (per chunk-doc Prompt 5 step 9). Do NOT flip the status until T030 + T031 are both green — if either fails, SDD-31 stays in-progress and the failing gate is fixed before retry.
- [ ] T030 Manual sample-PR validation (User Story 1 Independent Test + spec SC-001 / SC-002). Push a draft branch + open a PR against `main`:
    1. `git checkout -b sandbox/sdd-31-gate-validation`
    2. `git commit --allow-empty -m "ci: SDD-31 gate validation (draft)"`
    3. `git push -u origin sandbox/sdd-31-gate-validation`
    4. `gh pr create --draft --title "draft: SDD-31 gate validation" --body "Validating every CI gate after SDD-31 lands."`
    5. Watch the PR page (`gh pr checks --watch`) and confirm every required check name goes green: `ci / gates (macos-arm64)`, `ci / gates (linux-amd64)`, `ci / coverage-threshold`, `ci / coverage-upload`.
    6. Push a follow-up commit that intentionally breaks one gate (e.g. add a stray `\t` to a `.go` file to fail `format-check`) → confirm the corresponding check turns red and merge is blocked → revert.
    7. Confirm Codecov uploaded the report and the badge updates (SC-003).
  Mark T030 complete only when both (a) the all-green run AND (b) the intentional-break demonstration have been observed in the GitHub Actions UI.
- [ ] T031 Manual fuzz-cron validation (User Story 2 Independent Test + spec SC-005). From the repo root: `gh workflow run fuzz-cron.yml -f seconds_per_target=60` then `gh run watch`. Confirm six matrix legs each run ~60 s, all green; confirm the workflow summary lists six green check-runs. (Do NOT wait for the actual nightly fire — the manual dispatch with the 60 s floor is the validation contract.)
- [ ] T032 Combined commit per chunk-doc Prompt 5 step 8 (defer ALL commits from this chunk to a single combined commit at the end):
    ```sh
    git add .github/ .golangci.json .goreleaser.yml \
            .govulncheck-allow.yml \
            docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
            .specify/memory/constitution.md \
            specs/031-release-gates/tasks.md
    git commit -m "ci: release gates (coverage + 6 fuzz + magex + govulncheck + gitleaks + CGO=0 + no /vendor) (SDD-31)"
    ```
  Run this only after T024–T031 are all green. If any gate is RED on the sample PR from T030, do NOT mark SDD-31 done — fix the failing gate and re-run T024 onward.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1, T001–T005)**: No dependencies — can start immediately. T001 / T002 / T003 run in parallel; T004 (constitution amendment) is the only Phase 1 task whose output feeds a later phase (T007 / T009 / T011 consume the fenced block); T005 creates a brand-new file with no dependents.
- **Foundational (Phase 2, T006–T011)**: Depends on T004 (constitution fenced block must exist before T011's byte-equality test can pass). BLOCKS all user-story phases.
- **User Stories (Phase 3+)**: All depend on Phase 2 completion (the coverage-threshold tool must exist and pass tests before ci.yml can reference it).
  - **US1 (T012–T019)**: After Phase 2.
  - **US2 (T020–T021)**: After Phase 2 (technically independent of US1, but the fuzz target list is shared — see FR-029 lockstep below).
  - **US3 (T022–T023)**: After Phase 2 (`.goreleaser.yml` edit and release.yml are independent of US1/US2's workflow files; only constraints are Constitution VIII/XI which Phase 2 already satisfies).
- **Polish (Phase 6, T024–T032)**: T024–T026 (magex format:fix / lint / test:race) depend on every Go file under `.github/scripts/` being in its final form (i.e. after Phase 2 and after T016). T027–T029 (doc edits) depend on every workflow file existing (i.e. after Phases 3/4/5). T030 (sample PR) depends on ci.yml being deployed to a branch on the remote — i.e. after T019. T031 depends on T021. T032 (combined commit) depends on everything else.

### User Story Dependencies

- **US1 (P1, MVP)**: Can start after Phase 2. Independent of US2/US3 in source files, but ci.yml's `fuzz-smoke` step shares its fuzz target list with US2's fuzz-cron matrix (FR-029 — sets MUST NOT diverge); when adding/removing a target in either workflow, update both in the SAME change.
- **US2 (P2)**: Can start after Phase 2, in parallel with US1 (different file, no source overlap). Lockstep with US1 only on the fuzz target list (FR-029).
- **US3 (P2)**: Can start after Phase 2, in parallel with US1 and US2 (different files, no source overlap). Edits `.goreleaser.yml` which no other phase touches.

### Within Each User Story

- US1: workflow-level header (T012) → matrix-job skeleton (T013) → cheap pre-flights (T014) → pre-commit + test-race (T015 — produces cover.out for downstream) → govulncheck (T016) → gitleaks + fuzz-smoke + artefact upload (T017) → coverage-threshold job (T018, needs T015's cover.out artefact) → coverage-upload job (T019, needs T015's cover.out artefact). T018 and T019 are independent of each other (both `needs: gates`) and could run in parallel during execution, but their workflow-file edits to `ci.yml` are sequential since they edit the same file.
- US2: header (T020) → matrix-by-target job (T021).
- US3: `.goreleaser.yml` edit (T022) and release.yml creation (T023) are independent — T022 [P] runs in parallel with T023 because they edit different files. The runtime contract requires both to ship together; the task list does not force sequencing them.

### Parallel Opportunities

Within Phase 1: T001 / T002 / T003 / T005 all [P] (different files / read-only verifications). T004 stands alone (single-file edit on the constitution).

Within Phase 2: T008 [P] (test file) can be drafted in parallel with T009 (implementation of compute.go) — Constitution IX TDD discipline says tests first, but in this case the function signatures from T007 unblock both. T010 (main.go) depends on T009 finishing the function bodies.

Within Phase 3: every step inside the `gates` job sequence (T013–T017) edits the same file (`.github/workflows/ci.yml`) so they are sequential edits in calendar order; the runtime gates inside the job run on the GitHub runner in the order their YAML lists them.

Within Phase 5: T022 [P] (`.goreleaser.yml`) and T023 (`release.yml`) edit different files and can be done in parallel.

Within Phase 6: T024 / T025 / T027 / T028 / T029 are all [P] (different files or read-only commands). T026 (magex test:race) is sequential after T024/T025 to ensure the formatted+linted code is what gets race-tested. T030 / T031 are manual validations that must run on the deployed branch — they cannot start until T032's branch push, BUT the chunk-doc says "Do NOT mark SDD-31 done until the sample PR is green", so T030 / T031 happen between T023 and T032 (push the branch as a draft for validation, then commit only once green).

---

## Parallel Example: User Story 1

```bash
# Phase 2 (Foundational) — these are sequential because they share the same Go package
# but T008's test file can be drafted in a separate editor pane while T009 implements:
Task: "T008 Write compute_test.go with seven table-driven tests"           # different file from T009
Task: "T009 Implement parseCoverOut / checkThresholds / verifyConstitutionList in compute.go"

# Phase 3 (US1) — every step edits .github/workflows/ci.yml; sequential by necessity
# but the conceptual gates inside the YAML can be reasoned about in parallel:
Task: "T014 Add no-vendor + no-cgo + format-check + lint steps to gates job in ci.yml"
Task: "T015 Add pre-commit + test-race (-coverprofile=cover.out) steps to gates job in ci.yml"
Task: "T016 Add govulncheck step + .github/scripts/govulncheck-filter/main.go helper"
Task: "T017 Add gitleaks + fuzz-smoke (6× 30 s) + artefact-upload steps to gates job in ci.yml"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001–T005)
2. Complete Phase 2: Foundational — coverage-threshold tool green (T006–T011) — CRITICAL, blocks all stories
3. Complete Phase 3: US1 — ci.yml (T012–T019)
4. **STOP and VALIDATE**: Push a draft branch, watch `ci / gates (macos-arm64)` + `ci / gates (linux-amd64)` + `ci / coverage-threshold` + `ci / coverage-upload` go green (T030, partial — fuzz-cron + release validation deferred to US2 / US3).
5. This is the MVP — the load-bearing per-PR gate. If US2 and US3 slip, US1 alone unblocks `main`-branch merges with the constitutional bar enforced.

### Incremental Delivery

1. Phase 1 + Phase 2 → Foundation ready, tool tested in isolation.
2. Add US1 (ci.yml) → Validate per-PR gates on a sample PR → MVP ships.
3. Add US2 (fuzz-cron.yml) → Validate via `workflow_dispatch -f seconds_per_target=60` → nightly safety valve ships.
4. Add US3 (`.goreleaser.yml` + release.yml) → Cut a `v0.1.0-rc1` tag → validate four-binary + cosign chain → release pipeline ships.
5. Phase 6 polish + combined commit.
6. Each story adds value without breaking the prior; US1 stays green even if US2 or US3 fails.

### Parallel Team Strategy

With multiple maintainers:

1. Maintainer A: Phase 1 (T001–T005) + Phase 2 (T006–T011) — owns the constitution amendment + Go tool + tests.
2. Once Phase 2 is green:
   - Maintainer A: US1 (T012–T019) — ci.yml
   - Maintainer B: US2 (T020–T021) — fuzz-cron.yml (in parallel)
   - Maintainer C: US3 (T022–T023) — release.yml + `.goreleaser.yml` (in parallel)
3. Stories complete and integrate independently; final-phase polish + combined commit is co-authored.

---

## Notes

- [P] tasks = different files, no dependencies on incomplete tasks.
- [Story] label maps task to a specific user story for traceability; setup/foundational/polish tasks have no story label.
- Each user story is independently completable + testable (FR-028 — no gate may be downgraded to make a different gate ship faster).
- Verify tests fail before implementing (T008 tests are written before T009 fills in the function bodies — Constitution IX TDD).
- Commit at the END (T032, single combined commit per chunk-doc Prompt 5). Do NOT commit between tasks within this chunk.
- Stop at the Phase 2 checkpoint and validate the tool in isolation against a real cover.out before touching any workflow YAML — this is the user's explicit TASKS-phase ordering.
- Avoid: skipping fuzz targets to make CI faster (FR-021), disabling the race detector (FR-006 anti-contract), allowing CGO (FR-018/019), letting smoke and cron fuzz lists diverge (FR-029), waiving govulncheck findings in a PR description instead of `.govulncheck-allow.yml` (FR-008), signing per-binary instead of manifest-only (FR-025), adding a `/vendor` directory (FR-017), marking any gate `continue-on-error: true` (FR-003 / FR-028), adding an eighth security-critical package without updating BOTH the constitution fenced block AND `securityCriticalPackages` in the SAME change (FR-016 byte-equality enforces this).
