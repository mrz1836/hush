# hush

Discord-gated secrets broker for AI agents.

hush is a private, Tailscale-only system for keeping secrets off agent machines while still letting humans and daemons access them when needed.

The core model is simple:
- secrets live encrypted on one vault host
- fresh access requires Discord approval from Z's phone
- approved sessions are short-lived and tightly scoped
- secret values are delivered into process memory only
- long-running daemons use a supervisor so restarts do not spam approval prompts

This repo is being prepared so Claude Code, SpecKit, and other implementation agents can build hush from the docs without guessing.

---

## Why hush exists

Commodity malware does not need to be smart.

When untrusted code lands on a machine, it usually starts by scanning obvious places:
- `~/.zshrc`
- `~/.config/gh/hosts.yml`
- `~/.aws/credentials`
- `.env`
- `*.pem`, `*.key`, `signing.key`

AI agents increase this risk because they execute generated shell commands, install packages, and explore repos autonomously.

hush removes that attack surface from agent machines.

---

## Core guarantees

### 1. No secrets persist on agent machines
Agent machines must not have usable credentials at rest.

### 2. Approval is human and out-of-band
Every fresh session is approved from Discord on Z's phone.

### 3. The vault server is never public
The vault is reachable only on the Tailscale mesh.

### 4. Daemons do not re-prompt on every restart
`hush supervise` preserves session continuity across child restarts within the approved TTL.

### 5. Stale credentials fail loudly
Validators, exit code contracts, status sockets, and Discord alerts make auth drift visible.

---

## Primary usage modes

### Interactive human usage
Use `hush request --exec "zsh"` to wrap a shell session.

Intent:
- one approval covers a bounded work session
- tools inside that shell inherit env vars
- tool crashes do not require fresh approval if the shell remains alive

### Daemon usage
Use `hush supervise --config ~/.hush/supervisors/<name>.toml`.

Intent:
- one approval covers a bounded daemon session
- child crashes/restarts do not trigger fresh Discord prompts
- supervisor owns auth state, refresh timing, validation, and visibility

---

## What hush is not

hush is not:
- a public secrets service
- a bidirectional sync system
- a generic secrets dashboard
- a proxy that permanently fronts provider APIs
- a way to eliminate human approval

---

## Repo layout

### Runtime code
- `cmd/hush/` → CLI entrypoint
- `internal/config/` → config loading, validation, defaults
- `internal/keys/` → BIP32 derivation and client key management
- `internal/vault/` → encrypted vault file, secure bytes, load/save/reload
- `internal/token/` → JWT issue/validate/revoke/session bookkeeping
- `internal/transport/` → ECIES encryption, request signing, replay protection
- `internal/server/` → HTTP handlers and middleware
- `internal/discord/` → approval bot and audit messaging
- `internal/supervise/` → daemon supervisor state machine
- `internal/cli/` → cobra commands and shared CLI glue
- `internal/logging/` → structured logging setup

### Documentation
- `docs/SPEC.md` → build-grade product and implementation spec
- `docs/ARCHITECTURE.md` → component model and trust boundaries
- `docs/API.md` → HTTP and local socket schemas
- `docs/SECURITY.md` → threat model and security requirements
- `docs/OPERATIONS.md` → bootstrap, runtime, outage, and lifecycle runbooks
- `docs/SDD-GUIDE.md` → how SpecKit / Claude Code should build this repo
- `docs/MVP.md` → v0.1.0 boundaries and exit criteria
- `docs/PACKAGE-MAP.md` → file/package responsibility map
- `docs/CONFIG-SCHEMA.md` → config formats and exact field definitions
- `docs/LIFECYCLE-SCENARIOS.md` → expected end-to-end state transitions
- `docs/IMPLEMENTATION-PLAN.md` → phased execution order and verification map
- `docs/TESTING-STRATEGY.md` → required tests, fuzzing, and coverage targets

### Deployment examples
- `deploy/examples/supervisors/` → supervisor config examples for OpenClaw and Hermes

---

## Recommended reading order for implementation agents

1. `README.md`
2. `.specify/memory/constitution.md`
3. `docs/SPEC.md`
4. `docs/ARCHITECTURE.md`
5. `docs/SECURITY.md`
6. `docs/API.md`
7. `docs/CONFIG-SCHEMA.md`
8. `docs/PACKAGE-MAP.md`
9. `docs/LIFECYCLE-SCENARIOS.md`
10. `docs/IMPLEMENTATION-PLAN.md`
11. `docs/TESTING-STRATEGY.md`
12. `docs/SDD-GUIDE.md`

---

## Phase 0 status

Phase 0 is the documentation and architecture hardening phase.

Phase 0 is complete when:
- the threat model is unambiguous
- the supervisor model is unambiguous
- package responsibilities are explicit
- config and API schemas are explicit
- lifecycle scenarios are concrete enough to implement against
- implementation order and testing gates are explicit
- SpecKit/Claude Code can build phase-by-phase without inventing behavior

The Phase 0 doc set now exists. The remaining work is consistency hardening and then moving into implementation with a real `tasks.yaml`.

---

## MVP summary

The v0.1.0 goal is not “a compiled binary.”

The goal is a working private system that proves:
- secrets stay off agent disks
- Discord approval gates fresh access
- short-lived scoped sessions work
- daemon supervisors survive restarts cleanly within an approved session
- stale credentials are visible to both Z and downstream agents

See `docs/MVP.md` and `docs/SPEC.md` for exact scope.
