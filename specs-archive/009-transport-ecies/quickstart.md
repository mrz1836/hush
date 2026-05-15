# Quickstart: `internal/transport/ecies`

**Feature**: 009-transport-ecies
**Audience**: SDD-13 (server `/secrets/{name}` handler), SDD-16 (`hush request` client), SDD-19/SDD-21 (`hush supervise` consumers).

This is the shortest correct path from "I have a `*ecdsa.PublicKey` and a plaintext" or "I have a `*ecdsa.PrivateKey` and an envelope" to a wire-safe encrypt/decrypt round-trip. Two recipes, both end-to-end testable.

---

## Recipe 1 — Server-side encrypt (SDD-13)

You have:

- A `context.Context` from the HTTP handler (`r.Context()`).
- A `*ecdsa.PublicKey` from the JWT's `ephemeral_pubkey` claim (validated upstream by the auth middleware).
- A `*securebytes.SecureBytes` holding the freshly-decrypted secret value (acquired via `vault.Store.Get(secretName)`).

You want: an opaque `[]byte` envelope to write into the HTTP response body.

```go
package server

import (
    "fmt"
    "net/http"

    "github.com/mrz1836/hush/internal/transport/ecies"
    "github.com/mrz1836/hush/internal/vault"
)

func (s *Server) handleSecretFetch(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // 1. Derive the secret name from the path; pull the recipient pub from the JWT context.
    secretName := chi.URLParam(r, "name") // or your router's equivalent
    ephemeralPub := mustEphemeralPubFromContext(ctx) // your auth middleware set this

    // 2. Acquire the SecureBytes from the vault Store. Caller owns Destroy.
    sb, err := s.vault.Get(secretName)
    if err != nil {
        // ErrSecretNotFound → 404; ErrStoreDestroyed → 500.
        respondError(w, err)
        return
    }
    defer sb.Destroy()

    // 3. Encrypt inside the Use callback so the plaintext slice is exposed only to ecies.Encrypt.
    var (
        envelope []byte
        encErr   error
    )
    if useErr := sb.Use(func(plaintext []byte) {
        envelope, encErr = ecies.Encrypt(ctx, ephemeralPub, plaintext)
    }); useErr != nil {
        // securebytes.ErrDestroyed — should never happen here because we just got sb live.
        respondError(w, useErr)
        return
    }
    if encErr != nil {
        // Map by sentinel identity; do NOT include envelope or plaintext bytes in the response.
        switch {
        case errors.Is(encErr, ecies.ErrECIESEmptyPlaintext):
            // A vault entry was empty — operational anomaly; respond 500 with generic message.
        case errors.Is(encErr, ecies.ErrECIESInvalidRecipientKey):
            // The JWT's ephemeral_pubkey claim resolved to an unusable key — respond 401.
        default:
            // Any other path (ctx cancellation, etc.) → respond 500.
        }
        respondError(w, encErr)
        return
    }

    // 4. Write the opaque envelope as the response body.
    w.Header().Set("Content-Type", "application/octet-stream")
    w.Header().Set("Content-Length", fmt.Sprintf("%d", len(envelope)))
    if _, err := w.Write(envelope); err != nil {
        // Network error — log via SDD-05 logger; the secret is still safe (envelope is the
        // only artifact that left this process, and it's opaque without the matching priv).
    }
}
```

**Why each step matters**:

- **Step 2's `defer sb.Destroy()`** is the only thing that zeros the mlocked plaintext after the response is written. Without it, the SecureBytes lives until GC compaction zeros it implicitly (which is a race condition relative to a memory-forensics attacker).
- **Step 3's `sb.Use` wrap** confines the plaintext slice to a single call frame. After `Use` returns, no goroutine in the handler holds a reference to the plaintext bytes — only the envelope (which has no plaintext) and the still-live SecureBytes (whose backing memory the deferred `Destroy` will zero).
- **Step 3's `errors.Is` switch** is the FR-006 sentinel-identity contract. The handler classifies failures by sentinel, never by parsing `err.Error()`.
- **Step 4's `application/octet-stream` Content-Type** is the convention; the envelope is opaque bytes by design.

**What MUST NOT happen** in any branch:

- `respondError(w, err)` MUST NOT include any byte from the envelope or the plaintext in the response body. The error messages from this package are static category strings (`hush/transport/ecies: ...`) and contain no secret material — but if your `respondError` helper logs the error AND ALSO prints the original `r.URL` or `r.Header`, audit those for secret leakage.
- The `envelope` byte slice MUST NOT be logged (Constitution X). It is wire-safe but logging it pointlessly expands the diagnostic-surface.

---

## Recipe 2 — Client-side decrypt (SDD-16)

You have:

- A `context.Context` from the CLI command (`cmd.Context()`).
- A `*ecdsa.PrivateKey` — the per-session ephemeral private key (generated at session start, held in client-process memory).
- A `[]byte` envelope received over the wire (from the HTTP response body).

You want: a `*securebytes.SecureBytes` whose contents are the plaintext, ready to inject into a child process's environment via `--exec`.

```go
package cli

import (
    "context"
    "crypto/ecdsa"
    "errors"
    "fmt"
    "net/http"

    "github.com/mrz1836/hush/internal/transport/ecies"
    "github.com/mrz1836/hush/internal/vault/securebytes"
)

func fetchAndDecryptSecret(ctx context.Context, client *http.Client, ephemeralPriv *ecdsa.PrivateKey, url string) (*securebytes.SecureBytes, error) {
    // 1. Fetch the opaque envelope over the wire.
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err != nil {
        return nil, err
    }
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("hush: secret fetch returned status %d", resp.StatusCode)
    }

    envelope, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, err
    }

    // 2. Decrypt. Caller owns the returned SecureBytes' lifetime.
    sb, err := ecies.Decrypt(ctx, ephemeralPriv, envelope)
    if err != nil {
        // Map by sentinel identity. Do NOT log envelope or key bytes.
        switch {
        case errors.Is(err, ecies.ErrECIESEnvelopeTooShort):
            return nil, fmt.Errorf("hush: server returned a malformed (too-short) secret response")
        case errors.Is(err, ecies.ErrECIESDecryptFailed):
            return nil, fmt.Errorf("hush: secret decrypt failed (wrong key or tampered envelope)")
        case errors.Is(err, context.Canceled):
            return nil, ctx.Err()
        default:
            return nil, fmt.Errorf("hush: secret decrypt failed: %w", err)
        }
    }
    return sb, nil
}

// Caller pattern: fetch → defer Destroy → Use → exec.
func runWithSecret(ctx context.Context, ephemeralPriv *ecdsa.PrivateKey, url, secretName, childCmd string, childArgs []string) error {
    sb, err := fetchAndDecryptSecret(ctx, http.DefaultClient, ephemeralPriv, url)
    if err != nil {
        return err
    }
    defer sb.Destroy()

    var env []string
    if useErr := sb.Use(func(plaintext []byte) {
        // The string conversion materialises the secret into Go-string form for the OS exec contract.
        // Lifetime is bounded by the env slice that os.StartProcess consumes.
        env = append(env, secretName+"="+string(plaintext))
    }); useErr != nil {
        return useErr
    }

    cmd := exec.CommandContext(ctx, childCmd, childArgs...)
    cmd.Env = append(os.Environ(), env...)
    cmd.Stdin = os.Stdin
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}
```

**Why each step matters**:

- **Step 2's `defer sb.Destroy()`** is the client-side mirror of Recipe 1's defer: the plaintext lives in mlocked memory only as long as the SecureBytes does. After the child exits, the deferred Destroy zeros the backing buffer.
- **Step 2's `errors.Is` switch** maps sentinel identity to a user-visible message. The package's static error strings never contain envelope or key bytes (FR-007), so re-emitting them via `fmt.Errorf("...: %w", err)` is safe.
- **The `string(plaintext)` conversion in `runWithSecret`** is the deliberate one-time materialisation for the OS exec contract. The resulting string lives in the env-var slice for the duration of `cmd.Run()` and is GC'd after the child exits. The surrounding code MUST NOT print or log the env var (which is why we don't `fmt.Printf("env: %v", env)`).

**What MUST NOT happen** in any branch:

- The `envelope` byte slice MUST NOT be logged.
- The `*ecdsa.PrivateKey` MUST NOT be logged.
- The `string(plaintext)` form MUST NOT escape the `cmd.Env` slice (no logging, no struct fields, no goroutine captures beyond `os.StartProcess`).
- Any error returned to the user is wrapped via `fmt.Errorf("...: %w", err)` so consumers up the stack can `errors.Is` against `ecies.ErrECIESDecryptFailed` and `ecies.ErrECIESEnvelopeTooShort`. The wrapping preserves the sentinel identity.

---

## Recipe 3 — Test-time round-trip (for downstream packages)

For SDD-13 and SDD-16 integration tests that need to verify their own handler/CLI logic without exercising the full network stack, the round-trip primitive is:

```go
package server_test

import (
    "context"
    "crypto/ecdsa"
    "crypto/rand"
    "testing"

    "github.com/decred/dcrd/dcrec/secp256k1/v4"
    "github.com/mrz1836/hush/internal/transport/ecies"
    "github.com/mrz1836/hush/internal/vault/securebytes"
    "github.com/stretchr/testify/require"
)

func TestSecret_HappyPath_ECIESPayload(t *testing.T) {
    ctx := context.Background()

    // 1. Generate a fresh ephemeral keypair for the test.
    priv, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)
    require.NoError(t, err)

    plaintext := []byte("hush-test-secret-value")

    // 2. Server-side encrypt.
    envelope, err := ecies.Encrypt(ctx, &priv.PublicKey, plaintext)
    require.NoError(t, err)
    require.GreaterOrEqual(t, len(envelope), 85, "envelope must meet BIE1 minimum")

    // 3. Client-side decrypt.
    sb, err := ecies.Decrypt(ctx, priv, envelope)
    require.NoError(t, err)
    require.NotNil(t, sb)
    defer sb.Destroy()

    // 4. Assert byte-for-byte equality via Use.
    require.NoError(t, sb.Use(func(b []byte) {
        require.Equal(t, plaintext, b)
    }))
}
```

This is the canonical pattern for any downstream test that needs a wire-shape envelope. The test uses `ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)` directly (no `internal/keys` dependency); the package operates on raw `*ecdsa.PrivateKey` regardless of derivation provenance.

---

## Common mistakes (and how the package's contract prevents them)

| Mistake                                                       | What the package does                                                                                  |
|---------------------------------------------------------------|--------------------------------------------------------------------------------------------------------|
| Calling `Encrypt` with `len(plaintext) == 0`                  | Returns `ErrECIESEmptyPlaintext` immediately. No envelope is produced for empty inputs.                |
| Calling `Encrypt` with `recipientPub == nil`                   | Returns `ErrECIESInvalidRecipientKey`. No plaintext is allocated or copied.                            |
| Calling `Decrypt` with `envelope = nil` or `envelope = []byte{}` | Returns `ErrECIESEnvelopeTooShort`. No cryptographic primitive is invoked.                              |
| Forgetting to `Destroy` the returned `*SecureBytes`           | The runtime finalizer (registered by `securebytes.New`) zeros the backing memory on GC. **But you SHOULD `defer sb.Destroy()`** — finalizers are best-effort, not deterministic. |
| Logging `err.Error()` from a Decrypt failure                  | Safe — the error messages are static category strings, never envelope/plaintext/key bytes (FR-007).     |
| Logging the `envelope` bytes                                  | Allowed (the envelope is wire-safe), but pointless — opaque bytes have no diagnostic value, and logging expands the surface area. |
| Logging the `plaintext` bytes inside `sb.Use`                 | **DON'T.** The plaintext is the secret. Constitution X redaction is type-driven for `*SecureBytes`; once you've called `.Use(...)`, you've left the redaction-safe surface and your code is responsible for not logging the slice. |
| Reusing the same ephemeral keypair across multiple sessions   | Allowed by the package, but operationally wrong — see SDD-16 for the per-session keypair lifecycle.    |
| Comparing errors via `err.Error() == "..."`                   | Forbidden by Constitution IX. Use `errors.Is(err, ecies.ErrXxx)` instead.                              |

---

## What this package does NOT do (and where to look)

| Need                                                | Where to look                                                              |
|-----------------------------------------------------|----------------------------------------------------------------------------|
| Generate a session ephemeral keypair                | SDD-16 (`hush request` ephemeral keypair lifecycle)                         |
| Sign or verify a `/claim` request                   | `internal/transport/sign` (SDD-08)                                          |
| Validate a JWT and extract `ephemeral_pubkey` claim | SDD-07 (`internal/token`)                                                   |
| Acquire a `*SecureBytes` from a vault              | SDD-03 (`internal/vault`)                                                   |
| Inject a secret into a child process via `--exec`   | SDD-16 (`hush request --exec` injection safety)                             |
| Audit-log a secret fetch                            | SDD-13 (`/secrets/{name}` handler audit hook)                               |
| Hold the secret across child restarts (supervisor)  | SDD-19/SDD-21 (`hush supervise` JWT + ephemeral-key retention)              |

This package is a wire-level cryptographic primitive. Consumers compose it with their own session, audit, and lifecycle logic.
