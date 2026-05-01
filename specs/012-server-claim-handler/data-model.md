# Phase 1 Data Model — SDD-12 `/claim` handler

The handler owns five logical entities, four of which are HTTP-wire shapes; the fifth (the `Outcome` enum) is internal and exists only to drive the audit / log / status routing. Every wire shape is JSON-encoded; field names are lowercase snake_case to match `docs/API.md`.

---

## 1. `claimRequest` — request body (HTTP wire)

**Visibility**: package-private (lowercase). Decoded from `r.Body` by `json.NewDecoder(...).DisallowUnknownFields()`.

| Field | Type | Required | Validation |
|-------|------|----------|------------|
| `scope` | `[]string` | yes | non-empty; each element matches `^[A-Z][A-Z0-9_]{0,63}$`; alphabetised before canonicalisation |
| `reason` | `string` | yes | length ≤ 256; UTF-8; **never echoed in any response or operational log** |
| `ttl` | `string` (Go duration) | yes | parsed via `time.ParseDuration`; > 0 |
| `session_type` | `string` | yes | one of `"interactive"`, `"supervisor"` |
| `ephemeral_pubkey` | `string` | yes | hex-encoded 33-byte compressed secp256k1 pubkey (66 chars) |
| `nonce` | `string` | yes | base64url, length ∈ [8, 128] (matches `sign.NonceCache` contract) |
| `timestamp` | `string` (RFC3339Nano) | yes | parsed via `time.Parse(time.RFC3339Nano, …)` |
| `signature` | `string` | yes | base64-encoded ECDSA over canonical-JSON of every field above plus `request_id`, `machine_name` |
| `request_id` | `string` | yes | length ∈ [16, 64], `[A-Za-z0-9_-]+`; if absent / malformed → `400 bad_request` with **server-generated** id in response (FR-009) |
| `machine_name` | `string` | yes | length ≤ 64, `[A-Za-z0-9._-]+` |
| `client_key_fingerprint` | `string` | yes | 16-char lowercase hex (matches `keys.PublicKeyFingerprint`) |

**Validation rules**:
- Any required field absent / empty / malformed → `400 bad_request`.
- `ttl <= 0` → `400 bad_request` (FR-017).
- `session_type` not in the enum → `400 bad_request`.
- Body > `MaxRequestBodyBytes` (chassis cap, 64 KiB) → `413` from middleware (handler not invoked).
- Unknown fields rejected (`DisallowUnknownFields`).

**State transitions**: none — request is read once and not mutated.

---

## 2. `claimResponse` — successful response body (HTTP wire, status 200)

**Visibility**: package-private. JSON-marshalled by the handler via `json.NewEncoder(w).Encode(...)`.

| Field | Type | Notes |
|-------|------|-------|
| `jwt` | `string` | encoded ES256K JWT from `token.Token.Encoded` |
| `expires_at` | `string` (RFC3339) | from `token.Token.ExpiresAt`, formatted with `time.RFC3339Nano` |
| `jti` | `string` | from `token.Token.JTI` (UUID) |

**Forbidden fields** (FR-020): no echo of `scope`, `reason`, `ttl`, `nonce`, `signature`, `ephemeral_pubkey`, `machine_name`, `client_key_fingerprint`, `request_id` — only the three above are present.

**Status code**: always `200`.

---

## 3. `errorResponse` — failure response body (HTTP wire, status 400/403/408/429/503)

**Visibility**: package-private.

| Field | Type | Notes |
|-------|------|-------|
| `error` | `string` | static error code from the fixed enum (see Outcome below) |
| `request_id` | `string` | server-generated chassis ID (`RequestID(ctx)`); when the client's `request_id` was well-formed, it is **NOT** used here — this is intentional, the chassis ID is canonical for correlation |

**Forbidden fields** (FR-018, FR-019): no other fields. JSON encoder emits exactly these two keys.

---

## 4. `Outcome` — internal enum that drives audit / status routing

**Visibility**: package-private; not exposed on any wire surface. Encoded in audit `Detail["outcome"]` as the `outcomeLabel` string.

| Constant | `outcomeLabel` | HTTP status | Error code in body | Source |
|----------|---------------|-------------|---------------------|--------|
| `outcomeApproved` | `"approved"` | `200` | (none — success) | approver returned `(Decision{Approved:true}, nil)` |
| `outcomeBadRequest` | `"bad-request"` | `400` | `bad_request` | shape validation failed (FR-009, FR-017) |
| `outcomeBadSignature` | `"bad-signature"` | `403` | `bad_signature` | `sign.ErrSignatureInvalid` or `ErrClientUnknown` |
| `outcomeNonceReplay` | `"nonce-replay"` | `403` | `nonce_replay` | `sign.ErrNonceReplay` or `firstSeen=false` |
| `outcomeStaleTimestamp` | `"stale-timestamp"` | `403` | `stale_timestamp` | `sign.IsFreshTimestamp` returned false |
| `outcomeIPNotAllowed` | `"ip-not-allowed"` | `403` | `ip_not_allowed` | request peer not in `cfg.Network.AllowedCIDRs` |
| `outcomeDenied` | `"denied"` | `403` | `denied` | approver returned `ErrApproverDenied` |
| `outcomeApprovalTimeout` | `"approval-timeout"` | `408` | `approval_timeout` | approver returned `ErrApproverTimeout` (wraps `context.DeadlineExceeded`) |
| `outcomeRateLimited` | `"rate-limited"` | `429` | `rate_limited` | approver returned `ErrApproverRateLimited` |
| `outcomeDiscordUnavailable` | `"discord-unavailable"` | `503` | `discord_unavailable` | approver returned `ErrApproverUnavailable` |
| `outcomeUnknown` | `"unknown-outcome"` | `503` | `unknown_outcome` | any non-sentinel error from approver, OR `(Decision{Approved:false}, nil)` |

**Cardinality**: every claim produces exactly one outcome (FR-021). Each outcome maps to exactly one `(status, error_code)` pair (FR-018). The mapping is fail-closed: any unrecognised approver behaviour collapses into `outcomeUnknown` → 503, never 200 (Constitution II, SC-004).

---

## 5. `AuditEvent` — chassis-locked audit shape (already defined in `internal/server/approver.go`)

The handler emits exactly one `AuditEvent` per claim with:
- `Type = AuditClaimOutcome` (new constant, added in `claim_handler.go`)
- `At = s.clock()`
- `RequestID = RequestID(r.Context())`
- `ClientIP` = parsed peer address (already extracted by middleware; re-parsed locally if needed)
- `Detail` = map of the following keys (lowercase, sorted in test assertions for stability):

| Key | Value | Source |
|-----|-------|--------|
| `outcome` | one of the eleven `outcomeLabel` strings | the resolved Outcome constant |
| `session_type` | `"interactive"` \| `"supervisor"` \| `"unknown"` | `claimRequest.SessionType.String()` (from chassis `SessionType` type) |
| `scope` | `strings.Join(sortedScope, ",")` | `claimRequest.Scope` after lowercasing+sorting; **on `bad_request` outcomes where the body did not parse, this key is omitted** (cannot be derived) |
| `granted_ttl` | `cappedTTL.String()` | only present on `approved` outcome |
| `jti` | issued JWT's JTI | only present on `approved` outcome |

**Forbidden keys** (FR-023): `signature`, `nonce`, `ephemeral_pubkey`, `reason`, `jwt`, `client_key_fingerprint`. The audit map is constructed by an explicit allow-list builder (`buildAuditDetail(...)`), not by reflective marshalling, so adding a future field requires touching the builder.

---

## Relationships

```
                 +----------------+
inbound HTTP --> | claimRequest   | -- (validation) --> outcomeBadRequest --+
                 +----------------+                                        |
                          |                                                |
                          v                                                v
                 +------------------+    +-------------+      +---------------+
                 | sign.Verify      | -> | NonceCache  | ---> | IsFreshTS     | --> ip allow --> approver
                 +------------------+    +-------------+      +---------------+         |
                          |  fail              |  fail            |  fail               |  outcomes
                          v                    v                  v                     v
                  outcomeBadSignature   outcomeNonceReplay   outcomeStale...       outcomeApproved..outcomeUnknown
                          |                    |                  |                     |
                          +--------------------+------------------+---------------------+
                                                       |
                                                       v
                                          +--------------------------+
                                          | exactly one AuditEvent   |
                                          | exactly one HTTP response|
                                          +--------------------------+
```

The pipeline is strictly linear; failures short-circuit (FR-002).

---

## Out-of-scope (handled by upstream chunks, not redefined here)

- ECIES envelope shape — SDD-09 (`/secrets/<name>` consumer of the issued JWT).
- JWT claims (`scope`, `client_ip`, etc.) — SDD-07 owns the `token.Claims` shape.
- Canonical-JSON byte format — SDD-08.
- Discord prompt rendering, button payload, audit-channel mirror — SDD-11.
- Audit chain hash + ECDSA signature — SDD-13 (the audit writer wraps the chassis `AuditEvent` and adds the chain semantics).
