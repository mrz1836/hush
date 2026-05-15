---
description: "Tasks: SDD-07 internal/token — ES256K JWT issuance, validation, store, revocation"
---

# Tasks: `internal/token` — ES256K JWT issuance, validation, store, revocation

**Input**: Design documents from [/specs/007-token-jwt/](./)
**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/api.md](./contracts/api.md), [quickstart.md](./quickstart.md)
**Chunk contract**: [docs/sdd/SDD-07.md](../../docs/sdd/SDD-07.md)

**Tests**: TDD-mandatory per Constitution VIII. Every behaviour contract has a test-writing task scheduled BEFORE its implementation task. Tests MUST be written first and MUST FAIL before implementation begins. Coverage target: **100%** on `internal/token/`. Fuzz target #2: `FuzzJWTValidate` ≥60 s clean.

**Organization**: Tasks are grouped by user story (US1–US8 from [spec.md](./spec.md)) so each story can be implemented and validated independently. Within each story, tests precede implementation.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1–US8). No story label on Setup/Foundational/Polish.

## Path conventions

- Production source files live under [internal/token/](../../internal/token/).
- Test files live alongside production files in the same package directory.
- Fuzz seed corpus lives under [internal/token/testdata/fuzz/FuzzJWTValidate/](../../internal/token/testdata/fuzz/FuzzJWTValidate/).
- Repository root: `/Users/mrz/projects/hush`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the package directory, add the new direct dependency, draft the package doc.

- [X] T001 Create directory tree at [internal/token/](../../internal/token/) and the fuzz seed directory at [internal/token/testdata/fuzz/FuzzJWTValidate/](../../internal/token/testdata/fuzz/FuzzJWTValidate/) per [plan.md §Project Structure](./plan.md)
- [X] T002 Add `github.com/golang-jwt/jwt/v5` as a NEW direct dependency in [go.mod](../../go.mod) and update [go.sum](../../go.sum) (`go get github.com/golang-jwt/jwt/v5@latest && go mod tidy`); verify zero new transitive dependencies per research [R-001](./research.md)
- [X] T003 [P] Create [internal/token/doc.go](../../internal/token/doc.go) with package-level doc comment citing Constitution III/IV/VIII/IX/X and the Layer-2 roster per [plan.md §Project Structure](./plan.md)

**Checkpoint**: Package directory exists; `golang-jwt/jwt/v5` is in `go.mod`; doc.go compiles.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Build the primitives every user story depends on — sentinel errors, ES256K signing-method registration, the `Claims`/`SessionType` types, the `IssueParams`/`Token` types, the empty `Store` interface skeleton. No user-story behaviour wired yet; the package compiles and tests for the foundational layer pass.

**⚠️ CRITICAL**: No US1–US8 work can begin until this phase is complete.

### Tests for Foundational (TDD — write FIRST and ensure they FAIL)

- [X] T004 [P] Write `TestErrors_DistinctIdentities` in [internal/token/errors_test.go](../../internal/token/errors_test.go): assert each of the 7 sentinels (`ErrAlgorithmUnsupported`, `ErrTokenExpired`, `ErrTokenRevoked`, `ErrTokenExhausted`, `ErrIPMismatch`, `ErrScopeViolation`, `ErrUnknownSessionType`) is non-nil, has the static message from [data-model.md §4](./data-model.md), and `errors.Is(a, b)` is false for any two distinct sentinels (no wrap relationships). Per research [R-007](./research.md).
- [X] T005 [P] Write `TestRegisterOnce_Concurrent` in [internal/token/alg_es256k_test.go](../../internal/token/alg_es256k_test.go): launch 100 goroutines each calling `Register()`; assert no panic, no double-register, and the registered method's `Alg()` returns `"ES256K"`. Per [data-model.md §1.3](./data-model.md) and invariant I-020.
- [X] T006 [P] Write `TestES256KMethod_RoundTrip` in [internal/token/alg_es256k_test.go](../../internal/token/alg_es256k_test.go): generate a fresh `*ecdsa.PrivateKey` via `ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)`, sign a known signing-input via `es256kMethod{}.Sign`, verify via `es256kMethod{}.Verify` against the public key — assert verify returns nil. Then mutate one byte of the signature and assert verify returns `jwt.ErrTokenSignatureInvalid`. Per research [R-002](./research.md).
- [X] T007 [P] Write `TestSessionType_Vocabulary` in [internal/token/claims_test.go](../../internal/token/claims_test.go): table-driven test asserting `SessionInteractive` and `SessionSupervisor` are the only valid values; the empty string and any other string fail the vocabulary check (the file-private helper invoked in §1.3). Per FR-004.
- [X] T008 [P] Write `TestClaims_JSONRoundTrip` in [internal/token/claims_test.go](../../internal/token/claims_test.go): marshal a fully-populated `Claims` to JSON, unmarshal back, assert every field round-trips with the JSON keys from [data-model.md §2.2](./data-model.md) (`scope`, `client_ip`, `request_id`, `max_uses`, `ephemeral_pubkey`, `session_type`, plus `iss`/`iat`/`exp`/`jti` from `RegisteredClaims`).
- [X] T009 [P] Write `TestNewStore_Defaults` in [internal/token/store_test.go](../../internal/token/store_test.go): assert `NewStore()` returns a non-nil `Store`; assert `NewStoreWithTick(d)` returns a non-nil `Store` and that calling its methods on a fresh instance does not panic.

### Implementation for Foundational

- [X] T010 Create [internal/token/errors.go](../../internal/token/errors.go) with the seven sentinel `var Err... = errors.New("hush/token: <message>")` declarations from [data-model.md §4](./data-model.md). No `fmt.Errorf`; no wrap relationships. Static messages only.
- [X] T011 Create [internal/token/alg_es256k.go](../../internal/token/alg_es256k.go) with: file-private `es256kMethod` struct implementing `jwt.SigningMethod` (`Alg()` returns `"ES256K"`; `Sign` delegates to `ecdsa.SignASN1` over `sha256.Sum256(signingInput)`; `Verify` delegates to `ecdsa.VerifyASN1`); package-level `var registerOnce sync.Once`; exported `Register()` function gated by the `sync.Once` calling `jwt.RegisterSigningMethod("ES256K", ...)`. Per research [R-002](./research.md). NO `init()`.
- [X] T012 Create [internal/token/claims.go](../../internal/token/claims.go) with: `type SessionType string` + the two constants `SessionInteractive`/`SessionSupervisor`; `type Claims struct` embedding `jwt.RegisteredClaims` plus the six hush-specific fields with JSON tags from [data-model.md §1.2](./data-model.md); a file-private `validSessionType(SessionType) bool` helper.
- [X] T013 Create [internal/token/store.go](../../internal/token/store.go) with: the public `type Store interface` (5 methods: `Add`, `Get`, `ConsumeUse`, `Revoke`, `Cleanup`); file-private `type memStore struct` (`mu sync.RWMutex`, `live map[string]*Token`, `revoked map[string]struct{}`, `tick time.Duration`, `nowFn func() time.Time`); `const defaultTick = 30 * time.Second`; `func NewStore() Store` returning `&memStore{...}` with `tick=defaultTick` and `nowFn=time.Now`; `func NewStoreWithTick(d time.Duration) Store` (test seam). Method bodies are empty stubs returning `nil` or zero values — actual logic is wired by user-story phases.
- [X] T014 [P] Create [internal/token/issue.go](../../internal/token/issue.go) with: `type IssueParams struct` from [data-model.md §1.2](./data-model.md); `type Token struct` from [data-model.md §1.2](./data-model.md); `var randReader io.Reader = rand.Reader` (test seam); the `func generateJTI() (string, error)` helper from research [R-003](./research.md) (16 bytes from `randReader`, RFC 4122 v4 version+variant bits, hyphenated hex); `func Issue(...)` declared as a stub returning `nil, errors.New("not implemented")` — full body wired in US1.
- [X] T015 [P] Create [internal/token/validate.go](../../internal/token/validate.go) with: `func Validate(...)` declared as a stub returning `nil, errors.New("not implemented")` — full body wired across US1/US2/US3/US4/US5/US7. The file declares no helpers yet.
- [X] T016 [P] Create [internal/token/revoke.go](../../internal/token/revoke.go) with: stub `func (s *memStore) Revoke(jti string) error` returning `nil` — full body wired in US5.

**Checkpoint**: `go build ./internal/token/...` succeeds; the foundational tests T004–T009 pass; the package compiles but every user-story behaviour returns "not implemented" or zero-value.

---

## Phase 3: User Story 1 — Issue + Validate happy path (Priority: P1) 🎯 MVP

**Goal**: Issue a token from a known signing key, validate the resulting token against the matching verify key + matching IP + in-scope secret, recover the `*Claims` with the issued field values. Both `SessionInteractive` and `SessionSupervisor` round-trip.

**Independent test**: Issue a token; validate it; assert the recovered claims match the issuance parameters.

### Tests for User Story 1 (TDD — write FIRST and ensure they FAIL) ⚠️

- [X] T017 [P] [US1] Write `TestIssue_Interactive` in [internal/token/issue_test.go](../../internal/token/issue_test.go): given a fresh signing key and `IssueParams{SessionType: SessionInteractive, MaxUses: 50, ...}`, assert `Issue` returns a non-nil `*Token` whose `JTI` is non-empty (UUIDv4 hyphenated form), `Encoded` is the signed compact-form JWT (3 base64url segments separated by `.`), `ExpiresAt == params.Now.Add(params.TTL)`, `SessionType == SessionInteractive`, `MaxUses == 50`. Per FR-001/FR-005, AC-4, invariant I-001.
- [X] T018 [P] [US1] Write `TestIssue_Supervisor` in [internal/token/issue_test.go](../../internal/token/issue_test.go): given `IssueParams{SessionType: SessionSupervisor, MaxUses: 99 /* should be silently zeroed */, ...}`, assert returned `Token.MaxUses == 0` and that the encoded JWT's `max_uses` claim is `0`. Per FR-006 and the "supervisor TTL-only" property; invariant I-013-issue-side.
- [X] T019 [P] [US1] Write `TestIssue_FreshJTIPerCall` in [internal/token/issue_test.go](../../internal/token/issue_test.go): call `Issue` twice with identical `IssueParams`; assert the two `*Token` values have distinct `JTI` strings. Per FR-008, invariant I-002.
- [X] T020 [P] [US1] Write `TestIssue_HeaderAlg` in [internal/token/issue_test.go](../../internal/token/issue_test.go): issue a token; base64url-decode the first segment of `Token.Encoded`; JSON-unmarshal; assert `Alg == "ES256K"` and `Typ == "JWT"`. Per FR-001, invariant I-003.
- [X] T021 [P] [US1] Write `TestIssue_RejectsUnknownSessionType` in [internal/token/issue_test.go](../../internal/token/issue_test.go): given `IssueParams{SessionType: "delegated"}`, assert `Issue` returns `(nil, ErrUnknownSessionType)` via `errors.Is`. Per FR-004, invariant I-019.
- [X] T022 [P] [US1] Write `TestIssue_RejectsInvalidParams` in [internal/token/issue_test.go](../../internal/token/issue_test.go): table-driven test covering each invalid `IssueParams` field per [contracts/api.md §IssueParams](./contracts/api.md) (`Now=zero`, `TTL=0`, `Scope=nil`, `Scope=[]string{""}`, `ClientIP=""`, `ClientIP="not-an-ip"`, `RequestID=""`, `MaxUses=0` for INTERACTIVE, `EphemeralPubKey=""`); assert each returns `(nil, ErrAlgorithmUnsupported)` per research [R-008](./research.md).
- [X] T023 [P] [US1] Write `TestIssue_RespectsCancelledContext` in [internal/token/issue_test.go](../../internal/token/issue_test.go): build a pre-cancelled `ctx`; call `Issue(ctx, ...)`; assert returned error satisfies `errors.Is(err, context.Canceled)`. Per FR-015, SC-011, invariant I-018.
- [X] T024 [P] [US1] Write `TestValidate_HappyPath` in [internal/token/validate_test.go](../../internal/token/validate_test.go): issue a token; add it to the store; call `Validate(ctx, encoded, pub, store, ip, secretName)` with matching values; assert returned `*Claims` has the expected `Issuer`, `IssuedAt`, `ExpiresAt`, `ID` (= JTI), `Scope`, `ClientIP`, `RequestID`, `MaxUses`, `EphemeralPubKey`, `SessionType`. Per FR-001/FR-005/FR-009, AC-4, invariant I-001.
- [X] T025 [P] [US1] Write `TestValidate_DecrementsInteractive` in [internal/token/validate_test.go](../../internal/token/validate_test.go): issue an INTERACTIVE token with `MaxUses=3`; call `Validate` once; call `Store.Get(jti)`; assert the in-store record's `MaxUses == 2`. Per FR-005.
- [X] T026 [P] [US1] Write `TestValidate_RespectsCancelledContext` in [internal/token/validate_test.go](../../internal/token/validate_test.go): pre-cancel `ctx`; call `Validate`; assert `errors.Is(err, context.Canceled)`. Per FR-015, SC-011, invariant I-018.

### Implementation for User Story 1

- [X] T027 [US1] Implement `Issue` in [internal/token/issue.go](../../internal/token/issue.go) per [data-model.md §5.1](./data-model.md): (1) `ctx.Err()` early-out; (2) `Register()` (sync.Once); (3) validate `IssueParams.SessionType` (return `ErrUnknownSessionType`) and other fields per [contracts/api.md §IssueParams](./contracts/api.md) (return bare `ErrAlgorithmUnsupported`); (4) force `MaxUses=0` for `SessionSupervisor`; (5) call `generateJTI()`; (6) construct `Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer:"hush", IssuedAt:..., ExpiresAt:..., ID:jti}, ...}`; (7) `jwt.NewWithClaims(es256kMethod{}, claims).SignedString(signKey)`; (8) return `&Token{JTI, Encoded, ExpiresAt, SessionType, MaxUses}`. NO `fmt.Errorf`; NO logging; ctx-first.
- [X] T028 [US1] Implement the happy-path skeleton of `Validate` in [internal/token/validate.go](../../internal/token/validate.go) per [data-model.md §5.2](./data-model.md): (1) `ctx.Err()` early-out; (2) `Register()` (sync.Once); (3) `validateAlgorithm` stub returns nil for now (US4 wires the real check); (4) `jwt.ParseWithClaims(encoded, &Claims{}, keyfunc, jwt.WithValidMethods([]string{"ES256K"}))` — keyfunc returns `verifyKey`; map `jwt.ErrTokenExpired` → `ErrTokenExpired`, any other library-class error → `ErrAlgorithmUnsupported`; (5) skip vocabulary/scope/IP/store checks for now (later phases); (6) for INTERACTIVE call `store.ConsumeUse(jti)`; (7) return `claims, nil`. The full validation ordering is wired across US2/US3/US4/US5/US7 but the happy path must work now.
- [X] T029 [US1] Implement `Store.Add` in [internal/token/store.go](../../internal/token/store.go) per [data-model.md §3.2](./data-model.md): take write lock; check `revoked[t.JTI]` → return `ErrTokenRevoked`; insert into `live[t.JTI] = t`; return nil.
- [X] T030 [US1] Implement `Store.Get` in [internal/token/store.go](../../internal/token/store.go): take read lock; check `revoked[jti]` → return `(nil, ErrTokenRevoked)`; return `(live[jti], nil)` or if absent `(nil, ErrTokenRevoked)` (treat unknown as revoked from this store's perspective per research [R-005](./research.md)).
- [X] T031 [US1] Implement `Store.ConsumeUse` in [internal/token/store.go](../../internal/token/store.go) per [data-model.md §5.3](./data-model.md): take write lock; (1) `revoked[jti]` → `ErrTokenRevoked`; (2) `live[jti]` absent → `ErrTokenRevoked`; (3) `t.ExpiresAt <= nowFn()` → `ErrTokenExpired`; (4) `t.SessionType == SessionSupervisor` → return nil; (5) `t.MaxUses == 0` → `ErrTokenExhausted`; (6) `t.MaxUses--`; return nil.

**Checkpoint**: T017–T026 all pass. A round-trip of `Issue` → `Store.Add` → `Validate` → recovered `*Claims` works end-to-end for both INTERACTIVE and SUPERVISOR. SC-001 satisfied.

---

## Phase 4: User Story 2 — A token from a different IP is rejected (Priority: P1)

**Goal**: `Validate` returns `ErrIPMismatch` for a token whose recorded `client_ip` does not match `requestIP`. Semantically equal IPs (different textual forms) are accepted.

**Independent test**: Issue a token with one IP; validate against a different IP — assert `ErrIPMismatch`. Validate against the same IP in a different textual form — assert success.

### Tests for User Story 2 (TDD — write FIRST and ensure they FAIL) ⚠️

- [X] T032 [P] [US2] Write `TestValidate_WrongIP` in [internal/token/validate_test.go](../../internal/token/validate_test.go): issue with `ClientIP="100.64.0.1"`; validate with `requestIP="100.64.0.99"`; assert `errors.Is(err, ErrIPMismatch)`. Per FR-007 #3, SC-002, invariant I-007.
- [X] T033 [P] [US2] Write `TestValidate_IPSemanticallyEqual` in [internal/token/validate_test.go](../../internal/token/validate_test.go): table-driven test. Pair examples: (`"100.64.0.1"`, `"100.64.0.1"`); (`"::1"`, `"0000:0000:0000:0000:0000:0000:0000:0001"`); (`"2001:db8::1"`, `"2001:0db8:0000:0000:0000:0000:0000:0001"`). For each pair issue with the first form and validate with the second; assert success. Per FR-016, invariant I-008.
- [X] T034 [P] [US2] Write `TestValidate_MalformedRequestIP_Refused` in [internal/token/validate_test.go](../../internal/token/validate_test.go): issue with a valid IP; validate with `requestIP="not-an-ip"`; assert `errors.Is(err, ErrIPMismatch)` (malformed input folds into `ErrIPMismatch` per research [R-004](./research.md)).

### Implementation for User Story 2

- [X] T035 [US2] Add the `compareIPs(claimIP, requestIP string) error` helper to [internal/token/validate.go](../../internal/token/validate.go) per research [R-004](./research.md): `netip.ParseAddr` both sides; on parse failure return `ErrIPMismatch`; compare via `==`; return `ErrIPMismatch` if unequal.
- [X] T036 [US2] Wire the IP check into `Validate` in [internal/token/validate.go](../../internal/token/validate.go) at step 7 of the verification ordering ([data-model.md §2.3](./data-model.md)) — after signature/expiry but before the store check.

**Checkpoint**: T032–T034 pass. SC-002 satisfied.

---

## Phase 5: User Story 3 — Interactive exhausts; supervisor does not (Priority: P1)

**Goal**: After N successful validations of an INTERACTIVE token with `MaxUses=N`, the (N+1)-th returns `ErrTokenExhausted`. A SUPERVISOR token validates an arbitrary number of times within TTL without `ErrTokenExhausted`.

**Independent test**: Issue interactive `MaxUses=3`; validate 4 times — assert 4th returns `ErrTokenExhausted`. Issue supervisor; validate 1000 times within TTL — assert all succeed.

### Tests for User Story 3 (TDD — write FIRST and ensure they FAIL) ⚠️

- [X] T037 [P] [US3] Write `TestStore_ExhaustedInteractive_Refused` in [internal/token/store_test.go](../../internal/token/store_test.go): issue an INTERACTIVE token with `MaxUses=3`; call `Validate` (or `Store.ConsumeUse` directly) 3 times — assert success; call a 4th time — assert `errors.Is(err, ErrTokenExhausted)`. Per FR-007 #5, SC-003 sequential side, invariant I-012.
- [X] T038 [P] [US3] Write `TestStore_SupervisorIgnoresMaxUses` in [internal/token/store_test.go](../../internal/token/store_test.go): issue a SUPERVISOR token (with `IssueParams.MaxUses=99` to confirm it is silently zeroed); call `Validate` 1000 times within TTL; assert every call succeeds and `errors.Is(err, ErrTokenExhausted)` never fires. Per FR-006, SC-004, invariant I-013.

### Implementation for User Story 3

- [X] T039 [US3] The `Store.ConsumeUse` body from T031 already implements the exhaustion logic (SUPERVISOR returns nil immediately; INTERACTIVE decrements and returns `ErrTokenExhausted` at zero). Verify the wiring matches [data-model.md §5.3](./data-model.md) and confirm `Validate` invokes `ConsumeUse` for both session types (SUPERVISOR's `ConsumeUse` short-circuits without decrementing — the call is still made so revocation/expiry are checked).

**Checkpoint**: T037–T038 pass. SC-003 (sequential side) and SC-004 satisfied.

---

## Phase 6: User Story 4 — Algorithm-confusion attacks rejected (Priority: P1)

**Goal**: A token with header `alg=none` and a token with header `alg=HS256` are both rejected with `ErrAlgorithmUnsupported`. The rejection fires BEFORE the keyfunc is consulted (the verify key never enters a verify primitive on a rejected path).

**Independent test**: Hand-craft each malicious token; present to `Validate`; assert `errors.Is(err, ErrAlgorithmUnsupported)`. Two named tests — one per alg-confusion class.

### Tests for User Story 4 (TDD — write FIRST and ensure they FAIL) ⚠️

- [X] T040 [P] [US4] Write `TestValidate_AlgConfusion_None_Refused` in [internal/token/validate_test.go](../../internal/token/validate_test.go): hand-craft a token whose header is `{"alg":"none","typ":"JWT"}` (base64url-encoded); concatenate with a base64url-encoded payload (any valid Claims JSON) and an empty third segment; call `Validate`; assert `errors.Is(err, ErrAlgorithmUnsupported)`. Use a keyfunc-instrumentation pattern (e.g., a `*ecdsa.PublicKey` whose use can be detected) to assert the keyfunc was NOT invoked. Per FR-002, SC-005, invariant I-004.
- [X] T041 [P] [US4] Write `TestValidate_AlgConfusion_HS256_Refused` in [internal/token/validate_test.go](../../internal/token/validate_test.go): hand-craft a token whose header is `{"alg":"HS256","typ":"JWT"}`, payload is valid Claims JSON, signature is `HMAC-SHA256(verifyKeyBytes, signingInput)` (the textbook "use the verify key as a shared secret" attack); call `Validate`; assert `errors.Is(err, ErrAlgorithmUnsupported)`. Assert keyfunc was NOT invoked. Per FR-003, SC-006, invariant I-005.
- [X] T042 [P] [US4] Write `TestValidate_MalformedHeader_Refused` in [internal/token/validate_test.go](../../internal/token/validate_test.go): cover three cases — empty string; no `.` separator; first segment that doesn't base64url-decode; first segment that decodes to non-JSON bytes. Each should return `errors.Is(err, ErrAlgorithmUnsupported)`. Per research [R-006](./research.md), data-model §4.1.

### Implementation for User Story 4

- [X] T043 [US4] Implement the `validateAlgorithm(encoded string) error` helper in [internal/token/validate.go](../../internal/token/validate.go) per research [R-006](./research.md): find the first `.`; base64url-decode the first segment; JSON-unmarshal `{Alg string \`json:"alg"\`}`; return `ErrAlgorithmUnsupported` for any failure or any `alg` value other than `"ES256K"`. Fires BEFORE the keyfunc.
- [X] T044 [US4] Wire `validateAlgorithm` into `Validate` in [internal/token/validate.go](../../internal/token/validate.go) as step 3 of the verification ordering ([data-model.md §2.3](./data-model.md)) — strictly BEFORE `jwt.ParseWithClaims`. Also pass `jwt.WithValidMethods([]string{"ES256K"})` to `ParseWithClaims` for defence in depth.

**Checkpoint**: T040–T042 pass. SC-005 and SC-006 satisfied; alg-confusion defence proven for both `none` and `HS256` independently.

---

## Phase 7: User Story 5 — Revocation persists for the lifetime of the store (Priority: P1)

**Goal**: After `Store.Revoke(jti)`, every subsequent `Validate` of that token returns `ErrTokenRevoked` (or `ErrTokenExpired` after natural TTL elapses), never success. The revoked-set entry is never cleared by `Cleanup`. A re-issue draws a fresh JTI from the CSPRNG.

**Independent test**: Issue a token; revoke its JTI; validate immediately — assert `ErrTokenRevoked`. Wait past TTL; validate again — assert `ErrTokenRevoked` or `ErrTokenExpired`, never success.

### Tests for User Story 5 (TDD — write FIRST and ensure they FAIL) ⚠️

- [X] T045 [P] [US5] Write `TestStore_RevokedJTI_Refused` in [internal/token/revoke_test.go](../../internal/token/revoke_test.go): issue an INTERACTIVE token; `Store.Add`; `Store.Revoke(jti)`; call `Validate` — assert `errors.Is(err, ErrTokenRevoked)`. Per FR-011, SC-007, invariant I-011.
- [X] T046 [P] [US5] Write `TestStore_RevokeIsIdempotent` in [internal/token/revoke_test.go](../../internal/token/revoke_test.go): call `Store.Revoke(jti)` twice; both return nil. Call `Store.Revoke("never-issued")`; returns nil. Per [data-model.md §5.4](./data-model.md).
- [X] T047 [P] [US5] Write `TestStore_RevokedSurvivesCleanup` in [internal/token/revoke_test.go](../../internal/token/revoke_test.go): issue a token with short TTL; `Add`; `Revoke`; trigger `Cleanup` (use `NewStoreWithTick(1*time.Millisecond)` and `time.Sleep`); after cleanup, call `Validate` against the same JTI — assert `errors.Is(err, ErrTokenRevoked)` (the revoked-set entry survives even though the live record is gone). Per FR-011/FR-012, invariant I-014.
- [X] T048 [P] [US5] Write `TestStore_AddOnRevokedJTI_Refused` in [internal/token/store_test.go](../../internal/token/store_test.go): manually populate `revoked[jti]` (or revoke first then re-attempt `Add` with the same JTI through a test helper); assert `Add` returns `errors.Is(err, ErrTokenRevoked)`. Per [data-model.md §3.2](./data-model.md).

### Implementation for User Story 5

- [X] T049 [US5] Implement `Store.Revoke` in [internal/token/revoke.go](../../internal/token/revoke.go) per [data-model.md §5.4](./data-model.md): take write lock; `revoked[jti] = struct{}{}`; `delete(live, jti)`; return nil. Idempotent — revoking unknown or already-revoked JTI is a no-op success.
- [X] T050 [US5] Wire the store revocation check into `Validate` in [internal/token/validate.go](../../internal/token/validate.go) as step 8 of the verification ordering ([data-model.md §2.3](./data-model.md)) — invoke `store.Get(jti)` after the IP check; if it returns `ErrTokenRevoked`, return it. Otherwise proceed to step 9 (`store.ConsumeUse`) which also checks the revoked set under write-lock.

**Checkpoint**: T045–T048 pass. SC-007 satisfied; revoked tokens stay rejected for the lifetime of the store.

---

## Phase 8: User Story 6 — Concurrent decrement is race-clean (Priority: P1)

**Goal**: N goroutines simultaneously calling `ConsumeUse` on the same INTERACTIVE token whose `MaxUses` is N produce **exactly** N successes and zero race-detector warnings; the (N+1)-th call returns `ErrTokenExhausted`.

**Independent test**: `TestStore_ConcurrentDecrement` with N=100; runs under `-race` clean.

### Tests for User Story 6 (TDD — write FIRST and ensure they FAIL) ⚠️

- [X] T051 [P] [US6] Write `TestStore_ConcurrentDecrement` in [internal/token/store_test.go](../../internal/token/store_test.go) per research [R-005](./research.md): issue an INTERACTIVE token with `MaxUses=100`; `Store.Add`; spawn 100 goroutines each calling `Validate` (or `Store.ConsumeUse`) once with matching params; use a `sync.WaitGroup`; collect successes/failures via channels or atomic counters; assert exactly 100 successes and 0 failures during the burst; after the burst call `ConsumeUse` once more — assert `errors.Is(err, ErrTokenExhausted)`. The test must run race-clean under `go test -race`. Per FR-010, SC-012, invariant I-015.

### Implementation for User Story 6

- [X] T052 [US6] Verify `Store.ConsumeUse` (already implemented in T031) takes the **write lock** (not a read lock with atomic decrement). Confirm the lock acquisition path: `s.mu.Lock(); defer s.mu.Unlock()`. The write-lock pattern is the load-bearing concurrency choice that produces exactly-N decrements per research [R-005](./research.md). No code change expected here unless T051 fails — but if it does, fix `ConsumeUse` rather than weakening the test.

**Checkpoint**: T051 passes under `go test -race ./internal/token/`. SC-012 satisfied; FR-010 race-clean.

---

## Phase 9: User Story 7 — Each rejection mode is individually nameable (Priority: P1)

**Goal**: Every spec-defined rejection category surfaces as a distinct, named sentinel — no rejection collapses into a generic "invalid token". This phase adds the remaining rejection categories not yet wired (expired, scope-violation, unknown-session-type) and adds the runtime sentinel-leak witness `TestValidate_NoLeakOnError`.

**Independent test**: For each defined rejection category, build an input that triggers exactly that category and assert the returned error is identifiable as the corresponding named sentinel.

### Tests for User Story 7 (TDD — write FIRST and ensure they FAIL) ⚠️

- [X] T053 [P] [US7] Write `TestValidate_ExpiredJWT` in [internal/token/validate_test.go](../../internal/token/validate_test.go): issue with `IssueParams.Now=past`, `TTL=1*time.Minute` (so `ExpiresAt < now`); validate; assert `errors.Is(err, ErrTokenExpired)`. Per FR-007 #1, invariant I-006.
- [X] T054 [P] [US7] Write `TestValidate_OutOfScope` in [internal/token/validate_test.go](../../internal/token/validate_test.go): issue with `Scope=["FAKE_SECRET_A"]`; validate against `requestedSecret="FAKE_SECRET_B"`; assert `errors.Is(err, ErrScopeViolation)`. Per FR-007 #2, invariant I-009.
- [X] T055 [P] [US7] Write `TestValidate_UnknownSessionType_Refused` in [internal/token/validate_test.go](../../internal/token/validate_test.go): hand-craft a token whose `session_type` claim is `"delegated"` (sign with the test signing key so the signature verifies); validate; assert `errors.Is(err, ErrUnknownSessionType)`. Per FR-007 #6, invariant I-010.
- [X] T056 [P] [US7] Write `TestValidate_NoLeakOnError` in [internal/token/validate_test.go](../../internal/token/validate_test.go) per research [R-010](./research.md): import `internal/testutil`; embed `testutil.SentinelSecret(2)` in `IssueParams.RequestID`; drive every rejection category (alg-none, alg-HS256, expired, wrong-IP, out-of-scope, revoked, exhausted, unknown-session-type); for each error, walk the wrap chain via `errors.Unwrap`; assert `testutil.AssertSentinelAbsent(t, sentinel, errStr)` at every level. Also assert each error matches the expected sentinel via `errors.Is`. Per FR-014, SC-009, SC-013, invariant I-016.

### Implementation for User Story 7

- [X] T057 [US7] Wire the expiry mapping into `Validate` in [internal/token/validate.go](../../internal/token/validate.go): when `jwt.ParseWithClaims` returns an error, check `errors.Is(err, jwt.ErrTokenExpired)` first → return `ErrTokenExpired`; otherwise (signature failure, malformed payload, etc.) → return `ErrAlgorithmUnsupported`. Per [data-model.md §2.3](./data-model.md) step 4.
- [X] T058 [US7] Wire the `SessionType` vocabulary check into `Validate` in [internal/token/validate.go](../../internal/token/validate.go) as step 5 of the verification ordering ([data-model.md §2.3](./data-model.md)): after `ParseWithClaims` succeeds, call `validSessionType(claims.SessionType)` from claims.go; if false return `ErrUnknownSessionType`.
- [X] T059 [US7] Wire the scope check into `Validate` in [internal/token/validate.go](../../internal/token/validate.go) as step 6: walk `claims.Scope`; if `requestedSecret` is not a member return `ErrScopeViolation`.

**Checkpoint**: T053–T056 pass. Every rejection category surfaces a distinct sentinel; SC-009 (sentinel-leak) and SC-013 (external review) properties hold by construction and are runtime-witnessed.

---

## Phase 10: User Story 8 — Cleanup reclaims expired records (Priority: P2)

**Goal**: `Store.Cleanup` removes records whose `ExpiresAt <= now`, never touches the revoked set, never causes a revoked JTI to become re-usable, and runs race-clean alongside concurrent `Validate` calls.

**Independent test**: Populate the store with mixed-state entries (unexpired-not-revoked, unexpired-revoked, expired-not-revoked, expired-revoked); drive cleanup; assert expired-not-revoked entries are gone; revoked entries still reject.

### Tests for User Story 8 (TDD — write FIRST and ensure they FAIL) ⚠️

- [X] T060 [P] [US8] Write `TestStore_CleanupRemovesExpired` in [internal/token/store_test.go](../../internal/token/store_test.go): construct a store via `NewStoreWithTick(1*time.Millisecond)` with a closure-captured `nowFn` test seam (or use real time and short TTLs); add tokens with `ExpiresAt` in past and future; spawn `Cleanup(ctx)` in a goroutine; `time.Sleep(50*time.Millisecond)`; cancel ctx; assert past-`ExpiresAt` records are gone from `live` and future-`ExpiresAt` records remain. Per FR-012, invariant I-014-cleanup-side.
- [X] T061 [P] [US8] Write `TestStore_CleanupConcurrentWithValidate` in [internal/token/store_test.go](../../internal/token/store_test.go): spawn `Cleanup` and 50 goroutines calling `Validate` against various live tokens for 100ms; assert no panics and no race-detector warnings under `-race`. Per User Story 8 acceptance scenario 3.
- [X] T062 [P] [US8] Write `TestStore_CleanupNeverTouchesRevoked` in [internal/token/store_test.go](../../internal/token/store_test.go): add a token with `ExpiresAt` in past; revoke it; trigger cleanup; assert `Validate` against the JTI still returns `ErrTokenRevoked` (not `ErrTokenExpired` — the revoked-set check fires first in `Get`). Per FR-011/FR-012, complements T047.

### Implementation for User Story 8

- [X] T063 [US8] Implement `Store.Cleanup(ctx context.Context)` in [internal/token/store.go](../../internal/token/store.go) per [data-model.md §5.5](./data-model.md): create `time.NewTicker(s.tick)`; defer `ticker.Stop()`; in a loop `select` on `ctx.Done()` (return) and `ticker.C` (call `sweepExpired()`).
- [X] T064 [US8] Implement the file-private `(s *memStore) sweepExpired()` in [internal/token/store.go](../../internal/token/store.go): take write lock; capture `now := s.nowFn()`; iterate `s.live`; `delete(s.live, jti)` for entries where `!t.ExpiresAt.After(now)`. Do NOT touch `s.revoked`.

**Checkpoint**: T060–T062 pass. FR-012 satisfied; cleanup never resurrects revoked JTIs; race-clean under concurrent validation.

---

## Phase 11: Polish & Cross-Cutting Concerns

**Purpose**: Add the constitutional fuzz target, run the full gate suite, mirror the locked API into `docs/PACKAGE-MAP.md`, update `docs/AC-MATRIX.md` AC-4 row, and confirm 100% coverage.

### Fuzz target

- [X] T065 [P] Write `FuzzJWTValidate` in [internal/token/validate_fuzz_test.go](../../internal/token/validate_fuzz_test.go) per research [R-011](./research.md): `f.Add` each of the 5 seed entries from [data-model.md §7.3](./data-model.md); the fuzz body calls `token.Validate(t.Context(), encoded, pub, store, "100.64.0.1", "FAKE_SECRET")`; if `err != nil`, assert `errors.Is(err, ...)` matches one of the 7 sentinels OR `context.Canceled` / `context.DeadlineExceeded`; assert `Validate` never panics.
- [X] T066 [P] Create the fuzz seed corpus files in [internal/token/testdata/fuzz/FuzzJWTValidate/](../../internal/token/testdata/fuzz/FuzzJWTValidate/): `empty` (zero bytes), `malformed-base64` (`not.valid.base64`), `valid-base64-bad-json` (3 base64url-encoded segments of non-JSON bytes joined by `.`), `alg-none` (real JWT-shape with `{"alg":"none"}` header + empty signature), `alg-hs256` (real JWT-shape with `{"alg":"HS256"}` header + HMAC signature). Per [data-model.md §7.3](./data-model.md).

### Gates

- [X] T067 Run `magex format:fix` from the repo root; commit any whitespace/import-ordering fixups inside the implement combined commit. Required by the chunk contract Prompt 5 step list.
- [X] T068 Run `magex lint` from the repo root; resolve any new `golangci-lint` findings on `internal/token/` until clean. Required by the chunk contract Prompt 5 step list.
- [X] T069 Run `magex test:race` from the repo root; assert all tests pass with `-race` clean (no warnings, no failures). The load-bearing race target is `TestStore_ConcurrentDecrement` (T051). Required by the chunk contract Prompt 5 step list.
- [X] T070 Run `go test -fuzz=FuzzJWTValidate -fuzztime=60s ./internal/token/` from the repo root; assert no panic, no new corpus rows representing crashes. Required by the chunk contract Prompt 5 step 2 (the constitutional fuzz target #2 60-second gate).
- [X] T071 Run `go test -cover ./internal/token/` from the repo root; assert coverage = **100%** per Constitution VIII Critical band ("Vault crypto, key derivation, JWT, ECIES, request signing"). If any line is uncovered, add a targeted test rather than weakening coverage. SC-010 / AC-9.

### Documentation updates

- [X] T072 [P] Append an `## Exported API — locked at SDD-07` section to [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) under the `internal/token` heading, listing the 17 exported identifiers from [contracts/api.md](./contracts/api.md): `SessionType` (+ 2 constants), `Claims`, `Token`, `IssueParams`, `Store`, `NewStore`, `NewStoreWithTick`, `Issue`, `Validate`, and the 7 sentinels.
- [X] T073 [P] Update the `AC-4` row in [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) with the new test file paths: `internal/token/issue_test.go`, `internal/token/validate_test.go`, `internal/token/store_test.go`, `internal/token/revoke_test.go`, `internal/token/alg_es256k_test.go`, `internal/token/claims_test.go`, `internal/token/errors_test.go`, `internal/token/validate_fuzz_test.go`.
- [X] T074 [P] Mark SDD-07 status `done` in [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md).

### Combined commit

- [X] T075 Stage and create a single combined commit (chunk contract Prompt 5 step 8): `git add internal/token/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md specs/007-token-jwt/tasks.md go.mod go.sum && git commit -m "feat(token): ES256K JWT issue/validate/store/revoke (SDD-07)"`. Do NOT commit between phases — the implement phase is one combined commit.

---

## Dependencies & Execution Order

### Phase dependencies

- **Setup (Phase 1)**: No dependencies — start immediately.
- **Foundational (Phase 2)**: Depends on Setup. **BLOCKS all user stories.**
- **User stories (Phase 3+)**: All depend on Foundational. Within a story, tests precede implementation. Stories may proceed in parallel across multiple developers, but the natural dependency chain is US1 → (US2, US3, US4, US5, US6, US7) → US8 because US1 wires the happy-path skeleton of `Validate` that subsequent phases extend.
- **Polish (Phase 11)**: Depends on US1–US8 complete. The fuzz target (T065) can be drafted earlier in parallel but only the gate runs (T067–T071) require everything else done.

### User-story dependency map

- **US1 (P1)**: After Foundational — wires the happy-path `Issue`/`Validate` skeleton.
- **US2 (P1)**: After US1 — adds the IP check to the verification ordering.
- **US3 (P1)**: After US1 — exercises `ConsumeUse` exhaustion + supervisor short-circuit.
- **US4 (P1)**: After US1 — adds `validateAlgorithm` pre-check to `Validate`.
- **US5 (P1)**: After US1 — implements `Store.Revoke` and wires the store revocation check.
- **US6 (P1)**: After US1, US3 — race-clean burst test against the `ConsumeUse` write-lock.
- **US7 (P1)**: After US1, US2, US3, US4, US5 — adds the remaining rejection categories (expired, scope, unknown-session-type) and the sentinel-leak witness.
- **US8 (P2)**: After US5 — wires `Cleanup` and asserts revoked-set survival.

### Within each user story

- Tests MUST be written BEFORE implementation per Constitution VIII (TDD).
- Tests MUST FAIL before implementation begins (red-green-refactor).
- Implementation tasks within a story may run sequentially; the story is "complete" only when every test passes.

### Parallel opportunities

- All Setup tasks marked [P] (T003) can run in parallel with T001/T002.
- All Foundational tests T004–T009 are independent and run in parallel.
- All Foundational implementation files T010–T016 are different files and run in parallel after their tests are drafted.
- Within each user-story phase, [P]-marked test tasks run in parallel.
- Documentation updates T072–T074 are independent files and run in parallel.

---

## Parallel Example: Foundational Phase

```bash
# Tests (write together, FAIL together):
Task: "Write TestErrors_DistinctIdentities in internal/token/errors_test.go"
Task: "Write TestRegisterOnce_Concurrent in internal/token/alg_es256k_test.go"
Task: "Write TestES256KMethod_RoundTrip in internal/token/alg_es256k_test.go"
Task: "Write TestSessionType_Vocabulary in internal/token/claims_test.go"
Task: "Write TestClaims_JSONRoundTrip in internal/token/claims_test.go"
Task: "Write TestNewStore_Defaults in internal/token/store_test.go"

# Implementation (different files, run in parallel after tests are drafted):
Task: "Create internal/token/errors.go with 7 sentinel declarations"
Task: "Create internal/token/alg_es256k.go with es256kMethod + Register()"
Task: "Create internal/token/claims.go with SessionType + Claims + validSessionType"
Task: "Create internal/token/issue.go with IssueParams + Token + generateJTI + Issue stub"
Task: "Create internal/token/validate.go with Validate stub"
Task: "Create internal/token/revoke.go with Revoke stub"
```

## Parallel Example: User Story 1

```bash
# All US1 tests are different test functions in the same package — write together:
Task: "Write TestIssue_Interactive in internal/token/issue_test.go"
Task: "Write TestIssue_Supervisor in internal/token/issue_test.go"
Task: "Write TestIssue_FreshJTIPerCall in internal/token/issue_test.go"
Task: "Write TestIssue_HeaderAlg in internal/token/issue_test.go"
Task: "Write TestIssue_RejectsUnknownSessionType in internal/token/issue_test.go"
Task: "Write TestIssue_RejectsInvalidParams in internal/token/issue_test.go"
Task: "Write TestIssue_RespectsCancelledContext in internal/token/issue_test.go"
Task: "Write TestValidate_HappyPath in internal/token/validate_test.go"
Task: "Write TestValidate_DecrementsInteractive in internal/token/validate_test.go"
Task: "Write TestValidate_RespectsCancelledContext in internal/token/validate_test.go"
```

---

## Implementation Strategy

### MVP first (User Story 1 only)

1. Complete Phase 1 (Setup).
2. Complete Phase 2 (Foundational) — CRITICAL, blocks all stories.
3. Complete Phase 3 (US1) — Issue + Validate happy path, both session types round-trip.
4. **STOP and VALIDATE**: `go test ./internal/token/...` passes US1 tests. AC-4 happy path demonstrated.

### Incremental delivery

1. Setup + Foundational → package compiles, sentinels declared.
2. US1 → MVP: round-trip works.
3. US2 → IP check.
4. US3 → exhaustion + supervisor short-circuit.
5. US4 → alg-confusion defence.
6. US5 → revocation + lifetime-of-store guarantee.
7. US6 → concurrency proof.
8. US7 → every rejection category named + sentinel-leak witness.
9. US8 → cleanup memory reclamation.
10. Polish → fuzz target + gates + doc updates + combined commit.

### Hard gates (Phase 11)

The implement phase is **not done** until all five gates pass clean:

1. `magex format:fix` — formatting fixed (T067).
2. `magex lint` — `golangci-lint` clean on `internal/token/` (T068).
3. `magex test:race` — all tests pass under `-race` (T069); `TestStore_ConcurrentDecrement` is the load-bearing race exercise.
4. `go test -fuzz=FuzzJWTValidate -fuzztime=60s ./internal/token/` — 60 s clean, no panic, no new bug corpus (T070).
5. `go test -cover ./internal/token/` — coverage = **100%** (T071).

### Constitutional alignment

- **Constitution III (Layer 2)**: ES256K signing locked; alg-confusion defence proven for `none` AND `HS256` independently (US4).
- **Constitution IV (TTL discipline)**: SUPERVISOR is TTL-only (US3 proves); INTERACTIVE is TTL+max-uses (US3, US6 prove).
- **Constitution VIII (TDD + 100% + fuzz target #2)**: Tests precede implementation in every phase; coverage gate is 100%; `FuzzJWTValidate` 60 s clean.
- **Constitution IX (no init, ctx-first, no fire-and-forget goroutines)**: `Register()` uses `sync.Once` from inside `Issue`/`Validate`; `Cleanup` is synchronous and caller-owned; every public function takes `ctx context.Context` as the first parameter.
- **Constitution X (no JWT bytes in logs/errors)**: Static-message sentinels by construction; `TestValidate_NoLeakOnError` (T056) is the runtime witness.
- **Constitution XI (justified deps)**: One new direct dependency (`golang-jwt/jwt/v5`) — justification in research [R-001](./research.md).

---

## Notes

- [P] tasks operate on different files and have no incomplete dependencies.
- [Story] label maps each task to a spec user story for traceability against AC-4.
- Each user story is independently completable — once Foundational is done, stories may be developed in parallel by different developers (modulo the natural US1 → others dependency).
- TDD discipline is mandatory: a test task MUST be written and FAIL before its paired implementation task is allowed to start.
- All commits are deferred to a single combined commit at T075 (chunk contract Prompt 5 step 8). Do NOT commit between phases.
- Avoid introducing helpers, abstractions, or files beyond what plan.md and the chunk contract name. The package surface is locked at SDD-07.
