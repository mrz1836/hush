# SDD-26 — `internal/supervise/validators` (interface + 5 builtins)

**Phase:** 6
**Package:** `internal/supervise/validators`
**Files:** `validators.go`, `anthropic.go`, `anthropic_oauth.go`, `openai.go`, `google_ai.go`, `github.go`, `*_test.go`
**Branch:** `026-validators-builtins` (created by the `before_specify` git hook)
**Blocked by:** SDD-21
**Blocks:** SDD-25 (some scenarios), SDD-27, SDD-28
**Primary AC:** AC-10 (FR-13)
**Coverage target:** 90%

**Behaviour contracts (MUST):**
- Validator interface uses `SecureBytes` (no `string`); copies into ephemeral `[]byte` via `Use(fn)` at HTTP-call time, then immediately zeroes the buffer
- Registry: `Get(name string)` returns the validator by config name
- Timeout default 5s, configurable via `NewWithClient`
- Errors are typed: `ErrStaleCredential` (401/403), `ErrValidatorTimeout`, `ErrValidatorNetwork`

**Anti-contracts (MUST NOT):**
- Hit live provider APIs in tests
- Log secret value or bearer header
- Run validators on the vault server

**Tests required:**
- One per provider: happy-path, 401-stale, 403-stale, network-timeout, network-error
- Sentinel-leak: per-provider `TestValidator_<Name>_NoLeakOnError` — feed `SECRET_SHOULD_NEVER_APPEAR_26`; trigger 401; assert sentinel absent from `err.Error()` AND captured logs

**Constitutional principles in scope:** V (operator visibility), VIII, X (no values in logs/errors)

**Exported API to lock in PACKAGE-MAP.md (this chunk — new entry):**
- `type Validator interface { Validate(ctx context.Context, secret *securebytes.SecureBytes) error }`
- `type Registry struct { ... }`
- `func NewRegistry(httpClient *http.Client) *Registry`
- `func (r *Registry) Get(name string) (Validator, bool)`
- `func NewWithClient(httpClient *http.Client) Validator` (per-provider — five funcs total)
- `var ErrStaleCredential, ErrValidatorTimeout, ErrValidatorNetwork`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-26 (internal/supervise/
validators: Validator interface + 5 builtins) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles V, VIII)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-13, AC-10)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenario 6 — pre-flight credential validation)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (validator authoring)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-26.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
Validators are pre-flight credential checkers: before the supervisor
spawns the child with a freshly-fetched credential, it can ask "is
this credential actually valid against the upstream provider?".
This chunk delivers the Validator interface plus five built-in
implementations covering the providers hush operators most commonly
gate (anthropic, anthropic-oauth, openai, google-ai, github).

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- Every Validator takes a SecureBytes-wrapped credential. The
  raw bytes MUST never be materialised as a Go string. Each
  validator copies bytes into a freshly-allocated []byte via
  SecureBytes.Use(fn), uses it for the HTTP call, then zeroes
  the local buffer immediately.
- Each validator distinguishes three error classes via typed
  errors: ErrStaleCredential (provider returned 401/403),
  ErrValidatorTimeout (request exceeded the configured timeout),
  ErrValidatorNetwork (any other transport-level failure).
- The default request timeout is 5 seconds; operators may
  override via the constructor.
- Validators NEVER log the credential value or the Authorization
  header.
- Tests MUST NOT hit live provider APIs — every test uses
  net/http/httptest with per-provider response fixtures.
- The five names are fixed: anthropic, anthropic-oauth,
  openai, google-ai, github (matches SDD-18's allow-list).

The spec MUST NOT encode HOW (no library names, no specific HTTP
URLs unless they're part of the public spec). Those are plan-phase.

Acceptance criterion: AC-10 (FR-13).

Action — run exactly one command:
  /speckit-specify "internal/supervise/validators: pre-flight credential checker interface and five builtins (anthropic, anthropic-oauth, openai, google-ai, github); credential is SecureBytes (never materialised as string); typed errors distinguish stale (401/403) vs timeout vs network; default 5s timeout; never logs value or Authorization header; tests use httptest, never live providers"

The before_specify hook will create branch 026-validators-builtins.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-26 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-26.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-26 (internal/supervise/
validators) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; V/VIII/X load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-13)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenario 6)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (validator authoring guide — operator extension contract)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (no internal/supervise/validators entry yet)
- /Users/mrz/projects/hush/docs/sdd/SDD-26.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/supervise/validators (NEW)
- Files: validators.go (Validator interface + Registry +
  errors + shared HTTP plumbing), anthropic.go, anthropic_oauth.go,
  openai.go, google_ai.go, github.go, validators_test.go,
  anthropic_test.go, anthropic_oauth_test.go, openai_test.go,
  google_ai_test.go, github_test.go
- Exported API:
    type Validator interface {
        Validate(ctx context.Context, secret *securebytes.SecureBytes) error
    }
    type Registry struct { ... }
    func NewRegistry(httpClient *http.Client) *Registry
    func (r *Registry) Get(name string) (Validator, bool)
    // per-provider constructors:
    func NewAnthropic(httpClient *http.Client) Validator
    func NewAnthropicOAuth(httpClient *http.Client) Validator
    func NewOpenAI(httpClient *http.Client) Validator
    func NewGoogleAI(httpClient *http.Client) Validator
    func NewGitHub(httpClient *http.Client) Validator
    var ErrStaleCredential, ErrValidatorTimeout, ErrValidatorNetwork

Implementation contract (HOW — locked):
- validators.go defines the shared HTTP machinery: an internal
  helper that takes a target URL + Authorization-header builder
  callback. The callback receives a SecureBytes via Use(fn),
  emits the header value into a local []byte that's zeroed
  immediately after http.Client.Do returns.
- Each provider file implements one Validator, configured with
  the provider's well-known credential-check endpoint:
    anthropic        → GET /v1/messages with HEAD-style probe
                       (no message body — just auth check) OR
                       the documented credential-validation
                       endpoint if Anthropic publishes one.
    anthropic-oauth  → variant using OAuth bearer token semantics.
    openai           → GET /v1/models (lists models, fast,
                       returns 401 if creds bad).
    google-ai        → equivalent docs-published endpoint.
    github           → GET /user (returns 401 on bad token).
- Phase-0 research note for SDD-26: confirm each provider's
  cheapest-possible credential-check endpoint and document any
  that has changed (these endpoints are external dependencies;
  document the pinned URL).
- Status code mapping:
    200 → nil
    401 or 403 → ErrStaleCredential
    timeout → ErrValidatorTimeout
    other transport error → ErrValidatorNetwork
- Default http.Client.Timeout = 5*time.Second; constructor
  accepts an *http.Client to allow override.
- Registry: package-level constructor wires the five names to
  their constructors; Get(name) returns (Validator, true) or
  (nil, false).
- Tests use httptest.Server per provider with response fixtures
  (200 + 401 + 403 + slow + connection-refused). NEVER hit a
  live provider.
- Sentinel-leak tests per provider: feed
  SECRET_SHOULD_NEVER_APPEAR_26 wrapped in SecureBytes;
  trigger the 401 path; assert the sentinel is absent from
  err.Error() AND from any captured slog output.

Coverage target: 90%.
Constitutional principles in scope: V, VIII, IX, X.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-26 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-26.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 90%. For each of the five providers (anthropic, anthropic-oauth, openai, google-ai, github), tasks required: TestValidator_<Name>_HappyPath_200, TestValidator_<Name>_StaleCredential_401, TestValidator_<Name>_StaleCredential_403, TestValidator_<Name>_Timeout, TestValidator_<Name>_NetworkError, TestValidator_<Name>_NoLeakOnError (sentinel SECRET_SHOULD_NEVER_APPEAR_26). Plus shared tests: TestRegistry_GetByName, TestRegistry_GetUnknownName_FalseFound, TestValidator_NeverMaterializesString (manual code review checklist task — assert Use(fn) pattern in every provider). All tests use httptest.Server — NEVER live providers. Final phase MUST include magex format:fix, magex lint, magex test:race."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-26 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-26.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 90% on internal/supervise/validators:
     go test -cover ./internal/supervise/validators/
3. Confirm all five provider sentinel-leak tests passed —
   SECRET_SHOULD_NEVER_APPEAR_26 absent from every err.Error()
   and captured log.
4. Confirm no live-provider hits — grep tests for production
   hostnames (api.anthropic.com, api.openai.com, etc.) and
   confirm any matches are inside httptest.Server URL
   construction only.
5. Append a NEW internal/supervise/validators entry to
   docs/PACKAGE-MAP.md titled "Exported API — locked at SDD-26"
   listing the locked API from the chunk doc (Validator,
   Registry, NewRegistry, Get, the five New* funcs, the three
   Err* sentinels).
6. Update docs/AC-MATRIX.md AC-10 row with the new test file paths
   (FR-13 entry).
7. Mark SDD-26 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add internal/supervise/validators/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(supervise/validators): 5 builtin credential validators (SDD-26)"

Final message: confirm gates passed, race-clean, coverage ≥ 90%,
all five validators wired, sentinel-leak tests confirm no value
in error messages, no live-provider hits in tests, AC-10 row
updated, SDD-PLAYBOOK updated, and the combined commit created.
```
