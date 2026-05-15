# Implementation Plan: Release Gates (Coverage + Fuzz + Vulnerability + Secret Scans + CGO=0 + No Vendor)

**Branch**: `031-release-gates` | **Date**: 2026-05-14 | **Spec**: [specs/031-release-gates/spec.md](./spec.md)

**Input**: Feature specification from `/specs/031-release-gates/spec.md`; chunk contract `docs/sdd/SDD-31.md`.

## Summary

Wire every Constitution VIII / XI gate into CI as three new workflow files plus a Go coverage-threshold tool:

- **`.github/workflows/ci.yml`** — per-pull-request matrix (macOS-arm64 + linux-amd64, Go 1.26.1) running: `magex format:fix --check`, `magex lint`, no-vendor + no-CGO checks (FR-017/018/019), `magex test:race` with `-coverprofile=cover.out`, `go-pre-commit run --all`, `govulncheck ./...` (FR-008 with `.govulncheck-allow.yml`), `gitleaks detect` (FR-009 using existing `.gitleaks.toml`), 30 s fuzz smoke on each of the six canonical targets (FR-010), `codecov/codecov-action@v4` upload (FR-011), and a coverage-threshold gate (FR-012–016).
- **`.github/workflows/fuzz-cron.yml`** — nightly schedule at `0 7 * * *` UTC plus `workflow_dispatch`, 300 s budget per target (configurable via input), all six targets MUST run, crashing inputs preserved as workflow artefacts (FR-020/021/022).
- **`.github/workflows/release.yml`** — fires on `push` tags `v*`; sets up Go 1.26.1 + `sigstore/cosign-installer@v3`; invokes `goreleaser/goreleaser-action@v6` with OIDC `id-token: write` so cosign keyless signing publishes Fulcio certificates + Rekor inclusion proofs alongside the checksums manifest (FR-023–027).
- **`.github/scripts/coverage-threshold/`** — pure-Go (stdlib-only) tool that parses `cover.out`, computes per-package statement coverage, fails on project < 90 % or any security-critical package < 100 %, with a self-test asserting byte-equality between the script's hardcoded security-critical list and a structured fenced enumeration appended to `.specify/memory/constitution.md` (FR-016 closure — see Research R-002).

The plan **does NOT** remove the legacy `fortress-*` workflows (out of scope per spec assumption); those workflows already mirror or duplicate some gates, and pre-existing workflows MUST NOT relax the constitutional bar (spec assumption, last bullet). The new files own AC-9; branch-protection administration is the operator's job, not this feature's.

## Technical Context

**Language/Version**: Go 1.26.1 (from `go.mod`); pinned floor for every CI matrix entry per FR-002.

**Primary Dependencies**:
- CI runtime: `actions/checkout@v4`, `actions/setup-go@v5`, `actions/cache@v4`, `codecov/codecov-action@v4`, `gitleaks/gitleaks-action@v2`, `golang.org/x/vuln/cmd/govulncheck@latest`, `sigstore/cosign-installer@v3`, `goreleaser/goreleaser-action@v6`. All are GitHub-Actions runners, not Go module dependencies — so Constitution XI's "every NEW direct Go dependency requires written justification" gate does NOT fire.
- Coverage-threshold tool: Go stdlib only (`bufio`, `os`, `strings`, `strconv`, `fmt`, `errors`, `flag`, `testing`). Zero new Go module imports.

**Storage**: N/A (workflows operate on the repo working tree + ephemeral CI scratch).

**Testing**:
- Coverage-threshold tool: `TestCoverageThreshold_ProjectGEThreshold`, `TestCoverageThreshold_SecurityCriticalEQ100`, `TestCoverageThreshold_FailsBelowThreshold`, `TestCoverageThreshold_FailsOnMissingPackage`, `TestCoverageThreshold_FailsOnMalformedCoverOut`, `TestSecurityCriticalListMatchesConstitution` (FR-016 byte-equality self-test).
- Workflows themselves: the green CI run on a sample PR is the acceptance test (cannot TDD a CI workflow file; the chunk-doc and spec acknowledge this).

**Target Platform**: GitHub-hosted runners — `macos-14` (Apple-silicon, GOOS=darwin GOARCH=arm64) and `ubuntu-24.04` (GOOS=linux GOARCH=amd64). Release pipeline cross-compiles to `darwin-amd64` and `linux-arm64` from the `ubuntu-24.04` runner via GoReleaser (FR-024 + FR-027).

**Project Type**: CLI binary (`cmd/hush`) plus CI plumbing. This chunk adds workflow YAML + a single Go tool under `.github/scripts/`; no production package gains code.

**Performance Goals**:
- Per-PR pipeline target wall-clock: under 12 min cold (cache warm: ~6 min). 30 s × 6 fuzz targets = 180 s of fuzz; format / lint / build / test:race dominate the rest.
- Nightly cron target: 6 × 300 s fuzz = 30 min plus setup overhead — schedule outside business hours.
- Release pipeline target: under 10 min for the four-binary matrix plus cosign sign + Rekor upload.

**Constraints**:
- Race detector MUST stay enabled in the test step (FR-006; chunk-doc anti-contract).
- `CGO_ENABLED=0` MUST be set in every Go invocation environment (FR-019); enforced by job-level `env:` block and verified by a separate `! grep -r 'import "C"' --include='*.go' .` step that excludes `_test.go` files (FR-018).
- Per-PR pipeline MUST fail closed (FR-003): every matrix leg + every gate is `required: true` for merge per branch-protection; nothing is `continue-on-error`.
- No fuzz target may be skipped (chunk-doc anti-contract; FR-021, FR-029).
- Release pipeline MUST NOT relax the test bar (FR-026); GoReleaser's `before.hooks` runs `magex test` which is race-on by default.

**Scale/Scope**:
- One new ci.yml, one new fuzz-cron.yml, one new release.yml, plus one tool dir (`.github/scripts/coverage-threshold/`). Edits to `.goreleaser.yml` (add linux builds + cosign signs block); zero edits to `.golangci.json` (review concluded the existing linter set already covers Constitution IX — see Research R-005).
- One PATCH-level edit to `.specify/memory/constitution.md` (append a fenced security-critical-package enumeration block under Principle VIII; bump to 1.1.1 — see Research R-002).
- One AC-MATRIX edit (point AC-9 row at the three new workflow files).
- One PACKAGE-MAP edit (new `.github/workflows/` section).

## Constitution Check

*GATE: Re-evaluated after Phase 1 design — see "Post-Design Re-Check" below.*

| Principle | In Scope? | How the Plan Honours It | Status |
|---|---|---|---|
| **I — Zero Files at Rest on Agent Machines** | No | Workflows ship no agent-side artefacts. | n/a |
| **II — Approval is Human, Phone** | No | No runtime approval surface touched. | n/a |
| **III — Defense in Depth Through Crypto Layering** | No (the gates enforce that the *implementation* of III stays tested, but the chunk does not edit any crypto code). | Gates ensure 100 % coverage on `internal/keys`, `internal/vault`, `internal/vault/securebytes`, `internal/token`, `internal/transport/sign`, `internal/transport/ecies`, `internal/audit` — the seven security-critical packages from FR-014. | clean |
| **IV — Supervisor / Wrap-Shell** | No | No `hush serve`/`supervise`/`request` code touched. | n/a |
| **V — Staleness Visible, Failure Loud** | No (CI failure is inherently loud via the GitHub status API). | All gates `required: true` on the default branch; no `continue-on-error`. | clean |
| **VI — Tailscale-Only** | No | Workflows run on GitHub-hosted runners; do NOT touch the vault host or its bind config. | n/a |
| **VII — CLI Design Standards** | No | No CLI surface changed. | n/a |
| **VIII — Testing Discipline** | **YES — load-bearing** | Format-check, lint, race-test, six-fuzz-target smoke (FR-010), nightly six-target cron ≥ 60 s per target (FR-020), coverage threshold 90 % project / 100 % per security-critical pkg (FR-013/014), `govulncheck` (FR-008), `gitleaks` (FR-009), `go-pre-commit` (FR-007), codecov upload (FR-011). All gates wired into `required` checks; nothing soft-pinned. Coverage-threshold self-test enforces byte-equality against the constitution's enumeration (FR-016). | **clean** |
| **IX — Idiomatic Go Discipline** | **YES** | The coverage-threshold tool is the only new Go code. Constraint pack: package `main`, no `init()`, no package-level mutable state, errors wrapped with `%w`, sentinel errors `var ErrXxx = errors.New(...)`, table-driven `_test.go`. Linted by the same `magex lint` that gates the rest of the repo. **CGO=0 invariant enforced by the no-CGO gate** (FR-018/019) per Principle IX's "CGO disabled" bullet, with the constitutional amendment unchanged. **No-vendor invariant enforced by FR-017** per Principle IX's "Modules-only" bullet. | **clean** |
| **X — Observability & Redaction** | No | CI does not process secret material; gitleaks gate (FR-009) is the inverse — it asserts the repo carries no plaintext secret. | clean |
| **XI — Native-First, Minimal Dependencies, Ephemeral Vault** | **YES** | Zero new Go direct dependencies: the coverage-threshold tool is stdlib-only (`bufio`, `os`, `strings`, `strconv`, `fmt`, `errors`, `flag`, `testing`). `govulncheck` + `gitleaks` are CI tools, not Go module deps. CGO disabled in CI (FR-019) and release (FR-023), enforced by the no-CGO gate (FR-018). `/vendor` forbidden (FR-017). | **clean** |

**Result**: PASS — no Constitution violation. The Complexity Tracking table at the bottom of this plan is therefore empty.

## Project Structure

### Documentation (this feature)

```text
specs/031-release-gates/
├── plan.md              # This file
├── spec.md              # Feature specification (already approved + clarified)
├── research.md          # Phase 0 — design decisions (R-001 … R-007)
├── data-model.md        # Phase 1 — entities (gate, fuzz target, security-critical pkg, matrix entry, release artefact)
├── quickstart.md        # Phase 1 — operator/maintainer quickstart (local mirror + sample PR)
└── contracts/
    ├── ci-workflow.md             # CI workflow input/output contract
    ├── fuzz-cron-workflow.md      # Nightly cron contract
    ├── release-workflow.md        # Release pipeline contract
    └── coverage-threshold-cli.md  # Coverage-threshold tool CLI contract
```

### Source Code (repository root)

```text
.github/
├── workflows/
│   ├── ci.yml                      # NEW — per-PR matrix gates (this chunk)
│   ├── fuzz-cron.yml               # NEW — nightly fuzz cron (this chunk)
│   ├── release.yml                 # NEW — tag → GoReleaser + cosign (this chunk)
│   └── fortress-*.yml              # EXISTING — out of scope to remove; must NOT relax bar
└── scripts/
    └── coverage-threshold/
        ├── main.go                 # NEW — entrypoint; calls compute()
        ├── compute.go              # NEW — pure-fn cover.out parser + threshold check
        └── compute_test.go         # NEW — TestCoverageThreshold_* + TestSecurityCriticalListMatchesConstitution

.govulncheck-allow.yml              # NEW — OSV/GHSA waiver allow-list (FR-008)
.gitleaks.toml                      # EXISTING — reused (FR-009 spec assumption)
.goreleaser.yml                     # EDITED — add linux builds + signs (cosign keyless) + ensure CGO_ENABLED=0
.golangci.json                      # REVIEWED — no change needed (Research R-005)
.specify/memory/constitution.md     # EDITED — append fenced security-critical pkg list under Principle VIII; bump to 1.1.1 (Research R-002)
docs/AC-MATRIX.md                   # EDITED — AC-9 row cites the three new workflow files (FR-030)
docs/PACKAGE-MAP.md                 # EDITED — new ".github/workflows/" section listing the three files (chunk-doc Prompt 5 step 7)
```

**Structure Decision**: All workflow files live at the conventional `.github/workflows/` path so GitHub picks them up automatically. The coverage-threshold tool lives under `.github/scripts/coverage-threshold/` per FR-016. Because Go's `./...` pattern skips directories whose name begins with `.`, CI invokes the tool by **explicit path** (`go run ./.github/scripts/coverage-threshold ...`) and tests it with an explicit path too (`go test ./.github/scripts/coverage-threshold/...`); this is documented in `contracts/coverage-threshold-cli.md` and Research R-006. The Go file count is intentionally small (three files): every line of Go in this chunk is reviewable in one sitting, satisfying Principle IX's "every line of Go in this repo MUST follow…" expectation.

## Locked HOW (resolved in Phase 0 — see `research.md`)

1. **Canonical six fuzz targets** (R-001 — resolves spec FR-010 vs chunk-doc divergence):
   - `FuzzVaultDecode`            → `internal/vault`
   - `FuzzJWTValidate`            → `internal/token`
   - `FuzzECIESDecrypt`           → `internal/transport/ecies`
   - `FuzzVerifyRequest`          → `internal/transport/sign`
   - `FuzzSuperviseTOML`          → `internal/supervise/config`
   - `FuzzStatusJSON_Encode`      → `internal/supervise`
   The chunk-doc's listing of `FuzzServerTOML` is **stale**; spec FR-010 explicitly supersedes the chunk-doc. All six functions already exist in the codebase (verified by `grep '^func Fuzz' internal/...`).

2. **Constitution amendment for FR-016 byte-equality** (R-002): append a fenced block named `# security-critical-packages` to Principle VIII listing the seven packages, one per line. The coverage-threshold tool reads this block (delimited by the fence markers) and compares byte-for-byte with its hardcoded constant. PATCH-level bump 1.1.0 → 1.1.1 (no policy change — codifies existing FR-014 intent).

3. **CI matrix entries** (FR-001 lock): exactly two — `macos-14` (Apple-silicon) and `ubuntu-24.04`. The release pipeline cross-compiles to the other two combinations (`darwin-amd64`, `linux-arm64`) from the linux runner.

4. **Per-PR fuzz smoke budget** (FR-010 lock): `-fuzz=^Fuzz<X>$ -fuzztime=30s -run=^$` per target, six invocations issued sequentially in a single job step (so a failure in the first still fails CI; total fuzz wall-clock ≤ ~3 min).

5. **Nightly fuzz cron budget** (FR-020 lock): default `-fuzztime=300s` per target (5 min), configurable via `workflow_dispatch` input `seconds_per_target`. Schedule `cron: '0 7 * * *'` (07:00 UTC ≈ 02:00 ET) on linux-amd64 only (deterministic fuzz coverage, doubling on darwin adds wall-clock without coverage gain per Research R-003).

6. **`govulncheck` waiver mechanism** (FR-008 lock): `.govulncheck-allow.yml` at repo root, schema `{vulns: [{id: "GO-2024-XXXX", justification: "<text>", expires: "YYYY-MM-DD"}]}`. The CI step runs `govulncheck -format=json ./...`, pipes through a 30-line inline `jq`/`yq` filter (or a 30-line Go helper deferred to implement-phase) that drops findings whose ID appears in an unexpired waiver, then fails if any finding remains. Spec FR-008 forbids PR-description waivers; the file is single-source-of-truth.

7. **`gitleaks` invocation** (FR-009 lock): `gitleaks/gitleaks-action@v2` with `GITLEAKS_CONFIG=.gitleaks.toml`; the existing config file is reused.

8. **`go-pre-commit` invocation** (FR-007 lock): `go install github.com/mrz1836/go-pre-commit/cmd/go-pre-commit@<pinned-version>`; `go-pre-commit run --all`. Pin version at implement-time against whatever version `.github/CONTRIBUTING.md` recommends.

9. **No-vendor gate** (FR-017 lock): `if [ -d vendor ]; then echo "::error::/vendor directory forbidden (Principle XI)"; exit 1; fi` — single-line step inside ci.yml.

10. **No-CGO gate** (FR-018 lock): two checks, both inside ci.yml:
    - `if grep -rn 'import "C"' --include='*.go' --exclude='*_test.go' .; then echo "::error::CGO import forbidden (Principle XI)"; exit 1; fi` (only non-test files; test files may use CGO for harness purposes but are unaffected because they don't end up in release binaries).
    - Job-level `env: CGO_ENABLED: "0"` forced on every step that runs `go`.

11. **Coverage-threshold CLI** (FR-012–016 lock): `go run ./.github/scripts/coverage-threshold -cover cover.out -min-project 90 -constitution .specify/memory/constitution.md`. Exit codes: `0` pass, `1` threshold breach, `2` malformed `cover.out` (FR-015), `3` constitution-list divergence (FR-016 self-test failure).

12. **`.goreleaser.yml` edits** (FR-023/024/025 lock):
    - Top-level `env: [CGO_ENABLED=0]` (already present in builds — promote to top-level so it also affects archives + signs).
    - `builds[0].goos: [darwin, linux]` (currently `[darwin]` only).
    - New `signs:` block: `[{cmd: cosign, signature: '${artifact}.sig', certificate: '${artifact}.pem', args: ['sign-blob', '--yes', '--output-signature=${signature}', '--output-certificate=${certificate}', '${artifact}'], artifacts: checksum}]` — Sigstore Fulcio cert + Rekor inclusion proof published alongside SHA256SUMS.
    - Release workflow grants `id-token: write` so cosign keyless picks up the GitHub OIDC identity (FR-025 lock).

### Post-Design Re-Check (after Phase 1)

Re-running the Constitution Check against the finalised contracts/, data-model.md, and quickstart.md:

- **VIII**: every gate listed in FR-004 … FR-016 has a row in `data-model.md §Gate` and an explicit step in `contracts/ci-workflow.md` → still **clean**.
- **IX**: the coverage-threshold tool's API contract (`contracts/coverage-threshold-cli.md`) keeps the surface ≤ 80 lines of Go, stdlib-only, no init, no globals → still **clean**.
- **X**: gitleaks gate produces no false-positive logging beyond exit code; no secret material ever enters CI workflow logs → still **clean**.
- **XI**: zero new Go module imports introduced; CGO=0 + no-vendor invariants enforced by named gates → still **clean**.

**Result of post-design check**: PASS. Complexity Tracking table remains empty.

## Phase Deliverables

### Phase 0 (this command) — `research.md`

Resolves the seven unknowns / design choices catalogued as R-001 … R-007 below.

### Phase 1 (this command)

- `data-model.md` — entity model for Gate, Fuzz Target, Security-Critical Package, CI Matrix Entry, Release Artefact, Waiver Entry.
- `contracts/ci-workflow.md` — trigger events, job graph, gate-step inventory, required check names for branch protection.
- `contracts/fuzz-cron-workflow.md` — schedule, dispatch inputs, per-target budget, artefact preservation contract.
- `contracts/release-workflow.md` — trigger events, OIDC permissions, GoReleaser invocation, cosign signing contract.
- `contracts/coverage-threshold-cli.md` — CLI flags, exit codes, cover.out parsing contract, self-test guarantees.
- `quickstart.md` — local mirror commands + sample-PR walkthrough + branch-protection setup pointer.
- CLAUDE.md SPECKIT marker → points to this plan file.

### Phase 2 — `/speckit-tasks` (NEXT session, not this one)

Will decompose into ~25 tasks (constitution amend → script → ci.yml → fuzz-cron.yml → release.yml → .goreleaser.yml edit → AC-MATRIX + PACKAGE-MAP doc edits → sample PR validation).

## Complexity Tracking

*Empty — Constitution Check passes clean both before and after Phase 1 design.*

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| _(none)_  | _(none)_   | _(none)_                            |
