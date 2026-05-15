---

description: "Task list for SDD-06 — internal/config server TOML schema + validation"
---

# Tasks: `internal/config` — Server TOML Schema + Validation (SDD-06)

**Input**: Design documents from `/Users/mrz/projects/hush/specs/006-config-server/`
**Prerequisites**: plan.md (✅), spec.md (✅), research.md (✅), data-model.md (✅), contracts/api.md (✅), quickstart.md (✅)
**Chunk contract**: [docs/sdd/SDD-06.md](../../docs/sdd/SDD-06.md)

**Tests**: TDD-mandatory per Constitution VIII. Every behaviour-contract task has a paired test-writing task scheduled BEFORE the implementation task. Coverage target ≥95%; fuzz target #5 (`FuzzServerTOML`) ≥60 s clean.

**Organization**: Tasks are grouped by user story (US1–US6 from spec.md). Each story is independently testable and deliverable as an MVP increment.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependency on incomplete tasks)
- **[Story]**: Maps task to user story from spec.md (US1–US6)
- All file paths are absolute or workspace-relative from repo root `/Users/mrz/projects/hush/`

## Path Conventions

- **Production source**: `internal/config/{server,defaults,errors,paths,validate,doc}.go`
- **Tests**: `internal/config/{server,validate,defaults,errors,paths}_test.go` and `internal/config/server_fuzz_test.go`
- **Fixtures**: `internal/config/testdata/{valid,invalid,fuzz/FuzzServerTOML}/`
- **Cross-repo doc updates**: `docs/PACKAGE-MAP.md`, `docs/AC-MATRIX.md`, `docs/SDD-PLAYBOOK.md`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Project initialization for the new package and its TOML decoder dependency.

- [x] T001 Add `github.com/pelletier/go-toml/v2` direct dependency by running `go get github.com/pelletier/go-toml/v2` from repo root; verify `go.mod` and `go.sum` are updated and `go mod tidy` is clean
- [x] T002 Create directory structure for fixtures and fuzz corpus: `internal/config/testdata/valid/`, `internal/config/testdata/invalid/`, `internal/config/testdata/fuzz/FuzzServerTOML/`
- [x] T003 [P] Create `internal/config/doc.go` with the package-level doc comment citing Constitution principles III, VI, VIII, IX, X, XI and a roster of the package's locked exports (per plan.md §"Source Code")

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Defaults catalogue, sentinel error catalogue, path-safety helpers, and the public/internal type declarations that every user story depends on. These files are pure data + helpers and contain no story-specific behaviour.

**⚠️ CRITICAL**: No user-story phase can begin until this phase is complete — every test in US1–US6 imports `Default*`, `Min*`, `Err*`, or the `Server` type.

### Tests for Foundational layer (TDD — write FIRST)

> Tests below reference symbols whose implementation lands in T008–T012. They will fail to compile (Go's "red" state) until those tasks land.

- [x] T004 [P] Write `TestDefaults_MatchSchema` in `internal/config/defaults_test.go` — table-driven, one row per documented default in `docs/CONFIG-SCHEMA.md`, asserting that every `Default*`, `Min*`, `Max*`, and `TailscaleCGNAT` exported var equals the documented value (drift-detector for the catalogue)
- [x] T005 [P] Write `TestSentinels_Catalogue` and `TestSentinels_WrapRelationships` in `internal/config/errors_test.go` — assert every `Err*` sentinel is non-nil; assert `errors.Is(ErrListenLoopback, ErrTailscaleBindRequired)`, same for `ErrListenUnspecified` and `ErrListenPublic`; assert `errors.Is(ErrStateDirNotFound, fs.ErrNotExist)`
- [x] T006 [P] Write `TestPaths_ExpandHome` and `TestPaths_AbsPath` in `internal/config/paths_test.go` — table-driven; covers (a) `~/foo` → `$HOME/foo`, (b) `~user/foo` treated as literal (per R-005), (c) `$VAR` not expanded, (d) `Abs` canonicalisation of relative paths
- [x] T007 Write `TestPaths_IsUnderStateDir` in `internal/config/paths_test.go` — table-driven; covers (a) audit_log directly under state_dir → accepted, (b) parent-traversal `~/.hush/../etc` rejected by containment check, (c) absolute escape `/etc/passwd` rejected, (d) Snyk-class "drive-letter false-positive" (`state_dir=/usr`, `audit_log=/usrlocal/...`) rejected by `filepath.Rel` (per R-003). Sequential after T006 — same file

### Implementation for Foundational layer

- [x] T008 [P] Create `internal/config/defaults.go` with all exported vars: `DefaultArgonTime/MemoryMB/Threads`, `MinArgonTime/MemoryMB/Threads`, `DefaultJWTTTL`, `DefaultMaxInteractiveTTL`, `DefaultMaxSupervisorTTL`, `DefaultSupervisorTTLMax`, `DefaultMaxUses`, `DefaultNonceTTL`, `DefaultClockSkew`, `DefaultStateDir`, `DefaultAuditLog`, `DefaultClientRegistry`, `DefaultListenPort`, `DefaultRequireTailscale`, `DefaultAllowedCIDRs`, `DefaultRequireFileModeChecks`, `DefaultRequireKeychainACL`, `DefaultRequireNTPSync`, `DefaultMaxClockDrift`, `MinPathPrefixLen`, `MaxPathPrefixLen`, `TailscaleCGNAT` — each with inline `//nolint:gochecknoglobals` citing the sentinel-class precedent
- [x] T009 [P] Create `internal/config/errors.go` with the full sentinel catalogue from data-model.md (decode-phase, network/listen-addr family with wrap relationships, path-safety, crypto-floor, TTL-bound) — every sentinel as `var ErrXxx = errors.New("hush/config: <message>")` or `fmt.Errorf("...: %w", parent)` for the wrapped triple
- [x] T010 [P] Create `internal/config/paths.go` with unexported helpers: `expandHome(string) (string, error)` (uses `os.UserHomeDir`; only leading `~` + separator/EOF), `absPath(string) (string, error)` (wraps `filepath.Abs`), `isUnderStateDir(audit, stateDir string) bool` (per R-003: `filepath.Rel` + four-part check)
- [x] T011 Create `internal/config/server.go` with the public types: `Server`, `ServerSection`, `DiscordSection`, `CryptoSection`, `NetworkSection`, `SecuritySection` (fields per data-model.md §"Public types")
- [x] T012 Append the unexported decoded types to `internal/config/server.go`: `serverDecoded`, `serverSectionDecoded`, `discordSectionDecoded`, `cryptoSectionDecoded`, `networkSectionDecoded`, `securitySectionDecoded` (per data-model.md §"Wire-shape (decoded) types")

### Test fixtures (data only — parallelizable)

- [x] T013 [P] Create `internal/config/testdata/valid/minimal-valid.toml` — only required fields, valid Tailscale CGNAT `listen_addr` (e.g., `100.96.10.4:7743`), placeholder `__STATE_DIR__` for state-dir injection
- [x] T014 [P] Create `internal/config/testdata/valid/full-default.toml` — every documented field present at its documented default value, with `__STATE_DIR__` placeholder
- [x] T015 [P] Create `internal/config/testdata/valid/full-maximal.toml` — every documented field present with non-default values still inside legal ranges (Tailscale IP, 512 MiB Argon memory, 16h supervisor TTL, etc.); used to assert no field is silently overridden
- [x] T016 [P] Create the eight fuzz seed files in `internal/config/testdata/fuzz/FuzzServerTOML/` per R-008: `minimal-valid`, `full-default`, `malformed-bytes`, `empty`, `partial-table`, `conflicting-types`, `very-long-string`, `unicode-edge`

**Checkpoint**: Foundation ready — all user stories can now begin.

---

## Phase 3: User Story 1 — Operator runs vault server with valid config (Priority: P1) 🎯 MVP

**Goal**: `LoadServer` accepts a documented-shape TOML file and returns a populated, validated `*Server` with every absent optional field defaulted from the catalogue. The same input produces the same output across calls.

**Independent Test**: Provide `testdata/valid/minimal-valid.toml` and call `LoadServer(ctx, path)`; assert `err == nil`, every documented default present in the returned `*Server`, no field equals a secret-shaped value, and a second call with the same input returns equal values.

### Tests for User Story 1 (TDD — write FIRST)

> All tests below live in `internal/config/server_test.go` — same file, hence sequential within the test phase.

- [x] T017 [US1] Write `TestServer_FullMinimalConfig` in `internal/config/server_test.go` — load `testdata/valid/minimal-valid.toml` (with `__STATE_DIR__` rewritten to `t.TempDir()`); assert no error, every documented default applied, `Discord.BotTokenKeychainItem` populated as a name (not a secret)
- [x] T018 [US1] Write `TestServer_FullMaximalConfig` in `internal/config/server_test.go` — load `testdata/valid/full-maximal.toml`; assert every field equals the explicitly-set non-default value (no silent override of operator intent)
- [x] T019 [US1] Write `TestLoadServer_AppliesEveryDocumentedDefault` in `internal/config/server_test.go` — table-driven, one row per `Default*` var: load minimal-valid (with the corresponding key omitted), assert the returned `*Server` field equals the catalogue default. Covers SC-001 ("every documented default has an asserting test") for the load-application side
- [x] T020 [US1] Write `TestLoadServer_Idempotent` in `internal/config/server_test.go` — load same file twice; assert deep-equality of the two `*Server` values (per FR-011)
- [x] T021 [US1] Write `TestLoadServer_ContextCancelled` in `internal/config/server_test.go` — pre-cancelled `context.Context`; assert `errors.Is(err, context.Canceled)` and `s == nil`
- [x] T022 [US1] Write `TestServer_ExpandsTildePathsCorrectly` in `internal/config/server_test.go` — config with `state_dir = "~/temp-XXXX"` (where `XXXX` is `t.TempDir()` basename); assert returned `StateDir` is absolute and resolves under `$HOME` (per FR-005b)
- [x] T023 [US1] Write `TestServer_DoesNotExpandEnvVars` in `internal/config/server_test.go` — config with `state_dir = "$HOME/foo"`; set `$HOME` to a real dir; assert load fails with `ErrStateDirNotFound` (the literal `$HOME` is not expanded; treated as a directory name) — protects FR-005b's "no other shell expansion" clause

### Implementation for User Story 1

- [x] T024 [US1] Implement `materialize(decoded serverDecoded, stateDirAbs string) (*Server, error)` in `internal/config/server.go` — apply defaults to absent pointer/string fields, parse duration strings (`time.ParseDuration` failure → `ErrInvalidDuration`), `~`-expand and `Abs` every path field, accumulate `ErrMissingRequiredField` for empty required fields, return `errors.Join` on multiple missing-required failures (per R-006 + R-014)
- [x] T025 [US1] Implement `LoadServer(ctx context.Context, path string) (*Server, error)` in `internal/config/server.go` — `ctx.Err()` check at entry, `os.Open` + `defer Close`, `toml.NewDecoder(f).DisallowUnknownFields(true).Decode(&decoded)`, error-wrapping fan-out (`ErrUnknownField` / `ErrTOMLDecode`), call `materialize`, call `Server.Validate`, never return a partial `*Server` on non-nil error
- [x] T026 [US1] Implement `Server.Validate() error` skeleton in `internal/config/validate.go` — orchestrator that runs the rule order documented in contracts/api.md §"Determinism": (1) `RequireTailscale` gate, (2) Argon floors, (3) listen-addr family, (4) health-bind family, (5) `path_prefix`, (6) `audit_log` containment, (7) `max_supervisor_ttl` bounds. Multi-violation returns via `errors.Join`. Implementation of individual rule helpers lands in subsequent stories; this task ships the skeleton + rule registry only

**Checkpoint**: At this point, US1 (happy-path config load with defaults) is fully functional and testable independently. The MVP slice is shippable: a downstream consumer (SDD-10, SDD-15) can call `LoadServer` with a clean file and get a populated `*Server`. Rejection rules are wired in subsequent stories.

---

## Phase 4: User Story 2 — Operator catches a typo before it reaches production (Priority: P2)

**Goal**: Unknown / misspelled / wrong-type fields and malformed `path_prefix` values are rejected at load time with distinct, named sentinel errors so the operator is never surprised by silent acceptance.

**Independent Test**: Author a TOML with `lisen_addr = ...` (typo); assert `errors.Is(err, ErrUnknownField)` and `s == nil`. Author a TOML with `path_prefix = "ab"`; assert `errors.Is(err, ErrPathPrefixInvalid)`.

### Tests for User Story 2 (TDD — write FIRST)

- [x] T027 [P] [US2] Create `internal/config/testdata/invalid/unknown-field.toml` — valid except contains a `[server] lisen_addr = "100.96.10.4:7743"` typo
- [x] T028 [P] [US2] Create `internal/config/testdata/invalid/wrong-type.toml` — `[server] listen_addr = 1234` (int where string expected)
- [x] T029 [P] [US2] Create three `path_prefix` fixtures: `internal/config/testdata/invalid/path-prefix-too-short.toml` (`path_prefix = "ab"`), `path-prefix-too-long.toml` (33+ chars), `path-prefix-bad-charset.toml` (`path_prefix = "abc def"`)
- [x] T030 [P] [US2] Create `internal/config/testdata/invalid/bad-duration.toml` — `[crypto] jwt_default_ttl = "not-a-duration"`
- [x] T031 [US2] Write `TestServer_RejectsUnknownField` in `internal/config/server_test.go` — load `testdata/invalid/unknown-field.toml`; assert `errors.Is(err, ErrUnknownField)`, `s == nil`, error message names the offending field
- [x] T032 [US2] Write `TestServer_RejectsWrongType` in `internal/config/server_test.go` — load `testdata/invalid/wrong-type.toml`; assert `errors.Is(err, ErrTOMLDecode)`, `s == nil`
- [x] T033 [US2] Write `TestServer_RejectsBadDuration` in `internal/config/validate_test.go` — load `testdata/invalid/bad-duration.toml`; assert `errors.Is(err, ErrInvalidDuration)`
- [x] T034 [US2] Write `TestServer_RejectsPathPrefixTooShort` in `internal/config/validate_test.go` — assert `errors.Is(err, ErrPathPrefixInvalid)`
- [x] T035 [US2] Write `TestServer_RejectsPathPrefixTooLong` in `internal/config/validate_test.go`
- [x] T036 [US2] Write `TestServer_RejectsPathPrefixBadCharset` in `internal/config/validate_test.go`
- [x] T037 [US2] Write `TestServer_AcceptsValidPathPrefix` in `internal/config/validate_test.go` — `path_prefix = "valid_prefix-1"` loads cleanly

### Implementation for User Story 2

- [x] T038 [US2] Wire pelletier/go-toml/v2 strict-mode error → `ErrUnknownField` in `LoadServer` (`internal/config/server.go`) — translate `*toml.StrictMissingFieldsError` (or modern equivalent per R-001) into a wrapped `ErrUnknownField` whose message names the offending field path
- [x] T039 [US2] Wire other go-toml/v2 decode errors (syntax, type mismatch) → `ErrTOMLDecode` in `LoadServer` (`internal/config/server.go`) — wrap upstream error with `%w` so `errors.As` extracts the structured info
- [x] T040 [US2] Implement `validatePathPrefix(string) error` in `internal/config/validate.go` — length check first (against `MinPathPrefixLen`/`MaxPathPrefixLen`), then charset via `sync.Once`-initialised `pathPrefixRegex = regexp.MustCompile(\`^[A-Za-z0-9_-]+$\`)` (per R-010, no `init()`); either failure → `ErrPathPrefixInvalid`. Wire into `Server.Validate` orchestrator at rule slot 5

**Checkpoint**: US1 + US2 work independently. An operator who fat-fingers a config gets a single named error per load attempt.

---

## Phase 5: User Story 3 — Operator cannot accidentally weaken the network boundary (Priority: P1)

**Goal**: `listen_addr` (and `health_bind` when explicit) MUST resolve to a Tailscale CGNAT host. Loopback, unspecified, public IPs, and malformed addresses are each rejected with distinct, named sentinels — every wrap of the umbrella `ErrTailscaleBindRequired`. `require_tailscale = false` is rejected before any address validation runs.

**Independent Test**: For each rejected address category, supply an otherwise valid TOML; assert the corresponding sentinel via `errors.Is`. For an accepted CGNAT address, the same fixture loads cleanly. Same family of rejections applies to `health_bind` when it is explicit.

### Tests for User Story 3 (TDD — write FIRST)

- [x] T041 [P] [US3] Create six `listen_addr` fixtures in `internal/config/testdata/invalid/`: `loopback.toml` (`127.0.0.1:7743`), `unspecified.toml` (`0.0.0.0:7743`), `public.toml` (`8.8.8.8:7743`), `malformed.toml` (`listen_addr = "garbage"`), `missing-listen-addr.toml` (`listen_addr = ""`), `tailscale-required.toml` (`require_tailscale = false`)
- [x] T042 [P] [US3] Create three `health_bind` fixtures in `internal/config/testdata/invalid/`: `health-bind-loopback.toml`, `health-bind-public.toml`, `health-bind-malformed.toml`
- [x] T043 [US3] Write `TestServer_RejectsLoopback` in `internal/config/validate_test.go` — load `testdata/invalid/loopback.toml`; assert `errors.Is(err, ErrListenLoopback)` AND `errors.Is(err, ErrTailscaleBindRequired)`
- [x] T044 [US3] Write `TestServer_RejectsUnspecified` in `internal/config/validate_test.go` — assert `ErrListenUnspecified` + `ErrTailscaleBindRequired`
- [x] T045 [US3] Write `TestServer_RejectsPublic` in `internal/config/validate_test.go` — assert `ErrListenPublic` + `ErrTailscaleBindRequired`
- [x] T046 [US3] Write `TestServer_RejectsMalformedListenAddr` in `internal/config/validate_test.go` — assert `ErrListenMalformed`
- [x] T047 [US3] Write `TestServer_RejectsMissingListenAddr` in `internal/config/validate_test.go` — assert `ErrMissingRequiredField`
- [x] T048 [US3] Write `TestServer_AcceptsTailscaleCGNAT` in `internal/config/validate_test.go` — `listen_addr = "100.96.10.4:7743"` (mid-range CGNAT) loads cleanly; also covers boundary case `100.64.0.1` and `100.127.255.254`
- [x] T049 [US3] Write `TestServer_HealthBindRejectsLoopback` in `internal/config/validate_test.go` — same family for `health_bind` when explicit
- [x] T050 [US3] Write `TestServer_HealthBindRejectsPublic` in `internal/config/validate_test.go`
- [x] T051 [US3] Write `TestServer_HealthBindRejectsMalformed` in `internal/config/validate_test.go`
- [x] T052 [US3] Write `TestServer_HealthBindInheritsListenAddr` in `internal/config/validate_test.go` — `health_bind` absent → returned `Network.HealthBind == Server.ListenAddr`; no second validation pass needed
- [x] T053 [US3] Write `TestServer_RejectsRequireTailscaleFalse` in `internal/config/validate_test.go` — assert `errors.Is(err, ErrTailscaleRequired)`
- [x] T054 [US3] Write `TestServer_AcceptsRequireTailscaleTrue` in `internal/config/validate_test.go` — explicit `true` and absent (defaults to true) both load cleanly

### Implementation for User Story 3

- [x] T055 [US3] Implement `validateTailscaleAddrPort(field, value string) (netip.AddrPort, error)` helper in `internal/config/validate.go` — `netip.ParseAddrPort` (failure → `ErrListenMalformed`); check `addr.IsLoopback()` → wrap `ErrTailscaleBindRequired` as `ErrListenLoopback`; `addr.IsUnspecified()` → `ErrListenUnspecified`; `TailscaleCGNAT.Contains(addr)` → ACCEPT; otherwise → `ErrListenPublic`. Order matches R-002. Field name embedded in wrap message
- [x] T056 [US3] Wire `RequireTailscale` truthiness check into `Server.Validate` as the FIRST network rule (`internal/config/validate.go`) — `s.Network.RequireTailscale != true` → `ErrTailscaleRequired`
- [x] T057 [US3] Wire `validateTailscaleAddrPort` into `Server.Validate` for `s.Server.ListenAddr` (`internal/config/validate.go`)
- [x] T058 [US3] Wire `validateTailscaleAddrPort` into `Server.Validate` for `s.Network.HealthBind` when it differs from `ListenAddr` after materialization (`internal/config/validate.go`) — inherited values skip re-validation per R-012
- [x] T059 [US3] Implement `health_bind` inheritance in `materialize` (`internal/config/server.go`) — when decoded `HealthBind` is empty, copy the parsed `ListenAddr` value into `Server.Network.HealthBind`

**Checkpoint**: US1 + US2 + US3 independently functional. The Tailscale-only network boundary is fully enforced for both bindings.

---

## Phase 6: User Story 4 — Operator cannot weaken Argon2id below the floor (Priority: P1)

**Goal**: `argon_memory_mb < 256`, `argon_time < 4`, `argon_threads < 4` are each rejected with distinct, named sentinels (Constitution III non-negotiable). `max_supervisor_ttl` outside `(jwt_default_ttl, 24h]` is rejected with a single TTL-bound sentinel.

**Independent Test**: For each Argon floor, supply a TOML with the parameter below the floor; assert the corresponding sentinel. For `max_supervisor_ttl`, exercise both edges (≤ jwt_default_ttl AND > 24h cap).

### Tests for User Story 4 (TDD — write FIRST)

- [x] T060 [P] [US4] Create five fixtures in `internal/config/testdata/invalid/`: `argon-memory-low.toml` (`argon_memory_mb = 128`), `argon-time-low.toml` (`argon_time = 1`), `argon-threads-low.toml` (`argon_threads = 1`), `supervisor-ttl-below-jwt.toml` (`max_supervisor_ttl = "8h"` with `jwt_default_ttl = "8h"`), `supervisor-ttl-above-cap.toml` (`max_supervisor_ttl = "25h"`)
- [x] T061 [US4] Write `TestServer_RejectsArgonMemoryUnder256` in `internal/config/validate_test.go` — assert `errors.Is(err, ErrArgonMemoryTooLow)`
- [x] T062 [US4] Write `TestServer_AcceptsArgonMemoryAt256` in `internal/config/validate_test.go` — boundary; loads cleanly
- [x] T063 [US4] Write `TestServer_DefaultsArgonMemoryTo256` in `internal/config/validate_test.go` — absent `argon_memory_mb` → returned value equals `DefaultArgonMemoryMB` (256)
- [x] T064 [US4] Write `TestServer_RejectsArgonTimeUnder4` in `internal/config/validate_test.go` — assert `ErrArgonTimeTooLow`
- [x] T065 [US4] Write `TestServer_RejectsArgonThreadsUnder4` in `internal/config/validate_test.go` — assert `ErrArgonThreadsTooLow`
- [x] T066 [US4] Write `TestServer_RejectsSupervisorTTLBelowJWT` in `internal/config/validate_test.go` — `max_supervisor_ttl == jwt_default_ttl` triggers the rejection (boundary is strict-greater per R-011); assert `ErrSupervisorTTLOutOfRange`
- [x] T067 [US4] Write `TestServer_RejectsSupervisorTTLAboveCap` in `internal/config/validate_test.go` — `max_supervisor_ttl > 24h` triggers; assert `ErrSupervisorTTLOutOfRange`

### Implementation for User Story 4

- [x] T068 [US4] Implement Argon2id floor validators in `internal/config/validate.go` — three checks (`s.Crypto.ArgonMemoryMB < MinArgonMemoryMB` → `ErrArgonMemoryTooLow`; same shape for time/threads); wire into `Server.Validate` at rule slot 2 (per the locked rule order)
- [x] T069 [US4] Implement `max_supervisor_ttl` bounds validator in `internal/config/validate.go` — both conditions per R-011 (`> JWTDefaultTTL` AND `<= DefaultSupervisorTTLMax`); either failure → `ErrSupervisorTTLOutOfRange`; wire into `Server.Validate` at rule slot 7

**Checkpoint**: US1–US4 independently functional. The constitutional Argon2id floor cannot be silently lowered.

---

## Phase 7: User Story 5 — Operator cannot redirect the audit log out of the state directory (Priority: P1)

**Goal**: `audit_log` MUST resolve underneath `state_dir` after `~`-expansion + `Abs`. Out-of-tree paths (absolute escape, parent traversal, drive-letter false-positive) are rejected with `ErrAuditLogEscape`. Missing `state_dir` (FR-005a) and non-directory `state_dir` are distinct, named errors.

**Independent Test**: Supply a TOML with `audit_log = "/etc/passwd"`; assert `errors.Is(err, ErrAuditLogEscape)`. Supply a TOML with `state_dir = "/path/that/does/not/exist"`; assert `errors.Is(err, ErrStateDirNotFound)` AND `errors.Is(err, fs.ErrNotExist)`.

### Tests for User Story 5 (TDD — write FIRST)

- [x] T070 [P] [US5] Create four fixtures in `internal/config/testdata/invalid/`: `audit-log-escape.toml` (`audit_log = "/etc/passwd"` with placeholder state_dir), `audit-log-traversal.toml` (`audit_log = "__STATE_DIR__/../etc/passwd"`), `state-dir-missing.toml` (state_dir set to a placeholder for an ephemeral nonexistent path), `state-dir-not-a-dir.toml` (state_dir set to a placeholder for a regular file the test creates)
- [x] T071 [US5] Write `TestServer_RejectsAuditLogOutsideStateDir` in `internal/config/validate_test.go` — load `testdata/invalid/audit-log-escape.toml` (with `__STATE_DIR__` rewritten to `t.TempDir()`); assert `errors.Is(err, ErrAuditLogEscape)`, `s == nil`
- [x] T072 [US5] Write `TestServer_RejectsAuditLogParentTraversal` in `internal/config/validate_test.go` — same sentinel for `..`-escaping path
- [x] T073 [US5] Write `TestServer_AcceptsAuditLogUnderStateDir` in `internal/config/validate_test.go` — `audit_log = "__STATE_DIR__/audit.jsonl"` loads cleanly (positive case)
- [x] T074 [US5] Write `TestServer_AuditLogContainmentRejectsDriveLetterFalsePositive` in `internal/config/validate_test.go` — `state_dir = t.TempDir()/foo`, `audit_log = t.TempDir()/foobar/audit.jsonl` (string-prefix true, lexical-containment false); assert `ErrAuditLogEscape` (covers the R-003 stdlib-correct-substitute rationale)
- [x] T075 [US5] Write `TestServer_RejectsMissingStateDir` in `internal/config/server_test.go` — assert `errors.Is(err, ErrStateDirNotFound)` AND `errors.Is(err, fs.ErrNotExist)`
- [x] T076 [US5] Write `TestServer_RejectsStateDirNotADirectory` in `internal/config/server_test.go` — `state_dir` resolves to a regular file; assert `errors.Is(err, ErrStateDirUnsafe)`
- [x] T077 [US5] Write `TestServer_LoaderDoesNotCreateStateDir` in `internal/config/server_test.go` — `state_dir` does not exist; after a failed `LoadServer`, assert via `os.Stat` that the directory was NOT created (per FR-005a + R-004)

### Implementation for User Story 5

- [x] T078 [US5] Wire `os.Stat(absStateDir)` into the `materialize` step in `internal/config/server.go` — `errors.Is(err, fs.ErrNotExist)` → `ErrStateDirNotFound` (which itself wraps `fs.ErrNotExist`); `info.IsDir() == false` → `ErrStateDirUnsafe`; other `os.Stat` errors wrap with `%w` and field name. Loader NEVER calls `MkdirAll` (per R-004)
- [x] T079 [US5] Wire `isUnderStateDir` containment check into `Server.Validate` for `s.Server.AuditLog` (`internal/config/validate.go`) — runs at rule slot 6; failure → `ErrAuditLogEscape`

**Checkpoint**: US1–US5 independently functional. The audit-log path cannot be redirected out of the state directory; the loader never creates `state_dir`.

---

## Phase 8: User Story 6 — Operator cannot smuggle secrets through the config (Priority: P1)

**Goal**: The `Server` struct holds NO secret values — only Keychain item names and other non-secret pointers. The loader does NOT consult environment variables for any secret-bearing field; the same TOML produces the same `*Server` regardless of process environment.

**Independent Test**: Reflect over the `Server` struct shape; assert every field's documented purpose is non-secret. Set environment variables that NAME plausible secret fields (`HUSH_DISCORD_TOKEN=...`, `HUSH_VAULT_PASSPHRASE=...`); load a minimal-valid TOML; assert no field of the returned `*Server` equals any of those environment values.

### Tests for User Story 6 (TDD — write FIRST)

- [x] T080 [US6] Write `TestServer_SchemaHasNoSecretFields` in `internal/config/server_test.go` — reflect over the `Server` value type; for each string field, assert the field name does NOT contain `Token`, `Passphrase`, `Secret`, `Password`, or `Key` UNLESS the field is documented as a Keychain item name (e.g., `BotTokenKeychainItem` is allowed because the suffix `KeychainItem` is the safe-pointer marker per data-model.md). Anchors FR-006 + SC-005 against silent struct drift
- [x] T081 [US6] Write `TestLoadServer_DoesNotReadSecretsFromEnv` in `internal/config/server_test.go` — `t.Setenv("HUSH_DISCORD_TOKEN", "leaked-token-value-12345")`, `t.Setenv("HUSH_VAULT_PASSPHRASE", "leaked-passphrase-67890")`, `t.Setenv("HUSH_BOT_TOKEN", "another-leak")`; load `testdata/valid/minimal-valid.toml`; reflect over every string field of the returned `*Server` and assert NONE equals any of the seeded env values. Anchors FR-007
- [x] T082 [US6] Write `TestServer_DiscordBotTokenIsKeychainItemName` in `internal/config/server_test.go` — load minimal-valid; assert `s.Discord.BotTokenKeychainItem` equals the value from the TOML (a Keychain item name like `"hush-discord"`), NOT a token-shaped opaque string
- [x] T083 [US6] Write `TestPackageHasNoEnvVarReadsForSecretFields` in `internal/config/server_test.go` — runtime audit: read the package's source files (via `go/parser` over the `internal/config/*.go` files) and assert zero `os.Getenv`/`os.LookupEnv` call sites. The single permitted env-reading call is `os.UserHomeDir` (which reads `$HOME` for `~`-expansion only); the test allow-lists that callee. Anchors FR-007 in code rather than just behaviour

### Implementation for User Story 6

- [x] T084 [US6] Audit `internal/config/*.go`: confirm zero `os.Getenv`/`os.LookupEnv` call sites; the only `os` env-reading call is `os.UserHomeDir` inside `paths.go`'s `expandHome`. Add a doc.go assertion comment: `// Constitution X: this package reads no environment variable for any secret-bearing field. The single env-reading call is os.UserHomeDir (for ~-expansion of non-secret path fields).`
- [x] T085 [US6] Verify the `Server` struct shape against data-model.md §"Public types": no `[]byte` fields, no `*securebytes.SecureBytes` fields, no string field documented as holding a secret. Cross-check `BotTokenKeychainItem` is the only secret-adjacent name and its purpose is documented in the field's godoc

**Checkpoint**: US1–US6 independently functional. The full P1 acceptance set ships at this checkpoint; US2 (P2) is also complete.

---

## Phase 9: Polish & Cross-Cutting Concerns

**Purpose**: Multi-violation join semantics, fuzz target, sentinel-completeness regression, gates, coverage check, and the final cross-repo doc updates.

### Cross-cutting tests

- [x] T086 [P] Write `TestValidate_MultiViolationJoinsErrors` in `internal/config/validate_test.go` — author a synthetic config with multiple violations (e.g., loopback `listen_addr` AND argon-memory-low AND audit-log-escape); call `Server.Validate`; assert `errors.Is(err, ErrListenLoopback)` AND `errors.Is(err, ErrArgonMemoryTooLow)` AND `errors.Is(err, ErrAuditLogEscape)` against the SAME joined error value (per R-014)
- [x] T087 [P] Write `TestLoadServer_AllErrorsAreSentinels` in `internal/config/server_test.go` — iterate over every file in `testdata/invalid/`; call `LoadServer` for each (with `__STATE_DIR__` rewritten as needed); assert every returned `err` matches at least one sentinel from the package catalogue via `errors.Is`. Regression-detector for un-sentineled error paths (anchors FR-009)
- [x] T088 [P] Write `FuzzServerTOML(f *testing.F)` in `internal/config/server_fuzz_test.go` — per R-008: seed corpus from `testdata/fuzz/FuzzServerTOML/` (eight files), `f.Fuzz` callback writes input bytes to a `t.TempDir()`-scoped file with a real state-dir alongside, calls `LoadServer`, and asserts (a) no panic, (b) on non-nil err, `isKnownSentinel(err)` returns true. Helper `isKnownSentinel` iterates the package's sentinel catalogue using `errors.Is`

### Final gates (run from repo root)

- [x] T089 Run `magex format:fix` and resolve any formatting diffs introduced during implementation
- [x] T090 Run `magex lint` and resolve every finding; do NOT silence with directives unless the rationale is constitutional (e.g., `//nolint:gochecknoglobals` on sentinel-class vars, with the precedent-citation comment)
- [x] T091 Run `magex test:race` and verify the entire suite is race-clean
- [x] T092 Run `go test -fuzz=FuzzServerTOML -fuzztime=60s ./internal/config/` and verify no panic, no new entries written to `testdata/fuzz/FuzzServerTOML/` representing crashes, and a clean exit (the 60 s gate from chunk-contract + Constitution VIII fuzz target #5)
- [x] T093 Run `go test -cover ./internal/config/` and verify coverage ≥95% on the package (Constitution VIII High band). If below, identify uncovered paths and either add asserting tests or document the constitutional exemption

### Cross-repo doc updates

- [x] T094 [P] Append "Exported API — locked at SDD-06" subsection to `docs/PACKAGE-MAP.md` under `internal/config`, listing every locked symbol from contracts/api.md (struct types, `LoadServer`, `Server.Validate`, all `Default*`/`Min*`/`Max*` constants, `TailscaleCGNAT`, every sentinel error). Mirror the table headings from contracts/api.md so drift is grep-detectable
- [x] T095 [P] Update `docs/AC-MATRIX.md` rows for AC-1 (server CLI hardness — config gates) and AC-8 (startup hardening) with the new test file paths: `internal/config/server_test.go`, `internal/config/validate_test.go`, `internal/config/server_fuzz_test.go`. Mark each previously-`pending` cell as `verified by SDD-06`
- [x] T096 Mark SDD-06 status `done` in `docs/SDD-PLAYBOOK.md` (the chunk's row); do NOT modify status of other chunks

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately.
- **Phase 2 (Foundational)**: Depends on Phase 1. **BLOCKS** all user-story phases — every test in US1–US6 references symbols defined in T008–T012.
- **Phase 3 (US1)**: Depends on Phase 2. MVP slice — happy-path config load.
- **Phase 4 (US2)**: Depends on Phase 3 (`LoadServer` must exist before "load + reject" tests can run).
- **Phase 5 (US3)**: Depends on Phase 3 (`Server.Validate` skeleton in T026 is the rule-engine entry).
- **Phase 6 (US4)**: Depends on Phase 5 (rule-engine wiring established).
- **Phase 7 (US5)**: Depends on Phase 5.
- **Phase 8 (US6)**: Depends on Phase 3 (struct + loader exist; tests reflect over them).
- **Phase 9 (Polish)**: Depends on every user-story phase being complete (the regression suite covers all sentinels; the coverage gate measures the union).

### User Story Dependencies (within US phases)

- **US1 (P1, MVP)**: No story dependency. Ships first.
- **US2 (P2)**: Depends on US1's `LoadServer` (`ErrUnknownField` and `ErrTOMLDecode` paths fan out from the decoder error).
- **US3 (P1)**: Depends on US1's `Server.Validate` skeleton (rule-slot wiring).
- **US4 (P1)**: Depends on US3 (the rule-engine slot order is established by then).
- **US5 (P1)**: Depends on US3 + the materialize step (state-dir stat happens during materialize, not during validate).
- **US6 (P1)**: Depends on US1 (struct shape + loader exist). Independent of US3–US5 in implementation.

### Within Each User Story

- **TDD ordering** (Constitution VIII): every test-writing task is scheduled BEFORE the implementation task it covers.
- **File-locality ordering**: tasks that touch the same file (e.g., multiple validators in `validate.go`) are sequential; tasks across different files can be `[P]`.
- **Fixtures before tests** within a story: TOML fixtures are pure data and can be created in parallel before the test functions that load them.

### Parallel Opportunities

- **Phase 2 tests** (T004–T007): different files (`defaults_test.go`, `errors_test.go`, `paths_test.go`) — all `[P]`.
- **Phase 2 implementation** (T008–T010): different files — all `[P]`. T011/T012 share `server.go` so are sequential.
- **Phase 2 fixtures** (T013–T016): different files — all `[P]`.
- **All testdata/invalid/*.toml fixtures** within a story: different files — all `[P]`.
- **Cross-cutting tests in Phase 9** (T086–T088): different files — all `[P]`.
- **Cross-repo doc updates** (T094, T095): different files — `[P]`. T096 is sequential because the playbook is a single file potentially also touched by other future SDDs but here it is the only writer.
- **Once Phase 2 completes**, US2/US6 can run in parallel by different developers (US2 touches `server.go` decoder fan-out + `validate.go` path-prefix; US6 is read-only audit + reflection tests). US3 → US4 → US5 are sequential because they all extend `validate.go`.

---

## Parallel Example: Foundational Phase

```bash
# Launch all foundational TEST files in parallel (different files):
Task: "Write TestDefaults_MatchSchema in internal/config/defaults_test.go"
Task: "Write TestSentinels_Catalogue + WrapRelationships in internal/config/errors_test.go"
Task: "Write TestPaths_ExpandHome + AbsPath + IsUnderStateDir in internal/config/paths_test.go"

# Then launch all foundational IMPLEMENTATION files in parallel:
Task: "Create internal/config/defaults.go with all Default*/Min*/Max* + TailscaleCGNAT"
Task: "Create internal/config/errors.go with sentinel catalogue + wrap relationships"
Task: "Create internal/config/paths.go with expandHome, absPath, isUnderStateDir"

# And in parallel, all valid-fixtures + fuzz seed corpus:
Task: "Create internal/config/testdata/valid/minimal-valid.toml"
Task: "Create internal/config/testdata/valid/full-default.toml"
Task: "Create internal/config/testdata/valid/full-maximal.toml"
Task: "Create eight seed files in internal/config/testdata/fuzz/FuzzServerTOML/"
```

---

## Implementation Strategy

### MVP First (US1 only)

1. Phase 1: Setup → 2. Phase 2: Foundational → 3. Phase 3: US1.
2. **STOP and VALIDATE**: a downstream consumer (mock SDD-10 caller) can `LoadServer` a minimal-valid TOML and get a populated `*Server` with every default applied.
3. The MVP slice is shippable as a leaf package; SDD-10 can already wire against it for the happy path.

### Incremental Delivery

- After US1 (MVP): add US3 (Tailscale boundary) — security floor lands.
- After US3: add US4 (Argon floor) — crypto floor lands.
- After US4: add US5 (audit-log containment) — path-traversal defence lands.
- After US5: add US6 (no-secrets audit) — secret-injection defence lands.
- After US6: add US2 (typo defence — the P2 nicety) — operator-experience polish.
- After every user story: Phase 9 polish + gates + doc updates → ship the chunk.

### Parallel Team Strategy

With multiple developers post-Foundational:

1. Developer A: US1 (MVP) → US3 (Tailscale) → US5 (audit log).
2. Developer B: US2 (typo defence — independent of US3) once US1 is in.
3. Developer C: US6 (reflection-based audit — independent of US3/US4/US5) once US1 is in.
4. Whoever completes their lane first: US4 (Argon floor — fast).
5. Convergence on Phase 9: shared (gates + docs).

---

## Notes

- `[P]` tasks = different files, no dependency on incomplete tasks.
- `[Story]` label maps each story-phase task to its user story for traceability against `spec.md`.
- Every behaviour-contract task has a paired test-writing task scheduled BEFORE the implementation task (Constitution VIII).
- Verify each test FAILS before implementing the corresponding rule (Go's "doesn't compile" or "assertion fails" is the red state).
- DO NOT commit between phases — the chunk-contract requires a single combined commit at the end of Phase 9 (per `docs/sdd/SDD-06.md` Prompt 5).
- Avoid: same-file `[P]` parallelism (creates merge conflicts); cross-story dependencies that break per-story independent testability; silencing lint findings without constitutional citation.
- Every default in `docs/CONFIG-SCHEMA.md` is asserted by T004 (catalogue match) AND T019 (loader applies it) — the dual assertion is the drift defence.
- Every named test in the chunk-contract Tests-required list is present: `TestServer_RejectsLoopback` (T043), `TestServer_RejectsPublic` (T045), `TestServer_AcceptsTailscaleCGNAT` (T048), `TestServer_RejectsArgonMemoryUnder256` (T061), `TestServer_RejectsAuditLogOutsideStateDir` (T071), `TestServer_RejectsUnknownField` (T031), `TestServer_FullMinimalConfig` (T017), `TestServer_FullMaximalConfig` (T018).
- Final-phase gates (per the user-supplied input + SDD-06.md Prompt 5): T089 (`magex format:fix`), T090 (`magex lint`), T091 (`magex test:race`), T092 (`go test -fuzz=FuzzServerTOML -fuzztime=60s ./internal/config/`), T093 (coverage ≥95%).
