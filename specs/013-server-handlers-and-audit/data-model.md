# Phase 1 Data Model — SDD-13 `/s`, `/revoke`, `/hz` + `internal/audit`

This chunk owns nine logical entities split across two packages. Three are HTTP-wire shapes (request body, response body, error body); five are package-private internal types (the outcome enums for each handler, the audit Event, the writer's internal `pending` rendezvous); one is the locked-API exported type (`audit.Event`). All wire shapes are JSON-encoded with lowercase snake_case keys.

---

## 1. `audit.Event` — the on-disk and in-memory chain record (locked exported API)

**Visibility**: exported from `internal/audit`; serialised verbatim to `audit.jsonl`.

```go
type Event struct {
    Seq       uint64         `json:"seq"`
    Time      time.Time      `json:"time"`
    Action    string         `json:"action"`
    Data      map[string]any `json:"data,omitempty"`
    PrevHash  string         `json:"prev_hash"`
    Hash      string         `json:"hash"`
    Signature string         `json:"signature"`
}
```

| Field | Wire format | Computed by | Notes |
|-------|-------------|-------------|-------|
| `Seq` | unsigned integer | writer goroutine | monotonic from `1`; never reused; never has gaps in a single chain run |
| `Time` | RFC3339Nano UTC string | writer goroutine | wall-clock at acceptance; `time.UTC()` enforced before canonicalisation |
| `Action` | string | producer | one of the 19 outcome labels enumerated in §6 |
| `Data` | JSON object | producer | allow-list shape per outcome (§6); MUST NOT contain secret values, signatures, tokens, or bot tokens |
| `PrevHash` | lowercase hex (64 chars) | writer goroutine | hex of the prior event's `Hash`; for `Seq==1` the genesis constant `sha256("hush.audit.chain.v1.genesis")` |
| `Hash` | lowercase hex (64 chars) | writer goroutine | `hex(sha256(prevHash_bytes \|\| sign.CanonicalJSON({Seq, Time, Action, Data, PrevHash})))` |
| `Signature` | base64-standard (no padding) | writer goroutine | `base64(ecdsa.Sign(audit-signing-key, hash_bytes))` over the bytes of `Hash` decoded from hex |

**Validation rules** (verified by `audit.Verify`):
- `Seq` MUST start at `1` and increment by exactly `1` per record (FR-022).
- `PrevHash` of record `n` MUST equal `Hash` of record `n−1` for all `n > 1` (FR-023).
- `Hash` of every record MUST equal the canonical recomputation (FR-023, FR-026).
- `Signature` of every record MUST verify against the audit-signing public key over `hex.Decode(Hash)` (FR-024).
- The first inconsistency surfaces `ErrAuditChainBroken` and identifies the offending Seq (FR-025).

**State transitions**: append-only; no record is ever rewritten.

---

## 2. `audit.Writer` — locked producer-facing interface

**Visibility**: exported from `internal/audit`.

```go
type Writer interface {
    Append(ctx context.Context, action string, data map[string]any) error
    Run(ctx context.Context) error
}
```

| Method | Contract |
|--------|----------|
| `Append(ctx, action, data)` | Synchronously rendezvouses with the writer goroutine; returns nil only AFTER the event has been assigned a Seq, hashed, signed, and persisted to the on-disk chain. Returns `ErrShutdown` if `Run`'s ctx is cancelled. Blocks under producer contention (FR-031). |
| `Run(ctx)` | Single-call lifecycle; returns when `ctx.Done()` AND the buffered `pending` queue has drained AND the on-disk file is `Sync()`-ed and `Close()`-d. The mirror goroutine (if configured) drains its own buffer with a bounded `mirrorShutdownTimeout` and exits. |

**Implementation**: `func NewWriter(ctx context.Context, path string, signKey *ecdsa.PrivateKey, mirror *DiscordMirror, logger *slog.Logger) (Writer, error)` constructs the writer (validates inputs, opens the file with `O_APPEND|O_CREATE`, scans the file's last line to recover `Seq` and `prevHash`). The `ctx` parameter to `NewWriter` is used only for input validation (`ctx.Err()` short-circuit); the actual long-lived ctx is the one passed to `Run`.

**Sentinels**: `ErrAuditChainBroken`, `ErrShutdown`, `ErrChainTailUnreadable`, `ErrInvalidPath`, `ErrInvalidKey`.

---

## 3. `audit.DiscordMirror` — best-effort chat-platform publisher

**Visibility**: exported from `internal/audit`.

```go
type DiscordMirror struct { /* unexported fields */ }

type MirrorSession interface {
    ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, opts ...discordgo.RequestOption) (*discordgo.Message, error)
}

func NewDiscordMirror(channelID string, session MirrorSession) *DiscordMirror
```

`DiscordMirror` carries the `channelID`, the `session` interface, and an internal buffered channel of size `64`. It is passed to `NewWriter`; when `nil` (or when `channelID == ""`), the writer skips mirror dispatch entirely (FR-036).

**MirrorSession** is the narrow seam over `*discordgo.Session` so the audit package does NOT import `internal/discord` (avoids a circular import; `internal/discord` already imports the discordgo type for its Approver). `*discordgo.Session` satisfies the interface structurally.

**Best-effort discipline** (FR-035, R-006): the mirror goroutine receives events from the buffered channel, calls `session.ChannelMessageSendComplex(...)`, and on error logs WARN with `seq` + `action` + error class only — never the bot token, never the event's signature. No retries.

---

## 4. `secretRequest` — `/s` URL-path + header inputs (no body)

**Visibility**: package-private inside `internal/server/secret_handler.go`. Not a struct on the wire — the handler reads three pieces of input directly:

| Source | Field | Validation |
|--------|-------|------------|
| URL path | `name` (after `/s/`) | non-empty; matches `^[A-Z][A-Z0-9_]{0,63}$`; rejected with `400 bad_request` on mismatch |
| `Authorization` header | `Bearer <jwt>` | scheme MUST be `"Bearer "` (case-insensitive); the JWT body is opaque to the handler — `token.Validate` parses it |
| Socket peer | `r.RemoteAddr` | parsed via `parseRemoteAddr` (chassis helper); used as the `requestIP` argument to `token.Validate` |

The handler does NOT read or accept a request body for `/s` (it is a `GET`).

---

## 5. `revokeRequest` — `/revoke` request body (HTTP wire)

**Visibility**: package-private inside `internal/server/revoke_handler.go`. Decoded with `json.NewDecoder(http.MaxBytesReader(...)).DisallowUnknownFields()`.

| Field | Type | Required | Validation |
|-------|------|----------|------------|
| `jti` | `string` | yes | UUIDv4 form (`8-4-4-4-12` lowercase hex with dashes); length 36; `[0-9a-f-]+` |
| `nonce` | `string` | yes | base64url, length ∈ [8, 128] (matches `sign.NonceCache` contract) |
| `timestamp` | `string` (RFC3339Nano) | yes | parsed via `time.Parse(time.RFC3339Nano, ...)` |
| `request_id` | `string` | optional | length ∈ [16, 64], `[A-Za-z0-9_-]+` if present |
| `machine_name` | `string` | optional | length ≤ 64, `[A-Za-z0-9._-]+` if present |
| `client_key_fingerprint` | `string` | yes | 16-char lowercase hex |
| `signature` | `string` | yes | base64-standard ECDSA over `sign.CanonicalJSON` of every other field (alphabetical ordering) |

**Validation rules**:
- Any required field absent / empty / malformed → `400 bad_request`.
- Body > `MaxRequestBodyBytes` → `413` from chassis middleware.
- Unknown fields rejected.
- Verify failure / unknown fingerprint → `403 bad_signature` (same shape; FR-015 anti-enumeration).

---

## 6. `Outcome` — the closed enumeration of audit-action labels

**Visibility**: package-private constants inside the respective handler files (`secret_handler.go`, `revoke_handler.go`) and `audit/chain.go` (for chain lifecycle events). Encoded into `Event.Action` as the listed string.

| Constant | `Action` string | HTTP status / source | Body `error` code | Originating handler |
|----------|------------------|----------------------|---------------------|----------------------|
| `actionSecretRetrieved` | `secret_retrieved` | 200 | (none) | `/s` success |
| `actionSecretBadToken` | `secret_bad_token` | 401 | `bad_token` | `/s` (covers expired, sig-invalid, revoked, exhausted, malformed, unknown-jti, ip-mismatch, alg, missing-or-malformed-header) |
| `actionSecretTokenExpired` | `secret_token_expired` | 401 | `token_expired` | `/s` (the only 401 cause that gets a distinct body code, per R-007) |
| `actionSecretOutOfScope` | `secret_out_of_scope` | 403 | `out_of_scope` | `/s` |
| `actionSecretMissing` | `secret_missing` | 404 | `not_found` | `/s` (in-scope-but-not-in-vault) |
| `actionSecretInternalError` | `secret_internal_error` | 500 | `internal_error` | `/s` (vault read error, ECIES encrypt error, response write error) |
| `actionRevokeSucceeded` | `revoke_succeeded` | 200 | (none) | `/revoke` first-time success |
| `actionRevokeIdempotent` | `revoke_idempotent_already_revoked` | 200 | (none) | `/revoke` re-revocation; HTTP body identical to first-time success |
| `actionRevokeBadRequest` | `revoke_bad_request` | 400 | `bad_request` | `/revoke` malformed body |
| `actionRevokeBadSignature` | `revoke_bad_signature` | 403 | `bad_signature` | `/revoke` (covers verify failure AND unknown JTI per R-008 / FR-015) |
| `actionRevokeNonceReplay` | `revoke_nonce_replay` | 403 | `nonce_replay` | `/revoke` |
| `actionRevokeStaleTimestamp` | `revoke_stale_timestamp` | 403 | `stale_timestamp` | `/revoke` |
| `actionServerStart` | `server_start` | n/a (chassis) | n/a | chassis Run lifecycle |
| `actionServerStop` | `server_stop` | n/a (chassis) | n/a | chassis shutdown |
| `actionVaultReloaded` | `vault_reloaded` | n/a (chassis) | n/a | SIGHUP reload (SDD-10) |
| `actionFilePermCheckFailed` | `file_perm_check_failed` | n/a (chassis) | n/a | startup permission gate |
| `actionDiscordDisconnected` | `discord_disconnected` | n/a (SDD-11 / chassis) | n/a | connectivity transition |
| `actionDiscordReconnected` | `discord_reconnected` | n/a (SDD-11 / chassis) | n/a | connectivity transition |
| `actionAuditMirrorFailed` | `audit_mirror_failed` | n/a (audit) | n/a | mirror publish error (the WARN log; emitted as an audit event so the chain shows the operator that mirroring degraded) |

**Chunk-locked extension policy**: future SDDs MAY add new action strings (append-only). Renaming or repurposing an existing action is a constitutional amendment.

`/hz` is intentionally absent from this table — health probes do NOT emit audit events (spec clarification §4 / FR-021a).

---

## 7. `secretResponse` — `/s` success body

The success body is NOT JSON. It is the raw ECIES envelope bytes returned with `Content-Type: application/octet-stream`. No headers carry the secret name beyond what was already in the request URL; the response body is the ECIES envelope and nothing else (FR-005).

| Wire element | Value |
|--------------|-------|
| Status | `200 OK` |
| `Content-Type` | `application/octet-stream` |
| `Content-Length` | byte length of the envelope |
| `Cache-Control` | `no-store` |
| `X-Content-Type-Options` | `nosniff` |
| Body | `ecies.Encrypt(ctx, ephemeralPubKey, secretValue)` byte output |

**Forbidden**: any JSON wrapping, any echo of the secret name in the body, any echo of the JWT, any non-octet content type.

---

## 8. `errorResponse` — failure body shared by `/s` and `/revoke`

**Visibility**: package-private; the same shape both handlers emit on failure.

| Field | Type | Notes |
|-------|------|-------|
| `error` | `string` | one of the static codes from §6 column 4 |
| `request_id` | `string` | chassis-assigned 32-char hex from `RequestID(ctx)` |

JSON body is exactly `{"error": "...", "request_id": "..."}` — no other keys (FR-005, FR-016).

**Status / `error` matrix for `/s`** (subset of §6 specific to the secret handler):

| Status | `error` | When |
|--------|---------|------|
| `400` | `bad_request` | malformed name path, body present (GET should have none), unsupported method |
| `401` | `bad_token` | every token-validation failure family except expiry |
| `401` | `token_expired` | `token.ErrTokenExpired` (distinct hint to legitimate clients to re-`/claim`) |
| `403` | `out_of_scope` | `token.ErrScopeViolation` |
| `404` | `not_found` | in-scope-but-not-in-vault (post-validate) |
| `500` | `internal_error` | vault read error, ECIES encrypt error, response write error |

**Status / `error` matrix for `/revoke`**:

| Status | `error` | When |
|--------|---------|------|
| `400` | `bad_request` | malformed JSON, missing required field, unknown field |
| `403` | `bad_signature` | verify failure OR unknown fingerprint OR unknown JTI (R-008, FR-015) |
| `403` | `nonce_replay` | `sign.NonceCache.Add` returns `firstSeen=false` or `ErrNonceReplay` |
| `403` | `stale_timestamp` | `sign.IsFreshTimestamp` returns false |

`/revoke` success body (200) is a static one-line JSON: `{"revoked": true, "request_id": "..."}` — identical for first-time success AND idempotent re-revocation per FR-014 and spec clarification §5.

---

## 9. `healthResponse` — `/hz` body

**Visibility**: package-private inside `internal/server/health_handler.go`.

| Field | Type | Source |
|-------|------|--------|
| `status` | `string` | constant `"ok"` |
| `uptime` | `string` (Go-duration form) | `time.Since(s.runStartedAt).Round(time.Second).String()` |
| `secrets_count` | `int` | `len(vaultStore.Names())` |
| `active_tokens` | `int` | `tokenStore.ActiveCount()` |
| `discord_connected` | `bool` | `s.discordHealth()` (false if nil) |
| `config_valid` | `bool` | `true` (chassis only Runs after config validates; future SIGHUP failure reports `false`) |
| `vault_loaded` | `bool` | `s.vaultPtr.Load() != nil` |
| `clock_in_sync` | `bool` | latest cached `clockProbe` result |

**Forbidden fields** (FR-019, SC-006): no secret name, no token identifier, no client-key fingerprint, no bot token, no audit signing key, no audit chain hash, no random API path prefix.

**Status code**: always `200` regardless of operational state (FR-021).

---

## 10. Internal `pending` rendezvous (writer-private)

**Visibility**: package-private inside `internal/audit/writer.go`. NOT exported. Documented for completeness because tests reach for it via `export_test.go`.

```go
type pending struct {
    action string
    data   map[string]any
    ack    chan eventAck
}

type eventAck struct {
    seq uint64
    err error
}
```

`Append` constructs a `pending`, sends it on the unbuffered `accept` channel, blocks on `pending.ack`, and returns `eventAck.err`. The writer goroutine receives `pending`, computes the chain step, persists, and sends `eventAck` back. This rendezvous is the FR-033 enforcement mechanism.

---

## 11. Relationships

```
   ┌──────────────────────┐
   │ HTTP POST /revoke   │── decode ──> revokeRequest ──> sign.CanonicalJSON+Verify ──> NonceCache.Add ──> IsFreshTimestamp ──> token.Store.Revoke
   │ HTTP GET /s/<name>  │── extract ─> Bearer JWT ─────> token.Validate ─────────────> vault.Store.Get ─> ECIES.Encrypt ────> octet-stream body
   │ HTTP GET /hz        │── compose ─> healthResponse ─> JSON body (200)
   └──────────────────────┘
                 │ (every outcome)                                                                  ▲
                 ▼                                                                                  │
       chassisAuditAdapter.Write(AuditEvent)                                                        │
                 │                                                                                  │
                 ▼                                                                                  │
       audit.Writer.Append(ctx, action, data)  ── synchronous rendezvous ──>  writer goroutine ──> on-disk Event
                                                                                  │
                                                                                  └─── (best-effort) ──> mirror goroutine ──> DiscordMirror ──> session.ChannelMessageSendComplex
```

`/hz` is the only handler that does NOT call `Append` (FR-021a / spec clarification §4).

---

## 12. Out-of-scope (handled by upstream / downstream chunks)

- ECIES envelope wire format — SDD-09 (`ecies.Encrypt` is a black-box for this chunk).
- JWT claims structure (`scope`, `client_ip`, `session_type`, etc.) — SDD-07 (`token.Claims`).
- Canonical-JSON byte format — SDD-08 (`sign.CanonicalJSON`).
- Vault file format — SDD-03 (`vault.Store.Get` is a black-box).
- BIP32 audit-key derivation — SDD-01.
- Discord prompt rendering / button payloads / approver semantics — SDD-11.
- `cmd/hush` wiring of `audit.NewWriter` + `chassisAuditAdapter` — SDD-14.
- The CLI verb `hush revoke` that POSTs to `/revoke` — SDD-23.
- `hush audit verify` tooling that consumes the chain offline — out of scope for v0.1.0; the in-process `audit.Verify` is the only verifier this chunk ships.
- Audit-log rotation, archival, disk-space management — explicitly out of scope per the spec.
