# Test Catalogue — SDD-20 Supervise Child Process Layer

**Branch**: `020-supervise-child` | **Date**: 2026-05-05

This document maps every test required by SDD-20 to the
Functional Requirement (FR-020-N), Success Criterion (SC-020-N),
or research decision (R-NNN) it covers. `/speckit-tasks` consumes
this catalogue to generate test-writing tasks BEFORE the
implementation tasks, per Constitution VIII (TDD-mandatory).

Test naming convention: `TestChild_Scenario`, PascalCase, per
`.github/tech-conventions/testing-standards.md`.

Test file placement:
- `child_test.go` — cross-platform tests (linux + darwin)
- `child_linux_test.go` — `//go:build linux`
- `child_darwin_test.go` — `//go:build darwin`

Coverage target: ≥ 90% on `internal/supervise/{child, child_linux,
child_darwin}.go` (SC-020-8).

---

## Test roster

| # | Test name | File | FR/SC traced | What it asserts |
|---|----|----|----|----|
| T-01 | `TestChild_StartAndWait_HappyPath` | `child_test.go` | FR-020-1, FR-020-8, US-1 AC-1 | A test-helper child that exits 0 → `Wait` returns `(0, 0, nil)`; no shell process appears in the process tree (`/bin/sh` ancestry check). |
| T-02 | `TestChild_Wait_NonZeroExitCodeVerbatim` | `child_test.go` | FR-020-8, FR-020-10, US-1 AC-2 | A test-helper that exits 7 → `Wait` returns `(7, 0, nil)`. Verbatim — no remap. |
| T-03 | `TestChild_Wait_TerminatingSignalDistinct` | `child_test.go` | FR-020-8, US-1 AC-3 | A test-helper killed by SIGKILL → `Wait` returns `(0, syscall.SIGKILL, nil)`. Distinguishable from status-coded exit. |
| T-04 | `TestChild_Exit78Detection` | `child_test.go` | FR-020-9, FR-020-10, US-2 AC-1, US-2 AC-2 | Helper exits 78 → `Wait` returns `(78, 0, nil)` AND `exitCode == supervise.Exit78` succeeds. A helper that exits 7 produces `exitCode != Exit78` (no spurious coercion). |
| T-05 | `TestChild_RejectsEmptyCommand` | `child_test.go` | FR-020-2, US-5 AC-2, R-003 | `Start` with `Command = nil` returns error wrapping `ErrCommandEmpty` (verified via `errors.Is`). |
| T-06 | `TestChild_RejectsRelativeCommand` | `child_test.go` | FR-020-3, US-5 AC-1, R-003 | `Start` with `Command = []string{"daemon"}` returns error wrapping `ErrCommandPathRelative` AND **distinct** from `ErrCommandEmpty` (assert `!errors.Is(err, ErrCommandEmpty)`). |
| T-07 | `TestChild_SignalForwardingSIGTERM` | `child_test.go` | FR-020-6, FR-020-7, US-3 AC-1, R-013 | A helper that traps SIGTERM and prints `"SIGTERM_TRAPPED\n"` before exiting 0. After `Forward(syscall.SIGTERM)`: helper observes the signal (stdout contains the marker), helper exits, `Wait` returns `(0, 0, nil)`. |
| T-07b | `TestChild_ForwardAfterExit_ErrChildNotStarted` | `child_test.go` | FR-020-11 + Edge Case "signal forwarded after the daemon has already exited", Clarification 2 | A helper that exits 0 quickly. After `Wait` returns, calling `Forward(SIGTERM)` returns `ErrChildNotStarted` (verified via `errors.Is`). Likewise calling `Forward` after child exit but **before** `Wait` returns the same sentinel. |
| T-07c | `TestChild_ForwardingGoroutineExitsOnCtxCancel` | `child_test.go` | FR-020-7, Clarification 3, R-013 | Helper sleeps for 30s. After `Start` returns, cancel `ctx`. Within 1 s, the forwarding goroutine has exited (verified via `runtime.NumGoroutine()` delta). The daemon process MAY still be alive — the spec delegates SIGTERM-on-cancel to the higher layer; this test asserts only the goroutine termination contract. |
| T-08 | `TestChild_StdoutPipeNonBlocking` | `child_test.go` | FR-020-12, FR-020-13, US-6 AC-1, R-005, R-006 | A helper that writes 1 MB to stdout in a tight loop with no pause. With `cfg.Stdout = io.Discard` (no operator-side blocking): supervisor remains responsive — `Forward(SIGTERM)` succeeds within 1 s and helper exits. With `cfg.Stdout = blockingWriter` (test fake that blocks on first `Write`): supervisor STILL remains responsive (Clarification 4) — `Forward` succeeds and helper exits without ever blocking on its own writes. |
| T-08b | `TestChild_OverflowWarning_OneEpisodePerStream` | `child_test.go` | FR-020-13 + Clarification 5, R-006, US-6 AC-2 | A test logger captures `slog.Warn` lines. Helper writes 200 KB in a single burst (overflows), then sleeps 100ms (drains below cap), then writes another 200 KB. Assertions: exactly **two** warnings emitted (one per episode), both with `stream="stdout"`, neither during the quiet interval. |
| T-09 | `TestChild_ConcurrentWaitOK` | `child_test.go` | FR-020-15 + Clarification 1, SC-020-7, R-004 | Helper sleeps 100ms then exits 42. Spawn 100 goroutines all calling `Wait`. Assertions: exactly **one** goroutine returns `(42, 0, nil)`; all 99 others return `(0, 0, ErrChildNotStarted)`; `go test -race` reports no races. |
| T-09b | `TestChild_RestartCycles_NoGoroutineLeak` | `child_test.go` | FR-020-7, SC-020-6 | Capture `runtime.NumGoroutine()` baseline. Loop 100×: `NewChild`, `Start`, helper exits 0, `Wait`. Assert post-loop goroutine count is within `baseline + 2` (allowing for stdlib jitter; the spec is "returns to baseline"). |
| T-10 | `TestChild_PgidIsolation_KillingPgKillsChildren` | `child_test.go` | FR-020-4, FR-020-6, US-4 AC-1 | Helper spawns a grandchild (also via `os.Executable()` re-invocation) in the same process group, prints both PIDs to stdout, then sleeps 30s. Test reads both PIDs, sends `syscall.Kill(-pgid, SIGTERM)` directly (NOT via `Forward`, to isolate the PGID semantics from the forwarding goroutine), then asserts both processes are gone within 1 s via `os.FindProcess + Signal(syscall.Signal(0))` (the canonical "is process alive" probe). |
| T-11a | `TestChild_LinuxPdeathsig` | `child_linux_test.go` | FR-020-5 (linux), R-010, US-4 AC-2 | Spawn a sub-supervisor (via `os.Executable()`) that itself uses `Child` to start a long-running grandchild helper. Kill the sub-supervisor with SIGKILL. Within 2 s, the grandchild is gone (kernel-delivered SIGTERM). Asserts the kernel-enforced Pdeathsig path. |
| T-11b | `TestChild_DarwinDeathWatch` | `child_darwin_test.go` | FR-020-5 (darwin best-effort), R-009, US-4 AC-2 | Spawn a sub-supervisor that starts a long-running grandchild helper. Send SIGTERM (NOT SIGKILL — graceful exit) to the sub-supervisor. The kqueue death-watch goroutine fires; grandchild receives SIGTERM within 2 s. **The SIGKILL case is NOT tested** on darwin per R-009; a `t.Run("SIGKILL_supervisor_known_limitation", func(t *testing.T) { t.Skip("R-009 darwin gap") })` documents the gap explicitly in test output. |
| T-12 | `TestChild_PIDReturnsZeroBeforeStartAndAfterWait` | `child_test.go` | FR-020-11 (PID semantics), R-011 | After `NewChild` (no `Start`): `PID() == 0`. After `Start` (helper sleeping): `PID() != 0`. After helper exits and `Wait` returns: `PID() == 0` again. |
| T-13 | `TestChild_LoggerNilPanicsAtNewChild` | `child_test.go` | R-002, R-003, Constitution IX startup-wiring exemption | `NewChild(ChildConfig{Logger: nil, ...})` panics with the documented message. Tested via `recover()` in the test body. |

---

## Coverage cross-reference

| FR / SC | Tests covering it |
|---|---|
| FR-020-1 (no shell, absolute path only) | T-01 (no `/bin/sh` in process tree) |
| FR-020-2 (empty command rejected) | T-05 |
| FR-020-3 (relative path rejected, distinct from empty) | T-06 |
| FR-020-4 (own process group) | T-10 |
| FR-020-5 (parent-death cleanup) | T-11a (linux), T-11b (darwin best-effort) |
| FR-020-6 (signal forwarding to PGID) | T-07, T-10 |
| FR-020-7 (forwarding goroutine; explicit termination) | T-07, T-07c, T-09b |
| FR-020-8 (three-tuple exit disposition) | T-01, T-02, T-03, T-04 |
| FR-020-9 (Exit78 surfaced verbatim) | T-04 |
| FR-020-10 (no remap to/from 78) | T-02, T-04 |
| FR-020-11 (post-Wait re-entry returns ErrChildNotStarted) | T-07b, T-09, T-12 |
| FR-020-12 (kilobyte-scale buffers) | T-08, T-08b (memory budget asserted indirectly via 64 KB cap; explicit budget assertion is in T-08b) |
| FR-020-13 (FIFO eviction; no daemon block; one warning per episode) | T-08, T-08b |
| FR-020-14 (errors.Is comparable) | T-05, T-06, T-07b |
| FR-020-15 (concurrent Wait race-clean) | T-09 |
| FR-020-16 (no I/O outside child boundary) | Code review + linter; no runtime test (no negative assertion is feasible without a goroutine-leak detector for I/O syscalls). Documented as a code-review gate in `quickstart.md`. |
| SC-020-1 (no shell in tree) | T-01 |
| SC-020-2 (Exit78 not silently restarted) | T-04 + downstream SDD-21 (the supervisor's silent-refill suppression on exit-78 is owned by SDD-21; T-04 only verifies the child layer surfaces the code) |
| SC-020-3 (supervisor abrupt termination → no orphans) | T-11a (linux), T-11b (darwin best-effort + R-009 skip) |
| SC-020-4 (1 MB/s output → SIGTERM within 1 s) | T-08 |
| SC-020-5 (config audit: no relative/empty/shell commands) | T-05, T-06 (the layer refuses; the audit itself is operator-tooling) |
| SC-020-6 (100 restarts → goroutine baseline) | T-09b |
| SC-020-7 (100 concurrent Wait reads race-clean) | T-09 + `go test -race` invocation in CI |
| SC-020-8 (≥ 90% coverage) | All tests + `go test -cover ./internal/supervise/ -run Child` |

---

## TDD ordering for `/speckit-tasks`

The spec's User Stories define independence boundaries. Tasks
should be generated in this order so each test exists BEFORE its
implementation:

**Wave 1 — sentinel errors + ChildConfig + NewChild (no syscalls)**
1. Write T-05, T-06, T-13 → **test files first**
2. Implement `Child`, `ChildConfig`, `NewChild`, `ErrCommandEmpty`,
   `ErrCommandPathRelative`, `ErrChildNotStarted`, `Exit78`,
   `*ringBuffer` skeleton, validation gate inside `Start`
3. Tests T-05, T-06, T-13 pass

**Wave 2 — Start + Wait + Exit78 (basic process lifecycle)**
4. Write T-01, T-02, T-03, T-04 → **tests first**
5. Implement `Start` (cmd.Start, SysProcAttr.Setpgid, build-tagged
   `applyPlatformSysProcAttr`), `Wait` (sync.Once + cmd.Wait +
   ProcessState extraction)
6. Tests T-01..T-04 pass

**Wave 3 — Forwarding + termination contracts**
7. Write T-07, T-07b, T-07c → **tests first**
8. Implement `Forward` + the forwarding goroutine + `c.childDone`
   close ordering inside `Wait`
9. Tests T-07, T-07b, T-07c pass

**Wave 4 — PGID isolation**
10. Write T-10 → **test first**
11. Implementation already covers via Wave 2 (Setpgid is in
    `applyPlatformSysProcAttr`); confirm test passes
12. Test T-10 passes

**Wave 5 — Bounded buffers + drain + overflow accounting**
13. Write T-08, T-08b → **tests first**
14. Implement `*ringBuffer` Write/drain/Close with overflow
    accounting (R-006); spawn drain goroutines in `Start`
15. Tests T-08, T-08b pass

**Wave 6 — Concurrency + restart cycles**
16. Write T-09, T-09b, T-12 → **tests first**
17. Audit `Wait`'s `sync.Once` + cmd-handle clearing under lock;
    confirm `c.wg.Wait()` joins all per-`Start` goroutines
18. Tests T-09, T-09b, T-12 pass

**Wave 7 — Platform-specific death-watch**
19. Write T-11a → **test first** (linux)
20. Implement linux `applyPlatformSysProcAttr` (Pdeathsig)
21. Test T-11a passes
22. Write T-11b → **test first** (darwin)
23. Implement darwin `startDeathWatch` (kqueue + self-pipe + two
    goroutines, R-009)
24. Test T-11b passes (graceful path); SIGKILL subtest skips per
    R-009

**Final — gates**
25. `magex format:fix && magex lint && magex test:race`
26. `go test -cover ./internal/supervise/ -run Child` reports
    ≥ 90%

The implementation MUST NOT proceed past a wave until that wave's
tests have been authored. This is the Constitution VIII contract.

---

## Test helper protocol summary (R-012)

All cross-process tests use `os.Executable()` to re-invoke the
test binary itself with `HUSH_CHILD_TEST_HELPER_MODE` env-var
selecting the helper code path. Modes:

| Mode | Behaviour |
|------|-----------|
| `exit-zero` | Exit 0 immediately (T-01) |
| `exit-seven` | Exit 7 immediately (T-02) |
| `kill-self-sigkill` | Send SIGKILL to self via `syscall.Kill(syscall.Getpid(), SIGKILL)` (T-03) |
| `exit-78` | Exit 78 immediately (T-04) |
| `sigterm-trap` | Install SIGTERM handler; print `"SIGTERM_TRAPPED\n"` to stdout; exit 0 (T-07, T-07b) |
| `sleep-30s` | Sleep 30 seconds; exit 0 (T-07c, T-09, T-10, T-11a, T-11b) |
| `stdout-flood-1mb` | Write 1 MB to stdout in a tight loop; exit 0 (T-08) |
| `stdout-flood-200kb` | Write 200 KB; sleep 100ms; write 200 KB; exit 0 (T-08b) |
| `spawn-grandchild-and-sleep` | `os.Executable()` re-invoke "sleep-30s"; print `"SUPERVISOR_PID=$PID\nGRANDCHILD_PID=$PID\n"` to stdout; sleep 30s (T-10) |
| `subsupervisor-with-grandchild` | Construct a `Child` with a grandchild helper; print `"GRANDCHILD_PID=$PID\n"`; block on Wait (T-11a, T-11b) |
| `exit-42-after-100ms` | Sleep 100ms; exit 42 (T-09) |

Modes are exhaustive of test needs. The dispatch lives in
`TestMain` in `child_test.go` per R-012.
