# Specification

## Product

hush: Discord-gated secrets broker for AI agents.

## Core guarantee

No secrets persist on agent machines.

## Primary users

- Z approving access from phone
- OpenClaw and Hermes as daemon consumers
- interactive AI-assisted development shells

## Acceptance anchors

### Security
- encrypted vault
- no key files on disk
- human approval for fresh sessions
- Tailscale-only reachability
- signed and scoped sessions

### Lifecycle
- daemon restart resilience within approved session TTL
- validator-based stale credential detection
- local status visibility for agents
- explicit refresh behavior

## Build strategy

Follow the approved T-173 plan phase-by-phase.
Phase 0 exists to make every later phase straightforward.
