# Operations

## Day-to-day modes

### Interactive work
Approve a session, wrap a shell, work inside it.

### Daemon work
Run `hush supervise` under launchd/systemd.

## Operational truths

- The supervisor owns session continuity
- The child owns workload execution
- A daemon crash should usually not require a new phone approval
- A stale credential should always surface clearly

## Bootstrap checklist

- repo is private
- docs are ratified
- constitution is in place
- package layout exists
- supervisor model is documented before implementation

## Future runbooks

This file will expand into:
- install/bootstrap
- vault rotation
- credential refresh
- supervisor recovery
- Discord outage behavior
- Tailscale outage behavior
- clock-sync troubleshooting
