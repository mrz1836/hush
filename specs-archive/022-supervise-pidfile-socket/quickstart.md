# Quickstart — SDD-22 Verification

How to verify the SDD-22 chunk locally during the Implement phase. Run from
the repo root.

---

## 1. Prerequisites

- Go 1.26.1 toolchain (matches `go.mod`).
- `magex` available (`go install github.com/mrz1836/sigil/cmd/magex@latest`
  if missing).
- A POSIX system (Darwin or Linux). Windows is unsupported.
- Working tree on branch `022-supervise-pidfile-socket`.

```sh
git rev-parse --abbrev-ref HEAD
# 022-supervise-pidfile-socket

ls internal/supervise/
# pidfile.go, pidfile_test.go, socket.go, socket_darwin.go,
# socket_linux.go, socket_test.go (plus existing SDD-18..21 files)
```

---

## 2. Test gates

### 2.1 Race + coverage on the chunk

```sh
go test -race -cover ./internal/supervise/ -run "PidFile|Socket"
```

Expected:
- All ~22 tests pass.
- Coverage ≥ 95% on the new files (SC-022-10).
- `-race` reports zero races.

### 2.2 Project-wide gates (matches CI)

```sh
magex format:fix
magex lint
magex test:race
```

Expected:
- `format:fix` — no diff after the run (idempotent).
- `lint` — zero findings.
- `test:race` — full suite green.

---

## 3. Manual smoke

### 3.1 Verify socket mode + parent perms (SC-022-4)

```sh
go run ./cmd/hush-status-smoke   # if a smoke binary is provided in tasks-phase
# OR equivalent inline:
go test -run TestSocket_Mode0600 -v ./internal/supervise/
go test -run TestSocket_ParentMode0700 -v ./internal/supervise/
```

The tests assert `os.Stat(socketPath).Mode().Perm() == 0o600` and
`os.Stat(filepath.Dir(socketPath)).Mode().Perm() == 0o700` after `Run` is
up.

### 3.2 Drive a status request from the same UID (Scenario 12)

A test fixture in `socket_test.go` does this end-to-end:

1. Construct a `*Store` with a known `State` and `ChildPID`.
2. Construct a `*StatusServer` against a `t.TempDir()`-anchored path.
3. Attach a `stubStatusInputs` returning known values for the 8 fields
   not in `Snapshot`.
4. Spawn `Run(ctx)` in a goroutine.
5. `net.Dial("unix", path)`, write `"status\n"`, read the response,
   `json.Unmarshal` into a struct mirroring `statusJSON`.
6. Assert every FR-12 field is present with the expected type and value.

### 3.3 Verify token redaction (SC-022-6)

`TestSocket_TokenInResponseRedacted` constructs a `Store` whose
`Snapshot.Token` holds the marker bytes `"MARKER_d3adb33f"`, runs the
server, drives a request, and asserts:

```go
require.False(t, bytes.Contains(responseBody, []byte("MARKER_d3adb33f")))
require.False(t, bytes.Contains(responseBody, []byte(`"token"`)))
```

### 3.4 Verify graceful shutdown sub-second bound (FR-022-14)

```sh
go test -run TestSocket_GracefulShutdownOnCtx -v ./internal/supervise/
go test -run TestSocket_ConnectionForceClosedOnCtxCancel -v ./internal/supervise/
```

Both tests stamp `time.Now()` before the ctx-cancel signal and after
`Run` returns; they assert the elapsed time is under 1 second.

### 3.5 Verify duplicate supervisor refusal (Scenario 14)

```sh
go test -run TestPidFile_DuplicateRefused -v ./internal/supervise/
go test -run TestPidFile_StaleAcquired -v ./internal/supervise/
```

The first test acquires the PID file in goroutine A, then attempts a
second `AcquirePidFile` on the same path; expects
`errors.Is(err, ErrPidLocked)` immediately (sub-millisecond contention
detection — well within SC-022-2's bound).

The stale test forks a child process via `exec.Command("/bin/sh", "-c",
"...")` that opens the file, holds it briefly, then exits *without*
calling `Release`; the parent waits, then re-acquires successfully.

### 3.6 Static SC-022-9 grep assertion

```sh
go test -run TestSocket_NoTCPListenerOrHTTPServer -v ./internal/supervise/
```

Asserts the chunk's source files contain no `net.Listen("tcp"`,
`http.Server`, `http.ListenAndServe`, `bearer`, or `authorization`
substrings. Defends against future regressions adding any of these.

### 3.7 Goroutine-leak audit (SC-022-8)

```sh
go test -run TestSocket_NoGoroutineLeak -v ./internal/supervise/
```

Runs many start/stop cycles and asserts `runtime.NumGoroutine()` returns
to baseline post-`Run`.

---

## 4. Common failure modes

### 4.1 `EADDRINUSE` on rebind

If a test left a stale socket inode and pre-listen unlink failed,
subsequent test runs may see `bind: address already in use`. The chunk's
implementation removes stale inodes pre-bind (FR-022-11), so this should
never happen in clean runs. If it does:

```sh
ls -la $(go env GOTMPDIR 2>/dev/null || echo /tmp)/TestSocket_*
rm -f /tmp/TestSocket_*/*.sock
```

…and rerun. Investigate `TestSocket_PreviousSocketCleanedUp` if the
issue persists.

### 4.2 `EWOULDBLOCK` not returned on duplicate acquire

If `TestPidFile_DuplicateRefused` flakes, check that the test is using
two distinct file descriptors (one per `AcquirePidFile`) — `flock(2)` is
fd-based; two `dup`'d fds in the same process would *share* the lock
and not contend.

### 4.3 Coverage below 95%

```sh
go test -race -coverprofile=/tmp/cov.out ./internal/supervise/ -run "PidFile|Socket"
go tool cover -html=/tmp/cov.out
```

Inspect uncovered lines. Likely culprits: error paths in the watcher
goroutine (file-system permissions errors during teardown), platform-
shim `defaultRuntimeDir()` fallbacks. Add targeted tests if needed.

---

## 5. Documentation hooks (Implement-phase final steps)

After all gates pass, the implement-phase prompt requires:

1. **`docs/PACKAGE-MAP.md`** — append a new "Exported API — locked at
   SDD-22" section under the `internal/supervise/` entry, listing the
   PidFile + StatusServer signatures from contracts/api.md §1.
2. **`docs/AC-MATRIX.md`** — update the AC-10 row's "SDD-22 (pidfile,
   status socket)" supporting-chunk line with the test paths
   (`internal/supervise/pidfile_test.go`,
   `internal/supervise/socket_test.go`) and mark coverage achieved.
3. **`docs/SDD-PLAYBOOK.md`** — set SDD-22 status to `done`.

These updates are **manual** (speckit doesn't drive them). The combined
commit at the end of the implement-phase covers all four files.

---

## 6. Final commit shape

```sh
git add internal/supervise/pidfile.go internal/supervise/pidfile_test.go \
        internal/supervise/socket.go internal/supervise/socket_darwin.go \
        internal/supervise/socket_linux.go internal/supervise/socket_test.go \
        docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
        specs/022-supervise-pidfile-socket/

git commit -m "feat(supervise): PID flock + Unix status socket (SDD-22)"
```

(Per CLAUDE.md: pre-commit must be clean, gitleaks zero findings, and
the chunk is committed as one atomic unit per SDD-22 Prompt-5
guidance.)

---

**Quickstart status: COMPLETE.** All Phase 1 design artifacts are now
present.
