# SDD-07 — `internal/token` (ES256K JWT issuance + validation + store)

**Phase:** 2
**Package:** `internal/token`
**Files:** `claims.go`, `issue.go`, `validate.go`, `store.go`, `revoke.go`, `alg_es256k.go`, `*_test.go`, `validate_fuzz_test.go`
**Branch:** `007-token-jwt` (created by the `before_specify` git hook)
**Blocked by:** SDD-01, SDD-02, SDD-06
**Blocks:** SDD-12, SDD-13, SDD-23
**Primary AC:** AC-4
**Coverage target:** 100%; **fuzz target #2** (JWT parse/validate)

**Behaviour contracts (MUST):**
- Register ES256K signing method via `jwt.RegisterSigningMethod` ONCE through a `sync.Once`-gated `Register()` function called by `Issue` / `Validate` (NOT `init()` — Constitution IX bans `init`)
- Reject `jwt.SigningMethodNone` and any non-ES256K alg explicitly (alg-confusion defence)
- `crypto/rand` for `jti` (UUIDv4)
- `Validate` IP comparison uses `netip.Addr` equality
- `Store` uses `sync.RWMutex`; `ConsumeUse` decrements atomically (or returns `ErrTokenExhausted`) — race tests must pass
- Supervisor session type is TTL-only (ignores `MaxUses`); interactive consumes uses

**Anti-contracts (MUST NOT):**
- Use `init()`
- Use mutable package globals (the `sync.Once` for ES256K registration is a bounded exception)
- Cache verify keys globally — accept as parameter
- Log encoded JWT strings

**Tests required:**
- Unit (every claim validation branch): `TestIssue_Interactive`, `TestIssue_Supervisor`, `TestValidate_HappyPath`, `TestValidate_ExpiredJWT`, `TestValidate_WrongIP`, `TestValidate_OutOfScope`, `TestValidate_AlgConfusion_None_Refused`, `TestValidate_AlgConfusion_HS256_Refused`, `TestValidate_UnknownSessionType_Refused`, `TestStore_RevokedJTI_Refused`, `TestStore_ExhaustedInteractive_Refused`, `TestStore_SupervisorIgnoresMaxUses`, `TestStore_CleanupRemovesExpired`
- Fuzz: `FuzzJWTValidate` ≥60s clean — random JWT-shaped bytes; assert no panic
- Race: `TestStore_ConcurrentDecrement` — multiple goroutines decrementing `max_uses` on the same `jti`; exactly N decrements observed, no double-decrement

**Constitutional principles in scope:** III (Layer 2), IV (TTL discipline), VIII (100% coverage + fuzz target #2), IX (no `init`), X (no JWT contents in logs)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `type SessionType string`  (`SessionInteractive`, `SessionSupervisor`)
- `type Claims struct { jwt.RegisteredClaims; Scope []string; ClientIP string; RequestID string; MaxUses int; EphemeralPubKey string; SessionType SessionType }`
- `type Token struct { JTI string; Encoded string; ExpiresAt time.Time; SessionType SessionType; MaxUses int }`
- `type Store interface { Add(t *Token) error; Get(jti string) (*Token, error); ConsumeUse(jti string) error; Revoke(jti string) error; Cleanup(ctx context.Context) }`
- `func NewStore() Store`
- `func Issue(ctx context.Context, signKey *ecdsa.PrivateKey, params IssueParams) (*Token, error)`
- `func Validate(ctx context.Context, encoded string, verifyKey *ecdsa.PublicKey, store Store, requestIP string, requestedSecret string) (*Claims, error)`
- `var ErrAlgorithmUnsupported, ErrTokenRevoked, ErrTokenExhausted, ErrIPMismatch, ErrScopeViolation, ErrUnknownSessionType, ErrTokenExpired`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-07 (internal/token: ES256K
JWT issuance + validation + store) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles III, IV, VIII — Layer 2, TTL discipline, TDD)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-4, FR-9, AC-4)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 2, JWT claims table — every claim documented here is load-bearing)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-4 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-07.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/token package issues and validates ES256K-signed JWTs
that gate every secret retrieval. Two session types exist:
INTERACTIVE (short TTL, max-uses-bounded) and SUPERVISOR (longer
TTL, unbounded uses within TTL). It is consumed by SDD-12 (claim
handler), SDD-13 (secret handler + revoke), and SDD-23 (supervise
CLI).

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- Tokens MUST be signed with ES256K (Bitcoin-style secp256k1
  ECDSA). The validator MUST explicitly reject the "none"
  algorithm AND any other algorithm (alg-confusion attack
  defence — both must have named-error rejections).
- INTERACTIVE tokens have both a TTL and a max-uses count;
  SUPERVISOR tokens have only a TTL (max-uses is ignored).
  Both session types MUST be distinct, named values.
- Validation MUST check: signature, expiry, requested-secret-in-
  scope, client-IP-equality, store-side revocation, store-side
  use-exhaustion. Each failure mode is a distinct, named error.
- The token store is concurrent-safe: many goroutines may
  decrement the same interactive token's use count; the total
  decrement count MUST equal the number of successful calls.
- A revoked token CANNOT be re-issued; revocation persists
  for the lifetime of the store (cleanup removes only expired
  entries).
- The unique JTI is generated from a cryptographically secure
  random source.

The spec MUST NOT encode HOW (no library names, no struct field
layouts beyond the claim names). Those are plan-phase.

Acceptance criterion: AC-4 (token issue/validate/revoke/expire).

Action — run exactly one command:
  /speckit-specify "internal/token: issue + validate + store + revoke ES256K-signed JWTs gating every secret retrieval; two session types (INTERACTIVE TTL+max-uses, SUPERVISOR TTL-only); explicit alg-confusion defence; concurrent-safe use-count decrement"

The before_specify hook will create branch 007-token-jwt.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-07 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-07.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-07 (internal/token: ES256K
JWT) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; III/IV/VIII/IX/X are load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-4, FR-9, AC-4)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 2, JWT claims table)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/token — the API contract you will lock)
- /Users/mrz/projects/hush/docs/sdd/SDD-07.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/token
- Files: claims.go (Claims + SessionType), issue.go (Issue +
  IssueParams), validate.go, store.go (Store interface + memory
  impl), revoke.go, alg_es256k.go (signing method registration
  via sync.Once), claims_test.go, issue_test.go, validate_test.go,
  store_test.go, revoke_test.go, validate_fuzz_test.go
- Exported API:
    type SessionType string  // SessionInteractive, SessionSupervisor
    type Claims struct { jwt.RegisteredClaims; Scope []string; ClientIP string; RequestID string; MaxUses int; EphemeralPubKey string; SessionType SessionType }
    type Token struct { JTI string; Encoded string; ExpiresAt time.Time; SessionType SessionType; MaxUses int }
    type IssueParams struct { ... }
    type Store interface { Add(t *Token) error; Get(jti string) (*Token, error); ConsumeUse(jti string) error; Revoke(jti string) error; Cleanup(ctx context.Context) }
    func NewStore() Store
    func Issue(ctx context.Context, signKey *ecdsa.PrivateKey, params IssueParams) (*Token, error)
    func Validate(ctx context.Context, encoded string, verifyKey *ecdsa.PublicKey, store Store, requestIP string, requestedSecret string) (*Claims, error)
    var ErrAlgorithmUnsupported, ErrTokenRevoked, ErrTokenExhausted, ErrIPMismatch, ErrScopeViolation, ErrUnknownSessionType, ErrTokenExpired

Implementation contract (HOW — locked):
- JWT lib: github.com/golang-jwt/jwt/v5. ES256K is a custom signing
  method registered via jwt.RegisterSigningMethod gated by a
  package-level sync.Once invoked from a Register() helper that
  Issue and Validate call before doing any JWT work. NO init().
- ES256K signing method delegates to github.com/bitcoinschema/go-bitcoin
  for sign/verify (already locked by SDD-01).
- Validate's algorithm check: parse the header, refuse if
  alg != "ES256K". Explicitly map "none" and "HS256" to
  ErrAlgorithmUnsupported in distinct test cases (proof of
  alg-confusion defence).
- jti generated via crypto/rand → UUIDv4 (use a deterministic
  helper in tests via injectable rng).
- IP comparison: netip.ParseAddr both sides; compare with ==.
- Store backed by sync.RWMutex + map[string]*Token. ConsumeUse
  takes the write lock, checks expired/revoked/exhausted, then
  decrements MaxUses (or returns immediately for SUPERVISOR).
- Cleanup runs in a caller-controlled goroutine via Store.Cleanup(ctx);
  tick interval injectable in tests.

Coverage target: 100%. Fuzz target: FuzzJWTValidate (60s gate).
Constitutional principles in scope: III, IV, VIII, IX, X.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-07 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-07.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 100%. Tests required: TestIssue_Interactive, TestIssue_Supervisor, TestValidate_HappyPath, TestValidate_ExpiredJWT, TestValidate_WrongIP, TestValidate_OutOfScope, TestValidate_AlgConfusion_None_Refused, TestValidate_AlgConfusion_HS256_Refused, TestValidate_UnknownSessionType_Refused, TestStore_RevokedJTI_Refused, TestStore_ExhaustedInteractive_Refused, TestStore_SupervisorIgnoresMaxUses, TestStore_CleanupRemovesExpired, TestStore_ConcurrentDecrement (race-clean, exactly N decrements). Fuzz: FuzzJWTValidate — random JWT-shaped bytes, no panic. Final phase MUST include magex format:fix, magex lint, magex test:race, and go test -fuzz=FuzzJWTValidate -fuzztime=60s ./internal/token/"

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-07 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-07.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Fuzz (60s minimum, no crashes / no new bug corpus):
     go test -fuzz=FuzzJWTValidate -fuzztime=60s ./internal/token/
3. Verify coverage = 100% on internal/token:
     go test -cover ./internal/token/
4. Confirm alg-confusion defence proven by both
   TestValidate_AlgConfusion_None_Refused AND
   TestValidate_AlgConfusion_HS256_Refused passing.
5. Confirm TestStore_ConcurrentDecrement is race-clean and exactly
   N decrements observed.
6. Append "Exported API — locked at SDD-07" section to
   docs/PACKAGE-MAP.md under internal/token listing the locked
   API from the chunk doc.
7. Update docs/AC-MATRIX.md AC-4 row with the new test file paths.
8. Mark SDD-07 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/token/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(token): ES256K JWT issue/validate/store/revoke (SDD-07)"

Final message: confirm gates passed, fuzz 60s clean, coverage =
100%, alg-confusion attacks rejected (none + HS256), supervisor
TTL-only behaviour proven, race-clean ConsumeUse, AC-4 row
updated, SDD-PLAYBOOK updated, and the combined commit created.
```
