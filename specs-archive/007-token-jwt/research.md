# Phase 0 Research: `internal/token`

**Feature**: 007-token-jwt — ES256K JWT issuance, validation, store, and revocation
**Date**: 2026-04-29

This document resolves every technical decision the plan depends on.
Each entry follows the **Decision / Rationale / Alternatives considered**
format. There are no remaining `NEEDS CLARIFICATION` markers in the
spec; the chunk contract pre-fixed the load-bearing choices (ES256K
signing, two named session shapes, seven distinct sentinels), and the
spec's Assumptions section pinned the consumer wiring (signing key
from BIP32, ephemeral pubkey from `/claim`).

---

## R-001 — JWT library: `github.com/golang-jwt/jwt/v5`

**Decision**: Add `github.com/golang-jwt/jwt/v5` as a NEW direct
dependency in `go.mod`. The package uses three of its surfaces:

1. `jwt.RegisterSigningMethod(name string, factory func() jwt.SigningMethod)`
   to wire the custom ES256K signing method (one-shot, gated by
   `sync.Once`).
2. `jwt.NewWithClaims(method, claims)` + `Token.SignedString(key)` for
   the issuance compact-form encoder (header.payload.signature, both
   header and payload base64url-encoded, signature DER-bytes
   base64url-encoded, joined with `.`).
3. `jwt.ParseWithClaims(encoded, claims, keyfunc)` for the validate
   compact-form decoder + signature verification +
   `RegisteredClaims.Valid()` time-window check (which produces
   `jwt.ErrTokenExpired` for an expired token; the package maps this
   to its own `ErrTokenExpired`).

The `Claims` struct embeds `jwt.RegisteredClaims` (which provides
`Issuer`, `IssuedAt`, `ExpiresAt`, `ID` (= JTI) as
`jwt.NumericDate`-typed fields) and adds the six hush-specific
fields as named JSON tags.

**Rationale**: Constitution XI mandates the trusted-sources hierarchy
(stdlib first, then sigil baseline, then bsv-blockchain GitHub
organisation, then wider ecosystem). The walk for "JWT library":

1. **Stdlib**: no JWT support. The Go stdlib provides
   `crypto/ecdsa`, `crypto/sha256`, `encoding/base64`,
   `encoding/json` — i.e. the primitives a from-scratch JWT
   implementation would need — but does NOT provide a JWT compact-form
   encoder, decoder, or signing-method registry. From-scratch JWT in
   the package would be ~500 LOC of attack-surface (header parser,
   claims walker, signature framing) that has been written and
   battle-tested elsewhere.
2. **Sigil baseline (`github.com/mrz1836/sigil`)**: not currently a
   dependency, and `sigil`'s scope is general-purpose toolkit (BIP32
   helpers, key-management primitives, secure-bytes containers); a
   JWT primitive is out of `sigil`'s charter.
3. **bsv-blockchain GitHub organisation**: scope is
   Bitcoin/blockchain primitives; no JWT support.
4. **Wider ecosystem**: `github.com/golang-jwt/jwt/v5` is the canonical
   Go JWT library. **Maintained successor** to the deprecated
   `dgrijalva/jwt-go` (which has known CVEs that golang-jwt patched
   at the v4 fork; v5 is the current maintained line). MIT license,
   permissively licensed. **Zero transitive dependencies**
   (golang-jwt/jwt/v5 imports only stdlib). Active maintainer
   (≥monthly releases as of 2026-04). 5k+ GitHub stars; widely
   adopted across the Go ecosystem.

The walk bottoms out at the wider ecosystem because no upstream tier
supplies a JWT primitive. The constitutional requirement is
"justification" (Constitution XI: "Every NEW direct dependency requires
a written justification in the PR covering: maintainer activity,
supply-chain provenance, transitive dependency footprint, and why no
stdlib option suffices"). This research entry IS that justification;
the implement-commit PR description will quote it verbatim.

The `golang-jwt/jwt/v5` library is a deliberate choice over its v4
predecessor: v5 ships a stricter `RegisteredClaims.Valid()` that returns
typed errors (`jwt.ErrTokenExpired`, `jwt.ErrTokenMalformed`,
`jwt.ErrTokenSignatureInvalid`) which the package maps cleanly to its
own sentinels. v4 returned `*jwt.ValidationError` with a bitfield
(`ValidationErrorExpired | ValidationErrorMalformed | ...`); the
sentinel-mapping code would be more error-prone.

**Alternatives considered**:

- *From-scratch JWT in `internal/token`*: rejected. ~500 LOC of
  attack-surface duplicated for no benefit; the JWT compact form is
  a published spec but the corner cases (header escaping, base64url
  vs. base64, the `cty` content-type field, the typ-vs-alg ordering)
  are subtle. Reusing a maintained library is the safer choice.
- *`github.com/lestrrat-go/jwx`*: rejected. JWX is a more featureful
  library (JWE, JWK rotation, plugin ecosystem) that we don't need.
  Its dependency footprint is also larger (multiple sub-modules,
  `goccy/go-json`, etc.). golang-jwt/jwt/v5 is the minimal-footprint
  choice.
- *`github.com/cristalhq/jwt/v5`*: rejected. cristalhq/jwt is a
  performance-optimised alternative; no clear advantage over
  golang-jwt for our use case (a few JWT operations per second, not
  thousands), and it has a smaller community footprint.
- *`gopkg.in/square/go-jose.v2`*: deprecated (`square/go-jose` is no
  longer maintained; the `go-jose/go-jose` fork is the maintained
  successor but its API is still designed around JWE/JWS rather than
  JWT specifically).
- *Bring `github.com/bitcoinschema/go-bitcoin` for the JWT bits*:
  rejected. The library has Bitcoin-message signing helpers but no
  JWT compact-form encoder. Its ECIES helpers ARE relevant to SDD-09,
  but for SDD-07 we need JWT framing the library does not provide.

---

## R-002 — ES256K signing method: stdlib substitution

**Decision**: The custom `ES256K` `jwt.SigningMethod` implementation
delegates **sign** to stdlib `crypto/ecdsa.SignASN1` and **verify**
to stdlib `crypto/ecdsa.VerifyASN1`, both over `sha256.Sum256(signingInput)`.
The `signingInput` is `base64url(header) + "." + base64url(payload)`,
which the `golang-jwt/jwt/v5` library produces and passes to the
signing method.

```go
type es256kMethod struct{}

func (es256kMethod) Alg() string { return "ES256K" }

func (es256kMethod) Sign(signingInput string, key any) ([]byte, error) {
    priv, ok := key.(*ecdsa.PrivateKey)
    if !ok {
        return nil, jwt.ErrInvalidKeyType
    }
    digest := sha256.Sum256([]byte(signingInput))
    return ecdsa.SignASN1(rand.Reader, priv, digest[:])
}

func (es256kMethod) Verify(signingInput string, sig []byte, key any) error {
    pub, ok := key.(*ecdsa.PublicKey)
    if !ok {
        return jwt.ErrInvalidKeyType
    }
    digest := sha256.Sum256([]byte(signingInput))
    if !ecdsa.VerifyASN1(pub, digest[:], sig) {
        return jwt.ErrTokenSignatureInvalid
    }
    return nil
}
```

Registration is one-shot via `sync.Once`:

```go
var registerOnce sync.Once

func Register() {
    registerOnce.Do(func() {
        jwt.RegisterSigningMethod("ES256K", func() jwt.SigningMethod {
            return es256kMethod{}
        })
    })
}
```

`Issue` and `Validate` both call `Register()` as their first
non-context statement; the chunk contract names this pattern explicitly
("registered via `jwt.RegisterSigningMethod` ONCE through a `sync.Once`-
gated `Register()` function called by `Issue` / `Validate` (NOT
`init()`)").

**Rationale**: The chunk contract names
`github.com/bitcoinschema/go-bitcoin` for ECDSA sign/verify ("ES256K
signing method delegates to github.com/bitcoinschema/go-bitcoin for
sign/verify (already locked by SDD-01)"). The "already locked by
SDD-01" claim is factually incorrect — SDD-01's research [R-002]
substituted `bitcoinschema/go-bitcoin` for stdlib + decred, and
`go-bitcoin` is NOT in `go.mod`. So delegating to `go-bitcoin` would
require ADDING IT NOW as a new direct dependency, on top of the
already-justified `golang-jwt/jwt/v5` addition (R-001).

The same stdlib-substitution argument SDD-08's R-002 used for ECDSA
Sign/Verify applies here: stdlib `crypto/ecdsa.SignASN1` /
`VerifyASN1` over `SHA-256(signingInput)` is the IETF-standard ECDSA
signature shape (ASN.1 DER-encoded `(r, s)` pair), it is the same
algorithm `bitcoinschema/go-bitcoin` would produce for ECDSA over
secp256k1 + SHA-256, and it adds zero new direct dependencies (the
secp256k1 curve is supplied by `*ecdsa.PrivateKey.Curve`, which the
caller obtains via `keys.DeriveJWTSigningKey` from SDD-01 — already
backed by `decred/dcrd/dcrec/secp256k1/v4`).

This is a **stdlib-correct refinement** in the same pattern as:
- SDD-01's R-002 (replacing `bitcoinschema/go-bitcoin` for BIP32 with
  `decred/dcrd/hdkeychain/v3` and `decred/dcrd/dcrec/secp256k1/v4`).
- SDD-08's R-002 (replacing `bitcoinschema/go-bitcoin` for ECDSA
  Sign/Verify with stdlib `crypto/ecdsa.SignASN1` /
  `VerifyASN1`).
- SDD-09's R-002 (replacing `bitcoinschema/go-bitcoin` for ECIES with
  stdlib AES + HMAC + decred secp256k1 ParsePubKey).

Each substitution preserved the spec's behavioural contract while
eliminating a third-party crypto dependency.

Security equivalence:
- **Algorithm**: ES256K is "ECDSA over secp256k1 with SHA-256
  digest" — the IETF JOSE registry (RFC 8812) defines it precisely.
  Stdlib `crypto/ecdsa.SignASN1` produces a DER-encoded `(r, s)` pair;
  `crypto/ecdsa.VerifyASN1` checks the signature constant-time; the
  curve is supplied by `*ecdsa.PrivateKey.Curve` (secp256k1 from the
  decred provider via SDD-01). The wire form is identical to what
  `bitcoinschema/go-bitcoin` would produce for the same algorithm.
- **Entropy**: `crypto/rand.Reader` is the OS CSPRNG. Constitution III
  mandates `crypto/rand` for entropy.
- **Constant-time properties**: `crypto/ecdsa.VerifyASN1` is the
  stdlib's documented constant-time verify. There is no timing-side-channel
  exposure between "wrong key" and "tampered payload" — both surface
  as `jwt.ErrTokenSignatureInvalid` from the library, which the package
  maps to `ErrAlgorithmUnsupported` (the umbrella for every
  signature-class failure per FR-007 #1).

**Alternatives considered**:

- *Add `github.com/bitcoinschema/go-bitcoin/v2` as a new direct
  dependency*: rejected. Even though the chunk contract names it,
  SDD-01's substitution precedent shows the named library is honored
  at the *intent* level. Adding it now would partially un-do that
  substitution and pull in transitive Bitcoin-message-signing
  primitives that SDD-07 does not need (canonical-message preamble,
  varint length encoding, base58 helpers, etc.).
- *Use `crypto/ecdsa.Sign` + manual `(r, s)` packing*: rejected.
  `SignASN1` is the stdlib's recommended shape (Go 1.15+); manually
  packing `(r, s)` into the JOSE-style `r || s` 64-byte fixed-width
  form is what RFC 7518 defines for ES256, but ES256K (RFC 8812)
  inherits the same ECDSA signature framing rules — and the JOSE
  registry's recommendation is the JWS unencoded JSON serialization
  uses ASN.1 DER for ES256K signatures specifically because the
  Bitcoin ecosystem standardised on DER. `golang-jwt/jwt/v5`'s ES256
  method uses fixed-width; for ES256K we use DER (matching the
  Bitcoin convention and the SDD-08 sign/verify primitive). The
  resulting JWS verification is consistent — Validate calls the
  same DER-decoder that Issue's signer produced.
- *Use `crypto/ecdh` for ECDSA*: rejected. `crypto/ecdh` is for ECDH
  key agreement, not ECDSA signing.
- *Use `bsv-blockchain/go-sdk` for ECDSA*: rejected. Same trusted-sources
  walk as R-001 — no advantage over stdlib for the ECDSA primitive.
- *Implement the `Sign` method to take an `io.Reader` for entropy
  injection (test seam)*: deferred. The stdlib's `ecdsa.SignASN1`
  takes an `io.Reader`, which means tests could inject a deterministic
  reader. For SDD-07 we leave this as a future hardening pass — the
  signing-method's `Sign` method receives the key through `golang-jwt`'s
  signature, and weaving a custom RNG through that path is more code
  than the deterministic-JTI seam (R-003) already provides for test
  reproducibility.

---

## R-003 — JTI generation: `crypto/rand` → UUIDv4 (stdlib only)

**Decision**: JTI is a UUIDv4 string generated from 16 bytes of
`crypto/rand.Reader`, with RFC 4122 §4.4 version (byte 6 high nibble
= `0x4`) and variant (byte 8 high two bits = `0b10`) bits set, then
formatted in canonical hyphenated hex form (`xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`).

```go
var randReader io.Reader = rand.Reader

func generateJTI() (string, error) {
    var b [16]byte
    if _, err := io.ReadFull(randReader, b[:]); err != nil {
        return "", err
    }
    b[6] = (b[6] & 0x0f) | 0x40 // version 4
    b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
    return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
        b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
```

The `randReader` package-level seam defaults to `rand.Reader`; tests
swap it (under a per-package mutex) to a deterministic `io.Reader`
backed by `bytes.NewReader([]byte{...})` for reproducible JTIs.

**Rationale**: The chunk contract says "jti generated via `crypto/rand`
→ UUIDv4 (use a deterministic helper in tests via injectable rng)".
Two ways to implement UUIDv4 in Go:

1. **Stdlib-only** (12-line helper above). Pros: zero new dep,
   fully observable, deterministic test seam.
2. **`github.com/google/uuid`** (3-line wrapper around
   `uuid.NewRandom()`). Pros: well-tested library. Cons: new direct
   dependency for a 12-line helper that has zero Go features beyond
   `crypto/rand` and `fmt.Sprintf`.

The stdlib-only path follows the same precedent as SDD-08, SDD-09
(stdlib-correct refinements that avoid new deps when the substitute
is straightforward). UUIDv4 is a published RFC 4122 §4.4 spec that is
trivially correct from `crypto/rand` + 2 byte mutations + a format
string. The package therefore takes the stdlib-only path; this is one
fewer direct dep to justify per Constitution XI.

The `randReader` seam (a package-level `io.Reader` defaulting to
`crypto/rand.Reader`) is the standard Go test pattern for
entropy-source injection. Tests swap it via:

```go
func withDeterministicRand(t *testing.T, src io.Reader, fn func()) {
    t.Helper()
    prev := randReader
    randReader = src
    t.Cleanup(func() { randReader = prev })
    fn()
}
```

Constitution IX bans mutable package globals "beyond bounded
sentinel-class declarations". The `randReader` seam IS a mutable
global, but it's set-once at package load (to `rand.Reader`) and only
swapped from a test-only helper that captures + restores via
`t.Cleanup`. The same pattern is documented in SDD-08's research for
the `nowFn` clock seam. Production code never observes the seam
mutated; the constitutional principle (no caller-observable mutation)
holds.

**Alternatives considered**:

- *`github.com/google/uuid`*: rejected for the new-dep cost. The
  library is fine; we just don't need it for 12 lines of code.
- *Use `*ecdsa.PrivateKey`-derived JTI (e.g., `sha256(privBytes ||
  counter)[:16]`)*: rejected. The signing key is sensitive; mixing it
  into the JTI computation creates a side-channel for key recovery if
  an attacker can observe many JTIs and the counter increments.
- *Use a monotonically-increasing counter*: rejected. JTIs must be
  unguessable per FR-008 (and per the spec's Edge Case "a re-issue
  attempt that draws a new identifier MUST always produce a fresh
  identifier value distinct from any previously issued or revoked
  identifier"). A counter is guessable.
- *Inject the `io.Reader` via `IssueParams.RandReader`*: rejected. The
  package-level seam is simpler and matches SDD-08's `nowFn` pattern.
  Carrying the rand source through `IssueParams` adds API surface
  for a test-only need.

---

## R-004 — IP comparison: `netip.ParseAddr` + `Addr` equality

**Decision**: Validate parses both the recovered `Claims.ClientIP` and
the `requestIP` parameter via `netip.ParseAddr`, returns
`ErrIPMismatch` if either parse fails, and compares the resulting
`netip.Addr` values via `==`.

```go
func compareIPs(claimIP, requestIP string) error {
    claimed, err := netip.ParseAddr(claimIP)
    if err != nil {
        return ErrIPMismatch
    }
    actual, err := netip.ParseAddr(requestIP)
    if err != nil {
        return ErrIPMismatch
    }
    if claimed != actual {
        return ErrIPMismatch
    }
    return nil
}
```

`netip.Addr` is a value type (no pointer fields, no map fields); the
`==` operator compares the underlying 128-bit representation. Two
textual representations of the same address (e.g. `"100.64.0.1"` vs
`"100.064.000.001"`, or IPv6 `"::1"` vs
`"0000:0000:0000:0000:0000:0000:0000:0001"`) parse to byte-equal
`netip.Addr` values, so `==` returns `true` for semantically-equal
addresses regardless of textual form (FR-016).

**Rationale**: The chunk contract says "IP comparison: `netip.ParseAddr`
both sides; compare with `==`". `netip` is the modern stdlib
replacement for `net.IP` (Go 1.18+); it offers value semantics, a
fixed-size 128-bit representation, and a documented canonical form.

Naive byte-for-byte string comparison of `claimIP == requestIP` would
distinguish equivalent representations of the same address — which
violates FR-016 ("two textual representations of the same address
compare equal") and the spec User Story 2 acceptance scenario 2 ("a
token issued for one client IP, when validated against the same IP
expressed in a different but semantically equivalent textual form,
the validator treats the IPs as equal").

`netip.ParseAddr` rejects malformed input; failing to parse either side
is treated as `ErrIPMismatch` (the FR-006 sentinel). The package does
NOT distinguish "claim IP malformed" from "request IP malformed" from
"claim and request IPs unequal" — all three are operational failures
that surface the same recovery action (operator inspects the audit log,
which records BOTH the claim IP (from issue time) and the request IP
(from middleware) — so the audit log preserves the failure shape even
when the error sentinel does not).

**Alternatives considered**:

- *Naive `strings.EqualFold(claimIP, requestIP)`*: rejected per
  FR-016. `strings.EqualFold` does not normalise leading zeros in
  IPv4 or zero-compression in IPv6.
- *`net.ParseIP` + `IP.Equal`*: rejected. `net.ParseIP` returns
  `net.IP` (a 16-byte slice). `IP.Equal` performs the semantic
  comparison correctly, but `netip.Addr` is the modern stdlib type
  with value semantics; `net.IP` is being phased out (the `netip`
  package was added in Go 1.18 specifically to replace `net.IP`'s
  pointer-based design).
- *Treat `netip.ParseAddr` failures as a distinct sentinel
  `ErrIPMalformed`*: rejected. The chunk contract's sentinel
  catalogue locks seven sentinels at SDD-07; adding an eighth would
  require a chunk-contract amendment. Folding malformed-IP into
  `ErrIPMismatch` is the conservative choice.
- *Compare via `netip.Addr.Compare(other) == 0`*: equivalent to `==`
  for non-zero `netip.Addr` values; we use `==` for consistency with
  Go's value-equality idiom.

---

## R-005 — Token store concurrency: `sync.RWMutex` + `map[string]*Token`

**Decision**: The in-memory `Store` implementation is a struct
holding:

```go
type memStore struct {
    mu      sync.RWMutex
    live    map[string]*Token       // jti → record (TTL-bounded)
    revoked map[string]struct{}     // jti → revoked-flag (lifetime-of-store)
    tick    time.Duration           // injectable; default 30s
    nowFn   func() time.Time        // injectable; default time.Now
}
```

Operation lock semantics:

| Operation     | Lock held       | Action                                                     |
|---------------|-----------------|------------------------------------------------------------|
| `Add(t)`      | write           | `revoked[t.JTI]?` → reject with `ErrTokenRevoked`; else `live[t.JTI] = t` |
| `Get(jti)`    | read            | `revoked[jti]?` → reject with `ErrTokenRevoked`; else return `live[jti]` |
| `ConsumeUse`  | write           | `revoked[jti]?` → `ErrTokenRevoked`; `live[jti]?` → `ErrTokenRevoked` (treat unknown jti as revoked from this store's perspective); `t.ExpiresAt <= nowFn()` → `ErrTokenExpired`; `t.SessionType == SessionSupervisor` → return `nil` (no decrement); `t.MaxUses == 0` → `ErrTokenExhausted`; else `t.MaxUses--; return nil` |
| `Revoke(jti)` | write           | `revoked[jti] = struct{}{}`; `delete(live, jti)`; idempotent — revoking an already-revoked or unknown jti is a no-op success (FR-011's lifetime-of-store guarantee) |
| `Cleanup`     | write per-tick  | walk `live` map; remove entries where `t.ExpiresAt <= nowFn()`; **never touches `revoked`** |

The `revoked` set is consulted **before** the live map in every
read/write path. This guarantees the FR-011 invariant: once a JTI is
revoked, no future `Get` or `ConsumeUse` on that JTI can succeed,
even after `Cleanup` reclaims the live record.

`ConsumeUse` taking the **write lock** (rather than a read lock with
atomic decrement) is the load-bearing concurrency choice. With N=100
goroutines decrementing the same JTI whose `MaxUses` is 100, the
write lock serialises the decrements; each goroutine observes a
consistent view of `t.MaxUses` and decrements exactly once. The race
detector (`-race`) confirms no data race on the `MaxUses` field
under this pattern.

`Cleanup` runs synchronously on the caller's goroutine:

```go
func (s *memStore) Cleanup(ctx context.Context) {
    ticker := time.NewTicker(s.tick)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            s.sweepExpired()
        }
    }
}

func (s *memStore) sweepExpired() {
    now := s.nowFn()
    s.mu.Lock()
    defer s.mu.Unlock()
    for jti, t := range s.live {
        if !t.ExpiresAt.After(now) {
            delete(s.live, jti)
        }
    }
}
```

The caller (server.Server) invokes `go store.Cleanup(ctx)` from a
clearly-owned goroutine; when the server shuts down, `ctx` is
cancelled and `Cleanup` returns within one `tick` interval. The
chunk contract says "Cleanup runs in a caller-controlled goroutine
via `Store.Cleanup(ctx)`; tick interval injectable in tests" — this
implementation matches both clauses.

**Rationale**: The chunk contract names this design verbatim
("`sync.RWMutex` + `map[string]*Token`"). The concurrency-correctness
property (N goroutines decrementing one JTI produce exactly N
successes) is asserted by `TestStore_ConcurrentDecrement` running
under `-race`.

The two-map split (`live` vs. `revoked`) is the cleanest way to
express the FR-011 "revocation persists for the lifetime of the
store" guarantee. Cleanup walks `live` only; revoked stays. The
storage cost is modest: a `map[string]struct{}` entry is ~32 bytes
(header + ~16 bytes for the JTI key + struct{} value = 0); a busy
server that revokes ~100 JTIs/day across 30 days holds ~96 KB of
revoked-set state, which is bounded by operator behaviour rather
than by package design. If the revoked set ever grows large enough
to matter, a future hardening pass can add a `RevokeWithExpiry`
method that lets revocation entries themselves expire after a
configurable horizon (e.g. 7d post-original-TTL); this is a future
non-breaking extension.

The `nowFn` clock seam follows the same Constitution-IX pattern as
the `randReader` seam (R-003) and SDD-08's `nowFn`; tests swap it
via a per-store closure (constructor option) rather than a
package-level mutable.

The `tick` injectable defaults to `30 * time.Second` — matching
SDD-08's nonce-cache sweep cadence. Tests inject `1 * time.Millisecond`
via `NewStoreWithTick`. The chunk contract names `Cleanup` as a
synchronous method; the constructor option for `tick` is the plan's
extension to support deterministic test sweeps.

**Alternatives considered**:

- *Lock-free atomic `MaxUses` decrement*: rejected. `atomic.AddInt32`
  with a "go below zero?" check would let two goroutines both observe
  `MaxUses == 1`, both atomically-decrement to `0` and `-1`, and both
  succeed — violating the exactly-N-success invariant. A
  compare-and-swap loop could fix this but is more complex than the
  `RWMutex` write-lock pattern, with no observable performance benefit
  at v0.1.0 scale (hundreds of decrements per minute, not millions).
- *Sharded mutex (one mutex per JTI)*: rejected. Sharding is a
  scalability optimisation for thousand+ concurrent decrements per
  second; SDD-07's scale target (hundreds per minute) does not
  warrant the complexity.
- *Use `sync.Map` instead of `map[string]*Token` + `sync.RWMutex`*:
  rejected. `sync.Map` is documented to be slower than a plain map
  with `sync.RWMutex` when the read/write ratio is roughly balanced
  (SDD-08 used `sync.Map` for the nonce cache because there was no
  shared decrement; SDD-07's `ConsumeUse` is shared decrement, which
  is `sync.RWMutex` territory).
- *Run `Cleanup` from package code via `init()` or a constructor-spawned
  goroutine*: rejected. Constitution IX bans `init()` and
  fire-and-forget goroutines; the synchronous-method-with-caller-owned-goroutine
  pattern is the clean alternative.
- *Persist the live and revoked maps to disk*: rejected. `docs/SPEC.md`
  Assumptions section explicitly says "the package's store is in-process
  and is not persisted across restarts" — a server restart is, by
  design, a revocation of every active session.
- *Use a single map with a `revoked bool` field on `*Token`*: rejected.
  The `Token` struct is the in-store record; embedding revocation
  state in the record would require keeping the record alive past
  TTL just to remember the revocation. The two-map split lets
  `Cleanup` reclaim memory while preserving the revocation guarantee.

---

## R-006 — Algorithm-confusion defence

**Decision**: `Validate` parses the JWT header WITHOUT trusting it,
inspects the `alg` field, and refuses any value other than `"ES256K"`
with `ErrAlgorithmUnsupported`. The header parse uses a custom
header-only parser that base64url-decodes the first segment of the
compact form and JSON-decodes the result into a minimal struct
`{ Alg string \`json:"alg"\` }`. The check fires BEFORE
`jwt.ParseWithClaims` is invoked (so the keyfunc is never consulted
for a non-ES256K token).

```go
func validateAlgorithm(encoded string) error {
    dot := strings.Index(encoded, ".")
    if dot < 0 {
        return ErrAlgorithmUnsupported
    }
    headerJSON, err := base64.RawURLEncoding.DecodeString(encoded[:dot])
    if err != nil {
        return ErrAlgorithmUnsupported
    }
    var hdr struct {
        Alg string `json:"alg"`
    }
    if err := json.Unmarshal(headerJSON, &hdr); err != nil {
        return ErrAlgorithmUnsupported
    }
    if hdr.Alg != "ES256K" {
        return ErrAlgorithmUnsupported
    }
    return nil
}
```

Two named tests exercise the two textbook alg-confusion classes:
- `TestValidate_AlgConfusion_None_Refused` — a token whose header
  is `{"alg":"none","typ":"JWT"}` (so the signature segment is empty
  bytes) is rejected with `ErrAlgorithmUnsupported`.
- `TestValidate_AlgConfusion_HS256_Refused` — a token whose header
  is `{"alg":"HS256","typ":"JWT"}` and whose signature is the result
  of HMAC-SHA256 over the signing input using the package's verify
  key bytes (the "use the verify key as a shared secret" classic
  alg-confusion attack) is rejected with `ErrAlgorithmUnsupported`.

The chunk contract names both tests explicitly.

**Rationale**: Algorithm-confusion attacks against JWT verifiers are
the single most well-documented JWT vulnerability class
(`docs/SECURITY.md` Layer 2 implicitly addresses this via the "Only
the server can sign" property; FR-002 and FR-003 spell it out
explicitly).

The defence MUST happen on header inspection BEFORE the keyfunc is
consulted. If the header gate ran AFTER the keyfunc, an attacker
could feed a `{"alg":"HS256"}` token whose signature is HMAC over the
verify key — and the keyfunc, expecting an ECDSA public key, would
be invoked with the verify key, return it, and the JWT library would
HMAC-verify the signature against the verify key as if it were a
shared secret. The signature would verify; the attacker would have
forged a valid token.

`golang-jwt/jwt/v5` provides some defence-in-depth against this via
the `jwt.WithValidMethods([]string{"ES256K"})` parser option, which
restricts the parser to a whitelist of accepted methods. We use BOTH
the explicit `validateAlgorithm` pre-check AND the `WithValidMethods`
parser option (defence in depth — if a future version of the library
ever weakens its method-whitelist behaviour, the explicit pre-check
catches the regression).

**Alternatives considered**:

- *Rely solely on `jwt.WithValidMethods(...)`*: rejected per
  defence-in-depth. The chunk contract explicitly names "explicitly
  map 'none' and 'HS256' to ErrAlgorithmUnsupported in distinct test
  cases" — two test cases imply two observable rejection paths (the
  pre-check fires for both).
- *Reject only `"none"` and accept any other algorithm*: rejected.
  FR-003 says "any algorithm other than the project's ES256K" must
  be rejected — `"HS256"` is the textbook attack but the spec demands
  total alg-whitelist enforcement.
- *Use a deny-list (`{"none", "HS256"}`) instead of a whitelist
  (`{"ES256K"}`)*: rejected. A whitelist is fail-closed; a deny-list
  is fail-open against future JWT alg additions (e.g., a new RS-PSS
  or EdDSA method that an attacker could substitute).
- *Inspect the header through the library's `Token.Header` after
  `Parse`*: rejected — that runs AFTER the keyfunc, defeating the
  pre-check property.

---

## R-007 — Sentinel error catalogue

**Decision**: The package exports seven sentinels, all
`var Err... = errors.New("hush/token: <message>")` declarations
colocated in `errors.go`. Static messages; no `fmt.Errorf` formatting
on any sentinel.

| Sentinel                  | Static message                          | Triggered by                                                                                        |
|---------------------------|-----------------------------------------|-----------------------------------------------------------------------------------------------------|
| `ErrAlgorithmUnsupported` | `hush/token: algorithm unsupported`     | Header `alg` not `"ES256K"` (including `"none"`, `"HS256"`, malformed header). Also returned for every signature-class failure (DER decode, signature verify) — FR-007 #1's "signature failure" umbrella. |
| `ErrTokenExpired`         | `hush/token: token expired`             | `Claims.ExpiresAt.Before(now)` at validate time (mapped from `jwt.ErrTokenExpired`). Also returned by `Store.ConsumeUse` if the in-store record's `ExpiresAt <= now`. |
| `ErrTokenRevoked`         | `hush/token: token revoked`             | JTI present in the revoked set (`Store.Get`, `Store.ConsumeUse`). Also returned by `Store.Add` if the JTI collides with a revoked-set entry. |
| `ErrTokenExhausted`       | `hush/token: token exhausted`           | `Store.ConsumeUse` for an INTERACTIVE token whose `MaxUses == 0`. Never returned for SUPERVISOR.    |
| `ErrIPMismatch`           | `hush/token: ip mismatch`               | `Validate`'s `compareIPs` returns non-equal; either input fails to parse via `netip.ParseAddr`.    |
| `ErrScopeViolation`       | `hush/token: scope violation`           | `requestedSecret` not present in `Claims.Scope`.                                                    |
| `ErrUnknownSessionType`   | `hush/token: unknown session type`      | `Claims.SessionType` not in `{SessionInteractive, SessionSupervisor}`.                              |

All seven sentinels are independent — no wrap relationships. Compare
via `errors.Is`. The `Validate` function returns sentinels by direct
return; consumers MUST NOT depend on `errors.Unwrap` chains.

The single exception is `ctx.Err()`: when the caller's context is
already-cancelled at entry, `Issue` and `Validate` return `ctx.Err()`
verbatim. `errors.Is(err, context.Canceled)` and
`errors.Is(err, context.DeadlineExceeded)` evaluate as expected for
this path. `ctx.Err()` is NOT one of the package's sentinels; it is
the stdlib value passed through unchanged.

**Rationale**: Spec FR-013 mandates seven distinct, comparable
sentinels. Spec FR-014 mandates static error messages that contain
no JWT/key bytes. The catalogue above satisfies both.

The "signature failure → `ErrAlgorithmUnsupported`" mapping is
deliberate: from the spec's FR-007 #1 ("signature is not valid"),
the sentinel category is "the alg-class umbrella". A token that
asserts `alg=ES256K` but whose signature does not verify is, from
the recipient's perspective, indistinguishable from a token that
asserts `alg=HS256` and could not be verified against the ES256K
verify key — both are "the asserted algorithm did not produce a
verifying signature against the supplied key". Collapsing into a
single sentinel matches FR-002/FR-003's "the same sentinel for any
unsupported algorithm". The audit log records the `request_id`
(from upstream), so the failure shape is preserved at the right
layer.

There are no wrap relationships (no umbrella `ErrTokenInvalid`
parent). Each sentinel is its own identity; consumers care about
the specific category (audit-log handler classifies expired vs.
exhausted vs. revoked differently for retention policy).

**Alternatives considered**:

- *Eight-sentinel catalogue with separate `ErrSignatureInvalid`*:
  rejected. The chunk contract locks seven sentinels at SDD-07;
  adding an eighth would require a chunk-contract amendment, and
  the FR-007 #1 mapping (signature failure → algorithm-unsupported
  umbrella) is constitutionally clean.
- *Wrap all seven under a top-level `ErrTokenInvalid`*: rejected.
  Cosmetic-only; each consumer cares about the specific category.
- *Use `fmt.Errorf("token expired: %s", jti)` to embed the JTI in
  the message*: rejected. The JTI is a low-sensitivity identifier
  but FR-014 demands "static messages that contain only the failure
  category". The audit log writer logs the JTI separately; the
  error message itself is the category only.
- *Promote each sentinel to an error type with sub-fields*: rejected.
  Sub-fields would leak the failure shape through the type; the
  sentinel pattern keeps the discipline simple.

---

## R-008 — IssueParams validation

**Decision**: `Issue` validates `IssueParams` at entry and returns a
typed error if the params are malformed. Validation rules:

| Field             | Rule                                                                  | Failure sentinel             |
|-------------------|-----------------------------------------------------------------------|------------------------------|
| `Now`             | non-zero `time.Time`                                                   | `ErrAlgorithmUnsupported` (umbrella for "input invalid before sign") via `fmt.Errorf("hush/token: invalid IssueParams: now is zero: %w", ErrAlgorithmUnsupported)` |
| `TTL`             | positive `time.Duration`                                               | same                         |
| `Scope`           | non-empty slice; every entry non-empty string                          | same                         |
| `ClientIP`        | parses via `netip.ParseAddr` to a non-zero address                     | same                         |
| `RequestID`       | non-empty string                                                       | same                         |
| `MaxUses`         | for `SessionInteractive`: positive int. For `SessionSupervisor`: zero (we set it to 0 internally; non-zero input is silently zeroed for SUPERVISOR per the chunk contract's "Supervisor session type is TTL-only (ignores MaxUses)" property) | same |
| `EphemeralPubKey` | non-empty string (pubkey-format validation is the consumer's responsibility — the package treats it as opaque) | same |
| `SessionType`     | `SessionInteractive` or `SessionSupervisor`                            | `ErrUnknownSessionType`      |

For each malformed field, `Issue` returns
`fmt.Errorf("hush/token: invalid IssueParams: <field>: %w", sentinel)`.
This is the **single** place in the package where `fmt.Errorf` is
used: the error message names the field that failed (which is not
sensitive — `IssueParams` field names are public package identifiers),
and the wrapped sentinel lets consumers `errors.Is(err,
ErrAlgorithmUnsupported)` / `errors.Is(err, ErrUnknownSessionType)`
to identify the category.

Wait — the static-message discipline (R-007) bans `fmt.Errorf`. The
chunk contract clarifies this: the static-message ban applies to
**rejection sentinels surfaced from `Validate`**, where the input is
attacker-controlled and the error message is observed by external
consumers. `Issue` is server-internal; the caller is the claim
handler (SDD-12), which is trusted code. An `IssueParams` validation
failure at issue time is a **programmer error** (the claim handler
populated the params wrongly), not an attacker-driven event; the
field-name in the error message is diagnostic, not sensitive.

To keep R-007 tight, the implementation chooses a stricter alternative:
`Issue` returns the **bare sentinel** for each malformed field (no
field-name embedding). Consumers consult their own logs (claim
handler logging from SDD-12 captures the offending IssueParams
**fields by name without the values**, per the same Constitution X
discipline). This is a clean rule:

> Rule: every error return in this package is a bare sentinel from
> the catalogue (R-007), with no `fmt.Errorf` formatting. The single
> exception is `ctx.Err()` returned verbatim.

| Field             | Rule                                                                  | Failure sentinel             |
|-------------------|-----------------------------------------------------------------------|------------------------------|
| `SessionType` not in vocabulary  | reject before any other validation        | `ErrUnknownSessionType`      |
| Any other malformed field        | reject before signing                     | `ErrAlgorithmUnsupported` (re-used as the "invalid issue input" umbrella; the claim handler sees the bare sentinel and consults its own logs for the field) |

This keeps the seven-sentinel catalogue intact. The FR-013 promise
("seven distinct, comparable sentinel errors") is satisfied by reuse
of two existing sentinels for the issue-time validation failures —
`ErrUnknownSessionType` for the obvious case and
`ErrAlgorithmUnsupported` as the catch-all for "issue input cannot
be signed". The chunk contract's exported sentinel list is unchanged.

**Rationale**: Constitution IX's idiomatic-Go discipline says "wrap
errors with %w, compare with errors.Is". The R-008 rule honors this
in spirit: errors are sentinel-class; consumers compare via
`errors.Is`. Consumer logs (claim handler) capture the IssueParams
shape on failure for diagnostics; the package itself does not embed
field names in errors.

**Alternatives considered**:

- *Add an eighth sentinel `ErrInvalidIssueParams`*: rejected. The
  chunk contract locks seven sentinels at SDD-07.
- *Use `fmt.Errorf("hush/token: invalid IssueParams field=%s: %w",
  fieldName, ErrAlgorithmUnsupported)`*: rejected per the strict
  static-message rule above. (A future hardening pass could add an
  optional `IssueValidationError` type with structured fields if
  consumer diagnostics ever need richer info than what consumer
  logs already capture.)
- *Validate at the consumer (claim handler) and have `Issue` skip
  validation*: rejected. The package's contract should reject
  malformed input at the package boundary; trusting the caller to
  validate is fragile.

---

## R-009 — Cleanup goroutine ownership

**Decision**: `Store.Cleanup(ctx context.Context)` is a **synchronous**
method. The caller (typically `server.Server.Run`) invokes it as
`go store.Cleanup(ctx)` from a clearly-owned goroutine. The method
runs a `time.NewTicker(s.tick)` loop; on each tick it acquires the
write lock and removes expired live records; on `ctx.Done()` it
returns.

The tick interval is **injectable** via a constructor option:

```go
func NewStore() Store                       // tick = 30s, nowFn = time.Now
func NewStoreWithTick(d time.Duration) Store // tick = d, nowFn = time.Now (test seam)
```

Tests inject `NewStoreWithTick(1 * time.Millisecond)` for
deterministic sweep observation; production wires `NewStore()`.

**Rationale**: Constitution IX:

> **Goroutine discipline:** every goroutine has a clear owner, an
> explicit cancellation path (context), and a documented termination
> condition. No fire-and-forget goroutines.

The caller-spawned goroutine pattern satisfies all three:
- **Owner**: the `server.Server.Run` method (or test `t.Cleanup`).
- **Cancellation path**: the `ctx` passed to `Cleanup`.
- **Termination condition**: `ctx.Done()` fires; method returns
  within one `tick` interval.

The chunk contract names this pattern explicitly: "Cleanup runs in a
caller-controlled goroutine via `Store.Cleanup(ctx)`; tick interval
injectable in tests".

The 30 s default tick interval matches SDD-08's nonce-cache sweep
cadence — a reasonable default for "expire stale records without
busy-spinning". The interval is not security-critical (an expired
token still fails `Validate`'s `Claims.ExpiresAt` check before
reaching the store; cleanup is a memory-reclamation mechanism, not
a security gate).

**Alternatives considered**:

- *Spawn the goroutine inside `NewStore`*: rejected per Constitution
  IX (fire-and-forget goroutines are forbidden).
- *Run cleanup inline at every `Add` or `ConsumeUse` call*: rejected.
  Latency cost on the hot path; cleanup walks the whole map.
- *Use a `time.AfterFunc` per-token for self-cleanup*: rejected.
  Each `time.AfterFunc` is a goroutine in the runtime's timer
  heap; for hundreds of active tokens, the overhead beats the
  ticker-based pattern.
- *Expose a `Sweep()` method that the caller invokes manually*:
  rejected. Forcing the caller to schedule sweep adds API surface;
  the synchronous-`Cleanup`-with-caller-owned-goroutine pattern is
  the cleanest.
- *Add a `Stop()` method instead of `ctx`*: rejected. `ctx` is
  Constitution IX's mandated cancellation path; `Stop()` would be
  an idiosyncratic API.

---

## R-010 — Sentinel-leak test: TestValidate_NoLeakOnError

**Decision**: A dedicated test (`TestValidate_NoLeakOnError`)
implements the SC-009 / SC-013 "error-message redaction" property:

```go
func TestValidate_NoLeakOnError(t *testing.T) {
    sentinel := testutil.SentinelSecret(2) // "SECRET_SHOULD_NEVER_APPEAR_2"
    priv := generateFreshKey(t)
    pub := &priv.PublicKey
    store := token.NewStore()

    // Issue a token whose request_id contains the sentinel (worst case —
    // claim contents that an upstream caller could choose).
    params := token.IssueParams{
        Now: time.Now(), TTL: time.Hour,
        Scope: []string{"FAKE_SECRET"},
        ClientIP: "100.64.0.1",
        RequestID: "req-" + sentinel + "-id",
        MaxUses: 5,
        EphemeralPubKey: "deadbeef",
        SessionType: token.SessionInteractive,
    }
    tok, err := token.Issue(t.Context(), priv, params)
    if err != nil {
        t.Fatalf("issue failed: %v", err)
    }
    if err := store.Add(tok); err != nil {
        t.Fatalf("add failed: %v", err)
    }

    // Drive every rejection category and assert sentinel absence.
    cases := []struct {
        name      string
        wantErr   error
        invocation func() error
    }{
        {"alg-confusion-none", token.ErrAlgorithmUnsupported, func() error {
            // Mutate the token to alg=none; expect ErrAlgorithmUnsupported.
            mangled := mangleAlgToNone(tok.Encoded)
            _, e := token.Validate(t.Context(), mangled, pub, store, "100.64.0.1", "FAKE_SECRET")
            return e
        }},
        {"alg-confusion-hs256", token.ErrAlgorithmUnsupported, func() error {
            mangled := mangleAlgToHS256(tok.Encoded)
            _, e := token.Validate(t.Context(), mangled, pub, store, "100.64.0.1", "FAKE_SECRET")
            return e
        }},
        {"expired", token.ErrTokenExpired, func() error {
            // Issue a fresh token with a past TTL via IssueParams.Now in past.
            past := token.IssueParams{Now: time.Now().Add(-time.Hour), TTL: time.Minute,
                Scope: params.Scope, ClientIP: params.ClientIP, RequestID: params.RequestID,
                MaxUses: 1, EphemeralPubKey: params.EphemeralPubKey, SessionType: token.SessionInteractive}
            expired, _ := token.Issue(t.Context(), priv, past)
            _, e := token.Validate(t.Context(), expired.Encoded, pub, store, "100.64.0.1", "FAKE_SECRET")
            return e
        }},
        {"wrong-ip", token.ErrIPMismatch, func() error {
            _, e := token.Validate(t.Context(), tok.Encoded, pub, store, "100.64.0.99", "FAKE_SECRET")
            return e
        }},
        {"out-of-scope", token.ErrScopeViolation, func() error {
            _, e := token.Validate(t.Context(), tok.Encoded, pub, store, "100.64.0.1", "OTHER_SECRET")
            return e
        }},
        {"revoked", token.ErrTokenRevoked, func() error {
            _ = store.Revoke(tok.JTI)
            _, e := token.Validate(t.Context(), tok.Encoded, pub, store, "100.64.0.1", "FAKE_SECRET")
            return e
        }},
        // … plus exhausted and unknown-session-type cases.
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            err := tc.invocation()
            if !errors.Is(err, tc.wantErr) {
                t.Fatalf("got %v, want %v", err, tc.wantErr)
            }
            testutil.AssertSentinelAbsent(t, sentinel, err.Error())
            for cur := err; cur != nil; cur = errors.Unwrap(cur) {
                testutil.AssertSentinelAbsent(t, sentinel, cur.Error())
            }
        })
    }
}
```

The test uses `testutil.SentinelSecret(2)` and
`testutil.AssertSentinelAbsent` from SDD-04 — the canonical
sentinel-leak helpers.

**Rationale**: The static-error-messages discipline (R-007) makes the
property hold by construction. The test is the runtime witness; if
a future change accidentally introduces a `fmt.Errorf("token expired:
jti=%s", t.JTI)`, the test fails immediately.

**Alternatives considered**:

- *Use `slog.NewJSONHandler` to capture log output*: not applicable.
  The package emits zero log lines (FR-017).
- *Skip the wrap-chain walk because the package does not wrap*:
  rejected. The walk is defensive — costs nothing, catches future
  regressions where someone adds an unintended `fmt.Errorf("...:
  %w", err)` wrap.
- *Encrypt a plaintext that IS the sentinel (no prefix/suffix)*:
  N/A — the package's claim values are caller-supplied, and the
  test embeds the sentinel inside `RequestID` (a string claim) which
  is the worst-case sensitive surface in the Claims struct.

---

## R-011 — Fuzz target: `FuzzJWTValidate`

**Decision**: Add `validate_fuzz_test.go` with `FuzzJWTValidate(f
*testing.F)`:

```go
func FuzzJWTValidate(f *testing.F) {
    priv := generateFuzzKey(f) // deterministic test key
    pub := &priv.PublicKey
    store := token.NewStore()
    seedFuzzCorpus(f) // f.Add(...) for each file in testdata/fuzz/FuzzJWTValidate/

    f.Fuzz(func(t *testing.T, encoded string) {
        _, err := token.Validate(t.Context(), encoded, pub, store, "100.64.0.1", "FAKE_SECRET")
        if err == nil {
            // Random bytes that happen to verify against this key with this
            // store and these check params — vanishingly unlikely; treat as a pass.
            return
        }
        if !errors.Is(err, token.ErrAlgorithmUnsupported) &&
           !errors.Is(err, token.ErrTokenExpired) &&
           !errors.Is(err, token.ErrTokenRevoked) &&
           !errors.Is(err, token.ErrTokenExhausted) &&
           !errors.Is(err, token.ErrIPMismatch) &&
           !errors.Is(err, token.ErrScopeViolation) &&
           !errors.Is(err, token.ErrUnknownSessionType) &&
           !errors.Is(err, context.Canceled) &&
           !errors.Is(err, context.DeadlineExceeded) {
            t.Errorf("Validate returned non-sentinel error type %T: %v", err, err)
        }
    })
}
```

Seed corpus (in `testdata/fuzz/FuzzJWTValidate/`):

| Seed file                        | Bytes                                                                 | Exercises                                                  |
|----------------------------------|-----------------------------------------------------------------------|------------------------------------------------------------|
| `empty`                          | `(empty string)`                                                      | The empty-input early-out (no `.` separators)              |
| `malformed-base64`               | `not.valid.base64`                                                    | The base64-decode rejection in `validateAlgorithm`         |
| `valid-base64-bad-json`          | `<base64url("hello")>.<base64url("world")>.<base64url("garbage")>`    | The JSON-unmarshal rejection in `validateAlgorithm`        |
| `alg-none`                       | A real JWT-shaped string with `{"alg":"none"}` header and empty sig    | The alg-confusion `none` path                              |
| `alg-hs256`                      | A real JWT-shaped string with `{"alg":"HS256"}` header and HMAC sig    | The alg-confusion `HS256` path                             |

Seed bytes are deterministically generated at fuzz-target setup (the
last two seeds are pre-computed from a fixed test key and committed
under `testdata/fuzz/FuzzJWTValidate/`). CI gate (per the
implement-phase release-step list): `go test -fuzz=FuzzJWTValidate
-fuzztime=60s ./internal/token/` runs clean — no panic, no new
corpus entries representing crashes.

**Rationale**: The chunk contract names `FuzzJWTValidate` and the
60 s gate. Constitution VIII fuzz target #2 ("JWT parse/validate")
is the constitutional gate for SDD-07. The "no panic, every error
typed" invariant is the load-bearing security property: a panic in
`Validate` would crash the secret-fetch handler (SDD-13), and an
attacker who chose hostile bytes could deny service via crash. The
seed corpus accelerates fuzz-coverage convergence — the five seeds
cover the major Validate-path branches (no `.`, bad base64, bad JSON,
alg=none, alg=HS256) so 60 s of fuzzing actually exercises the
verification logic, not just the early-out.

**Alternatives considered**:

- *Skip the seed corpus*: rejected — 60 s of random bytes converges
  slowly through the parse early-out.
- *Run for 5 minutes*: deferred — chunk contract says 60 s, CI
  cost matters.
- *Fuzz `Issue` separately*: deferred — `Issue`'s input space is
  structured `IssueParams`; the chunk contract names FuzzJWTValidate
  as the single 60 s target.

---

## R-012 — Cancellation semantics

**Decision**: `Issue`, `Validate`, and `Store.Cleanup` honor context
cancellation as follows:

- **Pre-cancellation** (caller's `ctx` is already cancelled at entry):
  `Issue` and `Validate` return `ctx.Err()` verbatim. `Store.Cleanup`
  returns immediately.
- **Mid-operation cancellation** (caller cancels while the function is
  executing): NOT honored for `Issue` (signing is short and CPU-bound)
  or `Validate` (parse + verify is short and CPU-bound); aborting
  mid-operation has no security benefit. `Store.Cleanup` checks
  `ctx.Done()` between ticks; mid-sweep, the sweep completes (it holds
  the write lock; releasing mid-sweep would leave the store in a
  partial state).

The pre-cancellation behavior preserves
`errors.Is(err, context.Canceled)` and
`errors.Is(err, context.DeadlineExceeded)` (FR-015 / SC-011).

**Rationale**: Same pattern SDD-08 and SDD-09 use. The cancellation
gate is a Constitution IX requirement (`context.Context` first
parameter for cancellable work); honoring it at entry is the
Go-idiomatic pattern.

**Alternatives considered**:

- *Honor mid-operation cancellation in Validate*: rejected. Adding
  ctx.Err() checks between header parse, signature verify, and
  claim walk adds branching for no security benefit. The operations
  are sub-millisecond.
- *Have `Store.Cleanup` use `select` with a default branch to abort
  mid-tick*: rejected — the sweep holds the write lock; aborting
  mid-sweep with a partial map mutation would corrupt the store.

---

## R-013 — Test fixture keys: fresh per test run

**Decision**: Non-fuzz tests generate fresh ECDSA keypairs per run via
`ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)`. Fuzz tests use a
deterministic key (R-011's `generateFuzzKey`). Tests do NOT import
`internal/keys.DeriveJWTSigningKey` for fixture keys.

**Rationale**: The package operates on raw `*ecdsa.PrivateKey` /
`*ecdsa.PublicKey` regardless of derivation provenance. Using
`ecdsa.GenerateKey` directly (a) keeps the test independent of the
SDD-01 derivation path, (b) makes each test-run independent, (c)
catches any defect that depends on a specific key (the BIP32-derived
key is one specific key shape; fresh keys exercise a wider input
distribution).

The decred secp256k1 module is imported in the test file via
`github.com/decred/dcrd/dcrec/secp256k1/v4` for the curve identity
`secp256k1.S256()`; this is already a direct dep of the project (from
SDD-01) so no new dep is added by the test.

**Alternatives considered**:

- *Use `internal/keys.DeriveJWTSigningKey` with a deterministic
  passphrase*: rejected per the independence argument above.
- *Generate one key per test file and reuse across tests*: rejected —
  each test should construct its own fixtures so test ordering and
  parallel execution don't interfere.

---

## R-014 — Package-level concurrency: stateless aside from the store

**Decision**: `Issue` and `Validate` are pure functions on their
inputs (modulo `crypto/rand.Reader` consumption in `Issue` for the
JTI generator, which is stdlib-concurrency-safe). The only
package-level mutable state is the `sync.Once` for ES256K registration
(set-once, read-only after first `Do`) and the `randReader` test seam
(set-once at package init to `rand.Reader`, mutated only by test
helpers under `t.Cleanup` capture-and-restore).

The in-memory `Store` is concurrent-safe via its `sync.RWMutex`.
Multiple goroutines may invoke `Add` / `Get` / `ConsumeUse` /
`Revoke` / `Cleanup` concurrently with NO additional synchronisation.

A `TestStore_ConcurrentDecrement` test launches N=100 goroutines, each
calling `ConsumeUse` on the same JTI whose `MaxUses` was issued at
100. Exactly 100 successes are observed; the (101)-th call returns
`ErrTokenExhausted`. The test runs under `-race` via `magex
test:race`.

**Rationale**: Constitution IX "every goroutine has a clear owner".
The package itself spawns no goroutines; consumer goroutines own
their own invocations. Statelessness (modulo the bounded sync.Once
exception) is the simplest concurrency model.

**Alternatives considered**:

- *Sharded store mutex*: rejected per scale concerns (R-005).
- *Lock-free store via `sync/atomic`*: rejected per correctness
  concerns (R-005).
