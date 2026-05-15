# Phase 0 Research — SDD-17 `hush secret`

This document records the resolved questions that gate the Phase 1
design. Every "NEEDS CLARIFICATION" candidate from the spec or chunk
contract is closed here with a Decision / Rationale / Alternatives
triple.

---

## R1. Why does the stdin-TTY gate apply to `list` AND to the writes?

- **Decision**: All four verbs (`add`, `remove`, `list`, `rotate`)
  refuse if `term.IsTerminal(int(os.Stdin.Fd()))` is `false`.
- **Rationale**: Every verb requires the vault passphrase, and the
  passphrase MUST come from the controlling terminal — never a pipe.
  A piped stdin would either steal the passphrase prompt's input or
  silently bypass authentication. The spec's clarification (Q3 in
  Session 2026-05-03) closed this explicitly: "list requires stdin
  to be a TTY; stdout pipe-aware drives the JSON-vs-text rendering
  choice." This separates the stdin gate (passphrase capture) from
  the stdout convention (rendering format) so the supported invocation
  `hush secret list | jq` works from a real terminal.
- **Alternatives considered**:
  - *Allow `list` with a piped stdin and accept passphrase via env
    var* — rejected; FR-014 forbids env-var passphrases, and an
    env-var passphrase on a shared host is a leak waiting to happen.
  - *Allow `list` with a piped stdin and accept passphrase from the
    pipe itself* — rejected; the universal TTY gate is simpler and
    still permits `hush secret list | jq` because that pipeline only
    pipes stdout.
  - *Allow `list` to operate without a passphrase by exposing only
    metadata (no decryption)* — rejected; the SDD-03 vault format
    encrypts the entire entry list including names. Decrypting is
    unavoidable to enumerate.

## R2. What chooses TTY-text vs JSON for `list`?

- **Decision**: `term.IsTerminal(int(os.Stdout.Fd()))`. TTY → text,
  non-TTY → JSON.
- **Rationale**: Matches the project-wide convention codified in
  `internal/cli/output.go::detectTTY` and applied by `health` and
  `version`. Operators can predict the output by looking at where
  stdout is wired.
- **Alternatives considered**:
  - *A `--format` flag* — rejected; auto-detection covers both
    operator workflows (interactive read; `| jq` post-processing) with
    zero typing.
  - *Always JSON* — rejected; the human-text form is meaningfully
    nicer at the terminal and is the spec's stated default.

## R3. Where is the PID file?

- **Decision**: `<state_dir>/hush.pid`. The literal `"hush.pid"` is
  inlined as a `pidFilename` constant in `internal/cli/secret.go`
  next to a comment that points at SDD-17 §"Implementation contract"
  (the only place the path is locked).
- **Rationale**: SDD-17's contract names this path explicitly. The
  `state_dir` is resolved through `LoadServer` (the same path used
  by `serve` to find the vault file), so a single source of truth
  exists for "where do hush server-side artifacts live."
- **Caveat**: `hush serve` does NOT currently write this file. That
  responsibility belongs to a future SDD chunk (likely SDD-19 or
  later). Until then `rotate` always exercises the missing-PID
  branch in production — which is a legal, documented outcome
  (FR-011, SC-005). The unit-test stub used to verify SIGHUP
  delivery deliberately writes a `hush.pid` file with the test
  helper's own PID, then waits for the signal; this proves the
  path will work the day `serve` writes the file.
- **Alternatives considered**:
  - *Use the daemon's `os.Getppid()` discovery* — rejected; not
    portable across launchd/systemd, doesn't survive `serve`
    restarts, and offers no improvement over a PID file.
  - *Use a Unix status socket already created by `serve`* —
    rejected; `serve` does not currently create one (the supervisor
    status socket from SDD-23 is supervisor-side, not server-side).

## R4. How does `rotate` determine whether the PID is signal-able?

- **Decision**: `syscall.Kill(pid, 0)` is the liveness probe. The
  branches:
  - `nil` → process exists and is owned by us → safe to send SIGHUP.
  - `errors.Is(err, syscall.ESRCH)` (or `os.ErrProcessDone`) → stale
    PID → warn-and-continue.
  - `errors.Is(err, syscall.EPERM)` → process exists but is owned by
    a different user → warn-and-continue (do not signal something we
    don't own).
  - Other errors → treat as stale; warn with the underlying error
    class.
- **Rationale**: This is the canonical POSIX idiom and is the same
  approach used by every supervisor and pidfile library in the Go
  ecosystem. `os.FindProcess` always succeeds on POSIX and is
  therefore a poor liveness probe.
- **Alternatives considered**:
  - *Read `/proc/<pid>/comm`* — rejected; not available on macOS.
  - *Read `/proc/<pid>/status` and check Uid* — rejected; same.
    The two-syscall sequence is simpler than reading and parsing
    proc files.

## R5. What is the rotate "no content change" semantic?

- **Decision**: Call `vault.Save` with the same `[]vault.Secret`
  slice that came out of `vault.Load`. SDD-03's `Save` already mints
  a fresh nonce and salt on every call, so the on-disk ciphertext
  bytes are guaranteed to differ from the pre-rotation file (FR-009,
  SC-003) while the plaintext set (names, descriptions, values) is
  preserved exactly.
- **Rationale**: This is the cheapest possible "rotate" semantic and
  reuses an existing primitive. There is no key rotation in this
  chunk — only ciphertext-only rotation under the same vault key.
  Passphrase change is a separate, future SDD chunk.
- **Alternatives considered**:
  - *Add a `vault.Reencrypt` helper* — rejected; SDD-03 already
    delivers the same effect via `Save`. Adding a helper would
    duplicate the entry-point and increase the test surface for no
    benefit.
  - *Force a fresh derive of the vault key from the passphrase* —
    rejected; the passphrase is unchanged and the BIP32 path is
    deterministic, so the derived key is identical. The salt
    (header) DOES change because the SDD-03 file format includes a
    fresh per-file salt.

## R6. What names + descriptions are valid?

- **Decision**: Names match `^[A-Z_][A-Z0-9_]*$` with length 1–64
  (FR-017). Descriptions are free-form UTF-8 up to whatever
  `vault.Save` accepts (the codec rejects on `vault.ErrInvalidName`
  if the description exceeds the codec's bound).
- **Rationale**: The name regex is the POSIX-shell-safe identifier
  rule; this aligns with the `--exec NAME=value` env-injection
  contract from SDD-16. A name like `0FOO` or `foo-bar` would be
  rejected by `exec` anyway, so we reject at vault entry.
- **Alternatives considered**:
  - *Allow lowercase* — rejected; `NAME=` env injection assumes
    upper-case by convention, and mixing cases invites footguns.
  - *Allow length 0* — rejected; an empty name is meaningless and
    would produce empty-key env entries.

## R7. How is the secret value confirmed on `add`?

- **Decision**: Two `term.ReadPassword` calls. The first reads the
  value into a fresh `[]byte` → wrapped in `*SecureBytes` → input
  `[]byte` zeroed. The second reads the confirmation into another
  fresh `[]byte` → wrapped in a separate `*SecureBytes`. The
  comparison is done via `SecureBytes.Use(fn)` callbacks (constant-time
  byte-compare). Mismatch → `errSecretValueMismatch` →
  `ExitInputErr`. The confirmation buffer is `Destroy()`-ed
  immediately after the comparison; the original value buffer
  proceeds into `vault.Save`.
- **Rationale**: Mirrors the `init server` passphrase-confirmation
  pattern already in `internal/cli/init.go::runInitServer`. Reusing
  `secureBytesEqual` (already present in `init.go`) gives a
  constant-time compare for free.
- **Alternatives considered**:
  - *No confirmation* — rejected; FR-004 mandates re-entry to defend
    against typos.
  - *Display-with-confirm-toggle* — rejected; would require echoing
    the secret on the screen. The hidden-double-prompt is the
    industry norm.

## R8. How is the entry-name confirmation done on `remove`?

- **Decision**: A single echoing `readLineFromTTY` call with the
  prompt label `Type the entry name to confirm: `. The typed token
  is byte-compared to the `NAME` argument (case-sensitive).
  Mismatch → `errConfirmationMismatch` → `ExitInputErr`.
- **Rationale**: Forces the operator to consciously re-type the
  name. Echoing is correct here because the entry name is not a
  secret — it is the public identifier. A no-echo prompt would only
  confuse operators (they can already see the name in their shell
  history). The spec's clarification (Q2) closed this: "remove
  requires typing the entry name to confirm; rotate requires no
  extra confirmation."
- **Alternatives considered**:
  - *Confirm with `y/N`* — rejected; too easy to muscle-memory
    "yes" through.
  - *No confirmation* — rejected; FR-018 mandates the typed token.

## R9. Which slog logger do these verbs write to?

- **Decision**: `slog.Default()`. Records carry `verb`, `name`
  (where applicable), and `outcome` /`failure` fields.
- **Rationale**: The audit-chain writer (SDD-13 `internal/audit`) is
  server-side only and not in scope for CLI verbs. `slog.Default()`
  is set up with the project's redaction handler chain by
  `cmd/hush/main.go` already.
- **Alternatives considered**:
  - *Wire a chain audit writer into the CLI* — rejected; the
    chain-writer is a server-side type that requires the audit
    signing key derived from the master seed. CLI verbs already
    have the master seed at hand, but adding a writer here would
    create a second writer for the same chain (the running server
    being the first), which would race on append and break the
    chain. Server-side audit captures the same operations from a
    different vantage already.

## R10. What dependency footprint does this chunk add?

- **Decision**: Zero new direct dependencies. Reuses
  `golang.org/x/term` (already direct via `init.go`),
  `github.com/spf13/cobra` (already direct), and the internal
  packages locked at SDD-01..SDD-15.
- **Rationale**: Constitution XI ("Native-First, Minimal
  Dependencies"). The standard library covers `encoding/json`,
  `os/signal` (not needed here), `syscall.Kill`, and `regexp`.
- **Alternatives considered**: None — every primitive needed is in
  stdlib or in an already-direct dependency.

## R11. What test fixtures exist already that this chunk can reuse?

- **Decision**: Reuse:
  - `internal/testutil.NewTestVault(t, secrets)` — creates a real
    HUSH-format vault with a deterministic key.
  - `internal/testutil.SentinelSecret(17)` — returns
    `SECRET_SHOULD_NEVER_APPEAR_17` for the leak-scan tests.
  - `internal/testutil.AssertSentinelAbsent(t, sentinel, haystack)`
    — fails the test on any hit, with a 64-byte context window.
  - The existing pty-based helper from
    `internal/cli/init_helpers_test.go` —
    `TestReadPassphraseTTY_ViaPTY` shows how to drive
    `term.ReadPassword` from a unit test.
- **Rationale**: The testutil package was designed to absorb this
  kind of test fixture. Adding a sentinel `17` is the only new
  testutil constant needed, and the existing constructor accepts an
  arbitrary `n`.
- **Alternatives considered**:
  - *Add a per-chunk fixtures file* — rejected; testutil already
    covers the cases.

## R12. What is the operator UX for an empty vault on `list`?

- **Decision**:
  - **stdout-TTY** → stderr message `(vault is empty)\n`, stdout is
    silent, exit 0.
  - **stdout-pipe** → stdout `[]\n`, exit 0.
- **Rationale**: An empty vault is a legal, expected state right
  after `hush init server`. It is not an error. The empty-state
  text on TTY goes to STDERR so downstream tooling that pipes
  stdout sees nothing extraneous; the JSON form is a clean empty
  array.
- **Alternatives considered**:
  - *stdout TTY message instead of stderr* — rejected; mixing
    "hint" text into stdout breaks the contract that stdout is the
    machine-readable surface for the JSON pipe path. Keeping all
    hints on stderr keeps both modes byte-clean.
  - *Exit 4 (`ExitNotFound`) on empty vault* — rejected; an empty
    vault is not a not-found condition. SC-001 mandates an empty
    result with a successful exit.
