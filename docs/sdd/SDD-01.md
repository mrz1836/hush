# SDD-01 — `internal/keys` (Argon2id + BIP32 HD derivation)

**Phase:** 1
**Package:** `internal/keys`
**Files:** `derive.go`, `paths.go`, `client.go`, `fingerprint.go`, `*_test.go`, `derive_fuzz_test.go`
**Branch:** `001-keys-derivation` (created by the `before_specify` git hook)
**Blocked by:** none
**Blocks:** SDD-03, SDD-07, SDD-08, SDD-09 (indirectly), SDD-15
**Primary AC:** AC-7 (Bitcoin crypto: BIP32 hierarchy)
**Coverage target:** 100%

**Behaviour contracts (MUST):**
- Argon2id master seed via `time=4`, `memory=256*1024 KiB`, `threads=4`, `keyLen=64` (Constitution III + Security Requirements; non-negotiable)
- BIP32 derivation paths from `docs/SPEC.md` FR-3:
  - `m/44'/7743'/0'` → JWT signing (secp256k1)
  - `m/44'/7743'/1'` → vault encryption (32 bytes for AES-256)
  - `m/44'/7743'/2'` → audit signing (secp256k1)
  - `m/44'/7743'/3'/{machine_index}` → per-agent client keypair
- Use `github.com/bitcoinschema/go-bitcoin/v2` + `golang.org/x/crypto/argon2` only (Constitution XI)
- 16-hex-char public key fingerprint helper for client-key registration UX
- All exported functions take `context.Context` first (Constitution IX)
- Reject passphrase `< 12` bytes with `ErrPassphraseTooShort` BEFORE invoking Argon2id
- Reject salt `!= 16` bytes with `ErrSaltMissing`
- All derivations deterministic given same inputs

**Anti-contracts (MUST NOT):**
- Persist any derived material to disk
- Use `math/rand`
- Log passphrase, seed, salt, or any derived key (Constitution X)
- Import `internal/*` (this is a leaf package)
- Convert `[]byte` secret material to `string` anywhere in this package

**Tests required:**
- Unit (table-driven, deterministic): `TestDeriveMasterSeed_Deterministic`, `TestDeriveMasterSeed_RejectsShortPassphrase`, `TestDeriveMasterSeed_RejectsBadSalt`, `TestDeriveJWTSigningKey_Path`, `TestDeriveClientKey_MachineIndexIsolation`, `TestPublicKeyFingerprint_Stable`
- Fuzz: `FuzzDeriveMaster` ≥60s clean — random passphrase (≥12 bytes) + 16-byte salt; assert no panic, deterministic re-derivation, output length 64
- Sentinel-leak: **N/A** — this package has no logging surface; SDD-02 (`SecureBytes`) owns the redaction sentinel test
- Race: `go test -race ./internal/keys/`

**Constitutional principles in scope:** III (Layer 1), VIII (100% coverage + TDD), IX (idiomatic Go), X (no logging secrets), XI (no new crypto deps beyond go-bitcoin + golang.org/x/crypto)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `func DeriveMasterSeed(ctx context.Context, passphrase, salt []byte) ([]byte, error)`
- `func DeriveJWTSigningKey(seed []byte) (*ecdsa.PrivateKey, error)`
- `func DeriveVaultEncKey(seed []byte) ([]byte, error)`
- `func DeriveAuditSigningKey(seed []byte) (*ecdsa.PrivateKey, error)`
- `func DeriveClientKey(seed []byte, machineIndex uint32) (*ecdsa.PrivateKey, error)`
- `func PublicKeyFingerprint(pub *ecdsa.PublicKey) string`
- `var ErrPassphraseTooShort, ErrSaltMissing` (sentinel errors per Principle IX)

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. Do NOT
chain them in one session — speckit persists each artifact to disk
(`spec.md`, `plan.md`, `tasks.md`, plus `.specify/feature.json`) so
fresh sessions reload state without losing fidelity.

All commits for this chunk are deferred to a single combined commit at
the end of Prompt 5 (Implement). Do not commit between phases.

Specify (Prompt 1) and Plan (Prompt 3) are verbose because they lock
the WHAT and HOW respectively, and downstream phases inherit any
drift. Clarify, Tasks, and Implement are lean because they read the
already-locked artifacts off disk.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-01 (internal/keys: Argon2id
+ BIP32 HD derivation) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles III, VIII, X — these encode the non-negotiable ACs this chunk must satisfy)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-2, FR-3, FR-6, AC-7)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-7 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-01.md  (the full chunk contract — your source of truth for what spec.md must encode)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/keys package derives the entire hush key hierarchy
(JWT-signing, vault-encryption, audit-signing, per-machine client
keypair) deterministically from a passphrase + salt, using Argon2id
as the master KDF and BIP32 hierarchical derivation for the sub-keys.
It is consumed by SDD-03 (vault), SDD-07 (JWT), SDD-08 (request
signing), SDD-09 (ECIES), and SDD-15 (`hush init`).

The spec MUST encode these acceptance-level (WHAT) requirements.
Treat each as non-negotiable — if /speckit-specify's "informed
guesses" would soften any of them, override the guess to match this
list:

- Master seed derivation uses Argon2id with parameters
  time=4, memory=256*1024 KiB (256 MB), threads=4, keyLen=64.
  These parameters are non-negotiable per Constitution III + the
  Security Requirements section.
- The four BIP32 paths are exactly:
    m/44'/7743'/0'                    → JWT-signing key   (secp256k1)
    m/44'/7743'/1'                    → vault encryption  (32 bytes)
    m/44'/7743'/2'                    → audit-signing key (secp256k1)
    m/44'/7743'/3'/{machine_index}    → per-machine client keypair
- Same passphrase + salt MUST produce the same keys (determinism).
- A passphrase < 12 bytes MUST be rejected as a distinct, named error.
- A salt of any length other than 16 bytes MUST be rejected as a
  distinct, named error.
- A 16-hex-character public-key fingerprint helper exists for
  client-registration UX.

The spec MUST NOT encode HOW (no library names, no file layout, no
Go-specific idioms, no package internals). Those are plan-phase
concerns.

Acceptance criterion: AC-7 (Bitcoin crypto: BIP32 hierarchy).

Action — run exactly one command:
  /speckit-specify "internal/keys: derive the hush key hierarchy (JWT signing, vault encryption, audit signing, per-machine client keypair) deterministically from a passphrase + salt using Argon2id + BIP32"

The before_specify hook will create branch 001-keys-derivation.
Confirm the branch was created.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. If the contract
dictates the answer, fill it in. Otherwise leave the marker —
/speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-01 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-01.md (the chunk contract
— consult it if /speckit-clarify surfaces an ambiguity that the
contract already answers).

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-01 (internal/keys: Argon2id +
BIP32 HD derivation) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check against this)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-2, FR-3, FR-6, AC-7)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/keys section — the API contract you will lock)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 1 + Layer 5)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md  (§2 fuzz target list — FuzzDeriveMaster is yours)
- /Users/mrz/projects/hush/docs/sdd/SDD-01.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check gate — if it fires, fix the plan to comply,
do NOT bypass.

Scope:
- Package: internal/keys
- Files: derive.go, paths.go, client.go, fingerprint.go,
  derive_test.go, paths_test.go, client_test.go, derive_fuzz_test.go
- Exported API:
    func DeriveMasterSeed(ctx context.Context, passphrase, salt []byte) ([]byte, error)
    func DeriveJWTSigningKey(seed []byte) (*ecdsa.PrivateKey, error)
    func DeriveVaultEncKey(seed []byte) ([]byte, error)
    func DeriveAuditSigningKey(seed []byte) (*ecdsa.PrivateKey, error)
    func DeriveClientKey(seed []byte, machineIndex uint32) (*ecdsa.PrivateKey, error)
    func PublicKeyFingerprint(pub *ecdsa.PublicKey) string
    var ErrPassphraseTooShort, ErrSaltMissing

Implementation contract (HOW — locked):
- Crypto deps: github.com/bitcoinschema/go-bitcoin/v2 (secp256k1
  + BIP32) + golang.org/x/crypto/argon2 (Argon2id). NO other crypto
  deps may be introduced — Constitution XI requires an amendment
  for new crypto deps. Phase-0 research MUST NOT propose alternatives.
- Argon2id constants: time=4, memory=256*1024 KiB, threads=4,
  keyLen=64. Encode as named package constants. Non-negotiable.
- BIP32 path constants: m/44'/7743'/{0,1,2}', /3'/{machine_index}.
  Encode as named package constants.
- ctx context.Context as the FIRST parameter on every exported
  function (Principle IX).
- Sentinel errors ErrPassphraseTooShort, ErrSaltMissing as package
  variables.
- internal/keys is a LEAF package: no imports from internal/*.
- No file persistence; no math/rand; no []byte → string conversion
  of secret material; no logging of any secret value.

Coverage target: 100%.
Constitutional principles in scope: III, VIII, IX, X, XI.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-01 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-01.md.

NOTE: /speckit-tasks defaults to NO test tasks unless explicitly
told otherwise. This project is TDD-mandatory (Constitution VIII).
Pass TDD as the command argument.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task for that behaviour. Coverage target: 100%. Final phase MUST include magex format:fix, magex lint, magex test:race, and go test -fuzz=FuzzDeriveMaster -fuzztime=60s ./internal/keys/"

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-01 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-01.md (the chunk contract
— re-consult it if you start to drift mid-implementation).

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Fuzz (60s minimum, no crashes / no new bug corpus):
     go test -fuzz=FuzzDeriveMaster -fuzztime=60s ./internal/keys/
3. Verify coverage = 100%:
     go test -cover ./internal/keys/
4. Append "Exported API — locked at SDD-01" section to
   docs/PACKAGE-MAP.md under internal/keys (signatures listed in
   the chunk doc).
5. Update docs/AC-MATRIX.md AC-7 row with the new test file paths.
6. Mark SDD-01 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add internal/keys/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(keys): Argon2id KDF + BIP32 HD derivation (SDD-01)"

Final message: confirm gates passed, fuzz clean, coverage = 100%,
the three docs updated, and the combined commit created.
```
