# Phase 1 Data Model: `internal/transport/sign`

**Feature**: 008-transport-sign
**Date**: 2026-04-28

This package owns four primitives (`CanonicalJSON`, `Sign`, `Verify`, `IsFreshTimestamp`) and one stateful object (`NonceCache`), plus a `RawMessage` named-byte-slice for the canonical-encoder escape hatch and a six-entry sentinel error catalogue. There is no persistence and no on-the-wire schema beyond the canonical bytes the encoder produces. The "data model" below is the in-process representation.

---

## Public types

### `RawMessage`

```go
type RawMessage []byte
```

A named byte slice whose contents are inserted **verbatim** into the canonical output when this value appears as a leaf or sub-value in the input to `CanonicalJSON`. The caller guarantees the bytes are themselves canonical-JSON-shaped; the encoder performs no validation. A nil or zero-length `RawMessage` emits the JSON `null` literal (mirrors stdlib `json.RawMessage`'s MarshalJSON behaviour).

**Use cases**:
- Pre-canonicalised request templates that fill in only a few fields per call.
- Embedding an already-signed sub-payload into an outer canonical envelope without double-canonicalisation.

**Constraints**:
- The caller's bytes are trusted. If the caller's bytes are not canonical, signature verification fails on the recipient side; the consumer's test catches it.
- `RawMessage` is detected via `reflect.Type` equality with the package-private sentinel `rawMessageType = reflect.TypeOf(RawMessage(nil))`. A user-defined `type MyRaw []byte` is NOT detected — only the exported `RawMessage` type triggers the verbatim-emission branch.

---

### `NonceCache` (interface)

```go
type NonceCache interface {
    Add(ctx context.Context, nonce string, ttl time.Duration) (firstSeen bool, err error)
    Run(ctx context.Context)
}
```

The single point of state mutation in the package. The interface is small (two methods) and load-bearing for replay defence.

#### `Add(ctx, nonce, ttl) (firstSeen bool, err error)`

| Phase | Behaviour |
|-------|-----------|
| Cancellation | If `ctx.Err() != nil`, return `(false, ctx.Err())`. |
| Encoding gate | If `len(nonce) < 8 || len(nonce) > 128`, return `(false, ErrNonceEncoding)`. |
| TTL gate | If `ttl <= 0`, return `(false, ErrNonceTTLInvalid)`. |
| Atomic insert | `sync.Map.LoadOrStore(nonce, now+ttl)` — on store-fresh, return `(true, nil)`. On load-existing, evaluate the entry's expiry: if expired AND `CompareAndSwap` succeeds, return `(true, nil)`; otherwise return `(false, ErrNonceReplay)`. |

**Concurrency**: safe to call concurrently from N goroutines. Under N-goroutine concurrent insertion of the same nonce, exactly one observes `firstSeen=true` and N-1 observe `firstSeen=false` with `ErrNonceReplay`. The race-detector exercise (`TestNonceCache_ConcurrentAdd`) launches N=128 goroutines on a shared nonce and asserts the count.

**Idempotence**: not idempotent across calls. Add is a state-mutation. The first call with a fresh nonce stores the entry; subsequent calls return replay until the TTL elapses and the sweep removes the entry.

**Time source**: uses `nowFn()` (package-private, defaults to `time.Now`; tests swap via `setClockForTest`).

**Cancellation latency**: zero. Add is synchronous; the only cancellation point is the entry-time `ctx.Err()` check.

#### `Run(ctx)`

| Phase | Behaviour |
|-------|-----------|
| Setup | `time.NewTicker(c.sweepInterval)` (default 30 s). |
| Loop | `select { case <-ctx.Done(): ...; case <-t.C: c.sweep() }`. |
| Sweep | `c.entries.Range(...)` — for each entry whose stored expiry is in the past, `CompareAndDelete(key, observedExpiry)`. |
| Termination | When `ctx.Done()` fires: emit `slog.Info("hush/transport/sign: nonce cache sweep stopped", slog.String("reason", ctx.Err().Error()))`; return. |

**Concurrency**: Run is **synchronous** — the caller must invoke it in their own goroutine: `go cache.Run(ctx)`. Run holds a single ticker; the goroutine executing Run IS the sweep goroutine. Cancellation latency is bounded by the ticker interval.

**Pre-Run state**: prior to invoking Run, the cache stores entries normally (Add works) but does not sweep. Expired entries accumulate in the `sync.Map` until either Add re-attempts to store-fresh (the CompareAndSwap path picks them up lazily) or Run starts sweeping. This is the spec's User Story 4 acceptance scenario 3.

**Post-cancellation state**: after `ctx.Done()`, Run returns and the cache stops sweeping but Add continues to function — the cache becomes a "store-only" log of nonces with no maintenance. Consumers that cancel Run typically then discard the cache instance.

---

## Internal types (unexported)

### `nonceCache` struct

```go
type nonceCache struct {
    entries       sync.Map      // key: string (nonce), value: time.Time (absolute expiry)
    sweepInterval time.Duration // 30s default; test seams use shorter intervals
}
```

`NewNonceCache()` returns `&nonceCache{sweepInterval: 30 * time.Second}` typed as the `NonceCache` interface. Zero state on construction; Run is the only path that produces background activity.

### `nowFn` clock indirection

```go
//nolint:gochecknoglobals // test-swappable clock per Constitution IX (sentinel-class precedent)
var nowFn = time.Now
```

Used by `Add` (for expiry computation), the sweep loop (for expiry comparison), and `IsFreshTimestamp` (for delta computation). Tests swap via `setClockForTest(t, fixed time.Time)` (a `_test.go`-only helper); production code paths cannot mutate it.

### `rawMessageType` reflect sentinel

```go
//nolint:gochecknoglobals // sentinel-class type token, set-once at package load
var rawMessageType = reflect.TypeOf(RawMessage(nil))
```

Used by the canonical encoder's recursive walker to detect `RawMessage`-typed values BEFORE the generic `reflect.Slice` branch. The check is `v.Type() == rawMessageType` — exact type match, not assignable; a user-defined `type MyRaw []byte` does NOT trigger.

---

## Sentinel error catalogue

All sentinels live in `errors.go`. Each is `var ErrXxx = errors.New("hush/transport/sign: <message>")`. There are NO wrap relationships between sentinels — the six categories are independent.

```go
var (
    // Signature-rejection (FR-005, FR-015) — collapsed for any signature failure.
    ErrSignatureInvalid = errors.New("hush/transport/sign: signature invalid")

    // Nonce-cache categories.
    ErrNonceReplay     = errors.New("hush/transport/sign: nonce replay")
    ErrNonceEncoding   = errors.New("hush/transport/sign: nonce encoding invalid (length out of [8,128])")
    ErrNonceTTLInvalid = errors.New("hush/transport/sign: nonce ttl must be positive")

    // Timestamp freshness (consumer-mapped from IsFreshTimestamp's bool false; sentinel exposed
    // here so consumers do not redefine their own).
    ErrTimestampStale = errors.New("hush/transport/sign: timestamp outside freshness window")

    // Canonical-encoder rejection (FR-002 + FR-023; same category for non-finite floats and
    // unsupported types).
    ErrCanonicalUnsupported = errors.New("hush/transport/sign: value cannot be canonicalised")
)
```

| Sentinel                | Field(s) it can apply to                                       | Wraps |
|-------------------------|----------------------------------------------------------------|-------|
| `ErrSignatureInvalid`   | `Verify` return — wrong key / tampered payload / malformed sig | (none) |
| `ErrNonceReplay`        | `NonceCache.Add` return — already-seen nonce within TTL        | (none) |
| `ErrNonceEncoding`      | `NonceCache.Add` return — empty or out-of-range length nonce   | (none) |
| `ErrNonceTTLInvalid`    | `NonceCache.Add` return — non-positive ttl                     | (none) |
| `ErrTimestampStale`     | Consumer-side; `IsFreshTimestamp` returns bool `false`         | (none) |
| `ErrCanonicalUnsupported` | `CanonicalJSON` return — non-finite float, unsupported type, non-string-keyed map | (none) |

**Multi-violation behaviour**: not applicable. Each primitive (`Sign`, `Verify`, `Add`, `CanonicalJSON`, `IsFreshTimestamp`) is a single-step operation that returns a single failure category. The composition recipe (R-009 / contracts/api.md §Composition recipe) sequences them; a downstream `errors.Join` would only appear in SDD-12's verify middleware, not inside this package.

---

## Lifecycle / state transitions

### `Sign` lifecycle (one-shot)

```
caller calls Sign(ctx, privKey, payload)
   └── (1) ctx.Err() check — pre-cancellation returns ctx.Err()
   └── (2) sha256.Sum256(payload) — 32-byte digest
   └── (3) ecdsa.SignASN1(rand.Reader, privKey, digest[:])
   └── return (sig, nil) on success; (nil, err) on stdlib failure
```

Pure / synchronous. Reads `crypto/rand.Reader` for ECDSA randomness. No goroutines, no I/O beyond the entropy source.

### `Verify` lifecycle (one-shot)

```
caller calls Verify(ctx, pubKey, payload, sig)
   └── (1) ctx.Err() check — pre-cancellation returns ctx.Err()
   └── (2) sha256.Sum256(payload) — 32-byte digest
   └── (3) ecdsa.VerifyASN1(pubKey, digest[:], sig) — bool
   └── return nil on true; ErrSignatureInvalid (wrapped with operation context) on false
```

Pure / synchronous. Stdlib's `VerifyASN1` is panic-free under hostile DER inputs (asserted by `FuzzVerifyRequest`).

### `CanonicalJSON` lifecycle (one-shot, recursive)

```
caller calls CanonicalJSON(v)
   └── (1) buf := new(bytes.Buffer)
   └── (2) encodeValue(buf, reflect.ValueOf(v))
   │        └── recursive descent — see R-001 walker
   │        └── on error: discard buf, return (nil, err)
   └── return (buf.Bytes(), nil) on success
```

Pure / synchronous. The buffer is local; there is no shared state. Errors propagate up the recursion via plain Go return. No goroutines.

### `NonceCache.Add` lifecycle (one-shot, atomic)

```
caller calls cache.Add(ctx, nonce, ttl)
   └── (1) ctx.Err() check — pre-cancellation returns (false, ctx.Err())
   └── (2) length-gate — 8 ≤ len(nonce) ≤ 128
   │        └── violation: return (false, ErrNonceEncoding)
   └── (3) TTL gate — ttl > 0
   │        └── violation: return (false, ErrNonceTTLInvalid)
   └── (4) sync.Map.LoadOrStore(nonce, nowFn() + ttl)
   │        └── store-fresh: return (true, nil)
   │        └── load-existing → check expiry:
   │             ├── not yet expired: return (false, ErrNonceReplay)
   │             └── expired:
   │                  ├── CompareAndSwap success: return (true, nil)
   │                  └── CompareAndSwap fail: return (false, ErrNonceReplay)
```

Atomic. The CAS-on-expired path resolves the spec's Edge Case 3 ("a nonce presented exactly at the moment its TTL expires is treated consistently"): exactly one CAS-winner per concurrent re-attempt set.

### `NonceCache.Run` lifecycle (long-running)

```
caller invokes go cache.Run(ctx)
   └── ticker = time.NewTicker(c.sweepInterval)
   └── defer ticker.Stop()
   └── for {
   │       select {
   │       case <-ctx.Done():
   │            slog.Info("hush/transport/sign: nonce cache sweep stopped",
   │                       slog.String("reason", ctx.Err().Error()))
   │            return
   │       case <-ticker.C:
   │            sweep — sync.Map.Range with CompareAndDelete on expired entries
   │       }
   │    }
```

State transitions:
- **NotRunning** → **Running**: triggered by the caller's `go cache.Run(ctx)` invocation. No state inside the cache; "Running" is implicit in the caller's goroutine being alive.
- **Running** → **Stopped**: triggered by `ctx.Done()`. Run returns; the goroutine ends; the cache enters "store-only" mode.

The cache itself does not track Run state — there is no `IsRunning()` method, no `Stop()` method, no internal `running bool`. The lifecycle is entirely caller-owned per Constitution IX.

### `IsFreshTimestamp` lifecycle (one-shot, pure)

```
caller calls IsFreshTimestamp(ts, skew)
   └── (1) skew ≤ 0 → return false (FR-012 positive-window guard)
   └── (2) delta = abs(nowFn().Sub(ts))
   └── (3) return delta ≤ skew
```

Pure / synchronous. No state. No goroutines. No allocations beyond the implicit `time.Time` arithmetic (stack-only).

---

## Acceptance-criterion → entity / behaviour mapping

| Spec requirement / SC | Entity / behaviour                                                                                |
|-----------------------|---------------------------------------------------------------------------------------------------|
| FR-001 (canonical determinism) | `CanonicalJSON` recursive walker — sorts maps and structs at every depth                  |
| FR-002 (non-finite-float rejection) | `encodeValue` Float branch — `math.IsNaN` / `math.IsInf` → `ErrCanonicalUnsupported`   |
| FR-003 (Sign primitive) | `Sign` — `ecdsa.SignASN1` over `sha256.Sum256(payload)`                                          |
| FR-004 (Verify primitive) | `Verify` — `ecdsa.VerifyASN1` mirror                                                           |
| FR-005 (signature-rejection unification) | `Verify` returns `ErrSignatureInvalid` for every failure mode                       |
| FR-006 (nonce cache TTL semantics) | `nonceCache` `sync.Map[string]time.Time`; sweep removes expired entries                  |
| FR-007 (Add binary outcome) | `Add` returns `(true, nil)` or `(false, ErrNonceReplay)` (modulo encoding/TTL gates)            |
| FR-008 (concurrent-Add exactly-one) | `Add` uses `sync.Map.LoadOrStore` + `CompareAndSwap`; tested by `TestNonceCache_ConcurrentAdd` under -race |
| FR-009 (no implicit goroutines) | Construction (`NewNonceCache`) spawns nothing; only `Run` produces background activity        |
| FR-010 (Run cancellation) | `Run` returns within one ticker interval after `ctx.Done()`; tested by `TestNonceCache_RunStopsOnContextCancel` |
| FR-011 (timestamp freshness) | `IsFreshTimestamp` — `\|now - ts\| ≤ skew`; consumer maps false to `ErrTimestampStale`         |
| FR-012 (skew positive) | `IsFreshTimestamp` returns false for `skew ≤ 0`; `Add` returns `ErrNonceTTLInvalid` for `ttl ≤ 0` |
| FR-013 (no nonce-burn on stale-timestamp) | Composition recipe orders timestamp-check BEFORE nonce-Add; tested by SDD-12 integration |
| FR-014 (no nonce-burn on bad-sig) | Composition recipe orders signature-verify BEFORE nonce-Add; tested by SDD-12 integration       |
| FR-015 (three distinct sentinels) | Sentinel catalogue table above (six entries, no wrap relationships)                          |
| FR-016 (panic-free under hostile input) | `FuzzVerifyRequest` 60s gate; assertion: every error is `ErrSignatureInvalid`            |
| FR-017 (no nonces / sigs / keys in logs) | Single Run log line carries `reason=ctx.Err()` only; `TestVerify_NoLeakOnFuzzInput`      |
| FR-018 (ctx-first, ctx-honoured) | `Sign`, `Verify`, `Add` accept `ctx` first; pre-cancellation returns `ctx.Err()`               |
| FR-019 (consumer includes nonce/ts in payload) | Documented in [quickstart](./quickstart.md) and [contracts/api.md](./contracts/api.md); not enforced by package shape |
| FR-020 (no capacity cap) | `nonceCache` has no entry-count field, no eviction policy beyond TTL                            |
| FR-021 (nonce length bounds) | `Add` enforces `8 ≤ len ≤ 128`, returns `ErrNonceEncoding` BEFORE cache lookup                 |
| FR-022 (canonical input domain) | `encodeValue` rejects function/channel/complex/non-string-keyed-map; ignores `MarshalJSON`     |
| FR-023 (canonical-rejection sentinel) | Same `ErrCanonicalUnsupported` for non-finite-float and unsupported-type rejections      |
| FR-024 (no metrics emission) | Package emits zero counters/gauges; consumers derive operational signals from sentinel identity |
| SC-001 (10+ shape variants) | `TestCanonical_SortsAtAllDepths` — table-driven with 10 known shapes per [research R-008](./research.md#r-008--test-corpus-discipline-canonical-encoder-shapes) |
| SC-002 (round-trip) | `TestSign_VerifyRoundTrip` — fresh keypair, sign + verify                                       |
| SC-003 (wrong-key fails) | `TestVerify_WrongKeyFails` — sign with key A, verify with key B's public                      |
| SC-004 (replay rejected; post-TTL accepted) | `TestNonce_AddDuplicateReturnsReplay` + `TestNonce_ExpiredAllowedAfterSweep`        |
| SC-005 (timestamp boundary determinism) | `TestTimestamp_FreshAccepted` + `TestTimestamp_TooOldRejected` + `TestTimestamp_FutureSkewRejected` |
| SC-006 (concurrent-Add race-clean) | `TestNonceCache_ConcurrentAdd` under `-race`                                              |
| SC-007 (Run lifecycle) | `TestNonceCache_RunStopsOnContextCancel` — pre-Run goroutine count, post-Run goroutine count    |
| SC-008 (60s fuzz clean) | `FuzzVerifyRequest` + the implement-phase release-step list                                    |
| SC-009 (100% coverage) | `go test -cover ./internal/transport/sign/` ≥ 100%                                              |
| SC-010 (no leaks in diagnostics) | `TestVerify_NoLeakOnFuzzInput` + manual review at SDD-08 PR time                          |

---

## Anti-model (what is NOT modelled)

- **No `*ecdsa.PrivateKey` retention**: the keys are passed to `Sign` / `Verify` as parameters; the package does not store them anywhere.
- **No nonce-content storage in expired entries**: the sweep removes the `(nonce, expiry)` entry entirely; the package does not maintain a "previously seen" log beyond active entries.
- **No metrics emission**: per FR-024, the package emits zero counters. Consumers (SDD-12, SDD-16) derive their own counters from the sentinel-error identity.
- **No exported orchestrator**: there is no `VerifyRequest(...)` function that bundles all four primitives. The composition recipe is a contract, not an exported function. Consumers compose the primitives.
- **No Bitcoin-Signed-Message envelope**: the canonical bytes ARE the message; no `\x18Bitcoin Signed Message:\n` prefix is added (R-002).
- **No on-disk nonce log**: a server restart resets the cache; replays that span a restart are caught by the timestamp-stale check.
- **No `IsRunning()` / `Stop()` method on NonceCache**: lifecycle is caller-owned via `ctx`; there is no internal flag.
- **No CGO dependencies**: pure Go.
- **No package-level mutable state outside sentinels + `nowFn` + `rawMessageType`**: every other value is local to a function or is held inside a `sync.Map`.
