# SDD-12 — Server `/claim` handler

**Phase:** 3
**Package:** `internal/server`
**Files:** `claim_handler.go`, `claim_handler_test.go`, `claim_handler_integration_test.go` (`//go:build integration`)
**Branch:** `012-server-claim-handler` (created by the `before_specify` git hook)
**Blocked by:** SDD-07, SDD-08, SDD-10, SDD-11
**Blocks:** SDD-13, SDD-25
**Primary AC:** AC-1, AC-3, AC-4
**Coverage target:** 95%

**Behaviour contracts (MUST):**
- Validate request: JSON shape per `docs/API.md`; canonicalise → verify signature → check nonce/timestamp → check IP allowlist
- Cap TTL to config-defined max per `session_type`
- Call `Approver.RequestApproval`; map decision:
  - Approve → `token.Issue` + 200 with JSON `{jwt, expires_at, jti}`
  - Deny → 403 `{error: "denied"}`
  - Timeout → 408 `{error: "approval_timeout"}`
  - `ErrDiscordUnavailable` → 503 `{error: "discord_unavailable"}` (Constitution II — fail closed, no auto-approve fallback)
- Audit event for every outcome
- Error responses: no echoing of request body fields beyond `request_id`

**Anti-contracts (MUST NOT):**
- Fall back to auto-approve on Discord error (Constitution II)
- Include nonce or signature in error responses
- Log JWT contents

**Tests required:**
- Unit: `TestClaim_BadSignature_403`, `TestClaim_NonceReplay_403`, `TestClaim_StaleTimestamp_403`, `TestClaim_DiscordTimeout_408`, `TestClaim_DiscordUnavailable_503`, `TestClaim_Approved_IssuesJWT`, `TestClaim_SupervisorRequest_DaemonLabel`
- Integration (`//go:build integration`): full flow with `DiscordStub` from SDD-04
- Sentinel-leak: `TestClaim_ErrorBodyNoSentinel` — build a request with `reason=SECRET_SHOULD_NEVER_APPEAR_12`; force `ErrSignatureInvalid`; assert sentinel absent from response body and logs

**Constitutional principles in scope:** II (no auto-approve), IV (TTL discipline), VIII, X (no JWT or request body in error/log)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- The handler is registered via `Server.RegisterHandlers` entry point defined in SDD-10. This chunk does NOT add a new exported package symbol — the locked API is the HTTP route + request/response JSON shape from `docs/API.md`. PACKAGE-MAP entry: "POST /claim handler — see docs/API.md".

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-12 (server /claim handler)
of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles II, IV, VIII, X)
- /Users/mrz/projects/hush/docs/API.md  (POST /claim spec — request and response shapes are load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-7, FR-9, FR-19, AC-1, AC-3, AC-4)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenario 1, Scenario 10)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-1/3/4 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-12.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The /claim handler is the entry point for every secret request: it
verifies the client's signed payload, presents the approval to the
operator via Discord, and on approval mints an ES256K JWT bound to
the requested secret + client IP + TTL + max-uses. It is the
constitutional choke point that enforces "no auto-approve under
any circumstance" (Constitution II).

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- Every request goes through: shape validation → canonicalise +
  signature verify → nonce + timestamp freshness → IP allowlist.
  Each failure mode returns the documented HTTP status and a
  body that contains ONLY a request_id and a static error code.
- Approver dispatch MUST surface four outcomes, each with a
  documented HTTP status:
    Approve  → 200 {jwt, expires_at, jti}
    Deny     → 403 {error: "denied"}
    Timeout  → 408 {error: "approval_timeout"}
    ErrDiscordUnavailable → 503 {error: "discord_unavailable"}
- The 503 path MUST NEVER fall back to auto-approve (Constitution
  II — non-negotiable).
- The TTL the server issues is capped at the config max for the
  session type (interactive vs supervisor).
- Every outcome (success and every error) emits an audit event.
- Error response bodies MUST NEVER echo the nonce, signature, or
  any request body field beyond the server-generated request_id.

The spec MUST NOT encode HOW (no library names, no internal type
shapes). Those are plan-phase.

Acceptance criteria: AC-1, AC-3, AC-4.

Action — run exactly one command:
  /speckit-specify "POST /claim handler: shape validation → canonical-JSON signature verify → nonce/timestamp freshness → IP allowlist → Discord approval (Approve→200 with JWT, Deny→403, Timeout→408, DiscordUnavailable→503 with NO auto-approve fallback); audit every outcome; error bodies contain only request_id + static error code"

The before_specify hook will create branch 012-server-claim-handler.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-12 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-12.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-12 (server /claim handler) of
the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; II/IV/VIII/X load-bearing)
- /Users/mrz/projects/hush/docs/API.md  (POST /claim — full request/response/error spec)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-7, FR-9, FR-19, AC-1, AC-3, AC-4)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenario 1 happy path; Scenario 10 unavailable Discord)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/server)
- /Users/mrz/projects/hush/docs/sdd/SDD-12.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/server
- Files: claim_handler.go (HTTP handler + request/response types),
  claim_handler_test.go, claim_handler_integration_test.go
  (//go:build integration)
- The handler is registered via Server.RegisterHandlers (the entry
  point defined in SDD-10). This chunk does NOT add new package-
  level exported symbols — the contract is the HTTP route + JSON
  shapes documented in docs/API.md.

Implementation contract (HOW — locked):
- Handler signature: (s *Server) handleClaim(w http.ResponseWriter, r *http.Request).
  Wrapped by the middleware stack from SDD-10.
- Pipeline: ParseRequest → CanonicalJSON+Verify (SDD-08) → NonceCache.Add (SDD-08) → IsFreshTimestamp (SDD-08) → IP allowlist (config) → Approver.RequestApproval (SDD-11 via interface from SDD-10) → token.Issue (SDD-07) → Audit.Append (SDD-13 audit writer if available; if not yet wired, log INFO and proceed).
- TTL cap: min(requestedTTL, cfg.MaxTTL[sessionType]). Apply BEFORE
  Approver.RequestApproval so the operator sees the actual TTL.
- Status mapping (NO exceptions):
    bad signature      → 403 {error: "bad_signature", request_id}
    nonce replay       → 403 {error: "nonce_replay", request_id}
    stale timestamp    → 403 {error: "stale_timestamp", request_id}
    IP not allowed     → 403 {error: "ip_not_allowed", request_id}
    approver Deny      → 403 {error: "denied", request_id}
    approver Timeout   → 408 {error: "approval_timeout", request_id}
    ErrDiscordUnavailable → 503 {error: "discord_unavailable", request_id}
    approver Approve   → 200 {jwt, expires_at, jti}
- Audit data MUST omit the JWT, the signature, the nonce, and any
  reason field that may contain user input. Log the request_id,
  client IP, scope, session_type, decision.
- The 503 path MUST NOT include any code branch that flips to
  Approve. The test TestClaim_DiscordUnavailable_503 proves it.

Coverage target: 95%.
Constitutional principles in scope: II, IV, VIII, X.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-12 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-12.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 95%. Tests required: TestClaim_BadSignature_403, TestClaim_NonceReplay_403, TestClaim_StaleTimestamp_403, TestClaim_IPNotAllowed_403, TestClaim_DiscordTimeout_408, TestClaim_DiscordUnavailable_503 (proves no-auto-approve), TestClaim_Approved_IssuesJWT, TestClaim_SupervisorRequest_DaemonLabel, TestClaim_TTLCappedAtConfigMax, TestClaim_AuditEventEmittedForEveryOutcome. Integration test (//go:build integration) wires DiscordStub from SDD-04 for full flow. Sentinel-leak: TestClaim_ErrorBodyNoSentinel sets reason=SECRET_SHOULD_NEVER_APPEAR_12, forces ErrSignatureInvalid, asserts absence from response body AND captured logs. Final phase MUST include magex format:fix, magex lint, magex test:race, and magex test:race -tags=integration."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-12 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-12.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Integration tests:
     magex test:race -tags=integration
3. Verify coverage ≥ 95% on internal/server (claim handler portion):
     go test -cover ./internal/server/ -run Claim
4. Confirm TestClaim_DiscordUnavailable_503 proves no-auto-approve
   fallback exists.
5. Confirm TestClaim_ErrorBodyNoSentinel passed —
   SECRET_SHOULD_NEVER_APPEAR_12 absent from response body AND
   captured logs.
6. Note in docs/PACKAGE-MAP.md under internal/server: "POST /claim
   handler — see docs/API.md (locked at SDD-12)".
7. Update docs/AC-MATRIX.md AC-1, AC-3, AC-4 rows with the new
   test file paths.
8. Mark SDD-12 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/server/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(server): /claim handler with no-auto-approve fail-closed (SDD-12)"

Final message: confirm gates passed (unit + integration), coverage
≥ 95%, 503 on Discord unavailable proven, sentinel-leak passed,
AC-1/3/4 rows updated, SDD-PLAYBOOK updated, and the combined
commit created.
```
