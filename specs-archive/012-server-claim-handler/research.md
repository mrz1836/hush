# Phase 0 Research — SDD-12 `/claim` handler

The spec's `Clarifications` section already resolves the three `[NEEDS CLARIFICATION]` candidates that the spec phase identified (request_id absent → `400`; rate_limited → `429`; approval-timeout source → per-server `claim_approval_timeout`). This file records the small set of orientation decisions the handler still needs before code is written.

---

## R-001 — Approver outcome → HTTP status mapping is error-driven, not Decision-field-driven

**Decision**: The chassis `Approver.RequestApproval(ctx, ApprovalRequest) (Decision, error)` returns:
- Approve → `(Decision{Approved: true, GrantedTTL: cappedTTL, ApprovedAt: now}, nil)`
- every other outcome → `(Decision{}, <sentinel>)` with the sentinel matched by `errors.Is`.

The handler reads the error first; on `nil` the handler additionally asserts `dec.Approved` and `dec.GrantedTTL > 0` as a defence-in-depth check (a non-conforming Approver that returns `(Decision{}, nil)` is treated as the unknown-outcome class — fail-closed 503).

**Rationale**: An error-driven contract makes "forgot to check error" impossible — the Go compiler does not let you ignore `nil`. A Decision-field-only contract (`Decision{Approved: false, DeniedAt: now}`, `Decision{Approved: false, TimedOut: true}`, …) is an enum smuggled through booleans and is easy to misread (`if dec.Approved` is the only safe write but a single missed branch silently leaks tokens — exactly the regression Constitution II forbids). Discord's `BotApprover` already returns errors for non-Approve outcomes, so this matches the production surface.

**Alternatives considered**:
- *Boolean-only Decision*: rejected — see above.
- *Direct coupling to `internal/discord.ErrXxx`*: rejected — couples server to discord and breaks the clean chassis abstraction. The plan adds five chassis-level sentinels; cmd/hush installs a thin adapter that translates `discord.ErrDiscordUnavailable → server.ErrApproverUnavailable`, etc.
- *Untyped error string match*: rejected — Constitution IX bans error-string comparison.

---

## R-002 — Client-key fingerprint resolution lives in `Deps`, not in the handler

**Decision**: Add an **optional** field `Deps.ClientKeyResolver func(fingerprint string) (*ecdsa.PublicKey, error)`. When nil, `New` installs a default that loads `cfg.Server.ClientRegistry` (default `~/.hush/clients.json`) once at construction time and serves an in-memory map. Lookup misses return `ErrClientUnknown`; the handler maps that to the same `bad_signature` (403) response code as a verify failure (FR per spec edge case "Client supplies an unknown registered-client-key fingerprint" — must not enumerate registered clients via error variation).

**Rationale**: Resolving a fingerprint is pure read-only work that should not happen on every request (file I/O on the hot path is unacceptable per SC-006). Loading at construction time matches how `Deps.LoadVaultFn` already handles its file. Making it injectable (`func(...)`) keeps tests free of disk fixtures.

**Alternatives considered**:
- *Inline registry parse in handler*: rejected — file I/O on every request.
- *New `internal/registry` package*: rejected — over-engineered for a single read-only map; the loader is a 30-line helper inside `internal/server`.

---

## R-003 — `claim_approval_timeout` lives under `[crypto]` in the server config TOML

**Decision**: Add `Crypto.ClaimApprovalTimeout time.Duration` (TOML key `claim_approval_timeout`) with default 60 s. Validation: must be ≥ 1 s and ≤ 10 min (DoS-via-config ceiling, matching the existing argon ceiling pattern). The handler derives `ctx, cancel := context.WithTimeout(r.Context(), cfg.Crypto.ClaimApprovalTimeout)` immediately before invoking `RequestApproval`; cancel is deferred.

**Rationale**: Other request-timing tunables (`NonceTTL`, `ClockSkew`, `JWTDefaultTTL`) already live under `[crypto]`; placing the new field there preserves config-section coherence. A separate `[approval]` section would be premature for one field.

**Alternatives considered**:
- *Hard-coded 60 s constant*: rejected — fails the spec's "ops tune without code change" requirement.
- *`[server]` section*: rejected — the value is request-time semantics, not bind-time. Co-locating it with crypto/timing keys makes the SIGHUP reload story consistent.

---

## R-004 — Audit data set is the chassis `AuditEvent` shape; handler-specific event types live in this package

**Decision**: Reuse the locked `AuditEvent { Type AuditEventType, At time.Time, RequestID string, ClientIP netip.Addr, Detail map[string]string }`. Add two new `AuditEventType` constants in `claim_handler.go`:
- `AuditClaimOutcome AuditEventType = "claim_outcome"` — emitted exactly once per claim
- (Optional, post-MVP) `AuditClaimRejected` if outcome-class granularity helps queryability — for SDD-12 a single type with `Detail["outcome"]` is enough.

`Detail` carries (only) `outcome` (one of the ten labels), `session_type` ("interactive"|"supervisor"), `scope_count` (string-encoded int — never the names themselves to keep this off-the-record vs. audit-on-the-record clean), and `granted_ttl` (when issued). Names of secrets ARE in scope per FR-022 — re-evaluating: the spec mandates "the requested scope (the list of secret names)" in audit events, so `scope` (joined comma list, sorted, lowercase) IS in `Detail`. Operational logs still omit it (log-vs-audit redaction asymmetry per Constitution X).

**Rationale**: The chassis `AuditWriter` is a single-method interface; no need to widen it. Adding new `AuditEventType` constants is a documented extension point per `internal/server/approver.go` ("SDD-12, SDD-13, and SDD-11 emit additional types not listed here").

**Alternatives considered**:
- *One AuditEventType per outcome*: rejected — explodes the constant surface; queryability via `Detail["outcome"]` is sufficient.

---

## R-005 — Pipeline ordering is shape → signature → nonce → timestamp → IP allowlist (locked by FR-001)

**Decision**: The handler short-circuits on the first failing check (FR-002) and never invokes the approver on a failed claim (FR-003). The order is:

1. **Shape**: `json.NewDecoder(http.MaxBytesReader(...)).DisallowUnknownFields()` decode of the request body into a private `claimRequest` struct; reject malformed JSON, unknown fields, missing required fields with `400 bad_request`. Also reject `requested_ttl <= 0` and absent/empty/malformed `request_id` here (FR-009, FR-017).
2. **Signature**: build canonical-JSON via `sign.CanonicalJSON` over the same fields the client signed (per `docs/API.md`: scope, reason, ttl, session_type, ephemeral_pubkey, nonce, timestamp, request_id, machine_name); resolve `client_key_fingerprint` via `ClientKeyResolver`; call `sign.Verify(ctx, pub, payload, sig)`. Map `sign.ErrSignatureInvalid` and `ErrClientUnknown` (unknown fingerprint) to `403 bad_signature`.
3. **Nonce**: `sign.NonceCache.Add(ctx, nonce, cfg.Crypto.NonceTTL)` — `firstSeen=false` or `ErrNonceReplay` → `403 nonce_replay`.
4. **Timestamp**: `sign.IsFreshTimestamp(parsed, cfg.Crypto.ClockSkew)` returning false → `403 stale_timestamp`.
5. **IP allowlist**: compare `r.RemoteAddr` to `cfg.Network.AllowedCIDRs` (handler-level recheck); mismatch → `403 ip_not_allowed`. (Defense-in-depth — middleware at SDD-10 has already enforced this; the handler-level check protects against future middleware-stack changes.)

**Rationale**: This order is what the spec's FR-001 locks in; the rationale is "cheapest check first that still rejects without consulting the operator". Verify before nonce so a tampered request never consumes a nonce slot (which would otherwise let an attacker DoS the cache by replaying captured-but-tampered requests).

**Alternatives considered**:
- *Nonce before signature* (cheaper): rejected — fills the nonce cache with attacker-chosen values, helping enumeration.
- *Aggregate failures into one response*: rejected by FR-002 (short-circuit).

---

## R-006 — Sentinel-leak strategy

**Decision**: `TestClaim_ErrorBodyNoSentinel` builds a request with `reason = "SECRET_SHOULD_NEVER_APPEAR_12"`, forces `sign.Verify` to return `ErrSignatureInvalid` (via a deliberately-tampered signature), and asserts:
- the response body bytes do not contain the sentinel
- the captured `slog` buffer (a `*bytes.Buffer` configured as `Options.Out`) does not contain the sentinel
- the captured `AuditWriter` invocation `Detail["reason"]` is absent (not just redacted — absent from the map keys)

The helper `testutil.AssertSentinelAbsent` is reused (already locked in SDD-04).

**Rationale**: A sentinel test is the only test that proves "we did not log this field somewhere we forgot to redact". String redaction (`internal/logging.RedactString`) targets *credential-class regexes*, not arbitrary user input — so redaction will NOT catch `SECRET_SHOULD_NEVER_APPEAR_12`. The only safe behaviour is to never put `reason` (or any client-supplied field other than `request_id`) into the response body or the operational log line. The test asserts the policy directly.

**Alternatives considered**:
- *Trust redaction*: rejected — see above; redaction patterns are credential-shaped, not arbitrary user-string-shaped.

---

## R-007 — `TestClaim_NoAutoApproveKnobExists` is a code-grep test

**Decision**: The test compiles and runs `grep` (via `os/exec` with a fixed binary path validated at test setup) against `internal/server/*.go` looking for the substring "auto" within five lines of "approve" (case-insensitive). Failure mode: **any match fails the test**, with a message instructing the engineer to (a) document the legitimate use, or (b) remove the term. Additionally, the test exhaustively iterates over `Deps` field combinations that could plausibly route around the approver (nil approver, fake-always-approve approver, etc.) and asserts the handler still rejects under `ErrApproverUnavailable`.

**Rationale**: SC-004 requires zero configuration surface that maps `ErrApproverUnavailable` → 200. A grep-based regression test is the cheapest compile-time-ish check that prevents future PRs from re-introducing the term "auto-approve" silently. The Deps-permutation test exhaustively covers the runtime side.

**Alternatives considered**:
- *Static analysis lint rule*: rejected — heavier than a 20-line test; one-off use case.
- *Code review*: rejected — humans miss this; the constitution mandates a regression test.

---

## R-008 — Test scaffolding reuses `testutil.DiscordStub` for the integration leg only

**Decision**: Unit tests use a private `fakeApprover` type defined inside `claim_handler_test.go` that returns chassis-level sentinels directly (not discord-package errors). The integration test (`//go:build integration`) wraps `testutil.DiscordStub` in a small adapter that translates `testutil.Decision` into `(server.Decision, error)` so the full SDD-04 stub is exercised end-to-end.

**Rationale**: The unit tests want fine-grained control over exact sentinel returns; the integration leg wants to exercise the same code path that production wiring will use, so the adapter pattern (testutil → chassis) is rehearsed in test code before SDD-14 lands the production adapter.

**Alternatives considered**:
- *DiscordStub everywhere*: rejected — overhead for unit tests that just want to assert "approver returned ErrApproverDenied → 403 denied".
- *Skip integration test*: rejected — Constitution VIII mandates integration tests for AC-1, AC-3, AC-4.

---

All open questions are resolved. No `[NEEDS CLARIFICATION]` markers remain in the spec or the plan. Phase 0 is complete.
