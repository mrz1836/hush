# Quickstart: `internal/token`

**Feature**: 007-token-jwt
**Audience**: SDD-12 (server `/claim` handler), SDD-13 (server
`/secrets/{name}` and `/revoke/{jti}` handlers), SDD-23 (`hush
supervise` session-retention).

This is the shortest correct path from "I have an approved claim
request" to a wire-form JWT, and from "I have an inbound
`Authorization: Bearer ...` header" to a validated `*Claims` plus a
permitted secret fetch. Three recipes, all end-to-end testable.

---

## Recipe 1 — Server-side issue (SDD-12)

You have:

- A `context.Context` from the HTTP handler (`r.Context()`).
- A `*ecdsa.PrivateKey` — the server's JWT signing key, derived once
  at startup via `keys.DeriveJWTSigningKey` (SDD-01) and held in
  `Server.signKey`.
- An approved claim request: scope, IP, request ID, ephemeral
  pubkey, session type, TTL, max-uses (the parameters Discord
  approval accepted).
- A `Store` constructed at server startup via `token.NewStore()` and
  held in `Server.tokenStore`.

You want: an opaque encoded JWT to return in the `/claim` response,
and an in-store record so future `Validate` calls find the token.

```go
package server

import (
    "context"
    "errors"
    "net/http"
    "time"

    "github.com/mrz1836/hush/internal/token"
)

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // 1. Decode + signature-verify + nonce-check the inbound /claim
    //    request via internal/transport/sign (SDD-08). Out of scope here.
    claimReq, err := s.decodeAndVerifyClaim(ctx, r)
    if err != nil {
        respondError(w, err)
        return
    }

    // 2. Discord approval (SDD-11). Out of scope here.
    approval, err := s.discord.RequestApproval(ctx, ApprovalRequest{
        RequesterHost: claimReq.HostName,
        Scopes:        claimReq.Scope,
        SessionType:   string(claimReq.SessionType),
        TTL:           claimReq.TTL,
        MaxUses:       claimReq.MaxUses,
    })
    if err != nil || approval.Decision != DecisionApprove {
        respondStatus(w, http.StatusForbidden)
        s.audit.Record(SessionDeniedEvent{RequestID: claimReq.RequestID})
        return
    }

    // 3. Build IssueParams from approval + request.
    params := token.IssueParams{
        Now:             time.Now(),
        TTL:             approval.TTL,
        Scope:           claimReq.Scope,
        ClientIP:        s.clientIP(r),
        RequestID:       claimReq.RequestID,
        MaxUses:         s.config.DefaultMaxUses, // ignored if SUPERVISOR
        EphemeralPubKey: claimReq.EphemeralPubKey,
        SessionType:     claimReq.SessionType,
    }

    // 4. Issue.
    tok, err := token.Issue(ctx, s.signKey, params)
    if err != nil {
        // Map by sentinel — never log the encoded JWT.
        switch {
        case errors.Is(err, token.ErrUnknownSessionType):
            respondStatus(w, http.StatusBadRequest) // 400
        case errors.Is(err, token.ErrAlgorithmUnsupported):
            respondStatus(w, http.StatusBadRequest) // 400 — invalid IssueParams
        case errors.Is(err, context.Canceled):
            return
        default:
            respondStatus(w, http.StatusInternalServerError)
        }
        s.audit.Record(SessionDeniedEvent{RequestID: claimReq.RequestID, Reason: err})
        return
    }

    // 5. Register in the store. ErrTokenRevoked here means the JTI
    //    collided with a revoked-set entry — vanishingly unlikely
    //    (UUIDv4 collision space is 2^122) but possible.
    if err := s.tokenStore.Add(tok); err != nil {
        respondStatus(w, http.StatusInternalServerError)
        return
    }

    // 6. Return the encoded JWT to the client.
    s.audit.Record(SessionApprovedEvent{
        JTI:         tok.JTI,
        RequestID:   claimReq.RequestID,
        ExpiresAt:   tok.ExpiresAt,
        SessionType: tok.SessionType,
    })
    respondJSON(w, http.StatusOK, ClaimResponse{
        Token:     tok.Encoded,
        ExpiresAt: tok.ExpiresAt,
        JTI:       tok.JTI,
    })
}
```

**Why each step matters**:

- **Step 4's `errors.Is` switch** is the FR-013 sentinel-identity
  contract. The handler classifies failures by sentinel, never by
  parsing `err.Error()`.
- **Step 5's separation of `Issue` from `Store.Add`** lets the
  handler audit-record the issue success/failure separately from the
  store-add success/failure. If `Add` fails (JTI collision with
  revoked set), the `Issue` succeeded but the token is dead on
  arrival — the caller MUST NOT return it to the client.
- **Step 6's `tok.Encoded` byte string** is the wire form returned
  to the client. The `tok.JTI` is also returned for the client's
  audit-trace correlation.

**What MUST NOT happen** in any branch:

- `respondError(w, err)` MUST NOT include any byte from `tok.Encoded`
  in the response body. The error messages from this package are
  static category strings (`hush/token: ...`) and contain no token
  material — but if your `respondError` helper logs the error AND
  ALSO prints the original `r.URL` or `r.Header`, audit those for
  token-bytes leakage (the inbound request body shouldn't contain
  the OUTBOUND JWT, but cross-check).
- The `tok.Encoded` field MUST NOT be logged on the server side
  (Constitution X). It is wire-safe to return to the client (that's
  what JWT IS), but logging it pointlessly expands the diagnostic
  surface.

---

## Recipe 2 — Server-side validate (SDD-13 secret-fetch handler)

You have:

- A `context.Context` from the HTTP handler.
- The encoded JWT from the `Authorization: Bearer ...` header.
- A `*ecdsa.PublicKey` (server's JWT verify key, the public half of
  the signing key from Recipe 1).
- The `Store` (same instance as Recipe 1).
- The requesting IP (resolved by middleware from the underlying
  TCP connection — NOT from `X-Forwarded-For`, which a malicious
  agent can spoof).
- The secret name from the URL path parameter.

You want: a validated `*Claims` plus a confirmed use-decrement
(interactive) or revocation/expiry check (supervisor), so the
secret-fetch handler is permitted to read the vault.

```go
package server

import (
    "context"
    "errors"
    "net/http"
    "strings"

    "github.com/mrz1836/hush/internal/token"
    "github.com/mrz1836/hush/internal/transport/ecies"
)

func (s *Server) handleSecretFetch(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // 1. Extract the bearer token.
    encoded := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
    if encoded == "" || encoded == r.Header.Get("Authorization") {
        respondStatus(w, http.StatusUnauthorized)
        return
    }

    // 2. Resolve the requesting IP via middleware (Tailscale-resolved
    //    remote addr, not client-claimed X-Forwarded-For).
    requestIP := s.clientIP(r)

    // 3. Pull the secret name.
    secretName := chi.URLParam(r, "name") // or your router's equivalent

    // 4. Validate. This is the choke point — every check
    //    (signature, alg, expiry, scope, IP, revocation,
    //    use-decrement) happens here.
    claims, err := token.Validate(ctx, encoded, s.verifyKey, s.tokenStore, requestIP, secretName)
    if err != nil {
        s.audit.Record(AuthFailedEvent{
            JTI:       extractJTI(encoded), // best-effort; may be empty if header malformed
            Sentinel:  err,
            ClientIP:  requestIP,
            Path:      r.URL.Path,
        })
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
        case errors.Is(err, context.Canceled),
             errors.Is(err, context.DeadlineExceeded):
            // Client disconnected; nothing to respond to.
        default:
            respondStatus(w, http.StatusInternalServerError)
        }
        return
    }

    // 5. At this point: signature verified, claims valid, in-scope,
    //    IP matches, not revoked, use decremented (interactive only).
    //    Now reach into the vault.
    sb, err := s.vault.Get(secretName)
    if err != nil {
        respondStatus(w, http.StatusInternalServerError)
        return
    }
    defer sb.Destroy()

    // 6. Recover the recipient ephemeral pubkey from the validated claims.
    recipientPub, err := decodeCompressedPubKey(claims.EphemeralPubKey)
    if err != nil {
        respondStatus(w, http.StatusInternalServerError)
        return
    }

    // 7. ECIES-encrypt to the recipient's ephemeral pubkey (SDD-09).
    var (
        envelope []byte
        encErr   error
    )
    if useErr := sb.Use(func(plaintext []byte) {
        envelope, encErr = ecies.Encrypt(ctx, recipientPub, plaintext)
    }); useErr != nil {
        respondStatus(w, http.StatusInternalServerError)
        return
    }
    if encErr != nil {
        respondStatus(w, http.StatusInternalServerError)
        return
    }

    // 8. Audit + write the opaque envelope.
    s.audit.Record(SecretFetchedEvent{
        JTI:        claims.RegisteredClaims.ID,
        SecretName: secretName,
        ClientIP:   requestIP,
    })
    w.Header().Set("Content-Type", "application/octet-stream")
    w.Write(envelope)
}
```

**Why each step matters**:

- **Step 4's `Validate` call** is the security choke point. Until it
  returns success, the handler MUST NOT touch the vault.
  `Validate`'s side effect (use-count decrement on success) is the
  spec's "every successful validation MUST decrement before the
  secret-fetch handler is permitted to read the secret" promise
  (FR-005).
- **Step 4's `errors.Is` switch** maps sentinel categories to HTTP
  status codes. Note the careful 401 vs 403 split: 401 = "token is
  invalid for any reason a refresh could fix" (alg, expired,
  revoked, exhausted); 403 = "token is valid but not allowed for
  this operation" (IP, scope, session type). The audit log records
  the specific sentinel for forensic analysis.
- **Step 6's `decodeCompressedPubKey`** parses the ephemeral pubkey
  from the validated claims. Because the claims have been
  signature-verified at step 4, the pubkey is trusted — it cannot
  have been tampered with in transit.

**What MUST NOT happen** in any branch:

- The `encoded` JWT MUST NOT appear in any log line, error message,
  or response body. The package's static-message sentinels guarantee
  this for the package's own errors; the consumer's audit log writer
  is responsible for the same discipline.
- The vault MUST NOT be consulted before `Validate` returns success.
  Reaching into the vault on a failed validation would (a) waste
  cycles, (b) potentially expose a side-channel via vault-cache
  warming.

---

## Recipe 3 — Server-side revoke (SDD-13 revoke handler)

You have:

- A `context.Context` from the HTTP handler.
- The encoded JWT from `Authorization: Bearer ...` (the caller's own
  session token).
- The JTI to revoke from the URL path parameter.
- The `Store`.

You want: the JTI marked revoked permanently for the lifetime of the
store.

```go
package server

import (
    "context"
    "errors"
    "net/http"
    "strings"

    "github.com/mrz1836/hush/internal/token"
)

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    encoded := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
    requestIP := s.clientIP(r)
    targetJTI := chi.URLParam(r, "jti")

    // 1. Validate the caller's own session token. We pass an empty
    //    requestedSecret because /revoke isn't scoped to a secret;
    //    the consumer-side policy is "any valid session can revoke".
    //    For a stricter policy ("session can revoke only its own
    //    JTI"), check claims.RegisteredClaims.ID == targetJTI below.
    claims, err := token.Validate(ctx, encoded, s.verifyKey, s.tokenStore, requestIP, "")
    if err != nil {
        respondStatus(w, http.StatusUnauthorized)
        return
    }

    // 2. Authorisation policy: caller can revoke only their own JTI.
    //    (A future enhancement could allow operator-revoke of any JTI;
    //    the package primitive supports it, but SDD-13's policy is
    //    self-only for v0.1.0.)
    if claims.RegisteredClaims.ID != targetJTI {
        respondStatus(w, http.StatusForbidden)
        return
    }

    // 3. Revoke. The operation is idempotent — revoking an
    //    already-revoked JTI is a no-op success.
    if err := s.tokenStore.Revoke(targetJTI); err != nil {
        respondStatus(w, http.StatusInternalServerError)
        return
    }

    s.audit.Record(TokenRevokedEvent{JTI: targetJTI, ClientIP: requestIP})
    respondStatus(w, http.StatusNoContent)
}
```

**Why each step matters**:

- **Step 1's `Validate` with empty `requestedSecret`** validates the
  caller's session without enforcing the scope check (`/revoke` is
  not a secret-fetch). The Validate-then-Revoke ordering ensures
  only authenticated callers can revoke.
- **Step 2's policy check** restricts revocation to the caller's
  own JTI. This is SDD-13's policy decision; the package primitive
  (`Store.Revoke`) supports any JTI and leaves authorisation to the
  caller.
- **Step 3's idempotency** lets the client retry the revoke without
  worry; double-revoke is a no-op.

---

## Recipe 4 — Cleanup goroutine (SDD-10 server bootstrap)

You have:

- A server-scoped `context.Context` from `Server.Run`.
- The `Store` (same instance as Recipes 1–3).

You want: expired live records reclaimed periodically; the goroutine
terminates cleanly on server shutdown.

```go
package server

import (
    "context"
)

func (s *Server) Run(ctx context.Context) error {
    // ... server setup ...

    // Start the token store cleanup goroutine. Owned by Run; terminates
    // when ctx fires.
    go s.tokenStore.Cleanup(ctx)

    // ... start HTTP server ...
    return s.httpServer.Serve(...)
}
```

That's the entire bootstrap. No additional wiring; no tick-interval
tuning (production uses the 30 s default from `NewStore()`).

For tests that need deterministic sweep observation:

```go
func TestSomething(t *testing.T) {
    store := token.NewStoreWithTick(1 * time.Millisecond)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go store.Cleanup(ctx)
    // ... add tokens, time.Sleep(50*time.Millisecond), assert sweep behaviour ...
}
```

---

## Recipe 5 — Client-side session retention (SDD-23 supervise)

You have:

- A `context.Context` for the supervise process's lifetime.
- The encoded JWT (`*Token.Encoded`) returned by the server's
  `/claim` response.
- The recorded `ExpiresAt` from the response.

You want: hold the encoded JWT across child restarts within TTL,
re-presenting it to `/secrets/{name}` on each child startup; trigger
a Discord refresh prompt as TTL approaches.

```go
package supervise

import (
    "context"
    "time"
)

type session struct {
    encoded   string
    jti       string
    expiresAt time.Time
}

func (s *Supervisor) holdSession(ctx context.Context, sess *session) error {
    refreshAt := sess.expiresAt.Add(-s.refreshNudgeWindow)

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-time.After(time.Until(refreshAt)):
            if err := s.promptDiscordRefresh(ctx, sess); err != nil {
                return err
            }
            // promptDiscordRefresh updates sess.encoded + sess.expiresAt.
            refreshAt = sess.expiresAt.Add(-s.refreshNudgeWindow)
        case event := <-s.childEvents:
            switch event.Kind {
            case ChildExitClean:
                // Silent refill — no Discord call. Just refetch secrets.
                if err := s.refetchSecrets(ctx, sess); err != nil {
                    return err
                }
            case ChildExitStaleCredentials: // exit code 78
                // Force a refresh prompt regardless of TTL window.
                if err := s.promptDiscordRefresh(ctx, sess); err != nil {
                    return err
                }
            }
        }
    }
}

func (s *Supervisor) refetchSecrets(ctx context.Context, sess *session) error {
    // Re-present sess.encoded to /secrets/{name}; the server's
    // Validate will check signature + alg + expiry + scope + IP +
    // revocation + use-count (interactive only). For SUPERVISOR
    // sessions, the use count is unbounded within TTL.
    for _, secretName := range s.config.Scope {
        if _, err := s.fetchSecret(ctx, sess.encoded, secretName); err != nil {
            return err
        }
    }
    return nil
}
```

The supervisor never calls `token.Validate` directly — that is the
server's job. The supervisor only stores the encoded JWT and the
recorded expiry; it presents the JWT to the server on every secret
fetch and observes the server's HTTP status code.

---

## Common mistakes (and how the package's contract prevents them)

| Mistake                                                       | What the package does                                                                                  |
|---------------------------------------------------------------|--------------------------------------------------------------------------------------------------------|
| Calling `Issue` with an unknown `SessionType`                | Returns `ErrUnknownSessionType` immediately. No JWT is signed; no entropy is consumed.                  |
| Calling `Issue` with `MaxUses=0` for INTERACTIVE             | Returns `ErrAlgorithmUnsupported` (input validation umbrella). Consumer's `IssueParams` was malformed.  |
| Calling `Issue` with `MaxUses=99` for SUPERVISOR             | Succeeds; `MaxUses` is silently zeroed in the Token (FR-006). Validate then ignores it.                 |
| Calling `Validate` with a token from a forged `alg=none` header | Returns `ErrAlgorithmUnsupported`. The header pre-check fires before the keyfunc; signing key is safe. |
| Calling `Validate` with a token from a forged `alg=HS256` header | Returns `ErrAlgorithmUnsupported`. Same defence as alg=none.                                          |
| Calling `Validate` with two valid IPs in different textual forms | Treats them as equal. `netip.Addr` semantic equality (FR-016).                                       |
| Calling `Store.Revoke` on an unknown JTI                     | No-op success. Idempotent.                                                                              |
| Calling `Store.ConsumeUse` on an unknown JTI                 | Returns `ErrTokenRevoked` (the package treats unknown JTIs as revoked from this store's perspective).   |
| Spawning `Cleanup` without a context                          | Compile error — `Cleanup` requires `ctx context.Context`.                                              |
| Forgetting to spawn `Cleanup` at all                          | The live map grows without bound. Validate still works (each token's `ExpiresAt` claim is checked), but memory accumulates. |
| Logging `tok.Encoded`                                         | Allowed (the encoded JWT is wire-safe — that's what JWT is for), but pointless. Static-message sentinels mean errors never contain JWT bytes. |
| Logging the signing key                                       | **DON'T.** The signing key is the secret. The package never logs it; consumer code MUST treat it as a `SecureBytes`-equivalent (it's a `*ecdsa.PrivateKey`, but the same redaction discipline applies). |
| Comparing errors via `err.Error() == "..."`                   | Forbidden by Constitution IX. Use `errors.Is(err, token.ErrXxx)` instead.                              |
| Re-using `*Token` after `Store.Add`                           | The store holds a reference; mutating the `*Token` afterwards races with `ConsumeUse`. Treat `*Token` as immutable post-Add. |

---

## What this package does NOT do (and where to look)

| Need                                                | Where to look                                                              |
|-----------------------------------------------------|----------------------------------------------------------------------------|
| Derive the JWT signing key from a passphrase        | SDD-01 (`internal/keys.DeriveJWTSigningKey`)                               |
| Sign or verify a `/claim` request                   | SDD-08 (`internal/transport/sign`)                                          |
| ECIES-encrypt the secret-fetch response             | SDD-09 (`internal/transport/ecies`)                                         |
| Acquire a `*SecureBytes` from a vault              | SDD-03 (`internal/vault`)                                                   |
| Send the Discord approval DM                        | SDD-11 (`internal/discord`)                                                 |
| Wire the HTTP routes                                | SDD-10 (`internal/server`)                                                  |
| Implement the `/claim` handler                      | SDD-12                                                                      |
| Implement the `/secrets/{name}` and `/revoke` handlers | SDD-13                                                                  |
| Implement the supervise CLI                         | SDD-23                                                                      |
| Audit-log the token operations                      | SDD-25 (`internal/logging` audit writer)                                    |
| Cap TTL upper bounds for interactive vs supervisor  | SDD-06 (`internal/config`) — the consumer enforces, not the token package  |

This package is a session-authority primitive. Consumers compose it
with their own request signing, Discord approval, vault, ECIES, and
audit logic.
