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

> **Status:** this is still `ALPHA` and PR's are welcome to improve the project.

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

## 🗺️ Command palette

Every hush subcommand at a glance — every entry below is real today.

| Command | Purpose |
|---------|---------|
| `hush smoke` | Guided end-to-end test with a fake secret — start here |
| `hush init server` / `hush init client` | Bootstrap a vault host / enroll an agent host |
| `hush serve` | Run the vault server (Tailscale-only) |
| `hush secret add` / `list` / `remove` / `rotate` | Manage vault entries (rotate re-encrypts and hot-reloads) |
| `hush vault rekey` | Change the vault passphrase — rotates the root of trust (TTY-only) |
| `hush request --exec …` | One-shot interactive fetch + child exec |
| `hush supervise <config.toml>` | Long-running daemon with grace cache + validators |
| `hush health` / `server-url` / `version` | Daily-driver helpers |
| `hush revoke --jti …` | Kill an active session token |
| `hush upgrade` | Self-upgrade from a GitHub release (stable / beta / edge) |

Global flags — `--config <path>`, `--verbose`, `--quiet`, `--no-color` — work on every command.

<br/>

## ⚡ Quick Start

Four flows, in the order you'll use them.

<br/>

### Try it in 60 seconds

**One command, fake secret, real Discord approval:**

```bash
hush smoke --state-dir ~/.hush-smoke --reset
```

`hush smoke` walks the setup prompts, creates an isolated test vault, adds
`HUSH_SMOKE_TEST=hello-from-hush`, starts a temporary Tailscale-only server,
enrolls a client, asks you to approve in Discord, verifies the fake secret,
and shuts the temporary server down.

Smoke uses isolated macOS Keychain entries, `hush-smoke-discord` /
`hush-smoke-server`, under its smoke state. It does not read, overwrite, or
delete the production `hush-discord` / `hush-server` bot-token item. A transient
clock probe outage is downgraded to a warning in smoke; a measured clock drift
outside the configured limit still fails.

To prove client authentication and approval routing against an already-running
server without creating smoke state or touching Keychain/vault state, use an
enrolled client key:

```bash
hush smoke --against-running --client-key-file ~/.hush-client.key
```

> 🧹 **Cleanup:** `hush smoke clean` archives smoke artifacts by default.
> Add `--destroy --confirm 'destroy smoke'` to permanently delete them.

<br/>

### Bootstrap the vault host

Three commands, run on the host you trust with your secrets.

```bash
hush init server                       # interactive preflight + prompts
hush secret add ANTHROPIC_API_KEY      # then OPENAI_API_KEY, GEMINI_API_KEY, …
hush serve                             # binds Tailscale, brokers approvals
```

> 🛰️ **Listen on the vault host's Tailscale IPv4 — not your laptop IP.**
> When `hush init server` asks for a listen address, run
> `printf '%s:7743\n' "$(tailscale ip -4)"` on the **server** host and paste
> the result. Set `discord_approval_channel_id` to route approvals to a
> channel; leave it empty to DM the owner directly.

> 🔐 **macOS Keychain locked?** Choose the env-token fallback during init
> and run `hush serve` with `HUSH_DISCORD_BOT_TOKEN` exported in that
> terminal. Full recovery flow in [`docs/SECURITY.md`](docs/SECURITY.md) §2.4.

<br/>

### Enroll the agent host

Enroll a per-machine client key, then fetch secrets straight into a child
process. Nothing lands on disk.

```bash
hush init client --machine-index 1
HUSH_SERVER="$(hush --config ~/.hush/config.toml server-url)"

hush request \
  --server "$HUSH_SERVER" \
  --machine-index 1 \
  --scope ANTHROPIC_API_KEY --scope OPENAI_API_KEY --scope GEMINI_API_KEY \
  --max-uses 3 --ttl 10m \
  --reason "claude-code session" \
  --exec zsh
```

Approve on Discord; the shell you launched inherits all three keys in its
environment — and **only there**. They are zeroed the moment the shell exits.

> ⚙️ **`--scope` is repeatable and comma-separated.** Either
> `--scope A --scope B` or `--scope A,B` works. `--max-uses` must be ≥ the
> number of scopes (one fetch per scope per session). `--exec` names a
> program, not a shell string — pass child arguments after `--`.

<br/>

### Run a long-running daemon

For agents that run overnight, swap `hush request` for `hush supervise`.
One approval per refresh window keeps a 24/7 child alive across crashes;
the grace cache silently restarts a child that dies inside
`cache_grace_ttl`, so a 3am OOM doesn't page you.

Save the snippet below to `~/.hush/supervisors/hermes.toml` — the full
schema lives in
[`deploy/examples/supervisors/example-daemon.toml`](deploy/examples/supervisors/example-daemon.toml):

```toml
name                      = "hermes"
reason                    = "Hermes AI gateway"
server_url                = "http://100.64.0.1:7743/h/example"
client_machine_index      = 1
session_type              = "supervisor"
requested_ttl             = "20h"
refresh_window            = "09:00-10:00"
cache_secrets_for_restart = true
cache_grace_ttl           = "60m"
status_socket             = "/tmp/hush/supervise-hermes.sock"
pid_file                  = "/tmp/hush/supervise-hermes.pid"

scope = ["ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY"]

[child]
command = ["/usr/local/bin/hermes", "gateway", "start"]

[validators]
ANTHROPIC_API_KEY = "anthropic"
OPENAI_API_KEY    = "openai"
GEMINI_API_KEY    = "google-ai"
```

Then:

```bash
hush supervise ~/.hush/supervisors/hermes.toml --dry-run   # validate the config
hush supervise ~/.hush/supervisors/hermes.toml             # run for real
```

Built-in validators (`anthropic`, `anthropic-oauth`, `openai`, `google-ai`,
`github`) hit each provider before the child starts — stale credentials
fail loudly instead of breaking your daemon at 3am. Full guide in
[`docs/DAEMONS.md`](docs/DAEMONS.md).

<br/>

> 📖 **More?** [`docs/OPERATIONS.md`](docs/OPERATIONS.md) covers Keychain
> ACL recovery, clock-skew overrides, and `--non-interactive` mode for
> Terraform/Ansible provisioning. [`docs/CONFIG-SCHEMA.md`](docs/CONFIG-SCHEMA.md)
> has the full server + supervisor TOML schemas, and
> [`deploy/examples/supervisors/`](deploy/examples/supervisors/) holds
> production templates ready to copy.

<br/>

## At a glance

**What hush does:**

- Keeps every secret encrypted in a single AES-256-GCM + Argon2id (256MB) vault file on one trusted host.
- Requires phone approval (Discord DM with interactive buttons) for every fresh session.
- Delivers secrets ECIES-encrypted end-to-end into agent process memory only — no disk writes on the agent.

**What hush explicitly does NOT do:**

- No multi-owner approvals — a single configured operator approves every request (multi-owner quorum is future scope).
- No cloud KMS / SaaS dependency. The vault is self-hosted and offline-capable.
- No public network exposure. The vault server is bound to a Tailscale interface and refuses to start otherwise.

**Daily-driver helpers:**

- `hush health --server "$(hush server-url)"` — one-shot reachability + clock-skew check.
- `hush secret list` — enumerate vault entries (TTY: `NAME — description`; pipe-friendly).
- `hush secret rotate` — re-encrypt the vault and hot-reload `hush serve` (SIGHUP, no downtime).
- `hush vault rekey` — change the vault passphrase itself; snapshots the old vault and prints a restart-required reminder. See [`docs/VAULT-REKEY.md`](docs/VAULT-REKEY.md).
- `hush server-url` — print the canonical server URL from your TOML config, perfect for `$(…)` substitution.
- `hush revoke --jti <uuid>` — kill an active JWT before its TTL expires.

<br/>

## 🔀 Operating modes

Two ways to consume secrets; pick by **how often you can answer your phone**.

| Mode | Best for | Approval cadence |
|------|----------|------------------|
| `hush request --exec` | Interactive shells, one-shot scripts, CI jobs | Once per invocation |
| `hush supervise` | 24/7 daemons, AI gateways, long-running agents | Once per refresh window (e.g. 09:00–10:00 daily) |

Pick `request` for ad-hoc work; pick `supervise` when a phone buzz per
restart would page you at 3am.

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

The network perimeter is **Tailscale-only** — the vault server refuses to
bind on a non-Tailscale interface. Tailscale is the current mesh-VPN
choice, but the architecture does not depend on it specifically; the
requirement is "no public reachability," and Tailscale satisfies it
cleanly. The `Approver` interface is also pluggable: **Discord is the
reference implementation** and the only one that ships today, but future
Slack / Telegram / PagerDuty backends can be wired without changing the
rest of the system.

For the full architecture treatment, see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

<br/>

### Programmatic integration

AI agents that consume hush — Claude Code, Codex, custom Go daemons —
can integrate in-process via the
[`github.com/mrz1836/hush/pkg/client`](pkg/client/) SDK instead of
exec'ing the CLI. The SDK gives agents typed access to two surfaces:

**1. Supervisor freshness — `SupervisorStatus`** (read what the local
supervisor knows; no Discord roundtrip):

```go
import "github.com/mrz1836/hush/pkg/client"

sup := client.NewSupervisorStatus(os.Getenv("HUSH_STATUS_SOCKET"))
status, err := sup.Snapshot(ctx)
if len(status.ScopeStale) > 0 {
    log.Fatal("refusing to run — stale scopes:", status.ScopeStale)
}
fmt.Println("session expires at:", status.SessionExpiresAt)
```

**2. Capability discovery — `Me()`** (ask the vault server *what
scopes exist* and *what does my current session look like*, signed
with the agent's enrolled client key, without burning a Discord
approval):

```go
resp, err := client.Me(ctx, client.MeRequest{
    ServerURL:   "http://100.64.0.1:7743/h/abcd1234",
    ClientKey:   myEnrolledPrivKey,           // *ecdsa.PrivateKey
    MachineName: "claude-code-laptop",
    BearerJWT:   os.Getenv("HUSH_BEARER"),    // optional
})
if err != nil {
    log.Fatal(err)
}
fmt.Println("scopes available:", resp.ScopesAvailable)
if resp.CurrentSession != nil {
    fmt.Println("current jti:", resp.CurrentSession.JTI,
        "expires:", resp.CurrentSession.ExpiresAt)
}
```

Together these let an agent plan: "do I already hold a fresh session
for this scope? when does it expire? what *could* I request if I need
more?" — all without touching the operator's phone.

See [`pkg/client/README.md`](pkg/client/README.md) for the full v1
surface. The SDK ships typed errors (`ErrSocketUnavailable`,
`ErrInvalidResponse`, `ErrRefreshDenied`, `ErrUnauthenticated`) so
callers can switch on them with `errors.Is`.

**3. Lifecycle events — `Watch()`** (reactive notification so an
agent can wind down BEFORE its credentials expire, instead of being
killed mid-task):

```go
events, _ := sup.Watch(ctx, client.WatchOptions{
    PollInterval:     30 * time.Second,
    ExpiryThresholds: []time.Duration{15 * time.Minute, 5 * time.Minute, 1 * time.Minute},
})
for ev := range events {
    switch ev.Type {
    case client.EventExpiresSoon:
        if ev.Threshold <= time.Minute {
            checkpoint(); shutdownCleanly()
        }
    case client.EventStateChange:
        log.Info("supervisor state →", ev.Status.State)
    case client.EventSessionRenewed:
        log.Info("fresh JTI", ev.Status.SessionJTI)
    }
}
```

`Watch()` emits `EventInitial`, `EventStateChange`,
`EventScopeHealthChange`, `EventSessionRenewed`, `EventExpiresSoon`
(once per configured threshold per session), and `EventError` for
transient poll failures. The channel closes on context cancel.

**Worked example**: a runnable program demonstrating all three SDK
calls (Snapshot, Me, Watch) plus the agent-context flags on `/claim`
lives at [`examples/agent/`](examples/agent/). See
[`docs/AGENT-INTEGRATION.md`](docs/AGENT-INTEGRATION.md) for the
complete agent integration guide.

<br/>

### Tech stack

- **[Go 1.26+](https://go.dev/)** — single static binary, built
  `CGO_ENABLED=0` exclusively.
- **[decred/dcrd/dcrec/secp256k1/v4](https://github.com/decred/dcrd)** —
  secp256k1 primitives used for ECDSA signing, ES256K JWTs, and ECIES
  envelope encryption.
- **[decred/dcrd/hdkeychain/v3](https://github.com/decred/dcrd)** — BIP32
  HD key derivation from the operator passphrase, paired with stdlib
  `golang.org/x/crypto/argon2` for the KDF.
- **[Tailscale](https://tailscale.com/)** — the only network reachable to
  the vault server. WireGuard underneath; identity-based ACLs above.
- **[Discord](https://discord.com/)** + **[discordgo](https://github.com/bwmarrin/discordgo)**
  — phone-based approval channel; the reference `Approver` implementation.
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
| [`docs/VAULT-REKEY.md`](docs/VAULT-REKEY.md) | Vault passphrase rotation runbook: `secret rotate` vs `vault rekey`, Keychain follow-up, snapshot/rollback |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Component model, trust boundaries, supervisor lifecycle |
| [`docs/SECURITY.md`](docs/SECURITY.md) | Threat model, 7 security layers, residual risks |
| [`docs/AGENT-INTEGRATION.md`](docs/AGENT-INTEGRATION.md) | SDK guide for AI agents: `pkg/client` Snapshot / Me / Watch + agent-context flags on `/claim` |
| [`docs/CONFIG-SCHEMA.md`](docs/CONFIG-SCHEMA.md) | Server + supervisor TOML schemas, defaults, validation |
| [`docs/DAEMONS.md`](docs/DAEMONS.md) | Supervisor pattern, refresh tuning, validator authoring |
| [`docs/API.md`](docs/API.md) | HTTP endpoint reference |
| [`docs/LIFECYCLE-SCENARIOS.md`](docs/LIFECYCLE-SCENARIOS.md) | Supervisor lifecycle scenarios — behavioral reference |
| [`docs/TAILSCALE-ACLS.md`](docs/TAILSCALE-ACLS.md) | Recommended ACL pattern restricting the vault port |
| [`docs/CLEAN-MACHINE.md`](docs/CLEAN-MACHINE.md) | Agent-machine cleanup checklist |

<br/>

## 🔐 Security

### Important Disclaimer

> ⚠️ **Experimental Software — Use at Your Own Risk**
>
> hush is experimental, open-source software provided "AS-IS" without
> warranty. By running it, you acknowledge:
>
> - **Status:** ALPHA quality; expect bugs, edge cases, and breaking changes.
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

<br/>

### Test Coverage

View coverage report:

```bash
magex test:coverage
```

Coverage reports are automatically uploaded to [Codecov](https://codecov.io/gh/mrz1836/hush)
on every commit.

<br/>

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

<br>

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
> Last measured: 2026-05-24

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
