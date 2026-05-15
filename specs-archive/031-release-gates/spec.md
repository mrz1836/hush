# Feature Specification: Release Gates (Coverage + Fuzz + Vulnerability + Secret Scans + CGO=0 + No Vendor)

**Feature Branch**: `031-release-gates`

**Created**: 2026-05-14

**Status**: Draft

**Input**: User description: "release gates in CI: matrix macOS-arm64 + ubuntu-amd64 on Go 1.26; per-PR steps (format check, lint, test:race, go-pre-commit, govulncheck, gitleaks, 30s fuzz smoke, codecov upload) + nightly cron (60s+ fuzz per target); enforce no /vendor and CGO_ENABLED=0; coverage thresholds (project ≥90%, security-critical = 100%); .goreleaser.yml produces signed darwin/linux × amd64/arm64 binaries"

## Clarifications

### Session 2026-05-14

- Q: Fuzz target list contradicts the constitution — align FR-010 to constitution (status-socket JSON instead of server TOML), keep as-is, or run all seven? → A: Align spec to constitution (drop server TOML, keep status-socket JSON).
- Q: govulncheck severity policy and waiver mechanism — fail on any finding with checked-in allow-list, HIGH-only with allow-list, label-gated PR-body waiver, or no waiver at all? → A: Fail on any finding; waivers via checked-in `.govulncheck-allow.yml` (OSV IDs + expiry + justification); PR description is non-authoritative.
- Q: CI test matrix breadth — 2 entries (macOS-arm64 + Linux-amd64), 4 entries mirroring release, 3 entries dropping darwin-amd64, or 2 entries plus cross-compile build smoke for the other two? → A: 2 entries (macOS-arm64 + Linux-amd64); darwin-amd64 and linux-arm64 are cross-compile-only in the release pipeline.
- Q: Source of truth for the security-critical package list — hardcoded constant in the threshold script with constitution byte-equality self-test, runtime markdown parse of the constitution, new top-level file, or a Go package exporting the list? → A: Hardcoded constant in the threshold script; self-test asserts byte-equality against the constitution's enumeration.
- Q: Signing approach for the release checksums manifest — cosign keyless (Sigstore Fulcio + Rekor via GitHub Actions OIDC), cosign with stored key, GPG, or minisign? → A: Cosign keyless via Sigstore Fulcio + Rekor (GitHub Actions OIDC identity); no stored private key.

## Overview

This feature wires every code-quality and security gate that Constitution
principles VIII (Testing Discipline) and XI (Native-First, Minimal
Dependencies, Ephemeral Vault) demand into the continuous-integration
pipeline. The pipeline must reject any pull request that regresses on
formatting, linting, race-detector tests, fuzz coverage, vulnerability
posture, secret hygiene, coverage thresholds, the no-CGO invariant, or
the no-vendor invariant. A green CI run on a sample pull request — every
gate passing on both supported operating-system / architecture pairs — is
the acceptance test for AC-9 (the project's release-gate row in
`docs/AC-MATRIX.md`).

The release artefact build (signed darwin and linux binaries for both
amd64 and arm64) is included in scope because it is the on-merge proof
that the no-CGO invariant survives the actual release path, not only the
test path.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Per-pull-request release gate (Priority: P1)

A contributor opens a pull request against the default branch. The CI
system runs every required gate on every supported operating-system and
architecture combination. The pull request cannot merge until every gate
returns a passing result; any single failing gate blocks the merge.
Reviewers do not have to manually run lint, tests, or fuzz checks
locally to be confident the change is safe — CI is the authority.

**Why this priority**: This is the load-bearing flow. Every merge to the
default branch passes through this gate. If this story is broken, the
project loses its enforcement of the constitutional code-quality bar.

**Independent Test**: Open a draft pull request with a single trivial
commit; observe that all gates run, all pass, and the merge is enabled.
Then push a commit that intentionally breaks one gate (e.g., a known
lint violation); observe that the corresponding gate fails and the merge
is blocked.

**Acceptance Scenarios**:

1. **Given** a clean pull request with no gate violations, **When** CI
   runs on both supported operating-system / architecture pairs, **Then**
   every gate reports success and the pull request is mergeable.
2. **Given** a pull request that introduces a formatting deviation,
   **When** CI runs, **Then** the format gate fails and the merge is
   blocked.
3. **Given** a pull request that introduces a linter violation, **When**
   CI runs, **Then** the lint gate fails and the merge is blocked.
4. **Given** a pull request that introduces a data race in a test under
   the race detector, **When** CI runs the race-test gate, **Then** the
   race-test gate fails and the merge is blocked.
5. **Given** a pull request that lowers project-wide test coverage below
   the project threshold, **When** CI evaluates the coverage threshold
   gate, **Then** the coverage gate fails and the merge is blocked.
6. **Given** a pull request that lowers coverage on any security-critical
   package below 100%, **When** CI evaluates the coverage threshold gate,
   **Then** the coverage gate fails and the merge is blocked.
7. **Given** a pull request that introduces a known-vulnerable dependency
   reported by the vulnerability scanner, **When** CI runs the
   vulnerability-scan gate, **Then** the gate fails and the merge is
   blocked.
8. **Given** a pull request that introduces a secret-shaped value
   detected by the secret scanner, **When** CI runs the secret-scan gate,
   **Then** the gate fails and the merge is blocked.
9. **Given** a pull request that causes any of the six required fuzz
   targets to panic, error-without-typed-error, or grow memory
   unboundedly within the smoke-run budget, **When** CI runs the
   per-pull-request fuzz smoke gate, **Then** the fuzz gate fails and the
   merge is blocked.
10. **Given** a pull request that introduces a `/vendor` directory at the
    repository root, **When** CI runs the no-vendor gate, **Then** the
    no-vendor gate fails and the merge is blocked.
11. **Given** a pull request that introduces a non-test Go source file
    using CGO (`import "C"`) or that enables CGO during build or test,
    **When** CI runs the no-CGO gate, **Then** the no-CGO gate fails and
    the merge is blocked.
12. **Given** a green pull-request build, **When** CI completes, **Then**
    the coverage report is uploaded to the coverage-reporting service so
    the public coverage badge reflects the latest measurement.

---

### User Story 2 — Nightly deep-fuzz cron (Priority: P2)

A scheduled, off-PR job runs each required fuzz target for a budget
larger than the per-pull-request smoke budget so that long-tail panics,
slow-growth memory leaks, and pathological inputs that the 30-second
smoke cannot uncover are surfaced before any release tag is cut.
Findings are surfaced to maintainers; no production code is modified
automatically.

**Why this priority**: Constitution VIII requires every fuzz target to
run clean for at least 60 seconds before v0.1.0 tagging. A 30-second
per-PR smoke is the cheapest signal that the harness is still wired up,
but only the nightly deep run gives the project confidence that
parsing- and crypto-boundary code is panic-free across a wider input
distribution. Skipping fuzz targets to save PR time is explicitly
forbidden by the chunk contract; the nightly cron is the safety valve
that allows the per-PR smoke to stay fast without losing coverage.

**Independent Test**: Manually trigger the nightly fuzz workflow against
a known-good revision; observe that every one of the six required fuzz
targets runs for the configured cron budget, returns clean, and that the
job result is success.

**Acceptance Scenarios**:

1. **Given** the nightly schedule fires, **When** the cron job runs,
   **Then** every one of the six required fuzz targets executes for at
   least sixty seconds of effective fuzz time.
2. **Given** a fuzz target panics, leaks memory unboundedly, or returns
   an untyped error during the nightly run, **When** the run completes,
   **Then** the cron job result is failure and maintainers are notified
   through the workflow's standard failure surface.
3. **Given** a fuzz target finds a new crashing input, **When** the run
   completes, **Then** the corpus seed for that input is preserved so it
   can be reproduced locally.

---

### User Story 3 — Tagged release builds signed multi-OS / multi-arch binaries (Priority: P2)

When a maintainer creates a release tag, the release pipeline produces
binaries for the supported operating-system and architecture pairs,
publishes the binaries with a checksums manifest, and signs the
checksums manifest so consumers can verify provenance. The release
binaries are built with CGO disabled; a release that would link
against CGO must fail to publish.

**Why this priority**: The release artefacts are the only thing
operators actually run. If CGO leaks into the release path even though
the test path is clean, the constitutional invariant is silently
violated for everyone downstream. Signing the checksums manifest gives
operators a verifiable chain of trust that does not depend on the host
that ran the release.

**Independent Test**: Cut a pre-release tag on a feature branch;
observe that the release pipeline produces binaries for darwin-amd64,
darwin-arm64, linux-amd64, and linux-arm64; that a checksums manifest
covering all four is produced and signed; and that running `file` on
each binary confirms it is a statically-linked native binary with no
CGO dynamic-linker requirement.

**Acceptance Scenarios**:

1. **Given** a release tag is created, **When** the release pipeline
   runs, **Then** the build environment has CGO disabled and the build
   fails if any source file would require CGO.
2. **Given** the release pipeline completes successfully, **When** the
   release artefacts are inspected, **Then** binaries exist for
   darwin-amd64, darwin-arm64, linux-amd64, and linux-arm64.
3. **Given** the release artefacts are published, **When** a consumer
   downloads the checksums manifest, **Then** the manifest is
   accompanied by a verifiable signature that the consumer can validate
   against a documented public key or transparency log entry.
4. **Given** the release pipeline runs, **When** the test-suite hook
   inside the release configuration executes, **Then** the same race
   detector and lint discipline that gates a pull request is applied (no
   release softens the gates that protect the default branch).

---

### Edge Cases

- A fuzz target is too slow to fit within the per-pull-request smoke
  budget. The smoke MUST still execute that target for the full budget;
  the budget MUST NOT be reduced and the target MUST NOT be excluded.
  The nightly cron remains the place where deeper fuzz time is invested.
- A coverage report is missing or malformed (for example because a
  package failed to build). The coverage threshold gate MUST treat a
  missing or malformed report as a failure, not as zero or as a pass.
- A required gate's external tool (vulnerability scanner, secret
  scanner, coverage uploader, signing service) is temporarily
  unavailable. The gate MUST fail loudly with an actionable error rather
  than silently passing; transient failures MUST be retried where the
  underlying tool supports it, otherwise the pipeline blocks the merge
  until the tool is reachable again.
- A security-critical package is added or renamed. The coverage
  threshold gate MUST be updated to include the new or renamed package
  in the 100% bucket; the gate MUST fail until the security-critical
  package list inside the CI configuration matches the constitutional
  set.
- The supported Go toolchain is upgraded. The pipeline configuration
  MUST be updated to match the new floor before merging the upgrade;
  builds against a downgraded toolchain MUST fail.
- The supported operating-system or architecture matrix is changed.
  Every gate that runs per platform MUST run on every entry in the
  matrix; a gate that runs on only one platform MUST be explicitly
  documented as platform-conditional.
- A pull request adds a new fuzz target outside the six required by
  Constitution VIII. The new target MUST be added to both the
  per-pull-request smoke set and the nightly cron set; the set MUST NOT
  diverge between the two workflows.

## Requirements *(mandatory)*

### Functional Requirements

#### Continuous-integration matrix

- **FR-001**: The release-gate pipeline MUST run every per-pull-request
  gate on a matrix containing exactly two operating-system / architecture
  pairs: macOS-arm64 and Linux-amd64. The two additional release targets
  (darwin-amd64 and linux-arm64) are cross-compile-only inside the
  release artefact pipeline (FR-024) and are NOT tested per pull
  request; FR-027 ensures a cross-compile failure for either target
  fails the release closed.
- **FR-002**: The release-gate pipeline MUST use Go 1.26 as the
  toolchain floor on every matrix entry. A gate or build that compiles
  against an older toolchain MUST fail.
- **FR-003**: A failure of any gate on any matrix entry MUST mark the
  whole pipeline run as failed and MUST block merge of the pull request.

#### Required per-pull-request gates

- **FR-004**: The pipeline MUST include a formatting gate that fails if
  any Go source file would be modified by the project's canonical
  formatter (`magex format:fix --check` is the contract: the gate MUST
  exit non-zero when there is a diff and MUST NOT modify files in CI).
- **FR-005**: The pipeline MUST include a linting gate that runs the
  project's linter configuration (`magex lint`, which delegates to
  `.golangci.json`) and fails on any reported violation.
- **FR-006**: The pipeline MUST include a unit-and-integration test gate
  that runs the project's test suite with the race detector enabled
  (`magex test:race`). The race detector MUST NOT be disabled to keep
  the gate green.
- **FR-007**: The pipeline MUST include a pre-commit hook gate that runs
  the same `go-pre-commit` hook configuration developers run locally,
  so CI mirrors local-dev results.
- **FR-008**: The pipeline MUST include a vulnerability-scan gate
  (`govulncheck`) that fails on any reported finding regardless of
  severity. Waivers MUST be declared in a checked-in allow-list file
  (`.govulncheck-allow.yml` at the repository root) whose entries list
  the OSV/GHSA identifier, a justification, and an expiry date; the
  gate MUST treat an expired waiver as a finding and MUST fail. Free-
  form pull-request descriptions are non-authoritative for waiver
  purposes; the file in git is the single source of truth.
- **FR-009**: The pipeline MUST include a secret-scan gate (`gitleaks`)
  that fails on any finding.
- **FR-010**: The pipeline MUST include a per-pull-request fuzz-smoke
  gate that runs each of the six required fuzz targets — vault file
  decode, ES256K JWT parse/validate, ECIES decrypt input, request
  signature payload, supervisor config TOML parsing, and status-socket
  JSON encoding (when custom parsing exists) — for a per-target budget
  of at least 30 seconds and fails if any target panics, leaks memory
  unboundedly, or returns an untyped error. This list is canonical per
  Constitution VIII / `docs/TESTING-STRATEGY.md` §2 and supersedes any
  divergent enumeration in the parent chunk document.
- **FR-011**: The pipeline MUST include a coverage-reporting step that
  uploads the project coverage report to the project's coverage-reporting
  service on every successful pull-request run.

#### Coverage threshold gate

- **FR-012**: The pipeline MUST include a coverage-threshold gate that
  computes total repository statement coverage from the test run.
- **FR-013**: The coverage-threshold gate MUST fail the pipeline if
  total repository coverage is below 90%.
- **FR-014**: The coverage-threshold gate MUST fail the pipeline if the
  coverage for any of the following packages is below 100%:
  `internal/keys`, `internal/vault`, `internal/vault/securebytes`,
  `internal/token`, `internal/transport/sign`,
  `internal/transport/ecies`, `internal/audit`. These are the
  security-critical packages identified by Constitution VIII and
  `docs/TESTING-STRATEGY.md`.
- **FR-015**: The coverage-threshold gate MUST treat a missing or
  malformed coverage report as a gate failure, not as a pass.
- **FR-016**: The list of security-critical packages used by the gate
  MUST be defined as a hardcoded constant inside the coverage-threshold
  script (in `.github/scripts/`). The script's self-test suite MUST
  assert byte-equality between that constant and the enumeration
  recorded in `.specify/memory/constitution.md` (the canonical
  human-authoritative copy); any divergence between the two MUST fail
  the self-test (which in turn fails CI). No other file in the
  repository may carry a second machine-read copy of the list.

#### Constitutional invariants

- **FR-017**: The pipeline MUST include a no-vendor gate that fails if
  a `/vendor` directory exists at the repository root.
- **FR-018**: The pipeline MUST include a no-CGO gate that fails if any
  non-test Go source file contains the CGO import marker (`import "C"`)
  or if any build or test step is observed to have CGO enabled at
  invocation time.
- **FR-019**: Every Go build or test invocation in every workflow MUST
  set `CGO_ENABLED=0` in the environment.

#### Nightly cron

- **FR-020**: The pipeline MUST schedule a recurring (nightly or more
  frequent) fuzz workflow that runs each of the six required fuzz
  targets for an effective per-target fuzz time of at least 60 seconds.
- **FR-021**: The nightly cron MUST NOT skip any of the six required
  fuzz targets to fit a time budget; if the budget is exceeded, the
  cron's per-target budget MUST be configurable so it can be tuned
  without removing a target.
- **FR-022**: A crashing input found by the nightly cron MUST be
  preserved in a form that allows local reproduction.

#### Release artefact pipeline

- **FR-023**: The release artefact configuration MUST set
  `CGO_ENABLED=0` in the build environment.
- **FR-024**: The release artefact pipeline MUST produce a release
  binary for each of darwin-amd64, darwin-arm64, linux-amd64, and
  linux-arm64.
- **FR-025**: The release artefact pipeline MUST produce a checksums
  manifest covering every produced binary and MUST sign that manifest
  using cosign keyless signing (Sigstore Fulcio for the certificate,
  Rekor for the transparency-log entry). The signing identity MUST be
  the GitHub Actions OIDC identity of the release workflow on the
  release tag ref. No long-lived private signing key is stored in the
  repository or in CI secrets. The signature and certificate artefacts
  MUST be published alongside the manifest so a consumer can verify
  both the cryptographic signature and the Rekor transparency-log
  inclusion proof.
- **FR-026**: The release artefact pipeline MUST apply the same race
  detector and lint discipline as the per-pull-request gates; a
  release-time test hook MUST NOT relax those gates.
- **FR-027**: The release artefact pipeline MUST fail closed: any
  build that requires CGO, any binary that would dynamically link
  against the system C library because of an accidental CGO dependency,
  or any unsupported operating-system / architecture target slipping
  into the matrix MUST cause the release to fail rather than publish a
  partial set.

#### Workflow discipline

- **FR-028**: No gate listed above MAY be downgraded, conditionally
  skipped, or marked non-blocking on the default branch's protection
  rules. A gate is either required or it is removed from this
  specification by amendment.
- **FR-029**: When a new fuzz target is added to Constitution VIII or
  `docs/TESTING-STRATEGY.md`, the per-pull-request smoke set and the
  nightly cron set MUST both be updated; the two sets MUST NOT diverge.
- **FR-030**: The release-gate workflow files MUST be the AC-9 owner
  recorded in `docs/AC-MATRIX.md`; the AC-9 row MUST cite the workflow
  files by path once this feature is complete.

### Key Entities

- **Per-pull-request gate**: A single check that the continuous-
  integration pipeline runs on every pull request and whose pass/fail
  result is part of the merge-block decision. The set of gates is the
  list enumerated in FR-004 through FR-019.
- **Required fuzz target**: A Go fuzz function that must be executed by
  both the per-pull-request smoke and the nightly cron. The current set
  is the six functions named in FR-010, drawn from Constitution VIII
  and `docs/TESTING-STRATEGY.md`. The set is a single source of truth
  shared between workflows.
- **Security-critical package**: A Go package whose coverage must be
  100% per Constitution VIII. The set is enumerated in FR-014.
- **Coverage threshold gate**: The gate enumerated in FR-012 through
  FR-016. It reads a single coverage report produced by the test gate
  and decides pass/fail based on the project-wide and per-package
  thresholds.
- **Release artefact**: A signed binary plus its row in the checksums
  manifest, produced by the release artefact pipeline for one
  operating-system / architecture pair.
- **CI matrix entry**: A pair of operating-system and architecture
  (currently macOS-arm64 and Linux-amd64) on which the per-pull-request
  gates run.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A sample pull request opened against the default branch
  triggers every required gate on every matrix entry; every gate
  finishes within the workflow's normal time budget; and a maintainer
  can read the pull-request page and see explicit pass markers for
  format, lint, race-tests, pre-commit, vulnerability scan, secret
  scan, fuzz smoke, coverage upload, coverage threshold, no-vendor, and
  no-CGO.
- **SC-002**: A pull request that breaks any single one of the gates
  above results in that gate's status being failure on the pull-request
  page within the same workflow run; merge is blocked by the platform's
  branch-protection enforcement.
- **SC-003**: After a green run, the project's public coverage badge
  reflects the new measurement, demonstrating that the coverage upload
  step actually published to the reporting service.
- **SC-004**: A coverage measurement that places total repository
  coverage below 90% — or any security-critical package below 100% —
  causes the coverage threshold gate to fail; an audit of the failing
  gate's output identifies the package and percentage responsible.
- **SC-005**: A scheduled run of the nightly fuzz workflow executes all
  six required fuzz targets for at least sixty seconds of effective
  fuzz time per target with no panic, no untyped error, and no
  unbounded memory growth.
- **SC-006**: A release tag created on a release-ready commit produces
  four binaries (darwin-amd64, darwin-arm64, linux-amd64, linux-arm64),
  a checksums manifest covering all four, and a verifiable signature
  artefact, with all four binaries identifying as statically-linked
  pure-Go binaries (no dynamic libc dependency introduced by CGO).
- **SC-007**: The repository's `docs/AC-MATRIX.md` AC-9 row cites the
  release-gate workflow files by path and is marked `green`; no other
  release-gate-related row is left in `pending` because of missing CI
  coverage of the constitutional gates.
- **SC-008**: At any point a maintainer can answer "which CI workflow
  enforces gate X?" by reading the spec for this feature and the
  workflow files it points at, without inferring the answer from CI
  logs.
- **SC-009**: An attempt to introduce a `/vendor` directory or any
  non-test `import "C"` in a pull request fails CI within the same
  workflow run that performs the build, with a workflow-step error
  message that names the offending path or import.

## Assumptions

- The project's existing magex task surface (`magex format:fix
  --check`, `magex lint`, `magex test:race`) is the canonical entry
  point for formatting, linting, and race tests and will continue to
  exist for the lifetime of this feature. If those entry points are
  renamed, the gates will be updated to follow them rather than to
  reimplement their behaviour.
- The project's existing `go-pre-commit` hook configuration is the
  authoritative pre-commit definition; CI invokes the same
  configuration developers use locally.
- The vulnerability scanner is `govulncheck` (Go's first-party tool)
  and the secret scanner is `gitleaks`, per Constitution XI's
  Security Requirements table.
- The coverage-reporting service the project uploads to is the one
  already configured for the repository (Codecov, per
  `.specify/memory/constitution.md` § Development Workflow); no new
  reporting service is in scope.
- The six required fuzz targets are exactly the six listed in
  Constitution VIII and `docs/TESTING-STRATEGY.md` §2 — vault file
  decode, JWT parse/validate, ECIES decrypt input handling, request
  signature payload parsing, supervisor config TOML parsing, and
  status-socket JSON encoding (where custom parsing exists). All six
  have shipped under prior SDD chunks and are present in the codebase.
  This list supersedes any divergent enumeration in the parent
  chunk document (`docs/sdd/SDD-31.md`); FR-010 is the canonical
  enumeration for the workflow files this feature produces.
- The Go-1.26 floor in FR-002 matches `go.mod`; if `go.mod` is
  upgraded, the floor moves with it.
- The supported release operating-system / architecture pairs are
  exactly the four listed in FR-024 — darwin and linux on each of
  amd64 and arm64. Windows and FreeBSD are explicitly out of scope.
- Branch-protection rules on the default branch require every gate
  named in this specification to pass before merge; configuring the
  branch-protection rules themselves is platform-administration work
  performed alongside this feature but not within the workflow files
  it produces.
- Signing the checksums manifest uses cosign keyless via Sigstore
  Fulcio + Rekor with the GitHub Actions OIDC identity of the release
  workflow on the release tag ref (locked in FR-025); the GoReleaser
  cosign integration is the assumed implementation path. No long-lived
  signing key is kept in repo or CI secrets.
- The release artefact pipeline runs on tag push, not on every pull
  request; pull-request runs exercise the same build environment via
  the no-CGO check and the test gate but do not publish artefacts.
- A pull request author may add new fuzz targets; doing so triggers
  FR-029's discipline (smoke and cron sets stay in sync), but the
  per-pull-request budget per target may be tuned in the plan phase as
  long as no target is excluded.
- The project's existing `.github/workflows/` tree contains historical
  workflows from a prior automation framework. This feature owns
  whichever workflow files implement the gates listed here; any
  pre-existing workflow that duplicates a gate enforced by this feature
  is out of this feature's scope to remove, but pre-existing workflows
  MUST NOT relax the constitutional bar.
