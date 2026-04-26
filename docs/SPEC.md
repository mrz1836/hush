# SPEC — hush v0.1.0

> The build-grade specification for hush. This document is the single source of
> truth for what v0.1.0 ships. Phases, packages, and tests trace back to
> requirements and acceptance criteria here. If implementation contradicts SPEC,
> implementation is wrong.

---

## 1. Product

**hush** is a Discord-gated secrets broker for AI agents.

It separates **secret custody** (encrypted vault on a single host) from
**secret access** (short-lived, human-approved sessions over Tailscale) and
**secret consumption** (env vars in process memory only). Long-running daemons
use a supervisor lifecycle so a single Discord approval covers crashes,
updates, and restarts within a bounded session TTL.

Single binary: `hush`. Subcommands: `serve`, `request`, `supervise`, `init`,
`secret`, `client`, `health`, `revoke`, `version`.

---

## 2. Core guarantees

These five guarantees define the product. Every requirement in this spec exists
to enforce them.

| # | Guarantee | Enforced by |
|---|-----------|-------------|
| G1 | **No secrets persist on agent machines.** | Clean-machine checklist; env-only delivery; no secret files written by hush client. |
| G2 | **Approval is human and out-of-band.** | Discord DM with interactive buttons; no auto-approve mode; bot-down → 503. |
| G3 | **Vault server is never publicly reachable.** | Tailscale-only bind; ACL grant scoped to `tag:trusted → tag:sandbox:7743`. |
| G4 | **Daemons do not re-prompt on every restart.** | `hush supervise` holds JWT + ephemeral ECIES key across child restarts within session TTL. |
| G5 | **Stale credentials fail loudly and visibly.** | Validators (fetch-time), exit-code 78 contract, log-pattern watchdog (alert-only), local status socket, three distinct Discord alert formats. |

---

## 3. Primary users

| User | Role | Primary interaction |
|------|------|---------------------|
| Z (the human) | Vault owner, sole approver | Discord DMs on phone; `hush secret` on vault host |
| OpenClaw daemon | Long-running agent runtime on the Mac Mini | Receives secrets via env vars from `hush supervise` |
| Hermes daemon | Long-running gateway on the Mac Mini (T-161) | Receives secrets via env vars from `hush supervise` |
| Interactive shells | Z's day-to-day dev sessions | `hush request --exec "zsh"` wraps the shell, not the app |
| Downstream agents | Claude Code, cron jobs, ACP threads | Query `hush client status --json` to refuse running on stale creds |

Out-of-band users are explicitly excluded: there is no service account, no
remote API for third parties, no shared Discord approval pool. Only Z approves.

---

## 4. Functional Requirements

Each requirement is testable and traces to one or more acceptance criteria
(see §6) and, once implementation starts, one or more execution tasks in `tasks.yaml`.

### FR-1 — Single binary, cobra subcommands
A single statically-linked Go binary `hush` exposes nine subcommands. No
Makefile; build via MAGE-X. Released via GoReleaser for darwin/linux,
amd64/arm64.

### FR-2 — Encrypted vault file
Secrets are stored in `~/.hush/secrets.vault` as a binary file with magic
`HUSH`, version byte, 16-byte Argon2id salt, 12-byte AES-GCM nonce, and the
AES-256-GCM ciphertext + tag of a JSON payload. Argon2id parameters: `time=4`,
`memory=256MB`, `threads=4`, `keyLen=64`.

### FR-3 — BIP32 key hierarchy from passphrase
All cryptographic keys are derived at runtime via BIP32 HD derivation from the
Argon2id-derived 64-byte master seed. **Zero key files exist on disk.** Paths:

| Path | Purpose |
|------|---------|
| `m/44'/7743'/0'` | JWT signing key (secp256k1 ES256K) |
| `m/44'/7743'/1'` | Vault encryption key (AES-256) |
| `m/44'/7743'/2'` | Audit log signing key (secp256k1 ECDSA) |
| `m/44'/7743'/3'/{machine_index}` | Per-agent client keypair |

### FR-4 — ES256K JWT sessions
Session tokens are JWTs signed with secp256k1 ECDSA (custom signing method
`ES256K` registered with `golang-jwt/jwt/v5`). Required claims: `iss="hush"`,
`iat`, `exp`, `jti` (UUID), `scope` (string array of secret names),
`client_ip`, `request_id`, `max_uses`, `ephemeral_pubkey`, `session_type`.

`session_type` is one of `"interactive"` or `"supervisor"`. Interactive tokens
enforce `max_token_uses`. Supervisor tokens are TTL-only.

### FR-5 — ECIES end-to-end secret transport
Each secret-fetch response body is an ECIES ciphertext encrypted to the
client's ephemeral secp256k1 public key (provided in `/claim` and embedded in
the JWT). Client decrypts with the ephemeral private key, which exists only in
client memory for the session. No plaintext secret value ever appears in HTTP
response bodies, logs, or middleware.

### FR-6 — ECDSA-signed client requests
Every request to `/claim` and `/revoke` is signed with the agent's registered
client key (BIP32 path `m/44'/7743'/3'/{machine_index}`). Signature payload is
canonical JSON (alphabetical key order, compact form, `SHA256` digest signed
via go-bitcoin Bitcoin-message signing). Server verifies signature, IP
allowlist, nonce uniqueness within a 60s window, and timestamp freshness
(±30s).

### FR-7 — Discord approval flow
A dedicated Discord bot named `hush` sends interactive DMs to Z's user ID
when `/claim` is hit. DMs show machine name, client IP, scope, reason,
requested TTL, and `[Approve <ttl>] [Deny]` buttons. Supervisor sessions get a
distinct `[DAEMON]` label with `[Approve <ttl>]` only (no 15-minute button).
Refresh-window nudges and three staleness alert formats (`[STALE] Validator
Failure`, `[STALE] Child Exit 78`, `[STALE] Log Pattern Match`) are
visually distinct.

### FR-8 — Tailscale-only network boundary
`listen_addr` MUST resolve to a Tailscale interface IP. Server refuses to
start if `listen_addr` is `0.0.0.0`, empty host, or a non-Tailscale interface.
Tailscale ACLs MUST grant `tag:trusted → tag:sandbox:7743` (additive to
existing port 22 + 5900 grant).

### FR-9 — Token use-count + IP binding + revocation
- Interactive tokens decrement `max_uses` on each `/secrets/{name}` fetch;
  exhaustion → 401.
- Every fetch validates the JWT's `client_ip` against the requesting Tailscale
  IP; mismatch → 401.
- `POST /h/{prefix}/revoke/{jti}` immediately marks a token as exhausted.

### FR-10 — Atomic vault writes + SIGHUP hot-reload
`hush secret` subcommands write the new vault to a temp file in the same
directory, atomically rename, and set mode `0600`. If a running server is
detected, send SIGHUP. Server SIGHUP handler decrypts the new vault into a
**new** `Vault`, atomically swaps `atomic.Pointer[Vault]`, then explicitly
zeros the old vault's `SecureBytes`. In-flight requests using the old vault
complete safely.

### FR-11 — `hush supervise` daemon lifecycle
`hush supervise --config <path>` runs a state machine
(`fetching → running → crashed → running` for silent refill;
`* → awaiting-approval` for stale credentials, exit 78, or vault 401-unknown-jti).
The supervisor:

- Holds the JWT + ephemeral ECIES key across child restarts within session TTL
- Performs **silent refill** (no Discord call) on clean child exit within TTL
- Transitions to `awaiting-approval` on child exit 78 (regardless of TTL)
- Runs pluggable per-secret validators **before** injecting into the child
- Emits Discord alerts in three distinct formats per detection channel
- Exposes a Unix status socket (mode 0600) for `hush client status` queries
- Refreshes via Discord prompt anchored to a configured local time window
- Acquires a PID file + flock for split-brain protection
- Optionally caches secrets in mlocked memory for grace-window restart resilience

### FR-12 — Local status socket + `hush client`
Each running supervisor binds a Unix socket at:
- macOS: `~/Library/Caches/hush/supervise-{name}.sock` (mode 0600, parent dir 0700)
- Linux: `$XDG_RUNTIME_DIR/hush-supervise-{name}.sock` (mode 0600)

`GET /status` returns JSON:
```json
{
  "supervisor": "openclaw",
  "session_expires_at": "2026-04-15T06:12:00-07:00",
  "refresh_window_next": "2026-04-15T09:00:00-07:00",
  "scope_healthy": ["ANTHROPIC_API_KEY", "OPENAI_API_KEY"],
  "scope_stale": [],
  "last_auth_failure": null,
  "child_pid": 51234,
  "child_uptime": "8h12m",
  "discord_connected": true,
  "state": "running"
}
```

`hush client status [--supervisor NAME] [--json]` prints a human or JSON
summary. `hush client refresh --supervisor NAME` triggers a graceful refetch
+ child restart (used after `hush secret rotate` on the vault host).

### FR-13 — Pluggable credential validators
Built-in validators MUST exist for: `anthropic`, `anthropic-oauth`, `openai`,
`google-ai`, `github`. Each validator hits the cheapest read-only provider
endpoint and returns nil on success or an error on 401. **Validators run on
the supervisor**, never on the vault server.

### FR-14 — Audit log (signed + hash-chained)
All security-relevant events are appended to `~/.hush/audit.jsonl`. Each
record includes monotonic `seq`, `prev_hash`, `hash` (SHA-256 of the record
including `prev_hash`), and an ECDSA signature with the audit key
(`m/44'/7743'/2'`). If `discord_audit_channel_id` is configured, events are
mirrored to the channel.

Required event types: session_requested, session_approved, session_denied,
secret_fetched, token_expired, token_revoked, server_start, server_stop,
auth_failed, vault_reloaded, file_perm_check_failed, discord_disconnected,
discord_reconnected, supervisor_session_claimed, supervisor_session_refreshed,
supervisor_silent_refill, supervisor_child_clean_exit,
supervisor_child_exit_78, supervisor_stale_alert, supervisor_grace_entered,
supervisor_grace_exited, supervisor_awaiting_approval, client_refresh_invoked.

### FR-15 — File permissions enforced at startup
Server validates that every file in `~/.hush/` is mode `0600` (directory
itself `0700`). Any laxer permission → refuse to start with a clear error.

### FR-16 — Keychain ACLs (macOS)
All keychain entries (`hush`, `hush-discord`, `hush-client`) are created with
`-T /usr/local/bin/hush`. Any other binary attempting access triggers a
macOS Keychain prompt.

### FR-17 — Clock-sync startup check
On startup, server and supervisor MUST verify NTP sync (macOS:
`systemsetup -getusingnetworktime`; Linux: `timedatectl show`). Refuse to
start if unsynced or drift >60s.

### FR-18 — Refresh window scheduler
Supervisor sends a `[DAEMON] Refresh` DM at a configurable local-time window
(default `09:00–10:00`). A T-30min fallback nudge fires if the morning prompt
is ignored. The child is **never** force-restarted purely by TTL expiry; only
the supervisor's refill capability is gated.

### FR-19 — Bootstrap retry-with-backoff
At launchd start, supervisor retries vault and Tailscale reachability with
exponential backoff up to `boot_retry_timeout` (default `10m`), logging WARN
at each attempt. On exhaustion, exit non-zero so launchd's normal retry kicks
in. Discord prompts MUST NOT fire for network blips.

### FR-20 — Discord disconnect monitoring
Server monitors the bot's WebSocket connection. On unexpected disconnect:
log WARN, post audit event, refuse new `/claim` requests with 503. Health
endpoint reports `discord_connected: false`. On reconnect, resume normal
operation.

---

## 5. Non-functional requirements

| ID | Requirement |
|----|-------------|
| NFR-1 | 90%+ test coverage repo-wide; 100% for vault crypto / key derivation / JWT / ECIES / request signing. |
| NFR-2 | Fuzz tests for vault decrypt, ECIES decrypt, JWT validate, request-signature verify, TOML config parse. |
| NFR-3 | Pre-commit MUST pass `golangci-lint`, `go test -race`, and `go-pre-commit` (zero gitleaks findings). |
| NFR-4 | Single binary < 25 MB stripped. Cold start < 500 ms (excluding Argon2id KDF). |
| NFR-5 | Argon2id KDF must complete in < 5s on a 2026-class server CPU. |
| NFR-6 | Server handles ≥ 100 concurrent secret fetches without degradation. |
| NFR-7 | All sensitive material in `[]byte` / `SecureBytes` only. NEVER in Go `string`. |
| NFR-8 | Repo starts PRIVATE; flips to public only after the public-release checklist (constitution, §III) is satisfied. |
| NFR-9 | No deployment-specific data (real Tailscale IPs, Discord user IDs) committed to the repo. Examples use placeholders. |
| NFR-10 | All CLI output: text for TTY; JSON for pipes/redirects (auto-detected). |

---

## 6. Acceptance Criteria

These ACs are the MVP exit gate. Each maps to integration tests in
`docs/TESTING-STRATEGY.md` and, once implementation begins, to execution tracking in `tasks.yaml`.

### AC-1 — `hush serve` starts and serves
A fresh `hush init` followed by `hush serve` produces a running vault server
that responds to `GET /h/{prefix}/hz` over Tailscale within 5 seconds.

### AC-2 — Vault round-trip
`hush secret add NAME` → `hush secret list` → `hush secret rotate NAME` →
SIGHUP hot-reload preserves all other secrets and atomically swaps the rotated
value with no in-flight request failures.

### AC-3 — Discord approval flow (interactive)
`hush request --scope X --reason Y --ttl 1h --exec "env | grep X"` triggers a
DM to Z, waits for approval, and on approval injects the secret into the child
process whose stdout shows the secret value. Denial returns exit 3 with no
secret leak.

### AC-4 — JWT lifecycle
After approval, the issued JWT (a) is rejected from a different IP, (b) is
rejected after `max_uses` fetches, (c) can be revoked via `hush revoke
--jti`, (d) carries `session_type` in its claims.

### AC-5 — `hush request --exec` injection safety
With `--exec`, secrets exist only in the child process's environment. The
ephemeral private key is zeroed from the client's memory after fetch. With
`--format eval` AND no `--exec`, a stderr warning is printed.

### AC-6 — Client registration + per-machine keys
`hush init --client --machine-index N` produces a unique client key per N.
Reusing the same N from a different passphrase produces a different key.
Keychain entries are ACL-restricted to `/usr/local/bin/hush`.

### AC-7 — End-to-end ECIES
A captured HTTP response body to `/h/{prefix}/s/{name}` contains no plaintext
secret value. Decrypting with the wrong ephemeral private key fails cleanly.

### AC-8 — Server hardening
- Server refuses to start with `listen_addr=0.0.0.0`.
- Server refuses to start with empty `allowed_client_ips`.
- Server refuses to start with empty `registered_client_keys` unless
  `client_signature_required: false`.
- Server refuses to start if any file in `~/.hush/` is more permissive than
  `0600`.
- Server refuses to start if NTP-unsynced or drift > 60s.

### AC-9 — Test coverage + fuzz
`magex test:race` reports ≥ 90% repo coverage and ≥ 100% for crypto/key/JWT/
ECIES/signing packages. `magex fuzz` runs vault decrypt + ECIES decrypt + JWT
validate fuzz targets for ≥ 60s each without crash.

### AC-10 — Supervisor lifecycle (11 named scenarios)
The supervisor integration suite passes the 11 scenarios documented in
`docs/LIFECYCLE-SCENARIOS.md`:

| # | Scenario |
|---|----------|
| 1 | First interactive shell request |
| 2 | First daemon bootstrap |
| 3 | Clean child exit → silent refill |
| 4 | Child crash within valid session TTL |
| 5 | Child exit 78 stale-credential contract |
| 6 | Validator catches bad secret before child start |
| 7 | Vault server restart mid-session (401 unknown-jti) |
| 8 | Daytime refresh-window prompt |
| 9 | Overnight expiry with and without grace cache |
| 10 | Discord unavailable during new claim |
| 11 | Tailscale boot retry / startup ordering recovery |

---

## 7. Out of scope (v0.1.0)

Documented for future extensions. Anything in this list MUST NOT block v0.1.0.

- Session presets / secret groups
- Shamir passphrase splitting (sigil's SSS)
- TLS within Tailscale
- Web dashboard
- Proxy model (vault proxying API calls instead of injecting tokens)
- Agent-side credential proxy (per-provider HTTP proxy)
- `SO_PEERCRED` on status socket for per-caller identity
- Slack / PagerDuty fallback for staleness alerts
- Multi-owner approval
- TOTP second factor on Discord approval

---

## 8. Build strategy

Follow `docs/IMPLEMENTATION-PLAN.md` — phased execution, docs-first, security
properties verified by tests not vibes. Phase 0 (this documentation pass)
exists to make every later phase straightforward and to preserve intent if a
new agent picks up the work mid-build.

---

## 9. Cross-references

| Topic | See |
|-------|-----|
| Trust boundaries + components | `docs/ARCHITECTURE.md` |
| Threat model + 7 layers | `docs/SECURITY.md` |
| HTTP/socket schemas + payload shapes | `docs/API.md` |
| Server config + per-supervisor TOML | `docs/CONFIG-SCHEMA.md` |
| Package layout + responsibilities | `docs/PACKAGE-MAP.md` |
| 11 supervisor lifecycle scenarios | `docs/LIFECYCLE-SCENARIOS.md` |
| Phase-by-phase execution order | `docs/IMPLEMENTATION-PLAN.md` |
| Test plan + fuzz + coverage | `docs/TESTING-STRATEGY.md` |
| Bootstrap, runbooks, outage handling | `docs/OPERATIONS.md` |
| MVP exit gate | `docs/MVP.md` |
| SDD execution rules | `docs/SDD-GUIDE.md` |
| Constitutional principles | `.specify/memory/constitution.md` |
