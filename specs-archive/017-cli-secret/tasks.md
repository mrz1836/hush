---

description: "Task list for SDD-17 — hush secret (add/remove/list/rotate; TTY-only writes; SIGHUP reload)"
---

# Tasks: hush secret — Vault Entry Management (SDD-17)

**Input**: Design documents from `/Users/mrz/projects/hush/specs/017-cli-secret/`
**Prerequisites**: plan.md (✅), spec.md (✅), research.md (✅), data-model.md (✅), contracts/cli-secret.md (✅), quickstart.md (✅)

**Tests**: REQUIRED — TDD-mandatory per Constitution VIII. Every behaviour-contract test is written BEFORE its implementation task and MUST fail (red) before the implementation task lands (green). Coverage target: **85%** on the secret portion of `internal/cli`.

**Organization**: Tasks are grouped by user story (US1=add, US2=list, US3=rotate, US4=remove) per `spec.md`'s priority ordering (P1, P1, P1, P2). All implementation lands in two new files — `internal/cli/secret.go` and `internal/cli/secret_test.go` — so within-file `[P]` markers are rare; cross-story parallelism is bounded by file ownership.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1 = Add, US2 = List, US3 = Rotate, US4 = Remove)
- All paths are absolute from repo root: `/Users/mrz/projects/hush`

## Path Conventions

This is a Go CLI tool. Production code lives under `internal/cli/`. Test code lives next to production code (`*_test.go`). Test fixtures live under `internal/testutil/`. Docs live under `docs/`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Pre-feature scaffolding shared across every user story. Lands the leak-scan sentinel and confirms the existing pty/testutil fixtures are reachable from `internal/cli`.

- [X] T001 [P] Add sentinel constant `SentinelSecret17 = "SECRET_SHOULD_NEVER_APPEAR_17"` (or extend the existing `testutil.SentinelSecret(n int) string` to recognise `17`) in [internal/testutil/sentinels.go](../../internal/testutil/sentinels.go) — used by every leak-scan test in this chunk
- [X] T002 [P] Confirm/extend [internal/testutil/vault.go](../../internal/testutil/vault.go) `NewTestVault` accepts a slice of `(name, description, value)` triples and returns `(vaultPath string, key *securebytes.SecureBytes, salt []byte)` so tests can pre-populate the vault without going through the cobra surface; add a focused unit test if behaviour is added
- [X] T003 [P] Confirm pty-fixture helper `runWithPTY(t, stdinScript []byte, fn func(in *os.File))` exists in [internal/cli/init_helpers_test.go](../../internal/cli/init_helpers_test.go); if not, lift it into a package-internal `secret_helpers_test.go` so multiple test files can share it

**Checkpoint**: testutil exposes the sentinel and vault constructor; pty fixture is reachable from new tests.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core CLI scaffolding (cobra wiring, mount on root, `secretDeps` struct, TTY gate, name validation, mapErr sentinels). Every behaviour-contract test in Phase 3+ depends on this skeleton existing.

**⚠️ CRITICAL**: No user-story work can begin until this phase is complete. Both files (`secret.go`, `secret_test.go`) gain shared scaffolding here.

### Foundational TESTS (TDD: write first, ensure red)

- [X] T004 Write `TestSecret_HelpDoesNotMentionValueFlags` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — invokes `hush secret add --help` (and `remove`, `list`, `rotate`); asserts the help output contains zero occurrences of `--value`, `--secret`, `--password`, `--description`, `--force`, `--yes`, `--no-confirm` (SC-007, FR-016, contracts/cli-secret.md §1)
- [X] T005 Write `TestSecret_RootMounts` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — asserts `hush secret` parent command exists with subcommands exactly `[add, remove, list, rotate]` (no others); cobra rejects bare `hush secret` with non-zero exit (no `RunE` on parent)

### Foundational IMPLEMENTATION

- [X] T006 Create [internal/cli/secret.go](../../internal/cli/secret.go) skeleton: package decl, file-scope sentinels (`errInvalidSecretName`, `errSecretValueMismatch`, `errConfirmationMismatch`, `errSecretExists` per data-model.md §2), `pidFilename = "hush.pid"` constant with comment citing SDD-17 §"Implementation contract" and the rogue-process threat row in `docs/SECURITY.md`, `secretNameRE = regexp.MustCompile(\`^[A-Z_][A-Z0-9_]*\`)` (length 1–64 enforced separately), and the unexported `listEntry` struct (data-model.md §1.2) with `json:"name"` / `json:"description"` tags
- [X] T007 Implement `secretDeps` struct and `productionSecretDeps()` constructor in [internal/cli/secret.go](../../internal/cli/secret.go) per data-model.md §1.1 (loadVault, saveVault, promptPassphrase, promptSecret, promptLine, isStdinTTY, isStdoutTTY, deriveMasterSeed, readVaultSalt, kill, readPIDFile, stateDirRoot, logger, nowFn) — production wiring binds `golang.org/x/term.IsTerminal`, `term.ReadPassword`, `keys.DeriveMasterSeed`, `vault.Load`/`vault.Save`, `syscall.Kill`, `os.ReadFile`, `slog.Default()`, `time.Now`
- [X] T008 Implement `newSecretCmd() *cobra.Command` parent in [internal/cli/secret.go](../../internal/cli/secret.go) — `Use: "secret"`, no `RunE`, locked help text (no `--value`-class flag); attach the four child commands `newSecretAddCmd`, `newSecretRemoveCmd`, `newSecretListCmd`, `newSecretRotateCmd` (each is currently a stub returning `errors.New("not yet implemented")` — to be filled per user story)
- [X] T009 Edit [internal/cli/root.go](../../internal/cli/root.go) — inside `Execute`, add `root.AddCommand(newSecretCmd())` next to the existing `init`/`request`/`serve` mounts; no global state, no package `init()`
- [X] T010 Implement helper `validateSecretName(name string) error` in [internal/cli/secret.go](../../internal/cli/secret.go) — runs the regex AND length 1–64 check; returns `errInvalidSecretName` (which wraps `errMissingFlag` so `mapErr` classifies it as `ExitInputErr`); locked stderr message per contracts/cli-secret.md §3.2: `hush: secret: NAME must match ^[A-Z_][A-Z0-9_]*$ (1–64 chars)` (note the `–` is en-dash U+2013)
- [X] T011 Implement helper `enforceStdinTTY(in *os.File, deps *secretDeps, logger *slog.Logger, verb string) error` in [internal/cli/secret.go](../../internal/cli/secret.go) — calls `deps.isStdinTTY(in)`; on false emits the contract-locked stderr message `hush: secret: this command requires an interactive TTY (rogue-process defence)` (contracts/cli-secret.md §3.1), emits the `secret_tty_refused` slog WARN record (contracts/cli-secret.md §7.2), and returns the existing `errNoTTY` sentinel from `internal/cli` so `mapErr` classifies it as `ExitInputErr`
- [X] T012 Wire `mapErr` arms for the four verb-internal sentinels (`errors.Is(err, errInvalidSecretName)` → `ExitInputErr` via the `errMissingFlag` wrap chain; `errors.Is(err, errSecretValueMismatch)` and `errors.Is(err, errConfirmationMismatch)` → `ExitInputErr` via `errPassphraseMismatch`; `errors.Is(err, errSecretExists)` → `ExitErr` catch-all). Verify by `errors.Is` round-trip; do NOT edit [internal/cli/exit_codes.go](../../internal/cli/exit_codes.go) — wraps go through the existing classifier (data-model.md §2)

**Checkpoint**: `hush secret`, `hush secret add`, `hush secret remove`, `hush secret list`, `hush secret rotate` all parse and exit cleanly; T004 and T005 turn green; user-story work can now begin.

---

## Phase 3: User Story 1 — Add a secret to the vault (Priority: P1) 🎯 MVP

**Story Goal**: The vault owner adds a new named secret at the trusted host's terminal. `hush secret add NAME` prompts for passphrase, secret value (twice), and an optional description, then atomically persists the entry. Refuses piped stdin (rogue-process defence) and any value-bearing flag. Refuses duplicate names.

**Independent Test**: From a fresh `hush init server`, run `hush secret add FOO` at a real terminal, supply a value at the hidden prompt + matching confirmation, then assert (a) `hush secret list` reports `FOO`, (b) the on-disk vault file mode is `0600`, and (c) `echo bar | hush secret add BAR` returns exit code 2 with the rogue-process-defence stderr message.

### Tests for User Story 1 (TDD: write first, ensure red)

- [X] T013 [US1] Write `TestSecret_AddRefusesPipedStdin` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — `os.Stdin = pipe`; runs the cobra `add` command with arg `NAME`; asserts (a) returned error wraps `errNoTTY` → `ExitInputErr` (2), (b) stderr is byte-equal `hush: secret: this command requires an interactive TTY (rogue-process defence)\n`, (c) the on-disk vault bytes are unchanged (compare pre/post hashes), (d) the slog handler captured a `secret_tty_refused` WARN record with `verb=add` (contracts/cli-secret.md §3.1, §7.2; FR-002, SC-001)
- [X] T014 [US1] Write `TestSecret_AddRefusesValueFlag` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — runs `hush secret add --value foo NAME`; asserts cobra rejects with an "unknown flag" error (the substring `unknown flag: --value` appears in stderr); also runs the `--secret`/`--password` variants → `TestSecret_AddRefusesSecretFlag`, `TestSecret_AddRefusesPasswordFlag` (each with the same shape) (FR-003, FR-016, SC-007, contracts/cli-secret.md §1)
- [X] T015 [US1] Write `TestSecret_AddInvalidName` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — drives the pty fixture with stdin = TTY; arg `foo` (lowercase fails the regex); asserts (a) returned error wraps `errInvalidSecretName` → `ExitInputErr`, (b) stderr is byte-equal contracts/cli-secret.md §3.2, (c) the vault file is NOT opened (use a vault loader stub that fails the test if invoked), (d) NO slog record is emitted (FR-015 — routine input-validation refusals not audited)
- [X] T016 [US1] Write `TestSecret_AddTTYHappyPath` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — uses the pty fixture; scripted stdin: passphrase `\n`, value `\n`, confirm-value (same) `\n`, description `\n`; arg `ANTHROPIC_API_KEY`; asserts (a) `vault.Save` was invoked with a `[]vault.Secret` whose entries include `ANTHROPIC_API_KEY` with the typed description, (b) the `*SecureBytes` value bytes match the typed value, (c) on-disk file mode is `0600` (FR-009; contracts/cli-secret.md §3 prompt labels)
- [X] T017 [US1] Write `TestSecret_AddConfirmationMismatch` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — pty fixture; passphrase ok, value `secret123`, confirm `secret124`; asserts (a) `errSecretValueMismatch` → `ExitInputErr`, (b) stderr is byte-equal contracts/cli-secret.md §3.3, (c) `vault.Save` was NOT invoked (FR-004)
- [X] T018 [US1] Write `TestSecret_AddDuplicateRefuses` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — pre-populate the test vault with `EXISTING_KEY` whose value is the sentinel (`SECRET_SHOULD_NEVER_APPEAR_17`); pty fixture; arg `EXISTING_KEY`; asserts (a) `errSecretExists` → `ExitErr` (1), (b) stderr is byte-equal contracts/cli-secret.md §3.4 (`hush: secret: entry EXISTING_KEY already exists; use 'hush secret rotate' to replace`), (c) the sentinel does NOT appear anywhere in stderr (FR-005, FR-013), (d) on-disk vault bytes are unchanged
- [X] T019 [US1] Write `TestSecret_AddPassphraseFailureSurfacesAuthCode` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — pty fixture; vault loader stub returns `vault.ErrAuthFailed`; asserts `ExitAuth` (3) and a `secret_passphrase_failed` slog WARN record (contracts/cli-secret.md §7.2)

**At this point T013–T019 should all FAIL (red).**

### Implementation for User Story 1

- [X] T020 [US1] Implement `runSecretAdd(ctx, deps *secretDeps, args []string) error` in [internal/cli/secret.go](../../internal/cli/secret.go) per plan.md §"add flow" — order: stdin-TTY gate (via T011 helper) → name validation (T010) → passphrase prompt → derive vault key → load vault → secret-value prompt (no echo) → confirm-value prompt → byte-equal compare via `secureBytesEqual` (existing helper in `init.go`) → description line read → existence check → append → `vault.Save` → audit log INFO `secret_added` (contracts/cli-secret.md §7.1) → ExitOK; deferred LIFO `Destroy()` chain per data-model.md §4
- [X] T021 [US1] Wire `newSecretAddCmd() *cobra.Command` in [internal/cli/secret.go](../../internal/cli/secret.go) — `Use: "add NAME"`, `Args: cobra.ExactArgs(1)`, NO flags declared (structural FR-003 absence), `RunE` calls `runSecretAdd(cmd.Context(), productionSecretDeps(), args)`; help text omits any `--value`-class flag (so T004 passes)

**Verify T013–T019 turn green and T004/T005 stay green.**

**Checkpoint**: User Story 1 fully functional and testable independently. The MVP increment ships here — operators can populate the vault.

---

## Phase 4: User Story 2 — List vault entries without disclosing values (Priority: P1)

**Story Goal**: `hush secret list` enumerates entries (name + description only) with TTY/pipe-aware rendering. Never prints values. Empty-vault is not an error. Sorted ascending by name. Stdin-TTY required (passphrase prompt) but stdout-pipe is supported (`hush secret list | jq`).

**Independent Test**: With a populated vault including the leak sentinel as one entry's value, run `hush secret list` (TTY) and `hush secret list | cat` (pipe); assert the sentinel never appears in stdout or stderr; assert text format on TTY and JSON shape on pipe; assert ascending sort; assert empty vault produces stderr `(vault is empty)` on TTY and stdout `[]\n` on pipe.

### Tests for User Story 2 (TDD: write first, ensure red)

- [X] T022 [US2] Write `TestSecret_ListRefusesPipedStdin` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — stdin=pipe, stdout=anything; asserts the universal TTY-gate refusal applies even though stdout is being piped (FR-002, contracts/cli-secret.md §3.1) — confirms list is NOT exempt from the rogue-process defence
- [X] T023 [US2] Write `TestSecret_ListNoValues` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — pre-populate vault with three entries, one of which has value = sentinel `SECRET_SHOULD_NEVER_APPEAR_17`; run list in BOTH rendering modes (stdin=TTY+stdout=TTY; stdin=TTY+stdout=pipe); assert via `testutil.AssertSentinelAbsent` that the sentinel is absent from stdout AND stderr in both modes (SC-002, FR-007, FR-013)
- [X] T024 [US2] Write `TestSecret_ListJSONOutput` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — stdin=TTY, stdout=pipe; vault contains entries `FOO` (description `thing one`) and `GITHUB_TOKEN` (empty description); asserts stdout is byte-equal `[{"name":"FOO","description":"thing one"},{"name":"GITHUB_TOKEN","description":""}]\n` (contracts/cli-secret.md §5.2); asserts no other keys appear in any object (locked field set)
- [X] T025 [US2] Write `TestSecret_ListTTYOutput` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — stdin=TTY, stdout=TTY; same fixtures as T024; asserts stdout is byte-equal `FOO — thing one\nGITHUB_TOKEN\n` (em-dash U+2014; empty-description entry has no separator) per contracts/cli-secret.md §5.1
- [X] T026 [US2] Write `TestSecret_ListSortedAscending` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — pre-populate vault with names in random order (`ZULU`, `ALPHA`, `MIKE`); run list in both modes; assert output order is `ALPHA`, `MIKE`, `ZULU` (FR-008)
- [X] T027 [US2] Write `TestSecret_ListEmptyVault` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — empty vault; both modes; asserts (a) TTY mode → stdout empty AND stderr is byte-equal `(vault is empty)\n` (contracts/cli-secret.md §3.7), (b) pipe mode → stdout is byte-equal `[]\n` AND stderr is empty, (c) ExitOK in both (SC-001 — "empty result with successful exit")

**At this point T022–T027 should all FAIL (red).**

### Implementation for User Story 2

- [X] T028 [US2] Implement `runSecretList(ctx, stdout, stderr io.Writer, deps *secretDeps) error` in [internal/cli/secret.go](../../internal/cli/secret.go) per plan.md §"list flow" — order: stdin-TTY gate → passphrase prompt → derive vault key → load vault → for each `name` in `store.Names()` call `store.Get(name)` to obtain the `*SecureBytes` handle (we DISCARD the value — only Description is read), `Destroy()` it immediately, append `listEntry{Name, Description}` → `sort.Slice` ascending by Name → choose render via `deps.isStdoutTTY(stdout.(*os.File))`: TTY → loop emitting `%s — %s\n` (or `%s\n` when description empty); empty vault → stderr `(vault is empty)\n`. Pipe → `json.NewEncoder(stdout).Encode(entries)`. NO audit log on success (contracts/cli-secret.md §7.1 — `list` is read-only)
- [X] T029 [US2] Wire `newSecretListCmd() *cobra.Command` in [internal/cli/secret.go](../../internal/cli/secret.go) — `Use: "list"`, `Args: cobra.NoArgs`, no flags, `RunE` calls `runSecretList`; help text omits any value-class flag

**Verify T022–T027 turn green; sentinel-leak property is now provable in CI.**

**Checkpoint**: User Story 2 fully functional independently of US1's add path (works against a vault populated by `testutil.NewTestVault`). MVP + observation surface complete.

---

## Phase 5: User Story 3 — Rotate the vault and notify a live server (Priority: P1)

**Story Goal**: `hush secret rotate` re-encrypts the vault file with a fresh nonce + salt (preserving the plaintext entry set) and signals the running server via SIGHUP if a PID file at `<state_dir>/hush.pid` exists. Tolerates missing/stale/foreign PID with a warning + `ExitOK`.

**Independent Test**: With a populated vault and a fork-helper that writes its PID to `<state_dir>/hush.pid` and waits for SIGHUP, run `hush secret rotate` at a TTY; assert (a) the vault ciphertext bytes change, (b) the entry set is identical, (c) the helper exits 0 within 2s. Repeat with no PID file; assert ExitOK + WARN stderr.

### Tests for User Story 3 (TDD: write first, ensure red)

- [X] T030 [US3] Write `TestSecret_RotateRefusesPipedStdin` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — stdin=pipe → universal refusal (FR-002, contracts/cli-secret.md §3.1)
- [X] T031 [US3] Write `TestSecret_RotateAtomic` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — pre-populate vault with three entries; capture pre-rotation ciphertext bytes (full file contents); run `rotate` via pty fixture; assert (a) post-rotation ciphertext differs from pre-rotation (`bytes.Equal == false`) — fresh nonce/salt per FR-009, SC-003 — (b) post-rotation `vault.Load` returns the SAME set of `(Name, Description, Value)` tuples as pre-rotation (sort + compare), (c) on-disk file mode remains `0600`, (d) audit log INFO `vault_rotated` with `outcome=success` was emitted
- [X] T032 [US3] Write `TestSecret_RotateSendsSIGHUP` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — fork a child helper (build the test binary itself with a `TestSecretSIGHUPHelper` entrypoint guarded by `os.Getenv`) that (i) writes its own PID to `<state_dir>/hush.pid`, (ii) installs `signal.Notify` on `SIGHUP`, (iii) signals readiness via a sync file, (iv) waits for SIGHUP then exits 0; the parent test waits for the readiness file, runs `rotate` via pty fixture, asserts the child's `Wait()` returns `nil` within 2s, AND asserts the contract-locked stderr INFO line `hush: secret: signalled running server (pid=<int>)\n` (contracts/cli-secret.md §3.6) AND audit log INFO `vault_rotated` with `signalled=true`. (Use the `secretDeps.kill` recorder if a real fork is too heavy on the test runner; the chunk contract requires real-fork SIGHUP delivery as the canonical proof.)
- [X] T033 [US3] Write `TestSecret_RotateMissingPIDTolerant` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — no PID file exists at `<state_dir>/hush.pid`; pty fixture; asserts (a) ExitOK (NOT an error), (b) stderr WARN line is byte-equal `hush: secret: no running server signalled (no PID file)\n` (contracts/cli-secret.md §3.6 `pidAbsent` row), (c) the vault file IS still rewritten with new ciphertext (FR-011, SC-005), (d) `deps.kill` was never invoked
- [X] T034 [US3] Write `TestSecret_RotateStalePIDTolerant` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — write `<state_dir>/hush.pid` containing a PID that the test helper has already reaped (or a deliberately unallocated PID via `deps.kill` stub returning `syscall.ESRCH`); asserts (a) ExitOK, (b) stderr WARN byte-equal `hush: secret: no running server signalled (stale PID file)\n` (contracts/cli-secret.md §3.6 `pidStale` row), (c) the vault file IS still rewritten
- [X] T035 [US3] Write `TestSecret_RotateUnreadablePIDTolerant` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — write `<state_dir>/hush.pid` containing garbage (`not-a-number`); asserts ExitOK and stderr WARN byte-equal `hush: secret: no running server signalled (PID file unreadable)\n` (contracts/cli-secret.md §3.6 `pidUnreadable` row); vault still rewritten

**At this point T030–T035 should all FAIL (red).**

### Implementation for User Story 3

- [X] T036 [US3] Implement helper `probePIDFile(deps *secretDeps, path string) (pidStatus, int)` in [internal/cli/secret.go](../../internal/cli/secret.go) — reads via `deps.readPIDFile`; on error → `pidAbsent` (if `errors.Is(err, fs.ErrNotExist)`) or `pidUnreadable`; on parse failure → `pidUnreadable`; on parse success calls `deps.kill(pid, 0)`: nil → `pidPresent`; `errors.Is(err, syscall.ESRCH)` → `pidStale`; `errors.Is(err, syscall.EPERM)` → `pidNotOurUser`; other → `pidStale` (research.md R4)
- [X] T037 [US3] Implement `runSecretRotate(ctx, stderr io.Writer, deps *secretDeps) error` in [internal/cli/secret.go](../../internal/cli/secret.go) per plan.md §"rotate flow" — order: stdin-TTY gate → passphrase → load vault → build `[]vault.Secret` from `store.Names()` + `store.Get(name)` → `vault.Save` (SDD-03 mints fresh nonce + salt) → `Destroy()` each value SecureBytes (LIFO) → `probePIDFile` dispatch on `pidStatus`: `pidPresent` → `deps.kill(pid, syscall.SIGHUP)` + stderr INFO line (contracts/cli-secret.md §3.6) + `signalled=true` field; other branches → stderr WARN line per the `pidStatus`→message table (data-model.md §1.3) + `signalled=false`; audit log INFO `vault_rotated` with `outcome=success` and `signalled=<bool>` → ExitOK in EVERY branch (FR-011, SC-005)
- [X] T038 [US3] Wire `newSecretRotateCmd() *cobra.Command` in [internal/cli/secret.go](../../internal/cli/secret.go) — `Use: "rotate"`, `Args: cobra.NoArgs`, no flags, `RunE` calls `runSecretRotate`

**Verify T030–T035 turn green; SIGHUP delivery proven by real-fork test.**

**Checkpoint**: User Story 3 fully functional independently. AC-2 (vault round-trip — write half) provable end-to-end with US1's add path.

---

## Phase 6: User Story 4 — Remove an entry (Priority: P2)

**Story Goal**: `hush secret remove NAME` deletes a named entry after the operator types the name as a confirmation token. Refuses piped stdin, missing entry, and mismatched confirmation.

**Independent Test**: With a vault containing `FOO` and `BAR`, run `hush secret remove FOO` at a TTY, type `FOO` at the confirmation prompt; assert `FOO` is gone from `hush secret list` and `BAR` remains. Run `hush secret remove FOO` and type `foo` (lowercase) at confirmation; assert `ExitInputErr` and the vault is unchanged.

### Tests for User Story 4 (TDD: write first, ensure red)

- [X] T039 [US4] Write `TestSecret_RemoveRefusesPipedStdin` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — stdin=pipe → universal refusal (FR-002)
- [X] T040 [US4] Write `TestSecret_RemoveAtomic` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — pre-populate vault with `FOO` and `BAR`; pty fixture: passphrase, then `FOO\n` at the confirmation prompt; asserts (a) `vault.Save` invoked with a `[]vault.Secret` that contains `BAR` but NOT `FOO`, (b) on-disk file mode `0600`, (c) post-`remove` `vault.Load` returns exactly `{BAR}`, (d) no temp file from atomic rename lingers in `<state_dir>`, (e) audit log INFO `secret_removed` with `verb=remove`, `name=FOO`, `outcome=success` (contracts/cli-secret.md §7.1)
- [X] T041 [US4] Write `TestSecret_RemoveAbsent` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — pty fixture; arg `NOPE`; asserts (a) `vault.ErrSecretNotFound` → `ExitNotFound` (4) via `mapErr`, (b) the confirmation prompt is NOT fired (not-found check runs BEFORE confirmation per data-model.md §3.2), (c) vault file unchanged
- [X] T042 [US4] Write `TestSecret_RemoveTokenMismatch` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — pre-populate with `FOO`; pty fixture: passphrase ok, confirmation token typed as `foo` (lowercase); asserts (a) `errConfirmationMismatch` → `ExitInputErr`, (b) stderr is byte-equal contracts/cli-secret.md §3.5 (`hush: secret: typed name does not match the entry argument\n`), (c) `vault.Save` was NOT invoked, (d) audit log WARN `secret_confirmation_mismatch` with `verb=remove`, `name=FOO`, `outcome=confirmation_mismatch` (contracts/cli-secret.md §7.2)

**At this point T039–T042 should all FAIL (red).**

### Implementation for User Story 4

- [X] T043 [US4] Implement `runSecretRemove(ctx, stderr io.Writer, deps *secretDeps, args []string) error` in [internal/cli/secret.go](../../internal/cli/secret.go) per plan.md §"remove flow" — order: stdin-TTY gate → name validation → passphrase → load vault → existence check (`store.Get(name)` returning `vault.ErrSecretNotFound` short-circuits BEFORE the confirmation prompt) → echoing line read for the confirmation token → byte-equal compare against `args[0]` (mismatch → `errConfirmationMismatch` + WARN slog record per contracts/cli-secret.md §7.2) → filter the `[]vault.Secret` slice → `vault.Save` → audit INFO `secret_removed` → ExitOK; LIFO `Destroy()` chain
- [X] T044 [US4] Wire `newSecretRemoveCmd() *cobra.Command` in [internal/cli/secret.go](../../internal/cli/secret.go) — `Use: "remove NAME"`, `Args: cobra.ExactArgs(1)`, no flags, `RunE` calls `runSecretRemove`

**Verify T039–T042 turn green.**

**Checkpoint**: User Story 4 functional. All four verbs are now implemented; all behaviour-contract tests are green.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Cross-cutting invariants (leak scans, audit log, file mode, help text), gate runs, coverage threshold, and the darwin+linux smoke validation called out in SDD-17 §"Final message".

### Cross-cutting tests

- [X] T045 [P] Write `TestSecret_AuditLogOmitsSecretBytes` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — installs a capturing slog handler; runs the happy-path of all four verbs against a vault whose values include the sentinel `SECRET_SHOULD_NEVER_APPEAR_17`; asserts the captured handler output (across `add`/`remove`/`list`/`rotate`) does NOT contain the sentinel (FR-013, contracts/cli-secret.md §7)
- [X] T046 [P] Write `TestSecret_ErrorsDoNotLeakSecretBytes` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — drives every documented failure path (TTY refusal, invalid name, confirmation mismatch on `add`, duplicate `add`, not-found on `remove`, token mismatch on `remove`, stale PID on `rotate`, unreadable PID on `rotate`) against a vault populated with the sentinel; asserts `error.Error()` AND captured stderr scanned for the sentinel returns zero hits
- [X] T047 [P] Write `TestSecret_FileModeAfterAdd` and `TestSecret_FileModeAfterRotate` in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — post-`add` and post-`rotate`, `os.Stat(vaultPath).Mode().Perm()` MUST equal `0600` (FR-009, FR-012; SDD-03's enforcement re-verified at the CLI seam)

### Gates and coverage

- [X] T048 Run `magex format:fix` from repo root — auto-format Go files; assert `git diff --exit-code` is clean afterwards (the format pass should idempotently produce no diff on a freshly-implemented chunk)
- [X] T049 Run `magex lint` from repo root — golangci-lint must pass clean; in particular confirm `forbidigo` does NOT flag any `os.Getenv` for passphrase/value (research.md R10) and `errcheck` is clean on every defer-`Destroy` chain
- [X] T050 Run `magex test:race` from repo root — race-clean run of the full suite. The SIGHUP fork-helper test (T032) is the most likely race surface; resolve any data race surfaced
- [X] T051 Run `go test -cover ./internal/cli/ -run Secret` from repo root — coverage on the secret-portion test set MUST be `≥ 85%` per SDD-17. If below, add targeted tests in [internal/cli/secret_test.go](../../internal/cli/secret_test.go) (likely candidates: the `pidNotOurUser` branch, the `errInvalidSecretName` length-=64 boundary, the empty-vault TTY hint stderr stream)

### Cross-platform smoke (SDD-17 §"Final message")

- [X] T052 [P] Validate piped-stdin refusal on **darwin**: from a darwin host, build the binary, run `echo foo | ./hush secret add NAME`; assert exit code is `2` (`ExitInputErr`) and stderr is byte-equal `hush: secret: this command requires an interactive TTY (rogue-process defence)\n`. Repeat with `remove`, `list`, `rotate`. Capture the four exit codes and stderr lines as evidence
- [X] T053 [P] Validate piped-stdin refusal on **linux**: from a linux host (or via Docker `golang:1.26.1`), repeat T052 for all four verbs; capture evidence. (SDD-17 explicitly calls out darwin AND linux; both must succeed before `done`)
- [X] T054 Integration smoke for SIGHUP delivery: build the binary, fork the test stub child described in T032 OUTSIDE the unit-test harness, run the real `./hush secret rotate`, assert the child receives SIGHUP and exits 0 within 2s. (Optional automation; required if T032 used the `kill` recorder rather than a real fork)

### Documentation updates (deferred to the IMPLEMENT phase per SDD-17 prompt 5)

- [X] T055 [P] Append "Exported API — locked at SDD-17" subsection to [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) under `internal/cli/` noting the four-verb subcommand surface (`hush secret {add,remove,list,rotate}`) and that NO new exported package-level symbols were added
- [X] T056 [P] Update AC-1 and AC-2 rows in [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) to reference `internal/cli/secret_test.go` (write half of vault round-trip) — add the test names per the chunk-contract list
- [X] T057 [P] Mark SDD-17 status `done` in [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md)

### Final commit (single, combined; per SDD-17 prompt 5)

- [X] T058 Stage and commit per SDD-17 §"Final message": `git add internal/cli/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md specs/017-cli-secret/tasks.md` then `git commit -m "feat(cli): hush secret add/remove/list/rotate (TTY-enforced) (SDD-17)"` — verify gates passed (T048–T051), race-clean (T050), coverage ≥ 85% (T051), darwin+linux refusal proven (T052/T053), SIGHUP delivery verified (T032/T054), AC-1+AC-2 updated (T056), PACKAGE-MAP updated (T055), SDD-PLAYBOOK updated (T057)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — can start immediately
- **Phase 2 (Foundational)**: Depends on Phase 1 — BLOCKS all user stories (the cobra mount, `secretDeps` struct, TTY gate, name validator, and sentinel wraps are universally required)
- **Phase 3 (US1 Add)**: Depends on Phase 2
- **Phase 4 (US2 List)**: Depends on Phase 2 (independent of US1's add path; tests use `testutil.NewTestVault` to pre-populate)
- **Phase 5 (US3 Rotate)**: Depends on Phase 2 (independent of US1; tests use `testutil.NewTestVault`)
- **Phase 6 (US4 Remove)**: Depends on Phase 2 (independent of US1; tests use `testutil.NewTestVault`)
- **Phase 7 (Polish)**: Depends on Phases 3–6 (cross-cutting tests scan all four verbs' happy-paths)

### User Story Dependencies

US1, US2, US3, and US4 are **logically independent**: each test phase pre-populates its own vault via `testutil.NewTestVault` rather than chaining onto another verb. Implementation phases ALL converge on the single file `internal/cli/secret.go`, so within-file work is necessarily serialized; the [P] mark below identifies tasks that could land in parallel if the team accepts overlapping edits to one file.

### Within Each User Story

- TDD-strict: every behaviour-contract test (T013–T019, T022–T027, T030–T035, T039–T042) MUST be written and FAILING (red) before its implementation task runs
- Implementation order within a verb: helper(s) → `runSecret<Verb>` → cobra wiring → verify all named tests for the verb turn green
- Story complete before moving to next verb is NOT required — different developers MAY interleave verbs in parallel sessions, with the caveat that all edits target the same two files

### Parallel Opportunities

- T001–T003 (Setup) can run in parallel — different files (testutil/, init_helpers_test.go)
- T004 and T005 (Foundational tests) edit the same file (`secret_test.go`) but are independent test functions; can be merged from separate developers if a merge tool tolerates it. **Not strictly [P]** because both touch the same file.
- T045–T047 (cross-cutting tests) and T055–T057 (docs) are file-disjoint; can run [P]
- T052–T053 (darwin + linux smoke) are different host environments; trivially [P]
- Within a single user story, ALL test tasks edit `secret_test.go` and ALL implementation tasks edit `secret.go`; thus within-story parallel work is bounded by file ownership, not by [P] markers.

---

## Parallel Example: Setup phase

```bash
# Launch all setup-phase tasks together (different files):
Task: "T001 Add SentinelSecret17 in internal/testutil/sentinels.go"
Task: "T002 Confirm/extend NewTestVault in internal/testutil/vault.go"
Task: "T003 Confirm pty fixture helper in internal/cli/init_helpers_test.go"
```

## Parallel Example: Polish phase

```bash
# Cross-cutting tests, all editing different test functions but same file —
# can be drafted in parallel even though they are merged sequentially:
Task: "T045 Write TestSecret_AuditLogOmitsSecretBytes"
Task: "T046 Write TestSecret_ErrorsDoNotLeakSecretBytes"
Task: "T047 Write TestSecret_FileModeAfterAdd / TestSecret_FileModeAfterRotate"

# darwin+linux smoke + docs updates (truly disjoint files):
Task: "T052 Validate piped-stdin refusal on darwin"
Task: "T053 Validate piped-stdin refusal on linux"
Task: "T055 Update docs/PACKAGE-MAP.md"
Task: "T056 Update docs/AC-MATRIX.md"
Task: "T057 Mark SDD-17 done in docs/SDD-PLAYBOOK.md"
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Phase 1: Setup (sentinel + testutil scaffolding)
2. Phase 2: Foundational (cobra mount, deps, TTY gate, name validator)
3. Phase 3: US1 Add (tests T013–T019 → impl T020–T021)
4. **STOP and VALIDATE**: `hush secret add` works at a real TTY; rogue-process defence proven by T013; help text clean by T004
5. Demo: operator can populate the vault. AC-1 (server CLI surface) progressing.

### Incremental delivery

1. MVP (Phase 3) — operators populate the vault.
2. + Phase 4 (List) — operators inspect the vault. SC-002 (no values in output) provable in CI.
3. + Phase 5 (Rotate) — operators re-encrypt. SC-003 + SC-005 provable. AC-2 (vault round-trip — write half) provable end-to-end.
4. + Phase 6 (Remove) — operators prune. Full FR-001 verb set complete.
5. + Phase 7 (Polish) — all gates, coverage threshold, darwin+linux smoke, single combined commit.

### Parallel team strategy

With multiple developers and a willingness to merge sibling edits to `secret.go`/`secret_test.go`:

1. Team completes Phase 1 + Phase 2 together (one developer; foundational mount blocks everyone).
2. Once Phase 2 is done:
   - Developer A: US1 (Add) tests + impl
   - Developer B: US2 (List) tests + impl
   - Developer C: US3 (Rotate) tests + impl
   - Developer D: US4 (Remove) tests + impl
3. Stories integrate via merges to `secret.go`/`secret_test.go`; conflict surface is small because each verb is a distinct `runSecret<Verb>` function and a distinct `newSecret<Verb>Cmd` constructor.
4. Polish phase (Phase 7) is a single developer's responsibility (gate runs, coverage, smoke, commit).

---

## Notes

- TDD per Constitution VIII: every named test in SDD-17's "Tests required" list AND every contract-locked test (contracts/cli-secret.md §9) is written BEFORE the corresponding implementation task. The phase ordering enforces this.
- Coverage target 85% on the secret portion of `internal/cli` is enforced by T051. The contract-locked test set (33 named tests across the four verbs + cross-cutting + foundational) substantially exceeds the surface needed to clear 85%; T051 is a guardrail, not the test plan.
- The two-file convergence (`secret.go`, `secret_test.go`) is the dominant constraint on intra-chunk parallelism. The `secretDeps` seam (data-model.md §1.1) is the primary device for keeping verb implementations independently testable.
- darwin AND linux smoke (T052, T053) is mandated by SDD-17 §"Final message" and is the gate before marking the chunk `done`. Both must produce byte-equal stderr and exit code `2` for all four verbs under piped-stdin.
- The single combined commit at T058 is mandated by SDD-17 §"How to run this chunk" — no intermediate commits between phases.
- SIGHUP delivery (T032) is the only test in the chunk that forks a real child process. The sub-process model is gated by an `os.Getenv` re-entry pattern so the test binary doubles as the helper executable; no separate build target required.
- The `errSecretExists` exit-code (`ExitErr`/1, NOT `ExitInputErr`/2) is deliberate — see plan.md "Project Structure" § for the rationale. The operator-facing message (T020 + T018) is the contractual signal, not the exit code.
- NO edits to [internal/cli/exit_codes.go](../../internal/cli/exit_codes.go). All four new sentinels route through `mapErr` via `errors.Is` against existing classifiers.
