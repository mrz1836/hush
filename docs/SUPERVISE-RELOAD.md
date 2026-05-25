# Supervisor reload — zero-downtime HTTP handoff

This is the operator runbook for `hush supervise reload`. It explains
**when to use the HTTP proxy handoff**, **when not to**, how the swap
actually happens, the audit event it emits, every failure mode it
exposes, and the exact config + CLI sequence to drive one.

If you are an SDK consumer (Go agent or daemon embedding
`pkg/client`), read [`pkg/client/README.md`](../pkg/client/README.md)
and [`docs/AGENT-INTEGRATION.md`](AGENT-INTEGRATION.md) for the
typed `Reload()` surface. This document is the operator-side mirror.

---

## 1. When to use HTTP proxy handoff (and when not to)

The HTTP proxy handoff is the v1 strategy that gives a supervised
HTTP daemon a **truly zero-downtime restart**: a new child boots on a
fresh private port, hush proxies traffic to it after a successful
readiness probe, then SIGTERMs the old child. In-flight requests on
the old backend drain naturally; new requests land on the new child.

**Use HTTP proxy handoff when all of these are true:**

- The supervised child is an HTTP server.
- You can run two copies of the child simultaneously for a few seconds
  (during the swap window) without breaking shared state — typically
  true for stateless API servers, false for daemons that hold an
  exclusive resource (a single TCP listener of their own, a pid-locked
  database backend, a singleton hardware device, etc.).
- The child binds the port hush tells it to via the `HUSH_BIND_PORT`
  environment variable, not a hard-coded port.
- You need to roll out a config / binary / certificate change without
  interrupting public traffic, AND you accept that the swap path
  routes traffic through an extra in-process hop (hush's reverse
  proxy) for the lifetime of the supervisor.

**Do NOT use HTTP proxy handoff when:**

- The child is not HTTP (gRPC over raw TCP, TLS-terminating proxy
  that needs the client's source IP without `X-Forwarded-For` rewrite,
  arbitrary TCP/UDP services, long-lived WebSocket sessions that
  cannot tolerate a backend swap mid-stream). Stay on the default
  supervisor; restart is observable as a brief connect-refused window.
- The child must own the public listener directly (e.g. it sets
  socket options the proxy will not replicate, or the operations team
  has external monitoring tied to the child's exact socket
  fingerprint). For these, a future socket-activation handoff is on
  the roadmap — see §9.
- You only need to reload the supervisor's own state (rotate a
  bot-token Keychain item, etc.). That path is not yet wired; restart
  the supervisor instead.

Plain (non-reload-eligible) supervisors are unaffected. If you do
not add `[child.handoff]` to your supervisor TOML, hush behaves
exactly as before: the child owns the public listener, restarts
incur a brief connection-refused window, and `hush supervise reload`
returns `config-invalid`.

---

## 2. Lifecycle of a successful reload

```
operator                hush supervise (live)            new child
   │                          │                              │
   │  hush supervise reload   │                              │
   ├─────────────────────────►│                              │
   │                          │ allocate loopback port P_new │
   │                          │ build env: HUSH_BIND_PORT=P  │
   │                          ├─────── fork + exec ─────────►│
   │                          │                              │ bind 127.0.0.1:P_new
   │                          │                              │ serve /health → 200
   │                          │ readiness probe (HTTP)       │
   │                          │  ◄───────── 200 OK ──────────┤
   │                          │ proxy.SetBackend(P_new) ↯    │
   │                          │   (atomic pointer swap)      │
   │                          │ audit: supervisor_child_swap │
   │                          │ SIGTERM old child            │
   │                          │ wait up to grace, SIGKILL    │
   │  ok (readiness 42ms,     │                              │
   │      strategy http-proxy)│                              │
   │◄─────────────────────────┤                              │
```

State-machine view: `StateRunning → StateSwapping → StateRunning`.
The status socket reports `state = "swapping"` for the duration of
the swap; agents subscribed via `Watch()` see an `EventStateChange`
on each transition.

If the readiness probe **fails** (or the budget elapses): the new
child is SIGTERM'd, the proxy backend pointer stays on the **old**
child, no `supervisor_child_swap` audit event is emitted, and the
operator receives `readiness-failed`. The old child keeps serving
uninterrupted.

---

## 3. Config — making a supervisor reload-eligible

Three sections of the supervisor TOML are involved. All three are
required for reload-eligibility; the loader rejects partial setups
at startup with sentinel errors from
`internal/supervise/config/errors.go`.

### `[child.readiness]` — HTTP readiness probe (required)

```toml
[child.readiness]
http_url = "http://127.0.0.1:0/health"   # host:port replaced at swap time
timeout  = "30s"                         # total probe budget
interval = "200ms"                       # poll period
```

- `http_url` — must parse with an `http://` or `https://` scheme and
  a non-empty host. The host:port is a **placeholder** — at swap
  time hush rewrites it to `127.0.0.1:<HUSH_BIND_PORT>` of the new
  child. The path/query/fragment are preserved. Conventionally use
  `127.0.0.1:0` so the placeholder cannot accidentally reach a real
  service.
- `timeout` — wall-clock ceiling. The probe polls every `interval`
  until either a 200 response arrives or the budget elapses.
  Defaults: `timeout = 30s`, `interval = 200ms`. Both must be `> 0`.
- The child MUST serve a 2xx response on this URL once it is ready
  to accept traffic. Any non-2xx (including 503 / 404) is treated as
  not-ready and re-polled until `timeout`.

### `[child.shutdown]` — old-child termination grace (optional, default 30s)

```toml
[child.shutdown]
grace = "30s"
```

- Always populated by the loader (default 30s) because SIGTERM/SIGKILL
  timing applies on every stop, not just reloads.
- After a successful swap, hush sends SIGTERM to the old child and
  waits up to `grace` for natural exit. If the child is still alive
  at the deadline, SIGKILL is sent. Set this long enough that the
  child's in-flight HTTP handlers drain (a typical web app sees 5–15s
  of long-tail; 30s is comfortable for most cases).

### `[child.handoff]` — opt into HTTP-proxy handoff (required to enable reload)

```toml
[child.handoff]
mode        = "http-proxy"               # v1 only mode
listen_addr = "100.96.10.4:8080"         # public address hush binds
```

- `mode` — `"http-proxy"` is the only accepted value in v1. Future
  strategies (socket-activation) will appear here as additional
  string enums without breaking existing configs.
- `listen_addr` — the public host:port hush binds and serves
  through its reverse proxy. The child binds a private loopback
  port instead; that port is supplied via the `HUSH_BIND_PORT`
  environment variable. See §3.4.

### Cross-section requirements

The loader enforces three reload-eligibility invariants at config
parse time. Each maps to one sentinel:

| Missing condition | Sentinel | Where to fix |
|---|---|---|
| `[child.handoff]` present but `[child.readiness]` absent | `ErrHandoffRequiresReadiness` | Add `[child.readiness]` with at least `http_url` |
| `[child.handoff]` present but neither `child.command` nor `child.env` references `HUSH_BIND_PORT` | `ErrHandoffRequiresBindPortRef` | Wire the env var into the child's startup — see §3.4 |
| `[child.handoff].mode` is anything other than `"http-proxy"` | `ErrHandoffModeInvalid` | Use `mode = "http-proxy"` |

The loader also re-validates the existing `[child.readiness]` shape
(URL schema, positive durations) and the new `[child.shutdown]` grace
(positive). Any rejection is **startup-fatal**: `hush supervise` exits
non-zero with the sentinel before any side effects.

### Wiring `HUSH_BIND_PORT` into the child

The child must bind the port hush allocates for it. Two
equivalent patterns:

```toml
# Option A — argument templating in command
[child]
command = ["/usr/local/bin/your-daemon", "--port=$HUSH_BIND_PORT"]

# Option B — env-block reference
[child]
command = ["/usr/local/bin/your-daemon", "--port-from-env=PORT"]
env = { PORT = "$HUSH_BIND_PORT" }
```

The loader treats either as "references HUSH_BIND_PORT". The
substitution itself is performed by hush at child-spawn time; do not
rely on shell expansion (there is no shell in the exec path).

If the child cannot read an env var on startup (legacy binary that
only accepts a config file), either patch the child or stay on the
non-reload supervisor path.

### Complete reload-eligible example

```toml
name        = "api-gateway"
reason      = "Public HTTP API"
server_url  = "http://100.96.10.4:7743/h/a8k2f9"
client_machine_index = 2
session_type         = "supervisor"
requested_ttl        = "20h"
refresh_window       = "09:00-10:00"
refresh_nudge_before = "30m"
boot_retry_timeout   = "10m"
status_socket        = "~/Library/Caches/hush/supervise-api-gateway.sock"
pid_file             = "~/Library/Caches/hush/supervise-api-gateway.pid"
scope                = ["ANTHROPIC_API_KEY", "OPENAI_API_KEY"]

[child]
command     = ["/usr/local/bin/api-gateway", "--port=$HUSH_BIND_PORT"]
working_dir = "~"
env_passthrough = ["PATH", "HOME"]

[child.readiness]
http_url = "http://127.0.0.1:0/health"
timeout  = "30s"
interval = "200ms"

[child.shutdown]
grace = "30s"

[child.handoff]
mode        = "http-proxy"
listen_addr = "100.96.10.4:8080"

[validators]
ANTHROPIC_API_KEY = "anthropic"
OPENAI_API_KEY    = "openai"
```

---

## 4. Triggering a reload — CLI

```bash
hush supervise reload <config-path>
```

The positional `<config-path>` is the on-disk supervisor config the
operator wants the supervisor to validate against. **The operator's
CLI loads and validates this file locally before any socket I/O** —
a malformed file is rejected with the same sentinel set as
`hush supervise`, so you cannot accidentally tell a live supervisor
to swap into a broken config.

The supervisor uses **its already-loaded config** for the actual
swap. The path you pass is forwarded for audit attribution
(`config_path` in the reload ack) so the audit log records which
file you associated with the request.

### Success line

```
hush: supervise: reload: ok (readiness 42ms, strategy http-proxy)
```

`readiness` is the wall-clock duration from "new child Start"
to "first 2xx on the readiness URL". `strategy` is the wire-stable
handoff strategy string — `http-proxy` in v1.

### Failure shapes (stderr)

All errors are one-line and follow the locked
`hush: supervise: reload: <msg>` shape. The `<msg>` portion
contains the supervisor's reason verbatim so root-cause analysis
does not require log spelunking.

| Sentinel | Result code | Exit class | Typical cause |
|---|---|---|---|
| `errReloadConfigInvalid` | `config-invalid` | input-err | Supervisor config lacks `[child.readiness]` or `[child.handoff] mode = "http-proxy"`, or proxy listener not attached. Fix the config, restart the supervisor. |
| `errReloadReadinessFailed` | `readiness-failed` | err | New child started but did not respond 2xx on `[child.readiness].http_url` within `timeout`. Old child still serving. Check the new child's startup logs. |
| `errReloadInFlight` | `swap-in-flight` | err | Another reload is already running for this supervisor. Retry once it completes (`hush client status` to verify state). |
| `errReloadFailed` | `error` | err | Any other supervisor-side failure (child start error, backend port allocation, wrong state). Reason string carries the underlying cause. |
| `errSocketUnreachable` | `supervisor-unreachable` (client-side) | err | The supervisor socket could not be dialed. Likely the supervisor is not running, or the `status_socket` path in your config does not match the live one. |

The CLI exit code maps to hush's stable code set
(`internal/cli/exit_codes.go`):

- `config-invalid` → `ExitInputErr` (operator-fixable config issue)
- everything else above → `ExitErr` (operational failure)

### Pre-flight checklist before running a reload

1. **Validate the new config offline first.**
   ```bash
   hush supervise <new-config-path> --dry-run
   ```
   This renders the canonical claim payload without touching the live
   supervisor. Sentinel errors here predict reload failures.
2. **Confirm the new child binary is on disk at the path you expect.**
   The supervisor execs the binary; it cannot fall back.
3. **Confirm the live supervisor is in `StateRunning`.**
   ```bash
   hush client status --supervisor <name>
   ```
   Reload is rejected with `ErrSwapWrongState` outside `StateRunning`.
4. **Trigger the reload.**
   ```bash
   hush supervise reload <config-path>
   ```
5. **Verify after-state.** The success line carries the readiness
   duration and the new child PID is observable on the next
   `hush client status` (`child_pid` field) and in the
   `supervisor_child_swap` audit event (see §5).

---

## 5. Audit event shape

A successful swap appends exactly one `supervisor_child_swap`
event to the audit chain. The producer is `emitChildSwap` in
`internal/supervise/lifecycle_audit.go`. By construction it
contains only kernel/wall-clock identifiers — **no secret or env
value can appear in this event**.

| Key | Type | Value |
|---|---|---|
| `old_pid` | int | PID of the child that was active before the swap |
| `new_pid` | int | PID of the child that became active after the swap |
| `swap_completed_at` | string (RFC3339 UTC) | Wall-clock at which the swap was audited |
| `readiness_duration_ms` | int | Wall-clock ms from new-child Start to first 2xx |
| `strategy` | string | `"http-proxy"` (open enum; future: `"socket-activation"`) |

On a **failed** reload (readiness, in-flight, config refusal, etc.)
**no** `supervisor_child_swap` event is emitted. The supervisor's
slog stream records a warn-level line for the underlying failure,
and the CLI returns a non-zero exit with the typed sentinel — but
the audit chain is not polluted with not-actually-completed swaps.

---

## 6. Failure modes catalog

### 6.1 New child crashes during boot, before readiness

The readiness probe receives connection-refused until it times out;
the supervisor sees `readiness-failed`, SIGTERMs the (already-dead
or about-to-die) candidate, and leaves the old child serving.

**Operator action:** check the new child's stdout/stderr sinks — the
supervisor writes them to the same paths the boot child uses
(`child.stdout_path` / `child.stderr_path`). Fix the new binary or
revert the config and retry.

### 6.2 New child boots but returns 503 / 404 on the readiness URL

The probe keeps polling until `[child.readiness].timeout` elapses;
result is `readiness-failed`. Common cause: the child's startup
sequence has multiple stages (warmup, schema check, secret fetch)
and is not yet healthy at the moment hush probes. Either lengthen
`[child.readiness].timeout`, or make the child gate its readiness
endpoint behind a real readiness signal instead of "bound to port".

### 6.3 New child boots, readiness 2xx, but proxy backend swap fails

`ErrProxyNotStarted` is the typical cause (proxy listener was
explicitly stopped, e.g. via test fixture). In production this
should not happen — the supervisor binds the proxy at startup and
never voluntarily stops it. If you see this, file a bug with the
slog stream.

### 6.4 Old child ignores SIGTERM

Hush waits up to `[child.shutdown].grace` (default 30s), then sends
SIGKILL. The new child is already serving traffic via the proxy
swap, so the only operator-visible effect is the grace duration
before the old PID exits. Audit and status report the swap as
successful regardless.

### 6.5 Two concurrent reloads race

The single-flight lock returns `swap-in-flight` to the loser. The
winner runs normally. Operators should treat `swap-in-flight` as
"someone else is reloading; retry once they're done", not as a
permanent failure.

### 6.6 Vault unreachable during reload

The supervisor performs a silent refill of scopes from the vault to
build the env for the new child. If the vault is unreachable, the
refill fails and the reload returns `error` with the underlying
reason wrapped. The old child is untouched (its env is already
injected).

### 6.7 Supervisor crashes mid-swap

The supervisor uses its standard pidfile + flock guard — a crash
mid-swap leaves no partial state to recover from on the supervisor
side, but the candidate child may still be alive (no parent to
SIGTERM it). When the supervisor restarts (via launchd/systemd),
the boot path resolves this:

- The new supervisor binds the public `listen_addr` afresh. If the
  candidate child is still alive on its private loopback port, it
  becomes an orphan (no requests routed to it). It will exit on its
  own when its server shuts down or, more practically, when the host
  reboots.
- The boot child is launched fresh, with a new private port.
- Audit observers see no `supervisor_child_swap` for the aborted
  swap because the event is only appended after the successful
  proxy pointer swap.

If this becomes a frequent failure mode, a follow-up will add a
candidate-child tracking file so restarting supervisors can reap
orphans deterministically. v1 leaves it to the OS.

---

## 7. Concurrency, atomicity, and traffic guarantees

- **Public listener is owned by hush**, not the child. The listener
  binds once at supervisor startup and stays bound across every
  reload. Operators observe zero connection-refused windows on the
  public address for the lifetime of the supervisor.
- **Backend pointer swap is atomic.** The proxy reads the active
  backend via `atomic.Pointer`. In-flight requests targeted at the
  old backend run to completion against the old URL (the URL is
  captured per-request); subsequent requests land on the new
  backend.
- **Single-flight reload.** Concurrent `SwapChild` calls are
  serialised by an atomic-bool CAS. Losers see `swap-in-flight`
  without side effects.
- **Status socket is unaffected by the swap.** `hush client status`
  and `pkg/client.Watch()` continue to round-trip through the same
  Unix socket; state transitions are observable in real time
  (`running → swapping → running`).

---

## 8. Security and observability boundaries

- **Proxy never logs request headers, bodies, query strings, or
  upstream URLs.** The default stdlib `http.Server` error logger is
  discarded; transport errors are converted to 502/503 with a
  non-secret `X-Hush-Proxy-Reason` header. See `proxy.go`'s
  package header for the anti-contract.
- **Audit event carries only PIDs, RFC3339 timestamp, ms duration,
  and the strategy string.** Tested explicitly — see
  `tests/integration/scenario_16_reload_test.go` for the no-env-leak
  assertion.
- **`HUSH_BIND_PORT` is the only env mutation hush introduces.**
  Every other env var is the supervisor's normal child env
  (scopes + `env_passthrough` + `[child.env]`).
- **The reload verb does NOT trigger a new `/claim`.** The
  replacement child receives the existing JWT's scope refill; no
  Discord approval is required. This is the same path
  `supervisor_silent_refill` already uses on a normal child restart.

---

## 9. Future strategies — socket activation (not yet implemented)

The `strategy` field in `supervisor_child_swap` and the `[child.handoff]
mode` value are intentionally open string enums so a future
**socket-activation** handoff can land as a second strategy without
breaking audit consumers or existing configs. Socket activation is
the generic replacement for HTTP proxy handoff and would cover
non-HTTP services (raw TCP / TLS-terminating front ends / WebSocket
servers) by passing the listener file descriptor across an exec
boundary.

Socket activation requires OS-level support (launchd `Sockets` /
systemd `Sockets=`) plus child-side fd-inheritance. It is tracked as
a follow-up to T-306, not part of v1. Until then, non-HTTP services
restart with the standard supervisor restart cycle — observable as
a brief connection-refused window.

---

## 10. Quick reference

```bash
# Inspect current state.
hush client status --supervisor api-gateway

# Trigger a reload using a vetted on-disk config.
hush supervise reload ~/.hush/supervisors/api-gateway.toml

# Inspect the audit event afterwards.
jq 'select(.action == "supervisor_child_swap")' ~/.hush/audit.jsonl

# Subscribe to lifecycle events programmatically.
go doc github.com/mrz1836/hush/pkg/client SupervisorStatus.Watch
```

---

## 11. Cross-references

- [`docs/CONFIG-SCHEMA.md`](CONFIG-SCHEMA.md) — `[child.readiness]`,
  `[child.shutdown]`, `[child.handoff]` field reference.
- [`docs/LIFECYCLE-SCENARIOS.md`](LIFECYCLE-SCENARIOS.md) §16 —
  end-to-end behavioural spec for the happy path, the
  readiness-failure rollback, and the config-refusal path.
- [`docs/AGENT-INTEGRATION.md`](AGENT-INTEGRATION.md) — SDK
  `Reload()` surface for embedded Go agents.
- [`pkg/client/README.md`](../pkg/client/README.md) — `Reload()`
  method signature, typed errors, return shape.
- [`docs/OPERATIONS.md`](OPERATIONS.md) — day-to-day operator
  runbook (this document is its zero-downtime-reload appendix).
- `internal/supervise/lifecycle_swap.go` — orchestration source.
- `internal/supervise/proxy.go` — reverse-proxy listener source
  and security anti-contract.
