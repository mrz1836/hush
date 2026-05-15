# Phase 0 Research: `internal/transport/sign`

**Feature**: 008-transport-sign — canonical-JSON request signing + replay protection
**Date**: 2026-04-28

This document resolves every technical decision the plan depends on. Each entry follows the **Decision / Rationale / Alternatives considered** format. There are no remaining `NEEDS CLARIFICATION` markers in the spec; the five clarification answers from Session 2026-04-28 (`spec.md` §Clarifications) are encoded into the relevant decisions below.

---

## R-001 — CanonicalJSON: recursive reflect-walker that sorts at every depth

**Decision**: Implement `CanonicalJSON(v any) ([]byte, error)` as a single recursive function `encodeValue(buf *bytes.Buffer, v reflect.Value) error` driven by `reflect.Kind` switching:

- `reflect.Pointer` / `reflect.Interface`: `nil` → emit `null`; non-nil → recurse into `v.Elem()`.
- `reflect.Bool`: `strconv.AppendBool`.
- `reflect.Int`, `reflect.Int8`..`reflect.Int64`: `strconv.AppendInt(_, v.Int(), 10)`.
- `reflect.Uint`, `reflect.Uint8`..`reflect.Uint64`, `reflect.Uintptr`: `strconv.AppendUint(_, v.Uint(), 10)`.
- `reflect.Float32` / `reflect.Float64`: reject non-finite (`math.IsNaN(f) || math.IsInf(f, 0)` → `ErrCanonicalUnsupported`); finite → `strconv.AppendFloat(_, f, 'g', -1, 64)` (the canonical Go default; matches `json.Marshal`'s output for finite floats).
- `reflect.String`: emit a JSON-quoted string. The implementation uses `json.Marshal(v.String())` — `v.String()` returns the underlying basic `string`, never invoking a user-defined `MarshalJSON` even on a named-string type. The result is the JSON-correct quoted form (with `\u`-escapes for control bytes and `<`/`>`/`&` per stdlib behaviour).
- `reflect.Slice` / `reflect.Array`: emit `[`, recurse into each element separated by `,`, emit `]`. Element order is preserved (slices are inherently ordered). A `RawMessage`-typed value (a named `[]byte` alias — see R-002a) is special-cased BEFORE the slice branch: its bytes are emitted verbatim. A `[]byte` (unnamed) is rejected per R-005.
- `reflect.Map`: require `v.Type().Key().Kind() == reflect.String` (otherwise `ErrCanonicalUnsupported`); collect keys via `v.MapKeys()` into a `[]string`; sort lexicographically by raw key bytes via `sort.Strings`; emit `{`, then `key,value` pairs separated by `,` (each key emitted via the `reflect.String` branch, then `:`, then recurse into the value), emit `}`.
- `reflect.Struct`: enumerate exported fields in alphabetical order of the resolved field name; the resolved field name is taken from the `json:"name"` tag if present, otherwise the field's Go name. Tag options (`,omitempty`, `,string`) are **ignored** so the output is byte-deterministic for any concrete struct value (FR-022 anti-feature: a non-deterministic hook would silently break signature verification). Unexported fields are skipped. Anonymous (embedded) structs are walked transparently — their fields are flattened into the enclosing struct's name set, with collisions resolved by Go's standard depth-then-declaration tiebreak.
- `reflect.Func`, `reflect.Chan`, `reflect.UnsafePointer`, `reflect.Complex64`, `reflect.Complex128`, `reflect.Invalid`: `ErrCanonicalUnsupported`.

The encoder maintains **NO** state between recursive calls beyond the `*bytes.Buffer`. A failure mid-recursion does **NOT** flush partial output: the public entry point (`CanonicalJSON`) accumulates into a local buffer, and on error returns `nil, err` rather than the partial buffer (per FR-002 + FR-023 "no partial output").

**Rationale**: A reflect-driven walker is the only Go idiom that satisfies *all* of: byte-deterministic output regardless of map iteration order, struct-field alphabetisation regardless of declaration order, embedded `RawMessage` verbatim insertion, and a clean refusal of `MarshalJSON` hooks. The stdlib `encoding/json` package satisfies *some* of these (it sorts `map[string]any` keys at the top level, but does NOT recursively sort nested maps in all v1.x revisions, and DOES honour `MarshalJSON` — both deal-breakers for cryptographic determinism).

The `v.String()` / `v.Float()` / `v.Int()` family is the load-bearing detail: each returns the underlying basic Go value (plain `string`, `float64`, `int64`), so `json.Marshal` of those values cannot dispatch to a user-defined `MarshalJSON` — we can safely use stdlib for the terminal-string-quoting case without re-introducing the very hook we are excluding.

The "use json.Marshal for strings" tactic is bounded: it is used **only** for the single string-quoting concern (correctly handling control bytes and Unicode escapes per JSON spec). Numbers, booleans, and null are emitted by direct `strconv.Append*` calls; arrays / objects are emitted by recursion; nothing dispatches through `json.Marshal` for a user-typed value.

The two gotchas the chunk contract calls out are both exercised by tests:
- `map[string]any{"b": 1, "a": 2}` — stdlib's `json.Marshal` happens to sort top-level map keys alphabetically (Go 1.12+) but does NOT do so for nested maps in some compositions. Our recursive encoder sorts at every depth.
- `struct{ B int; A int }{B: 1, A: 2}` — stdlib emits in declaration order (`{"B":1,"A":2}`); our encoder alphabetises (`{"A":2,"B":1}`).

**Alternatives considered**:
- *Use stdlib `encoding/json` with `json.Marshal` directly*: rejected. (a) Stdlib invokes `MarshalJSON` on user types — a non-deterministic hook silently breaks signature verification across releases (FR-022). (b) Stdlib does not sort struct fields alphabetically. (c) Stdlib's nested-map ordering is implementation-defined for some compositions.
- *Use `github.com/gibson042/canonicaljson-go`*: rejected. New direct dependency for a single-purpose primitive; Constitution XI requires written justification, and the reflect-walker is ~250 LOC of well-understood Go. The cost of carrying the dependency exceeds the cost of writing the walker.
- *Pre-canonicalise via JCS (RFC 8785)*: rejected. JCS is the JSON Canonicalisation Scheme of record, but it is JSON-Number-format-strict (uses ECMAScript ToString, not Go's `strconv` default 'g' format). Implementing JCS would diverge from Go's natural number-formatting and surprise consumers writing tests against `fmt.Sprintf("%v", ...)`. The chunk-contract requirement is "byte-identical for semantically-equal inputs" — the reflect-walker satisfies that without committing to JCS-specific number formatting.
- *Use `gopkg.in/yaml.v3`-inspired ordered-map sort*: rejected. We are emitting JSON, not YAML; the sort-on-read trick imposes an extra type wrapper on every consumer.

---

## R-002 — Sign / Verify: stdlib `crypto/ecdsa.SignASN1` substitution

**Decision**: Implement `Sign(ctx, key *ecdsa.PrivateKey, payload []byte)` as:

```go
func Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    digest := sha256.Sum256(payload)
    return ecdsa.SignASN1(rand.Reader, key, digest[:])
}
```

and `Verify(ctx, key *ecdsa.PublicKey, payload, sig []byte)` as:

```go
func Verify(ctx context.Context, key *ecdsa.PublicKey, payload, sig []byte) error {
    if err := ctx.Err(); err != nil {
        return err
    }
    digest := sha256.Sum256(payload)
    if !ecdsa.VerifyASN1(key, digest[:], sig) {
        return ErrSignatureInvalid
    }
    return nil
}
```

The `*ecdsa.PrivateKey` / `*ecdsa.PublicKey` arguments carry the secp256k1 curve from `decred/dcrd/dcrec/secp256k1/v4` — the same key types `keys.DeriveClientKey` and `keys.DeriveJWTSigningKey` already produce. The signatures are DER-encoded ASN.1 (the standard format for `SignASN1` / `VerifyASN1`); no Bitcoin-Signed-Message envelope is added — the canonical bytes ARE the message, and the signature is over `SHA-256(canonical bytes)`.

**Rationale**: The chunk contract names `github.com/bitcoinschema/go-bitcoin` Bitcoin-style ECDSA over `SHA-256(payload)` and explicitly states "**NO new crypto deps.**" Those two clauses can both be honoured by reading the chunk contract's library reference at the *intent* level: the algorithm is ECDSA on the secp256k1 curve, the digest is SHA-256, the signature covers the canonical payload bytes. The curve, the algorithm, and the digest function are all already available via stdlib + the existing `decred/dcrd/dcrec/secp256k1/v4` direct dependency (introduced in SDD-01) — no new module is required.

This is a **stdlib-correct refinement** in the same spirit as SDD-06's R-003 (deprecated `filepath.HasPrefix` substituted with stdlib-correct `filepath.Rel`-based containment). The chunk contract's named library is honoured at the algorithmic and curve-choice level; the implementation library is the stdlib because the stdlib offers the same primitive with better supply-chain hygiene, fewer transitive dependencies, and zero CGO. The PR description for the implement commit will document this divergence verbatim.

Security equivalence:
- **Algorithm**: both `ecdsa.SignASN1` and go-bitcoin's `ecdsa.Sign` produce ECDSA signatures on the secp256k1 curve. The mathematical signature is identical; only the *encoding* differs (stdlib emits DER-encoded ASN.1; go-bitcoin's `SignCompact` emits a 65-byte recovery format). Since SDD-08 controls both ends of the protocol (client signs with the same library the server verifies with), DER-encoded ASN.1 is sufficient — there is no requirement to interoperate with external Bitcoin tooling.
- **Digest**: both libraries hash with SHA-256 over the canonical bytes. SHA-256 is `docs/SECURITY.md` §5 "Hash" requirement.
- **Curve**: secp256k1 (the Bitcoin curve), supplied by `decred/dcrd/dcrec/secp256k1/v4` and exposed via `*ecdsa.PrivateKey.Curve`. Stdlib `crypto/ecdsa.SignASN1` accepts any `elliptic.Curve` and falls back to a generic-curve scalar-multiplication path for non-stdlib curves; secp256k1 is a non-stdlib curve, so the generic path is used. The generic path is the same one Go has shipped since Go 1.0 and is well-fuzzed by the stdlib team.
- **Recipient distinguishability**: per FR-005, "wrong key" and "tampered payload" MUST return the same sentinel. Stdlib's `VerifyASN1` returns a single bool, not a categorised error, which is exactly the unified-failure-mode the spec demands. Implementations that return distinct error categories for "DER decode failed" vs "signature did not validate" would leak signature shape via timing or error identity — stdlib's binary-bool API closes that channel by construction.
- **Panic-free under hostile input**: `VerifyASN1` is the stdlib's official entry point and is fuzz-tested upstream. Hostile signatures (truncated DER, wrong-shape bytes, oversized integers) return `false`, never panic. The plan's `FuzzVerifyRequest` re-asserts this invariant for the secp256k1 generic path under the project's CI gate.

The `ctx.Err()` guard at entry honours Constitution IX (context as first parameter for cancellable work). Cancellation arriving mid-`SignASN1` is not honoured — the operation is short and CPU-bound; aborting an in-flight scalar multiplication has no security benefit and complicates the implementation.

**Alternatives considered**:
- *Add `github.com/bitcoinschema/go-bitcoin` as a new direct dependency*: rejected by the chunk contract's "NO new crypto deps" clause. Even if allowed, the dependency adds CGO-adjacent code (the library has historically had CGO build modes), pulls a non-trivial transitive surface, and offers no security improvement over stdlib for our use case.
- *Use go-bitcoin's `SignCompact` (recovery format) for compatibility with external Bitcoin tooling*: rejected. SDD-08 controls both ends of the wire protocol; we have no requirement to interoperate with external tooling. DER-encoded ASN.1 is the stdlib-natural format and is what `keys.DeriveClientKey`'s consumers already produce when calling stdlib ECDSA.
- *Use stdlib `crypto/ed25519`*: rejected. The project is committed to secp256k1 by Constitution III and `docs/SECURITY.md` Layer 2 + Layer 4. Switching curves would require a constitutional amendment.
- *Add a Bitcoin-Signed-Message envelope (`\x18Bitcoin Signed Message:\n` magic prefix)*: rejected. The envelope is Bitcoin-specific protocol metadata that has no role in our request-signing contract; including it would make the bytes signed differ from the canonical payload, complicating the layer-4 contract for no benefit.
- *Make Sign / Verify generic over the curve via an interface*: rejected. Constitution III pins secp256k1; an interface would invite future drift. The concrete type matches what `keys.DeriveClientKey` returns.

---

## R-002a — `RawMessage` escape hatch type

**Decision**: Define `type RawMessage []byte` as a public named-byte-slice. The recursive encoder special-cases values whose static type is `RawMessage` (detected via `reflect.Type` comparison): the bytes are emitted verbatim, with no quoting and no validation. The caller is responsible for guaranteeing the bytes are themselves canonical-JSON-shaped. A nil or zero-length `RawMessage` emits `null` (zero-length is treated as the JSON `null` literal — embedding empty bytes verbatim would produce malformed JSON).

The encoder detects `RawMessage` via `reflect.Type` equality with a package-private sentinel `rawMessageType = reflect.TypeOf(RawMessage(nil))`, populated by a single `var` declaration in `canonical.go`. The check runs BEFORE the `reflect.Slice` switch so a `RawMessage` (which is structurally `[]byte`) takes precedence over the generic byte-slice rejection.

**Rationale**: Spec FR-022 + Edge Case 4 require "a `RawMessage`-style escape hatch for embedding already-canonical bytes verbatim". Use cases: a server that holds pre-canonicalised request templates and only fills in a few fields; a client that builds a payload incrementally from already-signed sub-payloads. The escape hatch lets the consumer avoid double-canonicalisation and the cost of re-walking already-canonical bytes.

The `nil`-or-empty-becomes-`null` convention mirrors stdlib's `json.RawMessage` MarshalJSON behaviour: `var rm json.RawMessage; json.Marshal(rm)` returns `null`. Consumers porting from stdlib see consistent semantics.

**Alternatives considered**:
- *Reject empty `RawMessage` as `ErrCanonicalUnsupported`*: rejected. Surprising for consumers who declare `var rm RawMessage` and pass it through without explicitly setting bytes.
- *Use a struct wrapper (`type RawMessage struct{ Bytes []byte }`)*: rejected. The named-byte-slice form is idiomatic Go (`json.RawMessage` is the precedent) and avoids an unnecessary indirection.
- *Validate the bytes are themselves canonical-JSON-shaped before embedding*: rejected. Validating canonicalness would require recursive parsing of the embedded bytes, which defeats the purpose of the escape hatch (the consumer is asserting "these bytes are canonical; trust me"). If the consumer is wrong, signature verification fails on the recipient side and the consumer's test catches it.

---

## R-003 — NonceCache: sync.Map with LoadOrStore + CompareAndSwap

**Decision**: The cache is a struct embedding `sync.Map` with the value type `time.Time` (the absolute expiry). `Add` proceeds in three deterministic phases:

```go
func (c *nonceCache) Add(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
    if err := ctx.Err(); err != nil {
        return false, err
    }
    if l := len(nonce); l < 8 || l > 128 {
        return false, ErrNonceEncoding
    }
    if ttl <= 0 {
        return false, ErrNonceTTLInvalid
    }
    now := nowFn()
    expiry := now.Add(ttl)
    actual, loaded := c.entries.LoadOrStore(nonce, expiry)
    if !loaded {
        return true, nil
    }
    existing, ok := actual.(time.Time)
    if !ok || !now.After(existing) {
        return false, ErrNonceReplay
    }
    if c.entries.CompareAndSwap(nonce, existing, expiry) {
        return true, nil
    }
    return false, ErrNonceReplay
}
```

Phase 1 (cancellation): `ctx.Err()` early-out. Pre-cancellation returns the context's error verbatim; the package does not wrap.

Phase 2 (encoding gate): nonce length check (8 ≤ len ≤ 128) per FR-021. The check runs BEFORE any cache lookup so an encoding rejection is `ErrNonceEncoding`, not `ErrNonceReplay` (FR-021's anti-collapse requirement). TTL positivity check (`ttl > 0`) per FR-012; `ttl ≤ 0` returns `ErrNonceTTLInvalid` BEFORE any cache state is touched.

Phase 3 (atomic insert): `LoadOrStore` is the load-bearing primitive — it is a single sync.Map operation that either stores-fresh or loads-existing atomically. On store-fresh (`loaded == false`), the call returns "first seen" without further state inspection. On load-existing, the loaded value is checked for expiry; if expired AND a CompareAndSwap succeeds (the loser of the race becomes the replay), the call returns "first seen". If CompareAndSwap fails (another caller raced ahead), the caller is treated as a replay — the strict reading of "exactly one wins" semantics where the *first* CAS-winner is the first-seen and everyone else (including those who would have CAS-won had they raced earlier) is a replay.

The expired-but-not-yet-swept reuse path is the spec's Edge Case 3 ("a nonce presented exactly at the moment its TTL expires is treated consistently"). The CAS-based handling means the cache returns one consistent answer per call: the single CAS-winner is "first seen"; everyone else is "already seen". The caller never sees a result inconsistent with the cache state at the moment of the call.

**Rationale**: `sync.Map.LoadOrStore` is the stdlib's canonical "atomic insert if absent, return existing if present" primitive. It satisfies FR-008 (exactly-one winner under N-goroutine concurrent insertion) without an explicit lock. The race-detector exercise (`TestNonceCache_ConcurrentAdd`) launches N=128 goroutines on a shared nonce and asserts exactly one observes `firstSeen=true`; under `-race`, the test asserts no data races are reported.

`sync.Map.CompareAndSwap` (added in Go 1.20) is the stdlib's atomic CAS primitive on the same map — using it for the expired-reuse path keeps the entire operation lock-free.

The TTL-validity gate (`ttl > 0`) is the only structural validation Add performs beyond the length check. FR-012 requires "the supplied window MUST be a positive duration"; a non-positive TTL is a programmer error from the consumer and surfaces as `ErrNonceTTLInvalid` BEFORE any state mutation. The package does NOT impose a hard upper bound on TTL — the consumer (SDD-12) supplies its own per-call TTL aligned with `docs/SECURITY.md` Layer 4's documented 60s default.

**Alternatives considered**:
- *Use a `sync.Mutex` + `map[string]time.Time`*: rejected. A coarse-grained mutex serialises every Add and serialises every sweep-vs-Add interleaving — measurable contention under load. `sync.Map`'s lock-free fast path is ~10× faster on the read-mostly common case.
- *Use a per-shard mutex (`sync.Map[string]struct{m sync.Mutex; t time.Time}`)*: rejected. The complexity is unjustified when `sync.Map` already provides the atomicity we need.
- *Use a third-party LRU cache (`github.com/hashicorp/golang-lru`)*: rejected. New direct dependency for a primitive the stdlib already provides; FR-020 explicitly says no capacity cap, which most LRU caches enforce by construction.
- *Treat the expired-reuse path as a fresh insert without CAS (skip the `CompareAndSwap` race)*: rejected. Without CAS, two callers presenting the same expired nonce simultaneously could both observe `firstSeen=true`, breaking FR-008. The CAS makes the loser the replay and preserves "exactly one winner".
- *Treat the expired-reuse path as always-replay (never re-admit an expired nonce)*: rejected. FR-006's "forgets the nonce after the TTL elapses" + Edge Case 3 imply the expired nonce is reusable. Always-replay would gradually accumulate dead entries and violate FR-006.

---

## R-004 — NonceCache.Run: synchronous ticker loop owned by the caller

**Decision**: `Run(ctx context.Context)` is **synchronous** — it blocks on a `time.Ticker` inside a `for { select }` loop and returns when `ctx.Done()` fires:

```go
func (c *nonceCache) Run(ctx context.Context) {
    t := time.NewTicker(c.sweepInterval)
    defer t.Stop()
    log := slog.Default()
    for {
        select {
        case <-ctx.Done():
            log.Info("hush/transport/sign: nonce cache sweep stopped",
                slog.String("reason", ctx.Err().Error()))
            return
        case <-t.C:
            c.sweep()
        }
    }
}
```

The caller invokes Run inside its own goroutine: `go cache.Run(ctx)`. The package never spawns a goroutine on the caller's behalf; the goroutine that runs the sweep IS the caller's goroutine, with explicit ownership and an explicit cancellation path through `ctx`. Cancellation latency is bounded by the sweep tick interval (≤30 s by default) — when ctx is cancelled, the next iteration of the select notices and exits.

The sweep itself is a single `sync.Map.Range` over the entries:

```go
func (c *nonceCache) sweep() {
    now := nowFn()
    c.entries.Range(func(k, v any) bool {
        if expiry, ok := v.(time.Time); ok && now.After(expiry) {
            c.entries.CompareAndDelete(k, expiry)
        }
        return true
    })
}
```

`CompareAndDelete` (Go 1.20+) ensures we only remove the entry we observed; a concurrent re-store of the same key with a later expiry is preserved. This is the corner case where a nonce's TTL elapsed at the moment a new Add for the same nonce raced in: the sweep removes the *old* entry (matching the observed expiry) and the new entry survives.

The sweep interval is a struct field `c.sweepInterval` defaulting to 30 s in `NewNonceCache`. Tests construct a cache via a test-only helper that passes a shorter interval (e.g., 10 ms) so the lifecycle test (`TestNonceCache_RunStopsOnContextCancel`) completes in under a second.

**Rationale**: Constitution IX is unambiguous: "every goroutine has a clear owner, an explicit cancellation path (context), and a documented termination condition. No fire-and-forget goroutines." Run-as-synchronous with caller-owned `go cache.Run(ctx)` invocation makes the ownership explicit at the call site (the caller's `go` statement) and the cancellation path explicit in the function signature (`ctx context.Context`). This pattern is the same one `errgroup`, `oklog/run`, and `tomb` use — the goroutine is owned by the caller and the function blocks until told to stop.

The single "stopped" log line on exit is the chunk contract's literal requirement. The line carries `reason=ctx.Err().Error()` — `context.Canceled` for explicit cancellation, `context.DeadlineExceeded` for timeout. No other data is logged (no nonce count, no per-key data, no goroutine-id) — Constitution X "no nonces in logs".

**Alternatives considered**:
- *Run spawns its own goroutine and returns immediately*: rejected. The caller cannot wait for sweep termination on its own; cancellation behaviour is split across two goroutines (the caller's and the package's), and the caller must coordinate via a separate `Done()` channel — extra surface area for no benefit. The synchronous design is simpler.
- *Run accepts an explicit `done chan struct{}` parameter*: rejected. `context.Context` already provides cancellation semantics; a separate channel duplicates the surface and complicates the API.
- *Sweep on every Add call (no background goroutine)*: rejected. Sweep-on-Add adds O(N) work to every Add, ruining the sub-µs per-call performance goal. The 30s tick is the right granularity for a request-rate-limited workload.
- *Use a sync.Pool to amortise per-sweep allocations*: rejected. `sync.Map.Range` allocates nothing; the only allocation is the `now` `time.Time` value (stack-allocated). Pooling is unnecessary.
- *Run logs a "started" line on entry as well*: rejected. The chunk contract specifies a "stopped" line only; "started" would be operationally noisy and offers no diagnostic value (the caller's `go` statement is the start).

---

## R-005 — IsFreshTimestamp: pure function with injectable clock

**Decision**: Implement as:

```go
var nowFn = time.Now //nolint:gochecknoglobals // test-swappable clock per Constitution IX

func IsFreshTimestamp(ts time.Time, skew time.Duration) bool {
    if skew <= 0 {
        return false
    }
    delta := nowFn().Sub(ts)
    if delta < 0 {
        delta = -delta
    }
    return delta <= skew
}
```

`nowFn` is a package-private `var` set to `time.Now` at package load. A test-only helper in `_test.go` files captures the original and substitutes a frozen clock:

```go
// testutil_test.go (test-only — ignored by production builds)
func setClockForTest(t *testing.T, fixed time.Time) {
    t.Helper()
    prev := nowFn
    nowFn = func() time.Time { return fixed }
    t.Cleanup(func() { nowFn = prev })
}
```

The boundary semantics (Edge Case 2) are: `delta == skew` is **accepted** (`delta <= skew` is `true` at the boundary). A timestamp exactly at the boundary is fresh; one nanosecond outside is stale. This is the "always-accepted" boundary of Edge Case 2's two consistent options; tests assert it deterministically.

A non-positive `skew` returns `false` unconditionally — a zero or negative skew is a programmer error from the consumer (FR-012 "the package MUST validate that the supplied window is a positive duration"). The function's caller (SDD-12) supplies a positive `skew` matching `docs/SECURITY.md` Layer 4's documented ±30 s default.

The function returns `bool`, not `error`. The consumer (SDD-12 verify middleware) maps `false` to `ErrTimestampStale`; the primitive itself does not know whether the timestamp came from a signed request or a clock-sync probe — the failure-mode wrapping happens at the verification entry point, not inside this primitive. (This is the same separation-of-primitive-from-contract pattern SDD-06's `paths.go` helpers use.)

**Rationale**: A pure function with an injectable clock is the simplest deterministic-test primitive. The `nowFn` indirection is constitutionally-aligned: `var Err...` declarations are sentinel-class and `nowFn` is structurally similar (set-once at package load, mutated only by test helpers via a `_test.go`-only seam). The `//nolint:gochecknoglobals` annotation cites this rationale inline.

The bool-returning shape (instead of error-returning) matches the spec's User Story 6 distinction: each replay-defence failure is a distinct sentinel surfaced by the *verification orchestrator*, not by the freshness primitive. SDD-12 will write:

```go
if !sign.IsFreshTimestamp(ts, skew) {
    return sign.ErrTimestampStale
}
```

— the sentinel-wrapping happens once, at the caller, and the primitive itself is a clean predicate.

**Alternatives considered**:
- *Return `error` directly from IsFreshTimestamp*: rejected. The primitive's job is to answer "is this fresh?" — a yes/no question. Wrapping yes/no in an error type forces the caller to write a useless `if err != nil` for every "yes" case.
- *Accept a `clock Clock` interface as a parameter*: rejected. Threading a clock through every callsite is verbose; the package-private `nowFn` with test-swap helper is the idiomatic Go pattern (used by stdlib `time.AfterFunc` test seams).
- *Make `nowFn` exported for consumers to override*: rejected. The clock is a test-time concern, not a runtime configuration. Constitution IX forbids mutable-public globals.
- *Use `monotonic time` arithmetic (subtract two `time.Time` values directly)*: NOT rejected — that IS what the implementation does. Go's `time.Time.Sub` returns a `time.Duration`; the implementation uses it. Documented for clarity.

---

## R-006 — Sentinel error catalogue and wrap relationships

**Decision**: The package exports six sentinels (all `var Err... = errors.New("hush/transport/sign: ...")`); inline `//nolint:gochecknoglobals` comments cite the constitutional sentinel-class precedent.

| Sentinel | Wraps | Triggered by |
|----------|-------|-------------|
| `ErrSignatureInvalid` | (none) | Any signature failure: wrong public key, tampered payload, malformed DER, wrong-length sig, panic-recovered shape failure. Per FR-005, **all** signature failures collapse into this single sentinel; the recipient cannot distinguish "wrong key" from "tampered payload" by error type. |
| `ErrNonceReplay` | (none) | The nonce was already accepted within its TTL. Returned by `NonceCache.Add`. |
| `ErrTimestampStale` | (none) | The timestamp is outside the freshness window in either direction. The package itself does not return this — `IsFreshTimestamp` returns `bool` and the consumer (SDD-12) maps `false` to this sentinel. The sentinel is exported so consumers can `errors.Is`-match it without redefining their own. |
| `ErrCanonicalUnsupported` | (none) | A value cannot be canonicalised: non-finite float (`NaN`, `+Inf`, `-Inf`), non-string-keyed map, function value, channel value, complex number, raw `[]byte` outside `RawMessage`. Per FR-002 + FR-023, both rejection categories share the same sentinel (the spec calls this out as "the same sentinel category as the non-finite-float rejection"). |
| `ErrNonceEncoding` | (none) | The nonce is empty or its length is outside `[8, 128]`. Returned by `NonceCache.Add` BEFORE any cache lookup so an encoding rejection never collapses into `ErrNonceReplay` (FR-021). |
| `ErrNonceTTLInvalid` | (none) | A non-positive `ttl` was passed to `NonceCache.Add`. Per FR-012 + FR-007, a programmer error from the consumer; surfaced separately from `ErrNonceEncoding` because the failure category is distinct (TTL shape vs nonce shape). |

There are **no wrap relationships** between sentinels in this package — unlike SDD-06 where `ErrListenLoopback` wrapped `ErrTailscaleBindRequired`, the SDD-08 sentinels are six independent failure categories. Each `errors.Is(err, ErrXxx)` matches one and only one category; consumers do not need an umbrella sentinel because the categories do not naturally cluster.

The wrapping inside the package is at the *operation* level: `Verify` returns `fmt.Errorf("hush/transport/sign: verify: %w", ErrSignatureInvalid)` so the message contextualises the operation; the sentinel identity is preserved via `%w`. Tests use `errors.Is`, never string-comparison.

**Rationale**: The spec's FR-015 demands "three distinct, comparable error values" for the three replay-defence categories (signature, nonce-replay, timestamp-stale); the catalogue above satisfies that with three named sentinels. The three additional sentinels (`ErrCanonicalUnsupported`, `ErrNonceEncoding`, `ErrNonceTTLInvalid`) cover the spec's other typed-rejection categories (FR-002, FR-021, FR-012) — the spec does not collapse those into the three replay-defence categories.

The "no wrap relationships" decision is deliberate: SDD-06 wrapped `ErrListenLoopback` etc. into `ErrTailscaleBindRequired` because those three were genuinely sub-categories of one umbrella concern (Tailscale-only). The SDD-08 sentinels are not sub-categories of anything — replay vs forgery vs encoding-shape are independent failure modes.

**Alternatives considered**:
- *One umbrella `ErrInvalidRequest` for everything*: rejected. Spec FR-015 demands "distinct, named" per category — one sentinel cannot satisfy that.
- *Wrap all six under a top-level `ErrTransportSignError`*: rejected. The wrap would be cosmetic (no operational use case); each consumer cares about the specific category, not the umbrella.
- *Collapse `ErrNonceTTLInvalid` into `ErrNonceEncoding`*: rejected. The two cover distinct failure modes (TTL shape vs nonce shape); a programmer-error-on-TTL is debugged differently from an encoding-rejection-on-nonce.
- *Use error types instead of sentinels (e.g., `type SignatureError struct{...}`)*: deferred. Sentinels are simpler for the catalogue size at hand (six categories); if a future chunk needs richer error data (e.g., specific DER-decode-failure detail), it can promote specific sentinels to types. SDD-08's contract is sentinel-only.

---

## R-007 — Fuzz target shape

**Decision**: Add `verify_fuzz_test.go` with `FuzzVerifyRequest(f *testing.F)`:

```go
func FuzzVerifyRequest(f *testing.F) {
    pub := generateFuzzKey(f).Public().(*ecdsa.PublicKey)
    seedFuzzCorpus(f) // adds Add() entries for each seed file in testdata/fuzz/FuzzVerifyRequest/
    f.Fuzz(func(t *testing.T, payload, sig []byte) {
        err := sign.Verify(t.Context(), pub, payload, sig)
        if err == nil {
            return // succeeded against a random key — fine, the contract is "no panic"
        }
        if !errors.Is(err, sign.ErrSignatureInvalid) && !errors.Is(err, context.Canceled) {
            t.Errorf("Verify returned non-sentinel error type %T: %v", err, err)
        }
    })
}
```

The fuzz function uses a **fixed test-derived public key** (generated once at fuzz-target setup via `generateFuzzKey`, which returns a deterministic ecdsa key for fuzz reproducibility). The fuzz body asserts: (a) Verify never panics, (b) every error is `ErrSignatureInvalid` (or `context.Canceled` if the fuzz harness cancels its context — defensive), (c) no nonce / signature byte / payload byte appears in any error message (asserted by the test setup capturing the error string and `strings.Contains`-ing the input bytes).

The seed corpus (in `testdata/fuzz/FuzzVerifyRequest/`) ships four files matching the spec edge cases:

1. `empty-payload-empty-sig` — both inputs zero-byte. The Verify path's earliest exits.
2. `valid-shape-wrong-key` — payload = `"replay-defence-test"`, sig = a real ECDSA signature produced by a *different* key. Exercises the sig-decode-success-but-verify-fail path.
3. `truncated-der` — payload = `"x"`, sig = the first 16 bytes of a real ECDSA signature (truncated DER). Exercises stdlib's `VerifyASN1` parser on malformed DER.
4. `garbage-bytes` — payload = `\x00\x01\x02...\xff` (256 random bytes), sig = `\xff\xff...\xff` (72 random bytes). Random fuzz baseline.

CI gate (per the implement-phase release-step list): `go test -fuzz=FuzzVerifyRequest -fuzztime=60s ./internal/transport/sign/` runs clean — no panic, no new corpus entries representing crashes (any new entry goes into `testdata/fuzz/FuzzVerifyRequest/` and would trigger a CI follow-up).

**Rationale**: The chunk contract names FuzzVerifyRequest and the 60 s gate. Constitution VIII lists "Request signature payload parsing" as fuzz target #4 — same category, same package family. The "no panic, every error typed" invariant is the load-bearing security property: a panic in `Verify` would crash an HTTP handler, an attacker who chose hostile bytes could deny service via crash. The seed corpus accelerates fuzz-coverage convergence — the four seeds cover all the major Verify-path branches (zero-shape, valid-shape-fail, malformed-DER, random) so 60 s of fuzzing actually exercises the verification logic, not just "is this DER".

**Alternatives considered**:
- *Skip the seed corpus, rely on random-byte coverage alone*: rejected. 60 s of random bytes rarely hits the valid-shape-wrong-key path (most random bytes fail at "is this DER" early-out). Seeds are cheap to write and dramatically improve coverage.
- *Run for 5 minutes instead of 60 s*: deferred. The chunk contract says 60 s; CI cost is a real constraint. A future hardening pass can lengthen if a defect class emerges.
- *Fuzz `CanonicalJSON` separately*: deferred. The chunk contract names FuzzVerifyRequest as the single 60 s target. CanonicalJSON is exercised through the existing `FuzzVerifyRequest` (every random payload goes through it transitively if SDD-12's verify-with-canonicalisation path is the harness) AND through `TestCanonical_*` table-driven tests with deliberate edge inputs. A dedicated `FuzzCanonical` is a future hardening pass.
- *Fuzz the nonce cache*: deferred. The cache's surface is small (8-byte-to-128-byte string + positive duration) and the property under test (exactly-one-winner) is exercised by `TestNonceCache_ConcurrentAdd` under `-race` rather than by fuzz. A future hardening pass can add a fuzz target if a defect class emerges.

---

## R-008 — Test corpus discipline (canonical encoder shapes)

**Decision**: The 10 known shapes for `TestCanonical_SortsAtAllDepths` are committed as inline test-table entries (NOT as `testdata/` files) because they are programmatic Go values, not on-the-wire bytes:

1. **Flat map**: `map[string]any{"b": 1, "a": 2}` — top-level sort.
2. **Nested map**: `map[string]any{"outer": map[string]any{"y": 2, "x": 1}}` — inner sort.
3. **Three-level nested map**: `map[string]any{"a": map[string]any{"b": map[string]any{"d": 4, "c": 3}}}` — deep sort.
4. **Struct with json tags**: `struct{ B int `json:"b"`; A int `json:"a"` }{B: 1, A: 2}` — alphabetised by tag.
5. **Struct without json tags**: `struct{ B int; A int }{B: 1, A: 2}` — alphabetised by field name.
6. **Mixed struct + map field**: `struct{ Z map[string]any; A int }{Z: map[string]any{"q": 9}, A: 1}` — outer sort + inner sort.
7. **Embedded RawMessage**: `map[string]any{"a": RawMessage([]byte("[1,2,3]"))}` — verbatim insertion of pre-canonical bytes.
8. **Slice with mixed elements**: `[]any{1, "two", true, nil, 3.14}` — order preserved, types correct.
9. **Boolean + null + integer**: `map[string]any{"truthy": true, "falsy": false, "absent": nil, "n": 42}` — primitive emission.
10. **Unicode-edge string**: `map[string]any{"key": "  emoji-✓-Latin-1-é"}` — UTF-8 escape correctness.

Each entry is paired with an expected-bytes literal computed by hand and asserted via `bytes.Equal`. Drift between the input shape and the expected bytes is a test-time regression.

The struct-tag tests (4, 5) are the load-bearing gotcha exercises the chunk contract calls out — they assert the encoder's behaviour matches the spec (alphabetical) NOT stdlib's behaviour (declaration order). A future maintainer who accidentally swaps to `json.Marshal` would see these tests fail.

The NaN/Inf rejection tests use `math.NaN()`, `math.Inf(1)`, and `math.Inf(-1)` as input values inside flat maps; each must return `ErrCanonicalUnsupported` and produce no bytes.

The unsupported-type rejection tests use `chan int`, `func(){}`, `complex(1, 2)`, and `map[int]string{}` — each returns `ErrCanonicalUnsupported`.

**Rationale**: Inline Go-value test tables are the right form for canonicalisation tests because the input is a Go value (driven by reflection), not a serialised file. File-based fixtures would require a parser to re-create the Go value, which would defeat the purpose. The 10 shapes cover all the spec's User Story 3 acceptance scenarios (top-level sort, nested sort, embedded raw, NaN/Inf rejection) plus the chunk contract's struct-vs-map gotcha pair plus a unicode-edge case.

**Alternatives considered**:
- *Generate shapes dynamically from a property-based test (e.g., go-quick's `quick.Check`)*: deferred. PBT is a future hardening pass; the 10 known shapes give deterministic coverage of the documented contract today, and PBT can be added without changing the contract.
- *Serialise the inputs to JSON via stdlib first and round-trip*: rejected. Stdlib's serialisation is exactly the thing we are NOT using; round-tripping through it would defeat the determinism contract.

---

## R-009 — Composition recipe (replay-defence ordering)

**Decision**: The package does NOT export a single `VerifyRequest(ctx, pub, payload, sig, nonce, ts)` orchestrator. The four primitives are kept independent so consumers can compose them in the order their layer demands. The canonical ordering for SDD-12's `/claim` and `/revoke` middleware is:

```go
// 1. Canonical-bytes recompute (consumer-side; payload was already canonicalised by the client).
canonical, err := sign.CanonicalJSON(req)
if err != nil {
    return sign.ErrCanonicalUnsupported // or wrap as needed
}

// 2. Signature first — an attacker who forged a sig MUST NOT consume the operator's nonce budget.
if err := sign.Verify(ctx, pub, canonical, req.Sig); err != nil {
    return err // ErrSignatureInvalid
}

// 3. Timestamp second — a stale-but-validly-signed request MUST NOT burn the nonce.
if !sign.IsFreshTimestamp(req.Timestamp, skew) {
    return sign.ErrTimestampStale
}

// 4. Nonce LAST — only after both signature and timestamp pass do we commit to the cache.
firstSeen, err := nonceCache.Add(ctx, req.Nonce, ttl)
if err != nil {
    return err // ErrNonceReplay or ErrNonceEncoding or ErrNonceTTLInvalid
}
if !firstSeen {
    return sign.ErrNonceReplay
}
return nil
```

The recipe is enshrined in [contracts/api.md](./contracts/api.md#composition-recipe) and tested by an integration test scheduled for SDD-12 (the integration is out of scope for SDD-08 but the recipe is documented as a load-bearing layer-4 contract).

**Rationale**: Spec FR-013 + FR-014 explicitly require the ordering: an attacker MUST NOT be able to "burn" a nonce for an honest client by replaying with a forged signature OR a stale timestamp. The natural translation is: do NOT enrol the nonce into the cache until both signature verification and timestamp freshness have passed. The ordering above satisfies both FRs.

The package keeps the primitives independent (no `VerifyRequest` orchestrator) for two reasons:
1. **Different consumers need different primitives.** SDD-12 (server) needs all four; SDD-16 (client) needs only `CanonicalJSON` + `Sign` (the client signs but does not verify its own request); a future audit-log replay-validator might need only `Verify` + `CanonicalJSON`.
2. **Constitutional separation of primitive from contract.** The composition recipe is a layer-4 *contract*; the primitives are *building blocks*. Bundling them into one function would couple the building blocks to a single layer-4 ordering and make alternative orderings (e.g., a future audit-log validator that wants timestamp-first) harder to express.

**Alternatives considered**:
- *Export a `VerifyRequest(...)` orchestrator that bundles all four*: rejected. See above. Consumers can write the four-line recipe themselves; bundling forces a single ordering.
- *Reverse the timestamp / signature order*: rejected. Signature-first is faster on the common-case attack (forged signature → cheap reject); timestamp-first would reject stale-but-valid replays before exercising signature verification, which is a more expensive primitive. Common-case-first is the right ordering.
- *Run signature and timestamp in parallel*: rejected. Both are sub-ms; the parallelism gain is negligible; the sequential ordering is simpler and the spec's FR-013/FR-014 demand sequential semantics ("MUST NOT enrol the nonce in the cache" — implies the nonce-enrolment step does not start until earlier checks pass).

---

## R-010 — Nonce TTL semantics (per-call, no package default)

**Decision**: `NonceCache.Add(ctx, nonce, ttl)` accepts `ttl time.Duration` per call. The package imposes no default TTL — the consumer (SDD-12) supplies its own per-call value aligned with `docs/SECURITY.md` Layer 4's documented 60 s default and SDD-06's `DefaultNonceTTL = 60 * time.Second`. The TTL is the absolute window during which the nonce is treated as "already seen"; expiry is computed at Add time as `nowFn().Add(ttl)`.

**Rationale**: Per-call TTL is more flexible than a cache-construction-time TTL — it lets a single cache instance back multiple endpoint families (e.g., `/claim` with 60 s vs. `/revoke` with 5 s). The chunk contract names "60s window" but the spec's Assumptions section explicitly defers TTL choice to the consumer; the package follows the spec.

**Alternatives considered**:
- *Per-cache TTL set in `NewNonceCache(ttl time.Duration)`*: rejected. Less flexible; couples cache lifetime to a single TTL.
- *Per-package default (e.g., `DefaultNonceTTL = 60s`)*: deferred. SDD-06 already exports `DefaultNonceTTL`; SDD-08 does not duplicate it.

---

## R-011 — Test-only clock-swap helper discipline

**Decision**: The `setClockForTest(t *testing.T, fixed time.Time)` helper lives in `timestamp_test.go` (or a shared `testutil_test.go` for cross-test use). It captures the original `nowFn`, substitutes a frozen-clock function, and registers `t.Cleanup` to restore. The helper is `_test.go`-only — Go's build system excludes `_test.go` files from non-test builds — so production code paths cannot mutate the clock.

A second helper, `advanceClockForTest(t, by time.Duration)`, lets a test progressively advance the frozen clock to exercise sweep behaviour (TTL elapses, sweep removes entry, fresh-timestamp boundary).

The race between sweep and Add is exercised by `TestNonceCache_RunStopsOnContextCancel` + `TestNonce_AddDuringSweep` — these tests use a tiny `sweepInterval` (10 ms) and assert the cache state is consistent throughout.

**Rationale**: Test-only clock seams are the standard Go idiom for time-dependent code (see stdlib `time.AfterFunc` test patterns). Constitution IX permits this as a `_test.go`-gated seam, same constitutional class as the `RedactPatterns` reset helper in SDD-05's `internal/logging`.

**Alternatives considered**:
- *Pass `clock Clock` interface through every call site*: rejected. Verbose; the `nowFn` indirection is invisible to consumers and idiomatic.
- *Use `github.com/jonboulle/clockwork`*: rejected. New direct dependency for a primitive the project can solve in 5 lines of test helper.

---

## R-012 — Logging & redaction

**Decision**: The package emits exactly **one** log line in production: the "nonce cache sweep stopped" line on `Run` exit. The line uses `slog.Default()` and carries a single attribute `slog.String("reason", ctx.Err().Error())`. No nonce, no count, no per-key data.

Errors returned from the package's exported functions identify the failure category in the sentinel identity. The wrapping message names the *operation* (`verify`, `sign`, `nonce-cache add`) but never the payload bytes, the signature bytes, the nonce, or the key. A test (`TestVerify_NoLeakOnFuzzInput`) captures `slog.NewJSONHandler` output across 1000 random failed verifications and asserts the nonce / signature / payload bytes never appear in the log buffer.

**Rationale**: Constitution X mandates "no nonce values in logs" and "no secret values in errors". The package follows the strictest reading: errors carry category, not content; logs carry lifecycle, not data. The fuzz-pressure leak test makes the invariant verifiable.

**Alternatives considered**:
- *Log every nonce-replay rejection at debug level*: rejected. Constitution X forbids any nonce in logs, regardless of level.
- *Log a per-sweep metric (entries removed)*: rejected. Metrics are FR-024-forbidden; a per-sweep count would be informational at best and operationally noisy.
