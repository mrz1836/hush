# Phase 0 Research — internal/keys (SDD-01)

**Branch:** `001-keys-derivation`
**Constitution gate:** principles III, VIII, IX, X, XI in scope.

## Scope of this document

The chunk contract (`docs/sdd/SDD-01.md`) and the Constitution III +
Security Requirements table **lock** the cryptographic primitives,
parameters, paths, and dependencies. Per the user-supplied plan
prompt, **research MUST NOT propose alternatives** for these locked
decisions — Constitution XI requires a constitutional amendment for
new crypto dependencies.

This document therefore records:

1. The locked decisions (with citations back to the constitution /
   SPEC), so the implement phase has a single grep-able source.
2. The HOW questions the locked contract leaves open, with a
   resolution and rationale for each.

It is **not** a survey of alternative KDFs, alternative HD-wallet
libraries, or alternative fingerprint schemes — those choices are
already made.

---

## A. Locked decisions (no alternatives evaluated)

### A1. KDF: Argon2id with fixed parameters

- **Decision:** `golang.org/x/crypto/argon2.IDKey(passphrase, salt, time=4, memory=256*1024, threads=4, keyLen=64)`.
- **Rationale:** Constitution III ("Defense in Depth Through Crypto
  Layering"), Security Requirements table, and SPEC FR-2 all list
  these parameters as non-negotiable. SDD-01 §"Behaviour contracts"
  restates them. Memory unit is KiB in `argon2.IDKey`'s API:
  `256 * 1024 KiB = 256 MiB ≈ 256 MB`.
- **Alternatives considered:** **None.** Phase-0 research forbidden
  from proposing alternatives (Constitution XI; user prompt).

### A2. HD derivation: BIP32 over secp256k1 via go-bitcoin/v2

- **Decision:** `github.com/bitcoinschema/go-bitcoin/v2` is the sole
  source of BIP32 + secp256k1 primitives.
- **Rationale:** Constitution III pins the crypto stack to "BIP32
  (go-bitcoin)". Constitution XI forbids adding a new crypto
  dependency without amendment. go-bitcoin/v2 is referenced as the
  canonical secp256k1 source across SDD-08 (request signing) and
  SDD-09 (ECIES), so consistency requires `internal/keys` to use the
  same library.
- **Alternatives considered:** **None.** (e.g.
  `btcsuite/btcd/btcutil/hdkeychain` would add a parallel crypto
  surface — forbidden.)

### A3. BIP32 path layout

- **Decision:** Hush coin-type `7743'` (mirrors the vault port);
  hardened indexes only (high bit `0x80000000` set):
  - `m/44'/7743'/0'` → JWT signing key (secp256k1)
  - `m/44'/7743'/1'` → vault encryption key (32-byte symmetric)
  - `m/44'/7743'/2'` → audit signing key (secp256k1)
  - `m/44'/7743'/3'/{machine_index}` → per-machine client keypair
- **Rationale:** SPEC FR-3, SDD-01 contract, and SECURITY Layer 1
  specify this layout verbatim. Hardened-only paths defend against
  parent-key recovery if a child private key leaks (BIP32 standard
  guidance).
- **Alternatives considered:** **None.** SPEC FR-3 is locked; SDD-01
  forbids renumbering.

### A4. Sentinel errors

- **Decision:** Two exported package-level vars:
  - `ErrPassphraseTooShort = errors.New("hush/keys: passphrase too short")`
  - `ErrSaltMissing        = errors.New("hush/keys: salt missing or wrong length")`
- **Rationale:** Constitution IX mandates "sentinel errors as
  exported package-level `var Err... = errors.New(...)`" and
  forbids comparing error strings. Spec FR-003 / FR-004 require
  named errors that callers can match programmatically.
- **Alternatives considered:** **None.** Constitution IX is
  prescriptive.

### A5. Context discipline

- **Decision:** `DeriveMasterSeed` accepts `ctx context.Context` as
  the first parameter and inspects it **once** at function entry. If
  `ctx.Err() != nil`, that error is returned immediately and
  Argon2id is **not** invoked. After entry, the in-progress
  derivation runs to completion regardless of subsequent
  cancellation. `DeriveJWTSigningKey`, `DeriveVaultEncKey`,
  `DeriveAuditSigningKey`, `DeriveClientKey`, and
  `PublicKeyFingerprint` perform pure CPU work bounded in the
  microseconds and therefore do not carry `ctx`, per Constitution IX
  ("first parameter of any function that does I/O, can be
  cancelled, or has a timeout"). Spec FR-013 ratifies this for
  the master seed function; the spec clarification session
  (2026-04-27) confirms entry-only inspection.
- **Rationale:** `golang.org/x/crypto/argon2` exposes no
  cancellation hook; treating the call as interruptible would
  invite half-derived seeds (a catastrophic determinism failure for
  this package). Pre-cancellation rejection costs nothing and lets
  callers wire timeouts at boundary code without surprises.
- **Alternatives considered:** **None.** The clarification session
  closed the design space.

---

## B. Open HOW questions resolved by this research

Each question below was left open by the locked contract; this section
selects a single resolution per question and documents the rationale.

### B1. How to convert a BIP32 secp256k1 private scalar to `*ecdsa.PrivateKey`?

- **Decision:** Derive the BIP32 child node via go-bitcoin/v2's
  HD-wallet API; extract the 32-byte private scalar as `[]byte`;
  construct `*ecdsa.PrivateKey` whose `D` field holds the scalar
  (as `*big.Int`) and whose `PublicKey.X / Y / Curve` are computed
  via the curve's scalar-base-mult. Use the secp256k1 curve
  `elliptic.Curve` instance exported by go-bitcoin/v2.
- **Rationale:** The locked API for the package returns Go-stdlib
  `*ecdsa.PrivateKey` (not a go-bitcoin opaque type), which is the
  type `crypto/ecdsa.SignASN1` operates on directly. Holding the
  scalar in `D` (`*big.Int`) is the canonical Go-stdlib
  representation. Constructing the public point from the scalar
  matches `crypto/ecdsa.GenerateKey`'s internal shape.
- **Why not return a go-bitcoin private-key type instead?** The
  consumers (SDD-07 JWT signing, SDD-14 client-key registration)
  span both stdlib (`crypto/ecdsa.SignASN1`, `crypto/x509` for
  fingerprints) and go-bitcoin (Bitcoin-message signing for
  request signatures). `*ecdsa.PrivateKey` is the lowest common
  denominator and avoids forcing every consumer to import
  go-bitcoin. The conversion happens once, at this boundary.
- **Alternatives considered:** **N/A — the public API signature is
  locked** by SDD-01 and the user-supplied prompt
  (`*ecdsa.PrivateKey` return type). Phase 0 only confirms the
  conversion is realisable with the locked dependency set.

### B2. How to extract a 32-byte vault encryption key from BIP32 path `m/44'/7743'/1'`?

- **Decision:** Take the 32-byte private scalar of the BIP32 child
  node at `m/44'/7743'/1'` directly as the AES-256 key (`[]byte` of
  length 32). The scalar is uniformly distributed modulo the
  secp256k1 group order, which is dense in `[0, 2^256)`; treating
  it as 32 bytes of key material loses negligible entropy and is
  consistent with BIP32 use as a generic HD master.
- **Rationale:** BIP32's Layer-1 promise is that private scalars
  beneath hardened paths are independent of one another; using the
  32-byte scalar directly avoids inventing a second KDF (which
  would require Constitution XI's amendment process for the
  primitive choice). AES-256 expects exactly 32 bytes.
- **Alternatives considered:**
  - HKDF over the scalar — rejected: introduces a new primitive
    (`golang.org/x/crypto/hkdf`) that is **not** in the locked
    crypto stack pinned by Constitution III.
  - SHA-256 of the scalar — rejected: same objection plus a
    measurable loss of guarantee that the output is uniformly
    random over `[0, 2^256)`.

### B3. How to compute the 16-hex-char `PublicKeyFingerprint`?

- **Decision:**
  ```
  fingerprint = lowercase_hex( first_8_bytes( SHA-256( SEC1_compressed(pub) ) ) )
  ```
  - `SEC1_compressed(pub)` is the 33-byte compressed serialization
    of the secp256k1 public point (`0x02|0x03 || X` per SEC1).
  - First 8 bytes of the SHA-256 digest → 16 lowercase hex chars
    (FR-009 / SC-006).
- **Rationale:** Compressed-point serialization is canonical for
  secp256k1 public keys (BIP32, BIP44, BIP340 contexts) and is
  available via go-bitcoin/v2; SHA-256 is the project's pinned hash
  (Security Requirements §5). 64 bits of fingerprint provides a
  collision probability of `~2^-32` over a 1,000-key sample
  (SC-006: "no two distinct keys collide" — a birthday-bound of
  `~5.4 * 10^-7` for 1,000 keys, well within the
  "overwhelming probability" wording of FR-009).
- **Alternatives considered:**
  - HASH160 (SHA-256 then RIPEMD-160) — rejected: introduces
    RIPEMD-160 to the package's crypto surface (currently
    stdlib-only beyond go-bitcoin). Stdlib `crypto/sha256` is
    sufficient.
  - Bitcoin-style address fingerprint (Base58Check) — rejected:
    the spec demands lowercase hex, not Base58.
  - Truncated full-key SHA-256 over uncompressed point — rejected:
    less canonical and 32 bytes longer than compressed; offers
    no benefit.

### B4. Pre-Argon2id validation order

- **Decision:** In `DeriveMasterSeed`, the call sequence is exactly:
  1. `if err := ctx.Err(); err != nil { return nil, err }`
  2. `if len(passphrase) < 12 { return nil, ErrPassphraseTooShort }`
  3. `if len(salt) != 16 { return nil, ErrSaltMissing }`
  4. `seed := argon2.IDKey(passphrase, salt, 4, 256*1024, 4, 64)`
  5. `return seed, nil`
- **Rationale:** FR-012 mandates rejection before Argon2id runs;
  SC-004 caps validation latency at `< 100ms`. Step (1) honours
  the cancellation contract from FR-013 / clarification (Q-1).
  Sentinel-error returns happen with no allocation beyond the
  error return tuple — well under the latency cap.
- **Alternatives considered:** **None.** Order is fixed by the
  spec.

### B5. Concurrency safety

- **Decision:** No package-level mutable state. Every exported
  function is pure (its outputs depend solely on its inputs). Two
  goroutines invoking the same exported function with the same
  inputs MUST observe identical outputs (Edge-case "Determinism
  under concurrent calls", FR-002).
- **Rationale:** `argon2.IDKey` is pure; go-bitcoin's BIP32
  derivation is pure; SHA-256 is pure. No shared state to protect.
  No mutex needed. No `init()` either (Constitution IX
  prohibition).
- **Alternatives considered:** **None.**

### B6. Cross-architecture determinism

- **Decision:** Ride on the determinism guarantees of
  (a) `golang.org/x/crypto/argon2.IDKey` and (b) BIP32 over
  secp256k1, both of which are big-endian-byte-stream-defined and
  CPU-architecture-independent. Document this in a unit test that
  compares a derivation against a static known-answer vector.
- **Rationale:** SC-003 requires byte-for-byte identical seeds and
  sub-keys across processes; FR-002 extends this to OS / CPU
  architecture. Both Argon2id (RFC 9106) and BIP32 (BIP-32 spec)
  define their byte order explicitly, so determinism is a property
  of the spec, not the implementation. The known-answer vector
  test (KAT) prevents regressions if either dependency ever
  mis-ports endian-ness on a new architecture.
- **Alternatives considered:** **None.**

### B7. Memory hygiene boundary

- **Decision:** This package returns `[]byte` / `*ecdsa.PrivateKey`
  to the caller and does **not** zero them after return. Memory
  hygiene (mlock, zero on free) is owned by `internal/vault`'s
  `SecureBytes` (SDD-02), which the consumer wraps the returned
  material in.
- **Rationale:** SDD-01 anti-contract: "no imports from
  `internal/*`". Importing `SecureBytes` would violate the
  leaf-package rule and would couple key derivation to vault
  internals. The Spec Assumptions section confirms this is the
  consumer's responsibility.
- **Alternatives considered:** **None.** Constitution III's seven
  layers are purposely independent.

---

## C. Test strategy notes (locked by SDD-01)

The following are not research questions but reminders of what the
chunk contract demands; they are recorded here so Phase 1 can
reference them.

- **Unit tests** (table-driven, deterministic fixtures):
  `TestDeriveMasterSeed_Deterministic`,
  `TestDeriveMasterSeed_RejectsShortPassphrase`,
  `TestDeriveMasterSeed_RejectsBadSalt`,
  `TestDeriveJWTSigningKey_Path`,
  `TestDeriveClientKey_MachineIndexIsolation`,
  `TestPublicKeyFingerprint_Stable`.
- **Fuzz target:** `FuzzDeriveMaster` — random passphrase
  (`len ≥ 12`) + 16-byte salt; assert no panic, deterministic
  re-derivation in a single fuzz iteration, output length 64.
  ≥ 60s clean run required by AC-9 / SC-002.
- **Sentinel-leak test:** **N/A** for this package — it has no
  logging surface; SDD-02 owns the redaction sentinel test.
- **Race test:** `go test -race ./internal/keys/` clean.
- **Coverage:** 100% (Constitution VIII, codecov gate).

---

## D. Summary: every NEEDS-CLARIFICATION resolved

| # | Question | Resolution |
|---|----------|-----------|
| B1 | secp256k1 → `*ecdsa.PrivateKey` conversion | Build `*ecdsa.PrivateKey` from BIP32 child scalar + go-bitcoin's secp256k1 curve. |
| B2 | Vault enc-key extraction | Use the 32-byte BIP32 child scalar at path `m/44'/7743'/1'` directly. |
| B3 | Fingerprint algorithm | `lowercase_hex(SHA-256(compressed_pub)[:8])` → 16 hex chars. |
| B4 | Validation order | ctx → passphrase length → salt length → Argon2id. |
| B5 | Concurrency | Pure functions; no shared state; no mutex. |
| B6 | Cross-arch determinism | Argon2id + BIP32 are byte-defined; KAT test guards regressions. |
| B7 | Memory hygiene | Caller's responsibility (SDD-02 `SecureBytes`); not this package. |

No `[NEEDS CLARIFICATION]` markers remain.
