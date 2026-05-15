# Contract — `hush init` CLI surface (locked at SDD-15)

This is the operator-facing CLI contract for the `init` subcommand.
Test files assert against the literal text where called out as
"locked".

---

## 1. Subcommand tree

```
hush init                                — prints help, exits non-zero (no default mode)
hush init server                         — server-mode bootstrap
hush init client --machine-index <N>     — client-mode bootstrap
```

Each subcommand inherits the four global persistent flags from the
SDD-14 root (`--config/-c`, `--verbose/-v`, `--quiet/-q`,
`--no-color`). The persistent flags are accepted but mostly inert
for `init` — `init` writes the config rather than reading it, so
`--config` is unused; `--verbose` adds operational slog lines to
stderr; `--quiet` suppresses non-error stderr lines; `--no-color`
strips ANSI from any TTY decorations.

---

## 2. `hush init server`

### 2.1 Flags

`hush init server` accepts no positional arguments and no
subcommand-specific flags beyond the inherited globals. All operator
input is collected via interactive TTY prompts.

### 2.2 Prompt sequence (locked order)

```
1. "Vault passphrase: "                  [no-echo TTY read]
2. "Confirm vault passphrase: "          [no-echo TTY read]
3. "Listen address (e.g. 100.96.10.4:7743): "
4. "Discord owner ID (snowflake): "
5. "Discord application ID (snowflake): "
6. "Discord bot token: "                 [no-echo TTY read]
```

Prompts 1, 2, 6 use `term.ReadPassword`; prompts 3–5 use a line
reader (`bufio.Scanner`).

### 2.3 Exit conditions

| Condition | Stderr message (literal-text contract) | Exit code |
|---|---|---|
| Stdin not a terminal | `hush: init: stdin must be an interactive terminal` | `ExitInputErr` (2) |
| Passphrase < 12 bytes | `hush: init: passphrase must be at least 12 characters` | `ExitInputErr` (2) |
| Confirmation mismatch | `hush: init: passphrase confirmation does not match` | `ExitInputErr` (2) |
| Vault file already exists | `hush: init: vault already exists at <path>` | `ExitErr` (1) |
| Config file already exists | `hush: init: config already exists at <path>` | `ExitErr` (1) |
| Keychain item already exists | `hush: init: keychain item already exists for service=<s> account=<a>` | `ExitErr` (1) |
| Platform without per-binary ACL | `hush: init: platform <GOOS> has no per-binary keychain ACL; init refuses to run` | `ExitErr` (1) |
| Operator-input prompt empty after 3 attempts | `hush: init: <field> is required` | `ExitInputErr` (2) |
| Underlying keychain failure | `hush: init: keychain store failed: <err>` | `ExitErr` (1) |
| Underlying file-write failure | `hush: init: write <path>: <err>` | `ExitErr` (1) |
| Success | `hush: init: server bootstrap complete` (TTY) / no stdout output | `ExitOK` (0) |

### 2.4 Artifacts produced on success

- `~/.hush/secrets.vault` (mode `0600`) — fresh empty vault with a
  16-byte salt
- `~/.hush/config.toml` (mode `0600`) — populated with every
  documented default plus the operator-supplied `listen_addr`,
  `discord_owner_id`, `application_id`
- Keychain item `(hush-vault-passphrase, hush-server)` — the
  passphrase, ACL = absolute path of running `hush` binary
- Keychain item `(hush-discord, hush-server)` — the bot token,
  same ACL

The vault file MUST decrypt successfully on the first subsequent
`hush serve` invocation when the operator supplies the same
passphrase (SC-003 — verified by integration test).

---

## 3. `hush init client`

### 3.1 Flags

| Flag | Required? | Type | Notes |
|---|---|---|---|
| `--machine-index N` | yes | uint32 | identifies the per-machine client key; missing flag → `errMissingFlag` |

### 3.2 Prompt sequence (locked order)

```
1. "Vault passphrase: "                  [no-echo TTY read]
2. "Confirm vault passphrase: "          [no-echo TTY read]
```

### 3.3 Exit conditions

| Condition | Stderr message (literal-text contract) | Exit code |
|---|---|---|
| `--machine-index` missing | `hush: init: missing required flag: --machine-index` | `ExitInputErr` (2) |
| `--machine-index` not parseable as uint32 | `hush: init: --machine-index must be a non-negative integer` | `ExitInputErr` (2) |
| Stdin not a terminal | `hush: init: stdin must be an interactive terminal` | `ExitInputErr` (2) |
| Passphrase < 12 bytes | `hush: init: passphrase must be at least 12 characters` | `ExitInputErr` (2) |
| Confirmation mismatch | `hush: init: passphrase confirmation does not match` | `ExitInputErr` (2) |
| Keychain item already exists | `hush: init: keychain item already exists for service=hush-client account=machine-<N>` | `ExitErr` (1) |
| Platform without per-binary ACL | `hush: init: platform <GOOS> has no per-binary keychain ACL; init refuses to run` | `ExitErr` (1) |
| Underlying keychain failure | `hush: init: keychain store failed: <err>` | `ExitErr` (1) |
| Success | (no stderr — fingerprint goes to stdout) | `ExitOK` (0) |

### 3.4 Stdout output on success (locked)

Exactly **one** line:

```
SHA256:<43-char-base64>
```

Where:
- `SHA256:` is the literal 7-character prefix
- `<43-char-base64>` is `base64.RawStdEncoding.EncodeToString(sha256.Sum256(SEC1Compressed(pub)))` — exactly 43 characters from the alphabet `[A-Za-z0-9+/]`
- The line ends with exactly one `\n` byte
- No leading whitespace, no trailing space, no surrounding text

Tests assert this contract via:

```go
got := strings.TrimRight(stdout.String(), "\n")
require.Regexp(t, `^SHA256:[A-Za-z0-9+/]{43}$`, got)
require.Equal(t, 50, len(got))
require.Equal(t, "\n", stdout.String()[len(got):])
```

### 3.5 Determinism contract

For a given (passphrase, machine-index) pair, the printed fingerprint
is byte-identical across runs (SC-004). This is verified end-to-end
by `TestInitClient_DeterministicAcrossRuns` — two PTY-driven runs
with the same scripted passphrase and same machine index produce
identical stdout output, byte-for-byte.

For different passphrases at the same machine index, OR the same
passphrase at different machine indices, the fingerprints differ
with overwhelming probability (SC-005). Verified by
`TestInitClient_DistinctInputsProduceDistinctFingerprints`.

---

## 4. Universal contracts (both modes)

### 4.1 No environment-variable passphrase reads

`init.go` MUST NOT call `os.Getenv` for any passphrase-class value.
The CI lint check is a `grep -nF os.Getenv internal/cli/init.go`
asserting zero matches.

### 4.2 No argv passphrase reads

No flag value is ever interpreted as a passphrase. The cobra command
definitions for both subcommands declare zero `--passphrase`-like
flags.

### 4.3 Sentinel-leak invariant

For any sentinel value `S` supplied as the passphrase or bot token
during a test, no byte of `S` may appear in stdout, stderr, or the
operational slog output. Asserted by the three sentinel-leak tests
in research §10.

### 4.4 Atomic-write invariant

If init crashes or is killed mid-run, the post-condition is one of:

- **before any write**: no vault, no config, no keychain item
- **after vault write but before config write**: vault exists but
  config does not; init exit was non-zero; on retry, init refuses
  per FR-012
- **after vault + config write but before keychain Store**: vault +
  config exist; init exit was non-zero; on retry, init refuses

In no case is the vault file left half-written (the `<path>.tmp` →
`rename` pattern of `vault.Save` and the parallel pattern in init's
`config.toml` writer guarantee this — partial state is never visible
to readers).

### 4.5 Re-run discipline

There is no `--force`, `--overwrite`, or `--repair` flag. The
operator must `rm`, `security delete-generic-password`, etc. by hand
to re-bootstrap. This is the locked Clarification 2026-05-03 Q1
answer.

---

## 5. Stability

This contract is **locked** at SDD-15. Future SDDs may **add**
prompts (e.g. for additional secrets in SDD-17) or flags, but MUST
NOT alter:
- the locked exit-code mappings in §2.3 / §3.3
- the literal-text error messages
- the stdout fingerprint format
- the prompt order for the existing fields
- the "no env var, no argv" invariants
