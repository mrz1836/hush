# Contract — `.github/scripts/coverage-threshold/`

**Feature**: SDD-31 release gates · **Files**:
- `.github/scripts/coverage-threshold/main.go`     — entrypoint (≤40 lines: flag parsing + os.Exit codes)
- `.github/scripts/coverage-threshold/compute.go`  — pure-fn parser + threshold checker (≤120 lines, exported for testing)
- `.github/scripts/coverage-threshold/compute_test.go` — table-driven tests (Constitution IX, table-driven required)

## Invocation contract

```text
go run ./.github/scripts/coverage-threshold \
  -cover         <path-to-cover.out> \
  -min-project   <float-percent-default-90> \
  -constitution  <path-to-constitution.md>
```

Spec-locked defaults:
- `-min-project` default `90` (FR-013 — project-wide threshold).
- Security-critical packages are NOT a flag — they are a hardcoded constant inside `compute.go` (FR-016) and the self-test asserts byte-equality with the constitution's fenced block (Research R-002).

## Exit-code contract

| Code | Meaning |
|------|---------|
| `0`  | All thresholds met (project ≥ `min-project`, every security-critical pkg = 100 %). |
| `1`  | Threshold breach. Output names the offending package(s) and percentage(s). FR-013 / FR-014. |
| `2`  | `cover.out` missing, unreadable, or malformed (parse failure). FR-015. |
| `3`  | Self-test divergence — the hardcoded security-critical list differs from the constitution's fenced block. FR-016. |

Any other non-zero exit (≥ 4) is an unexpected internal error and indicates a bug in the tool itself; it should fail CI loudly.

## stdout contract

On success (`exit 0`):

```text
coverage-threshold: project 92.3% ≥ 90.0% ✓
coverage-threshold: internal/keys                100.0% = 100.0% ✓
coverage-threshold: internal/vault               100.0% = 100.0% ✓
coverage-threshold: internal/vault/securebytes   100.0% = 100.0% ✓
coverage-threshold: internal/token               100.0% = 100.0% ✓
coverage-threshold: internal/transport/sign      100.0% = 100.0% ✓
coverage-threshold: internal/transport/ecies     100.0% = 100.0% ✓
coverage-threshold: internal/audit               100.0% = 100.0% ✓
coverage-threshold: PASS (8 checks)
```

On threshold failure (`exit 1`):

```text
coverage-threshold: project 87.4% < 90.0% ✗
coverage-threshold: internal/audit  98.6% < 100.0% ✗
coverage-threshold: FAIL (2 failed checks of 8)
```

Failure lines are prefixed `::error::` so GitHub Actions surfaces them as annotations on the workflow run.

## `cover.out` parsing contract

Go's coverage file format (Go 1.x onward):

```text
mode: atomic
<file>:<startLine>.<startCol>,<endLine>.<endCol> <numStatements> <hitCount>
...
```

The tool:
1. Reads the first line; if not exactly `mode: atomic` or `mode: count` or `mode: set`, fails with exit 2 (malformed).
2. For each subsequent line, splits on whitespace into the four fields. Lines that don't match → exit 2.
3. Derives the package path from the file path by stripping the file basename and stripping the module-prefix `github.com/mrz1836/hush/`.
4. Accumulates `numStatements * (hitCount > 0 ? 1 : 0)` as covered statements per package; `numStatements` as total per package.
5. Project coverage = `sum(covered) / sum(total) * 100`.
6. Per-package coverage = `covered[pkg] / total[pkg] * 100`.
7. For each security-critical package in the hardcoded constant:
   - If the package is absent from `cover.out` (no test ran it) → exit 1 with explicit message ("package not present in coverage report — FR-015 missing-report-is-failure"). (This is distinct from a parse failure; the file IS valid but the package is missing.)
   - If per-pkg coverage < 100.0 → exit 1.

Edge cases:
- Empty `cover.out` (only the `mode:` line) → exit 2 (FR-015 — missing report ≠ pass).
- `cover.out` referencing packages outside the module (vendored deps, gen code) → silently ignored for the project-wide sum (they're not in the module).
- Floating-point rounding: comparisons use ≥/= against integer ratios computed in float64; the tool prints percentages to one decimal place but compares using the underlying ratio to avoid printing-vs-comparing skew.

## Hardcoded constant (FR-016 source of truth on the script side)

```go
// compute.go
var securityCriticalPackages = []string{
    "internal/keys",
    "internal/vault",
    "internal/vault/securebytes",
    "internal/token",
    "internal/transport/sign",
    "internal/transport/ecies",
    "internal/audit",
}
```

This constant MUST match byte-for-byte the fenced block `security-critical-packages: BEGIN/END` in `.specify/memory/constitution.md` (Research R-002). The byte-equality test re-reads the constitution at every CI run.

## Test contract (table-driven, Constitution IX)

Required test function names (chunk-doc Prompt 4 lock + plan additions):

| Test                                                    | Asserts                                                                  |
|---------------------------------------------------------|--------------------------------------------------------------------------|
| `TestCoverageThreshold_ProjectGEThreshold`              | Project ≥ 90 % passes when all sec-crit are 100 %.                       |
| `TestCoverageThreshold_SecurityCriticalEQ100`           | A sec-crit pkg at exactly 100 % is treated as pass; 99.9 % is fail.       |
| `TestCoverageThreshold_FailsBelowThreshold`             | Project at 89.9 % fails with exit 1.                                     |
| `TestCoverageThreshold_FailsOnMissingPackage`           | A sec-crit pkg absent from cover.out → exit 1 (FR-015).                  |
| `TestCoverageThreshold_FailsOnMalformedCoverOut`        | Garbage cover.out → exit 2 (FR-015).                                     |
| `TestCoverageThreshold_FailsOnEmptyCoverOut`            | cover.out with only `mode:` line → exit 2 (FR-015).                      |
| `TestSecurityCriticalListMatchesConstitution`           | Byte-equality between `securityCriticalPackages` and the fenced block (FR-016). Reads the constitution at the path supplied via test-side helper. |

Each test builds its own minimal cover.out fixture inline (no `testdata/` files needed for the parsing tests; the constitution-byte-equality test reads the real constitution from the test working directory's parent walk).

## Constitution IX compliance

- Package `main` only — no `init()`, no package-level mutable state.
- All errors wrapped with `%w`; sentinel errors `var ErrMalformedCoverOut = errors.New(...)`, `var ErrCoverageBelowThreshold = errors.New(...)`, `var ErrConstitutionMismatch = errors.New(...)`.
- Functions in `compute.go` take their inputs explicitly; nothing reads `os.Args` or `os.Getenv` outside `main.go`.
- Stdlib-only imports (`bufio`, `bytes`, `errors`, `flag`, `fmt`, `io`, `os`, `path`, `path/filepath`, `sort`, `strconv`, `strings`, `testing`). Zero `go get`.

## Out-of-contract

- Not parsing JSON cover reports (Go's text format is canonical; codecov consumes the same file).
- Not enforcing tier-2 thresholds (95 % / 85 % from constitution Test Priority table). Spec FR-012–016 limits scope to project-wide + security-critical = 100 %. A future chunk can extend.
- Not auto-discovering new security-critical packages — adding a package is a manual edit to (a) the constitution fenced block, (b) the `compute.go` constant, in the same PR (the self-test enforces sync).
