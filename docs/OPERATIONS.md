# Operations

## Purpose

This file is the runtime operating doc for hush. It is the first stop for a
new operator: it describes the single guided-setup path, day-to-day modes,
the structured-error remedy reference, and the operational invariants that
hold across releases.

If you have never run hush on this host, start with §1 ("First-run setup").
If you are already serving, jump to §2 ("Day-to-day modes") or §4
("Structured error reference") as needed.

---

## 1. First-run setup

### Built-in smoke test

Before putting real credentials in the vault, prove the full path with a fake
secret:

```bash
hush smoke --state-dir ~/.hush-smoke --reset
```

`hush smoke` prompts for the same Discord/listen-address inputs as server
setup, creates an isolated state dir, adds `HUSH_SMOKE_TEST=hello-from-hush`,
enrolls a client with a key-file fallback, starts a temporary `hush serve`,
waits for you to approve in Discord, verifies the fake secret through
`hush request`, prints one success line, and shuts the temporary server down.

Useful flags:

- `--state-dir` — isolated smoke state directory; default `~/.hush-smoke`.
- `--reset` — archive an existing smoke state dir before starting.
- `--listen-addr`, `--discord-owner-id`, `--discord-application-id`,
  `--discord-approval-channel-id` — skip the matching prompt.
- `--discord-audit-channel-id` — defaults to the approval channel.
- `--strict-clock` — disables the smoke-only clock-skew override while the
  temporary server runs.

Clean smoke/test artifacts safely:

```bash
hush smoke clean
```

By default this archives only `~/.hush-smoke`. To clean another isolated
test or validation vault, pass it explicitly with `--state-dir`; the command
accepts generic smoke/test/validation state names and refuses to touch real
`~/.hush` state. To permanently delete smoke state instead of archiving, use
the explicit confirmation gate:

```bash
hush smoke clean --destroy --confirm 'destroy smoke'
hush smoke clean --state-dir ~/.hush-release-validation
```

### Manual setup path

```bash
hush init server                         # guided / interactive (default)
hush serve --reload-on-vault-change      # binds Tailscale interface, brokers approvals
hush secret add OPENAI_API_KEY
```

`hush init server` is the bootstrap entry point for the vault host. It is
**guided by default**: the command runs a diagnostic-first preflight
pipeline, prompts for the inputs it actually needs, and never silently
overwrites pre-existing state.

You do **not** need to understand the macOS Keychain, the Discord bot
token storage layout, or the file-mode contract before running it — the
guided flow walks each decision interactively and explains the
consequences before any destructive choice.

At the end, `hush init server` prints four copy/paste commands. Prefer those
commands over hand-written ones: they include the generated server URL, the
client key-file path for the learning path, and the request flags needed to
make approval failures loud instead of ambiguous. Later, if you need the URL
again for scripts, use `hush --config '<state_dir>/config.toml' server-url`
instead of parsing TOML with `sed`. The request command should look like this
shape:

```bash
hush --config '<state_dir>/config.toml' request \
  --machine-index 1 \
  --client-key-file '<state_dir>/client-machine-1.key' \
  --server 'http://<tailscale-ip>:<port>/h/<path-prefix>' \
  --scope YOUR_SECRET \
  --ttl 5m \
  --max-uses 1 \
  --reason 'smoke test' \
  --exec printenv -- YOUR_SECRET
```

Do not quote `printenv YOUR_SECRET` as one string; `--exec` is the program and
arguments after `--` are passed to that program.

The minimum prerequisites are:

1. The hush binary is on `$PATH` (`hush version` works).
2. Tailscale is up on this host (`tailscale ip -4` returns a CGNAT
   address).
3. You have a Discord bot token reachable for paste-at-prompt (e.g. a
   1Password item — see `docs/SECURITY.md` §2.4 for storage guidance).

### What the guided flow does, in order

1. **Preflight pipeline.** Before any prompt fires, hush runs a
   deterministic check registry: binary/version probe → config-target
   writability → state-dir resolution + mode check → file-mode audit on
   existing artifacts → Keychain readability (Darwin) → Tailscale bind
   candidate → listen-port availability → clock sync → existing-artifact
   collision. Any `fail` short-circuits with a structured error (see §4)
   and an exact remedy.
2. **Existing-state classification.** If a previous bootstrap left
   config / vault / state-dir / Keychain artifacts, the classifier labels
   each as `safe-to-reuse`, `repairable`, or `collision` and prompts
   per-artifact: `[r]euse / [p]repair / [a]rchive / [q]uit`. `archive`
   renames the artifact to `<path>.bak-<RFC3339>` and continues. No
   artifact is overwritten or deleted without explicit operator
   confirmation.
3. **Discord/listen-address prompts.** Owner snowflake, application
   snowflake, listen address (Tailscale IP + free port), optional
   approval / audit channel IDs.

   The listen address is the **vault host's Tailscale IPv4 plus a free
   TCP port**. It is not the laptop/client IP. Run this on the machine
   that will run `hush serve`:

   ```zsh
   printf '%s:7743\n' "$(tailscale ip -4)"
   ```

   Paste the printed value at the prompt. For example, if the vault host's
   Tailscale IP is `100.96.10.4`, enter `100.96.10.4:7743`. Pick another
   free port such as `7744` if `7743` is already in use.
4. **Vault passphrase.** Entered with no echo. Confirmed once.
5. **Keychain writes (macOS).** Each Keychain write is preceded by a
   hush-authored explanation panel that names the item, says what is being
   stored, and tells you what to click in the Apple prompt that follows. The
   bare Apple "password data for new item" prompt never appears without that
   explanation immediately above it. If the login Keychain is locked or refuses
   the bot-token write, hush does **not** write a plaintext token file. It first
   offers a calm retry path so you can unlock/approve the Keychain while the
   token is still only in memory. Env-token fallback stays available, but only
   as a deliberate secondary choice. If the item already exists but macOS
   denies read access, run `hush keychain doctor` first and `hush keychain
   repair` to refresh the ACL for the current binary.
6. **Bootstrap completion + exact next commands.** `config.toml`,
   `secrets.vault`, and the state dir exist with the required modes. On
   macOS, the bot token is either in Keychain or supplied explicitly via the
   serve environment. The final panel prints copy/paste commands using the real
   config path, `listen_addr`, generated `path_prefix`, client registry, and
   suggested client key-file path.

When it finishes, follow the printed commands. The recommended serve command
uses `hush serve --reload-on-vault-change`, so secrets added after startup are
auto-reloaded without a restart or manual SIGHUP. If you run serve without that
flag, the server still supports manual hot reload by SIGHUP.

### Keychain store / ACL recovery during setup

The earlier config/vault reuse / repair / archive prompts are separate from
this later bot-token Keychain step. You can succeed on config and vault, then
still hit a macOS Keychain failure when hush stores the Discord token.

There are three different Keychain recovery paths:

- **Initial store failed** — you pasted the Discord bot token, but macOS refused
  to create the `hush-discord` item. If the login Keychain is locked, choosing
  `[r]` asks macOS to unlock the login Keychain, then retries the write while
  the token is still only in memory. If you choose env fallback instead,
  `hush keychain doctor` will report `missing` because no token was stored.
- **Login Keychain is unusable** — if the login Keychain password is unknown or
  out of sync, choose `[h]` to create/use a dedicated hush Keychain at
  `<state_dir>/hush.keychain-db`. This does not reset, delete, or modify
  `login.keychain-db`; hush stores the bot token in the dedicated Keychain and
  writes `bot_keychain_path` to the config so future `serve`, `doctor`, and
  `repair` commands target the same file.
- **Existing item denied** — the `hush-discord` item already exists, but the
  current hush binary cannot read it. Use `hush keychain doctor` to confirm and
  `hush keychain repair` to refresh the ACL for the current binary.

If an existing `hush-discord` Keychain item is present but macOS refuses the
read (Darwin exit 51 / `errSecAuthFailed` / `errSecInteractionNotAllowed`),
hush surfaces a recovery panel. The default path is to repair/retry Keychain;
env-token fallback is still available, but it is visually secondary and must be
chosen on purpose. The new `hush keychain doctor` / `hush keychain repair`
commands expose the same diagnosis and repair path directly:

| Choice | What it does |
|--------|--------------|
| `[1]` Retry / ACL repair | Retry after you unlock or approve the macOS prompt, or run `hush keychain repair` to refresh the ACL. Re-checks the Keychain afterwards. |
| `[2]` Delete + recreate | Destructive. Removes the existing item and stores a new one. Requires typing `delete` to confirm. The deletion is audit-logged. |
| `[3]` Env-token fallback | Skips the Keychain write and instructs you to export `HUSH_DISCORD_BOT_TOKEN` before `hush serve`. Deliberate secondary escape hatch only. After using it, `hush keychain doctor` will report missing because no token was stored; rerun `hush init server` to store it later. See `docs/SECURITY.md` §2.4 for the trade-off. |
| `[q]` Quit | Exits cleanly without modifying anything. |

#### Unlock failure (exit 51)

If `security unlock-keychain ~/Library/Keychains/login.keychain-db` exits 51
after you enter your current Mac password, that is a login Keychain mismatch,
not a hush secret leak. The prompt is for the macOS login Keychain; after
password changes or machine migrations, it may still be using an older
password. Hush never collects that password.

Try one of these:

- Retry with the correct/older login Keychain password.
- Fix the login Keychain in Keychain Access or System Settings, then retry.
- Choose `[h]` in the hush recovery prompt to create/use a dedicated hush
  Keychain at `<state_dir>/hush.keychain-db` without touching
  `login.keychain-db`.
- Use env-token fallback for this session only.

If you do not know the Keychain password, resetting the login Keychain is an
OS-level destructive repair outside hush, and hush will not automate it.

#### Dedicated hush Keychain

The dedicated hush Keychain is the recommended escape hatch when the macOS
login Keychain is broken but you still want durable, OS-managed storage for the
Discord bot token.

What hush does when you choose `[h]`:

1. Creates `<state_dir>/hush.keychain-db` if it does not exist.
2. Lets macOS `security` prompt for the dedicated Keychain password. Hush does
   not collect or log that password.
3. Tightens the dedicated Keychain file mode to `0600` before storing the
   token, because macOS may create `hush.keychain-db` as `0644` and hush's
   startup file-mode check intentionally refuses that.
4. Stores the Discord bot token as service `hush-discord`, account
   `hush-server`, ACL-restricted to the current hush binary.
5. Writes `bot_keychain_path = "<state_dir>/hush.keychain-db"` in
   `config.toml`, so future `hush serve`, `hush keychain doctor`, and
   `hush keychain repair` use the dedicated Keychain automatically.

Operational notes:

- Do not delete `hush.keychain-db`; it contains the durable bot-token item.
- The file is not plaintext, but it is still sensitive state. Keep it inside a
  `0700` state directory.
- If you forget the dedicated Keychain password, recreate that dedicated
  Keychain and rerun `hush init server`; this does not affect your macOS login
  Keychain.

Each branch is documented in detail in `docs/SECURITY.md` §2.4 ("Bot token
storage") so the security trade-offs are kept next to the threat model.

### Clock-sync failure during setup

If the preflight clock-sync check proves the clock is unsynchronised or
outside the drift budget, hush prints the platform-aware fix command (e.g.
`sudo sntp -sS time.apple.com` on macOS) and exits non-zero. Hush will
**never** run `sudo` on your behalf. If the read-only probe itself is killed
or unavailable during setup, hush warns and continues; `hush serve` remains
stricter.

If you have knowingly accepted clock skew (e.g. a deliberately air-gapped
test host), pass `--allow-clock-skew` to `hush init server` (and to
`hush serve`). The override downgrades the failure to a warning and
emits an audit event `clock_skew_override`.

---

## 2. Day-to-day modes

### Interactive work

Approve a session, wrap a shell, work inside it.

Primary path:
- `hush request --exec zsh`
- for a one-shot check with args: `hush request --scope OPENAI_API_KEY --ttl 5m --max-uses 1 --reason 'manual check' --exec printenv -- OPENAI_API_KEY`

Intent:
- one approval covers a bounded human work session
- tools inherit env vars from the wrapped shell
- secrets do not persist after the shell exits

### Agent-context flags (optional)

`hush request` accepts five optional flags that populate the Discord
approval embed with extra context — useful when AI agents call hush so
the human approver can spot anomalies before clicking Approve:

```bash
hush request \
  --agent claude-code/1.2.3 \
  --model claude-opus-4-7 \
  --tool Bash \
  --command 'git push origin master' \
  --summary 'Refactoring auth module' \
  --scope GITHUB_TOKEN --ttl 10m --max-uses 1 \
  --reason 'finish refactor' --exec true
```

The `--command` value is redacted client-side for common secret
patterns (`sk-…`, `ghp_…`, `xoxb-…`, `AKIA…`, generic high-entropy
base64) and re-redacted server-side before being shown to the
approver and recorded in the signed audit log. Length caps:
`--agent` ≤128, `--model` ≤64, `--tool` ≤64, `--command` ≤1024,
`--summary` ≤256. Oversized values are rejected with 400 `bad_request`.

> ⚠️ These fields are **operator-visible context, not authenticators**.
> A compromised agent can lie in any of them; trust the cryptographic
> identity (client signature, peer IP) for authorization. See
> `docs/SECURITY.md` §6.

### Daemon work

Run `hush supervise` under launchd/systemd.

Primary path:
- `hush supervise --config ~/.hush/supervisors/<name>.toml`

Intent:
- supervisor owns session continuity
- child restarts do not normally require a new phone approval
- refreshes happen in waking hours

### Vault root-key rotation (`hush vault rekey`)

`hush secret rotate` and `hush vault rekey` look adjacent but rotate
different material. Pick by the question you are answering.

| Question | Command |
|----------|---------|
| "Re-encrypt the vault under the same passphrase-derived key (refresh nonces; hot-reload `hush serve`)." | `hush secret rotate` |
| "Change the vault passphrase itself; rotate the root of trust for every BIP32-derived key." | `hush vault rekey` |

`hush vault rekey` is strictly TTY-only on both stdin and stdout, takes
no scripted-passphrase flags, prompts for the current passphrase, then
prompts for a new passphrase + confirmation, snapshots the current
vault to `secrets.vault.bak-<RFC3339>` (mode `0600`) before any
rewrite, and emits a single `vault_rekeyed` audit event.

Two operator follow-ups are mandatory after a successful rekey:

1. **Update the macOS Keychain item.** By default the rekey leaves
   `hush-vault-passphrase` / `hush-server` pointing at the **old**
   passphrase. Either pass `--update-keychain` (opt-in; updates the
   existing item only) or run `security add-generic-password -s
   hush-vault-passphrase -a hush-server -T "$(command -v hush)" -U -w`
   by hand with the new passphrase pasted at the no-echo prompt.
2. **Restart `hush serve`.** No signal is sent by the rekey command — a
   running daemon still holds the **old** key in memory until it
   restarts, so the vault rewrite alone is not sufficient. The success
   output prints a `restart it to pick up the new passphrase` reminder
   when a live server PID is detected.

The snapshot file remains decryptable under the **old** passphrase and
is the only manual rollback artefact; treat it like the prior vault
and delete it (`shred -u` on Linux, `rm -P` on macOS) once the new
state is trusted and backed up. See
[`docs/VAULT-REKEY.md`](VAULT-REKEY.md) for the full runbook,
including partial-failure semantics, audit attribute shape, and the
rollback procedure.

### Zero-downtime daemon reload (HTTP services)

When the supervised child is an HTTP server and the supervisor TOML
opts into the HTTP-proxy handoff
(`[child.handoff] mode = "http-proxy"` + `[child.readiness]`),
operators can roll out a new child without interrupting public
traffic:

```bash
hush supervise reload ~/.hush/supervisors/<name>.toml
```

The supervisor starts a candidate child on a private loopback port,
HTTP-probes the configured readiness URL, atomically swaps the
proxy backend pointer, and SIGTERMs the old child within the
configured shutdown grace. Success line:
`hush: supervise: reload: ok (readiness <ms>, strategy http-proxy)`.

Plain (non-handoff) supervisors return `config-invalid` here — they
are not affected and continue to restart via the standard cycle. See
[`docs/SUPERVISE-RELOAD.md`](SUPERVISE-RELOAD.md) for the operator
runbook, config matrix, failure modes, and audit event shape.

---

## 3. Operational truths

- the supervisor owns session continuity
- the child owns workload execution
- a daemon crash should usually not require a new phone approval
- a stale credential should always surface clearly
- the vault server must stay Tailscale-only
- Discord approval failure blocks new sessions; it does not weaken policy

---

## 4. Structured error reference

Every user-facing failure in the guided flow routes through a typed
sentinel in `internal/cli/setup/errors.go`. Each one carries a
copy-pasteable `RemedyHint()`. If you see one of these in the wild, the
remedy below is exactly what the binary prints.

| Sentinel | Meaning | Remedy hint |
|----------|---------|-------------|
| `ErrTokenAbsent` | Discord bot token cannot be located in the Keychain and `HUSH_DISCORD_BOT_TOKEN` is empty. | Run `hush init server` and follow the guided flow to store the bot token, or export `HUSH_DISCORD_BOT_TOKEN` before retrying. |
| `ErrTokenDenied` | Keychain item exists but the OS denied the read (exit 51 / errSecAuthFailed / errSecInteractionNotAllowed). | Run `hush keychain doctor` to confirm the denial, then `hush keychain repair` to refresh the ACL, **or** pick the delete-and-recreate option in the guided flow. Hush never silently switches to env-token mode here. |
| `ErrTokenBad` | A token was retrieved but failed structural / Discord-side validation. | Rotate the token in the Discord developer portal and re-run `hush init server` to store the new value. |
| `ErrBindConflict` | Configured listen address is off-CGNAT, already bound, or routes through a non-Tailscale interface. | Pick an unused Tailscale CGNAT address (`tailscale ip -4`) with a free port and re-run `hush init server`. |
| `ErrStateStale` | Partial config / vault / state-dir artifacts the classifier marks `repairable`. | Re-run `hush init server` and pick reuse / repair / archive per artifact, or pass `--on-existing=archive` for the non-interactive path. |
| `ErrArtifactCollision` | An existing artifact maps 1:1 to one the guided flow is about to create but has incompatible contents. | Archive the colliding artifact to `<path>.bak-<RFC3339>` via the archive option, or move it aside manually before re-running. |
| `ErrClockUnsynchronised` | Host clock is outside the allowed skew window. | Platform-aware: `sudo sntp -sS time.apple.com` (Darwin), `sudo chronyc makestep` or `sudo ntpdate -u pool.ntp.org` (Linux). Override knowingly with `--allow-clock-skew`. |
| `ErrKeychainPermissionDenied` | Generic "OS denied" verdict on a non-token Keychain item. | Open Keychain Access, locate the offending item, and grant the current hush binary path read access; then re-run. |

The sentinels are stable: callers can branch on them via `errors.Is`.

---

## 5. Advanced / non-interactive setup

The guided flow is the default. `--non-interactive` is the explicit
opt-out for scripted / test / CI callers. In non-interactive mode every
input must come from flags or `--input-file` (a `0600` JSON document with
the same field set the prompts populate).

Client enrollment with an explicit key file:

```bash
hush init client \
  --machine-index 1 \
  --client-registry ~/.hush/clients.json \
  --client-key-file ~/.hush/client-machine-1.key
```

When `--client-key-file` is present, hush writes the client key there and skips
macOS Keychain entirely. This is the recommended learning/smoke path because it
is deterministic and avoids Keychain prompts. Use the same `--client-key-file`
on `hush request`. If you omit `--client-key-file`, hush stores the client key
in macOS Keychain and fails closed if Keychain refuses the write.

Relevant flags for `hush init server`:

- `--non-interactive` — opt out of the guided flow.
- `--input-file <path>` — 0600 JSON bootstrap inputs.
- `--listen-addr <ip:port>` — required with `--non-interactive`.
- `--discord-owner-id`, `--discord-application-id` — required with
  `--non-interactive`; `--discord-approval-channel-id` /
  `--discord-audit-channel-id` are optional.
- `--state-dir <path>` — override the default `~/.hush` state dir
  (smoke / learning path). Interactive setup still stores the Discord
  bot token in Keychain so `hush serve` does not require a second token
  paste; the vault passphrase is not stored for explicit-state runs.
- `--on-existing prompt|reuse|repair|archive|fail` — recovery mode for
  pre-existing artifacts. Default is `prompt` interactively / `fail`
  non-interactively. `archive` renames colliding artifacts to
  `<path>.bak-<RFC3339>` before continuing.
- `--allow-clock-skew` — downgrade a failing clock-sync preflight to a
  warning. Same flag is available on `hush serve`.

Example non-interactive bootstrap from CI:

```bash
hush init server \
  --non-interactive \
  --input-file /run/secrets/hush-bootstrap.json \
  --listen-addr "$(tailscale ip -4):7743" \
  --discord-owner-id "$DISCORD_OWNER_ID" \
  --discord-application-id "$DISCORD_APP_ID" \
  --on-existing=archive
```

`--non-interactive` is the only supported path for unattended setup.
Anything that needs a TTY (secret add / list, passphrase entry) is
intentionally TTY-only as rogue-process defence.

---

## 6. Runbook index

The operational topics every hush install eventually needs:

- First-time `hush init` bootstrap (see §1)
- Vault server start / stop / reload
- Client registration and machine-index assignment
- Interactive session request workflow (`hush request`)
- Daemon supervisor deployment — one supervisor TOML per long-running daemon
- Zero-downtime daemon reload for HTTP services (see
  [`docs/SUPERVISE-RELOAD.md`](SUPERVISE-RELOAD.md))
- Vault secret rotation (`hush secret rotate`)
- Vault root-key rotation (`hush vault rekey`, see
  [`docs/VAULT-REKEY.md`](VAULT-REKEY.md))
- `hush client renew` flow for fresh daemon re-approval
- `hush client refresh` flow for secret-only refill after rotation
- Validator failure response
- Child exit 78 (stale-credential) response
- Discord outage behaviour
- Tailscale outage behaviour
- NTP / clock-sync troubleshooting (see §1)
- Duplicate supervisor / pid-file recovery

---

## 7. Shell snippet safety (zsh-first)

Every shell snippet hush emits to the operator or ships in this repo's docs MUST be safe to paste into the default macOS shell, which is `zsh`. Bash-only constructs that crash zsh — most notably `read -p` and `read -s` — are forbidden in:

- user-facing strings printed by the CLI (e.g. `hush init server` panels, error remedies)
- every doc surface a setup reader hits (`docs/*.md`, `README.md`)

A guard test (`internal/cli/setup/snippets_test.go::TestZshSafeSnippetsGuard`) scans those surfaces and fails CI on any unallowlisted match. The rule exists because the T-273 Hush 101 incident showed that operators land in `zsh` immediately after `hush init server`, and a single bash-only snippet in a fallback instruction instantly bricks the first interaction.

Zsh-safe alternatives:

- prompt + line read: `printf '%s ' 'prompt:'; read REPLY`
- no-echo read: prefer a separate `stty -echo`/`stty echo` block, or call the operator into hush's own no-echo prompt seam rather than recommending shell-level no-echo
- raw read: `IFS= read -r REPLY` (works on both zsh and bash)

If a doc surface legitimately needs to *describe* the forbidden constructs (e.g. when explaining the rule itself), add the file + substring to the `zshGuardAllowlist` in `snippets_test.go`. The OPERATIONS.md prose that introduces this rule is itself the canonical example: the line above that says snippets must not include those two flag forms is allowlisted by exact-substring match.

---

## 8. Cross-references

- Daemon lifecycle details: [`docs/LIFECYCLE-SCENARIOS.md`](LIFECYCLE-SCENARIOS.md)
- Vault root-key rotation (passphrase change): [`docs/VAULT-REKEY.md`](VAULT-REKEY.md)
- Zero-downtime HTTP reload runbook: [`docs/SUPERVISE-RELOAD.md`](SUPERVISE-RELOAD.md)
- Config locations and fields: [`docs/CONFIG-SCHEMA.md`](CONFIG-SCHEMA.md)
- Supervisor pattern + validators: [`docs/DAEMONS.md`](DAEMONS.md)
- Security posture (including Keychain vs env-token positioning): [`docs/SECURITY.md`](SECURITY.md)
- Structured error sentinels: [`internal/cli/setup/errors.go`](../internal/cli/setup/errors.go)
