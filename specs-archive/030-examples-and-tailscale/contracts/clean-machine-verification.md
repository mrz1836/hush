# Contract: CLEAN-MACHINE.md Verification Matrix

**Branch**: `030-examples-and-tailscale` | **Date**: 2026-05-14

This contract is the verify-and-polish audit for `docs/CLEAN-MACHINE.md`
per FR-010 / SC-006 / SC-007. The /speckit-tasks task T4 walks this
table top to bottom; the post-/speckit-implement gate is: every row's
"Status" cell is `OK` or has an applied patch.

The authoritative cross-reference is `deploy/install.sh` (SDD-29).
Constitution Principle I and the constitution's Security Requirements
row "Keychain ACLs (macOS)" are also referenced where applicable.

---

## Audit table

Each row checks one claim in CLEAN-MACHINE.md against the
authoritative sources. Status values: `OK` (no edit needed),
`PATCH` (specific patch applied in this chunk), `WONTFIX` (out of
scope; deferred to a later chunk with rationale).

| # | CLEAN-MACHINE.md claim | Authoritative source(s) | Status |
|---|------------------------|------------------------|--------|
| 1 | "Constitution Principle I requires zero secret files at rest on agent machines" (preamble) | Constitution Principle I: "Agent machines MUST have zero secrets on disk" | OK — exact match |
| 2 | "Move them into the vault on the trusted host via `hush secret add` (interactive TTY only)" (Pre-flight §1) | SPEC.md FR-2 + FR-10 (vault writes are interactive TTY only); install.sh has no `hush secret add` invocation (operator-side command) | OK — install.sh's role is daemon plumbing, not vault content; the doc correctly punts to the operator's interactive `hush secret add` |
| 3 | "Confirm the agent machine has Tailscale running and is reachable from the vault host on port 7743" (Pre-flight §2) | TAILSCALE-ACLS.md §Verification (curl on 7743); install.sh creates no Tailscale state | OK — pre-flight check is operator-side, install.sh-independent |
| 4 | "Confirm `hush init --client --machine-index N` has been run on the agent" (Pre-flight §3) | SPEC.md FR-3 (per-agent client key BIP32 path); install.sh installs the binary but does not run `hush init --client` | OK — pre-flight check is operator-side |
| 5 | §1 Shell dotfiles — grep + manual edit guidance | (operator hygiene; install.sh does not touch dotfiles) | OK — install.sh-independent |
| 6 | §2 GitHub CLI — `gh auth logout` guidance | (operator hygiene; install.sh does not interact with gh) | OK — install.sh-independent |
| 7 | §2 AWS CLI — `rm ~/.aws/credentials` guidance | (operator hygiene; install.sh does not interact with AWS state) | OK — install.sh-independent |
| 8 | §2 Anthropic/OpenAI/Google AI — env-var-only guidance | (operator hygiene; consistent with hush's env-var injection model) | OK — install.sh-independent |
| 9 | §2 Docker registry creds — `~/.docker/config.json` guidance | (operator hygiene; install.sh does not touch Docker) | OK — install.sh-independent |
| 10 | §3 `.env` files — find + review guidance | (operator hygiene) | OK |
| 11 | §4 Key files — `*.key`/`*.pem`/SSH keys guidance | (operator hygiene) | OK |
| 12 | §5 Cron / launchd / systemd unit env files | (operator hygiene; install.sh DOES install launchd plist `/Library/LaunchDaemons/hush.plist` on macOS and systemd unit `/etc/systemd/system/hush.service` on Linux — but these are non-secret, just service-management metadata; "EnvironmentVariables" is not used by hush.plist for secret injection) | OK — claim is general operator hygiene; the installed plist/unit is non-secret |
| 13 | §6 Editor / IDE plugin caches | (operator hygiene) | OK |
| 14 | §7 Browser / extension storage — explicit out-of-scope | (operator hygiene) | OK |
| 15 | §8 macOS Keychain — "hush-managed entries — keep these. They are ACL-restricted to `/usr/local/bin/hush` and used to derive client keys at runtime." | install.sh banner names entries `hush-vault-passphrase` (vault passphrase) for ACL-bound use; `hush-discord` (bot token; in server config) and `hush-client` (per-machine client key) are referenced in constitution Security Requirements row | **PATCH — R-003** (enumerate the three actual entries by name) |
| 16 | §8 "Tool-specific entries (e.g. gh-cli, git, AWS CLI) — these may bypass the file-based credential stores..." | (operator hygiene; install.sh does not touch these) | OK |
| 17 | §9 Shell history — grep + redact guidance | (operator hygiene) | OK |
| 18 | §10 Team / shared notes | (operator hygiene; install.sh-independent) | OK |
| 19 | §Verification — final re-scan grep | (operator hygiene check; install.sh-independent) | OK |
| 20 | §Re-runnability — "Re-run sections 1, 2, 3, 4 monthly" | (operator workflow; install.sh-independent) | OK |
| 21 | Cross-references block (footer) | All references resolve (SECURITY.md, DAEMONS.md, SPEC.md, TAILSCALE-ACLS.md, constitution.md) | OK |
| 22 | install.sh's macOS banner step 1 ("security add-generic-password ... -s 'hush-vault-passphrase' -T '/usr/local/bin/hush' ...") is mentioned only by reference in CLEAN-MACHINE.md §8; the binary path + ACL flag are NOT cited verbatim | install.sh banner is the source-of-truth for the exact security(1) invocation | **PATCH — R-003** (add a short verbatim cross-reference to the installer banner for operators who hit §8 first) |
| 23 | install.sh installs binary at `/usr/local/bin/hush` (default PREFIX `/usr/local`) | CLEAN-MACHINE.md does not contradict this; the per-binary ACL line `-T /usr/local/bin/hush` in install.sh's banner is the operator-facing reference | OK |
| 24 | install.sh creates state directory at `/usr/local/var/hush` (macOS) / `/var/lib/hush` (Linux) with mode `0700` owned by `_hush`/`hush` user | CLEAN-MACHINE.md does not reference state-dir cleanup (it's vault-host state, not agent-host) — out of scope for "clean agent machine" | OK — CLEAN-MACHINE.md correctly targets agent hosts; install.sh's state dir is on the vault host |
| 25 | install.sh's macOS Time Machine exclusion (`tmutil addexclusion`) per Constitution XI | CLEAN-MACHINE.md does not mention Time Machine — and shouldn't, because the exclusion targets the vault host's state dir, not agent hosts | OK |
| 26 | Operator-specific-identifier grep (FR-011) | seed list per FR-007 | OK — CLEAN-MACHINE.md uses only generic tool names (`gh`, `aws`, `docker`, etc.); no operator-specific identifiers |

---

## Patch specification (R-003)

The /speckit-tasks T4 task applies the following patches to
`docs/CLEAN-MACHINE.md`. Each patch is a minimal, focused edit
targeting one of the rows above marked **PATCH**.

### Patch 1 — Rewrite §8 macOS Keychain hush-managed bullet

**Before:**

```markdown
Two cases:

- **hush-managed entries** — keep these. They are ACL-restricted to
  `/usr/local/bin/hush` and used to derive client keys at runtime.
- **Tool-specific entries** (e.g. `gh-cli`, `git`, AWS CLI) — these may
  bypass the file-based credential stores you just cleaned up. If a tool
  caches a token in Keychain at login time, decide whether to:
  - Disable the tool's Keychain integration (preferred for hush model).
  - Leave it (acceptable if the Keychain ACL prevents non-tool processes
    reading it; verify with `security dump-keychain` and Access Control
    panel).
```

**After:**

```markdown
Two cases:

- **hush-managed entries** — keep these. They are ACL-restricted to
  `/usr/local/bin/hush` (per `-T /usr/local/bin/hush` on
  `security add-generic-password`; see `deploy/install.sh`'s
  next-steps banner for the exact invocation). The three canonical
  entries are:
  - `hush-vault-passphrase` — vault passphrase, on the **vault host**
    only. Created by the operator with
    `security add-generic-password -a <hush-user> -s hush-vault-passphrase
    -T /usr/local/bin/hush -U -w '<passphrase>'` after `deploy/install.sh`
    completes (the installer prints the exact command in its banner).
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
```

The three-entry enumeration is the entire change. Item names match
`deploy/install.sh`'s banner output exactly; the constitution's
Security Requirements row remains correct as written (it references
"the passphrase entry" and "the `hush` binary path", which describes
the ACL constraint, not the item name).

---

## Final verification gate (post-patch)

After patch 1 is applied, every row in the audit table reads `OK`.
The /speckit-implement step 4 manual cross-doc check confirms:

- `deploy/install.sh` banner (macOS step 1) ↔ CLEAN-MACHINE.md §8
  Keychain enumeration: aligned (`hush-vault-passphrase` named
  consistently).
- Constitution Security Requirements "Keychain ACLs (macOS)" ↔
  CLEAN-MACHINE.md §8: aligned (both reference the per-binary ACL
  contract).
- Operator-specific identifier grep (FR-011): zero matches in
  CLEAN-MACHINE.md after patch.

SC-006 ("zero contradictions between CLEAN-MACHINE.md and
install.sh") is satisfied. SC-007 ("zero operator-specific
identifier matches in CLEAN-MACHINE.md") is verified by the same
mechanism as TAILSCALE-ACLS.md (manual grep documented in tasks.md,
or extension of `TestExamples_NoOperatorSpecificNames` per
/speckit-tasks).

---

## Out-of-scope items (deferred to a future chunk if discovered)

- **Linux Keychain equivalent.** install.sh's Linux banner says
  "Provision the vault passphrase via the operator's chosen secret
  mechanism (systemd LoadCredential, vault-aware launcher, etc.)" —
  no Keychain on Linux. CLEAN-MACHINE.md §8 is macOS-specific
  ("macOS Keychain (if applicable)"). No alignment work needed for
  Linux in this chunk.
- **`/Users/.../.hush/clients.json` cleanup.** This file is a server
  artefact, not an agent artefact; lives on the vault host only.
  CLEAN-MACHINE.md's agent-host focus correctly omits it.
- **Auditing the per-machine client-key entry name.** SPEC.md FR-3
  documents BIP32 path `m/44'/7743'/3'/{machine_index}` for client
  keys; CLEAN-MACHINE.md §8 references the resulting Keychain entry
  as `hush-client`. The constitution row "Keychain ACLs (macOS)"
  documents three entries (`hush`, `hush-discord`, `hush-client`)
  but the renaming of `hush` → `hush-vault-passphrase` is encoded
  in install.sh, not constitution. R-003 leaves the constitution
  untouched per scope.
