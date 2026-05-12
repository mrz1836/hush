# Feature Specification: Supervisor Orchestrator (SDD-24)

**Feature Branch**: `026-supervisor-orchestration`
**Created**: 2026-05-12
**Status**: Draft
**Input**: User description: "internal/cli supervisor orchestrator: at boot submits signed /claim, runs initial Refiller.Refill, runs injected validators, builds child env from Grace and starts Child via NewChild, runs Child.Wait, dispatches on exit codes 0/non-zero-non-78/78, handles Refiller.ErrJTIUnknown by transitioning to awaiting-approval, advances JWT on Refresher window ticks, fires alerts via injected interface at every documented alert site, retries-with-backoff against Tailscale+vault reachability up to boot_retry_timeout, shuts down cleanly on SIGTERM; injected Validator/Alerts/Watchdog interfaces with no-op defaults so SDD-26/27/28 can fill them later; reconciles audit-event vocabulary with internal/audit/chain.go before emitting new names"

## Overview

SDD-24 ships the production orchestrator that drives the documented supervisor daemon lifecycle end-to-end against the locked SDD-19..22 primitives (Store, Child, Refiller, Refresher, Grace, StatusServer, PidFile). The orchestrator submits the initial signed `/claim`, calls `Refiller.Refill` at boot, runs the configured validators (via injected interface), builds the child env from `Grace`, starts the child via `NewChild`, runs `Child.Wait` on a supervisor goroutine, dispatches on the child's exit code (`0` / non-zero non-`78` / `78`), handles `Refiller.ErrJTIUnknown` by transitioning to `awaiting-approval`, fires alerts at every documented alert site through an injected interface, advances the cached JWT on each `Refresher` window tick, and exits cleanly on `SIGTERM`/`SIGINT`. It is the missing glue layer surfaced by SDD-25's harness phase: the locked primitives exist, the CLI skeleton exists, but no code anywhere composes them into the lifecycle.

This chunk delivers ONLY the orchestrator. It does NOT pre-define validators (SDD-26), the watchdog (SDD-27), or alert classes (SDD-28); instead, the orchestrator MUST host three small, injectable interfaces — `Validator`, `Watchdog`, `Alerts` — with no-op defaults so those later chunks can fill them without API churn. It MUST reconcile the audit-event vocabulary documented for the supervisor scope with the constants block in `internal/audit/chain.go` BEFORE emitting any new action name: every emitted name MUST be a declared constant.

The orchestrator is the AC-10 precondition. Until it ships, twelve of SDD-25's fifteen integration scenarios cannot reach their documented final states.

## Clarifications

### Session 2026-05-12

- Q: How should the orchestrator dispatch on `Refiller.Refill` errors OTHER than `supervise.ErrJTIUnknown` (e.g., transient network failure, vault 5xx, ECIES decrypt failure, unexpected envelope)? → A: At boot, route the error back into the same boot-retry-with-backoff loop bounded by `boot_retry_timeout`; for a silent-refill after child exit, transition to `awaiting-approval` and emit a generic `[STALE] Refill Failed` alert. Boot-time transient network blips stay inside the documented `boot_retry_timeout` budget (Scenario 11 spirit); post-running refill failures are loud/visible per Constitution V — never silent, never auto-retried forever.
- Q: How should the `AlertClass` enum reconcile FR-026-013's 9 orchestrator emission sites with A-026-7's 8 LIFECYCLE-SCENARIOS classes? → A: Lock 10 enum values, one per orchestrator emission site: `ValidatorFailure`, `Exit78`, `VaultRejectedJWT`, `RefillFailed`, `DiscordUnavailableOnClaim`, `RefreshDenied`, `RefreshTimeout`, `GraceEntered`, `LogPatternMatch`, `BootTimeout`. Server-side LIFECYCLE classes (`approval request`, `daemon refresh request`, `Discord disconnected`, `Discord reconnected`) are NOT in this enum — they belong to other channels. SDD-28 lands as a pure rendering layer on top of this enum.
- Q: How is `Watchdog.OnStderrLine` wired without opening a second drain on `Child.Stderr` AND without mutating SDD-20's locked surface? → A: The orchestrator constructs a line-splitting `io.Writer` wrapper (tee + line-buffered adapter) that fans every line to both the operator stderr sink AND `Watchdog.OnStderrLine`, then passes that wrapper into `ChildConfig.Stderr`. SDD-20's single drain goroutine still owns the read side; the watchdog hook lives in the orchestrator's line-splitter. No SDD-20 API change.
- Q: What should the status-socket `refresh` verb do when the orchestrator is in `boot-retry` (no claim submitted yet) or `fetching` (claim submitted, awaiting approval, secrets not yet fetched)? → A: Reject with an explicit `{ok:false, error:"<state>"}` ack carrying the current state name; take no other action. Honoured-state behaviour stays as already specified — `awaiting-approval` drives the full refill+validate+restart path, `running` coalesces with any in-flight refill. The natural boot/claim path already converges on the same outcome; explicit rejection makes the pre-running state visible to the operator instead of silently swallowing the verb.
- Q: How does the orchestrator detect "Discord unavailable on `/claim`" distinctly from a generic vault-unreachable failure during the boot-claim attempt? → A: Parse the 503 response body and switch on the `error` field — `"discord_unavailable"` (the existing `errCodeDiscordUnavailable` value emitted by `internal/server/claim_handler.go`) triggers the `DiscordUnavailableOnClaim` alert and a retry policy bounded by `boot_retry_timeout` that waits for bot reconnect. Any other 5xx or network error falls into the generic boot-retry path. The orchestrator MUST NOT conflate other 503 codes (token-issuer / token-store / unknown-outcome) with the Discord-unavailable path.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - First daemon bootstrap reaches `running` on one approval (Priority: P1)

The operator runs `hush supervise <config>` (typically under `launchd`/`systemd`). The supervisor performs ordered startup checks, retries against Tailscale + vault reachability with bounded exponential backoff up to `boot_retry_timeout`, then submits a single signed `/claim` to the vault server with `session_type=supervisor`. A `[DAEMON]` Discord DM lands on the configured approver's phone exactly once. On approval, the supervisor receives a JWT, persists it via `Store`, calls `Refiller.Refill` to fetch every configured scope, hands the secrets through each configured validator, builds the child environment, starts the child via `Child.Start`, transitions to `running`, and waits on `Child.Wait`. The child runs with secrets injected as env vars and never learns anything about the supervisor or vault.

**Why this priority**: Without this, the daemon never starts at all. Twelve of the fifteen documented lifecycle scenarios (Scenarios 2, 3, 4, 5, 6, 7, 8, 9a, 9b, 10/supervisor, 11, 12, 13, 15) are blocked. AC-10 cannot pass. SDD-25's harness has no production code to integrate.

**Independent Test**: With a real (or `httptest`) vault server, a stubbed approver that approves once, and a child binary that prints its environment and exits, the supervisor:
- emits exactly one `/claim` request,
- prompts the stub exactly once,
- transitions through `fetching → running`,
- starts the child with the requested scopes as env vars (and no extra env beyond `env_passthrough`),
- emits the audit subsequence `supervisor_session_claimed → secret_retrieved (× len(scope)) → supervisor_running` (or the final reconciled vocabulary; see FR-026-014),
- exposes the `running` state on the status socket within a bounded number of yields.

**Acceptance Scenarios**:

1. **Given** Tailscale and the vault `/hz` are reachable at boot, **When** the supervisor starts, **Then** it submits exactly one signed `/claim`, persists the returned JWT, calls `Refiller.Refill` once for the full scope, runs every configured validator once per scope, and starts the child exactly once.
2. **Given** Tailscale is not yet ready at boot, **When** the supervisor starts and the `[network]` precondition recovers before `boot_retry_timeout` elapses, **Then** the supervisor proceeds normally without emitting a Discord prompt during the unreachable window.
3. **Given** the boot precondition never recovers within `boot_retry_timeout`, **When** the timer elapses, **Then** the supervisor exits with a non-zero exit code (mapped to `ExitErr`), the failure is operator-visible on stderr, and no Discord prompt is emitted.
4. **Given** a validator rejects a fetched secret, **When** validation completes, **Then** the child is NOT started, the orchestrator transitions to `awaiting-approval`, a `[STALE] Validator Failure` alert is emitted naming the failed scope (never the secret value), and the corresponding audit action is appended.

---

### User Story 2 - Crash/clean exit silently refills (Priority: P2)

The child exits — cleanly or by crashing — while the cached supervisor session is still valid. The supervisor re-fetches secrets via `Refiller.Refill` using the cached JWT, re-runs validators, and restarts the child. No Discord prompt fires. No phone buzz. The operator is never paged for a 3am OOM crash within an approved session.

**Why this priority**: This is the entire reason `hush supervise` exists rather than `hush request --exec` for daemons. If the orchestrator cannot silently refill, every restart re-prompts and the operator gets retrained to auto-approve — which defeats Constitution Principle II.

**Independent Test**: With an approved session and a child that exits with code `0` (or a non-zero non-`78` code) immediately after start, the supervisor:
- detects the exit,
- emits the `supervisor_child_clean_exit` (or crash) audit event,
- calls `Refiller.Refill` again without prompting the approver,
- restarts the child,
- emits the `supervisor_silent_refill` audit event,
- never emits an alert (this is normal operation),
- the child's restart count is observable on the status socket.

**Acceptance Scenarios**:

1. **Given** a `running` supervisor with a valid session, **When** the child exits with code `0`, **Then** the supervisor performs a silent refill and restarts the child without emitting any Discord traffic and without changing supervisor state to `awaiting-approval`.
2. **Given** a `running` supervisor with a valid session, **When** the child exits with any non-zero exit code OTHER than `78` (e.g., `1`, `137`, signal-induced), **Then** the supervisor performs the same silent refill + restart path as for exit `0`.
3. **Given** a `running` supervisor whose cached JWT was just invalidated by a vault restart, **When** `Refiller.Refill` returns `ErrJTIUnknown` during the silent-refill attempt, **Then** the supervisor transitions to `awaiting-approval`, emits a `[STALE] Vault Rejected JWT` alert (or the SDD-28-final equivalent), keeps the child stopped, and waits for a fresh approval or a `client refresh` command.

---

### User Story 3 - Stale credentials surface loudly (Priority: P2)

A child exits with code `78` (the `EX_CONFIG` stale-credential contract from Constitution V) — OR a validator catches a bad secret before child start — OR the vault server rejects the cached JWT with `unknown jti` during a refill. In every case, the orchestrator transitions to `awaiting-approval`, emits a distinctly-classed `[STALE] …` alert, and refuses to restart the child until the operator either re-approves a fresh session via Discord or invokes `hush client refresh` after rotating the offending secret.

**Why this priority**: Constitution Principle V — "Staleness is visible, failure is loud." Silent stale-credential failures are unacceptable. The Mini-Zai 2026-04-04 incident (114 MB of logs in hours from a stale token) is the canonical failure mode hush is designed against.

**Independent Test**: For each of the three stale paths (child exit 78, validator failure, `ErrJTIUnknown`), inject the failure into a `running` supervisor and assert:
- the orchestrator transitions to `awaiting-approval`,
- the alert interface receives exactly one `Emit` call with a class distinct from the other two stale paths,
- the alert payload carries the offending scope name (never the secret value, never the JWT bytes),
- the orchestrator does NOT restart the child until a fresh approval or `client refresh` arrives,
- the corresponding audit action (`supervisor_child_exit_78`, `supervisor_stale_alert` with reason=`validator`, or `supervisor_stale_alert` with reason=`unknown_jti`) is appended to the chain.

**Acceptance Scenarios**:

1. **Given** a `running` supervisor, **When** the child exits with code `78`, **Then** the supervisor transitions to `awaiting-approval`, emits a `[STALE] Child Exit 78` alert, and does NOT silently refill (regardless of remaining session TTL).
2. **Given** a `running` supervisor performing a silent refill, **When** `Refiller.Refill` returns `ErrJTIUnknown`, **Then** the supervisor transitions to `awaiting-approval` and emits a `[STALE] Vault Rejected JWT` alert.
3. **Given** an `awaiting-approval` supervisor after any stale path, **When** the operator invokes `hush client refresh` (status-socket `refresh` verb), **Then** the supervisor re-submits a signed claim — OR re-uses an existing valid session if one was just granted — re-fetches secrets, re-runs validators, restarts the child, and transitions back to `running`.

---

### User Story 4 - Refresh window advances the session without restarting the child (Priority: P3)

When the configured `refresh_window` arrives (default `09:00–10:00` local), the supervisor's refresher fires its tick callback. The orchestrator submits a fresh signed `/claim` (which produces a fresh Discord `[DAEMON] Refresh` prompt), receives a fresh JWT on approval, and swaps the cached JWT atomically in the `Store` — without restarting the child. The child continues running uninterrupted on its already-injected env vars; only the supervisor's refill capability is renewed.

**Why this priority**: Without this, the supervisor cannot survive a 24-hour deployment. But unlike Stories 1–3, this only fails after >TTL elapses; it does not block initial bring-up.

**Independent Test**: With a `running` supervisor whose `Refresher` is driven by a controllable clock seam, fire the tick callback once and assert:
- the orchestrator submits a fresh signed claim,
- the new JWT is observable in `Store.Snapshot()`,
- the child PID is unchanged (no restart),
- the audit chain records `supervisor_session_refreshed`.

**Acceptance Scenarios**:

1. **Given** a `running` supervisor with a valid session inside the refresh window, **When** the refresher callback fires, **Then** the orchestrator submits a fresh signed `/claim`, swaps the cached JWT in `Store` on approval, and the child PID is unchanged after the swap.
2. **Given** a refresh attempt that the approver denies (or that the configured approval timeout exhausts), **When** the refresher callback returns, **Then** the orchestrator emits the appropriate alert class (refresh-denied or refresh-timeout), keeps the existing session active until its natural expiry, and remains in `running` until the next refresh window or stale signal.

---

### User Story 5 - Clean shutdown on SIGTERM/SIGINT releases all resources (Priority: P3)

When the supervisor receives `SIGTERM` or `SIGINT` (typically from the OS service manager), the orchestrator cancels its root context, forwards `SIGTERM` to the child via `Child.Forward`, waits for the child to exit and every spawned goroutine to join, releases the pidfile, and exits `0`. No resources leak. No partial state survives.

**Why this priority**: This is what makes `launchctl unload` / `systemctl stop` clean. Without it, the daemon can leave orphan children, dangling sockets, or stale pidfiles that break the next start (Scenario 14 — duplicate supervisor start attempt).

**Independent Test**: Send `SIGTERM` to a `running` supervisor and assert:
- the child receives `SIGTERM` and exits,
- the supervisor's pidfile is released (a fresh `hush supervise` for the same config can acquire the pidfile on retry),
- the status socket file is removed,
- the supervisor process itself exits `0` within a bounded interval (configured shutdown timeout).

**Acceptance Scenarios**:

1. **Given** a `running` supervisor with a live child, **When** the supervisor receives `SIGTERM`, **Then** the supervisor forwards `SIGTERM` to the child's process group, waits for the child to exit, joins every spawned goroutine, releases the pidfile, and exits `0`.
2. **Given** the supervisor is mid-boot (boot-retry loop running, no child yet), **When** `SIGTERM` arrives, **Then** the supervisor cancels its root context, releases the pidfile, and exits without ever submitting a `/claim` or contacting Discord.

---

### Edge Cases

- **Discord unavailable during initial claim** (Scenario 10/supervisor): the vault server returns 503 with body `{"error": "discord_unavailable", ...}` (the existing `errCodeDiscordUnavailable` envelope emitted by `internal/server/claim_handler.go`) when the bot WebSocket is disconnected. The orchestrator MUST parse the 503 body's `error` field and switch on `"discord_unavailable"` distinctly from any other 5xx or network failure: emit the `DiscordUnavailableOnClaim` alert and retry on the bot's reconnect within `boot_retry_timeout`, OR exit with `ExitErr` on exhaustion. Other 5xx codes (`token_issuer_error`, `token_store_error`, `unknown_outcome`, etc.) fall into the generic boot-retry path and MUST NOT be conflated with the Discord-unavailable path. Fail closed; never auto-approve.
- **Duplicate supervisor start** (Scenario 14): when the pidfile is already held by another live supervisor, the orchestrator MUST refuse to proceed, surface the locked pidfile path on stderr, and exit non-zero — BEFORE submitting any `/claim`, opening any socket, or contacting Discord.
- **Overnight expiry with strict mode** (Scenario 9 strict): when the session expires overnight and the child later crashes before the refresh window opens, the orchestrator MUST refuse to silently refill, transition to `awaiting-approval`, and wait for the morning prompt; the child stays down.
- **Overnight expiry with grace cache** (Scenario 9 grace): when `cache_secrets_for_restart=true` AND the cached `Grace` window has not elapsed, a crash-restart uses the cached `*SecureBytes` rather than calling `Refiller.Refill`. The supervisor emits a warning-tier `supervisor_grace_entered` event but does NOT page the operator at 3am.
- **`--no-cache` runtime override**: when the operator passes `--no-cache`, the orchestrator MUST force `cache_secrets_for_restart=false` regardless of the TOML setting and behave per the strict-mode path above.
- **Status-socket `refresh` verb during `awaiting-approval`**: when `hush client refresh` arrives while the orchestrator is in `awaiting-approval`, the orchestrator MUST drive the same refill+validate+restart path as the post-approval recovery sequence (Scenario 7 final step). When the orchestrator is in `running` and the session is still valid, `refresh` MUST coalesce with any in-flight refill to avoid a thundering herd (this constraint is already met by `internal/cli/supervise.go`'s existing `refreshCoalescer`; the new orchestrator MUST NOT regress it).
- **Status-socket `refresh` verb during pre-running states** (`boot-retry`, `fetching`): the orchestrator MUST reject the verb with an explicit `{ok:false, error:"<state>"}` ack carrying the current state name and MUST NOT mutate boot-retry timers, in-flight claim state, or `Store` state. The natural boot path is already converging on the same outcome; explicit rejection keeps the pre-running state operator-visible instead of silently swallowing the verb.
- **Validator runs but the secret is in `Grace` from a prior fetch**: validation runs on every freshly-fetched secret AND on every secret pulled from `Grace` during a grace-window restart — never skipped.
- **Slow child stdout/stderr drain**: the orchestrator MUST NOT itself drain `Child.Stdout`/`Stderr`; the SDD-20 `Child.drainLoop` already owns that. If the watchdog interface is wired, the orchestrator forwards lines via a single observer hook on the existing drain — it does not open a second drain.
- **Goroutine leak on shutdown**: every spawned goroutine MUST join via the orchestrator's WaitGroup before `Run` returns; a `runtime.NumGoroutine` pre/post snapshot at scenario teardown MUST show no growth (an SDD-25 harness assertion).
- **Audit chain continuity on shutdown**: when the orchestrator emits its final audit event before `Release()`, the chain MUST remain `audit.Verify`-clean (no torn last record).

## Requirements *(mandatory)*

### Functional Requirements

**Boot sequence**

- **FR-026-001**: The orchestrator MUST acquire the pidfile (via `AcquirePidFile`) BEFORE constructing any goroutine-owning primitive, BEFORE opening the status socket, and BEFORE contacting the vault server, Discord, or the network. Duplicate-start MUST surface as `ErrPidLocked` mapped to a non-zero exit code.
- **FR-026-002**: After acquiring the pidfile, the orchestrator MUST construct the `Store`, `Grace`, `StatusServer`, `Refiller`, and `Refresher` in dependency order, attach `StatusInputs` and the refresh handler to the `StatusServer`, and spawn the `StatusServer` and `Refresher` goroutines BEFORE entering the boot-retry loop.
- **FR-026-003**: The orchestrator MUST verify Tailscale interface presence (via the existing seam consumed by `internal/server`) AND vault `/hz` reachability via a bounded HTTP probe BEFORE submitting the initial `/claim`. Failures cycle through an exponential-backoff retry loop bounded by `boot_retry_timeout`. The exact backoff schedule is plan-phase (see Assumptions).
- **FR-026-004**: When the boot-retry loop exhausts `boot_retry_timeout`, the orchestrator MUST exit with a non-zero exit code (mapped to `ExitErr`), surface the failure on stderr, and emit the documented boot-timeout audit event — without ever submitting a `/claim` or contacting Discord.
- **FR-026-005**: On boot precondition recovery (Tailscale up AND vault `/hz` reachable), the orchestrator MUST submit exactly one signed `/claim` using the SDD-08 canonical-signing helper, persist the returned JWT into `Store`, and proceed to the initial `Refiller.Refill`.

**Refill / validation / child start**

- **FR-026-006**: After the JWT is persisted, the orchestrator MUST call `Refiller.Refill(ctx, scopes)` exactly once for the full configured scope. On success, every fetched secret is in `Grace`. On error, the orchestrator MUST follow the error-dispatch rules in FR-026-010.
- **FR-026-007**: Before child start, the orchestrator MUST run the injected `Validator` once per scope, against the `*SecureBytes` value retrieved from `Grace.Get`. The default `Validator` is a no-op so SDD-26 can supply the real implementation later without API churn. Validator failure (any non-nil return) blocks child start, transitions the orchestrator to `awaiting-approval`, and emits the documented validator-failure alert with the failed scope name as payload — never the secret value.
- **FR-026-008**: After every validator returns nil, the orchestrator MUST build the `ChildConfig.Env` from `Grace`-resident secrets, call `NewChild(cfg)` + `Start(ctx)`, transition to `running`, and start the `Child.Wait` loop on a single dedicated goroutine. Secret `*SecureBytes` handles MUST be destroyed immediately after env-block construction unless `cache_secrets_for_restart=true` AND the grace TTL has not elapsed (Constitution IV/X).

**Child-exit dispatch**

- **FR-026-009**: When `Child.Wait` returns, the orchestrator MUST dispatch on the exit code as follows:
  - exit code `0` → emit `supervisor_child_clean_exit` audit event → silent refill (`Refiller.Refill` → validators → `Child.Start`) → return to `running`. No Discord prompt. No alert.
  - exit code non-zero AND not equal to `78` → emit the crash audit event → same silent-refill path as `0`. No Discord prompt. No alert.
  - exit code `78` (the `Exit78` constant from `internal/supervise`) → emit `supervisor_child_exit_78` audit event → emit `[STALE] Child Exit 78` alert → transition to `awaiting-approval` → DO NOT restart the child until a fresh approval or `client refresh` arrives.
- **FR-026-010**: When `Refiller.Refill` returns `supervise.ErrJTIUnknown` from any silent-refill attempt OR from the post-refresh refill, the orchestrator MUST transition to `awaiting-approval`, emit the documented `[STALE] Vault Rejected JWT` alert, and refuse further refills until a fresh approval or `client refresh` re-issues the session.
- **FR-026-010a**: When `Refiller.Refill` returns any error OTHER than `supervise.ErrJTIUnknown` (e.g., transient network failure, vault 5xx, ECIES decrypt failure, unexpected response envelope), the orchestrator MUST dispatch by call-site:
  - **At boot** (the initial Refill following the first `/claim`): route the error back into the same boot-retry-with-backoff loop bounded by `boot_retry_timeout`. Exhaustion follows FR-026-004 (exit non-zero + boot-timeout audit + no Discord prompt).
  - **Post-running** (any silent-refill triggered by child exit `0` / non-zero non-`78` / refresh-window swap): transition to `awaiting-approval`, emit a generic `[STALE] Refill Failed` alert carrying the error class string (never the secret value, never the JWT bytes), keep the child stopped, and wait for a fresh approval or `client refresh`. Do NOT auto-retry the failed refill.

**Refresh-window tick**

- **FR-026-011**: When the `Refresher` window-tick callback fires, the orchestrator MUST submit a fresh signed `/claim`, swap the cached JWT atomically inside the `Store` on approval, and leave the running child untouched. The child PID MUST be unchanged across the swap.
- **FR-026-012**: When a refresh attempt is denied (`Approver` returns Deny) OR exhausts its approval timeout, the orchestrator MUST keep the existing session in `Store`, emit the matching alert class (refresh-denied / refresh-timeout), and remain in `running` until the existing session expires or another stale signal fires.

**Alerts / watchdog (host-only — implementations land in SDD-27/28)**

- **FR-026-013**: The orchestrator MUST expose an `Alerts` interface field on its dependency struct with a no-op default. The orchestrator MUST call `Alerts.Emit` at EVERY alert site documented in `docs/LIFECYCLE-SCENARIOS.md` and `docs/DAEMONS.md`: validator-failure, exit-78, vault-rejected-JWT, refill-failed-post-running (per FR-026-010a), Discord-unavailable-on-claim, refresh-denied, refresh-timeout, grace-entered, log-pattern-watchdog-match. The exact `AlertClass` enum is plan-phase, but the set of sites is locked here. SDD-28 supplies the rendering implementation.
- **FR-026-013a**: The orchestrator MUST expose a `Watchdog` interface field on its dependency struct with a no-op default. When configured, the orchestrator MUST forward observed child-stderr lines to the watchdog WITHOUT opening a second drain on top of `Child.drainLoop` AND without mutating SDD-20's locked `Child`/`ChildConfig` surface. The orchestrator satisfies this by constructing a line-splitting `io.Writer` wrapper (tee + line-buffered adapter) that fans each emitted line to both the operator stderr sink AND `Watchdog.OnStderrLine`, then passing that wrapper as `ChildConfig.Stderr`. SDD-20's single drain goroutine remains the sole reader. Watchdog matches MUST drive ONLY alerts; they MUST NOT influence supervisor state-machine transitions (Constitution V — log-pattern is alert-only).

**Audit vocabulary reconciliation**

- **FR-026-014**: The orchestrator MUST emit ONLY audit action names that exist as `audit.Action*` constants. Before emitting any new supervisor-scope name, the constants block in `internal/audit/chain.go` MUST be extended. The final reconciled vocabulary the orchestrator emits is (every item is either an existing constant or MUST be added before first emission):
  - `supervisor_session_claimed` — emitted after a successful initial `/claim` + JWT persist.
  - `supervisor_session_refreshed` — emitted after a successful refresh-window claim swap.
  - `supervisor_silent_refill` — emitted after a successful silent refill following clean exit OR crash.
  - `supervisor_child_clean_exit` — emitted when `Child.Wait` returns exit code `0`.
  - `supervisor_child_exit_crash` — emitted when `Child.Wait` returns a non-zero exit code OTHER than `78`.
  - `supervisor_child_exit_78` — emitted when `Child.Wait` returns exit code `78`.
  - `supervisor_awaiting_approval` — emitted when the orchestrator enters `awaiting-approval` for ANY reason; the audit `Data` field carries the cause (`validator`, `unknown_jti`, `exit_78`, `boot_timeout`).
  - `supervisor_stale_alert` — emitted when the orchestrator fires any `[STALE] …` alert; `Data` carries the alert class and the offending scope name (never the secret value).
  - `supervisor_grace_entered` / `supervisor_grace_exited` — emitted when a grace-window restart begins/ends.
  - `supervisor_boot_timeout` — emitted when `boot_retry_timeout` exhausts.
  - `client_refresh_invoked` — emitted when the status-socket `refresh` verb is consumed.
  - Reused from the existing constants block: `secret_retrieved` (already emitted server-side), `discord_disconnected`, `discord_reconnected`.

  Plan phase MUST finalise the constant additions in `internal/audit/chain.go` and update `docs/SPEC.md §FR-14` if the documented list disagrees with the names above. No aspirational name MUST remain in any spec or data-model document after this chunk lands.

**Injected interfaces**

- **FR-026-015**: The orchestrator MUST define a `Validator` interface that accepts a context, a scope name, and a `*securebytes.SecureBytes`, returning an error. The no-op default returns nil for every call.
- **FR-026-016**: The orchestrator MUST define an `Alerts` interface accepting a context, an `AlertClass` enum value, and an `AlertPayload` carrying only non-secret labels (scope name, error class string, reason string). The no-op default discards. The `AlertClass` enum is locked at exactly ten values, one per orchestrator emission site (FR-026-013): `ValidatorFailure`, `Exit78`, `VaultRejectedJWT`, `RefillFailed`, `DiscordUnavailableOnClaim`, `RefreshDenied`, `RefreshTimeout`, `GraceEntered`, `LogPatternMatch`, `BootTimeout`. The four server-side classes in `docs/LIFECYCLE-SCENARIOS.md "Required alert classes"` (`approval request`, `daemon refresh request`, `Discord disconnected`, `Discord reconnected`) are NOT in this enum — they are emitted on other channels (server-side Approver, server-side Discord-connection watcher). SDD-28 supplies the rendering layer on top of these 10 values and adds no new enum values without a spec amendment.
- **FR-026-017**: The orchestrator MUST define a `Watchdog` interface accepting a context and a single observed stderr line (`[]byte`). The no-op default discards.
- **FR-026-018**: All three interfaces MUST be defined at the orchestrator's consumer site, never imported from a producer package (Constitution IX). The orchestrator MUST function correctly with any combination of no-op defaults — i.e., SDD-26/27/28 can each ship independently.

**Shutdown**

- **FR-026-019**: On `SIGTERM` or `SIGINT`, the orchestrator MUST cancel its root context, forward `SIGTERM` to the child via `Child.Forward`, wait for every spawned goroutine (`StatusServer`, `Refresher`, the child-wait loop, the claim-refresh loop, and the main dispatcher) to join, release the pidfile, and return cleanly with no leaked goroutine and no left-behind socket inode.
- **FR-026-020**: Shutdown MUST complete within a bounded interval (the SDD-22 socket shutdown is sub-second; the child-forward + wait window is bounded by the platform `SIGTERM`-handling semantics, with `Child.Forward(SIGKILL)` as a hard ceiling escalation if `SIGTERM` is not honoured within a documented timeout — Plan phase pins the value).

**Test gates**

- **FR-026-021**: The orchestrator file(s) MUST achieve ≥85% line coverage under `magex test:race`.
- **FR-026-022**: The unit-test suite MUST contain at least one named test per child-exit-code branch (`0`, non-zero non-`78`, `78`) AND at least one named test per boot-retry branch (immediate success, N-failures-then-success, timeout exhausted).
- **FR-026-023**: An anti-leak test MUST assert the orchestrator file contains no state-string literal (`"running"`, `"awaiting-approval"`, etc.), no raw `78` literal, no `runtime.GOOS` branch, and no `case StateRunning` pattern outside the package's locked state-table reasoning (Constitution VII friendly; state-table reasoning is owned by SDD-19, exit-78 by SDD-20).
- **FR-026-024**: A sentinel-leak test MUST assert that a `testutil.SentinelSecret(N)` flowing through `Refiller`+`Grace`+child env construction never appears in: operational slog records, audit JSONL records, status-socket JSON, alert payloads, or any error message.

**Anti-contracts**

- **FR-026-025**: The orchestrator MUST NOT mutate any SDD-19..22 exported surface. It consumes only the locked APIs (`Store.Transition`, `Store.Snapshot`, `Refiller.Refill`, `Refresher.Run`, `Grace.Set/.Get/.Evict`, `StatusServer.AttachStatusInputs`/`AttachRefreshHandler`, `Child.Start/.Wait/.Forward`, `AcquirePidFile`).
- **FR-026-026**: The orchestrator MUST NOT pre-define any specific validator implementation, alert class rendering, or watchdog pattern. SDD-26, SDD-27, and SDD-28 own those.
- **FR-026-027**: The orchestrator MUST NOT introduce any new direct `go.mod` dependency (Constitution XI).
- **FR-026-028**: The orchestrator MUST NOT call `string(*securebytes.SecureBytes)` or otherwise materialise plaintext secret bytes outside the existing single permitted JWT-bearer-header site inside `Refiller`. Alert payloads carry scope names and error classes only (Constitution X).
- **FR-026-029**: Every goroutine the orchestrator spawns MUST have an explicit owner, an explicit context cancellation path, an explicit termination condition, and a top-frame `recover()` (Constitution IX).
- **FR-026-030**: The orchestrator MUST NOT add a second drain on `Child.Stdout`/`Stderr`. The SDD-20 `Child.drainLoop` already owns that; watchdog wiring MUST be a single hook on the existing drain.
- **FR-026-031**: No `init()` function, no package-level mutable `var`, and no global state. Sentinel-class `var Err… = errors.New(…)` declarations are the only permitted package-level `var`s, consistent with Constitution IX.

### Key Entities *(include if feature involves data)*

- **Orchestrator**: the long-lived, single-process object that composes the SDD-19..22 primitives into the documented daemon lifecycle. Holds dependency handles, owns the goroutine inventory, drives the supervisor state machine via `Store.Transition`.
- **Validator** (interface, injected, no-op default): per-scope credential validator. Real implementation supplied by SDD-26.
- **Alerts** (interface, injected, no-op default): operator-visible alert sink. Real classes + rendering supplied by SDD-28.
- **Watchdog** (interface, injected, no-op default): log-pattern observer. Real pattern engine supplied by SDD-27.
- **Audit vocabulary**: the reconciled set of `audit.Action*` constants the orchestrator emits (see FR-026-014). Any name in `docs/SPEC.md §FR-14` referencing supervisor-scope events MUST resolve to exactly one constant after this chunk lands.
- **Boot-retry budget**: a bounded duration (`boot_retry_timeout` from the supervisor TOML, default `10m`) within which Tailscale + vault preconditions must recover before the orchestrator exits non-zero.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-026-001**: An approved first daemon bootstrap (User Story 1) reaches the `running` state and a live child PID within a bounded number of bookkeeping yields after the approver taps Approve (no `time.Sleep`-driven waits in the orchestrator), measured by an integration test that drives the harness against a `httptest`-backed vault server and a `testutil.DiscordStub`.
- **SC-026-002**: For every documented stale-credential path (validator failure, exit `78`, `ErrJTIUnknown`), the orchestrator emits exactly one `Alerts.Emit` call with a class distinct from the other two paths, and the corresponding audit action is appended to the chain — verified by a unit test per path.
- **SC-026-003**: After a clean child exit OR a non-`78` crash on a valid session, the orchestrator restarts the child without any call to the approval stub — verified by zero `Decision` records on the stub for the entire silent-refill window.
- **SC-026-004**: A `Refresher` window-tick fires exactly one fresh `/claim` and the child PID is unchanged across the swap — verified by snapshot comparison before/after the tick.
- **SC-026-005**: `SIGTERM` to a `running` supervisor returns the process within the configured shutdown timeout (Plan phase pins the value), with `runtime.NumGoroutine` returning to baseline, the pidfile released, and the status-socket inode removed.
- **SC-026-006**: Unit-test line coverage on the orchestrator file(s) is ≥85% under `magex test:race`. One named test per exit-code branch and one per boot-retry branch is present.
- **SC-026-007**: A sentinel-leak assertion (`testutil.SentinelSecret(N)`) flows through the orchestrator end-to-end without appearing in operational logs, audit JSONL, status-socket JSON, alert payloads, or error messages — verified by a `testutil.AssertSentinelAbsent` sweep across every captured byte stream.
- **SC-026-008**: Every `audit.Action*` constant the orchestrator emits exists in `internal/audit/chain.go` AND every supervisor-scope name in `docs/SPEC.md §FR-14` resolves to exactly one constant — verified by a grep-style test that compares the documented list against the constants block.
- **SC-026-009**: SDD-25's lifecycle harness, when re-run after this chunk lands, reaches every documented final state in scenarios 2, 3, 4, 5, 6, 7, 8, 9a, 9b, 10/supervisor, 11, 12, 13, 15 — i.e., AC-10 is unblocked. (SDD-25 owns the assertion; this chunk owns the precondition.)
- **SC-026-010**: No new direct `go.mod` dependency is introduced (verified by `git diff go.mod` containing no new `require` line for a direct dep).
- **SC-026-011**: A `runtime.NumGoroutine` pre/post snapshot at unit-test teardown shows no growth (asserted via a bounded `runtime.Gosched()` poll, never via `time.Sleep`).

## Assumptions

- **A-026-1**: The plan phase chooses between (Option A) expanding `internal/cli/supervise.go` inline and (Option B) extracting the orchestrator to a new `internal/supervise/lifecycle` sub-package. Both options are constitutional; the spec is location-neutral. Plan-phase decision criterion in `docs/sdd/SDD-24.md`: orchestrator > ~700 LOC → Option B.
- **A-026-2**: Exponential-backoff schedule for the boot-retry loop (initial interval, multiplier, cap) is plan-phase. Constraint: total elapsed time across attempts MUST NOT exceed `boot_retry_timeout`; per-attempt HTTP probe timeout MUST be ≤ 2s so a stuck network does not consume the entire budget on the first attempt.
- **A-026-3**: The shutdown timeout that gates `SIGTERM → child exit → SIGKILL escalation` is plan-phase. Constraint: MUST be ≤ the SDD-22 `StatusServer` shutdown ceiling so the entire `Run` returns within a single bounded window.
- **A-026-4**: The `/claim` signing key handle is owned by the orchestrator and passed to `Refiller` via the existing `Store.Token.Use(func(b []byte){...})` borrow pattern; no new exported seam is needed (the `internal/supervise` post-construction wiring already exists).
- **A-026-5**: The integration harness (SDD-25) consumes the orchestrator as a black box; it does NOT depend on any orchestrator-internal seam. The orchestrator's only test seams are the three injected interfaces, the existing `Clock` seam on `Store`/`Refresher`, and the existing `http.Client.Transport` rewrite pattern from `claim_handler_integration_test.go`.
- **A-026-6**: The validators that SDD-26 will supply (anthropic, anthropic-oauth, openai, google-ai, github) are not in scope for this chunk; the no-op default is sufficient for orchestrator unit tests.
- **A-026-7**: Resolved by clarification 2 (2026-05-12): the orchestrator's `AlertClass` enum is locked at the 10 emission-site values in FR-026-016 — NOT the 8 LIFECYCLE-SCENARIOS classes verbatim. Of the 8 LIFECYCLE classes, 4 map onto orchestrator enum values (`validator stale failure` → `ValidatorFailure`; `child exit 78 stale failure` → `Exit78`; `log-pattern stale warning` → `LogPatternMatch`; `vault/server unreachable at boot timeout` → `BootTimeout`) and 4 are emitted by non-orchestrator channels (`approval request`, `daemon refresh request`, `Discord disconnected`, `Discord reconnected`). SDD-28 supplies rendering for the 10 enum values only; it does not extend the enum.
- **A-026-8**: The log-pattern watchdog (SDD-27) is alert-only by Constitution V; the orchestrator's `Watchdog` interface contract enforces this by exposing only an `OnStderrLine`-shaped method — no state-machine hook.
- **A-026-9**: The `client_refresh_invoked` audit event fires in `internal/cli/supervise.go`'s existing refresh-coalescer path; the orchestrator MUST emit it from the `perform` closure that this chunk replaces — the existing coalescer logic stays.
- **A-026-10**: This chunk is a precondition for SDD-25's AC-10 contract but does NOT itself ship the 15-scenario integration suite. SDD-25 remains paused until this chunk lands and is unblocked at SDD-24's implement phase per the SDD-24 playbook.

## Dependencies

- **D-026-1**: SDD-19 (`Store`, `State`, `Event`) — locked.
- **D-026-2**: SDD-20 (`Child`, `Exit78`, `ChildConfig`) — locked.
- **D-026-3**: SDD-21 (`Refiller`, `Refresher`, `Grace`, `ErrJTIUnknown`, `ErrBootTimeout`) — locked.
- **D-026-4**: SDD-22 (`PidFile`, `StatusServer`, `StatusInputs`, `AttachStatusInputs`, `AttachRefreshHandler`) — locked.
- **D-026-5**: SDD-08 (`internal/transport/sign.CanonicalJSON`, `sign.Sign`) — for signing the initial and refresh claims.
- **D-026-6**: SDD-18 (`internal/supervise/config`) — for the per-supervisor TOML the orchestrator consumes.
- **D-026-7**: SDD-13 (`internal/audit/chain.go`) — for the audit-event constants block that this chunk extends.
- **D-026-8**: Existing `internal/cli/supervise.go` skeleton — for the cobra command, dry-run path, pidfile acquisition, status-server construction, refresh-coalescer, and shutdown wiring. The orchestrator REPLACES the trailing `<-rootCtx.Done()` wait with the real lifecycle dispatcher.
