---
description: "SDD-22 task list — internal/supervise PID file + Unix status socket"
---

# Tasks: Supervisor PID File + Unix Status Socket (SDD-22)

**Input**: Design documents from `/specs/022-supervise-pidfile-socket/`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/api.md ✅, quickstart.md ✅

**Tests**: TDD-mandatory per Constitution VIII. Every behaviour contract MUST have a failing test written BEFORE its implementation task. Coverage gate: ≥95% on `pidfile.go` + `socket.go` + platform shims via `go test -race -cover ./internal/supervise/ -run "PidFile|Socket"` (SC-022-10).

**Organization**: Tasks are grouped by user story (US1–US5 from spec.md) to enable independent verification. Note: PidFile primitive is shared between US1 + US2; StatusServer primitive is shared between US3 + US4 + US5. Story-tagged tasks group tests by the behaviour they prove, even when they touch a file added under an earlier story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Different file, no dependency on an incomplete task → safe to run in parallel.
- **[Story]**: Maps a task to one of US1, US2, US3, US4, US5.
- File paths are absolute or rooted at the repo. All paths are under `internal/supervise/` unless noted.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Scaffold the chunk's new files with type declarations and stub bodies so test files compile against the locked API surface. **No behaviour yet** — every stub returns a `not-implemented` sentinel or panics. This unblocks parallel TDD work in Phases 3+.

- [X] T001 Verify working tree is on branch `022-supervise-pidfile-socket` and `internal/supervise/` exists (SDD-18..21 already present); abort if not.
- [X] T002 [P] Create [internal/supervise/pidfile.go](internal/supervise/pidfile.go) with `package supervise`, imports (`errors`, `fmt`, `io/fs`, `os`, `strconv`, `golang.org/x/sys/unix`), exported `type PidFile struct{ fd *os.File; path string }`, stub `func AcquirePidFile(path string) (*PidFile, error) { return nil, errors.New("supervise: AcquirePidFile not implemented") }`, stub `func (p *PidFile) Release() error { return errors.New("supervise: Release not implemented") }`. NO logic yet — just enough for the test file to compile.
- [X] T003 [P] Create [internal/supervise/socket.go](internal/supervise/socket.go) with `package supervise`, imports (`context`, `encoding/json`, `errors`, `fmt`, `log/slog`, `net`, `os`, `path/filepath`, `sync`, `time`), exported `type StatusServer struct{ /* private fields per data-model.md §1 */ }`, exported `type StatusInputs interface { /* 8 getters per contracts/api.md §3 */ }`, stub `func NewStatusServer(socketPath string, store *Store, logger *slog.Logger) *StatusServer { return &StatusServer{} }`, stub `func (s *StatusServer) Run(ctx context.Context) error { return errors.New("supervise: Run not implemented") }`, stub package-private `func (s *StatusServer) attach(inputs StatusInputs) {}`, package-private DTO `type statusJSON struct { ... }` per data-model.md §1.
- [X] T004 [P] Create [internal/supervise/socket_darwin.go](internal/supervise/socket_darwin.go) with `//go:build darwin` build tag, `package supervise`, and a package-private `func defaultRuntimeDir() string` returning `os.UserCacheDir()+"/hush"` (or `os.TempDir()` on error) per research.md R-2.
- [X] T005 [P] Create [internal/supervise/socket_linux.go](internal/supervise/socket_linux.go) with `//go:build linux` build tag, `package supervise`, and a package-private `func defaultRuntimeDir() string` returning `os.Getenv("XDG_RUNTIME_DIR")` (or `os.TempDir()` if empty) per research.md R-2.
- [X] T006 [P] Create empty [internal/supervise/pidfile_test.go](internal/supervise/pidfile_test.go) with `package supervise` + standard testify imports (`testing`, `github.com/stretchr/testify/require`, `github.com/stretchr/testify/assert`); no tests yet.
- [X] T007 [P] Create empty [internal/supervise/socket_test.go](internal/supervise/socket_test.go) with `package supervise` + standard testify imports (`testing`, `context`, `encoding/json`, `net`, `os`, `path/filepath`, `runtime`, `sync`, `time`, `testify` packages); no tests yet.
- [X] T008 Run `go build ./internal/supervise/...` and confirm both test files compile against the stubs.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Sentinel errors and the shared parent-perms helper used by both PidFile and StatusServer. Both stories US1 + US5 depend on `ErrSocketPermsLoose`; all four PidFile stories depend on `ErrPidLocked`; US4 depends on `ErrAlreadyRunning`.

**⚠️ CRITICAL**: No user-story implementation can begin until this phase is complete.

- [X] T009 [P] Declare exported sentinel errors in [internal/supervise/pidfile.go](internal/supervise/pidfile.go): `var ErrPidLocked = errors.New("supervise: pidfile already locked")` and `var ErrSocketPermsLoose = errors.New("supervise: parent directory mode laxer than 0700")` and package-private `var errAlreadyReleased = errors.New("supervise: pidfile already released")` per data-model.md §1 and contracts/api.md §1.3.
- [X] T010 [P] Declare exported sentinel `var ErrAlreadyRunning = errors.New("supervise: status server already running")` in [internal/supervise/socket.go](internal/supervise/socket.go) per data-model.md §1 / contracts/api.md §1.3.
- [X] T011 Implement package-private helper `func ensureParentMode0700(parent string) error` in [internal/supervise/socket.go](internal/supervise/socket.go) per research.md R-4: `os.Stat(parent)` → if missing, `os.MkdirAll(parent, 0o700)`; if exists with mode laxer than `0o700`, return `fmt.Errorf("supervise: %w (path=%s mode=%v)", ErrSocketPermsLoose, parent, mode.Perm())`. Consumed by both `AcquirePidFile` and `(*StatusServer).Run`.
- [X] T012 [P] [US3] Add test helper `func tempSocketPath(t *testing.T) string` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go) using `t.TempDir()` plus `defaultRuntimeDir()`-style suffix; honours the per-OS shim from T004/T005.
- [X] T013 [P] [US3] Add test fixture `type stubStatusInputs struct { ... }` with field-backed implementations of every `StatusInputs` getter in [internal/supervise/socket_test.go](internal/supervise/socket_test.go) per data-model.md §7.

**Checkpoint**: sentinel errors + shared helper + test fixtures ready. User-story phases may now proceed in parallel.

---

## Phase 3: User Story 1 — Duplicate supervisor start is refused (Priority: P1) 🎯 MVP

**Goal**: A second `AcquirePidFile` against the same path while a live owner holds the flock returns `errors.Is(err, ErrPidLocked)` immediately, without blocking, without modifying the file (FR-022-1, FR-022-2; AC-10 Lifecycle Scenario 14).

**Independent Test**: From a single Go test, acquire the PID file via one fd, then call `AcquirePidFile` again with the same path from a sibling fd; second call returns `ErrPidLocked` within milliseconds and the first owner's PID-file contents are unchanged.

### Tests for User Story 1 ⚠️ (write FIRST — must FAIL before implementation)

- [X] T014 [P] [US1] Write `TestPidFile_FlockExclusive` in [internal/supervise/pidfile_test.go](internal/supervise/pidfile_test.go): construct two `AcquirePidFile` attempts on the same `t.TempDir()`-rooted path; assert first succeeds, second returns non-nil error; cleanup via `Release` on the first holder (FR-022-1; contracts/api.md §5.1).
- [X] T015 [P] [US1] Write `TestPidFile_DuplicateRefused` in [internal/supervise/pidfile_test.go](internal/supervise/pidfile_test.go): like FlockExclusive but assert `errors.Is(err2, ErrPidLocked)` AND that the file's text content equals the first owner's PID (NOT modified by the refused attempt). Stamp `time.Now()` before second attempt; assert elapsed < 100ms (SC-022-2 — well below normal startup time) (FR-022-2; contracts/api.md §5.1).
- [X] T016 Run `go test -run "TestPidFile_FlockExclusive|TestPidFile_DuplicateRefused" -v ./internal/supervise/` and confirm BOTH tests FAIL (stub returns generic error). Verifies tests are genuinely red.

### Implementation for User Story 1

- [X] T017 [US1] Implement `AcquirePidFile(path)` core flow in [internal/supervise/pidfile.go](internal/supervise/pidfile.go) per research.md R-5: `ensureParentMode0700(filepath.Dir(path))` → `os.OpenFile(path, O_RDWR|O_CREATE, 0o600)` → `unix.Flock(int(fd.Fd()), unix.LOCK_EX|unix.LOCK_NB)` → on `EWOULDBLOCK` close fd and return `fmt.Errorf("supervise: pidfile flock: %w", ErrPidLocked)`; on success, `fd.Truncate(0)` + `fd.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)`; return `&PidFile{fd: fd, path: path}, nil`. NEVER parse existing PID text (FR-022-3).
- [X] T018 [US1] Re-run T016's tests; both must now PASS. Stamp `time.Now()` in the test to enforce the SC-022-2 sub-millisecond bound.

**Checkpoint**: AC-10 Lifecycle Scenario 14 has a passing unit test (SC-022-1 — first half).

---

## Phase 4: User Story 2 — Crashed supervisor recovers cleanly on next start (Priority: P1)

**Goal**: When the previous owner died without releasing the flock, the OS auto-releases the lock at process death; the next `AcquirePidFile` against the same path succeeds without operator intervention, without parsing the stale PID, without `kill(0)` probing (FR-022-3, FR-022-4, FR-022-5).

**Independent Test**: Spawn a sub-process via `os/exec` that calls `AcquirePidFile` and exits without `Release`; in the parent process, after `cmd.Wait` returns, call `AcquirePidFile` on the same path; assert success and that the file's text now contains the parent's PID.

### Tests for User Story 2 ⚠️ (write FIRST — must FAIL or be unimplemented before implementation)

- [X] T019 [P] [US2] Write `TestPidFile_StaleAcquired` in [internal/supervise/pidfile_test.go](internal/supervise/pidfile_test.go): spawn a sub-process (use `os/exec` with a small Go helper main built behind a `TEST_PIDFILE_HELPER` env-var convention or a separate `_test.go` `TestMain` re-entry pattern — pick whichever matches the existing supervise test idiom from `child_test.go`); helper acquires `t.TempDir()`-rooted path, exits without `Release`; parent waits, then `AcquirePidFile` on the same path; assert no error, file contents `== strconv.Itoa(os.Getpid())` (FR-022-3; AC-10 Lifecycle Scenario "previous owner died"; contracts/api.md §5.1).
- [X] T020 [P] [US2] Write `TestPidFile_ReleaseRemovesFile` in [internal/supervise/pidfile_test.go](internal/supervise/pidfile_test.go): `AcquirePidFile` → `Release` → assert `os.Stat(path)` returns `errors.Is(err, fs.ErrNotExist)`; second `Release` on same `*PidFile` returns the package-private `errAlreadyReleased` (callers MUST treat it as a programmer error per research.md R-6) (FR-022-5; contracts/api.md §5.1).
- [X] T021 [P] [US2] Write `TestPidFile_WritesOwnPID` in [internal/supervise/pidfile_test.go](internal/supervise/pidfile_test.go): `AcquirePidFile` → read file contents → assert `string(contents) == strconv.Itoa(os.Getpid())` (FR-022-4; contracts/api.md §5.1).
- [X] T022 Run `go test -run "TestPidFile_StaleAcquired|TestPidFile_ReleaseRemovesFile|TestPidFile_WritesOwnPID" -v ./internal/supervise/` and confirm tests FAIL (T020 + T021 fail because `Release` is still the stub; T019's helper sub-process invocation also fails).

### Implementation for User Story 2

- [X] T023 [US2] Implement `(*PidFile).Release` in [internal/supervise/pidfile.go](internal/supervise/pidfile.go) per research.md R-6: nil-check → `p.fd == nil` returns `errAlreadyReleased`; capture `fd, path`; zero `p.fd`; `unix.Flock(int(fd.Fd()), unix.LOCK_UN)` → wrap on error; `fd.Close()` → wrap on error; `os.Remove(path)` → `errors.Is(err, fs.ErrNotExist)` is success, otherwise wrap and return.
- [X] T024 [US2] Re-run T022's tests; all must now PASS. The stale-acquired path is already correct from T017 (no PID parsing — the OS releases the flock on process death; our single `Flock` attempt succeeds).

**Checkpoint**: SC-022-3 (clean stale acquisition) has a passing unit test.

---

## Phase 5: User Story 3 — Agent inspects daemon status via the local socket (Priority: P1)

**Goal**: A local client of the same UID connects to the configured Unix socket and reads back a JSON document exactly matching `docs/SPEC.md` FR-12: 10 fields, correct names, correct types, no token bytes (FR-022-8, FR-022-12, FR-022-13, FR-022-16; AC-10 Lifecycle Scenario 12).

**Independent Test**: Construct a `*StatusServer` against a `t.TempDir()`-rooted path with a stub `StatusInputs`; spawn `Run(ctx)` in a goroutine; `net.Dial("unix", path)`; write `"status\n"`; read response; `json.Unmarshal` into a mirror struct; assert every FR-12 field is present with the documented type.

### Tests for User Story 3 ⚠️ (write FIRST — must FAIL before implementation)

- [X] T025 [P] [US3] Write `TestSocket_StatusJSONShape` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): table-driven; spawn server with stub `*Store` + `stubStatusInputs`; drive one request; `json.Unmarshal` into `map[string]json.RawMessage`; assert all 10 FR-12 keys present (`supervisor`, `session_expires_at`, `refresh_window_next`, `scope_healthy`, `scope_stale`, `last_auth_failure`, `child_pid`, `child_uptime`, `discord_connected`, `state`); assert each field's `json.RawMessage` parses into the documented Go type from contracts/api.md §2.1 (FR-022-12, SC-022-5).
- [X] T026 [P] [US3] Write `TestSocket_StatusJSONFromSnapshot` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): construct a `*Store` with `State=running`, `ChildPID=4242`, `Token=nil`; attach a `stubStatusInputs` with fixed `Name="openclaw"`, fixed RFC3339 timestamps, fixed scope arrays; drive one request; assert the response body byte-equals the reference example from contracts/api.md §2.2 (modulo the supervisor-specific values) (FR-022-12, FR-022-16).
- [X] T027 [P] [US3] Write `TestSocket_TokenInResponseRedacted` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): construct a `*Store` whose `Snapshot.Token` is a `*securebytes.SecureBytes` initialised with the marker bytes `"MARKER_d3adb33f"`; drive a request; assert `!bytes.Contains(body, []byte("MARKER_d3adb33f"))` AND `!bytes.Contains(body, []byte(`"token"`))` AND `!bytes.Contains(body, []byte("MARKER_d3adb"))` (prefix check) (FR-022-13, SC-022-6, Constitution X).
- [X] T028 [P] [US3] Write `TestSocket_PreAttachDefaultsRenderShapeConformant` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): construct `*StatusServer` WITHOUT calling `attach`; drive a request; assert all 10 FR-12 fields present with their pre-attach default values per contracts/api.md §2.3 (`supervisor: ""`, `session_expires_at: "0001-01-01T00:00:00Z"`, `scope_healthy: []`, `last_auth_failure: null`, `child_pid: null`, `child_uptime: "0s"`, `discord_connected: false`, `state: ""`).
- [X] T029 Run `go test -run "TestSocket_StatusJSON|TestSocket_TokenInResponseRedacted|TestSocket_PreAttachDefaults" -v ./internal/supervise/` and confirm all FAIL (Run still returns the stub error).

### Implementation for User Story 3

- [X] T030 [US3] Implement `statusJSON` DTO + private encoder `func (s *StatusServer) renderStatus(snap Snapshot) ([]byte, error)` in [internal/supervise/socket.go](internal/supervise/socket.go) per data-model.md §2 and research.md R-7: project Snapshot.State → `State`; Snapshot.ChildPID → `*int` (`nil` when `0`); call `s.inputs` getters with nil-guard (pre-attach renders zero values per contracts/api.md §2.3); render times via `t.Format(time.RFC3339)`; render duration via `d.String()`; ensure `scope_healthy`/`scope_stale` initialised to `[]string{}` not nil. `Snapshot.Token` is intentionally NOT a field of `statusJSON` (Constitution X / FR-022-13).
- [X] T031 [US3] Implement `func (s *StatusServer) attach(inputs StatusInputs)` in [internal/supervise/socket.go](internal/supervise/socket.go): assigns under `s.mu`. Package-private per contracts/api.md §3.
- [X] T032 [US3] Implement `(*StatusServer).Run` pre-listen + accept loop core in [internal/supervise/socket.go](internal/supervise/socket.go) per research.md R-3 / R-4: `ensureParentMode0700(filepath.Dir(s.socketPath))` → `os.Remove(s.socketPath)` (ignore `IsNotExist`) → `net.Listen("unix", s.socketPath)` → `os.Chmod(s.socketPath, 0o600)` → accept loop in `Run`'s frame; on `Accept` returning a conn, spawn a per-connection handler goroutine that reads one line, takes ONE `store.Snapshot()`, calls `renderStatus`, writes the response, closes the conn. Each spawned goroutine has top-frame `recover()` and `wg.Done()` per Constitution IX.
- [X] T033 [US3] Re-run T029's tests; all must now PASS.

**Checkpoint**: AC-10 Lifecycle Scenario 12 has a passing unit test (SC-022-1 — second half). MVP is complete: P1 stories all pass.

---

## Phase 6: User Story 4 — Status server stops cleanly on lifecycle shutdown (Priority: P2)

**Goal**: On ctx-cancel, `Run` closes the listener, force-closes every in-flight per-connection handler via `conn.Close()`, joins every spawned goroutine, and returns `nil` within a sub-second bound. Stale socket inode from a previous run is unlinked pre-bind. Second `Run` on the same instance returns `ErrAlreadyRunning` (FR-022-11, FR-022-14, FR-022-14a, FR-022-17; SC-022-7, SC-022-8).

**Independent Test**: Start server bound to a temp path; cancel context; observe `Run` returns `nil` within sub-second bound. Run start/stop in a loop; assert `runtime.NumGoroutine()` returns to baseline.

### Tests for User Story 4 ⚠️ (write FIRST)

- [X] T034 [P] [US4] Write `TestSocket_GracefulShutdownOnCtx` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): start server; `cancelCtx`; stamp `time.Now()`; wait for `Run` to return; assert returned error is `nil`; assert `time.Since(stampStart) < 1*time.Second` (FR-022-14, SC-022-7, Clarification 3).
- [X] T035 [P] [US4] Write `TestSocket_ConnectionForceClosedOnCtxCancel` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): start server; `net.Dial`; client reads the response (server has not closed); cancel ctx WITHOUT closing the client; assert client's next `Read` returns `io.EOF` or `*net.OpError` within sub-second; assert `Run` returns within sub-second bound (FR-022-14, Clarification 3).
- [X] T036 [P] [US4] Write `TestSocket_RebindAfterStop` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): start server; cancel; wait for `Run` to return; construct a FRESH `*StatusServer` against the SAME path; start; assert `Run` does NOT return immediately (no `EADDRINUSE`) — bound is observed via a `select` with a 100ms timer or via a successful client connection (SC-022-7).
- [X] T037 [P] [US4] Write `TestSocket_RunSecondCallReturnsErrAlreadyRunning` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): start server in goroutine; while running, call `Run(ctx)` AGAIN on the same instance; assert second call returns `errors.Is(err, ErrAlreadyRunning)` without binding; cleanup via ctx cancel (FR-022-14a).
- [X] T038 [P] [US4] Write `TestSocket_NoGoroutineLeak` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): record `runtime.NumGoroutine()` baseline; loop 20 start/stop cycles; force a `runtime.GC()` + small sleep; assert post-loop count is within a small tolerance window (`<= baseline + 2`) (FR-022-17, SC-022-8).
- [X] T039 [P] [US4] Write `TestSocket_PreviousSocketCleanedUp` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): write a dummy regular file at the configured socket path (simulating a stale inode from a crashed prior run); start server; assert `Run` binds successfully and the stale inode is gone (FR-022-11).
- [X] T040 Run `go test -run "TestSocket_Graceful|TestSocket_Connection|TestSocket_Rebind|TestSocket_RunSecond|TestSocket_NoGoroutineLeak|TestSocket_PreviousSocketCleanedUp" -v ./internal/supervise/` and confirm all FAIL (no watcher / no force-close / no single-shot guard yet).

### Implementation for User Story 4

- [X] T041 [US4] Implement single-shot `Run` guard in [internal/supervise/socket.go](internal/supervise/socket.go) per research.md R-8: at the head of `Run`, take `s.mu`; if `s.started == true`, return `fmt.Errorf("supervise: %w", ErrAlreadyRunning)`; otherwise set `s.started = true` and release the lock. FR-022-14a.
- [X] T042 [US4] Implement watcher goroutine + conn-tracking map in [internal/supervise/socket.go](internal/supervise/socket.go) per research.md R-3 / data-model.md §3: `s.conns = map[net.Conn]struct{}{}` under `s.mu`; `done := make(chan struct{})`; `s.wg.Add(1); go s.watch(ctx, listener, done)` where `watch` blocks on `select { case <-ctx.Done(): … case <-done: }` and on ctx-done calls `listener.Close()` + ranges `s.conns` calling `conn.Close()` on each under `s.mu`. Top-frame `defer recover()` + `defer wg.Done()` per Constitution IX.
- [X] T043 [US4] Update accept loop in [internal/supervise/socket.go](internal/supervise/socket.go) to register each accepted conn in `s.conns` under `s.mu` BEFORE spawning the handler; handler `defer`s unregister-from-`s.conns` + `conn.Close()` + `wg.Done()`. On `Accept` returning `net.ErrClosed`, exit the loop; call `close(done)` to wake watcher if it has not already woken; `s.wg.Wait()` to join every goroutine; return `nil`.
- [X] T044 [US4] Re-run T040's tests; all must now PASS.

**Checkpoint**: SC-022-7 (clean rebind) + SC-022-8 (no goroutine leak) proven; AC-10 Scenario 14's "fresh `StatusServer` required for rebind" semantics enforced.

---

## Phase 7: User Story 5 — Filesystem permissions are the only authorization (Priority: P2)

**Goal**: Socket file mode is exactly `0600`; parent dir is `0700` (created if missing, REFUSED with `ErrSocketPermsLoose` if existing is laxer); symmetric rule applies to `pid_file` parent. Static-grep test asserts no `net.Listen("tcp"`, no `http.Server`, no bearer/authorization tokens in chunk files (FR-022-6, FR-022-9, FR-022-10, FR-022-15; SC-022-4, SC-022-9; Constitution V).

**Independent Test**: After server start, `stat` the socket path and assert mode `0o600`; `stat` the parent and assert `0o700`. Pre-create a `0o755` parent; assert `Run` returns `ErrSocketPermsLoose` and no listener exists.

### Tests for User Story 5 ⚠️ (write FIRST)

- [X] T045 [P] [US5] Write `TestPidFile_Mode0600` in [internal/supervise/pidfile_test.go](internal/supervise/pidfile_test.go): `AcquirePidFile` → `os.Stat(path)` → assert `Mode().Perm() == 0o600` (FR-022-6).
- [X] T046 [P] [US5] Write `TestPidFile_ParentMode0700Created` in [internal/supervise/pidfile_test.go](internal/supervise/pidfile_test.go): construct a path whose parent dir does NOT exist; `AcquirePidFile`; `os.Stat(filepath.Dir(path))` → assert `Mode().Perm() == 0o700` (FR-022-10).
- [X] T047 [P] [US5] Write `TestPidFile_ParentLooseRefuses` in [internal/supervise/pidfile_test.go](internal/supervise/pidfile_test.go): create the parent dir at `0o755`; `AcquirePidFile`; assert `errors.Is(err, ErrSocketPermsLoose)` AND `os.Stat(path)` returns `fs.ErrNotExist` (no file created on refusal) (FR-022-10, Clarification 1).
- [X] T048 [P] [US5] Write `TestSocket_Mode0600` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): start server; `os.Stat(socketPath)` → assert `Mode().Perm() == 0o600` (FR-022-9, SC-022-4).
- [X] T049 [P] [US5] Write `TestSocket_ParentMode0700` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): construct a `socketPath` whose parent dir does NOT exist; start server; `os.Stat(filepath.Dir(socketPath))` → assert `Mode().Perm() == 0o700` (FR-022-10, SC-022-4).
- [X] T050 [P] [US5] Write `TestSocket_ParentLooseRefuses` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go): pre-create parent dir at `0o755`; start server; assert `Run` returns `errors.Is(err, ErrSocketPermsLoose)`; assert no listener exists (a follow-up `net.Dial` returns connection-refused / `ENOENT`) (FR-022-10, Clarification 1).
- [X] T051 [P] [US5] Write `TestSocket_NoTCPListenerOrHTTPServer` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go) per research.md R-11: read each of `pidfile.go`, `socket.go`, `socket_darwin.go`, `socket_linux.go` via `os.ReadFile`; lowercase the contents; assert `!bytes.Contains` for each of `[]byte("net.listen(\"tcp\"")`, `[]byte("http.server")`, `[]byte("http.listenandserve")`, `[]byte("bearer")`, `[]byte("authorization")` (SC-022-9, Constitution V).
- [X] T052 Run `go test -run "TestPidFile_Mode|TestPidFile_Parent|TestSocket_Mode|TestSocket_Parent|TestSocket_NoTCPListener" -v ./internal/supervise/` and confirm all FAIL (mode enforcement + parent-perms refusal not wired through yet — T011's helper exists but T017 may not yet call it for PidFile, T032 may not yet enforce chmod). If T017/T032 already call `ensureParentMode0700` and chmod, some tests may already pass — note which pass naturally.

### Implementation for User Story 5

- [X] T053 [US5] Verify in [internal/supervise/pidfile.go](internal/supervise/pidfile.go) that `AcquirePidFile`'s first action is `ensureParentMode0700(filepath.Dir(path))` (added in T017); confirm the open uses mode `0o600`; if any test from T045-T047 still fails, fix the call order in `AcquirePidFile` so the parent-perms check is FIRST.
- [X] T054 [US5] Verify in [internal/supervise/socket.go](internal/supervise/socket.go) that `Run`'s pre-listen sequence is exactly per research.md R-4 step ordering (parent check → stale-inode unlink → Listen → chmod 0600). If any test from T048-T050 fails, fix the ordering. The `os.Chmod(socketPath, 0o600)` MUST be unconditional after `Listen` regardless of process umask.
- [X] T055 [US5] Re-run T052's tests; all must now PASS including `TestSocket_NoTCPListenerOrHTTPServer` (which is a static byte-grep — passes if no forbidden substrings appear in the chunk's source files).

**Checkpoint**: SC-022-4 (mode enforcement), SC-022-9 (no TCP/HTTP/bearer) proven. User Story 5 complete.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Final coverage / quality gates, fuzz seed, doc updates, single combined commit. **MUST include `magex format:fix`, `magex lint`, `magex test:race` per user input.**

### Fuzz target (Constitution VIII §Mandatory fuzz targets, item 6 — owned by this chunk per research.md R-10)

- [X] T056 [P] Author `FuzzStatusJSON_Encode` in [internal/supervise/socket_test.go](internal/supervise/socket_test.go) per research.md R-10: seed corpus from the table entries in `TestSocket_StatusJSONShape` plus the reference example from contracts/api.md §2.2; fuzz body feeds arbitrary `StatusInputs` return values + `Snapshot` shapes through `renderStatus`; assert no panic, no unbounded memory, output unmarshals into a `map[string]json.RawMessage` with all 10 FR-12 keys present. Save seed corpus under `testdata/fuzz/FuzzStatusJSON_Encode/`.
- [X] T057 Run `go test -fuzz=FuzzStatusJSON_Encode -fuzztime=10s ./internal/supervise/` and confirm no crashes during a short smoke run.

### Coverage verification (SC-022-10)

- [X] T058 Run `go test -race -cover ./internal/supervise/ -run "PidFile|Socket"` and capture the per-file coverage percentage. Confirm `pidfile.go` + `socket.go` + `socket_darwin.go` (on darwin) or `socket_linux.go` (on linux) are each ≥ 95%.
- [X] T059 If any covered file is below 95%, run `go test -race -coverprofile=/tmp/sdd22-cov.out ./internal/supervise/ -run "PidFile|Socket"` + `go tool cover -html=/tmp/sdd22-cov.out` and add targeted tests for uncovered branches (likely candidates per research.md R-12: error paths in watcher teardown, platform-shim fallbacks).

### Project-wide gates (MUST pass clean — quickstart.md §2.2)

- [X] T060 Run `magex format:fix` from repo root and confirm zero diff after the run (idempotent on a clean tree).
- [X] T061 Run `magex lint` from repo root and confirm zero findings.
- [X] T062 Run `magex test:race` from repo root and confirm the full suite is green and race-clean.

### Documentation updates (manual — quickstart.md §5)

- [X] T063 [P] Append a new "Exported API — locked at SDD-22" subsection to the `internal/supervise/` entry in [docs/PACKAGE-MAP.md](docs/PACKAGE-MAP.md) listing every exported symbol from contracts/api.md §1: `PidFile`, `AcquirePidFile`, `(*PidFile).Release`, `StatusServer`, `NewStatusServer`, `(*StatusServer).Run`, `StatusInputs` (interface), `ErrPidLocked`, `ErrSocketPermsLoose`, `ErrAlreadyRunning`.
- [X] T064 [P] Update the AC-10 row in [docs/AC-MATRIX.md](docs/AC-MATRIX.md) with the new test paths `internal/supervise/pidfile_test.go` + `internal/supervise/socket_test.go`; mark Scenarios 12 + 14 unit-level coverage as achieved by SDD-22 (SC-022-1).
- [X] T065 [P] Set SDD-22 status to `done` in [docs/SDD-PLAYBOOK.md](docs/SDD-PLAYBOOK.md).

### Combined commit (one atomic unit per SDD-22 Prompt-5 guidance)

- [X] T066 Stage and commit in a single commit:
  ```sh
  git add internal/supervise/pidfile.go internal/supervise/pidfile_test.go \
          internal/supervise/socket.go internal/supervise/socket_darwin.go \
          internal/supervise/socket_linux.go internal/supervise/socket_test.go \
          internal/supervise/testdata/ \
          docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/022-supervise-pidfile-socket/tasks.md
  git commit -m "feat(supervise): PID flock + Unix status socket (SDD-22)"
  ```
  Confirm pre-commit + gitleaks pass.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: no dependencies — runs immediately. T002–T007 are file-disjoint and run [P].
- **Phase 2 (Foundational)**: depends on Phase 1. Blocks every user story. T009 + T010 are [P] (different declarations in different files). T011 depends on T010 (uses `ErrSocketPermsLoose`). T012 + T013 are [P] (test-only helpers in `socket_test.go` — same file, so [P] applies only if you accept the convention "[P] = different concern, even same file"; here they touch disjoint additions and can be authored independently).
- **Phase 3 (US1)**: depends on Phase 2. Tests T014 + T015 are [P]. T017 is the implementation. T018 is verification.
- **Phase 4 (US2)**: depends on Phase 3 (extends `AcquirePidFile`'s `Release` semantics; tests rely on T017's flock acquisition working). Tests T019 + T020 + T021 are [P].
- **Phase 5 (US3)**: depends on Phase 2 (sentinel + helper). INDEPENDENT of US1 / US2 — different file (`socket.go` vs `pidfile.go`). Can run in parallel with US1 + US2 if a second contributor is on the chunk.
- **Phase 6 (US4)**: depends on Phase 5. Extends `Run`'s lifecycle.
- **Phase 7 (US5)**: depends on Phase 2 (helper) + Phase 3 + Phase 5 (the implementations being mode-tested). Tests T045-T051 are [P]. The "verify" tasks T053 + T054 may turn into no-ops if T017 + T032 already wired the perms correctly — that's fine.
- **Phase 8 (Polish)**: depends on every user-story phase. T060 + T061 + T062 run SEQUENTIALLY (format may change files; lint reads the formatted tree; test-race runs the full build). T063-T065 are [P] (three different docs files).

### Within Each User Story

- **TDD-mandatory**: tests are authored FIRST in each user-story phase. The "Run tests, confirm they FAIL" task (T016, T022, T029, T040, T052) gates progress to the implementation tasks. Per Constitution VIII this is non-negotiable.
- **Implementation order**: pure-value helpers (DTO, encoder) → I/O wrappers → lifecycle (`Run`, force-close, single-shot guard).
- **Verification**: each phase ends with a "re-run tests, confirm PASS" task.

### Parallel Opportunities

- **Phase 1**: T002 + T003 + T004 + T005 + T006 + T007 all in parallel — 6 distinct files.
- **Phase 2**: T009 + T010 in parallel; T012 + T013 in parallel.
- **Phase 3 tests**: T014 + T015 in parallel.
- **Phase 4 tests**: T019 + T020 + T021 in parallel.
- **Phase 5 tests**: T025 + T026 + T027 + T028 in parallel.
- **Phase 6 tests**: T034 + T035 + T036 + T037 + T038 + T039 in parallel.
- **Phase 7 tests**: T045 + T046 + T047 + T048 + T049 + T050 + T051 in parallel.
- **Phase 8 polish**: T056 ([P] on fuzz seed) and T063 + T064 + T065 ([P] on three doc files) in parallel.
- **Cross-phase**: US1+US2 (`pidfile.go`) and US3+US4+US5-socket-half (`socket.go`) operate on disjoint files and can be progressed by two contributors in parallel after Phase 2 completes.

---

## Parallel Example: User Story 3 tests

```bash
# Launch all P1-US3 tests together (different test bodies, same file but
# independent fixtures and assertions):
Task: "Write TestSocket_StatusJSONShape in internal/supervise/socket_test.go"
Task: "Write TestSocket_StatusJSONFromSnapshot in internal/supervise/socket_test.go"
Task: "Write TestSocket_TokenInResponseRedacted in internal/supervise/socket_test.go"
Task: "Write TestSocket_PreAttachDefaultsRenderShapeConformant in internal/supervise/socket_test.go"
```

---

## Implementation Strategy

### MVP-first (P1 only — US1 + US2 + US3)

1. Phase 1: scaffold the six files (~30 min).
2. Phase 2: sentinels + `ensureParentMode0700` (~30 min).
3. Phase 3: US1 — duplicate refusal proven (~1 hr).
4. Phase 4: US2 — stale acquired proven (~1 hr including sub-process helper).
5. Phase 5: US3 — agent reads status JSON proven (~2 hrs).
6. **VALIDATE**: AC-10 Lifecycle Scenarios 12 + 14 both have passing unit tests (SC-022-1). Coverage already likely ~80% on this slice.
7. **Stop point**: this is a deployable MVP. The orchestrator (SDD-23) can wire these primitives end-to-end. P2 stories below are quality-of-supervisor-lifecycle.

### Incremental delivery (add P2)

8. Phase 6: US4 — graceful shutdown + force-close + no-leak.
9. Phase 7: US5 — mode enforcement + grep test.
10. **VALIDATE**: coverage ≥ 95% (SC-022-10); `magex test:race` green.

### Final polish

11. Phase 8: fuzz seed, coverage check, format/lint/race gates, docs updates, single commit.

---

## Notes

- **TDD non-negotiable**: every test task is paired with a "confirm FAIL → implement → confirm PASS" sequence per Constitution VIII.
- **No new go.mod deps**: `golang.org/x/sys` is already in the module (FR-022-19).
- **PidFile + Socket files are PARALLEL implementations**: a second contributor can take US3 + US4 + US5-socket while US1 + US2 are in flight.
- **Sub-process helper for `TestPidFile_StaleAcquired`** (T019) is the trickiest piece — use the existing supervise test idiom from `child_test.go` if it has one, else use a `TEST_PIDFILE_HELPER=1` env-var re-entry pattern in `TestMain`.
- **Constitution X token redaction proof**: the encoder physically lacks a token field (`statusJSON` DTO has no `Token`); `TestSocket_TokenInResponseRedacted` (T027) is defense in depth — a marker-byte test that catches any future encoder extension that accidentally adds a token-adjacent field.
- **No goroutine without owner+ctx+termination+recover** (Constitution IX): the watcher (T042), the accept loop (T043, runs in `Run`'s frame), and each per-connection handler (T043) each have an explicit owner + cancellation signal + termination condition + top-frame `defer recover()`.
- **Anti-API enforcement is shape-driven**: T051's static-grep is the regression guard for "no TCP, no HTTP, no bearer" (Constitution V). Adding any of those substrings to a chunk source file will fail the test.
- **Single combined commit**: per SDD-22.md Prompt-5 (T066), all chunk artifacts land as one atomic unit.
