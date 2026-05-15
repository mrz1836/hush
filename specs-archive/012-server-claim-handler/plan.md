# Implementation Plan: Server `/claim` Handler

**Branch**: `012-server-claim-handler` | **Date**: 2026-04-30 | **Spec**: [spec.md](./spec.md)
**Input**: [docs/sdd/SDD-12.md](../../docs/sdd/SDD-12.md), [docs/API.md](../../docs/API.md), [docs/SPEC.md](../../docs/SPEC.md), [docs/LIFECYCLE-SCENARIOS.md](../../docs/LIFECYCLE-SCENARIOS.md), [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md)

## Summary

Implement `POST /h/<prefix>/claim` as `(s *Server).handleClaim` in `internal/server`, registered via a new `(s *Server).RegisterHandlers()` entry point. The handler runs a fixed pre-approval pipeline (shape → canonical-JSON signature verify → nonce → timestamp → IP allowlist), caps TTL to the per-session-type configured maximum, invokes `s.approverImpl.RequestApproval`, and on a positive `(Decision, nil)` return mints a session token via `token.Issue`. Every outcome — including the four named approver outcomes (Approve/Deny/Timeout/Unavailable) plus rate-limited (429), unknown outcome, and every pre-approval rejection — emits exactly one audit event and returns the documented status with a body of `{error, request_id}` or `{jwt, expires_at, jti}`. The 503 path has zero branches that can flip to Approve (Constitution II); a sentinel-leak test proves error bodies and operational logs never echo client-supplied fields.

## Technical Context

**Language/Version**: Go 1.24 (per `go.mod`)
**Primary Dependencies**: `internal/server` (chassis, SDD-10), `internal/transport/sign` (canonical JSON, signature verify, nonce cache, timestamp window — SDD-08), `internal/token` (ES256K JWT issue — SDD-07), `internal/config` (TTL caps, claim approval timeout, client registry path — SDD-06), `internal/discord` (sentinel errors translated by `cmd/hush` adapter — SDD-11), `internal/keys` (client-key fingerprint helper — SDD-01), `internal/logging` (redacting `*slog.Logger` — SDD-05). New direct deps: none.
**Storage**: None added. The existing client-key registry file (config `Server.ClientRegistry`, default `~/.hush/clients.json`) is read once at handler-init time via a small loader that returns a fingerprint→`*ecdsa.PublicKey` map (additive helper, not a new package).
**Testing**: `go test -race` for unit; `//go:build integration` plus `magex test:race -tags=integration` for the integration leg using `testutil.DiscordStub`. Coverage target ≥ 95% on the claim-handler portion of `internal/server` (Constitution VIII High tier).
**Target Platform**: macOS + Linux (server only runs on the trusted host; binds Tailscale CGNAT 100.64/10).
**Project Type**: Single Go module (`internal/`-only library code; `cmd/hush` is the lone main).
**Performance Goals**: Handler self-cost (excluding signature verify, nonce add, approver wait, token issue) ≤ 50 ms p99 (SC-006).
**Constraints**: Constitution II (no auto-approve under any reachable config — SC-004), Constitution IV (TTL cap before approver), Constitution VIII (TDD-mandatory; 95% coverage; sentinel-leak test), Constitution X (zero secret bytes in error bodies and operational logs; audit event omits signature, nonce, ephemeral pubkey, reason, JWT bytes).
**Scale/Scope**: One handler. Concurrent invocation across many request goroutines; the only mutable state is the upstream nonce cache and the upstream token store (both already concurrent-safe).

## Constitution Check

Per Constitution v1.1.0, this plan is screened against every principle; II / IV / VIII / X are explicitly load-bearing.

| Principle | Verdict | How this plan satisfies it |
|-----------|---------|----------------------------|
| **I. Zero files at rest on agent machines** | ✅ N/A | Server-side; no agent-side artifact added. |
| **II. Approval is human, approval is phone** | ✅ Pass | The handler's only path to a `200` is the `(Decision{Approved:true}, nil)` return from `approverImpl.RequestApproval`. Every other outcome (deny, timeout, unavailable, rate-limited, unknown, every pre-approval failure) is a non-200 with a static error code, no token issued. The `ErrApproverUnavailable` mapping is unconditional — no flag, env var, build tag, or runtime mode can flip it to 200 (SC-004; asserted by `TestClaim_DiscordUnavailable_503` and `TestClaim_NoAutoApproveKnobExists`). |
| **III. Defense in depth through crypto layering** | ✅ Pass | Reuses SDD-08 canonicalisation + secp256k1 verify, SDD-07 ES256K JWT, SDD-09 ECIES is not in this hop. No new crypto primitives introduced. |
| **IV. Supervisor for daemons, wrap-shell for humans** | ✅ Pass | TTL cap is `min(req.TTL, cfg.Crypto.MaxInteractiveTTL)` for `SessionInteractive` and `min(req.TTL, cfg.Crypto.MaxSupervisorTTL)` for `SessionSupervisor`. Cap is applied **before** `RequestApproval` so the operator's prompt shows the actual TTL (FR-016). Supervisor tokens have `MaxUses=0` (TTL-only) per `token.Issue` contract. |
| **V. Staleness is visible, failure is loud** | ✅ Pass | Each pre-approval failure class maps to a distinct, audit-emitting outcome label. Operator-visible 408/503/429 distinctions preserve the diagnostic surface. |
| **VI. Tailscale-only, never public** | ✅ Pass | The handler runs behind the chassis IP-allowlist middleware (SDD-10); it does not relax that contract. The `ip_not_allowed` error code in this handler is the **client-IP-vs-allowlist** check (FR-013) — distinct from the socket-level allowlist that SDD-10 already enforces. (Plan treats the per-handler IP check as additive defense-in-depth: even if a future middleware change weakens the network gate, the handler still rejects at L7.) |
| **VII. CLI design standards** | ✅ N/A | No CLI surface added. |
| **VIII. Testing discipline** | ✅ Pass | TDD-mandatory; tasks file (next phase) leads with test files. Required tests: `TestClaim_BadRequest_400`, `TestClaim_BadSignature_403`, `TestClaim_NonceReplay_403`, `TestClaim_StaleTimestamp_403`, `TestClaim_IPNotAllowed_403`, `TestClaim_DiscordTimeout_408`, `TestClaim_DiscordUnavailable_503`, `TestClaim_RateLimited_429`, `TestClaim_UnknownOutcome_503`, `TestClaim_Approved_IssuesJWT`, `TestClaim_SupervisorRequest_DaemonLabel`, `TestClaim_TTLCappedAtConfigMax`, `TestClaim_TTLZeroOrNegative_400`, `TestClaim_AuditEventEmittedForEveryOutcome`, `TestClaim_NoAutoApproveKnobExists`, `TestClaim_ErrorBodyNoSentinel`, plus integration leg `TestClaim_Integration_FullFlow_DiscordStub`. Coverage policy: 95% for the new code; no regression of overall server-package coverage by more than 2%. |
| **IX. Idiomatic Go discipline** | ✅ Pass | Handler signature `(s *Server) handleClaim(w http.ResponseWriter, r *http.Request)`; uses `r.Context()` end-to-end (no stored ctx); errors wrap via `%w` and compare via `errors.Is`; sentinel errors are package-level vars; no globals, no `init()`. New types are unexported except where needed for JSON marshalling. The fingerprint→pubkey map loader is a private helper, no public symbol. |
| **X. Observability & redaction** | ✅ Pass | Operational logs contain only `request_id`, `client_ip`, `outcome`, `scope_count`, `session_type`, capped TTL (where applicable), and the bare error category — never the signature, nonce, ephemeral pubkey, reason, machine name, scope contents, JWT, or any secret byte. Audit events follow the same redaction list (FR-022, FR-023). The `claim_audit_emitted` slog handler chain inherits the redacting `*slog.Logger` from `internal/logging`. Sentinel-leak test (`TestClaim_ErrorBodyNoSentinel`) injects `SECRET_SHOULD_NEVER_APPEAR_12` into `reason` and asserts it appears in neither the response body nor the captured slog buffer. |
| **XI. Native-first, minimal dependencies, ephemeral vault** | ✅ Pass | Zero new direct dependencies. Vault is not touched by this hop. |

**Verdict**: All gates pass. No `Complexity Tracking` justifications required for principle deviations.

## Project Structure

### Documentation (this feature)

```text
specs/012-server-claim-handler/
├── plan.md              # This file
├── spec.md              # /speckit-specify output
├── research.md          # Phase 0 — orientation decisions
├── data-model.md        # Phase 1 — entities + outcome enum
├── quickstart.md        # Phase 1 — how to test locally
├── contracts/
│   └── api.md           # Phase 1 — POST /h/<prefix>/claim contract
└── tasks.md             # Phase 2 — created by /speckit-tasks
```

### Source Code (repository root)

```text
internal/server/
├── claim_handler.go                       # NEW: handleClaim + request/response/error types + RegisterHandlers + sentinel errors
├── claim_handler_test.go                  # NEW: unit tests (every outcome + sentinel-leak + no-auto-approve grep)
├── claim_handler_integration_test.go      # NEW (//go:build integration): full flow w/ testutil.DiscordStub
├── server.go                              # EDIT: Deps gains optional ClientKeyResolver field (defaults to file-loader using cfg.Server.ClientRegistry)
├── errors.go                              # EDIT: append ErrApproverDenied, ErrApproverTimeout, ErrApproverUnavailable, ErrApproverRateLimited, ErrClientUnknown
└── (router.go, middleware.go, request_id.go, approver.go, audit, etc. — unchanged)

internal/config/
├── server.go                              # EDIT: CryptoSection gains ClaimApprovalTimeout time.Duration
├── defaults.go                            # EDIT: DefaultClaimApprovalTimeout = 60 * time.Second
└── server_test.go                         # EDIT: cover new field's parse + default
```

**Structure Decision**: The handler lives entirely inside `internal/server` per `docs/PACKAGE-MAP.md` (server owns HTTP route registration). No new package is created. Three small additive edits land outside `claim_handler.go`:

1. `internal/config/server.go` + `defaults.go` — adds `Crypto.ClaimApprovalTimeout` (default 60 s) per spec's clarification §3 and FR-006. Additive; no constitutional breach.
2. `internal/server/server.go` — adds an **optional** `Deps.ClientKeyResolver` field of type `func(fingerprint string) (*ecdsa.PublicKey, error)` plus a default file-based loader that reads `cfg.Server.ClientRegistry` once at construction. Additive; preserves SDD-10's locked Deps surface.
3. `internal/server/errors.go` — appends five new chassis-level sentinel errors. Additive.

The chassis Approver error contract is locked here: production wiring (`cmd/hush`, future SDD-14) will install a thin adapter that translates `internal/discord` sentinels into the new chassis sentinels, so the handler stays decoupled from the discord package (mirrors how `testutil.DiscordStub` is already a separate type).

## Complexity Tracking

> No constitutional violations require justification. The four additive surface changes (config field, optional Deps field, five sentinel errors, RegisterHandlers method) are necessary follow-ons to SDD-10's intentionally-incomplete locked surface and are documented in this section solely to keep the audit trail explicit:

| Surface change | Why needed | Why simpler alternative is wrong |
|----------------|------------|----------------------------------|
| `Crypto.ClaimApprovalTimeout` config field | FR-006 + spec clarification §3 require a config-driven uniform deadline (default 60 s) for the approver call. | A handler-side constant violates "ops tune the value without a code change" and breaks consistent operator UX across claims. |
| Optional `Deps.ClientKeyResolver` | The handler needs fingerprint→`*ecdsa.PublicKey` lookup to verify signatures (FR-001 step 2). | Loading the registry inside the handler on every request adds blocking I/O on the hot path; loading it inside `Run` (locked SDD-10 method) violates the "Run does not introspect handler state" property. |
| Five new sentinel errors (`ErrApproverDenied`, `ErrApproverTimeout`, `ErrApproverUnavailable`, `ErrApproverRateLimited`, `ErrClientUnknown`) | Map the chassis Approver's outcome categories to error sentinels the handler uses for status routing. | Coupling to `internal/discord` package errors via `errors.Is` works but violates the chassis abstraction (the test stubs and future non-Discord approvers would have to import the discord package solely for the sentinel surface). |
| `(s *Server) RegisterHandlers() error` | Provides the SDD-12-named entry point for cmd/hush wiring. Internally calls `s.Mount(http.MethodPost, "/claim", ...)` once. | Calling `Mount` directly from `cmd/hush` works but spreads route literals across packages; a single entry point keeps the route table inside `internal/server` (where SDD-13 will add `/secrets/{name}`, `/revoke/{jti}`, `/hz`). |
