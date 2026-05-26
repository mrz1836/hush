# SECURITY — hush

> Threat model, security layers, mitigations, and known residual risks for
> hush v0.1.0. This document is the security source of truth. If
> implementation contradicts SECURITY, implementation is wrong.

---

## 1. Threat model

### 1.1 Primary threat — commodity malware scanning for secrets

When untrusted code lands on a developer machine — via npm/pip supply-chain,
LLM-generated scripts, or a trojan masquerading as a tool — the very first
thing it usually does is grep for known credential patterns in known files:

```bash
grep -r "sk-ant-\|sk-proj-\|ghp_\|AKIA" ~/.zshrc ~/.bashrc ~/.profile
cat ~/.config/gh/hosts.yml
cat ~/.aws/credentials
find ~ -name ".env" -exec cat {} \;
find ~ -name "signing.key" -o -name "*.pem" -o -name "*.key"
```

**hush exists to make this enumeration return nothing on agent machines.**

### 1.2 Trust boundaries

| Zone | Trust |
|------|-------|
| The operator (the configured human approver) via Discord on a 2FA-protected phone | Fully trusted |
| Discord (delivery channel) | Trusted as transport, NOT as a security boundary |
| Tailscale mesh | Trusted as transport |
| Vault host process (mlocked, ACL-restricted Keychain) | Semi-trusted — root compromise still cannot issue new sessions |
| Agent machines | Untrusted |
| Public internet | Hostile |

### 1.3 What hush eliminates on agent machines

| Scanner target | hush state |
|----------------|------------|
| `~/.zshrc` / `~/.bashrc` exports | Empty — no secrets in dotfiles |
| `~/.config/gh/hosts.yml` | Doesn't exist — `gh auth login` removed |
| `~/.aws/credentials` | Doesn't exist — managed via vault |
| `.env` files | Don't exist |
| `signing.key`, `*.pem`, `*.key` | Don't exist — keys derived at runtime from passphrase |
| Tool-specific credential stores | All clean |

### 1.4 Threat matrix

| Threat | Mitigation |
|--------|------------|
| Commodity scanner greps for API keys in dotfiles | No secrets on disk on any agent. |
| Scanner looks for key files on vault host | No key files exist anywhere — all derived via BIP32 from the passphrase. |
| Agent machine fully compromised | No keys on disk. No tokens at rest. Attacker has nothing to steal between sessions. |
| Malware reads process env vars during active session | Bounded time window vs. files-on-disk 24/7. Tailscale ACLs prevent exfil to unknown hosts. |
| Vault host compromised (user-level) | Vault file is AES-256-GCM + Argon2id-256MB. Secrets in mlocked memory — can't be swapped to disk. No signing key file to steal. |
| Vault host compromised (root) | Attacker reads process memory but cannot issue new sessions without Discord approval. Can't forge JWTs — signing key only in memory. Killing the server zeros all secrets. |
| Attacker steals signing key to forge JWTs | **Eliminated.** No signing key file exists. ES256K (asymmetric) — even with the public key, tokens cannot be forged. |
| Secret values intercepted in HTTP response | **Eliminated.** ECIES-encrypted to the client's per-session ephemeral pubkey. Captured traffic shows blobs only. |
| Rogue process on agent impersonates vault client | **Mitigated.** ECDSA signature + IP allowlist = two factors. |
| Token intercepted on network | Tailscale (WireGuard) encrypts everything. Token is IP-bound and TTL-limited. |
| Token replayed | `max_uses` tracked server-side. After exhaustion, token is dead. |
| Token used from wrong machine | JWT `client_ip` claim is checked against requesting Tailscale IP every fetch. |
| Request replay attack | Client nonce + timestamp on every request; nonce cache rejects duplicates within 60s; timestamps must be ±30s. |
| Attacker approves via Discord | Requires the operator's authenticated Discord session on their phone. Discord account is 2FA. |
| Brute-force vault passphrase | Argon2id (time=4, memory=256MB, threads=4). Impractical even with 2026 GPU capabilities. |
| Vault file stolen from disk/backup | AES-256-GCM. Useless without passphrase. Safe to back up. |
| Audit trail tampered with | Every event ECDSA-signed and hash-chained. Modification breaks the chain. |
| Port scanner discovers vault API | Random API path prefix. Standard probes get 404. |
| Malware reads Keychain for vault passphrase | **Mitigated.** Keychain items created with a per-binary `-T` ACL for the installed hush binary path. Other processes trigger a system Keychain prompt. Management commands require interactive TTY passphrase. |
| Discord bot token stolen → auto-approve sessions | **Mitigated.** Bot token in Keychain by default (see §2.4 for the env-token fallback positioning). Server monitors WebSocket disconnect — unexpected disconnect → WARN log + audit + refusal of new `/claim`. Attacker's competing bot would have to keep displacing the real one, which is detectable. |
| Discord API outage → no new sessions | **Accepted.** Existing sessions continue. New sessions blocked with 503. Plan TTLs for full-day coverage. |
| Rogue process runs `hush secret add` on vault host | **Mitigated.** Management commands refuse piped stdin and Keychain reads. Only an interactive TTY can modify secrets. |

---

## 2. Security posture by zone

### 2.1 Agent machines
- Zero secrets at rest.
- No dotfile exports of API keys.
- No `gh auth login`.
- No `~/.aws/credentials`.
- No tool-specific credential stores.
- Client passphrase is in the OS keychain, ACL-restricted to `hush`.

### 2.2 Vault host
- Encrypted vault file at rest (AES-256-GCM + Argon2id-256MB).
- Derived keys only — no key files on disk.
- Secrets in mlocked memory — not swappable.
- Approval gate before each fresh session (Discord DM).
- Files in `~/.hush/` are mode `0600` (dir is `0700`).
- Server refuses to start if any file is more permissive.

### 2.3 Network
- Tailscale-only.
- Vault server bound to the Tailscale interface IP, never `0.0.0.0`.
- Tailscale ACLs restrict port 7743 to `tag:trusted → tag:sandbox`.
- Vault server never reachable from the public internet.

### 2.4 Bot token storage (macOS Keychain default, env-token fallback)

This section is only about the Discord bot token. The earlier config/vault
reuse and repair prompts are separate; they can succeed even if the later bot-token store hits a macOS Keychain problem.

On macOS the Discord bot token is stored in the **OS Keychain by default**,
under service `hush-discord`, ACL-restricted to the current hush binary path
(for example the dev install path from `command -v hush`). This is the
recommended posture and the path `hush init server` takes when no Keychain
failure is detected.

If Keychain refuses the bot-token write during setup, hush does **not** write a
plaintext token file. It first offers a retry path so you can unlock or approve
the Keychain while the token is still only in memory. If you deliberately pick
fallback, hush uses an explicit one-session `HUSH_DISCORD_BOT_TOKEN=... hush
serve ...` command. That keeps the bot token out of repo/config/state files
while the Keychain path is repaired.

Why Keychain is preferred:

| Property | Keychain | `HUSH_DISCORD_BOT_TOKEN` env-var |
|----------|----------|----------------------------------|
| Per-binary ACL | Yes — only the current hush binary path can read it without a system prompt. | No — any process running as the same user can read the env for the lifetime of the process. |
| Secret at rest | Not in hush files. | Not in hush files if used only as a one-session command/env. |
| Visibility in `ps eww` / `/proc/{pid}/environ` | Not exposed. | Exposed for the lifetime of the serving process. |
| Survives reboot | Yes. | No, unless exported from a login profile — which hush explicitly avoids. |
| Bootstrap UX | One-time `security` ACL prompt. | Manual serve-time export until Keychain is fixed. |

`hush init server` enforces this positioning without writing plaintext token
files. If a bot-token Keychain write is refused, hush shows a retry-first panel;
env-token fallback remains available but must be chosen explicitly. If the login
Keychain is locked, retry asks macOS to unlock the login Keychain first, then
stores the token while it is still only in memory. If an existing Keychain item
is readable, Keychain remains the default. If an existing item is unreadable,
use `hush keychain doctor` to confirm the state and `hush keychain repair` to
refresh the ACL for the current binary.

#### Unlock failure (exit 51)

If `security unlock-keychain ~/Library/Keychains/login.keychain-db` returns
exit 51 after the current Mac password is entered, that is usually a login
Keychain password mismatch, not a hush bug. The prompt is for the macOS login
Keychain itself; after password changes or migration, it may still require an
older password.

Hush never captures or stores that password. The safe responses are:

- Retry with the correct/older login Keychain password.
- Choose the dedicated hush Keychain option (`[h]`) to store the bot token in
  `<state_dir>/hush.keychain-db` without touching `login.keychain-db`.
- Repair the login Keychain in Keychain Access or System Settings.
- Use env-token fallback for this session only.

If the Keychain password is unknown, resetting the login Keychain is an
OS-level destructive repair outside hush and is intentionally not automated.

#### Initial store failure vs. dedicated Keychain vs. existing-item repair

- Initial store failure means no `hush-discord` item was created. Retry/unlock
  is the right fix while the pasted token is still in memory. `hush keychain
  repair` cannot repair a missing item.
- Dedicated hush Keychain means hush stores the bot token in
  `<state_dir>/hush.keychain-db` and records that path as `bot_keychain_path` in
  config. Future `serve`, `doctor`, and `repair` commands target that Keychain
  path directly. This avoids a broken login Keychain without writing plaintext
  token files or resetting macOS Keychain state.
- Existing-item repair means the item exists but current hush cannot read it.
  `hush keychain doctor` reports that state, and `hush keychain repair` refreshes
  the macOS ACL without asking for the Discord token again.

#### Keychain ACL repair reference

When the existing `hush-discord` Keychain item is unreadable, the guided flow
renders an ACL-denial panel and offers ACL repair as choice `[1]`. The exact
`security` command the panel emits is:

```bash
security set-generic-password-partition-list \
  -S apple-tool:,apple: \
  -s hush-discord -a "$USER" \
  ~/Library/Keychains/login.keychain-db
```

Substitute the `-a` account for whatever the original item was created with
(the panel prints the exact pair). After running it, return to the guided flow
and pick `[1]` again, or run `hush keychain repair` directly; hush re-runs only
the Keychain readability check from the preflight registry.

If repair is not feasible, choice `[2]` (delete-and-recreate, requires typing
`delete` to confirm; audit-logged) and choice `[3]` (env-token fallback per the
table above) remain available. After env-token fallback, `hush keychain doctor`
will report missing because nothing was stored in Keychain.

For the operational walkthrough, see `docs/OPERATIONS.md` §1
("First-run setup") and §4 ("Structured error reference").

---

## 3. Seven security layers

Each layer is independent. Compromise of any single layer MUST NOT enable
secret extraction.

### Layer 1 — Key hierarchy (no key files on disk)

All cryptographic keys are derived at runtime from the vault passphrase via
BIP32 HD derivation. The passphrase is the single root of trust.

```
passphrase + salt → Argon2id(time=4, mem=256MB) → 64-byte master seed
                                                      │
              ┌───────────────────────────────────────┘
              │
         BIP32 Master Key
              │
              ├── m/44'/7743'/0'  →  JWT signing key (secp256k1)
              ├── m/44'/7743'/1'  →  Vault encryption key (AES-256)
              ├── m/44'/7743'/2'  →  Audit log signing key (secp256k1)
              └── m/44'/7743'/3'
                    ├── /0  →  Client key for machine 0
                    └── /1  →  Client key for machine 1
```

`7743` is a custom coin-type; matches the vault port; no collision with
standard BIP44 coins.

**Implications:** No `signing.key` to steal. Backup is just the encrypted
vault file + salt. Key rotation is passphrase rotation — `hush vault
rekey` performs that rotation in place (see
[`docs/VAULT-REKEY.md`](VAULT-REKEY.md)).

### Layer 2 — Asymmetric JWT signing (ES256K)

Tokens are signed with ES256K (ECDSA over secp256k1, the Bitcoin curve). Only
the server can sign. Even leaking the public key does not enable forgery.

JWT claims:

| Claim | Purpose |
|-------|---------|
| `iss` | `"hush"` |
| `iat` / `exp` | Issued / expires |
| `jti` | UUID for use-count + revocation tracking |
| `scope` | Approved secret names |
| `client_ip` | Bound to requesting Tailscale IP |
| `request_id` | Ties to Discord approval |
| `max_uses` | Remaining uses (interactive only) |
| `ephemeral_pubkey` | Client's per-session ECIES pubkey |
| `session_type` | `"interactive"` or `"supervisor"` |

### Layer 3 — ECIES end-to-end secret transport

Client generates an ephemeral secp256k1 keypair per session. The pubkey is
sent in `/claim` and embedded in the JWT. Each `/secrets/{name}` response is
ECIES-encrypted to that pubkey.

```
Client                                  Server
──────                                  ──────
generate ephemeral keypair
send pubkey with claim  ─────────────►  store in JWT

GET /h/{prefix}/s/X     ─────────────►  encrypt secret with
                                         client ephemeral pubkey
                       ◄─────────────   return ECIES ciphertext

decrypt with privkey
inject as env var
zero ephemeral privkey
```

Secrets are double-encrypted in transit (ECIES inside Tailscale WireGuard).
No HTTP middleware, debug proxy, or memory dump of the HTTP stack ever sees
plaintext.

### Layer 4 — Client request signing (ECDSA)

Each agent machine has a registered client keypair (BIP32 path
`m/44'/7743'/3'/{machine_index}`). Every `/claim` and `/revoke` request is
ECDSA-signed by the client over a canonical-JSON payload (alphabetical keys,
compact form, SHA-256 hash signed via go-bitcoin Bitcoin-message signing).

Server verifies:
1. Signature ↔ a registered client public key.
2. Source IP ↔ allowlist.
3. Nonce uniqueness within 60s.
4. Timestamp ±30s.

Two independent factors: **what the client has** (private key in memory) +
**where the client is** (Tailscale IP).

### Layer 5 — Secure memory (mlocked + zero on free)

All sensitive material is wrapped in `SecureBytes`:
- `mlock()` prevents swap.
- Explicit zeroing on shutdown / SIGTERM.
- Runtime finalizer zeros + munlocks on GC.
- `[]byte`-only — secrets NEVER stored as Go `string`.
- Custom JSON unmarshaling reads secret values directly into `SecureBytes`.
- Intermediate buffers (during ECIES encrypt/decrypt) are zeroed before release.

**Known limitation:** Go's GC may relocate heap objects during compaction.
`SecureBytes` uses `mlock` to pin allocations, which prevents both swap and
relocation for the pinned region. Code paths that briefly convert secret
bytes to `string` create uncontrolled copies — implementation MUST audit all
paths to ensure secrets never touch `string`. Documented as residual risk
against root-level memory forensics; outside the commodity-malware threat
model.

### Layer 6 — Tamper-evident audit log

Every audit event is ECDSA-signed (audit key from `m/44'/7743'/2'`) and
hash-chained:

```json
{
  "seq": 42,
  "timestamp": "2026-04-05T14:30:00Z",
  "action": "secret_fetched",
  "data": {"secret": "ANTHROPIC_API_KEY", "client_ip": "100.97.178.13", "request_id": "8f3a1c2d"},
  "prev_hash": "a1b2c3d4e5f6...",
  "hash": "f6e5d4c3b2a1...",
  "signature": "H+DLx8v3..."
}
```

Each event's `hash` covers the whole event including `prev_hash`. Modification,
deletion, or insertion breaks the chain. The signed file
`~/.hush/audit.jsonl` is the authoritative record; Discord audit channel is
the convenience layer.

### Layer 7 — Obscurity

Additive only — never load-bearing. Helps the system disappear from automated
tooling.

| Measure | Hides |
|---------|-------|
| No key files on disk | `find -name '*.key'` finds nothing |
| Custom vault file format | "HUSH" magic — no standard tool recognizes it |
| Random API path prefix | `/h/{prefix}/...` — port probes get 404 on standard paths |
| ECIES-encrypted responses | Captured traffic shows binary blobs |
| Non-obvious binary name | `hush` reveals nothing to scanners |

---

## 4. Daemon-specific security (`hush supervise`)

`hush supervise` exists because daemon restart behavior is itself a security
and reliability concern.

**Without a supervisor:**
- Crashes trigger new approvals repeatedly.
- Overnight failures become outages.
- Humans get trained to auto-approve.
- A 3am daemon crash blocks the service until morning.

**With a supervisor:**
- One approval covers a bounded session.
- Child restarts within session TTL do NOT trigger new approvals.
- Stale credentials are surfaced explicitly via three independent channels.
- Validators run BEFORE the child sees the secret.
- The vault server is kept isolated from outbound internet (validators run on
  the supervisor, not the server).

The supervisor is an operational layer ON TOP of Layers 1–7. Its security
properties (grace-window plaintext cache, supervisor-side outbound calls)
are documented as residual risks (§6) and toggled per-supervisor in TOML.

---

## 5. Crypto requirements

| Requirement | Implementation |
|-------------|----------------|
| KDF | Argon2id (time=4, memory=256MB, threads=4, keyLen=64) |
| Symmetric encryption | AES-256-GCM |
| Asymmetric signing | secp256k1 ECDSA (ES256K via custom JWT signing method) |
| Asymmetric encryption | ECIES (secp256k1 via go-bitcoin) |
| Key derivation | BIP32 HD (custom coin type 7743) |
| Hash | SHA-256 (canonical-JSON digest, audit-chain hash) |
| Random | `crypto/rand` ONLY — `math/rand` is forbidden in security-critical paths |
| Signing payload | Canonical JSON (alphabetical keys, compact form) hashed with SHA-256 |

---

## 6. Known limitations & residual risks

Documented for transparency. These are accepted trade-offs.

| Limitation | Severity | Explanation |
|-----------|----------|-------------|
| Go GC may copy heap objects | Medium | Mitigated by `mlock` and `[]byte`-only mandate. Residual against root memory forensics. |
| ECIES protects transit, not at-rest in process memory | Low | After decryption, secrets live as env vars in the child. Readable via `/proc/{pid}/environ` (Linux) or `ps eww` (macOS) by same-user processes. Accepted vs. file-based secrets. |
| "No key files on disk" scope | Informational | Protects against scanner enumeration. Does NOT protect against an attacker who has Keychain access AND knowledge of the BIP32 scheme. ACL restriction reduces this. |
| Discord dependency | Medium | If Discord is unreachable, no new sessions. Existing sessions continue. Plan full-day TTLs. |
| Single passphrase as root of trust | Medium | Forgotten passphrase = unrecoverable vault (by design). Shamir splitting is a future extension. Until then, store the passphrase in a physical backup (paper in safe). |
| `--format eval` stdout leakage | Medium | Plaintext printed to stdout — captured by terminal scrollback, tmux, `script`. Use `--exec` whenever possible. `--format eval` is opt-in. |
| NTP clock skew | Low | 30s timestamp window requires synced clocks. Server and supervisor refuse to start if unsynced. |
| Grace-window plaintext cache in supervisor memory | Medium | When `cache_secrets_for_restart=true`, supervisor holds last decrypted secrets in mlocked memory for `grace.window` (default 60m, capped 4h) beyond JWT validity. Doubles on-host plaintext surface (child + supervisor). Approval becomes a gate on first arrival, not ongoing presence. **Opt-in per supervisor**; `--no-cache` disables it. |
| Log-pattern detection is version-coupled | Low | Patterns can drift across child versions. Primary signals are validators (fetch-time) and exit-78 (child contract). Log patterns are alert-only. |
| Supervisor validators make outbound calls from agent host | Low | Validators hit `api.anthropic.com`, `api.openai.com`, etc. — the same endpoints the child will hit anyway. **Vault server makes no outbound calls.** Validators can be disabled per supervisor. |
| Linux Secret Service retrievals leak an unzeroable string copy | Medium (Linux only) | `zalando/go-keyring` exposes only string APIs on Linux, so `linuxKeychain.Retrieve` necessarily routes the bot token / per-machine client signing scalar through a Go string (`v := backend.Get(...)` → `securebytes.New([]byte(v))`). The string copy lives in unmlocked heap until GC; mlock is applied only to the downstream SecureBytes. macOS retrievals are unaffected (the `/usr/bin/security` shell-out yields raw `[]byte`). Eliminating the residual requires swapping libraries or talking godbus directly. |
| `--input-file` JSON parsers leave secret strings unzeroable | Low | `hush init server --non-interactive --input-file`, `hush init client --non-interactive --input-file`, and `hush secret add --non-interactive --input-file` invoke `json.Unmarshal` which allocates Go strings for `vault_passphrase`, `discord_bot_token`, and `value`. The file body `[]byte` is zeroed before the helper returns; the JSON-struct strings cannot be (Go strings are immutable). They become unreachable as soon as the parser helper returns and survive in heap until GC. Documented and unavoidable while the wire format is JSON. |
| Agent-context fields are operator-visible, not authenticators | Low | `/claim` accepts five optional fields (`agent_identity`, `agent_model`, `tool_name`, `command_preview`, `recent_summary`) that an agent supplies for the human approver's benefit. They appear in the Discord embed and the signed hash-chained audit log. **A compromised agent can lie in any of them** — they are NOT used for authorization. Authorization continues to trust the ECDSA client signature + Tailscale peer IP + registered machine fingerprint. Redaction of `command_preview` is **best-effort defense-in-depth** (regex catalog in `internal/redact`); it is NOT a confidentiality guarantee. Operators should treat the agent-context rows as hints, not proofs. |

---

## 7. Phase 0 security goal

By the end of Phase 0, anyone reading the repo cold MUST be able to answer:

- What threat is being eliminated? (Commodity malware scanning agent dotfiles for secrets.)
- What is out of scope? (Root-level memory forensics; nation-state opponents; multi-owner approval.)
- Why is the supervisor model mandatory for daemons? (Crash-induced re-approval storms train humans to auto-approve and cause 3am outages.)
- Why are validators on the supervisor and not the vault server? (To preserve the vault's no-outbound-internet boundary.)
- Why is the grace-window cache opt-in? (Trades stricter secret isolation for restart resilience.)

If any of these is unclear after reading this doc + `docs/ARCHITECTURE.md`,
this doc is wrong.

---

## 8. Cross-references

| Topic | See |
|-------|-----|
| Components, data flow, lifecycle | `docs/ARCHITECTURE.md` |
| API payloads + signature canonicalization | `docs/API.md` |
| Server config + supervisor TOML | `docs/CONFIG-SCHEMA.md` |
| Lifecycle scenarios | `docs/LIFECYCLE-SCENARIOS.md` |
| Operational runbooks | `docs/OPERATIONS.md` |
| Vault passphrase rotation (`hush vault rekey`) | `docs/VAULT-REKEY.md` |
| Constitutional principles | `.specify/memory/constitution.md` |
