# Contract: `internal/transport/ecies` exported API

**Feature**: 009-transport-ecies
**Status**: Locked at SDD-09; mirrored into `docs/PACKAGE-MAP.md` once the implement commit lands.

This is the contract every downstream package (SDD-13 server `/secrets/{name}` handler, SDD-16 `hush request` client, future supervisor consumers via SDD-19/SDD-21) depends on. Changes after SDD-09 lands require a new SDD chunk; consumers may rely on every signature, every sentinel identity, and the round-trip property below.

SDD-09 (`internal/transport/ecies`) is a sibling sub-package of SDD-08 (`internal/transport/sign`) under the shared `internal/transport/` parent. Future ECIES-adjacent helpers (e.g., a streaming `Decrypt` for very large secrets) MAY land as additional symbols in this package without breaking the existing contract; symbol REMOVAL or signature CHANGES require a new SDD chunk.

---

## Package path

```
github.com/mrz1836/hush/internal/transport/ecies
```

---

## Exported functions

### `func Encrypt(ctx context.Context, recipientPub *ecdsa.PublicKey, plaintext []byte) ([]byte, error)`

```go
func Encrypt(ctx context.Context, recipientPub *ecdsa.PublicKey, plaintext []byte) ([]byte, error)
```

**Inputs**:
- `ctx` — checked once at entry; pre-cancellation returns `ctx.Err()` immediately. Mid-operation cancellation is NOT honored (the operation is short and CPU-bound).
- `recipientPub` — a `*ecdsa.PublicKey` whose curve is secp256k1 (`secp256k1.S256()`). Origin: the client's per-request ephemeral keypair, registered in the JWT's `ephemeral_pubkey` claim.
- `plaintext` — the bytes to encrypt. MUST be non-empty. Any non-empty length is accepted; the package imposes no upper bound (bound the size in the caller if needed).

**Output**:
- On success: an opaque `[]byte` envelope of length ≥ 85 bytes (the BIE1 minimum). The envelope is the bytes that travel over the wire.
- On failure: `nil, err`. NEVER returns a partial byte sequence with a non-nil error.

**Failure cases**:
- `ctx.Err() != nil` → `ctx.Err()` returned verbatim. `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)` evaluate as expected per FR-013.
- `recipientPub == nil` → `ErrECIESInvalidRecipientKey`.
- `recipientPub.Curve == nil` or `recipientPub.Curve != secp256k1.S256()` → `ErrECIESInvalidRecipientKey`.
- `len(plaintext) == 0` → `ErrECIESEmptyPlaintext`.

All four failure cases above fire BEFORE any cryptographic primitive is invoked AND BEFORE any plaintext-bearing buffer is allocated. The package therefore zeros nothing on these paths (there is nothing to zero).

**Side effects (intentional)**:
- Reads `crypto/rand.Reader` to generate the ephemeral keypair.
- Allocates a fresh `[]byte` for the returned envelope (caller-owned).
- Allocates internal plaintext-bearing buffers that are zeroed via `defer secureZero` BEFORE return on every code path.

**Side effects (explicitly NOT done)**:
- Does NOT mutate the caller's `plaintext` slice.
- Does NOT cache or memoise the recipient public key across calls.
- Does NOT log anything (FR-014).
- Does NOT spawn goroutines.

**Concurrency**: safe to call concurrently. Multiple goroutines may invoke `Encrypt` with their own keys and plaintexts simultaneously without synchronisation. `crypto/rand.Reader` is the stdlib's concurrent-safe entropy source.

**Determinism**: `Encrypt` is **non-deterministic** by design — each call generates a fresh ephemeral keypair, so two calls on the same `(pub, plaintext)` produce two different envelopes. Both envelopes decrypt to the same plaintext under the matching `priv`. This is the standard ECIES property; a deterministic Encrypt would leak whether two calls used the same plaintext.

### `func Decrypt(ctx context.Context, recipientPriv *ecdsa.PrivateKey, envelope []byte) (*securebytes.SecureBytes, error)`

```go
func Decrypt(ctx context.Context, recipientPriv *ecdsa.PrivateKey, envelope []byte) (*securebytes.SecureBytes, error)
```

**Inputs**:
- `ctx` — checked once at entry; pre-cancellation returns `ctx.Err()` immediately.
- `recipientPriv` — a `*ecdsa.PrivateKey` whose curve is secp256k1. Origin: the client's per-session ephemeral keypair (held in client-process memory only).
- `envelope` — the opaque bytes received over the wire. Any byte sequence is accepted as input; the package validates and rejects malformed bytes via the sentinel catalogue.

**Output**:
- On success: a fresh `*securebytes.SecureBytes` whose contents are the original plaintext. The caller owns the SecureBytes lifetime and MUST eventually call `Destroy()` (typically via `defer sb.Destroy()` at the call site).
- On failure: `nil, err`.

**Failure cases**:
- `ctx.Err() != nil` → `ctx.Err()` returned verbatim.
- `len(envelope) < 85` (the BIE1 minimum) → `ErrECIESEnvelopeTooShort`. **Fires BEFORE any cryptographic primitive is invoked** (FR-005, SC-004).
- Magic bytes mismatch (`envelope[0:4] != "BIE1"`) → `ErrECIESDecryptFailed`.
- Malformed compressed ephemeral pubkey (any failure in `secp256k1.ParsePubKey`) → `ErrECIESDecryptFailed`.
- Ciphertext length not a positive multiple of 16 → `ErrECIESDecryptFailed`.
- HMAC verification failure (wrong key, tampered envelope) → `ErrECIESDecryptFailed`.
- PKCS#7 unpad failure (last-byte > BlockSize, last-byte == 0, padding-bytes mismatch) → `ErrECIESDecryptFailed`.
- `securebytes.New` failure (e.g., mlock failed) → `ErrECIESDecryptFailed`.

**Sentinel matching**: `errors.Is(err, ErrECIESDecryptFailed)` returns `true` for **every** cryptographic failure above. Consumers cannot distinguish "wrong key" from "tampered envelope" — by design (FR-004, no failure-shape leakage).

**Panic-free**: `Decrypt` is panic-free under any byte input (FR-012, asserted by `FuzzECIESDecrypt` 60 s gate).

**Concurrency**: safe to call concurrently. The function holds no state.

**Ownership transfer**: a successful return transfers ownership of the recovered plaintext to the caller via `*SecureBytes`. The package retains NO parallel handle, NO finalizer registration, and NO cached reference. After return, the package's intermediate buffers are zeroed.

---

## Sentinel error catalogue

All sentinels are `var ErrXxx = errors.New("hush/transport/ecies: <message>")` declarations. Compare via `errors.Is`. **No wrap relationships** between sentinels — each category is independent.

| Sentinel                       | Static message                                | Triggered by                                                                          |
|--------------------------------|-----------------------------------------------|---------------------------------------------------------------------------------------|
| `ErrECIESDecryptFailed`        | `hush/transport/ecies: ECIES decrypt failed`   | Every cryptographic decrypt failure: BIE1 magic mismatch, malformed compressed pubkey, MAC mismatch, AES-block-shape failure, PKCS#7 unpad failure, SecureBytes wrap failure. **Wrong key and tampered envelope share this sentinel by design** (FR-004). |
| `ErrECIESEnvelopeTooShort`     | `hush/transport/ecies: envelope too short`     | Envelope length below the documented minimum (85 bytes). Fires BEFORE any cryptographic primitive (FR-005).                            |
| `ErrECIESEmptyPlaintext`       | `hush/transport/ecies: empty plaintext`        | `Encrypt` rejection: zero-length plaintext input (FR-015).                            |
| `ErrECIESInvalidRecipientKey`  | `hush/transport/ecies: invalid recipient key`  | `Encrypt` rejection: nil pub, nil curve, or non-secp256k1 curve (FR-015a). |

The error messages are static strings known at compile time; the package never formats envelope/plaintext/key bytes into any error. The `TestECIES_NoLeakOnError` test (mandatory; in the test floor) asserts the project sentinel `SECRET_SHOULD_NEVER_APPEAR_9` is absent from `err.Error()` AND from any wrap-chain descended via `errors.Unwrap`.

---

## Behavioural invariants (testable contract)

| Invariant | Spec ref | Test name (in tasks phase) |
|-----------|----------|----------------------------|
| Round-trip recovers plaintext for 1-byte size | FR-001 + FR-002, SC-001 | `TestECIES_RoundTrip_1B` |
| Round-trip recovers plaintext for 1-KiB size | FR-001 + FR-002, SC-001 | `TestECIES_RoundTrip_1KB` |
| Round-trip recovers plaintext for 1-MiB size | FR-001 + FR-002, SC-001 | `TestECIES_RoundTrip_1MB` |
| Two `Encrypt` calls on same `(pub, pt)` produce different envelopes | FR-001 + ECIES randomisation property | `TestECIES_EncryptIsRandomised` |
| Envelope contains no plaintext substring (sentinel-byte search) | User Story 1 acceptance scenario 3 | `TestECIES_NoPlaintextSubstringInEnvelope` |
| `Encrypt` rejects empty plaintext with `ErrECIESEmptyPlaintext` | FR-015 | `TestECIES_EncryptRejectsEmpty` |
| `Encrypt` rejects nil pub with `ErrECIESInvalidRecipientKey` | FR-015a | `TestECIES_EncryptRejectsNilPub` |
| `Encrypt` rejects wrong-curve pub with `ErrECIESInvalidRecipientKey` | FR-015a | `TestECIES_EncryptRejectsWrongCurvePub` |
| `Encrypt` zeros internal buffers on error path | FR-008, SC-008 | `TestECIES_EncryptZeroesInternalBuffersOnError` |
| `Encrypt` zeros internal buffers on happy path | FR-008, SC-008 | `TestECIES_EncryptZeroesInternalBuffersOnSuccess` |
| `Decrypt` with wrong priv returns `ErrECIESDecryptFailed` | FR-004, SC-002 | `TestECIES_DecryptWrongKey_Fails` |
| `Decrypt` with single-byte-flipped envelope returns `ErrECIESDecryptFailed` | FR-004, SC-003 | `TestECIES_DecryptMangledEnvelope_Fails` (subtests for magic, pubkey, ct, mac positions) |
| `Decrypt` with truncated envelope (1 byte short) returns `ErrECIESDecryptFailed` | FR-004 | `TestECIES_DecryptTruncatedEnvelope_Fails` |
| `Decrypt` with appended byte returns `ErrECIESDecryptFailed` | FR-004 + Edge Case "trailing byte" | `TestECIES_DecryptAppendedByte_Fails` |
| `Decrypt` with envelope < 85 bytes returns `ErrECIESEnvelopeTooShort` | FR-005, SC-004 | `TestECIES_DecryptEmptyEnvelope_TooShort` (subtests for lengths 0, 1, 84) |
| `Decrypt` returns `*SecureBytes` with byte-equal contents; Destroy then Use returns ErrDestroyed | FR-002 + FR-003, SC-007 | `TestECIES_DecryptReturnsSecureBytes` |
| `Decrypt` with cancelled context returns `ctx.Err()` (errors.Is matches) | FR-013, SC-011 | `TestECIES_DecryptRespectsCancelledContext` |
| `Encrypt` with cancelled context returns `ctx.Err()` (errors.Is matches) | FR-013, SC-011 | `TestECIES_EncryptRespectsCancelledContext` |
| `Decrypt` with deadline-exceeded context returns `context.DeadlineExceeded` | FR-013, SC-011 | `TestECIES_DecryptRespectsDeadlineContext` |
| Sentinel `SECRET_SHOULD_NEVER_APPEAR_9` absent from any error message | FR-007, SC-006, SC-010 | `TestECIES_NoLeakOnError` |
| `Decrypt` is panic-free under fuzz pressure (≥60 s) | FR-012, SC-005 | `FuzzECIESDecrypt` (60 s CI gate) |
| Concurrent Encrypt/Decrypt under `-race` is race-clean | FR-011 + Constitution IX | `TestECIES_ConcurrentRoundTrip` |
| Coverage of `internal/transport/ecies` is 100% | Constitution VIII Critical band | enforced by `codecov.yml` |

---

## Composition recipes

The package's two functions are independent primitives. Consumers compose them with their own session/JWT/storage logic. The two canonical recipes:

### Server `Encrypt` (SDD-13's `/secrets/{name}` handler)

```go
import (
    "github.com/mrz1836/hush/internal/transport/ecies"
    "github.com/mrz1836/hush/internal/vault"
    "github.com/mrz1836/hush/internal/vault/securebytes"
)

func handleSecretFetch(w http.ResponseWriter, r *http.Request, vaultStore vault.Store, ephemeralPub *ecdsa.PublicKey, secretName string) {
    ctx := r.Context()

    sb, err := vaultStore.Get(secretName)
    if err != nil {
        // 404 for not-found; 500 for store-destroyed
        return
    }
    defer sb.Destroy()

    var envelope []byte
    err = sb.Use(func(plaintext []byte) {
        envelope, err = ecies.Encrypt(ctx, ephemeralPub, plaintext)
    })
    if err != nil {
        // log via SDD-05 logger; respond with a generic 500 (no secret leaked)
        return
    }

    w.Header().Set("Content-Type", "application/octet-stream")
    w.Write(envelope)
}
```

The plaintext slice is exposed only inside the `sb.Use` callback. After `Use` returns, the plaintext reference is no longer reachable from the handler — only the envelope (which contains no plaintext) and the still-live `*SecureBytes` (whose backing memory will be zeroed by the deferred `Destroy`).

### Client `Decrypt` (SDD-16's `hush request --exec` flow)

```go
import (
    "github.com/mrz1836/hush/internal/transport/ecies"
)

func fetchSecretAndInjectEnv(ctx context.Context, ephemeralPriv *ecdsa.PrivateKey, secretName string, envelope []byte) ([]string, error) {
    sb, err := ecies.Decrypt(ctx, ephemeralPriv, envelope)
    if err != nil {
        // Map err to a user-visible category; do NOT log envelope or key bytes.
        return nil, fmt.Errorf("hush: secret fetch failed for %s: %w", secretName, err)
    }
    defer sb.Destroy()

    var entry string
    err = sb.Use(func(plaintext []byte) {
        // The string conversion materialises the secret into Go-string form for the
        // exec contract. Lifetime is bounded by the env-slice that os.StartProcess consumes.
        entry = secretName + "=" + string(plaintext)
    })
    if err != nil {
        return nil, err
    }
    return []string{entry}, nil
}
```

The `string(plaintext)` conversion is the single load-bearing string-conversion in the `hush request` flow. Its lifetime is bounded by the env-var slice the OS exec consumes; it is never logged or otherwise persisted.

---

## Non-contract (explicit non-promises)

- **No streaming variant.** `Encrypt`/`Decrypt` operate on whole-buffer inputs. A streaming `EncryptStream`/`DecryptStream` is a future hardening pass; v0.1.0 secret values fit in memory comfortably.
- **No exported envelope-shape types.** The envelope is `[]byte`; consumers pass it across the wire as bytes. The internal layout (magic / ephPub / ciphertext / mac) is not exposed; consumers MUST NOT parse or modify the envelope themselves.
- **No exported helpers for the BIE1 primitives.** `Encrypt`/`Decrypt` are the only entry points. Consumers MUST NOT reach into the package for the KDF, the ECDH, or the PKCS#7 helpers — those are file-private and may change without notice.
- **No `IsBIE1Envelope(b []byte) bool` predicate.** A length+magic check is a 2-line consumer-side helper if a consumer ever needs it; the package does not export one because doing so would invite consumer code that branches on envelope shape (a layering anti-pattern).
- **No support for non-secp256k1 curves.** Constitution III pins secp256k1; passing a key on a different curve produces `ErrECIESInvalidRecipientKey` (Encrypt) or `ErrECIESDecryptFailed` (Decrypt — the curve mismatch surfaces as a `ParsePubKey` rejection or a MAC mismatch).
- **No environment-variable reads, no file I/O, no network I/O.** The package is purely in-memory (modulo the entropy source `crypto/rand.Reader`).
- **No package-level metrics.** Constitution X forbids; consumers derive operational counters from sentinel-error identity.
- **No envelope / plaintext / key byte appearance in any error message.** FR-007.
- **No log lines and no logger dependency.** FR-014.
- **No upper bound on plaintext size beyond what the underlying primitives support.** AES-256-CBC tolerates plaintexts up to (theoretically) 2^32 blocks; consumers that need a hard cap enforce it themselves.
- **No deterministic Encrypt mode.** Encrypt is randomised by design (fresh ephemeral keypair per call); consumers that need determinism for testing use a deterministic-rand seam at their own layer.
- **No on-disk state.** The package writes nothing to disk. The fuzz seed corpus is read-only test data.
- **No goroutines.** `Encrypt` and `Decrypt` are synchronous CPU operations.
- **No cache or memoisation of recipient keys.** FR-011.

---

## Deprecation policy

This contract is **frozen** at SDD-09 ship. Adding a new sentinel error to the catalogue is non-breaking iff the new sentinel surfaces a new failure category that current consumers do not depend on the absence of (e.g., a future tightening of envelope-format rules). Removing or renaming any exported symbol requires a new SDD chunk; SDD-13 and SDD-16 both depend on the surface above.

Adding a new exported function (e.g., a future streaming variant) is non-breaking and may land via a separate chunk. Any change to the BIE1 envelope shape itself (magic, KDF, cipher, MAC) is a wire-format break and would require a coordinated SDD-13 + SDD-16 update plus a deprecation notice on this contract.
