# Feature Specification: Supervisor Refill, Refresh, and Grace Cache

**Feature Branch**: `021-supervise-refill-refresh`
**Created**: 2026-05-10
**Status**: Draft
**Input**: User description: "internal/supervise: Refill (GET /s/<name> per scope, ECIES decrypt, 401-unknown-jti → awaiting-approval); Refresh (cron-like in operator window, T-30 fallback); Grace (mlocked SecureBytes per name, ≤4h cap, disabled when config false); boot retry (exp backoff, never prompts Discord); per-supervisor DM rate limiter (default 1/5min)"

## Overview

This chunk adds the supervisor's three credential-lifecycle helpers — **Refill**, **Refresh**, and **Grace** — plus the **boot-retry** protocol and the per-supervisor **DM rate limiter**. Together they decide *when* a supervised daemon's secrets get re-fetched, *how* an overnight crash avoids paging the operator, and *how* the supervisor behaves when the vault server or the network is temporarily unreachable.

- **Refill** fetches the current set of secrets from the vault server using the cached supervisor JWT.
- **Refresh** decides *when* the next Discord-approved claim should fire, anchored to an operator-configured local-time window with a T-30-minute fallback.
- **Grace** holds the last-decrypted secret set in protected memory so a clean restart within a bounded window does not always burn a Discord prompt.
- **Boot retry** absorbs Tailscale / vault startup races without flooding Discord.
- **DM rate limiter** caps how often a single supervisor can prompt the operator, so a degenerate loop cannot become a notification storm.

This chunk implements behaviour required by FR-11 (`hush supervise` lifecycle), FR-18 (refresh-window scheduler), FR-19 (bootstrap retry-with-backoff), and the supervisor portions of FR-12. It is the primary behavioural carrier for AC-10 scenarios 3 (clean child exit → silent refill), 7 (vault server restart / 401-unknown-jti), 8 (daytime refresh-window prompt), 9 (overnight expiry with and without grace cache), and 11 (boot retry / startup ordering recovery).

## Clarifications

### Session 2026-05-10

- Q: When the supervisor process starts (or restarts) while the wall clock is already inside the configured refresh window and no prompt has yet fired for today, what should the scheduler do? → A: Fire on startup if inside window AND no refresh has yet fired for today's window — tracked by an in-memory flag scoped to the current process (lost on restart, never persisted to disk).
- Q: When a refill aborts via a transient error (FR-021-4: network, DNS, 5xx, timeout, malformed response) after some scopes have already been decrypted, what happens to that partial material? → A: Atomic refill — any failure (401-unknown-jti or transient) destroys all partially-decrypted material before the error propagates; refill is all-or-nothing.
- Q: What does the boot-retry phase actually probe to decide that the vault server is reachable? → A: An unauthenticated health endpoint (e.g. `GET /healthz`); only network-layer failures (connection refused, DNS, TLS handshake, timeout) count as "not yet connected". HTTP-level responses (any status code) from the health endpoint count as "connected" and exit boot retry. The first authenticated refill happens AFTER boot retry exits, where a 401-unknown-jti routes to `awaiting-approval` per Story 1.
- Q: When a scheduled refresh prompt is dropped by the per-supervisor DM rate limiter, does the scheduler treat that fire as issued or dropped? → A: Counts as issued — the scheduler advances to the next window crossing and does NOT retry within the same window. The WARN log entry produced by the rate limiter (FR-021-24) is the operator-visible signal that a refresh attempt was suppressed. Refresh prompts do NOT bypass the rate limiter.
- Q: How is the grace-cache eviction primitive (FR-021-16) exposed on the `Grace` type, given that the chunk's locked exported API in `docs/sdd/SDD-21.md` does not currently list one? → A: Add a single new method `func (g *Grace) Evict(name string)` to the locked exported API — it destroys the named entry's underlying `SecureBytes` and removes the map slot, with no error path. The orchestrator (SDD-23) wires this into the `hush client refresh` invalidation flow. The chunk's tests MUST include explicit coverage of `Evict` (entry destroyed, subsequent `Get` returns "not present").

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Silent refill on clean child exit (Priority: P1)

A supervised daemon (e.g. OpenClaw) exits cleanly mid-session. The supervisor still holds a valid JWT. The operator should not be paged. The supervisor must silently re-fetch the daemon's secrets from the vault server, run validators, and restart the child — without sending any Discord prompt.

**Why this priority**: This is the hot path for the supervisor model. Without it, every child restart turns into a re-approval, and the operator gets trained to auto-approve. It is the single behaviour that justifies the existence of `hush supervise` versus `hush request`. Lifecycle Scenario 3.

**Independent Test**: With a stub vault server returning 200 + ECIES-encrypted payloads and a stub child that exits 0, the supervisor performs a refill that delivers fresh secrets to the next child invocation, no Discord prompt is issued, and the supervisor remains in the running state. Verifiable as a unit test against the supervisor without a real Discord transport.

**Acceptance Scenarios**:

1. **Given** a supervisor in the `running` state with a non-expired session JWT and a child that exits with code 0, **When** the supervisor handles the exit, **Then** the supervisor calls the vault server once per requested scope using the cached JWT, decrypts each response, and the new secret set is available for the next child start without any Discord interaction.
2. **Given** the same precondition, **When** any one of the per-scope vault calls returns HTTP 401 with an `unknown_jti` error indicator, **Then** the supervisor stops the refill, transitions to `awaiting-approval`, and does NOT silently retry the refill against the same JWT.
3. **Given** a supervisor performing a refill, **When** a transient network error or timeout occurs on a single scope fetch, **Then** the refill fails with a typed network error that the orchestrator can choose to retry, and the supervisor does NOT transition to `awaiting-approval` solely on a network error.

---

### User Story 2 - Refresh window fires at the configured time (Priority: P1)

The supervisor's session is approaching expiry. The operator has configured a local-time refresh window (e.g. `09:00–10:00`). A `[DAEMON] Refresh` Discord prompt must arrive inside that window — not at 03:00, not after the session has already expired. If the operator-configured window for the day has already passed AND the session is within 30 minutes of expiry, a single fallback prompt fires immediately so the daemon does not run out before the next window opens.

**Why this priority**: This is the core human-factors guarantee of the supervisor model: pages happen during waking hours. Lifecycle Scenario 8. Without it, daemons either expire silently or wake the operator at 3am, which is exactly the failure mode the supervisor exists to eliminate.

**Independent Test**: With an injected clock and a stub Refill, the refresh scheduler can be advanced through a synthetic day; the Discord prompt is observed exactly inside the configured window for the normal path and exactly once at T-30-minutes for the fallback path. No real Discord transport is required.

**Acceptance Scenarios**:

1. **Given** a refresh window of `09:00–10:00` local and a session expiring later that day, **When** the local clock crosses the window's start time, **Then** the supervisor triggers exactly one refresh attempt (one Discord prompt) within the window.
2. **Given** the supervisor process starts at a wall-clock time that is already inside the configured window AND no refresh has yet fired for today (in-memory state), **When** the scheduler initialises, **Then** exactly one refresh prompt is issued for today's window.
3. **Given** the configured window has already passed for today AND the remaining session lifetime is less than the fallback threshold (default 30 minutes), **When** the fallback condition is evaluated, **Then** the supervisor triggers exactly one refresh attempt immediately, regardless of wall-clock position relative to the window.
4. **Given** a refresh attempt is already in flight or has already fired in the current window, **When** another window-tick or fallback-tick evaluates, **Then** no duplicate prompt is sent.
5. **Given** a running refresh scheduler, **When** the supervisor's lifecycle context is cancelled, **Then** the scheduler exits cleanly with no leaked goroutine.

---

### User Story 3 - Overnight crash absorbed by grace cache (Priority: P1)

The session expired overnight but the operator opted into the grace cache. The child crashes at 03:47. Instead of paging the operator, the supervisor uses the last-decrypted secret set held in protected memory to restart the child, then defers the Discord prompt until the next refresh window. The grace cache MUST be bounded: entries expire no later than the configured grace window, which is itself capped at 4 hours, and the cache is fully disabled when the operator has set `grace.cache_secrets_for_restart=false`.

**Why this priority**: This is the headline availability/secrecy tradeoff documented in `SECURITY.md` §6 and `DAEMONS.md` §6. Lifecycle Scenario 9. It is opt-in by design, but when opted in it must behave exactly as advertised — strict enforcement of the 4-hour cap, immediate teardown when disabled, no leakage of cached values into logs or strings.

**Independent Test**: With an injected clock, the grace cache can be primed, advanced past the configured TTL, and observed to evict; with the disabled config, no entry is ever stored; with a configured-but-too-large TTL, the value is clamped to 4 hours. No real vault server or Discord transport is required.

**Acceptance Scenarios**:

1. **Given** the grace cache is enabled with a 60-minute window and a primed secret set, **When** the supervisor needs secrets while its JWT is expired and the cache entry is younger than the window, **Then** the cached set is returned and no vault call is made.
2. **Given** the grace cache is enabled, **When** an entry's effective TTL elapses, **Then** the supervisor no longer hands out the cached value and the underlying secret material is irreversibly destroyed.
3. **Given** the operator has configured `grace.cache_secrets_for_restart=false`, **When** any code path attempts to populate the grace cache, **Then** no entry is stored and any future lookup returns "not present" without surfacing an error.
4. **Given** the operator has configured a grace window longer than 4 hours, **When** the cache is initialised, **Then** the effective TTL applied to entries is exactly 4 hours, regardless of the configured value.
5. **Given** any cached secret value, **When** any audit, operational, or error path renders the value, **Then** the value is never converted to a plain string and never appears in any log, error message, or debug output.

---

### User Story 4 - Boot retry tolerates startup races without paging (Priority: P2)

A supervisor is started by launchd/systemd at machine boot. Tailscale or the vault server is not yet reachable. The supervisor must retry connectivity with exponential backoff up to a total `boot_retry_timeout` budget. Discord MUST NOT be prompted during this window — the vault server may simply be down, and a flood of "approve session" prompts during a network blip would train the operator to auto-approve. On exhaustion, the supervisor exits non-zero so the OS-level supervisor (launchd / systemd) handles the next retry.

**Why this priority**: Lifecycle Scenario 11. Without this, every machine reboot that races Tailscale becomes a Discord notification, and the system trains the operator to approve sessions blindly.

**Independent Test**: With a stub vault server that fails the first N connection attempts and a stub Discord transport that records every prompt, the supervisor retries with monotonically-increasing delays, never sends a Discord prompt during the retry window, and either succeeds (when the stub starts returning 200) or exits non-zero at the budget cap.

**Acceptance Scenarios**:

1. **Given** the vault server is unreachable at supervisor start, **When** the supervisor enters the boot-retry phase, **Then** subsequent connectivity attempts use exponentially-increasing delays bounded by the configured boot-retry budget.
2. **Given** the supervisor is in the boot-retry phase, **When** any retry attempt fails for any reason (network, DNS, 5xx, timeout), **Then** no Discord prompt is sent and no Discord-prompt rate-limit token is consumed.
3. **Given** the boot-retry total time budget is exhausted with no successful connection, **When** the budget elapses, **Then** the supervisor exits with a non-zero status that the OS-level supervisor can use to schedule the next process restart.
4. **Given** a successful connection occurs before the budget is exhausted, **When** the supervisor exits the boot-retry phase, **Then** the normal claim/refill/refresh flow resumes (which is the point at which Discord prompts may legitimately fire).

---

### User Story 5 - DM rate limiter prevents prompt floods (Priority: P2)

A misbehaving supervisor (loop, bug, hostile log pattern) might attempt to send the operator many Discord DMs in quick succession. A per-supervisor token bucket caps the rate (default: one prompt per five minutes). Excess attempts are dropped with a WARN-level log entry — they are NOT queued, NOT retried automatically, and NOT escalated to a different channel. The operator is the safety valve; if the rate-limiter is dropping prompts, that is itself a signal worth seeing in the logs.

**Why this priority**: Loud-failure principle (Constitution V) plus operator-visibility principle: a supervisor whose rate-limiter is firing is a supervisor in trouble, but a flooded DM channel trains the operator to mute the bot, which defeats the entire approval gate.

**Independent Test**: With a fake clock, a fresh token bucket admits the first prompt and rejects the second within the 5-minute window; after the window elapses, a new prompt is admitted. Each rejection produces exactly one WARN log entry naming the supervisor and the suppressed prompt category. No real Discord transport required.

**Acceptance Scenarios**:

1. **Given** a fresh supervisor with a 5-minute bucket, **When** two DM-bearing operations are attempted within five minutes, **Then** the first proceeds and the second is dropped with a single WARN log entry that does NOT include any secret material.
2. **Given** a dropped DM, **When** the next bucket-refill interval elapses, **Then** a subsequent DM-bearing operation proceeds normally.
3. **Given** two distinct supervisors running on the same host, **When** one of them exhausts its bucket, **Then** the other's bucket is unaffected (per-supervisor isolation).

---

### Edge Cases

- **Cached JWT mid-rotation**: The vault server has restarted between the supervisor's last refill and this one. The first scope returns 200, the second returns 401-unknown-jti. The first scope's decrypted material MUST be irreversibly destroyed before the supervisor transitions to `awaiting-approval`; partial state must not leak forward. The same atomicity (FR-021-5) applies to transient mid-refill failures: a network error on scope 2 of 3 destroys scope 1's already-decrypted bytes before returning.
- **Refill called after grace cache was disabled mid-process**: If `grace.cache_secrets_for_restart` is observed as false, no `Set` is ever effective even if the call is made. `Get` returns "not present" without error. There is no error path for "cache is disabled" — calls degrade silently.
- **Refresh window crosses midnight**: A window like `23:00–01:00` MUST be treated as a single contiguous interval. The fallback (T-30 minutes before expiry) MUST still fire correctly when the expiry time itself crosses midnight.
- **Clock changes (DST, NTP step)**: A backwards step in the wall clock MUST NOT cause the refresh to fire repeatedly within a single window. A monotonic ticker is the appropriate primitive for "has the window-evaluation interval elapsed".
- **Supervisor process killed mid-refill**: If the supervisor exits while a refill is in flight, any partially-decrypted secret material in supervisor memory MUST be destroyed by process exit (mlock + zero-on-free guarantees from the existing `SecureBytes` type). No on-disk artifacts are produced.
- **Boot retry succeeds, then immediate first refill fails 401-unknown-jti**: This is *not* the same as "boot retry failed". The supervisor proceeds to the normal `awaiting-approval` flow (Story 1, Scenario 2). Boot retry is connectivity-only; it does not gate on the JWT being usable.
- **DM rate limiter's WARN log is itself a Discord prompt**: It is not. The rate limiter's WARN log goes to the operational log only. Only operator-action prompts (refresh, awaiting-approval) traverse the Discord transport.
- **Grace cache holds a value from a now-revoked secret**: When the operator rotates a vault secret and runs `hush client refresh`, that command's downstream effect MUST cause grace cache eviction for the affected names. (The eviction is the orchestrator's job — this chunk MUST expose an evict primitive that the orchestrator can call, otherwise the orchestrator cannot keep the cache honest.)
- **Grace TTL configured as 0**: Treated identically to "cache disabled". No entry is ever stored.

## Requirements *(mandatory)*

### Functional Requirements

#### Refill

- **FR-021-1**: The supervisor MUST be able to fetch a named secret from the vault server using the cached supervisor JWT, decrypt the response into protected memory, and make the decrypted value available for child injection without ever materialising the value as a plain string.
- **FR-021-2**: The supervisor MUST be able to refill a *set* of named scopes in a single logical operation; the refill operation MUST iterate the requested scopes and either deliver all of them successfully or fail the whole refill with a typed error.
- **FR-021-3**: When any single per-scope fetch returns HTTP 401 with an `unknown_jti` error indicator, the refill MUST fail with a distinguishable error (`ErrJTIUnknown`) such that the caller can transition the supervisor state to `awaiting-approval`. The supervisor MUST NOT silently retry refill against the same JWT after `ErrJTIUnknown`.
- **FR-021-4**: When a per-scope fetch fails for any reason other than 401-unknown-jti (network error, DNS error, 5xx, timeout, malformed response), the refill MUST fail with a typed error that is distinct from `ErrJTIUnknown`. Such failures MUST NOT, by themselves, force a state transition to `awaiting-approval`.
- **FR-021-5**: Refill MUST be atomic with respect to decrypted material: when a refill aborts for ANY reason (`ErrJTIUnknown`, transient network error, DNS error, 5xx, timeout, malformed response, or any other failure category), every secret value that was successfully decrypted earlier in the same refill MUST be destroyed before the error propagates. A failed refill MUST NEVER hand any partial decrypted material to the caller.
- **FR-021-6**: Refill MUST emit one audit-eligible event per refill attempt that distinguishes silent-refill success, JTI-unknown failure, and other-network failure. The event MUST NOT include any secret value.

#### Refresh

- **FR-021-7**: The supervisor MUST schedule a refresh prompt within the operator-configured local-time window (e.g. `09:00–10:00`). Exactly one prompt MUST be issued per window crossing.
- **FR-021-8**: When the configured window has already passed for today AND the remaining session lifetime is less than a configured fallback threshold (default 30 minutes), the supervisor MUST issue exactly one fallback refresh prompt immediately, irrespective of wall-clock position relative to the window.
- **FR-021-9**: The refresh scheduler MUST exit cleanly when its supervisory context is cancelled, leaking no goroutine and triggering no further prompts.
- **FR-021-10**: The refresh scheduler MUST be idempotent within a single window crossing: re-entering the window-check after a successful fire MUST NOT issue a duplicate prompt. The "already fired this window" indicator MUST be process-local in-memory state (not persisted), and on supervisor process restart the scheduler MUST fire exactly once on initialisation if the wall clock is already inside the configured window for today.
- **FR-021-11a**: A refresh fire that is dropped by the per-supervisor DM rate limiter MUST be treated by the scheduler as "issued for this window": the scheduler MUST mark the window as fired, advance to the next window crossing, and MUST NOT retry within the same window. Refresh prompts MUST NOT bypass the DM rate limiter. Operator visibility for a suppressed refresh comes solely from the WARN log entry required by FR-021-24.
- **FR-021-11**: The refresh scheduler's wall-clock evaluation MUST be tolerant of clock changes (DST, NTP step). A monotonic source SHOULD be used for the "should I re-evaluate now" tick, while wall-clock semantics apply only to the "is now inside the configured window" predicate.

#### Grace cache

- **FR-021-12**: When `grace.cache_secrets_for_restart=true`, the supervisor MUST hold the last-decrypted secret set in protected memory keyed by secret name; each entry MUST expire no later than the configured grace window, and the effective TTL MUST be capped at 4 hours regardless of the configured window's length.
- **FR-021-13**: Grace cache entries MUST expire by *destroying* the underlying protected-memory value (zeroing + munlock equivalent) — not merely by removing the map reference.
- **FR-021-14**: When `grace.cache_secrets_for_restart=false` (or when the configured window is zero), the grace cache MUST behave as permanently empty: `Set` operations are silent no-ops, `Get` returns "not present", no entries are ever stored.
- **FR-021-15**: A cached secret value MUST NEVER be converted to a Go string, MUST NEVER appear in any log line, error message, or debug output, and MUST NEVER be returned to a caller as anything other than a protected-memory handle.
- **FR-021-16**: The grace cache MUST expose an explicit eviction primitive that an orchestrator can call to purge a named entry (so that `hush client refresh` after a vault-side rotation can keep the cache honest). The primitive MUST be a single method on the `Grace` type with the shape `Evict(name string)` (no error return); calling it MUST destroy the named entry's underlying protected-memory value (FR-021-13 destruction semantics) and remove the map slot, such that any subsequent `Get(name)` returns "not present". Calling `Evict` for a name that is not present MUST be a silent no-op.
- **FR-021-17**: Grace-cache hits MUST be auditable as a distinct event class so the operator can see when an overnight crash was absorbed by the cache.

#### Boot retry

- **FR-021-18**: At supervisor start, the boot-retry phase MUST attempt vault-server connectivity with exponential backoff bounded by a total budget (the operator-configured `boot_retry_timeout`). The probe MUST be an unauthenticated health-endpoint request: it MUST NOT carry the supervisor's JWT and MUST NOT exercise the per-scope refill path. A network-layer failure (connection refused, DNS error, TLS handshake failure, or timeout) counts as "not yet connected" and triggers backoff; any HTTP response from the health endpoint (regardless of status code) counts as "connected" and exits boot retry into the normal claim/refill flow.
- **FR-021-19**: The boot-retry phase MUST NOT issue any Discord prompt and MUST NOT consume any Discord-prompt rate-limit token, regardless of how many connectivity failures occur.
- **FR-021-20**: On budget exhaustion without a successful connection, the supervisor MUST exit with a non-zero status that signals "operationally unhealthy at boot" to the OS-level supervisor.
- **FR-021-21**: On a successful connection before budget exhaustion, the boot-retry phase MUST exit cleanly into the normal claim/refill/refresh flow.
- **FR-021-22**: Each boot-retry attempt MUST be logged at WARN level with the attempt number and the underlying error category — no Discord, audit-only.

#### DM rate limiter

- **FR-021-23**: Each supervisor MUST enforce a per-supervisor rate limit on Discord-prompt-bearing operations. The default rate MUST be one prompt per five minutes.
- **FR-021-24**: When the rate limit is exceeded, excess attempts MUST be dropped (not queued, not retried automatically) and MUST produce exactly one WARN log entry per drop, naming the supervisor and the suppressed prompt category.
- **FR-021-25**: The WARN log entry for a dropped prompt MUST NOT contain any secret material and MUST NOT itself trigger a Discord notification.
- **FR-021-26**: Rate limiter state MUST be isolated per supervisor: one supervisor exhausting its bucket MUST NOT affect another supervisor's bucket on the same host.
- **FR-021-27**: The rate limiter's notion of time MUST be tolerant of wall-clock changes — a backwards clock step MUST NOT silently grant additional prompts.

### Key Entities

- **Refill operation**: A single logical attempt to fetch and decrypt every scope the supervisor needs, identified by the supervisor name and the set of requested scope names. Outcome is either a complete fresh secret set, `ErrJTIUnknown` (caller transitions state), or a typed transient error (caller decides whether to retry).
- **Refresh schedule**: An interpreted operator-configured local-time window plus a fallback threshold. Produces zero or one fire events per (window, day) pair, plus zero or one fallback fire per session.
- **Grace cache entry**: A named protected-memory secret value with an absolute expiry timestamp (≤ now + min(configured window, 4h)). Disabled-mode and zero-window are equivalent to "permanently empty".
- **Boot-retry budget**: A monotonic time budget for "first-connection-after-process-start" attempts. Distinguishes startup races from operationally-actionable failures.
- **Per-supervisor DM token bucket**: A simple rate-limit counter bound to a single supervisor. Default refill rate one token per five minutes; capacity one. Drains on prompt issuance, drops excess attempts.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-021-1**: Lifecycle Scenarios 3, 7, 8, 9, and 11 each have a passing automated test in this chunk.
- **SC-021-2**: A clean child exit while the supervisor's session is valid produces zero Discord prompts and a fresh secret set delivered to the next child invocation.
- **SC-021-3**: Across a synthetic 24-hour day with a configured `09:00–10:00` window, exactly one refresh prompt is issued (or exactly two, when the T-30 fallback fires for a session whose window was missed).
- **SC-021-4**: With grace cache enabled and a configured 60-minute window, an overnight child crash within the cache TTL restarts the child without a Discord prompt; with the same scenario but `cache_secrets_for_restart=false`, the same crash transitions the supervisor to `awaiting-approval`.
- **SC-021-5**: Any configured grace window greater than 4 hours produces an effective TTL of exactly 4 hours, observable from the cache's expiry behaviour.
- **SC-021-6**: A boot-retry phase with a stub vault server that fails 5 attempts then succeeds completes successfully without issuing a Discord prompt; with a stub that fails for the entire budget, the supervisor exits non-zero with no Discord prompt issued.
- **SC-021-7**: A burst of 10 prompt-bearing operations within 60 seconds against a fresh DM rate limiter results in exactly one prompt issued and exactly nine WARN log entries; none of the WARN entries contain secret material.
- **SC-021-8**: No code path in this chunk converts a cached or in-flight secret value to a Go `string`, observable both by lint/type-check and by a fuzz / inspection test that asserts no `string(secret)` conversion exists.
- **SC-021-9**: All goroutines spawned by this chunk (refresh scheduler, grace sweeper if any) terminate cleanly on context cancellation, observable via the Go race detector and a "no goroutine leaked" assertion across a stop/start cycle.
- **SC-021-10**: Test coverage for the refill, refresh, and grace files in `internal/supervise` is at least 95%, and the `-race` flag is clean.

## Assumptions

- The supervisor configuration package (SDD-18, `internal/supervise/config`) already supplies parsed, validated values for `refresh_window`, `refresh_nudge_before` (T-30 fallback threshold), `requested_ttl`, `grace.cache_secrets_for_restart`, `grace.window`, and `boot_retry_timeout`. This chunk consumes those values; it does not redefine the schema.
- The supervisor state machine (SDD-19, `internal/supervise/state.go`) already exposes the legal transitions used here (notably `* → awaiting-approval`). This chunk's helpers signal the orchestrator via typed errors; the orchestrator (SDD-23) drives the transitions.
- The child fork/exec layer (SDD-20, `internal/supervise/child.go`) already exists and is the consumer of the secret set this chunk produces. This chunk does NOT spawn children directly.
- ECIES decrypt (SDD-09) and the `SecureBytes` type (existing) already provide the protected-memory primitive and the decryption pipeline. This chunk does NOT add any new cryptographic primitive.
- The Discord transport and the underlying approval-prompt rate limiter implementation (SDD-11) already exist; this chunk's "DM rate limiter" requirement is satisfied by **using** the existing per-supervisor mechanism and by surfacing rate-limit drops as WARN logs without escalating them as state transitions.
- The HTTP client used for vault-server calls is configured upstream (TLS / Tailscale / timeout policy is not redecided here).
- The vault server's `unknown_jti` 401 response shape is the contract defined in the existing server handler suite (SDD-13). This chunk consumes that shape; it does not redefine the wire format.
- The operator's local timezone is determined by the running process's environment (Go's `time.Local`). Multi-timezone operation is out of scope for v0.1.0.
- "Prompt-bearing operation" for the DM rate limiter means any operator-visible Discord prompt (refresh request, awaiting-approval surfacing). Operational logs and audit-log entries are not prompts.
- Grace-cache eviction triggered by operator-driven `hush client refresh` is wired up by the orchestrator (SDD-23). This chunk MUST expose an evict primitive but is not responsible for invoking it from the client-refresh flow.
- Boot retry's exit-non-zero behaviour relies on the OS-level supervisor (launchd / systemd) for the eventual restart. This chunk does not implement its own outer retry loop.
