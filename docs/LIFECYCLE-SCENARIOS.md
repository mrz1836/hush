# Lifecycle Scenarios

This document is the canonical behavioral reference for hush's runtime —
especially the supervisor. Each numbered scenario describes a concrete
end-to-end story (normal use or failure), the flow it follows, and the
outcomes hush guarantees. Operators read it to understand what hush will
do under specific conditions; the integration suite under `tests/integration/`
uses these scenarios as the spec it verifies against.

---

## Purpose

hush has two primary runtime modes:

1. interactive shell sessions
2. long-running supervised daemons

The daemon path is where most failure risk lives.
These scenarios define what "correct" looks like under normal use and failure.

---

## State model

Supervisor states in v0.1.0:

- `fetching`
- `running`
- `awaiting-approval`
- `grace-restart` (conceptual sub-state when cached secrets are being used)
- `stopped`

Implementation may represent grace as flags instead of a distinct enum, but the behavior must still exist when enabled.

---

## Scenario 1 — first interactive shell request

Flow:
1. user runs `hush request --scope ANTHROPIC_API_KEY,GITHUB_TOKEN --exec "zsh"`
2. client derives its machine key and ephemeral ECIES keypair
3. client sends signed `/claim`
4. vault server verifies signature, IP, nonce, timestamp
5. Discord DM is sent to the configured approver
6. the approver approves
7. server returns scoped interactive JWT
8. client fetches secrets one by one via `/s/<name>`
9. each response is ECIES-encrypted to the ephemeral client key
10. client decrypts in memory and launches `zsh` with env vars injected
11. shell persists until the user exits
12. token expires later; no background refresh happens for interactive mode

Expected outcomes:
- no secrets written to disk
- no secret values logged
- approval is required exactly once for the shell session

---

## Scenario 2 — first daemon bootstrap

Flow:
1. launchd/systemd starts `hush supervise --config ~/.hush/supervisors/<daemon>.toml`
2. supervisor validates config, NTP, pid file, server reachability, and Tailscale presence
3. supervisor enters `fetching`
4. supervisor sends signed `/claim` with `session_type=supervisor`
5. Discord DM labeled `[DAEMON]` reaches the configured approver
6. the approver approves requested TTL
7. supervisor stores JWT + ephemeral ECIES key in mlocked memory
8. supervisor fetches all scoped secrets
9. configured validators run before child start
10. if validators pass, child starts with env vars injected
11. plaintext secret values are zeroed from supervisor memory unless grace cache is enabled
12. supervisor enters `running`

Expected outcomes:
- one approval brings up the daemon
- child does not need to know hush internals
- supervisor, not child, owns auth/session lifecycle

---

## Scenario 3 — clean child exit within valid supervisor session

Flow:
1. child exits with code 0 or another non-stale clean shutdown path
2. supervisor observes exit while session JWT is still valid
3. supervisor refetches secrets silently using existing JWT
4. validators run again
5. child restarts
6. no Discord approval is requested

Expected outcomes:
- restart is fast
- no human interruption
- audit log records silent refill and child restart

---

## Scenario 4 — child crash within valid supervisor session

Flow:
1. child crashes unexpectedly
2. supervisor remains alive
3. supervisor checks session validity
4. supervisor silently refetches secrets
5. validators run
6. child restarts

Expected outcomes:
- crash does not kill the supervisor
- crash does not force a new approval if session is still valid
- crash alerting may happen separately, but approval spam must not happen

---

## Scenario 5 — child exits with code 78 (stale credentials)

Flow:
1. child detects auth drift and exits with code 78
2. supervisor treats exit 78 as authoritative stale-credential signal
3. supervisor does not silently restart the child with the same session
4. supervisor enters `awaiting-approval`
5. supervisor sends a `[STALE] Child Exit 78` alert
6. the operator rotates/fixes the secret and either:
   - approves a fresh session request, or
   - triggers `hush client refresh --supervisor <daemon>`
7. supervisor refetches fresh secrets, validates them, and restarts child

Expected outcomes:
- exit 78 short-circuits naive restart loops
- stale creds are visible immediately
- the child is not relaunched into a known-bad auth state

---

## Scenario 6 — validator catches bad secret before child start

Flow:
1. supervisor fetches secrets after approval or silent refill
2. validator for one secret returns 401/invalid auth
3. supervisor blocks child launch/restart
4. supervisor emits `[STALE] Validator Failure` alert naming the failed scope
5. supervisor enters `awaiting-approval`
6. child remains stopped until the secret is corrected and refreshed

Expected outcomes:
- wrong-value-in-vault is caught before workload starts
- child never runs with obviously broken credentials

---

## Scenario 7 — vault server restart invalidates current session

Flow:
1. vault server restarts and loses in-memory active session map
2. supervisor later attempts silent refill
3. server rejects JWT with unknown/revoked session state (401-like unknown jti path)
4. supervisor interprets this as session no longer usable
5. supervisor enters `awaiting-approval`
6. fresh approval is requested
7. on approval, normal flow resumes

Expected outcomes:
- failure is clean and explicit
- supervisor does not loop forever on silent refill retries
- recovery path is obvious: re-approve

---

## Scenario 8 — session TTL nears expiry during daytime

Flow:
1. supervisor tracks `session_expires_at`
2. next refresh window arrives (default `09:00-10:00` local)
3. supervisor sends `[DAEMON] Refresh` prompt
4. the approver approves from phone
5. supervisor updates JWT for next session window
6. child keeps running throughout; no forced restart solely for refresh

Expected outcomes:
- refreshes happen during waking hours
- supervisor session continuity is renewed without a service interruption

---

## Scenario 9 — session TTL expires overnight, child later crashes

Without grace cache:
1. session expires overnight
2. child keeps running because env vars are already injected
3. child later crashes before the morning refresh approval
4. supervisor cannot silently refill because session is expired
5. supervisor enters `awaiting-approval`
6. child stays down until the approver approves in the morning

With grace cache enabled:
1. session expires overnight
2. child later crashes within `cache_grace_ttl`
3. supervisor uses cached plaintext secret set from mlocked memory
4. child restarts without paging the approver at 3am
5. supervisor still prompts for fresh approval in the next refresh window

Expected outcomes:
- the tradeoff is explicit
- strict mode favors secrecy purity
- grace mode favors overnight resilience

---

## Scenario 10 — Discord unavailable during a new claim

Flow:
1. client or supervisor submits `/claim`
2. server cannot reach Discord or the bot is disconnected
3. server returns 503
4. no auto-approve fallback exists
5. caller surfaces the failure clearly

Expected outcomes:
- fail closed
- existing sessions may continue working
- no new sessions are issued without approval

---

## Scenario 11 — Tailscale not ready at boot

Flow:
1. launchd/systemd starts supervisor at machine boot
2. Tailscale network is not ready yet
3. supervisor performs retry-with-backoff up to `boot_retry_timeout`
4. once Tailscale and vault reachability succeed, normal claim flow proceeds
5. if timeout is exceeded, supervisor exits with explicit operational error

Expected outcomes:
- normal boot races are tolerated
- failure mode is bounded and explainable

---

## Scenario 12 — agent checks status before long task

Flow:
1. downstream agent runs `hush client status --supervisor <daemon> --json`
2. local status socket returns freshness/health state
3. agent sees required scopes healthy and proceeds
   or
   agent sees stale scopes and refuses to begin the task

Expected outcomes:
- agents have a proactive way to inspect readiness
- auth drift becomes machine-readable instead of guesswork

---

## Scenario 13 — secret rotated on vault host during active daemon session

Flow:
1. the operator updates a secret via `hush secret rotate ...`
2. vault file is atomically rewritten
3. server reloads vault via SIGHUP or equivalent atomic swap path
4. running child still has old env vars until next restart/refetch
5. the operator or automation runs `hush client refresh --supervisor <name>`
6. supervisor refetches the rotated secret, validates it, and restarts child cleanly

Expected outcomes:
- rotation propagation is intentional and visible
- no hidden assumption that child env mutates in place

---

## Scenario 14 — duplicate supervisor start attempt

Flow:
1. second `hush supervise` instance starts for same config/name
2. existing pid file/flock is already held
3. second instance refuses to proceed
4. explicit split-brain error is emitted

Expected outcomes:
- only one supervisor owns a given daemon name
- duplicate launchd/systemd bugs do not create double-children or conflicting sessions

---

## Scenario 15 — log-pattern watchdog sees auth failure string

Flow:
1. child emits a known auth-failure log line
2. watchdog matches the configured pattern
3. supervisor emits `[STALE] Log Pattern Match` alert
4. no restart decision is made based on the log alone
5. operator investigates or waits for validator/exit-78 confirmation

Expected outcomes:
- log signals are useful but not trusted as control-plane truth
- false positives cause alerts, not destructive behavior

---

## Required alert classes

Distinct operator-visible alert classes:

- approval request
- daemon refresh request
- validator stale failure
- child exit 78 stale failure
- log-pattern stale warning
- Discord disconnected
- Discord reconnected
- vault/server unreachable at boot timeout

These should remain visually distinct in wording and/or label.

---

## Phase 0 completion check

This document is sufficient when an implementation agent can answer, without guessing:

- what happens on first boot?
- what happens on clean restart?
- what happens on crash?
- what happens on exit 78?
- what happens when Discord is down?
- what happens when the vault restarts?
- what happens overnight with and without grace cache?

If those flows are still fuzzy, Phase 0 is not done.
