<div align="center">

# 🤫&nbsp;&nbsp;hush

**Discord-gated secrets broker for AI agents**

One passphrase. No key files. No dotfiles on agent disks.

<br/>

<a href="https://github.com/mrz1836/hush/releases"><img src="https://img.shields.io/github/release-pre/mrz1836/hush?include_prereleases&style=flat-square&logo=github&color=black" alt="Release"></a>
<a href="https://golang.org/"><img src="https://img.shields.io/github/go-mod/go-version/mrz1836/hush?style=flat-square&logo=go&color=00ADD8" alt="Go Version"></a>
<a href="https://github.com/mrz1836/hush/blob/master/LICENSE"><img src="https://img.shields.io/github/license/mrz1836/hush?style=flat-square&color=blue&v=1" alt="License"></a>

<br/>

<table align="center" border="0">
  <tr>
    <td align="right">
       <code>CI / CD</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/mrz1836/hush/actions"><img src="https://img.shields.io/github/actions/workflow/status/mrz1836/hush/fortress.yml?branch=master&label=build&logo=github&style=flat-square" alt="Build"></a>
       <a href="https://github.com/mrz1836/hush/actions"><img src="https://img.shields.io/github/last-commit/mrz1836/hush?style=flat-square&logo=git&logoColor=white&label=last%20update" alt="Last Commit"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Quality</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://goreportcard.com/report/github.com/mrz1836/hush"><img src="https://goreportcard.com/badge/github.com/mrz1836/hush?style=flat-square&v=2" alt="Go Report"></a>
       <a href="https://codecov.io/gh/mrz1836/hush"><img src="https://codecov.io/gh/mrz1836/hush/branch/master/graph/badge.svg?style=flat-square" alt="Coverage"></a>
    </td>
  </tr>

  <tr>
    <td align="right">
       <code>Security</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://scorecard.dev/viewer/?uri=github.com/mrz1836/hush"><img src="https://api.scorecard.dev/projects/github.com/mrz1836/hush/badge?style=flat-square" alt="Scorecard"></a>
       <a href=".github/SECURITY.md"><img src="https://img.shields.io/badge/policy-active-success?style=flat-square&logo=security&logoColor=white" alt="Security"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Community</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/mrz1836/hush/graphs/contributors"><img src="https://img.shields.io/github/contributors/mrz1836/hush?style=flat-square&color=orange" alt="Contributors"></a>
       <a href="https://mrz1818.com/"><img src="https://img.shields.io/badge/donate-bitcoin-ff9900?style=flat-square&logo=bitcoin" alt="Bitcoin"></a>
    </td>
  </tr>
</table>

</div>

<br/>
<br/>

<div align="center">

### <code>Project Navigation</code>

</div>

<table align="center">
  <tr>
    <td align="center" width="33%">
       🚀&nbsp;<a href="#-installation"><code>Installation</code></a>
    </td>
    <td align="center" width="33%">
       ⚡&nbsp;<a href="#-quick-start"><code>Quick&nbsp;Start</code></a>
    </td>
    <td align="center" width="33%">
       📚&nbsp;<a href="#-documentation"><code>Documentation</code></a>
    </td>
  </tr>
  <tr>
    <td align="center">
       🔐&nbsp;<a href="#-security"><code>Security</code></a>
    </td>
    <td align="center">
      🛠️&nbsp;<a href="#-code-standards"><code>Code&nbsp;Standards</code></a>
    </td>
    <td align="center">
      🧪&nbsp;<a href="#-examples--tests"><code>Examples&nbsp;&&nbsp;Tests</code></a>
    </td>
  </tr>
  <tr>
    <td align="center">
      🤖&nbsp;<a href="#-ai-usage--assistant-guidelines"><code>AI&nbsp;Usage</code></a>
    </td>
    <td align="center">
       ⚖️&nbsp;<a href="#-license"><code>License</code></a>
    </td>
    <td align="center">
       🤝&nbsp;<a href="#-contributing"><code>Contributing</code></a>
    </td>
  </tr>
  <tr>
    <td align="center" colspan="3">
       👥&nbsp;<a href="#-maintainers"><code>Maintainers</code></a>
    </td>
  </tr>
</table>

<br/>

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

<br/>

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

<br/>

## 🚀 Installation

**hush** requires a [supported release of Go](https://golang.org/doc/devel/release.html#policy)
(Go 1.26+) and is built `CGO_ENABLED=0` — a single static binary.

> **Status:** v0.1.0 is a private MVP. Treat the steps below as the
> documented happy path, not a guarantee.

Prerequisites: a vault host and an agent host on the same Tailscale
tailnet, plus a Discord bot you control
(<https://discord.com/developers/applications>) for the approval channel.

### Build from source

```bash
git clone https://github.com/mrz1836/hush.git && cd hush
magex build && sudo install -m 0755 cmd/hush/hush /usr/local/bin/hush
```

<br/>

### Upgrade

Once `hush` is on your `PATH`, the binary can upgrade itself in place
from the [GitHub releases](https://github.com/mrz1836/hush/releases):

```bash
hush upgrade          # download the latest release tarball, verify SHA-256, install in place
hush upgrade --check  # report the latest available version without installing
hush upgrade --force  # reinstall the latest release even when already current
```

Channel selection is controlled by the `UPDATE_CHANNEL` environment
variable (case-insensitive; default `stable`):

```bash
UPDATE_CHANNEL=stable hush upgrade   # latest non-prerelease (default)
UPDATE_CHANNEL=beta   hush upgrade   # latest prerelease, falls back to stable when none
UPDATE_CHANNEL=edge   hush upgrade   # most recent release of any kind (excludes drafts)

# Or override the env per-invocation with --channel:
hush upgrade --channel beta
```

The install target is the resolved path of the running binary
(`os.Executable()` with symlinks evaluated — typically `$(which hush)`).
`hush upgrade` requires write access to that directory; when the
directory is not writable the command exits with a clear error naming
the directory rather than silently installing elsewhere. Re-run the
command under `sudo` (or copy the new binary into place manually) when
that happens.

After a successful upgrade `hush upgrade` prints a `Restart any
running 'hush serve' to pick up the new version` reminder — the
upgrade does not touch any supervised process.

<br/>

## ⚡ Quick Start

Get up and running with these essential commands:

<br>

### Run a confidence check

**One command, fake secret, real Discord approval:**

```bash
hush smoke --state-dir ~/.hush-smoke --reset
```

`hush smoke` walks the setup prompts, creates an isolated test vault, adds
`HUSH_SMOKE_TEST=hello-from-hush`, starts a temporary Tailscale-only server,
enrolls a client, asks you to approve in Discord, verifies the fake secret,
and then shuts the temporary server down. Clean smoke artifacts safely with
`hush smoke clean` (archives by default).

<br>

### Bootstrap the vault host

```bash
hush init server          # guided / interactive; preflight + prompts
hush secret add OPENAI_API_KEY
hush serve                # binds Tailscale interface, brokers approvals
```

`hush init server` is the canonical first-run entry point. It runs a
diagnostic-first preflight, prompts for the inputs it actually needs,
classifies pre-existing state per-artifact, and never silently overwrites.
When it asks for a listen address, use the **vault host's Tailscale IPv4**
plus a free port (for example, run `printf '%s:7743\n' "$(tailscale ip -4)"`
on the server host). Do not use the laptop/client IP.

During `hush init server`, set `discord_approval_channel_id` if you want
approvals in a Discord channel instead of owner DMs. On macOS, if the login
Keychain is locked or unavailable, choose the env-token fallback and run
`hush serve` with `HUSH_DISCORD_BOT_TOKEN` exported in that terminal.

<br>

### Enrol the agent host

```bash
hush init client --machine-index 1
HUSH_SERVER="$(hush --config ~/.hush/config.toml server-url)"
hush request \
  --server "$HUSH_SERVER" \
  --machine-index 1 --scope OPENAI_API_KEY \
  --max-uses 1 --ttl 5m --reason "smoke test" \
  --exec printenv -- OPENAI_API_KEY
```

Approve on Discord; the child process you named in `--exec` runs with
`OPENAI_API_KEY` in its environment — and only there. `--exec` names a
program, not a shell string; pass child arguments after `--`. Nothing is
written to disk on the agent host.

<br/>

> 📖 **For the full walkthrough — Keychain ACL recovery, clock-skew override,
> `--non-interactive` mode — see [`docs/OPERATIONS.md`](docs/OPERATIONS.md).**
> For Keychain vs `HUSH_DISCORD_BOT_TOKEN` positioning and the threat model,
> see [`docs/SECURITY.md`](docs/SECURITY.md) §2.4. For long-running daemons,
> see [`docs/DAEMONS.md`](docs/DAEMONS.md) and
> [`deploy/examples/supervisors/`](deploy/examples/supervisors/). For server +
> supervisor TOML schemas, see [`docs/CONFIG-SCHEMA.md`](docs/CONFIG-SCHEMA.md).

<br/>

## At a glance

**What hush does:**

- Keeps every secret encrypted in a single AES-256-GCM + Argon2id (256MB) vault file on one trusted host.
- Requires phone approval (Discord DM with interactive buttons) for every fresh session.
- Delivers secrets ECIES-encrypted end-to-end into agent process memory only — no disk writes on the agent.

**What hush explicitly does NOT do (v0.1.0):**

- No multi-owner approvals (a single configured operator approves; multi-owner is post-v0.1.0 future scope).
- No cloud KMS / SaaS dependency. The vault is self-hosted and offline-capable.
- No public network exposure. The vault server is bound to a Tailscale interface and refuses to start otherwise.

<br/>

### Why hush exists

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

<br/>

### Architecture summary

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

<br/>

### Tech stack

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

<br/>

## 📚 Documentation

View the comprehensive documentation for hush:

| Doc | Purpose |
|-----|---------|
| [`docs/OPERATIONS.md`](docs/OPERATIONS.md) | Setup, day-to-day modes, `--non-interactive`, Keychain recovery |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Component model, trust boundaries, supervisor lifecycle |
| [`docs/SECURITY.md`](docs/SECURITY.md) | Threat model, 7 security layers, residual risks |
| [`docs/CONFIG-SCHEMA.md`](docs/CONFIG-SCHEMA.md) | Server + supervisor TOML schemas, defaults, validation |
| [`docs/DAEMONS.md`](docs/DAEMONS.md) | Supervisor pattern, refresh tuning, validator authoring |
| [`docs/API.md`](docs/API.md) | HTTP endpoint reference |
| [`docs/LIFECYCLE-SCENARIOS.md`](docs/LIFECYCLE-SCENARIOS.md) | 17 supervisor lifecycle scenarios — behavioral reference |
| [`docs/TAILSCALE-ACLS.md`](docs/TAILSCALE-ACLS.md) | Recommended ACL pattern restricting the vault port |
| [`docs/CLEAN-MACHINE.md`](docs/CLEAN-MACHINE.md) | Agent-machine cleanup checklist |
| [`.specify/memory/constitution.md`](.specify/memory/constitution.md) | The 11 non-negotiable principles |

<br/>

### Goals

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

<br/>

## 🔐 Security

### Important Disclaimer

> ⚠️ **Experimental Software — Use at Your Own Risk**
>
> hush is experimental, open-source software provided "AS-IS" without
> warranty. By running it, you acknowledge:
>
> - **Private MVP:** v0.1.0 is an unproven private MVP — not production-grade,
>   and the end-to-end round-trip has not been independently verified.
> - **No formal audit:** hush has not been professionally audited or
>   penetration-tested.
> - **You own the host:** the trusted vault host, your Tailscale config, and
>   your Discord bot are yours to secure — hush only does its part.
> - **No liability:** the authors accept no responsibility for compromised
>   secrets, downtime, or damages.
>
> **Don't trust hush with a secret you can't afford to rotate.** If it breaks,
> you get to keep both pieces.

For the full threat model and the 7 security layers, see
[`docs/SECURITY.md`](docs/SECURITY.md). For security issues, see our
[Security Policy](.github/SECURITY.md) or contact: [hush@mrz1818.com](mailto:hush@mrz1818.com).

<br/>

### Additional Documentation & Repository Management

<details>
<summary><strong><code>Development Setup (Getting Started)</code></strong></summary>
<br/>

Install [MAGE-X](https://github.com/mrz1836/go-mage) build tool for development:

```bash
# Install MAGE-X for development and building
go install github.com/magefile/mage@latest
go install github.com/mrz1836/go-mage/magex@latest
magex update:install
```
</details>

<details>
<summary><strong><code>Build Commands</code></strong></summary>
<br/>

View all build commands:

```bash
magex help
```

Common commands:
- `magex build` — Build the binary
- `magex test` — Run test suite
- `magex lint` — Run all linters
- `magex deps:update` — Update dependencies

</details>

<details>
<summary><strong><code>Binary Deployment</code></strong></summary>
<br/>

This project uses [goreleaser](https://github.com/goreleaser/goreleaser) for
streamlined binary deployment to GitHub. To get started, install it via:

```bash
brew install goreleaser
```

The release process is defined in the [.goreleaser.yml](.goreleaser.yml)
configuration file. Then create and push a new Git tag using:

```bash
magex version:bump bump=patch push=true branch=master
```

This process ensures consistent, repeatable releases with properly versioned
artifacts.

</details>

<details>
<summary><strong><code>GitHub Workflows</code></strong></summary>
<br/>

hush uses the **Fortress** workflow system for comprehensive CI/CD:

- **fortress-test-suite.yml** — Complete test suite across multiple Go versions
- **fortress-code-quality.yml** — Code quality checks (gofmt, golangci-lint, staticcheck)
- **fortress-security-scans.yml** — Security vulnerability scanning
- **fortress-coverage.yml** — Code coverage reporting to Codecov
- **fortress-release.yml** — Automated binary releases via GoReleaser

See all workflows in [`.github/workflows/`](.github/workflows/).

</details>

<details>
<summary><strong><code>Updating Dependencies</code></strong></summary>
<br/>

To update all dependencies (Go modules, linters, and related tools), run:

```bash
magex deps:update
```

This command ensures all dependencies are brought up to date in a single step,
including Go modules and any managed tools. It is the recommended way to keep
your development environment and CI in sync with the latest versions.

</details>

<br/>

## 🧪 Examples & Tests

All unit tests run via [GitHub Actions](https://github.com/mrz1836/hush/actions).
View the [configuration file](.github/workflows/fortress.yml).

Run all tests (fast):

```bash
magex test
```

Run all tests with race detector (slower):

```bash
magex test:race
```

### Test Coverage

View coverage report:

```bash
magex test:coverage
```

Coverage reports are automatically uploaded to [Codecov](https://codecov.io/gh/mrz1836/hush)
on every commit.

### Benchmarks

Baseline performance numbers for the hot crypto paths. Re-measure after any
refactor that touches the request path, vault loader, or transport encryption —
regressions land here before they land in production.

Run the suite:

```bash
magex bench:default time=2s
```

Capture a baseline file for diffs across branches:

```bash
magex bench:save time=2s out=bench.txt
```

Compare two runs:

```bash
magex bench:compare old=before.txt new=after.txt
```

**Latest baseline** — Apple M1 Max · darwin/arm64 · Go 1.26 · `benchtime=2s`:

| Benchmark                                        | ns/op   | B/op   | allocs/op | Path covered                                          |
| ------------------------------------------------ | ------: | -----: | --------: | ----------------------------------------------------- |
| `BenchmarkValidate` (`internal/token`)           | 192,871 |  5,739 |       105 | JWT parse + ES256K verify + store lookup (supervisor) |
| `BenchmarkEncrypt` (`internal/transport/ecies`)  | 173,705 |  3,345 |        40 | Ephemeral keygen + ECDH + AES-CBC + HMAC envelope     |
| `BenchmarkDecrypt` (`internal/transport/ecies`)  | 151,457 |  2,528 |        29 | Pubkey parse + ECDH + KDF + HMAC verify + AES-CBC     |
| `BenchmarkLoad` (`internal/vault`)               |  51,813 | 16,528 |       113 | Encrypted-vault read (16 secrets, ~64 B each)         |

> Numbers are local-machine baselines, not SLOs. Use them to spot
> ≥10% regressions on the same hardware after a code change. The CI
> machine numbers will differ; track relative deltas, not absolutes.
>
> Last measured: 2026-05-24 at HEAD of [the optimization plan](./README.md#benchmarks).

<br/>

## 🛠️ Code Standards

Read more about this Go project's [code standards](.github/CODE_STANDARDS.md).

<br/>

## 🤖 AI Usage & Assistant Guidelines

Read the [AI Usage & Assistant Guidelines](.github/CLAUDE.md) for details on
how AI is used in this project and how to interact with AI assistants.

<br/>

## 👥 Maintainers

| [<img src="https://github.com/mrz1836.png" height="50" alt="MrZ" />](https://github.com/mrz1836) |
|:------------------------------------------------------------------------------------------------:|
|                                [MrZ](https://github.com/mrz1836)                                 |

<br/>

## 🤝 Contributing

View the [contributing guidelines](.github/CONTRIBUTING.md) and please follow
the [code of conduct](.github/CODE_OF_CONDUCT.md).

### How can I help?

All kinds of contributions are welcome :raised_hands:!
The most basic way to show your support is to star :star2: the project, or to
raise issues :speech_balloon:.
You can also support this project by [becoming a sponsor on GitHub](https://github.com/sponsors/mrz1836) :clap:
or by making a [**bitcoin donation**](https://mrz1818.com/?tab=tips&utm_source=github&utm_medium=sponsor-link&utm_campaign=hush&utm_term=hush&utm_content=hush)
to ensure this journey continues indefinitely! :rocket:

[![Stars](https://img.shields.io/github/stars/mrz1836/hush?label=Please%20like%20us&style=social)](https://github.com/mrz1836/hush/stargazers)

<br/>

## 📝 License

[![License](https://img.shields.io/github/license/mrz1836/hush.svg?style=flat&v=1)](LICENSE)

This project is licensed under the terms of the [`LICENSE`](LICENSE) file at
the repo root.
