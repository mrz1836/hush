# Feature Specification: hush secret — Vault Entry Management

**Feature Branch**: `017-cli-secret`
**Created**: 2026-05-03
**Status**: Draft
**Input**: User description: "hush secret: add/remove/list/rotate vault entries; write subcommands REFUSE if stdin is not an interactive TTY (defends the rogue-process threat); values entered only via hidden TTY prompt, never via flag; list never prints values; rotate signals running server via SIGHUP (tolerates missing PID file with a warning)"

## Overview

`hush secret` is the operator-facing vault-management command. It exposes four
verbs — `add`, `remove`, `list`, `rotate` — that the vault owner uses to
populate, prune, inspect, and re-encrypt the vault file on the trusted host.

All four verbs refuse to run unless their standard **input** is an
interactive terminal. For the write paths (`add`, `remove`, `rotate`)
this refusal is the documented defence against the "rogue process runs
`hush secret add`" threat in `docs/SECURITY.md`: a background process
that has somehow obtained shell access on the vault host MUST NOT be
able to silently inject, replace, or remove a vault entry by piping
bytes into the command. For `list` the same gate applies because every
verb requires a passphrase prompt at the terminal — a piped stdin would
either steal that prompt or silently bypass authentication.

`list` is read-only and intentionally narrow: it emits names and descriptions,
never values. Independently of the stdin TTY gate, the rendering format
keys off **stdout** — human-readable text on a TTY, JSON when stdout is
piped or redirected. Neither path ever discloses a secret value, and the
supported invocation `hush secret list | jq` works from a real terminal
(stdin is a TTY, stdout is a pipe).

`rotate` re-encrypts the vault file in place (same secrets, fresh nonce/salt)
and signals the running server via SIGHUP so the live server picks up the new
ciphertext atomically (the SDD-10 reload mechanism). When no running server
is present, `rotate` still completes the file write and exits successfully
with a warning — rotating an offline vault is a legitimate operator action,
not an error.

## Clarifications

### Session 2026-05-03

- Q: Entry name validation rule (charset and length)? → A: POSIX-shell-safe identifier `^[A-Z_][A-Z0-9_]*$`, length 1–64
- Q: Confirmation gate on destructive verbs `remove` and `rotate`? → A: `remove` requires typing the entry name to confirm; `rotate` requires no extra confirmation
- Q: Does `list` require an interactive TTY (for the passphrase prompt)? → A: `list` requires stdin to be a TTY; stdout pipe-aware drives the JSON-vs-text rendering choice
- Q: Audit-log scope — failures and refusals as well as successes? → A: log successes plus security-relevant failures (TTY-refusal, passphrase-failure, confirmation-mismatch); skip routine input-validation refusals
- Q: `list` output ordering? → A: sorted ascending by entry name (ASCII byte order)

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Add a secret to the vault (Priority: P1)

The vault owner sits at the trusted host's terminal and adds a new named
secret to the vault. They invoke `hush secret add <NAME>`, are prompted for
the secret value at the terminal (no echo), confirm by re-entry, and the
vault file is updated.

**Why this priority**: Without `add`, the vault is empty and every other
hush feature has nothing to broker. This is the bootstrap path for the entire
product.

**Independent Test**: From a fresh `hush init`, run `hush secret add FOO` at
a real terminal, supply a value at the hidden prompt, then confirm
`hush secret list` reports `FOO` and the vault file on disk has been updated
(size changed, still mode `0600`).

**Acceptance Scenarios**:

1. **Given** an initialised vault and stdin attached to an interactive
   terminal, **When** the operator runs `hush secret add ANTHROPIC_API_KEY`
   and enters a value at the hidden prompt, **Then** the vault file is updated
   with the new entry and a success message names the entry without printing
   its value.
2. **Given** an initialised vault and stdin attached to a pipe (e.g.
   `echo secret | hush secret add NAME`), **When** the command runs,
   **Then** it refuses with an input-error exit code and a message that
   names the rogue-process defence as the reason; the vault file is not
   touched.
3. **Given** the operator passes a `--value` flag (or any equivalent flag
   that would carry a secret value on the command line), **When** the
   command runs, **Then** the command refuses with an input-error exit code;
   no such flag exists in the help text.
4. **Given** the operator's confirmation prompt does not match the first
   entry, **When** confirmation is checked, **Then** the command refuses
   with an input-error exit code and the vault is unchanged.
5. **Given** an entry with the same name already exists, **When** the
   operator runs `add` for that name, **Then** the command refuses with a
   clear "entry already exists; use `rotate` to replace" message; the
   existing value is unchanged.

---

### User Story 2 — List vault entries without disclosing values (Priority: P1)

The vault owner inspects what is currently stored. The `list` subcommand
prints the name and description of every entry. It never prints the value.
Output format follows the project's TTY/pipe convention: human-readable
text when stdout is a TTY, JSON when stdout is redirected. `list` itself
still requires an interactive **stdin** so the vault passphrase can be
prompted safely.

**Why this priority**: Operators need to confirm what is in the vault before
rotating, removing, or requesting a secret. Without `list`, the vault is
opaque. The "never prints values" property is a hard security invariant.

**Independent Test**: With a populated vault, run `hush secret list` at a
TTY and verify each entry appears as one line. Run `hush secret list | cat`
and verify the output is a JSON array of `{name, description}` objects.
In both modes, search the output for any known secret value and confirm it
does not appear.

**Acceptance Scenarios**:

1. **Given** a vault with three entries, **When** the operator runs
   `hush secret list` at a terminal, **Then** stdout shows one human-readable
   line per entry containing the name and description.
2. **Given** the same vault, **When** the operator runs
   `hush secret list` with stdout redirected to a pipe or file, **Then**
   stdout contains a JSON array with one object per entry, each object
   carrying `name` and `description` and nothing more.
3. **Given** a vault containing a known sentinel value, **When** the operator
   runs `hush secret list` in either mode, **Then** the sentinel string does
   not appear anywhere in stdout or stderr.
4. **Given** an empty vault, **When** the operator runs `hush secret list`,
   **Then** the command exits successfully with an empty result (empty JSON
   array on a pipe; an empty-state human message on a TTY) and no error.

---

### User Story 3 — Rotate the vault and notify a live server (Priority: P1)

After changing the vault passphrase, or as a periodic hygiene step, the
operator rotates the vault file: the same set of secrets is re-encrypted
with fresh randomness so the on-disk ciphertext changes. If a vault
server is currently running, it must pick up the new ciphertext without
dropping in-flight requests. If no server is running, the rotation still
succeeds.

**Why this priority**: Rotation is the operator's lever for re-encrypting
under a new passphrase and for periodic re-keying. The "no in-flight failure"
property is part of acceptance criterion AC-2.

**Independent Test**: With a running server and the vault populated, run
`hush secret rotate` at a TTY. Verify (a) the vault file's bytes have
changed, (b) the running server received SIGHUP, (c) `hush secret list`
afterward returns the same set of entries. Repeat with no running server
and verify the rotation succeeds with a warning.

**Acceptance Scenarios**:

1. **Given** a populated vault and a running server identified by a PID
   file in the vault state directory, **When** the operator runs
   `hush secret rotate` at a TTY, **Then** the vault file is rewritten with
   different ciphertext, the running server is signalled via SIGHUP, the
   set of entry names and descriptions is preserved, and the command
   reports success.
2. **Given** a populated vault and no running server (no PID file present),
   **When** the operator runs `hush secret rotate` at a TTY, **Then** the
   vault file is still rewritten and the command exits successfully with a
   warning that no running server was signalled.
3. **Given** stdin is a pipe rather than a terminal, **When**
   `hush secret rotate` runs, **Then** the command refuses with an
   input-error exit code and the vault file is not touched.
4. **Given** the server is mid-flight serving a `/secrets/...` request when
   SIGHUP arrives, **When** the rotation completes, **Then** the in-flight
   request is satisfied from the pre-rotation vault and subsequent requests
   are served from the post-rotation vault. (This property is enforced by
   the server's reload mechanism and is verified end-to-end as part of
   AC-2.)

---

### User Story 4 — Remove an entry (Priority: P2)

The operator deletes a secret that is no longer needed (decommissioned
service, leaked key, rotated upstream credential). `remove` deletes the
named entry from the vault and persists the change atomically.

**Why this priority**: Removal is operationally important but not on the
bootstrap critical path. P2 reflects that no other feature is blocked on
removal landing first.

**Independent Test**: With a populated vault, run `hush secret remove NAME`
at a TTY. Verify the named entry is gone from `hush secret list` and the
remaining entries are unchanged.

**Acceptance Scenarios**:

1. **Given** a vault containing entry `FOO` and stdin is an interactive
   terminal, **When** the operator runs `hush secret remove FOO` and types
   `FOO` at the confirmation prompt, **Then** the vault file is rewritten
   without `FOO`, the other entries remain, and the command reports success.
2. **Given** stdin is a pipe, **When** `hush secret remove FOO` runs,
   **Then** the command refuses with an input-error exit code and the
   vault file is not touched.
3. **Given** an entry with the supplied name does not exist, **When**
   `hush secret remove NOPE` runs, **Then** the command exits with the
   not-found exit code and the vault file is not touched.
4. **Given** the operator types a value at the confirmation prompt that
   does not exactly match the entry name argument, **When**
   `hush secret remove FOO` runs and the operator types `foo` (or anything
   other than `FOO`), **Then** the command refuses with an input-error
   exit code and the vault file is not touched.

---

### Edge Cases

- **Concurrent edits**: two operators (or two terminals on the same host)
  attempt overlapping `add` / `remove` / `rotate` operations. The vault
  file write is atomic per operation; the spec does not require multi-writer
  coordination. The "last writer wins" outcome is acceptable for an
  operator-only command.
- **Crash mid-write**: if the process is terminated between reading the
  current vault and writing the new one, the on-disk vault MUST remain
  decryptable and consistent (atomic-rename guarantee).
- **Missing or unreadable vault file**: `add`, `remove`, `list`, and
  `rotate` MUST surface a clear "no vault initialised; run `hush init`"
  message rather than a generic decode error.
- **Wrong passphrase entered at TTY**: command refuses with an
  authentication-error exit code; the vault file is not touched.
- **Stale PID file** (PID file present, but no live process at that PID):
  `rotate` MUST treat this the same as a missing PID file — complete the
  write, log a warning, exit successfully. The operator is told that no
  live server was signalled.
- **PID file points at a process not owned by the current user**: `rotate`
  MUST NOT signal a process it does not own. Treat as if no live server is
  available; warn and exit successfully.
- **Empty `NAME` or otherwise-invalid name**: an entry name MUST match
  `^[A-Z_][A-Z0-9_]*$` and be 1–64 characters long (POSIX-shell-safe
  identifier; aligns with the `--exec NAME=value` env-injection contract).
  Names that fail this rule MUST be refused with a clear validation error
  before the vault is opened.
- **`list` against an empty vault**: succeeds with an empty result; not an
  error.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST expose a `secret` subcommand with four verbs:
  `add <NAME>`, `remove <NAME>`, `list`, and `rotate`. Each verb's contract
  is specified below.
- **FR-002**: For `add`, `remove`, `rotate`, and `list`, the system MUST
  refuse to proceed unless the process's standard input is an interactive
  terminal. The refusal MUST emit a clear message explaining that the
  command requires an interactive terminal and citing the rogue-process
  defence. The exit status MUST be the project's input-error code. (The
  stdin TTY gate is universal because every subcommand prompts for the
  vault passphrase per FR-014; `list` is stdin-TTY-required but remains
  stdout-pipe-aware for its JSON rendering — see FR-008.)
- **FR-003**: The system MUST NOT accept a secret value via any
  command-line flag, argument, or environment variable. Secret values MUST
  be entered only at a hidden terminal prompt with no echo.
- **FR-004**: For `add`, the system MUST prompt for the value twice and
  MUST refuse to write if the two entries differ. The system MUST also
  prompt for an optional human-readable description that is stored
  alongside the value.
- **FR-005**: For `add`, if an entry with the supplied name already exists,
  the system MUST refuse and direct the operator to use `rotate` (or
  `remove` followed by `add`) to replace the value. This refusal MUST NOT
  disclose the existing value or any portion of it.
- **FR-006**: For `remove`, the system MUST refuse with the not-found exit
  code if the supplied name does not match any existing entry.
- **FR-007**: For `list`, the system MUST emit name and description fields
  for every entry and MUST NOT emit, log, or otherwise expose any entry's
  value. This invariant holds in both TTY and pipe rendering modes and is
  testable via a sentinel-value scan of stdout and stderr.
- **FR-008**: For `list`, the rendering format is selected by stdout's
  TTY-ness (independent of the stdin TTY gate in FR-002). When stdout is
  an interactive terminal, the system MUST render entries in human-readable
  text form (one entry per line, name and description). When stdout is not
  a terminal (pipe, redirect), the system MUST render entries as a JSON
  array of objects, each with `name` and `description` keys and no other
  keys. In both rendering modes, entries MUST be ordered ascending by
  `name` using lexicographic byte comparison (Go `sort.Strings` semantics);
  the order MUST be stable across runs over an unchanged vault. The
  supported invocation `hush secret list | jq` MUST work from a real
  terminal: stdin is a TTY (passphrase prompt allowed), stdout is a pipe
  (JSON rendering selected).
- **FR-009**: For `rotate`, the system MUST re-encrypt the vault file with
  a freshly drawn nonce and salt while preserving the exact set of entries
  (names, descriptions, values). The on-disk file mode MUST remain `0600`.
- **FR-010**: For `rotate`, when a vault server is running on the same host,
  the system MUST signal that server via SIGHUP after the new vault file is
  in place, so that the server's reload mechanism picks up the rotated
  ciphertext.
- **FR-011**: For `rotate`, when no running server is detected (no PID
  record present, or the recorded PID is stale, or the recorded PID
  belongs to a process the current user cannot signal), the system MUST
  complete the file rewrite, emit a clear warning that no live server was
  notified, and exit successfully — not as an error.
- **FR-012**: All write subcommands (`add`, `remove`, `rotate`) MUST update
  the vault file atomically: a power loss or kill mid-write MUST leave
  either the pre-write vault or the post-write vault on disk, never a
  truncated or partially-written file.
- **FR-013**: The system MUST NOT print, log, redact-as-prefix, or
  otherwise leak secret values via any output channel (stdout, stderr,
  audit log, slog records). Error messages MUST identify entries by name
  only.
- **FR-014**: The system MUST authenticate the operator by requiring the
  vault passphrase to be entered at the same interactive terminal session
  that runs the command. The passphrase MUST NOT be obtainable from a flag,
  environment variable, or piped stdin.
- **FR-015**: The system MUST emit an audit-log event for every successful
  write operation (`add`, `remove`, `rotate`) describing the operation and
  the affected entry name, but never the value. In addition, the system
  MUST emit an audit-log event for every security-relevant failure: a
  refused TTY check (FR-002), a passphrase that fails to unlock the vault
  (FR-014), and a typed-name confirmation mismatch on `remove` (FR-018).
  Each failure event MUST record the verb, the affected entry name (when
  applicable), and the failure category, and MUST NOT include the
  passphrase, the typed confirmation token, or any secret value. Routine
  input-validation refusals (malformed name per FR-017, missing positional
  argument, unknown verb) are NOT audited.
- **FR-016**: Help text and command-listing output MUST describe the four
  verbs and MUST NOT advertise any flag that carries a secret value.
- **FR-017**: For `add` and `remove`, the system MUST validate the supplied
  entry name against the rule `^[A-Z_][A-Z0-9_]*$` with length 1–64 before
  any vault I/O. A name that fails this rule MUST be refused with the
  input-error exit code and a clear message; the vault file MUST NOT be
  opened, decrypted, or rewritten.
- **FR-018**: For `remove`, the system MUST prompt the operator to type the
  entry name as a confirmation token after the entry is located in the
  vault. The typed token MUST match the entry-name argument byte-for-byte
  (case-sensitive). A mismatch MUST result in refusal with the input-error
  exit code and the vault file MUST NOT be rewritten. `rotate` does not
  require an additional confirmation prompt beyond passphrase entry.

### Key Entities *(include if feature involves data)*

- **Vault entry**: a named record with three attributes — a name (operator-
  chosen identifier, e.g. `ANTHROPIC_API_KEY`), an optional human-readable
  description, and a secret value. Names are unique within a vault.
- **Vault file**: the on-disk encrypted artifact owned by the vault host.
  Holds the entire set of vault entries. Subject to project file-mode
  invariants (`0600`).
- **Server PID record**: the on-disk marker of a running vault server,
  consulted by `rotate` to deliver SIGHUP. Absence is a normal operational
  state, not an error.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of attempts to run any `hush secret` subcommand
  (`add`, `remove`, `list`, `rotate`) with a non-terminal stdin (pipe,
  redirect, here-doc) result in refusal with the input-error exit code and
  a clear message. `list` MUST still succeed when only stdout is piped
  (e.g. `hush secret list | jq`) because stdin remains a TTY.
- **SC-002**: 0 occurrences of a secret value in any captured stdout or
  stderr from `hush secret list` across both rendering modes, verified by a
  sentinel-value scan in CI.
- **SC-003**: After `hush secret rotate`, the vault file's ciphertext bytes
  differ from the pre-rotation bytes in 100% of runs (fresh nonce and
  salt), and the post-rotation `hush secret list` returns exactly the same
  set of names and descriptions as the pre-rotation listing.
- **SC-004**: When a vault server is running with a known PID record,
  `hush secret rotate` causes the server to reload the rotated vault in
  100% of runs, with no in-flight `/secrets/...` request observed to fail
  due to the rotation.
- **SC-005**: When `hush secret rotate` runs with no live server reachable
  (no PID record, stale PID, or unsignal-able PID), the command exits
  successfully with a warning in 100% of runs.
- **SC-006**: An operator who has run `hush init` can complete a full
  bootstrap (`add` three secrets, `list` to confirm, `rotate`, `list`
  again) in under 2 minutes at a terminal.
- **SC-007**: The `hush secret` help output contains zero flags whose name
  or description suggests carrying a secret value (e.g. `--value`,
  `--secret`, `--password`).

## Assumptions

- The operator runs `hush secret` on the trusted host where the vault file
  lives. Remote management of the vault is out of scope.
- A separate `hush init` flow has already created the vault file, the
  state directory, and the operator's passphrase entry in the OS keychain.
- The vault server, when running, exposes its PID via a file at a
  well-known location inside the vault state directory. The exact path is
  an implementation detail handled at plan time.
- The project's existing exit-code vocabulary (success / error / input-error
  / auth-error / not-found / permission) is reused without extension.
- The project's existing TTY / pipe auto-detection convention (text on a
  TTY, JSON on a pipe) is reused without extension.
- The project's existing `SecureBytes` discipline for in-memory secret
  handling and audit-log discipline for security-relevant events are
  reused without extension.
- The vault server's atomic-reload mechanism (introduced in an earlier
  chunk) is the contract `rotate` depends on; this spec does not redesign
  it.
- Single-operator, single-host operation. Multi-writer coordination across
  hosts is out of scope.
