# Implementation Plan: `internal/token` — ES256K JWT issuance, validation, store, revocation (SDD-07)

**Branch**: `007-token-jwt` | **Date**: 2026-04-29 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/007-token-jwt/spec.md`
**Chunk contract**: [docs/sdd/SDD-07.md](../../docs/sdd/SDD-07.md)

## Summary

`internal/token` is the session-authority of the vault server. After the
human approver clicks "Approve" on a Discord DM, the claim handler (SDD-12)
calls `Issue` with the parameters the approval defined — requesting client
IP, approved scope of secret names, request identifier, ephemeral pubkey,
TTL, max-uses (interactive only), and session shape — and receives an
opaque ES256K-signed JWT plus the in-store `*Token` record. Every
subsequent secret-fetch request (SDD-13) goes through `Validate`, which
checks signature + algorithm + expiry + scope + IP + revocation +
use-exhaustion before the secret-fetch handler is permitted to read a
`*SecureBytes` out of the vault. The package is **Layer 2** of the
seven-layer security stack (`docs/SECURITY.md` §3 Layer 2 — asymmetric
JWT signing) and the package that delivers acceptance criterion **AC-4**
(JWT lifecycle: IP-bind, max-uses, revoke, claims).

Approach (locked by SDD-07 chunk contract + Constitution III/IV/VIII/IX/X;
not subject to research alternatives):

- **JWT library — `github.com/golang-jwt/jwt/v5`**, a NEW direct dependency.
  The stdlib has no JWT support; the canonical Go JWT library is
  `golang-jwt/jwt`, the maintained successor to `dgrijalva/jwt-go`. The
  trusted-sources walk (Constitution XI: stdlib → sigil → bsv-blockchain
  → wider ecosystem) bottoms out at the wider ecosystem because no
  upstream tier in the hierarchy supplies a JWT primitive — see research
  [R-001](./research.md#r-001--jwt-library-golang-jwtjwtv5). The dep adds
  one entry to `go.mod` with zero further transitive dependencies.

- **ES256K signing method registration via `sync.Once`-gated `Register()` —
  NOT `init()`.** Constitution IX bans `init()` and mutable package-level
  state; the chunk contract names the `sync.Once` in a `Register()`
  helper invoked by `Issue` and `Validate` before any JWT work as the
  bounded constitutional exception (the `sync.Once` is set-once and
  read-only thereafter; no caller-observable mutation after the first
  call). The signing method delegates **sign** and **verify** to stdlib
  `crypto/ecdsa.SignASN1` and `crypto/ecdsa.VerifyASN1` over
  `sha256.Sum256(signingInput)` — same stdlib substitution SDD-08's
  `internal/transport/sign` made for ECDSA Sign/Verify ([R-002](./research.md#r-002--es256k-signing-method-stdlib-substitution)).
  The signing method's `Alg()` returns the literal string `"ES256K"`.

- **Algorithm-confusion defence — header inspection BEFORE signature
  verification.** `Validate` parses the JWT header WITHOUT trusting it,
  reads the `alg` field, and refuses any value other than `"ES256K"` with
  `ErrAlgorithmUnsupported`. Distinct named test cases exercise the
  `"none"` pseudo-algorithm AND the `"HS256"` symmetric algorithm — the
  two textbook alg-confusion classes — to prove the defence is observable
  for both. The header gate fires before the package's `keyfunc` is ever
  consulted, so the verify key never enters the verify primitive on a
  rejection path ([R-006](./research.md#r-006--algorithm-confusion-defence)).

- **JTI generation — 16 bytes from `crypto/rand.Reader`, then RFC 4122 v4
  version/variant bits, then canonical hyphenated hex form.** Stdlib-only
  (no `github.com/google/uuid` dep). The function is a 12-line helper in
  `issue.go`; tests use an injectable `randReader io.Reader` package-level
  variable that defaults to `crypto/rand.Reader` so deterministic seeds
  drive deterministic JTIs in unit tests. Because `crypto/rand.Reader` is
  the OS CSPRNG (Constitution III mandates `crypto/rand` for entropy), the
  resulting JTIs are unguessable and non-colliding with cryptographic
  probability — the store's "fresh JTI never collides with revoked"
  guarantee (FR-011 / FR-008) holds by construction
  ([R-003](./research.md#r-003--jti-generation-from-cryptorand)).

- **Claims layout** (fields with names locked at SDD-07; `Claims` embeds
  `jwt.RegisteredClaims` for `iss/iat/exp/jti` so the underlying library
  handles the standard JWT validity windows automatically):

  ```go
  type Claims struct {
      jwt.RegisteredClaims                // iss, iat, exp, jti
      Scope           []string `json:"scope"`
      ClientIP        string   `json:"client_ip"`
      RequestID       string   `json:"request_id"`
      MaxUses         int      `json:"max_uses"`
      EphemeralPubKey string   `json:"ephemeral_pubkey"`
      SessionType     SessionType `json:"session_type"`
  }
  ```

  `Issue` populates `RegisteredClaims.Issuer = "hush"`, `IssuedAt =
  jwt.NewNumericDate(now())`, `ExpiresAt = jwt.NewNumericDate(now() +
  ttl)`, `ID = generatedJTI`. The library's `RegisteredClaims.Valid()`
  enforces the time windows; `Validate` calls
  `jwt.ParseWithClaims(..., &Claims{}, keyfunc)` which invokes that
  validity check before returning; expiry rejection then surfaces as
  `ErrTokenExpired` via the package's `errors.Is(err, jwt.ErrTokenExpired)`
  mapping ([R-007](./research.md#r-007--sentinel-error-catalogue)).

- **`SessionType`** is a string-typed enum with two values:
  `SessionInteractive = "interactive"` and `SessionSupervisor =
  "supervisor"`. Any other value (including the empty string) on a parsed
  Claims surfaces as `ErrUnknownSessionType` — Validate inspects the
  recovered `SessionType` and rejects unknown values BEFORE consulting the
  store. The vocabulary is closed.

- **`Token`** is the in-store record:

  ```go
  type Token struct {
      JTI         string
      Encoded     string        // the signed compact-form JWT
      ExpiresAt   time.Time
      SessionType SessionType
      MaxUses     int           // 0 for SessionSupervisor
  }
  ```

  Stored fields are the minimum needed for `ConsumeUse` (decrement check),
  `Cleanup` (expiry sweep), and emergency forensics (encoded form for
  audit re-verification). `Encoded` is **never logged** by this package
  (FR-017 / Constitution X); the audit-log writer (SDD-25) writes the
  jti only, never the encoded form.

- **`IssueParams`** carries the issuer-side knobs:

  ```go
  type IssueParams struct {
      Now             time.Time      // injected for deterministic tests; defaults to time.Now()
      TTL             time.Duration  // converted to ExpiresAt = Now.Add(TTL)
      Scope           []string
      ClientIP        string         // normalised via netip.ParseAddr at issue time; rejected if invalid
      RequestID       string         // ties session to upstream Discord approval
      MaxUses         int            // ignored for SessionSupervisor
      EphemeralPubKey string         // hex-encoded compressed secp256k1 pubkey from /claim
      SessionType     SessionType    // SessionInteractive | SessionSupervisor
  }
  ```

  `Now` is always set by the caller (server.Server holds a `clock` field;
  tests inject a fixed instant). Any zero value triggers a typed
  validation error (FR-009 — see research [R-008](./research.md#r-008--issueparams-validation)).

- **`Store` — `sync.RWMutex` + `map[string]*Token`.** The chunk contract
  locks this; the implementation is straightforward. `Add` takes the
  write lock and inserts (rejects on JTI collision with `ErrTokenRevoked`
  if the JTI is in the revoked set, otherwise unique-by-construction).
  `Get` takes the read lock and returns a pointer (the returned `*Token`
  is read-only by convention; no caller mutates it). `ConsumeUse` takes
  the **write lock**, rechecks expiry/revocation/exhaustion under the
  lock (so the race-detector test of N goroutines decrementing one
  interactive token observes exactly N successes), and decrements
  `MaxUses` for INTERACTIVE; SUPERVISOR returns immediately without
  decrementing. `Revoke` takes the write lock, marks the JTI in a
  separate `revoked map[string]struct{}` (so revocation persists past
  TTL-driven cleanup), and removes the live record. `Cleanup` takes the
  write lock, walks the live map, and removes entries whose `ExpiresAt
  <= now()`; revoked-set entries are NEVER cleared by `Cleanup` (that
  is the FR-011 lifetime-of-store guarantee — cleanup may reclaim the
  live record but the revocation remains until the store itself is
  dropped) ([R-005](./research.md#r-005--token-store-concurrency)).

- **`Cleanup` lifecycle — caller-controlled goroutine.** Constitution IX
  bans fire-and-forget goroutines; the package therefore exposes
  `Cleanup(ctx context.Context)` as a SYNCHRONOUS method on the in-memory
  `Store`. The caller (server.Server) invokes it as `go store.Cleanup(ctx)`
  in a clearly-owned goroutine; the method's tick-loop watches `ctx.Done()`
  and returns when the context fires. The tick interval is **injectable**
  via a constructor option (`NewStoreWithTick(d time.Duration)`); production
  uses `30 * time.Second` (matching SDD-08's nonce-cache sweep cadence);
  tests inject `1 * time.Millisecond` and assert deterministic sweep
  observation ([R-009](./research.md#r-009--cleanup-goroutine-ownership)).

- **IP comparison — `netip.ParseAddr` + `Addr` equality (`==`).** Two
  textual representations of the same address (e.g. `"100.64.0.1"` vs
  `"100.064.000.001"`, or IPv6 `"::1"` vs `"0000:0000:...:0001"`) MUST
  compare equal under FR-016. `netip.Addr` is a value type that defines
  `==` semantically — equal addresses produce equal `netip.Addr` values
  regardless of textual form. `Validate` parses both the recovered
  `Claims.ClientIP` and the `requestIP` parameter via
  `netip.ParseAddr`, returning `ErrIPMismatch` if either parse fails or
  the parsed values are unequal ([R-004](./research.md#r-004--ip-comparison)).

- **Validation ordering** (deterministic, observable per FR-007):

  1. `ctx.Err()` early-out → returns `ctx.Err()` verbatim.
  2. Header parse + `alg` inspection → returns `ErrAlgorithmUnsupported`
     for `"none"`, `"HS256"`, or any value other than `"ES256K"`. Fires
     BEFORE the keyfunc is consulted.
  3. `jwt.ParseWithClaims` (signature verify + RegisteredClaims time
     check) → signature failure surfaces as `ErrAlgorithmUnsupported`
     (collapsed by FR-013's "every rejection mode is named" — a
     signature failure is named explicitly via the alg-class umbrella);
     expiry surfaces as `ErrTokenExpired`.
  4. `SessionType` vocabulary check → `ErrUnknownSessionType`.
  5. Scope check (`requestedSecret` in `Claims.Scope`) →
     `ErrScopeViolation`.
  6. IP equality → `ErrIPMismatch`.
  7. Store revocation check → `ErrTokenRevoked` (Validate's `Get` on the
     revoked-set; the revoke set is consulted before the live map).
  8. Use-count check (INTERACTIVE only) → `Store.ConsumeUse` returns
     `ErrTokenExhausted` when `MaxUses == 0`. SUPERVISOR sessions do
     NOT call `ConsumeUse` (the chunk contract's "supervisor TTL-only"
     property — see [R-005](./research.md#r-005--token-store-concurrency)
     for the implementation pattern).

  Each step's failure is an observable, named sentinel; no rejection
  collapses into a generic "invalid token" (FR-013 / SC-009).

- **Sentinel error catalogue (FR-013 / FR-014 — static messages, no
  token bytes in any error message).** Seven exported sentinels:
  `ErrAlgorithmUnsupported`, `ErrTokenExpired`, `ErrTokenRevoked`,
  `ErrTokenExhausted`, `ErrIPMismatch`, `ErrScopeViolation`,
  `ErrUnknownSessionType`. The chunk contract's "Exported API to lock"
  list names exactly these seven. All seven are
  `errors.New("hush/token: <category>")` — static strings; no
  `fmt.Errorf` with token bytes. The `TestValidate_NoLeakOnError` test
  asserts the project sentinel `SECRET_SHOULD_NEVER_APPEAR_2`
  (`testutil.SentinelSecret(2)`) is absent from `err.Error()` for every
  rejection category ([R-007](./research.md#r-007--sentinel-error-catalogue),
  [R-010](./research.md#r-010--sentinel-leak-test)).

- **Logging & observability** (FR-017 / Constitution X): the package
  emits **zero log lines** and takes **no logger dependency**. Diagnostic
  surface is sentinel-error identity only; error messages are static
  category strings (`"hush/token: token expired"`,
  `"hush/token: ip mismatch"`, ...). The encoded JWT is held in the
  `Token.Encoded` field for re-verification but is NEVER passed to a
  formatter or logger by this package.

- **Fuzz target `FuzzJWTValidate`** (constitutional fuzz target #2;
  60 s gate). Feeds random byte streams (interpreted as JWT-shaped
  base64url) to `Validate` against a fixed test-derived ECDSA pubkey
  and a fresh in-memory store. The fuzz body asserts: (a) `Validate`
  never panics, (b) every error returned is one of
  `{ErrAlgorithmUnsupported, ErrTokenExpired, ErrTokenRevoked,
  ErrTokenExhausted, ErrIPMismatch, ErrScopeViolation,
  ErrUnknownSessionType, context.Canceled, context.DeadlineExceeded}`,
  (c) the input bytes do not appear as a substring of the returned
  error's message. Seed corpus: 5 deterministic entries (zero-byte;
  malformed-base64; valid-base64-malformed-JSON; valid-JWT-shape with
  `alg=none`; valid-JWT-shape with `alg=HS256`)
  ([R-011](./research.md#r-011--fuzz-target-fuzzjwtvalidate)).

- **Race target `TestStore_ConcurrentDecrement`** — N=100 goroutines
  call `ConsumeUse` on the same JTI whose `MaxUses` was issued at 100;
  exactly 100 successes observed and the (101)-th call returns
  `ErrTokenExhausted`. The test runs under `-race` via `magex
  test:race`; no race-detector warnings (FR-010 / SC-012)
  ([R-005](./research.md#r-005--token-store-concurrency)).

Exported API (locked at SDD-07; mirrored into `docs/PACKAGE-MAP.md`
once the implement commit lands):

```go
// Package github.com/mrz1836/hush/internal/token

type SessionType string

const (
    SessionInteractive SessionType = "interactive"
    SessionSupervisor  SessionType = "supervisor"
)

type Claims struct {
    jwt.RegisteredClaims
    Scope           []string    `json:"scope"`
    ClientIP        string      `json:"client_ip"`
    RequestID       string      `json:"request_id"`
    MaxUses         int         `json:"max_uses"`
    EphemeralPubKey string      `json:"ephemeral_pubkey"`
    SessionType     SessionType `json:"session_type"`
}

type Token struct {
    JTI         string
    Encoded     string
    ExpiresAt   time.Time
    SessionType SessionType
    MaxUses     int
}

type IssueParams struct {
    Now             time.Time
    TTL             time.Duration
    Scope           []string
    ClientIP        string
    RequestID       string
    MaxUses         int
    EphemeralPubKey string
    SessionType     SessionType
}

type Store interface {
    Add(t *Token) error
    Get(jti string) (*Token, error)
    ConsumeUse(jti string) error
    Revoke(jti string) error
    Cleanup(ctx context.Context)
}

func NewStore() Store
func Issue(ctx context.Context, signKey *ecdsa.PrivateKey, params IssueParams) (*Token, error)
func Validate(ctx context.Context, encoded string, verifyKey *ecdsa.PublicKey, store Store, requestIP string, requestedSecret string) (*Claims, error)

// Sentinel errors — full catalogue and trigger conditions in contracts/api.md.
var ErrAlgorithmUnsupported error
var ErrTokenExpired         error
var ErrTokenRevoked         error
var ErrTokenExhausted       error
var ErrIPMismatch           error
var ErrScopeViolation       error
var ErrUnknownSessionType   error
```

The chunk contract's "Exported API to lock" lists exactly these symbols
in this shape. The plan does not add or rename any. The `IssueParams`
field set is the spec-derived completion of the chunk contract's
ellipsis `IssueParams struct { ... }` — every field above corresponds to
a claim in FR-009 or to the issuance knob the chunk contract names.

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`); `CGO_ENABLED=0`
(Constitution IX).

**Primary Dependencies**:
- Go stdlib: `context`, `crypto/ecdsa`, `crypto/rand`, `crypto/sha256`,
  `encoding/hex`, `errors`, `fmt`, `io`, `net/netip`, `sync`,
  `testing/iotest` (test-only), `time`.
- **NEW direct dependency** (Constitution XI new-dep justification in
  research [R-001](./research.md#r-001--jwt-library-golang-jwtjwtv5)):
  - `github.com/golang-jwt/jwt/v5` — the canonical Go JWT library.
    Maintained successor to `dgrijalva/jwt-go` (which has known CVEs
    that golang-jwt patched at the v4/v5 fork). Zero transitive
    dependencies. The library is permissively-licensed (MIT) and
    actively maintained (≥monthly releases as of 2026-04). Used for:
    JWT compact-form encoding/decoding, the `RegisteredClaims` type
    (which provides the iss/iat/exp/jti round-trip and time-window
    validation), and the `RegisterSigningMethod` registration hook.
- Existing direct dependency:
  `github.com/decred/dcrd/dcrec/secp256k1/v4` (already in `go.mod` from
  SDD-01) — supplies the secp256k1 curve via `*ecdsa.PrivateKey.Curve`.
  The package never imports the decred module directly; the curve is
  reached via the caller-supplied `*ecdsa.PrivateKey`/`*ecdsa.PublicKey`.
- Intra-repo: NONE at runtime. The package is a leaf primitives library —
  it accepts `*ecdsa.PrivateKey` / `*ecdsa.PublicKey` from the caller,
  returns bytes / sentinel errors, and imports nothing from
  `internal/keys`, `internal/vault`, `internal/config`,
  `internal/transport/sign`, `internal/transport/ecies`, or
  `internal/logging` at runtime. Test files MAY import
  `internal/testutil` (SDD-04) for the `SentinelSecret` /
  `AssertSentinelAbsent` helpers and for the deterministic test-key
  helper; they MAY NOT import `internal/keys` at runtime (the package
  operates on raw `*ecdsa.PrivateKey` regardless of derivation
  provenance — fresh keys via `ecdsa.GenerateKey(secp256k1.S256(),
  rand.Reader)` are sufficient for fixtures).

**Storage**: stateful in-process only — the in-memory `Store`
implementation holds `live map[string]*Token` and `revoked
map[string]struct{}` under a `sync.RWMutex`. No disk I/O. No network
I/O. No package-level state beyond the documented sentinel-class
exported `var Err...` declarations and the registration-`sync.Once`
described in approach. A server restart resets the live and revoked
sets — by design, a restart is a revocation of every active session
(`docs/SPEC.md` FR-18, the daily refresh-window prompt and supervisor
TTL recover after restart). Persistent storage of session records is
out of scope for v0.1.0.

**Testing**: `go test ./internal/token/...` (table-driven unit tests
per `.github/tech-conventions/testing-standards.md`); `magex test:race`
race-clean (the `TestStore_ConcurrentDecrement` test is the
load-bearing race-detector exercise); `go test -fuzz=FuzzJWTValidate
-fuzztime=60s ./internal/token/` with no panics and no new corpus
rows representing crashes. Coverage measured via `go test -cover
./internal/token/`; target **100%** per Constitution VIII Critical
band ("Vault crypto, key derivation, JWT, ECIES, request signing").

**Target Platform**: macOS (darwin amd64/arm64) and Linux server
(amd64/arm64) per `.goreleaser.yml`. Windows is out of scope. Zero
platform-conditional code paths — the same source compiles and passes
on every supported `GOOS/GOARCH`.

**Project Type**: Single Go module (`github.com/mrz1836/hush`) with a
flat `internal/<domain>` layout per `docs/PACKAGE-MAP.md`.
`internal/token` is a new package under the existing
`internal/token/` placeholder slot; the chunk contract's "Files:" list
names the production source files (`claims.go`, `issue.go`,
`validate.go`, `store.go`, `revoke.go`, `alg_es256k.go`) plus the test
files. The package is the realisation of `docs/PACKAGE-MAP.md`
§`internal/token` — currently a placeholder ("Filled by SDD-07").

**Performance Goals**:
- `Issue` total wall time: ≤2 ms per call on a 2026-class CPU
  (dominated by ECDSA sign over a 32-byte SHA-256 digest on
  secp256k1, generic-curve path).
- `Validate` total wall time: ≤2 ms per call (header parse +
  signature verify + claims walk + store check; the store check is
  O(1) under the RWMutex).
- `ConsumeUse` under contention (100 goroutines, single JTI):
  ≤50 ms total wall time for the whole burst (the write lock
  serialises the decrements).
- `Cleanup` sweep: O(n) over the live map; for n ≤10 000 active
  tokens, sweep wall time ≤5 ms. The 30 s tick interval is large
  relative to sweep cost, so sweep does not contend with `Validate`
  in steady state.
- `FuzzJWTValidate`: ≥10 k iter/s on a CI runner; the 60 s gate
  exercises ≥600 k randomly-generated JWT-shaped inputs.

**Constraints**:
- **100% test coverage** on `internal/token/` — Constitution VIII
  Critical band. Every documented sentinel + every locked function +
  every spec edge case has at least one named test.
- **Fuzz `FuzzJWTValidate` runs ≥60 s clean** (no panic, no unbounded
  allocation, every error a typed sentinel) per the chunk contract
  and Constitution VIII fuzz target #2 (JWT parse/validate).
- **Zero panics on hostile input.** Random byte streams (any length,
  any content, including streams whose first segment happens to be
  valid base64url-encoded JSON) MUST NOT crash `Validate`.
- **No `init()` function.** ES256K signing-method registration is
  performed via a `sync.Once`-gated `Register()` helper called by
  `Issue` and `Validate` before any JWT work. The `sync.Once` is the
  single bounded constitutional exception (Principle IX), justified
  by the chunk contract.
- **No mutable package-level globals** beyond (a) the seven
  sentinel-class exported `var Err...` declarations and (b) the
  `sync.Once` and the `randReader` test seam (set-once via
  `init()`-equivalent at first call; never mutated after).
- **No goroutines spawned by package code itself.** `Cleanup` is a
  synchronous method; the caller spawns the goroutine that runs it
  (Constitution IX: "every goroutine has a clear owner").
- **No metrics emission, no log lines, no logger dependency.** FR-017
  / Constitution X.
- **No JWT bytes / signing-key bytes / verify-key bytes in any error
  message** (FR-014). Static-message sentinels by construction;
  `TestValidate_NoLeakOnError` is the runtime witness.
- **The package never holds a long-lived secret of its own.** It
  receives the signing/verify keys as parameters, returns either a
  signed encoded JWT (`Token.Encoded`) or a recovered `*Claims`,
  and retains no key material between calls.
- No CGO, no `vendor/`, **zero** goroutine targets (the package
  itself spawns none).

**Scale/Scope**:
- Six production source files (per chunk contract "Files:" list):
  `claims.go` (Claims + SessionType + the SessionType vocabulary
  check helper), `issue.go` (Issue + IssueParams + the JTI
  generator), `validate.go` (Validate + the header-alg inspection
  helper + the claims-vocabulary checks), `store.go` (Store
  interface + memory implementation + `NewStore` + `NewStoreWithTick`
  + the live-map / revoked-set fields under `sync.RWMutex`),
  `revoke.go` (the in-store Revoke method body and the revoked-set
  membership check), `alg_es256k.go` (the ES256K
  `jwt.SigningMethod` implementation + `Register` helper +
  package-level `sync.Once`). Plus an `errors.go` (sentinel error
  declarations colocated for grep-locality, the same locality
  refinement SDD-08, SDD-09, SDD-06, SDD-03, SDD-02, and SDD-01 each
  made — added by the plan as a non-logic source file) and a
  `doc.go` (package-doc with Constitution citations).
- Six test files: `claims_test.go`, `issue_test.go`, `validate_test.go`,
  `store_test.go`, `revoke_test.go`, `alg_es256k_test.go` — plus the
  fuzz file `validate_fuzz_test.go`. Estimated ~600 LOC of production
  Go and ~1 200 LOC of tests.
- Exported surface: 1 string-typed enum (`SessionType`) with 2
  constants, 4 types (`Claims`, `Token`, `IssueParams`, `Store`
  interface), 3 functions (`NewStore`, `Issue`, `Validate`), 7
  sentinel errors. Total exported identifiers: **17**.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principles in scope (per SDD-07)

| Principle | Constraint | Plan compliance |
|-----------|------------|-----------------|
| **III. Defense in Depth — Layer 2 (asymmetric JWT signing)** | Tokens MUST be signed with ES256K (ECDSA over secp256k1, the project's locked asymmetric curve per `docs/SECURITY.md` §5). Only the server can sign; even leaking the verification key MUST NOT enable forgery. The seven JWT claims (iss/iat/exp/jti/scope/client_ip/request_id/max_uses/ephemeral_pubkey/session_type) are load-bearing — see `docs/SECURITY.md` Layer 2 claims table. | `Issue` signs via stdlib `crypto/ecdsa.SignASN1` over `sha256.Sum256(signingInput)` on the caller-supplied `*ecdsa.PrivateKey` (whose curve is secp256k1 by the SDD-01 derivation). The custom ES256K `jwt.SigningMethod` (registered via `sync.Once` from `Register()`) wraps that primitive and the standard JWT compact-form encoder produces the wire form. `Validate` parses, verifies signature against the caller-supplied `*ecdsa.PublicKey`, and refuses any `alg` other than `"ES256K"` (alg-confusion defence). The Claims struct embeds `jwt.RegisteredClaims` for iss/iat/exp/jti and adds the six hush-specific claims as named JSON fields — every field in the Layer-2 claims table is present and named. ✅ |
| **IV. Supervisor for Daemons — TTL discipline** | INTERACTIVE tokens are TTL+max-uses-bounded (default max-uses=50 per `docs/PACKAGE-MAP.md`); SUPERVISOR tokens are TTL-only (max_uses ignored). The two session shapes are distinct, named values; conflating them either pages the operator at 03:00 or trains them to auto-approve. The TTL caps are `max_interactive_session_ttl` and `max_supervisor_session_ttl` (config; SDD-06). | `SessionType` is a string-typed enum with two named constants (`SessionInteractive`, `SessionSupervisor`); any other value triggers `ErrUnknownSessionType`. `Token.MaxUses` is set to 0 for SUPERVISOR; `Store.ConsumeUse` checks `t.SessionType == SessionSupervisor` first and returns `nil` immediately without decrementing — the chunk contract's "supervisor TTL-only" property. The TTL itself comes from `IssueParams.TTL` (which the caller — server.Server — caps via the `Server.MaxInteractiveTTL` / `Server.MaxSupervisorTTL` config knobs from SDD-06; `internal/token` itself does not enforce caps, that is the consumer's job). The `TestIssue_Supervisor` and `TestStore_SupervisorIgnoresMaxUses` tests prove the asymmetry. ✅ |
| **VIII. Testing Discipline — TDD + 100% coverage + fuzz target #2** | Test-first; **100% coverage** for security-critical packages (JWT is named in the Critical band); fuzz target #2 (JWT parse/validate) ≥60 s clean in CI. Every spec FR + every spec SC has at least one named test. Race-detector clean. | The /speckit-tasks-phase prompt (chunk contract Prompt 4) enforces test-first ordering: every behaviour-contract task has a paired test-writing task scheduled BEFORE it. Coverage gate is `go test -cover ./internal/token/` ≥100% in the implement-phase release-step list. Fuzz target `FuzzJWTValidate` is constitutional fuzz target #2 ("JWT parse/validate"); the chunk contract names the 60 s gate. Race target is `TestStore_ConcurrentDecrement` (N=100 goroutines, single JTI, exactly 100 successes observed under `-race`). The 13 named tests from the chunk contract plus `TestValidate_NoLeakOnError` and `TestStore_RevokedSurvivesCleanup` are the floor; tasks.md will expand to at least one test per documented sentinel + one test per spec User Story acceptance scenario + the fuzz target. ✅ |
| **IX. Idiomatic Go Discipline — context first, no init, no globals** | `context.Context` is the first parameter of every function that does cancellable work. **No `init()`.** No mutable package-level globals beyond bounded sentinel-class declarations. Errors wrapped with `%w`. Compare with `errors.Is`. Every goroutine has a clear owner. | `Issue`, `Validate`, and `Store.Cleanup` accept `ctx context.Context` as **first** parameter. **Zero `init()` functions.** The `Register()` helper uses a package-level `sync.Once` invoked from inside `Issue` and `Validate` before any JWT work — the bounded constitutional exception named in the chunk contract. The `randReader` package-level seam defaults to `crypto/rand.Reader` and is set-once at first use; tests swap it via a test-only helper. **Zero goroutines spawned by package code itself** — `Cleanup` is a synchronous method; the caller spawns the goroutine. Errors are sentinel-class; consumers compare via `errors.Is`. The single `ctx.Err()` return path preserves `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)` for FR-015 / SC-011. ✅ |
| **X. Observability & Redaction — no JWT contents in logs/errors** | A secrets-broker that logs the secrets it brokers is worse than no logging at all. The package emits zero log lines; error messages identify failure category and never embed token bytes, signing keys, or verify keys. Type-driven redaction is the standing discipline. | The package emits **zero** log lines and takes **no** logger dependency (FR-017). Diagnostic surface is sentinel-error identity only; error messages are static category strings (`"hush/token: token expired"`, `"hush/token: ip mismatch"`, ...). The `TestValidate_NoLeakOnError` test asserts `testutil.SentinelSecret(2)` (`"SECRET_SHOULD_NEVER_APPEAR_2"`) is absent from `err.Error()` AND from any wrapped-error string descended via `errors.Unwrap` for every rejection category. The `Token.Encoded` field exists for re-verification but is NEVER passed to a formatter or logger by this package. ✅ |

### Other principles (not in scope but checked for non-violation)

- **I (Zero Files at Rest on Agent Machines):** out of scope — this
  package has zero filesystem surface. ✅
- **II (Approval is Human):** out of scope — the approval gate is in
  SDD-11/SDD-12; SDD-07 mints a token only after approval has
  happened. ✅
- **V (Staleness is Visible):** out of scope at the package level — the
  staleness signals are operational surfaces in
  SDD-19/SDD-21/SDD-26. ✅
- **VI (Tailscale-Only):** out of scope at the package level — the
  network boundary is enforced by SDD-06 (config) + SDD-10/SDD-12
  (server middleware). ✅
- **VII (CLI Design Standards):** out of scope — this package defines
  no CLI surface. ✅
- **XI (Native-First, Minimal Dependencies):** **partially in scope**
  — adding `golang-jwt/jwt/v5` is a NEW direct dependency. The
  trusted-sources hierarchy walk (stdlib → sigil → bsv-blockchain →
  wider ecosystem) bottoms out at the wider ecosystem because no
  upstream tier supplies a JWT primitive. The dep is justified in
  research [R-001](./research.md#r-001--jwt-library-golang-jwtjwtv5)
  with maintainer activity, supply-chain provenance, transitive
  footprint (zero transitive deps), and a stdlib-substitution
  argument that fails (no stdlib JWT). The constitutional
  requirement is satisfied (justification written) — no Complexity
  Tracking entry needed because the constitution permits new deps
  with written justification. ✅

### Gate result

**PASS** — every principle in scope is satisfied. The single
new direct dependency (`golang-jwt/jwt/v5`) is justified per
Constitution XI's "written justification" clause; **zero Complexity
Tracking entries**. The Constitution Check is re-evaluated
post-design (after Phase 1) below.

## Project Structure

### Documentation (this feature)

```text
specs/007-token-jwt/
├── plan.md                  # This file (/speckit-plan command output)
├── research.md              # Phase 0 output — locked HOW decisions
├── data-model.md            # Phase 1 output — claims layout + sentinel catalogue + lifecycle
├── quickstart.md            # Phase 1 output — consumer integration recipe (SDD-12 / SDD-13 / SDD-23)
├── contracts/
│   └── api.md               # Phase 1 output — exported API contract (locks PACKAGE-MAP §internal/token)
├── checklists/              # Pre-existing (untouched by /speckit-plan)
├── spec.md                  # WHAT contract (already written by /speckit-specify + /speckit-clarify)
└── tasks.md                 # Phase 2 output (/speckit-tasks command — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/token/
├── doc.go                       # Package doc: Constitution III/IV/VIII/IX/X citations + Layer-2 roster
├── claims.go                    # Claims + SessionType + vocabulary check helper
├── issue.go                     # Issue + IssueParams + JTI generator (crypto/rand → UUIDv4)
├── validate.go                  # Validate + header-alg inspection + claims walk
├── store.go                     # Store interface + memory impl + NewStore + NewStoreWithTick + Cleanup
├── revoke.go                    # In-store Revoke method body and revoked-set membership check
├── alg_es256k.go                # ES256K jwt.SigningMethod + Register() + sync.Once
├── errors.go                    # Sentinel error declarations (7 sentinels, static messages)
├── claims_test.go               # SessionType vocabulary + Claims round-trip tests
├── issue_test.go                # TestIssue_Interactive + TestIssue_Supervisor + JTI uniqueness + ctx-cancel
├── validate_test.go             # 13+ named validate tests covering every rejection category + alg-confusion
├── store_test.go                # TestStore_ConcurrentDecrement + TestStore_RevokedJTI_Refused + cleanup tests
├── revoke_test.go               # Revocation persistence + revoked-set survives cleanup
├── alg_es256k_test.go           # ES256K signing-method round-trip + register-once concurrency
├── validate_fuzz_test.go        # FuzzJWTValidate + 5-file seed corpus loader
└── testdata/
    └── fuzz/
        └── FuzzJWTValidate/
            ├── empty                        # zero-byte input
            ├── malformed-base64             # garbage that doesn't base64url-decode
            ├── valid-base64-bad-json        # decodes to non-JSON bytes
            ├── alg-none                     # valid JWT shape, alg="none" header
            └── alg-hs256                    # valid JWT shape, alg="HS256" header

go.mod                           # +1 direct dep: github.com/golang-jwt/jwt/v5
go.sum                           # updated for golang-jwt/jwt/v5 + its zero transitive deps
```

**Structure Decision**: hush is a single Go module
(`github.com/mrz1836/hush`) with a flat `internal/<domain>` layout
defined in `docs/PACKAGE-MAP.md`. SDD-07 fills the existing
`internal/token/` placeholder slot. The package ships eight
production source files; the chunk contract's "Files:" list named
six (`claims.go`, `issue.go`, `validate.go`, `store.go`, `revoke.go`,
`alg_es256k.go`). The plan adds two: `errors.go` (sentinel error
declarations colocated for grep-locality, the same locality
refinement SDD-08, SDD-09, SDD-06, SDD-03, SDD-02, and SDD-01 each
made under the same constitutional reading) and `doc.go` (package-level
doc comment with Constitution citations). The chunk-contract file list
is read as the **minimum** set: every file the contract names is
present, and the package may add purely declarative files where
idiomatic Go discipline calls for them. No production logic is added
beyond what the chunk contract describes.

The package import path is `github.com/mrz1836/hush/internal/token`.
Per `docs/PACKAGE-MAP.md` §`internal/token`, this is a leaf primitives
library: the allowed dependency direction is `internal/server →
internal/token` (SDD-12 will consume `Issue`, SDD-13 will consume
`Validate` + `Store.ConsumeUse` + `Store.Revoke`) and `internal/cli →
internal/token` (SDD-23 will consume the supervisor session-retention
path indirectly via the `internal/server` claim handler). SDD-07 itself
imports nothing intra-repo at runtime; test files import
`internal/testutil` (SDD-04) for the sentinel-leak helpers and may use
`ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)` directly for fixture
keys.

The `testdata/fuzz/FuzzJWTValidate/` directory ships five seed files
matching the spec's edge cases (zero-byte, malformed-base64,
valid-base64-bad-json, alg=none, alg=HS256) so the 60 s CI fuzz gate
exercises the whole `Validate` surface from the first run rather than
spending most of its budget bouncing off the parse early-out.

Tests use `internal/testutil` (SDD-04) for the
`SECRET_SHOULD_NEVER_APPEAR_2` sentinel and the
`AssertSentinelAbsent(t, sentinel, haystack)` helper. Test keys are
generated fresh per test run via `ecdsa.GenerateKey(secp256k1.S256(),
rand.Reader)` — there is no need to reuse `internal/keys.DeriveJWTSigningKey`
for fixture keys (the package operates on raw `*ecdsa.PrivateKey` /
`*ecdsa.PublicKey`, regardless of derivation provenance), and using
fresh keys keeps each test independent.

## Post-Design Constitution Re-check

Re-evaluated after Phase 1 design artifacts (`research.md`,
`data-model.md`, `contracts/api.md`, `quickstart.md`) were drafted:

| Principle | Phase 1 introduced | Re-check |
|-----------|--------------------|----------|
| **III** | [data-model.md](./data-model.md) formalises the Claims layout (10 named claims), the JWT compact-form layout (header.payload.signature with ES256K alg literal), and the per-call lifecycle showing every claim populated by Issue and verified by Validate. [contracts/api.md](./contracts/api.md) documents the failure-mode collapse rule (FR-007 — every distinct rejection category has its own sentinel; the alg-confusion umbrella covers BOTH "none" and "HS256" as documented edge cases). [quickstart.md](./quickstart.md) shows the SDD-12 server-side `Issue` recipe, the SDD-13 `Validate`-then-`ConsumeUse` recipe, and the SDD-23 supervisor-session-retention recipe. The Layer-2 contract is enforced by claim-shape and sign/verify discipline, not by additional in-band guards. | PASS — the Layer-2 contract is enforced by claim-shape and signature primitive choice. |
| **IV** | Phase 1 confirmed: SessionType is a string enum with two constants; SUPERVISOR sessions skip `ConsumeUse` decrement entirely; the asymmetry is testable via `TestStore_SupervisorIgnoresMaxUses`. The TTL caps live in the consumer (server.Server) per SDD-06 — `internal/token` does NOT enforce TTL upper bounds, only the claim's expiration time. | PASS — TTL discipline is enforced at the right layer. |
| **VIII** | [contracts/api.md](./contracts/api.md) enumerates 18+ named tests across the seven test files plus five fuzz seed entries. Coverage gate is 100% per the Critical band. The fuzz target ships with a deterministic 5-file seed corpus so CI's first run is meaningful. | PASS — every spec FR and every spec SC has at least one named test. |
| **IX** | Phase 1 confirmed: zero `init()`, the bounded `sync.Once` constitutional exception is documented and limited to ES256K signing-method registration, all primitives ctx-first, zero goroutines spawned by package code (Cleanup is a synchronous method whose caller owns the goroutine), errors returned by sentinel identity (no `%w` wrap inside the package — consumers compare via `errors.Is` directly against the exported sentinels). The `randReader` test seam is set-once at first use; tests swap it via a test-only helper that captures and restores. The single `ctx.Err()` return path preserves `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)`. | PASS — no new violations introduced. |
| **X** | The error catalogue is finalised. Every sentinel's identity is the failure category; no error message embeds JWT, signing-key, or verify-key bytes. The `TestValidate_NoLeakOnError` test scaffolding is in the test floor. | PASS — diagnostic surfaces audited and clean. |
| **XI** | One new direct dependency (`github.com/golang-jwt/jwt/v5`) with full written justification in research [R-001](./research.md#r-001--jwt-library-golang-jwtjwtv5). The trusted-sources hierarchy walk is documented; transitive dependency footprint is zero; supply-chain provenance is positive (active maintainer, MIT license, ≥monthly releases). The chunk contract's `github.com/bitcoinschema/go-bitcoin` reference for ECDSA sign/verify is honored at the *intent* level via the same stdlib substitution SDD-08 made — see research [R-002](./research.md#r-002--es256k-signing-method-stdlib-substitution). The PR description for the implement commit will document both the new dep and the stdlib-substitution choice verbatim. | PASS — new dep is justified per the constitution; substitution is constitutionally-aligned, not a deviation. |

**Final result**: PASS. **No Complexity Tracking entries.** The single
new direct dependency is justified per Constitution XI's "written
justification" clause. The Layer-2 contract is enforced via
claim-shape, signature-primitive choice, and the deterministic
validation ordering documented in §"Approach" above.

## Complexity Tracking

> Fill ONLY if Constitution Check has violations that must be justified.

*(empty — no constitutional deviations)*
