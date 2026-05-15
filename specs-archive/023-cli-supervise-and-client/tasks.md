---
description: "Task list for SDD-23 — hush supervise + hush client status + hush client refresh"
---

# Tasks: `hush supervise` + `hush client status` + `hush client refresh`

**Input**: Design documents from `/specs/023-cli-supervise-and-client/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/

**Tests**: **TDD-mandatory per Constitution VIII**. Every behaviour contract has a test-writing task BEFORE the implementation task. Coverage target ≥ 85 % on `internal/cli/supervise.go` + `internal/cli/client.go` (SC-023-10); ≥ 95 % on `internal/supervise/socket.go`.

**Organization**: Tasks are grouped by user story (priority order: P1 → P2 → P3) to enable independent implementation and testing.

## Format: `[ID] [P?] [Story?] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US5)
- Every task lists exact file paths

## Path Conventions

- Single Go module rooted at the repo root.
- Production CLI code: [internal/cli/](internal/cli/).
- Supervisor primitives (read/extend only): [internal/supervise/](internal/supervise/).
- Test files live next to the production files (`*_test.go`).
- Integration tests use the `//go:build integration` build tag, matching [internal/cli/serve_integration_test.go](internal/cli/serve_integration_test.go).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Wire the new sentinel taxonomy and cobra command tree skeleton so subsequent phases can register subcommands and `mapErr` resolves their exit codes.

- [X] T001 Add the five new sentinel errors to [internal/cli/exit_codes.go](internal/cli/exit_codes.go) and extend `mapErr` per data-model.md §5: `errInvalidGraceWindow` → `ExitInputErr`, `errSocketAmbiguous` → `ExitInputErr`, `errSocketUnreachable` → `ExitErr`, `errSupervisorRefused` → `ExitErr`, `errDuplicateSupervisor` → `ExitErr` (wraps `supervise.ErrPidLocked` with FR-023-6 message). Reserve `ExitConfigStale=78` for supervise-only use per cli-supervise.md §4.
- [X] T002 Add the test rows for the five new sentinels to [internal/cli/exit_codes_test.go](internal/cli/exit_codes_test.go) (mirror the existing table-driven `TestMapErr` shape).
- [X] T003 Register `newSuperviseCmd()` and `newClientCmd()` placeholders in [internal/cli/root.go](internal/cli/root.go) via `root.AddCommand(...)` in `Execute`. The constructors may return command skeletons whose `RunE` returns `errors.New("not implemented")` for now — Phase 3 fills them.

**Checkpoint**: `mapErr` knows the new sentinels; cobra knows the new subcommands; `go build ./...` succeeds. No behaviour yet.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Extend [internal/supervise/socket.go](internal/supervise/socket.go) with verb dispatch and add the two path-derivation helpers in the per-OS shims. These are shared by `hush supervise` (attaches refresh handler) and `hush client {status,refresh}` (auto-detects sockets), so they MUST land before any user story.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

### Tests (write FIRST, must FAIL before implementation)

- [X] T004 Add `TestSocket_VerbStatusReturnsStatusDocument`, `TestSocket_VerbRefreshInvokesHandler`, `TestSocket_VerbRefreshErrorIsSerialised`, `TestSocket_VerbRefreshErrorMultilineSerialisedAsOneLine`, `TestSocket_VerbRefreshHandlerUnwiredReturnsStableError`, `TestSocket_VerbUnrecognisedFallsBackToStatus`, `TestSocket_VerbStatusEmptyPayloadReturnsStatusDocument`, `TestSocket_RefreshHandlerCtxFiresOnServerCancel`, `TestSocket_AttachRefreshHandlerCalledTwicePanicsOrLastWinsLockedBehaviour` to [internal/supervise/socket_test.go](internal/supervise/socket_test.go) per socket-protocol.md §6.
- [X] T005 [P] Add `TestSocketPathForSupervisor_DerivesPlatformPath`, `TestEnumerateSupervisorSockets_ListsMatchingFiles`, `TestEnumerateSupervisorSockets_EmptyDirReturnsEmptySlice`, `TestEnumerateSupervisorSockets_MissingDirReturnsEmptySlice` to [internal/supervise/socket_darwin_test.go](internal/supervise/socket_darwin_test.go).
- [X] T006 [P] Add the same four tests (Linux scheme variant) to [internal/supervise/socket_linux_test.go](internal/supervise/socket_linux_test.go).

### Implementation

- [X] T007 Implement verb dispatch + package-private `(*StatusServer).attachRefreshHandler(handler func(ctx context.Context) error)` in [internal/supervise/socket.go](internal/supervise/socket.go) per socket-protocol.md §2–§3. Default branch (empty / unrecognised verb) remains the existing `renderStatus` path; `refresh\n` routes to the handler; unwired handler returns `{"ok":false,"error":"refresh handler not wired"}\n`; non-nil handler errors serialise as `{"ok":false,"error":"<one-line>"}\n` with newlines replaced by spaces. Coverage on `socket.go` MUST remain ≥ 95 %.
- [X] T008 [P] Add production-callable `SocketPathForSupervisor(name string) string` and `EnumerateSupervisorSockets() ([]string, error)` to [internal/supervise/socket_darwin.go](internal/supervise/socket_darwin.go) per socket-protocol.md §4 (Darwin scheme: `<UserCacheDir>/hush/supervise-<name>.sock`).
- [X] T009 [P] Add the same two helpers to [internal/supervise/socket_linux.go](internal/supervise/socket_linux.go) (Linux scheme: `<XDG_RUNTIME_DIR>/hush-supervise-<name>.sock`; `TempDir` fallback when XDG unset). Missing dir returns `([]string{}, nil)`.

**Checkpoint**: Foundation ready — supervisor sockets now dispatch by verb; client subcommands have path helpers; user story implementation can begin.

---

## Phase 3: User Story 1 — Operator runs a daemon under supervisor (Priority: P1) 🎯 MVP

**Goal**: `hush supervise <config-path>` runs a single supervisor in the foreground that orchestrates SDD-18/19/20/21/22 building blocks, performs the initial claim, spawns the child with injected secrets, restarts on child exit, and shuts down cleanly on SIGTERM/SIGINT.

**Independent Test**: With a fully-populated supervisor config + a `DiscordStub`, `hush supervise <path>` produces the expected approval prompt, spawns the child with secrets in env, releases pidfile + socket on SIGTERM, exits 0. Duplicate invocation against the same config exits non-zero with the dup-supervisor message.

### Tests for User Story 1 (write FIRST, must FAIL before implementation) ⚠️

- [X] T010 [US1] Write `TestSupervise_DuplicateStartRefused` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — pre-acquire pidfile in test setup; start `supervise`; assert wrapped `ErrPidLocked` + `ExitErr` distinguishing message (AC-10 Scenario 14 / FR-023-6).
- [X] T011 [US1] Write `TestSupervise_SigtermReleasesPidfileAndSocket` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — start supervise with a no-op child; send SIGTERM; assert pidfile + socket file removed within 5 s (SC-023-8).
- [X] T012 [US1] Write `TestSupervise_OrchestrationDelegatesToInternalSupervise` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — read source bytes of `supervise.go`; assert forbidden substrings absent: `runtime.GOOS`, `switch state`, `case StateRunning`, raw numeric `78` (outside whitelisted `supervise.Exit78` constant reference), `os.Exit(`, `net.Listen("tcp`, `http.Server`, `"Bearer "`, `string(decryptedBytes)`. Whitelist exact-byte matches per research.md R-10.
- [X] T013 [US1] Write `TestSupervise_ConfigNotFoundExitNotFound` + `TestSupervise_ConfigInvalidExitInputErr` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — point at missing path → `ExitNotFound`; point at malformed TOML → `ExitInputErr`. Stderr must include `hush: supervise: ` prefix per cli-supervise.md §8.
- [X] T014 [US1] Write `TestSupervise_NoSecretInErrorMessages` in [internal/cli/test_sentinels_test.go](internal/cli/test_sentinels_test.go) — drive every supervise error path (missing config, bad TOML, dup supervisor, perms loose, refill failure stub); assert no dummy-secret marker bytes appear on stdout / stderr / log records (FR-023-27/28).
- [X] T015 [US1] Write `TestSupervise_PerOSGrepClean` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — grep `supervise.go` for `runtime.GOOS` and assert zero occurrences (Constitution VII + cli-supervise.md §9 anti-contract).

### Implementation for User Story 1

- [X] T016 [US1] Create [internal/cli/supervise.go](internal/cli/supervise.go) with `newSuperviseCmd()`, positional-arg validation (`cobra.ExactArgs(1)`), and flag declarations (`--dry-run`, `--grace-window`, `--no-cache` — flag bodies wired in later phases). Add `claimPreview`, `orchestratorInputs`, `refreshFlight`, `refreshCoalescer`, `realClock` file-private types per data-model.md §2.3–§4.1. RunE skeleton: load config → derive `rootCtx` via `signal.NotifyContext` → call `runSupervise(rootCtx, cfg, flags)`.
- [X] T017 [US1] Implement `orchestratorInputs` interface methods (`Name`, `SessionExpiresAt`, `RefreshWindowNext`, `ScopeHealthy`, `ScopeStale`, `LastAuthFailure`, `ChildUptime`, `DiscordConnected`) in [internal/cli/supervise.go](internal/cli/supervise.go) — each a one-line `atomic.Pointer.Load` / `atomic.Bool.Load` per data-model.md §2.3.
- [X] T018 [US1] Implement `refreshCoalescer.Handle(ctx)` single-flight in [internal/cli/supervise.go](internal/cli/supervise.go) per research.md §R-7 (6-line mutex pattern, no `golang.org/x/sync/singleflight` dependency — Constitution XI).
- [X] T019 [US1] Implement the runtime wiring in `runSupervise` in [internal/cli/supervise.go](internal/cli/supervise.go) per cli-supervise.md §6 steps 4–7: `AcquirePidFile` (wrap `ErrPidLocked` as `errDuplicateSupervisor`) → defer `pidfile.Release()` after wg.Wait → `NewStore` → `NewGrace(effectiveTTL, effectiveEnabled)` → `NewRefiller` + attach → `NewRefresher` → `NewStatusServer` + `attach(inputs)` + `attachRefreshHandler(coalescer.Handle)` → `wg.Add(2)` for StatusServer + Refresher goroutines (each with top-frame `recover()` per Constitution IX).
- [X] T020 [US1] Implement the initial `refiller.Refill(rootCtx, cfg.Scope)` call + first-child spawn (`NewChild(buildChildConfig(...))` → `child.Start(rootCtx)`) in [internal/cli/supervise.go](internal/cli/supervise.go) per cli-supervise.md §6 steps 8–10.
- [X] T021 [US1] Implement the child-exit wait loop in [internal/cli/supervise.go](internal/cli/supervise.go) per research.md §R-8: dispatch `EventChildExit78Stale` / `EventChildExitClean` / `EventChildExitCrash` to SDD-19, handle `ErrJTIUnknown` → `EventFetchAuthRequired`, fall through to grace-cache restart when enabled, respawn child. NO state-table reasoning in this file — only event dispatch.
- [X] T022 [US1] Implement SIGTERM/SIGINT graceful shutdown path in [internal/cli/supervise.go](internal/cli/supervise.go) per cli-supervise.md §6 step 12 + research.md §R-9: on `rootCtx.Done()` call `child.Forward(syscall.SIGTERM)` → `child.Wait()` → `wg.Wait()` → return nil (`defer pidfile.Release()` runs AFTER WaitGroup join).
- [X] T023 [US1] Implement `errDuplicateSupervisor` wrap helper in [internal/cli/supervise.go](internal/cli/supervise.go) producing the FR-023-6 message `hush: supervise: another supervisor is already running for this configuration (pidfile=%s)`.

**Checkpoint**: User Story 1 fully functional. `hush supervise <path>` orchestrates the supervisor lifecycle end-to-end. AC-10 Scenarios 1, 3, 4, 5 + 14 pass. Duplicate-start refused per SC-023-7. Clean SIGTERM per SC-023-8.

---

## Phase 4: User Story 2 — Operator previews a configuration (Priority: P2) — `--dry-run`

**Goal**: `hush supervise <config-path> --dry-run` renders the canonical `/claim` payload to stdout via `sign.CanonicalJSON` and exits 0, with NO Discord call, NO vault contact, NO pidfile acquired, NO socket bound, NO child spawned (FR-023-9).

**Independent Test**: Pipe `hush supervise <valid-config> --dry-run` through `jq`; assert valid canonical JSON with `name`, `scope`, `requested_ttl`, `session_type=supervisor` matching config. Wall-clock time < 500 ms (SC-023-2).

### Tests for User Story 2 (write FIRST, must FAIL before implementation) ⚠️

- [X] T024 [US2] Write `TestSupervise_DryRunPrintsCanonicalPayload` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — fixture supervisor config; run dry-run; capture stdout; byte-equal compare against `sign.CanonicalJSON(claimPreview{...})` expected value; assert alphabetical key order, compact spacing, trailing `\n`.
- [X] T025 [US2] Write `TestSupervise_DryRunExitsZero` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — run dry-run; assert `ExitOK`; assert no pidfile / no socket file appears on disk pre→post; assert no goroutine count delta.
- [X] T026 [US2] Write `TestSupervise_DryRunValidatesConfigFirst` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — invalid TOML + `--dry-run`; assert `ExitInputErr`; assert stdout empty (no partial payload — FR-023-10).
- [X] T027 [US2] Write `TestSupervise_DryRunDoesNotSign` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — install a `sign.Sign` test fake that fails the test if called; run dry-run; assert fake never invoked (FR-023-9).

### Implementation for User Story 2

- [X] T028 [US2] Add the dry-run branch to `runSupervise` in [internal/cli/supervise.go](internal/cli/supervise.go) per research.md §R-3 + cli-supervise.md §5: after `config.Load` returns, if `flags.dryRun == true`, build `claimPreview{Name, Reason, Scope, SessionType: "supervisor", RequestedTTL: cfg.RequestedTTL.String(), MachineIndex: cfg.ClientMachineIndex}`, marshal via `sign.CanonicalJSON`, write to `cmd.OutOrStdout()` followed by `\n`, return nil. NEVER call `sign.Sign`. NEVER acquire pidfile, bind socket, or start child.

**Checkpoint**: User Story 2 fully functional. Operator can preview configs without burning a Discord prompt. SC-023-2 met.

---

## Phase 5: User Story 4 — Operator (or downstream agent) checks daemon freshness (Priority: P2) — `hush client status`

**Goal**: `hush client status` connects to a running supervisor's status socket and renders either a human summary (TTY) or raw JSON (pipe / `--json`). Bounded 2-s deadline (FR-023-19). Auto-detects single socket when no `--socket`/`--supervisor` flag supplied (FR-023-15).

**Independent Test**: With a fake supervisor (test seam) holding a known status doc, `hush client status` returns the document; TTY path produces labelled human summary; pipe / `--json` path produces verbatim bytes. Unreachable socket → `ExitErr`. Auto-detect 0 or >1 sockets → `ExitInputErr`.

### Tests for User Story 4 (write FIRST, must FAIL before implementation) ⚠️

- [X] T029 [P] [US4] Write `TestClientStatus_TTYHumanSummary` in [internal/cli/client_test.go](internal/cli/client_test.go) — fake supervisor returns a canned status doc; force `IsTerminal=true` via tty seam; assert human labels present (`Supervisor:`, `State:`, `Child PID:`, `Child up:`, `Session expires:`, `Next refresh:`, `Healthy scopes:`, `Stale scopes:`, `Discord:`, `Last auth fail:`) and JSON delimiters absent (per cli-client.md §2.5).
- [X] T030 [P] [US4] Write `TestClientStatus_PipeJSON` in [internal/cli/client_test.go](internal/cli/client_test.go) — fake supervisor with `IsTerminal=false`; assert stdout is byte-equal to the supervisor's response bytes (no re-marshal — R-5).
- [X] T031 [P] [US4] Write `TestClientStatus_JsonFlagOverridesTTY` in [internal/cli/client_test.go](internal/cli/client_test.go) — force `IsTerminal=true`, pass `--json`; assert JSON path taken (FR-023-17a).
- [X] T032 [P] [US4] Write `TestClientStatus_SocketUnreachableExitErr` in [internal/cli/client_test.go](internal/cli/client_test.go) — `--socket /nonexistent`; assert `ExitErr` + stderr identifies socket path (no secret bytes — FR-023-28).
- [X] T033 [P] [US4] Write `TestClientStatus_TimeoutExitErr` in [internal/cli/client_test.go](internal/cli/client_test.go) — fake supervisor accepts conn then hangs; assert 2-s deadline trips and maps to `ExitErr` (FR-023-19). Use a test-only timeout-override seam to keep test wall-clock short.
- [X] T034 [P] [US4] Write `TestClientStatus_AutoDetectSingleSocket` in [internal/cli/client_test.go](internal/cli/client_test.go) — temp runtime dir with exactly one `*.sock`; no `--socket`/`--supervisor`; assert auto-select.
- [X] T035 [P] [US4] Write `TestClientStatus_AutoDetectZeroSocketsExitInputErr` + `TestClientStatus_AutoDetectMultipleSocketsExitInputErr` in [internal/cli/client_test.go](internal/cli/client_test.go) — empty dir / two sockets respectively; assert `ExitInputErr` with descriptive stderr naming the runtime dir / candidates (FR-023-15 (4)).
- [X] T036 [P] [US4] Write `TestClientStatus_InvalidSocketPathExitInputErr` in [internal/cli/client_test.go](internal/cli/client_test.go) — `--socket relative/path`; assert `ExitInputErr`.
- [X] T037 [P] [US4] Write `TestClientStatus_NoSecretInOutput` in [internal/cli/test_sentinels_test.go](internal/cli/test_sentinels_test.go) — drive every client status error path; assert no dummy-secret marker bytes on stdout / stderr / log records (FR-023-27/28, SC-023-9).
- [X] T038 [P] [US4] Write `TestClientStatus_PerOSGrepClean` in [internal/cli/client_test.go](internal/cli/client_test.go) — grep `client.go` for `runtime.GOOS` / `net.Dial("tcp"` / `http.` / `"Bearer "`; assert zero occurrences (Constitution V + VII).

### Implementation for User Story 4

- [X] T039 [US4] Create [internal/cli/client.go](internal/cli/client.go) with `newClientCmd()` parent and `newClientStatusCmd()` leaf. Wire `--socket`, `--supervisor`, `--json` flags. Add file-private types `statusDoc` and `refreshAck` per data-model.md §3.4. Wire parent into `root.go` registration from T003.
- [X] T040 [US4] Implement socket-path resolution helper `resolveSocketPath(socket, supervisor string) (string, error)` in [internal/cli/client.go](internal/cli/client.go) per cli-client.md §2.3 + research.md §R-2: precedence `--socket` > `--supervisor` → `supervise.SocketPathForSupervisor` > `supervise.EnumerateSupervisorSockets()` (exactly-1 auto-select, else wrap as `errSocketAmbiguous`).
- [X] T041 [US4] Implement the 2-s round-trip in `clientStatusRun` in [internal/cli/client.go](internal/cli/client.go) per cli-client.md §2.4: `context.WithTimeout(cmd.Context(), 2*time.Second)` → `(&net.Dialer{}).DialContext(ctx, "unix", path)` → `conn.SetDeadline(...)` → `conn.Write([]byte("status\n"))` → read to EOF → close. Single attempt — no retries (FR-023-19 anti-contract). Dial / read failures wrap as `errSocketUnreachable`.
- [X] T042 [US4] Implement output path selection in [internal/cli/client.go](internal/cli/client.go): `if jsonOutput || !term.IsTerminal(int(os.Stdout.Fd()))` → write raw socket bytes verbatim (with single trailing `\n`); else unmarshal into `statusDoc` and call `writeHumanStatus(w, doc)`.
- [X] T043 [US4] Implement `writeHumanStatus(w io.Writer, doc statusDoc)` in [internal/cli/client.go](internal/cli/client.go) producing the locked label format from cli-client.md §2.5. `nil` `ChildPID` → `"no child"`; nil `LastAuthFailure` → `"never"`; empty scope lists → `"(none)"`; `DiscordConnected` → `"connected"` / `"disconnected"`.

**Checkpoint**: User Story 4 fully functional. AC-10 Scenario 12 (status check before long task) passes. SC-023-3 + SC-023-5 + SC-023-6 met.

---

## Phase 6: User Story 5 — Operator triggers an immediate refresh (Priority: P2) — `hush client refresh`

**Goal**: `hush client refresh` sends `refresh\n` over the status socket, waits up to 90 s (FR-023-24) for the supervisor's terminal ack (after refill + validate + child restart completes), maps `{"ok":true}` → `ExitOK`, `{"ok":false,"error":...}` → `ExitErr` with supervisor's reason on stderr.

**Independent Test**: With a fake supervisor that writes `{"ok":true}\n`, `hush client refresh` exits 0. With `{"ok":false,"error":"vault unreachable"}\n`, exits 1 with reason on stderr.

### Tests for User Story 5 (write FIRST, must FAIL before implementation) ⚠️

- [X] T044 [P] [US5] Write `TestClientRefresh_AckMapsToExitOK` in [internal/cli/client_test.go](internal/cli/client_test.go) — fake supervisor writes `{"ok":true}\n` on `refresh\n`; assert `ExitOK`, stdout empty, stderr empty (cli-client.md §3.5).
- [X] T045 [P] [US5] Write `TestClientRefresh_ErrorMapsToExitErr` in [internal/cli/client_test.go](internal/cli/client_test.go) — fake supervisor writes `{"ok":false,"error":"vault unreachable"}\n`; assert `ExitErr` with `hush: client refresh: supervisor refused: vault unreachable` on stderr.
- [X] T046 [P] [US5] Write `TestClientRefresh_SocketUnreachableExitErr` in [internal/cli/client_test.go](internal/cli/client_test.go) — `--socket /nonexistent`; assert `ExitErr` + descriptive stderr.
- [X] T047 [P] [US5] Write `TestClientRefresh_TimeoutExitErr` in [internal/cli/client_test.go](internal/cli/client_test.go) — fake supervisor hangs after accept; assert 90-s deadline trips (shortened to ~1 s via test-only timeout-override seam) and maps to `ExitErr` with `timed out after 90s` stderr.
- [X] T048 [P] [US5] Write `TestClientRefresh_MalformedJsonResponseExitErr` in [internal/cli/client_test.go](internal/cli/client_test.go) — fake supervisor writes `garbage\n`; assert `errSocketUnreachable` → `ExitErr` (treated as "supervisor produced unexpected bytes" per cli-client.md §3.5).
- [X] T049 [P] [US5] Write `TestClientRefresh_NoFormatFlag` in [internal/cli/client_test.go](internal/cli/client_test.go) — invoke with `--json`; assert cobra unknown-flag check fires (FR-023-17a — `--json` is structurally absent from refresh).
- [X] T050 [P] [US5] Write `TestClientRefresh_NoSecretInOutput` in [internal/cli/test_sentinels_test.go](internal/cli/test_sentinels_test.go) — drive every refresh error path; assert no dummy-secret marker bytes on any operator-visible surface (FR-023-27/28, SC-023-9).

### Implementation for User Story 5

- [X] T051 [US5] Add `newClientRefreshCmd()` leaf to [internal/cli/client.go](internal/cli/client.go); wire `--socket` + `--supervisor` flags ONLY (NO `--json`). Mount under `newClientCmd()` parent.
- [X] T052 [US5] Implement `clientRefreshRun` in [internal/cli/client.go](internal/cli/client.go) per cli-client.md §3.4: `context.WithTimeout(cmd.Context(), 90*time.Second)` → `resolveSocketPath` (shared with status) → dial → `conn.Write([]byte("refresh\n"))` → read to EOF → unmarshal `refreshAck` → on `OK=true` return nil → on `OK=false` wrap `Error` as `errSupervisorRefused` → malformed JSON wraps as `errSocketUnreachable`. Single attempt — NO retry on timeout (cli-client.md §3.7 anti-contract).

**Checkpoint**: User Story 5 fully functional. AC-10 Scenario 13 (secret rotated mid-session) enabled end-to-end. SC-023-4 met.

---

## Phase 7: User Story 3 — Operator overrides grace window for one run (Priority: P3) — `--grace-window` / `--no-cache`

**Goal**: `--grace-window <dur>` overrides `cfg.CacheGraceTTL` for this run (range `> 0 && ≤ 4h`); `--no-cache` forces `cfg.CacheSecretsForRestart = false`; `--no-cache` wins when both supplied (FR-023-14).

**Independent Test**: With a config setting `cache_secrets_for_restart=true`, running with `--no-cache` then crashing the child outside JWT TTL drops the supervisor into `awaiting-approval`. Running with `--grace-window 30m` against a config with `cache_grace_ttl=60m` uses 30 m.

### Tests for User Story 3 (write FIRST, must FAIL before implementation) ⚠️

- [X] T053 [P] [US3] Write `TestSupervise_GraceWindowOverrideTakesPrecedence` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — config `cache_grace_ttl=60m`, flag `--grace-window 30m`; assert effective `Grace` TTL = 30 m.
- [X] T054 [P] [US3] Write `TestSupervise_GraceWindowExceedsCapRejected` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — `--grace-window 5h`; assert `ExitInputErr` with stderr `hush: supervise: --grace-window must be >0 and ≤4h, got 5h0m0s` (FR-023-12).
- [X] T055 [P] [US3] Write `TestSupervise_GraceWindowNegativeRejected` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — `--grace-window -1s`; assert `ExitInputErr` (FR-023-12).
- [X] T056 [P] [US3] Write `TestSupervise_NoCacheForcesStrict` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — config `cache_secrets_for_restart=true`, flag `--no-cache`; assert effective `Grace.Enabled() == false`.
- [X] T057 [P] [US3] Write `TestSupervise_NoCacheBeatsGraceWindow` in [internal/cli/supervise_test.go](internal/cli/supervise_test.go) — both `--no-cache` and `--grace-window 30m`; assert `Grace.Enabled() == false`, `--grace-window` silently ignored (FR-023-14).

### Implementation for User Story 3

- [X] T058 [US3] Insert flag-override block in `runSupervise` of [internal/cli/supervise.go](internal/cli/supervise.go), BEFORE any side effect (pidfile / store / socket / child), per research.md §R-4: validate `flags.graceWindow` range (`> 0 && ≤ 4h`) → set `effectiveGraceTTL = flags.graceWindow` else `cfg.CacheGraceTTL`; if `flags.noCache` → `effectiveCacheEnabled = false` else `cfg.CacheSecretsForRestart`. Out-of-range values wrap as `errInvalidGraceWindow` → `ExitInputErr`. `--no-cache` precedence beats `--grace-window` (the value is ignored without error).

**Checkpoint**: User Story 3 fully functional. All P-priority stories independently testable.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Integration test, documentation updates, gates, and manual smokes mandated by the chunk Implement prompt.

### Integration test

- [X] T059 [P] Write `TestSuperviseIntegration_DryRunWithDiscordStub` in [internal/cli/supervise_integration_test.go](internal/cli/supervise_integration_test.go) (build tag `//go:build integration`) — chassis modelled on [internal/cli/serve_integration_test.go](internal/cli/serve_integration_test.go): full dry-run with a fixture supervisor config + `DiscordStub`; capture stdout; assert JSON-parseable canonical payload with expected `name`/`scope`/`requested_ttl`/`session_type=supervisor`; assert no Discord call was issued; assert no pidfile / socket binding occurred (FR-023-9).

### Documentation

- [X] T060 [P] Append "**Exported API — locked at SDD-23**" section to [docs/PACKAGE-MAP.md](docs/PACKAGE-MAP.md) under `internal/cli` noting the `supervise`, `client status`, `client refresh` subcommand registrations (no new exported package-level symbols — the cobra command tree IS the contract).
- [X] T061 [P] Update AC-10 row in [docs/AC-MATRIX.md](docs/AC-MATRIX.md) with the new test file paths (`supervise_test.go`, `client_test.go`, `supervise_integration_test.go`) and AC-10 Scenarios 12 + 13 + 14 coverage citations.
- [X] T062 [P] Mark SDD-23 status `done` in [docs/SDD-PLAYBOOK.md](docs/SDD-PLAYBOOK.md).

### Gates (run from repo root — MUST all pass clean)

- [X] T063 Run `magex format:fix` from repo root; resolve any formatting changes.
- [X] T064 Run `magex lint` from repo root; fix any lint violations until clean.
- [X] T065 Run `magex test:race` from repo root; ALL unit tests must pass with the race detector enabled.
- [X] T066 Run `magex test:race -tags=integration` from repo root; integration suite (including T059) must pass with race detector enabled.

### Coverage + manual smokes

- [X] T067 Verify coverage ≥ 85 % on `internal/cli` (supervise + client portions) via `go test -cover ./internal/cli/ -run "Supervise|Client"` (SC-023-10).
- [X] T068 Verify coverage ≥ 95 % on `internal/supervise/socket.go` (preserve SDD-22 bar) via `go test -coverprofile=/tmp/socket.out ./internal/supervise/ && go tool cover -func=/tmp/socket.out | grep socket.go`.
- [X] T069 Manual smoke: confirm `hush supervise <fixture-config> --dry-run` produces machine-parseable canonical-JSON on stdout in < 500 ms (SC-023-2).
- [X] T070 Manual smoke: confirm `hush client status --supervisor <name>` pretty-print is readable on a TTY (presence of all human labels from cli-client.md §2.5).
- [X] T071 Final grep audit: `grep -n "runtime.GOOS\|http.Server\|http.ListenAndServe\|net.Listen(\"tcp\|\"Bearer \"\|string(decryptedBytes)" internal/cli/supervise.go internal/cli/client.go` — MUST return zero matches (Constitution V/VII + cli-supervise.md §9, cli-client.md §2.7 anti-contracts).

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately.
- **Foundational (Phase 2)**: Depends on Phase 1 — BLOCKS all user stories (verb dispatch + path helpers are shared by supervise and client subcommands).
- **User Story 1 (Phase 3, P1)**: Depends on Phase 2 (uses `attachRefreshHandler`).
- **User Story 2 (Phase 4, P2)**: Depends on Phase 3 (dry-run is a branch inside `runSupervise`).
- **User Story 4 (Phase 5, P2)**: Depends on Phase 2 (`SocketPathForSupervisor`, `EnumerateSupervisorSockets`) — independent of Phase 3 (no shared file edits).
- **User Story 5 (Phase 6, P2)**: Depends on Phase 2 (verb dispatch refresh path) AND Phase 5 (shares `client.go` + `resolveSocketPath` helper). Tests are parallelizable with US4 tests until both write into `client_test.go`.
- **User Story 3 (Phase 7, P3)**: Depends on Phase 3 (`runSupervise` skeleton exists to extend).
- **Polish (Phase 8)**: Depends on Phases 3–7 complete.

### User Story Dependencies

- **US1 (P1)**: No story dependencies. Foundational only.
- **US2 (P2)**: Depends on US1 (extends `runSupervise`'s entry shape).
- **US4 (P2)**: Independent of US1/US2/US3 — different file (`client.go`).
- **US5 (P2)**: Shares `client.go` with US4 — sequential within file, parallel test authoring possible.
- **US3 (P3)**: Depends on US1 (extends `runSupervise`'s flag-handling shape).

### Within Each User Story

- Tests MUST be written and FAIL before implementation (TDD — Constitution VIII + SDD-23 Prompt 4).
- File-private types (e.g. `claimPreview`, `orchestratorInputs`, `refreshCoalescer`) before the functions that use them.
- Helpers (e.g. `resolveSocketPath`, `writeHumanStatus`) before the RunE that calls them.

### Parallel Opportunities

- T002 (exit_codes_test) is parallel-safe with T001 implementation work because the test file is separate.
- T005 and T006 ([P]) operate on different per-OS test files.
- T008 and T009 ([P]) operate on different per-OS production files.
- Within US4 (T029–T038): all are [P] because each is a new test function in `client_test.go` — by convention they may be appended in any order before the implementation lands.
- Within US5 (T044–T050): same — all [P] for test authoring.
- Within US3 (T053–T057): all [P] — independent test functions.
- Polish documentation tasks T060/T061/T062 are [P] — three different docs.
- US4 and US5 implementation tasks share `client.go` → sequential within the file.

---

## Parallel Example: User Story 4

```bash
# Launch all User Story 4 tests in parallel (different test functions in client_test.go,
# but each is logically independent — author them concurrently in one session):
Task: "Write TestClientStatus_TTYHumanSummary in internal/cli/client_test.go"
Task: "Write TestClientStatus_PipeJSON in internal/cli/client_test.go"
Task: "Write TestClientStatus_JsonFlagOverridesTTY in internal/cli/client_test.go"
Task: "Write TestClientStatus_SocketUnreachableExitErr in internal/cli/client_test.go"
Task: "Write TestClientStatus_TimeoutExitErr in internal/cli/client_test.go"
Task: "Write TestClientStatus_AutoDetectSingleSocket in internal/cli/client_test.go"
Task: "Write TestClientStatus_AutoDetectZeroSocketsExitInputErr + TestClientStatus_AutoDetectMultipleSocketsExitInputErr"
Task: "Write TestClientStatus_InvalidSocketPathExitInputErr"
Task: "Write TestClientStatus_NoSecretInOutput in internal/cli/test_sentinels_test.go"
Task: "Write TestClientStatus_PerOSGrepClean in internal/cli/client_test.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001–T003).
2. Complete Phase 2: Foundational (T004–T009). CRITICAL — blocks all stories.
3. Complete Phase 3: User Story 1 (T010–T023).
4. **STOP and VALIDATE**: `hush supervise <config>` works end-to-end with a `DiscordStub`. AC-10 Scenarios 1, 3, 4, 5, 14 pass.
5. MVP ready — operator can run a daemon under hush.

### Incremental Delivery

1. Setup + Foundational → Foundation ready.
2. US1 → MVP (daemon runs under supervisor).
3. US2 → dry-run preview (operator authoring affordance).
4. US4 → status query (agent-visible freshness API).
5. US5 → refresh trigger (mid-session rotation).
6. US3 → grace-window / no-cache flag overrides.
7. Polish → docs, gates, smokes.

### Parallel Team Strategy

With multiple developers post-Foundational:

- Developer A: US1 (supervise core), then US2 (dry-run), then US3 (flag overrides) — all in `supervise.go`.
- Developer B: US4 (client status), then US5 (client refresh) — all in `client.go`.
- Developer C: integration test (T059) + docs + gates (Phase 8) once US1+US4+US5 are merging.

---

## Test Inventory (Constitution VIII compliance)

Every behaviour contract in the SDD-23 §Prompt 4 list is covered by a test task BEFORE its implementation task:

| Test (required by SDD-23) | Task | Impl task |
|---|---|---|
| `TestSupervise_DryRunPrintsCanonicalPayload` | T024 | T028 |
| `TestSupervise_DryRunExitsZero` | T025 | T028 |
| `TestSupervise_GraceWindowOverrideTakesPrecedence` | T053 | T058 |
| `TestSupervise_NoCacheForcesStrict` | T056 | T058 |
| `TestSupervise_OrchestrationDelegatesToInternalSupervise` | T012 | T016–T023 (whole-file grep) |
| `TestClientStatus_TTYHumanSummary` | T029 | T042–T043 |
| `TestClientStatus_PipeJSON` | T030 | T042 |
| `TestClientStatus_SocketUnreachableExitErr` | T032 | T041 |
| `TestClientRefresh_AckMapsToExitOK` | T044 | T052 |
| `TestClientRefresh_ErrorMapsToExitErr` | T045 | T052 |
| Integration: dry-run + DiscordStub | T059 | (validates T028) |

Additional contract-driven tests (cli-supervise.md §10, cli-client.md §4, socket-protocol.md §6) are also enumerated above for full ≥ 85 % coverage.

---

## Notes

- [P] tasks = different files OR independent test functions; no shared-state dependencies.
- [Story] label maps each task to its user story (US1–US5) for traceability.
- Each user story is independently testable and deliverable as an MVP increment (US1 alone constitutes the MVP).
- Verify tests FAIL before implementing (TDD discipline — Constitution VIII).
- Defer all commits to the single combined commit mandated by SDD-23 Prompt 5; do NOT commit between phases.
- Avoid: vague tasks, same-file conflicts, cross-story dependencies that break independence, business-logic creep into `internal/cli` (the grep test T012 is the structural guard).
- Anti-contracts (cli-supervise.md §9, cli-client.md §2.7/§3.7, socket-protocol.md §5) are enforced by T012, T015, T038, T071 grep checks AND code review.
