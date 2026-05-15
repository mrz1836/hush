# Contract: `internal/token` exported API

**Feature**: 007-token-jwt
**Status**: Locked at SDD-07; mirrored into `docs/PACKAGE-MAP.md` once
the implement commit lands.

This is the contract every downstream package (SDD-12 server `/claim`
handler, SDD-13 server `/secrets/{name}` and `/revoke/{jti}` handlers,
SDD-23 `hush supervise` session-retention) depends on. Changes after
SDD-07 lands require a new SDD chunk; consumers may rely on every
signature, every sentinel identity, and the Validate-then-ConsumeUse
ordering below.

SDD-07 (`internal/token`) is a leaf primitives library — no intra-repo
runtime imports. Future token-adjacent helpers (e.g., a JTI prefix-search
helper for audit-log triage) MAY land as additional symbols in this
package without breaking the existing contract; symbol REMOVAL or
signature CHANGES require a new SDD chunk.

---

## Package path

```
github.com/mrz1836/hush/internal/token
```

---

## Exported types

### `type SessionType string`

```go
type SessionType string

const (
    SessionInteractive SessionType = "interactive"
    SessionSupervisor  SessionType = "supervisor"
)
```

Closed vocabulary. Any other value (including the empty string) is
treated as unknown and triggers `ErrUnknownSessionType` in both
`Issue` and `Validate`.

The two values are the package's authoritative session-type vocabulary
per FR-004. Adding a third value (e.g., `SessionDelegated`) is a
non-breaking change but requires a new SDD chunk to define the
lifecycle rules of the new shape.

### `type Claims struct`

```go
type Claims struct {
    jwt.RegisteredClaims
    Scope           []string    `json:"scope"`
    ClientIP        string      `json:"client_ip"`
    RequestID       string      `json:"request_id"`
    MaxUses         int         `json:"max_uses"`
    EphemeralPubKey string      `json:"ephemeral_pubkey"`
    SessionType     SessionType `json:"session_type"`
}
```

The recovered claim set returned by `Validate` on success. Embeds
`jwt.RegisteredClaims` for the standard JWT fields (`Issuer`,
`IssuedAt`, `ExpiresAt`, `ID` — the JTI). The six hush-specific
fields are JSON-tagged for stable wire encoding.

Consumers MAY read but MUST NOT mutate the returned `*Claims` (the
package does not retain a reference, but mutating the recovered
claims invalidates downstream audit-log assumptions). For
defensive programming, treat the returned `*Claims` as read-only.

### `type Token struct`

```go
type Token struct {
    JTI         string
    Encoded     string
    ExpiresAt   time.Time
    SessionType SessionType
    MaxUses     int
}
```

The in-store record returned by `Issue`. Consumers (the claim
handler) call `Store.Add(token)` to register it for future
validation. The `Encoded` field is the wire-form JWT compact string
the client receives in the `/claim` response.

The `*Token` returned by `Issue` is a fresh allocation; consumers
own its lifetime. The same pointer is held by the store after
`Add`; consumers MUST NOT mutate the `*Token` after `Add` (race
with `ConsumeUse`).

### `type IssueParams struct`

```go
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
```

Issuance knobs. All fields except `MaxUses` (for `SessionSupervisor`)
are required.

| Field             | Required | Validation rule                                          |
|-------------------|----------|----------------------------------------------------------|
| `Now`             | yes      | non-zero `time.Time`                                      |
| `TTL`             | yes      | positive `time.Duration`                                  |
| `Scope`           | yes      | non-empty slice; every entry non-empty string             |
| `ClientIP`        | yes      | parses via `netip.ParseAddr` to a non-zero address        |
| `RequestID`       | yes      | non-empty string                                           |
| `MaxUses`         | INTERACTIVE: yes; SUPERVISOR: ignored | INTERACTIVE: positive int; SUPERVISOR: zeroed by Issue |
| `EphemeralPubKey` | yes      | non-empty string (format opaque to package)                |
| `SessionType`     | yes      | `SessionInteractive` or `SessionSupervisor`                |

The caller (claim handler, SDD-12) is responsible for enforcing TTL
caps from the server config (`max_interactive_session_ttl`,
`max_supervisor_session_ttl` from SDD-06). The package itself does
NOT cap TTL — `IssueParams.TTL` is consumed verbatim.

### `type Store interface`

```go
type Store interface {
    Add(t *Token) error
    Get(jti string) (*Token, error)
    ConsumeUse(jti string) error
    Revoke(jti string) error
    Cleanup(ctx context.Context)
}
```

The session-state repository. `NewStore()` returns the in-memory
implementation; consumers MAY supply their own implementation
(e.g., a future Redis-backed store) but the in-memory version is
the only one shipped in v0.1.0 and the only one validated under
the `TestStore_*` suite.

| Method        | Returns                  | Behavior                                                                                       |
|---------------|--------------------------|------------------------------------------------------------------------------------------------|
| `Add(t)`      | `error` (nil on success) | Inserts `t` under `live[t.JTI]`. Returns `ErrTokenRevoked` if the JTI is in the revoked set.   |
| `Get(jti)`    | `(*Token, error)`         | Returns the live record, or `ErrTokenRevoked` if jti is revoked or unknown.                    |
| `ConsumeUse(jti)` | `error`                | INTERACTIVE: decrements MaxUses by 1 atomically; returns `ErrTokenExhausted` at 0. SUPERVISOR: returns nil without decrementing. Always checks revoked / expired first. |
| `Revoke(jti)` | `error` (always nil)     | Marks jti revoked permanently for the lifetime of the store; idempotent.                        |
| `Cleanup(ctx)` | (none)                   | Synchronous tick loop that removes expired live records on each tick; returns when ctx fires.   |

`Store.Cleanup` MUST be invoked from a caller-owned goroutine (per
Constitution IX — see [research R-009](../research.md#r-009--cleanup-goroutine-ownership));
the package does NOT spawn the goroutine.

The in-memory implementation is concurrent-safe via `sync.RWMutex`;
all five methods may be called from any goroutine.

---

## Exported functions

### `func NewStore() Store`

```go
func NewStore() Store
```

Returns a fresh in-memory `Store` with a 30-second `Cleanup` tick
interval and a `time.Now`-based clock. Consumers MUST eventually call
`Cleanup(ctx)` from a goroutine to reclaim expired records;
otherwise the live map grows without bound for the server's uptime.

The package also exposes (test seam, public-but-unstable):

```go
func NewStoreWithTick(d time.Duration) Store // tick = d, nowFn = time.Now
```

Tests use this for deterministic sweep observation (e.g., `tick =
1*time.Millisecond`). Production wires `NewStore()`.

### `func Issue(ctx, signKey, params) (*Token, error)`

```go
func Issue(ctx context.Context, signKey *ecdsa.PrivateKey, params IssueParams) (*Token, error)
```

**Inputs**:
- `ctx` — checked once at entry; pre-cancellation returns `ctx.Err()`
  immediately. Mid-operation cancellation is NOT honored (signing is
  short and CPU-bound).
- `signKey` — a `*ecdsa.PrivateKey` whose curve is secp256k1.
  Origin: the server's JWT signing key, derived once at startup via
  `keys.DeriveJWTSigningKey` (SDD-01).
- `params` — issuance knobs (see `IssueParams` above).

**Output**:
- On success: a fresh `*Token` whose `JTI` is a newly-generated
  UUIDv4, `Encoded` is the signed compact-form JWT, `ExpiresAt` is
  `params.Now.Add(params.TTL)`, `SessionType` matches
  `params.SessionType`, and `MaxUses` is `params.MaxUses` for
  INTERACTIVE or 0 for SUPERVISOR.
- On failure: `nil, err`.

**Failure cases**:
- `ctx.Err() != nil` → `ctx.Err()` returned verbatim.
- `params.SessionType` not in vocabulary → `ErrUnknownSessionType`.
- Any other invalid `IssueParams` field → `ErrAlgorithmUnsupported`
  (the umbrella for "input cannot be signed" — see [research R-008](../research.md#r-008--issueparams-validation)).

**Side effects (intentional)**:
- Reads `crypto/rand.Reader` to generate the JTI.
- Calls `Register()` (sync.Once-gated; first call registers the
  ES256K signing method with the JWT library).

**Side effects (explicitly NOT done)**:
- Does NOT mutate `IssueParams` (the caller's struct is read-only
  from the package's perspective).
- Does NOT call `Store.Add` — the caller is responsible.
- Does NOT log anything.
- Does NOT spawn goroutines.

**Concurrency**: safe to call concurrently. Multiple goroutines may
invoke `Issue` simultaneously without synchronisation.

**Determinism**: `Issue` is **non-deterministic** — each call
generates a fresh JTI, so two calls with identical `IssueParams`
produce two different `*Token`s with two different `Encoded` strings.

### `func Validate(ctx, encoded, verifyKey, store, requestIP, requestedSecret) (*Claims, error)`

```go
func Validate(
    ctx context.Context,
    encoded string,
    verifyKey *ecdsa.PublicKey,
    store Store,
    requestIP string,
    requestedSecret string,
) (*Claims, error)
```

**Inputs**:
- `ctx` — checked once at entry; pre-cancellation returns `ctx.Err()`.
- `encoded` — the compact-form JWT (`header.payload.signature`).
- `verifyKey` — the server's JWT verify key (the public half of the
  signing key from SDD-01).
- `store` — the session store; `Validate` consults it for revocation
  status and use-count decrement.
- `requestIP` — the requesting client's IP (resolved by middleware
  from the underlying TCP connection — NOT from `X-Forwarded-For`).
- `requestedSecret` — the secret name the caller wants. Empty string
  is treated as a wildcard for `/revoke` (the consumer is expected
  to supply a sentinel like `"*"` or `""` for non-fetch validations).

**Output**:
- On success: a fresh `*Claims` whose fields match the issuance
  parameters of the validated token.
- On failure: `nil, err`.

**Failure cases**:
- `ctx.Err() != nil` → `ctx.Err()` verbatim.
- Header `alg` is not `"ES256K"` (incl. `"none"`, `"HS256"`,
  malformed) → `ErrAlgorithmUnsupported`. Fires BEFORE the keyfunc
  is consulted.
- Signature does not verify → `ErrAlgorithmUnsupported` (the FR-007 #1
  umbrella; collapsed with alg-confusion by design).
- Token expired (`exp <= now`) → `ErrTokenExpired`.
- `Claims.SessionType` ∉ vocabulary → `ErrUnknownSessionType`.
- `requestedSecret` ∉ `Claims.Scope` → `ErrScopeViolation`.
- `Claims.ClientIP` ≠ `requestIP` (semantic netip.Addr equality) →
  `ErrIPMismatch`.
- JTI revoked → `ErrTokenRevoked`.
- INTERACTIVE token with `MaxUses == 0` → `ErrTokenExhausted`.

The validation order is fixed (see
[data-model §2.3](../data-model.md#23-verification-ordering-decrypt));
each step's failure surfaces a distinct sentinel.

**Side effects (intentional)**:
- For an INTERACTIVE token that passes every other check,
  `Validate` calls `Store.ConsumeUse(jti)` which atomically
  decrements the use count. The decrement happens BEFORE
  `Validate` returns success — so `Validate` returning success is
  the contract that the use was consumed (FR-005).
- For a SUPERVISOR token, `Validate` calls `Store.ConsumeUse(jti)`
  which validates revocation/expiry but does NOT decrement.
- Calls `Register()` (sync.Once).

**Side effects (explicitly NOT done)**:
- Does NOT mutate `encoded` (read-only).
- Does NOT call `Store.Add` or `Store.Revoke`.
- Does NOT log anything.
- Does NOT spawn goroutines.
- Does NOT cache the verify key across calls.

**Sentinel matching**: every failure surfaces as one of the seven
exported sentinels (or `context.Canceled` / `context.DeadlineExceeded`
for cancellation). Consumers compare via `errors.Is`.

**Panic-free**: `Validate` is panic-free under any string input
(asserted by `FuzzJWTValidate` 60 s gate).

**Concurrency**: safe to call concurrently with itself, with
`Issue`, and with all `Store` methods.

---

## Sentinel error catalogue

All sentinels are
`var Err... = errors.New("hush/token: <message>")` declarations.
Compare via `errors.Is`. **No wrap relationships** between sentinels —
each category is independent.

| Sentinel                  | Static message                          | Triggered by                                                                                        |
|---------------------------|-----------------------------------------|-----------------------------------------------------------------------------------------------------|
| `ErrAlgorithmUnsupported` | `hush/token: algorithm unsupported`     | Header `alg` ≠ "ES256K" (incl. "none", "HS256", malformed); signature-class verification failure; IssueParams validation failure (umbrella for "input cannot be signed"). |
| `ErrTokenExpired`         | `hush/token: token expired`             | `Claims.ExpiresAt.Before(now)` (mapped from `jwt.ErrTokenExpired`); `Store.ConsumeUse` against an expired live record. |
| `ErrTokenRevoked`         | `hush/token: token revoked`             | JTI in revoked set; `Store.Add` on JTI collision with revoked-set; `Store.Get`/`Store.ConsumeUse` on unknown JTI. |
| `ErrTokenExhausted`       | `hush/token: token exhausted`           | `Store.ConsumeUse` for an INTERACTIVE token whose `MaxUses == 0`. NEVER returned for SUPERVISOR.    |
| `ErrIPMismatch`           | `hush/token: ip mismatch`               | `compareIPs(Claims.ClientIP, requestIP)` returns non-equal; either input fails to parse via `netip.ParseAddr`. |
| `ErrScopeViolation`       | `hush/token: scope violation`           | `requestedSecret` not present in `Claims.Scope`.                                                    |
| `ErrUnknownSessionType`   | `hush/token: unknown session type`      | `Claims.SessionType` ∉ `{SessionInteractive, SessionSupervisor}`; `IssueParams.SessionType` ∉ vocabulary. |

The error messages are static strings known at compile time; the
package never formats encoded JWT bytes, signing-key bytes, or
verify-key bytes into any error. The `TestValidate_NoLeakOnError`
test (mandatory; in the test floor) asserts the project sentinel
`SECRET_SHOULD_NEVER_APPEAR_2` is absent from `err.Error()` AND from
any wrap-chain descended via `errors.Unwrap`.

---

## Behavioural invariants (testable contract)

| Invariant | Spec ref | Test name (in tasks phase) |
|-----------|----------|----------------------------|
| Round-trip recovers Claims for INTERACTIVE | FR-001, FR-005, SC-001 | `TestIssue_Interactive` (and `TestValidate_HappyPath`) |
| Round-trip recovers Claims for SUPERVISOR | FR-001, FR-006, SC-001 | `TestIssue_Supervisor` |
| `Issue` produces distinct JTIs per call | FR-008 | `TestIssue_FreshJTIPerCall` |
| `Issue` rejects zero `IssueParams.SessionType` | FR-004 | `TestIssue_RejectsUnknownSessionType` |
| `Issue` rejects invalid `IssueParams.TTL` | FR-009 / R-008 | `TestIssue_RejectsInvalidParams` |
| `Issue` zeroes `MaxUses` for SUPERVISOR | FR-006 | `TestIssue_SupervisorZeroesMaxUses` |
| `Validate` rejects alg=none with `ErrAlgorithmUnsupported` | FR-002, SC-005 | `TestValidate_AlgConfusion_None_Refused` |
| `Validate` rejects alg=HS256 with `ErrAlgorithmUnsupported` | FR-003, SC-006 | `TestValidate_AlgConfusion_HS256_Refused` |
| `Validate` rejects expired JWT with `ErrTokenExpired` | FR-007 #1 | `TestValidate_ExpiredJWT` |
| `Validate` rejects mismatched IP with `ErrIPMismatch` | FR-007 #3 | `TestValidate_WrongIP` |
| `Validate` accepts semantically-equivalent IPs | FR-016 | `TestValidate_IPSemanticallyEqual` |
| `Validate` rejects out-of-scope secret with `ErrScopeViolation` | FR-007 #2 | `TestValidate_OutOfScope` |
| `Validate` rejects unknown session type with `ErrUnknownSessionType` | FR-007 #6 | `TestValidate_UnknownSessionType_Refused` |
| `Validate` rejects revoked JTI with `ErrTokenRevoked` | FR-011 | `TestStore_RevokedJTI_Refused` |
| `Validate` decrements INTERACTIVE on success | FR-005 | `TestValidate_DecrementsInteractive` |
| `Validate` rejects exhausted INTERACTIVE with `ErrTokenExhausted` | FR-007 #5 | `TestStore_ExhaustedInteractive_Refused` |
| `Validate` does NOT decrement SUPERVISOR | FR-006 | `TestStore_SupervisorIgnoresMaxUses` |
| `Store.Cleanup` removes expired live records | FR-012 | `TestStore_CleanupRemovesExpired` |
| `Store.Cleanup` does NOT clear revoked entries | FR-011, FR-012 | `TestStore_RevokedSurvivesCleanup` |
| Concurrent `ConsumeUse` produces exactly N successes (race-clean) | FR-010, SC-012 | `TestStore_ConcurrentDecrement` |
| Sentinel `SECRET_SHOULD_NEVER_APPEAR_2` absent from any error message | FR-014, SC-009, SC-013 | `TestValidate_NoLeakOnError` |
| `Validate` is panic-free under fuzz pressure (≥60 s) | FR-012 spec, SC-008 | `FuzzJWTValidate` (60 s CI gate) |
| Pre-cancelled context returns `ctx.Err()` (errors.Is matches) | FR-015, SC-011 | `TestIssue_RespectsCancelledContext`, `TestValidate_RespectsCancelledContext` |
| ES256K registration is idempotent under concurrency | IX bounded exception | `TestRegisterOnce_Concurrent` |
| Coverage of `internal/token` is 100% | Constitution VIII Critical band | enforced by `codecov.yml` |

---

## Composition recipes

The package's two free functions and the `Store` interface compose
as follows. Two canonical recipes:

### Server `/claim` handler (SDD-12)

```go
import "github.com/mrz1836/hush/internal/token"

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // 1. Parse + signature-verify + nonce-check the inbound /claim
    //    request via internal/transport/sign (SDD-08). Out of scope here.
    claimReq := decodeAndVerifyClaim(ctx, r, ...)

    // 2. Discord approval (SDD-11). Out of scope here.
    approval := awaitDiscordApproval(ctx, claimReq)
    if approval.Decision != ApprovalApproved {
        return // 403 with audit-log entry
    }

    // 3. Build IssueParams from the approval + request.
    params := token.IssueParams{
        Now:             time.Now(),
        TTL:             approval.TTL,
        Scope:           claimReq.Scope,
        ClientIP:        clientIP(r),
        RequestID:       claimReq.RequestID,
        MaxUses:         s.config.DefaultMaxUses, // ignored if SUPERVISOR
        EphemeralPubKey: claimReq.EphemeralPubKey,
        SessionType:     claimReq.SessionType,
    }

    // 4. Issue.
    tok, err := token.Issue(ctx, s.signKey, params)
    if err != nil {
        // Map by sentinel — never log encoded bytes.
        respondError(w, err)
        return
    }

    // 5. Register in the store.
    if err := s.tokenStore.Add(tok); err != nil {
        respondError(w, err) // ErrTokenRevoked on JTI collision (vanishingly unlikely)
        return
    }

    // 6. Return the encoded JWT to the client.
    respondJSON(w, http.StatusOK, ClaimResponse{
        Token:     tok.Encoded,
        ExpiresAt: tok.ExpiresAt,
        JTI:       tok.JTI,
    })
}
```

### Server `/secrets/{name}` handler (SDD-13)

```go
import (
    "errors"
    "github.com/mrz1836/hush/internal/token"
)

func (s *Server) handleSecretFetch(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    encoded := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
    requestIP := s.clientIP(r) // resolved by middleware
    secretName := chi.URLParam(r, "name")

    claims, err := token.Validate(ctx, encoded, s.verifyKey, s.tokenStore, requestIP, secretName)
    if err != nil {
        switch {
        case errors.Is(err, token.ErrAlgorithmUnsupported),
             errors.Is(err, token.ErrTokenExpired),
             errors.Is(err, token.ErrTokenRevoked),
             errors.Is(err, token.ErrTokenExhausted):
            respondStatus(w, http.StatusUnauthorized) // 401
        case errors.Is(err, token.ErrIPMismatch),
             errors.Is(err, token.ErrScopeViolation),
             errors.Is(err, token.ErrUnknownSessionType):
            respondStatus(w, http.StatusForbidden) // 403
        default:
            respondStatus(w, http.StatusInternalServerError) // 500 — context, etc.
        }
        s.audit.Record(AuthFailedEvent{JTI: extractJTI(encoded), Sentinel: err}) // SDD-25
        return
    }

    // At this point: signature verified, claims valid, in-scope, IP matches,
    // not revoked, use decremented (interactive only). Now reach into the vault.
    sb, err := s.vault.Get(secretName)
    if err != nil {
        respondError(w, err)
        return
    }
    defer sb.Destroy()

    // ECIES-encrypt sb to claims.EphemeralPubKey via internal/transport/ecies (SDD-09).
    // ... write envelope to response.
}
```

### Store cleanup goroutine (SDD-10 server bootstrap)

```go
func (s *Server) Run(ctx context.Context) error {
    // ... server setup ...
    go s.tokenStore.Cleanup(ctx) // owned by Run; terminates when ctx fires
    return s.httpServer.Serve(...)
}
```

The `go store.Cleanup(ctx)` is the **only** package-related goroutine
the server spawns for token bookkeeping. The chunk-contract pattern
"Cleanup runs in a caller-controlled goroutine via Store.Cleanup(ctx)"
is satisfied by this single line.

---

## Non-contract (explicit non-promises)

- **No streaming variant.** `Issue` and `Validate` operate on
  whole-buffer inputs (the Encoded field is a string). A streaming
  variant would not make sense for compact-form JWTs.
- **No exported helpers for the JWT primitives.** `Issue`, `Validate`,
  `NewStore` are the only entry points. Consumers MUST NOT reach into
  the package for the signing-method, the JTI generator, or the
  `validateAlgorithm` helper — those are file-private and may change.
- **No `IsBearer(s string) bool` predicate.** The package does not
  parse `Authorization: Bearer ...` headers; that is the consumer's
  responsibility.
- **No support for non-ES256K algorithms.** Constitution III pins
  ES256K; passing a verify key on a different curve produces
  `ErrAlgorithmUnsupported` (signature class umbrella).
- **No environment-variable reads, no file I/O, no network I/O.** The
  package is purely in-memory (modulo the entropy source
  `crypto/rand.Reader`).
- **No package-level metrics.** Constitution X forbids; consumers
  derive operational counters from sentinel-error identity.
- **No JWT / signing-key / verify-key byte appearance in any error
  message.** FR-014.
- **No log lines and no logger dependency.** FR-017.
- **No upper bound on JWT size beyond what the underlying library
  enforces.** Consumers that need a hard cap enforce it themselves
  (e.g., reject `Authorization` headers above 4 KB).
- **No deterministic Issue mode.** Issue is randomised by design (fresh
  JTI per call); consumers that need determinism for testing inject
  the package-private `randReader` seam via the test-only helper.
- **No on-disk state.** The package writes nothing to disk.
- **No goroutines spawned by the package itself.** `Cleanup` is a
  synchronous method whose caller owns the goroutine.
- **No cache or memoisation of verify keys.** The keyfunc the package
  passes to `jwt.ParseWithClaims` simply returns the caller's
  `verifyKey` for every call.
- **No persistence across server restarts.** A restart drops the
  in-memory `live` and `revoked` maps; existing sessions die.
- **No cross-store revocation.** `Store.Revoke` is in-process only;
  a hypothetical multi-vault deployment would require a shared
  revocation list (out of scope for v0.1.0).
- **No automatic TTL clamping.** `IssueParams.TTL` is consumed
  verbatim; the caller (claim handler) clamps to config-defined
  upper bounds.

---

## Deprecation policy

This contract is **frozen** at SDD-07 ship. Adding a new sentinel
error to the catalogue is non-breaking iff the new sentinel surfaces
a new failure category that current consumers do not depend on the
absence of. Removing or renaming any exported symbol requires a new
SDD chunk; SDD-12, SDD-13, and SDD-23 all depend on the surface above.

Adding a new exported function (e.g., a future
`ValidateForRevoke(...)` that skips the scope check) is non-breaking
and may land via a separate chunk. Any change to the JWT
compact-form layout or the claim names is a wire-format break and
would require a coordinated SDD-12 + SDD-13 + SDD-23 update plus a
deprecation notice on this contract.

Adding a third `SessionType` value is non-breaking iff the new value
ships with documented lifecycle rules and at least one test
demonstrating its behaviour distinct from the existing two values.
A third session shape would also require a new SDD chunk to define
the supervisor/interactive distinctions.
