# Phase 0 — Research & Design Decisions

**Feature**: SDD-31 release gates · **Branch**: `031-release-gates` · **Date**: 2026-05-14

This file resolves every design choice that the spec leaves to the plan phase plus every conflict between spec FR-* requirements and the chunk-doc HOW. Each entry is a single decision the implement phase will execute without further interpretation.

---

## R-001 — Canonical six fuzz targets (resolves spec FR-010 vs chunk-doc divergence)

**Decision**: The six required fuzz targets — referenced by FR-010 (per-PR smoke), FR-020 (nightly cron), and FR-021 / FR-029 (no skip / no divergence) — are exactly these six Go fuzz functions, identified by **function name + owning package**:

| # | Function | Package | Source file |
|---|----------|---------|-------------|
| 1 | `FuzzVaultDecode`       | `internal/vault`             | `internal/vault/vault_fuzz_test.go` |
| 2 | `FuzzJWTValidate`       | `internal/token`             | `internal/token/validate_fuzz_test.go` |
| 3 | `FuzzECIESDecrypt`      | `internal/transport/ecies`   | `internal/transport/ecies/decrypt_fuzz_test.go` |
| 4 | `FuzzVerifyRequest`     | `internal/transport/sign`    | `internal/transport/sign/verify_fuzz_test.go` |
| 5 | `FuzzSuperviseTOML`     | `internal/supervise/config`  | `internal/supervise/config/config_fuzz_test.go` |
| 6 | `FuzzStatusJSON_Encode` | `internal/supervise`         | `internal/supervise/socket_test.go` |

**Rationale**:
- Constitution VIII §2 and `docs/TESTING-STRATEGY.md` §2 both enumerate these six (the sixth being "status socket JSON encoding (when custom parsing exists)").
- The 2026-05-14 clarification on spec.md explicitly says "**Align spec to constitution (drop server TOML, keep status-socket JSON)**" and spec FR-010 ends with the sentence "**This list is canonical per Constitution VIII / `docs/TESTING-STRATEGY.md` §2 and supersedes any divergent enumeration in the parent chunk document.**"
- The chunk-doc (`docs/sdd/SDD-31.md` Prompt 3, line 171) lists `FuzzServerTOML` instead of `FuzzStatusJSON_Encode`. This is **stale** — the chunk-doc was authored before the constitution amendment that introduced the status-socket-JSON target. Spec FR-010 wins by its own supersession clause, and Constitution VIII is the authoritative source.
- All six functions exist in the codebase as of this plan's date (verified by `grep -rE '^func Fuzz' --include='*.go'` on 2026-05-14). No new fuzz target needs to be authored by this chunk.

**Alternatives considered**:
- *Run all seven* (the six canonical plus `FuzzServerTOML`) — rejected: would add ~30 s to every PR for a target that neither the constitution nor the spec mandates. FR-021's "MUST NOT skip" rule is one-directional (don't drop required targets), not "MUST run every fuzz function in the repo".
- *Honour the chunk-doc and skip status-socket-JSON* — rejected: violates Constitution VIII §2 #6 and the spec's explicit supersession clause.

**How to apply**: the workflow files (ci.yml fuzz-smoke step, fuzz-cron.yml matrix) iterate exactly this six-element list. The list lives once, as a workflow-level `strategy.matrix.fuzz_target` array, so adding or removing a target requires a single edit (FR-029 — smoke and cron sets stay in sync).

---

## R-002 — Constitution amendment for FR-016 byte-equality self-test

**Decision**: Append a fenced enumeration block to `.specify/memory/constitution.md` under Principle VIII, exact form:

```text
<!-- security-critical-packages: BEGIN (FR-016 anchor — DO NOT EDIT without amending coverage-threshold/compute.go) -->
internal/keys
internal/vault
internal/vault/securebytes
internal/token
internal/transport/sign
internal/transport/ecies
internal/audit
<!-- security-critical-packages: END -->
```

The coverage-threshold tool reads the constitution at the path passed via `-constitution`, extracts the byte slice between the BEGIN and END markers, and compares it byte-for-byte against a hardcoded constant. Any divergence (whitespace, ordering, line endings) fails the self-test which fails CI.

**Rationale**:
- Spec FR-016 explicitly requires "byte-equality between that constant and the enumeration recorded in `.specify/memory/constitution.md`". The constitution currently lacks a machine-parseable enumeration — Principle VIII at line 221-222 mentions "vault, keys, token, transport" informally and AC-MATRIX has a structured table but is not the constitution.
- A fenced HTML-comment block survives Markdown rendering (renders as nothing on GitHub), is grep-able for the script, and is human-editable without specialised tooling.
- Constitution governance (line 467-475) requires version bumps for amendments. This change adds NO new policy — it codifies the security-critical set that FR-014 and AC-MATRIX already enumerate — so it qualifies as PATCH per the constitution's own rule "PATCH: clarifications, wording, non-semantic refinements". Version 1.1.0 → 1.1.1.

**Alternatives considered**:
- *Read the constitution Test Priority table (line 239-244)* — rejected: that table groups by priority tier, not by package path; would require fuzzy matching.
- *Read AC-MATRIX.md's Coverage targets per package table* — rejected: spec FR-016 says "constitution.md", not AC-MATRIX. AC-MATRIX is the wrong source of truth.
- *Hardcode the list and skip the self-test* — rejected: violates FR-016's MUST.
- *Add a new top-level YAML file `security-critical-packages.yml`* — rejected: FR-016 says "No other file in the repository may carry a second machine-read copy of the list."

**How to apply**: the implement phase will (a) edit constitution.md to bump version + append the fenced block + update SYNC IMPACT REPORT, (b) write the coverage-threshold tool's constant to match exactly, (c) include `TestSecurityCriticalListMatchesConstitution` in the tool's test suite to assert byte-equality on every CI run.

---

## R-003 — Nightly cron OS coverage (single-platform vs matrix)

**Decision**: Run `fuzz-cron.yml` on **linux-amd64 only** (single runner, no matrix).

**Rationale**:
- Fuzz coverage is a function of input distribution and time, not of host OS. The six fuzz targets exercise pure-Go parsers / crypto / TOML / JSON — no OS-specific code is reachable from any seed corpus.
- Doubling the cron onto macos-14 would consume ~30 min of macOS minute budget every night for zero new coverage signal.
- The per-PR smoke (FR-010) already covers both OSes — that's where OS-conditional regressions surface.

**Alternatives considered**:
- *Matrix across both OSes* — rejected per above (no coverage gain, doubled cost).
- *macOS-only on weekends, linux on weekdays* — rejected (added complexity, no signal benefit).

**How to apply**: `fuzz-cron.yml` declares a single `runs-on: ubuntu-24.04` job (no `strategy.matrix.os`). Per-target fan-out IS a matrix (one matrix axis: `fuzz_target`) so a single failing target reports cleanly.

---

## R-004 — Cosign keyless signing of the checksums manifest (FR-025)

**Decision**: Use **GoReleaser's native `signs:` block** invoking `cosign sign-blob` with keyless (Sigstore Fulcio + Rekor) auth via the GitHub-Actions OIDC token. The release workflow grants `permissions: id-token: write` so cosign picks up the OIDC identity automatically; no long-lived signing key is stored in repo or in CI secrets.

**Exact `signs:` block** (to be added to `.goreleaser.yml` at implement-phase):

```yaml
signs:
  - cmd: cosign
    signature: "${artifact}.sig"
    certificate: "${artifact}.pem"
    args:
      - sign-blob
      - "--yes"
      - "--output-signature=${signature}"
      - "--output-certificate=${certificate}"
      - "${artifact}"
    artifacts: checksum
```

The release workflow installs cosign via `sigstore/cosign-installer@v3` before invoking goreleaser.

**Rationale**:
- Spec FR-025 + the 2026-05-14 clarification lock cosign keyless. No alternative is in scope.
- `artifacts: checksum` signs the SHA256SUMS manifest (not every binary individually) which is the spec-mandated minimum and the consumer-verifiable chain of trust: verify SHA256SUMS signature, then verify binary SHA256 matches the manifest entry.
- GoReleaser's cosign integration is the documented happy path and avoids the need to author a custom signing step.

**Alternatives considered**:
- *Sign every binary individually* — rejected: spec FR-025 says "checksums manifest"; per-artefact signing inflates Rekor entries 4× without consumer benefit.
- *Cosign with stored key in CI secret* — rejected by spec clarification.
- *GPG/minisign* — rejected by spec clarification.

**How to apply**: implement phase edits `.goreleaser.yml` to add the `signs:` block above and ensures `release.yml` has `permissions: { contents: write, id-token: write }` at the workflow level.

---

## R-005 — `.golangci.json` review

**Decision**: **No edit required.** The existing `.golangci.json` already enables every linter that gates Constitution IX:

- `gochecknoglobals` + `gochecknoinits` — IX "No globals, no `init()`".
- `containedctx` + `contextcheck` + `noctx` — IX "Context propagation".
- `errcheck` + `errorlint` + `err113` + `errname` — IX "Error handling".
- `gosec` — IX security adjacent.
- `gocyclo` + `gocognit` + `nestif` — code complexity.
- `revive` + `staticcheck` + `govet` (with `shadow`) — general Go correctness.

The linter set already runs via `magex lint` which the CI workflow invokes. No new linter needs to be added to satisfy this chunk's Constitution Check.

**Rationale**: the chunk-doc Prompt 3 says ".golangci.json review (ensure linters cover the gates)" — the review verb is "ensure", not "add". The current configuration already covers Principle IX's enumerated rules. Adding more linters (e.g., `funlen`, `dupl`) would be scope-creep and would surface unrelated findings on existing code.

**Alternatives considered**:
- *Add `lll` to enforce 120-char lines* — rejected (not gated by IX; would surface false-positives on existing tables).
- *Add `dupl`* — rejected (already configured at threshold 100 in `settings.dupl` but disabled from enable list; flipping it on would break the build for non-blocking duplication).

**How to apply**: no file edit. Plan completion note records "linters reviewed, no change required".

---

## R-006 — `.github/scripts/coverage-threshold/` location & Go invocation

**Decision**: Place the tool at `.github/scripts/coverage-threshold/` (three files: `main.go` + `compute.go` + `compute_test.go`). CI invokes it via **explicit path**:

```sh
go run ./.github/scripts/coverage-threshold -cover cover.out -min-project 90 -constitution .specify/memory/constitution.md
go test ./.github/scripts/coverage-threshold/...
```

**Rationale**:
- Spec FR-016 mandates `.github/scripts/` — fixed location.
- Go's wildcard `./...` skips any directory whose name begins with `.` or `_`. Therefore `go test ./...` will NOT execute the threshold tool's test suite — CI must reference the path explicitly. This is a well-known Go behaviour, not a bug to work around.
- Using `go run ./.github/scripts/coverage-threshold` (no go-mod-init in the subdir) keeps the tool in the same module as the rest of the repo so it can `import "github.com/mrz1836/hush/..."` if ever needed (it does not today — stdlib-only).
- The three-file split keeps `main.go` thin (flag parsing + os.Exit codes) and the parseable logic in `compute.go` where tests can call it directly.

**Alternatives considered**:
- *Bash script* — rejected: parsing `cover.out` correctly handles statement counts (`name.go:line.col,line.col numStatements count`); easy to bungle in bash, easy in Go. Also Constitution IX expects Go for production tooling.
- *Python script* — rejected: introduces a Python dependency to CI, not present today.
- *Submodule with its own `go.mod`* — rejected: extra ceremony; the parent module suffices.

**How to apply**: implement phase creates the three files; ci.yml uses explicit-path invocation; `magex lint` automatically picks up the new Go files because golangci-lint walks the file tree (not the import graph).

---

## R-007 — Existing `fortress-*` workflows: leave or remove?

**Decision**: **Leave them.** Add new ci.yml / fuzz-cron.yml / release.yml alongside; do not edit or delete the legacy workflows in this chunk.

**Rationale**:
- Spec's last "Assumptions" bullet explicitly says: "any pre-existing workflow that duplicates a gate enforced by this feature is out of this feature's scope to remove, but pre-existing workflows MUST NOT relax the constitutional bar."
- Removing the legacy workflows would risk breaking unrelated CI signals (badge updates, dependabot auto-merge, scorecard reporting) that are out of scope for SDD-31.
- The three NEW workflow files own the AC-9 row in AC-MATRIX (FR-030). Branch protection on the default branch needs to add the new gates as required checks; the legacy workflow status checks can remain non-required without harm to the constitutional bar (the new gates are the floor).

**Alternatives considered**:
- *Delete every `fortress-*` workflow* — rejected (out of scope; risk of orphaning badge updates).
- *Convert `fortress-test-fuzz.yml` to point at the new fuzz-cron flow* — rejected (touches a file we don't own; defer to a future cleanup chunk).

**How to apply**: leave the `fortress-*.yml` files untouched. PR description for the implement-phase commit will note "AC-9 ownership transfers to ci.yml + fuzz-cron.yml + release.yml; fortress-*.yml retained per spec assumption".

---

## Resolution Summary

Seven decisions, zero unresolved `NEEDS CLARIFICATION` markers, zero Constitution violations. Phase 1 (data-model + contracts + quickstart) proceeds.
