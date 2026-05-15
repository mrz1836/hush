# Quickstart — `hush secret`

Operator-facing TL;DR for the four verbs delivered by SDD-17.
Every invocation must run at an interactive terminal — see §"TTY
gate" below.

## Prerequisites

```bash
hush init server      # one-time vault host bootstrap (SDD-15)
```

After `init server` completes, the vault file at `~/.hush/secrets.vault`
exists, is mode `0600`, and is empty. The vault passphrase is in
the macOS Keychain bound to the running `hush` binary path.

## Add a secret

```bash
hush secret add ANTHROPIC_API_KEY
# Vault passphrase: ********
# Secret value:     ********           # no echo
# Confirm secret value: ********       # no echo
# Description (optional): production console key
```

After the command exits 0, the vault contains the new entry. The
vault file's bytes have changed (new ciphertext) and the file mode
remains `0600`.

Name validation: `^[A-Z_][A-Z0-9_]*$`, length 1–64. A bad name
fails fast with `ExitInputErr` (2) BEFORE the vault is opened.

A duplicate name refuses with `ExitErr` (1) and the message
`hush: secret: entry NAME already exists; use 'hush secret rotate' to replace`.
The existing value is never disclosed.

## List entries

```bash
hush secret list
# ANTHROPIC_API_KEY — production console key
# GITHUB_TOKEN — gh auth token, scope: repo, workflow
# OPENAI_API_KEY
```

Two rendering modes, chosen by stdout:

```bash
hush secret list                        # text on a TTY
hush secret list | jq                   # JSON on a pipe (stdin still TTY)
# [{"name":"ANTHROPIC_API_KEY","description":"production console key"}, …]
```

`list` NEVER prints values. The same invariant holds in both
modes. An empty vault is not an error: TTY → stderr message
`(vault is empty)`; pipe → stdout `[]\n`. Entries are always
sorted ascending by name.

## Remove an entry

```bash
hush secret remove OPENAI_API_KEY
# Vault passphrase: ********
# Type the entry name to confirm: OPENAI_API_KEY
```

Typing anything other than `OPENAI_API_KEY` (case-sensitive,
byte-exact) refuses with `ExitInputErr` (2) and the vault is left
unchanged.

A nonexistent name refuses with `ExitNotFound` (4) BEFORE the
confirmation prompt fires.

## Rotate the vault

```bash
hush secret rotate
# Vault passphrase: ********
# hush: secret: signalled running server (pid=12345)
```

`rotate` re-encrypts the vault file with a fresh nonce + salt
(ciphertext bytes change; entry set is identical). If a running
`hush serve` has written `~/.hush/hush.pid`, rotate signals it via
SIGHUP so the server's atomic vault reload picks up the new
ciphertext without dropping in-flight requests.

When no PID file is present, rotate still rewrites the vault and
exits 0 with a warning:

```bash
hush secret rotate
# Vault passphrase: ********
# hush: secret: no running server signalled (no PID file)
```

This is a normal operational state — rotating an offline vault is
a legitimate operator action, not an error.

## TTY gate (universal)

All four verbs refuse to run unless `stdin` is an interactive
terminal. Piping anything into the command — even just `echo` —
returns exit code 2 and the message:

```text
hush: secret: this command requires an interactive TTY (rogue-process defence)
```

This is the documented defence against the "rogue process runs
`hush secret add`" threat in
[docs/SECURITY.md](../../docs/SECURITY.md). A background process
that has obtained shell access on the vault host CANNOT silently
inject, replace, or remove a vault entry by piping bytes into the
command.

The supported interactive-with-pipe combinations:

| Invocation | stdin | stdout | Behaviour |
|------------|-------|--------|-----------|
| `hush secret list` | TTY | TTY | text rendering |
| `hush secret list \| jq` | TTY | pipe | JSON rendering |
| `hush secret list > out.json` | TTY | file | JSON rendering |
| `echo foo \| hush secret add NAME` | pipe | TTY | refuses |
| `hush secret list < /dev/null` | non-TTY | TTY | refuses |

## Exit codes

| Code | Constant | When |
|------|----------|------|
| 0 | `ExitOK` | success |
| 1 | `ExitErr` | already-exists on `add`; otherwise unmapped errors |
| 2 | `ExitInputErr` | TTY refusal; bad name; mismatched value/token |
| 3 | `ExitAuth` | wrong vault passphrase |
| 4 | `ExitNotFound` | `remove` of an absent entry; vault file missing |
| 5 | `ExitPerm` | vault file mode loose |

## Cheat-sheet

```bash
# bootstrap
hush init server

# populate
hush secret add ANTHROPIC_API_KEY
hush secret add GITHUB_TOKEN

# inspect
hush secret list
hush secret list | jq

# rotate (re-encrypts + signals running server)
hush secret rotate

# remove
hush secret remove ANTHROPIC_API_KEY
```
