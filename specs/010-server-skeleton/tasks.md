# Tasks: internal/server — HTTP server skeleton, ordered startup checks, and SIGHUP atomic vault reload

**Input**: Design documents from `/specs/010-server-skeleton/`
**Prerequisites**: plan.md (loaded), spec.md (loaded), research.md (loaded), data-model.md (loaded), contracts/ (loaded), quickstart.md (loaded)

**Tests**: TDD-mandatory per Constitution VIII. A test task ALWAYS precedes the implementation task it pins. Coverage target is 95% on `internal/server`.

**Organization**: Tasks are grouped by user story (P1 → P2). Each story is independently testable. The MVP scope is User Stories 1, 2, 3 (P1 trio) — all P1 stories must land for AC-1 / AC-2 / AC-8 to flip green.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Maps task to a user story (US1…US7); Setup, Foundational, and Polish phases carry no story label.

## Path Conventions

Single Go module. All source lives at the repo root under `internal/server/`. Test files (including `//go:build integration`) live in the same directory per Go layout. Spec artefacts live at `/specs/010-server-skeleton/`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Lay down the empty package skeleton, doc.go, and the sentinel-error file shell. Nothing here implements behaviour — these are the empty drawers every later phase fills.

- [X] T001 Create the empty package directory and the package doc file at [internal/server/doc.go](internal/server/doc.go) with the package overview comment (chassis purpose, entry points, anti-contracts) and `package server` declaration. No `init()`, no package-level mutable state (Constitution IX, FR-028).
- [X] T002 [P] Create the sentinel-error file shell at [internal/server/errors.go](internal/server/errors.go) declaring the *exported* error variables only (no helpers): `ErrMissingConfig`, `ErrMissingVaultPtr`, `ErrMissingTokenStore`, `ErrMissingApprover`, `ErrMissingLogger`, `ErrMissingAuditWriter`, `ErrAlreadyRun`, `ErrClockUnsynchronised`, `ErrFileModeLoose`, `ErrBindNotOnTailscale`, `ErrStateDirUnsafe`, `ErrReloadFileMissing`, `ErrReloadDecryptFailed`, `ErrReloadInvalid`, `ErrReloadInProgress`, `ErrShuttingDown`. Each is `errors.New("server: ...")` exactly per `contracts/startup-checks.md` and Phase 0 R10. Constitution IX: idiomatic sentinels.
- [X] T003 [P] Add the package-default constants for chassis tunables at [internal/server/server.go](internal/server/server.go) (file may be created here even though `Server` is filled later): `DefaultReloadDrainWindow = 30 * time.Second`, `DefaultShutdownTimeout = 30 * time.Second`, `DefaultReadHeaderTimeout = 10 * time.Second`, `DefaultReadTimeout = 30 * time.Second`, `DefaultWriteTimeout = 30 * time.Second`, `DefaultIdleTimeout = 60 * time.Second`, `DefaultClockSyncTimeout = 5 * time.Second`, `MaxRequestBodyBytes = 64 << 10`. Per data-model.md "Configuration consumed" and research.md R2.

**Checkpoint**: Package compiles (empty bodies are fine); sentinel errors and defaults are importable. No behaviour yet.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Locked types + the dependency-injection entry point + the request-ID context plumbing. Every user story needs these. NO user-story work begins until Phase 2 lands.

**⚠️ CRITICAL**: User stories cannot be implemented until this phase is complete.

### Approver / Decision / AuditEvent value types

- [X] T004 [P] Write the table-driven test [internal/server/approver_test.go](internal/server/approver_test.go) `TestApprover_TypeShape` asserting: `Approver` is a single-method interface; `ApprovalRequest` has the locked field set per `contracts/approver.md` (RequestID, MachineName, ClientIP `netip.Addr`, Scope, Reason, SessionType, RequestedTTL, Metadata); `Decision` has the locked field set; `SessionType.String()` returns `"interactive"`, `"supervisor"`, `"unknown"` for the three values. Use `reflect` to verify field names and types so a future drift breaks the test loudly.
- [X] T005 [Foundation] Implement [internal/server/approver.go](internal/server/approver.go): the `Approver` interface, `ApprovalRequest`, `Decision`, `SessionType` (with `iota+1` constants `SessionInteractive`, `SessionSupervisor` and a `String()` method) per `contracts/approver.md`. T004 must pass. Constitution IX: single-method interface, declared at the consumer.
- [X] T006 [P] Write the table-driven test [internal/server/audit_test.go](internal/server/audit_test.go) `TestAuditEvent_TypeShape` asserting `AuditWriter` is a single-method interface and `AuditEvent` has the locked field set per data-model.md (Type, At, RequestID, ClientIP, Detail) and that the chassis-emitted `AuditEventType` constants are exactly `AuditServerStart`, `AuditServerStop`, `AuditVaultReloaded`, `AuditFilePermCheckFailed`, `AuditAuthFailedNotAllowed`, `AuditPanicCaptured`.
- [X] T007 [Foundation] Implement the audit types in [internal/server/approver.go](internal/server/approver.go) (same file is acceptable per data-model.md note): `AuditWriter` interface, `AuditEvent` struct, `AuditEventType` string with the six constants above. T006 must pass.

### RequestID context plumbing

- [X] T008 [P] Write the test [internal/server/request_id_test.go](internal/server/request_id_test.go) `TestRequestID_FromContext` asserting: `RequestID(ctx)` returns `""` for a context that did not pass through the chassis; returns the assigned hex string when the package-private key carries one; the context key is a typed empty struct (not a string) — verify by attempting to read the value through a `string(key)` lookup and asserting it returns `""`.
- [X] T009 [Foundation] Implement [internal/server/request_id.go](internal/server/request_id.go): the package-private `requestIDKeyType` empty-struct typed key, the `requestIDKey` value, and the exported `func RequestID(ctx context.Context) string`. T008 must pass. Per research.md R3.

### Deps validation + New entry point

- [X] T010 Write the table-driven test [internal/server/server_test.go](internal/server/server_test.go) `TestNew_RequiresAllDeps` asserting that `New(Deps{})` returns the matching `ErrMissing*` sentinel for each of the six required fields (Cfg, VaultPtr, TokenStore, Approver, Logger, AuditWriter), one row per nil case, plus a row where `VaultPtr.Load()` returns nil (mapped to `ErrMissingVaultPtr`). Each case is matched via `errors.Is`. Per FR-023, SC-011, data-model.md Deps.Validation.
- [X] T011 Write the test [internal/server/server_test.go](internal/server/server_test.go) `TestNew_ZeroIO` asserting `New` performs no I/O: invoke `New` with all valid deps where the supplied `Cfg` points at a non-existent state directory, an unreachable listen address, and where `VaultPtr.Load()` returns a sentinel marker store — assert `New` returns nil error and never touched the filesystem (use a wrapper around `*os.File`-style probes if needed; otherwise assert by absence of side-effects: no socket bound, no file opened, no signal registered). Per FR-027.
- [X] T012 [Foundation] Implement [internal/server/server.go](internal/server/server.go) `Deps` struct (locked surface from plan.md), unexported `Server` struct (fields per data-model.md §Server, including `mux`, `httpServer`, `reloadMu`, `drainWG`, `shuttingDown`, `runOnce sync.Once`), and `func New(deps Deps) (*Server, error)` which performs nil-checks against the six required fields, defaults `Clock` to `time.Now` and `ClockSyncProbe` to a platform-default helper (registered in T013/T014), assigns fields, and returns the `*Server`. NO I/O. T010 and T011 must pass.

### Default ClockSyncProbe (platform helpers, build-tag gated)

- [X] T013 [P] Write the test [internal/server/clock_sync_default_darwin_test.go](internal/server/clock_sync_default_darwin_test.go) (`//go:build darwin`) `TestDefaultClockSyncProbe_Darwin_Parses` asserting the darwin probe parses `On` and `Off` answers from a stubbed exec helper; tests inject the exec helper via an unexported function variable `execNetworkTime func(ctx context.Context) (string, error)` so no real binary runs. Cover: synced=true / synced=false / exec error → wrapped error.
- [X] T014 [P] Write the test [internal/server/clock_sync_default_linux_test.go](internal/server/clock_sync_default_linux_test.go) (`//go:build linux`) `TestDefaultClockSyncProbe_Linux_Parses` asserting the linux probe parses `NTPSynchronized=yes`/`NTPSynchronized=no` from a stubbed exec helper, derives drift from a stubbed `TimeUSec` parser, and returns a wrapped error on exec failure.
- [X] T015 [P] [Foundation] Implement [internal/server/clock_sync_default_darwin.go](internal/server/clock_sync_default_darwin.go) (`//go:build darwin`) and [internal/server/clock_sync_default_linux.go](internal/server/clock_sync_default_linux.go) (`//go:build linux`) — each exposes a package-private `defaultClockSyncProbe(ctx) (synced bool, drift time.Duration, err error)` plus the test-injection helper. The 5 s bounded timeout (`DefaultClockSyncTimeout`) is enforced via `context.WithTimeout`. Per research.md R4 and `contracts/startup-checks.md`. T013/T014 must pass on the matching platform.

**Checkpoint**: Foundational types and the chassis constructor are in place. The interfaces (`Approver`, `AuditWriter`) and value types are locked. User stories may now begin in parallel.

---

## Phase 3: User Story 1 — Refuse to start on a misconfigured host (Priority: P1) 🎯 MVP

**Goal**: Before binding any socket, the chassis runs `clock_sync → file_modes → tailscale_bind → state_dir`; the first failure short-circuits with a named sentinel and a non-zero return; no listener ever opens.

**Independent Test**: Build a chassis with each kind of misconfiguration in turn (clock unsynced, 0644 file, `0.0.0.0` listen, missing/foreign-owned state dir) and assert each launch returns the matching named sentinel error and no socket is bound; build a correctly-configured chassis and assert `Run` proceeds to bind. SC-001, SC-002.

**Tests for User Story 1 (write FIRST, MUST FAIL until implementation):**

- [X] T016 [P] [US1] Write [internal/server/startup_checks_test.go](internal/server/startup_checks_test.go) `TestStartupChecks_RefusesUnsyncedClock` — inject a `ClockSyncProbe` returning `(false, 0, nil)`; assert `Run` returns an error matching `errors.Is(err, ErrClockUnsynchronised)` and that no listener was opened (assert via a counting `net.Listen`-style hook or by asserting the internal listener field stayed nil). Per FR-004, SC-001.
- [X] T017 [P] [US1] Add to [internal/server/startup_checks_test.go](internal/server/startup_checks_test.go) `TestStartupChecks_RefusesClockDriftOver60s` — inject probe returning `(true, 61*time.Second, nil)`; assert `ErrClockUnsynchronised`. Per FR-004.
- [X] T018 [P] [US1] Add to [internal/server/startup_checks_test.go](internal/server/startup_checks_test.go) `TestStartupChecks_RefusesLooseFileMode` — `t.TempDir()` populated with one regular file at mode 0644; assert `ErrFileModeLoose` is returned and the wrapper text identifies the offending entry as a "regular file" without disclosing its bytes. Per FR-005, SC-001.
- [X] T019 [P] [US1] Add to [internal/server/startup_checks_test.go](internal/server/startup_checks_test.go) `TestStartupChecks_RefusesLooseDirMode` — temp dir at mode 0755 → `ErrFileModeLoose` (category "directory"). Per FR-005.
- [X] T020 [P] [US1] Add to [internal/server/startup_checks_test.go](internal/server/startup_checks_test.go) `TestStartupChecks_RefusesPublicBind` — `Cfg.Server.ListenAddr` set to `0.0.0.0:7743` (also a row each for `127.0.0.1:7743`, empty host, and a non-Tailscale public IP); assert `ErrBindNotOnTailscale`. Per FR-006, SC-001.
- [X] T021 [P] [US1] Add to [internal/server/startup_checks_test.go](internal/server/startup_checks_test.go) `TestStartupChecks_RefusesUnsafeStateDir` — three rows: missing dir, regular file in place of dir, dir owned by a different uid (skip when running as root and no second uid is available); assert `ErrStateDirUnsafe`. Per FR-007.
- [X] T022 [P] [US1] Add to [internal/server/startup_checks_test.go](internal/server/startup_checks_test.go) `TestStartupChecks_OrderedExecution` — host crafted with all four misconfigurations present at once; assert `errors.Is(err, ErrClockUnsynchronised)` (the first check in the documented order); assert via call-counting fakes for `file_modes`, `tailscale_bind`, and `state_dir` that none of them was invoked (state-dir last, clock first). Per FR-003, SC-002.
- [X] T023 [P] [US1] Add to [internal/server/startup_checks_test.go](internal/server/startup_checks_test.go) `TestStartupChecks_AuditEmitsRefused` — on any startup-check failure, assert exactly one `AuditServerStart` event is written with `Detail["status"]="refused"` and `Detail["check"]=<name>`, and that no audit event of any other type is emitted. Per `contracts/startup-checks.md` §Refuse-to-start semantics.
- [X] T024 [US1] Write [internal/server/integration_test.go](internal/server/integration_test.go) (`//go:build integration`) `TestStartupChecks_HappyPath` — correctly-configured chassis on a real Tailscale-CGNAT host (skip if `net.InterfaceAddrs()` reports no `100.64.0.0/10` address); assert `Run` proceeds past the check sequence, binds a listener (use `t.Context()` cancellation to cut it off shortly after bind), and emits one `AuditServerStart` event with `Detail["status"]="ok"`. Per quickstart.md "Lifecycle ordering".

**Implementation for User Story 1:**

- [X] T025 [US1] Implement [internal/server/startup_checks.go](internal/server/startup_checks.go) — `StartupCheck` struct (Name, Run), the `(*Server).startupChecks()` slice in the locked order, the four check methods (`checkClockSync`, `checkFileModes`, `checkTailscaleBind`, `checkStateDir`), the `(*Server).runStartupChecks(ctx)` driver that iterates the slice and short-circuits on first error, the unexported `interfaceLister` injection point (default `net.InterfaceAddrs`). The `checkFileModes` uses `filepath.WalkDir` (research.md R6). The `checkStateDir` uses `os.Lstat` + `syscall.Stat_t` (research.md R7). Each check honours its skip flag (`Cfg.Security.RequireNTPSync`, `Cfg.Security.RequireFileModeChecks`). T016–T023 must pass.
- [X] T026 [US1] Wire startup checks into the chassis lifecycle in [internal/server/server.go](internal/server/server.go) `(*Server).Run(ctx)`: invoke `runStartupChecks(ctx)` BEFORE any listener bind; on error, emit one `AuditServerStart` event with `status="refused"` and `check=<name>` (T023), log at ERROR with structured field naming the failed check (`contracts/startup-checks.md` §Refuse-to-start semantics), and return the error. T024 must pass on a Tailscale-CGNAT host.

**Checkpoint**: User Story 1 fully functional. AC-8 row in AC-MATRIX is now testable. The chassis refuses misconfigured hosts with named sentinels.

---

## Phase 4: User Story 2 — SIGHUP rotates the vault under live traffic without dropping a request (Priority: P1)

**Goal**: SIGHUP loads a new vault, atomically swaps the `*atomic.Pointer[vault.Store]`, and destroys the previous store after a 30 s drain window. In-flight requests holding the old store finish; new requests see the new store. Reloads are serialised; failed reloads leave the active pointer unchanged.

**Independent Test**: Start with vault A, begin a slow secret-fetch request, replace the file on disk with vault B, send SIGHUP; assert in-flight returns A's value, fresh request returns B's value, A's protected memory is destroyed exactly once at or after the drain window. SC-003, SC-004, SC-005.

**Tests for User Story 2 (write FIRST, MUST FAIL):**

- [X] T027 [P] [US2] Write [internal/server/reload_test.go](internal/server/reload_test.go) `TestReloadVault_HappyPath_SwapsPointer` — start with a fake `vault.Store` A in `vaultPtr`; call `ReloadVault(ctx, newPath, key)` with a working `vault.Load`-equivalent (use a test seam `loadVaultFn func(ctx, path, key) (vault.Store, error)` injected via `Deps` or an unexported package var); assert `vaultPtr.Load()` now returns B; assert one `AuditVaultReloaded` event was emitted. Per FR-010, `contracts/reload.md`.
- [X] T028 [P] [US2] Add to [internal/server/reload_test.go](internal/server/reload_test.go) `TestReloadVault_FailedReload_PointerUnchanged` — three rows, one per category: file missing → `ErrReloadFileMissing`; decrypt failed → `ErrReloadDecryptFailed`; structurally invalid → `ErrReloadInvalid`. Each row asserts the active pointer is unchanged after the call and the wrapped error message contains the failing path but contains no byte from the (mocked) ciphertext or plaintext. Per FR-012, FR-013, SC-004.
- [X] T029 [P] [US2] Add to [internal/server/reload_test.go](internal/server/reload_test.go) `TestReloadVault_DrainWindowDestroysOnce` — inject a `Clock` and a configurable `cfg.ReloadDrainWindow=50ms`; call `ReloadVault`; assert `oldStore.Destroy()` was NOT called immediately after `Swap` and IS called exactly once at or after the drain window elapses (use a test `vault.Store` whose `Destroy` increments a counter under a mutex). Per FR-011, SC-003.
- [X] T030 [P] [US2] Add to [internal/server/reload_test.go](internal/server/reload_test.go) `TestReloadVault_Serialised_TwoSighupsBackToBack` — issue two `ReloadVault` calls in rapid succession (the second arriving while the first is still inside its drain window); assert the previous vault's `Destroy` ran before the second swap; assert both old vaults are destroyed exactly once across the test. Per FR-014, SC-005, `contracts/reload.md` §Serialisation rule.
- [X] T031 [P] [US2] Add to [internal/server/reload_test.go](internal/server/reload_test.go) `TestReloadVault_DuringShutdown_ReturnsErrShuttingDown` — set `s.shuttingDown.Store(true)` then call `ReloadVault`; assert it returns `errors.Is(err, ErrShuttingDown)`; assert active pointer unchanged. Per FR-015.
- [X] T032 [P] [US2] Add to [internal/server/reload_test.go](internal/server/reload_test.go) `TestVaultPointerSwap_NoRace` — spawn N=100 reader goroutines that loop `vaultPtr.Load()` for the duration of the test, plus M=10 reload goroutines that loop `ReloadVault` (with a tiny drain window so the test completes in seconds); assert no panic, no use-after-destroy, no goroutine leak. Designed to run under `go test -race`. Per SC-010.
- [X] T033 [US2] Write [internal/server/integration_test.go](internal/server/integration_test.go) (`//go:build integration`) `TestSIGHUP_AtomicReload` — start a real chassis with a real vault A on disk in `t.TempDir()`; mount a probe handler that reads `vaultPtr.Load()` and a per-request artificial sleep gate (channel-controlled) so a request can be paused inside the handler; begin a slow request that captures vault A and stalls in the handler; replace the on-disk vault with vault B; send SIGHUP to `os.Getpid()`; release the slow request; assert: the slow request's response carries A's value, a fresh request after the swap returns B's value, A's protected memory is zeroed exactly once at or after the drain window has elapsed (verify by holding a reference to A's `Destroy` counter or by sampling A's underlying `SecureBytes` after the drain). Per AC-2 SIGHUP-reload half, SC-003, SC-010.

**Implementation for User Story 2:**

- [X] T034 [P] [US2] Implement [internal/server/reload.go](internal/server/reload.go) — the `(*Server).runReload(ctx, newPath, key)` coordinator (locks `reloadMu`, checks `shuttingDown`, calls the injected vault-load seam, swaps `vaultPtr`, schedules the drain under the same mutex per `contracts/reload.md` §Serialisation, calls `oldStore.Destroy`, releases the mutex), the public `(*Server).ReloadVault(ctx, newPath, key)` entry that delegates to `runReload`, and the SIGHUP signal-loop goroutine started inside `Run` (research.md R8): `signal.Notify(sigCh, syscall.SIGHUP)`, loop `select` on `ctx.Done()` / `sigCh`, ignore signals while `shuttingDown` is true. The reload error categoriser maps the underlying `vault.Load` error to one of `ErrReloadFileMissing`, `ErrReloadDecryptFailed`, `ErrReloadInvalid`; the wrapped error includes the failing path but no vault bytes (FR-013). T027–T032 must pass.
- [X] T035 [US2] Add `loadVaultFn` to `Deps` (default `vault.Load`) and pipe the SIGHUP-driven path through it inside [internal/server/reload.go](internal/server/reload.go); update [internal/server/server.go](internal/server/server.go) `New` to default `loadVaultFn` to `vault.Load` when nil. The SIGHUP handler invokes `runReload` with `s.cfg.VaultPath()` and a server-held `vaultKey *securebytes.SecureBytes` captured at construction (added to `Deps` as `VaultKey`). T033 must pass on the integration tag.
- [X] T036 [US2] Wire the audit emission for reloads in [internal/server/reload.go](internal/server/reload.go) — emit `AuditVaultReloaded` with `Detail["from_path"]` (sanitised), `Detail["to_path"]=newPath`, and the `RequestID` left empty (reload is not request-scoped). Failure of the audit write logs at WARN; never blocks the reload. Per `contracts/reload.md` §Audit emission.

**Checkpoint**: User Story 2 fully functional. AC-2 SIGHUP-reload row is now testable; race detector runs clean across the swap.

---

## Phase 5: User Story 3 — A panic in a handler is captured without leaking the request body (Priority: P1)

**Goal**: Recover middleware logs panic + stack + request_id at ERROR; never the request body. Returns `500 Internal Server Error` with a generic body. Process stays alive. Second-level panic is caught and fails closed for that single request only.

**Independent Test**: Mount a panicking handler; send a request whose body contains the sentinel `SECRET_SHOULD_NEVER_APPEAR_10_<random32hex>`; capture the slog output; assert log entry contains panic value, stack, request ID, AND does NOT contain the sentinel. Response body contains no panic detail. SC-006.

**Tests for User Story 3 (write FIRST, MUST FAIL):**

- [X] T037 [P] [US3] Write [internal/server/middleware_test.go](internal/server/middleware_test.go) `TestMiddleware_RecoverLogsStackNoBody` — capture slog output via a `bytes.Buffer`-backed `slog.NewJSONHandler`; mount a handler that panics with the literal value `"sentinel-panic-value"`; send a POST request with body containing `SECRET_SHOULD_NEVER_APPEAR_10_<random32hex>`; assert the captured JSON log entry contains `"panic":"sentinel-panic-value"`, contains a `"stack":` field with non-empty content, contains the request_id; substring-asserts the entry does NOT contain `SECRET_SHOULD_NEVER_APPEAR_10` AND does NOT contain the random suffix; asserts the HTTP response status is 500 and the response body is the generic `"internal server error\n"` with no fragment of the panic value, the stack, or the request body. Per FR-019, FR-020, SC-006, research.md R13.
- [X] T038 [P] [US3] Add to [internal/server/middleware_test.go](internal/server/middleware_test.go) `TestMiddleware_Recover_SecondLevelPanic_FailsClosedForOneRequest` — mount a handler that panics, AND inject a "logger that panics" (a `slog.Handler` whose `Handle` itself panics) for ONE request only. Assert: that single request returns a 500 (or any HTTP error response — the inner connection may simply be cut), the server process stays alive (subsequent request on a fresh connection succeeds with a normal handler), no goroutine leak. Per FR-019 edge case 4, research.md R12.
- [X] T039 [P] [US3] Add to [internal/server/middleware_test.go](internal/server/middleware_test.go) `TestMiddleware_BodyCap_413` — send a request body of 65 KiB (1 byte over the cap); assert HTTP `413 Payload Too Large` and the handler was never invoked. Per `contracts/api-routes.md` §Status codes and research.md R2.

**Implementation for User Story 3:**

- [X] T040 [US3] Implement the recover middleware and the body-cap wrapper in [internal/server/middleware.go](internal/server/middleware.go) — `recoverMiddleware(logger, audit) func(http.Handler) http.Handler` (per research.md R12: outer `defer recover()` logs panic, stack via `runtime/debug.Stack()`, request_id, returns `http.StatusInternalServerError` with body `"internal server error\n"`, and emits one `AuditPanicCaptured` event; the inner `defer recover()` swallows a second-level panic with a minimal log carrying only the request_id). The body-cap wrapper applies `http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)` to every request before the recover layer wraps it. T037, T038, T039 must pass.

**Checkpoint**: User Story 3 fully functional. The redaction property holds independently of the rest of the middleware chain.

---

## Phase 6: User Story 4 — Every request carries a stable, server-generated request ID (Priority: P1)

**Goal**: Every request gets a fresh 16-byte hex ID from `crypto/rand`; client-supplied `X-Request-ID` headers are ignored; the ID is visible to handlers via `RequestID(ctx)` and appears in every log line emitted in service of the request.

**Independent Test**: Send N≥100 requests with forged `X-Request-ID` headers; assert all chassis-assigned IDs are unique and none equals any forged value; every log entry produced for each request carries that request's ID. SC-007.

**Tests for User Story 4 (write FIRST, MUST FAIL):**

- [X] T041 [P] [US4] Write [internal/server/middleware_test.go](internal/server/middleware_test.go) `TestMiddleware_RequestIDStable` — mount a handler that echoes `RequestID(r.Context())` in the response body. Send N=100 requests, each carrying a forged `X-Request-ID: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa` header (and a few rows with other common header names: `Request-Id`, `X-Correlation-ID`). Assert: each response body is a 32-char lowercase hex string; the set of N IDs is unique; none equals the forged value; each captured log line for that request carries `request_id=<that ID>`. Per FR-016, FR-017, SC-007.

**Implementation for User Story 4:**

- [X] T042 [US4] Implement the request-ID middleware in [internal/server/middleware.go](internal/server/middleware.go) — `requestIDMiddleware() func(http.Handler) http.Handler` reads 16 bytes from `crypto/rand.Read`, hex-encodes them, stores under `requestIDKey` in the request context, and never reads any incoming header. Wire it as the first middleware in the chain inside [internal/server/router.go](internal/server/router.go) (T046 below). The slog logger carried in `Server` is wrapped at request time with `logger.With("request_id", id)` so all subsequent log lines from the request goroutine carry the ID. T041 must pass.

**Checkpoint**: User Story 4 fully functional. Every audit and operational log line emitted from the request goroutine is correlatable.

---

## Phase 7: User Story 5 — Requests from a non-allow-listed client IP are rejected before any handler runs (Priority: P1)

**Goal**: Inspect socket-level peer IP against `Cfg.Network.AllowedCIDRs`; on miss, return `403 Forbidden` and emit `AuditAuthFailedNotAllowed`; never invoke any handler, never read any vault, never verify any signature.

**Independent Test**: Configure allow-list `[A]`; mount a probe handler that records every invocation; send from A → reaches handler; send from B → 403, handler not invoked, log/audit entry carries request_id and source IP. SC-008.

**Tests for User Story 5 (write FIRST, MUST FAIL):**

- [X] T043 [P] [US5] Write [internal/server/middleware_test.go](internal/server/middleware_test.go) `TestMiddleware_IPAllowListBlocks` — configure allow-list with one CIDR; use `httptest.NewRequest` with `r.RemoteAddr` set to an address inside the CIDR (allowed) and one outside (blocked); assert: allowed → reaches the probe handler; blocked → HTTP `403 Forbidden`, response body `"forbidden\n"`, probe handler invocation count unchanged, exactly one `AuditAuthFailedNotAllowed` event written carrying `RequestID`, `ClientIP`. Also assert the chassis takes the source IP from the socket only — a header `X-Forwarded-For: <allowed CIDR>` on a request whose `RemoteAddr` is outside is still rejected. Per FR-018, SC-008, edge case "trusts socket-level peer".

**Implementation for User Story 5:**

- [X] T044 [US5] Implement the IP allow-list middleware in [internal/server/middleware.go](internal/server/middleware.go) — `ipAllowListMiddleware(allowed []netip.Prefix, audit AuditWriter) func(http.Handler) http.Handler`. Parse `r.RemoteAddr` via `netip.ParseAddrPort`; compare the address against the slice of `netip.Prefix`; on miss, write `403 Forbidden` with body `"forbidden\n"`, emit `AuditAuthFailedNotAllowed` carrying request_id and source IP (via `audit.Write`), do not call `next`. Place it AFTER the request-ID middleware and BEFORE the body-cap wrapper. T043 must pass.

**Checkpoint**: User Story 5 fully functional. `AC-1` "serves over Tailscale" gains its application-layer perimeter; FR-026 ordering invariant holds.

---

## Phase 8: User Story 6 — The server shuts down gracefully when its lifecycle context is cancelled (Priority: P2)

**Goal**: Cancellation of `Run`'s ctx triggers `httpServer.Shutdown(cfg.ShutdownTimeout)`, then `drainWG.Wait` so any pending reload's `Destroy` finishes, then `Run` returns; SIGHUP arriving during shutdown is dropped.

**Independent Test**: Start; begin slow in-flight request; cancel ctx; in-flight completes; new connections refused from the moment of cancel; `Run` returns within `ShutdownTimeout`. SC-009.

**Tests for User Story 6 (write FIRST, MUST FAIL):**

- [X] T045 [P] [US6] Write [internal/server/integration_test.go](internal/server/integration_test.go) (`//go:build integration`) `TestRun_GracefulShutdown_DrainsInflight` — start the chassis on a Tailscale-CGNAT host; mount a slow handler whose response is held by a channel; begin a request that enters the slow handler; cancel `ctx`; assert: the slow request completes; an attempt to open a new connection after cancel is refused; `Run` returns nil within `cfg.ShutdownTimeout`; if a SIGHUP is delivered during the shutdown window it is dropped (active vault unchanged). Per FR-024, FR-015, FR-025, SC-009.

**Implementation for User Story 6:**

- [X] T046 [US6] Implement `(*Server).Run(ctx)` in [internal/server/server.go](internal/server/server.go) — gate against repeat calls via `runOnce` (returning `ErrAlreadyRun` on a second call); run startup checks (T026); construct the `*http.ServeMux`, install the middleware chain in the locked order from `contracts/api-routes.md` (request ID → IP allow-list → body cap → recover → handler), apply each captured `Mount` tuple to the mux; configure the `*http.Server` with the timeout defaults from T003 and the mounted handler; spawn the SIGHUP loop (T034); spawn `httpServer.ListenAndServe` in a goroutine; emit `AuditServerStart` with `status="ok"`; block on `ctx.Done()`; on cancellation: `shuttingDown.Store(true)`, derive `shutdownCtx` from `context.Background()` with timeout `cfg.ShutdownTimeout`, call `httpServer.Shutdown(shutdownCtx)`, close the chassis-internal `shutdownDeadlineCh` so any active drain wakes early, `drainWG.Wait()`, `signal.Stop(sigCh)`, emit `AuditServerStop`, return. NO `ctx` stored in the struct — only flows through closures (Constitution IX). T045 must pass.
- [X] T047 [US6] Implement `(*Server).Mount(method, path, h)` in [internal/server/router.go](internal/server/router.go) — pre-Run: append `(method, path, h)` to a per-server slice; post-Run: return `ErrAlreadyRun`. The slice is consumed inside `Run` (T046) when the mux is built. Per `contracts/api-routes.md`. (Test for `Mount`'s pre-Run-only behaviour is part of T024's happy-path integration; a unit case in [internal/server/router_test.go](internal/server/router_test.go) `TestMount_AfterRunReturnsAlreadyRun` covers the post-Run rejection.)
- [X] T048 [P] [US6] Write [internal/server/router_test.go](internal/server/router_test.go) `TestMount_AfterRunReturnsAlreadyRun` and `TestRouter_PrefixMount` — first asserts `Mount` returns `ErrAlreadyRun` after a successful `Run` start; second asserts a registered handler `("POST", "/claim", h)` is reachable at the effective path `/h/<prefix>/claim` and unknown paths return 404. Use `httptest.NewServer` with a chassis that injects fakes for the startup-check probes so the router can be exercised without binding a Tailscale interface. (This task may live alongside T047 — group with US6 because the post-Run gate semantics are part of the lifecycle contract.)

**Checkpoint**: User Story 6 fully functional. AC-1 "graceful start/stop" holds; AC-2's "drain across exit" half is verified.

---

## Phase 9: User Story 7 — The Approver dependency is a typed interface placeholder (Priority: P2)

**Goal**: The chassis carries no concrete approver; it accepts any `Approver` value as a construction-time dependency; tests use a `fakeApprover` that records calls.

**Independent Test**: Construct a server with a recording `fakeApprover`; mount a handler that calls `srv.Deps().Approver.RequestApproval(...)` (or test the chassis-internal hook by exposing a test-only handler that surfaces the approver); assert the fake's call log shows exactly the expected parameters and the scripted decision is returned. (Note: the chassis itself does not invoke `RequestApproval` — SDD-12's claim handler will. This story locks the *interface shape* and the *injection*.)

**Tests for User Story 7 (write FIRST, MUST FAIL):**

- [X] T049 [P] [US7] Add to [internal/server/approver_test.go](internal/server/approver_test.go) (created in T004) `TestApprover_FakeImplements` — declare a `fakeApprover` per `contracts/approver.md` §Test fakes; assert it satisfies `Approver`; construct a `*Server` via `New` with the fake supplied as `Deps.Approver`; assert the chassis stored the fake unchanged and a test handler that invokes `srv.approver().RequestApproval(...)` (test-only accessor, see T050) returns the scripted decision and records the call. Per User Story 7 acceptance scenarios.

**Implementation for User Story 7:**

- [X] T050 [US7] Add a test-only accessor `(*Server).approver() Approver` (unexported but visible to `_test.go` in the same package) in [internal/server/server.go](internal/server/server.go) so tests can validate the chassis stored the fake. Per data-model.md §Server. T049 must pass.

**Checkpoint**: All seven user stories are independently functional. AC-1, AC-2, AC-8 are now testable end-to-end.

---

## Phase 10: Polish & Cross-Cutting Concerns

**Purpose**: Tighten coverage, run gates, lock the API in PACKAGE-MAP, flip the AC-MATRIX rows, mark the chunk done.

- [X] T051 [P] Add the negative-coverage gap-fillers to [internal/server/server_test.go](internal/server/server_test.go), [internal/server/middleware_test.go](internal/server/middleware_test.go), and [internal/server/reload_test.go](internal/server/reload_test.go) for any line uncovered after T004–T050: each branch of `runReload`'s error categoriser (file missing, decrypt failed, invalid), the second-level panic path in recover (T038 plus an additional case where `slog.Handle` succeeds but `http.Error` panics — fail closed), the no-op skip cases for `RequireNTPSync=false` and `RequireFileModeChecks=false`. Per research.md R15 coverage matrix.
- [X] T052 [P] Run `go test -cover ./internal/server/...` and confirm coverage ≥ 95%. If short, add the matching test rows from research.md R15 — never lower the bar by removing branches. Per Constitution VIII and SC-013.
- [X] T053 Run `magex format:fix` from the repo root. Resolve any formatting changes by re-running tests if the formatter touches a file in `internal/server/`. (Shared Polish; non-parallel because it touches every package.)
- [X] T054 Run `magex lint` from the repo root and fix any new warnings introduced by `internal/server/`. The bar is zero new warnings against `master`. Constitution VIII.
- [X] T055 Run `magex test:race` from the repo root. The race detector MUST run clean — `TestVaultPointerSwap_NoRace` (T032) is the load-bearing case. Per SC-010.
- [X] T056 Run `magex test:race -tags=integration` from the repo root. Both `TestStartupChecks_HappyPath` (T024), `TestSIGHUP_AtomicReload` (T033), and `TestRun_GracefulShutdown_DrainsInflight` (T045) MUST pass under `-race`. Per AC-2 SIGHUP, SC-009, SC-010.
- [X] T057 Append the "Exported API — locked at SDD-10" section to [docs/PACKAGE-MAP.md](docs/PACKAGE-MAP.md) under the `internal/server` row, listing the locked surface from plan.md (`Server`, `Deps`, `Approver`, `ApprovalRequest`, `Decision`, `SessionType`, `AuditWriter`, `AuditEvent`, `AuditEventType`, `RequestID`, the `Err*` sentinels, `New`, `Run`, `ReloadVault`, `Mount`). Per SDD-10 Prompt 5 step 5.
- [X] T058 [P] Update [docs/AC-MATRIX.md](docs/AC-MATRIX.md) AC-1, AC-2, and AC-8 rows: link each to the matching test files (`internal/server/server_test.go`, `internal/server/startup_checks_test.go`, `internal/server/reload_test.go`, `internal/server/middleware_test.go`, `internal/server/integration_test.go`), with the specific test names. Per SDD-10 Prompt 5 step 6.
- [X] T059 [P] Mark SDD-10 status `done` in [docs/SDD-PLAYBOOK.md](docs/SDD-PLAYBOOK.md). Per SDD-10 Prompt 5 step 7.
- [X] T060 Validate the quickstart wiring works end-to-end: from the repo root, build the dummy `cmd/hush serve` skeleton (or a `go run` shim if `cmd/hush` is not yet implemented) using the [specs/010-server-skeleton/quickstart.md](specs/010-server-skeleton/quickstart.md) wiring as the reference, and confirm the chassis returns one of the named startup-check sentinels on a deliberately misconfigured host (e.g. `STATE_DIR=/nonexistent`). Per quickstart.md "Common errors". (If `cmd/hush` does not yet exist, this task is satisfied by an inline `_test.go` smoke check that wires the chassis exactly as the quickstart shows.)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup, T001–T003)**: no dependencies — can start immediately.
- **Phase 2 (Foundational, T004–T015)**: depends on Phase 1. **BLOCKS all user stories.**
- **Phase 3 (US1, T016–T026)**: depends on Phase 2.
- **Phase 4 (US2, T027–T036)**: depends on Phase 2.
- **Phase 5 (US3, T037–T040)**: depends on Phase 2.
- **Phase 6 (US4, T041–T042)**: depends on Phase 2.
- **Phase 7 (US5, T043–T044)**: depends on Phase 2.
- **Phase 8 (US6, T045–T048)**: depends on Phase 2 AND on Phase 6 + Phase 7 + Phase 5 (because `Run` wires the full middleware chain in T046). The `TestRun_GracefulShutdown_DrainsInflight` integration test additionally depends on Phase 3 (startup checks must pass to reach the listener) and Phase 4 (the shutdown waits on `drainWG`).
- **Phase 9 (US7, T049–T050)**: depends on Phase 2; independent of all other user stories.
- **Phase 10 (Polish, T051–T060)**: depends on every prior phase.

### Within Each User Story

- **TDD-mandatory**: every test task (T0xx) precedes the implementation task it pins; the test MUST be written and FAIL before the implementation lands. Constitution VIII.
- Models / interfaces (Phase 2) before services (each user story's middleware/lifecycle work).
- Each user story is independently testable — Phase 3, 4, 5, 6, 7, 9 can be staffed in parallel after Phase 2 lands. Phase 8 is the integration point.

### Parallel Opportunities

- All `[P]` tasks within Phase 1 (T002, T003) can run together.
- Within Phase 2: the type-shape tests (T004, T006, T008, T013, T014) can be authored in parallel; their implementations (T005, T007, T009, T015) follow each.
- Within Phase 3: T016–T023 are all `[P]` (they live in the same test file but each adds a new top-level `Test*` function — Go's `t.Parallel()` model permits parallel authoring; the implementations T025 and T026 are sequential because T026 depends on T025).
- Phases 3, 4, 5, 6, 7 can run in parallel by different developers once Phase 2 is done. Phase 9 is also independent of these.
- Phase 10's documentation and matrix tasks (T057, T058, T059) can run in parallel.

---

## Parallel Example: User Story 1

```bash
# After Phase 2 lands, launch the US1 test authoring tasks together:
Task: "T016 [P] [US1] TestStartupChecks_RefusesUnsyncedClock in internal/server/startup_checks_test.go"
Task: "T017 [P] [US1] TestStartupChecks_RefusesClockDriftOver60s"
Task: "T018 [P] [US1] TestStartupChecks_RefusesLooseFileMode"
Task: "T019 [P] [US1] TestStartupChecks_RefusesLooseDirMode"
Task: "T020 [P] [US1] TestStartupChecks_RefusesPublicBind"
Task: "T021 [P] [US1] TestStartupChecks_RefusesUnsafeStateDir"
Task: "T022 [P] [US1] TestStartupChecks_OrderedExecution"
Task: "T023 [P] [US1] TestStartupChecks_AuditEmitsRefused"

# Once all the above FAIL, implement:
Task: "T025 [US1] startup_checks.go check methods + driver"
Task: "T026 [US1] wire runStartupChecks into Run"
```

---

## Implementation Strategy

### MVP (the P1 trio: US1 + US2 + US3 — required to flip AC-1 / AC-2 / AC-8)

1. Phase 1 (Setup) — T001–T003.
2. Phase 2 (Foundational) — T004–T015.
3. Phase 3 (US1: refuse-to-start) — T016–T026. AC-8 row testable.
4. Phase 4 (US2: SIGHUP atomic reload) — T027–T036. AC-2 SIGHUP-half row testable.
5. Phase 5 (US3: panic recover, no body leak) — T037–T040.
6. Phase 6 (US4: request ID) — T041–T042. (Required to land before Phase 8 because `Run` wires the full middleware chain.)
7. Phase 7 (US5: IP allow-list) — T043–T044. (Same reason.)
8. **STOP and VALIDATE**: run unit + integration tests. AC-1 / AC-2 SIGHUP / AC-8 all green.

### Incremental Delivery

- Phases 1 + 2 → foundation in place.
- Add Phase 3 → refuse-to-start testable on a misconfigured host.
- Add Phase 4 → SIGHUP atomic reload integration test runs green under `-race -tags=integration`.
- Add Phase 5, 6, 7 → middleware chain complete; `TestMiddleware_*` suite green.
- Add Phase 8 → graceful shutdown verified end-to-end.
- Add Phase 9 → Approver placeholder locked; SDD-11 unblocked.
- Add Phase 10 → all gates pass (`magex format:fix && magex lint && magex test:race && magex test:race -tags=integration`); coverage ≥ 95%; PACKAGE-MAP, AC-MATRIX, SDD-PLAYBOOK updated.

### Single-Developer Strategy

A solo developer should follow MVP order strictly. Phase 8 (US6) and Phase 9 (US7) can be deferred only if AC-1's "graceful shutdown" half is acceptable as a follow-up — but per SDD-10 contract, SDD-12 / SDD-13 / SDD-14 are blocked until the chassis ships fully, so deferring Phase 8 / 9 also defers SDD-11+.

---

## Notes

- Tests are **mandatory** in this chunk per Constitution VIII (TDD-mandatory). Every test task (`T0xx`) precedes its implementation task. Verify tests fail before implementing.
- `[P]` tasks operate on different files OR add new top-level `Test*` functions to a shared file — Go's package-level test layout permits the latter without merge churn when each test is added in its own commit / PR slice.
- The chunk's commit policy (per [docs/sdd/SDD-10.md](docs/sdd/SDD-10.md) §"How to run this chunk") defers commits to a single combined commit at the end of the IMPLEMENT phase. Do not commit between Phase 1–10 tasks during the IMPLEMENT session.
- Avoid: storing `ctx` in any struct field (Constitution IX, FR-024); using `init()` (FR-028); binding to `0.0.0.0` (Constitution VI, FR-006); logging request bodies anywhere (Constitution X, FR-020); third-party HTTP routers (Constitution XI, research.md R1).
- Final phase gates (T053–T056) are **non-negotiable**: `magex format:fix`, `magex lint`, `magex test:race`, `magex test:race -tags=integration` must all pass clean before SDD-10 is marked `done`.
