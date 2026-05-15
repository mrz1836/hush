---
description: "Tasks: Pre-Flight Credential Validators (SDD-26)"
---

# Tasks: Pre-Flight Credential Validators (Interface + 5 Builtins)

**Input**: Design documents from `/specs/026-validators-builtins/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/api.go, contracts/observable-behaviors.md, quickstart.md
**Chunk doc**: [docs/sdd/SDD-26.md](../../docs/sdd/SDD-26.md)

**Tests**: TDD is **MANDATORY** per Constitution VIII and the SDD-26 Prompt-4 directive. Every test-writing task in this list MUST precede its corresponding implementation task and the test MUST be observed failing (red) before the implementation makes it pass (green). Coverage target: **≥ 90%** on `internal/supervise/validators/`.

**Organization**: Tasks are grouped by the six user stories from [spec.md](./spec.md) (US1..US6, all priority P1 except US6 P2). Within each story, tests come first.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: US1..US6 — maps to spec.md user stories. Setup/Foundational/Polish phases carry no story label.
- Every task includes an exact file path under `/Users/mrz/projects/hush/`.

## Path Conventions

- Single Go module; new package at `internal/supervise/validators/`
- Test files live alongside production code in the same package (`*_test.go`)
- Documentation lives under `docs/`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Confirm preconditions and create the package skeleton.

- [X] T001 Verify the package directory `internal/supervise/validators/` does NOT yet exist (quickstart §1 precondition) by running `test ! -d internal/supervise/validators` from repo root.
- [X] T002 Create the empty package directory at `internal/supervise/validators/` (will be populated by Foundational + per-story phases).
- [X] T003 Confirm the `magex` toolchain is on PATH and the three gate commands resolve: `magex format:fix --help`, `magex lint --help`, `magex test:race --help`.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Stand up the shared test machinery + shared production helpers + the three exported sentinels + the `Validator` interface + the `Registry`. Every user story (US1..US6) depends on this phase.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete and the shared tests are green.

### Foundational tests (write first — RED)

- [X] T004 [P] Write `TestValidator_InterfaceHasOneMethod` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — asserts `reflect.TypeOf((*Validator)(nil)).Elem().NumMethod() == 1` (B-V-IF-1, FR-001).
- [X] T005 [P] Write `TestPackage_SentinelsArePairwiseDistinct` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — asserts the three exported sentinel error values are pairwise distinct under `!=` identity comparison (B-V-ERR-1, FR-002).
- [X] T006 [P] Write `TestPackage_SentinelStringsAreLiteral` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — asserts each sentinel's `Error()` is exactly the package-prefixed literal `"validators: …"` from [data-model.md §5](./data-model.md) (B-V-ERR-2, S-2/S-3).
- [X] T007 [P] Write `TestRegistry_AllFiveNamesPresent` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — `NewRegistry(nil).Get(name)` returns `(non-nil, true)` for each of `anthropic`, `anthropic-oauth`, `openai`, `google-ai`, `github` (B-V-REG-1, FR-010, US5-AS1).
- [X] T008 [P] Write `TestRegistry_GetUnknownName_FalseFound` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — table-driven negatives: `""`, `"Anthropic"`, `"GITHUB"`, `" openai "`, `"nonsense"`, `"anthropic-oauth-extra"` all return `(nil, false)` (B-V-REG-2, FR-011, Clarification Q2).
- [X] T009 [P] Write `TestRegistry_ExactlyFiveNames` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — enumerates the registry and asserts the key set is exactly `{anthropic, anthropic-oauth, openai, google-ai, github}` (B-V-REG-3, SC-007).
- [X] T010 [P] Write `TestRegistry_GetIsRaceClean` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — 100 goroutines × 100 `Get` invocations under `-race` (B-V-REG-4, FR-016/017).
- [X] T011 [P] Write `TestPackage_DefaultClientTimeoutIs5s` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — passes `nil` to a `New<Provider>` constructor and asserts the internal client's `Timeout == 5*time.Second` via the test seam or behavior (B-V-FIX-3, FR-012, Clarification Q1).
- [X] T012 [P] Write `TestPackage_CallerSuppliedClientReturnedVerbatim` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — passes a non-nil client with `Timeout: 1*time.Second` and asserts it is honoured (no override) (B-V-FIX-3, Clarification Q1).
- [X] T013 [P] Write `TestPackage_LogRecordSchema_Success` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — captures the DEBUG record from a successful `Validate` call and asserts the attribute set is exactly `{validator, outcome=success, status}` (B-V-LOG-1, FR-020).
- [X] T014 [P] Write `TestPackage_LogRecordSchema_Failure` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — captures the WARN record from a 401 path and asserts the attribute set is exactly `{validator, outcome=stale, status}` (B-V-LOG-2, FR-020).
- [X] T015 [P] Write `TestPackage_LogAttrsAreAllowList` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — source-scan asserts no `slog.Any("error", …)`, no `slog.String("url", …)`, no `slog.Any("request"/"response"/"header", …)` anywhere in non-test code (B-V-LOG-3, H-5).
- [X] T016 [P] Write `TestPackage_NoStringConversionsOfSecret` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — source-scan of every `.go` (non-`_test.go`) file asserts zero occurrences of `string(secret`, `string(creds`, `string(credential`, and `fmt.Sprintf("%s", secret` (B-V-SEC-1, SC-005, **manual review checklist item — TestValidator_NeverMaterializesString shared behavior**).
- [X] T017 [P] Write `TestPackage_NoRequestObjectInLogOrError` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — source-scan asserts no `slog.Any("request", req)` / `fmt.Errorf("%v", req)` and similar for `*http.Request` / `http.Header` (B-V-SEC-2, FR-008).
- [X] T018 [P] Write `TestPackage_AllBuildersZeroLocalBuffer` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — AST scan of each `set<Provider>Auth` builder asserts the last non-return statement is the byte-zeroing loop (B-V-SEC-3, B-3).
- [X] T019 [P] Write `TestPackage_NoLiveProviderHosts` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — grep every `*_test.go` file for `api.anthropic.com`, `api.openai.com`, `api.github.com`, `generativelanguage.googleapis.com`; assert every match is inside a `rewriteTransport{from: ...}` literal (B-V-FIX-1, SC-004, FR-014).
- [X] T020 [P] Write `TestPackage_ZeroNewDependencies` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — reads `go.mod` direct-dep list and asserts no new third-party direct dep was added by this chunk (B-V-FIX-2, FR-018).
- [X] T021 [P] Write the shared `rewriteTransport` test fixture type in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) ([data-model.md §8](./data-model.md)) — rewrites scheme+host on `RoundTrip` from the pinned production URL to the `httptest.Server.URL`; used by every per-provider test file (F-1, F-2).
- [X] T022 [P] Write the shared sentinel constant `sentinelLeakProbe = "SECRET_SHOULD_NEVER_APPEAR_26"` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) (F-3, SC-006).

### Foundational implementation (make tests green)

- [X] T023 Create [internal/supervise/validators/validators.go](internal/supervise/validators/validators.go) with: package declaration, stdlib imports, and the `Validator` interface declaration exactly as locked in [data-model.md §1](./data-model.md) (FR-001).
- [X] T024 In [internal/supervise/validators/validators.go](internal/supervise/validators/validators.go) declare the three exported sentinel errors `ErrStaleCredential`, `ErrValidatorTimeout`, `ErrValidatorNetwork` as `var Err… = errors.New(…)` with the literal `Error()` strings from [data-model.md §5](./data-model.md) (FR-002, S-1..S-3).
- [X] T025 In [internal/supervise/validators/validators.go](internal/supervise/validators/validators.go) declare the five `<provider>Name` constants (`anthropicName`, `anthropicOAuthName`, `openaiName`, `googleAIName`, `githubName`) per [data-model.md §2](./data-model.md) (FR-010).
- [X] T026 In [internal/supervise/validators/validators.go](internal/supervise/validators/validators.go) declare the five `<provider>Endpoint` constants (`anthropicEndpoint`, `openaiEndpoint`, `googleAIEndpoint`, `githubEndpoint`) + the `anthropicVersionHeader` constant per [data-model.md §3](./data-model.md) table (R-003a..R-003e).
- [X] T027 In [internal/supervise/validators/validators.go](internal/supervise/validators/validators.go) declare the four outcome constants `outcomeSuccess`, `outcomeStale`, `outcomeTimeout`, `outcomeNetwork` per [data-model.md §6](./data-model.md) (FR-020).
- [X] T028 In [internal/supervise/validators/validators.go](internal/supervise/validators/validators.go) implement the `authHeaderBuilder` function type and the `effectiveClient(*http.Client) *http.Client` helper per [data-model.md §6](./data-model.md) (R-004, FR-012).
- [X] T029 In [internal/supervise/validators/validators.go](internal/supervise/validators/validators.go) implement the `doRequest(ctx, logger, client, name, url, extra, secret, builder) error` shared HTTP helper per [data-model.md §6](./data-model.md) pseudocode — pre-cancel fast-path (R-016), `Use(fn)`-scoped builder invocation, per-request `CheckRedirect` shallow-copy override (R-005), body drain+close, status-code switch (FR-004), and `slog` emission via `emitWarnAndWrap` (FR-020).
- [X] T030 In [internal/supervise/validators/validators.go](internal/supervise/validators/validators.go) implement `classifyTransportError`, `isTimeout`, and `emitWarnAndWrap` helpers per [data-model.md §6](./data-model.md) — single status-switch site (H-1), single WARN emission site (H-2), single DEBUG emission site (H-3).
- [X] T031 In [internal/supervise/validators/validators.go](internal/supervise/validators/validators.go) implement the `Registry` struct, `NewRegistry(httpClient *http.Client) *Registry`, and `(*Registry).Get(name string) (Validator, bool)` per [data-model.md §2](./data-model.md) (FR-011, R-1..R-5).
- [X] T032 Run `go test -race ./internal/supervise/validators/` and confirm all Foundational shared tests (T004..T022) now pass green.

**Checkpoint**: Foundation ready — Validator interface, Registry, three sentinels, shared `doRequest`, and the shared test machinery (rewriteTransport, sentinel constant) are all in place. User stories US1..US6 can now proceed in parallel (independent provider files).

---

## Phase 3: User Story 1 — Supervisor refuses to start child on rotated credential (Priority: P1) 🎯 MVP

**Goal**: Each of the five validators correctly classifies a provider-side credential rejection (HTTP 401/403) as `ErrStaleCredential`, so the supervisor can gate child start on Lifecycle Scenario 6.

**Independent Test**: Construct each validator with an `httptest.Server` fixture returning 401 (then 403); invoke `Validate` with a `*SecureBytes`-wrapped credential; observe `errors.Is(err, ErrStaleCredential) == true` for every provider.

**Note on cross-story coverage**: US1 covers the happy-path + stale-401 + stale-403 paths for every provider. Together with the per-provider `Validate` body delegation (single line: `return doRequest(...)`), implementing US1 makes the per-provider files exist; subsequent stories (US2..US6) add tests against those same files without modifying production code, OR add small additional production behaviour (none for US2..US5; US6 is test-only).

### Tests for User Story 1 (write first — RED, one per provider)

#### Anthropic

- [X] T033 [P] [US1] Write `TestValidator_InterfaceSatisfied_Anthropic` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — compile-time guard `var _ Validator = NewAnthropic(nil)` + runtime non-nil assertion (B-V-IF-2).
- [X] T034 [P] [US1] Write `TestValidator_Anthropic_HappyPath_200` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — httptest fixture returns 200; `Validate` returns nil; DEBUG log captured (B-V-P-Anthropic-1).
- [X] T035 [P] [US1] Write `TestValidator_Anthropic_StaleCredential_401` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — fixture returns 401; `errors.Is(err, ErrStaleCredential)`; not Timeout, not Network (B-V-P-Anthropic-2, US1-AS2).
- [X] T036 [P] [US1] Write `TestValidator_Anthropic_StaleCredential_403` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — fixture returns 403; `errors.Is(err, ErrStaleCredential)` (B-V-P-Anthropic-3, US1-AS3).

#### Anthropic OAuth

- [X] T037 [P] [US1] Write `TestValidator_InterfaceSatisfied_AnthropicOAuth` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-IF-2).
- [X] T038 [P] [US1] Write `TestValidator_AnthropicOAuth_HappyPath_200` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-1).
- [X] T039 [P] [US1] Write `TestValidator_AnthropicOAuth_StaleCredential_401` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-2).
- [X] T040 [P] [US1] Write `TestValidator_AnthropicOAuth_StaleCredential_403` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-3).

#### OpenAI

- [X] T041 [P] [US1] Write `TestValidator_InterfaceSatisfied_OpenAI` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-IF-2).
- [X] T042 [P] [US1] Write `TestValidator_OpenAI_HappyPath_200` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-1).
- [X] T043 [P] [US1] Write `TestValidator_OpenAI_StaleCredential_401` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-2).
- [X] T044 [P] [US1] Write `TestValidator_OpenAI_StaleCredential_403` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-3).

#### Google AI

- [X] T045 [P] [US1] Write `TestValidator_InterfaceSatisfied_GoogleAI` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-IF-2).
- [X] T046 [P] [US1] Write `TestValidator_GoogleAI_HappyPath_200` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-1).
- [X] T047 [P] [US1] Write `TestValidator_GoogleAI_StaleCredential_401` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-2).
- [X] T048 [P] [US1] Write `TestValidator_GoogleAI_StaleCredential_403` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-3).

#### GitHub

- [X] T049 [P] [US1] Write `TestValidator_InterfaceSatisfied_GitHub` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-IF-2).
- [X] T050 [P] [US1] Write `TestValidator_GitHub_HappyPath_200` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-1).
- [X] T051 [P] [US1] Write `TestValidator_GitHub_StaleCredential_401` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-2).
- [X] T052 [P] [US1] Write `TestValidator_GitHub_StaleCredential_403` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-3).

### Implementation for User Story 1 (one provider file per task, parallel)

- [X] T053 [P] [US1] Create [internal/supervise/validators/anthropic.go](internal/supervise/validators/anthropic.go) — unexported `anthropicValidator` struct + compile-time guard + `NewAnthropic(httpClient *http.Client) Validator` + `setAnthropicAuth` builder (`x-api-key: <secret>`, fresh `[]byte`, zero-loop before return) + single-line `Validate` delegating to `doRequest` with `extra = {"anthropic-version": "2023-06-01"}` per [data-model.md §3 + §4](./data-model.md) (R-003a, R-007, B-1..B-5).
- [X] T054 [P] [US1] Create [internal/supervise/validators/anthropic_oauth.go](internal/supervise/validators/anthropic_oauth.go) — `anthropicOAuthValidator` + guard + `NewAnthropicOAuth` + `setAnthropicOAuthAuth` builder (`Authorization: Bearer <secret>` + `anthropic-version: 2023-06-01`) (R-003b).
- [X] T055 [P] [US1] Create [internal/supervise/validators/openai.go](internal/supervise/validators/openai.go) — `openaiValidator` + guard + `NewOpenAI` + `setOpenAIAuth` builder (`Authorization: Bearer <secret>`, no extra headers) (R-003c).
- [X] T056 [P] [US1] Create [internal/supervise/validators/google_ai.go](internal/supervise/validators/google_ai.go) — `googleAIValidator` + guard + `NewGoogleAI` + `setGoogleAIAuth` builder (`x-goog-api-key: <secret>` — header only, NEVER `?key=` query string per R-003d).
- [X] T057 [P] [US1] Create [internal/supervise/validators/github.go](internal/supervise/validators/github.go) — `githubValidator` + guard + `NewGitHub` + `setGitHubAuth` builder (`Authorization: token <secret>` + `Accept: application/vnd.github+json`) (R-003e).
- [X] T058 [US1] Create [internal/supervise/validators/export_test.go](internal/supervise/validators/export_test.go) — `SetLoggerForTest(v Validator, logger *slog.Logger)` type-switching over the five concrete provider types per [data-model.md §7](./data-model.md); panic on default branch is test-build-only (T-1, T-2, R-014).
- [X] T059 [US1] Write `TestExport_SetLoggerForTest_AllProvidersCovered` in [internal/supervise/validators/validators_test.go](internal/supervise/validators/validators_test.go) — iterates the registry and calls `SetLoggerForTest` on each entry without panicking (T-2).
- [X] T060 [US1] Run `go test -race ./internal/supervise/validators/ -run '^(TestValidator_InterfaceSatisfied_|TestValidator_.*_HappyPath_200|TestValidator_.*_StaleCredential_)'` and confirm all 20 US1 per-provider tests + T059 pass green.

**Checkpoint**: US1 (MVP) complete — all five validators correctly distinguish "credential accepted" from "credential rejected" via the typed `ErrStaleCredential` sentinel. The supervisor can now gate child start on Lifecycle Scenario 6.

---

## Phase 4: User Story 2 — Validator distinguishes provider rejection from network failure (Priority: P1)

**Goal**: Each validator returns `ErrValidatorTimeout` on request timeout / context deadline, and `ErrValidatorNetwork` on every other transport-level failure or non-2xx-non-401/403 HTTP status (3xx, 4xx-other, 5xx, 429). Sentinels are pairwise distinct.

**Independent Test**: Point a validator at a slow `httptest.Server`; assert `errors.Is(err, ErrValidatorTimeout)`. Point another at a closed listener; assert `errors.Is(err, ErrValidatorNetwork)`. Point another at a fixture returning 500; assert `errors.Is(err, ErrValidatorNetwork)`. None of the three errors satisfies more than one sentinel.

### Tests for User Story 2 (write first — RED, one per provider)

#### Anthropic

- [X] T061 [P] [US2] Write `TestValidator_Anthropic_NetworkError_5xx` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — table-driven over `{500, 502, 503, 429}` (B-V-P-Anthropic-4, US2-AS3).
- [X] T062 [P] [US2] Write `TestValidator_Anthropic_Timeout` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — fixture sleeps longer than client timeout; `errors.Is(err, ErrValidatorTimeout)` (B-V-P-Anthropic-5, US2-AS1).
- [X] T063 [P] [US2] Write `TestValidator_Anthropic_NetworkError_Refused` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — fixture closed listener; `errors.Is(err, ErrValidatorNetwork)` (B-V-P-Anthropic-6, US2-AS2).
- [X] T064 [P] [US2] Write `TestValidator_Anthropic_Redirect3xx_ClassifiedAsNetwork` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — fixture returns 302 with `Location`; assert no follow-up + `ErrValidatorNetwork` (B-V-P-Anthropic-7, FR-021, Clarification Q9).

#### Anthropic OAuth

- [X] T065 [P] [US2] Write `TestValidator_AnthropicOAuth_NetworkError_5xx` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-4).
- [X] T066 [P] [US2] Write `TestValidator_AnthropicOAuth_Timeout` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-5).
- [X] T067 [P] [US2] Write `TestValidator_AnthropicOAuth_NetworkError_Refused` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-6).
- [X] T068 [P] [US2] Write `TestValidator_AnthropicOAuth_Redirect3xx_ClassifiedAsNetwork` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-7).

#### OpenAI

- [X] T069 [P] [US2] Write `TestValidator_OpenAI_NetworkError_5xx` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-4).
- [X] T070 [P] [US2] Write `TestValidator_OpenAI_Timeout` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-5).
- [X] T071 [P] [US2] Write `TestValidator_OpenAI_NetworkError_Refused` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-6).
- [X] T072 [P] [US2] Write `TestValidator_OpenAI_Redirect3xx_ClassifiedAsNetwork` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-7).

#### Google AI

- [X] T073 [P] [US2] Write `TestValidator_GoogleAI_NetworkError_5xx` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-4).
- [X] T074 [P] [US2] Write `TestValidator_GoogleAI_Timeout` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-5).
- [X] T075 [P] [US2] Write `TestValidator_GoogleAI_NetworkError_Refused` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-6).
- [X] T076 [P] [US2] Write `TestValidator_GoogleAI_Redirect3xx_ClassifiedAsNetwork` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-7).

#### GitHub

- [X] T077 [P] [US2] Write `TestValidator_GitHub_NetworkError_5xx` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-4).
- [X] T078 [P] [US2] Write `TestValidator_GitHub_Timeout` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-5).
- [X] T079 [P] [US2] Write `TestValidator_GitHub_NetworkError_Refused` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-6).
- [X] T080 [P] [US2] Write `TestValidator_GitHub_Redirect3xx_ClassifiedAsNetwork` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-7).

### Implementation for User Story 2

US2's classification logic lives entirely in the shared `doRequest` + `classifyTransportError` already implemented at T029/T030. No per-provider production-code changes are required. The US2 task list is test-only; running the new tests against the existing implementation MUST pass green.

- [X] T081 [US2] Run `go test -race ./internal/supervise/validators/ -run '^TestValidator_.*_(NetworkError_5xx|Timeout|NetworkError_Refused|Redirect3xx_ClassifiedAsNetwork)$'` and confirm all 20 US2 per-provider tests pass green.

**Checkpoint**: US2 complete — the three sentinels are pairwise distinct and the supervisor can act differently on the three failure modes.

---

## Phase 5: User Story 3 — Credential never leaves SecureBytes as a Go string (Priority: P1)

**Goal**: For every validator, feeding `SECRET_SHOULD_NEVER_APPEAR_26` through the 401 path produces an error chain and captured `slog` output that NEVER contains the sentinel.

**Independent Test**: Per-provider `TestValidator_<P>_NoLeakOnError` — sentinel-leak assertion against `err.Error()`, every wrapped `Error()`, and every captured `slog.Record` (DEBUG + WARN levels).

### Tests for User Story 3 (write first — RED, one per provider)

- [X] T082 [P] [US3] Write `TestValidator_Anthropic_NoLeakOnError` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — wrap `sentinelLeakProbe` in `*SecureBytes`, drive 401 via httptest, capture slog records at all levels via a test handler injected by `SetLoggerForTest`, assert `!strings.Contains(err.Error(), sentinelLeakProbe)` AND `!strings.Contains(every wrapped err's Error(), sentinelLeakProbe)` AND `!strings.Contains(every captured record's Message+Attrs, sentinelLeakProbe)` (B-V-P-Anthropic-13, SC-006, US3-AS1, US3-AS2).
- [X] T083 [P] [US3] Write `TestValidator_AnthropicOAuth_NoLeakOnError` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-13).
- [X] T084 [P] [US3] Write `TestValidator_OpenAI_NoLeakOnError` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-13).
- [X] T085 [P] [US3] Write `TestValidator_GoogleAI_NoLeakOnError` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-13).
- [X] T086 [P] [US3] Write `TestValidator_GitHub_NoLeakOnError` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-13).

### Implementation for User Story 3

US3 is enforced by code review of the existing builders (B-1..B-5 invariants in [data-model.md §4](./data-model.md)) plus the AST/source-grep tests T016, T017, T018 from the Foundational phase, plus the per-provider sentinel-leak tests above. No new production code is needed.

- [X] T087 [US3] **Manual code review checklist task: `TestValidator_NeverMaterializesString`** — open each of [internal/supervise/validators/anthropic.go](internal/supervise/validators/anthropic.go), [internal/supervise/validators/anthropic_oauth.go](internal/supervise/validators/anthropic_oauth.go), [internal/supervise/validators/openai.go](internal/supervise/validators/openai.go), [internal/supervise/validators/google_ai.go](internal/supervise/validators/google_ai.go), [internal/supervise/validators/github.go](internal/supervise/validators/github.go) and [internal/supervise/validators/validators.go](internal/supervise/validators/validators.go) and verify by eye each invariant: (a) the credential is consumed exclusively via `secret.Use(fn)`; (b) inside `Use(fn)` a fresh `[]byte` is allocated, prefix+secret copied in, `req.Header.Set` called once, then a `for i := range buf { buf[i] = 0 }` loop zeros the buffer before the callback returns; (c) the `string(buf)` conversion exists exactly once per builder (the documented R-008 exception) and the converted variable is named `buf` (NOT `secret`/`creds`/`credential`); (d) there are zero `fmt.Sprintf("%s", secret)`, zero `%v secret`, zero `%+v secret`, zero `errors.New("…" + secret …)` patterns; (e) no `*http.Request` / `*http.Header` is passed to a logger / error formatter / byte sink. Sign off in PR description.
- [X] T088 [US3] Run `go test -race ./internal/supervise/validators/ -run '^TestValidator_.*_NoLeakOnError$'` and confirm all five sentinel-leak tests pass green (SC-006).

**Checkpoint**: US3 complete — no credential leakage through error chains or log records, verified per-provider AND by source-grep/AST scans.

---

## Phase 6: User Story 4 — Validator never logs the Authorization header (Priority: P1)

**Goal**: Even when slog handlers capture every level, no captured record contains the `Authorization` header value or any byte derived from it.

**Independent Test**: Each per-provider sentinel-leak test (US3) doubles as US4's evidence (because `Authorization: Bearer <sentinel>` contains the sentinel substring). The Foundational tests T015, T017 plus the AST scan T018 add structural enforcement.

### Tests for User Story 4 (write first — RED, one per provider — Auth-header-shape contract)

- [X] T089 [P] [US4] Write `TestValidator_Anthropic_AuthHeaderShape` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — fixture handler captures the inbound `Authorization` + `x-api-key` headers; assert the test secret bytes appear verbatim in `x-api-key` (no prefix) AND the `anthropic-version: 2023-06-01` extra header is present (B-V-P-Anthropic-15, R-003a, R-007).
- [X] T090 [P] [US4] Write `TestValidator_AnthropicOAuth_AuthHeaderShape` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) — assert `Authorization: Bearer <secret>` + `anthropic-version` (B-V-P-AnthropicOAuth-15, R-003b).
- [X] T091 [P] [US4] Write `TestValidator_OpenAI_AuthHeaderShape` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) — assert `Authorization: Bearer <secret>`, no extra headers (B-V-P-OpenAI-15, R-003c).
- [X] T092 [P] [US4] Write `TestValidator_GoogleAI_AuthHeaderShape` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) — assert `x-goog-api-key: <secret>` AND assert `req.URL.RawQuery` does NOT contain `key=` (R-003d strictly header-only).
- [X] T093 [P] [US4] Write `TestValidator_GitHub_AuthHeaderShape` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) — assert `Authorization: token <secret>` + `Accept: application/vnd.github+json` (B-V-P-GitHub-15, R-003e).
- [X] T094 [P] [US4] Write `TestValidator_Anthropic_NameIsLockedString` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — assert the captured slog record's `validator` attribute equals literal `"anthropic"` (B-V-P-Anthropic-14, FR-010).
- [X] T095 [P] [US4] Write `TestValidator_AnthropicOAuth_NameIsLockedString` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-14).
- [X] T096 [P] [US4] Write `TestValidator_OpenAI_NameIsLockedString` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-14).
- [X] T097 [P] [US4] Write `TestValidator_GoogleAI_NameIsLockedString` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-14).
- [X] T098 [P] [US4] Write `TestValidator_GitHub_NameIsLockedString` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-14).

### Implementation for User Story 4

No new production code — US4 is enforced by the shared log-attribute allow-list locked at T029/T030 plus the AST/source-grep tests T015/T017.

- [X] T099 [US4] Run `go test -race ./internal/supervise/validators/ -run '^TestValidator_.*_(AuthHeaderShape|NameIsLockedString)$'` and confirm all 10 US4 tests pass green.

**Checkpoint**: US4 complete — Authorization header never appears in any logged or returned output; the `validator` attribute is locked to the FR-010 lowercase string.

---

## Phase 7: User Story 5 — Five fixed names matching SDD-18's allow-list (Priority: P1)

**Goal**: The registry exposes exactly the five FR-010 names — no more, no fewer.

**Independent Test**: Already covered by Foundational tests T007 (`TestRegistry_AllFiveNamesPresent`), T008 (`TestRegistry_GetUnknownName_FalseFound`), T009 (`TestRegistry_ExactlyFiveNames`).

### Tests for User Story 5

The three Foundational tests above already encode every US5 acceptance scenario:

| Spec acceptance | Foundational test |
|-----------------|-------------------|
| US5-AS1 — Each of five names returns non-nil + true | T007 `TestRegistry_AllFiveNamesPresent` |
| US5-AS2 — Any other name returns nil + false | T008 `TestRegistry_GetUnknownName_FalseFound` |
| US5-AS3 — Set is exactly five | T009 `TestRegistry_ExactlyFiveNames` |

### Implementation for User Story 5

No additional production code or tests — the Foundational `NewRegistry` wiring (T031) already implements US5 verbatim.

- [X] T100 [US5] Verify US5 acceptance by running `go test -race ./internal/supervise/validators/ -run '^TestRegistry_(AllFiveNamesPresent|GetUnknownName_FalseFound|ExactlyFiveNames)$'` and confirming all three pass green.

**Checkpoint**: US5 complete — the registry's name set is locked and verified.

---

## Phase 8: User Story 6 — Tests never touch live provider APIs (Priority: P2)

**Goal**: Every test file uses `httptest.Server` (or a closed listener for refused-connection tests); zero outbound network requests to real provider hosts.

**Independent Test**: Already covered by Foundational T019 (`TestPackage_NoLiveProviderHosts`). The quickstart §5.4 manual recipe — running the test suite with the network interface disabled — is the human-driven validation.

### Tests for User Story 6

- [X] T101 [US6] Verify US6 acceptance by running `go test -race ./internal/supervise/validators/ -run '^TestPackage_NoLiveProviderHosts$'` and confirming it passes green.
- [X] T102 [US6] Optional manual recipe (quickstart §5.4): disable the host's network interface and re-run `go test -race -count=1 ./internal/supervise/validators/`; confirm 100% test pass (SC-004). Skip if running in CI where network-disable is impractical.

**Checkpoint**: US6 complete — the test suite is self-contained and cannot regress to live-provider hits.

---

## Phase 9: Edge cases & remaining per-provider invariants

**Purpose**: Cover the spec's Edge Cases section (context cancellation, concurrent invocations, destroyed SecureBytes, empty credential) and the remaining per-provider observable behaviours (single-request, concurrent-safe). These do not belong to a single user story — they apply to every provider equally.

### Tests for Edge Cases (write first — RED, one per provider per case)

#### Pre-cancel fast-path (SC-008)

- [X] T103 [P] Write `TestValidator_Anthropic_CtxCancelledBeforeSend_NoHandlerInvocation` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — already-cancelled ctx; httptest handler counter remains 0; return ≤ 50 ms (B-V-P-Anthropic-8, SC-008, R-016).
- [X] T104 [P] Write `TestValidator_AnthropicOAuth_CtxCancelledBeforeSend_NoHandlerInvocation` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-8).
- [X] T105 [P] Write `TestValidator_OpenAI_CtxCancelledBeforeSend_NoHandlerInvocation` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-8).
- [X] T106 [P] Write `TestValidator_GoogleAI_CtxCancelledBeforeSend_NoHandlerInvocation` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-8).
- [X] T107 [P] Write `TestValidator_GitHub_CtxCancelledBeforeSend_NoHandlerInvocation` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-8).

#### Mid-flight cancellation

- [X] T108 [P] Write `TestValidator_Anthropic_CtxCancelledMidFlight` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) (B-V-P-Anthropic-9).
- [X] T109 [P] Write `TestValidator_AnthropicOAuth_CtxCancelledMidFlight` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-9).
- [X] T110 [P] Write `TestValidator_OpenAI_CtxCancelledMidFlight` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-9).
- [X] T111 [P] Write `TestValidator_GoogleAI_CtxCancelledMidFlight` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-9).
- [X] T112 [P] Write `TestValidator_GitHub_CtxCancelledMidFlight` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-9).

#### Single outbound request (FR-019)

- [X] T113 [P] Write `TestValidator_Anthropic_SingleRequest` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — httptest counter is 1 after Validate, regardless of response status (B-V-P-Anthropic-10, FR-019).
- [X] T114 [P] Write `TestValidator_AnthropicOAuth_SingleRequest` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-10).
- [X] T115 [P] Write `TestValidator_OpenAI_SingleRequest` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-10).
- [X] T116 [P] Write `TestValidator_GoogleAI_SingleRequest` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-10).
- [X] T117 [P] Write `TestValidator_GitHub_SingleRequest` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-10).

#### Concurrent (FR-017)

- [X] T118 [P] Write `TestValidator_Anthropic_Concurrent` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — ≥ 4 goroutines × N invocations with distinct `*SecureBytes`; race-clean; correct per-goroutine verdict (B-V-P-Anthropic-11, FR-017).
- [X] T119 [P] Write `TestValidator_AnthropicOAuth_Concurrent` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-11).
- [X] T120 [P] Write `TestValidator_OpenAI_Concurrent` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-11).
- [X] T121 [P] Write `TestValidator_GoogleAI_Concurrent` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-11).
- [X] T122 [P] Write `TestValidator_GitHub_Concurrent` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-11).

#### Destroyed SecureBytes (Clarification Q6)

- [X] T123 [P] Write `TestValidator_Anthropic_DestroyedSecureBytes` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — caller destroys `*SecureBytes` before invoking `Validate`; assert `errors.Is(err, ErrValidatorNetwork)` AND the wrapped chain preserves the SDD-02 destroyed sentinel via `errors.Is` (B-V-P-Anthropic-12, Clarification Q6).
- [X] T124 [P] Write `TestValidator_AnthropicOAuth_DestroyedSecureBytes` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-12).
- [X] T125 [P] Write `TestValidator_OpenAI_DestroyedSecureBytes` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-12).
- [X] T126 [P] Write `TestValidator_GoogleAI_DestroyedSecureBytes` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-12).
- [X] T127 [P] Write `TestValidator_GitHub_DestroyedSecureBytes` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-12).

#### Empty credential (spec Edge Cases)

- [X] T128 [P] Write `TestValidator_Anthropic_EmptyCredentialForwarded` in [internal/supervise/validators/anthropic_test.go](internal/supervise/validators/anthropic_test.go) — pass `securebytes.NewFromBytes([]byte{})`; fixture observes the empty credential header and returns 401; validator returns `ErrStaleCredential` (B-V-P-Anthropic-16, spec Edge Cases "Empty credential").
- [X] T129 [P] Write `TestValidator_AnthropicOAuth_EmptyCredentialForwarded` in [internal/supervise/validators/anthropic_oauth_test.go](internal/supervise/validators/anthropic_oauth_test.go) (B-V-P-AnthropicOAuth-16).
- [X] T130 [P] Write `TestValidator_OpenAI_EmptyCredentialForwarded` in [internal/supervise/validators/openai_test.go](internal/supervise/validators/openai_test.go) (B-V-P-OpenAI-16).
- [X] T131 [P] Write `TestValidator_GoogleAI_EmptyCredentialForwarded` in [internal/supervise/validators/google_ai_test.go](internal/supervise/validators/google_ai_test.go) (B-V-P-GoogleAI-16).
- [X] T132 [P] Write `TestValidator_GitHub_EmptyCredentialForwarded` in [internal/supervise/validators/github_test.go](internal/supervise/validators/github_test.go) (B-V-P-GitHub-16).

### Implementation for Edge cases

No production-code changes required — every Edge Case is already handled by the shared `doRequest` + `classifyTransportError` implemented in the Foundational phase. The tests above verify those code paths against each provider's concrete entrypoint.

- [X] T133 Run `go test -race ./internal/supervise/validators/` and confirm all 103 named tests (18 shared + 17 × 5 per-provider) from [quickstart.md §4](./quickstart.md) pass green. Counts: T004..T022 (Foundational shared = 19, includes T021/T022 fixtures), T033..T060 (US1 = 28), T061..T081 (US2 = 21), T082..T088 (US3 = 7), T089..T099 (US4 = 11), T100..T102 (US5+US6 = 3), T103..T132 (edge cases = 30) — totaling the locked test inventory.

**Checkpoint**: All 103 mandatory tests pass green; all spec Edge Cases verified per provider.

---

## Phase 10: Polish & Cross-Cutting Concerns (Final Phase — gates + docs + commit)

**Purpose**: Run the three mandatory gates, verify coverage ≥ 90%, run the manual audits, update the three documentation files, and create the single combined commit.

### Gates (all MUST pass clean)

- [X] T134 Run `magex format:fix` from repo root and confirm no diff (quickstart §3, §6).
- [X] T135 Run `magex lint` from repo root and confirm exit 0 — no `golangci-lint` findings (notably `gochecknoglobals`, `contextcheck`, `noctx`, `containedctx`, `gochecknoinits`) (quickstart §3, §6).
- [X] T136 Run `magex test:race` from repo root and confirm exit 0 — whole-repo race-clean (quickstart §3, §6).

### Coverage verification

- [X] T137 Run `go test -cover ./internal/supervise/validators/` and confirm coverage ≥ 90.0% (SC-002, quickstart §6). If below, add targeted tests until threshold is met.
- [X] T138 Run `go test -coverprofile=/tmp/cover.out ./internal/supervise/validators/ && go tool cover -func=/tmp/cover.out | sort -k3 -n` and confirm every function reports ≥ 80% coverage individually (quickstart §5.3).

### Manual audits

- [X] T139 Run `go test -v -run 'TestValidator_.*_NoLeakOnError' ./internal/supervise/validators/ 2>&1 | grep -c 'SECRET_SHOULD_NEVER_APPEAR_26'` and confirm output is `0` (quickstart §5.1, SC-006).
- [X] T140 Run `grep -nH -E '(api\.anthropic\.com|api\.openai\.com|api\.github\.com|generativelanguage\.googleapis\.com)' internal/supervise/validators/*_test.go` and visually inspect each match: every occurrence MUST be inside a `rewriteTransport{from: "..."}` literal or an adjacent constant — never an `http.Client.Do` target (quickstart §5.2, FR-014).

### Documentation updates

- [X] T141 Append a new entry under "Exported API — locked at SDD-26" for `internal/supervise/validators` to [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md), listing verbatim: `type Validator interface { Validate(ctx, *securebytes.SecureBytes) error }`, `type Registry struct{…}`, `func NewRegistry(*http.Client) *Registry`, `func (*Registry) Get(string) (Validator, bool)`, `func NewAnthropic / NewAnthropicOAuth / NewOpenAI / NewGoogleAI / NewGitHub(*http.Client) Validator`, and the three sentinels `ErrStaleCredential`, `ErrValidatorTimeout`, `ErrValidatorNetwork`.
- [X] T142 Update [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-10 row (FR-13 entry) with the new test file paths from `internal/supervise/validators/*_test.go`.
- [X] T143 Mark SDD-26 status `done` in [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md).

### Final commit

- [X] T144 Stage and commit all changes in a single combined commit:
  ```bash
  git add internal/supervise/validators/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/026-validators-builtins/tasks.md
  git commit -m "feat(supervise/validators): 5 builtin credential validators (SDD-26)"
  ```

### Final report

- [X] T145 Confirm and summarise: gates passed, race-clean, coverage ≥ 90%, all five validators wired, sentinel-leak tests confirm no value in error messages or logs, no live-provider hits in tests, AC-10 row updated, SDD-PLAYBOOK updated, combined commit created. Output the final status message per the SDD-26 Prompt-5 closing requirement.

---

## Dependencies & Execution Order

### Phase dependencies

- **Phase 1 (Setup, T001–T003)**: No dependencies; runs first.
- **Phase 2 (Foundational, T004–T032)**: Depends on Phase 1. **BLOCKS** all user-story phases — every per-provider file delegates to the shared `doRequest` + sentinels + `Registry` that Foundational creates.
- **Phase 3 (US1 MVP, T033–T060)**: Depends on Phase 2. Creates the five `*.go` provider files. Once T053..T057 are written, every later phase's tests just exercise those same files.
- **Phases 4–8 (US2..US6, T061–T102)**: Depend on Phase 3 (the provider files must exist). Within each phase, all test-writing tasks are independent and may run in parallel. None of US2..US6 adds new production code — they are all test-only against the existing implementation.
- **Phase 9 (Edge cases, T103–T133)**: Depends on Phase 3. Test-only.
- **Phase 10 (Polish/gates/docs/commit, T134–T145)**: Depends on Phases 1–9. Sequential by design (gates → coverage → audits → docs → commit).

### User-story dependencies

- **US1 (P1)**: Foundational only. Independently testable via T033..T052 + T060.
- **US2 (P1)**: Foundational + US1 implementation (provider files). Independently testable via T061..T081.
- **US3 (P1)**: Foundational + US1 implementation. Independently testable via T082..T088 (sentinel-leak per provider).
- **US4 (P1)**: Foundational + US1 implementation. Independently testable via T089..T099.
- **US5 (P1)**: Foundational only (Registry tests T007..T009 already encode every US5 acceptance). Independently testable via T100.
- **US6 (P2)**: Foundational only. Independently testable via T101–T102.

### Within each phase

- TDD: every test-writing task MUST precede the corresponding implementation task. For US2..US6 + Edge cases, the "implementation" is already done in the Foundational phase + US1 — the test-writing task is enough; running it confirms the contract holds.
- Models/structs (the per-provider `<provider>Validator` structs) precede the `Validate` body (which is a one-line delegate) precede any provider-specific behaviour test.
- Each phase ends with a confirmation run (`go test -race`) before moving on.

### Parallel opportunities

- **Phase 2 tests (T004–T022)**: 19 tasks all marked [P]; all live in `validators_test.go` but each task adds an independent test function. Coordinate file edits or write them sequentially in one editor session.
- **Phase 3 tests (T033–T052)**: 20 tasks, all marked [P], split across five provider `*_test.go` files (4 tests per file). All five providers can be staffed in parallel.
- **Phase 3 implementation (T053–T057)**: 5 tasks, all marked [P], each in its own provider file.
- **Phases 4–8 tests**: All marked [P]; can be added to each provider file in parallel.
- **Phase 9 tests (T103–T132)**: 30 tasks, all marked [P], 6 tests per provider file.

---

## Parallel Example: User Story 1 implementation

```bash
# After Foundational (Phase 2) is green, five developers can each
# pick up one provider file:

Task T053 [US1] — anthropic.go      # Developer A
Task T054 [US1] — anthropic_oauth.go # Developer B
Task T055 [US1] — openai.go         # Developer C
Task T056 [US1] — google_ai.go      # Developer D
Task T057 [US1] — github.go         # Developer E

# Each developer also picks up their provider's US1 + US2 + US3 + US4
# + edge-case tests. Each provider file's tests are mutually independent.
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Complete Phase 1: Setup (T001–T003).
2. Complete Phase 2: Foundational (T004–T032). **CRITICAL — blocks every story.**
3. Complete Phase 3: User Story 1 (T033–T060).
4. **STOP and VALIDATE**: confirm `go test -race ./internal/supervise/validators/ -run '^TestValidator_.*_(HappyPath_200|StaleCredential_)'` passes.
5. Demo: the supervisor can now gate child start on Lifecycle Scenario 6 (FR-13 / AC-10) — MVP shipped.

### Incremental Delivery

1. Setup + Foundational → foundation ready.
2. US1 → MVP.
3. US2 → typed errors distinguish three failure classes.
4. US3 → sentinel-leak proofs per provider.
5. US4 → Authorization-header leakage proofs.
6. US5 → registry name set verified (covered by foundational).
7. US6 → no-live-provider test verified (covered by foundational).
8. Edge cases → cancellation / concurrency / destroyed / empty.
9. Polish → gates, coverage, audits, docs, commit.

### Parallel Team Strategy

With five developers:

1. Together: complete Setup (Phase 1) + Foundational (Phase 2).
2. Then split by provider — each developer owns one provider file + all 17 of its per-provider tests across US1..edge-cases.
3. One developer (or any of the above) runs Phase 10 (Polish + commit) once all provider tests are green.

---

## Notes

- **TDD enforcement**: Every test-writing task in this list MUST be observed failing (red) BEFORE the corresponding implementation task is undertaken. For US2..US6 + Edge cases, the "implementation" is already in the Foundational shared `doRequest` + `classifyTransportError`; the test-writing task alone is sufficient — observe it pass green against the existing code.
- **103-test inventory**: The locked test list lives in [quickstart.md §4](./quickstart.md): 18 shared + 17 × 5 per-provider = 103. Tasks T004–T132 add exactly those 103 tests (the shared `rewriteTransport` fixture and `sentinelLeakProbe` const are part of the package-level test machinery).
- **No live-provider hits**: Every test uses `httptest.NewServer` + the `rewriteTransport` round-tripper to redirect from the pinned production URL to the local fixture. T019 + T140 verify this in two independent ways.
- **Sentinel constant**: `sentinelLeakProbe = "SECRET_SHOULD_NEVER_APPEAR_26"` — used by all five `TestValidator_<P>_NoLeakOnError` tests + the manual audit recipe T139.
- **Manual `TestValidator_NeverMaterializesString` task**: T087 is the in-PR code-review checklist sign-off — open each provider file and visually confirm the `Use(fn)` pattern + zero-loop + lack of string conversions of `secret`.
- **Stop-points**: After Phase 3 → MVP demo. After Phase 9 → all 103 tests green. After Phase 10 → commit ready.
- Avoid: cross-story dependencies that break independence; same-file conflicts on parallel `*_test.go` edits (assign one provider per developer to sidestep); silent failure-path code in production (`emitWarnAndWrap` is the only WARN site — keep it that way).
