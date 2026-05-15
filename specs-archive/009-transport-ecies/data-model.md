# Phase 1 Data Model: `internal/transport/ecies`

**Feature**: 009-transport-ecies
**Date**: 2026-04-28

This document captures the types, the BIE1 envelope shape, the per-call lifecycle, and the sentinel-error catalogue that downstream consumers (SDD-13, SDD-16) depend on. There is no persistent data model — the package is stateless — so this file is the static contract of bytes-on-the-wire and types-in-memory.

---

## 1. Types (locked at SDD-09)

### 1.1 Public types

The package exports **zero** types of its own. The two function signatures use:

| Type                                    | Source                                           | Why this type                                                                                              |
|-----------------------------------------|--------------------------------------------------|------------------------------------------------------------------------------------------------------------|
| `*ecdsa.PrivateKey`                     | `crypto/ecdsa` (stdlib)                          | Same shape `internal/keys.DeriveClientKey` (SDD-01) returns; consumers can pass derived keys directly       |
| `*ecdsa.PublicKey`                      | `crypto/ecdsa` (stdlib)                          | The public half of the above; embedded inside `*ecdsa.PrivateKey.PublicKey`                                 |
| `*securebytes.SecureBytes`              | `internal/vault/securebytes` (SDD-02)            | Mlocked, zero-on-Destroy plaintext container; caller owns lifetime                                         |
| `[]byte` (envelope on encrypt return)   | stdlib                                           | The opaque BIE1 envelope; treated as caller-owned bytes (callers may copy, transmit, or discard freely)    |
| `error`                                 | stdlib                                           | Returned via the sentinel-error catalogue (§3)                                                              |
| `context.Context`                       | stdlib                                           | Cancellation surface (FR-013)                                                                              |

### 1.2 File-private types and constants

| Symbol                | Kind     | Purpose                                                                                       |
|-----------------------|----------|-----------------------------------------------------------------------------------------------|
| `minEnvelopeSize`     | `const`  | Minimum envelope length: `4 + 33 + aes.BlockSize + sha256.Size` = `4 + 33 + 16 + 32` = `85`   |
| `bie1Magic`           | `var`    | The 4-byte ASCII literal `[]byte{'B','I','E','1'}` (set-once at package load, never mutated)  |
| `secureZero`          | `func`   | `func(buf []byte)` — zeros every byte of `buf`                                                |
| `secureZeroBigInt`    | `func`   | `func(n *big.Int)` — zeros the underlying word slice via `SetInt64(0)` plus defensive copy zero |
| `compressPubKey`      | `func`   | `func(pub *ecdsa.PublicKey) ([]byte, error)` — returns the 33-byte compressed encoding         |
| `parseCompressedPubKey` | `func` | `func(b []byte) (x, y *big.Int, err error)` — uses `secp256k1.ParsePubKey`; returns `ErrECIESDecryptFailed` on any failure |
| `ecdh`                | `func`   | `func(curve elliptic.Curve, peerX, peerY, scalar *big.Int) []byte` — returns the 32-byte shared X via `Curve.ScalarMult` + `FillBytes` |
| `kdf`                 | `func`   | `func(sharedX []byte) (iv, keyE, keyM []byte)` — splits `SHA-512(sharedX)` into the three slices |
| `pkcs7Pad`            | `func`   | `func(plaintext []byte, blockSize int) []byte` — appends padding bytes per RFC 5652 §6.3       |
| `pkcs7Unpad`          | `func`   | `func(padded []byte, blockSize int) ([]byte, error)` — strips padding; returns `ErrECIESDecryptFailed` on any malformedness |

None of the file-private types or helpers are exported. They MAY be reorganised across files (`ecies.go` is the only production source file beyond `errors.go` + `doc.go`) without affecting the public contract.

---

## 2. The BIE1 envelope (the bytes-on-the-wire data model)

### 2.1 Layout

```text
+--------+--------------------+--------------------+--------+
| magic  | ephemeralPubKey    | ciphertext         | mac    |
| (4 B)  | (33 B compressed)  | (N × 16 B)         | (32 B) |
+--------+--------------------+--------------------+--------+
   ↑           ↑                      ↑                  ↑
   |           |                      |                  |
   "BIE1"      0x02|0x03 ‖ X(32)       AES-256-CBC of    HMAC-SHA256(keyM,
                                       PKCS#7-padded     magic ‖ ephPub ‖
                                       plaintext         ciphertext)
```

| Field            | Bytes               | Source                                                                                  |
|------------------|---------------------|-----------------------------------------------------------------------------------------|
| `magic`          | 4                   | Literal `"BIE1"` (`0x42 0x49 0x45 0x31`)                                                 |
| `ephemeralPubKey`| 33                  | Compressed encoding of `ephPriv * generator`: `0x02|0x03 ‖ X.FillBytes(32)`              |
| `ciphertext`     | N × `aes.BlockSize` | AES-256-CBC of the PKCS#7-padded plaintext under `(keyE, iv)`; N ≥ 1 (16 bytes minimum)  |
| `mac`            | 32                  | HMAC-SHA256(`keyM`, `magic ‖ ephemeralPubKey ‖ ciphertext`)                              |

### 2.2 Sizing math

| Plaintext length L | PKCS#7 padding bytes  | Padded length (multiple of 16)   | Ciphertext length | Envelope length        |
|--------------------|------------------------|----------------------------------|-------------------|------------------------|
| 1                  | 15                     | 16                               | 16                | **85** (minimum)       |
| 15                 | 1                      | 16                               | 16                | 85                     |
| 16                 | 16 (full block)        | 32                               | 32                | 101                    |
| 32                 | 16                     | 48                               | 48                | 117                    |
| 1 024              | 16                     | 1 040                            | 1 040             | 1 109                  |
| 1 048 576          | 16                     | 1 048 592                        | 1 048 592         | 1 048 661              |

`minEnvelopeSize = 85` is the floor — no plaintext can produce a smaller envelope.

### 2.3 KDF derivation (recap from R-003)

```text
sharedX  := ECDH(ephemeralPriv, recipientPub).X     // 32-byte big-endian (FillBytes(32))
H        := SHA-512(sharedX)                        // 64 bytes
iv       := H[ 0:16]                                // 16 bytes — AES-CBC IV
keyE     := H[16:48]                                // 32 bytes — AES-256 key
keyM     := H[48:64]                                // 16 bytes — HMAC-SHA256 key (RFC 2104 zero-pads)
```

### 2.4 Verification ordering (decrypt)

1. **Length gate** (`len(envelope) >= 85`) → `ErrECIESEnvelopeTooShort`
2. **Magic gate** (`envelope[0:4] == "BIE1"`) → `ErrECIESDecryptFailed`
3. **Pubkey parse** (`secp256k1.ParsePubKey(envelope[4:37])`) → `ErrECIESDecryptFailed`
4. **CT-shape gate** (`(ctEnd - 37) > 0 && (ctEnd - 37) % 16 == 0`) → `ErrECIESDecryptFailed`
5. **MAC gate** (`hmac.Equal(envelope[ctEnd:], expected)`) → `ErrECIESDecryptFailed`
6. **CBC decrypt** → unpadded plaintext via PKCS#7 strip → `ErrECIESDecryptFailed` on bad padding

The length gate runs BEFORE the magic gate (length too short cannot be cryptography). The magic gate runs BEFORE the pubkey parse (a wrong-magic envelope is structurally not BIE1, but it is cryptography-class because the package doesn't know whether the wrong magic is a benign mismatch or a forgery attempt). The MAC gate runs BEFORE the CBC decrypt (encrypt-then-MAC composition: never decrypt unauthenticated bytes). PKCS#7 strip is the final validation — the cipher block mode itself does not validate padding shape.

---

## 3. Sentinel error catalogue

All sentinels are package-level `var Err... = errors.New("hush/transport/ecies: <message>")` declarations. Compare via `errors.Is`. **No wrap relationships** between sentinels — each category is independent.

| Sentinel                       | Static message                                | Triggered by                                                                          |
|--------------------------------|-----------------------------------------------|---------------------------------------------------------------------------------------|
| `ErrECIESDecryptFailed`        | `hush/transport/ecies: ECIES decrypt failed`   | Every cryptographic decrypt failure: BIE1 magic mismatch, malformed compressed pubkey, MAC mismatch, AES-block-shape failure, PKCS#7 unpad failure, SecureBytes wrap failure. **Wrong key and tampered envelope share this sentinel by design** (FR-004). |
| `ErrECIESEnvelopeTooShort`     | `hush/transport/ecies: envelope too short`     | Envelope length below `minEnvelopeSize` (85). Returned BEFORE any cryptographic primitive (FR-005).                          |
| `ErrECIESEmptyPlaintext`       | `hush/transport/ecies: empty plaintext`        | `Encrypt` rejection: `len(plaintext) == 0`. Returned BEFORE any cryptographic primitive (FR-015).                            |
| `ErrECIESInvalidRecipientKey`  | `hush/transport/ecies: invalid recipient key`  | `Encrypt` rejection: `recipientPub == nil`, `recipientPub.Curve == nil`, or curve not equal to `secp256k1.S256()` (FR-015a). |

### 3.1 Sentinel selection table by failure shape

| Failure shape                              | Returned sentinel                  | Notes                                                                          |
|--------------------------------------------|------------------------------------|--------------------------------------------------------------------------------|
| `Encrypt(ctx, ...)` with cancelled `ctx`   | `context.Canceled` (verbatim)      | Preserves `errors.Is(err, context.Canceled)`                                  |
| `Encrypt` with deadline-exceeded `ctx`     | `context.DeadlineExceeded`         | Preserves `errors.Is(err, context.DeadlineExceeded)`                          |
| `Encrypt` with `recipientPub == nil`        | `ErrECIESInvalidRecipientKey`      | Fires before plaintext copy                                                    |
| `Encrypt` with wrong-curve `recipientPub`   | `ErrECIESInvalidRecipientKey`      | Curve identity check via `recipientPub.Curve == secp256k1.S256()`              |
| `Encrypt` with `len(plaintext) == 0`        | `ErrECIESEmptyPlaintext`           | Fires before plaintext copy                                                    |
| `Encrypt` with `len(plaintext) > 0`, valid pub | `nil` (success — opaque envelope returned) | Round-trip property holds (SC-001)                              |
| `Decrypt(ctx, ...)` with cancelled `ctx`   | `context.Canceled` (verbatim)      | Same as Encrypt                                                                |
| `Decrypt` with `len(envelope) < 85`         | `ErrECIESEnvelopeTooShort`         | Fires before any cryptographic primitive                                       |
| `Decrypt` with envelope[0:4] != "BIE1"     | `ErrECIESDecryptFailed`            | Magic mismatch — could be wrong format or forgery; collapsed into single sentinel |
| `Decrypt` with malformed ephPub             | `ErrECIESDecryptFailed`            | `secp256k1.ParsePubKey` rejects wrong-length, off-curve, or non-canonical points |
| `Decrypt` with bad CT block shape           | `ErrECIESDecryptFailed`            | CT length not a positive multiple of 16                                        |
| `Decrypt` with wrong recipient priv         | `ErrECIESDecryptFailed`            | MAC verification fails (collapsed with tampered-envelope per FR-004)           |
| `Decrypt` with tampered envelope            | `ErrECIESDecryptFailed`            | MAC verification fails (collapsed with wrong-key per FR-004)                   |
| `Decrypt` with bad PKCS#7 padding           | `ErrECIESDecryptFailed`            | Last-byte > BlockSize or padding-bytes mismatch                                |
| `Decrypt` with valid envelope, matching priv| `nil` + fresh `*SecureBytes`        | Caller owns Destroy lifetime                                                   |

### 3.2 Why no sub-categories of `ErrECIESDecryptFailed`

The collapse is deliberate. FR-004 mandates that `Decrypt` MUST NOT distinguish "wrong key" from "tampered envelope" by error identity. Promoting any of the cryptographic-failure sub-categories (magic / pubkey / ct-shape / mac / pkcs7 / wrap) to a distinct sentinel would leak the failure shape via `errors.Is` matching, enabling an attacker to probe which gate fired.

The single-sentinel discipline also matches what `bitcoinschema/go-bitcoin`'s reference helper produces (a single `error` for every decrypt-side failure), maintaining the wire-compatibility intent described in research [R-002](./research.md#r-002--ecies-stdlib-substitution).

---

## 4. Per-call lifecycle

### 4.1 Encrypt (server side, SDD-13 consumer)

```text
Encrypt(ctx, recipientPub, plaintext) -> (envelope, err)
│
├── 1. ctx.Err() check                     [returns ctx.Err() verbatim if cancelled]
├── 2. validate recipientPub               [returns ErrECIESInvalidRecipientKey on nil/wrong-curve]
├── 3. validate len(plaintext) > 0         [returns ErrECIESEmptyPlaintext on empty]
├── 4. copy plaintext into pt              [defer secureZero(pt)]
├── 5. generate ephemeral keypair          [defer secureZeroBigInt(ephPriv.D)]
├── 6. ECDH → sharedX                       [defer secureZero(sharedX)]
├── 7. KDF → iv, keyE, keyM                 [defer secureZero(H[:])]
├── 8. PKCS#7 pad pt → padded               [defer secureZero(padded)]
├── 9. AES-256-CBC encrypt → ciphertext
├── 10. compress ephPub → ephPubCompressed
├── 11. HMAC-SHA256 → mac
└── 12. envelope = magic ‖ ephPub ‖ ct ‖ mac → return (envelope, nil)
```

After step 12 returns, the deferred zero calls fire (LIFO): `secureZero(padded)`, `secureZero(H[:])`, `secureZero(sharedX)`, `secureZeroBigInt(ephPriv.D)`, `secureZero(pt)`. The caller's input slice is **NOT** mutated — only the package's internal copies are zeroed.

On any error path (steps 2–11), the deferred zero calls still fire — the package zeroes its own intermediate buffers regardless of return value (FR-008, SC-008).

### 4.2 Decrypt (client side, SDD-16 consumer)

```text
Decrypt(ctx, recipientPriv, envelope) -> (sb, err)
│
├── 1. ctx.Err() check                     [returns ctx.Err() verbatim if cancelled]
├── 2. len(envelope) >= 85                  [returns ErrECIESEnvelopeTooShort if false]
├── 3. magic == "BIE1"                      [returns ErrECIESDecryptFailed if false]
├── 4. parse ephPub via secp256k1.ParsePubKey [returns ErrECIESDecryptFailed on parse failure]
├── 5. ECDH → sharedX                       [defer secureZero(sharedX)]
├── 6. KDF → iv, keyE, keyM                 [defer secureZero(H[:])]
├── 7. CT-shape gate                        [returns ErrECIESDecryptFailed if not multiple of 16]
├── 8. HMAC verify (constant-time)          [returns ErrECIESDecryptFailed on mismatch]
├── 9. AES-256-CBC decrypt → plaintextBuf   [defer secureZero(plaintextBuf)]
├── 10. PKCS#7 unpad → unpadded             [returns ErrECIESDecryptFailed on bad padding]
├── 11. securebytes.New(unpadded) → sb       [unpadded is zeroed by securebytes.New; returns ErrECIESDecryptFailed on wrap failure]
└── 12. return (sb, nil)
```

After step 12 returns, the deferred zero calls fire: `secureZero(plaintextBuf)`, `secureZero(H[:])`, `secureZero(sharedX)`. Note that `plaintextBuf` and `unpadded` share the same underlying array; `securebytes.New` zeros that storage by contract (R-006), and the `defer secureZero(plaintextBuf)` is the defense-in-depth backstop.

The returned `*SecureBytes` is the caller's property. The caller MUST eventually call `sb.Destroy()` (typically via `defer sb.Destroy()` at the call site — see [quickstart.md](./quickstart.md)).

On any error path (steps 2–11), no `*SecureBytes` is constructed; the deferred zeros still fire on the buffers that were allocated.

### 4.3 Ephemeral keypair lifecycle (encrypt only)

The ephemeral keypair lives for the duration of a single `Encrypt` call. It is generated at step 5, used in step 6 (ECDH), encoded at step 10 (compressed pubkey), and the private scalar is zeroed at step 12 via `defer secureZeroBigInt(ephPriv.D)`. The compressed public encoding is embedded in the envelope and travels to the recipient. The scalar never leaves the package's stack and never lives in any package-level state.

The recipient (Decrypt) does not generate a keypair. It uses its own long-lived `*ecdsa.PrivateKey` (the `hush request` per-session ephemeral key, derived once per session and held in client memory until the session ends).

### 4.4 No package-level state

The package holds zero mutable state between calls. Two concurrent `Encrypt` calls share nothing; two concurrent `Decrypt` calls share nothing; an `Encrypt` and a `Decrypt` running concurrently share nothing. The race-detector test `TestECIES_ConcurrentRoundTrip` asserts this property under `-race`.

---

## 5. Caller-side data model (downstream consumers)

### 5.1 SDD-13 (server `/secrets/{name}` handler)

The server's secret-fetch handler holds:

- A `*ecdsa.PublicKey` from the session JWT's `ephemeral_pubkey` claim (per FR-4 / SDD-07).
- A `*securebytes.SecureBytes` containing the freshly-decrypted secret value (per SDD-03 vault Store).

Lifecycle:

```text
ctx := r.Context()
sb := vault.Get(secretName)            // acquired earlier in the handler
defer sb.Destroy()                     // SDD-13 owns the SecureBytes lifetime

var envelope []byte
err := sb.Use(func(plaintext []byte) {
    envelope, err = ecies.Encrypt(ctx, ephemeralPub, plaintext)
})
if err != nil {
    // log via SDD-05 logger; respond 500 with a generic error (no secret leaked)
    return
}
w.Header().Set("Content-Type", "application/octet-stream")
w.Write(envelope)
```

The `sb.Use` callback exposes the plaintext only to `Encrypt`'s call frame; once `Use` returns, the plaintext slice is no longer referenced from the handler.

### 5.2 SDD-16 (`hush request` client)

The client holds:

- A `*ecdsa.PrivateKey` — the per-session ephemeral private key (generated at session start, destroyed at session end).
- The HTTP response body (bytes received over Tailscale).

Lifecycle:

```text
ctx := cmd.Context()
envelope := readResponseBody(resp.Body)
sb, err := ecies.Decrypt(ctx, ephemeralPriv, envelope)
if err != nil {
    // map to user-visible error; envelope and key bytes are NOT logged
    return err
}
defer sb.Destroy()

err = sb.Use(func(plaintext []byte) {
    // inject plaintext as env var for the child process (--exec mode)
    childEnv = append(childEnv, fmt.Sprintf("%s=%s", secretName, string(plaintext)))
})
```

Note: the `fmt.Sprintf` call materialises a `string` from the plaintext bytes — this is a deliberate but tightly-scoped string conversion (the env-var form is mandated by the OS exec contract); the resulting `string` lives in the env-var slice for the duration of `os.StartProcess` only, and the surrounding `hush request` code is responsible for not printing or logging it (Constitution X).

### 5.3 No further consumers in v0.1.0

The package's two consumers (SDD-13, SDD-16) are the only ones in v0.1.0. SDD-19 (`hush supervise`) consumes secrets via the same SDD-16 pipeline (the supervisor IS a `hush request`-style consumer for daemon use cases).

---

## 6. Test data

### 6.1 Fixture keys

| Test                              | Key generation                                             | Fresh per-run? |
|-----------------------------------|------------------------------------------------------------|----------------|
| `TestECIES_RoundTrip_*`           | `ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)`          | **Yes**        |
| `TestECIES_DecryptWrongKey_*`     | Two separate fresh keys; encrypt to one, decrypt with other | Yes            |
| `TestECIES_DecryptMangledEnvelope_*` | Fresh key; mangle envelope after encrypt                  | Yes            |
| `TestECIES_DecryptEmptyEnvelope_*` | (Not used — envelope is empty bytes)                       | N/A            |
| `TestECIES_DecryptReturnsSecureBytes` | Fresh key                                              | Yes            |
| `TestECIES_NoLeakOnError`         | Fresh key                                                  | Yes            |
| `TestECIES_ConcurrentRoundTrip`   | One fresh key per goroutine                                 | Yes            |
| `FuzzECIESDecrypt`                | Deterministic key (R-011)                                   | **No**         |

### 6.2 Sentinel marker

`testutil.SentinelSecret(9)` returns `"SECRET_SHOULD_NEVER_APPEAR_9"` (28 ASCII chars). Used in `TestECIES_NoLeakOnError` as the plaintext-substring marker; tested for absence in `err.Error()` and the wrap chain via `testutil.AssertSentinelAbsent`.

### 6.3 Fuzz seed corpus

```text
testdata/fuzz/FuzzECIESDecrypt/
├── empty                       # 0 bytes
├── one-byte                    # 1 byte:  \x00
├── exactly-min-size-garbage    # 85 bytes: deterministically-generated random
└── two-x-min-size-garbage      # 170 bytes: deterministically-generated random
```

The "deterministically-generated random" content for the two larger seeds is a pre-committed byte sequence (e.g., the SHA-256 hash of the seed-name string, repeated to fill the target length). The exact content does not matter — any random-looking bytes exercise the same Decrypt code paths. The seeds exist to bypass the length-or-magic early-out and exercise the deeper validation logic.

---

## 7. Invariants (testable at any commit)

| ID    | Invariant                                                                                                   | Asserted by                          |
|-------|-------------------------------------------------------------------------------------------------------------|--------------------------------------|
| I-001 | `Encrypt(ctx, pub, pt)` followed by `Decrypt(ctx, priv, envelope)` (matching keypair) recovers `pt` byte-exactly | `TestECIES_RoundTrip_*` (3 sizes)    |
| I-002 | Two `Encrypt` calls on the same `(pub, pt)` produce **different** envelopes (fresh ephemeral keypair per call) | `TestECIES_EncryptIsRandomised`      |
| I-003 | `len(envelope) >= 85` for any successful `Encrypt` return                                                    | `TestECIES_EnvelopeMeetsMinSize`     |
| I-004 | `Decrypt(ctx, priv, envelope)` with mismatched `priv` returns `ErrECIESDecryptFailed`                       | `TestECIES_DecryptWrongKey_Fails`    |
| I-005 | `Decrypt(ctx, priv, mangle(envelope))` returns `ErrECIESDecryptFailed` for any single-byte flip               | `TestECIES_DecryptMangledEnvelope_Fails` |
| I-006 | `Decrypt(ctx, priv, envelope[:n])` for `n < 85` returns `ErrECIESEnvelopeTooShort`                           | `TestECIES_DecryptEmptyEnvelope_TooShort` |
| I-007 | The plaintext byte sequence does not appear as a substring of any successful `Encrypt`'s envelope            | `TestECIES_NoPlaintextSubstringInEnvelope` |
| I-008 | `Decrypt`'s returned `*SecureBytes` exposes contents byte-equal to the original; `Destroy` then `Use` returns `ErrDestroyed` | `TestECIES_DecryptReturnsSecureBytes` |
| I-009 | `Encrypt` zeros every internal plaintext-bearing buffer before return (happy AND error paths)                | `TestECIES_EncryptZeroesInternalBuffersOnError` (capture via test-only seam) |
| I-010 | `err.Error()` for any returned error never contains any byte from envelope/plaintext/key                    | `TestECIES_NoLeakOnError` (sentinel) |
| I-011 | `Decrypt` is panic-free under any byte input                                                                 | `FuzzECIESDecrypt` (60 s gate)        |
| I-012 | Pre-cancelled context returns `ctx.Err()` such that `errors.Is(err, context.Canceled)` is true              | `TestECIES_RespectsCancelledContext`  |
| I-013 | `Encrypt` rejects empty plaintext with `ErrECIESEmptyPlaintext` BEFORE any cryptographic primitive          | `TestECIES_EncryptRejectsEmpty`       |
| I-014 | `Encrypt` rejects nil/wrong-curve pub with `ErrECIESInvalidRecipientKey` BEFORE any plaintext copy          | `TestECIES_EncryptRejectsInvalidPub`  |
| I-015 | Concurrent Encrypt/Decrypt under `-race` has zero data races                                                  | `TestECIES_ConcurrentRoundTrip` (race-clean) |
| I-016 | Coverage of `internal/transport/ecies` is 100%                                                                | `magex test:race` (codecov.yml gate) |

---

## 8. What this data model does NOT specify

- **The exact internal arithmetic of `Curve.ScalarMult`** — that is the elliptic.Curve interface's contract, supplied by `decred/dcrd/dcrec/secp256k1/v4`. The package uses the result, does not re-implement scalar multiplication.
- **The exact byte order of `secp256k1.ParsePubKey`'s parser** — the package uses the parser's documented behaviour (length 33, prefix `0x02|0x03`, on-curve, non-infinity).
- **The macOS/Linux/Windows-specific behaviour of `crypto/rand.Reader`** — the package uses the stdlib's documented entropy source.
- **Whether `*SecureBytes` allocates inside `mlock` or `mmap+mlock` or via some platform-specific seam** — that is the SDD-02 contract; SDD-09 consumes the constructor only.
- **Storage of the per-session ephemeral private key on the client side** — that is SDD-16's responsibility.
- **Storage of the registered client public keys on the server side** — that is SDD-12 / SDD-13's responsibility.
- **The HTTP response header that wraps the envelope on the wire** — that is SDD-13's responsibility (typically `Content-Type: application/octet-stream`).

The data model is intentionally bounded to the bytes-on-the-wire and types-in-memory of THIS package; everything else is the consumer's responsibility.
