# Quickstart — Release Gates

**Feature**: SDD-31 · **Branch**: `031-release-gates`

This quickstart lets a maintainer reproduce the CI gates locally and validate a sample PR. It is also the artefact AC-9 reviewers read to answer "which workflow enforces gate X?" (spec SC-008).

## 1. Run the gates locally

From the repo root, in this order:

```sh
# Format check (FR-004)
magex format:fix --check

# Lint (FR-005)
magex lint

# Race tests with coverage (FR-006 + produces cover.out for FR-011/012)
magex test:race -- -coverprofile=cover.out -covermode=atomic ./...

# Pre-commit hooks (FR-007)
go-pre-commit run --all

# Vulnerability scan (FR-008)
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck -format=json ./... > vulns.json
go run ./.github/scripts/govulncheck-filter -input vulns.json -allow .govulncheck-allow.yml

# Secret scan (FR-009)
gitleaks detect --config .gitleaks.toml --no-banner --redact

# Fuzz smoke (FR-010 — 30 s × 6 targets)
go test -run=^$ -fuzz=^FuzzVaultDecode$       -fuzztime=30s ./internal/vault
go test -run=^$ -fuzz=^FuzzJWTValidate$       -fuzztime=30s ./internal/token
go test -run=^$ -fuzz=^FuzzECIESDecrypt$      -fuzztime=30s ./internal/transport/ecies
go test -run=^$ -fuzz=^FuzzVerifyRequest$     -fuzztime=30s ./internal/transport/sign
go test -run=^$ -fuzz=^FuzzSuperviseTOML$     -fuzztime=30s ./internal/supervise/config
go test -run=^$ -fuzz=^FuzzStatusJSON_Encode$ -fuzztime=30s ./internal/supervise

# Coverage threshold (FR-012–016)
go test ./.github/scripts/coverage-threshold/...
go run  ./.github/scripts/coverage-threshold \
  -cover cover.out -min-project 90 \
  -constitution .specify/memory/constitution.md

# Constitutional invariants (FR-017/018)
test ! -d vendor
! grep -rn 'import "C"' --include='*.go' --exclude='*_test.go' .
```

If every command above exits zero, your PR will pass per-PR CI.

## 2. Run the nightly deep-fuzz locally (User Story 2)

```sh
# 5 min per target — set BUDGET to whatever you can spare
BUDGET="300s"
go test -run=^$ -fuzz=^FuzzVaultDecode$       -fuzztime=$BUDGET ./internal/vault
go test -run=^$ -fuzz=^FuzzJWTValidate$       -fuzztime=$BUDGET ./internal/token
go test -run=^$ -fuzz=^FuzzECIESDecrypt$      -fuzztime=$BUDGET ./internal/transport/ecies
go test -run=^$ -fuzz=^FuzzVerifyRequest$     -fuzztime=$BUDGET ./internal/transport/sign
go test -run=^$ -fuzz=^FuzzSuperviseTOML$     -fuzztime=$BUDGET ./internal/supervise/config
go test -run=^$ -fuzz=^FuzzStatusJSON_Encode$ -fuzztime=$BUDGET ./internal/supervise
```

If `go test -fuzz` writes anything under `testdata/fuzz/<target>/`, that seed is a new crashing input — commit it as a corpus regression test.

## 3. Validate a sample PR

```sh
# Push a draft PR
git checkout -b sandbox/sdd-31-gate-validation
git commit --allow-empty -m "ci: SDD-31 gate validation (draft)"
git push -u origin sandbox/sdd-31-gate-validation
gh pr create --draft --title "draft: SDD-31 gate validation" --body "Validating every CI gate after SDD-31 lands."
```

Then watch the PR page and confirm every required check goes green:

- `ci / gates (macos-arm64)`
- `ci / gates (linux-amd64)`
- `ci / coverage-threshold`
- `ci / coverage-upload`

If a gate fails, the failure mode is in its named step's log — never inferred.

## 4. Validate the nightly fuzz cron

Manual dispatch with the floor budget:

```sh
gh workflow run fuzz-cron.yml -f seconds_per_target=60
gh run watch
```

Confirm six matrix legs each run ~60 s, all green.

## 5. Validate a release (User Story 3)

```sh
# On a release-ready commit
git tag v0.1.0-rc1
git push origin v0.1.0-rc1
```

Wait for `release.yml` to complete, then confirm four binaries + `hush_0.1.0-rc1_checksums.txt` + `.sig` + `.pem` on the release page. Then verify locally:

```sh
curl -LO https://github.com/<org>/hush/releases/download/v0.1.0-rc1/hush_0.1.0-rc1_checksums.txt
curl -LO https://github.com/<org>/hush/releases/download/v0.1.0-rc1/hush_0.1.0-rc1_checksums.txt.sig
curl -LO https://github.com/<org>/hush/releases/download/v0.1.0-rc1/hush_0.1.0-rc1_checksums.txt.pem
cosign verify-blob \
  --certificate hush_0.1.0-rc1_checksums.txt.pem \
  --signature   hush_0.1.0-rc1_checksums.txt.sig \
  --certificate-identity-regexp '^https://github.com/<org>/hush/.github/workflows/release.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  hush_0.1.0-rc1_checksums.txt
```

If `cosign verify-blob` exits 0, the release chain of trust is intact.

## 6. Set up branch protection (operator admin task — out of this chunk's files)

On the GitHub repo settings → Branches → main branch protection rule, add as **required status checks**:

- `ci / gates (macos-arm64)`
- `ci / gates (linux-amd64)`
- `ci / coverage-threshold`
- `ci / coverage-upload`

Plus any pre-existing required checks the operator wants to keep. Branch protection is the platform-side enforcement that backs FR-028 ("no gate may be downgraded, conditionally skipped, or marked non-blocking on the default branch's protection rules").

## 7. Maintainer reference

| Question | Answer |
|---|---|
| Which workflow enforces gate X? | See `data-model.md §Entity 1 Gate` — every gate is listed with its FR-### and the workflow file/step that runs it. |
| How do I add a new fuzz target? | (a) Add the function to a Go file ending `_fuzz_test.go`. (b) Add a matrix row to BOTH `ci.yml` `fuzz-smoke` step AND `fuzz-cron.yml` `fuzz_target` matrix (FR-029). (c) Confirm the new target survives 30 s smoke + 5 min cron. |
| How do I waive a govulncheck finding? | Add an entry under `vulns:` in `.govulncheck-allow.yml` with `id`, `justification`, `expires`. Maintainer review only — PR descriptions are non-authoritative (FR-008). |
| How do I add a new security-critical package? | (a) Append the package path inside the fenced block in `.specify/memory/constitution.md`. (b) Append the same string to `securityCriticalPackages` in `compute.go`. (c) Make sure the package's tests get to 100 % statement coverage. The byte-equality self-test enforces (a) and (b) stay in sync. |
| Why is the matrix only macOS-arm64 + linux-amd64? | spec FR-001 lock — the release pipeline cross-compiles the other two combinations. |
| Where do crashing fuzz inputs go? | `testdata/fuzz/<target>/` in the relevant package. The nightly cron uploads any seed it discovers as a workflow artefact named `corpus-<target>` (30-day retention). |
