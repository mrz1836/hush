# Vault Rekey — Runbook

> Operator runbook for `hush vault rekey`. This command rotates the vault
> passphrase — the single root of trust for the entire BIP32 key
> hierarchy. Use it when the existing passphrase may have been disclosed,
> when policy mandates a periodic rotation, or when migrating between
> passphrase managers. If you only want to refresh the encrypted-vault
> nonce under the same passphrase-derived key, use `hush secret rotate`
> instead.

---

## 1. `secret rotate` vs `vault rekey`

The two verbs look similar from the prompt level but rotate different
material. Pick the one whose effect actually matches what you need.

| Property                       | `hush secret rotate`                                     | `hush vault rekey`                                        |
|--------------------------------|----------------------------------------------------------|-----------------------------------------------------------|
| What changes                   | Vault nonce (re-encryption under same key)               | Vault salt + key + ciphertext (re-encryption under new key) |
| Passphrase prompt              | Current passphrase only                                  | Current passphrase, then new + confirmation               |
| Root-of-trust effect           | None — the BIP32 master seed is unchanged                | Full — every derived key (JWT signing, vault, audit, client keys) is rotated |
| Running daemon                 | Hot reload via SIGHUP (no restart needed)                | **Restart required** — no signal is sent                  |
| Snapshot of previous vault     | Not written                                              | Always written to `secrets.vault.bak-<RFC3339>` (mode `0600`) |
| Keychain effect (default)      | None                                                     | None (`--update-keychain` is opt-in)                      |
| Audit event                    | `vault_rotated`                                          | `vault_rekeyed`                                           |

Rule of thumb: `secret rotate` is housekeeping; `vault rekey` is a
passphrase change.

---

## 2. Before you start

- You must know the **current** vault passphrase. There is no recovery
  path through `vault rekey` if the current passphrase is lost.
- Decide the **new** passphrase ahead of time (≥12 bytes; must differ
  from the current one). The command refuses both inputs but the
  cleanest UX is to have the new passphrase ready in your password
  manager before you start typing.
- Run the command on the vault host as the same user that owns
  `~/.hush/secrets.vault`. The command is TTY-only and refuses piped
  stdin or redirected stdout.
- A running `hush serve` keeps the **old** passphrase-derived key in
  memory until it restarts. The rekey atomically replaces the on-disk
  vault, but the daemon will not pick up the new key on its own.
- Plan for the snapshot file: `secrets.vault.bak-<RFC3339>` remains
  decryptable under the **old** passphrase. Treat it like the prior
  vault, not like a free backup.

---

## 3. Default flow (Keychain untouched)

The default invocation makes zero Keychain calls. The vault file is
rewritten under a fresh salt and a key derived from the new passphrase;
nothing else on the system is mutated.

```bash
hush vault rekey
```

What the command does, in order:

1. Verifies stdin **and** stdout are interactive TTYs. Piped/redirected
   execution is refused with no vault mutation and an audit record of
   `outcome=tty_refused`.
2. Prompts for the current vault passphrase. Wrong passphrase exits
   non-zero with `outcome=passphrase_failed`; the vault is not touched.
3. Prompts for the new passphrase and a confirmation. Mismatches and
   passphrases shorter than 12 bytes are rejected without a snapshot
   or vault write.
4. Rejects a new passphrase that equals the current one (constant-time
   comparison runs before either buffer is destroyed).
5. Snapshots `secrets.vault` to `secrets.vault.bak-<RFC3339>` with mode
   `0600`. The snapshot is your rollback artefact.
6. Mints a fresh 16-byte salt, derives a new vault encryption key, and
   atomically rewrites `secrets.vault`.
7. Probes the server PID file (read-only — only `kill(pid, 0)` is
   used). If a server is running, prints:
   > `hush: vault: running server detected (pid=N) — restart it to pick
   > up the new passphrase`
8. Prints the success line with the absolute snapshot path:
   > `hush: vault: rekey complete; snapshot=<absolute path>`
9. Emits one `vault_rekeyed` audit event with `outcome=success`,
   `restart_required`, `keychain_updated=false`, and `snapshot_path`.

### Step 3a — update the Keychain by hand (macOS)

`hush serve` reads the vault passphrase at startup from the
`hush-vault-passphrase` / `hush-server` Keychain item created by
`hush init server`. After a default rekey that item still holds the
**old** passphrase. Update it before restarting the daemon, or
`hush serve` will fail to unlock the new vault:

```bash
security delete-generic-password -s hush-vault-passphrase -a hush-server >/dev/null 2>&1 || true
security add-generic-password \
  -s hush-vault-passphrase \
  -a hush-server \
  -T "$(command -v hush)" \
  -U \
  -w
```

The `-w` flag triggers a no-echo passphrase prompt. Paste the **new**
passphrase you just set. The `-T "$(command -v hush)"` clause pins the
per-binary ACL to the current `hush` binary — without it, macOS will
prompt for the login Keychain password every time `hush serve` reads
the item.

If your deployment uses a dedicated Keychain
(`bot_keychain_path = "<state_dir>/hush.keychain-db"` in
`config.toml`), pass `~/Library/Keychains/...` or the explicit
`<state_dir>/hush.keychain-db` path as the trailing argument to
`security add-generic-password` so the new item lands in the same file.

### Step 3b — restart the daemon

Restart `hush serve` (or the supervisor it runs under) so it picks up
the new passphrase from Keychain:

```bash
# Foreground / dev:
pkill -TERM -x hush && hush serve --reload-on-vault-change

# launchd:
launchctl kickstart -k gui/$(id -u)/<your.hush.serve.label>
```

Existing client sessions are unaffected — JWTs and ECIES envelopes
issued before the rekey continue to work until they expire. Only the
vault file's on-disk encryption changed; nothing about the wire
protocol changed.

---

## 4. Opt-in flow (`--update-keychain`)

If you want the Keychain update to happen inside the same TTY session
that ran the rekey, pass `--update-keychain`:

```bash
hush vault rekey --update-keychain
```

This adds a single Retrieve → Delete → Store sequence against
`hush-vault-passphrase` / `hush-server` after the vault has been
rewritten:

- **Unsupported platform** (no per-binary ACL backend): the flag is a
  no-op with a warning line. The vault rewrite still succeeds.
- **Item missing** (no existing Keychain entry to update): no-op with a
  warning line. The flag never *creates* an item; if you have never
  run `hush init server` on this host, there is nothing for it to
  replace. Use the manual `security add-generic-password` command from
  §3a instead.
- **Item present**: Delete + Store under the running `hush` binary's
  ACL. On success the command prints
  `hush: vault: --update-keychain: Keychain item updated`.

### Partial-failure semantics

The Keychain update is not atomic. If `Delete` succeeds but `Store`
fails — for example because the dedicated Keychain has been locked
between calls — the rekey is reported as a **partial success**:

- The new vault stays in place (the rewrite already committed).
- The snapshot (`secrets.vault.bak-<RFC3339>`) stays in place as the
  rollback artefact.
- The command prints:
  > `hush: vault: vault rekey SUCCEEDED but Keychain update FAILED —
  > manual follow-up required: <error>`
- The audit event is emitted with `outcome=success_partial`,
  `keychain_updated=false`.
- The process exits non-zero.

To recover, run the manual `security add-generic-password` command
from §3a with the new passphrase. The vault is already on the new
key; only the Keychain item needs to catch up.

---

## 5. Snapshot management

Every successful rekey writes one snapshot file alongside the live
vault:

```
~/.hush/secrets.vault.bak-2026-05-26T01:23:45Z
```

Properties:

- Mode `0600`, owned by the same user as `secrets.vault`.
- Encrypted under the **old** passphrase, with the **old** salt.
- Byte-identical to the pre-rekey `secrets.vault` (the rewrite uses an
  atomic rename, so the snapshot is taken before any new content
  touches disk).

### Retention

Treat snapshots like any other encrypted secrets file. Keep them only
as long as you might need to roll back. A common policy is:

- Verify the new passphrase works (run `hush serve` and prove an
  approved `hush request` returns a secret).
- Wait for the next backup cycle so the new vault is captured.
- **Then** delete the snapshot:

```bash
shred -u ~/.hush/secrets.vault.bak-<RFC3339>   # Linux
rm -P ~/.hush/secrets.vault.bak-<RFC3339>      # macOS (BSD rm)
```

### Manual rollback

If the new passphrase turns out to be wrong, lost, or compromised
before you trust the new state, restore the snapshot:

```bash
cp -p ~/.hush/secrets.vault.bak-<RFC3339> ~/.hush/secrets.vault
chmod 0600 ~/.hush/secrets.vault

# Restore the Keychain item to the OLD passphrase:
security delete-generic-password -s hush-vault-passphrase -a hush-server >/dev/null 2>&1 || true
security add-generic-password -s hush-vault-passphrase -a hush-server \
  -T "$(command -v hush)" -U -w
```

Then restart `hush serve`. The system is back on the prior passphrase
and the snapshot has served its purpose; remove it as in the
**Retention** section above.

There is **no automatic rollback**. The snapshot is the only rollback
artefact and the only path back; do not delete it until you are
confident the new passphrase is durable.

---

## 6. Audit event shape

Every terminal path emits one `vault_rekeyed` event through
`slog.Default()` with a stable attribute set:

| Attribute          | Type   | Notes |
|--------------------|--------|-------|
| `verb`             | string | Always `"rekey"`. |
| `outcome`          | string | `success`, `success_partial`, `tty_refused`, `passphrase_failed`, `passphrase_too_short`, `new_passphrase_mismatch`, `new_passphrase_unchanged`. |
| `restart_required` | bool   | True only when the read-only PID probe found a live server. |
| `keychain_updated` | bool   | True only on a successful opt-in Keychain update. |
| `snapshot_path`    | string | Absolute path on success / `success_partial`. Empty string for pre-snapshot failures. |

No passphrase, salt, key, or secret material appears in audit
attributes. Pre-snapshot failures (`tty_refused`,
`passphrase_failed`, validation errors) carry empty `snapshot_path`
because no snapshot was written.

---

## 7. What `vault rekey` does **not** do

- It does **not** signal the daemon. `secret rotate` SIGHUPs `hush
  serve`; `vault rekey` deliberately does not, because the running
  process still holds the **old** key in memory and a SIGHUP would
  just make it re-read the new vault with the wrong key. The command
  prints a restart-required line when a live server is detected.
- It does **not** migrate, re-encrypt, or touch client keys directly.
  Client BIP32 derivations are produced from the same master seed at
  request time; rotating the master seed (which is what a rekey does)
  changes every derived key the next time the daemon starts.
- It does **not** offer a scripted/non-interactive mode. There is no
  `--non-interactive`, `--input-file`, or env-var passphrase escape
  hatch — passphrase entry must come from an attached terminal.
- It does **not** mutate the macOS Keychain unless
  `--update-keychain` is passed.
- It does **not** automatically delete or rotate snapshot files. The
  operator owns snapshot retention (see §5).

---

## 8. Cross-references

- Day-to-day operations and structured-error reference:
  [`OPERATIONS.md`](OPERATIONS.md).
- Threat model and the seven security layers, including the BIP32 key
  hierarchy that derives every key from the vault passphrase:
  [`SECURITY.md`](SECURITY.md).
- Agent-machine cleanup and Keychain item inventory:
  [`CLEAN-MACHINE.md`](CLEAN-MACHINE.md).
