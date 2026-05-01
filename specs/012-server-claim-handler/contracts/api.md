# Contract — `POST /h/<prefix>/claim`

This contract is the SDD-12 lock on the HTTP shape of the `/claim` endpoint. It binds `internal/server`'s implementation and the `hush request` / `hush supervise` clients to the same wire format. Any future change to a status code, error label, request field, or response shape requires a new SDD chunk.

> Path prefix `<prefix>` is a per-deployment opaque string (6–32 chars, `[A-Za-z0-9_-]`) supplied by `cfg.Server.PathPrefix`. Mounted via `(s *Server).RegisterHandlers()` calling `(s *Server).Mount(http.MethodPost, "/claim", ...)` (chassis prepends `/h/<prefix>`).

---

## Request

`POST /h/<prefix>/claim`

**Headers**:
- `Content-Type: application/json` (required; mismatch → `400 bad_request`)
- `Content-Length: <= 65536` (chassis cap; over → `413 Payload Too Large`)

**Body** (JSON object — keys in any order; encoder MUST canonicalise to alphabetical order before signing):

```json
{
  "scope":                  ["ANTHROPIC_API_KEY", "GITHUB_TOKEN"],
  "reason":                 "human-readable string ≤ 256 chars",
  "ttl":                    "8h",
  "session_type":           "interactive",
  "ephemeral_pubkey":       "02b7…<66 hex chars>",
  "nonce":                  "<base64url, 8..128 chars>",
  "timestamp":              "2026-04-30T18:23:11.123456789Z",
  "signature":              "<base64 ECDSA over canonical JSON of all fields above plus request_id and machine_name>",
  "request_id":             "rq_a3b9...<16..64 chars>",
  "machine_name":           "starbird.local",
  "client_key_fingerprint": "9f81a4b6e0c0214d"
}
```

**Field-level validation**: see [data-model.md §1](../data-model.md#1-claimrequest--request-body-http-wire). Unknown fields are rejected (`json.Decoder.DisallowUnknownFields`).

---

## Responses

Exactly two response shapes exist: success (one shape) and failure (one shape). The handler emits exactly one of them per request.

### Success — `200 OK`

```json
{
  "jwt":        "<ES256K JWT, three base64url segments>",
  "expires_at": "2026-04-30T22:23:11Z",
  "jti":        "f7fa1c0a-9be3-4f2c-8d50-30b62b7a4b54"
}
```

- `jwt` — encoded ES256K JWT signed with the JWT-signing key (BIP32 path `m/44'/7743'/0'`). Claims include `scope`, `client_ip`, `session_type`, `request_id`, `max_uses` (0 for supervisor; `cfg.Crypto.DefaultMaxUses` for interactive), `ephemeral_pubkey`, `iat`, `exp`, `jti`.
- `expires_at` — RFC3339Nano formatting of `Token.ExpiresAt`. Equals `Now().Add(min(req.TTL, cfg.Crypto.MaxInteractiveTTL or MaxSupervisorTTL))`.
- `jti` — UUIDv4 identifying the issued token.

No other fields. The body is exactly `{"jwt": …, "expires_at": …, "jti": …}`.

### Failure — every non-200

All failure responses share the identical shape:

```json
{
  "error":      "<one of the ten static codes>",
  "request_id": "<32-char chassis-assigned hex>"
}
```

`request_id` is **always** the chassis-assigned ID from the request-ID middleware (32 lowercase hex chars), never the client-supplied `request_id` from the body. This is intentional: it gives the operator a single correlation key whether the body parsed or not.

#### Status / error code matrix

| Status | `error` value | When | Pre-conditions |
|--------|---------------|------|----------------|
| `400` | `"bad_request"` | Body malformed, missing required field, unknown field, `ttl ≤ 0`, `session_type` not in enum, `request_id` absent / malformed | shape stage |
| `403` | `"bad_signature"` | `sign.Verify` returns `ErrSignatureInvalid` OR `client_key_fingerprint` is unknown | sig stage |
| `403` | `"nonce_replay"` | `sign.NonceCache.Add` returns `firstSeen=false` or `ErrNonceReplay` | nonce stage |
| `403` | `"stale_timestamp"` | `sign.IsFreshTimestamp` returns false | ts stage |
| `403` | `"ip_not_allowed"` | request peer not in `cfg.Network.AllowedCIDRs` (handler-level recheck) | ip stage |
| `403` | `"denied"` | approver returns `ErrApproverDenied` | post-approval |
| `408` | `"approval_timeout"` | approver returns `ErrApproverTimeout` (or any error wrapping `context.DeadlineExceeded`) | post-approval |
| `429` | `"rate_limited"` | approver returns `ErrApproverRateLimited` | post-approval |
| `503` | `"discord_unavailable"` | approver returns `ErrApproverUnavailable` | post-approval |
| `503` | `"unknown_outcome"` | approver returns any other non-nil error, OR `(Decision{Approved:false}, nil)` | post-approval |

**Forbidden response fields** (FR-018, FR-019): `scope`, `reason`, `ttl`, `nonce`, `signature`, `ephemeral_pubkey`, `machine_name`, `client_key_fingerprint`, `decision_at`, anything else. The JSON encoder writes exactly the two keys.

---

## Headers on every response

- `Content-Type: application/json; charset=utf-8`
- `X-Content-Type-Options: nosniff`
- `Cache-Control: no-store`

No `X-Request-ID` echo header (the body carries `request_id`; an additional header is redundant and one more thing to forget to redact).

---

## Constitution-locked invariants (non-negotiable)

1. **No 200 without `(Decision{Approved:true}, nil)` from `approverImpl.RequestApproval`.** No flag, env var, build tag, runtime mode, or partial-success branch flips an error return into a 200. (Constitution II; SC-004.)
2. **TTL ceiling applied before approver call.** The TTL the operator's prompt shows MUST be the same TTL that the JWT carries. (FR-016; SC-005.)
3. **Exactly one audit event per request.** No duplicates, no omissions, regardless of where the pipeline fails or which outcome is reached. (FR-021; SC-003.)
4. **Error response bodies contain exactly two fields.** No echo of request body fields beyond the chassis-assigned `request_id`. (FR-018, FR-019; SC-002.)
5. **Operational logs never contain signature, nonce, ephemeral pubkey, reason, JWT, machine name, or scope contents.** Audit logs ARE allowed to contain `scope` (sorted-joined names) per FR-022; operational logs are not (Constitution X log-vs-audit asymmetry).

---

## Backward compatibility

This is the v0.1.0 contract. SDD-12 is the lock point. Future SDDs MAY add fields to the success response (200 body) only as additions, never removals. Future SDDs MAY add new outcome→`error` codes to the failure enumeration only by appending; existing codes never change meaning.

A non-additive change (renaming an error code, repurposing a status, etc.) requires:
- a new SDD chunk
- coordinated `hush request` / `hush supervise` client release
- a constitution-amendment-class review (this is the load-bearing endpoint of the entire product)
