# Phase 1 Data Model: `internal/token`

**Feature**: 007-token-jwt
**Date**: 2026-04-29

This document captures the types, the JWT compact-form layout, the
per-call lifecycle, the sentinel-error catalogue, and the invariants
that downstream consumers (SDD-12, SDD-13, SDD-23) depend on. The
package holds in-process state (the `Store`) but writes nothing to
disk and emits zero log lines — so this file is the static contract
of types-in-memory, claims-on-the-wire, and store-state-transitions.

---

## 1. Types (locked at SDD-07)

### 1.1 Public types

| Type             | Source                                               | Why this shape                                                                                              |
|------------------|------------------------------------------------------|-------------------------------------------------------------------------------------------------------------|
| `SessionType`    | this package, `string`-typed enum                    | Closed vocabulary (`SessionInteractive`, `SessionSupervisor`); third value triggers `ErrUnknownSessionType` |
| `Claims`         | this package, struct                                 | Embeds `jwt.RegisteredClaims` for iss/iat/exp/jti; adds 6 hush-specific claims                              |
| `Token`          | this package, struct                                 | In-store record: JTI, Encoded, ExpiresAt, SessionType, MaxUses                                              |
| `IssueParams`    | this package, struct                                 | Issuance knobs (Now, TTL, Scope, ClientIP, RequestID, MaxUses, EphemeralPubKey, SessionType)                |
| `Store`          | this package, interface                              | 5 methods: Add, Get, ConsumeUse, Revoke, Cleanup                                                            |
| `*ecdsa.PrivateKey` | `crypto/ecdsa` (stdlib)                            | The signing key (caller derives via `keys.DeriveJWTSigningKey` from SDD-01)                                  |
| `*ecdsa.PublicKey` | `crypto/ecdsa` (stdlib)                             | The verify key (the public half of the above)                                                               |
| `error`          | stdlib                                                | Returned via the sentinel-error catalogue (§4)                                                              |
| `context.Context` | stdlib                                              | Cancellation surface (FR-015)                                                                               |

### 1.2 Public type definitions

```go
type SessionType string

const (
    SessionInteractive SessionType = "interactive"
    SessionSupervisor  SessionType = "supervisor"
)

type Claims struct {
    jwt.RegisteredClaims                 // Issuer, IssuedAt, ExpiresAt, ID (= JTI)
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
```

### 1.3 File-private types and constants

| Symbol            | Kind     | Purpose                                                                                       |
|-------------------|----------|-----------------------------------------------------------------------------------------------|
| `es256kMethod`    | `struct` | Implements `jwt.SigningMethod` for ES256K — delegates to stdlib `crypto/ecdsa.SignASN1` / `VerifyASN1` over `sha256.Sum256(signingInput)` |
| `registerOnce`    | `var sync.Once` | Gates `jwt.RegisterSigningMethod("ES256K", ...)` to one-shot semantics                          |
| `randReader`      | `var io.Reader` | Test seam for JTI generation; defaults to `crypto/rand.Reader`                                  |
| `memStore`        | `struct` | The in-memory `Store` implementation (`sync.RWMutex` + `live map[string]*Token` + `revoked map[string]struct{}` + `tick time.Duration` + `nowFn func() time.Time`) |
| `defaultTick`     | `const time.Duration` | `30 * time.Second` — the default `Cleanup` tick interval                                  |
| `Register`        | `func()` | Public-internal helper; idempotent; called by `Issue` and `Validate` before any JWT work        |
| `generateJTI`     | `func()` | Returns a UUIDv4 string from `crypto/rand` (R-003)                                              |
| `validateAlgorithm` | `func()` | Pre-keyfunc header inspection (R-006)                                                        |
| `compareIPs`      | `func()` | netip.Addr equality check (R-004)                                                               |

None of the file-private types or helpers are exported. The `Register`
function is exported only to make the registration step visible in
godoc; consumers MUST NOT call it directly (it is invoked
automatically by `Issue` and `Validate`).

---

## 2. The JWT compact form (the bytes-on-the-wire data model)

### 2.1 Layout

```text
+---------+--------+----------+--------+-----------+
|  header |   .    |  payload |   .    | signature |
| (b64url)|        | (b64url) |        | (b64url)  |
+---------+--------+----------+--------+-----------+
   ↑                  ↑                    ↑
   |                  |                    |
   {                  {                    DER-encoded ECDSA signature
     "alg":"ES256K",    "iss":"hush",         (r, s) over
     "typ":"JWT"        "iat":1714435200,     SHA-256(b64url(header) +
   }                    "exp":1714438800,                  "." +
                        "jti":"<uuidv4>",                  b64url(payload))
                        "scope":[...],
                        "client_ip":"...",
                        "request_id":"...",
                        "max_uses":50,
                        "ephemeral_pubkey":"<hex>",
                        "session_type":"interactive"
                      }
```

| Field       | Encoding                       | Source                                                            |
|-------------|--------------------------------|-------------------------------------------------------------------|
| `header`    | base64url(JSON {"alg":"ES256K","typ":"JWT"}) | `golang-jwt/jwt/v5` produces this from `es256kMethod{}.Alg()`     |
| `payload`   | base64url(JSON of Claims)      | `Claims` struct's JSON marshalling                                |
| `signature` | base64url(ASN.1 DER `(r,s)`)   | `ecdsa.SignASN1(rand.Reader, key, sha256.Sum256(signingInput))`   |

### 2.2 Required claims (per FR-009 / `docs/SECURITY.md` Layer 2 table)

| Claim              | JSON key            | Type                | Source                                            |
|--------------------|---------------------|---------------------|---------------------------------------------------|
| Issuer             | `iss`               | string              | constant `"hush"`                                 |
| Issued at          | `iat`               | NumericDate (Unix s)| `IssueParams.Now`                                 |
| Expires at         | `exp`               | NumericDate (Unix s)| `IssueParams.Now.Add(IssueParams.TTL)`            |
| JWT ID             | `jti`               | string              | UUIDv4 from `crypto/rand` (R-003)                  |
| Scope              | `scope`             | string array        | `IssueParams.Scope`                               |
| Client IP          | `client_ip`         | string              | `IssueParams.ClientIP` (validated by `netip.ParseAddr`) |
| Request ID         | `request_id`        | string              | `IssueParams.RequestID`                           |
| Max uses           | `max_uses`          | int                 | `IssueParams.MaxUses` (forced to 0 for SUPERVISOR) |
| Ephemeral pubkey   | `ephemeral_pubkey`  | string              | `IssueParams.EphemeralPubKey` (hex compressed pubkey) |
| Session type       | `session_type`      | string              | `IssueParams.SessionType` (one of two named values) |

### 2.3 Verification ordering (decrypt)

1. `ctx.Err()` early-out → returns `ctx.Err()` verbatim (FR-015 / SC-011).
2. **Header pre-check** (`validateAlgorithm`): base64url-decode the
   first segment; JSON-unmarshal `{Alg string}`; reject any value
   other than `"ES256K"` with `ErrAlgorithmUnsupported`. Fires
   BEFORE the keyfunc is consulted (R-006).
3. **`jwt.ParseWithClaims`** with `WithValidMethods([]string{"ES256K"})`:
   - Library re-validates the alg (defence in depth).
   - Library invokes the keyfunc, which returns the caller's
     `*ecdsa.PublicKey`.
   - Library verifies the signature via `es256kMethod{}.Verify`
     (which calls `ecdsa.VerifyASN1` constant-time).
   - Library validates `RegisteredClaims` time windows; expired
     surfaces as `jwt.ErrTokenExpired`, mapped to `ErrTokenExpired`.
   - Signature verify failure (or any library-level error other
     than `jwt.ErrTokenExpired`) maps to `ErrAlgorithmUnsupported`
     (the FR-007 #1 umbrella).
4. **SessionType vocabulary check**: `Claims.SessionType` ∉
   `{SessionInteractive, SessionSupervisor}` → `ErrUnknownSessionType`.
5. **Scope check**: `requestedSecret` ∉ `Claims.Scope` → `ErrScopeViolation`.
6. **IP check**: `compareIPs(Claims.ClientIP, requestIP)` returns
   non-nil → `ErrIPMismatch`.
7. **Store revocation check**: `Store.Get(jti)` returns
   `ErrTokenRevoked` if jti ∈ revoked-set; otherwise returns the
   live record.
8. **Use-count check** (INTERACTIVE only): `Store.ConsumeUse(jti)`
   returns `ErrTokenExhausted` when `MaxUses == 0`. SUPERVISOR
   sessions: `ConsumeUse` returns `nil` immediately without
   decrementing.

The ordering is deterministic; each step's failure surfaces a
distinct sentinel.

---

## 3. The Store (state-machine data model)

### 3.1 State

```go
type memStore struct {
    mu      sync.RWMutex
    live    map[string]*Token   // active jti → record (TTL-bounded)
    revoked map[string]struct{} // revoked jti → flag (lifetime-of-store)
    tick    time.Duration       // injectable; default 30s
    nowFn   func() time.Time    // injectable; default time.Now
}
```

The two-map split lets `Cleanup` reclaim memory from `live` while
`revoked` retains the FR-011 lifetime-of-store guarantee.

### 3.2 Operations

| Operation     | Lock held       | Pre-check                                              | Post-condition                                                                                                  | Error sentinel                                            |
|---------------|-----------------|--------------------------------------------------------|------------------------------------------------------------------------------------------------------------------|-----------------------------------------------------------|
| `Add(t)`      | write           | `t.JTI ∈ revoked` → reject                              | `live[t.JTI] = t`                                                                                                | `ErrTokenRevoked` (collision with revoked set)            |
| `Get(jti)`    | read            | `jti ∈ revoked` → reject                                | returns `live[jti]` or "not found" if absent                                                                     | `ErrTokenRevoked` (revoked); `ErrTokenRevoked` (unknown — folded into revoked from this store's perspective per FR-011) |
| `ConsumeUse`  | write           | `jti ∈ revoked` → reject; `live[jti]` absent → reject; `t.ExpiresAt <= nowFn()` → reject; `t.SessionType == SessionSupervisor` → return `nil` (no decrement); `t.MaxUses == 0` → reject | `t.MaxUses--` (interactive only)                                                  | `ErrTokenRevoked` / `ErrTokenExpired` / `ErrTokenExhausted` |
| `Revoke(jti)` | write           | (none — idempotent)                                    | `revoked[jti] = struct{}{}`; `delete(live, jti)`                                                                 | (none — always nil)                                       |
| `Cleanup`     | write per-tick  | (none)                                                 | every `t ∈ live` with `t.ExpiresAt <= nowFn()` removed; `revoked` untouched                                       | (none — synchronous; returns when `ctx.Done()`)            |

### 3.3 State transitions

```
                    ┌─────────────────┐
                    │  freshly issued │
                    └────────┬────────┘
                             │ Add(t)
                             ▼
                    ┌─────────────────┐
                    │   live (active) │◄─── ConsumeUse decrements MaxUses
                    └────────┬────────┘                    (interactive only)
                             │
                ┌────────────┼────────────┐
                │            │            │
        TTL elapses   Revoke(jti)    MaxUses → 0
                │            │            │
                ▼            ▼            ▼
        ┌──────────────┐ ┌─────────┐ ┌──────────┐
        │ Cleanup      │ │ revoked │ │ exhausted│
        │ removes from │ │  set    │ │ (still   │
        │ live         │ │         │ │  in live │
        └──────────────┘ └────┬────┘ │  until   │
                              │      │  TTL or  │
                              │      │  cleanup)│
                              │      └────┬─────┘
                              │           │
                              └───────────┼─── Validate returns ErrTokenRevoked / ErrTokenExhausted
                                          │
                                          ▼
                                    (terminal — no recovery)
```

A revoked JTI is **terminally rejected** for the lifetime of the
store. `Cleanup` removes the live record but leaves the revoked-set
entry in place; subsequent `Validate` calls against the same JTI
return `ErrTokenRevoked` (the revoked-set check fires first in
`Get`).

### 3.4 Concurrency invariants

- **I-conc-1**: N goroutines simultaneously calling `ConsumeUse(jti)`
  on an INTERACTIVE token whose initial `MaxUses == M` produce
  exactly `min(N, M)` successes; subsequent calls return
  `ErrTokenExhausted`. Asserted by `TestStore_ConcurrentDecrement`
  with N=100, M=100; no race-detector warnings under `-race`.
- **I-conc-2**: Concurrent `Add` / `Get` / `ConsumeUse` / `Revoke`
  / `Cleanup` produce no race-detector warnings.
- **I-conc-3**: A `Cleanup` sweep that races with a `ConsumeUse` on
  an expired record results in either (a) the `ConsumeUse` succeeding
  before sweep removes the record, or (b) the `ConsumeUse` returning
  `ErrTokenExpired` (or `ErrTokenRevoked` if `Cleanup` ran first and
  the record is gone — which folds to `ErrTokenRevoked` per the
  "unknown jti from this store's perspective" rule). No partial
  state is ever observable.

---

## 4. Sentinel error catalogue

All sentinels are package-level
`var Err... = errors.New("hush/token: <message>")` declarations.
Compare via `errors.Is`. **No wrap relationships** between sentinels —
each category is independent.

| Sentinel                  | Static message                          | Triggered by                                                                                        |
|---------------------------|-----------------------------------------|-----------------------------------------------------------------------------------------------------|
| `ErrAlgorithmUnsupported` | `hush/token: algorithm unsupported`     | Header `alg` not `"ES256K"` (incl. `"none"`, `"HS256"`, malformed); signature-class verification failure; IssueParams validation failure (catch-all umbrella). |
| `ErrTokenExpired`         | `hush/token: token expired`             | `Claims.ExpiresAt.Before(now)` (mapped from `jwt.ErrTokenExpired`); `Store.ConsumeUse` against an expired live record. |
| `ErrTokenRevoked`         | `hush/token: token revoked`             | JTI in revoked set (`Store.Get`, `Store.ConsumeUse`); `Store.Add` on JTI collision with revoked-set; `Store.Get`/`Store.ConsumeUse` on unknown JTI (from this store's perspective). |
| `ErrTokenExhausted`       | `hush/token: token exhausted`           | `Store.ConsumeUse` for an INTERACTIVE token whose `MaxUses == 0`. NEVER returned for SUPERVISOR.    |
| `ErrIPMismatch`           | `hush/token: ip mismatch`               | `compareIPs(Claims.ClientIP, requestIP)` returns non-nil; either input fails to parse via `netip.ParseAddr`. |
| `ErrScopeViolation`       | `hush/token: scope violation`           | `requestedSecret` not present in `Claims.Scope`.                                                    |
| `ErrUnknownSessionType`   | `hush/token: unknown session type`      | `Claims.SessionType` ∉ `{SessionInteractive, SessionSupervisor}`; `IssueParams.SessionType` ∉ vocabulary. |

### 4.1 Sentinel selection table by failure shape

| Failure shape                                              | Returned sentinel              | Notes                                                                             |
|------------------------------------------------------------|--------------------------------|-----------------------------------------------------------------------------------|
| `Issue(ctx, ...)` with cancelled `ctx`                      | `context.Canceled` (verbatim)  | Preserves `errors.Is(err, context.Canceled)`                                     |
| `Issue` with deadline-exceeded `ctx`                        | `context.DeadlineExceeded`     | Preserves `errors.Is(err, context.DeadlineExceeded)`                             |
| `Issue` with `IssueParams.SessionType` not in vocabulary    | `ErrUnknownSessionType`        | First validation gate                                                             |
| `Issue` with any other invalid `IssueParams` field          | `ErrAlgorithmUnsupported`      | The umbrella for "input cannot be signed"                                         |
| `Issue` with valid params                                   | `nil` + fresh `*Token`         | Caller MUST call `Store.Add(t)` to register                                      |
| `Validate(ctx, ...)` with cancelled `ctx`                   | `context.Canceled` (verbatim)  | Same as Issue                                                                    |
| `Validate` with empty `encoded`                             | `ErrAlgorithmUnsupported`      | `validateAlgorithm` rejects no-`.` input                                          |
| `Validate` with malformed-base64 header                     | `ErrAlgorithmUnsupported`      | `validateAlgorithm` rejects                                                      |
| `Validate` with valid base64 / bad JSON header              | `ErrAlgorithmUnsupported`      | `validateAlgorithm` rejects                                                      |
| `Validate` with header `alg=none`                           | `ErrAlgorithmUnsupported`      | `validateAlgorithm` rejects BEFORE keyfunc                                       |
| `Validate` with header `alg=HS256`                          | `ErrAlgorithmUnsupported`      | `validateAlgorithm` rejects BEFORE keyfunc                                       |
| `Validate` with valid `alg=ES256K` but signature mismatch   | `ErrAlgorithmUnsupported`      | Library `jwt.ErrTokenSignatureInvalid` mapped to umbrella                         |
| `Validate` with valid signature, expired claims             | `ErrTokenExpired`              | Library `jwt.ErrTokenExpired` mapped                                              |
| `Validate` with valid signature, unknown session type       | `ErrUnknownSessionType`        | Vocabulary check fires after signature verify                                     |
| `Validate` with valid signature, requested secret out of scope | `ErrScopeViolation`         | Scope check                                                                       |
| `Validate` with valid signature, mismatched IPs             | `ErrIPMismatch`                | netip.Addr equality                                                               |
| `Validate` with revoked JTI                                  | `ErrTokenRevoked`              | Store revoked-set membership                                                      |
| `Validate` with exhausted INTERACTIVE token                 | `ErrTokenExhausted`            | `Store.ConsumeUse` returns it                                                     |
| `Validate` with happy SUPERVISOR token (any uses)           | `nil` + recovered `*Claims`    | SUPERVISOR skips ConsumeUse decrement                                             |
| `Validate` with happy INTERACTIVE token (uses > 0)          | `nil` + recovered `*Claims`    | `MaxUses` decremented atomically under store write lock                           |

### 4.2 Why no sub-categories of `ErrAlgorithmUnsupported`

The collapse is deliberate. FR-002 / FR-003 / FR-007 #1 mandate that
the validator cannot distinguish "alg=none" from "alg=HS256" from
"alg=ES256K but signature did not verify" by error identity.
Promoting any sub-category (none / HS256 / signature-fail) to a
distinct sentinel would leak the failure shape via `errors.Is`
matching, enabling an attacker to probe which gate fired.

The audit log writer (SDD-25) records the request-id and the failure
sentinel; consumer-side classification of "which alg attack" is the
audit log's responsibility, not the package's.

---

## 5. Per-call lifecycle

### 5.1 Issue (server side, SDD-12 consumer)

```text
Issue(ctx, signKey, params) -> (token, err)
│
├── 1. ctx.Err() check                     [returns ctx.Err() verbatim if cancelled]
├── 2. Register()                          [sync.Once; first call registers ES256K]
├── 3. validate params                      [returns ErrUnknownSessionType for bad SessionType, ErrAlgorithmUnsupported for any other invalid field]
├── 4. force MaxUses=0 for SUPERVISOR
├── 5. generate JTI                         [crypto/rand → 16 bytes → RFC 4122 v4]
├── 6. construct Claims                    [iss="hush", iat/exp from params.Now+TTL, jti, scope, client_ip, request_id, max_uses, ephemeral_pubkey, session_type]
├── 7. jwt.NewWithClaims(es256kMethod, claims)
├── 8. token.SignedString(signKey)         [stdlib ECDSA SignASN1 over SHA-256(signingInput)]
└── 9. return &Token{JTI, Encoded, ExpiresAt, SessionType, MaxUses}
```

### 5.2 Validate (server side, SDD-13 consumer)

```text
Validate(ctx, encoded, verifyKey, store, requestIP, requestedSecret) -> (claims, err)
│
├── 1. ctx.Err() check
├── 2. Register()                          [sync.Once; same as Issue]
├── 3. validateAlgorithm(encoded)          [returns ErrAlgorithmUnsupported for non-ES256K alg]
├── 4. jwt.ParseWithClaims(encoded, &Claims{}, keyfunc, WithValidMethods)
│      ├── library verifies signature via es256kMethod.Verify (constant-time)
│      └── library validates RegisteredClaims time windows
│      → returns mapped error (ErrTokenExpired / ErrAlgorithmUnsupported)
├── 5. SessionType vocabulary check         [returns ErrUnknownSessionType if not interactive|supervisor]
├── 6. scope check                          [returns ErrScopeViolation if requestedSecret not in Claims.Scope]
├── 7. compareIPs(Claims.ClientIP, requestIP) [returns ErrIPMismatch on parse fail or non-equal]
├── 8. store.Get(jti)                       [returns ErrTokenRevoked if revoked or unknown]
├── 9. store.ConsumeUse(jti)                [INTERACTIVE: decrements MaxUses or returns ErrTokenExhausted; SUPERVISOR: returns nil]
└── 10. return claims, nil
```

### 5.3 ConsumeUse (server side, SDD-13 consumer; called by Validate)

```text
ConsumeUse(jti) -> err
│ (mu.Lock held)
├── 1. revoked[jti]?                        → ErrTokenRevoked
├── 2. live[jti] absent?                    → ErrTokenRevoked
├── 3. t.ExpiresAt <= nowFn()?              → ErrTokenExpired
├── 4. t.SessionType == Supervisor?         → return nil (no decrement)
├── 5. t.MaxUses == 0?                      → ErrTokenExhausted
├── 6. t.MaxUses--
└── 7. return nil
```

### 5.4 Revoke (server side, SDD-13 revoke handler consumer)

```text
Revoke(jti) -> err
│ (mu.Lock held)
├── 1. revoked[jti] = struct{}{}
├── 2. delete(live, jti)
└── 3. return nil  [idempotent — revoking an unknown or already-revoked JTI is a no-op success]
```

### 5.5 Cleanup (server side, server.Server consumer; runs in caller's goroutine)

```text
Cleanup(ctx) -> (no return value)
│
├── 1. ticker := time.NewTicker(s.tick)
├── 2. for {
│        select {
│          case <-ctx.Done(): return
│          case <-ticker.C: sweepExpired()
│        }
│      }
└── (ticker.Stop() on defer)

sweepExpired():
│ (mu.Lock held)
├── now := nowFn()
├── for jti, t := range live { if !t.ExpiresAt.After(now) { delete(live, jti) } }
└── (revoked is NOT touched)
```

---

## 6. Caller-side data model (downstream consumers)

### 6.1 SDD-12 (server `/claim` handler)

The claim handler holds:
- A `context.Context` from the HTTP request.
- A `*ecdsa.PrivateKey` (server's JWT signing key, from
  `keys.DeriveJWTSigningKey` at server startup).
- An approved `IssueParams` populated from the Discord approval +
  client request (scope, IP, request ID, ephemeral pubkey, session
  type, TTL, max-uses).
- A `Store` (the server's session store, constructed via `NewStore()`
  at startup).

Lifecycle:

```text
ctx := r.Context()
params := buildIssueParams(approval, claimRequest)
tok, err := token.Issue(ctx, signKey, params)
if err != nil {
    return // 4xx with sentinel-mapped category
}
if err := store.Add(tok); err != nil {
    return // 4xx — JTI collision with revoked set is operationally weird but possible
}
respondJSON(w, http.StatusOK, ClaimResponse{Token: tok.Encoded, ExpiresAt: tok.ExpiresAt, ...})
```

The `tok.Encoded` byte string is the wire form returned to the client;
the `*Token` record stays in the in-memory store for future
`Validate` calls to find.

### 6.2 SDD-13 (server `/secrets/{name}` handler)

The secret-fetch handler holds:
- A `context.Context` from the HTTP request.
- The encoded JWT from the `Authorization: Bearer ...` header.
- A `*ecdsa.PublicKey` (server's JWT verify key, from
  `keys.DeriveJWTSigningKey().PublicKey` at server startup).
- The `Store`.
- The requesting IP (from middleware — Tailscale-resolved IP, not
  the client-claimed `X-Forwarded-For`).
- The secret name (from the URL path parameter).

Lifecycle:

```text
ctx := r.Context()
encoded := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
requestIP := remoteAddr(r)            // resolved by middleware, not client-claimed
secretName := chi.URLParam(r, "name") // or your router's equivalent

claims, err := token.Validate(ctx, encoded, verifyKey, store, requestIP, secretName)
if err != nil {
    // map sentinel → 401/403 with audit-log entry; respond generically (no token bytes leaked)
    return
}

// At this point, claims are valid AND the use-count has been decremented
// (for INTERACTIVE) or the supervisor session has been consulted (for SUPERVISOR).
// The handler now reaches into the vault to fetch the secret value.
sb, err := vaultStore.Get(secretName)
defer sb.Destroy()

// ... ECIES-encrypt sb to claims.EphemeralPubKey, write envelope to response (per SDD-09)
```

The handler MUST `Validate` BEFORE consulting the vault — `Validate`'s
side effect (use-count decrement) is the spec's "every successful
validation MUST decrement before the secret-fetch handler is permitted
to read the secret" promise (FR-005).

### 6.3 SDD-13 (server `/revoke/{jti}` handler)

```text
ctx := r.Context()
encoded := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
requestIP := remoteAddr(r)
jti := chi.URLParam(r, "jti")

// Validate the caller's session token first (the caller must hold the
// token they're trying to revoke — this is the API contract from
// `docs/SPEC.md` FR-9 third bullet).
claims, err := token.Validate(ctx, encoded, verifyKey, store, requestIP, "")
// (Note: empty `requestedSecret` for /revoke — the consumer skips the
// scope check or supplies a "REVOKE" sentinel in claims.Scope. The
// exact policy is SDD-13's responsibility.)
if err != nil {
    return // 401 / 403
}
if claims.RegisteredClaims.ID != jti {
    return // 403 — caller can revoke only their own jti
}
if err := store.Revoke(jti); err != nil {
    return
}
respondJSON(w, http.StatusOK, RevokeResponse{JTI: jti})
```

The revoke handler's exact authorisation rules (does it allow
revoking ANY jti, or only the caller's own jti?) are SDD-13's
responsibility; SDD-07 provides the primitive (`Store.Revoke`) and
the lifetime-of-store guarantee.

### 6.4 SDD-23 (`hush supervise` consumer)

The supervisor CLI holds the session token (`Token.Encoded`) in its
process memory across child restarts within the session TTL. The
supervisor does NOT validate the token itself (the server validates
on every request); the supervisor merely re-presents the token to
the server's `/secrets/{name}` endpoint on each child restart.

Lifecycle:

```text
// At supervise startup:
claimResp := requestClaim(ctx, ...) // POST /claim
sessionToken := claimResp.Token     // the JWT compact form
sessionExpires := claimResp.ExpiresAt

// At each child restart within TTL:
secretValue := fetchSecret(ctx, sessionToken, secretName)
spawnChild(ctx, secretValue)

// At TTL approach (refresh window):
if time.Until(sessionExpires) < refreshNudgeWindow {
    promptDiscordRefresh(ctx)
}
```

The supervisor never calls `token.Validate` directly; it only stores
the encoded JWT and the recorded expiry. This is consistent with the
package's no-network-I/O contract — the supervisor is a CLI client,
not a server.

---

## 7. Test data

### 7.1 Fixture keys

| Test                                          | Key generation                                          | Fresh per-run? |
|-----------------------------------------------|---------------------------------------------------------|----------------|
| `TestIssue_Interactive`                       | `ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)`       | Yes            |
| `TestIssue_Supervisor`                        | same                                                     | Yes            |
| `TestValidate_HappyPath`                      | same                                                     | Yes            |
| `TestValidate_ExpiredJWT`                     | same                                                     | Yes            |
| `TestValidate_WrongIP`                        | same                                                     | Yes            |
| `TestValidate_OutOfScope`                     | same                                                     | Yes            |
| `TestValidate_AlgConfusion_None_Refused`      | same; mangle alg to "none"                               | Yes            |
| `TestValidate_AlgConfusion_HS256_Refused`     | same; mangle alg to "HS256"                              | Yes            |
| `TestValidate_UnknownSessionType_Refused`     | same; manually craft token with bad session_type         | Yes            |
| `TestStore_RevokedJTI_Refused`                | same                                                     | Yes            |
| `TestStore_ExhaustedInteractive_Refused`      | same                                                     | Yes            |
| `TestStore_SupervisorIgnoresMaxUses`          | same                                                     | Yes            |
| `TestStore_CleanupRemovesExpired`             | same                                                     | Yes            |
| `TestStore_ConcurrentDecrement`               | same — N=100 goroutines on one JTI                       | Yes            |
| `TestValidate_NoLeakOnError`                  | same — sentinel embedded in `request_id`                 | Yes            |
| `FuzzJWTValidate`                             | Deterministic key (R-011)                                | **No**         |

### 7.2 Sentinel marker

`testutil.SentinelSecret(2)` returns `"SECRET_SHOULD_NEVER_APPEAR_2"`
(28 ASCII chars). Used in `TestValidate_NoLeakOnError` as the
embedded-claim marker; tested for absence in `err.Error()` and the
wrap chain via `testutil.AssertSentinelAbsent`.

### 7.3 Fuzz seed corpus

```text
testdata/fuzz/FuzzJWTValidate/
├── empty                       # empty string
├── malformed-base64            # "not.valid.base64"
├── valid-base64-bad-json       # base64url("hello").base64url("world").base64url("garbage")
├── alg-none                    # real JWT shape with alg="none" header, empty signature
└── alg-hs256                   # real JWT shape with alg="HS256" header, HMAC signature using verify-key bytes as shared secret
```

The "alg-none" and "alg-hs256" seeds are pre-computed at corpus
creation time using a fixed test keypair (the same keypair the fuzz
target's `generateFuzzKey` returns). The seeds are committed under
`testdata/fuzz/FuzzJWTValidate/`. The exact byte content does not
matter beyond exercising the alg-confusion code path; the seeds
exist to bypass the malformed-input early-out and exercise the
deeper validation logic.

---

## 8. Invariants (testable at any commit)

| ID    | Invariant                                                                                                   | Asserted by                                         |
|-------|-------------------------------------------------------------------------------------------------------------|-----------------------------------------------------|
| I-001 | `Issue` followed by `Validate` (matching keys, matching IP, in-scope name, non-expired) returns recovered claims with the issued field values. | `TestValidate_HappyPath`                            |
| I-002 | `Issue` produces a fresh JTI on every call; two calls with identical IssueParams produce two distinct JTIs. | `TestIssue_FreshJTIPerCall`                         |
| I-003 | `Issue` returns a token whose `Encoded` field decodes to a JWT compact form with `alg=ES256K` in the header. | `TestIssue_HeaderAlg`                               |
| I-004 | `Validate` rejects an alg=none token with `ErrAlgorithmUnsupported`.                                         | `TestValidate_AlgConfusion_None_Refused`            |
| I-005 | `Validate` rejects an alg=HS256 token (HMAC-signed with verify-key bytes as shared secret) with `ErrAlgorithmUnsupported`. | `TestValidate_AlgConfusion_HS256_Refused` |
| I-006 | `Validate` rejects an expired token with `ErrTokenExpired`.                                                  | `TestValidate_ExpiredJWT`                           |
| I-007 | `Validate` rejects a token presented from a different IP with `ErrIPMismatch`.                               | `TestValidate_WrongIP`                              |
| I-008 | `Validate` treats two textual representations of the same IP as equal.                                       | `TestValidate_IPSemanticallyEqual`                  |
| I-009 | `Validate` rejects a token whose scope does not include the requested secret with `ErrScopeViolation`.        | `TestValidate_OutOfScope`                           |
| I-010 | `Validate` rejects a token whose session_type is neither "interactive" nor "supervisor" with `ErrUnknownSessionType`. | `TestValidate_UnknownSessionType_Refused`     |
| I-011 | `Validate` rejects a token whose JTI has been revoked with `ErrTokenRevoked`.                                | `TestStore_RevokedJTI_Refused`                       |
| I-012 | `Validate` rejects an INTERACTIVE token whose use-budget is exhausted with `ErrTokenExhausted`.              | `TestStore_ExhaustedInteractive_Refused`             |
| I-013 | `Validate` does NOT decrement a SUPERVISOR token's use-budget; repeated validations succeed within TTL.       | `TestStore_SupervisorIgnoresMaxUses`                  |
| I-014 | `Store.Cleanup` removes expired live records but never touches the revoked set; a revoked JTI presented after cleanup still rejects with `ErrTokenRevoked`. | `TestStore_RevokedSurvivesCleanup` |
| I-015 | N goroutines decrementing the same INTERACTIVE token's use-count produce exactly N successes, no race-detector warnings. | `TestStore_ConcurrentDecrement`              |
| I-016 | `err.Error()` for any returned error never contains any byte of the encoded token, the signing key, or the verify key. | `TestValidate_NoLeakOnError`                |
| I-017 | `Validate` is panic-free under any string input.                                                              | `FuzzJWTValidate` (60 s gate)                        |
| I-018 | Pre-cancelled context returns `ctx.Err()` such that `errors.Is(err, context.Canceled)` is true.              | `TestIssue_RespectsCancelledContext`, `TestValidate_RespectsCancelledContext` |
| I-019 | `Issue` rejects an invalid `IssueParams.SessionType` with `ErrUnknownSessionType`.                            | `TestIssue_RejectsUnknownSessionType`                |
| I-020 | The signing-method registration is idempotent across many concurrent `Issue` / `Validate` calls; no panic, no double-register. | `TestRegisterOnce_Concurrent`                |
| I-021 | Coverage of `internal/token` is 100%.                                                                         | `magex test:race` (codecov.yml gate)                  |

---

## 9. What this data model does NOT specify

- **The exact bit-pattern of `crypto/ecdsa.SignASN1`'s output** — that
  is the stdlib's contract, supplied by `crypto/ecdsa`. The package
  uses the result, does not re-implement ECDSA.
- **The exact JSON serialisation of `Claims`** — that is
  `golang-jwt/jwt/v5`'s contract. The library uses `encoding/json`
  with the struct tags documented in §1.2.
- **The exact platform-specific behaviour of `crypto/rand.Reader`** —
  the package uses the stdlib's documented entropy source.
- **The HTTP transport of the JWT** — that is SDD-12's responsibility
  (claim handler returns the encoded JWT in the response body) and
  SDD-13's responsibility (secret handler reads it from the
  `Authorization: Bearer ...` header). The package treats the
  encoded JWT as opaque bytes.
- **The audit-log entry shape for token operations** — that is
  SDD-25's responsibility. The package returns enough information
  (the `*Claims` on success, the sentinel on failure) for the audit
  writer to build its entries.
- **The Discord approval flow that authorises issuance** — that is
  SDD-11/SDD-12's responsibility. The package mints a token only
  after approval has happened; the approval state is reflected in
  the `IssueParams` the caller supplies.
- **The supervisor's session-retention policy** — that is SDD-23's
  responsibility. The package returns a Token; the supervisor stores
  it across child restarts.

The data model is intentionally bounded to the JWT bytes-on-the-wire,
the in-store state, and the types-in-memory of THIS package;
everything else is the consumer's responsibility.
