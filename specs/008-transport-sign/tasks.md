---

description: "Tasks for SDD-08 — internal/transport/sign (canonical-JSON sign/verify + nonce + timestamp)"
---

# Tasks: `internal/transport/sign` — Canonical-JSON Sign/Verify + Nonce + Timestamp (SDD-08)

**Input**: Design documents from `/specs/008-transport-sign/`
**Prerequisites**: plan.md (loaded), spec.md (loaded), research.md (loaded), data-model.md (loaded), contracts/api.md (loaded), quickstart.md (loaded)
**Chunk contract**: [docs/sdd/SDD-08.md](../../docs/sdd/SDD-08.md)

**Tests**: TDD-MANDATORY per Constitution VIII. Every behaviour-contract task has a test-writing task scheduled BEFORE it; tests MUST be written first and MUST FAIL before the corresponding implementation task is run.

**Coverage target**: 100% (`go test -cover ./internal/transport/sign/` ≥ 100%, Constitution VIII Critical band — request signing).

**Fuzz target**: `FuzzVerifyRequest` ≥ 60 s clean (Constitution VIII fuzz target #4).

**Organization**: Tasks are grouped by user story (per spec.md priorities P1/P2). Each phase is independently testable.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: User story label (US1, US2, US3, US4, US5, US6); setup/foundational/polish carry no story label
- All file paths are absolute from repo root

## Path Conventions

- **Single Go module**: `github.com/mrz1836/hush`. Production sources under `internal/transport/sign/`. Tests colocated as `*_test.go`. Fuzz seed corpus under `internal/transport/sign/testdata/fuzz/FuzzVerifyRequest/`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the package skeleton and the declarative scaffolding (sentinels, package doc) that every later phase depends on.

- [X] T001 Create the package directory `internal/transport/sign/` with the package-doc file `internal/transport/sign/doc.go` containing the package comment, the Constitution III/VIII/IX/X/XI citations, and the Layer-4-roster pointer (per [plan.md §Project Structure](./plan.md))
- [X] T002 [P] Create `internal/transport/sign/errors.go` declaring the six sentinels (`ErrSignatureInvalid`, `ErrNonceReplay`, `ErrTimestampStale`, `ErrCanonicalUnsupported`, `ErrNonceEncoding`, `ErrNonceTTLInvalid`) per [research R-006](./research.md#r-006--sentinel-error-catalogue-and-wrap-relationships) — all `var ErrXxx = errors.New("hush/transport/sign: <message>")`, no wrap relationships
- [X] T003 [P] Create `internal/transport/sign/testdata/fuzz/FuzzVerifyRequest/` directory and add the four seed-corpus files described in [research R-007](./research.md#r-007--fuzz-target-shape): `empty-payload-empty-sig`, `valid-shape-wrong-key`, `truncated-der`, `garbage-bytes`

**Checkpoint**: Package compiles (`go build ./internal/transport/sign/`); sentinels exist; fuzz seed corpus present.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Add the test seams (`nowFn`, `setClockForTest`, `advanceClockForTest`, `generateFuzzKey`) and the `RawMessage` type / `rawMessageType` reflect sentinel that every later test and implementation phase consumes.

**⚠️ CRITICAL**: No user-story phase may begin until Phase 2 is complete — the test seams and the `RawMessage` type are foundational for every test file.

- [X] T004 Declare the package-private `nowFn = time.Now` clock indirection in `internal/transport/sign/timestamp.go` (with `//nolint:gochecknoglobals` and the sentinel-class precedent comment per [research R-005](./research.md#r-005--isfreshtimestamp-pure-function-with-injectable-clock))
- [X] T005 [P] Declare the exported `RawMessage` named-byte-slice type and the package-private `rawMessageType` reflect sentinel in `internal/transport/sign/canonical.go` per [research R-002a](./research.md#r-002a--rawmessage-escape-hatch-type)
- [X] T006 [P] Create `internal/transport/sign/testutil_test.go` with the test-only helpers: `setClockForTest(t *testing.T, fixed time.Time)`, `advanceClockForTest(t *testing.T, by time.Duration)`, and `generateFuzzKey(tb testing.TB) *ecdsa.PrivateKey` per [research R-011](./research.md#r-011--test-only-clock-swap-helper-discipline)

**Checkpoint**: Foundation ready — every later test can construct a deterministic key and a frozen clock; every later implementation can refer to `nowFn` and `RawMessage`.

---

## Phase 3: User Story 3 — Canonical Encoding Determinism (Priority: P1)

**Goal**: A canonical JSON encoder produces byte-identical output for any two semantically-equal inputs regardless of map iteration order — at every depth — and rejects every value that cannot be represented in standard JSON with a typed sentinel and no partial output.

**Independent Test**: Construct two structurally distinct in-memory values that represent the same logical payload (different map iteration orders, including at nested depth). Run both through `CanonicalJSON`. Assert byte-identical output. Repeat across nested levels and unicode-edge strings.

**Why first**: Sign + Verify both consume canonical bytes; the chunk's load-bearing determinism property must exist before any signature contract can be exercised.

### Tests for User Story 3 (write FIRST, ensure FAIL before implementation) ⚠️

- [X] T007 [P] [US3] Write `TestCanonical_SortsAtAllDepths` (10 known shapes per [research R-008](./research.md#r-008--test-corpus-discipline-canonical-encoder-shapes)) as a table-driven test in `internal/transport/sign/canonical_test.go` — each entry pairs a Go input value with its expected canonical bytes, asserted via `bytes.Equal`
- [X] T008 [P] [US3] Write `TestCanonical_RejectsNaN` and `TestCanonical_RejectsInf` (covering `+Inf` and `-Inf`) in `internal/transport/sign/canonical_test.go` — assert `errors.Is(err, ErrCanonicalUnsupported)` and that the returned byte slice is `nil`
- [X] T009 [P] [US3] Write `TestCanonical_StructAndMap` in `internal/transport/sign/canonical_test.go` exercising **both** chunk-contract gotcha cases: (a) `struct{ B int; A int }{B: 1, A: 2}` MUST emit `{"A":2,"B":1}` (alphabetical, NOT declaration order — the stdlib gotcha); (b) `map[string]any{"outer": map[string]any{"y": 2, "x": 1}}` MUST sort the inner map (the nested-depth gotcha)
- [X] T010 [P] [US3] Write `TestCanonical_RejectsFunc`, `TestCanonical_RejectsChan`, `TestCanonical_RejectsComplex`, and `TestCanonical_RejectsNonStringMap` in `internal/transport/sign/canonical_test.go` — each asserts `errors.Is(err, ErrCanonicalUnsupported)` and `nil` byte output
- [X] T011 [P] [US3] Write `TestCanonical_IgnoresMarshalJSON` in `internal/transport/sign/canonical_test.go` — define a local type whose `MarshalJSON` returns `[]byte("\"hijacked\"")` and assert the canonical output ignores the hook (FR-022 anti-feature)
- [X] T012 [P] [US3] Write `TestCanonical_EmbedsRawMessageVerbatim` in `internal/transport/sign/canonical_test.go` — assert that `RawMessage([]byte("[1,2,3]"))` is inserted verbatim and that `RawMessage(nil)` / zero-length emits `null`

### Implementation for User Story 3

- [X] T013 [US3] Implement `CanonicalJSON(v any) ([]byte, error)` in `internal/transport/sign/canonical.go` as a recursive `encodeValue(buf *bytes.Buffer, v reflect.Value) error` walker per [research R-001](./research.md#r-001--canonicaljson-recursive-reflect-walker-that-sorts-at-every-depth) — handling `Pointer`/`Interface`, `Bool`, `Int*`, `Uint*`, `Float*` (with `math.IsNaN` / `math.IsInf` rejection), `String` (via `json.Marshal(v.String())` for correct escaping), `Slice`/`Array` (with the `RawMessage` precedence check), `Map` (string-keyed only; `sort.Strings` on collected keys), `Struct` (alphabetised by resolved field name; `json:"name"` honoured for renaming, options ignored). Reject `Func`/`Chan`/`UnsafePointer`/`Complex64`/`Complex128`/`Invalid` with `ErrCanonicalUnsupported`. On error: discard the buffer, return `nil, err` (no partial output).

**Checkpoint**: All US3 tests pass. `go test -run TestCanonical ./internal/transport/sign/` is green; `go test -cover -run TestCanonical ./internal/transport/sign/` shows the canonical encoder fully covered.

---

## Phase 4: User Story 1 — Signed Request Round Trip (Priority: P1)

**Goal**: A signing operation takes a registered private key and a canonical-bytes payload, returns an ECDSA signature over `SHA-256(payload)`. A verification operation takes the matching public key, the same payload, and the signature; returns `nil` on success and `ErrSignatureInvalid` for every failure mode (wrong key, tampered payload, malformed DER).

**Independent Test**: Generate a fresh secp256k1 keypair via `internal/keys`. Compute `CanonicalJSON` of a typed payload. Sign with the private key; verify with the matching public key — `nil` returned. Repeat with a tampered payload and assert `errors.Is(err, ErrSignatureInvalid)`. Repeat with a wrong public key and assert the same sentinel.

**Dependencies on previous stories**: Phase 3 (US3) — `CanonicalJSON` is the input to Sign / Verify in every test.

### Tests for User Story 1 (write FIRST) ⚠️

- [X] T014 [P] [US1] Write `TestSign_VerifyRoundTrip` in `internal/transport/sign/sign_test.go` — generate a fresh key via `generateFuzzKey`, canonicalise a typed payload, call `Sign(ctx, priv, canonical)`, then `Verify(ctx, &priv.PublicKey, canonical, sig)`; assert `nil` error
- [X] T015 [P] [US1] Write `TestVerify_WrongKeyFails` in `internal/transport/sign/verify_test.go` — sign with key A, verify with key B's public; assert `errors.Is(err, ErrSignatureInvalid)`
- [X] T016 [P] [US1] Write `TestVerify_TamperedPayloadFails` in `internal/transport/sign/verify_test.go` — round-trip then mutate one byte of the canonical payload before verify; assert `errors.Is(err, ErrSignatureInvalid)`
- [X] T017 [P] [US1] Write `TestVerify_MalformedDERFails` in `internal/transport/sign/verify_test.go` — verify with a truncated 16-byte DER signature; assert `errors.Is(err, ErrSignatureInvalid)` (panic-free per FR-016)
- [X] T018 [P] [US1] Write `TestSign_RespectsCancelledContext` and `TestVerify_RespectsCancelledContext` in `internal/transport/sign/sign_test.go` and `internal/transport/sign/verify_test.go` — pre-cancel the context; assert `errors.Is(err, context.Canceled)`
- [X] T019 [P] [US1] Write `TestVerify_NoLeakOnFuzzInput` in `internal/transport/sign/verify_test.go` per [research R-012](./research.md#r-012--logging--redaction) — capture `slog.NewJSONHandler` output across 1000 random failed verifications and assert that no nonce, signature byte, or payload byte appears in the captured buffer (FR-017)
- [X] T020 [P] [US1] Write `FuzzVerifyRequest` in `internal/transport/sign/verify_fuzz_test.go` per [research R-007](./research.md#r-007--fuzz-target-shape) — fixed test-derived public key from `generateFuzzKey`; `f.Fuzz(func(t *testing.T, payload, sig []byte))` calls `Verify` and asserts: (a) no panic, (b) every error is `errors.Is(err, ErrSignatureInvalid)` or `errors.Is(err, context.Canceled)` — never another type. The seed corpus from T003 is loaded automatically by Go's fuzz harness.

### Implementation for User Story 1

- [X] T021 [US1] Implement `Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error)` in `internal/transport/sign/sign.go` per [research R-002](./research.md#r-002--sign--verify-stdlib-cryptoecdsasignasn1-substitution) — `ctx.Err()` early-out, `sha256.Sum256(payload)`, `ecdsa.SignASN1(rand.Reader, key, digest[:])`
- [X] T022 [US1] Implement `Verify(ctx context.Context, key *ecdsa.PublicKey, payload, sig []byte) error` in `internal/transport/sign/verify.go` per [research R-002](./research.md#r-002--sign--verify-stdlib-cryptoecdsasignasn1-substitution) — `ctx.Err()` early-out, `sha256.Sum256(payload)`, return `nil` on `ecdsa.VerifyASN1` true, return `fmt.Errorf("hush/transport/sign: verify: %w", ErrSignatureInvalid)` on false; **panic-free under any byte input**

**Checkpoint**: `TestSign_VerifyRoundTrip`, `TestVerify_WrongKeyFails`, `TestVerify_TamperedPayloadFails`, `TestVerify_MalformedDERFails` all green. The fuzz target compiles and a short `-fuzztime=2s` smoke run is panic-free (the full 60 s gate runs in Phase 9).

---

## Phase 5: User Story 2 — Replay Defence (Nonce Cache + Timestamp Freshness) (Priority: P1)

**Goal**: A nonce cache rejects re-submitted nonces within their TTL with `ErrNonceReplay`; allows them again after the TTL elapses (sweep- or CAS-mediated). A timestamp freshness check rejects timestamps outside a configurable symmetric skew window in either direction (consumer maps `false` to `ErrTimestampStale`). Both encoding (8 ≤ len ≤ 128) and TTL positivity are pre-cache-lookup gates with their own typed sentinels.

**Independent Test**: Build a `NonceCache`. Add a fresh nonce — `firstSeen=true`. Add the same nonce again — `firstSeen=false`, `ErrNonceReplay`. Advance the clock past TTL, run sweep, add again — `firstSeen=true`. For timestamps: assert `IsFreshTimestamp(now, skew)=true`, `IsFreshTimestamp(now-2*skew, skew)=false`, `IsFreshTimestamp(now+2*skew, skew)=false`.

**Dependencies on previous stories**: Phase 2 (foundational `nowFn` + `setClockForTest`); does NOT depend on US1 or US3 — the nonce cache and the timestamp primitive are independent of canonicalisation and signing.

### Tests for User Story 2 (write FIRST) ⚠️

#### Nonce-cache encoding/TTL gates

- [X] T023 [P] [US2] Write `TestNonce_AddNewReturnsFirstSeen` in `internal/transport/sign/nonce_test.go` — fresh cache, valid 32-byte nonce, valid TTL; assert `(true, nil)`
- [X] T024 [P] [US2] Write `TestNonce_AddDuplicateReturnsReplay` in `internal/transport/sign/nonce_test.go` — Add the same nonce twice in immediate succession; assert second call returns `(false, err)` with `errors.Is(err, ErrNonceReplay)`
- [X] T025 [P] [US2] Write `TestNonce_AddEmptyReturnsEncodingError`, `TestNonce_AddTooShortReturnsEncodingError`, and `TestNonce_AddTooLongReturnsEncodingError` (lengths 0, 7, 129) in `internal/transport/sign/nonce_test.go` — each asserts `errors.Is(err, ErrNonceEncoding)` BEFORE any cache lookup occurs (must NOT collapse into `ErrNonceReplay`)
- [X] T026 [P] [US2] Write `TestNonce_AddNonPositiveTTLReturnsInvalid` in `internal/transport/sign/nonce_test.go` — TTLs of `0`, `-1ns`, `-1h`; assert `errors.Is(err, ErrNonceTTLInvalid)`
- [X] T027 [P] [US2] Write `TestNonce_AddRespectsCancelledContext` in `internal/transport/sign/nonce_test.go` — pre-cancelled ctx; assert `errors.Is(err, context.Canceled)` returned BEFORE encoding/TTL gates fire

#### Nonce-cache TTL elapsed semantics

- [X] T028 [P] [US2] Write `TestNonce_ExpiredAllowedAfterSweep` in `internal/transport/sign/nonce_test.go` — Add nonce with short TTL, advance the frozen clock past TTL via `advanceClockForTest`, trigger a sweep (or rely on the lazy CAS path documented in [data-model.md](./data-model.md#addctx-nonce-ttl-firstseen-bool-err-error)); assert second `Add` returns `(true, nil)`

#### Timestamp freshness primitive

- [X] T029 [P] [US2] Write `TestTimestamp_FreshAccepted` in `internal/transport/sign/timestamp_test.go` — frozen clock at `t0`; assert `IsFreshTimestamp(t0, 30s) == true` and `IsFreshTimestamp(t0.Add(-29*time.Second), 30s) == true`
- [X] T030 [P] [US2] Write `TestTimestamp_TooOldRejected` in `internal/transport/sign/timestamp_test.go` — frozen clock at `t0`; assert `IsFreshTimestamp(t0.Add(-31*time.Second), 30s) == false`
- [X] T031 [P] [US2] Write `TestTimestamp_FutureSkewRejected` in `internal/transport/sign/timestamp_test.go` — frozen clock at `t0`; assert `IsFreshTimestamp(t0.Add(31*time.Second), 30s) == false`
- [X] T032 [P] [US2] Write `TestTimestamp_BoundaryAccepted` in `internal/transport/sign/timestamp_test.go` — `delta == skew` is the documented `<=` boundary; assert `IsFreshTimestamp(t0.Add(30*time.Second), 30s) == true` AND `IsFreshTimestamp(t0.Add(-30*time.Second), 30s) == true`
- [X] T033 [P] [US2] Write `TestTimestamp_NonPositiveSkewRejected` in `internal/transport/sign/timestamp_test.go` — assert `IsFreshTimestamp(t0, 0) == false` and `IsFreshTimestamp(t0, -1*time.Second) == false` (FR-012 positive-window guard)

### Implementation for User Story 2

- [X] T034 [US2] Implement `IsFreshTimestamp(ts time.Time, skew time.Duration) bool` in `internal/transport/sign/timestamp.go` per [research R-005](./research.md#r-005--isfreshtimestamp-pure-function-with-injectable-clock) — non-positive skew returns `false` unconditionally; otherwise `delta = abs(nowFn().Sub(ts))`, return `delta <= skew`
- [X] T035 [US2] Implement `NonceCache` interface, `nonceCache` struct (with `entries sync.Map` + `sweepInterval time.Duration` defaulting to 30 s), `NewNonceCache()`, and `Add(ctx, nonce, ttl) (bool, error)` in `internal/transport/sign/nonce.go` per [research R-003](./research.md#r-003--noncecache-syncmap-with-loadorstore--compareandswap) — three deterministic phases: ctx-err → encoding/TTL gates → `LoadOrStore` (with `CompareAndSwap` lazy expired-reuse path)

**Checkpoint**: All US2 tests pass without a sweep goroutine. `go test -run "TestNonce|TestTimestamp" ./internal/transport/sign/` is green.

---

## Phase 6: User Story 4 — Run Lifecycle (Explicit Goroutine Ownership) (Priority: P1)

**Goal**: `NewNonceCache()` spawns zero goroutines. `Run(ctx)` is synchronous — the caller invokes `go cache.Run(ctx)`; the call blocks until `ctx.Done()` and emits exactly one "stopped" `slog.Info` line on exit. `Add` works before `Run` is invoked.

**Independent Test**: Construct a cache; assert no new goroutine spawned (compare `runtime.NumGoroutine` before / after `NewNonceCache`). Invoke `go cache.Run(ctx)`; assert exactly one new goroutine. Cancel ctx; assert the goroutine count returns to baseline within one sweep interval; assert exactly one `slog.Info` "stopped" line was emitted.

**Dependencies on previous stories**: Phase 5 (US2) — `Run` operates on the same `nonceCache` struct and `entries sync.Map` declared by US2.

### Tests for User Story 4 (write FIRST) ⚠️

- [X] T036 [P] [US4] Write `TestNewNonceCache_NoGoroutineSpawned` in `internal/transport/sign/nonce_test.go` — capture `runtime.NumGoroutine()` before and after `NewNonceCache()`; assert delta is 0 (FR-009)
- [X] T037 [P] [US4] Write `TestNonceCache_RunStopsOnContextCancel` in `internal/transport/sign/nonce_test.go` — construct cache via the test-only short-interval helper (`sweepInterval = 10ms`); `go cache.Run(ctx)`; cancel `ctx`; assert Run returns within 100 ms and `runtime.NumGoroutine()` returns to baseline
- [X] T038 [P] [US4] Write `TestNonceCache_RunLogsStoppedOnce` in `internal/transport/sign/nonce_test.go` — install a `slog.NewJSONHandler(buf)` test logger via `slog.SetDefault`; run + cancel; assert exactly one log line whose message is `"hush/transport/sign: nonce cache sweep stopped"` and whose only attribute is `reason=<ctx.Err().Error()>` (no nonces, no count, no per-key data per FR-017)
- [X] T039 [P] [US4] Write `TestNonceCache_AddWorksWithoutRun` in `internal/transport/sign/nonce_test.go` — construct cache, do NOT invoke Run, perform Add operations, assert all succeed and the entries are tracked correctly (User Story 4 acceptance scenario 3)
- [X] T040 [P] [US4] Write `TestNonceCache_SweepRemovesExpired` in `internal/transport/sign/nonce_test.go` — Add nonce with short TTL, start Run with 10 ms sweep interval, advance the frozen clock past TTL, wait one sweep interval, assert the entry has been deleted (via re-Adding the same nonce returning `firstSeen=true`)

### Implementation for User Story 4

- [X] T041 [US4] Implement `Run(ctx context.Context)` and the package-private `sweep()` method on `*nonceCache` in `internal/transport/sign/nonce.go` per [research R-004](./research.md#r-004--noncecacherun-synchronous-ticker-loop-owned-by-the-caller) — `time.NewTicker(c.sweepInterval)` + `defer t.Stop()` + `for { select { case <-ctx.Done(): slog.Info("...stopped...", slog.String("reason", ctx.Err().Error())); return; case <-t.C: c.sweep() } }`. The sweep is `c.entries.Range` with `CompareAndDelete(k, observedExpiry)` for entries whose stored expiry is in the past.
- [X] T042 [US4] Add a test-only helper `newNonceCacheForTest(sweepInterval time.Duration)` to `internal/transport/sign/testutil_test.go` (NOT exported — `_test.go`-gated) so the lifecycle tests can use a sub-second sweep interval

**Checkpoint**: All US4 tests pass. The package's only goroutine is the caller-owned Run; `Run` exit is logged exactly once with `reason` only.

---

## Phase 7: User Story 5 — Concurrent Add Race-Clean Exactly-One-Winner (Priority: P1)

**Goal**: Under N concurrent goroutines presenting the same nonce, exactly one observes `firstSeen=true` and N-1 observe `firstSeen=false` with `ErrNonceReplay`. Under N concurrent goroutines presenting N distinct nonces, every goroutine observes `firstSeen=true`. The race detector reports zero data races on either test.

**Independent Test**: Launch N=128 goroutines all calling `cache.Add(ctx, sharedNonce, ttl)` concurrently. Count the number that observe `firstSeen=true`. Assert exactly 1. Run under `-race`. Repeat with N distinct nonces; assert N "first seen" outcomes.

**Dependencies on previous stories**: Phase 5 (US2) — `Add` is the function under test; the implementation must already use `LoadOrStore` + `CompareAndSwap` per [research R-003](./research.md#r-003--noncecache-syncmap-with-loadorstore--compareandswap).

### Tests for User Story 5 (write FIRST) ⚠️

- [X] T043 [P] [US5] Write `TestNonceCache_ConcurrentAdd` in `internal/transport/sign/nonce_test.go` — N=128 goroutines (or higher), shared nonce, shared cache, collect outcomes via `sync/atomic` counter; assert the counter of `firstSeen=true` is exactly 1 and that all other goroutines returned `errors.Is(err, ErrNonceReplay)`. The test MUST be race-clean under `magex test:race`.
- [X] T044 [P] [US5] Write `TestNonceCache_ConcurrentDistinct` in `internal/transport/sign/nonce_test.go` — N goroutines each Add a distinct unique nonce; assert all N observe `firstSeen=true` and the cache contains all N entries (FR-008 second leg)

### Implementation for User Story 5

- [X] T045 [US5] Verify the `Add` implementation from T035 satisfies the exactly-one-winner contract under `-race`. If `TestNonceCache_ConcurrentAdd` reports a race or a non-1 winner count, refine the `LoadOrStore` + `CompareAndSwap` ordering per [research R-003](./research.md#r-003--noncecache-syncmap-with-loadorstore--compareandswap). No new file — refinement is to `internal/transport/sign/nonce.go`.

**Checkpoint**: `magex test:race` reports zero races; `TestNonceCache_ConcurrentAdd` consistently passes with exactly 1 winner across repeated runs.

---

## Phase 8: User Story 6 — Sentinel Error Identity (Priority: P2)

**Goal**: Each rejection category is a distinct, comparable `errors.Is`-matchable sentinel. Callers identify rejection by sentinel identity, not string parsing. The composition recipe (signature → timestamp → nonce-Add) is documented in `contracts/api.md` and verified end-to-end (the integration test for the recipe is scheduled for SDD-12; SDD-08 verifies sentinel distinctness within this package).

**Independent Test**: For each defined rejection category, build a request that triggers exactly that category. Assert the returned error is identifiable as the corresponding named sentinel via `errors.Is`. Assert no two sentinels match the same `errors.Is` for a given error.

**Dependencies on previous stories**: Phases 3–7 (all sentinels exist and all paths return them).

### Tests for User Story 6 (write FIRST) ⚠️

- [X] T046 [P] [US6] Write `TestSentinels_AreDistinct` in `internal/transport/sign/errors_test.go` — for each pair `(ErrA, ErrB)` where `ErrA != ErrB`, assert `!errors.Is(ErrA, ErrB)` and `!errors.Is(ErrB, ErrA)` (i.e., no wrap relationships exist between sentinels per [research R-006](./research.md#r-006--sentinel-error-catalogue-and-wrap-relationships))
- [X] T047 [P] [US6] Write `TestSentinels_MessagePrefix` in `internal/transport/sign/errors_test.go` — each sentinel's `.Error()` string starts with `"hush/transport/sign: "` (catalog-locality discipline)

### Implementation for User Story 6

- [X] T048 [US6] No new code — confirm via the existing tests (T015, T024, T025, T026, T030, T031, T032, T046, T047) that every documented rejection category surfaces its own sentinel and that none collapse. If any test reveals a sentinel-collapse defect, fix the producing function (no new sentinels — the catalogue is locked).

**Checkpoint**: The six sentinels are independent, distinct, comparable. `TestSentinels_*` green.

---

## Phase 9: Polish, Gates & Combined Commit

**Purpose**: Run the constitutional gate suite, verify coverage and fuzz, update sibling docs, and create the single combined commit per the SDD-08 implement-phase release-step list. NO commits are made between phases — all changes land in one commit at the end of Phase 9 per [docs/sdd/SDD-08.md](../../docs/sdd/SDD-08.md) Prompt 5.

### Gate suite (each MUST pass clean)

- [X] T049 Run `magex format:fix` from the repo root — the package source files MUST be gofmt/goimports-clean
- [X] T050 Run `magex lint` from the repo root — golangci-lint MUST be clean for `internal/transport/sign/...` (no new warnings introduced)
- [X] T051 Run `magex test:race` from the repo root — full test suite race-clean, with particular attention to `TestNonceCache_ConcurrentAdd` and `TestNonceCache_RunStopsOnContextCancel`
- [X] T052 Run `go test -fuzz=FuzzVerifyRequest -fuzztime=60s ./internal/transport/sign/` — MUST produce no panic, no new entries in `testdata/fuzz/FuzzVerifyRequest/`, and every error a typed sentinel (Constitution VIII fuzz target #4)
- [X] T053 Run `go test -cover ./internal/transport/sign/` — coverage MUST be 100% (Constitution VIII Critical band — request signing). If <100%, identify the uncovered branches via `go test -coverprofile=cover.out ./internal/transport/sign/` + `go tool cover -html=cover.out` and add the missing tests before proceeding.

### Cross-document validation

- [X] T054 Manually compare the canonical-output of the example payload from [docs/SECURITY.md](../../docs/SECURITY.md) Layer 4 against the bytes produced by `CanonicalJSON` — capture a snapshot in a one-off `go run` or test, assert byte-equality with the documented example
- [X] T055 Confirm `TestNonceCache_ConcurrentAdd` is race-clean across at least 5 consecutive `magex test:race` runs and that the `firstSeen=true` count is exactly 1 each time

### Documentation updates

- [X] T056 Append a new section "## `internal/transport/sign` — Exported API (locked at SDD-08)" to `docs/PACKAGE-MAP.md` listing the locked surface from [contracts/api.md §Exported types/functions](./contracts/api.md): `RawMessage`, `NonceCache` interface, `CanonicalJSON`, `Sign`, `Verify`, `NewNonceCache`, `IsFreshTimestamp`, and the six sentinel `var Err...` declarations. Cite the contract document.
- [X] T057 Update the AC-7 row in `docs/AC-MATRIX.md` (Layer 4 — request signing + replay protection) with the new test file paths: `internal/transport/sign/canonical_test.go`, `internal/transport/sign/sign_test.go`, `internal/transport/sign/verify_test.go`, `internal/transport/sign/nonce_test.go`, `internal/transport/sign/timestamp_test.go`, `internal/transport/sign/errors_test.go`, `internal/transport/sign/verify_fuzz_test.go`. Mark the row's status to reflect the SDD-08 implementation landing.
- [X] T058 Mark SDD-08 status `done` in `docs/SDD-PLAYBOOK.md`

### Combined commit

- [X] T059 Stage and create the single combined commit per [docs/sdd/SDD-08.md](../../docs/sdd/SDD-08.md) Prompt 5:
  ```
  git add internal/transport/sign/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/008-transport-sign/tasks.md
  git commit -m "feat(transport/sign): canonical-JSON sign/verify + nonce + timestamp (SDD-08)"
  ```

**Final acknowledgement**: gates passed, fuzz 60 s clean, coverage = 100%, canonicalisation matches `docs/SECURITY.md` Layer 4 example, nonce cache race-clean with exactly-one `firstSeen=true`, AC-7 row updated, SDD-PLAYBOOK updated, single combined commit created.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: no dependencies — start immediately
- **Phase 2 (Foundational)**: depends on Phase 1 — BLOCKS every user-story phase
- **Phase 3 (US3 — Canonical)**: depends on Phase 2; US1 (Phase 4) depends on US3
- **Phase 4 (US1 — Sign/Verify)**: depends on Phase 3 (canonical bytes are the input); does NOT depend on US2/US4/US5
- **Phase 5 (US2 — Nonce + Timestamp)**: depends on Phase 2; INDEPENDENT of US3/US1 (the nonce cache and timestamp primitive operate on opaque inputs)
- **Phase 6 (US4 — Run lifecycle)**: depends on Phase 5 (Run operates on the nonceCache struct from US2)
- **Phase 7 (US5 — Concurrent Add)**: depends on Phase 5 (Add must exist) and ideally Phase 6 (so the test can exercise the long-running Run)
- **Phase 8 (US6 — Sentinel identity)**: depends on Phases 3–7 (every sentinel-producing path exists)
- **Phase 9 (Polish)**: depends on every prior phase — the gates can only run on a complete package

### User Story Dependencies (within Phase 3+)

- **US3 (P1, canonical)**: independent, no upstream story
- **US1 (P1, sign/verify)**: depends on US3 (canonical bytes are the Sign input)
- **US2 (P1, replay defence primitives)**: independent of US3/US1 — the nonce cache and timestamp primitive do NOT consume canonical bytes; they are leaf primitives the consumer composes with US3 + US1 outputs
- **US4 (P1, Run lifecycle)**: depends on US2 (Run is a method on `*nonceCache`)
- **US5 (P1, concurrent Add race)**: depends on US2 (Add is the function under test)
- **US6 (P2, sentinel identity)**: depends on every prior story (every sentinel path must exist)

### Within Each User Story

- Tests MUST be written and MUST FAIL before the corresponding implementation task is run (Constitution VIII TDD-mandatory)
- Test files for a story marked `[P]` can run in parallel (different test functions in the same file are written by separate edits but to one file — coordinate or write sequentially; tasks marked `[P]` against the same file means "no inter-task dependency on other-task content", not "different files")
- Implementation tasks within a single file (e.g., T021 / T022 both in `verify.go` — none here) are sequential; tasks across files within a story are parallelisable

### Parallel Opportunities

- All Phase 1 + Phase 2 `[P]` tasks (T002, T003, T005, T006) can run in parallel after T001 / T004 land
- After Phase 2, US3 (Phase 3) and US2 (Phase 5) can run in parallel — they touch disjoint files
- After Phase 3 lands, US1 (Phase 4) starts; US2 may already be complete or in flight
- Within each story's Tests subsection, the `[P]`-marked test-writing tasks are independent test functions — they can be written in parallel sessions provided the writers coordinate to avoid file-write conflicts (every Phase-3 test goes into `canonical_test.go`, etc.)
- Phase 9 documentation tasks T056 / T057 / T058 are independent — can run in parallel before T059 (the commit task)

---

## Parallel Execution Examples

### Phase 3 (US3 Canonical) — Tests in parallel before T013

```bash
# Each writes a distinct set of test functions into internal/transport/sign/canonical_test.go.
# Coordinate via a shared file or write each test in its own session and merge.
Task: "T007 — TestCanonical_SortsAtAllDepths (10 known shapes)"
Task: "T008 — TestCanonical_RejectsNaN / RejectsInf"
Task: "T009 — TestCanonical_StructAndMap (both gotcha cases)"
Task: "T010 — TestCanonical_RejectsFunc / Chan / Complex / NonStringMap"
Task: "T011 — TestCanonical_IgnoresMarshalJSON"
Task: "T012 — TestCanonical_EmbedsRawMessageVerbatim"

# After all tests are written and fail (RED), run T013:
Task: "T013 — Implement CanonicalJSON + recursive encodeValue walker (GREEN)"
```

### Phase 5 (US2 Replay-Defence) — Tests in parallel before T034 / T035

```bash
# Nonce-cache tests go into internal/transport/sign/nonce_test.go
Task: "T023 — TestNonce_AddNewReturnsFirstSeen"
Task: "T024 — TestNonce_AddDuplicateReturnsReplay"
Task: "T025 — TestNonce_AddEmpty/TooShort/TooLong returns ErrNonceEncoding"
Task: "T026 — TestNonce_AddNonPositiveTTLReturnsInvalid"
Task: "T027 — TestNonce_AddRespectsCancelledContext"
Task: "T028 — TestNonce_ExpiredAllowedAfterSweep"

# Timestamp tests go into internal/transport/sign/timestamp_test.go
Task: "T029 — TestTimestamp_FreshAccepted"
Task: "T030 — TestTimestamp_TooOldRejected"
Task: "T031 — TestTimestamp_FutureSkewRejected"
Task: "T032 — TestTimestamp_BoundaryAccepted"
Task: "T033 — TestTimestamp_NonPositiveSkewRejected"

# After all tests are RED, run T034 + T035:
Task: "T034 — Implement IsFreshTimestamp"
Task: "T035 — Implement NonceCache + Add (without Run)"
```

---

## Implementation Strategy

### MVP First (US3 → US1)

1. Complete Phase 1 (setup) and Phase 2 (foundational test seams + RawMessage type)
2. Complete Phase 3 (US3 — canonical encoder)
3. Complete Phase 4 (US1 — Sign + Verify round-trip)
4. **STOP and VALIDATE**: a fresh keypair + canonical payload + Sign + Verify round-trip works. This is the smallest end-to-end signing slice — the chunk's "MVP cut" that proves the protocol exists.

### Incremental Delivery

1. Setup + Foundational → Foundation ready
2. US3 (canonical) → byte-deterministic encoder works → demo-able by `go test -run TestCanonical`
3. US1 (sign/verify) → end-to-end round-trip works → SDD-12 / SDD-16 unblocked on the signing primitives
4. US2 (nonce + timestamp) → replay-defence primitives work → SDD-12 unblocked on replay defence
5. US4 (Run lifecycle) → goroutine ownership clean → server startup wiring viable
6. US5 (concurrent race-clean) → exactly-one-winner guarantee → load-bearing concurrency contract verified
7. US6 (sentinel identity) → operational signal taxonomy locked
8. Phase 9 (gates + docs + combined commit) → constitutional checks all green, AC-7 row updated, SDD-PLAYBOOK marks done

### Parallel Team Strategy (if multiple developers)

After Phase 2 lands, the user-story phases can be staffed in parallel pairs:
- **Developer A**: Phase 3 (US3) → Phase 4 (US1)
- **Developer B**: Phase 5 (US2) → Phase 6 (US4) → Phase 7 (US5)

US6 and Phase 9 require all upstream phases to land first, so they remain sequential at the end.

---

## Notes

- **TDD-mandatory**: every implementation task has at least one preceding test-writing task. Run the tests after writing them and confirm they FAIL (RED) before starting the implementation. Then run them again and confirm GREEN. Constitution VIII is non-negotiable for the Critical band (request signing).
- **No new dependencies**: zero changes to `go.mod` / `go.sum` (research [R-002](./research.md#r-002--sign--verify-stdlib-cryptoecdsasignasn1-substitution) — stdlib + the existing `decred/dcrd/dcrec/secp256k1/v4` dependency cover all crypto needs).
- **No commits between phases**: per the SDD-08 chunk contract, all work for this chunk lands in a single combined commit at the end of Phase 9 (T059).
- **No nonces / signatures / payloads in any log line or error message**: FR-017. The `TestVerify_NoLeakOnFuzzInput` test (T019) is the verifying floor; reviewers should also check Run's single log line carries `reason` only.
- **No init() functions**: every package-level mutation happens at `var` declaration time; the `nowFn` clock is `_test.go`-gated for swapping.
- **One goroutine target**: only `Run` is a goroutine. Constructor (`NewNonceCache`) spawns nothing; `Add` is synchronous.
- **`-race` is mandatory** for `TestNonceCache_ConcurrentAdd` (T043). The test's value comes entirely from running it under the race detector; running it without `-race` does NOT satisfy SC-006.
- **Coverage = 100%** is a constitutional bar, not an aspirational target. If the gate (T053) reports <100%, the missing branches are tasks for new tests; do not lower the bar.
- **The fuzz 60 s gate (T052) is non-negotiable**: `FuzzVerifyRequest` is Constitution VIII fuzz target #4. The seed corpus from T003 ensures the gate exercises the verification logic, not just "is this DER".
