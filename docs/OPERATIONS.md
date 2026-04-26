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
- daemon supervisor deployment for OpenClaw and Hermes
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

## Cross-references

- functional/acceptance scope: `docs/SPEC.md`
- daemon lifecycle details: `docs/LIFECYCLE-SCENARIOS.md`
- config locations and fields: `docs/CONFIG-SCHEMA.md`
- build sequence: `docs/IMPLEMENTATION-PLAN.md`
- security posture: `docs/SECURITY.md`
