# Quickstart — `internal/vault/securebytes` (SDD-02)

This file shows the canonical wiring of `internal/vault/securebytes`
from the boundary-code perspective. It is **descriptive of the
locked API** in [contracts/securebytes-api.md](./contracts/securebytes-api.md),
not an implementation guide for the package itself.

If you are reading this to consume the package (SDD-03 vault, SDD-07
JWT, SDD-09 ECIES, SDD-13 server handlers, SDD-16 client request,
SDD-21 supervisor grace cache), this is your reference. If you are
implementing the package, see `spec.md`, `plan.md`, `research.md`,
and `data-model.md` instead.

---

## 1. Wrap a secret derived elsewhere (SDD-03 vault → vault encryption key)

```go
import (
    "context"
    "fmt"

    "github.com/mrz1836/hush/internal/keys"
    "github.com/mrz1836/hush/internal/vault/securebytes"
)

func loadVaultEncKey(ctx context.Context, passphrase, salt []byte) (*securebytes.SecureBytes, error) {
    seed, err := keys.DeriveMasterSeed(ctx, passphrase, salt)
    if err != nil {
        return nil, fmt.Errorf("master seed: %w", err)
    }
    seedSB, err := securebytes.New(seed) // seed is zeroed inside New
    if err != nil {
        return nil, fmt.Errorf("wrap seed: %w", err)
    }
    defer func() { _ = seedSB.Destroy() }()

    encKey, err := keys.DeriveVaultEncKey(seed) // (called pre-wrap if you'd rather)
    if err != nil {
        return nil, err
    }
    return securebytes.New(encKey) // encKey is zeroed inside New
}
```

Key point: every call to `securebytes.New` zeros the input slice
before returning. Callers do NOT need a `defer secureZero(seed)` —
the constructor handles it. The caller's responsibility is to call
`Destroy` on the returned container when done.

---

## 2. Borrow the bytes for a single bounded operation

```go
import (
    "crypto/cipher"
)

func encryptOnce(ctx context.Context, encKey *securebytes.SecureBytes, plaintext []byte) ([]byte, error) {
    var ciphertext []byte
    err := encKey.Use(func(key []byte) {
        // key is the live mlocked buffer. Do NOT retain it past
        // this callback. Use it for one bounded operation.
        block, _ := aes.NewCipher(key)
        gcm, _ := cipher.NewGCM(block)
        nonce := make([]byte, gcm.NonceSize())
        // ... fill nonce from crypto/rand
        ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
    })
    if err != nil {
        return nil, err // ErrDestroyed if the container has been destroyed
    }
    return ciphertext, nil
}
```

Notes:

- `Use` returns `ErrDestroyed` if the container is already
  destroyed; the callback is NOT invoked in that case.
  `errors.Is(err, securebytes.ErrDestroyed)` is the supported
  detection.
- `Use` holds the container's mutex for the whole callback
  duration — concurrent `Use` callers serialise. For most
  cryptographic operations (microseconds), this is invisible.
- Do NOT capture `key` into a closure that outlives the callback.
  Do NOT pass `key` to a goroutine that may run after the
  callback returns.

---

## 3. Explicit destroy at a known boundary (HTTP handler return / SIGTERM)

```go
func handleSecretFetch(w http.ResponseWriter, r *http.Request, store SecretStore) {
    sb, err := store.Get(r.Context(), secretName)
    if err != nil {
        http.Error(w, "not found", http.StatusNotFound)
        return
    }
    defer func() { _ = sb.Destroy() }() // zero-on-handler-return

    // ... encrypt the payload to the client's ECIES pubkey, write the response
}
```

Notes:

- `defer sb.Destroy()` is the canonical pattern at every handler
  boundary, validator return, and supervisor child-launch
  boundary.
- `Destroy` is idempotent — if a downstream helper already called
  it, the deferred call is harmless.

---

## 4. Render protection — proof that secrets do not leak into logs

```go
import (
    "log/slog"
)

logger.Info("issued token",
    "request_id", reqID,
    "secret_value", sb,          // renders as "[redacted]"
)

// Or via fmt:
log.Printf("scope=%v secret=%s", scope, sb) // "...secret=[redacted]"

// Or via JSON:
out, _ := json.Marshal(struct {
    Name   string                  `json:"name"`
    Secret *securebytes.SecureBytes `json:"secret"`
}{Name: "ANTHROPIC_API_KEY", Secret: sb})
// out == `{"name":"ANTHROPIC_API_KEY","secret":"[redacted]"}`
```

The redaction is type-driven — the developer cannot forget to
redact, because the type itself refuses to render in plaintext.
The same renders identically before and after `Destroy` (FR-017).

---

## 5. Failure paths

```go
import "errors"

err := sb.Use(func(b []byte) {
    // ...
})
switch {
case errors.Is(err, securebytes.ErrDestroyed):
    // The container has been destroyed; the callback was NOT invoked.
    // Caller likely has a lifecycle bug — log + return error.
case err != nil:
    // No other error class is documented for v0.1.0; if you see one,
    // upstream contract drifted.
default:
    // success
}

sb2, err := securebytes.New(plaintext)
switch {
case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.ENOMEM):
    // mlock denied — RLIMIT_MEMLOCK exhausted. Caller MUST treat
    // this as fatal; falling back to unprotected memory is forbidden
    // by Spec FR-005.
case err != nil:
    // wrap and return
default:
    defer func() { _ = sb2.Destroy() }()
    // ...
}
```

---

## 6. What this package does NOT do

- It does **not** read or write files.
- It does **not** generate randomness (`crypto/rand` is the
  caller's responsibility — the container holds whatever bytes
  it's given).
- It does **not** sign, encrypt, decrypt, hash, or otherwise
  transform the payload. It is a holder, not a crypto engine.
- It does **not** log. No logger is imported. The static error
  messages identify the failure mode (sentinel values), never
  the payload.
- It does **not** allocate a goroutine. Concurrency is exclusively
  the caller's.
- It does **not** persist any payload across process boundaries.
  A container is owned by exactly one process; cross-process
  sharing is out of scope.
- It does **not** expose a `Bytes()` accessor or any other path
  to extract the raw payload — the `Use` callback is the only
  read path (Spec FR-006 / FR-007).

If your consumer code expects any of those behaviours, the
expectation is wrong — the responsibility lives in a different
package (vault, transport, token, supervise, or the caller's
bootstrap glue).

---

## 7. Verifying the wiring locally

After SDD-02 lands, the smoke test for a consumer is:

```bash
# from repo root
magex format:fix && magex lint && magex test:race
go test -cover ./internal/vault/securebytes/      # expect 100.0%
```

A green run on all three commands is the contract that downstream
SDDs can rely on. If any of them fails, fix
`internal/vault/securebytes` before consuming it from downstream
code.

The constitutional gate also asserts `internal/vault/...` carries
100% coverage at the v0.1.0 release-readiness check; this package
contributes exactly 100.0% of its own surface to that gate.

---

## 8. Composition pattern for downstream secret-bearing types

Downstream types that hold secret material (e.g. `internal/token`'s
JWT signing key, `internal/transport`'s ECIES envelope key) compose
`SecureBytes` rather than embedding raw `[]byte`:

```go
// In internal/token (SDD-07):

type JWTSigner struct {
    privKey *securebytes.SecureBytes // the secp256k1 scalar
    // ... other (non-secret) fields ...
}

func (s *JWTSigner) Sign(claims Claims) (string, error) {
    var token string
    err := s.privKey.Use(func(scalarBytes []byte) {
        // construct *ecdsa.PrivateKey from scalarBytes for this call only;
        // sign claims; encode token
    })
    return token, err
}
```

This pattern means:

1. The wrapping type need not implement its own redaction —
   `slog.LogValue` recurses, so any struct containing `*SecureBytes`
   that gets logged will show `"[redacted]"` for the secret field.
2. Lifetime hygiene is centralised — the wrapping type's `Close()`
   or `Destroy()` simply calls through to the embedded
   `SecureBytes.Destroy()`.
3. Consumers do not need to know about `mlock`, `munlock`, or the
   GC finalizer — it's all behind the `*SecureBytes` boundary.
