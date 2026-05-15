# Implementation Plan: Supervise Child Process Layer (SDD-20)

**Branch**: `020-supervise-child` | **Date**: 2026-05-05 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/020-supervise-child/spec.md`

## Summary

SDD-20 ships the supervisor's child-process layer in
`internal/supervise/`: `child.go` (cross-platform `Child` struct with
`Start`, `Wait`, `Forward`, `PID` and a 64 KB ring-buffer per output
stream), `child_linux.go` (Linux-only `SysProcAttr.Pdeathsig =
SIGTERM` for kernel-enforced parent-death cleanup), and
`child_darwin.go` (Darwin best-effort `kqueue`/`EVFILT_PROC` death
watch — see R-009 limitation). The package layer adds **zero
goroutines at rest** — every goroutine is per-`Start` and bounded by
the child's lifecycle, terminating on either the supervisor's `ctx`
cancel or the daemon's exit (whichever comes first, per spec
Clarification 3). The locked exit-disposition tuple `(exitCode int,
signal syscall.Signal, err error)` flows through `Wait`; the
exported `Exit78 = 78` constant is the documented child→supervisor
contract for stale credentials. Path-injection attacks are refused
at `Start` time via `filepath.IsAbs(cfg.Command[0])`; an empty
command returns the package-level `ErrCommandEmpty` sentinel,
distinct from the relative-path case (`ErrCommandPathRelative`) per
FR-020-3. After the first `Wait` succeeds — and likewise from any
post-exit `Forward` call — the cached `*exec.Cmd` handle is cleared
and every subsequent call returns `ErrChildNotStarted`. Bounded
pipes drop oldest bytes (FIFO eviction) and emit at most one
`slog.Warn` per overflow **episode** per stream (Clarification 5).
Coverage target: ≥90% on `internal/supervise/{child,
child_linux, child_darwin}.go`. Constitutional principles in
scope: IV (lifecycle integrity — child exit never reaches
`stopped`), VIII (TDD-mandatory; 11 tests T-01..T-11 authored
before implementation; race-clean), IX (`os/exec` stdlib only, no
shell parsing, no `init()`, every goroutine has an explicit
termination path, single-method `Logger` interface delegated to
`*slog.Logger`).

## Technical Context

**Language/Version**: Go (toolchain pinned in `go.mod` — current floor `go 1.26.1`)
**Primary Dependencies**: Go stdlib only (`os/exec`, `os`, `syscall`, `context`, `errors`, `fmt`, `io`, `path/filepath`, `sync`, `sync/atomic`, `log/slog`, `time`); `golang.org/x/sys/unix` (Darwin-only, already a direct dep at `go.mod` v0.43.0) for `EVFILT_PROC`/`Kevent` syscalls. **Zero new direct dependencies** — `os/exec` covers Linux `Pdeathsig` via `SysProcAttr.Pdeathsig`, and `golang.org/x/sys/unix` provides the Darwin kqueue surface that the stdlib `syscall` package partially exposes.
**Storage**: N/A — purely in-memory child handle plus 2 × 64 KB ring buffers. No filesystem, no socket, no network touch.
**Testing**: `go test -race` (stdlib `testing`), table-driven per `.github/tech-conventions/testing-standards.md`. Test helpers compiled via `os.Executable()` re-invocation pattern (see R-012) — no separate `cmd/test-helper` binary needed. Race-detector clean across 100 concurrent `Wait` calls (SC-020-7) and 100 sequential `Start`/`Wait` cycles (SC-020-6).
**Target Platform**: darwin (macOS) and linux. Pure Go (`CGO_ENABLED=0`). Windows out of scope (process-group semantics differ; spec Assumptions §1).
**Project Type**: Single-binary Go CLI (`cmd/hush`) with internal packages under `internal/`. This chunk **adds files to the existing `internal/supervise/` package** (which currently contains `doc.go`, `state.go`, `state_test.go` from SDD-19, plus the `config/` subpackage from SDD-18). SDD-20 introduces three new behaviour files (`child.go`, `child_linux.go`, `child_darwin.go`) and three new test files (`child_test.go`, `child_linux_test.go`, `child_darwin_test.go`). No SDD-18 or SDD-19 symbol is altered.
**Performance Goals**:
- `Start` issues exactly one `fork`+`execve` plus `O(1)` per-stream ring-buffer initialization (≤ 64 KB heap each).
- `Forward` is non-blocking from the caller's perspective (single send on a buffered `chan os.Signal` of capacity 1 + write-lock-free); end-to-end signal delivery latency ≤ 1 s under sustained 1 MB/s child output (SC-020-4).
- `Wait` blocks on `cmd.Wait()`; no spinning, no busy-wait.
- After 100 daemon restart cycles within a single supervisor process, the supervisor's goroutine count returns to its pre-first-start baseline (SC-020-6) — verified via `runtime.NumGoroutine()` delta.

**Constraints**:
- No goroutine leaks across `Start`/`Wait` cycles (FR-020-7).
- Bounded-buffer aggregate cost ≤ 64 KB per stream — kilobyte-scale, not megabyte-scale (FR-020-12).
- Daemon writes never block on supervisor (FR-020-13) — including indefinitely-blocked operator-supplied sinks.
- One overflow warning per episode per stream (FR-020-13, Clarification 5).
- All public errors comparable via `errors.Is` (FR-020-14, Constitution IX).
- Race-detector clean (FR-020-15, SC-020-7).
- No I/O outside the child process boundary — no network, no vault access, no Discord, no audit-log writes, no state-machine transitions (FR-020-16).
- Coverage ≥ 90% on the new files (SC-020-8).

**Scale/Scope**: One `Child` instance per child daemon. Long-lived supervisors host many sequential children within a single process. Package-level state: zero (the only globals are sentinel `var Err…` declarations and the chunk doc's `const Exit78 = 78`).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principle IV — Supervisor for Daemons, Wrap-Shell for Humans

| Constraint | Plan compliance |
|---|---|
| Supervisor JWTs MUST carry `session_type: "supervisor"` | Out of scope for this chunk; the JWT is opaque to the child layer (held by SDD-19 `Store`, written by SDD-21). The child layer accepts plain `[]string` env vars — never inspects them. ✅ |
| Supervisor TTL capped at `max_supervisor_session_ttl` | TTL enforcement lives in SDD-21 / refresh layer; the child runner has no TTL knowledge. ✅ |
| A child exit MUST NOT cause the supervisor to exit; the supervisor MUST hold state across the child's lifecycle within the session TTL | `Wait` returns `(exitCode, signal, err)` to the caller; the supervisor — not this layer — decides whether to silent-refill, restart from grace, or transition to `awaiting-approval`. The child runner emits no state-machine event (FR-020-16). ✅ |
| Supervisor MUST zero secret material from its memory after handoff to the child, EXCEPT during the optional grace-window cache | `ChildConfig.Env` is plain `[]string` per `os/exec` convention. The caller (SDD-21) is responsible for zeroing the strings after `Start` returns. The child runner does NOT retain `Env` past the `cmd.Start()` syscall — `cmd.Env` is consumed by the kernel on `execve`. ✅ no conflict |
| Forwarding goroutine MUST have a clear termination condition | Spec Clarification 3 + FR-020-7: terminates on `ctx.Done()` OR child process exit. Implementation: `select { case <-ctx.Done(): … case <-childDone: … case sig := <-forwardCh: syscall.Kill(-pgid, sig) }`. Documented termination + race-clean test. ✅ |

**Result:** PASS.

### Principle V — Staleness is Visible, Failure is Loud

| Constraint | Plan compliance |
|---|---|
| Validators run before child sees secrets | Validator orchestration is SDD-21's job; the child runner accepts a pre-validated env at `Start`. ✅ no conflict. |
| Exit code 78 = "my creds are stale" | Encoded as the package-level `const Exit78 = 78` (FR-020-9). `Wait` surfaces the exit code verbatim (FR-020-10): "MUST NOT coerce, remap, or mask any other exit code into 78". The plan's R-007 enforces this: `cmd.ProcessState.ExitCode()` returns the actual code; nothing in the child runner inspects or rewrites it. ✅ |
| Local Unix status socket exposes freshness state | Status socket (SDD-22) reads via SDD-19's `Snapshot()`; the child runner does not bind a socket (FR-020-16). ✅ |
| Three distinct Discord alert formats | Discord emission is SDD-21/SDD-25's job (FR-020-16: this layer "MUST NOT initiate any … Discord access"). ✅ |

**Result:** PASS.

### Principle VIII — Testing Discipline

| Constraint | Plan compliance |
|---|---|
| Table-driven unit tests, `TestFunctionName_Scenario` PascalCase | Test catalogue (`contracts/test-catalogue.md`) names every test as `TestChild_Scenario`; cross-platform tests live in `child_test.go`, build-tagged tests in `child_linux_test.go` / `child_darwin_test.go`. ✅ |
| Coverage tier — High band (supervisor state machine + child runner) ≥ 90% | Plan target: ≥ 90% on `internal/supervise/{child, child_linux, child_darwin}.go`. The integration ceiling for AC-10 is 95% across the full `internal/supervise` package; SDD-20's slice contributes 90% on the new files (SC-020-8). ✅ |
| Race-detector clean | `TestChild_ConcurrentWaitOK` (T-09) fans out 100 goroutines calling `Wait` against a single `Child` and asserts: (a) exactly one returns the real exit disposition; (b) all others return `ErrChildNotStarted` per Clarification 1; (c) no race report under `go test -race`. ✅ |
| Mandatory fuzz targets — list of 6 | This chunk adds **no new fuzz target**. The child runner has no parser surface (commands are typed `[]string` from validated config; signals are typed `os.Signal`; exit codes are typed `int`). Fuzz #5 (TOML) is already owned by SDD-18; fuzzes #1, #2, #3, #4, #6 are unrelated. Documented under R-008 (parallel to SDD-19's R-008). ✅ |
| AC-10 mapping (supervisor lifecycle, 15 scenarios) | The child runner is the unit-test arm of Scenarios 2 (first daemon bootstrap → `Start` succeeds), 3 (clean child exit → `Wait` returns `(0, 0, nil)`), 4 (child crash → `Wait` returns `(non-zero, signal_or_zero, nil)`), 5 (child exit 78 → `Wait` returns `(78, 0, nil)`). End-to-end Scenario 2..5 coverage is owned by SDD-25 (lifecycle harness). ✅ |

**Result:** PASS.

### Principle IX — Idiomatic Go Discipline

| Constraint | Plan compliance |
|---|---|
| Context propagation as first parameter on I/O / cancellable funcs | `Start(ctx context.Context) error` takes `context.Context` first. `Wait` and `Forward` do not — `Wait` blocks on `cmd.Wait()` (uncancellable by Go contract; cancellation flows through `Forward(SIGTERM)` instead, matching the spec Edge Case "context cancelled before daemon exits"). `PID()` is a pure scalar read. ✅ |
| No `Context` stored in struct fields | The forwarding goroutine captures `ctx` as a parameter at `Start` time and selects on `ctx.Done()`; `Child` itself has no `ctx` field. ✅ |
| Errors wrapped with `%w`, sentinel `var Err... = errors.New(...)` | `ErrChildNotStarted`, `ErrCommandEmpty`, `ErrCommandPathRelative` declared via `errors.New`. Wrapped form: `fmt.Errorf("supervise: %w (got %q)", ErrCommandPathRelative, cmd[0])`. Callers `errors.Is(err, supervise.ErrChildNotStarted)` succeeds (FR-020-14). ✅ |
| No globals, no `init()` | The only package-level identifiers added by this chunk are sentinel-class `var Err…`, the typed constant `const Exit78 = 78`, and a private `var defaultRingBufferSize = 64 * 1024` (sentinel-class read-only). No `init()` function. ✅ |
| Panic policy — library code returns errors | All public APIs return `error` or a value. No `panic` calls. ✅ |
| Goroutine discipline — every goroutine has an owner and termination | Two goroutines per `Start`: (a) the **signal-forwarding** goroutine — owner: `Child`; termination: `ctx.Done()` OR child-exit signal (per spec Clarification 3); (b) the **stdout/stderr drain** goroutine — owner: `Child`; termination: pipe EOF when child closes its end (i.e., child exits) OR `ctx.Done()`. On Darwin, a third **death-watch** goroutine — owner: `Child`; termination: kqueue NOTE_EXIT fires OR `ctx.Done()` (R-009). All three are joined via a per-`Start` `sync.WaitGroup` released by `Wait`; 100-cycle restart test (SC-020-6) asserts `runtime.NumGoroutine()` returns to baseline. ✅ |
| Interfaces — accept interfaces at consumer | `ChildConfig.Stdout io.Writer`, `ChildConfig.Stderr io.Writer` — stdlib interfaces accepted at the boundary. `ChildConfig.Logger *slog.Logger` is the concrete stdlib type per Constitution X (no third-party logger; `*slog.Logger` is the canonical handle). ✅ |
| Package layout — non-`main` lives under `internal/` | `internal/supervise/` ✅ |
| Modules-only, CGO-disabled | Inherits repo defaults; no new direct dependency. `golang.org/x/sys` is already at v0.43.0 in `go.mod` and is pure-Go. ✅ |

**Result:** PASS.

### Principle X — Observability & Redaction

| Constraint | Plan compliance |
|---|---|
| Structured logging via `log/slog`, no third-party logger | The single observable side-effect of this chunk is a `slog.Warn` line emitted on each overflow episode, via the `*slog.Logger` supplied in `ChildConfig.Logger`. No third-party logger. ✅ |
| Secret redaction is type-driven | `ChildConfig.Env []string` is the only secret-bearing input; the child runner does NOT log `Env` content under any code path. The `slog.Warn` overflow line carries only `{"stream": "stdout", "bytes_dropped_estimate": N}` — no buffer contents. SC-020-1 audit assertion: `grep "child env"` on test logs returns zero matches. ✅ |
| No secret values in errors | Error messages return failure mode (e.g. `"command not absolute"`) and the offending command's first element only when it is the source of the rejection — and in that case the value is non-secret (a path). Env values never appear in any returned error. ✅ |
| Audit log is separate | Audit emission is owned by SDD-21/SDD-13. The child runner does not write to any audit channel (FR-020-16). ✅ |
| Discord alert tiers | N/A — this chunk emits no alerts. ✅ |
| Metrics over local Unix status socket only | N/A — this chunk does not bind a socket. SDD-22 owns the socket and consumes via SDD-19's `Snapshot()`. ✅ |

**Result:** PASS.

### Other principles — quick clearance

- **I (Zero Files at Rest)**: The child runner touches no filesystem path. `cmd.Dir` is set to whatever the caller supplies (typically the operator's `~/.hush/`-adjacent working dir); no temp files, no buffer flushing to disk. ✅
- **II (Approval is Human)**: The state machine **encodes** the approval requirement; the child runner is downstream of approval. It cannot launch without the caller having traversed `fetching → running` per SDD-19. ✅
- **III (Defense in Depth)**: This chunk does not add or weaken a crypto layer. It does not handle `*SecureBytes`. ✅
- **VI (Tailscale-Only)**: N/A — no network. ✅
- **VII (CLI Design)**: N/A — internal package, no CLI surface. ✅
- **XI (Native-First, Minimal Deps)**: Stdlib + one already-direct dep (`golang.org/x/sys/unix`). **Zero new direct dependencies.** ✅

**Overall Constitution Check: PASS — no violations, Complexity Tracking section left empty.**

## Project Structure

### Documentation (this feature)

```text
specs/020-supervise-child/
├── plan.md                      # This file (/speckit-plan command output)
├── spec.md                      # Feature spec (already authored by /speckit-specify + /speckit-clarify)
├── checklists/
│   └── requirements.md          # Pre-existing /speckit-checklist output
├── research.md                  # Phase 0 output (this command)
├── data-model.md                # Phase 1 output (this command)
├── quickstart.md                # Phase 1 output (this command)
└── contracts/
    ├── go-api.md                # Locked Go signatures for child.go + build-tagged files
    ├── forwarding-protocol.md   # Forwarding goroutine state machine, drain goroutine, darwin death-watch
    └── test-catalogue.md        # Test → FR/SC mapping (TDD inputs for /speckit-tasks)
```

### Source Code (repository root)

```text
internal/
└── supervise/                   # PRE-EXISTING package; SDD-18 owns config/, SDD-19 owns state.go
    ├── config/                  # SDD-18 — out of scope; no edits
    ├── doc.go                   # SDD-19 — package godoc; no edits in SDD-20
    ├── state.go                 # SDD-19 — no edits in SDD-20
    ├── state_test.go            # SDD-19 — no edits in SDD-20
    ├── child.go                 # NEW — Child struct, ChildConfig, NewChild, Start (cross-platform), Wait, Forward, PID, ring buffer, sentinel errors, Exit78 const
    ├── child_linux.go           # NEW — //go:build linux; sets SysProcAttr.Pdeathsig = syscall.SIGTERM; no-op death-watch (Pdeathsig is kernel-enforced)
    ├── child_darwin.go          # NEW — //go:build darwin; spawns a kqueue/EVFILT_PROC death-watch goroutine; partial coverage per R-009 known limitation
    ├── child_test.go            # NEW — cross-platform unit tests T-01..T-09, T-11
    ├── child_linux_test.go      # NEW — //go:build linux; T-10a (Pdeathsig kernel delivery)
    └── child_darwin_test.go     # NEW — //go:build darwin; T-10b (kqueue goroutine fires on supervisor exit, best-effort path)
```

**Structure Decision**: Single-binary Go CLI; this chunk extends the
existing `internal/supervise/` package with three behaviour files
plus three test files. The package directory already exists from
SDD-18 (subpackage `config/`) and SDD-19 (`doc.go`, `state.go`,
`state_test.go`); SDD-20 adds the **child runner** alongside the
state machine without modifying any prior symbol. Build-tagged files
encapsulate platform-specific syscalls — the cross-platform `Start`
in `child.go` calls a build-tagged `applyPlatformSysProcAttr(*exec.Cmd)`
helper plus a build-tagged `startDeathWatch(ctx, *Child) error`
helper. Tests use the `os.Executable()` re-invocation pattern
(R-012) to avoid a separate test-helper binary; the test entry point
is gated by an `os.Getenv("HUSH_CHILD_TEST_HELPER_MODE")` switch in
`child_test.go`'s `TestMain`.

`internal/supervise/` is consumed by future chunks SDD-21
(`refresh.go`, `grace.go` — wires `Wait`'s exit-disposition tuple
into SDD-19 state-machine events), SDD-22 (`status_socket.go` —
reads `Child.PID()` via the SDD-19 `Snapshot.ChildPID` field, which
is written by an SDD-21 internal helper), and SDD-25 (lifecycle
harness — drives end-to-end Scenarios 2..5). Those chunks are **out
of scope** here.

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

*No violations. Section intentionally empty.*
