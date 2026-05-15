# Implementation Plan: hush secret — vault-entry management (TTY-only writes; SIGHUP reload)

**Branch**: `017-cli-secret` | **Date**: 2026-05-03 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/017-cli-secret/spec.md`
**Chunk contract**: [docs/sdd/SDD-17.md](../../docs/sdd/SDD-17.md)

## Summary

`hush secret` is the operator-facing vault-management subcommand. It mounts on
the SDD-14 cobra root via `root.AddCommand(newSecretCmd())` in `Execute`
([internal/cli/root.go](../../internal/cli/root.go)) and exposes four verbs —
`add <NAME>`, `remove <NAME>`, `list`, `rotate`. It adds **no new exported
package-level symbols** to `internal/cli`; the cobra command tree IS the
contract for this chunk.

Two new files plus their tests:

- [internal/cli/secret.go](../../internal/cli/secret.go) — verb wiring,
  TTY enforcement, prompt orchestration, vault load/save plumbing,
  PID-file SIGHUP delivery.
- [internal/cli/secret_test.go](../../internal/cli/secret_test.go) — unit
  tests for every behaviour contract listed in SDD-17 (TTY refusal,
  no-value-flag invariant, list-no-values, rotate atomic + SIGHUP +
  missing-PID-tolerant, add-confirmation mismatch, remove-confirmation
  token, list TTY/JSON rendering).

Behavioural shape (locked):

1. **TTY-first refusal** — every verb (including `list`) checks
   `term.IsTerminal(int(os.Stdin.Fd()))` before any vault I/O, any
   keychain read, any flag interpretation beyond `cobra` parse. A non-TTY
   stdin returns the `errNoTTY` sentinel mapped to `ExitInputErr` with
   the locked stderr message:
   `hush: secret: this command requires an interactive TTY (rogue-process defence)`
   (SDD-17 contract phrase: "this command requires an interactive TTY
   (rogue-process defence)"). The check is FIRST so a piped stdin can
   never steal the passphrase prompt or silently inject a value.
2. **Value-flag refusal is structural, not runtime** — there is NO
   `--value`, `--secret`, or `--password` flag declared on any verb's
   `pflag.FlagSet`. `cobra` will reject any such flag with an
   "unknown flag" parse error before our code runs. Help text omits
   them entirely. (FR-003, FR-016, SC-007.)
3. **Name validation** — `add` and `remove` validate the positional
   `NAME` against `^[A-Z_][A-Z0-9_]*$` with length 1–64 BEFORE opening
   the vault file (FR-017). A failing name returns `errInvalidSecretName`
   → `ExitInputErr` with the message `hush: secret: NAME must match ^[A-Z_][A-Z0-9_]*$ (1–64 chars)`.
4. **Passphrase resolution (TTY-only)** — every verb reuses the existing
   `readPassphraseTTY` helper from
   [internal/cli/init.go](../../internal/cli/init.go) (the same
   no-echo `term.ReadPassword` path used by `init server`). The serve
   subcommand's `resolvePassphrase` path (which falls back to a stdin
   pipe) is NOT used here — the universal stdin-TTY gate (FR-002) makes
   the pipe branch unreachable. The salt comes from the vault file
   header via `readVaultSalt` (already in `internal/cli/serve.go`); the
   master seed comes from `keys.DeriveMasterSeed`; the AES-GCM vault
   key comes from `keys.DeriveVaultEncKey`. A wrong passphrase surfaces
   `vault.ErrAuthFailed` → `ExitAuth` (existing `mapErr` mapping).
5. **`add` flow** — TTY gate → name validation → passphrase prompt →
   secret value prompt (`term.ReadPassword`, no echo, label
   `Secret value: `) → confirm-value prompt (label
   `Confirm secret value: `) → byte-equal check (mismatch →
   `errPassphraseMismatch`-style sentinel
   `errSecretValueMismatch` → `ExitInputErr`) → optional description
   prompt (echoing line read via `readLineFromTTY`, label
   `Description (optional): `) → load existing vault → reject if name
   already exists (`errSecretExists` → `ExitErr` with message
   `hush: secret: entry %s already exists; use 'hush secret rotate' to replace`)
   → append new `vault.Secret` → `vault.Save` (atomic per SDD-03) →
   audit log success → `ExitOK`. The newly read secret bytes flow only
   through `*securebytes.SecureBytes`; the input `[]byte` returned by
   `term.ReadPassword` is zeroed in the same line that wraps it
   (matches the helper's existing pattern in `init.go`).
6. **`remove` flow** — TTY gate → name validation → passphrase prompt →
   load vault → existence check (absent → `vault.ErrSecretNotFound` →
   `ExitNotFound` via existing `mapErr`) → confirmation token prompt
   (label `Type the entry name to confirm: `, echoing line via
   `readLineFromTTY`) → byte-equal compare against `NAME` argument
   (mismatch → `errConfirmationMismatch` → `ExitInputErr`) → filter
   the `[]vault.Secret` slice → `vault.Save` → audit log success
   → `ExitOK`.
7. **`list` flow** — TTY gate (stdin, FR-002) → passphrase prompt →
   load vault → enumerate via `store.Names()` → for each name
   `store.Get(name)` to assemble the `[]listEntry{Name,Description}`
   pairs → IMMEDIATELY `Destroy()` each returned `*SecureBytes` (we
   only need the description metadata, not the value; the plan exits
   here without having ever read the value bytes) → sort by name
   ascending (Go `sort.Strings` semantics, FR-008) → choose render
   based on `term.IsTerminal(int(os.Stdout.Fd()))`:
   - **stdout-TTY**: human-readable lines `NAME — description` (em-dash
     `—` U+2014; entries with empty description render `NAME` only).
     Empty vault → message `(vault is empty)`.
   - **stdout-pipe**: `encoding/json.NewEncoder(stdout).Encode(entries)`
     → JSON array of `{"name":"…","description":"…"}` objects, one
     element per entry, no other keys. Empty vault → `[]\n`.

   The renderer NEVER touches `Secret.Value`. A sentinel-leak test
   asserts the chosen sentinel string never appears in stdout or
   stderr in either rendering mode.
8. **`rotate` flow** — TTY gate → passphrase prompt → load vault →
   build `[]vault.Secret` from the loaded store (preserving names,
   descriptions, and `*SecureBytes` value handles obtained via
   `store.Get`) → `vault.Save` (SDD-03 mints a fresh nonce + salt on
   every save, so the on-disk ciphertext bytes change without any
   plaintext modification) → check for PID file at
   `<state_dir>/hush.pid`:
   - **PID file present, parses to int, process owned and signal-able**:
     `syscall.Kill(pid, syscall.SIGHUP)`. Success → audit log + stderr
     INFO `hush: secret: signalled running server (pid=%d)` →
     `ExitOK`.
   - **PID file absent**, **stale** (`syscall.Kill(pid, 0)` returns
     `os.ErrProcessDone` or `ESRCH`), or **not signal-able**
     (`EPERM` — different user): warn-and-continue. Stderr WARN
     `hush: secret: no running server signalled (%s)` where the
     parenthetical describes the cause: `no PID file`, `stale PID file`,
     or `PID owned by another user` → `ExitOK`. The vault file IS
     rewritten in every case (FR-011, SC-005).

   In every rotate branch the SecureBytes value handles obtained from
   `store.Get` are `Destroy()`-ed in a `defer` chain after `vault.Save`
   returns. (`store.Destroy()` zeroes the store's internal handles; the
   ones we copied into the `[]vault.Secret` slice for save must be
   Destroy-ed independently.)
9. **Audit-log discipline** — every successful `add`, `remove`, and
   `rotate` emits a slog INFO record carrying `verb`, `name` (where
   applicable), and `outcome=success`. Every security-relevant failure
   (TTY refusal, passphrase failure, confirmation mismatch on
   `remove`) emits a WARN record carrying `verb`, `name` (where
   applicable), and `failure=tty_refused|passphrase_failed|confirmation_mismatch`.
   Routine input-validation refusals (malformed name per FR-017,
   missing positional argument, unknown flag) are NOT logged
   (FR-015). NO log line ever carries the secret value, the
   confirmation token, the passphrase, or the PID-file path's
   contents beyond the parsed integer.

   This chunk uses `slog.Default()` — the project-wide audit-chain
   writer (`internal/audit`) is server-side only and not in scope for
   CLI verbs. Future SDD chunks may route these events to a dedicated
   audit channel; that is not this chunk's concern.

10. **Defer / cleanup chain** (LIFO) for every verb: `Destroy()` every
    secret `*SecureBytes` we obtained → `Destroy()` the vault key
    `*SecureBytes` → `Destroy()` the passphrase `*SecureBytes` → the
    cobra `RunE` returns. Cobra's caller (`Execute`) maps the returned
    error via `mapErr`.

The above flow is the same single-host, single-operator model
described in the spec's "Assumptions" section. Multi-writer
coordination across hosts is explicitly out of scope.

## Technical Context

**Language/Version**: Go 1.26.1 (module `github.com/mrz1836/hush`,
`go.mod` declares `go 1.26.1`). No language-version bump.

**Primary Dependencies** (all already direct — no new direct deps):
- `github.com/spf13/cobra` (existing) — verb + flag wiring.
- `golang.org/x/term` (existing) — `IsTerminal` on stdin AND stdout;
  `ReadPassword` for the secret value and passphrase prompts.
- `github.com/mrz1836/hush/internal/keys` (SDD-01) — `DeriveMasterSeed`
  + `DeriveVaultEncKey`.
- `github.com/mrz1836/hush/internal/vault` (SDD-03) — `Load`, `Save`,
  `Secret`, `Store`, `ErrSecretNotFound`, `ErrAuthFailed`,
  `ErrDuplicateName`.
- `github.com/mrz1836/hush/internal/vault/securebytes` (SDD-02) —
  in-memory secret + passphrase containers.
- `github.com/mrz1836/hush/internal/config` (SDD-06) — `LoadServer` to
  resolve `Server.StateDir` (vault path = `<state_dir>/secrets.vault`).
- Standard library: `context`, `encoding/json`, `errors`, `fmt`, `io`,
  `log/slog`, `os`, `path/filepath`, `regexp`, `sort`, `strconv`,
  `syscall`.

**Storage**: read/write to `<state_dir>/secrets.vault` via
`vault.Load` + `vault.Save`. Optional read of `<state_dir>/hush.pid`
during `rotate` (single `os.ReadFile`, parsed as integer, never
trusted as a string in any log line). No new files are created by
this chunk.

**Testing**: `go test -race`; integration tests gated by
`//go:build integration` (this chunk does not introduce one — every
test runs as a unit-test under the default build tag using
`testutil.NewTestVault` for a real on-disk vault inside `t.TempDir()`).
The TTY-path tests use `creack/pty` via the existing helper pattern in
`init_helpers_test.go::TestReadPassphraseTTY_ViaPTY`. The non-TTY
refusal tests run with `os.Stdin = os.NewFile(0, "")` against a pipe
fixture. SIGHUP delivery is verified by a child-process test stub:
the unit test `fork`s a tiny helper that installs `signal.Notify` on
`SIGHUP`, writes its own PID into `<state_dir>/hush.pid`, then waits
for the signal and exits 0; the test asserts `exec.Cmd.Wait()`
returns `nil` within 2s. (No real `hush serve` is started.)

**Target Platform**: macOS (darwin) AND linux. The TTY logic, vault
codec, and `syscall.Kill(pid, SIGHUP)` are all POSIX-portable. No
darwin-only paths in this chunk. (SDD-17 §"Final message" calls out
darwin AND linux validation explicitly.)

**Project Type**: CLI subcommand on top of the SDD-14 cobra root.

**Performance Goals**:
- Latency floor for `add`, `remove`, `rotate`: bounded by the
  Argon2id derivation cost (`time=4, memory=256MB, threads=4` — the
  Constitution-locked params) which is ~1-2s on the trusted host's
  CPU. This is intentional — it is the same cost paid by `hush init`
  and `hush serve`.
- `list` is bounded by the same Argon2 cost plus an O(n) walk over
  the vault entries. n ≤ a few hundred in any realistic operator
  vault.
- `rotate` is bounded by `2 ×` Argon2 (decrypt + re-encrypt) plus an
  O(n) re-marshal. The SIGHUP delivery itself is sub-millisecond.

**Constraints**:
- **No secret value ever leaves a `*SecureBytes` container** except
  inside the `Use(fn)` callback that hands the bytes to
  `vault.Save`. The renderer for `list` deliberately calls
  `Destroy()` on each value handle BEFORE rendering, so the value
  bytes are unreachable when JSON marshalling runs.
- **No flag carries a secret value** — structural absence enforced by
  cobra's "unknown flag" rejection plus the `TestSecret_AddRefusesValueFlag`
  unit test that runs `hush secret add --value foo NAME` and asserts
  exit code 2 with the cobra "unknown flag" error.
- **No env-var fallback for passphrase or value** — the TTY gate at
  step 1 makes both impossible. A lint-time check via the existing
  `forbidigo` `os.Getenv` rule covers this repo-wide.
- **PID-file path is ALWAYS `<state_dir>/hush.pid`** — never `pid`,
  never `server.pid`, never `~/.hush/server.pid`. The constant lives
  next to the existing `vaultFilename = "secrets.vault"` constant in
  `internal/server/server.go` only as a future cross-reference; this
  chunk inlines the literal `"hush.pid"` in `secret.go` next to its
  own `pidFilename` constant. SDD-17 deliberately does NOT add a
  "writes the PID file" responsibility to `serve`. That is a future
  chunk's job; until then `rotate` will exercise the
  missing-PID-file path in production, which is a legal and
  documented outcome (FR-011, SC-005).
- **The vault file mode stays `0600`** — `vault.Save` already
  enforces this on the post-rename file (SDD-03 `permissions.go`);
  no additional chmod call here.

**Scale/Scope**: one operator on one trusted host. Multi-writer
coordination is out of scope; the spec accepts last-writer-wins.

## Constitution Check

*Gates evaluated against
[`/Users/mrz/projects/hush/.specify/memory/constitution.md`](../../.specify/memory/constitution.md)
v1.1.0. Re-checked after Phase 1 design. No violations identified — no
Complexity Tracking entries required.*

### Principle VII — CLI Design Standards
- **Compliance**:
  - Subcommand follows the noun-verb pattern: `hush secret <verb>`.
  - Mounted on the SDD-14 cobra root via package-side-effect
    (`root.AddCommand(newSecretCmd())` in `Execute`); reuses the
    locked global flags (`--config/-c`, `--verbose/-v`, `--quiet/-q`,
    `--no-color`).
  - Reuses the locked `Exit*` constants
    ([internal/cli/exit_codes.go](../../internal/cli/exit_codes.go)):
    `ExitOK`, `ExitInputErr`, `ExitAuth`, `ExitNotFound`. No new
    exit-code constants.
  - `list` honours the project-wide TTY/pipe convention:
    text on a TTY, JSON on a pipe (FR-008).
  - No `--value`-class flag exists; `--format eval` is unrelated and
    not introduced here (it belongs to `request`, SDD-16).
- **Test guards**:
  - `TestSecret_AddRefusesValueFlag` — `--value` rejected by cobra
    with an "unknown flag" error and `ExitErr`/`ExitInputErr`.
  - `TestSecret_ListJSONOutput` — stdout-pipe → byte-exact JSON.
  - `TestSecret_ListTTYOutput` — stdout-TTY → human lines.
  - `TestSecret_HelpDoesNotMentionValueFlags` — `--help` text scanned
    for `--value`, `--secret`, `--password`; all absent (SC-007).

### Principle X — Observability & Redaction
- **Compliance**:
  - All secret bytes flow through `*securebytes.SecureBytes`; the
    type's `LogValue()` returns `[redacted]` so any accidental
    `slog.Any("secret", sb)` would still redact.
  - Errors carry sentinel + identifier only — secret values, the
    confirmation token, and the passphrase NEVER appear in error
    strings, slog records, or stderr text. The `add` "already exists"
    error uses the entry name only, never the existing value
    (FR-005, FR-013).
  - `list` is the highest-risk path — its renderer is provably
    value-free because the `*SecureBytes` handle is `Destroy()`-ed
    before the renderer runs.
- **Test guards**:
  - `TestSecret_ListNoValues` — populated vault including the
    sentinel `SECRET_SHOULD_NEVER_APPEAR_17`; both rendering modes
    are exercised; stdout AND stderr are scanned for the sentinel
    and assert it does NOT appear (SC-002).
  - `TestSecret_ErrorsDoNotLeakSecretBytes` — every documented failure
    path is exercised; the resulting `error.Error()` plus captured
    stderr text is scanned for the sentinel; no hits.
  - `TestSecret_AuditLogOmitsSecretBytes` — captures the slog handler
    output across the full happy-path of all four verbs and asserts
    the sentinel never appears.

### Security Requirements (Constitution §"Security Requirements")
- **Encrypted at rest**: `vault.Save` (SDD-03) is the only writer;
  Argon2id + AES-256-GCM is locked there.
- **Memory protection**: every value flows through `*SecureBytes`
  (mlock + zero-on-destroy).
- **Input validation**: name regex + length applied BEFORE any vault
  I/O (FR-017); confirmation-token byte-equal compare on `remove`;
  byte-equal compare on the second value prompt for `add`.
- **No hardcoded secrets**: passphrase comes from the TTY only.
- **Secure defaults**: TTY enforcement is on by default, fail-closed.
  No flag relaxes it. The "rogue process runs hush secret add" threat
  row in [docs/SECURITY.md](../../docs/SECURITY.md) is the documented
  defence; a comment in `secret.go` cites the threat row by name.
- **File permissions**: vault file mode `0600` (SDD-03 already
  enforces); rotate inherits this from `vault.Save`.

### Other principles (not in scope, but verified non-regressing)

- **Principle I — Zero Files at Rest on Agent Machines** — out of
  scope. `hush secret` runs on the trusted vault host only, where the
  vault file is the canonical encrypted artifact.
- **Principle II — Approval is Human, Approval is Phone** — out of
  scope. `hush secret` is local management; no Discord round-trip.
- **Principle III — Defense in Depth Through Crypto Layering** — uses
  the locked Argon2id + AES-256-GCM stack via `vault.Save`. No new
  layer; no existing layer weakened.
- **Principle IV — Supervisor for Daemons, Wrap-Shell for Humans** —
  not applicable; no JWT issuance, no child process.
- **Principle V — Staleness is Visible, Failure is Loud** — applies
  weakly to `rotate`: a missing PID file produces a STDERR WARN line
  (visible to the operator), not silence. Failure to deliver SIGHUP
  to a known PID is loud (warns and exits 0 because the rewrite
  succeeded; the operator can re-issue or restart the server).
- **Principle VI — Tailscale-Only, Never Public** — not applicable;
  no network surface.
- **Principle VIII — Testing Discipline** — coverage target 85% on
  the new file (matches the chunk contract). Every behaviour
  contract has a named test (see chunk-contract §"Tests required"
  list). No fuzz target added — no parser entry point in this chunk.
- **Principle IX — Idiomatic Go Discipline** —
  - `context.Context` first parameter on every helper that does I/O
    or accepts cancellation.
  - Sentinel errors are exported only when they map to a `mapErr`
    classification used elsewhere; verb-internal sentinels remain
    unexported (`errConfirmationMismatch`, `errSecretValueMismatch`,
    `errSecretExists`, `errInvalidSecretName`).
  - No globals; no `init()`; no goroutines spawned; no panics.
  - Subcommand registration is done in `Execute` (already locked at
    SDD-14), not in a package `init()`.
- **Principle XI — Native-First, Minimal Dependencies, Ephemeral
  Vault** — no new direct dependencies; reuses the locked crypto
  stack and standard library only.

**Result**: ✅ Constitution Check passes. Re-evaluated post-Phase-1 below.

## Project Structure

### Documentation (this feature)

```text
specs/017-cli-secret/
├── plan.md              # this file
├── spec.md              # /speckit-specify output (read-only here)
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/
│   └── cli-secret.md    # Phase 1 output — locked CLI contract for `hush secret`
└── tasks.md             # Phase 2 output (created by /speckit-tasks; not by /speckit-plan)
```

### Source Code (repository root)

```text
internal/cli/
├── secret.go               # NEW — verb wiring, TTY enforcement, vault load/save,
│                           #       PID-file SIGHUP delivery
├── secret_test.go          # NEW — unit tests for every behaviour contract
├── root.go                 # EDITED — root.AddCommand(newSecretCmd()) inside Execute
└── (no other production files modified)
```

`internal/cli/exit_codes.go` is NOT modified. The four verb-internal
sentinels — `errConfirmationMismatch`, `errSecretValueMismatch`,
`errSecretExists`, `errInvalidSecretName` — live inside `secret.go`
as unexported `var ... = errors.New(...)` declarations and route
through `mapErr` via `errors.Is` against existing input-error
sentinels:

- `errInvalidSecretName` → wraps `errMissingFlag` (already
  classified as `ExitInputErr`).
- `errSecretValueMismatch` → wraps `errPassphraseMismatch` (already
  `ExitInputErr`).
- `errConfirmationMismatch` → wraps `errPassphraseMismatch`.
- `errSecretExists` → no wrap; classified as `ExitErr` (catch-all).
  This is a deliberate choice: an "already exists" condition is
  state-of-the-vault, not operator-input-form, so the catch-all
  applies. The operator-facing message is locked
  (`hush: secret: entry %s already exists; use 'hush secret rotate' to replace`)
  and steers the operator to the correct verb.

If the post-Phase-1 review surfaces a need to make `errSecretExists`
its own classifier, that is a one-line edit to `mapErr` in a follow-up
chunk; the contract here does not depend on the exit code distinction
(the message + name argument are the operator-facing signal).

No new exported symbols are added to `internal/cli` (only the
subcommand registration on the cobra tree, per the chunk contract).
[docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) gets a new
"Exported API — locked at SDD-17" subsection under `internal/cli/`
in the IMPLEMENT phase noting the four-verb subcommand surface and
that no new symbols were added.

**Structure Decision**: The subcommand follows the existing
`internal/cli` pattern locked by SDD-14 / SDD-15 / SDD-16
(`serve.go`, `init.go`, `request.go`). Verb functions take
`(ctx, stdout, stderr, in *os.File, deps *secretDeps, ...)` so tests
can inject deterministic seams (TTY-detector, prompt sources, vault
loader/saver, `syscall.Kill` stand-in, hostname / state-dir override)
per the same `*Deps` pattern used by
[internal/cli/init.go](../../internal/cli/init.go) (`initDeps`) and
[internal/cli/request.go](../../internal/cli/request.go)
(`requestDeps`).

## Phase 0 — Outline & Research

Output: [research.md](./research.md). Key resolved questions:

1. **Why is the TTY gate ALSO required for `list`?** Because every
   verb (including `list`) requires the vault passphrase, and the
   passphrase MUST come from the controlling terminal — never a pipe.
   A piped stdin would either steal the prompt's input or silently
   bypass authentication. The stdin gate is a strict precondition;
   the stdout gate (TTY-vs-pipe) is independent and only governs
   the rendering format. The supported invocation
   `hush secret list | jq` works because stdin is a TTY (passphrase
   prompt allowed) and stdout is a pipe (JSON rendering selected).
2. **What controls the rendering format for `list`?**
   `term.IsTerminal(int(os.Stdout.Fd()))`. This is the same
   convention used by every other CLI subcommand in this repo (see
   `internal/cli/output.go::detectTTY`).
3. **Where is the PID file?** `<state_dir>/hush.pid`. The literal
   `"hush.pid"` is inlined as a `pidFilename` constant in
   `secret.go` next to a comment pointing at SDD-17 §"Implementation
   contract". `serve` does NOT write this file in the current chunk;
   that is a future SDD's job. Until then `rotate` always exercises
   the missing-PID branch in production — which is a legal,
   documented outcome (FR-011, SC-005).
4. **What is the rendering separator on the TTY path?** The em-dash
   `—` (U+2014), surrounded by single ASCII spaces. Format string:
   `%s — %s\n` (or `%s\n` when Description is empty). Tests assert
   the byte sequence.
5. **What is the JSON wire shape on the pipe path?** A JSON array of
   objects with exactly two keys, `name` and `description`, in that
   declaration order. `encoding/json` will preserve the struct's
   field order. Empty description is rendered as `""` (NOT
   omitted). An empty vault renders as `[]\n`.
6. **What is the rotate "no content change" semantic?** SDD-03's
   `vault.Save` mints a fresh nonce + salt every call, so calling
   `Save` with the same `[]vault.Secret` slice that came out of
   `Load` produces a byte-different ciphertext while preserving the
   plaintext set. This is the only "rotate" semantic in scope for
   this chunk — there is no key rotation here, only ciphertext-only
   rotation. (Passphrase change is a separate, future SDD chunk.)
7. **How is the PID-file owner check done without root?**
   `syscall.Kill(pid, 0)` returns `EPERM` when the PID is owned by a
   different user. The rotate code treats `EPERM` AND `ESRCH` AND
   "PID file not present" as the same branch: warn and continue with
   `ExitOK`. (`os.FindProcess` always succeeds on POSIX so it is not
   a useful liveness probe; the syscall.Kill(0) probe is the
   canonical idiom.)
8. **Which slog logger?** `slog.Default()`. Audit-chain integration
   (the SDD-13 `internal/audit` package) is server-side only and not
   in scope here. The structured fields are: `verb` (string), `name`
   (string, omitted for `list`), `outcome`
   (`success`|`tty_refused`|`passphrase_failed`|`confirmation_mismatch`).

## Phase 1 — Design & Contracts

### Data model — see [data-model.md](./data-model.md)

Core types (none exported beyond the package):

- `secretDeps` — testable seams (vault loader, vault saver,
  passphrase resolver, value prompter, line prompter, stdin TTY
  detector, stdout TTY detector, signal sender, state-dir override,
  now func, slog handle).
- `listEntry` — `{Name, Description string}` — the exact shape of
  each element in the JSON-pipe output. Field tags are
  `json:"name"`, `json:"description"`.
- `pidStatus` — small enum for `rotate`: `pidPresent`, `pidAbsent`,
  `pidStale`, `pidNotOurUser`. Each maps to a stderr warn message.

State transitions per verb:

- **add**: tty-gate → name-validate → passphrase-prompt →
  value-prompt → confirm-value-prompt → load → already-exists-check
  → append → save → audit → ok.
- **remove**: tty-gate → name-validate → passphrase-prompt → load →
  not-found-check → confirmation-prompt → confirmation-compare →
  filter → save → audit → ok.
- **list**: tty-gate (stdin) → passphrase-prompt → load → enumerate
  → destroy-values → sort → render → ok.
- **rotate**: tty-gate → passphrase-prompt → load → re-save → check
  pid file → kill-or-warn → ok.

### Contracts — see [contracts/cli-secret.md](./contracts/cli-secret.md)

Locks:

- The exact verb set (`add`, `remove`, `list`, `rotate`) and their
  argument shapes (`add NAME`, `remove NAME`, `list` (no args),
  `rotate` (no args)).
- The exact TTY-refusal stderr message:
  `hush: secret: this command requires an interactive TTY (rogue-process defence)`
  (byte-equal asserted).
- The exact name-validation regex (`^[A-Z_][A-Z0-9_]*$`) and length
  bounds (1, 64).
- The exact prompt labels (`Vault passphrase: `, `Secret value: `,
  `Confirm secret value: `, `Description (optional): `,
  `Type the entry name to confirm: `).
- The exact list rendering format (em-dash separator on TTY; JSON
  array on pipe with locked field set).
- The exact PID file path component (`hush.pid`) and rotate-warning
  shapes.
- Exit-code mapping for every error class.

### Quickstart — see [quickstart.md](./quickstart.md)

Operator-facing TL;DR:

```bash
# Add a secret (must be at a real terminal)
hush secret add ANTHROPIC_API_KEY
# prompts: Vault passphrase, Secret value, Confirm secret value,
#          Description (optional)

# List entries (text on TTY, JSON on pipe)
hush secret list                # NAME — description
hush secret list | jq           # [{"name":"…","description":"…"}, …]

# Remove an entry (typed-name confirmation)
hush secret remove ANTHROPIC_API_KEY
# prompts: Vault passphrase, Type the entry name to confirm

# Rotate the vault file (re-encrypts; signals server if PID file present)
hush secret rotate
# prompts: Vault passphrase
```

### Agent context update

Update the `<!-- SPECKIT START -->`…`<!-- SPECKIT END -->` block in
`CLAUDE.md` (project root) to reference this plan file:
`specs/017-cli-secret/plan.md`. The CLAUDE.md edit is the only file
this plan touches outside `specs/017-cli-secret/`.

## Re-evaluation: Constitution Check (post-Phase 1)

Re-checked after writing data-model + contracts + quickstart. The
following are unchanged:

- **Principle VII** — exit codes, output destinations, and the
  TTY/pipe rendering split for `list` are locked in
  `contracts/cli-secret.md`. No `--value`-class flag declared on any
  verb.
- **Principle X** — `*SecureBytes` is the only secret container;
  logs and errors carry identifiers only; the `list` renderer
  destroys value handles before rendering.
- **Security Requirements** — TTY enforcement is universal across
  every verb (FR-002); name validation runs before vault I/O;
  vault file mode `0600` inherited from SDD-03; rotate's SIGHUP path
  is owner-checked and tolerates absence/staleness; the
  rogue-process threat row is cited inline in `secret.go`.

No new violations surfaced. No Complexity Tracking entries needed.

## Complexity Tracking

> Not required — Constitution Check passed without violations.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none) | — | — |
