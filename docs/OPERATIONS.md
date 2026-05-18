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

### The one-command path

```bash
hush init server          # guided / interactive (default)
hush serve                # binds Tailscale interface, brokers approvals
```

`hush init server` is the bootstrap entry point for the vault host. It is
**guided by default**: the command runs a diagnostic-first preflight
pipeline, prompts for the inputs it actually needs, and never silently
overwrites pre-existing state.

You do **not** need to understand the macOS Keychain, the Discord bot
token storage layout, or the file-mode contract before running it — the
guided flow walks each decision interactively and explains the
consequences before any destructive choice.

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
   hush-authored explanation panel that names the item, says what is
   being stored, and tells you what to click in the Apple prompt that
   follows. The bare Apple "password data for new item" prompt never
   appears without that explanation immediately above it.
6. **Bootstrap completion.** `config.toml`, `secrets.vault`, the state
   dir, and (on macOS) the `hush-discord` Keychain item all exist with
   the required modes.

When it finishes, run `hush serve` and you are ready to enroll an agent
host (`hush init client …`).

### Keychain ACL denial during setup

If the existing `hush-discord` Keychain item is present but macOS refuses
the read (Darwin exit 51 / `errSecAuthFailed` /
`errSecInteractionNotAllowed`), hush surfaces a recovery panel with three
options and **never silently switches to env-token mode**:

| Choice | What it does |
|--------|--------------|
| `[1]` ACL repair | Prints the exact `security set-generic-password-partition-list` command to fix the ACL. Re-runs only the Keychain check afterwards. |
| `[2]` Delete + recreate | Destructive. Removes the existing item and stores a new one. Requires typing `delete` to confirm. The deletion is audit-logged. |
| `[3]` Env-token fallback | Skips the Keychain write and instructs you to export `HUSH_DISCORD_BOT_TOKEN` before `hush serve`. Supported, but secondary — use Keychain when possible. See `docs/SECURITY.md` §2.4 for the trade-off. |
| `[q]` Quit | Exits cleanly without modifying anything. |

Each branch is documented in detail in `docs/SECURITY.md` §2.4 ("Bot token
storage") so the security trade-offs are kept next to the threat model.

### Clock-sync failure during setup

If the preflight clock-sync check fails, hush prints the platform-aware
fix command (e.g. `sudo sntp -sS time.apple.com` on macOS) and exits
non-zero. Hush will **never** run `sudo` on your behalf.

If you have knowingly accepted clock skew (e.g. a deliberately air-gapped
test host), pass `--allow-clock-skew` to `hush init server` (and to
`hush serve`). The override downgrades the failure to a warning and
emits an audit event `clock_skew_override`.

---

## 2. Day-to-day modes

### Interactive work

Approve a session, wrap a shell, work inside it.

Primary path:
- `hush request --exec "zsh"`

Intent:
- one approval covers a bounded human work session
- tools inherit env vars from the wrapped shell
- secrets do not persist after the shell exits

### Daemon work

Run `hush supervise` under launchd/systemd.

Primary path:
- `hush supervise --config ~/.hush/supervisors/<name>.toml`

Intent:
- supervisor owns session continuity
- child restarts do not normally require a new phone approval
- refreshes happen in waking hours

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
| `ErrTokenDenied` | Keychain item exists but the OS denied the read (exit 51 / errSecAuthFailed / errSecInteractionNotAllowed). | Repair the ACL via `security set-generic-password-partition-list -S apple-tool:,apple: -s hush-discord -a <account>` and re-run, **or** pick the delete-and-recreate option in the guided flow. Hush never silently switches to env-token mode here. |
| `ErrTokenBad` | A token was retrieved but failed structural / Discord-side validation. | Rotate the token in the Discord developer portal and re-run `hush init server` to store the new value. |
| `ErrBindConflict` | Configured listen address is off-CGNAT, already bound, or routes through a non-Tailscale interface. | Pick an unused Tailscale CGNAT address (`tailscale ip -4`) with a free port and re-run `hush init server`. |
| `ErrStateStale` | Partial config / vault / state-dir artifacts the classifier marks `repairable`. | Re-run `hush init server` and pick reuse / repair / archive per artifact, or pass `--on-existing=archive` for the non-interactive path. |
| `ErrArtifactCollision` | An existing artifact maps 1:1 to one the guided flow is about to create but has incompatible contents. | Archive the colliding artifact to `<path>.bak-<RFC3339>` via the archive option, or move it aside manually before re-running. |
| `ErrClockUnsynchronised` | Host clock is outside the allowed skew window. | Platform-aware: `sudo sntp -sS time.apple.com` (Darwin), `sudo chronyc makestep` or `sudo ntpdate -u pool.ntp.org` (Linux). Override knowingly with `--allow-clock-skew`. |
| `ErrKeychainPermissionDenied` | Generic "OS denied" verdict on a non-token Keychain item. | Open Keychain Access, locate the offending item, and grant `/usr/local/bin/hush` read access; then re-run. |

The sentinels are stable: callers can branch on them via `errors.Is`.

---

## 5. Advanced / non-interactive setup

The guided flow is the default. `--non-interactive` is the explicit
opt-out for scripted / test / CI callers. In non-interactive mode every
input must come from flags or `--input-file` (a `0600` JSON document with
the same field set the prompts populate).

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

## 6. Bootstrap checklist

Before implementation deepens:

- repo remains private
- constitution is ratified
- core docs are present and cross-linked
- package layout is explicit
- config schema is explicit
- supervisor model is documented before code complexity grows
- implementation execution creates a real `tasks.yaml`

---

## 7. Required runbooks for v0.1.0

These are the operational topics the final implementation must cover:

- first-time `hush init` bootstrap (see §1)
- vault server start/stop/reload
- client registration / machine-index assignment
- interactive session request workflow
- daemon supervisor deployment (one supervisor TOML per long-running daemon)
- vault secret rotation
- `hush client refresh` flow after rotation
- validator failure response
- child exit 78 response
- Discord outage behavior
- Tailscale outage behavior
- NTP/clock-sync troubleshooting (see §1)
- duplicate supervisor / pid-file recovery

---

## 8. Known Phase 0 operational posture

Current truth:
- docs-first hardening is still the active work
- implementation has not started yet
- `tasks.yaml` should be created when implementation execution begins
- examples and config docs must stay placeholder-safe and avoid real infrastructure values

---

## 9. Shell snippet safety (zsh-first)

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

## 10. Cross-references

- functional/acceptance scope: `docs/SPEC.md`
- daemon lifecycle details: `docs/LIFECYCLE-SCENARIOS.md`
- config locations and fields: `docs/CONFIG-SCHEMA.md`
- build sequence: `docs/IMPLEMENTATION-PLAN.md`
- security posture (including Keychain vs env-token positioning): `docs/SECURITY.md`
- structured error sentinels: `internal/cli/setup/errors.go`
