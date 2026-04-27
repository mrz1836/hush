---

description: "Task list for SDD-02: internal/vault/securebytes (mlocked memory + zero-on-destroy)"
---

# Tasks: Secure Bytes Container (SDD-02)

**Input**: Design documents from `/specs/002-securebytes/`
**Prerequisites**: plan.md (✓), spec.md (✓), research.md (✓), data-model.md (✓), contracts/securebytes-api.md (✓), quickstart.md (✓)
**Chunk contract**: [docs/sdd/SDD-02.md](../../docs/sdd/SDD-02.md)

**Tests**: REQUIRED. This project is TDD-mandatory per Constitution VIII (100% coverage on `internal/vault/...`). Every behaviour contract has a test-writing task BEFORE the matching implementation task; tests MUST be written first and MUST fail before the corresponding implementation lands.

**Organization**: Tasks are grouped by user story (P1 → P2) so each story can be implemented and validated independently. The package's surface is small; most production code lives in `internal/vault/securebytes/securebytes.go` and all tests in `internal/vault/securebytes/securebytes_test.go`, so file-level parallelism is limited (tasks editing those two files run sequentially within a story). The per-OS wrappers (`securebytes_darwin.go`, `securebytes_linux.go`) and `doc.go` are independent files and are marked `[P]`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: User-story label (`[US1]`, `[US2]`, `[US3]`, `[US4]`); Setup, Foundational, and Polish tasks have no story label

## Path Conventions

- Single Go module at repo root (`github.com/mrz1836/hush`).
- Production code: `internal/vault/securebytes/`
- Test file: `internal/vault/securebytes/securebytes_test.go`
- All paths in tasks below are absolute-from-repo-root and identical to the file list locked in [plan.md](./plan.md) and [contracts/securebytes-api.md](./contracts/securebytes-api.md).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the package directory and promote the only new direct dependency.

- [ ] T001 Create directory `internal/vault/securebytes/` (the new sub-package under `internal/vault/`, currently empty per `docs/PACKAGE-MAP.md`)
- [ ] T002 Promote `golang.org/x/sys` from indirect to direct dependency in `go.mod` (the package already appears in `go.sum` as an indirect dep of `golang.org/x/crypto`; run `go mod tidy` after to confirm `go.sum` is unchanged); verify with `go list -m golang.org/x/sys` from repo root

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The package skeleton — files, type declaration, sentinel error, per-OS mlock/munlock wrappers, and the package-doc residual-risk note. NO behavioural methods (`New`, `Use`, `Destroy`, render methods) yet — those land in their respective user-story phases under TDD.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete; every story's tests reference symbols introduced here.

- [ ] T003 [P] Create `internal/vault/securebytes/doc.go` with the package comment from [contracts/securebytes-api.md:13-29](./contracts/securebytes-api.md): purpose, Layer 5 reference (`docs/SECURITY.md` §3.5 + §6), and the explicit residual-risk disclosure (Go runtime may transiently copy heap objects during GC compaction; outside the package's threat model; no bandaid mitigation added) — per Research B9
- [ ] T004 [P] Create `internal/vault/securebytes/securebytes_darwin.go` with `//go:build darwin` constraint and unexported `mlock(b []byte) error` / `munlock(b []byte) error` thin wrappers delegating to `golang.org/x/sys/unix.Mlock` / `unix.Munlock` — per Research A1 + A2
- [ ] T005 [P] Create `internal/vault/securebytes/securebytes_linux.go` with `//go:build linux` constraint and unexported `mlock(b []byte) error` / `munlock(b []byte) error` thin wrappers delegating to `golang.org/x/sys/unix.Mlock` / `unix.Munlock` — per Research A1 + A2
- [ ] T006 Create `internal/vault/securebytes/securebytes.go` with: package declaration; imports (`errors`, `fmt`, `log/slog`, `runtime`, `sync`); `type SecureBytes struct { mu sync.Mutex; buf []byte; destroyed bool }` (unexported fields per [data-model.md:91-95](./data-model.md)); `var ErrDestroyed = errors.New("hush/vault/securebytes: destroyed")` (Constitution IX sentinel form per Research A6); `const redactedLiteral = "[redacted]"`; `var redactedJSON = []byte(`"[redacted]"`)` (per [contracts/securebytes-api.md:202-208](./contracts/securebytes-api.md)). NO method bodies yet — only the type, sentinel, and constants
- [ ] T007 Create empty `internal/vault/securebytes/securebytes_test.go` with `package securebytes` declaration and `import "testing"` so subsequent test tasks can append without touching package boilerplate
- [ ] T008 Verify the foundational skeleton compiles on both supported OSes: from repo root run `GOOS=darwin go build ./internal/vault/securebytes/` and `GOOS=linux go build ./internal/vault/securebytes/`. Both MUST succeed with no errors before any user-story phase starts

**Checkpoint**: Skeleton compiles on darwin and linux. The type, sentinel error, and per-OS mlock/munlock wrappers exist; no behavioural methods exist yet. User-story phases can begin.

---

## Phase 3: User Story 1 — Hold and use a secret without ever leaking it (Priority: P1) 🎯 MVP

**Goal**: A consumer can wrap a secret in a `SecureBytes` container, render it through every standard log/format/JSON path without leaking the bytes, and read it through the borrow callback for a bounded operation.

**Independent Test**: Run `go test -run 'TestSecureBytes_(New_CopiesAndZeroesInput|Use_DeliversPayload|Render_RedactsAllPaths|RedactionSentinel)' ./internal/vault/securebytes/` — all four tests pass; the captured outputs across slog, `fmt`, and `json.Marshal` contain `[redacted]` and contain zero bytes of `SECRET_SHOULD_NEVER_APPEAR_2`.

### Tests for User Story 1 ⚠️ TDD — write FIRST, ensure they FAIL before implementation

> **NOTE**: Each test task below MUST be completed and the test confirmed failing (or not yet compiling, which counts as failing) before the matching implementation task in the next subsection.

- [ ] T009 [US1] Add `TestSecureBytes_New_CopiesAndZeroesInput` to `internal/vault/securebytes/securebytes_test.go` — covers G1 (Spec FR-003, FR-004, SC-005, SC-006). Table-driven over `nil`, `[]byte{}`, and a 32-byte non-zero input. For the non-empty cases assert: `New` returns a non-nil container and `nil` error; the caller's input slice contains only zero bytes after `New` returns; the container's `Len()` equals the original length; the bytes inside the container (verified through a single `Use` borrow) equal the original input. Test MUST fail until T015 lands
- [ ] T010 [US1] Add `TestSecureBytes_Use_DeliversPayload` to `internal/vault/securebytes/securebytes_test.go` — covers G2 (Spec FR-006, FR-008, edge case "Borrow callback that panics", edge case "Concurrent borrows"). Sub-tests: (a) the callback receives a buffer of correct length whose bytes equal the original input; (b) two concurrent `Use` calls each see correct bytes (use `sync.WaitGroup` + a barrier `sync.Mutex` to force overlap; assert no data race surface — race detector enforced by `magex test:race`); (c) a panic from the callback is recovered by the test and the container remains usable for a subsequent `Use` and `Destroy`. Test MUST fail until T016 lands
- [ ] T011 [US1] Add `TestSecureBytes_Render_RedactsAllPaths` to `internal/vault/securebytes/securebytes_test.go` — covers G5 for the LIVE container path (Spec FR-014, FR-015, FR-016). Build a `SecureBytes` wrapping a known short payload; assert `sb.LogValue() == slog.StringValue("[redacted]")`, `sb.String() == "[redacted]"`, `fmt.Sprintf("%v", sb) == "[redacted]"`, `fmt.Sprintf("%s", sb) == "[redacted]"`, and `string(must(sb.MarshalJSON())) == `"[redacted]"``. The DESTROYED-path branch of G5 (FR-017) is asserted by T019. Test MUST fail until T014 lands
- [ ] T012 [US1] Add `TestSecureBytes_RedactionSentinel` to `internal/vault/securebytes/securebytes_test.go` — covers G6 (Spec SC-001; the sentinel-leak test required by [docs/TESTING-STRATEGY.md](../../docs/TESTING-STRATEGY.md) §5 and the user-supplied prompt). Wrap the literal byte sequence `SECRET_SHOULD_NEVER_APPEAR_2` in a `SecureBytes`. Wire a `slog.JSONHandler` writing into a `bytes.Buffer`; emit `slog.New(handler).Info("entry", "secret", sb)`. Capture additional outputs from `fmt.Sprintf("%s", sb)`, `fmt.Sprintf("%v", sb)`, and `json.Marshal(sb)`. For each captured output assert: it contains `"[redacted]"` AND `bytes.Contains(out, []byte("SECRET_SHOULD_NEVER_APPEAR_2"))` is `false`. Per Research B5. Test MUST fail until T015–T017 land

### Implementation for User Story 1

- [ ] T013 [US1] Implement render constants usage in `internal/vault/securebytes/securebytes.go`: confirm `redactedLiteral` and `redactedJSON` from T006 are exactly `"[redacted]"` and `[]byte(`"[redacted]"`)` respectively (these MUST be the package's only string-typed outputs; per Constitution X and the negative-space contract in [contracts/securebytes-api.md:170-174](./contracts/securebytes-api.md))
- [ ] T014 [US1] Implement `LogValue() slog.Value`, `String() string`, and `MarshalJSON() ([]byte, error)` on `*SecureBytes` in `internal/vault/securebytes/securebytes.go`. All three MUST return the redacted literal directly without consulting `sb.destroyed` and without touching `sb.buf` (Spec FR-014, FR-015, FR-016, FR-017). `LogValue` returns `slog.StringValue(redactedLiteral)`; `String` returns `redactedLiteral`; `MarshalJSON` returns `redactedJSON, nil`. Validates T011 + the LIVE half of T012
- [ ] T015 [US1] Implement `New(b []byte) (*SecureBytes, error)` in `internal/vault/securebytes/securebytes.go`: allocate fresh `buf := make([]byte, len(b))`; `copy(buf, b)`; call package-internal `mlock(buf)` (resolved at compile time to the per-OS wrapper from T004 / T005); on `mlock` error return `nil, fmt.Errorf("hush/vault/securebytes: mlock: %w", err)` (per Research A7 + B7; Spec FR-005, SC-011); zero `b` byte-by-byte (`for i := range b { b[i] = 0 }`); construct `&SecureBytes{buf: buf}`; return the pointer. Finalizer wiring is added later in T024 (US3); leaving it out of this task is intentional so the US1 tests pass without depending on US3. Validates T009
- [ ] T016 [US1] Implement `Use(fn func(b []byte)) error` and `Len() int` on `*SecureBytes` in `internal/vault/securebytes/securebytes.go`: `Use` acquires `sb.mu` for the entire callback duration (Research B1, B8; Spec edge case "Concurrent borrow and destroy"); if `sb.destroyed` returns `ErrDestroyed` without invoking `fn`; otherwise calls `fn(sb.buf)`; releases the mutex on return (use `defer` so a panic from `fn` still releases — Spec edge case "Borrow callback that panics"). `Len` acquires `sb.mu`, returns `0` if destroyed (Spec FR-018) else `len(sb.buf)`, releases the mutex. Validates T010
- [ ] T017 [US1] US1 green-bar verification: from repo root run `go test -run 'TestSecureBytes_(New_CopiesAndZeroesInput|Use_DeliversPayload|Render_RedactsAllPaths|RedactionSentinel)' -race ./internal/vault/securebytes/` — all four MUST pass with `-race` clean. Closes the User Story 1 independent-test obligation

**Checkpoint**: User Story 1 fully functional. A consumer can `New` → `Use` → `Render` a secret without leakage. T012's sentinel-leak test passes. `Destroy` does not yet exist; lifecycle protection arrives in US2.

---

## Phase 4: User Story 2 — Explicitly destroy a secret the moment it is no longer needed (Priority: P1)

**Goal**: A consumer can deterministically zero a `SecureBytes` at a known boundary; subsequent `Use` calls return the named `ErrDestroyed`; double-`Destroy` is a no-op; rendering remains `"[redacted]"`.

**Independent Test**: Run `go test -run 'TestSecureBytes_(Destroy_ZeroesAndIdempotent|PostDestroy_ReturnsErrDestroyed)' ./internal/vault/securebytes/` — both pass.

### Tests for User Story 2 ⚠️ TDD — write FIRST, ensure they FAIL before implementation

- [ ] T018 [US2] Add `TestSecureBytes_Destroy_ZeroesAndIdempotent` to `internal/vault/securebytes/securebytes_test.go` — covers G3 (Spec FR-010, FR-011, SC-002, SC-007). Build a container with a known non-zero payload; call `Destroy()` once and assert it returns `nil`; capture the previously-held buffer slice header before `Destroy` (using a borrow that records the pointer; permitted only inside this white-box test) and assert every byte is zero after `Destroy`; call `Destroy()` a second time and assert it returns `nil` with no panic and no state change. Test MUST fail until T020 lands
- [ ] T019 [US2] Add `TestSecureBytes_PostDestroy_ReturnsErrDestroyed` to `internal/vault/securebytes/securebytes_test.go` — covers G4 (Spec FR-009, FR-012, FR-018, SC-008). Build a container; `Destroy()`; call `Use(fn)` and assert: the returned error satisfies `errors.Is(err, securebytes.ErrDestroyed)`, AND the callback `fn` was NOT invoked (sentinel `bool` flag inside `fn` remains `false`). Also assert `Len()` reports `0` after destroy. Also assert that rendering a destroyed container still returns `"[redacted]"` for `String`, `MarshalJSON`, and `LogValue` (G5 destroyed-path / Spec FR-017). Test MUST fail until T020 lands

### Implementation for User Story 2

- [ ] T020 [US2] Implement `Destroy() error` on `*SecureBytes` in `internal/vault/securebytes/securebytes.go`: acquire `sb.mu`; if `sb.destroyed` release and return `nil` (idempotency invariant per [data-model.md:182-184](./data-model.md)); else zero `sb.buf` byte-by-byte (`for i := range sb.buf { sb.buf[i] = 0 }` — the canonical Go idiom per Research B2); call `munlock(sb.buf)` (per-OS wrapper from T004 / T005); on `munlock` error wrap via `fmt.Errorf("hush/vault/securebytes: munlock: %w", err)` and still set `sb.destroyed = true` and `sb.buf = nil` before returning the error (the buffer is already zeroed; leaving it pinned but zero is acceptable; the security-relevant work has happened); on success set `sb.destroyed = true`, `sb.buf = nil`, then `runtime.KeepAlive(sb)` (Research B2 belt-and-braces guard); release the mutex; return `nil`. Validates T018 and T019
- [ ] T021 [US2] US2 green-bar verification: from repo root run `go test -run 'TestSecureBytes_(Destroy_ZeroesAndIdempotent|PostDestroy_ReturnsErrDestroyed)' -race ./internal/vault/securebytes/` — both MUST pass with `-race` clean. Closes the User Story 2 independent-test obligation

**Checkpoint**: User Stories 1 AND 2 fully functional. `Destroy` zeroes + munlocks + flips state; subsequent `Use` returns `ErrDestroyed`; rendering paths still redact. The reclamation safety net (US3) is not yet wired.

---

## Phase 5: User Story 3 — Forgotten secrets are zeroed automatically before reclamation (Priority: P2)

**Goal**: A `*SecureBytes` that becomes unreachable without a prior explicit `Destroy` has its finalizer trigger `Destroy` before the underlying memory is recycled.

**Independent Test**: Run `go test -run 'TestSecureBytes_FinalizerZerosOnGC' ./internal/vault/securebytes/` — passes; the side-channel flag set by the test-only finalizer wrapper observes that the destruction path ran after GC.

### Tests for User Story 3 ⚠️ TDD — write FIRST, ensure they FAIL before implementation

- [ ] T022 [US3] Add `TestSecureBytes_FinalizerZerosOnGC` to `internal/vault/securebytes/securebytes_test.go` — covers G7 (Spec FR-013, SC-003; User Story 3 acceptance scenario). Implementation pattern (per Research B4): in a function-local scope, allocate a `*SecureBytes` via `New`; per `runtime.SetFinalizer` semantics only one finalizer per pointer is allowed, so the test overrides the production finalizer (set in T024) with a wrapper that calls the production `(*SecureBytes).finalize` (T023) and then sets a captured `*atomic.Bool` flag; drop all live references; call `runtime.GC()` followed by another `runtime.GC()` (the canonical "two GCs" pattern); use a polling loop (≤ 2 s timeout) to assert the flag becomes `true`; do NOT inspect the buffer contents post-GC (the backing array may already have been recycled). Test MUST fail until T024 lands

### Implementation for User Story 3

- [ ] T023 [US3] Implement the unexported method `(sb *SecureBytes) finalize()` in `internal/vault/securebytes/securebytes.go`: simply calls `_ = sb.Destroy()` (idempotency from US2 makes this safe whether or not the user already destroyed). Per Research B3, this is a method-value form — no closure capture
- [ ] T024 [US3] Wire `runtime.SetFinalizer(sb, (*SecureBytes).finalize)` at the end of `New` in `internal/vault/securebytes/securebytes.go`, before the `return sb, nil`. The method-value form `(*SecureBytes).finalize` is required (Research B3) — do NOT use a closure such as `func(sb *SecureBytes) { sb.Destroy() }`. Validates T022

**Checkpoint**: User Stories 1, 2, AND 3 fully functional. The finalizer safety net catches forgotten containers. Cross-platform race-cleanness (US4) remains.

---

## Phase 6: User Story 4 — The package is portable across the project's supported platforms (Priority: P2)

**Goal**: The full behavioural test suite runs on darwin AND linux under `CGO_ENABLED=0`, race-clean, with no C-toolchain dependency.

**Independent Test**: From repo root, `GOOS=darwin go build ./internal/vault/securebytes/` and `GOOS=linux go build ./internal/vault/securebytes/` both succeed; `go test -race ./internal/vault/securebytes/` runs clean on the developer's local platform; CI matrix runs on both.

### Tests for User Story 4 ⚠️ TDD — write FIRST, ensure they FAIL before implementation

- [ ] T025 [US4] Add `TestSecureBytes_ConcurrentUse` to `internal/vault/securebytes/securebytes_test.go` — covers G8 (Spec FR-008, SC-010; edge case "Concurrent borrows"). Spawn N=16 goroutines (or `runtime.GOMAXPROCS(0) * 4`, whichever is larger) each invoking `sb.Use(fn)` against the SAME live container in a tight loop for ≥ 1000 iterations; each callback verifies `bytes.Equal(b, want)` and increments an `atomic.Int64` counter; `sync.WaitGroup` joins all goroutines; assert the counter equals `N * iterations`. Test MUST be `-race` clean — failure is reported by the race detector exiting non-zero. Test MUST fail (or surface a race) until the mutex implementation in T016 + T020 stabilises; given those landed earlier, this test is expected to pass on first run after T016/T020, but it is enumerated under US4 because race-cleanness is the user-story-level guarantee SC-010 owns

### Implementation for User Story 4

- [ ] T026 [US4] Cross-platform build verification in `internal/vault/securebytes/`: from repo root run `CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./internal/vault/securebytes/`, `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./internal/vault/securebytes/`, `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./internal/vault/securebytes/`, `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./internal/vault/securebytes/` — all four MUST succeed with no errors and no warnings (Spec FR-019, FR-020, SC-009)
- [ ] T027 [US4] Run the race-clean concurrent test suite locally: `go test -race -run 'TestSecureBytes_ConcurrentUse' ./internal/vault/securebytes/` — MUST exit 0 with no race reports (validates T025)

**Checkpoint**: All four user stories independently functional and verified. The package is feature-complete; remaining work is gates + docs.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Run the constitutional gate suite, verify 100% coverage, run negative-space grep checks against the contract's anti-list, and update the three project-level docs the SDD-02 implement prompt mandates.

**Note**: Tasks T028–T030 are the gate triplet from the user-supplied prompt: `magex format:fix && magex lint && magex test:race`. They run sequentially (`format:fix` may rewrite files that `lint` then re-validates; `test:race` is the final gate).

- [ ] T028 Run `magex format:fix` from repo root — MUST exit 0; if any file under `internal/vault/securebytes/` was reformatted, re-stage before T029
- [ ] T029 Run `magex lint` from repo root — MUST exit 0 with no warnings on `internal/vault/securebytes/...`. Specifically validates: no `gochecknoglobals` violations (Constitution IX); no `unused`; no `errcheck` on `Destroy` calls in tests (use `_ = sb.Destroy()` per quickstart pattern)
- [ ] T030 Run `magex test:race` from repo root — MUST exit 0 with no race reports for `./internal/vault/securebytes/...`. This is the canonical gate for SC-010 and the SDD-02 chunk-contract closure
- [ ] T031 Verify 100% coverage on the new package: `go test -cover ./internal/vault/securebytes/` from repo root MUST report `coverage: 100.0% of statements`. If any line is not covered, add the missing test case before proceeding (Constitution VIII; chunk contract coverage target)
- [ ] T032 [P] Run the negative-space contract grep checks from [contracts/securebytes-api.md:148-184](./contracts/securebytes-api.md) against `internal/vault/securebytes/`. All eight greps MUST return zero matches: (a) no `Bytes()`/`Get()`/`Slice()`/`Copy()` accessor; (b) no `string`-typed constructor; (c) no `import "C"` and no `/* #include */`; (d) no `"unsafe"` import; (e) no `func init()`; (f) no package-level mutable state (covered by `gochecknoglobals` in T029); (g) no `string(sb.buf)` / `string(buf)` / `string(b)` of secret-bearing slices; (h) no logger calls (`slog.Info`, `slog.Default`, etc.) — only `slog.Value` / `slog.StringValue` / `slog.LogValuer` symbols permitted
- [ ] T033 [P] Verify the leaf-package import rule: from repo root run `go list -deps ./internal/vault/securebytes/ | grep -E '^github\.com/mrz1836/hush/internal/'` — output MUST be empty (the package depends on stdlib + `golang.org/x/sys/unix` only; per [contracts/securebytes-api.md:175-178](./contracts/securebytes-api.md))
- [ ] T034 [P] Confirm the sentinel-leak test (T012, `TestSecureBytes_RedactionSentinel`) passed and that `SECRET_SHOULD_NEVER_APPEAR_2` is absent from any captured log output (re-run with `-v` if needed for human visual inspection) — closes the SDD-02 chunk contract's sentinel obligation
- [ ] T035 Append "Exported API — locked at SDD-02" subsection to `docs/PACKAGE-MAP.md` under the `internal/vault/` entry, listing the eight exported symbols from [contracts/securebytes-api.md](./contracts/securebytes-api.md) (`SecureBytes`, `New`, `(*SecureBytes).Use`, `(*SecureBytes).Len`, `(*SecureBytes).Destroy`, `(*SecureBytes).LogValue`, `(*SecureBytes).String`, `(*SecureBytes).MarshalJSON`, `ErrDestroyed`) — per the SDD-02 implement prompt step 4
- [ ] T036 [P] Update `docs/AC-MATRIX.md` AC-7 row (Layer 5 — secure memory) to reference the new test files: list `internal/vault/securebytes/securebytes_test.go` and the eight test names enumerated under [plan.md:125](./plan.md) Constitution Check row VIII — per the SDD-02 implement prompt step 5
- [ ] T037 [P] Mark SDD-02 status `done` in `docs/SDD-PLAYBOOK.md` — per the SDD-02 implement prompt step 6
- [ ] T038 Final verification: re-run `magex format:fix && magex lint && magex test:race && go test -cover ./internal/vault/securebytes/` from repo root — all MUST exit 0; coverage MUST be `100.0%`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately.
- **Foundational (Phase 2)**: Depends on Setup completion. BLOCKS all user stories (every story references the type, sentinel, mlock/munlock helpers, and skeleton files introduced here).
- **User Story 1 (Phase 3, P1)**: Depends on Foundational. Independently testable once T015–T017 land.
- **User Story 2 (Phase 4, P1)**: Depends on User Story 1 (T020 calls `munlock` and flips fields the US1 implementation introduced; T019 asserts behaviour of `Use` defined in T016). MAY proceed sequentially after US1.
- **User Story 3 (Phase 5, P2)**: Depends on User Story 1 (`finalize` calls `Destroy` defined in US2 and is wired into `New` defined in US1). MUST follow US2.
- **User Story 4 (Phase 6, P2)**: Depends on User Stories 1–3 (the concurrent test exercises `Use` + `Destroy`; cross-platform build verifies the per-OS wrappers from Foundational). Race-cleanness is final-state-of-the-package, so US4 is the natural pre-polish step.
- **Polish (Phase 7)**: Depends on all user stories complete.

### User Story Dependencies

User stories in this package are NOT fully independent — all stories share `securebytes.go` and `securebytes_test.go`. Story isolation is preserved at the *test-suite* level (each story's tests pass without referencing fixtures from other stories), but the implementation tasks must run in priority order (US1 → US2 → US3 → US4) because each story's implementation extends the same struct's method set.

### Within Each User Story

- Test tasks (Tests subsection) MUST be written and FAIL before the matching implementation task in the Implementation subsection (Constitution VIII / TDD-mandatory).
- All test functions for a story go in the same file (`securebytes_test.go`) and are written sequentially — they cannot be parallelised by file even though they are logically independent.
- Implementation tasks within a story extend the same source file (`securebytes.go`) and run sequentially.

### Parallel Opportunities

- **Phase 1**: T001 → T002 sequential (T002 references the directory created by T001 indirectly through `go mod tidy` running against the workspace).
- **Phase 2**: T003, T004, T005 are all `[P]` — three different files, no inter-dependencies. T006, T007 are sequential because they share the package boilerplate setup (T006 introduces the package; T007's test file imports it).
- **Phase 7 polish**: T032, T033, T034, T036, T037 are all `[P]` — they touch independent files (or are read-only verifications).
- The gate triplet T028 → T029 → T030 is strictly sequential (each gate's success is required before the next runs).

---

## Parallel Example: Phase 2 Foundational

```bash
# Launch the three independent file-creation tasks in parallel:
Task: "Create internal/vault/securebytes/doc.go with package comment + residual-risk note"
Task: "Create internal/vault/securebytes/securebytes_darwin.go with darwin build tag + mlock/munlock wrappers"
Task: "Create internal/vault/securebytes/securebytes_linux.go with linux build tag + mlock/munlock wrappers"

# Then run T006 (the cross-platform skeleton) and T007 (the test file) sequentially.
```

## Parallel Example: Phase 7 Polish (post-gates)

```bash
# After T028→T029→T030→T031 land, run the read-only verifications and doc updates in parallel:
Task: "Run negative-space contract grep checks (T032)"
Task: "Verify leaf-package import rule via go list -deps (T033)"
Task: "Re-confirm sentinel-leak test absence (T034)"
Task: "Update docs/AC-MATRIX.md AC-7 row (T036)"
Task: "Update docs/SDD-PLAYBOOK.md status to done (T037)"
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Complete Phase 1 (Setup) — T001, T002.
2. Complete Phase 2 (Foundational) — T003–T008. Skeleton compiles on darwin and linux.
3. Complete Phase 3 (User Story 1) — T009–T017. The container can wrap, render-redact, and borrow-read. **STOP and VALIDATE**: `go test -run 'TestSecureBytes_(New_CopiesAndZeroesInput|Use_DeliversPayload|Render_RedactsAllPaths|RedactionSentinel)' -race ./internal/vault/securebytes/` is green. The MVP delivers: a leak-proof secret holder usable by downstream packages for one bounded operation. Without lifecycle protection (US2) it is not yet release-ready, but it is independently demonstrable.

### Incremental Delivery

1. Setup + Foundational → Skeleton ready.
2. + User Story 1 → MVP: hold + render-redact + borrow-read (no destroy yet).
3. + User Story 2 → Lifecycle complete: explicit `Destroy` + `ErrDestroyed`.
4. + User Story 3 → Safety net wired: finalizer-driven destroy.
5. + User Story 4 → Cross-platform race-clean.
6. + Polish → 100% coverage + gates green + docs updated → SDD-02 chunk closed.

### Sequential single-developer strategy

All eight tests + four implementation methods + finalizer wiring + polish fits in a single short-lived branch (the chunk's expected LOC is ≤ ~150 production + ~250 test). Run T001 → T038 sequentially; the file overlap (everything lands in `securebytes.go` and `securebytes_test.go`) makes parallel team strategies low-yield for this chunk.

---

## Notes

- `[P]` tasks = different files, no dependencies on incomplete tasks.
- `[Story]` label maps a task to its user story (`US1`, `US2`, `US3`, `US4`); Setup, Foundational, and Polish tasks have no story label.
- TDD-mandatory: every implementation task has its matching test task in an earlier slot of the same phase. Verify each test fails (or fails to compile) before writing the implementation.
- Coverage MUST be 100.0% — the chunk's surface is small enough that the eight enumerated tests cover every branch when the test bodies are written exhaustively (table-driven over empty + non-empty payloads, live + destroyed states, single + concurrent callers).
- Final-phase gates (T028 → T029 → T030) come from the SDD-02 implement prompt and the user-supplied tasks-phase prompt: `magex format:fix && magex lint && magex test:race`.
- Commit cadence: the implement-phase prompt instructs declining the per-task auto-commit and producing one combined commit at the end (`feat(vault/securebytes): mlocked secure memory + redaction (SDD-02)`); follow that pattern.
- Anti-pattern reminders (negative-space contract): no `Bytes()` accessor, no `string` constructor input, no cgo, no `unsafe`, no `init()`, no package globals beyond `ErrDestroyed` + `redactedJSON`, no logger, no internal/* imports.
