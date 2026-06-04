# DAEMONS — supervisor guide

> The complete guide to running long-running daemons under `hush supervise`.
> Pairs with `docs/LIFECYCLE-SCENARIOS.md` (the 15 named scenarios) and
> `docs/CONFIG-SCHEMA.md` (the per-supervisor TOML schema).

---

## 1. Why `hush supervise` (and not `hush request --exec`)

`hush request --exec` is fine for interactive sessions: the operator approves
once, a shell starts, tools inside the shell inherit env vars, the operator
exits when done. The approval lifetime equals the shell's lifetime.

For a long-running daemon — anything started by launchd / systemd that needs
secrets at process startup — that model collapses on every child exit:

- A clean restart (binary update, config reload) terminates the wrapping
  `hush request` process. launchd reruns the wrapper. A new Discord DM lands
  on the approver's phone. Multiply by every child-exit reason and the
  operator gets retrained to auto-approve, which defeats the whole point.
- A 3am crash blocks the service until morning unless someone wakes up to
  approve.

`hush supervise` decouples Discord-approval lifetime from child-process
lifetime. One supervisor manages exactly one child. The supervisor holds the
JWT and ephemeral ECIES key in mlocked memory across child crashes, updates,
and restarts, within a bounded session TTL. The child exits and is restarted
silently with re-fetched secrets — no new approval, no buzz on the phone.

When credentials are genuinely stale, three independent signals surface that
explicitly (validators, exit-78, log-pattern watchdog) so the supervisor
fails loudly rather than silently. See `docs/LIFECYCLE-SCENARIOS.md`
Scenarios 5, 6, 15.

---

## 2. The supervisor model in one diagram

```
       launchd / systemd
              │
              ▼
   ┌──────────────────────┐
   │ hush supervise       │  ← long-lived; never exits while child can run
   │  (state machine)     │
   │                      │
   │  fetching            │  ─┐
   │   │                  │   │ JWT + ephemeral ECIES key in mlocked memory
   │   ▼                  │   │
   │  running    ◄──────  │   │ silent refill on clean exit / crash within TTL
   │   │                  │   │
   │   ▼                  │   │
   │  awaiting-approval   │  ─┘ on exit 78, vault 401-unknown-jti, or TTL exhausted
   │                      │
   └─────────┬────────────┘
             │ fork/exec
             ▼
       ┌─────────────┐
       │   child     │  ← knows nothing about hush; reads secrets from env
       │  (daemon)   │
       └─────────────┘
```

The supervisor owns auth state. The child owns workload. A child crash never
kills the supervisor; only an unrecoverable supervisor error (config load
failure, NTP unsynced, pidfile lock failure) terminates the supervisor itself.

---

## 3. A 48-hour walkthrough (canonical lifecycle)

This walkthrough is what to expect when running a daemon for two days. Every
step here maps to one of the 15 scenarios in `docs/LIFECYCLE-SCENARIOS.md`.

### Day 1, 09:30 — first boot

- launchd starts `<daemon>-hush-launch.sh` (a copy of
  `deploy/supervise-launch.sh.template`).
- The script reads the client passphrase from the OS keychain and `exec`s
  `hush supervise --config ~/.hush/supervisors/<daemon>.toml`.
- Supervisor performs startup checks (NTP sync, file modes, vault server
  reachable on Tailscale, pidfile flock).
- Supervisor enters `fetching`, sends signed `/claim` with
  `session_type=supervisor`.
- A `[DAEMON]` Discord DM lands on the approver's phone.
- Approver taps `Approve 20h`.
- Supervisor receives the JWT, fetches all scoped secrets, runs the
  configured validators, forks the child with secrets injected as env vars.
- Plaintext secrets are zeroed from supervisor memory (unless grace cache
  is enabled — see §6).
- State: `running`. (Scenario 2)

### Day 1, 10:00–22:00 — normal operation

- Child runs uninterrupted. Supervisor watches `wait()`. No Discord traffic.

### Day 1, 22:14 — child crashes (e.g. OOM)

- Supervisor detects exit, checks state machine + remaining TTL + scope.
- TTL still valid (~17 hours remain). Supervisor performs a **silent refill**:
  re-fetches secrets via `/s/<name>` with the cached JWT, re-runs validators,
  forks a fresh child. ~3 seconds end-to-end.
- No Discord call. No phone buzz. (Scenario 4)

### Day 2, 02:00 — overnight TTL expires

- Configured `requested_ttl=20h` started at 09:30 Day 1, expires 05:30 Day 2.
  (Numbers are illustrative; pick your own TTL to anchor the next refresh
  window during waking hours.)
- Child keeps running — env vars are already injected and the child does not
  consult the supervisor for them.
- Supervisor cannot perform a silent refill until refresh.

### Day 2, 03:47 — child crashes overnight (within grace window, if enabled)

**With grace cache (`cache_secrets_for_restart=true`):**
- Supervisor still holds the last-decrypted secret set in mlocked memory for
  `cache_grace_ttl` (default 60m, capped 4h).
- Child restarts using cached secrets. Approver is **not** paged at 4am.
- Supervisor logs an audit `supervisor_grace_entered` event and emits a
  **warning-tier** Discord message (NOT a critical-tier DM).
- (Scenario 9 with grace path)

**Without grace cache (`cache_secrets_for_restart=false`):**
- Supervisor enters `awaiting-approval` and emits a Discord alert.
- Child stays down until the approver wakes up and approves.
- (Scenario 9 strict path)

### Day 2, 09:00 — refresh window opens

- Supervisor's refresh scheduler fires. A `[DAEMON] Refresh` DM is sent.
- Approver taps `Approve 20h`. Supervisor receives a fresh JWT.
- The child is **not** restarted; only the supervisor's refill capability is
  refreshed. (Scenario 8)

The scheduler's approval claim is distinct from the operator-facing
`hush client refresh` command. The scheduled claim can obtain a fresh
approval and swap the supervisor session; the `client refresh` command
only refills secrets under the session the supervisor already holds.

### Day 2, 14:30 — secret rotated mid-session

- Operator runs `hush secret rotate ANTHROPIC_API_KEY` on the vault host.
- Vault file is atomically rewritten; SIGHUP triggers an atomic
  `atomic.Pointer[Vault]` swap on the running server.
- Operator runs `hush client refresh --supervisor <daemon>` on the agent
  host.
- Supervisor re-fetches the rotated secret, re-runs validators, gracefully
  restarts the child with the new value. (Scenario 13)

### Day 2, 18:11 — child exits with code 78 (stale credentials contract)

- Some upstream credential was revoked elsewhere. Child detects auth
  failure and exits with `code 78` (`EX_CONFIG`).
- Supervisor unconditionally enters `awaiting-approval`, regardless of TTL.
- A `[STALE] Child Exit 78` Discord alert lands.
- Operator rotates the secret in the vault (`hush secret rotate`) and runs
  `hush client refresh --supervisor <daemon>`.
- Supervisor re-fetches, validates, restarts the child. (Scenario 5)

That covers the realistic 48 hours: silent refills, overnight TTL, refresh
window, mid-session rotation, exit-78 stale-credential recovery — all
without the operator needing to wake up at 3am.

---

## 4. Refresh window tuning

The `refresh_window` field in the supervisor TOML controls when the daily
refresh DM fires. Three knobs, all in the operator's local timezone:

- `refresh_window = "09:00-10:00"` — the DM arrives somewhere in this window
  on the day before TTL expiry. Pick a window when the operator is reliably
  near their phone (commute, morning desk time, lunch — your call).
- `refresh_nudge_before = "30m"` — if the original window's DM is ignored,
  a fallback nudge fires this long before TTL actually expires. Keep it
  short enough to feel like a nudge, long enough that the operator can
  finish a meeting.
- `requested_ttl = "20h"` — choose so the next refresh window arrives
  before TTL expiry. With a 09:00–10:00 window and a 20h TTL, an approval
  at 09:30 Day 1 expires 05:30 Day 2 — beyond the next 09:00 Day 2 window's
  start. That is the intended overlap: the next window prompts you BEFORE
  the prior session expires.

### Concrete examples

- **Generous overlap, low risk of missed window:**
  `refresh_window=09:00-12:00`, `requested_ttl=24h`,
  `refresh_nudge_before=1h`.
- **Tight overlap (acceptable if the operator is reliably reachable in the
  morning):** `refresh_window=09:00-10:00`, `requested_ttl=20h`,
  `refresh_nudge_before=30m`.
- **24/7 operator availability not assumed:** lean on `cache_secrets_for_restart=true`
  with `cache_grace_ttl=60m` to absorb early-morning crashes that would
  otherwise hit before the refresh window opens.

---

## 5. Authoring credential validators

Validators run on the supervisor (NOT the vault server — the vault is
isolated from outbound internet). Each validator hits the cheapest
read-only provider endpoint and returns nil on success or a typed error on
401 / 403 / network / timeout.

### Built-in validators

| Name | Endpoint | Purpose |
|------|----------|---------|
| `anthropic` | `GET https://api.anthropic.com/v1/models` | Anthropic API key |
| `anthropic-oauth` | OAuth introspection | Anthropic OAuth token |
| `openai` | `GET https://api.openai.com/v1/models` | OpenAI API key |
| `google-ai` | `GET https://generativelanguage.googleapis.com/v1beta/models` | Google AI key |
| `github` | `GET https://api.github.com/user` | GitHub PAT |

Wire them in supervisor TOML:

```toml
[validators]
ANTHROPIC_API_KEY = "anthropic"
OPENAI_API_KEY    = "openai"
GITHUB_TOKEN      = "github"
```

Unknown validator names are startup errors — `hush supervise` refuses to
start if your TOML lists a validator that does not exist.

### Authoring a custom validator (future scope)

The `Validator` interface is intentionally tiny:

```go
type Validator interface {
    Validate(ctx context.Context, value *securebytes.SecureBytes) error
}
```

Any custom validator MUST:

- Use the SecureBytes value via `Use(fn)` with a bounded scope; never copy
  the secret to a `string`.
- Issue a single read-only HTTP request with a timeout (5s default).
- Return `ErrStaleCredential` on 401/403, `ErrValidatorTimeout` on timeout,
  `ErrValidatorNetwork` on network failure.
- Never log or include the secret value in error messages.

Custom validators are not yet wired up. Today only the five built-ins
above are registered; the registry refuses unknown names by design.

---

## 6. The grace-window tradeoff

`cache_secrets_for_restart` is the most security-relevant knob in the
supervisor TOML. Pick deliberately.

### Strict mode (`cache_secrets_for_restart = false`)

- Plaintext secrets only ever live in the child process's memory after
  being injected. Supervisor zeroes them immediately after fork.
- A 3am crash → child stays down until the approver approves in the morning.
- Stronger isolation; lower availability. Pick this for the most sensitive
  daemons or in environments with strict secrets-handling policies.

### Grace mode (`cache_secrets_for_restart = true`, default `60m`, cap `4h`)

- Supervisor holds the last-decrypted secret set in mlocked memory for
  `cache_grace_ttl` beyond JWT validity.
- A 3am crash → supervisor uses cached secrets, restarts the child, defers
  Discord approval to the next refresh window.
- Doubles on-host plaintext surface (child + supervisor) for the duration
  of the cache TTL.
- Approval becomes "first arrival, not ongoing presence" — the approver
  approved when the day started, the cache fills the gap.

The cap of `4h` is enforced by config validation; `hush supervise` refuses
to start with a longer grace window. Override at runtime with `--no-cache`
to force strict mode regardless of TOML.

Documented residual risk: see [`docs/SECURITY.md`](SECURITY.md) §6.

---

## 7. Status socket — the agent-visible freshness API

Every running supervisor binds a Unix socket at:

- macOS: `~/Library/Caches/hush/supervise-<daemon>.sock` (mode `0600`, parent dir `0700`)
- Linux: `$XDG_RUNTIME_DIR/hush-supervise-<daemon>.sock` (mode `0600`)

Filesystem permissions are the auth — there is no bearer token and no
HTTP-on-localhost listener.

`GET /status` returns the JSON shape from [`docs/CONFIG-SCHEMA.md`](CONFIG-SCHEMA.md). The
`hush client status [--supervisor NAME] [--json]` subcommand pretty-prints
or emits the JSON directly.

### Recommended pattern: pre-task gate

Downstream agents should query the supervisor BEFORE starting a long task:

```bash
if hush client status --supervisor <daemon> --json \
   | jq -e '.scope_stale | length == 0' >/dev/null; then
  ./run-long-task.sh
else
  echo "ERROR: required scopes are stale; refusing to run" >&2
  exit 1
fi
```

This closes the "the agent has no way to know its credentials are bad"
gap that motivated `hush supervise` in the first place.

### Manual renewal

`hush client renew --supervisor <daemon>` posts a renewal command to the
supervisor's status socket. The supervisor sends a fresh `/claim` request
through the normal Discord approval path, swaps to the newly-approved
session, and leaves the child running by default. Use this when the
operator wants to extend a daemon's approval horizon before the next
reseal or expiry window:

```bash
hush client renew --supervisor <daemon>
```

Pass `--restart` only when you also want a clean child restart after the
approval succeeds:

```bash
hush client renew --supervisor <daemon> --restart
```

Renewal preserves the human-in-the-loop boundary: it does not
auto-approve and it does not silently extend a session. Denial, timeout,
and ineligible supervisor states are reported as explicit failures.

### Manual refresh

`hush client refresh --supervisor <daemon>` posts a refresh command to the
supervisor's status socket. The supervisor re-fetches secrets, re-runs
validators, and gracefully restarts the child under the existing approved
session. It is a silent secret refill: no fresh `/claim` is issued and no
Discord approval prompt is sent. Use this:

- After `hush secret rotate <name>` on the vault host (Scenario 13).
- After a `[STALE] Child Exit 78` alert if you've fixed the underlying
  credential and want to recover under the current session.

If the goal is a fresh operator approval for the next window, run
`hush client renew --supervisor <daemon>` instead.

The scheduled refresh window's refill step and the `client refresh` verb
both refill under the existing session; only `client renew`, first boot,
or the scheduler's fresh approval claim requests a new approval.

---

## 8. Common operational patterns

### Multiple daemons on one host

One supervisor per daemon. Each gets its own:

- Supervisor TOML in `~/.hush/supervisors/<daemon>.toml`.
- launchd plist or systemd unit pointing at a `<daemon>-hush-launch.sh`
  copy of `deploy/supervise-launch.sh.template`.
- PID file and status socket (paths in TOML).

Failure coupling and DM rate-limit are independent per daemon.

### Disabling a daemon temporarily

`launchctl unload` (macOS) or `systemctl stop` (Linux). The supervisor
zeros all cached state on shutdown. Re-enabling triggers a fresh approval.

### Migrating a daemon to a new host

Run `hush init --client --machine-index N` on the new host with a fresh
machine index. Register the new client public key on the vault server
(`~/.hush/clients.json`). Copy the supervisor TOML, update paths if
needed, register the launcher with launchd/systemd. The new host is now a
distinct supervisor; the old host's pidfile/socket can be removed.

### Adding a new secret to an existing daemon's scope

1. `hush secret add NEW_SECRET` on the vault host (interactive TTY).
2. SIGHUP fires automatically (or send `kill -HUP <pid>` if the old
   server didn't pick it up).
3. Update the supervisor TOML's `scope` array to include `NEW_SECRET`.
4. Restart the supervisor (`launchctl kickstart` / `systemctl restart`).
5. Approver approves the fresh `[DAEMON]` claim with the expanded scope.

---

## 9. Cross-references

| Topic | See |
|-------|-----|
| Named lifecycle scenarios | [`docs/LIFECYCLE-SCENARIOS.md`](LIFECYCLE-SCENARIOS.md) |
| Per-supervisor TOML schema | [`docs/CONFIG-SCHEMA.md`](CONFIG-SCHEMA.md) |
| Threat model + grace-cache residual risk | [`docs/SECURITY.md`](SECURITY.md) |
| Status socket JSON shape | [`docs/CONFIG-SCHEMA.md`](CONFIG-SCHEMA.md) |
| HTTP API used during refill | [`docs/API.md`](API.md) |
| Supervisor state machine | [`docs/ARCHITECTURE.md`](ARCHITECTURE.md) §8 |
| Operational runbooks | [`docs/OPERATIONS.md`](OPERATIONS.md) |
