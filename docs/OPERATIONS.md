# Operations

## Purpose

This file defines the runtime operating posture for hush.

Phase 0 does not need every final runbook, but it does need the operating model to be explicit and current.

---

## Day-to-day modes

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

## Operational truths

- the supervisor owns session continuity
- the child owns workload execution
- a daemon crash should usually not require a new phone approval
- a stale credential should always surface clearly
- the vault server must stay Tailscale-only
- Discord approval failure blocks new sessions; it does not weaken policy

---

## Bootstrap checklist

Before implementation deepens:

- repo remains private
- constitution is ratified
- core docs are present and cross-linked
- package layout is explicit
- config schema is explicit
- supervisor model is documented before code complexity grows
- implementation execution creates a real `tasks.yaml`

---

## Required runbooks for v0.1.0

These are the operational topics the final implementation must cover:

- first-time `hush init` bootstrap
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
- NTP/clock-sync troubleshooting
- duplicate supervisor / pid-file recovery

---

## Known Phase 0 operational posture

Current truth:
- docs-first hardening is still the active work
- implementation has not started yet
- `tasks.yaml` should be created when implementation execution begins
- examples and config docs must stay placeholder-safe and avoid real infrastructure values

---

## Shell snippet safety (zsh-first)

Every shell snippet hush emits to the operator or ships in this repo's docs MUST be safe to paste into the default macOS shell, which is `zsh`. Bash-only constructs that crash zsh — most notably `read -p` and `read -s` — are forbidden in:

- user-facing strings printed by the CLI (e.g. `hush init server` panels, error remedies)
- every doc surface a setup reader hits (`docs/*.md`, `README.md`)

A guard test (`internal/cli/setup/snippets_test.go::TestZshSafeSnippetsGuard`) scans those surfaces and fails CI on any unallowlisted match. The rule exists because the T-273 Hush 101 incident showed that operators land in `zsh` immediately after `hush init server`, and a single `read -p` snippet in a fallback instruction instantly bricks the first interaction.

Zsh-safe alternatives:

- prompt + line read: `printf '%s ' 'prompt:'; read REPLY`
- no-echo read: prefer a separate `stty -echo`/`stty echo` block, or call the operator into hush's own no-echo prompt seam rather than recommending shell-level no-echo
- raw read: `IFS= read -r REPLY` (works on both zsh and bash)

If a doc surface legitimately needs to *describe* the forbidden constructs (e.g. when explaining the rule itself), add the file + substring to the `zshGuardAllowlist` in `snippets_test.go`. The OPERATIONS.md prose that introduces this rule is itself the canonical example: the line above that says snippets MUST NOT include `read -p` or `read -s` is allowlisted by exact-substring match.

---

## Cross-references

- functional/acceptance scope: `docs/SPEC.md`
- daemon lifecycle details: `docs/LIFECYCLE-SCENARIOS.md`
- config locations and fields: `docs/CONFIG-SCHEMA.md`
- build sequence: `docs/IMPLEMENTATION-PLAN.md`
- security posture: `docs/SECURITY.md`
