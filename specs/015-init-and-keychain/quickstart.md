# Quickstart — SDD-15 (`hush init`)

This document is the operator-facing walkthrough for the bootstrap
flow this chunk delivers. It doubles as the validation script for
the integration test (`init_integration_test.go`) — every step
below maps to one block in that test.

---

## Prerequisites

- macOS (darwin) — Linux init currently refuses (research §2)
- Tailscale running, your Tailscale IP known (e.g. `100.96.10.4`)
- A Discord application with a bot token in hand
- The `hush` binary at a stable absolute path (e.g.
  `/usr/local/bin/hush`); `os.Executable()` resolves this and is
  passed as the keychain ACL

---

## 1. Bootstrap the vault host

On the trusted vault host, with Tailscale up:

```sh
hush init server
```

Interactive transcript (operator input shown after each prompt):

```
Vault passphrase: ····················              # ≥ 12 chars; chosen by operator
Confirm vault passphrase: ····················
Listen address (e.g. 100.96.10.4:7743): 100.96.10.4:7743
Discord owner ID (snowflake): 123456789012345678
Discord application ID (snowflake): 345678901234567890
Discord bot token: ··················
hush: init: server bootstrap complete
```

Exit code: `0`.

**Verification**:

```sh
ls -l ~/.hush/
# -rw-------  vault file (mode 0600)
# -rw-------  config.toml (mode 0600)

security find-generic-password -s hush-discord -a hush-server -w
# prints the bot token to stdout (because the running shell's
# parent /usr/local/bin/hush is the ACL'd reader; if you run this
# from a different binary path, macOS prompts you)

security find-generic-password -s hush-vault-passphrase -a hush-server -w
# prints the passphrase
```

Then start the server:

```sh
hush serve
# Vault passphrase: ····················
# server: ready
```

The server reads the passphrase from the TTY (per SDD-14) and
unlocks the vault. The bot token is read from the keychain by
`serve.go::loadBotToken`.

---

## 2. Enroll an agent machine

On a fresh agent machine (machine index 3 in this example):

```sh
hush init client --machine-index 3
```

Interactive transcript:

```
Vault passphrase: ····················
Confirm vault passphrase: ····················
SHA256:abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ
```

Exit code: `0`. Stdout contains exactly one line: the
OpenSSH-style fingerprint. Copy that line and add it to the
server's registered-clients list (workflow defined in SDD-29).

**Verification on the agent**:

```sh
security find-generic-password -s hush-client -a machine-3 -w
# prints the per-machine private key (binary)

# Re-running produces the same fingerprint:
hush init client --machine-index 3
# hush: init: keychain item already exists for service=hush-client account=machine-3
```

The second run refuses (Clarification Q1) — the operator must
`security delete-generic-password -s hush-client -a machine-3`
before re-enrolling.

---

## 3. Negative-path verifications (matching the spec acceptance scenarios)

### 3.1 Short passphrase rejected (SC-010)

```sh
echo -n "shortpass" | hush init server  # not actually piped — would fail at TTY check
# In an interactive run: enter "shortpass"
# hush: init: passphrase must be at least 12 characters
```

Exit code `2` (`ExitInputErr`). No vault, no config, no keychain
item created.

### 3.2 Confirmation mismatch

Enter `correctpassphrase1` and `correctpassphrase2`:

```
Vault passphrase: ··································
Confirm vault passphrase: ··································
hush: init: passphrase confirmation does not match
```

Exit code `2`. No artifact created.

### 3.3 Mode conflict (FR-018)

Structurally impossible — the cobra command tree separates `server`
and `client` into distinct subcommands. There is no flag combination
that produces the conflict.

### 3.4 Missing `--machine-index`

```sh
hush init client
# hush: init: missing required flag: --machine-index
```

Exit code `2`. No artifact created.

### 3.5 Re-run on existing vault (FR-012)

```sh
hush init server
# hush: init: vault already exists at /Users/<you>/.hush/secrets.vault
```

Exit code `1` (`ExitErr`). The existing vault is untouched.

### 3.6 Linux platform refusal (FR-020a)

On a Linux host:

```sh
hush init server
# hush: init: platform linux has no per-binary keychain ACL; init refuses to run
```

Exit code `1`. No artifact created.

---

## 4. Determinism check (SC-004 / SC-005)

```sh
# Same passphrase, same machine index → same fingerprint
hush init client --machine-index 0   # → SHA256:AAA...
security delete-generic-password -s hush-client -a machine-0
hush init client --machine-index 0   # → SHA256:AAA... (same)

# Same passphrase, different machine index → different fingerprint
security delete-generic-password -s hush-client -a machine-0
hush init client --machine-index 1   # → SHA256:BBB... (different)
```

---

## 5. Sentinel-leak check (SC-006 / SC-007)

This is normally automated by the test suite, but operators can
verify by hand:

```sh
HUSH_PASSPHRASE=SECRET_SHOULD_NEVER_APPEAR_15 hush init server
# Enter a different passphrase at the prompt: "myrealpassphrase1"
# Then:
grep -F SECRET_SHOULD_NEVER_APPEAR_15 ~/.hush/config.toml
# (no match expected)
```

The env var is ignored; only the TTY-supplied passphrase decrypts
the vault.

---

## 6. Cleanup

To start over from scratch:

```sh
rm -rf ~/.hush/
security delete-generic-password -s hush-discord -a hush-server
security delete-generic-password -s hush-vault-passphrase -a hush-server
# For each enrolled machine:
security delete-generic-password -s hush-client -a machine-<N>
```

---

## 7. What this quickstart does NOT cover

- Adding secrets to the vault (`hush secret add`) — SDD-17.
- Issuing a session via `hush request` — SDD-16.
- Discord approval flow — SDD-11.
- Supervisor lifecycle (`hush supervise`) — SDD-23.

Those flows assume `hush init` has already produced the bootstrap
artifacts described above.
