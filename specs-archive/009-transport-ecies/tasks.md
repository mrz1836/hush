---

description: "Tasks for SDD-09 — internal/transport/ecies (BIE1 ECIES encrypt/decrypt of secret responses)"
---

# Tasks: `internal/transport/ecies` — BIE1 ECIES Encrypt/Decrypt of Secret Responses (SDD-09)

**Input**: Design documents from `/specs/009-transport-ecies/`
**Prerequisites**: plan.md (loaded), spec.md (loaded), research.md (loaded), data-model.md (loaded), contracts/api.md (loaded), quickstart.md (loaded)
**Chunk contract**: [docs/sdd/SDD-09.md](../../docs/sdd/SDD-09.md)

**Tests**: TDD-MANDATORY per Constitution VIII. Every behaviour-contract task has a test-writing task scheduled BEFORE it; tests MUST be written first and MUST FAIL before the corresponding implementation task is run.

**Coverage target**: 100% (`go test -cover ./internal/transport/ecies/` ≥ 100%, Constitution VIII Critical band — ECIES wire encryption).

**Fuzz target**: `FuzzECIESDecrypt` ≥ 60 s clean (Constitution VIII fuzz target #3).

**Organization**: Tasks are grouped by user story (per spec.md priorities P1/P2). Each phase is independently testable.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: User story label (US1, US2, US3, US4, US5, US6); setup/foundational/polish carry no story label
- All file paths are absolute from repo root

## Path Conventions

- **Single Go module**: `github.com/mrz1836/hush`. Production sources under `internal/transport/ecies/`. Tests colocated as `*_test.go`. Fuzz seed corpus under `internal/transport/ecies/testdata/fuzz/FuzzECIESDecrypt/`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the package skeleton and the declarative scaffolding (sentinels, package doc, fuzz seed corpus) that every later phase depends on.

- [X] T001 Create the package directory `internal/transport/ecies/` with the package-doc file `internal/transport/ecies/doc.go` containing the package comment, the Constitution III/VIII/IX/X/XI citations, and the Layer-3-roster pointer (per [plan.md §Project Structure](./plan.md))
- [X] T002 [P] Create `internal/transport/ecies/errors.go` declaring the four sentinels (`ErrECIESDecryptFailed`, `ErrECIESEnvelopeTooShort`, `ErrECIESEmptyPlaintext`, `ErrECIESInvalidRecipientKey`) per [research R-007](./research.md#r-007--sentinel-error-catalogue-and-static-error-messages) — all `var ErrXxx = errors.New("hush/transport/ecies: <message>")`, no wrap relationships, static messages only
- [X] T003 [P] Create `internal/transport/ecies/testdata/fuzz/FuzzECIESDecrypt/` directory and add the four seed-corpus files described in [research R-008](./research.md#r-008--fuzz-target-shape-fuzzeciesdecrypt): `empty` (0 bytes), `one-byte` (1 byte), `exactly-min-size-garbage` (85 deterministic bytes), `two-x-min-size-garbage` (170 deterministic bytes)

**Checkpoint**: Package compiles (`go build ./internal/transport/ecies/`); sentinels exist; fuzz seed corpus present.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Add the file-private constants, the secure-zero helpers, and the test-only helpers (`generateFreshKey`, `generateFuzzKey`, envelope-mangling helpers) that every later test and implementation phase consumes.

**⚠️ CRITICAL**: No user-story phase may begin until Phase 2 is complete — the `secureZero` helpers and the test fixture helpers are foundational for every test file and every implementation path.

- [X] T004 Declare the file-private `minEnvelopeSize = 4 + 33 + aes.BlockSize + sha256.Size` (= 85) constant and the `bie1Magic = []byte{'B','I','E','1'}` sentinel in `internal/transport/ecies/ecies.go` per [data-model.md §1.2](./data-model.md) — placeholder file containing only constants + package declaration (helpers and `Encrypt`/`Decrypt` land in later phases)
- [X] T005 [P] Implement the file-private memory-zeroization helpers `secureZero(buf []byte)` and `secureZeroBigInt(n *big.Int)` in `internal/transport/ecies/ecies.go` per [research R-005](./research.md#r-005--memory-zeroization-discipline) — simple loop pattern for the byte version; `SetInt64(0)` plus defensive copy zero for the big.Int version
- [X] T006 [P] Create `internal/transport/ecies/testutil_test.go` with the test-only helpers: `generateFreshKey(tb testing.TB) *ecdsa.PrivateKey` (uses `ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)`) and `generateFuzzKey(tb testing.TB) *ecdsa.PrivateKey` (deterministic key from the fixed seed `bytes.Repeat([]byte{0x42}, 32)` per [research R-011](./research.md#r-011--test-data-deterministic-ephemeral-keypair-for-fuzzeciesdecrypt))
- [X] T007 [P] Add the envelope-mangling helpers to `internal/transport/ecies/testutil_test.go`: `mangleByte(envelope []byte, position int) []byte` (clones and XORs one byte), `truncateEnvelope(envelope []byte, length int) []byte` (clones and truncates), and `appendByte(envelope []byte) []byte` (clones and appends one byte) — used by the wrong-key, mangled, truncated, and appended-byte test families

**Checkpoint**: Foundation ready — every later test can construct a fresh or deterministic key and mangle an envelope; every later implementation can call `secureZero` to honor FR-008.

---

## Phase 3: User Story 1 — Round-Trip Server-to-Client (Priority: P1) 🎯 MVP

**Goal**: A round-trip of (encrypt, decrypt) recovers the original plaintext byte-for-byte across plaintext sizes 1 byte, 1 KiB, and 1 MiB. This is the smallest end-to-end slice that proves the protocol exists; SDD-13 (server `/secrets/{name}` handler) and SDD-16 (`hush request` client) both sit on top of this contract.

**Independent Test**: Generate an ephemeral keypair via `generateFreshKey`. Build a plaintext byte slice. `Encrypt` against the public key, then `Decrypt` the resulting envelope against the matching private key. The returned `*SecureBytes` exposes contents byte-for-byte equal to the original plaintext via `Use(func(b []byte))`. Repeat across sizes 1, 1024, 1048576.

**Why first**: Round-trip is the load-bearing protocol property; every other user story (wrong-key failure, malformed-envelope rejection, ownership transfer, encrypt-zeroing) describes a *deviation* from this happy path. Implementing US1 first lets every later test reuse the same encrypt-then-decrypt fixture.

### Tests for User Story 1 (write FIRST, ensure FAIL before implementation) ⚠️

- [X] T008 [P] [US1] Write `TestECIES_RoundTrip_1B` in `internal/transport/ecies/ecies_test.go` — generate a fresh key via `generateFreshKey`, build a 1-byte plaintext (`[]byte{0x42}`), call `Encrypt(ctx, &priv.PublicKey, plaintext)`, then `Decrypt(ctx, priv, envelope)`; assert `nil` error, non-nil `*SecureBytes`, and that `sb.Use(func(b []byte) { require.True(t, bytes.Equal(plaintext, b)) })` succeeds
- [X] T009 [P] [US1] Write `TestECIES_RoundTrip_1KB` in `internal/transport/ecies/ecies_test.go` — same shape as T008 but with a 1024-byte plaintext (`bytes.Repeat([]byte{0xAB}, 1024)` or random bytes from `rand.Reader`)
- [X] T010 [P] [US1] Write `TestECIES_RoundTrip_1MB` in `internal/transport/ecies/ecies_test.go` — same shape as T008 but with a 1048576-byte plaintext (1 MiB)
- [X] T011 [P] [US1] Write `TestECIES_EncryptIsRandomised` in `internal/transport/ecies/ecies_test.go` per [data-model.md I-002](./data-model.md#7-invariants-testable-at-any-commit) — call `Encrypt` twice with the same `(pub, plaintext)`; assert the two envelopes are NOT byte-equal (fresh ephemeral keypair per call)
- [X] T012 [P] [US1] Write `TestECIES_EnvelopeMeetsMinSize` in `internal/transport/ecies/ecies_test.go` per [data-model.md I-003](./data-model.md#7-invariants-testable-at-any-commit) — for any successful `Encrypt` return, assert `len(envelope) >= 85`
- [X] T013 [P] [US1] Write `TestECIES_NoPlaintextSubstringInEnvelope` in `internal/transport/ecies/ecies_test.go` per spec User Story 1 acceptance scenario 3 — encrypt a recognisable plaintext (e.g., `"PLAINTEXT_MARKER_IN_ENVELOPE_TEST"`) and assert `bytes.Contains(envelope, plaintext) == false` (the BIE1 envelope shape encrypts the plaintext under AES-CBC, so a substring search MUST find nothing)

### Implementation for User Story 1

- [X] T014 [US1] Implement the file-private `compressPubKey(pub *ecdsa.PublicKey) ([]byte, error)` in `internal/transport/ecies/ecies.go` per [research R-004](./research.md#r-004--compressed-pubkey-codec-decred-secp256k1parsepubkey) — converts `*ecdsa.PublicKey` to `*secp256k1.PublicKey` via `secp256k1.NewPublicKey(x, y)` and returns the 33-byte compressed encoding via `SerializeCompressed()`. Returns `ErrECIESInvalidRecipientKey` on nil/wrong-curve input.
- [X] T015 [US1] Implement the file-private `parseCompressedPubKey(b []byte) (*ecdsa.PublicKey, error)` in `internal/transport/ecies/ecies.go` per [research R-004](./research.md#r-004--compressed-pubkey-codec-decred-secp256k1parsepubkey) — uses `secp256k1.ParsePubKey(b)`; on any failure returns `ErrECIESDecryptFailed`. Lifts the parsed `*secp256k1.PublicKey` to `*ecdsa.PublicKey` via field-value reads.
- [X] T016 [US1] Implement the file-private `ecdh(curve elliptic.Curve, peerX, peerY, scalar *big.Int) []byte` in `internal/transport/ecies/ecies.go` per [research R-003](./research.md#r-003--kdf-derivation-sha-512-of-shared-x-coordinate) — computes `Curve.ScalarMult(peerX, peerY, scalar.Bytes())`, returns the 32-byte fixed-width X coordinate via `sharedX.FillBytes(make([]byte, 32))` (NOT `Bytes()`)
- [X] T017 [US1] Implement the file-private `kdf(sharedX []byte) (iv, keyE, keyM []byte)` in `internal/transport/ecies/ecies.go` per [research R-003](./research.md#r-003--kdf-derivation-sha-512-of-shared-x-coordinate) — computes `H := sha512.Sum512(sharedX)` and returns `iv = H[0:16]`, `keyE = H[16:48]`, `keyM = H[48:64]`; the caller is responsible for `defer secureZero(H[:])`
- [X] T018 [US1] Implement the file-private `pkcs7Pad(plaintext []byte, blockSize int) []byte` and `pkcs7Unpad(padded []byte, blockSize int) ([]byte, error)` in `internal/transport/ecies/ecies.go` per RFC 5652 §6.3 — `pkcs7Unpad` returns `ErrECIESDecryptFailed` on any malformedness (last-byte > BlockSize, last-byte == 0, padding-bytes mismatch)
- [X] T019 [US1] Implement `Encrypt(ctx context.Context, recipientPub *ecdsa.PublicKey, plaintext []byte) ([]byte, error)` in `internal/transport/ecies/ecies.go` per [plan.md §Summary "Encrypt"](./plan.md) and [data-model.md §4.1](./data-model.md#41-encrypt-server-side-sdd-13-consumer): (1) `ctx.Err()` early-out, (2) reject nil/wrong-curve `recipientPub` with `ErrECIESInvalidRecipientKey`, (3) reject empty plaintext with `ErrECIESEmptyPlaintext`, (4) copy plaintext into `pt` with `defer secureZero(pt)`, (5) generate ephemeral keypair via `ecdsa.GenerateKey(recipientPub.Curve, rand.Reader)` with `defer secureZeroBigInt(ephPriv.D)`, (6) ECDH → sharedX with `defer secureZero(sharedX)`, (7) KDF with `defer secureZero(H[:])`, (8) PKCS#7-pad pt with `defer secureZero(padded)`, (9) AES-256-CBC encrypt, (10) compress ephPub, (11) HMAC-SHA256 over `magic ‖ ephPub ‖ ciphertext`, (12) return `magic ‖ ephPub ‖ ct ‖ mac`
- [X] T020 [US1] Implement `Decrypt(ctx context.Context, recipientPriv *ecdsa.PrivateKey, envelope []byte) (*securebytes.SecureBytes, error)` in `internal/transport/ecies/ecies.go` per [plan.md §Summary "Decrypt"](./plan.md) and [data-model.md §4.2](./data-model.md#42-decrypt-client-side-sdd-16-consumer): (1) `ctx.Err()` early-out, (2) length gate `len(envelope) >= 85` else `ErrECIESEnvelopeTooShort`, (3) magic gate `envelope[0:4] == "BIE1"` else `ErrECIESDecryptFailed`, (4) `parseCompressedPubKey(envelope[4:37])`, (5) ECDH → sharedX with `defer secureZero(sharedX)`, (6) KDF with `defer secureZero(H[:])`, (7) CT-shape gate `(ctEnd - 37) > 0 && (ctEnd - 37) % 16 == 0`, (8) `hmac.Equal` constant-time MAC verify, (9) AES-256-CBC decrypt into `plaintextBuf` with `defer secureZero(plaintextBuf)`, (10) `pkcs7Unpad`, (11) `securebytes.New(unpadded)` (constructor zeros source), (12) return the fresh `*SecureBytes`

**Checkpoint**: All US1 tests pass. `go test -run TestECIES_RoundTrip ./internal/transport/ecies/` is green; `go test -cover -run TestECIES_RoundTrip ./internal/transport/ecies/` shows the round-trip path fully covered.

---

## Phase 4: User Story 2 — Wrong-Key / Tampered-Envelope Failure (Priority: P1)

**Goal**: An on-path observer who captures an envelope cannot recover the plaintext. Decrypt with a non-matching private key, with a tampered envelope, or with a wrong-curve key returns `ErrECIESDecryptFailed` — the SAME sentinel for every cryptographic failure (FR-004 non-distinguishability). No portion of the plaintext appears in the returned error.

**Independent Test**: Generate two ephemeral keypairs A and B. Encrypt a plaintext to A's public key. Attempt Decrypt with B's private key. Assert `errors.Is(err, ErrECIESDecryptFailed)`. Repeat for various envelope mutations (single-byte flips at multiple positions, truncation by 1 byte, appended trailing byte). All produce the same sentinel.

**Dependencies on previous stories**: Phase 3 (US1) — Encrypt/Decrypt must exist and round-trip correctly before failure modes can be exercised.

### Tests for User Story 2 (write FIRST) ⚠️

- [X] T021 [P] [US2] Write `TestECIES_DecryptWrongKey_Fails` in `internal/transport/ecies/ecies_test.go` — generate two fresh keys A and B via `generateFreshKey`, encrypt to A's public, decrypt with B's private; assert `errors.Is(err, ErrECIESDecryptFailed)` and the returned `*SecureBytes` is nil
- [X] T022 [P] [US2] Write `TestECIES_DecryptMangledEnvelope_Fails` in `internal/transport/ecies/ecies_test.go` as a table-driven test with subtests for each mangle position per [research R-009](./research.md#r-009--sentinel-leak-test-testecies_noleakonerror): `flip-byte-in-magic` (position 0), `flip-byte-in-pubkey` (position 10), `flip-byte-in-ciphertext` (position 40), `flip-byte-in-mac` (position `len(envelope)-1`); each subtest uses `mangleByte` from T007, then asserts `errors.Is(err, ErrECIESDecryptFailed)` and a nil `*SecureBytes`
- [X] T023 [P] [US2] Write `TestECIES_DecryptTruncatedEnvelope_Fails` in `internal/transport/ecies/ecies_test.go` — encrypt a 100-byte plaintext, truncate the envelope to `len(envelope)-1` via `truncateEnvelope` from T007 (still ≥ 85 bytes); assert `errors.Is(err, ErrECIESDecryptFailed)`
- [X] T024 [P] [US2] Write `TestECIES_DecryptAppendedByte_Fails` in `internal/transport/ecies/ecies_test.go` per spec Edge Case "trailing byte" — encrypt, append one byte via `appendByte` from T007; assert `errors.Is(err, ErrECIESDecryptFailed)`

### Implementation for User Story 2

- [X] T025 [US2] **No new implementation** — the wrong-key, mangled, truncated, and appended-byte failure modes all flow through the cryptographic-failure paths already implemented in `Decrypt` (T020). The MAC gate and the CT-shape gate collapse every cryptographic failure into `ErrECIESDecryptFailed` per [research R-007](./research.md#r-007--sentinel-error-catalogue-and-static-error-messages). This task is the verification step: re-run the US2 tests added in T021–T024 and confirm they pass against the T020 implementation. If a test fails, the bug is in `Decrypt`, NOT in a new code path.

**Checkpoint**: All US2 tests pass. The package never distinguishes "wrong key" from "tampered envelope" — `errors.Is(err, ErrECIESDecryptFailed)` holds for every cryptographic failure.

---

## Phase 5: User Story 3 — Malformed / Short Envelope Rejection (Priority: P1)

**Goal**: A decrypt call with an envelope shorter than the documented minimum returns `ErrECIESEnvelopeTooShort` BEFORE any cryptographic primitive runs (FR-005). Random byte sequences supplied as the envelope produce typed errors and never panic, exhaust memory, or expose a partial plaintext (FR-012, fuzz target #3).

**Independent Test**: Invoke `Decrypt` with envelopes of length 0, length 1, and lengths up to but not including 85. For each, assert `errors.Is(err, ErrECIESEnvelopeTooShort)`. Run `FuzzECIESDecrypt` for ≥60 s; assert no panic and every error is one of the typed sentinels.

**Dependencies on previous stories**: Phase 3 (US1) — `Decrypt` must exist before its rejection paths can be tested. Phase 1 (T003) — fuzz seed corpus must be present.

### Tests for User Story 3 (write FIRST) ⚠️

- [X] T026 [P] [US3] Write `TestECIES_DecryptEmptyEnvelope_TooShort` in `internal/transport/ecies/ecies_test.go` as a table-driven test with subtests for envelope lengths 0, 1, and 84 — each subtest passes a synthetic envelope of the target length to `Decrypt`; assert `errors.Is(err, ErrECIESEnvelopeTooShort)` and a nil `*SecureBytes`. The subtests MUST also assert that the rejection is sentinel-identity-distinguishable from `ErrECIESDecryptFailed` (i.e., `errors.Is(err, ErrECIESDecryptFailed) == false` for the too-short cases)
- [X] T027 [P] [US3] Write `FuzzECIESDecrypt` in `internal/transport/ecies/decrypt_fuzz_test.go` per [research R-008](./research.md#r-008--fuzz-target-shape-fuzzeciesdecrypt) — fixed deterministic key from `generateFuzzKey` (T006); seeds the corpus from `testdata/fuzz/FuzzECIESDecrypt/` (T003) via `f.Add(...)` for each file; `f.Fuzz(func(t *testing.T, envelope []byte))` calls `Decrypt(t.Context(), priv, envelope)` and asserts: (a) no panic, (b) on error, exactly one of `errors.Is(err, ErrECIESDecryptFailed)`, `errors.Is(err, ErrECIESEnvelopeTooShort)`, `errors.Is(err, context.Canceled)`, or `errors.Is(err, context.DeadlineExceeded)` holds — never another type, (c) on the vanishingly-rare success path, defensively `Destroy()` the returned `*SecureBytes` and return

### Implementation for User Story 3

- [X] T028 [US3] **No new implementation** — the length-too-short rejection and the panic-free guarantee both flow through the gates already implemented in `Decrypt` (T020). The length gate at step 2 of `Decrypt` returns `ErrECIESEnvelopeTooShort` BEFORE any cryptographic primitive runs; subsequent gates return `ErrECIESDecryptFailed` for any cryptographic failure. This task is the verification step: re-run the US3 tests added in T026–T027 and confirm they pass against the T020 implementation. If `FuzzECIESDecrypt` finds a panic or a non-typed error, the bug is in `Decrypt`, NOT in a new code path.

**Checkpoint**: `TestECIES_DecryptEmptyEnvelope_TooShort` passes. A short, in-process fuzz run (`go test -fuzz=FuzzECIESDecrypt -fuzztime=10s ./internal/transport/ecies/`) is panic-free; the full 60 s gate is run in the Polish phase.

---

## Phase 6: User Story 4 — Decrypted Plaintext in Caller-Owned Memory (Priority: P1)

**Goal**: The `*SecureBytes` returned by `Decrypt` is a fresh, caller-owned handle. No copy of the plaintext lives in the package's memory after Decrypt returns. The package retains no parallel handle, no finalizer registration, no cached reference. After the caller `Destroy`s the SecureBytes, subsequent `Use` returns `securebytes.ErrDestroyed` — confirming ownership transfer.

**Independent Test**: Call `Decrypt` and capture the returned `*SecureBytes`. Inspect package-level state and assert no reference to the plaintext is retained. `Destroy` the returned SecureBytes and assert that subsequent `Use` returns `securebytes.ErrDestroyed`.

**Dependencies on previous stories**: Phase 3 (US1) — successful Decrypt is the precondition.

### Tests for User Story 4 (write FIRST) ⚠️

- [X] T029 [P] [US4] Write `TestECIES_DecryptReturnsSecureBytes` in `internal/transport/ecies/ecies_test.go` per spec User Story 4 acceptance scenarios — round-trip a plaintext, capture the returned `*SecureBytes`, call `sb.Use(func(b) { require.True(t, bytes.Equal(plaintext, b)) })` to verify byte-equal contents, then `sb.Destroy()`, then call `sb.Use(...)` again and assert it returns `securebytes.ErrDestroyed` (the package surrendered ownership; it kept no parallel handle)

### Implementation for User Story 4

- [X] T030 [US4] **No new implementation** — the ownership-transfer property flows from `securebytes.New(unpadded)` (which copies into mlocked memory and zeros the source per the SDD-02 contract) and the absence of any package-level cache or finalizer in the SDD-09 design. This task is the verification step: re-run T029 against the T020 implementation. If `Use`-after-`Destroy` does not return `securebytes.ErrDestroyed`, the bug is either in SDD-02 (out of scope) or in a parallel-handle leak from `Decrypt` (which would require the plaintextBuf-zeroing defer to be reordered).

**Checkpoint**: `TestECIES_DecryptReturnsSecureBytes` passes. The package holds no cached plaintext after Decrypt returns.

---

## Phase 7: User Story 5 — Encrypt Zeros Internal Buffers on Every Return Path (Priority: P1)

**Goal**: The encrypt operation copies the caller's input plaintext into an internal buffer. Before the encrypt call returns — on happy paths AND error paths — that internal buffer is overwritten with zero bytes (FR-008, SC-008). The caller's input slice is the caller's responsibility; the package's working copy is the package's responsibility.

**Independent Test**: Wrap the encrypt call with a hook that captures the package's internal buffer at the moment of return. Across happy paths and induced error paths, assert the captured buffer is all zeroes.

**Dependencies on previous stories**: Phase 3 (US1) — `Encrypt` must exist before its zeroization can be tested.

### Tests for User Story 5 (write FIRST) ⚠️

- [X] T031 [P] [US5] Write `TestECIES_EncryptZeroesInternalBuffersOnSuccess` in `internal/transport/ecies/ecies_test.go` — round-trip a plaintext via Encrypt+Decrypt, then assert that the caller's input slice was NOT mutated (the package zeros its OWN copy, not the caller's). This is the simpler half of the FR-008 contract: the caller-side observation is non-destructive.
- [X] T032 [P] [US5] Write `TestECIES_EncryptZeroesInternalBuffersOnError` in `internal/transport/ecies/ecies_test.go` — induce error paths (empty plaintext → `ErrECIESEmptyPlaintext`, nil pub → `ErrECIESInvalidRecipientKey`, wrong-curve pub → `ErrECIESInvalidRecipientKey`); for each, assert the caller's input slice was NOT mutated and that the returned envelope is nil. Note that for the empty-plaintext and invalid-pub paths the package allocates NO internal buffer (rejection fires before plaintext copy per [data-model.md §4.1](./data-model.md#41-encrypt-server-side-sdd-13-consumer)), so there is nothing to zero on those paths; the test asserts the *no-allocation* property by side-effect.
- [X] T033 [P] [US5] Write `TestECIES_EncryptDoesNotMutateCallerSlice` in `internal/transport/ecies/ecies_test.go` per spec User Story 5 acceptance scenario implicit constraint — capture a copy of the input plaintext, call `Encrypt`, then assert `bytes.Equal(originalCopy, plaintext)` (the package's contract is to zero only its OWN internal copy)

### Implementation for User Story 5

- [X] T034 [US5] **No new implementation** — the FR-008 zeroing discipline is implemented entirely via the `defer secureZero(pt)`, `defer secureZero(padded)`, `defer secureZero(sharedX)`, `defer secureZero(H[:])`, and `defer secureZeroBigInt(ephPriv.D)` registrations in `Encrypt` (T019). The caller's input slice is never touched (the package operates on its internal copy `pt`). This task is the verification step: re-run the US5 tests added in T031–T033. If a test fails, the bug is in `Encrypt`'s defer ordering.

**Checkpoint**: All US5 tests pass. The caller's input plaintext is untouched on every return path; the package's internal buffers are zeroed via deferred `secureZero` calls.

---

## Phase 8: User Story 6 — Sentinel-Identity Failure Categorisation (Priority: P2)

**Goal**: Every distinct rejection mode is reported as a distinct, named sentinel error. Callers identify the failure category by error identity, not by parsing a message string. The package exports four sentinels: `ErrECIESDecryptFailed`, `ErrECIESEnvelopeTooShort`, `ErrECIESEmptyPlaintext`, `ErrECIESInvalidRecipientKey` (FR-006). The decrypt-side wrong-key and tampered-envelope cases collapse into `ErrECIESDecryptFailed` by design (FR-004); the encrypt-side empty-plaintext and invalid-recipient-key cases are independent sentinels.

**Independent Test**: For each defined rejection category, build an input that triggers exactly that category. Assert the returned error is identifiable as the corresponding named sentinel via `errors.Is`. No rejection collapses into a generic "decrypt failed" string.

**Dependencies on previous stories**: Phase 1 (T002 — sentinel declarations); Phase 3 (US1 — `Encrypt`/`Decrypt` exist); the encrypt-side rejections are also covered by Phase 7 (US5).

### Tests for User Story 6 (write FIRST) ⚠️

- [X] T035 [P] [US6] Write `TestECIES_EncryptRejectsEmpty` in `internal/transport/ecies/ecies_test.go` per FR-015 / [data-model.md I-013](./data-model.md#7-invariants-testable-at-any-commit) — call `Encrypt(ctx, &priv.PublicKey, []byte{})`; assert `errors.Is(err, ErrECIESEmptyPlaintext)` and a nil envelope; assert the rejection is sentinel-identity-distinguishable from `ErrECIESInvalidRecipientKey` and `ErrECIESDecryptFailed`
- [X] T036 [P] [US6] Write `TestECIES_EncryptRejectsNilPub` in `internal/transport/ecies/ecies_test.go` per FR-015a — call `Encrypt(ctx, nil, plaintext)`; assert `errors.Is(err, ErrECIESInvalidRecipientKey)` and a nil envelope
- [X] T037 [P] [US6] Write `TestECIES_EncryptRejectsWrongCurvePub` in `internal/transport/ecies/ecies_test.go` per FR-015a — generate a P-256 key (`ecdsa.GenerateKey(elliptic.P256(), rand.Reader)`), call `Encrypt(ctx, &p256priv.PublicKey, plaintext)`; assert `errors.Is(err, ErrECIESInvalidRecipientKey)` (not `ErrECIESDecryptFailed`)

### Implementation for User Story 6

- [X] T038 [US6] **No new implementation** — the four sentinels are declared in T002; the rejection paths are wired in T019 (`Encrypt` rejections at steps 2–3) and T020 (`Decrypt` length and crypto-class gates). This task is the verification step: re-run T035–T037 and confirm sentinel identity holds for each rejection category.

**Checkpoint**: All US6 tests pass. Every rejection category is identifiable by `errors.Is` against the four exported sentinels.

---

## Phase 9: Cross-Cutting — Context Cancellation, Sentinel-Leak, Concurrency

**Purpose**: Properties that span ALL user stories — context cancellation handling (FR-013, SC-011), sentinel-leak absence in error messages (FR-007, SC-006, SC-010), and concurrent-call race-cleanliness (FR-011, Constitution IX). These tests exercise the public API end-to-end, not a single user story.

**Dependencies**: Phases 3–8 must be complete; the public API surface must be stable.

### Cross-cutting tests (write FIRST) ⚠️

- [X] T039 [P] Write `TestECIES_EncryptRespectsCancelledContext` in `internal/transport/ecies/ecies_test.go` per FR-013, SC-011 — pre-cancel the context, call `Encrypt(ctx, &priv.PublicKey, plaintext)`; assert `errors.Is(err, context.Canceled)` and that the package returns `ctx.Err()` verbatim (no package-private substitute)
- [X] T040 [P] Write `TestECIES_DecryptRespectsCancelledContext` in `internal/transport/ecies/ecies_test.go` per FR-013, SC-011 — pre-cancel the context, call `Decrypt(ctx, priv, envelope)`; assert `errors.Is(err, context.Canceled)`
- [X] T041 [P] Write `TestECIES_DecryptRespectsDeadlineContext` in `internal/transport/ecies/ecies_test.go` per FR-013, SC-011 — create a context with a deadline already in the past, call `Decrypt(ctx, priv, envelope)`; assert `errors.Is(err, context.DeadlineExceeded)`
- [X] T042 [P] Write `TestECIES_NoLeakOnError` in `internal/transport/ecies/ecies_test.go` per [research R-009](./research.md#r-009--sentinel-leak-test-testecies_noleakonerror) — encrypt a plaintext containing `testutil.SentinelSecret(9)` (= `"SECRET_SHOULD_NEVER_APPEAR_9"`) as a substring (e.g., `[]byte("prefix-" + sentinel + "-suffix")`); for each mangle case (flip-byte-in-magic, flip-byte-in-pubkey, flip-byte-in-ciphertext, flip-byte-in-mac, truncate-to-min-minus-1, truncate-to-zero), call `Decrypt(ctx, priv, mangled)`; assert via `testutil.AssertSentinelAbsent(t, sentinel, err.Error())` that the sentinel is absent from `err.Error()` AND from every wrapped-error string descended via repeated `errors.Unwrap`
- [X] T043 [P] Write `TestECIES_ConcurrentRoundTrip` in `internal/transport/ecies/ecies_test.go` per FR-011 / [data-model.md I-015](./data-model.md#7-invariants-testable-at-any-commit) — spawn N=64 goroutines, each performing a fresh round-trip with its own freshly-generated keypair (via `generateFreshKey`) and its own random plaintext; the test runs under `magex test:race` (CI gate); assert no race-detector reports and every goroutine's round-trip succeeds with byte-equal plaintext recovery

### Implementation for cross-cutting

- [X] T044 **No new implementation** — context cancellation is honored via the `ctx.Err()` early-out at step 1 of both `Encrypt` (T019) and `Decrypt` (T020). The sentinel-leak property holds by construction (R-007: static error messages, no `fmt.Errorf` calls). Concurrency safety holds by construction (R-012: zero package-level mutable state). This task is the verification step: re-run T039–T043 and confirm all properties hold.

**Checkpoint**: Cross-cutting tests pass. The package honors `ctx.Err()` verbatim, never leaks the sentinel marker, and is race-clean under N-goroutine concurrent invocation.

---

## Phase 10: Polish & Final Gates

**Purpose**: Run the constitutional gates (`magex format:fix`, `magex lint`, `magex test:race`), the 60 s fuzz gate, the 100% coverage check, and update the cross-cutting documentation (PACKAGE-MAP, AC-MATRIX, SDD-PLAYBOOK).

**Dependencies**: All phases 1–9 complete; the package builds, all unit tests pass, the fuzz target compiles.

### Gates and verification

- [X] T045 Run `magex format:fix` from repo root and assert no diff lingers (or commit the formatter's output)
- [X] T046 Run `magex lint` from repo root and assert clean (no new lint findings on `internal/transport/ecies/`)
- [X] T047 Run `magex test:race` from repo root and assert clean — `TestECIES_ConcurrentRoundTrip` (T043) under `-race` is the load-bearing concurrency assertion
- [X] T048 Run `go test -fuzz=FuzzECIESDecrypt -fuzztime=60s ./internal/transport/ecies/` and assert: no panic, no new corpus rows in `testdata/fuzz/FuzzECIESDecrypt/` representing crashes, every error returned by `Decrypt` is one of the four typed values (ErrECIESDecryptFailed, ErrECIESEnvelopeTooShort, context.Canceled, context.DeadlineExceeded)
- [X] T049 Run `go test -cover ./internal/transport/ecies/` and assert coverage ≥ 100% per Constitution VIII Critical band; if any line is uncovered, add a test (or remove the unreachable code) before the implement commit lands

### Documentation updates

- [X] T050 [P] Append the "Exported API — locked at SDD-09" section to `docs/PACKAGE-MAP.md` under `internal/transport/` listing the four sentinels and the two functions per [contracts/api.md](./contracts/api.md) — same locality refinement SDD-08 made under `internal/transport/sign`
- [X] T051 [P] Update `docs/AC-MATRIX.md` AC-7 row (Layer 3 — wire-level secret-response encryption) with the new test file paths (`internal/transport/ecies/ecies_test.go`, `internal/transport/ecies/decrypt_fuzz_test.go`) and the named tests (`TestECIES_RoundTrip_*`, `TestECIES_DecryptWrongKey_Fails`, `TestECIES_DecryptMangledEnvelope_Fails`, `TestECIES_DecryptEmptyEnvelope_TooShort`, `TestECIES_DecryptReturnsSecureBytes`, `TestECIES_NoLeakOnError`, `FuzzECIESDecrypt`)
- [X] T052 [P] Mark SDD-09 status `done` in `docs/SDD-PLAYBOOK.md`

### Combined commit

- [X] T053 Run the combined commit per [SDD-09 Prompt 5](../../docs/sdd/SDD-09.md#prompt-5--implement--fresh-session): `git add internal/transport/ecies/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md specs/009-transport-ecies/tasks.md` then `git commit -m "feat(transport/ecies): ECIES encrypt/decrypt of secret responses (SDD-09)"`. Confirm in the final message: gates passed, fuzz 60 s clean, coverage = 100%, round-trip across 1B/1KB/1MB sizes, sentinel-leak absent from `err.Error()`, AC-7 row updated, SDD-PLAYBOOK updated, combined commit created.

**Checkpoint**: SDD-09 lands. AC-7 has named tests; the BIE1 envelope shape is locked in `docs/PACKAGE-MAP.md`; SDD-13 and SDD-16 can begin consuming `Encrypt` / `Decrypt`.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately.
- **Foundational (Phase 2)**: Depends on Setup completion — BLOCKS all user stories. T004 must precede T005 (constants land before helpers); T006 and T007 can run in parallel after T004.
- **User Story 1 (Phase 3)**: Depends on Foundational completion. Tests T008–T013 run in parallel; implementation T014–T020 is sequential within `ecies.go` (helpers before `Encrypt`/`Decrypt`).
- **User Stories 2–6 (Phases 4–8)**: All depend on User Story 1 completion (Encrypt/Decrypt must exist). Within each phase, tests run in parallel; implementation is mostly verification (US2/US3/US4/US5/US6 add no new code paths beyond US1's T019/T020).
- **Cross-cutting (Phase 9)**: Depends on Phases 3–8 completion (the public API must be stable).
- **Polish (Phase 10)**: Depends on all preceding phases. T045–T049 are sequential gates; T050–T052 run in parallel; T053 is the final combined commit.

### User Story Dependencies

- **User Story 1 (P1)**: Foundational complete. No dependencies on other stories.
- **User Story 2 (P1)**: User Story 1 complete (Encrypt/Decrypt exist).
- **User Story 3 (P1)**: User Story 1 complete; Phase 1 fuzz seed corpus present.
- **User Story 4 (P1)**: User Story 1 complete (successful Decrypt is the precondition).
- **User Story 5 (P1)**: User Story 1 complete (Encrypt exists).
- **User Story 6 (P2)**: Phase 1 sentinel declarations complete; User Story 1 complete.

### Within Each User Story

- Tests (T0NN [P] [USx] Write …) MUST be written and MUST FAIL before implementation tasks (T0MM [USx] Implement …) — TDD-mandatory per Constitution VIII.
- Helpers (compressPubKey, parseCompressedPubKey, ecdh, kdf, pkcs7Pad/Unpad) before `Encrypt`/`Decrypt`.
- `Encrypt` (T019) before `Decrypt` (T020) within the same file (Encrypt establishes the envelope shape that Decrypt parses).

### Parallel Opportunities

- All Setup tasks marked [P] (T002, T003) can run in parallel (different files).
- Foundational T005, T006, T007 can run in parallel after T004.
- All US1 tests T008–T013 can run in parallel (same file, different test functions).
- US2, US3, US4, US5, US6 phases are mutually independent after Phase 3 — different developers can pick up T021–T024, T026–T027, T029, T031–T033, T035–T037 in parallel.
- Cross-cutting tests T039–T043 can all run in parallel.
- Polish documentation tasks T050–T052 can run in parallel.

---

## Parallel Example: User Story 1

```bash
# Launch all US1 tests together (different test functions in the same file):
Task: "Write TestECIES_RoundTrip_1B in internal/transport/ecies/ecies_test.go"
Task: "Write TestECIES_RoundTrip_1KB in internal/transport/ecies/ecies_test.go"
Task: "Write TestECIES_RoundTrip_1MB in internal/transport/ecies/ecies_test.go"
Task: "Write TestECIES_EncryptIsRandomised in internal/transport/ecies/ecies_test.go"
Task: "Write TestECIES_EnvelopeMeetsMinSize in internal/transport/ecies/ecies_test.go"
Task: "Write TestECIES_NoPlaintextSubstringInEnvelope in internal/transport/ecies/ecies_test.go"
```

After all US1 tests are written and failing, implement helpers and Encrypt/Decrypt sequentially:

```bash
# Sequential within ecies.go:
T014 → T015 → T016 → T017 → T018 → T019 → T020
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001–T003).
2. Complete Phase 2: Foundational (T004–T007) — CRITICAL, blocks all stories.
3. Complete Phase 3: User Story 1 (T008–T020).
4. **STOP and VALIDATE**: Run `go test ./internal/transport/ecies/` and confirm all US1 tests pass; round-trip works across 1B/1KB/1MB.
5. The MVP is a working ECIES round-trip; SDD-13 and SDD-16 could in principle consume it (modulo the failure-mode contract still being unverified).

### Incremental Delivery

1. Setup + Foundational → Foundation ready.
2. US1 → Test independently → Round-trip works (MVP!).
3. US2 → Test independently → Wrong-key / tampered-envelope rejection verified.
4. US3 → Test independently → Malformed-envelope rejection verified; fuzz target compiles.
5. US4 → Test independently → Ownership transfer verified.
6. US5 → Test independently → Encrypt zeroing verified.
7. US6 → Test independently → Sentinel identity verified.
8. Cross-cutting (Phase 9) → Context cancellation, sentinel-leak, concurrency verified.
9. Polish (Phase 10) → Gates, fuzz, coverage, docs, commit.

### Parallel Team Strategy

With multiple developers:

1. Team completes Setup + Foundational together.
2. Once Foundational is done:
   - Developer A: User Story 1 (the load-bearing implementation).
   - After US1 lands: Developers B/C/D/E pick up US2, US3, US4, US5, US6 in parallel — each story is mostly tests + verification (T025, T028, T030, T034, T038 are no-new-code tasks because US1 already wired the paths).
3. Cross-cutting (Phase 9) requires the full public API; one developer runs it after all user stories merge.
4. Polish (Phase 10) is a single developer's commit-and-ship pass.

---

## Notes

- [P] tasks = different files (or different test functions in the same file with no shared state) and no dependencies on incomplete tasks.
- [Story] label maps task to specific user story for traceability; setup/foundational/cross-cutting/polish carry no story label.
- TDD-MANDATORY: every behaviour-contract task has a paired test-writing task scheduled BEFORE it. Tests MUST be written first and MUST FAIL before the implementation task is run.
- Coverage target: 100% (Constitution VIII Critical band — ECIES wire encryption).
- Fuzz target: `FuzzECIESDecrypt` ≥ 60 s clean (Constitution VIII fuzz target #3).
- All commits for this chunk are deferred to a single combined commit at the end of Phase 10 (T053). Do not commit between phases.
- Avoid: vague tasks, same-file conflicts, cross-story dependencies that break independence.
