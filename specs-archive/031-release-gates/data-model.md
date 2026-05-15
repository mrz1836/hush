# Phase 1 — Data Model

**Feature**: SDD-31 release gates · **Branch**: `031-release-gates` · **Date**: 2026-05-14

This feature ships CI plumbing — no database schema, no persisted entity. The "data model" here captures the **conceptual entities** the workflows operate on, the relationships between them, and the validation rules each entity must satisfy. Each entity has a single source of truth, and that source is recorded.

---

## Entity 1 — Gate

A single check that the per-PR or release pipeline runs.

| Field | Type | Source | Notes |
|---|---|---|---|
| `id`              | string (kebab-case) | this doc | e.g. `format-check`, `lint`, `test-race`, `no-vendor`, `no-cgo`, `pre-commit`, `govulncheck`, `gitleaks`, `fuzz-smoke`, `codecov-upload`, `coverage-threshold` |
| `fr_ref`          | string              | spec.md  | The FR-### the gate satisfies (e.g. `FR-004` for format-check) |
| `command`         | shell snippet       | spec / R | The exact command CI invokes (e.g. `magex format:fix --check`) |
| `required_check`  | bool                | spec FR-028 | TRUE for every gate in this feature — none is downgradable |
| `runs_on`         | enum                | plan     | `matrix` (both OS pairs) \| `linux-only` \| `release-only` |
| `failure_exit`    | string              | spec     | What "fail" looks like (non-zero exit; specific exit codes if relevant) |

**Validation rules**:
- Every `id` MUST be unique within ci.yml job-step IDs.
- Every gate MUST appear as a required status check on the default-branch protection rule (FR-028 — admin task, not part of this chunk's file changes).
- A gate that fails MUST NOT be marked `continue-on-error: true` (FR-003).

**Per-PR gate inventory** (eleven gates — the full set required by FR-004 … FR-019):

| `id`                 | `fr_ref`   | `command`                                                                                    | `runs_on` |
|----------------------|------------|----------------------------------------------------------------------------------------------|-----------|
| `format-check`       | FR-004     | `magex format:fix --check`                                                                   | matrix    |
| `lint`               | FR-005     | `magex lint`                                                                                 | matrix    |
| `test-race`          | FR-006     | `magex test:race -- -coverprofile=cover.out -covermode=atomic`                               | matrix    |
| `pre-commit`         | FR-007     | `go-pre-commit run --all`                                                                    | matrix    |
| `govulncheck`        | FR-008     | `govulncheck -format=json ./... \| <waiver-filter> ; assert no remaining`                    | matrix    |
| `gitleaks`           | FR-009     | `gitleaks detect --config .gitleaks.toml --no-banner --redact`                               | matrix    |
| `fuzz-smoke`         | FR-010     | `go test -run=^$ -fuzz=^<target>$ -fuzztime=30s ./<pkg>` × 6 (sequential, fail-fast)        | matrix    |
| `codecov-upload`     | FR-011     | `codecov/codecov-action@v4` with `files: ./cover.out`                                        | matrix    |
| `coverage-threshold` | FR-012–016 | `go run ./.github/scripts/coverage-threshold -cover cover.out -min-project 90 -constitution .specify/memory/constitution.md` | matrix |
| `no-vendor`          | FR-017     | `test ! -d vendor`                                                                           | matrix    |
| `no-cgo`             | FR-018/019 | `! grep -rn 'import "C"' --include='*.go' --exclude='*_test.go' .` (+ env `CGO_ENABLED=0`)   | matrix    |

**Release-only gate inventory** (release.yml):

| `id`                | `fr_ref`   | `command` |
|---------------------|------------|-----------|
| `release-test-race` | FR-026     | invoked by GoReleaser's `before.hooks` running `magex test` (race detector on by default) |
| `release-build`     | FR-023/024 | `goreleaser release --clean` (env `CGO_ENABLED=0`, four-target build) |
| `release-sign`      | FR-025     | GoReleaser `signs:` block invoking `cosign sign-blob` keyless |

---

## Entity 2 — Fuzz Target

A Go fuzz function that runs in both the per-PR smoke and the nightly cron.

| Field | Type | Source | Notes |
|---|---|---|---|
| `name`     | string  | source code | e.g. `FuzzVaultDecode` |
| `package`  | string  | source code | e.g. `internal/vault` |
| `file`     | string  | source code | e.g. `internal/vault/vault_fuzz_test.go` |
| `smoke_s`  | int     | spec FR-010 | 30 (per-PR budget, seconds) |
| `cron_s`   | int     | spec FR-020 + plan R-005 | 300 (nightly budget, seconds; configurable via workflow_dispatch input) |

**Validation rules**:
- The set MUST equal exactly the six entries from Research R-001. No omission (FR-021), no extra (avoid scope-creep).
- A new fuzz target MUST be added to BOTH lists in the same change (FR-029).
- The list lives ONCE — as a workflow-level `strategy.matrix.fuzz_target` array in each workflow — referenced twice (once in ci.yml fuzz-smoke step, once in fuzz-cron.yml). The two files MUST stay in lockstep; a CI lint task (deferred to a future chunk if not now) could enforce this, but for this chunk the discipline is human-review.

**Canonical six** (from Research R-001):

| # | `name`                  | `package`                   |
|---|-------------------------|-----------------------------|
| 1 | `FuzzVaultDecode`       | `internal/vault`            |
| 2 | `FuzzJWTValidate`       | `internal/token`            |
| 3 | `FuzzECIESDecrypt`      | `internal/transport/ecies`  |
| 4 | `FuzzVerifyRequest`     | `internal/transport/sign`   |
| 5 | `FuzzSuperviseTOML`     | `internal/supervise/config` |
| 6 | `FuzzStatusJSON_Encode` | `internal/supervise`        |

---

## Entity 3 — Security-Critical Package

A Go package whose statement coverage MUST be 100 %.

| Field | Type | Source | Notes |
|---|---|---|---|
| `path`         | string | spec FR-014 + Research R-002 | e.g. `internal/keys` |
| `min_coverage` | float  | spec FR-014                   | 100.0 (constant) |
| `source_of_truth` | string | spec FR-016 + Research R-002 | `.specify/memory/constitution.md` fenced block `security-critical-packages` |

**Validation rules**:
- The list MUST be byte-equal between (a) the hardcoded constant in `.github/scripts/coverage-threshold/compute.go` and (b) the fenced block in `.specify/memory/constitution.md`. Asserted by `TestSecurityCriticalListMatchesConstitution` at every CI run (FR-016).
- A new security-critical package added to the constitution MUST be added to the script's constant in the SAME change (FR-016 byte-equality forces this).

**Canonical seven** (from spec FR-014 + Research R-002):

```text
internal/keys
internal/vault
internal/vault/securebytes
internal/token
internal/transport/sign
internal/transport/ecies
internal/audit
```

---

## Entity 4 — CI Matrix Entry

A `(GOOS, GOARCH)` pair on which the per-PR gates execute.

| Field | Type | Source | Notes |
|---|---|---|---|
| `os_label`  | string | spec FR-001 | `macos-arm64` \| `linux-amd64` (the only two entries) |
| `runs_on`   | string | plan        | `macos-14` (arm64) \| `ubuntu-24.04` |
| `goos`      | string | plan        | `darwin` \| `linux` |
| `goarch`    | string | plan        | `arm64` \| `amd64` |
| `go_version`| string | spec FR-002 | `1.26.1` (read from `go.mod`) |

**Validation rules**:
- Exactly two entries per FR-001. No additional matrix axes (no Windows, no FreeBSD per spec assumption).
- The Go version MUST equal the `go.mod` floor — never older (FR-002). Newer is acceptable on a major-version bump only when `go.mod` is bumped too.

---

## Entity 5 — Release Artefact

A signed binary plus its row in the SHA256SUMS manifest.

| Field | Type | Source | Notes |
|---|---|---|---|
| `goos`         | string | spec FR-024 | `darwin` \| `linux` |
| `goarch`       | string | spec FR-024 | `amd64` \| `arm64` |
| `binary_name`  | string | `.goreleaser.yml` archive `name_template` | `hush_<ver>_<os>_<arch>.tar.gz` (existing template) |
| `cgo_enabled`  | bool   | spec FR-023 | MUST equal `false` (env `CGO_ENABLED=0`) |
| `checksum_alg` | string | `.goreleaser.yml`                          | `sha256` (existing) |
| `signed_by`    | string | spec FR-025 | `cosign-keyless-github-oidc` |
| `signature`    | string | GoReleaser  | published as `${manifest}.sig` next to SHA256SUMS |
| `certificate`  | string | GoReleaser  | published as `${manifest}.pem` next to SHA256SUMS |
| `rekor_entry`  | string | cosign      | transparency-log inclusion proof — discoverable via Rekor search |

**Validation rules**:
- Exactly four artefacts per release tag: `{darwin, linux} × {amd64, arm64}` (FR-024).
- Every artefact MUST be statically-linked pure Go — `file <bin>` MUST report no dynamic linker reference (SC-006). Enforced indirectly by `CGO_ENABLED=0` + no `import "C"` in non-test source.
- The signature artefact MUST verify via `cosign verify-blob --certificate <cert> --signature <sig> --certificate-identity-regexp 'release-tag-ref' --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS`.

---

## Entity 6 — Waiver Entry

A `govulncheck` waiver in `.govulncheck-allow.yml`.

| Field | Type | Source | Notes |
|---|---|---|---|
| `id`            | string | spec FR-008 | OSV or GHSA identifier (e.g. `GO-2024-1234`) |
| `justification` | string | spec FR-008 | Free-form rationale (audit trail; no machine validation) |
| `expires`       | RFC-3339 date | spec FR-008 | YYYY-MM-DD; an expired waiver is treated as a finding (FR-008) |

**Validation rules**:
- File schema MUST be `{ vulns: [<entry>, ...] }` (versioned via top-level `version: 1` field; implement-phase may settle exact spelling).
- Implement-phase will start the file empty `{ version: 1, vulns: [] }` — waivers are added by maintainer review only.
- The PR description is NEVER authoritative (FR-008 explicit lock).

---

## Entity 7 — Coverage Snapshot

The output of `go test -coverprofile=cover.out`, parsed by the coverage-threshold tool.

| Field | Type | Source | Notes |
|---|---|---|---|
| `total_pct`     | float                        | computed | Project-wide statement coverage |
| `per_pkg_pct`   | map<string, float>           | computed | Per-package coverage |
| `missing_pkgs`  | set<string>                  | computed | Security-critical packages absent from cover.out (FR-015 fail-on-missing) |

**Validation rules**:
- `total_pct < 90.0` → fail (FR-013).
- `per_pkg_pct[<security-critical>] < 100.0` for ANY entry → fail (FR-014).
- `missing_pkgs` non-empty → fail (FR-015 — missing report ≠ pass).
- `cover.out` malformed (parse error) → fail (FR-015).

---

## Relationship Summary

```text
Gate ─────────────────────────────── runs on ─────────────────────► CI Matrix Entry  (per-PR gates)
                                                              └──► Release-only (release gates)

Fuzz Target ──── consumed by ──► Gate "fuzz-smoke"  (per-PR, 30 s/target)
            └─── consumed by ──► fuzz-cron.yml      (nightly, 300 s/target)

Security-Critical Package ──── consumed by ──► Gate "coverage-threshold"
                          └─── byte-equal-with ── Constitution VIII fenced block

Release Artefact ─── produced by ──► Gate "release-build"
                └─── signed by ────► Gate "release-sign"  (cosign keyless via OIDC)

Waiver Entry ─── consumed by ──► Gate "govulncheck"  (suppresses matching finding until `expires`)

Coverage Snapshot ─── produced by ──► Gate "test-race"
                 └─── consumed by ──► Gate "coverage-threshold" + Gate "codecov-upload"
```

No cross-feature entities; everything in this model lives inside this chunk's file scope.
