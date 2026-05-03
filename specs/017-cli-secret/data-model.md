# Data Model — SDD-17 `hush secret`

This chunk introduces NO new exported package-level symbols. Every
type below is unexported and lives inside
[internal/cli/secret.go](../../internal/cli/secret.go) (and its
test file).

---

## 1. Internal types

### 1.1 `secretDeps`

Testable seam for every verb. Production wiring comes from a single
`productionSecretDeps()` helper analogous to `productionInitDeps()`.

```go
type secretDeps struct {
    // Vault load + save. Tests substitute deterministic stubs that
    // back onto an in-memory map.
    loadVault func(ctx context.Context, path string, key *securebytes.SecureBytes) (vault.Store, error)
    saveVault func(ctx context.Context, path string, key *securebytes.SecureBytes, secrets []vault.Secret) error

    // Passphrase prompt — no echo via term.ReadPassword. Tests
    // substitute a deterministic reader.
    promptPassphrase func(in *os.File, prompt io.Writer, label string) (*securebytes.SecureBytes, error)

    // Secret value prompt — same no-echo path as the passphrase
    // prompt; separate seam so tests can return distinct values for
    // the two prompts (passphrase vs value).
    promptSecret func(in *os.File, prompt io.Writer, label string) (*securebytes.SecureBytes, error)

    // Echoing line prompt — used for the optional description field
    // and the typed-name confirmation token on `remove`.
    promptLine func(in *os.File, prompt io.Writer, label string) (string, error)

    // TTY detectors — separate seams for stdin and stdout so a test
    // can simulate `stdin=TTY, stdout=pipe` (the `hush secret list | jq`
    // case) without a real pty.
    isStdinTTY  func(*os.File) bool
    isStdoutTTY func(*os.File) bool

    // Argon2id master-seed derivation. Tests substitute a fast stub
    // that still validates the passphrase length contract.
    deriveMasterSeed func(ctx context.Context, passphrase, salt []byte) ([]byte, error)

    // Vault salt reader. Production: reads the 16-byte salt from the
    // vault file header. Tests substitute a deterministic value.
    readVaultSalt func(path string) ([]byte, error)

    // Signal sender. Production: syscall.Kill. Tests substitute a
    // recorder that captures (pid, signal) tuples without forking.
    kill func(pid int, sig syscall.Signal) error

    // PID-file reader. Production: os.ReadFile. Tests substitute a
    // map-backed reader.
    readPIDFile func(path string) ([]byte, error)

    // State-dir override. Empty string in production (resolved via
    // LoadServer); tests pass a t.TempDir() path.
    stateDirRoot string

    // Logger — slog.Default() in production; tests substitute a
    // capturing handler to assert on emitted records.
    logger *slog.Logger

    // Now — time.Now in production; tests freeze.
    nowFn func() time.Time
}
```

**Invariants**:
- Every field has exactly one production binding.
- Tests construct a `secretDeps` directly; no global state.

### 1.2 `listEntry`

The exact JSON wire shape emitted by `hush secret list` when
stdout is not a TTY. Field order matters — `encoding/json`
preserves struct declaration order in its output.

```go
type listEntry struct {
    Name        string `json:"name"`
    Description string `json:"description"`
}
```

**Invariants**:
- Exactly two fields. No `value`. No additional metadata.
- Both fields use string-typed values; an empty description marshals
  to `""`, never omitted.
- A slice of `listEntry` is sorted ascending by `Name` using
  `sort.Strings`-equivalent semantics (lexicographic byte order)
  before rendering, so the output is stable across runs over an
  unchanged vault (FR-008).

### 1.3 `pidStatus`

A small enum that drives the rotate stderr message.

```go
type pidStatus uint8

const (
    pidPresent pidStatus = iota // PID file exists, parses, owned by us
    pidAbsent                   // no PID file at <state_dir>/hush.pid
    pidStale                    // PID file present but no live process at that PID
    pidNotOurUser               // PID file present, process exists, but we cannot signal it (EPERM)
    pidUnreadable               // PID file present but unreadable / unparseable
)
```

The `rotate` flow's branch on `pidStatus`:

| Status | stderr WARN message | exit |
|--------|---------------------|------|
| `pidPresent` | (no warn — INFO line: `hush: secret: signalled running server (pid=%d)`) | 0 |
| `pidAbsent` | `hush: secret: no running server signalled (no PID file)` | 0 |
| `pidStale` | `hush: secret: no running server signalled (stale PID file)` | 0 |
| `pidNotOurUser` | `hush: secret: no running server signalled (PID owned by another user)` | 0 |
| `pidUnreadable` | `hush: secret: no running server signalled (PID file unreadable)` | 0 |

In every branch the file rewrite has already succeeded; the warn-and-continue
posture is mandated by FR-011 / SC-005.

---

## 2. Verb-internal sentinel errors

All four sentinels are unexported `var ... = errors.New(...)` declared
at file scope inside `secret.go`. They route through `mapErr` via
`errors.Is` against the existing input-error sentinels in
[internal/cli/exit_codes.go](../../internal/cli/exit_codes.go) — NO
edits to `exit_codes.go` are required.

```go
// errInvalidSecretName surfaces a name that fails ^[A-Z_][A-Z0-9_]*$ /
// length 1–64. Wraps errMissingFlag so mapErr classifies it as
// ExitInputErr without a separate switch arm.
var errInvalidSecretName = fmt.Errorf("hush: secret: invalid entry name: %w", errMissingFlag)

// errSecretValueMismatch surfaces an `add` confirmation prompt that
// did not match the first prompt. Wraps errPassphraseMismatch.
var errSecretValueMismatch = fmt.Errorf("hush: secret: value confirmation mismatch: %w", errPassphraseMismatch)

// errConfirmationMismatch surfaces a `remove` typed-name confirmation
// that did not match the NAME argument. Wraps errPassphraseMismatch.
var errConfirmationMismatch = fmt.Errorf("hush: secret: typed-name confirmation mismatch: %w", errPassphraseMismatch)

// errSecretExists surfaces an `add` for an entry name that already
// exists. Catch-all classification (ExitErr) so the operator-facing
// message — which directs them to `hush secret rotate` — is the
// signal, not the exit code.
var errSecretExists = errors.New("hush: secret: entry already exists; use 'hush secret rotate' to replace")
```

The `errInvalidSecretName` wrap of `errMissingFlag` is deliberate:
both are `ExitInputErr`-class operator-input errors. Reusing the
existing classifier keeps `mapErr` small.

---

## 3. State transitions

### 3.1 `add NAME`

```
tty-gate (stdin) ─┐
                  ├─ FAIL → errNoTTY → ExitInputErr
                  │
name-validate ────┐
                  ├─ FAIL → errInvalidSecretName → ExitInputErr
                  │
passphrase-prompt
  │
  └─ derive vault key
       │
       └─ load vault ──┐
                       ├─ FAIL (auth) → vault.ErrAuthFailed → ExitAuth
                       │
       value-prompt    │
            │          │
            ├─ confirm-prompt
            │     │
            │     └─ MISMATCH → errSecretValueMismatch → ExitInputErr
            │
       description-prompt (echoing; empty allowed)
            │
       exists-check ────┐
                        ├─ FAIL → errSecretExists → ExitErr
                        │
       append + save ───┐
                        ├─ FAIL → vault.ErrInvalidName / ErrDuplicateName → ExitInputErr
                        │
       audit log success
            │
       ExitOK
```

### 3.2 `remove NAME`

```
tty-gate (stdin) → name-validate → passphrase-prompt → load vault
                                                            │
                                                            ▼
                                            existence-check (vault.Store.Names)
                                                            │
                                          ABSENT → vault.ErrSecretNotFound → ExitNotFound
                                                            │
                                            confirmation-prompt (echoing)
                                                            │
                                                        compare token
                                                            │
                                          MISMATCH → errConfirmationMismatch → ExitInputErr
                                                            │
                                                        filter slice
                                                            │
                                                        save
                                                            │
                                                    audit log success
                                                            │
                                                          ExitOK
```

### 3.3 `list` (no positional argument)

```
tty-gate (stdin) → passphrase-prompt → load vault → enumerate names
                                                            │
                                                For each name:
                                                  - store.Get(name) → SecureBytes
                                                  - append listEntry{Name, Description}
                                                  - SecureBytes.Destroy()        ◀── value bytes never reach the renderer
                                                            │
                                                  sort entries by Name
                                                            │
                                            ┌──────────── stdout TTY? ──────────┐
                                            ▼                                    ▼
                                  TTY: write `NAME — description\n`     PIPE: json.NewEncoder(stdout).Encode(entries)
                                  per entry; empty vault →               (empty vault → `[]\n`)
                                  stderr `(vault is empty)\n`
                                            │
                                          ExitOK
```

### 3.4 `rotate`

```
tty-gate (stdin) → passphrase-prompt → load vault
                                              │
                                  build []vault.Secret from store
                                              │
                                              save (fresh nonce + salt)
                                              │
                                  destroy each value SecureBytes
                                              │
                                read <state_dir>/hush.pid
                                              │
                                ┌──── pidStatus dispatch ────┐
                                ▼                             ▼
                          pidPresent                       pidAbsent / pidStale /
                          syscall.Kill(pid, SIGHUP)        pidNotOurUser / pidUnreadable
                                │                             │
                          INFO "signalled running server"   WARN "no running server signalled (...)"
                                │                             │
                                └──────── ExitOK ────────────┘
```

In every rotate branch, the vault file has already been rewritten
before the PID-file branch is entered — so a kill failure (or
absence) cannot leave the operator's expectation of "vault rotated"
unmet.

---

## 4. Memory hygiene

| Source | Container | Lifetime |
|--------|-----------|----------|
| Passphrase typed at TTY | `*securebytes.SecureBytes` returned by `promptPassphrase` | `Destroy()` in verb-level defer (LIFO last) |
| Argon2id master seed | local `[]byte`, immediately wrapped after derivation | zeroed in defer alongside the vault key |
| AES-256-GCM vault key | `*securebytes.SecureBytes` wrapping the raw 32-byte derive output | `Destroy()` in verb-level defer |
| Secret value typed at TTY (`add`) | `*securebytes.SecureBytes` returned by `promptSecret` | passed into `vault.Save` via the `vault.Secret.Value` field; no separate Destroy needed (Save does not retain the reference per SDD-03) |
| Confirmation value (`add`) | `*securebytes.SecureBytes` returned by a second `promptSecret` call | `Destroy()` immediately after the byte-equal compare |
| Loaded secrets returned by `store.Get` (`list`) | `*securebytes.SecureBytes` per call | `Destroy()` immediately after extracting the (unused) handle reference; values are never read |
| Loaded secrets passed into `vault.Save` (`rotate`, `remove`) | `*securebytes.SecureBytes` per `vault.Secret` | `Destroy()` after `Save` returns |

The defer chain is LIFO, so the last thing to run is the
passphrase Destroy. This matches the pattern locked in
`init.go::runInitServer`.

---

## 5. Wire / file shapes

This chunk introduces no new wire formats and no new file formats.
Everything reuses:

- `<state_dir>/secrets.vault` — SDD-03 format.
- `<state_dir>/hush.pid` — text file containing a single ASCII
  decimal integer with optional trailing whitespace. Read by
  `rotate`; written by a future SDD chunk (not this one).

The `list` JSON output is the only consumable format introduced
here; its shape is locked in
[contracts/cli-secret.md](./contracts/cli-secret.md) §3.
