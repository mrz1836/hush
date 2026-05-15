# Phase 0 Research — SDD-13 `/s`, `/revoke`, `/hz` + audit chain

The spec's `Clarifications` section already resolves five candidates that the specify phase identified (`/hz` field set, `/revoke` URL shape, `/s` status codes, `/hz` audit emission, idempotent re-revoke shape). This file records the small set of orientation decisions the implementation still needs before code is written. No `[NEEDS CLARIFICATION]` markers remain in the spec; none are introduced here.

---

## R-001 — Audit `Event.Hash` covers the canonical JSON of the event minus `Hash` and `Signature`

**Decision**: For each event, the writer computes:

```text
preimage = prevHash || sign.CanonicalJSON({Seq, Time, Action, Data, PrevHash})
Hash      = sha256(preimage)
Signature = ecdsa.Sign(audit-signing-key, Hash)
```

`Hash` and `Signature` are excluded from the canonical input; they are *outputs* of it. `Time` is RFC3339Nano with UTC location (so the canonical bytes are stable across hosts). `Data` is canonicalised at all depths via `sign.CanonicalJSON` (SDD-08), which sorts map keys lexicographically.

**Rationale**: Including `Hash` in the preimage is circular; including `Signature` makes the chain irreproducible (ECDSA signatures are non-deterministic). The chosen shape matches the project's existing canonical-JSON contract from SDD-08, so verifiers reuse the same primitive used for request signing — one canonicalisation routine, one hash function, no parallel conventions.

**Alternatives considered**:
- *Include `Signature` in the preimage of the next event*: rejected — works, but breaks the property that `Hash` alone is enough to verify `Signature` and forces the verifier to reach into the prior event for two fields rather than one.
- *Use `gob` or a custom binary encoding*: rejected — Constitution XI prefers stdlib + the already-locked canonical-JSON layer over a parallel encoding.

---

## R-002 — Genesis `prevHash` is a fixed, domain-separated 32-byte constant

**Decision**: `genesisPrevHash = sha256("hush.audit.chain.v1.genesis")` — a 32-byte constant, domain-separated by a string-prefix to prevent collision with any other hash domain in the project. The constant is exported as an unexported `genesisPrevHash` byte array inside `internal/audit` and reproducible from the documented seed string in the spec / docs/SECURITY.md.

**Rationale**: An all-zero genesis is convention but admits a length-extension corner case in some Merkle constructions; an arbitrary random constant cannot be reproduced by an out-of-process verifier. A domain-separated SHA-256 image is reproducible, project-specific, and conforms to the constitution's "documented genesis predecessor hash" requirement (spec assumption 6).

**Alternatives considered**:
- *32 zero bytes*: rejected — see above.
- *Hash of build version*: rejected — couples the chain root to the binary tag and would force a chain-break on every upgrade.

---

## R-003 — Producer-facing API is `(action, data map[string]any)`; the chassis adapter folds `AuditEvent` fields into `Data`

**Decision**: `audit.Writer.Append(ctx, action string, data map[string]any) error` is the producer API per the SDD-13 chunk contract. The chassis-locked `server.AuditWriter.Write(ctx, AuditEvent)` is preserved by a tiny adapter (`internal/server/audit_adapter.go`):

```go
func (a *chassisAuditAdapter) Write(ctx context.Context, ev AuditEvent) error {
    data := make(map[string]any, len(ev.Detail)+2)
    for k, v := range ev.Detail { data[k] = v }
    if ev.RequestID != "" { data["request_id"] = ev.RequestID }
    if ev.ClientIP.IsValid() { data["client_ip"] = ev.ClientIP.String() }
    return a.w.Append(ctx, string(ev.Type), data)
}
```

`cmd/hush` (SDD-14) wires `&chassisAuditAdapter{w: realAuditWriter}` into `Deps.AuditWriter` so the chassis stays unchanged. Tests construct the adapter directly when they want to exercise both sides; tests of the chassis alone use a fake `AuditWriter` and never hit the real chain.

**Rationale**: The chunk contract locks the producer-facing shape `(action, data)` because that shape is what the on-disk JSON record carries — `Event.Action` and `Event.Data` are public fields. The chassis-side `AuditEvent` predates this chunk and was deliberately narrower (`Type`, `RequestID`, `ClientIP`, `Detail map[string]string`). Adapting between the two via a 12-line type is preferable to mutating the chassis surface (which would force an SDD-10 amendment).

**Alternatives considered**:
- *Replace `server.AuditWriter` with `audit.Writer`*: rejected — breaks the locked SDD-10 surface and forces the claim handler (SDD-12) to be re-touched.
- *Make `Append` accept `AuditEvent` directly*: rejected — couples `internal/audit` to `internal/server` types, breaks reusability, and contradicts the chunk-contract signature.

---

## R-004 — Single goroutine owns the file handle and the chain state; `Append` blocks on a buffered channel

**Decision**: `Writer.Run(ctx)` starts ONE long-lived goroutine that owns `*os.File`, the `Seq` counter, the `prevHash` byte slice, and an optional pointer to the `DiscordMirror`. Producers call `Append(ctx, action, data)`, which:

1. Validates inputs (non-empty action, non-nil ctx).
2. Constructs an internal `pending{action, data, ack chan eventAck}` value.
3. Sends `pending` to the unbuffered channel `accept` (the writer goroutine receives, computes seq+hash+signature SYNCHRONOUSLY, then sends `eventAck{seq, err}` on `ack`).
4. Receives the ack and returns its error.

This makes `Append`-success → "the event has been incorporated into the chain" by construction (FR-033): the seq is determined and the predecessor linkage is fixed before the producer's call returns. Disk persistence is `bufio.Writer.Flush` per event (no batching in v0.1.0 — Constitution VIII trumps throughput). The Discord mirror is dispatched on a SECOND goroutine fed by a SEPARATE buffered channel; mirror failures cannot delay the producer.

**Rationale**: The chunk contract reads "single goroutine, buffered chan", but a buffered channel + asynchronous persistence breaks FR-033 ("Append success guarantees event is on-chain at return"). The chosen synchronous-rendezvous design preserves the invariant while still serialising file writes. Buffer pressure manifests as contention on the unbuffered `accept` channel — concurrent producers queue up, exactly the backpressure semantics the spec requires.

The Discord mirror's independent goroutine + buffered channel keeps mirror failures off the producer's critical path (FR-035 / SC-011).

**Alternatives considered**:
- *Buffered `accept` channel with async writer-goroutine flush*: rejected — fails FR-033's "Append-success means on-chain" property unless the producer waits for an ack anyway, in which case the buffered channel offers no concurrency benefit.
- *One mutex around the writer struct + caller does the file write*: rejected — lock contention scales worse than channel rendezvous under high concurrency, and ownership of the file handle becomes ambiguous.
- *Pre-compute hash on the producer side, only flush from the writer*: rejected — splits the chain state across goroutines; correctness depends on locked Seq access, which a single-goroutine design avoids.

---

## R-005 — On-disk format is line-delimited canonical JSON (one Event per line), append-only, mode 0600

**Decision**: Each event is `sign.CanonicalJSON(Event{Seq, Time, Action, Data, PrevHash, Hash, Signature})` followed by `\n`. The writer opens the file with `os.OpenFile(path, O_WRONLY|O_APPEND|O_CREATE, 0600)` and never seeks. Hash and Signature are hex-encoded (lowercase). Time is RFC3339Nano UTC. Reading for verification iterates with `bufio.Scanner` (`MaxScanTokenSize = 1 MiB` to absorb generous Data payloads).

**Rationale**: JSONL is operator-friendly (`tail -f`, `jq` work out of the box), append-only matches the security model (the chain admits insertions only at the tail, which the signature catches as a chain break), and `0600` is the file-perms invariant from FR-015. The 1 MiB scanner cap is far above any realistic Data size and protects against a malformed-line DoS.

**Alternatives considered**:
- *Length-prefixed binary records*: rejected — opaque to operators, more error-prone, no benefit at v0.1.0 audit volumes.
- *Separate index file*: rejected — adds a coordination point (out-of-band index can drift from chain) for no v0.1.0 benefit.

---

## R-006 — Discord mirror is fed via a SEPARATE 64-deep buffered channel; on full buffer, the writer DROPS the mirror copy and logs a WARN

**Decision**: The on-disk path is FR-031-blocking; the mirror path is FR-035-best-effort. The writer goroutine, after persisting an event to disk, attempts a non-blocking send to the mirror channel:

```go
select {
case m.ch <- ev: // queued for the mirror goroutine
default:
    m.logger.WarnContext(ctx, "audit mirror buffer full, dropping mirror copy",
        "seq", ev.Seq, "action", ev.Action)
}
```

The on-disk chain is unaffected by a dropped mirror copy. The mirror goroutine drains `m.ch`, calls `MirrorSession.ChannelMessageSendComplex(channelID, ...)`, logs a WARN on per-call failure (with the event's seq + action only — never the bot token, never the event's signature), and never retries. On `ctx.Done()` the mirror goroutine drains its channel best-effort within a bounded `mirrorShutdownTimeout` (default 5 s) and exits.

**Rationale**: The chunk says "send asynchronously, log WARN on failure, NEVER block the disk write". A blocking-on-mirror-buffer design would couple disk-throughput to mirror-throughput, which violates the design intent. Dropping the mirror copy on a full buffer is acceptable BECAUSE the on-disk chain is the source of truth (per spec User Story 7 and `docs/SECURITY.md` Layer 6 "the signed file `~/.hush/audit.jsonl` is the authoritative record; Discord audit channel is the convenience layer"). The dropped-mirror WARN is an operational signal that mirror throughput needs attention.

**Alternatives considered**:
- *Block the writer on mirror buffer full*: rejected — violates the spec's best-effort discipline and converts a chat-platform outage into a server-handler outage.
- *Retry mirror failures with backoff*: rejected — the chunk says "never retry indefinitely" and a repeated-retry loop creates duplicate Discord messages on transient transport errors.
- *Hold mirror events in memory across restart*: rejected — would require the mirror to persist its own queue, which inverts the relationship between the source-of-truth chain and the convenience mirror.

---

## R-007 — `/s` handler maps token-validation sentinels to a fixed status table

**Decision**: The handler calls `token.Validate(ctx, encoded, s.jwtVerifyKey, s.tokenStore, peer.String(), name)` and maps the returned sentinel:

| `token` sentinel | HTTP status | `errorResponse.error` | `outcome` audit label |
|------------------|-------------|------------------------|------------------------|
| `nil` (success) | `200` | (none) | `secret_retrieved` |
| `ErrTokenExpired` | `401` | `token_expired` | `secret_token_expired` |
| `ErrSignatureInvalid` | `401` | `bad_token` | `secret_bad_signature` |
| `ErrTokenMalformed` | `401` | `bad_token` | `secret_token_malformed` |
| `ErrAlgorithmUnsupported` | `401` | `bad_token` | `secret_token_alg` |
| `ErrTokenRevoked` | `401` | `bad_token` | `secret_token_revoked` |
| `ErrTokenExhausted` | `401` | `bad_token` | `secret_token_exhausted` |
| `ErrIPMismatch` | `401` | `bad_token` | `secret_ip_mismatch` |
| `ErrUnknownSessionType` | `401` | `bad_token` | `secret_token_alg` |
| `ErrScopeViolation` | `403` | `out_of_scope` | `secret_out_of_scope` |
| (post-validate) `vault.Get` returns "not found" | `404` | `not_found` | `secret_missing` |
| (post-validate) ECIES encrypt error | `500` | `internal_error` | `secret_internal_error` |
| (post-validate) response write error | (no body) | (logged) | `secret_internal_error` |

**Rationale**: The 401-family is a SINGLE static error code (`bad_token`) for every cause that should not be distinguished to a caller (Constitution V's "loud failure" lives in audit, not in HTTP error variation). `ErrTokenExpired` gets its own code (`token_expired`) — the spec's clarification §3 lists `expired` as a documented failure cause and the rotation expectation needs a distinct hint to legitimate clients (their JWT just timed out; they should re-`/claim`). `403 out_of_scope` is distinct from 401 because the spec's clarifications explicitly carved it out. `404 not_found` exists per the same clarification (in-scope-but-not-in-vault).

The `internal_error` 500 path is rare and is included so that a vault/ECIES bug does not mask as a 401 (which would mislead the client into thinking its token was bad). Sentinel-leak still covers the 500 body (FR-005, SC-014).

**Alternatives considered**:
- *Collapse 403 into 401*: rejected — the spec's clarifications pin this distinction.
- *Per-cause error code in the 401 family*: rejected — enumeration vector for anti-debugging.

---

## R-008 — `/revoke` uses the SAME `sign.CanonicalJSON` + `sign.Verify` + nonce + timestamp path as `/claim`

**Decision**: The revoke body is:

```json
{
  "jti":         "<token id, UUIDv4>",
  "nonce":       "<base64url, 8..128>",
  "timestamp":   "2026-04-30T18:23:11.123456789Z",
  "request_id":  "<chassis-style id, optional but signed if present>",
  "machine_name":"<optional metadata, signed if present>",
  "client_key_fingerprint": "<16 hex chars>",
  "signature":   "<base64 ECDSA over canonical JSON of all fields above except signature>"
}
```

The pipeline is, in order: shape → CanonicalJSON+Verify → NonceCache.Add → IsFreshTimestamp → token-store lookup → `Revoke`. The `client_key_fingerprint` is resolved via the SAME `Deps.ClientKeyResolver` the claim handler uses (the resolver loaded at `New`-time from `cfg.Server.ClientRegistry`). The verify key is the public half of the original signer's key — i.e. the same fingerprint registry. Authorisation rule for revoke: if the request's signature verifies, the request is authorised — this is the chunk contract ("verify with same client key registry as `/claim`"). The revoker need not be the same fingerprint that issued the token; the system's authorisation is "any registered client may revoke any JTI" because the registry's mere presence is the authorisation token (the operator manages the registry on the trusted host out-of-band).

A re-revocation of an already-revoked JTI is idempotent: `token.Store.Revoke(jti)` is itself idempotent (the in-memory store's `Revoke` simply marks the JTI in the revoked set; re-marking is a no-op). The handler returns the same `200 {"revoked": true, "request_id": "<id>"}` body the first call returns. The audit chain distinguishes `revoke_succeeded` (first revocation) from `revoke_idempotent_already_revoked` (subsequent calls) — but the HTTP body is identical (per spec clarification §5).

**Rationale**: The chunk locks "the same canonical-JSON + Verify path as /claim" and a body-only JTI shape (per spec clarification §2). Reusing `sign.Verify` and `NonceCache` means no parallel canonicalisation logic and no duplicated test surface — the SDD-08 fuzz target already covers the canonical-JSON behaviour for this body. Treating any registered client as authorised to revoke matches the v0.1.0 trust model (the registry is the authorisation list; a future "originator-only revoke" rule would be a constitutional amendment).

**Alternatives considered**:
- *Require `client_key_fingerprint` to match the JTI's original signer*: rejected — adds a per-request store lookup that the chunk did not lock; the trusted-host registry is already the authorisation gate. Documented as a residual risk in the spec's Out of scope section.
- *URL-path JTI*: rejected by spec clarification §2.

---

## R-009 — `/hz` reads `discord_connected` via the optional `Deps.DiscordHealth func() bool`

**Decision**: The chassis stores an optional `Deps.DiscordHealth func() bool`. The `/hz` handler reads it once per request:

```go
discordConnected := false
if s.discordHealth != nil {
    discordConnected = s.discordHealth()
}
```

The default (nil → false) is fail-closed: an operator querying `/hz` during early startup before `cmd/hush` has wired the Discord approver sees `discord_connected: false`, which matches reality (the approver is not yet ready to mediate `/claim`). `cmd/hush` (SDD-14) wires the closure via:

```go
deps.DiscordHealth = botApprover.Connected
```

`(a *BotApprover) Connected() bool { return a.available.Load() }` is a one-line additive method on the existing `BotApprover` (the `available atomic.Bool` field already exists at `internal/discord/bot.go:66`).

**Rationale**: This injection seam preserves the chassis's "no direct discord import" rule (`docs/PACKAGE-MAP.md`). The fail-closed default matches Constitution II's spirit (fail-closed never falsely advertises Discord as connected) and is cheap to implement.

**Alternatives considered**:
- *Read from a chan or atomic on the chassis*: rejected — the chassis would need to subscribe to `/internal/discord` connectivity events, which would require a chassis-side handler, increasing the surface area.
- *Have the Approver interface gain `IsAvailable()`*: rejected — that would force every Approver impl (including test fakes) to implement a connectivity probe, when the only consumer is `/hz`.

---

## R-010 — `/hz` field set is the SDD-13 chunk shape verbatim, plus a `vault_loaded` boolean

**Decision**: The body is:

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

`status` is the constant `"ok"` (the response is `200` regardless of operational state per FR-021). `uptime` is the duration since `Server.Run` accepted its context, formatted via `time.Duration.String`. `secrets_count` is `len(vaultStore.Names())` (a new accessor on the vault store — see R-011). `active_tokens` is the count of non-revoked, non-expired entries in `token.Store` — exposed via a new `Store.ActiveCount()` accessor (additive single-method extension). `config_valid` is `true` (if the server started, the config validated; the field reports the *current* config state and would only flip to `false` if a future SIGHUP re-validation surfaces an error — for v0.1.0 it is always `true` after start). `vault_loaded` is `s.vaultPtr.Load() != nil` (always `true` post-Run, but exposed as a field so an early-startup probe sees `false`). `clock_in_sync` is the cached result of the most recent `clockProbe` call (already cached for the `clock_sync` startup gate; exposed here as a field).

**Rationale**: Spec clarification §1 picks the "match SDD-13 chunk contract verbatim" answer. The two additive fields (`vault_loaded`, `clock_in_sync`) are required by the spec's edge case "queried before the vault is loaded" — they let the body answer a yes/no question without a non-200 status.

**Alternatives considered**:
- *Booleans only*: rejected by spec clarification §1.
- *Include vault file path / discord channel ID*: rejected — Constitution X redaction; the body must not echo identifiers (FR-019, SC-006).

---

## R-011 — `vault.Store` gains a `Names() []string` accessor; `token.Store` gains an `ActiveCount() int` accessor

**Decision**: Two additive methods on existing interfaces. `Names() []string` returns a sorted copy of the secret-name set (no values); `ActiveCount() int` returns the live, non-revoked, non-expired count. Both are pure-read, concurrent-safe (the existing implementations already hold the relevant `sync.RWMutex`).

**Rationale**: Without these accessors, `/hz` cannot derive `secrets_count` and `active_tokens` without a privacy-violating workaround (e.g., iterating internal state via reflection). The methods are tiny, well-typed, and serve only the health endpoint — they do NOT broaden the secret-retrieval surface (no value disclosure, no JTI disclosure).

**Alternatives considered**:
- *Inject the counts via Deps callbacks*: rejected — duplicates state; the stores are the source of truth.
- *Skip the counts*: rejected — the spec User Story 4 acceptance scenario 1 lists them as required.

---

## R-012 — Sentinel-leak strategy reuses `testutil.SentinelSecret(13)` and `AssertSentinelAbsent`

**Decision**: `TestSecret_ErrorBodyNoSentinel` and `TestAudit_RecordNoSecretValue` both use `testutil.SentinelSecret(13)` (= `SECRET_SHOULD_NEVER_APPEAR_13`) injected into:

- a vault entry's value (assert no body, log line, audit event contains it — covers FR-005, FR-028, SC-002, SC-014, SC-015)
- a `/revoke` request's `nonce` field (assert no body, log line, audit event contains it — covers FR-029)
- a `/hz` adjacent harness setup with the sentinel as the discord bot token field via `cfg.DiscordToken = sentinel` (assert no body contains it — covers FR-019, SC-006)

`testutil.AssertSentinelAbsent` is the locked helper (already in `internal/testutil/sentinel.go`).

**Rationale**: A fresh sentinel per chunk lets a future grep of test output identify which chunk's invariant a regression broke. The Constitution-VIII "fuzz of error paths for partial secret exposure" also benefits from a stable-name sentinel.

**Alternatives considered**:
- *Trust redaction*: rejected — `internal/logging.RedactString` targets credential-shaped patterns (BIP32 seeds, JWT shapes, etc.), NOT arbitrary user input or vault values; only an explicit allow-list builder + sentinel-leak test proves the property.

---

## R-013 — Coverage strategy: `internal/audit` 100%, `internal/server` ≥ 95% on the new files

**Decision**: `internal/audit` is in the Constitution-VIII 100% tier (security-critical: the chain joins vault/keys/token/transport because the chain's signature is the same ECDSA primitive over canonical JSON). Per-file coverage is asserted by `internal/audit/coverage_test.go` (mirrors `internal/vault/coverage_test.go`'s pattern). `internal/server`'s coverage gate is whole-package; the three new handler files plus the audit adapter add ~600 LOC, and the tests target ≥ 95% line coverage on those files specifically (drift is monitored by `codecov.yml` per Constitution VIII).

**Rationale**: The 100% bar on the audit package is non-negotiable — the chain's cryptographic guarantees are the entire reason Layer 6 exists. Whole-package coverage on `internal/server` is sufficient because the chassis files are already tested under SDD-10/SDD-12 and the new handler files will dominate new-code coverage.

**Alternatives considered**:
- *95% on audit*: rejected — the chunk contract pins 100%.
- *No per-file gate on audit*: rejected — without it a reorganisation could silently drop one file under 100% and pass the package-aggregate gate.

---

## R-014 — Integration test runs the full chassis with three handlers + a real `audit.NewWriter` writing to `t.TempDir()`

**Decision**: A single `//go:build integration` test (`integration_test.go`'s extended `TestEndToEndIntegration` or a new `TestSecretRevokeHealth_Integration_FullFlow`) drives the chassis end-to-end:

1. Construct deps with `Approver = approveAlways(t)` (interactive token grant), `AuditWriter = chassisAuditAdapter` over `audit.NewWriter` writing to `filepath.Join(t.TempDir(), "audit.jsonl")`, `DiscordHealth = func() bool { return true }`, and a real `vault.Store` containing one known secret.
2. POST `/claim` with a signed body → expect 200, capture JWT.
3. GET `/s/<name>` with the captured JWT → expect 200, decrypt with the test ephemeral private key, assert plaintext matches the known secret value.
4. GET `/hz` → expect 200, assert the body's `secrets_count == 1`, `active_tokens == 1`.
5. POST `/revoke` with a signed body for the issued JTI → expect 200, idempotent body.
6. GET `/s/<name>` again → expect 401 (revoked).
7. Cancel the chassis ctx; assert the audit writer drains and the on-disk file passes `audit.Verify(...)` end-to-end.

**Rationale**: The integration test exercises the same wiring `cmd/hush` (SDD-14) will install in production, including the chassis adapter, the audit chain on disk, and the `/hz` field derivations. It is the closest test to production behaviour and is the load-bearing assertion for AC-1, AC-2, AC-4, AC-7 in the AC matrix.

**Alternatives considered**:
- *Separate integration test per handler*: rejected — duplicates harness setup and misses cross-handler invariants (e.g., the audit chain across both `/s` and `/revoke`).
- *Mock the audit chain in integration*: rejected — Constitution VIII "integration tests gated by //go:build integration" are *integration*, not unit-with-mocks; the real chain on disk is the value of this test.

---

All open questions are resolved. No `[NEEDS CLARIFICATION]` markers remain in the spec or the plan. Phase 0 is complete.
