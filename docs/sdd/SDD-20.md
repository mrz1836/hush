# SDD-20 — `internal/supervise/child` (fork/exec + signal forwarding + exit-78 + process-group death-watch)

**Phase:** 5
**Package:** `internal/supervise`
**Files:** `child.go`, `child_darwin.go`, `child_linux.go`, `*_test.go`
**Branch:** `020-supervise-child` (created by the `before_specify` git hook)
**Blocked by:** SDD-19
**Blocks:** SDD-21, SDD-25
**Primary AC:** AC-10
**Coverage target:** 90%

**Behaviour contracts (MUST):**
- `os/exec`; `SysProcAttr.Setpgid=true`; `PR_SET_PDEATHSIG=SIGTERM` on linux
- Signal forwarding via dedicated goroutine started in `Start`; cancellable via ctx
- `Exit78` constant; `Wait` returns `(exitCode int, signal syscall.Signal, err error)`
- stdout/stderr pipes have a 64KB ring; if watchdog isn't consuming, drop oldest

**Anti-contracts (MUST NOT):**
- Use shell parsing (no `/bin/sh -c`); `cmd[0]` must be absolute path
- Cache child handles after `Wait`
- Use `init()`

**Tests required:**
- Unit: `TestChild_StartAndWait_HappyPath`, `TestChild_Exit78Detection`, `TestChild_SignalForwardingSIGTERM`, `TestChild_PgidIsolation_KillingPgKillsChildren`, `TestChild_StdoutPipeNonBlocking`, `TestChild_RejectsRelativeCommand`
- Race: `TestChild_ConcurrentWaitOK`

**Constitutional principles in scope:** IV (lifecycle integrity), IX (idiomatic Go, explicit goroutine lifecycle, no shell parsing)

**Exported API to lock in PACKAGE-MAP.md (this chunk — extends internal/supervise entry):**
- `type Child struct { ... }`
- `func NewChild(cfg ChildConfig) *Child`
- `type ChildConfig struct { Command []string; Env []string; Dir string; Stdout, Stderr io.Writer; Logger *slog.Logger }`
- `func (c *Child) Start(ctx context.Context) error`
- `func (c *Child) Wait() (exitCode int, signal syscall.Signal, err error)`
- `func (c *Child) Forward(sig os.Signal) error`
- `func (c *Child) PID() int`
- `const Exit78 = 78`
- `var ErrChildNotStarted, ErrCommandPathRelative`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-20 (internal/supervise/child:
fork/exec + signal forwarding + exit-78 + pgid death-watch) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles IV, IX)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11, AC-10)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenarios 2 happy path, 3 stale credential, 4 supervisor restart, 5 child SIGTERM)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-20.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
This chunk is the supervisor's child-process layer: fork/exec the
configured daemon, forward signals to it, detect exit-78 (the
project-wide convention for "stale credential — refetch and
restart"), and ensure the process group dies if the supervisor
itself dies. It is consumed by SDD-21 (refill/refresh wires
exit-78 to the state machine) and SDD-25 (lifecycle harness).

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The child runs in its own process group; if the supervisor
  itself dies, the kernel kills the process group too (no
  orphaned child daemons).
- The child's command MUST be specified as an absolute path
  (no shell parsing, no PATH lookup) — defends against
  PATH-injection attacks.
- Signal forwarding from supervisor to child runs in a
  dedicated goroutine started in Start, cancelled when the
  supervisor's ctx cancels.
- Exit code 78 (the documented sysexits "configuration error"
  code) is recognised explicitly — this is the contract by
  which a daemon signals "my credential is stale, please
  refetch and restart".
- Wait returns (exitCode, signal, err) so callers can
  distinguish exit-by-signal from exit-by-status.
- stdout/stderr pipes are bounded (64KB ring) so a chatty
  daemon can't block the supervisor by failing to consume.

The spec MUST NOT encode HOW (no specific syscall names beyond
SIGTERM/SIGHUP, no specific Go package names). Those are plan-phase.

Acceptance criterion: AC-10 (supervisor lifecycle).

Action — run exactly one command:
  /speckit-specify "internal/supervise child: fork/exec a daemon in its own process group (kernel kills group if supervisor dies), absolute-path command only (no shell), signal forwarding via explicit goroutine, exit-78 recognition (stale-credential signal from daemon), Wait returns (exitCode, signal, err); bounded stdout/stderr pipes"

The before_specify hook will create branch 020-supervise-child.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-20 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-20.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-20 (internal/supervise/child)
of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; IV/IX load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenarios 2..5 — pgid death-watch, exit-78 are tested here)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/supervise — extending the SDD-19 entry)
- /Users/mrz/projects/hush/docs/sdd/SDD-20.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/supervise
- Files: child.go (cross-platform Child + Start + Wait +
  Forward + bounded pipes), child_darwin.go (kqueue-based
  death-watch), child_linux.go (Pdeathsig via SysProcAttr),
  child_test.go, child_darwin_test.go (//go:build darwin),
  child_linux_test.go (//go:build linux)
- Exported API:
    type Child struct { ... }
    type ChildConfig struct { Command []string; Env []string; Dir string; Stdout, Stderr io.Writer; Logger *slog.Logger }
    func NewChild(cfg ChildConfig) *Child
    func (c *Child) Start(ctx context.Context) error
    func (c *Child) Wait() (exitCode int, signal syscall.Signal, err error)
    func (c *Child) Forward(sig os.Signal) error
    func (c *Child) PID() int
    const Exit78 = 78
    var ErrChildNotStarted, ErrCommandPathRelative

Implementation contract (HOW — locked):
- os/exec.Cmd. Reject cfg.Command if len==0 or
  filepath.IsAbs(cfg.Command[0]) is false → ErrCommandPathRelative.
  NEVER prepend "/bin/sh -c".
- cmd.SysProcAttr:
    Setpgid: true
    On linux: Pdeathsig: syscall.SIGTERM (via build-tagged file)
    On darwin: separate goroutine that uses kqueue to watch the
               supervisor's parent and sends SIGTERM to the
               child pgid if the supervisor dies (build-tagged).
- Bounded pipes: cmd.Stdout/Stderr are wrapped in a 64KB ring
  buffer; if the consumer is slow, oldest bytes are dropped
  with a single WARN log per drop event (rate-limited).
- Signal forwarding goroutine started by Start: select on
  ctx.Done() OR a forwardCh that Forward writes to. Send the
  signal to the negative PID (process group). Goroutine exits
  on ctx cancel.
- Wait wraps cmd.Wait; computes exitCode + signal from
  cmd.ProcessState. Exit78 is just the constant 78 — callers
  compare exitCode == Exit78 to detect the stale-credential
  contract.
- After Wait: clear cached cmd handle so subsequent calls fail
  with ErrChildNotStarted (Constitution IX: no caching of
  released resources).

Coverage target: 90%.
Constitutional principles in scope: IV, VIII, IX.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-20 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-20.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 90%. Tests required: TestChild_StartAndWait_HappyPath (run /bin/echo or similar absolute-path noop), TestChild_RejectsRelativeCommand (ErrCommandPathRelative), TestChild_RejectsEmptyCommand, TestChild_Exit78Detection (spawn a tiny test helper that exits 78), TestChild_SignalForwardingSIGTERM (spawn helper that traps SIGTERM and prints), TestChild_PgidIsolation_KillingPgKillsChildren (spawn child that spawns grandchild, kill pgid, both gone), TestChild_StdoutPipeNonBlocking (spew >> 64KB into stdout, supervisor doesn't deadlock), TestChild_DarwinDeathWatch (//go:build darwin), TestChild_LinuxPdeathsig (//go:build linux), TestChild_ConcurrentWaitOK (race-clean). Final phase MUST include magex format:fix, magex lint, magex test:race."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-20 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-20.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 90% on internal/supervise (child portion):
     go test -cover ./internal/supervise/ -run Child
3. Confirm exit-78 detection works on darwin AND linux (manual
   spot check: ./hush supervise with a stub daemon that exits 78
   after a sleep).
4. Confirm pgid isolation prevents orphan grandchildren on
   linux (TestChild_PgidIsolation_KillingPgKillsChildren must
   pass in the test environment).
5. Append "Exported API — locked at SDD-20" extension to the
   internal/supervise entry in docs/PACKAGE-MAP.md listing the
   Child/ChildConfig API from the chunk doc.
6. Update docs/AC-MATRIX.md AC-10 row with the new test file paths.
7. Mark SDD-20 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add internal/supervise/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(supervise): child fork/exec + signal forwarding + exit-78 + pgid death-watch (SDD-20)"

Final message: confirm gates passed, race-clean, coverage ≥ 90%,
exit-78 detection on darwin AND linux, pgid isolation prevents
orphans, signal forwarding integration-tested, AC-10 row updated,
SDD-PLAYBOOK updated, and the combined commit created.
```
