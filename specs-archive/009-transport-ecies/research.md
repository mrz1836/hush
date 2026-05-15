# Phase 0 Research: `internal/transport/ecies`

**Feature**: 009-transport-ecies — wire-level ECIES encryption of secret responses
**Date**: 2026-04-28

This document resolves every technical decision the plan depends on. Each entry follows the **Decision / Rationale / Alternatives considered** format. There are no remaining `NEEDS CLARIFICATION` markers in the spec; the three clarification answers from Session 2026-04-28 (`spec.md` §Clarifications) are encoded into the relevant decisions below.

---

## R-001 — ECIES variant: BIE1 (BSV/Electrum-style) over secp256k1

**Decision**: Use the **BIE1** ECIES variant — a published BSV/Electrum-style ECIES with the following on-the-wire shape:

```text
envelope := magic(4) ‖ ephemeralPubKey(33) ‖ ciphertext(N) ‖ mac(32)
```

- `magic` is the four ASCII bytes `B`, `I`, `E`, `1` (`0x42 0x49 0x45 0x31`).
- `ephemeralPubKey` is the ephemeral secp256k1 public key, **compressed** (33 bytes: `0x02|0x03 ‖ X.FillBytes(32)`).
- `ciphertext` is AES-256-CBC over the PKCS#7-padded plaintext. Length is a positive multiple of `aes.BlockSize` (16); minimum is 16 bytes (a 1-byte plaintext PKCS#7-pads to 16, so the smallest ciphertext is one block).
- `mac` is HMAC-SHA256 computed over `magic ‖ ephemeralPubKey ‖ ciphertext`, truncated to **none** (full 32 bytes — the SHA-256 output length).

KDF: `H = SHA-512(sharedX)` where `sharedX` is the 32-byte X coordinate of `ephemeralPriv * recipientPub` (an ECDH operation on secp256k1). The 64-byte `H` is split into:

| Slice    | Bytes | Use                                                          |
|----------|-------|--------------------------------------------------------------|
| `H[0:16]`  | 16    | AES-256-CBC initialisation vector                            |
| `H[16:48]` | 32    | AES-256 symmetric key (`keyE`)                               |
| `H[48:64]` | 16    | HMAC-SHA256 key (`keyM`); HMAC-SHA256 accepts any key length per RFC 2104 |

The minimum envelope size is `4 + 33 + 16 + 32 = 85` bytes. `Decrypt` rejects envelopes shorter than this with `ErrECIESEnvelopeTooShort` BEFORE any cryptographic primitive is invoked (FR-005, SC-004).

**Rationale**: BIE1 is the de-facto BSV/Bitcoin-Cash ECIES variant; `github.com/bitcoinschema/go-bitcoin`, `github.com/bsv-blockchain/go-sdk`, `electrum`, and `electron-cash` all implement the same wire format. Using a **published** variant means:

1. The envelope shape, KDF, and MAC computation are externally documented and externally test-vectored.
2. A future implementer who reads this code can cross-check against any of those reference implementations.
3. A future swap to a third-party library (if the project ever revisits R-002) is wire-compatible without a flag day.

The hush protocol does NOT require interop with any external Bitcoin tooling — SDD-09 controls both the server (SDD-13) and the client (SDD-16). The wire-compatibility property is purely defensive: it makes the implementation cross-checkable and avoids ad-hoc cryptographic design (the kind of thing Constitution Principle III is meant to prevent).

The choice of 32-byte HMAC-SHA256 (full output, no truncation) matches the BIE1 reference. Truncating to 16 or 20 bytes would shave envelope size but reduce the forgery-resistance margin; the spec's FR-001 imposes no envelope-size cap, so we keep the full-strength tag.

**Alternatives considered**:
- *ECIES-KEM/DEM with HKDF-SHA256 instead of SHA-512 KDF*: rejected. HKDF is the more modern KDF (extract-then-expand), but BIE1 is the published variant the BSV ecosystem uses; staying interop-aligned has cross-check value. HKDF would also break wire-compatibility with the named `go-bitcoin` library.
- *AES-256-GCM instead of AES-256-CBC + HMAC*: rejected. AES-GCM is the modern AEAD construction and avoids the encrypt-then-MAC composition risk; however, BIE1 specifies CBC + HMAC explicitly, and the encrypt-then-MAC composition (mac over the ciphertext, including all envelope-prefix bytes) is well-understood and matches the published variant. The CBC + HMAC choice is a deliberate alignment with `docs/SECURITY.md` §5 ("ECIES (secp256k1 via go-bitcoin)") which names the BSV variant.
- *Custom envelope shape (e.g., bare ephemeralPubKey ‖ ciphertext ‖ mac without the magic prefix)*: rejected. The 4-byte BIE1 magic costs four bytes of envelope size for the property that a wrong-format envelope is rejected at the magic gate (one byte comparison, no cryptographic work). A custom shape would lose the cross-checkability described above for no benefit.
- *X25519 + ChaCha20-Poly1305*: rejected. Constitution III pins secp256k1 (`docs/SECURITY.md` §5 "Asymmetric encryption | ECIES (secp256k1 via go-bitcoin)"). Switching curves would require a constitutional amendment.

---

## R-002 — ECIES stdlib substitution

**Decision**: Implement the BIE1 ECIES envelope using **stdlib** primitives plus the existing `github.com/decred/dcrd/dcrec/secp256k1/v4` direct dependency. Specifically:

| Primitive | Source |
|-----------|--------|
| Curve (secp256k1) | `decred/dcrd/dcrec/secp256k1/v4` (already in `go.mod` from SDD-01) — exposed via `*ecdsa.PrivateKey.Curve` and `*ecdsa.PublicKey.Curve` |
| ECDH (scalar multiplication) | `Curve.ScalarMult(pub.X, pub.Y, priv.D.Bytes())` (stdlib `elliptic.Curve` interface, decred-provided implementation) |
| Compressed pubkey codec | `secp256k1.ParsePubKey([]byte)` (decode) and `(*secp256k1.PublicKey).SerializeCompressed()` (encode); the conversion `*ecdsa.PublicKey ↔ *secp256k1.PublicKey` is a coordinate lift via `secp256k1.NewPublicKey(x, y)` |
| Ephemeral keypair generation | `ecdsa.GenerateKey(secp256k1.S256(), crypto/rand.Reader)` |
| KDF | `crypto/sha512.Sum512(sharedX)` |
| Symmetric cipher | `crypto/aes.NewCipher(keyE)` + `crypto/cipher.NewCBCEncrypter` / `NewCBCDecrypter` |
| Integrity tag | `crypto/hmac.New(crypto/sha256.New, keyM)` + `hmac.Equal` (constant-time compare) |
| Constant-time helpers | `crypto/subtle.ConstantTimeCompare` (used as a fallback if `hmac.Equal` is not in scope) |

**Zero new direct dependencies** are added to `go.mod`. The chunk contract (`docs/sdd/SDD-09.md` §"Behaviour contracts") names `github.com/bitcoinschema/go-bitcoin`'s ECIES helpers as the implementation; the plan honors the *intent* (BIE1 ECIES over secp256k1) using stdlib + the existing decred direct dep.

**Rationale**: The chunk contract's two clauses are in tension:

1. "Use github.com/bitcoinschema/go-bitcoin's ECIES helpers (already locked by SDD-01)."
2. "NO new crypto deps (Constitution XI)."

The first clause's "already locked by SDD-01" is factually incorrect: SDD-01 (`internal/keys`) substituted its own named library reference (`github.com/bitcoinschema/go-bitcoin/v2`) for `decred/dcrd/dcrec/secp256k1/v4 + decred/dcrd/hdkeychain/v3`, which ARE the libraries currently in `go.mod`. So `go-bitcoin` is NOT physically in `go.mod`; SDD-09 would have to ADD it as a new direct dependency. That collides with clause 2.

Reading the chunk contract's library reference at the *intent* level resolves the tension. The **algorithm** is BIE1 ECIES; the **curve** is secp256k1; the **digest** is SHA-256 (for the MAC) and SHA-512 (for the KDF); the **cipher** is AES-256-CBC. All of these are available via stdlib + the existing decred direct dep. Implementing them produces a wire-compatible BIE1 envelope (R-001) with **zero** new direct deps.

This is a **stdlib-correct refinement** in the same spirit as:
- SDD-01's R-002 (replacing `bitcoinschema/go-bitcoin` for BIP32 with `decred/dcrd/hdkeychain/v3`).
- SDD-08's R-002 (replacing `bitcoinschema/go-bitcoin` for ECDSA Sign/Verify with stdlib `crypto/ecdsa`).
- SDD-06's R-003 (replacing deprecated `filepath.HasPrefix` with `filepath.Rel`-based containment).

Each of those substitutions preserved the spec's behavioural contract while eliminating a third-party crypto dependency. SDD-09's substitution is the same pattern at the package level: BIE1 ECIES is implemented from stdlib primitives that the project already trusts.

Security equivalence:
- **Algorithm**: BIE1 is a deterministic envelope structure (R-001). Both `bitcoinschema/go-bitcoin`'s implementation and the stdlib-based implementation produce byte-equal envelopes for the same inputs (modulo the random ephemeral keypair, which is by design). The decryption path is byte-equal too: same magic gate, same ECDH, same KDF, same MAC verification, same CBC decryption, same PKCS#7 strip.
- **Entropy**: stdlib's `crypto/rand.Reader` is the OS CSPRNG (`/dev/urandom` on Linux, `getentropy` on macOS, `BCryptGenRandom` on Windows). Constitution III mandates `crypto/rand` for entropy; ad-hoc entropy is forbidden.
- **Recipient distinguishability**: per FR-004, "wrong key" and "tampered envelope" MUST share the same sentinel. The stdlib + decred implementation collapses both into `ErrECIESDecryptFailed` because the only failure path that distinguishes them — a `ParsePubKey` decode failure on the ephemeral pubkey — also returns `ErrECIESDecryptFailed`. There is no error-identity leakage between the two failure modes.
- **Panic-free under hostile input**: stdlib `aes.NewCipher`, `cipher.NewCBCDecrypter`, `hmac.New`, `sha512.Sum512`, and `secp256k1.ParsePubKey` are all stdlib / well-fuzzed third-party code that returns errors rather than panics on malformed input. The plan's `FuzzECIESDecrypt` re-asserts this invariant for the composite envelope under the project's CI gate.
- **Constant-time MAC compare**: `hmac.Equal` is the stdlib's documented constant-time compare for HMAC outputs; using it (rather than `bytes.Equal`) closes the timing-side-channel from the very first byte of the comparison.

The `ctx.Err()` guard at entry honours Constitution IX (context as first parameter for cancellable work). Cancellation arriving mid-CBC-decrypt is not honored — the operation is short and CPU-bound; aborting mid-stream has no security benefit and complicates the implementation.

**Alternatives considered**:

- *Add `github.com/bitcoinschema/go-bitcoin/v2` as a new direct dependency*: rejected. Even though `go-bitcoin` is named in `docs/SECURITY.md` §5 and Constitution XI's pinned-stack ECIES slot, SDD-01's substitution precedent shows the named library is honored at the *intent* level, not the *literal* level. Adding it now would partially un-do that substitution and pull in transitive deps (BSV transaction primitives, address handling, OP_RETURN helpers — none of which SDD-09 needs). The library's ECIES API also operates on hex-string inputs and `bec.PrivateKey`/`bec.PublicKey` types from `bsv-blockchain/go-bk/bec`; converting to/from the `*ecdsa.PrivateKey` types our keys package returns would add an impedance layer for no security gain.
- *Use `github.com/bsv-blockchain/go-sdk` per the trusted-sources hierarchy*: deferred. The trusted-sources hierarchy (Constitution XI: "stdlib first, then sigil baseline, then bsv-blockchain GitHub organization, and only then the wider ecosystem") would prefer `bsv-blockchain/go-sdk` over `bitcoinschema/go-bitcoin` if both provided the same primitive. However, **stdlib comes first**, and stdlib + the existing decred dep can implement BIE1 directly. Adding `bsv-blockchain/go-sdk` would introduce a new direct dep we don't need.
- *Use `github.com/mrz1836/sigil` per the sigil-baseline tier*: deferred. `sigil` is the project's preferred general-purpose toolkit baseline; whether it provides a BIE1 ECIES helper is unclear at this writing. If a future hardening pass surfaces a sigil ECIES helper that is byte-compatible with BIE1, swapping is a one-file change (the public API of SDD-09 is library-independent). For now, stdlib + decred is sufficient and adds no new dep.
- *Implement ECIES from scratch with HKDF-SHA256 + AES-256-GCM (a "modernised" variant)*: rejected. Diverging from BIE1 loses the cross-checkability against published BSV ECIES implementations and would be a custom protocol not specified anywhere. The spec's Assumptions section explicitly says "ECIES over the elliptic curve already locked by the project's crypto stack" — the BIE1 variant the BSV stack uses is the locked choice.
- *Use `crypto/ecdh` (Go 1.20+) for the ECDH step*: rejected. Stdlib `crypto/ecdh` only supports P-256, P-384, P-521, and X25519 — secp256k1 is NOT on that list. The decred-provided `elliptic.Curve.ScalarMult` is the right primitive for secp256k1 ECDH.
- *Use generic-curve `elliptic.Marshal`/`Unmarshal` for the ephemeral pubkey codec*: rejected. Stdlib's compressed-form pubkey codec (`elliptic.MarshalCompressed`) accepts any `elliptic.Curve` but does NOT validate that the parsed point lies on the curve — a forged 33-byte input could produce an off-curve point that subsequent `ScalarMult` would mishandle. The decred-provided `secp256k1.ParsePubKey` validates curve membership and rejects malformed compressed points; that validation is load-bearing for FR-012's panic-free guarantee.

---

## R-003 — KDF derivation: SHA-512 of shared X coordinate

**Decision**: The KDF is `H = SHA-512(sharedX)` where `sharedX` is the **32-byte big-endian** representation of the X coordinate of `(ephemeralPriv * recipientPub)` on secp256k1. The shared X bigint is converted to fixed-width 32 bytes via `sharedX.FillBytes(make([]byte, 32))` (NOT `sharedX.Bytes()`, which strips leading zero bytes and produces variable-width output). The 64-byte `H` is then split as documented in R-001.

A `defer secureZero(sharedXBytes)` is registered immediately after the conversion so the shared-secret bytes are zeroed before `Encrypt`/`Decrypt` returns on every code path.

**Rationale**: Fixed-width 32-byte serialisation of the shared X is the BIE1 reference behaviour (electrum, `bitcoinschema/go-bitcoin`, `bsv-blockchain/go-sdk` all use 32 bytes regardless of leading-zero situation). Using `Bytes()` instead would produce different envelopes for X coordinates that happen to start with one or more zero bytes — wire-compatibility-breaking and a subtle source of "decryption fails for ~1 in 256 keypairs" defects.

`SHA-512` (rather than `SHA-256` doubled or HKDF-SHA256) is the BIE1 reference choice. The 64-byte output is split into the IV (16) + AES key (32) + HMAC key (16); the AES key uses bytes 16..47 (the most-entropy-dense slice) and the HMAC key uses the trailing 16 bytes (HMAC-SHA256 zero-pads any key shorter than 64 bytes per RFC 2104, so a 16-byte HMAC key is effectively a 16-byte block-zero-padded HMAC key — which is what the BIE1 reference uses).

`defer secureZero` on the shared-secret bytes prevents the bytes from lingering in the heap between the KDF computation and the function's return. The `H[:]` array (a stack-allocated `[64]byte` from `sha512.Sum512`) is also zeroed via `defer secureZero(H[:])` for the same reason.

**Alternatives considered**:
- *HKDF-SHA256 with a salt+info parameter*: rejected. Modern KDF, but BIE1's reference is SHA-512 with no salt and no info. Switching would break wire-compatibility with published BSV ECIES implementations.
- *PBKDF2 with iteration count*: rejected. PBKDF2 is for password-derived keys; the input here is a 32-byte ECDH shared secret with full secp256k1 entropy — no iteration is needed.
- *Use only the 32-byte AES key from `H[16:48]` and derive the HMAC key from a separate hash chain*: rejected. The BIE1 reference uses `H[48:64]` directly as the HMAC key; any deviation breaks wire-compatibility.
- *Skip the KDF and use the raw shared X as both AES key and HMAC key*: rejected. Re-using the raw ECDH output as both the cipher key and the MAC key is a documented anti-pattern (it eliminates the separation between encryption and integrity). The KDF's primary job is to produce statistically-independent key streams for AES and HMAC.

---

## R-004 — Compressed-pubkey codec: decred secp256k1.ParsePubKey

**Decision**: Compressed-pubkey codec uses `decred/dcrd/dcrec/secp256k1/v4`:

- **Encode**: `pub *ecdsa.PublicKey → secp256k1.NewPublicKey(pub.X, pub.Y).SerializeCompressed()` — produces 33 bytes (`0x02|0x03 ‖ X.FillBytes(32)`).
- **Decode**: `secp256k1.ParsePubKey(envelope[4:37]) → (*secp256k1.PublicKey, error)`. The library validates: (a) length is 33 bytes, (b) prefix byte is `0x02` or `0x03`, (c) the parsed point lies on secp256k1, (d) the parsed point is not the point at infinity. Any failure returns a non-nil error; the package maps that error to `ErrECIESDecryptFailed`.

The decoded `*secp256k1.PublicKey` exposes `X()` and `Y()` methods returning `*secp256k1.FieldVal`; converting to the `*big.Int`-using `*ecdsa.PublicKey` shape requires reading those field values into `*big.Int` (the library exposes `pub.X(*big.Int)`-style helpers, or equivalent). The actual ECDH step works on whichever shape is available — `Curve.ScalarMult` accepts `*big.Int` X, Y; `secp256k1.PublicKey` itself has its own scalar-mul methods. The implementation will use whichever shape is cleanest in code review; the contract is that the resulting shared X is byte-identical regardless of shape.

**Rationale**: The decred secp256k1 library is the project's existing crypto dependency (SDD-01); using it for the compressed-pubkey codec is **zero** new dep. The library validates curve membership and rejects malformed points, which is load-bearing for FR-012 (panic-free under hostile input).

Stdlib's `elliptic.MarshalCompressed` / `UnmarshalCompressed` are alternatives but have a load-bearing weakness: `UnmarshalCompressed` does NOT validate that the parsed X is in the field, only that the byte prefix is correct. A malicious compressed pubkey with an oversized X (≥ field prime) could produce a `*big.Int` that subsequent `ScalarMult` would mishandle. The decred library's `ParsePubKey` performs the field-membership check; that property is required for hostile-input safety.

**Alternatives considered**:
- *Stdlib `elliptic.MarshalCompressed` / `UnmarshalCompressed`*: rejected per the field-membership weakness above.
- *Hand-rolled compressed-pubkey codec in the package*: rejected. The point compression / decompression math (computing `Y` from `X` and the parity bit, with sqrt-mod-p) is intricate; reusing the decred library's tested implementation is far safer than re-implementing.
- *Use `crypto/ecdh` (Go 1.20+) for the codec*: rejected. `crypto/ecdh` does not support secp256k1.

---

## R-005 — Memory zeroization discipline

**Decision**: All intermediate buffers holding plaintext, padded plaintext, the shared X bytes, the SHA-512 KDF output, and the AES intermediate buffer are zeroed via `defer secureZero(buf)` on every return path of `Encrypt` and `Decrypt`. The ephemeral private scalar (`ephPriv.D`) is zeroed via `defer secureZeroBigInt(ephPriv.D)`.

`secureZero(buf []byte)` is a file-private helper:

```go
func secureZero(buf []byte) {
    for i := range buf {
        buf[i] = 0
    }
}
```

The simple loop pattern is sufficient because Go's compiler does not optimize away zeroing of slices that are subsequently observable — the surrounding `defer` registration plus the buffer being read by a downstream `securebytes.New` (in Decrypt) or being a function-local that the compiler cannot prove dead (in Encrypt) keeps the zero loop live. (See `internal/vault/securebytes/securebytes_*.go` for the same pattern in SDD-02.)

`secureZeroBigInt(n *big.Int)` is a file-private helper:

```go
func secureZeroBigInt(n *big.Int) {
    if n == nil {
        return
    }
    // Zero the underlying word slice via SetInt64(0); also overwrite the bytes.
    bz := n.Bytes()
    secureZero(bz)
    n.SetInt64(0)
}
```

The `Bytes()` call returns a fresh slice (a copy of the big.Int's internal Word storage interpreted as bytes); zeroing the copy does NOT zero the internal Words, so the `n.SetInt64(0)` call is the load-bearing zero. The `secureZero(bz)` is defensive (it zeroes the copy that `Bytes()` returned, in case the caller's downstream code holds a reference to it via reflection). Per `crypto/ecdsa` source review, `SetInt64(0)` overwrites the internal Word slice with zeros and resets the abs/sign fields.

**Rationale**: FR-008 mandates zeroing on every return path INCLUDING error paths. `defer` is the only structural guarantee that achieves this in Go — registering the zero call at allocation time means Go's runtime executes it on EVERY return, regardless of which `return` statement fires.

The `ephPriv.D` zeroing is the most subtle case. Go's `*ecdsa.PrivateKey.D` is a `*big.Int` whose Word slice holds the scalar bytes; the bytes survive GC (they are heap-allocated via `make([]big.Word, ...)` inside `big.Int`). Without explicit zeroing, the ephemeral private scalar would persist on the heap until GC compaction overwrites it — a window during which a memory-forensics attacker could recover the ECDH-decrypt-able key. The `secureZeroBigInt` helper closes this window deterministically.

**Alternatives considered**:
- *Use `runtime.KeepAlive` instead of `defer secureZero`*: rejected. `runtime.KeepAlive` prevents GC reclamation of a value but does NOT zero its bytes. The threat model (process memory inspection) demands actual zeroing, not just GC-survival.
- *Use `unsafe` to zero via direct memory writes*: rejected. The `unsafe` package is a sharp tool with portability risks; the simple loop pattern is sufficient and safe.
- *Skip zeroing on Encrypt's happy path and rely on GC*: rejected. FR-008 explicitly requires zeroing on every return path. GC is not a security primitive.
- *Zero the caller's input slice on Encrypt*: rejected. The caller's input is the caller's responsibility (spec User Story 5 acceptance scenario 1 — "the package's contract is to zero only its own internal copy"). Mutating the caller's slice would surprise consumers and violate the spec.

---

## R-006 — Decrypt's SecureBytes wrap discipline

**Decision**: After AES-256-CBC decrypt and PKCS#7 unpad produce a validated plaintext slice, `Decrypt` invokes `securebytes.New(unpadded)` to obtain a `*SecureBytes`. The constructor:

1. Allocates a fresh mlocked buffer of `len(unpadded)` bytes inside `SecureBytes`.
2. Copies `unpadded` into the mlocked buffer.
3. **Zeroes `unpadded` in place** (per the SDD-02 contract).
4. Returns the `*SecureBytes`.

After the wrap, `Decrypt` does NOT retain any reference to the unpadded slice or to its underlying bytes. The mlocked buffer inside `*SecureBytes` is the sole copy of the recovered plaintext. The caller of `Decrypt` is the sole owner of the SecureBytes lifetime and is responsible for calling `Destroy()` (FR-003).

The intermediate CBC decryption buffer (`plaintextBuf := make([]byte, len(ciphertext))`) — which holds the PKCS#7-padded plaintext before unpad — is also zeroed via `defer secureZero(plaintextBuf)`. This buffer is distinct from the `unpadded` slice (which is `plaintextBuf[:len(plaintextBuf)-padLen]` — the same underlying array, sliced to drop the padding bytes); zeroing `plaintextBuf` therefore zeroes the underlying storage that both `plaintextBuf` and `unpadded` share. The zero happens AFTER `securebytes.New` returns, so the SecureBytes' internal mlocked buffer is unaffected.

**Rationale**: The SDD-02 SecureBytes constructor has a contract: it copies and zeroes its source. SDD-09 relies on that contract — if `securebytes.New` did NOT zero the source, the unpadded slice would carry a parallel handle to the plaintext after wrap, violating User Story 4 acceptance scenario 3.

The double-zero (deferring `secureZero(plaintextBuf)` AND relying on `securebytes.New` to zero `unpadded`) is intentional defense-in-depth: if a future SecureBytes refactor silently weakens the constructor's zero-on-wrap behaviour, the `defer` still zeros the underlying bytes. Cost: one negligible memset per Decrypt return.

The error path is the subtle case. If `securebytes.New` returns an error, `Decrypt` does NOT have a `*SecureBytes` to return; the unpadded slice is still live; the `defer secureZero(plaintextBuf)` zeros the underlying bytes; the function returns `(nil, ErrECIESDecryptFailed)`. The plaintext bytes are zeroed regardless.

**Alternatives considered**:
- *Skip `defer secureZero(plaintextBuf)` and rely solely on `securebytes.New` to zero the source*: rejected on defense-in-depth grounds described above.
- *Allocate the AES decryption output directly into the SecureBytes' mlocked buffer*: rejected. The SecureBytes API does not expose a "fill me from a callback" constructor; the import-from-slice constructor is the only entrypoint. Adding a "fill from callback" constructor to SDD-02 is a future hardening pass; for now, the wrap-then-zero pattern is the right composition.
- *Use SecureBytes for the intermediate buffer too*: rejected. Wrapping `plaintextBuf` in a SecureBytes adds a Destroy-on-defer dance for a buffer that's about to be replaced. The simple `make + defer secureZero` pattern is cleaner.

---

## R-007 — Sentinel error catalogue and static error messages

**Decision**: The package exports four sentinels (all `var Err... = errors.New("hush/transport/ecies: <message>")`); inline `//nolint:gochecknoglobals` comments cite the constitutional sentinel-class precedent.

| Sentinel                       | Static message                              | Triggered by                                                                          |
|--------------------------------|---------------------------------------------|---------------------------------------------------------------------------------------|
| `ErrECIESDecryptFailed`        | `hush/transport/ecies: ECIES decrypt failed` | Every cryptographic decrypt failure: BIE1 magic mismatch, malformed compressed pubkey, MAC mismatch, AES-block-shape failure (ciphertext length not a positive multiple of 16), PKCS#7 unpad failure, SecureBytes wrap failure. Per FR-004, **all** cryptographic failures collapse into this single sentinel; the recipient cannot distinguish "wrong key" from "tampered envelope" by error type. |
| `ErrECIESEnvelopeTooShort`     | `hush/transport/ecies: envelope too short`   | Envelope length `< minEnvelopeSize (= 85)`. Returned by `Decrypt` BEFORE any cryptographic primitive is invoked (FR-005). |
| `ErrECIESEmptyPlaintext`       | `hush/transport/ecies: empty plaintext`      | `Encrypt` is called with `len(plaintext) == 0`. Returned BEFORE any cryptographic primitive is invoked (FR-015). |
| `ErrECIESInvalidRecipientKey`  | `hush/transport/ecies: invalid recipient key`| `Encrypt` is called with `recipientPub == nil`, with `recipientPub.Curve == nil`, or with a curve not equal to the package's expected secp256k1 (`secp256k1.S256()` identity comparison). Returned BEFORE the package allocates or copies any plaintext bytes (FR-015a). |

All four messages are **static strings** known at compile time. The package NEVER calls `fmt.Errorf` or any other formatter on these errors — the returned error IS the sentinel value itself, by direct return. This guarantees, by construction, that no envelope byte, no plaintext byte, no key byte, and no length value derived from any of them can appear in the error message (FR-007). Callers compare via `errors.Is(err, ErrECIES…)` — the sentinel identity is the failure category.

The `ctx.Err()` return path is the single exception: when `ctx.Err() != nil`, the package returns `ctx.Err()` verbatim (the stdlib value `context.Canceled` or `context.DeadlineExceeded`, NOT one of the package's sentinels). This preserves `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)` per FR-013 + the Session 2026-04-28 clarification answer 2.

**Rationale**: Spec FR-006 mandates four distinct, comparable sentinel error values. Spec FR-007 mandates static error messages that contain no byte from envelope/plaintext/key. The catalogue above satisfies both.

The "wrong key" / "tampered envelope" collapse into a single `ErrECIESDecryptFailed` is FR-004's explicit non-distinguishability requirement: distinguishing them by error identity would leak the failure shape and enable signature-shape probing. The collapse is enforced at the implementation level — every cryptographic failure path returns the same sentinel by direct return.

There are **no wrap relationships** between sentinels (unlike SDD-06 where loopback / unspecified / public listen-addr sentinels wrapped a common umbrella). The four SDD-09 sentinels are independent failure categories with no natural umbrella; collapsing any pair would lose specificity.

**Alternatives considered**:
- *One umbrella `ErrECIESError` for everything*: rejected. Spec FR-006 demands four distinct, comparable sentinel error values.
- *Wrap all four under a top-level `ErrECIES`*: rejected. Cosmetic-only; each consumer cares about the specific category (a server audit-log handler classifies envelope-too-short as "structural malformedness" and decrypt-failed as "cryptographic failure" — they need them distinct).
- *Use `fmt.Errorf("%w: ...", ErrECIESDecryptFailed)` to wrap sub-categories of the cryptographic-failure sentinel*: rejected. Wrapping with `%w` would leak the sub-category in the error message string — even if the message itself is a static category-string, the wrap structure exposes the failure shape via `errors.Unwrap` chain. Static-message + identity-only is the safest discipline.
- *Promote the cryptographic-failure sentinel to an error type with sub-fields (e.g., `type DecryptError struct { Phase string }`)*: rejected. Sub-fields with phase identifiers (`"magic"`, `"mac"`, `"unpad"`) would leak the failure shape through the type's fields, even if the `Error()` method returns the static message. The sentinel pattern keeps the discipline simple.

---

## R-008 — Fuzz target shape: FuzzECIESDecrypt

**Decision**: Add `decrypt_fuzz_test.go` with `FuzzECIESDecrypt(f *testing.F)`:

```go
func FuzzECIESDecrypt(f *testing.F) {
    priv := generateFuzzKey(f) // deterministic test key, regenerated on each fuzz target setup
    seedFuzzCorpus(f)          // f.Add(...) for each file in testdata/fuzz/FuzzECIESDecrypt/
    f.Fuzz(func(t *testing.T, envelope []byte) {
        sb, err := ecies.Decrypt(t.Context(), priv, envelope)
        if err == nil {
            // Random bytes that happen to verify against this key — vanishingly unlikely; treat as a pass.
            // Defensive cleanup so the test does not leak a SecureBytes.
            if sb != nil {
                _ = sb.Destroy()
            }
            return
        }
        if !errors.Is(err, ecies.ErrECIESDecryptFailed) &&
           !errors.Is(err, ecies.ErrECIESEnvelopeTooShort) &&
           !errors.Is(err, context.Canceled) &&
           !errors.Is(err, context.DeadlineExceeded) {
            t.Errorf("Decrypt returned non-sentinel error type %T: %v", err, err)
        }
    })
}
```

Seed corpus (in `testdata/fuzz/FuzzECIESDecrypt/`):

| Seed file                    | Bytes                                                                 | Exercises                                                  |
|------------------------------|-----------------------------------------------------------------------|------------------------------------------------------------|
| `empty`                      | `(empty)` — zero-byte envelope                                        | The length-zero `ErrECIESEnvelopeTooShort` early-out       |
| `one-byte`                   | `\x00`                                                                | The single-byte length boundary                            |
| `exactly-min-size-garbage`   | 85 random bytes (deterministically seeded — see below)                 | The min-size-but-not-BIE1 magic-mismatch path             |
| `two-x-min-size-garbage`     | 170 random bytes (deterministically seeded)                            | The min-size-passes-but-CT-shape-fails / MAC-mismatch path |

Seed bytes are deterministically generated at fuzz-target setup time via `rand.Reader`-equivalent fixed seeds (or hardcoded byte literals); the corpus directory is committed under `testdata/fuzz/FuzzECIESDecrypt/`. CI gate (per the implement-phase release-step list): `go test -fuzz=FuzzECIESDecrypt -fuzztime=60s ./internal/transport/ecies/` runs clean — no panic, no new corpus entries representing crashes (any new entry would land in `testdata/fuzz/FuzzECIESDecrypt/` and trigger CI follow-up).

**Rationale**: The chunk contract names `FuzzECIESDecrypt` and the 60 s gate. Constitution VIII lists "ECIES decrypt input handling" as fuzz target #3 — the constitutional gate for SDD-09. The "no panic, every error typed" invariant is the load-bearing security property: a panic in `Decrypt` would crash an HTTP handler (SDD-13) or a CLI process (SDD-16), and an attacker who chose hostile bytes could deny service via crash. The seed corpus accelerates fuzz-coverage convergence — the four seeds cover all the major Decrypt-path branches (length-zero, length-one, min-size-fails-magic, full-shape-fails-MAC) so 60 s of fuzzing actually exercises the verification logic, not just "is this long enough".

**Alternatives considered**:
- *Skip the seed corpus, rely on random-byte coverage alone*: rejected. 60 s of random bytes converges slowly through the length-or-magic early-out; most random bytes never reach the MAC-verification path. Seeds are cheap to write and dramatically improve coverage.
- *Run for 5 minutes instead of 60 s*: deferred. The chunk contract says 60 s; CI cost is a real constraint. A future hardening pass can lengthen if a defect class emerges.
- *Fuzz `Encrypt` separately*: deferred. `Encrypt`'s input space is much smaller (a `*ecdsa.PublicKey` is a structured value; a `[]byte` is the only fuzz-able shape). The chunk contract names FuzzECIESDecrypt as the single 60 s target. A dedicated `FuzzECIESEncrypt` is a future hardening pass.
- *Fuzz the round-trip (Encrypt → Decrypt with mutation between)*: deferred. The seed corpus's `exactly-min-size-garbage` and `two-x-min-size-garbage` seeds approximate this by feeding mutated-shape envelopes to Decrypt directly; a structured "encrypt then mutate then decrypt" fuzz would add complexity for marginal value.

---

## R-009 — Sentinel-leak test: TestECIES_NoLeakOnError

**Decision**: A dedicated test (`TestECIES_NoLeakOnError`) implements the SC-006 / SC-010 "error-message redaction" property:

```go
func TestECIES_NoLeakOnError(t *testing.T) {
    sentinel := testutil.SentinelSecret(9) // "SECRET_SHOULD_NEVER_APPEAR_9"
    priv := generateFreshKey(t)
    pub := &priv.PublicKey

    // Encrypt a plaintext that contains the sentinel as a substring.
    plaintext := []byte("prefix-" + sentinel + "-suffix")
    envelope, err := ecies.Encrypt(t.Context(), pub, plaintext)
    if err != nil {
        t.Fatalf("encrypt failed: %v", err)
    }

    // Mangle the envelope at multiple positions to exercise different decrypt-failure paths.
    cases := []struct {
        name   string
        mangle func([]byte) []byte
    }{
        {"flip-byte-in-magic",       func(e []byte) []byte { e2 := bytes.Clone(e); e2[0] ^= 0x01; return e2 }},
        {"flip-byte-in-pubkey",      func(e []byte) []byte { e2 := bytes.Clone(e); e2[10] ^= 0x01; return e2 }},
        {"flip-byte-in-ciphertext",  func(e []byte) []byte { e2 := bytes.Clone(e); e2[40] ^= 0x01; return e2 }},
        {"flip-byte-in-mac",         func(e []byte) []byte { e2 := bytes.Clone(e); e2[len(e2)-1] ^= 0x01; return e2 }},
        {"truncate-to-min-minus-1",  func(e []byte) []byte { return e[:84] }},
        {"truncate-to-zero",         func(e []byte) []byte { return nil }},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            mangled := tc.mangle(envelope)
            _, err := ecies.Decrypt(t.Context(), priv, mangled)
            if err == nil {
                t.Fatalf("expected error, got nil")
            }
            // Sentinel MUST NOT appear in the err.Error() string.
            testutil.AssertSentinelAbsent(t, sentinel, err.Error())
            // Walk the wrap chain (defensive — the package does NOT wrap, but we assert anyway).
            for cur := err; cur != nil; cur = errors.Unwrap(cur) {
                testutil.AssertSentinelAbsent(t, sentinel, cur.Error())
            }
        })
    }
}
```

The test uses `testutil.SentinelSecret(9)` and `testutil.AssertSentinelAbsent` from SDD-04 — the canonical sentinel-leak helpers. The mangle functions exercise each cryptographic-failure path (magic / pubkey-parse / MAC / length).

**Rationale**: The static-error-messages discipline (R-007) makes the property hold by construction — there is literally no code path in the package that calls `fmt.Errorf` with envelope or plaintext bytes. The test is the runtime witness that the discipline is honoured; if a future change accidentally introduces a `fmt.Errorf("decrypt failed: %x", envelope[…])`, the test fails immediately.

The sentinel-substring check (`testutil.AssertSentinelAbsent`) has zero false-positive risk: the sentinel `SECRET_SHOULD_NEVER_APPEAR_9` is a 28-character ASCII string with no overlap to any error-message word in the package. A leak would surface as a clear test failure.

**Alternatives considered**:
- *Use `slog.NewJSONHandler` to capture log output and assert the sentinel is absent*: not applicable. The package emits zero log lines (FR-014); there is no log surface to capture. The test exercises the `err.Error()` surface only.
- *Encrypt a plaintext that IS the sentinel (no prefix/suffix)*: deferred. The prefix/suffix variant is more realistic (real plaintexts have surrounding context); the test covers the worst case where the plaintext contains the sentinel as a substring.
- *Skip the wrap-chain walk because the package does not wrap*: rejected. The walk is defensive — it costs nothing and catches a future regression where someone adds an unintended `fmt.Errorf("...: %w", err)` wrap.

---

## R-010 — `*ecdsa.PrivateKey` and `*ecdsa.PublicKey` as the public-API types

**Decision**: The public API uses `*ecdsa.PrivateKey` and `*ecdsa.PublicKey` from `crypto/ecdsa`. Internally, conversion to `*secp256k1.PrivateKey` / `*secp256k1.PublicKey` happens only where the decred library is more ergonomic (compressed-pubkey codec via `secp256k1.ParsePubKey` / `SerializeCompressed`); the ECDH step uses `Curve.ScalarMult` directly on the `*ecdsa.*` values, avoiding the conversion when possible.

**Rationale**: The project's existing key-derivation API (SDD-01 `keys.DeriveClientKey`) returns `*ecdsa.PrivateKey`. Aligning SDD-09's public API with that type means consumers (SDD-13, SDD-16) can pass derived keys directly. Using the underlying decred type would force an upfront conversion at every call site.

The internal `*secp256k1.PublicKey` lift is local to one helper function (the compressed-pubkey codec); it does not leak through the public API.

**Alternatives considered**:
- *Use `*secp256k1.PrivateKey` / `*secp256k1.PublicKey` directly in the public API*: rejected. Misaligned with SDD-01's `keys` package output type; would force conversions at every call site.
- *Define a generic interface (`type ECIESKey interface{ ... }`)*: rejected. Constitution III pins secp256k1; an interface would invite future drift. Concrete `*ecdsa.*` types match what `keys.DeriveClientKey` returns.

---

## R-011 — Test data: deterministic ephemeral keypair for FuzzECIESDecrypt

**Decision**: `FuzzECIESDecrypt` uses a deterministic ephemeral keypair generated at fuzz-target setup. The key is derived from a fixed seed (e.g., `bytes.Repeat([]byte{0x42}, 32)` interpreted as a secp256k1 scalar) so that fuzz reproduction is byte-exact across runs. The fuzz harness regenerates this key on each fuzz target invocation; tests do NOT rely on the key surviving across test files.

The non-fuzz tests (TestECIES_*) use `ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)` for fresh, non-deterministic keys per spec User Story 1's "freshly-generated ephemeral keypair on each run" requirement.

**Rationale**: Fuzz targets need deterministic key material so that a corpus entry that triggers a defect can be reproduced byte-exactly via `go test -run=FuzzECIESDecrypt/<id>`. Non-fuzz round-trip tests need fresh keys to honor the spec's per-run-keypair property (and to catch any defect that depends on a specific key).

**Alternatives considered**:
- *Use the same fixed key for both fuzz and unit tests*: rejected. Unit tests benefit from per-run randomness to catch key-dependent defects; fuzz tests benefit from determinism for reproducibility.
- *Use `internal/keys.DeriveClientKey` for fuzz key generation*: rejected. SDD-09's contract is library-provenance-independent (the package operates on raw `*ecdsa.PrivateKey` regardless of derivation provenance). Pulling in `internal/keys` for fuzz fixture would couple the fuzz target to SDD-01 unnecessarily.

---

## R-012 — Package-level concurrency: stateless, race-clean, no shared state

**Decision**: The package holds **zero** package-level mutable state. `Encrypt` and `Decrypt` are pure functions on their inputs (modulo `crypto/rand.Reader` consumption in `Encrypt` for the ephemeral keypair, which is concurrency-safe in stdlib). Multiple goroutines may invoke `Encrypt`/`Decrypt` concurrently with NO synchronisation overhead.

A `TestECIES_ConcurrentRoundTrip` test launches N=64 goroutines, each performing a fresh round-trip with its own freshly-generated keypair, and asserts no race-detector reports under `-race`. The test is part of the floor and runs in CI via `magex test:race`.

**Rationale**: Constitution IX "every goroutine has a clear owner". The package itself spawns no goroutines; consumer goroutines own their own invocations. Statelessness is the simplest concurrency model.

**Alternatives considered**:
- *Add a per-package `sync.Pool` for ephemeral keypair allocation*: deferred. Pooling could shave microseconds off the ephemeral-keypair cost on a hot path; for the v0.1.0 use case (one Encrypt per `/secrets/{name}` response, dozens per minute), the optimization is unjustified. A future hardening pass can add it without changing the public API.
- *Allow Encrypt/Decrypt to share a thread-local scratch buffer*: rejected. Thread-local state introduces lifecycle complexity; the simple per-call allocation is fine.
