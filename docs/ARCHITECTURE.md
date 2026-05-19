# ARCHITECTURE — hush

> Component model, trust boundaries, data flow, and lifecycle for hush v0.1.0.
> This document explains *how* the system is shaped. The *why* lives in
> `docs/SECURITY.md`. The *what ships* lives in `docs/SPEC.md`.

---

## 1. One-paragraph overview

hush is a Tailscale-only secrets broker. An encrypted vault file lives on a
single trusted host. Agents request short-lived, scoped sessions over the
Tailscale mesh; each request is approved by the configured human approver via
Discord DM on their phone.
Approved sessions yield ES256K-signed JWTs that are IP-bound and use-limited.
Secret values are returned ECIES-encrypted to a per-session ephemeral key, then
injected into the requesting process's environment. Long-running daemons use
`hush supervise`, which holds session continuity across child restarts within
a bounded TTL so a single approval covers a working day.

---

## 2. Trust boundaries

| Zone | Trust | Notes |
|------|-------|-------|
| **The operator (human approver)** | Fully trusted | Approves via Discord on a 2FA-locked phone. v0.1.0 supports a single configured approver. |
| **Discord** | Trusted as a delivery channel | Bot token is high-sensitivity; disconnects raise alerts. Discord is NOT a security boundary — it is the human's UI. |
| **Tailscale mesh** | Trusted as transport | WireGuard-encrypted; ACL-restricted. |
| **Vault host process** | Semi-trusted | Holds decrypted secrets in mlocked memory. Host root compromise does not enable issuing new sessions without Discord approval. |
| **Agent machine processes** | Untrusted | Anything running as $USER may scan disk and process state. hush ensures there is nothing on disk to scan. |
| **Public internet** | Hostile | The vault server MUST NOT be reachable here. |

**Trust transitions** are explicit: a request crosses from "agent process →
Tailscale → vault server → Discord → approver's phone → Discord → vault
server → agent process." Every hop has a verification step (signature, IP allowlist,
JWT validation, ECIES decryption, button click). No hop trusts the previous
without re-verifying.

---

## 3. Top-level diagram

```
                           TAILSCALE MESH
  ┌─────────────────────────────────────────────────────────────────────┐
  │                                                                     │
  │  ┌──────────────────────────┐     ┌───────────────────────────────┐ │
  │  │  ANY AGENT MACHINE       │     │  VAULT HOST                   │ │
  │  │  (untrusted, clean disk) │     │  (process-isolated)           │ │
  │  │                          │     │                               │ │
  │  │  hush request / supervise│     │  hush serve                   │ │
  │  │  ┌─────────────────┐     │     │  ┌─────────────────────────┐  │ │
  │  │  │ ECDSA-sign req  │──────────►│  │ verify client signature │  │ │
  │  │  │ + ephemeral pubK│     │     │  │ check IP allowlist      │  │ │
  │  │  └─────────────────┘     │     │  └──────────┬──────────────┘  │ │
  │  │                          │     │             │                 │ │
  │  │  blocks, waiting         │     │  Discord DM ──────► phone     │ │
  │  │                          │     │  [Approve][Deny]              │ │
  │  │                          │     │             │                 │ │
  │  │  JWT (ES256K signed)     │     │  issue JWT ◄── approved       │ │
  │  │  ◄──────────────────────────── │                               │ │
  │  │                          │     │                               │ │
  │  │  GET /secrets/{name}     │     │  validate JWT                 │ │
  │  │  (JWT in header) ─────────────►│  check scope, IP, uses        │ │
  │  │                          │     │  ECIES-encrypt with client    │ │
  │  │  ECIES ciphertext        │     │  ephemeral pubkey             │ │
  │  │  ◄──────────────────────────── │                               │ │
  │  │  decrypt → env var       │     │  [mlocked memory, no swap]    │ │
  │  │                          │     │  [no key files on disk]       │ │
  │  └──────────────────────────┘     └───────────────────────────────┘ │
  │                                                                     │
  └─────────────────────────────────────────────────────────────────────┘
```

---

## 4. Components

| Component | Binary surface | Responsibility |
|-----------|----------------|----------------|
| **Vault server** (`hush serve`) | `internal/server` + `internal/cli` watcher | HTTP server on Tailscale; mlocked secret storage; JWT issue + validate; SIGHUP atomic reload; optional `--reload-on-vault-change` auto-reload; Discord-bot integration. |
| **Vault file** | `internal/vault` | AES-256-GCM + Argon2id encrypted JSON of secrets. `HUSH` magic, version byte, salt, GCM nonce, ciphertext+tag. |
| **Key hierarchy** | `internal/keys` | BIP32 derivation from passphrase. **No key files on disk.** |
| **JWT/session** | `internal/token` | ES256K signing/verification, multi-use tracking, IP binding, cleanup goroutine, revocation table. |
| **Transport crypto** | `internal/transport` | ECIES encrypt/decrypt for secret responses; ECDSA request signing; nonce + timestamp replay protection. |
| **Discord bot** | `internal/discord`, `internal/discord/alerts` | `Approver` interface, DM dispatch, button handling, disconnect monitoring; tiered operator alerting. The audit log writes _to_ Discord via the `audit.MirrorSession` seam (one-way: `internal/audit` → `internal/discord`); Discord does not import `internal/audit`. |
| **Vault client** (`hush request`) | `internal/cli/request.go` | ECDSA-sign request; ephemeral keypair; ECIES-decrypt secrets; env-inject into child via `--exec`. |
| **Supervisor** (`hush supervise`) | `internal/supervise`, `internal/supervise/config`, `internal/supervise/validators`, `internal/supervise/watchdog` | Daemon-mode state machine; child lifecycle; refresh scheduler; PID flock + Unix status socket. Validators (peer package) are invoked by the supervisor; the watchdog imports the supervisor (reverse direction) for log-pattern alerting on running children. |
| **Status client** (`hush client`) | `internal/cli/client_status.go` | Talks to local supervisor socket. |
| **Management CLI** (`hush secret`, `init`, `revoke`, `health`, `version`) | `internal/cli` | Vault management; client/server bootstrap; ad-hoc operations. |
| **Audit log** | `internal/audit` (top-level package) | Hash-chained, ECDSA-signed `~/.hush/audit.jsonl`; flock-guarded single-writer goroutine; optionally mirrored to a Discord channel via the `MirrorSession` seam. Imported by `internal/server`, `internal/cli`, and `internal/supervise`. |

See `docs/PACKAGE-MAP.md` for file-level responsibilities — the table
above names the load-bearing components only. Eleven additional
internal packages provide supporting concerns: `internal/audit`,
`internal/config`, `internal/keychain`, `internal/logging`,
`internal/supervise/config`, `internal/supervise/validators`,
`internal/supervise/watchdog`, `internal/testutil`,
`internal/vault/securebytes`, and `internal/discord/alerts` (`audit`
is now its own row above; the rest are listed for completeness — each
has its own `## \`internal/<pkg>\`` section in PACKAGE-MAP.md).

---

## 5. Architectural layers

### 5.1 Vault layer
Argon2id-derived master seed → BIP32 derivation → AES-256-GCM-encrypted vault
file. mlocked memory; explicit zeroing; `[]byte`-only secret handling. Atomic
write + SIGHUP atomic swap via `atomic.Pointer[Vault]`. When `hush serve --reload-on-vault-change` is enabled, the CLI watches `secrets.vault` for debounced atomic rewrites and invokes the same reload path automatically.

### 5.2 Identity + session layer
- BIP32 HD key tree (`m/44'/7743'/...`)
- ES256K JWT signing (server-side; private key only in memory)
- ECDSA client request signing (per-machine registered key)
- IP-bound, scope-limited, TTL-bound sessions (interactive: + max_uses)

### 5.3 Transport layer
- Tailscale mesh as the network perimeter
- ECIES end-to-end encryption of secret responses
- nonce + timestamp replay protection (60s nonce window, ±30s timestamp)
- Request body capped at 64KB via `http.MaxBytesReader`

### 5.4 Control plane
- Discord approval bot (interactive buttons)
- Hash-chained ECDSA-signed audit log
- Token revocation endpoint
- Health endpoint and Discord disconnect monitoring

### 5.5 Runtime lifecycle layer
- `hush supervise` state machine with PID + flock split-brain protection
- Pluggable validators run on the supervisor (NEVER the vault server)
- Local Unix status socket for agent-visible freshness
- Refresh window scheduler anchored to waking hours
- Optional grace-window cache (mlocked) for restart resilience

---

## 6. Primary modes

### 6.1 Interactive mode — wrap the shell, not the app

```bash
hush request \
  --server http://100.90.223.110:7743 \
  --scope "ANTHROPIC_API_KEY,GITHUB_TOKEN" \
  --reason "Morning dev session" \
  --ttl 8h \
  --exec "zsh"
```

One Discord approval covers the working day. Tools inside the shell inherit
env vars. Tool crashes (Claude, gh, git) do NOT require re-approval — the
shell persists.

### 6.2 Supervisor mode — one approval covers the daemon's life

```bash
hush supervise --config ~/.hush/supervisors/<daemon>.toml
# (one supervisor per daemon; the operator copies deploy/examples/supervisors/example-daemon.toml
#  and renames per-daemon, then registers a launchd/systemd unit per daemon)
```

The supervisor owns auth state and refresh timing. Children that crash or are
auto-updated restart silently within the session TTL. A single
`[DAEMON]`-labeled Discord approval covers them all.

### 6.3 One-shot batch — env injection into a single process tree

```bash
hush request --scope "ANTHROPIC_API_KEY" --reason "Overnight batch" --ttl 8h \
  --exec "./run-batch.sh"
```

Or, with explicit opt-in to the less safe stdout-eval form:

```bash
eval $(hush request --scope ... --reason ... --format eval)
```

`--format eval` MUST be explicit and emits a stderr warning.

---

## 7. Data flow — interactive request

1. Agent runs `hush request --scope X --reason Y --ttl 8h --exec ...`
2. Client generates an **ephemeral secp256k1 keypair** for this session.
3. Client builds a canonical-JSON payload (alphabetical keys, compact form),
   SHA-256 hashes it, ECDSA-signs with its registered client key, and POSTs
   to `/h/{prefix}/claim`.
4. Server verifies: signature ↔ registered key, IP ↔ allowlist, nonce
   uniqueness within 60s, timestamp within ±30s.
5. Server sends Discord DM to the configured approver with machine name,
   scope, reason, requested TTL, and approval buttons.
6. The approver taps `Approve <ttl>` (or `Deny`).
7. Server issues an ES256K-signed JWT with the approved scope, IP binding,
   `max_uses`, ephemeral pubkey claim, and `session_type="interactive"`.
8. Client receives the JWT and fetches each secret via
   `GET /h/{prefix}/s/{name}` with `Authorization: Bearer <jwt>`.
9. Server ECIES-encrypts each value with the client's ephemeral pubkey and
   returns the raw ciphertext (`Content-Type: application/octet-stream`).
10. Client decrypts each response with the ephemeral private key and injects
    plaintext into the child via `os/exec` env.
11. Ephemeral private key is zeroed; `--exec` child runs with secrets in env.
12. Token expires after TTL or use-count exhaustion.

---

## 8. Data flow — supervisor lifecycle

### 8.1 State machine

```
      ┌─────────────┐         Discord approved
      │  fetching   │ ────────────────────────────┐
      │  (startup)  │                             │
      └──────┬──────┘                             │
             │ approved                           │
             ▼                                    │
      ┌─────────────┐    child exits (clean)      │
      │   running   │ ──────────────┐             │
      │             │               │             │
      └──────┬──────┘    ┌──────────▼──────────┐  │
             ▲           │ silent refill       │  │
             │           │ (cached JWT, TTL OK)│  │
             │           └──────────┬──────────┘  │
             └──────────────────────┘             │
             │                                    │
             │  child exit 78 (EX_CONFIG)         │
             │  OR TTL expired                    │
             │  OR vault returned 401-unknown-jti │
             ▼                                    │
      ┌──────────────────┐                        │
      │ awaiting-approval│ ───────────────────────┘
      │                  │
      └──────────────────┘
```

### 8.2 First boot

1. launchd/systemd starts each `<daemon>-hush-launch.sh` (a copy of the
   committed `deploy/supervise-launch.sh.template` renamed per-daemon).
2. Each script `exec`s `hush supervise --config <path>`.
3. Each supervisor POSTs `/claim` with `session_type=supervisor` →
   server → Discord DM labeled `[DAEMON]`.
4. The approver taps Approve on each → JWTs issued.
5. Supervisor runs validators, fetches secrets, forks child with env injected.
6. Plaintext secrets zeroed from supervisor memory (unless grace cache enabled).

### 8.3 Child exit within TTL

1. `wait()` returns; supervisor checks state machine + remaining TTL + scope.
2. Supervisor SILENTLY refetches each secret with the cached JWT.
3. Refetched values injected into a new child fork. No Discord call.
4. ACP threads / launchd dependents reconnect normally.

### 8.4 Child exit 78 (stale credentials contract)

1. Child exits with `code 78` (`EX_CONFIG`).
2. Supervisor unconditionally enters `awaiting-approval`, regardless of TTL.
3. Discord alert: `[STALE] Child Exit 78`.
4. The operator runs `hush secret rotate <name>` on the vault host, then
   `hush client refresh --supervisor <name>` on the agent host.
5. Supervisor refetches and forks a fresh child.

### 8.5 TTL refresh window

1. At `[refresh_window_start, refresh_window_end]` local time, supervisor
   sends `[DAEMON] Refresh` DM.
2. The approver taps Approve → new JWT covers next 20h.
3. Child is **never** restarted purely because of TTL expiry.
4. Ignored prompt → T-30min fallback nudge fires.

### 8.6 Grace window (opt-in)

When `cache_secrets_for_restart=true`, supervisor holds the last decrypted
secret set in mlocked memory for `grace.window` (default 60m, capped 4h)
beyond JWT validity. Inside the window, child restarts use cached secrets and
defer Discord approval to the morning refresh window. Tradeoff: doubles
on-host plaintext surface (child + supervisor); see `docs/SECURITY.md` §
"Residual risks".

---

## 9. Failure mode handling

| Failure | Behavior |
|---------|----------|
| Discord unreachable | Existing tokens validate normally. New `/claim` returns 503 with `Retry-After`. Health endpoint reports `discord_connected: false`. |
| Tailscale disconnect on agent host | Supervisor backs off exponentially up to remaining TTL. **No Discord prompts** (network blips MUST NOT spam the approver). |
| Vault server restart | Supervisor's next silent refill returns 401 unknown-jti → transitions to `awaiting-approval` cleanly. Child keeps running (refill is what's gated, not the child). |
| Boot ordering (hush before Tailscale) | Backoff up to `boot_retry_timeout` (default 10m), log WARN at each attempt. On exhaustion, exit non-zero so launchd retries. |
| Clock skew at supervisor or server | Refuse to start if `systemsetup -getusingnetworktime` / `timedatectl show` reports unsynced. |
| Split-brain launchd restart | PID file + flock at `~/.hush/run/supervise-{name}.pid`. Second instance waits or exits cleanly. |
| Vault secret rotation mid-session | Child still has old value. `hush secret rotate` triggers SIGHUP on server. Then `hush client refresh --supervisor X` makes the supervisor refetch and gracefully restart the child. |
| Discord DM rate-limit | Supervisor self-caps at 1 prompt per 5min per supervisor. Excess prompts log WARN and drop. |
| Bot token theft + competing instance | Server detects unexpected WebSocket disconnect → log WARN + audit event + refuse `/claim` until reconnect (legitimate bot reconnects; rogue bot would have to keep displacing it, all visibly). |

---

## 10. Phase 0 architecture goals (this bootstrap)

By the end of Phase 0, the architecture must already make the following truths
unambiguous to anyone reading the repo cold:

- The vault server is never public.
- Approval is always human and out-of-band.
- Secrets never persist on agent disks.
- Daemons use `hush supervise` — not naive `hush request --exec`.
- Staleness surfaces proactively via three independent channels.
- The seven security layers are independent — no single failure leaks secrets.
- The repo is private until the public-release gate is satisfied.

---

## 11. Cross-references

| Topic | See |
|-------|-----|
| Spec, requirements, ACs | `docs/SPEC.md` |
| Threat model + 7 layers | `docs/SECURITY.md` |
| HTTP/socket schemas | `docs/API.md` |
| Config formats | `docs/CONFIG-SCHEMA.md` |
| Package layout | `docs/PACKAGE-MAP.md` |
| Lifecycle scenarios | `docs/LIFECYCLE-SCENARIOS.md` |
| Phase execution | `docs/IMPLEMENTATION-PLAN.md` |
| Test plan | `docs/TESTING-STRATEGY.md` |
| Operational runbooks | `docs/OPERATIONS.md` |
| Constitutional principles | `.specify/memory/constitution.md` |
