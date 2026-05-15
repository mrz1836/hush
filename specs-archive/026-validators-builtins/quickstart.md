# Quickstart — SDD-26 (`internal/supervise/validators`)

**Feature:** 026-validators-builtins
**Date:** 2026-05-13
**Spec:** [spec.md](./spec.md) · **Plan:** [plan.md](./plan.md) · **Research:** [research.md](./research.md) · **Data model:** [data-model.md](./data-model.md) · **Contracts:** [contracts/api.go](./contracts/api.go) + [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) · **Chunk doc:** [docs/sdd/SDD-26.md](../../docs/sdd/SDD-26.md)

This file is the runbook for the chunk: how to build it, what tests
to run, what each test asserts, and the exit criteria that mark the
chunk complete and shippable.

---

## 1. Prerequisites

- Working tree clean for `internal/supervise/validators/` (no
  pre-existing files; this chunk creates the package).
- `magex` available (`magex format:fix`, `magex lint`,
  `magex test:race` are the gate commands).
- Go toolchain pinned by `go.mod` floor.
- No outbound network access required to run the test suite
  (SC-004) — but tests use stdlib `net/http/httptest`, which binds
  to `127.0.0.1` on an OS-assigned port, so the loopback interface
  must be up.

---

## 2. Phase order (per `/speckit-tasks`)

The Tasks phase generates a TDD-first task list. For each behavior
B-V-N in [contracts/observable-behaviors.md](./contracts/observable-behaviors.md),
a test-writing task is generated BEFORE the implementation task
that satisfies it. The expected order:

1. **Shared tests** (B-V-IF, B-V-REG, B-V-ERR, B-V-LOG, B-V-SEC, B-V-FIX) in `validators_test.go`.
2. **Shared implementation** in `validators.go`:
   - `Validator` interface declaration
   - Three sentinel `var Err… = errors.New(…)` declarations
   - Five `<provider>Name` + five `<provider>Endpoint` constants + `anthropicVersionHeader` constant
   - Four `outcome*` constants
   - `Registry` struct + `NewRegistry` + `Get`
   - `effectiveClient` helper
   - `doRequest` + `classifyTransportError` + `isTimeout` + `emitWarnAndWrap` helpers
   - `authHeaderBuilder` function type
3. **Per-provider tests + implementation, one provider at a time** (typically in this order, but order is interchangeable since the providers are independent):
   - `anthropic_test.go` (16 tests B-V-P-Anthropic-1..16) → `anthropic.go`
   - `anthropic_oauth_test.go` (16 tests B-V-P-AnthropicOAuth-1..16) → `anthropic_oauth.go`
   - `openai_test.go` (16 tests B-V-P-OpenAI-1..16) → `openai.go`
   - `google_ai_test.go` (16 tests B-V-P-GoogleAI-1..16) → `google_ai.go`
   - `github_test.go` (16 tests B-V-P-GitHub-1..16) → `github.go`
4. **Test-only export shim** `export_test.go` (added lazily when the first per-provider sentinel-leak test needs `SetLoggerForTest`).
5. **Gate run**: `magex format:fix && magex lint && magex test:race`.
6. **Coverage verification**: `go test -cover ./internal/supervise/validators/` ≥ 90.0%.
7. **Documentation updates** (per /speckit-implement Prompt 5):
   - Append "Exported API — locked at SDD-26" entry to `docs/PACKAGE-MAP.md`.
   - Update `docs/AC-MATRIX.md` AC-10 row (FR-13 entry) with new test file paths.
   - Mark SDD-26 status `done` in `docs/SDD-PLAYBOOK.md`.
8. **Single combined commit** spanning the package + the three doc updates + the generated `tasks.md`.

---

## 3. Test commands

| Stage | Command | Purpose |
|-------|---------|---------|
| Format | `magex format:fix` | gofmt + goimports + project-specific formatting |
| Lint | `magex lint` | `golangci-lint` with project config; gates `gochecknoglobals`, `contextcheck`, `noctx`, `containedctx`, `gochecknoinits` |
| Race-clean unit | `magex test:race` | full repo under `-race` |
| Targeted unit | `go test -race ./internal/supervise/validators/` | this chunk's package only |
| Coverage | `go test -cover ./internal/supervise/validators/` | must report ≥ 90.0% |
| Coverage profile | `go test -coverprofile=/tmp/cover.out ./internal/supervise/validators/ && go tool cover -func=/tmp/cover.out` | per-function breakdown for audit |
| Sentinel-leak scan | embedded in `TestValidator_<Name>_NoLeakOnError` (× 5) | proves SC-006 |
| Live-host scan | embedded in `TestPackage_NoLiveProviderHosts` | proves SC-004 |
| Dependency scan | embedded in `TestPackage_ZeroNewDependencies` | proves FR-018 |

---

## 4. Mandatory test list (per /speckit-tasks Phase 4)

All tests are PascalCase `TestFunctionName_Scenario` per
`.github/tech-conventions/testing-standards.md`. Tests marked **(× 5)**
have one variant per provider (`<P> ∈ {Anthropic, AnthropicOAuth,
OpenAI, GoogleAI, GitHub}`).

### Shared tests (in `validators_test.go`)

1. `TestValidator_InterfaceHasOneMethod` — B-V-IF-1
2. `TestRegistry_AllFiveNamesPresent` — B-V-REG-1
3. `TestRegistry_GetUnknownName_FalseFound` — B-V-REG-2
4. `TestRegistry_ExactlyFiveNames` — B-V-REG-3 / SC-007
5. `TestRegistry_GetIsRaceClean` — B-V-REG-4
6. `TestPackage_SentinelsArePairwiseDistinct` — B-V-ERR-1
7. `TestPackage_SentinelStringsAreLiteral` — B-V-ERR-2
8. `TestPackage_LogRecordSchema_Success` — B-V-LOG-1
9. `TestPackage_LogRecordSchema_Failure` — B-V-LOG-2
10. `TestPackage_LogAttrsAreAllowList` — B-V-LOG-3
11. `TestPackage_NoStringConversionsOfSecret` — B-V-SEC-1 / SC-005
12. `TestPackage_NoRequestObjectInLogOrError` — B-V-SEC-2
13. `TestPackage_AllBuildersZeroLocalBuffer` — B-V-SEC-3
14. `TestPackage_NoLiveProviderHosts` — B-V-FIX-1 / SC-004
15. `TestPackage_ZeroNewDependencies` — B-V-FIX-2 / FR-018
16. `TestPackage_DefaultClientTimeoutIs5s` — B-V-FIX-3 / FR-012
17. `TestPackage_CallerSuppliedClientReturnedVerbatim` — B-V-FIX-3 / Clarification Q1
18. `TestExport_SetLoggerForTest_AllProvidersCovered` — data-model T-2

### Per-provider tests (× 5: in `anthropic_test.go`, `anthropic_oauth_test.go`, `openai_test.go`, `google_ai_test.go`, `github_test.go`)

For each `<P>`:

19. `TestValidator_InterfaceSatisfied_<P>` — B-V-IF-2 (compile-time guard + runtime assertion)
20. `TestValidator_<P>_HappyPath_200` — B-V-P-`<p>`-1
21. `TestValidator_<P>_StaleCredential_401` — B-V-P-`<p>`-2
22. `TestValidator_<P>_StaleCredential_403` — B-V-P-`<p>`-3
23. `TestValidator_<P>_NetworkError_5xx` — B-V-P-`<p>`-4 (table-driven over 500, 502, 503, 429)
24. `TestValidator_<P>_Timeout` — B-V-P-`<p>`-5
25. `TestValidator_<P>_NetworkError_Refused` — B-V-P-`<p>`-6
26. `TestValidator_<P>_Redirect3xx_ClassifiedAsNetwork` — B-V-P-`<p>`-7 / FR-021
27. `TestValidator_<P>_CtxCancelledBeforeSend_NoHandlerInvocation` — B-V-P-`<p>`-8 / SC-008
28. `TestValidator_<P>_CtxCancelledMidFlight` — B-V-P-`<p>`-9
29. `TestValidator_<P>_SingleRequest` — B-V-P-`<p>`-10 / FR-019
30. `TestValidator_<P>_Concurrent` — B-V-P-`<p>`-11 / FR-017
31. `TestValidator_<P>_DestroyedSecureBytes` — B-V-P-`<p>`-12 / Clarification Q6
32. `TestValidator_<P>_NoLeakOnError` — B-V-P-`<p>`-13 / SC-006
33. `TestValidator_<P>_NameIsLockedString` — B-V-P-`<p>`-14
34. `TestValidator_<P>_AuthHeaderShape` — B-V-P-`<p>`-15
35. `TestValidator_<P>_EmptyCredentialForwarded` — B-V-P-`<p>`-16

**Total tests:** 18 shared + (17 × 5 = 85) per-provider = **103 named tests**.

The 35-line list above × 5 providers = 17 distinct test names per
provider (tests 19–35 are per-provider). The test-writing tasks in
`tasks.md` ARE expected to map 1:1 against this list.

---

## 5. Manual verification recipes

### 5.1 Sentinel-leak scan (one-off audit)

```sh
# Build the test binary, run it with sentinel-leak tests verbose,
# capture stderr (where slog DEBUG output would land), grep for the
# sentinel — there must be zero matches.
go test -v -run 'TestValidator_.*_NoLeakOnError' ./internal/supervise/validators/ 2>&1 \
    | grep -c 'SECRET_SHOULD_NEVER_APPEAR_26' \
    | grep -q '^0$' \
    && echo "PASS: zero sentinel leakage" \
    || echo "FAIL: sentinel found in test output"
```

### 5.2 Live-host scan (one-off audit, alternative to TestPackage_NoLiveProviderHosts)

```sh
# Every grep match for a production hostname in test code MUST be
# inside a rewriteTransport literal or a per-test constant.
grep -nH -E '(api\.anthropic\.com|api\.openai\.com|api\.github\.com|generativelanguage\.googleapis\.com)' \
    internal/supervise/validators/*_test.go
# Visually inspect each match: it must be inside rewriteTransport{from: "..."} or
# a constant declared right above the rewriteTransport literal.
```

### 5.3 Coverage breakdown

```sh
go test -coverprofile=/tmp/cover.out ./internal/supervise/validators/
go tool cover -func=/tmp/cover.out | sort -k3 -n
# Every function MUST report ≥ 80% coverage individually; the
# package-level total MUST be ≥ 90.0% (SC-002).
# If any per-function cell is below 80%, add a targeted test.
```

### 5.4 Network-disabled integration check (SC-004 spot check)

```sh
# macOS: disable Wi-Fi + ethernet at the network panel.
# Linux: ip link set <interface> down (requires root).
# Then run the package's test suite — it MUST pass cleanly because
# every fixture is httptest.NewServer (loopback only).
go test -race -count=1 ./internal/supervise/validators/
```

---

## 6. Exit criteria for the chunk

The chunk is shippable when **every one** of the following is
green:

- [ ] `magex format:fix` produces no diff.
- [ ] `magex lint` exits 0 (no `golangci-lint` findings).
- [ ] `magex test:race` exits 0 (whole repo race-clean).
- [ ] `go test -cover ./internal/supervise/validators/` reports ≥ 90.0% (SC-002).
- [ ] All 103 named tests in §4 above pass — verified by `go test -v -run '^(TestValidator|TestRegistry|TestPackage|TestExport)' ./internal/supervise/validators/`.
- [ ] All five `TestValidator_<P>_NoLeakOnError` tests pass cleanly (SC-006).
- [ ] `TestPackage_NoLiveProviderHosts` passes (SC-004).
- [ ] `TestPackage_ZeroNewDependencies` passes (FR-018) — `go.mod` and `go.sum` unchanged.
- [ ] `docs/PACKAGE-MAP.md` has a new "Exported API — locked at SDD-26" entry for `internal/supervise/validators`.
- [ ] `docs/AC-MATRIX.md` AC-10 row's FR-13 entry references the new test file paths.
- [ ] `docs/SDD-PLAYBOOK.md` SDD-26 row status is `done`.
- [ ] `CLAUDE.md` `<!-- SPECKIT START -->` block points at this plan.
- [ ] Single combined commit subject: `feat(supervise/validators): 5 builtin credential validators (SDD-26)`.

---

## 7. Common failure modes (and recovery)

| Symptom | Likely cause | Recovery |
|---------|--------------|----------|
| `TestValidator_<P>_Timeout` fails with "no error" | Test client timeout > fixture sleep, or fixture not actually sleeping | Verify the test client's `Timeout` is shorter than the fixture's `time.Sleep`; verify the fixture writes to its `ResponseWriter` after the sleep (so the timeout fires during the read, not before the dial) |
| `TestValidator_<P>_Redirect3xx_ClassifiedAsNetwork` fails with "got nil" | The validator followed the redirect to the second fixture, which returned 200 | Verify the per-request `CheckRedirect` override in `doRequest` runs even when the caller supplies a `*http.Client` with default redirect-follow; this is R-005's exact failure mode |
| `TestValidator_<P>_NoLeakOnError` fails with "sentinel found" | A new code path leaked the credential into an `slog.Attr`, an `errors.New`, or an `fmt.Errorf` format argument | Grep the per-provider file + the shared `doRequest` for any new `slog.Attr` or `fmt.Errorf` call accepting non-literal arguments derived from `secret` or `req.Header`; remove |
| `TestPackage_ZeroNewDependencies` fails | A new direct dependency was added to `go.mod` (likely a third-party HTTP / logging / errors helper) | Remove the dependency; use the stdlib equivalent (Constitution XI) |
| Coverage below 90% on `<provider>.go` | The per-provider file has uncovered branches | Each per-provider file should be ≤ 40 LOC; the only branchable site is the constructor's nil-client check (covered by `TestPackage_DefaultClientTimeoutIs5s`). Re-run the targeted test list for that provider |
| `TestPackage_LogRecordSchema_Failure` fails with "unexpected attribute" | A new attribute was added to an `slog.LogAttrs` call (e.g. `slog.Any("error", err)`) | The schema is locked at (validator, outcome, status); remove the extra attribute or update the schema (which requires re-running /speckit-clarify) |

---

## 8. Cross-references

| Resource | Path |
|----------|------|
| Spec | [spec.md](./spec.md) |
| Plan | [plan.md](./plan.md) |
| Research | [research.md](./research.md) |
| Data model | [data-model.md](./data-model.md) |
| Contracts API | [contracts/api.go](./contracts/api.go) |
| Contracts behaviors | [contracts/observable-behaviors.md](./contracts/observable-behaviors.md) |
| Chunk doc | [docs/sdd/SDD-26.md](../../docs/sdd/SDD-26.md) |
| Constitution | [.specify/memory/constitution.md](../../.specify/memory/constitution.md) |
| Testing standards | [.github/tech-conventions/testing-standards.md](../../.github/tech-conventions/testing-standards.md) |
| Go essentials | [.github/tech-conventions/go-essentials.md](../../.github/tech-conventions/go-essentials.md) |
| Lifecycle Scenario 6 | [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md#scenario-6--validator-catches-bad-secret-before-child-start) |
| SPEC FR-13 | [docs/SPEC.md](../../docs/SPEC.md#fr-13--pluggable-credential-validators) |
| DAEMONS §5 | [docs/DAEMONS.md](../../docs/DAEMONS.md#5-authoring-credential-validators) |
| PACKAGE-MAP target | [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) `internal/supervise/` |
