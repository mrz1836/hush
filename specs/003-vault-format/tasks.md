---
description: "Task list for SDD-03 — internal/vault: HUSH file format + AES-256-GCM + atomic write"
---

# Tasks: HUSH Vault File Format + In-Memory Store (SDD-03)

**Input**: Design documents from `/Users/mrz/projects/hush/specs/003-vault-format/`
**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/vault-api.md](./contracts/vault-api.md), [quickstart.md](./quickstart.md), [docs/sdd/SDD-03.md](../../docs/sdd/SDD-03.md)

**Tests**: TDD-MANDATORY per Constitution VIII. Every behaviour-contract test below is written **before** its implementation task. Each test phase is RED first; the implementation phase brings it to GREEN; the polish phase verifies the gates (race, fuzz, coverage).

**Coverage target**: 100 % on `internal/vault/...` (the security-critical-package gate).
**Fuzz target #1**: `FuzzVaultDecode`, ≥60 s clean, ≤50 MiB per call, every error typed.

**Organization**: Tasks are grouped by user story (US1–US5 from [spec.md](./spec.md)). Each story has its tests written first, sees them fail, then implementation makes them pass.

## Format: `[ID] [P?] [Story?] Description`

- **[P]**: Can run in parallel (different files, no incomplete-task dependencies).
- **[Story]**: User story this task serves (US1–US5). Setup, Foundational, and Polish tasks are unlabelled.
- File paths in this document are absolute under the repo root `/Users/mrz/projects/hush/`.

## Path conventions

- **Production code**: [internal/vault/](../../internal/vault/) — flat Go package, no further sub-packages introduced (`internal/vault/securebytes/` is SDD-02, untouched).
- **Tests**: same package, `*_test.go` files alongside production code (Go convention).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the package skeleton so every later test/impl task has a place to write code.

- [ ] T001 Verify [internal/vault/securebytes/](../../internal/vault/securebytes/) sub-package is unchanged and importable (SDD-02 lock); run `go build ./internal/vault/securebytes/...` and confirm exit 0. Do not edit any file under `securebytes/`.
- [ ] T002 [P] Create empty production source files at [internal/vault/file.go](../../internal/vault/file.go), [internal/vault/codec.go](../../internal/vault/codec.go), [internal/vault/store.go](../../internal/vault/store.go), [internal/vault/permissions.go](../../internal/vault/permissions.go). Each file must contain only `package vault` and a build-passing comment. No exports yet.
- [ ] T003 [P] Create empty test files at [internal/vault/file_test.go](../../internal/vault/file_test.go), [internal/vault/codec_test.go](../../internal/vault/codec_test.go), [internal/vault/store_test.go](../../internal/vault/store_test.go), [internal/vault/permissions_test.go](../../internal/vault/permissions_test.go), [internal/vault/vault_fuzz_test.go](../../internal/vault/vault_fuzz_test.go). Each file must contain only `package vault` and a build-passing comment.
- [ ] T004 Run `go build ./internal/vault/...` and `go vet ./internal/vault/...`; both must exit 0 against the empty skeleton before any further task starts.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Lock the exported API surface from [contracts/vault-api.md](./contracts/vault-api.md) so every test in Phases 3–7 can reference the locked symbols (types, sentinels, function signatures) and compile. Implementations remain stubs that return a placeholder error so the tests will FAIL at runtime — this is the RED side of TDD.

**⚠️ CRITICAL**: Until this phase is complete, no user-story phase can begin (test files would not compile).

- [ ] T005 In [internal/vault/file.go](../../internal/vault/file.go), declare the on-disk envelope constants exactly as in [data-model.md](./data-model.md) §1: package-level `var magic = []byte{0x48, 0x55, 0x53, 0x48}` and `const ( version byte = 0x01; saltLen = 16; nonceLen = 12; headerLen = 4 + 1 + saltLen + nonceLen; maxFileLen = 64 * 1024 * 1024 )`. Add the package-level `import "errors"`.
- [ ] T006 In [internal/vault/file.go](../../internal/vault/file.go), declare every sentinel error from [contracts/vault-api.md](./contracts/vault-api.md) verbatim: `var ( ErrBadMagic, ErrBadVersion, ErrShortHeader, ErrAuthFailed, ErrFilePermsLoose, ErrSecretNotFound, ErrStoreDestroyed, ErrDuplicateName, ErrFileTooLarge, ErrInvalidName = errors.New("hush/vault: bad magic"), ... )` (use the contract's exact rendered text for each).
- [ ] T007 In [internal/vault/file.go](../../internal/vault/file.go), declare the exported `type Secret struct { Name, Description string; Value *securebytes.SecureBytes }` and the exported `type Store interface { Get(name string) (*securebytes.SecureBytes, error); Names() []string; Destroy() error }` exactly as in [contracts/vault-api.md](./contracts/vault-api.md). Add the `github.com/mrz1836/hush/internal/vault/securebytes` import.
- [ ] T008 In [internal/vault/file.go](../../internal/vault/file.go), declare the exported function signatures `func Load(ctx context.Context, path string, vaultKey *securebytes.SecureBytes) (Store, error)` and `func Save(ctx context.Context, path string, vaultKey *securebytes.SecureBytes, secrets []Secret) error`, each returning `errors.New("vault: not implemented")` for now (RED stubs). Add the `context` import.
- [ ] T009 In [internal/vault/store.go](../../internal/vault/store.go), declare the unexported `type memStore struct { mu sync.RWMutex; names []string; byName map[string]*securebytes.SecureBytes; destroyed bool }` and stub the three `*memStore` methods (`Get`, `Names`, `Destroy`) returning the same not-implemented placeholder (or empty values + `ErrStoreDestroyed`/`nil` typed appropriately) so the package satisfies the `Store` interface at compile time.
- [ ] T010 Run `go build ./internal/vault/...` and `go vet ./internal/vault/...`; both must exit 0. The locked surface now matches [contracts/vault-api.md](./contracts/vault-api.md) and every later test compiles against it.

**Checkpoint**: API surface is locked and every test phase below can compile. `go test ./internal/vault/...` passes (no tests yet) but every later test will FAIL at runtime against the stubs — that is the intended RED state.

---

## Phase 3: User Story 1 — Persist secrets so they survive restart but are unreadable without the right key (Priority: P1) 🎯 MVP

**Goal**: Save a list of named secrets and reload them with the same key (round-trip exactness, all sizes); reject wrong-key load with `ErrAuthFailed`; reject duplicate or invalid names before any filesystem touch; never leak a secret value into err.Error() or any captured log line.

**Independent Test**: Round-trip every (name, description, value) byte-for-byte for vaults of size 0, 1, 5, and 500. Wrong-key load returns `ErrAuthFailed` with no payload bytes in the failure output. Duplicate / invalid input is rejected before any encryption or filesystem write.

### Tests for User Story 1 (write FIRST, watch them FAIL against the Phase 2 stubs)

> **TDD GATE**: After writing each test below, run `go test -run <TestName> ./internal/vault/` and confirm it FAILS. Only then move to the implementation tasks.

- [ ] T011 [P] [US1] In [internal/vault/codec_test.go](../../internal/vault/codec_test.go), write `TestCodec_SealOpen_RoundTrip` (deterministic-key, deterministic-nonce table-driven test that passes a JSON byte slice through the package's internal AES-256-GCM seal then open and asserts byte-for-byte equality). Also write `TestCodec_WireValue_MarshalUnmarshal_NoStringAllocation` — round-trips a `[]byte` payload through `wireValue.MarshalJSON` and `wireValue.UnmarshalJSON`, asserts (a) the JSON form is a quoted base64 string, (b) the resulting `*SecureBytes` borrows back the original bytes verbatim under `Use(fn)`, (c) no Go `string` was allocated to hold the secret value (assert via reflect-on-the-decoded-struct or by inspection of the codec source — the test guards against regressions of the Constitution-X anti-contract).
- [ ] T012 [US1] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_RoundTrip_0Secrets` per [contracts/vault-api.md](./contracts/vault-api.md) Behavioural Guarantees: build a fresh `~/.hush/secrets.vault` analogue under `t.TempDir()` (parent `0700`), call `Save(ctx, path, key, []Secret{})`, call `Load(ctx, path, key)`, assert `store.Names()` is empty and the file on disk has length `headerLen + cipher.Overhead()` exactly (i.e. the JSON `[]` payload encrypts to a tag-only ciphertext + minimal payload).
- [ ] T013 [US1] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_RoundTrip_1Secret`: round-trip exactly one named/described secret with a known value sentinel; assert names are `[name]`, description matches byte-for-byte, and `store.Get(name)` returns a fresh `*SecureBytes` whose `Use(fn)` callback yields the original value byte-for-byte.
- [ ] T014 [US1] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_RoundTrip_5Secrets`: round-trip five distinct entries; assert `store.Names()` returns the entries in their original input order; assert every value round-trips byte-for-byte under `Use`.
- [ ] T015 [US1] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_RoundTrip_500Secrets`: round-trip 500 distinct entries with varied value sizes (1 byte, 8 KiB, 64 KiB at random offsets); assert all 500 names appear in stable order, every description round-trips, and every value matches.
- [ ] T016 [US1] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_LoadWrongPass_ReturnsAuthFailed`: save with `keyA`, load with a different `keyB`, assert `errors.Is(err, ErrAuthFailed)` and `store == nil`. The test MUST NOT compare on `err.Error()` substring.
- [ ] T017 [US1] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_NoLeakInError` (sentinel-leak test from the SDD-03 chunk contract): pack a single secret whose value is the literal byte sequence `SECRET_SHOULD_NEVER_APPEAR_3`, save with `keyA`, install a buffered `slog.JSONHandler` as the test's logger, attempt `Load` with `keyB` (wrong key), assert `errors.Is(err, ErrAuthFailed)` AND assert `bytes.Contains([]byte(err.Error()), []byte("SECRET_SHOULD_NEVER_APPEAR_3")) == false` AND assert `bytes.Contains(logBuf.Bytes(), []byte("SECRET_SHOULD_NEVER_APPEAR_3")) == false`.
- [ ] T018 [US1] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_Save_DuplicateName_NoFilesystemTouch`: build an input list with two entries sharing the same `Name`, snapshot the directory contents (no `<path>` file, no `<path>.tmp`), call `Save`, assert `errors.Is(err, ErrDuplicateName)` AND assert the directory contents are unchanged (no `<path>`, no `<path>.tmp`).
- [ ] T019 [US1] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_Save_InvalidName_NoFilesystemTouch`: drive a table of FR-008 violations — empty name, name >256 bytes, name containing `0x00`, name containing `0x1F`, name containing `0x7F`, name containing a non-ASCII rune, description containing `0x00`, description containing `0x1F`, description containing `0x7F`, description >4096 bytes — and for each row assert `errors.Is(err, ErrInvalidName)` AND the directory is unchanged.
- [ ] T020 [US1] Run `go test ./internal/vault/...`; confirm every test added in T011–T019 FAILS (RED state). Capture failure output to confirm tests are exercising the stubs and not pre-passing.

### Implementation for User Story 1 (now bring tests to GREEN)

- [ ] T021 [P] [US1] In [internal/vault/codec.go](../../internal/vault/codec.go), declare `type wireSecret struct { Name string; Description string; Value wireValue }` with `json:"name"|"description"|"value"` tags, and the package-private `type wireValue struct { sb *securebytes.SecureBytes }`. Implement `wireValue.MarshalJSON` (borrows via `Use(fn)`, base64-encodes inside the borrow, returns the JSON-quoted bytes — no Go-`string` materialisation of the secret value) and `wireValue.UnmarshalJSON([]byte)` (verifies the token is a quoted string, base64-decodes directly into a fresh `[]byte`, calls `securebytes.New(buf)` which copies + mlocks + zeroes, stores the resulting pointer).
- [ ] T022 [US1] In [internal/vault/codec.go](../../internal/vault/codec.go), implement the AES-256-GCM seal/open helpers using `crypto/aes.NewCipher` + `crypto/cipher.NewGCM`. The 32-byte key is borrowed from `*securebytes.SecureBytes` via its `Use(fn)` callback for the duration of `Seal`/`Open` only; the key never escapes the callback or appears in a package-level variable. On `Open` failure, wrap once with `fmt.Errorf("vault: %w", ErrAuthFailed)`.
- [ ] T023 [US1] In [internal/vault/file.go](../../internal/vault/file.go), implement the `Save` input-validation pre-pass per [research.md](./research.md) Decision 10: scan `[]Secret` for duplicate names → return `fmt.Errorf("vault: duplicate name %q: %w", n, ErrDuplicateName)`; for each entry validate `Name` (non-empty, ≤256 bytes, every byte ∈ `0x20`–`0x7E`) and `Description` (≤4096 bytes, no `0x00`–`0x1F`, no `0x7F`) → return `ErrInvalidName` on any violation. The pre-pass MUST execute before any filesystem touch (no `os.Stat`, no `os.OpenFile`).
- [ ] T024 [US1] In [internal/vault/file.go](../../internal/vault/file.go), implement the `Save` happy-path flow per [research.md](./research.md) Decision 5: `ctx.Err()` check → JSON marshal of `[]wireSecret` into an in-memory buffer → `crypto/rand.Read` 16 bytes salt + 12 bytes nonce → AES-256-GCM seal → write `magic+version+salt+nonce+ciphertext+tag` to `<path>.tmp` (`O_WRONLY|O_CREATE|O_TRUNC`, mode `0600`) → `f.Sync()` → `f.Close()` → `os.Chmod(<path>.tmp, 0600)` → `os.Rename(<path>.tmp, <path>)` → `os.Chmod(<path>, 0600)`.
- [ ] T025 [US1] In [internal/vault/file.go](../../internal/vault/file.go), implement the `Load` happy-path flow: `ctx.Err()` check → `os.Stat(path)` → `os.OpenFile(path, O_RDONLY, 0)` → `io.ReadAll` → parse magic → parse version → length check (≥ `headerLen + cipher.Overhead()`) → AES-256-GCM open via the codec helper → `json.Unmarshal` into `[]wireSecret` → construct `*memStore` with `names []string` (in encounter order) and `byName map[string]*securebytes.SecureBytes` (the `wireValue.sb` pointers).
- [ ] T026 [US1] Run `go test -run 'TestCodec_|TestVault_RoundTrip_|TestVault_LoadWrongPass_|TestVault_NoLeakInError|TestVault_Save_Duplicate|TestVault_Save_Invalid' ./internal/vault/`; every test added in T011–T019 must now PASS (GREEN state). Run `go test -count=10 -run 'TestVault_RoundTrip_500Secrets' ./internal/vault/` to flush nondeterminism on the larger payload.

**Checkpoint**: User Story 1 is fully functional and independently testable — the round-trip MVP works end-to-end and the no-secret-in-error invariant is asserted. The full security-critical happy path is GREEN.

---

## Phase 4: User Story 2 — Serve secrets to internal consumers as redaction-protected, individually-owned containers (Priority: P1)

**Goal**: `Store.Get` returns a fresh `*SecureBytes` whose destruction has no effect on later retrievals; `Names()` returns a stable defensive copy; `Destroy()` is idempotent and zeroes every internally-held container; concurrent `Get` is race-clean.

**Independent Test**: Two `Get` calls return independently-owned containers; destroying one leaves the other (and subsequent `Get`s) intact. 100 goroutines `Get`-ing concurrently under `-race` produce zero data races.

### Tests for User Story 2 (write FIRST, watch them FAIL)

- [ ] T027 [P] [US2] In [internal/vault/store_test.go](../../internal/vault/store_test.go), write `TestStore_GetReturnsFreshContainer`: load a known vault, call `Get(name)` twice, destroy the first returned container, then `Use(fn)` on the second — assert the second container's payload is intact byte-for-byte. Also call `Get(name)` a third time and assert the new container's payload is intact (proves the store's internal copy was not affected).
- [ ] T028 [P] [US2] In [internal/vault/store_test.go](../../internal/vault/store_test.go), write `TestStore_GetUnknownName_ReturnsErrSecretNotFound`: load a known vault, call `Get("not-in-vault")`, assert `errors.Is(err, ErrSecretNotFound)`. Also call `Get("")` and assert the same sentinel (per spec edge case "Get on the empty string"). The error's rendered text MUST NOT name any other secret in the store.
- [ ] T029 [P] [US2] In [internal/vault/store_test.go](../../internal/vault/store_test.go), write `TestStore_Names_StableOrder_NoValues`: load a known vault with names `[alpha, bravo, charlie]` (in that order), call `Names()` twice, assert both calls return exactly `[alpha, bravo, charlie]` in order. Assert the returned slice is a defensive copy by sorting it in-place between the two calls — the second call's order MUST be unaffected.
- [ ] T030 [P] [US2] In [internal/vault/store_test.go](../../internal/vault/store_test.go), write `TestStore_Destroy_Idempotent_ZeroesContainers`: load a known vault, capture a `Get(name)` result, call `store.Destroy()`, call `store.Destroy()` a second time (must not panic, must return nil), then call `Get(name)` and assert `errors.Is(err, ErrStoreDestroyed)`.
- [ ] T031 [P] [US2] In [internal/vault/store_test.go](../../internal/vault/store_test.go), write `TestStore_GetAfterDestroy_ReturnsErrStoreDestroyed`: load a known vault, call `Destroy`, call `Get(known-name)`, assert `errors.Is(err, ErrStoreDestroyed)` AND `!errors.Is(err, ErrSecretNotFound)` (proves the two sentinels are programmatically distinguishable per spec Clarification Q1).
- [ ] T032 [US2] In [internal/vault/store_test.go](../../internal/vault/store_test.go), write `TestStore_ConcurrentGet` per the SDD-03 chunk contract: load a known vault containing 10 names with distinct values; spawn exactly 100 goroutines, each performing 100 `Get(name)` operations against a randomly-selected name from the 10; each goroutine asserts the borrowed payload matches the expected bytes; `wg.Wait()`; assert zero failures. The test MUST be race-clean under `go test -race -run TestStore_ConcurrentGet ./internal/vault/`.
- [ ] T033 [US2] Run `go test ./internal/vault/...`; confirm every test in T027–T032 FAILS (RED state) against the Phase 2 stubs.

### Implementation for User Story 2

- [ ] T034 [US2] In [internal/vault/store.go](../../internal/vault/store.go), implement `*memStore.Get(name)` per [research.md](./research.md) Decision 7 + Decision 13: `mu.RLock` → check `destroyed` (return `ErrStoreDestroyed`) → look up `byName[name]` (return `ErrSecretNotFound` if missing) → enter `Use(fn)` on the inner `*SecureBytes`, copy the borrowed bytes into a freshly allocated `[]byte`, exit `Use` → call `securebytes.New(buf)` to construct a NEW container (which copies + mlocks + zeroes the transient buf) → return the new container. If the inner `Use` returns `securebytes.ErrDestroyed` (race-with-destroy), wrap as `ErrStoreDestroyed`.
- [ ] T035 [US2] In [internal/vault/store.go](../../internal/vault/store.go), implement `*memStore.Names()` per [research.md](./research.md) Decision 11: `mu.RLock` → return `append([]string(nil), s.names...)` (defensive copy in stable load order). The method MUST NOT include any value or description bytes in its output.
- [ ] T036 [US2] In [internal/vault/store.go](../../internal/vault/store.go), implement `*memStore.Destroy()` per [research.md](./research.md) Decision 8 + Decision 12: `mu.Lock` → if `destroyed` already, return nil (idempotent) → set `destroyed = true` → iterate `byName`, calling each container's `Destroy()` → return `errors.Join(...)` over any per-container destroy errors.
- [ ] T037 [US2] Run `go test -race -run 'TestStore_' ./internal/vault/`; every test in T027–T032 must now PASS. Run `TestStore_ConcurrentGet` 10 additional times under `-race` (`go test -race -count=10 -run TestStore_ConcurrentGet ./internal/vault/`) to flush schedule nondeterminism. Zero data races, zero failures.

**Checkpoint**: User Story 2 is fully functional. Per-request retrieval is race-clean and consumer-lifecycle-decoupled. The store-destroyed sentinel is programmatically distinguishable from secret-not-found.

---

## Phase 5: User Story 3 — Replace the vault on disk atomically (Priority: P1)

**Goal**: At every instant during `Save`, the file at `<path>` is either the complete previous file or the complete new file (never partial / zero-length / syntactically-invalid). The produced file's mode is `0600`. On a controlled mid-flight error, `<path>.tmp` is best-effort cleaned up.

**Independent Test**: A parallel reader observes only complete-old-or-complete-new. The post-save file mode is `0600`. A controlled mid-flight error leaves the original file unchanged AND removes the working file.

### Tests for User Story 3 (write FIRST, watch them FAIL)

- [ ] T038 [US3] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_SaveAtomic_NoIntermediate`: save a baseline vault `V1` to `<path>`; in a goroutine loop, repeatedly stat `<path>` and capture `(size, mtime)` snapshots; concurrently call `Save` with `V2` (different content); after `Save` returns, assert every captured snapshot was either `(size(V1), mtime(V1))` or `(size(V2), mtime(V2))` — never a third (partial) tuple. Additionally, while the snapshot loop runs, attempt a `vault.Load` against `<path>` from the goroutine N times; every successful `Load` must return either the `V1` content or the `V2` content (no torn read). After `Save` returns, assert no `<path>.tmp` exists in the directory.
- [ ] T039 [US3] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_SaveSetsMode0600`: clear the parent directory's umask hint (set the test process's umask to `0022` for the duration of the test via `syscall.Umask`); call `Save`; stat the produced file and assert `info.Mode().Perm() == 0o600`. Restore the prior umask in `t.Cleanup`.
- [ ] T040 [US3] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_Save_MidFlightFailure_RemovesTmp` per [contracts/vault-api.md](./contracts/vault-api.md) Behavioural Guarantee: arrange a `Save` call that fails mid-flight (drive an `errors.New` from a deliberately-truncated parent-mode flip OR from a chmod-failure injection — pick whichever can be done without changing the production code's surface; document the chosen mechanism in the test comment). Pre-create a baseline vault `V0` at `<path>`. Assert (a) the post-call `Save` returns the controlled error, (b) `<path>` content is byte-for-byte unchanged from `V0` (FR-013), (c) no `<path>.tmp` exists in the directory after the error path returns.
- [ ] T041 [US3] Run `go test -run 'TestVault_SaveAtomic_NoIntermediate|TestVault_SaveSetsMode0600|TestVault_Save_MidFlightFailure_RemovesTmp' ./internal/vault/`; every test in T038–T040 must FAIL (the Phase 3 implementation laid the groundwork; these tests pin down the atomicity / mode / cleanup invariants explicitly).

### Implementation for User Story 3

- [ ] T042 [US3] In [internal/vault/file.go](../../internal/vault/file.go), tighten the `Save` flow's atomicity invariants per [research.md](./research.md) Decision 5 (if not already complete from T024): confirm the `<path>.tmp` is in the SAME directory as `<path>` (so `os.Rename` is same-FS atomic), confirm `f.Sync()` is called BEFORE `f.Close()` and BEFORE `os.Rename`, and confirm the post-rename `os.Chmod(<path>, 0600)` is the last step. Write a short comment naming each step's purpose.
- [ ] T043 [US3] In [internal/vault/file.go](../../internal/vault/file.go), implement the controlled-mid-flight-error cleanup branch per [research.md](./research.md) Decision 5 + spec Clarification Q4: every `return err` path between `<path>.tmp` creation and the successful `os.Rename` must, before returning, attempt `os.Remove(<path>.tmp)` via a `defer` or an inline cleanup. Any error from the remove call MUST be logged at `slog.LevelDebug` AND MUST NOT mask the original error (FR-013). Use `slog.Default()` or accept a logger via context — pick the one that does not introduce package-level mutable state.
- [ ] T044 [US3] Run `go test -run 'TestVault_SaveAtomic_NoIntermediate|TestVault_SaveSetsMode0600|TestVault_Save_MidFlightFailure_RemovesTmp' ./internal/vault/`; every test in T038–T040 must now PASS. Re-run `go test -race -count=5 ./internal/vault/...` to confirm the atomicity test does not introduce data races between its snapshot goroutine and the `Save` goroutine.

**Checkpoint**: User Story 3 is fully functional. Atomic-save is observable by an external reader; the post-save mode is `0600` regardless of umask; controlled mid-flight errors leave the directory clean.

---

## Phase 6: User Story 4 — Refuse to operate on a vault file with loose filesystem permissions (Priority: P1)

**Goal**: `Save` and `Load` enforce file mode `== 0600` and parent mode `== 0700`. Any deviation produces `ErrFilePermsLoose`. `Load` additionally rejects oversized files (`>64 MiB`) at stat time.

**Independent Test**: Loose file mode → `ErrFilePermsLoose` from `Load`. Loose parent mode → `ErrFilePermsLoose` from `Load` AND from `Save`. Oversized file → `ErrFileTooLarge` from `Load` before any read.

### Tests for User Story 4 (write FIRST, watch them FAIL)

- [ ] T045 [P] [US4] In [internal/vault/permissions_test.go](../../internal/vault/permissions_test.go), write `TestCheckFileMode_ExactEquality`: drive a table over `(mode, want, expectErr)` rows including `(0o600, 0o600, false)`, `(0o644, 0o600, true)`, `(0o400, 0o600, true)` (stricter is also a failure per [research.md](./research.md) Decision 6), `(0o700, 0o600, true)`. Assert each row's outcome.
- [ ] T046 [P] [US4] In [internal/vault/permissions_test.go](../../internal/vault/permissions_test.go), write `TestCheckParentMode_ExactEquality`: drive a similar table over `(parentMode, want, expectErr)` rows including `(0o700, 0o700, false)`, `(0o755, 0o700, true)`, `(0o770, 0o700, true)`, `(0o500, 0o700, true)`. Assert each row's outcome.
- [ ] T047 [US4] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_LoadLooseFileMode_PermsLoose`: produce a syntactically-valid vault file under `t.TempDir()`, `os.Chmod(<path>, 0o644)` to loosen, call `Load`, assert `errors.Is(err, ErrFilePermsLoose)` AND `store == nil` AND no decryption was attempted (assert via timing — the call returns before any AES-GCM cycle could complete, OR via instrumenting the codec helper to count invocations — pick the cleaner approach for the test).
- [ ] T048 [US4] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_LoadLooseParentMode_PermsLoose`: produce a syntactically-valid vault file, `os.Chmod(parent, 0o755)` to loosen the parent, call `Load`, assert `errors.Is(err, ErrFilePermsLoose)` AND `store == nil`.
- [ ] T049 [US4] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_Save_LooseParentMode_PermsLoose`: arrange a parent directory with mode `0o755`, call `Save`, assert `errors.Is(err, ErrFilePermsLoose)` AND `<path>` does not exist after the call AND `<path>.tmp` does not exist after the call.
- [ ] T050 [US4] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_Load_OversizedFile_ReturnsErrFileTooLarge` per [contracts/vault-api.md](./contracts/vault-api.md) Behavioural Guarantee: produce a `<path>` whose size is `64 MiB + 1 = 67_108_865` bytes (use `os.Truncate` to avoid actually writing 64 MiB of zeros — sparse-file semantics keep the test fast); `os.Chmod(<path>, 0o600)`; `os.Chmod(parent, 0o700)`; call `Load`; assert `errors.Is(err, ErrFileTooLarge)` AND no read attempt was made (verify by observing the file's `atime` is unchanged across the call on platforms that record atime — or by inspecting code paths in the production source, accepted as a documentation-style assertion). The 64 MiB-exact case (`64 MiB`) MUST succeed the size check.
- [ ] T051 [US4] Run `go test -run 'TestCheckFileMode_|TestCheckParentMode_|TestVault_LoadLooseFileMode_|TestVault_LoadLooseParentMode_|TestVault_Save_LooseParentMode_|TestVault_Load_OversizedFile_' ./internal/vault/`; every test in T045–T050 must FAIL (RED state).

### Implementation for User Story 4

- [ ] T052 [US4] In [internal/vault/permissions.go](../../internal/vault/permissions.go), implement `func checkFileMode(path string, want fs.FileMode) error` per [research.md](./research.md) Decision 6: `os.Stat(path)` → `info.Mode().Perm() == want` → return `nil` else `fmt.Errorf("vault: file %q mode %#o != %#o: %w", path, info.Mode().Perm(), want, ErrFilePermsLoose)`. Also implement `func checkParentMode(path string, want fs.FileMode) error` with the same shape but stating the parent in the rendered text.
- [ ] T053 [US4] In [internal/vault/file.go](../../internal/vault/file.go), wire `checkFileMode(<path>, 0o600)` and `checkParentMode(<path>, 0o700)` into the `Load` entry path BEFORE any `os.OpenFile` call. Wire `checkParentMode(<path>, 0o700)` into the `Save` entry path (BEFORE creating `<path>.tmp`). Wire the `os.Stat(<path>).Size() > maxFileLen` check into `Load` (after the perm checks, before `os.OpenFile`); on violation return `fmt.Errorf("vault: file %q size %d exceeds %d: %w", path, size, maxFileLen, ErrFileTooLarge)`.
- [ ] T054 [US4] Run `go test -run 'TestCheckFileMode_|TestCheckParentMode_|TestVault_LoadLooseFileMode_|TestVault_LoadLooseParentMode_|TestVault_Save_LooseParentMode_|TestVault_Load_OversizedFile_' ./internal/vault/`; every test in T045–T050 must now PASS.

**Checkpoint**: User Story 4 is fully functional. Loose file or parent permissions and oversized files are refused before any decryption attempt.

---

## Phase 7: User Story 5 — Distinguish failure modes precisely (Priority: P2)

**Goal**: Every parser-level failure mode (magic mismatch, version mismatch, header truncation at every boundary, ciphertext truncation below tag, ciphertext truncation above tag) is programmatically distinguishable via its own typed sentinel.

**Independent Test**: A minimal-input table drives one row per named failure mode and asserts each row's `errors.Is` identity is unique to that mode.

### Tests for User Story 5 (write FIRST, watch them FAIL)

- [ ] T055 [US5] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_LoadBadMagic_ReturnsErrBadMagic`: write a 49-byte file that begins with `WRONG` instead of `HUSH` (rest is plausible header + tag bytes); chmod `0o600`, parent `0o700`; call `Load`; assert `errors.Is(err, ErrBadMagic)`.
- [ ] T056 [US5] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_LoadBadVersion_ReturnsErrBadVersion`: write a 49-byte file that begins with `HUSH` but byte 4 is `0x02` (or any value other than `0x01`); chmod and load; assert `errors.Is(err, ErrBadVersion)`.
- [ ] T057 [US5] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_LoadTruncatedAtMagic_ShortHeader`: drive a table with file lengths `0, 1, 2, 3` (all below the 4-byte magic); for each, assert `errors.Is(err, ErrShortHeader)`. (Note: a non-empty 1–3 byte file whose prefix does NOT match the start of `HUSH` still classifies as `ErrShortHeader` since we cannot read the full magic — see [data-model.md](./data-model.md) §1 length-class invariants.)
- [ ] T058 [US5] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_LoadTruncatedAtSalt_ShortHeader`: drive a table with file lengths `5, 6, ..., 20` (above the magic+version boundary, below the end of the salt); for each, write `HUSH\x01` followed by the appropriate truncated salt bytes and assert `errors.Is(err, ErrShortHeader)`.
- [ ] T059 [US5] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_LoadTruncatedAtNonce_ShortHeader`: drive a table with file lengths `21, 22, ..., 32` (above the salt boundary, below the end of the nonce); for each, assert `errors.Is(err, ErrShortHeader)`. Also test exactly `headerLen = 33` (header complete, ciphertext absent — still fails the `headerLen + cipher.Overhead()` minimum) and assert `errors.Is(err, ErrShortHeader)`.
- [ ] T060 [US5] In [internal/vault/file_test.go](../../internal/vault/file_test.go), write `TestVault_LoadTruncatedCiphertext_AuthFailed`: take a known-good vault, truncate the ciphertext by 1 byte (still ≥ `cipher.Overhead()` so above the `ErrShortHeader` boundary), call `Load`, assert `errors.Is(err, ErrAuthFailed)`. Also drive a row that truncates the file to exactly `headerLen + cipher.Overhead() = 49` bytes (ciphertext length = tag length minimum) and assert it returns `ErrAuthFailed` (the AEAD library rejects the empty ciphertext under tag check) per [contracts/vault-api.md](./contracts/vault-api.md) and [research.md](./research.md) Decision 1.
- [ ] T061 [US5] Run `go test -run 'TestVault_LoadBadMagic_|TestVault_LoadBadVersion_|TestVault_LoadTruncatedAtMagic_|TestVault_LoadTruncatedAtSalt_|TestVault_LoadTruncatedAtNonce_|TestVault_LoadTruncatedCiphertext_' ./internal/vault/`; every test in T055–T060 must FAIL (RED).

### Implementation for User Story 5

- [ ] T062 [US5] In [internal/vault/file.go](../../internal/vault/file.go), refine the `Load` parser ordering per [data-model.md](./data-model.md) §1 length-class invariants: `len(file) < 4 → ErrShortHeader`; `len(file) ≥ 4 && bytes[0:4] != magic → ErrBadMagic`; `len(file) < 5 → ErrShortHeader`; `len(file) ≥ 5 && bytes[4] != version → ErrBadVersion`; `len(file) < headerLen + cipher.Overhead() → ErrShortHeader`; otherwise hand `bytes[headerLen:]` to AES-GCM `Open` with the salt+nonce slices, mapping any open failure to `ErrAuthFailed`. Wrap each sentinel with a `fmt.Errorf("vault: ...: %w", ...)` so the sentinel is comparable via `errors.Is`.
- [ ] T063 [US5] Run `go test -run 'TestVault_LoadBadMagic_|TestVault_LoadBadVersion_|TestVault_LoadTruncatedAtMagic_|TestVault_LoadTruncatedAtSalt_|TestVault_LoadTruncatedAtNonce_|TestVault_LoadTruncatedCiphertext_' ./internal/vault/`; every test in T055–T060 must now PASS.

**Checkpoint**: User Story 5 is fully functional. Every parser failure mode has a programmatically-distinguishable typed sentinel; the load path produces no panic on any reachable input.

---

## Phase 8: Fuzz Target #1 (`FuzzVaultDecode`) — Constitution VIII gate

**Goal**: A native Go fuzz target drives random byte sequences into `Load` for ≥60 seconds, with no panic, ≤50 MiB allocation per call, and every reachable error returned is one of the ten typed sentinels.

**Independent Test**: `go test -fuzz=FuzzVaultDecode -fuzztime=60s ./internal/vault/` runs to completion with no crashes and no new corpus entries (which would indicate a previously-undiscovered classification gap).

### Test for the fuzz target (write FIRST, but RED-state semantics differ)

> Fuzz tests are inherently RED-then-GREEN-via-corpus-coverage; the assertion table inside the fuzz function is the contract.

- [ ] T064 In [internal/vault/vault_fuzz_test.go](../../internal/vault/vault_fuzz_test.go), write `FuzzVaultDecode(f *testing.F)` per [research.md](./research.md) Decision 14: seed the corpus with at least three round-trip-derived envelopes (an empty vault, a 1-secret vault, a 5-secret vault) plus a curated set of truncated and bit-flipped variants. The fuzz function takes a single `[]byte` input, writes it to a `t.TempDir()`-rooted file (mode `0600`, parent `0700`), captures `runtime.MemStats` BEFORE `Load`, calls `Load`, captures `runtime.MemStats` AFTER `Load`, and asserts:
  1. No panic occurred (the Go fuzz framework already enforces this; the assertion is implicit but explicitly noted in the function's leading comment).
  2. If `err != nil`, then `err` matches at least one of the ten typed sentinels via `errors.Is` (table iterate over `[ErrBadMagic, ErrBadVersion, ErrShortHeader, ErrAuthFailed, ErrFilePermsLoose, ErrFileTooLarge, ErrSecretNotFound, ErrStoreDestroyed, ErrDuplicateName, ErrInvalidName]` — note the perm/size errors are unreachable here because the fuzz harness controls perms and size, but they are listed for completeness).
  3. `(memStats.AFTER.HeapAlloc - memStats.BEFORE.HeapAlloc) < 50 * 1024 * 1024` (50 MiB ceiling per call).
- [ ] T065 Verify the fuzz harness compiles and the seed corpus runs cleanly: `go test -run FuzzVaultDecode ./internal/vault/` (this executes only the seeds, not the fuzz engine; takes <1 s). All seed runs must pass.

**Checkpoint**: The fuzz harness is wired and ready. The 60 s gate runs in Phase 9 (Polish), where the full set of repo-wide gates fires.

---

## Phase 9: Polish & Cross-Cutting Concerns (gates + docs + commit)

**Purpose**: Final repo-wide gates per the SDD-03 Prompt 5 (Implement) ritual, plus the three doc updates that lock the API surface and mark SDD-03 done.

- [ ] T066 Run `magex format:fix` from the repo root. Confirm exit 0 and no uncommitted formatting changes remain after the formatter settles.
- [ ] T067 Run `magex lint` from the repo root. Confirm exit 0 with zero new lint findings on `internal/vault/...`. Address any new finding by editing the offending file directly (do not suppress).
- [ ] T068 Run `magex test:race` from the repo root. Confirm the entire test suite passes under `-race` with zero data races. The `TestStore_ConcurrentGet` and `TestVault_SaveAtomic_NoIntermediate` tests are the load-bearing ones for `internal/vault`; both must be GREEN.
- [ ] T069 Run `go test -fuzz=FuzzVaultDecode -fuzztime=60s ./internal/vault/`. Confirm 60 s elapsed with no crashes, no `testdata/fuzz/FuzzVaultDecode/<hash>` corpus additions (which would indicate a found-but-unfixed input class), and no allocation-ceiling violation.
- [ ] T070 Run `go test -cover -coverprofile=coverage.out ./internal/vault/`. Confirm coverage = 100.0 % on the package. If <100 %, run `go tool cover -html=coverage.out` to identify uncovered lines and either add a targeted test in the appropriate user-story phase OR justify an exclusion in writing (Constitution VIII permits no exclusions on security-critical packages — the second branch is rare and requires a SDD amendment).
- [ ] T071 Confirm the sentinel-leak test `TestVault_NoLeakInError` passed and `SECRET_SHOULD_NEVER_APPEAR_3` is absent from any `err.Error()` and any captured slog line. Re-run with `-count=20` to flush any nondeterminism: `go test -run TestVault_NoLeakInError -count=20 ./internal/vault/`.
- [ ] T072 [P] Append the "## `internal/vault/` → ### Exported API — locked at SDD-03" section to [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) listing the twelve exported identifiers from [contracts/vault-api.md](./contracts/vault-api.md): `Secret`, `Store`, `Load`, `Save`, plus the ten sentinels. Use the contract's docstring text verbatim where applicable.
- [ ] T073 [P] Update [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-2 row with the new test file paths: `internal/vault/file_test.go`, `internal/vault/codec_test.go`, `internal/vault/store_test.go`, `internal/vault/permissions_test.go`, `internal/vault/vault_fuzz_test.go`. Note explicitly that the SIGHUP-reload half of AC-2 remains owned by SDD-10.
- [ ] T074 [P] Mark SDD-03 status `done` in [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md).
- [ ] T075 Run `git status` to confirm the changeset includes only `internal/vault/`, `docs/PACKAGE-MAP.md`, `docs/AC-MATRIX.md`, `docs/SDD-PLAYBOOK.md`, and `specs/003-vault-format/tasks.md`. Stage these specific paths and create one combined commit titled `feat(vault): HUSH file format + AES-256-GCM + atomic write (SDD-03)` per the SDD-03 Prompt 5 ritual. Do not commit any unrelated path.

**Checkpoint**: All gates pass clean (format, lint, race, fuzz 60 s, coverage 100 %, sentinel-leak absent). The three docs are updated. One combined commit is created.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately.
- **Foundational (Phase 2)**: Depends on Setup. **BLOCKS every user-story phase** (without locked types/sentinels, no test compiles).
- **User Story 1 (Phase 3)**: Depends on Foundational. Independently testable.
- **User Story 2 (Phase 4)**: Depends on Foundational + Phase 3 (a real `Load` is needed to populate the store under test). Independently testable thereafter.
- **User Story 3 (Phase 5)**: Depends on Foundational + Phase 3 (`Save` orchestration scaffolded). Tightens atomicity / cleanup invariants.
- **User Story 4 (Phase 6)**: Depends on Foundational + Phase 3 (perms gates wrap the load/save entry points).
- **User Story 5 (Phase 7)**: Depends on Foundational + Phase 3 (the parser is part of `Load`; this phase tightens its sentinel classification). Independently testable.
- **Fuzz (Phase 8)**: Depends on Phases 3–7 (the parser must already be sentinel-classified for the fuzz oracle to be meaningful).
- **Polish (Phase 9)**: Depends on every prior phase.

### Within Each User Story

- Tests are written **first**; they MUST be observed to FAIL (RED) before implementation tasks begin.
- Implementation brings each test to GREEN one file at a time.
- For phases whose implementation is partly already done by an earlier phase (Phase 5 builds on Phase 3's `Save`), the "tightening" tasks merely refine an already-compiled production source.

### Parallel Opportunities

- T002 / T003 (Setup file-creation) — `[P]`, different files.
- T011 (codec tests) and T012–T019 (file tests) — codec_test.go is `[P]` with file_test.go; tasks within the same file are sequential (writing N tests in one file is one editor session, not parallelisable across agents).
- T021 (codec impl) and T022 (codec impl) target the same file (codec.go) — sequential.
- T027–T031 (store tests) — all in store_test.go, sequential within the same file but `[P]` against any other-file task running in the same window.
- T045 / T046 (permissions_test.go rows) — same file, sequential.
- T072 / T073 / T074 (doc updates) — `[P]`, different files.

---

## Parallel Execution Examples

### Phase 1 setup, parallel skeleton creation

```bash
# Run T002 and T003 in parallel (different files, no inter-deps):
Task: "Create empty production files: file.go, codec.go, store.go, permissions.go (each: package vault)"
Task: "Create empty test files: file_test.go, codec_test.go, store_test.go, permissions_test.go, vault_fuzz_test.go (each: package vault)"
```

### Phase 3 US1 RED phase, parallelisable across files

```bash
# Run T011 (codec_test.go) in parallel with the file_test.go tests T012–T019
# (the file_test.go tasks themselves are sequential within that file):
Task: "Write codec_test.go: TestCodec_SealOpen_RoundTrip + TestCodec_WireValue_MarshalUnmarshal_NoStringAllocation"
Task: "Write file_test.go round-trip + auth + sentinel-leak + duplicate/invalid tests in sequence"
```

### Phase 9 doc updates, parallel

```bash
# Run T072 / T073 / T074 in parallel — three different doc files, no shared editor state:
Task: "Append Exported API — locked at SDD-03 section to docs/PACKAGE-MAP.md"
Task: "Update docs/AC-MATRIX.md AC-2 row with new test file paths"
Task: "Mark SDD-03 done in docs/SDD-PLAYBOOK.md"
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Phase 1: Setup.
2. Phase 2: Foundational — locked API surface, every test compiles against it.
3. Phase 3: User Story 1 — round-trip, wrong-key rejection, no-secret-in-error, input validation.
4. **STOP and VALIDATE**: `go test -run 'TestVault_RoundTrip_|TestVault_LoadWrongPass_|TestVault_NoLeakInError|TestVault_Save_Duplicate|TestVault_Save_Invalid' ./internal/vault/` — every test green.
5. The MVP is the encrypted persistence half of AC-2. SDD-10 builds on this.

### Incremental Delivery

1. MVP (Phases 1 + 2 + 3) → demo persistence + wrong-key rejection.
2. Add Phase 4 (US2: serve secrets) → demo race-clean concurrent retrieval.
3. Add Phase 5 (US3: atomic) → demo torn-write impossibility under concurrent reads.
4. Add Phase 6 (US4: perms) → demo refusal on loose modes.
5. Add Phase 7 (US5: classification) → demo every parser failure has its own sentinel.
6. Add Phase 8 (fuzz) → demo 60-s clean, no panic, every error typed.
7. Phase 9 polish → gates, docs, combined commit.

### TDD discipline notes

- **Every** user-story phase is RED-first: write the tests, run them, observe them fail against the Phase 2 stubs (or earlier-phase scaffolding), only then write the implementation.
- The "watch them FAIL" steps (T020, T033, T041, T051, T061) are not throwaway — they are the guard against accidentally-pre-passing tests (e.g. a test that asserts `err != nil` against a stub returning `errors.New("not implemented")` would pass spuriously; the test must assert `errors.Is(err, ErrSpecificSentinel)` so that the stub's generic error fails the assertion).
- The 100 % coverage gate (T070) is a guard against silent dead-code paths. Add tests, do not exclude paths.
- The fuzz gate (T069) is a guard against panic-on-bad-input regressions. The 60 s budget is the constitutional minimum.

---

## Notes

- All file paths in this document are absolute under `/Users/mrz/projects/hush/`. The tasks above use markdown links (e.g. [file.go](../../internal/vault/file.go)) for IDE navigation.
- `[P]` marks tasks targeting different files with no dependency on incomplete tasks.
- `[USx]` labels user-story tasks (Phases 3–7); Setup, Foundational, Fuzz, and Polish phases carry no story label.
- Every test file lives alongside its production file (Go convention: `_test.go` suffix in the same package).
- The SDD-03 chunk contract's "no commits between phases" rule is upheld by deferring all `git add` / `git commit` to T075 (the single combined commit at the end of Phase 9).
- Coverage = 100 % on `internal/vault/...` is a Constitution VIII non-negotiable for the four security-critical packages (`vault`, `keys`, `token`, `transport`).
