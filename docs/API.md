# API

## Scope

Phase 0 defines the contract shape before implementation hardens it.

All server routes live under a random opaque prefix:
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

---

## Local supervisor socket endpoint

### `GET /status`

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
