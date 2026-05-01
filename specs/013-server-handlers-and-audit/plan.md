# Implementation Plan: Server `/s`, `/revoke`, `/hz` Handlers + Audit Log

**Branch**: `013-server-handlers-and-audit` | **Date**: 2026-05-01 | **Spec**: [spec.md](./spec.md)
**Input**: [docs/sdd/SDD-13.md](../../docs/sdd/SDD-13.md), [docs/API.md](../../docs/API.md), [docs/SPEC.md](../../docs/SPEC.md), [docs/SECURITY.md](../../docs/SECURITY.md), [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md)

## Summary

Land the three remaining vault-server endpoints — `GET /h/<prefix>/s/<name>`, `POST /h/<prefix>/revoke`, `GET /h/<prefix>/hz` — and a NEW package `internal/audit` whose hash-chained, ECDSA-signed `Writer` records every server outcome (claim, secret, revoke, lifecycle, reload, perm-check, chat-platform connectivity, mirror failure). The handlers compose existing layers: `token.Validate` (SDD-07) for `/s`; `sign.CanonicalJSON`+`sign.Verify`+`NonceCache`+`IsFreshTimestamp`+`token.Store.Revoke` (SDD-08, SDD-07) for `/revoke`; vault-loaded / token-store / discord-health / clock / config introspection for `/hz` (no auth — Constitution VI). The `internal/audit` package owns a single goroutine that reads from a buffered channel, assigns monotonic `Seq`, computes `Hash = SHA-256(prevHash || canonicalJSON(event-without-Hash-or-Signature))`, signs with the BIP32-derived audit key, persists `audit.jsonl` (mode 0600, append-only), and best-effort mirrors to a Discord channel via a separate non-blocking goroutine. `Append` blocks under buffer pressure (FR-031) and returns a documented shutdown error after `Run`'s ctx is cancelled (FR-039). A thin chassis adapter implements the locked `server.AuditWriter` surface on top of `audit.Writer.Append`. `(s *Server).RegisterHandlers()` is extended to mount `/s/{name}`, `/revoke`, `/hz`. Sentinel-leak tests assert `SECRET_SHOULD_NEVER_APPEAR_13` is absent from every response body, every operational log record, every audit `Data` payload. The chain-tamper test re-verifies the on-disk file after a single-byte mutation and asserts the surfaced `ErrAuditChainBroken` names the first inconsistent event.

## Technical Context

**Language/Version**: Go 1.24 (per `go.mod`)
**Primary Dependencies**: `internal/server` (chassis, SDD-10; claim handler, SDD-12), `internal/token` (`Validate`, `Store.Revoke`, claims — SDD-07), `internal/transport/sign` (canonical JSON, verify, nonce cache, timestamp window — SDD-08), `internal/transport/ecies` (`Encrypt` — SDD-09), `internal/vault` (in-memory `Store.Get` — SDD-03), `internal/keys` (BIP32 audit-signing-key derivation `m/44'/7743'/2'` — SDD-01), `internal/config` (`Server.AuditLog`, `Server.DiscordAuditChannelID` — SDD-06), `internal/logging` (redacting `*slog.Logger` — SDD-05), `internal/discord` (`BotApprover` connectivity probe — SDD-11). New direct dependency: NONE — `github.com/bwmarrin/discordgo` is already on the dep tree (SDD-11) and the audit mirror imports a narrow `MirrorSession` interface (`ChannelMessageSendComplex`) so `internal/audit` does NOT import `internal/discord`.
**Storage**: New file `cfg.Server.AuditLog` (default `~/.hush/audit.jsonl`, already validated by `internal/config`). Append-only writes, mode `0600`, parent dir mode `0700` (FR-015 already enforced by `internal/server/startup_checks.go`). No new schema; format is line-delimited JSON of `audit.Event`. The vault file is NOT touched by this chunk.
**Testing**: `go test -race` for unit; `//go:build integration` for the end-to-end leg (full chassis Run with all three handlers mounted, `audit.NewWriter` writing to a `t.TempDir()` path, a mirror stub). Coverage targets: `internal/server` ≥ 95% on the three new handler files; `internal/audit` = 100% (Constitution VIII security-critical — the audit chain joins vault, keys, token, transport in the 100% tier).
**Target Platform**: macOS + Linux trusted host. Server binds the Tailscale CGNAT interface (Constitution VI; enforced by SDD-10 startup checks).
**Project Type**: Single Go module; new sibling package `internal/audit` joins existing internals. `cmd/hush` is the only main.
**Performance Goals**: `/s` self-cost ≤ 5 ms p99 excluding `token.Validate` and `ECIES.Encrypt`. `/revoke` self-cost ≤ 5 ms p99 excluding signature verify. `/hz` self-cost ≤ 1 ms p99. `audit.Writer.Append` median latency ≤ 100 µs under steady load with empty buffer; under buffer pressure latency tracks the disk-write step (FR-031, no SLA tightening — backpressure is the *intent*).
**Constraints**: Constitution III Layer 6 (signed hash-chain over the audit log; layer-additive — does NOT replace or weaken Layers 1–5), Constitution IV (interactive `max_uses` decremented at most once per accepted retrieval; supervisor never decremented), Constitution VI (`/hz` reachable WITHOUT a JWT — Tailscale is the auth perimeter), Constitution VIII (TDD-mandatory; 95% / 100% coverage; sentinel-leak tests; race-clean concurrency tests), Constitution X (zero secret bytes in error bodies, operational logs, audit `Data` — sentinel-leak proves the property; audit log is separate from operational log; tiered alerts for chain-break = Critical, mirror failure = Warning, routine retrieval = Info-via-audit-only).
**Scale/Scope**: Three handlers + one new package (4 source files + 4 test files in `internal/audit`, 3 source files + 3 test files added to `internal/server`, 1 chassis adapter file). Concurrent producers across many request goroutines + chassis lifecycle hooks; the audit writer is the only goroutine that writes the file or advances the chain.

## Constitution Check

Per Constitution v1.1.0, this plan is screened against every principle. III / IV / VIII / X are explicitly load-bearing for SDD-13.

| Principle | Verdict | How this plan satisfies it |
|-----------|---------|----------------------------|
| **I. Zero files at rest on agent machines** | ✅ N/A | Server-side; the audit log lives on the trusted host (FR-015 perms already enforced), not on agents. |
| **II. Approval is human, approval is phone** | ✅ N/A | This chunk does NOT issue tokens. The `/claim` lock from SDD-12 still binds the only Approve→200 path; the new handlers operate entirely on already-issued tokens (`/s`, `/revoke`) or no token at all (`/hz`). |
| **III. Defense in depth through crypto layering** | ✅ Pass | Layer 6 lands here: every event is hash-chained (`SHA-256(prevHash \|\| CanonicalJSON(event))`) and signed with the BIP32-derived audit key (`m/44'/7743'/2'`). Crypto reuse only — `crypto/sha256`, `crypto/ecdsa`, `internal/transport/sign.CanonicalJSON`. No new crypto primitives, no new dependencies. The chain integrity test (`TestAuditChain_BreakDetectedOnTamper`) is the layer's acceptance assertion. |
| **IV. Supervisor for daemons, wrap-shell for humans** | ✅ Pass | `/s` calls `token.Validate(...)` whose `checkPostParseClaims` already implements the Constitution-IV rule: `ConsumeUse` decrements `MaxUses` for `SessionInteractive` and is a no-op for `SessionSupervisor`. The handler does NOT introduce a parallel use-count path; it inherits the SDD-07 contract verbatim. `TestSecret_SupervisorIgnoresMaxUses` and `TestSecret_ExhaustedInteractive_401` lock the property. |
| **V. Staleness is visible, failure is loud** | ✅ Pass | Distinct outcome labels for every failure class on `/s` and `/revoke` (token expired, scope violation, IP mismatch, exhausted, revoked, unknown JTI, malformed, signature invalid, replay, stale ts) — operator-visible diagnostic surface preserved through audit `Data["outcome"]`. Chain-break detection produces a Critical-tier alert (per docs/OPERATIONS.md tiering) so a tamper is "loud". |
| **VI. Tailscale-only, never public** | ✅ Pass | `/hz` is reachable WITHOUT a JWT BY DESIGN — Constitution VI states the mesh is the auth perimeter; the spec FR-017 locks this. The chassis IP-allowlist middleware (SDD-10) still gates the request at the network layer; `/hz` does not relax that. |
| **VII. CLI design standards** | ✅ N/A | No CLI surface added in this chunk. `hush revoke` (the CLI verb) is owned by SDD-23. |
| **VIII. Testing discipline** | ✅ Pass | TDD-mandatory; tasks file (next phase) leads with test files before each implementation file. Required tests (12 unit + 2 sentinel-leak + 1 race + 1 integration), enumerated in [docs/sdd/SDD-13.md](../../docs/sdd/SDD-13.md) Prompt 4 and [tasks.md] (next phase). Coverage targets: `internal/server` handlers ≥ 95%; `internal/audit` = 100% (security-critical 100% tier per Principle VIII row "Vault crypto, key derivation, JWT, ECIES, request signing" — the audit chain joins this list because the chain's signature is the same ECDSA primitive over canonical JSON). Race-clean is enforced by `magex test:race`; the writer's concurrent-append test asserts exactly N events with monotonic seq. |
| **IX. Idiomatic Go discipline** | ✅ Pass | Handler signatures `(s *Server) handleSecret(w, r)`, `(s *Server) handleRevoke(w, r)`, `(s *Server) handleHealth(w, r)`; uses `r.Context()` end-to-end (no stored ctx); errors wrap via `%w` and compare via `errors.Is`. The audit package's `Run(ctx)` runs ONE long-lived goroutine plus ONE optional mirror goroutine — both have explicit ownership and ctx-driven termination (Principle IX "no fire-and-forget"). Single-method consumer interfaces (`MirrorSession`, `Signer`) are defined where consumed. The audit package has no globals and no `init()`. |
| **X. Observability & redaction** | ✅ Pass | `/s` operational log carries `outcome`, `request_id`, `client_ip`, `secret_name` (only on success — never on out-of-scope failure, where the name has not been authorised), `session_type`, `granted_ttl_remaining` — never the secret value, never JWT bytes, never the ECIES ciphertext, never the ephemeral pubkey. `/revoke` operational log carries `jti`, `outcome`, `request_id`, `client_ip` — never the supplied signature, the supplied nonce, or the request body bytes. `/hz` emits NO log record per request (it is a poll endpoint; logging it dilutes the operational stream). Audit `Data` payload constructed by an explicit allow-list builder per outcome (FR-027 through FR-030); a sentinel-leak test (`TestAudit_RecordNoSecretValue`) injects `SECRET_SHOULD_NEVER_APPEAR_13` as the secret value and asserts the sentinel is absent from every event's serialised JSON form on disk. The mirror's WARN log carries only sequence number, outcome label, and the underlying error class string — never the bot token, never the event's signature. |
| **XI. Native-first, minimal dependencies, ephemeral vault** | ✅ Pass | Zero new direct dependencies. The audit log file is NOT a backed-up artifact; rotation is out of scope for this chunk (per spec "Out of scope") and the vault remains ephemeral (untouched by this chunk). `crypto/sha256` (stdlib), `crypto/ecdsa` (stdlib), `encoding/json` (stdlib), `os` + `bufio` (stdlib) cover the writer; `internal/transport/sign.CanonicalJSON` covers the canonicalisation layer (already-locked SDD-08 surface). |

**Verdict**: All gates pass. No `Complexity Tracking` justifications required for principle deviations. The four additive surface changes (one new package, one chassis adapter, three new sentinel errors in `internal/audit`, two new method signatures in `RegisterHandlers`) are necessary follow-ons to SDD-10's intentionally-incomplete locked surface and to SDD-13's chunk contract; they are documented in `Complexity Tracking` solely to keep the audit trail explicit.

## Project Structure

### Documentation (this feature)

```text
specs/013-server-handlers-and-audit/
├── plan.md              # This file
├── spec.md              # /speckit-specify output (already present)
├── research.md          # Phase 0 — orientation decisions
├── data-model.md        # Phase 1 — entities, outcome enum, audit Event shape
├── quickstart.md        # Phase 1 — how to drive the new handlers + audit chain locally
├── contracts/
│   ├── api.md           # Phase 1 — GET /s/<name>, POST /revoke, GET /hz contracts
│   └── audit.md         # Phase 1 — internal/audit package surface contract
└── tasks.md             # Phase 2 — created by /speckit-tasks
```

### Source Code (repository root)

```text
internal/audit/                              # NEW package
├── doc.go                                   # NEW: package doc, principle map
├── chain.go                                 # NEW: Event struct, hash + sign helper, ErrAuditChainBroken, Verify()
├── writer.go                                # NEW: Writer interface, NewWriter, Append, Run, single-goroutine impl, shutdown drain
├── discord_mirror.go                        # NEW: DiscordMirror type, NewDiscordMirror, MirrorSession interface, Publish() best-effort
├── chain_test.go                            # NEW: TestAuditChain_HashLinkContiguous, _SignatureValid, _BreakDetectedOnTamper, _BreakDetectedOnDelete, _BreakDetectedOnForgedSignature
├── writer_test.go                           # NEW: TestAuditWriter_BlocksOnBackpressure, _ConcurrentAppendMonotonicSeq (race), _DrainOnShutdown, _AppendAfterShutdownReturnsError, _ChainResumesFromTail, _NoSentinelInOnDiskBytes
├── discord_mirror_test.go                   # NEW: TestDiscordMirror_FailureLogsWarnNoBlock, _EmptyChannelIDDisablesPublish, _NoBotTokenInWarn, _ChainUnaffectedByMirrorFailure
└── coverage_test.go                         # NEW: per-file coverage assertion (matches the internal/vault, internal/server pattern)

internal/server/
├── secret_handler.go                        # NEW: handleSecret + sentinel + outcome enum + builder + RegisterHandlers extension
├── revoke_handler.go                        # NEW: handleRevoke + sentinel + outcome enum + builder
├── health_handler.go                        # NEW: handleHealth + JSON shape + count derivation + discord-health probe
├── audit_adapter.go                         # NEW: chassisAuditAdapter implements server.AuditWriter via audit.Writer.Append
├── secret_handler_test.go                   # NEW: TestSecret_HappyPath_ECIESPayload, _ExpiredJWT_401, _OutOfScope_403, _WrongIP_401, _ExhaustedInteractive_401, _SupervisorIgnoresMaxUses, _RevokedJWT_401, _UnknownJTI_401, _MalformedJWT_401, _MissingAuthHeader_401, _UnsupportedScheme_401, _SecretMissingInVault_404, _AuditEventEmittedForEveryOutcome, _ErrorBodyNoSentinel
├── revoke_handler_test.go                   # NEW: TestRevoke_HappyPath, _BadSignature_403, _UnknownJTI_403_AsBadSignature, _ReplayedNonce_403, _StaleTimestamp_403, _MalformedBody_400, _IdempotentReRevocation_200_StaticBody, _AuditEventForEveryOutcome, _ErrorBodyNoSentinel
├── health_handler_test.go                   # NEW: TestHealth_NoAuth_OK, _DiscordConnectedFlag, _VaultLoadedFalseDuringStartup, _NoSecretNameInBody, _NoTokenIdentifierInBody, _NoBotTokenInBody, _NoAuditEvent
├── audit_adapter_test.go                    # NEW: TestAuditAdapter_TranslatesAuditEventToActionAndDetail, _PreservesRequestIDAndClientIP, _PassesThroughError
├── server.go                                # EDIT: Deps gains optional DiscordHealth field of type func() bool (defaults to nil → reports disconnected)
├── claim_handler.go                         # EDIT: extend (s *Server).RegisterHandlers() to mount the three new routes
├── errors.go                                # EDIT: append ErrSecretMissing (404 mapping helper for /s — used internally; not surfaced to wire bodies beyond the static error code)
└── (router.go, middleware.go, request_id.go, approver.go, reload.go, startup_checks.go — unchanged)

internal/keys/
└── (no edits this chunk; the audit-signing key m/44'/7743'/2' is already derivable from SDD-01's BIP32 hierarchy)

cmd/hush/
└── (no edits this chunk; cmd/hush wiring of audit.NewWriter + chassisAuditAdapter is owned by SDD-14)
```

**Structure Decision**: Two packages cohabit the chunk per `docs/PACKAGE-MAP.md` (server owns HTTP handlers; audit owns the chain/writer/mirror). The chassis-side `AuditWriter` interface from SDD-10 is preserved verbatim; a small adapter in `internal/server/audit_adapter.go` translates `AuditEvent → (action, data map[string]any)` so the chassis stays decoupled from the audit package's wire vocabulary. `cmd/hush` (SDD-14) installs the adapter at construction time. The plan adds three additive surface changes outside the new package:

1. `internal/server/server.go` — adds an **optional** `Deps.DiscordHealth func() bool` field. When nil the chassis treats discord as disconnected (fail-closed). The production wiring (`cmd/hush`) supplies a closure that reads `BotApprover.available.Load()` via a new exported `(a *BotApprover) Connected() bool` accessor (the accessor is a one-line additive change to `internal/discord/bot.go` that the plan authorises here; it carries no contract risk because `available` is already an `atomic.Bool` field).
2. `internal/server/errors.go` — appends `ErrSecretMissing` (used only inside the secret handler's flow; never surfaces a body field).
3. `internal/server/claim_handler.go` (`RegisterHandlers`) — append three additional `s.Mount(...)` calls.

The chassis Approver / TokenIssuer / TokenStore contracts from SDD-10/SDD-11/SDD-12 are NOT modified.

## Complexity Tracking

> No constitutional violations require justification. The five additive surface changes (new `internal/audit` package, optional `Deps.DiscordHealth`, `BotApprover.Connected()` accessor, `ErrSecretMissing` sentinel, three new routes mounted from `RegisterHandlers`) are necessary follow-ons to SDD-10's intentionally-incomplete locked surface, SDD-11's intentionally-private connectivity flag, and SDD-13's chunk contract. They are documented here solely to keep the audit trail explicit:

| Surface change | Why needed | Why simpler alternative is wrong |
|----------------|------------|----------------------------------|
| New `internal/audit` package (Event, Writer, NewWriter, DiscordMirror, NewDiscordMirror, ErrAuditChainBroken) | SDD-13 chunk contract locks this surface; the chassis `AuditWriter.Write(ctx, AuditEvent)` is intentionally too narrow to carry the chain semantics (Seq, PrevHash, Hash, Signature) and on-disk persistence ownership. | Inlining the writer inside `internal/server` would couple HTTP-handler files to file I/O + crypto + a long-lived goroutine, breaking Principle IX's "interfaces at the consumer" and the chassis's "single-call lifecycle" property. A separate package keeps the chain primitive reusable by future `hush audit verify` tooling (out of scope here) without circular imports. |
| Optional `Deps.DiscordHealth func() bool` | `/hz` must report `discord_connected` per FR-018 + spec User Story 4. The chassis cannot read `BotApprover` state directly without coupling. | A required field would break SDD-10's locked Deps surface; an unconditional read of the BotApprover would force the chassis to import `internal/discord` (forbidden by `docs/PACKAGE-MAP.md` import rules). The injection seam mirrors how `Deps.Approver` itself is supplied. |
| `(a *BotApprover) Connected() bool` accessor in `internal/discord/bot.go` | The default `Deps.DiscordHealth` closure in `cmd/hush` (SDD-14) needs to read the WebSocket-available flag without touching unexported state. | Returning an `atomic.Bool` pointer from the constructor exposes mutable state; a method is the idiomatic seam. The implementation is `func (a *BotApprover) Connected() bool { return a.available.Load() }` — one line, additive, no contract risk. |
| `ErrSecretMissing` sentinel | The secret handler's "in-scope-but-not-in-vault" path needs a typed error to drive the 404 outcome → audit label without overloading existing token/vault sentinels. | Reusing `vault.ErrSecretNotFound` works in code but couples the handler's outcome enum to a non-server package's sentinel surface; a chassis-level sentinel keeps outcome-routing local. |
| Three new `s.Mount(...)` calls in `RegisterHandlers` | The chunk's three handlers must be reachable; SDD-12 documented `RegisterHandlers` as the single entry point. | Adding a `RegisterSecretHandlers` / `RegisterHealthHandler` per-handler entry point would proliferate registration calls in `cmd/hush`; the single entry point is what SDD-12 locked. |
