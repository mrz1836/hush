# CLEAN-MACHINE — agent machine cleanup checklist

> hush requires zero secret files at rest on agent machines. This
> checklist is the executable form of that rule. Run it on every agent
> machine before deploying hush, and re-run it periodically as a hygiene
> check.

---

## What "clean" means

After applying this checklist, an agent machine should have **no API keys,
OAuth tokens, or service credentials in any file the OS will let a
same-user process read**. Secrets only ever appear as environment
variables in processes started by hush.

The list below is not exhaustive — it covers the high-frequency targets
that commodity malware grep for first. New tools that store credentials
in files require equivalent treatment.

---

## Pre-flight

Before starting:

1. Note which secrets you need to keep. Move them into the vault on the
   trusted host via `hush secret add` (interactive TTY only).
2. Confirm the agent machine has Tailscale running and is reachable from
   the vault host on port 7743.
3. Confirm `hush init --client --machine-index N` has been run on the
   agent (the client key is stored in the OS keychain with ACL).

---

## Checklist

Every section below is a separate hardening step. Run them in order.
Each step is reversible (you can copy your secrets back into a dotfile
later if you decide hush isn't for you).

### 1. Shell dotfiles — remove `export FOO=...` lines for secrets

```bash
# Inspect first — do NOT pipe to a removal tool blindly.
grep -nE 'sk-ant-|sk-proj-|ghp_|AKIA|api[_-]key' \
  ~/.zshrc ~/.bashrc ~/.bash_profile ~/.profile 2>/dev/null
```

If matches appear: edit each file, delete the matching `export` lines.
**Restart any open shells** (or `unset $VAR` in each one) so the secrets
disappear from existing process environments.

Files to check (edit/remove the secret lines manually — these are
hand-edits, NOT bulk-delete commands):

- `~/.zshrc`, `~/.bashrc`, `~/.bash_profile`, `~/.profile`
- `~/.zshenv`, `~/.zlogin`, `~/.zlogout`
- `~/.config/fish/config.fish` (if you use fish)
- `/etc/profile.d/*.sh` (system-wide; usually empty of secrets, but check)

### 2. Tool-specific credential stores

**GitHub CLI:**

```bash
gh auth status
gh auth logout
# Confirm:
test ! -s ~/.config/gh/hosts.yml && echo "gh clean" || echo "gh still has creds"
```

After this, `gh` works via `GITHUB_TOKEN` env var only — exactly what
`hush request --scope GITHUB_TOKEN` will inject.

**AWS CLI:**

```bash
# If you have non-secret config (region, output format), keep it.
# Remove the credentials file specifically:
test -e ~/.aws/credentials && rm ~/.aws/credentials
test -e ~/.aws/config && grep -E 'aws_access_key|aws_secret' ~/.aws/config && \
  echo "edit ~/.aws/config to remove credential keys"
```

After this, AWS SDK / CLI works via env vars: `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, optional `AWS_SESSION_TOKEN`. Inject via
`hush request --scope AWS_ACCESS_KEY_ID,AWS_SECRET_ACCESS_KEY`.

**Anthropic / OpenAI / Google AI SDKs:**

```bash
# These typically read from ANTHROPIC_API_KEY / OPENAI_API_KEY / GOOGLE_API_KEY
# env vars, with no on-disk fallback. Inspect any tool-specific configs:
ls -la ~/.config/anthropic 2>/dev/null
ls -la ~/.config/openai 2>/dev/null
# If config dirs hold credentials, remove the credential files specifically.
```

**Docker registry credentials:**

```bash
# Docker stores registry creds in ~/.docker/config.json (often base64,
# not encrypted). If you have CI registry credentials there, rotate them
# and store them in the vault.
grep -l '"auth":' ~/.docker/config.json 2>/dev/null
```

If you keep docker credentials on the machine, ensure they are
short-lived (use `docker login` + `docker logout` per session, or use
ephemeral tokens via your registry's auth proxy).

### 3. `.env` files anywhere under `$HOME`

```bash
# Find them — review each one before deleting.
find ~ -maxdepth 6 -name '.env' -type f 2>/dev/null
find ~ -maxdepth 6 -name '.env.*' -type f 2>/dev/null
```

For each `.env` file:

- If it's in a project repo, decide whether the project really needs
  it on disk, or whether you can run the project under
  `hush request --exec "<command>"` instead.
- If you keep it, ensure the project's `.gitignore` excludes it.

### 4. Key files (`*.key`, `*.pem`)

```bash
# Inspect — do NOT bulk-delete; some are legitimate (SSH host keys, TLS certs).
find ~ -maxdepth 5 \( -name '*.key' -o -name '*.pem' -o -name 'id_rsa' -o -name 'id_ed25519' \) -type f 2>/dev/null
```

Decide per file:

- SSH user keys (`~/.ssh/id_*`) — keep, but ensure passphrase-protected.
- Project signing keys, JWT signing keys, app-specific PEMs — move into
  the vault as base64-encoded secrets if needed at runtime, then remove
  the file.

### 5. Cron / launchd / systemd unit env files

If you run scheduled jobs, they often read env from a file. Audit:

- macOS launchd plists referencing `EnvironmentVariables`:
  `~/Library/LaunchAgents/*.plist` and `/Library/LaunchAgents/*.plist`
  (system-wide).
- systemd user units with `EnvironmentFile=`:
  `~/.config/systemd/user/*.service`.
- crontab entries with inline env: `crontab -l | grep -E '=|API|TOKEN'`.

For each: replace inline secrets with a reference to a
`hush request --exec` or `hush supervise` invocation. The `hush
supervise` model is preferred for long-running jobs (see
[`docs/DAEMONS.md`](DAEMONS.md)).

### 6. Editor / IDE plugin caches

Some editor plugins cache API keys (LLM autocomplete, AI assistants):

- `~/.config/Code/User/settings.json` (VS Code)
- `~/.config/JetBrains/*/options/*.xml`
- `~/Library/Application Support/{Cursor,Code,JetBrains}/`

Inspect for fields named `apiKey`, `token`, `secret`. Replace with the
plugin's mechanism for reading from env vars (most modern plugins
support this); inject the env via `hush request --exec`.

### 7. Browser / extension storage

Browsers may store dev API keys in profile databases (rare but possible).
This is typically not a hush concern — treat browser credential storage
as out of scope unless your threat model demands it.

### 8. macOS Keychain (if applicable)

Two cases:

- **hush-managed entries** — keep these. They are ACL-restricted to
  `/usr/local/bin/hush` (per `-T /usr/local/bin/hush` on
  `security add-generic-password`; see `deploy/install.sh`'s
  next-steps banner for the exact invocation). The three canonical
  entries are:
  - `hush-vault-passphrase` — vault passphrase, on the **vault host**
    only. Created by the operator with
    `security add-generic-password -a <hush-user> -s hush-vault-passphrase -T /usr/local/bin/hush -U -w '<passphrase>'`
    after `deploy/install.sh` completes (the installer prints the
    exact command in its banner).
  - `hush-discord` — Discord bot token, on the **vault host** only.
    Referenced by server config `[discord].bot_token_keychain_item`.
  - `hush-client` — per-machine client-key derivation marker, on
    each **agent host**. Created by `hush init --client --machine-index N`.
- **Tool-specific entries** (e.g. `gh-cli`, `git`, AWS CLI) — these
  may bypass the file-based credential stores you just cleaned up. If
  a tool caches a token in Keychain at login time, decide whether to:
  - Disable the tool's Keychain integration (preferred for hush model).
  - Leave it (acceptable if the Keychain ACL prevents non-tool processes
    reading it; verify with `security dump-keychain` and Access Control
    panel).

### 9. Shell history

History files often contain secrets that were typed inline:

```bash
grep -nE 'export.+=.+sk-|export.+=.+ghp_|export.+=.+AKIA' \
  ~/.zsh_history ~/.bash_history 2>/dev/null
```

If matches appear, redact the file (open in editor, delete the lines)
or rotate the corresponding secrets in the vault.

### 10. Team / shared notes

This is the human checkpoint: search the team's shared notes (Notion,
Confluence, shared 1Password vault for non-hush secrets, etc.) for any
keys that were once distributed to the agent machine. Rotate anything
that may have been exposed in plaintext during the previous workflow.

---

## Verification

After running the checklist:

```bash
# Quick re-scan of the high-frequency targets:
grep -rnE '(sk-ant-|sk-proj-|ghp_|AKIA|aws_secret|aws_access_key)' \
  ~/.zshrc ~/.bashrc ~/.bash_profile ~/.profile \
  ~/.config/gh/hosts.yml ~/.aws/credentials \
  $(find ~ -maxdepth 5 -name '.env' 2>/dev/null) \
  2>/dev/null

# Should produce zero matches.
```

If anything shows up, repeat the relevant section.

---

## Re-runnability

This is not a one-time exercise. Tools install new credential stores
silently. Re-run sections 1, 2, 3, 4 monthly, or whenever you install a
new credential-handling tool.

A useful operator habit: **before any new dev tool installation, check
whether it stores credentials on disk by default**, and if so, configure
it to read from env vars before installing.

---

## Cross-references

- [`docs/SECURITY.md`](SECURITY.md) §1.3 — what hush eliminates on agent
  machines (the threat-eliminated list).
- [`docs/DAEMONS.md`](DAEMONS.md) — running long-running jobs under
  `hush supervise` instead of dotfile env vars.
- [`docs/TAILSCALE-ACLS.md`](TAILSCALE-ACLS.md) — companion network-layer
  hardening.
