# Tasks: Test Fixtures, Sentinel Helpers, and Programmable Discord Approval Stub (SDD-04)

**Input**: Design documents from `/specs/004-testutil-fixtures/`
**Prerequisites**: plan.md (loaded), spec.md (loaded), research.md (loaded), data-model.md (loaded), contracts/testutil-api.md (loaded), quickstart.md (loaded)

**Tests**: TDD-mandatory per Constitution VIII. Every helper-implementation task is preceded by a self-test task that defines the helper's behaviour and MUST FAIL before the implementation lands. Coverage target: ≥80% on `internal/testutil/` (test infrastructure relaxed bar; SDD-04 contract).

**Organization**: Tasks are grouped by user story (US1–US6). Stories US1, US3, US4, US5, US6 are independently testable; US2 depends on US1 (the vault fixture's key derivation reuses the deterministic master seed cached by `NewTestKeys`).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Parallelizable (different file, no incomplete-task dependencies).
- **[Story]**: Maps the task to a user story (US1–US6).
- File paths are exact and absolute relative to the repository root (`/Users/mrz/projects/hush`).

## Path Conventions

- Production source files: `internal/testutil/*.go`.
- Test source files: `internal/testutil/*_test.go`.
- Lint config (depguard rule update): `.golangci.yml` (existing file at repo root).
- Documentation lock: `docs/PACKAGE-MAP.md` (handled in Polish phase).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the new `internal/testutil` package directory and the package-level documentation file. No code logic in this phase — only the shell that subsequent phases populate.

- [X] T001 Create the package directory at [internal/testutil/](internal/testutil/) (sibling under `internal/`, first test-only package in the tree).
- [X] T002 Write the package-level doc file at [internal/testutil/doc.go](internal/testutil/doc.go): a single-paragraph package comment naming the package as test-only, listing the eight chunk-contract exported helpers (`NewTestVault`, `NewTestKeys`, `SentinelSecret`, `AssertSentinelAbsent`, `DiscordStub`, `NewDiscordStub`, `ApprovalCall`, `Approver`), citing Constitution Principles I and IX as the basis for the test-only constraint, and declaring `package testutil`.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: None beyond Setup. Every subsequent user story is buildable directly on top of Phase 1. The deterministic seed cache (US1) is the only logical prerequisite for the vault fixture (US2); that dependency is encoded in the US2 phase ordering, not lifted into a separate Foundational phase.

**⚠️ CRITICAL**: User-story phases proceed in the order US1 → US2 → US3 → US4 → US5 → US6. US3, US4, US5, US6 may proceed in parallel with each other once US1 is in place, but US2 depends on US1 (vault key derivation reuses the cached seed).

---

## Phase 3: User Story 1 — Deterministic Test Keys (Priority: P1) 🎯 MVP

**Goal**: A downstream test calls `testutil.NewTestKeys(t)` and receives a 64-byte master seed derived deterministically from the hardcoded test passphrase (`hush-test-seed-NEVER-USE-IN-PROD`) and a fixed 16-byte salt. Two invocations in any test, in any process, return byte-identical bytes. The first call costs ~1.5 s for Argon2id; subsequent calls are ~0 (memoised behind `sync.Once`).

**Independent Test**: A test invokes `NewTestKeys` twice, captures both byte sequences, asserts they are byte-identical and 64 bytes long. A second test runs concurrently from many goroutines under `-race` and asserts every result is byte-identical with no data race.

### Tests for User Story 1 (TDD-mandatory) ⚠️

> Write these tests FIRST. They MUST FAIL before any implementation in this phase lands.

- [X] T003 [US1] Author the keys-fixture self-test file at [internal/testutil/keys_fixture_test.go](internal/testutil/keys_fixture_test.go) with the package declaration `package testutil` and the following named tests:
  - `TestNewTestKeys_Length` — assert the returned seed is exactly 64 bytes.
  - `TestNewTestKeys_Determinism` — invoke `NewTestKeys(t)` twice, compare the two byte sequences with `bytes.Equal`, fail if not byte-identical (covers FR-003, SC-001).
  - `TestNewTestKeys_PassphraseProvenance` — assert the package-level test passphrase literal (exposed as an unexported constant readable by the test via the same package) contains the substring `NEVER-USE-IN-PROD` (covers FR-002, SC-002).
  - `TestNewTestKeys_Concurrent` — launch 100 goroutines each calling `NewTestKeys(t)`, collect the results into a slice, assert every result is byte-identical to the first; this test runs under `-race` (covers FR-003 concurrent invariant).
  - `TestNewTestKeys_DefensiveCopy` — invoke `NewTestKeys(t)`, mutate every byte in the returned slice, invoke again, assert the second invocation returns the unaltered original bytes (covers the cache-poisoning anti-recipe in `data-model.md` §1).

  Verify the file compiles (`go vet ./internal/testutil/`) but every test fails (the helper does not yet exist).

### Implementation for User Story 1

- [X] T004 [US1] Implement `NewTestKeys` in [internal/testutil/keys_fixture.go](internal/testutil/keys_fixture.go): declare the package-level unexported `testPassphrase = []byte("hush-test-seed-NEVER-USE-IN-PROD")` (32 bytes) and `testSalt = []byte{0x01,…,0x10}` (16 bytes); declare the unexported `seedOnce sync.Once` and `cachedSeed []byte` package-level variables with a `//nolint:gochecknoglobals` justification comment citing the Argon2id memoisation rationale (Plan §Complexity Tracking). Implement `func NewTestKeys(t *testing.T) (masterSeed []byte)` that calls `t.Helper()`, ensures the seed is derived once via `seedOnce.Do(func() { cachedSeed, _ = keys.DeriveMasterSeed(context.Background(), testPassphrase, testSalt) })`, and returns a defensive copy (`out := make([]byte, 64); copy(out, cachedSeed); return out`). Verify all T003 tests pass under `go test -race ./internal/testutil/`.

**Checkpoint**: User Story 1 is fully functional. `NewTestKeys(t)` returns a deterministic, race-clean, defensive-copied 64-byte master seed. The cached seed is the foundation US2 builds on.

---

## Phase 4: User Story 2 — On-Disk Vault Fixture (Priority: P1)

**Goal**: A downstream test calls `testutil.NewTestVault(t, secrets)` and receives the absolute path of a real HUSH-format vault file inside `t.TempDir()`, the 32-byte vault encryption key wrapped in `*securebytes.SecureBytes`, and a `cleanup func()`. The fixture itself registers `t.Cleanup` so callers can ignore the returned cleanup. After cleanup, the vault key's underlying buffer is zeroed (`Len() == 0`). Empty secrets maps and parallel subtests both work.

**Independent Test**: A test invokes the fixture with two named secrets, opens the file via `vault.Load`, decrypts both secrets, asserts byte-equivalence to the supplied values. A second test invokes the fixture, captures the vault key's `Len()`, runs the cleanup explicitly, and asserts post-cleanup `Len() == 0`.

**Depends on**: User Story 1 (vault key derivation reuses `NewTestKeys`'s cached master seed via `keys.DeriveVaultEncKey`).

### Tests for User Story 2 (TDD-mandatory) ⚠️

> Write these tests FIRST. They MUST FAIL before any implementation in this phase lands.

- [X] T005 [US2] Author the vault-fixture self-test file at [internal/testutil/vault_fixture_test.go](internal/testutil/vault_fixture_test.go) with the package declaration `package testutil` and the following named tests:
  - `TestNewTestVault_RoundTrip` — invoke the fixture with `{"foo": "bar", "baz": "qux"}`, call `vault.Load(ctx, path, vaultKey)`, retrieve each secret via `store.Get(name)`, borrow each value via `Use(func(b []byte) { ... })`, assert each plaintext equals the supplied value (covers FR-005, SC-003).
  - `TestNewTestVault_PathContainment` — invoke the fixture, capture the returned path, assert `strings.HasPrefix(path, t.TempDir())` AND `filepath.Dir(path) == t.TempDir()` (covers FR-006, SC-004).
  - `TestNewTestVault_KeyZeroed` — invoke the fixture, capture the `vaultKey`, run the returned `cleanup()` explicitly, assert `vaultKey.Len() == 0` (covers FR-008, SC-005).
  - `TestNewTestVault_CleanupIdempotent` — invoke the fixture, run the returned `cleanup()` twice in succession, assert no panic and no error from the second invocation (covers R-04 in `research.md`).
  - `TestNewTestVault_EmptySecrets` — invoke the fixture with `map[string]string{}`, assert the returned path is a real file (`os.Stat(path)` succeeds), `vault.Load` opens it without error, and `store.Names()` returns zero entries (covers FR-009, SC-007).
  - `TestNewTestVault_ParallelSubtests` — launch 8 parallel subtests via `t.Run("...", func(t *testing.T) { t.Parallel(); ... })`; each subtest invokes the fixture with a unique secret value; collect the eight returned paths, assert every path lives inside its own subtest's `t.TempDir()` (different parent dirs); assert no two subtests share a path (covers FR-006 parallel safety, SC-006).
  - `TestNewTestVault_NoLeakAcrossTests` — declare two sequential top-level tests (`TestVaultLeak_First` and `TestVaultLeak_Second`); the first invokes the fixture, captures the temp-dir path, lets `t.Cleanup` run; the second asserts the captured temp-dir path no longer exists (`os.Stat` returns `IsNotExist`) (covers FR-023, SC-019, FR-022 indirectly).

  Verify the file compiles but every test fails (the helper does not yet exist).

### Implementation for User Story 2

- [X] T006 [US2] Implement `NewTestVault` in [internal/testutil/vault_fixture.go](internal/testutil/vault_fixture.go): call `t.Helper()`; obtain the master seed via `NewTestKeys(t)`; derive the 32-byte vault encryption key via `keys.DeriveVaultEncKey(seed)`; wrap it in `*securebytes.SecureBytes` via `securebytes.New(rawKey)`; for every `(name, value)` in the supplied map, construct `securebytes.New([]byte(value))` and append `vault.Secret{Name: name, Description: "", Value: sb}` to a `[]vault.Secret` slice; resolve `path := filepath.Join(t.TempDir(), "test.vault")`; call `vault.Save(context.Background(), path, vaultKey, secrets)` and `t.Fatalf` on error; build the cleanup closure as `cleanup := func() { _ = vaultKey.Destroy(); for _, sb := range valueSBs { _ = sb.Destroy() } }`; register the cleanup via `t.Cleanup(cleanup)` BEFORE returning; return `(path, vaultKey, cleanup)`. Verify all T005 tests pass under `go test -race ./internal/testutil/`.

**Checkpoint**: User Story 2 is fully functional. Vault fixtures round-trip via `vault.Load`, paths stay inside `t.TempDir()`, cleanup zeroes the vault key, and parallel subtests are race-clean.

---

## Phase 5: User Story 3 — Sentinel Helper and Assertion (Priority: P1)

**Goal**: `testutil.SentinelSecret(n)` returns the canonical `SECRET_SHOULD_NEVER_APPEAR_<n>` literal. `testutil.AssertSentinelAbsent(t, sentinel, haystack)` is a no-op when the sentinel is absent and fails the test handle (with byte-offset + 64-byte context window) when present. Both are stateless and pure.

**Independent Test**: A test invokes `SentinelSecret(7)` and asserts the result is exactly `"SECRET_SHOULD_NEVER_APPEAR_7"`. A second test runs `AssertSentinelAbsent(subT, sentinel, "no leak here")` against a captured sub-`testing.T` and asserts no failure was recorded; a third runs the assertion against `"oops " + sentinel + " leaked"` and asserts a failure was recorded.

**Depends on**: Setup only (Phase 1).

### Tests for User Story 3 (TDD-mandatory) ⚠️

> Write these tests FIRST. They MUST FAIL before any implementation in this phase lands.

- [X] T007 [P] [US3] Author the sentinel self-test file at [internal/testutil/sentinel_test.go](internal/testutil/sentinel_test.go) with the package declaration `package testutil` and the following named tests:
  - `TestSentinelSecret_FormatStability` — assert `SentinelSecret(0) == "SECRET_SHOULD_NEVER_APPEAR_0"`, `SentinelSecret(7) == "SECRET_SHOULD_NEVER_APPEAR_7"`, `SentinelSecret(123) == "SECRET_SHOULD_NEVER_APPEAR_123"` (covers FR-010, SC-008).
  - `TestSentinelSecret_Uniqueness` — invoke `SentinelSecret` for indices 0..99, collect into a `map[string]struct{}`, assert all 100 strings are distinct (covers FR-011, SC-009).
  - `TestSentinelSecret_NoWhitespacePunct` — invoke `SentinelSecret(42)`, iterate every rune in the result, assert each is in `[A-Z_0-9-]` (covers FR-010 substring-search invariant).
  - `TestSentinelSecret_EdgeIndices` — invoke `SentinelSecret(0)` and assert no panic; invoke `SentinelSecret(-1)` and assert the result is `"SECRET_SHOULD_NEVER_APPEAR_-1"` and no panic (covers edge case "sentinel index of zero or negative").
  - `TestAssertSentinelAbsent_Absent` — construct a captured sub-`testing.T` (using `testing.T` mock pattern: invoke the assertion inside a wrapped `t.Run("subtest", func(subT *testing.T) { ... })` and check `subT.Failed()` after); pass a haystack that does NOT contain the sentinel; assert `subT.Failed() == false` (covers FR-012, SC-010).
  - `TestAssertSentinelAbsent_Present` — invoke the assertion inside a `t.Run` subtest with a haystack that contains the sentinel; assert the subtest reports a failure; assert the failure message contains the sentinel substring AND the byte offset (use the captured-output approach: redirect via a custom `*testing.T` wrapper that records `Errorf` arguments, OR run as a subprocess test and inspect the failure log — pick whichever is simpler in the project's test idiom) (covers FR-013, SC-011).
  - `TestAssertSentinelAbsent_EmptyHaystack` — invoke the assertion with `haystack = ""` against a sub-`testing.T`; assert the sub-T was not failed (covers edge case "empty haystack").

  Verify the file compiles but every test fails (the helpers do not yet exist).

### Implementation for User Story 3

- [X] T008 [P] [US3] Implement `SentinelSecret` and `AssertSentinelAbsent` in [internal/testutil/sentinel.go](internal/testutil/sentinel.go):
  - `func SentinelSecret(n int) string { return fmt.Sprintf("SECRET_SHOULD_NEVER_APPEAR_%d", n) }`.
  - `func AssertSentinelAbsent(t *testing.T, sentinel, haystack string)` — call `t.Helper()`, then `i := strings.Index(haystack, sentinel)`; if `i < 0`, return; otherwise compute the 64-byte context window clamped to `[max(0, i-32), min(len(haystack), i+len(sentinel)+32))` and call `t.Errorf("hush/testutil: sentinel %q leaked at offset %d; context: %q", sentinel, i, haystack[start:end])`.

  Verify all T007 tests pass under `go test -race ./internal/testutil/`.

**Checkpoint**: User Story 3 is fully functional. Every redaction test in the project can now use `SentinelSecret` + `AssertSentinelAbsent`.

---

## Phase 6: User Story 4 — Programmable Discord Approval Stub (Priority: P1)

**Goal**: `testutil.NewDiscordStub(t)` returns a `*DiscordStub`. The stub satisfies the local `Approver` interface. `Enqueue` adds programmed decisions to a FIFO queue; `Calls()` returns a defensive copy of the recorded calls. `RequestApproval` consumes the queue head if non-empty, falls back to `ApproveAll`, and finally fails the test handle with `ErrUnexpectedCall` if neither applies. All operations are mutex-serialised for race-clean concurrent use. The stub never opens a network socket.

**Independent Test**: A test instantiates the stub in `ApproveAll` mode, drives one approval through the SUT, asserts the recorded-calls list grows by exactly one. A second test enqueues `[Approve, Deny, Approve]`, drives three approvals, asserts the decisions returned in order. A third test enqueues `[Deny]` only, drives a second approval after the queue exhausts, asserts the test handle fails with `ErrUnexpectedCall`. A fourth test launches 100 goroutines under `-race` and asserts the recorded-calls list ends with exactly 100 entries with no data race.

**Depends on**: Setup only (Phase 1).

### Tests for User Story 4 (TDD-mandatory) ⚠️

> Write these tests FIRST. They MUST FAIL before any implementation in this phase lands.

- [X] T009 [P] [US4] Author the Discord-stub self-test file at [internal/testutil/discord_stub_test.go](internal/testutil/discord_stub_test.go) with the package declaration `package testutil` and the following named tests:
  - `TestDiscordStub_ApproveAll` — instantiate the stub, set `ApproveAll = true`, drive one `RequestApproval` call, assert the returned decision is `DecisionApprove` and the returned error is `nil` (covers FR-015, SC-012).
  - `TestDiscordStub_Queue` — instantiate the stub, call `Enqueue(DecisionApprove, DecisionDeny, DecisionApproveMute)`, drive three sequential `RequestApproval` calls, assert the returned decisions match the queue order (covers FR-016, SC-013).
  - `TestDiscordStub_QueueThenApproveAll` — instantiate the stub, call `Enqueue(DecisionDeny)`, set `ApproveAll = true`, drive five sequential approvals, assert the first returns `DecisionDeny`, calls 2–5 return `DecisionApprove` (covers clarification 1 in `spec.md` §Clarifications, FR-016 second sentence).
  - `TestDiscordStub_CallRecording` — instantiate the stub with `ApproveAll = true`, drive three approvals with three distinct `ApprovalRequest` payloads (different `RequesterHost`, `Scopes`, `SessionType`, `TTL`, `MaxUses`); call `Calls()`, assert the returned slice has length 3, each entry's `Request` field matches the corresponding driven request, each `Index` field equals its position, each `Decision` is `DecisionApprove`, each `Err` is `nil` (covers FR-017, SC-014).
  - `TestDiscordStub_UnexpectedCall` — instantiate the stub with `ApproveAll = false` and an empty queue; capture a sub-`testing.T` (using a recording wrapper that captures `t.Errorf` arguments); drive one approval; assert the returned decision is `DecisionDeny`, the returned error satisfies `errors.Is(err, ErrUnexpectedCall)`, the captured sub-T has been failed, AND the captured failure message contains the `RequesterHost`, the `Scopes`, the `SessionType`, and the result of `LimitDescription()` (covers clarification 2 in `spec.md`, FR-018a).
  - `TestDiscordStub_Concurrent` — instantiate the stub with `ApproveAll = true`, launch 100 goroutines each invoking `stub.RequestApproval(ctx, distinctRequest)`; wait via `sync.WaitGroup`; assert `len(stub.Calls()) == 100`, every recorded entry's `Decision == DecisionApprove`, every entry's `Err == nil`; this test runs under `-race` (covers FR-018, SC-015).
  - `TestDiscordStub_NoNetwork` — using `go/parser.ParseDir` against `internal/testutil/`, walk the AST of every non-`*_test.go` file and assert no import path begins with `"net"`, `"net/http"`, `"github.com/bwmarrin/discordgo"`, or any other network-capable package (covers FR-019, SC-016).
  - `TestDiscordStub_CallsDefensiveCopy` — instantiate the stub with `ApproveAll = true`, drive one approval, capture `Calls()`, mutate the returned slice (e.g. set element 0's `Decision = DecisionDeny`), call `Calls()` again, assert the second call's element 0 still has `Decision == DecisionApprove` (covers `data-model.md` §4 defensive-copy invariant).
  - `TestDiscordStub_CleanupDrains` — instantiate the stub inside a sub-`t.Run` with `ApproveAll = true`, drive one approval, let the subtest exit; in the outer test, assert there is no observable handle to the drained stub (this is structural — the test's purpose is to document the invariant that cleanup drains; the actual zeroing is verified by the sequential-test pattern in `TestVaultFixture_NoLeakAcrossTests` from US2, generalised here).

  Verify the file compiles but every test fails (the helpers do not yet exist).

### Implementation for User Story 4

- [X] T010 [P] [US4] Implement the Discord stub primitives in [internal/testutil/discord_stub.go](internal/testutil/discord_stub.go):
  - Declare the `Decision` type as `type Decision int` and the three constants `DecisionApprove = iota`, `DecisionDeny`, `DecisionApproveMute`.
  - Declare the `ApprovalRequest` struct with fields `RequesterHost string`, `Scopes []string`, `SessionType string`, `TTL time.Duration`, `MaxUses int`.
  - Implement `func (r ApprovalRequest) LimitDescription() string` returning `"<TTL>, <MaxUses> uses"` when `MaxUses > 0` and `"<TTL>, TTL-only"` otherwise.
  - Declare the `ApprovalCall` struct with fields `Request ApprovalRequest`, `Decision Decision`, `Err error`, `Index int`.
  - Declare the `Approver` interface with the single method `RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)`.
  - Declare the `DiscordStub` struct with the exported `ApproveAll bool` field and unexported `mu sync.Mutex`, `responses []Decision`, `calls []ApprovalCall`, `t *testing.T` fields.
  - Implement `func NewDiscordStub(t *testing.T) *DiscordStub` — call `t.Helper()`, return `&DiscordStub{t: t}`, register `t.Cleanup(func() { stub.mu.Lock(); defer stub.mu.Unlock(); stub.responses = nil; stub.calls = nil })`.
  - Implement `func (s *DiscordStub) Enqueue(decisions ...Decision)` — lock `mu`, defer unlock, append to `s.responses`.
  - Implement `func (s *DiscordStub) Calls() []ApprovalCall` — lock `mu`, defer unlock, return a defensive copy via `append([]ApprovalCall(nil), s.calls...)`.
  - Implement `func (s *DiscordStub) RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)` per the decision tree in `data-model.md` §4: lock `mu`, defer unlock; if queue non-empty pop head, record call with that decision and `nil` error, return; else if `ApproveAll` record call with `DecisionApprove + nil error` and return; else compute `err = ErrUnexpectedCall`, call `s.t.Errorf("hush/testutil: unexpected Discord approval call: host=%q scopes=%v session=%q limit=%s", req.RequesterHost, req.Scopes, req.SessionType, req.LimitDescription())`, record call with `DecisionDeny + err`, return `(DecisionDeny, err)`.
  - Declare the package-level sentinel `var ErrUnexpectedCall = errors.New("hush/testutil: unexpected approval call")` (allowed by the test-only-globals allowlist documented in `data-model.md` §5).

  Verify all T009 tests pass under `go test -race ./internal/testutil/`.

**Checkpoint**: User Story 4 is fully functional. The Discord approval flow can now be tested end-to-end without network access.

---

## Phase 7: User Story 5 — Leak-Safety Self-Verification (Priority: P1)

**Goal**: Confirm that every fixture this package ships registers `t.Cleanup` automatically and that no state survives between sequential tests in this package. This phase does not add new helpers — it adds a dedicated cross-cutting self-test that exercises all fixtures' leak-safety properties holistically.

**Independent Test**: Two sequential top-level tests in this package — the first invokes every fixture (`NewTestKeys`, `NewTestVault`, `SentinelSecret`, `AssertSentinelAbsent`, `NewDiscordStub`), captures observable state (temp-dir paths, vault-key bytes pre-cleanup, recorded-call counts), allows `t.Cleanup` to fire; the second asserts none of the captured state is observable (paths gone, vault-key `Len() == 0`, no recorded calls in any new stub).

**Depends on**: User Stories 1, 2, 3, 4 (this phase exercises every fixture).

### Tests for User Story 5

- [X] T011 [US5] Author the cross-cutting leak-safety self-test at the bottom of [internal/testutil/vault_fixture_test.go](internal/testutil/vault_fixture_test.go) (or a new file if cleaner) with the following named tests:
  - `TestLeakSafety_AllFixtures_FirstRun` — declare a package-level `var capturedPath string` allowed by the package globals allowlist (or use `t.Setenv`-equivalent test-isolated state); invoke `NewTestKeys(t)`, `NewTestVault(t, ...)`, `NewDiscordStub(t)`; capture the temp-dir path from the vault fixture into the package-level variable; let `t.Cleanup` fire (test exits).
  - `TestLeakSafety_AllFixtures_SecondRun` — read the captured path, assert `os.Stat(capturedPath)` returns `IsNotExist` (the temp dir was removed by the first test's cleanup); instantiate a fresh `NewDiscordStub(t)`, assert `len(stub.Calls()) == 0` (no state survives from the first test); call `NewTestKeys(t)`, assert the returned bytes are byte-identical to a freshly computed reference (the `sync.Once` cache surviving across tests is *intentional and spec-aligned* per Spec SC-001 — this assertion confirms the cache behaves as documented).
  - `TestLeakSafety_NoExplicitCleanupRequired` — invoke `NewTestVault(t, map[string]string{"k": "v"})` and `NewDiscordStub(t)` WITHOUT capturing or invoking the returned cleanups; let `t.Cleanup` fire; assert (in a subsequent line of the same test) that `vaultKey.Len() == 0` (the auto-registered cleanup zeroed the buffer even though the caller never invoked the returned cleanup).

  Verify all tests pass under `go test -race ./internal/testutil/`.

**Checkpoint**: User Story 5 is verified. Leak-safety is a tested property, not just a code-review property.

---

## Phase 8: User Story 6 — Test-Only Enforcement (Priority: P2)

**Goal**: Two-layer enforcement that no production source file imports `internal/testutil`: (a) `golangci-lint`'s `depguard` rule blocks the import at lint-time; (b) a self-test in this package walks the project's `internal/` tree at `go test` time and fails if any non-`*_test.go` file imports the package. Additionally, verify the package contains no `init()` and no unauthorised package-level mutable variables.

**Independent Test**: `magex lint` fails when a fictitious `internal/somepkg/foo.go` (non-test file) is patched to import `internal/testutil`. `go test ./internal/testutil/` fails with the same fictitious patch. Both halves catch the violation.

**Depends on**: User Stories 1–5 (the enforcement self-tests assume the package's full source is in place).

### Tests for User Story 6 (TDD-mandatory) ⚠️

> Write these tests FIRST. They MUST FAIL before the depguard rule is added.

- [X] T012 [P] [US6] Author the test-only-enforcement self-test file at [internal/testutil/doc_test.go](internal/testutil/doc_test.go) (new file) with the package declaration `package testutil` and the following named tests:
  - `TestNoProductionImport` — using `go/parser.ParseDir` walk every directory under `internal/` (relative to the repository root, found via runtime caller path or `os.Getwd` + `..` traversal); for each `.go` file whose name does NOT end with `_test.go`, parse the import block via `parser.ParseFile(fset, path, nil, parser.ImportsOnly)`; fail the test if any production file imports `github.com/mrz1836/hush/internal/testutil` (covers FR-025, SC-021).
  - `TestNoInit` — parse this package's own `.go` files via `parser.ParseDir`; walk the AST for each file; fail if any file declares a function named `init` at package scope (covers FR-024, SC-020).
  - `TestPackageGlobals` — parse this package's own `.go` files; walk top-level `var` declarations; build the set of declared identifier names; assert the set is exactly `{seedOnce, cachedSeed, testPassphrase, testSalt, ErrUnexpectedCall}` (the documented allowlist from `data-model.md` §5); fail on any extra global (covers FR-024 mutable-state invariant).

  Verify all three tests pass under `go test -race ./internal/testutil/` (they should pass against the current implementation; the depguard rule update in T013 is the lint-time half).

### Implementation for User Story 6

- [X] T013 [P] [US6] Update the `depguard` rule in [.golangci.yml](.golangci.yml) (existing config file at repo root) to forbid any non-`*_test.go` Go file from importing `github.com/mrz1836/hush/internal/testutil`. Use the `depguard` linter's `rules.<rule-name>.files` exclusion pattern to scope the deny rule to non-test files only. Verify by running `magex lint` — the rule should pass against the current tree (no production file imports `internal/testutil` yet); manually patch a fictitious `internal/keys/test_violation.go` with the forbidden import, re-run `magex lint`, confirm the lint fails; revert the patch.

**Checkpoint**: User Story 6 is fully enforced. Both halves of the test-only invariant are in place. The package has shipped its complete locked API (12+ exported identifiers per `contracts/testutil-api.md`).

---

## Phase 9: Polish & Cross-Cutting Concerns

**Purpose**: Final gates required before this chunk's combined commit. Per Constitution VIII and the SDD-04 Prompt 4 directive, these MUST run clean.

- [X] T014 Run `magex format:fix` from the repo root. Confirm no formatting diffs remain (`git diff --stat` shows zero changes after the run).
- [X] T015 Run `magex lint` from the repo root. Confirm zero lint findings; in particular confirm the `depguard` rule update from T013 produces no false positives against the current tree.
- [X] T016 Run `magex test:race` from the repo root. Confirm zero test failures and zero data races across the full project (not just `internal/testutil/` — race-detection is a project-wide gate per Constitution VIII).
- [X] T017 Verify coverage ≥ 80% on `internal/testutil/` via `go test -cover ./internal/testutil/`. Confirm the percentage printed at the end of the run is ≥ 80.0%. If lower, identify the uncovered lines via `go test -coverprofile=/tmp/cover.out ./internal/testutil/ && go tool cover -func=/tmp/cover.out` and add targeted self-tests until the bar is met.
- [X] T018 [P] Manual audit: grep `internal/testutil/*_test.go` for `os.CreateTemp` and `ioutil.TempFile`; replace any occurrence with `t.TempDir()`. Confirms the FR-022 invariant that no helper writes outside `t.TempDir()`.
- [X] T019 [P] Append a new `internal/testutil` entry to [docs/PACKAGE-MAP.md](docs/PACKAGE-MAP.md) under the title "**Exported API — locked at SDD-04**" listing the eight chunk-contract symbols (`NewTestVault`, `NewTestKeys`, `SentinelSecret`, `AssertSentinelAbsent`, `DiscordStub`, `NewDiscordStub`, `ApprovalCall`, `Approver`) and noting the four supporting symbols (`Decision` enum + 3 constants, `ApprovalRequest`, `ErrUnexpectedCall`) per the contract's strict-superset rationale. (No AC-MATRIX update — this chunk is indirect support, not an AC owner.)
- [X] T020 [P] Mark SDD-04 status `done` in [docs/SDD-PLAYBOOK.md](docs/SDD-PLAYBOOK.md).
- [X] T021 Create the single combined commit (the IMPLEMENT-phase prompt mandates one combined commit, NOT one per task): stage `internal/testutil/`, `docs/PACKAGE-MAP.md`, `docs/SDD-PLAYBOOK.md`, `specs/004-testutil-fixtures/tasks.md`, and `.golangci.yml` (only if T013 modified it); create the commit with the message `feat(testutil): test fixtures + sentinel helpers + Discord stub (SDD-04)`. Confirm the commit succeeds and `git status` is clean.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies. Start immediately.
- **Foundational (Phase 2)**: None beyond Setup.
- **User Story 1 (Phase 3)**: Depends on Setup. The MVP — the deterministic seed cache feeds US2.
- **User Story 2 (Phase 4)**: Depends on User Story 1 (vault key derivation reuses the cached master seed via `keys.DeriveVaultEncKey`).
- **User Story 3 (Phase 5)**: Depends on Setup only. Independent of US1, US2, US4.
- **User Story 4 (Phase 6)**: Depends on Setup only. Independent of US1, US2, US3.
- **User Story 5 (Phase 7)**: Depends on User Stories 1, 2, 3, 4 (the leak-safety self-test exercises every fixture).
- **User Story 6 (Phase 8)**: Depends on User Stories 1–5 (the enforcement self-tests assume the full source is in place).
- **Polish (Phase 9)**: Depends on every preceding phase.

### Within Each User Story

- Self-tests MUST be authored AND fail BEFORE the helper-implementation task lands (TDD-mandatory per Constitution VIII).
- Each story's helper file is self-contained; cross-file compilation needs every preceding story's file to be in place.

### Parallel Opportunities

- **Within US3 (Phase 5)**: T007 (sentinel test file) and T008 (sentinel implementation) are sequential (TDD), but the entire phase runs in parallel with US4 (Phase 6) since the two stories touch different files (`sentinel.go` vs. `discord_stub.go`).
- **Within US4 (Phase 6)**: same — runs in parallel with US3.
- **Within US6 (Phase 8)**: T012 (self-test) and T013 (depguard config) touch different files and have no inter-dependency; both can proceed in parallel after US1–US5 land.
- **Within Polish (Phase 9)**: T018, T019, T020 touch independent files and can be batched. T014 → T015 → T016 → T017 are sequential (each depends on the previous gate passing). T021 is the final action.

---

## Parallel Example: User Stories 3 and 4

```bash
# Once US1 + US2 are complete, US3 and US4 are independent — work them in parallel:

# Terminal A — User Story 3 (sentinel infrastructure):
Task: "Author T007 self-test file at internal/testutil/sentinel_test.go"
# (verify all tests fail under `go test ./internal/testutil/`)
Task: "Implement T008 sentinel.go"
# (verify all T007 tests now pass)

# Terminal B — User Story 4 (Discord stub):
Task: "Author T009 self-test file at internal/testutil/discord_stub_test.go"
# (verify all tests fail under `go test ./internal/testutil/`)
Task: "Implement T010 discord_stub.go"
# (verify all T009 tests now pass)
```

Both stories merge cleanly into US5 (Phase 7) once their respective phases close.

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001–T002).
2. Complete Phase 3: User Story 1 (T003 self-tests → T004 implementation).
3. **STOP and VALIDATE**: Run `go test -race ./internal/testutil/`. Confirm `NewTestKeys` round-trips, race-clean, deterministic. The MVP is the deterministic-seed primitive — every other helper builds on it (US2) or is independently testable (US3, US4).

### Incremental Delivery

1. **MVP** (US1): deterministic test keys, race-clean, memoised. Downstream chunks blocked on master-seed material can already use this.
2. **US2** (vault fixture): unblocks every downstream test that needs a real on-disk vault.
3. **US3** (sentinel infrastructure): unblocks every redaction / no-leak test.
4. **US4** (Discord stub): unblocks every approval-flow test (request handler, supervisor, watchdog).
5. **US5** (leak-safety verification): formalises the leak-safety property as a tested invariant.
6. **US6** (test-only enforcement): closes the constitutional invariant (Principle I).
7. **Polish** (Phase 9): final gates and combined commit.

### Parallel Team Strategy

With multiple workers, the parallel opportunities are:

1. One worker takes US1 (Phase 3) — the MVP — while another sets up the depguard rule sketch for US6.
2. Once US1 lands, two workers pick up US3 (Phase 5) and US4 (Phase 6) in parallel — they touch different files and have no inter-dependency.
3. A third worker takes US2 (Phase 4) once US1 lands.
4. US5 and US6 wait until US1–US4 are merged.
5. Polish (Phase 9) is single-worker because the gates are sequential.

---

## Notes

- **TDD discipline**: every helper-implementation task (T004, T006, T008, T010, T013) is preceded by a self-test task (T003, T005, T007, T009, T012). The self-tests MUST FAIL before the implementation lands. Reviewers should look for evidence (commit history or test output) that the tests were written first.
- **Coverage target**: ≥80% on `internal/testutil/`, verified by T017. The relaxed bar (vs. the project-wide 100% on security-critical packages) is licensed by the SDD-04 chunk contract because helpers without external surface area are exercised by the consuming tests downstream.
- **Final commit**: T021 creates ONE combined commit, not one per task. The SDD-04 IMPLEMENT-phase prompt mandates this — defer all commits to the end.
- **No AC-MATRIX update**: SDD-04 contributes to AC-9 indirectly (test infrastructure backing 100%-coverage targets elsewhere). Per the chunk contract, do NOT modify `docs/AC-MATRIX.md` in this chunk.
- **Race detection is project-wide**: T016's `magex test:race` runs the full test suite, not just `internal/testutil/`. A race introduced anywhere in the project would fail this gate.
- **Documented `sync.Once` exception**: the `seedOnce` + `cachedSeed` package-level variables (declared in T004) carry a `//nolint:gochecknoglobals` justification comment. Reviewers verifying Constitution IX compliance should expect to see exactly that exception and no other.
- **Sub-`testing.T` capture pattern (T009 `TestDiscordStub_UnexpectedCall` and T007 `TestAssertSentinelAbsent_Present`)**: pick the project's idiom — either a `t.Run` subtest whose `Failed()` is checked from the outer test, OR a custom recorder that wraps `*testing.T`. The `data-model.md` does not mandate either; whichever yields a cleaner self-test under `-race` is acceptable.
- **Avoid**: generating files outside `t.TempDir()` (FR-022); shipping a fixture that returns a cleanup but does NOT register it (FR-007); silently default-denying in the Discord stub (clarification 2 / FR-018a); skipping `t.Helper()` (loses caller-line attribution).
