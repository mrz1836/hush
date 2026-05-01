# Quickstart — exercising SDD-12 `/claim` locally

This document is for the implementation-phase agent and any reviewer who wants to manually drive the handler. It assumes the rest of the stack (vault, BIP32 keys, transport/sign, token, discord stub) is already wired into a test harness — i.e., the integration leg of `internal/server`.

---

## Run the unit suite

```bash
# 1. Format + lint + race
magex format:fix
magex lint
magex test:race

# 2. Just the claim-handler subset, with coverage
go test -race -cover -run '^TestClaim_' ./internal/server/

# 3. Full integration leg (uses testutil.DiscordStub)
magex test:race -tags=integration
```

Coverage threshold: ≥ 95% on the new code in `claim_handler.go` (Constitution VIII). The suite intentionally exercises **every** outcome row in the [contracts/api.md](./contracts/api.md) status matrix.

---

## Drive the happy path in a unit test

The pattern most tests follow:

```go
func TestClaim_Approved_IssuesJWT(t *testing.T) {
    h := newTestHarness(t,
        withApprover(approveAlways(t)),                     // returns (Decision{Approved:true, GrantedTTL: req.TTL}, nil)
        withClientKey(t, fingerprint, pub),
    )
    body := signedClaimBody(t, h, claimBodyOpts{
        Scope: []string{"ANTHROPIC_API_KEY"},
        TTL:   2 * time.Hour,
        Sess:  "interactive",
    })

    rr := h.do(t, http.MethodPost, "/claim", body)

    require.Equal(t, http.StatusOK, rr.Code)
    var resp struct {
        JWT       string `json:"jwt"`
        ExpiresAt string `json:"expires_at"`
        JTI       string `json:"jti"`
    }
    require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
    require.NotEmpty(t, resp.JWT)
    require.NotEmpty(t, resp.JTI)

    require.Len(t, h.audit.Events, 1)
    require.Equal(t, server.AuditClaimOutcome, h.audit.Events[0].Type)
    require.Equal(t, "approved", h.audit.Events[0].Detail["outcome"])
}
```

The test harness (`newTestHarness` in `claim_handler_test.go`) constructs:
- a `*config.Server` with the new `Crypto.ClaimApprovalTimeout = 60s` populated
- a fake approver (default: deny-everything) overridable via `withApprover`
- a `keys`-derived client signing keypair memoised across the suite via `testutil.NewTestKeys`
- a recording `AuditWriter` whose events the test reads
- a `*slog.Logger` bound to a `*bytes.Buffer` for sentinel-leak inspection

---

## Verify the no-auto-approve invariant

```bash
go test -race -run TestClaim_NoAutoApproveKnobExists ./internal/server/
go test -race -run TestClaim_DiscordUnavailable_503 ./internal/server/
```

`TestClaim_NoAutoApproveKnobExists` exhaustively iterates over `Deps` field combinations and grep-asserts that no source file in `internal/server/` mentions "auto-approve" in any case. `TestClaim_DiscordUnavailable_503` proves that with `approverImpl` returning `ErrApproverUnavailable` the response is `503 discord_unavailable` and no token is issued.

---

## Verify the sentinel-leak property

```bash
go test -race -run TestClaim_ErrorBodyNoSentinel ./internal/server/
```

The test injects `reason = "SECRET_SHOULD_NEVER_APPEAR_12"` into the request, forces a signature failure, and asserts the sentinel is absent from:
- the `*httptest.ResponseRecorder` body bytes
- the captured slog buffer
- the recorded `AuditEvent.Detail` keys (`reason` is not a key)

If this test ever fails, **stop**: a future PR has begun echoing client input into the response or the log. Fix that, do not adjust the test.

---

## End-to-end with the Discord stub (integration leg)

```bash
magex test:race -tags=integration -run TestClaim_Integration_FullFlow_DiscordStub ./internal/server/
```

This exercises the full SDD-04 `testutil.DiscordStub` through a small adapter (`stubAsApprover`) that translates the stub's `(Decision, error)` shape into chassis sentinels. It is the closest unit-of-test to the production wiring that `cmd/hush` will install in SDD-14.

---

## Manual smoke (NOT for CI — local only)

If you want to hit the handler with `curl`, build a small `signed_request_helper` binary that:
1. Derives the client key for `machine_index=0` from the test passphrase.
2. Computes canonical JSON of the body (alphabetised) and ECDSA-signs it.
3. Emits the body to stdout.

Then:

```bash
go run ./cmd/hush serve --config testdata/server.toml &     # SDD-14 wiring
PREFIX=$(jq -r .server.path_prefix testdata/server.toml)

go run ./internal/testutil/cmd/signed_request_helper \
    --scope ANTHROPIC_API_KEY \
    --ttl 2h \
    --session_type interactive \
    --reason "smoke test" \
    > /tmp/claim.json

curl -sS -X POST -H 'Content-Type: application/json' \
    --data @/tmp/claim.json \
    http://100.64.0.1:7743/h/$PREFIX/claim
```

You should receive a `200` with a JWT (after approving in the linked Discord stub) or one of the documented failure shapes.

> Note: the signed_request_helper does not exist yet — building it is part of SDD-14's scope. Until then, all manual exercise happens through the Go test suite.

---

## Failure-by-failure cheat sheet

| Symptom | Likely cause | Where to look |
|---------|--------------|---------------|
| Test expecting `200` got `403 bad_signature` | canonical-JSON ordering differs between client signer and server verifier | `sign.CanonicalJSON` field set in handler vs. helper |
| Test expecting `403 nonce_replay` got `403 stale_timestamp` | pipeline order regression | `claim_handler.go` ordering — must be sig → nonce → ts |
| Test expecting `408` got `503 discord_unavailable` | approver wrapped wrong sentinel | check that adapter wraps `context.DeadlineExceeded` with `ErrApproverTimeout`, not `ErrApproverUnavailable` |
| Test expecting `200` got `503 unknown_outcome` | approver returned `(Decision{Approved:false}, nil)` | unrecognised approver return — see [data-model.md §4](./data-model.md#4-outcome--internal-enum-that-drives-audit--status-routing) |
| Sentinel-leak test fails on `reason` | a code path puts `reason` into response or log | search `claim_handler.go` for `req.Reason` references; only the audit-OMITTED-key invariant allows it on the audit path; even there it's forbidden |
