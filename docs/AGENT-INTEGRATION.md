# Agent integration

This guide is for **AI agent authors** (Claude Code, Codex, custom Go
daemons, Python automation harnesses) who want to consume hush
in-process rather than exec'ing the CLI.

If you only need to wrap a single program in `hush request --exec`,
read [`docs/OPERATIONS.md`](OPERATIONS.md) instead. Come back here
when you want **reactive lifecycle events**, **capability discovery
without burning approvals**, or **richer approval prompts that show
the human what your agent is about to do**.

---

## 1. When to use the SDK vs. the CLI

| You need to … | Use |
|---|---|
| Wrap one program in a shell session | `hush request --exec` |
| Run a long-lived daemon under a supervisor | `hush supervise` |
| Monitor your own session's freshness from inside the child | **`pkg/client.SupervisorStatus`** |
| Know what scopes the vault holds without triggering an approval | **`pkg/client.Me`** |
| React to "your credentials expire in 5 minutes" before getting killed | **`pkg/client.SupervisorStatus.Watch`** |
| Show the human what tool/command you're about to invoke before they approve | **`hush request --agent --model --tool --command`** |

The SDK is a Go module at `github.com/mrz1836/hush/pkg/client`. Import
it like any other Go dependency.

---

## 2. Installing the SDK

```bash
go get github.com/mrz1836/hush@latest
```

```go
import "github.com/mrz1836/hush/pkg/client"
```

All exported identifiers in `pkg/client` are part of hush's v1 public
API. Wire-format additions appear as new optional fields with
`omitempty` so existing SDK builds keep working when talking to a
newer server.

---

## 3. Pre-task freshness gate (cooperative agent pattern)

The recommended pattern for any agent running under `hush supervise`:
**before doing work, check that your credentials are fresh**. The
supervisor exposes its state on a local Unix socket; the SDK gives you
a typed client.

```go
package main

import (
    "context"
    "log"
    "os"
    "time"

    "github.com/mrz1836/hush/pkg/client"
)

func main() {
    sock := os.Getenv("HUSH_STATUS_SOCKET")
    if sock == "" {
        log.Fatal("HUSH_STATUS_SOCKET not set — am I running under hush supervise?")
    }
    sup := client.NewSupervisorStatus(sock)
    defer sup.Close()

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    status, err := sup.Snapshot(ctx)
    if err != nil {
        log.Fatal("supervisor status unavailable:", err)
    }
    if len(status.ScopeStale) > 0 {
        log.Fatalf("refusing to start — stale scopes: %v", status.ScopeStale)
    }
    log.Printf("scopes healthy; session expires at %s", status.SessionExpiresAt)

    runWork()
}
```

The supervisor passes the socket path into the child via
`HUSH_STATUS_SOCKET`. Agents that don't want to take a hard dependency
on that env var can construct the path themselves
(`hush --config <path> server-url`-style helpers will land in a later
release).

---

## 4. Capability discovery — `Me()`

Before issuing a fresh `/claim`, agents can ask the vault server *what
scopes exist* and *what does my current session look like* — without
triggering a Discord approval. This is the cheapest way for an agent
to **batch its requests** ("ask for A, B, C in one approval") and
**avoid pestering the operator** ("I already have a fresh session for
this scope; skip the request").

```go
resp, err := client.Me(ctx, client.MeRequest{
    ServerURL:   serverURL,                   // e.g. "http://100.64.0.1:7743/h/abcd1234"
    ClientKey:   enrolledMachineKey,          // *ecdsa.PrivateKey from hush init client
    BearerJWT:   os.Getenv("HUSH_BEARER"),    // optional — populates CurrentSession when valid
    MachineName: mustHostname(),
})
if err != nil {
    if errors.Is(err, client.ErrUnauthenticated) {
        log.Fatal("server rejected my signed request — am I enrolled?")
    }
    log.Fatal(err)
}
fmt.Println("scopes available:", resp.ScopesAvailable)
if resp.CurrentSession != nil {
    fmt.Println("current jti:", resp.CurrentSession.JTI,
        "expires:", resp.CurrentSession.ExpiresAt,
        "uses left (max):", resp.CurrentSession.MaxUses)
}
```

`Me()` is signed (no anonymous probes), but it **never triggers an
approval prompt**. Poll it as often as your planning logic needs.

---

## 5. Lifecycle events — `Watch()`

The agent's single worst failure mode is being **killed mid-task** when
its credentials rotate. `Watch()` solves this with a reactive event
channel:

```go
events, _ := sup.Watch(ctx, client.WatchOptions{
    PollInterval:     30 * time.Second,
    ExpiryThresholds: []time.Duration{15 * time.Minute, 5 * time.Minute, time.Minute},
})
for ev := range events {
    switch ev.Type {
    case client.EventInitial:
        log.Println("watching; session expires at", ev.Status.SessionExpiresAt)
    case client.EventExpiresSoon:
        switch {
        case ev.Threshold >= 15*time.Minute:
            startCheckpointing()           // begin a graceful wind-down
        case ev.Threshold >= time.Minute:
            finishInflightTasks()          // stop accepting new work
        default:
            shutdownCleanly()              // exit before kill
        }
    case client.EventStateChange:
        log.Println("supervisor state →", ev.Status.State)
    case client.EventSessionRenewed:
        log.Println("fresh session", ev.Status.SessionJTI, "— resume normal cadence")
    case client.EventScopeHealthChange:
        log.Println("scope health changed; stale:", ev.Status.ScopeStale)
    case client.EventError:
        log.Printf("poll error (transient): %v", ev.Err)
    }
}
```

| Event | Fires when |
|---|---|
| `EventInitial` | Once on subscribe, carrying the current snapshot. |
| `EventStateChange` | Supervisor state transitions (e.g. `running` → `awaiting-approval`). |
| `EventScopeHealthChange` | Healthy/stale scope set diff. |
| `EventSessionRenewed` | New `SessionJTI` observed — resets the expiry-soon threshold tracker so a renewed session re-fires the warning ladder. |
| `EventExpiresSoon` | A configured `ExpiryThresholds` boundary is crossed. Each threshold fires at most once per session. |
| `EventError` | A poll failed (transient). The watch CONTINUES — the channel stays open. |

**Implementation note**: today `Watch()` is implemented via polling
plus local timers (poll for state/scope changes; precise timers for
expiry warnings). A future release may switch to a server-pushed event
stream; the channel and event types are designed to be
forward-compatible.

---

## 6. Agent context on approvals — `--agent --model --tool --command`

When your agent issues a `/claim` through the CLI, populate the
agent-context flags so the human approver sees **what tool you're
about to invoke** before clicking Approve:

```bash
hush request \
  --agent "claude-code/$(claude --version)" \
  --model "$ANTHROPIC_MODEL" \
  --tool Bash \
  --command "$(echo "$BASH_CMD" | head -c 200)" \
  --summary "$(recent_activity_one_liner)" \
  --scope GITHUB_TOKEN --ttl 10m --max-uses 1 \
  --reason 'push refactor branch' \
  --exec git push origin claude/refactor
```

The Discord approval embed will show:

```
✅ Interactive secret request

Machine: laptop-mrz
Mesh IP: 100.64.1.5
Scope:   GITHUB_TOKEN
Reason:  push refactor branch
TTL:     10m0s
Agent:   claude-code/1.2.3
Model:   claude-opus-4-7
Tool:    Bash
Command: git push origin claude/refactor
Summary: Refactoring auth module
Request: req-abc123
```

The `--command` value is redacted client-side for common secret
patterns (`sk-…`, `ghp_…`, `xoxb-…`, `AKIA…`, high-entropy base64)
and re-redacted server-side. Length caps: `--agent` ≤128, `--model`
≤64, `--tool` ≤64, `--command` ≤1024, `--summary` ≤256.

> ⚠️ **Security boundary**: these fields are operator-visible context,
> NOT authenticators. A compromised agent could lie in any of them.
> Authorization continues to trust the cryptographic identity (client
> signature, peer IP, registered machine fingerprint). See
> [`docs/SECURITY.md`](SECURITY.md) §6.

---

## 7. End-to-end example

A runnable program demonstrating Snapshot + Me + Watch in one place
lives at [`examples/agent/`](../examples/agent/). Use it as a starting
template:

```bash
cd examples/agent
go run .
```

---

## 8. Versioning + stability

- `pkg/client` is **v1**. Breaking changes follow semantic-versioning
  rules at the module level.
- Wire formats (supervisor status JSON, `/me` response, `/claim`
  request) extend by adding new optional fields. Older SDK builds
  silently drop unknown fields; older servers ignore unknown wire
  fields (within the documented JSON shape).
- `SnapshotRaw()` gives you the raw socket bytes when you want
  forward-compatibility with fields the SDK doesn't yet know about.
- `Event.Type` is a string enum — future events MAY be added. Code
  defensively with a `default:` branch in your `switch`.

---

## 9. What's NOT in the SDK (yet)

- **In-process `/claim`**. The SDK today exposes `Me()` (read-only)
  but does not yet provide a typed `Claim()` that performs the full
  request + receives the JWT in-process. Use `hush request --exec`
  for now.
- **`/s/{name}` secret fetch with ECIES decrypt**. The supervisor
  handles this on the agent's behalf; standalone agents that need it
  should currently exec the CLI.
- **Python / TypeScript bindings**. Go SDK first. Bindings can come
  later via a thin wrapper or a separate gRPC surface.

These are tracked as follow-ups to the agent-integration work.

---

## 10. Cross-references

- [`pkg/client/README.md`](../pkg/client/README.md) — surface reference
- [`docs/API.md`](API.md) — wire format including `/me` schema
- [`docs/DAEMONS.md`](DAEMONS.md) — supervisor lifecycle scenarios
- [`docs/SECURITY.md`](SECURITY.md) — threat model + residual risks
- [`docs/OPERATIONS.md`](OPERATIONS.md) — day-to-day operator runbook
