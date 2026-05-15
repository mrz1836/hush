# Contract — `.github/workflows/ci.yml`

**Feature**: SDD-31 release gates · **File**: `.github/workflows/ci.yml`

## Trigger contract

```yaml
on:
  pull_request:
    branches: [main]
  push:
    branches: [main]
  workflow_dispatch:
```

Rationale:
- `pull_request` against `main` is the load-bearing entry point (FR-001 + User Story 1).
- `push` to `main` runs the same gates post-merge so the badge reflects current head.
- `workflow_dispatch` lets a maintainer re-run on demand without pushing a commit.

## Permissions

```yaml
permissions:
  contents: read
  pull-requests: read   # codecov-action reads PR metadata
  checks: write         # publishes the per-gate check-runs
  id-token: write       # codecov OIDC token (no signing happens here — release.yml owns that)
```

## Concurrency

```yaml
concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
```

Cancels stale runs when a new commit lands on the same PR ref.

## Job graph

```
                    ┌──────────────────────────┐
                    │   gates  (matrix × 2)     │   ← all eleven per-PR gates
                    │   - macos-arm64 leg       │     (one job per OS, each runs every gate)
                    │   - linux-amd64 leg       │
                    └──────────┬───────────────┘
                               │   needs:
                               ▼
            ┌──────────────────────────────────┐
            │ coverage-threshold (linux only)   │   ← downloads cover.out from linux-amd64 leg
            │   go run ./.github/scripts/...    │     and runs the threshold + self-test
            └──────────┬───────────────────────┘
                       │   needs:
                       ▼
            ┌──────────────────────────────────┐
            │ coverage-upload (linux only)      │   ← codecov/codecov-action@v4
            │                                   │     reads same cover.out artefact
            └──────────────────────────────────┘
```

Branch protection on `main` MUST require these check names:
- `ci / gates (macos-arm64)`
- `ci / gates (linux-amd64)`
- `ci / coverage-threshold`
- `ci / coverage-upload`

## Job: `gates`

```yaml
jobs:
  gates:
    name: gates (${{ matrix.os_label }})
    strategy:
      fail-fast: false
      matrix:
        include:
          - { os_label: macos-arm64, runs-on: macos-14,      goos: darwin, goarch: arm64 }
          - { os_label: linux-amd64, runs-on: ubuntu-24.04,  goos: linux,  goarch: amd64 }
    runs-on: ${{ matrix.runs-on }}
    env:
      CGO_ENABLED: "0"          # FR-019 — set at job level so every `go ...` step inherits
      GOOS: ${{ matrix.goos }}
      GOARCH: ${{ matrix.goarch }}
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 } # gitleaks needs full history
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod  # pins to 1.26.1 per FR-002
          cache: true              # implicit module + build cache
      - name: install magex
        run: go install github.com/mrz1836/magex/cmd/magex@<pinned>
      - name: install go-pre-commit
        run: go install github.com/mrz1836/go-pre-commit/cmd/go-pre-commit@<pinned>
      - name: install govulncheck
        run: go install golang.org/x/vuln/cmd/govulncheck@latest

      # ── Gate: no-vendor (FR-017) ─────────────────────────────
      - name: no-vendor
        run: test ! -d vendor || (echo "::error::/vendor directory forbidden (Principle XI)"; exit 1)

      # ── Gate: no-cgo (FR-018) ─────────────────────────────────
      - name: no-cgo
        run: |
          if grep -rn 'import "C"' --include='*.go' --exclude='*_test.go' .; then
            echo "::error::CGO import forbidden in non-test files (Principle XI)"
            exit 1
          fi

      # ── Gate: format-check (FR-004) ───────────────────────────
      - name: format-check
        run: magex format:fix --check

      # ── Gate: lint (FR-005) ───────────────────────────────────
      - name: lint
        run: magex lint

      # ── Gate: pre-commit (FR-007) ─────────────────────────────
      - name: pre-commit
        run: go-pre-commit run --all

      # ── Gate: test-race (FR-006) — produces cover.out ─────────
      - name: test-race
        run: |
          magex test:race -- -coverprofile=cover.out -covermode=atomic ./...

      # ── Gate: govulncheck (FR-008) ────────────────────────────
      - name: govulncheck
        run: |
          govulncheck -format=json ./... > vulns.json
          go run ./.github/scripts/govulncheck-filter \
            -input vulns.json \
            -allow .govulncheck-allow.yml
        # `govulncheck-filter` is a thin (≤80-line) helper colocated with coverage-threshold;
        # exits 0 when no un-waived findings remain, 1 otherwise.

      # ── Gate: gitleaks (FR-009) ───────────────────────────────
      - name: gitleaks
        uses: gitleaks/gitleaks-action@v2
        env:
          GITLEAKS_CONFIG: .gitleaks.toml

      # ── Gate: fuzz-smoke (FR-010) — six targets × 30s ─────────
      - name: fuzz-smoke
        run: |
          set -e
          go test -run=^$ -fuzz=^FuzzVaultDecode$       -fuzztime=30s ./internal/vault
          go test -run=^$ -fuzz=^FuzzJWTValidate$       -fuzztime=30s ./internal/token
          go test -run=^$ -fuzz=^FuzzECIESDecrypt$      -fuzztime=30s ./internal/transport/ecies
          go test -run=^$ -fuzz=^FuzzVerifyRequest$     -fuzztime=30s ./internal/transport/sign
          go test -run=^$ -fuzz=^FuzzSuperviseTOML$     -fuzztime=30s ./internal/supervise/config
          go test -run=^$ -fuzz=^FuzzStatusJSON_Encode$ -fuzztime=30s ./internal/supervise

      # ── Upload cover.out for downstream jobs (linux leg only is canonical) ──
      - if: matrix.os_label == 'linux-amd64'
        uses: actions/upload-artifact@v4
        with:
          name: coverage
          path: cover.out
          retention-days: 7
```

Notes:
- Pinned tool versions (`<pinned>`) resolved at implement-phase from the repo's authoritative version pins.
- The macos-arm64 leg also produces a `cover.out` but the canonical one for the threshold + upload jobs is from `linux-amd64` (avoids the tail race where the two legs disagree by 0.1 % on an integer rounding boundary). If a future regression requires per-OS coverage, the threshold gate can be matrixed in a follow-up.
- `fuzz-smoke` is one step containing six sequential invocations rather than six steps; this keeps the wall-clock contiguous and surfaces the failing target via the same step log (FR-021 — no skipping).

## Job: `coverage-threshold`

```yaml
coverage-threshold:
  name: coverage-threshold
  needs: gates
  runs-on: ubuntu-24.04
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with: { go-version-file: go.mod }
    - uses: actions/download-artifact@v4
      with: { name: coverage }
    - name: self-test
      run: go test ./.github/scripts/coverage-threshold/...
    - name: threshold
      run: |
        go run ./.github/scripts/coverage-threshold \
          -cover cover.out \
          -min-project 90 \
          -constitution .specify/memory/constitution.md
```

Exit-code contract (per Research R-006 + spec FR-013/014/015/016):
- `0` — all thresholds met.
- `1` — project < 90 % or any security-critical pkg < 100 %.
- `2` — `cover.out` missing or malformed (FR-015).
- `3` — security-critical-package list diverges between script constant and constitution fenced block (FR-016 self-test failure).

## Job: `coverage-upload`

```yaml
coverage-upload:
  name: coverage-upload
  needs: gates
  runs-on: ubuntu-24.04
  steps:
    - uses: actions/checkout@v4
    - uses: actions/download-artifact@v4
      with: { name: coverage }
    - uses: codecov/codecov-action@v4
      with:
        files: ./cover.out
        fail_ci_if_error: true   # FR-011 requires the upload to actually happen; SC-003 binds the badge
```

## Failure semantics

- Any step's non-zero exit → that gate fails → the matrix leg fails → the `gates` job fails → branch-protection blocks merge (FR-003).
- `fail-fast: false` means a failing macos-arm64 leg doesn't cancel the linux-amd64 leg (so maintainers see both signals).
- `concurrency.cancel-in-progress: true` cancels stale runs on the same PR ref — but the most recent push runs all gates fresh, so this never short-circuits a "real" run.

## Out-of-contract

- This workflow does **NOT** publish release artefacts (release.yml owns FR-023–027).
- This workflow does **NOT** run deep-fuzz (fuzz-cron.yml owns FR-020–022).
- This workflow does **NOT** edit branch-protection rules (operator admin task).
