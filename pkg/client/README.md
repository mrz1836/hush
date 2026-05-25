# pkg/client

Public Go SDK for interacting with a running `hush` supervisor.

```go
import "github.com/mrz1836/hush/pkg/client"
```

## Quick start

```go
sup := client.NewSupervisorStatus("/var/run/hush/supervise-hermes.sock")
defer sup.Close()

ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()

status, err := sup.Snapshot(ctx)
if err != nil {
    // errors.Is(err, client.ErrSocketUnavailable) is true when the
    // supervisor is not running or the socket is unreachable.
    log.Fatal(err)
}
fmt.Println("expires at:", status.SessionExpiresAt)
```

## Surface (v1)

### Supervisor status — local Unix socket

| Method | Purpose |
|---|---|
| `NewSupervisorStatus(path)` | Construct a client bound to a supervisor's status socket. |
| `Snapshot(ctx)` | Fetch the typed status document. |
| `SnapshotRaw(ctx)` | Fetch the raw JSON bytes (forward-compatible pass-through). |
| `Refresh(ctx)` | Ask the supervisor to refill and restart its child immediately. |
| `Watch(ctx, WatchOptions)` | Stream lifecycle `Event`s (state changes, scope-health changes, session renewals, pre-expiry warnings) so an agent can wind down gracefully. |
| `Close()` | Release resources. No-op in v1; reserved for future use. |

#### `Watch()` event types

| Type | When it fires |
|---|---|
| `EventInitial` | Once at subscribe time, carrying the current snapshot. |
| `EventStateChange` | Supervisor state transitions (e.g. `running` → `awaiting-approval`). |
| `EventScopeHealthChange` | Healthy / stale scope set changes (rotation or validator flip). |
| `EventSessionRenewed` | New `SessionJTI` observed — resets the expiry-soon threshold tracker. |
| `EventExpiresSoon` | A configured `ExpiryThresholds` boundary is crossed (default `{15m, 5m, 1m, 30s}`). The `Threshold` field identifies which one. |
| `EventError` | A poll round-trip failed (transient). The watch continues; channel is NOT closed. |

`Watch()` is the recommended way for an agent to react to credential-rotation timing — instead of polling `Snapshot()` and computing diffs yourself, subscribe and switch on the event type. Each subscription spawns one goroutine; cancel the context to terminate it cleanly.

### Capability + freshness — vault HTTP

| Method | Purpose |
|---|---|
| `Me(ctx, MeRequest)` | Query the vault server's `/me` endpoint. Signed request, no Discord approval. Returns available scope names, server version, and (when a Bearer JWT accompanies the request) the current session's `jti`, `expires_at`, `scopes`, `max_uses`, `session_type`. |

`Me()` is safe to poll — it does NOT consume a JWT use and never
triggers an approval prompt. Use it before issuing a fresh `/claim` to
batch multiple scopes into a single human approval.

## Typed errors

- `ErrSocketUnavailable` — supervisor not running, socket missing, HTTP transport failure, or context cancelled mid-round-trip.
- `ErrInvalidResponse` — server responded but the payload could not be parsed (or had a non-2xx status that isn't an auth failure).
- `ErrRefreshDenied` — supervisor accepted the refresh request but refused (vault unreachable, window closed, etc.).
- `ErrUnauthenticated` — `/me` returned 401/403 (bad signature, unknown fingerprint, replayed nonce, stale timestamp, or non-Tailscale source IP).

Compare with `errors.Is`.

## Stability

All exports are part of hush's v1 public API. Wire-format additions appear as new optional fields with `omitempty` so existing SDK builds keep working when talking to newer servers. Use `SnapshotRaw` when you need to forward fields the SDK does not yet know about.

See `docs/AGENT-INTEGRATION.md` (added in a later PR) for the complete agent integration guide.
