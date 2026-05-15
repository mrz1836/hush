# Quickstart — exercising SDD-13 `/s`, `/revoke`, `/hz`, and `internal/audit` locally

This document is for the implementation-phase agent and any reviewer who wants to manually drive the new handlers and the audit chain. It assumes the rest of the stack (vault, BIP32 keys, transport/sign + ecies, token, discord stub, claim handler from SDD-12) is already wired into the test harness.

---

## Run the unit suite

```bash
# 1. Format + lint + race
magex format:fix
magex lint
magex test:race

# 2. The new handler subset, with coverage
go test -race -cover -run '^(TestSecret_|TestRevoke_|TestHealth_|TestAuditAdapter_)' ./internal/server/

# 3. The new audit package, with coverage
go test -race -cover ./internal/audit/

# 4. Full integration leg
magex test:race -tags=integration
```

Coverage thresholds:
- `internal/audit/`: **100%** (Constitution VIII security-critical — joins vault/keys/token/transport in the 100% tier).
- `internal/server/` new files (`secret_handler.go`, `revoke_handler.go`, `health_handler.go`, `audit_adapter.go`): **≥ 95%**.

The suite intentionally exercises **every** outcome row in the [contracts/api.md](./contracts/api.md) status matrix and **every** `Action` constant in [data-model.md §6](./data-model.md#6-outcome--the-closed-enumeration-of-audit-action-labels).

---

## Drive the `/s` happy path

```go
func TestSecret_HappyPath_ECIESPayload(t *testing.T) {
    h := newSecretHarness(t,
        withVaultSecret("ANTHROPIC_API_KEY", []byte("sk-ant-known-plaintext")),
        withInteractiveToken(t, withScope("ANTHROPIC_API_KEY"), withMaxUses(1), withClientIP("100.64.0.1")),
    )

    rr := h.do(t, http.MethodGet, "/s/ANTHROPIC_API_KEY", nil,
        withBearerToken(h.jwt),
        fromIP("100.64.0.1"),
    )

    require.Equal(t, http.StatusOK, rr.Code)
    require.Equal(t, "application/octet-stream", rr.Header().Get("Content-Type"))
    require.Equal(t, "no-store", rr.Header().Get("Cache-Control"))

    plaintext, err := ecies.Decrypt(t.Context(), h.ephemeralPriv, rr.Body.Bytes())
    require.NoError(t, err)
    require.Equal(t, []byte("sk-ant-known-plaintext"), plaintext)

    require.Equal(t, 0, h.tokenStore.Live(h.jti).MaxUses, "interactive token decremented")

    require.Len(t, h.audit.Events, 1)
    require.Equal(t, "secret_retrieved", h.audit.Events[0].Action)
    require.Equal(t, "ANTHROPIC_API_KEY", h.audit.Events[0].Data["secret_name"])
    require.NotContains(t, fmt.Sprint(h.audit.Events[0].Data), "sk-ant-known-plaintext",
        "audit event MUST NOT carry the secret value")
}
```

---

## Drive `/revoke` and assert idempotence

```go
func TestRevoke_IdempotentReRevocation_200_StaticBody(t *testing.T) {
    h := newRevokeHarness(t,
        withRegisteredClient(t, fingerprint, pub),
        withInteractiveToken(t, withJTI("dead-beef-...")),
    )

    body1 := signedRevokeBody(t, h, "dead-beef-...")
    rr1 := h.do(t, http.MethodPost, "/revoke", body1)
    require.Equal(t, http.StatusOK, rr1.Code)
    require.JSONEq(t, fmt.Sprintf(`{"revoked":true,"request_id":%q}`, h.requestID(rr1)), rr1.Body.String())

    body2 := signedRevokeBody(t, h, "dead-beef-...") // fresh nonce, same JTI
    rr2 := h.do(t, http.MethodPost, "/revoke", body2)
    require.Equal(t, http.StatusOK, rr2.Code)
    require.Equal(t, rr1.Body.String(), rr2.Body.String(),
        "idempotent re-revoke MUST produce identical body")

    require.Len(t, h.audit.Events, 2)
    require.Equal(t, "revoke_succeeded", h.audit.Events[0].Action)
    require.Equal(t, "revoke_idempotent_already_revoked", h.audit.Events[1].Action)
}
```

The audit chain distinguishes the two outcomes; the HTTP body does not.

---

## Drive `/hz` and assert no-secret-leak

```go
func TestHealth_NoAuth_OK(t *testing.T) {
    sentinel := testutil.SentinelSecret(13)
    h := newHealthHarness(t,
        withVaultSecrets(sentinel, []byte("known")),
        withDiscordHealth(true),
        withRegisteredClient(t, sentinel, pub),
        withDiscordBotToken(sentinel),
    )

    rr := h.do(t, http.MethodGet, "/hz", nil) // NO Authorization header

    require.Equal(t, http.StatusOK, rr.Code)
    var body map[string]any
    require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
    require.Equal(t, "ok", body["status"])
    require.Equal(t, float64(1), body["secrets_count"])
    require.True(t, body["discord_connected"].(bool))

    testutil.AssertSentinelAbsent(t, sentinel, rr.Body.String())
    require.Len(t, h.audit.Events, 0, "/hz MUST NOT emit audit events (FR-021a)")
}
```

---

## Verify the chain integrity invariants

```bash
go test -race -run TestAuditChain_HashLinkContiguous ./internal/audit/
go test -race -run TestAuditChain_SignatureValid ./internal/audit/
go test -race -run TestAuditChain_BreakDetectedOnTamper ./internal/audit/
```

`TestAuditChain_BreakDetectedOnTamper`:

```go
func TestAuditChain_BreakDetectedOnTamper(t *testing.T) {
    path := filepath.Join(t.TempDir(), "audit.jsonl")
    w, _ := audit.NewWriter(t.Context(), path, signKey, nil, slog.Default())
    go w.Run(t.Context())

    require.NoError(t, w.Append(t.Context(), "server_start", map[string]any{}))
    require.NoError(t, w.Append(t.Context(), "claim_outcome", map[string]any{"outcome": "approved"}))
    require.NoError(t, w.Append(t.Context(), "secret_retrieved", map[string]any{"secret_name": "X"}))

    // Mutate event 2's data.outcome from "approved" to "denied" on disk.
    raw, _ := os.ReadFile(path)
    mutated := bytes.Replace(raw, []byte(`"approved"`), []byte(`"denied"  `), 1) // pad to keep length
    require.NoError(t, os.WriteFile(path, mutated, 0600))

    err := audit.Verify(path, &signKey.PublicKey)
    var ce *audit.ChainError
    require.ErrorAs(t, err, &ce)
    require.True(t, errors.Is(err, audit.ErrAuditChainBroken))
    require.Equal(t, uint64(2), ce.Seq, "first tampered event surfaces at Seq 2")
}
```

If this test ever fails, **stop**: a future PR has weakened the chain integrity check. Fix that, do not adjust the test.

---

## Verify the backpressure invariant

```bash
go test -race -run TestAuditWriter_BlocksOnBackpressure ./internal/audit/
```

The test pauses the writer goroutine via a `chan struct{}` injected via `export_test.go`, dispatches more producers than the rendezvous channel can serve, observes that producers block, releases the pause, and asserts every event reaches the chain. The `-race` flag is mandatory.

---

## Verify the sentinel-leak property

```bash
go test -race -run TestSecret_ErrorBodyNoSentinel ./internal/server/
go test -race -run TestAudit_RecordNoSecretValue ./internal/audit/
```

Both tests inject `SECRET_SHOULD_NEVER_APPEAR_13` into the operational surface (vault value / revoke nonce / health bot-token field) and assert the sentinel is absent from:

- the `*httptest.ResponseRecorder` body bytes
- the captured slog buffer
- the on-disk audit chain bytes
- every `AuditEvent.Data` map (asserted both at producer-side and after readback from disk)

---

## End-to-end with the audit chain on disk (integration leg)

```bash
magex test:race -tags=integration -run TestSecretRevokeHealth_Integration_FullFlow ./internal/server/
```

This exercises the full chassis with a real `audit.NewWriter` writing to `t.TempDir()`:

1. POST `/claim` with a signed body → 200, capture JWT.
2. GET `/s/<name>` with the captured JWT → 200, decrypt and assert plaintext.
3. GET `/hz` → 200, assert counts.
4. POST `/revoke` for the issued JTI → 200.
5. GET `/s/<name>` again → 401 (revoked).
6. Cancel the chassis ctx; assert the writer drains and the on-disk file passes `audit.Verify(...)` end-to-end.

This is the closest test to production behaviour and is the load-bearing assertion for AC-1, AC-2, AC-4, AC-7.

---

## Manual smoke (NOT for CI — local only)

```bash
magex build
./bin/hush serve --config testdata/server.toml &
PREFIX=$(jq -r .server.path_prefix testdata/server.toml)

# /hz needs no auth
curl -sS http://100.64.0.1:7743/h/$PREFIX/hz | jq .

# /s and /revoke need a JWT — get one via /claim first (see SDD-12 quickstart)
go run ./internal/testutil/cmd/signed_request_helper \
    --scope ANTHROPIC_API_KEY \
    --ttl 2h \
    --session_type interactive \
    --reason "smoke" > /tmp/claim.json

JWT=$(curl -sS -X POST -H 'Content-Type: application/json' \
    --data @/tmp/claim.json \
    http://100.64.0.1:7743/h/$PREFIX/claim | jq -r .jwt)

curl -sS -H "Authorization: Bearer $JWT" \
    http://100.64.0.1:7743/h/$PREFIX/s/ANTHROPIC_API_KEY \
    -o /tmp/envelope.bin

# Decrypt /tmp/envelope.bin with your client ephemeral private key
# (use the SDD-09 ECIES helper or the client CLI from SDD-16)

# Inspect the audit chain
tail -n5 ~/.hush/audit.jsonl | jq .
```

> Note: `signed_request_helper` and `hush client decrypt` do not yet exist; building them is part of SDD-14 / SDD-16. Until then, exercise the new endpoints through the integration test.

---

## Failure-by-failure cheat sheet

| Symptom | Likely cause | Where to look |
|---------|--------------|---------------|
| `/s` test expecting `200` got `403 out_of_scope` | JWT scope claim does not include the requested name | check `withScope(...)` in the harness setup vs. URL path |
| `/s` test expecting `401 bad_token` got `401 token_expired` | Token Validate returned `ErrTokenExpired` instead of the targeted sentinel | check the JWT's `exp` claim in the harness; only `ErrTokenExpired` gets the distinct body code |
| `/revoke` test expecting `403 bad_signature` got `403 nonce_replay` | the harness reuses a nonce across runs | `signedRevokeBody(...)` MUST generate a fresh `crypto/rand` nonce per call |
| `/revoke` test expecting `200 idempotent` got `403 bad_signature` | the second body reused the first body's signature; nonce changed but signature didn't | re-canonicalise + re-sign per attempt |
| `/hz` test expecting `discord_connected: true` got `false` | `Deps.DiscordHealth` not wired in the harness; default is nil → false | `withDiscordHealth(true)` |
| `audit.Verify` reports `ErrAuditChainBroken` at unexpected Seq | non-deterministic ECDSA signature making the chain irreproducible — but `Hash` doesn't include `Signature` so signature non-determinism cannot break the chain. The likely cause is canonical-JSON ordering drift | check that `Time` is in UTC and that `Data` map keys are not embedded structs with non-string keys |
| `TestAuditWriter_ConcurrentAppendMonotonicSeq` flakes under `-race` | a producer goroutine reads `Seq` from `Event` before `Append` returns | `Append` returns AFTER `Seq` is final; do not race-read the event's Seq from a producer goroutine in the test |
| Sentinel-leak test fails with the sentinel in the audit `Data` map | a code path puts the secret value (or a request-supplied signature/nonce) into the audit Detail | grep the audit-builder file (`secret_handler.go` `buildAuditDetail`); fix the allow-list, do not adjust the sentinel test |

---

## Coverage drift

If `go test -cover ./internal/audit/` reports < 100%, the missing line is one of:

- An error-injection branch the test suite does not hit (e.g. `os.OpenFile` failure on a permission-denied path) — add a fault-injection test using `t.TempDir()` + `os.Chmod(0500)` on the parent.
- A `Verify` branch unreachable from the writer-emitted chains (e.g. `ErrChainTailUnreadable` from a hand-crafted corrupt file) — add a hand-crafted-file test.
- A best-effort mirror branch (e.g. `MirrorSession` returning a transport-level error) — extend the mirror test stub to return each error class.

If `internal/server/` falls below 95% on the new files, the missing line is almost always an error-injection branch on `vault.Get`, `ecies.Encrypt`, or response writes — add the matching fault-injection test.
