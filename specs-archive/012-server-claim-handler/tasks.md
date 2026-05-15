# Tasks: Server `/claim` Handler (SDD-12)

**Input**: Design documents from `/specs/012-server-claim-handler/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/api.md, quickstart.md
**Branch**: `012-server-claim-handler`
**Coverage target**: ≥ 95% on the new code in `internal/server/claim_handler.go` (Constitution VIII High tier)

**Tests**: Constitution VIII makes TDD mandatory — every behaviour contract gets a failing test FIRST, before the implementation task that turns it green. Test tasks are not optional in this chunk.

**Organization**: Tasks are grouped by user story (P1 → P2). All six user stories share the single file `internal/server/claim_handler.go`, so within-story `[P]` markers are scarce; cross-story parallelism is also tightly bounded — see "Dependencies & Execution Order" before starting work in parallel.

## Format: `[ID] [P?] [Story?] Description`

- **[P]**: Different file, no dependency on incomplete tasks — safe to run in parallel.
- **[Story]**: User-story tag (US1..US6); Setup, Foundational, and Polish phases carry no story tag.
- File paths are absolute relative to repo root (e.g., `internal/server/claim_handler.go`).

## Path Conventions

- Production code: `internal/server/claim_handler.go`, `internal/server/server.go`, `internal/server/errors.go`
- Config additions: `internal/config/server.go`, `internal/config/defaults.go`, `internal/config/server_test.go`
- Unit tests (race): `internal/server/claim_handler_test.go`
- Integration test (`//go:build integration`): `internal/server/claim_handler_integration_test.go`
- Documentation: `docs/PACKAGE-MAP.md`, `docs/AC-MATRIX.md`, `docs/SDD-PLAYBOOK.md`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Land the additive config and chassis-error surface the handler depends on. These four edits must precede ALL handler work because `claim_handler.go` imports the new config field, the new sentinels, and the new `Deps.ClientKeyResolver` field.

- [X] T001 [P] Add `ClaimApprovalTimeout time.Duration` field to `CryptoSection` in [internal/config/server.go](internal/config/server.go) with TOML key `claim_approval_timeout`; reject values < 1 s or > 10 min in `(*Server).Validate` (DoS-via-config ceiling per research.md R-003).
- [X] T002 [P] Add `DefaultClaimApprovalTimeout = 60 * time.Second` to [internal/config/defaults.go](internal/config/defaults.go) and wire it into the `*Server` zero-value initializer.
- [X] T003 [P] Extend [internal/config/server_test.go](internal/config/server_test.go) with `TestCryptoSection_ClaimApprovalTimeout_Default` (asserts 60 s default) and `TestCryptoSection_ClaimApprovalTimeout_OutOfRange` (asserts validation rejects 0 s, 500 ms, and 11 min).
- [X] T004 Append five chassis-level sentinel errors to [internal/server/errors.go](internal/server/errors.go): `ErrApproverDenied`, `ErrApproverTimeout`, `ErrApproverUnavailable`, `ErrApproverRateLimited`, `ErrClientUnknown` — package-level `var` declarations using `errors.New` with stable lowercase messages (no echoed input). No test stub here; the sentinels are exercised in handler tests.

**Checkpoint**: Setup done — config field is parsed/defaulted/validated; chassis sentinels are linkable from the handler. `magex test:race ./internal/config/...` passes; `magex test:race ./internal/server/...` still passes (sentinels are unused but compile clean).

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Lock the `Deps.ClientKeyResolver` extension point, the `RegisterHandlers()` entry point shell, and the `AuditClaimOutcome` event-type constant — the surface every story phase will plug into. **No US task may start until this phase is green.**

⚠️ **CRITICAL**: All user-story work below depends on these symbols existing.

### Tests for Foundational (TDD-mandatory) ⚠️

> Write FAILING tests first; only then add the symbols.

- [X] T005 In [internal/server/server_test.go](internal/server/server_test.go) add `TestDeps_ClientKeyResolver_DefaultLoadsRegistry` — constructs `New` with `Deps.ClientKeyResolver=nil` and `cfg.Server.ClientRegistry` pointing at a `t.TempDir()` JSON file with one fingerprint→pubkey entry; asserts the default loader resolves it.
- [X] T006 In [internal/server/server_test.go](internal/server/server_test.go) add `TestDeps_ClientKeyResolver_Override` — supplies a custom resolver via `Deps`; asserts `New` does not read disk.
- [X] T007 In [internal/server/server_test.go](internal/server/server_test.go) add `TestRegisterHandlers_MountsClaimRoute` — calls `s.RegisterHandlers()` and asserts `POST /claim` is mounted on the chassis router (verify via `s.Router().Match(...)` or equivalent inspection helper from SDD-10).

### Implementation for Foundational

- [X] T008 In [internal/server/server.go](internal/server/server.go) add optional field `ClientKeyResolver func(fingerprint string) (*ecdsa.PublicKey, error)` to `Deps`; in `New` install a default that, when nil, loads `cfg.Server.ClientRegistry` once and serves an in-memory `map[string]*ecdsa.PublicKey`. Lookup miss returns `ErrClientUnknown` (T004). Loader is a private helper, no new exported symbol.
- [X] T009 In [internal/server/claim_handler.go](internal/server/claim_handler.go) (new file) declare `func (s *Server) RegisterHandlers() error` that calls `s.Mount(http.MethodPost, "/claim", s.handleClaim)`. Body of `handleClaim` is a stub returning `http.StatusNotImplemented` for now — story phases fill the pipeline.
- [X] T010 In [internal/server/claim_handler.go](internal/server/claim_handler.go) declare `const AuditClaimOutcome AuditEventType = "claim_outcome"` and a private allow-list builder `buildAuditDetail(outcome string, scope []string, sessionType SessionType, grantedTTL time.Duration, jti string) map[string]string` that emits ONLY the keys listed in [data-model.md §5](specs/012-server-claim-handler/data-model.md) — never `signature`, `nonce`, `ephemeral_pubkey`, `reason`, `jwt`, `client_key_fingerprint`.

**Checkpoint**: Foundation ready. `magex test:race` passes T005–T007. `s.RegisterHandlers()` mounts the route; `handleClaim` returns 501. User-story phases can now proceed.

---

## Phase 3: User Story 1 — Operator approves valid claim, server issues JWT (Priority: P1) 🎯 MVP

**Goal**: A fully verified, approved claim returns `200 {jwt, expires_at, jti}` with the TTL capped at the per-session-type maximum, scope/IP/session bound into the JWT, and exactly one `outcome=approved` audit event.

**Independent Test**: With `fakeApprover` returning `(Decision{Approved:true, GrantedTTL: capped}, nil)` and a memoised `*ecdsa.PublicKey` resolver, POST a signed valid claim; assert 200 with all three response fields, JWT-decoded `exp` matches the cap, audit event count is 1 with `outcome=approved`.

### Tests for User Story 1 (TDD-mandatory) ⚠️

> Write FAILING tests first; only then turn them green by completing the implementation tasks below.

- [X] T011 [US1] Build the test harness in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): `newTestHarness(t, ...opt)` returning `{server, recordingAudit, slogBuf, signKey, pub}`; helpers `signedClaimBody(t, h, opts)` (canonical-JSON sign over the `[scope, reason, ttl, session_type, ephemeral_pubkey, nonce, timestamp, request_id, machine_name]` set per [data-model.md §1](specs/012-server-claim-handler/data-model.md)), `withApprover(fn)`, `withClientKey(fp, pub)`, `withConfigCrypto(...)`. Match the pattern in [quickstart.md](specs/012-server-claim-handler/quickstart.md) §"Drive the happy path".
- [X] T012 [US1] Write `TestClaim_Approved_IssuesJWT` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): asserts 200; response body has exactly the three keys `jwt`, `expires_at`, `jti`; the body has NO `scope`/`reason`/`ttl`/`nonce`/`signature`/`ephemeral_pubkey`/`machine_name`/`client_key_fingerprint`/`request_id` keys (FR-020); audit event count is 1; `audit.Events[0].Type == AuditClaimOutcome`; `Detail["outcome"] == "approved"`; `Detail["session_type"] == "interactive"`; `Detail["scope"]` is the sorted-joined names; `Detail["granted_ttl"]` is set; `Detail["jti"]` matches the response.
- [X] T013 [US1] Write `TestClaim_TTLCappedAtConfigMax` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): submits `ttl = cfg.Crypto.MaxInteractiveTTL + 1*time.Hour`; asserts (a) the value passed to `fakeApprover.RequestApproval` equals the cap (FR-016 / SC-005 — operator sees actual TTL), (b) the response `expires_at` parses to `start.Add(cap)` ± clock skew, (c) JWT-decoded `exp` matches the cap.
- [X] T014 [US1] Write `TestClaim_SupervisorRequest_DaemonLabel` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): submits `session_type=supervisor`; asserts (a) the cap used is `cfg.Crypto.MaxSupervisorTTL`, (b) the issued JWT carries `session_type=supervisor` and `max_uses=0`, (c) the audit event's `Detail["session_type"] == "supervisor"`.
- [X] T015 [US1] Write `TestClaim_TTLZeroOrNegative_400` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): submits `ttl="0s"` (and a separate sub-test for `ttl="-5m"`); asserts 400 `bad_request`, no approver call, audit `outcome=bad-request`.
- [X] T016 [US1] Write `TestClaim_BadRequest_400` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go) covering: malformed JSON; unknown extra field; missing `scope`; missing/empty/malformed `request_id` (FR-009 — server-generated id appears in response body); `session_type` not in enum. Each sub-test asserts 400, no approver call, audit `outcome=bad-request`, response body shape `{error: "bad_request", request_id: <chassis hex>}`.

### Implementation for User Story 1

- [X] T017 [US1] In [internal/server/claim_handler.go](internal/server/claim_handler.go) define the private types `claimRequest`, `claimResponse`, `errorResponse` per [data-model.md §1–§3](specs/012-server-claim-handler/data-model.md). `claimRequest` JSON-decoded with `json.NewDecoder(http.MaxBytesReader(...)).DisallowUnknownFields()`. `errorResponse` always emits exactly two keys via an explicit struct (no `omitempty` on either field). Request decoding lives in `parseClaimRequest(r *http.Request) (*claimRequest, error)`.
- [X] T018 [US1] In [internal/server/claim_handler.go](internal/server/claim_handler.go) implement `handleClaim` shape stage: read body, decode into `claimRequest`, validate every required-field rule from [data-model.md §1](specs/012-server-claim-handler/data-model.md) (regex on `scope` elements, `ttl > 0`, `session_type` enum, `request_id` regex+length, `nonce` length+charset, hex/base64 forms), and on any failure emit one audit event with `outcome=bad-request` and respond `400 {error: "bad_request", request_id}`. The chassis-assigned `RequestID(r.Context())` is the value in the response — never the (possibly malformed) client `request_id`.
- [X] T019 [US1] In [internal/server/claim_handler.go](internal/server/claim_handler.go) implement TTL-cap helper `capTTL(req.SessionType, req.TTL, cfg.Crypto) time.Duration` returning `min(req.TTL, MaxInteractiveTTL|MaxSupervisorTTL)`. Cap MUST be applied BEFORE invoking the approver (FR-016) so the operator's prompt and the issued JWT carry the same value.
- [X] T020 [US1] In [internal/server/claim_handler.go](internal/server/claim_handler.go) wire the post-shape pipeline up to and including `Approver.RequestApproval` for the **success** path: build the canonical-JSON payload (via `sign.CanonicalJSON` over the field set per [contracts/api.md](specs/012-server-claim-handler/contracts/api.md)), resolve the client key via `s.deps.ClientKeyResolver`, call `sign.Verify`, call `sign.NonceCache.Add`, call `sign.IsFreshTimestamp`, recheck IP allowlist, derive `ctx, cancel := context.WithTimeout(r.Context(), cfg.Crypto.ClaimApprovalTimeout)`, call `s.approverImpl.RequestApproval(ctx, ApprovalRequest{...capped TTL...})`. On `(Decision{Approved:true}, nil)` AND `dec.GrantedTTL > 0` (defence-in-depth per research.md R-001), call `token.Issue(...)` and respond `200 {jwt, expires_at, jti}` with audit `outcome=approved`. (Other return values are owned by US2/US4/US5; they fall through to a temporary `503 unknown_outcome` until those phases land.)

**Checkpoint**: T011–T020 green; happy-path MVP works; the 503/408/429/403/Deny branches return `unknown_outcome` (filled in by later phases). Run: `go test -race -run '^TestClaim_(Approved|TTL|Supervisor|BadRequest)' ./internal/server/`.

---

## Phase 4: User Story 2 — Discord unavailable → 503 with NO auto-approve fallback (Priority: P1)

**Goal**: When the approver returns `ErrApproverUnavailable`, the handler returns `503 discord_unavailable`, issues NO token, emits one `outcome=discord-unavailable` audit event, AND no configuration permutation can flip this to 200 (Constitution II / SC-004).

**Independent Test**: Force `fakeApprover` to return `ErrApproverUnavailable` after a successful pre-approval pipeline; assert 503 with body `{error: "discord_unavailable", request_id}`, no `token.Issue` call, audit count 1 with `outcome=discord-unavailable`. Run the no-auto-approve grep over `internal/server/*.go`.

### Tests for User Story 2 (TDD-mandatory) ⚠️

- [X] T021 [US2] Write `TestClaim_DiscordUnavailable_503` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): asserts 503; body keys exactly `{error, request_id}`; `error == "discord_unavailable"`; `tokenIssuer.Calls == 0`; audit count 1, `outcome=discord-unavailable`.
- [X] T022 [US2] Write `TestClaim_NoAutoApproveKnobExists` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go) per [research.md R-007](specs/012-server-claim-handler/research.md): grep `internal/server/*.go` for the substring "auto" within five lines of "approve" (case-insensitive) using `os/exec` against a validated path; ANY match fails the test. Then exhaustively iterate `Deps` permutations (nil approver — should fail construction; fake-always-approve approver paired with a forced `ErrApproverUnavailable`; etc.) and assert handler still returns 503 under unavailable.
- [X] T023 [US2] Write `TestClaim_UnknownOutcome_503` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): forces `fakeApprover` to return `(Decision{}, errors.New("nonsense"))` AND a separate sub-test for `(Decision{Approved:false}, nil)`; both must return 503 `unknown_outcome` with `outcome=unknown-outcome` audit (FR-008 / data-model.md §4).

### Implementation for User Story 2

- [X] T024 [US2] In [internal/server/claim_handler.go](internal/server/claim_handler.go) extend the post-approver branch to translate `errors.Is(err, ErrApproverUnavailable)` → `503 discord_unavailable` with `outcome=discord-unavailable`. Add the unknown-outcome fall-through: any non-sentinel error OR `(Decision{Approved:false}, nil)` → `503 unknown_outcome` with `outcome=unknown-outcome`. There is exactly ONE place where the handler decides "issue a JWT", and it requires `errors.Is(err, nil) && dec.Approved && dec.GrantedTTL > 0`. Document that invariant inline with a brief comment that names Constitution II.

**Checkpoint**: T021–T023 green; no `auto` near `approve` in source; all unrecognised approver behaviour collapses to 503 fail-closed.

---

## Phase 5: User Story 3 — Pre-approval failures (signature, nonce, timestamp, IP) fail closed (Priority: P1)

**Goal**: Each pre-approval failure class returns the documented 403 with the documented static error code, never invokes the approver, redacts every client-supplied field except the chassis `request_id`, and emits exactly one audit event with the matching outcome label. The sentinel-leak test proves the redaction.

**Independent Test**: Submit one tampered claim per failure class; assert 403 + correct error code per row in [contracts/api.md](specs/012-server-claim-handler/contracts/api.md); approver call count is zero; audit event per claim with the matching label.

### Tests for User Story 3 (TDD-mandatory) ⚠️

- [X] T025 [US3] Write `TestClaim_BadSignature_403` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go) covering BOTH (a) signature does not verify against the registered key and (b) `client_key_fingerprint` is unknown (`ErrClientUnknown`) — same status, same `bad_signature` code (edge case "Client supplies an unknown registered-client-key fingerprint"); asserts approver call count == 0; audit `outcome=bad-signature`.
- [X] T026 [US3] Write `TestClaim_NonceReplay_403` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): drives two concurrent goroutines posting the same nonce; asserts exactly one wins the nonce check, the other receives 403 `nonce_replay`; approver invoked at most once (only for the winner); two audit events, one `outcome=approved` (or whatever the winner's terminal outcome is) and one `outcome=nonce-replay`.
- [X] T027 [US3] Write `TestClaim_StaleTimestamp_403` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go) with sub-tests for both directions (`now - skew - 1s` and `now + skew + 1s`); each asserts 403 `stale_timestamp`, approver not called, audit `outcome=stale-timestamp`.
- [X] T028 [US3] Write `TestClaim_IPNotAllowed_403` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): bypasses the SDD-10 socket-level allowlist (test injects `r.RemoteAddr` directly) so the handler's L7 recheck (FR-013, defence-in-depth) is what fires; asserts 403 `ip_not_allowed`, approver not called, audit `outcome=ip-not-allowed`.
- [X] T029 [US3] Write `TestClaim_ErrorBodyNoSentinel` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go) per [research.md R-006](specs/012-server-claim-handler/research.md): build a request with `reason = "SECRET_SHOULD_NEVER_APPEAR_12"`, force `sign.Verify` to return `ErrSignatureInvalid`; assert (a) `bytes.Contains(rr.Body.Bytes(), []byte("SECRET_SHOULD_NEVER_APPEAR_12")) == false`, (b) `bytes.Contains(slogBuf.Bytes(), []byte("SECRET_SHOULD_NEVER_APPEAR_12")) == false`, (c) the recorded `AuditEvent.Detail` map has no `reason` key (key absence, not just empty value). Use `testutil.AssertSentinelAbsent` from SDD-04 if available.
- [X] T030 [US3] Write `TestClaim_ShortCircuitOrdering` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go) (covers FR-002 / edge case "Multiple checks would fail"): a request that fails BOTH signature and timestamp must return `bad_signature` (not `stale_timestamp`); a request that fails BOTH nonce and IP must return `nonce_replay`. Confirms the locked order shape → sig → nonce → ts → ip.

### Implementation for User Story 3

- [X] T031 [US3] In [internal/server/claim_handler.go](internal/server/claim_handler.go) implement the explicit error-class translator: `errors.Is(err, sign.ErrSignatureInvalid) || errors.Is(err, ErrClientUnknown)` → 403 `bad_signature`; `errors.Is(err, sign.ErrNonceReplay) || firstSeen==false` → 403 `nonce_replay`; `!sign.IsFreshTimestamp(...)` → 403 `stale_timestamp`; peer IP not in `cfg.Network.AllowedCIDRs` → 403 `ip_not_allowed`. Each branch emits its corresponding outcome label via `buildAuditDetail` (note: on `bad_request` outcomes where the body did not parse, the `scope` key is OMITTED from `Detail` — see data-model.md §5).
- [X] T032 [US3] In [internal/server/claim_handler.go](internal/server/claim_handler.go) gate the operational `*slog.Logger` chain so log records contain ONLY `request_id`, `client_ip`, `outcome`, `scope_count` (not `scope`), `session_type`, capped TTL where applicable, and the bare error category. NEVER `signature`, `nonce`, `ephemeral_pubkey`, `reason`, JWT, machine name, or scope contents (Constitution X log-vs-audit asymmetry — audit MAY contain `scope`, ops log MAY NOT).

**Checkpoint**: T025–T030 green; all four pre-approval failure classes route correctly; sentinel never appears in body or ops log; ordering invariant locked.

---

## Phase 6: User Story 4 — Operator denies → 403 (Priority: P1)

**Goal**: When the approver returns `ErrApproverDenied`, the handler returns `403 denied` with no token issued and one `outcome=denied` audit event.

**Independent Test**: `fakeApprover` returns `ErrApproverDenied`; submit a fully verified valid claim; assert 403 + `denied` + redacted body + audit count 1.

### Tests for User Story 4 (TDD-mandatory) ⚠️

- [X] T033 [US4] Write `TestClaim_Denied_403` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): asserts 403; `error == "denied"`; body has exactly two keys; `tokenIssuer.Calls == 0`; audit count 1, `outcome=denied`.
- [X] T034 [US4] Write `TestClaim_RateLimited_429` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go) (FR-007a): `fakeApprover` returns `ErrApproverRateLimited`; asserts 429; `error == "rate_limited"`; audit `outcome=rate-limited`. This MUST be distinct from 503 `discord_unavailable` and 403 `denied`.

### Implementation for User Story 4

- [X] T035 [US4] In [internal/server/claim_handler.go](internal/server/claim_handler.go) extend the post-approver translator to map `errors.Is(err, ErrApproverDenied)` → 403 `denied` and `errors.Is(err, ErrApproverRateLimited)` → 429 `rate_limited`. Both emit their corresponding outcome label.

**Checkpoint**: T033–T034 green; deny vs. rate-limited are distinguishable on the wire.

---

## Phase 7: User Story 5 — Operator does not respond → 408 (Priority: P2)

**Goal**: When the per-server `claim_approval_timeout` deadline expires before the operator decides, the approver returns `ErrApproverTimeout` (or any `context.DeadlineExceeded` wrap), and the handler returns `408 approval_timeout` with one `outcome=approval-timeout` audit event.

**Independent Test**: `fakeApprover` blocks past `cfg.Crypto.ClaimApprovalTimeout` (set to 50 ms in the test) and then returns `ErrApproverTimeout`; assert 408 + `approval_timeout` + redacted body + audit count 1.

### Tests for User Story 5 (TDD-mandatory) ⚠️

- [X] T036 [US5] Write `TestClaim_DiscordTimeout_408` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): set `cfg.Crypto.ClaimApprovalTimeout = 50 * time.Millisecond`; `fakeApprover` selects on its own ctx and returns `ErrApproverTimeout` when `ctx.Done()` fires; assert (a) 408, (b) `error == "approval_timeout"`, (c) `tokenIssuer.Calls == 0`, (d) audit `outcome=approval-timeout`, (e) the elapsed wall-clock time is ≥ 50 ms and ≤ 500 ms (the deadline IS the cap, not the request TTL or any client field — FR-006).

### Implementation for User Story 5

- [X] T037 [US5] In [internal/server/claim_handler.go](internal/server/claim_handler.go) extend the post-approver translator to map `errors.Is(err, ErrApproverTimeout)` AND `errors.Is(err, context.DeadlineExceeded)` (defence-in-depth) → 408 `approval_timeout`. Confirm the `defer cancel()` pattern around the `context.WithTimeout` call (no leaked goroutines; ctx cancel happens on every return path).

**Checkpoint**: T036 green; timeout cleanly distinguished from deny (403) and unavailable (503).

---

## Phase 8: User Story 6 — Every outcome is recorded in audit log (Priority: P2)

**Goal**: Exactly one audit event per request, regardless of which outcome fired, with the locked field set, and never the forbidden fields.

**Independent Test**: Drive the handler through ALL eleven outcomes; capture all `AuditWriter` invocations; assert per-outcome count 1, label correct, allowed-key set per [data-model.md §5](specs/012-server-claim-handler/data-model.md), forbidden-key set absent.

### Tests for User Story 6 (TDD-mandatory) ⚠️

- [X] T038 [US6] Write `TestClaim_AuditEventEmittedForEveryOutcome` in [internal/server/claim_handler_test.go](internal/server/claim_handler_test.go): table-driven over all eleven `outcomeLabel` values (approved, bad-request, bad-signature, nonce-replay, stale-timestamp, ip-not-allowed, denied, approval-timeout, rate-limited, discord-unavailable, unknown-outcome); for each driver, assert (a) `len(audit.Events) == 1`, (b) `Events[0].Type == AuditClaimOutcome`, (c) `Detail["outcome"]` matches, (d) for non-`bad-request` rows: `Detail["session_type"]` and `Detail["scope"]` are present and non-empty, (e) for the `approved` row: `Detail["granted_ttl"]` and `Detail["jti"]` are present, (f) NEVER any of the forbidden keys: `signature`, `nonce`, `ephemeral_pubkey`, `reason`, `jwt`, `client_key_fingerprint` (assert via map-key inspection, not string scan, so a future field rename is caught).

### Implementation for User Story 6

- [X] T039 [US6] Walk every branch in [internal/server/claim_handler.go](internal/server/claim_handler.go) introduced by US1–US5 and confirm each terminates with exactly one call to `s.audit.Append(ctx, AuditEvent{...})` built via `buildAuditDetail` (no inline map literals — the allow-list builder is the only place audit details are constructed). If T038 surfaces a missing or duplicated emission, fix it here.

**Checkpoint**: T038 green; the audit obligation is structurally enforced through a single emission point per outcome.

---

## Phase 9: Polish & Cross-Cutting Concerns

**Purpose**: Integration leg, gates, coverage proof, and the docs-update step that closes out the chunk. Everything in this phase MUST pass clean before commit.

### Integration leg

- [X] T040 Write [internal/server/claim_handler_integration_test.go](internal/server/claim_handler_integration_test.go) with `//go:build integration`: `TestClaim_Integration_FullFlow_DiscordStub` wires `testutil.DiscordStub` from SDD-04 through a small in-test adapter `stubAsApprover` (translates `testutil.Decision{Approve|Deny|Timeout|Unavailable|RateLimited}` into the chassis sentinels from T004). Drives at least three end-to-end paths: stub approves → 200 + JWT decodes + audit `approved`; stub denies → 403 `denied` + audit `denied`; stub reports unavailable → 503 `discord_unavailable` + audit `discord-unavailable`. Reuses the `newTestHarness` helper from T011.

### Gates

- [X] T041 Run `magex format:fix` from repo root and confirm zero diff after the run (i.e., the implementation files were formatted before this step or the formatter only normalizes whitespace).
- [X] T042 Run `magex lint` and resolve any reported issue at root cause (no `//nolint` suppressions for newly-introduced code without an inline comment naming the constitutional reason).
- [X] T043 Run `magex test:race` and confirm 0 failures across the full repo (race-detector clean).
- [X] T044 Run `magex test:race -tags=integration` and confirm 0 failures in [internal/server/claim_handler_integration_test.go](internal/server/claim_handler_integration_test.go).
- [X] T045 Run `go test -race -cover -run '^TestClaim_' ./internal/server/` and verify coverage on the claim-handler portion of `internal/server` is ≥ 95% (Constitution VIII High tier; SC-007). If under, add the missing line/branch tests until ≥ 95%.

### No-auto-approve & sentinel-leak proof points (final assertion)

- [X] T046 Re-run `go test -race -run TestClaim_NoAutoApproveKnobExists ./internal/server/` standalone and confirm the grep finds no occurrences of "auto" near "approve" in `internal/server/*.go`. (Belt-and-braces: T043 already ran it; this re-run is the final pre-commit safety check.)
- [X] T047 Re-run `go test -race -run TestClaim_ErrorBodyNoSentinel ./internal/server/` standalone and confirm `SECRET_SHOULD_NEVER_APPEAR_12` is absent from the response body, slog buffer, and audit `Detail` keys. If this test ever fails, **stop and fix the code path**, do not adjust the test.

### Documentation

- [X] T048 [P] Update [docs/PACKAGE-MAP.md](docs/PACKAGE-MAP.md) under `internal/server`: append entry "POST /claim handler — see docs/API.md (locked at SDD-12)".
- [X] T049 [P] Update [docs/AC-MATRIX.md](docs/AC-MATRIX.md) — populate the AC-1, AC-3, and AC-4 rows with the new test file paths (`internal/server/claim_handler_test.go` and `internal/server/claim_handler_integration_test.go`).
- [X] T050 [P] Update [docs/SDD-PLAYBOOK.md](docs/SDD-PLAYBOOK.md) — mark SDD-12 status `done`.

### Combined commit (deferred per SDD-12 §"How to run this chunk")

- [X] T051 From repo root, stage and commit in a single commit:
  ```
  git add internal/server/ internal/config/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/012-server-claim-handler/tasks.md
  git commit -m "feat(server): /claim handler with no-auto-approve fail-closed (SDD-12)"
  ```
  Confirm `git status` is clean and the commit was created (do NOT push).

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)** → no dependencies; must complete before Phase 2.
- **Phase 2 (Foundational)** → depends on Phase 1; **BLOCKS** every user-story phase.
- **Phase 3 (US1) — MVP** → depends on Phase 2.
- **Phase 4 (US2)** → depends on Phase 3 (US1 introduces the approver-call wiring; US2 extends it).
- **Phase 5 (US3)** → depends on Phase 3 (introduces the post-shape pre-approval pipeline that US3's tests target). Does **not** depend on US2 directly.
- **Phase 6 (US4)** → depends on Phase 4 (US4 extends the approver-translator branch).
- **Phase 7 (US5)** → depends on Phase 6 (US5 extends the same translator).
- **Phase 8 (US6)** → depends on Phase 7 (audit obligation can only be exhaustively asserted once every outcome branch exists).
- **Phase 9 (Polish)** → depends on Phase 8.

### Within Each Story

- Tests (T0xx) MUST be written and observed FAILING before the corresponding implementation task in the same story (Constitution VIII).
- Within US1: T011 (harness) blocks T012–T016. T017 (types) blocks T018 (shape) blocks T019 (TTL cap) blocks T020 (pipeline up to approver).
- Within US2/US4/US5: each story extends the same `handleClaim` function — only one in-flight implementation task per phase.
- Within US3: T031 must follow T025–T030; T032 (log redaction) is independent of T031 and may run in parallel **only if both authors avoid the same edit window** in `claim_handler.go`.

### Cross-story file conflicts

`internal/server/claim_handler.go` is one file; **all US implementation tasks edit it**. Therefore implementation tasks across user-story phases CANNOT run in parallel — only test-writing tasks (T012–T016, T021–T023, T025–T030, T033–T034, T036, T038) can be parallelised, since they all live in `claim_handler_test.go` and follow Go's table-test-friendly style (small additive functions, no shared mutable state).

`claim_handler_test.go` is also one file, but its top-level test functions are independent and may be authored concurrently provided the harness (T011) lands first.

### Parallel Opportunities

- **Phase 1**: T001, T002, T003 marked `[P]` — three different files (`internal/config/server.go`, `internal/config/defaults.go`, `internal/config/server_test.go`) with no inter-task dependency.
- **Phase 2**: T005, T006, T007 (all in `server_test.go`) can be authored serially (same file) but the implementation tasks T008/T009/T010 touch three distinct symbols and may run sequentially in `claim_handler.go` (one author).
- **Phase 9**: T048, T049, T050 marked `[P]` — three different doc files.

### Strict serial chain

`T011 (harness) → T017 (types) → T018 (shape) → T019 (cap) → T020 (pipeline) → T024 (US2 translator) → T031 (US3 translator) → T035 (US4) → T037 (US5) → T039 (US6 audit walk) → T040 (integration) → T041…T051`. This is the longest dependency chain in the chunk.

---

## Parallel Example: User Story 3 test authoring

```bash
# After T011 (harness) is in place and T017–T020 (US1 implementation) has landed,
# the US3 tests are independent enough to be drafted concurrently:
Task: "Write TestClaim_BadSignature_403 in internal/server/claim_handler_test.go"
Task: "Write TestClaim_NonceReplay_403 in internal/server/claim_handler_test.go"
Task: "Write TestClaim_StaleTimestamp_403 in internal/server/claim_handler_test.go"
Task: "Write TestClaim_IPNotAllowed_403 in internal/server/claim_handler_test.go"
Task: "Write TestClaim_ErrorBodyNoSentinel in internal/server/claim_handler_test.go"
Task: "Write TestClaim_ShortCircuitOrdering in internal/server/claim_handler_test.go"
```

Note the file-conflict caveat above: parallel authors must coordinate non-overlapping insertion points in `claim_handler_test.go`.

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Complete Phase 1 (Setup) — config field + chassis sentinels + Deps extension.
2. Complete Phase 2 (Foundational) — `RegisterHandlers`, `AuditClaimOutcome`, `buildAuditDetail`.
3. Complete Phase 3 (US1) — happy path + TTL cap + supervisor label + bad-request 400.
4. **STOP and VALIDATE**: `go test -race -run '^TestClaim_(Approved|TTL|Supervisor|BadRequest)' ./internal/server/` is green. The MVP issues real JWTs to approved claims.
5. The handler is **not yet shippable** — Phase 4 (US2) is constitutionally mandatory before merge because the no-auto-approve fail-closed property is non-negotiable. (Stop here only for review; do NOT merge.)

### Incremental Delivery

1. Setup + Foundational → skeleton in place.
2. + US1 → MVP demonstrable.
3. + US2 → constitutional fail-closed proven; THIS is the earliest reviewable point.
4. + US3 → all four pre-approval failure classes covered; sentinel-leak proven.
5. + US4 → deny + rate-limited covered.
6. + US5 → timeout covered.
7. + US6 → audit obligation exhaustively proven.
8. + Polish → integration leg, gates, coverage, docs, single combined commit.

### Parallel Team Strategy (only weakly applicable here)

This chunk has one production file (`claim_handler.go`) and one unit-test file. Two authors can work the test file (one writes US1+US3 tests, one writes US2+US4+US5+US6 tests) **provided the harness from T011 is shared first**. The implementation tasks (T017–T039) are intrinsically serial — single author through the implementation walk.

---

## Notes

- `[P]` tasks = different files, no incomplete-task dependency.
- `[Story]` label maps task to spec.md user story (US1..US6).
- Tests MUST FAIL before implementation. If a test passes immediately, the harness is mocking too much — re-examine.
- Commit ALL of `internal/server/`, `internal/config/`, and the three doc files in **one** commit at T051. Do NOT commit between phases (per SDD-12 §"How to run this chunk").
- Avoid: bypassing T046/T047 (the no-auto-approve grep and sentinel-leak test are constitutional safeguards), suppressing `magex lint` findings on new code, lowering the 95% coverage gate.
- Coverage is measured on `internal/server` claim-handler portion (`-run '^TestClaim_'`); not the whole repo.
- The five sentinel errors in T004 are the chassis-level abstraction; the production wiring (cmd/hush, future SDD-14) installs an adapter that translates `internal/discord` sentinels into these — the handler stays decoupled from the discord package.

---

## Task Count Summary

| Phase | Story | Tasks | Test tasks | Impl tasks |
|-------|-------|-------|------------|------------|
| 1. Setup | — | T001–T004 (4) | 1 (T003) | 3 |
| 2. Foundational | — | T005–T010 (6) | 3 (T005–T007) | 3 |
| 3. User Story 1 | US1 | T011–T020 (10) | 6 (T011–T016) | 4 |
| 4. User Story 2 | US2 | T021–T024 (4) | 3 (T021–T023) | 1 |
| 5. User Story 3 | US3 | T025–T032 (8) | 6 (T025–T030) | 2 |
| 6. User Story 4 | US4 | T033–T035 (3) | 2 (T033–T034) | 1 |
| 7. User Story 5 | US5 | T036–T037 (2) | 1 (T036) | 1 |
| 8. User Story 6 | US6 | T038–T039 (2) | 1 (T038) | 1 |
| 9. Polish | — | T040–T051 (12) | 1 (T040 — integration) | 11 |
| **Total** | | **51** | **24** | **27** |

**Independent test criteria (per story, restated for traceability)**:

- **US1**: Approve fakeApprover + valid signed claim → 200 with `{jwt, expires_at, jti}`, capped TTL on the JWT, supervisor label flows through, audit `outcome=approved`.
- **US2**: ErrApproverUnavailable → 503 `discord_unavailable`, no token, no source code reference to `auto-approve`.
- **US3**: Tampered sig / replayed nonce / stale ts / disallowed IP → 403 with the matching static error code, approver never invoked, sentinel `SECRET_SHOULD_NEVER_APPEAR_12` absent from body and ops log.
- **US4**: ErrApproverDenied → 403 `denied`; ErrApproverRateLimited → 429 `rate_limited` (distinct from 503/403).
- **US5**: Approver blocks past `cfg.Crypto.ClaimApprovalTimeout` → 408 `approval_timeout`, deadline driven by config, not request TTL.
- **US6**: All eleven outcomes each emit exactly one `AuditClaimOutcome` event with the locked field set and no forbidden keys.

**Suggested MVP scope**: US1 + US2 (Phase 3 + Phase 4) — happy path plus constitutional fail-closed. This is the smallest reviewable cut; merge gate also requires US3 (sentinel-leak) per Constitution VIII and X.

**Format validation**: All 51 tasks use the strict checklist format `- [ ] T0NN [P?] [USx?] Description with file path`. Setup, Foundational, and Polish tasks carry no `[Story]` label; user-story tasks all carry one. File paths are explicit (markdown-link-style relative paths) for every actionable task.
