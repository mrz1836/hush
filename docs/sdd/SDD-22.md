# SDD-22 — `internal/supervise` PID file + flock + Unix status socket

**Phase:** 5
**Package:** `internal/supervise`
**Files:** `pidfile.go`, `socket.go`, `socket_darwin.go`, `socket_linux.go`, `*_test.go`
**Branch:** `022-supervise-pidfile-socket` (created by the `before_specify` git hook)
**Blocked by:** SDD-19
**Blocks:** SDD-23, SDD-25
**Primary AC:** AC-10
**Coverage target:** 95%

**Behaviour contracts (MUST):**
- PID file via `golang.org/x/sys` `flock` (`LOCK_EX | LOCK_NB`)
- Socket at platform-correct path; mode `0600`; parent dir mode `0700` created if needed
- Status response is exactly the JSON shape in `docs/SPEC.md` FR-12
- Socket server graceful shutdown on `ctx` cancel

**Anti-contracts (MUST NOT):**
- Use HTTP-on-localhost (Constitution V — FS perms are the auth)
- Add bearer-token auth on the socket
- Allow non-root agent processes to bind without `0600` enforcement

**Tests required:**
- Unit: `TestPidFile_FlockExclusive`, `TestPidFile_DuplicateRefused`, `TestPidFile_StaleAcquired` (after previous owner died), `TestSocket_Mode0600`, `TestSocket_ParentMode0700`, `TestSocket_StatusJSONShape` (per `docs/SPEC.md` FR-12), `TestSocket_GracefulShutdownOnCtx`

**Constitutional principles in scope:** V (operator visibility — status socket; FS perms as auth, no bearer token), VIII, IX (explicit goroutine lifecycle on socket server)

**Exported API to lock in PACKAGE-MAP.md (this chunk — extends internal/supervise entry):**
- `type PidFile struct { ... }`
- `func AcquirePidFile(path string) (*PidFile, error)`
- `func (p *PidFile) Release() error`
- `type StatusServer struct { ... }`
- `func NewStatusServer(socketPath string, store *Store, logger *slog.Logger) *StatusServer`
- `func (s *StatusServer) Run(ctx context.Context) error`
- `var ErrPidLocked, ErrSocketPermsLoose`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-22 (internal/supervise PID
file + Unix status socket) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principle V — operator visibility via status socket; filesystem perms as auth)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11, FR-12, AC-10)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (status_socket, pid_file)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenarios 12, 14)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-22.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
This chunk delivers two operator-visibility primitives: a flock-
backed PID file (so two supervisors with the same name can't run
simultaneously) and a Unix domain status socket (so `hush client
status` can ask the daemon what it's doing). The socket
intentionally has no bearer-token auth — Unix file permissions
ARE the auth (Constitution V).

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- Each supervisor acquires an exclusive flock on its PID file
  at startup. A second invocation with the same PID-file path
  fails fast with a distinct, named error.
- A previous supervisor that died without releasing its flock
  leaves a stale PID file; the next supervisor MUST be able
  to acquire it cleanly (the OS releases the flock on process
  death).
- The status socket is a Unix domain socket at the configured
  path; the socket file mode is 0600 and its parent directory
  is 0700 (created if needed).
- The status response JSON shape matches docs/SPEC.md FR-12
  exactly.
- The socket server stops cleanly when its context is
  cancelled.
- The socket MUST NOT use HTTP-over-localhost or any token-
  based auth (Constitution V — FS perms are the auth).

The spec MUST NOT encode HOW (no library names, no specific
syscall names beyond flock). Those are plan-phase.

Acceptance criterion: AC-10 (supervisor lifecycle).

Action — run exactly one command:
  /speckit-specify "internal/supervise: flock-backed PID file (exclusive, NB; stale acquired cleanly after previous owner died) + Unix domain status socket (mode 0600, parent 0700, status JSON per FR-12, graceful shutdown on ctx); FS perms ARE the auth — no bearer-token, no HTTP-on-localhost"

The before_specify hook will create branch 022-supervise-pidfile-socket.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-22 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-22.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-22 (internal/supervise pidfile
+ socket) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; V/VIII/IX load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11, FR-12 — the FR-12 status JSON shape is load-bearing)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (status_socket, pid_file paths)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenarios 12, 14)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/supervise — extending the SDD-19 entry)
- /Users/mrz/projects/hush/docs/sdd/SDD-22.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/supervise
- Files: pidfile.go (PidFile + AcquirePidFile + Release),
  socket.go (StatusServer cross-platform), socket_darwin.go
  (platform path resolution), socket_linux.go (platform path
  resolution), pidfile_test.go, socket_test.go
- Exported API:
    type PidFile struct { ... }
    func AcquirePidFile(path string) (*PidFile, error)
    func (p *PidFile) Release() error
    type StatusServer struct { ... }
    func NewStatusServer(socketPath string, store *Store, logger *slog.Logger) *StatusServer
    func (s *StatusServer) Run(ctx context.Context) error
    var ErrPidLocked, ErrSocketPermsLoose

Implementation contract (HOW — locked):
- PidFile uses golang.org/x/sys/unix.Flock with
  LOCK_EX|LOCK_NB. On EWOULDBLOCK → ErrPidLocked. Write the
  current PID to the file as text. Release does Flock(LOCK_UN)
  + Close + os.Remove (best-effort).
- Stale-PID handling: the OS automatically releases the flock
  when the previous owner died, so AcquirePidFile just retries
  the lock once after Open — no explicit stale-PID check
  needed.
- StatusServer:
    - Listen on net.Listen("unix", socketPath).
    - Pre-listen: if socketPath exists, os.Remove (clean up
      stale socket from previous run); then create parent
      dir at 0700 if missing; chmod the socket to 0600 after
      Listen.
    - Accept loop in a goroutine started by Run(ctx). Each
      connection: read one line of request (e.g. "status"),
      respond with the JSON shape from docs/SPEC.md FR-12,
      close.
    - On ctx cancel: close listener; accept loop exits;
      Run returns nil.
    - The status JSON is built from store.Snapshot() (SDD-19);
      the Token field renders as "[redacted]" via
      SecureBytes.LogValue.
- No HTTP. No bearer auth. The 0600 + 0700 mode IS the auth.

Coverage target: 95%.
Constitutional principles in scope: V, VIII, IX, X.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-22 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-22.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 95%. Tests required: TestPidFile_FlockExclusive, TestPidFile_DuplicateRefused (ErrPidLocked), TestPidFile_StaleAcquired (previous owner died, lock auto-released by OS), TestPidFile_ReleaseRemovesFile, TestSocket_Mode0600 (chmod after Listen), TestSocket_ParentMode0700 (created if missing), TestSocket_StatusJSONShape (matches docs/SPEC.md FR-12 — assert every field present), TestSocket_TokenInResponseRedacted (SecureBytes LogValue path), TestSocket_GracefulShutdownOnCtx, TestSocket_PreviousSocketCleanedUp. Final phase MUST include magex format:fix, magex lint, magex test:race."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-22 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-22.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 95% on internal/supervise (pidfile + socket
   portions):
     go test -cover ./internal/supervise/ -run "PidFile|Socket"
3. Confirm socket path resolution correct on darwin AND linux
   (manual smoke if available).
4. Confirm mode enforcement proven (TestSocket_Mode0600 +
   TestSocket_ParentMode0700).
5. Confirm Token in status response renders as "[redacted]"
   (TestSocket_TokenInResponseRedacted).
6. Append "Exported API — locked at SDD-22" extension to the
   internal/supervise entry in docs/PACKAGE-MAP.md listing the
   PidFile + StatusServer API from the chunk doc.
7. Update docs/AC-MATRIX.md AC-10 row with the new test file paths.
8. Mark SDD-22 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/supervise/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(supervise): PID flock + Unix status socket (SDD-22)"

Final message: confirm gates passed, race-clean, coverage ≥ 95%,
socket paths correct on both OSes, mode enforcement proven, Token
redaction proven in status response, AC-10 row updated,
SDD-PLAYBOOK updated, and the combined commit created.
```
