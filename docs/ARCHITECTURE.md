# Architecture

## Overview

hush is a private, Tailscale-only secrets broker for AI agents.

It separates:

1. **Secret custody** — encrypted vault on a single host
2. **Approval** — Discord DM approval by Z on phone
3. **Delivery** — short-lived session + scoped fetch over Tailscale
4. **Consumption** — env injection into processes only
5. **Lifecycle** — supervisor model for daemons, wrapped shell for humans

## Primary modes

### Interactive mode
`hush request --exec "zsh"`

This creates a human-approved session that injects secrets into a shell.
The shell persists across tool restarts.

### Supervisor mode
`hush supervise --config ~/.hush/supervisors/<name>.toml`

This decouples daemon restarts from repeated Discord prompts.
The supervisor owns session continuity. The child does not.

## Architectural layers

### Vault layer
- Argon2id key derivation
- AES-256-GCM encrypted vault file
- mlocked sensitive memory

### Identity and session layer
- BIP32 key hierarchy
- ES256K JWT signing
- ECDSA client request signing
- IP-bound, scoped, short-lived sessions

### Transport layer
- Tailscale network boundary
- ECIES-encrypted secret responses
- nonce + timestamp replay protection

### Control plane
- Discord approval bot
- audit chain
- token revocation
- health/status visibility

### Runtime lifecycle layer
- supervisor state machine
- validator-based stale credential detection
- Unix status socket for agent visibility
- graceful child restart path

## Phase 0 architecture goals

Before deeper implementation, the architecture must already make these truths obvious:

- the vault server is never public
- approval is always human
- secrets never persist on agent disks
- daemon lifecycle uses `supervise`, not naive `request --exec`
- staleness is surfaced proactively, not discovered by accident later
