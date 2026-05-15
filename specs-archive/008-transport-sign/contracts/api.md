# Contract: `internal/transport/sign` exported API

**Feature**: 008-transport-sign
**Status**: Locked at SDD-08; mirrored into `docs/PACKAGE-MAP.md` once the implement commit lands.

This is the contract every downstream package (SDD-12 server `/claim` + `/revoke`, SDD-16 `hush request`, future audit-log validators) depends on. Changes after SDD-08 lands require a new SDD chunk; consumers may rely on every signature, every sentinel identity, and the composition recipe below.

SDD-09 (`internal/transport/ecies`) will land as a sibling sub-package and may share helper utilities if a genuine shared concern emerges. SDD-09 MUST NOT alter any symbol below.

---

## Package path

```
github.com/mrz1836/hush/internal/transport/sign
```

---

## Exported types

### `type RawMessage []byte`

```go
type RawMessage []byte
```

**Contract**:
- A named byte slice whose contents are inserted **verbatim** into the canonical output when the value appears as a leaf or sub-value in the input to `CanonicalJSON`.
- The caller guarantees the bytes are themselves canonical-JSON-shaped. The encoder performs no validation.
- A `nil` or zero-length `RawMessage` emits the JSON `null` literal (mirrors stdlib `json.RawMessage` MarshalJSON behaviour).
- Detected via exact `reflect.Type` match — a user-defined `type MyRaw []byte` does NOT trigger the verbatim path.

### `type NonceCache interface`

```go
type NonceCache interface {
    Add(ctx context.Context, nonce string, ttl time.Duration) (firstSeen bool, err error)
    Run(ctx context.Context)
}
```

**Contract**:
- Both methods are part of the locked interface; no third method is allowed without a new SDD chunk.
- Implementations MUST be safe for concurrent `Add` from many goroutines (FR-008).
- `Run` MUST be synchronous — the caller invokes it inside their own goroutine (`go cache.Run(ctx)`) and Run blocks until `ctx.Done()` (FR-009).
- `NewNonceCache` is the only supported way to construct a `NonceCache` instance; a zero-valued interface (e.g., `var nc NonceCache`) is not usable.

---

## Exported functions

### `func CanonicalJSON(v any) ([]byte, error)`

```go
func CanonicalJSON(v any) ([]byte, error)
```

**Inputs**:
- `v` — any value. Accepted shapes: `nil`, booleans, signed/unsigned integers, finite `float32`/`float64`, strings, slices/arrays of accepted shapes, maps with string keys and accepted-shape values, structs whose exported fields are accepted shapes, pointers/interfaces dereferenced to accepted shapes, and `RawMessage` (verbatim).

**Output**:
- On success: a canonical-JSON byte sequence. The bytes are byte-identical for any two inputs that are semantically equal regardless of map iteration order.
- On failure: `nil, err`. NEVER returns a partial byte sequence with a non-nil error.

**Failure cases**:
- Non-finite float (`NaN`, `+Inf`, `-Inf`) → `ErrCanonicalUnsupported`.
- Non-string-keyed map → `ErrCanonicalUnsupported`.
- Function / channel / complex / unsafe-pointer / invalid value → `ErrCanonicalUnsupported`.
- A `[]byte` (unnamed) outside `RawMessage` → `ErrCanonicalUnsupported`. Callers wishing to embed bytes verbatim use `RawMessage`; callers wishing to embed bytes as a base64 string convert to `string` themselves before calling.

**Determinism**:
- Map keys are sorted lexicographically by raw key bytes (`sort.Strings`).
- Struct fields are emitted in alphabetical order of resolved field name. Tag `json:"name"` honoured for renaming only — `,omitempty` and other options are ignored.
- User-defined `MarshalJSON` is **NOT** invoked. The encoder walks `reflect.Value` directly.

**Concurrency**: safe to call concurrently. The function holds no state.

### `func Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error)`

```go
func Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error)
```

**Inputs**:
- `ctx` — checked once at entry; pre-cancellation returns `ctx.Err()` immediately. Cancellation arriving mid-`SignASN1` is not honoured (the operation is short and CPU-bound).
- `key` — a `*ecdsa.PrivateKey` whose curve is secp256k1. The caller obtains this from `keys.DeriveClientKey` or equivalent. The key MUST NOT be nil; passing nil is undefined behaviour (the stdlib will panic).
- `payload` — the bytes to sign. Typically the output of `CanonicalJSON`.

**Output**:
- On success: a DER-encoded ASN.1 ECDSA signature. The byte length is variable (typically 70–72 bytes for secp256k1).
- On failure: `nil, err`. The only documented failure is pre-cancellation (`ctx.Err()`).

**Concurrency**: safe to call concurrently. The function reads `crypto/rand.Reader` for ECDSA randomness; the stdlib reader is concurrency-safe.

### `func Verify(ctx context.Context, key *ecdsa.PublicKey, payload, sig []byte) error`

```go
func Verify(ctx context.Context, key *ecdsa.PublicKey, payload, sig []byte) error
```

**Inputs**:
- `ctx` — checked once at entry; pre-cancellation returns `ctx.Err()` immediately.
- `key` — a `*ecdsa.PublicKey` whose curve is secp256k1. MUST NOT be nil.
- `payload` — the bytes that were signed. Typically the output of `CanonicalJSON` recomputed on the recipient's side.
- `sig` — the candidate signature. Any byte sequence; malformed sigs are rejected with `ErrSignatureInvalid`.

**Output**:
- `nil` on full verification success.
- `ErrSignatureInvalid` (wrapped with operation context) for **every** signature failure: wrong key, tampered payload, malformed DER, wrong-length sig.
- `ctx.Err()` if the context is pre-cancelled.

**Sentinel matching**: `errors.Is(err, ErrSignatureInvalid)` returns `true` for any signature-rejection failure. Consumers cannot distinguish "wrong key" from "tampered payload" — by design (FR-005, no signature-shape leakage).

**Panic-free**: `Verify` is panic-free under any byte input (FR-016, asserted by `FuzzVerifyRequest` 60 s gate).

**Concurrency**: safe to call concurrently.

### `func NewNonceCache() NonceCache`

```go
func NewNonceCache() NonceCache
```

**Inputs**: none.

**Output**: a fresh `NonceCache` with sweep interval defaulting to 30 s.

**Side effects**: NONE. The constructor spawns NO goroutines (FR-009). Background sweep starts only when the caller invokes `Run`.

**Concurrency**: safe to call concurrently. Each call returns an independent cache instance; instances do not share state.

### `func IsFreshTimestamp(ts time.Time, skew time.Duration) bool`

```go
func IsFreshTimestamp(ts time.Time, skew time.Duration) bool
```

**Inputs**:
- `ts` — the candidate timestamp.
- `skew` — the symmetric freshness window. MUST be positive; non-positive returns `false` unconditionally.

**Output**: `true` iff `|now() - ts| <= skew`. Boundary value (`delta == skew`) is **accepted** — the comparison is `<=`, not `<`.

**Time source**: the package-private `nowFn` (defaults to `time.Now`; tests swap via `setClockForTest`).

**Concurrency**: safe to call concurrently. The function reads `nowFn` (a single read of a package-level function variable) and performs arithmetic on local values.

---

## NonceCache method contracts

### `Add(ctx, nonce, ttl) (firstSeen bool, err error)`

| Pre-condition | Outcome |
|---------------|---------|
| `ctx.Err() != nil` | `(false, ctx.Err())` |
| `len(nonce) < 8` or `len(nonce) > 128` | `(false, ErrNonceEncoding)` |
| `ttl <= 0` | `(false, ErrNonceTTLInvalid)` |
| nonce absent from cache | `(true, nil)` — entry stored with absolute expiry `nowFn() + ttl` |
| nonce present, not yet expired | `(false, ErrNonceReplay)` |
| nonce present, already expired (sweep hasn't run yet) | exactly one concurrent caller observes `(true, nil)` via CompareAndSwap; others observe `(false, ErrNonceReplay)` |

**Concurrency**: under N concurrent callers presenting the same nonce with the same TTL, exactly one observes `firstSeen=true` and N-1 observe `firstSeen=false` with `ErrNonceReplay`. Asserted by `TestNonceCache_ConcurrentAdd` under `-race`.

**Cancellation**: a cancelled context returns immediately at entry; mid-call cancellation is not supported (Add is a few atomic operations).

**Idempotence**: not idempotent. Add is a state-mutation. Two calls with the same fresh nonce produce different outcomes (first → first-seen; second → replay).

### `Run(ctx)`

**Pre-condition**: caller invokes inside its own goroutine (`go cache.Run(ctx)`).

**Behaviour**:
- Spawns no further goroutines.
- Sweeps every 30 s (default sweep interval). Each sweep is `O(N)` over current cache entries; expired entries are removed via `CompareAndDelete`.
- Returns when `ctx.Done()` fires. Cancellation latency is bounded by one sweep interval.
- Emits exactly one log line on exit: `slog.Info("hush/transport/sign: nonce cache sweep stopped", slog.String("reason", ctx.Err().Error()))`. No nonces, no counts, no per-key data.

**Post-condition**: after Run returns, the cache enters "store-only" mode — Add still functions but expired entries accumulate. Consumers typically discard the cache instance after Run returns.

**Concurrency**: Run MUST NOT be invoked twice on the same cache instance from different goroutines simultaneously — the second invocation would race on the shared sync.Map sweep and double the sweep work. (The package does not guard against this; it is a caller invariant.) A future `RunOnce`-style guard could be added if the constraint becomes operationally important.

---

## Composition recipe

The four primitives are deliberately independent. The canonical ordering for SDD-12's `/claim` and `/revoke` middleware (and any future verifying consumer) is:

```go
// 1. Canonical-bytes recompute.
canonical, err := sign.CanonicalJSON(req)
if err != nil {
    return err // ErrCanonicalUnsupported, etc.
}

// 2. Signature first — an attacker who forged a sig MUST NOT consume the operator's nonce budget.
if err := sign.Verify(ctx, registeredPubKey, canonical, req.Sig); err != nil {
    return err // ErrSignatureInvalid
}

// 3. Timestamp second — a stale-but-validly-signed request MUST NOT burn the nonce.
if !sign.IsFreshTimestamp(req.Timestamp, skew) {
    return sign.ErrTimestampStale
}

// 4. Nonce LAST — only after both signature and timestamp pass do we commit to the cache.
firstSeen, err := nonceCache.Add(ctx, req.Nonce, ttl)
if err != nil {
    return err // ErrNonceEncoding, ErrNonceTTLInvalid, ErrNonceReplay
}
if !firstSeen {
    return sign.ErrNonceReplay
}

return nil // accepted
```

**Why this ordering**:
- **FR-013 + FR-014 anti-burn invariant**: an attacker MUST NOT be able to "burn" a nonce for an honest client by submitting a forged signature OR a stale timestamp. Cache enrolment happens last, after both gates have passed.
- **Signature first (cheaper-fail-faster)**: a forged signature is the cheaper rejection (no clock read). Common-case attacks fail early.
- **Timestamp second**: rejecting a stale-but-validly-signed replay before nonce-cache enrolment.
- **Nonce last**: the load-bearing state mutation. Only well-formed, signed, fresh requests touch the cache.

**FR-019 — caller bundles nonce + timestamp INTO the canonical payload**: the consumer's `req` value MUST include both `Nonce` and `Timestamp` as fields of the canonical payload. The signature transitively covers them because they are part of the canonical bytes that are hashed and signed. A consumer that omits the nonce or the timestamp from the signed payload breaks the replay-defence contract: an attacker who captured a valid `(payload, sig, nonce, timestamp)` tuple could otherwise replay the same payload+sig with a fresh nonce+timestamp and the request would be admitted. The package does NOT enforce this — it is a caller invariant documented here and in [quickstart.md](../quickstart.md).

---

## Sentinel error catalogue

All sentinels are `var ErrXxx = errors.New("hush/transport/sign: <message>")` declarations. Compare via `errors.Is`. **No wrap relationships** between sentinels — each category is independent.

| Sentinel                  | Triggered by                                                                          |
|---------------------------|---------------------------------------------------------------------------------------|
| `ErrSignatureInvalid`     | Any signature failure: wrong key, tampered payload, malformed DER, wrong-length sig.  |
| `ErrNonceReplay`          | The nonce was already accepted within its TTL.                                        |
| `ErrNonceEncoding`        | The nonce is empty or its length is outside `[8, 128]`.                                |
| `ErrNonceTTLInvalid`      | A non-positive `ttl` was passed to `NonceCache.Add`.                                  |
| `ErrTimestampStale`       | Consumer-mapped from `IsFreshTimestamp` returning `false`. The package does not return this directly from any primitive; it is exposed so consumers do not redefine it. |
| `ErrCanonicalUnsupported` | A value cannot be canonicalised (non-finite float, unsupported Go kind, non-string-keyed map). Same sentinel for all canonical-encoding rejections (FR-002 + FR-023). |

**Multi-violation behaviour**: not applicable inside this package. Each primitive is a single-step operation that returns a single failure category. Multi-violation reporting (via `errors.Join`) appears only in the consumer's verify middleware (SDD-12), if at all.

---

## Behavioural invariants (testable contract)

| Invariant | Spec ref | Test name (in tasks phase) |
|-----------|----------|----------------------------|
| Two semantically-equal map values produce byte-identical canonical output at every depth | FR-001, SC-001 | `TestCanonical_SortsAtAllDepths` (10-shape table) |
| Struct fields are emitted in alphabetical order of resolved field name (gotcha: stdlib does NOT) | FR-001 | `TestCanonical_StructAndMap` |
| `map[string]any` keys are sorted at every depth (gotcha: stdlib sorts only top level) | FR-001 | `TestCanonical_StructAndMap` |
| `RawMessage` is embedded verbatim in the canonical output | FR-022 (escape hatch), Edge Case 4 | `TestCanonical_EmbedsRawMessageVerbatim` |
| `NaN` returns `ErrCanonicalUnsupported` and no partial output | FR-002 | `TestCanonical_RejectsNaN` |
| `+Inf` returns `ErrCanonicalUnsupported` | FR-002 | `TestCanonical_RejectsInf` |
| `-Inf` returns `ErrCanonicalUnsupported` | FR-002 | `TestCanonical_RejectsNegInf` |
| Function value returns `ErrCanonicalUnsupported` | FR-022 + FR-023 | `TestCanonical_RejectsFunc` |
| Channel value returns `ErrCanonicalUnsupported` | FR-022 + FR-023 | `TestCanonical_RejectsChan` |
| Complex value returns `ErrCanonicalUnsupported` | FR-022 + FR-023 | `TestCanonical_RejectsComplex` |
| Non-string-keyed map returns `ErrCanonicalUnsupported` | FR-022 | `TestCanonical_RejectsNonStringMap` |
| User-defined `MarshalJSON` is NOT honoured | FR-022 anti-feature | `TestCanonical_IgnoresMarshalJSON` |
| Round-trip Sign + Verify with matching key returns `nil` | FR-003 + FR-004, SC-002 | `TestSign_VerifyRoundTrip` |
| Verify with wrong public key returns `ErrSignatureInvalid` | FR-005, SC-003 | `TestVerify_WrongKeyFails` |
| Verify with tampered payload returns `ErrSignatureInvalid` | FR-005 | `TestVerify_TamperedPayloadFails` |
| Verify with malformed DER signature returns `ErrSignatureInvalid` | FR-005 (Edge Case 6) | `TestVerify_MalformedDERFails` |
| Verify is panic-free under fuzz pressure | FR-016, SC-008 | `FuzzVerifyRequest` (60 s gate) |
| Verify diagnostic output never contains nonce/sig/payload bytes | FR-017, SC-010 | `TestVerify_NoLeakOnFuzzInput` |
| Add of a fresh nonce returns `firstSeen=true` | FR-006 + FR-007 | `TestNonce_AddNewReturnsFirstSeen` |
| Add of a duplicate nonce returns `firstSeen=false, ErrNonceReplay` | FR-006 + FR-007 | `TestNonce_AddDuplicateReturnsReplay` |
| Add of an empty nonce returns `ErrNonceEncoding` | FR-021 + Edge Case 1 | `TestNonce_AddEmptyReturnsEncodingError` |
| Add of a too-long nonce returns `ErrNonceEncoding` | FR-021 + Edge Case 1 | `TestNonce_AddTooLongReturnsEncodingError` |
| Add of a too-short nonce returns `ErrNonceEncoding` | FR-021 + Edge Case 1 | `TestNonce_AddTooShortReturnsEncodingError` |
| Add of a nonce with `ttl <= 0` returns `ErrNonceTTLInvalid` | FR-012 | `TestNonce_AddNonPositiveTTLReturnsInvalid` |
| Add of a previously-expired nonce after sweep returns `firstSeen=true` | FR-006 + Edge Case 3, SC-004 | `TestNonce_ExpiredAllowedAfterSweep` |
| N concurrent Adds of the same nonce produce exactly one `firstSeen=true` | FR-008, SC-006 | `TestNonceCache_ConcurrentAdd` (under `-race`) |
| N concurrent Adds of N distinct nonces all produce `firstSeen=true` | FR-008 | `TestNonceCache_ConcurrentDistinct` |
| Construction (`NewNonceCache`) spawns zero goroutines | FR-009, SC-007 | `TestNewNonceCache_NoGoroutineSpawned` |
| Run returns within one sweep interval after `ctx.Done()` | FR-010, SC-007 | `TestNonceCache_RunStopsOnContextCancel` |
| Run logs exactly one "stopped" line on exit, with no nonce / count / per-key data | FR-017 | `TestNonceCache_RunLogsStoppedOnce` |
| Add works before Run is invoked (no implicit goroutine) | FR-009 + User Story 4 acceptance #3 | `TestNonceCache_AddWorksWithoutRun` |
| Cancelled context to Add returns `ctx.Err()` | FR-018 | `TestNonce_AddRespectsCancelledContext` |
| Fresh timestamp inside skew returns `true` | FR-011, SC-005 | `TestTimestamp_FreshAccepted` |
| Timestamp older than skew returns `false` | FR-011, SC-005 | `TestTimestamp_TooOldRejected` |
| Timestamp further in future than skew returns `false` | FR-011, SC-005 | `TestTimestamp_FutureSkewRejected` |
| Timestamp exactly at skew boundary returns `true` (`<=` semantics) | Edge Case 2 | `TestTimestamp_BoundaryAccepted` |
| Non-positive skew returns `false` | FR-012 | `TestTimestamp_NonPositiveSkewRejected` |

---

## Non-contract (explicit non-promises)

- **No exported orchestrator**. There is no `VerifyRequest(ctx, pub, payload, sig, nonce, ts)` function that bundles all four primitives. Consumers compose using the recipe above.
- **No exported `nonceCache` struct type**. `NewNonceCache` returns the `NonceCache` interface; the underlying struct is unexported.
- **No exported sweep-interval setter**. The 30 s sweep interval is fixed for the locked API. Tests use a package-private constructor variant via a `_test.go` seam.
- **No `IsRunning()` or `Stop()` method on NonceCache**. Lifecycle is caller-owned via `ctx`.
- **No package-level metrics**. FR-024 forbids; consumers derive metrics from sentinel-error identity.
- **No nonce content storage in expired entries**. The sweep removes the entry entirely; the package does not maintain a "previously seen" log beyond active entries.
- **No nonce / signature / payload byte appearance in any error message or log line**. FR-017.
- **No environment-variable reads, no file I/O, no network I/O**. The package is purely in-memory (modulo the entropy source `crypto/rand.Reader`).
- **No support for non-secp256k1 curves**. Constitution III pins secp256k1; passing a key on a different curve produces undefined behaviour (typically a stdlib panic from `ecdsa.SignASN1`).
- **No Bitcoin-Signed-Message envelope**. The canonical bytes ARE the message; no `\x18Bitcoin Signed Message:\n` prefix is added (research R-002).
- **No on-disk nonce log**. Server restart resets the nonce cache; replays that span a restart are caught by `IsFreshTimestamp`.
- **No FR-019 enforcement**. The package documents the caller's responsibility to include the nonce and the timestamp in the canonical payload but does not enforce payload shape.

---

## Deprecation policy

This contract is **frozen** at SDD-08 ship. Adding a new sentinel error to the catalogue is non-breaking iff the new sentinel surfaces a new failure category that current consumers do not depend on the absence of (e.g., a future tightening of canonical-encoder rules). Removing or renaming any exported symbol requires a new SDD chunk; SDD-12 and SDD-16 both depend on the surface above.

Adding a third method to the `NonceCache` interface is breaking — interface widening invalidates every existing implementation. Such a change requires a new chunk and a deprecation notice on this contract.
