# Quickstart: `internal/transport/sign`

**Audience**: SDD-12 (server `/claim` + `/revoke`), SDD-16 (`hush request` client), and any future agent reading a signed-request consumer.
**Last updated**: 2026-04-28 (Phase 1 of SDD-08)

This is the operational cheat-sheet for using the request-signing primitives. The contract and rationale live in [contracts/api.md](./contracts/api.md), [data-model.md](./data-model.md), and [research.md](./research.md); this file shows you how to wire the primitives into a sign / verify flow and how to react to each documented failure.

---

## 1. Sign a request (client side)

```go
package main

import (
    "context"
    "crypto/ecdsa"
    "encoding/hex"
    "fmt"
    "time"

    "github.com/mrz1836/hush/internal/keys"
    "github.com/mrz1836/hush/internal/transport/sign"
)

type ClaimRequest struct {
    Scope     []string  `json:"scope"`
    Reason    string    `json:"reason"`
    TTL       string    `json:"ttl"` // e.g. "1h"
    Nonce     string    `json:"nonce"`     // 8-128 byte opaque string (typically hex of 32 random bytes)
    Timestamp time.Time `json:"timestamp"` // RFC 3339 — encoded as a string by CanonicalJSON
}

func signClaim(ctx context.Context, clientKey *ecdsa.PrivateKey, req ClaimRequest) (canonical, sig []byte, err error) {
    canonical, err = sign.CanonicalJSON(req)
    if err != nil {
        return nil, nil, fmt.Errorf("canonical: %w", err)
    }
    sig, err = sign.Sign(ctx, clientKey, canonical)
    if err != nil {
        return nil, nil, fmt.Errorf("sign: %w", err)
    }
    return canonical, sig, nil
}
```

**Important**: the `Nonce` and `Timestamp` MUST be fields of the canonical payload BEFORE you sign — this is FR-019. The signature transitively covers them. A consumer that omits either from the signed payload breaks the replay-defence contract.

The `Nonce` is an opaque 8–128 byte string. Typical encoding: hex of 32 random bytes (64 ASCII chars; well within the bounds). Generate with `crypto/rand`.

The `Timestamp` is a `time.Time`. `CanonicalJSON` does NOT have built-in `time.Time` support — convert to a deterministic string form (RFC 3339) BEFORE handing to the encoder, OR use a struct field with a `time.Time` value and have `json:"timestamp"` produce the RFC 3339 form via the field's natural string emission. The cleanest pattern is to convert in the request struct itself, e.g.:

```go
type SignedRequest struct {
    // ... your fields ...
    Nonce     string `json:"nonce"`
    Timestamp string `json:"timestamp"` // RFC 3339 string — already deterministic
}

req.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
```

Both client and server then operate on identical canonical bytes.

---

## 2. Verify a request (server side)

The composition recipe from [contracts/api.md](./contracts/api.md#composition-recipe) — the canonical layer-4 verification flow:

```go
package server

import (
    "context"
    "errors"
    "time"

    "github.com/mrz1836/hush/internal/transport/sign"
)

func (s *server) verifyRequest(ctx context.Context, req SignedRequest, sig []byte) error {
    // 1. Recompute canonical bytes from the request struct.
    canonical, err := sign.CanonicalJSON(req)
    if err != nil {
        return err // ErrCanonicalUnsupported
    }

    // 2. Signature first — an attacker who forged a sig MUST NOT consume the nonce budget.
    pub, err := s.lookupRegisteredClientKey(req.ClientID)
    if err != nil {
        return err // not a request-signing concern
    }
    if err := sign.Verify(ctx, pub, canonical, sig); err != nil {
        return err // ErrSignatureInvalid
    }

    // 3. Timestamp second — stale-but-validly-signed replays MUST NOT burn the nonce.
    ts, err := time.Parse(time.RFC3339Nano, req.Timestamp)
    if err != nil {
        return sign.ErrTimestampStale // malformed timestamp == stale
    }
    if !sign.IsFreshTimestamp(ts, s.cfg.Crypto.ClockSkew) {
        return sign.ErrTimestampStale
    }

    // 4. Nonce LAST — only after both gates pass do we commit to the cache.
    firstSeen, err := s.nonceCache.Add(ctx, req.Nonce, s.cfg.Crypto.NonceTTL)
    if err != nil {
        return err // ErrNonceEncoding, ErrNonceTTLInvalid, ErrNonceReplay
    }
    if !firstSeen {
        return sign.ErrNonceReplay
    }

    return nil
}
```

The ordering is load-bearing — see [contracts/api.md](./contracts/api.md#composition-recipe) for the full FR-013 + FR-014 anti-burn rationale.

---

## 3. React to each documented failure

Every error from this package is matchable via `errors.Is`. The recommended HTTP-handler path checks for the operator-actionable categories first:

```go
err := s.verifyRequest(ctx, req, sig)
switch {
case err == nil:
    return nil // accepted

case errors.Is(err, sign.ErrSignatureInvalid):
    s.audit("auth_failed", "signature_invalid", req.ClientID)
    return httpError(http.StatusUnauthorized, "signature invalid")

case errors.Is(err, sign.ErrNonceReplay):
    s.audit("auth_failed", "nonce_replay", req.ClientID)
    return httpError(http.StatusUnauthorized, "nonce replay")

case errors.Is(err, sign.ErrNonceEncoding):
    return httpError(http.StatusBadRequest, "nonce encoding invalid")

case errors.Is(err, sign.ErrNonceTTLInvalid):
    // Programmer error — the consumer mis-passed the ttl. Should be impossible from the wire.
    return httpError(http.StatusInternalServerError, "internal: nonce ttl misconfigured")

case errors.Is(err, sign.ErrTimestampStale):
    s.audit("auth_failed", "timestamp_stale", req.ClientID)
    return httpError(http.StatusUnauthorized, "timestamp stale")

case errors.Is(err, sign.ErrCanonicalUnsupported):
    return httpError(http.StatusBadRequest, "request body cannot be canonicalised")

default:
    // Unclassified — context cancellation, lookup failure, etc.
    return httpError(http.StatusInternalServerError, err.Error())
}
```

The full sentinel catalogue is in [contracts/api.md](./contracts/api.md#sentinel-error-catalogue). All sentinels are `var ErrXxx = errors.New(...)` — compare with `errors.Is`, never with string parsing.

---

## 4. Run the nonce cache

The `NonceCache` requires a long-running sweep goroutine. The caller owns the goroutine — invoke `Run` inside a `go` statement:

```go
package server

func (s *server) Start(ctx context.Context) error {
    s.nonceCache = sign.NewNonceCache()

    // Launch the sweep goroutine. Sweep stops when ctx is cancelled.
    go s.nonceCache.Run(ctx)

    // ... other startup wiring ...
    return nil
}
```

**Lifecycle**:
- `NewNonceCache()` returns immediately. **Zero goroutines are spawned at construction** (FR-009).
- `Run(ctx)` blocks until `ctx.Done()` fires. The sweep ticks every 30 s and removes expired entries via `CompareAndDelete`.
- After `ctx` cancels, Run emits one `slog.Info` line ("hush/transport/sign: nonce cache sweep stopped") and returns. The cache enters store-only mode — `Add` still functions, but expired entries accumulate.

**Common patterns**:
- For a server with a shared lifecycle context, pass that context: `go s.nonceCache.Run(s.ctx)`.
- For a server using `errgroup.Group`, register Run as a group function: `g.Go(func() error { s.nonceCache.Run(ctx); return nil })`.
- For a test, use `t.Context()` — the test framework cancels it on cleanup.

**Add can run before Run**:
```go
firstSeen, err := s.nonceCache.Add(ctx, nonce, ttl) // works fine without Run
```
The cache stores entries normally; only the sweep is gated on Run. This is User Story 4 acceptance scenario 3.

---

## 5. Concurrency guarantees

| Function | Concurrent calls | Race-detector tested |
|----------|------------------|----------------------|
| `CanonicalJSON` | safe; pure | yes (implicit — no shared state) |
| `Sign` | safe; reads `crypto/rand.Reader` | yes |
| `Verify` | safe; pure modulo `nowFn` (no time read in Verify) | yes |
| `IsFreshTimestamp` | safe; reads `nowFn` | yes |
| `NonceCache.Add` | safe; exactly-one-winner under same-nonce concurrency | **yes — `TestNonceCache_ConcurrentAdd`** |
| `NonceCache.Run` | one Run per cache instance — concurrent Run on the same instance is undefined | yes (one-Run-per-test) |

The load-bearing concurrency exercise is `TestNonceCache_ConcurrentAdd`: 128 goroutines submit the same nonce; assert exactly one observes `firstSeen=true`. The test runs under `-race` in `magex test:race`.

---

## 6. What you MUST NOT do

- **Do not omit the nonce or the timestamp from the canonical payload.** FR-019 — the signature transitively covers them; omitting them allows replay with fresh nonce+timestamp. The package documents this requirement but does not enforce payload shape.
- **Do not reuse a `NonceCache` across server restarts.** The cache is in-process; restart resets it. Replays that span a restart are caught by `IsFreshTimestamp`.
- **Do not run two `NonceCache.Run` goroutines on the same cache instance.** The sweep work would double; the `CompareAndDelete` race resolution is correct but the work is wasted. Each cache should have one Run owner.
- **Do not log nonces or signatures.** FR-017 — Constitution X mandates type-driven redaction; this package never logs them, and consumers MUST follow the same discipline.
- **Do not parse error strings.** The sentinel-identity contract is the matching surface; string parsing breaks on the next message refinement.
- **Do not pass a non-secp256k1 key to Sign / Verify.** Constitution III pins secp256k1; the package does not check the curve and a different curve produces undefined behaviour (typically a stdlib panic).
- **Do not invoke a user-defined `MarshalJSON` and expect it to influence canonical output.** The encoder ignores `MarshalJSON` (FR-022 anti-feature). To embed pre-canonical bytes, use `RawMessage`.
- **Do not depend on the cache imposing a capacity cap.** FR-020 — there is no eviction beyond TTL. DoS resistance is the consumer's responsibility (Tailscale boundary + server-side rate limiting).
- **Do not depend on metrics emission from this package.** FR-024 — the package emits zero counters. Derive your own counters from sentinel-error identity in your handler.

---

## 7. Testing your consumer

Your consumer's tests should exercise:

1. **Round-trip**: build a request, call `CanonicalJSON` + `Sign` on the client side, call `CanonicalJSON` + `Verify` on the server side, assert `nil`.
2. **Tampered payload**: round-trip but mutate one field of the request between sign and verify; assert `errors.Is(err, sign.ErrSignatureInvalid)`.
3. **Wrong key**: sign with key A, verify with key B; assert `ErrSignatureInvalid`.
4. **Replay**: verify the same `(payload, sig, nonce, timestamp)` tuple twice; assert second call returns `ErrNonceReplay`.
5. **Stale timestamp**: build a request with `Timestamp = time.Now().Add(-2 * skew)`; assert `ErrTimestampStale`.
6. **Future timestamp**: build a request with `Timestamp = time.Now().Add(2 * skew)`; assert `ErrTimestampStale`.
7. **Cancelled context**: cancel the context before verify; assert `ctx.Err()` is returned.

For deterministic timestamp tests, your consumer can either inject a frozen clock at its own layer (recommended) or set up a long skew so wall-clock drift in CI does not flake the test.

---

## 8. Deferring to the constitution

If a question is not answered above, the answers in priority order are:
1. `.specify/memory/constitution.md` Principles III, VIII, IX, X, XI.
2. `docs/SECURITY.md` §3 Layer 4 (request signing + replay protection).
3. `docs/SPEC.md` FR-6, AC-7.
4. `docs/sdd/SDD-08.md` (the chunk contract).
5. The contract document at [contracts/api.md](./contracts/api.md).

All of the above are checked-in source-of-truth — when they conflict, the constitution wins, and any drift between the others is an issue to file before writing code.
