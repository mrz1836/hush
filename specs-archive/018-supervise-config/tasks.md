---

description: "Task list for SDD-18 — internal/supervise/config (per-supervisor TOML schema + validation)"
---

# Tasks: `internal/supervise/config` — Per-Supervisor TOML Schema + Validation (SDD-18)

**Input**: Design documents from `/specs/018-supervise-config/`
**Prerequisites**: plan.md (required), spec.md (required), research.md, data-model.md, contracts/api.md, quickstart.md
**Branch**: `018-supervise-config`
**Chunk contract**: [docs/sdd/SDD-18.md](../../docs/sdd/SDD-18.md)

**Tests**: TDD-mandatory per Constitution VIII. Every behaviour-contract test is scheduled BEFORE the implementation task that satisfies it. Coverage gate is ≥95% on `internal/supervise/config/`. Fuzz target `FuzzSuperviseTOML` runs ≥60s clean (no panic, every error matches a named sentinel via `errors.Is`).

**Organization**: Tasks are grouped by user story (US1–US8 from spec.md) so each story can be implemented and validated independently. Cross-cutting edge-case validators (FR-009, FR-010, FR-012, FR-013, FR-013a) are bundled into Phase 12 since the spec lists them as Edge Cases rather than as their own stories.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, …, US8)
- File paths are absolute relative to repo root: `internal/supervise/config/<file>`

## Path Conventions

- Source: `internal/supervise/config/` (NEW package; first chunk under `internal/supervise/`)
- Tests: same directory as source (Go convention)
- Test fixtures: `internal/supervise/config/testdata/`
- Fuzz seed corpus: `internal/supervise/config/testdata/fuzz/FuzzSuperviseTOML/`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the package directory and the static testdata fixtures every later phase consumes.

- [X] T001 Create package directory `internal/supervise/config/` and the parent `internal/supervise/` (delete the existing `internal/supervise/.gitkeep` once a real file lands)
- [X] T002 [P] Create `internal/supervise/config/doc.go` with package doc-comment citing Constitution IV / V / VIII / IX / X / XI and pointing readers to `docs/CONFIG-SCHEMA.md` Supervisor section
- [X] T003 [P] Create golden fixture `internal/supervise/config/testdata/valid_minimal.toml` containing only the required supervisor fields per `docs/CONFIG-SCHEMA.md` (every optional field absent)
- [X] T004 [P] Create golden fixture `internal/supervise/config/testdata/valid_maximal.toml` containing every documented supervisor field set to its documented default value
- [X] T005 [P] Create the eight fuzz seed corpus files under `internal/supervise/config/testdata/fuzz/FuzzSuperviseTOML/` — `minimal-valid.toml`, `full-default.toml`, `malformed-bytes.toml`, `empty.toml`, `partial-table.toml`, `conflicting-types.toml`, `unknown-validator-name.toml`, `refresh-window-edge.toml` (per research.md R-011)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Declare the exported API surface (types, sentinels, defaults, function signatures) so tests in every later user-story phase compile against the locked contract. Implementations are stubs that return `errors.New("not implemented")` so the TDD red phase is real.

**⚠️ CRITICAL**: No user-story phase can begin until this phase compiles cleanly.

- [X] T006 [P] Declare the full sentinel error catalogue in `internal/supervise/config/errors.go` — exactly the 17 `var Err* = errors.New(...)` declarations from contracts/api.md §Sentinel error catalogue (`ErrTOMLDecode`, `ErrUnknownField`, `ErrMissingRequiredField`, `ErrInvalidDuration`, `ErrUnknownValidator`, `ErrGraceWindowTooLong`, `ErrGraceTTLWithoutCache`, `ErrRefreshWindowFormat`, `ErrRefreshWindowOrder`, `ErrCommandEmpty`, `ErrCommandPathRelative`, `ErrScopeEmpty`, `ErrSessionTypeInvalid`, `ErrRequestedTTLOutOfRange`, `ErrServerURLInvalid`, `ErrLogLevelInvalid`, `ErrWatchdogRateInvalid`)
- [X] T007 [P] Declare the defaults catalogue + validator allow-list in `internal/supervise/config/defaults.go` — every `Default*` and `Max*` `var` from contracts/api.md §Default constants (`DefaultRequestedTTL = 20*time.Hour`, `DefaultRefreshWindow = "09:00-10:00"`, `DefaultRefreshNudgeBefore = 30*time.Minute`, `DefaultBootRetryTimeout = 10*time.Minute`, `DefaultCacheSecretsForRestart = false`, `DefaultGraceWindow = 60*time.Minute`, `DefaultLogLevel = "info"`, `DefaultRestartOnCleanExit = true`, `DefaultRestartOnExit78 = false`, `DefaultWatchdogEnabled = true`, `DefaultWatchdogMaxAlertsPerHour = 6`, `DefaultWatchdogPatterns = []string{}`, `DefaultDMRateLimit = 5*time.Minute`, `MaxGraceWindow = 4*time.Hour`, `MaxRequestedTTL = 24*time.Hour`) plus the unexported `validatorAllowList = map[string]struct{}{...}` with the five constitutional names; each declaration carries `//nolint:gochecknoglobals` citing the sentinel-class precedent
- [X] T008 [P] Declare the public types + the internal wire-shape in `internal/supervise/config/config.go` — `type Supervisor struct`, `type Child struct`, `type DiscordRouting struct`, `type Watchdog struct`, `type Validator string`, plus the unexported `supervisorDecoded`, `childDecoded`, `discordDecoded`, `watchdogDecoded` per data-model.md §Public types and §Internal wire-shape (with the documented pointer discriminators); declare `func Load(ctx context.Context, path string) (*Supervisor, error)` returning `nil, errors.New("not implemented")` for now
- [X] T009 [P] Declare path helpers `expandHome` and `absPath` in `internal/supervise/config/paths.go` as stubs returning `s, nil` (real implementation lands in Phase 3); duplicate the SDD-06 pattern verbatim per research.md R-010
- [X] T010 Declare `func (s *Supervisor) Validate() error` returning `errors.New("not implemented")` in `internal/supervise/config/validate.go` (depends on T008 for the receiver type)
- [X] T011 Run `go build ./internal/supervise/config/` from repo root to confirm the foundational skeleton compiles cleanly (depends on T006–T010)

**Checkpoint**: Foundation ready — every user-story phase can now begin in parallel because every type, sentinel, default, and function signature is declared.

---

## Phase 3: User Story 1 — Operator starts a supervisor with a valid config (Priority: P1) 🎯 MVP

**Goal**: A minimal valid TOML loads cleanly; every absent optional field is populated from the documented default; the loaded `*Supervisor` holds no secret material; two `Load` calls on the same path return equivalent values.

**Independent Test**: Provide a TOML containing only required fields. The loader returns a populated `*Supervisor` with every optional field equal to its `Default*` constant; loading the same file twice produces `reflect.DeepEqual`-true values; struct inspection shows no secret-typed fields.

### Tests for User Story 1 ⚠️

> **NOTE: Write these tests FIRST, ensure they FAIL before implementation lands. Tests share `internal/supervise/config/config_test.go` so they are sequential within that file.**

- [X] T012 [US1] Add `TestSuperviseConfig_FullMinimal` to `internal/supervise/config/config_test.go` — loads `testdata/valid_minimal.toml`, asserts every optional field equals the corresponding `Default*` constant, asserts `*Supervisor` is non-nil and `err` is nil
- [X] T013 [US1] Add `TestSuperviseConfig_FullMaximal` to `internal/supervise/config/config_test.go` — loads `testdata/valid_maximal.toml`, asserts every field equals its documented default exactly (round-trip parity check)
- [X] T014 [US1] Add `TestLoad_Idempotent` to `internal/supervise/config/config_test.go` — loads the same fixture twice, asserts `reflect.DeepEqual` on the two `*Supervisor` values (per contracts/api.md invariant 6)
- [X] T015 [US1] Add the thirteen default-asserting tests to `internal/supervise/config/config_test.go` — `TestSuperviseConfig_DefaultRequestedTTL`, `TestSuperviseConfig_DefaultRefreshWindow`, `TestSuperviseConfig_DefaultRefreshNudgeBefore`, `TestSuperviseConfig_DefaultBootRetryTimeout`, `TestSuperviseConfig_DefaultCacheSecretsForRestart`, `TestSuperviseConfig_DefaultGraceWindow`, `TestSuperviseConfig_DefaultLogLevel`, `TestSuperviseConfig_DefaultRestartOnCleanExit`, `TestSuperviseConfig_DefaultRestartOnExit78`, `TestSuperviseConfig_DefaultWatchdogEnabled`, `TestSuperviseConfig_DefaultWatchdogMaxAlertsPerHour`, `TestSuperviseConfig_DefaultWatchdogPatterns`, `TestSuperviseConfig_DefaultDMRateLimit` — each asserts that loading a TOML with the field absent yields the documented default value (FR-016 + SC-001)
- [X] T016 [US1] Add `TestSuperviseConfig_MaxGraceWindowConstant` and `TestSuperviseConfig_MaxRequestedTTLConstant` to `internal/supervise/config/config_test.go` — asserts `MaxGraceWindow == 4*time.Hour` and `MaxRequestedTTL == 24*time.Hour` (constitutional + spec-clarification anchors)
- [X] T017 [US1] Add `TestSuperviseConfig_WatchdogSectionAbsent_AppliesAllDefaults` to `internal/supervise/config/config_test.go` — covers Clarification 4: omitting the entire `[watchdog]` section yields `Enabled=true`, `Patterns=[]string{}` (non-nil), `MaxAlertsPerHour=6` per research.md R-008
- [X] T018 [US1] Add `TestSuperviseConfig_PathFieldsAreExpandedAndAbsolute` to `internal/supervise/config/config_test.go` — asserts `StatusSocket` and `PIDFile` are `~`-expanded and absolute on the loaded struct (per data-model.md `Supervisor` table)

### Implementation for User Story 1

- [X] T019 [US1] Implement the path helpers `expandHome` and `absPath` in `internal/supervise/config/paths.go` per research.md R-010 — `os.UserHomeDir` for `~` expansion + `filepath.Abs`; no symlink resolution, no existence checks
- [X] T020 [US1] Implement the `pelletier/go-toml/v2` decoder pipeline in `internal/supervise/config/config.go` — `os.Open` → `defer f.Close()` → `toml.NewDecoder(f).DisallowUnknownFields(true).Decode(&decoded)` → return `(nil, errors.New("not implemented"))` on success for now (sets up file open + close discipline; full materializer follows in T021); inspect `ctx.Err()` once at function entry
- [X] T021 [US1] Implement the materializer that turns `supervisorDecoded` into `*Supervisor` in `internal/supervise/config/config.go` — apply every documented default for every absent optional field per FR-016, expand `~` paths via T019 helpers, materialise `Validators map[string]string` → `map[string]Validator` (no allow-list check yet — that lands in US3); wire `Load` to call decode → required-field gate → materialiser → return
- [X] T022 [US1] Implement the required-field gate in `internal/supervise/config/validate.go` — check every required field per data-model.md §Public types tables, aggregate misses via `errors.Join`, wrap with `ErrMissingRequiredField` carrying the dotted TOML path (e.g., `child.command`)
- [X] T023 [US1] Verify Phase 3 tests pass — run `go test ./internal/supervise/config/ -run 'TestSuperviseConfig_(FullMinimal|FullMaximal|Default|MaxGraceWindowConstant|MaxRequestedTTLConstant|WatchdogSectionAbsent|PathFieldsAreExpandedAndAbsolute)|TestLoad_Idempotent'` and confirm green

**Checkpoint**: User Story 1 functional — happy path + every documented default asserted. This is the MVP.

---

## Phase 4: User Story 2 — Operator catches a typo before it reaches production (Priority: P1)

**Goal**: An unknown / misspelled key in any documented section produces `ErrUnknownField`. A type-mismatch produces `ErrTOMLDecode`. A missing required field produces `ErrMissingRequiredField`.

**Independent Test**: Author a TOML file that adds an extra key (or misspells a known one). `Load` returns `errors.Is(err, ErrUnknownField) == true`. A TOML with a string-where-int-expected returns `errors.Is(err, ErrTOMLDecode) == true`.

### Tests for User Story 2 ⚠️

- [X] T024 [US2] Add `TestSuperviseConfig_RejectsUnknownField` to `internal/supervise/config/config_test.go` — table-driven with rows for each section (root, `[child]`, `[discord]`, `[validators]`, `[watchdog]`); each row injects an unknown key into a copy of `valid_minimal.toml` and asserts `errors.Is(err, ErrUnknownField)`
- [X] T025 [US2] Add `TestSuperviseConfig_RejectsTypeMismatch` to `internal/supervise/config/config_test.go` — supplies a TOML with `requested_ttl = 42` (int instead of duration string) and asserts `errors.Is(err, ErrTOMLDecode)`
- [X] T026 [US2] Add `TestSuperviseConfig_RejectsMissingRequiredField` to `internal/supervise/config/validate_test.go` — table-driven with one row per required field, each row removes a different required field from `valid_minimal.toml`, asserts `errors.Is(err, ErrMissingRequiredField)` and that the error message contains the dotted TOML path
- [X] T027 [US2] Add `TestSuperviseConfig_MultipleMissingFields_AllSurfaced` to `internal/supervise/config/validate_test.go` — removes two required fields from `valid_minimal.toml`, asserts both individual `errors.Is` matches succeed via `errors.Join` traversal

### Implementation for User Story 2

- [X] T028 [US2] Translate pelletier/v2 strict-mode errors into the typed sentinels in `internal/supervise/config/config.go` — wrap `*toml.StrictMissingError` as `ErrUnknownField`, wrap any other decoder error as `ErrTOMLDecode`, both with `%w` so `errors.Is` works (per research.md R-001)
- [X] T029 [US2] Verify Phase 4 tests pass — run `go test ./internal/supervise/config/ -run 'TestSuperviseConfig_(RejectsUnknownField|RejectsTypeMismatch|RejectsMissingRequiredField|MultipleMissingFields)'` and confirm green

**Checkpoint**: User Story 2 functional — typos and type mismatches and missing-required fields are surfaced as named sentinels at load time.

---

## Phase 5: User Story 3 — Operator cannot declare an unsupported validator (Priority: P1)

**Goal**: `[validators]` map values must be one of `{anthropic, anthropic-oauth, openai, google-ai, github}`. Any other value produces `ErrUnknownValidator` carrying the validator NAME (RHS) only — never the LHS secret name.

**Independent Test**: A TOML mapping a secret name to `"slack"` returns `errors.Is(err, ErrUnknownValidator) == true` and the error string contains `"slack"` but does NOT contain the LHS bytes.

### Tests for User Story 3 ⚠️

- [X] T030 [US3] Add `TestSuperviseConfig_RejectsUnknownValidator` to `internal/supervise/config/validate_test.go` — table-driven rows: `"slack"`, `"anthropc"` (typo), `""`, `"ANTHROPIC"` (case wrong); each asserts `errors.Is(err, ErrUnknownValidator)`
- [X] T031 [US3] Add `TestSuperviseConfig_AcceptsAllAllowListedValidators` to `internal/supervise/config/validate_test.go` — five rows, one per allow-listed name, each maps a high-entropy LHS to that name and asserts `Load` returns `nil` error
- [X] T032 [US3] Add `TestErrUnknownValidator_DoesNotIncludeSecretMaterial` to `internal/supervise/config/validate_test.go` — constructs `[validators]\nHIGH_ENTROPY_LHS_xyz789ABC = "slack"`, asserts the error string contains `"slack"` but does NOT contain the substring `"HIGH_ENTROPY_LHS_xyz789ABC"` (FR-014 + FR-020 + Constitution X)
- [X] T033 [US3] Add `TestSuperviseConfig_LoadedConfigContainsOnlyAllowListedValidators` to `internal/supervise/config/config_test.go` — for the maximal fixture, iterates `s.Validators`, asserts each value is in the five-element allow-list (SC-005)

### Implementation for User Story 3

- [X] T034 [US3] Implement the validator allow-list check inside the materialiser in `internal/supervise/config/config.go` — iterate every `[validators]` entry, look up RHS in `validatorAllowList`, on miss return `fmt.Errorf("hush/supervise/config: unknown validator %q: %w", name, ErrUnknownValidator)` per research.md R-002 (the `%q` produces ONLY the validator name; LHS is never reproduced)
- [X] T035 [US3] Verify Phase 5 tests pass — run `go test ./internal/supervise/config/ -run 'TestSuperviseConfig_(RejectsUnknownValidator|AcceptsAllAllowListedValidators|LoadedConfigContainsOnlyAllowListedValidators)|TestErrUnknownValidator'` and confirm green

**Checkpoint**: User Story 3 functional — validator allow-list enforced; downstream consumers cannot observe an out-of-list validator on a successfully loaded `*Supervisor`.

---

## Phase 6: User Story 4 — Operator cannot extend the daily grace window beyond the cap (Priority: P1)

**Goal**: `cache_grace_ttl > 4h` produces `ErrGraceWindowTooLong`. Exactly `4h` is accepted. Absence applies `DefaultGraceWindow = 60m`. The contradiction `cache_secrets_for_restart=false` AND `cache_grace_ttl` explicitly set produces `ErrGraceTTLWithoutCache`.

**Independent Test**: A TOML with `cache_grace_ttl = "5h"` returns `ErrGraceWindowTooLong`. With `"4h"` it loads. With `cache_secrets_for_restart = false` AND `cache_grace_ttl = "1h"` it returns `ErrGraceTTLWithoutCache`.

### Tests for User Story 4 ⚠️

- [X] T036 [US4] Add `TestSuperviseConfig_GraceWindowOver4h_Rejected` to `internal/supervise/config/validate_test.go` — table rows: `"5h"`, `"12h"`, `"4h1m"`, `"24h"`; all assert `errors.Is(err, ErrGraceWindowTooLong)`
- [X] T037 [US4] Add `TestSuperviseConfig_GraceWindowExactly4h_Accepted` to `internal/supervise/config/validate_test.go` — `cache_grace_ttl = "4h"` with cache enabled loads cleanly
- [X] T038 [US4] Add `TestSuperviseConfig_GraceTTLWithoutCache_Rejected` to `internal/supervise/config/validate_test.go` — three rows: cache flag absent + ttl set, cache flag false + ttl set, cache flag false + ttl explicitly zero; all assert `errors.Is(err, ErrGraceTTLWithoutCache)` per research.md R-005 + Clarification 3
- [X] T039 [US4] Add `TestSuperviseConfig_GraceTTL_AbsentWithCacheTrue_AppliesDefault` to `internal/supervise/config/validate_test.go` — `cache_secrets_for_restart = true` with `cache_grace_ttl` absent loads cleanly with `s.CacheGraceTTL == DefaultGraceWindow` (60m)

### Implementation for User Story 4

- [X] T040 [US4] Implement the grace-cache validator in `internal/supervise/config/validate.go` — contradiction-guard runs FIRST (return `ErrGraceTTLWithoutCache` if cache flag is false-or-absent AND `*string CacheGraceTTL` pointer is non-nil), then cap-enforcement (`> MaxGraceWindow → ErrGraceWindowTooLong`), then default application; per research.md R-005 the order matters because the contradiction is the more useful error message
- [X] T041 [US4] Verify Phase 6 tests pass — run `go test ./internal/supervise/config/ -run 'TestSuperviseConfig_GraceWindow|TestSuperviseConfig_GraceTTL'` and confirm green

**Checkpoint**: User Story 4 functional — Constitution IV TTL discipline + Layer-6 audit boundary enforced at load time.

---

## Phase 7: User Story 5 — Operator cannot misformat or invert the refresh window (Priority: P1)

**Goal**: `refresh_window` matches `HH:MM-HH:MM` (24-hour, leading zeros, single dash). Format violations produce `ErrRefreshWindowFormat`; format-clean but `start >= end` (incl. wrap-around) produces a distinct `ErrRefreshWindowOrder`.

**Independent Test**: `"9-10"` returns `ErrRefreshWindowFormat`. `"10:00-09:00"` returns `ErrRefreshWindowOrder`. `"09:00-10:00"` loads.

### Tests for User Story 5 ⚠️

- [X] T042 [US5] Add `TestSuperviseConfig_RefreshWindowFormat` to `internal/supervise/config/validate_test.go` — table rows of format violations: `""`, `"9-10"`, `"09:00 to 10:00"`, `"09:00-10"`, `"09:00-25:00"`, `"99:99-99:99"`, `"09:00-10:00-bad"`, `"09:00-10:00 "` (trailing whitespace), `"9:00-10:00"` (no leading zero); each asserts `errors.Is(err, ErrRefreshWindowFormat)` (per research.md R-003)
- [X] T043 [US5] Add `TestSuperviseConfig_RefreshWindowStartGEEnd_Rejected` to `internal/supervise/config/validate_test.go` — table rows: `"10:00-09:00"`, `"09:00-09:00"`, `"23:59-00:01"` (wrap-around); each asserts `errors.Is(err, ErrRefreshWindowOrder)` AND NOT `errors.Is(err, ErrRefreshWindowFormat)` (the two sentinels must be separately matchable)
- [X] T044 [US5] Add `TestSuperviseConfig_RefreshWindowAccepts_InOrder` to `internal/supervise/config/validate_test.go` — rows: `"09:00-10:00"`, `"00:00-23:59"`, `"08:30-08:31"` (one-minute window); all load cleanly

### Implementation for User Story 5

- [X] T045 [US5] Implement the refresh-window parser in `internal/supervise/config/validate.go` — split on the single `-` separator (reject `strings.Index != strings.LastIndex` for the second-dash guard), parse each side via `time.Parse("15:04", side)`, return `ErrRefreshWindowFormat` on any parse failure; format-clean values then compare via `!startT.Before(endT)` → `ErrRefreshWindowOrder` (per research.md R-003)
- [X] T046 [US5] Verify Phase 7 tests pass — run `go test ./internal/supervise/config/ -run 'TestSuperviseConfig_RefreshWindow'` and confirm green

**Checkpoint**: User Story 5 functional — refresh-window's two distinct rejection categories surface as two distinct, separately-matchable sentinels.

---

## Phase 8: User Story 6 — Operator cannot smuggle a child command through a shell (Priority: P1)

**Goal**: `[child].command` is a non-empty `[]string`; `command[0]` passes `filepath.IsAbs`. Empty vector → `ErrCommandEmpty`. Relative first element → `ErrCommandPathRelative`. Subsequent elements pass through verbatim.

**Independent Test**: `command = []` returns `ErrCommandEmpty`. `command = ["my-daemon", "start"]` returns `ErrCommandPathRelative`. `command = ["/usr/local/bin/d", "start"]` loads, and the loaded `s.Child.Command` equals the input verbatim.

### Tests for User Story 6 ⚠️

- [X] T047 [US6] Add `TestSuperviseConfig_CommandFirstElementMustBeAbsolute` to `internal/supervise/config/validate_test.go` — table rows: `["my-daemon"]`, `["./run.sh"]`, `["bin/daemon"]`, `["../etc/daemon"]`; all assert `errors.Is(err, ErrCommandPathRelative)`
- [X] T048 [US6] Add `TestSuperviseConfig_CommandEmpty_Rejected` to `internal/supervise/config/validate_test.go` — `command = []` asserts `errors.Is(err, ErrCommandEmpty)`; absent `command` field asserts `errors.Is(err, ErrMissingRequiredField)` (the two are distinct)
- [X] T049 [US6] Add `TestSuperviseConfig_CommandAcceptsAbsoluteWithArgs` to `internal/supervise/config/validate_test.go` — `command = ["/usr/local/bin/your-daemon", "start", "--flag", ""]` loads cleanly and `s.Child.Command` equals the input verbatim (no quoting, no splitting, empty-string elements preserved per spec Edge Cases)

### Implementation for User Story 6

- [X] T050 [US6] Implement the child-command validator in `internal/supervise/config/validate.go` — `len(cmd) == 0 → ErrCommandEmpty`, then `!filepath.IsAbs(cmd[0]) → ErrCommandPathRelative`; subsequent elements untouched (per research.md R-004)
- [X] T051 [US6] Verify Phase 8 tests pass — run `go test ./internal/supervise/config/ -run 'TestSuperviseConfig_Command'` and confirm green

**Checkpoint**: User Story 6 functional — `PATH`-hijack surface removed at load time.

---

## Phase 9: User Story 7 — Operator cannot start a supervisor with no scoped secrets (Priority: P2)

**Goal**: Top-level `scope` non-empty (absence and emptiness equivalent) → else `ErrScopeEmpty`.

**Independent Test**: `scope = []` and absent-`scope` both return `ErrScopeEmpty`. `scope = ["A"]` loads.

### Tests for User Story 7 ⚠️

- [X] T052 [US7] Add `TestSuperviseConfig_ScopeEmpty_Rejected` to `internal/supervise/config/validate_test.go` — `scope = []` asserts `errors.Is(err, ErrScopeEmpty)`
- [X] T053 [US7] Add `TestSuperviseConfig_ScopeAbsent_RejectedSameSentinel` to `internal/supervise/config/validate_test.go` — `scope` field absent asserts `errors.Is(err, ErrScopeEmpty)` (FR-008: absence and emptiness equivalent)
- [X] T054 [US7] Add `TestSuperviseConfig_ScopeAccepts_NonEmpty` to `internal/supervise/config/validate_test.go` — `scope = ["ANTHROPIC_API_KEY"]` loads cleanly

### Implementation for User Story 7

- [X] T055 [US7] Implement the scope-non-empty validator in `internal/supervise/config/validate.go` — runs alongside the required-field gate but produces `ErrScopeEmpty` (not `ErrMissingRequiredField`) when the field is absent OR an empty array (FR-008 explicitly equates the two)
- [X] T056 [US7] Verify Phase 9 tests pass — run `go test ./internal/supervise/config/ -run 'TestSuperviseConfig_Scope'` and confirm green

**Checkpoint**: User Story 7 functional — supervisors without scoped secrets cannot start.

---

## Phase 10: User Story 8 — Operator cannot smuggle secrets through the supervisor config (Priority: P1)

**Goal**: The `Supervisor` struct holds zero secret-typed fields (FR-014). The loader reads zero environment variables for any supervisor field — `os.UserHomeDir` for `~` expansion is the only env-touching call (FR-015 spirit per Constitution X). The package has no `init()` and spawns no goroutines.

**Independent Test**: Set `HUSH_DISCORD_TOKEN`, `HUSH_VAULT_PASSPHRASE`, `HUSH_REASON`, `HUSH_REFRESH_WINDOW` and load a fixture; loaded values equal the fixture's literals, not the env. Struct shape inspection finds zero `[]byte` / `*SecureBytes` fields.

### Tests for User Story 8 ⚠️

- [X] T057 [US8] Add `TestLoad_DoesNotReadSecretsFromEnv` to `internal/supervise/config/config_test.go` — uses `t.Setenv` to set `HUSH_DISCORD_TOKEN`, `HUSH_VAULT_PASSPHRASE`, `HUSH_REASON`, `HUSH_REFRESH_WINDOW` to plausible values, loads `valid_maximal.toml`, asserts loaded fields equal the fixture's literal values (not the env values)
- [X] T058 [US8] Add `TestSchema_HasNoSecretFields` to `internal/supervise/config/config_test.go` — uses `reflect` to walk the `Supervisor` type, asserts no field has type `[]byte`, no field name contains `Token` / `Password` / `Passphrase` / `Secret` (string-shape inspection guard against future drift; FR-014)
- [X] T059 [US8] Add `TestPackage_NoInit` to `internal/supervise/config/config_test.go` — uses `runtime` or a build-tag grep to confirm the package has no `init()` function (Constitution IX guard)
- [X] T060 [US8] Add `TestLoad_NoGoroutineLeak` to `internal/supervise/config/config_test.go` — `runtime.NumGoroutine` diff before / after a `Load` call asserts zero new goroutines (Constitution IX guard)
- [X] T061 [US8] Add `TestSuperviseConfig_HomeExpansionIsTheOnlyEnvCall` to `internal/supervise/config/config_test.go` — sets `HOME` to a sentinel temp dir, asserts the loaded `StatusSocket` / `PIDFile` paths are expanded relative to that sentinel; covers `os.UserHomeDir` as the sole permitted env-reader

### Implementation for User Story 8

- [X] T062 [US8] Audit `internal/supervise/config/` for `os.Getenv` / `os.LookupEnv` calls — there must be zero. The only permitted env-touching call is `os.UserHomeDir` invoked from `paths.go`. Add a code-comment to that call citing FR-015 + Constitution X.
- [X] T063 [US8] Verify Phase 10 tests pass — run `go test ./internal/supervise/config/ -run 'TestLoad_DoesNotReadSecretsFromEnv|TestSchema_HasNoSecretFields|TestPackage_NoInit|TestLoad_NoGoroutineLeak|TestSuperviseConfig_HomeExpansionIsTheOnlyEnvCall'` and confirm green

**Checkpoint**: User Story 8 functional — the supervisor config carries zero secret material; the loader is environment-independent; Constitution IX + X invariants asserted by self-tests.

---

## Phase 11: Cross-Cutting Edge-Case Validators (Spec Edge Cases)

**Purpose**: Implement and test the FR-level rejection categories that the spec lists under §Edge Cases rather than under explicit user stories. These complete the spec's FR-017 ("typed sentinel for every documented rejection category") coverage.

> **NOTE: These tests share `internal/supervise/config/validate_test.go` so they are sequential within that file. No story label because the spec attaches these rules to Edge Cases, not to user stories.**

### Tests for Cross-Cutting Validators ⚠️

- [X] T064 Add `TestSuperviseConfig_SessionTypeInvalid_Rejected` to `internal/supervise/config/validate_test.go` — table rows: `"interactive"`, `"daemon"`, `""`, `"SUPERVISOR"` (case wrong); all assert `errors.Is(err, ErrSessionTypeInvalid)` (FR-009)
- [X] T065 Add `TestSuperviseConfig_RequestedTTLOver24h_Rejected` to `internal/supervise/config/validate_test.go` — table rows: `"25h"`, `"48h"`, `"24h1m"`; all assert `errors.Is(err, ErrRequestedTTLOutOfRange)`. Also assert `"24h"` exactly is accepted. (FR-010 + Clarification 1)
- [X] T066 Add `TestSuperviseConfig_LogLevelInvalid_Rejected` to `internal/supervise/config/validate_test.go` — table rows: `"trace"`, `"verbose"`, `"INFO"` (case wrong), `""` (when explicitly set vs absent); all assert `errors.Is(err, ErrLogLevelInvalid)` (FR-013)
- [X] T067 Add `TestSuperviseConfig_ServerURLInvalid_Rejected` to `internal/supervise/config/validate_test.go` — four rows mapping to the four documented rejection sub-categories (empty, parse-error, empty host, scheme not http/https): `""`, `"https//bad"`, `"http://"`, `"ftp://1.2.3.4:7743"`; all assert `errors.Is(err, ErrServerURLInvalid)` (FR-013a + Clarification 5)
- [X] T068 Add `TestSuperviseConfig_WatchdogRateInvalid_Rejected` to `internal/supervise/config/validate_test.go` — rows: `0`, `-1`, `-100`; all assert `errors.Is(err, ErrWatchdogRateInvalid)` (FR-012)
- [X] T069 Add `TestSuperviseConfig_RejectsInvalidDuration` to `internal/supervise/config/validate_test.go` — rows targeting each duration field (`requested_ttl`, `refresh_nudge_before`, `boot_retry_timeout`, `cache_grace_ttl`) with garbage like `"not-a-duration"`; all assert `errors.Is(err, ErrInvalidDuration)`

### Implementation for Cross-Cutting Validators

- [X] T070 Implement `session_type == "supervisor"` check in `internal/supervise/config/validate.go` — return `ErrSessionTypeInvalid` on any other value (FR-009)
- [X] T071 Implement `requested_ttl ≤ MaxRequestedTTL` check in `internal/supervise/config/validate.go` — parse via shared `parseDuration` helper (research.md R-012), return `ErrRequestedTTLOutOfRange` on `> 24h` (FR-010)
- [X] T072 Implement `log_level ∈ {debug, info, warn, error}` allow-list check in `internal/supervise/config/validate.go` — return `ErrLogLevelInvalid` otherwise (FR-013)
- [X] T073 Implement `server_url` syntactic gate in `internal/supervise/config/validate.go` — `url.Parse` + non-empty `Host` + scheme via `strings.EqualFold` against `http`/`https`; return `ErrServerURLInvalid` on any sub-violation (FR-013a + research.md R-007)
- [X] T074 Implement `watchdog.max_alerts_per_hour > 0` check in `internal/supervise/config/validate.go` — return `ErrWatchdogRateInvalid` on `≤ 0` (FR-012)
- [X] T075 Implement the shared `parseDuration(raw, def, fieldName)` helper in `internal/supervise/config/validate.go` per research.md R-012 — empty string → `def`; otherwise `time.ParseDuration(raw)`; on parse error wrap with `ErrInvalidDuration`
- [X] T076 Verify Phase 11 tests pass — run `go test ./internal/supervise/config/ -run 'TestSuperviseConfig_(SessionTypeInvalid|RequestedTTLOver24h|LogLevelInvalid|ServerURLInvalid|WatchdogRateInvalid|RejectsInvalidDuration)'` and confirm green

**Checkpoint**: Every spec FR-017 rejection category surfaces as a typed sentinel.

---

## Phase 12: Fuzz Target — `FuzzSuperviseTOML`

**Purpose**: Constitution VIII fuzz target #5 — random byte streams into `Load` must produce no panic, no unbounded allocation, and only typed-sentinel errors.

### Test for Fuzz Target ⚠️

- [X] T077 Implement `FuzzSuperviseTOML` in `internal/supervise/config/config_fuzz_test.go` per research.md R-011 — accept `[]byte` input, write to `t.TempDir()`-scoped file, call `Load(ctx, path)`, assert (a) no panic, (b) if `err != nil` then `errors.Is(err, sentinel)` for at least one of the 17 catalogued sentinels, (c) if `err == nil` then `*Supervisor` is non-nil and `s.Validators` contains only allow-listed values
- [X] T078 Verify the seed corpus from T005 is wired correctly — `go test -fuzz=FuzzSuperviseTOML -fuzztime=2s ./internal/supervise/config/` (a 2-second smoke run; full 60s gate is in Phase 13)

**Checkpoint**: Fuzz target compiles, smoke-runs, and exercises the eight seed corpus files.

---

## Phase 13: Polish — Final Gates, Coverage, and Doc Updates

**Purpose**: Run every gate the chunk contract requires; lock the API into PACKAGE-MAP; mark the chunk done.

- [X] T079 Run `magex format:fix` from repo root and confirm no formatting changes outstanding
- [X] T080 Run `magex lint` from repo root and confirm zero lint findings on `internal/supervise/config/`
- [X] T081 Run `magex test:race` from repo root and confirm race-clean across the full module
- [X] T082 Run `go test -fuzz=FuzzSuperviseTOML -fuzztime=60s ./internal/supervise/config/` and confirm zero crashes, zero new corpus rows added (Constitution VIII fuzz target #5; SDD-18 chunk-contract gate)
- [X] T083 Run `go test -cover ./internal/supervise/config/` and confirm coverage ≥ 95% (Constitution VIII High band; SDD-18 chunk-contract gate)
- [X] T084 Append "Exported API — locked at SDD-18" section to `docs/PACKAGE-MAP.md` as a NEW entry under `internal/supervise/` listing the locked surface from contracts/api.md (`Supervisor`, `Child`, `DiscordRouting`, `Watchdog`, `Validator`, `Load`, `Validate`, every `Default*` / `Max*`, every `Err*` sentinel)
- [X] T085 Update the AC-10 row in `docs/AC-MATRIX.md` to reference the new test file paths (`internal/supervise/config/config_test.go`, `internal/supervise/config/validate_test.go`, `internal/supervise/config/config_fuzz_test.go`)
- [X] T086 Mark SDD-18 status `done` in `docs/SDD-PLAYBOOK.md`
- [X] T087 Confirm every default documented in `docs/CONFIG-SCHEMA.md` Supervisor section is asserted by a `TestSuperviseConfig_Default*` test (manual cross-check; the thirteen tests from T015 + T016 + T017 cover the full default catalogue)
- [X] T088 Create the single combined commit per the SDD-18 chunk contract — `git add internal/supervise/config/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md specs/018-supervise-config/tasks.md && git commit -m "feat(supervise/config): supervisor TOML schema + validation + fuzz (SDD-18)"`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: no dependencies — can start immediately
- **Phase 2 (Foundational)**: depends on Phase 1 — BLOCKS every user-story phase (types and signatures must exist before tests compile)
- **Phases 3–10 (User Stories US1–US8)**: depend on Phase 2 completion; can proceed in priority order or in parallel by developer slot
- **Phase 11 (Cross-cutting validators)**: depends on Phase 2 (types) + Phase 3 (Load happy path); covers spec Edge Cases not assigned to a user story
- **Phase 12 (Fuzz target)**: depends on Phase 11 completion (the fuzz contract requires every error path typed; every typed error must already exist)
- **Phase 13 (Polish)**: depends on every prior phase — final gates and doc updates

### User Story Dependencies (within Phases 3–10)

- **US1 (P1)**: foundational happy path — every later user story depends on US1's `Load` happy path being green (so its test fixtures can be loaded as the baseline before introducing the rejection trigger)
- **US2 (P1)**: depends on US1 — adds strict-mode error mapping
- **US3 (P1)**: depends on US1 — adds validator allow-list inside the materialiser
- **US4 (P1)**: depends on US1 — adds grace-cache validator + contradiction guard
- **US5 (P1)**: depends on US1 — adds refresh-window parser
- **US6 (P1)**: depends on US1 — adds child-command validator
- **US7 (P2)**: depends on US1 — adds scope non-empty check
- **US8 (P1)**: depends on US1 — self-tests on shape, env, init, goroutines

After US1 completes, US2–US8 can proceed in parallel by separate developer slots; their test additions touch disjoint sentinel paths even when they share `validate_test.go` (sequential edits within the file are still required).

### Within Each User Story

- Tests MUST be written and FAIL before the implementation task in that phase lands
- Implementation tasks within a story may be staged (e.g., US1 splits the materialiser across decode + materialise + required-field gate)
- A "Verify Phase N tests pass" task closes each user-story phase and gates the next

### Parallel Opportunities

- Phase 1: T002, T003, T004, T005 are all `[P]` — different files, no dependencies
- Phase 2: T006, T007, T008, T009 are all `[P]` — different files; T010 depends on T008's type declaration; T011 depends on T006–T010
- After Phase 2 completes, US1 (Phase 3) is the gating MVP work; once US1 lands, US2–US8 can run in parallel by developer
- Phase 13 gate runs are mostly sequential (lint after format, test:race after lint, fuzz after test:race) but the doc updates (T084–T086) can run in parallel after T083

---

## Parallel Example: Phase 2 Foundational

```bash
# Launch all four foundational file declarations together (different files, no dependencies):
Task: "Declare sentinel error catalogue in internal/supervise/config/errors.go"
Task: "Declare defaults catalogue + validator allow-list in internal/supervise/config/defaults.go"
Task: "Declare public types + decoded shape + Load signature in internal/supervise/config/config.go"
Task: "Declare path helpers in internal/supervise/config/paths.go"
```

## Parallel Example: After US1 Lands

```bash
# Six user stories in parallel by separate developers (one per slot):
Developer A: Phase 4 (US2 unknown field rejection)
Developer B: Phase 5 (US3 validator allow-list)
Developer C: Phase 6 (US4 grace window cap)
Developer D: Phase 7 (US5 refresh window)
Developer E: Phase 8 (US6 command absolute path)
Developer F: Phase 10 (US8 no secrets / no env)
# Phase 9 (US7 P2 scope) and Phase 11 (cross-cutting) follow as capacity allows
```

---

## Implementation Strategy

### MVP First (US1 only)

1. Complete Phase 1 (Setup) — five tasks, mostly file scaffolding
2. Complete Phase 2 (Foundational) — six tasks, types + signatures + stub `Load`
3. Complete Phase 3 (US1) — happy path + every default asserted; this is the MVP
4. **STOP and VALIDATE**: every named default test passes; `Load` returns a fully-populated `*Supervisor` for `valid_maximal.toml`

### Incremental Delivery (TDD-mandatory)

1. Setup + Foundational + US1 → MVP (Phase 3 checkpoint)
2. Add US2 (unknown-field rejection) → typo defence operational
3. Add US3 (validator allow-list) → staleness-detection coverage enforced
4. Add US4 (grace window cap) → Constitution IV TTL boundary enforced
5. Add US5 (refresh window) → daily approval window anchored
6. Add US6 (command absolute path) → `PATH`-hijack surface removed
7. Add US7 (scope non-empty) → empty-supervisor failure mode caught
8. Add US8 (no secrets / no env) → Constitution X invariants self-tested
9. Add cross-cutting validators (Phase 11) → spec Edge Cases all typed
10. Add fuzz target (Phase 12) → Constitution VIII fuzz target #5 operational
11. Run final gates (Phase 13) → format / lint / test:race / fuzz 60s / coverage ≥95% / docs updated → single combined commit

### Parallel Team Strategy

After Phase 2 + Phase 3 (US1) lands, six developer slots can pick up US2–US8 (skip US7 if P2 is deferred) in parallel. Phase 11 follows once every story-level validator has compiled. Phase 12 depends on Phase 11. Phase 13 is the integration / release sequence one developer drives.

---

## Notes

- `[P]` tasks edit different files and have no dependencies on incomplete tasks; tasks that share a file are sequential
- `[Story]` label maps each task to its user story for traceability; cross-cutting validators (Phase 11) and final gates (Phase 13) carry no story label
- Every user story is independently completable and testable; the user-story phases form an acyclic dependency graph rooted at US1
- TDD-mandatory: verify each test FAILS before its implementation task lands; verify each test PASSES at the end of its phase via the corresponding "Verify Phase N tests pass" task
- Constitutional gates (no `init`, no goroutines, no env reads, no secret-typed fields, no new direct deps) are self-tested in Phase 10 (US8) — failures there indicate a Constitution IX / X regression and MUST be fixed before Phase 13 runs
- Coverage gate (≥95%) and fuzz gate (60s clean) are non-negotiable per the SDD-18 chunk contract; either failing blocks the combined commit
- Single combined commit at the end of Phase 13 (T088) per the SDD-18 chunk contract — no per-phase commits
