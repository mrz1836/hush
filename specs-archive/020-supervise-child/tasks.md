---

description: "Task list for SDD-20 Supervise Child Process Layer"
---

# Tasks: Supervise Child Process Layer (SDD-20)

**Input**: Design documents from `/specs/020-supervise-child/`
**Prerequisites**: plan.md (required), spec.md (required), research.md, data-model.md, contracts/{go-api,forwarding-protocol,test-catalogue}.md

**Tests**: TDD-MANDATORY per Constitution VIII. Every behaviour contract gets a test-writing task BEFORE the implementation task. Each test in the wave MUST be authored and observed to FAIL before the corresponding implementation task may begin.

**Coverage target**: ≥ 90% on `internal/supervise/{child, child_linux, child_darwin}.go` (SC-020-8).

**Organization**: Tasks are grouped by user story (US1..US6 from spec.md) following the wave-ordering imposed by `contracts/test-catalogue.md`. Implementation dependencies between waves force a serial order across user-story phases; tasks within a phase are parallelizable where they touch different files (marked `[P]`).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1..US6)
- File paths are absolute-from-repo-root

## Path Conventions

- Source package: `internal/supervise/`
- Cross-platform source: `internal/supervise/child.go`
- Build-tagged source: `internal/supervise/child_linux.go` (`//go:build linux`), `internal/supervise/child_darwin.go` (`//go:build darwin`)
- Cross-platform tests: `internal/supervise/child_test.go`
- Build-tagged tests: `internal/supervise/child_linux_test.go`, `internal/supervise/child_darwin_test.go`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Branch verification + skeleton files + cross-process test-helper protocol scaffold (R-012).

- [X] T001 Verify environment: branch is `020-supervise-child` (`git rev-parse --abbrev-ref HEAD`); confirm SDD-18/SDD-19 state untouched (`ls internal/supervise/` shows `config doc.go state.go state_test.go`); confirm `golang.org/x/sys` is at v0.43.0 in `go.mod`; run `go test ./internal/supervise/ -run TestStore_` to baseline pre-existing tests as green.
- [X] T002 [P] Create source skeletons with package declaration, build tags, and import blocks only (no symbols yet): `internal/supervise/child.go` (cross-platform, no build tag), `internal/supervise/child_linux.go` (`//go:build linux`), `internal/supervise/child_darwin.go` (`//go:build darwin`).
- [X] T003 [P] Create test-file skeletons + `TestMain` dispatcher in `internal/supervise/child_test.go` per R-012: switch on `os.Getenv("HUSH_CHILD_TEST_HELPER_MODE")` with empty stub branches for every mode in `contracts/test-catalogue.md` ("Test helper protocol summary" table) — `exit-zero`, `exit-seven`, `kill-self-sigkill`, `exit-78`, `sigterm-trap`, `sleep-30s`, `stdout-flood-1mb`, `stdout-flood-200kb`, `spawn-grandchild-and-sleep`, `subsupervisor-with-grandchild`, `exit-42-after-100ms`. Empty case (`""`) calls `os.Exit(m.Run())`. Default case prints "unknown helper mode" + `os.Exit(2)`. Also create empty `internal/supervise/child_linux_test.go` (`//go:build linux`) and `internal/supervise/child_darwin_test.go` (`//go:build darwin`) with package declaration only.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Sentinel-error declarations, `Exit78` constant, `Child`/`ChildConfig` types, `NewChild` constructor + Logger-nil panic, and `*ringBuffer` skeleton. Every user story depends on these symbols existing.

**⚠️ CRITICAL**: No user story phase can begin until this phase is complete.

### Test for Foundational (Wave 1, partial)

- [X] T004 Write `TestChild_LoggerNilPanicsAtNewChild` (T-13) in `internal/supervise/child_test.go` per test-catalogue.md row T-13: invoke `NewChild(ChildConfig{Logger: nil, Command: ...})` inside `func() { defer recover() }()`; assert recovered value is the documented panic message `"supervise: NewChild requires a non-nil Logger"`. Test MUST FAIL (no `NewChild` exists yet).

### Implementation for Foundational

- [X] T005 In `internal/supervise/child.go`: declare package-level `var ErrChildNotStarted = errors.New("supervise: child not started")`, `var ErrCommandEmpty = errors.New("supervise: command is empty")`, `var ErrCommandPathRelative = errors.New("supervise: command path is not absolute")`; declare `const Exit78 = 78`; declare unexported `var defaultRingBufferSize = 64 * 1024` (sentinel-class read-only). Add package godoc comment block referencing FR-020-9, FR-020-11, FR-020-14, R-003.
- [X] T006 In `internal/supervise/child.go`: define exported `ChildConfig` struct with fields `Command []string`, `Env []string`, `Dir string`, `Stdout io.Writer`, `Stderr io.Writer`, `Logger *slog.Logger` (per data-model.md "ChildConfig" entity).
- [X] T007 In `internal/supervise/child.go`: define exported `Child` struct with private fields per data-model.md "Child" entity: `cfg ChildConfig`, `mu sync.RWMutex`, `cmd *exec.Cmd`, `pid int`, `stdoutRing *ringBuffer`, `stderrRing *ringBuffer`, `forwardCh chan os.Signal`, `childDone chan struct{}`, `wg sync.WaitGroup`, `waitOnce sync.Once`, `exitCode int`, `exitSignal syscall.Signal`, `exitErr error`. Add `// Child is not safe to copy; pass as *Child.` comment for `go vet` compatibility.
- [X] T008 In `internal/supervise/child.go`: define unexported `ringBuffer` struct per data-model.md "ringBuffer" entity (`mu sync.Mutex`, `buf []byte`, `cap int`, `streamLabel string`, `logger *slog.Logger`, `atCapacity bool`, `notify chan struct{}`, `closed bool`); implement `newRingBuffer(streamLabel string, logger *slog.Logger) *ringBuffer` constructor (preallocate `buf` with `cap = defaultRingBufferSize`, allocate `notify` with capacity 1); implement idempotent `Close() error`. Stub `Write([]byte) (int, error)` and `drain(io.Writer) (int64, error)` to `panic("ringBuffer Write/drain not yet implemented")` — Phase 8 fills these.
- [X] T009 In `internal/supervise/child.go`: implement `NewChild(cfg ChildConfig) *Child` — panic with exact string `"supervise: NewChild requires a non-nil Logger"` when `cfg.Logger == nil`; copy `cfg` by value into the returned `*Child`; allocate `stdoutRing` via `newRingBuffer("stdout", cfg.Logger)`; allocate `stderrRing` via `newRingBuffer("stderr", cfg.Logger)`; allocate `forwardCh` with capacity 1. Do NOT validate `cfg.Command` here (R-002 — validation lives in Start).
- [X] T010 Run `go test -run TestChild_LoggerNilPanicsAtNewChild ./internal/supervise/` and confirm T-13 passes.

**Checkpoint**: Sentinels + Exit78 + ChildConfig + Child + NewChild + ringBuffer skeleton all exist; T-13 green. User story phases may now begin.

---

## Phase 3: User Story 5 — Path-injection attacks are refused (Priority: P1) 🎯 MVP-1

**Goal**: `Start` refuses empty commands and relative-path commands with two distinct, `errors.Is`-comparable sentinels. No spawn happens for either rejected case.

**Independent Test**: Construct `Child` with `Command = nil`; call `Start(ctx)`; assert `errors.Is(err, ErrCommandEmpty)`. Construct with `Command = []string{"daemon"}`; assert `errors.Is(err, ErrCommandPathRelative)` AND `!errors.Is(err, ErrCommandEmpty)` (FR-020-3 distinctness).

### Tests for User Story 5 (Wave 1, remainder) ⚠️

> Write these tests FIRST and observe FAIL before implementing T013.

- [X] T011 [US5] Write `TestChild_RejectsEmptyCommand` (T-05) in `internal/supervise/child_test.go` per test-catalogue.md row T-05: `Start(context.Background())` on a `Child` with `Command = nil`; assert returned error is non-nil and `errors.Is(err, supervise.ErrCommandEmpty)` is true. Test MUST FAIL (no `Start` exists yet).
- [X] T012 [US5] Write `TestChild_RejectsRelativeCommand` (T-06) in `internal/supervise/child_test.go` per test-catalogue.md row T-06: `Start(context.Background())` on a `Child` with `Command = []string{"daemon", "--flag"}`; assert `errors.Is(err, supervise.ErrCommandPathRelative)` AND `!errors.Is(err, supervise.ErrCommandEmpty)` (distinctness clause from FR-020-3 + R-003). Test MUST FAIL.

### Implementation for User Story 5

- [X] T013 [US5] In `internal/supervise/child.go`: implement `Start(ctx context.Context) error` validation arm only — return `fmt.Errorf("supervise: %w", ErrCommandEmpty)` when `len(c.cfg.Command) == 0`; return `fmt.Errorf("supervise: %w (got %q)", ErrCommandPathRelative, c.cfg.Command[0])` when `!filepath.IsAbs(c.cfg.Command[0])`. (Spawn logic added in Phase 4 — for now `Start` may return `nil` or `errors.New("not implemented")` after the validation arm; tests for US5 will pass either way since they only exercise the validation arm.)
- [X] T014 [US5] Run `go test -run "TestChild_(RejectsEmptyCommand|RejectsRelativeCommand)" ./internal/supervise/` and confirm T-05, T-06 pass.

**Checkpoint**: User Story 5 fully functional and independently testable.

---

## Phase 4: User Story 1 — Daemon launches and reports exit disposition (Priority: P1) 🎯 MVP-2

**Goal**: `Start` forks/execs the daemon directly via `os/exec` (no shell, no PATH lookup); `Wait` returns the three-tuple `(exitCode int, signal syscall.Signal, err error)` distinguishing exit-by-status from exit-by-signal. PGID is set so descendants inherit the group.

**Independent Test**: Spawn helper that exits 0 → `Wait` returns `(0, 0, nil)`. Spawn helper that exits 7 → `Wait` returns `(7, 0, nil)`. Spawn helper that SIGKILLs itself → `Wait` returns `(0, syscall.SIGKILL, nil)`.

### Tests for User Story 1 (Wave 2, partial) ⚠️

> Write these tests FIRST and observe FAIL before implementing T018+.

- [X] T015 [US1] In `internal/supervise/child_test.go`: implement helper-mode dispatch bodies for `exit-zero` (`os.Exit(0)`), `exit-seven` (`os.Exit(7)`), and `kill-self-sigkill` (`syscall.Kill(syscall.Getpid(), syscall.SIGKILL)`) inside the existing `TestMain` switch from T003.
- [X] T016 [US1] Write `TestChild_StartAndWait_HappyPath` (T-01) in `internal/supervise/child_test.go` per test-catalogue.md row T-01: build `ChildConfig` with `Command = []string{exePath, "-test.run=^$"}` and `Env = append(os.Environ(), "HUSH_CHILD_TEST_HELPER_MODE=exit-zero")` where `exePath` comes from `os.Executable()`; `Start` then `Wait`; assert `(0, 0, nil)`. Also assert no `/bin/sh` in the process tree by checking that `exec.Cmd.Path` equals `exePath` exactly (no shell prefix). Test MUST FAIL.
- [X] T017 [US1] Write `TestChild_Wait_NonZeroExitCodeVerbatim` (T-02) in `internal/supervise/child_test.go` per test-catalogue.md row T-02: helper mode `exit-seven`; assert `Wait` returns `(7, 0, nil)` verbatim — no remap (FR-020-10). Test MUST FAIL.
- [X] T018 [US1] Write `TestChild_Wait_TerminatingSignalDistinct` (T-03) in `internal/supervise/child_test.go` per test-catalogue.md row T-03: helper mode `kill-self-sigkill`; assert `Wait` returns `(0, syscall.SIGKILL, nil)`; assert exit code is zero (signal-terminated processes have no exit code in the conventional sense). Test MUST FAIL.

### Implementation for User Story 1

- [X] T019 [US1] In `internal/supervise/child_linux.go`: implement `applyPlatformSysProcAttr(cmd *exec.Cmd)` setting `cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM` (preserving any other fields the cross-platform Start has already set on `SysProcAttr`). Implement `startDeathWatch(ctx context.Context, c *Child) error` as a no-op returning `nil` (Linux relies on kernel-enforced Pdeathsig — see R-010).
- [X] T020 [US1] In `internal/supervise/child_darwin.go`: implement `applyPlatformSysProcAttr(cmd *exec.Cmd)` as a no-op (Darwin has no Pdeathsig — full death-watch added in Phase 7). Implement `startDeathWatch(ctx context.Context, c *Child) error` as a stub returning `nil` for now (real kqueue logic in Phase 7).
- [X] T021 [US1] In `internal/supervise/child.go`: extend `Start(ctx context.Context) error` past the validation arm — construct `*exec.Cmd` via `exec.CommandContext(ctx, c.cfg.Command[0], c.cfg.Command[1:]...)` (do NOT call `LookPath`; `cmd.Path = c.cfg.Command[0]` directly per R-001); set `cmd.Env = c.cfg.Env`; set `cmd.Dir = c.cfg.Dir`; set `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`; call `applyPlatformSysProcAttr(cmd)`; wire `cmd.Stdout = c.stdoutRing`, `cmd.Stderr = c.stderrRing`; allocate `c.childDone = make(chan struct{})`; call `cmd.Start()`; on error return wrapped error and leave `c.cmd == nil`; on success store `c.cmd = cmd` and `c.pid = cmd.Process.Pid` under write lock; call `startDeathWatch(ctx, c)`. Forwarding/drain goroutine spawn deferred to Phases 6/8.
- [X] T022 [US1] In `internal/supervise/child.go`: implement `Wait() (exitCode int, signal syscall.Signal, err error)` — read `cmd` under RLock; if `cmd == nil` return `(0, 0, ErrChildNotStarted)`; otherwise call `c.waitOnce.Do(func() { ... })`. Inside `Do`: call `cmd.Wait()`; derive disposition from `cmd.ProcessState.Sys().(syscall.WaitStatus)` per R-007 (signaled → `(0, ws.Signal(), nil)`; status-coded → `(ws.ExitStatus(), 0, nil)`); cache to `c.exitCode`, `c.exitSignal`, `c.exitErr`; close `c.childDone`; call `c.wg.Wait()` to join all per-Start goroutines; under write lock set `c.cmd = nil`, `c.pid = 0` (FR-020-11). After `Do` returns: re-read `cmd` under RLock; if it was the winning caller (cached triple was just populated by this goroutine — track via a winner flag local to the closure or sentinel) return the cached triple; otherwise return `(0, 0, ErrChildNotStarted)` per Clarification 1 + R-004. Implementation note: the simplest "first caller wins" pattern is to capture a `won bool` outside `waitOnce.Do` and set it `true` inside the closure.
- [X] T023 [US1] In `internal/supervise/child.go`: implement `PID() int` — read `c.pid` under RLock; return verbatim. Returns `0` before `Start` and after `Wait` clears it (per data-model.md and R-011).
- [X] T024 [US1] Run `go test -run "TestChild_(StartAndWait_HappyPath|Wait_NonZeroExitCodeVerbatim|Wait_TerminatingSignalDistinct)" ./internal/supervise/` and confirm T-01, T-02, T-03 pass.

**Checkpoint**: User Story 1 fully functional. Daemon spawn + exit-disposition read works for status-coded and signal-terminated children.

---

## Phase 5: User Story 2 — Stale-credential exit (78) is recognized (Priority: P1)

**Goal**: A daemon that exits 78 surfaces `(78, 0, nil)` from `Wait` verbatim, and callers compare against the exported `Exit78` constant. No coercion in either direction (FR-020-10).

**Independent Test**: Spawn helper that exits 78 → `Wait` returns `(78, 0, nil)` AND `exitCode == supervise.Exit78` is true. Spawn helper that exits 7 → `exitCode != supervise.Exit78` (no false positive — already covered by T-02 from US1, asserted again here for completeness).

### Tests for User Story 2 (Wave 2, remainder) ⚠️

- [X] T025 [US2] In `internal/supervise/child_test.go`: implement helper-mode dispatch body for `exit-78` (`os.Exit(78)`) inside the existing `TestMain` switch.
- [X] T026 [US2] Write `TestChild_Exit78Detection` (T-04) in `internal/supervise/child_test.go` per test-catalogue.md row T-04: helper mode `exit-78`; assert `Wait` returns `(78, 0, nil)` AND `exitCode == supervise.Exit78` is true. Add a sub-test that re-runs with helper mode `exit-seven` and asserts `exitCode != supervise.Exit78` (defensive: no spurious coercion). Test MUST FAIL — wait, must initially run on the existing `Wait` from US1; if `Wait` is correct, T-04 may pass on first run because no Exit78-specific code exists yet (the constant is just a comparable integer). The TDD discipline here is verifying the contract is encoded, not adding new logic.

### Implementation for User Story 2

- [X] T027 [US2] No production code changes required: `Exit78` const exists from Phase 2 (T005); `Wait`'s verbatim exit-code surfacing exists from Phase 4 (T022). Run `go test -run TestChild_Exit78Detection ./internal/supervise/` and confirm T-04 passes; if it fails, the failure is in `Wait`'s ProcessState extraction (not in any new code) — fix in `child.go` Wait body and re-run.

**Checkpoint**: User Story 2 fully functional. Exit-78 contract is comparable, surfaced verbatim, and never coerced.

---

## Phase 6: User Story 3 — Signals from supervisor reach the daemon (Priority: P1)

**Goal**: `Forward(sig)` delivers the signal to the child's process group (negative PID) via a dedicated forwarding goroutine that terminates on `ctx.Done()` OR child exit (whichever comes first, per Clarification 3 + R-013). Post-exit `Forward` calls return `ErrChildNotStarted` (FR-020-11 + Clarification 2).

**Independent Test**: Spawn helper that traps SIGTERM and prints a marker; `Forward(syscall.SIGTERM)`; assert helper observes the signal (marker appears in stdout sink) and exits. Cancel `ctx` while helper sleeps; assert forwarding goroutine has exited within 1 s.

### Tests for User Story 3 (Wave 3) ⚠️

> Write all three tests FIRST and observe FAIL before implementing T031+.

- [X] T028 [US3] In `internal/supervise/child_test.go`: implement helper-mode dispatch bodies for `sigterm-trap` (install `signal.Notify` for SIGTERM; on receipt write `"SIGTERM_TRAPPED\n"` to `os.Stdout`, then `os.Exit(0)`) and `sleep-30s` (call `time.Sleep(30 * time.Second)` then `os.Exit(0)`) inside the existing `TestMain` switch.
- [X] T029 [US3] Write `TestChild_SignalForwardingSIGTERM` (T-07) in `internal/supervise/child_test.go` per test-catalogue.md row T-07: helper mode `sigterm-trap`; capture stdout into a `bytes.Buffer` via `cfg.Stdout`; `Start` then `Forward(syscall.SIGTERM)`; `Wait`; assert `Wait` returns `(0, 0, nil)` (helper trapped and exited cleanly) and the captured stdout contains `"SIGTERM_TRAPPED\n"`. Test MUST FAIL (no `Forward` exists).
- [X] T030 [US3] Write `TestChild_ForwardAfterExit_ErrChildNotStarted` (T-07b) in `internal/supervise/child_test.go` per test-catalogue.md row T-07b: helper mode `exit-zero`; `Start`; `Wait` (returns `(0, 0, nil)`); call `Forward(syscall.SIGTERM)`; assert `errors.Is(err, ErrChildNotStarted)`. Add a sub-test for the post-exit-not-yet-Waited case: helper mode `exit-zero`, `Start`, sleep briefly to let the child exit, then call `Forward` BEFORE `Wait` — assert the same sentinel (per spec Edge Case + Clarification 2). Test MUST FAIL.
- [X] T031 [US3] Write `TestChild_ForwardingGoroutineExitsOnCtxCancel` (T-07c) in `internal/supervise/child_test.go` per test-catalogue.md row T-07c: helper mode `sleep-30s`; capture `runtime.NumGoroutine()` baseline; build `ctx, cancel := context.WithCancel(context.Background())`; `Start(ctx)`; `cancel()`; poll `runtime.NumGoroutine()` for up to 1 s asserting it returns to baseline (allowing `+2` for stdlib jitter). Note: the helper process MAY still be alive — this test asserts only the forwarding-goroutine termination contract, NOT child shutdown. Clean up with `Forward(SIGKILL)` + `Wait` to avoid hanging the test runner. Test MUST FAIL.

### Implementation for User Story 3

- [X] T032 [US3] In `internal/supervise/child.go`: implement `Forward(sig os.Signal) error` — read `c.cmd` under RLock; if `cmd == nil` return `fmt.Errorf("supervise: %w", ErrChildNotStarted)`; otherwise non-blocking-send `sig` on `c.forwardCh` (use buffered-channel send with `select { case c.forwardCh <- sig: default: c.forwardCh <- sig }` per R-013 — capacity 1 absorbs the common case without coalescing).
- [X] T033 [US3] In `internal/supervise/child.go` `Start` body (extending T021): after `cmd.Start()` succeeds, increment `c.wg` by 1 and `go func() { defer c.wg.Done(); ... }()` the forwarding goroutine. Body per R-013: `for { select { case <-ctx.Done(): return; case <-c.childDone: return; case sig := <-c.forwardCh: _ = syscall.Kill(-c.cmd.Process.Pid, sig.(syscall.Signal)) } }` — ESRCH/EPERM are ignored (legal terminal states). The `c.cmd` read inside the syscall is safe because the goroutine terminates before `Wait` clears `c.cmd` (the `childDone` close gates the clear under `c.wg.Wait()`).
- [X] T034 [US3] Run `go test -run "TestChild_(SignalForwardingSIGTERM|ForwardAfterExit_ErrChildNotStarted|ForwardingGoroutineExitsOnCtxCancel)" ./internal/supervise/` and confirm T-07, T-07b, T-07c pass.

**Checkpoint**: User Story 3 fully functional. Signal forwarding to PGID works; goroutine has explicit termination on both ctx cancel and child exit; post-exit Forward returns the correct sentinel.

---

## Phase 7: User Story 4 — Supervisor death does not orphan the daemon (Priority: P1)

**Goal**: PGID kill reaches the child AND any descendants it spawned (FR-020-4, FR-020-6). On Linux, kernel-enforced `Pdeathsig` delivers SIGTERM to the child when the supervisor exits by any means including SIGKILL (FR-020-5, R-010). On Darwin, a kqueue/`EVFILT_PROC` death-watch goroutine handles graceful supervisor exits (R-009 best-effort; SIGKILL gap documented).

**Independent Test (PGID isolation)**: Spawn helper that spawns grandchild in same PGID; `syscall.Kill(-pgid, SIGTERM)`; assert both processes are gone within 1 s. **Independent Test (Linux Pdeathsig)**: Spawn sub-supervisor that owns a long-running grandchild; SIGKILL the sub-supervisor; assert grandchild is gone within 2 s. **Independent Test (Darwin death-watch graceful)**: Same setup; SIGTERM (not SIGKILL) the sub-supervisor; assert grandchild is gone within 2 s; SIGKILL subtest skips with `t.Skip("R-009 darwin gap")`.

### Tests for User Story 4 (Waves 4 + 7) ⚠️

- [X] T035 [US4] In `internal/supervise/child_test.go`: implement helper-mode dispatch body for `spawn-grandchild-and-sleep` per R-012 — fork via `os.Executable()` with `HUSH_CHILD_TEST_HELPER_MODE=sleep-30s`, set `Setpgid: true`+`Pgid: 0` so the grandchild joins this process's PGID, capture grandchild PID, print `"SUPERVISOR_PID=<pid>\nGRANDCHILD_PID=<pid>\n"` to stdout, then `time.Sleep(30 * time.Second)`.
- [X] T036 [US4] Write `TestChild_PgidIsolation_KillingPgKillsChildren` (T-10) in `internal/supervise/child_test.go` per test-catalogue.md row T-10: helper mode `spawn-grandchild-and-sleep`; capture stdout into `bytes.Buffer`; `Start`; poll until both `SUPERVISOR_PID` and `GRANDCHILD_PID` are visible; parse PIDs; call `syscall.Kill(-pgid, syscall.SIGTERM)` directly (NOT via `Forward`, to isolate PGID semantics from forwarding goroutine — per test-catalogue T-10 note); poll `os.FindProcess(pid).Signal(syscall.Signal(0))` for both PIDs for up to 1 s; assert both return non-nil (process gone). `Wait` to clean up. Test MUST FAIL only if PGID is not set on the child; should pass after T021 (Setpgid was wired in Phase 4).
- [X] T037 [US4] In `internal/supervise/child_test.go`: implement helper-mode dispatch body for `subsupervisor-with-grandchild` — instantiate a `*supervise.Child` whose Command is the same test binary in `sleep-30s` mode; `Start(context.Background())`; print `"GRANDCHILD_PID=<child.PID()>\n"` to stdout; block on `child.Wait()` (which only returns when the grandchild exits, e.g. via Pdeathsig or death-watch).
- [X] T038 [US4] Write `TestChild_LinuxPdeathsig` (T-11a) in `internal/supervise/child_linux_test.go` (`//go:build linux`) per test-catalogue.md row T-11a: spawn helper mode `subsupervisor-with-grandchild`; capture stdout; poll until `GRANDCHILD_PID` line appears; SIGKILL the sub-supervisor (`syscall.Kill(subsupervisor.PID(), syscall.SIGKILL)`); poll `os.FindProcess(grandchildPID).Signal(syscall.Signal(0))` for up to 2 s; assert non-nil (grandchild reaped via kernel-delivered SIGTERM from Pdeathsig). Test MUST PASS once T019 (Linux applyPlatformSysProcAttr → Pdeathsig=SIGTERM) is in place.
- [X] T039 [US4] Write `TestChild_DarwinDeathWatch` (T-11b) in `internal/supervise/child_darwin_test.go` (`//go:build darwin`) per test-catalogue.md row T-11b: same setup as T-11a but send `syscall.SIGTERM` (graceful) to the sub-supervisor. Assert grandchild gone within 2 s. Add a sub-test `t.Run("SIGKILL_supervisor_known_limitation", func(t *testing.T) { t.Skip("R-009 darwin gap") })` documenting the limitation explicitly. Test MUST FAIL until T040 (death-watch goroutine) lands.
- [X] T040 [US4] Run `go test -run TestChild_PgidIsolation_KillingPgKillsChildren ./internal/supervise/` and confirm T-10 passes (PGID semantics already wired via Phase 4 Setpgid + Phase 7 Linux Pdeathsig). On Linux, also run `go test -run TestChild_LinuxPdeathsig ./internal/supervise/` and confirm T-11a passes.

### Implementation for User Story 4 (Darwin death-watch — Wave 7)

- [X] T041 [US4] In `internal/supervise/child_darwin.go`: implement `startDeathWatch(ctx context.Context, c *Child) error` per R-009 — open a kqueue via `unix.Kqueue()`; create a self-pipe (`os.Pipe()`); register two `unix.Kevent_t`s in one `unix.Kevent` call: (a) `EVFILT_PROC | NOTE_EXIT` on `os.Getppid()` (parent supervisor PID), (b) `EVFILT_READ` on the self-pipe read fd (waker). Increment `c.wg` by 2 and spawn two goroutines: (i) **kqueue blocker** — loops on `unix.Kevent(kq, nil, events, nil)`; on `NOTE_EXIT` fires, calls `syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)` then returns; on self-pipe read, returns; (ii) **waker** — selects on `ctx.Done()` and `c.childDone`, on either fires writes one byte to the self-pipe write end then returns. Both goroutines `defer c.wg.Done()`; both have a `defer recover()` at the top frame per Constitution IX. Close kqueue + self-pipe before returning from each goroutine.
- [X] T042 [US4] On Darwin, run `go test -run TestChild_DarwinDeathWatch ./internal/supervise/` and confirm T-11b passes (graceful path); the SIGKILL subtest skips with the R-009 message and is reported as a skipped test, not a failure.

**Checkpoint**: User Story 4 fully functional. Linux: PGID + Pdeathsig kernel-enforced. Darwin: PGID + kqueue best-effort with documented SIGKILL gap.

---

## Phase 8: User Story 6 — Chatty daemon cannot deadlock the supervisor (Priority: P2)

**Goal**: A daemon that floods stdout/stderr writes into a 64 KB FIFO ring (FR-020-12) that ALWAYS accepts writes (`Write` returns `(len(p), nil)`, FR-020-13). Slow/blocked operator-supplied sinks do not back-propagate to the daemon (Clarification 4). Each overflow episode produces exactly one `slog.Warn` per stream (Clarification 5 + R-006).

**Independent Test (non-blocking)**: Helper writes 1 MB to stdout; `cfg.Stdout = io.Discard`; assert `Forward(SIGTERM)` succeeds within 1 s and helper exits cleanly. Repeat with `cfg.Stdout = blockingWriter` (test fake that blocks on first `Write`); assert same outcome. **Independent Test (overflow accounting)**: Helper writes 200 KB (overflow), sleeps 100ms (drain below cap), writes another 200 KB (second overflow); test logger captures exactly two `slog.Warn` lines, both with `stream="stdout"`.

### Tests for User Story 6 (Wave 5) ⚠️

- [X] T043 [US6] In `internal/supervise/child_test.go`: implement helper-mode dispatch bodies for `stdout-flood-1mb` (loop writing 1 MB to `os.Stdout` in 4 KB chunks then `os.Exit(0)`) and `stdout-flood-200kb` (write 200 KB; `time.Sleep(100 * time.Millisecond)`; write 200 KB; `os.Exit(0)`) inside the existing `TestMain` switch. Also implement helper-mode body for `sigterm-trap` augmented to also flush a "READY\n" marker before signal-handler installation if not already done.
- [X] T044 [US6] Write `TestChild_StdoutPipeNonBlocking` (T-08) in `internal/supervise/child_test.go` per test-catalogue.md row T-08 — TWO sub-tests: (a) `cfg.Stdout = io.Discard` + helper mode `stdout-flood-1mb` chained with the SIGTERM trap (use a new helper mode that combines flood + sigterm-trap, OR rely on flood-then-exit semantics — pick whichever matches T043 design); assert helper exits cleanly within 5 s; (b) `cfg.Stdout = &blockingWriter{}` (a test fake whose `Write` method calls `select{}` to block forever); same flood+exit; assert helper still exits cleanly within 5 s (Clarification 4 — daemon writes never block on slow sink). Test MUST FAIL while ringBuffer.Write panics from T008.
- [X] T045 [US6] Write `TestChild_OverflowWarning_OneEpisodePerStream` (T-08b) in `internal/supervise/child_test.go` per test-catalogue.md row T-08b: build a `*slog.Logger` backed by a `slog.Handler` that captures every record into a `[]slog.Record` (test handler — implement inline); pass that logger via `cfg.Logger`; helper mode `stdout-flood-200kb`; `cfg.Stdout = &slowWriter{delay: 50 * time.Millisecond}` (a fake that drains slowly enough for overflow but eventually drains below cap during the 100ms gap); after `Wait`, assert exactly **two** captured records of level `slog.LevelWarn` with `stream` attribute `"stdout"`. Test MUST FAIL.

### Implementation for User Story 6

- [X] T046 [US6] In `internal/supervise/child.go`: implement `*ringBuffer.Write(p []byte) (int, error)` per R-005 + R-006 — acquire `r.mu`; if `r.closed` return `(len(p), nil)` silently; if `len(r.buf) + len(p) > r.cap`: drop oldest `(len(r.buf) + len(p) - r.cap)` bytes from the head (use slice copy or a circular index pair — pick the simplest correct form that survives the 1 MB flood test), set `r.atCapacity = true` if not already, and on the `false → true` transition emit `r.logger.Warn("supervise: child output buffer overflowed", slog.String("stream", r.streamLabel))`; append the full `p`; non-blocking send on `r.notify` (`select { case r.notify <- struct{}{}: default: }`); ALWAYS return `(len(p), nil)`.
- [X] T047 [US6] In `internal/supervise/child.go`: implement `*ringBuffer.drain(dst io.Writer) (int64, error)` per R-006 + R-014 — acquire `r.mu`; copy `r.buf` contents to a local slice; reset `r.buf` to length 0 (preserve capacity); if `r.atCapacity == true` AND new occupancy is `0`, set `r.atCapacity = false` (episode resets when buffer drains below cap — interpret "below cap" as "below the threshold that triggered atCapacity"; per Clarification 5, the natural threshold is `< r.cap`); release `r.mu`; call `dst.Write(local)` (this MAY block indefinitely per Clarification 4 — that is the contract); return `(int64(n), err)`. If `r.closed`, drain the remainder and return `io.EOF` after the final write.
- [X] T048 [US6] In `internal/supervise/child.go` `Start` body (extending T021 + T033): after `cmd.Start()` succeeds, `c.wg.Add(2)` and spawn two drain goroutines per R-014. Each loops `for { select { case <-r.notify: r.drain(sink); case <-c.childDone: r.drain(sink); return } }` where `sink` is the operator-supplied `cfg.Stdout`/`cfg.Stderr` (treating `nil` as `io.Discard` per data-model.md). Both goroutines `defer c.wg.Done()` and have `defer recover()` at the top frame.
- [X] T049 [US6] In `internal/supervise/child.go` `Wait` body (extending T022): after `cmd.Wait()` returns and inside `waitOnce.Do`, call `c.stdoutRing.Close()` and `c.stderrRing.Close()` BEFORE closing `c.childDone` so drain goroutines wake on Close-induced notify and drain the final bytes before exiting via `c.childDone`.
- [X] T050 [US6] Run `go test -run "TestChild_(StdoutPipeNonBlocking|OverflowWarning_OneEpisodePerStream)" ./internal/supervise/` and confirm T-08, T-08b pass.

**Checkpoint**: User Story 6 fully functional. Daemon writes never block; slow sinks are absorbed by the ring; overflow accounting emits one warning per episode per stream.

---

## Phase 9: Polish & Cross-Cutting Concerns

**Purpose**: Race-clean concurrent `Wait` (FR-020-15, SC-020-7), restart-cycle goroutine baseline (SC-020-6), `PID` lifecycle semantics (FR-020-11), and the gate trio (`magex format:fix`, `magex lint`, `magex test:race`) plus coverage verification and downstream-doc updates.

### Cross-cutting tests (Wave 6) ⚠️

- [X] T051 In `internal/supervise/child_test.go`: implement helper-mode dispatch body for `exit-42-after-100ms` (`time.Sleep(100 * time.Millisecond); os.Exit(42)`) inside the existing `TestMain` switch.
- [X] T052 Write `TestChild_ConcurrentWaitOK` (T-09) in `internal/supervise/child_test.go` per test-catalogue.md row T-09: helper mode `exit-42-after-100ms`; `Start`; spawn 100 goroutines all calling `Wait()`; collect results into a `chan` of `(int, syscall.Signal, error)` triples; after all 100 join, assert **exactly one** triple is `(42, 0, nil)` and the other 99 are `(0, 0, ErrChildNotStarted)`. The test relies on `go test -race` for race detection; mark with a comment that this test MUST be run under `-race` in CI. Test MUST FAIL if `Wait`'s sync.Once + cmd-clear is incorrect.
- [X] T053 Write `TestChild_RestartCycles_NoGoroutineLeak` (T-09b) in `internal/supervise/child_test.go` per test-catalogue.md row T-09b: capture `runtime.NumGoroutine()` baseline; loop 100 times: `NewChild`, `Start`, `Wait`; capture `runtime.NumGoroutine()` after; assert delta is within `+2` (allowing for stdlib/goroutine-scheduler jitter — the spec says "returns to baseline"). Test MUST FAIL if any per-Start goroutine leaks across the cycle.
- [X] T054 Write `TestChild_PIDReturnsZeroBeforeStartAndAfterWait` (T-12) in `internal/supervise/child_test.go` per test-catalogue.md row T-12: build `Child` via `NewChild`; assert `child.PID() == 0`; `Start` with helper mode `sleep-30s`; assert `child.PID() != 0`; call `Forward(SIGKILL)` (or `Forward(SIGTERM)` then briefly wait); call `Wait`; assert `child.PID() == 0` again. Test MUST FAIL if `Wait` does not clear `c.pid`.

### Cross-cutting verification

- [X] T055 Audit `internal/supervise/child.go` `Wait` body for the FR-020-11 contract: confirm under the `waitOnce.Do` body that (a) `cmd.Wait()` is called exactly once; (b) `c.stdoutRing.Close()` + `c.stderrRing.Close()` happen before `close(c.childDone)`; (c) `c.wg.Wait()` joins ALL per-Start goroutines (forwarding + 2 drain on linux; +2 death-watch on darwin = 5 total); (d) under write lock, both `c.cmd = nil` and `c.pid = 0` are set before returning the cached triple; (e) the "won the race" flag correctly distinguishes the first caller from subsequent/concurrent callers (R-004). Make any corrections needed in `internal/supervise/child.go`.
- [X] T056 Run `go test -race -run "TestChild_(ConcurrentWaitOK|RestartCycles_NoGoroutineLeak|PIDReturnsZeroBeforeStartAndAfterWait)" ./internal/supervise/` and confirm T-09, T-09b, T-12 pass with no race reports.

### Gates and coverage

- [X] T057 Run `magex format:fix` from repo root.
- [X] T058 Run `magex lint` from repo root and confirm zero warnings on `internal/supervise/{child,child_linux,child_darwin}.go` and the three test files.
- [X] T059 Run `magex test:race` from repo root and confirm fully clean (no failures, no races, no skips other than R-009 darwin gap subtest).
- [X] T060 Run `go test -cover ./internal/supervise/ -run Child` and confirm coverage ≥ 90% on the SDD-20 source files. If below 90%, identify uncovered branches via `go test -coverprofile=cover.out ./internal/supervise/ -run Child && go tool cover -func=cover.out` and add targeted tests until the bar is met.

### Documentation updates

- [X] T061 [P] Append "Exported API — locked at SDD-20" extension to the `internal/supervise/` entry in `docs/PACKAGE-MAP.md` listing `type Child`, `type ChildConfig`, `func NewChild`, `func (*Child) Start`, `func (*Child) Wait`, `func (*Child) Forward`, `func (*Child) PID`, `const Exit78`, `var ErrChildNotStarted`, `var ErrCommandEmpty`, `var ErrCommandPathRelative` (per data-model.md Sentinel errors table; note the additive `ErrCommandEmpty` per R-003).
- [X] T062 [P] Update the AC-10 row in `docs/AC-MATRIX.md`: populate the test-file column with `internal/supervise/child_test.go`, `internal/supervise/child_linux_test.go`, `internal/supervise/child_darwin_test.go` and the test-name column with `TestChild_*` (the full roster from test-catalogue.md).
- [X] T063 [P] Mark SDD-20 status `done` in `docs/SDD-PLAYBOOK.md`.

**Checkpoint**: All gates green, coverage ≥ 90%, race-clean, downstream docs updated. Ready for the combined commit per `docs/sdd/SDD-20.md` Prompt 5 step 7.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately.
- **Phase 2 (Foundational)**: Depends on Phase 1. **BLOCKS all user-story phases** — sentinel symbols, `Child`/`ChildConfig` types, `NewChild`, and the `ringBuffer` skeleton are referenced by every test.
- **Phase 3 (US5 — path injection)**: Depends on Phase 2. Independent of Phases 4–8 (only touches the validation arm of `Start`).
- **Phase 4 (US1 — exit disposition)**: Depends on Phase 2. Adds the spawn body + `Wait` body. Blocks Phases 5–8 because they all need a working `Start`/`Wait`.
- **Phase 5 (US2 — Exit78)**: Depends on Phase 4. Test-only — no production code change beyond what Phase 2 + Phase 4 already deliver.
- **Phase 6 (US3 — signal forwarding)**: Depends on Phase 4. Adds `Forward` + forwarding goroutine.
- **Phase 7 (US4 — no orphans)**: Depends on Phase 4 (PGID is wired in Phase 4); the Darwin death-watch goroutine is added in Phase 7.
- **Phase 8 (US6 — bounded buffers)**: Depends on Phase 4 (drain goroutines spawn alongside the forwarding goroutine in `Start`).
- **Phase 9 (Polish)**: Depends on Phases 2–8. Cross-cutting concurrency tests + gates + coverage + doc updates.

### Wave-Ordered TDD (Constitution VIII)

The wave order from `contracts/test-catalogue.md` ("TDD ordering for `/speckit-tasks`") is the binding implementation order. Each wave's tests MUST be authored AND observed to FAIL before the corresponding implementation tasks may begin:

- **Wave 1** (T-05, T-06, T-13): Phases 2 (T-13) + 3 (T-05, T-06)
- **Wave 2** (T-01..T-04): Phases 4 (T-01..T-03) + 5 (T-04)
- **Wave 3** (T-07, T-07b, T-07c): Phase 6
- **Wave 4** (T-10): Phase 7 (PGID arm)
- **Wave 5** (T-08, T-08b): Phase 8
- **Wave 6** (T-09, T-09b, T-12): Phase 9 (cross-cutting)
- **Wave 7** (T-11a, T-11b): Phase 7 (death-watch arm)

### Within Each User Story

- All test-writing tasks in a phase MUST land before any implementation task in that phase.
- All test-writing tasks in a phase MUST be observed to FAIL (run `go test`, confirm `FAIL`) before the corresponding implementation tasks begin.
- After the phase's implementation tasks complete, the phase's test verification task (typically the last `go test -run ...` step) MUST pass.

### Parallel Opportunities

- T002 + T003 are parallelizable (different files).
- T011 + T012 are NOT parallelizable (same file: `child_test.go`) — sequential.
- T015 (TestMain helper bodies) is in the same file as T016+T017+T018 (test functions) — sequential.
- T029 + T030 + T031 are NOT parallelizable (same file).
- T035 + T036 + T037 + T038 are NOT parallelizable across `child_test.go` and `child_linux_test.go` for some, but T038 is in `child_linux_test.go` and T039 is in `child_darwin_test.go` — those two ARE parallelizable as `[P]` if executed on different platforms; on a single dev box, run sequentially.
- T044 + T045 are NOT parallelizable (same file).
- T052 + T053 + T054 are NOT parallelizable (same file).
- T061 + T062 + T063 are parallelizable (three different files in `docs/`).

---

## Parallel Example: Phase 1 Setup

```bash
# Launch source skeletons + test-helper protocol scaffold together:
Task: "Create source skeletons internal/supervise/{child,child_linux,child_darwin}.go"
Task: "Create test-file skeletons + TestMain dispatcher in internal/supervise/{child_test,child_linux_test,child_darwin_test}.go"
```

## Parallel Example: Phase 9 documentation

```bash
# Three different docs files — fully parallel:
Task: "Append SDD-20 API to docs/PACKAGE-MAP.md"
Task: "Update AC-10 row in docs/AC-MATRIX.md"
Task: "Mark SDD-20 done in docs/SDD-PLAYBOOK.md"
```

---

## Implementation Strategy

### MVP-1: User Story 5 (path injection refused)

1. Phase 1 (Setup) → Phase 2 (Foundational) → Phase 3 (US5).
2. **Stop and validate**: T-05, T-06 pass; the validation arm rejects bad inputs cleanly. The `Start` spawn body does not yet exist — that's fine for MVP-1.
3. This is the smallest defensible increment: the layer refuses to launch a daemon under the threat scenarios the spec calls out as security-critical (US-5 priority "P1 — security-critical input-validation boundary").

### MVP-2: User Story 1 (exit disposition)

4. Phase 4 (US1).
5. **Stop and validate**: T-01, T-02, T-03 pass. The supervisor can now spawn a daemon and read its exit disposition correctly — the bedrock contract every higher-level lifecycle decision depends on.

### Incremental delivery

6. Phase 5 (US2 — exit-78 verification, mostly test-only).
7. Phase 6 (US3 — signal forwarding) → validate T-07/T-07b/T-07c.
8. Phase 7 (US4 — no orphans) → validate T-10, T-11a (linux), T-11b (darwin).
9. Phase 8 (US6 — chatty daemon) → validate T-08, T-08b.
10. Phase 9 (cross-cutting + gates + docs) → final commit.

### Parallel team strategy

With multiple developers (theoretical for this single-developer repo, but the SDD pattern supports it):

- After Phase 2 completes, US5/US1/US3/US6 can be worked in parallel by different developers because they touch disjoint regions of `child.go` (validation arm vs. spawn body vs. forwarding goroutine vs. drain goroutines).
- US4 (Phase 7) depends on Phase 4 having wired Setpgid — schedule it after one developer has shipped Phase 4.
- The Darwin death-watch (T041) is the only task requiring `golang.org/x/sys/unix` syscalls — schedule it on a developer comfortable with kqueue.

---

## Notes

- `[P]` tasks = different files, no dependencies on incomplete tasks within the same phase.
- `[USn]` label maps each task to the user story it serves (US5/US1/US2/US3/US4/US6 — note SDD ordering puts US5 first per wave-1 TDD).
- Every test task MUST observe FAIL before the corresponding implementation task begins (Constitution VIII).
- Commit frequency: per `docs/sdd/SDD-20.md` Prompt 5, ALL of Phase 1–9 is bundled into a single combined commit at the end of Phase 9. Do NOT commit between phases.
- The R-009 Darwin SIGKILL gap is documented in spec/research/test-catalogue and surfaced via `t.Skip("R-009 darwin gap")` in T039 — that skip is the correct, audit-trail outcome, not a test failure.
- Constitutional principles in scope: IV (lifecycle integrity), VIII (TDD-mandatory), IX (idiomatic Go, explicit goroutine lifecycle, no shell parsing), X (observability + redaction in overflow warnings).
- Coverage target: ≥ 90% on `internal/supervise/{child,child_linux,child_darwin}.go` (SC-020-8). Other files in `internal/supervise/` (config/, doc.go, state.go) are out of scope for this chunk's coverage measurement.
