# API

## Scope

The HTTP contract between client and vault server, plus the local
supervisor status socket. All server routes live under a random opaque
prefix:

- `/h/<prefix>/...`

The local supervisor status socket exposes a separate local-only API.

---

## Server endpoints

### `POST /h/<prefix>/claim`

Purpose:
- request a new interactive or supervisor session

Required request fields:
- `scope`
- `reason`
- `ttl`
- `session_type`
- `ephemeral_pubkey`
- `nonce`
- `timestamp`
- `signature`
- `request_id`
- `machine_name`
- `client_key_fingerprint`

Behavior:
- verify client identity
- verify nonce + timestamp replay protections
- verify client IP / allowed network policy
- send Discord approval request
- issue scoped JWT on approval
- return denial or timeout cleanly when not approved

### `GET /h/<prefix>/s/<name>`

Purpose:
- fetch one secret under an approved session

Behavior:
- validate JWT signature and expiry
- validate scope membership
- validate IP binding
- validate token use/exhaustion rules
- return ECIES-encrypted secret payload as binary

### `POST /h/<prefix>/revoke/<jti>`

Purpose:
- revoke an active session token immediately

Behavior:
- require signed authorized request
- mark token exhausted/revoked in session state
- emit audit event

### `GET /h/<prefix>/hz`

Purpose:
- health/status endpoint for basic server readiness

Expected status dimensions:
- vault loaded
- Discord connected/disconnected
- config valid
- clock-sync status

### `POST /h/<prefix>/claim` — optional agent-context fields (PR 4)

In addition to the required fields documented above, `/claim` now
accepts five OPTIONAL fields that an agent populates so the human
approver can spot anomalies in the Discord prompt:

| Field | Max length | Purpose |
|---|---|---|
| `agent_identity` | 128 | Agent name + version (e.g. `claude-code/1.2.3`) |
| `agent_model` | 64 | Model identifier (e.g. `claude-opus-4-7`) |
| `tool_name` | 64 | The tool the agent is about to invoke (e.g. `Bash`) |
| `command_preview` | 1024 | First N chars of argv; client-side redacted for common secret patterns, server-side re-redacted as belt-and-braces |
| `recent_summary` | 256 | One-line activity context |

All fields are `omitempty` on the wire. They appear in the signed
canonical payload as empty strings when unset, so clients that omit
them produce signatures byte-identical to a pre-PR-4 client.

The server enforces the length caps at shape-validation time
(returning `400 bad_request` on violation) and re-runs
`internal/redact.CommandPreview` on `command_preview` AFTER signature
verification — redacting before verify would mutate the bytes the
client signed over.

**Security note**: these fields are operator-visible metadata for
anomaly detection. They are NOT authenticators. A compromised agent
could lie in any of them. Authorization decisions trust the
cryptographic identity (client signature, `machine_name`, peer IP)
only.

### `POST /h/<prefix>/me`

Purpose:
- capability + freshness introspection for enrolled clients (agent planning)
- **never** triggers a Discord approval

Required request fields (signed body, same crypto as `/claim`):
- `nonce`
- `timestamp`
- `signature`
- `request_id`
- `machine_name`
- `client_key_fingerprint`

Optional headers:
- `Authorization: Bearer <jwt>` — when present and valid, the response
  includes a `current_session` block. An invalid bearer is silently
  ignored (the response simply omits `current_session`) — `/me` never
  returns 401 for a bad bearer because the signed body already
  authenticated the caller.

Response (success, 200):
- `schema_version` (int, currently `1`)
- `server_version` (string, semantic version of the server binary)
- `scopes_available` (array of string scope names; values never returned)
- `current_session` (object, present only when bearer validates):
  - `jti`
  - `expires_at` (RFC3339)
  - `scopes`
  - `max_uses`
  - `session_type` (`interactive` / `supervisor`)
- `next_refresh_window` (omitted server-side — the refresh window is a
  supervisor concept; fetch it from the supervisor's status socket)

Failure outcomes:
- 400 `bad_request` — malformed body
- 403 `bad_signature` — signature invalid or fingerprint unknown
- 403 `nonce_replay` — nonce already seen within `NonceTTL`
- 403 `stale_timestamp` — timestamp outside `ClockSkew`
- 403 `ip_not_allowed` — source IP not in Tailscale CIDR allow-list

Audit:
- emits exactly one `me_query` event per request, with
  `detail.outcome` ∈ {`ok`, `bad-request`, `bad-signature`,
  `nonce-replay`, `stale-timestamp`, `ip-not-allowed`}.

Side-effect-free relative to session state:
- never decrements the bearer JWT's remaining uses
- never invokes the approver
- consumes one nonce-cache slot (shared with `/claim`)

---

## Local supervisor socket endpoint

The local supervisor socket is a Unix-domain control plane protected by
filesystem permissions. It accepts one line per connection. Unknown or empty
input renders the status document for backward compatibility.

### `status`

Purpose:
- provide agent-visible freshness and runtime state for one local supervisor

Expected response shape:
- `supervisor`
- `state`
- `session_expires_at`
- `refresh_window_next`
- `scope_healthy`
- `scope_stale`
- `last_auth_failure`
- `child_pid`
- `child_uptime`
- `discord_connected`

See `docs/CONFIG-SCHEMA.md` for the canonical JSON shape.

### `refresh`

Purpose:
- silently refill secrets under the existing supervisor session
- re-run validators and restart the supervised child with the refilled
  environment

Behavior:
- does not issue `/claim`
- does not trigger a Discord approval prompt
- returns `{"ok":true}` on success, or `{"ok":false,"error":"..."}`

Use `refresh` after vault-side secret rotation or stale-credential repair
when the current supervisor session is still the intended approval boundary.

### `renew[ <json>]`

Purpose:
- request a fresh supervisor approval through `/claim`
- swap the supervisor to the newly-approved session
- optionally restart the child after approval

Request body:
- omitted or `{}` — seamless renewal; child keeps running
- `{"restart":true}` — restart the child after the session renewal succeeds

Response:
- `ok` (bool)
- `outcome` (`renewed`, `denied`, `timeout`, `refused-state`, `error`)
- `restarted` (bool)
- `session_expires_at` (RFC3339, present on success when known)
- `jti` (session identifier, present on success when known)
- `error` (string, present on failure)

`renew` preserves the normal Discord approval path. It never
auto-approves and never returns JWT or secret bytes.

### `reload[ <json>]`

Purpose:
- request a zero-downtime HTTP-proxy child handoff for reload-eligible
  supervisors

Request body:
- `{"config_path":"..."}` — operator-supplied config path for audit
  attribution; the supervisor performs the swap from its already-loaded
  config

Response:
- `ok` (bool)
- `result` (`ok`, `readiness-failed`, `config-invalid`,
  `swap-in-flight`, `error`)
- old/new PID, readiness duration, strategy, and error fields as applicable

---

## API invariants

- no public endpoint model
- no auto-approve path
- no plaintext secret response body
- no endpoint that writes secret material to agent disk
- validators run on the supervisor, not the vault server API surface
- local status socket is filesystem-authenticated, not bearer-token authenticated

---

## Cross-references

- transport and trust boundaries: `docs/ARCHITECTURE.md`
- config fields and status schema: `docs/CONFIG-SCHEMA.md`
- runtime scenarios: `docs/LIFECYCLE-SCENARIOS.md`
