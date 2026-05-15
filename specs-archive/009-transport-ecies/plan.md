# Implementation Plan: `internal/transport/ecies` — ECIES Encrypt/Decrypt of Secret Responses (SDD-09)

**Branch**: `009-transport-ecies` | **Date**: 2026-04-28 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/009-transport-ecies/spec.md`
**Chunk contract**: [docs/sdd/SDD-09.md](../../docs/sdd/SDD-09.md)

## Summary

`internal/transport/ecies` is the wire-level secret-response encryption
boundary for hush. The server runs `Encrypt` with the freshly-decrypted
secret value and the client's per-request ephemeral public key (delivered
in the JWT's `ephemeral_pubkey` claim, FR-4) and produces an opaque
envelope of bytes that travels in the HTTP response body. The client
runs `Decrypt` with the matching ephemeral private key (held only in
client-process memory) and recovers the plaintext as a freshly-allocated
`*securebytes.SecureBytes` whose lifetime the caller owns. It backs
**Layer 3 of the seven-layer security stack** (`docs/SECURITY.md` §3
Layer 3) and is the package called out by **AC-7** ("End-to-end ECIES,
no plaintext on the wire"). It is consumed by SDD-13 (server
`/secrets/{name}` handler) and SDD-16 (`hush request`); both consumers
agree on a single envelope shape and a single set of sentinel errors.

Approach (locked by SDD-09 chunk contract + Constitution III/VIII/IX/X/XI;
not subject to research alternatives):

- **ECIES variant — BIE1 (BSV/Electrum-style)** over secp256k1. The
  envelope has the published BIE1 layout: 4-byte magic `"BIE1"` ‖
  33-byte compressed ephemeral public key ‖ AES-256-CBC ciphertext ‖
  32-byte HMAC-SHA256 tag. KDF is `SHA-512(shared_x) → iv ‖ keyE ‖
  keyM` (16+32+16 bytes). Same shape `github.com/bitcoinschema/go-bitcoin`'s
  ECIES helpers produce, so a hypothetical future swap to the named
  library would be wire-compatible. Interop with external Bitcoin
  tooling is **not** a goal — SDD-09 controls both the server and the
  client — but using a published variant avoids ad-hoc cryptographic
  design.

- **Encrypt** (`func Encrypt(ctx, recipientPub, plaintext) ([]byte, error)`):
  1. `ctx.Err()` early-out (FR-013).
  2. Reject `nil` recipient pub or any pub not on the secp256k1 curve
     with `ErrECIESInvalidRecipientKey` — BEFORE any plaintext copy
     (FR-015a, SC-008 error-path zeroing).
  3. Reject empty plaintext with `ErrECIESEmptyPlaintext` — BEFORE any
     cryptographic primitive (FR-015).
  4. Copy caller's plaintext into an internal buffer `pt` and register
     `defer secureZero(pt)` so the buffer is zeroed on every return
     path (FR-008, SC-008).
  5. Generate the ephemeral keypair on `recipientPub.Curve` via
     `ecdsa.GenerateKey(curve, crypto/rand.Reader)`. Register
     `defer secureZeroBigInt(ephPriv.D)` and
     `defer ephPriv.D.SetInt64(0)` so the ephemeral scalar is zeroed
     on every return path.
  6. ECDH via `recipientPub.Curve.ScalarMult(recipientPub.X,
     recipientPub.Y, ephPriv.D.Bytes())` → 32-byte shared X coordinate.
     Register `defer secureZero(sharedX)`.
  7. KDF: `H = SHA-512(sharedX)`; `iv = H[0:16]`, `keyE = H[16:48]`
     (32 bytes for AES-256), `keyM = H[48:64]` (16 bytes; HMAC-SHA256
     accepts any key length per RFC 2104). Register
     `defer secureZero(H[:])`.
  8. PKCS#7-pad the plaintext copy to `aes.BlockSize`. Register
     `defer secureZero(padded)`.
  9. AES-256-CBC encrypt with `(keyE, iv)` → ciphertext.
  10. Serialize the ephemeral public key in **compressed** form (33
      bytes: `0x02|0x03 ‖ X.FillBytes(32)`) — same encoding the BSV
      libraries produce. The decred secp256k1 library's
      `*secp256k1.PublicKey.SerializeCompressed()` provides this
      directly; the conversion `*ecdsa.PublicKey → *secp256k1.PublicKey`
      is a coordinate-to-point lift (`secp256k1.NewPublicKey(x, y)`).
  11. HMAC-SHA256(keyM, "BIE1" ‖ ephPubCompressed ‖ ciphertext) →
      32-byte MAC.
  12. Envelope `= "BIE1"(4) ‖ ephPubCompressed(33) ‖ ciphertext(N) ‖
      MAC(32)`. Returned as a fresh `[]byte`.

- **Decrypt** (`func Decrypt(ctx, recipientPriv, envelope) (*SecureBytes, error)`):
  1. `ctx.Err()` early-out (FR-013).
  2. Length gate: `len(envelope) >= minEnvelopeSize` where
     `minEnvelopeSize = 4 + 33 + aes.BlockSize + 32 = 85` (FR-005,
     SC-004). Length-too-short fires `ErrECIESEnvelopeTooShort` BEFORE
     any cryptographic primitive runs.
  3. Magic gate: `envelope[0:4] == "BIE1"`. Magic mismatch is
     `ErrECIESDecryptFailed` (cryptographic-class failure, indistinguishable
     from wrong-key by sentinel identity per FR-005).
  4. Parse the ephemeral public key from `envelope[4:37]` via
     `secp256k1.ParsePubKey` — which validates curve membership and
     rejects malformed compressed points. Any parse failure is
     `ErrECIESDecryptFailed`.
  5. ECDH via `recipientPriv.Curve.ScalarMult(ephPub.X, ephPub.Y,
     recipientPriv.D.Bytes())` → 32-byte shared X. Register
     `defer secureZero(sharedX)`.
  6. KDF: same SHA-512 derivation as Encrypt. Register
     `defer secureZero(H[:])`.
  7. Verify HMAC: compute expected over `envelope[0:ctEnd]` (where
     `ctEnd = len(envelope) - 32`); `hmac.Equal` (constant-time)
     against `envelope[ctEnd:]`. Mismatch is `ErrECIESDecryptFailed`.
  8. AES-block-shape gate: `(ctEnd - 37) > 0 && (ctEnd - 37) %
     aes.BlockSize == 0`. Bad shape is `ErrECIESDecryptFailed`.
  9. AES-256-CBC decrypt with `(keyE, iv)` into a fresh
     `plaintextBuf := make([]byte, ctEnd - 37)`. Register
     `defer secureZero(plaintextBuf)`.
  10. PKCS#7 unpad — validated. Malformed padding (last-byte > BlockSize,
      last-byte == 0, padding bytes mismatch) is `ErrECIESDecryptFailed`.
  11. Wrap the validated plaintext slice via `securebytes.New(unpadded)`
      — the constructor copies into mlocked memory and zeroes the
      source slice (SDD-02 contract). Return the fresh `*SecureBytes`.
      Caller owns `Destroy()`.

- **Memory zeroization discipline** (FR-008 / FR-009 / SC-007 / SC-008):
  - Encrypt's `pt` (internal plaintext copy), `padded` (PKCS#7-padded
    buffer), `sharedX` (ECDH output bytes), `H[:]` (KDF output) are
    each zeroed via `defer secureZero(buf)` on every return path
    (happy AND error). The `ephPriv.D` is zeroed via
    `defer secureZeroBigInt(ephPriv.D)`. The caller's input slice is
    NOT touched (caller's responsibility per spec User Story 5
    acceptance scenario 1).
  - Decrypt's `sharedX`, `H[:]`, and the intermediate `plaintextBuf`
    are each zeroed via `defer secureZero(buf)` BEFORE return on every
    path. `securebytes.New` zeroes its input by contract — so after
    wrap, only the SecureBytes-internal mlocked buffer holds the
    plaintext.
  - `secureZero(buf []byte)` and `secureZeroBigInt(n *big.Int)` are
    file-private helpers in `ecies.go`. The byte version uses a simple
    `for i := range buf { buf[i] = 0 }` loop (Go's compiler does not
    optimize this away for any package-private call site). The big.Int
    version overwrites the underlying `Word` slice via
    `n.SetInt64(0)` plus a slice-zero of any prior backing — see
    research [R-005](./research.md#r-005).

- **Error mapping** (FR-006 / FR-007 / FR-014 / SC-010):
  - All errors are exported `var Err... = errors.New("static message")`
    declarations — message strings contain ONLY the failure category,
    NEVER a byte from envelope/plaintext/key.
  - `ErrECIESDecryptFailed` ("ECIES decrypt failed"): every cryptographic
    failure in Decrypt — magic mismatch, malformed pubkey, MAC mismatch,
    AES-block-shape failure, PKCS#7 unpad failure, SecureBytes wrap
    failure. "Wrong key" and "tampered envelope" collapse into this
    single sentinel by design (FR-004 / spec User Story 2 acceptance
    scenario 2).
  - `ErrECIESEnvelopeTooShort` ("envelope too short"): envelope length
    below `minEnvelopeSize`. Fires BEFORE any cryptographic primitive
    (FR-005).
  - `ErrECIESEmptyPlaintext` ("empty plaintext"): Encrypt rejects
    empty plaintext input (FR-015).
  - `ErrECIESInvalidRecipientKey` ("invalid recipient key"): Encrypt
    rejects nil pub or wrong-curve pub (FR-015a). Fires BEFORE the
    package allocates or copies any plaintext bytes.

- **Cancellation** (FR-013 / SC-011):
  - Both `Encrypt` and `Decrypt` check `ctx.Err()` ONCE at entry.
    Pre-cancellation returns `ctx.Err()` verbatim (no wrap, no
    package-private substitute). The wrapped chain therefore preserves
    `errors.Is(err, context.Canceled)` and
    `errors.Is(err, context.DeadlineExceeded)` for the two stdlib
    cancellation causes (clarification 2026-04-28).
  - Mid-operation cancellation is NOT honored — both operations are
    short and CPU-bound. Aborting an in-flight scalar multiplication
    has no security benefit and complicates the implementation.

- **Logging & observability** (FR-014 / Constitution X):
  - The package emits **zero** log lines and takes **no** logger
    dependency (clarification 2026-04-28: caller-driven only).
    Errors are the only diagnostic surface, and their `Error()` is a
    static category-string. The `TestECIES_NoLeakOnError` test
    encrypts the project sentinel `SECRET_SHOULD_NEVER_APPEAR_9` (via
    `testutil.SentinelSecret(9)`), mangles the resulting envelope,
    and asserts the sentinel string is absent from `err.Error()` AND
    from any wrapped-error string descended via `errors.Unwrap`.
  - `Decrypt` returns the plaintext only as `*securebytes.SecureBytes`
    — its `LogValue()`, `String()`, and `MarshalJSON()` all return
    `"[redacted]"` (SDD-02 contract). A downstream consumer that
    accidentally logs the result therefore CANNOT leak the plaintext.

- **Fuzz target** (`FuzzECIESDecrypt`, SC-005, Constitution VIII fuzz
  target #3):
  - Feeds random byte streams to `Decrypt` against a fixed
    test-derived ephemeral private key (generated once at fuzz-target
    setup). The fuzz body asserts: (a) `Decrypt` never panics, (b)
    every error is one of `{ErrECIESDecryptFailed,
    ErrECIESEnvelopeTooShort, context.Canceled,
    context.DeadlineExceeded}` — no other error type may surface, (c)
    the input bytes do not appear as a substring of the returned
    error's message (sentinel-leak invariant). Seed corpus: 4
    deterministic entries (zero-byte, one-byte, exactly-min-size
    garbage, two-x-min-size garbage). The 60 s gate is enforced by
    the implement-phase release-step list (`go test
    -fuzz=FuzzECIESDecrypt -fuzztime=60s ./internal/transport/ecies/`).

- **Round-trip property** (SC-001):
  - For plaintext sizes `1`, `1024`, `1048576` bytes, with a
    freshly-generated ephemeral keypair on each run,
    `Encrypt`-then-`Decrypt` produces a `*SecureBytes` whose
    `Use(func(b))` callback observes a slice byte-for-byte equal to
    the original plaintext via `bytes.Equal`. After the caller
    `Destroy`s the SecureBytes, a subsequent `Use` returns
    `securebytes.ErrDestroyed` — confirming the package surrendered
    ownership and keeps no parallel handle (SC-007 / FR-003).

Exported API (locked at SDD-09; mirrored into `docs/PACKAGE-MAP.md` once
the implement commit lands):

```go
// Package github.com/mrz1836/hush/internal/transport/ecies

func Encrypt(ctx context.Context, recipientPub *ecdsa.PublicKey, plaintext []byte) ([]byte, error)
func Decrypt(ctx context.Context, recipientPriv *ecdsa.PrivateKey, envelope []byte) (*securebytes.SecureBytes, error)

// Sentinel errors — full catalogue and trigger conditions in contracts/api.md.
var ErrECIESDecryptFailed       error // every cryptographic decrypt failure
var ErrECIESEnvelopeTooShort    error // envelope length below the documented minimum
var ErrECIESEmptyPlaintext      error // Encrypt reject: empty plaintext input
var ErrECIESInvalidRecipientKey error // Encrypt reject: nil or wrong-curve recipient pub
```

The chunk contract (`docs/sdd/SDD-09.md` §"Exported API to lock in
PACKAGE-MAP.md") names two sentinels (`ErrECIESDecryptFailed`,
`ErrECIESEnvelopeTooShort`); the catalogue above is the spec-derived
completion of the ellipsis. The two additions
(`ErrECIESEmptyPlaintext`, `ErrECIESInvalidRecipientKey`) implement
spec FR-006 + FR-015 + FR-015a + the Session 2026-04-28 clarification
(answer 1: "Two distinct sentinels"). They are NOT collapsible into
the named two without violating spec FR-006's "Callers MUST be able to
identify every rejection category by sentinel identity" guarantee, in
the same way SDD-08's plan expanded its chunk contract's three
sentinels to six.

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`); `CGO_ENABLED=0`
(Constitution IX).

**Primary Dependencies**:
- Go stdlib: `bytes`, `context`, `crypto/aes`, `crypto/cipher`,
  `crypto/ecdsa`, `crypto/hmac`, `crypto/rand`, `crypto/sha256`,
  `crypto/sha512`, `crypto/subtle`, `errors`, `math/big`.
- Existing direct dependency:
  `github.com/decred/dcrd/dcrec/secp256k1/v4` (already in `go.mod`
  from SDD-01) — provides the secp256k1 curve singleton
  (`secp256k1.S256()`), the `secp256k1.ParsePubKey([]byte)` helper for
  compressed-pubkey parsing, and the `*secp256k1.PublicKey` type with
  `SerializeCompressed()` for compressed-pubkey emission.
- Intra-repo: `github.com/mrz1836/hush/internal/vault/securebytes`
  (SDD-02) — the SecureBytes wrapper. SDD-09 imports its constructor
  `New(b []byte) (*SecureBytes, error)`.
- **Zero new external dependencies.** The chunk contract names
  `github.com/bitcoinschema/go-bitcoin` for the ECIES helpers; the
  plan honors the *intent* (BIE1 ECIES over secp256k1) using stdlib
  + the existing `decred/dcrd/dcrec/secp256k1/v4` direct dependency.
  See research [R-002](./research.md#r-002--ecies-stdlib-substitution)
  for the security-equivalence argument and the alternatives
  considered (including `bitcoinschema/go-bitcoin/v2` as a direct
  add and the trusted-sources hierarchy walk through
  `mrz1836/sigil` and the `bsv-blockchain` GitHub organisation). Same
  precedent as SDD-08's R-002 stdlib substitution for ECDSA
  Sign/Verify and SDD-06's R-003 substitution of deprecated
  `filepath.HasPrefix`.

**Storage**: stateless and in-memory only — no disk I/O, no network
I/O. `Encrypt` allocates an internal plaintext copy that is zeroed
before return on every code path. `Decrypt` allocates a fresh
plaintext slice that is wrapped in `*SecureBytes` (whose constructor
zeroes the source). No package-level state of any kind beyond the
sentinel-class `var Err...` declarations.

**Testing**: `go test ./internal/transport/ecies/...` (table-driven
unit tests per `.github/tech-conventions/testing-standards.md`);
`magex test:race` race-clean (concurrent encrypt/decrypt invocations
under `-race` are exercised by `TestECIES_ConcurrentRoundTrip`); `go
test -fuzz=FuzzECIESDecrypt -fuzztime=60s
./internal/transport/ecies/` with no panics and no new corpus rows
representing crashes. Coverage measured via `go test -cover
./internal/transport/ecies/`; target **100%** per Constitution VIII
Critical band ("Vault crypto, key derivation, JWT, ECIES, request
signing").

**Target Platform**: macOS (darwin amd64/arm64) and Linux server
(amd64/arm64) per `.goreleaser.yml`. Windows is out of scope. Zero
platform-conditional code paths — the same source compiles and passes
on every supported `GOOS/GOARCH`. (Note: the SecureBytes mlock seam
in SDD-02 IS platform-conditional, but SDD-09 imports SecureBytes
through its public constructor and never touches the mlock primitives.)

**Project Type**: Single Go module (`github.com/mrz1836/hush`) with a
flat `internal/<domain>` layout per `docs/PACKAGE-MAP.md`.
`internal/transport/ecies` lands as a new sub-package under the
existing `internal/transport/` directory; it is a sibling of SDD-08's
`internal/transport/sign`. The two sub-packages share the
`internal/transport/` parent (for ownership clarity in
`docs/PACKAGE-MAP.md`) and the same general design discipline (leaf
primitives library, no intra-repo runtime imports beyond
`securebytes`); they share no source code.

**Performance Goals**:
- `Encrypt` total wall time: ≤2 ms per call for plaintexts up to
  1 KiB on a 2026-class CPU (dominated by ECDH scalar multiplication
  on secp256k1, generic-curve path).
- `Decrypt` total wall time: ≤2 ms per call for envelopes up to
  1 KiB (same scalar multiplication cost).
- 1 MiB plaintext round-trip: ≤30 ms (dominated by AES-256-CBC
  throughput; ~200 MB/s on the stdlib generic AES path).
- `FuzzECIESDecrypt`: ≥10 k iter/s on a CI runner; the 60 s gate
  exercises ≥600 k randomly-generated envelopes.

**Constraints**:
- **100% test coverage** on `internal/transport/ecies/` —
  Constitution VIII Critical band ("Vault crypto, key derivation,
  JWT, ECIES, request signing"). Every documented sentinel + every
  locked function + every spec edge case has at least one named
  test.
- **Fuzz `FuzzECIESDecrypt` runs ≥60 s clean** (no panic, no
  unbounded allocation, every error a typed sentinel) per the chunk
  contract and Constitution VIII fuzz target #3 (ECIES decrypt input
  handling).
- **Zero panics on hostile input.** Random byte streams (any length,
  any content, including streams whose first 4 bytes happen to be
  `"BIE1"` and whose tail is corrupt) MUST NOT crash `Decrypt`.
- **No `init()` function**, no mutable package-level globals beyond
  the read-only sentinel-class exported `var Err...` declarations.
- **No goroutines.** `Encrypt` and `Decrypt` are pure / synchronous
  CPU operations. The package spawns nothing.
- **No metrics emission, no log lines, no logger dependency** —
  Constitution X type-driven redaction holds by construction
  (FR-014).
- **No envelope / plaintext / key bytes in any error message**
  (FR-007). Tests assert this against a captured `err.Error()`
  string AND against any value the err wraps via repeated
  `errors.Unwrap` descent.
- **The package never holds a long-lived secret of its own.** It
  receives the recipient key as a parameter, returns either an
  envelope (encrypt) or a fresh `*SecureBytes` (decrypt), and
  retains nothing between calls.
- No CGO, no `vendor/`, no `init()`, **zero** goroutine targets.

**Scale/Scope**:
- Two production source files: `ecies.go` (`Encrypt` + `Decrypt` +
  the file-private helpers `ecdh`, `kdf`, `pkcs7Pad`, `pkcs7Unpad`,
  `compressPubKey`, `parseCompressedPubKey`, `secureZero`,
  `secureZeroBigInt`, plus the `minEnvelopeSize` constant) and
  `errors.go` (sentinel error declarations colocated for
  grep-locality, the same locality refinement SDD-08, SDD-06,
  SDD-03, SDD-02, SDD-01 each made under the same constitutional
  reading). One package-doc file: `doc.go` (package comment +
  Constitution citations + Layer-3 roster). The chunk contract's
  "Files:" list named one production file (`ecies.go`); the plan
  reads that as the **minimum** set and adds `errors.go` + `doc.go`
  for grep-locality. No production logic is added beyond what the
  chunk contract describes.
- Two test files: `ecies_test.go` (round-trip 1B/1KB/1MB +
  wrong-key + tampered-envelope variants + empty-plaintext +
  invalid-pub variants + envelope-too-short variants +
  sentinel-leak + ctx-cancel + SecureBytes ownership +
  encrypt-zeroing-on-error + concurrent-round-trip under `-race`)
  and `decrypt_fuzz_test.go` (`FuzzECIESDecrypt` + 4-file seed
  corpus loader). One test-only helper file may emerge during
  implementation (`testutil_test.go`) for shared key-generation +
  envelope-mangling helpers — same locality refinement
  `internal/transport/sign` made; no production logic.
- Estimated ~250 LOC of production Go (`ecies.go` is the largest
  at ~200 LOC) and ~700 LOC of tests.
- Exported surface: 2 functions (`Encrypt`, `Decrypt`), 4 sentinel
  errors. Total exported identifiers: **6**.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principles in scope (per SDD-09)

| Principle | Constraint | Plan compliance |
|-----------|------------|-----------------|
| **III. Defense in Depth — Layer 3 (ECIES transport encryption)** | Each `/secrets/{name}` response is ECIES-encrypted to the client's per-session ephemeral pubkey (`docs/SECURITY.md` §3 Layer 3). Captured traffic at the Tailscale interface MUST contain only opaque envelope bytes — no plaintext secret value ever appears on the wire, in HTTP middleware, in proxies, or in memory dumps of the HTTP stack. | `Encrypt`/`Decrypt` implement BIE1 ECIES over secp256k1 — same algorithmic shape `docs/SECURITY.md` §5 names (`ECIES (secp256k1 via go-bitcoin)`). The server holds the plaintext secret only for the encrypt window; it produces an opaque envelope; the client recovers the plaintext only as a fresh `*securebytes.SecureBytes`. The SecureBytes contract is mlocked + zero-on-Destroy (SDD-02), so the recovered plaintext lives in protected memory until the caller explicitly Destroys it. The on-the-wire envelope contains a 4-byte magic `"BIE1"`, a 33-byte ephemeral compressed pubkey, the AES-256-CBC ciphertext, and a 32-byte HMAC-SHA256 tag — no plaintext byte appears anywhere in the envelope ([R-001](./research.md#r-001--ecies-variant-bie1)). The sentinel-byte search test (User Story 1 acceptance scenario 3) asserts the envelope bytes do not contain the plaintext as a substring. ✅ |
| **VIII. Testing Discipline — TDD + 100% coverage + fuzz target #3** | Test-first; **100% coverage** for security-critical packages (ECIES is named in the Critical band); fuzz target #3 (ECIES decrypt input handling) ≥60 s clean in CI. Every spec FR + every spec SC has at least one named test. | The /speckit-tasks-phase prompt (chunk contract Prompt 4) enforces test-first ordering: every behaviour-contract task has a paired test-writing task scheduled BEFORE it. Coverage gate is `go test -cover ./internal/transport/ecies/` ≥100% in the implement-phase release-step list. Fuzz target `FuzzECIESDecrypt` is constitutional fuzz target #3 ("ECIES decrypt input handling"); the chunk contract names the 60 s gate. The named tests (`TestECIES_RoundTrip_1B`, `TestECIES_RoundTrip_1KB`, `TestECIES_RoundTrip_1MB`, `TestECIES_DecryptWrongKey_Fails`, `TestECIES_DecryptMangledEnvelope_Fails`, `TestECIES_DecryptEmptyEnvelope_TooShort`, `TestECIES_DecryptReturnsSecureBytes`, `TestECIES_NoLeakOnError`) are the floor; tasks.md will expand to one test per documented sentinel + one test per spec User Story acceptance scenario + the fuzz target. ✅ |
| **IX. Idiomatic Go Discipline — context first, no init, no globals** | `context.Context` is the first parameter of every function that does cancellable work. No `init()`. No mutable package-level globals beyond the sentinel-class `var Err...` declarations. Errors wrapped with `%w`. Compare with `errors.Is`. | `Encrypt` and `Decrypt` accept `ctx context.Context` as **first** parameter. **Zero `init()` functions.** The package's only package-level `var`s are the sentinel error declarations, set-once at package load, never mutated — same constitutional class as `internal/keys`, `internal/vault`, `internal/transport/sign` (SDD-01/-03/-08). **Zero goroutines** — `Encrypt` and `Decrypt` are synchronous CPU operations. Errors are returned by sentinel identity and the locked policy is "static error message strings" (FR-007); the package therefore does NOT wrap inside its own returns (the sentinel itself is the failure category) — but consumers' `errors.Is` calls work because the returned error IS the sentinel. The single exception is `ctx.Err()`, which is returned verbatim so `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)` evaluate correctly per FR-013. ✅ |
| **X. Observability & Redaction — no plaintext, envelope, or key bytes in logs/errors** | A secrets-broker that logs the secrets it brokers is worse than no logging at all. Diagnostic surfaces identify failure category, never content. Type-driven redaction is mandatory for any value carrying secret material. | The package emits **zero** log lines and takes **no** logger dependency (FR-014, clarification 2026-04-28: "caller-driven only"). Diagnostic surface is sentinel-error identity only; error messages are static category strings (`"ECIES decrypt failed"`, `"envelope too short"`, `"empty plaintext"`, `"invalid recipient key"`) — no envelope byte, no plaintext byte, no key byte appears in any error message. The `TestECIES_NoLeakOnError` test encrypts `SECRET_SHOULD_NEVER_APPEAR_9` (via `testutil.SentinelSecret(9)`), mangles the envelope, and asserts the sentinel string is absent from `err.Error()` AND from any wrapped-error string descended via repeated `errors.Unwrap`. `Decrypt` returns plaintext only as a `*securebytes.SecureBytes` — whose `LogValue()`, `String()`, and `MarshalJSON()` all return `"[redacted]"` (SDD-02 contract) — so even a downstream consumer that accidentally logs the result CANNOT leak the plaintext. ✅ |
| **XI. Native-First, Minimal Dependencies — no new crypto libraries** | Stdlib first. Every NEW direct dependency requires a written justification per the trusted-sources hierarchy. The crypto stack pinned in Principle III (BIP32, secp256k1, ECIES) is the ONLY cryptographic dependency surface; adding another crypto library requires a constitutional amendment. | **Zero new direct dependencies.** The chunk contract's `github.com/bitcoinschema/go-bitcoin` reference is honored at the *intent* level: the package implements BIE1 ECIES over secp256k1 — the same algorithmic and curve choices the chunk contract names — using **stdlib** primitives (`crypto/aes` for AES-256-CBC, `crypto/hmac`+`crypto/sha256` for the integrity tag, `crypto/sha512` for the KDF, `crypto/rand` for ephemeral-keypair entropy, `crypto/subtle` and `hmac.Equal` for constant-time MAC compare) and the existing `decred/dcrd/dcrec/secp256k1/v4` direct dependency (introduced in SDD-01) for the secp256k1 curve and the compressed-pubkey codec (`secp256k1.ParsePubKey` / `*PublicKey.SerializeCompressed`). This is a stdlib-correct refinement of the chunk contract's named library, in the same spirit as SDD-08's R-002 stdlib substitution for ECDSA Sign/Verify — the spec's behavioural contract (encrypt/decrypt deterministically over a published ECIES variant) is satisfied identically; only the implementation library differs. The PR description for the implement commit will document this divergence verbatim. See research [R-002](./research.md#r-002--ecies-stdlib-substitution) for the full security-equivalence argument and the alternatives considered (including `bitcoinschema/go-bitcoin/v2` as a direct add, `bsv-blockchain/go-sdk` per the trusted-sources hierarchy, and `mrz1836/sigil`). ✅ |

### Other principles (not in scope but checked for non-violation)

- **I (Zero Files at Rest on Agent Machines):** out of scope — this
  package has zero filesystem surface. ✅
- **II (Approval is Human):** out of scope — no approval surface. ✅
- **IV (Supervisor for Daemons):** out of scope — supervisor uses
  this package as a primitives consumer (SDD-19 / SDD-21); SDD-09
  itself defines no daemon lifecycle. ✅
- **V (Staleness is Visible):** out of scope. ✅
- **VI (Tailscale-Only):** out of scope at the package level — the
  package encrypts/decrypts irrespective of network; the Tailscale
  boundary is enforced by SDD-06 (config) + SDD-12 (server
  middleware). ✅
- **VII (CLI Design Standards):** out of scope — this package
  defines no CLI surface. ✅

### Gate result

**PASS** — every principle in scope is satisfied. **Zero Complexity
Tracking entries** (the SDD-09 implementation introduces no new
dependencies and no constitutional deviations). The Constitution
Check is re-evaluated post-design (after Phase 1) below.

## Project Structure

### Documentation (this feature)

```text
specs/009-transport-ecies/
├── plan.md                  # This file (/speckit-plan command output)
├── research.md              # Phase 0 output — locked HOW decisions
├── data-model.md            # Phase 1 output — envelope shape + sentinel catalogue + sizing math
├── quickstart.md            # Phase 1 output — consumer integration recipe (SDD-13 + SDD-16)
├── contracts/
│   └── api.md               # Phase 1 output — exported API contract (locks PACKAGE-MAP §internal/transport)
├── checklists/              # Pre-existing (untouched by /speckit-plan)
├── spec.md                  # WHAT contract (already written by /speckit-specify + /speckit-clarify)
└── tasks.md                 # Phase 2 output (/speckit-tasks command — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/transport/ecies/
├── doc.go                       # Package doc: Constitution III/VIII/IX/X/XI citations + Layer-3 roster
├── ecies.go                     # Encrypt + Decrypt + file-private helpers (ECDH, KDF, PKCS#7, secureZero)
├── errors.go                    # Sentinel error declarations (4 sentinels, static messages)
├── ecies_test.go                # Round-trip + wrong-key + tampered-envelope + empty + invalid-pub + too-short + sentinel-leak + ctx-cancel + ownership + concurrent
├── decrypt_fuzz_test.go         # FuzzECIESDecrypt + seed-corpus loader
└── testdata/
    └── fuzz/
        └── FuzzECIESDecrypt/
            ├── empty                       # zero-byte envelope
            ├── one-byte                    # 1-byte envelope (below minEnvelopeSize)
            ├── exactly-min-size-garbage    # 85-byte random — exercises min-size-but-not-BIE1 path
            └── two-x-min-size-garbage      # 170-byte random — exercises CT-block-shape path

go.mod                           # NO CHANGES — zero new direct dependencies
go.sum                           # NO CHANGES
```

**Structure Decision**: hush is a single Go module
(`github.com/mrz1836/hush`) with a flat `internal/<domain>` layout
defined in `docs/PACKAGE-MAP.md`. SDD-09 fills a new sub-package
`internal/transport/ecies/` under the existing `internal/transport/`
parent, alongside SDD-08's `internal/transport/sign/`. The package
ships three production source files; the chunk contract's "Files:"
list named one (`ecies.go`). The plan adds two: `errors.go` (sentinel
error declarations colocated for grep-locality, the same locality
refinement SDD-08, SDD-06, SDD-03, SDD-02, and SDD-01 each made under
the same constitutional reading) and `doc.go` (package-level doc
comment with Constitution citations). The chunk-contract file list is
read as the **minimum** set: every file the contract names is
present, and the package may add purely declarative files where
idiomatic Go discipline calls for them. No production logic is added
beyond what the chunk contract describes.

The package import path is
`github.com/mrz1836/hush/internal/transport/ecies`. Per
`docs/PACKAGE-MAP.md` §`internal/transport/`, this is a leaf
primitives library: the allowed dependency direction is
`internal/server → internal/transport/ecies` (SDD-13 will consume the
`Encrypt` path) and `internal/cli → internal/transport/ecies`
(SDD-16 will consume the `Decrypt` path); SDD-09 itself imports
nothing intra-repo at runtime beyond `internal/vault/securebytes`
(SDD-02 — for the `*SecureBytes` constructor on the decrypt path).

The `testdata/fuzz/FuzzECIESDecrypt/` directory ships four seed files
matching the spec's edge cases (empty envelope, 1-byte envelope,
exactly-min-size-but-not-BIE1, two-x-min-size garbage) so the 60 s
CI fuzz gate exercises the whole `Decrypt` surface from the first
run rather than spending most of its budget bouncing off the
length-or-magic early-out.

Tests use `internal/testutil` (SDD-04) for the
`SECRET_SHOULD_NEVER_APPEAR_9` sentinel and the
`AssertSentinelAbsent(t, sentinel, haystack)` helper. Test keys are
generated fresh per test run via `ecdsa.GenerateKey(secp256k1.S256(),
rand.Reader)` — there is no need to reuse `internal/keys.DeriveClientKey`
for fixture keys (the package operates on raw `*ecdsa.PrivateKey` /
`*ecdsa.PublicKey`, regardless of derivation provenance), and using
fresh keys keeps each test independent.

## Post-Design Constitution Re-check

Re-evaluated after Phase 1 design artifacts (`research.md`,
`data-model.md`, `contracts/api.md`, `quickstart.md`) were drafted:

| Principle | Phase 1 introduced | Re-check |
|-----------|--------------------|----------|
| **III** | [data-model.md](./data-model.md) formalises the BIE1 envelope shape (4+33+N+32 bytes), the `minEnvelopeSize=85` invariant, and the per-call ephemeral-keypair lifecycle. [contracts/api.md](./contracts/api.md) documents the failure-mode collapse rule (FR-005: wrong-key and tampered-envelope share `ErrECIESDecryptFailed`). [quickstart.md](./quickstart.md) shows the SDD-13 server-side `Encrypt` recipe and the SDD-16 client-side `Decrypt`-then-`Use`-then-`Destroy` recipe. The Tailscale-interface property ("captured traffic shows opaque envelope bytes only") is enforced at the wire-shape level — every byte of the envelope is either magic, ephemeral pubkey, ciphertext, or MAC; no plaintext byte appears. | PASS — the layer-3 contract is enforced by envelope shape, not by an additional in-band guard. |
| **VIII** | [contracts/api.md](./contracts/api.md) enumerates 14+ named tests across the two test files plus four fuzz seed entries. Coverage gate is 100% per the Critical band. | PASS — every spec FR and every spec SC has at least one named test; the fuzz target ships with a deterministic 4-file seed corpus so CI's first run is meaningful. |
| **IX** | Phase 1 confirmed: zero `init()`, zero mutable globals beyond the documented sentinel-class `var Err...`, all primitives ctx-first, zero goroutines, errors returned by sentinel identity (no `%w` wrap inside the package; consumers compare via `errors.Is` directly against the exported sentinels). The single `ctx.Err()` return path preserves `errors.Is(err, context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)` because the underlying `ctx.Err()` IS one of those values. | PASS — no new violations introduced. |
| **X** | The error catalogue is finalised. Every sentinel's identity is the failure category; no error message embeds envelope, plaintext, or key bytes. The `TestECIES_NoLeakOnError` test scaffolding in `ecies_test.go` is in the test floor. | PASS — diagnostic surfaces audited and clean. |
| **XI** | Zero new direct dependencies; the `go.mod` diff for the implement commit is empty. The chunk contract's named library (`go-bitcoin`) is substituted for stdlib + the existing `decred/dcrd/dcrec/secp256k1/v4` dep; the substitution is documented in research [R-002](./research.md#r-002--ecies-stdlib-substitution) and re-asserted in the implement-phase PR description. | PASS — no Complexity Tracking entry needed (the substitution is constitutionally-aligned, not a deviation). |

**Final result**: PASS. **No Complexity Tracking entries.** No new
violations introduced by the design phase; no new dependencies
introduced; the Layer-3 contract is enforced via envelope shape and
the per-call ephemeral-keypair lifecycle.

## Complexity Tracking

> Fill ONLY if Constitution Check has violations that must be justified.

*(empty — no constitutional deviations)*
