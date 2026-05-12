# Implementation Plan: Supervisor Orchestrator (SDD-24)

**Branch**: `026-supervisor-orchestration` | **Date**: 2026-05-12 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification at `/specs/026-supervisor-orchestration/spec.md`

## Summary

SDD-24 ships the production orchestrator that composes the locked SDD-19..22
primitives (`Store`, `Child`, `Refiller`, `Refresher`, `Grace`, `StatusServer`,
`PidFile`) into the documented supervisor daemon lifecycle: pidfile acquire →
StatusServer + Refresher goroutines → boot-retry against Tailscale + vault →
signed `/claim` → JWT into Store → `Refiller.Refill` → injected validators →
child env build → `NewChild.Start` → `Child.Wait` loop with `0` / non-zero-non-`78`
/ `78` dispatch → Refresher window-tick swap → SIGTERM clean shutdown. Validator,
Alerts, and Watchdog are defined here as injected single-method interfaces with
no-op defaults so SDD-26/27/28 fill them later without API churn. The audit-event
vocabulary is reconciled by extending `internal/audit/chain.go`'s `Action*`
constants block with the twelve supervisor-scope names spec FR-026-014 enumerates.

**Technical approach**: orchestrator lives inside `package supervise` (new file
`lifecycle.go` plus four siblings) so it can consume the SDD-19/21 package-private
wiring seams (`(*Refiller).attach`, `(*Store).setTokenForTest` renamed `setToken`)
WITHOUT mutating any SDD-19..22 locked exported surface. `internal/cli/supervise.go`
becomes a thin ~80-LOC shim that loads config, acquires the pidfile, and delegates
to `supervise.NewLifecycle(deps).Run(ctx)`. Three orchestrator-owned goroutines
(`childWaitLoop`, `claimRefreshLoop`, `mainLoop`) plus the two carried over from
the primitives (`StatusServer.Run`, `Refresher.Run`) each carry the Constitution-IX
owner + ctx + termination + top-frame `recover` quadruple. The line-splitting
watchdog stderr fan-out is a `io.Writer` wrapper that adapts the SDD-20 single-drain
contract to also notify `Watchdog.OnStderrLine` per emitted line.

## Technical Context

**Language/Version**: Go (toolchain pinned in `go.mod`; pure-Go release per
Constitution IX — CGO disabled at the `.goreleaser.yml` layer)
**Primary Dependencies**: stdlib only (`net/http`, `context`, `crypto/ecdsa`,
`encoding/json`, `log/slog`, `os/signal`, `sync`, `sync/atomic`, `syscall`,
`time`); existing in-tree packages (`internal/supervise`, `internal/transport/sign`,
`internal/transport/ecies`, `internal/audit`, `internal/vault/securebytes`).
**No new direct `go.mod` dependency** (Constitution XI; spec FR-026-027).
**Storage**: in-memory only (cached JWT lives in `Store.token` as `*SecureBytes`;
Grace-resident decrypted secrets as `*SecureBytes`); ephemeral pidfile + Unix
status socket on the local filesystem at config-supplied paths.
**Testing**: `magex test:race` (Go race detector enabled); table-driven unit
tests in `internal/supervise/lifecycle_test.go`; integration tests gated by
`//go:build integration` in `internal/supervise/lifecycle_integration_test.go`
(per Constitution VIII; spec FR-026-021..024).
**Target Platform**: darwin + linux, amd64 + arm64 (the supervise package
already carries platform-specific death-watch wiring in `child_linux.go` /
`child_darwin.go`; the orchestrator is platform-agnostic per spec
anti-contract FR-023-7).
**Project Type**: single-binary CLI (cobra-driven `hush supervise <config>`
subcommand). The new orchestrator code is part of the existing `hush` binary
under `internal/`; no new module, no new top-level binary.
**Performance Goals**:
  - Boot-to-`running` after approver tap: bounded by a `runtime.Gosched()`-poll,
    never by `time.Sleep` (spec FR-026-025; SC-026-001).
  - Boot-retry budget: cumulative attempts MUST stay inside `boot_retry_timeout`
    (default 10 minutes per `docs/CONFIG-SCHEMA.md`); per-attempt HTTP probe
    timeout ≤ 2s.
  - Silent-refill round-trip (child exit → child restart): < 5s end-to-end on
    a hot vault server (illustrative, not a hard gate).
  - SIGTERM-to-exit shutdown: bounded by `shutdown_grace_timeout` (Plan
    decision below); SIGKILL escalation as the hard ceiling.
**Constraints**:
  - No new `go.mod` direct dependency (Constitution XI / spec FR-026-027).
  - No mutation of SDD-19..22 exported surfaces (PACKAGE-MAP locks: SDD-19's
    state.Store, SDD-20's Child, SDD-21's "3 ctors + 4 methods + 2 sentinels +
    Evict", SDD-22's PidFile + StatusServer + StatusInputs). The orchestrator
    consumes the locked surfaces only and adds NEW symbols (Lifecycle / Deps /
    New / Run / Validator / Alerts / AlertClass / AlertPayload / Watchdog +
    sentinels) that become SDD-24's locked surface.
  - No `string(*SecureBytes)` outside Refiller's permitted JWT bearer-header
    site (Constitution X; spec FR-026-028).
  - No platform branching, no `runtime.GOOS`, no raw state-string literals,
    no raw `78` arithmetic in the orchestrator file (spec FR-026-023).
  - ≥ 85% line coverage on the new orchestrator file(s) under
    `magex test:race` (spec FR-026-021; SC-026-006). SDD-19..22 coverage MUST
    NOT regress (chunk-doc carryover).
**Scale/Scope**:
  - Estimated LOC: ~700-900 across 5-6 orchestrator files + ~80 in the cli
    shim + ~12 lines added to `internal/audit/chain.go`.
  - 12-15 unit-test cases + 2-4 integration cases (spec FR-026-022; chunk doc
    "Tests required" list).
  - 10 `AlertClass` enum values (spec FR-026-016 — locked at this exact set).
  - 12 new `audit.Action*` constants (Plan §Audit Vocabulary table below).

## Constitution Check

*GATE: re-checked post-Phase-1 (see end of file).*

The orchestrator is constitutional under every in-scope principle. The two
material gates are surfaced in Complexity Tracking below.

| Principle | Scope | Pre-Phase-0 verdict |
|-----------|-------|--------------------|
| IV — Supervisor for daemons | State machine, silent refill, exit-78 contract, refresh window, grace cache, single approval covers crashes within TTL. | ✅ Plan implements the documented lifecycle verbatim against the SDD-19..22 primitives; the chunk-doc Behaviour Contracts table mapped 1:1 to plan §Implementation Contract. |
| V — Staleness is visible, failure is loud | Validators run before child start; exit 78 / `ErrJTIUnknown` / validator failure produce three distinct `[STALE] …` alerts; status socket reflects every transition. | ✅ Plan locks Alerts interface at exactly 10 `AlertClass` values, one per documented emission site (spec FR-026-016). |
| VII — CLI design standards | Business logic OUT of the cli layer; orchestrator code lives under `internal/supervise/`. | ✅ Plan picks Option C (orchestrator inside `package supervise`); cli/supervise.go shrinks to ~80 LOC, all business logic moves into `internal/supervise/lifecycle*.go`. |
| VIII — Testing discipline | TDD-mandatory; one test per exit-code branch + one per boot-retry branch; sentinel-leak assertion; race-clean; ≥85% line coverage. | ✅ Plan-phase Tests-Required table below pins 12 unit cases + 2 integration cases + 2 anti-leak greppers. |
| IX — Idiomatic Go discipline | Every spawned goroutine has owner + ctx + termination + top-frame recover; no `init()`; no package-level mutable state; interfaces defined at the consumer; context first param. | ✅ Plan §Goroutine Inventory enumerates 5 goroutines (3 orchestrator-owned + 2 carried from primitives) each with the full quadruple. Three injected interfaces are defined inside `package supervise` (the consumer), not imported from a producer. Only sentinel `var Err…` package-level vars; no `init()`. |
| X — Observability & Redaction | No `string(*SecureBytes)` outside the JWT bearer-header site; alert payloads carry scope name + error class strings ONLY; sentinel-leak fuzz/unit-test gates. | ✅ Plan §Secret-Bytes Audit confirms zero new `string(secretBytes)` sites; AlertPayload carries 3 string fields (scope / errorClass / reason) by construction. |

## Project Structure

### Documentation (this feature)

```text
specs/026-supervisor-orchestration/
├── plan.md              # this file
├── spec.md              # already authored
├── research.md          # Phase-0 output (this command)
├── data-model.md        # Phase-1 output (this command)
├── quickstart.md        # Phase-1 output (this command)
├── contracts/           # Phase-1 output (this command)
│   ├── lifecycle.go.md         # Lifecycle / Deps / New / Run contract
│   ├── interfaces.go.md        # Validator / Alerts / Watchdog contract
│   └── audit-vocabulary.md     # 12 new audit.Action* constants
├── checklists/          # (carry-over from existing scaffold)
└── tasks.md             # Phase-2 output (NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/
├── cli/
│   └── supervise.go                         # ~80 LOC — thin shim; loads config, acquires pidfile,
│                                            #   constructs Deps, delegates to supervise.NewLifecycle.
│                                            #   Keeps the FR-023 dry-run / flag-override / pidfile-error
│                                            #   handling carry-over.
├── supervise/
│   ├── state.go                             # SDD-19 UNCHANGED externally; renames `setTokenForTest`
│   │                                        #   to `setToken` (still package-private) so the orchestrator
│   │                                        #   in the same package writes the JWT post-/claim.
│   ├── refill.go                            # SDD-21 UNCHANGED externally; the existing package-private
│   │                                        #   (*Refiller).attach is consumed by the orchestrator
│   │                                        #   in this package.
│   ├── grace.go / refresh.go / child.go /   # UNCHANGED.
│   │   socket.go / pidfile.go / ringbuf.go
│   ├── lifecycle.go                         # NEW ~250 LOC — Lifecycle struct, Deps struct,
│   │                                        #   NewLifecycle, Run (top-level dispatcher),
│   │                                        #   mainLoop, shutdown sequencing.
│   ├── lifecycle_interfaces.go              # NEW ~80 LOC — Validator, Alerts, AlertClass enum
│   │                                        #   (10 values), AlertPayload struct, Watchdog;
│   │                                        #   no-op default implementations.
│   ├── lifecycle_boot.go                    # NEW ~180 LOC — boot precondition check
│   │                                        #   (Tailscale interface + vault /hz), exponential
│   │                                        #   backoff loop, signed /claim submission +
│   │                                        #   503-body parsing for discord_unavailable,
│   │                                        #   initial Refiller.Refill + validator pass.
│   ├── lifecycle_child.go                   # NEW ~200 LOC — child env build from Grace,
│   │                                        #   NewChild + Start, childWaitLoop goroutine,
│   │                                        #   exit-code dispatch (0 / non-zero-non-78 / 78),
│   │                                        #   line-splitting watchdog stderr writer.
│   ├── lifecycle_refresh.go                 # NEW ~120 LOC — claimRefreshLoop goroutine,
│   │                                        #   refresh-window callback, refresh-denied /
│   │                                        #   refresh-timeout dispatch, ErrJTIUnknown handler,
│   │                                        #   status-socket refresh-verb dispatcher.
│   ├── lifecycle_audit.go                   # NEW ~80 LOC — audit-emission helpers binding
│   │                                        #   Lifecycle to the audit writer (injected via Deps);
│   │                                        #   reason-tag projection for supervisor_stale_alert
│   │                                        #   + supervisor_awaiting_approval Data fields.
│   ├── lifecycle_test.go                    # NEW ~600 LOC — 12+ unit cases (one per exit-code
│   │                                        #   branch, one per boot-retry branch, sentinel-leak,
│   │                                        #   no-business-logic-leakage grepper).
│   └── lifecycle_integration_test.go        # NEW ~300 LOC (//go:build integration) — 2-4
│                                            #   end-to-end cases driving the full boot path
│                                            #   against httptest.Server + stub Approver.
└── audit/
    └── chain.go                             # EXTENDED — 12 new exported Action* constants
                                             #   appended to the existing block per spec FR-026-014.
                                             #   Append-only per audit/chain.go header comment.

docs/
├── PACKAGE-MAP.md                           # UPDATED at implement-phase: SDD-24 locked surface
│                                            #   appended (Lifecycle / Deps / NewLifecycle / Run +
│                                            #   3 interfaces + AlertClass + AlertPayload +
│                                            #   sentinels). SDD-19/21 locked surfaces UNCHANGED.
├── AC-MATRIX.md                             # UPDATED: AC-10 row references the new orchestrator
│                                            #   unit + integration tests as evidence.
├── SPEC.md                                  # UPDATED §FR-14: adds the two missing supervisor-scope
│                                            #   names (`supervisor_child_exit_crash`,
│                                            #   `supervisor_boot_timeout`) to the documented
│                                            #   audit-event list; existing 10 entries unchanged.
└── SDD-PLAYBOOK.md                          # UPDATED: SDD-24 marked done.

specs/025-lifecycle-harness/
└── tasks.md                                 # UPDATED: pause banner removed.
```

**Structure Decision**: **Option C** — orchestrator inside `package supervise`
as new sibling files (`lifecycle.go` + 4 supporting files). This is a deliberate
deviation from SDD-24 chunk-doc Option B (sub-package `internal/supervise/lifecycle`)
and is justified in Complexity Tracking below. The cli layer at
`internal/cli/supervise.go` shrinks to a ~80-LOC shim — Constitution VII outcome
identical to chunk-doc Option B.

## Implementation Contract

The Plan locks every HOW the chunk doc designates as Plan-phase.

### 1. Scope decision: Option C (deviation from chunk doc)

**Picked**: orchestrator inside `package supervise` at
`internal/supervise/lifecycle.go` + 4 siblings. Cli shim at
`internal/cli/supervise.go` (~80 LOC).

**Rejected — Option A** (orchestrator in cli): violates SDD-23
anti-contract ("cli/ free of business logic"); estimated LOC ~800
exceeds the chunk-doc 700-LOC threshold.

**Rejected — Option B literal** (new sub-package
`internal/supervise/lifecycle/`): cannot reach the package-private
`(*Refiller).attach` (SDD-21) or `(*Store).setTokenForTest` (SDD-19),
which are the documented post-construction wiring seams for the
orchestrator per SDD-21's PACKAGE-MAP comment ("wired post-construction
by the orchestrator via the package-private `(*Refiller).attach`").
Making them exported would mutate the SDD-19 / SDD-21 PACKAGE-MAP locks
verbatim ("three ctors + four methods + two sentinels + Evict" — adding
`Attach` makes it five methods, violating the lock string).

**Why Option C is constitutional**: it introduces NEW exported symbols
(`Lifecycle` / `Deps` / `NewLifecycle` / `Run` + 3 interfaces +
`AlertClass` enum + `AlertPayload` struct + sentinels) — these are
SDD-24's own locked surface, not a mutation of any SDD-19..22 lock.
PACKAGE-MAP gets an APPENDED SDD-24 section, no edits to existing
SDD-19..22 sections. Constitution VII intent (business logic out of
cli) is preserved; cli shim ≤ 100 LOC.

### 2. Goroutine inventory (Constitution IX)

Five long-running goroutines after `Run(ctx)` is called:

| # | Goroutine | Owner | Cancellation | Termination | Top-frame recover |
|---|-----------|-------|--------------|-------------|-------------------|
| 1 | `StatusServer.Run` (carried from SDD-22) | `Lifecycle` | `<-ctx.Done()` (root context) | returns when listener closes + every per-connection handler joins | ✅ existing |
| 2 | `Refresher.Run` (carried from SDD-21) | `Lifecycle` | `<-ctx.Done()` | returns on ctx cancel | ✅ existing |
| 3 | `childWaitLoop` (NEW) | `Lifecycle` | implicit via `Child.Wait` returning (Wait observes ctx cancel via `Child.Forward`) | sends one `exitCode` message per child instance; exits when the child instance is destroyed AND ctx is done | ✅ added |
| 4 | `claimRefreshLoop` (NEW) | `Lifecycle` | `<-ctx.Done()` | drains the `refreshTickCh` channel and exits | ✅ added |
| 5 | `mainLoop` (NEW) | `Lifecycle` | `<-ctx.Done()` | runs until ctx is cancelled, then performs the shutdown sequence | ✅ added |

The `refill` callback `NewRefresher` requires runs INLINE inside the
Refresher's tick body (not a separate goroutine) — that callback posts
on `refreshTickCh` and returns immediately, so `claimRefreshLoop`
does the actual signed-/claim work without blocking the Refresher.

All goroutines join via `Lifecycle.wg` before `Run` returns.

### 3. Boot retry schedule

| Knob | Value | Source |
|------|-------|--------|
| Initial backoff | 500 ms | Plan-phase decision (A-026-2: per-attempt probe ≤ 2s) |
| Multiplier | 2.0 | Standard exponential |
| Cap (per attempt) | 30 s | Plan-phase decision (well below `boot_retry_timeout`) |
| Per-attempt HTTP probe timeout | 2 s | spec A-026-2 (literal) |
| Total budget | `cfg.BootRetryTimeout` (default 10 m per `docs/CONFIG-SCHEMA.md`) | config |
| Jitter | ±20% on each interval | Plan-phase decision (avoids thundering herd on shared launchd cohorts) |

Tailscale check seam: orchestrator's `Deps` includes
`TailscaleProbe func(context.Context) error` with a default backed by
the existing `internal/server` interface lister used by AC-8. The probe
returns nil on success or a typed error; on error the boot loop sleeps
the next backoff interval and retries until `boot_retry_timeout` elapses.

Vault `/hz` check: orchestrator issues `GET <server>/hz` with a 2s
`context.WithTimeout`. 200 → ok; any other status or error → retry.

Exhaustion: emit `Alerts.Emit(ctx, AlertClassBootTimeout, …)`, append
`ActionSupervisorBootTimeout` audit event, return `ErrBootTimeout`
(existing SDD-21 sentinel — emitted now, not just declared) wrapped
in a `Lifecycle.Run`-shaped error mapped to the cli's `ExitErr`.

### 4. /claim submission

- Caller signs the canonical-JSON `/claim` body using
  `sign.CanonicalJSON` + `sign.Sign` (SDD-08; see
  `internal/transport/sign`).
- Signing-key handle (`*ecdsa.PrivateKey` BIP32-derived per FR-3) is
  carried in `Deps.ClaimSigningKey`. Wired by the cli layer from the
  client-machine keychain.
- HTTP request: `POST <server>/claim`, `Content-Type: application/json`,
  body = canonical JSON, `X-Signature` header = base64 signature.
- On 200: parse `{token, jti, scope, exp}` response, wrap the token
  bytes in `*securebytes.SecureBytes`, call package-private
  `Store.setToken(sb)`, append `ActionSupervisorSessionClaimed` audit
  event with `data.jti` only (never the token).
- On 503 with body `{"error":"discord_unavailable", ...}` (matches
  existing `internal/server/claim_handler.go` `errCodeDiscordUnavailable`):
  emit `AlertClassDiscordUnavailableOnClaim`, retry in boot loop until
  exhaustion.
- On any other 5xx / network error: retry in boot loop until exhaustion.
- On 401: terminal error, exit `ExitErr` (this is a programmer-config
  problem — the client key isn't registered or the IP is wrong;
  retrying won't help).
- On 4xx ≠ 401: same terminal behaviour.

### 5. Validator interface (defined at consumer per Constitution IX)

```go
// Validator validates a freshly-fetched (or Grace-resident) secret for
// one scope. Returns nil on success or a wrapped error naming the scope
// on failure. Implementations MUST NOT log the secret value.
// No-op default returns nil for every call.
type Validator interface {
    Validate(ctx context.Context, scope string, secret *securebytes.SecureBytes) error
}

// ErrValidatorFailed is the sentinel orchestrator emits (wrapped with
// the scope name in the message) on any Validator.Validate non-nil
// return. Compare via errors.Is.
var ErrValidatorFailed = errors.New("supervise: validator failed")
```

### 6. Alerts interface + AlertClass enum (LOCKED at 10 values per spec FR-026-016)

```go
type AlertClass int

const (
    AlertClassValidatorFailure AlertClass = iota + 1
    AlertClassExit78
    AlertClassVaultRejectedJWT
    AlertClassRefillFailed
    AlertClassDiscordUnavailableOnClaim
    AlertClassRefreshDenied
    AlertClassRefreshTimeout
    AlertClassGraceEntered
    AlertClassLogPatternMatch
    AlertClassBootTimeout
)

type AlertPayload struct {
    Scope      string // failed scope name (e.g. "ANTHROPIC_API_KEY") or "" when N/A
    ErrorClass string // coarse error class ("transient", "unknown_jti", "cancelled", …)
    Reason     string // human-readable phrase ("validator rejected fetched secret")
}

type Alerts interface {
    Emit(ctx context.Context, class AlertClass, payload AlertPayload)
}
```

No-op default discards every call. SDD-28 will land as the rendering
layer on top of this enum; it MUST NOT extend the enum without a spec
amendment (spec FR-026-016 anti-extension).

### 7. Watchdog interface

```go
// Watchdog observes child stderr lines. Hook is alert-only — MUST NOT
// influence state-machine transitions (Constitution V).
// No-op default discards.
type Watchdog interface {
    OnStderrLine(ctx context.Context, line []byte)
}
```

Wiring: orchestrator constructs an `io.Writer` wrapper that fans bytes
to both the operator-stderr sink (whatever the cli passed in) AND a
line-buffered observer. The observer emits each completed line to
`Watchdog.OnStderrLine`. The wrapper is passed as `ChildConfig.Stderr`.
SDD-20's `Child.drainLoop` still owns the read side; the watchdog sees
each line exactly once. No second drain (spec FR-026-030).

### 8. Audit vocabulary reconciliation

Append the following 12 constants to `internal/audit/chain.go`'s
`Action*` constants block (the block already has the
"future SDDs MAY append (never repurpose)" header — appending is in
contract). NO existing constant is renamed or removed.

| Constant | String value | Emission site |
|----------|--------------|---------------|
| `ActionSupervisorSessionClaimed` | `"supervisor_session_claimed"` | After successful initial `/claim` + JWT persist |
| `ActionSupervisorSessionRefreshed` | `"supervisor_session_refreshed"` | After successful refresh-window claim swap |
| `ActionSupervisorSilentRefill` | `"supervisor_silent_refill"` | After successful silent refill following clean exit / crash |
| `ActionSupervisorChildCleanExit` | `"supervisor_child_clean_exit"` | When `Child.Wait` returns exit code `0` |
| `ActionSupervisorChildExitCrash` | `"supervisor_child_exit_crash"` | When `Child.Wait` returns non-zero non-`78` |
| `ActionSupervisorChildExit78` | `"supervisor_child_exit_78"` | When `Child.Wait` returns exit code `78` |
| `ActionSupervisorAwaitingApproval` | `"supervisor_awaiting_approval"` | When the orchestrator enters `awaiting-approval` for ANY reason; `Data.cause` carries `validator` / `unknown_jti` / `exit_78` / `boot_timeout` |
| `ActionSupervisorStaleAlert` | `"supervisor_stale_alert"` | When the orchestrator fires any `[STALE] …` alert; `Data.class` carries the AlertClass name, `Data.scope` the failed scope name |
| `ActionSupervisorGraceEntered` | `"supervisor_grace_entered"` | When a grace-window restart begins |
| `ActionSupervisorGraceExited` | `"supervisor_grace_exited"` | When a grace-window restart ends |
| `ActionSupervisorBootTimeout` | `"supervisor_boot_timeout"` | When `boot_retry_timeout` exhausts |
| `ActionClientRefreshInvoked` | `"client_refresh_invoked"` | When the status-socket `refresh\n` verb is consumed |

**Reused** (no addition needed — already in chain.go):
`ActionSecretRetrieved`, `ActionDiscordDisconnected`,
`ActionDiscordReconnected`.

**SPEC.md §FR-14 amendment** (Plan-phase ADR-1): `docs/SPEC.md`'s
FR-14 audit-event list currently enumerates 10 supervisor-scope names
but is missing `supervisor_child_exit_crash` and `supervisor_boot_timeout`.
Implement-phase MUST add those two names to the §FR-14 list so the
documented vocabulary matches the orchestrator's emission set 1:1
(spec FR-026-014; SC-026-008).

### 9. Shutdown timeout

| Knob | Value | Source |
|------|-------|--------|
| `shutdown_grace_timeout` | 10 s | Plan-phase decision (A-026-3: ≤ SDD-22 socket-shutdown ceiling, which is sub-second; 10s leaves headroom for the child SIGTERM-honour grace) |
| `shutdown_hard_ceiling` | 15 s | Plan-phase decision (cli rootCtx-cancel → `Child.Forward(SIGTERM)` → wait 10s → `Child.Forward(SIGKILL)` → wait 5s → `wg.Wait()` → pidfile.Release) |
| Pidfile release | on `defer` from the cli shim | mirrors existing `internal/cli/supervise.go` lines 312-314 |

### 10. Status-socket refresh-verb dispatch (per spec Clarification 4)

| Orchestrator state at refresh-verb arrival | Behaviour |
|---|---|
| `boot-retry` (no JWT yet) | reject with `{"ok":false,"error":"boot-retry"}\n` ack; no state mutation |
| `fetching` (claim submitted, awaiting approval) | reject with `{"ok":false,"error":"fetching"}\n` ack; no state mutation |
| `awaiting-approval` | drive the full refill+validate+restart path; emit `ActionClientRefreshInvoked` |
| `running` (valid session) | coalesce with any in-flight refill via the existing `refreshCoalescer` (FR-023-22a); emit `ActionClientRefreshInvoked` once per coalesced cohort |
| `grace-restart` | coalesce same as `running` |
| `stopped` | reject with `{"ok":false,"error":"stopped"}\n` ack |

### 11. Tests Required (Plan-phase pinned list)

Unit (`internal/supervise/lifecycle_test.go` — Constitution VIII TDD):

| # | Test name | Branch |
|---|-----------|--------|
| 1 | `TestLifecycle_BootSubmitsClaim` | happy path |
| 2 | `TestLifecycle_BootRetrySucceedsAfterNFailures` | boot-retry: N failures then success |
| 3 | `TestLifecycle_BootRetryTimeoutExhausted` | boot-retry: timeout |
| 4 | `TestLifecycle_ClaimDeniedTransitionsToAwaitingApproval` | 401 (or 200-with-Deny outcome) |
| 5 | `TestLifecycle_ClaimDiscordUnavailableEmitsAlert` | 503 + `discord_unavailable` |
| 6 | `TestLifecycle_RefillJTIUnknownTransitionsToAwaitingApproval` | `ErrJTIUnknown` |
| 7 | `TestLifecycle_RefillTransientErrorPostRunningEmitsRefillFailed` | spec FR-026-010a post-running path |
| 8 | `TestLifecycle_ValidatorFailureBlocksChildStart` | validator path |
| 9 | `TestLifecycle_ChildExitZeroTriggersSilentRefill` | exit code 0 |
| 10 | `TestLifecycle_ChildExitNonZeroTriggersSilentRefill` | exit code != 0 && != 78 |
| 11 | `TestLifecycle_ChildExit78EmitsStaleAlertNoRestart` | exit code 78 |
| 12 | `TestLifecycle_RefresherTickSubmitsFreshClaim` | refresh window happy path |
| 13 | `TestLifecycle_RefresherTickKeepsChildRunning` | child PID unchanged across swap |
| 14 | `TestLifecycle_RefreshDeniedEmitsAlert` | refresh denial |
| 15 | `TestLifecycle_GraceRestartUsesCachedSecrets` | grace-cache path |
| 16 | `TestLifecycle_SigtermDrainsChild` | shutdown clean |
| 17 | `TestLifecycle_StatusSocketRefreshInBootRetryRejects` | refresh during boot-retry |
| 18 | `TestLifecycle_NoBusinessLogicLeakage` | grepper: no state literals, no raw 78, no runtime.GOOS |
| 19 | `TestLifecycle_NoSentinelLeakage` | sentinel-leak across 5 byte streams |

Integration (`internal/supervise/lifecycle_integration_test.go`,
`//go:build integration` — Constitution VIII):

| # | Test name |
|---|-----------|
| I1 | `TestLifecycle_Integration_FullBootRoundTrip` (httptest.Server + stubApprover + child binary that prints env and exits) |
| I2 | `TestLifecycle_Integration_SilentRefillOnChildExit` |
| I3 | `TestLifecycle_Integration_GracefulShutdownReleasesPidfile` |

### 12. Test seams (Constitution IX-friendly)

- `Deps.HTTPClient` — orchestrator's outgoing http.Client; tests
  pass a client whose `Transport` is rewired to `httptest.Server`
  per the existing `claim_handler_integration_test.go` pattern.
- `Deps.Clock` — `supervise.Clock` interface (already exists in SDD-19).
  Tests inject a controllable fake; the Refresher already supports
  `setTickerForTest` and `setClockForTest` for its own tick loop.
- `Deps.TailscaleProbe func(context.Context) error` — tests inject a
  controllable stub.
- `Deps.Validators`, `Deps.Alerts`, `Deps.Watchdog` — the three
  injected interfaces with controllable fakes.
- `Deps.AuditWriter` — `*audit.Writer` injection; tests use the
  existing in-memory test writer.

NO new package-private seams are added inside `internal/supervise/`
beyond renaming `setTokenForTest` → `setToken` (which becomes a
production-path internal seam; tests can still call it directly because
they live in the same package).

### 13. Secret-bytes audit (Constitution X)

Sites where the orchestrator touches secret bytes:

| Site | Bytes form | Lifetime |
|------|-----------|----------|
| `/claim` response JWT bytes | `[]byte` in HTTP body buffer → wrapped in `*SecureBytes` → stored via `Store.setToken` | response body zeroed before return |
| `Refiller.Refill` decrypted values | `*SecureBytes` end-to-end (SDD-21 guarantees) | held in `Grace` until evict or expiry |
| Child env construction | `Grace.Get` returns `*SecureBytes`; `Use(func(b []byte) { env = append(env, "KEY="+string(b)) })` builds the env slice | child env slice is a `[]string` — this IS a `string(secretBytes)` site at the documented child-fork boundary; mirrors SDD-20 `ChildConfig.Env`. Per spec FR-026-028, the only `string(*SecureBytes)` outside the Refiller JWT site is the child env construction site at fork time. The env slice is reset via slice-reuse + zeroing helper after `Child.Start` returns. |
| JWT bearer header (during refill) | inside `Snapshot.Token.Use` — SDD-21 permitted | scoped to one HTTP request |

The "child env construction site" is the only new `string(...)` site
introduced by SDD-24. It is at the OS fork boundary, mirrors the
existing SDD-20 contract (`ChildConfig.Env` is `[]string`), and exists
in operator memory for the duration of exactly one `Child.Start` call.
This is the documented Constitution-IV+X tradeoff (FR-026-008 + the
SDD-20 contract).

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| **Deviation from SDD-24 chunk-doc Option B** (sub-package `internal/supervise/lifecycle/`) → **Option C** (orchestrator inside `package supervise` itself) | Option B literal CANNOT reach the package-private `(*Refiller).attach` (SDD-21) or `(*Store).setTokenForTest` (SDD-19) — these are the documented post-construction wiring seams the SDD-21 PACKAGE-MAP comment names. Without them, the orchestrator cannot inject Grace / private key / server URL into Refiller, nor write the JWT into Store. | Option B + new exported `(*Refiller).Attach` + `(*Store).SetToken` REJECTED because: it mutates the SDD-21 PACKAGE-MAP lock string "three constructors + four methods + two sentinels + Evict" (Attach would make it five methods) AND adds a new method to SDD-19's Store (also locked). Both PACKAGE-MAP locks are verbatim per `docs/PACKAGE-MAP.md` lines 1628-1714 and the SDD-19 section. Option C achieves Constitution-VII intent (business logic out of cli; cli shim ≤ 100 LOC) and adds ONLY new exported SDD-24 symbols — no mutation of any SDD-19..22 lock. |
| **One new `string(*SecureBytes)` site at child-env build time** | The OS `execve` call requires `[]string` for env. SDD-20 `ChildConfig.Env` is `[]string`. The orchestrator must convert `Grace`-resident `*SecureBytes` → `string` exactly once per scope at fork time. | Alternatives (e.g., pipe secret over a Unix socket post-exec) require child-side cooperation and would break the "child knows nothing about hush" contract of Principle IV. The string materialization is scoped to one `Child.Start` call; the env slice is zeroed after Start returns. This is the documented Constitution-IV+X tradeoff. |

These two are the only Constitutional checks that needed explicit
justification. Everything else aligns by construction (see the
Constitution Check table above).

## Phase 0 — Outline & Research

**Output**: [research.md](research.md) (generated by this command).

Phase-0 resolves every Plan-phase decision the chunk doc and spec
flagged as Plan-phase, then ratifies them against:

- the SDD-19..22 PACKAGE-MAP lock strings
- the supervise sub-package source files
- the audit-event vocabulary in `internal/audit/chain.go`
- the SDD-24 chunk-doc Behaviour Contracts table

No `NEEDS CLARIFICATION` markers remain after Phase 0; spec's
Clarifications section was resolved during /speckit-clarify (Session
2026-05-12).

## Phase 1 — Design & Contracts

**Outputs**: [data-model.md](data-model.md), [contracts/](contracts/),
[quickstart.md](quickstart.md), [CLAUDE.md update](../../CLAUDE.md).

Phase-1 freezes the interface contracts (Validator / Alerts / Watchdog),
the `Lifecycle` / `Deps` data model, and the audit-event vocabulary.
The Quickstart documents the developer workflow: how to wire a
controllable test triple, how to run the unit + integration suites,
and where the SDD-25 harness will reattach once this chunk lands.

## Constitution Re-Check (Post-Phase-1)

Re-evaluated after Phase-1 contract authoring:

- ✅ All five orchestrator goroutines (Phase-1 §Goroutine Inventory)
  carry owner + ctx + termination + top-frame recover (Principle IX).
- ✅ The three injected interfaces (Phase-1 contracts/interfaces.go.md)
  are defined at the consumer (`package supervise`), not imported from
  any producer package (Principle IX).
- ✅ AlertPayload (Phase-1 data-model.md) carries 3 string fields only —
  scope name + error class + reason; never the secret value (Principle X).
- ✅ Audit-event vocabulary (Phase-1 contracts/audit-vocabulary.md)
  matches `internal/audit/chain.go` 1:1 after the 12 additions; spec
  FR-026-014 + SC-026-008 satisfied.
- ✅ Tests-Required pinned at 19 unit + 3 integration cases (Plan §11);
  Constitution VIII gate is mechanical (line coverage ≥ 85%).
- ✅ Sentinel-leak (`TestLifecycle_NoSentinelLeakage`) sweeps 5 byte
  streams: operational slog, audit JSONL, alert payloads, status-socket
  JSON, error messages. Principle X verified by test.

Constitution Check **PASSES** post-Phase-1. No additional violations.
The two violations in Complexity Tracking remain the only Plan-phase
deviations; both have written justification.
