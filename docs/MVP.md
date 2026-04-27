# MVP

## v0.1.0 objective

Ship a working private hush system that proves:

- secrets can stay off agent disks
- Discord can gate fresh access cleanly
- short-lived scoped sessions work
- supervisor lifecycle solves daemon restart pain
- stale credentials are visible to both the operator and agents

This is a private-system MVP first, not a public-launch MVP.

## In MVP

- single `hush` binary with the scoped command set in `docs/SPEC.md`
- encrypted vault at rest on one trusted host
- Discord approval flow for fresh interactive and supervisor sessions
- ES256K JWT sessions with scope, TTL, IP binding, and interactive use limits
- ECIES-encrypted secret delivery
- `hush request --exec` shell/process injection flow
- `hush supervise` daemon lifecycle for any long-running daemon (the operator
  configures one supervisor TOML per daemon)
- validator-based stale-credential detection
- local status socket plus `hush client status` / `hush client refresh`
- atomic vault rotation + reload path
- launchd/systemd-ready deployment posture

## Not in MVP

- public release polish beyond the private-repo quality gate
- multi-owner approvals
- Slack/PagerDuty fallback
- proxy-mode secrets access
- agent-side credential proxy
- `SO_PEERCRED` per-caller socket identity
- bidirectional sync/dashboard magic
- web UI

## MVP bar

The MVP is not done because the binary compiles.
It is done when the security properties and lifecycle properties are both real.

The authoritative MVP exit gate is the acceptance criteria in `docs/SPEC.md` plus the test strategy in `docs/TESTING-STRATEGY.md`.
