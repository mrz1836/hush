---
description: "Task list for SDD-01 internal/keys (Argon2id + BIP32 HD derivation)"
---

# Tasks: Hush Key Hierarchy Derivation (SDD-01)

**Input**: Design documents from `/specs/001-keys-derivation/`
**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/keys-api.md](./contracts/keys-api.md), [quickstart.md](./quickstart.md), chunk contract [docs/sdd/SDD-01.md](../../docs/sdd/SDD-01.md)

**Tests**: TDD-mandatory per Constitution VIII. Every behaviour contract listed in `contracts/keys-api.md` (G1–G12) gets a failing test task BEFORE the implementation task that satisfies it. Coverage target: **100%**. The negative-space contract from `contracts/keys-api.md` is enforced in the polish phase.

**Organization**: Tasks are grouped by user story (US1–US4 from `spec.md`) so each story can be implemented and validated independently. Story priority order P1 → P1 → P1 → P2 follows `spec.md`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files / different test functions, no dependencies on incomplete tasks)
- **[Story]**: Maps the task to its user story (US1–US4). Setup, Foundational, and Polish tasks have no story label.
- All file paths are repository-relative.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Land the package directory and confirm the locked crypto dependencies are present. No new crypto deps may be added (Constitution XI).

- [X] T001 Create package directory `internal/keys/` at the repository root (no files yet — those are created in Phase 2).
- [X] T002 Verify `go.mod` already pins `golang.org/x/crypto` (for `argon2`) and `github.com/bitcoinschema/go-bitcoin/v2` (for secp256k1 + BIP32). Run `go mod why golang.org/x/crypto` and `go mod why github.com/bitcoinschema/go-bitcoin/v2`. Add them via `go get` ONLY if absent. Do NOT introduce any other crypto dependency (Constitution XI prohibition).

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Declare the package-level constants and sentinel errors that every test file and every implementation file references. These are pure declarations — function bodies are stubbed to `panic("not implemented")` so tests can compile and FAIL meaningfully (TDD: red → green).

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T003 [P] Create [internal/keys/derive.go](internal/keys/derive.go) with: `package keys` declaration; Argon2id constants `argon2Time = 4`, `argon2MemoryK = 256 * 1024`, `argon2Threads = 4`, `argon2KeyLen = 64`; validation constants `minPassphraseLen = 12`, `saltLen = 16`; sentinel errors `ErrPassphraseTooShort = errors.New("hush/keys: passphrase too short")` and `ErrSaltMissing = errors.New("hush/keys: salt missing or wrong length")`; stub signature `func DeriveMasterSeed(ctx context.Context, passphrase, salt []byte) ([]byte, error) { panic("not implemented") }`.
- [X] T004 [P] Create [internal/keys/paths.go](internal/keys/paths.go) with: `package keys` declaration; BIP32 path constants `bip44Purpose = 44`, `hushCoinType = 7743`, `pathJWTSigning = "m/44'/7743'/0'"`, `pathVaultEnc = "m/44'/7743'/1'"`, `pathAuditSigning = "m/44'/7743'/2'"`, `pathClientKeyBase = "m/44'/7743'/3'"`; stub signatures `DeriveJWTSigningKey(seed []byte) (*ecdsa.PrivateKey, error)`, `DeriveVaultEncKey(seed []byte) ([]byte, error)`, `DeriveAuditSigningKey(seed []byte) (*ecdsa.PrivateKey, error)` — all stubs `panic("not implemented")`.
- [X] T005 [P] Create [internal/keys/client.go](internal/keys/client.go) with: `package keys` declaration; stub signature `func DeriveClientKey(seed []byte, machineIndex uint32) (*ecdsa.PrivateKey, error) { panic("not implemented") }`.
- [X] T006 [P] Create [internal/keys/fingerprint.go](internal/keys/fingerprint.go) with: `package keys` declaration; stub signature `func PublicKeyFingerprint(pub *ecdsa.PublicKey) string { panic("not implemented") }`.

**Checkpoint**: `go build ./internal/keys/` succeeds; `go vet ./internal/keys/` is clean; no behaviour implemented yet.

---

## Phase 3: User Story 1 — Bootstrap a fresh key hierarchy (Priority: P1) 🎯 MVP

**Goal**: Deterministically derive the 64-byte master seed and the three primary sub-keys (JWT signing, vault encryption, audit signing) from a passphrase + salt, matching `quickstart.md` §1.

**Independent Test**: Provide a known-good passphrase (≥ 12B) + 16B salt; assert a 64B seed is returned with `ctx.Background()`; assert the three sub-key derivations each return the documented type/length and that JWT vs audit keys are distinct.

### Tests for User Story 1 (write FIRST — must FAIL before any implementation in this phase)

- [X] T007 [P] [US1] Add `TestDeriveMasterSeed_Deterministic` to [internal/keys/derive_test.go](internal/keys/derive_test.go): table-driven; for each `(passphrase, salt)` pair, two re-derivations with `context.Background()` MUST yield byte-identical 64-byte seeds; include a known-answer-vector (KAT) entry that pins one fixed `(passphrase, salt) → seed_hex` to guard cross-architecture determinism per research §B6 (G1 + FR-002 + SC-003).
- [X] T008 [P] [US1] Add `TestDeriveJWTSigningKey_Path` to [internal/keys/paths_test.go](internal/keys/paths_test.go): derive a seed, derive the JWT key, assert the returned `*ecdsa.PrivateKey` (a) is on the secp256k1 curve, (b) its scalar matches go-bitcoin/v2's BIP32 derivation at `m/44'/7743'/0'`, (c) is byte-identical across two re-derivations from the same seed (G5 + FR-005).
- [X] T009 [P] [US1] Add `TestDeriveVaultEncKey_Length` to [internal/keys/paths_test.go](internal/keys/paths_test.go): assert the returned `[]byte` length is exactly 32 and equals the 32-byte private scalar of the BIP32 child node at `m/44'/7743'/1'`; assert determinism on re-derivation (G6 + FR-006).
- [X] T010 [P] [US1] Add `TestDeriveAuditSigningKey_Path` to [internal/keys/paths_test.go](internal/keys/paths_test.go): assert secp256k1 key derived at `m/44'/7743'/2'`, deterministic, and the audit private scalar is DISTINCT from the JWT private scalar derived at `m/44'/7743'/0'` (G7 + FR-007 + AC-7 distinctness).
- [X] T011 [P] [US1] Add background-ctx subcase to `TestDeriveMasterSeed_Deterministic` in [internal/keys/derive_test.go](internal/keys/derive_test.go) covering the happy-path ctx contract: a non-cancelled `context.Background()` does NOT abort the derivation; verify by asserting a successful 64B return for a valid `(passphrase, salt)` pair (G4 happy-path subcase + FR-013).

### Implementation for User Story 1

- [X] T012 [US1] Implement `DeriveMasterSeed` in [internal/keys/derive.go](internal/keys/derive.go) per research §B4: step 1 `if err := ctx.Err(); err != nil { return nil, err }`; step 2 `if len(passphrase) < minPassphraseLen { return nil, ErrPassphraseTooShort }`; step 3 `if len(salt) != saltLen { return nil, ErrSaltMissing }`; step 4 `seed := argon2.IDKey(passphrase, salt, argon2Time, argon2MemoryK, argon2Threads, argon2KeyLen)`; step 5 `return seed, nil`. No logging. No `string(passphrase)` / `string(salt)` / `string(seed)` conversion (Constitution X).
- [X] T013 [US1] Implement unexported helper `scalarToECDSAKey(scalar []byte) (*ecdsa.PrivateKey, error)` in [internal/keys/paths.go](internal/keys/paths.go) per research §B1: take the 32-byte BIP32 child scalar, set `D = new(big.Int).SetBytes(scalar)`, derive `PublicKey.X / Y` via `curve.ScalarBaseMult(scalar)` using go-bitcoin/v2's secp256k1 curve, populate `PublicKey.Curve` with the same curve instance; return the resulting `*ecdsa.PrivateKey`. Reused by US2 (T015).
- [X] T014 [US1] Implement `DeriveJWTSigningKey`, `DeriveVaultEncKey`, `DeriveAuditSigningKey` in [internal/keys/paths.go](internal/keys/paths.go): build a go-bitcoin/v2 BIP32 master from `seed`, walk hardened path `m/44'/7743'/{0,1,2}'` for each, then for `0'` and `2'` call `scalarToECDSAKey` (T013); for `1'` return the 32-byte child scalar directly as `[]byte` per research §B2.

**Checkpoint**: T007–T011 GREEN; the bootstrap flow from [quickstart.md](./quickstart.md) §1 works end-to-end.

---

## Phase 4: User Story 2 — Per-machine client keys: machine-index isolation (Priority: P1)

**Goal**: Distinct machine indexes yield distinct keypairs from the same seed; distinct seeds yield distinct keypairs at the same index; the full `uint32` range works without truncation.

**Independent Test**: Derive client keys for indexes 0, 1, 2 from one seed; re-derive index 0 from a different seed; assert all four keypairs distinct. Re-derive index 0 a third time from the original seed; assert determinism. Derive index `math.MaxUint32`; assert it succeeds.

### Tests for User Story 2 (write FIRST — must FAIL before implementation)

- [X] T015 [P] [US2] Add `TestDeriveClientKey_MachineIndexIsolation` to [internal/keys/client_test.go](internal/keys/client_test.go) with subtests: (a) distinct indexes from the SAME seed → distinct private scalars, (b) the SAME index from distinct seeds → distinct private scalars, (c) the same `(seed, machineIndex)` re-derived → byte-identical scalar, (d) `machineIndex = 0` and `machineIndex = math.MaxUint32` both succeed and produce distinct valid keypairs (no silent wrap or truncation), (e) the public scalar lies on secp256k1 (G8 + FR-008 + spec edge-case "Maximum machine index" + AC-6).

### Implementation for User Story 2

- [X] T016 [US2] Implement `DeriveClientKey` in [internal/keys/client.go](internal/keys/client.go): build a go-bitcoin/v2 BIP32 master from `seed`, walk hardened `m/44'/7743'/3'`, then derive the NON-hardened child at index `machineIndex` (so the full `uint32` space is valid; hardened index would cap at `2^31 - 1`), pass the resulting child scalar through `scalarToECDSAKey` (T013), return the `*ecdsa.PrivateKey`.

**Checkpoint**: T015 GREEN; the per-machine registration flow from [quickstart.md](./quickstart.md) §2 works.

---

## Phase 5: User Story 3 — Input validation: hard-fail on weak or malformed inputs (Priority: P1)

**Goal**: Short passphrase and wrong-length salt return distinct sentinel errors BEFORE Argon2id runs; a pre-cancelled `ctx` returns its `ctx.Err()` BEFORE Argon2id runs; latency for each rejection path is `< 100ms`.

**Independent Test**: Call `DeriveMasterSeed` with (a) passphrases of length 0, 1, 11, (b) salts of length 0, 8, 15, 17, 24, 32, (c) a pre-cancelled `context.Context`. Assert each call returns its sentinel error within 100ms; assert the 12-byte / 16-byte boundary inputs SUCCEED.

**Note on TDD**: The validation behaviour is implemented inside `DeriveMasterSeed` (T012, US1) — there is no separate implementation file for US3. The tests below LOCK that behaviour as an independent contract. They MUST be authored and FAILING (against the Phase 2 stub from T003) BEFORE T012 lands; T012 is the implementation that turns them GREEN.

### Tests for User Story 3 (write FIRST — must FAIL before T012 implementation lands)

- [X] T017 [P] [US3] Add `TestDeriveMasterSeed_RejectsShortPassphrase` to [internal/keys/derive_test.go](internal/keys/derive_test.go): table-driven over passphrase lengths `{0, 1, 11}` plus a `salt` of valid length 16; each row asserts `errors.Is(err, ErrPassphraseTooShort)`, `seed == nil`, and `time.Since(start) < 100*time.Millisecond` (proves Argon2id was NOT invoked); include a 12-byte boundary subcase that MUST succeed (G2 + FR-003 + SC-004).
- [X] T018 [P] [US3] Add `TestDeriveMasterSeed_RejectsBadSalt` to [internal/keys/derive_test.go](internal/keys/derive_test.go): table-driven over salt lengths `{0, 8, 15, 17, 24, 32}` plus a passphrase of valid length 12; each row asserts `errors.Is(err, ErrSaltMissing)`, `seed == nil`, and `time.Since(start) < 100*time.Millisecond`; include a 16-byte boundary subcase that MUST succeed (G3 + FR-004 + SC-004).
- [X] T019 [P] [US3] Add `TestDeriveMasterSeed_RespectsCancelledContext` to [internal/keys/derive_test.go](internal/keys/derive_test.go): construct `ctx, cancel := context.WithCancel(context.Background()); cancel()` BEFORE calling `DeriveMasterSeed`; assert `errors.Is(err, context.Canceled)`, `seed == nil`, and `time.Since(start) < 100*time.Millisecond` (proves the validation order from research §B4: ctx is checked at entry, before any Argon2id work) (G4 cancellation subcase + FR-013).

**Checkpoint**: T017–T019 GREEN once T012 lands; the validation paths from [quickstart.md](./quickstart.md) §4 work; SC-004 latency cap satisfied.

---

## Phase 6: User Story 4 — Public-key fingerprint for client registration UX (Priority: P2)

**Goal**: A 16-character lowercase hex fingerprint that is stable for a given public key and distinct across distinct keys, used by `hush init --client` registration UX.

**Independent Test**: Compute the fingerprint twice for the same public key (assert identical, length 16, lowercase hex); compute for two distinct public keys (assert different); pin one known-answer vector.

### Tests for User Story 4 (write FIRST — must FAIL before implementation)

- [X] T020 [P] [US4] Add `TestPublicKeyFingerprint_Stable` to [internal/keys/fingerprint_test.go](internal/keys/fingerprint_test.go) with subtests: (a) two invocations on the same `*ecdsa.PublicKey` return byte-identical strings, (b) length is exactly 16, (c) matches regex `^[0-9a-f]{16}$` (lowercase hex only), (d) two distinct public keys (e.g. JWT key vs audit key derived in T014) produce DIFFERENT fingerprints, (e) one KAT subcase pins a fixed public key (constructed via `scalarToECDSAKey` over a hard-coded 32-byte scalar) to a fixed fingerprint hex string (G9 + FR-009 + SC-006).

### Implementation for User Story 4

- [X] T021 [US4] Implement `PublicKeyFingerprint` in [internal/keys/fingerprint.go](internal/keys/fingerprint.go) per research §B3: SEC1-compress the `*ecdsa.PublicKey` (33 bytes: prefix `0x02` if `pub.Y` is even, `0x03` if odd, followed by `pub.X` left-padded to 32 bytes), `digest := sha256.Sum256(compressed)`, return `hex.EncodeToString(digest[:8])`. Result: 16 lowercase hex chars.

**Checkpoint**: T020 GREEN; the registration UX from [quickstart.md](./quickstart.md) §2 returns a 16-hex fingerprint.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Cross-story tests, the negative-space contract from [contracts/keys-api.md](./contracts/keys-api.md), and the four mandatory final-phase gates.

- [X] T022 [P] Add `FuzzDeriveMaster` to [internal/keys/derive_fuzz_test.go](internal/keys/derive_fuzz_test.go): seed corpus with mixed valid `(passphrase ≥ 12B, salt = 16B)` and invalid pairs; the fuzz body MUST (i) call `DeriveMasterSeed` with `context.Background()` and recover no panic, (ii) for valid pairs assert `len(seed) == 64` AND a second invocation with the same inputs yields a byte-identical seed (deterministic re-derivation in the same iteration), (iii) for invalid pairs assert the returned error is `ErrPassphraseTooShort` or `ErrSaltMissing` (not a panic, not a different error) (G10 + AC-9 + SC-002).
- [X] T023 Negative-space verification per [contracts/keys-api.md](./contracts/keys-api.md): from repo root, run and confirm each command returns NO matches (or, for `go list -deps`, returns ONLY stdlib + `golang.org/x/crypto/...` + `github.com/bitcoinschema/go-bitcoin/v2/...`):
  - `grep -nE 'log/slog|logging' internal/keys/*.go`
  - `grep -n 'math/rand' internal/keys/*.go`
  - `grep -nE 'string\([^)]*passphrase|string\(seed|string\(salt' internal/keys/*.go`
  - `grep -nE '^func init\(\)' internal/keys/*.go`
  - `grep -nE 'github.com/mrz1836/hush/internal/' internal/keys/*.go` (leaf-package rule)
  - `go list -deps ./internal/keys/`
- [X] T024 Run `magex format:fix` from repo root; stage any whitespace/import-ordering fixes it produces.
- [X] T025 Run `magex lint` from repo root; resolve every finding for `./internal/keys/...`. NO `//nolint:` waivers without an SDD amendment; in particular `gochecknoglobals` MUST stay clean (sentinel errors are exempt by lint config).
- [X] T026 Run `magex test:race` from repo root; the race detector MUST be clean for `./internal/keys/` with no data-race reports (G11 + Constitution VIII).
- [X] T027 Run `go test -fuzz=FuzzDeriveMaster -fuzztime=60s ./internal/keys/` from repo root; require ZERO panics, ZERO crashes, ZERO new entries written under `internal/keys/testdata/fuzz/FuzzDeriveMaster/` (G10 + AC-9 + SC-002).
- [X] T028 Run `go test -cover ./internal/keys/` from repo root; require coverage report `coverage: 100.0% of statements` exactly (G12 + Constitution VIII; codecov gate for security-critical packages).

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies.
- **Foundational (Phase 2)**: Depends on Setup. BLOCKS all user stories — every test file imports the constants and sentinel errors from Phase 2.
- **User Stories (Phases 3–6)**: All depend on Foundational. Within TDD, tests for a given story MUST be authored and FAILING before any implementation task in that story begins.
- **Polish (Phase 7)**: Depends on every user-story phase being complete (Phases 3–6 all GREEN).

### Cross-story task dependencies

- T013 (BIP32-scalar-to-ECDSA helper, in US1) is reused by T016 (US2) and T020 KAT (US4). Land T013 before T016 / T020 KAT subcase.
- T012 (DeriveMasterSeed implementation, in US1) is what turns T017–T019 (US3 tests) GREEN. Author US3 tests against the Phase 2 stub from T003 (red), then T012 lands and turns them GREEN. There is NO separate impl task for US3 — the validation order lives inside `DeriveMasterSeed`.
- T021 (Fingerprint impl, US4) is independent of US1/US2 impl in the function-signature sense, but the US4 KAT subcase in T020 uses `scalarToECDSAKey` (T013) to construct its fixed public key, so T013 must land first.

### Within Each User Story

- Tests authored and FAILING before implementation (Constitution VIII).
- Constants / errors / stubs (Phase 2) before any test, so test files compile.
- Story complete before moving to the next priority.

### Parallel Opportunities

- All Phase 2 file-creation tasks (T003–T006) are parallel — separate files.
- All Phase 3 test-writing tasks (T007–T011) are parallel — separate test functions across `derive_test.go` / `paths_test.go`.
- All Phase 4 / 5 / 6 test-writing tasks (T015, T017–T019, T020) are parallel within their phase — separate test functions.
- US2 impl (T016), US3 (no separate impl), and US4 impl (T021) can be parallel after T012 + T013 land — they touch separate files (`client.go`, `derive.go`, `fingerprint.go`).
- T022 (fuzz target) is parallel with T023 (negative-space grep audit) — different files.
- T024–T028 are SERIAL — each gate must pass before the next runs.

---

## Parallel Example: User Story 1 tests

```bash
# After Phase 2 lands, author all US1 tests in parallel (TDD red phase):
Task: "T007 [P] [US1] Add TestDeriveMasterSeed_Deterministic to internal/keys/derive_test.go"
Task: "T008 [P] [US1] Add TestDeriveJWTSigningKey_Path to internal/keys/paths_test.go"
Task: "T009 [P] [US1] Add TestDeriveVaultEncKey_Length to internal/keys/paths_test.go"
Task: "T010 [P] [US1] Add TestDeriveAuditSigningKey_Path to internal/keys/paths_test.go"
Task: "T011 [P] [US1] Add background-ctx subcase to TestDeriveMasterSeed_Deterministic"

# Verify red — all five must FAIL against the Phase 2 stubs:
go test ./internal/keys/   # expect: panic "not implemented" from each test

# Then land T012 → T013 → T014 in order (T013 is reused by T014).
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Phase 1 + Phase 2 (foundation: directory, constants, sentinel errors, stubs).
2. Phase 3 (US1 — bootstrap). Tests RED → T012/T013/T014 land → tests GREEN.
3. **STOP and VALIDATE**: the bootstrap flow from [quickstart.md](./quickstart.md) §1 works.

### Incremental Delivery

1. Foundation (Phase 1 + 2).
2. + US1 (bootstrap = MVP — vault/JWT/audit consumers can wire up against this).
3. + US2 (per-machine client keys — multi-host registration unblocked; SDD-08 / SDD-15 client flow ready).
4. + US3 (validation tests lock the failure modes that T012 already implements; this phase is "lock the contract" rather than "ship behaviour").
5. + US4 (fingerprint — completes the registration UX consumed by SDD-15 / SDD-16).
6. Polish: fuzz target + negative-space audit + the four magex/go gates.

### Parallel Team Strategy

With multiple developers (after Phase 2 lands):

1. Developer A owns T007 + T011 (US1 derive tests) and T012 (DeriveMasterSeed impl).
2. Developer B owns T008–T010 (US1 paths tests) and T013 + T014 (helper + sub-keys impl).
3. Developer C owns T015 (US2 test) and T016 (US2 impl) — starts after T013 lands.
4. Developer D owns T017–T019 (US3 tests) — runs in parallel with A/B; turns GREEN once T012 lands.
5. Developer E owns T020 (US4 test) and T021 (US4 impl) — starts after T013 lands.
6. Polish phase serial: one developer runs T022 → T023 → T024 → T025 → T026 → T027 → T028 and reports back.

---

## Notes

- **TDD discipline (Constitution VIII)**: Every test task in this list MUST land its test in a FAILING state before the matching implementation task begins. Verify red explicitly with `go test ./internal/keys/` before writing impl.
- **Coverage 100%**: Non-negotiable. T028 enforces it. If a branch is uncovered, add a test — do NOT delete the branch.
- **No `[]byte → string` of secret material** anywhere in this package (Constitution X). Verified by T023 grep.
- **No new crypto deps**: Constitution XI requires an SDD amendment for any addition beyond `golang.org/x/crypto` and `github.com/bitcoinschema/go-bitcoin/v2`. T002 verifies; T023 `go list -deps` re-verifies.
- **Fuzz duration**: T027 requires 60s minimum (AC-9 floor). CI nightly may run longer; the chunk gate is 60s clean.
- **Leaf-package rule**: NO imports from `github.com/mrz1836/hush/internal/...`. Verified by T023 grep.
- **Race detector**: T026 runs the project-wide `magex test:race` task; the relevant slice is `./internal/keys/` but the gate runs the full repo to avoid false-clean from un-instrumented code.
- **Sentinel errors are exported `var`s** (Constitution IX). The package's `gochecknoglobals` lint config exempts `Err*` vars — verified clean by T025.
- **Commit cadence**: One commit per task or per logical group (e.g. all US1 tests, then T012, then T013, then T014). Do NOT bundle red and green commits together — the failing-test commit is the TDD evidence trail.
