# hush

Discord-gated secrets broker for AI agents.

## Mission

hush eliminates file-based secret exposure on agent machines.

- Secrets live encrypted on a vault host
- Access requires Discord approval from Z's phone
- Delivery happens over Tailscale
- Secrets land in process memory, not on disk

## Phase 0

Phase 0 is the bootstrap foundation.

It exists to make implementation fast, correct, and spec-driven:

- crystal-clear mission
- hard security boundaries
- explicit architecture
- supervisor-first daemon model
- docs that keep future work on rails

## Core Operating Model

### Interactive humans
Use `hush request --exec "zsh"` to wrap a shell for the day.

### Daemons
Use `hush supervise` for OpenClaw, Hermes, and future long-running agents.

### Source of truth
- Spec and constitution define the system
- Code implements the spec
- Tests prove the security properties

## Repo Shape

- `cmd/hush/` → CLI entrypoint
- `internal/` → implementation packages
- `docs/` → architecture, security, API, operations, SDD guidance
- `deploy/` → launchd/systemd/install examples

## Phase 0 Deliverables

- strong bootstrap docs
- constitution ratified
- repo layout established
- implementation phases aligned to the approved plan
- no ambiguity about threat model, lifecycle model, or scope boundaries
