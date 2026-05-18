# hush

> **Discord-gated secrets broker for AI agents.**
> One passphrase. No key files. No dotfiles on agent disks.

[![Build](https://github.com/mrz1836/hush/actions/workflows/fortress.yml/badge.svg)](https://github.com/mrz1836/hush/actions/workflows/fortress.yml)
[![Coverage](https://codecov.io/gh/mrz1836/hush/branch/master/graph/badge.svg)](https://codecov.io/gh/mrz1836/hush)
[![Latest Release](https://img.shields.io/github/v/release/mrz1836/hush?include_prereleases)](https://github.com/mrz1836/hush/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/mrz1836/hush)](go.mod)
[![License](https://img.shields.io/github/license/mrz1836/hush)](LICENSE)
[![govulncheck](https://img.shields.io/badge/govulncheck-clean-brightgreen)](https://github.com/mrz1836/hush/actions)
[![gitleaks](https://img.shields.io/badge/gitleaks-zero-brightgreen)](https://github.com/mrz1836/hush/actions)

**hush is a single Go binary that keeps every API key, OAuth token, and
service credential encrypted on a single trusted host. Agents request
short-lived, scoped sessions over Tailscale; the request is approved on
your phone via Discord; approved secrets are delivered ECIES-encrypted
end-to-end and injected into the agent process's environment — never
written to disk on the agent machine.**

If your dev workflow runs untrusted code (npm/pip packages, LLM-generated
scripts, AI-agent tools that execute shell commands) and your secrets
currently live in shell rc files or cloud-provider credential files, hush
is for you. Vault, 1Password CLI, and dotfile-based env vars all leave
files on disk that commodity malware grep for first. hush makes those
files not exist.

<br>

## Quick start

> **Status:** v0.1.0 is a private MVP. Treat the steps below as the
> documented happy path, not a guarantee.

Prerequisites: a vault host and an agent host on the same Tailscale
tailnet, plus a Discord bot you control
(<https://discord.com/developers/applications>) for the approval channel.

Build and install:

```bash
git clone https://github.com/mrz1836/hush.git && cd hush
magex build && sudo install -m 0755 cmd/hush/hush /usr/local/bin/hush
```

Bootstrap the vault host — **one command, then follow the prompts**:

```bash
hush init server          # guided / interactive; preflight + prompts
hush secret add OPENAI_API_KEY
hush serve
```

Enrol the agent host:

```bash
hush init client --machine-index 1
hush request \
  --server "https://<vault-host-tailscale-ip>:7743" \
  --machine-index 1 --scope OPENAI_API_KEY \
  --max-uses 1 --ttl 5m --reason "smoke test" \
  --exec "env | grep OPENAI_API_KEY"
```

Approve the Discord DM on your phone; the child process you named in
`--exec` runs with `OPENAI_API_KEY` in its environment — and only there.
Nothing is written to disk on the agent host.

`hush init server` is the canonical first-run entry point. It runs a
diagnostic-first preflight, prompts for the inputs it actually needs,
classifies pre-existing state per-artifact, and never silently overwrites.
For the full walkthrough — Keychain ACL recovery, clock-skew override,
`--non-interactive` mode — see [`docs/OPERATIONS.md`](docs/OPERATIONS.md).
For Keychain vs `HUSH_DISCORD_BOT_TOKEN` positioning and the threat
model, see [`docs/SECURITY.md`](docs/SECURITY.md) §2.4. For long-running
daemons, see [`docs/DAEMONS.md`](docs/DAEMONS.md) and
[`deploy/examples/supervisors/`](deploy/examples/supervisors/).
For server + supervisor TOML schemas, see
[`docs/CONFIG-SCHEMA.md`](docs/CONFIG-SCHEMA.md).

<br>

## At a glance

**What hush does:**

- Keeps every secret encrypted in a single AES-256-GCM + Argon2id (256MB) vault file on one trusted host.
- Requires phone approval (Discord DM with interactive buttons) for every fresh session.
- Delivers secrets ECIES-encrypted end-to-end into agent process memory only — no disk writes on the agent.

**What hush explicitly does NOT do (v0.1.0):**

- No multi-owner approvals (a single configured operator approves; multi-owner is post-v0.1.0 future scope).
- No cloud KMS / SaaS dependency. The vault is self-hosted and offline-capable.
- No public network exposure. The vault server is bound to a Tailscale interface and refuses to start otherwise.

```
                            TAILSCALE MESH
  ┌─────────────────────────────────────────────────────────────────────┐
  │                                                                     │
  │  ┌──────────────────────────┐     ┌───────────────────────────────┐ │
  │  │  AGENT MACHINE           │     │  VAULT HOST                   │ │
  │  │  (untrusted, clean disk) │     │  (mlocked memory; offline)    │ │
  │  │                          │     │                               │ │
  │  │  interactive client /    │     │  vault server                 │ │
  │  │  supervisor              │     │                               │ │
  │  │       │                  │     │       ▲                       │ │
  │  │       │ ECDSA-signed     │─────┼──────►│ verify signature      │ │
  │  │       │ claim            │     │       │ check Tailscale IP    │ │
  │  │       │                  │     │       ▼                       │ │
  │  │       │                  │     │  Discord DM ─────► phone      │ │
  │  │       │                  │     │       │  [Approve]            │ │
  │  │       │ ES256K JWT       │◄────┼───────┤ issue scoped JWT      │ │
  │  │       │                  │     │       ▼                       │ │
  │  │       │ secret fetch     │─────┼──────►│ ECIES-encrypt to      │ │
  │  │       │ (ECIES bytes)    │     │       │ ephemeral pubkey      │ │
  │  │       ▼                  │     │       │                       │ │
  │  │  decrypt → env var       │     │       └─────────────────────  │ │
  │  │  inject into child       │     │  [no key files anywhere]      │ │
  │  └──────────────────────────┘     └───────────────────────────────┘ │
  │                                                                     │
  └─────────────────────────────────────────────────────────────────────┘
```

<br>

## Why hush exists

When untrusted code lands on a developer machine — via npm/pip
supply-chain attacks, LLM-generated scripts, or trojans masquerading as
tools — the very first thing it does is enumerate known credential
patterns in known files: shell rc files, cloud-provider credential
files, `.env` files, signing keys, and PEM files.

**hush exists to make this enumeration return nothing.** Secrets stay
encrypted on a single trusted host. Agents fetch them only after a human
approves the request from a phone. Approved secrets are delivered into
process memory and zeroed when the process exits. Nothing on disk.

For the full threat model, see [`docs/SECURITY.md`](docs/SECURITY.md).

<br>

## Architecture summary

hush is a single Go binary playing three roles:

- **Vault server** — holds the encrypted vault file in mlocked memory;
  issues ES256K-signed JWTs after Discord approval; ECIES-encrypts
  secret responses to the client's per-session ephemeral public key;
  exposes a tiny HTTP API over Tailscale only.
- **Interactive client** — ECDSA-signs a claim with a per-machine
  BIP32-derived key; receives a JWT after approval; fetches and
  decrypts secrets; injects them into a child process's environment.
- **Supervisor** — long-lived per-daemon process that holds the JWT and
  ephemeral ECIES key in mlocked memory across child crashes/restarts
  within the session TTL; runs validators before child start; exposes a
  Unix status socket for agent-visible freshness queries.

**Seven security layers** stack independently — compromise of any single
layer does not enable secret extraction:

1. **BIP32 HD key hierarchy** — all keys derived at runtime from a
   single passphrase. **Zero key files on disk.**
2. **ES256K asymmetric JWT signing** — only the server can sign;
   leaking the public key does not enable forgery.
3. **ECIES end-to-end secret transport** — secrets are encrypted to a
   per-session ephemeral pubkey; captured HTTP traffic shows binary
   blobs.
4. **ECDSA-signed client requests** — every claim and revocation is
   signed with a registered per-machine client key.
5. **mlocked secure memory** — `SecureBytes` containers; secrets never
   stored as Go `string`; explicit zeroing on shutdown.
6. **Signed hash-chained audit log** — every event ECDSA-signed; chain
   breaks on modification.
7. **Obscurity** — random API path prefix, custom vault file format,
   non-obvious binary name. Additive only — never load-bearing.

The network perimeter is **Tailscale-only** (Constitution Principle VI).
Tailscale is the v0.1.0 mesh-VPN choice; the architecture does not depend
on it specifically — the requirement is "no public reachability" and
Tailscale satisfies it cleanly. The `Approver` interface is also
pluggable; **Discord is the v0.1.0 reference implementation** and the
only one that ships, but future Slack / Telegram / PagerDuty backends can
be wired without changing the rest of the system.

For the full architecture treatment, see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

<br>

## Tech stack

- **[Go 1.26+](https://go.dev/)** — single static binary, `CGO_ENABLED=0`
  exclusively (Constitution Principle IX).
- **[decred/dcrd/dcrec/secp256k1/v4](https://github.com/decred/dcrd)** —
  secp256k1 primitives used for ECDSA signing, ES256K JWTs, and ECIES
  envelope encryption (Constitution Principle III).
- **[decred/dcrd/hdkeychain/v3](https://github.com/decred/dcrd)** — BIP32
  HD key derivation from the operator passphrase (Constitution Principle
  III); paired with stdlib `golang.org/x/crypto/argon2` for the KDF.
- **[Tailscale](https://tailscale.com/)** — the only network reachable to
  the vault server. WireGuard underneath; identity-based ACLs above.
- **[Discord](https://discord.com/)** + **[discordgo](https://github.com/bwmarrin/discordgo)**
  — phone-based approval channel; the v0.1.0 reference Approver.
- **[golang-jwt/jwt v5](https://github.com/golang-jwt/jwt)** — JWT framework;
  hush registers a custom `ES256K` signing method.
- **[go-toml v2](https://github.com/pelletier/go-toml)** — strict TOML
  parsing for server and supervisor configs.
- **[zalando/go-keyring](https://github.com/zalando/go-keyring)** — OS
  keychain access with ACL support.
- **[cobra](https://github.com/spf13/cobra)** + **[pflag](https://github.com/spf13/pflag)** —
  CLI subcommand routing and flag parsing.

The `SecureBytes` mlock pattern is custom-implemented in
`internal/vault/securebytes/`; the design is inspired by
[sigil](https://github.com/mrz1836/sigil) but takes no dependency on it.
hush itself is built using the [Spec-Kit](https://github.com/github/spec-kit)
spec-driven development methodology — see
[`docs/SDD-GUIDE.md`](docs/SDD-GUIDE.md).

<br>

## Documentation

| Doc | Purpose |
|-----|---------|
| [`docs/SPEC.md`](docs/SPEC.md) | Functional requirements + acceptance criteria — single source of truth for what v0.1.0 ships |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Component model, trust boundaries, data flows, supervisor lifecycle |
| [`docs/SECURITY.md`](docs/SECURITY.md) | Threat model, 7 security layers, residual risks, crypto requirements |
| [`docs/CONFIG-SCHEMA.md`](docs/CONFIG-SCHEMA.md) | Server TOML and per-supervisor TOML schemas, defaults, validation rules |
| [`docs/PACKAGE-MAP.md`](docs/PACKAGE-MAP.md) | Go package layout, ownership, dependency rules |
| [`docs/LIFECYCLE-SCENARIOS.md`](docs/LIFECYCLE-SCENARIOS.md) | 15 named runtime scenarios — the AC-10 integration suite |
| [`docs/DAEMONS.md`](docs/DAEMONS.md) | Supervisor pattern, refresh tuning, validator authoring, grace-window tradeoff |
| [`docs/OPERATIONS.md`](docs/OPERATIONS.md) | Day-to-day modes, runbook list, operational truths |
| [`docs/TESTING-STRATEGY.md`](docs/TESTING-STRATEGY.md) | Coverage targets, fuzz targets, test layers, sentinel-leak pattern |
| [`docs/SDD-GUIDE.md`](docs/SDD-GUIDE.md) | Spec-driven development methodology used to build hush |
| [`docs/SDD-PLAYBOOK.md`](docs/SDD-PLAYBOOK.md) | At-a-glance index of the SDD chunks + status dashboard |
| [`docs/SDD-CATALOG.md`](docs/SDD-CATALOG.md) | Full catalog of chunks with ready-to-paste agent prompts |
| [`docs/AC-MATRIX.md`](docs/AC-MATRIX.md) | AC-1..AC-10 ↔ chunk ↔ test path mapping (the v0.1.0 release gate) |
| [`docs/IMPLEMENTATION-PLAN.md`](docs/IMPLEMENTATION-PLAN.md) | Phased build sequence, dependency direction, cross-phase invariants |
| [`docs/TAILSCALE-ACLS.md`](docs/TAILSCALE-ACLS.md) | Recommended ACL pattern restricting the vault port to tagged agents |
| [`docs/CLEAN-MACHINE.md`](docs/CLEAN-MACHINE.md) | Agent-machine cleanup checklist (Constitution Principle I) |
| [`.specify/memory/constitution.md`](.specify/memory/constitution.md) | The 11 non-negotiable principles |

<br>

## Goals

The v0.1.0 goal is a working private MVP that proves the threat model in
practice: an agent machine with a clean disk, a vault host on a phone-gated
Tailscale mesh, and a daily dev workflow that no longer requires
plaintext credentials anywhere on the agent.

**Post-v0.1.0 / future scope** (any of these may become a future release;
none is on the v0.1.0 critical path):

- Multi-owner approvals
- Slack / Telegram / PagerDuty Approver backends (the interface is
  already pluggable)
- Shamir passphrase splitting (sigil's SSS) for vault recovery
- Web dashboard
- Proxy mode (vault proxying provider API calls instead of injecting
  tokens)
- Agent-side credential proxy (per-provider HTTP proxy on the agent host)
- TLS within Tailscale (defence-in-depth on top of WireGuard)
- TOTP second factor on Discord approval
- Custom validator authoring SDK

Track progress on the v0.1.0 build via [`docs/SDD-PLAYBOOK.md`](docs/SDD-PLAYBOOK.md).

<br>

## License

This project is licensed under the terms of the [`LICENSE`](LICENSE) file
at the repo root.

<br>

*hush is a tool to make secrets management invisible — to attackers, and
to the operator who just wants to write code without thinking about
where their API keys live. If it's working, you barely notice it.*
