# Contract — `GET /h/<prefix>/s/<name>`, `POST /h/<prefix>/revoke`, `GET /h/<prefix>/hz`

This contract is the SDD-13 lock on the HTTP shapes of the three remaining vault-server endpoints. It binds `internal/server`'s implementations and the `hush request` / `hush supervise` / `hush revoke` clients to the same wire format. Any future change to a status code, error label, request field, or response shape requires a new SDD chunk.

> Path prefix `<prefix>` is per-deployment opaque (6–32 chars, `[A-Za-z0-9_-]`) supplied by `cfg.Server.PathPrefix` and mounted by `(s *Server).RegisterHandlers()`. The chassis prepends `/h/<prefix>` to the documented relative paths below.

---

## 1. `GET /h/<prefix>/s/<name>`

Fetch one secret under an approved session.

### Request

`GET /h/<prefix>/s/<name>`

**Headers**:
- `Authorization: Bearer <jwt>` — REQUIRED. Scheme MUST be `Bearer` (case-insensitive). Any other scheme, or an absent header, → `401 bad_token`.

**URL path**:
- `<name>` — REQUIRED. Pattern `^[A-Z][A-Z0-9_]{0,63}$`. A name that fails the pattern → `400 bad_request`.

**Body**: NONE. Any body bytes → `400 bad_request`.

### Responses

#### Success — `200 OK`

```text
HTTP/1.1 200 OK
Content-Type: application/octet-stream
Content-Length: <N>
Cache-Control: no-store
X-Content-Type-Options: nosniff

<N bytes of ECIES envelope>
```

The body is the raw ECIES envelope produced by `ecies.Encrypt(ctx, ephemeralPubKey, secretValue)`. The plaintext value never appears in the body — the envelope decrypts only with the ephemeral private key the client retained from `/claim`. No JSON wrapping. No metadata fields.

#### Failure — `4xx` / `5xx`

All failure responses share the identical shape:

```json
{
  "error":      "<one of the static codes below>",
  "request_id": "<32-char chassis-assigned hex>"
}
```

`request_id` is **always** the chassis-assigned ID from the request-ID middleware.

##### Status / `error` matrix

| Status | `error` | When |
|--------|---------|------|
| `400` | `bad_request` | name path malformed; non-empty body present |
| `401` | `bad_token` | header absent / malformed scheme; JWT malformed / signature invalid / unknown JTI / revoked / exhausted (interactive only) / wrong IP / unknown session type / wrong algorithm |
| `401` | `token_expired` | `token.ErrTokenExpired` only — distinct hint to legitimate clients (their token timed out; re-`/claim`) |
| `403` | `out_of_scope` | `token.ErrScopeViolation` (the requested secret name is not in the JWT's claimed scope) |
| `404` | `not_found` | name was in the JWT scope but is absent from the in-memory vault |
| `500` | `internal_error` | post-validate vault read error, ECIES encrypt error, or response write error — DEFENSIVE; should not occur in healthy operation |

**Forbidden response fields** (FR-005): in success, anything other than the raw envelope bytes. In failure, anything beyond `error` and `request_id`. The handler MUST NOT echo the requested name, the JWT, the JWT's claims, or any vault contents into the response body.

---

## 2. `POST /h/<prefix>/revoke`

Mark a previously-issued JTI as revoked.

### Request

`POST /h/<prefix>/revoke`

**Headers**:
- `Content-Type: application/json` (required; mismatch → `400 bad_request`)
- `Content-Length: <= 65536` (chassis cap; over → `413 Payload Too Large`)

**Body** (JSON object — keys in any order; encoder MUST canonicalise to alphabetical order before signing):

```json
{
  "jti":                    "f7fa1c0a-9be3-4f2c-8d50-30b62b7a4b54",
  "nonce":                  "<base64url, 8..128 chars>",
  "timestamp":              "2026-04-30T18:23:11.123456789Z",
  "request_id":             "rq_a3b9...<16..64 chars, optional>",
  "machine_name":           "starbird.local",
  "client_key_fingerprint": "9f81a4b6e0c0214d",
  "signature":              "<base64 ECDSA over canonical JSON of every field above except signature>"
}
```

**Field-level validation**: see [data-model.md §5](../data-model.md#5-revokerequest--revoke-request-body-http-wire). Unknown fields are rejected (`json.Decoder.DisallowUnknownFields`).

The signed canonical-JSON form is computed by `sign.CanonicalJSON({jti, nonce, timestamp, request_id, machine_name, client_key_fingerprint})` (alphabetical at every depth — same primitive used by `/claim`). The signature MUST verify against the public key resolved from `client_key_fingerprint` via the chassis's `Deps.ClientKeyResolver`.

### Responses

#### Success — `200 OK`

```json
{
  "revoked":    true,
  "request_id": "<32-char chassis-assigned hex>"
}
```

The body is identical for first-time success AND idempotent re-revocation per spec clarification §5 / FR-014. The first/idempotent distinction lives only in the audit chain (`revoke_succeeded` vs `revoke_idempotent_already_revoked`). HTTP MUST NOT distinguish.

#### Failure — `400` / `403`

Same shape as `/s` failure:

```json
{
  "error":      "<static code>",
  "request_id": "<32-char chassis-assigned hex>"
}
```

##### Status / `error` matrix

| Status | `error` | When |
|--------|---------|------|
| `400` | `bad_request` | malformed JSON, missing required field, unknown field, body > 64 KiB |
| `403` | `bad_signature` | `sign.Verify` returns `ErrSignatureInvalid` OR `client_key_fingerprint` unknown OR JTI was never issued (anti-enumeration; FR-015) |
| `403` | `nonce_replay` | `sign.NonceCache.Add` returns `firstSeen=false` or `ErrNonceReplay` |
| `403` | `stale_timestamp` | `sign.IsFreshTimestamp` returns false |

**Forbidden response fields** (FR-016): the body MUST NOT contain the supplied signature, the supplied nonce, the JTI bytes, or any field beyond the documented two/three keys.

### Authorisation rule

Any registered client (any signature that verifies against a fingerprint in the registry) is authorised to revoke ANY JTI. This matches the v0.1.0 trust model: the registry IS the authorisation list (the operator manages the registry on the trusted host). A future "originator-only revoke" rule is a constitutional amendment.

---

## 3. `GET /h/<prefix>/hz`

Operator readiness signal for monitoring within the trust mesh.

### Request

`GET /h/<prefix>/hz`

**Headers**: NONE required. The handler is reachable WITHOUT a session token (Constitution VI: the Tailscale mesh is the auth perimeter for this signal — FR-017).

**Body**: NONE.

### Response

#### Always `200 OK`

```json
{
  "status":            "ok",
  "uptime":            "1h23m",
  "secrets_count":     7,
  "active_tokens":     2,
  "discord_connected": true,
  "config_valid":      true,
  "vault_loaded":      true,
  "clock_in_sync":     true
}
```

| Field | Type | Source |
|-------|------|--------|
| `status` | string | constant `"ok"` |
| `uptime` | string (Go duration) | `time.Since(s.runStartedAt).Round(time.Second).String()` |
| `secrets_count` | integer | `len(vaultStore.Names())` (count, never names) |
| `active_tokens` | integer | `tokenStore.ActiveCount()` (count, never JTIs) |
| `discord_connected` | bool | `s.discordHealth()` (false if no probe wired) |
| `config_valid` | bool | `true` post-startup |
| `vault_loaded` | bool | `s.vaultPtr.Load() != nil` |
| `clock_in_sync` | bool | latest cached `clockProbe` result |

**Status MUST be `200`** regardless of operational state (FR-021). A `vault_loaded: false` field reports an early-startup state without a non-200 status.

**Forbidden response fields** (FR-019, SC-006):
- any secret name from the loaded vault
- any registered-client identifier
- any token identifier (JTI) or its claims
- the chat-platform bot token
- the audit signing key
- the audit chain's most recent hash
- the random API path prefix (beyond what the URL the request used already implies)

**No audit event is emitted for `/hz`** (spec clarification §4 / FR-021a).

---

## Headers on every response

All three endpoints emit:

- `Content-Type: application/json; charset=utf-8` (failure bodies, `/revoke` success, `/hz`)
- `Content-Type: application/octet-stream` (`/s` success only)
- `X-Content-Type-Options: nosniff`
- `Cache-Control: no-store`

No `X-Request-ID` echo header. The body carries `request_id`; one more header is one more thing to forget to redact.

---

## Constitution-locked invariants (non-negotiable)

1. **`/s` MUST NOT issue `200` without a successful `token.Validate(...)`.** No flag, env var, build tag, runtime mode, or partial-success branch flips a token-validation failure into a 200. (Constitution IV; FR-002, FR-043.)
2. **`/s` success body is the ECIES envelope and nothing else.** No JSON wrapping, no header echo of secret value, no log line carrying the secret value. (Constitution X; FR-005, SC-002.)
3. **`/revoke` MUST NOT mark a token revoked without a successful `sign.Verify(...)`.** A signature failure path that mutates the token store is a critical bug. (Constitution III Layer 4; FR-013, FR-044.)
4. **`/revoke` HTTP body MUST be identical for first-time and idempotent re-revocation.** The distinction is audit-only. (FR-014, spec clarification §5.)
5. **`/hz` MUST be reachable without a JWT.** Adding a JWT-required gate to `/hz` would couple operator readiness checks to claim issuance. (Constitution VI; FR-017.)
6. **`/hz` body MUST NOT echo any secret name, JTI, fingerprint, bot token, audit key, or audit hash.** The body carries counts, not identifiers. (Constitution X; FR-019, SC-006.)
7. **`/hz` MUST NOT emit an audit event.** Health probes are non-security-relevant operational signals. (FR-021a.)
8. **All three endpoints MUST emit exactly one audit event per request, EXCEPT `/hz`.** No duplicates, no omissions, regardless of where the pipeline fails. (FR-027; SC-007.)
9. **Operational logs MUST NOT carry the secret value, the JWT, the request signature, the request nonce, the bot token, the audit signing key, or the audit chain hash.** (Constitution X; FR-041, SC-015.)
10. **No configuration knob, env var, build tag, or runtime mode causes the audit writer to drop events on backpressure, publish secret values to the mirror, or weaken any chain-integrity invariant.** (FR-045.)

---

## Backward compatibility

This is the v0.1.0 contract. SDD-13 is the lock point for all three endpoints. Future SDDs MAY add fields to the success responses only as additions, never removals. Future SDDs MAY add new outcome → `error` codes only by appending; existing codes never change meaning.

A non-additive change (renaming an error code, repurposing a status, restructuring `/hz`) requires:

- a new SDD chunk
- coordinated client release (`hush request`, `hush supervise`, `hush revoke`, `hush client status`)
- a constitution-amendment-class review (these are the load-bearing endpoints of the entire product after `/claim`)
