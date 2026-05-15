# Contract — `.github/workflows/fuzz-cron.yml`

**Feature**: SDD-31 release gates · **File**: `.github/workflows/fuzz-cron.yml`

## Trigger contract

```yaml
on:
  schedule:
    - cron: '0 7 * * *'   # 07:00 UTC every day  ≈ 02:00 ET / 23:00 PT
  workflow_dispatch:
    inputs:
      seconds_per_target:
        description: "Per-target fuzz budget in seconds (default 300)"
        required: false
        default: "300"
```

Rationale:
- Schedule outside business hours (FR-020 — nightly cron).
- `workflow_dispatch` enables ad-hoc runs (User Story 2 Independent Test) and lets a maintainer extend the per-target budget for a deep search without editing the file.
- A floor of 60 s per target is the constitutional minimum (Constitution VIII §2); the default 300 s overshoots that comfortably so transient flakiness doesn't bracket the floor.

## Permissions

```yaml
permissions:
  contents: read
  actions: write       # upload crashing-input artefacts (FR-022)
  issues: write        # optional: file an issue on failure — implement-phase decision
```

## Concurrency

```yaml
concurrency:
  group: fuzz-cron
  cancel-in-progress: false   # let yesterday's run finish before today's starts
```

## Job graph

```
                ┌─────────────────────────────────┐
                │  fuzz  (matrix × 6 fuzz targets) │   ← one job per target
                │  - FuzzVaultDecode               │     each running for `seconds_per_target`
                │  - FuzzJWTValidate               │
                │  - FuzzECIESDecrypt              │
                │  - FuzzVerifyRequest             │
                │  - FuzzSuperviseTOML             │
                │  - FuzzStatusJSON_Encode         │
                └─────────────────────────────────┘
```

Rationale for matrix-by-target (not all-in-one-step like ci.yml):
- A failing target should fail loudly on its own line in the workflow summary — not a single "fuzz" step whose stack-trace requires log scrolling.
- Targets run in parallel, halving wall-clock vs sequential (~10 min vs ~30 min for 300 s × 6).

## Job: `fuzz`

```yaml
jobs:
  fuzz:
    name: fuzz-${{ matrix.fuzz_target.name }}
    strategy:
      fail-fast: false
      matrix:
        fuzz_target:
          - { name: FuzzVaultDecode,       pkg: ./internal/vault            }
          - { name: FuzzJWTValidate,       pkg: ./internal/token            }
          - { name: FuzzECIESDecrypt,      pkg: ./internal/transport/ecies  }
          - { name: FuzzVerifyRequest,     pkg: ./internal/transport/sign   }
          - { name: FuzzSuperviseTOML,     pkg: ./internal/supervise/config }
          - { name: FuzzStatusJSON_Encode, pkg: ./internal/supervise        }
    runs-on: ubuntu-24.04
    env:
      CGO_ENABLED: "0"
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - name: fuzz
        run: |
          BUDGET="${{ github.event.inputs.seconds_per_target || '300' }}s"
          go test -run=^$ -fuzz=^${{ matrix.fuzz_target.name }}$ -fuzztime="$BUDGET" ${{ matrix.fuzz_target.pkg }}
      - name: preserve crash corpus  # FR-022
        if: failure()
        uses: actions/upload-artifact@v4
        with:
          name: corpus-${{ matrix.fuzz_target.name }}
          path: ${{ matrix.fuzz_target.pkg }}/testdata/fuzz/${{ matrix.fuzz_target.name }}/
          retention-days: 30
          if-no-files-found: warn
```

## Failure semantics

- Per FR-020/021: a fuzz target failing (panic, untyped error, unbounded memory growth) MUST fail its matrix leg; `fail-fast: false` means each target reports independently.
- Per FR-022: any crashing input that `go test -fuzz` writes to `testdata/fuzz/<target>/` is uploaded as a 30-day artefact named `corpus-<target>` so a maintainer can reproduce locally with `go test -run <target>/<seed>` after downloading it.
- Per FR-021: no target may be removed to fit a time budget. To extend time, use `workflow_dispatch` with a larger `seconds_per_target` input — never edit the matrix to remove a row.

## Independent Test (User Story 2)

A maintainer can validate this workflow by:
1. `gh workflow run fuzz-cron.yml -f seconds_per_target=60` (manual dispatch at the floor).
2. Verify six matrix legs run, each for ~60 s, all green.
3. Verify the workflow's summary page lists six green check-runs.

## Out-of-contract

- Not gating any PR — the per-PR smoke (ci.yml `fuzz-smoke`) is the merge gate; this workflow is supplementary depth.
- No automatic issue-filing in v1 — failure surfaces via the standard "workflow failed" email to maintainers. Implement-phase may add `actions/github-script` to open an issue if maintainers prefer; out of mandatory scope.
- No corpus auto-PR-back — preserved seed corpora are downloaded manually; auto-PR-back is a future chunk.
