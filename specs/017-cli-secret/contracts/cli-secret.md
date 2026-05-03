# CLI Contract — `hush secret`

This file locks the operator-facing surface of `hush secret` for
SDD-17. Every literal string below is asserted byte-equal by a unit
test in [internal/cli/secret_test.go](../../../internal/cli/secret_test.go).

---

## 0. Subcommand tree

```
hush
└── secret
    ├── add NAME
    ├── remove NAME
    ├── list
    └── rotate
```

Mounted via `root.AddCommand(newSecretCmd())` inside `Execute`
([internal/cli/root.go](../../../internal/cli/root.go)). No new
exported package-level symbols. The cobra command tree IS the
contract.

The `secret` parent command has no `Run*`; invoking `hush secret`
without a verb prints the parent's help and exits non-zero (default
cobra behavior, matching the `hush init` parent locked at SDD-15).

---

## 1. Flags

**Universal global flags** (inherited from the SDD-14 root):
`--config/-c`, `--verbose/-v`, `--quiet/-q`, `--no-color`.

**Per-verb flags**: NONE. There is no `--value`, `--secret`,
`--password`, `--description`, `--force`, `--yes`, `--no-confirm`,
or any other flag. Cobra rejects any such flag with its own
"unknown flag" parse error — verified by
`TestSecret_AddRefusesValueFlag`.

This structural absence is the FR-003 / FR-016 / SC-007 invariant.

---

## 2. Positional arguments

| Verb | Args | Validation |
|------|------|------------|
| `add` | exactly one: `NAME` | matches `^[A-Z_][A-Z0-9_]*$`, length 1–64 |
| `remove` | exactly one: `NAME` | matches `^[A-Z_][A-Z0-9_]*$`, length 1–64 |
| `list` | none | cobra `cobra.NoArgs` |
| `rotate` | none | cobra `cobra.NoArgs` |

Wrong arity is rejected by cobra with its own "accepts N arg(s),
received M" error → exit code 1 (cobra's own behaviour; not mapped
through `mapErr`).

A name that fails the regex/length is rejected by our code with
`errInvalidSecretName` → `ExitInputErr` (2). The validation runs
BEFORE any vault I/O (FR-017).

---

## 3. Locked literal strings

### 3.1 stderr — TTY refusal (universal across all four verbs)

```text
hush: secret: this command requires an interactive TTY (rogue-process defence)
```

Asserted byte-equal by `TestSecret_AddRefusesPipedStdin` (also
parameterised over `remove`, `list`, `rotate`).

### 3.2 stderr — name validation failure

```text
hush: secret: NAME must match ^[A-Z_][A-Z0-9_]*$ (1–64 chars)
```

(Note the `–` is the en-dash U+2013, matching the regex's
spelling in the spec.)

### 3.3 stderr — `add` confirmation mismatch

```text
hush: secret: secret value confirmation does not match
```

### 3.4 stderr — `add` already-exists refusal

The format string is:

```text
hush: secret: entry %s already exists; use 'hush secret rotate' to replace
```

`%s` is the entry name argument verbatim (already validated against
the regex, so it is shell-safe to interpolate). The existing value
is NEVER referenced.

### 3.5 stderr — `remove` typed-name confirmation mismatch

```text
hush: secret: typed name does not match the entry argument
```

### 3.6 stderr — `rotate` PID branch messages

| Branch | Message |
|--------|---------|
| `pidPresent` (INFO line; not a warning) | `hush: secret: signalled running server (pid=%d)` |
| `pidAbsent` (WARN) | `hush: secret: no running server signalled (no PID file)` |
| `pidStale` (WARN) | `hush: secret: no running server signalled (stale PID file)` |
| `pidNotOurUser` (WARN) | `hush: secret: no running server signalled (PID owned by another user)` |
| `pidUnreadable` (WARN) | `hush: secret: no running server signalled (PID file unreadable)` |

The INFO line is emitted to stderr (matches the rest of the verb's
diagnostic output). The vault rewrite has already succeeded by the
time this dispatch runs, so every branch returns `ExitOK`.

### 3.7 stderr — `list` empty-vault hint (TTY only)

```text
(vault is empty)
```

Emitted to stderr (NOT stdout) so a TTY operator sees it but a
piped consumer of stdout sees only the JSON empty array `[]\n`.

---

## 4. Locked prompt labels

All prompts go to stderr (matching `init.go`'s convention so
machine-driven stdin with a tty stdout still sees the prompts). The
trailing `: ` is part of the locked string.

| Label | Used by | Echoes? |
|-------|---------|---------|
| `Vault passphrase: ` | every verb (reuses `init.go` constant `promptVaultPassphrase`) | no (term.ReadPassword) |
| `Secret value: ` | `add` | no |
| `Confirm secret value: ` | `add` | no |
| `Description (optional): ` | `add` | yes (line read) |
| `Type the entry name to confirm: ` | `remove` | yes (line read) |

`rotate` has only the passphrase prompt; the spec's clarification
Q2 closed the question of "additional confirmation on rotate?" with
"no extra confirmation" (only the passphrase entry suffices).

---

## 5. `list` rendering format

### 5.1 stdout-TTY (text)

One line per entry. Format string per line:

```text
%s — %s\n
```

(`—` is the em-dash U+2014; surrounded by single ASCII spaces.)
For an entry with empty `Description` the format string degenerates
to `%s\n` (no separator, no trailing space) so a description-free
entry doesn't render a dangling em-dash.

Entries are sorted ascending by `Name` using lexicographic byte
comparison (Go `sort.Strings` semantics) before rendering. The
sort is stable across runs over an unchanged vault (FR-008).

Empty vault → no stdout output; stderr message `(vault is empty)\n`
(see §3.7).

### 5.2 stdout-pipe (JSON)

A single JSON array. Field order — `name` then `description` —
matches the struct declaration order; `encoding/json` preserves it.
A populated vault renders, with no leading whitespace inside elements:

```json
[{"name":"FOO","description":"thing one"},{"name":"GITHUB_TOKEN","description":""}]
```

Followed by a single trailing `\n` (added by `encoding/json.Encoder`).
Empty vault renders `[]\n`.

The renderer NEVER touches `Secret.Value`; the
`*securebytes.SecureBytes` value handles obtained from
`store.Get(name)` are `Destroy()`-ed BEFORE the renderer runs.
`TestSecret_ListNoValues` plants
`testutil.SentinelSecret(17) = "SECRET_SHOULD_NEVER_APPEAR_17"` as
the value of one entry and asserts the sentinel does not appear in
either stdout or stderr in either rendering mode (SC-002).

---

## 6. Exit-code map

| Condition | Sentinel | Exit |
|-----------|----------|------|
| Success | (nil) | 0 (`ExitOK`) |
| stdin not a TTY (any verb) | `errNoTTY` (existing) | 2 (`ExitInputErr`) |
| Invalid `NAME` argument | `errInvalidSecretName` → wraps `errMissingFlag` | 2 (`ExitInputErr`) |
| `add` confirmation mismatch | `errSecretValueMismatch` → wraps `errPassphraseMismatch` | 2 (`ExitInputErr`) |
| `remove` typed-name mismatch | `errConfirmationMismatch` → wraps `errPassphraseMismatch` | 2 (`ExitInputErr`) |
| Wrong passphrase / vault auth | `vault.ErrAuthFailed` | 3 (`ExitAuth`) |
| `remove` of an absent entry | `vault.ErrSecretNotFound` | 4 (`ExitNotFound`) |
| `add` of an existing entry | `errSecretExists` (no wrap) | 1 (`ExitErr`) |
| Vault file mode loose | `vault.ErrFilePermsLoose` | 5 (`ExitPerm`) |
| Vault file missing / state-dir not found | `fs.ErrNotExist` / `config.ErrStateDirNotFound` | 4 (`ExitNotFound`) |
| Any other error | (unmapped) | 1 (`ExitErr`) |

Re-uses the locked `mapErr` from
[internal/cli/exit_codes.go](../../../internal/cli/exit_codes.go) —
NO edits to that file. The `errSecretExists` branch hits the
catch-all `ExitErr` arm (deliberate; see plan §"Project Structure").

---

## 7. Audit-log records

`slog.Default()` records emitted by this chunk. JSON shape under
`logging.New` is locked by SDD-05; this chunk only describes the
`msg` and the structured fields.

### 7.1 Success records (INFO)

| Verb | `msg` | Fields |
|------|-------|--------|
| `add` | `secret_added` | `verb=add`, `name=<NAME>`, `outcome=success` |
| `remove` | `secret_removed` | `verb=remove`, `name=<NAME>`, `outcome=success` |
| `rotate` | `vault_rotated` | `verb=rotate`, `outcome=success`, `signalled=<bool>` (`true` only on `pidPresent`) |

`list` does NOT emit an audit record on success (read-only operation;
the spec's clarification Q4 limits audit scope to write operations
plus security-relevant failures).

### 7.2 Security-relevant failure records (WARN)

| Failure | `msg` | Fields |
|---------|-------|--------|
| TTY refusal | `secret_tty_refused` | `verb=<verb>`, `outcome=tty_refused` |
| Wrong passphrase | `secret_passphrase_failed` | `verb=<verb>`, `name=<NAME or "">`, `outcome=passphrase_failed` |
| Typed-name confirmation mismatch on `remove` | `secret_confirmation_mismatch` | `verb=remove`, `name=<NAME>`, `outcome=confirmation_mismatch` |
| `add` value confirmation mismatch | `secret_confirmation_mismatch` | `verb=add`, `name=<NAME>`, `outcome=value_mismatch` |

Routine input-validation refusals (malformed name, missing arg,
unknown flag) are NOT logged.

NO record EVER carries the secret value, the passphrase, the
confirmation token, or any byte derived from them.
`TestSecret_AuditLogOmitsSecretBytes` asserts the sentinel string
is absent from the captured slog handler output across the full
happy-path of all four verbs.

---

## 8. PID file format (read-only here)

`<state_dir>/hush.pid` — text file containing a single ASCII decimal
integer, optionally followed by trailing whitespace (newline).
`rotate` parses with `strconv.Atoi(strings.TrimSpace(string(b)))`.
A parse failure is treated as `pidUnreadable`.

This chunk does NOT write the PID file. A future SDD chunk wires
PID-writing into `serve`. Until then `rotate` always exercises the
`pidAbsent` branch in production — which is a legal, documented
outcome (FR-011, SC-005).

---

## 9. Test surface (locked names)

Every test below lives in
[internal/cli/secret_test.go](../../../internal/cli/secret_test.go)
unless flagged otherwise. The list matches SDD-17 §"Tests required"
plus the additions surfaced during Phase 0/1.

| Test name | Asserts |
|-----------|---------|
| `TestSecret_AddRefusesPipedStdin` | piped stdin → `ExitInputErr`, byte-equal stderr message §3.1, no vault touched |
| `TestSecret_AddTTYHappyPath` | pty fixture; valid name + matching confirmation → vault contains entry, mode `0600` |
| `TestSecret_AddRefusesValueFlag` | `hush secret add --value foo NAME` → cobra "unknown flag" error |
| `TestSecret_AddRefusesPasswordFlag` | `hush secret add --password foo NAME` → cobra "unknown flag" error |
| `TestSecret_AddRefusesSecretFlag` | `hush secret add --secret foo NAME` → cobra "unknown flag" error |
| `TestSecret_AddInvalidName` | `hush secret add foo` → `ExitInputErr`, stderr §3.2, vault not touched |
| `TestSecret_AddConfirmationMismatch` | mismatched confirmation → `ExitInputErr`, stderr §3.3, vault not touched |
| `TestSecret_AddDuplicateRefuses` | adding an existing name → `ExitErr`, stderr §3.4, existing value bytes never appear in stderr |
| `TestSecret_RemoveRefusesPipedStdin` | piped stdin → refusal |
| `TestSecret_RemoveAtomic` | pty + matching token → vault rewrites without the entry; on-disk size sane; no temp file lingers |
| `TestSecret_RemoveAbsent` | nonexistent name → `ExitNotFound`, vault not touched |
| `TestSecret_RemoveTokenMismatch` | typed token `\!= NAME` → `ExitInputErr`, stderr §3.5, vault not touched |
| `TestSecret_ListNoValues` | populated vault including sentinel value → both rendering modes, sentinel absent from stdout AND stderr (SC-002) |
| `TestSecret_ListJSONOutput` | stdout=pipe → byte-equal JSON shape per §5.2 |
| `TestSecret_ListTTYOutput` | stdout=tty → human-line format per §5.1 |
| `TestSecret_ListEmptyVault` | empty vault, both modes (TTY → stderr `(vault is empty)`; pipe → stdout `[]\n`) |
| `TestSecret_ListSortedAscending` | vault populated in random order → both modes render entries sorted ascending by Name |
| `TestSecret_ListRefusesPipedStdin` | piped stdin → refusal even when stdout is a pipe |
| `TestSecret_RotateRefusesPipedStdin` | piped stdin → refusal |
| `TestSecret_RotateAtomic` | rotate writes byte-different ciphertext while preserving entry set (names + descriptions) (SC-003) |
| `TestSecret_RotateSendsSIGHUP` | helper child writes its PID to `<state_dir>/hush.pid` and waits for SIGHUP; rotate signals it; child exits 0 within 2s; INFO line on stderr |
| `TestSecret_RotateMissingPIDTolerant` | no PID file → `ExitOK`, WARN stderr §3.6 (`pidAbsent` branch) |
| `TestSecret_RotateStalePIDTolerant` | PID file contains an unused PID → `ExitOK`, WARN stderr §3.6 (`pidStale` branch) |
| `TestSecret_RotateUnreadablePIDTolerant` | PID file contains garbage → `ExitOK`, WARN stderr §3.6 (`pidUnreadable` branch) |
| `TestSecret_HelpDoesNotMentionValueFlags` | `hush secret add --help` text scanned for `--value`, `--secret`, `--password`; all absent (SC-007) |
| `TestSecret_AuditLogOmitsSecretBytes` | captured slog handler across all four verbs' happy-paths; sentinel absent |
| `TestSecret_ErrorsDoNotLeakSecretBytes` | every documented failure path's error string AND captured stderr scanned for sentinel; absent |
| `TestSecret_PassphraseFailureSurfacesAuthCode` | wrong passphrase on any verb → `ExitAuth` |
| `TestSecret_FileModeAfterAdd` | post-`add` vault file mode is `0600` |
| `TestSecret_FileModeAfterRotate` | post-`rotate` vault file mode is `0600` |

Coverage target on the `secret` portion of `internal/cli`: **85%**
(per SDD-17 contract). Tests run under both `darwin` and `linux`
build targets (the SIGHUP child-process helper is POSIX-portable;
no os-conditional skips).
