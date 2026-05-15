# Quickstart: Operator copy/paste workflow for a new daemon

**Branch**: `030-examples-and-tailscale` | **Date**: 2026-05-14

This quickstart is the operationalisation of User Story 1 in
[spec.md](./spec.md): a new operator finds the canonical template,
copies it, find/replaces the placeholders, and launches `hush
supervise` without further reference material.

The quickstart targets an operator who has already:

- Set up a vault host via `hush init` (vault file at `~/.hush/secrets.vault`
  on the vault host; secrets added via `hush secret add NAME`).
- Set up an agent host via `hush init --client --machine-index N`
  (per-machine client key registered with the vault).
- Confirmed Tailscale is running and the agent host can `curl
  http://<vault-tailscale-ip>:7743/h/<prefix>/hz` and get a 200.

If any of those preconditions is unmet, follow the bootstrap doc
chain (`docs/OPERATIONS.md` for vault bootstrap;
`docs/CLEAN-MACHINE.md` Pre-flight section for agent bootstrap)
first.

---

## The five steps

### Step 1 — Open the public hush repository

```bash
# Replace <your-ref> with the tag or branch you're consuming (e.g., v0.1.0).
git clone https://github.com/<your-org>/hush /tmp/hush && cd /tmp/hush
# Or browse on the web:
#   https://github.com/<your-org>/hush/blob/<your-ref>/deploy/examples/supervisors/example-daemon.toml
```

The canonical template lives at:

```text
deploy/examples/supervisors/example-daemon.toml
```

Read the top-of-file comment block first. It links to:
- `docs/CONFIG-SCHEMA.md#supervisor-config` (authoritative schema)
- `docs/TAILSCALE-ACLS.md` (network-layer hardening)
- `docs/CLEAN-MACHINE.md` (agent-host hygiene; Keychain ACL contract for AC-6)

### Step 2 — Copy the template to your supervisors directory

On the **agent host** (the machine that will run `hush supervise`):

```bash
mkdir -p ~/.hush/supervisors
cp /tmp/hush/deploy/examples/supervisors/example-daemon.toml \
   ~/.hush/supervisors/my-daemon.toml
chmod 0640 ~/.hush/supervisors/my-daemon.toml
```

Rename the file from `example-daemon.toml` to `<your-daemon>.toml`
(URL/path-safe slug — same constraint as the in-file `name` field).

### Step 3 — Find/replace the placeholders

Open `~/.hush/supervisors/my-daemon.toml` in your editor and substitute
each placeholder. The template carries inline comments naming the
loader default for every optional field, so you can safely delete
optional entries that you accept the default for.

| Placeholder | Substitute with | Example |
|-------------|-----------------|---------|
| `example-daemon` | Your daemon's slug | `my-daemon` |
| `Example long-running daemon` | One-line reason shown in Discord DMs | `"my-daemon — production agent runtime"` |
| `http://100.64.0.1:7743/h/example` | Your vault's Tailscale URL + path-prefix | `http://100.96.10.4:7743/h/a8k2f9` (use values from your `hush init` output) |
| `your-daemon-binary` | Absolute path to your daemon binary | `/usr/local/bin/my-daemon` |
| `EXAMPLE_API_KEY_1`, `EXAMPLE_API_KEY_2` | Your real secret names (must be in the vault on the vault host) | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` |
| `Example Daemon` | Nicer label in Discord DMs/alerts | `"My Daemon"` |
| `REPLACE_ME` (`[discord].alert_channel_id`) | Your Discord alert channel snowflake | `"234567890123456789"` |

Optional fields you may want to tune:
- `refresh_window` — local-time window when the daily Discord refresh
  DM fires (default `09:00-10:00`).
- `cache_secrets_for_restart` + `cache_grace_ttl` — opt-in grace cache
  for restart resilience (default `true` + `60m` in the template; flip
  `cache_secrets_for_restart` to `false` if you want hard re-prompt on
  every crash).
- `[watchdog].patterns` — extra log-fragment strings to alert on
  (default `["401 Unauthorized", "No API key found", "invalid x-api-key"]`).

Optional fields you usually leave at the default:
- `requested_ttl` (20h).
- `boot_retry_timeout` (10m).
- `refresh_nudge_before` (30m).
- `[child].restart_on_clean_exit` (true).
- `[child].restart_on_exit_78` (false — see the inline comment; flipping
  is dangerous).

### Step 4 — Set up the Keychain ACL (macOS agent hosts only)

This is the AC-6 contract the template's top-of-file comment block
points at. The per-binary Keychain ACL means hush is the only binary
that can read the agent's local secrets-broker passphrase.

```bash
# On the agent host (macOS only):
security add-generic-password \
  -a "$USER" -s "hush-client" \
  -T "/usr/local/bin/hush" \
  -U -w "<your-agent-passphrase>"
```

The `-T /usr/local/bin/hush` flag is the ACL — only `/usr/local/bin/hush`
can read this Keychain entry without a prompt. The path MUST match
`[child].command[0]` in your supervisor TOML if your daemon also
uses Keychain entries with the same ACL.

See `docs/CLEAN-MACHINE.md` §8 for the full enumeration of canonical
Keychain entries.

### Step 5 — Launch the supervisor

```bash
hush supervise --config ~/.hush/supervisors/my-daemon.toml
```

What to expect on first launch:

1. The supervisor reads the TOML and validates it (this is the same
   `Load` function that the `TestExamples_GenericTOMLValidates` test
   exercises — the template is loader-clean by construction, so any
   error here points at your substitutions, not the template).
2. The supervisor connects to the vault server over Tailscale and
   requests the secrets in `scope`.
3. A Discord DM lands on your phone with `[DAEMON] my-daemon`,
   the scope, the requested TTL, and `[Approve 20h]` / `[Deny]` buttons.
4. On approval, the supervisor runs each validator
   (`[validators]` map) against the fetched secrets; any 401 from
   the provider's check endpoint raises a `[STALE] Validator Failure`
   Discord alert before the daemon ever sees the bad secret.
5. On all-green, the supervisor exec's `[child].command` with the
   secrets injected as env vars. Your daemon now runs with the
   secrets in process memory only — no files on disk on the agent.
6. The local status socket is live at `[status_socket]`; query it
   with `hush client status --supervisor my-daemon --json`.

---

## Failure modes the template catches early

The template's value choices are deliberate: each one is the
value most likely to succeed on first launch, so if you see an
error the failure mode points at *your* substitutions, not the
template.

| Symptom | Likely cause | Where to look |
|---------|--------------|---------------|
| `hush/supervise/config: unknown field: <name>` | You added a field that's not in the schema | `docs/CONFIG-SCHEMA.md#supervisor-config` |
| `hush/supervise/config: missing required field: <name>` | You deleted a required field by accident | The inline comment on the field — anything with "Required." can't be deleted |
| `hush/supervise/config: server_url must parse with http/https scheme...` | You replaced `100.64.0.1` with a malformed value (e.g., missing scheme, no port) | The inline comment on `server_url` |
| `hush/supervise/config: refresh_window must be HH:MM-HH:MM` | Typo in refresh_window (e.g., `9:00-10:00` rejected — needs leading zero) | The inline comment on `refresh_window` |
| `hush/supervise/config: child.command first element must be an absolute path` | You used a relative path for the daemon binary | The inline comment on `[child].command` |
| `hush/supervise/config: unknown validator: <name>` | You used a validator name outside the allow-list | The inline comment on `[validators]` — allow-list: anthropic, anthropic-oauth, openai, google-ai, github |
| Discord DM never arrives | Tailscale ACL not configured | `docs/TAILSCALE-ACLS.md` §Verification |
| Supervisor refuses to start with "Tailscale unreachable" | First-boot Tailscale state not yet up | The supervisor retries with exponential backoff up to `boot_retry_timeout` (default 10m) — wait, or check `tailscale status` |
| Secret resolves but daemon crashes with `EX_CONFIG` (exit 78) | The daemon believes the secret is stale | `docs/SPEC.md` AC-10 scenario 5 (exit-78 stale-credential contract) — supervisor moves to `awaiting-approval`; re-approve via DM |

---

## What the template does NOT do

The template is documentation. It does **not**:

- **Configure your Tailscale ACL.** That's `docs/TAILSCALE-ACLS.md` —
  apply the ACL pattern to your tailnet before first boot.
- **Configure your Keychain ACL.** That's step 4 above (operator runs
  `security add-generic-password ...` directly).
- **Bootstrap the vault.** Vault setup happens on the vault host via
  `hush init` — see `docs/OPERATIONS.md`.
- **Register your client key.** That's `hush init --client --machine-index N`
  on the agent host.
- **Add secrets to the vault.** That's `hush secret add NAME` on the
  vault host (interactive TTY only).
- **Set up launchd/systemd autostart.** Use
  `deploy/supervise-launch.sh.template` (SDD-29) as a starting point.

Each of these is a separate, deliberate step under operator control
— the template is the central pivot for the per-daemon supervisor
config, not the whole bootstrap workflow.

---

## When the operator copies the template into a fork

Per spec.md Edge Cases: "Because the template contains zero
operator-specific identifiers, no follow-on leakage occurs. The
operator's substitutions land in *their* fork, not in the public
template."

That property is enforced by `TestExamples_NoOperatorSpecificNames`
in `internal/supervise/config/example_test.go` (FR-007). If you find
yourself wanting to commit a real daemon name back to the public
hush repo, **stop** — that name belongs in your private overlay,
not in the public template.

---

## Cross-references

- [spec.md](./spec.md) §User Story 1 — the user story this quickstart
  operationalises.
- [data-model.md](./data-model.md) §5 — the three placeholder
  categories operators substitute (slugs, scoped secret names,
  `REPLACE_ME` markers).
- [contracts/template-field-census.md](./contracts/template-field-census.md)
  — every field the operator will see, with its inline comment.
- `docs/SPEC.md` AC-6 / AC-8 / AC-10 — the acceptance criteria this
  template exercises.
- `docs/CONFIG-SCHEMA.md` §Supervisor-config — the authoritative
  schema.
- `docs/DAEMONS.md` — operator workflow for running multiple
  daemons.
- `docs/CLEAN-MACHINE.md` §8 — Keychain ACL contract enumeration
  (post-R-003 patch).
- `docs/TAILSCALE-ACLS.md` — network-layer hardening (post-R-002
  patch).
