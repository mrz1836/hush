---

description: "Task list for SDD-14 — CLI root + server-facing subcommands (serve, health, version, revoke)"
---

# Tasks: CLI Root and Server-Facing Subcommands

**Input**: Design documents from `/Users/mrz/projects/hush/specs/014-cli-root-and-server-cmds/`
**Prerequisites**: plan.md (loaded), spec.md (loaded), research.md (loaded), data-model.md (loaded), contracts/cli.md (loaded), quickstart.md (loaded)

**Tests**: TDD-mandatory per Constitution VIII. Every behaviour contract gets a test-writing task BEFORE its implementation task. Coverage target ≥ 85% on `internal/cli`; passphrase-resolution and sentinel-leak paths reach 100%.

**Organization**: Tasks are grouped by user story (P1 → P4) per spec.md. Setup + Foundational phases block all stories; the four user stories within Phase 3+ are independently testable once Foundational completes.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: US1=`serve`, US2=`health`, US3=`revoke`, US4=`version`
- File paths in descriptions are absolute under `/Users/mrz/projects/hush/`

## Path Conventions

- Single Go module at repo root (`github.com/mrz1836/hush`)
- New packages: `cmd/hush/` (binary entry) + `internal/cli/` (logic + tests)
- Locked file list per [plan.md §Source Code](./plan.md) — no files outside this list except deferred docs (PACKAGE-MAP, AC-MATRIX, SDD-PLAYBOOK)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Pull in the two new dependencies (`cobra` direct, `creack/pty` test-only) and lay down the package scaffolding so every subsequent task can land its file in place.

- [ ] T001 Run `go get github.com/spf13/cobra@v1.8.1` from repo root; verify `go.mod` records it as a direct dep and `go.sum` is updated
- [ ] T002 Run `go get github.com/creack/pty@v1.1.21` from repo root; verify it lands as an indirect dep (will become direct once `serve_test.go` imports it under no build tag)
- [ ] T003 [P] Create `cmd/hush/` directory with placeholder `cmd/hush/main.go` (package main; empty body — filled in Phase 2 once `cli.Execute` exists)
- [ ] T004 [P] Create `internal/cli/` directory and `internal/cli/doc.go` documenting the package purpose + Constitution VII/IX/X/XI principle map (matches `internal/server/doc.go` style)
- [ ] T005 [P] Create `internal/cli/coverage_test.go` with the per-file coverage assertion harness (matches `internal/server/coverage_test.go`, `internal/vault/coverage_test.go`); target ≥ 85%

**Checkpoint**: Deps in `go.mod`/`go.sum`; empty package skeleton compiles; `go test ./internal/cli/` runs (zero tests) without error.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The exit-code constants, output formatter, persistent global flags, and root command surface that every user story consumes. NO user-story work begins until this phase completes.

**⚠️ CRITICAL**: User Stories 1-4 cannot start until Phase 2 is done.

### Tests First (Foundational)

- [ ] T006 [P] Write `internal/cli/root_test.go::TestExitCodes_ConstantValues` asserting `ExitOK=0, ExitErr=1, ExitInputErr=2, ExitAuth=3, ExitNotFound=4, ExitPerm=5, ExitConfigStale=78` (FR-005, [contracts/cli.md §3](./contracts/cli.md))
- [ ] T007 [P] Write `internal/cli/root_test.go::TestExitCodes_NoStaleConfigInThisChunk` asserting no `mapErr` input maps to `ExitConfigStale` (FR-005 last sentence; [data-model.md §1](./data-model.md))
- [ ] T008 [P] Write `internal/cli/root_test.go::TestExitCodes_AllSentinelsCovered` table-driven over the locked sentinel sets in [research.md §9](./research.md) (config, token, vault, server, sign, os.ErrPermission); each maps to its expected non-default code
- [ ] T009 [P] Write `internal/cli/output_test.go::TestOutput_TTYPicksText` (FR-003) — a `Stream` with `isTTY=true` calling `Auto(text, jsonV)` writes `text`, not JSON
- [ ] T010 [P] Write `internal/cli/output_test.go::TestOutput_NonTTYPicksJSON` (FR-003) — a `Stream` with `isTTY=false` writes the JSON encoding of `jsonV`, not `text`
- [ ] T011 [P] Write `internal/cli/output_test.go::TestOutput_NoColorStripsANSI` (FR-004, SC-006) — feeding `\x1b[31mred\x1b[0m` through `WriteText` with `noColor=true` emits only `red`
- [ ] T012 [P] Write `internal/cli/output_test.go::TestOutput_PerStreamDecision` (Edge Case "Output context detection") — stdout-TTY + stderr-pipe → text on stdout, JSON-or-plain on stderr; diagnostics never bleed onto stdout JSON
- [ ] T013 [P] Write `internal/cli/output_test.go::TestOutput_JSONIndentOnTTY` ([research.md §5](./research.md)) — JSON on a TTY uses `MarshalIndent("", "  ")`; on a pipe uses `Marshal`; both end in single trailing `\n`
- [ ] T014 [P] Write `internal/cli/root_test.go::TestRoot_GlobalFlagsWired` (FR-002, [contracts/cli.md §2](./contracts/cli.md)) — every subcommand inherits `--config/-c`, `--verbose/-v`, `--quiet/-q`, `--no-color`; defaults match the contract
- [ ] T015 [P] Write `internal/cli/root_test.go::TestRoot_VerboseQuietConflict_ExitInputErr` (Edge Case "Verbose vs quiet conflict") — both flags set → `ExitInputErr` with literal text `"--verbose and --quiet are mutually exclusive"`
- [ ] T016 [P] Write `internal/cli/root_test.go::TestRoot_ConfigUnreadable_ExitInputErr` (Edge Case "Config file missing or unreadable") — `--config` pointing at a nonexistent path → `ExitInputErr` with a message naming the file
- [ ] T017 [P] Write `internal/cli/root_test.go::TestNoViperImport` — static AST scan of `cmd/hush/...` and `internal/cli/...` asserts no file imports `github.com/spf13/viper` (Constitution VII; [plan.md Constitution Check VII](./plan.md))
- [ ] T018 [P] Write `internal/cli/root_test.go::TestExecute_PropagatesContextCancellation` (Constitution IX) — `Execute(ctx)` with a pre-cancelled ctx returns promptly with `ExitErr`

### Implementation (Foundational)

- [ ] T019 [P] Implement `internal/cli/exit_codes.go`: declare seven `int` package-level constants (`ExitOK..ExitConfigStale`); add unexported `mapErr(err error) int` walking `errors.Is` against the sentinel sets in [research.md §9](./research.md); default → `ExitErr`; `nil` → `ExitOK`; **never** returns `78`
- [ ] T020 [P] Implement `internal/cli/output.go`: `type Stream struct { w io.Writer; isTTY bool; noColor bool }`; `NewStream(w, isTTY, noColor)`, `StreamFor(*os.File, noColor)` using `term.IsTerminal(f.Fd())`; methods `WriteText`, `WriteJSON`, `Auto`; ANSI-strip regexp `\x1b\[[0-9;]*m` ([research.md §5](./research.md))
- [ ] T021 Implement `internal/cli/flags.go`: persistent-flag registration (`--config/-c`, `--verbose/-v`, `--quiet/-q`, `--no-color`); typed getters; `ErrFlagConflict` sentinel; verbose-quiet conflict check (depends on T019 for `ExitInputErr` mapping)
- [ ] T022 Implement `internal/cli/root.go`: `rootCmd` constructor (cobra), `func Execute(ctx context.Context) int`; wires persistent flags via T021; sets shared `OutputContext` on cobra `Context`; runs flag-conflict gate before dispatch; routes `RunE` errors through `mapErr`. **No `init()` function.** (depends on T019, T020, T021)
- [ ] T023 Implement `cmd/hush/main.go`: two-line body — `os.Exit(cli.Execute(context.Background()))`. No business logic per `docs/PACKAGE-MAP.md` (depends on T022)

**Checkpoint**: Foundational phase complete. `go test -race ./internal/cli/...` for T006-T018 passes (tests fail-then-pass against T019-T023). `./hush --help` (after `go build ./cmd/hush`) lists no subcommands yet but renders the global flags. User-story phases can now proceed in parallel.

---

## Phase 3: User Story 1 — `hush serve` (Priority: P1) 🎯 MVP

**Goal**: Operator runs `hush serve` to bring the vault online; passphrase resolved via `stdin pipe → TTY prompt → ExitInputErr`, never from any environment variable; SIGTERM/SIGINT trigger graceful shutdown via the chassis's existing context cancellation.

**Independent Test**: Pipe a known passphrase to `./hush serve`, hit `/hz` from another process, verify 200 OK; send SIGTERM and verify clean exit (`ExitOK`). Asserted by `TestServe_StartAndShutdown` (integration).

### Tests First (US1)

> **WRITE THESE FIRST. ENSURE THEY FAIL BEFORE IMPLEMENTATION.**

- [ ] T024 [P] [US1] Write `internal/cli/serve_test.go::TestServe_PassphraseFromStdinPipe` (FR-008, FR-008a) — feed `"correct horse\n"` via a pipe, assert `resolvePassphrase` returns a `*securebytes.SecureBytes` whose contents equal `"correct horse"` (`\n` stripped, all other bytes preserved); cover bare `\n`, bare `\r\n`, two trailing `\n`s (only one stripped), leading-whitespace preservation
- [ ] T025 [P] [US1] Write `internal/cli/serve_test.go::TestServe_PassphraseFromTTYPrompt` using `github.com/creack/pty` (FR-008, [research.md §3](./research.md)) — open a real PTY, write `"correct horse\n"` to the master, assert `resolvePassphrase` returns the secret AND that **no byte of the typed passphrase appears on the slave's terminal output capture** (no-echo property)
- [ ] T026 [P] [US1] Write `internal/cli/serve_test.go::TestServe_NoStdinNoTTY_ExitInputErr` (FR-008 third clause) — stdin neither pipe nor TTY → `errNoPassphraseSource` → `ExitInputErr` with literal text `"no passphrase source: stdin is not a pipe and is not a terminal"` ([contracts/cli.md §6](./contracts/cli.md))
- [ ] T027 [P] [US1] Write `internal/cli/serve_test.go::TestServe_NeverReadsEnv` — AST scan of every `internal/cli/*.go` (excluding `*_test.go`) using `go/parser`; fails on any `*ast.SelectorExpr{X: "os", Sel: "Getenv"}` reference (FR-009, [research.md §13](./research.md))
- [ ] T028 [P] [US1] Write `internal/cli/serve_test.go::TestServe_ZeroByteStdinPipe` (Edge Case "Passphrase source ambiguity on `serve`") — zero-byte pipe + no TTY → `ExitInputErr` (does NOT fall through to TTY when no TTY is attached)
- [ ] T029 [P] [US1] Write `internal/cli/serve_test.go::TestServe_OutputNoSentinel` (FR-007, FR-012, SC-004) — plant `SECRET_SHOULD_NEVER_APPEAR_14` as the piped passphrase; capture every byte of stdout, stderr, and any audit-log file; assert via `internal/testutil.AssertSentinelAbsent` that the marker never appears
- [ ] T030 [P] [US1] Write `internal/cli/serve_test.go::TestServe_DestroysPassphraseAndSeedOnExit` (Constitution X; [data-model.md §7](./data-model.md)) — instrumented `*securebytes.SecureBytes` records `Destroy()` calls; assert both the passphrase and the master seed are zeroed before `runServe` returns
- [ ] T031 [P] [US1] Write `internal/cli/serve_test.go::TestServe_StartAndShutdown` under `//go:build integration` ([research.md §12](./research.md)) — start `runServe(ctx, deps)` in a goroutine with a `testutil.DiscordStub` approver, in-memory audit mirror, free-port listener, `t.TempDir()` vault; poll `GET /hz` until 200 OK or 2 s timeout; `cancel()`; assert err-channel receives `nil` within 5 s; assert sentinel absent on captured output
- [ ] T032 [P] [US1] Write `internal/cli/serve_test.go::TestServe_SIGTERMGracefulShutdown` under `//go:build integration` (FR-011, [contracts/cli.md §4.1](./contracts/cli.md)) — start `runServe`; once `/hz` responds, send `SIGTERM` to the test process; assert the goroutine returns `nil` and exit code maps to `ExitOK`
- [ ] T033 [P] [US1] Write `internal/cli/serve_test.go::TestServe_BadPassphrase_ExitAuth` (FR-010, [data-model.md §1](./data-model.md)) — wire a vault whose decryption surfaces `vault.ErrAuthFailed`; assert `runServe` returns an error that maps to `ExitAuth` (3)
- [ ] T034 [P] [US1] Write `internal/cli/serve_test.go::TestServe_MissingConfig_ExitNotFound` ([data-model.md §1](./data-model.md)) — `--config` pointing at nonexistent path → `ExitNotFound` (4); existing-but-unreadable → `ExitInputErr` (2)
- [ ] T035 [P] [US1] Write `internal/cli/serve_test.go::TestServe_BindPermissionDenied_ExitPerm` ([data-model.md §1](./data-model.md), [research.md §9](./research.md)) — chassis surfaces `os.ErrPermission` or `server.ErrFileModeLoose` → `ExitPerm` (5)
- [ ] T036 [P] [US1] Write `internal/cli/serve_test.go::TestLoadBotToken_ItemNameValidation` ([research.md §11](./research.md)) — invalid item-name patterns (containing `;`, `&`, `$`, spaces, > 128 chars) return error before any subprocess invocation
- [ ] T037 [P] [US1] Write `internal/cli/serve_test.go::TestLoadBotToken_StripsTrailingNewlines` ([research.md §11](./research.md)) — keychain helper output `"abc123\n"` is unwrapped to `"abc123"` inside the returned `*securebytes.SecureBytes`

### Implementation (US1)

- [ ] T038 [US1] Implement `internal/cli/serve.go::resolvePassphrase` (the testable seam from [research.md §6](./research.md)): pipe path uses `io.ReadAll(in)` + `stripPOSIXLineEnd`; TTY path writes `"Vault passphrase: "` to the prompt writer + `term.ReadPassword(int(in.Fd()))`; missing path returns `errNoPassphraseSource`; **no `os.Getenv` anywhere on the path** (depends on T024-T028)
- [ ] T039 [US1] Implement `internal/cli/serve.go::stripPOSIXLineEnd` (6-line helper from [research.md §7](./research.md)) — strip exactly one trailing `\r\n` or one trailing `\n`; preserve all other bytes verbatim
- [ ] T040 [US1] Implement `internal/cli/serve.go::loadBotToken` (60-LOC inline helper, [research.md §11](./research.md)) — Darwin: `security find-generic-password -s <item> -w`; Linux: `secret-tool lookup service hush attribute <item>`; item-name regex `^[a-zA-Z0-9._-]{1,128}$`; 1 KiB stdout cap; wrap in `*securebytes.SecureBytes`; map errors to `errBotTokenMissing`/`errBotTokenSubprocess` (depends on T036, T037)
- [ ] T041 [US1] Implement `internal/cli/serve.go::serveDeps` struct + `runServe(ctx, deps) error` skeleton ([research.md §10](./research.md)): the 11-step composition (config load → passphrase → master seed → subkeys → audit writer → approver → token store → chassis `New` → `RegisterHandlers` → `signal.NotifyContext(SIGINT/SIGTERM)` → `srv.Run`); each step's error is wrapped with `%w` so `mapErr` can match (depends on T038, T039, T040)
- [ ] T042 [US1] Implement `internal/cli/serve.go::serveCmd` (the cobra command) — `RunE` constructs production `serveDeps{passphraseSource: resolvePassphrase, approverFactory: newProductionBotApprover}` and calls `runServe`; register on `rootCmd` from T022 (depends on T041)
- [ ] T043 [US1] Wire `serveCmd` into `rootCmd` via `internal/cli/root.go` (subcommand registration entry point; unexported); verify `./hush serve --help` renders (depends on T042)
- [ ] T044 [US1] Verify `internal/cli/serve.go` and helpers compile, tests T024-T037 pass, integration tests T031-T032 pass under `//go:build integration`, and `TestServe_NeverReadsEnv` (T027) confirms zero `os.Getenv` references

**Checkpoint**: User Story 1 complete and independently testable. `./hush serve` runs end-to-end against a real vault. AC-1 timing target verifiable: server accepts `/hz` within 5 s of `serve` invocation excluding passphrase delivery.

---

## Phase 4: User Story 2 — `hush health` (Priority: P2)

**Goal**: Operator runs `hush health` from any trusted-network host; sees a per-dimension summary in text-on-TTY or JSON-on-pipe; partial-health → `ExitErr` with full summary; connection-refused → literal `"could not connect to hush server at <addr>: connection refused"` and `ExitErr`; 5-second total-request timeout (FR-015a).

**Independent Test**: Start `./hush serve`, run `./hush health`, observe success summary and `ExitOK`. Stop server, run again, observe explicit refused message and `ExitErr`.

### Tests First (US2)

> **WRITE THESE FIRST. ENSURE THEY FAIL BEFORE IMPLEMENTATION.**

- [ ] T045 [P] [US2] Write `internal/cli/health_test.go::TestHealth_HappyPath` (FR-013, US2 Acceptance Scenario 1) — `httptest.Server` returns the locked 8-key health JSON with all green dimensions; TTY mode prints the two-column table; non-TTY mode prints the JSON byte-for-byte; exit code `ExitOK`
- [ ] T046 [P] [US2] Write `internal/cli/health_test.go::TestHealth_NonTTY_JSONByteForByte` (FR-013, [contracts/cli.md §5.2](./contracts/cli.md)) — non-TTY response body is emitted verbatim (no re-marshaling, no field suppression, no added wrapper)
- [ ] T047 [P] [US2] Write `internal/cli/health_test.go::TestHealth_PartialHealth_ExitErr` (FR-017a, US2 Acceptance Scenario 6) — server returns `discord_connected=false`; CLI renders the full summary AND exits `ExitErr` (1)
- [ ] T048 [P] [US2] Write `internal/cli/health_test.go::TestHealth_ConnectionRefusedExplicitMessage` (FR-014, [contracts/cli.md §6](./contracts/cli.md)) — point at a closed port; assert stderr contains the literal `"could not connect to hush server at <addr>: connection refused"`; exit code `ExitErr`
- [ ] T049 [P] [US2] Write `internal/cli/health_test.go::TestHealth_TimeoutMessage` (FR-015a, [contracts/cli.md §6](./contracts/cli.md)) — `httptest.Server` that sleeps 6 s; assert stderr contains the literal `"could not connect to hush server at <addr>: timeout after 5s"`; exit code `ExitErr`
- [ ] T050 [P] [US2] Write `internal/cli/health_test.go::TestHealth_OtherTransportFailures` (FR-015) — DNS failure / unroutable host → message names the address + `<classifier>` in {`no route`, `name resolution failed`, `EOF`}; `ExitErr`
- [ ] T051 [P] [US2] Write `internal/cli/health_test.go::TestHealth_5xxServerError_ExitErr` ([data-model.md §1](./data-model.md)) — 500 response → `ExitErr`; body excerpt with control chars sanitised; never includes any auth header or signed payload
- [ ] T052 [P] [US2] Write `internal/cli/health_test.go::TestHealth_NoAuthRequired` (FR-016) — assert the outgoing request has no `Authorization` header and no signed-request envelope
- [ ] T053 [P] [US2] Write `internal/cli/health_test.go::TestHealth_OutputNoSentinel` (FR-007, FR-017, SC-004) — server response body contains `SECRET_SHOULD_NEVER_APPEAR_14`; CLI captures stdout/stderr; `testutil.AssertSentinelAbsent` confirms no leak (the body is rendered, but the sentinel marker would be flagged by upstream — verify the redaction story holds for any byte-pattern match)
- [ ] T054 [P] [US2] Write `internal/cli/health_test.go::TestHealth_VerboseTrace` (FR-002a) — `--verbose` writes the URL hit, HTTP status, response-body byte count to stderr; never the body itself unless the format permits

### Implementation (US2)

- [ ] T055 [US2] Implement `internal/cli/health.go::healthCmd` — cobra command with optional `--server <url>` flag (defaults to loaded config's `ListenAddr` + `path_prefix`); `RunE` calls unexported `runHealth(ctx, w *Stream, errW *Stream, server string) error` (depends on T020, T022)
- [ ] T056 [US2] Implement `internal/cli/health.go::runHealth` — construct one `*http.Client{Timeout: 5*time.Second, Transport: &http.Transport{DisableKeepAlives: true, MaxIdleConnsPerHost: 1}}` per call ([research.md §4](./research.md)); `GET <server>/h/<prefix>/hz`; classify errors via `errors.Is(context.DeadlineExceeded)` / `syscall.ECONNREFUSED` / DNS / EOF; render via `Stream.Auto` (depends on T055)
- [ ] T057 [US2] Implement `internal/cli/health.go::renderTextSummary` — fixed two-column table in stable JSON-key order; healthy = green checkmark (suppressed under `--no-color`), unhealthy = red cross
- [ ] T058 [US2] Implement `internal/cli/health.go::evaluateHealth(snap HealthSnapshot) bool` — true iff `Status=="ok"` AND `DiscordConnected` AND `ConfigValid` AND `VaultLoaded` AND `ClockInSync` ([data-model.md §6](./data-model.md))
- [ ] T059 [US2] Wire `healthCmd` into `rootCmd`; verify `./hush health --help` renders (depends on T055-T058)
- [ ] T060 [US2] Verify tests T045-T054 pass; coverage on `internal/cli/health.go` ≥ 85%

**Checkpoint**: User Story 2 complete. `./hush health` works against a live `serve`, against a closed port, and against a hung server (timeout path).

---

## Phase 5: User Story 3 — `hush revoke` (Priority: P3)

**Goal**: Operator runs `hush revoke --server <addr> --jti <uuid>`; CLI signs `{jti, nonce, timestamp}` via SDD-08, POSTs to `/revoke` with 5-second timeout, maps HTTP status to exit code (200→0, 401/403→3, 404→4, 5xx/network→1).

**Independent Test**: Issue a token; run `./hush revoke --server <addr> --jti <id>`; verify success exit and that a subsequent token use returns auth failure.

### Tests First (US3)

> **WRITE THESE FIRST. ENSURE THEY FAIL BEFORE IMPLEMENTATION.**

- [ ] T061 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_SignedRequestPosted` (FR-021, US3 Acceptance Scenario 1) — `httptest.Server` captures the POST body and signature header; assert canonical JSON shape `{jti, nonce, timestamp}`; assert signature verifies against the test client public key via `sign.Verify`; CLI exits `ExitOK`
- [ ] T062 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_BadStatusMapsToExitCode` table-driven over `{200→ExitOK, 401→ExitAuth, 403→ExitAuth, 404→ExitNotFound, 500→ExitErr, 503→ExitErr}` ([contracts/cli.md §4.4](./contracts/cli.md))
- [ ] T063 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_MissingFlags_ExitInputErr` (FR-020, [contracts/cli.md §6](./contracts/cli.md)) — missing `--server` → literal `"missing required flag: --server"` + `ExitInputErr`; missing `--jti` → literal `"missing required flag: --jti"` + `ExitInputErr`
- [ ] T064 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_MalformedJTI_ExitInputErr` ([data-model.md §4](./data-model.md), [contracts/cli.md §6](./contracts/cli.md)) — `--jti deadbeef` → literal `"invalid --jti: must be a UUID"` + `ExitInputErr`
- [ ] T065 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_ConnectionRefused_ExitErr` (FR-025) — closed port → literal `"could not connect to hush server at <addr>: connection refused"` + `ExitErr`
- [ ] T066 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_Timeout_ExitErr` (FR-015a) — `httptest.Server` sleeps 6 s → timeout-after-5s message + `ExitErr`
- [ ] T067 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_5xxBodyExcerptSanitized` ([contracts/cli.md §4.4](./contracts/cli.md)) — 503 response with control-character-laden body; CLI prints `"server returned 503: <excerpt>"` with control chars replaced by `?`; first 256 bytes only; raw signed-request payload NEVER appears
- [ ] T068 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_OutputNoSentinel` (FR-007, FR-026, SC-004) — plant `SECRET_SHOULD_NEVER_APPEAR_14` as both the piped passphrase AND a server-response field; capture stdout/stderr; `testutil.AssertSentinelAbsent` confirms no leak (sentinel-leak path reaches 100% coverage per [plan.md §Coverage](./plan.md))
- [ ] T069 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_NeverPrintsSigningKey` (FR-026) — verbose mode trace prints the canonical JSON bytes that were signed but **NOT** the signature itself or any byte of the signing key
- [ ] T070 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_TTYSuccessMessage_NonTTY_JSONShape` ([contracts/cli.md §5.3](./contracts/cli.md)) — TTY: `"revoked jti=<id>"`; non-TTY: `{"revoked":"<id>"}`
- [ ] T071 [P] [US3] Write `internal/cli/revoke_test.go::TestRevoke_NonceUniquePerCall` (replay protection, FR-021) — two consecutive `runRevoke` calls produce distinct nonces; nonce is 32 random bytes hex-encoded from `crypto/rand.Read`

### Implementation (US3)

- [ ] T072 [US3] Implement `internal/cli/revoke.go::revokeCmd` — cobra command with required `--server <url>` and `--jti <uuid>` flags (cobra `MarkFlagRequired` plus explicit literal-text error messages from [contracts/cli.md §6](./contracts/cli.md)); `RunE` calls `runRevoke(ctx, ...)` (depends on T022)
- [ ] T073 [US3] Implement `internal/cli/revoke.go::runRevoke` — UUID-validate `--jti` against the same regex as `internal/server::getRequestIDRe`; resolve passphrase via the **same `passphraseSource` seam** used by `serve` (T038); derive client key via `keys.DeriveClientKey(masterSeed, machineIndex)`; build `{jti, nonce, timestamp}` (32 random hex bytes from `crypto/rand`); `sign.CanonicalJSON` + `sign.Sign`; POST with the same 5 s `*http.Client` shape as `health` ([research.md §4](./research.md)); map status → exit code (depends on T038, T039, T056-pattern)
- [ ] T074 [US3] Implement `internal/cli/revoke.go::renderResult` — TTY: `"revoked jti=<id>"`; non-TTY: `{"revoked":"<id>"}` via `Stream.Auto`
- [ ] T075 [US3] Wire `revokeCmd` into `rootCmd`; verify `./hush revoke --help` renders (depends on T072-T074)
- [ ] T076 [US3] Verify tests T061-T071 pass; coverage on `internal/cli/revoke.go` ≥ 85%

**Checkpoint**: User Story 3 complete. `./hush revoke` issues signed requests, maps every documented HTTP status to its locked exit code, and never leaks signing-key bytes.

---

## Phase 6: User Story 4 — `hush version` (Priority: P4)

**Goal**: Operator runs `hush version` to identify the binary; build-time-injected `Version`/`Commit`/`Date` rendered as human lines on TTY or the locked 3-key JSON shape on a pipe; development builds show `dev`/`unknown`/`unknown` placeholders.

**Independent Test**: Run `hush version` on a binary built with `-ldflags "-X .../cli.Version=v0.1.0-test"`; verify the output contains `v0.1.0-test`. Run `hush version` on a dev build; verify it contains `dev`.

### Tests First (US4)

> **WRITE THESE FIRST. ENSURE THEY FAIL BEFORE IMPLEMENTATION.**

- [ ] T077 [P] [US4] Write `internal/cli/version_test.go::TestExecute_VersionPrintsBuildVersion` (FR-018, US4 Acceptance Scenario 1) — set `cli.Version="v0.1.0", cli.Commit="abc1234", cli.Date="2026-05-01T12:34:56Z"`; run `version` subcommand; TTY output contains all three values; non-TTY output equals the locked JSON
- [ ] T078 [P] [US4] Write `internal/cli/version_test.go::TestVersion_NonTTYJSONShape_ThreeKeys` (FR-019a, [contracts/cli.md §5.1](./contracts/cli.md)) — assert non-TTY output is exactly `{"version":"...","commit":"...","date":"..."}` (no other keys, in that specific order)
- [ ] T079 [P] [US4] Write `internal/cli/version_test.go::TestVersion_DevPlaceholderWhenUnset` (FR-019, US4 Acceptance Scenario 2) — defaults `Version="dev", Commit="unknown", Date="unknown"`; non-TTY JSON contains those literal strings; TTY output contains them as well
- [ ] T080 [P] [US4] Write `internal/cli/version_test.go::TestVersion_AlwaysExitsOK` ([contracts/cli.md §4.3](./contracts/cli.md)) — `version` has no failure mode in this chunk; `ExitOK` regardless of build state
- [ ] T081 [P] [US4] Write `internal/cli/version_test.go::TestVersion_NoColorIrrelevant` — `version` output has no ANSI styling regardless of `--no-color`; flag is accepted without error

### Implementation (US4)

- [ ] T082 [US4] Implement `internal/cli/version.go::versionCmd` — cobra command; `RunE` reads package-level `var Version, Commit, Date string` (defaults `"dev"`, `"unknown"`, `"unknown"`); renders via `Stream.Auto` against the locked text/JSON shapes from [contracts/cli.md §4.3 / §5.1](./contracts/cli.md) (depends on T020, T022)
- [ ] T083 [US4] Wire `versionCmd` into `rootCmd`; verify `./hush version --help` renders (depends on T082)
- [ ] T084 [US4] Verify tests T077-T081 pass; verify `go build -ldflags "-X github.com/mrz1836/hush/internal/cli.Version=v0.1.0-test" -o ./hush ./cmd/hush && ./hush version | grep "v0.1.0-test"` succeeds

**Checkpoint**: User Story 4 complete. All four subcommands runnable via `--help`; build version injection verified.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Final formatting/lint/test/coverage gates per [SDD-14 Prompt 5 step 1-2](../../docs/sdd/SDD-14.md), documentation updates per Prompt 5 steps 7-9, and the single combined commit.

- [ ] T085 Run `magex format:fix` from repo root; commit zero formatting drift
- [ ] T086 Run `magex lint` from repo root; resolve any new lint findings on `cmd/hush/...` and `internal/cli/...`
- [ ] T087 Run `magex test:race` from repo root; **all unit tests must pass race-clean**
- [ ] T088 Run `magex test:race -tags=integration` from repo root; **`TestServe_StartAndShutdown` (T031) and `TestServe_SIGTERMGracefulShutdown` (T032) must pass race-clean**
- [ ] T089 Run `go test -cover ./internal/cli/`; verify coverage ≥ 85%; verify the passphrase-resolution path (`resolvePassphrase`, `stripPOSIXLineEnd`, T038-T039) and sentinel-leak tests (T029, T053, T068) reach 100% line coverage on their critical statements
- [ ] T090 Smoke-test each subcommand's `--help` renders successfully: `./hush --help`, `./hush serve --help`, `./hush health --help`, `./hush version --help`, `./hush revoke --help` (per [quickstart.md §7](./quickstart.md))
- [ ] T091 Confirm `grep -RnE 'os\.Getenv' internal/cli/` returns zero matches (per [quickstart.md §8(b)](./quickstart.md), FR-009 verifiability)
- [ ] T092 Confirm `grep -RnE '"github.com/spf13/viper"' cmd/ internal/` returns zero matches (per [quickstart.md §8(a)](./quickstart.md), Constitution VII)
- [ ] T093 Confirm `govulncheck ./...` reports no advisories on the new tree (NFR-3, [quickstart.md §9](./quickstart.md))
- [ ] T094 Append "Exported API — locked at SDD-14" section to `docs/PACKAGE-MAP.md` under `cmd/hush` + `internal/cli` listing the locked API: `cmd/hush.main`, `cli.Execute(ctx context.Context) int`, the seven `Exit*` constants
- [ ] T095 Update `docs/AC-MATRIX.md` AC-1 row with the new test file paths (`internal/cli/serve_test.go`, `internal/cli/health_test.go`, `internal/cli/revoke_test.go`, `internal/cli/version_test.go`, `internal/cli/output_test.go`, `internal/cli/root_test.go`)
- [ ] T096 Mark SDD-14 status `done` in `docs/SDD-PLAYBOOK.md`
- [ ] T097 Single combined commit per [SDD-14 Prompt 5 step 9](../../docs/sdd/SDD-14.md): `git add cmd/hush/ internal/cli/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md specs/014-cli-root-and-server-cmds/tasks.md go.mod go.sum && git commit -m "feat(cli): root + serve/health/version/revoke subcommands (SDD-14)"`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately.
- **Foundational (Phase 2)**: Depends on Setup. **BLOCKS all user stories** — `exit_codes.go`, `output.go`, `flags.go`, `root.go` are consumed by every subcommand.
- **User Stories (Phase 3-6)**: All depend on Foundational. Once T023 lands:
  - US1 (`serve`) — P1, MVP gate.
  - US2 (`health`) — P2, can proceed in parallel with US1.
  - US3 (`revoke`) — P3, can proceed in parallel; reuses the passphrase-source seam from US1.
  - US4 (`version`) — P4, can proceed in parallel; trivially independent.
- **Polish (Phase 7)**: Depends on Foundational + every user story being merged.

### User Story Dependencies

- **US1 (`serve`)**: Independent of US2/US3/US4. Depends only on Foundational. **MVP gate (AC-1)**.
- **US2 (`health`)**: Independent of US1's source code, but the integration smoke (`hush health` against a real `hush serve`) requires US1 implemented to test end-to-end.
- **US3 (`revoke`)**: Reuses `passphraseSource` (T038) implemented by US1 — sequential dependency on T038 within `internal/cli/serve.go`. **All other US3 logic is independent.**
- **US4 (`version`)**: Fully independent — touches only `internal/cli/version.go` and the package-level `Version`/`Commit`/`Date` vars.

### Within Each User Story

- **Tests written and FAIL before implementation** (TDD-mandatory per Constitution VIII; T024-T037 fail before T038-T044 land; T045-T054 fail before T055-T060; etc.).
- Helpers (`stripPOSIXLineEnd`, `loadBotToken`) before consumers (`resolvePassphrase`, `runServe`).
- `runX` business logic before the cobra `xCmd` shell that calls it.
- Cobra command registration (`rootCmd.AddCommand`) is the last step in each story so `--help` validation in T090 reflects the final state.

### Parallel Opportunities

- **Setup [P]**: T003, T004, T005 run in parallel (different files, no dep on each other).
- **Foundational tests [P]**: T006-T018 are all in parallel (different test functions, possibly different test files). They write tests that fail until T019-T023 land.
- **Foundational implementation**: T019, T020 are [P]; T021 depends on T019; T022 depends on T019, T020, T021; T023 depends on T022 (sequential tail).
- **Cross-story parallelism**: Once T023 (Foundational) is done, **US1, US2, US3, US4 can be worked by four developers/agents simultaneously**. Each story's test+implementation tasks are internally [P]-marked.
- **Within US1**: T024-T037 are all [P]; T038-T040 are sequential helper-then-consumer; T041 depends on T038-T040; T042-T043-T044 are sequential.
- **Within US2**: T045-T054 are all [P]; T055-T058 are mostly sequential within `health.go`; T059-T060 trail.
- **Within US3**: T061-T071 are all [P]; T072-T076 are sequential within `revoke.go`.
- **Within US4**: T077-T081 are all [P]; T082-T084 are sequential.
- **Polish (Phase 7)**: T085-T093 mostly sequential (each depends on the previous gate); T094-T096 are [P] (different files); T097 trails everything.

---

## Parallel Example: User Story 1 (`serve`) tests

```bash
# Launch all US1 tests in parallel before any implementation lands:
Task: "Write internal/cli/serve_test.go::TestServe_PassphraseFromStdinPipe"
Task: "Write internal/cli/serve_test.go::TestServe_PassphraseFromTTYPrompt (creack/pty)"
Task: "Write internal/cli/serve_test.go::TestServe_NoStdinNoTTY_ExitInputErr"
Task: "Write internal/cli/serve_test.go::TestServe_NeverReadsEnv (AST scan)"
Task: "Write internal/cli/serve_test.go::TestServe_ZeroByteStdinPipe"
Task: "Write internal/cli/serve_test.go::TestServe_OutputNoSentinel"
Task: "Write internal/cli/serve_test.go::TestServe_DestroysPassphraseAndSeedOnExit"
Task: "Write internal/cli/serve_test.go::TestServe_StartAndShutdown (//go:build integration)"
Task: "Write internal/cli/serve_test.go::TestServe_SIGTERMGracefulShutdown (//go:build integration)"
Task: "Write internal/cli/serve_test.go::TestServe_BadPassphrase_ExitAuth"
Task: "Write internal/cli/serve_test.go::TestServe_MissingConfig_ExitNotFound"
Task: "Write internal/cli/serve_test.go::TestServe_BindPermissionDenied_ExitPerm"
Task: "Write internal/cli/serve_test.go::TestLoadBotToken_ItemNameValidation"
Task: "Write internal/cli/serve_test.go::TestLoadBotToken_StripsTrailingNewlines"
```

---

## Parallel Example: Foundational test wave

```bash
# Once Setup is done, launch all 13 Foundational tests in parallel:
Task: "TestExitCodes_ConstantValues"
Task: "TestExitCodes_NoStaleConfigInThisChunk"
Task: "TestExitCodes_AllSentinelsCovered"
Task: "TestOutput_TTYPicksText"
Task: "TestOutput_NonTTYPicksJSON"
Task: "TestOutput_NoColorStripsANSI"
Task: "TestOutput_PerStreamDecision"
Task: "TestOutput_JSONIndentOnTTY"
Task: "TestRoot_GlobalFlagsWired"
Task: "TestRoot_VerboseQuietConflict_ExitInputErr"
Task: "TestRoot_ConfigUnreadable_ExitInputErr"
Task: "TestNoViperImport"
Task: "TestExecute_PropagatesContextCancellation"
```

---

## Implementation Strategy

### MVP First (User Story 1 — `serve`)

1. Complete Phase 1 (Setup): T001-T005.
2. Complete Phase 2 (Foundational): T006-T023.
3. Complete Phase 3 (US1 `serve`): T024-T044.
4. **STOP and VALIDATE**: AC-1 — `./hush serve` brings the vault online and `/hz` responds within 5 s of invocation (excluding passphrase delivery).
5. Run `magex test:race` and `magex test:race -tags=integration` to confirm the chassis composition is race-clean.
6. Demo / deploy if ready. **This is the v0.1.0 release gate.**

### Incremental Delivery

1. Setup + Foundational → skeleton ready.
2. Add US1 (`serve`) → MVP demo (AC-1 met).
3. Add US2 (`health`) → diagnostic tool ready; readiness probes can land.
4. Add US3 (`revoke`) → emergency token-invalidation lever ready (AC-4(c) met).
5. Add US4 (`version`) → operator triage tool ready.
6. Polish (Phase 7) → all gates green, single combined commit.

### Parallel Team Strategy

With four agents/developers after Foundational completes:

- **Agent A**: US1 (`serve`) — T024-T044 (longest path; chassis composition + integration tests)
- **Agent B**: US2 (`health`) — T045-T060
- **Agent C**: US3 (`revoke`) — T061-T076 (depends on T038 from Agent A; coordinate the `passphraseSource` seam early)
- **Agent D**: US4 (`version`) — T077-T084 (shortest path; can finish first)

Once all four merge, a single agent runs Phase 7 (T085-T097) and creates the combined commit.

---

## Notes

- [P] tasks operate on different files / different test functions with no shared state.
- [Story] label maps each user-story task back to spec.md priorities (US1=P1, US2=P2, US3=P3, US4=P4).
- TDD discipline: write the test, watch it fail, write the implementation, watch it pass. **Never invert.**
- Integration tests (T031, T032) live behind `//go:build integration` and only run under `magex test:race -tags=integration`.
- Coverage gate: `go test -cover ./internal/cli/` ≥ 85% — refuse to commit (T097) if below.
- The single combined commit (T097) is per [SDD-14 Prompt 5 step 9](../../docs/sdd/SDD-14.md). No intermediate commits during this chunk.
- The `creack/pty` dependency is test-only — never appears in the release binary's dependency closure.
- `os.Getenv` is **forbidden anywhere on the passphrase resolution path** (FR-009). Verified by T027 (AST scan) and T091 (final grep).
- Sentinel marker for leak tests: `SECRET_SHOULD_NEVER_APPEAR_14` (per [SDD-14 chunk doc](../../docs/sdd/SDD-14.md)).
- Locked exit-code values (`0, 1, 2, 3, 4, 5, 78`) are the public CLI contract — operators script against them. **Never remap; never add new codes without a constitutional amendment.**
