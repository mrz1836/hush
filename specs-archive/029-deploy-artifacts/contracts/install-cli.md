# Contract — `install.sh` operator CLI

This document specifies the operator-visible CLI shape of
`deploy/install.sh`. The four operator surfaces are: invocation form,
recognised env vars, exit codes, and stdout shape.

---

## Invocation

```
sudo ./deploy/install.sh           # canonical operator invocation
HUSH_USER=foo ./deploy/install.sh  # advanced: override run-as user
```

No positional arguments. No flags. Everything is via environment.
This keeps the script's surface area minimal and the chunk's contract
machine-grep-able (FR-009, no operator-specific values in committed
files).

---

## Environment

| Variable             | Required? | Default                                                  | Validation                                          |
|----------------------|-----------|----------------------------------------------------------|-----------------------------------------------------|
| `PREFIX`             | No        | `/usr/local`                                             | Must begin with `/`. Must be space-free.            |
| `HUSH_USER`          | No        | `_hush` (macOS) or `hush` (Linux)                        | `^[a-zA-Z_][a-zA-Z0-9_-]*$`.                        |
| `HUSH_STATE_DIR`     | No        | `/usr/local/var/hush` (macOS) or `/var/lib/hush` (Linux) | Must begin with `/`. Must be space-free.            |
| `HUSH_INSTALL_ROOT`  | No        | (empty)                                                  | If set, must be an absolute path that already exists. |
| `HUSH_SOURCE_BIN`    | No        | `./hush`                                                 | Must exist; spec FR-007 — missing → hard fail.      |

A validation failure causes immediate exit code 2 with a single-line
error to stderr.

---

## Exit codes

| Code | Meaning                                                                        |
|------|--------------------------------------------------------------------------------|
| 0    | Success (first run or no-op re-run).                                           |
| 1    | Generic install failure (subprocess error, copy failure, chown failure, etc.). |
| 2    | Bad input (unsupported OS, malformed env var, missing source binary).          |
| 3    | Insufficient privilege (cannot write to system paths and `HUSH_INSTALL_ROOT` is unset). |
| 4    | Required external tool missing (`tmutil` on macOS, `useradd` on Linux).        |

Exit codes 1–4 are documented in the script header for operator
debugging. Code 0 on re-run is the idempotency contract (FR-001).

---

## stdout

Always: zero stdout output before the banner. On success, exactly one
banner block (per OS, per [data-model.md §4](../data-model.md)). On
error, zero stdout output (errors go to stderr).

**Byte-identical re-run.** Two runs in the same env produce identical
stdout. The integration test `TestDeploy_InstallIdempotent_BannerByteIdentical`
asserts this with `bytes.Equal`.

---

## stderr

Reserved for error messages. Every error message uses the format
`install.sh: <stage>: <reason>` so failures are greppable and the
operator can locate the failing step without reading the script.

---

## Side effects (in order)

1. Create `${HUSH_USER}` account (idempotent — skipped if exists).
2. `install -d -m 0700 -o ${HUSH_USER} ${STATE_DIR}`.
3. macOS only: `tmutil addexclusion ${STATE_DIR}`.
4. `install -m 0755 ${HUSH_SOURCE_BIN} ${BIN_PATH}`.
5. Copy service file to `${SERVICE_DST}` with substitutions per
   [research.md §R-011](../research.md).
6. Linux only: `systemctl daemon-reload` (if `${HUSH_INSTALL_ROOT}` is
   empty — i.e. real install, not test staging).
7. Print banner to stdout.

If any step fails, exit immediately with code 1 (or the more specific
code above). No rollback — partial state is the explicit failure mode
the spec accepts (Edge Cases: "the installer must fail before any
partial state is written" — failing *before* destructive steps is the
contract; failing partway through after side-effects is permitted but
should be rare).

---

## Idempotency contract (FR-001)

Re-running `install.sh` in the same environment:
- exits 0;
- emits byte-identical stdout (per "Byte-identical re-run" above);
- causes at most one user-creation invocation across all runs;
- causes at most one `tmutil addexclusion` invocation across all runs
  (verified by the stubbed tmutil's argv log);
- leaves the filesystem (size, mode, owner, content hash of every
  produced file) in the same state as after the first run.

The Go integration test asserts each of those bullets.
