# Tasks: Server `/s`, `/revoke`, `/hz` Handlers + Audit Log (SDD-13)

**Input**: Design documents from `/Users/mrz/projects/hush/specs/013-server-handlers-and-audit/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/api.md, contracts/audit.md
**Branch**: `013-server-handlers-and-audit`

**Tests**: TDD-mandatory per Constitution VIII. Every behaviour contract gets a failing test BEFORE its implementation. Coverage targets enforced by gate tasks: `internal/server` new files ≥ 95 %; `internal/audit` = 100 %.

**Organization**: Tasks are grouped by user story so each P1 story can be implemented and tested independently.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: User story label (US1..US8). Setup, Foundational, and Polish phases have no story label.
- All paths are repository-relative (rooted at `/Users/mrz/projects/hush/`).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Create the new package skeleton, doc files, and per-file coverage gate. No production logic lands here.

- [x] T001 Create [internal/audit/doc.go](internal/audit/doc.go) with package documentation: package summary, Constitution III Layer 6 mapping, link to [docs/SECURITY.md](docs/SECURITY.md) Layer 6 section, link to [specs/013-server-handlers-and-audit/contracts/audit.md](specs/013-server-handlers-and-audit/contracts/audit.md)
- [x] T002 [P] Create empty stubs (package decl + import-block placeholders only) for [internal/audit/chain.go](internal/audit/chain.go), [internal/audit/writer.go](internal/audit/writer.go), [internal/audit/discord_mirror.go](internal/audit/discord_mirror.go) so subsequent test files compile against `package audit`
- [x] T003 [P] Create empty stubs (package decl only) for [internal/server/secret_handler.go](internal/server/secret_handler.go), [internal/server/revoke_handler.go](internal/server/revoke_handler.go), [internal/server/health_handler.go](internal/server/health_handler.go), [internal/server/audit_adapter.go](internal/server/audit_adapter.go) so subsequent test files compile against `package server`
- [x] T004 [P] Add per-file 100 % coverage gate at [internal/audit/coverage_test.go](internal/audit/coverage_test.go) mirroring [internal/vault/coverage_test.go](internal/vault/coverage_test.go); list `chain.go`, `writer.go`, `discord_mirror.go` as the gated set per R-013

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Locked exported types, sentinel errors, single-method accessors, and the chassis surface additions every user story consumes. Each task ships a test file alongside the source change so the foundational surface is itself test-covered.

**⚠️ CRITICAL**: No user-story phase may begin until this phase is complete. All P1 stories depend on these accessors and types existing.

### Accessors on existing stores (single-method additive extensions per R-011)

- [x] T005 [P] Add `TestStore_Names_ReturnsSortedCopy_NoValues` to [internal/vault/store_test.go](internal/vault/store_test.go) covering: returns sorted name list; never returns secret values; concurrent-safe under `t.Parallel()` race
- [x] T006 [P] Implement `(s *Store) Names() []string` accessor in [internal/vault/store.go](internal/vault/store.go) returning a sorted copy of the loaded-secret name set under the existing `sync.RWMutex`
- [x] T007 [P] Add `TestStore_ActiveCount_ExcludesRevokedAndExpired` to [internal/token/store_test.go](internal/token/store_test.go) covering: counts only non-revoked, non-expired entries; concurrent-safe; clock fake controls expiry
- [x] T008 [P] Implement `(s *Store) ActiveCount() int` accessor in [internal/token/store.go](internal/token/store.go) returning the live, non-revoked, non-expired entry count under the existing mutex
- [x] T009 [P] Add `TestBotApprover_Connected_TracksAvailability` to [internal/discord/bot_test.go](internal/discord/bot_test.go) covering: returns false before websocket ready; returns true after ready handler fires; returns false after disconnect handler fires
- [x] T010 [P] Implement `(a *BotApprover) Connected() bool` accessor in [internal/discord/bot.go](internal/discord/bot.go) (one-line `return a.available.Load()`) per R-009

### Chassis surface additions (per plan §"Project Structure")

- [x] T011 Add `Deps.DiscordHealth func() bool` optional field to [internal/server/server.go](internal/server/server.go) Deps struct + nil-safe accessor `(s *Server) discordHealth() bool` (returns false when field is nil — fail-closed per R-009)
- [x] T012 [P] Add `TestServer_DiscordHealth_NilFieldReportsDisconnected` and `TestServer_DiscordHealth_FieldHonoured` to [internal/server/server_test.go](internal/server/server_test.go) verifying the nil-safe and honoured paths
- [x] T013 [P] Append `ErrSecretMissing` sentinel to [internal/server/errors.go](internal/server/errors.go) (used by `/s` handler for in-scope-but-not-in-vault per data-model §6 + R-007)
- [x] T014 [P] Add `TestErrSecretMissing_DistinctSentinel` to [internal/server/errors_test.go](internal/server/errors_test.go) asserting the sentinel is non-nil, distinct from existing token sentinels, and `errors.Is(ErrSecretMissing, ErrSecretMissing)` holds

### Audit package data types (locked exported API per contracts/audit.md)

- [x] T015 Define `audit.Event` struct with json tags in [internal/audit/chain.go](internal/audit/chain.go) per data-model §1 (Seq, Time, Action, Data, PrevHash, Hash, Signature)
- [x] T016 [P] Define exported sentinels in [internal/audit/chain.go](internal/audit/chain.go): `ErrAuditChainBroken`, `ErrShutdown`, `ErrChainTailUnreadable`, `ErrInvalidPath`, `ErrInvalidKey` per contracts/audit.md "Exported sentinels"
- [x] T017 [P] Define `audit.ChainError` struct (Seq, Reason, Err) with `Error()` and `Unwrap()` methods in [internal/audit/chain.go](internal/audit/chain.go); `Reason` enum strings: `"hash_mismatch"`, `"signature_invalid"`, `"seq_gap"`, `"prev_hash_mismatch"`
- [x] T018 Define `audit.Writer` interface in [internal/audit/writer.go](internal/audit/writer.go) with `Append(ctx, action, data) error` and `Run(ctx) error` per contracts/audit.md
- [x] T019 [P] Define `audit.MirrorSession` interface and `audit.DiscordMirror` struct (unexported fields: `channelID`, `session MirrorSession`, `ch chan Event`, `logger *slog.Logger`) in [internal/audit/discord_mirror.go](internal/audit/discord_mirror.go); implement `NewDiscordMirror(channelID string, session MirrorSession) *DiscordMirror`
- [x] T020 [P] Define `genesisPrevHash` package-private constant in [internal/audit/chain.go](internal/audit/chain.go) computed as `sha256.Sum256([]byte("hush.audit.chain.v1.genesis"))` per R-002
- [x] T021 [P] Define the closed `Action` constant set in [internal/audit/chain.go](internal/audit/chain.go) (19 strings from data-model §6: `actionServerStart`, `actionServerStop`, `actionVaultReloaded`, `actionFilePermCheckFailed`, `actionDiscordDisconnected`, `actionDiscordReconnected`, `actionAuditMirrorFailed`, plus the 12 handler outcome strings)

**Checkpoint**: Foundation ready — every user story can now begin in parallel.

---

## Phase 3: User Story 1 — `/s` happy path returns an ECIES envelope (Priority: P1) 🎯 MVP

**Goal**: An approved-client GET to `/h/<prefix>/s/<name>` with a valid bearer JWT returns `200 application/octet-stream` whose body is `ecies.Encrypt(ephemeralPubKey, secretValue)`. Interactive tokens have `MaxUses` decremented exactly once; supervisor tokens never decrement. Exactly one `secret_retrieved` audit event is emitted carrying the secret name but never the secret value.

**Independent Test**: `TestSecret_HappyPath_ECIESPayload` (interactive) and `TestSecret_SupervisorIgnoresMaxUses` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go).

### Tests for User Story 1 (write FIRST; assert they FAIL before implementation)

- [x] T022 [P] [US1] Write `TestSecret_HappyPath_ECIESPayload` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): GET with valid interactive JWT (scope = name, IP-bound, `MaxUses=1`); assert 200, `Content-Type: application/octet-stream`, `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, body decrypts via test ephemeral private key to known plaintext, MaxUses decremented to 0, audit event seq=1 with `Action=secret_retrieved` carrying `secret_name` but NOT the plaintext value
- [x] T023 [P] [US1] Write `TestSecret_SupervisorIgnoresMaxUses` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): same as T022 but session_type=supervisor with no MaxUses field; assert 200, body decrypts correctly, no decrement attempted on the token store, exactly one audit event
- [x] T024 [P] [US1] Write `TestSecret_AuditEventEmittedForSuccess` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): asserts `Data` map contains `request_id`, `client_ip`, `secret_name`, `session_type`, `outcome=secret_retrieved`, and is missing `secret_value`, `jwt`, `signature` keys
- [x] T025 [P] [US1] Write secret-handler test harness helpers in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): `newSecretHarness(t, opts...)`, `withVaultSecret(name, value)`, `withInteractiveToken(...)`, `withSupervisorToken(...)`, `withScope(...)`, `withMaxUses(n)`, `withClientIP(ip)`, `withBearerToken(jwt)`, `fromIP(ip)`; harness installs a fake `audit.Writer` capturing events into `[]audit.Event` for assertions

### Implementation for User Story 1

- [x] T026 [US1] Implement `(s *Server) handleSecret(w, r)` in [internal/server/secret_handler.go](internal/server/secret_handler.go): extract Bearer JWT → call `token.Validate(ctx, encoded, s.jwtVerifyKey, s.tokenStore, peer.String(), name)` → on success call `s.vaultStore.Get(name)` → on miss return 404 with `ErrSecretMissing` audit outcome → on hit call `ecies.Encrypt(ctx, claims.EphemeralPubKey, value)` → write octet-stream body with the locked headers per [contracts/api.md](specs/013-server-handlers-and-audit/contracts/api.md) §1 (depends on T022–T025)
- [x] T027 [US1] Implement `buildSecretAuditDetail(outcome, requestID, clientIP, name, claims)` allow-list builder in [internal/server/secret_handler.go](internal/server/secret_handler.go) populating ONLY the documented keys per data-model §6 + plan Constitution-X row (no value, no JWT, no ephemeral pubkey, no signature)
- [x] T028 [US1] Mount `GET /s/{name}` in [internal/server/claim_handler.go](internal/server/claim_handler.go) `(s *Server) RegisterHandlers()` — append `s.Mount(http.MethodGet, "/s/{name}", s.handleSecret)` per plan §"Project Structure" footnote 3

**Checkpoint**: `/s` happy path round-trips end-to-end; both interactive and supervisor token paths covered; audit emits exactly one `secret_retrieved` event per success.

---

## Phase 4: User Story 2 — `/s` rejects out-of-scope, wrong-IP, expired, exhausted, malformed, revoked, unknown JTI (Priority: P1)

**Goal**: Every documented token-validation failure for `/s` returns the documented status with the static `error` body and `request_id`, leaves the vault unread, and emits exactly one outcome-specific audit event.

**Independent Test**: The `TestSecret_*_401`, `TestSecret_OutOfScope_403`, and `TestSecret_ErrorBodyNoSentinel` tests in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go).

### Tests for User Story 2 (write FIRST)

- [x] T029 [P] [US2] Write `TestSecret_ExpiredJWT_401` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): JWT with `exp` in the past; assert 401, body `{"error":"token_expired","request_id":"..."}`, vault not consulted, audit event `secret_token_expired`
- [x] T030 [P] [US2] Write `TestSecret_OutOfScope_403` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): JWT scope does not include the requested name; assert 403, body `{"error":"out_of_scope",...}`, vault not consulted, audit event `secret_out_of_scope`
- [x] T031 [P] [US2] Write `TestSecret_WrongIP_401` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): JWT bound to a different IP than `r.RemoteAddr`; assert 401 `bad_token`, vault not consulted, audit event `secret_ip_mismatch`
- [x] T032 [P] [US2] Write `TestSecret_ExhaustedInteractive_401` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): interactive JWT with `MaxUses=0`; assert 401 `bad_token`, vault not consulted, audit event `secret_token_exhausted`
- [x] T033 [P] [US2] Write `TestSecret_RevokedJWT_401` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): JTI marked revoked in token store; assert 401 `bad_token`, audit event `secret_token_revoked`
- [x] T034 [P] [US2] Write `TestSecret_UnknownJTI_401`, `TestSecret_MalformedJWT_401`, `TestSecret_BadSignature_401`, `TestSecret_MissingAuthHeader_401`, `TestSecret_UnsupportedScheme_401`, `TestSecret_AlgUnsupported_401` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): each asserts 401 `bad_token`, vault unread, distinct audit `outcome` label per R-007 mapping
- [x] T035 [P] [US2] Write `TestSecret_SecretMissingInVault_404` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): JWT validates, name in scope, vault has no entry; assert 404 `not_found`, audit `secret_missing` carrying the requested name (allowed because already in scope)
- [x] T036 [P] [US2] Write `TestSecret_BadName_400` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): URL path name fails `^[A-Z][A-Z0-9_]{0,63}$`; assert 400 `bad_request`, no token validation attempted, audit event with `bad_request` outcome
- [x] T037 [P] [US2] Write `TestSecret_VaultReadError_500` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): vault store fake returns a non-`ErrSecretMissing` error; assert 500 `internal_error`, audit `secret_internal_error`
- [x] T038 [P] [US2] Write `TestSecret_ECIESEncryptError_500` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go): ECIES fake returns error; assert 500 `internal_error`, audit `secret_internal_error`, body does not contain the plaintext sentinel
- [x] T039 [P] [US2] Write `TestSecret_ErrorBodyNoSentinel` in [internal/server/secret_handler_test.go](internal/server/secret_handler_test.go) using `testutil.SentinelSecret(13)` (= `SECRET_SHOULD_NEVER_APPEAR_13`) per R-012: inject sentinel as the vault value, drive every status row in the §"Status / `error` matrix" table, assert `testutil.AssertSentinelAbsent` holds for response body bytes AND captured slog buffer AND every audit event Data map for every row

### Implementation for User Story 2

- [x] T040 [US2] Extend `(s *Server) handleSecret` in [internal/server/secret_handler.go](internal/server/secret_handler.go) with the full token-validation sentinel → status table from R-007 (`ErrTokenExpired` → 401 `token_expired`; the rest of the 401 family → `bad_token`; `ErrScopeViolation` → 403 `out_of_scope`; `ErrSecretMissing` → 404 `not_found`; vault/ECIES/write errors → 500 `internal_error`) (depends on T029–T039)
- [x] T041 [US2] Implement static-body error writer `writeSecretError(w, r, status, code, requestID)` in [internal/server/secret_handler.go](internal/server/secret_handler.go): writes only `{"error":<code>,"request_id":<id>}`, sets `Content-Type: application/json; charset=utf-8`, `X-Content-Type-Options: nosniff`, `Cache-Control: no-store`; never echoes the JWT, the requested name beyond audit, the supplied bytes
- [x] T042 [US2] Wire post-validate audit emission to the appropriate outcome label per R-007 in [internal/server/secret_handler.go](internal/server/secret_handler.go); ensure exactly ONE `Append` call regardless of which branch returns, including the 500 paths (per FR-027)

**Checkpoint**: All eight token-validation rejection paths plus 400/404/500 paths are tested and behave to spec; sentinel-leak holds across every error row.

---

## Phase 5: User Story 3 — `/revoke` is signed-only, idempotent, anti-enumeration (Priority: P1)

**Goal**: `POST /h/<prefix>/revoke` accepts the body documented in [contracts/api.md](specs/013-server-handlers-and-audit/contracts/api.md) §2, runs canonical-JSON+verify against the same client-key registry as `/claim`, applies nonce + timestamp checks, and on success calls `token.Store.Revoke(jti)`. Idempotent re-revocation returns the identical 200 body; the audit chain distinguishes via `revoke_succeeded` vs `revoke_idempotent_already_revoked`. Unknown JTI is mapped to `bad_signature` per FR-015.

**Independent Test**: The `TestRevoke_*` tests in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go).

### Tests for User Story 3 (write FIRST)

- [x] T043 [P] [US3] Write revoke harness helpers in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go): `newRevokeHarness(t)`, `withRegisteredClient(fp, pub)`, `withInteractiveToken(jti, ...)`, `signedRevokeBody(t, h, jti)` (canonicalises body and signs with the registered private key, generating a fresh `crypto/rand` nonce per call per Failure-by-failure cheat sheet)
- [x] T044 [P] [US3] Write `TestRevoke_HappyPath` in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go): valid signed body, registered fingerprint, fresh nonce, fresh timestamp; assert 200 with body `{"revoked":true,"request_id":"..."}`, token store records JTI revoked, audit event `revoke_succeeded`
- [x] T045 [P] [US3] Write `TestRevoke_BadSignature_403` in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go): signature does not verify against the fingerprint's public key; assert 403 `bad_signature`, token store unchanged, audit event `revoke_bad_signature`
- [x] T046 [P] [US3] Write `TestRevoke_UnknownJTI_403_AsBadSignature` in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go): well-signed body but JTI was never issued; assert 403 `bad_signature` (FR-015 anti-enumeration — same status AND error code as a real signature failure), audit event `revoke_bad_signature`, asserting the response body is byte-for-byte identical to T045's body except for `request_id`
- [x] T047 [P] [US3] Write `TestRevoke_ReplayedNonce_403` in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go): submit a body, then resubmit the same body within the replay window; assert 403 `nonce_replay`, audit event `revoke_nonce_replay`
- [x] T048 [P] [US3] Write `TestRevoke_StaleTimestamp_403` in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go): timestamp older than `sign.IsFreshTimestamp` window; assert 403 `stale_timestamp`, audit event `revoke_stale_timestamp`
- [x] T049 [P] [US3] Write `TestRevoke_MalformedBody_400` in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go): malformed JSON; missing required field; unknown field (`DisallowUnknownFields`); each asserts 400 `bad_request`, audit event `revoke_bad_request`
- [x] T050 [P] [US3] Write `TestRevoke_IdempotentReRevocation_200_StaticBody` in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go): two well-signed bodies for the same JTI (fresh nonce per attempt); assert both return 200 with byte-identical bodies (after normalising `request_id`); audit chain has TWO events: `revoke_succeeded` then `revoke_idempotent_already_revoked`
- [x] T051 [P] [US3] Write `TestRevoke_ErrorBodyNoSentinel` in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go) using sentinel `SECRET_SHOULD_NEVER_APPEAR_13`: inject sentinel as the body's `nonce` field for one variant and as a vault value for another; drive every status row; assert sentinel absent from all response bodies, all log records, all audit Data maps
- [x] T052 [P] [US3] Write `TestRevoke_BodySizeCap_413` in [internal/server/revoke_handler_test.go](internal/server/revoke_handler_test.go): body > `MaxRequestBodyBytes`; assert 413 from chassis middleware (no audit event from the handler — chassis-level rejection occurs before handler runs)

### Implementation for User Story 3

- [x] T053 [US3] Define `revokeRequest` struct (jti, nonce, timestamp, request_id, machine_name, client_key_fingerprint, signature) in [internal/server/revoke_handler.go](internal/server/revoke_handler.go) with json tags per data-model §5; field validation per data-model §5 (UUIDv4 jti, base64url nonce 8..128, RFC3339Nano timestamp, 16-hex fingerprint)
- [x] T054 [US3] Implement `(s *Server) handleRevoke(w, r)` in [internal/server/revoke_handler.go](internal/server/revoke_handler.go): decode with `DisallowUnknownFields` + `MaxBytesReader` → field-level validation → resolve fingerprint via `s.deps.ClientKeyResolver` → `sign.CanonicalJSON` of body-without-signature → `sign.Verify(canonical, signature, pubKey)` → `s.deps.NonceCache.Add(nonce)` → `sign.IsFreshTimestamp(ts)` → `s.tokenStore.Revoke(jti)` → write success body; map every failure to its R-008 / data-model §8 row (depends on T043–T052)
- [x] T055 [US3] Implement idempotent re-revocation branch: `tokenStore.Revoke` reports whether the JTI was already revoked; emit `revoke_succeeded` for first revoke, `revoke_idempotent_already_revoked` for subsequent; HTTP body identical per FR-014 / spec clarification §5
- [x] T056 [US3] Implement `buildRevokeAuditDetail(outcome, requestID, clientIP, jti, fingerprint)` allow-list builder in [internal/server/revoke_handler.go](internal/server/revoke_handler.go): populates ONLY documented keys; never carries the body's `signature`, the body's `nonce`, the JTI bytes beyond the JTI string itself per FR-029
- [x] T057 [US3] Implement static success body writer `writeRevokeSuccess(w, r, requestID)` in [internal/server/revoke_handler.go](internal/server/revoke_handler.go) writing `{"revoked":true,"request_id":<id>}` with the locked response headers
- [x] T058 [US3] Mount `POST /revoke` in [internal/server/claim_handler.go](internal/server/claim_handler.go) `(s *Server) RegisterHandlers()` — append `s.Mount(http.MethodPost, "/revoke", s.handleRevoke)`

**Checkpoint**: `/revoke` is signed, idempotent, anti-enumeration; the unknown-JTI body matches the bad-signature body byte-for-byte (anti-enumeration assertion); audit chain distinguishes first vs idempotent.

---

## Phase 6: User Story 4 — `/hz` reachable without auth, redacted, never audits (Priority: P1)

**Goal**: `GET /h/<prefix>/hz` returns 200 with the locked field set per data-model §9 / R-010 regardless of auth header presence; the body never contains any secret name, JTI, fingerprint, bot token, audit-signing key, or random API path prefix; no audit event is emitted (FR-021a).

**Independent Test**: The `TestHealth_*` tests in [internal/server/health_handler_test.go](internal/server/health_handler_test.go).

### Tests for User Story 4 (write FIRST)

- [x] T059 [P] [US4] Write health harness helpers in [internal/server/health_handler_test.go](internal/server/health_handler_test.go): `newHealthHarness(t, opts...)`, `withVaultSecrets(name, value)`, `withDiscordHealth(connected bool)`, `withDiscordBotToken(tok)`, `withRegisteredClient(fp, pub)`, `withRunStartedAt(ts)`, `withClockInSync(bool)`, `withConfigValid(bool)`, `withVaultLoaded(bool)`
- [x] T060 [P] [US4] Write `TestHealth_NoAuth_OK` in [internal/server/health_handler_test.go](internal/server/health_handler_test.go): GET with no Authorization header; assert 200, JSON body parses, all eight documented fields present (`status`, `uptime`, `secrets_count`, `active_tokens`, `discord_connected`, `config_valid`, `vault_loaded`, `clock_in_sync`), `status=="ok"`, audit chain has zero events from the request (FR-021a)
- [x] T061 [P] [US4] Write `TestHealth_DiscordConnectedFlag` in [internal/server/health_handler_test.go](internal/server/health_handler_test.go): flip `Deps.DiscordHealth` between `true` and `false`; assert body's `discord_connected` matches; assert nil-`DiscordHealth` reports `discord_connected: false` (fail-closed per R-009)
- [x] T062 [P] [US4] Write `TestHealth_VaultLoadedFalseDuringStartup` in [internal/server/health_handler_test.go](internal/server/health_handler_test.go): harness with `vaultPtr.Load() == nil`; assert 200 with `vault_loaded: false`, NOT 503 (FR-021)
- [x] T063 [P] [US4] Write `TestHealth_NoSecretNameInBody`, `TestHealth_NoTokenIdentifierInBody`, `TestHealth_NoBotTokenInBody`, `TestHealth_NoFingerprintInBody`, `TestHealth_NoAuditChainHashInBody`, `TestHealth_NoPathPrefixInBody` in [internal/server/health_handler_test.go](internal/server/health_handler_test.go): each plants a unique sentinel into the relevant store/config field (vault names containing sentinel, JTIs in token store containing sentinel, `cfg.DiscordToken = sentinel`, `cfg.ClientRegistry` fingerprint = sentinel, `cfg.PathPrefix = sentinel`, audit signing key sentinel) and asserts `testutil.AssertSentinelAbsent` against the body
- [x] T064 [P] [US4] Write `TestHealth_NoAuditEvent` in [internal/server/health_handler_test.go](internal/server/health_handler_test.go): drive 100 sequential `GET /hz` requests; assert the captured audit-event slice has length zero (FR-021a)
- [x] T065 [P] [US4] Write `TestHealth_CountsMatchHarnessState` in [internal/server/health_handler_test.go](internal/server/health_handler_test.go): vault loaded with N secrets; token store with M active + K revoked; assert `secrets_count == N` and `active_tokens == M` (revoked entries excluded)

### Implementation for User Story 4

- [x] T066 [US4] Define `healthResponse` struct in [internal/server/health_handler.go](internal/server/health_handler.go) with the eight fields and json tags per data-model §9
- [x] T067 [US4] Implement `(s *Server) handleHealth(w, r)` in [internal/server/health_handler.go](internal/server/health_handler.go): assemble `healthResponse` from `time.Since(s.runStartedAt).Round(time.Second).String()`, `len(s.vaultStore.Names())` (or 0 if vaultPtr nil), `s.tokenStore.ActiveCount()`, `s.discordHealth()`, `true` (config_valid), `s.vaultPtr.Load() != nil`, `s.clockInSync.Load()`; encode JSON; write 200 with `Content-Type: application/json; charset=utf-8`, `X-Content-Type-Options: nosniff`, `Cache-Control: no-store`; MUST NOT call `Append` (depends on T059–T065)
- [x] T068 [US4] Mount `GET /hz` in [internal/server/claim_handler.go](internal/server/claim_handler.go) `(s *Server) RegisterHandlers()` — append `s.Mount(http.MethodGet, "/hz", s.handleHealth)`

**Checkpoint**: `/hz` is reachable without auth; never emits an audit event; never echoes any sentinel-bearing identifier.

---

## Phase 7: User Story 5 — Audit chain records every outcome with valid hash + signature (Priority: P1)

**Goal**: `audit.Writer.Append` synchronously appends a record to the on-disk JSONL file with `Seq` monotonic from 1, `PrevHash` = previous record's `Hash` (genesis for Seq=1), `Hash` = `sha256(prevHash || canonicalJSON(Event-without-Hash-or-Signature))`, and `Signature` = ECDSA over `Hash`. `audit.Verify` re-validates the chain end-to-end and surfaces `ErrAuditChainBroken` (wrapped in `*ChainError`) at the first inconsistent event.

**Independent Test**: The `TestAuditChain_*` and `TestAuditWriter_*` tests in [internal/audit/](internal/audit/).

### Tests for User Story 5 (write FIRST)

- [x] T069 [P] [US5] Write `TestAuditChain_HashLinkContiguous` in [internal/audit/chain_test.go](internal/audit/chain_test.go): construct N events via the writer; read back; assert `Seq` is `1..N` contiguous, every `PrevHash[i] == Hash[i-1]`, and `PrevHash[0] == hex(genesisPrevHash)`
- [x] T070 [P] [US5] Write `TestAuditChain_SignatureValid` in [internal/audit/chain_test.go](internal/audit/chain_test.go): for every event, decode `Hash` from hex, decode `Signature` from base64, assert `ecdsa.VerifyASN1(verifyKey, hashBytes, sigBytes)` returns true
- [x] T071 [P] [US5] Write `TestAuditChain_BreakDetectedOnTamper` in [internal/audit/chain_test.go](internal/audit/chain_test.go) per [quickstart.md](specs/013-server-handlers-and-audit/quickstart.md): write 3 events; mutate event 2's data on disk (replace `"approved"` with `"denied"  ` to preserve length); call `audit.Verify`; assert `errors.Is(err, ErrAuditChainBroken)`, `errors.As(err, &ce)` recovers a `*ChainError` with `ce.Seq == 2` and `ce.Reason == "hash_mismatch"`
- [x] T072 [P] [US5] Write `TestAuditChain_BreakDetectedOnDelete` in [internal/audit/chain_test.go](internal/audit/chain_test.go): write 3 events; delete the second line on disk; verify; assert `ErrAuditChainBroken` with `Reason == "seq_gap"` or `"prev_hash_mismatch"` at Seq=3
- [x] T073 [P] [US5] Write `TestAuditChain_BreakDetectedOnForgedSignature` in [internal/audit/chain_test.go](internal/audit/chain_test.go): write 3 events; replace event 2's `Signature` with a base64-valid-but-wrong value; verify; assert `ErrAuditChainBroken` with `Reason == "signature_invalid"` at Seq=2
- [x] T074 [P] [US5] Write `TestAuditChain_GenesisPrevHashIsDomainSeparated` in [internal/audit/chain_test.go](internal/audit/chain_test.go): assert `genesisPrevHash == sha256.Sum256([]byte("hush.audit.chain.v1.genesis"))` (R-002 reproducibility check)
- [x] T075 [P] [US5] Write `TestAuditChain_HashCoversCanonicalEventWithoutHashOrSignature` in [internal/audit/chain_test.go](internal/audit/chain_test.go): construct an Event manually; recompute hash via `sha256(prevHash || sign.CanonicalJSON({Seq, Time, Action, Data, PrevHash}))`; assert equality with the writer's recorded `Hash` (R-001)
- [x] T076 [P] [US5] Write `TestAudit_RecordNoSecretValue` in [internal/audit/writer_test.go](internal/audit/writer_test.go) using `testutil.SentinelSecret(13)`: append events whose `Data` map has been deliberately constructed by the canonical-builder helpers (no sentinel injection at call site) AND a separate path where a producer attempts to inject the sentinel into Data; for the deliberate path assert sentinel absent from on-disk bytes; for the injection path assert the writer either persists it (because the audit package itself does not know what is "secret") OR an explicit handler-side sentinel-leak test verifies handlers never construct such Data — document the layering: **the audit package's contract is to faithfully record what it is given; preventing secret leak into Data is the producer's obligation per FR-028 + R-012**, and the handler-side tests T039 / T051 are the binding assertion (this test verifies the sentinel-absence at the readback layer, closing the loop)
- [x] T077 [P] [US5] Write `TestAuditChain_ResumesFromTail` in [internal/audit/writer_test.go](internal/audit/writer_test.go): write events with one writer, close it; open a new writer at the same path; append more events; assert `Seq` continues from the prior tail+1, `PrevHash` of the first new event equals the `Hash` of the prior tail, full-chain `Verify` passes
- [x] T078 [P] [US5] Write `TestAuditWriter_NewWriter_ValidationFailures` in [internal/audit/writer_test.go](internal/audit/writer_test.go): empty path → `ErrInvalidPath`; nil signKey → `ErrInvalidKey`; signKey not on secp256k1 curve → `ErrInvalidKey`; nil logger → typed validation error; corrupt last line → `ErrChainTailUnreadable`

### Implementation for User Story 5

- [x] T079 [US5] Implement `Verify(path string, verifyKey *ecdsa.PublicKey) error` in [internal/audit/chain.go](internal/audit/chain.go): open file, `bufio.Scanner` with 1 MiB buffer per R-005, decode each line into `Event`, recompute hash, verify signature, check seq contiguous + prevHash linkage; first inconsistency returns `*ChainError` wrapping `ErrAuditChainBroken` (depends on T015–T021, T069–T078)
- [x] T080 [US5] Implement `func computeHash(prevHash []byte, ev Event) ([]byte, error)` in [internal/audit/chain.go](internal/audit/chain.go) computing `sha256(prevHash || sign.CanonicalJSON(EventWithoutHashSig))` per R-001
- [x] T081 [US5] Implement `func signEventHash(key *ecdsa.PrivateKey, hash []byte) (string, error)` in [internal/audit/chain.go](internal/audit/chain.go) producing base64-encoded ASN.1 ECDSA signature (matches verify path); reject nil key
- [x] T082 [US5] Implement `NewWriter(ctx, path, signKey, mirror, logger)` in [internal/audit/writer.go](internal/audit/writer.go): validate inputs (non-empty path, non-nil signKey on secp256k1 curve, non-nil logger); open file `O_WRONLY|O_APPEND|O_CREATE`, mode 0600; if non-empty, scan to last line and recover `Seq` and `prevHash` (return `ErrChainTailUnreadable` if last line cannot be parsed); store state on the writer struct
- [x] T083 [US5] Implement the writer goroutine inside `(*writerImpl) Run(ctx)` in [internal/audit/writer.go](internal/audit/writer.go): receives `pending` from unbuffered `accept` chan, computes `Seq+1`, `Hash`, `Signature`, marshals `sign.CanonicalJSON(Event)` + `\n`, writes via `bufio.Writer`, calls `Flush()` per event, sends `eventAck{seq, err}` back; on `ctx.Done()` drains in-flight `pending`s, writes a final `actionServerStop` if requested (chassis emits it via `Append` immediately before cancelling), then `Sync()` + `Close()`
- [x] T084 [US5] Implement `(*writerImpl) Append(ctx, action, data)` in [internal/audit/writer.go](internal/audit/writer.go): validate `action != ""`, validate `ctx`; construct `pending`; send to `accept` chan via `select { case w.accept <- p: case <-ctx.Done(): return ctx.Err(); case <-w.shutdown: return ErrShutdown }`; await `pending.ack`; return ack error
- [x] T085 [US5] Wire mirror dispatch into the writer goroutine after a successful disk persist: non-blocking `select { case mirror.ch <- ev: default: WARN-log (FR-035 / R-006) }`; the mirror goroutine itself is implemented in Phase 9

**Checkpoint**: The chain is hash-linked, signed, verifiable end-to-end; tampering surfaces `ErrAuditChainBroken` at the first inconsistent event with the correct `Seq`.

---

## Phase 8: User Story 6 — Audit writer blocks producers under buffer pressure, never drops (Priority: P1)

**Goal**: When the rendezvous channel is contested, producers' `Append` calls block until the writer goroutine accepts them. Concurrent `Append`s yield exactly N records with monotonic `Seq` and valid linkage. `Append` returning success guarantees the event is on-chain (FR-033).

**Independent Test**: `TestAuditWriter_BlocksOnBackpressure` and `TestAuditWriter_ConcurrentAppendMonotonicSeq` in [internal/audit/writer_test.go](internal/audit/writer_test.go).

### Tests for User Story 6 (write FIRST)

- [x] T086 [P] [US6] Add `internal/audit/export_test.go` exposing a writer-private `pause` `chan struct{}` (or test-only setter) so the test goroutine can hold the writer goroutine before its `Flush` completes per [quickstart.md](specs/013-server-handlers-and-audit/quickstart.md) §"Verify the backpressure invariant"
- [x] T087 [P] [US6] Write `TestAuditWriter_BlocksOnBackpressure` in [internal/audit/writer_test.go](internal/audit/writer_test.go): pause the writer goroutine via T086; spawn N concurrent `Append` calls in goroutines, each posting a `chan struct{}` ack; wait for the rendezvous channel to fill; assert every producer goroutine is still blocked (no ack delivered) within a tight deadline; release pause; assert all N producers complete; assert exactly N records on disk with Seqs 1..N
- [x] T088 [P] [US6] Write `TestAuditWriter_ConcurrentAppendMonotonicSeq` in [internal/audit/writer_test.go](internal/audit/writer_test.go): spawn `N=8` goroutines × `M=64` events each; wait for all to complete; assert exactly `N*M` records on disk, Seqs 1..N*M contiguous, every linkage holds, full `Verify` passes; the test MUST pass under `go test -race -count=10`
- [x] T089 [P] [US6] Write `TestAuditWriter_AppendSuccess_MeansOnChain` in [internal/audit/writer_test.go](internal/audit/writer_test.go) (FR-033): in a producer goroutine, await `Append` return; immediately read the on-disk file in the same goroutine; assert the just-appended event is present (not buffered for later flush)
- [x] T090 [P] [US6] Write `TestAuditWriter_NeverDropsUnderLoad` in [internal/audit/writer_test.go](internal/audit/writer_test.go) (SC-009): hammer the writer with concurrent producers; track every successful `Append` return (count = X); assert on-disk record count == X (no silent loss)

### Implementation for User Story 6

- [x] T091 [US6] Confirm the writer goroutine + unbuffered `accept` channel design from T083/T084 already satisfies the backpressure invariant (no buffered channel between producer and writer per R-004); add an `audit.t_pauseFlush` test hook in `export_test.go` to enable T087 without leaking pause logic into production code
- [x] T092 [US6] Add a writer-private invariant assertion (compile-time and run-time): the only buffered channel in the writer struct is the OPTIONAL `mirror.ch`; the producer-to-writer path is unbuffered. Document via a comment near the `accept` field declaration referencing R-004 and FR-031.

**Checkpoint**: Concurrent producers serialise through the rendezvous; `-race` is clean; SC-009 + SC-010 (race-clean exact-N) pass.

---

## Phase 9: User Story 7 — Discord audit mirror is best-effort, never blocks chain (Priority: P2)

**Goal**: When `DiscordMirror` is non-nil and `channelID != ""`, every accepted event is dispatched best-effort to the Discord channel via a separate goroutine with a 64-deep buffered channel. Mirror failures log WARN with seq + action only and never affect the on-disk chain. Empty `channelID` disables mirroring entirely (FR-036).

**Independent Test**: The `TestDiscordMirror_*` tests in [internal/audit/discord_mirror_test.go](internal/audit/discord_mirror_test.go).

### Tests for User Story 7 (write FIRST)

- [x] T093 [P] [US7] Write `mirrorSessionStub` test fake in [internal/audit/discord_mirror_test.go](internal/audit/discord_mirror_test.go) implementing `MirrorSession.ChannelMessageSendComplex` with configurable behaviour: succeed, fail-immediately, fail-slow (returns after a configurable delay), return-rate-limit-error, return-transport-error
- [x] T094 [P] [US7] Write `TestDiscordMirror_FailureLogsWarnNoBlock` in [internal/audit/discord_mirror_test.go](internal/audit/discord_mirror_test.go): wire writer with mirror configured to fail-immediately; append 5 events; assert all 5 reach the on-disk chain, each generates one WARN log carrying `seq` + `action` + error class, the WARN log does NOT contain the bot token / event signature / data values, the producer's `Append` latency does not include the mirror's failure latency (assert via wall-clock measurement against a slow-failure stub)
- [x] T095 [P] [US7] Write `TestDiscordMirror_EmptyChannelIDDisablesPublish` in [internal/audit/discord_mirror_test.go](internal/audit/discord_mirror_test.go) (FR-036): construct mirror with `channelID == ""`; assert no calls to the stub's `ChannelMessageSendComplex` for any number of appended events; on-disk chain unaffected
- [x] T096 [P] [US7] Write `TestDiscordMirror_NoBotTokenInWarn` in [internal/audit/discord_mirror_test.go](internal/audit/discord_mirror_test.go) using sentinel `SECRET_SHOULD_NEVER_APPEAR_13` planted into the stub's error string; trigger mirror failures; assert sentinel absent from the captured slog buffer
- [x] T097 [P] [US7] Write `TestDiscordMirror_ChainUnaffectedByMirrorFailure` in [internal/audit/discord_mirror_test.go](internal/audit/discord_mirror_test.go) (FR-037): set the stub to fail-immediately on event 2; append 3 events; run `audit.Verify` on the on-disk file; assert verify succeeds (no compensating event was inserted)
- [x] T098 [P] [US7] Write `TestDiscordMirror_BufferFullDropsMirrorCopyNotChain` in [internal/audit/discord_mirror_test.go](internal/audit/discord_mirror_test.go) per R-006: pause the mirror goroutine via an export_test hook so its 64-deep buffer fills; append 100 events; assert on-disk chain has 100 records, the WARN log records the dropped-mirror events, no mirror call was made for the dropped events; release; assert mirror goroutine drains gracefully
- [x] T099 [P] [US7] Write `TestDiscordMirror_NoRetryOnFailure` in [internal/audit/discord_mirror_test.go](internal/audit/discord_mirror_test.go) per R-006: stub configured to fail-once-then-succeed; append 1 event; assert exactly 1 call to `ChannelMessageSendComplex` (no retry) and 1 WARN log

### Implementation for User Story 7

- [x] T100 [US7] Implement the mirror goroutine in [internal/audit/discord_mirror.go](internal/audit/discord_mirror.go): receives from the 64-deep buffered `m.ch chan Event`; for each event renders a human-readable message (seq, time, action — never data fields, never signature, never bot token); calls `m.session.ChannelMessageSendComplex(m.channelID, msg)`; on error logs WARN; never retries (R-006); on `ctx.Done()` drains buffer up to `mirrorShutdownTimeout=5s` then exits (depends on T093–T099)
- [x] T101 [US7] Implement `actionAuditMirrorFailed` chain emission per contracts/audit.md §"Mirror discipline" item 6: when the mirror goroutine encounters a failure, after the WARN log it calls `Append(ctx, actionAuditMirrorFailed, {seq, action, error_class})` on the writer; document the loop concern (a mirror failure on the `actionAuditMirrorFailed` event itself is allowed to drop silently — the on-disk chain still records the original failure event); add `TestDiscordMirror_FailedEventEmitsAuditEvent` to [internal/audit/discord_mirror_test.go](internal/audit/discord_mirror_test.go)
- [x] T102 [US7] Wire the mirror's lifecycle into `(*writerImpl) Run`: spawn mirror goroutine alongside the writer goroutine when `mirror != nil`; coordinate shutdown so the mirror goroutine exits before `Run` returns

**Checkpoint**: Mirror is fully best-effort; on-disk chain integrity is independent of mirror state; SC-011 (mirror-failure → no on-disk impact, no blocking) holds.

---

## Phase 10: User Story 8 — Audit writer drains cleanly on shutdown, returns ErrShutdown for new appends (Priority: P2)

**Goal**: On `Run`'s ctx cancel, the writer drains every `pending` already in flight, writes its tail, calls `Sync()` + `Close()`, and returns. New `Append` calls after shutdown return `ErrShutdown`. Every event whose `Append` returned success appears on disk.

**Independent Test**: The shutdown-drain tests in [internal/audit/writer_test.go](internal/audit/writer_test.go).

### Tests for User Story 8 (write FIRST)

- [x] T103 [P] [US8] Write `TestAuditWriter_DrainOnShutdown` in [internal/audit/writer_test.go](internal/audit/writer_test.go) (FR-039 / SC-012): pause the writer via the test hook; queue K successful `Append`-not-yet-acked rendezvous (each in a goroutine); cancel `Run`'s ctx; release pause; assert every queued `Append` returns successfully or returns `ErrShutdown`, and for every nil-returning `Append` the event appears on disk
- [x] T104 [P] [US8] Write `TestAuditWriter_AppendAfterShutdownReturnsError` in [internal/audit/writer_test.go](internal/audit/writer_test.go): cancel `Run`'s ctx; await `Run` return; call `Append`; assert `errors.Is(err, ErrShutdown)`
- [x] T105 [P] [US8] Write `TestAuditWriter_RunReturnsAfterDrain` in [internal/audit/writer_test.go](internal/audit/writer_test.go): start `Run` in a goroutine; append events; cancel ctx; assert `Run` returns within a bounded deadline (e.g., 2 seconds) and that the on-disk file is `Sync`'d (file size > 0, all events readable)
- [x] T106 [P] [US8] Write `TestAuditWriter_DoubleRunReturnsError` in [internal/audit/writer_test.go](internal/audit/writer_test.go): call `Run(ctx)` twice; assert the second call returns a typed already-running error per contracts/audit.md §"Run(ctx)" item 1

### Implementation for User Story 8

- [x] T107 [US8] Implement the shutdown drain in `(*writerImpl) Run`: on `ctx.Done()` close `accept`, drain remaining `pending`s, persist each, then `Sync()` + `Close()` the file; cooperate with the mirror goroutine's bounded-drain shutdown (T102) (depends on T103–T106)
- [x] T108 [US8] Implement the `Append` shutdown branch: select on `accept`, `ctx.Done()`, AND a `w.shutdown` chan that is closed when `Run`'s ctx fires; the shutdown branch returns `ErrShutdown`
- [x] T109 [US8] Implement the double-`Run` guard: `atomic.Bool` set on first call; second call returns a typed error

**Checkpoint**: Shutdown drain preserves SC-012; new appends return `ErrShutdown`; double-`Run` is rejected.

---

## Phase 11: Chassis adapter wiring + integration test

**Purpose**: Wire the chunk together end-to-end. The chassis-side `AuditWriter` interface from SDD-10 is preserved; the adapter translates `AuditEvent` to `(action, data map[string]any)`. A single integration test exercises the production wiring.

### Adapter (per R-003)

- [x] T110 [P] Write `TestAuditAdapter_TranslatesAuditEventToActionAndDetail` in [internal/server/audit_adapter_test.go](internal/server/audit_adapter_test.go): construct `chassisAuditAdapter{w: fakeAuditWriter}`; call `Write(ctx, AuditEvent{Type: ..., RequestID: ..., ClientIP: ..., Detail: {...}})`; assert the underlying `Append` saw `action == string(ev.Type)` and `data` contained every Detail key plus `request_id` and `client_ip`
- [x] T111 [P] Write `TestAuditAdapter_PreservesRequestIDAndClientIP` in [internal/server/audit_adapter_test.go](internal/server/audit_adapter_test.go): empty `RequestID` is omitted from data; non-empty is forwarded; invalid `ClientIP` is omitted; valid is forwarded as `String()` form
- [x] T112 [P] Write `TestAuditAdapter_PassesThroughError` in [internal/server/audit_adapter_test.go](internal/server/audit_adapter_test.go): underlying `Append` returns sentinel; assert adapter's `Write` returns the same sentinel verbatim
- [x] T113 Implement `chassisAuditAdapter` in [internal/server/audit_adapter.go](internal/server/audit_adapter.go) per R-003: ~12-line struct + `Write(ctx, AuditEvent) error` method that folds Detail + RequestID + ClientIP into `data` and calls `a.w.Append(ctx, string(ev.Type), data)` (depends on T110–T112)

### Integration leg (per R-014)

- [x] T114 Write `TestSecretRevokeHealth_Integration_FullFlow` in [internal/server/integration_test.go](internal/server/integration_test.go) under `//go:build integration`: construct chassis with `chassisAuditAdapter` over a real `audit.NewWriter` writing to `t.TempDir()`, real vault store with one secret, fake approver always-approve, `DiscordHealth = func() bool { return true }`; drive POST /claim → GET /s/<name> → GET /hz → POST /revoke → GET /s/<name> (expect 401 revoked); cancel chassis ctx; call `audit.Verify` on the on-disk file; assert chain is contiguous, signatures verify, every outcome is recorded

---

## Phase 12: Polish & Cross-Cutting Concerns (final gates)

**Purpose**: Land the post-implementation gates required by Constitution VIII. These tasks MUST be the last to run; their success is the chunk-completion contract.

- [x] T115 [P] Confirm all sentinel-leak tests use `testutil.SentinelSecret(13)` (= `SECRET_SHOULD_NEVER_APPEAR_13`) per R-012: grep [internal/server/*_test.go](internal/server/) and [internal/audit/*_test.go](internal/audit/) for the sentinel string; assert the only files referencing it are the two named in the prompt (`TestSecret_ErrorBodyNoSentinel` in secret_handler_test.go, `TestAudit_RecordNoSecretValue` in audit/writer_test.go) plus the chunk-13 fan-out tests (T039, T051, T063, T076, T096)
- [x] T116 Run `magex format:fix` from the repo root; assert clean — no diff after the run; fix any drift the formatter introduced
- [x] T117 Run `magex lint` from the repo root; assert exit zero; address every finding (no waivers; per Constitution VIII)
- [x] T118 Run `magex test:race` from the repo root; assert exit zero; the run MUST include `TestAuditWriter_ConcurrentAppendMonotonicSeq` and every other test in the chunk (Constitution VIII race-clean requirement)
- [x] T119 [P] Run `go test -cover ./internal/server/` and assert the new files (`secret_handler.go`, `revoke_handler.go`, `health_handler.go`, `audit_adapter.go`) are at ≥ 95 % line coverage individually — use `go test -coverprofile=cover.out ./internal/server/ && go tool cover -func=cover.out | grep -E '(secret_handler|revoke_handler|health_handler|audit_adapter)'` and compare against the threshold (per SC-013, R-013)
- [x] T120 [P] Run `go test -cover ./internal/audit/` and assert package coverage = 100.0 %; the per-file gate from T004's `coverage_test.go` should also fire and confirm `chain.go`, `writer.go`, `discord_mirror.go` are individually 100 %
- [x] T121 [P] Run `go test -tags=integration -race ./internal/server/...` and assert `TestSecretRevokeHealth_Integration_FullFlow` passes; the on-disk audit file under `t.TempDir()` is verified by the test itself
- [x] T122 [P] Append the SDD-13 locked-API entry to [docs/PACKAGE-MAP.md](docs/PACKAGE-MAP.md): under `internal/server` note "GET /s/<name>, POST /revoke, GET /hz handlers — see docs/API.md (locked at SDD-13)"; add NEW entry for `internal/audit` listing `Event`, `Writer`, `NewWriter`, `DiscordMirror`, `NewDiscordMirror`, `Verify`, `ChainError`, `ErrAuditChainBroken`, `ErrShutdown`, `ErrChainTailUnreadable`, `ErrInvalidPath`, `ErrInvalidKey`
- [x] T123 [P] Update [docs/AC-MATRIX.md](docs/AC-MATRIX.md) AC-1, AC-2, AC-4, AC-7 rows with the new test file paths from this chunk
- [x] T124 [P] Mark SDD-13 status `done` in [docs/SDD-PLAYBOOK.md](docs/SDD-PLAYBOOK.md)

**Checkpoint**: Gates pass clean; coverage thresholds met; race-clean; sentinel-leak holds; AC-1 / AC-2 / AC-4 / AC-7 references updated; SDD-13 marked done.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)** — no dependencies; can start immediately on a fresh branch
- **Foundational (Phase 2)** — depends on Phase 1; BLOCKS all user stories
- **User Stories (Phases 3–10)** — all depend on Phase 2; within each story Tests precede Implementation per Constitution VIII
- **Adapter & Integration (Phase 11)** — depends on Phases 3–10 (US1–US8 deliver the surfaces the adapter and integration test exercise)
- **Polish (Phase 12)** — depends on Phases 1–11 complete

### User-Story Dependencies (within Phase 3+)

- **US1 (`/s` happy path)** — depends only on Foundational
- **US2 (`/s` rejection paths)** — depends on US1's handler stub being mounted (T026 + T028); the rejection branches extend the same `handleSecret` function
- **US3 (`/revoke`)** — depends only on Foundational; independent of US1/US2
- **US4 (`/hz`)** — depends only on Foundational; independent of US1/US2/US3
- **US5 (audit chain integrity)** — depends only on Foundational; independent of all handler stories (the chain is exercised by handler tests via fake `audit.Writer`, then exercised end-to-end by US5's own tests)
- **US6 (audit backpressure)** — depends on US5 (extends the writer goroutine)
- **US7 (Discord mirror)** — depends on US5 (uses the writer's mirror-dispatch hook from T085)
- **US8 (shutdown drain)** — depends on US5 + US6 (extends `Run`'s lifecycle and `Append`'s shutdown branch)

### Within Each User Story

- Test files MUST be written and asserted-failing BEFORE the implementation files (Constitution VIII)
- Models / types / sentinels precede the handler / writer goroutine logic
- The audit-emission branch in every handler closes the loop and is added LAST inside the story (after the success/failure status logic is correct)

### Parallel Opportunities

- All Phase 1 setup tasks marked [P] are file-independent and can run concurrently
- All Phase 2 foundational tasks marked [P] are file-independent (different packages and different test files)
- Once Phase 2 completes, **US1, US3, US4, and US5 can be implemented fully in parallel** (different developers, different files, no cross-story merges)
- US2 must follow US1 in the same `secret_handler.go` source file
- US6/US7/US8 all extend the audit writer; US7 (mirror) and US8 (shutdown) can land in parallel after US5+US6 are merged
- Within any phase, every test file [P] can be written in parallel; every implementation file [P] can be implemented in parallel

---

## Parallel Example: Phase 2 Foundational

```bash
# All independent additive accessors / sentinels — launch in parallel:
Task: "Add TestStore_Names_ReturnsSortedCopy_NoValues to internal/vault/store_test.go"            # T005
Task: "Add TestStore_ActiveCount_ExcludesRevokedAndExpired to internal/token/store_test.go"        # T007
Task: "Add TestBotApprover_Connected_TracksAvailability to internal/discord/bot_test.go"           # T009
Task: "Add TestServer_DiscordHealth_NilFieldReportsDisconnected to internal/server/server_test.go" # T012
Task: "Add TestErrSecretMissing_DistinctSentinel to internal/server/errors_test.go"                # T014

# Followed by the matching implementations (still parallel — different files):
Task: "Implement (s *Store) Names() in internal/vault/store.go"                                    # T006
Task: "Implement (s *Store) ActiveCount() in internal/token/store.go"                              # T008
Task: "Implement (a *BotApprover) Connected() in internal/discord/bot.go"                          # T010
Task: "Define audit.Event in internal/audit/chain.go"                                              # T015
Task: "Define audit sentinels in internal/audit/chain.go"                                          # T016
Task: "Define audit.MirrorSession + DiscordMirror struct + NewDiscordMirror in internal/audit/discord_mirror.go" # T019
```

---

## Parallel Example: User Story 1 (US1 happy path)

```bash
# Tests first (different test functions in the same file — write the file in one pass):
Task: "Write TestSecret_HappyPath_ECIESPayload in internal/server/secret_handler_test.go"          # T022
Task: "Write TestSecret_SupervisorIgnoresMaxUses in internal/server/secret_handler_test.go"        # T023
Task: "Write TestSecret_AuditEventEmittedForSuccess in internal/server/secret_handler_test.go"     # T024
Task: "Write secret-handler test harness helpers in internal/server/secret_handler_test.go"        # T025

# Then the implementation (sequential — same file):
Task: "Implement (s *Server) handleSecret in internal/server/secret_handler.go"                    # T026
Task: "Implement buildSecretAuditDetail allow-list builder in internal/server/secret_handler.go"   # T027
Task: "Mount GET /s/{name} in internal/server/claim_handler.go"                                    # T028
```

---

## Implementation Strategy

### MVP (P1 stories only)

1. Complete Phase 1 (Setup) — package skeletons.
2. Complete Phase 2 (Foundational) — locked types, accessors, sentinels.
3. Complete Phases 3–7 (US1–US5) — `/s`, `/revoke`, `/hz`, audit chain.
4. **STOP & VALIDATE**: every P1 user story is independently testable; sentinel-leak tests pass; chain `Verify` passes.
5. The MVP at this point: working server with retrieval + revoke + health + tamper-evident audit chain.

### Full Chunk (P1 + P2)

6. Phase 8 (US6 backpressure) — extends US5's writer.
7. Phase 9 (US7 Discord mirror) — best-effort layer.
8. Phase 10 (US8 shutdown drain) — Run lifecycle.
9. Phase 11 (chassis adapter + integration leg) — wire it together; the integration leg is the load-bearing assertion for AC-1 / AC-2 / AC-4 / AC-7.
10. Phase 12 (polish gates) — `magex format:fix`, `magex lint`, `magex test:race`, coverage gates, doc updates.

### TDD discipline (Constitution VIII)

- For every behaviour contract there is a test task BEFORE its implementation task; the implementation task explicitly lists the test tasks it depends on (`(depends on Txxx–Tyyy)`).
- Every implementation task that introduces a new error path adds a test for that path in the same phase.
- Race-clean is enforced via the final `magex test:race` gate (T118); concurrent tests (T088, T103, T104) are designed to be deterministic under `-count=10`.

---

## Notes

- [P] tasks operate on different files or independent test functions; verify before parallelising that no two [P] tasks edit the same file.
- Every user-story implementation task lists the test tasks it depends on so a Phase-12 reviewer can confirm tests-fail-first holds.
- The two locked sentinel-leak tests called out by name in the prompt (`TestSecret_ErrorBodyNoSentinel` and `TestAudit_RecordNoSecretValue`) live at T039 and T076 respectively; both use `SECRET_SHOULD_NEVER_APPEAR_13` per R-012.
- The final-phase commands `magex format:fix && magex lint && magex test:race` are split across T116, T117, T118 so each can fail independently with a clear blame line.
- Coverage gates (T119, T120) are split per package to surface the right error message: 95 % failure points at the handler files, 100 % failure points at the audit package.
- The integration test (T114) is the only `//go:build integration` task; all other tests are unit + race.
