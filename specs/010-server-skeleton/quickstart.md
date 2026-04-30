# Quickstart — wiring `internal/server`

How `cmd/hush serve` (SDD-14) — the only production caller —
constructs and runs the chassis.

---

## Wiring (production, ~30 lines)

```go
package main // (illustrative; SDD-14 owns the real wiring)

import (
    "context"
    "fmt"
    "log/slog"
    "net/netip"
    "os"
    "os/signal"
    "sync/atomic"
    "syscall"

    "github.com/mrz1836/hush/internal/config"
    "github.com/mrz1836/hush/internal/discord"
    "github.com/mrz1836/hush/internal/logging"
    "github.com/mrz1836/hush/internal/server"
    "github.com/mrz1836/hush/internal/token"
    "github.com/mrz1836/hush/internal/vault"
    "github.com/mrz1836/hush/internal/vault/securebytes"
)

func runServe(ctx context.Context, cfgPath string, vaultKey *securebytes.SecureBytes) error {
    cfg, err := config.LoadServer(ctx, cfgPath)
    if err != nil { return fmt.Errorf("config: %w", err) }
    if err := cfg.Validate(); err != nil { return fmt.Errorf("config: %w", err) }

    logger := logging.New(logging.Options{Level: slog.LevelInfo})

    // initial vault load — chassis takes the *atomic.Pointer, not the Store
    initial, err := vault.Load(ctx, cfg.VaultPath(), vaultKey)
    if err != nil { return fmt.Errorf("vault load: %w", err) }
    var vaultPtr atomic.Pointer[vault.Store]
    vaultPtr.Store(&initial)

    tokenStore := token.NewStore() // SDD-07
    botApprover, err := discord.NewBotApprover(ctx, cfg, logger) // SDD-11
    if err != nil { return fmt.Errorf("approver: %w", err) }
    audit, err := discord.NewAuditWriter(ctx, cfg, logger) // SDD-13 / future
    if err != nil { return fmt.Errorf("audit: %w", err) }

    srv, err := server.New(server.Deps{
        Cfg:         cfg,
        VaultPtr:    &vaultPtr,
        TokenStore:  tokenStore,
        Approver:    botApprover,
        Logger:      logger,
        AuditWriter: audit,
        // Clock and ClockSyncProbe default to host implementations
    })
    if err != nil { return fmt.Errorf("server new: %w", err) }

    // SDD-12 / SDD-13 mount their handlers BEFORE Run is called
    if err := srv.Mount("POST", "/claim", claimHandler); err != nil { return err }
    if err := srv.Mount("GET",  "/s/{name}", secretHandler); err != nil { return err }
    if err := srv.Mount("POST", "/revoke/{jti}", revokeHandler); err != nil { return err }
    if err := srv.Mount("GET",  "/hz", healthHandler); err != nil { return err }

    return srv.Run(ctx) // blocks until ctx cancels; runs startup checks first
}

func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    // (passphrase + vault-key derivation omitted — see SDD-14 / SDD-04)
    vaultKey := mustDeriveVaultKey()

    if err := runServe(ctx, "/etc/hush/server.toml", vaultKey); err != nil {
        os.Stderr.WriteString(err.Error() + "\n")
        os.Exit(1) // SDD-14 maps sentinel errors to exit codes
    }
}
```

---

## Lifecycle ordering

`server.New` performs no I/O — only nil-checks and field
assignment (FR-027). Every blocking step happens inside `Run`:

1. **Startup checks** run in order: `clock_sync → file_modes →
   tailscale_bind → state_dir`. First failure exits early.
2. **Route mounts** registered via `Mount` are bound to the
   `*http.ServeMux` (the pre-startup `Mount` calls only
   captured the (method, path, handler) tuples).
3. **SIGHUP signal handler** is installed via
   `signal.Notify(sigCh, syscall.SIGHUP)`.
4. **HTTP listener** binds to `cfg.Server.ListenAddr` and
   `httpServer.Serve` runs in a goroutine.
5. `Run` blocks on a select between the SIGHUP loop and
   `ctx.Done()`.
6. On `ctx.Done()`: `shuttingDown.Store(true)`,
   `httpServer.Shutdown(shutdownCtx)`, then `drainWG.Wait()`,
   then return.

---

## Reload (operator-facing)

Operator rotates a secret on the vault host:

```bash
hush secret rotate ANTHROPIC_API_KEY  # SDD-15: writes new vault file, sends SIGHUP
```

Server log:

```json
{"level":"INFO","msg":"vault reload","path":"/var/hush/secrets.vault"}
{"level":"INFO","msg":"vault swap complete","drain_window":"30s"}
{"level":"INFO","msg":"vault destroyed","jti":"<after drain>"}
```

In-flight `/s/{name}` requests that captured the old vault before
the swap finish with the old value. New requests after the swap
see the rotated value.

---

## Testing locally without Tailscale

The chassis refuses to start without a Tailscale CGNAT bind. To
run unit tests on a host without Tailscale, the test injects a
fake `interfaceLister` (see `contracts/startup-checks.md`). The
integration test (`//go:build integration`) requires a real
Tailscale interface; CI runs it on a tagged Tailscale host.

For local exploration:

```bash
# Force the chassis to skip checks (TEST ONLY — do not ship)
HUSH_TEST_SKIP_STARTUP_CHECKS=1 go run ./cmd/hush serve --config testdata/dev.toml
```

The env var is honoured ONLY when `go test` is the entry point —
the production binary refuses to read it. (Implementation: a
`testbuildtag.go` file with `//go:build dev` exposes the env var
override; release builds exclude it.)

---

## Common errors

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `server: startup: clock unsynchronised` | NTP daemon not running or drift > 60 s | `sudo systemsetup -setusingnetworktime on` (darwin) / `timedatectl set-ntp true` (linux) |
| `server: startup: file mode laxer than 0600/0700` | a config file in `~/.hush/` has loose perms | `chmod 600 ~/.hush/<file>` |
| `server: startup: listen address not on Tailscale CGNAT` | Tailscale not running, or wrong IP in config | `tailscale up`; check `tailscale ip -4`; update `[server].listen_addr` |
| `server: startup: state directory missing or unsafe` | `~/.hush/` does not exist or is owned by another user | `mkdir -m 0700 ~/.hush && chown $USER ~/.hush` |
| `server: reload: vault decrypt failed` | vault file replaced under a different passphrase | restart the server with the matching keychain entry |

---

## What this chunk does NOT do

- Implement `/claim`, `/s/{name}`, `/revoke/{jti}`, or `/hz`
  handler bodies — those are SDD-12 / SDD-13.
- Wire Discord — SDD-11.
- Run the cobra CLI — SDD-14.
- Implement the audit log's hash chain or ECDSA signing — SDD-13.
- Define `vault.Store`, `token.Store`, or `securebytes.SecureBytes` —
  SDD-03, SDD-07, SDD-02.

The chassis is the chassis. Everything else mounts on it.
