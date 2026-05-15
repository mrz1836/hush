# Phase 0 — Research: SDD-15 (`hush init` + Keychain)

This document resolves every NEEDS CLARIFICATION raised by the plan's
Technical Context, plus every consequential design choice that the SDD
contract leaves unstated. Each section follows the
**Decision / Rationale / Alternatives considered** template.

---

## §1 — Linux keychain backend: `github.com/zalando/go-keyring`

**Decision**: Use `github.com/zalando/go-keyring` as the Linux keychain
backend, added as a NEW direct dependency.

**Rationale (Constitution XI written justification)**:

- **Trusted-sources hierarchy** (`.github/tech-conventions/dependency-management.md`):
  - **stdlib**: no, Go stdlib has no Secret Service / D-Bus client.
  - **sigil baseline** (`github.com/mrz1836/sigil`): no, sigil does not
    expose a keychain API.
  - **bsv-blockchain organization**: not applicable.
  - **wider ecosystem**: yes — `zalando/go-keyring` is the de-facto Go
    binding to the freedesktop.org Secret Service.
- **Maintainer activity**: actively maintained by Zalando SE; tagged
  releases for the past several years; issues are triaged.
- **Supply-chain footprint**: only one transitive dependency
  (`github.com/godbus/godbus/v5`, the canonical Go D-Bus binding —
  Apache-2.0, used by docker/podman/snapd). Zero crypto code in the
  chain; the package is a thin wrapper around D-Bus method calls.
- **License**: MIT — compatible with the project's policy.
- **Alternatives evaluated**:
  1. **Hand-roll a D-Bus client.** Rejected: ≈ 800 LOC of bespoke
     protocol code, all of it security-relevant, with no upstream eyes
     and no benefit over the published wrapper.
  2. **Shell out to `secret-tool`.** Already used by `serve.go` for
     read-only retrieval and is fine for that one-shot, but `init`
     needs to **create** items with collision-detection semantics,
     which `secret-tool` reports through opaque exit codes that vary
     across libsecret versions. Programmatic D-Bus is more reliable.
  3. **`github.com/keybase/go-keychain`.** macOS-focused, Linux is a
     separate sub-module that wraps libsecret via cgo — incompatible
     with our `CGO_ENABLED=0` policy (Constitution IX).
  4. **`github.com/99designs/keyring`.** Higher-level abstraction
     supporting many backends (file, pass, kwallet); too broad — pulls
     ten transitive deps and is design-incompatible with the
     per-binary ACL story.

**Alternatives considered for the macOS backend**: the `security`
CLI is the locked mechanism per `docs/SECURITY.md` and SDD-15. No Go
library shells the per-binary `-T` ACL flag through to the underlying
`SecKeychainItemSetAccess` call without cgo, so shelling out is the
only `CGO_ENABLED=0` option.

**Operational note**: zalando/go-keyring on Linux talks to the running
session's Secret Service (typically `gnome-keyring-daemon` or
`kwalletd`). Headless servers and CI runners may not have a session
keychain. This is acceptable in v0.1.0 because Linux init refuses to
run today (see §2); when a per-binary ACL story exists for Linux, a
separate research pass will revisit headless support.

---

## §2 — Linux per-binary ACL gap → init refuses on Linux (FR-020a)

**Decision**: `hush init` returns a platform-incompatibility error and
exits non-zero on any non-Darwin platform. The Linux `keychain_linux.go`
implementation **exists** (so the binary compiles on Linux and so the
existing `serve.go` Linux path that retrieves an out-of-band-stored bot
token continues to work), but `init` itself never invokes `Keychain.Store`
on Linux today.

**Rationale**:

- The freedesktop.org Secret Service D-Bus API exposes per-collection
  ACLs (locked / unlocked) and per-item attributes, but no per-binary
  reader restriction. A process running as the same uid can read any
  unlocked collection's items.
- macOS Keychain's per-binary ACL (`SecAccessRef` with a bound
  `SecTrustedApplicationRef`) has no Linux Secret Service equivalent.
- Spec FR-020a, locked in the 2026-05-03 clarification round, mandates
  refuse-and-exit (no silent downgrade, no opt-in flag) on platforms
  without a per-binary mechanism.

**Implementation**:

```go
// internal/keychain/keychain.go
//
// PerBinaryACLSupported reports whether the platform impl honours the
// `acl` argument as a per-binary access restriction. macOS: true.
// Linux: false. Init must check this and refuse if false.
func PerBinaryACLSupported() bool { /* GOOS == "darwin" */ }
```

`init.go` then guards both the server and client subcommands:

```go
if !keychain.PerBinaryACLSupported() {
    return fmt.Errorf("%w: platform %q has no per-binary keychain ACL",
        errPlatformACLUnsupported, runtime.GOOS)
}
```

`errPlatformACLUnsupported` maps to `ExitErr` (1) — it is not an
operator-input error. The error message names the missing capability
("per-binary keychain ACL") and the platform name, satisfying FR-020a's
literal-text requirement.

**Alternatives considered**:

1. **Use `chmod 0600` on a `~/.hush/clients/N.bin` file as a "weaker
   restriction"** — silently downgrades the security guarantee.
   Forbidden by FR-020a's locked clarification answer.
2. **Add a `--i-know-this-is-not-secure` opt-in flag** — explicitly
   rejected by the same clarification.
3. **Implement an AppArmor/SELinux profile installer in `hush init`** —
   far out of scope; would require root privilege and distribution
   awareness.
4. **Wait until SDD-26+ to define a Linux ACL mechanism** — accepted;
   v0.1.0 ships macOS-only and refuses on Linux. The keychain interface
   surface is forward-compatible with a future Linux ACL mechanism.

---

## §3 — Public-key fingerprint format: `SHA256:<base64-no-padding>`

**Decision**: Add a new helper in `internal/cli/init.go` (NOT in
`internal/keys`) that returns
`"SHA256:" + base64.RawStdEncoding.EncodeToString(sha256.Sum256(SEC1Compressed(pub)))`.
The existing `keys.PublicKeyFingerprint` (16-char lowercase hex,
SDD-01-locked) is **NOT modified** — it remains the operator-facing
fingerprint inside the vault server's client registry, where its
SDD-01 contract is locked.

**Rationale**:

- Spec FR-017 + clarification answer locks the printed format to
  OpenSSH-style `SHA256:<base64-no-padding>` for **operator
  copy-paste workflow** — one line, terminal-friendly, recognisable
  to operators familiar with `ssh-keygen -lf`.
- The existing `keys.PublicKeyFingerprint` produces a 16-char hex
  truncation suitable for compact server-side identifiers; that
  contract is consumed by SDD-12 (`/claim` handler) and SDD-13
  (`/revoke` handler) and MUST NOT change.
- The two formats serve different purposes and live at different
  layers; duplicating the SHA-256 computation is acceptable
  (Constitution IX: prefer concrete, local code over premature
  abstraction).
- Encoding choice: `base64.RawStdEncoding` (RFC 4648 §3.2 — alphabet
  `[A-Za-z0-9+/]`, no padding). Matches OpenSSH's `ssh-keygen -E sha256`
  output exactly: `SHA256:<43-char-base64>`.

**Implementation**:

```go
// internal/cli/init.go

func sshStyleFingerprint(pub *ecdsa.PublicKey) string {
    compressed := sec1Compress(pub) // 33 bytes
    digest := sha256.Sum256(compressed)
    return "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:])
}
```

`sec1Compress` is a 10-line helper that produces the SEC1-compressed
encoding (parity byte + 32-byte X coordinate). The same compression is
already done inside `keys.PublicKeyFingerprint`; we reproduce it locally
to avoid coupling SDD-15 to a refactor of the SDD-01-locked surface.

**Alternatives considered**:

1. **Extend `internal/keys` with a second fingerprint function.**
   Rejected: SDD-01's exported API is locked; adding a function here
   technically does not break the contract but creates pressure to
   widen the surface every time a new format is needed.
2. **Print both fingerprints (hex + SHA256:base64).** Rejected: spec
   FR-017 says the line "MUST NOT contain a trailing space, decorative
   prefix, or any other text". Two fingerprints would violate the
   single-line invariant.
3. **Use `base64.URLEncoding` (RFC 4648 §5).** Rejected: OpenSSH
   convention is `StdEncoding` (with `+/`), not `URLEncoding` (with
   `-_`). Operators copy-paste from this output into the server's
   registered-clients list; matching the OpenSSH convention reduces
   surprise.

---

## §4 — Keychain item naming convention

**Decision**: Lock the following `(service, account)` pairs:

| Item | Service | Account |
|---|---|---|
| Discord bot token | `cfg.Discord.BotTokenKeychainItem` (default `"hush-discord"`) | `"hush-server"` |
| Vault passphrase | `"hush-vault-passphrase"` | `"hush-server"` |
| Per-machine client private key | `"hush-client"` | `fmt.Sprintf("machine-%d", machineIndex)` |

**Rationale**:

- The existing `internal/cli/serve.go::loadBotToken` retrieves the
  bot token by **service** alone (`security find-generic-password -s <item> -w`).
  To keep that retrieval contract intact, the bot-token entry must
  use the configured service name; the account is a constant we own
  (`"hush-server"`) so collision detection in init doesn't trip on a
  stray pre-existing item with a different account.
- Vault passphrase: `"hush-vault-passphrase"` is unambiguous and
  doesn't collide with the bot-token service name. It is read by
  `hush init` only — `hush serve` reads the passphrase from
  stdin/TTY (SDD-14), not from the keychain — so this entry exists
  for **operator convenience** (the operator can re-run `serve` with
  `security find-generic-password -s hush-vault-passphrase -w | hush serve`).
- Per-machine client key: the service `"hush-client"` is shared across
  machine indices, with the index encoded into the account. This makes
  it possible to enumerate enrolled machines on a host via
  `security dump-keychain` filtered by service name, and it keeps the
  account name human-readable (`machine-0`, `machine-3`, …).

**Collision detection**: server-mode init invokes
`Retrieve(ctx, service, account)` on each item before `Store`; if
`Retrieve` returns a non-`ErrKeychainItemNotFound` error AND a non-nil
value, init refuses with `errKeychainItemExists`. Same logic for
client-mode (FR-012 / Clarification Q1). Operator must `security
delete-generic-password -s <service> -a <account>` before re-running.

**Alternatives considered**:

1. **Use a `(service, account) = ("hush", "<role>-<n>")` flat scheme.**
   Rejected: would force `serve.go` to change its retrieval API to
   include an account, breaking the SDD-14 lock.
2. **Use `runtime.GOOS`-specific item names.** Rejected: pointless
   complexity; the per-binary ACL already separates Darwin from Linux
   semantics.

---

## §5 — Vault file existence guard (FR-012)

**Decision**: Server-mode init calls `os.Stat(<state_dir>/secrets.vault)`
before any KDF or keychain work. If the file exists, init returns
`errVaultExists` (mapped to `ExitErr`) with the conflicting absolute
path embedded in the message. If `os.Stat` returns `fs.ErrNotExist`,
init proceeds; any other stat error is wrapped and returned.

**Rationale**: FR-012 mandates refuse-and-exit. `vault.Save` itself
performs an atomic write (`<path>.tmp` → `rename`) which would
overwrite an existing file silently — we must catch this **before**
calling `vault.Save`.

**Alternatives considered**: extending `vault.Save` with a "create
new only" flag — rejected: SDD-03 has locked `vault.Save`; the guard
belongs in the caller.

---

## §6 — Cobra subcommand layout

**Decision**: `init` is a parent command with two subcommands —
`hush init server` and `hush init client`. Mode flags (e.g.
`--server` / `--client` on a single `hush init` command) are NOT
supported.

```text
hush init           → prints help, exits non-zero (no default mode)
hush init server    → server-mode bootstrap
hush init client --machine-index N
                    → client-mode bootstrap
```

**Rationale**:

- Constitution VII: noun-verb pattern (`hush <noun> <verb>`).
  `init` is the noun, `server`/`client` are the verbs. Subcommands
  match this convention better than mode flags.
- Mutual exclusivity (FR-018) is enforced **structurally** by the
  cobra command tree — two separate commands cannot be invoked in
  the same process. There is no flag combination that can produce
  the conflict, so `errModeConflict` becomes a defensive sentinel
  that fires only on a programming bug, not on operator input.
- The existing `cli.Execute` ordering (SDD-14) places subcommands at
  the second positional argument; `hush init server` slots in
  cleanly with no root-level changes beyond `root.AddCommand(newInitCmd())`.

**Alternatives considered**:

1. **`hush init --server` / `hush init --client --machine-index N`**.
   Rejected: forces runtime FR-018 enforcement (tedious cobra flag
   group code); harder to write `--help` text per mode; departs from
   the noun-verb pattern.
2. **`hush init` with an auto-detect heuristic** (e.g. "if a vault
   exists, this must be a client install"). Explicitly rejected by
   FR-013 / FR-014 — the operator must declare intent.

---

## §7 — TTY-only enforcement strategy

**Decision**: Init reads the passphrase, the passphrase confirmation,
and the Discord bot token via `golang.org/x/term.IsTerminal` +
`term.ReadPassword`. If `term.IsTerminal(int(os.Stdin.Fd()))` is
false, init returns `errNoTTY` (mapped to `ExitInputErr`) before any
prompt is written and before any artifact is touched.

**Rationale**: FR-005 + FR-005a + Clarification Q5 (2026-05-03) lock
init to TTY-only operation. Unlike `serve` (which permits a piped
stdin per SDD-14), `init` rejects pipes outright because the
multi-prompt sequence (passphrase → confirmation → bot-token) is
ill-suited to a single piped stream and the failure modes of a
truncated pipe are operator-confusing.

**Test strategy**: tests use `github.com/creack/pty` (already a
test-only dep from SDD-14) to construct a real PTY, write scripted
input, and read the no-echo prompts. The Darwin command-construction
tests use a `looker` seam (`func(name string) (string, error)`) that
records the resolved binary path AND a `runner` seam
(`func(*exec.Cmd) error`) that captures argv + stdin without
launching `/usr/bin/security`, so unit tests run on any host without
a populated keychain.

**Alternatives considered**:

1. **Allow piped stdin like `serve`.** Rejected by Clarification Q5.
2. **Use `os.Stdin.Stat() & os.ModeCharDevice` instead of
   `term.IsTerminal`.** Rejected: `term.IsTerminal` is the
   conventional Go check and matches what `serve.go` already uses; it
   handles the macOS "named pipe with terminal flags" edge case
   correctly.

---

## §8 — Passphrase confirmation (double entry)

**Decision**: Both modes prompt for the passphrase twice. If the two
entries do not match byte-for-byte, init returns `errPassphraseMismatch`
(mapped to `ExitInputErr`) with no retry loop; the operator must
re-run the command. Both `*securebytes.SecureBytes` are destroyed
before the error returns.

**Rationale**: FR-004 + Clarification Q4. A retry loop is rejected
because (a) the operator may have deliberately typed a different
passphrase the second time and a loop would mask intent, and (b) a
loop adds branches that the sentinel-leak tests must cover.

**Alternatives considered**: re-prompt on mismatch with a 3-attempt
cap. Rejected: simpler is better; the operator's second mismatch is
already evidence of the wrong key in their head, not a fat-finger.

---

## §9 — Atomic write of `config.toml` at mode 0600

**Decision**: Server-mode init writes `config.toml` via the same
atomic-write pattern used by `vault.Save`:

1. Generate the TOML body in memory using `pelletier/go-toml/v2`'s
   marshaller, with every field from `docs/CONFIG-SCHEMA.md`
   populated with its documented default. Operator-supplied values
   (`listen_addr`, `path_prefix`, `discord_owner_id`,
   `application_id`) are interpolated via prompts during
   server-mode init **OR** left as documented placeholder strings if
   the operator declines to supply them now (init prompts only when
   the field has no schema default — see data-model.md §1).
2. Write to `<state_dir>/config.toml.tmp` with `os.OpenFile(..., O_WRONLY|O_CREATE|O_EXCL, 0o600)`.
3. `f.Sync()` to flush.
4. `os.Rename(..., "config.toml")`.
5. Defensive `os.Chmod(0o600)` post-rename in case `umask` mangled
   the bits during `O_CREATE`.

**Rationale**: `O_EXCL` is the strongest collision guard against a
TOCTOU race between init-was-killed and init-resumed scenarios.
`f.Sync()` before `Rename` matches the durability guarantee of
`vault.Save` (SDD-03). The defensive `os.Chmod` is cheap and matches
`docs/SECURITY.md`'s mode-checking requirement.

**Path prefix generation**: the schema requires `path_prefix` to be
6–32 URL-safe characters and "generated by `hush init`". Init
generates a 12-character `[A-Za-z0-9_-]` prefix from `crypto/rand`.
Reads 9 random bytes, encodes via `base64.RawURLEncoding` (yields
12 chars).

**Alternatives considered**:

1. **Hand-roll TOML output.** Rejected: pelletier/go-toml/v2 is
   already a direct dep, used by `internal/config` for read; using
   it for write keeps the field names and quoting consistent with
   the loader and avoids a subtle round-trip mismatch.
2. **Skip `O_EXCL` and assume the directory is empty.** Rejected:
   FR-021 mandates "no partially-initialized state on retry"; `O_EXCL`
   makes that bullet-proof at the syscall layer.

---

## §10 — Sentinel-leak discipline

**Decision**: `init.go` adds a single test sentinel
`SECRET_SHOULD_NEVER_APPEAR_15` (via `internal/testutil.SentinelSecret(15)`)
and three sentinel-leak tests:

1. `TestInitServer_NeverLeaksPassphraseToOutput` — drives PTY input
   with a passphrase containing the sentinel, captures stdout +
   stderr + slog output, asserts `AssertSentinelAbsent`.
2. `TestInitServer_NeverReadsPassphraseFromEnv` — sets
   `HUSH_PASSPHRASE=<sentinel>`, drives a different passphrase via
   PTY, asserts the resulting vault opens with the PTY-supplied
   passphrase only.
3. `TestInitClient_NeverLeaksDerivedKeyToOutput` — drives PTY input,
   captures all output streams, asserts no byte sequence matches the
   stored keychain item value (verified by retrieving from the
   FakeKeychain).

**Rationale**: SDD-15 requires sentinel-leak coverage at 100%; SC-006
mandates a 0% leak rate. Three tests cover the three independent
secret-bearing values (passphrase, derived private key, bot token —
the latter rolled into the first test by including it in the same
PTY script).

---

## §11 — Test-injectable seams

**Decision**: `init.go` exposes the following testable seams via an
unexported `initDeps` struct:

```go
type initDeps struct {
    keychain      keychain.Keychain                                            // overridable; default = keychain.New(...)
    binaryPath    func() (string, error)                                       // default = os.Executable
    randReader    io.Reader                                                    // default = rand.Reader
    ttyReader     func(in *os.File, prompt io.Writer, label string) ([]byte, error) // default = readPassphraseTTY
    stateDirRoot  string                                                       // default = "" → cfg.Server.StateDir
    nowFn         func() time.Time                                             // default = time.Now (for deterministic logging in tests)
}
```

**Rationale**: mirrors the `serveDeps` pattern (SDD-14). Each seam
has exactly one production binding; tests inject substitutes that
return programmed values. Constitution IX is satisfied because the
struct is unexported (no public surface to lock) and every field has
a clear ownership.

**Alternatives considered**: package-level function pointers (e.g.
`var execLookPath = exec.LookPath`). Rejected: violates
"no globals" (Constitution IX).

---

## §12 — Resolved status of all NEEDS CLARIFICATION

The plan's Technical Context contained no `NEEDS CLARIFICATION`
markers — every field was resolved by the SDD-15 contract, the spec
clarifications round (2026-05-03), or this research document. The
plan-phase Constitution Check passes with the **§1 zalando/go-keyring
written justification** as the sole new-direct-dep entry.
